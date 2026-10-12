package cold

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/document"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Index 는 cold(스냅샷)에 있는 인덱스 하나다. 같은 인덱스가 여러 스냅샷에 있으면 최신 것.
type Index struct {
	Name         string
	Snapshot     string
	SnapshotTime string
	Restored     bool // 지금 hot 에 복원돼 있는가
}

type snapshotInfo struct {
	Snapshot  string   `json:"snapshot"`
	Indices   []string `json:"indices"`
	State     string   `json:"state"`
	StartTime string   `json:"start_time"`
}

func snapshots(ctx context.Context, c opensearch.Client, repo string) ([]snapshotInfo, error) {
	var out struct {
		Snapshots []snapshotInfo `json:"snapshots"`
	}
	if err := c.Do(ctx, http.MethodGet, "/_snapshot/"+repo+"/_all", nil, nil, &out); err != nil {
		return nil, err
	}
	ok := out.Snapshots[:0]
	for _, s := range out.Snapshots {
		if s.State == "SUCCESS" {
			ok = append(ok, s)
		}
	}
	return ok, nil
}

// LiveIndices 는 열려 있는 인덱스 이름 집합이다.
func LiveIndices(ctx context.Context, c opensearch.Client, pattern string) (map[string]bool, error) {
	var rows []struct {
		Index string `json:"index"`
	}
	err := c.Do(ctx, http.MethodGet, "/_cat/indices/"+pattern, nil, url.Values{"format": {"json"}, "h": {"index"}, "expand_wildcards": {"open"}}, &rows)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(rows))
	for _, r := range rows {
		live[r.Index] = true
	}
	return live, nil
}

