package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// Result 는 문서 하나를 적재한 결과다.
type Result struct {
	SourcePath string
	Index      string
	Chunks     int
}

// Prepared 는 쓰기 전에 읽어 둔 문서다. 데몬이 변경·cold 소유 여부를 판단할 때 쓴다.
type Prepared struct {
	Abs     string
	Rel     string
	Content string
	Built   document.Built
}

// Indexer 는 문서 파일을 색인한다.
type Indexer interface {
	Prepare(ctx context.Context, input, teamOverride string) (Prepared, error)
	Write(ctx context.Context, p Prepared) (Result, error)
	IngestFile(ctx context.Context, input, teamOverride string) (*Result, error)
	IngestPath(ctx context.Context, input, teamOverride string) ([]Result, error)
	// RemoveFile 은 문서를 색인과 청크 JSONL 에서 뺀다. indices 가 nil 이면 모든 메모리 인덱스에서.
	RemoveFile(ctx context.Context, input string, indices []string) (int, error)
	BulkChunks(ctx context.Context, chunks []model.Chunk) (int, error)
}

// Deps 는 Indexer 의존이다.
type Deps struct {
	OS     opensearch.Client
	Cold   cold.Store
	Config config.Config
	Now    func() time.Time
	// Distilled 는 증류 캐시 조회다 (데몬이 꽂는다). hit 면 LLM 없이 summary/entities 가 붙는다.
	Distilled func(team, bodySHA string) (model.Distilled, bool)
	// SupersededBy 는 지금 알려진 superseded_by 다 (데몬이 꽂는다). 재색인해도 밀린 표시와 인덱스 위치가 유지된다.
	SupersededBy func(sourcePath string) string
}

// ErrTeamOwned 는 다른 workspace 가 이미 쓰는 팀이라는 뜻이다.
var ErrTeamOwned = errors.New("다른 workspace 가 쓰는 팀입니다")

// maxTeamsPerDoc 는 한 문서가 거쳐 간 팀 수 상한이다 (frontmatter team 을 바꿀 때마다 늘어난다).
const maxTeamsPerDoc = 100

type indexer struct {
	Deps
	mu    sync.Mutex
	owned map[string]bool // 이 workspace 가 쓰는 것으로 확인한 팀
}

// New 는 Indexer 다.
func New(d Deps) Indexer {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &indexer{Deps: d, owned: map[string]bool{}}
}

func (ix *indexer) Prepare(ctx context.Context, input, teamOverride string) (Prepared, error) {
	abs, rel, err := ResolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return Prepared{}, err
	}
	if !IsIndexable(rel) {
		return Prepared{}, fmt.Errorf("색인 대상이 아닙니다: %s", rel)
	}
	if err := RealInsideRoot(ix.Config.MetaRepoRoot, abs); err != nil {
		return Prepared{}, err
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return Prepared{}, fmt.Errorf("read %s: %w", rel, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Prepared{}, fmt.Errorf("stat %s: %w", rel, err)
	}
	git := document.LastCommit(ctx, abs)
	fallback := git.CommittedAt
	if fallback.IsZero() {
		fallback = info.ModTime()
	}
	built, err := document.Build(document.Input{
		Workspace:    ix.Config.Workspace,
		SourcePath:   rel,
		Content:      string(content),
		DefaultTeam:  ix.Config.DefaultTeam,
		FallbackTime: fallback,
		GitSHA:       git.SHA,
		Now:          ix.Now(),
		MtimeMs:      info.ModTime().UnixMilli(),
		Size:         info.Size(),
		TeamOverride: teamOverride,
		Distilled:    ix.Distilled,
	})
	if err != nil {
		return Prepared{}, fmt.Errorf("%s: %w", rel, err)
	}
	by, deprecated, err := ix.flagsOf(ctx, rel)
	if err != nil {
		return Prepared{}, err
	}
	for i := range built.Chunks {
		built.Chunks[i].SupersededBy, built.Chunks[i].Deprecated = by, deprecated
	}
	return Prepared{Abs: abs, Rel: rel, Content: string(content), Built: built}, nil
}

// flagsOf 는 다시 쓸 문서에 이어 붙일 superseded_by·deprecated 다. 데몬은 정본(선언·원장)에서 계산한 값을 꽂고(SupersededBy),
// 데몬 밖(ingest_path, audit -apply)에서는 지금 색인에 있는 값을 이어받는다 — 재색인이 밀린 문서를 기본 검색에 되살리지 않게.
func (ix *indexer) flagsOf(ctx context.Context, rel string) (string, bool, error) {
	if ix.SupersededBy != nil {
		return ix.SupersededBy(rel), false, nil
	}
	var out opensearch.SearchResponse[model.Chunk]
	body := map[string]any{
		"size":    1,
		"_source": []string{"superseded_by", "deprecated"},
		"query":   opensearch.FirstChunks(ix.Config.Workspace, map[string]any{"term": map[string]any{"source_path": rel}}),
	}
	if err := ix.OS.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return "", false, fmt.Errorf("flags %s: %w", rel, err)
	}
	if len(out.Hits.Hits) == 0 {
		return "", false, nil
	}
	h := out.Hits.Hits[0].Source
	return h.SupersededBy, h.Deprecated, nil
}

