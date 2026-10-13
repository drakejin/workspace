//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/archive"
	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/ops"
	"github.com/drakejin/workspace/app/internal/search"
)

// TestAuditFindsDriftAndApplyFixesIt 는 색인·cold store 와 파일이 어긋난 네 가지(고칠 수 있음)와 보고만 하는 두 가지를 만든 뒤
// 계획 → 적용 → 다시 계획 순서로 확인한다.
func TestAuditFindsDriftAndApplyFixesIt(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	gone := write(t, root, "docs/specs/gone.md", "# 지워질 문서\nGoneToken")
	write(t, root, "docs/specs/changed.md", "# 바뀔 문서\nBeforeToken")
	write(t, root, "docs/specs/linker.md", "# 링크\n[없는 문서](./missing.md) 를 본다")
	write(t, root, "docs/knowledge/bare-fact.md", "---\ndoc_type: knowledge\n---\n# 근거 없는 사실\nBareFactToken")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	write(t, root, "docs/specs/changed.md", "# 바뀔 문서\nAfterToken")
	write(t, root, "docs/specs/new.md", "# 새 문서\nUnindexedToken")

	deps := audit.Deps{OS: m.os, Config: m.cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}
	kinds := func(rep audit.Report) []string {
		var out []string
		for _, f := range rep.Findings {
			out = append(out, fmt.Sprintf("%s %s fixed=%v", f.Kind, f.Path, f.Fixed))
		}
		sort.Strings(out)
		return out
	}

	plan, err := audit.Run(ctx, deps, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"broken-link docs/specs/linker.md fixed=false",
		"orphan-index docs/specs/gone.md fixed=false",
		"stale-content docs/specs/changed.md fixed=false",
		"unindexed docs/specs/new.md fixed=false",
		"unsourced-fact docs/knowledge/bare-fact.md fixed=false",
	}
	if got := kinds(plan); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plan\n got  %v\n want %v", got, want)
	}
	if got := searchPaths(t, m.cfg, "UnindexedToken", false); len(got) != 0 {
		t.Error("계획만 세우면 아무것도 바꾸지 않는다")
	}

	applied, err := audit.Run(ctx, deps, true)
	if err != nil {
		t.Fatal(err)
	}
	fixed := 0
	for _, f := range applied.Findings {
		if f.Fixed {
			fixed++
		}
	}
	if fixed != 3 || !applied.Applied {
		t.Errorf("apply = %v", kinds(applied))
	}
	if got := searchPaths(t, m.cfg, "UnindexedToken", false); len(got) != 1 {
		t.Errorf("unindexed 는 색인된다: %v", got)
	}
	if got := searchPaths(t, m.cfg, "GoneToken", false); len(got) != 0 {
		t.Errorf("orphan 은 빠진다: %v", got)
	}
	if got := searchPaths(t, m.cfg, "AfterToken", false); len(got) != 1 {
		t.Errorf("stale 은 재색인된다: %v", got)
	}

	again, err := audit.Run(ctx, deps, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(again); len(got) != 2 {
		t.Errorf("고친 뒤에는 보고용 두 건만 남는다: %v", got)
	}
}

