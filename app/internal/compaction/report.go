// Package compaction 은 주간 compaction 1·2단계(수명 확인, 충돌 탐지)의 재료를 만든다.
// 판단과 다시 쓰기는 에이전트가 한다 (.claude/commands/compaction.md).
package compaction

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// Report 는 compaction 리포트다.
type Report struct {
	Team      string
	Cutoff    time.Time
	Expired   []ExpiredDoc
	Conflicts []Conflict
}

// ExpiredDoc 은 hot 기간이 지난 episodic 문서다.
type ExpiredDoc struct {
	SourcePath string
	Repo       string
	DocType    model.DocType
	EventTime  string
}

// Conflict 는 같은 repo·같은 주제를 서로 다른 문서가 다룬 묶음이다.
type Conflict struct {
	Repo    string
	Topic   string
	Entries []ConflictEntry
}

// ConflictEntry 는 묶음의 문서 하나다.
type ConflictEntry struct {
	SourcePath string
	HeaderPath string
	EventTime  string
	Excerpt    string
}

const excerptRunes = 240

// genericTopics 는 거의 모든 문서에 있는 섹션 제목이라 같은 주제로 보지 않는다.
var genericTopics = map[string]bool{
	"에피소드": true, "배경": true, "개요": true, "결과": true, "참고": true, "참고 자료": true, "요약": true, "목차": true,
	"결정": true, "대안": true, "출처": true, "범위": true, "목표": true, "작업": true, "이유": true, "확인할 것": true,
	"overview": true, "background": true, "context": true, "summary": true, "references": true, "notes": true,
	"todo": true, "decision": true, "alternatives": true, "consequences": true, "status": true, "goals": true,
}

var adrNumber = regexp.MustCompile(`^(adr|prd|spec)[-_ ]?\d+\s*`)

// Build 는 팀의 compaction 리포트를 만든다.
func Build(ctx context.Context, c opensearch.Client, cfg config.Config, team string, now time.Time) (Report, error) {
	cutoff := now.Add(-cfg.HotWindow())
	chunks, err := fetch(ctx, c, cfg, team)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Team:      team,
		Cutoff:    cutoff,
		Expired:   FindExpired(chunks, cutoff),
		Conflicts: FindConflicts(chunks),
	}, nil
}

func fetch(ctx context.Context, c opensearch.Client, cfg config.Config, team string) ([]model.Chunk, error) {
	// mem-ads-* 는 mem-ads-data-* 도 잡으므로 team 필드로 거른다
	query := map[string]any{"bool": map[string]any{"filter": []any{
		map[string]any{"term": map[string]any{"team": team}},
		map[string]any{"term": map[string]any{"workspace": cfg.Workspace}},
	}}}
	source := []string{"workspace", "team", "source_path", "chunk_index", "header_path", "repo", "doc_type", "memory_kind", "event_time", "text", "superseded_by", "deprecated"}
	var chunks []model.Chunk
	err := opensearch.Scan(ctx, c, naming.TeamPattern(team), query, source, opensearch.ChunkSort, func(h opensearch.Hit[model.Chunk]) error {
		chunks = append(chunks, h.Source)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("compaction %s: %w", team, err)
	}
	return chunks, nil
}

// FindExpired 는 문서의 가장 최신 청크가 기준보다 오래된 episodic 문서다. knowledge 는 노화하지 않는다.
func FindExpired(chunks []model.Chunk, cutoff time.Time) []ExpiredDoc {
	newest := map[string]model.Chunk{}
	for _, c := range chunks {
		if prev, ok := newest[c.SourcePath]; !ok || prev.EventTime < c.EventTime {
			newest[c.SourcePath] = c
		}
	}
	var out []ExpiredDoc
	for _, c := range newest {
		if c.Kind == model.KindKnowledge || !c.EventTimeValue().Before(cutoff) {
			continue
		}
		out = append(out, ExpiredDoc{SourcePath: c.SourcePath, Repo: c.Repo, DocType: c.DocType, EventTime: c.EventTime})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EventTime == out[j].EventTime {
			return out[i].SourcePath < out[j].SourcePath
		}
		return out[i].EventTime < out[j].EventTime
	})
	return out
}

