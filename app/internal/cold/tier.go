package cold

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// TierAction 은 인덱스 하나에 대한 결정이다.
type TierAction string

const (
	TierMoved    TierAction = "tiered"
	TierWould    TierAction = "would-tier"
	TierKept     TierAction = "kept-hot"
	TierRestored TierAction = "skipped-restored"
	TierPruned   TierAction = "pruned"
	// TierDropped 는 문서가 하나도 없는 지난달 인덱스를 스냅샷 없이 지운 것이다 (그 달을 cold 가 가졌다고 남기지 않는다).
	TierDropped   TierAction = "dropped-empty"
	TierWouldDrop TierAction = "would-drop-empty"
	// TierKeptUncommitted 는 cold 로 간 달의 hot 원본을 git 이 되살릴 수 없어(커밋하지 않았거나 커밋 뒤 고쳤다) 남긴 것이다.
	TierKeptUncommitted TierAction = "kept-uncommitted"
)

// TierDecision 은 tier 결과 한 줄이다.
type TierDecision struct {
	Index        string
	NewestEvent  string
	Action       TierAction
	Snapshot     string
	RemovedFiles []string // cold 로 넘어가 hot 에서 뺀 원본
	KeptFiles    []string // cold 로 넘어갔지만 git 이 되살릴 수 없어 hot 에 남긴 원본 (커밋하면 다음 tier 가 뺀다)
	// GitRemoved 면 RemovedFiles 를 git rm 으로 지웠다(커밋해야 한다). 아니면 git 이 다루지 않는 meta-repo 라 파일만 지웠다
	GitRemoved bool
}

// TierOptions 는 tier 실행 옵션이다.
type TierOptions struct {
	Now       time.Time
	DryRun    bool
	Team      string // 비면 모든 팀
	KeepFiles bool   // hot 원본을 남긴다 (기본: 스냅샷 뒤 hot 에서 뺀다)
}

// Tier 는 가장 최신 문서도 hot 기간을 넘긴 월별 인덱스를 스냅샷 → hot 원본 제거 → 인덱스 삭제한다.
// ISM 은 인덱스 생성일 기준이라 오래된 문서를 늦게 적재한 인덱스는 이 함수가 맡고, ISM 이 지운 인덱스의
// 남은 hot 원본도 여기서 정리한다. 원본은 지우기 전에 cold store raw 에 같은 내용(sha)이 있는지 확인한다.
func Tier(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, opt TierOptions) ([]TierDecision, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	pattern := naming.AllPattern
	if opt.Team != "" {
		pattern = naming.TeamPattern(opt.Team)
	}
	live, err := LiveIndices(ctx, c, pattern)
	if err != nil {
		return nil, err
	}
	// 이 workspace 의 팀 인덱스만 — 같은 클러스터를 쓰는 다른 meta-repo 의 인덱스를 스냅샷·삭제하지 않는다
	owned, err := opensearch.WorkspaceTeams(ctx, c, cfg.Workspace)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(live))
	for name := range live {
		idx, ok := naming.ParseIndex(name)
		if ok && !idx.Knowledge && owned[idx.Team] && (opt.Team == "" || idx.Team == opt.Team) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	cutoff := opt.Now.Add(-cfg.HotWindow())
	decisions := make([]TierDecision, 0, len(names))
	for _, name := range names {
		d, err := tierOne(ctx, c, store, cfg, name, cutoff, opt)
		if d.Action != "" {
			decisions = append(decisions, d) // 실패했어도 이미 한 일(인덱스 삭제, hot 에서 뺀 원본)은 알린다
		}
		if err != nil {
			return decisions, err
		}
	}
	if opt.KeepFiles || opt.DryRun {
		return decisions, nil
	}
	// 방금 tier 한 인덱스는 더 이상 live 가 아니다 — 목록을 다시 읽어 그 달의 남은 원본까지 정리한다
	live, err = LiveIndices(ctx, c, pattern)
	if err != nil {
		return decisions, err
	}
	pruned, err := pruneArchived(ctx, c, store, cfg, opt.Team, live)
	return append(decisions, withoutReportedKeeps(pruned, decisions)...), err
}

