package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// docLockWait 는 쥔 락을 기다려 보는 시간이다.
const docLockWait = 50 * time.Millisecond

// IDX-3: 문서 쓰기 락은 같은 문서의 두 writer 를 줄 세운다 — 한쪽이 쥐고 있으면 다른 쪽은 기다리고(ctx 가 끝나면 그 오류),
// 풀면 잡는다. 다른 문서는 기다리지 않는다 (같은 락 파일을 나눠 쥐는 문서가 아니면).
func TestLockDocSerializesWritersOfTheSameDocument(t *testing.T) {
	root := t.TempDir()
	const doc = "docs/knowledge/a.md"
	unlock, err := LockDoc(context.Background(), root, doc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), docLockWait)
	defer cancel()
	if _, err := LockDoc(ctx, root, doc); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("쥔 락을 기다리지 않았다: %v", err)
	}
	other := "docs/knowledge/b.md"
	for docLockFile(root, other) == docLockFile(root, doc) {
		other += "x"
	}
	release, err := LockDoc(context.Background(), root, other)
	if err != nil {
		t.Fatalf("다른 문서는 기다리지 않는다: %v", err)
	}
	release()
	unlock()
	again, err := LockDoc(context.Background(), root, doc)
	if err != nil {
		t.Fatalf("푼 락을 다시 잡지 못했다: %v", err)
	}
	again()
	if _, err := LockDoc(context.Background(), "", doc); err == nil {
		t.Error("루트가 비면 락을 만들 곳이 없다")
	}
}