// Write 는 문서 하나를 색인과 cold store 에 쓴다. 청크 0 의 mtime·size·content_sha 가 데몬의 "최신" 표시이고,
// 순서가 곧 복구 규칙이다:
//  1. 청크 0 을 무효 표시(content_sha·mtime 없음)로 먼저 쓴다 — 여기서부터 어디서 끊겨도 이 문서는 "색인에 있는데 낡았다" 로 보여
//     다음 사이클이 처음부터 다시 쓴다 (cold 소유로 오인되거나, 섞인 청크가 최신으로 보이지 않는다)
//  2. 남길 것(대상 인덱스 안의 새 id)만 빼고 이 문서의 청크를 지운다 — 날짜·성질이 바뀌어 인덱스가 달라졌으면 옛 인덱스 쪽은 전부
//  3. 나머지 청크를 쓴다 (id 가 결정적이라 덮어쓰기)
//  4. cold store 에 원본과 청크 JSONL 을 쓴다
//  5. 청크 0 을 진짜 표시로 다시 쓴다
func (ix *indexer) Write(ctx context.Context, p Prepared) (Result, error) {
	b := p.Built
	index := naming.KnowledgeIndex(b.Meta.Team)
	if len(b.Chunks) > 0 {
		index = naming.IndexFor(b.Chunks[0])
	}
	if err := ix.ensureTeamOwner(ctx, b.Meta.Team); err != nil {
		return Result{}, err
	}
	if len(b.Chunks) > 0 {
		pending := b.Chunks[0]
		pending.ContentSHA, pending.MtimeMs, pending.Size = "", 0, 0
		if _, err := ix.BulkChunks(ctx, []model.Chunk{pending}); err != nil {
			return Result{}, fmt.Errorf("index %s: %w", p.Rel, err)
		}
	}
	keep := make([]string, 0, len(b.Chunks))
	for _, c := range b.Chunks {
		keep = append(keep, c.ID)
	}
	if _, err := opensearch.DeleteDocExcept(ctx, ix.OS, []string{naming.AllPattern}, ix.Config.Workspace, p.Rel, index, keep); err != nil {
		return Result{}, err
	}
	if len(b.Chunks) > 1 {
		if _, err := ix.BulkChunks(ctx, b.Chunks[1:]); err != nil {
			return Result{}, fmt.Errorf("index %s: %w", p.Rel, err)
		}
	}
	if err := ix.Cold.Put(ctx, cold.RawKey(b.Meta.Team, p.Rel), p.Content); err != nil {
		return Result{}, fmt.Errorf("cold store raw %s: %w", p.Rel, err)
	}
	if err := ix.Cold.Put(ctx, cold.ChunksKey(b.Meta.Team, p.Rel), cold.EncodeChunks(b.Chunks)); err != nil {
		return Result{}, fmt.Errorf("cold store chunks %s: %w", p.Rel, err)
	}
	if len(b.Chunks) > 0 {
		if _, err := ix.BulkChunks(ctx, b.Chunks[:1]); err != nil {
			return Result{}, fmt.Errorf("index %s: %w", p.Rel, err)
		}
	}
	return Result{SourcePath: p.Rel, Index: index, Chunks: len(b.Chunks)}, nil
}

func (ix *indexer) IngestFile(ctx context.Context, input, teamOverride string) (*Result, error) {
	_, rel, err := ResolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return nil, err
	}
	if !IsIndexable(rel) {
		return nil, nil
	}
	p, err := ix.Prepare(ctx, input, teamOverride)
	if err != nil {
		return nil, err
	}
	r, err := ix.Write(ctx, p)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (ix *indexer) IngestPath(ctx context.Context, input, teamOverride string) ([]Result, error) {
	abs, _, err := ResolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", input, err)
	}
	if !info.IsDir() {
		r, err := ix.IngestFile(ctx, abs, teamOverride)
		if err != nil || r == nil {
			return nil, err
		}
		return []Result{*r}, nil
	}
	var files []string
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(ix.Config.MetaRepoRoot, p)
		if d.IsDir() {
			if p != abs && IgnoredSegments[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && IsIndexable(rel) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", input, err)
	}
	sort.Strings(files)
	results := make([]Result, 0, len(files))
	for _, f := range files {
		r, err := ix.IngestFile(ctx, f, teamOverride)
		if err != nil {
			return results, err
		}
		if r != nil {
			results = append(results, *r)
		}
	}
	return results, nil
}

