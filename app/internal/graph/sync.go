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
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.SourcePath))); err == nil {
			in.Links = document.Links(string(b), d.SourcePath)
		}
		inputs = append(inputs, in)
	}
	return Build(workspace, inputs)
}

// Rebuild 는 hot 문서로 workspace 그래프를 통째로 다시 만든다 (LLM 0회).
func Rebuild(ctx context.Context, s Store, root, workspace string, docs map[string]ingest.IndexedDoc) (Graph, error) {
	g := FromHot(root, workspace, docs)
	return g, s.Replace(ctx, g)
}
