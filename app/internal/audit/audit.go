// Package audit 는 주간 정합성 감사다 — hot·인덱스·cold store·원장이 서로 맞는지 보고, 고칠 수 있는 것은 고친다.
// 데몬이 매 사이클 수렴시키지만, stat 이 보존된 복사(cp -p)나 꺼져 있던 동안의 사고처럼 사이클이 못 보는 틈을 메운다.
package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/drakejin/workspace/app/internal/cold"
	"github.com/drakejin/workspace/app/internal/config"
	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/ingest"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/naming"
	"github.com/drakejin/workspace/app/internal/opensearch"
	"github.com/drakejin/workspace/app/internal/state"
)

// Kind 는 발견 종류다.
type Kind string

const (
	OrphanIndex     Kind = "orphan-index"     // 색인에 있는데 파일이 없다 (고칠 수 있음: 색인에서 뺀다)
	Unindexed       Kind = "unindexed"        // 파일이 있는데 색인에 없고 cold 소유도 아니다 (고칠 수 있음: 색인)
	StaleContent    Kind = "stale-content"    // 색인 내용과 파일 내용(sha)이 다르다 (고칠 수 있음: 재색인)
	OrphanChunks    Kind = "orphan-chunks"    // cold store 청크 JSONL 의 파일도, cold 보관도 없다 (고칠 수 있음: JSONL 삭제)
	BrokenLink      Kind = "broken-link"      // 링크 대상이 디스크에도(색인 범위 밖 포함) hot 에도 cold 에도 없다, 또는 철자만 다르다
	UnsourcedFact   Kind = "unsourced-fact"   // knowledge 에 근거(derived_from 또는 ## 출처)가 없다
	StaleQuestion   Kind = "stale-question"   // 답을 오래 기다리는 판정 질문
	CorruptedLedger Kind = "corrupted-ledger" // 원장에 읽을 수 없는 줄이 있다
	PartialScan     Kind = "partial-scan"     // 읽지 못한 디렉토리가 있다 — 지우는 조치(orphan-index·orphan-chunks)를 건너뛴다
	BadFrontmatter  Kind = "bad-frontmatter"  // frontmatter YAML 이 깨졌다(메타데이터 없이 색인) 또는 값이 잘못됐다(그 판은 색인하지 않는다)
	SupersedesCycle Kind = "supersedes-cycle" // frontmatter supersedes 가 순환한다 — 수렴이 그 선언들을 버린다
	SupersedesSelf  Kind = "supersedes-self"  // frontmatter supersedes 가 자기 자신을 가리킨다 — 수렴이 버린다
)

var fixable = map[Kind]bool{OrphanIndex: true, Unindexed: true, StaleContent: true, OrphanChunks: true}

// staleQuestionAge 는 질문을 오래 묵었다고 볼 기간이다.
const staleQuestionAge = 14 * 24 * time.Hour

// Finding 은 발견 하나다.
type Finding struct {
	Kind   Kind
	Path   string
	Detail string
	Fixed  bool
}

// Report 는 감사 결과다.
type Report struct {
	Findings []Finding
	Applied  bool
}

// Deps 는 감사 의존이다.
type Deps struct {
	OS      opensearch.Client
	Config  config.Config
	Cold    cold.Store
	Indexer ingest.Indexer
	Now     time.Time
}

var sourcesSection = regexp.MustCompile(`(?m)^##\s+출처\s*$`)

// Run 은 감사한다. apply 면 고칠 수 있는 것을 고친다.
func Run(ctx context.Context, d Deps, apply bool) (Report, error) {
	r := &runner{Deps: d, apply: apply}
	if err := r.load(ctx); err != nil {
		return Report{}, err
	}
	steps := []func(context.Context) error{r.hotVsIndex, r.supersedesDeclarations, r.orphanChunks, r.links, r.questions}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return Report{Findings: r.findings, Applied: apply}, err
		}
	}
	if r.corrupt > 0 {
		r.add(CorruptedLedger, "-", fmt.Sprintf(".cache 의 원장(judge·distill·archive)에 읽을 수 없는 줄 %d개 (건너뛰고 있다)", r.corrupt), false)
	}
	sort.SliceStable(r.findings, func(i, j int) bool {
		if r.findings[i].Kind != r.findings[j].Kind {
			return r.findings[i].Kind < r.findings[j].Kind
		}
		return r.findings[i].Path < r.findings[j].Path
	})
	return Report{Findings: r.findings, Applied: apply}, nil
}

