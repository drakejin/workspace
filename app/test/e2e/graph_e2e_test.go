//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/ops"
	"github.com/drakejin/workspace/app/internal/search"
)

// newGraph 는 실제 Neo4j(make memory-init)에 붙는다. 그래프를 끈 설정(MEMORY_GRAPH_URI=off)이면 건너뛰고,
// 켜져 있는데 붙지 못하면 실패한다 — test-e2e 가 그래프 없이 조용히 통과하지 않게.
func newGraph(t *testing.T, cfg config.Graph) graph.Store {
	t.Helper()
	g, err := graph.New(cfg)
	if errors.Is(err, graph.ErrDisabled) {
		t.Skip("MEMORY_GRAPH_URI=off — 그래프 e2e 를 건너뜁니다")
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := g.Ping(ctx); err != nil {
		_ = g.Close(context.Background())
		t.Fatalf("Neo4j(%s)에 붙을 수 없습니다 — make memory-init 먼저: %v", cfg.URI, err)
	}
	t.Cleanup(func() { _ = g.Close(context.Background()) })
	return g
}

// TestKnowledgeGraphAndSupersededRelocation 은 데몬 사이클이 knowledge graph 를 만들고(링크·supersedes·entity),
// 밀린 knowledge 가 knowledge 인덱스에서 그 달 인덱스로 옮겨져 tier 로 cold 에 가는 흐름이다.
func TestKnowledgeGraphAndSupersededRelocation(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_DISTILL_ENABLED": "true"})
	g := newGraph(t, cfg.Graph)
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	client := opensearch.New(cfg.OpenSearchURL)
	var logMu sync.Mutex
	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Graph: g, Ask: &fakeLLM{}, Log: func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		t.Logf(f, a...)
	}, RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })

	write(t, root, "docs/specs/orders.md", "---\ndate: "+daysAgo(5)+"\n---\n# 주문 스펙\n상태는 [주문 상태](../knowledge/order-status-v2.md) 를 따른다. OrderSpecToken\n")
	write(t, root, "docs/knowledge/order-status.md", "---\ndoc_type: knowledge\ndate: "+daysAgo(hotDays+60)+"\n---\n# 주문 상태\nOrderStatusEnum 은 CREATED, PAID 두 가지다.\n")
	write(t, root, "docs/knowledge/order-status-v2.md", "---\ndoc_type: knowledge\ndate: "+daysAgo(2)+
		"\nsupersedes: docs/knowledge/order-status.md\nderived_from:\n  - path: docs/specs/orders.md\n    why: 스펙에서 상태 목록을 뽑았다\n---\n"+
		"# 주문 상태\nOrderStatusEnum 은 CREATED, PAID, SHIPPED 세 가지다.\n## 출처\n- docs/specs/orders.md\n")
	if err := d.Boot(ctx, false); err != nil {
		t.Fatal(err)
	}
	d.Settle() // 증류가 entity 를 붙이면 그래프가 한 번 더 만들어진다

	t.Run("밀린 knowledge 는 knowledge 인덱스를 떠나 자기 달 인덱스로 간다", func(t *testing.T) {
		hitsRes, err := search.Search(ctx, client, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: "OrderStatusEnum", IncludeSuperseded: true})
		hits := hitsRes.Hits
		if err != nil {
			t.Fatal(err)
		}
		where := map[string]string{}
		for _, h := range hits {
			where[h.SourcePath] = h.Index
		}
		if got := where["docs/knowledge/order-status-v2.md"]; got != naming.KnowledgeIndex(cfg.DefaultTeam) {
			t.Errorf("v2 index = %q", got)
		}
		if got := where["docs/knowledge/order-status.md"]; !naming.IsMonthly(got) {
			t.Errorf("구판 index = %q (월 인덱스여야 한다)", got)
		}
	})

	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: client, Graph: g, Log: t.Logf})

	t.Run("related_docs: supersedes·derived_from·링크로 이어진 문서", func(t *testing.T) {
		out, err := h.Related(ctx, mcpserver.RelatedInput{Path: "docs/knowledge/order-status-v2.md", Depth: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"docs/knowledge/order-status.md (superseded)",
			"SUPERSEDES",
			"docs/specs/orders.md (hot)",
			"DERIVED_FROM(스펙에서 상태 목록을 뽑았다)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("related 에 %q 가 없다:\n%s", want, out)
			}
		}
	})

	t.Run("docs_by_entity: 증류가 뽑은 entity 로 문서를 찾는다", func(t *testing.T) {
		out, err := h.Entities(ctx, mcpserver.EntityInput{Entity: "demo"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "concept:demo — ") || !strings.Contains(out, "docs/specs/orders.md") {
			t.Errorf("entity = %s", out)
		}
	})

	t.Run("graph CLI: stats·rebuild 는 LLM 없이 같은 그래프", func(t *testing.T) {
		ix := ingest.New(ingest.Deps{OS: client, Config: cfg})
		var out bytes.Buffer
		cli := &ops.CLI{Config: cfg, OS: client, Indexer: ix, Handlers: h, Graph: g, Out: &out, Now: time.Now}
		if err := cli.Run(ctx, "graph", []string{"stats"}); err != nil {
			t.Fatal(err)
		}
		before := out.String()
		if !strings.Contains(before, "Doc") || !strings.Contains(before, "SUPERSEDES") {
			t.Errorf("stats = %s", before)
		}
		out.Reset()
		if err := cli.Run(ctx, "graph", []string{"rebuild"}); err != nil || !strings.Contains(out.String(), "docs=3") {
			t.Fatalf("rebuild = %s %v", out.String(), err)
		}
		out.Reset()
		_ = cli.Run(ctx, "graph", []string{"stats"})
		if out.String() != before {
			t.Errorf("rebuild 전후가 다르다\n before %s\n after  %s", before, out.String())
		}
	})

	t.Run("밀린 knowledge 는 그 달이 오래되면 tier 로 cold 에 간다", func(t *testing.T) {
		store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Shutdown(); err != nil { // tier 는 hot 파일을 지운다 — 데몬과 겹치지 않게
			t.Fatal(err)
		}
		decisions, err := cold.Tier(ctx, client, store, cfg, cold.TierOptions{Team: cfg.DefaultTeam})
		if err != nil {
			t.Fatal(err)
		}
		var moved []string
		for _, dec := range decisions {
			if dec.Action == cold.TierMoved {
				moved = append(moved, dec.RemovedFiles...)
			}
		}
		if len(moved) != 1 || moved[0] != "docs/knowledge/order-status.md" {
			t.Errorf("moved = %v (decisions %+v)", moved, decisions)
		}
		if got := searchPaths(t, cfg, "OrderStatusEnum", true); len(got) != 1 || got[0] != "docs/knowledge/order-status-v2.md" {
			t.Errorf("knowledge 인덱스의 현재판은 남는다: %v", got)
		}
	})
}
