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
	"github.com/drakejin/workspace/app/internal/state"
)

// Result 는 문서 하나를 적재한 결과다.
type Result struct {
	SourcePath string
	Index      string
	Chunks     int
	// Skipped 는 게이트(바이너리·MEMORY_MAX_FILE_BYTES 초과)에 걸려 색인하지 않았을 때의 사유다. 색인했으면 "".
	Skipped string
}

// Prepared 는 쓰기 전에 읽어 둔 문서다. 데몬이 변경·cold 소유 여부를 판단할 때 쓴다.
type Prepared struct {
	Rel     string
	Content string
	Built   document.Built
	// PendingCycle 이면 Write 가 청크 0 에 넘김 표시(pending_cycle)를 남긴다 — 데몬 밖의 쓰기라 다음 데몬 사이클이
	// 판정·증류·그래프로 넘겨야 한다 (Prepare 가 정한다)
	PendingCycle bool
}

// Indexer 는 문서 파일을 색인한다.
type Indexer interface {
	Prepare(ctx context.Context, input, teamOverride string) (Prepared, error)
	Write(ctx context.Context, p Prepared) (Result, error)
	// IngestPath 는 파일 또는 디렉토리를 적재한다 (ingest_path·CLI ingest). 파일 하나도 이 길로 적재한다.
	IngestPath(ctx context.Context, input, teamOverride string) ([]Result, error)
	// RemoveFile 은 문서를 색인과 청크 JSONL 에서 뺀다. indices 가 nil 이면 모든 메모리 인덱스에서. input 은 색인에 있는
	// source_path 그대로 지운다 — 같은 파일이 디스크에 다른 철자로 있으면 청크 JSONL 은 남긴다 (LiveUnderOtherSpelling).
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
	// SupersededBy 는 지금 알려진 숨김 표시(superseded_by, deprecated)다 (데몬이 꽂는다). 재색인해도 밀린·deprecate 표시와
	// 인덱스 위치가 유지된다. 이것이 있으면 데몬 사이클 안의 쓰기다 — 판정·증류·그래프를 그 사이클이 이어서 하므로 넘김 표시를
	// 남기지 않는다.
	SupersededBy func(sourcePath string) (supersededBy string, deprecated bool)
}

// ErrTeamOwned 는 다른 workspace 가 이미 쓰는 팀이라는 뜻이다.
var ErrTeamOwned = errors.New("다른 workspace 가 쓰는 팀입니다")

// maxTeamsPerDoc 는 한 문서가 거쳐 간 팀 수 상한이다 (frontmatter team 을 바꿀 때마다 늘어난다).
const maxTeamsPerDoc = 100

type indexer struct {
	Deps
	mu       sync.Mutex
	owned    map[string]bool // 이 workspace 가 쓰는 것으로 확인한 팀
	listings *listingCache   // 철자 맞추기의 디렉토리 나열 (바뀌지 않은 디렉토리는 다시 읽지 않는다)
}

// New 는 Indexer 다.
func New(d Deps) Indexer {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &indexer{Deps: d, owned: map[string]bool{}, listings: newListingCache()}
}

// resolve 는 ResolveOnDisk 이되 바뀌지 않은 디렉토리의 나열을 다시 쓴다.
func (ix *indexer) resolve(input string) (abs, rel string, err error) {
	return resolveOnDisk(ix.Config.MetaRepoRoot, input, ix.listings.match)
}

// Prepare 는 파일 하나를 읽어 청크로 만든다. 입력 철자는 디스크의 철자로 바꾸고 NFC 로 맞춘 rel 이 문서의 정체다 (ResolveOnDisk).
// 게이트(바이너리·크기)에 걸리면 *SkipError 다.
func (ix *indexer) Prepare(ctx context.Context, input, teamOverride string) (Prepared, error) {
	abs, rel, err := ix.resolve(input)
	if err != nil {
		return Prepared{}, err
	}
	return ix.prepare(ctx, abs, rel, teamOverride)
}

