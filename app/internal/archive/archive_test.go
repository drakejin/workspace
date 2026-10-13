package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

type recordOS struct {
	mu    sync.Mutex
	paths []string
}

func (o *recordOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	o.mu.Lock()
	o.paths = append(o.paths, path)
	o.mu.Unlock()
	var v any = map[string]any{}
	if strings.HasSuffix(path, "/_settings") {
		v = map[string]any{
			"mem-ops-2026.07":   map[string]any{"settings": map[string]any{}},
			"mem-ops-x-2026.07": map[string]any{"settings": map[string]any{}},
			"mem-e1-2026.10":    map[string]any{"settings": map[string]any{}},
		}
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (o *recordOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// archive deprecate·undeprecate 의 _update_by_query 는 그 팀의 인덱스에만 간다 — 시나리오 K10 에서 mem-* 로 보낸 요청이
// 다른 workspace 가 그 사이 지운 인덱스 때문에 search_context_missing(404)로 실패했다 (문서는 이미 바뀌었는데 exit 1).
func TestDeprecateUpdatesOnlyTheTeamsIndices(t *testing.T) {
	ctx := context.Background()
	const team, path = "ops", "docs/repository/alpha-api/adr/0000-legacy-queue.md"
	store := cold.NewLocalStore(t.TempDir())
	chunk, _ := json.Marshal(model.Chunk{Team: team, SourcePath: path, DocType: model.DocADR, Kind: model.KindEpisodic,
		EventTime: "2026-07-05T00:00:00Z", EventDay: "2026-07-05"})
	if err := store.Put(ctx, cold.ChunksKey(team, path), string(chunk)+"\n"); err != nil {
		t.Fatal(err)
	}
	os := &recordOS{}
	s := &Service{OS: os, Cold: store, Now: time.Now,
		Config: config.Config{MetaRepoRoot: t.TempDir(), Workspace: "ws", DefaultTeam: team}}
	a := Audit{By: "test", Reason: "scope"}
	if _, err := s.Deprecate(ctx, team, path, "", a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undeprecate(ctx, team, path, a); err != nil {
		t.Fatal(err)
	}
	var updates []string
	for _, p := range os.paths {
		if strings.HasSuffix(p, "/_update_by_query") {
			updates = append(updates, p)
		}
	}
	want := "/mem-ops-2026.07/_update_by_query"
	if len(updates) != 2 || updates[0] != want || updates[1] != want {
		t.Errorf("update paths = %v, want twice %s", updates, want)
	}
}
