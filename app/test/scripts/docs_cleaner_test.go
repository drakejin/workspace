package scripts

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// cleanerWorkspace 는 docs-cleaner 를 돌릴 meta-repo 다: docs/{issues,specs} 마다 하위 디렉토리 하나와 바로 아래 파일(INDEX.md, notes.md).
func cleanerWorkspace(t *testing.T) (*sandbox, string) {
	t.Helper()
	sb := newSandbox(t)
	root := filepath.Join(sb.dir, "meta")
	sb.copyTo(root, "scripts/docs-cleaner")
	for _, f := range []string{
		"docs/issues/INDEX.md", "docs/issues/notes.md", "docs/issues/ISSUE-1/a.md",
		"docs/specs/INDEX.md", "docs/specs/s1/x.md", "docs/specs/s1/deep/y.md",
	} {
		writeFile(t, filepath.Join(root, f), "x\n")
	}
	return sb, root
}

// FILE-3: docs-cleaner 는 docs/issues·docs/specs 의 하위 디렉토리만 지운다 — 바로 아래 파일(INDEX.md 등)은 남긴다 (README 와 같다).
func TestDocsCleanerRemovesSubdirectoriesOnly(t *testing.T) {
	sb, root := cleanerWorkspace(t)

	r := sb.mustBash(map[string]string{"CONFIRM": "yes"}, filepath.Join(root, "scripts", "docs-cleaner", "run.sh"))

	for _, gone := range []string{"docs/issues/ISSUE-1", "docs/specs/s1"} {
		if exists(filepath.Join(root, gone)) {
			t.Errorf("%s 가 남았다", gone)
		}
		if !strings.Contains(r.stdout, "[clean] "+gone) {
			t.Errorf("출력에 [clean] %s 가 없다:\n%s", gone, r.output())
		}
	}
	for _, kept := range []string{"docs/issues/INDEX.md", "docs/issues/notes.md", "docs/specs/INDEX.md"} {
		if !exists(filepath.Join(root, kept)) {
			t.Errorf("바로 아래 파일 %s 를 지웠다", kept)
		}
	}
	for _, want := range []string{"done: 1 issues cleaned", "done: 1 specs cleaned"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("출력에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// FILE-3: 확인을 묻기 전에, 지운 문서는 cold 로 가지 않고 메모리 색인에서도 빠진다고 알리고 오래된 문서는 tier 로 보내라고 권한다.
// 확인 없이(입력이 없으면) 아무것도 지우지 않는다.
func TestDocsCleanerWarnsDeletedDocsLeaveMemory(t *testing.T) {
	sb, root := cleanerWorkspace(t)

	r := sb.bash(nil, filepath.Join(root, "scripts", "docs-cleaner", "run.sh"))

	if r.err == nil || !strings.Contains(r.stdout, "[docs-cleaner] 취소") {
		t.Fatalf("확인 없이 진행했다: %v\n%s", r.err, r.output())
	}
	if !exists(filepath.Join(root, "docs", "issues", "ISSUE-1", "a.md")) {
		t.Errorf("취소했는데 지웠다")
	}
	for _, want := range []string{"cold", "색인", "scripts/memory/run.sh tier"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("확인 전 경고(stderr)에 %q 가 없다:\n%s", want, r.output())
		}
	}
}

// FILE-3: 두 디렉토리를 지우는 내부 스크립트는 하나다 (디렉토리 이름만 다른 복제를 두지 않는다).
func TestDocsCleanerHasOneCleanHelper(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join(repoRoot(t), "scripts", "docs-cleaner", "_*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range matches {
		names = append(names, filepath.Base(m))
	}
	if want := []string{"_clean_dir.sh"}; !reflect.DeepEqual(names, want) {
		t.Errorf("docs-cleaner 내부 스크립트 = %v, want %v", names, want)
	}
}