type runner struct {
	Deps
	apply    bool
	partial  bool // 스캔이 일부만 됐다 — 파일이 "없다" 는 판단을 믿을 수 없다
	findings []Finding
	corrupt  int

	files   map[string]ingest.ScannedFile
	indices []string
	docs    map[string]ingest.IndexedDoc
	owner   *cold.Ownership
	// declared 는 hot 파일의 frontmatter supersedes 선언이다 (경로 → 밀어내는 구판). checkFile 이 모은다
	declared map[string]string
}

func (r *runner) add(k Kind, path, detail string, fixed bool) {
	r.findings = append(r.findings, Finding{Kind: k, Path: path, Detail: detail, Fixed: fixed})
}

func (r *runner) load(ctx context.Context) error {
	files, _, err := ingest.ScanHot(r.Config.MetaRepoRoot, r.Config.Reconcile.MaxFileBytes)
	r.partial = errors.Is(err, ingest.ErrPartialScan)
	if err != nil && !r.partial {
		return fmt.Errorf("audit scan: %w", err)
	}
	if r.partial {
		// 데몬의 tombstone 과 같은 규칙: 읽지 못한 디렉토리의 문서를 "파일이 없다" 로 보고 지우지 않는다
		r.add(PartialScan, "-", "읽지 못한 디렉토리가 있어 orphan-index·orphan-chunks 검사를 건너뛴다 (권한 확인)", false)
	}
	r.files = make(map[string]ingest.ScannedFile, len(files))
	for _, f := range files {
		r.files[f.Rel] = f
	}
	if r.indices, err = ingest.ManagedIndices(ctx, r.OS); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if r.docs, err = ingest.FetchIndexedDocs(ctx, r.OS, r.indices, r.Config.Workspace); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	archived, err := cold.ArchivedMonths(ctx, r.OS, r.Config)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	r.owner = cold.NewOwnership(archived, r.Cold)
	return nil
}

// hotVsIndex 는 파일과 색인을 stat 을 무시하고 내용(sha)으로 대조한다 (deep pass).
func (r *runner) hotVsIndex(ctx context.Context) error {
	if err := r.orphanIndex(ctx); err != nil {
		return err
	}
	rels := make([]string, 0, len(r.files))
	for rel := range r.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if err := r.checkFile(ctx, r.files[rel]); err != nil {
			return err
		}
	}
	return nil
}

// orphanIndex 는 색인에는 있는데 그 철자의 파일이 스캔에 없는 문서다.
func (r *runner) orphanIndex(ctx context.Context) error {
	for rel, doc := range r.docs {
		if _, ok := r.files[rel]; ok || r.partial {
			continue
		}
		fixed := false
		if r.apply {
			if _, err := r.Indexer.RemoveFile(ctx, rel, r.indices); err != nil {
				return err
			}
			fixed = true
		}
		r.add(OrphanIndex, rel, r.orphanDetail(rel, doc.Index), fixed)
	}
	return nil
}

// orphanDetail 은 orphan-index 의 설명이다. 같은 파일이 디스크에 다른 철자(대소문자·NFD)로 있으면 옛 철자의 색인일 뿐이라
// "파일이 없다" 대신 디스크의 철자를 알린다 — 조치(-apply)는 같다: 옛 철자만 색인에서 빼고 파일·청크 JSONL 은 그대로 둔다.
func (r *runner) orphanDetail(rel, index string) string {
	if !ingest.LiveUnderOtherSpelling(r.Config.MetaRepoRoot, rel) {
		return fmt.Sprintf("색인(%s)에는 있는데 파일이 없다", index)
	}
	_, onDisk, err := ingest.ResolveOnDisk(r.Config.MetaRepoRoot, rel)
	if err != nil {
		return fmt.Sprintf("색인(%s)에 옛 철자로 남았다 — 파일은 디스크에 다른 철자로 있다 (옛 철자만 색인에서 뺀다)", index)
	}
	return fmt.Sprintf("색인(%s)에 옛 철자로 남았다 — 파일은 디스크에 %s 로 있다 (옛 철자만 색인에서 뺀다)", index, onDisk)
}

func (r *runner) checkFile(ctx context.Context, f ingest.ScannedFile) error {
	p, err := r.Indexer.Prepare(ctx, f.Abs, "")
	if err != nil {
		r.badFrontmatterValue(f.Rel, err) // 게이트(바이너리·크기)에 걸린 파일 등 나머지는 데몬 로그가 이미 남긴다
		return nil
	}
	b := p.Built
	r.noteFrontmatter(f.Rel, b)
	if b.Meta.Kind == model.KindKnowledge && b.Meta.DocType == model.DocKnowledge && !hasSources(b) {
		r.add(UnsourcedFact, f.Rel, "knowledge 에 derived_from(why 포함)도 '## 출처' 절도 없다", false)
	}
	doc, indexed := r.docs[f.Rel]
	switch {
	case indexed && doc.ContentSHA == b.ContentSHA:
		return nil
	case indexed:
		return r.fixWith(ctx, StaleContent, p, "색인 내용이 파일과 다르다 (stat 이 보존된 복사 등)")
	}
	if len(b.Chunks) > 0 {
		chunks, ok, err := cold.Archived(ctx, r.Cold, b.Meta.Team, f.Rel)
		if err != nil {
			return err
		}
		if ok && chunks[0].ContentSHA == b.ContentSHA {
			owned, err := r.owner.Owns(ctx, chunks[0].Team, naming.IndexFor(chunks[0]), f.Rel, chunks[0].ContentSHA)
			if err != nil {
				return err
			}
			if owned {
				return nil // cold 소유
			}
		}
	}
	return r.fixWith(ctx, Unindexed, p, "파일이 있는데 색인에 없다")
}

