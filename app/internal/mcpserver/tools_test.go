package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/ingest"
)

// RUN-14: confirmed 없는 restore_cold 는 그 인덱스가 정말 cold 에 있는지 먼저 본다 — 없는 인덱스를 두고 사용자에게
// "cold 인덱스 X가 있는데 복원할까요" 를 묻게 시키지 않는다. 있으면 예전처럼 먼저 물으라고 돌려준다.
func TestRestoreWithoutConfirmationChecksTheIndexIsInCold(t *testing.T) {
	h := hintHandlers(`{"key":"ads"}`, nil) // cold 에는 mem-ads-2026.06 하나
	ctx := context.Background()

	for _, missing := range []string{"mem-ads-2026.07", "mem-zz-2026.06"} {
		out, err := h.Restore(ctx, RestoreInput{Index: missing})
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "list_cold") {
			t.Errorf("cold 에 없는 %s = %q %v — 없다고 알리고 list_cold 를 가리켜야 한다", missing, out, err)
		}
	}

	out, err := h.Restore(ctx, RestoreInput{Index: "mem-ads-2026.06"})
	if err != nil || !strings.Contains(out, "복원할까요") || !strings.Contains(out, "confirmed=true") {
		t.Errorf("cold 에 있는 인덱스는 먼저 물으라고 돌려준다: %q %v", out, err)
	}
}

// RUN-12: ingest_path 의 team 은 이번 적재에만 쓰인다 — 데몬은 파일이 바뀌면 frontmatter team(없으면 기본 팀)으로 다시 쓴다.
// 입력 설명이 "frontmatter 대신 쓸 팀" 이라 오래가는 설정처럼 읽혔다.
func TestIngestTeamIsDescribedAsAOneOffOverride(t *testing.T) {
	field, ok := reflect.TypeOf(IngestInput{}).FieldByName("Team")
	if !ok {
		t.Fatal("IngestInput.Team 이 없다")
	}

	desc := field.Tag.Get("jsonschema")

	for _, want := range []string{"이번 적재에만", "frontmatter team"} {
		if !strings.Contains(desc, want) {
			t.Errorf("ingest_path team 설명에 %q 가 없다: %q", want, desc)
		}
	}
}

// stubIndexer 는 IngestPath 만 답하는 Indexer 다.
type stubIndexer struct {
	ingest.Indexer
	results []ingest.Result
}

func (s stubIndexer) IngestPath(context.Context, string, string) ([]ingest.Result, error) {
	return s.results, nil
}

// RUN-12: team 을 주고 적재하면 결과에도 일회성이라고 적는다 — CLI(ingest -team)로 친 사람도 본다.
func TestIngestWithTeamSaysItIsOneOff(t *testing.T) {
	ix := stubIndexer{results: []ingest.Result{{SourcePath: "docs/a.md", Index: "mem-t3-2026.10", Chunks: 1}}}
	h := &Handlers{Deps: Deps{Indexer: ix}, ready: true}
	ctx := context.Background()

	withTeam, err := h.Ingest(ctx, IngestInput{Path: "docs/a.md", Team: "t3"})
	if err != nil || !strings.Contains(withTeam, "이번 적재에만") || !strings.Contains(withTeam, "frontmatter") {
		t.Errorf("team 을 준 적재 = %q %v", withTeam, err)
	}
	plain, err := h.Ingest(ctx, IngestInput{Path: "docs/a.md"})
	if err != nil || strings.Contains(plain, "이번 적재에만") {
		t.Errorf("team 없는 적재에는 덧붙이지 않는다: %q %v", plain, err)
	}
}
