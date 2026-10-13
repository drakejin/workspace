package state

import (
	"reflect"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// R1-paths-3: 문서의 정체(source_path)는 NFC 다. 그 전에 NFD 경로(Finder 가 만든 한글 이름)로 남은 판정·cold 문서 조치
// 원장 기록도 읽을 때 NFC 로 맞춰 계속 적용된다 — 밀린·deprecated 문서가 다시 떠오르거나 같은 쌍을 다시 판정하지 않게.
func TestLedgerPathsAreReadAsNFC(t *testing.T) {
	oldNFC, newNFC := "docs/specs/결제/v1.md", "docs/specs/결제/v2.md"
	oldNFD, newNFD := norm.NFD.String(oldNFC), norm.NFD.String(newNFC)
	if oldNFD == oldNFC {
		t.Fatal("NFD 철자가 NFC 와 같다 — 테스트 전제가 틀렸다")
	}
	root := t.TempDir()

	judge := NewJudgeState(root)
	rec := JudgeRecord{PairKey: PairKey("sha-1", "sha-2"), Decision: DecisionSuperseded,
		Old: Side{Path: oldNFD, BodySHA: "sha-1"}, New: Side{Path: newNFD, BodySHA: "sha-2"}}
	if err := judge.Ledger.Append("t", rec); err != nil {
		t.Fatal(err)
	}
	docs := map[string]JudgedDoc{oldNFC: {BodySHA: "sha-1"}, newNFC: {BodySHA: "sha-2"}}
	got, err := judge.SupersededMap("t", docs)
	if err != nil || !reflect.DeepEqual(got, map[string]string{oldNFC: newNFC}) {
		t.Errorf("SupersededMap = %v %v, want %s → %s", got, err, oldNFC, newNFC)
	}
	latest, err := judge.Latest("t")
	if err != nil || latest[rec.PairKey].Old.Path != oldNFC || latest[rec.PairKey].New.Path != newNFC {
		t.Errorf("Latest = %+v %v (경로는 NFC)", latest, err)
	}

	archive := NewArchiveState(root)
	if err := archive.Ledger.Append("t", ArchiveRecord{Action: ArchiveDeprecate, Path: oldNFD}); err != nil {
		t.Fatal(err)
	}
	dep, err := archive.Deprecated("t")
	if err != nil || !reflect.DeepEqual(dep, map[string]bool{oldNFC: true}) {
		t.Errorf("Deprecated = %v %v, want {%s}", dep, err, oldNFC)
	}
	// NFC 로 남긴 해제 기록이 NFD 로 남긴 deprecate 기록을 푼다
	if err := archive.Ledger.Append("t", ArchiveRecord{Action: ArchiveRestore, Path: oldNFC}); err != nil {
		t.Fatal(err)
	}
	if dep, err := archive.Deprecated("t"); err != nil || len(dep) != 0 {
		t.Errorf("해제 뒤 Deprecated = %v %v", dep, err)
	}
}
