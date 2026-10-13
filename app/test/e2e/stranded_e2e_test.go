//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/model"
)

// TestStrandedColdDocumentIsReportedNotReloadedAndRecoverable 는 R1-cold-1 의 재현이다. 고치기 전의 archive 는 같은 달의
// 최신 스냅샷만 되살려 다시 쓰고 옛 스냅샷을 지워, 옛 스냅샷에만 있던 문서를 잃었다 (목록에는 지워진 스냅샷의 줄이 남는다).
// 그 문서는 cold 소유가 아니다: 감사는 stranded-cold 로 알리고 -apply 로도 cold store 사본을 지우지 않으며, reload 는 싣지
// 않는다 (실으면 데몬이 파일 없는 문서로 보고 cold store 청크까지 지운다). raw 를 hot 에 다시 두면 다시 색인되고 tier 가 cold 로 보낸다.
func TestStrandedColdDocumentIsReportedNotReloadedAndRecoverable(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	c := newCLI(t, cfg, nil)
	ctx := context.Background()
	const lost, kept = "docs/specs/stranded/lost.md", "docs/specs/stranded/kept.md"
	date := daysAgo(hotDays + 60)
	ingestOne(t, m, "docs/knowledge/keep.md", daysAgo(0), "StrandKeepToken") // 팀이 이 workspace 의 것으로 남게
	index := ingestOne(t, m, lost, date, "StrandedLostToken")
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier#1 = %+v", d)
	}
	first := snapshotNames(t, m, index)
	ingestOne(t, m, kept, date, "StrandedKeptToken") // 같은 달에 늦게 들어온 문서 — 두 번째 스냅샷
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved || len(first) != 1 {
		t.Fatalf("tier#2 = %+v (첫 스냅샷 %v)", d, first)
	}
	// 고치기 전의 archive 가 남긴 손상: lost 만 담은 스냅샷이 지워졌다
	if err := m.os.Do(ctx, http.MethodDelete, "/_snapshot/"+cfg.Snapshot.Repository+"/"+first[0], nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	report := c.run(t, "audit", "-apply")
	if !strings.Contains(report, string(audit.StrandedCold)+" ") || !strings.Contains(report, lost) {
		t.Errorf("감사는 어느 스냅샷에도 없는 문서를 알린다:\n%s", report)
	}
	if _, ok, _ := m.store.Get(ctx, cold.ChunksKey(cfg.DefaultTeam, lost)); !ok {
		t.Fatal("audit -apply 가 유일한 cold store 사본을 지웠다")
	}
	c.run(t, "reload")
	if got := searchPaths(t, cfg, "StrandedLostToken", true); len(got) != 0 {
		t.Errorf("reload 는 hot 파일이 없는 갇힌 문서를 싣지 않는다 (데몬이 파일 없는 문서로 지운다): %v", got)
	}

	// 되살리기: raw 를 hot 에 다시 두면 다시 색인되고 (cold 소유가 아니다) tier 가 다시 cold 로 보낸다
	raw, ok, err := m.store.Get(ctx, cold.RawKey(cfg.DefaultTeam, lost))
	if err != nil || !ok {
		t.Fatalf("raw = %v %v", ok, err)
	}
	abs := filepath.Join(cfg.MetaRepoRoot, filepath.FromSlash(lost))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ingestFile(ctx, m.indexer, abs); err != nil {
		t.Fatal(err)
	}
	if d := decisionOf(tierTeam(t, m), index); d.Action != cold.TierMoved {
		t.Fatalf("tier#3 = %+v", d)
	}
	if report := c.run(t, "audit"); strings.Contains(report, string(audit.StrandedCold)) {
		t.Errorf("다시 cold 로 보낸 뒤에는 갇힌 문서가 없다:\n%s", report)
	}
	if _, err := cold.Restore(ctx, m.os, cfg, index); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"StrandedLostToken", "StrandedKeptToken"} {
		if got := searchPaths(t, cfg, token, false); len(got) != 1 {
			t.Errorf("복원 후 %s = %v", token, got)
		}
	}
}

