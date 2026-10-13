package mcpserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/opensearch"
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

// coldStateHandlers 는 hintHandlers 의 cold 인덱스(mem-ads-2026.06)가 지금 settings·aliases 처럼 열려 있는 Handlers 다.
func coldStateHandlers(settings, aliases string) *Handlers {
	os := routeOS{
		"/_snapshot/mem-cold/_all": `{"snapshots":[{"snapshot":"cold-mem-ads-2026.06","indices":["mem-ads-2026.06"],"state":"SUCCESS","start_time":"2026-10-01T00:00:00Z"}]}`,
		"/mem-*/_search":           `{"aggregations":{"teams":{"buckets":[{"key":"ads"}]}}}`,
		"/mem-ads-*/_settings":     settings,
		"/mem-ads-*/_alias":        aliases,
	}
	cfg := config.Config{Workspace: "ws", DefaultTeam: "ads", HotDays: 60, Snapshot: config.Snapshot{Repository: "mem-cold"}}
	return &Handlers{Deps: Deps{Config: cfg, OS: os}, ready: true}
}

// R4-query-b-5: list_cold 가 [현재 복원됨] 으로 보이는 인덱스를 두고 confirmed 없는 restore_cold 가 사용자에게 "복원할까요" 를
// 묻게 시키지 않는다 — 이미 검색에 나온다고 알린다. hot 에 다시 열린 인덱스([hot 에 다시 열림])는 지금 복원할 수 없다고 바로
// 알리고, 합치다 멈춘 복원본([복원 중단])은 예전처럼 먼저 물으라고 돌려준다.
func TestRestoreWithoutConfirmationFollowsTheColdMark(t *testing.T) {
	unmanaged := `{"mem-ads-2026.06":{"settings":{"` + opensearch.AutoManageSetting + `":"false"}}}`
	searchable := `{"mem-ads-2026.06":{"aliases":{"mem-ads-hot":{}}}}`
	ctx := context.Background()
	in := RestoreInput{Index: "mem-ads-2026.06"}

	restored, err := coldStateHandlers(unmanaged, searchable).Restore(ctx, in)
	if err != nil || strings.Contains(restored, "복원할까요") || !strings.Contains(restored, "이미 복원") || !strings.Contains(restored, searchHint) {
		t.Errorf("이미 복원된 인덱스 = %q %v", restored, err)
	}
	partial, err := coldStateHandlers(unmanaged, `{"mem-ads-2026.06":{"aliases":{}}}`).Restore(ctx, in)
	if err != nil || !strings.Contains(partial, "복원할까요") {
		t.Errorf("합치다 멈춘 복원본은 먼저 묻는다 = %q %v", partial, err)
	}
	reopened, err := coldStateHandlers(`{"mem-ads-2026.06":{"settings":{}}}`, searchable).Restore(ctx, in)
	if !errors.Is(err, cold.ErrReopened) || strings.Contains(reopened, "복원할까요") {
		t.Errorf("hot 에 다시 열린 인덱스 = %q %v", reopened, err)
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
