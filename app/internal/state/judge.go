package state

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

// pairKeySHALen 은 pairKey 에 쓰는 sha 접두 길이다. 원장에 영속되므로 바꾸면 기존 기록이 고아가 된다.
const pairKeySHALen = 12

// PairKey 는 방향 무관 쌍 키다. 본문(frontmatter 제외) sha 라 판정 기록이 재판정을 부르지 않는다.
func PairKey(bodyA, bodyB string) string {
	keys := []string{shortSHA(bodyA), shortSHA(bodyB)}
	sort.Strings(keys)
	return keys[0] + ":" + keys[1]
}

func shortSHA(sha string) string {
	if len(sha) > pairKeySHALen {
		return sha[:pairKeySHALen]
	}
	return sha
}

// JudgeState 는 .cache/judge/{team}.jsonl 원장이다.
type JudgeState struct{ Ledger *Ledger[JudgeRecord] }

// NewJudgeState 는 root 의 판정 원장이다.
func NewJudgeState(root string) *JudgeState {
	return &JudgeState{Ledger: NewLedger[JudgeRecord](filepath.Join(CacheDir(root), "judge"))}
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

// SupersededMap 은 지금 유효한 결론: 구판 경로 → 신판 경로. bodies 는 팀의 지금 문서(경로 → 본문 sha)다.
// 결론은 경로가 아니라 내용(본문 sha 쌍)을 따른다 — 같은 쌍은 다시 판정하지 않으므로(멱등) 경로만 보면 이름을
// 바꾸기만 해도(git mv) 밀린 문서가 영영 되살아난다. 구판 본문이 어디에도 없거나(고쳤다 — 새 쌍으로 재평가)
// 신판이 없어졌으면 무효.
func (s *JudgeState) SupersededMap(team string, bodies map[string]string) (map[string]string, error) {
	latest, err := s.Latest(team)
	if err != nil {
		return nil, err
	}
	bySHA := pathsBySHA(bodies)
	out := map[string]string{}
	for _, r := range latest {
		if r.Decision != DecisionSuperseded {
			continue
		}
		newPath, ok := resolveNew(r.New, bodies, bySHA)
		if !ok {
			continue
		}
		// 구판 본문을 가진 문서는 모두 밀린다 — 사본도 같은 쌍이라 다시 판정하지 않는다
		for _, oldPath := range bySHA[r.Old.BodySHA] {
			if oldPath != newPath {
				out[oldPath] = newPath
			}
		}
	}
	return out, nil
}

// pathsBySHA 는 본문 sha → 그 본문을 가진 경로(정렬)다. sha 가 빈 문서는 내용으로 찾을 수 없어 뺀다.
func pathsBySHA(bodies map[string]string) map[string][]string {
	out := map[string][]string{}
	for p, sha := range bodies {
		if sha != "" {
			out[sha] = append(out[sha], p)
		}
	}
	for _, paths := range out {
		sort.Strings(paths)
	}
	return out
}

// resolveNew 는 신판의 지금 경로다. 기록한 경로에 그 본문 → 그 본문의 다른 경로(이름을 바꿨다, 경로 순 첫째)
// → 기록한 경로(제자리에서 고쳤다 — 결론 유지) 순으로 찾는다. 셋 다 아니면 신판이 없어졌다.
func resolveNew(side Side, bodies map[string]string, bySHA map[string][]string) (string, bool) {
	sha, exists := bodies[side.Path]
	if exists && sha == side.BodySHA {
		return side.Path, true
	}
	if paths := bySHA[side.BodySHA]; len(paths) > 0 {
		return paths[0], true
	}
	return side.Path, exists
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

// AllPendingQuestions 는 모든 팀의 질문 수다.
func (s *JudgeState) AllPendingQuestions() (map[string][]JudgeRecord, error) {
	teams, err := s.Ledger.Teams()
	if err != nil {
		return nil, err
	}
	out := map[string][]JudgeRecord{}
	for _, team := range teams {
		qs, err := s.PendingQuestions(team)
		if err != nil {
			return nil, err
		}
		if len(qs) > 0 {
			out[team] = qs
		}
	}
	return out, nil
}
