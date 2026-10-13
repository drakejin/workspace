package opensearch

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// 쓰기는 클러스터 전체(mem-*)가 아니라 그 팀의 인덱스에만 보낸다 — 같은 스택의 다른 workspace 가 그 사이 인덱스를 지우면
// mem-* 에 보낸 _update_by_query·_delete_by_query 가 search_context_missing 으로 실패한다 (대상 문서는 바뀌었는데 명령은 실패).
// 팀 이름은 정확히 맞춘다: mem-t-* 패턴은 팀 t-x 의 인덱스까지 잡는다.
func TestTeamIndicesMatchesTheTeamExactly(t *testing.T) {
	os := &fakeOS{reply: func(_, path string, _ any) (any, error) {
		if !strings.HasSuffix(path, "/_settings") {
			return map[string]any{}, nil
		}
		return map[string]any{
			"mem-t-2026.10":     map[string]any{"settings": map[string]any{}},
			"mem-t-knowledge":   map[string]any{"settings": map[string]any{}},
			"mem-t-x-2026.10":   map[string]any{"settings": map[string]any{}},
			"mem-other-2026.10": map[string]any{"settings": map[string]any{}},
			"not-memory":        map[string]any{"settings": map[string]any{}},
		}, nil
	}}
	got, err := TeamIndices(context.Background(), os, "t")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mem-t-2026.10", "mem-t-knowledge"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TeamIndices(t) = %v, want %v", got, want)
	}
	got, _ = TeamIndices(context.Background(), os, "t", "t-x")
	if want := []string{"mem-t-2026.10", "mem-t-knowledge", "mem-t-x-2026.10"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TeamIndices(t, t-x) = %v, want %v", got, want)
	}
	if got, _ = TeamIndices(context.Background(), os); len(got) != 0 {
		t.Errorf("팀이 없으면 인덱스도 없다: %v", got)
	}
}
