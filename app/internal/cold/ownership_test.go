package cold

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/model"
)

// snapTime 은 archiveOf 가 만드는 스냅샷의 시작 시각이다. 그 전에 적재된 cold store 청크(beforeSnap)는 ISM 스냅샷에 들어 있다.
const (
	snapTime   = "2026-09-01T00:00:00.000Z"
	beforeSnap = "2026-06-01T00:00:00Z"
)

// archiveOf 는 인덱스별 스냅샷 이름으로 Archive 를 만든다 (모두 snapTime 에 떴다).
func archiveOf(names map[string][]string) Archive {
	out := Archive{}
	for index, snaps := range names {
		for _, name := range snaps {
			out[index] = append(out[index], Snapshot{Name: name, Indices: []string{index}, State: snapshotSuccess, StartTime: snapTime})
		}
	}
	return out
}

func TestOwnershipUsesTierManifestAndFallsBackForISM(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	// tier 가 스냅샷에 넣은 문서 목록을 남긴 달 — 스냅샷 둘 다 목록이 있다
	if err := AppendManifest(ctx, store, "t", "mem-t-2026.05", "snap-1", []ManifestEntry{{SourcePath: "docs/a.md", ContentSHA: "a1"}}); err != nil {
		t.Fatal(err)
	}
	if err := AppendManifest(ctx, store, "t", "mem-t-2026.05", "snap-2", []ManifestEntry{{SourcePath: "docs/a.md", ContentSHA: "a2"}}); err != nil {
		t.Fatal(err)
	}
	// ISM 이 먼저 스냅샷을 뜨고(목록 없음) 나중에 tier 가 같은 달을 다시 뜬 경우
	if err := AppendManifest(ctx, store, "t", "mem-t-2026.03", "snap-tier", []ManifestEntry{{SourcePath: "docs/k.md", ContentSHA: "k1"}}); err != nil {
		t.Fatal(err)
	}
	archive := archiveOf(map[string][]string{
		"mem-t-2026.05": {"snap-1", "snap-2"},
		"mem-t-2026.04": {"ism-cold-1"},
		"mem-t-2026.03": {"ism-cold-0", "snap-tier"},
	})
	// ISM 스냅샷 전에 적재된 문서의 cold store 사본
	putChunk(t, store, "t", "docs/c.md", "c1", beforeSnap)
	putChunk(t, store, "t", "docs/old.md", "o1", beforeSnap)
	own := NewOwnership(archive, store)

	cases := []struct {
		index, path, sha string
		want             bool
	}{
		{"mem-t-2026.05", "docs/a.md", "a1", true},   // 첫 스냅샷의 내용
		{"mem-t-2026.05", "docs/a.md", "a2", true},   // 다시 tier 한 내용
		{"mem-t-2026.05", "docs/a.md", "a3", false},  // 스냅샷 뒤에 쓴 내용 — cold 에 없다
		{"mem-t-2026.05", "docs/b.md", "b1", false},  // 목록에 없는 문서
		{"mem-t-2026.04", "docs/c.md", "c1", true},   // ISM 이 만든 스냅샷 — 목록이 없으면 스냅샷 전에 적재된 내용을 가졌다
		{"mem-t-2026.03", "docs/old.md", "o1", true}, // 목록이 덮지 못하는 ISM 스냅샷이 있으면 적재 시각으로 본다
		{"mem-t-2026.06", "docs/d.md", "d1", false},  // cold 로 간 적 없는 달
	}
	for _, c := range cases {
		got, err := own.Owns(ctx, "t", c.index, c.path, c.sha)
		if err != nil || got != c.want {
			t.Errorf("Owns(%s, %s, %s) = %v %v, want %v", c.index, c.path, c.sha, got, err, c.want)
		}
	}

	// purge 는 그 문서를 목록에서 뺀다
	if err := DropFromManifest(ctx, store, "t", "mem-t-2026.05", "docs/a.md"); err != nil {
		t.Fatal(err)
	}
	if got, _ := NewOwnership(archive, store).Owns(ctx, "t", "mem-t-2026.05", "docs/a.md", "a1"); got {
		t.Error("purge 뒤에는 소유하지 않는다")
	}
}

