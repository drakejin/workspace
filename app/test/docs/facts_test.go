package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/ops"
)

// fact 는 문서 하나가 지켜야 하는 문구다 — 있어야 할 것과 없어야 할 것.
type fact struct {
	id      string
	file    string
	must    []string
	mustNot []string
}

func checkFacts(t *testing.T, facts []fact) {
	t.Helper()
	for _, f := range facts {
		content := readDoc(t, f.file)
		for _, s := range f.must {
			if !strings.Contains(content, s) {
				t.Errorf("%s: %s 에 %q 가 없다", f.id, f.file, s)
			}
		}
		for _, s := range f.mustNot {
			if strings.Contains(content, s) {
				t.Errorf("%s: %s 에 %q 가 남아 있다", f.id, f.file, s)
			}
		}
	}
}

// lineWith 는 prefix 로 시작하는 첫 줄이다 (없으면 실패).
func lineWith(t *testing.T, content, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return line
		}
	}
	t.Fatalf("%q 로 시작하는 줄이 없다", prefix)
	return ""
}

// DOC-12 · DOC-13 · DOC-14 · DOC-17 · FILE-11 · DOC-21 · FILE-4 · FILE-12: 문구가 레포·코드와 맞는다.
func TestDocWording(t *testing.T) {
	checkFacts(t, []fact{
		{id: "DOC-12", file: "README.md",
			must:    []string{"발표의 넷(`search_memory`·`ingest_path`·`list_cold`·`restore_cold`)"},
			mustNot: []string{"앞의 넷"}},
		{id: "DOC-13", file: "GUIDE.md", must: []string{"\ntopic: "}},
		{id: "DOC-17 · FILE-11", file: "CLAUDE.md", must: []string{"## 에피소드 기록 (다섯 줄)"}, mustNot: []string{"(머지 전 세 줄)"}},
		{id: "DOC-17 · FILE-11", file: "README.md", must: []string{"](./CLAUDE.md#에피소드-기록-다섯-줄)"}},
		{id: "DOC-21 · FILE-4", file: "INDEX.md", mustNot: []string{"| **books-guitar** | - | - | - |"}},
		{id: "DOC-21 · FILE-12", file: "docs/feature/INDEX.md", mustNot: []string{"어트리뷰션"}},
		{id: "DOC-21", file: "docs/issues/INDEX.md", mustNot: []string{"{issue}/README.md", "함께 쓰고"}},
	})
	readme := readDoc(t, "README.md")
	for _, c := range []struct {
		id, prefix string
		want       []string
	}{
		{"DOC-13 frontmatter 키", "frontmatter(`team`", []string{"`topic`", "`project`"}},
		{"DOC-13 청크 필드", "청크 한 건의 주요 필드:", []string{"`topic`", "`event_day`"}},
	} {
		line := lineWith(t, readme, c.prefix)
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("%s: README 줄 %q 에 %s 가 없다", c.id, line, w)
			}
		}
	}
	step3 := lineWith(t, readDoc(t, ".claude/commands/compaction.md"), "3. **분리**")
	for _, w := range []string{"derived_from", "## 출처"} {
		if !strings.Contains(step3, w) {
			t.Errorf("DOC-14: compaction 3단계가 근거(%s)를 남기라고 하지 않는다: %q", w, step3)
		}
	}
}

// koreanCount 는 문서가 도구 수를 적는 말이다.
var koreanCount = map[int]string{4: "넷", 5: "다섯", 6: "여섯", 7: "일곱", 8: "여덟", 9: "아홉"}

// toolCount 는 memory-mcp 가 실제로 노출하는 도구 수다 (메모리 안 전송으로 tools/list).
func toolCount(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := mcpserver.NewServer(mcpserver.NewHandlers(mcpserver.Deps{})).Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "docs", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Tools)
}

var sampleDocs = regexp.MustCompile(`측정 당시 샘플 (\d+) 문서`)

// benchSampleDocs 는 2026-10-13 벤치를 잰 커밋의 examples/sample-workspace 마크다운 수다 (git ls-tree 로 확인).
// 측정 기록이라 샘플이 늘어도 바꾸지 않는다 — 지금 샘플 수와 비교하면 문서를 더할 때마다 옛 측정을 고쳐 쓰게 된다.
const benchSampleDocs = 14

