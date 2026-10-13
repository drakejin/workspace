//go:build e2e

package e2e

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// listed 는 search_memory 결과에서 source_path 목록을 뽑는다 (결과 줄: "[n] path › header  (...)").
var listedPath = regexp.MustCompile(`(?m)^\[\d+\] (\S+) › `)

func listed(text string) []string {
	var out []string
	for _, m := range listedPath.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// TestSearchFiltersDateRangeTopicProject 는 search_memory 의 필터 — 날짜(date)·범위(time_range)·주제(topic)·프로젝트(project)·
// 문서 종류(doc_type) — 와 질의어 없는 가져오기, search_filters 가 MCP 로 그대로 동작하는지 본다.
func TestSearchFiltersDateRangeTopicProject(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	d := func(n int) string { return daysAgo(n) }
	write(t, root, "docs/repository/alpha-api/adr/0001-ledger-lock.md", "---\ndate: "+d(2)+"\ntopic: payments\n---\n# ADR-0001 원장 잠금\nAlphaLedgerLockToken")
	write(t, root, "docs/repository/beta-web/adr/0001-ranking-cache.md", "---\ndate: "+d(2)+"\n---\n# ADR-0001 랭킹 캐시\nBetaRankingCacheToken")
	write(t, root, "docs/feature/payments/refund-flow.md", "---\ndoc_type: spec\ndate: "+d(5)+"\n---\n# 환불 흐름\nRefundFlowToken")
	write(t, root, "docs/specs/onboarding/plan.md", "---\nproject: alpha-api\ndate: "+d(9)+"\n---\n# 온보딩 plan\nOnboardingPlanToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	search := func(args map[string]any) []string {
		t.Helper()
		text, isErr := m.call(t, "search_memory", args)
		if isErr {
			t.Fatalf("search_memory %v → %s", args, text)
		}
		return listed(text)
	}
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"날짜 하루", map[string]any{"date": d(2)}, []string{"docs/repository/alpha-api/adr/0001-ledger-lock.md", "docs/repository/beta-web/adr/0001-ranking-cache.md"}},
		{"기간 범위 (날짜만 주면 양 끝 날 포함)", map[string]any{"time_range": map[string]any{"from": d(5), "to": d(2)}},
			[]string{"docs/feature/payments/refund-flow.md", "docs/repository/alpha-api/adr/0001-ledger-lock.md", "docs/repository/beta-web/adr/0001-ranking-cache.md"}},
		{"주제 (frontmatter topic 과 docs/feature/{주제} 경로)", map[string]any{"topic": "Payments"},
			[]string{"docs/feature/payments/refund-flow.md", "docs/repository/alpha-api/adr/0001-ledger-lock.md"}},
		{"프로젝트 (경로 docs/repository/{p} 와 frontmatter project)", map[string]any{"project": "alpha-api"},
			[]string{"docs/repository/alpha-api/adr/0001-ledger-lock.md", "docs/specs/onboarding/plan.md"}},
		{"문서 종류", map[string]any{"doc_type": "adr"}, []string{"docs/repository/alpha-api/adr/0001-ledger-lock.md", "docs/repository/beta-web/adr/0001-ranking-cache.md"}},
		{"조합: 프로젝트 + 주제 + 범위", map[string]any{"project": "alpha-api", "topic": "payments", "time_range": map[string]any{"from": d(3)}},
			[]string{"docs/repository/alpha-api/adr/0001-ledger-lock.md"}},
		{"질의어 + 필터", map[string]any{"query": "RefundFlowToken", "topic": "payments"}, []string{"docs/feature/payments/refund-flow.md"}},
		{"질의어 + 다른 프로젝트 → 없음", map[string]any{"query": "RefundFlowToken", "project": "beta-web"}, nil},
		{"주제 이름을 질의어로 (본문에 그 낱말이 없어도)", map[string]any{"query": "payments", "project": "alpha-api"},
			[]string{"docs/repository/alpha-api/adr/0001-ledger-lock.md"}},
	}
	for _, c := range cases {
		if got := search(c.args); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s\n got  %v\n want %v", c.name, got, c.want)
		}
	}
	if text, isErr := m.call(t, "search_memory", map[string]any{"date": "2026-1-2"}); !isErr {
		t.Errorf("잘못된 date 는 도구 에러: %s", text)
	}
	if text, isErr := m.call(t, "search_memory", map[string]any{}); !isErr {
		t.Errorf("질의어도 필터도 없으면 도구 에러: %s", text)
	}

	text, isErr := m.call(t, "search_filters", map[string]any{})
	if isErr {
		t.Fatal(text)
	}
	for _, want := range []string{"문서 4건", "alpha-api(2)", "beta-web(1)", "payments(2)", "onboarding(1)", "adr(2)", "date: " + d(9) + " ~ " + d(2)} {
		if !strings.Contains(text, want) {
			t.Errorf("search_filters 에 %q 가 없다:\n%s", want, text)
		}
	}
	if text, _ := m.call(t, "search_filters", map[string]any{"project": "alpha-api"}); !strings.Contains(text, "문서 2건") || !strings.Contains(text, "project=alpha-api") {
		t.Errorf("search_filters 범위 = %s", text)
	}
}

