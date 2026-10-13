package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/state"
)

// newRecoveryDaemon 은 가짜 OpenSearch·로컬 cold store 위의 데몬이다 (그래프 없음).
func newRecoveryDaemon(t *testing.T, cfg config.Config, fos *routedOS) (*Daemon, *logSink) {
	t.Helper()
	logs := &logSink{}
	d, err := New(context.Background(), Deps{Config: cfg, OS: fos, Ask: &stubAsker{}, Cold: cold.NewLocalStore(t.TempDir()),
		Log: logs.Logf, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return d, logs
}

// IDX-11: 읽지 못한 디렉토리가 있으면(스캔이 일부만 됐다) 색인에 있는데 스캔에 없는 문서를 지우지 않는다 — 그 디렉토리의 문서가
// "파일이 없다" 로 보여 지워지지 않게. 다 읽히면 정말 없는 문서만 지운다.
func TestReconcileSkipsTombstonesOnAPartialScan(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 는 권한이 없는 디렉토리도 읽는다")
	}
	root := t.TempDir()
	writeDoc(t, root, "docs/specs/open/a.md", "# 열림\n본문\n")
	writeDoc(t, root, "docs/specs/locked/b.md", "# 잠김\n본문\n")
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{
		indexedHead(t, root, "docs/specs/open/a.md", nil),
		indexedHead(t, root, "docs/specs/locked/b.md", nil),
		{"_index": recoveryIndex, "_source": map[string]any{"source_path": "docs/specs/gone.md", "team": "t", "workspace": "ws", "memory_kind": "episodic", "content_sha": "x"}},
	}}
	locked := filepath.Join(root, "docs", "specs", "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	d, logs := newRecoveryDaemon(t, recoveryConfig(root), fos)

	sum, diff, err := d.rec.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Partial || len(diff) != 0 || fos.sent("_delete_by_query", "docs/specs/") {
		t.Fatalf("일부만 읽은 스캔은 지우지 않는다: partial=%v diff=%+v\n%s", sum.Partial, diff, logs)
	}
	if !logs.has("스캔이 일부만 됐습니다") {
		t.Errorf("건너뛴 사실을 로그에 남긴다\n%s", logs)
	}

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, diff, err = d.rec.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].SourcePath != "docs/specs/gone.md" || diff[0].Change != ChangeRemoved {
		t.Errorf("다 읽히면 정말 없는 문서만 지운다: %+v", diff)
	}
}

// R4-daemon-a-1: 판정·cold 문서 조치 원장을 읽지 못해도(권한·IO 오류) 사이클은 hot 동기화·tombstone 을 마친다 — 숨김 표시는
// 지난 값(처음이면 색인에 있는 값)을 잇고, 수렴(Converge)이 그 오류를 사이클 오류로 알린다. 예전에는 원장 하나를 읽지 못하면
// 고친 문서도, 지운 문서도 색인에 반영되지 않았다.
func TestReconcileKeepsIndexingWhenALedgerCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 는 권한이 없는 파일도 읽는다")
	}
	const hidden = "docs/specs/pay/old.md"
	root := t.TempDir()
	writeDoc(t, root, docA, contentA) // 새 문서 — 색인한다
	writeDoc(t, root, hidden, "# 옛 결제\n본문\n")
	staleHidden := indexedHead(t, root, hidden, map[string]any{"superseded_by": "docs/specs/pay/new.md", "content_sha": "old", "mtime_ms": 1}) // 색인 뒤에 고쳤다
	gone := map[string]any{"_index": recoveryIndex, "_source": map[string]any{"source_path": "docs/specs/gone.md", "team": "t",
		"workspace": "ws", "memory_kind": "episodic", "content_sha": "x"}}
	archive := state.NewArchiveState(root)
	if err := archive.Ledger.Append("t", state.ArchiveRecord{Action: state.ArchiveDeprecate, Path: "docs/specs/x.md", By: "test"}); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(state.CacheDir(root), "archive", "t.jsonl")
	if err := os.Chmod(ledger, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ledger, 0o644) })
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{staleHidden, gone}}
	d, logs := newRecoveryDaemon(t, recoveryConfig(root), fos)

	sum, diff, err := d.rec.Once(context.Background())

	if err == nil || !strings.Contains(err.Error(), "converge") {
		t.Errorf("수렴이 원장 오류를 알려야 한다: %v", err)
	}
	if sum.Indexed != 2 || sum.Removed != 1 {
		t.Errorf("색인·tombstone 은 마쳐야 한다: %+v\n%s", sum, logs)
	}
	changes := map[string]Change{}
	for _, e := range diff {
		changes[e.SourcePath] = e.Change
	}
	if changes[docA] != ChangeNew || changes["docs/specs/gone.md"] != ChangeRemoved {
		t.Errorf("변경분 = %v", changes)
	}
	if by, _ := d.rec.SupersededBy(hidden); by != "docs/specs/pay/new.md" {
		t.Errorf("다시 쓰는 문서는 색인에 있던 숨김 표시를 잇는다: %q", by)
	}
}

