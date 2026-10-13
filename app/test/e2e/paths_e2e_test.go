//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// macOS(APFS)는 대소문자와 유니코드 정규화를 구분하지 않는다. Finder 는 한글 이름을 NFD 로 쓰고, 사람·LLM 은 NFC 로 친다.
var (
	memoNFC   = norm.NFC.String("docs/feature/환불정책/메모.md")
	memoNFD   = "docs/feature/" + norm.NFD.String("환불정책") + "/" + norm.NFD.String("메모.md")
	limitsRel = "docs/knowledge/limits-v2.md"
)

func writeSpellingFixtures(t *testing.T, root string) {
	t.Helper()
	write(t, root, memoNFD, "# 환불 메모\nNfdTopicToken\n[흐름](../payments/refund-flow.md)\n")
	write(t, root, "docs/feature/payments/refund-flow.md", "# 환불 흐름\nRefundFlowToken")
	write(t, root, limitsRel, "---\ndoc_type: knowledge\n---\n# 한도\nDailyLimitToken")
}

func lastSummary(t *testing.T, root string) state.CycleSummary {
	t.Helper()
	s := state.ReadStatus(root)
	if s == nil || s.LastCycle == nil || s.LastCycle.Summary == nil {
		t.Fatalf("status = %+v", s)
	}
	return *s.LastCycle.Summary
}

// coldChunkPaths 는 cold store 의 청크 JSONL 이 담은 source_path 다. 없으면 nil.
func coldChunkPaths(t *testing.T, store cold.Store, team, rel string) []string {
	t.Helper()
	chunks, ok, err := cold.Archived(context.Background(), store, team, rel)
	if err != nil || !ok {
		return nil
	}
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, c.SourcePath)
	}
	return out
}

func searchListed(t *testing.T, m *mcpHarness, args map[string]any) []string {
	t.Helper()
	text, isErr := m.call(t, "search_memory", args)
	if isErr {
		t.Fatalf("search_memory %v → %s", args, text)
	}
	return listed(text)
}

// TestCaseAndUnicodeVariantsAreOneDocument 는 RUN-4·RUN-5 의 재현이다. 대소문자만 다른 철자나 NFC/NFD 만 다른 철자로
// ingest_path 해도 디스크의 철자(NFC) 하나가 문서의 정체라서 두 문서가 되지 않고, 다음 사이클의 tombstone 이 진짜 문서의
// cold store 청크를 지우지 않는다. NFD 디렉토리에서 온 주제는 NFC 로 친 주제 필터에 맞고, search_filters 에 한 번만 보인다.
func TestCaseAndUnicodeVariantsAreOneDocument(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_JUDGE_ENABLED": "false"})
	root := cfg.MetaRepoRoot
	writeSpellingFixtures(t, root)
	d := bootDaemon(t, cfg, &fakeLLM{})
	t.Cleanup(func() { _ = d.Shutdown() })
	m := newMCP(t, cfg)

	if got := searchListed(t, m, map[string]any{"query": "NfdTopicToken"}); strings.Join(got, ",") != memoNFC {
		t.Errorf("NFD 파일의 source_path 는 NFC: %q", got)
	}
	if got := searchListed(t, m, map[string]any{"topic": "환불정책"}); strings.Join(got, ",") != memoNFC {
		t.Errorf("NFC 로 친 주제가 NFD 디렉토리의 문서에 맞는다: %q", got)
	}

	cases := map[string]string{"docs/knowledge/LIMITS-V2.md": limitsRel, memoNFC: memoNFC, memoNFD: memoNFC}
	for input, want := range cases {
		text, isErr := m.call(t, "ingest_path", map[string]any{"path": input})
		if isErr || !strings.Contains(text, "- "+want+" → ") || strings.Contains(text, "LIMITS-V2") {
			t.Errorf("ingest_path %q 는 디스크의 철자 %q 로 적재한다: %s", input, want, text)
		}
	}
	for token, want := range map[string]string{"DailyLimitToken": limitsRel, "NfdTopicToken": memoNFC} {
		if got := searchListed(t, m, map[string]any{"query": token}); strings.Join(got, ",") != want {
			t.Errorf("%s 는 문서 하나: %q", token, got)
		}
	}

	d.Trigger("timer")
	if got := lastSummary(t, root); got.Removed != 0 {
		t.Errorf("다른 철자의 ingest 가 남긴 문서가 없으니 지울 것도 없다: %+v", got)
	}
	for _, rel := range []string{limitsRel, memoNFC} {
		if got := coldChunkPaths(t, m.store, cfg.DefaultTeam, rel); len(got) == 0 || got[0] != rel {
			t.Errorf("%s 의 cold store 청크 = %q", rel, got)
		}
	}
	text, _ := m.call(t, "search_filters", map[string]any{})
	if strings.Count(text, "환불정책(") != 1 || !strings.Contains(text, "환불정책(1)") {
		t.Errorf("search_filters 의 주제는 하나로 보인다:\n%s", text)
	}
}

