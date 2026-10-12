// Package ops 는 운영 명령이다: 데몬 상태, 판정 질문, CLI.
package ops

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/daemon"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Probe 는 상태 화면이 읽는 바깥 세계다. 테스트는 fake 를 쓴다.
type Probe interface {
	// ContainerState 는 compose opensearch 서비스 상태다. docker 가 없으면 ok=false.
	ContainerState(ctx context.Context) (state string, ok bool)
	ClusterHealth(ctx context.Context) (status string, ok bool)
	IndexStats(ctx context.Context) (IndexStats, bool)
}

// IndexStats 는 색인 현황이다.
type IndexStats struct {
	Indices     int
	Documents   int
	Chunks      int
	LastIndexed string
}

type liveProbe struct {
	cfg config.Config
	os  opensearch.Client
}

// NewProbe 는 실제 환경을 읽는 Probe 다.
func NewProbe(cfg config.Config, os opensearch.Client) Probe { return &liveProbe{cfg: cfg, os: os} }

func (p *liveProbe) ContainerState(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "--format", "{{.Service}} {{.State}}")
	cmd.Dir = p.cfg.MetaRepoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if state, ok := strings.CutPrefix(line, "opensearch "); ok {
			return strings.TrimSpace(state), true
		}
	}
	return "not started", true
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
	if err := p.os.Do(ctx, http.MethodGet, "/_cat/indices/"+naming.AllPattern, nil, map[string][]string{"format": {"json"}, "h": {"index"}}, &rows); err != nil {
		return IndexStats{}, false
	}
	count := func(query map[string]any) int {
		var out struct {
			Count int `json:"count"`
		}
		_ = p.os.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_count", map[string]any{"query": query}, opensearch.Lenient(), &out)
		return out.Count
	}
	var last struct {
		Aggregations struct {
			Last struct {
				ValueAsString string `json:"value_as_string"`
			} `json:"last"`
		} `json:"aggregations"`
	}
	_ = p.os.Do(ctx, http.MethodPost, "/"+naming.AllPattern+"/_search",
		map[string]any{"size": 0, "aggs": map[string]any{"last": map[string]any{"max": map[string]any{"field": "ingested_at"}}}}, opensearch.Lenient(), &last)
	return IndexStats{
		Indices: len(rows),
		Documents: count(map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"chunk_index": 0}},
			map[string]any{"term": map[string]any{"workspace": p.cfg.Workspace}},
		}}}),
		Chunks:      count(map[string]any{"term": map[string]any{"workspace": p.cfg.Workspace}}),
		LastIndexed: last.Aggregations.Last.ValueAsString,
	}, true
}

// RenderStatus 는 데몬 상태 화면이다. 읽기만 한다. 꺼진 것은 무엇을 실행하면 되는지 할 일로 안내한다.
func RenderStatus(ctx context.Context, cfg config.Config, probe Probe, now time.Time) string {
	var lines, hints []string
	add := func(format string, args ...any) { lines = append(lines, "[status] "+fmt.Sprintf(format, args...)) }
	hint := func(h string) {
		for _, existing := range hints {
			if existing == h {
				return
			}
		}
		hints = append(hints, h)
	}

	if state, ok := probe.ContainerState(ctx); !ok {
		add("docker: not running")
		hint("Docker(OrbStack) 실행 후 'make memory-up'")
	} else {
		add("container opensearch: %s", state)
		if state != "running" {
			hint("'make memory-up' 으로 스택 기동")
		}
	}

	if health, ok := probe.ClusterHealth(ctx); !ok {
		add("opensearch: unreachable (%s)", cfg.OpenSearchURL)
		hint("'make memory-up' 으로 스택 기동")
	} else {
		add("opensearch: %s (%s)", health, cfg.OpenSearchURL)
		if s, ok := probe.IndexStats(ctx); ok {
			add("index: workspace=%s %d docs, %d chunks (memory indices %d)", cfg.Workspace, s.Documents, s.Chunks, s.Indices)
			if s.LastIndexed != "" {
				add("last indexed: %s", s.LastIndexed)
			}
			if s.Documents == 0 {
				hint("'make reconcile' 로 1회 색인 (또는 'make daemon')")
			}
		}
	}

	root := cfg.MetaRepoRoot
	status := daemon.ReadStatus(root)
	if pid := daemon.RunningPID(root); pid == 0 {
		add("daemon: stopped")
		hint("'make daemon' 으로 데몬 실행 (터미널 하나를 계속 점유한다)")
	} else {
		if status != nil {
			add("daemon: running pid=%d since %s, cycles=%d", pid, status.StartedAt, status.Cycles)
			heartbeat, _ := time.Parse(time.RFC3339Nano, status.HeartbeatAt)
			age := now.Sub(heartbeat).Round(time.Second)
			add("heartbeat: %s ago (interval %ds)", age, status.IntervalSeconds)
			if age > 3*time.Duration(status.IntervalSeconds)*time.Second {
				hint("데몬 heartbeat 가 멈췄다 — 데몬 터미널 로그 확인 후 재시작")
			}
		} else {
			add("daemon: running pid=%d", pid)
		}
	}
	if status != nil && status.LastCycle != nil {
		last := status.LastCycle
		switch {
		case last.Error != "":
			add("last cycle: %s trigger=%s FAILED — %s", last.At, last.Trigger, last.Error)
		case last.Summary != nil:
			s := last.Summary
			refused := ""
			if s.Refused {
				refused = " REFUSED(sanityMaxFiles)"
			}
			add("last cycle: %s trigger=%s scanned=%d indexed=%d stat_updated=%d removed=%d skipped=%d cold_owned=%d took=%dms%s",
				last.At, last.Trigger, s.Scanned, s.Indexed, s.StatUpdated, s.Removed, s.Skipped, s.ColdOwned, s.TookMs, refused)
			if s.ColdOwned > 0 {
				hint(fmt.Sprintf("cold 로 넘어간 hot 원본 %d개 — 'make tier' 가 정리한다", s.ColdOwned))
			}
		}
	}

	state := daemon.NewJudgeState(root)
	teams, _ := state.Ledger.Teams()
	questions := 0
	for _, t := range teams {
		q, _ := state.PendingQuestions(t)
		questions += len(q)
	}
	add("judge: %s, 답을 기다리는 질문 %d건", onOff(cfg.Judge.Enabled), questions)
	if questions > 0 {
		hint("'make questions' 로 질문 확인 후 'make answer KEY=<pairKey> A=latest|old|keep'")
	}

	cache := daemon.NewDistillCache(root)
	_ = cache.Load()
	pending := ""
	if status != nil {
		pending = fmt.Sprintf(", 대기 %d건", status.DistillPending)
	}
	add("distill: %s, cache %d건%s", onOff(cfg.Distill.Enabled), cache.Size(""), pending)

	if len(hints) > 0 {
		lines = append(lines, "")
		for _, h := range hints {
			lines = append(lines, "[status] 할 일: "+h)
		}
	}
	return strings.Join(lines, "\n")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
