package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// Change 는 문서 변화 종류다.
type Change string

const (
	ChangeNew      Change = "new"
	ChangeModified Change = "modified"
	ChangeRemoved  Change = "removed"
)

// DiffEntry 는 이번 사이클에 내용이 바뀐 문서다. 판정·증류·그래프의 입력이다 (메모리 전달만, 영속 없음).
type DiffEntry struct {
	SourcePath string
	Team       string
	Project    string
	DocType    model.DocType
	Kind       model.Kind
	Change     Change
	BodySHA    string
	EventTime  time.Time
	Title      string
	Body       string
	Supersedes string
}

func diffFrom(rel string, b document.Built, change Change) DiffEntry {
	return DiffEntry{
		SourcePath: rel, Team: b.Meta.Team, Project: b.Meta.Repo, DocType: b.Meta.DocType, Kind: b.Meta.Kind, Change: change, BodySHA: b.BodySHA,
		EventTime: b.EventTime, Title: b.Title, Body: b.Body, Supersedes: b.Meta.Supersedes,
	}
}

// Reconciler 는 수렴 사이클 A 다.
type Reconciler struct {
	OS      opensearch.Client
	Config  config.Config
	Indexer ingest.Indexer
	Cold    cold.Store
	Judge   *state.JudgeState
	Archive *state.ArchiveState
	Log     func(format string, args ...any)

	mu sync.Mutex
	// coldOwned 는 cold 가 소유한 hot 파일의 stat 이다. stat 이 그대로면 다음 사이클에 다시 읽지 않는다.
	coldOwned map[string]statKey

	desiredMu sync.RWMutex
	desired   map[string]string // 마지막으로 수렴한 superseded_by (재색인 때 위치 유지)
}

type statKey struct {
	mtimeMs int64
	size    int64
}

// SupersededBy 는 마지막 수렴 기준 superseded_by 다. Indexer 의 Deps.SupersededBy 로 꽂는다.
func (r *Reconciler) SupersededBy(sourcePath string) string {
	r.desiredMu.RLock()
	defer r.desiredMu.RUnlock()
	return r.desired[sourcePath]
}

type cycleState struct {
	indices  []string
	existing map[string]ingest.IndexedDoc
	owner    *cold.Ownership
	// restored 는 복원한 인덱스(데몬이 고치지 않는다)에 든 문서다. tombstone·수렴 대상이 아니다.
	restored map[string]ingest.IndexedDoc
}

// Once 는 hot(git working tree)과 인덱스를 맞춘다. 매 사이클이 곧 풀 싱크라 첫 기동 분기가 없다.
//  1. stat 이 같으면 건너뛴다  2. stat 이 달라도 sha 가 같으면 stat 만 갱신  3. 내용이 바뀌면 재색인
//  4. 이미 cold 로 간 원본(같은 내용이 cold store 에 보관돼 있다)은 다시 색인하지 않는다 — cold 소유.
//     되살리려면 restore_cold 하거나, date 를 갱신해 다시 쓴다
//  5. 디스크에 없는 문서는 인덱스에서 지운다(tombstone) — 스캔이 일부만 됐으면 지우지 않는다
//  6. superseded_by·deprecated 를 원장·선언대로 수렴
func (r *Reconciler) Once(ctx context.Context) (state.CycleSummary, []DiffEntry, error) {
	start := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coldOwned == nil {
		r.coldOwned = map[string]statKey{}
	}

	files, skipped, err := ingest.ScanHot(r.Config.MetaRepoRoot, r.Config.Reconcile.MaxFileBytes)
	partial := errors.Is(err, ingest.ErrPartialScan)
	if err != nil && !partial {
		return state.CycleSummary{}, nil, fmt.Errorf("scan: %w", err)
	}
	sum := state.CycleSummary{Scanned: len(files), Skipped: len(skipped), Partial: partial}
	for _, s := range skipped {
		r.Log("[reconcile] skip %s (%s)", s.Rel, s.Reason)
	}
	if len(files) > r.Config.Reconcile.SanityMaxFiles {
		r.Log("[reconcile] matched %d files > sanityMaxFiles(%d) — 설정 오류로 판단, 색인 거부", len(files), r.Config.Reconcile.SanityMaxFiles)
		sum.Refused = true
		sum.TookMs = time.Since(start).Milliseconds()
		return sum, nil, nil
	}

	st, err := r.loadState(ctx)
	if err != nil {
		return sum, nil, err
	}
	var diff []DiffEntry
	failed := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return sum, diff, err
		}
		entry, err := r.syncFile(ctx, f, st, &sum)
		if err != nil {
			// 파일 하나의 실패(다른 workspace 의 팀, 일시적인 _bulk 실패 …)가 나머지 파일·tombstone·수렴을 막지 않게 넘긴다.
			// 청크 0 을 마지막에 쓰므로 이 파일은 다음 사이클이 다시 시도한다
			r.Log("[reconcile] fail %s (%v)", f.Rel, err)
			failed++
			continue
		}
		if entry != nil {
			diff = append(diff, *entry)
		}
	}
	sum.Failed = failed

	removed, err := r.tombstone(ctx, st, partial)
	if err != nil {
		return sum, diff, err
	}
	diff = append(diff, removed...)
	sum.Removed = len(removed)

	converged, err := r.Converge(ctx)
	if err != nil {
		return sum, diff, err
	}
	sum.FlagsConverged = converged
	sum.TookMs = time.Since(start).Milliseconds()
	return sum, diff, nil
}

