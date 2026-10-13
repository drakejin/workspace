package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/llm"
	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// Verdict 는 LLM 판정이다.
type Verdict struct {
	Contradicts bool   `json:"contradicts"`
	Winner      string `json:"winner"` // old | new
	Confident   bool   `json:"confident"`
}

const (
	likeRunes          = 6000   // more_like_this 에 넣는 본문 길이
	judgeRunes         = 30_000 // LLM 에 넣는 문서당 본문 길이
	neighborCandidates = 30     // 이웃 후보 청크 수
	mltMaxQueryTerms   = 40
	judgeSystem        = "두 팀 문서가 같은 주제를 다룬다. contradicts=두 문서가 모순되는 주장인가. winner=어느 쪽이 현재 사실인가. confident=확신하는가(애매하면 false)."
)

var judgeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"contradicts": map[string]any{"type": "boolean"},
		"winner":      map[string]any{"type": "string", "enum": []string{"old", "new"}},
		"confident":   map[string]any{"type": "boolean"},
	},
	"required":             []string{"contradicts", "winner", "confident"},
	"additionalProperties": false,
}

// Judge 는 판정 게이트(사이클 D)다. 같은 workspace·팀 안에서 episodic 끼리만 유사도로 판정한다.
// knowledge 는 유사도로 판정하지 않는다 — 개정판의 supersedes 선언만 믿는다(오판정 0).
// 1단 latest-wins(유효 시각 차 > windowDays) → 2단 로컬 claude → 3단 질문(저확신·LLM 불가).
type Judge struct {
	OS     opensearch.Client
	Config config.Config
	State  *state.JudgeState
	Ask    llm.Asker
	Log    func(format string, args ...any)
	Now    func() time.Time

	// retry 는 지난 Run 에서 오류로 판정하지 못한 변경분이다. 변경분은 그 사이클에만 있으므로 여기 두지 않으면
	// 문서가 다시 바뀔 때까지 판정되지 않는다. 사이클은 직렬로 돌아 잠금이 필요 없다.
	retry []DiffEntry
}

// Run 은 변경분을 판정한다. 한 문서의 실패는 나머지를 막지 않는다 — 실패분은 다음 Run 이 다시 판정하고, 오류는 모아 돌려준다.
func (j *Judge) Run(ctx context.Context, diff []DiffEntry) (state.JudgeSummary, error) {
	var sum state.JudgeSummary
	if !j.Config.Judge.Enabled {
		return sum, nil
	}
	work := append(j.retry, diff...)
	j.retry = nil
	seen := map[string]bool{}
	var errs []error
	for _, d := range work {
		if d.Change == ChangeRemoved || d.Supersedes != "" || d.Kind != model.KindEpisodic {
			continue
		}
		if err := j.judgeOne(ctx, d, seen, &sum); err != nil {
			j.retry = append(j.retry, d)
			errs = append(errs, fmt.Errorf("judge %s: %w", d.SourcePath, err))
		}
	}
	return sum, errors.Join(errs...)
}

func (j *Judge) judgeOne(ctx context.Context, d DiffEntry, seen map[string]bool, sum *state.JudgeSummary) error {
	n, err := BestNeighbor(ctx, j.OS, d, j.Config.Workspace, j.Config.Judge.MinScore)
	if err != nil {
		return err
	}
	if n == nil {
		return nil
	}
	sum.Candidates++
	key := state.PairKey(d.BodySHA, n.BodySHA)
	latest, err := j.State.Latest(d.Team)
	if err != nil {
		return err
	}
	if _, done := latest[key]; done || seen[key] {
		sum.SkippedIdempotent++
		return nil
	}
	seen[key] = true

	mine := side{state.Side{Path: d.SourcePath, BodySHA: d.BodySHA, EventTime: d.EventTime.UTC().Format(time.RFC3339Nano)}, d.Body}
	theirs := side{state.Side{Path: n.SourcePath, BodySHA: n.BodySHA, EventTime: n.EventTime}, readBody(j.Config.MetaRepoRoot, n.SourcePath, n.Text)}
	older, newer := mine, theirs
	if model.ParseTime(mine.EventTime).After(model.ParseTime(theirs.EventTime)) {
		older, newer = theirs, mine
	}
	rec := j.Decide(ctx, key, older, newer)
	if err := j.State.Ledger.Append(d.Team, rec); err != nil {
		return err
	}
	j.Log("[judge] %s %s ↔ %s (%s: %s)", rec.Decision, older.Path, newer.Path, rec.By, rec.Reason)
	switch {
	case rec.By == "latest-wins":
		sum.LatestWins++
	case rec.Decision == state.DecisionQuestion:
		sum.Questions++
	default:
		sum.LLMJudged++
	}
	return nil
}

type side struct {
	state.Side
	text string
}

