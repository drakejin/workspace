package ops

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// ComposeProject 는 docker-compose.yml 의 name 이다. META_REPO_ROOT 가 다른 meta-repo 를 가리켜도
// 스택을 이 이름으로 찾는다.
const ComposeProject = "drakejin-workspace-memory"

// heartbeatStaleFactor 는 heartbeat 가 interval 의 몇 배를 넘으면 멈춘 것으로 볼지다.
const heartbeatStaleFactor = 3

// Probe 는 상태 화면이 읽는 바깥 세계다. 테스트는 fake 를 쓴다.
type Probe interface {
	// Containers 는 compose 서비스 → 상태다. docker 가 없으면 ok=false.
	Containers(ctx context.Context) (map[string]string, bool)
	ClusterHealth(ctx context.Context) (status string, ok bool)
	IndexStats(ctx context.Context) (IndexStats, bool)
	// Graph 는 그래프 크기다. 꺼져 있으면 enabled=false, 붙지 못하면 err.
	Graph(ctx context.Context) (st graph.Stats, enabled bool, err error)
}

// IndexStats 는 색인 현황이다.
type IndexStats struct {
	Indices     int
	Documents   int
	Chunks      int
	LastIndexed string
}

type liveProbe struct {
	cfg   config.Config
	os    opensearch.Client
	graph graph.Store
}

// NewProbe 는 실제 환경을 읽는 Probe 다.
func NewProbe(cfg config.Config, os opensearch.Client, g graph.Store) Probe {
	return &liveProbe{cfg: cfg, os: os, graph: g}
}

func (p *liveProbe) Containers(ctx context.Context) (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "compose", "-p", ComposeProject, "ps", "--format", "{{.Service}} {{.State}}").Output()
	if err != nil {
		return nil, false
	}
	states := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if svc, st, ok := strings.Cut(line, " "); ok {
			states[svc] = strings.TrimSpace(st)
		}
	}
	return states, true
}

func (p *liveProbe) ClusterHealth(ctx context.Context) (string, bool) {
	var out struct {
		Status string `json:"status"`
	}
	if err := p.os.Do(ctx, http.MethodGet, "/_cluster/health", nil, nil, &out); err != nil {
		return "", false
	}
	return out.Status, true
}

func (p *liveProbe) IndexStats(ctx context.Context) (IndexStats, bool) {
	var rows []struct {
		Index string `json:"index"`
	}
	if err := p.os.Do(ctx, http.MethodGet, "/_cat/indices/"+naming.AllPattern, nil, url.Values{"format": {"json"}, "h": {"index"}}, &rows); err != nil {
		return IndexStats{}, false
	}
	ws := map[string]any{"term": map[string]any{"workspace": p.cfg.Workspace}}
	docs, _ := opensearch.Count(ctx, p.os, naming.AllPattern, opensearch.FirstChunks(p.cfg.Workspace))
	chunks, _ := opensearch.Count(ctx, p.os, naming.AllPattern, ws)
	var last struct {
		Aggregations struct {
			Last struct {
				ValueAsString string `json:"value_as_string"`
			} `json:"last"`
		} `json:"aggregations"`
	}
	_ = p.os.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search",
		map[string]any{"size": 0, "query": ws, "aggs": map[string]any{"last": map[string]any{"max": map[string]any{"field": "ingested_at"}}}}, opensearch.Lenient(), &last)
	return IndexStats{Indices: len(rows), Documents: docs, Chunks: chunks, LastIndexed: last.Aggregations.Last.ValueAsString}, true
}

