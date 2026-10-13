package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
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
	// distillRetryBase 는 실패한 증류를 다시 시도하기까지의 첫 대기다. 실패할 때마다 두 배, 최대 distillRetryMax —
	// LLM 을 쓸 수 없는 동안(미설치·미인증) 사이클마다 문서 전부를 다시 부르지 않게
	distillRetryBase = time.Minute
	distillRetryMax  = time.Hour
	// distillRetryMaxShift 는 백오프를 두 배로 늘리는 횟수의 상한이다 (그 뒤로는 distillRetryMax)
	distillRetryMaxShift = 10
	// distillMaxAttempts 는 같은 본문(body_sha)의 증류를 시도하는 상한이다 (첫 시도 포함). 다 실패하면 포기하고 알린다 — 늘 같은 식으로
	// 실패하는 본문(스키마 불일치·빈 요약)이 데몬이 도는 내내 매시간 LLM(기본 opus)을 부르지 않게. 본문이 바뀌면 처음부터 다시
	// 시도하고, 데몬을 다시 띄우면 boot 백필이 한 번 더 넣는다. 백오프로 첫 실패 뒤 약 두 시간에 걸쳐 시도한다
	distillMaxAttempts = 8
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
// 실패는 그 문서만 건너뛰고, 백오프(distillRetryBase 부터 두 배씩, 최대 distillRetryMax)가 지난 뒤의 사이클이 다시 넣는다 —
// 같은 본문은 distillMaxAttempts 번까지. 데몬이 다시 뜨면 부팅 백필(요약이 빈 문서)이 이어받는다.
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
	// failed 는 실패해 다시 시도를 기다리는 작업이다 (문서마다 마지막 본문 하나). now 는 그 백오프의 시계다
	failed map[string]retryJob
	now    func() time.Time
	// applied 는 인덱스에 반영한 증류 수다. 그래프 단계가 entity 가 바뀌었는지 볼 때 쓴다.
	applied atomic.Int64
}

// Applied 는 지금까지 인덱스에 반영한 증류 수다.
func (q *DistillQueue) Applied() int64 { return q.applied.Load() }

// NewDistillQueue 는 증류 큐다.
func NewDistillQueue(ctx context.Context, os opensearch.Client, cfg config.Config, cache *state.DistillCache, ask llm.Asker, log func(string, ...any)) *DistillQueue {
	q := &DistillQueue{ctx: ctx, os: os, cfg: cfg, cache: cache, ask: ask, log: log, queued: map[string]bool{}, failed: map[string]retryJob{}, now: time.Now}
	q.idle = sync.NewCond(&q.mu)
	return q
}

func jobKey(j DistillJob) string { return docKey(j.Team, j.SourcePath) + "\x00" + j.BodySHA }

func docKey(team, sourcePath string) string { return team + "\x00" + sourcePath }

// retryJob 은 실패해 다시 시도를 기다리는 작업이다.
type retryJob struct {
	job      DistillJob
	attempts int
	nextAt   time.Time
}

// retryWait 는 attempts 번 실패한 작업이 다시 시도하기까지 기다리는 시간이다.
func retryWait(attempts int) time.Duration {
	shift := min(max(attempts-1, 0), distillRetryMaxShift)
	return min(distillRetryBase<<shift, distillRetryMax)
}

// EnqueueDiff 는 변경분과, 다시 시도할 때가 된 실패분을 큐에 넣고 바로 돌아온다 (사이클마다 불린다).
func (q *DistillQueue) EnqueueDiff(diff []DiffEntry) {
	jobs := make([]DistillJob, 0, len(diff))
	for _, d := range diff {
		if d.Change != ChangeRemoved {
			jobs = append(jobs, DistillJob{Team: d.Team, SourcePath: d.SourcePath, BodySHA: d.BodySHA, Title: d.Title, Body: d.Body})
		}
	}
	q.Enqueue(append(q.dueRetries(diff), jobs...))
}

// dueRetries 는 다시 시도할 때가 된 실패분이다. 이번 변경분에 든 문서(고쳤거나 지웠다)의 실패분은 옛 본문이라 버린다.
// 꺼낸 작업도 목록에 남는다 — 성공해야 지워지고, 다시 실패하면 더 길게 기다린다 (recordLocked).
func (q *DistillQueue) dueRetries(diff []DiffEntry) []DistillJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, d := range diff {
		delete(q.failed, docKey(d.Team, d.SourcePath))
	}
	now := q.now()
	var due []DistillJob
	for _, r := range q.failed {
		if !now.Before(r.nextAt) {
			due = append(due, r.job)
		}
	}
	sort.Slice(due, func(i, k int) bool { return due[i].SourcePath < due[k].SourcePath })
	return due
}

