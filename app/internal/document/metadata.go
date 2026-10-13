// Package document 는 파일 하나를 메타데이터와 청크로 바꾼다.
package document

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
)

// RootGuideFiles 는 meta-repository 루트에서 색인하는 안내 문서다 (guide = knowledge, 노화하지 않는다).
// ingest 의 색인 대상 판단도 이 목록을 쓴다 — 두 목록이 어긋나지 않게 한 곳에 둔다.
var RootGuideFiles = map[string]bool{"README.md": true, "INDEX.md": true, "CLAUDE.md": true, "GUIDE.md": true}

var (
	// docs/repository/{repo}/... — 그 repository 에만 해당하는 메타 문서
	repoDocs = regexp.MustCompile(`^docs/repository/([^/]+)/`)
	// ManagedRepoDocs 는 main/{repo}/docs/... — 관리 대상 repository 자신의 문서다
	ManagedRepoDocs = regexp.MustCompile(`^main/([^/]+)/docs/`)
	// docs/{feature,specs,issues}/{주제}/... — 주제 디렉토리 (CLAUDE.md 의 저장 위치)
	topicDirs = regexp.MustCompile(`^docs/(?:feature|specs|issues)/([^/]+)/`)
	nonTopic  = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

type typeHint struct {
	pattern *regexp.Regexp
	docType model.DocType
}

// 위에서부터 첫 매치. 어디에도 안 걸리면 note.
var typeHints = []typeHint{
	// 안내·목차 문서 — 시간이 지나도 낡지 않는 정본
	{regexp.MustCompile(`(^|/)INDEX\.md$|^docs/guides/`), model.DocGuide},
	{regexp.MustCompile(`(?i)(^|/)(adr|adrs|decisions?)(/|$)|(^|/)adr[-_]`), model.DocADR},
	{regexp.MustCompile(`(?i)(^|/)prds?(/|$)|(^|/)prd[-_]`), model.DocPRD},
	{regexp.MustCompile(`(?i)(^|/)plans?(/|$)|(^|/)plan[-_]|[-_]plan\.md$|(^|/)plan\.md$`), model.DocPlan},
	{regexp.MustCompile(`(^|/)knowledge/`), model.DocKnowledge},
	{regexp.MustCompile(`(?i)(^|/)specs?(/|$)|(^|/)spec[-_]|[-_]spec\.md$`), model.DocSpec},
}

// pathFacts 는 경로에서 추론한 값이다.
type pathFacts struct {
	Repo    string
	Topic   string
	DocType model.DocType
}

// factsFromPath 는 meta-repository 기준 상대 경로에서 repo 와 문서 종류를 추론한다.
func factsFromPath(rel string) pathFacts {
	rel = strings.ReplaceAll(rel, "\\", "/")
	facts := pathFacts{DocType: model.DocNote}
	if m := repoDocs.FindStringSubmatch(rel); m != nil {
		facts.Repo = NormalizeProject(m[1])
	} else if m := ManagedRepoDocs.FindStringSubmatch(rel); m != nil {
		facts.Repo = NormalizeProject(m[1])
	}
	if m := topicDirs.FindStringSubmatch(rel); m != nil {
		facts.Topic = NormalizeTopic(m[1])
	}
	if RootGuideFiles[rel] {
		facts.DocType = model.DocGuide
		return facts
	}
	for _, h := range typeHints {
		if h.pattern.MatchString(rel) {
			facts.DocType = h.docType
			break
		}
	}
	return facts
}

// kindOf 는 기억의 성질이다. knowledge·guide 문서, 그리고 관리 대상 repository(main/{repo}/docs)의 문서는
// 누가 바꾸기 전까지 유효한 사실이라 knowledge — 유사도로 판정하지 않고 노화하지 않는다.
// 나머지(meta-repo 의 spec·adr·prd·plan·note)는 시간순으로 쌓이는 기록이라 episodic.
func kindOf(docType model.DocType, rel string) model.Kind {
	if docType == model.DocKnowledge || docType == model.DocGuide || strings.HasPrefix(rel, "main/") {
		return model.KindKnowledge
	}
	return model.KindEpisodic
}

// Metadata 는 frontmatter 와 경로에서 정한 문서 메타다. frontmatter 가 경로 추론보다 우선한다.
type Metadata struct {
	Team         string
	Repo         string // 프로젝트
	Topic        string
	DocType      model.DocType
	Kind         model.Kind
	DeclaredTime time.Time // frontmatter 날짜. 없으면 zero (호출자가 git/mtime 으로 채운다)
	DeclaredDay  string    // frontmatter 날짜가 시각 없는 날짜(YYYY-MM-DD)면 그 날 — 시간대와 상관없이 적힌 그대로의 날이다
	Supersedes   string    // 개정판이 밀어내는 구판 경로 (선언은 항상 새 문서 쪽에 쓴다)
	Links        []model.Link
}

// ResolveMetadata 는 frontmatter 와 경로로 메타를 정한다.
func ResolveMetadata(fm markdown.Frontmatter, rel, defaultTeam string) (Metadata, error) {
	facts := factsFromPath(rel)
	team := strings.ToLower(firstString(fm, "team"))
	if team == "" {
		team = defaultTeam
	}
	if err := naming.ValidateTeam(team); err != nil {
		return Metadata{}, &markdown.FrontmatterError{Err: err} // frontmatter 값 문제 — audit 가 bad-frontmatter 로 보고한다
	}
	docType := facts.DocType
	if t, ok := model.ParseDocType(strings.ToLower(firstString(fm, "doc_type", "type"))); ok {
		docType = t
	}
	repo := NormalizeProject(firstString(fm, "repo", "project"))
	if repo == "" {
		repo = facts.Repo
	}
	topic := NormalizeTopic(firstString(fm, "topic"))
	if topic == "" {
		topic = facts.Topic
	}
	return Metadata{
		Team:         team,
		Repo:         repo,
		Topic:        topic,
		DocType:      docType,
		Kind:         kindOf(docType, rel),
		DeclaredTime: firstTime(fm, "event_time", "date"),
		DeclaredDay:  firstDay(fm, "event_time", "date"),
		Supersedes:   CanonicalPath(firstString(fm, "supersedes")),
		Links:        frontmatterLinks(fm),
	}, nil
}

// dayLayout 은 날짜만 있는 값의 꼴이다.
const dayLayout = "2006-01-02"

// NormalizeTopic 은 주제를 비교할 수 있는 꼴로 맞춘다: NFC, 소문자, 글자·숫자가 아닌 것은 하이픈 하나로.
// "Payment Retry" 와 "payment-retry" 를 같은 주제로 본다. NFC 를 먼저 한다 — Finder 가 만든 NFD 이름도 사람이 친(NFC)
// 주제와 같은 값이 되고, NFD 의 결합 문자(악센트)가 글자가 아니라서 하이픈으로 바뀌지 않게.
func NormalizeTopic(s string) string {
	return strings.Trim(nonTopic.ReplaceAllString(strings.ToLower(norm.NFC.String(strings.TrimSpace(s))), "-"), "-")
}

// NormalizeProject 는 프로젝트 이름을 비교할 수 있는 꼴로 맞춘다: 앞뒤 공백을 빼고 NFC. 이름 자체(대소문자·공백)는 그대로다.
func NormalizeProject(s string) string {
	return norm.NFC.String(strings.TrimSpace(s))
}

// CanonicalPath 는 문서 경로(source_path, 그리고 그것을 가리키는 링크·supersedes)의 정본 꼴이다 — 유니코드 NFC.
// macOS 의 Finder 는 한글 이름을 NFD 로 쓰고 사람·LLM 은 NFC 로 친다. 같은 글자의 두 철자가 한 문서가 되게 NFC 하나로 맞춘다.
func CanonicalPath(p string) string {
	return norm.NFC.String(p)
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
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", dayLayout} {
				if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
					return t.UTC()
				}
			}
		}
	}
	return time.Time{}
}

