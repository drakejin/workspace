package graph

import (
	"reflect"
	"testing"

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
	})
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
	if !reflect.DeepEqual(Build("ws", nil), Graph{Workspace: "ws"}) {
		t.Error("빈 입력")
	}
}
