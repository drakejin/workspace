package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
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
}

func TestFormatIngest(t *testing.T) {
	out := FormatIngest([]ingest.Result{{SourcePath: "docs/a.md", Index: "mem-a-2026.10", Chunks: 3}})
	if out != "1개 문서 적재\n- docs/a.md → mem-a-2026.10 (3 chunks)" {
		t.Errorf("= %q", out)
	}
}
