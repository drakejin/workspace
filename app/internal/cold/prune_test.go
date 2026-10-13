package cold

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
)

// countingStore 는 키마다 Get 횟수를 센다 (S3 cold store 라면 GET 요청 수다).
type countingStore struct {
	Store
	mu   sync.Mutex
	gets map[string]int
}

func (s *countingStore) Get(ctx context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	s.gets[key]++
	s.mu.Unlock()
	return s.Store.Get(ctx, key)
}

// coldMonthFixture 는 팀 t 의 cold 로 간 달 months 개를 만든다 — 달마다 tier 스냅샷 하나와 문서 docsPerMonth 개
// (목록·raw·청크 JSONL, 그리고 내용이 같은 hot 원본). 스냅샷 목록을 돌려주는 OpenSearch 와 hot 원본 경로를 돌려준다.
func coldMonthFixture(t *testing.T, store Store, root string, months, docsPerMonth int) (*fakeOS, []string) {
	return monthFixture(t, store, root, months, docsPerMonth, false)
}

// ismMonthFixture 는 같은 달들을 ISM 이 옮긴 것이다 — 달마다 목록 없는 ism-cold 스냅샷 하나 (문서를 적재한 한참 뒤에 떴다).
func ismMonthFixture(t *testing.T, store Store, root string, months, docsPerMonth int) (*fakeOS, []string) {
	return monthFixture(t, store, root, months, docsPerMonth, true)
}

func monthFixture(t *testing.T, store Store, root string, months, docsPerMonth int, ism bool) (*fakeOS, []string) {
	t.Helper()
	ctx := context.Background()
	var snaps []any
	var hot []string
	for m := 1; m <= months; m++ {
		index := fmt.Sprintf("mem-t-2026.%02d", m)
		snap := "cold-" + index + "-20261001t000000-000000001"
		if ism {
			snap = fmt.Sprintf("ism-cold-2026.10.01-00:00:%02d.000", m)
		}
		snaps = append(snaps, map[string]any{"snapshot": snap, "indices": []string{index}, "state": snapshotSuccess, "start_time": "2026-10-01T00:00:00.000Z"})
		var entries []ManifestEntry
		for d := 0; d < docsPerMonth; d++ {
			rel := fmt.Sprintf("docs/specs/m%02d/d%d.md", m, d)
			body := fmt.Sprintf("# %s\n", rel)
			sha := document.SHA256(body)
			chunk := model.Chunk{ID: rel + "#0", Team: "t", Kind: model.KindEpisodic, SourcePath: rel, ContentSHA: sha,
				EventTime: fmt.Sprintf("2026-%02d-10T00:00:00Z", m), IngestedAt: fmt.Sprintf("2026-%02d-10T00:00:00Z", m)}
			mustDo(t, store.Put(ctx, RawKey("t", rel), body))
			mustDo(t, store.Put(ctx, ChunksKey("t", rel), EncodeChunks([]model.Chunk{chunk})))
			abs := filepath.Join(root, filepath.FromSlash(rel))
			mustDo(t, os.MkdirAll(filepath.Dir(abs), 0o755))
			mustDo(t, os.WriteFile(abs, []byte(body), 0o644))
			entries = append(entries, ManifestEntry{SourcePath: rel, ContentSHA: sha})
			hot = append(hot, rel)
		}
		if !ism {
			mustDo(t, AppendManifest(ctx, store, "t", index, snap, entries))
		}
	}
	fake := &fakeOS{route: func(method, path string, _ map[string]any) (any, error) {
		if method == http.MethodGet && path == "/_snapshot/mem-cold/_all" {
			return map[string]any{"snapshots": snaps}, nil
		}
		return nil, nil
	}}
	return fake, hot
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// OPS-12: tier 의 prune 은 cold 로 간 달마다 팀의 청크 JSONL 을 전부 다시 읽지 않는다 — 청크 JSONL 하나는 한 번만 읽는다
// (S3 cold store 라면 tier-tick 마다 "달 수 × 문서 수" 번의 GET 이었다). 치우는 원본은 그대로다.
func TestPruneReadsEachChunkFileOnce(t *testing.T) {
	cfg := tierConfig(t)
	store := &countingStore{Store: NewLocalStore(t.TempDir()), gets: map[string]int{}}
	fake, hot := coldMonthFixture(t, store, cfg.MetaRepoRoot, 4, 3)

	decisions, err := pruneArchived(context.Background(), fake, store, cfg, "t", map[string]bool{})

	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, d := range decisions {
		removed = append(removed, d.RemovedFiles...)
	}
	sort.Strings(removed)
	if strings.Join(removed, ",") != strings.Join(hot, ",") {
		t.Errorf("치운 원본 = %v, want %v", removed, hot)
	}
	assertChunkFilesReadOnce(t, store)
}

// OPS-12: ISM 이 옮긴 달(목록 없음)은 소유를 청크의 적재 시각으로 판단한다 — prune 이 이미 읽은 청크 JSONL 의 적재 시각을
// 쓰고, 문서마다 같은 청크 JSONL 을 다시 읽지 않는다.
func TestPruneOfISMMonthsReadsEachChunkFileOnce(t *testing.T) {
	cfg := tierConfig(t)
	store := &countingStore{Store: NewLocalStore(t.TempDir()), gets: map[string]int{}}
	fake, hot := ismMonthFixture(t, store, cfg.MetaRepoRoot, 2, 3)

	decisions, err := pruneArchived(context.Background(), fake, store, cfg, "t", map[string]bool{})

	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, d := range decisions {
		removed = append(removed, d.RemovedFiles...)
	}
	sort.Strings(removed)
	if strings.Join(removed, ",") != strings.Join(hot, ",") {
		t.Errorf("ISM 스냅샷 전에 적재된 원본을 치운다 = %v, want %v", removed, hot)
	}
	assertChunkFilesReadOnce(t, store)
}

func assertChunkFilesReadOnce(t *testing.T, store *countingStore) {
	t.Helper()
	for key, n := range store.gets {
		if strings.HasPrefix(key, ChunksPrefix("t")) && n > 1 {
			t.Errorf("%s 를 %d번 읽었다 (한 번이면 된다)", key, n)
		}
	}
}
