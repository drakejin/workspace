package daemon

import (
	"context"

	"github.com/drakejin/workspace/app/internal/cold"
)

// gitProbe 는 사이클 하나 동안 한 번만 읽는 git 상태(cold.GitState)다 — tier 가 hot 에 남긴 cold 원본을 원본마다·사이클마다 git
// 프로세스 여럿으로 다시 보지 않고, 이 값이 그 원본을 마지막으로 본 때와 같으면 지난 답을 쓴다. 사이클은 직렬이라 잠그지 않는다.
type gitProbe struct {
	read  bool
	state string
	err   error
}

func (p *gitProbe) get(ctx context.Context, root string) (string, error) {
	if !p.read {
		p.state, p.err = cold.GitState(ctx, root)
		p.read = true
	}
	return p.state, p.err
}

// recheckKept 는 tier 가 hot 에 남긴(kept) cold 소유 원본이 아직 남는 원본인가를 다시 본다 — 커밋해도 원본의 stat 은 그대로라
// 기억한 stat 만으로는 알 수 없다. git 상태가 그 원본을 마지막으로 본 때와 같으면 git 을 부르지 않는다. git 으로 확인하지 못하면
// 남긴다고 보고(tier 도 지우지 않는다) 다음 사이클에 다시 본다.
func (r *reconciler) recheckKept(ctx context.Context, rel string, c coldOwnedFile, git *gitProbe) coldOwnedFile {
	state, err := git.get(ctx, r.Config.MetaRepoRoot)
	if err == nil && c.gitState == state {
		return c
	}
	kept := true
	if err == nil {
		kept, err = cold.KeptByGit(ctx, r.Config.MetaRepoRoot, rel)
	}
	if err != nil {
		r.warnGit(err)
		return coldOwnedFile{stat: c.stat, kept: true}
	}
	r.gitWarned = ""
	return coldOwnedFile{stat: c.stat, kept: kept, gitState: state}
}

// warnGit 은 남긴 원본의 커밋 여부를 git 으로 확인하지 못한 까닭을 알린다 — 같은 까닭이면 원본마다·사이클마다 되풀이하지 않는다
// (git 이 없거나 레포를 거부하면 모든 원본이 같은 까닭으로 실패한다).
func (r *reconciler) warnGit(err error) {
	if msg := err.Error(); msg != r.gitWarned {
		r.gitWarned = msg
		r.Log("[reconcile] tier 가 hot 에 남긴 cold 원본의 커밋 여부를 git 으로 확인하지 못했습니다 — cold_kept 로 세고 tier 도 지우지 않는다: %v", err)
	}
}
