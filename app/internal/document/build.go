package document

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/markdown"
	"github.com/drakejin/workspace/app/internal/model"
)

var titleLine = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// Input 은 파일 하나를 청크로 만들 재료다.
type Input struct {
	Workspace    string // meta-repository 이름 (격리 축)
	SourcePath   string // meta-repository 기준 상대 경로 (posix)
	Content      string
	DefaultTeam  string
	FallbackTime time.Time // frontmatter 날짜가 없을 때 (git 커밋 시각 → mtime)
	GitSHA       string
	Now          time.Time
	MtimeMs      int64
	Size         int64
	TeamOverride string
	// Distilled 는 증류 캐시 조회다. hit 면 LLM 없이 summary/entities 가 붙는다.
	Distilled func(team, bodySHA string) (model.Distilled, bool)
}

// Built 는 문서 하나의 결과다.
type Built struct {
	Meta       Metadata
	EventTime  time.Time
	ContentSHA string // 원문 전체 — 데몬의 변경 감지 기준
	BodySHA    string // frontmatter 제외 본문 — 판정 쌍·증류 캐시 키. 메타만 바뀌면 그대로다
	Title      string
	Body       string
	Chunks     []model.Chunk
}

// SHA256 은 hex sha256 이다.
func SHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Build 는 파일 하나를 청크 목록으로 바꾼다. 같은 입력이면 같은 id 가 나온다(재색인은 덮어쓰기).
func Build(in Input) (Built, error) {
	fm, body := markdown.SplitFrontmatter(in.Content)
	if in.TeamOverride != "" {
		fm["team"] = in.TeamOverride
	}
	meta, err := ResolveMetadata(fm, in.SourcePath, in.DefaultTeam)
	if err != nil {
		return Built{}, err
	}
	eventTime := meta.DeclaredTime
	if eventTime.IsZero() {
		eventTime = in.FallbackTime.UTC()
	}
	rootTitle := strings.TrimSuffix(path.Base(in.SourcePath), path.Ext(in.SourcePath))
	title := Title(body, in.SourcePath)
	meta.Links = append(meta.Links, MarkdownLinks(body, in.SourcePath)...)
	built := Built{
		Meta:       meta,
		EventTime:  eventTime,
		ContentSHA: SHA256(in.Content),
		BodySHA:    SHA256(body),
		Title:      title,
		Body:       body,
	}
	var distilled model.Distilled
	hasDistilled := false
	if in.Distilled != nil {
		distilled, hasDistilled = in.Distilled(meta.Team, built.BodySHA)
	}
	for i, c := range markdown.ChunkMarkdown(body, rootTitle, 0) {
		chunk := model.Chunk{
			ID:         ChunkID(in.Workspace, meta.Team, in.SourcePath, i),
			Workspace:  in.Workspace,
			Team:       meta.Team,
			Repo:       meta.Repo,
			DocType:    meta.DocType,
			Kind:       meta.Kind,
			EventTime:  eventTime.Format(time.RFC3339Nano),
			SourcePath: in.SourcePath,
			GitSHA:     in.GitSHA,
			HeaderPath: c.HeaderPath,
			ChunkIndex: i,
			Text:       c.Text,
			IngestedAt: in.Now.UTC().Format(time.RFC3339Nano),
			ContentSHA: built.ContentSHA,
			BodySHA:    built.BodySHA,
			MtimeMs:    in.MtimeMs,
			Size:       in.Size,
			Title:      title,
			Supersedes: meta.Supersedes,
		}
		if hasDistilled {
			chunk.Summary = distilled.Summary
			chunk.Entities = distilled.Entities
		}
		built.Chunks = append(built.Chunks, chunk)
	}
	return built, nil
}

// Title 은 본문의 첫 `# ` 제목이다. 없으면 확장자를 뺀 파일 이름.
func Title(body, sourcePath string) string {
	if m := titleLine.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSuffix(path.Base(sourcePath), path.Ext(sourcePath))
}

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// MarkdownLinks 는 본문의 상대 경로 마크다운 링크(.md)다. 대상은 meta-repository 기준 경로로 바꾼다.
// 외부 URL·앵커만 있는 링크·meta-repository 밖을 가리키는 링크는 버린다.
func MarkdownLinks(body, sourcePath string) []model.Link {
	seen := map[string]bool{}
	var links []model.Link
	for _, m := range markdownLink.FindAllStringSubmatch(body, -1) {
		target, _, _ := strings.Cut(m[1], "#")
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || path.Ext(target) != ".md" {
			continue
		}
		var resolved string
		if strings.HasPrefix(target, "/") {
			resolved = path.Clean(strings.TrimPrefix(target, "/"))
		} else {
			resolved = path.Clean(path.Join(path.Dir(sourcePath), target))
		}
		if strings.HasPrefix(resolved, "..") || resolved == sourcePath || seen[resolved] {
			continue
		}
		seen[resolved] = true
		links = append(links, model.Link{Type: model.LinkLinksTo, To: resolved})
	}
	return links
}

// ChunkID 는 결정적 청크 id 다.
func ChunkID(workspace, team, sourcePath string, index int) string {
	h := sha1.New()
	h.Write([]byte(workspace + "\x00" + team + "\x00" + sourcePath + "\x00" + strconv.Itoa(index)))
	return hex.EncodeToString(h.Sum(nil))
}

// Links 는 파일 하나가 가리키는 다른 문서다: frontmatter supersedes·derived_from·relates 와 본문 마크다운 링크.
func Links(content, sourcePath string) []model.Link {
	fm, body := markdown.SplitFrontmatter(content)
	links := frontmatterLinks(fm)
	if s := firstString(fm, "supersedes"); s != "" {
		links = append(links, model.Link{Type: model.LinkSupersedes, To: s, Why: "frontmatter supersedes"})
	}
	return append(links, MarkdownLinks(body, sourcePath)...)
}
