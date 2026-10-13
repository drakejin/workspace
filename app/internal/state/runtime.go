package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// CycleSummary 는 데몬 사이클 한 번의 요약이다.
type CycleSummary struct {
	Scanned        int   `json:"scanned"`
	Indexed        int   `json:"indexed"`
	StatUpdated    int   `json:"statUpdated"`
	Removed        int   `json:"removed"`
	Skipped        int   `json:"skipped"`
	ColdOwned      int   `json:"coldOwned"` // cold 로 간 hot 원본 — 다음 tier 가 정리한다
	ColdKept       int   `json:"coldKept"`  // cold 로 간 hot 원본 중 git 이 되살릴 수 없어(커밋 전·커밋 뒤 고침) tier 가 남기는 것 — 커밋하면 정리한다
	Failed         int   `json:"failed"`
	FlagsConverged int   `json:"flagsConverged"`
	Refused        bool  `json:"refused"`
	Partial        bool  `json:"partial"`
	TookMs         int64 `json:"tookMs"`
}

// JudgeSummary 는 판정 게이트 한 번의 요약이다.
type JudgeSummary struct {
	Candidates        int `json:"candidates"`
	LatestWins        int `json:"latestWins"`
	LLMJudged         int `json:"llmJudged"`
	Questions         int `json:"questions"`
	SkippedIdempotent int `json:"skippedIdempotent"`
}

// CycleRecord 는 사이클 하나의 기록이다.
type CycleRecord struct {
	Trigger string        `json:"trigger"`
	At      string        `json:"at"`
	Summary *CycleSummary `json:"summary,omitempty"`
	Judge   *JudgeSummary `json:"judge,omitempty"`
	Graph   string        `json:"graph,omitempty"` // 그래프 단계 결과 한 줄 (꺼짐·실패 포함)
	Error   string        `json:"error,omitempty"`
}

// Status 는 데몬이 사이클마다 쓰는 상태다. scripts/memory-daemon/run.sh status(운영 CLI 의 status)가 읽는다 —
// 돌고 있는 pid 는 pid 파일(RunningPID)이 맡는다.
type Status struct {
	StartedAt       string       `json:"startedAt"`
	HeartbeatAt     string       `json:"heartbeatAt"`
	IntervalSeconds int          `json:"intervalSeconds"`
	Cycles          int          `json:"cycles"`
	LastCycle       *CycleRecord `json:"lastCycle,omitempty"`
	DistillPending  int          `json:"distillPending"`
}

// RuntimeDir 은 데몬 런타임 파일 디렉토리다 (.cache/daemon — git 에 들어가지 않는다).
func RuntimeDir(root string) string { return filepath.Join(CacheDir(root), "daemon") }

func statusFile(root string) string { return filepath.Join(RuntimeDir(root), "status.json") }
func pidFile(root string) string    { return filepath.Join(RuntimeDir(root), "daemon.pid") }

// writeJSON 은 rename 으로 바꿔 끼워 읽는 쪽이 반쯤 쓴 파일을 보지 않게 한다.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteStatus 는 데몬 상태를 쓴다.
func WriteStatus(root string, s Status) error {
	if err := writeJSON(statusFile(root), s); err != nil {
		return fmt.Errorf("daemon status: %w", err)
	}
	return nil
}

// ReadStatus 는 데몬 상태를 읽는다. 없으면 nil.
func ReadStatus(root string) *Status {
	b, err := os.ReadFile(statusFile(root))
	if err != nil {
		return nil
	}
	var s Status
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// lockFile 은 데몬 락이다. flock 은 커널이 쥐고 프로세스가 죽으면 풀어 주므로, 낡은 pid 파일이나 재사용된 pid 에
// 속지 않고 잡기도 원자적이다. pid 파일은 보여 주기용이다.
func lockFile(root string) string { return filepath.Join(RuntimeDir(root), "daemon.lock") }

var (
	heldMu sync.Mutex
	held   = map[string]*os.File{} // 이 프로세스가 쥔 락 (root 별)
)

// RunningPID 는 돌고 있는 데몬 pid 다. 없으면 0. 락이 잡혀 있는데 pid 파일을 읽을 수 없으면 -1.
func RunningPID(root string) int {
	heldMu.Lock()
	mine := held[root] != nil
	heldMu.Unlock()
	if mine {
		return os.Getpid()
	}
	f, err := os.Open(lockFile(root))
	if err != nil {
		return 0
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0
	}
	if pid := readPID(root); pid > 0 {
		return pid
	}
	return -1
}

func readPID(root string) int {
	b, err := os.ReadFile(pidFile(root))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// AlreadyRunningError 는 다른 데몬이 이미 돌고 있다는 뜻이다.
type AlreadyRunningError struct{ PID int }

func (e *AlreadyRunningError) Error() string {
	pid := "?"
	if e.PID > 0 {
		pid = strconv.Itoa(e.PID)
	}
	return "데몬이 이미 돌고 있습니다 (pid " + pid + "). 파생물의 writer 는 하나여야 합니다 — 'scripts/memory-daemon/run.sh status' 로 확인하세요"
}

// AcquireLock 은 데몬 락을 잡는다. 데몬은 하나만 — writer 가 둘이면 수렴이 깨진다. 이미 쥐고 있으면 그대로 둔다.
func AcquireLock(root string) error {
	heldMu.Lock()
	defer heldMu.Unlock()
	if held[root] != nil {
		return nil
	}
	if err := os.MkdirAll(RuntimeDir(root), 0o755); err != nil {
		return fmt.Errorf("daemon lock: %w", err)
	}
	f, err := os.OpenFile(lockFile(root), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("daemon lock: %w", err)
	}
	if err := flockRetry(f); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return &AlreadyRunningError{PID: readPID(root)}
		}
		return fmt.Errorf("daemon lock: %w", err)
	}
	if err := os.WriteFile(pidFile(root), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return fmt.Errorf("daemon lock: %w", err)
	}
	held[root] = f
	return nil
}

