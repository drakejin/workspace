//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
	"github.com/drakejin/workspace/app/internal/state"
)

// 판정 쌍 픽스처: 같은 주제의 episodic 두 문서. 유효 시각 차가 판정 기간(7일)보다 커서 latest-wins 로 진 쪽이 숨는다.
const (
	renameToken  = "RenameShipFeePolicy"
	renameLoser  = "docs/specs/ship-fee/v1.md"
	renameWinner = "docs/specs/ship-fee/v2.md"
	renameFinal  = "docs/specs/ship-fee/final.md"
	renameOld    = "docs/specs/ship-fee/old.md"
)

func shipFeeDoc(daysOld int, fee string) string {
	return "---\ndate: " + daysAgo(daysOld) + "\n---\n# 배송비 정책\n## 결정\n" + renameToken + " 는 기본 배송비를 " + fee + " 원으로 받는다. 제주는 따로 받는다.\n"
}

// judgedPair 는 판정을 켠 데몬의 boot 사이클로 쌍을 색인·판정해, 진 쪽이 기본 검색에서 숨은 상태를 만든다.
func judgedPair(t *testing.T) (config.Config, *daemon.Daemon, *fakeLLM) {
	t.Helper()
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"})
	write(t, cfg.MetaRepoRoot, renameLoser, shipFeeDoc(hotDays*2/3, "3000"))
	write(t, cfg.MetaRepoRoot, renameWinner, shipFeeDoc(1, "3500"))
	ai := &fakeLLM{}
	d := bootDaemon(t, cfg, ai)
	t.Cleanup(func() { _ = d.Shutdown() })
	if got := searchPaths(t, cfg, renameToken, false); !reflect.DeepEqual(got, []string{renameWinner}) {
		t.Fatalf("latest-wins 로 진 쪽이 숨어야 한다: %v", got)
	}
	return cfg, d, ai
}

func movePath(t *testing.T, root, from, to string) {
	t.Helper()
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))); err != nil {
		t.Fatal(err)
	}
}

func judgeRecords(t *testing.T, cfg config.Config) []state.JudgeRecord {
	t.Helper()
	records, err := state.NewJudgeState(cfg.MetaRepoRoot).Ledger.ReadAll(cfg.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// supersededOf 는 query 에 걸린 문서(밀린 문서 포함)의 경로 → superseded_by 다.
func supersededOf(t *testing.T, cfg config.Config, query string) map[string]string {
	t.Helper()
	res, err := search.Search(context.Background(), opensearch.New(cfg.OpenSearchURL), search.Request{
		Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: query, IncludeSuperseded: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, h := range res.Hits {
		out[h.SourcePath] = h.SupersededBy
	}
	return out
}

// TestJudgeDecisionFollowsRenames 는 판정 결론이 파일 이름이 아니라 내용(본문 sha 쌍)을 따라가는지 본다.
// 같은 쌍은 다시 판정하지 않으므로(멱등) 경로만 보면 이긴 쪽 이름만 바꿔도(git mv) 낡은 문서가 영영 되살아난다.
func TestJudgeDecisionFollowsRenames(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(t *testing.T, root string)
		visible    []string
		superseded map[string]string // 기본 검색에 숨은 문서 → superseded_by
	}{
		{
			name:       "이긴 쪽 이름 바꿈",
			mutate:     func(t *testing.T, root string) { movePath(t, root, renameWinner, renameFinal) },
			visible:    []string{renameFinal},
			superseded: map[string]string{renameLoser: renameFinal},
		},
		{
			name:       "진 쪽 이름 바꿈",
			mutate:     func(t *testing.T, root string) { movePath(t, root, renameLoser, renameOld) },
			visible:    []string{renameWinner},
			superseded: map[string]string{renameOld: renameWinner},
		},
		{
			name: "둘 다 이름 바꿈",
			mutate: func(t *testing.T, root string) {
				movePath(t, root, renameLoser, renameOld)
				movePath(t, root, renameWinner, renameFinal)
			},
			visible:    []string{renameFinal},
			superseded: map[string]string{renameOld: renameFinal},
		},
		{
			name: "이긴 쪽 삭제 — 진 쪽이 다시 보인다",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(renameWinner))); err != nil {
					t.Fatal(err)
				}
			},
			visible:    []string{renameLoser},
			superseded: map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, d, ai := judgedPair(t)
			before := judgeRecords(t, cfg)

			tc.mutate(t, cfg.MetaRepoRoot)
			d.Trigger("watch")
			d.Trigger("timer") // 한 사이클 더 돌아도 그대로다

			if got := searchPaths(t, cfg, renameToken, false); !reflect.DeepEqual(got, tc.visible) {
				t.Errorf("기본 검색 = %v, want %v", got, tc.visible)
			}
			hidden := map[string]string{}
			for p, by := range supersededOf(t, cfg, renameToken) {
				if by != "" {
					hidden[p] = by
				}
			}
			if !reflect.DeepEqual(hidden, tc.superseded) {
				t.Errorf("superseded_by = %v, want %v", hidden, tc.superseded)
			}
			if after := judgeRecords(t, cfg); len(after) != len(before) {
				t.Errorf("이름만 바뀐 쌍은 다시 판정하지 않는다: 원장 %d → %d건", len(before), len(after))
			}
			if calls, _ := ai.counts(); calls != 0 {
				t.Errorf("LLM 호출 %d", calls)
			}
		})
	}
}

// TestJudgeRejudgesEditedLoser 는 진 쪽 본문이 바뀌면 새 쌍(새 pairKey)으로 다시 판정하는지 본다.
func TestJudgeRejudgesEditedLoser(t *testing.T) {
	cfg, d, _ := judgedPair(t)
	before := judgeRecords(t, cfg)

	write(t, cfg.MetaRepoRoot, renameLoser, shipFeeDoc(hotDays*2/3, "3200"))
	d.Trigger("watch")

	after := judgeRecords(t, cfg)
	if len(after) != len(before)+1 {
		t.Fatalf("본문이 바뀐 진 쪽은 다시 판정한다: 원장 %d → %d건", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.PairKey == before[len(before)-1].PairKey || last.Decision != state.DecisionSuperseded || last.Old.Path != renameLoser || last.New.Path != renameWinner {
		t.Errorf("재판정 = %+v", last)
	}
	if got := searchPaths(t, cfg, renameToken, false); !reflect.DeepEqual(got, []string{renameWinner}) {
		t.Errorf("기본 검색 = %v", got)
	}
}
