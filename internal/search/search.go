// Package search 는 팀 hot alias 에 대한 BM25 검색이다.
package search

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Request 는 검색 요청이다.
type Request struct {
	Workspace         string // 비면 모든 workspace
	Team              string
	Query             string
	TimeRange         model.TimeRange
	Repo              string
	DocType           model.DocType
	Size              int
	IncludeSuperseded bool // 판정·선언으로 밀린 문서도 본다 (기본: 숨김)
}

// Hit 는 검색 결과 청크 하나다.
type Hit struct {
	Score        float64
	Index        string
	SourcePath   string
	HeaderPath   string
	Repo         string
	DocType      model.DocType
	EventTime    string
	GitSHA       string
	Text         string
	Summary      string
	SupersededBy string
}

const (
	defaultSize = 8
	maxSize     = 50
)

// ErrEmptyQuery 는 빈 질의다.
var ErrEmptyQuery = errors.New("query 가 비어 있습니다")

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
	if r.TimeRange.From != "" || r.TimeRange.To != "" {
		rng := map[string]any{}
		if r.TimeRange.From != "" {
			rng["gte"] = r.TimeRange.From
		}
		if r.TimeRange.To != "" {
			rng["lte"] = r.TimeRange.To
		}
		filter = append(filter, map[string]any{"range": map[string]any{"event_time": rng}})
	}
	if r.Repo != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"repo": r.Repo}})
	}
	if r.DocType != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"doc_type": r.DocType}})
	}
	boolQ := map[string]any{
		"must":   []any{map[string]any{"multi_match": map[string]any{"query": r.Query, "fields": []string{"text", "header_path^2", "source_path.text", "title^2", "summary"}}}},
		"filter": filter,
	}
	if !r.IncludeSuperseded {
		boolQ["must_not"] = []any{map[string]any{"exists": map[string]any{"field": "superseded_by"}}}
	}
	return map[string]any{"size": size, "query": map[string]any{"bool": boolQ}}
}

// Search 는 팀 hot alias 에 묻는다. alias 가 아직 없으면(문서 0건) 빈 결과.
func Search(ctx context.Context, c opensearch.Client, r Request) ([]Hit, error) {
	if strings.TrimSpace(r.Query) == "" {
		return nil, ErrEmptyQuery
	}
	if err := naming.ValidateTeam(r.Team); err != nil {
		return nil, err
	}
	var out struct {
		Hits struct {
			Hits []struct {
				Index  string      `json:"_index"`
				Score  float64     `json:"_score"`
				Source model.Chunk `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	err := c.Do(ctx, http.MethodPost, "/"+naming.HotAlias(r.Team)+"/_search", BuildQuery(r), nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		s := h.Source
		hits = append(hits, Hit{
			Score: h.Score, Index: h.Index, SourcePath: s.SourcePath, HeaderPath: s.HeaderPath, Repo: s.Repo,
			DocType: s.DocType, EventTime: s.EventTime, GitSHA: s.GitSHA, Text: s.Text, Summary: s.Summary, SupersededBy: s.SupersededBy,
		})
	}
	return hits, nil
}
