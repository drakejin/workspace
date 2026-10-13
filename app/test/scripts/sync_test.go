package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// syncWorkspace 는 scripts/sync 를 돌릴 임시 워크스페이스다: 원격(bare) 하나와 그것을 clone 한 <root>/main/<name> 들.
type syncWorkspace struct {
	sb     *sandbox
	root   string
	src    string // 원격에 push 하는 작업 사본
	origin string // bare 원격 — 기본 브랜치(HEAD)는 src 의 첫 브랜치
}

func (w *syncWorkspace) git(dir string, args ...string) string {
	w.sb.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = w.sb.env(gitEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.sb.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newSyncWorkspace 는 기본 브랜치가 defaultBranch 인 원격을 만든다.
func newSyncWorkspace(t *testing.T, defaultBranch string) *syncWorkspace {
	t.Helper()
	sb := newSandbox(t, "git")
	w := &syncWorkspace{sb: sb, root: filepath.Join(sb.dir, "ws"), src: filepath.Join(sb.dir, "src"), origin: filepath.Join(sb.dir, "origin.git")}
	sb.copyTo(w.root, "scripts/sync")
	writeFile(t, filepath.Join(w.src, "README.md"), "v1\n")
	w.git(sb.dir, "init", "-q", "-b", defaultBranch, w.src)
	w.git(w.src, "add", "README.md")
	w.git(w.src, "commit", "-q", "-m", "v1")
	w.git(sb.dir, "clone", "-q", "--bare", w.src, w.origin)
	w.git(w.src, "remote", "add", "origin", w.origin)
	return w
}

// clone 은 원격을 main/<name> 에 받는다 (git clone 이 origin/HEAD 를 원격의 기본 브랜치로 정한다).
func (w *syncWorkspace) clone(name string) string {
	w.sb.t.Helper()
	dst := filepath.Join(w.root, "main", name)
	w.git(w.sb.dir, "clone", "-q", w.origin, dst)
	return dst
}

// push 는 원격의 branch 에 새 커밋을 올리고 그 sha 를 돌려준다.
func (w *syncWorkspace) push(branch, body string) string {
	w.sb.t.Helper()
	writeFile(w.sb.t, filepath.Join(w.src, "README.md"), body)
	w.git(w.src, "commit", "-q", "-am", body)
	w.git(w.src, "push", "-q", "origin", branch)
	return w.git(w.src, "rev-parse", "HEAD")
}

func (w *syncWorkspace) pull() result {
	w.sb.t.Helper()
	return w.sb.bash(gitEnv, filepath.Join(w.root, "scripts", "sync", "_pull_repos.sh"), filepath.Join(w.root, "main"))
}

// DOC-11 / OPS-15: sync 는 main/master 를 하드코딩하지 않고 repo 마다 원격의 기본 브랜치(origin/HEAD — run-issues 와 같은 규칙)를 pull 한다.
// 기본 브랜치가 develop 인 repo 도 받고, 기본 브랜치가 아닌 브랜치에 있는 repo 는 건너뛴다.
func TestSyncPullsEachReposDefaultBranch(t *testing.T) {
	w := newSyncWorkspace(t, "develop")
	onDefault := w.clone("app")
	onTopic := w.clone("topic")
	w.git(onTopic, "checkout", "-q", "-b", "feature-x")
	want := w.push("develop", "v2\n")

	r := w.pull()

	if r.err != nil {
		t.Fatalf("_pull_repos.sh: %v\n%s", r.err, r.output())
	}
	if !strings.Contains(r.stdout, "[sync] app: pulled 'develop'") {
		t.Errorf("기본 브랜치 develop 을 pull 해야 한다:\n%s", r.output())
	}
	if got := w.git(onDefault, "rev-parse", "HEAD"); got != want {
		t.Errorf("app HEAD = %s, want %s", got, want)
	}
	if !strings.Contains(r.stdout, "[skip] topic: on branch 'feature-x'") {
		t.Errorf("기본 브랜치가 아닌 repo 는 건너뛴다:\n%s", r.output())
	}
	if !strings.Contains(r.stdout, "2 repos / 1 synced / 1 skipped / 0 failed") {
		t.Errorf("요약:\n%s", r.output())
	}
}

// 기본 브랜치가 main 인 흔한 경우는 예전처럼 pull 한다.
func TestSyncStillPullsMain(t *testing.T) {
	w := newSyncWorkspace(t, "main")
	repo := w.clone("app")
	want := w.push("main", "v2\n")

	r := w.pull()

	if r.err != nil || !strings.Contains(r.stdout, "[sync] app: pulled 'main'") {
		t.Fatalf("_pull_repos.sh: %v\n%s", r.err, r.output())
	}
	if got := w.git(repo, "rev-parse", "HEAD"); got != want {
		t.Errorf("app HEAD = %s, want %s", got, want)
	}
}

// origin/HEAD 는 clone 때 정해지고 fetch 가 고치지 않는다 — 원격이 기본 브랜치 이름을 master 에서 main 으로 바꾼 뒤 main 으로
// 옮긴 repo 는 origin/HEAD 가 아직 master 다. 예전 규칙(main/master)에 있는 repo 는 그래도 pull 한다.
func TestSyncPullsMainWhenOriginHeadIsStale(t *testing.T) {
	w := newSyncWorkspace(t, "master")
	repo := w.clone("app")
	w.git(w.origin, "branch", "-m", "master", "main")
	w.git(w.origin, "symbolic-ref", "HEAD", "refs/heads/main")
	w.git(w.src, "branch", "-m", "master", "main")
	w.git(repo, "fetch", "-q", "origin")
	w.git(repo, "checkout", "-q", "-b", "main", "origin/main")
	if head := w.git(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); head != "origin/master" {
		t.Fatalf("전제: origin/HEAD = %s (낡은 origin/master 여야 한다)", head)
	}
	want := w.push("main", "v2\n")

	r := w.pull()

	if r.err != nil || !strings.Contains(r.stdout, "[sync] app: pulled 'main'") {
		t.Fatalf("_pull_repos.sh: %v\n%s", r.err, r.output())
	}
	if got := w.git(repo, "rev-parse", "HEAD"); got != want {
		t.Errorf("app HEAD = %s, want %s", got, want)
	}
}

// OPS-14: 커밋이 없는(unborn HEAD — git init 만 했거나 빈 원격을 clone 한) repo 가 있어도 sync 는 멈추지 않는다.
// 그 repo 는 [skip] 으로 알리고 뒤의 repo 를 계속 pull 한다.
func TestSyncSkipsRepoWithoutCommitsAndContinues(t *testing.T) {
	w := newSyncWorkspace(t, "main")
	w.git(w.sb.dir, "init", "-q", "-b", "main", filepath.Join(w.root, "main", "aaa-empty"))
	repo := w.clone("zzz-ok")
	want := w.push("main", "v2\n")

	r := w.pull()

	if r.err != nil {
		t.Fatalf("_pull_repos.sh: %v\n%s", r.err, r.output())
	}
	if !strings.Contains(r.stdout, "[skip] aaa-empty:") {
		t.Errorf("커밋이 없는 repo 는 [skip] 으로 알려야 한다:\n%s", r.output())
	}
	if got := w.git(repo, "rev-parse", "HEAD"); got != want {
		t.Errorf("뒤의 repo 를 pull 하지 않았다 — zzz-ok HEAD = %s, want %s\n%s", got, want, r.output())
	}
	if !strings.Contains(r.stdout, "2 repos / 1 synced / 1 skipped / 0 failed") {
		t.Errorf("요약:\n%s", r.output())
	}
}

// OPS-14: detached HEAD 인 repo 도 [skip] 이다 (어느 브랜치를 pull 할지 모른다).
func TestSyncSkipsDetachedHead(t *testing.T) {
	w := newSyncWorkspace(t, "main")
	repo := w.clone("app")
	w.git(repo, "checkout", "-q", "--detach")

	r := w.pull()

	if r.err != nil || !strings.Contains(r.stdout, "[skip] app:") {
		t.Fatalf("detached HEAD 는 건너뛴다: %v\n%s", r.err, r.output())
	}
}

// OPS-14: sync/run.sh 는 pull 실패를 삼키지 않는다 — INDEX.md 단계까지 마친 뒤 실패를 알리고 0 이 아닌 값으로 끝난다.
func TestSyncRunReportsPullFailure(t *testing.T) {
	w := newSyncWorkspace(t, "main")
	w.clone("app")
	if err := os.RemoveAll(w.origin); err != nil { // 원격이 사라져 pull 이 실패한다
		t.Fatal(err)
	}

	r := w.sb.bash(gitEnv, filepath.Join(w.root, "scripts", "sync", "run.sh"))

	if r.err == nil {
		t.Errorf("pull 이 실패했는데 sync 가 성공(exit 0)했다:\n%s", r.output())
	}
	for _, want := range []string{"[fail] app", "0 synced / 0 skipped / 1 failed", "[index] INDEX.md"} {
		if !strings.Contains(r.output(), want) {
			t.Errorf("출력에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// R4-ops-scripts-3: make init 은 끝에 sync 를 부르므로 pull 에 실패한 repo 가 있으면 init 도 끝까지 돈 뒤 0 이 아닌 값으로 끝난다 (README §5.1).
func TestInitReportsPullFailureOfSync(t *testing.T) {
	w := newSyncWorkspace(t, "main")
	w.sb.extra = append(w.sb.extra, toolDirs(t, []string{"jq"})...)
	w.sb.copyTo(w.root, "scripts/init", "scripts/hooks")
	w.git(w.sb.dir, "init", "-q", w.root)
	writeFile(t, filepath.Join(w.root, "workspace.seed.json"),
		`{"githubHost": "github.invalid", "org": "o", "workspaces": [{"name": "app"}]}`)
	w.clone("app")
	if err := os.RemoveAll(w.origin); err != nil { // 원격이 사라져 pull 이 실패한다
		t.Fatal(err)
	}

	r := w.sb.bash(gitEnv, filepath.Join(w.root, "scripts", "init", "run.sh"))

	if r.err == nil {
		t.Errorf("pull 이 실패했는데 init 이 성공(exit 0)했다:\n%s", r.output())
	}
	for _, want := range []string{"[skip] app", "[fail] app", "[index] INDEX.md"} {
		if !strings.Contains(r.output(), want) {
			t.Errorf("출력에 %q 가 없다:\n%s", want, r.output())
		}
	}
}
