// Package llm 은 로컬 Claude Code CLI 를 LLM 백엔드로 쓴다 — API 키 없이, 머신에 로그인된 구독 인증으로.
//
// 격리: cwd=임시 디렉토리 + --setting-sources "" + --strict-mcp-config + --tools "" + --disable-slash-commands
// → 이 레포의 CLAUDE.md·MCP·훅이 판정 프롬프트에 새지 않는다.
// 실패(미설치·미인증·타임아웃·스키마 불일치)는 에러가 아니라 ok=false — 호출측이 폴백 경로를 갖는다.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
)

// Input 은 질의 하나다.
type Input struct {
	System string
	User   string // stdin 으로 넘긴다 (argv 길이 제한 회피)
	Schema map[string]any
	Tag    string // 로그 접두 — 호출한 게이트 이름
}

// Asker 는 구조화된 답을 돌려주는 LLM 이다. 답을 못 얻으면 ok=false.
type Asker interface {
	Ask(ctx context.Context, in Input) (json.RawMessage, bool)
}

// Logf 는 로그 함수다.
type Logf func(format string, args ...any)

type claude struct {
	cfg config.LLM
	log Logf
}

// New 는 로컬 claude CLI Asker 다.
func New(cfg config.LLM, log Logf) Asker { return &claude{cfg: cfg, log: log} }

var jsonObject = regexp.MustCompile(`(?s)\{.*\}`)

func (c *claude) Ask(ctx context.Context, in Input) (json.RawMessage, bool) {
	schema, err := json.Marshal(in.Schema)
	if err != nil {
		c.log("[%s] 스키마 직렬화 실패: %v", in.Tag, err)
		return nil, false
	}
	stdout, err := c.run(ctx, []string{
		"-p",
		"--output-format", "json",
		"--model", c.cfg.Model,
		"--system-prompt", in.System,
		"--json-schema", string(schema),
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--setting-sources", "",
	}, in.User)
	if err != nil && ctx.Err() != nil {
		// 호출한 쪽이 끊었다(데몬 종료) — LLM 이 답하지 못한 것(폴백)이 아니다. 판정은 그 쌍을 질문으로 만들지 않고 대기분으로 남긴다
		c.log("[%s] 로컬 claude 호출이 끊겼습니다 — 답 없이 돌아간다: %v", in.Tag, ctx.Err())
		return nil, false
	}
	if err != nil {
		c.log("[%s] 로컬 claude 호출 실패 — 폴백: %v", in.Tag, err)
		return nil, false
	}
	env, ok := parseEnvelope(stdout)
	if !ok || env.IsError || env.Subtype != "success" {
		c.log("[%s] 로컬 claude 실패(subtype=%s) — 폴백", in.Tag, env.Subtype)
		return nil, false
	}
	if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
		return env.StructuredOutput, true
	}
	if m := jsonObject.FindString(env.Result); m != "" && json.Valid([]byte(m)) {
		return json.RawMessage(m), true
	}
	c.log("[%s] 로컬 claude 출력에 JSON 없음 — 폴백", in.Tag)
	return nil, false
}

// pipeWaitDelay 는 claude 가 끝난 뒤(또는 끊겨 죽인 뒤) 그 출력 파이프가 닫히기를 기다리는 한도다. claude 가 띄운 자식이
// stdout·stderr 를 물려받아 쥐고 있으면 파이프가 그 자식이 끝날 때까지 열려 있다 — 한도가 없으면 Wait 가 거기 묶여 데몬 종료가
// LLM 시간 상한만큼 늦는다. 이미 쓴 출력은 그 사이 다 읽힌다.
const pipeWaitDelay = 2 * time.Second

func (c *claude) run(ctx context.Context, args []string, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.ClaudeBin, args...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = pipeWaitDelay
	// 키가 있으면 CLI 가 구독 대신 API 과금 경로를 탄다
	cmd.Env = withoutKeys(os.Environ(), "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// ErrWaitDelay 는 claude 가 성공으로 끝났는데 남은 자식이 파이프를 쥐고 있었다는 뜻이다 — 받은 답을 쓴다
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("`%s` 미설치/PATH 부재", c.cfg.ClaudeBin)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		first, _, _ := strings.Cut(msg, "\n")
		return "", errors.New(first)
	}
	return stdout.String(), nil
}

func withoutKeys(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// envelope 는 `claude -p --output-format json` 의 한 줄 응답이다.
type envelope struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// parseEnvelope 는 마지막 JSON 줄을 envelope 으로 읽는다 (앞에 경고 줄이 섞여도 된다).
func parseEnvelope(stdout string) (envelope, bool) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env envelope
		if json.Unmarshal([]byte(line), &env) == nil {
			return env, true
		}
	}
	return envelope{}, false
}
