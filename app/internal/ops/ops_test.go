package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

type fakeProbe struct {
	containers   map[string]string
	containersOK bool
	health       string
	healthOK     bool
	stats        IndexStats
	graphOn      bool
	graphErr     error
}

func (f fakeProbe) Containers(context.Context) (map[string]string, bool) {
	return f.containers, f.containersOK
}
func (f fakeProbe) ClusterHealth(context.Context) (string, bool)  { return f.health, f.healthOK }
func (f fakeProbe) IndexStats(context.Context) (IndexStats, bool) { return f.stats, true }
func (f fakeProbe) Graph(context.Context) (graph.Stats, bool, error) {
	return graph.Stats{Nodes: map[string]int{"Doc": 4, "Entity": 2}, Edges: map[string]int{"LINKS_TO": 3}}, f.graphOn, f.graphErr
}

func TestRenderStatusAllDown(t *testing.T) {
	cfg := config.Config{MetaRepoRoot: t.TempDir(), OpenSearchURL: "http://localhost:9202"}
	out := RenderStatus(context.Background(), cfg, fakeProbe{}, time.Now())
	for _, want := range []string{"docker: not running", "opensearch: unreachable", "graph: off", "daemon: stopped", "make memory-daemon-start", "judge: off", "distill: off"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
}

func TestRenderStatusRunning(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", Judge: config.Judge{Enabled: true}, Graph: config.Graph{URI: "bolt://x"}}
	if err := state.AcquireLock(root); err != nil {
		t.Fatal(err)
	}
	defer state.ReleaseLock(root)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	sum := state.CycleSummary{Scanned: 9, ColdOwned: 2, Refused: true, Partial: true}
	_ = state.WriteStatus(root, state.Status{
		StartedAt: "s", HeartbeatAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), IntervalSeconds: 60, Cycles: 4,
		LastCycle: &state.CycleRecord{Trigger: "timer", At: "a", Summary: &sum, Graph: "docs=4 entities=2 edges=3"},
	})
	_ = state.NewJudgeState(root).Ledger.Append("t", state.JudgeRecord{PairKey: "k", Decision: state.DecisionQuestion})
	_ = state.NewTick(root, "tier").Done(now)
	probe := fakeProbe{containers: map[string]string{"opensearch": "running", "neo4j": "exited"}, containersOK: true, health: "green", healthOK: true, graphOn: true}
	out := RenderStatus(context.Background(), cfg, probe, now)
	for _, want := range []string{"container neo4j: exited", "graph: 4 docs, 2 entities, 3 edges", "daemon: running", "cycles=4", "heartbeat 가 멈췄다",
		"REFUSED", "PARTIAL-SCAN", "last graph: docs=4", "cold 로 넘어간 hot 원본 2개", "질문 1건", "scripts/memory/run.sh questions", "memory-daemon/run.sh once'", "tier: 마지막"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
	probe.graphErr = errors.New("down")
	if out := RenderStatus(context.Background(), cfg, probe, now); !strings.Contains(out, "graph: unreachable") {
		t.Errorf("graph down:\n%s", out)
	}
}

// R5-final-daemon-1: status 의 마지막 사이클 줄은 cold_kept(cold 로 갔지만 git 에 커밋된 내용이 아니라 tier 가 hot 에 남긴 원본)도
// 보이고, 있으면 커밋하라고 안내한다 — cold_owned 는 그런 원본을 세지 않으므로 status 만 보면 cold_owned=0 이 전부로 보였다.
func TestRenderStatusShowsColdKept(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws"}
	sum := state.CycleSummary{Scanned: 3, ColdKept: 3}
	_ = state.WriteStatus(root, state.Status{LastCycle: &state.CycleRecord{Trigger: "timer", At: "a", Summary: &sum}})

	out := RenderStatus(context.Background(), cfg, fakeProbe{}, time.Now())

	for _, want := range []string{"cold_owned=0 cold_kept=3 ", "hot 에 남긴 cold 원본 3개", "커밋하면 다음 tier 가"} {
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
	s := state.NewJudgeState(root)
	old := state.Side{Path: "a.md", BodySHA: "a", EventTime: "2026-10-01T00:00:00Z"}
	neu := state.Side{Path: "b.md", BodySHA: "b", EventTime: "2026-10-02T00:00:00Z"}
	_ = s.Ledger.Append("t", state.JudgeRecord{PairKey: "k", Decision: state.DecisionQuestion, Old: old, New: neu, Reason: "?"})
	out, _ := RenderQuestions(root)
	if !strings.Contains(out, "old: a.md (2026-10-01)") {
		t.Errorf("questions = %s", out)
	}
	cases := []struct {
		answer   Answer
		decision state.Decision
		loser    string
	}{{AnswerLatest, state.DecisionSuperseded, "a.md"}, {AnswerOld, state.DecisionSuperseded, "b.md"}, {AnswerKeep, state.DecisionKeep, "a.md"}}
	for _, c := range cases {
		if _, err := AnswerQuestion(root, "k", c.answer, time.Now()); err != nil {
			t.Fatal(err)
		}
		latest, _ := s.Latest("t")
		if latest["k"].Decision != c.decision || latest["k"].Old.Path != c.loser || latest["k"].By != "human" {
			t.Errorf("%s → %+v", c.answer, latest["k"])
		}
		_ = s.Ledger.Append("t", state.JudgeRecord{PairKey: "k", Decision: state.DecisionQuestion, Old: old, New: neu})
	}
	// 답을 바꿔도(old → latest) 어느 쪽이 구판인지는 기록 순서가 아니라 유효 시각으로 정한다
	for _, a := range []Answer{AnswerOld, AnswerLatest} {
		if _, err := AnswerQuestion(root, "k", a, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if latest, _ := s.Latest("t"); latest["k"].Old.Path != "a.md" || latest["k"].New.Path != "b.md" {
		t.Errorf("다시 답한 latest → %+v", latest["k"])
	}
	// 유효 시각이 같은 쌍도 질문이 정한 구판·신판을 따른다
	same := "2026-10-05T00:00:00Z"
	_ = s.Ledger.Append("t", state.JudgeRecord{PairKey: "tie", Decision: state.DecisionQuestion,
		Old: state.Side{Path: "c.md", BodySHA: "c", EventTime: same}, New: state.Side{Path: "d.md", BodySHA: "d", EventTime: same}})
	for _, a := range []Answer{AnswerOld, AnswerLatest} {
		if _, err := AnswerQuestion(root, "tie", a, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if latest, _ := s.Latest("t"); latest["tie"].Old.Path != "c.md" {
		t.Errorf("같은 시각 쌍을 다시 답한 latest → %+v", latest["tie"])
	}
	if _, err := AnswerQuestion(root, "missing", AnswerKeep, time.Now()); err == nil {
		t.Error("없는 질문")
	}
	if _, ok := ParseAnswer("maybe"); ok {
		t.Error("잘못된 답")
	}
}

func TestCLIUsage(t *testing.T) {
	cli := &CLI{Config: config.Config{MetaRepoRoot: t.TempDir()}, Out: &bytes.Buffer{}, Now: time.Now}
	for _, args := range [][]string{{"answer", "only-key"}, {"answer", "k", "maybe"}, {"nope"}, {"questions", "-bogus"}} {
		if err := cli.Run(context.Background(), args[0], args[1:]); !errors.Is(err, ErrUsage) {
			t.Errorf("%v → %v", args, err)
		}
	}
	usage := Usage()
	for name := range commands {
		if !strings.Contains(usage, "  "+name+" ") {
			t.Errorf("Usage 에 %s 가 없다", name)
		}
	}
	if err := cli.Run(context.Background(), "graph", []string{"stats"}); !errors.Is(err, graph.ErrDisabled) {
		t.Errorf("그래프가 없으면 꺼짐: %v", err)
	}
}

func TestRenderTier(t *testing.T) {
	out := renderTier([]cold.TierDecision{
		{Index: "mem-a-2026.06", Action: cold.TierMoved, NewestEvent: "2026-06-15", RemovedFiles: []string{"docs/a.md"}, GitRemoved: true},
		{Index: "mem-a-2026.10", Action: cold.TierKept},
	})
	for _, want := range []string{"tiered           mem-a-2026.06", "hot 에서 뺌: docs/a.md", "newest=-", "(git rm)", "git commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
	if renderTier(nil) != "대상 인덱스 없음" {
		t.Error("빈 결과")
	}
}

// R5-final-docs-4: git 이 다루지 않는 meta-repo 에서는 tier 가 원본 파일을 그냥 지운다 — git rm·git commit 을 말하지 않고,
// 원본이 cold store 와 스냅샷에만 남는다고 알린다.
func TestRenderTierOutsideGitDoesNotAskForACommit(t *testing.T) {
	out := renderTier([]cold.TierDecision{
		{Index: "mem-a-2026.06", Action: cold.TierMoved, NewestEvent: "2026-06-15", RemovedFiles: []string{"docs/a.md"}},
	})

	if strings.Contains(out, "git rm") || strings.Contains(out, "git commit") {
		t.Errorf("git 이 다루지 않는 meta-repo 에 git rm·commit 을 말한다:\n%s", out)
	}
	for _, want := range []string{"hot 원본 1개를 cold 로 넘겼습니다", "git 이 다루지 않는 meta-repo", "cold store raw 와 스냅샷에만"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
}

// OPS-3: git 이 되살릴 수 없어 hot 에 남긴 원본은 지운 원본과 따로, 무엇을 하면 되는지와 함께 보인다.
func TestRenderTierShowsOriginalsKeptForGit(t *testing.T) {
	out := renderTier([]cold.TierDecision{
		{Index: "mem-a-2026.06", Action: cold.TierMoved, NewestEvent: "2026-06-15", RemovedFiles: []string{"docs/a.md"}, KeptFiles: []string{"docs/new.md"}},
		{Index: "mem-a-2026.05", Action: cold.TierKeptUncommitted, KeptFiles: []string{"docs/old.md"}},
	})
	for _, want := range []string{"hot 에서 뺌: docs/a.md", "hot 에 남김: docs/new.md", "hot 에 남김: docs/old.md", "kept-uncommitted", "커밋"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q 없음:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hot 에서 뺌: docs/new.md") {
		t.Errorf("남긴 원본을 뺐다고 하지 않는다:\n%s", out)
	}
}

func TestRepoOfAndBranchWords(t *testing.T) {
	root := "/meta"
	cases := map[string][2]string{
		"/meta":                                 {"", "/meta"},
		"/meta/docs/x":                          {"", "/meta/docs/x"},
		"/meta/main/svc":                        {"svc", "/meta/main/svc"},
		"/meta/main/svc/src/pkg":                {"svc", "/meta/main/svc"},
		"/meta/main/svc/.worktrees/ISSUE-1/src": {"svc", "/meta/main/svc/.worktrees/ISSUE-1"},
		"/elsewhere":                            {"", "/elsewhere"},
	}
	for cwd, want := range cases {
		repo, dir := repoOf(root, cwd)
		if repo != want[0] || dir != want[1] {
			t.Errorf("repoOf(%s) = %s %s", cwd, repo, dir)
		}
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	run("init", "-q", "-b", "feat/order-retry_policy")
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644)
	run("add", "a")
	run("commit", "-q", "-m", "a")
	if got := branchWords(context.Background(), dir); !reflect.DeepEqual(got, []string{"feat", "order", "retry", "policy"}) {
		t.Errorf("branchWords = %v", got)
	}
}

func TestSessionContextIsSilentWithoutBackend(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "main", "svc")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// repo 가 잡히는 세션인데 OpenSearch 가 없다 — 검색까지 가서 실패해도 아무것도 출력하지 않는다
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: "t", OpenSearchURL: "http://127.0.0.1:1"}
	client := opensearch.New(cfg.OpenSearchURL)
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: client, Log: t.Logf})
	for _, in := range []string{"not json", fmt.Sprintf(`{"cwd":%q,"source":"startup"}`, cwd)} {
		cli := &CLI{Config: cfg, OS: client, Handlers: h, Out: &bytes.Buffer{}, In: strings.NewReader(in), Now: time.Now}
		out, err := runSessionContext(context.Background(), cli, nil)
		if err != nil || out != "" {
			t.Errorf("%s: 훅은 조용해야 한다: %q %v", in, out, err)
		}
	}
}

func TestBenchRefusesQuestionsInsideMetaRepo(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "docs", "bench.yaml")
	outside := filepath.Join(t.TempDir(), "bench.yaml")
	for _, p := range []string{inside, outside} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("questions:\n  - id: q\n    prompt: p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// claude 가 없는 경로를 줘서 질문 파일 검사까지만 돈다 (OpenSearch 도 필요 없다)
	cli := &CLI{Config: config.Config{MetaRepoRoot: root, LLM: config.LLM{ClaudeBin: filepath.Join(root, "no-claude")}}, Out: &bytes.Buffer{}, Now: time.Now}
	if err := cli.Run(context.Background(), "bench", nil); !errors.Is(err, ErrUsage) {
		t.Errorf("-questions 없이 = %v", err)
	}
	err := cli.Run(context.Background(), "bench", []string{"-questions", inside})
	if err == nil || !strings.Contains(err.Error(), "meta-repo 안") {
		t.Errorf("meta-repo 안의 질문 파일 = %v", err)
	}
	if err := cli.Run(context.Background(), "bench", []string{"-questions", outside, "-modes", "without"}); err == nil || strings.Contains(err.Error(), "meta-repo 안") {
		t.Errorf("밖의 질문 파일은 검사를 통과하고 claude 실행에서 실패해야 한다: %v", err)
	}
}

func TestRunPrintsWhatACommandDidBeforeItFailed(t *testing.T) {
	boom := errors.New("boom")
	commands["test-partial"] = command{offline: true, run: func(context.Context, *CLI, []string) (string, error) {
		return "tiered mem-t-2026.01", boom
	}}
	t.Cleanup(func() { delete(commands, "test-partial") })
	var out bytes.Buffer
	cli := &CLI{Out: &out}

	err := cli.Run(context.Background(), "test-partial", nil)

	// 파괴적인 명령(tier)이 중간에 실패해도 이미 한 일은 보여야 한다
	if !errors.Is(err, boom) || !strings.Contains(out.String(), "tiered mem-t-2026.01") {
		t.Errorf("out = %q err = %v", out.String(), err)
	}
}
