package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// recoveryIndex 는 실패·복구 테스트의 관리 대상 인덱스다 (팀 t).
const recoveryIndex = "mem-t-2026.10"

// routedOS 는 경로별로 답하는 OpenSearch 대신이다: _settings 는 관리 대상 인덱스를, 팀 hot alias 검색(판정 이웃)은 neighbors 를,
// 나머지 _search 는 docs(문서 대표 청크)를, _snapshot 은 snapshots 를 돌려준다. fail 의 경로 조각이 든 요청은 그 오류로 실패하고,
// ctx 가 끝났으면 실제 클라이언트처럼 ctx 오류다. 받은 요청(경로와 본문)을 모은다.
type routedOS struct {
	indices   []string
	docs      []map[string]any
	neighbors []map[string]any
	snapshots []map[string]any

	mu   sync.Mutex
	fail map[string]error
	reqs []string
}

func (o *routedOS) setFail(fragment string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail == nil {
		o.fail = map[string]error{}
	}
	if err == nil {
		delete(o.fail, fragment)
		return
	}
	o.fail[fragment] = err
}

func (o *routedOS) failure(path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for fragment, err := range o.fail {
		if strings.Contains(path, fragment) {
			return err
		}
	}
	return nil
}

func (o *routedOS) record(path string, body any) {
	b, _ := json.Marshal(body)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reqs = append(o.reqs, path+" "+string(b))
}

// sent 는 경로 조각과 본문 조각이 모두 든 요청이 있었는가다.
func (o *routedOS) sent(pathFragment, bodyFragment string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, r := range o.reqs {
		path, body, _ := strings.Cut(r, " ")
		if strings.Contains(path, pathFragment) && strings.Contains(body, bodyFragment) {
			return true
		}
	}
	return false
}

func (o *routedOS) Do(ctx context.Context, _, path string, body any, _ url.Values, out any) error {
	o.record(path, body)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.failure(path); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(o.response(path), out)
}

func (o *routedOS) response(path string) []byte {
	var v any = map[string]any{}
	switch {
	case strings.HasSuffix(path, "/_settings"):
		settings := map[string]any{}
		for _, idx := range o.indices {
			settings[idx] = map[string]any{"settings": map[string]any{}}
		}
		v = settings
	case strings.HasPrefix(path, "/_snapshot/"):
		v = map[string]any{"snapshots": nonNil(o.snapshots)}
	case strings.HasPrefix(path, "/_cat/"):
		v = []any{}
	case strings.HasSuffix(path, "/_delete_by_query"):
		// 실제 OpenSearch 처럼 지운 청크 수를 돌려준다 — tombstone 은 실제로 지운 문서만 removed 로 보고한다
		v = map[string]any{"deleted": 1}
	case strings.HasSuffix(path, "-hot/_search"):
		v = map[string]any{"hits": map[string]any{"hits": nonNil(o.neighbors)}}
	case strings.HasSuffix(path, "/_search"):
		v = map[string]any{"hits": map[string]any{"hits": nonNil(o.docs)}}
	}
	b, _ := json.Marshal(v)
	return b
}

func nonNil(hits []map[string]any) []map[string]any {
	if hits == nil {
		return []map[string]any{}
	}
	return hits
}

func (o *routedOS) Bulk(ctx context.Context, lines []any, _ url.Values) (opensearch.BulkResponse, error) {
	o.record("/_bulk", len(lines))
	if err := ctx.Err(); err != nil {
		return opensearch.BulkResponse{}, err
	}
	return opensearch.BulkResponse{}, o.failure("/_bulk")
}

// logSink 는 여러 goroutine(사이클·증류)이 함께 쓰는 로그다.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) has(fragment string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// fakeClock 은 테스트가 움직이는 시계다.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// replaceCounter 는 Replace 만 세는 그래프 저장소 대신이다 (다른 메서드는 데몬 사이클이 부르지 않는다).
type replaceCounter struct {
	graph.Store
	mu       sync.Mutex
	replaced int
}

func (g *replaceCounter) Replace(context.Context, graph.Graph) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replaced++
	return nil
}

func (g *replaceCounter) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.replaced
}

func recoveryConfig(root string) config.Config {
	return config.Config{
		MetaRepoRoot: root, Workspace: "ws", DefaultTeam: "t",
		Judge:     config.Judge{Enabled: true, WindowDays: 7, MinScore: 0.3},
		Distill:   config.Distill{Enabled: true, Concurrency: 1},
		Reconcile: config.Reconcile{Interval: time.Minute, MaxFileBytes: 1 << 20, SanityMaxFiles: 1000},
		Snapshot:  config.Snapshot{Repository: "repo"},
	}
}

// writeDoc 은 root 아래 디스크 이름 name 으로 content 를 쓰고, 그 문서(source_path 는 NFC)의 새 문서 변경분을 돌려준다.
func writeDoc(t *testing.T, root, name, content string) diffEntry {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := document.CanonicalPath(name)
	b, err := document.Build(document.Input{SourcePath: rel, Content: content, DefaultTeam: "t", Now: time.Now(), FallbackTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return diffEntry{
		SourcePath: rel, Team: b.Meta.Team, Project: b.Meta.Repo, DocType: b.Meta.DocType, Kind: b.Meta.Kind, Change: changeNew,
		BodySHA: b.BodySHA, EventTime: b.EventTime, Title: b.Title, Body: b.Body, Supersedes: b.Meta.Supersedes,
	}
}

// indexedHead 는 디스크의 파일을 지금 내용 그대로 색인한 문서의 대표 청크(hit)다.
func indexedHead(t *testing.T, root, rel string, extra map[string]any) map[string]any {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	content, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := document.Build(document.Input{SourcePath: rel, Content: string(content), DefaultTeam: "t", Now: time.Now(), FallbackTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	src := map[string]any{
		"source_path": rel, "team": "t", "workspace": "ws", "chunk_index": 0, "memory_kind": string(b.Meta.Kind), "doc_type": string(b.Meta.DocType),
		"content_sha": b.ContentSHA, "body_sha": b.BodySHA, "mtime_ms": info.ModTime().UnixMilli(), "size": info.Size(),
		"index_version": model.IndexVersion, "title": b.Title, "text": b.Body,
	}
	for k, v := range extra {
		src[k] = v
	}
	return map[string]any{"_index": recoveryIndex, "_source": src}
}
