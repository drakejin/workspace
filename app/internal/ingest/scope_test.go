package ingest

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// scopedOS 는 요청 경로를 적고, _settings 에는 이 팀·다른 팀 인덱스를, 문서 팀 집계에는 이 팀을 돌려준다.
type scopedOS struct {
	mu    sync.Mutex
	paths []string
}

func (o *scopedOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	o.mu.Lock()
	o.paths = append(o.paths, path)
	o.mu.Unlock()
	var v any = map[string]any{}
	switch {
	case strings.HasSuffix(path, "/_settings"):
		v = map[string]any{
			"mem-" + unitTeam + "-2026.10":   map[string]any{"settings": map[string]any{}},
			"mem-" + unitTeam + "-knowledge": map[string]any{"settings": map[string]any{}},
			"mem-other-2026.10":              map[string]any{"settings": map[string]any{}},
		}
	case strings.HasSuffix(path, "/_search"):
		v = map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": []any{map[string]any{"key": unitTeam}}}}}
	case strings.HasSuffix(path, "/_delete_by_query"):
		v = map[string]any{"deleted": 1}
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (o *scopedOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

func (o *scopedOS) deletes() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, p := range o.paths {
		if strings.HasSuffix(p, "/_delete_by_query") {
			out = append(out, p)
		}
	}
	return out
}

const unitTeamIndices = "/mem-" + unitTeam + "-2026.10,mem-" + unitTeam + "-knowledge/_delete_by_query"

// 문서를 지우거나 다시 쓸 때 옛 청크를 지우는 요청은 그 문서가 든 팀의 인덱스에만 간다 — mem-* 전체에 보내면 같은 스택의 다른
// workspace 가 그 사이 인덱스를 지울 때 search_context_missing 으로 실패한다.
func TestDeletesAreScopedToTheDocumentsTeams(t *testing.T) {
	ctx := context.Background()
	t.Run("RemoveFile", func(t *testing.T) {
		os := &scopedOS{}
		ix := New(unitDeps(t.TempDir(), os, cold.NewLocalStore(t.TempDir())))
		if _, err := ix.RemoveFile(ctx, "docs/specs/a.md", nil); err != nil {
			t.Fatal(err)
		}
		if got := os.deletes(); len(got) != 1 || got[0] != unitTeamIndices {
			t.Errorf("delete paths = %v, want [%s]", got, unitTeamIndices)
		}
	})
	t.Run("replaceChunks", func(t *testing.T) {
		os := &scopedOS{}
		ix := New(unitDeps(t.TempDir(), os, cold.NewLocalStore(t.TempDir()))).(*indexer)
		p := Prepared{Rel: "docs/specs/a.md", Built: document.Built{Meta: document.Metadata{Team: unitTeam},
			Chunks: []model.Chunk{{ID: "c0", Team: unitTeam, SourcePath: "docs/specs/a.md"}}}}
		if err := ix.replaceChunks(ctx, p, "mem-"+unitTeam+"-2026.10"); err != nil {
			t.Fatal(err)
		}
		if got := os.deletes(); len(got) != 1 || got[0] != unitTeamIndices {
			t.Errorf("delete paths = %v, want [%s]", got, unitTeamIndices)
		}
	})
}
