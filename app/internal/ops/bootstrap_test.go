package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// policyOS 는 ISM 정책이 다른 설정(hot 30일)으로 있는 클러스터다. 받은 요청을 남긴다.
type policyOS struct {
	mu    sync.Mutex
	calls []string
}

func (f *policyOS) Do(_ context.Context, method, path string, _ any, _ url.Values, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+path)
	f.mu.Unlock()
	if method != http.MethodGet || !strings.HasPrefix(path, "/_plugins/_ism/policies/") || out == nil {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"_seq_no": 3, "_primary_term": 1, "policy": opensearch.HotColdPolicyBody(30, "other-cold")["policy"]})
	return json.Unmarshal(raw, out)
}

func (f *policyOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// RUN-9: 시작할 때의 bootstrap(MCP·CLI·데몬)은 다른 설정의 클러스터 정책을 덮어쓰지 않는다. 클러스터 정책을 이 설정으로 바꾸는
// 길은 명시적인 bootstrap 명령이다.
func TestBootstrapCommandClaimsTheClusterPolicy(t *testing.T) {
	fake := &policyOS{}
	cfg := config.Config{HotDays: 60, Snapshot: config.Snapshot{Repository: "mem-cold", Type: "fs", FSLocation: "/snap"}}
	cli := &CLI{Config: cfg, OS: fake, Out: &bytes.Buffer{}}

	if err := cli.Run(context.Background(), "bootstrap", nil); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.Join(fake.calls, "|"), "PUT /_plugins/_ism/policies/"+opensearch.HotColdPolicy) {
		t.Errorf("bootstrap 명령은 클러스터 정책을 이 설정으로 바꾼다: %v", fake.calls)
	}
}
