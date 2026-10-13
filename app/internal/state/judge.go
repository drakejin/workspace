package state

import (
	"path/filepath"
	"sort"

	"golang.org/x/text/unicode/norm"
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

// records 는 팀 원장 전체다. 경로는 NFC 로 맞춰 읽는다 — 문서의 정체(source_path)가 NFC 로 바뀌기 전에 NFD 경로
// (Finder 가 만든 한글 이름)로 남은 기록도 계속 적용된다.
func (s *JudgeState) records(team string) ([]JudgeRecord, error) {
	records, err := s.Ledger.ReadAll(team)
	if err != nil {
		return nil, err
	}
	out := make([]JudgeRecord, len(records))
	for i, r := range records {
		r.Old.Path, r.New.Path = canonicalPath(r.Old.Path), canonicalPath(r.New.Path)
		out[i] = r
	}
	return out, nil
}

// canonicalPath 는 원장에 적힌 문서 경로를 source_path 꼴(유니코드 NFC — document.CanonicalPath 와 같다)로 맞춘다.
func canonicalPath(p string) string { return norm.NFC.String(p) }

// Latest 는 pairKey → 마지막 기록이다.
func (s *JudgeState) Latest(team string) (map[string]JudgeRecord, error) {
	records, err := s.records(team)
	if err != nil {
		return nil, err
	}
	out := make(map[string]JudgeRecord, len(records))
	for _, r := range records {
		out[r.PairKey] = r
	}
	return out, nil
}

// JudgedDoc 은 판정 결론을 옮길 때 보는 팀 문서 하나다. Scope 는 판정이 견주는 범위(같은 프로젝트·같은 문서 종류)다 —
// 다른 범위의 문서는 판정이 짝지은 적이 없어 결론이 따라가지 않는다.
type JudgedDoc struct {
	BodySHA string
	Scope   string
}

// SupersededMap 은 지금 유효한 결론: 구판 경로 → 신판 경로. docs 는 팀의 지금 판정 대상 문서(경로 → 본문 sha·범위)다.
// 결론은 경로가 아니라 내용(본문 sha 쌍)을 따른다 — 같은 쌍은 다시 판정하지 않으므로(멱등) 경로만 보면 이름을
// 바꾸기만 해도(git mv) 밀린 문서가 영영 되살아난다. 내용은 같은 범위 안에서만 따라가고, 구판 본문이 그 범위
// 어디에도 없거나(고쳤다 — 새 쌍으로 재평가) 신판이 없어졌으면 무효. 한 문서를 두 결론이 가리키면 원장에 나중에
// 붙은 결론이 이긴다 (사이클마다 같은 답).
func (s *JudgeState) SupersededMap(team string, docs map[string]JudgedDoc) (map[string]string, error) {
	records, err := s.latestInOrder(team)
	if err != nil {
		return nil, err
	}
	scopes := scopesOf(docs)
	out := map[string]string{}
	for _, r := range records {
		if r.Decision != DecisionSuperseded {
			continue
		}
		// 범위끼리는 경로가 겹치지 않아 범위 순서는 결과를 바꾸지 않는다
		for _, sc := range scopes {
			for oldPath, newPath := range sc.superseded(r) {
				out[oldPath] = newPath
			}
		}
	}
	return out, nil
}

// latestInOrder 는 쌍마다 마지막 기록을, 그 기록이 원장에 붙은 순서대로 돌려준다.
func (s *JudgeState) latestInOrder(team string) ([]JudgeRecord, error) {
	records, err := s.records(team)
	if err != nil {
		return nil, err
	}
	last := make(map[string]int, len(records))
	for i, r := range records {
		last[r.PairKey] = i
	}
	out := make([]JudgeRecord, 0, len(last))
	for i, r := range records {
		if last[r.PairKey] == i {
			out = append(out, r)
		}
	}
	return out, nil
}

// judgedScope 는 판정 범위 하나의 문서들이다: 경로 → 본문 sha, 본문 sha → 경로(정렬).
type judgedScope struct {
	bodies map[string]string
	bySHA  map[string][]string
}

// scopesOf 는 docs 를 판정 범위별로 나눈다.
func scopesOf(docs map[string]JudgedDoc) map[string]judgedScope {
	bodies := map[string]map[string]string{}
	for p, d := range docs {
		if bodies[d.Scope] == nil {
			bodies[d.Scope] = map[string]string{}
		}
		bodies[d.Scope][p] = d.BodySHA
	}
	out := make(map[string]judgedScope, len(bodies))
	for scope, b := range bodies {
		out[scope] = judgedScope{bodies: b, bySHA: pathsBySHA(b)}
	}
	return out
}

// superseded 는 이 범위에서 결론 r 이 숨기는 문서 → 신판 경로다. 구판 본문을 가진 문서는 모두 밀린다 —
// 같은 범위의 사본은 같은 쌍이라 다시 판정하지 않는다.
func (sc judgedScope) superseded(r JudgeRecord) map[string]string {
	newPath, ok := sc.resolveNew(r)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, oldPath := range sc.bySHA[r.Old.BodySHA] {
		if oldPath != newPath {
			out[oldPath] = newPath
		}
	}
	return out
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
// 두 쪽 본문이 같은 쌍(사본)은 내용으로 찾지 않는다 — 구판도 같은 본문이라 구판을 신판으로 오인해 결론이 뒤집힌다.
func (sc judgedScope) resolveNew(r JudgeRecord) (string, bool) {
	side := r.New
	sha, exists := sc.bodies[side.Path]
	if exists && sha == side.BodySHA {
		return side.Path, true
	}
	if side.BodySHA != r.Old.BodySHA {
		if paths := sc.bySHA[side.BodySHA]; len(paths) > 0 {
			return paths[0], true
		}
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
