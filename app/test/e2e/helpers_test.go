//go:build e2e

// Package e2e 는 실제 OpenSearch·Neo4j(루트의 make memory-init)에 붙는 블랙박스 테스트다: app/ 에서 make test-e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/llm"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
)

// clusterKeys 는 클러스터 전체 객체(ISM 정책, 스냅샷 repository, org alias)를 정하는 설정이다.
var clusterKeys = []string{
	"OPENSEARCH_URL", "MEMORY_HOT_DAYS", "MEMORY_GRAPH_URI",
	"COLD_SNAPSHOT_REPOSITORY", "COLD_SNAPSHOT_TYPE", "COLD_SNAPSHOT_FS_LOCATION", "COLD_SNAPSHOT_S3_BUCKET",
}

// newConfig 는 임시 meta-repo 루트와 고유 팀으로 설정을 만든다.
func newConfig(t *testing.T, extra map[string]string) config.Config {
	t.Helper()
	root := t.TempDir()
	team := fmt.Sprintf("e%d", time.Now().UnixNano()%1_000_000_000)
	env := map[string]string{
		"META_REPO_ROOT":         root,
		"MEMORY_DEFAULT_TEAM":    team,
		"MEMORY_WORKSPACE":       "ws-" + team,
		"MEMORY_DISTILL_ENABLED": "false",
	}
	// 클러스터 전체에 걸리는 값(ISM 정책·스냅샷 repository·포트)은 실제 설정을 그대로 쓴다 — bootstrap 이 같은 클러스터의
	// 실제 메모리 설정을 테스트 기본값으로 덮어쓰지 않게
	for _, k := range clusterKeys {
		env[k] = os.Getenv(k)
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HotDays < minHotDays {
		t.Fatalf("MEMORY_HOT_DAYS=%d — e2e 픽스처는 %d일 이상에서 성립한다", cfg.HotDays, minHotDays)
	}
	hotDays = cfg.HotDays
	client := opensearch.New(cfg.OpenSearchURL)
	if err := client.Do(context.Background(), http.MethodGet, "/_cluster/health", nil, nil, nil); err != nil {
		t.Skipf("OpenSearch(%s)에 붙을 수 없습니다 — make memory-init 먼저: %v", cfg.OpenSearchURL, err)
	}
	if err := opensearch.Bootstrap(context.Background(), client, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err) // 붙는데 bootstrap 이 실패하면 버그다 — 건너뛰지 않는다
	}
	t.Cleanup(func() { cleanupTeam(client, cfg) })
	return cfg
}

// cleanupTeam 은 테스트 팀이 남긴 것 — 인덱스, 그 팀 인덱스만 담은 스냅샷, workspace 그래프 — 을 지운다.
// 같은 OpenSearch·스냅샷 repository 를 실제 메모리와 함께 쓰므로 팀 접두사로만 지운다.
func cleanupTeam(client opensearch.Client, cfg config.Config) {
	ctx := context.Background()
	prefix := "mem-" + cfg.DefaultTeam + "-"
	_ = client.Do(ctx, http.MethodDelete, "/"+prefix+"*", nil, nil, nil)
	if snaps, err := cold.Snapshots(ctx, client, cfg.Snapshot.Repository); err == nil {
		for _, s := range snaps {
			if allPrefixed(s.Indices, prefix) {
				_ = client.Do(ctx, http.MethodDelete, "/_snapshot/"+cfg.Snapshot.Repository+"/"+s.Name, nil, nil, nil)
			}
		}
	}
	if g, err := graph.New(cfg.Graph); err == nil {
		_ = g.Replace(ctx, graph.Graph{Workspace: cfg.Workspace}) // workspace 그래프 비우기
		_ = g.Close(ctx)
	}
}

func allPrefixed(names []string, prefix string) bool {
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			return false
		}
	}
	return len(names) > 0
}

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ingestFile 은 파일 하나를 IngestPath 로 적재한 결과다 — 파일 경로를 주면 결과는 하나다.
func ingestFile(ctx context.Context, ix ingest.Indexer, path string) (ingest.Result, error) {
	results, err := ix.IngestPath(ctx, path, "")
	if err != nil {
		return ingest.Result{}, err
	}
	if len(results) != 1 {
		return ingest.Result{}, fmt.Errorf("ingest %s: 결과 %d개 (파일 하나면 하나다)", path, len(results))
	}
	return results[0], nil
}

// hotDays 는 지금 테스트 설정의 hot 기간이다 (newConfig 가 실제 MEMORY_HOT_DAYS 로 맞춘다).
// 픽스처의 나이는 이 값에 맞춰 잡는다: hot 안(hotDays*2/3), cold(hotDays+N).
var hotDays = 60

// minHotDays 는 픽스처가 성립하는 최소 hot 기간이다 (hot 안의 문서가 판정 기간 7일보다 오래돼야 한다).
const minHotDays = 15

func daysAgo(n int) string {
	return time.Now().Add(-time.Duration(n) * 24 * time.Hour).Format("2006-01-02")
}

func paths(hits []search.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.SourcePath)
	}
	return out
}

func searchPaths(t *testing.T, cfg config.Config, query string, includeSuperseded bool) []string {
	t.Helper()
	hitsRes, err := search.Search(context.Background(), opensearch.New(cfg.OpenSearchURL), search.Request{
		Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: query, IncludeSuperseded: includeSuperseded,
	})
	hits := hitsRes.Hits
	if err != nil {
		t.Fatal(err)
	}
	return paths(hits)
}

// coldIndices 는 이 테스트 팀의 cold 인덱스 이름이다.
func coldIndices(t *testing.T, cfg config.Config) []string {
	t.Helper()
	list, err := cold.ListCold(context.Background(), opensearch.New(cfg.OpenSearchURL), cfg, cfg.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, idx := range list {
		names = append(names, idx.Name)
	}
	return names
}

// eventuallyFound 는 데몬이 비동기로 색인하는 동안 검색을 되풀이한다. 막 만들어진 인덱스는 샤드가 뜨기 전 잠깐
// 503 을 돌려줄 수 있어, 오류도 "아직" 으로 본다.
func eventuallyFound(cfg config.Config, query string, timeout time.Duration) bool {
	client := opensearch.New(cfg.OpenSearchURL)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		hitsRes, err := search.Search(context.Background(), client, search.Request{Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: query})
		hits := hitsRes.Hits
		if err == nil && len(hits) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// fakeLLM 은 결정적인 가짜 LLM 이다. 판정은 큐에 넣은 답, 증류는 고정 요약. 호출 수를 센다.
type fakeLLM struct {
	mu       sync.Mutex
	verdicts []string
	judge    int
	distill  int
}

func (f *fakeLLM) Ask(_ context.Context, in llm.Input) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Tag == "judge" {
		f.judge++
		if len(f.verdicts) == 0 {
			return nil, false
		}
		v := f.verdicts[0]
		f.verdicts = f.verdicts[1:]
		if v == "" {
			return nil, false
		}
		return json.RawMessage(v), true
	}
	f.distill++
	title, _, _ := strings.Cut(strings.TrimPrefix(in.User, "title: "), "\n")
	out, _ := json.Marshal(map[string]any{"summary": "요약: " + title, "entities": []string{"concept:demo"}})
	return out, true
}

func (f *fakeLLM) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.judge, f.distill
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
