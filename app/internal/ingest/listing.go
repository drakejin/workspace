package ingest

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
)

// readDir 는 디렉토리 나열이다 (테스트가 나열 횟수를 센다).
var readDir = os.ReadDir

// racyListingAge 는 디렉토리 나열을 다시 써도 될 만큼 디렉토리의 mtime 이 지난 시간이다. 같은 mtime 틱 안에 바뀐 항목은 mtime 으로
// 가릴 수 없으므로(FAT 은 2초 단위) 막 바뀐 디렉토리의 나열은 캐시하지 않는다 — git 의 racy-clean 과 같은 규칙.
const racyListingAge = 2 * time.Second

// dirListing 은 디렉토리 한 곳의 항목 이름이다 (os.ReadDir 순서 = 이름순).
type dirListing struct {
	modTime time.Time
	names   []string
	exact   map[string]bool
}

// readListing 은 dir 을 나열한다. modTime 은 나열하기 전에 잰 디렉토리의 mtime 이다 — 나열과 겹친 변경은 다음 조회에서 mtime 이
// 달라 다시 읽는다 (나열한 뒤에 재면 옛 나열이 새 mtime 으로 고정된다).
func readListing(dir string, modTime time.Time) (dirListing, error) {
	entries, err := readDir(dir)
	if err != nil {
		return dirListing{}, err
	}
	l := dirListing{modTime: modTime, names: make([]string, 0, len(entries)), exact: make(map[string]bool, len(entries))}
	for _, e := range entries {
		l.names = append(l.names, e.Name())
		l.exact[e.Name()] = true
	}
	return l, nil
}

// match 는 name 과 같은 항목의 실제 이름이다: 그 철자 그대로 있으면 그것, 없으면 대소문자·유니코드 정규화만 다른 것(여럿이면 이름순 첫째).
func (l dirListing) match(name string) (string, bool) {
	if l.exact[name] {
		return name, true
	}
	want := norm.NFC.String(name)
	for _, n := range l.names {
		if strings.EqualFold(norm.NFC.String(n), want) {
			return n, true
		}
	}
	return "", false
}

// listingCache 는 디렉토리 나열을 그 디렉토리의 mtime 이 그대로인 동안 다시 쓴다 — 항목이 생기거나 지워지거나 이름이 바뀌면
// 디렉토리의 mtime 이 바뀐다. 철자 맞추기는 경로 조각마다 디렉토리 전체를 읽어, 같은 디렉토리의 파일을 여럿 준비하면(데몬의
// 재작성, 주간 감사, ingest_path) 디렉토리 크기의 제곱으로 느려졌다 — 바뀌지 않은 디렉토리는 조각마다 stat 한 번이다.
type listingCache struct {
	mu   sync.Mutex
	dirs map[string]dirListing
}

func newListingCache() *listingCache { return &listingCache{dirs: map[string]dirListing{}} }

// match 는 matchEntry 와 같되 바뀌지 않은 디렉토리의 나열을 다시 쓴다.
func (c *listingCache) match(dir, name string) (string, bool) {
	l, err := c.listing(dir)
	if err != nil {
		return name, !errors.Is(err, fs.ErrNotExist)
	}
	return l.match(name)
}

func (c *listingCache) listing(dir string) (dirListing, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return dirListing{}, err
	}
	c.mu.Lock()
	cached, ok := c.dirs[dir]
	c.mu.Unlock()
	if ok && cached.modTime.Equal(info.ModTime()) {
		return cached, nil
	}
	l, err := readListing(dir, info.ModTime())
	if err != nil {
		return dirListing{}, err
	}
	if time.Since(info.ModTime()) >= racyListingAge {
		c.mu.Lock()
		c.dirs[dir] = l
		c.mu.Unlock()
	}
	return l, nil
}
