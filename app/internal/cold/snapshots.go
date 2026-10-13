package cold

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// Index 는 cold(스냅샷)에 있는 인덱스 하나다. 한 달을 tier 한 뒤 그 달 날짜의 문서가 늦게 들어오면 같은 인덱스가
// 여러 스냅샷에 있다 — 복원·archive 는 그 전부를 합친다.
type Index struct {
	Name         string
	Snapshots    []string // 이 인덱스를 담은 성공 스냅샷 전부 (오래된 것부터)
	SnapshotTime string   // 가장 최근 스냅샷 시각
	Restored     bool     // 복원본(auto_manage=false)이 열려 있다
	Searchable   bool     // 복원본에 검색 alias 가 붙어 있다 (false 면 합치다 멈춘 복원 — restore 를 다시 해야 검색에 나온다)
	Reopened     bool     // 같은 이름의 hot 인덱스가 다시 생겼다 (다음 tier 전까지 복원·archive 를 하지 않는다)
}

// Snapshot 은 스냅샷 하나다.
type Snapshot struct {
	Name      string   `json:"snapshot"`
	Indices   []string `json:"indices"`
	State     string   `json:"state"`
	StartTime string   `json:"start_time"`
}

// Snapshots 는 성공한 스냅샷 목록이다. repository 가 없으면 빈 목록.
func Snapshots(ctx context.Context, c opensearch.Client, repo string) ([]Snapshot, error) {
	var out struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	err := c.Do(ctx, http.MethodGet, "/_snapshot/"+repo+"/_all", nil, nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("snapshots %s: %w", repo, err)
	}
	ok := make([]Snapshot, 0, len(out.Snapshots))
	for _, s := range out.Snapshots {
		if s.State == snapshotSuccess {
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
		return nil, fmt.Errorf("list indices %s: %w", pattern, err)
	}
	live := make(map[string]bool, len(rows))
	for _, r := range rows {
		live[r.Index] = true
	}
	return live, nil
}

// ListCold 는 팀의 cold 인덱스 목록이다.
func ListCold(ctx context.Context, c opensearch.Client, cfg config.Config, team string) ([]Index, error) {
	snaps, err := Snapshots(ctx, c, cfg.Snapshot.Repository)
	if err != nil {
		return nil, err
	}
	open, err := opensearch.Settings(ctx, c, naming.TeamPattern(team))
	if err != nil {
		return nil, fmt.Errorf("list indices %s: %w", team, err)
	}
	searchable, err := searchAliased(ctx, c, team)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].StartTime < snaps[j].StartTime })
	byName := map[string]Index{}
	for _, s := range snaps {
		for _, name := range s.Indices {
			// mem-ads-* 는 mem-ads-data-* 도 잡으므로 이름에서 파싱한 team 으로 거른다
			idx, ok := naming.ParseIndex(name)
			if !ok || idx.Team != team || idx.Knowledge {
				continue
			}
			entry, seen := byName[name]
			if !seen {
				settings, live := open[name]
				unmanaged := opensearch.IsUnmanaged(settings)
				entry = Index{Name: name, Restored: live && unmanaged, Searchable: live && unmanaged && searchable[name], Reopened: live && !unmanaged}
			}
			entry.Snapshots = append(entry.Snapshots, s.Name)
			entry.SnapshotTime = s.StartTime
			byName[name] = entry
		}
	}
	out := make([]Index, 0, len(byName))
	for _, v := range byName {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// searchAliased 는 팀 인덱스 중 검색 alias(팀 hot alias)가 붙은 것이다. restore_cold 는 다 합친 뒤에야 alias 를 붙이므로,
// alias 없는 복원본은 합치다 멈춘 것이다.
func searchAliased(ctx context.Context, c opensearch.Client, team string) (map[string]bool, error) {
	var current map[string]struct {
		Aliases map[string]any `json:"aliases"`
	}
	err := c.Do(ctx, http.MethodGet, "/"+naming.TeamPattern(team)+"/_alias", nil, url.Values{"allow_no_indices": {"true"}}, &current)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aliases %s: %w", team, err)
	}
	out := make(map[string]bool, len(current))
	for index, v := range current {
		_, aliased := v.Aliases[naming.HotAlias(team)]
		out[index] = aliased
	}
	return out, nil
}

