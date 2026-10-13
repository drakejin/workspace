package scripts

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
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

var makeTarget = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])make ([a-z][a-z0-9-]*)`)
var makefileTarget = regexp.MustCompile(`^([a-z][a-z0-9-]*):`)

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
	for _, rel := range commentFiles(t, root) {
		for n, line := range readLines(t, filepath.Join(root, rel)) {
			for _, m := range makeTarget.FindAllStringSubmatch(line, -1) {
				if !targets[m[1]] {
					t.Errorf("%s:%d: 없는 타깃 'make %s'", rel, n+1, m[1])
				}
			}
			if strings.Contains(line, "memory-up") {
				t.Errorf("%s:%d: 없어진 memory-up 을 가리킨다 (make memory-init)", rel, n+1)
			}
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
