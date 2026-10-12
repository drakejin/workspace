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
)

// TierDecision 은 tier 결과 한 줄이다.
type TierDecision struct {
	Index        string
	NewestEvent  string
	Action       TierAction
	Snapshot     string
	RemovedFiles []string // cold 로 넘어가 hot 에서 뺀 원본
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
		if err != nil {
			return decisions, err
		}
		decisions = append(decisions, d)
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
	return append(decisions, pruned...), err
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
	case newest != "" && !model.ParseTime(newest).Before(cutoff):
		d.Action = TierKept
		return d, nil
	case opt.DryRun:
		d.Action = TierWould
		return d, nil
	}
	// 순서: 쓰기 막기 → 문서 목록 → 스냅샷 → 목록(manifest) 기록 → 인덱스 삭제 → hot 원본 제거.
	// 쓰기를 막아 스냅샷과 목록이 정확히 같은 내용이 되고, 인덱스가 먼저 사라지므로 그 사이에 도는 데몬 사이클은
	// 남은 파일을 cold 소유로 보고 건드리지 않는다 (원본을 먼저 지우면 데몬이 tombstone 으로 cold store 청크까지 지운다).
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
	if err != nil {
		// 스냅샷까지 못 갔으면 인덱스를 그대로 hot 으로 돌려놓는다
		if uerr := setWriteBlock(context.WithoutCancel(ctx), c, name, false); uerr != nil {
			err = fmt.Errorf("%w (쓰기 막기 해제도 실패: %v)", err, uerr)
		}
		return d, err
	}
	if err := c.Do(ctx, http.MethodDelete, "/"+name, nil, nil, nil); err != nil {
		return d, fmt.Errorf("delete index %s: %w", name, err)
	}
	d.Action = TierMoved
	if !opt.KeepFiles {
		if d.RemovedFiles, err = removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs); err != nil {
			return d, err
		}
	}
	return d, nil
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

// recordManifest 는 스냅샷에 들어간 이 workspace 문서들을 팀별 목록에 남긴다.
func recordManifest(ctx context.Context, store Store, index, snapshot string, docs []sourceDoc) error {
	byTeam := map[string][]ManifestEntry{}
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
	var out []TierDecision
	for _, name := range names {
		idx, _ := naming.ParseIndex(name)
		docs, err := coldOwnedFiles(ctx, store, idx.Team, name, own)
		if err != nil {
			return out, err
		}
		removed, err := removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs)
		if err != nil {
			return out, err
		}
		if len(removed) > 0 {
			out = append(out, TierDecision{Index: name, Action: TierPruned, RemovedFiles: removed})
		}
	}
	return out, nil
}

type sourceDoc struct {
	team       string
	sourcePath string
	contentSHA string
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

// coldOwnedFiles 는 cold store 청크 JSONL 중 이 달 인덱스로 라우팅됐고 그 내용이 스냅샷에 들어 있는 문서다
// (이 workspace 의 cold store 만 보므로 다른 meta-repo 의 원본은 고르지 않는다).
func coldOwnedFiles(ctx context.Context, store Store, team, index string, own *Ownership) ([]sourceDoc, error) {
	keys, err := store.List(ctx, ChunksPrefix(team))
	if err != nil {
		return nil, err
	}
	var docs []sourceDoc
	for _, key := range keys {
		body, ok, err := store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("cold store %s: %w", key, err)
		}
		if !ok {
			continue
		}
		chunks, _ := DecodeChunks(body)
		if len(chunks) == 0 || naming.IndexFor(chunks[0]) != index {
			continue
		}
		owned, err := own.Owns(ctx, team, index, chunks[0].SourcePath, chunks[0].ContentSHA)
		if err != nil {
			return nil, err
		}
		if !owned {
			continue
		}
		docs = append(docs, sourceDoc{team: chunks[0].Team, sourcePath: chunks[0].SourcePath, contentSHA: chunks[0].ContentSHA})
	}
	return docs, nil
}

// removeHotOriginals 는 hot(working tree) 원본을 지운다. 이 meta-repo 의 docs/ 원본만,
// 그리고 지금 내용이 색인·cold store 에 있던 내용과 같을 때만 지운다 (그 사이 고친 문서는 남긴다).
// git 이 추적하는 파일은 git rm 으로 지워 다음 커밋에 들어가게 한다.
func removeHotOriginals(ctx context.Context, store Store, root string, docs []sourceDoc) ([]string, error) {
	var removed []string
	for _, d := range docs {
		if !strings.HasPrefix(d.sourcePath, "docs/") {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(d.sourcePath))
		content, err := os.ReadFile(abs)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("read %s: %w", d.sourcePath, err)
		}
		if document.SHA256(string(content)) != d.contentSHA {
			continue
		}
		raw, ok, err := store.Get(ctx, RawKey(d.team, d.sourcePath))
		if err != nil {
			return removed, fmt.Errorf("cold store raw %s: %w", d.sourcePath, err)
		}
		if !ok || document.SHA256(raw) != d.contentSHA {
			continue // cold store 에 같은 원본이 없으면 지우지 않는다
		}
		if err := gitRemove(ctx, root, d.sourcePath); err != nil {
			return removed, err
		}
		removed = append(removed, d.sourcePath)
	}
	return removed, nil
}

func gitRemove(ctx context.Context, root, rel string) error {
	cmd := exec.CommandContext(ctx, "git", "rm", "-q", "-f", "--", rel)
	cmd.Dir = root
	if err := cmd.Run(); err == nil {
		return nil
	}
	// git 밖이거나 추적하지 않는 파일
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", rel, err)
	}
	return nil
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
