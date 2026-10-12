package ingest

import (
	"reflect"
	"testing"

	"github.com/drakejin/workspace/internal/model"
)

func TestIsIndexable(t *testing.T) {
	cases := map[string]bool{
		"docs/a.md": true, "docs/x/y.mdx": true, "main/svc/docs/a.md": true, "README.md": true, "CLAUDE.md": true, "GUIDE.md": true,
		"main/svc/.worktrees/feat/docs/a.md": false, "main/svc/src/a.md": false, "app/README.md": false,
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

func TestJSONLRoundTrip(t *testing.T) {
	chunks := []model.Chunk{{ID: "a", Team: "t", SourcePath: "docs/a.md", ChunkIndex: 0}, {ID: "b", Team: "t", SourcePath: "docs/a.md", ChunkIndex: 1}}
	got, bad := DecodeJSONL(EncodeJSONL(chunks) + "{broken\n\n")
	if !reflect.DeepEqual(got, chunks) || !reflect.DeepEqual(bad, []int{3}) {
		t.Errorf("got=%+v bad=%v", got, bad)
	}
	if f := DocFilter("ws", "docs/a.md"); len(f) != 2 {
		t.Error("DocFilter 는 workspace + source_path")
	}
}
