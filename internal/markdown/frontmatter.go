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

// SplitFrontmatter 는 `---` 로 감싼 YAML frontmatter 를 떼어 낸다.
// 닫히지 않았거나 YAML 이 깨졌거나 객체가 아니면 frontmatter 가 없는 것으로 본다.
func SplitFrontmatter(source string) (Frontmatter, string) {
	normalized := strings.ReplaceAll(source, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || !fenceLine.MatchString(lines[0]) {
		return Frontmatter{}, source
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if fenceLine.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return Frontmatter{}, source
	}
	var data map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &data); err != nil {
		return Frontmatter{}, source
	}
	if data == nil {
		data = map[string]any{}
	}
	return Frontmatter(data), strings.Join(lines[end+1:], "\n")
}
