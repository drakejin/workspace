package ingest

import (
	"testing"
)

func TestIsIndexable(t *testing.T) {
	cases := map[string]bool{
		"docs/a.md": true, "docs/x/y.mdx": true, "main/svc/docs/a.md": true, "README.md": true, "CLAUDE.md": true, "GUIDE.md": true,
		"main/svc/.worktrees/feat/docs/a.md": false, "docs/feature/worktrees/a.md": true, ".cache/x/docs/a.md": false, "main/svc/src/a.md": false, "app/README.md": false,
		".claude/commands/x.md": false, "docs/a.txt": false, "docs/node_modules/a.md": false,
	}
	for p, want := range cases {
		if got := IsIndexable(p); got != want {
			t.Errorf("IsIndexable(%s) = %v", p, got)
		}
	}
}

func TestInScope(t *testing.T) {
	cases := map[string]bool{
		"": true, ".": true, "docs": true, "docs/a/b": true, "main": true, "main/svc": true, "main/svc/docs": true, "main/svc/docs/adr": true,
		"main/svc/src": false, "main/svc/.worktrees": false, "main/svc/node_modules": false, "app": false, "scripts": false, ".git": false,
	}
	for p, want := range cases {
		if got := InScope(p); got != want {
			t.Errorf("InScope(%s) = %v", p, got)
		}
	}
}

func TestResolveInsideRoot(t *testing.T) {
	abs, rel, err := ResolveInsideRoot("/meta", "docs/a.md")
	if err != nil || abs != "/meta/docs/a.md" || rel != "docs/a.md" {
		t.Errorf("= %s %s %v", abs, rel, err)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "docs/../../x"} {
		if _, _, err := ResolveInsideRoot("/meta", bad); err == nil {
			t.Errorf("%s 는 거부", bad)
		}
	}
}
