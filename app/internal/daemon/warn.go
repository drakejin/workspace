package daemon

import (
	"context"

	"github.com/drakejin/workspace/app/internal/ingest"
)

// warnIndexer 는 frontmatter YAML 이 깨진 문서를 색인할 때 경고를 남긴다. Build 는 쓴 글을 버리지 않으려고 메타데이터 없이
// 원문 전체를 본문으로 색인하므로, 알리지 않으면 team·date·supersedes 가 조용히 빠진다 (audit 는 bad-frontmatter 로 보고한다).
type warnIndexer struct {
	ingest.Indexer
	log func(format string, args ...any)
}

func (w warnIndexer) Prepare(ctx context.Context, input, teamOverride string) (ingest.Prepared, error) {
	p, err := w.Indexer.Prepare(ctx, input, teamOverride)
	if err == nil && p.Built.FrontmatterErr != nil {
		w.log("[reconcile] warn %s: frontmatter YAML 이 깨졌습니다 — 메타데이터(team·date·supersedes …) 없이 원문 전체를 본문으로 색인합니다: %v",
			p.Rel, p.Built.FrontmatterErr)
	}
	return p, err
}
