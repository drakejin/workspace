package mcpserver

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/search"
)

// filtersHint 는 필터 값을 보는 방법이다 — 위 안내문들처럼 MCP 도구와 CLI 명령을 함께 적는다.
const filtersHint = "search_filters(CLI: filters)"

// errEmptyQuery 는 질의어도 필터도 없을 때다. 필터의 MCP 입력 이름과 CLI 플래그를 함께 적는다.
var errEmptyQuery = fmt.Errorf("%w — MCP: date·time_range·project·topic·doc_type, CLI: -date·-from/-to·-project·-topic·-type", search.ErrEmptyQuery)

// filters 는 search_memory 입력의 필터 부분이다 — search_filters 와 같은 요청 조립·범위 표시를 쓴다.
func (in SearchInput) filters() FiltersInput {
	return FiltersInput{Project: in.Project, Topic: in.Topic, Date: in.Date, TimeRange: in.TimeRange, DocType: in.DocType,
		Team: in.Team, Org: in.Org, IncludeSuperseded: in.IncludeSuperseded}
}

// WithQuery 는 같은 필터에 질의어와 결과 수를 더한 search_memory 입력이다 (CLI 의 search·filters 가 필터 플래그를 함께 쓴다).
func (in FiltersInput) WithQuery(query string, size int) SearchInput {
	return SearchInput{Query: query, Size: size, Project: in.Project, Topic: in.Topic, Date: in.Date, TimeRange: in.TimeRange,
		DocType: in.DocType, Team: in.Team, Org: in.Org, IncludeSuperseded: in.IncludeSuperseded}
}

// summary 는 사람이 읽는 필터 요약이다 (결과 머리에 붙여, 어떤 조건으로 가져왔는지 보이게).
func (in FiltersInput) summary() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("project", in.Project)
	add("topic", in.Topic)
	add("date", in.Date)
	if in.TimeRange.From != "" || in.TimeRange.To != "" {
		parts = append(parts, "time_range="+in.TimeRange.From+"~"+in.TimeRange.To)
	}
	add("doc_type", in.DocType)
	if in.IncludeSuperseded {
		parts = append(parts, "include_superseded")
	}
	return strings.Join(parts, ", ")
}

// request 는 검색 요청이다 — 팀(생략하면 기본 팀)·org·필터·질의어를 모아 OpenSearch 에 묻기 전에 검사한다.
// search_memory 와 search_filters 가 같이 쓴다.
func (h *Handlers) request(f FiltersInput, query string, size int) (search.Request, error) {
	team, err := h.team(f.Team)
	if err != nil {
		return search.Request{}, err
	}
	if f.Org != "" && !naming.ValidName(f.Org) {
		return search.Request{}, fmt.Errorf("org 이름이 잘못됐습니다: %q", f.Org)
	}
	docType, err := parseDocType(f.DocType)
	if err != nil {
		return search.Request{}, err
	}
	req := search.Request{
		Workspace: h.Config.Workspace, Team: team, Org: f.Org, Query: query, Size: size, Date: f.Date, TimeRange: f.TimeRange,
		TimeZone: h.Config.QueryTimeZone(time.Now()), Project: f.Project, Topic: f.Topic, DocType: docType, IncludeSuperseded: f.IncludeSuperseded,
	}
	if err := search.Validate(req); err != nil {
		return search.Request{}, withFiltersHint(err)
	}
	return req, nil
}

func parseDocType(s string) (model.DocType, error) {
	if s == "" {
		return "", nil
	}
	t, ok := model.ParseDocType(s)
	if !ok {
		return "", fmt.Errorf("doc_type 이 잘못됐습니다: %q", s)
	}
	return t, nil
}

// withFiltersHint 는 글자 없는 주제 에러에 쓸 수 있는 주제를 보는 방법(도구·CLI 이름)을 붙인다.
func withFiltersHint(err error) error {
	if errors.Is(err, search.ErrBlankTopic) {
		return fmt.Errorf("%w (%s 로 쓸 수 있는 주제를 확인하세요)", err, filtersHint)
	}
	return err
}

