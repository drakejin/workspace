package cold

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// tierNow 는 tier 단위 테스트의 지금이다 — hot 기간 60일이면 2026.07 까지가 cold 대상이다.
var tierNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func tierConfig(t *testing.T) config.Config {
	return config.Config{Workspace: "ws", HotDays: 60, MetaRepoRoot: t.TempDir(), Snapshot: config.Snapshot{Repository: "mem-cold"}}
}

// cluster 는 팀 t 의 월별 인덱스 하나를 가진 클러스터다. newest 가 비면 문서가 없는 인덱스다.
type cluster struct {
	index, newest string
	count         int   // 쓰기를 막은 뒤 다시 센 문서 수
	deleteErr     error // 인덱스 삭제의 실패
	snapshotState string
}

func (cl cluster) fake() *fakeOS {
	return &fakeOS{route: func(method, path string, body map[string]any) (any, error) {
		switch {
		case method == http.MethodGet && strings.HasPrefix(path, "/_cat/indices/"):
			return []map[string]string{{"index": cl.index}}, nil
		case method == http.MethodPost && path == "/mem-*/_search": // WorkspaceTeams
			return map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": []any{map[string]any{"key": "t"}}}}}, nil
		case method == http.MethodGet && path == "/"+cl.index+"/_settings":
			return map[string]any{cl.index: map[string]any{"settings": map[string]any{}}}, nil
		case method == http.MethodPost && path == "/"+cl.index+"/_search" && body["aggs"] != nil:
			return map[string]any{"aggregations": map[string]any{"newest": map[string]any{"value_as_string": cl.newest}}}, nil
		case method == http.MethodPost && path == "/"+cl.index+"/_count":
			return map[string]any{"count": cl.count}, nil
		case method == http.MethodPut && strings.HasPrefix(path, "/_snapshot/mem-cold/"):
			return map[string]any{"snapshot": map[string]any{"state": cl.snapshotState}}, nil
		case method == http.MethodDelete && path == "/"+cl.index:
			return nil, cl.deleteErr
		}
		return nil, nil // 쓰기 막기, 빈 scan, 빈 스냅샷 목록
	}}
}

// blocked 는 index 의 마지막 설정 변경이 쓰기 막기인가다.
func blocked(fake *fakeOS, index string) bool {
	settings := fake.called(http.MethodPut, "/"+index+"/_settings")
	if len(settings) == 0 {
		return false
	}
	body, _ := settings[len(settings)-1].Body.(map[string]any)
	return body["index.blocks.write"] == true
}

func TestTierLiftsWriteBlockWhenIndexDeleteFails(t *testing.T) {
	cfg := tierConfig(t)
	boom := &opensearch.Error{Status: http.StatusInternalServerError, Reason: "boom", Op: "DELETE"}
	fake := cluster{index: "mem-t-2026.01", newest: "2026-01-10T00:00:00.000Z", deleteErr: boom, snapshotState: "SUCCESS"}.fake()

	_, err := Tier(context.Background(), fake, NewLocalStore(t.TempDir()), cfg, TierOptions{Now: tierNow, Team: "t"})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if blocked(fake, "mem-t-2026.01") {
		t.Errorf("삭제가 실패하면 쓰기 막기를 푼다 — 요청 = %+v", fake.calls)
	}
}

func TestTierNeverSnapshotsAnEmptyIndex(t *testing.T) {
	cases := []struct {
		name, index string
		count       int
		dryRun      bool
		want        TierAction
		deleted     bool
	}{
		{"이번 달의 빈 인덱스는 hot 에 둔다", "mem-t-2026.10", 0, false, TierKept, false},
		{"hot 기간 안 달의 빈 인덱스도 둔다", "mem-t-2026.08", 0, false, TierKept, false},
		{"hot 기간이 지난 달의 빈 인덱스는 스냅샷 없이 지운다", "mem-t-2026.07", 0, false, TierDropped, true},
		{"미리보기는 지우지 않는다", "mem-t-2026.07", 0, true, TierWouldDrop, false},
		{"쓰기를 막는 사이 문서가 들어왔으면 지우지 않는다", "mem-t-2026.07", 1, false, TierKept, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := tierConfig(t)
			fake := cluster{index: c.index, count: c.count, snapshotState: "SUCCESS"}.fake()
			store := NewLocalStore(t.TempDir())

			decisions, err := Tier(context.Background(), fake, store, cfg, TierOptions{Now: tierNow, Team: "t", DryRun: c.dryRun})

			if err != nil || len(decisions) != 1 || decisions[0].Action != c.want || decisions[0].Snapshot != "" {
				t.Fatalf("decisions = %+v %v", decisions, err)
			}
			for _, call := range fake.calls {
				if call.Method == http.MethodPut && strings.HasPrefix(call.Path, "/_snapshot/") {
					t.Errorf("빈 인덱스를 스냅샷했다: %+v", call)
				}
			}
			if deleted := len(fake.called(http.MethodDelete, "/"+c.index)) > 0; deleted != c.deleted {
				t.Errorf("deleted = %v, want %v", deleted, c.deleted)
			}
			if !c.deleted && blocked(fake, c.index) {
				t.Errorf("지우지 않으면 쓰기 막기를 푼다 — 요청 = %+v", fake.calls)
			}
			if _, ok, _ := store.Get(context.Background(), ManifestKey("t", c.index)); ok {
				t.Error("빈 인덱스는 그 달을 cold 가 가졌다고 남기지 않는다")
			}
		})
	}
}

func TestTierKeepsIndexWhenSnapshotIsNotComplete(t *testing.T) {
	cfg := tierConfig(t)
	fake := cluster{index: "mem-t-2026.01", newest: "2026-01-10T00:00:00.000Z", snapshotState: "PARTIAL"}.fake()

	_, err := Tier(context.Background(), fake, NewLocalStore(t.TempDir()), cfg, TierOptions{Now: tierNow, Team: "t"})

	if err == nil || !strings.Contains(err.Error(), "PARTIAL") {
		t.Fatalf("완료되지 않은 스냅샷은 실패다: %v", err)
	}
	if len(fake.called(http.MethodDelete, "/mem-t-2026.01")) != 0 {
		t.Error("스냅샷이 완료되지 않으면 인덱스를 지우지 않는다")
	}
}