// Decide 는 쌍 하나를 판정한다.
func (j *Judge) Decide(ctx context.Context, pairKey string, older, newer side) state.JudgeRecord {
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	base := state.JudgeRecord{PairKey: pairKey, Old: older.Side, New: newer.Side, At: now().UTC().Format(time.RFC3339)}
	gapDays := model.ParseTime(newer.EventTime).Sub(model.ParseTime(older.EventTime)).Hours() / 24
	if gapDays > j.Config.Judge.WindowDays {
		base.Decision, base.By = state.DecisionSuperseded, "latest-wins"
		base.Reason = fmt.Sprintf("유효 시각 차 %.1f일 > %g일", gapDays, j.Config.Judge.WindowDays)
		return base
	}
	raw, ok := j.Ask.Ask(ctx, llm.Input{
		System: judgeSystem,
		User:   fmt.Sprintf("--- old (%s) ---\n%s\n\n--- new (%s) ---\n%s", older.Path, truncate(older.text, judgeRunes), newer.Path, truncate(newer.text, judgeRunes)),
		Schema: judgeSchema,
		Tag:    "judge",
	})
	var v Verdict
	if !ok || json.Unmarshal(raw, &v) != nil || (v.Winner != "old" && v.Winner != "new") {
		base.Decision, base.By, base.Reason = state.DecisionQuestion, "fallback", "LLM 판정 불가 — 사람이 답한다"
		return base
	}
	base.By = "llm"
	switch {
	case !v.Contradicts:
		base.Decision, base.Reason = state.DecisionKeep, "모순 아님 — 공존"
	case !v.Confident:
		base.Decision, base.Reason = state.DecisionQuestion, "모순, 저확신(winner="+v.Winner+")"
	case v.Winner == "new":
		base.Decision, base.Reason = state.DecisionSuperseded, "모순, 고확신 — 신판이 사실"
	default:
		// 새 문서가 틀렸다 — 방향을 뒤집어 기록한다
		base.Decision, base.Reason = state.DecisionSuperseded, "모순, 고확신 — 구판이 사실"
		base.Old, base.New = newer.Side, older.Side
	}
	return base
}

// Neighbor 는 가장 비슷한 다른 episodic 문서다.
type Neighbor struct {
	SourcePath string
	BodySHA    string
	EventTime  string
	Text       string
	// RelativeScore 는 자기 자신 점수 대비 비율(0~1)이다. BM25 점수는 문서마다 스케일이 달라 정규화한다.
	RelativeScore float64
}

// BestNeighbor 는 같은 workspace·팀 hot 영역에서 가장 비슷한 다른 episodic 문서다. 이미 밀린 문서와는 재판정하지 않는다.
func BestNeighbor(ctx context.Context, c opensearch.Client, d DiffEntry, workspace string, minScore float64) (*Neighbor, error) {
	filter := []any{
		map[string]any{"term": map[string]any{"memory_kind": model.KindEpisodic}},
		map[string]any{"term": map[string]any{"workspace": workspace}},
	}
	mustNot := []any{
		map[string]any{"exists": map[string]any{"field": "superseded_by"}},
		map[string]any{"term": map[string]any{"deprecated": true}},
	}
	// 같은 프로젝트 안, 같은 종류의 기록끼리만 (drakejin 회사: "같은 project 안") — 짧은 문서끼리는 조사·흔한 낱말만으로도
	// 비슷해 보여 엉뚱한 쌍이 latest-wins 로 숨는다 (예: ADR 이 같은 프로젝트의 PRD 에 밀림).
	// 프로젝트가 없는 문서(여러 프로젝트에 걸친 주제)는 프로젝트 없는 문서끼리 견준다
	if d.DocType != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"doc_type": d.DocType}})
	}
	if d.Project != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"repo": d.Project}})
	} else {
		mustNot = append(mustNot, map[string]any{"exists": map[string]any{"field": "repo"}})
	}
	body := map[string]any{
		"size": neighborCandidates,
		"query": map[string]any{"bool": map[string]any{
			"must": []any{map[string]any{"more_like_this": map[string]any{
				"fields": []string{"text", "title"}, "like": truncate(d.Body, likeRunes),
				"min_term_freq": 1, "min_doc_freq": 1, "max_query_terms": mltMaxQueryTerms,
			}}},
			"filter":   filter,
			"must_not": mustNot,
		}},
		"_source": []string{"source_path", "body_sha", "event_time", "text"},
	}
	var out opensearch.SearchResponse[model.Chunk]
	err := c.Do(ctx, http.MethodPost, "/"+naming.HotAlias(d.Team)+"/_search", body, nil, &out)
	if opensearch.IsStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	self := 0.0
	best := map[string]Neighbor{}
	for _, h := range out.Hits.Hits {
		s := h.Source
		if s.SourcePath == d.SourcePath {
			self = max(self, h.Score)
			continue
		}
		if s.BodySHA == "" {
			continue
		}
		if prev, ok := best[s.SourcePath]; !ok || prev.RelativeScore < h.Score {
			best[s.SourcePath] = Neighbor{SourcePath: s.SourcePath, BodySHA: s.BodySHA, EventTime: s.EventTime, Text: s.Text, RelativeScore: h.Score}
		}
	}
	if self == 0 || len(best) == 0 {
		return nil, nil
	}
	list := make([]Neighbor, 0, len(best))
	for _, n := range best {
		n.RelativeScore /= self
		list = append(list, n)
	}
	sort.Slice(list, func(i, k int) bool { return list[i].RelativeScore > list[k].RelativeScore })
	if list[0].RelativeScore < minScore {
		return nil, nil
	}
	return &list[0], nil
}

// neighborScope 는 BestNeighbor 가 견주는 범위(같은 프로젝트·같은 문서 종류)의 키다. 판정 결론도 이 범위 안에서만
// 내용을 따라간다 — 다른 프로젝트·종류의 사본은 판정이 짝지은 적이 없다.
func neighborScope(project string, docType model.DocType) string {
	return project + "\x00" + string(docType)
}

// readBody 는 판정에 쓸 원문 본문이다. 검색은 경로를 돌려주고 본문은 원문을 읽는다.
func readBody(root, rel, fallback string) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return fallback
	}
	_, body := markdown.SplitFrontmatter(string(b))
	return body
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