// scopeOf 는 결과 머리의 범위다 — 팀(또는 org)과 필터 요약.
func scopeOf(req search.Request, f FiltersInput) string {
	scope := "team=" + req.Team
	if req.Org != "" {
		scope = "org=" + req.Org
	}
	if s := f.summary(); s != "" {
		scope += " · " + s
	}
	return scope
}

// noIndex 는 팀(org) alias 가 없을 때의 안내다 — "문서 0건" 과 구별한다. alias 가 없는 까닭은 이름 오타, 아직 적재 전,
// 문서가 모두 cold 로 내려감(tier 가 마지막 hot 인덱스를 지우면 alias 도 사라진다) 셋이라, 이름 확인과 cold 보는 방법을 함께 적는다.
func (h *Handlers) noIndex(req search.Request) string {
	if req.Org != "" {
		return fmt.Sprintf("org=%s 의 hot 색인이 없습니다 — org 이름이 틀렸거나(팀→org 는 MEMORY_TEAM_ORGS), 그 팀들에 아직 문서가 없거나, "+
			"문서가 모두 %d일이 지나 cold 로 내려간 경우다. 오래된 맥락이면 팀마다 %s 를 확인하세요.", req.Org, h.Config.HotDays, listColdHint)
	}
	return fmt.Sprintf("team=%s 의 hot 색인이 없습니다 — 팀 이름이 틀렸거나(생략하면 기본 팀 %s), 아직 문서를 적재하지 않았거나, "+
		"문서가 모두 %d일이 지나 cold 로 내려간 팀이다. 오래된 맥락이면 %s 를 확인하세요.", req.Team, h.Config.DefaultTeam, h.Config.HotDays, listColdHint)
}

// noHits 는 0건일 때의 안내다 — 필터만 줬으면 쓸 수 있는 값을 보라고, 질의어 검색이면 낱말을 바꾸거나 cold 를 보라고.
func (h *Handlers) noHits(scope, query string) string {
	if strings.TrimSpace(query) == "" {
		return fmt.Sprintf("%s · 필터에 맞는 문서가 없습니다. %s 로 쓸 수 있는 프로젝트·주제·날짜를 확인하세요.", scope, filtersHint)
	}
	return fmt.Sprintf("%s hot 영역에서 %q 에 맞는 문서가 없습니다.\n"+
		"단어(서비스명, 함수명, 에러 코드)를 바꿔 다시 검색하세요. %d일보다 오래된 맥락이면 %s 를 확인하세요.", scope, query, h.Config.HotDays, listColdHint)
}

