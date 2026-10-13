package cold

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

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
	archive := Archive{
		"mem-t-2026.05": {"snap-1", "snap-2"},
		"mem-t-2026.04": {"ism-cold-1"},
		"mem-t-2026.03": {"ism-cold-0", "snap-tier"},
	}
	own := NewOwnership(archive, store)

	cases := []struct {
		index, path, sha string
		want             bool
	}{
		{"mem-t-2026.05", "docs/a.md", "a1", true},   // 첫 스냅샷의 내용
		{"mem-t-2026.05", "docs/a.md", "a2", true},   // 다시 tier 한 내용
		{"mem-t-2026.05", "docs/a.md", "a3", false},  // 스냅샷 뒤에 쓴 내용 — cold 에 없다
		{"mem-t-2026.05", "docs/b.md", "b1", false},  // 목록에 없는 문서
		{"mem-t-2026.04", "docs/c.md", "c1", true},   // ISM 이 만든 스냅샷 — 목록이 없으면 달만 본다
		{"mem-t-2026.03", "docs/old.md", "o1", true}, // 목록이 덮지 못하는 ISM 스냅샷이 있으면 달만 본다
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
	archive := Archive{
		"mem-t-2026.10": {"snap-empty"},
		// 목록을 쓰기 전에 끊긴 tier 스냅샷 — tier·archive 가 뜬 스냅샷은 이름(cold-)으로 안다
		"mem-t-2026.09": {"cold-mem-t-2026.09-20261013t000000-000000001"},
		"mem-t-2026.08": {"ism-cold-2026.10.13-00:00:00.000"},
	}
	own := NewOwnership(archive, store)

	cases := []struct {
		index string
		want  bool
	}{
		{"mem-t-2026.10", false}, // 빈 tier 스냅샷은 아무것도 갖지 않는다
		{"mem-t-2026.09", false}, // 목록 없는 tier 스냅샷도 마찬가지
		{"mem-t-2026.08", true},  // ISM 스냅샷만 달 단위로 본다
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