// withoutReportedKeeps 는 prune 결정에서 방금 tier 한 달의 결정이 이미 알린 남긴 원본을 뺀다 — 방금 옮긴 달도 prune 이 다시
// 보므로 같은 원본을 두 번 알리게 된다. 남는 것이 없는 kept-uncommitted 결정은 버린다.
func withoutReportedKeeps(pruned, reported []TierDecision) []TierDecision {
	seen := map[string]bool{}
	for _, d := range reported {
		for _, f := range d.KeptFiles {
			seen[d.Index+"\x00"+f] = true
		}
	}
	out := make([]TierDecision, 0, len(pruned))
	for _, d := range pruned {
		var kept []string
		for _, f := range d.KeptFiles {
			if !seen[d.Index+"\x00"+f] {
				kept = append(kept, f)
			}
		}
		if len(d.RemovedFiles) == 0 && len(kept) == 0 {
			continue
		}
		next := d
		next.KeptFiles = kept
		out = append(out, next)
	}
	return out
}

func tierOne(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, name string, cutoff time.Time, opt TierOptions) (TierDecision, error) {
	settings, err := opensearch.Settings(ctx, c, name)
	if err != nil {
		return TierDecision{}, err
	}
	newest, err := newestEventTime(ctx, c, name)
	if err != nil {
		return TierDecision{}, err
	}
	d := TierDecision{Index: name, NewestEvent: newest}
	switch {
	case opensearch.IsUnmanaged(settings[name]):
		d.Action = TierRestored
		return d, nil
	case newest == "":
		return dropEmpty(ctx, c, name, cutoff, opt.DryRun, d)
	case !model.ParseTime(newest).Before(cutoff):
		d.Action = TierKept
		return d, nil
	case opt.DryRun:
		d.Action = TierWould
		return d, nil
	}
	return moveToCold(ctx, c, store, cfg, opt, d)
}

// dropEmpty 는 문서가 하나도 없는 인덱스다 (그 달의 유일한 문서를 지우거나 옮겼다). 스냅샷하지 않는다 — 빈 스냅샷은
// 그 달을 cold 가 가졌다는 표시가 되어, 그 달에 나중에 쓴 hot 문서까지 스냅샷 없이 cold 소유로 읽힌다.
// 달(인덱스 이름의 연.월)이 hot 기간을 다 지났으면 스냅샷 없이 지우고 (ISM 이 빈 인덱스를 스냅샷하지 않게), 아니면 둔다.
func dropEmpty(ctx context.Context, c opensearch.Client, name string, cutoff time.Time, dryRun bool, d TierDecision) (TierDecision, error) {
	idx, _ := naming.ParseIndex(name)
	switch {
	case idx.MonthStart.AddDate(0, 1, 0).After(cutoff):
		d.Action = TierKept
		return d, nil
	case dryRun:
		d.Action = TierWouldDrop
		return d, nil
	}
	// 쓰기를 막고 다시 센다 — 그 사이 들어온 문서가 있으면 지우지 않는다 (다음 tier 가 스냅샷과 함께 옮긴다)
	if err := setWriteBlock(ctx, c, name, true); err != nil {
		return d, err
	}
	n, err := opensearch.Count(ctx, c, name, map[string]any{"match_all": map[string]any{}})
	if err != nil {
		return d, unblockAfter(ctx, c, name, fmt.Errorf("count %s: %w", name, err))
	}
	if n > 0 {
		d.Action = TierKept
		return d, unblockAfter(ctx, c, name, nil)
	}
	if err := deleteIndex(ctx, c, name); err != nil {
		return d, unblockAfter(ctx, c, name, err)
	}
	d.Action = TierDropped
	return d, nil
}

