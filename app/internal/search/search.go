// Package search 는 팀 hot alias 에 대한 BM25 검색이다.
package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	TimeZone          string          // date math·시각 경계를 읽을 시간대 (Asia/Seoul, 이름이 없으면 +09:00). 비면 UTC
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

// boundFilter 는 범위 경계 하나다. 날짜만 준 경계는 문서의 날(event_day)끼리 비교하고(그날 전체 포함),
// date math·시각은 유효 시각(event_time)에 시간대를 붙여 비교한다. epoch 밀리초는 같은 시각의 UTC 표기로 바꿔 보낸다 —
// OpenSearch 가 epoch 에도 시간대를 입혀 엉뚱한 시각으로 읽는다 (epochAsISO).
func boundFilter(op, v, timeZone string) map[string]any {
	if isDay(v) {
		return map[string]any{"range": map[string]any{"event_day": map[string]any{op: v}}}
	}
	rng := map[string]any{op: epochAsISO(v)}
	if timeZone != "" {
		rng["time_zone"] = timeZone
	}
	return map[string]any{"range": map[string]any{"event_time": rng}}
}

// Hit 는 검색 결과 청크 하나다.
type Hit struct {
	Team         string
	Score        float64
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

const defaultSize = 8

// MaxSize 는 결과 수의 상한이다. 더 큰 size 는 이 값으로 자른다.
const MaxSize = 50

// Result 는 검색 결과다. Total 은 필터로만 가져올 때 맞는 문서 전체 수다 (Hits 는 그중 최신 Size 건, 질의어 검색이면 0).
// NoIndex 는 팀(org) alias 가 없다는 뜻이다 — 그 팀(org)에 색인된 문서가 아직 없거나 이름이 틀렸다 (alias 가 있는 0건과 구별한다).
type Result struct {
	Hits    []Hit
	Total   int
	NoIndex bool
}

// ErrEmptyQuery 는 질의어도 필터도 없다는 뜻이다. 필터의 MCP 입력 이름·CLI 플래그는 부르는 쪽(mcpserver)이 붙인다.
var ErrEmptyQuery = errors.New("query 가 비어 있습니다 (질의어 없이 가져오려면 필터를 하나 이상 주세요)")

// BuildQuery 는 BM25 multi_match + 필터 질의다.
func BuildQuery(r Request) map[string]any {
	size := r.Size
	if size <= 0 {
		size = defaultSize
	}
	if size > MaxSize {
		size = MaxSize
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
		// 프로젝트는 대소문자를 가리지 않는다 (Alpha-API 로 alpha-api 를) — 색인 값은 디렉토리·frontmatter 철자 그대로라 질의에서 맞춘다
		filter = append(filter, map[string]any{"term": map[string]any{"repo": map[string]any{"value": project, "case_insensitive": true}}})
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
	// 본문이 있는 청크만 돌려준다 — 본문이 없는 문서(빈 파일·frontmatter 만)의 청크 0 은 text 가 없다. 필터로만 가져오기(최신순)의
	// 맨 위를 빈 결과로 차지해 size 안의 진짜 문서를 밀어내거나 세션 훅의 맥락에 들어가지 않게 (색인에는 남아 search_filters 의 수에는 든다)
	query["post_filter"] = map[string]any{"exists": map[string]any{"field": "text"}}
	return query
}

// aliasOf 는 요청이 묻는 alias 다 — org 가 있으면 org alias(그 org 의 모든 팀), 없으면 팀 hot alias.
func aliasOf(r Request) (string, error) {
	if r.Org != "" {
		if !naming.ValidName(r.Org) {
			return "", fmt.Errorf("org 이름이 잘못됐습니다: %q", r.Org)
		}
		return naming.OrgAlias(r.Org), nil
	}
	if err := naming.ValidateTeam(r.Team); err != nil {
		return "", err
	}
	return naming.HotAlias(r.Team), nil
}

// Search 는 팀 hot alias 에 묻는다. alias 가 없으면(그 팀에 문서가 아직 없거나 이름이 틀렸다) NoIndex 인 빈 결과.
func Search(ctx context.Context, c opensearch.Client, r Request) (Result, error) {
	// 필터 값부터 본다 — 정규화하면 비는 필터만 준 요청이 "질의어가 비었다" 가 아니라 무엇이 잘못됐는지로 거부되게
	if err := Validate(r); err != nil {
		return Result{}, err
	}
	listing := strings.TrimSpace(r.Query) == ""
	if listing && !HasFilter(r) {
		return Result{}, ErrEmptyQuery
	}
	alias, err := aliasOf(r)
	if err != nil {
		return Result{}, err
	}
	var out opensearch.SearchResponse[model.Chunk]
	err = c.Do(ctx, http.MethodPost, "/"+alias+"/_search", BuildQuery(r), nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return Result{NoIndex: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		s := h.Source
		hits = append(hits, Hit{
			Team:  s.Team,
			Score: h.Score, SourcePath: s.SourcePath, HeaderPath: s.HeaderPath, Project: s.Repo, Topic: s.Topic,
			DocType: s.DocType, EventTime: s.EventTime, EventDay: s.EventDay, GitSHA: s.GitSHA, Text: s.Text, Summary: s.Summary, SupersededBy: s.SupersededBy, Deprecated: s.Deprecated,
		})
	}
	res := Result{Hits: hits}
	if listing {
		res.Total = out.Hits.Total.Value
	}
	return res, nil
}
