#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
REPO_NAME=$(basename "$REPO_DIR")
GIT_DIR="${REPO_DIR}/.git"

found=0

for wt_dir in "${GIT_DIR}/worktrees/${ISSUE}" "${GIT_DIR}/worktrees/${ISSUE}"-[0-9]*; do
    [[ -d "$wt_dir" ]] || continue

    wt_name=$(basename "$wt_dir")
    git -C "$REPO_DIR" worktree remove --force "$wt_dir" 2>/dev/null || {
        echo "[done-issue] ${REPO_NAME}: failed to remove worktree '${wt_name}', trying cleanup"
        rm -rf "$wt_dir"
        git -C "$REPO_DIR" worktree prune
    }
    echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' removed"
    found=1
done

if [[ $found -eq 0 ]]; then
    echo "[done-issue] ${REPO_NAME}: no worktree found for issue '${ISSUE}'"
fi
