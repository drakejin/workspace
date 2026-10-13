package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

	flagsMu    sync.RWMutex
	desired    map[string]string // 마지막으로 계산한 superseded_by (재색인 때 숨김 표시와 위치 유지)
	deprecated map[string]bool   // 마지막으로 계산한 deprecated
}

type statKey struct {
	mtimeMs int64
	size    int64
}

func statOf(f ingest.ScannedFile) statKey { return statKey{f.MtimeMs, f.Size} }

// SupersededBy 는 마지막으로 계산한 숨김 표시(superseded_by, deprecated)다. Indexer 의 Deps.SupersededBy 로 꽂는다 — 사이클이 파일을
// 다시 쓰기 전에(loadState) 정본에서 계산해 두므로, 다시 쓴 문서가 수렴 전에 기본 검색에 드러나거나 knowledge 인덱스를 오가지 않는다.
func (r *Reconciler) SupersededBy(sourcePath string) (string, bool) {
	r.flagsMu.RLock()
	defer r.flagsMu.RUnlock()
	return r.desired[sourcePath], r.deprecated[sourcePath]
}

// refreshFlags 는 docs 로 정본 숨김 표시를 계산해 SupersededBy 가 돌려줄 값으로 둔다.
func (r *Reconciler) refreshFlags(docs map[string]ingest.IndexedDoc) (map[string]string, map[string]bool, error) {
	desired, err := DesiredSupersededBy(docs, r.Judge)
	if err != nil {
		return nil, nil, err
	}
	deprecated, err := r.deprecatedPaths(docs)
	if err != nil {
		return nil, nil, err
	}
	r.flagsMu.Lock()
	r.desired, r.deprecated = desired, deprecated
	r.flagsMu.Unlock()
	return desired, deprecated, nil
}

// cycleState 는 사이클이 시작할 때 읽은 색인 상태다. 사이클 동안 읽기만 한다.
type cycleState struct {
	indices  []string
	existing map[string]ingest.IndexedDoc
	owner    *cold.Ownership
	// restored 는 복원한 인덱스(데몬이 고치지 않는다)에 든 문서다. tombstone·수렴 대상이 아니다.
	restored map[string]ingest.IndexedDoc
	// partial 이면 스캔이 일부만 됐다 — 보지 못한 파일을 지워진 것으로 보면 안 된다.
	partial bool
}

// Once 는 hot(git working tree)과 인덱스를 맞춘다. 매 사이클이 곧 풀 싱크라 첫 기동 분기가 없다.
//  1. stat 이 같으면 건너뛴다  2. stat 이 달라도 sha 가 같으면 stat 만 갱신  3. 내용이 바뀌면 재색인
//  4. 이미 cold 로 간 원본(같은 내용이 cold store 에 보관돼 있다)은 다시 색인하지 않는다 — cold 소유.
//     되살리려면 restore_cold 하거나, date 를 갱신해 다시 쓴다
//  5. 이번 스캔에서 보지 못한 문서는 인덱스에서 지운다(tombstone) — 디스크에 없는 파일, 게이트(크기·바이너리)에 걸린 파일,
//     색인 범위 밖이 된 경로. 스캔이 일부만 됐으면 지우지 않는다
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
	st.partial = partial
	synced, err := r.syncFiles(ctx, files, st, &sum)
	if err != nil {
		return sum, synced.diff, err
	}
	gated := r.reportGated(skipped, synced.gated, st)
	removed, err := r.tombstone(ctx, st, synced.seen, gated)
	diff := append(synced.diff, removed...)
	sum.Removed = len(removed)
	if err != nil {
		return sum, diff, err
	}
	sum.FlagsConverged, err = r.Converge(ctx)
	sum.TookMs = time.Since(start).Milliseconds()
	return sum, diff, err
}

