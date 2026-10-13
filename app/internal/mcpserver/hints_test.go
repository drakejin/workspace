package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
)

// routeOS 는 경로별 JSON 답을 주는 OpenSearch 다. 없는 경로는 빈 답(검색 0건, 스냅샷 없음)이다.
type routeOS map[string]string

func (r routeOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	body, ok := r[path]
	if !ok || out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

func (routeOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// hintHandlers 는 팀 ads 의 cold 인덱스 하나(mem-ads-2026.06)만 있고 hot 검색은 0건인 Handlers 다.
func hintHandlers(teams string, store cold.Store) *Handlers {
	os := routeOS{
		"/_snapshot/mem-cold/_all": `{"snapshots":[{"snapshot":"cold-mem-ads-2026.06","indices":["mem-ads-2026.06"],"state":"SUCCESS","start_time":"2026-10-01T00:00:00Z"}]}`,
		"/mem-*/_search":           `{"aggregations":{"teams":{"buckets":[` + teams + `]}}}`,
	}
	cfg := config.Config{Workspace: "ws", DefaultTeam: "ads", HotDays: 60, Snapshot: config.Snapshot{Repository: "mem-cold"}}
	return &Handlers{Deps: Deps{Config: cfg, OS: os, Cold: store}, ready: true}
}

// API-10: 안내문은 MCP 도구와 CLI(scripts/memory/run.sh <command>)가 같이 본다 — MCP 도구 이름만 적으면 CLI 사용자는
// 칠 명령(filters·list-cold·restore <index>)을 모른다. 두 이름을 함께 적는다.
func TestHintsNameBothMCPToolAndCLICommand(t *testing.T) {
	h := hintHandlers(`{"key":"ads"}`, nil)
	ctx := context.Background()
	cases := []struct {
		name string
		call func() (string, error)
		want []string
	}{
		{"필터만 준 검색이 비면", func() (string, error) { return h.Search(ctx, SearchInput{Project: "p"}) },
			[]string{"search_filters", "CLI: filters"}},
		{"질의 검색이 비면", func() (string, error) { return h.Search(ctx, SearchInput{Query: "x"}) },
			[]string{"list_cold", "CLI: list-cold"}},
		{"cold 목록 끝의 복원 안내", func() (string, error) { return h.ListCold(ctx, ListColdInput{}) },
			[]string{"mem-ads-2026.06", "restore_cold(index, confirmed=true)", "CLI: restore <index>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.call()

			if err != nil {
				t.Fatal(err)
			}
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("%q 가 없다:\n%s", want, out)
				}
			}
		})
	}
}

// API-10: 에러 안내도 두 이름을 함께 적는다 — CLI 의 search 는 -from/-to·-type 플래그이고, 필터 값은 filters 로 본다.
func TestErrorHintsNameBothMCPToolAndCLICommand(t *testing.T) {
	h := hintHandlers(``, cold.NewLocalStore(t.TempDir()))
	ctx := context.Background()

	_, empty := h.Search(ctx, SearchInput{IncludeSuperseded: true})
	_, topic := h.Search(ctx, SearchInput{Topic: "!!!"})
	_, notOurs := h.ListCold(ctx, ListColdInput{})

	checks := []struct {
		name string
		err  error
		is   error
		want []string
	}{
		{"질의어도 필터도 없음", empty, search.ErrEmptyQuery, []string{"time_range", "doc_type", "CLI", "-from", "-type"}},
		{"글자 없는 주제", topic, nil, []string{"search_filters", "CLI: filters"}},
		{"다른 workspace 의 팀", notOurs, ingest.ErrTeamOwned, []string{"list_cold", "CLI: list-cold", "restore"}},
	}
	for _, c := range checks {
		if c.err == nil || (c.is != nil && !errors.Is(c.err, c.is)) {
			t.Errorf("%s: err = %v", c.name, c.err)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(c.err.Error(), want) {
				t.Errorf("%s: %q 가 없다: %v", c.name, want, c.err)
			}
		}
	}
}

// DEAD-16: list_cold 는 팀 이름만 받는다 (org 를 팀으로 펼치지 않는다) — 입력 설명이 "팀/실" 이라고 org 를 약속하지 않는다.
func TestListColdSchemaNamesTeamOnly(t *testing.T) {
	field, ok := reflect.TypeOf(ListColdInput{}).FieldByName("Team")
	if !ok {
		t.Fatal("ListColdInput.Team 이 없다")
	}

	desc := field.Tag.Get("jsonschema")

	if strings.Contains(desc, "실") || strings.Contains(desc, "org") || !strings.Contains(desc, "팀 이름") {
		t.Errorf("list_cold team 설명 = %q", desc)
	}
}
