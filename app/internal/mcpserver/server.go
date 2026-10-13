// Package mcpserver 는 MCP 서버 drakejin-workspace-memory-mcp 의 도구 일곱 개다: 발표의 search_memory · ingest_path ·
// list_cold · restore_cold, 검색 필터 값 search_filters, knowledge graph 의 related_docs · docs_by_entity.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/search"
)

// Version 은 서버 버전이다.
const Version = "0.2.0"

// 안내문은 MCP 도구와 CLI 명령(scripts/memory/run.sh <command>)이 같은 Handlers 로 보므로, 도구 이름 옆에 CLI 명령을 함께 적는다.
const (
	listColdHint = "list_cold(CLI: list-cold)"
	restoreHint  = "restore_cold(index, confirmed=true) (CLI: restore <index>)"
	searchHint   = "search_memory, CLI: search"
)

// Deps 는 서버 의존이다.
type Deps struct {
	Config  config.Config
	OS      opensearch.Client
	Cold    cold.Store
	Indexer ingest.Indexer
	// Graph 는 knowledge graph 다. nil 이면 그래프 도구는 "꺼져 있다"를 돌려준다.
	Graph graph.Store
	Log   func(format string, args ...any)
}

// Handlers 는 도구 구현이다. 전송(MCP)과 분리해 CLI 도 같은 함수를 쓴다.
type Handlers struct {
	Deps
	readyMu sync.Mutex
	ready   bool
}

// NewHandlers 는 Handlers 다.
func NewHandlers(d Deps) *Handlers { return &Handlers{Deps: d} }

// Ready 는 템플릿·ISM·스냅샷 repository 를 한 번 맞춘다. 실패하면 다음 호출에서 다시 시도한다.
func (h *Handlers) Ready(ctx context.Context) error {
	h.readyMu.Lock()
	defer h.readyMu.Unlock()
	if h.ready {
		return nil
	}
	if err := opensearch.Bootstrap(ctx, h.OS, h.Config); err != nil {
		return err
	}
	h.ready = true
	return nil
}

// SearchInput 은 search_memory 입력이다. 질의어(query)와 필터(날짜·범위·주제·프로젝트·문서 종류)는 함께 써도 되고,
// 질의어 없이 필터만 주면 맞는 문서를 최신순으로 가져온다.
type SearchInput struct {
	Query             string          `json:"query,omitempty" jsonschema:"자연어 문장 또는 식별자(서비스·함수 이름, 에러 코드). 비우면 필터에 맞는 문서를 최신순으로 가져온다"`
	Project           string          `json:"project,omitempty" jsonschema:"프로젝트(관리 대상 repository) 이름으로 좁히기. 예: order-api. 쓸 수 있는 값은 search_filters"`
	Topic             string          `json:"topic,omitempty" jsonschema:"주제로 좁히기 — docs/feature|specs|issues/{주제}/ 디렉토리 또는 frontmatter topic. 예: payment-retry. 쓸 수 있는 값은 search_filters"`
	Date              string          `json:"date,omitempty" jsonschema:"날짜(YYYY-MM-DD) — 그날의 문서만. 문서의 날은 frontmatter date 그대로, 없으면 커밋 시각을 MEMORY_TIME_ZONE(기본: 이 머신의 시간대)에서 본 날"`
	TimeRange         model.TimeRange `json:"time_range,omitempty" jsonschema:"기간 범위 — from~to. 날짜(YYYY-MM-DD)는 문서의 날로 비교하고 그날 전체를 포함한다. date math(now-30d)·시각은 유효 시각을 MEMORY_TIME_ZONE 으로 비교한다"`
	DocType           string          `json:"doc_type,omitempty" jsonschema:"spec | adr | prd | plan | knowledge | guide | note"`
	Team              string          `json:"team,omitempty" jsonschema:"팀 이름. 생략하면 기본 팀"`
	Org               string          `json:"org,omitempty" jsonschema:"org(실) 이름. 주면 그 org 에 속한 모든 팀을 함께 검색한다 (team 무시)"`
	Size              int             `json:"size,omitempty" jsonschema:"결과 수 (1~50, 기본 8)"`
	IncludeSuperseded bool            `json:"include_superseded,omitempty" jsonschema:"판정·개정으로 밀린 문서와 deprecate 된 문서도 본다 (기본: 숨김)"`
}

