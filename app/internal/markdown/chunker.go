package markdown

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Chunk 는 헤더 계층 단위로 자른 조각이다.
type Chunk struct {
	HeaderPath string
	Text       string
}

// HeaderSeparator 는 header_path 구분자다.
const HeaderSeparator = " > "

// DefaultMaxChars 를 넘는 섹션은 문단 경계에서 더 나눈다. 문단 하나가 넘으면 줄 → 문장 → 글자 경계로 더 나눈다.
const DefaultMaxChars = 4000

var (
	// headerLine 은 ATX 제목 줄이다. 닫는 # 은 앞에 스페이스·탭이 있을 때만 뺀다 (CommonMark) — "C#"·"F#" 의 # 은 제목의 일부다
	headerLine = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)(?:[ \t]+#+)?[ \t]*$`)
	// sentenceEnd 는 문장 끝(마침표·물음표·느낌표 뒤 공백)이다. 긴 줄 하나를 나눌 때 쓴다 — 1번 묶음이 문장 사이의 공백이다
	sentenceEnd = regexp.MustCompile(`[.!?。！？](\s+)`)
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
// 긴 섹션을 나눌 때도 코드 블록 안의 빈 줄에서는 자르지 않는다 (코드 블록 하나가 상한을 넘을 때만 줄 경계에서).
// rootTitle 은 헤더 앞 머리말의 이름이다.
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
			if strings.TrimSpace(part) != "" {
				out = append(out, Chunk{HeaderPath: headerPath, Text: part})
			}
		}
	}
	return out
}

func splitSections(body string) []section {
	var (
		sections []section
		stack    []heading
		current  []string
	)
	flush := func() {
		path := make([]string, 0, len(stack))
		for _, h := range stack {
			path = append(path, h.title)
		}
		sections = append(sections, section{path: path, lines: current})
		current = nil
	}
	for _, l := range markLines(body) {
		if !l.code {
			if m := headerLine.FindStringSubmatch(l.text); m != nil {
				flush()
				level := len(m[1])
				kept := make([]heading, 0, len(stack)+1)
				for _, h := range stack {
					if h.level < level {
						kept = append(kept, h)
					}
				}
				stack = append(kept, heading{level: level, title: m[2]})
			}
		}
		current = append(current, l.text)
	}
	flush()
	return sections
}

// splitLevel 은 긴 글을 나누는 단계 하나다: 나누는 법과, 나눈 조각을 다시 이을 때의 구분자.
type splitLevel struct {
	split func(string) []string
	sep   string
}

// splitLevels 는 긴 섹션을 나누는 순서다: 문단(빈 줄, 코드 블록 안의 빈 줄은 경계가 아니다) → 줄 → 문장.
// 문장 하나도 상한을 넘으면 글자 경계에서 자른다 (runeChunks).
var splitLevels = []splitLevel{
	{paragraphBlocks, "\n\n"},
	{splitLines, "\n"},
	{sentences, ""},
}

// splitLong 은 긴 섹션을 상한(maxChars) 안의 조각으로 나눈다. 문단을 상한까지 이어 붙이고, 문단 하나가 상한을 넘으면
// 그 문단만 줄 경계에서(그래도 크면 문장, 글자 경계에서) 나눈다 — 상한을 넘는 코드 블록도 줄 경계에서 나뉜다.
func splitLong(text string, maxChars int) []string {
	return splitAt(text, maxChars, 0)
}

func splitAt(text string, maxChars, level int) []string {
	if runeLen(text) <= maxChars {
		return []string{text}
	}
	if level == len(splitLevels) {
		return runeChunks(text, maxChars)
	}
	lv := splitLevels[level]
	var parts []string
	buffer := ""
	for _, piece := range lv.split(text) {
		if runeLen(piece) > maxChars {
			// 조각 하나가 상한을 넘는다 — 모아 둔 것을 내보내고 그 조각만 다음 단계로 나눈다
			if buffer != "" {
				parts = append(parts, buffer)
			}
			buffer = ""
			parts = append(parts, splitAt(piece, maxChars, level+1)...)
			continue
		}
		candidate := piece
		if buffer != "" {
			candidate = buffer + lv.sep + piece
		}
		if runeLen(candidate) > maxChars && buffer != "" {
			parts = append(parts, buffer)
			buffer = piece
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
	)
	for _, l := range markLines(text) {
		if !l.code && strings.TrimSpace(l.text) == "" {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = nil
			continue
		}
		current = append(current, l.text)
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

func splitLines(text string) []string { return strings.Split(text, "\n") }

// sentences 는 줄 하나를 문장 경계에서 자른다. 조각은 원문 그대로다 — 문장 사이의 공백(스페이스·탭 묶음)은 뒤 문장의 앞에 붙어
// 남아, 조각을 구분자 없이 이으면 원래 줄이다 (다듬어 공백 하나로 다시 이으면 공백 묶음이 사라진다).
func sentences(text string) []string {
	var out []string
	start := 0
	for _, loc := range sentenceEnd.FindAllStringSubmatchIndex(text, -1) {
		out = append(out, text[start:loc[2]])
		start = loc[2]
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// runeChunks 는 글자 경계에서 maxChars 글자씩 자른다 — 문장 경계도 없는 긴 글(공백 없는 토큰·base64 등)의 마지막 수단이다.
func runeChunks(text string, maxChars int) []string {
	r := []rune(text)
	parts := make([]string, 0, len(r)/maxChars+1)
	for start := 0; start < len(r); start += maxChars {
		parts = append(parts, string(r[start:min(start+maxChars, len(r))]))
	}
	return parts
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