// prepare 는 디스크의 철자로 맞춘 파일(abs)과 그 정체(rel)로 Prepare 한다.
func (ix *indexer) prepare(ctx context.Context, abs, rel, teamOverride string) (Prepared, error) {
	if !IsIndexable(rel) {
		return Prepared{}, fmt.Errorf("색인 대상이 아닙니다: %s", rel)
	}
	if err := realInsideRoot(ix.Config.MetaRepoRoot, abs); err != nil {
		return Prepared{}, err
	}
	content, info, err := ix.readGated(abs, rel)
	if err != nil {
		return Prepared{}, err
	}
	built, err := ix.build(ctx, abs, rel, teamOverride, string(content), info)
	if err != nil {
		return Prepared{}, err
	}
	kept, err := ix.carriedOf(ctx, rel, built.ContentSHA)
	if err != nil {
		return Prepared{}, err
	}
	built.Chunks = WithFlags(built.Chunks, kept.supersededBy, kept.deprecated)
	return Prepared{Rel: rel, Content: string(content), Built: built, PendingCycle: kept.pendingCycle}, nil
}

// build 는 읽은 내용을 청크로 만든다. 유효 시각이 frontmatter 에 없으면 마지막 커밋 시각, 그것도 없으면 mtime 이다.
func (ix *indexer) build(ctx context.Context, abs, rel, teamOverride, content string, info fs.FileInfo) (document.Built, error) {
	git := document.LastCommit(ctx, abs)
	fallback := git.CommittedAt
	if fallback.IsZero() {
		fallback = info.ModTime()
	}
	built, err := document.Build(document.Input{
		Workspace:    ix.Config.Workspace,
		SourcePath:   rel,
		Content:      content,
		DefaultTeam:  ix.Config.DefaultTeam,
		FallbackTime: fallback,
		GitSHA:       git.SHA,
		Now:          ix.Now(),
		MtimeMs:      info.ModTime().UnixMilli(),
		Size:         info.Size(),
		TeamOverride: teamOverride,
		Location:     ix.Config.Location,
		Distilled:    ix.Distilled,
	})
	if err != nil {
		return document.Built{}, fmt.Errorf("%s: %w", rel, err)
	}
	return built, nil
}

// WithFlags 는 chunks 의 사본에 숨김 표시(superseded_by, deprecated)를 붙인 것이다. 받은 슬라이스는 고치지 않는다.
func WithFlags(chunks []model.Chunk, supersededBy string, deprecated bool) []model.Chunk {
	out := make([]model.Chunk, len(chunks))
	for i, c := range chunks {
		c.SupersededBy, c.Deprecated = supersededBy, deprecated
		out[i] = c
	}
	return out
}

// readGated 는 게이트(크기·바이너리 — 데몬과 같은 규칙)를 거친 파일 내용과 stat 이다. 걸리면 *SkipError. 크기는 읽기 전에 본다.
func (ix *indexer) readGated(abs, rel string) ([]byte, fs.FileInfo, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", rel, err)
	}
	reason, err := gate(abs, info.Size(), ix.Config.Reconcile.MaxFileBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", rel, err)
	}
	if reason != "" {
		return nil, nil, &SkipError{Rel: rel, Reason: reason}
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return content, info, nil
}

// carried 는 다시 쓸 문서에 이어 붙일 값이다.
type carried struct {
	supersededBy string
	deprecated   bool
	pendingCycle bool
}

