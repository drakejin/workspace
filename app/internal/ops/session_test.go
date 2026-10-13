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