// DOC-8 · DOC-9: 설계 배경 문서의 도구 수는 레포와 같고, 측정 근거의 샘플 문서 수는 잰 때의 수다.
func TestTalkAlignmentCountsMatchTheRepo(t *testing.T) {
	talk := readDoc(t, "docs/feature/memory-layer/talk-alignment.md")
	tools := toolCount(t)
	summary := lineWith(t, talk, "| 발표 §12 기능·인덱스·도구 |")
	if !strings.Contains(summary, "= "+koreanCount[tools]) {
		t.Errorf("DOC-8: 요약표의 도구 수가 실제 %d개(%s)와 다르다: %q", tools, koreanCount[tools], summary)
	}
	if !strings.Contains(talk, "도구 "+koreanCount[tools]+" 개") {
		t.Errorf("DOC-8: Q&A 의 도구 수가 실제 %d개와 다르다", tools)
	}
	if want := fmt.Sprintf("MCP 도구 %d개", tools); !strings.Contains(readDoc(t, "GUIDE.md"), want) {
		t.Errorf("DOC-8: GUIDE §10 에 %q 가 없다", want)
	}
	matches := sampleDocs.FindAllStringSubmatch(talk, -1)
	if len(matches) == 0 || strings.Contains(talk, "15 문서") {
		t.Errorf("DOC-9: 측정 근거가 그때의 샘플 문서 수(측정 당시 샘플 %d 문서)를 적지 않거나 옛 수(15 문서)가 남았다", benchSampleDocs)
	}
	for _, m := range matches {
		if m[1] != fmt.Sprint(benchSampleDocs) {
			t.Errorf("DOC-9: %q — 잰 때의 샘플은 %d 문서다", m[0], benchSampleDocs)
		}
	}
}

// DOC-10: Go 주석이 없어진 명령(daemon-status — 지금은 scripts/memory-daemon/run.sh status)을 가리키지 않는다.
func TestGoCommentsNameLiveCommands(t *testing.T) {
	root := filepath.Join(repoRoot(t), "app")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".go" || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "daemon-status") {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("app/%s: 없어진 daemon-status 를 가리킨다 (scripts/memory-daemon/run.sh status)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// jsonKeys 는 JSON 값 안의 모든 객체 키다.
func jsonKeys(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			out[k] = true
			jsonKeys(child, out)
		}
	case []any:
		for _, child := range x {
			jsonKeys(child, out)
		}
	}
}

// readmeSeedExample 은 README §7.1 의 workspace.seed.json 예시다 (그 절의 첫 json 코드 블록).
func readmeSeedExample(t *testing.T) string {
	t.Helper()
	readme := readDoc(t, "README.md")
	_, after, ok := strings.Cut(readme, "### 7.1 workspace.seed.json")
	if !ok {
		t.Fatal("README 에 §7.1 workspace.seed.json 이 없다")
	}
	_, block, ok := strings.Cut(after, "```json\n")
	if !ok {
		t.Fatal("README §7.1 에 json 예시가 없다")
	}
	body, _, _ := strings.Cut(block, "```")
	return body
}

// DOC-11 · OPS-15: workspace.seed.json(과 README 의 예시)의 키는 모두 scripts/ 가 읽는다 — 문서에만 있는 설정이 없다.
func TestSeedKeysAreReadByScripts(t *testing.T) {
	keys := map[string]bool{}
	for name, raw := range map[string]string{"workspace.seed.json": readDoc(t, "workspace.seed.json"), "README §7.1": readmeSeedExample(t)} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		jsonKeys(v, keys)
	}
	var scripts strings.Builder
	err := filepath.WalkDir(filepath.Join(repoRoot(t), "scripts"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".sh" {
			return err
		}
		b, err := os.ReadFile(p)
		scripts.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if !strings.Contains(scripts.String(), "."+k) {
			t.Errorf("seed 키 %q 를 읽는 스크립트가 없다", k)
		}
	}
}

// DOC-15: 감사가 내는 종류는 모두 GUIDE §6.3 표에 있다.
func TestAuditKindsAreDocumented(t *testing.T) {
	guide := readDoc(t, "GUIDE.md")
	for _, k := range []audit.Kind{
		audit.OrphanIndex, audit.Unindexed, audit.StaleContent, audit.OrphanChunks, audit.BrokenLink,
		audit.UnsourcedFact, audit.StaleQuestion, audit.CorruptedLedger, audit.PartialScan,
	} {
		if !strings.Contains(guide, "| `"+string(k)+"` |") {
			t.Errorf("GUIDE §6.3 감사 표에 %s 가 없다", k)
		}
	}
}

var (
	// 단어는 백틱(인라인 코드)에서 끊는다
	bm25Quote  = regexp.MustCompile("(?:[^\\s\x60]+ (?:\\d+|[NM])건 · )?[^\\s\x60]+ (?:\\d+|[NM])건 \\(BM25\\)")
	bm25Header = regexp.MustCompile(`^청크 (\d+|N)건 · 문서 (\d+|M)건 \(BM25\)$`)
)

