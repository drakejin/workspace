package ingest

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
)

// RUN-7: ingest_path 도 데몬과 같은 게이트를 거친다 — NUL 바이트가 든 .md 와 MEMORY_MAX_FILE_BYTES 를 넘는 파일은
// 색인하지 않는다 (예전에는 바이너리를 영구히 색인에 남기고, 큰 파일은 "적재했다" 고 한 뒤 다음 사이클에 데몬이 지웠다).
func TestIngestPathAppliesTheDaemonGates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const maxBytes = 64
	writeFile(t, root, "docs/knowledge/probe/bin.md", []byte("\x00\x01\x02BinaryProbeToken\x00stuff"))
	writeFile(t, root, "docs/knowledge/probe/big.md", append([]byte("# 큰 문서\n"), bytes.Repeat([]byte("BigDocToken "), maxBytes)...))
	writeFile(t, root, "docs/knowledge/probe/ok.md", []byte("# 보통\nOkToken"))
	ix, store := newUnitIndexer(t, root, maxBytes)

	results, err := ix.IngestPath(ctx, "docs/knowledge/probe", "")
	if err != nil {
		t.Fatal(err)
	}
	indexed := 0
	for _, r := range results {
		if r.Chunks > 0 {
			indexed++
		}
	}
	if indexed != 1 {
		t.Errorf("색인한 문서는 ok.md 하나: %+v", results)
	}
	gated := map[string]string{"docs/knowledge/probe/bin.md": "binary content", "docs/knowledge/probe/big.md": "size "}
	for rel, reason := range gated {
		if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, rel)); ok {
			t.Errorf("%s 를 cold store 에 썼다 (게이트에 걸려야 한다)", rel)
		}
		res, err := ix.IngestFile(ctx, rel, "")
		if err != nil || res == nil || res.Chunks != 0 || res.SourcePath != rel || !strings.HasPrefix(res.Skipped, reason) {
			t.Errorf("IngestFile %s = %+v %v — 색인하지 않고 건너뛴 사유(%s)를 알린다", rel, res, err, reason)
		}
		var skip *SkipError
		if _, err := ix.Prepare(ctx, rel, ""); !errors.As(err, &skip) || skip.Rel != rel {
			t.Errorf("Prepare %s = %v — 데몬도 같은 게이트로 건너뛴다", rel, err)
		}
	}
	skipped := 0
	for _, r := range results {
		if r.Skipped != "" {
			skipped++
		}
	}
	if skipped != 2 || len(results) != 3 {
		t.Errorf("디렉토리 적재는 건너뛴 파일도 결과에 남긴다: %+v", results)
	}
}

func TestGate(t *testing.T) {
	root := t.TempDir()
	text := writeFile(t, root, "a.md", []byte("# 텍스트"))
	bin := writeFile(t, root, "b.md", []byte("앞\x00뒤"))
	cases := []struct {
		abs     string
		size    int64
		max     int64
		want    string
		wantErr bool
	}{
		{text, 10, 100, "", false},
		{text, 101, 100, "size 101 > 100", false},
		{bin, 5, 100, "binary content", false},
		{bin, 500, 100, "size 500 > 100", false}, // 크기를 먼저 본다 — 큰 파일은 읽지 않는다
		{filepath.Join(root, "none.md"), 1, 100, "", true},
	}
	for _, c := range cases {
		got, err := Gate(c.abs, c.size, c.max)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("Gate(%s, %d, %d) = %q %v", filepath.Base(c.abs), c.size, c.max, got, err)
		}
	}
}
