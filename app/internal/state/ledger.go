// Package state 는 메모리 층의 영속 상태다. 데몬이 쓰고 운영 CLI 가 읽는다. 모두 meta-repo 의 .cache/ 아래에 있다.
//
//	.cache/judge/{team}.jsonl     판정 원장 (git 에 커밋 — 인덱스를 지워도 LLM 없이 복원)
//	.cache/distill/{team}.jsonl   증류 캐시 (git 에 커밋)
//	.cache/archive/{team}.jsonl   cold 문서 감사 기록 — deprecate·purge·edit (git 에 커밋)
//	.cache/daemon/*               데몬 pid·상태·틱 (커밋하지 않음)
package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CacheDir 는 meta-repo 의 생성물 디렉토리다.
func CacheDir(root string) string { return filepath.Join(root, ".cache") }

// maxLedgerLine 은 원장 한 줄의 상한이다 (증류 요약이 길어도 넉넉하게).
const maxLedgerLine = 16 * 1024 * 1024

// Ledger 는 팀별 append-only JSONL 원장이다. git 에 커밋되는 정본이라
// 인덱스가 날아가도 판정·증류·감사 결과를 LLM 재호출 없이 복원한다.
type Ledger[T any] struct {
	dir string
	mu  sync.Mutex
	// OnCorrupt 는 깨진 줄을 알린다 (파일, 줄 번호 1부터). nil 이면 조용히 건너뛴다.
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
		return nil, fmt.Errorf("ledger %s: %w", l.file(team), err)
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLedgerLine)
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
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("ledger %s: %w", l.file(team), err)
	}
	return out, nil
}

// Append 는 한 줄을 더한다.
func (l *Ledger[T]) Append(team string, v T) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("ledger %s: %w", l.dir, err)
	}
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ledger %s: %w", l.file(team), err)
	}
	f, err := os.OpenFile(l.file(team), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ledger %s: %w", l.file(team), err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ledger %s: %w", l.file(team), err)
	}
	return nil
}

// Teams 는 원장이 있는 팀 목록이다.
func (l *Ledger[T]) Teams() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger %s: %w", l.dir, err)
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
