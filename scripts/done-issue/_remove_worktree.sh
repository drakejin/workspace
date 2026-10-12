#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
REPO_NAME=$(basename "$REPO_DIR")
WORKTREES_DIR="${REPO_DIR}/.worktrees"

found=0

# worktree 는 main/{repo}/.worktrees/{issue}[-N] 에 있다 (run-issues/_create_worktree.sh).
# .git/worktrees/ 는 git 의 내부 관리 디렉토리라 거기를 직접 지우면 안 된다.
for wt_dir in "${WORKTREES_DIR}/${ISSUE}" "${WORKTREES_DIR}/${ISSUE}"-[0-9]*; do
    [[ -d "$wt_dir" ]] || continue

    wt_name=$(basename "$wt_dir")
    if git -C "$REPO_DIR" worktree remove --force "$wt_dir" 2>/dev/null; then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' removed"
    else
        echo "[done-issue] ${REPO_NAME}: failed to remove worktree '${wt_name}', trying cleanup"
        rm -rf "$wt_dir"
        git -C "$REPO_DIR" worktree prune
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' pruned"
    fi
    found=1
done

if [[ $found -eq 0 ]]; then
    echo "[done-issue] ${REPO_NAME}: no worktree found for issue '${ISSUE}'"
fi
