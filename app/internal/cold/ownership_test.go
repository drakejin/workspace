package cold

import (
	"context"
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
