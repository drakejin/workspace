//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/archive"
	"github.com/drakejin/workspace/app/internal/cold"
)

// ingestOne 은 date 날짜의 문서 하나를 쓰고 적재해 그 문서가 들어간 인덱스 이름을 돌려준다.
func ingestOne(t *testing.T, m *mcpHarness, rel, date, token string) string {
	t.Helper()
	abs := write(t, m.cfg.MetaRepoRoot, rel, "---\ndate: "+date+"\n---\n# "+token+" 문서\n"+token+" 를 쓴다.\n")
	res, err := m.indexer.IngestFile(context.Background(), abs, "")
	if err != nil || res == nil {
		t.Fatalf("ingest %s = %+v %v", rel, res, err)
	}
	return res.Index
}

// removeDoc 은 hot 파일을 지우고 색인에서도 뺀다 (데몬 tombstone 과 같은 경로 — 월별 인덱스는 남는다).
func removeDoc(t *testing.T, m *mcpHarness, rel string) {
	t.Helper()
	abs := filepath.Join(m.cfg.MetaRepoRoot, filepath.FromSlash(rel))
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if _, err := m.indexer.RemoveFile(context.Background(), abs, nil); err != nil {
		t.Fatal(err)
	}
}

func tierTeam(t *testing.T, m *mcpHarness) []cold.TierDecision {
	t.Helper()
	decisions, err := cold.Tier(context.Background(), m.os, m.store, m.cfg, cold.TierOptions{Team: m.cfg.DefaultTeam})
	if err != nil {
		t.Fatalf("tier = %+v %v", decisions, err)
	}
	return decisions
}

func decisionOf(decisions []cold.TierDecision, index string) cold.TierDecision {
	for _, d := range decisions {
		if d.Index == index && d.Action != cold.TierPruned {
			return d
		}
	}
	return cold.TierDecision{}
}

func snapshotNames(t *testing.T, m *mcpHarness, index string) []string {
	t.Helper()
	snaps, err := cold.SnapshotsOf(context.Background(), m.os, m.cfg, index)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(snaps))
	for _, s := range snaps {
		names = append(names, s.Name)
	}
	return names
}

