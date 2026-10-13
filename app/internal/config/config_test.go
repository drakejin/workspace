package config

import (
	"errors"
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
	if err != nil || !sameOffsets(cfg.Location, time.Local) {
		t.Fatalf("기본은 이 머신의 시간대: %v %v", cfg.Location, err)
	}
	cfg, err = Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TIME_ZONE": "Asia/Seoul"}))
	if err != nil || cfg.Location.String() != "Asia/Seoul" {
		t.Fatalf("Location = %v %v", cfg.Location, err)
	}
}

// API-2: 그래프 URI 의 scheme 오타(http://, scheme 없는 host:port)는 다른 설정 오류와 함께 Load 가 모아 알린다 —
// 예전에는 Load 를 통과하고 graph.New 에서 실패해 MCP 서버·status 를 포함한 모든 명령이 함께 멈췄다.
func TestLoadRejectsBadGraphURIScheme(t *testing.T) {
	for _, bad := range []string{"http://localhost:7474", "localhost:7689", "bolt//localhost:7689", "neo4j+x://h:7687", "bolt://"} {
		_, err := Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_GRAPH_URI": bad, "MEMORY_HOT_DAYS": "-1"}))

		if err == nil || !strings.Contains(err.Error(), "MEMORY_GRAPH_URI") || !strings.Contains(err.Error(), "MEMORY_HOT_DAYS") {
			t.Errorf("%q: 다른 설정 오류와 함께 알린다: %v", bad, err)
		}
	}
	for _, good := range []string{"bolt://localhost:7689", "bolt+s://h", "bolt+ssc://h:1", "neo4j://h", "neo4j+s://h:7687", "neo4j+ssc://h", "off", "OFF"} {
		if _, err := Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_GRAPH_URI": good})); err != nil {
			t.Errorf("%q 는 받는다: %v", good, err)
		}
	}
}

// API-13: OpenSearch time_zone 에는 IANA 이름을 넘긴다 — 지금 시각의 고정 오프셋을 넘기면 DST 를 넘는 date math(now-90d/d)가
// 한 시간 어긋난다. 이름이 없는 시간대(이름을 못 찾은 time.Local)만 그 시각의 오프셋이다.
func TestQueryTimeZoneUsesIANAName(t *testing.T) {
	winter, summer := time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"America/Los_Angeles", "Asia/Seoul", "UTC"} {
		cfg, err := Load(env(map[string]string{"META_REPO_ROOT": "/m", "MEMORY_TIME_ZONE": name}))
		if err != nil {
			t.Fatal(err)
		}

		w, s := cfg.QueryTimeZone(winter), cfg.QueryTimeZone(summer)

		if w != name || s != name {
			t.Errorf("%s: 겨울 %q 여름 %q", name, w, s)
		}
	}
	unnamed := Config{Location: time.FixedZone("", -8*60*60)}
	if got := unnamed.QueryTimeZone(winter); got != "-08:00" {
		t.Errorf("이름 없는 시간대는 오프셋: %q", got)
	}
	// time.Local 의 이름은 "Local" 이거나 TZ 값 그대로(zoneinfo 경로일 수 있다)라 넘기지 않는다
	if got, want := (Config{Location: time.Local}).QueryTimeZone(winter), winter.In(time.Local).Format("-07:00"); got != want {
		t.Errorf("time.Local 은 오프셋: %q, want %q", got, want)
	}
}

// API-13: MEMORY_TIME_ZONE 이 비면 이 머신의 시간대 이름을 TZ(":Asia/Seoul" 꼴, zoneinfo 경로도)나 /etc/localtime 링크에서 찾는다.
func TestMachineZoneName(t *testing.T) {
	noLink := func(string) (string, error) { return "", errors.New("no link") }
	link := func(target string) func(string) (string, error) {
		return func(string) (string, error) { return target, nil }
	}
	cases := []struct {
		name     string
		tz       string
		readlink func(string) (string, error)
		want     string
	}{
		{"TZ 이름", "Asia/Seoul", noLink, "Asia/Seoul"},
		{"TZ 의 콜론 꼴", ":America/New_York", noLink, "America/New_York"},
		{"TZ 의 zoneinfo 경로", "/usr/share/zoneinfo/Europe/Berlin", noLink, "Europe/Berlin"},
		{"macOS 링크", "", link("/var/db/timezone/zoneinfo/Asia/Seoul"), "Asia/Seoul"},
		{"Debian 상대 링크", "", link("../usr/share/zoneinfo/Asia/Tokyo"), "Asia/Tokyo"},
		{"zoneinfo 가 아닌 링크", "", link("/etc/custom-zone"), ""},
		{"링크 없음", "", noLink, ""},
	}
	for _, c := range cases {
		getenv := env(map[string]string{"TZ": c.tz})

		if got := machineZoneName(getenv, c.readlink); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// API-13: 찾은 이름은 그 시간대가 이 프로세스의 time.Local 과 같을 때만 쓴다 (TZ="" 처럼 Go 가 다르게 읽은 경우는 time.Local 그대로).
func TestMachineLocationKeepsLocalWhenNameDisagrees(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("tzdata 없음")
	}
	noLink := func(string) (string, error) { return "", errors.New("no link") }

	same := machineLocation(env(map[string]string{"TZ": "America/Los_Angeles"}), noLink, la)
	differ := machineLocation(env(map[string]string{"TZ": "Asia/Seoul"}), noLink, la)
	unknown := machineLocation(env(map[string]string{"TZ": "Mars/Base"}), noLink, la)

	if same.String() != "America/Los_Angeles" || differ != la || unknown != la {
		t.Errorf("same=%v differ=%v unknown=%v", same, differ, unknown)
	}
}