func (r *Reconciler) loadState(ctx context.Context) (cycleState, error) {
	indices, restoredIdx, err := ingest.WorkspaceIndices(ctx, r.OS, r.Config.Workspace, r.Config.DefaultTeam)
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
	restored, err := ingest.FetchIndexedDocs(ctx, r.OS, restoredIdx, r.Config.Workspace)
	if err != nil {
		return cycleState{}, fmt.Errorf("list restored docs: %w", err)
	}
	// 파일을 다시 쓰기 전에 숨김 표시를 정본대로 계산해 둔다 — boot 사이클도 밀린·deprecate 문서를 숨긴 채로 다시 쓴다
	if _, _, err := r.refreshFlags(existing); err != nil {
		return cycleState{}, fmt.Errorf("flags: %w", err)
	}
	return cycleState{indices: indices, existing: existing, owner: cold.NewOwnership(archived, r.Cold), restored: restored}, nil
}

// synced 는 파일들을 색인과 맞춘 결과다.
type synced struct {
	diff []DiffEntry
	// seen 은 디스크에 있고 색인 범위 안인 파일이다 — tombstone 은 색인에 있는데 여기 없는 문서를 지운다
	seen map[string]bool
	// gated 는 게이트(바이너리)에 걸린 파일과 사유다 — 색인 범위 밖이라 seen 이 아니다 (크기 게이트는 스캔이 이미 거른다)
	gated map[string]string
}

// syncFiles 는 스캔한 파일을 하나씩 맞춘다. 파일 하나의 실패(다른 workspace 의 팀, 일시적인 _bulk 실패, frontmatter 오류 …)가
// 나머지 파일·tombstone·수렴을 막지 않게 넘긴다 — 청크 0 을 마지막에 쓰므로 그 파일은 다음 사이클이 다시 시도하고, 그동안 색인에
// 있던 판은 그대로 둔다 (seen).
func (r *Reconciler) syncFiles(ctx context.Context, files []ingest.ScannedFile, st cycleState, sum *state.CycleSummary) (synced, error) {
	out := synced{seen: make(map[string]bool, len(files)), gated: map[string]string{}}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := r.syncFile(ctx, f, st, sum)
		switch {
		case err != nil:
			r.Log("[reconcile] fail %s (%v)", f.Rel, err)
			sum.Failed++
			out.seen[f.Rel] = true
		case res.gated != "":
			out.gated[f.Rel] = res.gated
		default:
			out.seen[f.Rel] = true
		}
		if res.entry != nil {
			out.diff = append(out.diff, *res.entry)
		}
	}
	return out, nil
}

// fileResult 는 파일 하나를 맞춘 결과다.
type fileResult struct {
	entry *DiffEntry // 판정·증류·그래프로 넘길 변경분 (없으면 nil)
	gated string     // 게이트에 걸린 사유 (걸리지 않았으면 "")
}

// syncFile 은 파일 하나를 색인과 맞춘다. 내용이 바뀌어 다시 썼거나, 데몬 밖에서 쓴 문서(넘김 표시)면 변경분을 돌려준다.
// 인자로 받은 색인 상태(st)는 읽기만 한다.
func (r *Reconciler) syncFile(ctx context.Context, f ingest.ScannedFile, st cycleState, sum *state.CycleSummary) (fileResult, error) {
	ex, known := st.existing[f.Rel]
	if upToDate(f, ex, known, st.restored) {
		return fileResult{}, nil
	}
	if !known && r.coldOwned[f.Rel] == statOf(f) {
		sum.ColdOwned++ // 지난 사이클에 cold 소유로 본 stat 그대로다
		return fileResult{}, nil
	}
	// Prepare 가 게이트(바이너리·크기 — ingest_path 와 같은 규칙)를 거친다
	prepared, err := r.Indexer.Prepare(ctx, f.Abs, "")
	var skip *ingest.SkipError
	if errors.As(err, &skip) {
		sum.Skipped++
		return fileResult{gated: skip.Reason}, nil
	}
	if err != nil {
		return fileResult{}, err
	}
	if known && ex.ContentSHA == prepared.Built.ContentSHA {
		entry, err := r.syncSameContent(ctx, f, ex, prepared, st.indices, sum)
		return fileResult{entry: entry}, err
	}
	if !known {
		owned, err := r.claimColdOwned(ctx, f, prepared.Built, st.owner, sum)
		if err != nil || owned {
			return fileResult{}, err
		}
	}
	entry, err := r.rewrite(ctx, f.Rel, prepared, known, sum)
	return fileResult{entry: entry}, err
}

