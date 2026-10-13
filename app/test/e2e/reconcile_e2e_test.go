//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/daemon"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
	"github.com/drakejin/workspace/app/internal/state"
)

// logBook 은 데몬 로그를 모은다 — 사이클과 증류 goroutine 이 함께 쓴다.
type logBook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBook) logf(t *testing.T) func(string, ...any) {
	return func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.lines = append(l.lines, line)
		t.Log(line)
	}
}

// reset 은 모은 로그를 비운다 — 다음 사이클의 로그만 보려고.
func (l *logBook) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

// has 는 prefix 로 시작하는 로그 줄이 있는가다.
func (l *logBook) has(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// bootLoggedDaemon 은 로그를 모으는 데몬을 띄운다 (boot 사이클까지 돈다).
func bootLoggedDaemon(t *testing.T, cfg config.Config) (*daemon.Daemon, *logBook) {
	t.Helper()
	logs := &logBook{}
	d, err := daemon.New(context.Background(), daemon.Deps{Config: cfg, Ask: &fakeLLM{}, Log: logs.logf(t), RetryDelays: []time.Duration{100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	return d, logs
}

// TestGatedFilesLeaveTheIndexAndSayRemove 는 IDX-6 의 재현이다. 색인에 있던 파일이 게이트(크기·바이너리)에 걸리면 둘 다 색인 범위
// 밖이라 색인과 cold store 청크에서 빠지고, 로그는 skip 이 아니라 remove 와 사유를 쓴다 — 예전에는 크기 초과만 지우면서 로그는
// skip 이라고 했고, 바이너리는 옛 판이 그대로 남았다. frontmatter 오류처럼 고치면 되는 실패는 옛 판을 남기고 fail 로 센다.
func TestGatedFilesLeaveTheIndexAndSayRemove(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_MAX_FILE_BYTES": "300"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	const grow, bin, typo = "docs/specs/gate/grow.md", "docs/specs/gate/bin.md", "docs/specs/gate/typo.md"
	write(t, root, grow, "# 성장\nGateGrowToken\n")
	write(t, root, bin, "# 바이너리\nGateBinToken\n")
	write(t, root, typo, "# 오타\nGateTypoToken\n")
	store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	d, logs := bootLoggedDaemon(t, cfg)
	if got := lastSummary(t, root); got.Indexed != 3 {
		t.Fatalf("boot = %+v", got)
	}

	write(t, root, grow, "# 성장\nGateGrowToken\n"+strings.Repeat("길게 늘어난 본문. ", 40))
	write(t, root, bin, "# 바이너리\nGateBinToken\n\x00\x01")
	write(t, root, typo, "---\nteam: \"bad team!\"\n---\n# 오타\nGateTypoToken\n")
	logs.reset()
	d.Trigger("timer")

	if got := lastSummary(t, root); got.Removed != 2 || got.Skipped != 2 || got.Failed != 1 {
		t.Errorf("cycle = %+v (게이트에 걸린 둘은 removed·skipped, frontmatter 오류는 failed)", got)
	}
	for _, rel := range []string{grow, bin} {
		if !logs.has("[reconcile] remove "+rel+" (") || logs.has("[reconcile] skip "+rel) {
			t.Errorf("%s: 지웠으면 remove 로 알린다 — skip 이라고 쓰지 않는다", rel)
		}
		if _, ok, _ := cold.Archived(ctx, store, cfg.DefaultTeam, rel); ok {
			t.Errorf("%s 의 cold store 청크가 남았다", rel)
		}
	}
	for _, token := range []string{"GateGrowToken", "GateBinToken"} {
		if got := searchPaths(t, cfg, token, true); len(got) != 0 {
			t.Errorf("%s 가 아직 검색된다: %v", token, got)
		}
	}
	if got := searchPaths(t, cfg, "GateTypoToken", false); !reflect.DeepEqual(got, []string{typo}) {
		t.Errorf("frontmatter 오류는 고칠 때까지 옛 판을 남긴다: %v", got)
	}
	if !logs.has("[reconcile] fail " + typo) {
		t.Errorf("frontmatter 오류는 fail 로 알린다")
	}

	logs.reset()
	d.Trigger("timer")
	if got := lastSummary(t, root); got.Removed != 0 || got.Skipped != 2 {
		t.Errorf("다음 사이클은 지우지 않고 건너뛰기만 한다: %+v", got)
	}
	if !logs.has("[reconcile] skip "+grow+" (size") || !logs.has("[reconcile] skip "+bin+" (binary content)") {
		t.Errorf("색인에 없는 게이트 파일은 skip 으로 알린다")
	}
}

// TestOutOfScopeIndexEntryIsRemovedOnce 는 IDX-7 의 재현이다. 색인 규칙이 바뀌어(또는 옛 cold 청크를 reload 해) 색인 범위 밖
// 경로가 색인에 남으면 tombstone 이 한 번에 지운다 — 예전에는 RemoveFile 이 범위 밖 경로를 0건 지우고도 매 사이클 removed 로
// 세서 diff 가 비지 않았고, 문서는 계속 검색됐다.
func TestOutOfScopeIndexEntryIsRemovedOnce(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"})
	ctx := context.Background()
	d, _ := bootLoggedDaemon(t, cfg)
	const legacy = "scripts/legacy.md"
	store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	ix := ingest.New(ingest.Deps{OS: opensearch.New(cfg.OpenSearchURL), Cold: store, Config: cfg})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	chunk := model.Chunk{
		ID: document.ChunkID(cfg.Workspace, cfg.DefaultTeam, legacy, 0), Workspace: cfg.Workspace, Team: cfg.DefaultTeam,
		DocType: model.DocKnowledge, Kind: model.KindKnowledge, SourcePath: legacy, Text: "LegacyPhantomToken", ContentSHA: "legacy",
		EventTime: now, EventDay: daysAgo(0), IngestedAt: now, IndexVersion: model.IndexVersion,
	}
	if _, err := ix.BulkChunks(ctx, []model.Chunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, cold.ChunksKey(cfg.DefaultTeam, legacy), cold.EncodeChunks([]model.Chunk{chunk})); err != nil {
		t.Fatal(err)
	}

	d.Trigger("timer")
	if got := lastSummary(t, cfg.MetaRepoRoot); got.Removed != 1 {
		t.Errorf("첫 사이클이 지운다: %+v", got)
	}
	if got := searchPaths(t, cfg, "LegacyPhantomToken", true); len(got) != 0 {
		t.Errorf("범위 밖 경로가 아직 검색된다: %v", got)
	}
	if _, ok, _ := cold.Archived(ctx, store, cfg.DefaultTeam, legacy); ok {
		t.Error("범위 밖 경로의 cold store 청크가 남았다 (reload 가 되살린다)")
	}
	d.Trigger("timer")
	if got := lastSummary(t, cfg.MetaRepoRoot); got.Removed != 0 {
		t.Errorf("지운 뒤의 사이클은 removed 를 세지 않는다: %+v", got)
	}
}

// TestRewrittenHiddenDocsStayHiddenOnBoot 는 IDX-13 의 재현이다. 밀린 knowledge 와 deprecate 한 문서를 데몬이 꺼져 있는 동안
// 고치면, 다음 boot 사이클이 다시 쓰면서도 숨김 표시를 잇는다 — 예전에는 boot 때 정본 표시를 아직 몰라 superseded_by 없이
// knowledge 인덱스에, deprecated=false 로 썼다가 사이클 끝의 수렴이 되돌렸다 (그 사이 기본 검색에 드러나고 cold store 를 두 번 썼다).
func TestRewrittenHiddenDocsStayHiddenOnBoot(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off", "MEMORY_JUDGE_ENABLED": "false"})
	root := cfg.MetaRepoRoot
	const k1, k2, dep = "docs/knowledge/hold/k1.md", "docs/knowledge/hold/k2.md", "docs/specs/hold/old.md"
	day := daysAgo(5)
	write(t, root, k1, "---\ndoc_type: knowledge\ndate: "+day+"\n---\n# 한도\nHoldK1Token 은 100 이다.\n")
	write(t, root, k2, "---\ndoc_type: knowledge\nsupersedes: "+k1+"\n---\n# 한도 개정\nHoldK2Token 은 200 이다.\n")
	write(t, root, dep, "---\ndate: "+day+"\n---\n# 낡은 스펙\nHoldDepToken\n")
	rec := state.ArchiveRecord{Action: state.ArchiveDeprecate, Path: dep, By: "e2e", Reason: "틀린 스펙"}
	if err := state.NewArchiveState(root).Ledger.Append(cfg.DefaultTeam, rec); err != nil {
		t.Fatal(err)
	}
	first := bootDaemon(t, cfg, &fakeLLM{})
	if err := first.Shutdown(); err != nil {
		t.Fatal(err)
	}

	write(t, root, k1, "---\ndoc_type: knowledge\ndate: "+day+"\n---\n# 한도\nHoldK1Token 은 150 이다.\n")
	write(t, root, dep, "---\ndate: "+day+"\n---\n# 낡은 스펙\nHoldDepToken 고침\n")
	_, _ = bootLoggedDaemon(t, cfg)

	if got := lastSummary(t, root); got.Indexed != 2 || got.FlagsConverged != 0 {
		t.Errorf("boot = %+v (다시 쓴 두 문서가 처음부터 숨김 표시를 가져 수렴할 것이 없다)", got)
	}
	monthly := naming.MonthlyIndex(cfg.DefaultTeam, model.ParseTime(day+"T00:00:00Z"))
	if got := docIndices(t, opensearch.New(cfg.OpenSearchURL), cfg, k1); !reflect.DeepEqual(got, []string{monthly}) {
		t.Errorf("밀린 knowledge 는 자기 달 인덱스에 바로 쓴다: %v", got)
	}
	for _, token := range []string{"HoldK1Token", "HoldDepToken"} {
		if got := searchPaths(t, cfg, token, false); len(got) != 0 {
			t.Errorf("%s 가 기본 검색에 드러났다: %v", token, got)
		}
	}
}

// TestTeamChangeLeavesNoChunksInTheOldTeam 은 RUN-11 의 재현이다. frontmatter team 을 바꿔 다시 적재하면 옛 팀의 cold store
// 청크 JSONL 도 지운다 — 남아 있으면 reload 가 옛 팀 사본을 되살려 같은 문서가 두 팀에 보인다.
func TestTeamChangeLeavesNoChunksInTheOldTeam(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	m := newMCP(t, cfg)
	const rel = "docs/knowledge/move/glossary.md"
	other := cfg.DefaultTeam + "-t2"
	write(t, root, rel, "# 용어\nTeamMoveToken\n")
	if _, err := ingestFile(ctx, m.indexer, rel); err != nil {
		t.Fatal(err)
	}
	write(t, root, rel, "---\nteam: "+other+"\n---\n# 용어\nTeamMoveToken\n")
	if _, err := ingestFile(ctx, m.indexer, rel); err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := cold.Archived(ctx, m.store, cfg.DefaultTeam, rel); ok {
		t.Error("옛 팀의 cold store 청크가 남았다")
	}
	if _, ok, _ := cold.Archived(ctx, m.store, other, rel); !ok {
		t.Error("새 팀의 cold store 청크가 없다")
	}
	if _, err := ingest.Reload(ctx, m.store, m.indexer, cfg.DefaultTeam, nil); err != nil {
		t.Fatal(err)
	}
	query := opensearch.FirstChunks(cfg.Workspace, map[string]any{"term": map[string]any{"source_path": rel}})
	if n, err := opensearch.Count(ctx, m.os, naming.KnowledgeIndex(cfg.DefaultTeam), query); err != nil || n != 0 {
		t.Errorf("reload 가 옛 팀 사본을 되살렸다: %d %v", n, err)
	}
}

// TestBlankDocumentsStayOutOfSearchResults 는 R2-handoff-1 의 재현이다. 본문이 없는 문서(빈 파일·frontmatter 만)는 색인에는
// 청크 0 하나로 남지만(데몬 수렴용) 검색 결과에는 나오지 않는다 — 예전에는 필터로만 가져오기(최신순)의 맨 위를 빈 결과로 차지해
// size 안의 진짜 문서를 밀어냈고, 세션 훅의 맥락에도 빈 문서가 들어갔다.
func TestBlankDocumentsStayOutOfSearchResults(t *testing.T) {
	cfg := newConfig(t, map[string]string{"MEMORY_GRAPH_URI": "off"})
	root := cfg.MetaRepoRoot
	ctx := context.Background()
	m := newMCP(t, cfg)
	write(t, root, "docs/knowledge/blank/real.md", "---\ndoc_type: knowledge\ndate: "+daysAgo(3)+"\n---\n# 실제\nBlankRealToken\n")
	write(t, root, "docs/knowledge/blank/empty.md", "")
	write(t, root, "docs/knowledge/blank/fm-only.md", "---\ndoc_type: knowledge\ndate: "+daysAgo(0)+"\n---\n")
	if text, isErr := m.call(t, "ingest_path", map[string]any{"path": "docs/knowledge/blank"}); isErr || !strings.Contains(text, "3개 문서 적재") {
		t.Fatalf("ingest_path = %s", text)
	}

	listing, err := search.Search(ctx, m.os, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, DocType: model.DocKnowledge})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(listing.Hits); !reflect.DeepEqual(got, []string{"docs/knowledge/blank/real.md"}) || listing.Total != 1 {
		t.Errorf("필터로만 가져오기 = %v (total %d) — 빈 문서는 나오지 않는다", got, listing.Total)
	}
	if got := searchPaths(t, cfg, "empty fm-only blank", true); contains(got, "docs/knowledge/blank/empty.md") || contains(got, "docs/knowledge/blank/fm-only.md") {
		t.Errorf("질의어 검색(세션 훅도 이 길) = %v — 빈 문서는 나오지 않는다", got)
	}
}