// TestSearchFiltersUseTheDocumentsLocalDay 는 날짜 필터가 UTC 가 아니라 문서의 날로 맞는지 본다 — frontmatter 날짜가 없는
// 문서는 MEMORY_TIME_ZONE 에서 본 유효 시각(커밋·mtime)의 날이다. KST 오전 8시 반에 쓴 문서는 UTC 로는 전날이다.
// 가져오기가 잘렸으면 전체 수를 알려 주는지, 공백이 든 프로젝트 이름도 필터로 맞는지도 본다.
func TestSearchFiltersUseTheDocumentsLocalDay(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_TIME_ZONE": "Asia/Seoul"}))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	base := time.Now().UTC().AddDate(0, 0, -3)
	written := time.Date(base.Year(), base.Month(), base.Day(), 23, 30, 0, 0, time.UTC)
	utcDay, kstDay := written.Format("2006-01-02"), written.Add(24*time.Hour).Format("2006-01-02")
	abs := write(t, root, "docs/specs/morning/note.md", "# 아침 메모\nMorningNoteToken")
	if err := os.Chtimes(abs, written, written); err != nil {
		t.Fatal(err)
	}
	write(t, root, "docs/specs/morning/plan.md", "---\nproject: 결제 서비스\ndate: "+kstDay+"\n---\n# 아침 plan\nMorningPlanToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"KST 의 날", map[string]any{"date": kstDay}, []string{"docs/specs/morning/note.md", "docs/specs/morning/plan.md"}},
		{"UTC 의 날에는 없다", map[string]any{"date": utcDay}, nil},
		{"범위 끝 날짜도 문서의 날", map[string]any{"time_range": map[string]any{"from": kstDay, "to": kstDay}}, []string{"docs/specs/morning/note.md", "docs/specs/morning/plan.md"}},
		{"공백이 든 프로젝트 이름", map[string]any{"project": "결제 서비스"}, []string{"docs/specs/morning/plan.md"}},
	}
	for _, c := range cases {
		text, isErr := m.call(t, "search_memory", c.args)
		if got := listed(text); isErr || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s\n got  %v (%s)\n want %v", c.name, got, text, c.want)
		}
	}
	text, _ := m.call(t, "search_memory", map[string]any{"topic": "morning", "size": 1})
	if !strings.Contains(text, "문서 2건 중 최신 1건") || !strings.Contains(text, kstDay) {
		t.Errorf("잘린 가져오기는 전체 수를 알린다:\n%s", text)
	}
	if text, _ := m.call(t, "search_filters", map[string]any{}); !strings.Contains(text, kstDay+"(2)") || strings.Contains(text, utcDay) {
		t.Errorf("날짜별 수도 문서의 날로 센다:\n%s", text)
	}
}
