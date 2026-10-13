// Package archive 는 cold 문서의 유일한 쓰기 경로다 — deprecate·purge·edit.
// 스냅샷은 고칠 수 없으므로 "그 달의 스냅샷 전부를 합쳐 복원(검색 alias 없이) → 고침 → 남을 문서 확인 → 새 스냅샷 → 옛 스냅샷 삭제"로 다시 쓴다.
// 모든 조치는 --by·--reason 과 함께 .cache/archive/{team}.jsonl 에 남는다 (리뷰 없는 cold 쓰기의 감사 기록).
package archive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
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

// Service 는 cold 문서 조치다.
type Service struct {
	OS      opensearch.Client
	Config  config.Config
	Cold    cold.Store
	Indexer ingest.Indexer
	Now     func() time.Time
}

// Audit 는 모든 조치에 필요한 감사 필드다.
type Audit struct {
	By     string
	Reason string
}

func (a Audit) validate() error {
	if strings.TrimSpace(a.By) == "" || strings.TrimSpace(a.Reason) == "" {
		return errors.New("-by 와 -reason 이 필요합니다 (cold 쓰기의 감사 기록)")
	}
	return nil
}

func (s *Service) ledger() *state.ArchiveState { return state.NewArchiveState(s.Config.MetaRepoRoot) }

func (s *Service) record(team string, r state.ArchiveRecord) error {
	r.At = s.Now().UTC().Format(time.RFC3339)
	return s.ledger().Ledger.Append(team, r)
}

// locate 는 cold store 에 보관된 문서의 청크와 그 청크가 들어간 인덱스다.
func (s *Service) locate(ctx context.Context, team, path string) ([]model.Chunk, string, error) {
	chunks, ok, err := cold.Archived(ctx, s.Cold, team, path)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("team %s 의 cold store 에 %s 가 없습니다 (-team 확인)", team, path)
	}
	return chunks, naming.IndexFor(chunks[0]), nil
}

// Deprecate 는 문서를 틀린 것으로 표시한다 — 기본 검색에서 빠지고, 복원해도 표시가 다시 입혀진다.
func (s *Service) Deprecate(ctx context.Context, team, path, supersededBy string, a Audit) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	if _, _, err := s.locate(ctx, team, path); err != nil {
		return "", err
	}
	if err := s.record(team, state.ArchiveRecord{Action: state.ArchiveDeprecate, Path: path, SupersededBy: supersededBy, By: a.By, Reason: a.Reason}); err != nil {
		return "", err
	}
	if err := opensearch.UpdateDoc(ctx, s.OS, []string{naming.AllPattern}, s.Config.Workspace, path, map[string]any{"deprecated": true}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: deprecated — 기본 검색에서 빠집니다 (include_superseded 로는 보인다). 기록: .cache/archive/%s.jsonl", path, team), nil
}

// Undeprecate 는 deprecate 를 되돌린다.
func (s *Service) Undeprecate(ctx context.Context, team, path string, a Audit) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	if err := s.record(team, state.ArchiveRecord{Action: state.ArchiveRestore, Path: path, By: a.By, Reason: a.Reason}); err != nil {
		return "", err
	}
	if err := opensearch.UpdateDoc(ctx, s.OS, []string{naming.AllPattern}, s.Config.Workspace, path, map[string]any{"deprecated": false}); err != nil {
		return "", err
	}
	return path + ": deprecated 해제", nil
}

