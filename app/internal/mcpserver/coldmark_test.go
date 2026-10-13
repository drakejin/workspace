package mcpserver

import (
	"strings"
	"testing"

	"github.com/drakejin/workspace/app/internal/cold"
)

// R2-followups-2: 합치다 멈춘 복원본(검색 alias 없음)은 "[현재 복원됨]" 이 아니라 다시 restore_cold 하라는 표시다.
func TestColdMarkTellsAPartialRestore(t *testing.T) {
	cases := []struct {
		index     cold.Index
		want, not string
	}{
		{cold.Index{Restored: true, Searchable: true}, "[현재 복원됨]", "중단"},
		{cold.Index{Restored: true}, "[복원 중단 — restore_cold 로 다시]", "현재 복원됨"},
		{cold.Index{Reopened: true}, "hot 에 다시 열림", "복원됨"},
	}
	for _, c := range cases {
		got := coldMark(c.index)
		if !strings.Contains(got, c.want) || strings.Contains(got, c.not) {
			t.Errorf("coldMark(%+v) = %q, want %q", c.index, got, c.want)
		}
	}
}
