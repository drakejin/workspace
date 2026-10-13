package opensearch

import (
	"fmt"

	"github.com/drakejin/workspace/app/internal/naming"
)

const (
	// templateName 은 모든 mem-* 인덱스에 붙는 인덱스 템플릿이다.
	templateName = "mem-template"
	// HotColdPolicy 는 episodic 월별 인덱스의 ISM 정책이다: hot → 스냅샷 → 삭제.
	HotColdPolicy = "mem-hot-cold"
	// keepPolicy 는 knowledge 인덱스의 ISM 정책이다: 노화하지 않는다. 우선순위가 높아 HotColdPolicy 를 이긴다.
	keepPolicy = "mem-knowledge-keep"
	// AutoManageSetting 이 false 면 ISM 이 인덱스를 관리하지 않는다 (restore_cold 로 복원한 인덱스).
	AutoManageSetting = "index.plugins.index_state_management.auto_manage"

	textAnalyzer   = "memory_text"
	searchAnalyzer = "memory_search"
)

func analyzed() map[string]any {
	return map[string]any{"analyzer": textAnalyzer, "search_analyzer": searchAnalyzer}
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// Properties 는 청크 매핑이다. 이미 있는 인덱스에도 put mapping 으로 더한다(추가만이라 안전).
func Properties() map[string]any {
	text := merge(map[string]any{"type": "text"}, analyzed())
	return map[string]any{
		"workspace":  map[string]any{"type": "keyword"},
		"team":       map[string]any{"type": "keyword"},
		"deprecated": map[string]any{"type": "boolean"},
		// 넘김 표시 — 데몬 밖에서 쓴 문서의 청크 0 에만 있다 (다음 데몬 사이클이 판정·증류·그래프로 넘기고 지운다)
		"pending_cycle": map[string]any{"type": "boolean"},
		// 프로젝트·주제는 필터(keyword)이면서 질의어로도 맞게(text) 둔다
		"repo":          map[string]any{"type": "keyword", "fields": map[string]any{"text": text}},
		"topic":         map[string]any{"type": "keyword", "fields": map[string]any{"text": text}},
		"index_version": map[string]any{"type": "integer"},
		"doc_type":      map[string]any{"type": "keyword"},
		"memory_kind":   map[string]any{"type": "keyword"},
		"event_time":    map[string]any{"type": "date"},
		"event_day":     map[string]any{"type": "keyword"}, // YYYY-MM-DD — 날짜 필터는 이 문자열로 비교한다 (시간대와 무관)
		"source_path":   map[string]any{"type": "keyword", "fields": map[string]any{"text": text}},
		"git_sha":       map[string]any{"type": "keyword"},
		"header_path":   merge(text, map[string]any{"fields": map[string]any{"keyword": map[string]any{"type": "keyword", "ignore_above": 512}}}),
		"chunk_index":   map[string]any{"type": "integer"},
		"text":          text,
		"ingested_at":   map[string]any{"type": "date"},
		"content_sha":   map[string]any{"type": "keyword"},
		"body_sha":      map[string]any{"type": "keyword"},
		"mtime_ms":      map[string]any{"type": "long"},
		"size":          map[string]any{"type": "long"},
		"title":         text,
		"supersedes":    map[string]any{"type": "keyword"},
		"superseded_by": map[string]any{"type": "keyword"},
		"summary":       text,
		"entities":      map[string]any{"type": "keyword"},
	}
}

// indexTemplate 은 mem-* 인덱스 템플릿이다. 임베딩 없이 BM25(기본 similarity)만 쓴다.
// 식별자 분할 analyzer: `ClickDeduplicator.window`, `click_dedup_window` 를 원형과 조각 모두로 색인해
// 함수명·필드명 질의가 맞게 한다 (기본 standard analyzer 는 이걸 토큰 하나로 묶어 0건이 나왔다).
func indexTemplate() map[string]any {
	return map[string]any{
		"index_patterns": []string{naming.AllPattern},
		"priority":       100,
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":   1,
				"number_of_replicas": 0,
				"analysis": map[string]any{
					"filter": map[string]any{
						"identifier_split": map[string]any{
							"type":                    "word_delimiter_graph",
							"preserve_original":       true,
							"split_on_case_change":    true,
							"split_on_numerics":       false,
							"stem_english_possessive": false,
						},
					},
					"analyzer": map[string]any{
						textAnalyzer:   map[string]any{"tokenizer": "standard", "filter": []string{"identifier_split", "lowercase", "flatten_graph"}},
						searchAnalyzer: map[string]any{"tokenizer": "standard", "filter": []string{"identifier_split", "lowercase"}},
					},
				},
			},
			"mappings": map[string]any{"dynamic": "strict", "properties": Properties()},
		},
	}
}

// HotColdPolicyBody 는 hot(hotDays) → 스냅샷 → 삭제 정책이다.
func HotColdPolicyBody(hotDays int, repository string) map[string]any {
	return map[string]any{"policy": map[string]any{
		"description":   fmt.Sprintf("memory episodic: hot %dd → snapshot(%s) → delete", hotDays, repository),
		"default_state": "hot",
		"states": []any{
			map[string]any{
				"name":        "hot",
				"actions":     []any{},
				"transitions": []any{map[string]any{"state_name": "cold", "conditions": map[string]any{"min_index_age": fmt.Sprintf("%dd", hotDays)}}},
			},
			map[string]any{
				"name":        "cold",
				"actions":     []any{map[string]any{"snapshot": map[string]any{"repository": repository, "snapshot": "ism-cold"}}},
				"transitions": []any{map[string]any{"state_name": "delete"}},
			},
			map[string]any{"name": "delete", "actions": []any{map[string]any{"delete": map[string]any{}}}, "transitions": []any{}},
		},
		"ism_template": []any{map[string]any{"index_patterns": []string{naming.AllPattern}, "priority": 100}},
	}}
}

// keepPolicyBody 는 knowledge 인덱스를 노화시키지 않는 정책이다.
func keepPolicyBody() map[string]any {
	return map[string]any{"policy": map[string]any{
		"description":   "memory knowledge: never ages (supersedes 로만 밀린다)",
		"default_state": "keep",
		"states":        []any{map[string]any{"name": "keep", "actions": []any{}, "transitions": []any{}}},
		"ism_template":  []any{map[string]any{"index_patterns": []string{naming.KnowledgePattern}, "priority": 200}},
	}}
}
