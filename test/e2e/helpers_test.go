//go:build e2e

// Package e2e 는 실제 OpenSearch(make memory-up)에 붙는 블랙박스 테스트다: make test-e2e
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

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/llm"
	"github.com/drakejin/workspace/internal/opensearch"
	"github.com/drakejin/workspace/internal/search"
)

// newConfig 는 임시 meta-repo 루트와 고유 팀으로 설정을 만든다.
func newConfig(t *testing.T, extra map[string]string) config.Config {
	t.Helper()
	root := t.TempDir()
	team := fmt.Sprintf("e%d", time.Now().UnixNano()%1_000_000_000)
	env := map[string]string{
		"META_REPO_ROOT":         root,
		"MEMORY_DEFAULT_TEAM":    team,
		"MEMORY_WORKSPACE":       "ws-" + team,
		"OPENSEARCH_URL":         os.Getenv("OPENSEARCH_URL"),
		"MEMORY_DISTILL_ENABLED": "false",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	client := opensearch.New(cfg.OpenSearchURL)
	if err := opensearch.Bootstrap(context.Background(), client, cfg); err != nil {
		t.Skipf("OpenSearch(%s)에 붙을 수 없습니다 — make memory-up 먼저: %v", cfg.OpenSearchURL, err)
	}
	t.Cleanup(func() {
		_ = client.Do(context.Background(), http.MethodDelete, "/mem-"+cfg.DefaultTeam+"-*", nil, nil, nil)
	})
	return cfg
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
	hits, err := search.Search(context.Background(), opensearch.New(cfg.OpenSearchURL), search.Request{
		Workspace: cfg.Workspace, Team: cfg.DefaultTeam, Query: query, IncludeSuperseded: includeSuperseded,
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths(hits)
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
