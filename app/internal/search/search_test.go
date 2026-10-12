package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"testing"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

func TestBuildQuery(t *testing.T) {
	q := BuildQuery(Request{Workspace: "ws", Team: "ads", Query: "dedup", TimeRange: model.TimeRange{From: "now-30d"}, Repo: "attribution", DocType: model.DocADR, Size: 500})
	if q["size"] != maxSize {
		t.Errorf("size = %v", q["size"])
	}
	b := q["query"].(map[string]any)["bool"].(map[string]any)
	if len(b["filter"].([]any)) != 4 || b["must_not"] == nil {
		t.Errorf("bool = %v", b)
	}
	plain := BuildQuery(Request{Team: "a", Query: "x", IncludeSuperseded: true})["query"].(map[string]any)["bool"].(map[string]any)
	if len(plain["filter"].([]any)) != 0 || plain["must_not"] != nil {
		t.Errorf("plain = %v", plain)
	}
	if BuildQuery(Request{Query: "x"})["size"] != defaultSize {
		t.Error("default size")
	}
}

type stubOS struct {
	path string
	resp any
	err  error
}

func (s *stubOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	s.path = path
	if s.err != nil {
		return s.err
	}
	b, _ := json.Marshal(s.resp)
	return json.Unmarshal(b, out)
}

func (s *stubOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

func TestSearch(t *testing.T) {
	os := &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{map[string]any{
		"_index": "mem-ads-2026.10", "_score": 3.2,
		"_source": map[string]any{"source_path": "docs/a.md", "header_path": "A", "doc_type": "note", "summary": "s", "superseded_by": "docs/b.md"},
	}}}}}
	hits, err := Search(context.Background(), os, Request{Team: "ads", Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{{Score: 3.2, Index: "mem-ads-2026.10", SourcePath: "docs/a.md", HeaderPath: "A", DocType: model.DocNote, Summary: "s", SupersededBy: "docs/b.md"}}
	if !reflect.DeepEqual(hits, want) || os.path != "/mem-ads-hot/_search" {
		t.Errorf("hits = %+v path=%s", hits, os.path)
	}
}

func TestSearchErrors(t *testing.T) {
	ctx := context.Background()
	if hits, err := Search(ctx, &stubOS{err: &opensearch.Error{Status: 404}}, Request{Team: "a", Query: "q"}); err != nil || hits != nil {
		t.Errorf("alias 가 없으면 빈 결과: %v %v", hits, err)
	}
	if _, err := Search(ctx, &stubOS{err: &opensearch.Error{Status: 500, Reason: "boom"}}, Request{Team: "a", Query: "q"}); err == nil {
		t.Error("다른 에러는 그대로")
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "a", Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Error("빈 질의")
	}
	if _, err := Search(ctx, &stubOS{}, Request{Team: "Bad", Query: "q"}); err == nil {
		t.Error("잘못된 team")
	}
}

func TestSearchOrgAlias(t *testing.T) {
	os := &stubOS{resp: map[string]any{"hits": map[string]any{"hits": []any{}}}}
	if _, err := Search(context.Background(), os, Request{Org: "ads", Query: "x"}); err != nil || os.path != "/mem-org-ads-hot/_search" {
		t.Errorf("org alias = %s %v", os.path, err)
	}
	if _, err := Search(context.Background(), os, Request{Org: "Bad Org", Query: "x"}); err == nil {
		t.Error("잘못된 org")
	}
	q := BuildQuery(Request{Query: "x"})["query"].(map[string]any)["bool"].(map[string]any)
	if len(q["must_not"].([]any)) != 2 {
		t.Error("밀린 문서와 deprecated 를 숨긴다")
	}
}
