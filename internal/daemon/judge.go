package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/llm"
	"github.com/drakejin/workspace/internal/markdown"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// JudgeSummary 는 판정 게이트 한 번의 요약이다.
type JudgeSummary struct {
	Candidates        int `json:"candidates"`
	LatestWins        int `json:"latestWins"`
	LLMJudged         int `json:"llmJudged"`
	Questions         int `json:"questions"`
	SkippedIdempotent int `json:"skippedIdempotent"`
}

// Verdict 는 LLM 판정이다.
type Verdict struct {
	Contradicts bool   `json:"contradicts"`
	Winner      string `json:"winner"` // old | new
	Confident   bool   `json:"confident"`
}

const (
	likeRunes   = 6000
	judgeRunes  = 30_000
	judgeSystem = "두 팀 문서가 같은 주제를 다룬다. contradicts=두 문서가 모순되는 주장인가. winner=어느 쪽이 현재 사실인가. confident=확신하는가(애매하면 false)."
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

// Judge 는 판정 게이트(사이클 D)다. 같은 팀 안에서 episodic 끼리만 유사도로 판정한다.
// knowledge 는 유사도로 판정하지 않는다 — 개정판의 supersedes 선언만 믿는다(오판정 0).
// 1단 latest-wins(유효 시각 차 > windowDays) → 2단 로컬 claude → 3단 질문(저확신·LLM 불가).
type Judge struct {
	OS     opensearch.Client
	Config config.Config
	State  *JudgeState
	Ask    llm.Asker
	Log    func(format string, args ...any)
	Now    func() time.Time
}

// Run 은 변경분을 판정한다.
func (j *Judge) Run(ctx context.Context, diff []DiffEntry) (JudgeSummary, error) {
	var sum JudgeSummary
	if !j.Config.Judge.Enabled {
		return sum, nil
	}
	seen := map[string]bool{}
	for _, d := range diff {
		if d.Change == ChangeRemoved || d.Supersedes != "" || d.Kind != model.KindEpisodic {
			continue
		}
		n, err := BestNeighbor(ctx, j.OS, d, j.Config.Workspace, j.Config.Judge.MinScore)
		if err != nil {
			return sum, err
		}
		if n == nil {
			continue
		}
		sum.Candidates++
		key := PairKey(d.BodySHA, n.BodySHA)
		latest, err := j.State.Latest(d.Team)
		if err != nil {
			return sum, err
		}
		if _, done := latest[key]; done || seen[key] {
			sum.SkippedIdempotent++
			continue
		}
		seen[key] = true

		mine := side{Side{d.SourcePath, d.BodySHA, d.EventTime.UTC().Format(time.RFC3339Nano)}, d.Body}
		theirsBody := readBody(j.Config.MetaRepoRoot, n.SourcePath, n.Text)
		theirs := side{Side{n.SourcePath, n.BodySHA, n.EventTime}, theirsBody}
		older, newer := mine, theirs
		if parseRFC3339(mine.EventTime).After(parseRFC3339(theirs.EventTime)) {
			older, newer = theirs, mine
		}
		rec := j.Decide(ctx, key, older, newer)
		if err := j.State.Ledger.Append(d.Team, rec); err != nil {
			return sum, err
		}
		j.Log("[judge] %s %s ↔ %s (%s: %s)", rec.Decision, older.Path, newer.Path, rec.By, rec.Reason)
		switch {
		case rec.By == "latest-wins":
			sum.LatestWins++
		case rec.Decision == DecisionQuestion:
			sum.Questions++
		default:
			sum.LLMJudged++
		}
	}
	return sum, nil
}

type side struct {
	Side
	text string
}

// Decide 는 쌍 하나를 판정한다.
func (j *Judge) Decide(ctx context.Context, pairKey string, older, newer side) JudgeRecord {
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	base := JudgeRecord{PairKey: pairKey, Old: older.Side, New: newer.Side, At: now().UTC().Format(time.RFC3339)}
	gapDays := parseRFC3339(newer.EventTime).Sub(parseRFC3339(older.EventTime)).Hours() / 24
	if gapDays > j.Config.Judge.WindowDays {
		base.Decision, base.By = DecisionSuperseded, "latest-wins"
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
		base.Decision, base.By, base.Reason = DecisionQuestion, "fallback", "LLM 판정 불가 — 사람이 답한다"
		return base
	}
	base.By = "llm"
	switch {
	case !v.Contradicts:
		base.Decision, base.Reason = DecisionKeep, "모순 아님 — 공존"
	case !v.Confident:
		base.Decision, base.Reason = DecisionQuestion, "모순, 저확신(winner="+v.Winner+")"
	case v.Winner == "new":
		base.Decision, base.Reason = DecisionSuperseded, "모순, 고확신 — 신판이 사실"
	default:
		// 새 문서가 틀렸다 — 방향을 뒤집어 기록한다
		base.Decision, base.Reason = DecisionSuperseded, "모순, 고확신 — 구판이 사실"
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

// BestNeighbor 는 같은 팀 hot 영역에서 가장 비슷한 다른 episodic 문서다. 이미 밀린 문서와는 재판정하지 않는다.
func BestNeighbor(ctx context.Context, c opensearch.Client, d DiffEntry, workspace string, minScore float64) (*Neighbor, error) {
	var out struct {
		Hits struct {
			Hits []struct {
				Score  float64     `json:"_score"`
				Source model.Chunk `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	body := map[string]any{
		"size": 30,
		"query": map[string]any{"bool": map[string]any{
			"must": []any{map[string]any{"more_like_this": map[string]any{
				"fields": []string{"text", "title"}, "like": truncate(d.Body, likeRunes),
				"min_term_freq": 1, "min_doc_freq": 1, "max_query_terms": 40,
			}}},
			"filter": []any{
				map[string]any{"term": map[string]any{"memory_kind": model.KindEpisodic}},
				map[string]any{"term": map[string]any{"workspace": workspace}},
			},
			"must_not": []any{map[string]any{"exists": map[string]any{"field": "superseded_by"}}},
		}},
		"_source": []string{"source_path", "body_sha", "event_time", "text"},
	}
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
			if h.Score > self {
				self = h.Score
			}
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

func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
