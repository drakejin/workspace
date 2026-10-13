//go:build e2e

package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

// explainedRange 는 _validate/query?explain 이 보인 event_time 범위의 밀리초 두 끝이다.
var explainedRange = regexp.MustCompile(`event_time:\[(-?\d+) TO (-?\d+)\]`)

// R4-query-a-1 · R4-query-a-2: 경계를 읽는 계산(at)과 보내는 질의(boundFilter)가 실제 OpenSearch 의 읽기와 같다 — 서머타임이
// 바뀌는 날(UTC 서쪽·동쪽)의 시각·date math, 그리고 epoch 밀리초(시간대를 입히지 않은 그 시각). 문서를 넣지 않고 스크래치
// 인덱스에 _validate/query?explain 으로 경계를 물어, 설명에 나온 밀리초 경계와 견준다.
func TestBoundsResolveLikeOpenSearch(t *testing.T) {
	c, index := scratchIndex(t)
	cases := append([]struct {
		zone, v string
		roundUp bool
		want    time.Time
	}{
		{"Asia/Seoul", "1759312800000", false, utc(2025, 10, 1, 10, 0, 0)},
		{"Asia/Seoul", "1759312800000", true, utc(2025, 10, 1, 10, 0, 0)},
		{"Asia/Seoul", "1759312800000||/d", false, utc(2025, 9, 30, 15, 0, 0)},
		{"Asia/Seoul", "1759312800000||+1d/d", true, utc(2025, 10, 2, 14, 59, 59).Add(999 * time.Millisecond)},
		// 네 자리 수는 epoch 가 아니라 연도다
		{"Asia/Seoul", "2026", false, utc(2025, 12, 31, 15, 0, 0)},
		{"Asia/Seoul", "2026", true, utc(2026, 1, 1, 14, 59, 59).Add(999 * time.Millisecond)},
		{"Asia/Seoul", "2026||+1M", false, utc(2026, 1, 31, 15, 0, 0)},
	}, dstCases...)
	for _, tc := range cases {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		b, ok := parseBound(tc.v, loc)
		if !ok {
			t.Errorf("%q 를 읽지 못했다", tc.v)
			continue
		}

		ours := b.at(time.Now(), loc, tc.roundUp)
		theirs := explainBound(t, c, index, tc.v, tc.zone, tc.roundUp)

		if !ours.Equal(tc.want) || !theirs.Equal(tc.want) {
			t.Errorf("%s %q roundUp=%v: 계산 %v, OpenSearch %v, want %v", tc.zone, tc.v, tc.roundUp, ours.UTC(), theirs.UTC(), tc.want)
		}
	}
}

// scratchIndex 는 event_time(date) 하나만 가진 빈 인덱스를 만들고, 테스트가 끝나면 지운다.
func scratchIndex(t *testing.T) (opensearch.Client, string) {
	t.Helper()
	base := os.Getenv("OPENSEARCH_URL")
	if base == "" {
		base = "http://localhost:9202"
	}
	c := opensearch.New(base)
	ctx := context.Background()
	if err := c.Do(ctx, http.MethodGet, "/_cluster/health", nil, nil, nil); err != nil {
		t.Skipf("OpenSearch(%s)에 붙을 수 없습니다 — make memory-init 먼저: %v", base, err)
	}
	index := fmt.Sprintf("search-bounds-e2e-%d", time.Now().UnixNano())
	body := map[string]any{
		"settings": map[string]any{"number_of_replicas": 0},
		"mappings": map[string]any{"properties": map[string]any{"event_time": map[string]any{"type": "date"}}},
	}
	if err := c.Do(ctx, http.MethodPut, "/"+index, body, nil, nil); err != nil {
		t.Fatalf("스크래치 인덱스 %s: %v", index, err)
	}
	t.Cleanup(func() { _ = c.Do(context.Background(), http.MethodDelete, "/"+index, nil, nil, nil) })
	return c, index
}

// explainBound 는 BuildQuery 가 v 로 만든 경계(gte, roundUp 이면 lte)를 OpenSearch 가 읽은 시각이다.
func explainBound(t *testing.T, c opensearch.Client, index, v, zone string, roundUp bool) time.Time {
	t.Helper()
	tr, side := model.TimeRange{From: v}, 1
	if roundUp {
		tr, side = model.TimeRange{To: v}, 2
	}
	q := BuildQuery(Request{Query: "x", TimeRange: tr, TimeZone: zone})
	var out struct {
		Valid        bool `json:"valid"`
		Explanations []struct {
			Explanation string `json:"explanation"`
		} `json:"explanations"`
	}
	body := map[string]any{"query": filterOf(t, q)[0]}
	if err := c.Do(context.Background(), http.MethodPost, "/"+index+"/_validate/query", body, url.Values{"explain": {"true"}}, &out); err != nil {
		t.Fatalf("%q: %v", v, err)
	}
	if !out.Valid || len(out.Explanations) == 0 {
		t.Fatalf("%q: OpenSearch 가 읽지 못했다: %+v", v, out)
	}
	m := explainedRange.FindStringSubmatch(out.Explanations[0].Explanation)
	if m == nil {
		t.Fatalf("%q: 범위가 없는 설명: %s", v, out.Explanations[0].Explanation)
	}
	ms, err := strconv.ParseInt(m[side], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.UnixMilli(ms).UTC()
}
