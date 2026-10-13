package ingest

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// recordOS 는 OpenSearch 대신이다 — 청크 0 조회(_search)에는 indexed 를 hit 으로 돌려주고(nil 이면 색인에 없다),
// _bulk 로 쓴 청크 줄은 JSON 모양 그대로 모은다.
type recordOS struct {
	indexed map[string]any

	mu     sync.Mutex
	chunks []map[string]any
}

func (r *recordOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	if out == nil {
		return nil
	}
	hits := []any{}
	if r.indexed != nil && strings.HasSuffix(path, "/_search") {
		hits = append(hits, map[string]any{"_index": "mem-unit-knowledge", "_source": r.indexed})
	}
	resp, err := json.Marshal(map[string]any{"hits": map[string]any{"hits": hits}})
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, out)
}

func (r *recordOS) Bulk(_ context.Context, lines []any, _ url.Values) (opensearch.BulkResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return opensearch.BulkResponse{}, err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return opensearch.BulkResponse{}, err
		}
		if _, action := m["index"]; !action {
			r.chunks = append(r.chunks, m)
		}
	}
	return opensearch.BulkResponse{}, nil
}

// head 는 rel 의 청크 0 가운데 마지막으로 쓴 것이다 — Write 의 마지막 단계라 데몬이 읽는 표시다.
func (r *recordOS) head(t *testing.T, rel string) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var last map[string]any
	for _, c := range r.chunks {
		if c["source_path"] == rel && c["chunk_index"] == float64(0) {
			last = c
		}
	}
	if last == nil {
		t.Fatalf("%s 의 청크 0 을 쓰지 않았다: %v", rel, r.chunks)
	}
	return last
}

func unitDeps(root string, os opensearch.Client, store cold.Store) Deps {
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: unitTeam, Reconcile: config.Reconcile{MaxFileBytes: unitMaxFileBytes}}
	return Deps{OS: os, Cold: store, Config: cfg}
}

// RUN-3: 데몬 밖(ingest_path·CLI ingest·audit -apply)에서 쓴 문서는 청크 0 에 넘김 표시(pending_cycle)를 남긴다 — 데몬은 색인이
// 이미 최신인 문서를 건너뛰므로, 표시가 없으면 그 문서는 판정·증류·그래프를 영영 거치지 않는다. 데몬의 쓰기와, 데몬이 이미
// 넘겨받은 같은 내용의 재적재는 표시를 남기지 않는다. 표시는 색인의 청크 0 에만 있고 cold store 청크에는 없다.
func TestWriteOutsideTheDaemonMarksChunkZeroForTheNextCycle(t *testing.T) {
	const rel = "docs/knowledge/ship.md"
	content := "# 배송\nShipToken"
	sha := document.SHA256(content)
	cases := []struct {
		name    string
		daemon  bool
		indexed map[string]any
		want    bool
	}{
		{"처음 보는 문서", false, nil, true},
		{"내용이 바뀐 문서", false, map[string]any{"source_path": rel, "content_sha": "old"}, true},
		{"아직 넘기지 못한 같은 내용", false, map[string]any{"source_path": rel, "content_sha": sha, "pending_cycle": true}, true},
		{"데몬이 이미 넘겨받은 같은 내용", false, map[string]any{"source_path": rel, "content_sha": sha}, false},
		{"데몬의 쓰기", true, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			writeFile(t, root, rel, []byte(content))
			os := &recordOS{indexed: c.indexed}
			store := cold.NewLocalStore(t.TempDir())
			deps := unitDeps(root, os, store)
			if c.daemon {
				deps.SupersededBy = func(string) string { return "" }
			}
			ix := New(deps)
			p, err := ix.Prepare(ctx, rel, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ix.Write(ctx, p); err != nil {
				t.Fatal(err)
			}
			if got := os.head(t, rel)["pending_cycle"] == true; got != c.want {
				t.Errorf("pending_cycle = %v, want %v (청크 0 = %v)", got, c.want, os.head(t, rel))
			}
			raw, ok, err := store.Get(ctx, cold.ChunksKey(unitTeam, rel))
			if err != nil || !ok || strings.Contains(raw, "pending_cycle") {
				t.Errorf("cold store 청크에는 넘김 표시가 없다: %q %v %v", raw, ok, err)
			}
		})
	}
}

// RUN-6·IDX-1: 빈 파일·frontmatter 만 있는 문서도 색인에 청크 0 을 쓴다 — 데몬의 "최신" 표시(content_sha·mtime·size)가
// 있어야 다음 사이클이 건너뛰고, 지우면 tombstone 이 cold store 청크까지 치운다.
func TestIngestPathWritesChunkZeroOfABlankDocument(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	blank := []string{"docs/knowledge/empty.md", "docs/knowledge/fm-only.md"}
	writeFile(t, root, blank[0], nil)
	writeFile(t, root, blank[1], []byte("---\ndoc_type: knowledge\n---\n"))
	os := &recordOS{}
	store := cold.NewLocalStore(t.TempDir())
	results, err := New(unitDeps(root, os, store)).IngestPath(ctx, "docs/knowledge", "")
	if err != nil || len(results) != 2 {
		t.Fatalf("results = %+v %v", results, err)
	}
	for _, r := range results {
		if r.Chunks != 1 {
			t.Errorf("%s: chunks = %d — 청크 0 하나", r.SourcePath, r.Chunks)
		}
	}
	for _, rel := range blank {
		head := os.head(t, rel)
		if sha, _ := head["content_sha"].(string); sha == "" || head["mtime_ms"] == float64(0) || head["text"] != "" {
			t.Errorf("%s 청크 0 = %v", rel, head)
		}
		if _, ok, err := cold.Archived(ctx, store, unitTeam, rel); err != nil || !ok {
			t.Errorf("%s 의 cold store 청크 = %v %v", rel, ok, err)
		}
	}
}
