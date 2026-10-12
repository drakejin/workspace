package ops

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/daemon"
)

type fakeProbe struct {
	container, health string
	containerOK       bool
	healthOK          bool
	stats             IndexStats
}

func (f fakeProbe) ContainerState(context.Context) (string, bool) { return f.container, f.containerOK }
func (f fakeProbe) ClusterHealth(context.Context) (string, bool)  { return f.health, f.healthOK }
func (f fakeProbe) IndexStats(context.Context) (IndexStats, bool) { return f.stats, true }

func TestRenderStatusAllDown(t *testing.T) {
	cfg := config.Config{MetaRepoRoot: t.TempDir(), OpenSearchURL: "http://localhost:9202"}
	out := RenderStatus(context.Background(), cfg, fakeProbe{}, time.Now())
	for _, want := range []string{"docker: not running", "opensearch: unreachable", "daemon: stopped", "할 일: 'make daemon'", "judge: off", "distill: off"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
}

func TestRenderStatusRunning(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", Judge: config.Judge{Enabled: true}}
	if err := daemon.AcquireLock(root); err != nil {
		t.Fatal(err)
	}
	defer daemon.ReleaseLock(root)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	sum := daemon.CycleSummary{Scanned: 9, ColdOwned: 2, Refused: true}
	_ = daemon.WriteStatus(root, daemon.Status{
		PID: 1, StartedAt: "s", HeartbeatAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), IntervalSeconds: 60, Cycles: 4,
		LastCycle: &daemon.CycleRecord{Trigger: "timer", At: "a", Summary: &sum},
	})
	q := daemon.NewJudgeState(root)
	_ = q.Ledger.Append("t", daemon.JudgeRecord{PairKey: "k", Decision: daemon.DecisionQuestion})
	out := RenderStatus(context.Background(), cfg, fakeProbe{container: "exited", containerOK: true, health: "green", healthOK: true}, now)
	for _, want := range []string{"container opensearch: exited", "daemon: running", "cycles=4", "heartbeat 가 멈췄다", "REFUSED", "cold 로 넘어간 hot 원본 2개", "질문 1건", "make questions", "reconcile' 로 1회 색인"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
}

func TestQuestionsAndAnswer(t *testing.T) {
	root := t.TempDir()
	if out, _ := RenderQuestions(root); !strings.Contains(out, "없습니다") {
		t.Error("빈 질문")
	}
	s := daemon.NewJudgeState(root)
	old := daemon.Side{Path: "a.md", BodySHA: "a", EventTime: "2026-10-01T00:00:00Z"}
	neu := daemon.Side{Path: "b.md", BodySHA: "b", EventTime: "2026-10-02T00:00:00Z"}
	_ = s.Ledger.Append("t", daemon.JudgeRecord{PairKey: "k", Decision: daemon.DecisionQuestion, Old: old, New: neu, Reason: "?"})
	out, _ := RenderQuestions(root)
	if !strings.Contains(out, "old: a.md (2026-10-01)") {
		t.Errorf("questions = %s", out)
	}
	cases := []struct {
		answer   Answer
		decision daemon.Decision
		loser    string
	}{{AnswerLatest, daemon.DecisionSuperseded, "a.md"}, {AnswerOld, daemon.DecisionSuperseded, "b.md"}, {AnswerKeep, daemon.DecisionKeep, "a.md"}}
	for _, c := range cases {
		if _, err := AnswerQuestion(root, "k", c.answer, time.Now()); err != nil {
			t.Fatal(err)
		}
		latest, _ := s.Latest("t")
		if latest["k"].Decision != c.decision || latest["k"].Old.Path != c.loser || latest["k"].By != "human" {
			t.Errorf("%s → %+v", c.answer, latest["k"])
		}
		// 다음 답을 위해 다시 질문 상태로
		_ = s.Ledger.Append("t", daemon.JudgeRecord{PairKey: "k", Decision: daemon.DecisionQuestion, Old: old, New: neu})
	}
	if _, err := AnswerQuestion(root, "missing", AnswerKeep, time.Now()); err == nil {
		t.Error("없는 질문")
	}
	if _, ok := ParseAnswer("maybe"); ok {
		t.Error("잘못된 답")
	}
}

func TestCLIUsageErrors(t *testing.T) {
	cli := &CLI{Config: config.Config{MetaRepoRoot: t.TempDir()}, Out: &bytes.Buffer{}, Now: time.Now}
	for _, args := range [][]string{{"answer", "only-key"}, {"answer", "k", "maybe"}, {"nope"}} {
		if err := cli.Run(context.Background(), args[0], args[1:]); !errors.Is(err, ErrUsage) {
			t.Errorf("%v → %v", args, err)
		}
	}
	if err := cli.Run(context.Background(), "questions", []string{"-bogus"}); !errors.Is(err, ErrUsage) {
		t.Errorf("잘못된 플래그 → %v", err)
	}
}

func TestRenderTier(t *testing.T) {
	out := renderTier([]cold.TierDecision{
		{Index: "mem-a-2026.06", Action: cold.TierMoved, NewestEvent: "2026-06-15", RemovedFiles: []string{"docs/a.md"}},
		{Index: "mem-a-2026.10", Action: cold.TierKept},
	})
	for _, want := range []string{"tiered           mem-a-2026.06", "hot 에서 뺌: docs/a.md", "newest=-", "git commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
	if renderTier(nil) != "대상 인덱스 없음" {
		t.Error("빈 결과")
	}
}
