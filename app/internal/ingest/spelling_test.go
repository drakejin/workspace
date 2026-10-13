package ingest

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// fakeOS 는 OpenSearch 대신이다 — 모든 Do 에 resp 를 디코드해 주고 _bulk 는 성공한다.
// 색인 내용은 보지 않고 문서의 정체(source_path)와 cold store 만 본다.
type fakeOS struct{ resp map[string]any }

func (f fakeOS) Do(_ context.Context, _, _ string, _ any, _ url.Values, out any) error {
	if out == nil || f.resp == nil {
		return nil
	}
	b, err := json.Marshal(f.resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (fakeOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

const unitTeam = "unit"

// newUnitIndexer 는 fakeOS 와 로컬 cold store 로 만든 Indexer 다. teamsOf 는 언제나 unitTeam 을 돌려준다.
func newUnitIndexer(t *testing.T, root string, maxFileBytes int64) (Indexer, cold.Store) {
	t.Helper()
	store := cold.NewLocalStore(t.TempDir())
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: unitTeam, Reconcile: config.Reconcile{MaxFileBytes: maxFileBytes}}
	teams := map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": []any{map[string]any{"key": unitTeam}}}}}
	return New(Deps{OS: fakeOS{resp: teams}, Cold: store, Config: cfg}), store
}

func writeFile(t *testing.T, root, rel string, body []byte) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Finder 같은 macOS 앱이 만든 것 같은 NFD 철자의 한글 경로와, 사용자·LLM 이 치는 NFC 철자.
var (
	nfcRel = norm.NFC.String("docs/feature/환불정책/메모.md")
	nfdRel = "docs/feature/" + norm.NFD.String("환불정책") + "/" + norm.NFD.String("메모.md")
)

const unitMaxFileBytes = 1 << 20

// RUN-4: 대소문자만 다른 철자로 ingest 해도 디스크에 있는 철자 하나가 문서의 정체다 — 같은 파일이 두 문서가 되지 않는다.
func TestIngestFileUsesTheOnDiskSpelling(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/knowledge/limits-v2.md", []byte("# 한도\nDailyLimitToken"))
	ix, store := newUnitIndexer(t, root, unitMaxFileBytes)
	for _, input := range []string{"docs/knowledge/LIMITS-V2.md", "DOCS/Knowledge/Limits-V2.md", filepath.Join(root, "docs/knowledge/limits-v2.md")} {
		res, err := ix.IngestFile(context.Background(), input, "")
		if err != nil || res == nil {
			t.Fatalf("%s: %v %v", input, res, err)
		}
		if res.SourcePath != "docs/knowledge/limits-v2.md" {
			t.Errorf("%s → source_path %q (디스크의 철자여야 한다)", input, res.SourcePath)
		}
	}
	keys, _ := store.List(context.Background(), cold.ChunksPrefix(unitTeam))
	if len(keys) != 1 || keys[0] != cold.ChunksKey(unitTeam, "docs/knowledge/limits-v2.md") {
		t.Errorf("cold store 청크 키는 하나: %v", keys)
	}
}

// RUN-4·RUN-5: NFD 이름의 파일은 NFC 로 친 철자로도, NFD 철자로도 같은 NFC source_path 하나가 된다.
func TestIngestFileStoresNFCSourcePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, nfdRel, []byte("# 환불 메모\nNfdTopicToken"))
	ix, _ := newUnitIndexer(t, root, unitMaxFileBytes)
	for name, input := range map[string]string{"NFC 입력": nfcRel, "NFD 입력": nfdRel} {
		res, err := ix.IngestFile(context.Background(), input, "")
		if err != nil || res == nil {
			t.Fatalf("%s: %v %v", name, res, err)
		}
		if res.SourcePath != nfcRel {
			t.Errorf("%s → source_path %q, NFC=%v (NFC %q 여야 한다)", name, res.SourcePath, norm.NFC.IsNormalString(res.SourcePath), nfcRel)
		}
	}
}

// RUN-4: 색인에 남은 다른 철자(대소문자·NFD)의 문서를 지워도, 같은 파일이 디스크에 있으면 cold store 청크는 지우지 않는다 —
// APFS 위의 로컬 cold store 에서는 두 철자의 키가 같은 파일이라, 지우면 진짜 문서의 청크 JSONL 이 사라진다.
func TestRemoveFileKeepsColdChunksOfAFileLiveUnderAnotherSpelling(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "docs/knowledge/limits-v2.md", []byte("# 한도\nDailyLimitToken"))
	writeFile(t, root, nfdRel, []byte("# 환불 메모\nNfdTopicToken"))
	ix, store := newUnitIndexer(t, root, unitMaxFileBytes)
	live := []string{"docs/knowledge/limits-v2.md", nfcRel}
	for _, rel := range live {
		if err := store.Put(ctx, cold.ChunksKey(unitTeam, rel), "{}\n"); err != nil {
			t.Fatal(err)
		}
	}
	for _, legacy := range []string{"docs/knowledge/LIMITS-V2.md", nfdRel} {
		if _, err := ix.RemoveFile(ctx, legacy, nil); err != nil {
			t.Fatalf("RemoveFile %s: %v", legacy, err)
		}
	}
	for _, rel := range live {
		if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, rel)); !ok {
			t.Errorf("%s 의 청크 JSONL 이 지워졌다", rel)
		}
	}
	// 같은 철자로 지우면(파일이 정말 그 이름이면) 예전처럼 청크도 지운다
	if _, err := ix.RemoveFile(ctx, "docs/knowledge/limits-v2.md", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(ctx, cold.ChunksKey(unitTeam, "docs/knowledge/limits-v2.md")); ok {
		t.Error("같은 철자의 RemoveFile 은 청크 JSONL 을 지운다")
	}
}

