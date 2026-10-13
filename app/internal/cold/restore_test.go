package cold

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

const (
	restoreMonth  = "mem-t-2026.01"
	restoreOlder  = "cold-mem-t-2026.01-a"
	restoreNewest = "cold-mem-t-2026.01-b"
	deprecatedDoc = "docs/specs/wrong.md"
)

// restoreCluster 는 restoreMonth 가 스냅샷 둘(restoreOlder, restoreNewest)에 나뉜 클러스터다. mergeErr 가 있으면
// 옛 스냅샷을 임시 이름으로 되살리는 단계(합치기)가 실패한다. open 은 restoreMonth 가 복원본으로 열려 있는가다.
type restoreCluster struct {
	open     atomic.Bool
	mergeErr error
}

func (cl *restoreCluster) fake() *fakeOS {
	unmanaged := map[string]any{opensearch.AutoManageSetting: "false"}
	return &fakeOS{route: func(method, path string, body map[string]any) (any, error) {
		switch {
		case method == http.MethodGet && path == "/_snapshot/mem-cold/_all":
			return map[string]any{"snapshots": []any{
				map[string]any{"snapshot": restoreOlder, "indices": []string{restoreMonth}, "state": "SUCCESS", "start_time": "2026-02-01"},
				map[string]any{"snapshot": restoreNewest, "indices": []string{restoreMonth}, "state": "SUCCESS", "start_time": "2026-03-01"},
			}}, nil
		case method == http.MethodGet && (path == "/mem-t-*/_settings" || path == "/"+restoreMonth+"/_settings"):
			if !cl.open.Load() {
				return map[string]any{}, nil
			}
			return map[string]any{restoreMonth: map[string]any{"settings": unmanaged}}, nil
		case method == http.MethodPost && path == "/_snapshot/mem-cold/"+restoreNewest+"/_restore":
			cl.open.Store(true)
			return nil, nil
		case method == http.MethodPost && path == "/_snapshot/mem-cold/"+restoreOlder+"/_restore":
			return nil, cl.mergeErr
		}
		return nil, nil // 빈 scan, 별칭·표시 갱신
	}}
}

// callIndex 는 method path 요청이 처음 나온 순번이다 (없으면 -1).
func callIndex(f *fakeOS, method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c.Method == method && c.Path == path {
			return i
		}
	}
	return -1
}

// runRestore 는 팀 t 에 deprecate 된 문서(deprecatedDoc)가 하나 있는 설정으로 restoreMonth 를 복원한다.
func runRestore(t *testing.T, cl *restoreCluster) (*fakeOS, error) {
	t.Helper()
	cfg := tierConfig(t)
	deprecated := state.ArchiveRecord{Action: state.ArchiveDeprecate, Path: deprecatedDoc}
	if err := state.NewArchiveState(cfg.MetaRepoRoot).Ledger.Append("t", deprecated); err != nil {
		t.Fatal(err)
	}
	fake := cl.fake()
	_, err := Restore(context.Background(), fake, cfg, restoreMonth)
	return fake, err
}

// R1-cold-3: 옛 스냅샷을 합치다 실패하면, deprecate 표시를 다시 입히지 못한 복원본이 기본 검색에 나오면 안 된다 —
// 검색 alias 는 다 합치고 표시를 입힌 뒤에만 붙이고, 오류는 일부만 복원됐다고 분명히 말한다.
func TestRestoreKeepsAPartialRestoreOutOfSearch(t *testing.T) {
	boom := &opensearch.Error{Status: http.StatusInternalServerError, Reason: "boom", Op: "POST"}

	fake, err := runRestore(t, &restoreCluster{mergeErr: boom})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), restoreMonth) || !strings.Contains(err.Error(), "일부만") {
		t.Errorf("오류가 일부만 복원된 상태를 말하지 않는다: %v", err)
	}
	if got := fake.called(http.MethodPost, "/_aliases"); len(got) != 0 {
		t.Errorf("합치다 실패한 복원본에 검색 alias 를 붙였다: %+v", got)
	}
	for _, c := range fake.called(http.MethodPost, "/_snapshot/mem-cold/"+restoreNewest+"/_restore") {
		if body, _ := c.Body.(map[string]any); body["include_aliases"] != false {
			t.Errorf("스냅샷의 alias 로 되살리면 합치기 전에 검색에 나온다: %+v", body)
		}
	}
}

// R1-cold-3: 이미 alias 가 붙은 복원본(예전 판이 최신 스냅샷만 되살린 복원본)에 합치다 실패해도, 더한 문서까지
// deprecate 표시를 다시 입힌다.
func TestRestoreReappliesDeprecationsWhenMergeFails(t *testing.T) {
	boom := &opensearch.Error{Status: http.StatusInternalServerError, Reason: "boom", Op: "POST"}
	cl := &restoreCluster{mergeErr: boom}
	cl.open.Store(true)

	fake, err := runRestore(t, cl)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if callIndex(fake, http.MethodPost, "/"+restoreMonth+"/_update_by_query") < 0 {
		t.Errorf("합치다 실패해도 deprecate 표시를 다시 입힌다 — 요청 = %+v", fake.calls)
	}
}

// R1-cold-3: 다 합친 복원은 deprecate 표시를 먼저 입히고 그다음에 검색 alias 를 붙인다 — 그 사이에 deprecate 된
// 문서가 기본 검색에 나오지 않는다.
func TestRestoreMarksDeprecationsBeforeSearch(t *testing.T) {
	fake, err := runRestore(t, &restoreCluster{})

	if err != nil {
		t.Fatal(err)
	}
	marked := callIndex(fake, http.MethodPost, "/"+restoreMonth+"/_update_by_query")
	searchable := callIndex(fake, http.MethodPost, "/_aliases")
	if marked < 0 || searchable < 0 || marked > searchable {
		t.Errorf("표시(%d)가 alias(%d)보다 먼저여야 한다 — 요청 = %+v", marked, searchable, fake.calls)
	}
}
