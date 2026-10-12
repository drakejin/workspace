// Package daemon 은 reconcile 데몬이다 — 인덱스·판정 플래그·증류 반영·그래프 같은 모든 파생물의 유일한 writer.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/drakejin/workspace/app/internal/audit"
	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/graph"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/llm"
	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// auditEvery 는 데몬이 정합성 감사를 도는 주기다 (보고만 한다 — 수리는 make audit APPLY=1).
const auditEvery = 7 * 24 * time.Hour

// Deps 는 데몬 의존이다. nil 이면 설정으로 만든다.
type Deps struct {
	Config config.Config
	OS     opensearch.Client
	Ask    llm.Asker
	Cold   cold.Store
	// Graph 가 nil 이고 MEMORY_GRAPH_URI 가 off 가 아니면 Neo4j 에 붙는다. 붙지 못해도 데몬은 돈다.
	Graph graph.Store
	// Log 는 사이클과 증류 goroutine 들에서 동시에 불린다 — 동시성 안전해야 한다 (log.Logger.Printf 처럼).
	Log func(format string, args ...any)
	// RetryDelays 는 부팅 bootstrap 재시도 간격이다. 마지막 값으로 계속 기다린다.
	RetryDelays []time.Duration
	// Now 는 시계다 (테스트용). nil 이면 time.Now.
	Now func() time.Time
}

// Daemon 은 reconcile 데몬이다. 매 사이클이 곧 풀 싱크라 증분/풀 구분도, 첫 기동 특수 분기도 없다.
// 죽어 있던 사이의 변경은 다음 boot 가 흡수한다.
type Daemon struct {
	// ctx 는 프로세스 수명이다. 사이클·판정·증류가 모두 이 ctx 로 돈다 — 종료 신호가 진행 중인 호출까지 끊는다.
	ctx      context.Context
	inflight sync.WaitGroup
	cfg      config.Config
	os       opensearch.Client
	graph    graph.Store
	// ownsGraph 면 데몬이 연 연결이라 Shutdown 이 닫는다. Deps.Graph 로 받은 연결은 건드리지 않는다.
	ownsGraph bool
	log       func(format string, args ...any)
	now       func() time.Time
	retry     []time.Duration
	judge     *Judge
	cache     *state.DistillCache
	store     cold.Store
	ix        ingest.Indexer
	rec       *Reconciler
	distill   *DistillQueue
	serial    *Serial
	auditTk   state.Tick

	mu           sync.Mutex
	startedAt    time.Time
	cycles       int
	lastCycle    *state.CycleRecord
	locked       bool
	graphBuilt   bool
	graphDistill int64 // 마지막 그래프 빌드 때의 증류 반영 수
}

