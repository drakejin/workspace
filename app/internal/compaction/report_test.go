package compaction

import (
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/model"
)

func chunk(path, header, at string, kind model.Kind, text string) model.Chunk {
	return model.Chunk{SourcePath: path, HeaderPath: header, EventTime: at, Kind: kind, Repo: "svc", DocType: model.DocADR, Text: text}
}

func TestFindExpired(t *testing.T) {
	cutoff := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	got := FindExpired([]model.Chunk{
		chunk("old.md", "A", "2026-07-01T00:00:00Z", model.KindEpisodic, ""),
		chunk("new.md", "B", "2026-10-01T00:00:00Z", model.KindEpisodic, ""),
		chunk("fact.md", "C", "2026-01-01T00:00:00Z", model.KindKnowledge, ""),
	}, cutoff)
	if len(got) != 1 || got[0].SourcePath != "old.md" {
		t.Errorf("expired = %+v (knowledge 는 노화하지 않는다)", got)
	}
}

func TestFindConflicts(t *testing.T) {
	superseded := chunk("z.md", "Z > 활성 광고 정의", "2026-09-01T00:00:00Z", model.KindEpisodic, "")
	superseded.SupersededBy = "b.md"
	got := FindConflicts([]model.Chunk{
		chunk("a.md", "ADR-1 > 활성 광고 정의", "2026-07-01T00:00:00Z", model.KindEpisodic, "노출 중인 광고"),
		chunk("b.md", "PRD > 활성 광고 정의", "2026-09-01T00:00:00Z", model.KindEpisodic, "예산이   남은 광고"),
		superseded,
		chunk("a.md", "ADR-1 > 결정", "2026-07-01T00:00:00Z", model.KindEpisodic, ""),
		chunk("b.md", "PRD > 결정", "2026-09-01T00:00:00Z", model.KindEpisodic, ""),
		chunk("c.md", "ADR-0002 주문 상태", "2026-09-01T00:00:00Z", model.KindEpisodic, ""),
		chunk("d.md", "주문 상태", "2026-09-02T00:00:00Z", model.KindEpisodic, ""),
	})
	if len(got) != 2 {
		t.Fatalf("conflicts = %+v", got)
	}
	if got[0].Topic != "주문 상태" || got[1].Topic != "활성 광고 정의" {
		t.Errorf("topics = %s, %s", got[0].Topic, got[1].Topic)
	}
	if got[1].Entries[0].SourcePath != "b.md" || got[1].Entries[0].Excerpt != "예산이 남은 광고" || len(got[1].Entries) != 2 {
		t.Errorf("entries = %+v (최신 먼저, 밀린 문서 제외)", got[1].Entries)
	}
}

func TestRender(t *testing.T) {
	md := Render(Report{
		Team: "ads", Cutoff: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
		Expired:   []ExpiredDoc{{SourcePath: "old.md", Repo: "svc", DocType: model.DocADR, EventTime: "2026-07-01T00:00:00Z"}},
		Conflicts: []Conflict{{Topic: "t", Entries: []ConflictEntry{{SourcePath: "a.md", HeaderPath: "A > t", EventTime: "2026-07-01", Excerpt: "e"}}}},
	})
	for _, want := range []string{"`old.md` (adr, svc, 2026-07-01)", "(repo 없음) — t", "다시 쓰기", "scripts/memory/run.sh tier -team"} {
		if !strings.Contains(md, want) {
			t.Errorf("render 에 %q 없음:\n%s", want, md)
		}
	}
	if !strings.Contains(Render(Report{Team: "a"}), "- 없음") {
		t.Error("빈 리포트")
	}
}

// R5-final-docs-4: 리포트의 5단계(티어 이동)는 tier 가 늘 git rm 하고 커밋해야 한다고 하지 않는다 — git 이 다루는 meta-repo 에서
// 커밋된 그대로인 원본만 git rm 하고, 커밋하지 않은 원본은 hot 에 남기고, git 이 다루지 않는 meta-repo 는 파일을 지운다.
func TestRenderTierStepDoesNotPromiseGitRm(t *testing.T) {
	md := Render(Report{Team: "a"})
	step := ""
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "5. 티어 이동") {
			step = line
		}
	}
	if step == "" {
		t.Fatalf("5단계가 없다:\n%s", md)
	}
	if strings.Contains(step, "hot 원본 제거(git rm). 이후 커밋한다") {
		t.Errorf("5단계가 조건 없이 git rm·커밋을 말한다: %q", step)
	}
	for _, want := range []string{"커밋된 그대로인 원본만 git rm", "파일을 지운다", "`(git rm)` 줄이 있으면", "`hot 에 남김`"} {
		if !strings.Contains(step, want) {
			t.Errorf("5단계에 %q 없음: %q", want, step)
		}
	}
}
