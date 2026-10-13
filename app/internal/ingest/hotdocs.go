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
	Supersedes   string
	SupersededBy string
	Deprecated   bool
	Summary      string
	Entities     []string
	Index        string
	// IndexVersion 은 이 문서를 쓴 필드 구성의 판이다 (model.IndexVersion 보다 낮으면 다시 쓴다)
	IndexVersion int
	// Duplicate 면 같은 문서가 두 인덱스 이상에 있다 (인덱스를 옮기다 끊긴 경우). 다시 쓰면 한 곳만 남는다.
	Duplicate bool
	// PendingCycle 이면 데몬 밖(ingest_path, audit -apply)에서 쓴 문서다 — 데몬이 아직 판정·증류·그래프로 넘기지 않았다.
	PendingCycle bool
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

// WorkspaceIndices 는 이 workspace 팀의 인덱스를 데몬이 맞추는 것(managed)과 restore_cold 로 복원해 열려 있는 것(restored,
// auto_manage=false — 데몬은 고치지 않지만 그 달의 hot 파일이 이미 거기 그대로 들어 있는지는 본다)으로 나눈다. 팀은 이 workspace 가
// 문서를 둔 팀과 기본 팀이다. 다른 workspace 의 팀 인덱스는 뺀다 — 그쪽 인덱스가 잠깐 열리지 않아도(샤드 503) 이 workspace 의
// 사이클이 실패하지 않게 (팀 하나는 workspace 하나에만 속한다).
func WorkspaceIndices(ctx context.Context, c opensearch.Client, workspace, defaultTeam string) (managed, restored []string, err error) {
	teams, err := opensearch.WorkspaceTeams(ctx, c, workspace)
	if err != nil {
		return nil, nil, err
	}
	settings, err := opensearch.Settings(ctx, c, naming.AllPattern)
	if err != nil {
		return nil, nil, err
	}
	managed, restored = []string{}, []string{}
	for index, s := range settings {
		idx, ok := naming.ParseIndex(index)
		if !ok || (!teams[idx.Team] && idx.Team != defaultTeam) {
			continue
		}
		if opensearch.IsUnmanaged(s) {
			restored = append(restored, index)
		} else {
			managed = append(managed, index)
		}
	}
	sort.Strings(managed)
	sort.Strings(restored)
	return managed, restored, nil
}

// FetchIndexedDocs 는 indices 에서 이 workspace 의 문서별 대표값이다 (건수 상한 없이 페이지로 읽는다).
func FetchIndexedDocs(ctx context.Context, c opensearch.Client, indices []string, workspace string) (map[string]IndexedDoc, error) {
	docs := map[string]IndexedDoc{}
	if len(indices) == 0 {
		return docs, nil
	}
	// IndexedDoc 에 담는 필드만 읽는다 — 데몬이 사이클마다 모든 문서에 대해 읽는다
	source := []string{"team", "repo", "index_version", "title", "source_path", "doc_type", "memory_kind", "content_sha", "body_sha",
		"mtime_ms", "size", "supersedes", "superseded_by", "deprecated", "summary", "entities", "pending_cycle"}
	err := opensearch.Scan(ctx, c, joinIndices(indices), opensearch.FirstChunks(workspace), source, opensearch.ChunkSort,
		func(h opensearch.Hit[model.Chunk]) error {
			s := h.Source
			if prev, seen := docs[s.SourcePath]; seen && prev.Index != h.Index {
				prev.Duplicate = true
				docs[s.SourcePath] = prev
				return nil
			}
			docs[s.SourcePath] = IndexedDoc{
				SourcePath: s.SourcePath, Team: s.Team, Repo: s.Repo, IndexVersion: s.IndexVersion, Title: s.Title, DocType: s.DocType, Kind: s.Kind,
				ContentSHA: s.ContentSHA, BodySHA: s.BodySHA, MtimeMs: s.MtimeMs, Size: s.Size,
				Supersedes: s.Supersedes, SupersededBy: s.SupersededBy, Deprecated: s.Deprecated, Summary: s.Summary,
				Entities: s.Entities, Index: h.Index, PendingCycle: s.PendingCycle,
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
