package opensearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
)

// clusterShaped 는 PUT 한 정책 본문을 클러스터가 GET 으로 돌려주는 모양으로 바꾼다 — 기본값(retry)·시각·판 필드가 붙는다.
func clusterShaped(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body["policy"])
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	policy["policy_id"], policy["last_updated_time"], policy["schema_version"], policy["error_notification"] = "p", 1791874270415, 21, nil
	for _, st := range policy["states"].([]any) {
		for _, a := range st.(map[string]any)["actions"].([]any) {
			a.(map[string]any)["retry"] = map[string]any{"count": 3, "backoff": "exponential", "delay": "1m"}
		}
	}
	for _, tmpl := range policy["ism_template"].([]any) {
		tmpl.(map[string]any)["last_updated_time"] = 1791874270415
	}
	return map[string]any{"_id": "p", "_seq_no": 7, "_primary_term": 1, "policy": policy}
}

// policyCluster 는 정책 하나가 existing(nil 이면 없음)으로 있는 클러스터다.
func policyCluster(existing map[string]any) *fakeOS {
	return &fakeOS{reply: func(method, path string, _ any) (any, error) {
		if method == http.MethodGet {
			if existing == nil {
				return nil, &Error{Status: http.StatusNotFound}
			}
			return existing, nil
		}
		return map[string]any{}, nil
	}}
}

// OPS-9: 내용이 같은 정책은 다시 쓰지 않는다 — 모든 CLI·MCP·데몬이 시작할 때마다 PUT 해 정책의 판이 수천으로 올라갔다.
func TestPolicyIsNotRewrittenWhenUnchanged(t *testing.T) {
	body := HotColdPolicyBody(60, "mem-cold")
	for _, mode := range []clusterWrite{leaveCluster, claimCluster} {
		os := policyCluster(clusterShaped(t, body))
		var warned warnings

		err := ensurePolicy(context.Background(), os, HotColdPolicy, body, mode, warned.add)

		if err != nil || strings.Join(os.calls, "|") != "GET /_plugins/_ism/policies/"+HotColdPolicy || len(warned) != 0 {
			t.Errorf("mode=%v: 같은 정책은 GET 만 한다: %v %v %v", mode, os.calls, warned, err)
		}
	}
}

// warnings 는 경고를 모은다.
type warnings []string

func (w *warnings) add(format string, args ...any) { *w = append(*w, fmt.Sprintf(format, args...)) }