// moveToCold 는 순서대로: 쓰기 막기 → 문서 목록 → 스냅샷 → 목록(manifest) 기록 → 인덱스 삭제 → hot 원본 제거.
// 쓰기를 막아 스냅샷과 목록이 정확히 같은 내용이 되고, 인덱스가 먼저 사라지므로 그 사이에 도는 데몬 사이클은
// 남은 파일을 cold 소유로 보고 건드리지 않는다 (원본을 먼저 지우면 데몬이 tombstone 으로 cold store 청크까지 지운다).
func moveToCold(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, opt TierOptions, d TierDecision) (TierDecision, error) {
	name := d.Index
	if err := setWriteBlock(ctx, c, name, true); err != nil {
		return d, err
	}
	docs, err := sourcesIn(ctx, c, name, cfg.Workspace)
	if err == nil {
		d.Snapshot, err = SnapshotIndex(ctx, c, cfg, name, opt.Now)
	}
	if err == nil {
		err = recordManifest(ctx, store, name, d.Snapshot, docs)
	}
	if err == nil {
		err = deleteIndex(ctx, c, name)
	}
	if err != nil {
		// 인덱스를 지우지 못했으면 그대로 hot 으로 돌려놓는다 (스냅샷이 남았어도 다음 tier 가 다시 뜬다)
		return d, unblockAfter(ctx, c, name, err)
	}
	d.Action = TierMoved
	if !opt.KeepFiles {
		if d.RemovedFiles, d.KeptFiles, d.GitRemoved, err = removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs); err != nil {
			return d, err
		}
	}
	return d, nil
}

func deleteIndex(ctx context.Context, c opensearch.Client, name string) error {
	if err := c.Do(ctx, http.MethodDelete, "/"+name, nil, nil, nil); err != nil {
		return fmt.Errorf("delete index %s: %w", name, err)
	}
	return nil
}

// unblockAfter 는 tier 가 인덱스를 지우지 못하고 멈출 때 쓰기 막기를 푼다 — 막힌 채 남으면 데몬 쓰기가 계속 실패한다.
// 풀기까지 실패하면 두 에러를 함께 돌려준다.
func unblockAfter(ctx context.Context, c opensearch.Client, name string, cause error) error {
	if uerr := setWriteBlock(context.WithoutCancel(ctx), c, name, false); uerr != nil {
		if cause == nil {
			return uerr
		}
		return fmt.Errorf("%w (쓰기 막기 해제도 실패: %v)", cause, uerr)
	}
	return cause
}

func setWriteBlock(ctx context.Context, c opensearch.Client, index string, on bool) error {
	var value any = true
	if !on {
		value = nil // 설정을 지워 기본(쓰기 허용)으로
	}
	if err := c.Do(ctx, http.MethodPut, "/"+index+"/_settings", map[string]any{"index.blocks.write": value}, nil, nil); err != nil {
		return fmt.Errorf("write block %s: %w", index, err)
	}
	return nil
}

// RecordIndexManifest 는 index 의 지금 내용(이 workspace 문서)을 snapshot 의 목록으로 남긴다. 스냅샷을 뜨는 동안
// 인덱스에 쓰는 이가 없어야 정확하다 (tier 는 쓰기를 막고, archive 는 alias 없이 잠깐 복원한 인덱스를 쓴다).
func RecordIndexManifest(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, index, snapshot string) error {
	docs, err := sourcesIn(ctx, c, index, cfg.Workspace)
	if err != nil {
		return err
	}
	return recordManifest(ctx, store, index, snapshot, docs)
}

