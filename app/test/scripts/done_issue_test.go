package scripts

import (
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
