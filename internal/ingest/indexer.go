package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/document"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Result 는 문서 하나를 적재한 결과다.
type Result struct {
	SourcePath string
	Team       string
	Index      string
	Chunks     int
	DocType    model.DocType
	Kind       model.Kind
	BodySHA    string
	ContentSHA string
	EventTime  time.Time
	Title      string
	Body       string
	Supersedes string
}

// Prepared 는 쓰기 전에 읽어 둔 문서다. 데몬이 cold 소유 여부를 판단할 때 쓴다.
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
	// RemoveFile 은 문서를 색인과 청크 JSONL 에서 뺀다. indices 가 비면 모든 메모리 인덱스에서.
	RemoveFile(ctx context.Context, input string, indices []string) (int, error)
	BulkChunks(ctx context.Context, chunks []model.Chunk) (int, error)
}

// DistillLookup 은 증류 캐시 조회다 (데몬이 꽂는다).
type DistillLookup func(team, bodySHA string) (model.Distilled, bool)

// Deps 는 Indexer 의존이다.
type Deps struct {
	OS        opensearch.Client
	Cold      cold.Store
	Config    config.Config
	Now       func() time.Time
	Distilled DistillLookup
}

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

// ErrTeamOwned 는 다른 workspace 가 이미 쓰는 팀이라는 뜻이다.
var ErrTeamOwned = errors.New("다른 workspace 가 쓰는 팀입니다")

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
		return err
	}
	if b := out.Aggregations.Others.Buckets; len(b) > 0 {
		return fmt.Errorf("%w: team %q 는 workspace %q 가 쓰고 있습니다 — 이 meta-repo 에는 다른 팀 이름을 쓰세요 (MEMORY_DEFAULT_TEAM 또는 frontmatter team)", ErrTeamOwned, team, b[0].Key)
	}
	ix.mu.Lock()
	ix.owned[team] = true
	ix.mu.Unlock()
	return nil
}