// RUN-4·RUN-5: 스캔은 디스크의 이름(NFD 일 수 있다)으로 걷지만, 문서는 NFC source_path 로 맞추고 IO 는 실제 경로로 한다.
func TestScanHotKeysNFCAndKeepsTheRealPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, nfdRel, []byte("# 환불 메모"))
	files, _, err := ScanHot(root, unitMaxFileBytes)
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %+v %v", files, err)
	}
	if files[0].Rel != nfcRel {
		t.Errorf("rel = %q (NFC=%v), NFC %q 여야 한다", files[0].Rel, norm.NFC.IsNormalString(files[0].Rel), nfcRel)
	}
	if _, err := os.Stat(files[0].Abs); err != nil {
		t.Errorf("abs 는 읽을 수 있는 실제 경로: %v", err)
	}
	if files[0].Abs != filepath.Join(root, filepath.FromSlash(nfdRel)) {
		t.Errorf("abs = %q — 디스크의 이름 그대로여야 한다", files[0].Abs)
	}
}

func TestResolveOnDisk(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/knowledge/limits-v2.md", []byte("x"))
	writeFile(t, root, nfdRel, []byte("x"))
	cases := []struct{ input, rel, real string }{
		{"docs/knowledge/limits-v2.md", "docs/knowledge/limits-v2.md", "docs/knowledge/limits-v2.md"},
		{"DOCS/Knowledge/LIMITS-V2.md", "docs/knowledge/limits-v2.md", "docs/knowledge/limits-v2.md"},
		{nfcRel, nfcRel, nfdRel},
		{nfdRel, nfcRel, nfdRel},
		{"docs/feature/" + norm.NFC.String("환불정책"), norm.NFC.String("docs/feature/환불정책"), "docs/feature/" + norm.NFD.String("환불정책")},
		// 디스크에 없으면 입력 철자 그대로 (NFC) — 읽는 쪽이 실패한다
		{"docs/knowledge/NONE.md", "docs/knowledge/NONE.md", "docs/knowledge/NONE.md"},
		{"Docs/" + norm.NFD.String("없음") + ".md", "docs/" + norm.NFC.String("없음") + ".md", "docs/" + norm.NFD.String("없음") + ".md"},
	}
	for _, c := range cases {
		abs, rel, err := ResolveOnDisk(root, c.input)
		if err != nil || rel != c.rel || abs != filepath.Join(root, filepath.FromSlash(c.real)) {
			t.Errorf("ResolveOnDisk(%q) = %q %q %v, want rel %q real %q", c.input, abs, rel, err, c.rel, c.real)
		}
	}
	if _, _, err := ResolveOnDisk(root, "../outside.md"); err == nil {
		t.Error("meta-repo 밖은 거부")
	}
}

// 대소문자를 구분하는 파일 시스템에서 두 철자가 모두 있으면 그 철자 그대로인 것을 고른다.
func TestResolveOnDiskPrefersTheExactName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/a.md", []byte("소문자"))
	writeFile(t, root, "docs/A.md", []byte("대문자"))
	if entries, _ := os.ReadDir(filepath.Join(root, "docs")); len(entries) != 2 {
		t.Skip("대소문자를 구분하지 않는 파일 시스템 — 두 철자가 한 파일이다")
	}
	for _, name := range []string{"docs/a.md", "docs/A.md"} {
		if _, rel, err := ResolveOnDisk(root, name); err != nil || rel != name {
			t.Errorf("ResolveOnDisk(%q) = %q %v", name, rel, err)
		}
	}
}

func TestLiveUnderOtherSpelling(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/knowledge/limits-v2.md", []byte("x"))
	writeFile(t, root, nfdRel, []byte("x"))
	_, statErr := os.Stat(filepath.Join(root, "docs/knowledge/LIMITS-V2.md"))
	cases := map[string]bool{
		"docs/knowledge/limits-v2.md": false, // 정본 철자 그대로
		nfcRel:                        false, // 정본(NFC) — 디스크 이름이 NFD 여도 정체는 NFC 다
		nfdRel:                        true,  // 디스크에 있지만 정본은 NFC
		"docs/knowledge/none.md":      false,
		// 대소문자를 구분하지 않는 파일 시스템(APFS)에서만 다른 철자로 열린다
		"docs/knowledge/LIMITS-V2.md": statErr == nil,
	}
	for rel, want := range cases {
		if got := LiveUnderOtherSpelling(root, rel); got != want {
			t.Errorf("LiveUnderOtherSpelling(%q) = %v", rel, got)
		}
	}
}
