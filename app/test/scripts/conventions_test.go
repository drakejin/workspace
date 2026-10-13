package scripts

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// sourceOnlyHelpers 는 run.sh 도 _*.sh 도 아닌, source 로만 쓰는 공용 헬퍼다 (README 의 스크립트 컨벤션에 따로 적혀 있다).
var sourceOnlyHelpers = map[string]bool{"memory-common.sh": true}

// FILE-8 / DOC-18: scripts/ 의 .sh 는 진입점 run.sh 이거나 내부 스크립트 _*.sh 다 (README 컨벤션).
func TestScriptsFollowEntryOrInternalNaming(t *testing.T) {
	root := filepath.Join(repoRoot(t), "scripts")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".sh" {
			return err
		}
		base := d.Name()
		if base != "run.sh" && !strings.HasPrefix(base, "_") && !sourceOnlyHelpers[base] {
			rel, _ := filepath.Rel(repoRoot(t), p)
			t.Errorf("%s: 진입점은 run.sh, 내부 스크립트는 _*.sh 로 이름 짓는다", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// FILE-8: 이름을 바꾼 clone 스크립트를 _clone_repos.sh 가 그대로 부른다 (이미 있는 repo 는 네트워크 없이 [skip]).
func TestCloneReposCallsCloneScript(t *testing.T) {
	sb := newSandbox(t, "jq")
	sb.copyTo(sb.dir, "scripts/init")
	repos := filepath.Join(sb.dir, "main")
	if err := os.MkdirAll(filepath.Join(repos, "x", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(sb.dir, "seed.json")
	writeFile(t, seed, `{"workspaces": [{"name": "x"}]}`)

	r := sb.mustBash(nil, filepath.Join(sb.dir, "scripts", "init", "_clone_repos.sh"), "github.invalid", "org", repos, seed)
	if !strings.Contains(r.stdout, "[skip] x: already exists") || !strings.Contains(r.stdout, "1 skipped / 0 failed") {
		t.Errorf("_clone_repos.sh 출력 = %q", r.output())
	}
}

// makeCommand 는 주석·메시지에서 make 로 타깃을 부르는 자리다. 영어 산문("make sure")은 거르고 명령으로 쓴 자리만 본다:
// 코드 표기(`make x …`), 따옴표·괄호로 감싼 명령('make x', "make x", (make x)), 명령 줄의 make(줄 머리, 프롬프트 "$ ",
// &&·||·;·| 뒤). 한국어 문장 속 명령도 이 중 하나로 쓴다.
var makeCommand = regexp.MustCompile("`make ([a-z][a-z0-9-]*)[^`]*`" +
	`|'make ([a-z][a-z0-9-]*)'|"make ([a-z][a-z0-9-]*)"|\(make ([a-z][a-z0-9-]*)\)` +
	`|(?:^\s*|\$ |&&\s*|\|\|?\s*|;\s*)make ([a-z][a-z0-9-]*)`)
var makefileTarget = regexp.MustCompile(`^([a-z][a-z0-9-]*):`)

// makeTargetsIn 은 line 에서 make 로 부르는 타깃들이다 (makeCommand 의 자리마다 하나).
func makeTargetsIn(line string) []string {
	var out []string
	for _, m := range makeCommand.FindAllStringSubmatch(line, -1) {
		for _, target := range m[1:] {
			if target != "" {
				out = append(out, target)
				break
			}
		}
	}
	return out
}

// FILE-6 / FILE-7 / DEAD-15: 스크립트·예제 주석이 가리키는 make 타깃은 실제로 있어야 하고,
// 없어진 memory-up(지금은 make memory-init)을 가리키지 않는다.
func TestCommentsNameLiveCommands(t *testing.T) {
	root := repoRoot(t)
	targets := map[string]bool{}
	for _, mk := range []string{"Makefile", "app/Makefile"} {
		for _, line := range readLines(t, filepath.Join(root, mk)) {
			if m := makefileTarget.FindStringSubmatch(line); m != nil {
				targets[m[1]] = true
			}
		}
	}
	checked := 0
	for _, rel := range commentFiles(t, root) {
		for n, line := range readLines(t, filepath.Join(root, rel)) {
			for _, target := range makeTargetsIn(line) {
				checked++
				if !targets[target] {
					t.Errorf("%s:%d: 없는 타깃 'make %s'", rel, n+1, target)
				}
			}
			if strings.Contains(line, "memory-up") {
				t.Errorf("%s:%d: 없어진 memory-up 을 가리킨다 (make memory-init)", rel, n+1)
			}
		}
	}
	if checked == 0 {
		t.Error("make 타깃을 하나도 찾지 못했다 — 검사가 헛돈다")
	}
}

// R1-shell-4: 주석·메시지의 영어 산문("make sure")은 타깃으로 보지 않는다 — 명령으로 쓴 자리만 본다.
func TestMakeTargetsInCommandContextsOnly(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"# make sure the stack is up before running", nil},
		{"# Please make sure that OpenSearch answers", nil},
		{"echo \"make sure docker is running\"", nil},
		{"# (make sure it is up) 먼저", nil},
		{"# 스택 종료 (make memory-init) — 다시 만든다", []string{"memory-init"}},
		{"echo \"다음: 'make memory-daemon-start' 로 데몬을 상주시키세요\"", []string{"memory-daemon-start"}},
		{"# 먼저 `make memory-daemon-stop` 을 하세요", []string{"memory-daemon-stop"}},
		{"# 코드 표기 안의 인자도 된다: `make memory-init FOO=1`", []string{"memory-init"}},
		{"#   $ make memory-init", []string{"memory-init"}},
		{"make memory-init", []string{"memory-init"}},
		{"    cd app && make test-e2e", []string{"test-e2e"}},
		{"build; make lint || make fmt", []string{"lint", "fmt"}},
		{"# remake things, automake it", nil},
	}
	for _, tc := range cases {
		if got := makeTargetsIn(tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("makeTargetsIn(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// scriptAlias 는 scripts/{기능}/run.sh 를 그대로 부르는 Makefile 레시피 줄이다.
var scriptAlias = regexp.MustCompile(`^\t@?bash \.\./scripts/[^/\s]+/run\.sh`)

// FILE-14: app/Makefile 은 Go 개발 타깃(build·test*)만 둔다. scripts/*/run.sh 를 다시 감싼 별칭(예전 mcp)은 아무도 부르지 않는
// 두 번째 진입점이다 (.mcp.json 은 scripts/mcp/run.sh 를 직접 부른다).
func TestAppMakefileHasNoScriptAliases(t *testing.T) {
	for n, line := range readLines(t, filepath.Join(repoRoot(t), "app", "Makefile")) {
		if scriptAlias.MatchString(line) {
			t.Errorf("app/Makefile:%d: 스크립트 별칭 %q — 진입점(scripts/*/run.sh)을 직접 부른다", n+1, strings.TrimSpace(line))
		}
	}
}

// commentFiles 는 주석을 검사할 파일이다: scripts/ 전부, scenarios/*.sh, examples/*.yaml.
func commentFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(root, "scripts"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, glob := range []string{"scenarios/*.sh", "examples/*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			out = append(out, rel)
		}
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
