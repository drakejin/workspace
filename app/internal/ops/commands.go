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
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

func runBootstrap(ctx context.Context, c *CLI, args []string) (string, error) {
	if _, err := newFlags("bootstrap").parse(args); err != nil {
		return "", err
	}
	// 명시적인 bootstrap 명령만 클러스터 전역 객체(ISM 정책·스냅샷 repository)를 이 설정으로 바꾼다
	if err := opensearch.BootstrapClaim(ctx, c.OS, c.Config); err != nil {
		return "", err
	}
	return "bootstrap 완료", nil
}

// filterFlags 는 search·filters 가 같이 받는 필터 플래그다 (MCP 의 search_filters 입력과 같은 값).
type filterFlags struct {
	team, org, project, topic, date, from, to, docType *string
	superseded                                         *bool
}

// addFilterFlags 는 필터 플래그를 f 에 정의한다 — search 와 filters 가 같은 이름·설명을 쓰게 한 곳에서.
func addFilterFlags(f flags, defaultTeam string) filterFlags {
	return filterFlags{
		team:       f.String("team", defaultTeam, "팀"),
		org:        f.String("org", "", "org(실) — 그 org 의 모든 팀을 함께 본다 (-team 무시)"),
		project:    f.String("project", "", "프로젝트(관리 대상 repository, 대소문자 무시)"),
		topic:      f.String("topic", "", "주제"),
		date:       f.String("date", "", "날짜 YYYY-MM-DD"),
		from:       f.String("from", "", "범위 시작 (YYYY-MM-DD, 2026-10-01T09:00, now-30d)"),
		to:         f.String("to", "", "범위 끝 (YYYY-MM-DD, 2026-10-01T18:00, now)"),
		docType:    f.String("type", "", "문서 종류 (spec|adr|prd|plan|knowledge|guide|note)"),
		superseded: f.Bool("superseded", false, "밀린·deprecate 된 문서도 본다"),
	}
}

func (ff filterFlags) input() mcpserver.FiltersInput {
	return mcpserver.FiltersInput{Team: *ff.team, Org: *ff.org, Project: *ff.project, Topic: *ff.topic, Date: *ff.date,
		TimeRange: model.TimeRange{From: *ff.from, To: *ff.to}, DocType: *ff.docType, IncludeSuperseded: *ff.superseded}
}

func runSearch(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("search")
	filters := addFilterFlags(f, c.Config.DefaultTeam)
	size := f.Int("size", 0, "결과 수 (1~50, 기본 8)")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	in := filters.input().WithQuery(strings.Join(rest, " "), *size)
	if strings.TrimSpace(in.Query) == "" && in.Filters() == "" {
		return "", f.usageErr()
	}
	return c.Handlers.Search(ctx, in)
}

func runFilters(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("filters")
	filters := addFilterFlags(f, c.Config.DefaultTeam)
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 0 {
		return "", f.usageErr()
	}
	return c.Handlers.Filters(ctx, filters.input())
}

func runIngest(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("ingest")
	team := f.String("team", "", "이번 적재에만 쓸 팀 (frontmatter 는 그대로 — 파일이 바뀌면 데몬이 frontmatter team 으로 다시 쓴다)")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", f.usageErr()
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
		return renderTierPartial(decisions), err
	}
	return renderTier(decisions), nil
}

