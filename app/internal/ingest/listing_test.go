package ingest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countReadDir 는 테스트 동안 디렉토리 나열 횟수를 센다.
func countReadDir(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	orig := readDir
	readDir = func(dir string) ([]os.DirEntry, error) {
		n.Add(1)
		return orig(dir)
	}
	t.Cleanup(func() { readDir = orig })
	return &n
}

// ageDirs 는 root 아래 디렉토리의 mtime 을 과거로 돌린다 — 방금 바뀐 디렉토리의 나열은 믿지 않으므로(같은 mtime 틱 안의 변경),
// 픽스처를 만든 직후의 디렉토리도 오래된 디렉토리처럼 보이게 한다.
func ageDirs(t *testing.T, root string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chtimes(p, past, past)
	})
	if err != nil {
		t.Fatal(err)
	}
}

const manyFiles = 30

func writeMany(t *testing.T, root string) []string {
	t.Helper()
	rels := make([]string, 0, manyFiles)
	for i := range manyFiles {
		rel := fmt.Sprintf("docs/knowledge/many/f%02d.md", i)
		writeFile(t, root, rel, []byte(fmt.Sprintf("# 문서 %d\nManyToken%d\n", i, i)))
		rels = append(rels, rel)
	}
	ageDirs(t, root)
	return rels
}

// R1-paths-1: 철자 맞추기(ResolveOnDisk)는 경로 조각마다 디렉토리 전체를 읽는다 — 같은 디렉토리의 파일을 여럿 준비하면
// (데몬의 IndexVersion 재작성, 주간 감사, ingest_path 디렉토리) 디렉토리 크기의 제곱으로 느려진다. 바뀌지 않은 디렉토리의 나열은
// 다시 쓴다.
func TestPrepareListsAnUnchangedDirectoryOnce(t *testing.T) {
	root := t.TempDir()
	rels := writeMany(t, root)
	ix, _ := newUnitIndexer(t, root, unitMaxFileBytes)
	reads := countReadDir(t)
	for _, rel := range rels {
		if _, err := ix.Prepare(context.Background(), rel, ""); err != nil {
			t.Fatal(err)
		}
	}
	// 루트·docs·knowledge·many 를 한 번씩
	if got := reads.Load(); got > 4 {
		t.Errorf("파일 %d개를 준비하며 디렉토리를 %d번 읽었다", len(rels), got)
	}

	// 디렉토리가 바뀌면 다시 읽는다 — 새 철자의 파일도 찾는다
	writeFile(t, root, "docs/knowledge/many/New.md", []byte("# 새 문서\n"))
	p, err := ix.Prepare(context.Background(), "docs/knowledge/many/new.md", "")
	if err != nil || p.Rel != "docs/knowledge/many/New.md" {
		t.Errorf("바뀐 디렉토리를 다시 읽지 않았다: %q %v", p.Rel, err)
	}
}

// R1-paths-1: ingest_path 의 디렉토리 적재는 걸으며 얻은 디스크의 이름을 그대로 쓴다 — 파일마다 철자를 두 번(ingestFile·Prepare)
// 다시 맞추지 않는다.
func TestIngestPathDirectoryResolvesOnlyTheRoot(t *testing.T) {
	root := t.TempDir()
	writeMany(t, root)
	ix, _ := newUnitIndexer(t, root, unitMaxFileBytes)
	reads := countReadDir(t)
	results, err := ix.IngestPath(context.Background(), "docs/knowledge/many", "")
	if err != nil || len(results) != manyFiles {
		t.Fatalf("results = %d %v", len(results), err)
	}
	if got := reads.Load(); got > 4 {
		t.Errorf("파일 %d개를 적재하며 철자를 맞추려 디렉토리를 %d번 읽었다", manyFiles, got)
	}
}
