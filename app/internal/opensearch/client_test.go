package opensearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
)

func TestClientDoAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.URL.Query().Get("refresh") != "true" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("query/header = %v %v", r.URL.Query(), r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"echo":` + string(body) + `}`))
		case "/typed":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"type":"resource_already_exists_exception","reason":"exists"}}`))
		case "/plain":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"alias [x] missing"}`))
		case "/_bulk":
			if r.Header.Get("Content-Type") != "application/x-ndjson" {
				t.Errorf("bulk content type = %s", r.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Count(string(body), "\n") != 2 {
				t.Errorf("ndjson = %q", body)
			}
			_, _ = w.Write([]byte(`{"errors":true,"items":[{"index":{"status":201}},{"index":{"status":400,"error":{"type":"mapper_parsing_exception","reason":"bad"}}}]}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL + "/")
	ctx := context.Background()

	var out struct {
		Echo map[string]int `json:"echo"`
	}
	if err := c.Do(ctx, http.MethodPost, "/ok", map[string]int{"a": 1}, url.Values{"refresh": {"true"}}, &out); err != nil || out.Echo["a"] != 1 {
		t.Fatalf("Do = %v %+v", err, out)
	}
	err := c.Do(ctx, http.MethodPut, "/typed", nil, nil, nil)
	if !hasType(err, "resource_already_exists_exception") || !IsStatus(err, 400) || !strings.Contains(err.Error(), "exists") {
		t.Errorf("typed err = %v", err)
	}
	err = c.Do(ctx, http.MethodGet, "/plain", nil, nil, nil)
	if !IsStatus(err, 404) || !strings.Contains(err.Error(), "alias [x] missing") {
		t.Errorf("plain err = %v", err)
	}
	resp, err := c.Bulk(ctx, []any{map[string]any{"index": map[string]any{}}, map[string]any{"a": 1}}, nil)
	if err != nil || !resp.Errors || resp.FirstError() != "mapper_parsing_exception: bad" {
		t.Errorf("bulk = %v %+v", err, resp)
	}
	err = New("http://127.0.0.1:1").Do(ctx, http.MethodGet, "/", nil, nil, nil)
	if !strings.Contains(err.Error(), "연결 불가") || IsStatus(err, 404) {
		t.Errorf("unreachable = %v", err)
	}
	if q := With(Lenient(), "refresh", "true"); q.Get("refresh") != "true" || q.Get("allow_no_indices") != "true" {
		t.Errorf("With = %v", q)
	}
}

// fakeOS 는 경로별로 응답을 정하는 Client 다.
type fakeOS struct {
	mu    sync.Mutex
	calls []string
	reply func(method, path string, body any) (any, error)
}

