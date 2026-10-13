package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// API-11: META_REPO_ROOT 와 훅이 준 cwd 중 한쪽만 심볼릭 링크 경로여도(macOS 의 /var → /private/var) 같은 repo 다.
// 예전에는 경로 글자만 비교해 repo 를 놓쳤고, repo 필터와 브랜치 맥락이 빠졌다.
func TestRepoOfResolvesSymlinksOnBothSides(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	for _, dir := range []string{"src", filepath.Join(".worktrees", "fix-1", "src")} { // 훅의 cwd 는 있는 디렉토리다
		if err := os.MkdirAll(filepath.Join(real, "main", "svc", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, root, cwd, dir string
	}{
		{"root 는 링크, cwd 는 실제 경로", link, filepath.Join(real, "main", "svc", "src"), filepath.Join(link, "main", "svc")},
		{"root 는 실제 경로, cwd 는 링크", real, filepath.Join(link, "main", "svc"), filepath.Join(real, "main", "svc")},
		{"worktree 도", link, filepath.Join(real, "main", "svc", ".worktrees", "fix-1", "src"), filepath.Join(link, "main", "svc", ".worktrees", "fix-1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, dir := repoOf(c.root, c.cwd)

			if repo != "svc" || dir != c.dir {
				t.Errorf("repoOf(%s, %s) = %q %q, want svc %q", c.root, c.cwd, repo, dir, c.dir)
			}
		})
	}
}

// R4-query-b-1: main/{repo} 가 meta-repo 밖의 checkout 을 가리키는 심볼릭 링크여도 그 repo 다 — cwd 가 링크 철자
// (root/main/linked-svc/…)든 링크를 푼 철자(밖의 checkout/…)든, root 가 링크 경로든 실제 경로든. 예전에는 양쪽 링크를 다
// 풀어 비교해, 링크 철자의 cwd 가 root 밖으로 풀려 repo 이름과 프로젝트 필터가 빠졌다.
func TestRepoOfFindsARepoLinkedFromOutside(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "meta")
	outside := filepath.Join(base, "checkouts", "linked-svc")
	for _, dir := range []string{filepath.Join(real, "main"), filepath.Join(outside, "src"), filepath.Join(outside, ".worktrees", "fix-2", "src")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(real, "main", "linked-svc")); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "meta-link")
	if err := os.Symlink(real, rootLink); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, root, cwd, dir string
	}{
		{"링크 철자의 cwd", real, filepath.Join(real, "main", "linked-svc", "src"), filepath.Join(real, "main", "linked-svc")},
		{"링크를 푼 철자의 cwd", real, filepath.Join(outside, "src"), filepath.Join(real, "main", "linked-svc")},
		{"root 는 링크, cwd 는 링크 철자", rootLink, filepath.Join(rootLink, "main", "linked-svc"), filepath.Join(rootLink, "main", "linked-svc")},
		{"root 는 실제 경로, cwd 는 root 링크로 들어온 링크 철자", real, filepath.Join(rootLink, "main", "linked-svc", "src"), filepath.Join(real, "main", "linked-svc")},
		{"밖의 checkout 의 worktree", real, filepath.Join(outside, ".worktrees", "fix-2", "src"), filepath.Join(real, "main", "linked-svc", ".worktrees", "fix-2")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, dir := repoOf(c.root, c.cwd)

			if repo != "linked-svc" || dir != c.dir {
				t.Errorf("repoOf(%s, %s) = %q %q, want linked-svc %q", c.root, c.cwd, repo, dir, c.dir)
			}
		})
	}
	if repo, _ := repoOf(real, filepath.Join(base, "checkouts")); repo != "" {
		t.Errorf("링크 대상의 부모는 repo 가 아니다: %q", repo)
	}
}
