package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	neo4jconfig "github.com/neo4j/neo4j-go-driver/v5/neo4j/config"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
)

// ErrDisabled 는 그래프가 꺼져 있다는 뜻이다 (MEMORY_GRAPH_URI=off).
var ErrDisabled = errors.New("그래프가 꺼져 있습니다 (MEMORY_GRAPH_URI=off)")

// MaxDepth 는 이웃 탐색 깊이 상한이다.
const MaxDepth = 3

// maxNeighbors 는 이웃 질의 결과 상한이다.
const maxNeighbors = 50

// Neighbor 는 문서에서 몇 걸음 떨어진 다른 문서다.
type Neighbor struct {
	Path   string
	Title  string
	Status string
	Hops   int
	Via    []string // 지나온 노드 (경로·entity 이름)
	Rels   []string // 지나온 관계 종류
	Whys   []string // 관계마다의 이유 (없으면 "")
}

// EntityDocs 는 entity 하나와 그것을 다루는 문서다.
type EntityDocs struct {
	Name string
	Type string
	Docs []string
}

// Stats 는 workspace 그래프 크기다.
type Stats struct {
	Nodes map[string]int
	Edges map[string]int
}

// Store 는 그래프 저장소다.
type Store interface {
	Ping(ctx context.Context) error
	// Replace 는 workspace 의 그래프를 통째로 바꾼다 (한 트랜잭션).
	Replace(ctx context.Context, g Graph) error
	Neighbors(ctx context.Context, workspace, path string, depth int) ([]Neighbor, error)
	Entities(ctx context.Context, workspace, query string) ([]EntityDocs, error)
	Stats(ctx context.Context, workspace string) (Stats, error)
	Close(ctx context.Context) error
}

type store struct {
	driver   neo4j.DriverWithContext
	database string
}

const (
	connectTimeout = 3 * time.Second
	acquireTimeout = 5 * time.Second
)

// New 는 Neo4j Store 다. 연결은 하지 않는다 — 확인은 Ping. URI 가 비면 ErrDisabled.
func New(cfg config.Graph) (Store, error) {
	if cfg.URI == "" {
		return nil, ErrDisabled
	}
	auth := neo4j.NoAuth()
	if cfg.User != "" {
		auth = neo4j.BasicAuth(cfg.User, cfg.Password, "")
	}
	driver, err := neo4j.NewDriverWithContext(cfg.URI, auth, func(c *neo4jconfig.Config) {
		c.SocketConnectTimeout = connectTimeout
		c.ConnectionAcquisitionTimeout = acquireTimeout
	})
	if err != nil {
		return nil, fmt.Errorf("neo4j %s: %w", cfg.URI, err)
	}
	return &store{driver: driver, database: cfg.Database}, nil
}

func (s *store) Close(ctx context.Context) error { return s.driver.Close(ctx) }

var constraints = []string{
	"CREATE CONSTRAINT doc_key IF NOT EXISTS FOR (n:Doc) REQUIRE n.key IS UNIQUE",
	"CREATE CONSTRAINT entity_key IF NOT EXISTS FOR (n:Entity) REQUIRE n.key IS UNIQUE",
	"CREATE CONSTRAINT repo_key IF NOT EXISTS FOR (n:Repo) REQUIRE n.key IS UNIQUE",
	"CREATE INDEX doc_workspace IF NOT EXISTS FOR (n:Doc) ON (n.workspace)",
}

func (s *store) Ping(ctx context.Context) error {
	if err := s.driver.VerifyConnectivity(ctx); err != nil {
		return fmt.Errorf("neo4j: %w", err)
	}
	for _, q := range constraints {
		if _, err := neo4j.ExecuteQuery(ctx, s.driver, q, nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database)); err != nil {
			return fmt.Errorf("neo4j schema: %w", err)
		}
	}
	return nil
}

// edgeTypes 는 Cypher 관계 종류다. 관계 종류는 파라미터로 넘길 수 없어 이 목록에 있는 것만 쿼리에 넣는다.
var edgeTypes = []string{
	string(model.LinkSupersedes), string(model.LinkDerivedFrom), string(model.LinkRelates), string(model.LinkLinksTo),
	RelMentions, RelInRepo,
}

func (s *store) Replace(ctx context.Context, g Graph) error {
	session := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.database, AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		run := func(q string, params map[string]any) error {
			res, err := tx.Run(ctx, q, params)
			if err != nil {
				return err
			}
			_, err = res.Consume(ctx)
			return err
		}
		ws := map[string]any{"ws": g.Workspace}
		if err := run("MATCH (n {workspace: $ws}) DETACH DELETE n", ws); err != nil {
			return nil, err
		}
		if err := run(`UNWIND $rows AS r CREATE (n:Doc) SET n = r, n.workspace = $ws`, map[string]any{"ws": g.Workspace, "rows": docRows(g.Docs)}); err != nil {
			return nil, err
		}
		if err := run(`UNWIND $rows AS r CREATE (n:Entity) SET n = r, n.workspace = $ws`, map[string]any{"ws": g.Workspace, "rows": namedRows(g.Entities)}); err != nil {
			return nil, err
		}
		if err := run(`UNWIND $rows AS r CREATE (n:Repo) SET n = r, n.workspace = $ws`, map[string]any{"ws": g.Workspace, "rows": namedRows(g.Repos)}); err != nil {
			return nil, err
		}
		byType := map[string][]map[string]any{}
		for _, e := range g.Edges {
			byType[e.Type] = append(byType[e.Type], map[string]any{"from": e.From, "to": e.To, "why": e.Why, "source": e.Source})
		}
		for _, typ := range edgeTypes {
			rows := byType[typ]
			if len(rows) == 0 {
				continue
			}
			q := fmt.Sprintf(`UNWIND $rows AS r MATCH (a {key: r.from}), (b {key: r.to}) CREATE (a)-[:%s {why: r.why, source: r.source}]->(b)`, typ)
			if err := run(q, map[string]any{"rows": rows}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("neo4j replace %s: %w", g.Workspace, err)
	}
	return nil
}

func docRows(docs []Doc) []map[string]any {
	rows := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, map[string]any{
			"key": d.Key, "path": d.Path, "team": d.Team, "repo": d.Repo, "title": d.Title,
			"docType": d.DocType, "kind": d.Kind, "status": d.Status, "summary": d.Summary,
		})
	}
	return rows
}

