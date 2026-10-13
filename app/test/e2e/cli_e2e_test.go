//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/ops"
)

// cliHarness 는 memory-mcp <command> 와 같은 배선의 운영 CLI 다 (scripts/memory/run.sh 가 부르는 것).
type cliHarness struct {
	cli *ops.CLI
	out *bytes.Buffer
}

func newCLI(t *testing.T, cfg config.Config, g graph.Store) *cliHarness {
	t.Helper()
	ctx := context.Background()
	client := opensearch.New(cfg.OpenSearchURL)
	store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	ix := ingest.New(ingest.Deps{OS: client, Cold: store, Config: cfg})
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: client, Cold: store, Indexer: ix, Graph: g, Log: t.Logf})
	out := &bytes.Buffer{}
	return &cliHarness{out: out, cli: &ops.CLI{Config: cfg, OS: client, Cold: store, Indexer: ix, Handlers: h, Graph: g, Out: out, Now: time.Now}}
}

// run 은 명령 하나를 돌려 출력을 돌려준다.
func (c *cliHarness) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	c.out.Reset()
	if err := c.cli.Run(context.Background(), name, args); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return c.out.String()
}

func (c *cliHarness) fail(name string, args ...string) error {
	c.out.Reset()
	return c.cli.Run(context.Background(), name, args)
}

// TestOperatorCLIWalkthrough 는 사람이 운영 CLI 로 하는 순서 그대로다:
// bootstrap → ingest → query → compaction-report → tier(미리보기·실행) → list-cold → archive → restore → reload → audit →
// tier-tick → questions → status.
func TestOperatorCLIWalkthrough(t *testing.T) {
	cfg := newConfig(t, nil)
	g := newGraph(t, cfg.Graph)
	c := newCLI(t, cfg, g)
	root := cfg.MetaRepoRoot
	const legacy = "docs/repository/payments/adr/0002-legacy.md"
	write(t, root, legacy, "---\nrepo: payments\ndate: "+daysAgo(hotDays+90)+"\n---\n# ADR-0002 레거시 정산\nSettlementBatchJob 은 매일 새벽 3시.\n")
	write(t, root, "docs/specs/payments/refund.md", "---\nrepo: payments\ndate: "+daysAgo(3)+"\n---\n# 환불 스펙\nRefundWindowDays 는 14.\n")

	if out := c.run(t, "bootstrap"); out != "bootstrap 완료\n" {
		t.Errorf("bootstrap = %q", out)
	}
	if out := c.run(t, "ingest", "docs"); !strings.Contains(out, legacy) {
		t.Errorf("ingest = %s", out)
	}
	if out := c.run(t, "search", "-project", "payments", "RefundWindowDays"); !strings.Contains(out, "docs/specs/payments/refund.md") {
		t.Errorf("search = %s", out)
	}
	if err := c.fail("search"); !errors.Is(err, ops.ErrUsage) {
		t.Errorf("질의 없는 search = %v", err)
	}
	if out := c.run(t, "compaction-report"); !strings.Contains(out, legacy) {
		t.Errorf("compaction-report 에 수명이 다한 문서가 나온다:\n%s", out)
	}

	dry := c.run(t, "tier", "-team", cfg.DefaultTeam, "-dry-run")
	index := regexp.MustCompile(`mem-` + cfg.DefaultTeam + `-\d{4}\.\d{2}`).FindString(dry)
	if index == "" {
		t.Fatalf("tier -dry-run = %s", dry)
	}
	if out := c.run(t, "tier", "-team", cfg.DefaultTeam); !strings.Contains(out, "hot 에서 뺌: "+legacy) {
		t.Errorf("tier = %s", out)
	}
	if out := c.run(t, "list-cold"); !strings.Contains(out, index) {
		t.Errorf("list-cold = %s", out)
	}

	if err := c.fail("archive", "deprecate", legacy); err == nil {
		t.Error("archive 는 -by·-reason 없이 거부한다")
	}
	if out := c.run(t, "archive", "deprecate", "-by", "jin", "-reason", "정산 방식 폐기", legacy); !strings.Contains(out, "deprecated") {
		t.Errorf("deprecate = %s", out)
	}
	if out := c.run(t, "restore", index); !strings.Contains(out, "복원했습니다") {
		t.Errorf("restore = %s", out)
	}
	if out := c.run(t, "search", "SettlementBatchJob"); strings.Contains(out, legacy) {
		t.Errorf("deprecate 된 문서는 기본 검색에서 빠진다: %s", out)
	}
	if out := c.run(t, "search", "-superseded", "SettlementBatchJob"); !strings.Contains(out, legacy) {
		t.Errorf("-superseded 로는 보인다: %s", out)
	}
	if out := c.run(t, "archive", "undeprecate", "-by", "jin", "-reason", "되돌림", legacy); !strings.Contains(out, "해제") {
		t.Errorf("undeprecate = %s", out)
	}

	if out := c.run(t, "reload"); !strings.Contains(out, "chunks 재적재") {
		t.Errorf("reload = %s", out)
	}
	if out := c.run(t, "audit"); !strings.Contains(out, "audit") {
		t.Errorf("audit = %s", out)
	}
	if out := c.run(t, "audit", "-apply"); out == "" {
		t.Error("audit -apply 는 결과를 남긴다")
	}
	if out := c.run(t, "tier-tick", "-every", "1h"); !strings.Contains(out, "[audit]") {
		t.Errorf("첫 tier-tick 은 돈다: %s", out)
	}
	if out := c.run(t, "tier-tick", "-every", "1h"); !strings.Contains(out, "아직 아님") {
		t.Errorf("간격 안의 tier-tick 은 건너뛴다: %s", out)
	}
	if out := c.run(t, "questions"); strings.Contains(out, "[") {
		t.Errorf("판정 질문 없음 = %s", out)
	}
	if out := c.run(t, "graph", "rebuild"); !strings.Contains(out, "rebuild") {
		t.Errorf("graph rebuild = %s", out)
	}
	status := c.run(t, "status")
	for _, want := range []string{"opensearch: ", "index: workspace=" + cfg.Workspace, "graph: ", "daemon: stopped"} {
		if !strings.Contains(status, want) {
			t.Errorf("status 에 %q 가 없다:\n%s", want, status)
		}
	}
	if err := c.fail("no-such-command"); !errors.Is(err, ops.ErrUsage) {
		t.Errorf("모르는 명령 = %v", err)
	}
}
