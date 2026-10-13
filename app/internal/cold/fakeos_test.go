package cold

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"

	"github.com/drakejin/workspace/app/internal/opensearch"
)

// osCall 은 fakeOS 가 받은 요청 하나다.
type osCall struct {
	Method string
	Path   string
	Body   any
}

// fakeOS 는 경로별로 답을 정하는 OpenSearch 다. route 가 돌려준 값을 JSON 으로 out 에 옮긴다.
type fakeOS struct {
	mu    sync.Mutex
	calls []osCall
	route func(method, path string, body map[string]any) (any, error)
}

func (f *fakeOS) Do(_ context.Context, method, path string, body any, _ url.Values, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, osCall{Method: method, Path: path, Body: body})
	f.mu.Unlock()
	var m map[string]any
	if body != nil {
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &m)
	}
	resp, err := f.route(method, path, m)
	if err != nil || out == nil || resp == nil {
		return err
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (f *fakeOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// called 는 method path 요청들이다.
func (f *fakeOS) called(method, path string) []osCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []osCall
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}
