package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// handoffOS 는 색인에 hits 가 있는 OpenSearch 대신이다 — _search 는 hits 를, 나머지는 빈 응답을 돌려주고,
// 받은 요청(경로와 본문)과 _bulk 횟수를 모은다.
type handoffOS struct {
	hits []map[string]any

	mu    sync.Mutex
	reqs  []string
	bulks int
}

func (o *handoffOS) Do(_ context.Context, _, path string, body any, _ url.Values, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.reqs = append(o.reqs, path+" "+string(b))
	o.mu.Unlock()
	if out == nil {
		return nil
	}
	resp := []byte(`{}`)
	if strings.HasSuffix(path, "/_search") {
		if resp, err = json.Marshal(map[string]any{"hits": map[string]any{"hits": o.hits}}); err != nil {
			return err
		}
	}
	return json.Unmarshal(resp, out)
}

func (o *handoffOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bulks++
	return opensearch.BulkResponse{}, nil
}

// cleared 는 넘김 표시(pending_cycle)를 지우는 _update_by_query 를 보냈는가다.
func (o *handoffOS) cleared() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, r := range o.reqs {
		if strings.Contains(r, "/_update_by_query") && strings.Contains(r, "pending_cycle") {
			return true
		}
	}
	return false
}

// RUN-3: 데몬 밖(ingest_path·CLI ingest·audit -apply)에서 쓴 문서(청크 0 의 pending_cycle)는 색인이 최신이어도 변경분으로
// 넘긴다 — 판정·증류·그래프가 그 문서를 보게. 내용이 같으면 다시 쓰지 않고 표시만 지운다. 표시가 없으면 예전처럼 건너뛴다.
func TestSyncFileHandsOverADocumentWrittenOutsideTheDaemon(t *testing.T) {
	const rel = "docs/feature/ship-a/v2.md"
	content := "---\ndate: 2026-10-01\n---\n# 배송비\nHandoffShipFee 는 3500원이다.\n"
	root := t.TempDir()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	f := ingest.ScannedFile{Rel: rel, Abs: abs, MtimeMs: info.ModTime().UnixMilli(), Size: info.Size()}
	head := func(index string, pending bool) map[string]any {
		src := map[string]any{"source_path": rel, "team": "t", "memory_kind": "episodic", "content_sha": document.SHA256(content),
			"mtime_ms": f.MtimeMs, "size": f.Size, "index_version": model.IndexVersion}
		if pending {
			src["pending_cycle"] = true
		}
		return map[string]any{"_index": index, "_source": src}
	}
	cases := []struct {
		name               string
		hits               []map[string]any
		handed, rewritten  bool
		clearedWithoutCopy bool
	}{
		{"표시 없음 — 건너뛴다", []map[string]any{head("mem-t-2026.10", false)}, false, false, false},
		{"표시 있음 — 다시 쓰지 않고 넘긴 뒤 표시를 지운다", []map[string]any{head("mem-t-2026.10", true)}, true, false, true},
		{"표시 있고 두 인덱스에 걸쳐 있음 — 다시 쓰면서 넘긴다", []map[string]any{head("mem-t-2026.09", true), head("mem-t-2026.10", true)}, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			fos := &handoffOS{hits: c.hits}
			cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: "t", Reconcile: config.Reconcile{MaxFileBytes: 1 << 20}}
			r := &Reconciler{OS: fos, Config: cfg, Log: t.Logf, coldOwned: map[string]statKey{}}
			r.Indexer = ingest.New(ingest.Deps{OS: fos, Cold: cold.NewLocalStore(t.TempDir()), Config: cfg, SupersededBy: r.SupersededBy})
			indices := []string{"mem-t-2026.09", "mem-t-2026.10"}
			existing, err := ingest.FetchIndexedDocs(ctx, fos, indices, "ws")
			if err != nil {
				t.Fatal(err)
			}
			var sum state.CycleSummary
			res, err := r.syncFile(ctx, f, cycleState{indices: indices, existing: existing}, &sum)
			if err != nil {
				t.Fatal(err)
			}
			entry := res.entry
			if (entry != nil) != c.handed {
				t.Fatalf("diff = %+v, want handed=%v", entry, c.handed)
			}
			if entry != nil && (entry.SourcePath != rel || entry.Change != ChangeModified || entry.Team != "t" || !strings.Contains(entry.Body, "HandoffShipFee")) {
				t.Errorf("diff = %+v", entry)
			}
			if got := fos.bulks > 0; got != c.rewritten {
				t.Errorf("다시 썼나 = %v, want %v", got, c.rewritten)
			}
			if got := fos.cleared(); got != c.clearedWithoutCopy {
				t.Errorf("표시만 지웠나 = %v, want %v (%v)", got, c.clearedWithoutCopy, fos.reqs)
			}
		})
	}
}
