package document

import (
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
)

// nfd 는 Finder 같은 macOS 앱이 이름에 쓰는 NFD 철자다.
func nfd(s string) string { return norm.NFD.String(s) }

// RUN-5: 주제·프로젝트는 NFC 로 맞춘다 — Finder 가 만든 NFD 디렉토리 이름도 사용자가 친(NFC) 주제와 같은 값이 되고,
// search_filters 에 같은 글자의 주제가 둘로 갈려 보이지 않는다.
func TestTopicAndProjectAreNFC(t *testing.T) {
	if got := NormalizeTopic(nfd("환불정책")); got != "환불정책" || !norm.NFC.IsNormalString(got) {
		t.Errorf("NormalizeTopic(NFD 환불정책) = %q (NFC=%v)", got, norm.NFC.IsNormalString(got))
	}
	// NFD 의 결합 문자(악센트)는 글자가 아니라서 NFC 로 합치기 전에 정규식에 걸리면 하이픈이 된다
	if got := NormalizeTopic(nfd("Café Menu")); got != "café-menu" {
		t.Errorf("NormalizeTopic(NFD Café Menu) = %q", got)
	}
	if got := factsFromPath("docs/feature/" + nfd("환불정책") + "/" + nfd("메모.md")); got.Topic != "환불정책" {
		t.Errorf("경로의 NFD 주제 = %q", got.Topic)
	}
	if got := factsFromPath("docs/repository/" + nfd("결제") + "/adr/0001.md"); got.Repo != "결제" {
		t.Errorf("경로의 NFD 프로젝트 = %q", got.Repo)
	}
	fm := markdown.Frontmatter{"topic": nfd("환불 정책"), "project": nfd("결제 서비스")}
	m, err := ResolveMetadata(fm, "docs/knowledge/x.md", "t")
	if err != nil || m.Topic != "환불-정책" || m.Repo != "결제 서비스" {
		t.Errorf("frontmatter NFD topic=%q project=%q err=%v", m.Topic, m.Repo, err)
	}
	if got := NormalizeProject("  " + nfd("결제 서비스") + " "); got != "결제 서비스" {
		t.Errorf("NormalizeProject = %q", got)
	}
}

// RUN-4: 문서가 가리키는 경로(supersedes·derived_from·relates·본문 링크)도 source_path 와 같은 NFC 로 맞춘다.
func TestLinkTargetsAreNFC(t *testing.T) {
	content := "---\nsupersedes: docs/knowledge/" + nfd("옛판.md") + "\nrelates:\n  - {path: docs/feature/" + nfd("환불정책") + "/a.md, why: 같은 주제}\n---\n" +
		"# T\n[메모](../feature/" + nfd("환불정책") + "/" + nfd("메모.md") + ")"
	want := map[string]model.LinkType{
		"docs/knowledge/옛판.md": model.LinkSupersedes, "docs/feature/환불정책/a.md": model.LinkRelates, "docs/feature/환불정책/메모.md": model.LinkLinksTo,
	}
	for _, l := range Links(content, "docs/knowledge/new.md") {
		if want[norm.NFC.String(l.To)] != l.Type || !norm.NFC.IsNormalString(l.To) {
			t.Errorf("link %s %q (NFC=%v)", l.Type, l.To, norm.NFC.IsNormalString(l.To))
		}
	}
	b, err := Build(Input{Workspace: "ws", SourcePath: "docs/knowledge/new.md", Content: content, DefaultTeam: "t"})
	if err != nil || b.Meta.Supersedes != "docs/knowledge/옛판.md" || b.Chunks[0].Supersedes != "docs/knowledge/옛판.md" {
		t.Errorf("supersedes = %q %v", b.Meta.Supersedes, err)
	}
}