// upToDate 는 내용을 읽지 않고 건너뛸 파일이다: 색인이 최신이다(판이 낮지 않고, 한 인덱스에만 있고, stat 이 같고, 넘김 표시가 없다),
// 또는 색인에 없지만 복원한 인덱스에 지금 내용이 그대로 들어 있다(그 달 hot 파일을 고치면 그 인덱스에 쓴다). 넘김 표시가 있으면
// stat 이 같아도 본다 — 데몬 밖(ingest_path, audit -apply)의 쓰기는 판정·증류·그래프를 거치지 않았다.
func upToDate(f ingest.ScannedFile, ex ingest.IndexedDoc, known bool, restored map[string]ingest.IndexedDoc) bool {
	if known {
		return isCurrent(ex) && !ex.PendingCycle && ex.MtimeMs == f.MtimeMs && ex.Size == f.Size
	}
	rex, ok := restored[f.Rel]
	return ok && rex.MtimeMs == f.MtimeMs && rex.Size == f.Size
}

// isCurrent 는 색인에 있는 그대로 써도 되는 문서다 — 두 인덱스에 걸쳐 있거나 낮은 판(필드가 빠진 것)이면 다시 쓴다.
func isCurrent(ex ingest.IndexedDoc) bool {
	return !ex.Duplicate && ex.IndexVersion >= model.IndexVersion
}

// claimColdOwned 는 처음 보는 파일이 cold 소유인지 보고, 그렇다면 그 stat 을 기억해 다음 사이클에 다시 읽지 않는다.
func (r *Reconciler) claimColdOwned(ctx context.Context, f ingest.ScannedFile, b document.Built, owner *cold.Ownership, sum *state.CycleSummary) (bool, error) {
	owned, err := r.isColdOwned(ctx, b, owner)
	if err != nil || !owned {
		return false, err
	}
	r.coldOwned[f.Rel] = statOf(f)
	sum.ColdOwned++
	return true, nil
}

// rewrite 는 내용이 바뀐(또는 처음 보는) 문서를 다시 쓰고 변경분을 돌려준다.
func (r *Reconciler) rewrite(ctx context.Context, rel string, p ingest.Prepared, known bool, sum *state.CycleSummary) (*DiffEntry, error) {
	delete(r.coldOwned, rel)
	if _, err := r.Indexer.Write(ctx, p); err != nil {
		return nil, err
	}
	sum.Indexed++
	change := ChangeNew
	if known {
		change = ChangeModified
	}
	entry := diffFrom(rel, p.Built, change)
	return &entry, nil
}