func (r *Reconciler) loadState(ctx context.Context) (cycleState, error) {
	indices, err := ingest.ManagedIndices(ctx, r.OS)
	if err != nil {
		return cycleState{}, fmt.Errorf("list indices: %w", err)
	}
	existing, err := ingest.FetchIndexedDocs(ctx, r.OS, indices, r.Config.Workspace)
	if err != nil {
		return cycleState{}, fmt.Errorf("list indexed docs: %w", err)
	}
	archived, err := cold.ArchivedMonths(ctx, r.OS, r.Config)
	if err != nil {
		return cycleState{}, fmt.Errorf("list archived: %w", err)
	}
	restoredIdx, err := ingest.RestoredIndices(ctx, r.OS)
	if err != nil {
		return cycleState{}, fmt.Errorf("list restored: %w", err)
	}
	restored, err := ingest.FetchIndexedDocs(ctx, r.OS, restoredIdx, r.Config.Workspace)
	if err != nil {
		return cycleState{}, fmt.Errorf("list restored docs: %w", err)
	}
	return cycleState{indices: indices, existing: existing, owner: cold.NewOwnership(archived, r.Cold), restored: restored}, nil
}

// syncFile 은 파일 하나를 색인과 맞춘다. 내용이 바뀌어 다시 썼거나, 데몬 밖에서 쓴 문서(넘김 표시)면 DiffEntry 를 돌려준다.
func (r *Reconciler) syncFile(ctx context.Context, f ingest.ScannedFile, st cycleState, sum *state.CycleSummary) (*DiffEntry, error) {
	ex, known := st.existing[f.Rel]
	delete(st.existing, f.Rel)
	stat := statKey{f.MtimeMs, f.Size}
	// current 면 색인에 있는 그대로 써도 된다 — 두 인덱스에 걸쳐 있거나 낮은 판(필드가 빠진 것)이면 다시 쓴다.
	// 넘김 표시가 있으면 stat 이 같아도 본다 — 데몬 밖(ingest_path, audit -apply)의 쓰기는 판정·증류·그래프를 거치지 않았다
	current := known && !ex.Duplicate && ex.IndexVersion >= model.IndexVersion
	if current && !ex.PendingCycle && ex.MtimeMs == f.MtimeMs && ex.Size == f.Size {
		return nil, nil
	}
	if rex, ok := st.restored[f.Rel]; !known && ok && rex.MtimeMs == f.MtimeMs && rex.Size == f.Size {
		return nil, nil // 복원한 인덱스에 지금 내용이 그대로 들어 있다 (그 달 hot 파일을 고치면 그 인덱스에 쓴다)
	}
	if !known && r.coldOwned[f.Rel] == stat {
		sum.ColdOwned++
		return nil, nil
	}
	// Prepare 가 게이트(바이너리·크기 — ingest_path 와 같은 규칙)를 거친다
	prepared, err := r.Indexer.Prepare(ctx, f.Abs, "")
	if err != nil {
		r.Log("[reconcile] skip %s (%s)", f.Rel, skipReason(err))
		sum.Skipped++
		return nil, nil
	}
	b := prepared.Built
	if known && ex.ContentSHA == b.ContentSHA {
		return r.syncSameContent(ctx, f, ex, current, prepared, st.indices, sum)
	}
	if !known {
		owned, err := r.isColdOwned(ctx, b, st.owner)
		if err != nil {
			return nil, err
		}
		if owned {
			r.coldOwned[f.Rel] = stat
			sum.ColdOwned++
			return nil, nil
		}
	}
	delete(r.coldOwned, f.Rel)
	if _, err := r.Indexer.Write(ctx, prepared); err != nil {
		return nil, err
	}
	sum.Indexed++
	change := ChangeNew
	if known {
		change = ChangeModified
	}
	entry := diffFrom(f.Rel, b, change)
	return &entry, nil
}