func deleteIndex(t *testing.T, m *mcpHarness, index string) {
	t.Helper()
	if err := m.os.Do(context.Background(), http.MethodDelete, "/"+index, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func isLive(t *testing.T, m *mcpHarness, index string) bool {
	t.Helper()
	live, err := cold.LiveIndices(context.Background(), m.os, index)
	if err != nil {
		t.Fatal(err)
	}
	return live[index]
}

func fileExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// TestEmptyIndexIsNeverTieredAsAColdMonth 는 OPS-2 의 재현이다. 그 달의 유일한 문서를 지워 월별 인덱스가 비면
// tier 는 그 인덱스를 스냅샷하지 않는다 (빈 스냅샷은 목록이 없어 "그 달 전체가 cold" 로 읽혔다). 그 달의 hot 문서는
// 인덱스를 지우고 다시 실어도(hot 재구축) 다시 색인되고, 스냅샷에 들어간 뒤에만 hot 에서 빠진다.
func TestEmptyIndexIsNeverTieredAsAColdMonth(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	c := newCLI(t, cfg, nil)
	root := cfg.MetaRepoRoot
	today, old := daysAgo(0), daysAgo(hotDays+60)
	// 팀에 다른 문서가 남아 있어야 tier 가 이 workspace 의 팀으로 본다 (knowledge 는 노화하지 않는다)
	ingestOne(t, m, "docs/knowledge/keep.md", today, "KeepToken")

	current := ingestOne(t, m, "docs/specs/gone.md", today, "GoneToken")
	past := ingestOne(t, m, "docs/specs/old-gone.md", old, "OldGoneToken")
	removeDoc(t, m, "docs/specs/gone.md")
	removeDoc(t, m, "docs/specs/old-gone.md")

	decisions := tierTeam(t, m)
	if d := decisionOf(decisions, current); d.Action != cold.TierKept {
		t.Errorf("이번 달의 빈 인덱스는 hot 에 둔다: %+v", d)
	}
	if d := decisionOf(decisions, past); d.Action != cold.TierDropped || d.Snapshot != "" {
		t.Errorf("hot 기간이 지난 빈 인덱스는 스냅샷 없이 지운다: %+v", d)
	}
	for _, index := range []string{current, past} {
		if snaps := snapshotNames(t, m, index); len(snaps) != 0 {
			t.Errorf("%s: 빈 인덱스는 스냅샷하지 않는다 = %v", index, snaps)
		}
	}
	if isLive(t, m, past) {
		t.Errorf("%s 는 지워졌다", past)
	}

	// hot 재구축: 인덱스를 지우고 cold store 에서 다시 싣는다 (docker-compose.yml·GUIDE 가 안전하다고 안내하는 절차)
	ingestOne(t, m, "docs/specs/today.md", today, "TodayToken")
	ingestOne(t, m, "docs/specs/late.md", old, "LateToken")
	deleteIndex(t, m, current)
	deleteIndex(t, m, past)
	if out := c.run(t, "reload"); !strings.Contains(out, "chunks 재적재") {
		t.Errorf("reload = %s", out)
	}
	for _, token := range []string{"TodayToken", "LateToken"} {
		if got := searchPaths(t, cfg, token, false); len(got) != 1 {
			t.Errorf("reload 후 %s = %v — cold 가 그 달을 가졌다고 오인했다", token, got)
		}
	}

	decisions = tierTeam(t, m)
	if d := decisionOf(decisions, current); d.Action != cold.TierKept || !fileExists(root, "docs/specs/today.md") {
		t.Errorf("이번 달 문서는 hot 에 남는다: %+v", decisions)
	}
	d := decisionOf(decisions, past)
	if d.Action != cold.TierMoved || len(d.RemovedFiles) != 1 || d.RemovedFiles[0] != "docs/specs/late.md" {
		t.Fatalf("지난달 문서는 스냅샷에 넣은 뒤 hot 에서 뺀다: %+v", decisions)
	}
	if _, err := cold.Restore(context.Background(), m.os, cfg, past); err != nil {
		t.Fatal(err)
	}
	if got := searchPaths(t, cfg, "LateToken", false); len(got) != 1 {
		t.Errorf("hot 에서 뺀 문서는 스냅샷에 있다: %v", got)
	}
}

// TestColdMonthWithSeveralSnapshotsKeepsEveryDocument 는 OPS-1 의 재현이다. 한 달을 tier 한 뒤 그 달 날짜의 문서가
// 늦게 들어오면 같은 인덱스의 스냅샷이 둘이 된다. list_cold·restore_cold 는 둘을 합치고, archive edit·purge 는
// 모든 스냅샷을 되살려 다시 쓴 뒤에만 옛 스냅샷을 지운다 — 어느 문서도 잃지 않는다.
func TestColdMonthWithSeveralSnapshotsKeepsEveryDocument(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	ctx := context.Background()
	team := cfg.DefaultTeam
	date := daysAgo(hotDays + 60)
	svc := &archive.Service{OS: m.os, Config: cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now}
	who := archive.Audit{By: "jin", Reason: "e2e"}

	index := ingestOne(t, m, "docs/specs/a.md", date, "AlphaToken")
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier#1 = %+v", d)
	}
	ingestOne(t, m, "docs/specs/b.md", date, "BetaToken") // 늦게 들어온 같은 달 문서 — 인덱스가 hot 에 다시 생긴다

	// 다시 생긴 hot 인덱스에는 cold 문서를 섞지 않는다 (데몬이 파일 없는 문서로 보고 cold store 청크까지 지운다)
	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); !isErr || !strings.Contains(text, "다시 열려") {
		t.Errorf("hot 에 다시 열린 달의 restore 는 거부한다: %s", text)
	}
	if _, err := svc.Purge(ctx, team, "docs/specs/a.md", who); !errors.Is(err, cold.ErrReopened) {
		t.Errorf("hot 에 다시 열린 달의 archive 는 거부한다: %v", err)
	}
	if snaps := snapshotNames(t, m, index); len(snaps) != 1 {
		t.Fatalf("거부한 조치는 스냅샷을 건드리지 않는다: %v", snaps)
	}

	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier#2 = %+v", d)
	}
	snaps := snapshotNames(t, m, index)
	if len(snaps) != 2 {
		t.Fatalf("같은 달의 스냅샷 둘 = %v", snaps)
	}
	if text, _ := m.call(t, "list_cold", map[string]any{}); !strings.Contains(text, snaps[0]) || !strings.Contains(text, snaps[1]) {
		t.Errorf("list_cold 는 두 스냅샷을 다 보여 준다: %s", text)
	}

	// restore 는 모든 스냅샷을 합친다
	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); isErr {
		t.Fatalf("restore = %s", text)
	}
	for _, token := range []string{"AlphaToken", "BetaToken"} {
		if got := searchPaths(t, cfg, token, false); len(got) != 1 {
			t.Errorf("복원 후 %s = %v", token, got)
		}
	}
	deleteIndex(t, m, index) // 다시 cold 로

	// edit 은 두 스냅샷을 모두 되살려 다시 쓴다
	edited := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(edited, []byte("---\ndate: "+date+"\n---\n# B\nBetaEditedToken 를 쓴다.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := svc.Edit(ctx, team, "docs/specs/b.md", edited, who); err != nil {
		t.Fatalf("edit = %s %v", out, err)
	}
	after := snapshotNames(t, m, index)
	if len(after) != 1 {
		t.Fatalf("다시 쓴 스냅샷 하나만 남는다: %v", after)
	}
	manifest, _, _ := m.store.Get(ctx, cold.ManifestKey(team, index))
	for _, old := range snaps {
		if strings.Contains(manifest, old) {
			t.Errorf("지운 스냅샷의 목록 줄도 지운다 (%s):\n%s", old, manifest)
		}
	}
	if _, err := cold.Restore(ctx, m.os, cfg, index); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]int{"AlphaToken": 1, "BetaEditedToken": 1, "BetaToken": 0} {
		if got := searchPaths(t, cfg, token, true); len(got) != want {
			t.Errorf("edit 후 %s = %v, want %d건", token, got, want)
		}
	}

	// 복원돼 열린 채로 purge 해도 다른 문서는 남는다
	if out, err := svc.Purge(ctx, team, "docs/specs/a.md", who); err != nil {
		t.Fatalf("purge = %s %v", out, err)
	}
	deleteIndex(t, m, index)
	if _, err := cold.Restore(ctx, m.os, cfg, index); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]int{"AlphaToken": 0, "BetaEditedToken": 1} {
		if got := searchPaths(t, cfg, token, true); len(got) != want {
			t.Errorf("purge 후 %s = %v, want %d건", token, got, want)
		}
	}
}

