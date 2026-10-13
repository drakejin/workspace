package cold

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDirName 은 보관소 루트 아래 잠금 파일 디렉토리다. 청크·목록 키와 섞이지 않게 점으로 시작한다.
const lockDirName = ".locks"

// keyLocks 는 키별 잠금 파일(flock)이다. 목록(manifest)처럼 읽고-고치고-쓰는 키를 tier(launchd tier-tick)·archive 가
// 다른 프로세스로 동시에 고쳐도 한쪽 줄이 사라지지 않게 한다. dir 이 비면 잠그지 않는다.
type keyLocks struct{ dir string }

// lock 은 key 의 잠금을 잡고 푸는 함수를 돌려준다. 같은 프로세스 안에서도 열 때마다 따로 잡히므로 goroutine 사이도 막는다.
func (l keyLocks) lock(key string) (func(), error) {
	if l.dir == "" {
		return func() {}, nil
	}
	clean, err := safeKey(key)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(l.dir, filepath.FromSlash(clean)+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", key, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// locker 는 키 잠금을 아는 보관소다.
type locker interface {
	lockKey(key string) (func(), error)
}

// lockKey 는 보관소가 잠금을 알면 key 를 잠근다.
func lockKey(store Store, key string) (func(), error) {
	if l, ok := store.(locker); ok {
		return l.lockKey(key)
	}
	return func() {}, nil
}

// updateKey 는 key 를 잠근 채 읽고, edit 이 돌려준 내용을 쓴다. edit 이 false 를 돌려주면 쓰지 않는다.
func updateKey(ctx context.Context, store Store, key string, edit func(body string, ok bool) (string, bool)) error {
	unlock, err := lockKey(store, key)
	if err != nil {
		return err
	}
	defer unlock()
	body, ok, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	next, write := edit(body, ok)
	if !write {
		return nil
	}
	return store.Put(ctx, key, next)
}
