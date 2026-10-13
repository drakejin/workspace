// Package markdown 은 frontmatter 분리와 헤더 계층 청킹을 한다.
package markdown

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var fenceLine = regexp.MustCompile(`^---\s*$`)

// Frontmatter 는 문서 맨 위 YAML 이다. 깨진 YAML 이면 비어 있다.
type Frontmatter map[string]any

// FrontmatterError 는 frontmatter 문제다: 닫힌 `---` 블록의 YAML 이 깨졌거나(ParseFrontmatter — 메타데이터 없이 원문 전체를
// 본문으로 쓴다), 값이 규칙에 어긋났다(team 이름 — 그 판은 색인하지 않는다). 데몬은 경고를 남기고 audit 가 bad-frontmatter 로 보고한다.
type FrontmatterError struct{ Err error }

func (e *FrontmatterError) Error() string { return e.Err.Error() }

func (e *FrontmatterError) Unwrap() error { return e.Err }

// SplitFrontmatter 는 `---` 로 감싼 YAML frontmatter 를 떼어 낸다.
// 닫히지 않았거나 YAML 이 깨졌거나 객체가 아니면 frontmatter 가 없는 것으로 본다 (깨졌는지는 ParseFrontmatter 가 알려 준다).
func SplitFrontmatter(source string) (Frontmatter, string) {
	fm, body, _ := ParseFrontmatter(source)
	return fm, body
}

// ParseFrontmatter 는 SplitFrontmatter 와 같되, 닫힌 `---` 블록의 YAML 이 깨졌으면 *FrontmatterError 를 함께 돌려준다.
// 그때도 쓴 글은 버리지 않는다 — 원문 전체가 본문이다. YAML 로는 읽히지만 객체가 아닌 블록(문서 맨 위 구분선 사이의
// 목록·글일 수 있다)은 frontmatter 가 아닐 뿐 오류가 아니다.
func ParseFrontmatter(source string) (Frontmatter, string, error) {
	normalized := strings.ReplaceAll(source, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || !fenceLine.MatchString(lines[0]) {
		return Frontmatter{}, source, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if fenceLine.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return Frontmatter{}, source, nil
	}
	block := []byte(strings.Join(lines[1:end], "\n"))
	var data map[string]any
	if err := yaml.Unmarshal(block, &data); err != nil {
		return Frontmatter{}, source, yamlProblem(block, err)
	}
	if data == nil {
		data = map[string]any{}
	}
	return Frontmatter(data), strings.Join(lines[end+1:], "\n"), nil
}

// yamlProblem 은 객체로 읽지 못한 블록이 frontmatter 의 문제인가다: YAML 문법 오류이거나, 객체인데 읽지 못했다.
// 객체가 아닌 YAML(목록·글)은 frontmatter 가 아니다 — nil.
func yamlProblem(block []byte, decodeErr error) error {
	var node yaml.Node
	if err := yaml.Unmarshal(block, &node); err != nil {
		return &FrontmatterError{Err: err}
	}
	if len(node.Content) > 0 && node.Content[0].Kind == yaml.MappingNode {
		return &FrontmatterError{Err: decodeErr}
	}
	return nil
}
