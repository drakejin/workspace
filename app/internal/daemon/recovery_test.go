package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/state"
)

const (
	docA     = "docs/specs/pay/a.md"
	contentA = "---\ndate: 2026-10-01\n---\n# 결제\nRecoveryFee 는 3500원이다.\n"
	editedA  = "---\ndate: 2026-10-01\n---\n# 결제\nRecoveryFee 는 4000원이다.\n"
)

var errSearchDown = errors.New("search down")

func newRecoveryJudge(t *testing.T, root string, fos *routedOS) *Judge {
	t.Helper()
	return &Judge{OS: fos, Config: recoveryConfig(root), State: state.NewJudgeState(root), Ask: &stubAsker{}, Log: t.Logf}
}

// IDX-11·IDX-4: 판정이 오류로 실패한 문서는 다음 Run 이 다시 판정한다. 그 사이 같은 문서가 또 바뀌면 최신 본문 하나만 남긴다.
func TestJudgeRetriesAFailedJudgmentOnTheNextRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first := writeDoc(t, root, docA, contentA)
	fos := &routedOS{indices: []string{recoveryIndex}}
	fos.setFail("-hot/_search", errSearchDown)
	j := newRecoveryJudge(t, root, fos)

	if _, err := j.Run(ctx, []DiffEntry{first}); err == nil {
		t.Fatal("이웃 검색이 실패하면 오류다")
	}
	edited := writeDoc(t, root, docA, editedA)
	if _, err := j.Run(ctx, []DiffEntry{edited}); err == nil {
		t.Fatal("이웃 검색이 실패하면 오류다")
	}
	if len(j.retry) != 1 || j.retry[0].BodySHA != edited.BodySHA {
		t.Fatalf("retry = %+v (문서마다 최신 본문 하나)", j.retry)
	}

	fos.setFail("-hot/_search", nil)
	if _, err := j.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !fos.sent("-hot/_search", "4000원") || len(j.retry) != 0 {
		t.Errorf("다음 Run 이 최신 본문으로 다시 판정해야 한다: retry=%+v", j.retry)
	}
}

// IDX-4: 판정 대기분은 디스크(.cache/daemon)에 남는다 — 종료(ctx 취소)로 판정하지 못한 문서를 재시작한 boot 가 이어 판정한다.
// 그 사이 본문이 바뀐 문서는 버린다 (바뀐 본문은 사이클이 새 변경분으로 넘긴다).
func TestJudgePendingSurvivesARestart(t *testing.T) {
	root := t.TempDir()
	entry := writeDoc(t, root, docA, contentA)
	other := writeDoc(t, root, "docs/specs/pay/b.md", "# 다른 문서\nRecoveryOther 본문\n")
	fos := &routedOS{indices: []string{recoveryIndex}}
	stopped, cancel := context.WithCancel(context.Background())
	cancel() // 종료 신호 — 진행 중 호출이 끊긴다
	if _, err := newRecoveryJudge(t, root, fos).Run(stopped, []DiffEntry{entry, other}); err == nil {
		t.Fatal("끊긴 판정은 오류다")
	}
	writeDoc(t, root, "docs/specs/pay/b.md", "# 다른 문서\n고친 본문\n")

	restarted := newRecoveryJudge(t, root, fos)
	if err := restarted.Restore(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.retry) != 1 || restarted.retry[0].SourcePath != docA || restarted.retry[0].Body != entry.Body {
		t.Fatalf("되살린 대기분 = %+v (본문이 그대로인 a.md 하나)", restarted.retry)
	}
	if _, err := restarted.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !fos.sent("-hot/_search", "3500원") {
		t.Error("재시작한 데몬이 대기분을 판정해야 한다")
	}
	if pending, err := state.ReadJudgePending(root); err != nil || len(pending) != 0 {
		t.Errorf("판정을 마쳤으면 대기 목록이 빈다: %v %v", pending, err)
	}
}

