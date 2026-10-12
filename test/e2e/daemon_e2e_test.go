//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/daemon"
	"github.com/drakejin/workspace/internal/opensearch"
	"github.com/drakejin/workspace/internal/ops"
	"github.com/drakejin/workspace/internal/search"
)

// TestDaemonLifecycle 는 데몬 하나로 수렴 사이클 → 판정 3단 → knowledge 선언 → 증류 → 재구축 계약 → tombstone →
// cold 소유 → 단일 writer 를 순서대로 확인한다 (각 단계가 앞 단계의 상태 위에 쌓인다).
func TestDaemonLifecycle(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_DISTILL_ENABLED": "true"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	client := opensearch.New(cfg.OpenSearchURL)
	ai := &fakeLLM{}
	// Log 는 증류 goroutine 들에서 동시에 불린다
	var logMu sync.Mutex
	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Ask: ai, Log: func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		t.Logf(f, a...)
	}, RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	status := func() daemon.CycleSummary {
		s := daemon.ReadStatus(root)
		if s == nil || s.LastCycle == nil || s.LastCycle.Summary == nil {
			t.Fatalf("status = %+v", s)
		}
		return *s.LastCycle.Summary
	}

	write(t, root, "docs/specs/orders/2026-08-retry.md", "---\ndate: "+daysAgo(40)+"\n---\n# 주문 재시도 정책\n## 결정\nOrderRetryPolicy 는 결제 실패 시 3회 재시도한다. 간격은 1분이다.\n")
	write(t, root, "main/shop-api/docs/adr/0001-idempotency.md", "---\ndate: "+daysAgo(3)+"\n---\n# ADR-0001 멱등 키\nIdempotencyKeyHeader 를 모든 결제 요청에 붙인다.\n")
	write(t, root, "main/shop-api/src/ignored.md", "# 코드 트리\nCodeTreeToken")

	t.Run("boot 사이클: docs/ 와 main/{repo}/docs/ 를 색인하고 코드 트리는 건너뛴다", func(t *testing.T) {
		if err := d.Boot(ctx, false); err != nil {
			t.Fatal(err)
		}
		if got := status(); got.Scanned != 2 || got.Indexed != 2 {
			t.Errorf("boot = %+v", got)
		}
		if got := searchPaths(t, cfg, "OrderRetryPolicy", false); !reflect.DeepEqual(got, []string{"docs/specs/orders/2026-08-retry.md"}) {
			t.Errorf("search = %v", got)
		}
		if got := searchPaths(t, cfg, "CodeTreeToken", false); len(got) != 0 {
			t.Errorf("코드 트리 = %v", got)
		}
	})

	t.Run("stat 게이트: 변화가 없으면 다시 읽지 않고, mtime 만 바뀌면 stat 만 갱신한다", func(t *testing.T) {
		d.Trigger("timer")
		if got := status(); got.Indexed != 0 || got.StatUpdated != 0 {
			t.Errorf("idle = %+v", got)
		}
		future := time.Now().Add(time.Minute)
		_ = os.Chtimes(filepath.Join(root, "docs/specs/orders/2026-08-retry.md"), future, future)
		d.Trigger("watch")
		if got := status(); got.Indexed != 0 || got.StatUpdated != 1 {
			t.Errorf("touch = %+v", got)
		}
	})

	t.Run("판정 1단 latest-wins: 7일 넘게 차이 나는 비슷한 episodic 은 최신이 이긴다 (LLM 0회)", func(t *testing.T) {
		before, _ := ai.counts()
		write(t, root, "docs/specs/orders/2026-10-retry.md", "---\ndate: "+daysAgo(1)+"\n---\n# 주문 재시도 정책\n## 결정\nOrderRetryPolicy 는 결제 실패 시 5회 재시도한다. 간격은 지수 백오프다.\n")
		d.Trigger("watch")
		if got := searchPaths(t, cfg, "OrderRetryPolicy", false); !reflect.DeepEqual(got, []string{"docs/specs/orders/2026-10-retry.md"}) {
			t.Errorf("visible = %v", got)
		}
		if got := searchPaths(t, cfg, "OrderRetryPolicy", true); len(got) != 2 {
			t.Errorf("include_superseded = %v", got)
		}
		if after, _ := ai.counts(); after != before {
			t.Error("latest-wins 는 LLM 을 부르지 않는다")
		}
	})

	t.Run("판정 3단 질문: 7일 이내 + LLM 불가면 질문, 사람이 답하면 다음 사이클이 반영", func(t *testing.T) {
		write(t, root, "docs/specs/payments/a.md", "---\ndate: "+daysAgo(3)+"\n---\n# 환불 기한\nRefundWindowDays 는 7일이다. 환불 요청은 고객센터로 받는다.\n")
		d.Trigger("watch")
		write(t, root, "docs/specs/payments/b.md", "---\ndate: "+daysAgo(2)+"\n---\n# 환불 기한\nRefundWindowDays 는 14일이다. 환불 요청은 고객센터로 받는다.\n")
		ai.mu.Lock()
		ai.verdicts = append(ai.verdicts, "")
		ai.mu.Unlock()
		d.Trigger("watch")
		qs, _ := daemon.NewJudgeState(root).PendingQuestions(cfg.DefaultTeam)
		if len(qs) != 1 {
			t.Fatalf("questions = %+v", qs)
		}
		if got := searchPaths(t, cfg, "RefundWindowDays", false); len(got) != 2 {
			t.Errorf("답 전에는 둘 다 보인다: %v", got)
		}
		if _, err := ops.AnswerQuestion(root, qs[0].PairKey, ops.AnswerLatest, time.Now()); err != nil {
			t.Fatal(err)
		}
		d.Trigger("timer")
		if got := searchPaths(t, cfg, "RefundWindowDays", false); !reflect.DeepEqual(got, []string{"docs/specs/payments/b.md"}) {
			t.Errorf("답 후 = %v", got)
		}
	})

	t.Run("판정 2단 LLM: 모순이 아니면 공존, 같은 내용 쌍은 다시 묻지 않는다", func(t *testing.T) {
		write(t, root, "docs/specs/shipping/a.md", "---\ndate: "+daysAgo(2)+"\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 둔다.\n")
		d.Trigger("watch")
		ai.mu.Lock()
		ai.verdicts = append(ai.verdicts, `{"contradicts":false,"winner":"new","confident":true}`)
		ai.mu.Unlock()
		before, _ := ai.counts()
		write(t, root, "docs/specs/shipping/b.md", "---\ndate: "+daysAgo(1)+"\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 두고 제주를 3구역으로 둔다.\n")
		d.Trigger("watch")
		if after, _ := ai.counts(); after != before+1 {
			t.Errorf("judge calls %d → %d", before, after)
		}
		if got := searchPaths(t, cfg, "ShippingZoneTable", false); len(got) != 2 {
			t.Errorf("공존 = %v", got)
		}
		write(t, root, "docs/specs/shipping/b.md", "---\ndate: "+daysAgo(1)+"\ntags: [x]\n---\n# 배송 지역\nShippingZoneTable 은 수도권을 1구역으로 두고 제주를 3구역으로 둔다.\n")
		d.Trigger("watch")
		if after, _ := ai.counts(); after != before+1 {
			t.Error("frontmatter 만 바뀌면 재판정하지 않는다")
		}
	})

	t.Run("knowledge 는 유사도로 판정하지 않고 supersedes 선언만 따른다", func(t *testing.T) {
		write(t, root, "docs/knowledge/order-status.md", "---\ndoc_type: knowledge\n---\n# 주문 상태\nOrderStatusEnum 은 CREATED, PAID 두 가지다.\n")
		d.Trigger("watch")
		write(t, root, "docs/knowledge/order-status-v2.md", "---\ndoc_type: knowledge\nsupersedes: docs/knowledge/order-status.md\n---\n# 주문 상태\nOrderStatusEnum 은 CREATED, PAID, SHIPPED 세 가지다.\n")
		d.Trigger("watch")
		if got := searchPaths(t, cfg, "OrderStatusEnum", false); !reflect.DeepEqual(got, []string{"docs/knowledge/order-status-v2.md"}) {
			t.Errorf("knowledge = %v", got)
		}
	})

	t.Run("증류: 변경분에 요약이 붙는다", func(t *testing.T) {
		d.Settle()
		hits, _ := search.Search(ctx, client, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: "IdempotencyKeyHeader"})
		if len(hits) == 0 || hits[0].Summary != "요약: ADR-0001 멱등 키" {
			t.Errorf("summary = %+v", hits)
		}
	})

	t.Run("재구축 계약: 인덱스를 지워도 같은 결과 — 판정·증류는 원장에서, LLM 0회", func(t *testing.T) {
		d.Settle()
		snapshot := func() []string {
			hits, _ := search.Search(ctx, client, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Size: 50, IncludeSuperseded: true,
				Query: "OrderRetryPolicy RefundWindowDays OrderStatusEnum IdempotencyKeyHeader ShippingZoneTable"})
			var out []string
			for _, h := range hits {
				out = append(out, h.SourcePath+"|"+h.SupersededBy+"|"+h.Summary)
			}
			sort.Strings(out)
			return out
		}
		want := snapshot()
		j0, d0 := ai.counts()
		_ = client.Do(ctx, "DELETE", "/mem-"+cfg.DefaultTeam+"-*", nil, nil, nil)
		d.Trigger("timer")
		d.Settle()
		if got := snapshot(); !reflect.DeepEqual(got, want) {
			t.Errorf("rebuild\n got  %v\n want %v", got, want)
		}
		if j1, d1 := ai.counts(); j1 != j0 || d1 != d0 {
			t.Errorf("LLM 호출 judge %d→%d distill %d→%d", j0, j1, d0, d1)
		}
	})

	t.Run("부분 스캔이면 지우지 않는다: 읽지 못한 디렉토리의 문서는 그대로 남는다", func(t *testing.T) {
		dir := filepath.Join(root, "docs/specs/payments")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o755)
		d.Trigger("timer")
		if got := status(); got.Removed != 0 {
			t.Errorf("partial = %+v", got)
		}
		if got := searchPaths(t, cfg, "RefundWindowDays", false); len(got) != 1 {
			t.Errorf("남아 있어야 한다: %v", got)
		}
	})

	t.Run("tombstone: 파일을 지우면 다음 사이클이 색인에서 뺀다", func(t *testing.T) {
		_ = os.Remove(filepath.Join(root, "main/shop-api/docs/adr/0001-idempotency.md"))
		d.Trigger("watch")
		if got := searchPaths(t, cfg, "IdempotencyKeyHeader", false); len(got) != 0 {
			t.Errorf("tombstone = %v", got)
		}
	})

	t.Run("cold 소유: 이미 cold 로 간 달의 원본은 되살리지 않는다", func(t *testing.T) {
		store, _ := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
		old := filepath.Join(root, "docs/specs/legacy/old.md")
		write(t, root, "docs/specs/legacy/old.md", "---\ndate: "+daysAgo(150)+"\n---\n# 레거시\nColdOwnedToken")
		d.Trigger("watch")
		if _, err := cold.Tier(ctx, client, store, cfg, cold.TierOptions{Team: cfg.DefaultTeam, KeepFiles: true}); err != nil {
			t.Fatal(err)
		}
		d.Trigger("timer")
		if got := status(); got.ColdOwned != 1 || got.Indexed != 0 {
			t.Errorf("cold 소유 = %+v", got)
		}
		if got := searchPaths(t, cfg, "ColdOwnedToken", false); len(got) != 0 {
			t.Errorf("되살아나면 안 된다: %v", got)
		}
		// 같은 달이지만 처음 보는 오래된 문서는 색인한다 (cold 에 보관된 적이 없다)
		write(t, root, "docs/specs/legacy/late.md", "---\ndate: "+daysAgo(150)+"\n---\n# 늦게 온 레거시\nLateArrivalToken")
		d.Trigger("watch")
		if got := searchPaths(t, cfg, "LateArrivalToken", false); len(got) != 1 {
			t.Errorf("처음 보는 문서는 색인 = %v", got)
		}
		// tier 는 ISM·keep-files 로 남은 cold 소유 원본도 정리한다
		decisions, err := cold.Tier(ctx, client, store, cfg, cold.TierOptions{Team: cfg.DefaultTeam})
		if err != nil {
			t.Fatal(err)
		}
		pruned := false
		for _, dec := range decisions {
			pruned = pruned || contains(dec.RemovedFiles, "docs/specs/legacy/old.md")
		}
		if _, err := os.Stat(old); !pruned || !os.IsNotExist(err) {
			t.Errorf("prune = %+v", decisions)
		}
	})

	t.Run("writer 는 하나: 다른 프로세스의 1회 색인은 데몬이 떠 있으면 건너뛴다", func(t *testing.T) {
		repo, _ := filepath.Abs("../..")
		cmd := exec.Command("go", "run", "./app/daemon", "-once")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "META_REPO_ROOT="+root, "MEMORY_DEFAULT_TEAM="+cfg.DefaultTeam, "MEMORY_WORKSPACE="+cfg.Workspace)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "1회 색인을 건너뜁니다") {
			t.Errorf("once = %v\n%s", err, out)
		}
	})
}

// TestDaemonRunWatchAndShutdown 은 상주 루프를 돌린다: 파일 저장 → watcher 힌트(debounce) → 색인,
// 그리고 종료 신호를 주면 루프가 빠져나오고 락이 풀린다.
func TestDaemonRunWatchAndShutdown(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_RECONCILE_DEBOUNCE_MS": "50", "MEMORY_RECONCILE_INTERVAL_SECONDS": "3600"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Ask: &fakeLLM{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(ctx, false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(200 * time.Millisecond) // watch 등록

	write(t, cfg.MetaRepoRoot, "docs/feature/watch/new.md", "# 워처\nWatchHintToken")
	deadline := time.Now().Add(10 * time.Second)
	for len(searchPaths(t, cfg, "WatchHintToken", false)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watcher 힌트로 색인되지 않았다 (새로 만든 하위 디렉토리 watch 포함)")
		}
		time.Sleep(100 * time.Millisecond)
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
	if pid := daemon.RunningPID(cfg.MetaRepoRoot); pid != 0 {
		t.Errorf("락이 남았다: %d", pid)
	}
}
