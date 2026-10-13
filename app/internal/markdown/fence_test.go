package markdown

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// IDX-10: 펜스 판정은 한 벌이고 CommonMark 를 따른다 — 닫는 줄은 여는 줄과 같은 문자, 길이 이상, info string 없음.
// 섹션 나누기(헤더)와 긴 섹션 나누기(문단)가 같은 규칙을 쓴다.
func TestChunkFenceRulesFollowCommonMark(t *testing.T) {
	para := strings.Repeat("가", 30)
	paras := strings.Join([]string{para, para, para, para}, "\n\n")
	cases := []struct {
		name     string
		md       string
		maxChars int
		want     []string // 청크별 header_path
	}{
		// ~~~ 블록 안의 ``` 줄은 펜스가 아니다 — 문단 나누기가 그 뒤를 펜스 안으로 오인하지 않는다
		{"~~~ 안의 ```", "# T\n~~~\n```\n~~~\n\n" + paras, 40, []string{"T", "T", "T", "T", "T"}},
		// info string 이 붙은 줄은 닫는 펜스가 아니다 — 그 뒤의 # 줄도 코드다
		{"info string 은 닫지 않는다", "```\n```bash\n# not header\n```", 0, []string{"x"}},
		// 닫는 줄은 여는 줄 이상 길어야 한다
		{"짧은 닫는 줄", "````\n```\n# inside\n````\n# After\nx", 0, []string{"x", "After"}},
		// 닫는 줄 뒤의 공백은 괜찮다
		{"닫는 줄 뒤 공백", "```\n# in\n```   \n# H\nx", 0, []string{"x", "H"}},
		// ``` 의 info string 에 ` 가 있으면 펜스가 아니다 (인라인 코드)
		{"info string 의 백틱", "``` a`b\n# H\nx", 0, []string{"x", "H"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := paths(ChunkMarkdown(c.md, "x", c.maxChars)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("paths = %v, want %v", got, c.want)
			}
		})
	}
}

// IDX-10: 상한을 넘는 문단 하나도 자른다 — 줄 경계, 그래도 크면 문장 경계, 그래도 크면 글자 경계에서.
func TestChunkSplitsOversizeParagraph(t *testing.T) {
	const maxChars = 100
	line := strings.Repeat("나", 40)
	sentence := strings.Repeat("다", 39) + "."
	cases := []struct {
		name  string
		body  string
		check func(t *testing.T, part string)
	}{
		{"줄 경계", strings.TrimSuffix(strings.Repeat(line+"\n", 10), "\n"), func(t *testing.T, part string) {
			for _, l := range strings.Split(part, "\n") {
				if l != line {
					t.Errorf("줄 중간에서 잘렸다: %q", l)
				}
			}
		}},
		{"문장 경계", strings.TrimSuffix(strings.Repeat(sentence+" ", 10), " "), func(t *testing.T, part string) {
			if !strings.HasSuffix(part, ".") {
				t.Errorf("문장 중간에서 잘렸다: %q", part)
			}
		}},
		{"글자 경계", strings.Repeat("라", 1000), func(t *testing.T, part string) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunks := ChunkMarkdown("# T\n"+c.body, "x", maxChars)
			if len(chunks) < 2 {
				t.Fatalf("나뉘어야 한다: %d 청크", len(chunks))
			}
			total := 0
			for i, ch := range chunks {
				n := utf8.RuneCountInString(ch.Text)
				if n > maxChars {
					t.Errorf("청크 %d = %d자 > %d", i, n, maxChars)
				}
				total += n
				if i > 0 { // 첫 청크에는 헤더 줄이 붙는다
					c.check(t, ch.Text)
				}
			}
			if total < utf8.RuneCountInString(c.body)*9/10 {
				t.Errorf("내용이 빠졌다: %d자 / %d자", total, utf8.RuneCountInString(c.body))
			}
		})
	}
}

// IDX-15: 첫 H1 은 코드 펜스 밖에서 찾는다 (청커와 같은 펜스·헤더 규칙).
func TestFirstH1IgnoresCodeFences(t *testing.T) {
	cases := []struct {
		body  string
		want  string
		found bool
	}{
		{"```bash\n# 설치 먼저\nmake init\n```\n\n# 진짜 제목\n본문", "진짜 제목", true},
		{"~~~\n# 코드\n~~~\n## 둘째\n# 첫째 ##\n", "첫째", true},
		{"```\n# 닫히지 않은 펜스\n", "", false},
		{"#\n\n본문", "", false},
	}
	for _, c := range cases {
		got, found := FirstH1(c.body)
		if got != c.want || found != c.found {
			t.Errorf("FirstH1(%q) = %q %v, want %q %v", c.body, got, found, c.want, c.found)
		}
	}
}

// RUN-15: 닫힌 frontmatter 블록의 YAML 이 깨졌으면 알린다. 쓴 글은 버리지 않는다 — 원문 전체가 본문이다.
// YAML 로 읽히지만 객체가 아닌 블록(문서 맨 위 구분선 사이의 목록·글)은 frontmatter 가 아닐 뿐 오류가 아니다.
func TestParseFrontmatterReportsBrokenYAML(t *testing.T) {
	broken := "---\nteam: [unclosed\ntopic: broken\n---\n# Bad YAML\n본문"
	fm, body, err := ParseFrontmatter(broken)
	var fe *FrontmatterError
	if !errors.As(err, &fe) || len(fm) != 0 || body != broken {
		t.Errorf("깨진 YAML = fm %v body %q err %v (FrontmatterError 와 원문 전체 본문)", fm, body, err)
	}
	for _, src := range []string{"---\nteam: ads\n---\n# ok", "---\n- a\n---\nbody", "---\nteam: ads\n# 닫히지 않음", "# frontmatter 없음"} {
		if _, _, err := ParseFrontmatter(src); err != nil {
			t.Errorf("%q → %v (오류가 아니다)", src, err)
		}
	}
	if fm, body := SplitFrontmatter(broken); len(fm) != 0 || body != broken {
		t.Errorf("SplitFrontmatter 는 그대로 원문 전체를 본문으로: %v %q", fm, body)
	}
}
