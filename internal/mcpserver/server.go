// Package mcpserver 는 MCP 도구 네 개다: search_memory · ingest_path · list_cold · restore_cold.
package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
	"github.com/drakejin/workspace/internal/search"
)

// Version 은 서버 버전이다.
const Version = "0.2.0"

// Deps 는 서버 의존이다.
type Deps struct {
	Config  config.Config
	OS      opensearch.Client
	Cold    cold.Store
	Indexer ingest.Indexer
	Log     func(format string, args ...any)
}

// Handlers 는 도구 구현이다. 전송(MCP)과 분리해 CLI 도 같은 함수를 쓴다.
type Handlers struct {
	Deps
	readyOnce sync.Mutex
	ready     bool
}

// NewHandlers 는 Handlers 다.
func NewHandlers(d Deps) *Handlers { return &Handlers{Deps: d} }

// Ready 는 템플릿·ISM·스냅샷 repository 를 한 번 맞춘다. 실패하면 다음 호출에서 다시 시도한다.
func (h *Handlers) Ready(ctx context.Context) error {
	h.readyOnce.Lock()
	defer h.readyOnce.Unlock()
	if h.ready {
		return nil
	}
	if err := opensearch.Bootstrap(ctx, h.OS, h.Config); err != nil {
		return err
	}
	h.ready = true
	return nil
}

// SearchInput 은 search_memory 입력이다.
type SearchInput struct {
	Team              string          `json:"team,omitempty" jsonschema:"팀/실 이름. 생략하면 기본 팀"`
	Query             string          `json:"query" jsonschema:"자연어 문장 또는 식별자(repository·서비스·함수 이름, 에러 코드)"`
	TimeRange         model.TimeRange `json:"time_range,omitempty" jsonschema:"event_time 범위"`
	Repo              string          `json:"repo,omitempty" jsonschema:"관리 대상 repository 로 좁히기"`
	DocType           string          `json:"doc_type,omitempty" jsonschema:"spec | adr | prd | plan | knowledge | guide | note"`
	Size              int             `json:"size,omitempty" jsonschema:"결과 수 (1~50, 기본 8)"`
	IncludeSuperseded bool            `json:"include_superseded,omitempty" jsonschema:"판정·개정으로 밀린 문서도 본다 (기본: 숨김)"`
}

