// Package model 은 메모리 층이 다루는 문서·청크·링크 타입이다.
package model

import "time"

// DocType 은 문서 종류다. 발표의 메타 정보 네 가지(spec·adr·prd·plan)에 knowledge·guide·note 를 더했다.
type DocType string

const (
	DocSpec      DocType = "spec"
	DocADR       DocType = "adr"
	DocPRD       DocType = "prd"
	DocPlan      DocType = "plan"
	DocKnowledge DocType = "knowledge"
	DocGuide     DocType = "guide"
	DocNote      DocType = "note"
)

// DocTypes 는 허용되는 문서 종류 전부다.
var DocTypes = []DocType{DocSpec, DocADR, DocPRD, DocPlan, DocKnowledge, DocGuide, DocNote}

// ParseDocType 은 문자열을 DocType 으로 바꾼다. 모르는 값이면 false.
func ParseDocType(s string) (DocType, bool) {
	for _, t := range DocTypes {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

// Kind 는 기억의 성질이다 — 시간이 지나면 낡는 기록(episodic)인가, 누가 바꾸기 전까지 유효한 사실(knowledge)인가.
type Kind string

const (
	KindEpisodic  Kind = "episodic"
	KindKnowledge Kind = "knowledge"
)

// IndexVersion 은 청크에 담는 필드 구성의 판이다. 필드를 더하면 올린다 — 데몬은 판이 낮은 문서를 내용이 같아도 다시 써서
// 새 필드(예: topic)를 채운다. 1: 처음, 2: topic, 3: topic·repo 를 질의어로도 검색 (text 하위 필드),
// 4: event_day (날짜 필터가 UTC 가 아니라 문서의 날로 맞게).
const IndexVersion = 4

// Chunk 는 OpenSearch 에 들어가는 청크 한 건이다. ID 는 _id 라 본문(_source)에 넣지 않는다.
type Chunk struct {
	ID           string   `json:"-"`
	Workspace    string   `json:"workspace"`
	Team         string   `json:"team"`
	Repo         string   `json:"repo,omitempty"`  // 프로젝트 — 관리 대상 repository 이름 (frontmatter repo 또는 project)
	Topic        string   `json:"topic,omitempty"` // 주제 — frontmatter topic 또는 docs/{feature,specs,issues}/{주제}/
	DocType      DocType  `json:"doc_type"`
	Kind         Kind     `json:"memory_kind"`
	EventTime    string   `json:"event_time"`
	EventDay     string   `json:"event_day"` // 문서의 날 (YYYY-MM-DD) — frontmatter 날짜 그대로, 아니면 설정한 시간대에서 본 유효 시각의 날
	SourcePath   string   `json:"source_path"`
	GitSHA       string   `json:"git_sha,omitempty"`
	HeaderPath   string   `json:"header_path"`
	ChunkIndex   int      `json:"chunk_index"`
	Text         string   `json:"text"`
	IngestedAt   string   `json:"ingested_at"`
	ContentSHA   string   `json:"content_sha"`
	BodySHA      string   `json:"body_sha"`
	MtimeMs      int64    `json:"mtime_ms"`
	Size         int64    `json:"size"`
	Title        string   `json:"title"`
	IndexVersion int      `json:"index_version"`
	Supersedes   string   `json:"supersedes,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	Deprecated   bool     `json:"deprecated,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Entities     []string `json:"entities,omitempty"`
}

// EventTimeValue 는 EventTime 을 시각으로 읽는다.
func (c Chunk) EventTimeValue() time.Time { return ParseTime(c.EventTime) }

// ParseTime 은 RFC3339(Nano) 문자열을 읽는다. 읽지 못하면 zero time.
func ParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Day 는 RFC3339 문자열의 날짜 부분(yyyy-mm-dd)이다.
func Day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// TimeRange 는 검색 시간 필터다. ISO 날짜 또는 date math(now-30d).
type TimeRange struct {
	From string `json:"from,omitempty" jsonschema:"ISO 날짜 또는 date math (예: now-30d)"`
	To   string `json:"to,omitempty" jsonschema:"ISO 날짜 또는 date math"`
}

// Distilled 는 증류 산출이다.
type Distilled struct {
	Summary  string   `json:"summary"`
	Entities []string `json:"entities"`
}

// LinkType 은 문서 사이 관계의 종류다. 그래프의 edge 가 된다.
type LinkType string

const (
	LinkSupersedes  LinkType = "SUPERSEDES"   // 개정판 → 구판 (frontmatter supersedes, 판정 원장)
	LinkDerivedFrom LinkType = "DERIVED_FROM" // knowledge → 근거가 된 기록 (frontmatter derived_from, why 필수)
	LinkRelates     LinkType = "RELATES"      // 관련 문서 (frontmatter relates, why 필수)
	LinkLinksTo     LinkType = "LINKS_TO"     // 본문의 마크다운 링크
)

// Link 는 문서에서 다른 문서로 가는 관계 하나다. 대상은 meta-repository 기준 상대 경로.
type Link struct {
	Type LinkType
	To   string
	Why  string
}
