package markdown

import (
	"regexp"
	"strings"
)

// Chunk 는 헤더 계층 단위로 자른 조각이다.
type Chunk struct {
	HeaderPath string
	Text       string
}

// HeaderSeparator 는 header_path 구분자다.
const HeaderSeparator = " > "

// DefaultMaxChars 를 넘는 섹션은 문단 경계에서 더 나눈다.
const DefaultMaxChars = 4000

var (
	headerLine = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	fenceOpen  = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
)

type heading struct {
	level int
	title string
}

type section struct {
	path  []string
	lines []string
}

// ChunkMarkdown 은 마크다운을 헤더 계층 단위로 자른다. 코드 블록 안의 `#` 줄은 헤더가 아니고,
// 긴 섹션을 나눌 때도 코드 블록 안에서는 자르지 않는다. rootTitle 은 헤더 앞 머리말의 이름이다.
func ChunkMarkdown(body, rootTitle string, maxChars int) []Chunk {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	var out []Chunk
	for _, s := range splitSections(body) {
		text := strings.TrimSpace(strings.Join(s.lines, "\n"))
		if text == "" {
			continue
		}
		path := s.path
		if len(path) == 0 {
			path = []string{rootTitle}
		}
		headerPath := strings.Join(path, HeaderSeparator)
		for _, part := range splitLong(text, maxChars) {
			out = append(out, Chunk{HeaderPath: headerPath, Text: part})
		}
	}
	return out
}

func splitSections(body string) []section {
	var (
		sections []section
		stack    []heading
		current  []string
		fence    string
	)
	flush := func() {
		path := make([]string, 0, len(stack))
		for _, h := range stack {
			path = append(path, h.title)
		}
		sections = append(sections, section{path: path, lines: current})
		current = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			marker := m[1]
			switch {
			case fence == "":
				fence = marker
			case marker[0] == fence[0] && len(marker) >= len(fence):
				fence = ""
			}
			current = append(current, line)
			continue
		}
		if fence == "" {
			if m := headerLine.FindStringSubmatch(line); m != nil {
				flush()
				level := len(m[1])
				kept := make([]heading, 0, len(stack)+1)
				for _, h := range stack {
					if h.level < level {
						kept = append(kept, h)
					}
				}
				stack = append(kept, heading{level: level, title: m[2]})
				current = append(current, line)
				continue
			}
		}
		current = append(current, line)
	}
	flush()
	return sections
}

// splitLong 은 긴 섹션을 문단(빈 줄) 경계에서 나눈다. 코드 블록 안의 빈 줄은 경계가 아니다.
func splitLong(text string, maxChars int) []string {
	if len([]rune(text)) <= maxChars {
		return []string{text}
	}
	var parts []string
	buffer := ""
	for _, block := range paragraphBlocks(text) {
		candidate := block
		if buffer != "" {
			candidate = buffer + "\n\n" + block
		}
		if len([]rune(candidate)) > maxChars && buffer != "" {
			parts = append(parts, buffer)
			buffer = block
			continue
		}
		buffer = candidate
	}
	if buffer != "" {
		parts = append(parts, buffer)
	}
	return parts
}

func paragraphBlocks(text string) []string {
	var (
		blocks  []string
		current []string
		inFence bool
	)
	for _, line := range strings.Split(text, "\n") {
		if fenceOpen.MatchString(line) {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = nil
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}
