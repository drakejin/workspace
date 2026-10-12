package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/llm"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

const (
	distillSystem = `너는 팀 문서 코퍼스의 증류기다. 문서 하나를 받아 요약과 entity 를 산출한다.
규칙: entities 는 문서가 실제로 다루는 대상만(스쳐간 언급 제외), "type:name" 형식, type 은 service|repo|table|api|concept|person 중 하나.
summary 는 검색 결과 카드용 한두 문장, 한국어. 문서에 없는 내용을 추론하지 않는다.`
	distillBodyRunes = 60_000
)

var distillSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":  map[string]any{"type": "string"},
		"entities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required":             []string{"summary", "entities"},
	"additionalProperties": false,
}

// DistillJob 은 증류할 문서 하나다.
type DistillJob struct {
	Team       string
	SourcePath string
	BodySHA    string
	Title      string
	Body       string
}

// DistillQueue 는 증류 큐(사이클 E)다. 캐시 키는 본문 sha — hit 면 LLM 0회, miss 면 로컬 claude 로
// {summary, entities} 를 뽑아 .cache/distill 원장에 append 하고 인덱스에 반영한다.
// 색인 사이클과 분리된 비동기 큐라 문서가 한꺼번에 바뀌어도 사이클이 막히지 않는다.
// 실패는 그 문서만 건너뛰고 다음 사이클(부팅 백필)에 다시 본다.
type DistillQueue struct {
	ctx   context.Context
	os    opensearch.Client
	cfg   config.Config
	cache *state.DistillCache
	ask   llm.Asker
	log   func(format string, args ...any)

	mu     sync.Mutex
	idle   *sync.Cond
	queue  []DistillJob
	queued map[string]bool
	active int
	// applied 는 인덱스에 반영한 증류 수다. 그래프 단계가 entity 가 바뀌었는지 볼 때 쓴다.
	applied atomic.Int64
}

// Applied 는 지금까지 인덱스에 반영한 증류 수다.
func (q *DistillQueue) Applied() int64 { return q.applied.Load() }

// NewDistillQueue 는 증류 큐다.
func NewDistillQueue(ctx context.Context, os opensearch.Client, cfg config.Config, cache *state.DistillCache, ask llm.Asker, log func(string, ...any)) *DistillQueue {
	q := &DistillQueue{ctx: ctx, os: os, cfg: cfg, cache: cache, ask: ask, log: log, queued: map[string]bool{}}
	q.idle = sync.NewCond(&q.mu)
	return q
}

func jobKey(j DistillJob) string { return j.Team + "\x00" + j.SourcePath + "\x00" + j.BodySHA }

// EnqueueDiff 는 변경분을 큐에 넣고 바로 돌아온다.
func (q *DistillQueue) EnqueueDiff(diff []DiffEntry) {
	jobs := make([]DistillJob, 0, len(diff))
	for _, d := range diff {
		if d.Change != ChangeRemoved {
			jobs = append(jobs, DistillJob{Team: d.Team, SourcePath: d.SourcePath, BodySHA: d.BodySHA, Title: d.Title, Body: d.Body})
		}
	}
	q.Enqueue(jobs)
}

// Enqueue 는 작업을 넣는다. 같은 (팀, 경로, 본문) 은 한 번만.
func (q *DistillQueue) Enqueue(jobs []DistillJob) {
	if !q.cfg.Distill.Enabled {
		return
	}
	q.mu.Lock()
	for _, j := range jobs {
		if q.queued[jobKey(j)] {
			continue
		}
		q.queued[jobKey(j)] = true
		q.queue = append(q.queue, j)
	}
	q.pumpLocked()
	q.mu.Unlock()
}

func (q *DistillQueue) pumpLocked() {
	for q.active < q.cfg.Distill.Concurrency && len(q.queue) > 0 {
		job := q.queue[0]
		q.queue = q.queue[1:]
		q.active++
		go func() {
			if err := q.process(job); err != nil {
				q.log("[distill] %s: %v", job.SourcePath, err)
			}
			q.mu.Lock()
			q.active--
			delete(q.queued, jobKey(job))
			q.pumpLocked()
			if q.active == 0 && len(q.queue) == 0 {
				q.idle.Broadcast()
			}
			q.mu.Unlock()
		}()
	}
}

func (q *DistillQueue) process(job DistillJob) error {
	ctx := q.ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	facts, ok := q.cache.Get(job.Team, job.BodySHA)
	if !ok {
		raw, ok := q.ask.Ask(ctx, llm.Input{
			System: distillSystem,
			User:   fmt.Sprintf("title: %s\npath: %s\n\n--- 본문 ---\n%s", job.Title, job.SourcePath, truncate(job.Body, distillBodyRunes)),
			Schema: distillSchema,
			Tag:    "distill",
		})
		if !ok {
			return errors.New("LLM 응답 없음 — 다음 사이클 재시도") // 자세한 원인은 llm 이 이미 남겼다
		}
		var out model.Distilled
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("LLM 응답이 스키마와 다릅니다 — 다음 사이클 재시도: %w", err)
		}
		if strings.TrimSpace(out.Summary) == "" {
			return errors.New("빈 요약 — 다음 사이클 재시도")
		}
		facts = model.Distilled{Summary: strings.TrimSpace(out.Summary), Entities: dedupe(out.Entities)}
		if err := q.cache.Put(job.Team, state.DistillRecord{BodySHA: job.BodySHA, SourcePath: job.SourcePath, Summary: facts.Summary, Entities: facts.Entities, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		q.log("[distill] %s: summary %d자, entities %d", job.SourcePath, len([]rune(facts.Summary)), len(facts.Entities))
	}
	indices, err := ingest.ManagedIndices(ctx, q.os)
	if err != nil {
		return err
	}
	// 이 결과를 만든 본문(body_sha)의 청크에만 — 그 사이 문서가 바뀌었으면 새 본문의 증류가 따로 돈다
	sameBody := []any{map[string]any{"term": map[string]any{"body_sha": job.BodySHA}}}
	if err := opensearch.UpdateDocWhere(ctx, q.os, indices, q.cfg.Workspace, job.SourcePath, sameBody, map[string]any{"summary": facts.Summary, "entities": facts.Entities}); err != nil {
		return err
	}
	q.applied.Add(1)
	return nil
}

// Settle 은 큐가 빌 때까지 기다린다 (--once 와 종료 때).
func (q *DistillQueue) Settle() {
	q.mu.Lock()
	for q.active > 0 || len(q.queue) > 0 {
		q.idle.Wait()
	}
	q.mu.Unlock()
}

// Pending 은 대기·진행 중 작업 수다.
func (q *DistillQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue) + q.active
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
