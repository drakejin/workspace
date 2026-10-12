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

	"github.com/drakejin/workspace/internal/config"
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
	if err != nil {
		c.log("[%s] 로컬 claude 호출 실패 — 폴백: %v", in.Tag, err)
		return nil, false
	}
	env, ok := ParseEnvelope(stdout)
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

func (c *claude) run(ctx context.Context, args []string, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.ClaudeBin, args...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	// 키가 있으면 CLI 가 구독 대신 API 과금 경로를 탄다
	cmd.Env = withoutKeys(os.Environ(), "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
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

// Envelope 는 `claude -p --output-format json` 의 한 줄 응답이다.
type Envelope struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// ParseEnvelope 는 마지막 JSON 줄을 envelope 으로 읽는다 (앞에 경고 줄이 섞여도 된다).
func ParseEnvelope(stdout string) (Envelope, bool) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env Envelope
		if json.Unmarshal([]byte(line), &env) == nil {
			return env, true
		}
	}
	return Envelope{}, false
}
