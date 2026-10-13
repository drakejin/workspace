package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"META_REPO_ROOT": "/meta/My Workspace"}))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]any{
		"url":       {cfg.OpenSearchURL, "http://localhost:9202"},
		"root":      {cfg.MetaRepoRoot, "/meta/My Workspace"},
		"workspace": {cfg.Workspace, "my-workspace"},
		"team":      {cfg.DefaultTeam, "default"},
		"hot":       {cfg.HotDays, 60},
		"repo":      {cfg.Snapshot.Repository, "mem-cold"},
		"coldDir":   {cfg.Cold.Dir, "/meta/My Workspace/.cache/cold-store"},
		"interval":  {cfg.Reconcile.Interval, 60 * time.Second},
		"debounce":  {cfg.Reconcile.Debounce, 3 * time.Second},
		"judge":     {cfg.Judge.Enabled, true},
		"minScore":  {cfg.Judge.MinScore, 0.3},
		"distill":   {cfg.Distill.Enabled, false},
		"model":     {cfg.LLM.Model, "opus"},
		"graph":     {cfg.Graph.URI, "bolt://localhost:7689"},
		"pathStyle": {cfg.Cold.S3PathStyle, false},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %v, want %v", name, c[0], c[1])
		}
	}
	if cfg.HotWindow() != 60*24*time.Hour {
		t.Errorf("HotWindow = %v", cfg.HotWindow())
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"META_REPO_ROOT": "/m", "MEMORY_WORKSPACE": "demo", "MEMORY_DISTILL_ENABLED": "true", "MEMORY_JUDGE_ENABLED": "off",
		"COLD_STORE_DIR": "/abs/cold", "MEMORY_HOT_DAYS": "30", "MEMORY_RECONCILE_DEBOUNCE_MS": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != "demo" || !cfg.Distill.Enabled || cfg.Judge.Enabled || cfg.Cold.Dir != "/abs/cold" || cfg.HotDays != 30 || cfg.Reconcile.Debounce != 0 {
		t.Errorf("override 실패: %+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]map[string]string{
		"MEMORY_HOT_DAYS":         {"MEMORY_HOT_DAYS": "-1"},
		"MEMORY_DEFAULT_TEAM":     {"MEMORY_DEFAULT_TEAM": "Ads"},
		"COLD_SNAPSHOT_S3_BUCKET": {"COLD_SNAPSHOT_TYPE": "s3"},
		"COLD_SNAPSHOT_TYPE":      {"COLD_SNAPSHOT_TYPE": "gcs"},
		"MEMORY_JUDGE_MIN_SCORE":  {"MEMORY_JUDGE_MIN_SCORE": "2"},
		"MEMORY_DISTILL_ENABLED":  {"MEMORY_DISTILL_ENABLED": "maybe"},
		"OPENSEARCH_URL":          {"OPENSEARCH_URL": "not a url"},
		"MEMORY_WORKSPACE":        {"MEMORY_WORKSPACE": "Bad Name"},
		"MEMORY_TIME_ZONE":        {"MEMORY_TIME_ZONE": "Mars/Base"},
	}
	for want, m := range cases {
		m["META_REPO_ROOT"] = "/m"
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestWorkspaceName(t *testing.T) {
	for in, want := range map[string]string{"/a/workspace": "workspace", "/a/My_Repo.v2": "my-repo-v2", "/a/한글": "workspace"} {
		if got := workspaceName(in); got != want {
			t.Errorf("workspaceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadOrgsAndGraphOff(t *testing.T) {
	cfg, err := Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TEAM_ORGS": "ads-data=ads, ads-serving = ads", "MEMORY_GRAPH_URI": "off",
		"AWS_ENDPOINT_URL_S3": "http://localhost:4566", "COLD_STORE_S3_PATH_STYLE": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OrgOf("ads-data") != "ads" || cfg.OrgOf("ads-serving") != "ads" || cfg.OrgOf("x") != "" || cfg.Graph.URI != "" {
		t.Errorf("orgs/graph = %+v %s", cfg.TeamOrgs, cfg.Graph.URI)
	}
	if cfg.Cold.S3Endpoint != "http://localhost:4566" || !cfg.Cold.S3PathStyle {
		t.Errorf("s3 = %+v", cfg.Cold)
	}
	if _, err := Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TEAM_ORGS": "bad"})); err == nil || !strings.Contains(err.Error(), "MEMORY_TEAM_ORGS") {
		t.Errorf("잘못된 org 매핑: %v", err)
	}
}

func TestTimeZone(t *testing.T) {
	cfg, err := Load(env(map[string]string{"META_REPO_ROOT": "/m"}))
	if err != nil || cfg.Location != time.Local {
		t.Fatalf("기본은 이 머신의 시간대: %v %v", cfg.Location, err)
	}
	cfg, err = Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TIME_ZONE": "Asia/Seoul"}))
	if err != nil || cfg.Location.String() != "Asia/Seoul" {
		t.Fatalf("Location = %v %v", cfg.Location, err)
	}
	// OpenSearch 의 time_zone 에는 그 시각의 오프셋을 넘긴다 (date math 의 /d 반올림이 그 시간대의 하루가 된다)
	if got := cfg.QueryTimeZone(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)); got != "+09:00" {
		t.Errorf("QueryTimeZone = %q", got)
	}
	cfg, _ = Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TIME_ZONE": "America/Los_Angeles"}))
	if got := cfg.QueryTimeZone(time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC)); got != "-08:00" {
		t.Errorf("겨울 LA = %q", got)
	}
}
