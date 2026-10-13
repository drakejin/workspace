package state

import "path/filepath"

// ArchiveAction 은 cold 문서에 대한 사람의 조치다.
type ArchiveAction string

const (
	ArchiveDeprecate ArchiveAction = "deprecate" // 틀린 것으로 표시 — 기본 검색에서 빠진다
	ArchiveRestore   ArchiveAction = "undeprecate"
	ArchivePurge     ArchiveAction = "purge" // 원본·청크·스냅샷에서 지운다 (개인정보 등)
	ArchiveEdit      ArchiveAction = "edit"  // 보관된 본문을 고친다
)

// ArchiveRecord 는 cold 문서 감사 기록 한 줄이다. --by·--reason 이 리뷰 없는 cold 쓰기를 보완한다.
type ArchiveRecord struct {
	Action       ArchiveAction `json:"action"`
	Path         string        `json:"path"`
	SupersededBy string        `json:"supersededBy,omitempty"`
	ContentSHA   string        `json:"contentSha,omitempty"` // edit 후 새 원문 sha
	Snapshots    []string      `json:"snapshots,omitempty"`  // 다시 쓴 스냅샷
	By           string        `json:"by"`
	Reason       string        `json:"reason"`
	At           string        `json:"at"`
}

// ArchiveState 는 .cache/archive/{team}.jsonl 원장이다.
type ArchiveState struct{ Ledger *Ledger[ArchiveRecord] }

// NewArchiveState 는 root 의 cold 문서 감사 원장이다.
func NewArchiveState(root string) *ArchiveState {
	return &ArchiveState{Ledger: NewLedger[ArchiveRecord](filepath.Join(CacheDir(root), "archive"))}
}

// Deprecated 는 지금 deprecated 인 경로 집합이다 (마지막 기록 기준). 경로는 NFC 로 맞춰 읽는다 — NFD 경로로 남은
// 옛 기록도 지금의 source_path(NFC)에 적용된다.
func (s *ArchiveState) Deprecated(team string) (map[string]bool, error) {
	records, err := s.Ledger.ReadAll(team)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range records {
		switch path := canonicalPath(r.Path); r.Action {
		case ArchiveDeprecate:
			out[path] = true
		case ArchiveRestore, ArchivePurge:
			delete(out, path)
		}
	}
	return out, nil
}
