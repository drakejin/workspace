// Package config 는 메모리 층의 설정을 환경변수에서 읽고 검증한다.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/naming"
)

// Snapshot 은 cold 인덱스 스냅샷 repository 좌표다.
type Snapshot struct {
	Repository string
	Type       string // fs | s3
	FSLocation string
	S3Bucket   string
}

// ColdStore 는 raw 원본과 청크 JSONL 을 두는 곳이다. S3Bucket 이 비면 로컬 디렉토리.
type ColdStore struct {
	Dir      string
	S3Bucket string
	S3Prefix string
	Region   string
	// S3Endpoint·S3PathStyle 은 S3 호환 저장소(LocalStack, MinIO 등)를 쓸 때만. 비우면 AWS.
	S3Endpoint  string
	S3PathStyle bool
}

// Graph 는 knowledge graph(Neo4j) 좌표다. URI 가 비면 그래프 단계를 끈다.
type Graph struct {
	URI      string
	User     string
	Password string
	Database string
}

// Reconcile 은 데몬 주기와 스캔 게이트다.
type Reconcile struct {
	Interval       time.Duration
	Debounce       time.Duration
	MaxFileBytes   int64
	SanityMaxFiles int
}

// Judge 는 충돌 판정 게이트다.
type Judge struct {
	Enabled    bool
	WindowDays float64
	// MinScore 는 more_like_this 점수 / 자기 자신 점수. 짧은 문서는 같은 주제여도 0.4 안팎이 나온다.
	MinScore float64
}

// Distill 은 증류 큐다. 문서 본문이 LLM 으로 가므로 기본은 꺼짐.
type Distill struct {
	Enabled     bool
	Concurrency int
}

// LLM 은 판정·증류가 부르는 로컬 claude CLI 다.
type LLM struct {
	ClaudeBin string
	Model     string
	Timeout   time.Duration
}

// Config 는 메모리 층 전체 설정이다.
type Config struct {
	OpenSearchURL string
	MetaRepoRoot  string
	// Workspace 는 이 meta-repository 의 이름이다. 같은 OpenSearch 를 여러 meta-repo 가 써도
	// 문서 id·삭제·수렴·판정·검색이 서로 섞이지 않게 하는 격리 축이다 (vms 의 project).
	Workspace   string
	DefaultTeam string
	HotDays     int
	// Location 은 문서의 날(event_day)과 날짜 필터의 하루를 정하는 시간대다 (MEMORY_TIME_ZONE, 기본은 이 머신의 시간대).
	Location  *time.Location
	Snapshot  Snapshot
	Cold      ColdStore
	Reconcile Reconcile
	Judge     Judge
	Distill   Distill
	LLM       LLM
	Graph     Graph
	// TeamOrgs 는 팀 → org("실") 매핑이다. 같은 org 의 팀들은 mem-org-{org}-hot alias 로 함께 검색된다.
	TeamOrgs map[string]string
}

// OrgOf 는 팀이 속한 org 다. 없으면 "".
func (c Config) OrgOf(team string) string { return c.TeamOrgs[team] }

// HotWindow 는 hot/cold 경계 기간이다.
func (c Config) HotWindow() time.Duration {
	return time.Duration(c.HotDays) * 24 * time.Hour
}

// QueryTimeZone 은 at 시각의 Location 오프셋(+09:00)이다 — OpenSearch 질의의 time_zone 에 넘긴다.
func (c Config) QueryTimeZone(at time.Time) string {
	loc := c.Location
	if loc == nil {
		loc = time.UTC
	}
	return at.In(loc).Format("-07:00")
}

// FromEnv 는 프로세스 환경변수로 Load 한다.
func FromEnv() (Config, error) { return Load(os.Getenv) }

