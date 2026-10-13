//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/ops"
)

// TestGraphToolsTakeTypedPathsAndMarkHiddenDocs 는 RUN-13·SCN-G7-1 의 재현이다.
//   - related_docs 는 ingest_path 처럼 './docs/…' 와 meta-repo 안의 절대 경로를 source_path 로 맞춘다 (예전에는 "없다" 였다).
//   - 그래프에 없는 문서와, 있지만 이어진 것이 없는 문서를 다른 말로 답한다.
//   - docs_by_entity·graph entity 는 기본 검색에서 숨는 문서(밀린·deprecate 된)에 related_docs 처럼 상태를 붙인다.
func TestGraphToolsTakeTypedPathsAndMarkHiddenDocs(t *testing.T) {
	cfg := newConfig(t, nil)
	g := newGraph(t, cfg.Graph)
	ctx := context.Background()
	root := cfg.MetaRepoRoot
	const (
		current = "docs/knowledge/status-codes-v2.md"
		old     = "docs/knowledge/status-codes.md"
		wrong   = "docs/knowledge/status-guess.md"
		lonely  = "docs/specs/lonely.md"
	)
	for _, rel := range []string{current, old, wrong, lonely} {
		write(t, root, rel, "# "+rel+"\n")
	}
	mentions := []string{"repo:alpha-api"}
	built := graph.Build(cfg.Workspace, []graph.Input{
		{Path: current, Entities: mentions},
		{Path: old, SupersededBy: current, Entities: mentions},
		{Path: wrong, Deprecated: true, Entities: mentions},
		{Path: lonely},
	}, nil)
	if err := g.Replace(ctx, built); err != nil {
		t.Fatal(err)
	}
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, Graph: g, Log: t.Logf})

	t.Run("related_docs 는 ./ 와 meta-repo 안의 절대 경로도 source_path 로 맞춘다", func(t *testing.T) {
		for _, typed := range []string{current, "./" + current, filepath.Join(root, current)} {
			out, err := h.Related(ctx, mcpserver.RelatedInput{Path: typed, Depth: 1})
			if err != nil || !strings.Contains(out, old+" (superseded)") {
				t.Errorf("related_docs %q = %s %v", typed, out, err)
			}
		}
	})

	t.Run("related_docs 는 그래프에 없는 문서와 이어진 것이 없는 문서를 가른다", func(t *testing.T) {
		missing, err := h.Related(ctx, mcpserver.RelatedInput{Path: "docs/nope/none.md"})
		if err != nil || !strings.Contains(missing, "그래프에 없는 문서입니다") {
			t.Errorf("그래프에 없는 문서 = %s %v", missing, err)
		}
		alone, err := h.Related(ctx, mcpserver.RelatedInput{Path: lonely})
		if err != nil || !strings.Contains(alone, "이어진 문서가 없습니다") || strings.Contains(alone, "그래프에 없는") {
			t.Errorf("그래프에 있지만 이어진 것이 없는 문서 = %s %v", alone, err)
		}
	})

	t.Run("docs_by_entity 와 graph entity 는 밀린·deprecate 된 문서에 상태를 붙인다", func(t *testing.T) {
		want := "repo:alpha-api — " + current + ", " + old + " (superseded), " + wrong + " (deprecated)"
		out, err := h.Entities(ctx, mcpserver.EntityInput{Entity: "alpha-api"})
		if err != nil || out != want {
			t.Errorf("docs_by_entity\n got  %q %v\n want %q", out, err, want)
		}
		var cliOut bytes.Buffer
		cli := &ops.CLI{Config: cfg, Handlers: h, Graph: g, Out: &cliOut, Now: time.Now}
		if err := cli.Run(ctx, "graph", []string{"entity", "alpha-api"}); err != nil || cliOut.String() != want+"\n" {
			t.Errorf("graph entity = %q %v", cliOut.String(), err)
		}
	})
}

// TestRestoreColdWithoutConfirmationChecksCold 는 RUN-14 의 재현이다 — confirmed 없는 restore_cold 는 cold 에 없는 인덱스를
// 두고 "복원할까요" 를 묻게 시키지 않는다 (예전에는 이름 꼴만 보고 바로 물으라고 했다).
func TestRestoreColdWithoutConfirmationChecksCold(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"}))
	index := ingestOne(t, m, "docs/specs/legacy-batch.md", daysAgo(hotDays+60), "LegacyBatchToken")
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier = %+v", d)
	}

	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": index}); isErr || !strings.Contains(text, "복원할까요") {
		t.Errorf("cold 에 있는 인덱스는 먼저 물으라고 돌려준다: %s", text)
	}
	missing := "mem-" + m.cfg.DefaultTeam + "-2001.01"
	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": missing}); !isErr || !strings.Contains(text, missing) || !strings.Contains(text, "list_cold") {
		t.Errorf("cold 에 없는 인덱스는 없다고 알린다: %s", text)
	}
}

// TestStatusCountsOnlyThisWorkspacesIndices 는 RUN-19 의 재현이다 — status 의 'memory indices N' 은 같은 클러스터의 다른
// meta-repo(실제 workspace 의 mem-default-* 등) 인덱스를 세지 않는다.
func TestStatusCountsOnlyThisWorkspacesIndices(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"}))
	ctx := context.Background()
	ingestOne(t, m, "docs/specs/now.md", daysAgo(2), "StatusNowToken")
	ingestOne(t, m, "docs/specs/last-month.md", daysAgo(35), "StatusLastMonthToken") // 35일 전은 다른 달이다
	var rows []struct {
		Index string `json:"index"`
	}
	if err := m.os.Do(ctx, http.MethodGet, "/_cat/indices/mem-"+m.cfg.DefaultTeam+"-*", nil, url.Values{"format": {"json"}, "h": {"index"}}, &rows); err != nil {
		t.Fatal(err)
	}

	out := ops.RenderStatus(ctx, m.cfg, ops.NewProbe(m.cfg, m.os, nil), time.Now())

	if want := fmt.Sprintf("(memory indices %d)", len(rows)); len(rows) < 2 || !strings.Contains(out, want) {
		t.Errorf("이 workspace 의 인덱스 %d개(%v)만 센다 — %q 가 없다:\n%s", len(rows), rows, want, out)
	}
}