func (f *fakeOS) Do(_ context.Context, method, path string, body any, _ url.Values, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+path)
	f.mu.Unlock()
	v, err := f.reply(method, path, body)
	if err != nil || out == nil || v == nil {
		return err
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (f *fakeOS) Bulk(context.Context, []any, url.Values) (BulkResponse, error) {
	return BulkResponse{}, nil
}

func TestBootstrapToleratesConcurrentPolicyWrite(t *testing.T) {
	cfg := config.Config{HotDays: 60, Snapshot: config.Snapshot{Repository: "mem-cold", Type: "fs", FSLocation: "/snap"}}
	os := &fakeOS{reply: func(method, path string, _ any) (any, error) {
		switch {
		case method == http.MethodGet && strings.Contains(path, HotColdPolicy):
			return map[string]int{"_seq_no": 1, "_primary_term": 1}, nil
		case method == http.MethodPut && strings.Contains(path, HotColdPolicy):
			return nil, &Error{Status: http.StatusConflict, Op: "PUT", Reason: "version conflict"}
		case method == http.MethodGet && strings.Contains(path, keepPolicy):
			return nil, &Error{Status: http.StatusNotFound}
		}
		return map[string]any{}, nil
	}}
	// 다른 내용의 정책을 바꾸는 것은 bootstrap 명령뿐이다 (RUN-9) — 그 PUT 이 다른 프로세스와 겹친 경우
	if err := bootstrap(context.Background(), os, cfg, claimCluster, func(string, ...any) {}); err != nil {
		t.Fatalf("409 는 성공으로 본다: %v", err)
	}
	want := []string{
		"PUT /_index_template/mem-template", "PUT /mem-*/_mapping", "GET /_snapshot/mem-cold", "PUT /_snapshot/mem-cold",
		"GET /_plugins/_ism/policies/mem-hot-cold", "PUT /_plugins/_ism/policies/mem-hot-cold",
		"GET /_plugins/_ism/policies/mem-knowledge-keep", "PUT /_plugins/_ism/policies/mem-knowledge-keep",
		"POST /mem-*/_search", "GET /mem-*/_alias",
	}
	if strings.Join(os.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v", os.calls)
	}
}

func TestBootstrapPropagatesOtherErrors(t *testing.T) {
	os := &fakeOS{reply: func(method, path string, _ any) (any, error) {
		if strings.Contains(path, "_snapshot") {
			return nil, &Error{Status: 500, Reason: "boom"}
		}
		return map[string]any{}, nil
	}}
	if err := Bootstrap(context.Background(), os, config.Config{Snapshot: config.Snapshot{Type: "s3", S3Bucket: "b"}}); err == nil {
		t.Error("500 은 실패")
	}
}

// 인덱스를 만들 때 매핑도 준다 — 같은 클러스터의 다른 판 프로세스가 bootstrap 으로 템플릿을 옛 필드 구성으로 되돌려도
// 이 판이 쓰는 필드(예: pending_cycle)가 strict 매핑에 거부되지 않게. 요청의 매핑은 템플릿 매핑과 합쳐진다.
func TestEnsureIndexCreatesWithTheMapping(t *testing.T) {
	var created any
	os := &fakeOS{reply: func(method, path string, body any) (any, error) {
		if method == http.MethodPut && path == "/mem-a-2026.01" {
			created = body
		}
		return map[string]any{}, nil
	}}
	if err := EnsureIndex(context.Background(), os, "mem-a-2026.01", "a", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(created)
	var got struct {
		Aliases  map[string]any `json:"aliases"`
		Mappings struct {
			Properties map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Mappings.Properties) != len(Properties()) || got.Mappings.Properties["pending_cycle"] == nil || got.Aliases["mem-a-hot"] == nil {
		t.Errorf("create body = %s", b)
	}
}

func TestEnsureIndexIgnoresExisting(t *testing.T) {
	os := &fakeOS{reply: func(string, string, any) (any, error) {
		return nil, &Error{Status: 400, Type: "resource_already_exists_exception"}
	}}
	if err := EnsureIndex(context.Background(), os, "mem-a-2026.01", "a", "org"); err != nil {
		t.Error(err)
	}
}

func TestSchemaShape(t *testing.T) {
	tpl := indexTemplate()
	if tpl["index_patterns"].([]string)[0] != "mem-*" {
		t.Error("pattern")
	}
	props := Properties()
	for _, f := range []string{"workspace", "memory_kind", "content_sha", "superseded_by", "summary"} {
		if _, ok := props[f]; !ok {
			t.Errorf("필드 %s 없음", f)
		}
	}
	keep := keepPolicyBody()["policy"].(map[string]any)["ism_template"].([]any)[0].(map[string]any)
	if keep["priority"].(int) <= 100 {
		t.Error("keep 정책이 hot-cold 를 이겨야 한다")
	}
}

// 매핑이 strict 라 청크의 json 필드는 모두 Properties 에 있어야 한다 — 빠지면 그 필드를 쓰는 _bulk 가 통째로 거부된다.
func TestPropertiesCoverEveryChunkField(t *testing.T) {
	props := Properties()
	typ := reflect.TypeOf(model.Chunk{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if _, ok := props[name]; !ok {
			t.Errorf("청크 필드 %s 가 매핑에 없다", name)
		}
	}
}

func TestSyncOrgAliases(t *testing.T) {
	var actions []any
	os := &fakeOS{reply: func(method, path string, body any) (any, error) {
		switch {
		case method == http.MethodPost && path == "/_aliases":
			actions = body.(map[string]any)["actions"].([]any)
			return map[string]any{}, nil
		case method == http.MethodPost && strings.HasSuffix(path, "/_search"):
			// 이 workspace 의 팀은 a, b 뿐이다
			return map[string]any{"aggregations": map[string]any{"teams": map[string]any{"buckets": []any{
				map[string]any{"key": "a"}, map[string]any{"key": "b"},
			}}}}, nil
		}
		return map[string]any{
			"mem-a-2026.01":   map[string]any{"aliases": map[string]any{"mem-a-hot": map[string]any{}}},
			"mem-b-knowledge": map[string]any{"aliases": map[string]any{"mem-b-hot": map[string]any{}, "mem-org-old-hot": map[string]any{}}},
			// 다른 meta-repo 의 팀 c — 그쪽 org alias 는 건드리지 않는다
			"mem-c-2026.01": map[string]any{"aliases": map[string]any{"mem-c-hot": map[string]any{}, "mem-org-theirs-hot": map[string]any{}}},
		}, nil
	}}
	cfg := config.Config{Workspace: "ws", TeamOrgs: map[string]string{"a": "ads", "b": "ads"}}
	if err := syncOrgAliases(context.Background(), os, cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(actions)
	want := `[{"add":{"alias":"mem-org-ads-hot","index":"mem-a-2026.01"}},{"remove":{"alias":"mem-org-old-hot","index":"mem-b-knowledge"}},{"add":{"alias":"mem-org-ads-hot","index":"mem-b-knowledge"}}]`
	if string(got) != want {
		t.Errorf("actions = %s", got)
	}
}

func TestScanPagesAndUpdateDocAllowlist(t *testing.T) {
	pages := 0
	os := &fakeOS{reply: func(method, path string, body any) (any, error) {
		b := body.(map[string]any)
		if b["search_after"] == nil {
			pages++
			hits := make([]any, scanPageSize)
			for i := range hits {
				hits[i] = map[string]any{"_id": fmt.Sprint(i), "_source": map[string]any{}, "sort": []any{i}}
			}
			return map[string]any{"hits": map[string]any{"hits": hits}}, nil
		}
		pages++
		return map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_id": "last", "_source": map[string]any{}}}}}, nil
	}}
	n := 0
	err := Scan(context.Background(), os, "mem-*", map[string]any{}, nil, ChunkSort, func(Hit[map[string]any]) error { n++; return nil })
	if err != nil || n != scanPageSize+1 || pages != 2 {
		t.Errorf("scan n=%d pages=%d err=%v", n, pages, err)
	}
	if err := UpdateDoc(context.Background(), os, []string{"i"}, "ws", "p", map[string]any{"evil'];": 1}); err == nil {
		t.Error("매핑에 없는 필드는 거부")
	}
	if !IsUnmanaged(map[string]any{AutoManageSetting: "false"}) || IsUnmanaged(map[string]any{}) {
		t.Error("IsUnmanaged")
	}
}

func TestSnapshotRepositoryIsLeftAloneWhenUnchanged(t *testing.T) {
	cfg := config.Snapshot{Repository: "mem-cold", Type: "fs", FSLocation: "/snap"}
	same := &fakeOS{reply: func(method, path string, _ any) (any, error) {
		if method == http.MethodGet {
			return map[string]any{"mem-cold": map[string]any{"type": "fs", "settings": map[string]any{"location": "/snap"}}}, nil
		}
		return nil, &Error{Status: 500, Reason: "should not PUT"}
	}}
	quiet := func(string, ...any) {}
	if err := ensureSnapshotRepository(context.Background(), same, cfg, claimCluster, quiet); err != nil || strings.Join(same.calls, "|") != "GET /_snapshot/mem-cold" {
		t.Errorf("같으면 다시 쓰지 않는다: %v %v", same.calls, err)
	}
	// 스냅샷·복원이 도는 동안 OpenSearch 는 repository 를 바꾸지 못하게 한다 — bootstrap 명령은 바꾸지 못했다고 알린다
	busy := &fakeOS{reply: func(method, path string, _ any) (any, error) {
		if method == http.MethodGet {
			return map[string]any{"mem-cold": map[string]any{"type": "fs", "settings": map[string]any{"location": "/old"}}}, nil
		}
		return nil, &Error{Status: 500, Reason: "trying to modify or unregister repository that is currently used"}
	}}
	if err := ensureSnapshotRepository(context.Background(), busy, cfg, claimCluster, quiet); err == nil || !strings.Contains(err.Error(), "다시") {
		t.Errorf("쓰는 중인 repository 는 끝난 뒤 다시 하라고 한다: %v", err)
	}
	if err := ensureSnapshotRepository(context.Background(), busy, cfg, leaveCluster, quiet); err != nil {
		t.Errorf("다른 프로세스의 bootstrap 은 바꾸려 하지 않는다: %v", err)
	}
	missing := &fakeOS{reply: func(method, path string, _ any) (any, error) {
		if method == http.MethodGet {
			return nil, &Error{Status: 404}
		}
		return map[string]any{}, nil
	}}
	if err := ensureSnapshotRepository(context.Background(), missing, cfg, leaveCluster, quiet); err != nil || strings.Join(missing.calls, "|") != "GET /_snapshot/mem-cold|PUT /_snapshot/mem-cold" {
		t.Errorf("없으면 만든다: %v %v", missing.calls, err)
	}
}

// RUN-10 / API-3: 400 의 원인은 error.root_cause 에만 있다 — 최상위 reason 은 "all shards failed" 뿐이라, 그것만 보이면
// 에이전트는 무엇을 고칠지 모른다. 원인(type: reason)을 붙이고, 최상위 type 은 그대로 둔다 (hasType 이 그것으로 판단한다).
func TestErrorCarriesRootCause(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		typ     string
		want    []string
		notWant string
	}{
		{"search_phase 의 root_cause", `{"error":{"root_cause":[{"type":"parse_exception","reason":"failed to parse date field [yesterday]"}],` +
			`"type":"search_phase_execution_exception","reason":"all shards failed"},"status":400}`,
			"search_phase_execution_exception", []string{"all shards failed", "parse_exception", "failed to parse date field [yesterday]"}, ""},
		{"root_cause 가 없으면 failed_shards", `{"error":{"type":"search_phase_execution_exception","reason":"all shards failed",` +
			`"failed_shards":[{"shard":0,"reason":{"type":"too_complex_to_determinize_exception","reason":"Exceeded maximum recursion level"}}]}}`,
			"search_phase_execution_exception", []string{"all shards failed", "Exceeded maximum recursion level"}, ""},
		{"원인이 최상위와 같으면 한 번만", `{"error":{"root_cause":[{"type":"resource_already_exists_exception","reason":"exists"}],` +
			`"type":"resource_already_exists_exception","reason":"exists"}}`,
			"resource_already_exists_exception", []string{"exists"}, "exists — "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := parseError("POST /mem-a-hot/_search", http.StatusBadRequest, []byte(c.payload))

			if !hasType(err, c.typ) || !IsStatus(err, http.StatusBadRequest) {
				t.Errorf("type/status = %v", err)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q 가 없다: %v", want, err)
				}
			}
			if c.notWant != "" && strings.Contains(err.Error(), c.notWant) {
				t.Errorf("원인을 되풀이한다: %v", err)
			}
		})
	}
}

// API-1: 읽기 준비는 클러스터에 닿는지만 본다 — GET / 하나, 아무것도 쓰지 않는다.
func TestPingOnlyReads(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"version":{"number":"2.19.2"}}`))
	}))
	defer srv.Close()

	err := Ping(context.Background(), New(srv.URL))

	if err != nil || !reflect.DeepEqual(methods, []string{"GET /"}) {
		t.Errorf("Ping = %v, 요청 %v", err, methods)
	}
	if err := Ping(context.Background(), New("http://127.0.0.1:1")); err == nil || !strings.Contains(err.Error(), "연결 불가") {
		t.Errorf("닿지 않으면 연결 불가: %v", err)
	}
}
