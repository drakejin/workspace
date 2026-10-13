package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// brokenColdOS 는 cold 설정이 잘못된 클러스터다 — 스냅샷 repository PUT 이 500 이고, 읽기(GET·_search)는 된다.
type brokenColdOS struct {
	mu    sync.Mutex
	calls []string
}

func (o *brokenColdOS) Do(_ context.Context, method, path string, _ any, _ url.Values, out any) error {
	o.mu.Lock()
	o.calls = append(o.calls, method+" "+path)
	o.mu.Unlock()
	if method == http.MethodPut && strings.HasPrefix(path, "/_snapshot/") {
		return &opensearch.Error{Op: method + " " + path, Status: http.StatusInternalServerError, Reason: "repository type [s3] does not exist"}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`), out)
}

func (o *brokenColdOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// split 은 받은 요청을 쓰기(GET·_search 말고)와 검색으로 나눈다.
func (o *brokenColdOS) split() (writes []string, searches int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.calls {
		switch {
		case strings.HasSuffix(c, "/_search"):
			searches++
		case !strings.HasPrefix(c, http.MethodGet+" "):
			writes = append(writes, c)
		}
	}
	return writes, searches
}

func brokenColdCLI(t *testing.T) (*CLI, *brokenColdOS) {
	t.Helper()
	fake := &brokenColdOS{}
	cfg := config.Config{MetaRepoRoot: t.TempDir(), Workspace: "ws", DefaultTeam: "ads", HotDays: 60,
		Snapshot: config.Snapshot{Repository: "mem-cold", Type: "s3", S3Bucket: "b"}}
	h := mcpserver.NewHandlers(mcpserver.Deps{Config: cfg, OS: fake})
	return &CLI{Config: cfg, OS: fake, Handlers: h, Out: &bytes.Buffer{}, Now: time.Now}, fake
}

// API-1: CLI 의 읽기 명령(search·filters·list-cold)은 쓰기 쪽 bootstrap 을 거치지 않는다 — cold 설정이 잘못돼도 돌고,
// 부를 때마다 템플릿·매핑·ISM 을 PUT 하지 않는다. 쓰기 명령(reload)은 그대로 bootstrap 한다.
func TestCLIReadCommandsDoNotBootstrap(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{{"search", []string{"hello"}}, {"filters", nil}, {"list-cold", nil}} {
		cli, fake := brokenColdCLI(t)

		err := cli.Run(context.Background(), c.name, c.args)

		if writes, _ := fake.split(); err != nil || len(writes) != 0 {
			t.Errorf("%s: err=%v, 클러스터에 썼다: %v", c.name, err, writes)
		}
	}
	cli, _ := brokenColdCLI(t)
	if err := cli.Run(context.Background(), "reload", nil); err == nil || !strings.Contains(err.Error(), "snapshot repository") {
		t.Errorf("쓰기 명령은 bootstrap 한다: %v", err)
	}
}

// API-1: SessionStart 훅도 읽기 준비만 보고 검색한다 — 세션을 열 때마다 클러스터에 쓰지 않고, cold 설정 오류에 막히지 않는다.
func TestSessionContextSearchesWithoutBootstrap(t *testing.T) {
	cli, fake := brokenColdCLI(t)
	cwd := filepath.Join(cli.Config.MetaRepoRoot, "main", "svc")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	cli.In = strings.NewReader(`{"cwd":"` + cwd + `"}`)

	if _, err := runSessionContext(context.Background(), cli, nil); err != nil {
		t.Fatal(err)
	}

	writes, searches := fake.split()
	if len(writes) != 0 || searches == 0 {
		t.Errorf("훅: 쓰기 %v, 검색 %d번", writes, searches)
	}
}

// API-9: search 와 filters 는 같은 필터 플래그를 한 곳에서 정의한다 — search 에만 -size 가 더 있다.
func TestSearchAndFiltersShareFilterFlags(t *testing.T) {
	names := func(cmd string) []string {
		var out bytes.Buffer
		if err := helpCLI(t, &out).Run(context.Background(), cmd, []string{"-h"}); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range flagLine.FindAllStringSubmatch(out.String(), -1) {
			got = append(got, m[1])
		}
		sort.Strings(got)
		return got
	}

	searchFlags, filterFlags := names("search"), names("filters")

	want := append(append([]string{}, filterFlags...), "size")
	sort.Strings(want)
	if strings.Join(searchFlags, ",") != strings.Join(want, ",") {
		t.Errorf("search %v, filters %v", searchFlags, filterFlags)
	}
}
