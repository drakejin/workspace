package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/llm"
	"github.com/drakejin/workspace/internal/markdown"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Deps 는 데몬 의존이다. OS·Ask·Cold 가 nil 이면 설정으로 만든다.
type Deps struct {
	Config config.Config
	OS     opensearch.Client
	Ask    llm.Asker
	Cold   cold.Store
	// Log 는 사이클과 증류 goroutine 들에서 동시에 불린다 — 동시성 안전해야 한다 (log.Logger.Printf 처럼).
	Log func(format string, args ...any)
	// RetryDelays 는 부팅 bootstrap 재시도 간격이다. 마지막 값으로 계속 기다린다.
	RetryDelays []time.Duration
}

// Daemon 은 reconcile 데몬 — 인덱스·판정 플래그·증류 반영 같은 모든 파생물의 유일한 writer 다.
// 매 사이클이 곧 풀 싱크라 증분/풀 구분도, 첫 기동 특수 분기도 없다. 죽어 있던 사이의 변경은 다음 boot 가 흡수한다.
type Daemon struct {
	// ctx 는 프로세스 수명이다. 사이클·판정·증류가 모두 이 ctx 로 돈다 — 종료 신호가 진행 중인 호출까지 끊는다.
	ctx      context.Context
	inflight sync.WaitGroup
	cfg      config.Config
	os       opensearch.Client
	log      func(format string, args ...any)
	retry    []time.Duration
	judge    *Judge
	state    *JudgeState
	cache    *DistillCache
	rec      *Reconciler
	distill  *DistillQueue
	serial   *Serial

	mu        sync.Mutex
	startedAt time.Time
	cycles    int
	lastCycle *CycleRecord
	locked    bool
}

// New 는 데몬이다. I/O 를 하지 않는다 — 부팅은 Boot.
func New(ctx context.Context, d Deps) (*Daemon, error) {
	cfg := d.Config
	if d.Log == nil {
		return nil, errors.New("daemon: Log 가 필요합니다")
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
	if d.RetryDelays == nil {
		d.RetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 60 * time.Second}
	}
	state := NewJudgeState(cfg.MetaRepoRoot)
	cache := NewDistillCache(cfg.MetaRepoRoot)
	ix := ingest.New(ingest.Deps{OS: d.OS, Cold: d.Cold, Config: cfg, Distilled: cache.Get})
	dm := &Daemon{
		ctx: ctx,
		cfg: cfg, os: d.OS, log: d.Log, retry: d.RetryDelays, state: state, cache: cache,
		judge:     &Judge{OS: d.OS, Config: cfg, State: state, Ask: d.Ask, Log: d.Log},
		rec:       &Reconciler{OS: d.OS, Config: cfg, Indexer: ix, Cold: d.Cold, Judge: state, Log: d.Log},
		distill:   NewDistillQueue(ctx, d.OS, cfg, cache, d.Ask, d.Log),
		startedAt: time.Now(),
	}
	dm.serial = NewSerial(dm.cycle)
	return dm, nil
}

