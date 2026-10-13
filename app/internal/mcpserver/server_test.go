package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
)

func TestInputValidationBeforeIO(t *testing.T) {
	h := NewHandlers(Deps{Config: config.Config{DefaultTeam: "ads"}})
	ctx := context.Background()
	if out, err := h.Restore(ctx, RestoreInput{Index: "mem-ads-2026.06"}); err != nil || !strings.Contains(out, "복원할까요") {
		t.Errorf("confirmed 없이는 복원하지 않는다: %q %v", out, err)
	}
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
