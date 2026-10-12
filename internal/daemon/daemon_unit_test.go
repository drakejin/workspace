package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/llm"
)

func TestLedgerSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	l := NewLedger[map[string]int](dir)
	var bad []int
	l.OnCorrupt = func(_ string, line int) { bad = append(bad, line) }
	if got, err := l.ReadAll("t"); err != nil || got != nil {
		t.Errorf("없는 파일 = %v %v", got, err)
	}
	if err := l.Append("t", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.jsonl"), []byte("{\"n\":1}\n{broken\n\n{\"n\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := l.ReadAll("t")
	if len(got) != 2 || got[1]["n"] != 2 || !reflect.DeepEqual(bad, []int{2}) {
		t.Errorf("got=%v bad=%v", got, bad)
	}
	if teams, _ := l.Teams(); !reflect.DeepEqual(teams, []string{"t"}) {
		t.Errorf("teams = %v", teams)
	}
	if teams, _ := NewLedger[int](filepath.Join(dir, "none")).Teams(); teams != nil {
		t.Error("없는 디렉토리")
	}
}

func TestJudgeState(t *testing.T) {
	if PairKey("aaaa", "bbbb") != PairKey("bbbb", "aaaa") {
		t.Error("pairKey 는 방향 무관")
	}
	s := NewJudgeState(t.TempDir())
	old := Side{Path: "a.md", BodySHA: "sha-a", EventTime: "2026-10-01"}
	neu := Side{Path: "b.md", BodySHA: "sha-b", EventTime: "2026-10-02"}
	_ = s.Ledger.Append("t", JudgeRecord{PairKey: "k1", Decision: DecisionQuestion, Old: old, New: neu})
	if q, _ := s.PendingQuestions("t"); len(q) != 1 {
		t.Errorf("질문 = %v", q)
	}
	_ = s.Ledger.Append("t", JudgeRecord{PairKey: "k1", Decision: DecisionSuperseded, Old: old, New: neu, By: "human"})
	if q, _ := s.PendingQuestions("t"); len(q) != 0 {
		t.Error("답이 붙으면 질문이 사라진다")
	}
	shas := map[string]string{"a.md": "sha-a", "b.md": "sha-b"}
	lookup := func(m map[string]string) func(string) (string, bool) {
		return func(p string) (string, bool) { v, ok := m[p]; return v, ok }
	}
	if m, _ := s.SupersededMap("t", lookup(shas)); m["a.md"] != "b.md" {
		t.Errorf("map = %v", m)
	}
	if m, _ := s.SupersededMap("t", lookup(map[string]string{"a.md": "edited", "b.md": "sha-b"})); len(m) != 0 {
		t.Error("구판 본문이 바뀌면 결론 무효")
	}
	if m, _ := s.SupersededMap("t", lookup(map[string]string{"a.md": "sha-a"})); len(m) != 0 {
		t.Error("신판이 없으면 결론 무효")
	}
}

func TestDesiredSupersededBy(t *testing.T) {
	s := NewJudgeState(t.TempDir())
	_ = s.Ledger.Append("t", JudgeRecord{PairKey: "k", Decision: DecisionSuperseded, Old: Side{Path: "x.md", BodySHA: "x"}, New: Side{Path: "y.md", BodySHA: "y"}})
	docs := map[string]IndexedDoc{
		"x.md":  {SourcePath: "x.md", Team: "t", BodySHA: "x"},
		"y.md":  {SourcePath: "y.md", Team: "t", BodySHA: "y"},
		"k1.md": {SourcePath: "k1.md", Team: "t"},
		"k2.md": {SourcePath: "k2.md", Team: "t", Supersedes: "k1.md"},
		"k3.md": {SourcePath: "k3.md", Team: "t", Supersedes: "missing.md"},
	}
	got, err := DesiredSupersededBy(docs, s)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"x.md": "y.md", "k1.md": "k2.md"}) {
		t.Errorf("desired = %v %v", got, err)
	}
}

