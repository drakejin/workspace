package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// CycleRecord 는 사이클 하나의 기록이다.
type CycleRecord struct {
	Trigger string        `json:"trigger"`
	At      string        `json:"at"`
	Summary *CycleSummary `json:"summary,omitempty"`
	Judge   *JudgeSummary `json:"judge,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// Status 는 데몬이 사이클마다 쓰는 상태다. daemon-status 가 읽는다.
type Status struct {
	PID             int          `json:"pid"`
	StartedAt       string       `json:"startedAt"`
	HeartbeatAt     string       `json:"heartbeatAt"`
	IntervalSeconds int          `json:"intervalSeconds"`
	Cycles          int          `json:"cycles"`
	LastCycle       *CycleRecord `json:"lastCycle,omitempty"`
	DistillPending  int          `json:"distillPending"`
}

// RuntimeDir 은 데몬 런타임 파일 디렉토리다 (.data/daemon — git 에 들어가지 않는다).
func RuntimeDir(root string) string { return filepath.Join(root, ".data", "daemon") }

func statusFile(root string) string { return filepath.Join(RuntimeDir(root), "status.json") }
func pidFile(root string) string    { return filepath.Join(RuntimeDir(root), "daemon.pid") }

// WriteStatus 는 상태를 쓴다. rename 으로 바꿔 끼워 읽는 쪽이 반쯤 쓴 파일을 보지 않게 한다.
func WriteStatus(root string, s Status) error {
	if err := os.MkdirAll(RuntimeDir(root), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statusFile(root) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statusFile(root))
}

// ReadStatus 는 상태를 읽는다. 없으면 nil.
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

// IsAlive 는 pid 프로세스가 살아 있는가.
func IsAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// RunningPID 는 돌고 있는 데몬 pid 다. 없으면 0.
func RunningPID(root string) int {
	b, err := os.ReadFile(pidFile(root))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || !IsAlive(pid) {
		return 0
	}
	return pid
}

// AlreadyRunningError 는 다른 데몬이 이미 돌고 있다는 뜻이다.
type AlreadyRunningError struct{ PID int }

func (e *AlreadyRunningError) Error() string {
	return "데몬이 이미 돌고 있습니다 (pid " + strconv.Itoa(e.PID) + "). 파생물의 writer 는 하나여야 합니다 — 'make daemon-status' 로 확인하세요"
}

// AcquireLock 은 데몬 락을 잡는다. 데몬은 하나만 — writer 가 둘이면 수렴이 깨진다.
func AcquireLock(root string) error {
	if pid := RunningPID(root); pid != 0 && pid != os.Getpid() {
		return &AlreadyRunningError{PID: pid}
	}
	if err := os.MkdirAll(RuntimeDir(root), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pidFile(root), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ReleaseLock 은 내가 잡은 락만 푼다.
func ReleaseLock(root string) error {
	if RunningPID(root) != os.Getpid() {
		return nil
	}
	if err := os.Remove(pidFile(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