func TestOwnershipNeverReadsATierSnapshotAsISM(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	// 문서가 하나도 없던 스냅샷도 목록에 표시 줄을 남긴다 — 줄이 없으면 ISM 스냅샷으로 읽혀 그 달 전체를 가진 것이 된다
	if err := AppendManifest(ctx, store, "t", "mem-t-2026.10", "snap-empty", nil); err != nil {
		t.Fatal(err)
	}
	archive := archiveOf(map[string][]string{
		"mem-t-2026.10": {"snap-empty"},
		// 목록을 쓰기 전에 끊긴 tier 스냅샷 — tier·archive 가 뜬 스냅샷은 이름(cold-)으로 안다
		"mem-t-2026.09": {"cold-mem-t-2026.09-20261013t000000-000000001"},
		"mem-t-2026.08": {"ism-cold-2026.10.13-00:00:00.000"},
	})
	putChunk(t, store, "t", "docs/today.md", "s1", beforeSnap)
	own := NewOwnership(archive, store)

	cases := []struct {
		index string
		want  bool
	}{
		{"mem-t-2026.10", false}, // 빈 tier 스냅샷은 아무것도 갖지 않는다
		{"mem-t-2026.09", false}, // 목록 없는 tier 스냅샷도 마찬가지
		{"mem-t-2026.08", true},  // ISM 스냅샷만 목록 없이 (적재 시각으로) 본다
	}
	for _, c := range cases {
		got, err := own.Owns(ctx, "t", c.index, "docs/today.md", "s1")
		if err != nil || got != c.want {
			t.Errorf("Owns(%s) = %v %v, want %v", c.index, got, err, c.want)
		}
	}
}

func TestAppendManifestKeepsEveryLineUnderConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const writers = 32
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// tier-tick 과 archive 는 다른 프로세스다 — 보관소를 저마다 연다
			entry := ManifestEntry{SourcePath: fmt.Sprintf("docs/%02d.md", i), ContentSHA: "x"}
			errs <- AppendManifest(ctx, NewLocalStore(root), "t", "mem-t-2026.05", fmt.Sprintf("snap-%02d", i), []ManifestEntry{entry})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	body, _, err := NewLocalStore(root).Get(ctx, ManifestKey("t", "mem-t-2026.05"))
	if got := len(decodeManifest(body)); err != nil || got != writers {
		t.Errorf("목록 줄 %d개, want %d (%v)", got, writers, err)
	}
}

func TestDropSnapshotsFromManifestKeepsTheOthers(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	for snap, entries := range map[string][]ManifestEntry{
		"snap-1": {{SourcePath: "docs/a.md", ContentSHA: "a1"}},
		"snap-2": {{SourcePath: "docs/b.md", ContentSHA: "b1"}},
		"snap-3": nil,
	} {
		if err := AppendManifest(ctx, store, "t", "mem-t-2026.05", snap, entries); err != nil {
			t.Fatal(err)
		}
	}

	if err := DropSnapshotsFromManifest(ctx, store, "t", "mem-t-2026.05", []string{"snap-1", "snap-3"}); err != nil {
		t.Fatal(err)
	}

	body, _, _ := store.Get(ctx, ManifestKey("t", "mem-t-2026.05"))
	if got := decodeManifest(body); len(got) != 1 || got[0] != (ManifestEntry{SourcePath: "docs/b.md", ContentSHA: "b1", Snapshot: "snap-2"}) {
		t.Errorf("남은 목록 = %+v", got)
	}
}

// R1-cold-1: 고치기 전의 archive 가 옛 스냅샷을 지우며 문서를 잃었어도 목록에는 지워진 스냅샷의 줄이 남는다. 그 줄은 문서를
// cold 소유로 붙잡지 않는다 (데몬이 hot 파일을 다시 색인하고, tier 가 hot 원본을 지우지 않게).
func TestOwnershipIgnoresLinesOfDeletedSnapshots(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	const (
		index = "mem-t-2026.05"
		gone  = "cold-mem-t-2026.05-20260801t000000-000000001" // 예전 archive 가 지운 스냅샷
		live  = "cold-mem-t-2026.05-20260901t000000-000000002" // 다시 쓴 스냅샷 — j.md 가 빠졌다
	)
	if err := AppendManifest(ctx, store, "t", index, gone, []ManifestEntry{{SourcePath: "docs/j.md", ContentSHA: "j1"}, {SourcePath: "docs/b.md", ContentSHA: "b1"}}); err != nil {
		t.Fatal(err)
	}
	if err := AppendManifest(ctx, store, "t", index, live, []ManifestEntry{{SourcePath: "docs/b.md", ContentSHA: "b2"}}); err != nil {
		t.Fatal(err)
	}
	own := NewOwnership(archiveOf(map[string][]string{index: {live}}), store)

	cases := []struct {
		path, sha string
		want      bool
	}{
		{"docs/j.md", "j1", false}, // 지워진 스냅샷에만 있던 문서 — 어느 스냅샷에도 없다
		{"docs/b.md", "b1", false}, // 지워진 스냅샷의 옛 내용
		{"docs/b.md", "b2", true},  // 지금 있는 스냅샷의 내용
	}
	for _, c := range cases {
		got, err := own.Owns(ctx, "t", index, c.path, c.sha)
		if err != nil || got != c.want {
			t.Errorf("Owns(%s, %s) = %v %v, want %v", c.path, c.sha, got, err, c.want)
		}
	}
}

