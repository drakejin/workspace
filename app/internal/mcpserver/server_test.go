package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/search"
)

func TestInputValidationBeforeIO(t *testing.T) {
	h := NewHandlers(Deps{Config: config.Config{DefaultTeam: "ads"}})
	ctx := context.Background()
	// confirmed 없는 restore_cold 는 cold 목록을 읽어 존재를 확인한다 (RUN-14) — TestRestoreWithoutConfirmationChecksTheIndexIsInCold
	for _, bad := range []string{"mem-ads-hot", "../x", "mem-ads-knowledge"} {
		if _, err := h.Restore(ctx, RestoreInput{Index: bad, Confirmed: true}); err == nil {
			t.Errorf("%s 는 거부", bad)
		}
	}
	if _, err := h.Search(ctx, SearchInput{Team: "Bad Team", Query: "x"}); err == nil {
		t.Error("잘못된 team")
	}
	if _, err := h.Search(ctx, SearchInput{Query: "x", DocType: "weird"}); err == nil {
		t.Error("잘못된 doc_type")
	}
	if _, err := h.Ingest(ctx, IngestInput{Path: " "}); err == nil {
		t.Error("빈 path")
	}
	if _, err := h.Ingest(ctx, IngestInput{Path: "docs", Team: "Bad"}); err == nil {
		t.Error("잘못된 team")
	}
	if _, err := h.ListCold(ctx, ListColdInput{Team: "Bad"}); err == nil {
		t.Error("잘못된 team")
	}
	// RUN-8: 정규화하면 비는 topic 은 조용히 빠지지 않고 OpenSearch 에 묻기 전에 거부된다 (search_filters 도)
	for _, topic := range []string{"!!!", " / "} {
		if _, err := h.Search(ctx, SearchInput{Topic: topic}); err == nil || !strings.Contains(err.Error(), "topic") {
			t.Errorf("search_memory topic=%q: %v", topic, err)
		}
		if _, err := h.Filters(ctx, FiltersInput{Topic: topic}); err == nil || !strings.Contains(err.Error(), "topic") {
			t.Errorf("search_filters topic=%q: %v", topic, err)
		}
	}
}

func TestFormatIngest(t *testing.T) {
	out := FormatIngest([]ingest.Result{{SourcePath: "docs/a.md", Index: "mem-a-2026.10", Chunks: 3}})
	if out != "1개 문서 적재\n- docs/a.md → mem-a-2026.10 (3 chunks)" {
		t.Errorf("= %q", out)
	}
	// RUN-7: 게이트에 걸린 파일은 적재한 수에 넣지 않고 건너뛴 사유와 함께 따로 적는다
	out = FormatIngest([]ingest.Result{
		{SourcePath: "docs/a.md", Index: "mem-a-2026.10", Chunks: 3},
		{SourcePath: "docs/bin.md", Skipped: "binary content"},
		{SourcePath: "docs/big.md", Skipped: "size 2300271 > 2097152"},
	})
	want := "1개 문서 적재\n- docs/a.md → mem-a-2026.10 (3 chunks)\n" +
		"2개 건너뜀 — 데몬과 같은 게이트: 바이너리·MEMORY_MAX_FILE_BYTES 초과는 색인하지 않는다\n" +
		"- docs/bin.md (binary content)\n- docs/big.md (size 2300271 > 2097152)"
	if out != want {
		t.Errorf("건너뜀\n got  %q\n want %q", out, want)
	}
}

// DOC-16 / API-7: 질의어 검색(BM25)은 청크 단위라 머리에 청크 수와 그 청크들의 문서 수를 함께 밝힌다
// ("청크 N건 · 문서 M건" — 한 문서의 여러 절이 size 를 나눠 쓴 것이 보인다). 필터 가져오기는 문서 하나에 한 건이라 "문서 N건" 이다.
func TestSearchHeaderNamesItsUnit(t *testing.T) {
	threeChunksTwoDocs := []search.Hit{{SourcePath: "docs/a.md"}, {SourcePath: "docs/b.md"}, {SourcePath: "docs/a.md"}}
	twoDocs := []search.Hit{{SourcePath: "docs/a.md"}, {SourcePath: "docs/b.md"}}
	cases := []struct {
		name    string
		listing bool
		total   int
		hits    []search.Hit
		want    string
	}{
		{"질의어 검색 — 청크와 문서", false, 3, threeChunksTwoDocs, "team=a · 청크 3건 · 문서 2건 (BM25)"},
		{"필터 가져오기 — 문서 단위", true, 2, twoDocs, "team=a · 문서 2건 (필터, 최신순)"},
		{"잘린 가져오기 — 전체 문서 수", true, 5, twoDocs, "team=a · 문서 5건 중 최신 2건 (필터, 최신순 — 더 보려면 size 를 늘리거나 필터를 좁히세요)"},
	}
	for _, c := range cases {
		if got := searchHeader("team=a", c.listing, c.total, c.hits); got != c.want {
			t.Errorf("%s\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
