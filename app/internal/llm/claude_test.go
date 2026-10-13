package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
)

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env bash\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// fakeTimeout 은 가짜 claude 의 시간 상한이다. 가짜는 곧바로 끝나므로 상한은 결과를 가르지 않는다 — 부하가 걸린 머신에서
// 새로 만든 실행 파일의 첫 기동(macOS 의 보안 검사 포함)이 몇 초씩 걸려도 시간 초과로 오판하지 않게 넉넉히 둔다.
// 시간 초과·취소 경로는 이미 끝난 ctx 로 따로 본다 (TestAskGivesUpWhenCancelled). 정말 멈추면 go test -timeout 이 끊는다.
const fakeTimeout = 24 * time.Hour

func asker(bin string, logs *[]string) Asker {
	return New(config.LLM{ClaudeBin: bin, Model: "opus", Timeout: fakeTimeout}, func(f string, a ...any) {
		*logs = append(*logs, f)
	})
}

var input = Input{System: "s", User: "본문", Schema: map[string]any{"type": "object"}, Tag: "t"}

func TestAskStructuredOutputAndIsolation(t *testing.T) {
	dir := t.TempDir()
	bin := fakeClaude(t, `printf '%s\n' "$@" > `+dir+`/args; cat > `+dir+`/stdin; echo "${ANTHROPIC_API_KEY:-none}" > `+dir+`/key
echo 'warning line'
echo '{"subtype":"success","structured_output":{"ok":true}}'`)
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	var logs []string
	raw, ok := asker(bin, &logs).Ask(context.Background(), input)
	var out struct{ OK bool }
	if !ok || json.Unmarshal(raw, &out) != nil || !out.OK {
		t.Fatalf("ask = %s %v %v", raw, ok, logs)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, f := range []string{"--strict-mcp-config", "--setting-sources", "--json-schema", "--disable-slash-commands"} {
		if !strings.Contains(string(args), f) {
			t.Errorf("격리 플래그 %s 없음", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(b) != "본문" {
		t.Errorf("stdin = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "key")); strings.TrimSpace(string(b)) != "none" {
		t.Error("API 키는 자식 프로세스에서 지운다")
	}
}

func TestAskResultFallback(t *testing.T) {
	var logs []string
	raw, ok := asker(fakeClaude(t, `echo '{"subtype":"success","result":"답: {\"a\":1}"}'`), &logs).Ask(context.Background(), input)
	if !ok || string(raw) != `{"a":1}` {
		t.Errorf("result fallback = %s %v", raw, ok)
	}
}

func TestAskFailuresAreFallbacks(t *testing.T) {
	cases := map[string]string{
		"is_error":  `echo '{"subtype":"error_max_turns","is_error":true}'`,
		"exit":      `echo nope >&2; exit 3`,
		"not json":  `echo not json`,
		"no object": `echo '{"subtype":"success","result":"없음"}'`,
	}
	for name, script := range cases {
		var logs []string
		if _, ok := asker(fakeClaude(t, script), &logs).Ask(context.Background(), input); ok || len(logs) == 0 {
			t.Errorf("%s: ok=%v logs=%v", name, ok, logs)
		}
	}
	var logs []string
	if _, ok := asker("/nonexistent/claude", &logs).Ask(context.Background(), input); ok {
		t.Error("미설치는 실패")
	}
}

// R2-deadcode-4: 호출이 끊기면(시간 초과·종료) 폴백이다 — 벽시계와 경주하지 않게 이미 끝난 ctx 로 본다.
func TestAskGivesUpWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs []string
	if _, ok := asker(fakeClaude(t, `echo '{"subtype":"success","structured_output":{"ok":true}}'`), &logs).Ask(ctx, input); ok || len(logs) == 0 {
		t.Errorf("끊긴 호출은 폴백: ok=%v logs=%v", ok, logs)
	}
}

func TestParseEnvelope(t *testing.T) {
	env, ok := ParseEnvelope("{\"subtype\":\"a\"}\nnoise\n{\"subtype\":\"b\"}")
	if !ok || env.Subtype != "b" {
		t.Errorf("env = %+v", env)
	}
	if _, ok := ParseEnvelope("nothing"); ok {
		t.Error("없으면 false")
	}
}
