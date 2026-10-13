// Package opensearch 는 메모리 층이 OpenSearch 에 바라는 것의 전부다. 공식 클라이언트 대신 REST 경로를 직접 쓴다
// (API 시그니처가 버전마다 달라도 경로는 같다).
package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 는 OpenSearch REST 호출이다. 테스트는 같은 인터페이스의 fake 를 쓴다.
type Client interface {
	// Do 는 JSON 요청을 보내고 응답을 out 에 디코드한다(out 이 nil 이면 버린다).
	Do(ctx context.Context, method, path string, body any, query url.Values, out any) error
	// Bulk 는 _bulk NDJSON 을 보낸다.
	Bulk(ctx context.Context, lines []any, query url.Values) (BulkResponse, error)
}

// BulkResponse 는 _bulk 응답 중 쓰는 부분이다.
type BulkResponse struct {
	Errors bool                        `json:"errors"`
	Items  []map[string]BulkItemResult `json:"items"`
}

// BulkItemResult 는 _bulk 항목 하나의 결과다.
type BulkItemResult struct {
	Error *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error,omitempty"`
}

// FirstError 는 첫 실패 항목의 설명이다.
func (r BulkResponse) FirstError() string {
	for _, item := range r.Items {
		for _, res := range item {
			if res.Error != nil {
				return res.Error.Type + ": " + res.Error.Reason
			}
		}
	}
	return ""
}

// Error 는 OpenSearch 가 돌려준 실패다. 연결 실패면 Status 가 0 이다.
type Error struct {
	Status int
	Type   string
	Reason string
	Op     string
}

func (e *Error) Error() string {
	status := "연결 불가"
	if e.Status != 0 {
		status = fmt.Sprint(e.Status)
	}
	return fmt.Sprintf("OpenSearch %s 실패 (%s): %s", e.Op, status, e.Reason)
}

// IsStatus 는 err 가 해당 HTTP 상태의 OpenSearch 에러인가.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// hasType 은 err 가 해당 타입의 OpenSearch 에러인가 (예: resource_already_exists_exception).
func hasType(err error, typ string) bool {
	var e *Error
	return errors.As(err, &e) && e.Type == typ
}

// Lenient 는 인덱스가 없어도 실패하지 않게 하는 쿼리 파라미터다.
func Lenient() url.Values {
	return url.Values{"ignore_unavailable": {"true"}, "allow_no_indices": {"true"}}
}

// With 는 쿼리 파라미터를 더한 사본이다.
func With(q url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

type client struct {
	base string
	http *http.Client
}

const requestTimeout = 2 * time.Minute

// New 는 base URL 에 묶인 Client 다. 연결은 하지 않는다.
func New(baseURL string) Client {
	return &client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: requestTimeout}}
}

func (c *client) Do(ctx context.Context, method, path string, body any, query url.Values, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("OpenSearch %s %s 요청 직렬화 실패: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	return c.send(ctx, method, path, reader, "application/json", query, out)
}

func (c *client) Bulk(ctx context.Context, lines []any, query url.Values) (BulkResponse, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			return BulkResponse{}, fmt.Errorf("_bulk 직렬화 실패: %w", err)
		}
	}
	var out BulkResponse
	err := c.send(ctx, http.MethodPost, "/_bulk", &buf, "application/x-ndjson", query, &out)
	return out, err
}

func (c *client) send(ctx context.Context, method, path string, body io.Reader, contentType string, query url.Values, out any) error {
	op := method + " " + path
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return &Error{Op: op, Reason: err.Error()}
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Op: op, Reason: err.Error()}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Op: op, Status: resp.StatusCode, Reason: err.Error()}
	}
	if resp.StatusCode >= 300 {
		return parseError(op, resp.StatusCode, payload)
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return &Error{Op: op, Status: resp.StatusCode, Reason: "응답 디코드 실패: " + err.Error()}
	}
	return nil
}

// Ping 은 클러스터에 닿는지 본다 (GET /). 읽기 도구의 준비다 — 아무것도 쓰지 않는다.
func Ping(ctx context.Context, c Client) error {
	return c.Do(ctx, http.MethodGet, "/", nil, nil, nil)
}

// cause 는 OpenSearch 에러 본문의 type·reason 한 쌍이다.
type cause struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// errorDetail 은 OpenSearch 에러 본문의 error 객체다. 검색 실패의 최상위 reason 은 "all shards failed" 뿐이고 원인은
// root_cause(없으면 failed_shards[].reason)에 있다.
type errorDetail struct {
	cause
	RootCause    []cause `json:"root_cause"`
	FailedShards []struct {
		Reason cause `json:"reason"`
	} `json:"failed_shards"`
}

// rootCause 는 최상위와 다른 첫 원인이다 (없으면 빈 값).
func (d errorDetail) rootCause() cause {
	candidates := append([]cause(nil), d.RootCause...)
	for _, f := range d.FailedShards {
		candidates = append(candidates, f.Reason)
	}
	for _, c := range candidates {
		if c.Reason != "" && c != d.cause {
			return c
		}
	}
	return cause{}
}

func parseError(op string, status int, payload []byte) error {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	e := &Error{Op: op, Status: status, Reason: strings.TrimSpace(string(payload))}
	if json.Unmarshal(payload, &body) != nil || len(body.Error) == 0 {
		return e
	}
	var detail errorDetail
	if json.Unmarshal(body.Error, &detail) == nil && detail.Type != "" {
		e.Type, e.Reason = detail.Type, detail.Reason
		if rc := detail.rootCause(); rc.Reason != "" {
			e.Reason += " — " + rc.Type + ": " + rc.Reason
		}
		return e
	}
	var s string
	if json.Unmarshal(body.Error, &s) == nil {
		e.Reason = s
	}
	return e
}
