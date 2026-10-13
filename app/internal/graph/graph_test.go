package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
)

func TestBuild(t *testing.T) {
	g := Build("ws", []Input{
		{Path: "docs/knowledge/v2.md", Team: "t", Kind: "knowledge", Entities: []string{"service:order-api", "멱등"},
			Links: []model.Link{
				{Type: model.LinkSupersedes, To: "docs/knowledge/v1.md"},
				{Type: model.LinkDerivedFrom, To: "docs/specs/a.md", Why: "근거"},
				{Type: model.LinkLinksTo, To: "docs/missing.md"},
				{Type: model.LinkLinksTo, To: "docs/knowledge/v2.md"},
			}},
		{Path: "docs/knowledge/v1.md", Team: "t", Kind: "knowledge", SupersededBy: "docs/knowledge/v2.md"},
		{Path: "docs/specs/a.md", Team: "t", Repo: "order-api", Deprecated: true},
	}, nil)
	status := map[string]string{}
	for _, d := range g.Docs {
		status[d.Path] = d.Status
	}
	want := map[string]string{"docs/knowledge/v2.md": StatusHot, "docs/knowledge/v1.md": StatusSuperseded, "docs/specs/a.md": StatusDeprecated, "docs/missing.md": StatusUnindexed}
	if !reflect.DeepEqual(status, want) {
		t.Errorf("status = %v", status)
	}
	types := map[string]int{}
	for _, e := range g.Edges {
		types[e.Type]++
	}
	wantTypes := map[string]int{"SUPERSEDES": 1, "DERIVED_FROM": 1, "LINKS_TO": 1, RelMentions: 2, RelInRepo: 1}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Errorf("edges = %v", types)
	}
	for _, e := range g.Edges {
		if e.Type == "SUPERSEDES" && (e.From != Key("ws", LabelDoc, "docs/knowledge/v2.md") || e.To != Key("ws", LabelDoc, "docs/knowledge/v1.md")) {
			t.Errorf("supersedes 방향 = %+v", e)
		}
	}
	if len(g.Entities) != 2 || g.Entities[0].Type == "" || len(g.Repos) != 1 {
		t.Errorf("entities=%+v repos=%+v", g.Entities, g.Repos)
	}
	if !reflect.DeepEqual(Build("ws", nil, nil), Graph{Workspace: "ws"}) {
		t.Error("빈 입력")
	}
}

// DOC-1: 색인 범위 밖이지만 디스크에 있는 링크 대상(scenarios/·.claude/·examples/)은 unindexed(깨진 링크 후보)가 아니라
// out-of-scope 다 — README §4.2 의 깨진 링크 질의({status: 'unindexed'})에 나오지 않는다. 디스크에 없는 대상은 그대로 unindexed.
func TestFromHotTellsOutOfScopeFilesFromMissingOnes(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# 안내\n[시나리오](./scenarios/SCENARIOS.md) · [compaction](./.claude/commands/compaction.md)\n"+
		"[없음](./scenarios/missing.md) · [cold](./docs/specs/old.md)\n")
	write("scenarios/SCENARIOS.md", "# 시나리오\n")
	write(".claude/commands/compaction.md", "# compaction\n")
	docs := map[string]ingest.IndexedDoc{"README.md": {SourcePath: "README.md", Team: "t", Kind: model.KindKnowledge}}

	g := FromHot(root, "ws", docs)

	status := map[string]string{}
	for _, d := range g.Docs {
		status[d.Path] = d.Status
	}
	want := map[string]string{
		"README.md":                      StatusHot,
		"scenarios/SCENARIOS.md":         StatusOutOfScope,
		".claude/commands/compaction.md": StatusOutOfScope,
		"scenarios/missing.md":           StatusUnindexed,
		"docs/specs/old.md":              StatusUnindexed,
	}
	if !reflect.DeepEqual(status, want) {
		t.Errorf("status\n got  %v\n want %v", status, want)
	}
}