// retryOutcome 은 작업 결과를 다시 시도 목록에 반영한 결과다.
type retryOutcome struct {
	wait     time.Duration // 다시 시도하기까지 기다리는 시간 (retrying 일 때)
	retrying bool
	gaveUp   bool // 같은 본문이 distillMaxAttempts 번 실패해 포기했다
	attempts int
}

// recordLocked 는 작업 결과를 다시 시도 목록에 반영한다: 성공이면 지우고, 실패면 백오프 뒤로 미루고, 같은 본문이
// distillMaxAttempts 번 실패했으면 목록에서 빼고 포기한다. 종료 중(ctx 끝)의 실패는 미루지 않는다 — 다시 뜬 데몬의 부팅 백필이
// 이어받는다.
func (q *DistillQueue) recordLocked(job DistillJob, err error) retryOutcome {
	key := docKey(job.Team, job.SourcePath)
	if err == nil {
		if r, ok := q.failed[key]; ok && r.job.BodySHA == job.BodySHA {
			delete(q.failed, key)
		}
		return retryOutcome{}
	}
	if q.ctx.Err() != nil {
		return retryOutcome{}
	}
	r := q.failed[key]
	if r.job.BodySHA != job.BodySHA {
		r = retryJob{job: job} // 새 본문 — 백오프와 시도 수를 처음부터
	}
	r.attempts++
	if r.attempts >= distillMaxAttempts {
		delete(q.failed, key)
		return retryOutcome{gaveUp: true, attempts: r.attempts}
	}
	wait := retryWait(r.attempts)
	r.nextAt = q.now().Add(wait)
	q.failed[key] = r
	return retryOutcome{wait: wait, retrying: true, attempts: r.attempts}
}

// Enqueue 는 작업을 넣는다. 같은 (팀, 경로, 본문) 은 한 번만. 본문이 없는 문서(빈 파일·frontmatter 만)는 요약할 것이
// 없으니 넣지 않는다.
func (q *DistillQueue) Enqueue(jobs []DistillJob) {
	if !q.cfg.Distill.Enabled {
		return
	}
	q.mu.Lock()
	for _, j := range jobs {
		if q.queued[jobKey(j)] || document.IsBlank(j.Body) {
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
			err := q.process(job)
			q.mu.Lock()
			out := q.recordLocked(job, err)
			q.active--
			delete(q.queued, jobKey(job))
			q.pumpLocked()
			if q.active == 0 && len(q.queue) == 0 {
				q.idle.Broadcast()
			}
			q.mu.Unlock()
			q.logResult(job, err, out)
		}()
	}
}

// logResult 는 실패한 작업의 처리(다시 시도·포기)를 알린다.
func (q *DistillQueue) logResult(job DistillJob, err error, out retryOutcome) {
	switch {
	case out.gaveUp:
		q.log("[distill] %s: %v — %d번 실패해 포기한다 (본문이 바뀌거나 데몬을 다시 띄우면 다시 시도한다)", job.SourcePath, err, out.attempts)
	case out.retrying:
		q.log("[distill] %s: %v — %s 뒤 사이클에 다시 시도", job.SourcePath, err, out.wait)
	case err != nil:
		q.log("[distill] %s: %v", job.SourcePath, err)
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
			return errors.New("LLM 응답 없음") // 자세한 원인은 llm 이 이미 남겼다
		}
		var out model.Distilled
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("LLM 응답이 스키마와 다릅니다: %w", err)
		}
		if strings.TrimSpace(out.Summary) == "" {
			return errors.New("빈 요약")
		}
		facts = model.Distilled{Summary: strings.TrimSpace(out.Summary), Entities: dedupe(out.Entities)}
		if err := q.cache.Put(job.Team, state.DistillRecord{BodySHA: job.BodySHA, SourcePath: job.SourcePath, Summary: facts.Summary, Entities: facts.Entities, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		q.log("[distill] %s: summary %d자, entities %d", job.SourcePath, len([]rune(facts.Summary)), len(facts.Entities))
	}
	indices, err := managedIndices(ctx, q.os, q.cfg)
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
