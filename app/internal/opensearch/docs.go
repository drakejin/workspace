package opensearch

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/drakejin/workspace/app/internal/naming"
)

// DocFilter 는 workspace 안의 문서 하나(모든 청크)를 가리키는 필터다.
func DocFilter(workspace, sourcePath string) []any {
	return []any{
		map[string]any{"term": map[string]any{"workspace": workspace}},
		map[string]any{"term": map[string]any{"source_path": sourcePath}},
	}
}

// FirstChunks 는 workspace 의 문서별 대표 청크(chunk_index 0)를 고르는 질의다.
func FirstChunks(workspace string, extra ...any) map[string]any {
	filter := append([]any{
		map[string]any{"term": map[string]any{"chunk_index": 0}},
		map[string]any{"term": map[string]any{"workspace": workspace}},
	}, extra...)
	return map[string]any{"bool": map[string]any{"filter": filter}}
}

// UpdateDoc 는 문서의 모든 청크에 필드를 덮어쓴다 (stat 갱신, superseded_by 수렴, deprecated).
// 필드 이름은 painless 스크립트에 들어가므로 매핑에 있는 이름만 받는다. 값은 params 로만 간다.
func UpdateDoc(ctx context.Context, c Client, indices []string, workspace, sourcePath string, fields map[string]any) error {
	return UpdateDocWhere(ctx, c, indices, workspace, sourcePath, nil, fields)
}

// UpdateDocWhere 는 UpdateDoc 에 조건(filter 절)을 더한 것이다. 예: 증류 결과는 그 결과를 만든 본문(body_sha)의 청크에만.
func UpdateDocWhere(ctx context.Context, c Client, indices []string, workspace, sourcePath string, where []any, fields map[string]any) error {
	if len(indices) == 0 {
		return nil
	}
	props := Properties()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if _, ok := props[k]; !ok {
			return fmt.Errorf("update %s: 매핑에 없는 필드 %q", sourcePath, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var script strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&script, "ctx._source['%s'] = params['%s']; ", k, k)
	}
	filter := append(DocFilter(workspace, sourcePath), where...)
	body := map[string]any{
		"query":  map[string]any{"bool": map[string]any{"filter": filter}},
		"script": map[string]any{"lang": "painless", "source": script.String(), "params": fields},
	}
	if err := c.Do(ctx, http.MethodPost, "/"+strings.Join(indices, ",")+"/_update_by_query", body,
		With(Lenient(), "refresh", "true", "conflicts", "proceed"), nil); err != nil {
		return fmt.Errorf("update %s: %w", sourcePath, err)
	}
	return nil
}

// DeleteDoc 는 indices 에서 문서의 청크를 모두 지운다.
func DeleteDoc(ctx context.Context, c Client, indices []string, workspace, sourcePath string) (int, error) {
	return deleteDoc(ctx, c, indices, sourcePath, map[string]any{"filter": DocFilter(workspace, sourcePath)})
}

// DeleteDocExcept 는 indices 에서 문서의 청크를 지우되, keepIndex 안의 keepIDs 청크만 남긴다.
// 청크 id 에는 인덱스가 들어가지 않으므로(날짜·성질이 바뀌면 같은 id 로 다른 인덱스에 간다) 인덱스까지 같아야 남긴다.
func DeleteDocExcept(ctx context.Context, c Client, indices []string, workspace, sourcePath, keepIndex string, keepIDs []string) (int, error) {
	query := map[string]any{"filter": DocFilter(workspace, sourcePath)}
	if len(keepIDs) > 0 {
		query["must_not"] = []any{map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"_index": keepIndex}},
			map[string]any{"ids": map[string]any{"values": keepIDs}},
		}}}}
	}
	return deleteDoc(ctx, c, indices, sourcePath, query)
}

// deleteDoc 은 query 에 맞는 청크를 지운다. workspace 격리는 호출하는 쪽이 query 에 넣는다(DocFilter).
func deleteDoc(ctx context.Context, c Client, indices []string, sourcePath string, query map[string]any) (int, error) {
	if len(indices) == 0 {
		return 0, nil
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	err := c.Do(ctx, http.MethodPost, "/"+strings.Join(indices, ",")+"/_delete_by_query", map[string]any{"query": map[string]any{"bool": query}},
		With(Lenient(), "refresh", "true", "conflicts", "proceed"), &out)
	if err != nil {
		return 0, fmt.Errorf("delete %s: %w", sourcePath, err)
	}
	return out.Deleted, nil
}

// WorkspaceTeams 는 workspace 가 문서를 둔 팀이다 (팀 하나는 workspace 하나에만 속한다).
// 인덱스 단위로 움직이는 일(tier, org alias 정리)은 이 팀들의 인덱스만 건드린다 — 같은 클러스터의 다른 meta-repo 를 지키려고.
func WorkspaceTeams(ctx context.Context, c Client, workspace string) (map[string]bool, error) {
	var out struct {
		Aggregations struct {
			Teams struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"teams"`
		} `json:"aggregations"`
	}
	body := map[string]any{
		"size":  0,
		"query": map[string]any{"term": map[string]any{"workspace": workspace}},
		"aggs":  map[string]any{"teams": map[string]any{"terms": map[string]any{"field": "team", "size": 10000}}},
	}
	if err := c.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search", body, Lenient(), &out); err != nil {
		return nil, fmt.Errorf("teams of %s: %w", workspace, err)
	}
	teams := map[string]bool{}
	for _, b := range out.Aggregations.Teams.Buckets {
		teams[b.Key] = true
	}
	return teams, nil
}
