package mcpserver

import (
	"path/filepath"
	"strings"
)

// inMetaRepo 는 related_docs 에 준 경로를 root 철자의 meta-repo 안 경로로 맞춘다 — 상대 경로는 root 기준이다. 절대 경로는 적힌
// 철자로 root 안인지 먼저 보고, 아니면 심볼릭 링크를 푼 철자로 본다: META_REPO_ROOT 가 링크 경로(macOS 의 /var/…)면 푼 철자
// (/private/var/…)로 준 경로도 같은 곳이다. 밖이면 ok 가 아니다.
func inMetaRepo(root, input string) (string, bool) {
	if !filepath.IsAbs(input) {
		input = filepath.Join(root, input)
	}
	abs := filepath.Clean(input)
	if rel, ok := relWithin(root, abs); ok {
		return filepath.Join(root, rel), true
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	for _, p := range []string{abs, realPath(abs)} {
		if rel, ok := relWithin(realRoot, p); ok {
			return filepath.Join(root, rel), true
		}
	}
	return "", false
}

// realPath 는 링크를 다 푼 경로다. 풀 수 없으면(없는 파일) p 그대로.
func realPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// relWithin 은 p 가 base 안(base 자신 포함)이면 base 기준 상대 경로다.
func relWithin(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