// recordManifest 는 스냅샷에 들어간 이 workspace 문서들을 팀별 목록에 남긴다. 문서가 없어도 인덱스 팀의 목록에
// 그 스냅샷의 표시 줄을 남긴다 (빈 스냅샷이 ISM 스냅샷으로 읽히지 않게).
func recordManifest(ctx context.Context, store Store, index, snapshot string, docs []sourceDoc) error {
	byTeam := map[string][]ManifestEntry{}
	if idx, ok := naming.ParseIndex(index); ok {
		byTeam[idx.Team] = nil
	}
	for _, d := range docs {
		byTeam[d.team] = append(byTeam[d.team], ManifestEntry{SourcePath: d.sourcePath, ContentSHA: d.contentSHA})
	}
	for team, entries := range byTeam {
		if err := AppendManifest(ctx, store, team, index, snapshot, entries); err != nil {
			return err
		}
	}
	return nil
}

// pruneArchived 는 ISM 이 이미 cold 로 보낸(스냅샷에 있고 지금 열려 있지 않은) 달의 hot 원본을 정리한다.
// 팀의 청크 JSONL 은 한 번만 읽어 달별로 나눈다 — 달마다 다시 읽으면 S3 cold store 의 GET 이 "달 수 × 문서 수" 가 된다.
func pruneArchived(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, team string, live map[string]bool) ([]TierDecision, error) {
	archived, err := ArchivedMonths(ctx, c, cfg)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(archived))
	for name := range archived {
		idx, _ := naming.ParseIndex(name)
		if !live[name] && (team == "" || idx.Team == team) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	own := NewOwnership(archived, store)
	candidates := map[string]map[string][]sourceDoc{} // 팀 → 인덱스 → hot 원본이 남은 문서
	var out []TierDecision
	for _, name := range names {
		idx, _ := naming.ParseIndex(name)
		byIndex, seen := candidates[idx.Team]
		if !seen {
			if byIndex, err = hotCandidates(ctx, store, cfg.MetaRepoRoot, idx.Team); err != nil {
				return out, err
			}
			candidates[idx.Team] = byIndex
		}
		docs, err := coldOwned(ctx, own, idx.Team, name, byIndex[name])
		if err != nil {
			return out, err
		}
		removed, kept, byGit, err := removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs)
		if err != nil {
			return out, err
		}
		switch {
		case len(removed) > 0:
			out = append(out, TierDecision{Index: name, Action: TierPruned, RemovedFiles: removed, KeptFiles: kept, GitRemoved: byGit})
		case len(kept) > 0:
			out = append(out, TierDecision{Index: name, Action: TierKeptUncommitted, KeptFiles: kept})
		}
	}
	return out, nil
}

type sourceDoc struct {
	team       string
	sourcePath string
	contentSHA string
	ingestedAt string // cold store 청크에서 읽은 적재 시각 (hotCandidates 만 채운다)
}

// sourcesIn 은 인덱스 안의 이 workspace 문서다 (다른 meta-repo 의 원본은 건드리지 않는다).
func sourcesIn(ctx context.Context, c opensearch.Client, index, workspace string) ([]sourceDoc, error) {
	var docs []sourceDoc
	err := opensearch.Scan(ctx, c, index, opensearch.FirstChunks(workspace), []string{"team", "source_path", "content_sha"}, opensearch.ChunkSort,
		func(h opensearch.Hit[model.Chunk]) error {
			docs = append(docs, sourceDoc{team: h.Source.Team, sourcePath: h.Source.SourcePath, contentSHA: h.Source.ContentSHA})
			return nil
		})
	return docs, err
}

