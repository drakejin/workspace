package document

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drakejin/workspace/app/internal/markdown"
)

// IDX-15: 제목은 코드 펜스 밖의 첫 H1 이다 — 본문 앞 코드 블록의 셸 주석이 제목이 되지 않는다 (청커와 같은 펜스 규칙).
func TestTitleIgnoresCodeFences(t *testing.T) {
	body := "```bash\n# 설치 먼저\nmake init\n```\n\n# 진짜 제목\n본문"
	if got := Title(body, "docs/specs/setup.md"); got != "진짜 제목" {
		t.Errorf("Title = %q, want 진짜 제목", got)
	}
	if got := Title("```\n# 코드뿐\n```\n본문", "docs/specs/setup.md"); got != "setup" {
		t.Errorf("H1 이 없으면 파일 이름: %q", got)
	}
	b, err := Build(Input{SourcePath: "docs/specs/setup.md", Content: body, DefaultTeam: "t", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Chunks {
		if c.Title != "진짜 제목" {
			t.Errorf("청크 제목 = %q", c.Title)
		}
	}
}

// RUN-15: frontmatter YAML 이 깨지면 쓴 글은 그대로 본문으로 색인하되(메타데이터 없이) 경고할 거리를 남긴다.
// team 값이 규칙에 어긋나면 색인하지 않고, 그 오류가 frontmatter 문제임을 알린다 — audit 가 bad-frontmatter 로 보고한다.
func TestBuildFlagsFrontmatterProblems(t *testing.T) {
	broken := "---\nteam: [unclosed\ntopic: broken\n---\n# Bad YAML\n본문"
	b, err := Build(Input{SourcePath: "docs/specs/bad-yaml.md", Content: broken, DefaultTeam: "t", Now: time.Now()})
	var fe *markdown.FrontmatterError
	if err != nil || !errors.As(b.FrontmatterErr, &fe) {
		t.Fatalf("깨진 YAML: err=%v FrontmatterErr=%v (색인은 하되 경고할 거리를 남긴다)", err, b.FrontmatterErr)
	}
	if b.Meta.Team != "t" || b.Body != broken {
		t.Errorf("메타데이터 없이 원문 전체가 본문이다: team=%q body=%q", b.Meta.Team, b.Body)
	}

	ok, err := Build(Input{SourcePath: "docs/specs/ok.md", Content: "---\nteam: t\n---\n# 정상\n", DefaultTeam: "t", Now: time.Now()})
	if err != nil || ok.FrontmatterErr != nil {
		t.Errorf("정상 frontmatter: err=%v FrontmatterErr=%v", err, ok.FrontmatterErr)
	}

	_, err = Build(Input{SourcePath: "docs/repository/beta-web/adr/0001.md", Content: "---\nteam: Beta Web\n---\n# ADR\n", DefaultTeam: "t", Now: time.Now()})
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "team 이름이 잘못됐습니다") {
		t.Errorf("잘못된 team = %v (FrontmatterError, 문구는 그대로)", err)
	}
}
