// Package model 은 메모리 층이 다루는 문서·청크 타입이다.
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

// Chunk 는 OpenSearch 에 들어가는 청크 한 건이다. ID 는 _id 라 본문(_source)에 넣지 않는다.
type Chunk struct {
	ID           string   `json:"-"`
	Workspace    string   `json:"workspace"`
	Team         string   `json:"team"`
	Repo         string   `json:"repo,omitempty"`
	DocType      DocType  `json:"doc_type"`
	Kind         Kind     `json:"memory_kind"`
	EventTime    string   `json:"event_time"`
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
	Supersedes   string   `json:"supersedes,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Entities     []string `json:"entities,omitempty"`
}

// EventTimeValue 는 EventTime 을 시각으로 읽는다.
func (c Chunk) EventTimeValue() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, c.EventTime)
	return t
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
