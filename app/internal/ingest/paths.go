// Package ingest 는 문서 파일을 OpenSearch 에 적재한다.
package ingest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/document"
)

var docExtensions = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

// IgnoredSegments 는 어디에 있든 색인도 watch 도 하지 않는 디렉토리다 — 의존성·VCS·캐시·작업 사본이다. .worktrees 는 작업 중
// 사본이라 원본과 중복된다. 코드 트리의 빌드 산출물 이름(build·dist·bin·coverage·vendor)은 두지 않는다: 색인 범위(docs/**,
// main/{repo}/docs/**) 안에서는 docs/feature/build/ 처럼 주제 디렉토리 이름일 수 있고, 코드 트리는 InScope 가 내려가지 않는다.
var IgnoredSegments = map[string]bool{
	"node_modules": true, ".git": true, ".worktrees": true, ".cache": true, ".next": true, ".claude": true,
}

// IsIndexable 는 색인 대상인가: 메타레포 루트 문서, docs/ 아래, 관리 대상 repository(main/{repo})의 docs/ 아래 마크다운.
func IsIndexable(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !docExtensions[strings.ToLower(path.Ext(rel))] {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if IgnoredSegments[seg] {
			return false
		}
	}
	return document.RootGuideFiles[rel] || strings.HasPrefix(rel, "docs/") || document.ManagedRepoDocs.MatchString(rel)
}

// InScope 는 스캔·watch 가 내려가도 되는 디렉토리인가. 색인 대상이 있을 수 있는 길만 연다:
// 루트, docs/**, main/, main/{repo}/, main/{repo}/docs/**. 관리 대상 repository 의 코드 트리는 보지 않는다
// (대형 트리를 통째로 watch 하면 macOS 에서 파일 디스크립터가 바닥난다).
func InScope(relDir string) bool {
	rel := filepath.ToSlash(relDir)
	if rel == "" || rel == "." {
		return true
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if IgnoredSegments[p] {
			return false
		}
	}
	switch parts[0] {
	case "docs":
		return true
	case "main":
		return len(parts) <= 2 || parts[2] == "docs"
	default:
		return false
	}
}

// ResolveInsideRoot 는 사용자 입력 경로를 meta-repo 안의 절대/상대 경로로 바꾼다. 밖으로 나가면 실패한다.
func ResolveInsideRoot(root, input string) (abs, rel string, err error) {
	abs = input
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, input)
	}
	abs = filepath.Clean(abs)
	rel, err = filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("meta-repository 밖의 경로는 적재할 수 없습니다: %s", input)
	}
	return abs, filepath.ToSlash(rel), nil
}

// CanonicalRel 은 문서의 정체(source_path)로 쓰는 꼴이다: posix 구분자, 유니코드 NFC. 스캔(디스크 이름, NFD 일 수 있다)과
// ingest_path(사람이 친 NFC)가 같은 파일을 같은 문서로 맞춘다.
func CanonicalRel(rel string) string {
	return document.CanonicalPath(filepath.ToSlash(rel))
}

// ResolveOnDisk 는 사용자 입력 경로를 디스크에 실제로 있는 철자로 바꾼다. macOS(APFS)는 대소문자와 유니코드 정규화를
// 구분하지 않아 철자만 다른 경로가 같은 파일을 연다 — 입력 철자를 그대로 쓰면 같은 파일이 두 문서가 된다.
// abs 는 실제 이름의 경로(IO 용), rel 은 그 이름의 NFC 다 (문서의 정체). 디스크에 없는 세그먼트부터는 입력 철자를 그대로 쓴다
// (읽는 쪽이 실패한다).
func ResolveOnDisk(root, input string) (abs, rel string, err error) {
	return resolveOnDisk(root, input, matchEntry)
}

// resolveOnDisk 는 ResolveOnDisk 이되 디렉토리 항목 찾기(match)를 고를 수 있다 — Indexer 는 나열을 다시 쓰는 것을 꽂는다.
func resolveOnDisk(root, input string, match entryMatcher) (abs, rel string, err error) {
	if _, rel, err = ResolveInsideRoot(root, input); err != nil {
		return "", "", err
	}
	onDisk, _ := onDiskSpelling(root, rel, match)
	return filepath.Join(root, filepath.FromSlash(onDisk)), CanonicalRel(onDisk), nil
}

// LiveUnderOtherSpelling 은 rel 의 파일이 디스크에 다른 철자(대소문자·NFC/NFD)로 살아 있는가다 — rel 로 os.Stat 은 되는데
// 디스크의 철자(NFC)가 rel 과 다르다. 그런 rel 은 옛 철자로 색인된 것일 뿐이라, 그 문서를 지울 때 cold store 청크까지 지우면
// APFS 위의 로컬 cold store 에서는 두 철자의 키가 같은 파일이라 진짜 문서의 청크가 사라진다.
func LiveUnderOtherSpelling(root, rel string) bool {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		return false
	}
	onDisk, found := onDiskSpelling(root, rel, matchEntry)
	return found && CanonicalRel(onDisk) != rel
}

// entryMatcher 는 dir 안에서 name 과 같은 항목의 실제 이름을 찾는다 (matchEntry 또는 listingCache.match).
type entryMatcher func(dir, name string) (string, bool)

// onDiskSpelling 은 rel(posix)의 세그먼트를 디스크의 실제 이름으로 바꾼다. 모든 세그먼트를 찾았으면 found.
func onDiskSpelling(root, rel string, match entryMatcher) (string, bool) {
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

// matchEntry 는 dir 안에서 name 과 같은 항목의 실제 이름이다: 그 철자 그대로 있으면 그것, 없으면 대소문자·유니코드 정규화만
// 다른 것(여럿이면 이름순 첫째). 디렉토리를 나열할 수 없으면(실행 권한만 있는 디렉토리 등) 입력 철자를 그대로 믿는다.
func matchEntry(dir, name string) (string, bool) {
	l, err := readListing(dir, time.Time{})
	if err != nil {
		return name, !errors.Is(err, fs.ErrNotExist)
	}
	return l.match(name)
}

// RealInsideRoot 는 abs 가 심볼릭 링크를 다 풀어도 root 안인지 본다. 링크로 meta-repo 밖의 파일을 읽어
// 색인·cold store 에 옮기지 않게 한다 (ResolveInsideRoot 는 경로 문자열만 본다). 파일이 없으면 통과시킨다 — 읽는 쪽이 실패한다.
func RealInsideRoot(root, abs string) error {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("심볼릭 링크가 meta-repository 밖을 가리킵니다: %s", abs)
	}
	return nil
}
