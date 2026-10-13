package ops

import (
	"fmt"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/state"
)

// Answer 는 질문에 대한 답이다.
type Answer string

const (
	AnswerLatest Answer = "latest" // 최신이 사실 — 구판을 숨긴다
	AnswerOld    Answer = "old"    // 구판이 사실 — 신판을 숨긴다
	AnswerKeep   Answer = "keep"   // 둘 다 유지 (공존)
)

// ParseAnswer 는 답을 읽는다.
func ParseAnswer(s string) (Answer, bool) {
	switch Answer(s) {
	case AnswerLatest, AnswerOld, AnswerKeep:
		return Answer(s), true
	}
	return "", false
}

// RenderQuestions 는 판정이 사람에게 넘긴 질문 목록이다. 같은 내용 쌍은 다시 묻지 않는다.
func RenderQuestions(root string) (string, error) {
	judge := state.NewJudgeState(root)
	teams, err := judge.Ledger.Teams()
	if err != nil {
		return "", err
	}
	var rows []string
	for _, team := range teams {
		qs, err := judge.PendingQuestions(team)
		if err != nil {
			return "", err
		}
		for _, q := range qs {
			rows = append(rows, fmt.Sprintf("- [%s] %s\n  old: %s (%s)\n  new: %s (%s)\n  이유: %s",
				team, q.PairKey, q.Old.Path, model.Day(q.Old.EventTime), q.New.Path, model.Day(q.New.EventTime), q.Reason))
		}
	}
	if len(rows) == 0 {
		return "답을 기다리는 질문이 없습니다.", nil
	}
	return fmt.Sprintf("질문 %d건 — 답: scripts/memory/run.sh answer <pairKey> latest|old|keep\n"+
		"  latest = 최신을 사실로(구판 숨김) · old = 구판이 사실(신판 숨김) · keep = 둘 다 유지(공존)\n\n%s",
		len(rows), strings.Join(rows, "\n")), nil
}

// AnswerQuestion 은 원장에 사람 판정을 append 한다. 다음 데몬 사이클이 인덱스를 맞춘다.
func AnswerQuestion(root, pairKey string, answer Answer, now time.Time) (string, error) {
	judge := state.NewJudgeState(root)
	teams, err := judge.Ledger.Teams()
	if err != nil {
		return "", err
	}
	for _, team := range teams {
		latest, err := judge.Latest(team)
		if err != nil {
			return "", err
		}
		q, ok := latest[pairKey]
		if !ok {
			continue
		}
		// 구판·신판은 처음 질문이 정한 방향을 따른다 (판정이 같은 시각 쌍의 순서까지 정했다).
		// 마지막 기록은 이미 답(old)으로 뒤집혀 있을 수 있어 쓰지 않는다
		older, newer := q.Old, q.New
		if orig, ok, err := questionOf(judge, team, pairKey); err != nil {
			return "", err
		} else if ok {
			older, newer = orig.Old, orig.New
		}
		if model.ParseTime(older.EventTime).After(model.ParseTime(newer.EventTime)) {
			older, newer = newer, older
		}
		rec := state.JudgeRecord{PairKey: pairKey, Old: older, New: newer, By: "human", At: now.UTC().Format(time.RFC3339)}
		switch answer {
		case AnswerKeep:
			rec.Decision, rec.Reason = state.DecisionKeep, "사람: 공존"
		case AnswerLatest:
			rec.Decision, rec.Reason = state.DecisionSuperseded, "사람: 최신이 사실"
		case AnswerOld:
			rec.Decision, rec.Reason = state.DecisionSuperseded, "사람: 구판이 사실"
			rec.Old, rec.New = newer, older
		}
		if err := judge.Ledger.Append(team, rec); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %s — 다음 데몬 사이클(또는 'scripts/memory-daemon/run.sh once')이 인덱스에 반영합니다", pairKey, rec.Reason), nil
	}
	return "", fmt.Errorf("질문을 찾지 못했습니다: %s ('scripts/memory/run.sh questions' 로 확인)", pairKey)
}

// questionOf 는 쌍의 가장 최근 질문 기록이다.
func questionOf(judge *state.JudgeState, team, pairKey string) (state.JudgeRecord, bool, error) {
	records, err := judge.Ledger.ReadAll(team)
	if err != nil {
		return state.JudgeRecord{}, false, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].PairKey == pairKey && records[i].Decision == state.DecisionQuestion {
			return records[i], true, nil
		}
	}
	return state.JudgeRecord{}, false, nil
}
