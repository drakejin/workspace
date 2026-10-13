package opensearch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Hit 는 _search 결과 한 건이다.
type Hit[T any] struct {
	ID     string  `json:"_id"`
	Index  string  `json:"_index"`
	Score  float64 `json:"_score"`
	Source T       `json:"_source"`
	Sort   []any   `json:"sort,omitempty"`
}

// SearchResponse 는 _search 응답 중 hits 부분이다.
type SearchResponse[T any] struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []Hit[T] `json:"hits"`
	} `json:"hits"`
}

// scanPageSize 는 Scan 이 한 번에 읽는 건수다.
const scanPageSize = 1000

// ChunkSort 는 청크를 유일하게 정렬하는 키다 (workspace·team·경로·청크 번호가 청크 id 의 재료).
// 인덱스를 옮기다 끊기면 같은 청크가 두 인덱스에 있을 수 있어 _index 로 마저 가른다 — search_after 가 페이지 경계에서
// 같은 정렬 값을 가진 두 번째 사본을 건너뛰지 않게.
var ChunkSort = []any{
	map[string]any{"workspace": "asc"},
	map[string]any{"team": "asc"},
	map[string]any{"source_path": "asc"},
	map[string]any{"chunk_index": "asc"},
	map[string]any{"_index": "asc"},
}

// Scan 은 search_after 로 결과 전체를 페이지 단위로 읽어 fn 에 넘긴다 (10,000 건 상한이 없다).
// sort 는 결과를 유일하게 정렬해야 한다. 인덱스가 없으면 아무것도 하지 않는다.
func Scan[T any](ctx context.Context, c Client, indices string, query map[string]any, source []string, sort []any, fn func(Hit[T]) error) error {
	var after []any
	for {
		body := map[string]any{"size": scanPageSize, "query": query, "sort": sort}
		if source != nil {
			body["_source"] = source
		}
		if after != nil {
			body["search_after"] = after
		}
		var out SearchResponse[T]
		err := c.Do(ctx, http.MethodPost, "/"+indices+"/_search", body, Lenient(), &out)
		if IsStatus(err, http.StatusNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan %s: %w", indices, err)
		}
		for _, h := range out.Hits.Hits {
			if err := fn(h); err != nil {
				return err
			}
		}
		if len(out.Hits.Hits) < scanPageSize {
			return nil
		}
		after = out.Hits.Hits[len(out.Hits.Hits)-1].Sort
	}
}

// Count 는 질의에 맞는 문서 수다.
func Count(ctx context.Context, c Client, indices string, query map[string]any) (int, error) {
	var out struct {
		Count int `json:"count"`
	}
	err := c.Do(ctx, http.MethodPost, "/"+indices+"/_count", map[string]any{"query": query}, Lenient(), &out)
	return out.Count, err
}

// Settings 는 인덱스별 flat 설정이다.
func Settings(ctx context.Context, c Client, indices string) (map[string]map[string]any, error) {
	var out map[string]struct {
		Settings map[string]any `json:"settings"`
	}
	err := c.Do(ctx, http.MethodGet, "/"+indices+"/_settings", nil,
		url.Values{"flat_settings": {"true"}, "allow_no_indices": {"true"}, "expand_wildcards": {"open"}}, &out)
	if IsStatus(err, http.StatusNotFound) {
		return map[string]map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	settings := make(map[string]map[string]any, len(out))
	for index, v := range out {
		settings[index] = v.Settings
	}
	return settings, nil
}

// IsUnmanaged 는 ISM 자동 관리에서 빠진 인덱스(restore_cold 로 복원한 인덱스)인가.
// 설정은 bool 로 쓰지만 flat_settings 로 읽으면 문자열 "false" 로 돌아온다.
func IsUnmanaged(settings map[string]any) bool {
	return fmt.Sprint(settings[AutoManageSetting]) == "false"
}
