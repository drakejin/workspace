package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
)

// keptFixture 는 git 레포인 meta-repo 에 커밋하지 않은 hot 원본 n개를 두고, 그 내용을 tier 가 cold 로 보낸 것처럼 꾸민 데몬이다 —
// 원본은 모두 tier 가 hot 에 남긴 cold 소유 원본(cold_kept)이다.
func keptFixture(t *testing.T, n int) (*Daemon, *logSink, string) {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	store := cold.NewLocalStore(t.TempDir())
	var snapshot map[string]any
	for i := range n {
		rel := fmt.Sprintf("docs/specs/legacy/old-%d.md", i)
		writeDoc(t, root, rel, fmt.Sprintf("---\ndate: 2026-01-10\n---\n# 레거시 %d\nColdKeptToken%d\n", i, i))
		snapshot = archiveToCold(t, store, root, rel) // 같은 달 — 스냅샷 이름도 같다
	}
	fos := &routedOS{indices: []string{recoveryIndex}, snapshots: []map[string]any{snapshot}}
	logs := &logSink{}
	d, err := New(context.Background(), Deps{Config: recoveryConfig(root), OS: fos, Ask: &stubAsker{}, Cold: store, Log: logs.Logf, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return d, logs, root
}

// countGitCalls 는 PATH 의 git 을 부를 때마다 한 줄 남기고 진짜 git 을 부르는 가짜를 앞에 둔다. 지금까지 부른 횟수를 돌려주는 함수다.
func countGitCalls(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 이 없다")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> '%s'\nexec '%s' \"$@\"\n", calls, real)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(calls)
		return strings.Count(string(b), "\n")
	}
}

// R5-final-daemon-5: tier 가 hot 에 남긴 cold 원본을 사이클마다 원본 하나에 git 프로세스 서넛으로 다시 보지 않는다 — 원본의 stat 과
// git 상태(HEAD·index)가 그대로면 지난 답을 쓴다. 바뀐 것이 없는 사이클의 git 호출은 남긴 원본 수와 상관없이 몇 번뿐이고,
// 커밋하면(HEAD 가 바뀐다 — 원본의 stat 은 그대로다) 다시 봐 cold_owned 로 센다.
func TestKeptOriginalsAreNotRecheckedWithGitEveryCycle(t *testing.T) {
	const kept = 4
	d, logs, root := keptFixture(t, kept)
	gitCalls := countGitCalls(t)
	ctx := context.Background()
	if sum, _, err := d.rec.Once(ctx); err != nil || sum.ColdKept != kept {
		t.Fatalf("첫 사이클 = %+v %v\n%s", sum, err, logs)
	}

	before := gitCalls()
	sum, _, err := d.rec.Once(ctx)

	if err != nil || sum.ColdKept != kept || sum.ColdOwned != 0 {
		t.Fatalf("바뀐 것 없는 사이클 = %+v %v\n%s", sum, err, logs)
	}
	if calls := gitCalls() - before; calls >= kept {
		t.Errorf("바뀐 것 없는 사이클이 git 을 %d번 불렀다 (남긴 원본 %d개마다 다시 본다)", calls, kept)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "원본")
	if sum, _, err := d.rec.Once(ctx); err != nil || sum.ColdOwned != kept || sum.ColdKept != 0 {
		t.Errorf("커밋한 뒤 = %+v %v (git 상태가 바뀌어 다시 보고 cold_owned 로 센다)\n%s", sum, err, logs)
	}
}

// R5-final-daemon-5: git 을 부를 수 없으면 남긴 원본은 그대로 cold_kept 로 센다(tier 도 지우지 않는다). 그 경고는 원본마다·사이클마다가
// 아니라 같은 까닭이면 한 번만 남긴다.
func TestGitFailureForKeptOriginalsIsLoggedOnce(t *testing.T) {
	const kept = 3
	d, logs, _ := keptFixture(t, kept)
	t.Setenv("PATH", t.TempDir()) // git 이 없다
	ctx := context.Background()

	for cycle := range 2 {
		if sum, _, err := d.rec.Once(ctx); err != nil || sum.ColdKept != kept || sum.ColdOwned != 0 {
			t.Fatalf("사이클 %d = %+v %v\n%s", cycle, sum, err, logs)
		}
	}

	if n := strings.Count(logs.String(), "git 으로 확인하지 못했습니다"); n != 1 {
		t.Errorf("git 을 부를 수 없다는 경고 %d번 (한 번만)\n%s", n, logs)
	}
}
