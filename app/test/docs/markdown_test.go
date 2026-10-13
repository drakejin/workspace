// Package docs 는 사람용 문서(README 에서 링크로 닿는 가이드)가 레포와 코드에 맞는지 보는 테스트다 — 스택 없이 돈다.
// 링크는 GitHub 이 렌더하는 [text](path) 만 본다: 펜스 코드 블록·인라인 코드 안의 것은 링크가 아니다.
package docs

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// repoRoot 는 이 레포 루트다 (app/test/docs 에서 세 단계 위).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

var (
	fence      = regexp.MustCompile("^\\s*(```|~~~)")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	mdLink     = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	headLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// prose 는 펜스 코드 블록 밖의 줄들이다.
func prose(content string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		if fence.MatchString(line) {
			inFence = !inFence
			continue
		}
		if !inFence {
			out = append(out, line)
		}
	}
	return out
}

// links 는 문서의 [text](target) 링크 대상이다 (코드 밖).
func links(content string) []string {
	var out []string
	for _, line := range prose(content) {
		for _, m := range mdLink.FindAllStringSubmatch(inlineCode.ReplaceAllString(line, ""), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// target 은 링크 하나를 레포 기준으로 푼 것이다.
type target struct {
	raw      string
	path     string // 레포 기준 posix 경로 (같은 문서 앵커면 그 문서)
	anchor   string
	external bool
	outside  bool // 레포 밖을 가리킨다
}

// resolve 는 from(레포 기준 경로) 안의 링크 raw 를 푼다. 퍼센트 인코딩은 풀고, / 로 시작하면 레포 루트 기준이다.
func resolve(from, raw string) target {
	tg := target{raw: raw}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "mailto:") {
		tg.external = true
		return tg
	}
	p, anchor, _ := strings.Cut(raw, "#")
	if dp, err := url.PathUnescape(p); err == nil {
		p = dp
	}
	if da, err := url.PathUnescape(anchor); err == nil {
		anchor = da
	}
	tg.anchor = anchor
	switch {
	case p == "":
		tg.path = from
	case strings.HasPrefix(p, "/"):
		tg.path = path.Clean(strings.TrimPrefix(p, "/"))
	default:
		tg.path = path.Clean(path.Join(path.Dir(from), p))
	}
	tg.outside = tg.path == ".." || strings.HasPrefix(tg.path, "../")
	return tg
}

// existsExact 는 rel 이 디스크에 그 철자 그대로(대소문자까지) 있는가다 — GitHub 은 대소문자를 구분한다.
func existsExact(root, rel string) (isDir, ok bool) {
	dir := root
	for _, seg := range strings.Split(rel, "/") {
		if seg == "." {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false, false
		}
		found := false
		for _, e := range entries {
			if e.Name() == seg {
				found = true
				break
			}
		}
		if !found {
			return false, false
		}
		dir = filepath.Join(dir, seg)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false, false
	}
	return info.IsDir(), true
}

// githubSlug 는 GitHub 이 제목에 붙이는 앵커다 (github-slugger): 소문자로, 글자·숫자·결합 문자·'-'·'_'·공백만 남기고, 공백은 '-'.
func githubSlug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// headingText 는 제목 줄이 렌더된 글자다: 인라인 코드·강조 표시를 벗기고 링크는 글자만.
func headingText(raw string) string {
	s := headLink.ReplaceAllString(raw, "$1")
	return strings.NewReplacer("`", "", "**", "", "*", "").Replace(s)
}

// anchors 는 문서 제목들의 GitHub 앵커다. 같은 앵커가 또 나오면 -1, -2 … 를 붙인다.
func anchors(content string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, line := range prose(content) {
		m := atxHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		slug := githubSlug(headingText(m[2]))
		if n := seen[slug]; n > 0 {
			out[slug+"-"+strconv.Itoa(n)] = true
		} else {
			out[slug] = true
		}
		seen[slug]++
	}
	return out
}

func TestGithubSlug(t *testing.T) {
	cases := map[string]string{
		"2. 설치 — memory-init · memory-daemon-start · status": "2-설치--memory-init--memory-daemon-start--status",
		"4.2 Neo4j — knowledge graph":                        "42-neo4j--knowledge-graph",
		"4.7 효과 재기 — `bench` (발표 데모 4단계)":                    "47-효과-재기--bench-발표-데모-4단계",
		"결과 문서 저장 위치":                                        "결과-문서-저장-위치",
		"8. 설정 레퍼런스":                                         "8-설정-레퍼런스",
	}
	for in, want := range cases {
		if got := githubSlug(headingText(in)); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
