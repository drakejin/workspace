package cold

import (
	"context"
	"encoding/json"
	"strings"
)

// ArchivedContentSHA 는 cold store 에 보관된 문서의 원문 sha 다. 보관된 적 없으면 ok=false.
func ArchivedContentSHA(ctx context.Context, store Store, team, sourcePath string) (string, bool, error) {
	body, ok, err := store.Get(ctx, ChunksKey(team, sourcePath))
	if err != nil || !ok {
		return "", false, err
	}
	head := firstChunk(body)
	if head == nil {
		return "", false, nil
	}
	return head.contentSHA, true, nil
}

type chunkHead struct {
	team       string
	sourcePath string
	contentSHA string
	eventTime  string
	kind       string
}

// firstChunk 는 청크 JSONL 첫 줄의 문서 메타다 (ingest 패키지에 의존하지 않으려고 필요한 필드만 읽는다).
func firstChunk(body string) *chunkHead {
	line, _, _ := strings.Cut(body, "\n")
	var row struct {
		Team       string `json:"team"`
		SourcePath string `json:"source_path"`
		ContentSHA string `json:"content_sha"`
		EventTime  string `json:"event_time"`
		Kind       string `json:"memory_kind"`
	}
	if json.Unmarshal([]byte(line), &row) != nil || row.SourcePath == "" {
		return nil
	}
	return &chunkHead{team: row.Team, sourcePath: row.SourcePath, contentSHA: row.ContentSHA, eventTime: row.EventTime, kind: row.Kind}
}