// syncSameContent 는 색인과 내용이 같은 파일이다. 판이 그대로면 stat 만 갱신하고, 판이 낮거나(새 필드를 채운다) 두 인덱스에
// 걸쳐 있으면 다시 쓰기만 한다 — 내용 변경이 아니므로 판정·증류·그래프에 넘기지 않고(아무도 고치지 않은 문서가 latest-wins 로
// 숨지 않게), 밀린·deprecate 표시는 색인에 있던 그대로 잇는다. 다만 데몬 밖(ingest_path, audit -apply)에서 쓴 문서(넘김 표시)는
// 그 쓰기가 판정·증류·그래프를 거치지 않았으므로 변경분으로 넘기고 표시를 지운다 (다시 쓰면 데몬의 쓰기가 표시 없이 덮는다).
func (r *Reconciler) syncSameContent(ctx context.Context, f ingest.ScannedFile, ex ingest.IndexedDoc, prepared ingest.Prepared,
	indices []string, sum *state.CycleSummary) (*DiffEntry, error) {
	if isCurrent(ex) {
		if err := r.refreshStat(ctx, f, ex, indices, sum); err != nil {
			return nil, err
		}
	} else {
		kept := prepared
		kept.Built.Chunks = ingest.WithFlags(prepared.Built.Chunks, ex.SupersededBy, ex.Deprecated)
		if _, err := r.Indexer.Write(ctx, kept); err != nil {
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

// reportGated 는 게이트(크기·바이너리)에 걸린 파일을 알리고, 색인에 있어 tombstone 이 지울 것의 사유를 돌려준다. 둘 다 색인 범위
// 밖이다 — 색인에 있던 문서는 tombstone 이 이 사유와 함께 색인에서 빼며 remove 로 알리고, 색인에 없던 파일(또는 스캔이 일부만 돼
// 지우지 않는 사이클)은 여기서 skip 으로 알린다. 지우지 않는 것만 skip 이라고 쓴다. 읽지 못한 디렉토리(스캔이 일부만 됐다)도 skip 이다.
func (r *Reconciler) reportGated(scanSkipped []ingest.Skipped, binary map[string]string, st cycleState) map[string]string {
	all := make(map[string]string, len(scanSkipped)+len(binary))
	for _, s := range scanSkipped {
		all[s.Rel] = s.Reason
	}
	for rel, reason := range binary {
		all[rel] = reason
	}
	out := map[string]string{}
	for rel, reason := range all {
		if _, indexed := st.existing[rel]; indexed && !st.partial {
			out[rel] = reason
			continue
		}
		r.Log("[reconcile] skip %s (%s)", rel, reason)
	}
	return out
}

// tombstone 은 색인에는 있는데 이번 스캔에서 보지 못한(seen 이 아닌) 문서를 지운다 — 디스크에 없는 파일, 게이트에 걸린 파일(gated),
// 색인 규칙이 바뀌어 범위 밖이 된 경로. 정말 지운 것만 변경분(removed)으로 센다. 스캔이 일부만 됐으면 지우지 않는다.
func (r *Reconciler) tombstone(ctx context.Context, st cycleState, seen map[string]bool, gated map[string]string) ([]DiffEntry, error) {
	gone := unseen(st.existing, seen)
	if st.partial {
		if len(gone) > 0 {
			r.Log("[reconcile] 스캔이 일부만 됐습니다 — 이번 사이클은 삭제(tombstone)를 건너뜁니다")
		}
		return nil, nil
	}
	var removed []DiffEntry
	for _, d := range gone {
		n, err := r.Indexer.RemoveFile(ctx, d.SourcePath, st.indices)
		if err != nil {
			return removed, err
		}
		if n == 0 {
			continue // 그 사이 다른 writer 가 지웠다
		}
		r.Log("[reconcile] remove %s (%s)", d.SourcePath, removeReason(d.SourcePath, gated))
		removed = append(removed, DiffEntry{SourcePath: d.SourcePath, Team: d.Team, Kind: d.Kind, Change: ChangeRemoved, BodySHA: d.BodySHA})
	}
	return removed, nil
}

// unseen 은 색인에는 있는데 이번 스캔에서 보지 못한 문서다 (경로순).
func unseen(existing map[string]ingest.IndexedDoc, seen map[string]bool) []ingest.IndexedDoc {
	var out []ingest.IndexedDoc
	for rel, d := range existing {
		if !seen[rel] {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourcePath < out[j].SourcePath })
	return out
}

// removeReason 은 remove 로그의 사유다.
func removeReason(rel string, gated map[string]string) string {
	if reason, ok := gated[rel]; ok {
		return reason + " — 색인 범위 밖"
	}
	if !ingest.IsIndexable(rel) {
		return "색인 범위 밖 경로"
	}
	return "디스크에 없다 — 지웠거나 이름·철자가 바뀌었다"
}

// Converge 는 superseded_by 와 deprecated 를 정본대로 맞춘다. 정본은 개정판 frontmatter 의 supersedes 선언,
// 판정 원장(.cache/judge), cold 문서 조치 원장(.cache/archive)이다. 인덱스 값은 파생물이라 매 사이클 다시 맞춘다
// (인덱스를 지워도 같은 결과로 재구축된다). knowledge 가 밀리면 자기 날짜의 월별 인덱스로 옮겨 노화시킨다.
// 문서 하나가 실패해도 나머지 문서는 마저 맞추고, 실패는 모아 돌려준다 (실패한 문서는 다음 사이클이 다시 맞춘다).
func (r *Reconciler) Converge(ctx context.Context) (int, error) {
	indices, _, err := ingest.WorkspaceIndices(ctx, r.OS, r.Config.Workspace, r.Config.DefaultTeam)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	docs, err := ingest.FetchIndexedDocs(ctx, r.OS, indices, r.Config.Workspace)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	desired, deprecated, err := r.refreshFlags(docs)
	if err != nil {
		return 0, fmt.Errorf("converge: %w", err)
	}
	changed := 0
	var errs []error
	for _, d := range docs {
		n, err := r.convergeDoc(ctx, indices, d, desired[d.SourcePath], deprecated[d.SourcePath])
		changed += n
		if err != nil {
			errs = append(errs, fmt.Errorf("converge %s: %w", d.SourcePath, err))
		}
	}
	return changed, errors.Join(errs...)
}

// convergeDoc 은 문서 하나의 숨김 표시를 맞추고 바꾼 수를 돌려준다. 그 문서의 쓰기 락 안에서 — 데몬 밖의 Write 와 엇갈려
// 일부 청크만 표시가 바뀌지 않게.
func (r *Reconciler) convergeDoc(ctx context.Context, indices []string, d ingest.IndexedDoc, wantBy string, wantDep bool) (int, error) {
	if d.SupersededBy == wantBy && d.Deprecated == wantDep {
		return 0, nil
	}
	unlock, err := state.LockDoc(ctx, r.Config.MetaRepoRoot, d.SourcePath)
	if err != nil {
		return 0, err
	}
	defer unlock()
	changed := 0
	if d.SupersededBy != wantBy {
		if err := r.applySuperseded(ctx, indices, d, wantBy); err != nil {
			return changed, err
		}
		changed++
	}
	if d.Deprecated != wantDep {
		if err := opensearch.UpdateDoc(ctx, r.OS, indices, r.Config.Workspace, d.SourcePath, map[string]any{"deprecated": wantDep}); err != nil {
			return changed, err
		}
		changed++
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
	for team, judged := range judgedDocsByTeam(docs) {
		m, err := judge.SupersededMap(team, judged)
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

// judgedDocsByTeam 은 팀 → (경로 → 본문 sha·판정 범위) 다. 판정 원장이 팀별이고 판정은 episodic 끼리만 하므로
// (knowledge 는 유사도로 판정하지 않는다), 결론을 옮길 문서도 같은 팀의 episodic 에서, BestNeighbor 가 견주는 범위 안에서만 찾는다.
func judgedDocsByTeam(docs map[string]ingest.IndexedDoc) map[string]map[string]state.JudgedDoc {
	out := map[string]map[string]state.JudgedDoc{}
	for _, d := range docs {
		if d.Kind != model.KindEpisodic {
			continue
		}
		if out[d.Team] == nil {
			out[d.Team] = map[string]state.JudgedDoc{}
		}
		out[d.Team][d.SourcePath] = state.JudgedDoc{BodySHA: d.BodySHA, Scope: neighborScope(d.Repo, d.DocType)}
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