// Purge 는 cold 문서를 원본(raw)·청크·스냅샷에서 지운다 (개인정보 등). hot 에 파일이 있으면 거부한다.
func (s *Service) Purge(ctx context.Context, team, path string, a Audit) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	if s.hotFileExists(path) {
		return "", fmt.Errorf("%s 가 hot 에 있습니다 — 파일을 먼저 지우세요 (purge 는 cold 문서용)", path)
	}
	_, index, err := s.locate(ctx, team, path)
	if err != nil {
		return "", err
	}
	snaps, err := s.rewriteIndex(ctx, index, path, func(target string) error {
		_, err := opensearch.DeleteDoc(ctx, s.OS, []string{target}, s.Config.Workspace, path)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := cold.DropFromManifest(ctx, s.Cold, team, index, path); err != nil {
		return "", err
	}
	if err := s.Cold.Remove(ctx, cold.ChunksKey(team, path)); err != nil {
		return "", fmt.Errorf("cold store chunks: %w", err)
	}
	if err := s.Cold.Remove(ctx, cold.RawKey(team, path)); err != nil {
		return "", fmt.Errorf("cold store raw: %w", err)
	}
	if err := s.record(team, state.ArchiveRecord{Action: state.ArchivePurge, Path: path, Snapshots: snaps, By: a.By, Reason: a.Reason}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: purged — %s 에서 지우고 스냅샷 %d개를 다시 썼습니다", path, index, len(snaps)), nil
}

// Edit 는 cold 문서의 본문을 file 의 내용으로 바꾼다. 날짜가 바뀌어 다른 달로 가야 하면 거부한다
// (그건 hot 에 새로 쓰는 일이다).
func (s *Service) Edit(ctx context.Context, team, path, file string, a Audit) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	if s.hotFileExists(path) {
		return "", fmt.Errorf("%s 가 hot 에 있습니다 — hot 문서는 파일을 고치면 됩니다", path)
	}
	old, index, err := s.locate(ctx, team, path)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	built, err := document.Build(document.Input{
		Workspace: s.Config.Workspace, SourcePath: path, Content: string(content), DefaultTeam: team, TeamOverride: team,
		FallbackTime: old[0].EventTimeValue(), GitSHA: old[0].GitSHA, Now: s.Now(), Size: int64(len(content)), Location: s.Config.Location,
	})
	if err != nil {
		return "", err
	}
	// 표시는 본문을 고쳐도 이어진다: deprecated 는 조치 원장이, superseded_by 는 스냅샷에 든 청크가 정본이다
	// (데몬은 episodic 의 superseded_by 를 인덱스에서만 고치므로 cold store 청크 사본은 낡았을 수 있다)
	deprecated, err := s.ledger().Deprecated(team)
	if err != nil {
		return "", err
	}
	setFlags := func(supersededBy string) {
		for i := range built.Chunks {
			built.Chunks[i].SupersededBy = supersededBy
			built.Chunks[i].Deprecated = deprecated[path]
		}
	}
	setFlags(old[0].SupersededBy)
	if len(built.Chunks) == 0 || naming.IndexFor(built.Chunks[0]) != index {
		return "", fmt.Errorf("고친 본문의 날짜가 %s 밖입니다 — 날짜를 바꾸려면 hot 에 새 문서로 쓰세요", index)
	}
	snaps, err := s.rewriteIndex(ctx, index, "", func(target string) error {
		current, err := ingest.DocChunks(ctx, s.OS, []string{target}, s.Config.Workspace, path)
		if err != nil {
			return err
		}
		if len(current) > 0 {
			setFlags(current[0].Source.SupersededBy)
		}
		if _, err := opensearch.DeleteDoc(ctx, s.OS, []string{target}, s.Config.Workspace, path); err != nil {
			return err
		}
		_, err = s.Indexer.BulkChunks(ctx, built.Chunks)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := s.Cold.Put(ctx, cold.RawKey(team, path), string(content)); err != nil {
		return "", fmt.Errorf("cold store raw: %w", err)
	}
	if err := s.Cold.Put(ctx, cold.ChunksKey(team, path), cold.EncodeChunks(built.Chunks)); err != nil {
		return "", fmt.Errorf("cold store chunks: %w", err)
	}
	if err := s.record(team, state.ArchiveRecord{Action: state.ArchiveEdit, Path: path, ContentSHA: built.ContentSHA, Snapshots: snaps, By: a.By, Reason: a.Reason}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: edited — %s 의 본문을 바꾸고 스냅샷 %d개를 다시 썼습니다", path, index, len(snaps)), nil
}

func (s *Service) hotFileExists(path string) bool {
	_, err := os.Stat(filepath.Join(s.Config.MetaRepoRoot, filepath.FromSlash(path)))
	return !errors.Is(err, fs.ErrNotExist)
}

// rewriteIndex 는 index 를 담은 스냅샷들을 mutate 를 적용한 새 스냅샷 하나로 바꾼다. 새 스냅샷 이름을 돌려준다.
// 같은 달의 스냅샷이 여럿이면(tier 뒤 늦게 들어온 문서) 전부를 합친 뒤 고친다 — 최신 하나만 고치고 옛것을 지우면
// 옛 스냅샷에만 있던 문서를 잃는다. drop 은 mutate 가 지우는 문서다 (purge). 그 밖의 문서가 하나라도 빠졌으면
// 옛 스냅샷을 지우지 않는다. 복원본이 열려 있으면 그 자리에서, 닫혀 있으면 검색 alias 없이 잠깐 복원해 고친다.
func (s *Service) rewriteIndex(ctx context.Context, index, drop string, mutate func(target string) error) (written []string, err error) {
	old, err := cold.SnapshotsOf(ctx, s.OS, s.Config, index)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(old))
	for _, snap := range old {
		if len(snap.Indices) != 1 {
			return nil, fmt.Errorf("스냅샷 %s 에 다른 인덱스도 들어 있어 다시 쓸 수 없습니다: %v", snap.Name, snap.Indices)
		}
		names = append(names, snap.Name)
	}
	restored, hot, err := cold.OpenState(ctx, s.OS, index)
	switch {
	case err != nil:
		return nil, err
	case len(names) == 0 && !restored && !hot:
		return nil, fmt.Errorf("%s 를 담은 스냅샷이 없습니다", index)
	case len(names) == 0:
		return nil, mutate(index) // cold 로 간 적 없는 인덱스 — 그 자리에서 고치면 끝이다
	case hot:
		return nil, cold.Reopened(index)
	}
	if !restored {
		// 잠깐 복원한 인덱스는 어느 길로 끝나든 지운다 — 남으면 alias 없는 인덱스가 열린 채로 남고, 다음 조치가 그걸 hot 으로 착각한다
		defer func() {
			if derr := s.OS.Do(context.WithoutCancel(ctx), http.MethodDelete, "/"+index, nil, nil, nil); derr != nil && err == nil {
				err = fmt.Errorf("delete temporary %s: %w", index, derr)
			}
		}()
	}
	if err := cold.RestoreAll(ctx, s.OS, s.Config, index, names); err != nil {
		return nil, err
	}
	keep, err := cold.DocPaths(ctx, s.OS, index)
	if err != nil {
		return nil, err
	}
	delete(keep, drop)
	if err := mutate(index); err != nil {
		return nil, err
	}
	return s.replaceSnapshots(ctx, index, names, keep)
}

// replaceSnapshots 는 index 의 지금 내용을 새 스냅샷으로 뜨고, 남아야 할 문서(keep)가 모두 들어 있을 때만 옛 스냅샷과
// 그 목록 줄을 지운다.
func (s *Service) replaceSnapshots(ctx context.Context, index string, old []string, keep map[string]bool) ([]string, error) {
	missing, err := cold.MissingDocs(ctx, s.OS, index, keep)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("다시 쓴 %s 에 문서 %d개가 빠져 옛 스냅샷을 지우지 않습니다: %v", index, len(missing), missing)
	}
	name, err := cold.SnapshotIndex(ctx, s.OS, s.Config, index, s.Now())
	if err != nil {
		return nil, err
	}
	// 새 스냅샷의 목록을 통째로 남긴다 — 이 달이 ISM 스냅샷뿐이었어도 이제 목록이 덮는다
	if err := cold.RecordIndexManifest(ctx, s.OS, s.Cold, s.Config, index, name); err != nil {
		return nil, err
	}
	idx, _ := naming.ParseIndex(index)
	deleted := make([]string, 0, len(old))
	for _, snap := range old {
		if err := cold.DeleteSnapshot(ctx, s.OS, s.Config, snap); err != nil {
			return []string{name}, errors.Join(err, cold.DropSnapshotsFromManifest(ctx, s.Cold, idx.Team, index, deleted))
		}
		deleted = append(deleted, snap)
	}
	return []string{name}, cold.DropSnapshotsFromManifest(ctx, s.Cold, idx.Team, index, deleted)
}
