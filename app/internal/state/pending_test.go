package state

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

// IDX-4: 판정 대기 목록은 .cache/daemon 에 문서마다 하나 남고, 비면 파일을 지운다.
func TestJudgePendingRoundTrip(t *testing.T) {
	root := t.TempDir()
	if got, err := ReadJudgePending(root); err != nil || len(got) != 0 {
		t.Fatalf("없으면 빈 목록: %v %v", got, err)
	}
	want := map[string]PendingJudgment{"docs/specs/a.md": {SourcePath: "docs/specs/a.md", Team: "t", Kind: model.KindEpisodic,
		DocType: model.DocSpec, BodySHA: "sha", EventTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}
	if err := WriteJudgePending(root, want); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadJudgePending(root); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("읽기 = %v %v", got, err)
	}
	if err := WriteJudgePending(root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(judgePendingFile(root)); !os.IsNotExist(err) {
		t.Errorf("비면 파일을 지운다: %v", err)
	}
}
