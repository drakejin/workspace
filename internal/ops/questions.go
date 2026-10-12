package ops

import (
	"fmt"
	"strings"
	"time"

	"github.com/drakejin/workspace/internal/daemon"
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
	state := daemon.NewJudgeState(root)
	teams, err := state.Ledger.Teams()
	if err != nil {
		return "", err
	}
	var rows []string
	for _, team := range teams {
		qs, err := state.PendingQuestions(team)
		if err != nil {
			return "", err
		}
		for _, q := range qs {
			rows = append(rows, fmt.Sprintf("- [%s] %s\n  old: %s (%s)\n  new: %s (%s)\n  이유: %s",
				team, q.PairKey, q.Old.Path, day(q.Old.EventTime), q.New.Path, day(q.New.EventTime), q.Reason))
		}
	}
	if len(rows) == 0 {
		return "답을 기다리는 질문이 없습니다.", nil
	}
	return fmt.Sprintf("질문 %d건 — 답: make answer KEY=<pairKey> A=latest|old|keep\n"+
		"  latest = 최신을 사실로(구판 숨김) · old = 구판이 사실(신판 숨김) · keep = 둘 다 유지(공존)\n\n%s",
		len(rows), strings.Join(rows, "\n")), nil
}

// AnswerQuestion 은 원장에 사람 판정을 append 한다. 다음 데몬 사이클이 인덱스를 맞춘다.
func AnswerQuestion(root, pairKey string, answer Answer, now time.Time) (string, error) {
	state := daemon.NewJudgeState(root)
	teams, err := state.Ledger.Teams()
	if err != nil {
		return "", err
	}
	for _, team := range teams {
		latest, err := state.Latest(team)
		if err != nil {
			return "", err
		}
		q, ok := latest[pairKey]
		if !ok {
			continue
		}
		rec := daemon.JudgeRecord{PairKey: pairKey, Old: q.Old, New: q.New, By: "human", At: now.UTC().Format(time.RFC3339)}
		switch answer {
		case AnswerKeep:
			rec.Decision, rec.Reason = daemon.DecisionKeep, "사람: 공존"
		case AnswerLatest:
			rec.Decision, rec.Reason = daemon.DecisionSuperseded, "사람: 최신이 사실"
		case AnswerOld:
			rec.Decision, rec.Reason = daemon.DecisionSuperseded, "사람: 구판이 사실"
			rec.Old, rec.New = q.New, q.Old
		}
		if err := state.Ledger.Append(team, rec); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %s — 다음 데몬 사이클(또는 'make reconcile')이 인덱스에 반영합니다", pairKey, rec.Reason), nil
	}
	return "", fmt.Errorf("질문을 찾지 못했습니다: %s ('make questions' 로 확인)", pairKey)
}

func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}
