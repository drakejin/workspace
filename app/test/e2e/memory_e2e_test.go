//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/compaction"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

type mcpHarness struct {
	cfg     config.Config
	os      opensearch.Client
	store   cold.Store
	indexer ingest.Indexer
	session *mcp.ClientSession
}

func newMCP(t *testing.T, cfg config.Config) *mcpHarness {
	t.Helper()
	ctx := context.Background()
	client := opensearch.New(cfg.OpenSearchURL)
	store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	ix := ingest.New(ingest.Deps{OS: client, Cold: store, Config: cfg})
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: client, Cold: store, Indexer: ix, Log: t.Logf})
	serverT, clientT := mcp.NewInMemoryTransports()
	server := mcpserver.NewServer(h)
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &mcpHarness{cfg: cfg, os: client, store: store, indexer: ix, session: session}
}

func (m *mcpHarness) call(t *testing.T, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := m.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestMCPToolsAreTheTalksFourPlusFiltersAndGraph(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	res, err := m.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	// 발표의 도구 네 개 + 검색 필터 값 + knowledge graph 도구 두 개
	if !reflect.DeepEqual(names, []string{"docs_by_entity", "ingest_path", "list_cold", "related_docs", "restore_cold", "search_filters", "search_memory"}) {
		t.Errorf("tools = %v", names)
	}
}

func TestSearchFindsIdentifiersWithoutBackground(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	root := m.cfg.MetaRepoRoot
	write(t, root, "docs/repository/attribution/adr/0007-click-dedup.md",
		"---\nrepo: attribution\ndate: "+daysAgo(2)+"\n---\n# ADR-0007 클릭 중복 제거\n## 결정\n`ClickDeduplicator.window` 는 30m.\n")
	write(t, root, "main/serving/docs/prd-active-ad.md", "---\ndate: "+daysAgo(1)+"\n---\n# 활성 광고\n## 활성 광고 정의\n예산이 남은 광고")

	if text, isErr := m.call(t, "ingest_path", map[string]any{"path": "docs"}); isErr || !strings.Contains(text, "0007-click-dedup.md → mem-") {
		t.Fatalf("ingest docs = %s", text)
	}
	if text, _ := m.call(t, "ingest_path", map[string]any{"path": "main/serving/docs"}); !strings.Contains(text, "mem-"+m.cfg.DefaultTeam+"-knowledge") {
		t.Errorf("관리 대상 repo docs 는 knowledge 인덱스로: %s", text)
	}
	if text, isErr := m.call(t, "ingest_path", map[string]any{"path": "../../etc"}); !isErr || !strings.Contains(text, "밖의 경로") {
		t.Errorf("meta-repo 밖은 거부: %s", text)
	}

	// 기본 standard analyzer 였다면 0건이었던 질의 — 식별자 분할 analyzer 로 맞는다
	text, _ := m.call(t, "search_memory", map[string]any{"query": "ClickDeduplicator window"})
	if !strings.Contains(text, "docs/repository/attribution/adr/0007-click-dedup.md › ADR-0007 클릭 중복 제거 > 결정") {
		t.Errorf("search = %s", text)
	}
	text, _ = m.call(t, "search_memory", map[string]any{"query": "활성 광고 정의", "project": "serving"})
	if !strings.Contains(text, "main/serving/docs/prd-active-ad.md") {
		t.Errorf("repo 필터 = %s", text)
	}
	text, _ = m.call(t, "search_memory", map[string]any{"query": "ClickDeduplicator", "time_range": map[string]any{"from": "now-1d"}})
	if !strings.Contains(text, "문서가 없습니다") {
		t.Errorf("time_range = %s", text)
	}
	if text, isErr := m.call(t, "search_memory", map[string]any{"query": "x", "team": "Bad Team"}); !isErr {
		t.Errorf("잘못된 team 은 도구 에러: %s", text)
	}
}

func TestReindexKeepsLiveChunksAndRemoveCleansUp(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	file := write(t, m.cfg.MetaRepoRoot, "docs/specs/shrinking.md", "# S\n## 하나\nAlphaSectionToken\n## 둘\nBetaSectionToken")
	if _, err := m.indexer.IngestPath(ctx, file, ""); err != nil {
		t.Fatal(err)
	}
	write(t, m.cfg.MetaRepoRoot, "docs/specs/shrinking.md", "# S\n## 하나\nAlphaSectionToken 수정")
	if _, err := m.indexer.IngestPath(ctx, file, ""); err != nil {
		t.Fatal(err)
	}
	if got := searchPaths(t, m.cfg, "AlphaSectionToken", false); len(got) != 1 {
		t.Errorf("Alpha = %v", got)
	}
	if got := searchPaths(t, m.cfg, "BetaSectionToken", false); len(got) != 0 {
		t.Errorf("낡은 청크만 지워진다: %v", got)
	}
	// 남은 청크는 "S" 와 "S > 하나" 두 개
	n, err := m.indexer.RemoveFile(ctx, file, nil)
	if err != nil || n != 2 {
		t.Errorf("RemoveFile = %d %v", n, err)
	}
	if _, ok, _ := m.store.Get(ctx, cold.ChunksKey(m.cfg.DefaultTeam, "docs/specs/shrinking.md")); ok {
		t.Error("청크 JSONL 도 지운다")
	}
	if _, ok, _ := m.store.Get(ctx, cold.RawKey(m.cfg.DefaultTeam, "docs/specs/shrinking.md")); !ok {
		t.Error("raw 원본은 남긴다")
	}
}

func TestColdTierRestoreAndReload(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	write(t, root, "docs/specs/new.md", "---\ndate: "+daysAgo(2)+"\n---\n# 새 문서\nFreshDocToken")
	legacy := write(t, root, "docs/repository/attribution/adr/0001-legacy.md",
		"---\nrepo: attribution\ndate: "+daysAgo(hotDays+60)+"\n---\n# ADR-0001 레거시\nLegacyWindowPolicy 사용.\n")
	write(t, root, "docs/knowledge/old-fact.md", "---\ndoc_type: knowledge\ndate: "+daysAgo(hotDays+240)+"\n---\n# 오래된 사실\nTimelessFactToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}

	// compaction 1단계: 수명 확인은 episodic 만 — knowledge 는 노화하지 않는다
	report, err := compaction.Build(ctx, m.os, m.cfg, m.cfg.DefaultTeam, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Expired) != 1 || report.Expired[0].SourcePath != "docs/repository/attribution/adr/0001-legacy.md" {
		t.Errorf("expired = %+v", report.Expired)
	}

	// 5단계: 티어 이동 — 스냅샷 → hot 원본 제거 → 인덱스 삭제
	decisions, err := cold.Tier(ctx, m.os, m.store, m.cfg, cold.TierOptions{Team: m.cfg.DefaultTeam})
	if err != nil {
		t.Fatal(err)
	}
	var tiered string
	for _, d := range decisions {
		if d.Action == cold.TierMoved {
			tiered = d.Index
			if !reflect.DeepEqual(d.RemovedFiles, []string{"docs/repository/attribution/adr/0001-legacy.md"}) {
				t.Errorf("removed = %v", d.RemovedFiles)
			}
		}
	}
	if tiered == "" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("hot 원본은 cold 로 넘어갔다")
	}
	if raw, ok, _ := m.store.Get(ctx, cold.RawKey(m.cfg.DefaultTeam, "docs/repository/attribution/adr/0001-legacy.md")); !ok || !strings.Contains(raw, "LegacyWindowPolicy") {
		t.Error("원본은 cold store raw 에 있다")
	}
	if got := searchPaths(t, m.cfg, "LegacyWindowPolicy", false); len(got) != 0 {
		t.Errorf("cold 는 검색되지 않는다: %v", got)
	}
	if got := searchPaths(t, m.cfg, "TimelessFactToken", false); len(got) != 1 {
		t.Errorf("knowledge 는 남는다: %v", got)
	}

	text, _ := m.call(t, "list_cold", map[string]any{})
	if !strings.Contains(text, tiered) {
		t.Errorf("list_cold = %s", text)
	}
	if text, _ := m.call(t, "restore_cold", map[string]any{"index": tiered}); !strings.Contains(text, "복원할까요") {
		t.Errorf("확인 없이는 복원하지 않는다: %s", text)
	}
	if got := searchPaths(t, m.cfg, "LegacyWindowPolicy", false); len(got) != 0 {
		t.Error("아직 복원 전")
	}
	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": tiered, "confirmed": true}); isErr {
		t.Fatalf("restore = %s", text)
	}
	if got := searchPaths(t, m.cfg, "LegacyWindowPolicy", false); !contains(got, "docs/repository/attribution/adr/0001-legacy.md") {
		t.Errorf("복원 후 검색 = %v", got)
	}
	again, _ := cold.Tier(ctx, m.os, m.store, m.cfg, cold.TierOptions{Team: m.cfg.DefaultTeam, DryRun: true})
	for _, d := range again {
		if d.Index == tiered && d.Action != cold.TierRestored {
			t.Errorf("복원본은 tier 대상이 아니다: %+v", d)
		}
	}

	// S3 층(청크 JSONL)에서 _bulk 재적재 — 인덱스를 지워도 같은 검색
	_ = m.os.Do(ctx, "DELETE", "/mem-"+m.cfg.DefaultTeam+"-*", nil, nil, nil)
	reloaded, err := ingest.Reload(ctx, m.store, m.indexer, m.cfg.DefaultTeam, func(model.Chunk) bool { return true })
	if err != nil || reloaded.Chunks == 0 || len(reloaded.Corrupted) != 0 {
		t.Fatalf("reload = %+v %v", reloaded, err)
	}
	if got := searchPaths(t, m.cfg, "FreshDocToken", false); len(got) != 1 {
		t.Errorf("reload 후 = %v", got)
	}
}

