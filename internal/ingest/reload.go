package ingest

import (
	"context"
	"strings"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/model"
)

// Corrupted 는 파싱하지 못한 줄이 있는 cold store 키다.
type Corrupted struct {
	Key   string
	Lines []int
}

// ReloadReport 는 재적재 결과다.
type ReloadReport struct {
	Chunks    int
	Corrupted []Corrupted
}

// Reload 는 cold store 의 청크 JSONL 을 그대로 _bulk 로 다시 넣는다. 매핑을 바꾸거나 나중에 임베딩을 붙일 때
// 원본 파싱 없이 전체 재적재하는 길이다. 깨진 줄은 건너뛰고 보고하며, 나머지 키는 계속 적재한다.
func Reload(ctx context.Context, store cold.Store, ix Indexer, team string, keep func(model.Chunk) bool) (ReloadReport, error) {
	keys, err := store.List(ctx, cold.ChunksPrefix(team))
	if err != nil {
		return ReloadReport{}, err
	}
	var report ReloadReport
	for _, key := range keys {
		if !strings.HasSuffix(key, ".jsonl") {
			continue
		}
		body, ok, err := store.Get(ctx, key)
		if err != nil {
			return report, err
		}
		if !ok {
			continue
		}
		chunks, bad := DecodeJSONL(body)
		if len(bad) > 0 {
			report.Corrupted = append(report.Corrupted, Corrupted{Key: key, Lines: bad})
		}
		if keep != nil {
			kept := chunks[:0]
			for _, c := range chunks {
				if keep(c) {
					kept = append(kept, c)
				}
			}
			chunks = kept
		}
		n, err := ix.BulkChunks(ctx, chunks)
		if err != nil {
			return report, err
		}
		report.Chunks += n
	}
	return report, nil
}