// carriedOf 는 다시 쓸 문서에 이어 붙일 superseded_by·deprecated 와 넘김 표시다. 데몬은 정본(선언·원장)에서 계산한 두 값을
// 꽂고(SupersededBy) 넘김 표시를 남기지 않는다 — 판정·증류·그래프를 그 사이클이 이어서 한다. 데몬 밖(ingest_path, audit -apply)
// 에서는 지금 색인에 있는 값을 이어받고(재색인이 밀린 문서를 기본 검색에 되살리지 않게), 색인에 없거나 내용이 다르거나 아직
// 넘기지 못한 문서면 넘김 표시를 남긴다 — 데몬은 색인이 최신인 문서를 건너뛰므로, 표시가 없으면 이 문서는 판정·증류·그래프를
// 영영 거치지 않는다. 데몬이 이미 넘겨받은 같은 내용을 다시 적재할 때는 남기지 않는다 (같은 내용은 한 번만 판정한다).
func (ix *indexer) carriedOf(ctx context.Context, rel, contentSHA string) (carried, error) {
	if ix.SupersededBy != nil {
		by, deprecated := ix.SupersededBy(rel)
		return carried{supersededBy: by, deprecated: deprecated}, nil
	}
	var out opensearch.SearchResponse[model.Chunk]
	body := map[string]any{
		"size":    1,
		"_source": []string{"superseded_by", "deprecated", "content_sha", "pending_cycle"},
		"query":   opensearch.FirstChunks(ix.Config.Workspace, map[string]any{"term": map[string]any{"source_path": rel}}),
	}
	if err := ix.OS.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search", body, opensearch.Lenient(), &out); err != nil {
		return carried{}, fmt.Errorf("flags %s: %w", rel, err)
	}
	if len(out.Hits.Hits) == 0 {
		return carried{pendingCycle: true}, nil
	}
	h := out.Hits.Hits[0].Source
	return carried{supersededBy: h.SupersededBy, deprecated: h.Deprecated, pendingCycle: h.PendingCycle || h.ContentSHA != contentSHA}, nil
}

// Write 는 문서 하나를 색인과 cold store 에 쓴다. 청크 0 의 mtime·size·content_sha 가 데몬의 "최신" 표시이고,
// 순서가 곧 복구 규칙이다:
//  1. 청크 0 을 무효 표시(content_sha·mtime 없음)로 먼저 쓴다 — 여기서부터 어디서 끊겨도 이 문서는 "색인에 있는데 낡았다" 로 보여
//     다음 사이클이 처음부터 다시 쓴다 (cold 소유로 오인되거나, 섞인 청크가 최신으로 보이지 않는다)
//  2. 문서가 전에 있던 다른 팀의 청크 JSONL 을 지운다 (frontmatter team 을 바꾼 문서)
//  3. 남길 것(대상 인덱스 안의 새 id)만 빼고 이 문서의 청크를 지운다 — 날짜·성질이 바뀌어 인덱스가 달라졌으면 옛 인덱스 쪽은 전부
//  4. 나머지 청크를 쓴다 (id 가 결정적이라 덮어쓰기)
//  5. cold store 에 원본과 청크 JSONL 을 쓴다
//  6. 청크 0 을 진짜 표시로 다시 쓴다 — 데몬 밖의 쓰기면 넘김 표시(pending_cycle)도 여기서만 붙인다 (cold store 청크에는 없다)
//
// 끊김에는 이 순서가, 동시 writer(데몬과 ingest_path·audit -apply)에는 문서 락(state.LockDoc)이 지킨다 — 쓰는 동안 그 문서를 쥔다.
func (ix *indexer) Write(ctx context.Context, p Prepared) (Result, error) {
	unlock, err := state.LockDoc(ctx, ix.Config.MetaRepoRoot, p.Rel)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	b := p.Built
	if err := ix.ensureTeamOwner(ctx, b.Meta.Team); err != nil {
		return Result{}, err
	}
	// 지우기 전에 이 문서가 있던 팀을 본다 — 3 이 옛 팀 인덱스의 청크를 지우면 더는 찾을 수 없다
	former, err := ix.teamsOf(ctx, p.Rel)
	if err != nil {
		return Result{}, err
	}
	index := targetIndex(b)
	if err := ix.writeHead(ctx, p, false); err != nil {
		return Result{}, err
	}
	if err := ix.removeFormerTeams(ctx, p.Rel, b.Meta.Team, former); err != nil {
		return Result{}, err
	}
	if err := ix.replaceChunks(ctx, p, index); err != nil {
		return Result{}, err
	}
	if err := ix.storeCold(ctx, p); err != nil {
		return Result{}, err
	}
	if err := ix.writeHead(ctx, p, true); err != nil {
		return Result{}, err
	}
	return Result{SourcePath: p.Rel, Index: index, Chunks: len(b.Chunks)}, nil
}

