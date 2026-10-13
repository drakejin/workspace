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

// legacyTierPlist 는 예전 판 install 이 깐 고정 이름 잡의 plist 다 — checkout 의 scripts/memory/run.sh tier-tick 을
// 그 checkout 에서(WorkingDirectory) PATH 만 담아 돌렸다. 그래서 그 잡의 meta-repo 는 그 checkout 의 .env 가 정했다.
func legacyTierPlist(checkout string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>` + legacyTierLabel + `</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>` + checkout + `/scripts/memory/run.sh</string>
        <string>tier-tick</string>
    </array>
    <key>WorkingDirectory</key><string>` + checkout + `</string>
    <key>EnvironmentVariables</key>
    <dict><key>PATH</key><string>/usr/bin:/bin</string></dict>
    <key>StartInterval</key><integer>21600</integer>
</dict>
</plist>
`
}

// assertLegacyKept 는 예전 잡을 지우지도, launchctl 로 건드리지도 않았는가다.
func assertLegacyKept(t *testing.T, sb *sandbox, launchctlLog string, r result) {
	t.Helper()
	if !exists(sb.agentPlist(legacyTierLabel)) {
		t.Errorf("다른 meta-repo 의 예전 잡을 지웠다:\n%s", r.output())
	}
	for _, c := range calls(t, launchctlLog) {
		if strings.Contains(c, legacyTierLabel) {
			t.Errorf("예전 잡을 launchctl 로 건드렸다: %s", c)
		}
	}
}

// FILE-9 · R4-ops-scripts-1 · R5-final-docs-3: 예전 고정 이름(com.drakejin.workspace.tier)의 잡은 그 잡이 돌던 meta-repo 에
// install·uninstall 할 때 내리고 지운다. 무엇을 내렸는지(그 잡이 돌던 meta-repo) 그 잡을 내렸다는 줄에서 알린다 — install 이 늘
// 찍는 'installed … root=<root>' 줄로는 알 수 없다.
func TestScheduleMigratesLegacyJob(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			sb, root, launchctlLog := tierSandbox(t, "meta")
			legacy := sb.agentPlist(legacyTierLabel)
			writeFile(t, legacy, legacyTierPlist(root))

			r := sb.mustBash(nil, scheduleScript(root), action)

			if exists(legacy) {
				t.Errorf("%s 뒤에도 예전 잡의 plist 가 남았다:\n%s", action, r.output())
			}
			if !containsCall(calls(t, launchctlLog), "unload "+legacy) {
				t.Errorf("예전 잡을 launchd 에서 내리지 않았다: %v", calls(t, launchctlLog))
			}
			if line := lineContaining(r.stdout, legacyTierLabel); !strings.Contains(line, "meta-repo "+root) {
				t.Errorf("예전 잡을 내린 줄이 그 잡이 돌던 meta-repo(%s)를 알려야 한다: %q\n%s", root, line, r.output())
			}
		})
	}
}

// FILE-9: 예전 잡은 그 checkout 의 .env 가 가리키는 meta-repo 의 tier 를 돈다 — 다른 meta-repo 에 install 할 때는 그대로 둔다.
func TestScheduleKeepsLegacyJobOfAnotherMetaRepo(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "meta")
	writeFile(t, sb.agentPlist(legacyTierLabel), legacyTierPlist(root))

	r := sb.mustBash(map[string]string{"META_REPO_ROOT": filepath.Join(sb.dir, "other-meta")}, scheduleScript(root), "install")

	assertLegacyKept(t, sb, launchctlLog, r)
}

// R4-ops-scripts-1: 예전 잡이 어느 meta-repo 를 돌았는지는 그 plist 가 부르는 checkout 으로 정한다 — install 하는 checkout(worktree·사본)의
// .env 가 아니다. 다른 checkout 에서 install 해도 그 checkout 의 meta-repo 를 돌던 예전 잡은 그대로 두고, 그 잡의 meta-repo 를 알린다.
func TestScheduleKeepsLegacyJobOfAnotherCheckout(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			sb, root, launchctlLog := tierSandbox(t, "copy")
			realws := filepath.Join(sb.dir, "realws")
			writeFile(t, filepath.Join(realws, "scripts", "memory", "run.sh"), "#!/usr/bin/env bash\n")
			writeFile(t, sb.agentPlist(legacyTierLabel), legacyTierPlist(realws))

			r := sb.mustBash(nil, scheduleScript(root), action)

			assertLegacyKept(t, sb, launchctlLog, r)
			if !strings.Contains(r.stdout, realws) {
				t.Errorf("예전 잡이 도는 meta-repo(%s)를 알려야 한다:\n%s", realws, r.output())
			}
		})
	}
}

// R4-ops-scripts-1: 예전 잡의 checkout 이 .env 로 다른 meta-repo 를 가리켰으면 그 meta-repo 가 예전 잡의 것이다 —
// 그 meta-repo 로 install 하면 어느 checkout 에서든 옮긴다.
func TestScheduleMigratesLegacyJobByItsCheckoutEnv(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "copy")
	shared := filepath.Join(sb.dir, "shared-meta")
	checkout := filepath.Join(sb.dir, "realws")
	writeFile(t, filepath.Join(checkout, ".env"), "META_REPO_ROOT="+shared+"\n")
	legacy := sb.agentPlist(legacyTierLabel)
	writeFile(t, legacy, legacyTierPlist(checkout))

	r := sb.mustBash(map[string]string{"META_REPO_ROOT": shared}, scheduleScript(root), "install")

	if exists(legacy) || !containsCall(calls(t, launchctlLog), "unload "+legacy) {
		t.Errorf("같은 meta-repo(%s)를 돌던 예전 잡을 옮기지 않았다:\n%s", shared, r.output())
	}
}

// R4-ops-scripts-1: status 도 남아 있는 예전 잡이 어느 meta-repo 를 도는지 알린다.
func TestScheduleStatusNamesLegacyJobsMetaRepo(t *testing.T) {
	sb, root, _ := tierSandbox(t, "copy")
	realws := filepath.Join(sb.dir, "realws")
	writeFile(t, filepath.Join(realws, "scripts", "memory", "run.sh"), "#!/usr/bin/env bash\n")
	writeFile(t, sb.agentPlist(legacyTierLabel), legacyTierPlist(realws))

	r := sb.mustBash(nil, scheduleScript(root), "status")

	if !strings.Contains(r.stdout, legacyTierLabel) || !strings.Contains(r.stdout, "meta-repo "+realws) {
		t.Errorf("status 가 예전 잡과 그 meta-repo(%s)를 알려야 한다:\n%s", realws, r.output())
	}
}

// R4-ops-scripts-1: plist 에서 예전 잡이 부르는 checkout 을 읽지 못하면 어느 meta-repo 의 것인지 모르므로 지우지 않고 알린다.
func TestScheduleKeepsUnreadableLegacyJob(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "meta")
	writeFile(t, sb.agentPlist(legacyTierLabel), "<plist/>")

	r := sb.mustBash(nil, scheduleScript(root), "install")

	assertLegacyKept(t, sb, launchctlLog, r)
	if !strings.Contains(r.stdout, legacyTierLabel) {
		t.Errorf("읽지 못한 예전 잡을 남겼다고 알려야 한다:\n%s", r.output())
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

// R5-final-docs-1: 예전 잡이 부르는 checkout 이 없어졌으면 그 잡은 어디서도 돌 수 없다 — 어느 meta-repo 에서 install·uninstall
// 하든 내리고 지우며 그렇다고 알린다. 없어진 경로로 install 하라고 하지 않는다 (그 install 은 없는 meta-repo 의 잡을 깐다).
func TestScheduleRetiresLegacyJobWhoseCheckoutIsGone(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			sb, root, launchctlLog := tierSandbox(t, "copy")
			gone := filepath.Join(sb.dir, "gone-wt")
			legacy := sb.agentPlist(legacyTierLabel)
			writeFile(t, legacy, legacyTierPlist(gone))

			r := sb.mustBash(nil, scheduleScript(root), action)

			if exists(legacy) || !containsCall(calls(t, launchctlLog), "unload "+legacy) {
				t.Errorf("checkout 이 없는 예전 잡을 내리고 지워야 한다:\n%s", r.output())
			}
			line := lineContaining(r.stdout, legacyTierLabel)
			if !strings.Contains(line, gone) || !strings.Contains(line, "없") {
				t.Errorf("예전 잡의 checkout(%s)이 없어 내렸다고 알려야 한다: %q", gone, line)
			}
			if strings.Contains(r.stdout, "META_REPO_ROOT="+gone) {
				t.Errorf("없어진 경로로 install 하라고 한다:\n%s", r.output())
			}
			if exists(filepath.Join(gone, ".cache")) {
				t.Errorf("없어진 checkout 에 .cache 를 만들었다")
			}
		})
	}
}

// R5-final-docs-1: status 는 checkout 이 없어진 예전 잡을 그대로 알리되, 없어진 경로의 meta-repo 로 install 하라고 하지 않는다.
func TestScheduleStatusNamesLegacyJobWhoseCheckoutIsGone(t *testing.T) {
	sb, root, launchctlLog := tierSandbox(t, "copy")
	gone := filepath.Join(sb.dir, "gone-wt")
	writeFile(t, sb.agentPlist(legacyTierLabel), legacyTierPlist(gone))

	r := sb.mustBash(nil, scheduleScript(root), "status")

	assertLegacyKept(t, sb, launchctlLog, r)
	line := lineContaining(r.stdout, legacyTierLabel)
	if !strings.Contains(line, gone) || !strings.Contains(line, "없") || strings.Contains(line, "그 meta-repo 로 install") {
		t.Errorf("status 가 checkout 이 없는 예전 잡을 알려야 한다 (install 안내 없이): %q", line)
	}
}

// lineContaining 은 out 에서 fragment 가 든 첫 줄이다 (없으면 "").
func lineContaining(out, fragment string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, fragment) {
			return line
		}
	}
	return ""
}

func containsCall(got []string, want string) bool {
	for _, c := range got {
		if c == want {
			return true
		}
	}
	return false
}