// gitRun 은 root 에서 git 을 돌린다 (테스트용 이름·메일).
func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// archiveToCold 는 hot 파일 rel 의 지금 내용을 tier 가 cold 로 보낸 것처럼 꾸민다 — cold store 에 원본·청크·목록을 두고,
// 그 달의 tier 스냅샷 하나를 돌려준다 (routedOS.snapshots 에 넣는다).
func archiveToCold(t *testing.T, store cold.Store, root, rel string) map[string]any {
	t.Helper()
	ctx := context.Background()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := document.Build(document.Input{Workspace: "ws", SourcePath: rel, Content: string(content), DefaultTeam: "t", Now: time.Now(), FallbackTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	index := naming.IndexFor(b.Chunks[0])
	snapshot := "cold-" + index + "-20261001t000000-000000001"
	for key, body := range map[string]string{cold.RawKey("t", rel): string(content), cold.ChunksKey("t", rel): cold.EncodeChunks(b.Chunks)} {
		if err := store.Put(ctx, key, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := cold.AppendManifest(ctx, store, "t", index, snapshot, []cold.ManifestEntry{{SourcePath: rel, ContentSHA: b.ContentSHA}}); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"snapshot": snapshot, "indices": []string{index}, "state": "SUCCESS", "start_time": "2026-10-01T00:00:00.000Z"}
}

// R4-ops-cold-1: tier 가 git 이 되살릴 수 없어(커밋하지 않았다) hot 에 남긴 cold 소유 원본은 cold_owned 로 세지 않는다 —
// cold_owned 는 다음 tier 가 정리할 원본이다 (status 가 "tier 가 정리한다" 고 안내한다). 남긴 원본은 cold_kept 로 따로 세고,
// 커밋하면(stat 은 그대로다) 다음 사이클부터 cold_owned 로 센다.
func TestColdOwnedOriginalsKeptForGitAreCountedApart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	const rel = "docs/specs/legacy/old.md"
	writeDoc(t, root, rel, "---\ndate: 2026-01-10\n---\n# 레거시\nColdKeptToken\n")
	store := cold.NewLocalStore(t.TempDir())
	fos := &routedOS{indices: []string{recoveryIndex}, snapshots: []map[string]any{archiveToCold(t, store, root, rel)}}
	logs := &logSink{}
	d, err := New(ctx, Deps{Config: recoveryConfig(root), OS: fos, Ask: &stubAsker{}, Cold: store, Log: logs.Logf, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	d.cycle("test")
	if rec := d.lastCycle; rec == nil || rec.Summary == nil || rec.Summary.Indexed != 0 || rec.Summary.ColdOwned != 0 || rec.Summary.ColdKept != 1 {
		t.Fatalf("커밋하지 않은 cold 소유 원본 = %+v (cold_kept 로 센다)\n%s", rec, logs)
	}
	if !logs.has("cold_owned=0 cold_kept=1 ") {
		t.Errorf("사이클 로그가 cold_kept 를 알려야 한다\n%s", logs)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "원본")
	sum, _, err := d.rec.Once(ctx)
	if err != nil || sum.Indexed != 0 || sum.ColdOwned != 1 || sum.ColdKept != 0 {
		t.Errorf("커밋한 뒤 = %+v %v (tier 가 정리할 cold_owned)\n%s", sum, err, logs)
	}
}

// IDX-11: 스캔한 파일 수가 sanityMaxFiles 를 넘으면 설정 오류로 보고 색인하지 않는다 (지우지도 않는다).
func TestReconcileRefusesTooManyFiles(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/specs/x/a.md", "# a\n본문\n")
	writeDoc(t, root, "docs/specs/x/b.md", "# b\n본문\n")
	cfg := recoveryConfig(root)
	cfg.Reconcile.SanityMaxFiles = 1
	fos := &routedOS{indices: []string{recoveryIndex}}
	d, logs := newRecoveryDaemon(t, cfg, fos)

	sum, diff, err := d.rec.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Refused || len(diff) != 0 || fos.sent("/_bulk", "") || !logs.has("sanityMaxFiles") {
		t.Errorf("거부해야 한다: refused=%v diff=%+v\n%s", sum.Refused, diff, logs)
	}
}

// IDX-11: cold 문서 조치 원장(.cache/archive)의 deprecate 는 수렴이 색인의 deprecated 로 옮긴다.
func TestConvergeAppliesArchiveDeprecations(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, docA, contentA)
	if err := state.NewArchiveState(root).Ledger.Append("t", state.ArchiveRecord{Action: state.ArchiveDeprecate, Path: docA, By: "test", Reason: "틀림"}); err != nil {
		t.Fatal(err)
	}
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{indexedHead(t, root, docA, nil)}}
	d, _ := newRecoveryDaemon(t, recoveryConfig(root), fos)

	n, err := d.rec.Converge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || !fos.sent("/_update_by_query", `"deprecated":true`) || !fos.sent("/_update_by_query", docA) {
		t.Errorf("deprecated 를 색인에 옮겨야 한다: changed=%d", n)
	}
}