// Search 는 search_memory 다.
func (h *Handlers) Search(ctx context.Context, in SearchInput) (string, error) {
	team, err := h.team(in.Team)
	if err != nil {
		return "", err
	}
	req := search.Request{Workspace: h.Config.Workspace, Team: team, Query: in.Query, TimeRange: in.TimeRange, Repo: in.Repo, Size: in.Size, IncludeSuperseded: in.IncludeSuperseded}
	if in.DocType != "" {
		t, ok := model.ParseDocType(in.DocType)
		if !ok {
			return "", fmt.Errorf("doc_type 이 잘못됐습니다: %q", in.DocType)
		}
		req.DocType = t
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	hits, err := search.Search(ctx, h.OS, req)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("team=%s hot 영역에서 %q 에 맞는 문서가 없습니다.\n"+
			"단어(서비스명, 함수명, 에러 코드)를 바꿔 다시 검색하세요. 60일보다 오래된 맥락이면 list_cold 를 확인하세요.", team, in.Query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "team=%s · %d건 (BM25)\n\n", team, len(hits))
	for i, hit := range hits {
		meta := []string{string(hit.DocType)}
		if hit.Repo != "" {
			meta = append(meta, hit.Repo)
		}
		if len(hit.EventTime) >= 10 {
			meta = append(meta, hit.EventTime[:10])
		}
		if len(hit.GitSHA) >= 7 {
			meta = append(meta, hit.GitSHA[:7])
		}
		flag := ""
		if hit.SupersededBy != "" {
			flag = "  [superseded → " + hit.SupersededBy + "]"
		}
		fmt.Fprintf(&b, "[%d] %s › %s  (%s; score %.2f)%s\n", i+1, hit.SourcePath, hit.HeaderPath, strings.Join(meta, ", "), hit.Score, flag)
		if hit.Summary != "" {
			fmt.Fprintf(&b, "요약: %s\n", hit.Summary)
		}
		b.WriteString(hit.Text + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
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

// FormatIngest 는 적재 결과 목록이다.
func FormatIngest(results []ingest.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d개 문서 적재", len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "\n- %s → %s (%d chunks)", r.SourcePath, r.Index, r.Chunks)
	}
	return b.String()
}

// ListColdInput 은 list_cold 입력이다.
type ListColdInput struct {
	Team string `json:"team,omitempty" jsonschema:"팀/실 이름. 생략하면 기본 팀"`
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
	list, err := cold.ListCold(ctx, h.OS, h.Config, team)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "team=" + team + ": cold 인덱스가 없습니다.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "team=%s cold 인덱스 %d개 (repository %s)\n", team, len(list), h.Config.Snapshot.Repository)
	for _, c := range list {
		restored := ""
		if c.Restored {
			restored = "  [현재 복원됨]"
		}
		fmt.Fprintf(&b, "- %s  snapshot=%s  at=%s%s\n", c.Name, c.Snapshot, c.SnapshotTime, restored)
	}
	b.WriteString("\n복원이 필요하면 사용자에게 먼저 \"cold 인덱스 X가 있는데 복원할까요\"라고 묻고, 동의를 받은 뒤 restore_cold(index, confirmed=true).")
	return b.String(), nil
}

// RestoreInput 은 restore_cold 입력이다.
type RestoreInput struct {
	Index     string `json:"index" jsonschema:"list_cold 가 돌려준 인덱스 이름 (mem-{team}-{yyyy.MM})"`
	Confirmed bool   `json:"confirmed,omitempty" jsonschema:"사용자가 복원에 동의했으면 true"`
}

var monthIndexName = regexp.MustCompile(`^mem-[a-z0-9][a-z0-9-]*-\d{4}\.\d{2}$`)

// Restore 는 restore_cold 다. confirmed 가 없으면 복원하지 않고 "먼저 물으라"고 돌려준다.
func (h *Handlers) Restore(ctx context.Context, in RestoreInput) (string, error) {
	if !monthIndexName.MatchString(in.Index) {
		return "", fmt.Errorf("index 이름이 잘못됐습니다: %q", in.Index)
	}
	if !in.Confirmed {
		return fmt.Sprintf("복원하지 않았습니다. 사용자에게 \"cold 인덱스 %s가 있는데 복원할까요\"라고 먼저 묻고, 동의하면 confirmed=true 로 다시 호출하세요.", in.Index), nil
	}
	if err := h.Ready(ctx); err != nil {
		return "", err
	}
	restored, err := cold.Restore(ctx, h.OS, h.Config, in.Index)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s 을(를) snapshot %s 에서 hot 으로 복원했습니다. 이제 search_memory 에 포함됩니다 (ISM 자동 관리 제외).", restored.Name, restored.Snapshot), nil
}

func (h *Handlers) team(t string) (string, error) {
	if t == "" {
		return h.Config.DefaultTeam, nil
	}
	return t, naming.ValidateTeam(t)
}

// NewServer 는 도구 네 개를 단 MCP 서버다.
func NewServer(h *Handlers) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "memory-mcp", Version: Version}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "search_memory",
		Title: "meta-repository 문서 검색",
		Description: "meta-repository 에 쌓인 spec·ADR·PRD·plan·knowledge 문서를 BM25 로 검색한다 (hot 영역). " +
			"작업을 시작하면 파일 트리를 뒤지기 전에 먼저 부른다. 질의에는 repository 이름, 서비스명, 함수명, 에러 코드 같은 식별자를 그대로 넣고, " +
			"결과가 부족하면 단어를 바꿔 다시 부른다. 결과의 source_path 를 Read 해서 원문을 근거로 일한다 (스니펫을 답으로 쓰지 않는다). " +
			"더 새 문서에 밀린 문서는 기본으로 숨긴다.",
		Annotations: readOnly,
	}, wrap(h.Log, h.Search))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ingest_path",
		Title:       "문서 수동 적재",
		Description: "데몬이 아직 잡지 못한 문서나 디렉토리를 바로 색인한다. 경로는 meta-repository 기준 상대 경로.",
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