func (r *runner) fixWith(ctx context.Context, k Kind, p ingest.Prepared, detail string) error {
	fixed := false
	if r.apply {
		if _, err := r.Indexer.Write(ctx, p); err != nil {
			return err
		}
		fixed = true
	}
	r.add(k, p.Rel, detail, fixed)
	return nil
}

func hasSources(b document.Built) bool {
	for _, l := range b.Meta.Links {
		if l.Type == model.LinkDerivedFrom {
			return true
		}
	}
	return sourcesSection.MatchString(b.Body)
}

// orphanChunks 는 파일도 cold 보관도 없는 청크 JSONL 이다 (raw 원본은 이력이라 남긴다).
func (r *runner) orphanChunks(ctx context.Context) error {
	if r.partial {
		return nil
	}
	teams := map[string]bool{r.Config.DefaultTeam: true}
	for _, d := range r.docs {
		teams[d.Team] = true
	}
	for team := range teams {
		keys, err := r.Cold.List(ctx, cold.ChunksPrefix(team))
		if err != nil {
			return fmt.Errorf("audit cold store: %w", err)
		}
		for _, key := range keys {
			rel := strings.TrimSuffix(strings.TrimPrefix(key, cold.ChunksPrefix(team)), ".jsonl")
			if _, onDisk := r.files[rel]; onDisk {
				continue
			}
			if ingest.LiveUnderOtherSpelling(r.Config.MetaRepoRoot, rel) {
				// 같은 파일이 다른 철자(대소문자·NFD)로 디스크에 있다 — APFS 위의 로컬 cold store 에서는 지금 문서의 청크 파일이다
				continue
			}
			if _, indexed := r.docs[rel]; indexed {
				continue // orphan-index 로 이미 보고했다 — 그 조치(RemoveFile)가 청크 JSONL 까지 지운다
			}
			chunks, ok, err := cold.Archived(ctx, r.Cold, team, rel)
			if err != nil {
				return err
			}
			if ok {
				owned, err := r.owner.Owns(ctx, team, naming.IndexFor(chunks[0]), rel, chunks[0].ContentSHA)
				if err != nil {
					return err
				}
				if owned {
					continue // cold 로 간 문서 — 정상
				}
			}
			fixed := false
			if r.apply {
				if err := r.Cold.Remove(ctx, key); err != nil {
					return fmt.Errorf("audit cold store: %w", err)
				}
				fixed = true
			}
			r.add(OrphanChunks, rel, "cold store 에 청크만 남았다 (파일도 cold 보관도 없다)", fixed)
		}
	}
	return nil
}

