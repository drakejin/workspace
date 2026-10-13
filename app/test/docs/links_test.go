package docs

import (
	"path"
	"sort"
	"testing"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
)

// humanGuides 는 README 에서 링크를 따라 닿아야 하는 사람용 문서다 (README 의 문서 지도).
var humanGuides = []string{
	"GUIDE.md", "CLAUDE.md", "INDEX.md",
	"scenarios/SCENARIOS.md", "examples/sample-workspace/README.md", ".claude/commands/compaction.md",
	"docs/feature/INDEX.md", "docs/issues/INDEX.md", "docs/knowledge/INDEX.md", "docs/repository/INDEX.md", "docs/specs/INDEX.md",
	"docs/feature/memory-layer/INDEX.md", "docs/feature/memory-layer/talk-alignment.md",
}

// reachable 은 README.md 에서 [text](path) 링크만 따라 닿는 .md 문서다. 디렉토리 링크는 GitHub 처럼 그 README.md 를 연다.
func reachable(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	seen := map[string]bool{"README.md": true}
	queue := []string{"README.md"}
	for len(queue) > 0 {
		doc := queue[0]
		queue = queue[1:]
		for _, raw := range links(readDoc(t, doc)) {
			tg := resolve(doc, raw)
			if tg.external || tg.outside {
				continue
			}
			p := tg.path
			if isDir, ok := existsExact(root, p); !ok {
				continue
			} else if isDir {
				p = path.Join(p, "README.md")
				if _, ok := existsExact(root, p); !ok {
					continue
				}
			}
			if path.Ext(p) == ".md" && !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// DOC-3 · DOC-4 · DOC-5 · DOC-6 · FILE-10: 사람용 가이드는 모두 README 에서 링크로 닿는다 (코드 블록·백틱 표기는 링크가 아니다).
func TestGuidesAreReachableFromReadme(t *testing.T) {
	got := map[string]bool{}
	for _, p := range reachable(t) {
		got[p] = true
	}
	for _, want := range humanGuides {
		if !got[want] {
			t.Errorf("README 에서 링크로 닿지 않는다: %s", want)
		}
	}
}

// DOC-1 · DOC-3~6 · README:174: README 에서 닿는 문서의 링크는 모두 디스크에 그 철자대로 있고, 앵커(#…)는 대상 문서의
// 제목에서 나온 GitHub 앵커다 — 한글 제목도 GitHub 규칙(소문자, 구두점 제거, 공백→'-')으로 맞춘다.
func TestLinksAndAnchorsResolve(t *testing.T) {
	root := repoRoot(t)
	for _, doc := range reachable(t) {
		for _, raw := range links(readDoc(t, doc)) {
			tg := resolve(doc, raw)
			if tg.external {
				continue
			}
			if tg.outside {
				t.Errorf("%s: %s 는 레포 밖을 가리킨다", doc, raw)
				continue
			}
			isDir, ok := existsExact(root, tg.path)
			if !ok {
				t.Errorf("%s: 깨진 링크 %s (%s 가 없다)", doc, raw, tg.path)
				continue
			}
			if tg.anchor == "" {
				continue
			}
			if isDir || path.Ext(tg.path) != ".md" {
				t.Errorf("%s: %s 는 마크다운이 아닌 곳에 앵커를 건다", doc, raw)
				continue
			}
			if !anchors(readDoc(t, tg.path))[tg.anchor] {
				t.Errorf("%s: 깨진 앵커 %s (%s 에 #%s 제목이 없다)", doc, raw, tg.path, tg.anchor)
			}
		}
	}
}

// DOC-2 · FILE-1: README 에서 닿는 색인 문서는 노화하지 않는다(knowledge) — episodic 이면 60일 뒤 tier 가 git rm 해
// README·GUIDE 의 링크가 깨진다. 설계 배경처럼 낡지 않는 문서는 doc_type: guide 로 둔다.
func TestReadmeLinkedDocsDoNotAge(t *testing.T) {
	for _, rel := range reachable(t) {
		if !ingest.IsIndexable(rel) {
			continue
		}
		fm, _ := markdown.SplitFrontmatter(readDoc(t, rel))
		meta, err := document.ResolveMetadata(fm, rel, "default")
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if meta.Kind != model.KindKnowledge {
			t.Errorf("%s 는 %s(%s) — README 에서 닿는 문서가 60일 뒤 cold 로 간다", rel, meta.Kind, meta.DocType)
		}
	}
}
