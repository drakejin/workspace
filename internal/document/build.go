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

	"github.com/drakejin/workspace/internal/markdown"
	"github.com/drakejin/workspace/internal/model"
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
	title := rootTitle
	if m := titleLine.FindStringSubmatch(body); m != nil {
		title = strings.TrimSpace(m[1])
	}
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

// ChunkID 는 결정적 청크 id 다.
func ChunkID(workspace, team, sourcePath string, index int) string {
	h := sha1.New()
	h.Write([]byte(workspace + "\x00" + team + "\x00" + sourcePath + "\x00" + strconv.Itoa(index)))
	return hex.EncodeToString(h.Sum(nil))
}