// renderHits 는 결과 목록이다 — 머리 줄, 그리고 청크마다 위치·메타·(질의어 검색이면) 점수·표시, 요약, 본문.
func renderHits(scope string, listing, orgMode bool, res search.Result) string {
	var b strings.Builder
	b.WriteString(searchHeader(scope, listing, res.Total, res.Hits) + "\n\n")
	for i, hit := range res.Hits {
		score := ""
		if !listing {
			score = fmt.Sprintf("; score %.2f", hit.Score)
		}
		fmt.Fprintf(&b, "[%d] %s › %s  (%s%s)%s\n", i+1, hit.SourcePath, hit.HeaderPath, hitMeta(hit, orgMode), score, hitFlags(hit))
		if hit.Summary != "" {
			fmt.Fprintf(&b, "요약: %s\n", hit.Summary)
		}
		b.WriteString(hit.Text + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// hitMeta 는 결과 한 건의 메타다 — 종류, (org 검색이면) 팀, 프로젝트, 주제, 문서의 날, 커밋.
func hitMeta(hit search.Hit, orgMode bool) string {
	meta := []string{string(hit.DocType)}
	if orgMode {
		meta = append(meta, "team="+hit.Team)
	}
	if hit.Project != "" {
		meta = append(meta, "project="+hit.Project)
	}
	if hit.Topic != "" {
		meta = append(meta, "topic="+hit.Topic)
	}
	if day := hit.Day(); day != "" {
		meta = append(meta, day)
	}
	if len(hit.GitSHA) >= shortSHA {
		meta = append(meta, hit.GitSHA[:shortSHA])
	}
	return strings.Join(meta, ", ")
}

// shortSHA 는 결과에 보일 커밋 해시 길이다.
const shortSHA = 7

// hitFlags 는 밀린·deprecate 된 문서의 표시다 (include_superseded 로 볼 때만 나온다).
func hitFlags(hit search.Hit) string {
	flag := ""
	if hit.SupersededBy != "" {
		flag = "  [superseded → " + hit.SupersededBy + "]"
	}
	if hit.Deprecated {
		flag += "  [deprecated]"
	}
	return flag
}

// searchHeader 는 검색 결과의 머리 줄이다 — 질의어 검색은 청크 단위라 청크 수와 그 청크들의 문서 수를 함께
// (한 문서의 여러 절이 size 를 나눠 쓴 것이 보인다), 필터 가져오기는 문서 하나에 한 건이라 문서 수 (잘렸으면 전체 수도).
func searchHeader(scope string, listing bool, total int, hits []search.Hit) string {
	shown := len(hits)
	switch {
	case listing && total > shown:
		return fmt.Sprintf("%s · 문서 %d건 중 최신 %d건 (필터, 최신순 — %s)", scope, total, shown, moreHint(shown))
	case listing:
		return fmt.Sprintf("%s · 문서 %d건 (필터, 최신순)", scope, shown)
	default:
		return fmt.Sprintf("%s · 청크 %d건 · 문서 %d건 (BM25)", scope, shown, distinctDocs(hits))
	}
}

// moreHint 는 잘린 가져오기에서 더 보는 방법이다. 페이지 넘김은 없으므로, size 가 이미 상한이면 필터를 좁히라고만 한다.
func moreHint(shown int) string {
	if shown < search.MaxSize {
		return fmt.Sprintf("더 보려면 size 를 늘리거나(최대 %d) 필터를 좁히세요", search.MaxSize)
	}
	return fmt.Sprintf("size 는 최대 %d 이라, 더 보려면 time_range·project·topic·doc_type 으로 좁히세요", search.MaxSize)
}

// distinctDocs 는 결과 청크들이 나온 문서(source_path) 수다.
func distinctDocs(hits []search.Hit) int {
	seen := make(map[string]bool, len(hits))
	for _, hit := range hits {
		seen[hit.SourcePath] = true
	}
	return len(seen)
}

// renderFacets 는 search_filters 의 결과다 — 문서 수, 그리고 필터마다 값(문서 수). 0건이면 머리 줄만.
func renderFacets(scope string, includeSuperseded bool, f search.Facets) string {
	hidden := " (밀린·deprecate 된 문서 제외)"
	if includeSuperseded {
		hidden = ""
	}
	head := fmt.Sprintf("%s · 문서 %d건%s", scope, f.Docs, hidden)
	if f.Docs == 0 {
		return head
	}
	return strings.Join([]string{
		head,
		"project: " + countsLine(f.Projects, f.NoProject, f.MoreProjects),
		"topic: " + countsLine(f.Topics, f.NoTopic, f.MoreTopics),
		"doc_type: " + countsLine(f.DocTypes, 0, 0),
		fmt.Sprintf("date: %s ~ %s", f.First, f.Last),
		fmt.Sprintf("날짜별(최신 %d일): %s", len(f.Days), countsLine(f.Days, 0, 0)),
	}, "\n")
}

// countsLine 은 값(문서 수)을 늘어놓은 줄이다 — 그 필터 값이 없는 문서는 (없음)(N), 상한에 잘린 값들은 그 문서 수를 덧붙인다.
func countsLine(cs []search.Count, none, more int) string {
	parts := make([]string, 0, len(cs)+2)
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s(%d)", c.Value, c.Docs))
	}
	if none > 0 {
		parts = append(parts, fmt.Sprintf("(없음)(%d)", none))
	}
	if more > 0 {
		parts = append(parts, fmt.Sprintf("외 문서 %d건(값이 %d개를 넘어 상위만 보임 — 필터로 좁혀 보세요)", more, search.MaxFacetValues))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}
