package search

import (
	"reflect"
	"testing"
)

// hasTextFilter 는 본문이 있는 청크만 고르는 필터다 — 본문이 없는 문서(빈 파일·frontmatter 만)의 청크 0 은 text 필드가 없다.
var hasTextFilter = map[string]any{"exists": map[string]any{"field": "text"}}

// R2-handoff-1: 본문이 없는 문서는 검색 결과에 나오지 않는다 — 필터로만 가져오기(최신순)의 맨 위를 빈 결과로 차지해 size 안의
// 진짜 문서를 밀어내고, 세션 훅(질의어 검색)의 맥락에 빈 문서가 들어갔다. 밀린 문서를 함께 볼 때도 마찬가지다.
func TestBuildQueryLeavesOutBlankDocuments(t *testing.T) {
	for name, r := range map[string]Request{
		"필터로만 가져오기":    {Workspace: "ws", Team: "t", DocType: "knowledge"},
		"질의어 검색":       {Workspace: "ws", Team: "t", Query: "배송비"},
		"밀린 문서도 함께 보기": {Team: "t", Query: "배송비", IncludeSuperseded: true},
	} {
		if got := BuildQuery(r)["post_filter"]; !reflect.DeepEqual(got, hasTextFilter) {
			t.Errorf("%s: text 가 있는 청크만 돌려주지 않는다: %v", name, got)
		}
	}
}