// hotCandidates 는 팀의 cold store 청크 JSONL 중 hot 원본이 아직 남은 문서를 그 청크가 라우팅된 인덱스별로 묶는다.
// 청크 JSONL 은 한 번씩만 읽고, 원본이 없는 문서는 읽지도 않는다 (이 workspace 의 cold store 만 보므로 다른 meta-repo 의
// 원본은 고르지 않는다).
func hotCandidates(ctx context.Context, store Store, root, team string) (map[string][]sourceDoc, error) {
	keys, err := store.List(ctx, ChunksPrefix(team))
	if err != nil {
		return nil, err
	}
	out := map[string][]sourceDoc{}
	for _, key := range keys {
		rel := strings.TrimSuffix(strings.TrimPrefix(key, ChunksPrefix(team)), ".jsonl")
		if !hotOriginalExists(root, rel) {
			continue
		}
		body, ok, err := store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("cold store %s: %w", key, err)
		}
		chunks, _ := DecodeChunks(body)
		if !ok || len(chunks) == 0 {
			continue
		}
		head := chunks[0]
		index := naming.IndexFor(head)
		out[index] = append(out[index], sourceDoc{team: head.Team, sourcePath: head.SourcePath, contentSHA: head.ContentSHA, ingestedAt: head.IngestedAt})
	}
	return out, nil
}

// coldOwned 는 docs 중 index 의 스냅샷이 그 내용을 담은 문서다. hotCandidates 가 읽어 둔 적재 시각을 판단기에 넘겨
// ISM 달에서 같은 청크 JSONL 을 다시 읽지 않게 한다.
func coldOwned(ctx context.Context, own *Ownership, team, index string, docs []sourceDoc) ([]sourceDoc, error) {
	var out []sourceDoc
	for _, d := range docs {
		own.knowIngested(team, d.sourcePath, d.contentSHA, d.ingestedAt)
		owned, err := own.Owns(ctx, team, index, d.sourcePath, d.contentSHA)
		if err != nil {
			return nil, err
		}
		if owned {
			out = append(out, d)
		}
	}
	return out, nil
}

// hotOriginalPrefix 는 tier 가 hot 에서 빼는 원본의 위치다 (이 meta-repo 의 docs/ 만).
const hotOriginalPrefix = "docs/"

// hotOriginalExists 는 rel 이 tier 가 빼는 원본이고 working tree 에 아직 있는가다.
func hotOriginalExists(root, rel string) bool {
	return strings.HasPrefix(rel, hotOriginalPrefix) && onDiskExists(root, rel)
}

// onDiskExists 는 문서 rel(source_path, NFC)의 파일이 working tree 에 있는가다. source_path 로 열리지 않으면 디스크의 철자로
// 다시 본다 — 정규화를 구분하는 파일 시스템(Linux)에서는 NFD 이름(Finder 가 만든 한글 이름)의 파일이 NFC 경로로 열리지 않는다.
func onDiskExists(root, rel string) bool {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(document.OnDiskRel(root, rel))))
	return err == nil
}

// removeHotOriginals 는 hot(working tree) 원본을 지운다. 이 meta-repo 의 docs/ 원본만,
// 그리고 지금 내용이 색인·cold store 에 있던 내용과 같을 때만 지운다 (그 사이 고친 문서는 남긴다).
// git 레포면 git 이 되살릴 수 있는 원본(추적하고 HEAD 와 같다)만 git rm 으로 지워 다음 커밋에 들어가게 하고, 추적하지 않거나
// 커밋 뒤 고친 원본은 남겨 kept 로 알린다 — 지우면 그 내용은 .gitignore 된 cold store 에만 남는다. git 레포가 아니면
// 되살릴 히스토리가 처음부터 없으므로 cold store raw 확인만으로 지운다. 파일은 디스크의 철자로 열고 git 에 넘긴다 (NFD 이름).
// byGit 은 git 으로 다루는 meta-repo 라 removed 를 git rm 으로 지웠다는 뜻이다 (커밋해야 한다).
func removeHotOriginals(ctx context.Context, store Store, root string, docs []sourceDoc) (removed, kept []string, byGit bool, err error) {
	if len(docs) == 0 {
		return nil, nil, false, nil
	}
	inGit, err := gitWorkTree(ctx, root)
	if err != nil {
		return nil, nil, false, err
	}
	for _, d := range docs {
		disk := document.OnDiskRel(root, d.sourcePath)
		ok, err := verifiedCopy(ctx, store, root, d, disk)
		if err != nil {
			return removed, kept, inGit, err
		}
		if !ok {
			continue
		}
		if keptForGit(ctx, root, disk, inGit) {
			kept = append(kept, d.sourcePath)
			continue
		}
		if err := removeOriginal(ctx, root, disk, inGit); err != nil {
			return removed, kept, inGit, err
		}
		removed = append(removed, d.sourcePath)
	}
	return removed, kept, inGit, nil
}