// RUN-9: 정책은 클러스터에 하나다(ism_template mem-*). 다른 MEMORY_HOT_DAYS·COLD_SNAPSHOT_REPOSITORY 로 시작한 프로세스
// (다른 meta-repo 의 MCP·CLI·데몬)는 덮어쓰지 않고 두 값을 경고한다. 명시적인 bootstrap 명령만 이 설정으로 바꾼다.
func TestPolicyFromAnotherSettingIsOnlyClaimedByTheBootstrapCommand(t *testing.T) {
	cluster := clusterShaped(t, HotColdPolicyBody(30, "other-cold"))
	mine := HotColdPolicyBody(60, "mem-cold")

	os := policyCluster(cluster)
	var warned warnings
	err := ensurePolicy(context.Background(), os, HotColdPolicy, mine, leaveCluster, warned.add)
	if err != nil || len(os.calls) != 1 {
		t.Errorf("다른 설정의 정책은 덮어쓰지 않는다: %v %v", os.calls, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "hot 30d → snapshot(other-cold)") || !strings.Contains(warned[0], "hot 60d → snapshot(mem-cold)") ||
		!strings.Contains(warned[0], "scripts/memory/run.sh bootstrap") {
		t.Errorf("경고는 클러스터 값과 이 설정 값, 바꾸는 법을 말한다: %v", warned)
	}

	os = policyCluster(cluster)
	warned = nil
	err = ensurePolicy(context.Background(), os, HotColdPolicy, mine, claimCluster, warned.add)
	if err != nil || strings.Join(os.calls, "|") != "GET /_plugins/_ism/policies/mem-hot-cold|PUT /_plugins/_ism/policies/mem-hot-cold" {
		t.Errorf("bootstrap 명령은 이 설정으로 바꾼다: %v %v", os.calls, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "hot 30d") || !strings.Contains(warned[0], "hot 60d") {
		t.Errorf("바꿀 때도 무엇을 무엇으로 바꾸는지 알린다: %v", warned)
	}
}

// OPS-9: 이 판의 본문에서 뺀 필드가 클러스터 정책에 남아 있으면 다른 정책이다 — 클러스터가 덧붙인 필드(시각·판·retry)만 빼고
// 본다. 그러지 않으면 코드가 정책에서 무언가를 뺐을 때 bootstrap 명령으로도 클러스터 정책을 고칠 수 없다. 설명은 같고 본문만
// 다르면 경고는 그렇다고 말한다 (이 레포를 업데이트해 정책 본문이 바뀌었다).
func TestPolicyWithAFieldThisVersionDroppedIsDifferent(t *testing.T) {
	cluster := clusterShaped(t, HotColdPolicyBody(60, "mem-cold"))
	mine := HotColdPolicyBody(60, "mem-cold")
	delete(mine["policy"].(map[string]any), "ism_template") // 이 판이 정책에서 뺀 필드

	os := policyCluster(cluster)
	var warned warnings
	err := ensurePolicy(context.Background(), os, HotColdPolicy, mine, claimCluster, warned.add)
	if err != nil || strings.Join(os.calls, "|") != "GET /_plugins/_ism/policies/mem-hot-cold|PUT /_plugins/_ism/policies/mem-hot-cold" {
		t.Errorf("bootstrap 명령은 뺀 필드까지 맞춘다: %v %v", os.calls, err)
	}

	os = policyCluster(cluster)
	warned = nil
	err = ensurePolicy(context.Background(), os, HotColdPolicy, mine, leaveCluster, warned.add)
	if err != nil || len(os.calls) != 1 || len(warned) != 1 || !strings.Contains(warned[0], "본문") {
		t.Errorf("시작하는 프로세스는 본문이 다르다고 경고만 한다: %v %v %v", os.calls, warned, err)
	}
}

func TestPolicyIsCreatedWhenMissing(t *testing.T) {
	for _, mode := range []clusterWrite{leaveCluster, claimCluster} {
		os := policyCluster(nil)
		var warned warnings

		err := ensurePolicy(context.Background(), os, HotColdPolicy, HotColdPolicyBody(60, "mem-cold"), mode, warned.add)

		if err != nil || strings.Join(os.calls, "|") != "GET /_plugins/_ism/policies/mem-hot-cold|PUT /_plugins/_ism/policies/mem-hot-cold" || len(warned) != 0 {
			t.Errorf("mode=%v: 없으면 만든다: %v %v %v", mode, os.calls, warned, err)
		}
	}
}

// RUN-9: 스냅샷 repository 도 클러스터 전역이다 — 다른 설정으로 있으면 bootstrap 명령만 바꾸고, 나머지는 경고만 한다.
func TestSnapshotRepositoryFromAnotherSettingIsOnlyClaimedByTheBootstrapCommand(t *testing.T) {
	cfg := config.Snapshot{Repository: "mem-cold", Type: "fs", FSLocation: "/snap"}
	cluster := func() *fakeOS {
		return &fakeOS{reply: func(method, path string, _ any) (any, error) {
			if method == http.MethodGet {
				return map[string]any{"mem-cold": map[string]any{"type": "fs", "settings": map[string]any{"location": "/other"}}}, nil
			}
			return map[string]any{}, nil
		}}
	}
	os := cluster()
	var warned warnings
	if err := ensureSnapshotRepository(context.Background(), os, cfg, leaveCluster, warned.add); err != nil || len(os.calls) != 1 {
		t.Errorf("다른 설정의 repository 는 덮어쓰지 않는다: %v %v", os.calls, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "/other") || !strings.Contains(warned[0], "/snap") {
		t.Errorf("경고는 두 설정을 말한다: %v", warned)
	}
	os = cluster()
	if err := ensureSnapshotRepository(context.Background(), os, cfg, claimCluster, warned.add); err != nil || strings.Join(os.calls, "|") != "GET /_snapshot/mem-cold|PUT /_snapshot/mem-cold" {
		t.Errorf("bootstrap 명령은 바꾼다: %v %v", os.calls, err)
	}
}