// TeamInWorkspace 는 team 이 이 workspace 의 팀인가다 — 이 workspace 가 문서를 둔 팀이거나(opensearch.WorkspaceTeams),
// 이 workspace 의 cold store 에 그 팀의 보관본이 있다 (모든 달이 cold 로 가 열린 인덱스가 없는 팀). 스냅샷 repository 는
// 클러스터의 모든 meta-repo 가 같이 쓰지만 cold store 는 workspace 마다 따로다 (로컬은 meta-repo 아래, S3 는 {prefix}/{workspace}/).
func TeamInWorkspace(ctx context.Context, c opensearch.Client, store Store, workspace, team string) (bool, error) {
	owned, err := opensearch.WorkspaceTeams(ctx, c, workspace)
	if err != nil {
		return false, err
	}
	if owned[team] {
		return true, nil
	}
	keys, err := store.List(ctx, TeamPrefix(team))
	if err != nil {
		return false, fmt.Errorf("cold store %s: %w", team, err)
	}
	return len(keys) > 0, nil
}

// Archive 는 cold 로 간(스냅샷에 들어간) 월별 인덱스와 그 인덱스를 담은 성공 스냅샷들이다 (복원 여부 무관).
type Archive map[string][]Snapshot

// Has 는 index 가 스냅샷에 있는가다.
func (a Archive) Has(index string) bool { return len(a[index]) > 0 }

// ArchivedMonths 는 스냅샷에 들어간 월별 인덱스다. 어떤 내용이 cold 소유인지는 Ownership 이 정한다.
func ArchivedMonths(ctx context.Context, c opensearch.Client, cfg config.Config) (Archive, error) {
	snaps, err := Snapshots(ctx, c, cfg.Snapshot.Repository)
	if err != nil {
		return nil, err
	}
	out := Archive{}
	for _, s := range snaps {
		for _, name := range s.Indices {
			if naming.IsMonthly(name) {
				out[name] = append(out[name], s)
			}
		}
	}
	return out, nil
}

// tierSnapshotPrefix 는 tier·archive 가 뜨는 스냅샷 이름의 머리다. ISM 스냅샷(ism-cold-…)과 갈린다.
const tierSnapshotPrefix = "cold-"

// isTierSnapshot 은 tier·archive 가 뜬 스냅샷인가다 (그 내용은 목록이 말한다).
func isTierSnapshot(name string) bool { return strings.HasPrefix(name, tierSnapshotPrefix) }

// SnapshotIndex 는 인덱스 하나를 새 스냅샷으로 뜬다. 같은 초에 두 번 떠도 겹치지 않게 나노초까지 쓴다.
func SnapshotIndex(ctx context.Context, c opensearch.Client, cfg config.Config, index string, now time.Time) (string, error) {
	name := fmt.Sprintf("%s%s-%s-%09d", tierSnapshotPrefix, index, strings.ToLower(now.UTC().Format("20060102t150405")), time.Now().Nanosecond())
	body := map[string]any{"indices": index, "include_global_state": false}
	var out struct {
		Snapshot struct {
			State string `json:"state"`
		} `json:"snapshot"`
	}
	if err := c.Do(ctx, http.MethodPut, "/_snapshot/"+cfg.Snapshot.Repository+"/"+name, body, url.Values{"wait_for_completion": {"true"}}, &out); err != nil {
		return "", fmt.Errorf("snapshot %s: %w", index, err)
	}
	// 끝까지 뜬 스냅샷만 믿는다 — 그 뒤에 인덱스·옛 스냅샷을 지우기 때문이다 (PARTIAL·FAILED 는 cold 목록에도 없다)
	if out.Snapshot.State != snapshotSuccess {
		return "", fmt.Errorf("snapshot %s: %s 가 %s 로 끝났습니다", index, name, orUnknown(out.Snapshot.State))
	}
	return name, nil
}

// snapshotSuccess 는 다 뜬 스냅샷의 상태다.
const snapshotSuccess = "SUCCESS"

func orUnknown(s string) string {
	if s == "" {
		return "알 수 없는 상태"
	}
	return s
}

