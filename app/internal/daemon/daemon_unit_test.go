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
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/state"
)

func TestDesiredSupersededByAndChains(t *testing.T) {
	s := state.NewJudgeState(t.TempDir())
	_ = s.Ledger.Append("t", state.JudgeRecord{PairKey: "k", Decision: state.DecisionSuperseded, Old: state.Side{Path: "x.md", BodySHA: "x"}, New: state.Side{Path: "y.md", BodySHA: "y"}})
	docs := map[string]ingest.IndexedDoc{
		"x.md":  {SourcePath: "x.md", Team: "t", Kind: model.KindEpisodic, BodySHA: "x"},
		"y.md":  {SourcePath: "y.md", Team: "t", Kind: model.KindEpisodic, BodySHA: "y"},
		"k1.md": {SourcePath: "k1.md", Team: "t"},
		"k2.md": {SourcePath: "k2.md", Team: "t", Supersedes: "k1.md"},
		"k3.md": {SourcePath: "k3.md", Team: "t", Supersedes: "k2.md"},
		"k4.md": {SourcePath: "k4.md", Team: "t", Supersedes: "missing.md"},
	}
	got, err := desiredSupersededBy(docs, s)
	want := map[string]string{"x.md": "y.md", "k1.md": "k3.md", "k2.md": "k3.md"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("desired = %v %v (A→B→C 는 마지막 판을 가리킨다)", got, err)
	}
	if got := compressChains(map[string]string{"a": "b", "b": "a"}); len(got) > 2 {
		t.Errorf("순환은 끊는다: %v", got)
	}
}

// 판정은 본문 sha 쌍에 묶인다 — 파일 이름을 바꿔도(git mv) 결론이 내용을 따라 새 경로로 간다.
// 같은 쌍은 다시 판정하지 않으므로(멱등) 경로만 보면 밀렸던 문서가 영영 되살아난다.
func TestDesiredSupersededByFollowsContent(t *testing.T) {
	s := state.NewJudgeState(t.TempDir())
	rec := state.JudgeRecord{PairKey: state.PairKey("sha-l", "sha-w"), Decision: state.DecisionSuperseded,
		Old: state.Side{Path: "l.md", BodySHA: "sha-l"}, New: state.Side{Path: "w.md", BodySHA: "sha-w"}}
	if err := s.Ledger.Append("t", rec); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		docs map[string]string // 경로 → 본문 sha (팀 t)
		want map[string]string
	}{
		{"그대로", map[string]string{"l.md": "sha-l", "w.md": "sha-w"}, map[string]string{"l.md": "w.md"}},
		{"이긴 쪽 이름 바꿈", map[string]string{"l.md": "sha-l", "w2.md": "sha-w"}, map[string]string{"l.md": "w2.md"}},
		{"진 쪽 이름 바꿈", map[string]string{"l2.md": "sha-l", "w.md": "sha-w"}, map[string]string{"l2.md": "w.md"}},
		{"둘 다 이름 바꿈", map[string]string{"l2.md": "sha-l", "w2.md": "sha-w"}, map[string]string{"l2.md": "w2.md"}},
		{"이긴 쪽 삭제 — 진 쪽이 다시 보인다", map[string]string{"l.md": "sha-l"}, map[string]string{}},
		{"진 쪽 본문 수정 — 새 쌍이라 재판정 몫", map[string]string{"l.md": "sha-l2", "w.md": "sha-w"}, map[string]string{}},
		{"이긴 쪽 본문 수정(제자리) — 결론 유지", map[string]string{"l.md": "sha-l", "w.md": "sha-w2"}, map[string]string{"l.md": "w.md"}},
		{"이긴 쪽 이름 바꾸고 옛 경로에 다른 문서 — 내용이 먼저", map[string]string{"l.md": "sha-l", "w.md": "other", "w2.md": "sha-w"}, map[string]string{"l.md": "w2.md"}},
		{"이긴 쪽 사본이 여럿 — 경로 순 첫째", map[string]string{"l.md": "sha-l", "w3.md": "sha-w", "w2.md": "sha-w"}, map[string]string{"l.md": "w2.md"}},
		{"진 쪽 사본도 밀린다 — 같은 쌍은 다시 판정하지 않는다", map[string]string{"l.md": "sha-l", "l2.md": "sha-l", "w.md": "sha-w"}, map[string]string{"l.md": "w.md", "l2.md": "w.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs := map[string]ingest.IndexedDoc{}
			for p, sha := range tc.docs {
				docs[p] = ingest.IndexedDoc{SourcePath: p, Team: "t", Kind: model.KindEpisodic, BodySHA: sha}
			}
			got, err := desiredSupersededBy(docs, s)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("desired = %v %v, want %v", got, err, tc.want)
			}
		})
	}

	t.Run("다른 팀의 같은 본문은 따라가지 않는다", func(t *testing.T) {
		docs := map[string]ingest.IndexedDoc{
			"l.md":  {SourcePath: "l.md", Team: "t", Kind: model.KindEpisodic, BodySHA: "sha-l"},
			"w2.md": {SourcePath: "w2.md", Team: "u", Kind: model.KindEpisodic, BodySHA: "sha-w"},
		}
		if got, err := desiredSupersededBy(docs, s); err != nil || len(got) != 0 {
			t.Errorf("desired = %v %v", got, err)
		}
	})
}

func TestSerialCoalesces(t *testing.T) {
	var (
		mu      sync.Mutex
		runs    []string
		active  int32
		maxSeen int32
	)
	release := make(chan struct{})
	s := newSerial(func(reason string) {
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
	j := &judge{Config: cfg, Ask: ask, Now: func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }}
	r, err := j.Decide(context.Background(), "k", sd("old.md", "01"), sd("new.md", "12"))
	if err != nil || r.Decision != state.DecisionSuperseded || r.By != "latest-wins" || r.Old.Path != "old.md" || ask.calls != 0 {
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
		r, err := j.Decide(context.Background(), "k", sd("old.md", "10"), sd("new.md", "12"))
		if err != nil || r.Decision != c.decision || r.By != c.by || r.Old.Path != c.loser {
			t.Errorf("%s → %+v", c.raw, r)
		}
	}
}
