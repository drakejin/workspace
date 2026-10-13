package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"thinking"},{"type":"tool_use","name":"mcp__drakejin-workspace-memory-mcp__search_memory"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Read"}]}}
not json
{"type":"result","subtype":"success","duration_ms":6929,"num_turns":3,"total_cost_usd":0.06,"result":"재시도는 5회, 근거 docs/feature/payment-retry/2026-10-06-retry-policy.md","usage":{"input_tokens":18,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"output_tokens":319}}`

func TestParseStream(t *testing.T) {
	r := ParseStream(stream)
	if r.TotalTools() != 3 || r.MemoryTools() != 1 || r.Turns != 3 || r.DurationMs != 6929 || r.InputTokens != 318 || r.OutputTokens != 319 || r.CostUSD != 0.06 {
		t.Errorf("parsed = %+v", r)
	}
	if !strings.Contains(r.Answer, "5회") {
		t.Errorf("answer = %q", r.Answer)
	}
}

func TestRunAndRender(t *testing.T) {
	suite := Suite{Questions: []Question{{ID: "retry", Prompt: "몇 번?", Expect: []string{"5회", "2026-10-06"}}}}
	var calls [][]string
	runner := func(_ context.Context, dir string, args []string) (string, error) {
		calls = append(calls, args)
		if strings.Contains(strings.Join(args, " "), "mcp__drakejin-workspace-memory-mcp__search_memory") {
			return stream, nil
		}
		return `{"type":"result","num_turns":9,"result":"모르겠다"}`, errors.New("exit 1")
	}
	script := filepath.Join(t.TempDir(), "run.sh") // with 조건은 MCP 스크립트가 있어야 시작한다 (API-12)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	results, err := Run(context.Background(), suite, Options{Root: t.TempDir(), MCPScript: script, Model: "haiku", Modes: []string{ModeWith, ModeWithout}, Runs: 1, MaxTurns: 5, RulesFile: "/meta/CLAUDE.md"}, runner)
	if err != nil || len(results) != 2 {
		t.Fatalf("results = %+v %v", results, err)
	}
	if !results[0].Correct || results[1].Correct || results[1].Err == "" {
		t.Errorf("correct = %+v", results)
	}
	// 두 조건 모두 내장 도구는 파일 셋뿐이고 같은 규칙(CLAUDE.md)을 받는다 — 차이는 memory MCP 유무뿐
	for i, call := range calls {
		joined := strings.Join(call, " ")
		for _, want := range []string{"--strict-mcp-config", "--setting-sources", "--max-turns 5", "--model haiku", "--tools Read,Glob,Grep", "--append-system-prompt-file /meta/CLAUDE.md"} {
			if !strings.Contains(joined, want) {
				t.Errorf("call %d args 에 %q 없음: %s", i, want, joined)
			}
		}
	}
	if strings.Contains(strings.Join(calls[1], " "), "mcp__drakejin-workspace-memory-mcp__") {
		t.Errorf("without 조건에는 memory 도구가 없다: %v", calls[1])
	}
	md := Render(results, "haiku", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{"| retry | with | 1 | ✅ | 3 (1) | 3 |", "⚠️ exit 1", "조건별 평균", "| with | 1/1 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("render 에 %q 없음:\n%s", want, md)
		}
	}
}

func TestLoadSuite(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(good, []byte("questions:\n  - id: a\n    prompt: p\n    expect: [x]\n"), 0o644)
	if s, err := LoadSuite(good); err != nil || len(s.Questions) != 1 {
		t.Errorf("load = %+v %v", s, err)
	}
	for name, body := range map[string]string{"empty.yaml": "questions: []\n", "noid.yaml": "questions:\n  - prompt: p\n", "bad.yaml": ":\n- ["} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0o644)
		if _, err := LoadSuite(p); err == nil {
			t.Errorf("%s 는 실패해야 한다", name)
		}
	}
	if _, err := LoadSuite(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("없는 파일")
	}
}

// API-12: 맵에 없는 조건(wiht)이나 없는 MCP 스크립트로 Run 하면 claude 가 --mcp-config "" 로 돌아 memory 도구 없이 잰
// 결과가 보고서에 남았다. Run 은 claude 를 한 번도 부르지 않고 실패한다.
func TestRunRefusesUnknownModesAndAMissingScript(t *testing.T) {
	suite := Suite{Questions: []Question{{ID: "q", Prompt: "p"}}}
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		modes  []string
		script string
	}{
		{"오타", []string{"wiht"}, script},
		{"공백이 붙은 이름", []string{"with "}, script},
		{"빈 조건", nil, script},
		{"없는 스크립트", []string{ModeWith}, filepath.Join(t.TempDir(), "nope.sh")},
		{"스크립트 자리에 디렉토리", []string{ModeWith}, t.TempDir()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			runner := func(context.Context, string, []string) (string, error) { calls++; return "", nil }

			_, err := Run(context.Background(), suite, Options{Root: t.TempDir(), MCPScript: c.script, Modes: c.modes, Runs: 1, MaxTurns: 1}, runner)

			if err == nil || calls != 0 {
				t.Errorf("err = %v, claude 호출 %d번 — 시작 전에 거부해야 한다", err, calls)
			}
		})
	}
}

// API-12: without 만 재면 MCP 스크립트는 보지 않는다.
func TestValidateNeedsTheScriptOnlyForWith(t *testing.T) {
	opt := Options{MCPScript: filepath.Join(t.TempDir(), "nope.sh"), Modes: []string{ModeWithout}}

	if err := opt.Validate(); err != nil {
		t.Errorf("without 만 = %v", err)
	}
}

func TestParseModesTrimsNames(t *testing.T) {
	got := ParseModes(" with , without ")

	if len(got) != 2 || got[0] != ModeWith || got[1] != ModeWithout {
		t.Errorf("ParseModes = %q", got)
	}
}
