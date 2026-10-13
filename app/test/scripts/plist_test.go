package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// awkward 는 sed·XML 에서 특수한 글자를 모두 담은 경로 조각이다.
const awkward = `a&b<c>|d\e`

// awkwardWorkspace 는 awkward 디렉토리의 workspace 이름이다 (memory_workspace_name: 소문자, [a-z0-9-] 밖은 하이픈).
const awkwardWorkspace = "a-b-c-d-e"

// tierLabel 은 workspace 의 tier 잡 이름이다 (데몬의 com.drakejin.memory.<workspace>.daemon 과 같은 규칙).
func tierLabel(workspace string) string { return "com.drakejin.memory." + workspace + ".tier" }

// 공용 렌더러: 값은 XML escape 해서 글자 그대로 넣고, __ENV__ 는 EnvironmentVariables 조각을 그대로 넣고, 그린 결과를 lint 한다.
func TestRenderPlistSubstitutesLiterally(t *testing.T) {
	requireDarwin(t)
	sb := newSandbox(t, "plutil")
	sb.copyTo(sb.dir, "scripts/memory-common.sh", "scripts/memory-daemon/daemon.plist.template")
	root := filepath.Join(sb.dir, awkward)
	logFile := filepath.Join(root, ".cache", "daemon", "daemon.log")
	out := filepath.Join(sb.dir, "out.plist")
	sb.mustBash(map[string]string{"MEMORY_TEAM_ORGS": "x=y&z", "ROOT": root, "LOGF": logFile, "OUT": out}, "-c", `
		source scripts/memory-common.sh
		memory_render_plist scripts/memory-daemon/daemon.plist.template "$OUT" "$(memory_launchd_env_xml)" \
			"LABEL=com.drakejin.memory.t.daemon" "ROOT=$ROOT" "LOG=$LOGF"`)

	p := plistJSON(t, out)
	assertPlist(t, p, "Label", "com.drakejin.memory.t.daemon")
	assertPlist(t, p, "WorkingDirectory", root)
	assertPlist(t, p, "StandardOutPath", logFile)
	args, _ := p["ProgramArguments"].([]any)
	if len(args) < 2 || args[1] != root+"/scripts/memory-daemon/run.sh" {
		t.Errorf("ProgramArguments = %v", args)
	}
	env, _ := p["EnvironmentVariables"].(map[string]any)
	if env["MEMORY_TEAM_ORGS"] != "x=y&z" {
		t.Errorf("EnvironmentVariables = %v", env)
	}
}

// 템플릿에 채우지 않은 자리표시자가 남으면 실패한다 (조용히 깨진 잡을 만들지 않는다).
func TestRenderPlistRejectsUnfilledPlaceholder(t *testing.T) {
	requireDarwin(t)
	sb := newSandbox(t, "plutil")
	sb.copyTo(sb.dir, "scripts/memory-common.sh", "scripts/memory-daemon/daemon.plist.template")
	r := sb.bash(nil, "-c", `
		source scripts/memory-common.sh
		memory_render_plist scripts/memory-daemon/daemon.plist.template out.plist "" "LABEL=l" "ROOT=/r"`)
	if r.err == nil {
		t.Fatalf("__LOG__ 를 채우지 않았는데 성공했다:\n%s", r.output())
	}
	if !strings.Contains(r.output(), "__LOG__") {
		t.Errorf("오류에 빠진 자리표시자 이름이 없다: %s", r.output())
	}
}

// tierSandbox 는 meta-repo(<임시>/<dirName>)에 schedule 스크립트를 두고, launchctl 을 가짜로 바꾼다.
func tierSandbox(t *testing.T, dirName string) (sb *sandbox, root, launchctlLog string) {
	t.Helper()
	requireDarwin(t)
	sb = newSandbox(t, "plutil")
	root = filepath.Join(sb.dir, dirName)
	sb.copyTo(root, "scripts/memory-common.sh", "scripts/schedule")
	return sb, root, sb.stub("launchctl", "exit 0")
}