// lockRetries 는 락이 잡혀 있을 때 다시 시도하는 횟수다. 'scripts/memory-daemon/run.sh status' 의 RunningPID 가 확인하느라 아주 잠깐
// 쥐는 것과 겹쳐도 데몬이 "이미 돌고 있다" 로 오인하지 않게.
const (
	lockRetries    = 5
	lockRetryDelay = 20 * time.Millisecond
)

func flockRetry(f *os.File) error {
	var err error
	for i := 0; i < lockRetries; i++ {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		time.Sleep(lockRetryDelay)
	}
	return err
}

// ReleaseLock 은 내가 잡은 락만 푼다.
func ReleaseLock(root string) error {
	heldMu.Lock()
	defer heldMu.Unlock()
	f := held[root]
	if f == nil {
		return nil
	}
	delete(held, root)
	if err := os.Remove(pidFile(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		f.Close()
		return fmt.Errorf("daemon lock: %w", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}

// 문서 쓰기 락. 파생물의 주 writer 는 데몬이지만 ingest_path·CLI ingest·audit -apply 도 같은 Write 를 부른다 — Write 는 여러 단계
// (청크 0 무효 표시 → 옛 청크 정리 → 나머지 청크 → cold store → 청크 0)라, 두 writer 가 같은 문서를 엇갈려 쓰면 청크 0 은 새 내용
// (최신 stat)이고 나머지는 옛 내용인 섞인 문서가 최신으로 고정된다. 그래서 문서 하나를 쓰는 동안은 그 문서의 락을 쥔다.
const (
	// docLockStripes 는 문서 쓰기 락 파일 수다. source_path 의 해시로 나눠 쥔다 — 서로 다른 문서는 거의 기다리지 않고, 락 파일은
	// 이 수를 넘지 않는다 (문서마다 두면 지운 문서의 락 파일이 쌓이고, 쓰는 중에 지우면 두 writer 가 다른 파일을 쥔다).
	docLockStripes = 64
	// docLockPoll 은 다른 writer 가 쥔 문서 락을 다시 시도하는 간격이다.
	docLockPoll = 5 * time.Millisecond
)

func docLockFile(root, sourcePath string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sourcePath)) // hash.Hash 의 Write 는 실패하지 않는다
	return filepath.Join(RuntimeDir(root), "doc-locks", fmt.Sprintf("%02d.lock", h.Sum32()%docLockStripes))
}

// LockDoc 은 문서 하나의 쓰기 락을 잡는다. flock 이라 프로세스 사이(데몬·MCP 서버·CLI)도, 한 프로세스 안의 두 호출(열 때마다 다른
// open file 이다)도 막고, 쥔 프로세스가 죽으면 커널이 푼다. 다른 writer 가 쥐고 있으면 ctx 가 끝날 때까지 기다린다. 돌려준 함수로 푼다.
// 락을 쥔 채로 다른 문서의 락을 잡지 않는다 (같은 파일을 나눠 쥐는 두 문서가 서로 기다리지 않게).
func LockDoc(ctx context.Context, root, sourcePath string) (func(), error) {
	if root == "" {
		return nil, errors.New("doc lock: meta-repo 루트가 비었습니다")
	}
	path := docLockFile(root, sourcePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("doc lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("doc lock: %w", err)
	}
	if err := flockWait(ctx, f); err != nil {
		f.Close()
		return nil, fmt.Errorf("doc lock %s: %w", sourcePath, err)
	}
	return func() {
		// 풀기 실패는 되돌릴 방법이 없다 — 파일을 닫으면 커널이 어차피 푼다
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// flockWait 는 f 의 배타 flock 을 잡을 때까지 기다린다. ctx 가 끝나면 그 오류다.
func flockWait(ctx context.Context, f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(docLockPoll):
		}
	}
}

// Tick 은 주기 작업(주간 audit, 일간 tier)의 마지막 성공 시각이다. 성공 뒤에 찍는다 —
// 먼저 찍으면 실패한 주기가 통째로 건너뛰어진다.
type Tick struct{ path string }

// NewTick 은 .cache/daemon/{name}.tick.json 이다.
func NewTick(root, name string) Tick {
	return Tick{path: filepath.Join(RuntimeDir(root), name+".tick.json")}
}

// Due 는 마지막 성공에서 every 가 지났는가.
func (t Tick) Due(now time.Time, every time.Duration) bool {
	last := t.Last()
	return last.IsZero() || now.Sub(last) >= every
}

// Last 는 마지막 성공 시각이다. 없으면 zero.
func (t Tick) Last() time.Time {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return time.Time{}
	}
	var v struct {
		At time.Time `json:"at"`
	}
	if json.Unmarshal(b, &v) != nil {
		return time.Time{}
	}
	return v.At
}

// Done 은 성공을 찍는다.
func (t Tick) Done(now time.Time) error {
	return writeJSON(t.path, map[string]time.Time{"at": now.UTC()})
}
