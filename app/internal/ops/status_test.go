package ops

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// pathOS 는 경로별 JSON 답을 주는 OpenSearch 다. 없는 경로는 빈 답이다.
type pathOS map[string]string

func (p pathOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	body, ok := p[path]
	if !ok || out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

func (pathOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// RUN-19: 'memory indices N' 은 이 workspace 의 팀 인덱스만 센다 — 같은 클러스터의 다른 meta-repo(mem-default-*)와
// 샘플(mem-other-*) 인덱스까지 세어 이 workspace 의 숫자처럼 보였다.
func TestIndexStatsCountsOnlyThisWorkspacesIndices(t *testing.T) {
	os := pathOS{
		"/_cat/indices/mem-*": `[{"index":"mem-ws-2026.09"},{"index":"mem-ws-2026.10"},{"index":"mem-ws-knowledge"},` +
			`{"index":"mem-ws-t2-knowledge"},{"index":"mem-default-2026.10"},{"index":"mem-other-2026.10"}]`,
		"/mem-*/_search": `{"aggregations":{"teams":{"buckets":[{"key":"ws"},{"key":"ws-t2"}]}}}`,
		"/mem-*/_count":  `{"count":3}`,
	}
	probe := NewProbe(config.Config{Workspace: "ws"}, os, nil)

	st, ok := probe.IndexStats(context.Background())

	if !ok || st.Indices != 4 {
		t.Errorf("IndexStats = %+v %v — 이 workspace 의 팀(ws, ws-t2) 인덱스 4개", st, ok)
	}
}
