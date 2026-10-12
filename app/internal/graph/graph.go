// Package graph 는 knowledge graph 다 — 문서·entity·repo 를 노드로, 문서 사이 관계를 edge 로 Neo4j 에 둔다.
// 그래프는 파생물이다: hot 문서(색인), 파일의 링크, 판정 원장, 증류 캐시만으로 LLM 없이 다시 만든다.
package graph

import (
	"sort"
	"strings"

	"github.com/drakejin/workspace/app/internal/model"
)

// Node 종류.
const (
	LabelDoc    = "Doc"
	LabelEntity = "Entity"
	LabelRepo   = "Repo"
)

// Edge 종류 — model.LinkType 에 둘을 더한다.
const (
	RelMentions = "MENTIONS" // 문서 → entity (증류 산출)
	RelInRepo   = "IN_REPO"  // 문서 → 관리 대상 repository
)

// DocStatus 는 그래프 안 문서 노드의 상태다.
const (
	StatusHot        = "hot"        // 지금 색인에 있다
	StatusUnindexed  = "unindexed"  // 링크로만 알려졌다 (cold 로 갔거나, 오타거나, 색인 범위 밖)
	StatusSuperseded = "superseded" // 더 새 문서에 밀렸다
	StatusDeprecated = "deprecated" // 틀린 것으로 표시됐다
)

// Doc 은 문서 노드다.
type Doc struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Team    string `json:"team"`
	Repo    string `json:"repo"`
	Title   string `json:"title"`
	DocType string `json:"docType"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// Named 는 entity·repo 노드다.
type Named struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Edge 는 관계 하나다.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"`
	Why    string `json:"why"`
	Source string `json:"source"`
}

// Graph 는 workspace 하나의 그래프 전체다.
type Graph struct {
	Workspace string
	Docs      []Doc
	Entities  []Named
	Repos     []Named
	Edges     []Edge
}

// Input 은 그래프의 재료다 — 문서 하나.
type Input struct {
	Path         string
	Team         string
	Repo         string
	Title        string
	DocType      string
	Kind         string
	Summary      string
	Entities     []string
	SupersededBy string
	Deprecated   bool
	Links        []model.Link
}

// Key 는 workspace 안에서 유일한 노드 키다.
func Key(workspace, kind, name string) string { return workspace + "|" + kind + "|" + name }

// Build 는 문서 목록에서 그래프를 만든다. 순수 함수 — 같은 입력이면 같은 그래프.
func Build(workspace string, inputs []Input) Graph {
	g := Graph{Workspace: workspace}
	docKey := func(p string) string { return Key(workspace, LabelDoc, p) }
	hot := map[string]bool{}
	for _, in := range inputs {
		hot[in.Path] = true
	}
	unindexed := map[string]bool{}
	entities := map[string]Named{}
	repos := map[string]Named{}
	edges := map[string]Edge{}
	addEdge := func(e Edge) {
		k := e.From + "\x00" + e.Type + "\x00" + e.To
		if _, ok := edges[k]; !ok {
			edges[k] = e
		}
	}
	for _, in := range inputs {
		status := StatusHot
		switch {
		case in.Deprecated:
			status = StatusDeprecated
		case in.SupersededBy != "":
			status = StatusSuperseded
		}
		g.Docs = append(g.Docs, Doc{
			Key: docKey(in.Path), Path: in.Path, Team: in.Team, Repo: in.Repo, Title: in.Title,
			DocType: in.DocType, Kind: in.Kind, Status: status, Summary: in.Summary,
		})
		if in.SupersededBy != "" {
			addEdge(Edge{From: docKey(in.SupersededBy), To: docKey(in.Path), Type: string(model.LinkSupersedes), Why: "현행 판 → 밀린 판", Source: "converged"})
		}
		for _, l := range in.Links {
			if l.To == in.Path {
				continue
			}
			if !hot[l.To] {
				unindexed[l.To] = true
			}
			if l.Type == model.LinkSupersedes {
				continue // 선언은 수렴 결과(SupersededBy)로 이미 들어간다 — 대상이 없으면 아래 unindexed 노드만 남긴다
			}
			addEdge(Edge{From: docKey(in.Path), To: docKey(l.To), Type: string(l.Type), Why: l.Why, Source: "document"})
		}
		for _, e := range in.Entities {
			typ, name, ok := strings.Cut(e, ":")
			if !ok {
				typ, name = "concept", e
			}
			k := Key(workspace, LabelEntity, e)
			entities[k] = Named{Key: k, Name: name, Type: typ}
			addEdge(Edge{From: docKey(in.Path), To: k, Type: RelMentions, Source: "distill"})
		}
		if in.Repo != "" {
			k := Key(workspace, LabelRepo, in.Repo)
			repos[k] = Named{Key: k, Name: in.Repo, Type: "repo"}
			addEdge(Edge{From: docKey(in.Path), To: k, Type: RelInRepo, Source: "path"})
		}
	}
	for p := range unindexed {
		g.Docs = append(g.Docs, Doc{Key: docKey(p), Path: p, Title: p, Status: StatusUnindexed})
	}
	for _, n := range entities {
		g.Entities = append(g.Entities, n)
	}
	for _, n := range repos {
		g.Repos = append(g.Repos, n)
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Docs, func(i, j int) bool { return g.Docs[i].Key < g.Docs[j].Key })
	sort.Slice(g.Entities, func(i, j int) bool { return g.Entities[i].Key < g.Entities[j].Key })
	sort.Slice(g.Repos, func(i, j int) bool { return g.Repos[i].Key < g.Repos[j].Key })
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.To < b.To
	})
	return g
}