// New 는 데몬이다. 저장소에 연결하지 않는다 — 부팅은 Boot.
func New(ctx context.Context, d Deps) (*Daemon, error) {
	cfg := d.Config
	if d.Log == nil {
		return nil, errors.New("daemon: Log 가 필요합니다")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.OS == nil {
		d.OS = opensearch.New(cfg.OpenSearchURL)
	}
	if d.Ask == nil {
		d.Ask = llm.New(cfg.LLM, d.Log)
	}
	if d.Cold == nil {
		store, err := cold.NewStore(ctx, cfg.Cold, cfg.Workspace)
		if err != nil {
			return nil, err
		}
		d.Cold = store
	}
	ownsGraph := false
	if d.Graph == nil && cfg.Graph.URI != "" {
		g, err := graph.New(cfg.Graph)
		if err != nil {
			return nil, err
		}
		d.Graph, ownsGraph = g, true
	}
	if d.RetryDelays == nil {
		d.RetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 60 * time.Second}
	}
	corrupt := func(file string, line int) {
		d.Log("[ledger] %s:%d 읽을 수 없는 줄 — 건너뜀 (make audit)", file, line)
	}
	judgeState := state.NewJudgeState(cfg.MetaRepoRoot)
	archiveState := state.NewArchiveState(cfg.MetaRepoRoot)
	cache := state.NewDistillCache(cfg.MetaRepoRoot)
	judgeState.Ledger.OnCorrupt, archiveState.Ledger.OnCorrupt, cache.Ledger.OnCorrupt = corrupt, corrupt, corrupt

	rec := &Reconciler{OS: d.OS, Config: cfg, Cold: d.Cold, Judge: judgeState, Archive: archiveState, Log: d.Log}
	ix := ingest.New(ingest.Deps{OS: d.OS, Cold: d.Cold, Config: cfg, Now: d.Now, Distilled: cache.Get, SupersededBy: rec.SupersededBy})
	rec.Indexer = ix
	dm := &Daemon{
		ctx: ctx, cfg: cfg, os: d.OS, graph: d.Graph, ownsGraph: ownsGraph, log: d.Log, now: d.Now, retry: d.RetryDelays,
		cache: cache, store: d.Cold, ix: ix, rec: rec,
		judge:     &Judge{OS: d.OS, Config: cfg, State: judgeState, Ask: d.Ask, Log: d.Log, Now: d.Now},
		distill:   NewDistillQueue(ctx, d.OS, cfg, cache, d.Ask, d.Log),
		auditTk:   state.NewTick(cfg.MetaRepoRoot, "audit"),
		startedAt: d.Now(),
	}
	dm.serial = NewSerial(dm.cycle)
	return dm, nil
}

// Boot 는 락 → bootstrap(재시도) → boot 사이클 → 증류 백필 이다.
func (d *Daemon) Boot(ctx context.Context, once bool) error {
	if err := state.AcquireLock(d.cfg.MetaRepoRoot); err != nil {
		return err
	}
	d.locked = true
	if err := d.cache.Load(); err != nil {
		return err
	}
	if err := d.bootstrap(ctx, once); err != nil {
		return err
	}
	if d.graph != nil {
		if err := d.graph.Ping(ctx); err != nil {
			d.log("[graph] Neo4j 에 붙지 못했습니다 — 그래프 단계 없이 돈다 (make memory-up): %v", err)
		}
	}
	d.serial.Run("boot")
	// 증류 공백 백필 — 지난 실패나 데몬 정지 중 변경으로 summary 가 빈 문서
	if d.cfg.Distill.Enabled {
		jobs, err := d.missingDistill(ctx)
		if err != nil {
			d.log("[distill] 백필 목록 실패: %v", err)
		}
		d.distill.Enqueue(jobs)
	}
	return nil
}

