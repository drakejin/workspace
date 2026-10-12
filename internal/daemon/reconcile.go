package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/drakejin/workspace/internal/cold"
	"github.com/drakejin/workspace/internal/config"
	"github.com/drakejin/workspace/internal/ingest"
	"github.com/drakejin/workspace/internal/model"
	"github.com/drakejin/workspace/internal/naming"
	"github.com/drakejin/workspace/internal/opensearch"
)

// Change 는 문서 변화 종류다.
type Change string

const (
	ChangeNew      Change = "new"
	ChangeModified Change = "modified"
	ChangeRemoved  Change = "removed"
)

// DiffEntry 는 이번 사이클에 내용이 바뀐 문서다. 판정·증류의 입력이다 (메모리 전달만, 영속 없음).
type DiffEntry struct {
	SourcePath string
	Team       string
	DocType    model.DocType
	Kind       model.Kind
	Change     Change
	BodySHA    string
	EventTime  time.Time
	Title      string
	Body       string
	Supersedes string
}

// CycleSummary 는 사이클 한 번의 요약이다.
type CycleSummary struct {
	Scanned        int   `json:"scanned"`
	Indexed        int   `json:"indexed"`
	StatUpdated    int   `json:"statUpdated"`
	Removed        int   `json:"removed"`
	Skipped        int   `json:"skipped"`
	ColdOwned      int   `json:"coldOwned"`
	FlagsConverged int   `json:"flagsConverged"`
	Refused        bool  `json:"refused"`
	TookMs         int64 `json:"tookMs"`
}

// Reconciler 는 수렴 사이클 A 다.
type Reconciler struct {
	OS      opensearch.Client
	Config  config.Config
	Indexer ingest.Indexer
	Cold    cold.Store
	Judge   *JudgeState
	Log     func(format string, args ...any)

	mu sync.Mutex
	// coldOwned 는 cold 가 소유한 hot 파일의 stat 이다. stat 이 그대로면 다음 사이클에 다시 읽지 않는다.
	coldOwned map[string]statKey
}

type statKey struct {
	mtimeMs int64
	size    int64
}

// Once 는 hot(git working tree)과 인덱스를 맞춘다. 매 사이클이 곧 풀 싱크라 첫 기동 분기가 없다.
//  1. stat 이 같으면 건너뛴다  2. stat 이 달라도 sha 가 같으면 stat 만 갱신  3. 내용이 바뀌면 재색인
//  4. 이미 cold 로 간 원본(같은 내용이 cold store 에 있다)은 다시 색인하지 않는다 — cold 소유.
//     되살리려면 restore_cold 하거나, date 를 갱신해 다시 쓴다
//  5. 디스크에 없는 문서는 인덱스에서 지운다(tombstone)  6. superseded_by 를 원장·선언대로 수렴
func (r *Reconciler) Once(ctx context.Context) (CycleSummary, []DiffEntry, error) {
	start := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coldOwned == nil {
		r.coldOwned = map[string]statKey{}
	}

	files, skipped, err := ScanHot(r.Config.MetaRepoRoot, r.Config.Reconcile.MaxFileBytes)
	partial := errors.Is(err, ErrPartialScan)
	if err != nil && !partial {
		return CycleSummary{}, nil, err
	}
	sum := CycleSummary{Scanned: len(files), Skipped: len(skipped)}
	for _, s := range skipped {
		r.Log("[reconcile] skip %s (%s)", s.Rel, s.Reason)
	}
	if len(files) > r.Config.Reconcile.SanityMaxFiles {
		r.Log("[reconcile] matched %d files > sanityMaxFiles(%d) — 설정 오류로 판단, 색인 거부", len(files), r.Config.Reconcile.SanityMaxFiles)
		sum.Refused = true
		sum.TookMs = time.Since(start).Milliseconds()
		return sum, nil, nil
	}

	indices, err := ManagedIndices(ctx, r.OS)
	if err != nil {
		return sum, nil, err
	}
	existing, err := FetchIndexedDocs(ctx, r.OS, indices, r.Config.Workspace)
	if err != nil {
		return sum, nil, err
	}
	archived, err := cold.ArchivedMonths(ctx, r.OS, r.Config)
	if err != nil {
		return sum, nil, err
	}

	var diff []DiffEntry
	for _, f := range files {
		ex, known := existing[f.Rel]
		delete(existing, f.Rel)
		stat := statKey{f.MtimeMs, f.Size}
		if known && ex.MtimeMs == f.MtimeMs && ex.Size == f.Size {
			continue
		}
		if !known && r.coldOwned[f.Rel] == stat {
			sum.ColdOwned++
			continue
		}
		if binary, err := IsProbablyBinary(f.Abs); err != nil || binary {
			r.Log("[reconcile] skip %s (binary content)", f.Rel)
			sum.Skipped++
			continue
		}
		prepared, err := r.Indexer.Prepare(ctx, f.Abs, "")
		if err != nil {
			r.Log("[reconcile] skip %s (%v)", f.Rel, err)
			sum.Skipped++
			continue
		}
		b := prepared.Built
		if known && ex.ContentSHA == b.ContentSHA {
			if err := UpdateBySource(ctx, r.OS, indices, r.Config.Workspace, f.Rel, map[string]any{"mtime_ms": f.MtimeMs, "size": f.Size}); err != nil {
				return sum, diff, err
			}
			sum.StatUpdated++
			continue
		}
		if !known && b.Meta.Kind == model.KindEpisodic && archived[naming.MonthlyIndex(b.Meta.Team, b.EventTime)] {
			// 그 달이 cold 로 갔고, 이 workspace 의 cold store 에 지금과 같은 내용이 보관돼 있으면 cold 소유다.
			// 처음 보는 문서(또는 내용이 바뀐 문서)는 색인한다 — 다음 tier 가 다시 cold 로 보낸다.
			sha, ok, err := cold.ArchivedContentSHA(ctx, r.Cold, b.Meta.Team, f.Rel)
			if err != nil {
				return sum, diff, err
			}
			if ok && sha == b.ContentSHA {
				r.coldOwned[f.Rel] = stat
				sum.ColdOwned++
				continue
			}
		}
		delete(r.coldOwned, f.Rel)
		res, err := r.Indexer.Write(ctx, prepared)
		if err != nil {
			return sum, diff, err
		}
		change := ChangeNew
		if known {
			change = ChangeModified
		}
		diff = append(diff, DiffEntry{
			SourcePath: res.SourcePath, Team: res.Team, DocType: res.DocType, Kind: res.Kind, Change: change,
			BodySHA: res.BodySHA, EventTime: res.EventTime, Title: res.Title, Body: res.Body, Supersedes: res.Supersedes,
		})
		sum.Indexed++
	}

	// tombstone — 사람이 hot 에서 지운 문서. 원본 이력은 git 히스토리와 cold store raw 가 갖는다.
	// 스캔이 일부만 됐으면 "없는 파일"이 진짜 지워진 건지 알 수 없다 — 이번 사이클은 지우지 않는다
	if partial {
		r.Log("[reconcile] 스캔이 일부만 됐습니다 — 이번 사이클은 삭제(tombstone)를 건너뜁니다")
		existing = nil
	}
	for _, gone := range existing {
		if _, err := r.Indexer.RemoveFile(ctx, gone.SourcePath, indices); err != nil {
			return sum, diff, err
		}
		diff = append(diff, DiffEntry{SourcePath: gone.SourcePath, Team: gone.Team, DocType: gone.DocType, Kind: gone.Kind, Change: ChangeRemoved, BodySHA: gone.BodySHA})
		sum.Removed++
	}

	// 재색인으로 인덱스가 새로 생겼을 수 있다 (인덱스를 통째로 지운 뒤의 재구축) — 목록을 다시 읽는다
	indices, err = ManagedIndices(ctx, r.OS)
	if err != nil {
		return sum, diff, err
	}
	converged, err := r.ConvergeSupersededBy(ctx, indices)
	if err != nil {
		return sum, diff, err
	}
	sum.FlagsConverged = converged
	sum.TookMs = time.Since(start).Milliseconds()
	return sum, diff, nil
}

