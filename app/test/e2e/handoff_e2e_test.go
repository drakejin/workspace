//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/search"
	"github.com/drakejin/workspace/app/internal/state"
)

// relatedWait 는 그래프가 비동기로 갱신되는 동안 related_docs 를 되풀이한다.
const relatedWait = 15 * time.Second

// shipDoc 은 판정 latest-wins 감인 배송비 문서다 — 유효 시각과 금액만 다르고 본문이 거의 같다.
func shipDoc(day string, fee int) string {
	return fmt.Sprintf("---\ndate: %s\n---\n# 배송비 정책\n## 결정\nHandoffShipFee 는 기본 배송비를 %d원으로 받는다. 제주는 추가 요금이 있다.\n", day, fee)
}

// quietCycle 은 마지막 사이클이 아무것도 다시 쓰지 않고, 판정할 변경분도 그래프를 다시 만들 일도 없었는지 본다.
func quietCycle(t *testing.T, root string) {
	t.Helper()
	s := state.ReadStatus(root)
	if s == nil || s.LastCycle == nil || s.LastCycle.Summary == nil {
		t.Fatalf("status = %+v", s)
	}
	if got := *s.LastCycle.Summary; got.Indexed != 0 || got.StatUpdated != 0 || got.Removed != 0 {
		t.Errorf("한 번 넘긴 뒤의 사이클은 조용하다: %+v", got)
	}
	if j := s.LastCycle.Judge; j != nil && j.Candidates != 0 {
		t.Errorf("다시 판정하지 않는다: %+v", j)
	}
	if g := s.LastCycle.Graph; g != "unchanged" && g != "off" {
		t.Errorf("그래프를 다시 만들지 않는다: %q", g)
	}
}

// TestIngestPathDocsAreJudgedDistilledAndGraphed 는 RUN-3 의 재현이다. ingest_path(·CLI ingest·audit -apply)로 먼저 색인한
// 문서도 다음 데몬 사이클이 판정·증류·그래프에 넘긴다 — 예전에는 색인이 이미 최신이라 데몬이 건너뛰어, 충돌하는 낡은 문서가
// 숨지 않았고 그래프에도 들어가지 않았다. 넘긴 뒤의 사이클은 조용하다.
func TestIngestPathDocsAreJudgedDistilledAndGraphed(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_DISTILL_ENABLED": "true"})
	g := newGraph(t, cfg.Graph)
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	d := bootDaemon(t, cfg, &fakeLLM{})
	t.Cleanup(func() { _ = d.Shutdown() })
	m := newMCP(t, cfg)
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: m.os, Graph: g, Log: t.Logf})

	write(t, root, "docs/feature/ship-a/v1.md", shipDoc(daysAgo(25), 3000))
	write(t, root, "docs/feature/ship-a/v2.md", shipDoc(daysAgo(2), 3500))
	write(t, root, "docs/knowledge/gp/limits.md", "---\ndoc_type: knowledge\n---\n# 한도\nHandoffLimitToken\n")
	write(t, root, "docs/knowledge/gp/linker.md", "---\ndoc_type: knowledge\n---\n# 링크\n[한도](limits.md) 를 따른다. HandoffLinkerToken\n")
	if text, isErr := m.call(t, "ingest_path", map[string]any{"path": "docs"}); isErr || !strings.Contains(text, "4개 문서 적재") {
		t.Fatalf("ingest_path = %s", text)
	}
	d.Trigger("timer")

	t.Run("판정: 충돌하는 낡은 문서는 latest-wins 로 숨는다", func(t *testing.T) {
		if got := searchPaths(t, cfg, "HandoffShipFee", false); !reflect.DeepEqual(got, []string{"docs/feature/ship-a/v2.md"}) {
			t.Errorf("visible = %v", got)
		}
		if got := searchPaths(t, cfg, "HandoffShipFee", true); len(got) != 2 {
			t.Errorf("include_superseded = %v", got)
		}
	})

	t.Run("그래프: ingest 한 문서의 링크가 들어간다", func(t *testing.T) {
		out, err := h.Related(ctx, mcpserver.RelatedInput{Path: "docs/knowledge/gp/linker.md", Depth: 1})
		if err != nil || !strings.Contains(out, "docs/knowledge/gp/limits.md") {
			t.Errorf("related_docs = %s %v", out, err)
		}
	})

	t.Run("증류: ingest 한 문서에도 요약이 붙는다", func(t *testing.T) {
		d.Settle()
		res, err := search.Search(ctx, m.os, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: "HandoffLinkerToken"})
		if err != nil || len(res.Hits) == 0 || res.Hits[0].Summary != "요약: 링크" {
			t.Errorf("summary = %+v %v", res.Hits, err)
		}
	})

	t.Run("넘긴 뒤의 사이클은 조용하다", func(t *testing.T) {
		d.Trigger("timer")
		quietCycle(t, root)
	})
}

