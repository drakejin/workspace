package opensearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/naming"
)

// Bootstrap 은 템플릿·매핑·스냅샷 repository·ISM 정책·org alias 를 멱등하게 맞춘다.
// 여러 프로세스(데몬·MCP·CLI)가 동시에 불러도 안전하다.
func Bootstrap(ctx context.Context, c Client, cfg config.Config) error {
	if err := c.Do(ctx, http.MethodPut, "/_index_template/"+TemplateName, IndexTemplate(), nil, nil); err != nil {
		return fmt.Errorf("index template: %w", err)
	}
	// 템플릿은 새 인덱스에만 붙는다. 이미 있는 인덱스에는 새 필드를 더한다 (추가만이라 안전)
	if err := c.Do(ctx, http.MethodPut, "/"+naming.AllPattern+"/_mapping", map[string]any{"properties": Properties()}, Lenient(), nil); err != nil {
		return fmt.Errorf("mapping: %w", err)
	}
	if err := ensureSnapshotRepository(ctx, c, cfg.Snapshot); err != nil {
		return fmt.Errorf("snapshot repository %s: %w", cfg.Snapshot.Repository, err)
	}
	if err := ensurePolicy(ctx, c, HotColdPolicy, HotColdPolicyBody(cfg.HotDays, cfg.Snapshot.Repository)); err != nil {
		return fmt.Errorf("ISM %s: %w", HotColdPolicy, err)
	}
	if err := ensurePolicy(ctx, c, KeepPolicy, KeepPolicyBody()); err != nil {
		return fmt.Errorf("ISM %s: %w", KeepPolicy, err)
	}
	if err := SyncOrgAliases(ctx, c, cfg); err != nil {
		return fmt.Errorf("org alias: %w", err)
	}
	return nil
}

func ensureSnapshotRepository(ctx context.Context, c Client, s config.Snapshot) error {
	want := map[string]any{"type": "fs", "settings": map[string]any{"location": s.FSLocation}}
	if s.Type == "s3" {
		want = map[string]any{"type": "s3", "settings": map[string]any{"bucket": s.S3Bucket, "base_path": "snapshots"}}
	}
	// 이미 같은 설정이면 건드리지 않는다 — 모든 CLI·MCP 호출이 bootstrap 하므로, 다른 프로세스가 스냅샷·복원하는 동안
	// PUT 하면 OpenSearch 가 "currently used" 로 거부해 읽기 전용 검색까지 실패한다
	var current map[string]struct {
		Type     string         `json:"type"`
		Settings map[string]any `json:"settings"`
	}
	err := c.Do(ctx, http.MethodGet, "/_snapshot/"+s.Repository, nil, nil, &current)
	if err != nil && !IsStatus(err, http.StatusNotFound) {
		return err
	}
	if cur, ok := current[s.Repository]; ok && sameRepository(cur.Type, cur.Settings, want) {
		return nil
	}
	err = c.Do(ctx, http.MethodPut, "/_snapshot/"+s.Repository, want, nil, nil)
	var e *Error
	if errors.As(err, &e) && strings.Contains(e.Reason, "currently used") {
		return nil // 스냅샷·복원 중이라 지금은 못 바꾼다 — 있는 repository 를 쓰고 다음 bootstrap 이 맞춘다
	}
	return err
}

func sameRepository(typ string, settings map[string]any, want map[string]any) bool {
	if typ != want["type"] {
		return false
	}
	for k, v := range want["settings"].(map[string]any) {
		if fmt.Sprint(settings[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
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

// IndexAliases 는 새 인덱스에 붙일 alias 다: 팀 hot alias, 그리고 팀이 org 에 속하면 org alias.
func IndexAliases(team, org string) map[string]any {
	aliases := map[string]any{naming.HotAlias(team): map[string]any{}}
	if org != "" {
		aliases[naming.OrgAlias(org)] = map[string]any{}
	}
	return aliases
}

// EnsureIndex 는 인덱스가 없으면 alias 와 매핑을 붙여 만든다. 매핑은 템플릿에도 있지만 만들 때 한 번 더 준다 — 같은 클러스터의
// 다른 판 프로세스(업그레이드 중인 데몬 등)가 bootstrap 으로 템플릿을 옛 필드 구성으로 되돌려도, 이 판이 쓰는 필드가 strict
// 매핑에 거부되지 않게 (요청의 매핑은 템플릿의 매핑·설정과 합쳐진다).
func EnsureIndex(ctx context.Context, c Client, index, team, org string) error {
	body := map[string]any{"aliases": IndexAliases(team, org), "mappings": map[string]any{"properties": Properties()}}
	err := c.Do(ctx, http.MethodPut, "/"+index, body, nil, nil)
	if HasType(err, "resource_already_exists_exception") {
		return nil
	}
	return err
}

// SyncOrgAliases 는 이 workspace 팀들의 인덱스에 org alias 를 MEMORY_TEAM_ORGS 대로 맞춘다 (팀을 org 로 옮기거나 뺄 때).
// 다른 meta-repo 의 팀 인덱스는 건드리지 않는다 — 그쪽 설정은 그쪽 bootstrap 이 맞춘다.
func SyncOrgAliases(ctx context.Context, c Client, cfg config.Config) error {
	owned, err := WorkspaceTeams(ctx, c, cfg.Workspace)
	if err != nil {
		return err
	}
	var current map[string]struct {
		Aliases map[string]any `json:"aliases"`
	}
	err = c.Do(ctx, http.MethodGet, "/"+naming.AllPattern+"/_alias", nil, url.Values{"allow_no_indices": {"true"}}, &current)
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	indices := make([]string, 0, len(current))
	for index := range current {
		indices = append(indices, index)
	}
	sort.Strings(indices)
	var actions []any
	for _, index := range indices {
		idx, ok := naming.ParseIndex(index)
		if !ok || !owned[idx.Team] {
			continue
		}
		want := ""
		if org := cfg.OrgOf(idx.Team); org != "" {
			want = naming.OrgAlias(org)
		}
		for alias := range current[index].Aliases {
			if strings.HasPrefix(alias, naming.Prefix+"org-") && alias != want && alias != naming.HotAlias(idx.Team) {
				actions = append(actions, map[string]any{"remove": map[string]any{"index": index, "alias": alias}})
			}
		}
		if _, has := current[index].Aliases[want]; want != "" && !has {
			actions = append(actions, map[string]any{"add": map[string]any{"index": index, "alias": want}})
		}
	}
	if len(actions) == 0 {
		return nil
	}
	return c.Do(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, nil, nil)
}