// targetIndex 는 문서가 들어갈 인덱스다. 청크가 없으면(쓸 것이 없다) 팀의 knowledge 인덱스.
func targetIndex(b document.Built) string {
	if len(b.Chunks) == 0 {
		return naming.KnowledgeIndex(b.Meta.Team)
	}
	return naming.IndexFor(b.Chunks[0])
}

// writeHead 는 청크 0 을 쓴다. final 이 아니면 무효 표시(content_sha·mtime·size 없음), final 이면 진짜 표시와 넘김 표시다.
func (ix *indexer) writeHead(ctx context.Context, p Prepared, final bool) error {
	if len(p.Built.Chunks) == 0 {
		return nil
	}
	head := p.Built.Chunks[0]
	if final {
		head.PendingCycle = p.PendingCycle
	} else {
		head.ContentSHA, head.MtimeMs, head.Size = "", 0, 0
	}
	if _, err := ix.BulkChunks(ctx, []model.Chunk{head}); err != nil {
		return fmt.Errorf("index %s: %w", p.Rel, err)
	}
	return nil
}

// removeFormerTeams 는 문서가 전에 있던 다른 팀의 청크 JSONL 을 지운다. frontmatter team 을 바꾸면 새 팀에만 쓰므로, 남기면 reload 가
// 옛 팀 사본을 되살려 같은 문서가 두 팀에 보인다. 원본(raw)은 이력이라 남긴다. 새 팀의 청크를 쓰기 전에 지운다 — 쓰다 끊기면 다음
// 사이클이 처음부터 다시 쓴다.
func (ix *indexer) removeFormerTeams(ctx context.Context, rel, team string, former []string) error {
	for _, t := range former {
		if t == team {
			continue
		}
		if err := ix.Cold.Remove(ctx, cold.ChunksKey(t, rel)); err != nil {
			return fmt.Errorf("cold store chunks %s (team %s): %w", rel, t, err)
		}
	}
	return nil
}

// replaceChunks 는 남길 것(대상 인덱스 안의 새 id)만 빼고 이 문서의 청크를 지운 뒤 청크 0 밖의 청크를 쓴다.
func (ix *indexer) replaceChunks(ctx context.Context, p Prepared, index string) error {
	keep := make([]string, 0, len(p.Built.Chunks))
	for _, c := range p.Built.Chunks {
		keep = append(keep, c.ID)
	}
	indices, err := ix.docIndices(ctx, p.Rel, p.Built.Meta.Team)
	if err != nil {
		return err
	}
	if _, err := opensearch.DeleteDocExcept(ctx, ix.OS, indices, ix.Config.Workspace, p.Rel, index, keep); err != nil {
		return err
	}
	if len(p.Built.Chunks) > 1 {
		if _, err := ix.BulkChunks(ctx, p.Built.Chunks[1:]); err != nil {
			return fmt.Errorf("index %s: %w", p.Rel, err)
		}
	}
	return nil
}

// storeCold 는 cold store 에 원본과 청크 JSONL 을 쓴다.
func (ix *indexer) storeCold(ctx context.Context, p Prepared) error {
	team := p.Built.Meta.Team
	if err := ix.Cold.Put(ctx, cold.RawKey(team, p.Rel), p.Content); err != nil {
		return fmt.Errorf("cold store raw %s: %w", p.Rel, err)
	}
	if err := ix.Cold.Put(ctx, cold.ChunksKey(team, p.Rel), cold.EncodeChunks(p.Built.Chunks)); err != nil {
		return fmt.Errorf("cold store chunks %s: %w", p.Rel, err)
	}
	return nil
}

