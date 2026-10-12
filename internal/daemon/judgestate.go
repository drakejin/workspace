package daemon

import (
	"path/filepath"
	"sort"
)

// Decision 은 판정 결론이다.
type Decision string

const (
	DecisionSuperseded Decision = "superseded" // old 가 밀렸다
	DecisionKeep       Decision = "keep"       // 공존
	DecisionQuestion   Decision = "question"   // 사람이 답한다
)

// Side 는 판정 쌍의 한쪽이다.
type Side struct {
	Path      string `json:"path"`
	BodySHA   string `json:"bodySha"`
	EventTime string `json:"eventTime"`
}

// JudgeRecord 는 판정 한 건이다. 같은 PairKey 의 마지막 기록이 현재 결론이다 (질문 → 답이 뒤에 붙는다).
type JudgeRecord struct {
	PairKey  string   `json:"pairKey"`
	Decision Decision `json:"decision"`
	Old      Side     `json:"old"`
	New      Side     `json:"new"`
	Reason   string   `json:"reason"`
	By       string   `json:"by"` // latest-wins | llm | human | fallback
	At       string   `json:"at"`
}

// PairKey 는 방향 무관 쌍 키다. 본문(frontmatter 제외) sha 라 판정 기록이 재판정을 부르지 않는다.
func PairKey(bodyA, bodyB string) string {
	keys := []string{short(bodyA), short(bodyB)}
	sort.Strings(keys)
	return keys[0] + ":" + keys[1]
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// JudgeState 는 judge-state/{team}.jsonl 원장이다.
type JudgeState struct{ Ledger *Ledger[JudgeRecord] }

// NewJudgeState 는 root/judge-state 원장이다.
func NewJudgeState(root string) *JudgeState {
	return &JudgeState{Ledger: NewLedger[JudgeRecord](filepath.Join(root, "judge-state"))}
}

// Latest 는 pairKey → 마지막 기록이다.
func (s *JudgeState) Latest(team string) (map[string]JudgeRecord, error) {
	records, err := s.Ledger.ReadAll(team)
	if err != nil {
		return nil, err
	}
	out := make(map[string]JudgeRecord, len(records))
	for _, r := range records {
		out[r.PairKey] = r
	}
	return out, nil
}

// SupersededMap 은 지금 유효한 결론: 구판 경로 → 신판 경로. 구판 본문이 바뀌었거나 신판이 없어졌으면 무효(재평가).
func (s *JudgeState) SupersededMap(team string, bodySHA func(path string) (string, bool)) (map[string]string, error) {
	latest, err := s.Latest(team)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range latest {
		if r.Decision != DecisionSuperseded {
			continue
		}
		if sha, ok := bodySHA(r.Old.Path); !ok || sha != r.Old.BodySHA {
			continue
		}
		if _, ok := bodySHA(r.New.Path); !ok {
			continue
		}
		out[r.Old.Path] = r.New.Path
	}
	return out, nil
}

// PendingQuestions 는 답을 기다리는 질문이다.
func (s *JudgeState) PendingQuestions(team string) ([]JudgeRecord, error) {
	latest, err := s.Latest(team)
	if err != nil {
		return nil, err
	}
	var out []JudgeRecord
	for _, r := range latest {
		if r.Decision == DecisionQuestion {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PairKey < out[j].PairKey })
	return out, nil
}