// renderTierPartial 은 중간에 실패한 tier 가 그때까지 한 일이다 (없으면 빈 문자열 — 에러만 보인다).
func renderTierPartial(decisions []cold.TierDecision) string {
	if len(decisions) == 0 {
		return ""
	}
	return "[tier] 중간에 실패했습니다 — 그 전에 한 일:\n" + renderTier(decisions)
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
		return renderTierPartial(decisions), err
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
	f := newFlags("restore")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", f.usageErr()
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
	// cold 로 간 달은 되살리지 않는다 (필요하면 restore). 어느 스냅샷에도 없는 갇힌 문서는 hot 파일이 있을 때만 싣는다
	// (없으면 데몬이 파일 없는 문서로 보고 cold store 청크까지 지운다 — 감사가 stranded-cold 로 알린다)
	archived, err := cold.ArchivedMonths(ctx, c.OS, c.Config)
	if err != nil {
		return "", err
	}
	own := cold.NewOwnership(archived, c.Cold)
	report, err := ingest.Reload(ctx, c.Cold, c.Indexer, *team, func(ch model.Chunk) bool {
		return own.Reloadable(ctx, c.Config.MetaRepoRoot, ch)
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
	return renderQuestions(c.Config.MetaRepoRoot)
}

func runAnswer(_ context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("answer")
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 2 {
		return "", f.usageErr()
	}
	a, ok := parseAnswer(rest[1])
	if !ok {
		return "", f.usageErr()
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
	f := newFlags("archive")
	team := f.String("team", c.Config.DefaultTeam, "팀")
	by := f.String("by", "", "누가")
	reason := f.String("reason", "", "왜")
	supersededBy := f.String("superseded-by", "", "(deprecate) 대신 볼 문서")
	file := f.String("file", "", "(edit) 새 본문 파일")
	action, args, err := f.sub(args)
	if err != nil {
		return "", err
	}
	rest, err := f.parse(args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", f.usageErr()
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
			return "", f.usageErr()
		}
		return svc.Edit(ctx, *team, rest[0], *file, a)
	default:
		return "", f.usageErr()
	}
}

func runGraph(ctx context.Context, c *CLI, args []string) (string, error) {
	f := newFlags("graph")
	depth := f.Int("depth", 2, "(neighbors) 몇 걸음 (1~3)")
	sub, args, err := f.sub(args)
	if err != nil {
		return "", err
	}
	if sub == "neighbors" { // 플래그('-h' 포함)는 그래프가 꺼져 있어도 먼저 읽는다
		if args, err = f.parse(args); err != nil {
			return "", err
		}
	}
	if c.Graph == nil {
		return "", graph.ErrDisabled
	}
	switch {
	case sub == "neighbors" && len(args) == 1:
		return c.Handlers.Related(ctx, mcpserver.RelatedInput{Path: args[0], Depth: *depth})
	case sub == "entity" && len(args) == 1:
		return c.Handlers.Entities(ctx, mcpserver.EntityInput{Entity: args[0]})
	case sub == "stats":
		st, err := c.Graph.Stats(ctx, c.Config.Workspace)
		if err != nil {
			return "", err
		}
		return renderGraphStats(c.Config.Workspace, st), nil
	case sub == "rebuild":
		return rebuildGraph(ctx, c)
	default:
		return "", f.usageErr()
	}
}

// rebuildGraph 는 색인·파일·원장만으로 workspace 그래프를 다시 만든다 (LLM 없이).
func rebuildGraph(ctx context.Context, c *CLI) (string, error) {
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
	gitRemoved, fileRemoved, kept := 0, 0, 0
	for _, d := range decisions {
		fmt.Fprintf(&b, "%-16s %s  newest=%s\n", d.Action, d.Index, orDash(d.NewestEvent))
		for _, f := range d.RemovedFiles {
			fmt.Fprintf(&b, "  - hot 에서 뺌: %s\n", f)
		}
		if d.GitRemoved {
			gitRemoved += len(d.RemovedFiles)
		} else {
			fileRemoved += len(d.RemovedFiles)
		}
		for _, f := range d.KeptFiles {
			fmt.Fprintf(&b, "  - hot 에 남김: %s (git 에 커밋된 내용이 아니다)\n", f)
			kept++
		}
	}
	if gitRemoved > 0 {
		fmt.Fprintf(&b, "\nhot 원본 %d개를 cold 로 넘겼습니다 (git rm). 원본은 cold store raw 와 스냅샷에 있습니다 — 'git commit' 하세요.", gitRemoved)
	}
	if fileRemoved > 0 {
		fmt.Fprintf(&b, "\nhot 원본 %d개를 cold 로 넘겼습니다 (파일 삭제 — git 이 다루지 않는 meta-repo). 원본은 이제 cold store raw 와 스냅샷에만 있습니다.", fileRemoved)
	}
	if kept > 0 {
		fmt.Fprintf(&b, "\nhot 원본 %d개는 git 이 되살릴 수 없어(커밋하지 않았거나 커밋 뒤 고쳤다) 남겼습니다 — 커밋하면 다음 tier 가 hot 에서 뺍니다.", kept)
	}
	return strings.TrimRight(b.String(), "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