func TestDistillCache(t *testing.T) {
	root := t.TempDir()
	c := NewDistillCache(root)
	if _, ok := c.Get("t", "sha"); ok {
		t.Error("빈 캐시")
	}
	if err := c.Put("t", DistillRecord{BodySHA: "sha", SourcePath: "docs/a.md", Summary: "요약", Entities: []string{"concept:x"}}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewDistillCache(root)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if d, ok := reloaded.Get("t", "sha"); !ok || d.Summary != "요약" || reloaded.Size("") != 1 || reloaded.Size("t") != 1 || reloaded.Size("x") != 0 {
		t.Errorf("reloaded = %+v %v", d, ok)
	}
}

func TestScanHot(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	write("README.md", "# r")
	write("docs/a/x.md", "# x")
	write("docs/a/big.md", string(make([]byte, 200)))
	write("main/svc/docs/y.md", "# y")
	write("main/svc/src/z.md", "# no")
	write("main/svc/.worktrees/feat/docs/w.md", "# no")
	write("app/README.md", "# no")
	files, skipped, err := ScanHot(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
	}
	if !reflect.DeepEqual(rels, []string{"README.md", "docs/a/x.md", "main/svc/docs/y.md"}) || files[0].Size != 3 {
		t.Errorf("files = %v", rels)
	}
	if len(skipped) != 1 || skipped[0].Rel != "docs/a/big.md" {
		t.Errorf("skipped = %v", skipped)
	}
	if bin, _ := IsProbablyBinary(filepath.Join(root, "docs/a/big.md")); !bin {
		t.Error("NUL 바이트면 바이너리")
	}
	if bin, _ := IsProbablyBinary(filepath.Join(root, "README.md")); bin {
		t.Error("텍스트")
	}
	if _, _, err := ScanHot(filepath.Join(root, "missing"), 100); err == nil {
		t.Error("루트가 없으면 실패")
	}
	locked := filepath.Join(root, "docs/a")
	_ = os.Chmod(locked, 0o000)
	defer os.Chmod(locked, 0o755)
	files, skipped, err = ScanHot(root, 100)
	if !errors.Is(err, ErrPartialScan) || len(files) != 2 || skipped[len(skipped)-1].Rel != "docs/a" {
		t.Errorf("partial = %v files=%d skipped=%v", err, len(files), skipped)
	}
}

func TestSerialCoalesces(t *testing.T) {
	var (
		mu      sync.Mutex
		runs    []string
		active  int32
		maxSeen int32
	)
	release := make(chan struct{})
	s := NewSerial(func(reason string) {
		n := atomic.AddInt32(&active, 1)
		if n > atomic.LoadInt32(&maxSeen) {
			atomic.StoreInt32(&maxSeen, n)
		}
		mu.Lock()
		runs = append(runs, reason)
		mu.Unlock()
		if reason == "boot" {
			<-release
		}
		atomic.AddInt32(&active, -1)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.Run("boot") }()
	time.Sleep(20 * time.Millisecond)
	for _, r := range []string{"watch", "timer"} {
		wg.Add(1)
		go func(r string) { defer wg.Done(); s.Run(r) }(r)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if len(runs) != 2 || runs[0] != "boot" || maxSeen != 1 {
		t.Errorf("runs = %v max = %d (재실행 1회로 합쳐지고 동시에 둘이 돌지 않는다)", runs, maxSeen)
	}
}

type stubAsker struct {
	raw   string
	ok    bool
	calls int
}

func (s *stubAsker) Ask(context.Context, llm.Input) (json.RawMessage, bool) {
	s.calls++
	return json.RawMessage(s.raw), s.ok
}

func TestDecide(t *testing.T) {
	cfg := config.Config{Judge: config.Judge{Enabled: true, WindowDays: 7, MinScore: 0.3}}
	sd := func(p, day string) side {
		return side{Side: Side{Path: p, BodySHA: p + "-sha", EventTime: "2026-10-" + day + "T00:00:00Z"}, text: p}
	}
	ask := &stubAsker{}
	j := &Judge{Config: cfg, Ask: ask, Now: func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }}
	r := j.Decide(context.Background(), "k", sd("old.md", "01"), sd("new.md", "12"))
	if r.Decision != DecisionSuperseded || r.By != "latest-wins" || r.Old.Path != "old.md" || ask.calls != 0 {
		t.Errorf("latest-wins = %+v calls=%d", r, ask.calls)
	}
	cases := []struct {
		raw      string
		ok       bool
		decision Decision
		by       string
		loser    string
	}{
		{"", false, DecisionQuestion, "fallback", "old.md"},
		{`{"contradicts":false,"winner":"new","confident":true}`, true, DecisionKeep, "llm", "old.md"},
		{`{"contradicts":true,"winner":"new","confident":false}`, true, DecisionQuestion, "llm", "old.md"},
		{`{"contradicts":true,"winner":"new","confident":true}`, true, DecisionSuperseded, "llm", "old.md"},
		{`{"contradicts":true,"winner":"old","confident":true}`, true, DecisionSuperseded, "llm", "new.md"},
		{`{"contradicts":"yes"}`, true, DecisionQuestion, "fallback", "old.md"},
	}
	for _, c := range cases {
		j.Ask = &stubAsker{raw: c.raw, ok: c.ok}
		r := j.Decide(context.Background(), "k", sd("old.md", "10"), sd("new.md", "12"))
		if r.Decision != c.decision || r.By != c.by || r.Old.Path != c.loser {
			t.Errorf("%s → %+v", c.raw, r)
		}
	}
}

func TestLockAndStatus(t *testing.T) {
	root := t.TempDir()
	if RunningPID(root) != 0 || ReadStatus(root) != nil {
		t.Error("처음엔 없음")
	}
	if err := AcquireLock(root); err != nil {
		t.Fatal(err)
	}
	if RunningPID(root) != os.Getpid() {
		t.Error("락을 잡은 pid")
	}
	// 죽은 pid 가 남긴 락은 무시한다
	_ = os.WriteFile(pidFile(root), []byte("999999"), 0o644)
	if RunningPID(root) != 0 {
		t.Error("죽은 pid")
	}
	// 살아 있는 다른 pid 면 거부한다 (pid 1 은 항상 산다)
	_ = os.WriteFile(pidFile(root), []byte("1"), 0o644)
	var running *AlreadyRunningError
	if err := AcquireLock(root); err == nil || !asRunning(err, &running) || running.PID != 1 {
		t.Errorf("AcquireLock = %v", err)
	}
	_ = os.WriteFile(pidFile(root), []byte("x"), 0o644)
	if err := AcquireLock(root); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseLock(root); err != nil || RunningPID(root) != 0 {
		t.Error("release")
	}
	sum := CycleSummary{Scanned: 3}
	if err := WriteStatus(root, Status{PID: 1, Cycles: 2, LastCycle: &CycleRecord{Trigger: "boot", Summary: &sum}}); err != nil {
		t.Fatal(err)
	}
	if s := ReadStatus(root); s == nil || s.Cycles != 2 || s.LastCycle.Summary.Scanned != 3 {
		t.Errorf("status = %+v", s)
	}
}

func asRunning(err error, target **AlreadyRunningError) bool {
	r, ok := err.(*AlreadyRunningError)
	if ok {
		*target = r
	}
	return ok
}
