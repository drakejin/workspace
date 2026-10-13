package document

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
)

func TestFactsFromPath(t *testing.T) {
	cases := []struct {
		path string
		repo string
		typ  model.DocType
	}{
		{"docs/repository/attribution/adr/0001-x.md", "attribution", model.DocADR},
		{"main/serving/docs/prd/launch.md", "serving", model.DocPRD},
		{"main/serving/docs/plans/x.md", "serving", model.DocPlan},
		{"docs/specs/refund/plan.md", "", model.DocPlan},
		{"docs/repository/serving/notes.md", "serving", model.DocNote},
		{"GUIDE.md", "", model.DocGuide},
		{"CLAUDE.md", "", model.DocGuide},
		{"docs/specs/memory-mcp/design.md", "", model.DocSpec},
		{"docs/specs/memory-mcp/INDEX.md", "", model.DocGuide},
		{"README.md", "", model.DocGuide},
		{"docs/guides/howto.md", "", model.DocGuide},
		{"docs/knowledge/active-ad.md", "", model.DocKnowledge},
		{"docs/feature/attribution/notes.md", "", model.DocNote},
		{"docs/feature/x/ADR-3.md", "", model.DocADR},
	}
	for _, c := range cases {
		got := FactsFromPath(c.path)
		if got.Repo != c.repo || got.DocType != c.typ {
			t.Errorf("%s → %+v, want repo=%q type=%s", c.path, got, c.repo, c.typ)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		typ  model.DocType
		path string
		want model.Kind
	}{
		{model.DocKnowledge, "docs/knowledge/a.md", model.KindKnowledge},
		{model.DocGuide, "README.md", model.KindKnowledge},
		{model.DocGuide, "GUIDE.md", model.KindKnowledge},
		{model.DocADR, "main/svc/docs/adr/1.md", model.KindKnowledge},
		{model.DocADR, "docs/repository/svc/adr/1.md", model.KindEpisodic},
		{model.DocNote, "docs/feature/a.md", model.KindEpisodic},
	}
	for _, c := range cases {
		if got := KindOf(c.typ, c.path); got != c.want {
			t.Errorf("KindOf(%s,%s) = %s", c.typ, c.path, got)
		}
	}
}

func TestResolveMetadata(t *testing.T) {
	meta, err := ResolveMetadata(markdown.Frontmatter{"team": "Ads-Data", "repo": "x", "doc_type": "PRD", "date": "2026-08-01", "supersedes": "docs/old.md"}, "docs/repository/attribution/adr/a.md", "default")
	if err != nil {
		t.Fatal(err)
	}
	want := Metadata{Team: "ads-data", Repo: "x", DocType: model.DocPRD, Kind: model.KindEpisodic, DeclaredTime: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), DeclaredDay: "2026-08-01", Supersedes: "docs/old.md"}
	if !reflect.DeepEqual(meta, want) {
		t.Errorf("meta = %+v", meta)
	}
	meta, _ = ResolveMetadata(markdown.Frontmatter{"doc_type": "weird", "date": "not a date"}, "docs/specs/a.md", "default")
	if meta.Team != "default" || meta.DocType != model.DocSpec || !meta.DeclaredTime.IsZero() {
		t.Errorf("fallback = %+v", meta)
	}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	meta, _ = ResolveMetadata(markdown.Frontmatter{"event_time": when}, "docs/a.md", "t")
	if !meta.DeclaredTime.Equal(when) {
		t.Errorf("time.Time = %v", meta.DeclaredTime)
	}
	if _, err := ResolveMetadata(markdown.Frontmatter{"team": "광고 팀"}, "docs/a.md", "t"); err == nil {
		t.Error("잘못된 team 은 거부")
	}
}

func baseInput(content string) Input {
	return Input{
		Workspace: "ws", SourcePath: "docs/repository/attribution/adr/0007-dedup.md", Content: content, DefaultTeam: "ads",
		FallbackTime: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), GitSHA: "abc", Now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), MtimeMs: 1000, Size: 42,
	}
}

