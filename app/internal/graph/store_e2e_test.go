//go:build e2e

package graph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/drakejin/workspace/app/internal/config"
)

// planOperators 는 plan 트리의 연산자 이름 전부다.
func planOperators(p neo4j.Plan) []string {
	if p == nil {
		return nil
	}
	out := []string{p.Operator()}
	for _, child := range p.Children() {
		out = append(out, planOperators(child)...)
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// OPS-11: 실제 Neo4j 의 계획(EXPLAIN — 실행하지 않는다)에 AllNodesScan 이 없다. 관계 적재는 라벨별 key 유일 제약의 인덱스를,
// workspace 비우기는 라벨 스캔(Doc 은 workspace 인덱스)을 쓴다.
func TestGraphWritePlansUseLabelIndexes(t *testing.T) {
	uri := envOr("MEMORY_GRAPH_URI", "bolt://localhost:7689")
	if uri == "off" {
		t.Skip("MEMORY_GRAPH_URI=off")
	}
	cfg := config.Graph{URI: uri, User: os.Getenv("MEMORY_GRAPH_USER"), Password: os.Getenv("MEMORY_GRAPH_PASSWORD"), Database: envOr("MEMORY_GRAPH_DATABASE", "neo4j")}
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer g.Close(context.Background())
	if err := g.Ping(ctx); err != nil { // 제약·인덱스를 만든다 (IF NOT EXISTS)
		t.Fatal(err)
	}
	s := g.(*store)
	queries := workspaceDeleteQueries()
	for _, typ := range edgeTypes {
		queries = append(queries, relationshipQuery(typ))
	}
	params := map[string]any{"ws": "plan-only", "rows": []map[string]any{{"from": "a", "to": "b", "why": "", "source": ""}}}
	for _, q := range queries {
		res, err := neo4j.ExecuteQuery(ctx, s.driver, "EXPLAIN "+q, params, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database))
		if err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		for _, op := range planOperators(res.Summary.Plan()) {
			if op == "AllNodesScan" || op == "AllNodesScan@neo4j" {
				t.Errorf("%s\n→ 계획에 AllNodesScan: %v", q, planOperators(res.Summary.Plan()))
				break
			}
		}
	}
}
