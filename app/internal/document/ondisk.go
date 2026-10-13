package document

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 문서의 정체(source_path)는 디스크 이름을 NFC 로 맞춘 경로라, 정규화·대소문자를 구분하는 파일 시스템(Linux)에서는 NFD 이름
// (Finder 가 만든 한글 이름)의 파일을 source_path 로 열 수 없고 git 도 그 철자를 모른다. 파일을 열거나 git 에 넘길 때는
// 디스크의 철자로 푼다 — ingest(ResolveOnDisk)·cold(tier) 가 같은 규칙을 쓴다.

// EntryMatcher 는 dir 안에서 name 과 같은 항목의 실제 이름을 찾는다 (MatchEntry, 또는 나열을 다시 쓰는 것).
type EntryMatcher func(dir, name string) (string, bool)

// OnDiskSpelling 은 rel(posix)의 세그먼트를 디스크의 실제 이름으로 바꾼다. 모든 세그먼트를 찾았으면 found. 디스크에 없는
// 세그먼트부터는 입력 철자를 그대로 쓴다 (여는 쪽이 실패한다).
func OnDiskSpelling(root, rel string, match EntryMatcher) (string, bool) {
	if rel == "" || rel == "." {
		return rel, true
	}
	segs := strings.Split(rel, "/")
	names := make([]string, 0, len(segs))
	dir := root
	for i, seg := range segs {
		name, ok := match(dir, seg)
		if !ok {
			return path.Join(path.Join(names...), path.Join(segs[i:]...)), false
		}
		names = append(names, name)
		dir = filepath.Join(dir, name)
	}
	return path.Join(names...), true
}

// OnDiskRel 은 root 아래 문서 rel(source_path)의 디스크 철자(posix 상대 경로)다 — 파일을 열거나 git 에 넘길 때 쓴다.
func OnDiskRel(root, rel string) string {
	onDisk, _ := OnDiskSpelling(root, rel, MatchEntry)
	return onDisk
}

// MatchEntry 는 dir 안에서 name 과 같은 항목의 실제 이름이다 (MatchName). 디렉토리를 나열할 수 없으면(실행 권한만 있는
// 디렉토리 등) 입력 철자를 그대로 믿는다.
func MatchEntry(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name, !errors.Is(err, fs.ErrNotExist)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return MatchName(names, name)
}

// MatchName 은 names(디렉토리 항목 이름, 이름순) 중 name 과 같은 항목이다: 그 철자 그대로 있으면 그것, 없으면 대소문자·유니코드
// 정규화만 다른 것(여럿이면 앞의 것) — macOS(APFS)가 같은 파일로 여는 철자다.
func MatchName(names []string, name string) (string, bool) {
	if slices.Contains(names, name) {
		return name, true
	}
	want := norm.NFC.String(name)
	for _, n := range names {
		if strings.EqualFold(norm.NFC.String(n), want) {
			return n, true
		}
	}
	return "", false
}
