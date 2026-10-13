//go:build e2e

package e2e

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
)

func brokenLinks(rep audit.Report) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Kind == audit.BrokenLink {
			out = append(out, f.Path+": "+f.Detail)
		}
	}
	sort.Strings(out)
	return out
}

// TestAuditAndGraphKeepLinksToFilesOutsideIndexScope 는 README 가 디스크에 있는 색인 범위 밖 가이드(scenarios/·.claude/·examples/)를
// 링크해도 감사가 broken-link 로 보지 않고, 그래프도 그 대상을 unindexed(깨진 링크 후보)가 아닌 out-of-scope 로 두는지 본다 (DOC-1).
// 디스크에도 hot 에도 cold 에도 없는 대상만 깨진 링크다.
func TestAuditAndGraphKeepLinksToFilesOutsideIndexScope(t *testing.T) {
	cfg := newConfig(t, nil)
	g := newGraph(t, cfg.Graph)
	m := newMCP(t, cfg)
	ctx := context.Background()
	root := cfg.MetaRepoRoot
	write(t, root, "README.md", "# 안내\n[시나리오](./scenarios/SCENARIOS.md) · [compaction](./.claude/commands/compaction.md) · "+
		"[샘플](./examples/sample-workspace/README.md) · [없는 문서](./scenarios/missing.md)\n")
	write(t, root, "scenarios/SCENARIOS.md", "# 시나리오\n")
	write(t, root, ".claude/commands/compaction.md", "# compaction\n")
	write(t, root, "examples/sample-workspace/README.md", "# 샘플\n")
	if _, err := m.indexer.IngestPath(ctx, "README.md", ""); err != nil {
		t.Fatal(err)
	}

	rep, err := audit.Run(ctx, audit.Deps{OS: m.os, Config: cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := brokenLinks(rep); len(got) != 1 || !strings.Contains(got[0], "scenarios/missing.md") {
		t.Errorf("broken-link 은 없는 문서 하나뿐이다: %v", got)
	}

	indices, err := ingest.ManagedIndices(ctx, m.os)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := ingest.FetchIndexedDocs(ctx, m.os, indices, cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Rebuild(ctx, g, root, cfg.Workspace, docs); err != nil {
		t.Fatal(err)
	}
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: m.os, Graph: g, Log: t.Logf})
	out, err := h.Related(ctx, mcpserver.RelatedInput{Path: "README.md", Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"scenarios/SCENARIOS.md (out-of-scope)", ".claude/commands/compaction.md (out-of-scope)",
		"examples/sample-workspace/README.md (out-of-scope)", "scenarios/missing.md (unindexed)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("related 에 %q 가 없다:\n%s", want, out)
		}
	}
}

// copyRepoDocs 는 이 레포의 마크다운 전부(main/·.cache/·app/ 밖)를 root 로 같은 경로에 복사한다.
func copyRepoDocs(t *testing.T, root string) {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{"main": true, ".cache": true, "app": true, ".git": true, ".worktrees": true, "node_modules": true}
	err = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, p)
		if d.IsDir() && skip[rel] {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".md" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		write(t, root, filepath.ToSlash(rel), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAuditFindsNoBrokenLinksInRepoDocs 는 이 레포의 실제 문서(README·GUIDE·CLAUDE·INDEX·docs/, 그리고 그들이 링크하는
// scenarios/·.claude/·examples/ 의 가이드)를 사본 meta-repo 에 색인해 감사를 돌린다 — 실제 workspace 의 주간 감사가
// README·GUIDE 의 링크 때문에 '이상 없음' 이 될 수 없던 일(DOC-1)이 다시 생기지 않게.
func TestAuditFindsNoBrokenLinksInRepoDocs(t *testing.T) {
	m := newMCP(t, newConfig(t, nil))
	ctx := context.Background()
	copyRepoDocs(t, m.cfg.MetaRepoRoot)
	if _, err := m.indexer.IngestPath(ctx, ".", ""); err != nil {
		t.Fatal(err)
	}

	rep, err := audit.Run(ctx, audit.Deps{OS: m.os, Config: m.cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := brokenLinks(rep); len(got) != 0 {
		t.Errorf("레포 문서의 broken-link:\n%s", strings.Join(got, "\n"))
	}
}

// TestAuditReportsWrongCaseLinkToAHotDoc 는 철자(대소문자)만 다른 링크를 broken-link 로 알리는지 본다 — GitHub 에서 깨진다.
// 대소문자를 구분하지 않는 디스크(macOS APFS 기본)에서는 적재가 남긴 hot 문서의 raw 사본이 로컬 cold store 에서 그 철자로도
// 열려, 예전 감사는 cold 로 간 문서로 보고 넘어갔다. 대소문자를 구분하는 디스크에서는 cold 조회가 실패해 원래 보고됐다.
func TestAuditReportsWrongCaseLinkToAHotDoc(t *testing.T) {
	cfg := newConfig(t, nil)
	m := newMCP(t, cfg)
	ctx := context.Background()
	root := cfg.MetaRepoRoot
	write(t, root, "docs/knowledge/status-codes-v2.md", "# 상태 코드\n\n## 출처\n- 운영 회의\n")
	write(t, root, "docs/feature/INDEX.md", "# feature\n[상태 코드](../knowledge/Status-Codes-V2.md) · [정상](../knowledge/status-codes-v2.md)\n")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}

	rep, err := audit.Run(ctx, audit.Deps{OS: m.os, Config: cfg, Cold: m.store, Indexer: m.indexer, Now: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := brokenLinks(rep)
	if len(got) != 1 || !strings.Contains(got[0], "docs/feature/INDEX.md: LINKS_TO → docs/knowledge/Status-Codes-V2.md") {
		t.Errorf("철자만 다른 링크 하나가 broken-link 다: %v", got)
	}
}
