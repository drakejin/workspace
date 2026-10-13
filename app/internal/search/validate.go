package search

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
)

// maxQueryWords 는 질의어 낱말 수의 상한이다. 식별자 분할 analyzer 가 낱말마다 토큰을 여럿 만들어, 1000 낱말 언저리부터
// OpenSearch 가 "Exceeded maximum recursion level during graph analysis" 로 거부한다 (재 보니 800 통과·1000 거부) — 넉넉히 아래에서 막는다.
const maxQueryWords = 256

// boundForms 는 time_range 경계로 받는 꼴이다. 거부할 때 그대로 알린다.
const boundForms = "날짜(YYYY-MM-DD), 시각(2026-10-01T09:00:00, 시간대 생략 가능), date math(now, now-30d, now-7d/d, 2026-10-01||+1M) 중 하나여야 합니다"

var (
	dayOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	// dateMathOps 는 date math 의 기준(now, 날짜||) 뒤에 오는 계산이다: ±N단위 더하기, /단위 반올림 (단위 y M w d h H m s).
	dateMathOps = regexp.MustCompile(`^(?:[+-]\d+[yMwdhHms]|/[yMwdhHms])*$`)
	// dateTimeLayouts 는 시각 경계로 받는 ISO 꼴이다 (OpenSearch strict_date_optional_time 이 읽는 꼴). 소수 초는 Go 가 함께 읽는다.
	dateTimeLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04Z07:00", "2006-01-02T15:04"}
)

// ErrBlankTopic 은 정규화하면 비는 주제(글자·숫자가 없다)다. 쓸 수 있는 값을 보는 도구 이름은 부르는 쪽(mcpserver)이 붙인다.
var ErrBlankTopic = errors.New("topic 에 글자·숫자가 없습니다")

func isDay(v string) bool {
	if !dayOnly.MatchString(v) {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

func isDateTime(v string) bool {
	for _, layout := range dateTimeLayouts {
		if _, err := time.Parse(layout, v); err == nil {
			return true
		}
	}
	return false
}

// validBound 는 범위 경계 하나가 OpenSearch 가 읽는 꼴인가다 — 날짜, 시각, now 기준 date math, 날짜·시각||date math.
func validBound(v string) bool {
	if anchor, ops, anchored := strings.Cut(v, "||"); anchored {
		return (isDay(anchor) || isDateTime(anchor)) && dateMathOps.MatchString(ops)
	}
	if ops, ok := strings.CutPrefix(v, "now"); ok {
		return dateMathOps.MatchString(ops)
	}
	return isDay(v) || isDateTime(v)
}

// Validate 는 필터 값과 질의어를 검사한다 — OpenSearch 에 묻기 전에, 원인을 알 수 있는 말로 거부한다.
func Validate(r Request) error {
	if r.Date != "" && !isDay(r.Date) {
		return fmt.Errorf("date 는 YYYY-MM-DD 여야 합니다: %q", r.Date)
	}
	if err := validateTimeRange(r.TimeRange); err != nil {
		return err
	}
	if n := len(strings.Fields(r.Query)); n > maxQueryWords {
		return fmt.Errorf("query 가 너무 깁니다: 낱말 %d개 (최대 %d) — 서비스·함수 이름, 에러 코드 같은 핵심 낱말만 넣으세요", n, maxQueryWords)
	}
	return validateNames(r)
}

// validateTimeRange 는 범위의 두 경계를 본다. 둘 다 날짜인데 from 이 to 보다 늦으면 조용한 0건 대신 거부한다.
func validateTimeRange(tr model.TimeRange) error {
	for _, b := range []struct{ name, v string }{{"time_range.from", tr.From}, {"time_range.to", tr.To}} {
		if b.v != "" && !validBound(b.v) {
			return fmt.Errorf("%s 값은 %s: %q", b.name, boundForms, b.v)
		}
	}
	if isDay(tr.From) && isDay(tr.To) && tr.From > tr.To {
		return fmt.Errorf("time_range 가 뒤집혔습니다: from(%s)이 to(%s)보다 늦습니다 — from 에 이른 날, to 에 늦은 날을 주세요", tr.From, tr.To)
	}
	return nil
}

// validateNames 는 프로젝트·주제 값을 검사한다. 색인이 받은 이름이면 무엇이든 되므로 제어 문자만 막되, 준 값이 정규화하면
// 비는 경우(topic "!!!", project "  ")는 거부한다 — 조용히 필터를 빼면 결과 머리에는 걸러진 것처럼 보이면서 전체 문서가 나온다.
func validateNames(r Request) error {
	for _, n := range []struct{ name, v string }{{"project", r.Project}, {"topic", r.Topic}} {
		if strings.IndexFunc(n.v, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s 에 제어 문자가 있습니다: %q", n.name, n.v)
		}
	}
	if r.Topic != "" && document.NormalizeTopic(r.Topic) == "" {
		return fmt.Errorf("%w: %q", ErrBlankTopic, r.Topic)
	}
	if r.Project != "" && document.NormalizeProject(r.Project) == "" {
		return fmt.Errorf("project 가 공백뿐입니다: %q", r.Project)
	}
	return nil
}
