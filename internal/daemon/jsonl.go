// Package daemon 은 reconcile 데몬이다 — 인덱스·판정 플래그·증류 반영 같은 모든 파생물의 유일한 writer.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Ledger 는 팀별 append-only JSONL 원장이다. judge-state/ 와 knowledge-cache/ 가 쓴다.
// git 에 커밋되는 정본이라 인덱스가 날아가도 판정·증류 결과를 LLM 재호출 없이 복원한다.
type Ledger[T any] struct {
	dir string
	mu  sync.Mutex
	// OnCorrupt 는 깨진 줄을 알린다 (파일, 줄 번호 1부터).
	OnCorrupt func(file string, line int)
}

// NewLedger 는 dir 아래 {team}.jsonl 원장이다.
func NewLedger[T any](dir string) *Ledger[T] { return &Ledger[T]{dir: dir} }

func (l *Ledger[T]) file(team string) string { return filepath.Join(l.dir, team+".jsonl") }

// ReadAll 은 팀 원장 전체다. 없으면 빈 목록. 깨진 줄은 건너뛴다.
func (l *Ledger[T]) ReadAll(team string) ([]T, error) {
	f, err := os.Open(l.file(team))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			if l.OnCorrupt != nil {
				l.OnCorrupt(l.file(team), n)
			}
			continue
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Append 는 한 줄을 더한다.
func (l *Ledger[T]) Append(team string, v T) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.file(team), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Teams 는 원장이 있는 팀 목록이다.
func (l *Ledger[T]) Teams() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var teams []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok && !e.IsDir() {
			teams = append(teams, name)
		}
	}
	sort.Strings(teams)
	return teams, nil
}