// writeLegacy 는 이 수정 전의 ingest_path 처럼 입력 철자(rel)를 그대로 source_path 로 써서 색인한다 — 이미 색인된 옛 상태를
// 흉내 낸다. 내용은 디스크의 파일(onDisk)에서 읽는다 (대소문자를 구분하는 파일 시스템에서도 같은 상태가 되게).
func writeLegacy(t *testing.T, m *mcpHarness, rel, onDisk string) {
	t.Helper()
	abs := filepath.Join(m.cfg.MetaRepoRoot, filepath.FromSlash(onDisk))
	content, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	built, err := document.Build(document.Input{Workspace: m.cfg.Workspace, SourcePath: rel, Content: string(content), DefaultTeam: m.cfg.DefaultTeam,
		FallbackTime: time.Now(), Now: time.Now(), Location: m.cfg.Location})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.indexer.Write(context.Background(), ingest.Prepared{Rel: rel, Content: string(content), Built: built}); err != nil {
		t.Fatal(err)
	}
}

// TestLegacySpellingIsTombstonedWithoutLosingColdChunks 는 옮겨 가기(migration)다. 예전 판이 남긴 다른 철자(대소문자 변형,
// NFD)의 문서는 다음 사이클이 색인에서 빼되 — 같은 파일이 디스크에 다른 철자로 있으니 cold store 청크는 지우지 않는다
// (APFS 의 로컬 cold store 에서 두 철자의 키는 같은 파일이다). 진짜 문서는 NFC source_path 로 남는다.
func TestLegacySpellingIsTombstonedWithoutLosingColdChunks(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_JUDGE_ENABLED": "false"})
	root := cfg.MetaRepoRoot
	writeSpellingFixtures(t, root)
	m := newMCP(t, cfg)
	writeLegacy(t, m, "docs/knowledge/LIMITS-V2.md", limitsRel)
	writeLegacy(t, m, memoNFD, memoNFD)

	d := bootDaemon(t, cfg, &fakeLLM{})
	t.Cleanup(func() { _ = d.Shutdown() })
	if got := lastSummary(t, root); got.Removed != 2 {
		t.Errorf("옛 철자의 문서 둘을 색인에서 뺀다: %+v", got)
	}
	for token, want := range map[string]string{"DailyLimitToken": limitsRel, "NfdTopicToken": memoNFC} {
		if got := searchListed(t, m, map[string]any{"query": token}); strings.Join(got, ",") != want {
			t.Errorf("%s 는 정본 철자의 문서 하나: %q", token, got)
		}
	}
	for _, rel := range []string{limitsRel, memoNFC} {
		if got := coldChunkPaths(t, m.store, cfg.DefaultTeam, rel); len(got) == 0 || got[0] != rel {
			t.Errorf("%s 의 cold store 청크가 남고 정본 철자를 담는다: %q", rel, got)
		}
	}
	d.Trigger("timer")
	if got := lastSummary(t, root); got.Removed != 0 || got.Indexed != 0 {
		t.Errorf("한 번 맞춘 뒤에는 조용하다: %+v", got)
	}
	// 옛 철자로 만들어진 cold store 파일(APFS 는 처음 만든 이름을 지킨다)을 감사가 orphan-chunks 로 지우지 않는다
	if err := d.Shutdown(); err != nil {
		t.Fatal(err)
	}
	rep, err := audit.Run(context.Background(), audit.Deps{OS: m.os, Config: cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if f.Kind == audit.OrphanChunks || f.Kind == audit.OrphanIndex {
			t.Errorf("감사가 살아 있는 문서를 고아로 본다: %+v", f)
		}
	}
	for _, rel := range []string{limitsRel, memoNFC} {
		if got := coldChunkPaths(t, m.store, cfg.DefaultTeam, rel); len(got) == 0 {
			t.Errorf("audit -apply 뒤에도 %s 의 cold store 청크가 남는다", rel)
		}
	}
}

// TestRelatedDocsFindsTheNFDDocumentByTheTypedPath 는 RUN-5 의 related_docs 재현이다 — NFD 디렉토리의 문서도 NFC 로 친 경로로 찾는다.
func TestRelatedDocsFindsTheNFDDocumentByTheTypedPath(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_JUDGE_ENABLED": "false"})
	g := newGraph(t, cfg.Graph)
	writeSpellingFixtures(t, cfg.MetaRepoRoot)
	ctx := context.Background()
	var logMu sync.Mutex
	d, err := daemon.New(ctx, daemon.Deps{Config: cfg, Graph: g, Ask: &fakeLLM{}, Log: func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		t.Logf(f, a...)
	}, RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	if err := d.Boot(ctx, false); err != nil {
		t.Fatal(err)
	}
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: opensearch.New(cfg.OpenSearchURL), Graph: g, Log: t.Logf})
	for _, typed := range []string{memoNFC, memoNFD} {
		out, err := h.Related(ctx, mcpserver.RelatedInput{Path: typed, Depth: 1})
		if err != nil || !strings.Contains(out, "docs/feature/payments/refund-flow.md") {
			t.Errorf("related_docs %q = %s %v", typed, out, err)
		}
	}
}

