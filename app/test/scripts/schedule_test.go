package scripts

import (
	"path/filepath"
	"strings"
	"testing"
)

// legacyTierLabel 은 예전 판의 고정 tier 잡 이름이다 — 머신에 하나뿐이었고 이 레포의 .env 가 가리키는 meta-repo 만 돌았다.
const legacyTierLabel = "com.drakejin.workspace.tier"

// scheduleScript 는 sandbox meta-repo(root)의 scripts/schedule/run.sh 다.
func scheduleScript(root string) string {
	return filepath.Join(root, "scripts", "schedule", "run.sh")
}

// dryRunPlist 는 install --dry-run 이 그린 plist 를 읽는다.
func dryRunPlist(t *testing.T, sb *sandbox, root string, vars map[string]string) map[string]any {
	t.Helper()
	r := sb.mustBash(vars, scheduleScript(root), "install", "--dry-run")
	rendered := filepath.Join(sb.dir, "dry-run.plist")
	writeFile(t, rendered, r.stdout)
	return plistJSON(t, rendered)
}

// FILE-9 / DOC-20: tier 잡은 데몬처럼 대상 meta-repo 를 따른다 — 이름은 com.drakejin.memory.<workspace>.tier 이고,
// install 때의 META_REPO_ROOT 와 메모리 설정(데몬과 같은 허용 목록, 비밀은 뺀다)을 잡에 담고, 로그는 그 meta-repo 에 쓴다.
func TestScheduleJobFollowsTargetMetaRepo(t *testing.T) {
	sb, root, _ := tierSandbox(t, "meta")
	other := filepath.Join(sb.dir, "other-meta")

	p := dryRunPlist(t, sb, root, map[string]string{
		"META_REPO_ROOT": other, "MEMORY_WORKSPACE": "other", "MEMORY_HOT_DAYS": "30", "MEMORY_GRAPH_PASSWORD": "pw-1",
	})

	assertPlist(t, p, "Label", tierLabel("other"))
	assertPlist(t, p, "StandardOutPath", other+"/.cache/schedule/tier.log")
	args, _ := p["ProgramArguments"].([]any)
	if len(args) < 3 || args[1] != root+"/scripts/memory/run.sh" || args[2] != "tier-tick" {
		t.Errorf("ProgramArguments = %v (스크립트는 이 레포의 것을 부른다)", args)
	}
	env, _ := p["EnvironmentVariables"].(map[string]any)
	want := map[string]string{"META_REPO_ROOT": other, "MEMORY_WORKSPACE": "other", "MEMORY_HOT_DAYS": "30"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("EnvironmentVariables.%s = %v, want %q", k, env[k], v)
		}
	}
	if _, ok := env["MEMORY_GRAPH_PASSWORD"]; ok {
		t.Errorf("비밀(MEMORY_GRAPH_PASSWORD)이 plist 에 들어갔다")
	}
	if env["PATH"] == nil || env["PATH"] == "" {
		t.Errorf("EnvironmentVariables.PATH 가 없다: %v", env)
	}
}

// FILE-9: meta-repo 마다 자기 tier 잡을 둔다 — 다른 meta-repo 의 install 이 먼저 깐 잡을 내리거나 덮지 않는다.
func TestScheduleInstallsOneJobPerMetaRepo(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "meta")
	other := filepath.Join(sb.dir, "other-meta")
	sb.mustBash(nil, scheduleScript(root), "install")
	first := len(calls(t, launchctlLog))

	sb.mustBash(map[string]string{"META_REPO_ROOT": other}, scheduleScript(root), "install")

	for _, ws := range []string{"meta", "other-meta"} {
		if !exists(sb.tierPlist(ws)) {
			t.Errorf("%s 의 tier 잡(%s)이 없다", ws, tierLabel(ws))
		}
	}
	for _, c := range calls(t, launchctlLog)[first:] {
		if strings.Contains(c, tierLabel("meta")) {
			t.Errorf("다른 meta-repo 의 install 이 meta 의 잡을 건드렸다: launchctl %s", c)
		}
	}
}

// FILE-9: 예전 고정 이름(com.drakejin.workspace.tier)의 잡은 그 잡이 돌던 meta-repo 에 install·uninstall 할 때 내리고 지운다.
func TestScheduleMigratesLegacyJob(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			sb, root, launchctlLog := tierSandbox(t, "meta")
			legacy := sb.agentPlist(legacyTierLabel)
			writeFile(t, legacy, "<plist/>")

			r := sb.mustBash(nil, scheduleScript(root), action)

			if exists(legacy) {
				t.Errorf("%s 뒤에도 예전 잡의 plist 가 남았다:\n%s", action, r.output())
			}
			if !containsCall(calls(t, launchctlLog), "unload "+legacy) {
				t.Errorf("예전 잡을 launchd 에서 내리지 않았다: %v", calls(t, launchctlLog))
			}
			if !strings.Contains(r.stdout, legacyTierLabel) {
				t.Errorf("예전 잡을 옮겼다고 알려야 한다:\n%s", r.output())
			}
		})
	}
}

// FILE-9: 예전 잡은 이 레포의 .env 가 가리키는 meta-repo 의 tier 를 돈다 — 다른 meta-repo 에 install 할 때는 그대로 둔다.
func TestScheduleKeepsLegacyJobOfAnotherMetaRepo(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "meta")
	legacy := sb.agentPlist(legacyTierLabel)
	writeFile(t, legacy, "<plist/>")

	sb.mustBash(map[string]string{"META_REPO_ROOT": filepath.Join(sb.dir, "other-meta")}, scheduleScript(root), "install")

	if !exists(legacy) {
		t.Errorf("다른 meta-repo 의 install 이 예전 잡(이 레포 .env 의 meta-repo 를 돈다)을 지웠다")
	}
	for _, c := range calls(t, launchctlLog) {
		if strings.Contains(c, legacyTierLabel) {
			t.Errorf("예전 잡을 launchctl 로 건드렸다: %s", c)
		}
	}
}

// FILE-9: install 이 깐 잡 이름을 기억한다 — 그 뒤 workspace 이름이 바뀌어도 uninstall 이 실제로 깐 잡을 내린다 (데몬과 같다).
func TestScheduleUninstallFindsInstalledJobAfterRename(t *testing.T) {
	sb, root, _ := tierSandbox(t, "meta")
	sb.mustBash(nil, scheduleScript(root), "install")

	r := sb.mustBash(map[string]string{"MEMORY_WORKSPACE": "renamed"}, scheduleScript(root), "uninstall")

	if exists(sb.tierPlist("meta")) {
		t.Errorf("이름이 바뀐 뒤 uninstall 이 깔린 잡(%s)을 찾지 못했다:\n%s", tierLabel("meta"), r.output())
	}
}

func containsCall(got []string, want string) bool {
	for _, c := range got {
		if c == want {
			return true
		}
	}
	return false
}
