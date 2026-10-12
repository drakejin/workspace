//go:build e2e && e2e_s3

package e2e

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/opensearch"
)

const s3Bucket = "mem-e2e"

// TestS3ColdLayer 는 cold 층 두 가지 — OpenSearch 스냅샷(repository-s3)과 cold store(raw·청크 JSONL) — 를 S3 에 두고
// tier → 복원 → 재적재를 돈다. LocalStack 스택(app/test/s3)이 필요하다: make test-e2e-s3.
func TestS3ColdLayer(t *testing.T) {
	endpoint := envOr("S3_E2E_ENDPOINT", "http://localhost:4576")
	osURL := envOr("S3_E2E_OPENSEARCH_URL", "http://localhost:9203")
	t.Setenv("AWS_ACCESS_KEY_ID", "test") // LocalStack 은 아무 자격증명이나 받는다
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	ctx := context.Background()
	if err := opensearch.New(osURL).Do(ctx, http.MethodGet, "/_cluster/health", nil, nil, nil); err != nil {
		t.Fatalf("S3 e2e OpenSearch(%s) 가 없다 — make test-e2e-s3 로 돌린다: %v", osURL, err)
	}
	client := newS3(t, endpoint)
	ensureBucket(t, client)

	cfg := newConfig(t, map[string]string{
		"OPENSEARCH_URL":           osURL,
		"COLD_SNAPSHOT_TYPE":       "s3",
		"COLD_SNAPSHOT_S3_BUCKET":  s3Bucket,
		"COLD_STORE_S3_BUCKET":     s3Bucket,
		"AWS_ENDPOINT_URL_S3":      endpoint,
		"COLD_STORE_S3_PATH_STYLE": "true",
		"AWS_REGION":               "us-east-1",
		"MEMORY_GRAPH_URI":         "off",
	})
	m := newMCP(t, cfg)
	const legacy = "docs/repository/ledger/adr/0003-legacy.md"
	write(t, cfg.MetaRepoRoot, legacy, "---\nrepo: ledger\ndate: "+daysAgo(hotDays+70)+"\n---\n# ADR-0003 레거시 원장\nLedgerS3Token 은 월말에 닫는다.\n")
	write(t, cfg.MetaRepoRoot, "docs/specs/fresh.md", "---\ndate: "+daysAgo(1)+"\n---\n# 새 스펙\nFreshS3Token\n")
	if _, err := m.indexer.IngestPath(ctx, "docs", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.store.Location(), "s3://"+s3Bucket+"/") {
		t.Fatalf("cold store = %s", m.store.Location())
	}

	decisions, err := cold.Tier(ctx, m.os, m.store, cfg, cold.TierOptions{Team: cfg.DefaultTeam})
	if err != nil {
		t.Fatal(err)
	}
	var index string
	for _, d := range decisions {
		if d.Action == cold.TierMoved {
			index = d.Index
		}
	}
	if index == "" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if n := countObjects(t, client, "snapshots/"); n == 0 {
		t.Error("스냅샷이 S3 에 있어야 한다")
	}
	if raw, ok, err := m.store.Get(ctx, cold.RawKey(cfg.DefaultTeam, legacy)); err != nil || !ok || !strings.Contains(raw, "LedgerS3Token") {
		t.Errorf("raw 원본이 S3 cold store 에 있어야 한다: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(cfg.MetaRepoRoot + "/" + legacy); !os.IsNotExist(err) {
		t.Error("hot 원본은 cold 로 넘어갔다")
	}

	if text, isErr := m.call(t, "restore_cold", map[string]any{"index": index, "confirmed": true}); isErr {
		t.Fatalf("restore = %s", text)
	}
	if got := searchPaths(t, cfg, "LedgerS3Token", false); len(got) != 1 {
		t.Errorf("S3 스냅샷에서 복원 = %v", got)
	}

	_ = m.os.Do(ctx, http.MethodDelete, "/mem-"+cfg.DefaultTeam+"-*", nil, nil, nil)
	reloaded, err := ingest.Reload(ctx, m.store, m.indexer, cfg.DefaultTeam, func(model.Chunk) bool { return true })
	if err != nil || reloaded.Chunks == 0 {
		t.Fatalf("reload = %+v %v", reloaded, err)
	}
	if got := searchPaths(t, cfg, "FreshS3Token", false); len(got) != 1 {
		t.Errorf("S3 청크 JSONL 에서 재적재 = %v", got)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newS3(t *testing.T, endpoint string) *s3.Client {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func ensureBucket(t *testing.T, c *s3.Client) {
	t.Helper()
	_, err := c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(s3Bucket)})
	var owned *types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &owned) {
		t.Fatalf("bucket %s: %v", s3Bucket, err)
	}
}

func countObjects(t *testing.T, c *s3.Client, prefix string) int {
	t.Helper()
	out, err := c.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(s3Bucket), Prefix: aws.String(prefix)})
	if err != nil {
		t.Fatal(err)
	}
	return len(out.Contents)
}
