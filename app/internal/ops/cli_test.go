package ops

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// deadOpenSearch 는 아무도 듣지 않는 주소다 — 사용 오류는 OpenSearch 에 묻기 전에 나야 하므로, 묻게 되면 연결 실패가 난다.
const deadOpenSearch = "http://127.0.0.1:1"

// offlineCLI 는 helpCLI 에 붙지 못하는 OpenSearch 의 Handlers 를 단 CLI 다.
func offlineCLI(t *testing.T) *CLI {
	t.Helper()
	c := helpCLI(t, &bytes.Buffer{})
	c.Config.OpenSearchURL = deadOpenSearch
	c.OS = opensearch.New(deadOpenSearch)
	c.Handlers = mcpserver.NewHandlers(mcpserver.Deps{Config: c.Config, OS: c.OS, Log: t.Logf})
	return c
}

// R2-deadcode-1: 하위 명령 앞에 값을 받는 플래그를 두면(graph -depth 3 neighbors p) 값이 있는데도 "flag needs an argument" 라고
// 잘못 알렸다. 하위 명령을 먼저 쓰라는 사용 오류다 ('-h' 는 그대로 도움말).
func TestSubcommandAfterAFlagIsAPlainUsageError(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context, *CLI, []string) (string, error)
		args []string
	}{
		{"graph", runGraph, []string{"-depth", "3", "neighbors", "docs/a.md"}},
		{"archive", runArchive, []string{"-team", "t", "deprecate", "-by", "b", "-reason", "r", "docs/a.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.run(context.Background(), helpCLI(t, &bytes.Buffer{}), c.args)

			if !errors.Is(err, ErrUsage) {
				t.Fatalf("%s %v = %v", c.name, c.args, err)
			}
			if strings.Contains(err.Error(), "needs an argument") || !strings.Contains(err.Error(), "하위 명령을") {
				t.Errorf("하위 명령을 먼저 쓰라고 알려야 한다: %v", err)
			}
		})
	}
}

// RUN-16: 질의어 뒤에 붙인 이 명령의 플래그는 flag 패키지가 읽지 않아 말없이 질의어가 됐다 (search RefundSplitter -topic payments
// 가 topic 필터 없이 "RefundSplitter -topic payments" 를 찾았다). 플래그는 앞에 두라는 사용 오류다 — OpenSearch 에 묻기 전에.
func TestFlagAfterTheQueryIsAUsageError(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context, *CLI, []string) (string, error)
		args []string
		flag string
	}{
		{"search", runSearch, []string{"RefundSplitter", "-topic", "payments"}, "-topic"},
		{"search --flag=value", runSearch, []string{"RefundSplitter", "--size=3"}, "--size=3"},
		{"filters", runFilters, []string{"payments", "-superseded"}, "-superseded"},
		{"graph neighbors", runGraph, []string{"neighbors", "docs/a.md", "-depth", "1"}, "-depth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.run(context.Background(), offlineCLI(t), c.args)

			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "플래그 "+c.flag+" 는") || !strings.Contains(err.Error(), "앞에") {
				t.Errorf("%v = %v — 플래그 %s 는 앞에 두라고 알려야 한다", c.args, err, c.flag)
			}
		})
	}
}

// RUN-16: 이 명령의 플래그가 아닌 '-' 글자와 '--' 뒤의 인자는 질의어로 그대로 둔다 — 일부러 찾는 글자일 수 있다.
func TestDashWordsThatAreNotFlagsStayInTheQuery(t *testing.T) {
	cases := []struct {
		args, want []string
	}{
		{[]string{"timeout", "-1"}, []string{"timeout", "-1"}},
		{[]string{"--", "RefundSplitter", "-topic"}, []string{"RefundSplitter", "-topic"}},
		{[]string{"RefundSplitter", "--", "-topic"}, []string{"RefundSplitter", "--", "-topic"}},
	}
	for _, c := range cases {
		f := newFlags("search")
		f.String("topic", "", "주제")

		rest, err := f.parse(c.args)

		if err != nil || !reflect.DeepEqual(rest, c.want) {
			t.Errorf("parse(%v) = %v %v — 질의어로 남아야 한다 (%v)", c.args, rest, err, c.want)
		}
	}
}
