package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/archive"
	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/compaction"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/mcpserver"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

func runBootstrap(ctx context.Context, c *CLI, args []string) (string, error) {
	if _, err := newFlags("bootstrap").parse(args); err != nil {
		return "", err
	}
	if err := opensearch.Bootstrap(ctx, c.OS, c.Config); err != nil {
		return "", err
	}
	return "bootstrap 완료", nil
}

func runSearch(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("search")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	org := f.String("org", "", "org(실)")
	project := f.String("project", "", "프로젝트(관리 대상 repository)")
	topic := f.String("topic", "", "주제")
	date := f.String("date", "", "날짜 YYYY-MM-DD")
	from := f.String("from", "", "범위 시작 (YYYY-MM-DD 또는 now-30d)")
	to := f.String("to", "", "범위 끝 (YYYY-MM-DD 또는 now)")
	docType := f.String("type", "", "문서 종류")
	size := f.Int("size", 0, "결과 수")
	superseded := f.Bool("superseded", false, "밀린·deprecate 된 문서도 본다")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	in := mcpserver.SearchInput{
		Team: *team, Org: *org, Query: strings.Join(rest, " "), Project: *project, Topic: *topic, Date: *date,
		TimeRange: model.TimeRange{From: *from, To: *to}, DocType: *docType, Size: *size, IncludeSuperseded: *superseded,
	}
	if strings.TrimSpace(in.Query) == "" && in.Filters() == "" {
		return "", usageErr("search")
	}
	return c.Handlers.Search(ctx, in)
}

func runFilters(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("filters")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	org := f.String("org", "", "org(실)")
	project := f.String("project", "", "프로젝트")
	topic := f.String("topic", "", "주제")
	date := f.String("date", "", "날짜 YYYY-MM-DD")
	from := f.String("from", "", "범위 시작")
	to := f.String("to", "", "범위 끝")
	docType := f.String("type", "", "문서 종류")
	superseded := f.Bool("superseded", false, "밀린·deprecate 된 문서도 센다")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 0 {
		return "", usageErr("filters")
	}
	return c.Handlers.Filters(ctx, mcpserver.FiltersInput{Team: *team, Org: *org, Project: *project, Topic: *topic, Date: *date,
		TimeRange: model.TimeRange{From: *from, To: *to}, DocType: *docType, IncludeSuperseded: *superseded})
}

func runIngest(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("ingest")
	team := f.String("team", "", "frontmatter 대신 쓸 팀")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", usageErr("ingest")
	}
	return c.Handlers.Ingest(ctx, mcpserver.IngestInput{Path: rest[0], Team: *team})
}

func runCompactionReport(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("compaction-report")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	r, err := compaction.Build(ctx, c.OS, c.Config, *team, c.Now())
	if err != nil {
		return "", err
	}
	return compaction.Render(r), nil
}

func runTier(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("tier")
	team := f.String("team", "", "팀 (비면 모든 팀)")
	dryRun := f.Bool("dry-run", false, "미리보기")
	keepFiles := f.Bool("keep-files", false, "hot 원본을 남긴다")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	decisions, err := cold.Tier(ctx, c.OS, c.Cold, c.Config, cold.TierOptions{Now: c.Now(), DryRun: *dryRun, Team: *team, KeepFiles: *keepFiles})
	if err != nil {
		return "", err
	}
	return renderTier(decisions), nil
}

// tierTickDefault 는 스케줄러가 tier 를 도는 최소 간격이다 (자주 깨워도 하루 한 번만 돈다).
const tierTickDefault = 24 * time.Hour

func runTierTick(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("tier-tick")
	every := f.Duration("every", tierTickDefault, "최소 간격")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	now := c.Now()
	tick := state.NewTick(c.Config.MetaRepoRoot, "tier")
	if !tick.Due(now, *every) {
		return fmt.Sprintf("[tier-tick] 아직 아님 — 마지막 %s", tick.Last().Local().Format(time.RFC3339)), nil
	}
	decisions, err := cold.Tier(ctx, c.OS, c.Cold, c.Config, cold.TierOptions{Now: now})
	if err != nil {
		return "", err
	}
	rep, err := audit.Run(ctx, audit.Deps{OS: c.OS, Config: c.Config, Cold: c.Cold, Indexer: c.Indexer, Now: now}, false)
	if err != nil {
		return "", err
	}
	if err := tick.Done(now); err != nil {
		return "", err
	}
	return fmt.Sprintf("[tier-tick] %s\n%s\n[audit] %s", now.Local().Format(time.RFC3339), renderTier(decisions), audit.Summary(rep)), nil
}

func runListCold(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("list-cold")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	return c.Handlers.ListCold(ctx, mcpserver.ListColdInput{Team: *team})
}

func runRestore(ctx context.Context, c *CLI, args []string) (string, error) {
	rest, err := newFlags("restore").parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", usageErr("restore")
	}
	// CLI 는 사람이 직접 치는 것이라 확인을 거친 것으로 본다
	return c.Handlers.Restore(ctx, mcpserver.RestoreInput{Index: rest[0], Confirmed: true})
}

