//go:build e2e

package e2e

import (
	"reflect"
	"sort"
	"testing"
)

// 본문이 똑같은 사본 쌍: frontmatter 날짜만 달라 latest-wins 가 옛 사본을 새 사본으로 밀어낸다.
const (
	dupToken   = "DupShipRuleToken"
	dupOld     = "docs/specs/dup/b-old.md"
	dupNew     = "docs/specs/dup/c-new.md"
	dupRenamed = "docs/specs/dup/z-new.md"
)

func dupDoc(daysOld int) string {
	return "---\ndate: " + daysAgo(daysOld) + "\n---\n# 묶음 배송 규칙\n## 결정\n" + dupToken + " 는 같은 날 주문을 한 상자로 묶는다.\n"
}

// sortedPaths 는 검색 결과 경로를 점수와 상관없이 비교하려고 정렬한 사본이다.
func sortedPaths(visible []string) []string {
	out := append([]string(nil), visible...)
	sort.Strings(out)
	return out
}

// TestJudgeNeverFlipsIdenticalCopies 는 R1-judge-1 의 재현이다. 두 쪽 본문이 같으면 내용으로 이긴 쪽을 가릴 수 없다 —
// 이긴 사본의 이름을 바꿨을 때 옛 사본을 이긴 쪽으로 오인하면 결론이 뒤집혀 새 사본이 숨는다. 결론이 풀려 둘 다 보여야 한다.
func TestJudgeNeverFlipsIdenticalCopies(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"})
	write(t, cfg.MetaRepoRoot, dupOld, dupDoc(hotDays*2/3))
	write(t, cfg.MetaRepoRoot, dupNew, dupDoc(1))
	d := bootDaemon(t, cfg, &fakeLLM{})
	t.Cleanup(func() { _ = d.Shutdown() })
	if got := searchPaths(t, cfg, dupToken, false); !reflect.DeepEqual(got, []string{dupNew}) {
		t.Fatalf("latest-wins 로 옛 사본이 숨어야 한다: %v", got)
	}

	movePath(t, cfg.MetaRepoRoot, dupNew, dupRenamed)
	d.Trigger("watch")
	d.Trigger("timer")

	if got := sortedPaths(searchPaths(t, cfg, dupToken, false)); !reflect.DeepEqual(got, []string{dupOld, dupRenamed}) {
		t.Errorf("기본 검색 = %v — 새 사본(%s)이 옛 사본 뒤로 숨으면 안 된다", got, dupRenamed)
	}
}

// TestJudgeDecisionStaysOutOfKnowledge 는 R1-judge-2 의 재현이다. episodic 판정의 결론은 knowledge 로 옮긴 같은 본문을
// 숨기지 않는다 — knowledge 는 유사도로 판정하지 않는다 (GUIDE 5.3).
func TestJudgeDecisionStaysOutOfKnowledge(t *testing.T) {
	cfg, d, _ := judgedPair(t)
	const moved = "docs/knowledge/ship-fee-v1.md"

	movePath(t, cfg.MetaRepoRoot, renameLoser, moved)
	d.Trigger("watch")
	d.Trigger("timer")

	if got := sortedPaths(searchPaths(t, cfg, renameToken, false)); !reflect.DeepEqual(got, []string{moved, renameWinner}) {
		t.Errorf("기본 검색 = %v — knowledge 문서는 episodic 판정으로 숨지 않는다", got)
	}
	if by := supersededOf(t, cfg, renameToken)[moved]; by != "" {
		t.Errorf("%s superseded_by = %q", moved, by)
	}
}