// tierPlist 는 workspace 의 tier 잡 plist 경로다.
func (sb *sandbox) tierPlist(workspace string) string {
	return sb.agentPlist(tierLabel(workspace))
}

// agentPlist 는 가짜 HOME 의 LaunchAgents 에 놓이는 label 의 plist 경로다.
func (sb *sandbox) agentPlist(label string) string {
	return filepath.Join(sb.home, "Library", "LaunchAgents", label+".plist")
}

// OPS-7: --dry-run 이 그린 plist 는 경로를 글자 그대로 담는다 (sed 가 & 를 매치 문자열로 바꾸지 않는다). 설치는 하지 않는다.
func TestScheduleDryRunKeepsAwkwardPath(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, awkward)
	r := sb.mustBash(nil, filepath.Join(root, "scripts", "schedule", "run.sh"), "install", "--dry-run")
	rendered := filepath.Join(sb.dir, "dry-run.plist")
	writeFile(t, rendered, r.stdout)

	p := plistJSON(t, rendered)
	assertPlist(t, p, "Label", tierLabel(awkwardWorkspace))
	assertPlist(t, p, "WorkingDirectory", root)
	assertPlist(t, p, "StandardOutPath", root+"/.cache/schedule/tier.log")
	args, _ := p["ProgramArguments"].([]any)
	if len(args) < 3 || args[1] != root+"/scripts/memory/run.sh" || args[2] != "tier-tick" {
		t.Errorf("ProgramArguments = %v", args)
	}
	if env, _ := p["EnvironmentVariables"].(map[string]any); env["PATH"] == "" || env["PATH"] == nil {
		t.Errorf("EnvironmentVariables.PATH 가 없다: %v", p["EnvironmentVariables"])
	}
	if exists(sb.tierPlist(awkwardWorkspace)) || len(calls(t, launchctlLog)) > 0 {
		t.Errorf("--dry-run 이 설치했다 (launchctl %v)", calls(t, launchctlLog))
	}
}

// OPS-7: install 은 임시 파일에 그려 lint 한 뒤 제자리로 옮겨 load 한다.
func TestScheduleInstallPlacesLintedPlist(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, awkward)
	sb.mustBash(nil, filepath.Join(root, "scripts", "schedule", "run.sh"), "install")

	assertPlist(t, plistJSON(t, sb.tierPlist(awkwardWorkspace)), "WorkingDirectory", root)
	got := calls(t, launchctlLog)
	if len(got) == 0 || got[len(got)-1] != "load "+sb.tierPlist(awkwardWorkspace) {
		t.Errorf("launchctl 호출 = %v, 마지막이 load 여야 한다", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(sb.dir, "tmp", "tier.plist.*"))
	if len(leftovers) > 0 {
		t.Errorf("임시 plist 가 남았다: %v", leftovers)
	}
}

// OPS-7: lint 에 실패하면 LaunchAgents 에 아무것도 두지 않고 load 도 하지 않는다.
func TestScheduleInstallLeavesNothingWhenLintFails(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "meta")
	// 닫는 태그를 틀리게 해 plutil -lint 가 거부하는 템플릿으로 만든다
	template := filepath.Join(root, "scripts", "schedule", "tier.plist.template")
	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, template, strings.Replace(string(data), "</dict>", "</dikt>", 1))

	r := sb.bash(nil, filepath.Join(root, "scripts", "schedule", "run.sh"), "install")
	if r.err == nil {
		t.Fatalf("깨진 템플릿인데 install 이 성공했다:\n%s", r.output())
	}
	if exists(sb.tierPlist("meta")) {
		t.Errorf("lint 에 실패한 plist 가 LaunchAgents 에 남았다")
	}
	for _, c := range calls(t, launchctlLog) {
		if strings.HasPrefix(c, "load") {
			t.Errorf("lint 실패인데 launchctl %s", c)
		}
	}
}

func assertPlist(t *testing.T, p map[string]any, key, want string) {
	t.Helper()
	if p[key] != want {
		t.Errorf("%s = %v, want %q", key, p[key], want)
	}
}
