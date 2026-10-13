package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
)

// fakeGraph 는 그래프에 있는 문서, 문서별 이웃, entity 목록을 미리 정해 둔 그래프다.
type fakeGraph struct {
	docs      map[string]bool
	neighbors map[string][]graph.Neighbor
	entities  []graph.EntityDocs
}

func (fakeGraph) Ping(context.Context) error                 { return nil }
func (fakeGraph) Replace(context.Context, graph.Graph) error { return nil }
func (fakeGraph) Close(context.Context) error                { return nil }
func (fakeGraph) Stats(context.Context, string) (graph.Stats, error) {
	return graph.Stats{}, nil
}
func (f fakeGraph) Neighbors(_ context.Context, _, path string, _ int) ([]graph.Neighbor, error) {
	return f.neighbors[path], nil
}
func (f fakeGraph) HasDoc(_ context.Context, _, path string) (bool, error) { return f.docs[path], nil }
func (f fakeGraph) Entities(context.Context, string, string) ([]graph.EntityDocs, error) {
	return f.entities, nil
}

const (
	limitsV2 = "docs/knowledge/limits-v2.md"
	lonely   = "docs/specs/lonely.md"
)

// graphHandlers 는 limits-v2 가 구판 limits 를 밀어낸 그래프와 이어진 것이 없는 문서 하나를 가진 Handlers 다.
func graphHandlers(t *testing.T) (*Handlers, string) {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{limitsV2, lonely} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := fakeGraph{
		docs: map[string]bool{limitsV2: true, lonely: true},
		neighbors: map[string][]graph.Neighbor{limitsV2: {
			{Path: "docs/knowledge/limits.md", Status: graph.StatusSuperseded, Hops: 1, Rels: []string{"SUPERSEDES"}},
		}},
	}
	return NewHandlers(Deps{Config: config.Config{MetaRepoRoot: root, Workspace: "ws"}, Graph: g}), root
}

// RUN-13: related_docs 는 ingest_path 처럼 경로를 맞춘다 — './docs/…', meta-repo 안의 절대 경로, 앞뒤 공백.
// 예전에는 글자 그대로 그래프에 물어 "이어진 문서가 없다" 고 답했다.
func TestRelatedDocsNormalizesTheTypedPath(t *testing.T) {
	h, root := graphHandlers(t)
	for _, typed := range []string{limitsV2, "./" + limitsV2, filepath.Join(root, limitsV2), " " + limitsV2 + " "} {
		out, err := h.Related(context.Background(), RelatedInput{Path: typed, Depth: 1})

		if err != nil || !strings.HasPrefix(out, limitsV2+" 와 이어진 문서 1건") || !strings.Contains(out, "docs/knowledge/limits.md (superseded)") {
			t.Errorf("related_docs %q = %q %v", typed, out, err)
		}
	}
	if _, err := h.Related(context.Background(), RelatedInput{Path: "../outside.md"}); err == nil {
		t.Error("meta-repo 밖의 경로는 거부한다")
	}
}

// RUN-13: 그래프에 없는 문서와, 그래프에 있지만 이어진 것이 없는 문서를 같은 말로 답하지 않는다.
func TestRelatedDocsTellsMissingFromUnconnected(t *testing.T) {
	h, _ := graphHandlers(t)
	ctx := context.Background()

	missing, err := h.Related(ctx, RelatedInput{Path: "docs/nope/none.md"})
	if err != nil || !strings.Contains(missing, "그래프에 없는 문서입니다") || !strings.Contains(missing, "source_path") {
		t.Errorf("그래프에 없는 문서 = %q %v", missing, err)
	}
	alone, err := h.Related(ctx, RelatedInput{Path: lonely})
	if err != nil || !strings.Contains(alone, "이어진 문서가 없습니다") || strings.Contains(alone, "그래프에 없는") {
		t.Errorf("이어진 것이 없는 문서 = %q %v", alone, err)
	}
}

// SCN-G7-1: docs_by_entity 는 기본 검색에서 숨는 문서(밀린·deprecate 된)에 related_docs 와 같은 상태 이름을 붙인다.
// 현행(hot) 문서는 그대로 둔다.
func TestDocsByEntityMarksHiddenDocuments(t *testing.T) {
	g := fakeGraph{entities: []graph.EntityDocs{{Name: "alpha-api", Type: "repo", Docs: []graph.DocRef{
		{Path: "docs/knowledge/status-codes-v2.md", Status: graph.StatusHot},
		{Path: "docs/knowledge/status-codes.md", Status: graph.StatusSuperseded},
		{Path: "docs/knowledge/status-guess.md", Status: graph.StatusDeprecated},
	}}}}
	h := NewHandlers(Deps{Config: config.Config{Workspace: "ws"}, Graph: g})

	out, err := h.Entities(context.Background(), EntityInput{Entity: "alpha-api"})

	want := "repo:alpha-api — docs/knowledge/status-codes-v2.md, docs/knowledge/status-codes.md (superseded), docs/knowledge/status-guess.md (deprecated)"
	if err != nil || out != want {
		t.Errorf("docs_by_entity\n got  %q %v\n want %q", out, err, want)
	}
}
