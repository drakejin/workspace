package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// issueRepo 는 done-issue 를 돌릴 임시 워크스페이스다: <root>/main/repo 와 그 worktree <repo>/.worktrees/ISSUE-1.
type issueRepo struct {
	sb       *sandbox
	root     string
	repo     string
	worktree string
}

// gitEnv 는 사용자 git 설정을 읽지 않게 하고 커밋 작성자를 정한다.
var gitEnv = map[string]string{
	"GIT_CONFIG_NOSYSTEM": "1",
	"GIT_AUTHOR_NAME":     "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
	"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid",
}

func newIssueRepo(t *testing.T) *issueRepo {
	t.Helper()
	sb := newSandbox(t, "git")
	root := filepath.Join(sb.dir, "ws")
	sb.copyTo(root, "scripts/done-issue")
	ir := &issueRepo{sb: sb, root: root, repo: filepath.Join(root, "main", "repo")}
	ir.worktree = filepath.Join(ir.repo, ".worktrees", "ISSUE-1")
	writeFile(t, filepath.Join(ir.repo, "README.md"), "hello\n")
	ir.git("init", "-q", "-b", "main")
	writeFile(t, filepath.Join(ir.repo, ".git", "info", "exclude"), ".worktrees/\n")
	ir.git("add", "README.md")
	ir.git("commit", "-q", "-m", "init")
	ir.git("worktree", "add", "-q", "-b", "ISSUE-1", ir.worktree)
	return ir
}

