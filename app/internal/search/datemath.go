package search

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

// 이 파일은 time_range 경계를 OpenSearch 처럼 읽는다 — 받는 꼴인지 보고(Validate), 뒤집힌 범위를 미리 알아보려고 경계가
// 가리키는 시각을 계산한다. 꼴은 event_time 의 형식(strict_date_optional_time||epoch_millis)과 date math 를 따른다.

var (
	// absoluteBound 는 strict_date_optional_time 이다: 연[-월[-일[T시[:분[:초[.소수]]][시간대]]]]. 시간대는 시각 뒤에만 붙는다.
	absoluteBound = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2})(?:T(\d{2})(?::(\d{2})(?::(\d{2})(?:\.(\d{1,9}))?)?)?(Z|[+-]\d{2}(?::?\d{2})?)?)?)?)?$`)
	// epochMillis 는 1970 이후의 epoch 밀리초다 (음수는 OpenSearch 가 받기는 하지만 엉뚱한 시각으로 읽어 받지 않는다)
	epochMillis = regexp.MustCompile(`^\d+$`)
	// offsetOnly 는 요청 시간대가 IANA 이름 대신 고정 오프셋일 때다 (이름 없는 시간대 — config.QueryTimeZone)
	offsetOnly = regexp.MustCompile(`^[+-]\d{2}(?::?\d{2})?$`)
	// dateMathOps 는 date math 의 기준(now, 날짜||) 뒤에 오는 계산이다: ±N단위 더하기(N 을 빼면 1), /단위 반올림 (단위 y M w d h H m s).
	dateMathOps = regexp.MustCompile(`^(?:[+-]\d*[yMwdhHms]|/[yMwdhHms])*$`)
	dateMathOp  = regexp.MustCompile(`[+-]\d*[yMwdhHms]|/[yMwdhHms]`)
)

// 단위는 date math 의 글자 그대로다 (H 는 h 로 읽는다). exact 는 소수 초·epoch 처럼 올릴 칸이 없는 시각이다.
const (
	unitYear   = 'y'
	unitMonth  = 'M'
	unitWeek   = 'w'
	unitDay    = 'd'
	unitHour   = 'h'
	unitMinute = 'm'
	unitSecond = 's'
	exact      = 0
)

// fieldUnits 는 absoluteBound 의 연·월·일·시·분·초 칸의 단위다.
const fieldUnits = "yMdhms"

// fractionDigits 는 소수 초를 나노초로 읽을 자릿수다.
const fractionDigits = 9

// maxOffset 은 시간대 오프셋의 한계다 (OpenSearch(java.time)는 ±18:00 까지 받는다).
const maxOffset = 18 * time.Hour

// bound 는 읽은 경계 하나다.
type bound struct {
	isNow  bool
	anchor time.Time // now 가 아니면 적힌 시각 (빠진 칸은 가장 이른 값)
	unit   byte      // anchor 에 적힌 가장 작은 단위 — 올려 읽으면 그 단위의 끝으로 간다
	plain  bool      // date math 없는 날짜·시각이다 (|| 뒤에 계산이 붙으면 기준 날짜는 올리지 않는다)
	ops    []mathOp
}

// mathOp 는 date math 계산 하나다 — n 단위를 더하거나, round 면 그 단위로 반올림한다.
type mathOp struct {
	round bool
	n     int
	unit  byte
}

// parseBound 는 경계 하나를 읽는다 — 날짜·시각(시간대가 없으면 loc), epoch 밀리초, now·날짜|| 뒤의 date math.
func parseBound(v string, loc *time.Location) (bound, bool) {
	if ops, ok := strings.CutPrefix(v, "now"); ok {
		return withMath(bound{isNow: true}, ops)
	}
	anchor, ops, anchored := strings.Cut(v, "||")
	b, ok := parseAbsolute(anchor, loc)
	if !ok || !anchored {
		return b, ok
	}
	return withMath(b, ops)
}

func withMath(b bound, ops string) (bound, bool) {
	if !dateMathOps.MatchString(ops) {
		return bound{}, false
	}
	b.plain = false
	for _, s := range dateMathOp.FindAllString(ops, -1) {
		op, ok := parseOp(s)
		if !ok {
			return bound{}, false
		}
		b.ops = append(b.ops, op)
	}
	return b, true
}

func parseOp(s string) (mathOp, bool) {
	unit := s[len(s)-1]
	if unit == 'H' {
		unit = unitHour
	}
	if s[0] == '/' {
		return mathOp{round: true, unit: unit}, true
	}
	n := 1
	if digits := s[1 : len(s)-1]; digits != "" {
		var err error
		if n, err = strconv.Atoi(digits); err != nil {
			return mathOp{}, false
		}
	}
	if s[0] == '-' {
		n = -n
	}
	return mathOp{n: n, unit: unit}, true
}

func parseAbsolute(s string, loc *time.Location) (bound, bool) {
	if m := absoluteBound.FindStringSubmatch(s); m != nil {
		return dateBound(m, loc)
	}
	if !epochMillis.MatchString(s) {
		return bound{}, false
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return bound{}, false
	}
	// OpenSearch 2.19 는 time_zone 을 epoch 밀리초에도 입힌다 — UTC 벽시계를 그 시간대의 벽시계로 읽는다 (재 보니 그렇다)
	wall := time.UnixMilli(ms).UTC()
	return bound{anchor: inZone(wall, loc), unit: exact, plain: true}, true
}

// inZone 은 UTC 로 둔 벽시계 wall 을 loc 의 벽시계로 읽은 시각이다. DST 틈에 빠진(없는) 벽시계는 OpenSearch(java.time)처럼 틈
// 앞의 오프셋으로 읽는다 (02:30 → 03:30 서머타임) — time.Date 는 틈 뒤의 오프셋으로 읽는다.
func inZone(wall time.Time, loc *time.Location) time.Time {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	if t.Hour() == wall.Hour() && t.Minute() == wall.Minute() {
		return t
	}
	_, before := t.Zone()
	return wall.Add(-time.Duration(before) * time.Second).In(loc)
}

// dateBound 는 strict_date_optional_time 으로 읽은 칸들의 시각이다. 없는 날·시각(2월 30일, 25시)은 거부한다.
func dateBound(m []string, loc *time.Location) (bound, bool) {
	zone, ok := zoneOf(m[8], loc)
	if !ok {
		return bound{}, false
	}
	f := [6]int{0, 1, 1, 0, 0, 0}
	unit := byte(unitYear)
	for i := range f {
		if m[i+1] == "" {
			break
		}
		f[i], _ = strconv.Atoi(m[i+1])
		unit = fieldUnits[i]
	}
	nanos := 0
	if m[7] != "" {
		nanos, _ = strconv.Atoi((m[7] + strings.Repeat("0", fractionDigits))[:fractionDigits])
		unit = exact
	}
	// time.Date 는 넘친 칸을 다음 칸으로 넘겨 맞추므로 UTC 로 만들어 적은 값 그대로인지 본다 (시간대의 DST 틈은 inZone 이 맞춘다)
	check := time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], 0, time.UTC)
	if int(check.Month()) != f[1] || check.Day() != f[2] || check.Hour() != f[3] || check.Minute() != f[4] || check.Second() != f[5] {
		return bound{}, false
	}
	return bound{anchor: inZone(time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], nanos, time.UTC), zone), unit: unit, plain: true}, true
}

// zoneOf 는 시각에 붙은 시간대다 — 없으면 loc, Z 면 UTC, ±HH[:MM]·±HHMM 이면 그 오프셋 (±18:00 까지).
func zoneOf(s string, loc *time.Location) (*time.Location, bool) {
	switch s {
	case "":
		return loc, true
	case "Z":
		return time.UTC, true
	}
	digits := strings.ReplaceAll(s[1:], ":", "")
	hh, _ := strconv.Atoi(digits[:2])
	mm := 0
	if len(digits) > 2 {
		mm, _ = strconv.Atoi(digits[2:])
	}
	offset := time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute
	if mm >= 60 || offset > maxOffset {
		return nil, false
	}
	if s[0] == '-' {
		offset = -offset
	}
	return time.FixedZone("", int(offset/time.Second)), true
}

// locationOf 는 요청의 시간대(IANA 이름 또는 +09:00 오프셋)다. 비거나 읽지 못하면 UTC — OpenSearch 도 time_zone 이 없으면 UTC
// 로 읽고, 읽지 못하는 이름은 OpenSearch 가 거부한다.
func locationOf(timeZone string) *time.Location {
	if offsetOnly.MatchString(timeZone) {
		if loc, ok := zoneOf(timeZone, time.UTC); ok {
			return loc
		}
	}
	if loc, err := time.LoadLocation(timeZone); err == nil {
		return loc
	}
	return time.UTC
}

// edgeLayout 은 뒤집힌 범위를 알릴 때 보이는 읽은 시각의 꼴이다.
const edgeLayout = "2006-01-02T15:04:05.000Z07:00"

// rangeEdges 는 범위의 두 끝 시각이다 — from 이 가리키는 가장 이른 시각과 to 가 가리키는 가장 늦은 시각. from 이 to 보다
// 늦으면 맞는 문서가 있을 수 없다. 날짜 경계(event_day 로 비교한다)도 그날의 시작·끝 시각으로 함께 본다. 한쪽이라도
// 없거나 읽지 못하면 ok 가 아니다.
func rangeEdges(tr model.TimeRange, now time.Time, loc *time.Location) (from, to time.Time, ok bool) {
	f, okFrom := parseBound(tr.From, loc)
	t, okTo := parseBound(tr.To, loc)
	if !okFrom || !okTo {
		return time.Time{}, time.Time{}, false
	}
	return f.at(now, loc, false), t.at(now, loc, true), true
}

// at 은 경계가 가리키는 시각이다 — OpenSearch 처럼 from(gte)은 그대로, to(lte)는 올려 읽는다(roundUp): 날짜·시각은 빠진 시각
// 칸을 끝으로(빠진 월·일은 1 이라 to=2026-10 은 10월 1일 끝까지다 — 달 전체는 2026-10||/M), /단위 반올림은 그 단위의
// 끝으로. date math 는 요청의 시간대(loc)에서 계산한다.
func (b bound) at(now time.Time, loc *time.Location, roundUp bool) time.Time {
	if b.plain {
		if roundUp {
			return endOf(b.anchor, roundUpUnit(b.unit))
		}
		return b.anchor
	}
	t := b.anchor
	if b.isNow {
		t = now
	}
	t = t.In(loc)
	for _, op := range b.ops {
		t = op.apply(t, roundUp)
	}
	return t
}

// roundUpUnit 은 날짜·시각을 올려 읽을 단위다. 연·월까지만 적었으면 OpenSearch 는 빠진 월·일을 1 로 채우고 그날 끝으로 올린다.
func roundUpUnit(unit byte) byte {
	if unit == unitYear || unit == unitMonth {
		return unitDay
	}
	return unit
}

func (op mathOp) apply(t time.Time, roundUp bool) time.Time {
	switch {
	case !op.round:
		return add(t, op.unit, op.n)
	case roundUp:
		return endOf(t, op.unit)
	}
	return startOf(t, op.unit)
}

// startOf 는 t 가 든 단위의 시작이다 (주는 월요일에 시작한다). exact 면 t 그대로.
func startOf(t time.Time, unit byte) time.Time {
	y, mo, d := t.Date()
	loc := t.Location()
	switch unit {
	case unitYear:
		return time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	case unitMonth:
		return time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	case unitWeek:
		return time.Date(y, mo, d-(int(t.Weekday())+6)%7, 0, 0, 0, 0, loc)
	case unitDay:
		return time.Date(y, mo, d, 0, 0, 0, 0, loc)
	case unitHour:
		return time.Date(y, mo, d, t.Hour(), 0, 0, 0, loc)
	case unitMinute:
		return time.Date(y, mo, d, t.Hour(), t.Minute(), 0, 0, loc)
	case unitSecond:
		return time.Date(y, mo, d, t.Hour(), t.Minute(), t.Second(), 0, loc)
	}
	return t
}

// endOf 는 t 가 든 단위의 끝이다 — 다음 단위가 시작하기 1ms 전 (OpenSearch 의 date 는 ms 단위다). exact 면 t 그대로.
func endOf(t time.Time, unit byte) time.Time {
	if unit == exact {
		return t
	}
	return add(startOf(t, unit), unit, 1).Add(-time.Millisecond)
}

// add 는 t 에 n 단위를 더한다. 연·달은 그달 끝에서 멈춘다 (1/31 + 1M = 2/28 — OpenSearch(java.time)와 같다).
func add(t time.Time, unit byte, n int) time.Time {
	switch unit {
	case unitYear:
		return addMonths(t, 12*n)
	case unitMonth:
		return addMonths(t, n)
	case unitWeek:
		return t.AddDate(0, 0, 7*n)
	case unitDay:
		return t.AddDate(0, 0, n)
	case unitHour:
		return t.Add(time.Duration(n) * time.Hour)
	case unitMinute:
		return t.Add(time.Duration(n) * time.Minute)
	}
	return t.Add(time.Duration(n) * time.Second)
}

func addMonths(t time.Time, n int) time.Time {
	y, mo, d := t.Date()
	first := time.Date(y, mo+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	return first.AddDate(0, 0, min(d, daysIn(first))-1)
}

// daysIn 은 t 가 든 달의 날 수다.
func daysIn(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
