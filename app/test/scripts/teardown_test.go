package scripts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

const snapshotRepo = "mem-cold"

// fakeOpenSearch 는 teardown.sh 가 부르는 API 만 흉내 낸다 — 인덱스 목록·삭제(와일드카드 포함)와 스냅샷 목록·삭제.
type fakeOpenSearch struct {
	t                *testing.T
	mu               sync.Mutex
	indices          []string
	snapshots        map[string][]string // 스냅샷 이름 → 담은 인덱스
	deletedIndices   []string
	deletedSnapshots []string
}

func (f *fakeOpenSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/":
		writeJSON(w, map[string]any{"name": "fake"})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/_cat/indices/"):
		var rows []map[string]string
		for _, i := range f.match(strings.TrimPrefix(p, "/_cat/indices/")) {
			rows = append(rows, map[string]string{"index": i})
		}
		writeJSON(w, rows)
	case r.Method == http.MethodGet && p == "/_snapshot/"+snapshotRepo:
		writeJSON(w, map[string]any{snapshotRepo: map[string]any{"type": "fs"}})
	case r.Method == http.MethodGet && p == "/_snapshot/"+snapshotRepo+"/_all":
		writeJSON(w, map[string]any{"snapshots": f.snapshotList()})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/_snapshot/"+snapshotRepo+"/"):
		f.deletedSnapshots = append(f.deletedSnapshots, strings.TrimPrefix(p, "/_snapshot/"+snapshotRepo+"/"))
		writeJSON(w, map[string]any{"acknowledged": true})
	case r.Method == http.MethodDelete && !strings.HasPrefix(p, "/_"):
		f.deletedIndices = append(f.deletedIndices, f.match(strings.TrimPrefix(p, "/"))...)
		writeJSON(w, map[string]any{"acknowledged": true})
	default:
		f.t.Errorf("예상하지 못한 요청: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// match 는 쉼표로 이은 인덱스 식(와일드카드 가능)에 맞는 인덱스다.
func (f *fakeOpenSearch) match(expr string) []string {
	var out []string
	for _, idx := range f.indices {
		for _, pat := range strings.Split(expr, ",") {
			if ok, _ := path.Match(pat, idx); ok {
				out = append(out, idx)
				break
			}
		}
	}
	return out
}

func (f *fakeOpenSearch) snapshotList() []map[string]any {
	var out []map[string]any
	for name, indices := range f.snapshots {
		out = append(out, map[string]any{"snapshot": name, "indices": indices, "state": "SUCCESS"})
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// OPS-13: teardown <name> 은 정확히 자기 팀(scn-<name>)의 인덱스·스냅샷만 지운다 —
// 이름이 <name> 으로 시작하는 다른 시나리오(a 와 a-b)의 것은 남긴다.
func TestTeardownDeletesOnlyItsOwnTeam(t *testing.T) {
	cases := []struct {
		name          string
		wantIndices   []string
		wantSnapshots []string
	}{
		{"a", []string{"mem-scn-a-2026.09", "mem-scn-a-knowledge"}, []string{"cold-mem-scn-a-2026.09-1"}},
		{"a-b", []string{"mem-scn-a-b-2026.10", "mem-scn-a-b-knowledge"}, []string{"cold-mem-scn-a-b-2026.10-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeOpenSearch{t: t,
				indices: []string{
					"mem-scn-a-2026.09", "mem-scn-a-knowledge",
					"mem-scn-a-b-2026.10", "mem-scn-a-b-knowledge",
					"mem-scn-ab-2026.10", "mem-default-2026.10",
				},
				snapshots: map[string][]string{
					"cold-mem-scn-a-2026.09-1":   {"mem-scn-a-2026.09"},
					"cold-mem-scn-a-b-2026.10-1": {"mem-scn-a-b-2026.10"},
					"mixed-1":                    {"mem-scn-a-2026.09", "mem-default-2026.10"},
					"empty-1":                    {},
				},
			}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			sb := newSandbox(t, "curl", "jq")
			sb.copyTo(sb.dir, "scenarios/teardown.sh", "scripts/memory-common.sh")
			dockerLog := sb.stub("docker", "exit 0")
			r := sb.mustBash(map[string]string{"OPENSEARCH_URL": srv.URL, "COLD_SNAPSHOT_REPOSITORY": snapshotRepo},
				filepath.Join(sb.dir, "scenarios", "teardown.sh"), tc.name)

			fake.mu.Lock()
			defer fake.mu.Unlock()
			assertSet(t, "지운 인덱스", fake.deletedIndices, tc.wantIndices)
			assertSet(t, "지운 스냅샷", fake.deletedSnapshots, tc.wantSnapshots)
			if !strings.Contains(r.stdout, "정리됨") {
				t.Errorf("teardown 출력 = %q", r.output())
			}
			if got := calls(t, dockerLog); len(got) != 1 || !strings.HasPrefix(got[0], "ps") {
				t.Errorf("docker 는 ps 만 불러야 한다 (컨테이너가 없으면 그래프는 건너뛴다): %v", got)
			}
		})
	}
}

func assertSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	sort.Strings(g)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %v, want %v", what, g, w)
	}
}
