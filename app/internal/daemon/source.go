package daemon

import (
	"os"
	"path/filepath"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/markdown"
)

// sourceFile 은 문서(source_path)의 디스크 경로다. source_path 는 NFC 라, 정규화를 구분하는 파일 시스템(Linux)에서는
// NFD 이름(Finder 가 만든 한글 이름)의 파일을 그대로 열 수 없다 — 디스크의 철자로 푼다.
func sourceFile(root, rel string) string {
	abs, _, err := ingest.ResolveOnDisk(root, rel)
	if err != nil {
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	return abs
}

// bodyAt 은 디스크에 있는 문서의 지금 본문(frontmatter 제외)이다 — 그 sha 가 bodySHA 와 같을 때만. 색인·대기 목록이 기억하는
// sha 와 다른(그 사이 고친) 본문을 그 sha 의 것으로 섞지 않는다 (고친 본문은 사이클이 새 변경분으로 넘긴다).
func bodyAt(root, rel, bodySHA string) (string, bool) {
	b, err := os.ReadFile(sourceFile(root, rel))
	if err != nil {
		return "", false
	}
	_, body := markdown.SplitFrontmatter(string(b))
	if document.SHA256(body) != bodySHA {
		return "", false
	}
	return body, true
}
