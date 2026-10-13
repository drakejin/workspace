package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

func TestBuildQuery(t *testing.T) {
	q := BuildQuery(Request{Workspace: "ws", Team: "ads", Query: "dedup", TimeRange: model.TimeRange{From: "now-30d"}, Project: "attribution", DocType: model.DocADR, Size: 500})
	if q["size"] != MaxSize {
		t.Errorf("size = %v", q["size"])
	}
	b := q["query"].(map[string]any)["bool"].(map[string]any)
	if len(b["filter"].([]any)) != 4 || b["must_not"] == nil {
		t.Errorf("bool = %v", b)
	}
	plain := BuildQuery(Request{Team: "a", Query: "x", IncludeSuperseded: true})["query"].(map[string]any)["bool"].(map[string]any)
	if len(plain["filter"].([]any)) != 0 || plain["must_not"] != nil {
		t.Errorf("plain = %v", plain)
	}
	if BuildQuery(Request{Query: "x"})["size"] != defaultSize {
		t.Error("default size")
	}
}

type stubOS struct {
	path string
	resp any
	err  error
}

func (s *stubOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	s.path = path
	if s.err != nil {
		return s.err
	}
	b, _ := json.Marshal(s.resp)
	return json.Unmarshal(b, out)
}

func (s *stubOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

func TestSearch(t *testing.T) {
	os := &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{map[string]any{
		"_index": "mem-ads-2026.10", "_score": 3.2,
		"_source": map[string]any{"source_path": "docs/a.md", "header_path": "A", "doc_type": "note", "summary": "s", "superseded_by": "docs/b.md"},
	}}}}}
	res, err := Search(context.Background(), os, Request{Team: "ads", Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	hits := res.Hits
	want := []Hit{{Score: 3.2, SourcePath: "docs/a.md", HeaderPath: "A", DocType: model.DocNote, Summary: "s", SupersededBy: "docs/b.md"}}
	if !reflect.DeepEqual(hits, want) || os.path != "/mem-ads-hot/_search" {
		t.Errorf("hits = %+v path=%s", hits, os.path)
	}
}

func TestSearchErrors(t *testing.T) {
	ctx := context.Background()
	if res, err := Search(ctx, &stubOS{err: &opensearch.Error{Status: 404}}, Request{Team: "a", Query: "q"}); err != nil || res.Hits != nil {
		t.Errorf("alias 가 없으면 빈 결과: %v %v", res, err)
	}
	if _, err := Search(ctx, &stubOS{err: &opensearch.Error{Status: 500, Reason: "boom"}}, Request{Team: "a", Query: "q"}); err == nil {
		t.Error("다른 에러는 그대로")
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "a", Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Error("질의도 필터도 없으면 거부")
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "a", Date: "2026-10-6"}); err == nil {
		t.Error("date 는 YYYY-MM-DD")
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "a", Project: "bad\nproject"}); err == nil {
		t.Error("제어 문자가 든 project")
	}
	if _, err := Search(ctx, &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{}}}}, Request{Team: "a", Project: "결제 서비스"}); err != nil {
		t.Errorf("색인이 받는 프로젝트 이름은 검색도 받는다: %v", err)
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "Bad", Query: "q"}); err == nil {
		t.Error("잘못된 team")
	}
}

func TestSearchOrgAlias(t *testing.T) {
	os := &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{}}}}
	if _, err := Search(context.Background(), os, Request{Org: "ads", Query: "x"}); err != nil || os.path != "/mem-org-ads-hot/_search" {
		t.Errorf("org alias = %s %v", os.path, err)
	}
	if _, err := Search(context.Background(), os, Request{Org: "Bad Org", Query: "x"}); err == nil {
		t.Error("잘못된 org")
	}
	q := BuildQuery(Request{Query: "x"})["query"].(map[string]any)["bool"].(map[string]any)
	if len(q["must_not"].([]any)) != 2 {
		t.Error("밀린 문서와 deprecated 를 숨긴다")
	}
}

