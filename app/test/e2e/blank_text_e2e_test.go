//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
)

// listingPaths 는 그 날(event_day)의 문서를 필터로만 가져온 목록이다 (질의어 없이 — 세션 훅·search_filters 다음의 목록).
func listingPaths(t *testing.T, m *mcpHarness, day string) []string {
	t.Helper()
	res, err := search.Search(context.Background(), m.os, search.Request{Workspace: m.cfg.Workspace, Team: m.cfg.DefaultTeam, Date: day})
	if err != nil {
		t.Fatal(err)
	}
	return paths(res.Hits)
}

// R4-daemon-a-2: 예전 판은 본문이 없는 문서의 청크 0 에 "text": "" 를 썼다 — exists 는 빈 문자열도 있다고 보아 그 문서가 목록에
// 빈 결과로 나온다. 그런 문서가 든 달이 cold 로 갔다가 restore_cold 로 돌아오면, 복원은 검색 alias 를 붙이기 전에 빈 text 를
// 지운다 — 복원한 달의 목록에 빈 문서가 나오지 않는다.
func TestRestoreClearsTheEmptyTextOfOldBlankDocs(t *testing.T) {
	ctx := context.Background()
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	old := daysAgo(hotDays + 60)
	ingestOne(t, m, "docs/knowledge/keep.md", daysAgo(0), "KeepToken") // 팀이 이 workspace 의 것으로 남게
	index := ingestOne(t, m, "docs/specs/full.md", old, "FullToken")
	res, err := ingestFile(ctx, m.indexer, write(t, cfg.MetaRepoRoot, "docs/specs/blank.md", "---\ndate: "+old+"\n---\n"))
	if err != nil || res.Index != index {
		t.Fatalf("빈 문서 적재 = %+v %v (같은 달 %s 에 들어가야 한다)", res, err, index)
	}
	// 예전 판이 쓴 모양 — 빈 청크 0 의 text 가 빈 문자열이다
	if err := opensearch.UpdateDocWhere(ctx, m.os, []string{index}, cfg.Workspace, "docs/specs/blank.md", nil, map[string]any{"text": ""}); err != nil {
		t.Fatal(err)
	}
	if got := listingPaths(t, m, old); !contains(got, "docs/specs/blank.md") {
		t.Fatalf("픽스처: 빈 text 문서가 목록에 나와야 재현이다: %v", got)
	}

	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier = %+v", d)
	}
	if _, err := cold.Restore(ctx, m.os, cfg, index); err != nil {
		t.Fatal(err)
	}

	got := listingPaths(t, m, old)
	if contains(got, "docs/specs/blank.md") || !contains(got, "docs/specs/full.md") {
		t.Errorf("복원한 달의 목록 = %v (빈 문서는 빠지고 본문이 있는 문서는 나온다)", got)
	}
	blankWithText := map[string]any{"bool": map[string]any{"filter": []any{
		map[string]any{"term": map[string]any{"source_path": "docs/specs/blank.md"}},
		map[string]any{"exists": map[string]any{"field": "text"}},
	}}}
	if n, err := opensearch.Count(ctx, m.os, index, blankWithText); err != nil || n != 0 {
		t.Errorf("복원본에 빈 text 가 남았다: %d %v", n, err)
	}
}
