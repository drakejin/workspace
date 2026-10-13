// Package search 는 팀 hot alias 에 대한 BM25 검색이다.
package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// Request 는 검색 요청이다. Query 가 비어도 필터(Date·TimeRange·Project·Topic·DocType)가 하나라도 있으면
// 필터에 맞는 문서를 최신순으로 가져온다 (문서 하나에 한 건).
type Request struct {
	Workspace         string // 비면 모든 workspace
	Team              string
	Org               string // 있으면 팀 대신 org("실") alias — 그 org 의 모든 팀을 함께 검색
	Query             string
	Date              string          // 날짜: 그날 하루 (YYYY-MM-DD) — 문서의 날(event_day)과 맞춘다
	TimeRange         model.TimeRange // 범위: from~to. 날짜만 주면 그날 전체를 포함한다. date math(now-30d) 도 된다
	TimeZone          string          // date math·시각 경계를 읽을 시간대 (+09:00). 비면 UTC
	Project           string          // 프로젝트 — 관리 대상 repository 이름 (색인 필드 repo)
	Topic             string          // 주제 — docs/{feature,specs,issues}/{주제}/ 또는 frontmatter topic
	DocType           model.DocType
	Size              int
	IncludeSuperseded bool // 판정·선언으로 밀린 문서와 deprecate 된 문서도 본다 (기본: 숨김)
}

// HasFilter 는 질의어 말고 문서를 좁히는 조건이 있는가다. 프로젝트·주제는 BuildQuery 와 같은 정규화한 값으로 본다.
func HasFilter(r Request) bool {
	return r.Date != "" || r.TimeRange.From != "" || r.TimeRange.To != "" || document.NormalizeProject(r.Project) != "" ||
		document.NormalizeTopic(r.Topic) != "" || r.DocType != ""
}

var dayOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isDay(v string) bool {
	if !dayOnly.MatchString(v) {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

// Validate 는 필터 값을 검사한다.
func Validate(r Request) error {
	if r.Date != "" && !isDay(r.Date) {
		return fmt.Errorf("date 는 YYYY-MM-DD 여야 합니다: %q", r.Date)
	}
	for name, v := range map[string]string{"time_range.from": r.TimeRange.From, "time_range.to": r.TimeRange.To} {
		if dayOnly.MatchString(v) && !isDay(v) {
			return fmt.Errorf("%s 날짜가 잘못됐습니다: %q", name, v)
		}
	}
	return validateNames(r)
}

// validateNames 는 프로젝트·주제 값을 검사한다. 색인이 받은 이름이면 무엇이든 되므로 제어 문자만 막되, 준 값이 정규화하면
// 비는 경우(topic "!!!", project "  ")는 거부한다 — 조용히 필터를 빼면 결과 머리에는 걸러진 것처럼 보이면서 전체 문서가 나온다.
func validateNames(r Request) error {
	for name, v := range map[string]string{"project": r.Project, "topic": r.Topic} {
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s 에 제어 문자가 있습니다: %q", name, v)
		}
	}
	if r.Topic != "" && document.NormalizeTopic(r.Topic) == "" {
		return fmt.Errorf("topic 에 글자·숫자가 없습니다: %q (search_filters 로 쓸 수 있는 주제를 확인하세요)", r.Topic)
	}
	if r.Project != "" && document.NormalizeProject(r.Project) == "" {
		return fmt.Errorf("project 가 공백뿐입니다: %q", r.Project)
	}
	return nil
}

// boundFilter 는 범위 경계 하나다. 날짜만 준 경계는 문서의 날(event_day)끼리 비교하고(그날 전체 포함),
// date math·시각은 유효 시각(event_time)에 시간대를 붙여 비교한다.
func boundFilter(op, v, timeZone string) map[string]any {
	if isDay(v) {
		return map[string]any{"range": map[string]any{"event_day": map[string]any{op: v}}}
	}
	rng := map[string]any{op: v}
	if timeZone != "" {
		rng["time_zone"] = timeZone
	}
	return map[string]any{"range": map[string]any{"event_time": rng}}
}

// Hit 는 검색 결과 청크 하나다.
type Hit struct {
	Team         string
	Score        float64
	Index        string
	SourcePath   string
	HeaderPath   string
	Project      string
	Topic        string
	DocType      model.DocType
	EventTime    string
	EventDay     string
	GitSHA       string
	Text         string
	Summary      string
	SupersededBy string
	Deprecated   bool
}

// Day 는 결과에 보일 문서의 날이다. event_day 가 없는 예전 판 청크는 유효 시각의 날짜 부분.
func (h Hit) Day() string {
	if h.EventDay != "" {
		return h.EventDay
	}
	return model.Day(h.EventTime)
}

const (
	defaultSize = 8
	maxSize     = 50
)

// Result 는 검색 결과다. Total 은 필터로만 가져올 때 맞는 문서 전체 수다 (Hits 는 그중 최신 Size 건, 질의어 검색이면 0).
type Result struct {
	Hits  []Hit
	Total int
}

// ErrEmptyQuery 는 질의어도 필터도 없다는 뜻이다.
var ErrEmptyQuery = errors.New("query 가 비어 있습니다 (질의어 없이 가져오려면 date·time_range·project·topic·doc_type 중 하나를 주세요)")

// BuildQuery 는 BM25 multi_match + 필터 질의다.
func BuildQuery(r Request) map[string]any {
	size := r.Size
	if size <= 0 {
		size = defaultSize
	}
	if size > maxSize {
		size = maxSize
	}
	filter := []any{}
	if r.Workspace != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"workspace": r.Workspace}})
	}
	if r.Date != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"event_day": r.Date}})
	}
	if r.TimeRange.From != "" {
		filter = append(filter, boundFilter("gte", r.TimeRange.From, r.TimeZone))
	}
	if r.TimeRange.To != "" {
		filter = append(filter, boundFilter("lte", r.TimeRange.To, r.TimeZone))
	}
	if project := document.NormalizeProject(r.Project); project != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"repo": project}})
	}
	if topic := document.NormalizeTopic(r.Topic); topic != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"topic": topic}})
	}
	if r.DocType != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"doc_type": r.DocType}})
	}
	boolQ := map[string]any{"filter": filter}
	query := map[string]any{"size": size, "query": map[string]any{"bool": boolQ}}
	if strings.TrimSpace(r.Query) != "" {
		boolQ["must"] = []any{map[string]any{"multi_match": map[string]any{"query": r.Query, "fields": []string{"text", "header_path^2", "source_path.text", "title^2", "summary", "topic.text^2", "repo.text"}}}}
	} else {
		// 필터로만 가져오기 — 점수가 없으니 문서 하나에 한 건(대표 청크), 최신순
		boolQ["filter"] = append(filter, map[string]any{"term": map[string]any{"chunk_index": 0}})
		query["sort"] = []any{map[string]any{"event_time": "desc"}, map[string]any{"source_path": "asc"}}
		query["collapse"] = map[string]any{"field": "source_path"}
		query["track_total_hits"] = true
	}
	if !r.IncludeSuperseded {
		boolQ["must_not"] = []any{
			map[string]any{"exists": map[string]any{"field": "superseded_by"}},
			map[string]any{"term": map[string]any{"deprecated": true}},
		}
	}
	return query
}

// Search 는 팀 hot alias 에 묻는다. alias 가 아직 없으면(문서 0건) 빈 결과.
func Search(ctx context.Context, c opensearch.Client, r Request) (Result, error) {
	// 필터 값부터 본다 — 정규화하면 비는 필터만 준 요청이 "질의어가 비었다" 가 아니라 무엇이 잘못됐는지로 거부되게
	if err := Validate(r); err != nil {
		return Result{}, err
	}
	listing := strings.TrimSpace(r.Query) == ""
	if listing && !HasFilter(r) {
		return Result{}, ErrEmptyQuery
	}
	alias := naming.HotAlias(r.Team)
	if r.Org != "" {
		if !naming.ValidName(r.Org) {
			return Result{}, fmt.Errorf("org 이름이 잘못됐습니다: %q", r.Org)
		}
		alias = naming.OrgAlias(r.Org)
	} else if err := naming.ValidateTeam(r.Team); err != nil {
		return Result{}, err
	}
	var out opensearch.SearchResponse[model.Chunk]
	err := c.Do(ctx, http.MethodPost, "/"+alias+"/_search", BuildQuery(r), nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		s := h.Source
		hits = append(hits, Hit{
			Team:  s.Team,
			Score: h.Score, Index: h.Index, SourcePath: s.SourcePath, HeaderPath: s.HeaderPath, Project: s.Repo, Topic: s.Topic,
			DocType: s.DocType, EventTime: s.EventTime, EventDay: s.EventDay, GitSHA: s.GitSHA, Text: s.Text, Summary: s.Summary, SupersededBy: s.SupersededBy, Deprecated: s.Deprecated,
		})
	}
	res := Result{Hits: hits}
	if listing {
		res.Total = out.Hits.Total.Value
	}
	return res, nil
}
