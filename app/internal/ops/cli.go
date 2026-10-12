// Package ops 는 운영 명령이다 — 검색·적재·cold·판정 질문·감사·cold 문서 조치·그래프·상태·세션 훅·측정.
package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// ErrUsage 는 잘못된 사용이다.
var ErrUsage = errors.New("usage")

// CLI 는 운영 명령 실행기다.
type CLI struct {
	Config   config.Config
	OS       opensearch.Client
	Cold     cold.Store
	Indexer  ingest.Indexer
	Handlers *mcpserver.Handlers
	Graph    graph.Store // nil 이면 그래프 명령은 "꺼져 있다"
	Out      io.Writer
	In       io.Reader
	Now      func() time.Time
}

// command 는 명령 하나다. 사용법·OpenSearch 필요 여부·실행이 한 곳에 있다.
type command struct {
	usage string
	// offline 이면 OpenSearch 가 꺼져 있어도 돈다 (bootstrap 을 건너뛴다).
	offline bool
	run     func(ctx context.Context, c *CLI, args []string) (string, error)
}

// commands 는 명령 표다. 이름 순으로 Usage 에 나온다.
// 명령 함수가 usageErr 로 이 표를 다시 읽으므로 init 에서 채운다 (초기화 순환 방지).
var commands map[string]command

func init() {
	commands = map[string]command{
		"bootstrap":         {usage: "템플릿·ISM 정책·스냅샷 repository·org alias 생성/갱신", run: runBootstrap},
		"search":            {usage: "[-team T | -org O] [-repo R] [-superseded] <query>   BM25 검색", run: runSearch},
		"ingest":            {usage: "[-team T] <path>   파일/디렉토리 적재", run: runIngest},
		"compaction-report": {usage: "[-team T]   주간 compaction 1·2단계 리포트 (markdown)", run: runCompactionReport},
		"tier":              {usage: "[-team T] [-dry-run] [-keep-files]   hot 기간이 지난 인덱스 → 스냅샷 → hot 원본 제거 → 삭제 (-team 없으면 모든 팀)", run: runTier},
		"tier-tick":         {usage: "[-every 24h]   스케줄러용: 마지막 tier 에서 every 가 지났으면 tier + audit (보고만)", run: runTierTick},
		"list-cold":         {usage: "[-team T]   cold 인덱스 목록", run: runListCold},
		"restore":           {usage: "<index>   cold 인덱스 복원", run: runRestore},
		"reload":            {usage: "[-team T]   cold store 의 청크 JSONL 을 _bulk 로 재적재 (cold 로 간 달은 빼고)", run: runReload},
		"status":            {usage: "데몬 상태 (make daemon-status)", offline: true, run: runStatus},
		"questions":         {usage: "판정이 사람에게 넘긴 질문 목록", offline: true, run: runQuestions},
		"answer":            {usage: "<pairKey> latest|old|keep   질문에 답한다", offline: true, run: runAnswer},
		"audit":             {usage: "[-apply]   정합성 감사 (hot·인덱스·cold store·원장·링크). -apply 면 고칠 수 있는 것을 고친다", run: runAudit},
		"archive":           {usage: "<deprecate|undeprecate|purge|edit> [-team T] -by B -reason R [-superseded-by P] [-file F] <path>   cold 문서 조치", run: runArchive},
		"graph":             {usage: "<neighbors [-depth N] <path> | entity <name> | stats | rebuild>   knowledge graph (Neo4j)", offline: true, run: runGraph},
		"session-context":   {usage: "Claude Code SessionStart 훅 — stdin 의 훅 JSON 을 읽고 관련 메모리를 출력한다 (실패해도 조용히)", offline: true, run: runSessionContext},
		"bench":             {usage: "-questions F [-model M] [-modes with,without] [-runs N]   memory-mcp 유무로 같은 질문을 claude 에 시켜 비교 (F 는 meta-repo 밖)", offline: true, run: runBench},
	}
}

// Usage 는 운영 CLI 사용법이다.
func Usage() string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("사용법: memory-mcp <command> [flags]\n  serve   MCP 서버 (stdio) — 인자가 없을 때 기본\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %-18s %s\n", name, commands[name].usage)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Run 은 명령 하나를 실행한다.
func (c *CLI) Run(ctx context.Context, name string, args []string) error {
	cmd, ok := commands[name]
	if !ok {
		return fmt.Errorf("%w: 모르는 명령 %q", ErrUsage, name)
	}
	if !cmd.offline && name != "bootstrap" {
		if err := c.Handlers.Ready(ctx); err != nil {
			return err
		}
	}
	out, err := cmd.run(ctx, c, args)
	if err != nil {
		return err
	}
	if out != "" {
		_, err = fmt.Fprintln(c.Out, out)
	}
	return err
}

// flags 는 명령 하나의 플래그 집합이다. 쓰지 않는 플래그를 받으면 사용 오류가 된다.
type flags struct {
	*flag.FlagSet
	name string
}

func newFlags(name string) flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return flags{FlagSet: fs, name: name}
}

func (f flags) parse(args []string) ([]string, error) {
	if err := f.Parse(args); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUsage, f.name, err)
	}
	return f.Args(), nil
}

func usageErr(name string) error {
	return fmt.Errorf("%w: %s %s", ErrUsage, name, commands[name].usage)
}
