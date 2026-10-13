package audit

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
)

// mapStore 는 대소문자·유니코드 정규화를 구분하는 보관소다 (S3 처럼 철자가 다르면 다른 키).
type mapStore struct {
	mu   sync.Mutex
	keys map[string]string
}

func newMapStore() *mapStore { return &mapStore{keys: map[string]string{}} }

func (s *mapStore) Location() string { return "s3://unit/ws" }

func (s *mapStore) Put(_ context.Context, key, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key] = body
	return nil
}

func (s *mapStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.keys[key]
	return body, ok, nil
}

func (s *mapStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *mapStore) Remove(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
	return nil
}

func putChunks(t *testing.T, store cold.Store, rel string, c model.Chunk) {
	t.Helper()
	c.Team, c.SourcePath, c.Kind = unitTeam, rel, model.KindEpisodic
	if err := store.Put(context.Background(), cold.ChunksKey(unitTeam, rel), cold.EncodeChunks([]model.Chunk{c})); err != nil {
		t.Fatal(err)
	}
}

func kindsOf(findings []Finding) map[string]Kind {
	out := map[string]Kind{}
	for _, f := range findings {
		out[f.Path] = f.Kind
	}
	return out
}

// R1-paths-4: S3 cold store 는 철자가 다르면 다른 키다. 옛 철자(대소문자·NFD)로 남은 청크 JSONL 은 같은 파일의 지금 철자 키가
// 따로 있으면 쓰레기다 — audit -apply 가 지운다 (그대로 두면 reload 가 옛 철자 문서를 다시 싣는다). 지금 철자 키는 남긴다.
func TestOrphanChunksDeletesALegacySpellingKeyNextToTheCanonicalOne(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "docs/knowledge/status-codes-v2.md", "# 상태 코드\n")
	store := newMapStore()
	putChunks(t, store, "docs/knowledge/status-codes-v2.md", model.Chunk{ContentSHA: "now"})
	putChunks(t, store, "docs/knowledge/STATUS-CODES-V2.md", model.Chunk{ContentSHA: "old"}) // 옛 철자
	r := newUnitRunner(t, root, store, map[string]ingest.IndexedDoc{})
	r.apply, r.owner = true, cold.NewOwnership(cold.Archive{}, store)

	if err := r.orphanChunks(ctx); err != nil {
		t.Fatal(err)
	}

	if got := kindsOf(r.findings); got["docs/knowledge/STATUS-CODES-V2.md"] != OrphanChunks || len(got) != 1 {
		t.Errorf("findings = %+v", r.findings)
	}
	if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, "docs/knowledge/STATUS-CODES-V2.md")); ok {
		t.Error("옛 철자 키를 지운다")
	}
	if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, "docs/knowledge/status-codes-v2.md")); !ok {
		t.Error("지금 철자 키는 남긴다")
	}
}

// 로컬 cold store(APFS)에서는 두 철자가 같은 파일이다 — 옛 철자로만 보이는 청크 파일은 지금 문서의 것이라 지우지 않는다.
func TestOrphanChunksKeepsTheOnlySpellingOnACaseInsensitiveStore(t *testing.T) {
	ctx := context.Background()
	root, coldDir := t.TempDir(), t.TempDir()
	requireCaseInsensitiveFS(t, root)
	requireCaseInsensitiveFS(t, coldDir)
	writeFile(t, root, "docs/knowledge/status-codes-v2.md", "# 상태 코드\n")
	store := cold.NewLocalStore(coldDir)
	putChunks(t, store, "docs/knowledge/STATUS-CODES-V2.md", model.Chunk{ContentSHA: "now"}) // APFS 는 처음 만든 이름을 지킨다
	r := newUnitRunner(t, root, store, map[string]ingest.IndexedDoc{})
	r.apply, r.owner = true, cold.NewOwnership(cold.Archive{}, store)

	if err := r.orphanChunks(ctx); err != nil {
		t.Fatal(err)
	}

	if len(r.findings) != 0 {
		t.Errorf("findings = %+v", r.findings)
	}
	if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, "docs/knowledge/status-codes-v2.md")); !ok {
		t.Error("지금 문서의 청크 파일을 지웠다")
	}
}

// R1-cold-1 · R1-cold-4: cold 로 간 달의 문서인데 어느 스냅샷에도 없는 것(지워진 스냅샷에만 목록 줄이 남았다, ISM 스냅샷 뒤에
// 적재됐다)은 cold store 사본이 유일하다. 감사는 그것을 stranded-cold 로 알리고 -apply 로도 지우지 않는다.
func TestOrphanChunksReportsStrandedColdDocumentsWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newMapStore()
	const (
		month = "mem-unit-2026.05"
		gone  = "cold-mem-unit-2026.05-20260801t000000-000000001"
		live  = "cold-mem-unit-2026.05-20260901t000000-000000002"
		ism   = "mem-unit-2026.04"
	)
	may := model.Chunk{ContentSHA: "j1", EventTime: "2026-05-10T00:00:00Z", IngestedAt: "2026-05-10T00:00:00Z"}
	putChunks(t, store, "docs/specs/j.md", may)
	if err := cold.AppendManifest(ctx, store, unitTeam, month, gone, []cold.ManifestEntry{{SourcePath: "docs/specs/j.md", ContentSHA: "j1"}}); err != nil {
		t.Fatal(err)
	}
	late := model.Chunk{ContentSHA: "l1", EventTime: "2026-04-10T00:00:00Z", IngestedAt: "2026-08-01T00:00:00Z"} // ISM 스냅샷 뒤
	putChunks(t, store, "docs/specs/late.md", late)
	orphan := model.Chunk{ContentSHA: "o1", EventTime: "2026-10-01T00:00:00Z"} // cold 로 간 적 없는 달
	putChunks(t, store, "docs/specs/orphan.md", orphan)
	archive := cold.Archive{
		month: {{Name: live, Indices: []string{month}, State: "SUCCESS", StartTime: "2026-09-01T00:00:00.000Z"}},
		ism:   {{Name: "ism-cold-2026.06.30", Indices: []string{ism}, State: "SUCCESS", StartTime: "2026-06-30T00:00:00.000Z"}},
	}
	r := newUnitRunner(t, root, store, map[string]ingest.IndexedDoc{})
	r.apply, r.owner = true, cold.NewOwnership(archive, store)

	if err := r.orphanChunks(ctx); err != nil {
		t.Fatal(err)
	}

	want := map[string]Kind{"docs/specs/j.md": StrandedCold, "docs/specs/late.md": StrandedCold, "docs/specs/orphan.md": OrphanChunks}
	if got := kindsOf(r.findings); len(got) != len(want) || got["docs/specs/j.md"] != want["docs/specs/j.md"] ||
		got["docs/specs/late.md"] != want["docs/specs/late.md"] || got["docs/specs/orphan.md"] != want["docs/specs/orphan.md"] {
		t.Errorf("findings = %+v", r.findings)
	}
	for _, rel := range []string{"docs/specs/j.md", "docs/specs/late.md"} {
		if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, rel)); !ok {
			t.Errorf("%s: 유일한 사본을 지웠다", rel)
		}
	}
	for _, f := range r.findings {
		if f.Kind == StrandedCold && (f.Fixed || !strings.Contains(f.Detail, cold.RawKey(unitTeam, f.Path))) {
			t.Errorf("stranded-cold 는 보고만 하고 되살리는 길(raw 위치)을 알린다: %+v", f)
		}
	}
}
