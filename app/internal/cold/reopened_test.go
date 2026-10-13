package cold

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// reopenedCluster 는 index 가 hot(데몬 관리)으로 다시 열려 있고 그 안의 가장 최근 문서가 newest 인 클러스터다.
func reopenedCluster(index, newest string) *fakeOS {
	return &fakeOS{route: func(method, path string, body map[string]any) (any, error) {
		switch {
		case method == http.MethodGet && path == "/"+index+"/_settings":
			return map[string]any{index: map[string]any{"settings": map[string]any{}}}, nil
		case method == http.MethodPost && path == "/"+index+"/_search" && body["aggs"] != nil:
			return map[string]any{"aggregations": map[string]any{"newest": map[string]any{"value_as_string": newest}}}, nil
		}
		return nil, nil
	}}
}

// R1-cold-2: cold 로 간 달이 hot 에 다시 열려 있으면 복원·archive 를 거부한다. 늦게 들어온 문서가 hot 기간 안이면 tier 도 그 달을
// 옮기지 않으므로(kept-hot) "tier 를 돌리라" 는 안내는 소용이 없다 — 언제부터 tier 가 그 달을 다시 cold 로 보내는지 알려 준다.
func TestReopenedMonthRefusalTellsWhenTierCanMoveIt(t *testing.T) {
	const index = "mem-t-2026.08"
	cfg := tierConfig(t)
	cfg.Location = time.UTC
	now := time.Now().UTC()
	inside := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	outside := now.Add(-time.Duration(cfg.HotDays+5) * 24 * time.Hour).Format(time.RFC3339)
	ready := now.Add(-10 * 24 * time.Hour).Add(cfg.HotWindow()).Format("2006-01-02 15:04")

	cases := []struct {
		name, newest string
		want, not    []string
	}{
		{"hot 기간 안의 늦은 문서 — 기다릴 때를 알린다", inside, []string{ready, "tier-tick"}, []string{"지금 scripts/memory/run.sh tier"}},
		{"hot 기간이 지났다 — 지금 tier 를 돌리면 된다", outside, []string{"지금 scripts/memory/run.sh tier"}, []string{ready}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RestoreAll(context.Background(), reopenedCluster(index, c.newest), cfg, index, []string{"cold-" + index + "-a"})

			if !errors.Is(err, ErrReopened) {
				t.Fatalf("err = %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q 가 없다: %v", w, err)
				}
			}
			for _, n := range c.not {
				if strings.Contains(err.Error(), n) {
					t.Errorf("%q 는 이 경우 맞지 않는 안내다: %v", n, err)
				}
			}
		})
	}
}
