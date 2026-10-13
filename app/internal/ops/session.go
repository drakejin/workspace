package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/search"
	"github.com/drakejin/workspace/app/internal/state"
)

const (
	sessionTimeout = 5 * time.Second
	sessionHits    = 5
)

// hookInput 은 Claude Code SessionStart 훅이 stdin 으로 주는 JSON 중 쓰는 부분이다.
type hookInput struct {
	Cwd string `json:"cwd"`
}

var wordSplit = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// runSessionContext 는 SessionStart 훅이다. 세션이 열린 디렉토리가 main/{repo}(또는 그 worktree)면 repo·브랜치 이름으로
// 메모리를 검색해 맥락으로 넣는다 — 에이전트가 search_memory 를 빼먹어도 지난 history 가 앞에 붙는다.
// 훅은 세션을 막으면 안 되므로 어떤 실패도 조용히 넘긴다 (출력 없음 = 맥락 없음).
func runSessionContext(ctx context.Context, c *CLI, args []string) (string, error) {
	// 훅은 인자 없이 부른다. 사람이 친 '-h' 는 stdin 을 기다리지 않고 도움말을 본다
	if _, err := newFlags("session-context").parse(args); err != nil {
		return "", err
	}
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
	if len(terms) > 0 && c.Handlers.ReadReady(ctx) == nil {
		res, err := search.Search(ctx, c.OS, search.Request{
			Workspace: c.Config.Workspace, Team: c.Config.DefaultTeam, Project: repo, Query: strings.Join(terms, " "), Size: sessionHits,
		})
		if err == nil && len(res.Hits) == 0 && repo != "" {
			// repo 필터로 아무것도 없으면 repo 이름이 들어간 문서 전체에서 다시 찾는다
			res, err = search.Search(ctx, c.OS, search.Request{Workspace: c.Config.Workspace, Team: c.Config.DefaultTeam, Query: strings.Join(terms, " "), Size: sessionHits})
		}
		hits := res.Hits
		if err == nil && len(hits) > 0 {
			lines = append(lines, fmt.Sprintf("## memory — 이 세션과 관련된 문서 (자동 검색: %q)", strings.Join(terms, " ")))
			for _, h := range hits {
				lines = append(lines, fmt.Sprintf("- %s › %s (%s, %s)", h.SourcePath, h.HeaderPath, h.DocType, h.Day()))
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
			lines = append(lines, fmt.Sprintf("(메모리 판정 질문 %d건이 답을 기다린다 — 사용자에게 'scripts/memory/run.sh questions' 를 알려 줄 것)", n))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// repoOf 는 cwd 가 가리키는 관리 대상 repository 와 그 작업 디렉토리다 (main/{repo}/... 또는 main/{repo}/.worktrees/{issue}/...).
// 경로는 적힌 철자와 심볼릭 링크를 푼 철자를 모두 본다 — root·cwd 중 한쪽만 링크 경로여도(macOS 의 /var → /private/var),
// main/{repo} 가 meta-repo 밖의 checkout 을 가리키는 링크여도(cwd 가 링크 철자든 푼 철자든) 같은 repo 다. dir 은 root 의 철자다.
func repoOf(root, cwd string) (string, string) {
	if cwd == "" {
		return "", ""
	}
	rel, ok := relInRoot(root, cwd)
	if !ok {
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

// relInRoot 는 cwd 의 root 기준 상대 경로다 — 적힌 철자끼리 먼저(링크 철자의 cwd), 다음은 링크를 푼 철자끼리, 마지막으로
// main/ 아래 링크가 가리키는 checkout 안인지 본다 (링크를 푼 철자의 cwd 는 root 밖으로 풀린다).
func relInRoot(root, cwd string) (string, bool) {
	if rel, ok := relInside(root, cwd); ok {
		return rel, true
	}
	realCwd := resolved(cwd)
	if rel, ok := relInside(resolved(root), realCwd); ok {
		return rel, true
	}
	return linkedRepoRel(root, realCwd)
}

// linkedRepoRel 은 realCwd(링크를 푼 철자)가 main/ 아래 심볼릭 링크가 가리키는 곳 안이면 그 링크(main/{repo}) 기준 상대 경로다.
func linkedRepoRel(root, realCwd string) (string, bool) {
	mainDir := filepath.Join(root, "main")
	entries, err := os.ReadDir(mainDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if rel, ok := relInside(resolved(filepath.Join(mainDir, e.Name())), realCwd); ok {
			return filepath.Join("main", e.Name(), rel), true
		}
	}
	return "", false
}

// relInside 는 p 가 base 안(base 자신 포함)이면 base 기준 상대 경로다.
func relInside(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
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
