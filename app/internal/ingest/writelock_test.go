package ingest

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// opLog 는 여러 writer 의 쓰기 순서다.
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

// orderOS 는 writer 하나(프로세스 하나)의 OpenSearch 대신이다 — _bulk·_delete_by_query 를 이름표와 함께 opLog 에 적는다.
// hold 가 있으면 첫 _bulk 에서 entered 를 닫고 hold 가 닫힐 때까지 기다린다 (Write 한가운데서 멈춘 writer).
type orderOS struct {
	name    string
	log     *opLog
	hold    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (o *orderOS) Do(_ context.Context, _, path string, _ any, _ url.Values, out any) error {
	if strings.HasSuffix(path, "/_delete_by_query") {
		o.log.add(o.name + ":delete")
	}
	return fakeOS{resp: map[string]any{}}.Do(context.Background(), "", path, nil, nil, out)
}

func (o *orderOS) Bulk(context.Context, []any, url.Values) (opensearch.BulkResponse, error) {
	o.once.Do(func() {
		if o.hold != nil {
			close(o.entered)
			<-o.hold
		}
	})
	o.log.add(o.name + ":bulk")
	return opensearch.BulkResponse{}, nil
}

// IDX-3: 데몬 밖의 writer(ingest_path·audit -apply)와 데몬이 같은 문서를 엇갈려 쓰지 않는다 — Write 는 여러 단계라, 엇갈리면
// 청크 0 은 새 내용(최신 stat)이고 나머지 청크는 옛 내용인 섞인 문서가 최신으로 고정된다. 두 writer 는 서로 다른 프로세스처럼
// 각자의 Indexer 를 쓴다. 먼저 들어간 Write 가 끝나기 전에는 다른 writer 가 그 문서에 한 줄도 쓰지 않는다.
func TestWritesOfTheSameDocumentDoNotInterleave(t *testing.T) {
	const rel = "docs/knowledge/race/doc.md"
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, rel, []byte("# 경합\n## 하나\nRaceOne\n## 둘\nRaceTwo\n"))
	log := &opLog{}
	first := &orderOS{name: "daemon", log: log, hold: make(chan struct{}), entered: make(chan struct{})}
	second := &orderOS{name: "ingest_path", log: log}
	ixFirst := New(unitDeps(root, first, cold.NewLocalStore(t.TempDir())))
	ixSecond := New(unitDeps(root, second, cold.NewLocalStore(t.TempDir())))
	p1, err := ixFirst.Prepare(ctx, rel, "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ixSecond.Prepare(ctx, rel, "")
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	go func() { _, err := ixFirst.Write(ctx, p1); errs <- err }()
	<-first.entered
	go func() { _, err := ixSecond.Write(ctx, p2); errs <- err }()
	time.Sleep(100 * time.Millisecond) // 두 번째 writer 가 끼어들 틈
	close(first.hold)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	seenSecond := false
	for _, op := range log.ops {
		if strings.HasPrefix(op, "ingest_path:") {
			seenSecond = true
		} else if seenSecond {
			t.Fatalf("두 writer 가 엇갈렸다: %v", log.ops)
		}
	}
	if !seenSecond {
		t.Fatalf("두 번째 writer 가 쓰지 않았다: %v", log.ops)
	}
}

// IDX-7: RemoveFile 은 색인에 있는 source_path 를 그대로 지운다 — 색인 규칙이 바뀌어(또는 옛 cold 청크를 reload 해) 범위 밖이 된
// 경로도. 예전에는 범위 밖 경로를 0건 지우고 돌아와, tombstone 이 매 사이클 removed 로 세는데도 문서는 계속 검색됐다.
func TestRemoveFileRemovesAnOutOfScopePath(t *testing.T) {
	const legacy = "scripts/legacy.md"
	ctx := context.Background()
	root := t.TempDir()
	store := cold.NewLocalStore(t.TempDir())
	if err := store.Put(ctx, cold.ChunksKey(unitTeam, legacy), "{}\n"); err != nil {
		t.Fatal(err)
	}
	log := &opLog{}
	ix := New(unitDeps(root, &orderOS{name: "audit", log: log}, store))
	if _, err := ix.RemoveFile(ctx, legacy, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(log.ops, ",") != "audit:delete" {
		t.Errorf("범위 밖 경로를 색인에서 지우지 않았다: %v", log.ops)
	}
}