func (d *Daemon) bootstrap(ctx context.Context, once bool) error {
	for attempt := 0; ; attempt++ {
		err := opensearch.Bootstrap(ctx, d.os, d.cfg)
		if err == nil {
			return nil
		}
		if once && attempt >= len(d.retry)-1 {
			return err // --once 는 무한 대기하지 않는다
		}
		wait := d.retry[min(attempt, len(d.retry)-1)]
		d.log("[daemon] OpenSearch 준비 실패 (attempt %d): %v — %s 후 재시도", attempt+1, err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Trigger 는 사이클을 돌린다. 돌고 있으면 재실행 1회로 합쳐진다.
func (d *Daemon) Trigger(reason string) { d.serial.Run(reason) }

// Settle 은 증류 큐가 빌 때까지 기다리고, 그 사이 바뀐 entity 를 그래프에 반영한다.
func (d *Daemon) Settle() {
	d.distill.Settle()
	if d.distill.Applied() != d.graphDistill {
		d.Trigger("settle")
	}
}

// Shutdown 은 진행 중 사이클·증류를 마치고 락을 푼다. ctx 가 이미 끝났으면 진행 중 호출은 곧 끊긴다.
func (d *Daemon) Shutdown() error {
	d.inflight.Wait()
	d.distill.Settle()
	if d.ownsGraph {
		d.ownsGraph = false
		_ = d.graph.Close(context.Background())
	}
	if !d.locked {
		return nil
	}
	d.locked = false
	return state.ReleaseLock(d.cfg.MetaRepoRoot)
}

func (d *Daemon) cycle(trigger string) {
	ctx := d.ctx
	if ctx.Err() != nil {
		return
	}
	rec := &state.CycleRecord{Trigger: trigger, At: d.now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		d.mu.Lock()
		d.cycles++
		d.lastCycle = rec
		d.mu.Unlock()
		if err := d.Heartbeat(); err != nil {
			d.log("[daemon] 상태 기록 실패: %v", err)
		}
	}()

	// A. hot 동기화 — 변경분(diff)이 나온다. 끝에서 superseded_by·deprecated 를 원장대로 수렴시킨다
	sum, diff, err := d.rec.Once(ctx)
	if err != nil {
		d.log("[reconcile] failed: %v", err)
		rec.Error = err.Error()
		if len(diff) == 0 {
			return
		}
		// 이미 색인에 쓴 변경분은 판정·증류·그래프로 이어 간다 — 다음 사이클에는 바뀌지 않은 파일로 보여 놓친다
	} else {
		d.log("[reconcile] scanned=%d indexed=%d stat_updated=%d removed=%d skipped=%d failed=%d cold_owned=%d flags=%d trigger=%s took=%dms",
			sum.Scanned, sum.Indexed, sum.StatUpdated, sum.Removed, sum.Skipped, sum.Failed, sum.ColdOwned, sum.FlagsConverged, trigger, sum.TookMs)
		rec.Summary = &sum
	}

	// D. 판정 게이트 — 실패해도 색인 사이클은 산다. 실패한 문서는 다음 사이클이 다시 판정한다
	flags := sum.FlagsConverged
	judged, err := d.judge.Run(ctx, diff)
	if err != nil {
		d.log("[judge] 일부 실패 — 다음 사이클에 다시: %v", err)
	}
	rec.Judge = &judged
	if judged.Candidates > 0 {
		d.log("[judge] candidates=%d latestWins=%d llm=%d questions=%d idempotentSkip=%d",
			judged.Candidates, judged.LatestWins, judged.LLMJudged, judged.Questions, judged.SkippedIdempotent)
	}
	if judged.LatestWins+judged.LLMJudged > 0 {
		// 방금 내린 판정을 같은 사이클 안에서 인덱스에 반영한다
		n, err := d.rec.Converge(ctx)
		if err != nil {
			d.log("[judge] 반영 실패 — 다음 사이클이 수렴: %v", err)
		}
		flags += n
	}

	// E. 증류 큐 — 넣기만 하고 돌아온다. 소비는 백그라운드
	d.distill.EnqueueDiff(diff)

	// F. 그래프 — 문서·관계·entity 가 바뀌었을 때만 다시 만든다
	rec.Graph = d.syncGraph(ctx, len(diff) > 0 || flags > 0)

	// 주간 감사 — 보고만 한다
	d.maybeAudit(ctx)
}

func (d *Daemon) syncGraph(ctx context.Context, changed bool) string {
	if d.graph == nil {
		return "off"
	}
	applied := d.distill.Applied()
	d.mu.Lock()
	needed := changed || !d.graphBuilt || applied != d.graphDistill
	d.mu.Unlock()
	if !needed {
		return "unchanged"
	}
	indices, err := ingest.ManagedIndices(ctx, d.os)
	if err != nil {
		return "failed: " + err.Error()
	}
	docs, err := ingest.FetchIndexedDocs(ctx, d.os, indices, d.cfg.Workspace)
	if err != nil {
		return "failed: " + err.Error()
	}
	g, err := graph.Rebuild(ctx, d.graph, d.cfg.MetaRepoRoot, d.cfg.Workspace, docs)
	if err != nil {
		d.log("[graph] 적재 실패 — 다음 사이클 재시도: %v", err)
		return "failed: " + err.Error()
	}
	d.mu.Lock()
	d.graphBuilt, d.graphDistill = true, applied
	d.mu.Unlock()
	summary := fmt.Sprintf("docs=%d entities=%d edges=%d", len(g.Docs), len(g.Entities), len(g.Edges))
	d.log("[graph] %s", summary)
	return summary
}

func (d *Daemon) maybeAudit(ctx context.Context) {
	now := d.now()
	if !d.auditTk.Due(now, auditEvery) {
		return
	}
	rep, err := audit.Run(ctx, audit.Deps{OS: d.os, Config: d.cfg, Cold: d.store, Indexer: d.ix, Now: now}, false)
	if err != nil {
		d.log("[audit] 주간 감사 실패 — 다음 사이클 재시도: %v", err)
		return
	}
	d.log("[audit] 주간 감사: %s", audit.Summary(rep))
	if err := d.auditTk.Done(now); err != nil {
		d.log("[audit] 틱 기록 실패: %v", err)
	}
}

// Heartbeat 는 상태 파일을 쓴다.
func (d *Daemon) Heartbeat() error {
	d.mu.Lock()
	s := state.Status{
		PID: os.Getpid(), StartedAt: d.startedAt.UTC().Format(time.RFC3339Nano), HeartbeatAt: d.now().UTC().Format(time.RFC3339Nano),
		IntervalSeconds: int(d.cfg.Reconcile.Interval / time.Second), Cycles: d.cycles, LastCycle: d.lastCycle, DistillPending: d.distill.Pending(),
	}
	d.mu.Unlock()
	return state.WriteStatus(d.cfg.MetaRepoRoot, s)
}

func (d *Daemon) missingDistill(ctx context.Context) ([]DistillJob, error) {
	indices, err := ingest.ManagedIndices(ctx, d.os)
	if err != nil {
		return nil, err
	}
	docs, err := ingest.FetchIndexedDocs(ctx, d.os, indices, d.cfg.Workspace)
	if err != nil {
		return nil, err
	}
	var jobs []DistillJob
	for _, doc := range docs {
		if doc.Summary != "" || doc.BodySHA == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.cfg.MetaRepoRoot, filepath.FromSlash(doc.SourcePath)))
		if err != nil {
			continue
		}
		_, body := markdown.SplitFrontmatter(string(b))
		jobs = append(jobs, DistillJob{Team: doc.Team, SourcePath: doc.SourcePath, BodySHA: doc.BodySHA, Title: document.Title(body, doc.SourcePath), Body: body})
	}
	return jobs, nil
}

// Run 은 상주 루프다: 타이머(interval) + 파일 watcher(debounce) 힌트. ctx 가 끝나면 돌아온다.
// watcher 이벤트 내용은 쓰지 않는다 — 사이클을 앞당기는 힌트일 뿐이고, 색인 범위 밖 트리는 watch 도 열지 않는다.
func (d *Daemon) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	defer watcher.Close()
	root := d.cfg.MetaRepoRoot
	if err := addWatches(watcher, root, root); err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	ticker := time.NewTicker(d.cfg.Reconcile.Interval)
	defer ticker.Stop()
	var debounce *time.Timer
	fire := make(chan struct{}, 1)
	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return nil
		case <-ticker.C:
			d.triggerAsync("timer")
		case <-fire:
			d.triggerAsync("watch")
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = addWatches(watcher, root, ev.Name)
				}
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(d.cfg.Reconcile.Debounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			d.log("[daemon] watcher error: %v", err)
		}
	}
}

// triggerAsync 는 사이클을 별도 goroutine 에서 돌린다. 이벤트 루프는 계속 종료 신호를 본다.
// 여러 번 불려도 Serial 이 재실행 1회로 합친다.
func (d *Daemon) triggerAsync(reason string) {
	d.inflight.Add(1)
	go func() {
		defer d.inflight.Done()
		d.Trigger(reason)
	}()
}

func addWatches(w *fsnotify.Watcher, root, dir string) error {
	return filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if p != root && !ingest.InScope(rel) {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}
