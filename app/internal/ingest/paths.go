// Package ingest 는 문서 파일을 OpenSearch 에 적재한다.
package ingest

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/drakejin/workspace/app/internal/document"
)

var docExtensions = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

// IgnoredSegments 는 색인도 watch 도 하지 않는 디렉토리다. .worktrees 는 작업 중 사본이라 원본과 중복된다.
var IgnoredSegments = map[string]bool{
	"node_modules": true, ".git": true, ".worktrees": true, ".cache": true,
	"dist": true, "build": true, "bin": true, ".next": true, "coverage": true, ".claude": true, "vendor": true,
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