// syncSameContent 는 색인과 내용이 같은 파일이다. 판이 그대로면 stat 만 갱신하고, 판이 낮거나(새 필드를 채운다) 두 인덱스에
// 걸쳐 있으면 다시 쓰기만 한다 — 내용 변경이 아니므로 판정·증류·그래프에 넘기지 않고(아무도 고치지 않은 문서가 latest-wins 로
// 숨지 않게), 밀린·deprecate 표시는 색인에 있던 그대로 잇는다. 다만 데몬 밖(ingest_path, audit -apply)에서 쓴 문서(넘김 표시)는
// 그 쓰기가 판정·증류·그래프를 거치지 않았으므로 변경분으로 넘기고 표시를 지운다 (다시 쓰면 데몬의 쓰기가 표시 없이 덮는다).
func (r *Reconciler) syncSameContent(ctx context.Context, f ingest.ScannedFile, ex ingest.IndexedDoc, current bool, prepared ingest.Prepared,
	indices []string, sum *state.CycleSummary) (*DiffEntry, error) {
	if current {
		if err := r.refreshStat(ctx, f, ex, indices, sum); err != nil {
			return nil, err
		}
	} else {
		for i := range prepared.Built.Chunks {
			prepared.Built.Chunks[i].SupersededBy, prepared.Built.Chunks[i].Deprecated = ex.SupersededBy, ex.Deprecated
		}
		if _, err := r.Indexer.Write(ctx, prepared); err != nil {
			return nil, err
		}
		sum.Indexed++
	}
	if !ex.PendingCycle {
		return nil, nil
	}
	r.Log("[reconcile] handoff %s (데몬 밖에서 색인한 문서 — 판정·증류·그래프로 넘긴다)", f.Rel)
	entry := diffFrom(f.Rel, prepared.Built, ChangeModified)
	return &entry, nil
}

// refreshStat 은 내용이 그대로인 문서의 stat 을 고치고 넘김 표시를 지운다. 색인에 있던 내용(content_sha)의 청크에만 —
// 그 사이 데몬 밖에서 새 내용을 썼으면(새 표시가 붙는다) 건드리지 않는다.
func (r *Reconciler) refreshStat(ctx context.Context, f ingest.ScannedFile, ex ingest.IndexedDoc, indices []string, sum *state.CycleSummary) error {
	fields := map[string]any{}
	statChanged := ex.MtimeMs != f.MtimeMs || ex.Size != f.Size
	if statChanged {
		fields["mtime_ms"], fields["size"] = f.MtimeMs, f.Size
	}
	if ex.PendingCycle {
		fields["pending_cycle"] = nil
	}
	if len(fields) == 0 {
		return nil
	}
	sameContent := []any{map[string]any{"term": map[string]any{"content_sha": ex.ContentSHA}}}
	if err := opensearch.UpdateDocWhere(ctx, r.OS, indices, r.Config.Workspace, f.Rel, sameContent, fields); err != nil {
		return err
	}
	if statChanged {
		sum.StatUpdated++
	}
	return nil
}

