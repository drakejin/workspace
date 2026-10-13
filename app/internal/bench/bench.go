// Package bench 는 발표 데모 4단계다 — 같은 질문을 memory-mcp 가 있을 때와 없을 때 claude 에 시켜
// 도구 호출 수·턴·시간·토큰·비용·정답 여부를 나란히 잰다.
package bench

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Mode 는 측정 조건이다.
const (
	ModeWith    = "with"    // memory-mcp + 파일 도구
	ModeWithout = "without" // 파일 도구만
)

// fileTools 는 두 조건 모두에 허용하는 도구다 — 에이전트가 파일 트리를 직접 뒤질 수 있다.
var fileTools = []string{"Read", "Glob", "Grep"}

// memoryTools 는 with 조건에만 더하는 도구다.
var memoryTools = []string{
	"mcp__" + serverName + "__search_memory", "mcp__" + serverName + "__search_filters",
	"mcp__" + serverName + "__related_docs", "mcp__" + serverName + "__docs_by_entity",
}

// serverName 은 with 조건에 붙이는 MCP 서버 이름이다 — .mcp.json 과 같은 이름이라 CLAUDE.md 의 도구 이름이 그대로 맞는다.
const serverName = "drakejin-workspace-memory-mcp"

// Question 은 측정 질문 하나다. Expect 의 문자열이 답에 모두 들어 있으면 정답으로 본다.
type Question struct {
	ID     string   `yaml:"id"`
	Prompt string   `yaml:"prompt"`
	Expect []string `yaml:"expect"`
}

// Suite 는 질문 묶음이다.
type Suite struct {
	Questions []Question `yaml:"questions"`
}

// LoadSuite 는 YAML 질문 파일을 읽는다.
func LoadSuite(path string) (Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, fmt.Errorf("bench 질문 %s: %w", path, err)
	}
	var s Suite
	if err := yaml.Unmarshal(b, &s); err != nil {
		return Suite{}, fmt.Errorf("bench 질문 %s: %w", path, err)
	}
	if len(s.Questions) == 0 {
		return Suite{}, fmt.Errorf("bench 질문 %s: questions 가 비어 있습니다", path)
	}
	for i, q := range s.Questions {
		if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Prompt) == "" {
			return Suite{}, fmt.Errorf("bench 질문 %s: %d번째 항목에 id·prompt 가 필요합니다", path, i+1)
		}
	}
	return s, nil
}

// Result 는 질문 하나 × 조건 하나 × 회차 하나의 측정이다.
type Result struct {
	QuestionID   string
	Mode         string
	Run          int
	ToolCalls    map[string]int
	Turns        int
	DurationMs   int64
	InputTokens  int64 // 캐시 읽기·생성 포함
	OutputTokens int64
	CostUSD      float64
	Answer       string
	Correct      bool
	Err          string
}

// TotalTools 는 도구 호출 수다.
func (r Result) TotalTools() int {
	n := 0
	for _, v := range r.ToolCalls {
		n += v
	}
	return n
}

// MemoryTools 는 memory-mcp 도구 호출 수다.
func (r Result) MemoryTools() int {
	n := 0
	for name, v := range r.ToolCalls {
		if strings.HasPrefix(name, "mcp__"+serverName+"__") {
			n += v
		}
	}
	return n
}

// Runner 는 claude 를 한 번 실행하고 stdout 을 돌려준다. 테스트는 가짜를 쓴다.
type Runner func(ctx context.Context, dir string, args []string) (string, error)

// Options 는 측정 설정이다.
type Options struct {
	Root      string   // 질문을 던질 meta-repository (claude 의 작업 디렉토리)
	MCPScript string   // scripts/mcp/run.sh 절대 경로
	MCPEnv    []string // MCP 서버에 넘길 환경 (META_REPO_ROOT 등)
	Model     string
	Modes     []string
	Runs      int
	MaxTurns  int
	// RulesFile 은 meta-repo 의 CLAUDE.md 다. 측정은 사용자 설정을 끄고(--setting-sources "") 돌아 CLAUDE.md 가 자동으로
	// 읽히지 않으므로, 두 조건 모두에 같은 규칙으로 덧붙인다. 비면 덧붙이지 않는다.
	RulesFile string
}

// Run 은 질문마다 조건마다 Runs 회 측정한다. 실패한 회차는 Err 에 남기고 계속한다.
func Run(ctx context.Context, suite Suite, opt Options, run Runner) ([]Result, error) {
	mcpConfig, cleanup, err := writeMCPConfig(opt)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var results []Result
	for _, q := range suite.Questions {
		for _, mode := range opt.Modes {
			for i := 1; i <= opt.Runs; i++ {
				args := claudeArgs(q.Prompt, mode, opt, mcpConfig)
				start := time.Now()
				stdout, err := run(ctx, opt.Root, args)
				r := ParseStream(stdout)
				r.QuestionID, r.Mode, r.Run = q.ID, mode, i
				if r.DurationMs == 0 {
					r.DurationMs = time.Since(start).Milliseconds()
				}
				if err != nil {
					r.Err = err.Error()
				}
				r.Correct = r.Err == "" && containsAll(r.Answer, q.Expect)
				results = append(results, r)
			}
		}
	}
	return results, nil
}

