package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	lookup := func(m map[string]string) func(string) (string, bool) {
		return func(p string) (string, bool) { v, ok := m[p]; return v, ok }
	}
	if m, _ := s.SupersededMap("t", lookup(map[string]string{"a.md": "sha-a", "b.md": "sha-b"})); m["a.md"] != "b.md" {
		t.Errorf("map = %v", m)
	}
	if m, _ := s.SupersededMap("t", lookup(map[string]string{"a.md": "edited", "b.md": "sha-b"})); len(m) != 0 {
		t.Error("구판 본문이 바뀌면 결론 무효")
	}
	if m, _ := s.SupersededMap("t", lookup(map[string]string{"a.md": "sha-a"})); len(m) != 0 {
		t.Error("신판이 없으면 결론 무효")
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