// TestResidentDaemonPicksUpADocIngestedBetweenCycles 는 RUN-3 의 상주 데몬 재현이다. 데몬이 도는 중에 파일을 만들고 바로
// ingest_path 하면 watcher 힌트보다 ingest 가 먼저 색인한다 — 그래도 다음 타이머 사이클이 그 문서를 그래프에 넣는다.
func TestResidentDaemonPicksUpADocIngestedBetweenCycles(t *testing.T) {
	// watcher 힌트는 테스트 동안 오지 않게 늦추고, 타이머는 1초마다 돈다
	cfg := newConfig(t, map[string]string{"MEMORY_RECONCILE_INTERVAL_SECONDS": "1", "MEMORY_RECONCILE_DEBOUNCE_MS": "600000"})
	g := newGraph(t, cfg.Graph)
	root := cfg.MetaRepoRoot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Graph: g, Ask: &fakeLLM{}, Log: t.Logf, RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(ctx, false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	m := newMCP(t, cfg)
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: m.os, Graph: g, Log: t.Logf})

	write(t, root, "docs/knowledge/gp/limits.md", "---\ndoc_type: knowledge\n---\n# 한도\nResidentLimitToken\n")
	if !eventuallyFound(cfg, "ResidentLimitToken", relatedWait) {
		t.Fatal("타이머 사이클이 링크 대상을 색인하지 않았다")
	}
	write(t, root, "docs/knowledge/gp/linker.md", "---\ndoc_type: knowledge\n---\n# 링크\n[한도](limits.md) 를 따른다.\n")
	if text, isErr := m.call(t, "ingest_path", map[string]any{"path": "docs/knowledge/gp/linker.md"}); isErr || !strings.Contains(text, "1개 문서 적재") {
		t.Fatalf("ingest_path = %s", text)
	}
	var out string
	for deadline := time.Now().Add(relatedWait); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if out, err = h.Related(context.Background(), mcpserver.RelatedInput{Path: "docs/knowledge/gp/linker.md", Depth: 1}); err == nil && strings.Contains(out, "docs/knowledge/gp/limits.md") {
			break
		}
	}
	if !strings.Contains(out, "docs/knowledge/gp/limits.md") {
		t.Errorf("다음 사이클이 ingest 한 문서를 그래프에 넣지 않았다: %s %v", out, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("종료 신호에 루프가 빠져나오지 않았다")
	}
	if err := d.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

// TestBlankDocumentsConverge 는 RUN-6·IDX-1 의 재현이다. 빈 .md 와 frontmatter 만 있는 .md 도 청크 0 하나로 색인돼 다음
// 사이클은 조용하다 — 예전에는 청크가 0개라 색인에 흔적이 없어 매 사이클 새 문서로 다시 쓰였고(판정·그래프 재구축·cold store
// 쓰기 반복), audit -apply 는 '[고침]' 이라고 했지만 아무것도 바뀌지 않았다. 본문이 없으니 증류하지 않는다.
func TestBlankDocumentsConverge(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_DISTILL_ENABLED": "true"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	ai := &fakeLLM{}
	blank := []string{"docs/knowledge/empty.md", "docs/knowledge/fm-only.md"}
	write(t, root, blank[0], "")
	write(t, root, blank[1], "---\ndoc_type: knowledge\ndate: "+daysAgo(3)+"\n---\n")
	d := bootDaemon(t, cfg, ai)
	t.Cleanup(func() { _ = d.Shutdown() })
	m := newMCP(t, cfg)

	t.Run("두 번째 사이클은 다시 쓰지 않는다", func(t *testing.T) {
		if got := lastSummary(t, root); got.Indexed != 2 {
			t.Errorf("boot 는 두 문서를 색인한다: %+v", got)
		}
		d.Trigger("timer")
		quietCycle(t, root)
		d.Trigger("timer")
		quietCycle(t, root)
	})

	t.Run("본문이 없으니 증류하지 않는다", func(t *testing.T) {
		d.Settle()
		if _, n := ai.counts(); n != 0 {
			t.Errorf("빈 본문에 LLM 을 %d번 불렀다", n)
		}
	})

	t.Run("지우면 색인과 cold store 청크에서 빠진다", func(t *testing.T) {
		for _, rel := range blank {
			if _, ok, _ := cold.Archived(ctx, m.store, cfg.DefaultTeam, rel); !ok {
				t.Errorf("%s 의 cold store 청크가 없다", rel)
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
		d.Trigger("timer")
		if got := lastSummary(t, root); got.Removed != 2 {
			t.Errorf("tombstone = %+v", got)
		}
		for _, rel := range blank {
			if _, ok, _ := cold.Archived(ctx, m.store, cfg.DefaultTeam, rel); ok {
				t.Errorf("%s 의 cold store 청크가 orphan 으로 남았다", rel)
			}
		}
	})

	t.Run("audit -apply 가 고쳤다고 하면 정말 고쳐져서, 다시 감사하면 나오지 않는다", func(t *testing.T) {
		if err := d.Shutdown(); err != nil { // audit -apply 는 데몬 밖의 쓰기다
			t.Fatal(err)
		}
		write(t, root, "docs/knowledge/later.md", "---\ndoc_type: knowledge\n---\n")
		run := func(apply bool) []audit.Finding {
			rep, err := audit.Run(ctx, audit.Deps{OS: m.os, Config: cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}, apply)
			if err != nil {
				t.Fatal(err)
			}
			var out []audit.Finding
			for _, f := range rep.Findings {
				if f.Kind == audit.Unindexed || f.Kind == audit.StaleContent || f.Kind == audit.OrphanChunks || f.Kind == audit.OrphanIndex {
					out = append(out, f)
				}
			}
			return out
		}
		if got := run(true); len(got) != 1 || got[0].Path != "docs/knowledge/later.md" || got[0].Kind != audit.Unindexed || !got[0].Fixed {
			t.Errorf("audit -apply = %+v", got)
		}
		if got := run(false); len(got) != 0 {
			t.Errorf("고쳤다고 한 문서가 그대로 나온다: %+v", got)
		}
	})
}