func namedRows(nodes []Named) []map[string]any {
	rows := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, map[string]any{"key": n.Key, "name": n.Name, "type": n.Type})
	}
	return rows
}

func (s *store) Neighbors(ctx context.Context, workspace, path string, depth int) ([]Neighbor, error) {
	if depth < 1 || depth > MaxDepth {
		return nil, fmt.Errorf("depth 는 1~%d 이어야 합니다: %d", MaxDepth, depth)
	}
	// 가변 길이 패턴의 상한은 파라미터로 넘길 수 없어 검증한 정수만 넣는다
	q := fmt.Sprintf(`
MATCH (d:Doc {key: $key})
MATCH p = (d)-[*1..%d]-(n:Doc)
WHERE n <> d
// 같은 길이면 뜻이 강한 관계(개정 > 근거 > 관련 > 링크 > entity·repo)를 지나는 경로를 보인다 — 결과가 매번 같게
WITH n, p, reduce(w = 0, r IN relationships(p) |
       w + CASE type(r) WHEN 'SUPERSEDES' THEN 0 WHEN 'DERIVED_FROM' THEN 1 WHEN 'RELATES' THEN 2 WHEN 'LINKS_TO' THEN 3 ELSE 4 END) AS weight
ORDER BY length(p), weight
WITH n, collect(p)[0] AS p
RETURN n.path AS path, n.title AS title, n.status AS status, length(p) AS hops,
       [x IN nodes(p)[1..-1] | coalesce(x.path, x.name)] AS via,
       [r IN relationships(p) | type(r)] AS rels,
       [r IN relationships(p) | coalesce(r.why, '')] AS whys
ORDER BY hops, path LIMIT %d`, depth, maxNeighbors)
	res, err := neo4j.ExecuteQuery(ctx, s.driver, q, map[string]any{"key": Key(workspace, LabelDoc, path)},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database), neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, fmt.Errorf("neo4j neighbors %s: %w", path, err)
	}
	out := make([]Neighbor, 0, len(res.Records))
	for _, r := range res.Records {
		m := r.AsMap()
		out = append(out, Neighbor{
			Path: str(m["path"]), Title: str(m["title"]), Status: str(m["status"]), Hops: int(num(m["hops"])),
			Via: strs(m["via"]), Rels: strs(m["rels"]), Whys: strs(m["whys"]),
		})
	}
	return out, nil
}

func (s *store) Entities(ctx context.Context, workspace, query string) ([]EntityDocs, error) {
	q := `
MATCH (e:Entity {workspace: $ws}) WHERE toLower(e.name) CONTAINS toLower($q) OR toLower(e.type + ':' + e.name) = toLower($q)
MATCH (e)<-[:MENTIONS]-(d:Doc)
WITH e, d ORDER BY d.path
RETURN e.name AS name, e.type AS type, collect(d.path) AS docs
ORDER BY name LIMIT 50`
	res, err := neo4j.ExecuteQuery(ctx, s.driver, q, map[string]any{"ws": workspace, "q": query},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database), neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, fmt.Errorf("neo4j entities %q: %w", query, err)
	}
	out := make([]EntityDocs, 0, len(res.Records))
	for _, r := range res.Records {
		m := r.AsMap()
		out = append(out, EntityDocs{Name: str(m["name"]), Type: str(m["type"]), Docs: strs(m["docs"])})
	}
	return out, nil
}

func (s *store) Stats(ctx context.Context, workspace string) (Stats, error) {
	st := Stats{Nodes: map[string]int{}, Edges: map[string]int{}}
	nodes, err := neo4j.ExecuteQuery(ctx, s.driver, `MATCH (n {workspace: $ws}) RETURN labels(n)[0] AS label, count(*) AS n`,
		map[string]any{"ws": workspace}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database))
	if err != nil {
		return st, fmt.Errorf("neo4j stats: %w", err)
	}
	for _, r := range nodes.Records {
		m := r.AsMap()
		st.Nodes[str(m["label"])] = int(num(m["n"]))
	}
	edges, err := neo4j.ExecuteQuery(ctx, s.driver, `MATCH (a {workspace: $ws})-[r]->() RETURN type(r) AS type, count(*) AS n`,
		map[string]any{"ws": workspace}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database))
	if err != nil {
		return st, fmt.Errorf("neo4j stats: %w", err)
	}
	for _, r := range edges.Records {
		m := r.AsMap()
		st.Edges[str(m["type"])] = int(num(m["n"]))
	}
	return st, nil
}

// SortedKeys 는 통계 출력용 정렬 키다.
func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int64 {
	n, _ := v.(int64)
	return n
}

func strs(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, str(it))
	}
	return out
}