// firstDay 는 firstTime 이 읽을 값이 시각 없는 날짜일 때 그 날짜다. 시각이 있으면 "" (호출자가 시간대로 날을 정한다).
func firstDay(fm markdown.Frontmatter, keys ...string) string {
	for _, k := range keys {
		switch v := fm[k].(type) {
		case time.Time:
			// YAML 이 날짜만 있는 값을 UTC 자정으로 읽었다
			if v.Location() == time.UTC && v.Equal(v.Truncate(24*time.Hour)) {
				return v.Format(dayLayout)
			}
			return ""
		case string:
			if s := strings.TrimSpace(v); s != "" {
				if _, err := time.Parse(dayLayout, s); err == nil {
					return s
				}
				return ""
			}
		}
	}
	return ""
}

// frontmatterLinks 는 derived_from·relates 목록이다. 각 항목은 {path, why} — why 가 없는 링크는 버린다
// (이유 없는 링크는 열어 볼지 판단할 수 없다).
func frontmatterLinks(fm markdown.Frontmatter) []model.Link {
	var links []model.Link
	for key, typ := range map[string]model.LinkType{"derived_from": model.LinkDerivedFrom, "relates": model.LinkRelates} {
		items, _ := fm[key].([]any)
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			p, _ := m["path"].(string)
			why, _ := m["why"].(string)
			if strings.TrimSpace(p) == "" || strings.TrimSpace(why) == "" {
				continue
			}
			links = append(links, model.Link{Type: typ, To: CanonicalPath(strings.TrimSpace(p)), Why: strings.TrimSpace(why)})
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Type != links[j].Type {
			return links[i].Type < links[j].Type
		}
		return links[i].To < links[j].To
	})
	return links
}
