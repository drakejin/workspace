package daemon

import (
	"fmt"
	"sort"

	"github.com/drakejin/workspace/app/internal/document"
	"github.com/drakejin/workspace/app/internal/model"
	"github.com/drakejin/workspace/app/internal/state"
)

// 판정 대기분은 .cache/daemon/judge-pending.json 에 남는다 (state.WriteJudgePending). 변경분은 그 사이클에만 있어서 메모리에만
// 두면 종료·크래시 때 잃는다 — 이미 색인에 쓴 문서는 다음 boot 에 "바뀌지 않은 파일" 로 보여 다시 편집하기 전까지 판정되지 않는다.

// judgeable 은 판정할 변경분이다. 지운 문서·개정판(supersedes 선언을 믿는다)·knowledge·본문이 없는 문서(주장이 없다)는 뺀다.
func judgeable(d DiffEntry) bool {
	return d.Change != ChangeRemoved && d.Supersedes == "" && d.Kind == model.KindEpisodic && !document.IsBlank(d.Body)
}

// latestByPath 는 문서(source_path)마다 마지막 변경분 하나만 남긴다 — 판정이 실패하는 동안 고친 문서의 옛 본문이 쌓이지 않게.
// 순서는 문서가 처음 나온 자리를 지킨다. 마지막 변경분이 지운 것이면 그대로 남겨 호출자가 거른다.
func latestByPath(entries []DiffEntry) []DiffEntry {
	pos := map[string]int{}
	out := make([]DiffEntry, 0, len(entries))
	for _, e := range entries {
		if i, ok := pos[e.SourcePath]; ok {
			out[i] = e
			continue
		}
		pos[e.SourcePath] = len(out)
		out = append(out, e)
	}
	return out
}

func pendingOf(d DiffEntry) state.PendingJudgment {
	return state.PendingJudgment{SourcePath: d.SourcePath, Team: d.Team, Project: d.Project, DocType: d.DocType, Kind: d.Kind,
		BodySHA: d.BodySHA, EventTime: d.EventTime, Title: d.Title}
}

// persist 는 판정 대기 목록을 entries 로 바꿔 쓴다. 비어 있고 이미 비어 있으면 쓰지 않는다 (사이클마다 쓰지 않게).
func (j *Judge) persist(entries []DiffEntry) error {
	root := j.Config.MetaRepoRoot
	if root == "" || (len(entries) == 0 && !j.saved) {
		return nil
	}
	pending := make(map[string]state.PendingJudgment, len(entries))
	for _, e := range entries {
		pending[e.SourcePath] = pendingOf(e)
	}
	if err := state.WriteJudgePending(root, pending); err != nil {
		return err
	}
	j.saved = len(entries) > 0
	return nil
}

// Restore 는 지난 프로세스가 판정하지 못하고 남긴 대기분을 되살린다 (boot — 다음 Run 이 이어 판정한다). 디스크 본문의 sha 가
// 그대로인 문서만 되살린다 — 그 사이 고쳤거나 지운 문서는 버린다 (고친 본문은 사이클이 새 변경분으로 넘긴다).
func (j *Judge) Restore() error {
	root := j.Config.MetaRepoRoot
	if root == "" {
		return nil
	}
	pending, err := state.ReadJudgePending(root)
	if err != nil {
		return fmt.Errorf("judge restore: %w", err)
	}
	paths := make([]string, 0, len(pending))
	for p := range pending {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	restored := make([]DiffEntry, 0, len(paths))
	for _, p := range paths {
		pj := pending[p]
		body, ok := bodyAt(root, pj.SourcePath, pj.BodySHA)
		if !ok {
			j.Log("[judge] 대기 판정 %s — 그 사이 본문이 바뀌었거나 파일이 없어 버린다 (바뀐 본문은 사이클이 다시 넘긴다)", pj.SourcePath)
			continue
		}
		restored = append(restored, DiffEntry{SourcePath: pj.SourcePath, Team: pj.Team, Project: pj.Project, DocType: pj.DocType, Kind: pj.Kind,
			Change: ChangeModified, BodySHA: pj.BodySHA, EventTime: pj.EventTime, Title: pj.Title, Body: body})
	}
	j.retry, j.saved = restored, len(pending) > 0
	if len(restored) > 0 {
		j.Log("[judge] 지난 실행이 남긴 대기 판정 %d건을 이어 판정한다", len(restored))
	}
	return nil
}