// ConvergeSupersededBy 는 superseded_by 를 정본대로 맞춘다. 정본은 둘이다:
// 개정판 frontmatter 의 supersedes 선언, 그리고 judge-state 원장.
// 인덱스 값은 파생물이라 매 사이클 다시 맞춘다 (인덱스를 지워도 같은 결과로 재구축된다).
func (r *Reconciler) ConvergeSupersededBy(ctx context.Context, indices []string) (int, error) {
	docs, err := FetchIndexedDocs(ctx, r.OS, indices, r.Config.Workspace)
	if err != nil {
		return 0, err
	}
	desired, err := DesiredSupersededBy(docs, r.Judge)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, d := range docs {
		want := desired[d.SourcePath]
		if d.SupersededBy == want {
			continue
		}
		var value any
		if want != "" {
			value = want
		}
		if err := UpdateBySource(ctx, r.OS, indices, r.Config.Workspace, d.SourcePath, map[string]any{"superseded_by": value}); err != nil {
			return changed, err
		}
		if want == "" {
			r.Log("[judge] superseded_by %s → (해제)", d.SourcePath)
		} else {
			r.Log("[judge] superseded_by %s → %s", d.SourcePath, want)
		}
		changed++
	}
	return changed, nil
}

// DesiredSupersededBy 는 문서별로 있어야 할 superseded_by 다. 사람의 선언이 판정보다 우선한다.
func DesiredSupersededBy(docs map[string]IndexedDoc, judge *JudgeState) (map[string]string, error) {
	desired := map[string]string{}
	teams := map[string]bool{}
	for _, d := range docs {
		teams[d.Team] = true
	}
	bodySHA := func(p string) (string, bool) {
		d, ok := docs[p]
		return d.BodySHA, ok
	}
	for team := range teams {
		m, err := judge.SupersededMap(team, bodySHA)
		if err != nil {
			return nil, err
		}
		for oldPath, newPath := range m {
			desired[oldPath] = newPath
		}
	}
	for _, d := range docs {
		if d.Supersedes != "" {
			if _, ok := docs[d.Supersedes]; ok {
				desired[d.Supersedes] = d.SourcePath
			}
		}
	}
	return desired, nil
}