func claudeArgs(prompt, mode string, opt Options, mcpConfig map[string]string) []string {
	tools := append([]string(nil), fileTools...)
	if mode == ModeWith {
		tools = append(tools, memoryTools...)
	}
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--model", opt.Model,
		"--max-turns", fmt.Sprint(opt.MaxTurns),
		// 사용자 설정(훅·플러그인)이 측정에 섞이지 않게 끈다. 두 조건의 차이는 MCP 서버 유무뿐이다
		"--setting-sources", "",
		"--strict-mcp-config", "--mcp-config", mcpConfig[mode],
		"--disable-slash-commands", "--no-session-persistence",
		// --tools 는 쓸 수 있는 내장 도구를 파일 셋으로 좁히고, --allowedTools 는 묻지 않고 허용한다 (MCP 도구 포함)
		"--tools", strings.Join(fileTools, ","),
		"--allowedTools", strings.Join(tools, ","),
	}
	if opt.RulesFile != "" {
		args = append(args, "--append-system-prompt-file", opt.RulesFile)
	}
	return args
}

func writeMCPConfig(opt Options) (map[string]string, func(), error) {
	dir, err := os.MkdirTemp("", "memory-bench-")
	if err != nil {
		return nil, func() {}, err
	}
	env := map[string]string{}
	for _, kv := range opt.MCPEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	configs := map[string]any{
		ModeWith:    map[string]any{"mcpServers": map[string]any{serverName: map[string]any{"command": "bash", "args": []string{opt.MCPScript}, "env": env}}},
		ModeWithout: map[string]any{"mcpServers": map[string]any{}},
	}
	paths := map[string]string{}
	for mode, cfg := range configs {
		b, _ := json.Marshal(cfg)
		p := filepath.Join(dir, mode+".json")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, func() {}, err
		}
		paths[mode] = p
	}
	return paths, func() { os.RemoveAll(dir) }, nil
}

// ParseStream 은 claude -p --output-format stream-json 출력에서 도구 호출과 result 를 읽는다.
func ParseStream(stdout string) Result {
	r := Result{ToolCalls: map[string]int{}}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content"`
			} `json:"message"`
			DurationMs int64   `json:"duration_ms"`
			NumTurns   int     `json:"num_turns"`
			Cost       float64 `json:"total_cost_usd"`
			Result     string  `json:"result"`
			IsError    bool    `json:"is_error"`
			Usage      struct {
				Input       int64 `json:"input_tokens"`
				Output      int64 `json:"output_tokens"`
				CacheRead   int64 `json:"cache_read_input_tokens"`
				CacheCreate int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					r.ToolCalls[c.Name]++
				}
			}
		case "result":
			r.DurationMs, r.Turns, r.CostUSD, r.Answer = ev.DurationMs, ev.NumTurns, ev.Cost, ev.Result
			r.InputTokens = ev.Usage.Input + ev.Usage.CacheRead + ev.Usage.CacheCreate
			r.OutputTokens = ev.Usage.Output
			if ev.IsError {
				r.Err = "claude result is_error"
			}
		}
	}
	return r
}

func containsAll(answer string, expect []string) bool {
	a := strings.ToLower(answer)
	for _, e := range expect {
		if !strings.Contains(a, strings.ToLower(e)) {
			return false
		}
	}
	return true
}

// Render 는 측정 결과를 마크다운 표로 쓴다 — 질문별 행과 조건별 평균.
func Render(results []Result, model string, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# memory-mcp 유무 비교 — %s (model %s)\n\n", at.Local().Format("2006-01-02 15:04"), model)
	b.WriteString("| 질문 | 조건 | 회차 | 정답 | 도구 호출 (memory) | 턴 | 시간 | 입력 토큰 | 출력 토큰 | 비용 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range results {
		correct := "✅"
		if !r.Correct {
			correct = "❌"
		}
		if r.Err != "" {
			correct = "⚠️ " + r.Err
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %d (%d) | %d | %.1fs | %d | %d | $%.4f |\n",
			r.QuestionID, r.Mode, r.Run, correct, r.TotalTools(), r.MemoryTools(), r.Turns, float64(r.DurationMs)/1000, r.InputTokens, r.OutputTokens, r.CostUSD)
	}
	b.WriteString("\n## 조건별 평균\n\n| 조건 | 정답률 | 도구 호출 | 턴 | 시간 | 입력 토큰 | 비용 |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	byMode := map[string][]Result{}
	for _, r := range results {
		byMode[r.Mode] = append(byMode[r.Mode], r)
	}
	modes := make([]string, 0, len(byMode))
	for m := range byMode {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	for _, m := range modes {
		rs := byMode[m]
		var correct, tools, turns int
		var dur, in int64
		var cost float64
		for _, r := range rs {
			if r.Correct {
				correct++
			}
			tools += r.TotalTools()
			turns += r.Turns
			dur += r.DurationMs
			in += r.InputTokens
			cost += r.CostUSD
		}
		n := float64(len(rs))
		fmt.Fprintf(&b, "| %s | %d/%d | %.1f | %.1f | %.1fs | %.0f | $%.4f |\n", m, correct, len(rs), float64(tools)/n, float64(turns)/n, float64(dur)/n/1000, float64(in)/n, cost/n)
	}
	return b.String()
}
