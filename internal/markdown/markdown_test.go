package markdown

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSplitFrontmatter(t *testing.T) {
	fm, body := SplitFrontmatter("---\nteam: ads\ndate: 2026-10-01\n---\n# 본문")
	if fm["team"] != "ads" || body != "# 본문" {
		t.Errorf("fm=%v body=%q", fm, body)
	}
	if d, ok := fm["date"].(time.Time); !ok || d.Day() != 1 {
		t.Errorf("date = %#v", fm["date"])
	}
	for _, src := range []string{"# hi", "---\nteam: ads\n# x", "---\n: : [\n---\nbody", "---\n- a\n---\nbody"} {
		fm, body := SplitFrontmatter(src)
		if len(fm) != 0 || body != src {
			t.Errorf("%q → fm=%v body=%q", src, fm, body)
		}
	}
	if fm, body := SplitFrontmatter("---\n---\nbody"); len(fm) != 0 || body != "body" {
		t.Errorf("빈 frontmatter: %v %q", fm, body)
	}
}

func paths(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.HeaderPath
	}
	return out
}

func TestChunkHierarchy(t *testing.T) {
	md := "# ADR-7\nintro\n## 결정\nA를 고른다\n### 대안\nB는 버린다\n## 결과\n비워둠"
	chunks := ChunkMarkdown(md, "adr-7", 0)
	want := []string{"ADR-7", "ADR-7 > 결정", "ADR-7 > 결정 > 대안", "ADR-7 > 결과"}
	if got := paths(chunks); !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v", got)
	}
	if !strings.Contains(chunks[2].Text, "B는 버린다") {
		t.Errorf("text = %q", chunks[2].Text)
	}
}

func TestChunkRootAndSkips(t *testing.T) {
	chunks := ChunkMarkdown("머리말\n\n# 제목\n본문", "file", 0)
	if chunks[0] != (Chunk{HeaderPath: "file", Text: "머리말"}) {
		t.Errorf("root = %+v", chunks[0])
	}
	if got := paths(ChunkMarkdown("# A\na\n### C\nc\n## B\nb", "x", 0)); !reflect.DeepEqual(got, []string{"A", "A > C", "A > B"}) {
		t.Errorf("레벨 건너뛰기 = %v", got)
	}
	if len(ChunkMarkdown("\n\n", "x", 0)) != 0 {
		t.Error("빈 섹션은 버린다")
	}
}

func TestChunkCodeFence(t *testing.T) {
	chunks := ChunkMarkdown("# 설치\n```bash\n# 주석\nmake start\n```\n끝", "x", 0)
	if len(chunks) != 1 || !strings.Contains(chunks[0].Text, "# 주석") {
		t.Errorf("코드 블록 안 # 은 헤더가 아니다: %+v", chunks)
	}
	tilde := ChunkMarkdown("# T\n~~~\n# not header\n~~~\n## H\nx", "x", 0)
	if got := paths(tilde); !reflect.DeepEqual(got, []string{"T", "T > H"}) {
		t.Errorf("~~~ fence = %v", got)
	}
}

func TestChunkLongSectionSplitsOutsideFence(t *testing.T) {
	code := "```\nline1\n\nline2\n```"
	md := "# T\n" + strings.Repeat("a", 30) + "\n\n" + code + "\n\n" + strings.Repeat("b", 30)
	chunks := ChunkMarkdown(md, "x", 40)
	if len(chunks) < 2 {
		t.Fatalf("나뉘어야 한다: %d", len(chunks))
	}
	found := false
	for _, c := range chunks {
		if c.HeaderPath != "T" {
			t.Errorf("header = %s", c.HeaderPath)
		}
		if strings.Contains(c.Text, "line1\n\nline2") {
			found = true
		}
	}
	if !found {
		t.Error("코드 블록은 자르지 않는다")
	}
}
