#!/usr/bin/env bash
# 이슈 worktree 하나를 지운다. 사용법: _remove_worktree.sh <repo-dir> <issue> [--force]
# 커밋하지 않은 변경·untracked 파일이 있으면 잃게 될 파일을 보여 주고 멈춘다 — --force 일 때만 버리고 지운다.
# 지우기는 git worktree remove 만 쓴다: git 이 거부하는 것(lock 건 worktree, worktree 가 아닌 디렉토리)은 --force 여도 남긴다.
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
FORCE="${3:-}"
case "$FORCE" in
    ''|--force) ;;
    *) echo "usage: $(basename "$0") <repo-dir> <issue> [--force]" >&2; exit 2 ;;
esac
REPO_NAME=$(basename "$REPO_DIR")
WORKTREES_DIR="${REPO_DIR}/.worktrees"

found=0
failed=0

# worktree 의 커밋하지 않은 변경과 untracked 파일 (git status --short). worktree 가 아니면(.git 파일이 없다) 빈 문자열
pending_changes() {
    local wt_dir="$1"
    [[ -f "${wt_dir}/.git" ]] || return 0
    git -C "$wt_dir" status --short --untracked-files=all
}

# worktree 하나를 지운다. 지웠으면 0, 남겼으면 1
remove_one() {
    local wt_dir="$1" wt_name changes err
    wt_name=$(basename "$wt_dir")
    changes=$(pending_changes "$wt_dir") || {
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 의 상태를 읽지 못해 남겨 둡니다" >&2
        return 1
    }
    if [[ -n "$changes" && "$FORCE" != "--force" ]]; then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 에 커밋하지 않은 변경이 있어 남겨 둡니다 — 지우면 잃는 것:" >&2
        printf '%s\n' "$changes" | sed 's/^/    /' >&2
        echo "[done-issue] ${REPO_NAME}: 커밋·stash 하거나, 버려도 되면 --force 를 붙여 다시 실행하세요" >&2
        return 1
    fi
    if [[ -n "$changes" ]]; then
        echo "[done-issue] ${REPO_NAME}: --force — worktree '${wt_name}' 의 다음 변경을 버립니다:"
        printf '%s\n' "$changes" | sed 's/^/    /'
    fi
    if ! err=$(git -C "$REPO_DIR" worktree remove ${FORCE:+--force} "$wt_dir" 2>&1); then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 를 지우지 못해 남겨 둡니다 — ${err}" >&2
        echo "[done-issue] ${REPO_NAME}: lock 이면 'git worktree unlock', 이미 없는 worktree 기록이면 'git -C ${REPO_DIR} worktree prune' 후 확인하세요" >&2
        return 1
    fi
    echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' removed"
}

# worktree 는 main/{repo}/.worktrees/{issue}[-N] 에 있다 (run-issues/_create_worktree.sh).
# .git/worktrees/ 는 git 의 내부 관리 디렉토리라 거기를 직접 지우면 안 된다.
for wt_dir in "${WORKTREES_DIR}/${ISSUE}" "${WORKTREES_DIR}/${ISSUE}"-[0-9]*; do
    [[ -d "$wt_dir" ]] || continue
    found=1
    remove_one "$wt_dir" || failed=1
done

if [[ $found -eq 0 ]]; then
    echo "[done-issue] ${REPO_NAME}: no worktree found for issue '${ISSUE}'"
fi
exit "$failed"