func filterOf(t *testing.T, q map[string]any) []any {
	t.Helper()
	return q["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
}

func TestFiltersDateRangeTopicProject(t *testing.T) {
	// 날짜는 문서의 날(event_day: frontmatter 날짜 그대로, 아니면 설정한 시간대의 날)로 맞춘다
	q := BuildQuery(Request{Query: "x", Date: "2026-10-06", Topic: "Payments", Project: "alpha-api"})
	got, _ := json.Marshal(filterOf(t, q))
	want := `[{"term":{"event_day":"2026-10-06"}},{"term":{"repo":{"case_insensitive":true,"value":"alpha-api"}}},{"term":{"topic":"payments"}}]`
	if string(got) != want {
		t.Errorf("date·project·topic\n got  %s\n want %s", got, want)
	}
	// 날짜만 준 범위는 그날 전체를 포함한다 (event_day 끼리 비교)
	q = BuildQuery(Request{Query: "x", TimeRange: model.TimeRange{From: "2026-10-01", To: "2026-10-05"}})
	got, _ = json.Marshal(filterOf(t, q))
	if string(got) != `[{"range":{"event_day":{"gte":"2026-10-01"}}},{"range":{"event_day":{"lte":"2026-10-05"}}}]` {
		t.Errorf("range = %s", got)
	}
	// date math 는 유효 시각에 시간대를 붙여 (now-3d/d 같은 반올림이 그 시간대의 하루가 되게)
	q = BuildQuery(Request{Query: "x", TimeZone: "+09:00", TimeRange: model.TimeRange{From: "now-7d", To: "2026-10-05"}})
	got, _ = json.Marshal(filterOf(t, q))
	if string(got) != `[{"range":{"event_time":{"gte":"now-7d","time_zone":"+09:00"}}},{"range":{"event_day":{"lte":"2026-10-05"}}}]` {
		t.Errorf("date math = %s", got)
	}
}

// R4-query-a-2: OpenSearch 2.19 는 time_zone 을 epoch 밀리초에도 입혀 UTC 벽시계를 그 시간대의 벽시계로 읽는다 (서울이면
// 9시간 이르다 — 경계보다 이른 문서가 조용히 섞였다). epoch 기준은 같은 시각의 UTC 표기로 바꿔 보낸다: 시간대를 적은 시각은
// OpenSearch 가 그대로 읽고, 뒤의 date math 만 time_zone 에서 계산한다 — 다른 경계와 같다.
func TestEpochBoundIsSentAsUTCInstant(t *testing.T) {
	q := BuildQuery(Request{Query: "x", TimeZone: "Asia/Seoul", TimeRange: model.TimeRange{From: "1759312800000", To: "1759312800000||+1d/d"}})

	got, _ := json.Marshal(filterOf(t, q))

	want := `[{"range":{"event_time":{"gte":"2025-10-01T10:00:00.000Z","time_zone":"Asia/Seoul"}}},` +
		`{"range":{"event_time":{"lte":"2025-10-01T10:00:00.000Z||+1d/d","time_zone":"Asia/Seoul"}}}]`
	if string(got) != want {
		t.Errorf("epoch 경계\n got  %s\n want %s", got, want)
	}
}

func TestFilterOnlyListingIsOneHitPerDocumentNewestFirst(t *testing.T) {
	q := BuildQuery(Request{Project: "beta-web"})
	b := q["query"].(map[string]any)["bool"].(map[string]any)
	if _, scored := b["must"]; scored {
		t.Error("질의가 없으면 점수 없이 필터만")
	}
	if q["collapse"] == nil || q["sort"] == nil {
		t.Errorf("문서 하나에 한 건, 최신순: %v", q)
	}
	if !HasFilter(Request{Topic: "x"}) || HasFilter(Request{Query: "x", Size: 3}) {
		t.Error("HasFilter")
	}
	if q["track_total_hits"] != true {
		t.Error("가져오기는 맞는 문서 전체 수를 센다")
	}
	os := &stubOS{resp: map[string]any{"hits": map[string]any{"total": map[string]any{"value": 12}, "hits": []any{
		map[string]any{"_source": map[string]any{"source_path": "docs/a.md", "chunk_index": 0}},
	}}}}
	res, err := Search(context.Background(), os, Request{Team: "a", Project: "beta-web", Size: 1})
	if err != nil || res.Total != 12 || len(res.Hits) != 1 {
		t.Errorf("필터만으로 가져오기: %+v %v", res, err)
	}
}

func TestFacets(t *testing.T) {
	os := &stubOS{resp: map[string]any{
		"hits": map[string]any{"total": map[string]any{"value": 5}},
		"aggregations": map[string]any{
			"project":    map[string]any{"buckets": []any{map[string]any{"key": "alpha-api", "doc_count": 3}}},
			"no_project": map[string]any{"doc_count": 2},
			"topic":      map[string]any{"buckets": []any{map[string]any{"key": "payments", "doc_count": 2}}},
			"no_topic":   map[string]any{"doc_count": 3},
			"doc_type":   map[string]any{"buckets": []any{map[string]any{"key": "adr", "doc_count": 4}}},
			"days": map[string]any{"buckets": []any{
				map[string]any{"key": "2026-10-11", "doc_count": 1},
				map[string]any{"key": "2026-07-01", "doc_count": 2},
				map[string]any{"key": "2026-10-12", "doc_count": 2},
			}},
		},
	}}
	f, err := FetchFacets(context.Background(), os, Request{Team: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Docs != 5 || f.Projects[0] != (Count{"alpha-api", 3}) || f.NoProject != 2 || f.Topics[0] != (Count{"payments", 2}) ||
		f.DocTypes[0] != (Count{"adr", 4}) || f.First != "2026-07-01" || f.Last != "2026-10-12" || f.Days[0] != (Count{"2026-10-12", 2}) {
		t.Errorf("facets = %+v", f)
	}
	if os.path != "/mem-a-hot/_search" {
		t.Errorf("path = %s", os.path)
	}
}

// 주제·프로젝트 이름도 질의어로 맞는다 — topic 이 frontmatter 에만 있는 문서도 "onboarding" 질의로 찾게
func TestQueryAlsoMatchesTopicAndProjectNames(t *testing.T) {
	q := BuildQuery(Request{Query: "onboarding"})
	mm := q["query"].(map[string]any)["bool"].(map[string]any)["must"].([]any)[0].(map[string]any)["multi_match"].(map[string]any)
	fields := strings.Join(mm["fields"].([]string), ",")
	if !strings.Contains(fields, "topic.text") || !strings.Contains(fields, "repo.text") {
		t.Errorf("fields = %s", fields)
	}
}
