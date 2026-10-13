package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// RUN-8·API-6: 정규화하면 빈 값이 되는 topic("!!!", " / ")은 조용히 빠지지 않고 거부된다 — 예전에는 머리글에 topic=!!! 를
// 찍고 실제로는 필터 없이 workspace 전체 문서를 돌려줬다. 공백뿐인 project 도 같다.
func TestFilterThatNormalizesToEmptyIsRejected(t *testing.T) {
	ctx := context.Background()
	for _, r := range []Request{{Topic: "!!!"}, {Topic: " / "}, {Topic: "---"}, {Project: "   "}, {Query: "x", Topic: "!!!"}} {
		if err := Validate(r); err == nil {
			t.Errorf("Validate(%+v) 는 거부", r)
		}
		r.Team = "a"
		_, err := Search(ctx, &stubOS{resp: map[string]any{}}, r)
		if err == nil || errors.Is(err, ErrEmptyQuery) {
			t.Errorf("Search(%+v) = %v — 필터 값이 잘못됐다고 알려야 한다", r, err)
		}
		if _, err := FetchFacets(ctx, &stubOS{resp: map[string]any{}}, r); err == nil {
			t.Errorf("FetchFacets(%+v) 도 거부", r)
		}
	}
	if err := Validate(Request{Topic: "!!!"}); err == nil || !strings.Contains(err.Error(), "topic") || !strings.Contains(err.Error(), "!!!") {
		t.Errorf("무엇이 잘못됐는지 알린다: %v", err)
	}
	if HasFilter(Request{Topic: " / "}) || HasFilter(Request{Project: "  "}) {
		t.Error("HasFilter 는 정규화한 값으로 판단한다")
	}
	if !HasFilter(Request{Topic: "Payments"}) || !HasFilter(Request{Project: "결제 서비스"}) {
		t.Error("HasFilter")
	}
}

// RUN-5: 검색 입력의 주제·프로젝트도 NFC 로 맞춘다 — 색인 값(NFC)과 같은 꼴로 비교한다.
func TestFilterValuesAreNFC(t *testing.T) {
	q := buildQuery(Request{Query: "x", Topic: norm.NFD.String("환불정책"), Project: " " + norm.NFD.String("결제 서비스") + " "})
	got, _ := json.Marshal(filterOf(t, q))
	want := `[{"term":{"repo":{"case_insensitive":true,"value":"결제 서비스"}}},{"term":{"topic":"환불정책"}}]`
	if string(got) != norm.NFC.String(want) {
		t.Errorf("filter\n got  %s\n want %s", got, want)
	}
}