func TestBuild(t *testing.T) {
	b, err := Build(baseInput("---\ndate: 2026-10-01\nsupersedes: docs/old.md\n---\n# 중복 제거\n클릭 기준"))
	if err != nil {
		t.Fatal(err)
	}
	c := b.Chunks[0]
	if c.Workspace != "ws" || c.Team != "ads" || c.Repo != "attribution" || c.DocType != model.DocADR || c.Kind != model.KindEpisodic ||
		c.EventTime != "2026-10-01T00:00:00Z" || c.GitSHA != "abc" || c.HeaderPath != "중복 제거" || c.Title != "중복 제거" ||
		c.MtimeMs != 1000 || c.Size != 42 || c.Supersedes != "docs/old.md" || c.IngestedAt != "2026-10-12T00:00:00Z" {
		t.Errorf("chunk = %+v", c)
	}
	if len(c.ContentSHA) != 64 || c.ContentSHA == c.BodySHA {
		t.Error("sha")
	}
}

func TestBuildDeterministicAndDistill(t *testing.T) {
	a, _ := Build(baseInput("---\ndate: 2026-01-01\n---\n# A\nx"))
	b, _ := Build(baseInput("---\ndate: 2026-02-01\n---\n# A\nx"))
	if a.BodySHA != b.BodySHA || a.ContentSHA == b.ContentSHA || a.Chunks[0].ID != b.Chunks[0].ID {
		t.Error("frontmatter 만 바뀌면 body sha·id 는 그대로")
	}
	in := baseInput("# A\ny")
	in.Distilled = func(team, sha string) (model.Distilled, bool) {
		return model.Distilled{Summary: "요약", Entities: []string{"concept:x"}}, team == "ads" && len(sha) == 64
	}
	in.TeamOverride = "ads"
	c, _ := Build(in)
	if c.Chunks[0].Summary != "요약" || c.EventTime != in.FallbackTime {
		t.Errorf("distill/fallback = %+v", c.Chunks[0])
	}
	if ChunkID("w1", "t", "p", 0) == ChunkID("w2", "t", "p", 0) {
		t.Error("workspace 가 다르면 id 도 다르다")
	}
	if _, err := Build(Input{SourcePath: "docs/a.md", Content: "---\nteam: Bad Team\n---\nx", DefaultTeam: "t"}); err == nil {
		t.Error("잘못된 team")
	}
}