// DOC-16 · API-7: 문서에 옮긴 질의어 검색 머리(일반형 포함)는 청크 수와 문서 수를 함께 밝힌다 ("청크 N건 · 문서 M건 (BM25)", N ≥ M).
func TestQuotedBM25HeadersCountChunks(t *testing.T) {
	quoted := 0
	for _, f := range []string{"README.md", "GUIDE.md", "scenarios/SCENARIOS.md"} {
		for _, q := range bm25Quote.FindAllString(readDoc(t, f), -1) {
			quoted++
			m := bm25Header.FindStringSubmatch(q)
			if m == nil {
				t.Errorf("%s: %q — 질의어 검색 머리는 \"청크 N건 · 문서 M건 (BM25)\" 다", f, q)
				continue
			}
			if m[1] == "N" || m[2] == "M" {
				continue // 일반형("청크 N건 · 문서 M건") — 수는 없다
			}
			if chunks, docs := atoi(t, m[1]), atoi(t, m[2]); chunks < docs {
				t.Errorf("%s: %q — 청크가 문서보다 적을 수 없다", f, q)
			}
		}
	}
	if quoted == 0 {
		t.Error("README·GUIDE·SCENARIOS 에 옮긴 질의어 검색 머리가 없다")
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var (
	usageLine = regexp.MustCompile(`^  (\S+)\s+(.*)$`)
	usageFlag = regexp.MustCompile(`(?:^|[\s\[|])(-[a-z][a-z0-9-]*)`)
)

// usageFlags 는 운영 CLI 사용법(help)의 명령별 플래그다 — 설명 앞(세 칸 공백 전)의 문법 부분만 본다.
func usageFlags() map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(ops.Usage(), "\n") {
		m := usageLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		syntax, _, _ := strings.Cut(m[2], "   ")
		for _, f := range usageFlag.FindAllStringSubmatch(syntax, -1) {
			out[m[1]] = append(out[m[1]], f[1])
		}
	}
	return out
}

// DOC-19 · API-14: README 의 scripts/memory/run.sh 명령 표는 help 의 플래그를 빠뜨리지 않고, help 는 bench 의 플래그를 다 보인다.
func TestReadmeCLIRowsListEveryFlag(t *testing.T) {
	flags := usageFlags()
	for _, want := range []string{"-max-turns", "-mcp-script"} {
		if !strings.Contains(strings.Join(flags["bench"], " "), want) {
			t.Errorf("help 의 bench 사용법에 %s 가 없다", want)
		}
	}
	readme := readDoc(t, "README.md")
	for cmd, fs := range flags {
		prefix := "| `scripts/memory/run.sh " + cmd + " "
		var row string
		for _, line := range strings.Split(readme, "\n") {
			if strings.HasPrefix(line, prefix) {
				row = line
				break
			}
		}
		if row == "" {
			continue // README 표에 없는 명령은 'scripts/memory/run.sh help' 로 안내한다
		}
		for _, f := range fs {
			if !strings.Contains(row, f+" ") && !strings.Contains(row, f+"]") {
				t.Errorf("README 의 %s 줄에 %s 가 없다", cmd, f)
			}
		}
	}
}

var allowedEnv = regexp.MustCompile(`local allowed='\^\(([^)]*)\)\$'`)

// R1-shell-3: GUIDE §8 의 데몬 문장은 launchd 잡에 실제로 담는 이름(memory_launchd_env_allowed)을 그대로 적는다.
func TestGuideDaemonEnvSentenceMatchesAllowlist(t *testing.T) {
	m := allowedEnv.FindStringSubmatch(readDoc(t, "scripts/memory-common.sh"))
	if m == nil {
		t.Fatal("memory-common.sh 에 memory_launchd_env_allowed 의 allowed 목록이 없다")
	}
	line := lineWith(t, readDoc(t, "GUIDE.md"), "`make memory-daemon-start` 로 띄운 데몬")
	for _, name := range strings.Split(m[1], "|") {
		name = strings.Replace(name, "[A-Z0-9_]+", "*", 1)
		if !strings.Contains(line, "`"+name+"`") {
			t.Errorf("GUIDE §8 데몬 문장에 %s 가 없다: %q", name, line)
		}
	}
	carried, dash, hasDash := strings.Cut(line, "`OS_DASH_PORT`")
	if hasDash && !strings.Contains(dash, "담지 않는다") {
		t.Errorf("GUIDE §8: OS_DASH_PORT 는 잡에 담지 않는다: %q", line)
	}
	// 재시작 안내는 잡에 담는 값에 붙는다 — 담지 않는 OS_DASH_PORT 절 뒤에 오면 "OS_DASH_PORT 를 바꾸면 재시작" 으로 읽힌다
	if !strings.Contains(carried, "make memory-daemon-stop && make memory-daemon-start") || strings.Contains(dash, "바꾸면") {
		t.Errorf("GUIDE §8: 재시작 안내가 잡에 담는 값 바로 뒤에 와야 한다: %q", line)
	}
}