// IDX-11·IDX-8: 증류 실패(LLM 응답 없음·스키마 불일치·빈 요약)는 부팅 백필만 기다리지 않고 뒤 사이클에 다시 시도한다 —
// 바로 다음 사이클이 아니라 백오프가 지난 뒤에.
func TestDistillRetriesAFailedJobOnALaterCycle(t *testing.T) {
	failures := map[string]stubAsker{
		"LLM 응답 없음": {ok: false},
		"스키마 불일치":   {raw: `{"summary":1}`, ok: true},
		"빈 요약":      {raw: `{"summary":"  ","entities":[]}`, ok: true},
	}
	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			entry := writeDoc(t, root, docA, contentA)
			fos := &routedOS{indices: []string{recoveryIndex}}
			clock := &fakeClock{t: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
			ask := &stubAsker{raw: fail.raw, ok: fail.ok}
			cache := state.NewDistillCache(root)
			logs := &logSink{}
			q := NewDistillQueue(ctx, fos, recoveryConfig(root), cache, ask, logs.Logf)
			q.now = clock.Now

			q.EnqueueDiff([]DiffEntry{entry})
			q.Settle()
			q.EnqueueDiff(nil) // 바로 다음 사이클 — 아직 기다린다
			q.Settle()
			if ask.calls != 1 {
				t.Fatalf("calls = %d (실패 직후에는 다시 부르지 않는다)\n%s", ask.calls, logs)
			}

			*ask = stubAsker{raw: `{"summary":"결제 요약","entities":["concept:결제"]}`, ok: true, calls: ask.calls}
			clock.Advance(distillRetryBase)
			q.EnqueueDiff(nil)
			q.Settle()
			if ask.calls != 2 {
				t.Fatalf("calls = %d (백오프가 지나면 다시 시도한다)\n%s", ask.calls, logs)
			}
			if _, ok := cache.Get("t", entry.BodySHA); !ok || !fos.sent("/_update_by_query", "결제 요약") {
				t.Errorf("다시 시도한 증류가 캐시와 색인에 반영돼야 한다\n%s", logs)
			}
			q.EnqueueDiff(nil)
			q.Settle()
			if ask.calls != 2 {
				t.Errorf("성공한 문서는 다시 하지 않는다: calls=%d", ask.calls)
			}
		})
	}
}

// IDX-8: 다시 시도를 기다리던 문서가 그 사이 바뀌면 옛 본문은 버리고 새 본문만 증류한다.
func TestDistillDropsAStaleRetryWhenTheDocumentChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first := writeDoc(t, root, docA, contentA)
	fos := &routedOS{indices: []string{recoveryIndex}}
	clock := &fakeClock{t: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	ask := &stubAsker{ok: false}
	q := NewDistillQueue(ctx, fos, recoveryConfig(root), state.NewDistillCache(root), ask, t.Logf)
	q.now = clock.Now
	q.EnqueueDiff([]DiffEntry{first})
	q.Settle()

	edited := writeDoc(t, root, docA, editedA)
	*ask = stubAsker{raw: `{"summary":"새 요약","entities":[]}`, ok: true, calls: ask.calls}
	q.EnqueueDiff([]DiffEntry{edited})
	q.Settle()
	clock.Advance(distillRetryMax)
	q.EnqueueDiff(nil)
	q.Settle()
	if ask.calls != 2 {
		t.Errorf("calls = %d (실패 1 + 새 본문 1 — 옛 본문은 다시 하지 않는다)", ask.calls)
	}
}

// IDX-8: 부팅 백필은 색인이 기억하는 본문(body_sha)의 증류만 만든다 — 그 사이 디스크의 본문이 바뀌었으면 건너뛴다
// (새 본문은 사이클이 다시 색인하고 증류한다). 요약이 있는 문서는 건너뛴다.
// R1-paths-5: 파일은 디스크의 철자로 연다 — source_path(NFC)와 디스크 이름(NFD)이 다른 문서도 백필한다.
func TestMissingDistillUsesOnlyTheBodyOfTheIndexedSHA(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, docA, contentA)
	staleHead := indexedHead(t, root, docA, nil)
	writeDoc(t, root, docA, editedA) // 색인 뒤에 고쳤다
	same := writeDoc(t, root, "docs/specs/pay/b.md", "# 그대로\n본문 그대로\n")
	writeDoc(t, root, "docs/specs/pay/c.md", "# 요약 있음\n본문\n")
	nfd := writeDoc(t, root, "docs/specs/\u1100\u1161\u11a8/d.md", "# 한글 디렉토리\n본문\n") // "각" 의 NFD
	nfdHead := indexedHead(t, root, "docs/specs/\u1100\u1161\u11a8/d.md", nil)
	nfdHead["_source"].(map[string]any)["source_path"] = nfd.SourcePath
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{
		staleHead,
		indexedHead(t, root, "docs/specs/pay/b.md", nil),
		indexedHead(t, root, "docs/specs/pay/c.md", map[string]any{"summary": "있다"}),
		nfdHead,
	}}
	d := &Daemon{os: fos, cfg: recoveryConfig(root)}

	jobs, err := d.missingDistill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DistillJob{}
	for _, j := range jobs {
		got[j.SourcePath] = j
	}
	for _, want := range []DiffEntry{same, nfd} {
		if j, ok := got[want.SourcePath]; !ok || j.Body != want.Body || j.BodySHA != want.BodySHA {
			t.Errorf("%s 백필 = %+v", want.SourcePath, j)
		}
	}
	if len(jobs) != 2 {
		t.Errorf("jobs = %+v (본문이 sha 와 맞는 b.md 와 NFD 이름의 d.md)", jobs)
	}
}

