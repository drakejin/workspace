package opensearch

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/naming"
)

// Bootstrap 은 템플릿·매핑·스냅샷 repository·ISM 정책을 멱등하게 맞춘다.
// 여러 프로세스(데몬·MCP·CLI)가 동시에 불러도 안전하다.
func Bootstrap(ctx context.Context, c Client, cfg config.Config) error {
	if err := c.Do(ctx, http.MethodPut, "/_index_template/"+TemplateName, IndexTemplate(), nil, nil); err != nil {
		return err
	}
	// 템플릿은 새 인덱스에만 붙는다. 이미 있는 인덱스에는 새 필드를 더한다
	if err := c.Do(ctx, http.MethodPut, "/"+naming.AllPattern+"/_mapping", map[string]any{"properties": Properties()}, Lenient(), nil); err != nil {
		return err
	}
	if err := ensureSnapshotRepository(ctx, c, cfg.Snapshot); err != nil {
		return err
	}
	if err := ensurePolicy(ctx, c, HotColdPolicy, HotColdPolicyBody(cfg.HotDays, cfg.Snapshot.Repository)); err != nil {
		return err
	}
	return ensurePolicy(ctx, c, KeepPolicy, KeepPolicyBody())
}

func ensureSnapshotRepository(ctx context.Context, c Client, s config.Snapshot) error {
	body := map[string]any{"type": "fs", "settings": map[string]any{"location": s.FSLocation}}
	if s.Type == "s3" {
		body = map[string]any{"type": "s3", "settings": map[string]any{"bucket": s.S3Bucket, "base_path": "snapshots"}}
	}
	return c.Do(ctx, http.MethodPut, "/_snapshot/"+s.Repository, body, nil, nil)
}

func ensurePolicy(ctx context.Context, c Client, id string, body map[string]any) error {
	path := "/_plugins/_ism/policies/" + id
	var existing struct {
		SeqNo       int64 `json:"_seq_no"`
		PrimaryTerm int64 `json:"_primary_term"`
	}
	err := c.Do(ctx, http.MethodGet, path, nil, nil, &existing)
	switch {
	case err == nil:
		err = c.Do(ctx, http.MethodPut, path, body, url.Values{
			"if_seq_no":       {strconv.FormatInt(existing.SeqNo, 10)},
			"if_primary_term": {strconv.FormatInt(existing.PrimaryTerm, 10)},
		}, nil)
	case IsStatus(err, http.StatusNotFound):
		err = c.Do(ctx, http.MethodPut, path, body, nil, nil)
	default:
		return err
	}
	// 409 = 다른 프로세스가 방금 같은 정책을 썼다. 내용이 같으니 성공으로 본다
	if IsStatus(err, http.StatusConflict) {
		return nil
	}
	return err
}

// EnsureIndex 는 인덱스가 없으면 팀 hot alias 를 붙여 만든다.
func EnsureIndex(ctx context.Context, c Client, index, team string) error {
	body := map[string]any{"aliases": map[string]any{naming.HotAlias(team): map[string]any{}}}
	err := c.Do(ctx, http.MethodPut, "/"+index, body, nil, nil)
	if HasType(err, "resource_already_exists_exception") {
		return nil
	}
	return err
}
