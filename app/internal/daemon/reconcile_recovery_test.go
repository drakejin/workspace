package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
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