// TestLateDocumentInAnISMMonthSurvivesAHotRebuild 는 R1-cold-4 의 재현이다. ISM 스냅샷(ism-cold-…, 목록 없음)은 그 스냅샷 전에
// 적재된 내용만 가진다. 스냅샷 뒤 그 달로 늦게 쓴 문서는 hot 재구축(인덱스 삭제 → reload) 뒤에도 다시 실리고, tier 가 스냅샷에
// 넣은 뒤에야 hot 에서 빠진다 — 예전에는 달 단위로 cold 소유로 읽혀 reload 가 건너뛰고 tier 가 hot 원본을 지워 어디에도 없게 됐다.
func TestLateDocumentInAnISMMonthSurvivesAHotRebuild(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	c := newCLI(t, cfg, nil)
	ctx := context.Background()
	date := daysAgo(hotDays + 60)
	ingestOne(t, m, "docs/knowledge/keep.md", daysAgo(0), "IsmKeepToken") // 팀이 이 workspace 의 것으로 남게
	const early = "docs/specs/ism/early.md"
	index := ingestOne(t, m, early, date, "IsmEarlyToken")
	// ISM 이 하는 일: 스냅샷(목록 없음) → 인덱스 삭제
	snapshot := "ism-cold-" + index + "-e2e"
	body := map[string]any{"indices": index, "include_global_state": false}
	if err := m.os.Do(ctx, http.MethodPut, "/_snapshot/"+cfg.Snapshot.Repository+"/"+snapshot, body, url.Values{"wait_for_completion": {"true"}}, nil); err != nil {
		t.Fatal(err)
	}
	deleteIndex(t, m, index)
	// 실제 ISM 은 인덱스를 만든 지 hot 기간이 지나서 스냅샷을 뜬다 — 그 사이 적재 시각은 스냅샷보다 한참 앞이다
	backdateIngest(t, m, early, 24*time.Hour)

	ingestOne(t, m, "docs/specs/ism/late.md", date, "IsmLateToken") // 스냅샷 뒤에 그 달로 늦게 쓴 문서
	deleteIndex(t, m, index)                                        // hot 재구축
	c.run(t, "reload")
	if got := searchPaths(t, cfg, "IsmLateToken", false); len(got) != 1 {
		t.Errorf("스냅샷 뒤에 쓴 문서는 reload 가 다시 싣는다: %v", got)
	}
	if got := searchPaths(t, cfg, "IsmEarlyToken", false); len(got) != 0 {
		t.Errorf("ISM 스냅샷이 가진 문서는 싣지 않는다: %v", got)
	}

	decisions := tierTeam(t, m)
	if d := decisionOf(decisions, index); d.Action != cold.TierMoved || !contains(d.RemovedFiles, "docs/specs/ism/late.md") {
		t.Errorf("늦은 문서는 tier 가 스냅샷에 넣은 뒤에 hot 에서 뺀다: %+v", decisions)
	}
	if _, err := cold.Restore(ctx, m.os, cfg, index); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"IsmEarlyToken", "IsmLateToken"} {
		if got := searchPaths(t, cfg, token, false); len(got) != 1 {
			t.Errorf("복원 후 %s = %v — 어느 스냅샷에도 없다", token, got)
		}
	}
}

// backdateIngest 는 cold store 에 남은 rel 의 청크 사본의 적재 시각을 by 만큼 앞당긴다.
func backdateIngest(t *testing.T, m *mcpHarness, rel string, by time.Duration) {
	t.Helper()
	ctx := context.Background()
	key := cold.ChunksKey(m.cfg.DefaultTeam, rel)
	body, ok, err := m.store.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("cold store %s = %v %v", key, ok, err)
	}
	chunks, _ := cold.DecodeChunks(body)
	for i := range chunks {
		chunks[i].IngestedAt = model.ParseTime(chunks[i].IngestedAt).Add(-by).UTC().Format(time.RFC3339Nano)
	}
	if err := m.store.Put(ctx, key, cold.EncodeChunks(chunks)); err != nil {
		t.Fatal(err)
	}
}
