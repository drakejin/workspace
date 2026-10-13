package audit

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
)

const (
	unitTeam         = "unit"
	unitMaxFileBytes = 1 << 20
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newUnitRunner 는 OpenSearch 없이 도는 감사 실행기다 — 디스크 스캔과 로컬 cold store, 주어진 색인 목록만 본다.
func newUnitRunner(t *testing.T, root string, store cold.Store, docs map[string]ingest.IndexedDoc) *runner {
	t.Helper()
	files, _, err := ingest.ScanHot(root, unitMaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{
		Deps: Deps{Config: config.Config{MetaRepoRoot: root, DefaultTeam: unitTeam}, Cold: store},
		docs: docs, files: make(map[string]ingest.ScannedFile, len(files)),
	}
	for _, f := range files {
		r.files[f.Rel] = f
	}
	return r
}

var brokenTarget = regexp.MustCompile(`→ (\S+)`)

func brokenTargets(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		if f.Kind != BrokenLink {
			continue
		}
		if m := brokenTarget.FindStringSubmatch(f.Detail); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// DOC-1: 링크 대상이 디스크에 있으면 색인 범위 밖(scenarios/·.claude/·examples/)이어도 깨진 링크가 아니다.
// 깨진 링크는 디스크에도, hot 에도, cold 에도 없는 대상뿐이다 — 철자(대소문자)가 다른 링크는 GitHub 에서 깨지므로 그대로 알린다.
func TestLinksToFilesOutsideIndexScopeAreNotBroken(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "README.md", "# 안내\n"+
		"[시나리오](./scenarios/SCENARIOS.md) · [compaction](./.claude/commands/compaction.md) · [샘플](./examples/sample-workspace/README.md)\n"+
		"[없음](./scenarios/missing.md) · [철자 틀림](./docs/specs/Plan.md) · [cold](./docs/specs/old.md) · [hot](./docs/specs/plan.md)\n")
	writeFile(t, root, "scenarios/SCENARIOS.md", "# 시나리오\n")
	writeFile(t, root, ".claude/commands/compaction.md", "# compaction\n")
	writeFile(t, root, "examples/sample-workspace/README.md", "# 샘플\n")
	writeFile(t, root, "docs/specs/plan.md", "# plan\n")
	store := cold.NewLocalStore(t.TempDir())
	if err := store.Put(ctx, cold.RawKey(unitTeam, "docs/specs/old.md"), "# old\n"); err != nil {
		t.Fatal(err)
	}
	r := newUnitRunner(t, root, store, map[string]ingest.IndexedDoc{})

	if err := r.links(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{"docs/specs/Plan.md", "scenarios/missing.md"}
	if got := brokenTargets(r.findings); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("broken-link 대상\n got  %v\n want %v", got, want)
	}
}

// requireCaseInsensitiveFS 는 대소문자를 구분하지 않는 파일 시스템(macOS APFS 기본)에서만 성립하는 테스트다.
func requireCaseInsensitiveFS(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(probe) }()
	if _, err := os.Stat(filepath.Join(dir, "CASE-PROBE")); err != nil {
		t.Skip("대소문자를 구분하는 파일 시스템 — 옛 철자가 같은 파일을 열지 않는다")
	}
}

// R1-paths-2: 옛 철자(대소문자)로 색인에 남은 문서는 orphan-index 로 알리되(조치는 같다 — 색인에서 뺀다),
// 파일이 없다고 하지 않고 디스크의 철자를 알려 준다. 정말 파일이 없는 문서는 예전 문구 그대로다.
func TestOrphanIndexNamesTheOnDiskSpellingOfALegacyEntry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	requireCaseInsensitiveFS(t, root)
	writeFile(t, root, "docs/knowledge/status-codes-v2.md", "# 상태 코드\n")
	docs := map[string]ingest.IndexedDoc{
		"docs/knowledge/STATUS-CODES-V2.md": {SourcePath: "docs/knowledge/STATUS-CODES-V2.md", Index: "mem-unit-knowledge"},
		"docs/knowledge/gone.md":            {SourcePath: "docs/knowledge/gone.md", Index: "mem-unit-knowledge"},
	}
	r := newUnitRunner(t, root, cold.NewLocalStore(t.TempDir()), docs)

	if err := r.orphanIndex(ctx); err != nil {
		t.Fatal(err)
	}

	details := map[string]string{}
	for _, f := range r.findings {
		if f.Kind != OrphanIndex {
			t.Errorf("종류 = %s (orphan-index 여야 한다)", f.Kind)
		}
		details[f.Path] = f.Detail
	}
	legacy := details["docs/knowledge/STATUS-CODES-V2.md"]
	if strings.Contains(legacy, "파일이 없다") || !strings.Contains(legacy, "docs/knowledge/status-codes-v2.md") {
		t.Errorf("옛 철자 문구 = %q (디스크의 철자를 알려야 한다)", legacy)
	}
	if gone := details["docs/knowledge/gone.md"]; !strings.Contains(gone, "파일이 없다") {
		t.Errorf("없는 파일 문구 = %q", gone)
	}
}
