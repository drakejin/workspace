package cold

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/drakejin/workspace/app/internal/model"
)

// EncodeChunks 는 문서 하나의 청크를 JSONL 로 쓴다 (id 포함 — 재적재 때 같은 _id 로 덮어쓴다).
func EncodeChunks(chunks []model.Chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		line, _ := json.Marshal(chunkRow{ID: c.ID, Chunk: c})
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// DecodeChunks 는 청크 JSONL 을 읽는다. 깨진 줄은 건너뛰고 줄 번호(1부터)를 돌려준다.
func DecodeChunks(body string) ([]model.Chunk, []int) {
	var (
		chunks []model.Chunk
		bad    []int
	)
	for i, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row chunkRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			bad = append(bad, i+1)
			continue
		}
		row.Chunk.ID = row.ID
		chunks = append(chunks, row.Chunk)
	}
	return chunks, bad
}

type chunkRow struct {
	ID string `json:"id"`
	model.Chunk
}

// Archived 는 cold store 에 보관된 문서의 청크다. 보관된 적 없으면 ok=false.
func Archived(ctx context.Context, store Store, team, sourcePath string) ([]model.Chunk, bool, error) {
	body, ok, err := store.Get(ctx, ChunksKey(team, sourcePath))
	if err != nil || !ok {
		return nil, false, err
	}
	chunks, _ := DecodeChunks(body)
	if len(chunks) == 0 {
		return nil, false, nil
	}
	return chunks, true, nil
}