// SnapshotsOf 는 index 를 담은 스냅샷이다 (오래된 것부터).
func SnapshotsOf(ctx context.Context, c opensearch.Client, cfg config.Config, index string) ([]Snapshot, error) {
	snaps, err := Snapshots(ctx, c, cfg.Snapshot.Repository)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, s := range snaps {
		for _, i := range s.Indices {
			if i == index {
				out = append(out, s)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime < out[j].StartTime })
	return out, nil
}

// DeleteSnapshot 은 스냅샷 하나를 지운다.
func DeleteSnapshot(ctx context.Context, c opensearch.Client, cfg config.Config, name string) error {
	if err := c.Do(ctx, http.MethodDelete, "/_snapshot/"+cfg.Snapshot.Repository+"/"+name, nil, nil, nil); err != nil {
		return fmt.Errorf("delete snapshot %s: %w", name, err)
	}
	return nil
}

// RestoreIndex 는 스냅샷에서 인덱스를 되살린다. 복원본은 ISM 자동 관리에서 뺀다 — 원래 생성일을 유지하므로
// 그대로 두면 ISM 이 바로 다시 cold 로 보낸다. 검색 alias 없이 되살린다 — restore_cold 는 다 합치고 deprecate 표시를
// 입힌 뒤에 alias 를 붙이고(Restore), archive 는 alias 없이 잠깐 열어 고친다.
func RestoreIndex(ctx context.Context, c opensearch.Client, cfg config.Config, index, snapshot string) error {
	if err := restoreSnapshot(ctx, c, cfg, snapshot, restoreBody(index)); err != nil {
		return fmt.Errorf("restore %s from %s: %w", index, snapshot, err)
	}
	return nil
}

func restoreBody(index string) map[string]any {
	return map[string]any{
		"indices":              index,
		"include_global_state": false,
		"include_aliases":      false,
		"index_settings":       map[string]any{opensearch.AutoManageSetting: false},
		// tier 는 쓰기를 막고 스냅샷한다 — 복원본은 deprecate 표시·archive 조치를 받아야 하므로 막기를 풀어 되살린다
		"ignore_index_settings": []string{"index.blocks.write"},
	}
}

func restoreSnapshot(ctx context.Context, c opensearch.Client, cfg config.Config, snapshot string, body map[string]any) error {
	path := "/_snapshot/" + cfg.Snapshot.Repository + "/" + snapshot + "/_restore"
	return c.Do(ctx, http.MethodPost, path, body, url.Values{"wait_for_completion": {"true"}}, nil)
}

// Restore 는 cold 인덱스 하나를 hot 으로 되돌린다 — 그 인덱스를 담은 스냅샷 전부를 합치고, 그 사이 deprecate 된 문서
// 표시를 다시 입힌 뒤에야 검색 alias 를 붙인다. 이미 복원돼 있으면 빠진 스냅샷 문서만 더한다 (중간에 끊긴 복원·예전 판이
// 최신 스냅샷만 되살린 복원본).
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
		if target.Reopened {
			return Index{}, Reopened(ctx, c, cfg, name)
		}
		if err := restoreSearchable(ctx, c, cfg, target, idx.Team); err != nil {
			return Index{}, err
		}
		target.Restored, target.Searchable = true, true
		return target, nil
	}
	return Index{}, fmt.Errorf("cold 에 %s 가 없습니다. list_cold 로 먼저 확인하세요", name)
}

// restoreSearchable 은 target 의 스냅샷 전부를 합치고 예전 판의 빈 text 를 지우고 deprecate 표시를 다시 입힌 뒤에 검색 alias 를
// 붙인다 — 그 전까지 새로 연 복원본은 검색에 나오지 않아, 합치다 멈춰도 표시가 빠진 문서가 기본 검색에 나오지 않는다.
func restoreSearchable(ctx context.Context, c opensearch.Client, cfg config.Config, target Index, team string) error {
	if err := RestoreAll(ctx, c, cfg, target.Name, target.Snapshots); err != nil {
		return partialRestore(ctx, c, cfg, team, target.Name, err)
	}
	if err := dropEmptyText(ctx, c, target.Name); err != nil {
		return fmt.Errorf("%s: 스냅샷은 다 합쳤지만 본문이 없는 문서의 빈 text 를 지우지 못해 검색에 붙이지 않았습니다 — restore 를 다시 하세요: %w", target.Name, err)
	}
	if err := applyDeprecations(ctx, c, cfg, team, target.Name); err != nil {
		return fmt.Errorf("%s: 스냅샷은 다 합쳤지만 deprecate 표시를 다시 입히지 못해 검색에 붙이지 않았습니다 — restore 를 다시 하세요: %w", target.Name, err)
	}
	// 검색 alias 는 스냅샷 메타데이터에 기대지 않고 다시 붙인다 — archive edit·purge 가 다시 쓴 스냅샷에는 alias 가 없고,
	// 그 사이 org 설정이 바뀌었을 수도 있다
	return attachAliases(ctx, c, target.Name, team, cfg.OrgOf(team))
}