// skipReason 은 skip 로그의 사유다 — 게이트에 걸렸으면 그 사유(binary content, size N > M)만, 아니면 오류 그대로.
func skipReason(err error) string {
	var skip *ingest.SkipError
	if errors.As(err, &skip) {
		return skip.Reason
	}
	return err.Error()
}

// isColdOwned 는 이 workspace 의 cold store 에 지금과 같은 내용이 보관돼 있고, 그 내용이 들어갔던 인덱스가
// cold(스냅샷)로 간 경우다. 처음 보는 문서·내용이 바뀐 문서는 색인한다 — 다음 tier 가 다시 cold 로 보낸다.
func (r *Reconciler) isColdOwned(ctx context.Context, b document.Built, owner *cold.Ownership) (bool, error) {
	if len(b.Chunks) == 0 {
		return false, nil
	}
	chunks, ok, err := cold.Archived(ctx, r.Cold, b.Meta.Team, b.Chunks[0].SourcePath)
	if err != nil || !ok {
		return false, err
	}
	head := chunks[0]
	if head.ContentSHA != b.ContentSHA {
		return false, nil
	}
	return owner.Owns(ctx, head.Team, naming.IndexFor(head), head.SourcePath, head.ContentSHA)
}

func (r *Reconciler) tombstone(ctx context.Context, st cycleState, partial bool) ([]DiffEntry, error) {
	if partial {
		if len(st.existing) > 0 {
			r.Log("[reconcile] 스캔이 일부만 됐습니다 — 이번 사이클은 삭제(tombstone)를 건너뜁니다")
		}
		return nil, nil
	}
	var removed []DiffEntry
	for _, gone := range st.existing {
		if _, err := r.Indexer.RemoveFile(ctx, gone.SourcePath, st.indices); err != nil {
			return removed, err
		}
		removed = append(removed, DiffEntry{SourcePath: gone.SourcePath, Team: gone.Team, Kind: gone.Kind, Change: ChangeRemoved, BodySHA: gone.BodySHA})
	}
	return removed, nil
}