// TestColdToolsRefuseAnotherWorkspacesTeam 은 RUN-1·API-4 의 재현이다. 같은 클러스터·스냅샷 repository 를 쓰는 다른
// meta-repo(B)의 팀은 A 의 list_cold·restore_cold 가 보지도 복원하지도 않는다. B 는 모든 달이 cold 로 가 열린 인덱스가
// 없어도 자기 cold store 로 자기 팀임을 안다.
func TestColdToolsRefuseAnotherWorkspacesTeam(t *testing.T) {
	a := newMCP(t, newConfig(t, nil))
	bcfg := newConfig(t, nil)
	b := newMCP(t, bcfg)
	ctx := context.Background()
	const doc = "docs/specs/legacy-queue.md"
	index := ingestOne(t, b, doc, daysAgo(hotDays+60), "NightlySettleQueue")
	if d := decisionOf(tierTeam(t, b), index); d.Action != cold.TierMoved {
		t.Fatalf("tier = %+v", d)
	}
	if text, isErr := b.call(t, "list_cold", map[string]any{}); isErr || !strings.Contains(text, index) {
		t.Errorf("B 는 열린 인덱스가 없어도 자기 cold 인덱스를 본다: %s", text)
	}
	svc := &archive.Service{OS: b.os, Config: bcfg, Cold: b.store, Indexer: b.indexer, Now: time.Now}
	if _, err := svc.Deprecate(ctx, bcfg.DefaultTeam, doc, "", archive.Audit{By: "jin", Reason: "e2e"}); err != nil {
		t.Fatal(err)
	}

	const owned = "다른 workspace 가 쓰는 팀입니다"
	if text, isErr := a.call(t, "list_cold", map[string]any{"team": bcfg.DefaultTeam}); !isErr || !strings.Contains(text, owned) || strings.Contains(text, index) {
		t.Errorf("A 는 B 의 cold 인덱스를 보지 못한다: %s", text)
	}
	if text, isErr := a.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); !isErr || !strings.Contains(text, owned) {
		t.Errorf("A 는 B 의 cold 인덱스를 복원하지 못한다: %s", text)
	}
	if isLive(t, b, index) {
		t.Fatal("거부한 restore 는 인덱스를 열지 않는다")
	}
	if text, isErr := a.call(t, "list_cold", map[string]any{}); isErr || !strings.Contains(text, "cold 인덱스가 없습니다") {
		t.Errorf("A 자기 팀은 cold 가 비어 있어도 에러가 아니다: %s", text)
	}

	// B 가 직접 복원하면 B 의 deprecate 표시가 다시 입혀진다
	if text, isErr := b.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); isErr {
		t.Fatalf("B restore = %s", text)
	}
	if got := searchPaths(t, bcfg, "NightlySettleQueue", false); len(got) != 0 {
		t.Errorf("deprecate 된 문서는 기본 검색에서 빠진다: %v", got)
	}
	if got := searchPaths(t, bcfg, "NightlySettleQueue", true); len(got) != 1 {
		t.Errorf("include_superseded 로는 보인다: %v", got)
	}
}

