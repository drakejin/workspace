package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// API-3 / RUN-10: time_range 경계는 날짜(YYYY-MM-DD)·시각(ISO)·date math(now±N단위, /단위 반올림, 날짜||math) 만 받는다 —
// 예전에는 'yesterday' 같은 값이 Validate 를 지나 OpenSearch 에서 원인 없는 "all shards failed" 로 돌아왔다.
func TestValidateTimeRangeBounds(t *testing.T) {
	accepted := []string{
		"2026-10-01", "now", "now-30d", "now-7d/d", "now/d", "now-1M+2h/d", "now/d+1h", "now-90d/d", "now+1y", "now-15m", "now-2w",
		"2026-10-01T10:00:00", "2026-10-01T10:00:00Z", "2026-10-01T10:00:00.123+09:00", "2026-10-01T10:00", "2026-10-01T10:00Z",
		"2026-10-01||+1M/d", "2026-10-01T10:00:00Z||-1d",
	}
	for _, v := range accepted {
		for _, r := range []Request{{TimeRange: model.TimeRange{From: v}}, {TimeRange: model.TimeRange{To: v}}} {
			if err := Validate(r); err != nil {
				t.Errorf("%q 는 받는다: %v", v, err)
			}
		}
	}
	rejected := []string{"yesterday", "garbage", "2026-02-30T00:00:00", "2026-13-01", "now-30", "now-3x", "now-30d/", "now - 3d",
		"2026/10/01", "1696118400000", "2026-10-01||yesterday", "||+1d", "2026-10-01T25:00"}
	for _, v := range rejected {
		err := Validate(Request{TimeRange: model.TimeRange{From: v}})

		if err == nil || !strings.Contains(err.Error(), "time_range.from") || !strings.Contains(err.Error(), v) || !strings.Contains(err.Error(), "now-30d") {
			t.Errorf("%q 는 받을 수 있는 꼴을 알리며 거부: %v", v, err)
		}
	}
	if err := Validate(Request{TimeRange: model.TimeRange{To: "yesterday"}}); err == nil || !strings.Contains(err.Error(), "time_range.to") {
		t.Errorf("to 도 같다: %v", err)
	}
}

// RUN-18: 날짜 범위가 뒤집히면(from 이 to 보다 늦다) 조용한 0건 대신 무엇이 잘못됐는지 알린다.
func TestValidateRejectsReversedDayRange(t *testing.T) {
	err := Validate(Request{TimeRange: model.TimeRange{From: "2026-10-13", To: "2026-10-03"}})

	if err == nil || !strings.Contains(err.Error(), "2026-10-13") || !strings.Contains(err.Error(), "2026-10-03") || !strings.Contains(err.Error(), "늦") {
		t.Errorf("뒤집힌 범위: %v", err)
	}
	for _, ok := range []model.TimeRange{{From: "2026-10-03", To: "2026-10-03"}, {From: "2026-10-03", To: "2026-10-13"}, {From: "2026-10-13", To: "now"}} {
		if err := Validate(Request{TimeRange: ok}); err != nil {
			t.Errorf("%+v 는 받는다: %v", ok, err)
		}
	}
}

// RUN-10: 아주 긴 질의어는 OpenSearch 가 원인 모를 400(graph 분석 재귀 한도)으로 거부한다 — 미리 낱말 수로 막고 줄이라고 알린다.
func TestValidateRejectsVeryLongQuery(t *testing.T) {
	long := strings.Repeat("RefundSplitter ", maxQueryWords+1)

	err := Validate(Request{Query: long})

	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Errorf("긴 질의어: %v", err)
	}
	if err := Validate(Request{Query: strings.Repeat("RefundSplitter ", maxQueryWords)}); err != nil {
		t.Errorf("상한까지는 받는다: %v", err)
	}
}

// RUN-18: 프로젝트 필터는 대소문자를 가리지 않는다 (Alpha-API 로 alpha-api 를 찾는다 — 주제처럼). 색인 값은 그대로 두고
// 질의의 term 에 case_insensitive 를 건다.
func TestProjectFilterIgnoresCase(t *testing.T) {
	q := BuildQuery(Request{Query: "x", Project: " Alpha-API "})

	got, _ := json.Marshal(filterOf(t, q))

	if want := `[{"term":{"repo":{"case_insensitive":true,"value":"Alpha-API"}}}]`; string(got) != want {
		t.Errorf("project filter\n got  %s\n want %s", got, want)
	}
}

// RUN-18: 없는 팀·org(alias 404)는 "문서 0건" 과 구별해 알린다 — 검색도 필터 값 세기도.
func TestMissingAliasIsReported(t *testing.T) {
	ctx := context.Background()
	notFound := &stubOS{err: &opensearch.Error{Status: http.StatusNotFound, Type: "index_not_found_exception"}}

	res, err := Search(ctx, notFound, Request{Org: "nosuchorg", Query: "memory"})
	f, ferr := FetchFacets(ctx, notFound, Request{Team: "nosuchteam"})

	if err != nil || !res.NoIndex || len(res.Hits) != 0 {
		t.Errorf("Search = %+v %v", res, err)
	}
	if ferr != nil || !f.NoIndex {
		t.Errorf("FetchFacets = %+v %v", f, ferr)
	}
	empty := &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{}}}}
	if res, _ := Search(ctx, empty, Request{Team: "a", Query: "x"}); res.NoIndex {
		t.Error("alias 가 있는 0건은 NoIndex 가 아니다")
	}
}

// API-8: 필터 값이 상한(maxFacetValues)을 넘으면 잘린 값들의 문서 수(sum_other_doc_count)를 함께 돌려준다.
func TestFacetsReportTruncatedValues(t *testing.T) {
	os := &stubOS{resp: map[string]any{
		"hits": map[string]any{"total": map[string]any{"value": 120}},
		"aggregations": map[string]any{
			"project": map[string]any{"sum_other_doc_count": 7, "buckets": []any{map[string]any{"key": "alpha-api", "doc_count": 3}}},
			"topic":   map[string]any{"sum_other_doc_count": 0, "buckets": []any{map[string]any{"key": "payments", "doc_count": 2}}},
		},
	}}

	f, err := FetchFacets(context.Background(), os, Request{Team: "a"})

	if err != nil || f.MoreProjects != 7 || f.MoreTopics != 0 {
		t.Errorf("facets = %+v %v", f, err)
	}
}

// R2-deadcode-3: 검색 층은 CLI 명령 이름을 모른다 — 도구·CLI 이름 안내는 mcpserver 가 붙인다. 원인은 sentinel 로 구별한다.
func TestSearchErrorsDoNotNameCLICommands(t *testing.T) {
	topic := Validate(Request{Topic: "!!!"})

	if !errors.Is(topic, ErrBlankTopic) || strings.Contains(topic.Error(), "CLI") || strings.Contains(topic.Error(), "search_filters") {
		t.Errorf("topic err = %v", topic)
	}
	if strings.Contains(ErrEmptyQuery.Error(), "CLI") || strings.Contains(ErrEmptyQuery.Error(), "MCP") {
		t.Errorf("ErrEmptyQuery = %v", ErrEmptyQuery)
	}
}
