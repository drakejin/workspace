// Package naming 은 인덱스·alias 이름 규칙이다.
//
//	episodic   mem-{team}-{yyyy.MM}   월별. ISM 이 hot 기간이 지나면 cold 로 보낸다
//	knowledge  mem-{team}-knowledge   노화하지 않는다 (supersedes 로 밀린 것만 판정이 숨긴다)
//	읽기 alias  mem-{team}-hot         위 둘 전부
package naming

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/model"
)

// Prefix 는 모든 메모리 인덱스의 접두어다.
const Prefix = "mem-"

var (
	monthPattern     = regexp.MustCompile(`^mem-([a-z0-9][a-z0-9-]*)-(\d{4})\.(\d{2})$`)
	knowledgePattern = regexp.MustCompile(`^mem-([a-z0-9][a-z0-9-]*)-knowledge$`)
)

// ValidateTeam 은 팀 이름을 검사한다.
func ValidateTeam(team string) error {
	if !config.TeamPattern.MatchString(team) {
		return fmt.Errorf("team 이름이 잘못됐습니다: %q (소문자, 숫자, 하이픈만)", team)
	}
	return nil
}

// MonthlyIndex 는 episodic 월별 인덱스 이름이다.
func MonthlyIndex(team string, t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%s%s-%04d.%02d", Prefix, team, t.Year(), int(t.Month()))
}

// KnowledgeIndex 는 노화하지 않는 knowledge 인덱스 이름이다.
func KnowledgeIndex(team string) string { return Prefix + team + "-knowledge" }

// IndexFor 는 청크가 들어갈 인덱스다.
func IndexFor(team string, kind model.Kind, eventTime time.Time) string {
	if kind == model.KindKnowledge {
		return KnowledgeIndex(team)
	}
	return MonthlyIndex(team, eventTime)
}

// HotAlias 는 팀의 hot 인덱스 전부를 가리키는 읽기 alias 다.
func HotAlias(team string) string { return Prefix + team + "-hot" }

// TeamPattern 은 팀 인덱스 와일드카드다. 주의: mem-ads-* 는 mem-ads-data-* 도 잡는다.
// 결과는 ParseIndex 의 Team 이나 team 필드로 다시 거른다.
func TeamPattern(team string) string { return Prefix + team + "-*" }

// AllPattern 은 모든 메모리 인덱스다.
const AllPattern = Prefix + "*"

// Index 는 파싱한 인덱스 이름이다.
type Index struct {
	Team       string
	Knowledge  bool
	MonthStart time.Time // Knowledge 면 zero
}

// ParseIndex 는 메모리 인덱스 이름을 읽는다. 아니면 false.
func ParseIndex(name string) (Index, bool) {
	if m := monthPattern.FindStringSubmatch(name); m != nil {
		year, _ := strconv.Atoi(m[2])
		month, _ := strconv.Atoi(m[3])
		if month < 1 || month > 12 {
			return Index{}, false
		}
		return Index{Team: m[1], MonthStart: time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)}, true
	}
	if m := knowledgePattern.FindStringSubmatch(name); m != nil {
		return Index{Team: m[1], Knowledge: true}, true
	}
	return Index{}, false
}

// IsMonthly 는 episodic 월별 인덱스인가.
func IsMonthly(name string) bool {
	idx, ok := ParseIndex(name)
	return ok && !idx.Knowledge
}
