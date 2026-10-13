package markdown

import (
	"regexp"
	"strings"
)

// fenceMarker 는 코드 펜스 후보 줄이다: 들여쓰기 3칸까지, ` 또는 ~ 3개 이상, 그 뒤(info string).
var fenceMarker = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// codeFence 는 열린 코드 펜스다. char 가 0 이면 펜스 밖이다.
type codeFence struct {
	char byte // '`' 또는 '~'
	size int  // 여는 줄의 마커 길이
}

// advanceFence 는 줄 하나를 지난 뒤의 펜스 상태와, 그 줄이 펜스 줄(여는 줄·닫는 줄)인지다 (CommonMark).
// 펜스 밖에서는 ``` 나 ~~~ 3개 이상이 연다 — ``` 의 info string 에 ` 가 있으면 인라인 코드라 펜스가 아니다.
// 펜스 안에서는 같은 문자로, 여는 줄 이상 길게, 뒤에 공백만 있는 줄만 닫는다 — info string 이 붙은 줄(```bash)이나
// 다른 문자의 펜스(~~~ 안의 ```)는 코드 본문이다.
func advanceFence(open codeFence, line string) (codeFence, bool) {
	m := fenceMarker.FindStringSubmatch(line)
	if m == nil {
		return open, false
	}
	marker, rest := m[1], m[2]
	if open.char == 0 {
		if marker[0] == '`' && strings.Contains(rest, "`") {
			return open, false
		}
		return codeFence{char: marker[0], size: len(marker)}, true
	}
	if marker[0] == open.char && len(marker) >= open.size && strings.TrimSpace(rest) == "" {
		return codeFence{}, true
	}
	return open, false
}

// markedLine 은 줄 하나와, 그 줄이 코드(펜스 줄이거나 펜스 안)인지다.
type markedLine struct {
	text string
	code bool
}

// markLines 는 본문을 줄로 나눠 코드 여부를 붙인다. 청커(헤더·문단 경계)와 제목 찾기가 이 펜스 판정 하나를 쓴다.
func markLines(body string) []markedLine {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]markedLine, 0, len(lines))
	var open codeFence
	for _, line := range lines {
		next, isFence := advanceFence(open, line)
		out = append(out, markedLine{text: line, code: isFence || open.char != 0})
		open = next
	}
	return out
}

// FirstH1 은 코드 펜스 밖의 첫 `# ` 제목이다 (청커와 같은 펜스·헤더 규칙 — 앞에 공백이 있는 닫는 # 만 뺀다). 없으면 "", false.
func FirstH1(body string) (string, bool) {
	for _, l := range markLines(body) {
		if l.code {
			continue
		}
		if m := headerLine.FindStringSubmatch(l.text); m != nil && len(m[1]) == 1 {
			return strings.TrimSpace(m[2]), true
		}
	}
	return "", false
}
