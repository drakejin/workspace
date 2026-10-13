package ingest

import "testing"

// DOC-1: 링크 대상 확인 — 색인 범위 밖 파일도 있으면 있다. NFD 이름은 NFC 로 친 철자로도 있다(같은 글자),
// 대소문자만 다른 철자와 meta-repo 밖은 없다.
func TestExistsAsSpelled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "scenarios/SCENARIOS.md", []byte("# 시나리오"))
	writeFile(t, root, nfdRel, []byte("# 환불 메모"))
	cases := map[string]bool{
		"scenarios/SCENARIOS.md": true,
		nfcRel:                   true,
		"scenarios/missing.md":   false,
		"scenarios/scenarios.md": false,
		"../outside.md":          false,
	}
	for rel, want := range cases {
		if got := ExistsAsSpelled(root, rel); got != want {
			t.Errorf("ExistsAsSpelled(%q) = %v, want %v", rel, got, want)
		}
	}
}
