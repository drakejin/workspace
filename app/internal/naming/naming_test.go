package naming

import (
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

func TestNames(t *testing.T) {
	at := time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
	if got := MonthlyIndex("ads", at); got != "mem-ads-2026.03" {
		t.Errorf("MonthlyIndex = %s", got)
	}
	ev := at.Format(time.RFC3339)
	if got := IndexFor(model.Chunk{Team: "ads", Kind: model.KindKnowledge, EventTime: ev}); got != "mem-ads-knowledge" {
		t.Errorf("IndexFor knowledge = %s", got)
	}
	if got := IndexFor(model.Chunk{Team: "ads", Kind: model.KindKnowledge, EventTime: ev, SupersededBy: "x"}); got != "mem-ads-2026.03" {
		t.Errorf("밀린 knowledge 는 월별 인덱스로 = %s", got)
	}
	if got := IndexFor(model.Chunk{Team: "ads", Kind: model.KindEpisodic, EventTime: ev}); got != "mem-ads-2026.03" {
		t.Errorf("IndexFor episodic = %s", got)
	}
	if HotAlias("ads") != "mem-ads-hot" || TeamPattern("ads") != "mem-ads-*" || OrgAlias("ads") != "mem-org-ads-hot" {
		t.Error("alias/pattern")
	}
}

func TestParseIndex(t *testing.T) {
	cases := []struct {
		name string
		want Index
		ok   bool
	}{
		{"mem-ads-data-2026.12", Index{Team: "ads-data", MonthStart: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}, true},
		{"mem-ads-knowledge", Index{Team: "ads", Knowledge: true}, true},
		{"mem-ads-knowledge-2026.01", Index{Team: "ads-knowledge", MonthStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, true},
		{"mem-ads-hot", Index{}, false},
		{"mem-ads-2026.13", Index{}, false},
		{"other", Index{}, false},
	}
	for _, c := range cases {
		got, ok := ParseIndex(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseIndex(%s) = %+v,%v", c.name, got, ok)
		}
	}
	if !IsMonthly("mem-a-2026.01") || IsMonthly("mem-a-knowledge") {
		t.Error("IsMonthly")
	}
}

func TestValidateTeam(t *testing.T) {
	for team, ok := range map[string]bool{"ads": true, "ads-1": true, "Ads": false, "-x": false, "광고": false} {
		if (ValidateTeam(team) == nil) != ok {
			t.Errorf("ValidateTeam(%q)", team)
		}
	}
}

func TestValidateTeamReservesOrgNames(t *testing.T) {
	for _, team := range []string{"org", "org-ads"} {
		if ValidateTeam(team) == nil {
			t.Errorf("%q 는 org alias 자리라 팀 이름으로 쓸 수 없다", team)
		}
	}
	for _, team := range []string{"orga", "ads-org", "default"} {
		if err := ValidateTeam(team); err != nil {
			t.Errorf("%q: %v", team, err)
		}
	}
}
