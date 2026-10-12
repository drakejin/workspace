package naming

import (
	"testing"
	"time"

	"github.com/drakejin/workspace/internal/model"
)

func TestNames(t *testing.T) {
	at := time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
	if got := MonthlyIndex("ads", at); got != "mem-ads-2026.03" {
		t.Errorf("MonthlyIndex = %s", got)
	}
	if got := IndexFor("ads", model.KindKnowledge, at); got != "mem-ads-knowledge" {
		t.Errorf("IndexFor knowledge = %s", got)
	}
	if got := IndexFor("ads", model.KindEpisodic, at); got != "mem-ads-2026.03" {
		t.Errorf("IndexFor episodic = %s", got)
	}
	if HotAlias("ads") != "mem-ads-hot" || TeamPattern("ads") != "mem-ads-*" {
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