func runReload(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("reload")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	// cold 로 간 달은 되살리지 않는다 (필요하면 restore)
	archived, err := cold.ArchivedMonths(ctx, c.OS, c.Config)
	if err != nil {
		return "", err
	}
	own := cold.NewOwnership(archived, c.Cold)
	report, err := ingest.Reload(ctx, c.Cold, c.Indexer, *team, func(ch model.Chunk) bool {
		owned, err := own.Owns(ctx, ch.Team, naming.IndexFor(ch), ch.SourcePath, ch.ContentSHA)
		return err != nil || !owned // 모르면 싣는다 — 빠뜨리는 쪽보다 낫다
	})
	if err != nil {
		return "", err
	}
	lines := []string{fmt.Sprintf("%d chunks 재적재 (from %s)", report.Chunks, c.Cold.Location())}
	for _, bad := range report.Corrupted {
		lines = append(lines, fmt.Sprintf("  손상: %s (줄 %v)", bad.Key, bad.Lines))
	}
	return strings.Join(lines, "\n"), nil
}

func runStatus(ctx context.Context, c *CLI, args []string) (string, error) {
	if _, err := newFlags("status").parse(args); err != nil {
		return "", err
	}
	return RenderStatus(ctx, c.Config, NewProbe(c.Config, c.OS, c.Graph), c.Now()), nil
}

func runQuestions(_ context.Context, c *CLI, args []string) (string, error) {
	if _, err := newFlags("questions").parse(args); err != nil {
		return "", err
	}
	return RenderQuestions(c.Config.MetaRepoRoot)
}

func runAnswer(_ context.Context, c *CLI, args []string) (string, error) {
	rest, err := newFlags("answer").parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 2 {
		return "", usageErr("answer")
	}
	a, ok := ParseAnswer(rest[1])
	if !ok {
		return "", usageErr("answer")
	}
	return AnswerQuestion(c.Config.MetaRepoRoot, rest[0], a, c.Now())
}

func runAudit(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("audit")
	apply := f.Bool("apply", false, "고칠 수 있는 것을 고친다")
	if _, err := f.parse(args); err != nil {
		return "", err
	}
	rep, err := audit.Run(ctx, audit.Deps{OS: c.OS, Config: c.Config, Cold: c.Cold, Indexer: c.Indexer, Now: c.Now()}, *apply)
	if err != nil {
		return "", err
	}
	return audit.Render(rep), nil
}

func runArchive(ctx context.Context, c *CLI, args []string) (string, error) {
	if len(args) == 0 {
		return "", usageErr("archive")
	}
	action, args := args[0], args[1:]
	f := newFlags("archive " + action)
	team := f.String("team", c.Config.DefaultTeam, "팀")
	by := f.String("by", "", "누가")
	reason := f.String("reason", "", "왜")
	supersededBy := f.String("superseded-by", "", "(deprecate) 대신 볼 문서")
	file := f.String("file", "", "(edit) 새 본문 파일")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", usageErr("archive")
	}
	svc := &archive.Service{OS: c.OS, Config: c.Config, Cold: c.Cold, Indexer: c.Indexer, Now: c.Now}
	a := archive.Audit{By: *by, Reason: *reason}
	switch action {
	case "deprecate":
		return svc.Deprecate(ctx, *team, rest[0], *supersededBy, a)
	case "undeprecate":
		return svc.Undeprecate(ctx, *team, rest[0], a)
	case "purge":
		return svc.Purge(ctx, *team, rest[0], a)
	case "edit":
		if *file == "" {
			return "", usageErr("archive")
		}
		return svc.Edit(ctx, *team, rest[0], *file, a)
	default:
		return "", usageErr("archive")
	}
}

func runGraph(ctx context.Context, c *CLI, args []string) (string, error) {
	if len(args) == 0 {
		return "", usageErr("graph")
	}
	if c.Graph == nil {
		return "", graph.ErrDisabled
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "neighbors":
		f := newFlags("graph neighbors")
		depth := f.Int("depth", 2, "몇 걸음")
		rest, err := f.parse(args)
		if err != nil || len(rest) != 1 {
			return "", usageErr("graph")
		}
		return c.Handlers.Related(ctx, mcpserver.RelatedInput{Path: rest[0], Depth: *depth})
	case "entity":
		if len(args) != 1 {
			return "", usageErr("graph")
		}
		return c.Handlers.Entities(ctx, mcpserver.EntityInput{Entity: args[0]})
	case "stats":
		st, err := c.Graph.Stats(ctx, c.Config.Workspace)
		if err != nil {
			return "", err
		}
		return renderGraphStats(c.Config.Workspace, st), nil
	case "rebuild":
		if err := c.Handlers.Ready(ctx); err != nil {
			return "", err
		}
		indices, err := ingest.ManagedIndices(ctx, c.OS)
		if err != nil {
			return "", err
		}
		docs, err := ingest.FetchIndexedDocs(ctx, c.OS, indices, c.Config.Workspace)
		if err != nil {
			return "", err
		}
		if err := c.Graph.Ping(ctx); err != nil {
			return "", err
		}
		g, err := graph.Rebuild(ctx, c.Graph, c.Config.MetaRepoRoot, c.Config.Workspace, docs)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[graph] rebuild: docs=%d entities=%d repos=%d edges=%d (LLM 0회 — 색인·파일·원장에서)", len(g.Docs), len(g.Entities), len(g.Repos), len(g.Edges)), nil
	default:
		return "", usageErr("graph")
	}
}

func renderGraphStats(workspace string, st graph.Stats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workspace=%s\nnodes:", workspace)
	for _, k := range graph.SortedKeys(st.Nodes) {
		fmt.Fprintf(&b, " %s=%d", k, st.Nodes[k])
	}
	b.WriteString("\nedges:")
	for _, k := range graph.SortedKeys(st.Edges) {
		fmt.Fprintf(&b, " %s=%d", k, st.Edges[k])
	}
	return b.String()
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
