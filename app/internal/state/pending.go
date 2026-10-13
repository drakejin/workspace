package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

// PendingJudgment 는 판정하지 못하고 다음 사이클로 미룬 변경분 하나다. 본문은 담지 않는다 — 되살릴 때 디스크 본문의 sha 가
// BodySHA 와 같아야 쓴다 (그 사이 고친 문서는 사이클이 새 본문으로 다시 넘긴다).
type PendingJudgment struct {
	SourcePath string        `json:"sourcePath"`
	Team       string        `json:"team"`
	Project    string        `json:"project,omitempty"`
	DocType    model.DocType `json:"docType,omitempty"`
	Kind       model.Kind    `json:"kind"`
	BodySHA    string        `json:"bodySha"`
	EventTime  time.Time     `json:"eventTime"`
	Title      string        `json:"title,omitempty"`
}

// judgePendingFile 은 판정 대기 목록이다 (.cache/daemon — git 에 들어가지 않는다). 문서(source_path)마다 하나.
func judgePendingFile(root string) string {
	return filepath.Join(runtimeDir(root), "judge-pending.json")
}

// ReadJudgePending 은 판정 대기 목록(경로 → 대기분)이다. 없으면 빈 목록.
func ReadJudgePending(root string) (map[string]PendingJudgment, error) {
	b, err := os.ReadFile(judgePendingFile(root))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]PendingJudgment{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("judge pending: %w", err)
	}
	out := map[string]PendingJudgment{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("judge pending %s: %w", judgePendingFile(root), err)
	}
	return out, nil
}

// WriteJudgePending 은 판정 대기 목록을 통째로 바꿔 쓴다 (rename — 반쯤 쓴 파일을 읽지 않는다). 비면 파일을 지운다.
func WriteJudgePending(root string, pending map[string]PendingJudgment) error {
	if len(pending) == 0 {
		if err := os.Remove(judgePendingFile(root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("judge pending: %w", err)
		}
		return nil
	}
	if err := writeJSON(judgePendingFile(root), pending); err != nil {
		return fmt.Errorf("judge pending: %w", err)
	}
	return nil
}