// ListCold 는 팀의 cold 인덱스 목록이다.
func ListCold(ctx context.Context, c opensearch.Client, cfg config.Config, team string) ([]Index, error) {
	snaps, err := snapshots(ctx, c, cfg.Snapshot.Repository)
	if err != nil {
		return nil, err
	}
	live, err := LiveIndices(ctx, c, naming.TeamPattern(team))
	if err != nil {
		return nil, err
	}
	latest := map[string]Index{}
	for _, s := range snaps {
		for _, name := range s.Indices {
			// mem-ads-* 는 mem-ads-data-* 도 잡으므로 이름에서 파싱한 team 으로 거른다
			idx, ok := naming.ParseIndex(name)
			if !ok || idx.Team != team || idx.Knowledge {
				continue
			}
			if prev, seen := latest[name]; !seen || prev.SnapshotTime < s.StartTime {
				latest[name] = Index{Name: name, Snapshot: s.Snapshot, SnapshotTime: s.StartTime, Restored: live[name]}
			}
		}
	}
	out := make([]Index, 0, len(latest))
	for _, v := range latest {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ArchivedMonths 는 스냅샷에 들어간 episodic 월별 인덱스 집합이다(복원 여부 무관).
// 데몬은 이 달의 hot 파일을 다시 색인하지 않는다 — cold 가 소유한다.
func ArchivedMonths(ctx context.Context, c opensearch.Client, cfg config.Config) (map[string]bool, error) {
	snaps, err := snapshots(ctx, c, cfg.Snapshot.Repository)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range snaps {
		for _, name := range s.Indices {
			if naming.IsMonthly(name) {
				out[name] = true
			}
		}
	}
	return out, nil
}

// Restore 는 스냅샷에서 cold 인덱스 하나를 hot 으로 되돌린다.
// 복원본은 ISM 자동 관리에서 뺀다 — 원래 생성일을 유지하므로 그대로 두면 ISM 이 바로 다시 cold 로 보낸다.
func Restore(ctx context.Context, c opensearch.Client, cfg config.Config, name string) (Index, error) {
	idx, ok := naming.ParseIndex(name)
	if !ok || idx.Knowledge {
		return Index{}, fmt.Errorf("episodic 월별 인덱스 이름이 아닙니다: %s", name)
	}
	list, err := ListCold(ctx, c, cfg, idx.Team)
	if err != nil {
		return Index{}, err
	}
	for _, target := range list {
		if target.Name != name {
			continue
		}
		if target.Restored {
			return target, nil
		}
		body := map[string]any{
			"indices":              name,
			"include_global_state": false,
			"include_aliases":      true,
			"index_settings":       map[string]any{opensearch.AutoManageSetting: false},
		}
		path := "/_snapshot/" + cfg.Snapshot.Repository + "/" + target.Snapshot + "/_restore"
		if err := c.Do(ctx, http.MethodPost, path, body, url.Values{"wait_for_completion": {"true"}}, nil); err != nil {
			return Index{}, err
		}
		target.Restored = true
		return target, nil
	}
	return Index{}, fmt.Errorf("cold 에 %s 가 없습니다. list_cold 로 먼저 확인하세요", name)
}

// TierAction 은 인덱스 하나에 대한 결정이다.
type TierAction string

const (
	TierMoved    TierAction = "tiered"
	TierWould    TierAction = "would-tier"
	TierKept     TierAction = "kept-hot"
	TierRestored TierAction = "skipped-restored"
)

// TierDecision 은 tier 결과 한 줄이다.
type TierDecision struct {
	Index        string
	NewestEvent  string
	Action       TierAction
	RemovedFiles []string // cold 로 넘어가 hot 에서 뺀 원본
}

// TierOptions 는 tier 실행 옵션이다.
type TierOptions struct {
	Now       time.Time
	DryRun    bool
	Team      string // 비면 모든 팀
	KeepFiles bool   // hot 원본을 남긴다 (기본: 스냅샷 뒤 hot 에서 뺀다)
}

// Tier 는 가장 최신 문서도 hot 기간을 넘긴 episodic 월별 인덱스를 스냅샷 → hot 원본 제거 → 인덱스 삭제한다.
// ISM 은 인덱스 생성일 기준이라 오래된 문서를 늦게 적재한 인덱스는 이 함수가 맡고, ISM 이 지운 인덱스의
// 남은 hot 원본도 여기서 정리한다. 원본은 지우기 전에 cold store raw 에 같은 내용(sha)이 있는지 확인한다.
func Tier(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, opt TierOptions) ([]TierDecision, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	cutoff := opt.Now.Add(-cfg.HotWindow())
	pattern := naming.AllPattern
	if opt.Team != "" {
		pattern = naming.TeamPattern(opt.Team)
	}
	live, err := LiveIndices(ctx, c, pattern)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(live))
	for name := range live {
		idx, ok := naming.ParseIndex(name)
		if ok && !idx.Knowledge && (opt.Team == "" || idx.Team == opt.Team) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	decisions := make([]TierDecision, 0, len(names))
	for _, name := range names {
		d, err := tierOne(ctx, c, store, cfg, name, cutoff, opt)
		if err != nil {
			return decisions, err
		}
		decisions = append(decisions, d)
	}
	// 방금 tier 한 인덱스는 더 이상 live 가 아니다 — 목록을 다시 읽어 그 달의 남은 원본까지 정리한다
	live, err = LiveIndices(ctx, c, pattern)
	if err != nil {
		return decisions, err
	}
	pruned, err := pruneArchived(ctx, c, store, cfg, opt, live)
	if err != nil {
		return decisions, err
	}
	return append(decisions, pruned...), nil
}

func tierOne(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, name string, cutoff time.Time, opt TierOptions) (TierDecision, error) {
	restored, err := isRestored(ctx, c, name)
	if err != nil {
		return TierDecision{}, err
	}
	newest, err := newestEventTime(ctx, c, name)
	if err != nil {
		return TierDecision{}, err
	}
	d := TierDecision{Index: name, NewestEvent: newest}
	switch {
	case restored:
		d.Action = TierRestored
		return d, nil
	case newest != "" && !parseTime(newest).Before(cutoff):
		d.Action = TierKept
		return d, nil
	case opt.DryRun:
		d.Action = TierWould
		return d, nil
	}
	docs, err := sourcesIn(ctx, c, name, cfg.Workspace)
	if err != nil {
		return d, err
	}
	// 같은 초에 tier 가 두 번 돌아도 겹치지 않게 나노초까지 쓴다
	snapshot := fmt.Sprintf("cold-%s-%s-%d", name, strings.ToLower(opt.Now.UTC().Format("20060102t150405")), time.Now().UnixNano()%1_000_000_000)
	body := map[string]any{"indices": name, "include_global_state": false}
	if err := c.Do(ctx, http.MethodPut, "/_snapshot/"+cfg.Snapshot.Repository+"/"+snapshot, body, url.Values{"wait_for_completion": {"true"}}, nil); err != nil {
		return d, err
	}
	if !opt.KeepFiles {
		removed, err := removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs)
		if err != nil {
			return d, err
		}
		d.RemovedFiles = removed
	}
	if err := c.Do(ctx, http.MethodDelete, "/"+name, nil, nil, nil); err != nil {
		return d, err
	}
	d.Action = TierMoved
	return d, nil
}

// pruneArchived 는 ISM 이 이미 cold 로 보낸(스냅샷에 있고 지금 열려 있지 않은) 달의 hot 원본을 정리한다.
func pruneArchived(ctx context.Context, c opensearch.Client, store Store, cfg config.Config, opt TierOptions, live map[string]bool) ([]TierDecision, error) {
	if opt.KeepFiles || opt.DryRun {
		return nil, nil
	}
	archived, err := ArchivedMonths(ctx, c, cfg)
	if err != nil {
		return nil, err
	}
	var out []TierDecision
	for name := range archived {
		idx, _ := naming.ParseIndex(name)
		if live[name] || (opt.Team != "" && idx.Team != opt.Team) {
			continue
		}
		docs, err := coldOwnedFiles(ctx, store, cfg, idx.Team, name)
		if err != nil {
			return out, err
		}
		removed, err := removeHotOriginals(ctx, store, cfg.MetaRepoRoot, docs)
		if err != nil {
			return out, err
		}
		if len(removed) > 0 {
			out = append(out, TierDecision{Index: name, Action: TierMoved, RemovedFiles: removed})
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
	var out struct {
		Hits struct {
			Hits []struct {
				Source struct {
					Team       string `json:"team"`
					SourcePath string `json:"source_path"`
					ContentSHA string `json:"content_sha"`
					Kind       string `json:"memory_kind"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	body := map[string]any{
		"size": 10000,
		"query": map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"chunk_index": 0}},
			map[string]any{"term": map[string]any{"workspace": workspace}},
		}}},
		"_source": []string{"team", "source_path", "content_sha", "memory_kind"},
	}
	if err := c.Do(ctx, http.MethodPost, "/"+index+"/_search", body, nil, &out); err != nil {
		return nil, err
	}
	docs := make([]sourceDoc, 0, len(out.Hits.Hits))
	for _, h := range out.Hits.Hits {
		docs = append(docs, sourceDoc{team: h.Source.Team, sourcePath: h.Source.SourcePath, contentSHA: h.Source.ContentSHA})
	}
	return docs, nil
}

// coldOwnedFiles 는 cold store 청크 JSONL 중 이 달 인덱스에 속했던 문서다.
func coldOwnedFiles(ctx context.Context, store Store, cfg config.Config, team, index string) ([]sourceDoc, error) {
	keys, err := store.List(ctx, ChunksPrefix(team))
	if err != nil {
		return nil, err
	}
	var docs []sourceDoc
	for _, key := range keys {
		body, ok, err := store.Get(ctx, key)
		if err != nil || !ok {
			continue
		}
		first := firstChunk(body)
		if first == nil || naming.MonthlyIndex(first.team, parseTime(first.eventTime)) != index || first.kind != "episodic" {
			continue
		}
		docs = append(docs, sourceDoc{team: first.team, sourcePath: first.sourcePath, contentSHA: first.contentSHA})
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
			return removed, err
		}
		if document.SHA256(string(content)) != d.contentSHA {
			continue
		}
		raw, ok, err := store.Get(ctx, RawKey(d.team, d.sourcePath))
		if err != nil {
			return removed, err
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
		return err
	}
	return nil
}

func isRestored(ctx context.Context, c opensearch.Client, index string) (bool, error) {
	var out map[string]struct {
		Settings map[string]any `json:"settings"`
	}
	if err := c.Do(ctx, http.MethodGet, "/"+index+"/_settings", nil, url.Values{"flat_settings": {"true"}}, &out); err != nil {
		return false, err
	}
	return fmt.Sprint(out[index].Settings[opensearch.AutoManageSetting]) == "false", nil
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
		return "", err
	}
	return out.Aggregations.Newest.ValueAsString, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
