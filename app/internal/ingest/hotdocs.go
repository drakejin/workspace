package ingest

import (
	"context"
	"sort"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// IndexedDoc 은 색인에 있는 문서 하나의 대표값(chunk_index 0)이다.
type IndexedDoc struct {
	SourcePath   string
	Team         string
	Repo         string
	Title        string
	DocType      model.DocType
	Kind         model.Kind
	ContentSHA   string
	BodySHA      string
	MtimeMs      int64
	Size         int64
	EventTime    string
	Supersedes   string
	SupersededBy string
	Deprecated   bool
	Summary      string
	Entities     []string
	Index        string
	// Duplicate 면 같은 문서가 두 인덱스 이상에 있다 (인덱스를 옮기다 끊긴 경우). 다시 쓰면 한 곳만 남는다.
	Duplicate bool
}

// ManagedIndices 는 데몬이 맞추는 hot 인덱스다. restore_cold 로 복원한 인덱스(auto_manage=false)는 뺀다 —
// 데몬은 cold 를 건드리지 않는다.
func ManagedIndices(ctx context.Context, c opensearch.Client) ([]string, error) {
	settings, err := opensearch.Settings(ctx, c, naming.AllPattern)
	if err != nil {
		return nil, err
	}
	indices := []string{}
	for index, s := range settings {
		if _, ok := naming.ParseIndex(index); ok && !opensearch.IsUnmanaged(s) {
			indices = append(indices, index)
		}
	}
	sort.Strings(indices)
	return indices, nil
}

// RestoredIndices 는 restore_cold 로 복원해 열려 있는 월별 인덱스다 (auto_manage=false). 데몬은 고치지 않지만,
// 그 달의 hot 파일이 이미 거기 그대로 들어 있는지는 본다.
func RestoredIndices(ctx context.Context, c opensearch.Client) ([]string, error) {
	settings, err := opensearch.Settings(ctx, c, naming.AllPattern)
	if err != nil {
		return nil, err
	}
	indices := []string{}
	for index, s := range settings {
		if _, ok := naming.ParseIndex(index); ok && opensearch.IsUnmanaged(s) {
			indices = append(indices, index)
		}
	}
	sort.Strings(indices)
	return indices, nil
}

// FetchIndexedDocs 는 indices 에서 이 workspace 의 문서별 대표값이다 (건수 상한 없이 페이지로 읽는다).
func FetchIndexedDocs(ctx context.Context, c opensearch.Client, indices []string, workspace string) (map[string]IndexedDoc, error) {
	docs := map[string]IndexedDoc{}
	if len(indices) == 0 {
		return docs, nil
	}
	source := []string{"workspace", "team", "repo", "title", "source_path", "chunk_index", "doc_type", "memory_kind", "content_sha", "body_sha",
		"mtime_ms", "size", "event_time", "supersedes", "superseded_by", "deprecated", "summary", "entities"}
	err := opensearch.Scan(ctx, c, joinIndices(indices), opensearch.FirstChunks(workspace), source, opensearch.ChunkSort,
		func(h opensearch.Hit[model.Chunk]) error {
			s := h.Source
			if prev, seen := docs[s.SourcePath]; seen && prev.Index != h.Index {
				prev.Duplicate = true
				docs[s.SourcePath] = prev
				return nil
			}
			docs[s.SourcePath] = IndexedDoc{
				SourcePath: s.SourcePath, Team: s.Team, Repo: s.Repo, Title: s.Title, DocType: s.DocType, Kind: s.Kind,
				ContentSHA: s.ContentSHA, BodySHA: s.BodySHA, MtimeMs: s.MtimeMs, Size: s.Size, EventTime: s.EventTime,
				Supersedes: s.Supersedes, SupersededBy: s.SupersededBy, Deprecated: s.Deprecated, Summary: s.Summary,
				Entities: s.Entities, Index: h.Index,
			}
			return nil
		})
	return docs, err
}

// DocChunks 는 indices 에서 문서 하나의 청크 전부다.
func DocChunks(ctx context.Context, c opensearch.Client, indices []string, workspace, sourcePath string) ([]opensearch.Hit[model.Chunk], error) {
	var hits []opensearch.Hit[model.Chunk]
	query := map[string]any{"bool": map[string]any{"filter": opensearch.DocFilter(workspace, sourcePath)}}
	err := opensearch.Scan(ctx, c, joinIndices(indices), query, nil, opensearch.ChunkSort, func(h opensearch.Hit[model.Chunk]) error {
		hits = append(hits, h)
		return nil
	})
	return hits, err
}

func joinIndices(indices []string) string {
	out := ""
	for i, idx := range indices {
		if i > 0 {
			out += ","
		}
		out += idx
	}
	return out
}