// links 는 링크 대상이 디스크에도 hot 에도 cold 에도 없는 것을 찾는다.
func (r *runner) links(ctx context.Context) error {
	rels := make([]string, 0, len(r.files))
	for rel := range r.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		b, err := os.ReadFile(r.files[rel].Abs) // rel 은 NFC 다 — IO 는 디스크의 이름으로
		if err != nil {
			continue
		}
		team := r.Config.DefaultTeam
		if d, ok := r.docs[rel]; ok {
			team = d.Team
		}
		for _, l := range document.Links(string(b), rel) {
			if err := r.checkLink(ctx, rel, team, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkLink 는 rel 의 링크 하나가 살아 있는지 본다: hot(스캔한 파일) → 디스크(그 철자 그대로) → cold store 순서.
func (r *runner) checkLink(ctx context.Context, rel, team string, l model.Link) error {
	if _, ok := r.files[l.To]; ok {
		return nil
	}
	if !insideRoot(l.To) {
		// meta-repo 밖(../ 로 시작)이나 절대 경로 — cold store 키가 될 수 없으니 깨진 링크로만 알린다
		r.add(BrokenLink, rel, fmt.Sprintf("%s → %s 는 meta-repository 안의 경로가 아니다", l.Type, l.To), false)
		return nil
	}
	if ingest.ExistsAsSpelled(r.Config.MetaRepoRoot, l.To) {
		return nil // 디스크에 있다 — 색인 범위 밖(scenarios/·.claude/·examples/)이거나 게이트에 걸린 파일이어도 링크는 산다
	}
	if ingest.LiveUnderOtherSpelling(r.Config.MetaRepoRoot, l.To) {
		// 철자(대소문자)만 다른 파일이 디스크에 있다 — GitHub 에서 깨진다. cold store 는 보지 않는다: APFS 위의 로컬
		// cold store 는 그 hot 문서의 raw 사본을 이 철자로도 열어서 cold 로 간 문서처럼 보인다
		r.add(BrokenLink, rel, r.wrongCaseDetail(l), false)
		return nil
	}
	_, ok, err := r.Cold.Get(ctx, cold.RawKey(team, l.To))
	if err != nil {
		return fmt.Errorf("audit cold store: %w", err)
	}
	if !ok {
		r.add(BrokenLink, rel, fmt.Sprintf("%s → %s 가 디스크에도 hot 에도 cold 에도 없다", l.Type, l.To), false)
	}
	return nil // ok 면 cold 로 간 문서를 가리킨다 — 정상
}

// wrongCaseDetail 은 철자만 다른 링크의 설명이다 — 디스크의 철자를 알려 준다.
func (r *runner) wrongCaseDetail(l model.Link) string {
	_, onDisk, err := ingest.ResolveOnDisk(r.Config.MetaRepoRoot, l.To)
	if err != nil {
		return fmt.Sprintf("%s → %s 는 디스크의 철자와 다르다 (GitHub 에서 깨진다)", l.Type, l.To)
	}
	return fmt.Sprintf("%s → %s 는 디스크의 철자와 다르다 — 파일은 %s 로 있다 (GitHub 에서 깨진다)", l.Type, l.To, onDisk)
}

func (r *runner) questions(_ context.Context) error {
	judge := state.NewJudgeState(r.Config.MetaRepoRoot)
	archive := state.NewArchiveState(r.Config.MetaRepoRoot)
	cache := state.NewDistillCache(r.Config.MetaRepoRoot)
	count := func(string, int) { r.corrupt++ }
	judge.Ledger.OnCorrupt, archive.Ledger.OnCorrupt, cache.Ledger.OnCorrupt = count, count, count
	all, err := judge.AllPendingQuestions()
	if err != nil {
		return err
	}
	for team, qs := range all {
		for _, q := range qs {
			at, _ := time.Parse(time.RFC3339, q.At)
			if !at.IsZero() && r.Now.Sub(at) > staleQuestionAge {
				r.add(StaleQuestion, q.Old.Path, fmt.Sprintf("[%s] %s ↔ %s — %s 부터 답을 기다린다 (scripts/memory/run.sh questions)", team, q.Old.Path, q.New.Path, model.Day(q.At)), false)
			}
		}
	}
	teams, err := archive.Ledger.Teams()
	if err != nil {
		return err
	}
	for _, t := range teams {
		if _, err := archive.Ledger.ReadAll(t); err != nil {
			return err
		}
	}
	return cache.Load()
}

// Render 는 감사 결과를 사람이 읽는 표로 쓴다.
func Render(rep Report) string {
	if len(rep.Findings) == 0 {
		return "[audit] 이상 없음"
	}
	var b strings.Builder
	counts := map[Kind]int{}
	fixedN := 0
	for _, f := range rep.Findings {
		counts[f.Kind]++
		if f.Fixed {
			fixedN++
		}
	}
	fmt.Fprintf(&b, "[audit] 발견 %d건", len(rep.Findings))
	if rep.Applied {
		fmt.Fprintf(&b, ", 고침 %d건", fixedN)
	}
	b.WriteString("\n")
	for _, f := range rep.Findings {
		mark := ""
		switch {
		case f.Fixed:
			mark = " [고침]"
		case fixable[f.Kind] && !rep.Applied:
			mark = " [scripts/memory/run.sh audit -apply 로 고칠 수 있음]"
		}
		fmt.Fprintf(&b, "- %-16s %s — %s%s\n", f.Kind, f.Path, f.Detail, mark)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Summary 는 데몬 로그용 한 줄 요약이다.
func Summary(rep Report) string {
	if len(rep.Findings) == 0 {
		return "이상 없음"
	}
	counts := map[Kind]int{}
	for _, f := range rep.Findings {
		counts[f.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for k, n := range counts {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds)
	return fmt.Sprintf("%d건 (%s) — scripts/memory/run.sh audit 로 확인", len(rep.Findings), strings.Join(kinds, " "))
}

// insideRoot 는 링크 대상이 meta-repo 기준 상대 경로인가다.
func insideRoot(p string) bool {
	clean := path.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, "/")
}
