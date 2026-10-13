package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// blankBodies 는 청크로 만들 본문이 없는 문서의 본문이다 (빈 파일·frontmatter 만·공백만). 색인에는 빈 청크 0 하나만 있다.
var blankBodies = []string{"", "\n\n", " \t\n"}

// hitsOS 는 _search 마다 같은 hits(JSON 배열)를 돌려주는 OpenSearch 대신이다.
type hitsOS struct{ hits string }

func (h hitsOS) Do(_ context.Context, _, _ string, _ any, _ url.Values, out any) error {
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"hits":{"hits":`+h.hits+`}}`), out)
}

func (hitsOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	return opensearch.BulkResponse{}, nil
}

// RUN-6: 본문이 없는 문서는 주장이 없다 — 판정하지 않는다 (이웃을 찾지도 않는다).
func TestJudgeSkipsBlankDocuments(t *testing.T) {
	os := &captureOS{}
	cfg := config.Config{Workspace: "ws", Judge: config.Judge{Enabled: true, WindowDays: 7, MinScore: 0.3}}
	j := &judge{OS: os, Config: cfg, State: state.NewJudgeState(t.TempDir()), Log: t.Logf}
	for _, body := range blankBodies {
		d := diffEntry{SourcePath: "docs/a.md", Team: "t", Kind: model.KindEpisodic, Change: changeNew, BodySHA: "x", Body: body}
		if sum, err := j.Run(context.Background(), []diffEntry{d}); err != nil || sum.Candidates != 0 {
			t.Errorf("Run(%q) = %+v %v", body, sum, err)
		}
	}
	if os.body != "" {
		t.Errorf("빈 문서로 이웃을 찾았다: %s", os.body)
	}
}

// RUN-6: 빈 청크 0(본문 없는 문서)은 판정 이웃이 아니다 — 제목(파일 이름)만 비슷한 빈 문서가 오래된 문서를 latest-wins 로
// 숨기거나, 문서가 빈 문서에 밀리지 않게.
func TestBestNeighborSkipsBlankDocuments(t *testing.T) {
	self := `{"_score":2.0,"_source":{"source_path":"docs/a.md","body_sha":"a","text":"배송비는 3000원이다"}}`
	d := diffEntry{SourcePath: "docs/a.md", Team: "t", Kind: model.KindEpisodic, Body: "배송비는 3000원이다"}
	blank := hitsOS{hits: `[` + self + `,{"_score":1.9,"_source":{"source_path":"docs/b.md","body_sha":"b","text":""}}]`}
	if n, err := bestNeighbor(context.Background(), blank, d, "ws", 0.3); err != nil || n != nil {
		t.Errorf("빈 문서가 이웃이다: %+v %v", n, err)
	}
	full := hitsOS{hits: `[` + self + `,{"_score":1.9,"_source":{"source_path":"docs/b.md","body_sha":"b","text":"배송비는 3500원이다"}}]`}
	if n, err := bestNeighbor(context.Background(), full, d, "ws", 0.3); err != nil || n == nil || n.SourcePath != "docs/b.md" {
		t.Errorf("본문이 있는 문서는 이웃이다: %+v %v", n, err)
	}
}

// RUN-6: 본문이 없는 문서는 증류하지 않는다 — 요약할 것이 없어 LLM 호출만 낭비하고, 빈 요약이면 매번 다시 돈다.
func TestDistillSkipsBlankDocuments(t *testing.T) {
	ask := &stubAsker{raw: `{"summary":"요약","entities":[]}`, ok: true}
	cfg := config.Config{Workspace: "ws", Distill: config.Distill{Enabled: true, Concurrency: 1}}
	q := newDistillQueue(context.Background(), &captureOS{}, cfg, state.NewDistillCache(t.TempDir()), ask, t.Logf)
	var diff []diffEntry
	for i, body := range blankBodies {
		diff = append(diff, diffEntry{Team: "t", SourcePath: "docs/a.md", Change: changeNew, BodySHA: string(rune('a' + i)), Body: body})
	}
	q.EnqueueDiff(diff)
	q.Settle()
	if ask.calls != 0 {
		t.Errorf("빈 본문에 LLM 을 %d번 불렀다", ask.calls)
	}
}

// R4-daemon-a-2: 본문이 없는 문서를 text 필드 없이 쓰기 전의 판(5)은 빈 청크 0 에 "text": "" 를 썼다 — exists 는 빈 문자열도 있다고
// 보아 그 문서가 목록·검색에 빈 결과로 나온다. 데몬은 그 판의 문서를 내용이 같아도 한 번 다시 써 빈 text 를 없앤다.
func TestReconcileRewritesBlankDocsWrittenWithAnEmptyText(t *testing.T) {
	const rel = "docs/specs/pay/blank.md"
	const emptyTextVersion = 5 // 빈 청크 0 에 "text": "" 를 쓰던 마지막 판
	root := t.TempDir()
	writeDoc(t, root, rel, "---\ndate: 2026-10-01\n---\n")
	fos := &routedOS{indices: []string{recoveryIndex}, docs: []map[string]any{
		indexedHead(t, root, rel, map[string]any{"text": "", "index_version": emptyTextVersion}),
	}}
	d, logs := newRecoveryDaemon(t, recoveryConfig(root), fos)

	sum, diff, err := d.rec.Once(context.Background())

	if err != nil || sum.Indexed != 1 || !fos.sent("/_bulk", "") {
		t.Errorf("빈 text 로 쓴 판은 다시 쓴다: %+v %v\n%s", sum, err, logs)
	}
	if len(diff) != 0 {
		t.Errorf("내용이 같으니 판정·증류로 넘기지 않는다: %+v", diff)
	}
}
