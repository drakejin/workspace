//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/naming"
)

// TestSearchInputMistakesAreExplained 는 검색 입력 실수가 조용한 0건이나 원인 없는 "all shards failed" 가 아니라 고칠 수 있는
// 말로 돌아오는지 본다 (API-3 · RUN-10 · RUN-18) — 프로젝트는 대소문자를 가리지 않고, 시간대는 IANA 이름으로 넘긴다 (API-13).
func TestSearchInputMistakesAreExplained(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_TIME_ZONE": "America/Los_Angeles"}))
	ctx := context.Background()
	doc := "docs/repository/alpha-api/adr/0001-ledger-lock.md"
	write(t, m.cfg.MetaRepoRoot, doc, "---\ndate: "+daysAgo(2)+"\n---\n# ADR-0001 원장 잠금\nLedgerLockInputToken")
	dstDoc := "docs/repository/alpha-api/adr/0002-dst-probe.md"
	dstRange, dstTime := dstProbe(t)
	write(t, m.cfg.MetaRepoRoot, dstDoc, "---\nevent_time: "+dstTime+"\n---\n# ADR-0002 DST\nDstProbeInputToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	found := []struct {
		name string
		args map[string]any
		doc  string
	}{
		{"대소문자가 다른 프로젝트", map[string]any{"project": "Alpha-API"}, doc},
		{"서머타임이 지금과 반대인 날의 시각 경계 (time_zone=America/Los_Angeles)", map[string]any{"query": "DstProbeInputToken", "time_range": dstRange}, dstDoc},
		{"시각 경계", map[string]any{"query": "LedgerLockInputToken", "time_range": map[string]any{"to": "2999-01-01T00:00"}}, doc},
		{"연월·숫자 없는 date math 경계", map[string]any{"query": "LedgerLockInputToken", "time_range": map[string]any{"from": "2000-01", "to": "now+d"}}, doc},
	}
	for _, c := range found {
		if text, isErr := m.call(t, "search_memory", c.args); isErr || !strings.Contains(text, c.doc) {
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
		{"뒤집힌 date math 범위", "search_filters", map[string]any{"time_range": map[string]any{"from": "now-1d", "to": "now-30d"}}, []string{"뒤집혔"}},
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

// dstProbe 는 서머타임이 지금과 반대인 날(1월 또는 7월 15일)의 LA 자정을 시간대 없이 적은 경계와, 그 경계를 IANA 이름
// (America/Los_Angeles)으로 읽을 때만 범위에 드는 문서 시각이다 — 지금의 고정 오프셋(-07:00 또는 -08:00)으로 읽으면 경계가
// 한 시간 어긋나 문서가 빠진다 (API-13). 언제 돌려도 성립하게 지금의 오프셋에 맞춰 날과 경계 쪽(from·to)을 고른다.
func dstProbe(t *testing.T) (map[string]any, string) {
	t.Helper()
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(la)
	_, nowOffset := now.Zone()
	day := time.Date(now.Year(), time.January, 15, 0, 0, 0, 0, la)
	if _, off := day.Zone(); off == nowOffset {
		day = time.Date(now.Year(), time.July, 15, 0, 0, 0, 0, la)
	}
	fixed := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.FixedZone("", nowOffset))
	between := day.Add(fixed.Sub(day) / 2)
	side := "to" // 고정 오프셋 경계가 이르면 lte 가 그 사이 문서를 빠뜨린다
	if fixed.After(day) {
		side = "from" // 고정 오프셋 경계가 늦으면 gte 가 빠뜨린다
	}
	return map[string]any{side: day.Format("2006-01-02T15:04")}, between.UTC().Format(time.RFC3339)
}

// TestSearchOfTieredTeamPointsToCold 는 리뷰가 찾은 회귀의 재현이다. 팀의 hot 인덱스가 모두 tier 되면 hot alias 가 사라져
// 검색은 "색인이 없습니다" 로 답한다. 이때도 cold 를 보는 방법(list_cold)을 알려야 한다 — 끝난 프로젝트 팀의 오래된 맥락으로
// 가는 길은 이 안내뿐이다 (search_memory 설명은 list_cold 를 말하지 않는다).
func TestSearchOfTieredTeamPointsToCold(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	index := ingestOne(t, m, "docs/specs/legacy-queue.md", daysAgo(hotDays+60), "LegacyQueueToken")
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier = %+v", d)
	}
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"search_memory", map[string]any{"query": "LegacyQueueToken"}},
		{"search_filters", map[string]any{}},
	}
	for _, c := range calls {
		text, isErr := m.call(t, c.tool, c.args)

		if isErr || !strings.Contains(text, "색인이 없습니다") || !strings.Contains(text, "list_cold(CLI: list-cold)") {
			t.Errorf("%s: cold 를 보라고 알린다: %s", c.tool, text)
		}
	}
	if text, _ := m.call(t, "list_cold", map[string]any{}); !strings.Contains(text, index) {
		t.Errorf("list_cold 에는 있다: %s", text)
	}
}
