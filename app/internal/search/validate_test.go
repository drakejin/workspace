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

// API-3 / RUN-10: time_range 경계는 OpenSearch 가 읽는 꼴(strict_date_optional_time||epoch_millis, date math) 중 흔한 것만 받는다 —
// 예전에는 'yesterday' 같은 값이 Validate 를 지나 OpenSearch 에서 원인 없는 "all shards failed" 로 돌아왔다. 받는 꼴은
// OpenSearch 2.19 에 그대로 보내 확인한 목록이다 (연·연월, 시만 적은 시각, 숫자 없는 now-d, epoch 밀리초도 OpenSearch 가 받는다).
func TestValidateTimeRangeBounds(t *testing.T) {
	accepted := []string{
		"2026-10-01", "2026-09", "2026", "2028-02-29",
		"now", "now-30d", "now-7d/d", "now/d", "now-1M+2h/d", "now/d+1h", "now-90d/d", "now+1y", "now-15m", "now-2w", "now-d", "now+d/d", "now-01d", "now/d/M",
		"2026-10-01T10", "2026-10-01T10:00", "2026-10-01T10:00:00", "2026-10-01T10:00:00.5", "2026-10-01T10:00:00.123456789",
		"2026-10-01T10Z", "2026-10-01T10:00Z", "2026-10-01T10:00:00Z", "2026-10-01T10:00+09", "2026-10-01T10:00+0900", "2026-10-01T10:00:00.123+09:00",
		"1759312800000",
		"2026-10-01||+1M/d", "2026-10-01T10:00:00Z||-1d", "2026-09||+M", "2026||+1y/M", "1759312800000||+1d", "2026-10-01T10||-1h",
	}
	for _, v := range accepted {
		for _, r := range []Request{{TimeRange: model.TimeRange{From: v}}, {TimeRange: model.TimeRange{To: v}}} {
			if err := Validate(r); err != nil {
				t.Errorf("%q 는 받는다: %v", v, err)
			}
		}
	}
	rejected := []string{"yesterday", "garbage", "NOW", "2026-02-30T00:00:00", "2026-02-29", "2026-13-01", "2026-1-1", "26-10-01", "+2026-10-01",
		"2026/10/01", "2026-10-01 10:00", "2026-10-01t10:00", "2026-10-01T1", "2026-10-01T25:00", "2026-10-01T10:60", "2026-10-01Z", "2026-10-01+09:00",
		"2026-10-01T10:00:00[Asia/Seoul]", "2026-10-01T10:00+19:00", "-1000", "now-30", "now-3x", "now-1D", "now-1.5d", "now+-1d", "now-30d/", "now - 3d",
		"2026-10-01||yesterday", "||+1d"}
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

// RUN-18: 범위가 뒤집히면(from 이 가리키는 가장 이른 시각이 to 가 가리키는 가장 늦은 시각보다 늦다) 조용한 0건 대신
// 무엇이 잘못됐는지 알린다 — 날짜끼리만이 아니라 시각·date math 가 섞여도. OpenSearch 처럼 to 는 올려 읽는다
// (now/d 는 오늘 끝까지, 2026-10 은 OpenSearch 규칙대로 10월 1일 끝까지 — 그래서 읽은 시각도 함께 알린다).
func TestValidateRejectsReversedRange(t *testing.T) {
	reversed := []model.TimeRange{
		{From: "2026-10-13", To: "2026-10-03"},
		{From: "2026-10-13T00:00", To: "2026-10-03"},
		{From: "now-1d", To: "now-30d"},
		{From: "now/d", To: "now-1d/d"},
		{From: "2026-11", To: "2026-10-31"},
		{From: "2026-10-03T10:00Z", To: "2026-10-03T18:00+09:00"},
		{From: "2026-10-03||+1d", To: "2026-10-03"},
		{From: "now", To: "2000-01-01"},
		{From: "2026-10-02", To: "2026-10"},
		{From: "1759312800000", To: "2025-10-01T10:00"}, // epoch(10:00Z = 서울 19:00)는 서울 10:00 보다 늦다
	}
	for _, tr := range reversed {
		err := Validate(Request{TimeRange: tr, TimeZone: "Asia/Seoul"})

		if err == nil || !strings.Contains(err.Error(), "뒤집혔") || !strings.Contains(err.Error(), tr.From) || !strings.Contains(err.Error(), tr.To) {
			t.Errorf("뒤집힌 범위 %+v: %v", tr, err)
		}
	}
	ok := []model.TimeRange{
		{From: "2026-10-03", To: "2026-10-03"}, {From: "2026-10-03", To: "2026-10-13"}, {From: "2026-10-13", To: "now"},
		{From: "2026-10-03", To: "2026-10-03T00:00"}, {From: "2026-10-03T23:00", To: "2026-10-03"},
		{From: "2026-10", To: "2026-10-01"}, {From: "2026-10-02", To: "2026-10||/M"},
		{From: "now-30d", To: "now-1d"}, {From: "now/d", To: "now/d"}, {From: "now-1d/d", To: "now/d"}, {From: "2000-01-01", To: "now"},
		{From: "2026-01-31||+1M", To: "2026-02-28"}, // 달 더하기는 그달 끝에서 멈춘다 (1/31 + 1M = 2/28) — OpenSearch 와 같다
		{From: "2026-10-03T18:00+09:00", To: "2026-10-03T10:00Z"},
		{From: "1759312800000", To: "2025-10-01T19:00"}, // epoch 는 그 시각 그대로다 — 2025-10-01T10:00Z 는 서울 19:00
	}
	err := Validate(Request{TimeRange: model.TimeRange{From: "2026-10-02", To: "2026-10"}, TimeZone: "Asia/Seoul"})
	if err == nil || !strings.Contains(err.Error(), "2026-10-01T23:59:59.999+09:00") {
		t.Errorf("읽은 시각을 함께 알린다: %v", err)
	}
	for _, tr := range ok {
		if err := Validate(Request{TimeRange: tr, TimeZone: "Asia/Seoul"}); err != nil {
			t.Errorf("%+v 는 받는다: %v", tr, err)
		}
	}
}

// R4-query-a-1: UTC 동쪽 시간대의 서머타임 틈(베를린 02:00~03:00)에 걸친 범위는 뒤집히지 않았다 — OpenSearch 는 to 02:30 을
// 틈 앞의 오프셋으로 읽어 03:30 CEST 까지로 본다. 예전에는 to 를 한 시간 이르게(01:30) 읽어 "뒤집혔다" 고 거부했다.
func TestValidateAcceptsRangesAcrossDSTGapsEastOfUTC(t *testing.T) {
	ranges := []struct {
		zone string
		tr   model.TimeRange
	}{
		{"Europe/Berlin", model.TimeRange{From: "2026-03-29T01:45", To: "2026-03-29T02:30"}},
		{"Europe/London", model.TimeRange{From: "2026-03-29T00:45", To: "2026-03-29T01:30"}},
		{"America/Los_Angeles", model.TimeRange{From: "2026-03-08T01:45", To: "2026-03-08T02:30"}},
		// 겹침(01:00~02:00 이 두 번)의 01:30 을 OpenSearch 는 GMT(01:30Z)로 읽는다 — 00:50Z 부터는 뒤집히지 않았다
		{"Europe/London", model.TimeRange{From: "2026-10-25T00:50Z", To: "2026-10-25T01:30"}},
	}
	for _, r := range ranges {
		if err := Validate(Request{TimeRange: r.tr, TimeZone: r.zone}); err != nil {
			t.Errorf("%s %+v 는 받는다: %v", r.zone, r.tr, err)
		}
	}
}

// R4-query-a-2: epoch 밀리초는 OpenSearch 의 epoch_millis 처럼 네 자리 연도 안의 시각만 받는다 — 보낼 때 UTC 시각
// (2025-10-01T10:00:00.000Z)으로 바꾸므로, 그 꼴로 적을 수 없는 시각(10000년 이후)은 OpenSearch 에 닿기 전에 거부한다.
func TestValidateEpochWithinFourDigitYears(t *testing.T) {
	if err := Validate(Request{TimeRange: model.TimeRange{From: "253402300799999"}}); err != nil { // 9999-12-31T23:59:59.999Z
		t.Errorf("9999년 끝까지는 받는다: %v", err)
	}
	for _, v := range []string{"253402300800000", "99999999999999999"} {
		if err := Validate(Request{TimeRange: model.TimeRange{From: v}}); err == nil || !strings.Contains(err.Error(), "epoch 밀리초") {
			t.Errorf("%q 는 받을 수 있는 꼴을 알리며 거부: %v", v, err)
		}
	}
}

// R4-query-a-5: 받는 꼴은 OpenSearch 가 읽는 꼴의 일부다 — OpenSearch 가 읽더라도 드문 꼴(쉼표 소수, 초까지 적은 오프셋, 소수 epoch,
// 끝의 T, 다섯 자리 연도, 오프셋 뒤의 Z)과 없는 날(OpenSearch 는 lte 에서 그달 끝으로 당긴다)은 받지 않는다. 거부는 안전한
// 쪽이다 — 무엇이 틀렸는지 알리고, 여기서 받은 값을 OpenSearch 가 거부하는 일은 없다 (TestValidateTimeRangeBounds 의 목록).
func TestValidateRejectsRareFormsOpenSearchAlsoReads(t *testing.T) {
	for _, v := range []string{"2026-10-01T10:00:00,5", "2026-10-01T10:00:00+09:00:00", "1759312800000.5", "2026-10-01T", "+12345-01-01",
		"2026-10-01T10:00+09:00Z", "2026-02-29T00:00", "2026-02-30", "2026-04-31"} {
		if err := Validate(Request{TimeRange: model.TimeRange{To: v}}); err == nil || !strings.Contains(err.Error(), "time_range.to") {
			t.Errorf("%q 는 받을 수 있는 꼴을 알리며 거부: %v", v, err)
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
	q := buildQuery(Request{Query: "x", Project: " Alpha-API "})

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