// TestIngestPathReportsGatedFilesAsSkipped 는 RUN-7 의 재현이다. ingest_path 는 데몬과 같은 게이트로 바이너리와
// MEMORY_MAX_FILE_BYTES 초과 파일을 건너뛰고 그렇게 알린다 — 색인에 남기지도, "적재했다" 고 하지도 않는다.
func TestIngestPathReportsGatedFilesAsSkipped(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_JUDGE_ENABLED": "false", "MEMORY_MAX_FILE_BYTES": "4096"})
	root := cfg.MetaRepoRoot
	write(t, root, "docs/knowledge/probe/bin.md", "\x00\x01\x02BinaryProbeToken\x00stuff")
	write(t, root, "docs/knowledge/probe/big.md", "# 큰 문서\n"+strings.Repeat("BigDocToken ", 400))
	write(t, root, "docs/knowledge/probe/ok.md", "# 보통\nOkProbeToken")
	m := newMCP(t, cfg)

	text, isErr := m.call(t, "ingest_path", map[string]any{"path": "docs/knowledge/probe"})
	if isErr || !strings.Contains(text, "1개 문서 적재") || !strings.Contains(text, "2개 건너뜀") ||
		!strings.Contains(text, "- docs/knowledge/probe/bin.md (binary content)") || !strings.Contains(text, "- docs/knowledge/probe/big.md (size ") {
		t.Errorf("ingest_path 디렉토리 = %s", text)
	}
	text, isErr = m.call(t, "ingest_path", map[string]any{"path": "docs/knowledge/probe/bin.md"})
	if isErr || !strings.Contains(text, "0개 문서 적재") || !strings.Contains(text, "1개 건너뜀") {
		t.Errorf("ingest_path 바이너리 파일 = %s", text)
	}
	for _, token := range []string{"BinaryProbeToken", "BigDocToken"} {
		if got := searchPaths(t, cfg, token, true); len(got) != 0 {
			t.Errorf("%s 는 색인에 없다: %v", token, got)
		}
	}
	d := bootDaemon(t, cfg, &fakeLLM{})
	t.Cleanup(func() { _ = d.Shutdown() })
	if got := lastSummary(t, root); got.Removed != 0 || got.Skipped != 2 {
		t.Errorf("데몬도 같은 두 파일을 건너뛰고, 지울 것이 없다: %+v", got)
	}
}

// TestSearchRejectsAFilterThatNormalizesToEmpty 는 RUN-8·API-6 의 재현이다 — topic "!!!" 가 조용히 빠져 전체 문서가 나오지 않는다.
func TestSearchRejectsAFilterThatNormalizesToEmpty(t *testing.T) {
	m := newMCP(t, newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"}))
	write(t, m.cfg.MetaRepoRoot, "docs/feature/payments/a.md", "# 결제\nPaymentsToken")
	if _, err := m.indexer.IngestPath(context.Background(), "docs", ""); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"search_memory", "search_filters"} {
		for _, topic := range []string{"!!!", " / "} {
			text, isErr := m.call(t, tool, map[string]any{"topic": topic})
			if !isErr || !strings.Contains(text, "topic") {
				t.Errorf("%s topic=%q 는 거부: %s", tool, topic, text)
			}
		}
	}
}
