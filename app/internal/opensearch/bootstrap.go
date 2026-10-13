package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/naming"
)

// clusterWrite 는 클러스터 전역 객체(스냅샷 repository·ISM 정책)가 이 설정과 다를 때 할 일이다. 이 객체는 클러스터에 하나라
// (정책의 ism_template 은 모든 mem-* 에 걸린다) 같은 스택을 쓰는 meta-repo 끼리 설정이 다르면 마지막에 쓴 쪽이 모두를 정한다.
type clusterWrite bool

const (
	leaveCluster clusterWrite = false // 없을 때만 만들고, 다르면 두 값을 경고만 한다 (MCP·CLI·데몬이 시작할 때)
	claimCluster clusterWrite = true  // 이 설정으로 바꾼다 (scripts/memory/run.sh bootstrap)
)

// warnFunc 는 경고를 남기는 곳이다.
type warnFunc func(format string, args ...any)

// stderrWarn 은 경고를 stderr 로 쓴다 — MCP 서버의 stdout 은 프로토콜 전용이고, 데몬(launchd)은 stdout·stderr 를 같은 로그에 남긴다.
func stderrWarn(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

// Bootstrap 은 템플릿·매핑·스냅샷 repository·ISM 정책·org alias 를 멱등하게 맞춘다.
// 여러 프로세스(데몬·MCP·CLI)가 동시에 불러도 안전하다. 클러스터 전역 객체(스냅샷 repository·ISM 정책)는 없을 때만 만들고,
// 이 설정과 다르게 이미 있으면 덮어쓰지 않고 두 값을 stderr 로 경고한다 — 바꾸는 것은 BootstrapClaim(bootstrap 명령)뿐이다.
func Bootstrap(ctx context.Context, c Client, cfg config.Config) error {
	return bootstrap(ctx, c, cfg, leaveCluster, stderrWarn)
}

// BootstrapClaim 은 Bootstrap 에 더해 클러스터 전역 객체를 이 설정으로 바꾼다 (scripts/memory/run.sh bootstrap) — 같은 스택을 쓰는
// 모든 workspace 의 새 mem-* 인덱스가 이 hot 기간·스냅샷 repository 를 따른다.
func BootstrapClaim(ctx context.Context, c Client, cfg config.Config) error {
	return bootstrap(ctx, c, cfg, claimCluster, stderrWarn)
}

func bootstrap(ctx context.Context, c Client, cfg config.Config, mode clusterWrite, warn warnFunc) error {
	if err := c.Do(ctx, http.MethodPut, "/_index_template/"+TemplateName, IndexTemplate(), nil, nil); err != nil {
		return fmt.Errorf("index template: %w", err)
	}
	// 템플릿은 새 인덱스에만 붙는다. 이미 있는 인덱스에는 새 필드를 더한다 (추가만이라 안전)
	if err := c.Do(ctx, http.MethodPut, "/"+naming.AllPattern+"/_mapping", map[string]any{"properties": Properties()}, Lenient(), nil); err != nil {
		return fmt.Errorf("mapping: %w", err)
	}
	if err := ensureSnapshotRepository(ctx, c, cfg.Snapshot, mode, warn); err != nil {
		return fmt.Errorf("snapshot repository %s: %w", cfg.Snapshot.Repository, err)
	}
	if err := ensurePolicy(ctx, c, HotColdPolicy, HotColdPolicyBody(cfg.HotDays, cfg.Snapshot.Repository), mode, warn); err != nil {
		return fmt.Errorf("ISM %s: %w", HotColdPolicy, err)
	}
	if err := ensurePolicy(ctx, c, KeepPolicy, KeepPolicyBody(), mode, warn); err != nil {
		return fmt.Errorf("ISM %s: %w", KeepPolicy, err)
	}
	if err := SyncOrgAliases(ctx, c, cfg); err != nil {
		return fmt.Errorf("org alias: %w", err)
	}
	return nil
}

func ensureSnapshotRepository(ctx context.Context, c Client, s config.Snapshot, mode clusterWrite, warn warnFunc) error {
	want := map[string]any{"type": "fs", "settings": map[string]any{"location": s.FSLocation}}
	if s.Type == "s3" {
		want = map[string]any{"type": "s3", "settings": map[string]any{"bucket": s.S3Bucket, "base_path": "snapshots"}}
	}
	// 이미 같은 설정이면 건드리지 않는다 — 모든 CLI·MCP 호출이 bootstrap 하므로, 다른 프로세스가 스냅샷·복원하는 동안
	// PUT 하면 OpenSearch 가 "currently used" 로 거부해 읽기 전용 검색까지 실패한다
	var current map[string]struct {
		Type     string         `json:"type"`
		Settings map[string]any `json:"settings"`
	}
	err := c.Do(ctx, http.MethodGet, "/_snapshot/"+s.Repository, nil, nil, &current)
	if err != nil && !IsStatus(err, http.StatusNotFound) {
		return err
	}
	if cur, ok := current[s.Repository]; ok {
		if sameRepository(cur.Type, cur.Settings, want) {
			return nil
		}
		theirs, mine := fmt.Sprintf("type=%s %v", cur.Type, cur.Settings), fmt.Sprintf("type=%s %v", want["type"], want["settings"])
		if mode == leaveCluster {
			warn("[bootstrap] 경고: 스냅샷 repository %s 가 클러스터에 다른 설정으로 있어 덮어쓰지 않습니다 — 클러스터: %s, 이 설정(COLD_SNAPSHOT_*): %s. "+
				"스냅샷은 클러스터의 설정대로 뜬다. 이 설정으로 바꾸려면 %s", s.Repository, theirs, mine, claimCommand)
			return nil
		}
		warn("[bootstrap] 스냅샷 repository %s 를 이 설정으로 바꿉니다 — 클러스터: %s → %s", s.Repository, theirs, mine)
	}
	err = c.Do(ctx, http.MethodPut, "/_snapshot/"+s.Repository, want, nil, nil)
	var e *Error
	if errors.As(err, &e) && strings.Contains(e.Reason, "currently used") {
		return fmt.Errorf("지금 스냅샷·복원 중이라 repository 를 바꾸지 못했습니다 — 끝난 뒤 %s 를 다시 하세요: %w", claimCommand, err)
	}
	return err
}

// claimCommand 는 클러스터 전역 객체를 이 설정으로 바꾸는 명령이다.
const claimCommand = "scripts/memory/run.sh bootstrap"

func sameRepository(typ string, settings map[string]any, want map[string]any) bool {
	if typ != want["type"] {
		return false
	}
	for k, v := range want["settings"].(map[string]any) {
		if fmt.Sprint(settings[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// ensurePolicy 는 ISM 정책 id 를 body 로 맞춘다. 내용이 같으면 쓰지 않는다 — 모든 프로세스가 시작할 때 부르므로 매번 PUT 하면
// 정책의 판만 올라간다. 다른 내용으로 있으면 mode 가 claimCluster 일 때만 바꾼다.
func ensurePolicy(ctx context.Context, c Client, id string, body map[string]any, mode clusterWrite, warn warnFunc) error {
	path := "/_plugins/_ism/policies/" + id
	var existing struct {
		SeqNo       int64          `json:"_seq_no"`
		PrimaryTerm int64          `json:"_primary_term"`
		Policy      map[string]any `json:"policy"`
	}
	err := c.Do(ctx, http.MethodGet, path, nil, nil, &existing)
	switch {
	case IsStatus(err, http.StatusNotFound):
		err = c.Do(ctx, http.MethodPut, path, body, nil, nil)
	case err != nil:
		return err
	case hasAll(existing.Policy, normalized(body["policy"])):
		return nil
	case mode == leaveCluster:
		warn("[bootstrap] 경고: ISM 정책 %s 이 클러스터에 다른 내용으로 있어 덮어쓰지 않습니다 — 클러스터: %q, 이 설정: %q. "+
			"정책은 클러스터에 하나라(같은 스택을 쓰는 모든 workspace 의 새 mem-* 인덱스에 걸린다) 이 설정으로 바꾸려면 %s",
			id, description(existing.Policy), description(body["policy"]), claimCommand)
		return nil
	default:
		warn("[bootstrap] ISM 정책 %s 을 이 설정으로 바꿉니다 — 클러스터: %q → %q (같은 스택을 쓰는 모든 workspace 의 새 mem-* 인덱스에 걸린다)",
			id, description(existing.Policy), description(body["policy"]))
		err = c.Do(ctx, http.MethodPut, path, body, url.Values{
			"if_seq_no":       {strconv.FormatInt(existing.SeqNo, 10)},
			"if_primary_term": {strconv.FormatInt(existing.PrimaryTerm, 10)},
		}, nil)
	}
	// 409 = 다른 프로세스가 방금 같은 정책을 썼다. 내용이 같으니 성공으로 본다
	if IsStatus(err, http.StatusConflict) {
		return nil
	}
	return err
}

// description 은 정책의 설명이다 (hot 기간·스냅샷 repository 가 들어 있다).
func description(policy any) string {
	m, _ := policy.(map[string]any)
	d, _ := m["description"].(string)
	return d
}

// normalized 는 v 를 JSON 으로 오간 값이다 — 클러스터가 돌려준 값(숫자는 float64, 배열은 []any)과 비교할 수 있게.
func normalized(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// hasAll 은 want 의 모든 값이 got 에 같은 값으로 있는가다. 클러스터가 덧붙인 필드(시각·판·retry 기본값)는 보지 않는다.
// 배열은 길이와 순서까지 같아야 한다.
func hasAll(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !hasAll(g[k], v) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !hasAll(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

// IndexAliases 는 새 인덱스에 붙일 alias 다: 팀 hot alias, 그리고 팀이 org 에 속하면 org alias.
func IndexAliases(team, org string) map[string]any {
	aliases := map[string]any{naming.HotAlias(team): map[string]any{}}
	if org != "" {
		aliases[naming.OrgAlias(org)] = map[string]any{}
	}
	return aliases
}

// EnsureIndex 는 인덱스가 없으면 alias 와 매핑을 붙여 만든다. 매핑은 템플릿에도 있지만 만들 때 한 번 더 준다 — 같은 클러스터의
// 다른 판 프로세스(업그레이드 중인 데몬 등)가 bootstrap 으로 템플릿을 옛 필드 구성으로 되돌려도, 이 판이 쓰는 필드가 strict
// 매핑에 거부되지 않게 (요청의 매핑은 템플릿의 매핑·설정과 합쳐진다).
func EnsureIndex(ctx context.Context, c Client, index, team, org string) error {
	body := map[string]any{"aliases": IndexAliases(team, org), "mappings": map[string]any{"properties": Properties()}}
	err := c.Do(ctx, http.MethodPut, "/"+index, body, nil, nil)
	if HasType(err, "resource_already_exists_exception") {
		return nil
	}
	return err
}

// SyncOrgAliases 는 이 workspace 팀들의 인덱스에 org alias 를 MEMORY_TEAM_ORGS 대로 맞춘다 (팀을 org 로 옮기거나 뺄 때).
// 다른 meta-repo 의 팀 인덱스는 건드리지 않는다 — 그쪽 설정은 그쪽 bootstrap 이 맞춘다.
func SyncOrgAliases(ctx context.Context, c Client, cfg config.Config) error {
	owned, err := WorkspaceTeams(ctx, c, cfg.Workspace)
	if err != nil {
		return err
	}
	var current map[string]struct {
		Aliases map[string]any `json:"aliases"`
	}
	err = c.Do(ctx, http.MethodGet, "/"+naming.AllPattern+"/_alias", nil, url.Values{"allow_no_indices": {"true"}}, &current)
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	indices := make([]string, 0, len(current))
	for index := range current {
		indices = append(indices, index)
	}
	sort.Strings(indices)
	var actions []any
	for _, index := range indices {
		idx, ok := naming.ParseIndex(index)
		if !ok || !owned[idx.Team] {
			continue
		}
		want := ""
		if org := cfg.OrgOf(idx.Team); org != "" {
			want = naming.OrgAlias(org)
		}
		for alias := range current[index].Aliases {
			if strings.HasPrefix(alias, naming.Prefix+"org-") && alias != want && alias != naming.HotAlias(idx.Team) {
				actions = append(actions, map[string]any{"remove": map[string]any{"index": index, "alias": alias}})
			}
		}
		if _, has := current[index].Aliases[want]; want != "" && !has {
			actions = append(actions, map[string]any{"add": map[string]any{"index": index, "alias": want}})
		}
	}
	if len(actions) == 0 {
		return nil
	}
	return c.Do(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, nil, nil)
}
