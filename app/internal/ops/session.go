package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/search"
	"github.com/drakejin/workspace/app/internal/state"
)

const (
	sessionTimeout = 5 * time.Second
	sessionHits    = 5
)

// hookInput 은 Claude Code SessionStart 훅이 stdin 으로 주는 JSON 중 쓰는 부분이다.
type hookInput struct {
	Cwd    string `json:"cwd"`
	Source string `json:"source"` // startup | resume | clear | compact
}

var wordSplit = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// runSessionContext 는 SessionStart 훅이다. 세션이 열린 디렉토리가 main/{repo}(또는 그 worktree)면 repo·브랜치 이름으로
// 메모리를 검색해 맥락으로 넣는다 — 에이전트가 search_memory 를 빼먹어도 지난 history 가 앞에 붙는다.
// 훅은 세션을 막으면 안 되므로 어떤 실패도 조용히 넘긴다 (출력 없음 = 맥락 없음).
func runSessionContext(ctx context.Context, c *CLI, _ []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()
	var in hookInput
	if c.In != nil {
		b, _ := io.ReadAll(io.LimitReader(c.In, 1<<20))
		_ = json.Unmarshal(b, &in)
	}
	repo, dir := repoOf(c.Config.MetaRepoRoot, in.Cwd)
	terms := []string{}
	if repo != "" {
		terms = append(terms, repo)
	}
	terms = append(terms, branchWords(ctx, dir)...)

	var lines []string
	if len(terms) > 0 && c.Handlers.Ready(ctx) == nil {
		hits, err := search.Search(ctx, c.OS, search.Request{
			Workspace: c.Config.Workspace, Team: c.Config.DefaultTeam, Repo: repo, Query: strings.Join(terms, " "), Size: sessionHits,
		})
		if err == nil && len(hits) == 0 && repo != "" {
			// repo 필터로 아무것도 없으면 repo 이름이 들어간 문서 전체에서 다시 찾는다
			hits, err = search.Search(ctx, c.OS, search.Request{Workspace: c.Config.Workspace, Team: c.Config.DefaultTeam, Query: strings.Join(terms, " "), Size: sessionHits})
		}
		if err == nil && len(hits) > 0 {
			lines = append(lines, fmt.Sprintf("## memory — 이 세션과 관련된 문서 (자동 검색: %q)", strings.Join(terms, " ")))
			for _, h := range hits {
				lines = append(lines, fmt.Sprintf("- %s › %s (%s, %s)", h.SourcePath, h.HeaderPath, h.DocType, model.Day(h.EventTime)))
			}
			lines = append(lines, "근거가 필요하면 위 경로를 Read 하고, 더 필요하면 search_memory 로 찾는다.")
		}
	}
	if qs, err := state.NewJudgeState(c.Config.MetaRepoRoot).AllPendingQuestions(); err == nil {
		n := 0
		for _, q := range qs {
			n += len(q)
		}
		if n > 0 {
			lines = append(lines, fmt.Sprintf("(메모리 판정 질문 %d건이 답을 기다린다 — 사용자에게 'make questions' 를 알려 줄 것)", n))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// repoOf 는 cwd 가 가리키는 관리 대상 repository 와 그 작업 디렉토리다 (main/{repo}/... 또는 main/{repo}/.worktrees/{issue}/...).
func repoOf(root, cwd string) (string, string) {
	if cwd == "" {
		return "", ""
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", cwd
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] != "main" {
		return "", cwd
	}
	dir := filepath.Join(root, "main", parts[1])
	if len(parts) >= 4 && parts[2] == ".worktrees" {
		dir = filepath.Join(dir, ".worktrees", parts[3])
	}
	return parts[1], dir
}

// branchWords 는 작업 브랜치 이름을 검색어로 쪼갠다 (main·master 는 뺀다).
func branchWords(ctx context.Context, dir string) []string {
	if dir == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return nil
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "main" || branch == "master" {
		return nil
	}
	var words []string
	for _, w := range wordSplit.Split(branch, -1) {
		if len([]rune(w)) > 1 {
			words = append(words, w)
		}
	}
	return words
}
