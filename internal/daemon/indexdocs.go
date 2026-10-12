package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// IndexedDoc 은 색인에 있는 문서 하나의 대표값(chunk_index 0)이다.
type IndexedDoc struct {
	SourcePath   string
	Team         string
	DocType      model.DocType
	Kind         model.Kind
	ContentSHA   string
	BodySHA      string
	MtimeMs      int64
	Size         int64
	EventTime    string
	Supersedes   string
	SupersededBy string
	Summary      string
}

const maxIndexedDocs = 10_000

// ManagedIndices 는 데몬이 맞추는 hot 인덱스다. restore_cold 로 복원한 인덱스(auto_manage=false)는 뺀다 —
// 데몬은 cold 를 건드리지 않는다. (설정 이름으로 거르면 그 설정이 없는 인덱스가 응답에서 빠져서 전체를 받는다)
func ManagedIndices(ctx context.Context, c opensearch.Client) ([]string, error) {
	var out map[string]struct {
		Settings map[string]any `json:"settings"`
	}
	err := c.Do(ctx, http.MethodGet, "/"+naming.AllPattern+"/_settings", nil,
		url.Values{"flat_settings": {"true"}, "allow_no_indices": {"true"}, "expand_wildcards": {"open"}}, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	indices := []string{}
	for name, v := range out {
		if _, ok := naming.ParseIndex(name); !ok {
			continue
		}
		if fmt.Sprint(v.Settings[opensearch.AutoManageSetting]) == "false" {
			continue
		}
		indices = append(indices, name)
	}
	sort.Strings(indices)
	return indices, nil
}

// FetchIndexedDocs 는 indices 에서 이 workspace 의 문서별 대표값이다.
func FetchIndexedDocs(ctx context.Context, c opensearch.Client, indices []string, workspace string) (map[string]IndexedDoc, error) {
	docs := map[string]IndexedDoc{}
	if len(indices) == 0 {
		return docs, nil
	}
	var out struct {
		Hits struct {
			Hits []struct {
				Source model.Chunk `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	body := map[string]any{
		"size": maxIndexedDocs,
		"query": map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"chunk_index": 0}},
			map[string]any{"term": map[string]any{"workspace": workspace}},
		}}},
		"_source": []string{"source_path", "team", "doc_type", "memory_kind", "content_sha", "body_sha", "mtime_ms", "size",
			"event_time", "supersedes", "superseded_by", "summary"},
	}
	if err := c.Do(ctx, http.MethodPost, "/"+strings.Join(indices, ",")+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return nil, err
	}
	for _, h := range out.Hits.Hits {
		s := h.Source
		docs[s.SourcePath] = IndexedDoc{
			SourcePath: s.SourcePath, Team: s.Team, DocType: s.DocType, Kind: s.Kind, ContentSHA: s.ContentSHA, BodySHA: s.BodySHA,
			MtimeMs: s.MtimeMs, Size: s.Size, EventTime: s.EventTime, Supersedes: s.Supersedes, SupersededBy: s.SupersededBy, Summary: s.Summary,
		}
	}
	return docs, nil
}

// UpdateBySource 는 문서의 모든 청크에 필드를 덮어쓴다 (stat 갱신, superseded_by 수렴, 증류 반영).
// 필드 이름은 이 패키지의 상수뿐이라 스크립트에 그대로 넣는다. 값은 params 로만 간다.
func UpdateBySource(ctx context.Context, c opensearch.Client, indices []string, workspace, sourcePath string, fields map[string]any) error {
	if len(indices) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var script strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&script, "ctx._source['%s'] = params['%s']; ", k, k)
	}
	body := map[string]any{
		"query":  map[string]any{"bool": map[string]any{"filter": ingest.DocFilter(workspace, sourcePath)}},
		"script": map[string]any{"lang": "painless", "source": script.String(), "params": fields},
	}
	return c.Do(ctx, http.MethodPost, "/"+strings.Join(indices, ",")+"/_update_by_query", body,
		opensearch.With(opensearch.Lenient(), "refresh", "true", "conflicts", "proceed"), nil)
}