func TestTeamPrefixAndWorkspaceIsolation(t *testing.T) {
	base := newConfig(t, nil)
	ctx := context.Background()
	sub := base.DefaultTeam + "-sub"
	client := opensearch.New(base.OpenSearchURL)
	t.Cleanup(func() { _ = client.Do(ctx, "DELETE", "/mem-"+sub+"-*", nil, nil, nil) })
	m := newMCP(t, base)
	write(t, base.MetaRepoRoot, "docs/old.md", "---\nteam: "+sub+"\ndate: "+daysAgo(hotDays+140)+"\n---\n# old\nx")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	report, _ := compaction.Build(ctx, client, base, base.DefaultTeam, time.Now())
	decisions, _ := cold.Tier(ctx, client, m.store, base, cold.TierOptions{Team: base.DefaultTeam, DryRun: true})
	if len(report.Expired) != 0 || len(decisions) != 0 {
		t.Errorf("team X 는 team X-sub 를 잡지 않는다: %+v %+v", report.Expired, decisions)
	}

	// 팀 하나는 workspace 하나에 속한다 — 다른 meta-repo 가 같은 팀으로 적재하면 거부한다
	// (인덱스 단위로 움직이는 tier·ISM·restore 가 남의 문서를 지우지 않게)
	write(t, base.MetaRepoRoot, "docs/same.md", "# 같은 경로\nWorkspaceAToken")
	if _, err := m.indexer.IngestPath(ctx, filepath.Join(base.MetaRepoRoot, "docs/same.md"), ""); err != nil {
		t.Fatal(err)
	}
	other := base
	other.MetaRepoRoot = t.TempDir()
	other.Workspace = base.Workspace + "-other"
	o := newMCP(t, other)
	write(t, other.MetaRepoRoot, "docs/same.md", "# 같은 경로\nWorkspaceBToken")
	if _, err := o.indexer.IngestPath(ctx, filepath.Join(other.MetaRepoRoot, "docs/same.md"), ""); !errors.Is(err, ingest.ErrTeamOwned) {
		t.Fatalf("같은 팀 = %v", err)
	}

	// 다른 팀이면 같은 OpenSearch·같은 경로라도 workspace 가 섞이지 않는다
	other.DefaultTeam = sub + "-b"
	t.Cleanup(func() { _ = client.Do(ctx, "DELETE", "/mem-"+other.DefaultTeam+"-*", nil, nil, nil) })
	o = newMCP(t, other)
	if _, err := o.indexer.IngestPath(ctx, filepath.Join(other.MetaRepoRoot, "docs/same.md"), ""); err != nil {
		t.Fatal(err)
	}
	if got := searchPaths(t, base, "WorkspaceAToken", false); len(got) != 1 {
		t.Errorf("A 는 덮이지 않는다: %v", got)
	}
	if got := searchPaths(t, base, "WorkspaceBToken", false); len(got) != 0 {
		t.Errorf("A 의 검색에 B 가 섞이지 않는다: %v", got)
	}
	if got := searchPaths(t, other, "WorkspaceBToken", false); len(got) != 1 {
		t.Errorf("B 는 자기 문서를 본다: %v", got)
	}
	if _, err := o.indexer.RemoveFile(ctx, filepath.Join(other.MetaRepoRoot, "docs/same.md"), nil); err != nil {
		t.Fatal(err)
	}
	if got := searchPaths(t, base, "WorkspaceAToken", false); len(got) != 1 {
		t.Errorf("B 의 삭제가 A 를 지우지 않는다: %v", got)
	}
}
