package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
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

// 마지막 줄이 줄바꿈 없이 끝나 있으면(짧은 쓰기·전원 손실, git 병합·편집기가 마지막 \n 을 뺀 경우) 새 기록을 그 줄에
// 이어 붙이지 않는다 — 이어 붙이면 새 기록(판정·사람의 답)까지 깨진 줄로 버려진다.
func TestLedgerAppendAfterTruncatedLine(t *testing.T) {
	dir := t.TempDir()
	l := NewLedger[JudgeRecord](dir)
	var bad []int
	l.OnCorrupt = func(_ string, line int) { bad = append(bad, line) }
	if err := l.Append("t", JudgeRecord{PairKey: "a", Decision: DecisionKeep}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "t.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"pairKey":"b","decis`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := l.Append("t", JudgeRecord{PairKey: "c", Decision: DecisionSuperseded}); err != nil {
		t.Fatal(err)
	}

	got, err := l.ReadAll("t")
	if err != nil {
		t.Fatal(err)
	}
	if keys := pairKeysOf(got); !reflect.DeepEqual(keys, []string{"a", "c"}) || !reflect.DeepEqual(bad, []int{2}) {
		t.Errorf("records=%v corrupt=%v — 잘린 줄만 버리고 새 기록은 살아야 한다", keys, bad)
	}
}

// 줄바꿈으로 끝나는 원장(빈 파일 포함)에는 빈 줄을 끼우지 않는다.
func TestLedgerAppendKeepsOneRecordPerLine(t *testing.T) {
	dir := t.TempDir()
	l := NewLedger[JudgeRecord](dir)
	if err := os.WriteFile(filepath.Join(dir, "t.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if err := l.Append("t", JudgeRecord{PairKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) != 3 || lines[2] != "" || !strings.Contains(lines[0], `"a"`) || !strings.Contains(lines[1], `"b"`) {
		t.Errorf("raw = %q", raw)
	}
}

func pairKeysOf(records []JudgeRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.PairKey)
	}
	return out
}

func TestLedgersLiveUnderCache(t *testing.T) {
	root := t.TempDir()
	_ = NewJudgeState(root).Ledger.Append("t", JudgeRecord{PairKey: "k"})
	_ = NewArchiveState(root).Ledger.Append("t", ArchiveRecord{Action: ArchiveDeprecate, Path: "p"})
	_ = NewDistillCache(root).Put("t", DistillRecord{BodySHA: "s"})
	for _, rel := range []string{".cache/judge/t.jsonl", ".cache/archive/t.jsonl", ".cache/distill/t.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s 없음", rel)
		}
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
	if all, _ := s.AllPendingQuestions(); len(all["t"]) != 1 {
		t.Errorf("질문 = %v", all)
	}
	_ = s.Ledger.Append("t", JudgeRecord{PairKey: "k1", Decision: DecisionSuperseded, Old: old, New: neu, By: "human"})
	if q, _ := s.PendingQuestions("t"); len(q) != 0 {
		t.Error("답이 붙으면 질문이 사라진다")
	}
	if m, _ := s.SupersededMap("t", oneScope(map[string]string{"a.md": "sha-a", "b.md": "sha-b"})); m["a.md"] != "b.md" {
		t.Errorf("map = %v", m)
	}
	if m, _ := s.SupersededMap("t", oneScope(map[string]string{"a.md": "edited", "b.md": "sha-b"})); len(m) != 0 {
		t.Error("구판 본문이 바뀌면 결론 무효")
	}
	if m, _ := s.SupersededMap("t", oneScope(map[string]string{"a.md": "sha-a"})); len(m) != 0 {
		t.Error("신판이 없으면 결론 무효")
	}
	if m, _ := s.SupersededMap("t", oneScope(map[string]string{"a2.md": "sha-a", "b2.md": "sha-b"})); !reflect.DeepEqual(m, map[string]string{"a2.md": "b2.md"}) {
		t.Errorf("이름을 바꿔도 결론은 본문을 따라간다: %v", m)
	}
	blank := NewJudgeState(t.TempDir())
	_ = blank.Ledger.Append("t", JudgeRecord{PairKey: "k", Decision: DecisionSuperseded, Old: Side{Path: "x.md"}, New: Side{Path: "y.md"}})
	if m, _ := blank.SupersededMap("t", oneScope(map[string]string{"y.md": "", "z.md": ""})); len(m) != 0 {
		t.Errorf("본문 sha 를 모르는 문서끼리는 내용으로 잇지 않는다: %v", m)
	}
}

// oneScope 는 경로 → 본문 sha 를 한 판정 범위의 문서들로 만든다.
func oneScope(bodies map[string]string) map[string]JudgedDoc {
	out := make(map[string]JudgedDoc, len(bodies))
	for p, sha := range bodies {
		out[p] = JudgedDoc{BodySHA: sha, Scope: "alpha/spec"}
	}
	return out
}

// R1-judge-4: 같은 문서를 두 결론이 가리키면 원장에 나중에 붙은 결론이 이긴다 — 질문에 나중에 붙은 답은 그 사이의 다른
// 결론보다 나중이다. map 순회 순서에 기대면 사이클마다 superseded_by 가 오간다.
func TestSupersededMapLatestRecordWins(t *testing.T) {
	s := NewJudgeState(t.TempDir())
	lv := JudgeRecord{PairKey: "l:v", Old: Side{Path: "l.md", BodySHA: "l"}, New: Side{Path: "v.md", BodySHA: "v"}}
	lw := JudgeRecord{PairKey: "l:w", Decision: DecisionSuperseded, Old: Side{Path: "l.md", BodySHA: "l"}, New: Side{Path: "w.md", BodySHA: "w"}}
	question, answer := lv, lv
	question.Decision, answer.Decision = DecisionQuestion, DecisionSuperseded
	for _, r := range []JudgeRecord{question, lw, answer} {
		if err := s.Ledger.Append("t", r); err != nil {
			t.Fatal(err)
		}
	}
	docs := oneScope(map[string]string{"l.md": "l", "v.md": "v", "w.md": "w"})

	for range 50 {
		if m, err := s.SupersededMap("t", docs); err != nil || !reflect.DeepEqual(m, map[string]string{"l.md": "v.md"}) {
			t.Fatalf("map = %v %v — 나중에 붙은 답(l→v)이 이긴다", m, err)
		}
	}
}

// R1-judge-2: 결론은 같은 판정 범위 안에서만 내용을 따라간다.
func TestSupersededMapStaysInScope(t *testing.T) {
	s := NewJudgeState(t.TempDir())
	_ = s.Ledger.Append("t", JudgeRecord{PairKey: "l:w", Decision: DecisionSuperseded, Old: Side{Path: "l.md", BodySHA: "l"}, New: Side{Path: "w.md", BodySHA: "w"}})
	docs := map[string]JudgedDoc{
		"l.md":      {BodySHA: "l", Scope: "alpha"},
		"w.md":      {BodySHA: "w", Scope: "alpha"},
		"beta/l.md": {BodySHA: "l", Scope: "beta"},
	}

	m, err := s.SupersededMap("t", docs)

	if err != nil || !reflect.DeepEqual(m, map[string]string{"l.md": "w.md"}) {
		t.Errorf("map = %v %v — 다른 범위의 사본은 숨기지 않는다", m, err)
	}
}

func TestArchiveDeprecated(t *testing.T) {
	s := NewArchiveState(t.TempDir())
	_ = s.Ledger.Append("t", ArchiveRecord{Action: ArchiveDeprecate, Path: "a.md"})
	_ = s.Ledger.Append("t", ArchiveRecord{Action: ArchiveDeprecate, Path: "b.md"})
	_ = s.Ledger.Append("t", ArchiveRecord{Action: ArchiveRestore, Path: "b.md"})
	_ = s.Ledger.Append("t", ArchiveRecord{Action: ArchiveDeprecate, Path: "c.md"})
	_ = s.Ledger.Append("t", ArchiveRecord{Action: ArchivePurge, Path: "c.md"})
	if got, _ := s.Deprecated("t"); !reflect.DeepEqual(got, map[string]bool{"a.md": true}) {
		t.Errorf("deprecated = %v", got)
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

func TestLockStatusAndTick(t *testing.T) {
	root := t.TempDir()
	if RunningPID(root) != 0 || ReadStatus(root) != nil {
		t.Error("처음엔 없음")
	}
	// 낡은 pid 파일(재사용된 pid 1 포함)은 락이 아니다 — 락은 커널이 쥐는 flock 이다
	if err := os.MkdirAll(RuntimeDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(pidFile(root), []byte("1"), 0o644)
	if RunningPID(root) != 0 {
		t.Error("아무도 flock 을 쥐지 않았으면 pid 파일이 있어도 돌고 있지 않다")
	}
	if err := AcquireLock(root); err != nil {
		t.Fatal(err)
	}
	if RunningPID(root) != os.Getpid() {
		t.Error("락을 잡은 pid")
	}
	if err := AcquireLock(root); err != nil {
		t.Error("이미 잡은 락을 다시 잡아도 된다")
	}
	if err := ReleaseLock(root); err != nil || RunningPID(root) != 0 {
		t.Error("release")
	}

	// 다른 프로세스가 쥔 락 — flock 은 열린 파일 단위라 같은 프로세스의 다른 fd 로 흉내 낸다
	other, err := os.OpenFile(lockFile(root), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(pidFile(root), []byte("4242"), 0o644)
	var running *AlreadyRunningError
	if err := AcquireLock(root); !errors.As(err, &running) || running.PID != 4242 {
		t.Errorf("다른 holder 가 있으면 거부: %v", err)
	}
	if RunningPID(root) != 4242 {
		t.Error("holder 의 pid 를 보인다")
	}
	_ = syscall.Flock(int(other.Fd()), syscall.LOCK_UN)
	if RunningPID(root) != 0 {
		t.Error("holder 가 죽으면(락이 풀리면) 돌고 있지 않다")
	}
	sum := CycleSummary{Scanned: 3}
	if err := WriteStatus(root, Status{PID: 1, Cycles: 2, LastCycle: &CycleRecord{Trigger: "boot", Summary: &sum}}); err != nil {
		t.Fatal(err)
	}
	if s := ReadStatus(root); s == nil || s.Cycles != 2 || s.LastCycle.Summary.Scanned != 3 {
		t.Errorf("status = %+v", s)
	}
	tick := NewTick(root, "audit")
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if !tick.Due(now, time.Hour) || !tick.Last().IsZero() {
		t.Error("처음엔 due")
	}
	_ = tick.Done(now)
	if tick.Due(now.Add(30*time.Minute), time.Hour) || !tick.Due(now.Add(2*time.Hour), time.Hour) {
		t.Error("tick 가드")
	}
}

func TestLookupDistilledReadsFreshLedger(t *testing.T) {
	root := t.TempDir()
	lookup := LookupDistilled(root)
	if _, ok := lookup("t", "sha"); ok {
		t.Error("빈 원장")
	}
	// 조회 함수를 만든 뒤에 데몬이 증류한 결과도 보인다
	if err := NewDistillCache(root).Put("t", DistillRecord{BodySHA: "sha", SourcePath: "a.md", Summary: "요약"}); err != nil {
		t.Fatal(err)
	}
	if d, ok := lookup("t", "sha"); !ok || d.Summary != "요약" {
		t.Errorf("lookup = %+v %v", d, ok)
	}
}