// putChunk 는 cold store 에 문서 하나의 청크 JSONL 을 둔다 (적재가 hot 문서마다 남기는 사본).
func putChunk(t *testing.T, store Store, team, path, sha, ingestedAt string) {
	t.Helper()
	chunk := model.Chunk{ID: path + "#0", Team: team, SourcePath: path, ContentSHA: sha, IngestedAt: ingestedAt}
	if err := store.Put(context.Background(), ChunksKey(team, path), EncodeChunks([]model.Chunk{chunk})); err != nil {
		t.Fatal(err)
	}
}

// verdicts 는 Owns·Stranded 를 한 번에 본다.
func verdicts(t *testing.T, own *Ownership, index, path, sha string) [2]bool {
	t.Helper()
	ctx := context.Background()
	owned, err1 := own.Owns(ctx, "t", index, path, sha)
	stranded, err2 := own.Stranded(ctx, "t", index, path, sha)
	if err := errors.Join(err1, err2); err != nil {
		t.Fatal(err)
	}
	return [2]bool{owned, stranded}
}

// R1-cold-1: 지워진 스냅샷에만 남은 문서는 cold 소유가 아니지만 "갇힌" 문서로 알린다 — 감사가 청크를 지우지 않고,
// reload 는 hot 파일이 없으면 싣지 않는다 (Reloadable).
func TestOwnershipReportsDocumentsStrandedByADeletedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	const (
		index = "mem-t-2026.05"
		gone  = "cold-mem-t-2026.05-20260801t000000-000000001"
		live  = "cold-mem-t-2026.05-20260901t000000-000000002"
	)
	if err := AppendManifest(ctx, store, "t", index, gone, []ManifestEntry{{SourcePath: "docs/j.md", ContentSHA: "j1"}}); err != nil {
		t.Fatal(err)
	}
	if err := AppendManifest(ctx, store, "t", index, live, []ManifestEntry{{SourcePath: "docs/b.md", ContentSHA: "b2"}}); err != nil {
		t.Fatal(err)
	}
	// 스냅샷이 하나도 남지 않은 달 — 목록 줄만 남았다 (스냅샷을 손으로 지웠거나 repository 를 바꿨다)
	if err := AppendManifest(ctx, store, "t", "mem-t-2026.04", "cold-mem-t-2026.04-x", []ManifestEntry{{SourcePath: "docs/k.md", ContentSHA: "k1"}}); err != nil {
		t.Fatal(err)
	}
	own := NewOwnership(archiveOf(map[string][]string{index: {live}}), store)

	cases := []struct {
		index, path, sha string
		want             [2]bool // owned, stranded
	}{
		{"mem-t-2026.04", "docs/k.md", "k1", [2]bool{false, true}},  // 그 달의 스냅샷이 모두 없다
		{index, "docs/j.md", "j1", [2]bool{false, true}},            // 지워진 스냅샷에만 있다
		{index, "docs/b.md", "b2", [2]bool{true, false}},            // 지금 있는 스냅샷에 있다
		{index, "docs/new.md", "n1", [2]bool{false, false}},         // 목록에 없는 새 문서
		{"mem-t-2026.06", "docs/j.md", "j1", [2]bool{false, false}}, // cold 로 간 적 없는 달
	}
	for _, c := range cases {
		if got := verdicts(t, own, c.index, c.path, c.sha); got != c.want {
			t.Errorf("%s %s: owned/stranded = %v, want %v", c.index, c.path, got, c.want)
		}
	}
}

