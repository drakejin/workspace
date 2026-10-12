package cold

import (
	"context"
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

// Index 는 cold(스냅샷)에 있는 인덱스 하나다. 같은 인덱스가 여러 스냅샷에 있으면 최신 것.
type Index struct {
	Name         string
	Snapshot     string
	SnapshotTime string
	Restored     bool // 지금 hot 에 복원돼 있는가
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
				latest[name] = Index{Name: name, Snapshot: s.Name, SnapshotTime: s.StartTime, Restored: live[name]}
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

// Archive 는 cold 로 간(스냅샷에 들어간) 월별 인덱스와 그 인덱스를 담은 성공 스냅샷 이름들이다 (복원 여부 무관).
type Archive map[string][]string

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
				out[name] = append(out[name], s.Name)
			}
		}
	}
	return out, nil
}

// SnapshotIndex 는 인덱스 하나를 새 스냅샷으로 뜬다. 같은 초에 두 번 떠도 겹치지 않게 나노초까지 쓴다.
func SnapshotIndex(ctx context.Context, c opensearch.Client, cfg config.Config, index string, now time.Time) (string, error) {
	name := fmt.Sprintf("cold-%s-%s-%09d", index, strings.ToLower(now.UTC().Format("20060102t150405")), time.Now().Nanosecond())
	body := map[string]any{"indices": index, "include_global_state": false}
	if err := c.Do(ctx, http.MethodPut, "/_snapshot/"+cfg.Snapshot.Repository+"/"+name, body, url.Values{"wait_for_completion": {"true"}}, nil); err != nil {
		return "", fmt.Errorf("snapshot %s: %w", index, err)
	}
	return name, nil
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
// 그대로 두면 ISM 이 바로 다시 cold 로 보낸다. withAliases 가 false 면 검색 alias 없이 (cold 다시 쓰기용).
func RestoreIndex(ctx context.Context, c opensearch.Client, cfg config.Config, index, snapshot string, withAliases bool) error {
	body := map[string]any{
		"indices":              index,
		"include_global_state": false,
		"include_aliases":      withAliases,
		"index_settings":       map[string]any{opensearch.AutoManageSetting: false},
		// tier 는 쓰기를 막고 스냅샷한다 — 복원본은 deprecate 표시·archive 조치를 받아야 하므로 막기를 풀어 되살린다
		"ignore_index_settings": []string{"index.blocks.write"},
	}
	path := "/_snapshot/" + cfg.Snapshot.Repository + "/" + snapshot + "/_restore"
	if err := c.Do(ctx, http.MethodPost, path, body, url.Values{"wait_for_completion": {"true"}}, nil); err != nil {
		return fmt.Errorf("restore %s from %s: %w", index, snapshot, err)
	}
	return nil
}

// Restore 는 cold 인덱스 하나를 hot 으로 되돌리고, 그 사이 deprecate 된 문서 표시를 다시 입힌다.
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
		if err := RestoreIndex(ctx, c, cfg, name, target.Snapshot, true); err != nil {
			return Index{}, err
		}
		// 검색 alias 는 스냅샷 메타데이터에 기대지 않고 다시 붙인다 — archive edit·purge 가 다시 쓴 스냅샷에는 alias 가 없고,
		// 그 사이 org 설정이 바뀌었을 수도 있다
		if err := attachAliases(ctx, c, name, idx.Team, cfg.OrgOf(idx.Team)); err != nil {
			return Index{}, err
		}
		if err := applyDeprecations(ctx, c, cfg, idx.Team, name); err != nil {
			return Index{}, err
		}
		target.Restored = true
		return target, nil
	}
	return Index{}, fmt.Errorf("cold 에 %s 가 없습니다. list_cold 로 먼저 확인하세요", name)
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