// partialRestore 는 스냅샷을 합치다 멈춘 복원의 오류다. 복원본이 열려 있으면 deprecate 표시를 다시 입힌다 — 예전 판의
// 복원본은 이미 검색 alias 가 붙어 있어, 합치며 더한 문서가 표시 없이 기본 검색에 나온다.
func partialRestore(ctx context.Context, c opensearch.Client, cfg config.Config, team, index string, cause error) error {
	restored, _, err := OpenState(ctx, c, index)
	if err != nil {
		return fmt.Errorf("%s 를 일부만 복원했을 수 있습니다 — 복원본 상태를 읽지 못해 deprecate 표시를 다시 입히지 못했습니다. restore 를 다시 하세요: %w",
			index, errors.Join(cause, err))
	}
	if !restored {
		return cause // 아무것도 열지 못했다 (가장 최근 스냅샷부터 실패했거나, hot 에 다시 열렸다)
	}
	if err := applyDeprecations(ctx, c, cfg, team, index); err != nil {
		return fmt.Errorf("%s 를 일부만 복원했고 deprecate 표시도 다시 입히지 못했습니다 — restore 를 다시 하세요: %w", index, errors.Join(cause, err))
	}
	return fmt.Errorf("%s 를 일부만 복원했습니다 — 스냅샷을 합치다 멈췄습니다(deprecate 표시는 다시 입혔다). "+
		"새로 연 복원본은 다 합칠 때까지 검색에 나오지 않습니다. restore 를 다시 하면 빠진 문서만 더해 마무리합니다: %w", index, cause)
}

func attachAliases(ctx context.Context, c opensearch.Client, index, team, org string) error {
	var actions []any
	for alias := range opensearch.IndexAliases(team, org) {
		actions = append(actions, map[string]any{"add": map[string]any{"index": index, "alias": alias}})
	}
	if err := c.Do(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, nil, nil); err != nil {
		return fmt.Errorf("aliases %s: %w", index, err)
	}
	return nil
}

// dropEmptyTextScript 는 빈 text 를 필드째 지우는 스크립트다. 다른 문서는 건드리지 않는다(noop).
const dropEmptyTextScript = "if (ctx._source.text instanceof String && ctx._source.text.isEmpty()) { ctx._source.remove('text') } else { ctx.op = 'noop' }"

// dropEmptyText 는 복원본에서 본문이 없는 문서의 청크 0 에 남은 "text": "" 를 지운다. 예전 판은 빈 text 를 빈 문자열로 썼고
// exists 는 빈 문자열도 있다고 보아, 그대로 검색에 붙이면 그 문서가 목록·검색에 빈 결과로 나온다 (지금 판은 필드째 쓰지 않는다).
func dropEmptyText(ctx context.Context, c opensearch.Client, index string) error {
	body := map[string]any{
		"query": map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"chunk_index": 0}},
			map[string]any{"exists": map[string]any{"field": "text"}},
		}}},
		"script": map[string]any{"lang": "painless", "source": dropEmptyTextScript},
	}
	q := url.Values{"refresh": {"true"}, "conflicts": {"proceed"}}
	if err := c.Do(ctx, http.MethodPost, "/"+index+"/_update_by_query", body, q, nil); err != nil {
		return fmt.Errorf("empty text %s: %w", index, err)
	}
	return nil
}

func applyDeprecations(ctx context.Context, c opensearch.Client, cfg config.Config, team, index string) error {
	deprecated, err := state.NewArchiveState(cfg.MetaRepoRoot).Deprecated(team)
	if err != nil {
		return err
	}
	for p := range deprecated {
		if err := opensearch.UpdateDoc(ctx, c, []string{index}, cfg.Workspace, p, map[string]any{"deprecated": true}); err != nil {
			return err
		}
	}
	return nil
}
