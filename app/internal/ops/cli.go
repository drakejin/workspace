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
	// reads 면 읽기 명령이다 — 쓰기 쪽 bootstrap 대신 핸들러가 읽기 준비(ReadReady)만 본다 (cold 설정이 잘못돼도 검색은 된다).
	reads bool
	run   func(ctx context.Context, c *CLI, args []string) (string, error)
}

// commands 는 명령 표다. 이름 순으로 Usage 에 나온다.
// 명령 함수가 usageErr 로 이 표를 다시 읽으므로 init 에서 채운다 (초기화 순환 방지).
var commands map[string]command

func init() {
	commands = map[string]command{
		"bootstrap":         {usage: "템플릿·ISM 정책·스냅샷 repository·org alias 생성/갱신", run: runBootstrap},
		"search":            {usage: "[-project P] [-topic T] [-date YYYY-MM-DD] [-from D] [-to D] [-type K] [-team T | -org O] [-size N] [-superseded] [query]   검색 (질의어 없이 필터만 주면 최신순으로 가져온다. 플래그는 질의어 앞에)", reads: true, run: runSearch},
		"filters":           {usage: "[-project P] [-topic T] [-date D] [-from D] [-to D] [-type K] [-team T | -org O] [-superseded]   검색 필터에 쓸 수 있는 값(프로젝트·주제·문서 종류·날짜)과 문서 수", reads: true, run: runFilters},
		"ingest":            {usage: "[-team T] <path>   파일/디렉토리 적재", run: runIngest},
		"compaction-report": {usage: "[-team T]   주간 compaction 1·2단계 리포트 (markdown)", run: runCompactionReport},
		"tier":              {usage: "[-team T] [-dry-run] [-keep-files]   hot 기간이 지난 인덱스 → 쓰기 막기 → 스냅샷 → 삭제 → hot 원본 제거 (-team 없으면 이 workspace 의 모든 팀)", run: runTier},
		"tier-tick":         {usage: "[-every 24h]   스케줄러용: 마지막 tier 에서 every 가 지났으면 tier + audit (보고만)", run: runTierTick},
		"list-cold":         {usage: "[-team T]   cold 인덱스 목록", reads: true, run: runListCold},
		"restore":           {usage: "<index>   cold 인덱스 복원", run: runRestore},
		"reload":            {usage: "[-team T]   cold store 의 청크 JSONL 을 _bulk 로 재적재 (cold 로 간 달은 빼고)", run: runReload},
		"status":            {usage: "데몬 상태 (scripts/memory-daemon/run.sh status)", offline: true, run: runStatus},
		"questions":         {usage: "판정이 사람에게 넘긴 질문 목록", offline: true, run: runQuestions},
		"answer":            {usage: "<pairKey> latest|old|keep   질문에 답한다", offline: true, run: runAnswer},
		"audit":             {usage: "[-apply]   정합성 감사 (hot·인덱스·cold store·원장·링크). -apply 면 고칠 수 있는 것을 고친다", run: runAudit},
		"archive":           {usage: "<deprecate|undeprecate|purge|edit> [-team T] -by B -reason R [-superseded-by P] [-file F] <path>   cold 문서 조치", run: runArchive},
		"graph":             {usage: "<neighbors [-depth N] <path> | entity <name> | stats | rebuild>   knowledge graph (Neo4j)", offline: true, run: runGraph},
		"session-context":   {usage: "Claude Code SessionStart 훅 — stdin 의 훅 JSON 을 읽고 관련 메모리를 출력한다 (실패해도 조용히)", offline: true, run: runSessionContext},
		"bench":             {usage: "-questions F [-model M] [-modes with,without] [-runs N] [-max-turns N] [-mcp-script P]   memory-mcp 유무로 같은 질문을 claude 에 시켜 비교 (F 는 meta-repo 밖)", offline: true, run: runBench},
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
	b.WriteString("사용법: memory-mcp <command> [flags]   (플래그 설명: memory-mcp <command> -h)\n  serve   MCP 서버 (stdio) — 인자가 없을 때 기본\n")
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
	// 도움말은 OpenSearch 없이도 보인다
	if !cmd.offline && !cmd.reads && name != "bootstrap" && !asksHelp(args) {
		if err := c.Handlers.Ready(ctx); err != nil {
			return err
		}
	}
	// 실패해도 명령이 돌려준 출력은 보인다 — tier 처럼 앞에서 이미 지운 것이 있는 명령이 그 내역을 잃지 않게
	out, err := cmd.run(ctx, c, args)
	var help *helpRequest
	if errors.As(err, &help) {
		out, err = help.text, nil
	}
	if out != "" {
		if _, werr := fmt.Fprintln(c.Out, out); err == nil {
			err = werr
		}
	}
	return err
}

// flags 는 명령 하나의 플래그 집합이다. 쓰지 않는 플래그를 받으면 사용 오류가 된다.
// name 은 commands 표의 이름이다 — 사용 오류와 '-h' 가 그 줄과 플래그 표를 함께 보인다.
type flags struct {
	*flag.FlagSet
	name string
}

func newFlags(name string) flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // 플래그 표는 help 가 만든다 — flag 패키지가 stderr 에 따로 쓰지 않게
	return flags{FlagSet: fs, name: name}
}