func (p *liveProbe) Graph(ctx context.Context) (graph.Stats, bool, error) {
	if p.graph == nil {
		return graph.Stats{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := p.graph.Stats(ctx, p.cfg.Workspace)
	return st, true, err
}

// screen 은 상태 화면의 줄과 할 일이다.
type screen struct {
	lines []string
	hints []string
}

func (s *screen) add(format string, args ...any) {
	s.lines = append(s.lines, "[status] "+fmt.Sprintf(format, args...))
}

func (s *screen) hint(h string) {
	for _, existing := range s.hints {
		if existing == h {
			return
		}
	}
	s.hints = append(s.hints, h)
}

func (s *screen) String() string {
	out := s.lines
	if len(s.hints) > 0 {
		out = append(out, "")
		for _, h := range s.hints {
			out = append(out, "[status] 할 일: "+h)
		}
	}
	return strings.Join(out, "\n")
}

// RenderStatus 는 데몬 상태 화면이다. 읽기만 한다. 꺼진 것은 무엇을 실행하면 되는지 할 일로 안내한다.
func RenderStatus(ctx context.Context, cfg config.Config, probe Probe, now time.Time) string {
	s := &screen{}
	statusInfra(ctx, s, cfg, probe)
	status := state.ReadStatus(cfg.MetaRepoRoot)
	statusDaemon(s, cfg, status, now)
	statusLedgers(s, cfg, status)
	return s.String()
}

func statusInfra(ctx context.Context, s *screen, cfg config.Config, probe Probe) {
	containers, ok := probe.Containers(ctx)
	if !ok {
		s.add("docker: not running")
		s.hint("Docker(OrbStack) 실행 후 'make memory-up'")
	} else {
		for _, svc := range []string{"opensearch", "neo4j"} {
			st := containers[svc]
			if st == "" {
				st = "not started"
			}
			s.add("container %s: %s", svc, st)
			if st != "running" && (svc == "opensearch" || cfg.Graph.URI != "") {
				s.hint("'make memory-up' 으로 스택 기동")
			}
		}
	}
	if health, ok := probe.ClusterHealth(ctx); !ok {
		s.add("opensearch: unreachable (%s)", cfg.OpenSearchURL)
		s.hint("'make memory-up' 으로 스택 기동")
	} else {
		s.add("opensearch: %s (%s)", health, cfg.OpenSearchURL)
		if st, ok := probe.IndexStats(ctx); ok {
			s.add("index: workspace=%s %d docs, %d chunks (memory indices %d)", cfg.Workspace, st.Documents, st.Chunks, st.Indices)
			if st.LastIndexed != "" {
				s.add("last indexed: %s", st.LastIndexed)
			}
			if st.Documents == 0 {
				s.hint("'make reconcile' 로 1회 색인 (또는 'make daemon')")
			}
		}
	}
	switch gst, enabled, err := probe.Graph(ctx); {
	case !enabled:
		s.add("graph: off (MEMORY_GRAPH_URI=off)")
	case err != nil:
		s.add("graph: unreachable (%s)", cfg.Graph.URI)
		s.hint("'make memory-up' 으로 Neo4j 기동 — 색인·검색은 그래프 없이도 돈다")
	default:
		s.add("graph: %s docs, %d entities, %d edges (%s)", fmt.Sprint(gst.Nodes[graph.LabelDoc]), gst.Nodes[graph.LabelEntity], sum(gst.Edges), cfg.Graph.URI)
	}
}

func statusDaemon(s *screen, cfg config.Config, status *state.Status, now time.Time) {
	pid := state.RunningPID(cfg.MetaRepoRoot)
	switch {
	case pid == 0:
		s.add("daemon: stopped")
		s.hint("'make daemon' 으로 데몬 실행 (또는 'make daemon-service' 로 로그인 시 자동 실행)")
	case status != nil:
		s.add("daemon: running pid=%d since %s, cycles=%d", pid, status.StartedAt, status.Cycles)
		heartbeat, _ := time.Parse(time.RFC3339Nano, status.HeartbeatAt)
		age := now.Sub(heartbeat).Round(time.Second)
		s.add("heartbeat: %s ago (interval %ds)", age, status.IntervalSeconds)
		if age > heartbeatStaleFactor*time.Duration(status.IntervalSeconds)*time.Second {
			s.hint("데몬 heartbeat 가 멈췄다 — 데몬 로그 확인 후 재시작")
		}
	default:
		s.add("daemon: running pid=%d", pid)
	}
	if status == nil || status.LastCycle == nil {
		return
	}
	last := status.LastCycle
	switch {
	case last.Error != "":
		s.add("last cycle: %s trigger=%s FAILED — %s", last.At, last.Trigger, last.Error)
	case last.Summary != nil:
		c := last.Summary
		flags := ""
		if c.Refused {
			flags += " REFUSED(sanityMaxFiles)"
		}
		if c.Partial {
			flags += " PARTIAL-SCAN"
		}
		s.add("last cycle: %s trigger=%s scanned=%d indexed=%d stat_updated=%d removed=%d skipped=%d failed=%d cold_owned=%d took=%dms%s",
			last.At, last.Trigger, c.Scanned, c.Indexed, c.StatUpdated, c.Removed, c.Skipped, c.Failed, c.ColdOwned, c.TookMs, flags)
		if c.Failed > 0 {
			s.hint(fmt.Sprintf("색인에 실패한 파일 %d개 — 데몬 로그의 '[reconcile] fail' 줄을 보세요 (다음 사이클이 다시 시도한다)", c.Failed))
		}
		if c.ColdOwned > 0 {
			s.hint(fmt.Sprintf("cold 로 넘어간 hot 원본 %d개 — 'make tier' 가 정리한다", c.ColdOwned))
		}
	}
	if last.Graph != "" {
		s.add("last graph: %s", last.Graph)
	}
}

func statusLedgers(s *screen, cfg config.Config, status *state.Status) {
	judge := state.NewJudgeState(cfg.MetaRepoRoot)
	corrupt := 0
	count := func(string, int) { corrupt++ }
	judge.Ledger.OnCorrupt = count
	questions := 0
	all, err := judge.AllPendingQuestions()
	if err != nil {
		s.add("judge: 원장을 읽지 못했습니다 — %v", err)
	}
	for _, qs := range all {
		questions += len(qs)
	}
	s.add("judge: %s, 답을 기다리는 질문 %d건", onOff(cfg.Judge.Enabled), questions)
	if questions > 0 {
		s.hint("'make questions' 로 질문 확인 후 'make answer KEY=<pairKey> A=latest|old|keep'")
	}
	cache := state.NewDistillCache(cfg.MetaRepoRoot)
	cache.Ledger.OnCorrupt = count
	if err := cache.Load(); err != nil {
		s.add("distill: 캐시를 읽지 못했습니다 — %v", err)
	}
	pending := ""
	if status != nil {
		pending = fmt.Sprintf(", 대기 %d건", status.DistillPending)
	}
	s.add("distill: %s, cache %d건%s", onOff(cfg.Distill.Enabled), cache.Size(""), pending)
	if corrupt > 0 {
		s.hint(fmt.Sprintf("원장에 읽을 수 없는 줄 %d개 — 'make audit' 로 확인", corrupt))
	}
	tick := state.NewTick(cfg.MetaRepoRoot, "tier")
	if last := tick.Last(); !last.IsZero() {
		s.add("tier: 마지막 %s", last.Local().Format(time.RFC3339))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
