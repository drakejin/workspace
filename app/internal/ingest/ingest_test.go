package ingest

import (
	"testing"
)

func TestIsIndexable(t *testing.T) {
	cases := map[string]bool{
		"docs/a.md": true, "docs/x/y.mdx": true, "main/svc/docs/a.md": true, "README.md": true, "CLAUDE.md": true, "GUIDE.md": true,
		"main/svc/.worktrees/feat/docs/a.md": false, "docs/feature/worktrees/a.md": true, ".cache/x/docs/a.md": false, "main/svc/src/a.md": false, "app/README.md": false,
		".claude/commands/x.md": false, "docs/a.txt": false, "docs/node_modules/a.md": false,
		// IDX-14: 코드 트리의 빌드 산출물 이름(build·dist·bin·coverage·vendor)은 색인 범위 안에서는 주제 디렉토리 이름일 수 있다
		"docs/feature/build/INDEX.md": true, "docs/specs/coverage/plan.md": true, "docs/issues/dist/a.md": true,
		"main/svc/docs/bin/x.md": true, "docs/feature/vendor/a.md": true, "main/svc/build/docs/a.md": false,
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
		"docs/feature/build": true, "docs/specs/coverage": true, "main/svc/docs/vendor": true, "main/svc/build": false, "docs/x/node_modules": false,
	}
	for p, want := range cases {
		if got := InScope(p); got != want {
			t.Errorf("InScope(%s) = %v", p, got)
		}
	}
}

func TestResolveInsideRoot(t *testing.T) {
	abs, rel, err := resolveInsideRoot("/meta", "docs/a.md")
	if err != nil || abs != "/meta/docs/a.md" || rel != "docs/a.md" {
		t.Errorf("= %s %s %v", abs, rel, err)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "docs/../../x"} {
		if _, _, err := resolveInsideRoot("/meta", bad); err == nil {
			t.Errorf("%s 는 거부", bad)
		}
	}
}