func (ix *indexer) Prepare(ctx context.Context, input, teamOverride string) (Prepared, error) {
	abs, rel, err := ResolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return Prepared{}, err
	}
	if !IsIndexable(rel) {
		return Prepared{}, fmt.Errorf("색인 대상이 아닙니다: %s", rel)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return Prepared{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Prepared{}, err
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
	return Prepared{Abs: abs, Rel: rel, Content: string(content), Built: built}, nil
}

// Write 는 새 청크를 먼저 쓰고(id 가 결정적이라 덮어쓰기) 남은 낡은 청크만 지운다.
// _bulk 가 실패해도 이전 내용은 검색에 남고, 검색이 0건이 되는 틈도 없다.
func (ix *indexer) Write(ctx context.Context, p Prepared) (Result, error) {
	b := p.Built
	if _, err := ix.BulkChunks(ctx, b.Chunks); err != nil {
		return Result{}, err
	}
	keep := make([]string, 0, len(b.Chunks))
	for _, c := range b.Chunks {
		keep = append(keep, c.ID)
	}
	if _, err := ix.deleteBySource(ctx, p.Rel, keep, nil); err != nil {
		return Result{}, err
	}
	if err := ix.Cold.Put(ctx, cold.RawKey(b.Meta.Team, p.Rel), p.Content); err != nil {
		return Result{}, fmt.Errorf("cold store raw 저장 실패: %w", err)
	}
	if err := ix.Cold.Put(ctx, cold.ChunksKey(b.Meta.Team, p.Rel), EncodeJSONL(b.Chunks)); err != nil {
		return Result{}, fmt.Errorf("cold store 청크 저장 실패: %w", err)
	}
	return Result{
		SourcePath: p.Rel,
		Team:       b.Meta.Team,
		Index:      naming.IndexFor(b.Meta.Team, b.Meta.Kind, b.EventTime),
		Chunks:     len(b.Chunks),
		DocType:    b.Meta.DocType,
		Kind:       b.Meta.Kind,
		BodySHA:    b.BodySHA,
		ContentSHA: b.ContentSHA,
		EventTime:  b.EventTime,
		Title:      b.Title,
		Body:       b.Body,
		Supersedes: b.Meta.Supersedes,
	}, nil
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
		return nil, err
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
		if IsIndexable(rel) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
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
	deleted, err := ix.deleteBySource(ctx, rel, nil, indices)
	if err != nil {
		return 0, err
	}
	for _, team := range teams {
		if err := ix.Cold.Remove(ctx, cold.ChunksKey(team, rel)); err != nil {
			return deleted, fmt.Errorf("cold store 청크 삭제 실패: %w", err)
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
		idx := naming.IndexFor(c.Team, c.Kind, c.EventTimeValue())
		if _, ok := byIndex[idx]; !ok {
			order = append(order, idx)
		}
		byIndex[idx] = append(byIndex[idx], c)
	}
	lines := make([]any, 0, len(chunks)*2)
	for _, idx := range order {
		group := byIndex[idx]
		if err := ix.ensureTeamOwner(ctx, group[0].Team); err != nil {
			return 0, err
		}
		if err := opensearch.EnsureIndex(ctx, ix.OS, idx, group[0].Team); err != nil {
			return 0, err
		}
		for _, c := range group {
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

// deleteBySource 는 source_path 의 청크를 지운다. keep 의 id 는 남긴다. indices 가 비면 모든 메모리 인덱스.
func (ix *indexer) deleteBySource(ctx context.Context, rel string, keep, indices []string) (int, error) {
	target := naming.AllPattern
	if indices != nil {
		if len(indices) == 0 {
			return 0, nil
		}
		target = strings.Join(indices, ",")
	}
	query := map[string]any{"filter": DocFilter(ix.Config.Workspace, rel)}
	if len(keep) > 0 {
		query["must_not"] = []any{map[string]any{"ids": map[string]any{"values": keep}}}
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	err := ix.OS.Do(ctx, http.MethodPost, "/"+target+"/_delete_by_query", map[string]any{"query": map[string]any{"bool": query}},
		opensearch.With(opensearch.Lenient(), "refresh", "true", "conflicts", "proceed"), &out)
	return out.Deleted, err
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
		"query": map[string]any{"bool": map[string]any{"filter": DocFilter(ix.Config.Workspace, rel)}},
		"aggs":  map[string]any{"teams": map[string]any{"terms": map[string]any{"field": "team", "size": 100}}},
	}
	if err := ix.OS.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return nil, err
	}
	teams := make([]string, 0, len(out.Aggregations.Teams.Buckets))
	for _, b := range out.Aggregations.Teams.Buckets {
		teams = append(teams, b.Key)
	}
	return teams, nil
}

// DocFilter 는 workspace 안의 문서 하나를 가리키는 필터다.
func DocFilter(workspace, rel string) []any {
	return []any{
		map[string]any{"term": map[string]any{"workspace": workspace}},
		map[string]any{"term": map[string]any{"source_path": rel}},
	}
}

// EncodeJSONL 은 청크를 JSONL 로 쓴다 (id 포함 — 재적재 때 같은 _id 로 덮어쓴다).
func EncodeJSONL(chunks []model.Chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		line, _ := json.Marshal(struct {
			ID string `json:"id"`
			model.Chunk
		}{c.ID, c})
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// DecodeJSONL 은 청크 JSONL 을 읽는다. 깨진 줄은 건너뛰고 줄 번호(1부터)를 돌려준다.
func DecodeJSONL(body string) ([]model.Chunk, []int) {
	var (
		chunks []model.Chunk
		bad    []int
	)
	for i, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			ID string `json:"id"`
			model.Chunk
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			bad = append(bad, i+1)
			continue
		}
		row.Chunk.ID = row.ID
		chunks = append(chunks, row.Chunk)
	}
	return chunks, bad
}