func (ir *issueRepo) git(args ...string) string {
	ir.sb.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", ir.repo}, args...)...)
	cmd.Env = ir.sb.env(gitEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		ir.sb.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// doneIssue 는 scripts/done-issue/run.sh 를 args 로 돌린다.
func (ir *issueRepo) doneIssue(args ...string) result {
	ir.sb.t.Helper()
	return ir.sb.bash(gitEnv, append([]string{filepath.Join(ir.root, "scripts", "done-issue", "run.sh")}, args...)...)
}

// dirty 는 worktree 에 커밋하지 않은 변경(수정)과 untracked 파일을 만든다.
func (ir *issueRepo) dirty() {
	ir.sb.t.Helper()
	writeFile(ir.sb.t, filepath.Join(ir.worktree, "README.md"), "changed\n")
	writeFile(ir.sb.t, filepath.Join(ir.worktree, "wip.txt"), "unsaved work\n")
}

func TestDoneIssueRemovesCleanWorktree(t *testing.T) {
	ir := newIssueRepo(t)
	r := ir.doneIssue("ISSUE-1", "repo")
	if r.err != nil {
		t.Fatalf("clean worktree 제거 실패: %v\n%s", r.err, r.output())
	}
	if !strings.Contains(r.stdout, "worktree 'ISSUE-1' removed") || exists(ir.worktree) {
		t.Errorf("worktree 가 지워지지 않았다:\n%s", r.output())
	}
	if !strings.Contains(ir.git("branch", "--list", "ISSUE-1"), "ISSUE-1") {
		t.Errorf("브랜치는 남겨야 한다")
	}
}

// FILE-2: 커밋하지 않은 변경·untracked 파일이 있으면 --force 없이는 지우지 않고, 잃게 될 파일을 보여 준다.
func TestDoneIssueRefusesDirtyWorktree(t *testing.T) {
	ir := newIssueRepo(t)
	ir.dirty()
	r := ir.doneIssue("ISSUE-1", "repo")
	if r.err == nil {
		t.Errorf("변경이 남은 worktree 인데 성공(exit 0)했다:\n%s", r.output())
	}
	if !exists(filepath.Join(ir.worktree, "wip.txt")) {
		t.Fatalf("untracked wip.txt 가 사라졌다:\n%s", r.output())
	}
	for _, want := range []string{"wip.txt", "README.md", "--force"} {
		if !strings.Contains(r.output(), want) {
			t.Errorf("출력에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// FILE-2: --force 를 주면 지우되, 무엇을 버렸는지 보여 준다.
func TestDoneIssueForceRemovesAndListsLostChanges(t *testing.T) {
	ir := newIssueRepo(t)
	ir.dirty()
	r := ir.doneIssue("--force", "ISSUE-1", "repo")
	if r.err != nil {
		t.Fatalf("--force 제거 실패: %v\n%s", r.err, r.output())
	}
	if exists(ir.worktree) {
		t.Errorf("--force 인데 worktree 가 남았다:\n%s", r.output())
	}
	if !strings.Contains(r.output(), "wip.txt") {
		t.Errorf("버린 파일을 보여 주지 않았다:\n%s", r.output())
	}
}

// FILE-2: git 이 지우지 못하는 경우(lock 건 worktree)에도 rm -rf 로 밀어 버리지 않는다 — --force 여도.
func TestDoneIssueNeverDeletesLockedWorktree(t *testing.T) {
	ir := newIssueRepo(t)
	ir.dirty()
	ir.git("worktree", "lock", ir.worktree)
	r := ir.doneIssue("--force", "ISSUE-1", "repo")
	if r.err == nil {
		t.Errorf("lock 건 worktree 인데 성공(exit 0)했다:\n%s", r.output())
	}
	if !exists(filepath.Join(ir.worktree, "wip.txt")) {
		t.Fatalf("lock 건 worktree 를 지웠다:\n%s", r.output())
	}
}

// FILE-2: worktree 가 아닌 디렉토리(.worktrees/ISSUE-1-2)도 rm -rf 하지 않는다.
func TestDoneIssueNeverDeletesStrayDirectory(t *testing.T) {
	ir := newIssueRepo(t)
	stray := filepath.Join(ir.repo, ".worktrees", "ISSUE-1-2")
	writeFile(t, filepath.Join(stray, "notes.txt"), "keep me\n")
	r := ir.doneIssue("--force", "ISSUE-1", "repo")
	if r.err == nil {
		t.Errorf("worktree 가 아닌 디렉토리가 있는데 성공(exit 0)했다:\n%s", r.output())
	}
	if !exists(filepath.Join(stray, "notes.txt")) {
		t.Fatalf("worktree 가 아닌 디렉토리를 지웠다:\n%s", r.output())
	}
}

// R1-shell-1: --force 의 "버렸다" 안내는 git 이 worktree 를 실제로 지운 뒤에만 한다. git 이 거부하면(lock) 변경은
// 그대로 남았다고 말한다 — 먼저 "버립니다" 를 찍고 "남겨 둡니다" 를 찍으면 무엇이 사라졌는지 헷갈린다.
func TestDoneIssueForceReportsDiscardOnlyAfterRemoval(t *testing.T) {
	t.Run("lock 이라 못 지우면 버렸다고 말하지 않는다", func(t *testing.T) {
		ir := newIssueRepo(t)
		ir.dirty()
		ir.git("worktree", "lock", ir.worktree)

		r := ir.doneIssue("--force", "ISSUE-1", "repo")

		if r.err == nil {
			t.Fatalf("lock 건 worktree 인데 성공(exit 0)했다:\n%s", r.output())
		}
		for _, claim := range []string{"버립니다", "버렸습니다"} {
			if strings.Contains(r.output(), claim) {
				t.Errorf("지우지 못했는데 %q 라고 안내했다:\n%s", claim, r.output())
			}
		}
		if !strings.Contains(r.stderr, "변경은 그대로") || !strings.Contains(r.stderr, "wip.txt") {
			t.Errorf("남은 변경을 알려야 한다:\n%s", r.output())
		}
	})
	t.Run("지운 뒤에 버린 변경을 보여 준다", func(t *testing.T) {
		ir := newIssueRepo(t)
		ir.dirty()

		r := ir.doneIssue("--force", "ISSUE-1", "repo")

		if r.err != nil {
			t.Fatalf("--force 제거 실패: %v\n%s", r.err, r.output())
		}
		discarded := strings.Index(r.stdout, "버렸습니다")
		if discarded < 0 || !strings.Contains(r.stdout[discarded:], "wip.txt") {
			t.Errorf("지운 뒤 버린 변경을 보여 주지 않았다:\n%s", r.output())
		}
	})
}

// ignoreLocalFiles 는 .env 와 build/ 를 gitignore 하고 worktree 에 그 파일을 만든다 (git status 의 기본 출력에 안 보인다).
func (ir *issueRepo) ignoreLocalFiles() {
	ir.sb.t.Helper()
	writeFile(ir.sb.t, filepath.Join(ir.repo, ".git", "info", "exclude"), ".worktrees/\n.env\nbuild/\n")
	writeFile(ir.sb.t, filepath.Join(ir.worktree, ".env"), "LOCAL_SECRET=1\n")
	writeFile(ir.sb.t, filepath.Join(ir.worktree, "build", "out.bin"), "bin\n")
}

// R1-shell-2: git worktree remove 는 gitignored 파일(.env, 빌드 산출물)도 지운다. git 이 깨끗하다고 보는 worktree 라
// 그대로 지우되, 함께 지운 gitignored 파일을 보여 준다.
func TestDoneIssueListsIgnoredFilesItRemoves(t *testing.T) {
	ir := newIssueRepo(t)
	ir.ignoreLocalFiles()

	r := ir.doneIssue("ISSUE-1", "repo")

	if r.err != nil {
		t.Fatalf("gitignored 파일만 있는 worktree 는 지운다: %v\n%s", r.err, r.output())
	}
	if exists(ir.worktree) {
		t.Fatalf("worktree 가 남았다:\n%s", r.output())
	}
	for _, want := range []string{"gitignored", ".env", "build/"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("함께 지운 gitignored 파일을 보여 줘야 한다 — 출력에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// R1-shell-2: 변경이 남아 멈출 때 보여 주는 "지우면 잃는 것" 에 gitignored 파일도 넣는다 (--force 면 그것도 지워진다).
func TestDoneIssueRefusalListsIgnoredFilesToo(t *testing.T) {
	ir := newIssueRepo(t)
	ir.dirty()
	ir.ignoreLocalFiles()

	r := ir.doneIssue("ISSUE-1", "repo")

	if r.err == nil {
		t.Fatalf("변경이 남은 worktree 인데 성공(exit 0)했다:\n%s", r.output())
	}
	for _, want := range []string{"wip.txt", ".env", "build/"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("잃게 될 것에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// R2-followups-1: --force 로 git 이 지우다가 실패하면(여기서는 .worktrees 가 읽기 전용이라 마지막 rmdir 이 실패한다 —
// 안의 파일은 이미 다 지웠다) "변경은 그대로 있다" 고 말하지 않는다. 실제로 무엇이 지워지고 무엇이 남았는지 확인해 알린다.
func TestDoneIssueForceFailureReportsWhatWasLost(t *testing.T) {
	ir := newIssueRepo(t)
	ir.dirty()
	parent := filepath.Dir(ir.worktree)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	r := ir.doneIssue("--force", "ISSUE-1", "repo")

	if r.err == nil {
		t.Skipf("읽기 전용 디렉토리에서도 지워졌다 (root 로 도는가?):\n%s", r.output())
	}
	if exists(filepath.Join(ir.worktree, "wip.txt")) {
		t.Fatalf("전제: git 이 wip.txt 를 지웠어야 한다:\n%s", r.output())
	}
	if strings.Contains(r.output(), "그대로 있습니다") {
		t.Errorf("git 이 이미 지운 변경을 그대로 있다고 안내했다:\n%s", r.output())
	}
	lost := strings.Index(r.stderr, "지워졌습니다")
	if lost < 0 || !strings.Contains(r.stderr[lost:], "wip.txt") {
		t.Errorf("지워진 변경(wip.txt)을 알려야 한다:\n%s", r.output())
	}
}

// R2-followups-1: git 이 일부만 지우고 멈추면(하위 디렉토리가 읽기 전용) 남은 것과 지워진 것을 나눠 알린다 —
// "그대로 있다" 고 한 파일은 실제로 있어야 하고, 사라진 파일을 남았다고 하지 않는다.
func TestDoneIssueForcePartialFailureListsOnlyWhatRemains(t *testing.T) {
	ir := newIssueRepo(t)
	names := []string{"a-wip.txt", "b-wip.txt", "c-wip.txt", "d-wip.txt", "e-wip.txt", "m/b.txt"}
	for _, n := range names {
		writeFile(t, filepath.Join(ir.worktree, n), "wip\n")
	}
	locked := filepath.Join(ir.worktree, "m")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	r := ir.doneIssue("--force", "ISSUE-1", "repo")

	if r.err == nil {
		t.Skipf("읽기 전용 디렉토리에서도 지워졌다 (root 로 도는가?):\n%s", r.output())
	}
	kept := r.stderr
	if i := strings.Index(kept, "지워졌습니다"); i >= 0 {
		kept = kept[:i]
	}
	for _, n := range names {
		if !exists(filepath.Join(ir.worktree, n)) && strings.Contains(kept, n) {
			t.Errorf("git 이 지운 %s 를 남아 있다고 안내했다:\n%s", n, r.output())
		}
	}
	if !strings.Contains(r.stderr, "m/b.txt") {
		t.Errorf("남은 m/b.txt 를 알려야 한다:\n%s", r.output())
	}
}