// TestTierPrintsDecisionsAlreadyExecutedWhenALaterIndexFails 는 OPS-10 의 재현이다. 앞선 인덱스를 지우고 원본을
// hot 에서 뺀 뒤 뒤의 인덱스에서 실패해도, tier 는 이미 한 일을 에러와 함께 출력한다.
func TestTierPrintsDecisionsAlreadyExecutedWhenALaterIndexFails(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	c := newCLI(t, cfg, nil)
	first := ingestOne(t, m, "docs/specs/first/a.md", daysAgo(hotDays+120), "FirstToken")
	second := ingestOne(t, m, "docs/specs/second/b.md", daysAgo(hotDays+30), "SecondToken")
	if first >= second {
		t.Fatalf("픽스처: %s 가 %s 보다 먼저 처리돼야 한다", first, second)
	}
	locked := filepath.Join(cfg.MetaRepoRoot, "docs/specs/second")
	if err := os.Chmod(locked, 0o555); err != nil { // 원본을 지우지 못하게
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	err := c.fail("tier", "-team", cfg.DefaultTeam)
	out := c.out.String()
	if err == nil {
		t.Fatalf("원본을 지우지 못하면 실패한다: %s", out)
	}
	for _, want := range []string{"tiered", first, "hot 에서 뺌: docs/specs/first/a.md", second} {
		if !strings.Contains(out, want) {
			t.Errorf("이미 한 일 %q 가 출력에 없다:\n%s\nerr=%v", want, out, err)
		}
	}
}
