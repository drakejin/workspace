package scripts

import (
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
)

// launchdEnv 는 memory_launchd_env_xml 이 vars 환경에서 그리는 EnvironmentVariables 를 key→값으로 돌려준다 (원문도 함께).
func launchdEnv(t *testing.T, vars map[string]string) (map[string]string, string) {
	t.Helper()
	sb := newSandbox(t)
	sb.copyTo(sb.dir, "scripts/memory-common.sh")
	common := filepath.Join(sb.dir, "scripts", "memory-common.sh")
	r := sb.mustBash(vars, "-c", `source "$1"; memory_launchd_env_xml`, "_", common)
	return parseDict(t, r.stdout), r.stdout
}

// parseDict 는 plist <dict> 안쪽(<key>K</key><string>V</string> 의 반복)을 읽는다.
func parseDict(t *testing.T, inner string) map[string]string {
	t.Helper()
	var dict struct {
		Items []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	}
	if err := xml.Unmarshal([]byte("<dict>"+inner+"</dict>"), &dict); err != nil {
		t.Fatalf("EnvironmentVariables 가 XML 이 아니다: %v\n%s", err, inner)
	}
	out := map[string]string{}
	for i := 0; i+1 < len(dict.Items); i += 2 {
		k, v := dict.Items[i], dict.Items[i+1]
		if k.XMLName.Local != "key" || v.XMLName.Local != "string" {
			t.Fatalf("key/string 쌍이 아니다: %s/%s\n%s", k.XMLName.Local, v.XMLName.Local, inner)
		}
		out[k.Value] = v.Value
	}
	return out
}

// DEAD-1: 이름에 숫자가 든 cold S3 설정(COLD_STORE_S3_BUCKET …)도 launchd 잡에 들어가야 한다 —
// 빠지면 명령줄로 준 S3 설정을 데몬이 모르고 로컬 cold store 에 쓴다.
func TestLaunchdEnvKeepsSettingsWithDigits(t *testing.T) {
	want := map[string]string{
		"COLD_STORE_S3_BUCKET":     "store-bucket",
		"COLD_SNAPSHOT_S3_BUCKET":  "snap-bucket",
		"COLD_STORE_S3_PREFIX":     "pfx",
		"COLD_STORE_S3_PATH_STYLE": "true",
		"AWS_ENDPOINT_URL_S3":      "http://localhost:4566",
		"COLD_STORE_DIR":           "/x",
		"MEMORY_HOT_DAYS":          "30",
		"MEMORY_TEAM_ORGS":         "a=x&y<z>",
		"META_REPO_ROOT":           "/tmp/a&b",
	}
	got, _ := launchdEnv(t, want)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["PATH"] == "" {
		t.Errorf("PATH 가 없다: %v", got)
	}
}

// OPS-6: 비밀은 launchd plist 에 평문으로 쓰지 않는다 — NEO4J_* 는 포트만, 이름에 PASSWORD·SECRET·TOKEN 이 든 것은 모두 뺀다.
func TestLaunchdEnvDropsSecretsAndForeignNames(t *testing.T) {
	dropped := map[string]string{
		"NEO4J_PASSWORD":        "neo-pass-1",
		"NEO4J_AUTH":            "neo4j/neo-pass-2",
		"NEO4J_USER":            "neo-user-3",
		"MEMORY_GRAPH_PASSWORD": "graph-pass-4",
		"MEMORY_API_TOKEN":      "token-5",
		"COLD_STORE_SECRET_KEY": "secret-6",
		"AWS_SECRET_ACCESS_KEY": "aws-secret-7",
		"AWS_SESSION_TOKEN":     "aws-token-8",
		"GITHUB_TOKEN":          "gh-token-9",
		"UNRELATED":             "other-10",
	}
	kept := map[string]string{
		"NEO4J_BOLT_PORT":   "7689",
		"NEO4J_HTTP_PORT":   "7476",
		"MEMORY_GRAPH_USER": "neo4j",
		"AWS_REGION":        "ap-northeast-2",
		"AWS_PROFILE":       "dev",
	}
	vars := map[string]string{}
	for k, v := range dropped {
		vars[k] = v
	}
	for k, v := range kept {
		vars[k] = v
	}
	got, raw := launchdEnv(t, vars)
	for k, v := range dropped {
		if _, ok := got[k]; ok {
			t.Errorf("%s 가 plist 에 들어갔다", k)
		}
		if strings.Contains(raw, v) {
			t.Errorf("%s 의 값이 plist 에 남았다", k)
		}
	}
	for k, v := range kept {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
