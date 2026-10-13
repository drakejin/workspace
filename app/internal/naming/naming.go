// Package naming 은 이름 규칙이다 — 팀·workspace·org 이름, 인덱스·alias 이름.
//
//	episodic   mem-{team}-{yyyy.MM}   월별. ISM·tier 가 hot 기간이 지나면 cold 로 보낸다
//	knowledge  mem-{team}-knowledge   노화하지 않는다 (supersedes 로 밀리면 월별 인덱스로 옮겨 노화한다)
//	읽기 alias  mem-{team}-hot         위 둘 전부
//	org alias  mem-org-{org}-hot      "실" — 여러 팀의 hot 인덱스 전부
package naming

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

// Prefix 는 모든 메모리 인덱스의 접두어다.
const Prefix = "mem-"

// AllPattern 은 모든 메모리 인덱스다.
const AllPattern = Prefix + "*"

// KnowledgePattern 은 모든 knowledge 인덱스다.
const KnowledgePattern = Prefix + "*-knowledge"

var (
	name             = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	monthPattern     = regexp.MustCompile(`^mem-([a-z0-9][a-z0-9-]*)-(\d{4})\.(\d{2})$`)
	knowledgePattern = regexp.MustCompile(`^mem-([a-z0-9][a-z0-9-]*)-knowledge$`)
)

// ValidName 은 팀·workspace·org 이름 규칙(소문자, 숫자, 하이픈)이다. 인덱스 이름에 그대로 들어간다.
func ValidName(s string) bool { return name.MatchString(s) }

// ValidateTeam 은 팀 이름을 검사한다.
func ValidateTeam(team string) error {
	if !ValidName(team) {
		return fmt.Errorf("team 이름이 잘못됐습니다: %q (소문자, 숫자, 하이픈만)", team)
	}
	if team == "org" || strings.HasPrefix(team, "org-") {
		// mem-org-{org}-hot 은 org alias 자리다 — 팀 alias(mem-{team}-hot)와 겹치지 않게
		return fmt.Errorf("team 이름 %q 는 쓸 수 없습니다 (org 와 org-* 는 org alias 용)", team)
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

// IndexFor 는 청크가 들어갈 인덱스다. knowledge 는 knowledge 인덱스에 두되, 개정판에 밀려나면(superseded)
// 자기 날짜의 월별 인덱스로 옮겨 episodic 처럼 노화시킨다 (drakejin 회사: "개정판이 밀어낸 것만 이동").
func IndexFor(c model.Chunk) string {
	if c.Kind == model.KindKnowledge && c.SupersededBy == "" {
		return KnowledgeIndex(c.Team)
	}
	return MonthlyIndex(c.Team, c.EventTimeValue())
}

// HotAlias 는 팀의 hot 인덱스 전부를 가리키는 읽기 alias 다.
func HotAlias(team string) string { return Prefix + team + "-hot" }

// OrgAlias 는 org("실")에 속한 팀들의 hot 인덱스 전부를 가리키는 읽기 alias 다.
func OrgAlias(org string) string { return Prefix + "org-" + org + "-hot" }

// TeamPattern 은 팀 인덱스 와일드카드다. 주의: mem-ads-* 는 mem-ads-data-* 도 잡는다.
// 결과는 ParseIndex 의 Team 이나 team 필드로 다시 거른다.
func TeamPattern(team string) string { return Prefix + team + "-*" }

// Index 는 파싱한 인덱스 이름이다.
type Index struct {
	Team       string
	Knowledge  bool
	MonthStart time.Time // Knowledge 면 zero
}

// ParseIndex 는 메모리 인덱스 이름을 읽는다. 아니면 false.
func ParseIndex(index string) (Index, bool) {
	if m := monthPattern.FindStringSubmatch(index); m != nil {
		year, _ := strconv.Atoi(m[2])
		month, _ := strconv.Atoi(m[3])
		if month < 1 || month > 12 {
			return Index{}, false
		}
		return Index{Team: m[1], MonthStart: time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)}, true
	}
	if m := knowledgePattern.FindStringSubmatch(index); m != nil {
		return Index{Team: m[1], Knowledge: true}, true
	}
	return Index{}, false
}

// IsMonthly 는 episodic 월별 인덱스인가.
func IsMonthly(index string) bool {
	idx, ok := ParseIndex(index)
	return ok && !idx.Knowledge
}