// TestArchiveActionsOnColdDocument 는 cold 로 간 문서에 대한 사람의 조치 네 가지 — deprecate·undeprecate·edit·purge — 를
// 감사 기록과 함께 확인한다. 복원해도 deprecate 표시는 다시 입혀진다.
func TestArchiveActionsOnColdDocument(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	const doc = "docs/repository/billing/adr/0001-old.md"
	date := daysAgo(hotDays + 60)
	write(t, root, doc, "---\nrepo: billing\ndate: "+date+"\n---\n# ADR-0001 옛 결정\nWrongDecisionToken 을 쓴다.\n")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	decisions, err := cold.Tier(ctx, m.os, m.store, m.cfg, cold.TierOptions{Team: m.cfg.DefaultTeam})
	if err != nil {
		t.Fatal(err)
	}
	var index string
	for _, d := range decisions {
		if d.Action == cold.TierMoved {
			index = d.Index
		}
	}
	if index == "" {
		t.Fatalf("decisions = %+v", decisions)
	}
	svc := &archive.Service{OS: m.os, Config: m.cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now}
	team := m.cfg.DefaultTeam
	who := archive.Audit{By: "jin", Reason: "e2e"}
	hidden := func() bool { return len(searchPaths(t, m.cfg, "WrongDecisionToken", false)) == 0 }

	if _, err := svc.Deprecate(ctx, team, doc, "", archive.Audit{}); err == nil {
		t.Error("감사 필드 없이는 거부한다")
	}
	if _, err := svc.Deprecate(ctx, team, doc, "", who); err != nil {
		t.Fatal(err)
	}
	if _, err := cold.Restore(ctx, m.os, m.cfg, index); err != nil {
		t.Fatal(err)
	}
	if !hidden() {
		t.Error("복원해도 deprecate 표시가 다시 입혀져 기본 검색에서 빠진다")
	}
	if got := searchPaths(t, m.cfg, "WrongDecisionToken", true); len(got) != 1 {
		t.Errorf("include_superseded 로는 보인다: %v", got)
	}
	if _, err := svc.Undeprecate(ctx, team, doc, who); err != nil {
		t.Fatal(err)
	}
	if hidden() {
		t.Error("undeprecate 후 다시 보인다")
	}

	edited := filepath.Join(t.TempDir(), "edited.md")
	if err := os.WriteFile(edited, []byte("---\nrepo: billing\ndate: "+date+"\n---\n# ADR-0001 옛 결정\nFixedDecisionToken 을 쓴다.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := svc.Edit(ctx, team, doc, edited, who); err != nil {
		t.Fatalf("edit = %s %v", out, err)
	}
	if got := searchPaths(t, m.cfg, "FixedDecisionToken", false); len(got) != 1 || !hidden() {
		t.Errorf("edit 후 새 본문만 = %v", got)
	}
	if raw, ok, _ := m.store.Get(ctx, cold.RawKey(team, doc)); !ok || !strings.Contains(raw, "FixedDecisionToken") {
		t.Error("cold store raw 도 바뀐다")
	}
	moved := filepath.Join(t.TempDir(), "moved.md")
	_ = os.WriteFile(moved, []byte("---\ndate: "+daysAgo(1)+"\n---\n# x\ny"), 0o644)
	if _, err := svc.Edit(ctx, team, doc, moved, who); err == nil || !strings.Contains(err.Error(), "밖입니다") {
		t.Errorf("다른 달로 가는 edit 은 거부: %v", err)
	}

	if out, err := svc.Purge(ctx, team, doc, who); err != nil {
		t.Fatalf("purge = %s %v", out, err)
	}
	if got := searchPaths(t, m.cfg, "FixedDecisionToken", true); len(got) != 0 {
		t.Errorf("purge 후 어디서도 안 보인다: %v", got)
	}
	if _, ok, _ := m.store.Get(ctx, cold.RawKey(team, doc)); ok {
		t.Error("purge 는 raw 도 지운다")
	}
	snaps, err := cold.SnapshotsOf(ctx, m.os, m.cfg, index)
	if err != nil || len(snaps) != 1 {
		t.Errorf("스냅샷은 다시 쓴 하나만 남는다: %+v %v", snaps, err)
	}
	ledger, err := os.ReadFile(filepath.Join(root, ".cache/archive", team+".jsonl"))
	if err != nil || strings.Count(string(ledger), "\n") != 4 {
		t.Errorf("감사 기록 4줄(deprecate·undeprecate·edit·purge) = %q %v", ledger, err)
	}
}

// TestOrgAliasSearchesEveryTeamOfTheOrg 는 MEMORY_TEAM_ORGS 로 묶인 팀들을 org("실") alias 하나로 함께 찾는 흐름이다.
func TestOrgAliasSearchesEveryTeamOfTheOrg(t *testing.T) {
	probe := newConfig(t, nil)
	teamA, teamB, org := probe.DefaultTeam, probe.DefaultTeam+"-b", "o"+probe.DefaultTeam
	cfg := newConfig(t, map[string]string{
		"MEMORY_DEFAULT_TEAM": teamA,
		"MEMORY_WORKSPACE":    probe.Workspace,
		"MEMORY_TEAM_ORGS":    teamA + "=" + org + "," + teamB + "=" + org,
	})
	m := newMCP(t, cfg)
	t.Cleanup(func() { _ = m.os.Do(context.Background(), "DELETE", "/mem-"+teamB+"-*", nil, nil, nil) })
	ctx := context.Background()
	write(t, cfg.MetaRepoRoot, "docs/specs/a.md", "# A 팀 문서\nSharedOrgToken A")
	write(t, cfg.MetaRepoRoot, "docs/specs/b.md", "---\nteam: "+teamB+"\n---\n# B 팀 문서\nSharedOrgToken B")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	byTeamRes, err := search.Search(ctx, m.os, search.Request{Workspace: cfg.Workspace, Team: teamA, Query: "SharedOrgToken"})
	byTeam := byTeamRes.Hits
	if err != nil || len(byTeam) != 1 {
		t.Errorf("팀 검색은 자기 팀만 = %v %v", paths(byTeam), err)
	}
	byOrgRes, err := search.Search(ctx, m.os, search.Request{Workspace: cfg.Workspace, Org: org, Query: "SharedOrgToken"})
	byOrg := byOrgRes.Hits
	if err != nil || len(byOrg) != 2 {
		t.Errorf("org 검색은 두 팀 모두 = %v %v", paths(byOrg), err)
	}
	if text, _ := m.call(t, "search_memory", map[string]any{"query": "SharedOrgToken", "org": org}); !strings.Contains(text, "docs/specs/b.md") {
		t.Errorf("search_memory org = %s", text)
	}
}

// TestSessionContextHookInjectsRepoHistory 는 SessionStart 훅이 main/{repo} 에서 열린 세션에 그 repo 의 문서를 붙이는 흐름이다.
func TestSessionContextHookInjectsRepoHistory(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	root := m.cfg.MetaRepoRoot
	write(t, root, "main/books-guitar/docs/adr/0001-storage.md", "# ADR-0001 저장소\nChordStorageToken 은 SQLite 에 둔다.")
	if err := os.MkdirAll(filepath.Join(root, "main/books-guitar/.worktrees/fix-sync/src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.indexer.IngestPath(ctx, "main/books-guitar/docs", ""); err != nil {
		t.Fatal(err)
	}
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: m.cfg, OS: m.os, Cold: m.store, Indexer: m.indexer, Log: t.Logf})
	run := func(cwd string) string {
		var out bytes.Buffer
		cli := &ops.CLI{Config: m.cfg, OS: m.os, Cold: m.store, Indexer: m.indexer, Handlers: h, Out: &out,
			In: strings.NewReader(fmt.Sprintf(`{"cwd":%q,"source":"startup"}`, cwd)), Now: time.Now}
		if err := cli.Run(ctx, "session-context", nil); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	out := run(filepath.Join(root, "main/books-guitar/.worktrees/fix-sync/src"))
	if !strings.Contains(out, "main/books-guitar/docs/adr/0001-storage.md") {
		t.Errorf("worktree 안에서도 repo 의 문서가 붙는다:\n%s", out)
	}
	if out := run(t.TempDir()); strings.TrimSpace(out) != "" {
		t.Errorf("meta-repo 밖은 조용하다: %q", out)
	}
}
