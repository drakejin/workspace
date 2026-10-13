//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/naming"
)

// TestSearchInputMistakesAreExplained 는 검색 입력 실수가 조용한 0건이나 원인 없는 "all shards failed" 가 아니라 고칠 수 있는
// 말로 돌아오는지 본다 (API-3 · RUN-10 · RUN-18) — 프로젝트는 대소문자를 가리지 않고, 시간대는 IANA 이름으로 넘긴다 (API-13).
func TestSearchInputMistakesAreExplained(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_TIME_ZONE": "America/Los_Angeles"}))
	ctx := context.Background()
	doc := "docs/repository/alpha-api/adr/0001-ledger-lock.md"
	write(t, m.cfg.MetaRepoRoot, doc, "---\ndate: "+daysAgo(2)+"\n---\n# ADR-0001 원장 잠금\nLedgerLockInputToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	found := []struct {
		name string
		args map[string]any
	}{
		{"대소문자가 다른 프로젝트", map[string]any{"project": "Alpha-API"}},
		{"DST 를 넘는 date math (time_zone=America/Los_Angeles)", map[string]any{"time_range": map[string]any{"from": "now-200d/d"}}},
		{"시각 경계", map[string]any{"query": "LedgerLockInputToken", "time_range": map[string]any{"to": "2999-01-01T00:00"}}},
	}
	for _, c := range found {
		if text, isErr := m.call(t, "search_memory", c.args); isErr || !strings.Contains(text, doc) {
			t.Errorf("%s: %s", c.name, text)
		}
	}
	explained := []struct {
		name string
		tool string
		args map[string]any
		want []string
	}{
		{"날짜가 아닌 경계", "search_memory", map[string]any{"time_range": map[string]any{"from": "yesterday"}}, []string{"time_range.from", "now-30d", "yesterday"}},
		{"없는 날의 시각", "search_filters", map[string]any{"time_range": map[string]any{"to": "2026-02-30T00:00:00"}}, []string{"time_range.to"}},
		{"뒤집힌 날짜 범위", "search_memory", map[string]any{"time_range": map[string]any{"from": daysAgo(2), "to": daysAgo(9)}}, []string{"뒤집혔"}},
	}
	for _, c := range explained {
		text, isErr := m.call(t, c.tool, c.args)
		for _, want := range c.want {
			if !isErr || !strings.Contains(text, want) {
				t.Errorf("%s: 도구 에러로 %q 를 알린다: %s", c.name, want, text)
			}
		}
	}
	for _, args := range []map[string]any{{"org": "o" + m.cfg.DefaultTeam + "-none", "query": "memory"}, {"team": m.cfg.DefaultTeam + "-none", "query": "memory"}} {
		if text, isErr := m.call(t, "search_memory", args); isErr || !strings.Contains(text, "색인이 없습니다") {
			t.Errorf("없는 팀·org %v: %s", args, text)
		}
	}
	// OpenSearch 의 400 은 root_cause 를 함께 보인다 (검증을 거치지 않는 경로에서도 원인을 알 수 있게)
	bad := map[string]any{"query": map[string]any{"range": map[string]any{"event_time": map[string]any{"gte": "yesterday"}}}}
	err := m.os.Do(ctx, http.MethodPost, "/"+naming.HotAlias(m.cfg.DefaultTeam)+"/_search", bad, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "parse_exception") || !strings.Contains(err.Error(), "yesterday") {
		t.Errorf("root_cause: %v", err)
	}
}