// IDX-9·IDX-11: reconcile(수렴)이 실패해도 사이클은 판정(지난 실패분 재시도)·그래프·주간 감사를 이어 간다 — 수렴 실패 하나가
// 변경 없는 사이클의 재시도와 감사를 막지 않게.
func TestCycleContinuesAfterAConvergeError(t *testing.T) {
	root := t.TempDir()
	entry := writeDoc(t, root, docA, contentA)
	// 색인에는 지금 내용 그대로 있다(변경분 없음) — 다만 있어서는 안 될 superseded_by 가 남아 수렴이 고치려 한다
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{
		indexedHead(t, root, docA, map[string]any{"superseded_by": "docs/specs/pay/gone.md"}),
	}}
	fos.setFail("/_update_by_query", errors.New("update refused"))
	logs := &logSink{}
	g := &replaceCounter{}
	d, err := New(context.Background(), Deps{Config: recoveryConfig(root), OS: fos, Ask: &stubAsker{}, Cold: cold.NewLocalStore(t.TempDir()),
		Graph: g, Log: logs.Logf, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	d.judge.retry = []DiffEntry{entry} // 지난 사이클에 판정하지 못한 문서

	d.cycle("test")

	if !logs.has("[reconcile] failed") {
		t.Fatalf("수렴 실패가 로그에 남아야 한다\n%s", logs)
	}
	if !fos.sent("-hot/_search", "3500원") {
		t.Error("판정 재시도가 돌아야 한다")
	}
	if g.count() != 1 {
		t.Errorf("그래프 단계가 돌아야 한다: replace=%d", g.count())
	}
	if !logs.has("[audit] 주간 감사") {
		t.Errorf("주간 감사가 돌아야 한다\n%s", logs)
	}
}

// RUN-15: frontmatter YAML 이 깨진 문서를 색인할 때 데몬이 경고를 남긴다 — 메타데이터 없이 원문 전체가 본문으로 들어간다.
func TestReconcileWarnsAboutBrokenFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/specs/bad-yaml.md", "---\nteam: [unclosed\ntopic: broken\n---\n# Bad YAML\n본문\n")
	logs := &logSink{}
	d, err := New(context.Background(), Deps{Config: recoveryConfig(root), OS: &routedOS{}, Ask: &stubAsker{}, Cold: cold.NewLocalStore(t.TempDir()),
		Log: logs.Logf, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.rec.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !logs.has("docs/specs/bad-yaml.md") || !logs.has("frontmatter YAML 이 깨졌습니다") {
		t.Errorf("깨진 frontmatter 경고가 없다\n%s", logs)
	}
}

// R1-paths-5: 판정이 이웃 본문을 읽을 때 디스크의 철자로 연다 — source_path(NFC)와 디스크 이름(NFD)이 다른 파일도 읽는다
// (정규화를 구분하는 파일 시스템에서는 NFC 경로로 열면 없다). 색인의 sha 와 다른 본문은 쓰지 않고 색인 text 로 돌아간다.
func TestReadBodyUsesTheOnDiskSpellingAndTheIndexedSHA(t *testing.T) {
	root := t.TempDir()
	nfd := "docs/specs/각/a.md" // "각" 의 NFD
	entry := writeDoc(t, root, nfd, contentA)
	if entry.SourcePath == nfd {
		t.Fatal("source_path 는 NFC 여야 한다")
	}
	if got := readBody(root, entry.SourcePath, entry.BodySHA, "fallback"); got != entry.Body {
		t.Errorf("readBody = %q, want 디스크 본문", got)
	}
	if got := readBody(root, entry.SourcePath, "other-sha", "fallback"); got != "fallback" {
		t.Errorf("sha 가 다르면 색인 text: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".cache")); err == nil {
		t.Error("읽기만 한다")
	}
}
