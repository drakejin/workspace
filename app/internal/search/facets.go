package search

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/drakejin/workspace/app/internal/opensearch"
)

// Count 는 필터 값 하나와 그 값을 가진 문서 수다.
type Count struct {
	Value string
	Docs  int
}

// Facets 는 검색 필터에 쓸 수 있는 값이다 — 프로젝트·주제·문서 종류·날짜. search_memory 의 필터를 고르기 전에 본다.
type Facets struct {
	Docs      int
	Projects  []Count
	NoProject int
	// MoreProjects 는 값이 MaxFacetValues 개를 넘어 잘린 프로젝트들의 문서 수다 (0 이면 다 보였다). MoreTopics 도 같다.
	MoreProjects int
	Topics       []Count
	NoTopic      int
	MoreTopics   int
	DocTypes     []Count
	First        string  // 가장 이른 문서의 날 (YYYY-MM-DD)
	Last         string  // 가장 늦은 문서의 날
	Days         []Count // 문서의 날(event_day)별 문서 수, 최신 먼저 (최대 maxDays)
	// NoIndex 는 팀(org) alias 가 없다는 뜻이다 (Result.NoIndex 와 같다).
	NoIndex bool
}

const (
	// MaxFacetValues 는 필터 하나에서 보이는 값 수의 상한이다 (문서가 많은 값부터).
	MaxFacetValues = 100
	maxDays        = 31
	// maxDayBuckets 는 날짜를 셀 때 받는 날 수의 상한이다 — 처음·마지막 날도 여기서 고른다 (hot 은 60일 남짓)
	maxDayBuckets = 3660
)

// facetsResponse 는 FetchFacets 가 받는 응답이다.
type facetsResponse struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
	} `json:"hits"`
	Aggregations struct {
		Project   buckets  `json:"project"`
		NoProject docCount `json:"no_project"`
		Topic     buckets  `json:"topic"`
		NoTopic   docCount `json:"no_topic"`
		DocType   buckets  `json:"doc_type"`
		Days      buckets  `json:"days"`
	} `json:"aggregations"`
}

// FetchFacets 는 r 의 범위(team 또는 org, 그리고 필터)에 든 문서들의 필터 값을 센다. 문서 수는 대표 청크(chunk 0)로 센다.
// alias 가 없으면 NoIndex 인 빈 값이다.
func FetchFacets(ctx context.Context, c opensearch.Client, r Request) (Facets, error) {
	alias, err := aliasOf(r)
	if err != nil {
		return Facets{}, err
	}
	if err := Validate(r); err != nil {
		return Facets{}, err
	}
	var out facetsResponse
	err = c.Do(ctx, http.MethodPost, "/"+alias+"/_search", facetsBody(r), nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return Facets{NoIndex: true}, nil
	}
	if err != nil {
		return Facets{}, err
	}
	return out.facets(), nil
}

// facetsBody 는 필터에 맞는 문서들의 값별 수를 세는 집계 질의다 (문서는 돌려받지 않는다).
func facetsBody(r Request) map[string]any {
	q := buildQuery(Request{Workspace: r.Workspace, Date: r.Date, TimeRange: r.TimeRange, TimeZone: r.TimeZone, Project: r.Project, Topic: r.Topic, DocType: r.DocType, IncludeSuperseded: r.IncludeSuperseded})
	terms := func(field string) map[string]any {
		return map[string]any{"terms": map[string]any{"field": field, "size": MaxFacetValues}}
	}
	return map[string]any{
		"size":             0,
		"track_total_hits": true,
		"query":            q["query"],
		"aggs": map[string]any{
			"project":    terms("repo"),
			"no_project": map[string]any{"missing": map[string]any{"field": "repo"}},
			"topic":      terms("topic"),
			"no_topic":   map[string]any{"missing": map[string]any{"field": "topic"}},
			"doc_type":   terms("doc_type"),
			"days":       map[string]any{"terms": map[string]any{"field": "event_day", "size": maxDayBuckets, "order": map[string]any{"_key": "desc"}}},
		},
	}
}

// facets 는 응답을 Facets 로 옮긴다. 날짜별 수는 최신 maxDays 일만 남기되, 처음·마지막 날은 전체에서 고른다.
func (out facetsResponse) facets() Facets {
	a := out.Aggregations
	days := a.Days.counts()
	sort.Slice(days, func(i, j int) bool { return days[i].Value > days[j].Value })
	f := Facets{
		Docs: out.Hits.Total.Value, Projects: a.Project.counts(), NoProject: a.NoProject.DocCount, MoreProjects: a.Project.SumOther,
		Topics: a.Topic.counts(), NoTopic: a.NoTopic.DocCount, MoreTopics: a.Topic.SumOther, DocTypes: a.DocType.counts(),
	}
	if len(days) > 0 {
		f.First, f.Last = days[len(days)-1].Value, days[0].Value
	}
	if len(days) > maxDays {
		days = days[:maxDays]
	}
	f.Days = days
	return f
}

type buckets struct {
	// SumOther 는 상한에 잘려 돌려받지 못한 값들의 문서 수다
	SumOther int `json:"sum_other_doc_count"`
	Buckets  []struct {
		Key         any    `json:"key"`
		KeyAsString string `json:"key_as_string"`
		DocCount    int    `json:"doc_count"`
	} `json:"buckets"`
}

func (b buckets) counts() []Count {
	out := make([]Count, 0, len(b.Buckets))
	for _, x := range b.Buckets {
		v := x.KeyAsString
		if v == "" {
			v = fmt.Sprint(x.Key)
		}
		out = append(out, Count{Value: v, Docs: x.DocCount})
	}
	return out
}

type docCount struct {
	DocCount int `json:"doc_count"`
}