// ingestFile 은 디스크의 철자로 맞춘 파일 하나를 적재한다. 색인 대상이 아니면 nil, 게이트에 걸리면 Skipped 에 사유를 담은 결과다.
func (ix *indexer) ingestFile(ctx context.Context, abs, rel, teamOverride string) (*Result, error) {
	if !IsIndexable(rel) {
		return nil, nil
	}
	p, err := ix.prepare(ctx, abs, rel, teamOverride)
	var skip *SkipError
	if errors.As(err, &skip) {
		return &Result{SourcePath: skip.Rel, Skipped: skip.Reason}, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := ix.Write(ctx, p)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// IngestPath 는 파일 또는 디렉토리를 적재한다. 입력을 디스크의 철자로 한 번 바꾸고, 디렉토리는 그 철자로 걷는다 — 걸으며 얻은
// 이름이 디스크의 이름이라 파일마다 철자를 다시 맞추지 않는다 (색인 대상 판단도 실제 이름으로 된다).
func (ix *indexer) IngestPath(ctx context.Context, input, teamOverride string) ([]Result, error) {
	abs, rel, err := ix.resolve(input)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", input, err)
	}
	files := []docFile{{abs: abs, rel: rel}}
	if info.IsDir() {
		if files, err = ix.walkIndexable(abs); err != nil {
			return nil, fmt.Errorf("walk %s: %w", input, err)
		}
	}
	results := make([]Result, 0, len(files))
	for _, f := range files {
		r, err := ix.ingestFile(ctx, f.abs, f.rel, teamOverride)
		if err != nil {
			return results, err
		}
		if r != nil {
			results = append(results, *r)
		}
	}
	return results, nil
}

// docFile 은 디스크의 철자로 된 파일(abs)과 그 정체(rel, NFC)다.
type docFile struct{ abs, rel string }

// walkIndexable 은 dir 아래 색인 대상 파일이다 (rel 순). 스캔과 같이 색인 범위 밖 디렉토리(main/{repo} 의 코드 트리, node_modules
// 등)는 내려가지 않는다.
func (ix *indexer) walkIndexable(dir string) ([]docFile, error) {
	var files []docFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(ix.Config.MetaRepoRoot, p)
		if err != nil {
			return err
		}
		rel = canonicalRel(rel)
		if d.IsDir() && !InScope(rel) {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Type().IsRegular() && IsIndexable(rel) {
			files = append(files, docFile{abs: p, rel: rel})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, err
}

// RemoveFile 은 문서를 색인과 청크 JSONL 에서 뺀다. 색인에 있는 source_path 를 그대로 지운다 — 색인 규칙이 바뀌었거나 옛 cold
// 청크를 reload 해 색인 범위 밖이 된 경로도 지운다 (범위 밖이라고 건너뛰면 tombstone·audit -apply 가 지웠다고 하는데 남는다).
func (ix *indexer) RemoveFile(ctx context.Context, input string, indices []string) (int, error) {
	_, rel, err := resolveInsideRoot(ix.Config.MetaRepoRoot, input)
	if err != nil {
		return 0, err
	}
	unlock, err := state.LockDoc(ctx, ix.Config.MetaRepoRoot, rel)
	if err != nil {
		return 0, err
	}
	defer unlock()
	teams, err := ix.teamsOf(ctx, rel)
	if err != nil {
		return 0, err
	}
	if indices == nil {
		if indices, err = opensearch.TeamIndices(ctx, ix.OS, teams...); err != nil {
			return 0, err
		}
	}
	deleted, err := opensearch.DeleteDoc(ctx, ix.OS, indices, ix.Config.Workspace, rel)
	if err != nil {
		return 0, err
	}
	if LiveUnderOtherSpelling(ix.Config.MetaRepoRoot, rel) {
		// 같은 파일이 디스크에 다른 철자로 있다 — rel 은 옛 철자의 색인일 뿐이다. APFS 위의 로컬 cold store 에서는 두 철자의 키가
		// 같은 파일이라, 청크 JSONL 을 지우면 진짜 문서의 사본(isColdOwned·archive·reload 가 기대는 것)이 사라진다
		return deleted, nil
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

// docIndices 는 문서의 청크가 있을 수 있는 인덱스다 — 지금 그 문서를 가진 팀들(팀을 바꾼 문서의 옛 팀 포함)과 새로 쓸 팀의 인덱스.
func (ix *indexer) docIndices(ctx context.Context, rel, team string) ([]string, error) {
	teams, err := ix.teamsOf(ctx, rel)
	if err != nil {
		return nil, err
	}
	return opensearch.TeamIndices(ctx, ix.OS, append(teams, team)...)
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