// Boot 는 락 → bootstrap(재시도) → boot 사이클 → 증류 백필 이다.
func (d *Daemon) Boot(ctx context.Context, once bool) error {
	if err := AcquireLock(d.cfg.MetaRepoRoot); err != nil {
		return err
	}
	d.locked = true
	if err := d.cache.Load(); err != nil {
		return err
	}
	if err := d.bootstrap(ctx, once); err != nil {
		return err
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

// Settle 은 증류 큐가 빌 때까지 기다린다.
func (d *Daemon) Settle() { d.distill.Settle() }

// Cycles 는 지금까지 돈 사이클 수다.
func (d *Daemon) Cycles() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cycles
}

// Shutdown 은 진행 중 사이클·증류를 마치고 락을 푼다. ctx 가 이미 끝났으면 진행 중 호출은 곧 끊긴다.
func (d *Daemon) Shutdown() error {
	d.inflight.Wait()
	d.distill.Settle()
	if !d.locked {
		return nil
	}
	d.locked = false
	return ReleaseLock(d.cfg.MetaRepoRoot)
}

func (d *Daemon) cycle(trigger string) {
	ctx := d.ctx
	if ctx.Err() != nil {
		return
	}
	rec := &CycleRecord{Trigger: trigger, At: time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		d.mu.Lock()
		d.cycles++
		d.lastCycle = rec
		d.mu.Unlock()
		if err := d.Heartbeat(); err != nil {
			d.log("[daemon] 상태 기록 실패: %v", err)
		}
	}()

	// A. hot 동기화 — 변경분(diff)이 나온다. 끝에서 superseded_by 를 원장대로 수렴시킨다
	sum, diff, err := d.rec.Once(ctx)
	if err != nil {
		d.log("[reconcile] failed: %v", err)
		rec.Error = err.Error()
		return
	}
	d.log("[reconcile] scanned=%d indexed=%d stat_updated=%d removed=%d skipped=%d cold_owned=%d flags=%d trigger=%s took=%dms",
		sum.Scanned, sum.Indexed, sum.StatUpdated, sum.Removed, sum.Skipped, sum.ColdOwned, sum.FlagsConverged, trigger, sum.TookMs)
	rec.Summary = &sum

	// D. 판정 게이트 — 실패해도 색인 사이클은 산다
	judged, err := d.judge.Run(ctx, diff)
	if err != nil {
		d.log("[judge] 게이트 실패 — 사이클 계속: %v", err)
	} else {
		rec.Judge = &judged
		if judged.Candidates > 0 {
			d.log("[judge] candidates=%d latestWins=%d llm=%d questions=%d idempotentSkip=%d",
				judged.Candidates, judged.LatestWins, judged.LLMJudged, judged.Questions, judged.SkippedIdempotent)
		}
		if judged.LatestWins+judged.LLMJudged > 0 {
			// 방금 내린 판정을 같은 사이클 안에서 인덱스에 반영한다
			indices, err := ManagedIndices(ctx, d.os)
			if err == nil {
				_, err = d.rec.ConvergeSupersededBy(ctx, indices)
			}
			if err != nil {
				d.log("[judge] 반영 실패 — 다음 사이클이 수렴: %v", err)
			}
		}
	}

	// E. 증류 큐 — 넣기만 하고 돌아온다. 소비는 백그라운드
	d.distill.EnqueueDiff(diff)
}

// Heartbeat 는 상태 파일을 쓴다.
func (d *Daemon) Heartbeat() error {
	d.mu.Lock()
	s := Status{
		PID: os.Getpid(), StartedAt: d.startedAt.UTC().Format(time.RFC3339Nano), HeartbeatAt: time.Now().UTC().Format(time.RFC3339Nano),
		IntervalSeconds: int(d.cfg.Reconcile.Interval / time.Second), Cycles: d.cycles, LastCycle: d.lastCycle, DistillPending: d.distill.Pending(),
	}
	d.mu.Unlock()
	return WriteStatus(d.cfg.MetaRepoRoot, s)
}

func (d *Daemon) missingDistill(ctx context.Context) ([]DistillJob, error) {
	indices, err := ManagedIndices(ctx, d.os)
	if err != nil {
		return nil, err
	}
	docs, err := FetchIndexedDocs(ctx, d.os, indices, d.cfg.Workspace)
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
		jobs = append(jobs, DistillJob{Team: doc.Team, SourcePath: doc.SourcePath, BodySHA: doc.BodySHA, Title: titleOf(body, doc.SourcePath), Body: body})
	}
	return jobs, nil
}

// Run 은 상주 루프다: 타이머(interval) + 파일 watcher(debounce) 힌트. ctx 가 끝나면 돌아온다.
// watcher 이벤트 내용은 쓰지 않는다 — 사이클을 앞당기는 힌트일 뿐이고, 색인 범위 밖 트리는 watch 도 열지 않는다.
func (d *Daemon) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	root := d.cfg.MetaRepoRoot
	if err := addWatches(watcher, root, root); err != nil {
		return err
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

func titleOf(body, rel string) string {
	if m := titlePattern.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return filepath.Base(rel)
}
