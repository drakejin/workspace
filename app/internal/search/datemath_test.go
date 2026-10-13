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
		{"2026-10-01||", true, at(2026, 10, 1, 0, 0, 0, 0)},                     // || 뒤에 계산이 없어도 기준 날짜는 올리지 않는다
		{"1759312800000", true, time.Date(2025, 10, 1, 10, 0, 0, 0, time.UTC)},  // epoch 는 그 시각 그대로다 (시간대를 입히지 않는다)
		{"1759312800000||/d", false, time.Date(2025, 10, 1, 0, 0, 0, 0, seoul)}, // epoch 뒤의 date math 는 요청의 시간대에서 계산한다
		{"1759312800000||+1d/d", true, time.Date(2025, 10, 2, 23, 59, 59, 999*int(time.Millisecond), seoul)},
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

// R4-query-a-1: 서머타임이 바뀌는 날의 벽시계를 OpenSearch 2.19(java.time)가 읽는 그대로 읽는다 — UTC 서쪽(LA)만이 아니라
// 동쪽(베를린·런던·로드하우)도. DST 틈에 빠진(없는) 벽시계는 틈 앞의 오프셋으로 읽어 틈 길이만큼 늦어지고, 두 번 오는(겹치는)
// 벽시계는 앞의 것이다. date math 의 더하기·반올림이 틈·겹침에 닿으면 계산하던 시각의 오프셋을 고집한다(맞지 않으면 앞과 같다).
// 기댓값은 모두 OpenSearch 2.19 의 _validate/query?explain 이 보인 경계다 (datemath_e2e_test.go 가 실제 클러스터와 견준다).
// 예전에는 time.Date 가 고르는 쪽에 기댔는데, 그 쪽이 UTC 서쪽과 동쪽에서 달라 베를린 02:30(틈)을 한 시간 이르게 읽었다.
func TestBoundQuirksMatchOpenSearch(t *testing.T) {
	for _, c := range dstCases {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		b, ok := parseBound(c.v, loc)

		if got := b.at(time.Now(), loc, c.roundUp); !ok || !got.Equal(c.want) {
			t.Errorf("%s %q roundUp=%v = %v %v, want %v", c.zone, c.v, c.roundUp, got.UTC(), ok, c.want)
		}
	}
}

// dstCases 는 서머타임이 바뀌는 날의 경계와 OpenSearch 2.19 가 읽은 시각(UTC)이다.
var dstCases = []struct {
	zone, v string
	roundUp bool
	want    time.Time
}{
	{"America/Los_Angeles", "2026-03-08T02:30", false, utc(2026, 3, 8, 10, 30, 0)}, // 틈 → 03:30 PDT
	{"America/Los_Angeles", "2026-11-01T01:30", false, utc(2026, 11, 1, 8, 30, 0)}, // 겹침 → 앞의 것(PDT)
	{"Europe/Berlin", "2026-03-29T02:30", false, utc(2026, 3, 29, 1, 30, 0)},       // 틈 → 03:30 CEST
	{"Europe/Berlin", "2026-10-25T02:30", false, utc(2026, 10, 25, 0, 30, 0)},      // 겹침 → 앞의 것(CEST)
	{"Europe/Berlin", "2026-10-25T02:30", true, utc(2026, 10, 25, 0, 30, 59).Add(999 * time.Millisecond)},
	{"Europe/London", "2026-03-29T01:30", false, utc(2026, 3, 29, 1, 30, 0)},        // 틈 → 02:30 BST
	{"Australia/Lord_Howe", "2026-10-04T02:15", false, utc(2026, 10, 3, 15, 45, 0)}, // 30분 틈 → 02:45 +11:00
	{"America/Los_Angeles", "2026-03-07T02:30||+1d", false, utc(2026, 3, 8, 10, 30, 0)},
	{"Europe/Berlin", "2026-10-24T02:30||+1d", false, utc(2026, 10, 25, 0, 30, 0)},      // CEST 에서 와서 겹침 → CEST
	{"Europe/Berlin", "2026-10-26T02:30||-1d", false, utc(2026, 10, 25, 1, 30, 0)},      // CET 에서 와서 겹침 → CET
	{"America/Los_Angeles", "2026-11-02T01:30||-1d", false, utc(2026, 11, 1, 9, 30, 0)}, // PST 에서 와서 겹침 → PST
	{"Europe/Berlin", "2026-03-29T03:30||/h", false, utc(2026, 3, 29, 1, 0, 0)},
	{"Europe/Berlin", "2026-03-29T03:30||/d", false, utc(2026, 3, 28, 23, 0, 0)},
	{"Europe/Berlin", "2026-03-29T03:30||/d", true, utc(2026, 3, 29, 21, 59, 59).Add(999 * time.Millisecond)},
	{"Europe/Berlin", "2026-10-25T02:30||/h", false, utc(2026, 10, 25, 0, 0, 0)}, // 겹침 안의 반올림은 앞의 시각(CEST)에 머문다
	{"Europe/Berlin", "2026-10-25T02:30||/h", true, utc(2026, 10, 25, 0, 59, 59).Add(999 * time.Millisecond)},
	{"Europe/Berlin", "2026-03-29T02", false, utc(2026, 3, 29, 1, 0, 0)},
	{"Europe/Berlin", "2026-03-29T02", true, utc(2026, 3, 29, 1, 59, 59).Add(999 * time.Millisecond)},
}

func utc(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}