// keptForGit 는 tier 가 cold 로 간 원본(디스크의 철자 disk)을 git 때문에 hot 에 남기는가다 — git 으로 다루는 meta-repo 인데
// git 이 그 원본을 되살릴 수 없다 (추적하지 않거나 HEAD 와 다르다).
func keptForGit(ctx context.Context, root, disk string, inGit bool) bool {
	return inGit && !committedAsIs(ctx, root, disk)
}

// KeptByGit 는 tier 가 cold 로 간 hot 원본 rel(source_path)을 지우지 않고 hot 에 남기는가다 (removeHotOriginals 와 같은 규칙):
// 커밋하지 않았거나 커밋 뒤 고친 원본이다 — 커밋하면 다음 tier 가 뺀다. 데몬은 이런 원본을 tier 가 정리할 cold 소유와 따로 센다.
// git 으로 확인하지 못하면 tier 도 지우지 않으므로 남긴다고 보고 오류를 함께 돌려준다.
func KeptByGit(ctx context.Context, root, rel string) (bool, error) {
	inGit, err := gitWorkTree(ctx, root)
	if err != nil {
		return true, err
	}
	return keptForGit(ctx, root, document.OnDiskRel(root, rel), inGit), nil
}

// gitStateNone 은 root 가 git 레포 안에 없다는 GitState 값이다.
const gitStateNone = "no-git"

// GitState 는 KeptByGit 의 답을 바꿀 수 있는 git 상태다 — HEAD 커밋과 index 파일(무엇을 추적하는가)의 크기·수정 시각. 원본의
// 내용(stat)과 이 값이 그대로면 KeptByGit 의 답도 그대로라, 데몬은 사이클마다 원본마다 git 을 다시 부르지 않고 이 값만 견준다.
// git 레포가 아니면 그렇다는 값이고, git 이 돌지 않거나 레포를 거부하면 KeptByGit 처럼 오류다.
func GitState(ctx context.Context, root string) (string, error) {
	out, err := gitOutput(ctx, root, "rev-parse", "--git-path", "index")
	if err != nil && strings.Contains(out, notAGitRepository) {
		return gitStateNone, nil
	}
	if err != nil {
		return "", gitCheckError(root, err, out)
	}
	head, _ := gitOutput(ctx, root, "rev-parse", "-q", "--verify", "HEAD") // 커밋이 아직 없으면 빈 값
	index := strings.TrimSpace(out)
	if !filepath.IsAbs(index) {
		index = filepath.Join(root, index) // --git-path 는 root 에서 본 상대 경로일 수 있다
	}
	stamp := "no-index" // 아직 아무것도 add 하지 않았다
	if info, err := os.Stat(index); err == nil {
		stamp = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
	}
	return strings.TrimSpace(head) + " " + stamp, nil
}

// verifiedCopy 는 d 가 tier 가 빼는 원본이고, 지금 파일(디스크의 철자 disk) 내용과 cold store raw 가 모두 색인에 있던 내용(sha)과
// 같은가다.
func verifiedCopy(ctx context.Context, store Store, root string, d sourceDoc, disk string) (bool, error) {
	if !strings.HasPrefix(d.sourcePath, hotOriginalPrefix) {
		return false, nil
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(disk)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", d.sourcePath, err)
	}
	if document.SHA256(string(content)) != d.contentSHA {
		return false, nil
	}
	raw, ok, err := store.Get(ctx, RawKey(d.team, d.sourcePath))
	if err != nil {
		return false, fmt.Errorf("cold store raw %s: %w", d.sourcePath, err)
	}
	return ok && document.SHA256(raw) == d.contentSHA, nil // cold store 에 같은 원본이 없으면 지우지 않는다
}

