package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/compaction"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/mcpserver"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Usage 는 운영 CLI 사용법이다.
const Usage = `사용법: memory-mcp <command> [flags]
  serve                                     MCP 서버 (stdio) — 인자가 없을 때 기본
  bootstrap                                 템플릿·ISM 정책·스냅샷 repository 생성/갱신
  search [-team T] [-repo R] [-superseded] <query>   BM25 검색
  ingest [-team T] <path>                   파일/디렉토리 적재
  compaction-report [-team T]               주간 compaction 1·2단계 리포트 (markdown)
  tier [-team T] [-dry-run] [-keep-files]   hot 기간이 지난 episodic 인덱스 → 스냅샷 → hot 원본 제거 → 삭제
  list-cold [-team T]                       cold 인덱스 목록
  restore <index>                           cold 인덱스 복원
  reload [-team T]                          cold store 의 청크 JSONL 을 _bulk 로 재적재
  status                                    데몬 상태 (make daemon-status)
  questions                                 판정이 사람에게 넘긴 질문 목록
  answer <pairKey> latest|old|keep          질문에 답한다`

// ErrUsage 는 잘못된 사용이다.
var ErrUsage = errors.New("usage")

// CLI 는 운영 명령 실행기다.
type CLI struct {
	Config   config.Config
	OS       opensearch.Client
	Cold     cold.Store
	Indexer  ingest.Indexer
	Handlers *mcpserver.Handlers
	Out      io.Writer
	Now      func() time.Time
}

// offline 명령은 OpenSearch 가 꺼져 있어도 돈다 (bootstrap 을 건너뛴다).
var offline = map[string]bool{"status": true, "questions": true, "answer": true}

var commands = map[string]bool{
	"bootstrap": true, "search": true, "ingest": true, "compaction-report": true, "tier": true,
	"list-cold": true, "restore": true, "reload": true, "status": true, "questions": true, "answer": true,
}

// Run 은 명령 하나를 실행한다.
func (c *CLI) Run(ctx context.Context, name string, args []string) error {
	if !commands[name] {
		return fmt.Errorf("%w: 모르는 명령 %q", ErrUsage, name)
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	team := fs.String("team", c.Config.DefaultTeam, "팀")
	repo := fs.String("repo", "", "repository")
	superseded := fs.Bool("superseded", false, "밀린 문서도 본다")
	dryRun := fs.Bool("dry-run", false, "미리보기")
	keepFiles := fs.Bool("keep-files", false, "hot 원본을 남긴다")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	rest := fs.Args()
	teamSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "team" {
			teamSet = true
		}
	})
	if !offline[name] && name != "bootstrap" {
		if err := c.Handlers.Ready(ctx); err != nil {
			return err
		}
	}

	switch name {
	case "bootstrap":
		if err := opensearch.Bootstrap(ctx, c.OS, c.Config); err != nil {
			return err
		}
		return c.print("bootstrap 완료")
	case "search":
		if len(rest) == 0 {
			return fmt.Errorf("%w: search <query>", ErrUsage)
		}
		text, err := c.Handlers.Search(ctx, mcpserver.SearchInput{Team: *team, Query: strings.Join(rest, " "), Repo: *repo, IncludeSuperseded: *superseded})
		if err != nil {
			return err
		}
		return c.print(text)
	case "ingest":
		if len(rest) != 1 {
			return fmt.Errorf("%w: ingest <path>", ErrUsage)
		}
		override := ""
		if teamSet {
			override = *team
		}
		text, err := c.Handlers.Ingest(ctx, mcpserver.IngestInput{Path: rest[0], Team: override})
		if err != nil {
			return err
		}
		return c.print(text)
	case "compaction-report":
		r, err := compaction.Build(ctx, c.OS, c.Config, *team, c.Now())
		if err != nil {
			return err
		}
		return c.print(compaction.Render(r))
	case "tier":
		tierTeam := ""
		if teamSet {
			tierTeam = *team
		}
		decisions, err := cold.Tier(ctx, c.OS, c.Cold, c.Config, cold.TierOptions{Now: c.Now(), DryRun: *dryRun, Team: tierTeam, KeepFiles: *keepFiles})
		if err != nil {
			return err
		}
		return c.print(renderTier(decisions))
	case "list-cold":
		text, err := c.Handlers.ListCold(ctx, mcpserver.ListColdInput{Team: *team})
		if err != nil {
			return err
		}
		return c.print(text)
	case "restore":
		if len(rest) != 1 {
			return fmt.Errorf("%w: restore <index>", ErrUsage)
		}
		text, err := c.Handlers.Restore(ctx, mcpserver.RestoreInput{Index: rest[0], Confirmed: true})
		if err != nil {
			return err
		}
		return c.print(text)
	case "reload":
		// cold 로 간 달은 되살리지 않는다 (필요하면 restore)
		archived, err := cold.ArchivedMonths(ctx, c.OS, c.Config)
		if err != nil {
			return err
		}
		report, err := ingest.Reload(ctx, c.Cold, c.Indexer, *team, func(ch model.Chunk) bool {
			return ch.Kind == model.KindKnowledge || !archived[naming.MonthlyIndex(ch.Team, ch.EventTimeValue())]
		})
		if err != nil {
			return err
		}
		lines := []string{fmt.Sprintf("%d chunks 재적재 (from %s)", report.Chunks, c.Cold.Location())}
		for _, bad := range report.Corrupted {
			lines = append(lines, fmt.Sprintf("  손상: %s (줄 %v)", bad.Key, bad.Lines))
		}
		return c.print(strings.Join(lines, "\n"))
	case "status":
		return c.print(RenderStatus(ctx, c.Config, NewProbe(c.Config, c.OS), c.Now()))
	case "questions":
		text, err := RenderQuestions(c.Config.MetaRepoRoot)
		if err != nil {
			return err
		}
		return c.print(text)
	case "answer":
		if len(rest) != 2 {
			return fmt.Errorf("%w: answer <pairKey> latest|old|keep", ErrUsage)
		}
		a, ok := ParseAnswer(rest[1])
		if !ok {
			return fmt.Errorf("%w: 답은 latest|old|keep", ErrUsage)
		}
		text, err := AnswerQuestion(c.Config.MetaRepoRoot, rest[0], a, c.Now())
		if err != nil {
			return err
		}
		return c.print(text)
	default:
		return fmt.Errorf("%w: 모르는 명령 %q", ErrUsage, name)
	}
}

func renderTier(decisions []cold.TierDecision) string {
	if len(decisions) == 0 {
		return "대상 인덱스 없음"
	}
	var b strings.Builder
	removed := 0
	for _, d := range decisions {
		fmt.Fprintf(&b, "%-16s %s  newest=%s\n", d.Action, d.Index, orDash(d.NewestEvent))
		for _, f := range d.RemovedFiles {
			fmt.Fprintf(&b, "  - hot 에서 뺌: %s\n", f)
			removed++
		}
	}
	if removed > 0 {
		fmt.Fprintf(&b, "\nhot 원본 %d개를 cold 로 넘겼습니다 (git rm). 원본은 cold store raw 와 스냅샷에 있습니다 — 'git commit' 하세요.", removed)
	}
	return strings.TrimRight(b.String(), "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (c *CLI) print(text string) error {
	_, err := fmt.Fprintln(c.Out, text)
	return err
}