func (f flags) parse(args []string) ([]string, error) {
	err := f.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return nil, &helpRequest{text: f.help()}
	case err != nil:
		return nil, f.invalid(err)
	}
	rest := f.Args()
	if misplaced := f.misplacedFlag(args, rest); misplaced != "" {
		return nil, f.invalid(fmt.Errorf("플래그 %s 는 질의어·경로보다 앞에 두세요 — 뒤에 오면 플래그가 아니라 질의어·경로로 읽힙니다 (그 글자를 그대로 넘기려면 -- 뒤에)", misplaced))
	}
	return rest, nil
}

// misplacedFlag 는 플래그가 아닌 인자 뒤에 온 이 명령의 플래그다 (없으면 ""). flag 패키지는 첫 비플래그 인자에서 읽기를 멈춰,
// 'search 질의어 -topic T' 의 -topic T 가 말없이 질의어가 된다. '--' 뒤의 인자는 사용자가 일부러 넘긴 글자라 보지 않는다
// (flag 패키지가 먹은 맨 앞의 '--' 도, 질의어 사이의 '--' 도).
func (f flags) misplacedFlag(args, rest []string) string {
	if start := len(args) - len(rest); start > 0 && args[start-1] == "--" {
		return ""
	}
	for _, a := range rest {
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name != "" && f.Lookup(name) != nil {
			return a
		}
	}
	return ""
}

// sub 는 하위 명령(archive deprecate, graph neighbors)과 나머지 인자다. 없거나 플래그가 먼저 오면 사용 오류다 ('-h' 면 도움말).
// 앞의 플래그를 따로 읽지 않는다 — 값을 받는 플래그(-depth 3)를 혼자 읽으면 값이 있는데도 "값이 없다" 고 잘못 알린다.
func (f flags) sub(args []string) (string, []string, error) {
	switch {
	case len(args) == 0:
		return "", nil, f.usageErr()
	case helpArgs[args[0]]:
		return "", nil, &helpRequest{text: f.help()}
	case strings.HasPrefix(args[0], "-"):
		return "", nil, f.invalid(fmt.Errorf("하위 명령을 플래그(%s)보다 먼저 쓰세요", args[0]))
	}
	return args[0], args[1:], nil
}

func (f flags) usageErr() error {
	return fmt.Errorf("%w: %s", ErrUsage, f.help())
}

// invalid 는 이 명령의 인자가 틀렸다는 사용 오류다 — 무엇이 틀렸는지와 사용법·플래그 표를 함께 보인다. 원인이 이미 명령
// 이름으로 시작하면(bench.Options.Validate 의 "bench: …") 이름을 다시 붙이지 않는다.
func (f flags) invalid(err error) error {
	if strings.HasPrefix(err.Error(), f.name+": ") {
		return fmt.Errorf("%w: %w\n%s", ErrUsage, err, f.help())
	}
	return fmt.Errorf("%w: %s: %w\n%s", ErrUsage, f.name, err, f.help())
}

// help 는 명령의 사용법 줄과 플래그 표(이름·값 종류·설명·기본값)다.
func (f flags) help() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", f.name, commands[f.name].usage)
	f.VisitAll(func(fl *flag.Flag) {
		b.WriteString("\n    " + flagHelp(fl))
	})
	return b.String()
}

// flagNameWidth 는 플래그 표의 이름 칸 너비다 (가장 긴 "-superseded-by string" 에 맞춘다).
const flagNameWidth = 22

// zeroDefaults 는 기본값으로 보이지 않을 값이다 (flag.PrintDefaults 와 같다).
var zeroDefaults = map[string]bool{"": true, "0": true, "false": true}

func flagHelp(fl *flag.Flag) string {
	kind, desc := flag.UnquoteUsage(fl)
	name := "-" + fl.Name
	if kind != "" {
		name += " " + kind
	}
	line := fmt.Sprintf("%-*s %s", flagNameWidth, name, desc)
	if !zeroDefaults[fl.DefValue] {
		line += " (기본 " + fl.DefValue + ")"
	}
	return line
}

// helpRequest 는 '<command> -h' 다. Run 이 text 를 Out 에 쓰고 성공으로 끝낸다.
type helpRequest struct{ text string }

func (h *helpRequest) Error() string { return h.text }

// helpArgs 는 flag 패키지가 도움말로 읽는 인자다.
var helpArgs = map[string]bool{"-h": true, "-help": true, "--h": true, "--help": true}

// asksHelp 는 명령 바로 뒤에 도움말을 청했는가다.
func asksHelp(args []string) bool { return len(args) > 0 && helpArgs[args[0]] }