// Converge 는 superseded_by 와 deprecated 를 정본대로 맞춘다. 정본은 개정판 frontmatter 의 supersedes 선언,
// 판정 원장(.cache/judge), cold 문서 조치 원장(.cache/archive)이다. 인덱스 값은 파생물이라 매 사이클 다시 맞춘다
// (인덱스를 지워도 같은 결과로 재구축된다). knowledge 가 밀리면 자기 날짜의 월별 인덱스로 옮겨 노화시킨다.
func (r *Reconciler) Converge(ctx context.Context) (int, error) {
	indices, err := ingest.ManagedIndices(ctx, r.OS)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	docs, err := ingest.FetchIndexedDocs(ctx, r.OS, indices, r.Config.Workspace)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	desired, err := DesiredSupersededBy(docs, r.Judge)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	r.desiredMu.Lock()
	r.desired = desired
	r.desiredMu.Unlock()

	deprecated, err := r.deprecatedPaths(docs)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	changed := 0
	for _, d := range docs {
		want := desired[d.SourcePath]
		if d.SupersededBy != want {
			if err := r.applySuperseded(ctx, indices, d, want); err != nil {
				return changed, err
			}
			changed++
		}
		if wantDep := deprecated[d.SourcePath]; d.Deprecated != wantDep {
			if err := opensearch.UpdateDoc(ctx, r.OS, indices, r.Config.Workspace, d.SourcePath, map[string]any{"deprecated": wantDep}); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

func (r *Reconciler) deprecatedPaths(docs map[string]ingest.IndexedDoc) (map[string]bool, error) {
	out := map[string]bool{}
	if r.Archive == nil {
		return out, nil
	}
	teams := map[string]bool{}
	for _, d := range docs {
		teams[d.Team] = true
	}
	for team := range teams {
		paths, err := r.Archive.Deprecated(team)
		if err != nil {
			return nil, err
		}
		for p := range paths {
			out[p] = true
		}
	}
	return out, nil
}

// applySuperseded 는 문서의 superseded_by 를 바꾼다. knowledge 는 플래그에 따라 인덱스가 달라지므로
// 청크를 새 인덱스로 옮기고 cold store 청크도 다시 쓴다. episodic 은 제자리에서 고친다.
func (r *Reconciler) applySuperseded(ctx context.Context, indices []string, d ingest.IndexedDoc, want string) error {
	if want == "" {
		r.Log("[judge] superseded_by %s → (해제)", d.SourcePath)
	} else {
		r.Log("[judge] superseded_by %s → %s", d.SourcePath, want)
	}
	if d.Kind != model.KindKnowledge {
		var value any
		if want != "" {
			value = want
		}
		return opensearch.UpdateDoc(ctx, r.OS, indices, r.Config.Workspace, d.SourcePath, map[string]any{"superseded_by": value})
	}
	hits, err := ingest.DocChunks(ctx, r.OS, indices, r.Config.Workspace, d.SourcePath)
	if err != nil {
		return err
	}
	chunks := make([]model.Chunk, 0, len(hits))
	from := map[string]bool{}
	for _, h := range hits {
		c := h.Source
		c.ID = h.ID
		c.SupersededBy = want
		chunks = append(chunks, c)
		from[h.Index] = true
	}
	if len(chunks) == 0 {
		return nil
	}
	if _, err := r.Indexer.BulkChunks(ctx, chunks); err != nil {
		return fmt.Errorf("relocate %s: %w", d.SourcePath, err)
	}
	target := naming.IndexFor(chunks[0])
	var old []string
	for idx := range from {
		if idx != target {
			old = append(old, idx)
		}
	}
	if _, err := opensearch.DeleteDoc(ctx, r.OS, old, r.Config.Workspace, d.SourcePath); err != nil {
		return fmt.Errorf("relocate %s: %w", d.SourcePath, err)
	}
	if err := r.Cold.Put(ctx, cold.ChunksKey(d.Team, d.SourcePath), cold.EncodeChunks(chunks)); err != nil {
		return fmt.Errorf("relocate %s: cold store: %w", d.SourcePath, err)
	}
	return nil
}

// DesiredSupersededBy 는 문서별로 있어야 할 superseded_by 다. 사람의 선언이 판정보다 우선하고,
// A→B→C 처럼 이어지면 마지막 판(C)을 가리키게 압축한다.
func DesiredSupersededBy(docs map[string]ingest.IndexedDoc, judge *state.JudgeState) (map[string]string, error) {
	direct := map[string]string{}
	for team, bodies := range bodiesByTeam(docs) {
		m, err := judge.SupersededMap(team, bodies)
		if err != nil {
			return nil, err
		}
		for oldPath, newPath := range m {
			direct[oldPath] = newPath
		}
	}
	for _, d := range docs {
		if d.Supersedes != "" && d.Supersedes != d.SourcePath {
			if _, ok := docs[d.Supersedes]; ok {
				direct[d.Supersedes] = d.SourcePath
			}
		}
	}
	return compressChains(direct), nil
}

// bodiesByTeam 은 팀 → (경로 → 본문 sha) 다. 판정 원장이 팀별이라 결론을 옮길 문서도 같은 팀 안에서만 찾는다.
func bodiesByTeam(docs map[string]ingest.IndexedDoc) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, d := range docs {
		if out[d.Team] == nil {
			out[d.Team] = map[string]string{}
		}
		out[d.Team][d.SourcePath] = d.BodySHA
	}
	return out
}

// compressChains 는 A→B, B→C 를 A→C, B→C 로 만든다. 순환은 순환에 들어선 지점에서 끊는다.
func compressChains(direct map[string]string) map[string]string {
	out := make(map[string]string, len(direct))
	for start := range direct {
		seen := map[string]bool{start: true}
		cur := direct[start]
		for {
			next, ok := direct[cur]
			if !ok || seen[cur] {
				break
			}
			seen[cur] = true
			cur = next
		}
		if cur != start {
			out[start] = cur
		}
	}
	return out
}
