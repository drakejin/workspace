//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

func bootDaemon(t *testing.T, cfg config.Config, ai *fakeLLM) *daemon.Daemon {
	t.Helper()
	var logMu sync.Mutex
	d, err := daemon.New(context.Background(), daemon.Deps{Config: cfg, Ask: ai, Log: func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		t.Logf(f, a...)
	}, RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestIndexVersionUpgradeRewritesWithoutRejudging 는 필드 구성의 판(index_version)이 오른 뒤 첫 사이클이
// 내용이 그대로인 문서를 다시 쓰기만 하고(새 필드를 채우려고) 판정·증류에 넘기지 않는지 본다 — 판정에 넘기면
// 아무도 고치지 않은 오래된 문서가 latest-wins 로 숨는다.
func TestIndexVersionUpgradeRewritesWithoutRejudging(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_JUDGE_ENABLED": "false"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	client := opensearch.New(cfg.OpenSearchURL)

	// 판정을 끈 채로 비슷한 두 문서(유효 시각 28일 차이)를 색인한다 — 판정을 켜고 다시 보면 latest-wins 감이다
	write(t, root, "docs/specs/fee/old.md", "---\ndate: "+daysAgo(30)+"\n---\n# 수수료 정책\n## 결정\nUpgradeFeePolicy 는 카드 수수료를 2% 로 받는다. 해외 카드도 같다.\n")
	write(t, root, "docs/specs/fee/new.md", "---\ndate: "+daysAgo(2)+"\n---\n# 수수료 정책\n## 결정\nUpgradeFeePolicy 는 카드 수수료를 3% 로 받는다. 해외 카드도 같다.\n")
	first := bootDaemon(t, cfg, &fakeLLM{})
	if err := first.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if got := searchPaths(t, cfg, "UpgradeFeePolicy", false); len(got) != 2 {
		t.Fatalf("판정 없이 둘 다 보인다: %v", got)
	}

	// 예전 판으로 쓰인 색인을 흉내 낸다
	body := map[string]any{
		"query":  map[string]any{"term": map[string]any{"workspace": cfg.Workspace}},
		"script": map[string]any{"lang": "painless", "source": "ctx._source['index_version'] = 1"},
	}
	if err := client.Do(ctx, http.MethodPost, "/mem-"+cfg.DefaultTeam+"-*/_update_by_query", body, opensearch.With(nil, "refresh", "true"), nil); err != nil {
		t.Fatal(err)
	}

	// 판정을 켠 데몬의 boot 사이클 — 판이 낮은 문서를 다시 쓰지만 내용 변경이 아니므로 판정하지 않는다
	cfg.Judge.Enabled = true
	ai := &fakeLLM{}
	second := bootDaemon(t, cfg, ai)
	t.Cleanup(func() { _ = second.Shutdown() })
	s := state.ReadStatus(root)
	if s == nil || s.LastCycle == nil || s.LastCycle.Summary == nil {
		t.Fatalf("status = %+v", s)
	}
	if got := s.LastCycle.Summary.Indexed; got != 2 {
		t.Errorf("낮은 판의 문서를 다시 쓴다: indexed=%d", got)
	}
	if j := s.LastCycle.Judge; j != nil && j.Candidates != 0 {
		t.Errorf("내용이 그대로면 판정하지 않는다: %+v", j)
	}
	if calls, _ := ai.counts(); calls != 0 {
		t.Errorf("LLM 호출 %d", calls)
	}
	if got := searchPaths(t, cfg, "UpgradeFeePolicy", false); len(got) != 2 {
		t.Errorf("아무도 고치지 않은 문서가 숨으면 안 된다: %v", got)
	}
	hits, err := opensearch.Count(ctx, client, "mem-"+cfg.DefaultTeam+"-*", map[string]any{"bool": map[string]any{"filter": []any{
		map[string]any{"term": map[string]any{"workspace": cfg.Workspace}},
		map[string]any{"range": map[string]any{"index_version": map[string]any{"lt": model.IndexVersion}}},
	}}})
	if err != nil || hits != 0 {
		t.Errorf("낮은 판 청크가 남았다: %d %v", hits, err)
	}
}
