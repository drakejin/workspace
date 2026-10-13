package search

import (
	"testing"
	"time"
)

// RUN-18: 뒤집힌 범위를 미리 알아보려고 경계가 가리키는 시각을 OpenSearch 처럼 계산한다 — from(gte)은 내림, to(lte)는 올림.
// 빠진 시각 칸은 끝으로(빠진 월·일은 1), /단위 반올림은 그 단위의 끝으로 올리고, 주는 월요일에 시작하고, 달 더하기는
// 그달 끝에서 멈춘다. 모두 OpenSearch 2.19 스크래치 인덱스에 경계 양쪽 문서를 넣어 확인한 동작이다.
func TestBoundEdges(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 15, 4, 5, 0, seoul) // 화요일
	at := func(y int, mo time.Month, d, h, mi, s, ms int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ms*int(time.Millisecond), seoul)
	}
	cases := []struct {
		v       string
		roundUp bool
		want    time.Time
	}{
		{"now", true, now},
		{"now/d", false, at(2026, 10, 13, 0, 0, 0, 0)},
		{"now/d", true, at(2026, 10, 13, 23, 59, 59, 999)},
		{"now-d", false, at(2026, 10, 12, 15, 4, 5, 0)},
		{"now-1M/M", false, at(2026, 9, 1, 0, 0, 0, 0)},
		{"now/w", false, at(2026, 10, 12, 0, 0, 0, 0)},
		{"now+2h/H", true, at(2026, 10, 13, 17, 59, 59, 999)},
		{"2026-10", false, at(2026, 10, 1, 0, 0, 0, 0)},
		// OpenSearch 는 올려 읽을 때 빠진 월·일을 1 로 채우고 시각 칸만 끝으로 올린다 (2026-10 은 10월 1일 끝까지 — 재 보니 그렇다)
		{"2026-10", true, at(2026, 10, 1, 23, 59, 59, 999)},
		{"2026", true, at(2026, 1, 1, 23, 59, 59, 999)},
		{"2026-10||/M", true, at(2026, 10, 31, 23, 59, 59, 999)},
		{"2026-10-01T10", true, at(2026, 10, 1, 10, 59, 59, 999)},
		{"2026-10-01T10:00:00.5Z", true, time.Date(2026, 10, 1, 10, 0, 0, 500*int(time.Millisecond), time.UTC)},
		{"2026-01-31||+1M", false, at(2026, 2, 28, 0, 0, 0, 0)},
		{"2024-02-29||+1y", false, at(2025, 2, 28, 0, 0, 0, 0)},
		{"2026-10-01||+1M/d", true, at(2026, 11, 1, 23, 59, 59, 999)},
		{"2026-10-01||", true, at(2026, 10, 1, 0, 0, 0, 0)},                 // || 뒤에 계산이 없어도 기준 날짜는 올리지 않는다
		{"1759312800000", true, time.Date(2025, 10, 1, 10, 0, 0, 0, seoul)}, // epoch 에도 time_zone 이 입혀진다 (아래 TestBoundQuirksMatchOpenSearch)
	}
	for _, c := range cases {
		b, ok := parseBound(c.v, seoul)
		if !ok {
			t.Errorf("%q 를 읽지 못했다", c.v)
			continue
		}

		got := b.at(now, seoul, c.roundUp)

		if !got.Equal(c.want) {
			t.Errorf("%q roundUp=%v = %v, want %v", c.v, c.roundUp, got, c.want)
		}
	}
}

// 시간대 없는 시각은 요청의 시간대(IANA 이름 또는 오프셋)로 읽는다. 읽지 못하는 이름이면 UTC 다 (OpenSearch 가 거부한다).
func TestLocationOf(t *testing.T) {
	cases := map[string]string{"": "+0000", "Asia/Seoul": "+0900", "+09:00": "+0900", "-08:00": "-0800", "Nowhere/City": "+0000"}
	for tz, want := range cases {
		if got := time.Date(2026, 1, 1, 0, 0, 0, 0, locationOf(tz)).Format("-0700"); got != want {
			t.Errorf("%q = %s, want %s", tz, got, want)
		}
	}
}

// OpenSearch 2.19 가 읽는 그대로다: DST 틈에 빠진 벽시계(LA 의 2026-03-08 02:30)는 틈 앞의 오프셋으로 읽어 03:30 PDT 가 되고,
// epoch 밀리초에도 time_zone 이 입혀져 UTC 벽시계를 그 시간대의 벽시계로 읽는다.
func TestBoundQuirksMatchOpenSearch(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]time.Time{
		"2026-03-08T02:30": time.Date(2026, 3, 8, 10, 30, 0, 0, time.UTC),
		"2026-11-01T01:30": time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC), // 겹치는 시각은 앞의 것(PDT)
		"1759312800000":    time.Date(2025, 10, 1, 17, 0, 0, 0, time.UTC), // 2025-10-01T10:00 을 LA 벽시계로
	}
	for v, want := range cases {
		b, ok := parseBound(v, la)

		if got := b.at(time.Now(), la, false); !ok || !got.Equal(want) {
			t.Errorf("%q = %v %v, want %v", v, got.UTC(), ok, want)
		}
	}
}