// Load 는 getenv 로 설정을 읽는다. 빈 문자열은 설정되지 않은 것으로 본다. 잘못된 값은 모아서 한 번에 실패한다.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	root := p.str("META_REPO_ROOT", "")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("작업 디렉토리를 알 수 없습니다: %w", err)
		}
		root = wd
	}
	root, _ = filepath.Abs(root)

	cfg := Config{
		OpenSearchURL: p.str("OPENSEARCH_URL", "http://localhost:9202"),
		MetaRepoRoot:  root,
		Workspace:     p.str("MEMORY_WORKSPACE", workspaceName(root)),
		DefaultTeam:   p.str("MEMORY_DEFAULT_TEAM", "default"),
		HotDays:       p.posInt("MEMORY_HOT_DAYS", 60),
		Location:      p.location("MEMORY_TIME_ZONE"),
		Snapshot: Snapshot{
			Repository: p.str("COLD_SNAPSHOT_REPOSITORY", "mem-cold"),
			Type:       p.str("COLD_SNAPSHOT_TYPE", "fs"),
			FSLocation: p.str("COLD_SNAPSHOT_FS_LOCATION", "/usr/share/opensearch/snapshots"),
			S3Bucket:   p.str("COLD_SNAPSHOT_S3_BUCKET", ""),
		},
		Cold: ColdStore{
			Dir:      resolveUnder(root, p.str("COLD_STORE_DIR", ".cache/cold-store")),
			S3Bucket: p.str("COLD_STORE_S3_BUCKET", ""),
			S3Prefix: p.str("COLD_STORE_S3_PREFIX", "memory"),
			Region:   p.str("AWS_REGION", "ap-northeast-2"),
			// AWS SDK 표준 변수(AWS_ENDPOINT_URL_S3)를 그대로 쓴다
			S3Endpoint:  p.str("AWS_ENDPOINT_URL_S3", ""),
			S3PathStyle: p.flag("COLD_STORE_S3_PATH_STYLE", false),
		},
		Reconcile: Reconcile{
			Interval:       time.Duration(p.posInt("MEMORY_RECONCILE_INTERVAL_SECONDS", 60)) * time.Second,
			Debounce:       time.Duration(p.nonNegInt("MEMORY_RECONCILE_DEBOUNCE_MS", 3000)) * time.Millisecond,
			MaxFileBytes:   int64(p.posInt("MEMORY_MAX_FILE_BYTES", 2*1024*1024)),
			SanityMaxFiles: p.posInt("MEMORY_SANITY_MAX_FILES", 5000),
		},
		Judge: Judge{
			Enabled:    p.flag("MEMORY_JUDGE_ENABLED", true),
			WindowDays: p.posFloat("MEMORY_JUDGE_WINDOW_DAYS", 7),
			MinScore:   p.unitFloat("MEMORY_JUDGE_MIN_SCORE", 0.3),
		},
		Distill: Distill{
			Enabled:     p.flag("MEMORY_DISTILL_ENABLED", false),
			Concurrency: p.posInt("MEMORY_DISTILL_CONCURRENCY", 2),
		},
		LLM: LLM{
			ClaudeBin: p.str("MEMORY_CLAUDE_BIN", "claude"),
			Model:     p.str("MEMORY_LLM_MODEL", "opus"),
			Timeout:   time.Duration(p.posInt("MEMORY_LLM_TIMEOUT_MS", 120_000)) * time.Millisecond,
		},
		Graph: Graph{
			URI:      p.str("MEMORY_GRAPH_URI", "bolt://localhost:7689"),
			User:     p.str("MEMORY_GRAPH_USER", ""),
			Password: p.str("MEMORY_GRAPH_PASSWORD", ""),
			Database: p.str("MEMORY_GRAPH_DATABASE", "neo4j"),
		},
		TeamOrgs: p.teamOrgs("MEMORY_TEAM_ORGS"),
	}
	if strings.EqualFold(cfg.Graph.URI, "off") {
		cfg.Graph.URI = ""
	}

	if u, err := url.Parse(cfg.OpenSearchURL); err != nil || u.Scheme == "" || u.Host == "" {
		p.fail("OPENSEARCH_URL: URL 이 아닙니다: %q", cfg.OpenSearchURL)
	}
	if !naming.ValidName(cfg.Workspace) {
		p.fail("MEMORY_WORKSPACE: 소문자/숫자/하이픈만: %q", cfg.Workspace)
	}
	if err := naming.ValidateTeam(cfg.DefaultTeam); err != nil {
		p.fail("MEMORY_DEFAULT_TEAM: %v", err)
	}
	switch cfg.Snapshot.Type {
	case "fs":
	case "s3":
		if cfg.Snapshot.S3Bucket == "" {
			p.fail("COLD_SNAPSHOT_TYPE=s3 이면 COLD_SNAPSHOT_S3_BUCKET 이 필요합니다")
		}
	default:
		p.fail("COLD_SNAPSHOT_TYPE: fs 또는 s3: %q", cfg.Snapshot.Type)
	}
	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("memory 설정이 잘못됐습니다 — %w", errors.Join(p.errs...))
	}
	return cfg, nil
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// workspaceName 은 루트 디렉토리 이름을 workspace 이름 규칙에 맞춘다.
func workspaceName(root string) string {
	name := strings.Trim(nonName.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-"), "-")
	if name == "" {
		return "workspace"
	}
	return name
}

func resolveUnder(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf(format, args...))
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

// location 은 IANA 시간대 이름(Asia/Seoul)이다. 없으면 이 머신의 시간대.
func (p *parser) location(key string) *time.Location {
	v := p.str(key, "")
	if v == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		p.fail("%s: IANA 시간대 이름이어야 합니다 (예: Asia/Seoul): %q", key, v)
		return time.Local
	}
	return loc
}

func (p *parser) posInt(key string, def int) int {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		p.fail("%s: 양의 정수여야 합니다: %q", key, v)
		return def
	}
	return n
}

func (p *parser) nonNegInt(key string, def int) int {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		p.fail("%s: 0 이상의 정수여야 합니다: %q", key, v)
		return def
	}
	return n
}

func (p *parser) posFloat(key string, def float64) float64 {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		p.fail("%s: 양수여야 합니다: %q", key, v)
		return def
	}
	return f
}

func (p *parser) unitFloat(key string, def float64) float64 {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		p.fail("%s: 0~1 사이여야 합니다: %q", key, v)
		return def
	}
	return f
}

// teamOrgs 는 "team=org,team2=org" 를 읽는다.
func (p *parser) teamOrgs(key string) map[string]string {
	out := map[string]string{}
	raw := p.str(key, "")
	if raw == "" {
		return out
	}
	for _, pair := range strings.Split(raw, ",") {
		team, org, ok := strings.Cut(strings.TrimSpace(pair), "=")
		team, org = strings.TrimSpace(team), strings.TrimSpace(org)
		if !ok || naming.ValidateTeam(team) != nil || !naming.ValidName(org) {
			p.fail("%s: \"team=org,team2=org\" 형식이어야 합니다: %q", key, pair)
			continue
		}
		out[team] = org
	}
	return out
}

func (p *parser) flag(key string, def bool) bool {
	switch strings.ToLower(p.str(key, "")) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		p.fail("%s: true/false 여야 합니다", key)
		return def
	}
}