func (ix *indexer) RemoveFile(ctx context.Context, input string, indices []string) (int, error) {
	_, rel, err := ResolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return 0, err
	}
	if !IsIndexable(rel) {
		return 0, nil
	}
	teams, err := ix.teamsOf(ctx, rel)
	if err != nil {
		return 0, err
	}
	if indices == nil {
		indices = []string{naming.AllPattern}
	}
	deleted, err := opensearch.DeleteDoc(ctx, ix.OS, indices, ix.Config.Workspace, rel)
	if err != nil {
		return 0, err
	}
	for _, team := range teams {
		if err := ix.Cold.Remove(ctx, cold.ChunksKey(team, rel)); err != nil {
			return deleted, fmt.Errorf("cold store chunks %s: %w", rel, err)
		}
	}
	return deleted, nil
}

func (ix *indexer) BulkChunks(ctx context.Context, chunks []model.Chunk) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	byIndex := map[string][]model.Chunk{}
	var order []string
	for _, c := range chunks {
		idx := naming.IndexFor(c)
		if _, ok := byIndex[idx]; !ok {
			order = append(order, idx)
		}
		byIndex[idx] = append(byIndex[idx], c)
	}
	lines := make([]any, 0, len(chunks)*2)
	for _, idx := range order {
		team := byIndex[idx][0].Team
		if err := ix.ensureTeamOwner(ctx, team); err != nil {
			return 0, err
		}
		if err := opensearch.EnsureIndex(ctx, ix.OS, idx, team, ix.Config.OrgOf(team)); err != nil {
			return 0, fmt.Errorf("ensure index %s: %w", idx, err)
		}
		for _, c := range byIndex[idx] {
			lines = append(lines, map[string]any{"index": map[string]any{"_index": idx, "_id": c.ID}}, c)
		}
	}
	resp, err := ix.OS.Bulk(ctx, lines, url.Values{"refresh": {"true"}})
	if err != nil {
		return 0, err
	}
	if resp.Errors {
		return 0, fmt.Errorf("_bulk 일부 실패: %s", resp.FirstError())
	}
	return len(chunks), nil
}

// ensureTeamOwner 는 팀 하나가 workspace 하나에만 속하게 한다. 인덱스는 mem-{team}-{yyyy.MM} 이라
// tier·ISM·restore 가 인덱스 단위로 움직인다 — 두 meta-repo 가 같은 팀을 쓰면 한쪽의 tier 가 다른 쪽 문서를 지운다.
func (ix *indexer) ensureTeamOwner(ctx context.Context, team string) error {
	ix.mu.Lock()
	ok := ix.owned[team]
	ix.mu.Unlock()
	if ok {
		return nil
	}
	var out struct {
		Aggregations struct {
			Others struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"others"`
		} `json:"aggregations"`
	}
	body := map[string]any{
		"size": 0,
		"query": map[string]any{"bool": map[string]any{
			"filter":   []any{map[string]any{"term": map[string]any{"team": team}}},
			"must_not": []any{map[string]any{"term": map[string]any{"workspace": ix.Config.Workspace}}},
		}},
		"aggs": map[string]any{"others": map[string]any{"terms": map[string]any{"field": "workspace", "size": 1}}},
	}
	if err := ix.OS.Do(ctx, http.MethodPost, "/"+naming.TeamPattern(team)+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return fmt.Errorf("team owner %s: %w", team, err)
	}
	if b := out.Aggregations.Others.Buckets; len(b) > 0 {
		return fmt.Errorf("%w: team %q 는 workspace %q 가 쓰고 있습니다 — 이 meta-repo 에는 다른 팀 이름을 쓰세요 (MEMORY_DEFAULT_TEAM 또는 frontmatter team)", ErrTeamOwned, team, b[0].Key)
	}
	ix.mu.Lock()
	ix.owned[team] = true
	ix.mu.Unlock()
	return nil
}

func (ix *indexer) teamsOf(ctx context.Context, rel string) ([]string, error) {
	var out struct {
		Aggregations struct {
			Teams struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"teams"`
		} `json:"aggregations"`
	}
	body := map[string]any{
		"size":  0,
		"query": map[string]any{"bool": map[string]any{"filter": opensearch.DocFilter(ix.Config.Workspace, rel)}},
		"aggs":  map[string]any{"teams": map[string]any{"terms": map[string]any{"field": "team", "size": maxTeamsPerDoc}}},
	}
	if err := ix.OS.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return nil, fmt.Errorf("teams of %s: %w", rel, err)
	}
	teams := make([]string, 0, len(out.Aggregations.Teams.Buckets))
	for _, b := range out.Aggregations.Teams.Buckets {
		teams = append(teams, b.Key)
	}
	return teams, nil
}
