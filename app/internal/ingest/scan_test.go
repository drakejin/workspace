package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanHot(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	write("README.md", "# r")
	write("docs/a/x.md", "# x")
	write("docs/a/big.md", string(make([]byte, 200)))
	write("main/svc/docs/y.md", "# y")
	write("docs/feature/build/b.md", "# 주제 디렉토리 이름이 build")
	write("main/svc/src/z.md", "# no")
	write("main/svc/.worktrees/feat/docs/w.md", "# no")
	write(".cache/judge/x.md", "# no")
	write("app/README.md", "# no")
	files, skipped, err := ScanHot(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
	}
	if !reflect.DeepEqual(rels, []string{"README.md", "docs/a/x.md", "docs/feature/build/b.md", "main/svc/docs/y.md"}) || files[0].Size != 3 {
		t.Errorf("files = %v", rels)
	}
	if len(skipped) != 1 || skipped[0].Rel != "docs/a/big.md" {
		t.Errorf("skipped = %v", skipped)
	}
	if bin, _ := isProbablyBinary(filepath.Join(root, "docs/a/big.md")); !bin {
		t.Error("NUL 바이트면 바이너리")
	}
	if bin, _ := isProbablyBinary(filepath.Join(root, "README.md")); bin {
		t.Error("텍스트")
	}
	if _, _, err := ScanHot(filepath.Join(root, "missing"), 100); err == nil {
		t.Error("루트가 없으면 실패")
	}
	locked := filepath.Join(root, "docs/a")
	_ = os.Chmod(locked, 0o000)
	defer os.Chmod(locked, 0o755)
	files, skipped, err = ScanHot(root, 100)
	if !errors.Is(err, ErrPartialScan) || len(files) != 3 || skipped[len(skipped)-1].Rel != "docs/a" {
		t.Errorf("partial = %v files=%d skipped=%v", err, len(files), skipped)
	}
}
