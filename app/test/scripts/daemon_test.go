package scripts

import (
	"path/filepath"
	"strings"
	"testing"
)

// DEAD-13 / FILE-5: memory-daemon stop 은 자기 잡(com.drakejin.memory.<workspace>.daemon)만 내린다 —
// 이름이 다른 LaunchAgents(예전 판의 com.drakejin.workspace.daemon 포함)는 건드리지 않는다.
func TestDaemonStopTouchesOnlyItsOwnJob(t *testing.T) {
	requireDarwin(t)
	sb := newSandbox(t)
	root := filepath.Join(sb.dir, "meta")
	sb.copyTo(root, "scripts/memory-common.sh", "scripts/memory-daemon")
	agents := filepath.Join(sb.home, "Library", "LaunchAgents")
	other := filepath.Join(agents, "com.drakejin.workspace.daemon.plist")
	own := filepath.Join(agents, "com.drakejin.memory.meta.daemon.plist")
	writeFile(t, other, "<plist/>")
	writeFile(t, own, "<plist/>")
	launchctlLog := sb.stub("launchctl", "exit 0")

	r := sb.mustBash(nil, filepath.Join(root, "scripts", "memory-daemon", "run.sh"), "stop")

	if !strings.Contains(r.stdout, "stopped (launchd 에서 내림)") {
		t.Errorf("stop 출력 = %q", r.output())
	}
	if exists(own) {
		t.Errorf("자기 잡의 plist 가 남았다")
	}
	if !exists(other) {
		t.Errorf("다른 잡의 plist(%s)를 지웠다", filepath.Base(other))
	}
	for _, c := range calls(t, launchctlLog) {
		if strings.Contains(c, filepath.Base(other)) {
			t.Errorf("다른 잡을 launchctl 로 건드렸다: %s", c)
		}
	}
}
