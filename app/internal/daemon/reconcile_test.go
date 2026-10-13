package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// cycleOS 는 사이클 하나가 부르는 OpenSearch 대신이다 — 인덱스 설정(_settings), 이 workspace 가 문서를 둔 팀(aggs),
// 인덱스별 청크 0(_search), 스냅샷 목록, _update_by_query 를 흉내 낸다. broken 에 든 인덱스를 담은 _search 는 503 처럼 실패하고,
// failUpdate 에 든 문서의 _update_by_query 도 실패한다.
type cycleOS struct {
	settings   map[string]map[string]any   // 인덱스 → flat 설정
	teams      []string                    // 이 workspace 가 문서를 둔 팀
	heads      map[string][]map[string]any // 인덱스 → 청크 0 의 _source
	broken     map[string]bool
	failUpdate map[string]bool

	mu      sync.Mutex
	updated []string // _update_by_query 를 받은 문서 (실패한 것 포함)
}

func decodeInto(v any, out any) error {
	if out == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (o *cycleOS) Do(_ context.Context, _, path string, body any, _ url.Values, out any) error {
	switch {
	case strings.HasSuffix(path, "/_settings"):
		resp := map[string]any{}
		for index, s := range o.settings {
			resp[index] = map[string]any{"settings": s}
		}
		return decodeInto(resp, out)
	case strings.HasPrefix(path, "/_snapshot/"):
		return decodeInto(map[string]any{"snapshots": []any{}}, out)
	case strings.HasSuffix(path, "/_update_by_query"):
		return o.update(body)
	case strings.HasSuffix(path, "/_search"):
		return o.search(path, body, out)
	}
	return decodeInto(map[string]any{}, out)
}

func (o *cycleOS) search(path string, body any, out any) error {
	if b, ok := body.(map[string]any); ok && b["aggs"] != nil {
		buckets := []any{}
		for _, team := range o.teams {
			buckets = append(buckets, map[string]any{"key": team})
		}
		return decodeInto(map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": buckets}}}, out)
	}
	indices := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_search"), ",")
	hits := []any{}
	for _, index := range indices {
		if o.broken[index] {
			return fmt.Errorf("OpenSearch POST %s 실패 (503): all shards failed", path)
		}
		for _, src := range o.heads[index] {
			hits = append(hits, map[string]any{"_index": index, "_source": src})
		}
	}
	return decodeInto(map[string]any{"hits": map[string]any{"hits": hits}}, out)
}

func (o *cycleOS) update(body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for path := range o.failUpdate {
		if strings.Contains(string(b), `"`+path+`"`) {
			o.record(path)
			return errors.New("일시적인 실패")
		}
	}
	var q struct {
		Query struct {
			Bool struct {
				Filter []map[string]map[string]string `json:"filter"`
			} `json:"bool"`
		} `json:"query"`
	}
	if err := json.Unmarshal(b, &q); err != nil {
		return err
	}
	for _, f := range q.Query.Bool.Filter {
		if p, ok := f["term"]["source_path"]; ok {
			o.record(p)
		}
	}
	return nil
}

func (o *cycleOS) record(path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.updated = append(o.updated, path)
}

func (*cycleOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// restoredSettings 는 restore_cold 로 복원한 인덱스의 설정이다 (데몬이 고치지 않는다).
var restoredSettings = map[string]any{opensearch.AutoManageSetting: "false"}

func headOf(path string, extra map[string]any) map[string]any {
	src := map[string]any{"source_path": path, "team": "t", "memory_kind": "episodic", "index_version": model.IndexVersion}
	for k, v := range extra {
		src[k] = v
	}
	return src
}

func newCycleReconciler(t *testing.T, fos *cycleOS) *Reconciler {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: "t", Reconcile: config.Reconcile{MaxFileBytes: 1 << 20}}
	return &Reconciler{OS: fos, Config: cfg, Judge: state.NewJudgeState(root), Log: t.Logf, coldOwned: map[string]statKey{}}
}

// IDX-9: 수렴은 문서 하나가 실패해도 나머지 문서를 마저 맞추고, 실패는 모아 돌려준다 — 예전에는 첫 실패에서 멈춰 그 뒤 문서는
// 다음 사이클까지 밀린 표시 없이 기본 검색에 보였고, 그 오류로 변경 없는 사이클이 판정 재시도·감사를 건너뛰었다.
func TestConvergeContinuesPastADocumentThatFails(t *testing.T) {
	const docs = 6
	fos := &cycleOS{
		settings:   map[string]map[string]any{"mem-t-2026.10": {}},
		teams:      []string{"t"},
		heads:      map[string][]map[string]any{},
		failUpdate: map[string]bool{"docs/specs/c/old-0.md": true, "docs/specs/c/old-3.md": true},
	}
	for i := 0; i < docs; i++ {
		oldPath, newPath := fmt.Sprintf("docs/specs/c/old-%d.md", i), fmt.Sprintf("docs/specs/c/new-%d.md", i)
		fos.heads["mem-t-2026.10"] = append(fos.heads["mem-t-2026.10"], headOf(oldPath, nil), headOf(newPath, map[string]any{"supersedes": oldPath}))
	}
	r := newCycleReconciler(t, fos)
	changed, err := r.Converge(context.Background())
	if len(fos.updated) != docs {
		t.Errorf("밀린 문서 %d개를 모두 맞추려 했어야 한다: %v", docs, fos.updated)
	}
	if changed != docs-len(fos.failUpdate) {
		t.Errorf("changed = %d, want %d", changed, docs-len(fos.failUpdate))
	}
	if err == nil || !strings.Contains(err.Error(), "old-0.md") || !strings.Contains(err.Error(), "old-3.md") {
		t.Errorf("실패는 모아 돌려준다: %v", err)
	}
}

// R2-handoff-4: 사이클은 이 workspace 팀의 인덱스만 읽는다 — 다른 workspace(병렬 e2e 의 팀 등)의 인덱스가 잠깐 열리지 않아도
// (샤드 503) 이 workspace 의 사이클이 실패하지 않는다. 이 workspace 가 복원한 인덱스는 그대로 본다.
func TestLoadStateReadsOnlyThisWorkspacesIndices(t *testing.T) {
	fos := &cycleOS{
		settings: map[string]map[string]any{
			"mem-t-2026.10": {}, "mem-t-2026.06": restoredSettings,
			"mem-other-2026.10": {}, "mem-other-2026.06": restoredSettings,
		},
		teams: []string{"t"},
		heads: map[string][]map[string]any{
			"mem-t-2026.10": {headOf("docs/specs/a/hot.md", nil)},
			"mem-t-2026.06": {headOf("docs/specs/a/restored.md", nil)},
		},
		broken: map[string]bool{"mem-other-2026.10": true, "mem-other-2026.06": true},
	}
	st, err := newCycleReconciler(t, fos).loadState(context.Background())
	if err != nil {
		t.Fatalf("다른 workspace 의 인덱스 때문에 실패했다: %v", err)
	}
	if _, ok := st.existing["docs/specs/a/hot.md"]; !ok || len(st.existing) != 1 {
		t.Errorf("existing = %v", st.existing)
	}
	if _, ok := st.restored["docs/specs/a/restored.md"]; !ok || len(st.restored) != 1 {
		t.Errorf("restored = %v (이 workspace 가 복원한 인덱스는 본다)", st.restored)
	}
	if strings.Join(st.indices, ",") != "mem-t-2026.10" {
		t.Errorf("indices = %v", st.indices)
	}
}

// IDX-12: syncFile 은 인자로 받은 색인 상태(existing)를 고치지 않는다 — 예전에는 본 파일을 existing 에서 지워(delete) 그 "남은 것"이
// tombstone 의 입력이 되는 숨은 결합이 있었고, 게이트에 걸린 파일의 처리(IDX-6)가 거기서 어긋났다.
func TestSyncFileLeavesTheIndexedDocsAlone(t *testing.T) {
	const rel = "docs/specs/a/same.md"
	r := newCycleReconciler(t, &cycleOS{})
	abs := filepath.Join(r.Config.MetaRepoRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# 같다\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := ingest.ScannedFile{Rel: rel, Abs: abs, MtimeMs: 1, Size: 7}
	existing := map[string]ingest.IndexedDoc{rel: {SourcePath: rel, Team: "t", MtimeMs: 1, Size: 7, IndexVersion: model.IndexVersion}}
	var sum state.CycleSummary
	if _, err := r.syncFile(context.Background(), f, cycleState{existing: existing}, &sum); err != nil {
		t.Fatal(err)
	}
	if _, ok := existing[rel]; !ok {
		t.Error("syncFile 이 existing 에서 문서를 지웠다")
	}
}
