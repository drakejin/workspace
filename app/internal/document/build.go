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
	Location     *time.Location // 문서의 날(event_day)을 정하는 시간대. nil 이면 UTC
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
	// FrontmatterErr 는 frontmatter YAML 이 깨졌다는 경고다 (*markdown.FrontmatterError). 쓴 글은 버리지 않고 메타데이터 없이
	// 원문 전체를 본문으로 색인한다 — 데몬은 경고를 남기고 audit 는 bad-frontmatter 로 보고한다
	FrontmatterErr error
}

// SHA256 은 hex sha256 이다.
func SHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Build 는 파일 하나를 청크 목록으로 바꾼다. 같은 입력이면 같은 id 가 나온다(재색인은 덮어쓰기).
func Build(in Input) (Built, error) {
	fm, body, fmErr := markdown.ParseFrontmatter(in.Content)
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
	eventDay := meta.DeclaredDay
	if eventDay == "" {
		eventDay = eventTime.In(locationOrUTC(in.Location)).Format(dayLayout)
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
		// 깨진 YAML 은 실패가 아니라 경고다 — 원문 전체가 본문이다
		FrontmatterErr: fmErr,
	}
	var distilled model.Distilled
	hasDistilled := false
	if in.Distilled != nil {
		distilled, hasDistilled = in.Distilled(meta.Team, built.BodySHA)
	}
	chunks := markdown.ChunkMarkdown(body, rootTitle, 0)
	if len(chunks) == 0 {
		// 청크로 만들 본문이 없어도(빈 파일·frontmatter 만·공백만) 빈 청크 0 하나를 남긴다 — 청크 0 의 content_sha·mtime·size 가
		// 데몬의 "최신" 표시라, 없으면 이 문서는 색인에 흔적이 없어 매 사이클 새 문서로 다시 쓰인다
		chunks = []markdown.Chunk{{HeaderPath: title}}
	}
	for i, c := range chunks {
		chunk := model.Chunk{
			ID:         ChunkID(in.Workspace, meta.Team, in.SourcePath, i),
			Workspace:  in.Workspace,
			Team:       meta.Team,
			Repo:       meta.Repo,
			Topic:      meta.Topic,
			DocType:    meta.DocType,
			Kind:       meta.Kind,
			EventTime:  eventTime.Format(time.RFC3339Nano),
			EventDay:   eventDay,
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
			// 필드 구성의 판 — 데몬이 낮은 판의 문서를 다시 써 새 필드를 채운다
			IndexVersion: model.IndexVersion,
		}
		if hasDistilled {
			chunk.Summary = distilled.Summary
			chunk.Entities = distilled.Entities
		}
		built.Chunks = append(built.Chunks, chunk)
	}
	return built, nil
}

// IsBlank 는 본문에 청크로 만들 내용이 없는가다 (빈 파일·frontmatter 만·공백만). 이런 문서는 빈 청크 0 하나만 색인하고,
// 주장이 없으니 판정·증류하지 않는다.
func IsBlank(body string) bool {
	return len(markdown.ChunkMarkdown(body, "", 0)) == 0
}

func locationOrUTC(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}

// Title 은 본문의 첫 `# ` 제목이다 — 코드 펜스 안의 `#` 줄(셸 주석)은 제목이 아니다 (청커와 같은 펜스 규칙).
// 없으면 확장자를 뺀 파일 이름.
func Title(body, sourcePath string) string {
	if title, ok := markdown.FirstH1(body); ok {
		return title
	}
	return strings.TrimSuffix(path.Base(sourcePath), path.Ext(sourcePath))
}

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// MarkdownLinks 는 본문의 상대 경로 마크다운 링크(.md)다. 대상은 meta-repository 기준 경로(NFC — source_path 와 같은 꼴)로 바꾼다.
// 외부 URL·앵커만 있는 링크·meta-repository 밖을 가리키는 링크는 버린다.
func MarkdownLinks(body, sourcePath string) []model.Link {
	seen := map[string]bool{}
	var links []model.Link
	for _, m := range markdownLink.FindAllStringSubmatch(body, -1) {
		target, _, _ := strings.Cut(CanonicalPath(m[1]), "#")
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
	if s := CanonicalPath(firstString(fm, "supersedes")); s != "" {
		links = append(links, model.Link{Type: model.LinkSupersedes, To: s, Why: "frontmatter supersedes"})
	}
	return append(links, MarkdownLinks(body, sourcePath)...)
}
