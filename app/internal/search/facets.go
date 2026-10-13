package search

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/drakejin/workspace/app/internal/naming"
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
	Topics    []Count
	NoTopic   int
	DocTypes  []Count
	First     string  // 가장 이른 문서의 날 (YYYY-MM-DD)
	Last      string  // 가장 늦은 문서의 날
	Days      []Count // 문서의 날(event_day)별 문서 수, 최신 먼저 (최대 maxDays)
}

const (
	maxFacetValues = 100
	maxDays        = 31
	// maxDayBuckets 는 날짜를 셀 때 받는 날 수의 상한이다 — 처음·마지막 날도 여기서 고른다 (hot 은 60일 남짓)
	maxDayBuckets = 3660
)

// FetchFacets 는 r 의 범위(team 또는 org, 그리고 필터)에 든 문서들의 필터 값을 센다. 문서 수는 대표 청크(chunk 0)로 센다.
func FetchFacets(ctx context.Context, c opensearch.Client, r Request) (Facets, error) {
	alias := naming.HotAlias(r.Team)
	if r.Org != "" {
		if !naming.ValidName(r.Org) {
			return Facets{}, fmt.Errorf("org 이름이 잘못됐습니다: %q", r.Org)
		}
		alias = naming.OrgAlias(r.Org)
	} else if err := naming.ValidateTeam(r.Team); err != nil {
		return Facets{}, err
	}
	if err := Validate(r); err != nil {
		return Facets{}, err
	}
	q := BuildQuery(Request{Workspace: r.Workspace, Date: r.Date, TimeRange: r.TimeRange, TimeZone: r.TimeZone, Project: r.Project, Topic: r.Topic, DocType: r.DocType, IncludeSuperseded: r.IncludeSuperseded})
	terms := func(field string) map[string]any {
		return map[string]any{"terms": map[string]any{"field": field, "size": maxFacetValues}}
	}
	body := map[string]any{
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
	var out struct {
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
	err := c.Do(ctx, http.MethodPost, "/"+alias+"/_search", body, nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return Facets{}, nil
	}
	if err != nil {
		return Facets{}, err
	}
	a := out.Aggregations
	days := a.Days.counts()
	sort.Slice(days, func(i, j int) bool { return days[i].Value > days[j].Value })
	f := Facets{
		Docs: out.Hits.Total.Value, Projects: a.Project.counts(), NoProject: a.NoProject.DocCount,
		Topics: a.Topic.counts(), NoTopic: a.NoTopic.DocCount, DocTypes: a.DocType.counts(),
	}
	if len(days) > 0 {
		f.First, f.Last = days[len(days)-1].Value, days[0].Value
	}
	if len(days) > maxDays {
		days = days[:maxDays]
	}
	f.Days = days
	return f, nil
}

type buckets struct {
	Buckets []struct {
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