// Filters 는 사람이 읽는 필터 요약이다 (결과 머리에 붙여, 어떤 조건으로 가져왔는지 보이게).
func (in SearchInput) Filters() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("project", in.Project)
	add("topic", in.Topic)
	add("date", in.Date)
	if in.TimeRange.From != "" || in.TimeRange.To != "" {
		parts = append(parts, "time_range="+in.TimeRange.From+"~"+in.TimeRange.To)
	}
	add("doc_type", in.DocType)
	if in.IncludeSuperseded {
		parts = append(parts, "include_superseded")
	}
	return strings.Join(parts, ", ")
}

// Search 는 search_memory 다.
func (h *Handlers) Search(ctx context.Context, in SearchInput) (string, error) {
	team, err := h.team(in.Team)
	if err != nil {
		return "", err
	}
	req := search.Request{
		Workspace: h.Config.Workspace, Team: team, Org: in.Org, Query: in.Query, Date: in.Date, TimeRange: in.TimeRange,
		TimeZone: h.Config.QueryTimeZone(time.Now()), Project: in.Project, Topic: in.Topic, Size: in.Size, IncludeSuperseded: in.IncludeSuperseded,
	}
	if in.DocType != "" {
		t, ok := model.ParseDocType(in.DocType)
		if !ok {
			return "", fmt.Errorf("doc_type 이 잘못됐습니다: %q", in.DocType)
		}
		req.DocType = t
	}
	if err := search.Validate(req); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Query) == "" && !search.HasFilter(req) {
		return "", search.ErrEmptyQuery
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	res, err := search.Search(ctx, h.OS, req)
	if err != nil {
		return "", err
	}
	hits := res.Hits
	scope := "team=" + team
	if in.Org != "" {
		scope = "org=" + in.Org
	}
	if f := in.Filters(); f != "" {
		scope += " · " + f
	}
	listing := strings.TrimSpace(in.Query) == ""
	if len(hits) == 0 {
		if listing {
			return fmt.Sprintf("%s · 필터에 맞는 문서가 없습니다. %s 로 쓸 수 있는 프로젝트·주제·날짜를 확인하세요.", scope, search.FiltersHint), nil
		}
		return fmt.Sprintf("%s hot 영역에서 %q 에 맞는 문서가 없습니다.\n"+
			"단어(서비스명, 함수명, 에러 코드)를 바꿔 다시 검색하세요. %d일보다 오래된 맥락이면 %s 를 확인하세요.", scope, in.Query, h.Config.HotDays, listColdHint), nil
	}
	var b strings.Builder
	b.WriteString(searchHeader(scope, listing, res.Total, hits) + "\n\n")
	for i, hit := range hits {
		meta := []string{string(hit.DocType)}
		if in.Org != "" {
			meta = append(meta, "team="+hit.Team)
		}
		if hit.Project != "" {
			meta = append(meta, "project="+hit.Project)
		}
		if hit.Topic != "" {
			meta = append(meta, "topic="+hit.Topic)
		}
		if day := hit.Day(); day != "" {
			meta = append(meta, day)
		}
		if len(hit.GitSHA) >= 7 {
			meta = append(meta, hit.GitSHA[:7])
		}
		score := ""
		if !listing {
			score = fmt.Sprintf("; score %.2f", hit.Score)
		}
		flag := ""
		if hit.SupersededBy != "" {
			flag = "  [superseded → " + hit.SupersededBy + "]"
		}
		if hit.Deprecated {
			flag += "  [deprecated]"
		}
		fmt.Fprintf(&b, "[%d] %s › %s  (%s%s)%s\n", i+1, hit.SourcePath, hit.HeaderPath, strings.Join(meta, ", "), score, flag)
		if hit.Summary != "" {
			fmt.Fprintf(&b, "요약: %s\n", hit.Summary)
		}
		b.WriteString(hit.Text + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// searchHeader 는 검색 결과의 머리 줄이다 — 질의어 검색은 청크 단위라 청크 수와 그 청크들의 문서 수를 함께
// (한 문서의 여러 절이 size 를 나눠 쓴 것이 보인다), 필터 가져오기는 문서 하나에 한 건이라 문서 수 (잘렸으면 전체 수도).
func searchHeader(scope string, listing bool, total int, hits []search.Hit) string {
	shown := len(hits)
	switch {
	case listing && total > shown:
		return fmt.Sprintf("%s · 문서 %d건 중 최신 %d건 (필터, 최신순 — 더 보려면 size 를 늘리거나 필터를 좁히세요)", scope, total, shown)
	case listing:
		return fmt.Sprintf("%s · 문서 %d건 (필터, 최신순)", scope, shown)
	default:
		return fmt.Sprintf("%s · 청크 %d건 · 문서 %d건 (BM25)", scope, shown, distinctDocs(hits))
	}
}

// distinctDocs 는 결과 청크들이 나온 문서(source_path) 수다.
func distinctDocs(hits []search.Hit) int {
	seen := make(map[string]bool, len(hits))
	for _, hit := range hits {
		seen[hit.SourcePath] = true
	}
	return len(seen)
}

// FiltersInput 은 search_filters 입력이다.
type FiltersInput struct {
	Project           string          `json:"project,omitempty" jsonschema:"이 프로젝트 안에서 센다"`
	Topic             string          `json:"topic,omitempty" jsonschema:"이 주제 안에서 센다"`
	Date              string          `json:"date,omitempty" jsonschema:"이 날짜(YYYY-MM-DD) 안에서 센다"`
	TimeRange         model.TimeRange `json:"time_range,omitempty" jsonschema:"이 기간 안에서 센다"`
	DocType           string          `json:"doc_type,omitempty" jsonschema:"이 문서 종류 안에서 센다"`
	Team              string          `json:"team,omitempty" jsonschema:"팀 이름. 생략하면 기본 팀"`
	Org               string          `json:"org,omitempty" jsonschema:"org(실) 이름"`
	IncludeSuperseded bool            `json:"include_superseded,omitempty" jsonschema:"밀린·deprecate 된 문서도 센다"`
}

// Filters 는 search_filters 다.
func (h *Handlers) Filters(ctx context.Context, in FiltersInput) (string, error) {
	team, err := h.team(in.Team)
	if err != nil {
		return "", err
	}
	req := search.Request{Workspace: h.Config.Workspace, Team: team, Org: in.Org, Project: in.Project, Topic: in.Topic, Date: in.Date,
		TimeRange: in.TimeRange, TimeZone: h.Config.QueryTimeZone(time.Now()), IncludeSuperseded: in.IncludeSuperseded}
	if in.DocType != "" {
		t, ok := model.ParseDocType(in.DocType)
		if !ok {
			return "", fmt.Errorf("doc_type 이 잘못됐습니다: %q", in.DocType)
		}
		req.DocType = t
	}
	if err := search.Validate(req); err != nil {
		return "", err
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	f, err := search.FetchFacets(ctx, h.OS, req)
	if err != nil {
		return "", err
	}
	scope := "team=" + team
	if in.Org != "" {
		scope = "org=" + in.Org
	}
	if s := (SearchInput{Project: in.Project, Topic: in.Topic, Date: in.Date, TimeRange: in.TimeRange, DocType: in.DocType, IncludeSuperseded: in.IncludeSuperseded}).Filters(); s != "" {
		scope += " · " + s
	}
	hidden := " (밀린·deprecate 된 문서 제외)"
	if in.IncludeSuperseded {
		hidden = ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · 문서 %d건%s\n", scope, f.Docs, hidden)
	if f.Docs == 0 {
		return strings.TrimRight(b.String(), "\n"), nil
	}
	fmt.Fprintf(&b, "project: %s\n", countsLine(f.Projects, f.NoProject))
	fmt.Fprintf(&b, "topic: %s\n", countsLine(f.Topics, f.NoTopic))
	fmt.Fprintf(&b, "doc_type: %s\n", countsLine(f.DocTypes, 0))
	fmt.Fprintf(&b, "date: %s ~ %s\n", f.First, f.Last)
	fmt.Fprintf(&b, "날짜별(최신 %d일): %s", len(f.Days), countsLine(f.Days, 0))
	return b.String(), nil
}

func countsLine(cs []search.Count, none int) string {
	parts := make([]string, 0, len(cs)+1)
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s(%d)", c.Value, c.Docs))
	}
	if none > 0 {
		parts = append(parts, fmt.Sprintf("(없음)(%d)", none))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// IngestInput 은 ingest_path 입력이다.
type IngestInput struct {
	Path string `json:"path" jsonschema:"meta-repository 기준 상대 경로 (파일 또는 디렉토리)"`
	Team string `json:"team,omitempty" jsonschema:"frontmatter 대신 쓸 팀"`
}

// Ingest 는 ingest_path 다.
func (h *Handlers) Ingest(ctx context.Context, in IngestInput) (string, error) {
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path 가 비어 있습니다")
	}
	if in.Team != "" {
		if err := naming.ValidateTeam(in.Team); err != nil {
			return "", err
		}
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	results, err := h.Indexer.IngestPath(ctx, in.Path, in.Team)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return in.Path + ": 색인 대상 문서가 없습니다 (루트 문서, docs/, main/{repo}/docs/ 아래 .md 만 대상)", nil
	}
	return FormatIngest(results), nil
}

// FormatIngest 는 적재 결과 목록이다. 게이트(바이너리·MEMORY_MAX_FILE_BYTES 초과)에 걸린 파일은 건너뛴 것으로 따로 적는다.
func FormatIngest(results []ingest.Result) string {
	var indexed, skipped []ingest.Result
	for _, r := range results {
		if r.Skipped != "" {
			skipped = append(skipped, r)
		} else {
			indexed = append(indexed, r)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d개 문서 적재", len(indexed))
	for _, r := range indexed {
		fmt.Fprintf(&b, "\n- %s → %s (%d chunks)", r.SourcePath, r.Index, r.Chunks)
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "\n%d개 건너뜀 — 데몬과 같은 게이트: 바이너리·MEMORY_MAX_FILE_BYTES 초과는 색인하지 않는다", len(skipped))
		for _, r := range skipped {
			fmt.Fprintf(&b, "\n- %s (%s)", r.SourcePath, r.Skipped)
		}
	}
	return b.String()
}

// ListColdInput 은 list_cold 입력이다.
type ListColdInput struct {
	Team string `json:"team,omitempty" jsonschema:"팀 이름. 생략하면 기본 팀"`
}

// ListCold 는 list_cold 다.
func (h *Handlers) ListCold(ctx context.Context, in ListColdInput) (string, error) {
	team, err := h.team(in.Team)
	if err != nil {
		return "", err
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	list, err := h.coldList(ctx, team)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "team=" + team + ": cold 인덱스가 없습니다.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "team=%s cold 인덱스 %d개 (repository %s)\n", team, len(list), h.Config.Snapshot.Repository)
	for _, c := range list {
		fmt.Fprintf(&b, "- %s  snapshot=%s  at=%s%s\n", c.Name, strings.Join(c.Snapshots, ","), c.SnapshotTime, coldMark(c))
	}
	b.WriteString("\n복원이 필요하면 사용자에게 먼저 \"cold 인덱스 X가 있는데 복원할까요\"라고 묻고, 동의를 받은 뒤 " + restoreHint + ".")
	return b.String(), nil
}

// coldMark 는 cold 인덱스의 지금 상태 표시다.
func coldMark(c cold.Index) string {
	switch {
	case c.Restored && !c.Searchable:
		return "  [복원 중단 — restore_cold 로 다시]" // 합치다 멈춘 복원본은 검색 alias 가 없어 검색에 나오지 않는다
	case c.Restored:
		return "  [현재 복원됨]"
	case c.Reopened:
		return "  [hot 에 다시 열림 — 다음 tier 뒤에 복원할 수 있다]"
	}
	return ""
}

// coldList 는 이 workspace 팀의 cold 인덱스 목록이다. 다른 meta-repo 의 팀이면 보여주지도 복원하지도 않는다 — 스냅샷
// repository 는 클러스터가 같이 쓰지만, 복원과 deprecate 다시 입히기는 그 팀을 가진 workspace 의 원장으로만 맞다.
func (h *Handlers) coldList(ctx context.Context, team string) ([]cold.Index, error) {
	list, err := cold.ListCold(ctx, h.OS, h.Config, team)
	if err != nil || len(list) == 0 {
		return list, err
	}
	ours, err := cold.TeamInWorkspace(ctx, h.OS, h.Cold, h.Config.Workspace, team)
	if err != nil {
		return nil, err
	}
	if !ours {
		return nil, fmt.Errorf("%w: team %q 는 workspace %q 의 팀이 아닙니다 — cold 목록·복원(list_cold·restore_cold, CLI: list-cold·restore)은 이 meta-repo 가 적재한 팀의 cold 인덱스만 다룹니다",
			ingest.ErrTeamOwned, team, h.Config.Workspace)
	}
	return list, nil
}

// RestoreInput 은 restore_cold 입력이다.
type RestoreInput struct {
	Index     string `json:"index" jsonschema:"list_cold 가 돌려준 인덱스 이름 (mem-{team}-{yyyy.MM})"`
	Confirmed bool   `json:"confirmed,omitempty" jsonschema:"사용자가 복원에 동의했으면 true"`
}

// Restore 는 restore_cold 다. confirmed 가 없으면 복원하지 않고 "먼저 물으라"고 돌려준다.
func (h *Handlers) Restore(ctx context.Context, in RestoreInput) (string, error) {
	if !naming.IsMonthly(in.Index) {
		return "", fmt.Errorf("index 이름이 잘못됐습니다: %q", in.Index)
	}
	if !in.Confirmed {
		return fmt.Sprintf("복원하지 않았습니다. 사용자에게 \"cold 인덱스 %s가 있는데 복원할까요\"라고 먼저 묻고, 동의하면 confirmed=true 로 다시 호출하세요.", in.Index), nil
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	idx, _ := naming.ParseIndex(in.Index)
	if _, err := h.coldList(ctx, idx.Team); err != nil {
		return "", err
	}
	restored, err := cold.Restore(ctx, h.OS, h.Config, in.Index)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s 을(를) snapshot %s 에서 hot 으로 복원했습니다. 이제 검색(%s)에 포함됩니다 (ISM 자동 관리 제외).",
		restored.Name, strings.Join(restored.Snapshots, ","), searchHint), nil
}

// RelatedInput 은 related_docs 입력이다.
type RelatedInput struct {
	Path  string `json:"path" jsonschema:"meta-repository 기준 문서 경로 (search_memory 결과의 source_path)"`
	Depth int    `json:"depth,omitempty" jsonschema:"몇 걸음까지 (1~3, 기본 2). 2 면 같은 entity 를 다루는 문서도 나온다"`
}

// Related 는 related_docs 다 — knowledge graph 에서 문서와 이어진 문서를 찾는다.
func (h *Handlers) Related(ctx context.Context, in RelatedInput) (string, error) {
	if h.Graph == nil {
		return "", graph.ErrDisabled
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path 가 비어 있습니다")
	}
	depth := in.Depth
	if depth == 0 {
		depth = 2
	}
	path := ingest.CanonicalRel(strings.TrimSpace(in.Path)) // 그래프의 문서 경로는 source_path 와 같은 NFC 다
	ns, err := h.Graph.Neighbors(ctx, h.Config.Workspace, path, depth)
	if err != nil {
		return "", err
	}
	if len(ns) == 0 {
		return path + ": 그래프에서 이어진 문서가 없습니다 (그래프에 없는 문서이거나 링크·entity 가 없다)", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 와 이어진 문서 %d건 (knowledge graph)\n", path, len(ns))
	for _, n := range ns {
		fmt.Fprintf(&b, "- [%d] %s (%s) — %s\n", n.Hops, n.Path, n.Status, describePath(n))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func describePath(n graph.Neighbor) string {
	parts := make([]string, 0, len(n.Rels))
	for i, rel := range n.Rels {
		step := rel
		if i < len(n.Whys) && n.Whys[i] != "" {
			step += "(" + n.Whys[i] + ")"
		}
		if i < len(n.Via) {
			step += " → " + n.Via[i]
		}
		parts = append(parts, step)
	}
	return strings.Join(parts, " · ")
}

// EntityInput 은 docs_by_entity 입력이다.
type EntityInput struct {
	Entity string `json:"entity" jsonschema:"entity 이름 또는 type:name (예: service:order-api). 부분 일치"`
}

// Entities 는 docs_by_entity 다 — 증류가 뽑은 entity 로 문서를 찾는다.
func (h *Handlers) Entities(ctx context.Context, in EntityInput) (string, error) {
	if h.Graph == nil {
		return "", graph.ErrDisabled
	}
	if strings.TrimSpace(in.Entity) == "" {
		return "", fmt.Errorf("entity 가 비어 있습니다")
	}
	es, err := h.Graph.Entities(ctx, h.Config.Workspace, in.Entity)
	if err != nil {
		return "", err
	}
	if len(es) == 0 {
		return fmt.Sprintf("%q 에 맞는 entity 가 없습니다 (entity 는 증류가 켜져 있을 때 생긴다: MEMORY_DISTILL_ENABLED)", in.Entity), nil
	}
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, "%s:%s — %s\n", e.Type, e.Name, strings.Join(e.Docs, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (h *Handlers) team(t string) (string, error) {
	if t == "" {
		return h.Config.DefaultTeam, nil
	}
	return t, naming.ValidateTeam(t)
}

// ServerName 은 MCP 서버 이름이다. .mcp.json 에도 같은 이름으로 등록한다 (도구는 mcp__drakejin-workspace-memory-mcp__*).
const ServerName = "drakejin-workspace-memory-mcp"

// NewServer 는 도구 일곱 개를 단 MCP 서버다.
func NewServer(h *Handlers) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: Version}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "search_memory",
		Title: "meta-repository 문서 검색",
		Description: "meta-repository 에 쌓인 spec·ADR·PRD·plan·knowledge 문서를 BM25 로 검색하거나 필터로 가져온다 (hot 영역). " +
			"작업을 시작하면 파일 트리를 뒤지기 전에 먼저 부른다. 질의에는 repository 이름, 서비스명, 함수명, 에러 코드 같은 식별자를 그대로 넣고, " +
			"결과가 부족하면 단어를 바꿔 다시 부른다. 필터: project(프로젝트), topic(주제), date(날짜 하루), time_range(기간 범위), doc_type — " +
			"쓸 수 있는 값은 search_filters 로 본다. query 를 비우고 필터만 주면 맞는 문서를 최신순으로 가져온다. " +
			"결과의 source_path 를 Read 해서 원문을 근거로 일한다 (스니펫을 답으로 쓰지 않는다). 더 새 문서에 밀린 문서는 기본으로 숨긴다.",
		Annotations: readOnly,
	}, wrap(h.Log, h.Search))

	mcp.AddTool(server, &mcp.Tool{
		Name:  "search_filters",
		Title: "검색 필터 값",
		Description: "search_memory 의 필터에 쓸 수 있는 값과 문서 수를 보여 준다 — 프로젝트, 주제, 문서 종류, 날짜(가장 이른·늦은 날, 날짜별 수). " +
			"필터를 주면 그 범위 안에서 센다.",
		Annotations: readOnly,
	}, wrap(h.Log, h.Filters))

	mcp.AddTool(server, &mcp.Tool{
		Name:  "ingest_path",
		Title: "문서 수동 적재",
		Description: "데몬이 아직 잡지 못한 문서나 디렉토리를 바로 색인한다. 경로는 meta-repository 기준 상대 경로. " +
			"바이너리·크기 초과 파일은 데몬처럼 색인하지 않고 건너뛴 것으로 알린다. 적재한 문서는 다음 데몬 사이클이 판정·증류·그래프로 넘긴다.",
	}, wrap(h.Log, h.Ingest))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_cold",
		Title:       "cold 인덱스 목록",
		Description: "hot 기간(기본 60일)이 지나 스냅샷(cold)으로 내려간 팀의 인덱스 목록. 오래된 맥락이 필요할 때 확인한다.",
		Annotations: readOnly,
	}, wrap(h.Log, h.ListCold))

	mcp.AddTool(server, &mcp.Tool{
		Name:  "restore_cold",
		Title: "cold 인덱스 복원",
		Description: "스냅샷에서 cold 인덱스 하나를 hot 으로 복원한다. 반드시 사용자에게 \"cold 인덱스 X가 있는데 복원할까요\"를 먼저 묻고, " +
			"동의를 받은 경우에만 confirmed=true 로 호출한다.",
	}, wrap(h.Log, h.Restore))

	mcp.AddTool(server, &mcp.Tool{
		Name:  "related_docs",
		Title: "이어진 문서 (knowledge graph)",
		Description: "knowledge graph 에서 문서와 이어진 문서를 찾는다 — 개정 관계(SUPERSEDES), 근거(DERIVED_FROM), 관련(RELATES), " +
			"본문 링크(LINKS_TO), 같은 entity 를 다루는 문서. search_memory 로 찾은 문서의 배경을 넓힐 때 쓴다.",
		Annotations: readOnly,
	}, wrap(h.Log, h.Related))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "docs_by_entity",
		Title:       "entity 로 문서 찾기",
		Description: "서비스·테이블·API·개념 같은 entity 이름으로 그것을 다루는 문서를 찾는다 (증류가 뽑은 entity, knowledge graph).",
		Annotations: readOnly,
	}, wrap(h.Log, h.Entities))

	return server
}

func wrap[In any](log func(string, ...any), fn func(context.Context, In) (string, error)) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		text, err := fn(ctx, in)
		if err != nil {
			log("[memory-mcp] tool error: %v", err)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "실패: " + err.Error()}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}