func TestLastCommit(t *testing.T) {
	dir := t.TempDir()
	if got := LastCommit(context.Background(), filepath.Join(dir, "none.md")); got.SHA != "" {
		t.Error("git 밖이면 빈 값")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.md")
	run("commit", "-q", "-m", "a")
	got := LastCommit(context.Background(), filepath.Join(dir, "a.md"))
	if len(got.SHA) != 40 || got.CommittedAt.IsZero() {
		t.Errorf("LastCommit = %+v", got)
	}
}

func TestLinks(t *testing.T) {
	content := "---\nsupersedes: docs/knowledge/old.md\nderived_from:\n  - path: docs/specs/a.md\n    why: 근거 조사\n  - path: docs/specs/no-why.md\nrelates:\n  - {path: docs/feature/x.md, why: 같은 주제}\n---\n" +
		"# T\n[상대](../specs/b.md#h) [절대](/docs/c.md) [외부](https://x.io/a.md) [그림](img.png) [자기](new.md) [밖](../../../out.md)"
	links := Links(content, "docs/knowledge/new.md")
	got := map[string]model.LinkType{}
	for _, l := range links {
		got[l.To] = l.Type
		if (l.Type == model.LinkDerivedFrom || l.Type == model.LinkRelates) && l.Why == "" {
			t.Errorf("why 없는 링크가 남았다: %+v", l)
		}
	}
	want := map[string]model.LinkType{
		"docs/knowledge/old.md": model.LinkSupersedes, "docs/specs/a.md": model.LinkDerivedFrom, "docs/feature/x.md": model.LinkRelates,
		"docs/specs/b.md": model.LinkLinksTo, "docs/c.md": model.LinkLinksTo,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v", got)
	}
	if Title("본문만", "docs/a/b.md") != "b" || Title("# 제목 \n", "x.md") != "제목" {
		t.Error("Title")
	}
}

func TestTopicAndProject(t *testing.T) {
	cases := []struct {
		rel, fmTopic, fmProject, wantTopic, wantProject string
	}{
		{"docs/feature/payment-retry/a.md", "", "", "payment-retry", ""},
		{"docs/specs/Onboarding/plan.md", "", "", "onboarding", ""},
		{"docs/issues/ISSUE-7/README.md", "", "", "issue-7", ""},
		{"docs/repository/alpha-api/adr/0001.md", "", "", "", "alpha-api"},
		{"docs/repository/alpha-api/adr/0001.md", "Payments Ledger", "", "payments-ledger", "alpha-api"},
		{"docs/knowledge/x.md", "", "beta-web", "", "beta-web"}, // frontmatter project 는 repo 와 같은 뜻
		{"docs/feature/payments/INDEX.md", "", "", "payments", ""},
		{"README.md", "", "", "", ""},
	}
	for _, c := range cases {
		fm := markdown.Frontmatter{}
		if c.fmTopic != "" {
			fm["topic"] = c.fmTopic
		}
		if c.fmProject != "" {
			fm["project"] = c.fmProject
		}
		m, err := ResolveMetadata(fm, c.rel, "t")
		if err != nil || m.Topic != c.wantTopic || m.Repo != c.wantProject {
			t.Errorf("%s: topic=%q project=%q err=%v, want %q %q", c.rel, m.Topic, m.Repo, err, c.wantTopic, c.wantProject)
		}
	}
	b, err := Build(Input{Workspace: "ws", SourcePath: "docs/feature/payments/a.md", Content: "# A\n본문", DefaultTeam: "t", Now: time.Now()})
	if err != nil || b.Chunks[0].Topic != "payments" || b.Chunks[0].IndexVersion != model.IndexVersion {
		t.Errorf("chunk topic/version = %+v %v", b.Chunks[0], err)
	}
}

func TestEventDay(t *testing.T) {
	seoul := time.FixedZone("KST", 9*3600)
	la := time.FixedZone("PDT", -7*3600)
	// frontmatter 날짜는 적힌 그대로의 날
	for _, loc := range []*time.Location{seoul, la, nil} {
		b, err := Build(Input{Workspace: "ws", SourcePath: "docs/a.md", Content: "---\ndate: 2026-10-06\n---\n# A", DefaultTeam: "t", Now: time.Now(), Location: loc})
		if err != nil || b.Chunks[0].EventDay != "2026-10-06" {
			t.Errorf("loc=%v frontmatter day = %q %v", loc, b.Chunks[0].EventDay, err)
		}
	}
	// 커밋·mtime 시각은 설정한 시간대의 날 — KST 오전 8시 커밋은 UTC 로는 전날이지만 그날이다
	commit := time.Date(2026, 10, 12, 23, 30, 0, 0, time.UTC)
	b, _ := Build(Input{Workspace: "ws", SourcePath: "docs/b.md", Content: "# B", DefaultTeam: "t", Now: time.Now(), FallbackTime: commit, Location: seoul})
	if b.Chunks[0].EventDay != "2026-10-13" {
		t.Errorf("KST day = %q", b.Chunks[0].EventDay)
	}
	b, _ = Build(Input{Workspace: "ws", SourcePath: "docs/b.md", Content: "# B", DefaultTeam: "t", Now: time.Now(), FallbackTime: commit})
	if b.Chunks[0].EventDay != "2026-10-12" {
		t.Errorf("시간대가 없으면 UTC = %q", b.Chunks[0].EventDay)
	}
	// frontmatter 에 시각까지 적었으면 그 시각을 시간대로 본다
	b, _ = Build(Input{Workspace: "ws", SourcePath: "docs/c.md", Content: "---\ndate: 2026-10-06T23:30:00Z\n---\n# C", DefaultTeam: "t", Now: time.Now(), Location: seoul})
	if b.Chunks[0].EventDay != "2026-10-07" {
		t.Errorf("frontmatter 시각 = %q", b.Chunks[0].EventDay)
	}
}
