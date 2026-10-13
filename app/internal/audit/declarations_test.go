package audit

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// noHitsOS 는 검색마다 빈 결과를 돌려주는 OpenSearch 대신이다 (Prepare 가 색인의 표시를 물을 때).
type noHitsOS struct{}

func (noHitsOS) Do(_ context.Context, _, _ string, _ any, _ url.Values, out any) error {
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"hits":{"hits":[]}}`), out)
}

func (noHitsOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// newCheckingRunner 는 파일마다 Prepare 를 거치는(checkFile) 감사 실행기다 — 색인은 비어 있다.
func newCheckingRunner(t *testing.T, root string) *runner {
	t.Helper()
	store := cold.NewLocalStore(t.TempDir())
	r := newUnitRunner(t, root, store, map[string]ingest.IndexedDoc{})
	cfg := config.Config{MetaRepoRoot: root, Workspace: "ws", DefaultTeam: unitTeam, Reconcile: config.Reconcile{MaxFileBytes: unitMaxFileBytes}}
	r.Config = cfg
	r.Indexer = ingest.New(ingest.Deps{OS: noHitsOS{}, Cold: store, Config: cfg})
	r.owner = cold.NewOwnership(cold.Archive{}, store)
	return r
}

func findingsOf(findings []Finding, kinds ...Kind) map[string]string {
	want := map[Kind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := map[string]string{}
	for _, f := range findings {
		if want[f.Kind] {
			out[f.Path] = string(f.Kind) + ": " + f.Detail
		}
	}
	return out
}

// RUN-15: frontmatter YAML 이 깨진 문서(메타데이터 없이 원문 전체가 본문으로 색인된다)와 team 값이 규칙에 어긋난 문서
// (그 판은 색인되지 않아 고치기 전 판이 계속 검색된다)를 audit 가 bad-frontmatter 로 알린다.
func TestAuditReportsBadFrontmatter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "docs/specs/bad-yaml.md", "---\nteam: [unclosed\ntopic: broken\n---\n# Bad YAML\n본문\n")
	writeFile(t, root, "docs/repository/beta-web/adr/0001-ranking-cache.md", "---\nteam: Beta Web\n---\n# 랭킹 캐시\n")
	writeFile(t, root, "docs/specs/ok.md", "---\nteam: unit\n---\n# 정상\n")
	r := newCheckingRunner(t, root)

	if err := r.hotVsIndex(ctx); err != nil {
		t.Fatal(err)
	}

	got := findingsOf(r.findings, badFrontmatter)
	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{"docs/repository/beta-web/adr/0001-ranking-cache.md", "docs/specs/bad-yaml.md"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("bad-frontmatter = %v, want %v", got, want)
	}
	if d := got["docs/specs/bad-yaml.md"]; !strings.Contains(d, "YAML") {
		t.Errorf("깨진 YAML 문구 = %q", d)
	}
	if d := got["docs/repository/beta-web/adr/0001-ranking-cache.md"]; !strings.Contains(d, "team 이름이 잘못됐습니다") {
		t.Errorf("잘못된 team 문구 = %q", d)
	}
}

// RUN-17: frontmatter supersedes 가 순환(A↔B)하거나 자기 자신을 가리키면 수렴이 그 선언을 버린다 — 어느 쪽도 밀리지 않고
// 그래프에도 관계가 없다. audit 가 supersedes-cycle·supersedes-self 로 알린다. 이어진 사슬(A→B→C)은 정상이다.
func TestAuditReportsSupersedesCyclesAndSelfReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	decl := func(target string) string { return "---\nsupersedes: " + target + "\n---\n# 문서\n본문\n" }
	writeFile(t, root, "docs/specs/cyc/a.md", decl("docs/specs/cyc/b.md"))
	writeFile(t, root, "docs/specs/cyc/b.md", decl("docs/specs/cyc/a.md"))
	writeFile(t, root, "docs/specs/cyc/self.md", decl("docs/specs/cyc/self.md"))
	writeFile(t, root, "docs/specs/chain/v1.md", "# v1\n")
	writeFile(t, root, "docs/specs/chain/v2.md", decl("docs/specs/chain/v1.md"))
	writeFile(t, root, "docs/specs/chain/v3.md", decl("docs/specs/chain/v2.md"))
	r := newCheckingRunner(t, root)

	if err := r.hotVsIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.supersedesDeclarations(ctx); err != nil {
		t.Fatal(err)
	}

	got := findingsOf(r.findings, supersedesCycle, supersedesSelf)
	if len(got) != 2 {
		t.Fatalf("findings = %v (순환 하나·자기 참조 하나)", got)
	}
	if d := got["docs/specs/cyc/a.md"]; !strings.HasPrefix(d, string(supersedesCycle)) || !strings.Contains(d, "docs/specs/cyc/a.md → docs/specs/cyc/b.md → docs/specs/cyc/a.md") {
		t.Errorf("순환 = %q", d)
	}
	if d := got["docs/specs/cyc/self.md"]; !strings.HasPrefix(d, string(supersedesSelf)) {
		t.Errorf("자기 참조 = %q", d)
	}
}