// R1-cold-4: ISM 스냅샷(목록 없음)은 그 스냅샷이 떠지기 전에 적재된 내용만 가진 것으로 본다. 스냅샷 뒤 그 달로 늦게 쓴 문서는
// 스냅샷에 없다 — hot 재구축 뒤 reload 가 건너뛰고 tier 가 hot 원본을 지우면 어디에도 없게 된다.
func TestOwnershipOfAnISMMonthFollowsIngestTime(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	const index = "mem-t-2026.04"
	archive := Archive{index: {{Name: "ism-cold-2026.06.30-00:00:00.000", Indices: []string{index}, State: snapshotSuccess, StartTime: "2026-06-30T00:00:00.000Z"}}}
	putChunk(t, store, "t", "docs/early.md", "e1", "2026-04-10T09:00:00Z")
	putChunk(t, store, "t", "docs/late.md", "l1", "2026-08-01T09:00:00Z")
	putChunk(t, store, "t", "docs/other.md", "o2", "2026-04-10T09:00:00Z") // cold store 사본은 다른 내용
	putChunk(t, store, "t", "docs/edge.md", "g1", "2026-06-29T23:59:59Z")  // 스냅샷 1초 전 — 호스트와 OpenSearch 의 시계가 다를 수 있다
	own := NewOwnership(archive, store)

	cases := []struct {
		path, sha string
		want      [2]bool // owned, stranded
	}{
		{"docs/early.md", "e1", [2]bool{true, false}},   // 스냅샷 전에 적재 — 스냅샷에 있다
		{"docs/late.md", "l1", [2]bool{false, true}},    // 스냅샷 뒤에 적재 — 스냅샷에 없다
		{"docs/other.md", "o1", [2]bool{false, true}},   // 이 내용의 적재 시각을 모른다
		{"docs/nochunk.md", "x1", [2]bool{false, true}}, // cold store 사본이 없다
		{"docs/edge.md", "g1", [2]bool{false, true}},    // 시계 차이 안 — 스냅샷에 있다고 단정하지 않는다
	}
	for _, c := range cases {
		if got := verdicts(t, own, index, c.path, c.sha); got != c.want {
			t.Errorf("%s: owned/stranded = %v, want %v", c.path, got, c.want)
		}
	}
}

// R1-cold-1 · R1-cold-4: reload 는 스냅샷이 가진 내용을 싣지 않고(필요하면 restore), 갇힌 내용은 hot 파일이 있을 때만 싣는다 —
// 없으면 데몬이 파일 없는 문서로 보고 cold store 청크(유일한 사본)까지 지운다.
func TestReloadableSkipsOwnedAndFileLessStrandedContent(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore(t.TempDir())
	root := t.TempDir()
	const (
		index = "mem-t-2026.05"
		gone  = "cold-mem-t-2026.05-20260801t000000-000000001"
		live  = "cold-mem-t-2026.05-20260901t000000-000000002"
	)
	mustDo(t, AppendManifest(ctx, store, "t", index, gone, []ManifestEntry{{SourcePath: "docs/lost.md", ContentSHA: "l1"}, {SourcePath: "docs/back.md", ContentSHA: "b1"}}))
	mustDo(t, AppendManifest(ctx, store, "t", index, live, []ManifestEntry{{SourcePath: "docs/owned.md", ContentSHA: "o1"}}))
	writeHot(t, root, "docs/back.md", "# 사람이 hot 에 되살렸다\n")
	own := NewOwnership(archiveOf(map[string][]string{index: {live}}), store)
	may := func(path, sha string) model.Chunk {
		return model.Chunk{Team: "t", Kind: model.KindEpisodic, SourcePath: path, ContentSHA: sha, EventTime: "2026-05-10T00:00:00Z"}
	}

	cases := []struct {
		chunk model.Chunk
		want  bool
	}{
		{may("docs/owned.md", "o1"), false}, // 스냅샷에 있다 — restore 로
		{may("docs/lost.md", "l1"), false},  // 갇혔고 hot 파일이 없다
		{may("docs/back.md", "b1"), true},   // 갇혔지만 hot 파일이 있다
		{may("docs/hot.md", "h1"), true},    // cold 와 무관한 hot 문서
	}
	for _, c := range cases {
		if got := own.Reloadable(ctx, root, c.chunk); got != c.want {
			t.Errorf("Reloadable(%s) = %v, want %v", c.chunk.SourcePath, got, c.want)
		}
	}
}
