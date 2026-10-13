package llm

import (
	"context"
	"encoding/json"
	"fmt"
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

// R5-final-daemon-3: 호출한 쪽이 끊은 호출(데몬 종료)은 폴백이 아니다 — 판정은 그 쌍을 질문(폴백)으로 만들지 않고 대기분으로 남긴다.
// 로그도 '폴백' 이라 하지 않는다. 이 호출의 시간 상한(MEMORY_LLM_TIMEOUT_MS)에 걸린 것은 그대로 폴백이다.
func TestAskDoesNotCallACancelledCallAFallback(t *testing.T) {
	bin := fakeClaude(t, `echo '{"subtype":"success","structured_output":{"ok":true}}'`)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var logs []string

	if _, ok := asker(bin, &logs).Ask(cancelled, input); ok || len(logs) != 1 {
		t.Fatalf("끊긴 호출은 답 없이 한 줄을 남긴다: ok=%v logs=%v", ok, logs)
	}
	if strings.Contains(logs[0], "폴백") || !strings.Contains(logs[0], "끊") {
		t.Errorf("호출한 쪽이 끊은 호출을 폴백이라 한다: %q", logs[0])
	}

	logs = nil
	timedOut := New(config.LLM{ClaudeBin: bin, Model: "opus", Timeout: time.Nanosecond}, func(f string, a ...any) { logs = append(logs, f) })
	if _, ok := timedOut.Ask(context.Background(), input); ok || len(logs) != 1 || !strings.Contains(logs[0], "폴백") {
		t.Errorf("시간 상한에 걸린 호출은 폴백이다: ok=%v logs=%v", ok, logs)
	}
}

func TestParseEnvelope(t *testing.T) {
	env, ok := parseEnvelope("{\"subtype\":\"a\"}\nnoise\n{\"subtype\":\"b\"}")
	if !ok || env.Subtype != "b" {
		t.Errorf("env = %+v", env)
	}
	if _, ok := parseEnvelope("nothing"); ok {
		t.Error("없으면 false")
	}
}

// heldFor 는 가짜 claude 가 남긴 자식이 stdout 을 쥐고 있는 시간이다 — 기다리지 않는다면 이 시간보다 한참 먼저 돌아온다.
const heldFor = 20 * time.Second

// R4-daemon-b-7: 끊긴 호출(시간 초과·데몬 종료)은 claude 가 띄운 자식이 stdout 을 쥐고 있어도 그 자식을 기다리지 않는다 — 예전에는
// claude 를 죽인 뒤에도 Wait 가 파이프가 닫힐 때까지(자식이 끝날 때까지) 묶여, 데몬 종료가 LLM 시간 상한만큼 늦었다.
func TestAskDoesNotWaitForAChildHoldingStdoutAfterCancel(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	bin := fakeClaude(t, fmt.Sprintf("touch %s\nsleep %d\necho '{}'", started, int(heldFor.Seconds())))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	var logs []string
	go func() {
		_, ok := asker(bin, &logs).Ask(ctx, input)
		done <- ok
	}()
	waitForFile(t, started)

	cancel()
	begin := time.Now()

	select {
	case ok := <-done:
		if ok {
			t.Error("끊긴 호출은 폴백이다")
		}
		if waited := time.Since(begin); waited > heldFor/2 {
			t.Errorf("자식이 끝나기를 %v 기다렸다", waited)
		}
	case <-time.After(heldFor + 5*time.Second):
		t.Fatal("끊긴 호출이 돌아오지 않는다")
	}
}

// R4-daemon-b-7: claude 가 답을 쓰고 끝났는데 남은 자식이 stdout 을 쥐고 있으면, 그 자식을 기다리지 않고 받은 답을 쓴다.
func TestAskKeepsTheAnswerWhenAChildHoldsStdout(t *testing.T) {
	bin := fakeClaude(t, fmt.Sprintf("sleep %d &\necho '{\"subtype\":\"success\",\"structured_output\":{\"ok\":true}}'", int(heldFor.Seconds())))
	var logs []string
	begin := time.Now()

	raw, ok := asker(bin, &logs).Ask(context.Background(), input)

	if !ok || string(raw) != `{"ok":true}` {
		t.Errorf("답 = %s %v %v", raw, ok, logs)
	}
	if waited := time.Since(begin); waited > heldFor/2 {
		t.Errorf("자식이 끝나기를 %v 기다렸다", waited)
	}
}

// waitForFile 은 가짜 claude 가 파일을 만들 때까지 기다린다 (새 실행 파일의 첫 기동이 느린 머신에서도).
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(heldFor)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("가짜 claude 가 %s 를 만들지 않았다", path)
}
