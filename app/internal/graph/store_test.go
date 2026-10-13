package graph

import (
	"reflect"
	"testing"
)

// SCN-G7-1: entity 질의는 문서마다 경로와 상태를 돌려준다 — docs_by_entity 가 기본 검색에서 숨는 문서(밀린·deprecate 된)를
// related_docs 처럼 표시할 수 있게. 모양이 맞지 않는 항목은 버린다.
func TestDocRefsReadsPathAndStatus(t *testing.T) {
	rows := []any{
		map[string]any{"path": "docs/a.md", "status": StatusHot},
		map[string]any{"path": "docs/b.md", "status": StatusSuperseded},
		"docs/c.md",
		map[string]any{"status": StatusHot},
	}

	got := docRefs(rows)

	want := []DocRef{{Path: "docs/a.md", Status: StatusHot}, {Path: "docs/b.md", Status: StatusSuperseded}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("docRefs = %+v, want %+v", got, want)
	}
}
