package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drakejin/workspace/app/internal/bench"
)

func runBench(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("bench")
	questions := f.String("questions", "", "질문 YAML (meta-repo 밖)")
	model := f.String("model", "haiku", "claude 모델")
	modes := f.String("modes", bench.ModeWith+","+bench.ModeWithout, "조건")
	runs := f.Int("runs", 1, "조건마다 회차")
	maxTurns := f.Int("max-turns", 15, "질문당 최대 턴")
	script := f.String("mcp-script", defaultMCPScript(), "scripts/mcp/run.sh 절대 경로")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	if *questions == "" || *runs < 1 || *maxTurns < 1 {
		return "", f.usageErr()
	}
	if err := questionsOutside(c.Config.MetaRepoRoot, *questions); err != nil {
		return "", err
	}
	if _, err := exec.LookPath(c.Config.LLM.ClaudeBin); err != nil {
		return "", fmt.Errorf("bench: claude CLI(%s)를 찾을 수 없습니다 — MEMORY_CLAUDE_BIN: %w", c.Config.LLM.ClaudeBin, err)
	}
	suite, err := bench.LoadSuite(*questions)
	if err != nil {
		return "", err
	}
	opt := bench.Options{
		Root: c.Config.MetaRepoRoot, MCPScript: *script, Model: *model, Runs: *runs, MaxTurns: *maxTurns, RulesFile: rulesFile(c.Config.MetaRepoRoot),
		Modes: strings.Split(*modes, ","),
		MCPEnv: []string{
			"META_REPO_ROOT=" + c.Config.MetaRepoRoot, "MEMORY_DEFAULT_TEAM=" + c.Config.DefaultTeam,
			"MEMORY_WORKSPACE=" + c.Config.Workspace, "OPENSEARCH_URL=" + c.Config.OpenSearchURL,
		},
	}
	results, err := bench.Run(ctx, suite, opt, claudeRunner(c.Config.LLM.ClaudeBin))
	if err != nil {
		return "", err
	}
	report := bench.Render(results, *model, c.Now())
	dir := filepath.Join(c.Config.MetaRepoRoot, ".cache", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("bench report: %w", err)
	}
	path := filepath.Join(dir, c.Now().Format("20060102-150405")+".md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		return "", fmt.Errorf("bench report: %w", err)
	}
	return report + "\n(저장: " + path + ")", nil
}

// questionsOutside 는 질문 파일이 meta-repo 밖에 있는지 본다. 안에 있으면 without 조건의 에이전트가
// Grep 으로 expect(정답)를 읽어 측정이 무의미해진다.
func questionsOutside(root, questions string) error {
	r, q := resolved(root), resolved(questions)
	rel, err := filepath.Rel(r, q)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("bench: 질문 파일 %s 가 meta-repo 안에 있습니다 — 에이전트가 정답을 Grep 할 수 있으니 밖에 두세요", questions)
	}
	return nil
}

// rulesFile 은 meta-repo 의 CLAUDE.md 경로다. 없으면 빈 문자열.
func rulesFile(root string) string {
	p := filepath.Join(root, "CLAUDE.md")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// resolved 는 절대 경로이고 심볼릭 링크를 푼 경로다 (macOS 의 /var → /private/var 처럼). 실패하면 절대 경로만.
func resolved(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// defaultMCPScript 는 이 바이너리(.cache/bin/memory-mcp) 옆의 레포에서 scripts/mcp/run.sh 를 찾는다.
func defaultMCPScript() string {
	exe, err := os.Executable()
	if err != nil {
		return "scripts/mcp/run.sh"
	}
	return filepath.Join(filepath.Dir(exe), "..", "..", "scripts", "mcp", "run.sh")
}

func claudeRunner(bin string) bench.Runner {
	return func(ctx context.Context, dir string, args []string) (string, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		cmd.Stdin = nil
		out, err := cmd.Output()
		if err != nil {
			return string(out), fmt.Errorf("claude: %w", err)
		}
		return string(out), nil
	}
}
