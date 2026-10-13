package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
)

// brokenColdOS 는 cold 설정이 잘못된 클러스터다 — 스냅샷 repository PUT 이 500 이고, 읽기(GET·_search)는 된다.
// 받은 요청을 적어 둔다.
type brokenColdOS struct {
	mu    sync.Mutex
	calls []string
}

func (o *brokenColdOS) Do(_ context.Context, method, path string, _ any, _ url.Values, out any) error {
	o.mu.Lock()
	o.calls = append(o.calls, method+" "+path)
	o.mu.Unlock()
	if method == http.MethodPut && strings.HasPrefix(path, "/_snapshot/") {
		return &opensearch.Error{Op: method + " " + path, Status: http.StatusInternalServerError, Reason: "repository type [s3] does not exist"}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`), out)
}

func (o *brokenColdOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// writes 는 받은 요청 중 클러스터에 무엇을 쓰는 것이다 (GET 과 _search 말고).
func (o *brokenColdOS) writes() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, c := range o.calls {
		if !strings.HasPrefix(c, http.MethodGet+" ") && !strings.HasSuffix(c, "/_search") {
			out = append(out, c)
		}
	}
	return out
}

// API-1: 읽기 도구는 쓰기 쪽 Bootstrap(템플릿·매핑·ISM·스냅샷 repository·org alias)에 묶이지 않는다 — cold 설정이 잘못돼도
// 이미 색인된 문서는 검색되고, 읽기 호출은 클러스터에 아무것도 쓰지 않는다. 쓰기 쪽 Ready 는 그대로 Bootstrap 한다.
func TestReadToolsDoNotBootstrap(t *testing.T) {
	os := &brokenColdOS{}
	cfg := config.Config{Workspace: "ws", DefaultTeam: "ads", HotDays: 60, Snapshot: config.Snapshot{Repository: "mem-cold", Type: "s3", S3Bucket: "b"}}
	h := NewHandlers(Deps{Config: cfg, OS: os})
	ctx := context.Background()

	_, searchErr := h.Search(ctx, SearchInput{Query: "hello"})
	_, filtersErr := h.Filters(ctx, FiltersInput{})
	_, coldErr := h.ListCold(ctx, ListColdInput{})

	if searchErr != nil || filtersErr != nil || coldErr != nil {
		t.Errorf("읽기 도구가 실패했다: search=%v filters=%v list_cold=%v", searchErr, filtersErr, coldErr)
	}
	if w := os.writes(); len(w) != 0 {
		t.Errorf("읽기 도구가 클러스터에 썼다: %v", w)
	}
	if err := h.Ready(ctx); err == nil || !strings.Contains(err.Error(), "snapshot repository") {
		t.Errorf("쓰기 쪽 Ready 는 Bootstrap 한다: %v", err)
	}
}

// notFoundOS 는 모든 요청에 404(alias 없음)로 답한다.
type notFoundOS struct{}

func (notFoundOS) Do(context.Context, string, string, any, url.Values, any) error {
	return &opensearch.Error{Status: http.StatusNotFound, Type: "index_not_found_exception", Reason: "no such index"}
}

func (notFoundOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// RUN-18: 없는 팀·org 는 "문서 0건" 과 구별해 알린다 (검색도 필터 값도). alias 가 없는 까닭은 이름 오타·아직 적재 전·
// 문서가 모두 cold 로 내려감 셋이라, 이름만 의심하지 않고 cold 를 보는 방법(list_cold·CLI)도 함께 적는다 — 끝난 프로젝트의
// 팀은 hot 인덱스가 모두 tier 되면 alias 가 사라지는데, 이 안내가 cold 로 가는 유일한 길잡이다.
func TestMissingTeamOrOrgIsNamed(t *testing.T) {
	h := &Handlers{Deps: Deps{Config: config.Config{Workspace: "ws", DefaultTeam: "default", HotDays: 60}, OS: notFoundOS{}}, ready: true}
	ctx := context.Background()
	cases := []struct {
		name string
		call func() (string, error)
		want []string
	}{
		{"없는 org 검색", func() (string, error) { return h.Search(ctx, SearchInput{Org: "nosuchorg", Query: "memory"}) },
			[]string{"org=nosuchorg", "색인이 없습니다", "MEMORY_TEAM_ORGS", "cold", "list_cold(CLI: list-cold)"}},
		{"없는 팀 검색", func() (string, error) { return h.Search(ctx, SearchInput{Team: "nosuchteam", Query: "memory"}) },
			[]string{"team=nosuchteam", "색인이 없습니다", "기본 팀 default", "cold", "60일", "list_cold(CLI: list-cold)"}},
		{"없는 팀 필터 값", func() (string, error) { return h.Filters(ctx, FiltersInput{Team: "nosuchteam"}) },
			[]string{"team=nosuchteam", "색인이 없습니다", "list_cold(CLI: list-cold)"}},
	}
	for _, c := range cases {
		out, err := c.call()

		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: %q 가 없다: %s", c.name, want, out)
			}
		}
		if strings.Contains(out, "맞는 문서가 없습니다") {
			t.Errorf("%s: 0건 안내와 구별해야 한다: %s", c.name, out)
		}
	}
}

// API-3 / RUN-18: 잘못된 time_range(꼴이 아니거나 뒤집힌 날짜)·org 이름은 OpenSearch 에 묻기 전에 거부한다 (OS 가 nil 이어도).
func TestTimeRangeAndOrgValidatedBeforeIO(t *testing.T) {
	h := NewHandlers(Deps{Config: config.Config{DefaultTeam: "ads"}})
	ctx := context.Background()
	bad := []model.TimeRange{{From: "yesterday"}, {To: "2026-02-30T00:00:00"}, {From: "2026-10-13", To: "2026-10-03"}}
	for _, tr := range bad {
		if _, err := h.Search(ctx, SearchInput{TimeRange: tr}); err == nil || !strings.Contains(err.Error(), "time_range") {
			t.Errorf("search_memory %+v: %v", tr, err)
		}
		if _, err := h.Filters(ctx, FiltersInput{TimeRange: tr}); err == nil || !strings.Contains(err.Error(), "time_range") {
			t.Errorf("search_filters %+v: %v", tr, err)
		}
	}
	if _, err := h.Search(ctx, SearchInput{Org: "Bad Org", Query: "x"}); err == nil || !strings.Contains(err.Error(), "org") {
		t.Errorf("잘못된 org: %v", err)
	}
	if _, err := h.Search(ctx, SearchInput{IncludeSuperseded: true}); !errors.Is(err, search.ErrEmptyQuery) {
		t.Errorf("질의어도 필터도 없음: %v", err)
	}
}

// API-5: 결과 한 건의 표시 — 밀린 문서·deprecate 표시, org 검색의 팀, 질의어 검색의 점수, 요약 줄.
func TestRenderHitsShowsFlagsTeamAndScore(t *testing.T) {
	hits := []search.Hit{
		{Team: "ads-data", SourcePath: "docs/a.md", HeaderPath: "A", DocType: model.DocSpec, Project: "alpha-api", Topic: "payments",
			EventDay: "2026-10-06", GitSHA: "0123456789", Score: 3.2, Summary: "요약문", Text: "본문 A", SupersededBy: "docs/b.md", Deprecated: true},
		{Team: "ads-web", SourcePath: "docs/c.md", HeaderPath: "C", DocType: model.DocNote, Text: "본문 C"},
	}
	cases := []struct {
		name    string
		listing bool
		orgMode bool
		want    []string
		notWant []string
	}{
		{"질의어 검색, org", false, true, []string{
			"[1] docs/a.md › A  (spec, team=ads-data, project=alpha-api, topic=payments, 2026-10-06, 0123456; score 3.20)  [superseded → docs/b.md]  [deprecated]\n요약: 요약문\n본문 A",
			"[2] docs/c.md › C  (note, team=ads-web; score 0.00)\n본문 C",
		}, nil},
		{"필터 가져오기, 팀", true, false, []string{"[1] docs/a.md › A  (spec, project=alpha-api, topic=payments, 2026-10-06, 0123456)  [superseded → docs/b.md]  [deprecated]"},
			[]string{"team=ads-data", "score"}},
	}
	for _, c := range cases {
		out := renderHits("org=ads", c.listing, c.orgMode, search.Result{Hits: hits, Total: 2})

		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: %q 가 없다:\n%s", c.name, want, out)
			}
		}
		for _, no := range c.notWant {
			if strings.Contains(out, no) {
				t.Errorf("%s: %q 는 없어야 한다:\n%s", c.name, no, out)
			}
		}
	}
}

// API-8: 잘린 가져오기의 안내 — size 가 이미 상한(50)이면 size 를 늘리라고 하지 않고 필터를 좁히라고 한다.
func TestTruncatedListingHint(t *testing.T) {
	hits := make([]search.Hit, search.MaxSize)
	for i := range hits {
		hits[i] = search.Hit{SourcePath: "docs/x.md"}
	}

	atCap := searchHeader("team=a", true, 120, hits)
	belowCap := searchHeader("team=a", true, 120, hits[:8])

	if strings.Contains(atCap, "size 를 늘리") || !strings.Contains(atCap, "문서 120건 중 최신 50건") || !strings.Contains(atCap, "time_range") {
		t.Errorf("상한에서 잘림: %s", atCap)
	}
	if !strings.Contains(belowCap, "size 를 늘리거나") || !strings.Contains(belowCap, "최대 50") {
		t.Errorf("상한 아래에서 잘림: %s", belowCap)
	}
}

// API-5 / API-8: search_filters 의 표시 — 값이 없는 문서 (없음)(N), 상한에 잘린 값들의 문서 수, 0건이면 머리 줄만.
func TestRenderFacets(t *testing.T) {
	f := search.Facets{
		Docs: 12, Projects: []search.Count{{Value: "alpha-api", Docs: 4}}, NoProject: 7, MoreProjects: 3,
		Topics: []search.Count{{Value: "payments", Docs: 2}}, NoTopic: 9, DocTypes: []search.Count{{Value: "adr", Docs: 1}},
		First: "2026-06-15", Last: "2026-10-13", Days: []search.Count{{Value: "2026-10-13", Docs: 6}},
	}

	out := renderFacets("team=demo", false, f)
	empty := renderFacets("team=demo · topic=x", true, search.Facets{})

	want := "team=demo · 문서 12건 (밀린·deprecate 된 문서 제외)\n" +
		"project: alpha-api(4) (없음)(7) 외 문서 3건(값이 100개를 넘어 상위만 보임 — 필터로 좁혀 보세요)\n" +
		"topic: payments(2) (없음)(9)\n" +
		"doc_type: adr(1)\n" +
		"date: 2026-06-15 ~ 2026-10-13\n" +
		"날짜별(최신 1일): 2026-10-13(6)"
	if out != want {
		t.Errorf("facets\n got  %q\n want %q", out, want)
	}
	if empty != "team=demo · topic=x · 문서 0건" {
		t.Errorf("0건 = %q", empty)
	}
}

// API-5: 0건 안내 두 가지 — 필터만 줬으면 쓸 수 있는 값을 보라고, 질의어 검색이면 낱말을 바꾸거나 cold 를 보라고.
func TestNoHitsTexts(t *testing.T) {
	h := &Handlers{Deps: Deps{Config: config.Config{HotDays: 60}}}

	listing := h.noHits("team=a · project=p", "  ")
	query := h.noHits("team=a", "RefundSplitter")

	if want := "team=a · project=p · 필터에 맞는 문서가 없습니다. search_filters(CLI: filters) 로 쓸 수 있는 프로젝트·주제·날짜를 확인하세요."; listing != want {
		t.Errorf("필터 0건\n got  %q\n want %q", listing, want)
	}
	if want := "team=a hot 영역에서 \"RefundSplitter\" 에 맞는 문서가 없습니다.\n" +
		"단어(서비스명, 함수명, 에러 코드)를 바꿔 다시 검색하세요. 60일보다 오래된 맥락이면 list_cold(CLI: list-cold) 를 확인하세요."; query != want {
		t.Errorf("질의어 0건\n got  %q\n want %q", query, want)
	}
}
