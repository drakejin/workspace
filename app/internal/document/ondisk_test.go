package document

import (
	"os"
	"path/filepath"
	"testing"
)

// R4-daemon-b-6: 디스크의 철자 풀기는 한 벌이다 (ingest 의 ResolveOnDisk 와 cold 의 tier 가 함께 쓴다) — 세그먼트마다 그 철자
// 그대로 있으면 그것, 없으면 대소문자·유니코드 정규화만 다른 항목. 디스크에 없는 세그먼트부터는 입력 철자를 그대로 쓴다.
func TestOnDiskRelUsesTheSpellingOnDisk(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"docs/" + nfd("환불정책") + "/" + nfd("메모.md"), "docs/Specs/Upper.md"} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]string{
		"docs/환불정책/메모.md":       "docs/" + nfd("환불정책") + "/" + nfd("메모.md"),
		"docs/specs/upper.md":   "docs/Specs/Upper.md",
		"docs/Specs/Upper.md":   "docs/Specs/Upper.md",
		"docs/specs/missing.md": "docs/Specs/missing.md",
		"docs/nowhere/a/b.md":   "docs/nowhere/a/b.md",
		"":                      "",
	}
	for in, want := range cases {
		if got := OnDiskRel(root, in); got != want {
			t.Errorf("OnDiskRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchNamePrefersTheExactSpelling(t *testing.T) {
	names := []string{"Readme.md", "readme.md", nfd("각.md")}
	cases := []struct {
		name, want string
		found      bool
	}{
		{"readme.md", "readme.md", true},
		{"README.md", "Readme.md", true},
		{"각.md", nfd("각.md"), true},
		{"없음.md", "", false},
	}
	for _, c := range cases {
		if got, found := MatchName(names, c.name); got != c.want || found != c.found {
			t.Errorf("MatchName(%q) = %q %v, want %q %v", c.name, got, found, c.want, c.found)
		}
	}
}