// notAGitRepository 는 git 레포 밖에서 git 이 내는 문구다 (gitOutput 은 LC_ALL=C 로 돌려 번역되지 않는다).
const notAGitRepository = "not a git repository"

// gitWorkTree 는 root 의 원본을 git 으로 다루는가다: root 가 git 레포의 꼭대기이거나, 바깥 레포가 root 아래 파일을 추적한다
// (모노레포의 한 디렉토리). 바깥 레포가 root 를 추적하지 않으면(홈 디렉토리의 dotfiles 레포 등) git 레포가 아닌 meta-repo 로
// 본다 — 그 레포에 커밋하라는 안내는 맞지 않다. git 이 레포를 거부하면(소유자가 달라 믿지 않는다 등) 커밋 여부를 알 수 없어
// 에러다 — git 레포가 아니라고 보고 지우면 커밋 안 된 원본이 cold store 에만 남는다.
func gitWorkTree(ctx context.Context, root string) (bool, error) {
	prefix, err := gitOutput(ctx, root, "rev-parse", "--show-prefix")
	if err != nil && strings.Contains(prefix, notAGitRepository) {
		return false, nil
	}
	if err != nil {
		return false, gitCheckError(root, err, prefix)
	}
	if strings.TrimSpace(prefix) == "" {
		return true, nil // root 가 레포의 꼭대기다
	}
	tracked, err := gitOutput(ctx, root, "ls-files", "--", ".")
	if err != nil {
		return false, gitCheckError(root, err, tracked)
	}
	return strings.TrimSpace(tracked) != "", nil
}

func gitCheckError(root string, err error, out string) error {
	return fmt.Errorf("git 으로 %s 를 확인하지 못해 hot 원본을 지우지 않았습니다: %w %s", root, err, strings.TrimSpace(out))
}

// committedAsIs 는 rel(디스크의 철자)을 git 이 추적하고 지금 내용이 HEAD 와 같은가다 (지워도 git 히스토리에서 되살릴 수 있다).
// 확인하지 못하면 아니라고 본다 — 남기는 쪽이 안전하다.
func committedAsIs(ctx context.Context, root, rel string) bool {
	if _, err := gitOutput(ctx, root, "ls-files", "--error-unmatch", "--", rel); err != nil {
		return false
	}
	_, err := gitOutput(ctx, root, "diff", "--quiet", "HEAD", "--", rel)
	return err == nil
}

// removeOriginal 은 원본 하나(디스크의 철자 rel)를 지운다. git 레포면 git rm 으로 지워 다음 커밋에 들어가게 한다.
func removeOriginal(ctx context.Context, root, rel string, inGit bool) error {
	if inGit {
		if out, err := gitOutput(ctx, root, "rm", "-q", "-f", "--", rel); err != nil {
			return fmt.Errorf("git rm %s: %w %s", rel, err, strings.TrimSpace(out))
		}
		return nil
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", rel, err)
	}
	return nil
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "LC_ALL=C") // 문구(notAGitRepository)를 번역하지 않게
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func newestEventTime(ctx context.Context, c opensearch.Client, index string) (string, error) {
	var out struct {
		Aggregations struct {
			Newest struct {
				ValueAsString string `json:"value_as_string"`
			} `json:"newest"`
		} `json:"aggregations"`
	}
	body := map[string]any{"size": 0, "aggs": map[string]any{"newest": map[string]any{"max": map[string]any{"field": "event_time"}}}}
	if err := c.Do(ctx, http.MethodPost, "/"+index+"/_search", body, nil, &out); err != nil {
		return "", fmt.Errorf("newest event %s: %w", index, err)
	}
	return out.Aggregations.Newest.ValueAsString, nil
}
