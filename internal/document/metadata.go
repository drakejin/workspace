// Package document 는 파일 하나를 메타데이터와 청크로 바꾼다.
package document

import (
	"regexp"
	"strings"
	"time"

	"github.com/drakejin/workspace/internal/markdown"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
)

var (
	// docs/repository/{repo}/... (발표 구조) 또는 docs/main/{repo}/... (워크스페이스 init 구조)
	repoDocs = regexp.MustCompile(`^docs/(?:repository|main)/([^/]+)/`)
	// main/{repo}/docs/... — 관리 대상 repository 안의 docs
	managedRepoDocs = regexp.MustCompile(`^main/([^/]+)/docs/`)
)

type typeHint struct {
	pattern *regexp.Regexp
	docType model.DocType
}

// 위에서부터 첫 매치. 어디에도 안 걸리면 note.
var typeHints = []typeHint{
	// 안내·목차 문서 — 시간이 지나도 낡지 않는 정본
	{regexp.MustCompile(`^(README|INDEX|CLAUDE)\.md$|(^|/)INDEX\.md$|^docs/guides/`), model.DocGuide},
	{regexp.MustCompile(`(?i)(^|/)(adr|adrs|decisions?)(/|$)|(^|/)adr[-_]`), model.DocADR},
	{regexp.MustCompile(`(?i)(^|/)prds?(/|$)|(^|/)prd[-_]`), model.DocPRD},
	{regexp.MustCompile(`(?i)(^|/)plans?(/|$)|(^|/)plan[-_]|[-_]plan\.md$|(^|/)plan\.md$`), model.DocPlan},
	{regexp.MustCompile(`(^|/)knowledge/`), model.DocKnowledge},
	{regexp.MustCompile(`(?i)(^|/)specs?(/|$)|(^|/)spec[-_]|[-_]spec\.md$`), model.DocSpec},
}

// PathFacts 는 경로에서 추론한 값이다.
type PathFacts struct {
	Repo    string
	DocType model.DocType
}

// FactsFromPath 는 meta-repository 기준 상대 경로에서 repo 와 문서 종류를 추론한다.
func FactsFromPath(rel string) PathFacts {
	rel = strings.ReplaceAll(rel, "\\", "/")
	facts := PathFacts{DocType: model.DocNote}
	if m := repoDocs.FindStringSubmatch(rel); m != nil {
		facts.Repo = m[1]
	} else if m := managedRepoDocs.FindStringSubmatch(rel); m != nil {
		facts.Repo = m[1]
	}
	for _, h := range typeHints {
		if h.pattern.MatchString(rel) {
			facts.DocType = h.docType
			break
		}
	}
	return facts
}

// KindOf 는 기억의 성질이다. knowledge·guide 문서, 그리고 관리 대상 repository(main/{repo}/docs)의 문서는
// 누가 바꾸기 전까지 유효한 사실이라 knowledge — 유사도로 판정하지 않고 노화하지 않는다.
// 나머지(meta-repo 의 spec·adr·prd·plan·note)는 시간순으로 쌓이는 기록이라 episodic.
func KindOf(docType model.DocType, rel string) model.Kind {
	if docType == model.DocKnowledge || docType == model.DocGuide || strings.HasPrefix(rel, "main/") {
		return model.KindKnowledge
	}
	return model.KindEpisodic
}

// Metadata 는 frontmatter 와 경로에서 정한 문서 메타다. frontmatter 가 경로 추론보다 우선한다.
type Metadata struct {
	Team         string
	Repo         string
	DocType      model.DocType
	Kind         model.Kind
	DeclaredTime time.Time // frontmatter 날짜. 없으면 zero (호출자가 git/mtime 으로 채운다)
	Supersedes   string    // 개정판이 밀어내는 구판 경로 (선언은 항상 새 문서 쪽에 쓴다)
}

// ResolveMetadata 는 frontmatter 와 경로로 메타를 정한다.
func ResolveMetadata(fm markdown.Frontmatter, rel, defaultTeam string) (Metadata, error) {
	facts := FactsFromPath(rel)
	team := strings.ToLower(firstString(fm, "team"))
	if team == "" {
		team = defaultTeam
	}
	if err := naming.ValidateTeam(team); err != nil {
		return Metadata{}, err
	}
	docType := facts.DocType
	if t, ok := model.ParseDocType(strings.ToLower(firstString(fm, "doc_type", "type"))); ok {
		docType = t
	}
	repo := firstString(fm, "repo")
	if repo == "" {
		repo = facts.Repo
	}
	return Metadata{
		Team:         team,
		Repo:         repo,
		DocType:      docType,
		Kind:         KindOf(docType, rel),
		DeclaredTime: firstTime(fm, "event_time", "date"),
		Supersedes:   firstString(fm, "supersedes"),
	}, nil
}

func firstString(fm markdown.Frontmatter, keys ...string) string {
	for _, k := range keys {
		if s, ok := fm[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstTime(fm markdown.Frontmatter, keys ...string) time.Time {
	for _, k := range keys {
		switch v := fm[k].(type) {
		case time.Time:
			return v.UTC()
		case string:
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
				if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
					return t.UTC()
				}
			}
		}
	}
	return time.Time{}
}
