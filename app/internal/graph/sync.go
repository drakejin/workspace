package graph

import (
	"context"
	"os"
	"path/filepath"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
)

// FromHot 은 지금 색인에 있는 문서(hot)와 그 파일의 링크로 그래프를 만든다.
// 파일을 읽지 못한 문서는 링크 없이 노드만 둔다.
func FromHot(root, workspace string, docs map[string]ingest.IndexedDoc) Graph {
	inputs := make([]Input, 0, len(docs))
	for _, d := range docs {
		in := Input{
			Path: d.SourcePath, Team: d.Team, Repo: d.Repo, Title: d.Title, DocType: string(d.DocType), Kind: string(d.Kind),
			Summary: d.Summary, Entities: d.Entities, SupersededBy: d.SupersededBy, Deprecated: d.Deprecated,
		}
		if b, err := os.ReadFile(onDiskPath(root, d.SourcePath)); err == nil {
			in.Links = document.Links(string(b), d.SourcePath)
		}
		inputs = append(inputs, in)
	}
	return Build(workspace, inputs, outOfScope(root, inputs))
}

// onDiskPath 는 문서(source_path, NFC)의 디스크 경로다 — 디스크의 철자로 푼다. 정규화를 구분하는 파일 시스템(Linux)에서는
// NFC 경로로 NFD 이름(Finder 가 만든 한글 이름)의 파일을 열 수 없다.
func onDiskPath(root, rel string) string {
	abs, _, err := ingest.ResolveOnDisk(root, rel)
	if err != nil {
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	return abs
}

// outOfScope 는 hot 이 아닌 링크 대상 가운데 색인 범위 밖이면서 디스크에 그 철자로 있는 것이다 (scenarios/·.claude/·examples/
// 의 가이드). 범위 안인데 hot 이 아닌 대상(cold 로 갔거나, 오타거나, 게이트에 걸렸다)은 그대로 unindexed 로 둔다.
func outOfScope(root string, inputs []Input) map[string]bool {
	hot := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		hot[in.Path] = true
	}
	checked := map[string]bool{}
	out := map[string]bool{}
	for _, in := range inputs {
		for _, l := range in.Links {
			if hot[l.To] || checked[l.To] || ingest.IsIndexable(l.To) {
				continue
			}
			checked[l.To] = true
			if ingest.ExistsAsSpelled(root, l.To) {
				out[l.To] = true
			}
		}
	}
	return out
}

// Rebuild 는 hot 문서로 workspace 그래프를 통째로 다시 만든다 (LLM 0회).
func Rebuild(ctx context.Context, s Store, root, workspace string, docs map[string]ingest.IndexedDoc) (Graph, error) {
	g := FromHot(root, workspace, docs)
	return g, s.Replace(ctx, g)
}
