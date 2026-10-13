package ingest

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"testing"

	"github.com/drakejin/workspace/app/internal/opensearch"
)

// scanOS 는 OpenSearch 대신이다 — _search 마다 hits 를 돌려주고, 마지막으로 받은 _source 목록을 기억한다.
type scanOS struct {
	hits   []map[string]any
	source []string
}

func (s *scanOS) Do(_ context.Context, _, _ string, body any, _ url.Values, out any) error {
	if b, ok := body.(map[string]any); ok {
		if src, ok := b["_source"].([]string); ok {
			s.source = src
		}
	}
	if out == nil {
		return nil
	}
	resp, err := json.Marshal(map[string]any{"hits": map[string]any{"hits": s.hits}})
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, out)
}

func (*scanOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// DEAD-6: 데몬은 사이클마다 모든 문서의 대표값(청크 0)을 읽는다 — IndexedDoc 에 담지 않는 필드는 가져오지 않는다.
func TestFetchIndexedDocsFetchesOnlyTheFieldsItKeeps(t *testing.T) {
	os := &scanOS{}
	if _, err := FetchIndexedDocs(context.Background(), os, []string{"mem-t-2026.10"}, "ws"); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), os.source...)
	sort.Strings(got)
	want := []string{"body_sha", "content_sha", "deprecated", "doc_type", "entities", "index_version", "memory_kind", "mtime_ms",
		"pending_cycle", "repo", "size", "source_path", "summary", "superseded_by", "supersedes", "team", "title"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("_source = %v\nwant      %v", got, want)
	}
}

// RUN-3: 대표값은 넘김 표시(pending_cycle)를 읽는다 — 데몬 밖에서 쓴 문서다. 표시가 없으면 false (데몬이 쓴 문서).
func TestFetchIndexedDocsReadsTheHandoffMark(t *testing.T) {
	os := &scanOS{hits: []map[string]any{
		{"_index": "mem-t-2026.10", "_source": map[string]any{"source_path": "docs/a.md", "pending_cycle": true}},
		{"_index": "mem-t-2026.10", "_source": map[string]any{"source_path": "docs/b.md"}},
	}}
	docs, err := FetchIndexedDocs(context.Background(), os, []string{"mem-t-2026.10"}, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if !docs["docs/a.md"].PendingCycle || docs["docs/b.md"].PendingCycle {
		t.Errorf("pending_cycle = %+v", docs)
	}
}