// FindConflicts 는 같은 repo 안에서 같은 주제(헤더 경로의 마지막 제목)를 서로 다른 문서가 다루면 후보로 낸다.
// 이미 밀린(superseded) 문서는 판정이 끝났으니 빼고, 흔한 섹션 제목은 주제로 보지 않는다.
func FindConflicts(chunks []model.Chunk) []Conflict {
	type group struct {
		repo, topic string
		chunks      []model.Chunk
	}
	groups := map[string]*group{}
	for _, c := range chunks {
		if c.SupersededBy != "" || c.Deprecated {
			continue
		}
		topic := topicOf(c.HeaderPath)
		if topic == "" || genericTopics[topic] {
			continue
		}
		key := c.Repo + "\x00" + topic
		g, ok := groups[key]
		if !ok {
			g = &group{repo: c.Repo, topic: topic}
			groups[key] = g
		}
		g.chunks = append(g.chunks, c)
	}
	var out []Conflict
	for _, g := range groups {
		sources := map[string]bool{}
		for _, c := range g.chunks {
			sources[c.SourcePath] = true
		}
		if len(sources) < 2 {
			continue
		}
		entries := make([]ConflictEntry, 0, len(g.chunks))
		for _, c := range g.chunks {
			entries = append(entries, ConflictEntry{SourcePath: c.SourcePath, HeaderPath: c.HeaderPath, EventTime: c.EventTime, Excerpt: excerpt(c.Text)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].EventTime > entries[j].EventTime })
		out = append(out, Conflict{Repo: g.repo, Topic: g.topic, Entries: entries})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

func topicOf(headerPath string) string {
	parts := strings.Split(headerPath, markdown.HeaderSeparator)
	leaf := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	return strings.TrimSpace(adrNumber.ReplaceAllString(leaf, ""))
}

func excerpt(text string) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > excerptRunes {
		r = r[:excerptRunes]
	}
	return string(r)
}

// Render 는 리포트를 마크다운으로 쓴다.
func Render(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# compaction 리포트 — team %s\n\n기준 시각: event_time < %s\n\n", r.Team, r.Cutoff.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "## 1. 수명 확인 — 경과한 episodic 문서 %d건\n", len(r.Expired))
	if len(r.Expired) == 0 {
		b.WriteString("- 없음\n")
	}
	for _, d := range r.Expired {
		repo := ""
		if d.Repo != "" {
			repo = ", " + d.Repo
		}
		fmt.Fprintf(&b, "- `%s` (%s%s, %s)\n", d.SourcePath, d.DocType, repo, model.Day(d.EventTime))
	}
	fmt.Fprintf(&b, "\n## 2. 충돌 후보 — 같은 repo·같은 주제를 다룬 문서 묶음 %d개\n\n", len(r.Conflicts))
	if len(r.Conflicts) == 0 {
		b.WriteString("- 없음\n\n")
	}
	for _, c := range r.Conflicts {
		repo := c.Repo
		if repo == "" {
			repo = "(repo 없음)"
		}
		fmt.Fprintf(&b, "### %s — %s\n", repo, c.Topic)
		for _, e := range c.Entries {
			fmt.Fprintf(&b, "- %s `%s` › %s\n  > %s\n", model.Day(e.EventTime), e.SourcePath, e.HeaderPath, e.Excerpt)
		}
		b.WriteString("\n")
	}
	b.WriteString("## 다음 단계 (에이전트)\n")
	b.WriteString("3. 분리: 후보마다 정의가 실제로 다른지 원문으로 확인하고, 유효하지 않은 정의는 deprecated 문서로 떼어 낸다.\n")
	b.WriteString("4. 다시 쓰기: 살아남은 사실을 오늘 날짜 frontmatter 로 `docs/knowledge/` 에 다시 쓴다. 추론 없이, 출처 경로를 남긴다.\n")
	b.WriteString("5. 티어 이동: `make tier` — 스냅샷 → hot 원본 제거(git rm) → 인덱스 삭제. 이후 커밋한다.\n")
	return b.String()
}
