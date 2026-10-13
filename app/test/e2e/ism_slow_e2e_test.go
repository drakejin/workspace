//go:build e2e && e2e_slow

package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

const (
	// ISM 은 단계마다 한 주기(1분 + 스케줄 지연)를 쓴다: 초기화 → hot 평가 → cold 전이 → 스냅샷 → 완료 확인 → delete 전이 → 삭제.
	// 실측 6~15분
	ismWait = 25 * time.Minute
	ismPoll = 15 * time.Second
)

// TestISMMovesIndexToColdAndTierPrunesHot 는 tier 를 한 번도 돌리지 않아도 ISM 이 hot → snapshot → delete 로 인덱스를 옮기고,
// 그다음 tier 가 cold 로 간 달의 hot 원본을 치우는(pruned) 흐름이다. ISM 은 실제 시간으로 돈다 — app/ 에서 make test-e2e-slow (6~15분).
//
// 실제 메모리와 같은 클러스터를 쓰므로 공용 정책(mem-hot-cold)은 건드리지 않는다. 같은 본문에 hot 0일·ism_template 없는
// 테스트 정책을 만들어 이 인덱스에만 붙이고, ISM 주기만 잠깐 1분으로 줄였다가 되돌린다.
func TestISMMovesIndexToColdAndTierPrunesHot(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	const doc = "docs/specs/ism/aged.md"
	abs := write(t, m.cfg.MetaRepoRoot, doc, "---\ndate: "+daysAgo(hotDays+90)+"\n---\n# ISM 대상\nIsmAgedToken\n")
	res, err := ingestFile(ctx, m.indexer, abs)
	if err != nil {
		t.Fatal(err)
	}
	index := res.Index

	policy := "mem-hot-cold-e2e-" + m.cfg.DefaultTeam
	body := opensearch.HotColdPolicyBody(0, m.cfg.Snapshot.Repository)
	delete(body["policy"].(map[string]any), "ism_template")
	must(t, m.os.Do(ctx, http.MethodPut, "/_plugins/_ism/policies/"+policy, body, nil, nil))
	t.Cleanup(func() {
		_ = m.os.Do(context.Background(), http.MethodDelete, "/_plugins/_ism/policies/"+policy, nil, nil, nil)
	})
	// 주기는 정책을 붙이기 전에 줄인다 — 붙는 순간의 주기로 job 이 잡힌다
	must(t, setISMInterval(ctx, m.os, 1, 0.0))
	t.Cleanup(func() { _ = setISMInterval(context.Background(), m.os, nil, nil) })
	must(t, m.os.Do(ctx, http.MethodPost, "/_plugins/_ism/remove/"+index, nil, nil, nil))
	must(t, m.os.Do(ctx, http.MethodPost, "/_plugins/_ism/add/"+index, map[string]any{"policy_id": policy}, nil, nil))

	deadline := time.Now().Add(ismWait)
	for {
		err := m.os.Do(ctx, http.MethodGet, "/"+index, nil, nil, nil)
		if opensearch.IsStatus(err, http.StatusNotFound) {
			break
		}
		if time.Now().After(deadline) {
			var explain map[string]any
			_ = m.os.Do(ctx, http.MethodGet, "/_plugins/_ism/explain/"+index, nil, nil, &explain)
			t.Fatalf("ISM 이 %s 안에 %s 를 지우지 않았다: %v", ismWait, index, explain)
		}
		time.Sleep(ismPoll)
	}

	archived, err := cold.ArchivedMonths(ctx, m.os, m.cfg)
	if err != nil || !archived.Has(index) {
		t.Fatalf("ISM 스냅샷에 %s 가 있어야 한다: %v %v", index, archived, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatal("ISM 은 파일을 모른다 — hot 원본은 아직 남아 있다")
	}
	// ISM 스냅샷(목록 없음)은 그 스냅샷보다 시계 차이 여유(5분) 넘게 앞서 적재된 내용만 가진 것으로 본다 (GUIDE §7 cold 소유).
	// 실제 ISM 은 인덱스를 만든 지 hot 기간(기본 60일)이 지나서 스냅샷을 뜨지만, 이 테스트는 hot 0일이라 적재 몇 분 뒤에 뜬다 —
	// 여유 안이라 tier 가 원본을 남긴다. 적재 시각을 하루 앞당겨 실제 순서를 흉내 낸다.
	backdateIngest(t, m, doc, 24*time.Hour)
	decisions, err := cold.Tier(ctx, m.os, m.store, m.cfg, cold.TierOptions{Team: m.cfg.DefaultTeam})
	if err != nil {
		t.Fatal(err)
	}
	var pruned []string
	for _, d := range decisions {
		if d.Index == index && d.Action == cold.TierPruned {
			pruned = d.RemovedFiles
		}
	}
	if len(pruned) != 1 || pruned[0] != doc {
		t.Errorf("tier 가 ISM 이 옮긴 달의 hot 원본을 치운다: %+v", decisions)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.MetaRepoRoot, doc)); !os.IsNotExist(err) {
		t.Error("hot 원본이 지워져야 한다")
	}
	if text, _ := m.call(t, "list_cold", map[string]any{}); !strings.Contains(text, index) {
		t.Errorf("list_cold = %s", text)
	}
	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); isErr {
		t.Fatalf("restore = %s", text)
	}
	if got := searchPaths(t, m.cfg, "IsmAgedToken", false); len(got) != 1 {
		t.Errorf("ISM 스냅샷에서 복원해 다시 찾는다: %v", got)
	}
}

// setISMInterval 은 ISM 주기(분)와 jitter 를 바꾼다. nil 이면 기본값으로 되돌린다.
func setISMInterval(ctx context.Context, c opensearch.Client, minutes, jitter any) error {
	return c.Do(ctx, http.MethodPut, "/_cluster/settings", map[string]any{"transient": map[string]any{
		"plugins.index_state_management.job_interval": minutes,
		"plugins.index_state_management.jitter":       jitter,
	}}, nil, nil)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
