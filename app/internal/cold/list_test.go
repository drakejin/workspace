package cold

import (
	"context"
	"net/http"
	"testing"

	"github.com/drakejin/workspace/app/internal/opensearch"
)

// R2-followups-2: 합치다 멈춘 복원본은 검색 alias 가 없어 검색에 나오지 않는다. list_cold 는 그것을 다 복원된 인덱스와
// 구분해야 한다 — 에이전트가 "복원됨" 을 보고 restore_cold 를 다시 하지 않는다.
func TestListColdTellsAPartialRestoreFromASearchableOne(t *testing.T) {
	unmanaged := map[string]any{opensearch.AutoManageSetting: "false"}
	fake := &fakeOS{route: func(method, path string, _ map[string]any) (any, error) {
		switch {
		case method == http.MethodGet && path == "/_snapshot/mem-cold/_all":
			return map[string]any{"snapshots": []any{
				map[string]any{"snapshot": "cold-a", "indices": []string{"mem-t-2026.01"}, "state": "SUCCESS", "start_time": "2026-04-01"},
				map[string]any{"snapshot": "cold-b", "indices": []string{"mem-t-2026.02"}, "state": "SUCCESS", "start_time": "2026-05-01"},
				map[string]any{"snapshot": "cold-c", "indices": []string{"mem-t-2026.03"}, "state": "SUCCESS", "start_time": "2026-06-01"},
			}}, nil
		case method == http.MethodGet && path == "/mem-t-*/_settings":
			return map[string]any{
				"mem-t-2026.01": map[string]any{"settings": unmanaged},
				"mem-t-2026.02": map[string]any{"settings": unmanaged},
			}, nil
		case method == http.MethodGet && path == "/mem-t-*/_alias":
			return map[string]any{
				"mem-t-2026.01": map[string]any{"aliases": map[string]any{"mem-t-hot": map[string]any{}}},
				"mem-t-2026.02": map[string]any{"aliases": map[string]any{}}, // 합치다 멈췄다
			}, nil
		}
		return nil, nil
	}}

	list, err := ListCold(context.Background(), fake, tierConfig(t), "t")

	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v %v", list, err)
	}
	want := map[string][2]bool{ // restored, searchable
		"mem-t-2026.01": {true, true},
		"mem-t-2026.02": {true, false},
		"mem-t-2026.03": {false, false},
	}
	for _, idx := range list {
		if got := [2]bool{idx.Restored, idx.Searchable}; got != want[idx.Name] {
			t.Errorf("%s: restored/searchable = %v, want %v", idx.Name, got, want[idx.Name])
		}
	}
}
