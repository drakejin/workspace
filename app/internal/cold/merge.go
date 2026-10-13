package cold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// ErrReopened 는 cold 로 간 달의 인덱스가 hot 에 다시 생겼다는 뜻이다 (그 달 날짜의 문서가 늦게 들어왔다).
// 그 인덱스는 데몬·tier 가 관리한다 — cold 문서를 섞으면 데몬이 파일 없는 문서로 보고 cold store 청크까지 지우고,
// 그 자리에서 스냅샷을 다시 쓰면 hot 내용만 남아 옛 스냅샷의 문서를 잃는다.
var ErrReopened = errors.New("cold 로 간 달의 인덱스가 hot 에 다시 열려 있습니다")

// Reopened 는 index 에 대한 ErrReopened 다.
func Reopened(index string) error {
	return fmt.Errorf("%w: %s — 다음 tier 가 이 달을 다시 cold 로 보낸 뒤에 하세요 (scripts/memory/run.sh tier)", ErrReopened, index)
}

// mergePrefix 는 옛 스냅샷을 잠깐 되살리는 임시 인덱스 이름의 머리다. mem-* 가 아니라 검색·데몬·ISM 이 보지 않는다.
const mergePrefix = "cold-merge-"

// mergeBatch 는 한 번의 _reindex 로 옮기는 문서 경로 수다 (terms 질의 상한보다 한참 작게).
const mergeBatch = 1000

// OpenState 는 월별 인덱스가 지금 열려 있는 모양이다. restored 는 복원본(auto_manage=false), hot 은 데몬·tier 가
// 관리하는 인덱스다. 둘 다 아니면 cold 에만 있다.
func OpenState(ctx context.Context, c opensearch.Client, index string) (restored, hot bool, err error) {
	settings, err := opensearch.Settings(ctx, c, index)
	if err != nil {
		return false, false, fmt.Errorf("settings %s: %w", index, err)
	}
	s, open := settings[index]
	if !open {
		return false, false, nil
	}
	unmanaged := opensearch.IsUnmanaged(s)
	return unmanaged, !unmanaged, nil
}

// RestoreAll 은 index 를 담은 스냅샷 전부(snapshots, 오래된 것부터)를 index 하나로 합친다 — 한 달을 tier 한 뒤 그 달
// 날짜의 문서가 늦게 들어오면 같은 인덱스의 스냅샷이 여럿이 된다. 같은 문서(source_path)는 더 최근 것을 쓴다.
// index 가 복원본으로 열려 있으면 그 내용이 가장 최근이라 없는 문서만 더하고, 닫혀 있으면 가장 최근 스냅샷을 index 로 되살린다.
// 같은 이름의 hot 인덱스가 다시 생겼으면 ErrReopened.
func RestoreAll(ctx context.Context, c opensearch.Client, cfg config.Config, index string, snapshots []string, withAliases bool) error {
	restored, hot, err := OpenState(ctx, c, index)
	if err != nil {
		return err
	}
	if hot {
		return Reopened(index)
	}
	rest := snapshots
	if !restored {
		if len(rest) == 0 {
			return fmt.Errorf("%s 를 담은 스냅샷이 없습니다", index)
		}
		if err := RestoreIndex(ctx, c, cfg, index, rest[len(rest)-1], withAliases); err != nil {
			return err
		}
		rest = rest[:len(rest)-1]
	}
	for i := len(rest) - 1; i >= 0; i-- {
		if err := mergeSnapshot(ctx, c, cfg, index, rest[i]); err != nil {
			return err
		}
	}
	return nil
}

// mergeSnapshot 은 snapshot 의 index 를 임시 이름으로 되살려, index 에 없는 문서만 옮기고 임시 인덱스를 지운다.
func mergeSnapshot(ctx context.Context, c opensearch.Client, cfg config.Config, index, snapshot string) (err error) {
	tmp := fmt.Sprintf("%s%s-%09d", mergePrefix, index, time.Now().Nanosecond())
	body := restoreBody(index, false)
	body["rename_pattern"] = "(.+)"
	body["rename_replacement"] = tmp
	if err := restoreSnapshot(ctx, c, cfg, snapshot, body); err != nil {
		return fmt.Errorf("merge %s from %s: %w", index, snapshot, err)
	}
	defer func() {
		if derr := deleteIndex(context.WithoutCancel(ctx), c, tmp); derr != nil && err == nil {
			err = derr
		}
	}()
	have, err := DocPaths(ctx, c, index)
	if err != nil {
		return err
	}
	theirs, err := DocPaths(ctx, c, tmp)
	if err != nil {
		return err
	}
	return copyDocs(ctx, c, tmp, index, without(theirs, have))
}

// copyDocs 는 from 의 paths 문서(모든 청크)를 같은 _id 로 to 에 옮긴다.
func copyDocs(ctx context.Context, c opensearch.Client, from, to string, paths []string) error {
	for start := 0; start < len(paths); start += mergeBatch {
		batch := paths[start:min(start+mergeBatch, len(paths))]
		var out struct {
			Failures []json.RawMessage `json:"failures"`
		}
		body := map[string]any{
			"source": map[string]any{"index": from, "query": map[string]any{"terms": map[string]any{"source_path": batch}}},
			"dest":   map[string]any{"index": to},
		}
		if err := c.Do(ctx, http.MethodPost, "/_reindex", body, url.Values{"refresh": {"true"}, "wait_for_completion": {"true"}}, &out); err != nil {
			return fmt.Errorf("merge %s → %s: %w", from, to, err)
		}
		if len(out.Failures) > 0 {
			return fmt.Errorf("merge %s → %s: %d건 실패: %s", from, to, len(out.Failures), out.Failures[0])
		}
	}
	return nil
}

// DocPaths 는 index 에 든 문서 경로들이다 (청크 0 기준. 팀 하나는 workspace 하나에만 속해 경로로 충분하다).
func DocPaths(ctx context.Context, c opensearch.Client, index string) (map[string]bool, error) {
	paths := map[string]bool{}
	query := map[string]any{"term": map[string]any{"chunk_index": 0}}
	err := opensearch.Scan(ctx, c, index, query, []string{"source_path"}, opensearch.ChunkSort,
		func(h opensearch.Hit[model.Chunk]) error {
			paths[h.Source.SourcePath] = true
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("doc paths %s: %w", index, err)
	}
	return paths, nil
}

// MissingDocs 는 want 중 index 에 없는 문서 경로다 (정렬).
func MissingDocs(ctx context.Context, c opensearch.Client, index string, want map[string]bool) ([]string, error) {
	have, err := DocPaths(ctx, c, index)
	if err != nil {
		return nil, err
	}
	return without(want, have), nil
}

// without 은 a 에만 있는 경로다 (정렬).
func without(a, b map[string]bool) []string {
	var out []string
	for p := range a {
		if !b[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
