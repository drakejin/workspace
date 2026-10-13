package daemon

import (
	"reflect"
	"testing"

	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/state"
)

// judgedDoc 은 판정 범위(팀 t, 프로젝트 alpha, spec)의 episodic 문서다.
func judgedDoc(path, bodySHA string) ingest.IndexedDoc {
	return ingest.IndexedDoc{SourcePath: path, Team: "t", Repo: "alpha", DocType: model.DocSpec, Kind: model.KindEpisodic, BodySHA: bodySHA}
}

// supersededLedger 는 팀 t 의 판정 원장에 superseded 결론들을 차례로 붙인다.
func supersededLedger(t *testing.T, records ...state.JudgeRecord) *state.JudgeState {
	t.Helper()
	s := state.NewJudgeState(t.TempDir())
	for _, r := range records {
		r.PairKey = state.PairKey(r.Old.BodySHA, r.New.BodySHA)
		if r.Decision == "" {
			r.Decision = state.DecisionSuperseded
		}
		if err := s.Ledger.Append("t", r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func supersedes(oldPath, oldSHA, newPath, newSHA string) state.JudgeRecord {
	return state.JudgeRecord{Old: state.Side{Path: oldPath, BodySHA: oldSHA}, New: state.Side{Path: newPath, BodySHA: newSHA}}
}

func assertDesired(t *testing.T, s *state.JudgeState, docs []ingest.IndexedDoc, want map[string]string) {
	t.Helper()
	byPath := make(map[string]ingest.IndexedDoc, len(docs))
	for _, d := range docs {
		byPath[d.SourcePath] = d
	}
	got, err := desiredSupersededBy(byPath, s)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("desired = %v %v, want %v", got, err, want)
	}
}

// R1-judge-1: 두 쪽 본문이 똑같은 쌍(사본 — latest-wins 가 frontmatter 날짜만 다른 사본을 밀어낸다)은 내용으로 이긴 쪽을
// 가릴 수 없다. 이긴 쪽 이름을 바꿨을 때 같은 본문의 진 쪽을 이긴 쪽으로 오인하면 결론이 뒤집혀 최신 사본이 숨는다.
func TestDesiredSupersededByNeverFlipsIdenticalCopies(t *testing.T) {
	s := supersededLedger(t, supersedes("b-old.md", "same", "c-new.md", "same"))
	cases := []struct {
		name string
		docs []ingest.IndexedDoc
		want map[string]string
	}{
		{"그대로", []ingest.IndexedDoc{judgedDoc("b-old.md", "same"), judgedDoc("c-new.md", "same")},
			map[string]string{"b-old.md": "c-new.md"}},
		{"이긴 쪽 이름 바꿈 — 어느 쪽이 이겼는지 몰라 결론이 풀린다(뒤집히지 않는다)",
			[]ingest.IndexedDoc{judgedDoc("b-old.md", "same"), judgedDoc("z-new.md", "same")}, map[string]string{}},
		{"이긴 쪽 이름을 진 쪽보다 앞 순서로 바꿈", []ingest.IndexedDoc{judgedDoc("b-old.md", "same"), judgedDoc("a-new.md", "same")},
			map[string]string{}},
		{"진 쪽 이름 바꿈 — 이긴 쪽이 제자리라 결론이 따라간다",
			[]ingest.IndexedDoc{judgedDoc("a-old.md", "same"), judgedDoc("c-new.md", "same")}, map[string]string{"a-old.md": "c-new.md"}},
		{"이긴 쪽 본문 수정(제자리) — 결론 유지", []ingest.IndexedDoc{judgedDoc("b-old.md", "same"), judgedDoc("c-new.md", "edited")},
			map[string]string{"b-old.md": "c-new.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertDesired(t, s, tc.docs, tc.want) })
	}
}

// R1-judge-2: 결론은 판정이 견주는 범위(bestNeighbor: 같은 팀의 episodic, 같은 프로젝트·같은 문서 종류) 안에서만 내용을
// 따라간다. knowledge 는 유사도로 판정하지 않고(GUIDE 5.3), 다른 프로젝트·종류의 사본은 이긴 쪽과 견준 적이 없다.
func TestDesiredSupersededByStaysInTheJudgedScope(t *testing.T) {
	s := supersededLedger(t, supersedes("docs/feature/l.md", "sha-l", "docs/feature/w.md", "sha-w"))
	winner := judgedDoc("docs/feature/w.md", "sha-w")
	other := func(path, sha string, edit func(*ingest.IndexedDoc)) ingest.IndexedDoc {
		d := judgedDoc(path, sha)
		edit(&d)
		return d
	}
	cases := []struct {
		name string
		docs []ingest.IndexedDoc
		want map[string]string
	}{
		{"knowledge 로 옮긴 진 쪽은 숨기지 않는다", []ingest.IndexedDoc{winner,
			other("docs/knowledge/l.md", "sha-l", func(d *ingest.IndexedDoc) { d.Kind, d.DocType = model.KindKnowledge, model.DocKnowledge })},
			map[string]string{}},
		{"다른 프로젝트의 진 쪽 사본은 숨기지 않는다", []ingest.IndexedDoc{winner, judgedDoc("docs/feature/l.md", "sha-l"),
			other("docs/beta/l.md", "sha-l", func(d *ingest.IndexedDoc) { d.Repo = "beta" })},
			map[string]string{"docs/feature/l.md": "docs/feature/w.md"}},
		{"프로젝트 없는 사본도 프로젝트 있는 쌍과 따로다", []ingest.IndexedDoc{winner,
			other("docs/l.md", "sha-l", func(d *ingest.IndexedDoc) { d.Repo = "" })},
			map[string]string{}},
		{"다른 종류(ADR)의 진 쪽 사본은 숨기지 않는다", []ingest.IndexedDoc{winner,
			other("docs/feature/adr/l.md", "sha-l", func(d *ingest.IndexedDoc) { d.DocType = model.DocADR })},
			map[string]string{}},
		{"이긴 쪽이 다른 프로젝트로 옮겨 가면 견줄 상대가 없어 결론이 풀린다", []ingest.IndexedDoc{judgedDoc("docs/feature/l.md", "sha-l"),
			other("docs/beta/w.md", "sha-w", func(d *ingest.IndexedDoc) { d.Repo = "beta" })},
			map[string]string{}},
		{"다른 프로젝트에 두 쪽 사본이 다 있으면 그 범위 안에서 따라간다", []ingest.IndexedDoc{winner,
			other("docs/beta/l.md", "sha-l", func(d *ingest.IndexedDoc) { d.Repo = "beta" }),
			other("docs/beta/w.md", "sha-w", func(d *ingest.IndexedDoc) { d.Repo = "beta" })},
			map[string]string{"docs/beta/l.md": "docs/beta/w.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertDesired(t, s, tc.docs, tc.want) })
	}
}

// R1-judge-3: 기록한 이긴 쪽 경로가 아직 이긴 본문을 갖고 있으면, 경로 순으로 앞서는 사본이 있어도 기록한 경로를 가리킨다.
func TestDesiredSupersededByPrefersTheRecordedWinnerPath(t *testing.T) {
	s := supersededLedger(t, supersedes("l.md", "sha-l", "w.md", "sha-w"))
	docs := []ingest.IndexedDoc{judgedDoc("l.md", "sha-l"), judgedDoc("w.md", "sha-w"), judgedDoc("a-copy.md", "sha-w")}

	assertDesired(t, s, docs, map[string]string{"l.md": "w.md"})
}

// R1-judge-4: 진 쪽 본문이 같은 결론이 둘(서로 다른 이긴 쪽)이면 원장에 나중에 붙은 결론이 이긴다 — 사이클마다
// superseded_by 가 오가며 인덱스를 다시 쓰지 않는다.
func TestDesiredSupersededByLatestDecisionWins(t *testing.T) {
	s := supersededLedger(t,
		supersedes("l.md", "sha-l", "v.md", "sha-v"),
		supersedes("l-copy.md", "sha-l", "w.md", "sha-w"),
	)
	docs := []ingest.IndexedDoc{judgedDoc("l.md", "sha-l"), judgedDoc("l-copy.md", "sha-l"), judgedDoc("v.md", "sha-v"), judgedDoc("w.md", "sha-w")}
	want := map[string]string{"l.md": "w.md", "l-copy.md": "w.md"}

	for range determinismRuns {
		assertDesired(t, s, docs, want)
	}
}

// determinismRuns 는 map 순회 순서에 기대는 결과를 잡으려고 같은 계산을 되풀이하는 횟수다.
const determinismRuns = 50
