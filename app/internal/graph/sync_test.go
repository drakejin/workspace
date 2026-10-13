package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
)

// R1-paths-5: 그래프는 문서(source_path, NFC)의 파일을 디스크의 철자로 연다 — Finder 가 만든 NFD 이름의 파일도 링크를 읽는다
// (정규화를 구분하는 파일 시스템에서는 NFC 경로로 열면 없어, 링크 없는 노드만 남는다).
func TestFromHotReadsLinksOfAnNFDNamedFile(t *testing.T) {
	root := t.TempDir()
	rel := "docs/specs/결제/plan.md"
	abs := filepath.Join(root, filepath.FromSlash(norm.NFD.String(rel)))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# plan\n[근거](./spec.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := fromHot(root, "ws", map[string]ingest.IndexedDoc{rel: {SourcePath: rel, Team: "t"}})
	for _, e := range g.Edges {
		if e.Type == string(model.LinkLinksTo) && strings.HasSuffix(e.To, "docs/specs/결제/spec.md") {
			return
		}
	}
	t.Errorf("NFD 이름의 파일에서 링크를 읽어야 한다: %+v", g.Edges)
}
