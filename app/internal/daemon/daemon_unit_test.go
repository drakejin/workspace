package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/llm"
	"github.com/drakejin/workspace/app/internal/state"
)

func TestDesiredSupersededByAndChains(t *testing.T) {
	s := state.NewJudgeState(t.TempDir())
	_ = s.Ledger.Append("t", state.JudgeRecord{PairKey: "k", Decision: state.DecisionSuperseded, Old: state.Side{Path: "x.md", BodySHA: "x"}, New: state.Side{Path: "y.md", BodySHA: "y"}})
	docs := map[string]ingest.IndexedDoc{
		"x.md":  {SourcePath: "x.md", Team: "t", BodySHA: "x"},
		"y.md":  {SourcePath: "y.md", Team: "t", BodySHA: "y"},
		"k1.md": {SourcePath: "k1.md", Team: "t"},
		"k2.md": {SourcePath: "k2.md", Team: "t", Supersedes: "k1.md"},
		"k3.md": {SourcePath: "k3.md", Team: "t", Supersedes: "k2.md"},
		"k4.md": {SourcePath: "k4.md", Team: "t", Supersedes: "missing.md"},
	}
	got, err := DesiredSupersededBy(docs, s)
	want := map[string]string{"x.md": "y.md", "k1.md": "k3.md", "k2.md": "k3.md"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("desired = %v %v (A→B→C 는 마지막 판을 가리킨다)", got, err)
	}
	if got := compressChains(map[string]string{"a": "b", "b": "a"}); len(got) > 2 {
		t.Errorf("순환은 끊는다: %v", got)
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
		return side{Side: state.Side{Path: p, BodySHA: p + "-sha", EventTime: "2026-10-" + day + "T00:00:00Z"}, text: p}
	}
	ask := &stubAsker{}
	j := &Judge{Config: cfg, Ask: ask, Now: func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }}
	r := j.Decide(context.Background(), "k", sd("old.md", "01"), sd("new.md", "12"))
	if r.Decision != state.DecisionSuperseded || r.By != "latest-wins" || r.Old.Path != "old.md" || ask.calls != 0 {
		t.Errorf("latest-wins = %+v calls=%d", r, ask.calls)
	}
	cases := []struct {
		raw      string
		ok       bool
		decision state.Decision
		by       string
		loser    string
	}{
		{"", false, state.DecisionQuestion, "fallback", "old.md"},
		{`{"contradicts":false,"winner":"new","confident":true}`, true, state.DecisionKeep, "llm", "old.md"},
		{`{"contradicts":true,"winner":"new","confident":false}`, true, state.DecisionQuestion, "llm", "old.md"},
		{`{"contradicts":true,"winner":"new","confident":true}`, true, state.DecisionSuperseded, "llm", "old.md"},
		{`{"contradicts":true,"winner":"old","confident":true}`, true, state.DecisionSuperseded, "llm", "new.md"},
		{`{"contradicts":"yes"}`, true, state.DecisionQuestion, "fallback", "old.md"},
	}
	for _, c := range cases {
		j.Ask = &stubAsker{raw: c.raw, ok: c.ok}
		r := j.Decide(context.Background(), "k", sd("old.md", "10"), sd("new.md", "12"))
		if r.Decision != c.decision || r.By != c.by || r.Old.Path != c.loser {
			t.Errorf("%s → %+v", c.raw, r)
		}
	}
}
