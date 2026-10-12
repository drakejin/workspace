#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
REPO_NAME=$(basename "$REPO_DIR")

WORKTREE_NAME="$ISSUE"
WORKTREE_PATH="${REPO_DIR}/.worktrees/${WORKTREE_NAME}"
BRANCH_NAME="$ISSUE"

suffix=0
while [[ -d "$WORKTREE_PATH" ]]; do
    suffix=$((suffix + 1))
    WORKTREE_NAME="${ISSUE}-${suffix}"
    WORKTREE_PATH="${REPO_DIR}/.worktrees/${WORKTREE_NAME}"
    BRANCH_NAME="${ISSUE}-${suffix}"
done

# 관리 대상 리포가 .worktrees/ 를 untracked 로 보지 않게 (git add -A 사고 방지)
exclude_file="$(git -C "$REPO_DIR" rev-parse --git-path info/exclude)"
[[ "$exclude_file" = /* ]] || exclude_file="${REPO_DIR}/${exclude_file}"
mkdir -p "$(dirname "$exclude_file")"
grep -qxF '.worktrees/' "$exclude_file" 2>/dev/null || echo '.worktrees/' >> "$exclude_file"

main_branch=$(git -C "$REPO_DIR" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || echo "main")
main_branch="${main_branch:-main}"

if git -C "$REPO_DIR" show-ref --verify --quiet "refs/heads/${BRANCH_NAME}" 2>/dev/null; then
    git -C "$REPO_DIR" worktree add "$WORKTREE_PATH" "$BRANCH_NAME"
    echo "[run-issues] ${REPO_NAME}: worktree '${WORKTREE_NAME}' created (existing branch '${BRANCH_NAME}')"
else
    git -C "$REPO_DIR" fetch origin "$main_branch" --quiet || echo "[run-issues] ${REPO_NAME}: fetch failed, using cached origin/${main_branch}"
    git -C "$REPO_DIR" worktree add --no-track -b "$BRANCH_NAME" "$WORKTREE_PATH" "origin/${main_branch}"
    echo "[run-issues] ${REPO_NAME}: worktree '${WORKTREE_NAME}' created (new branch '${BRANCH_NAME}' from origin/${main_branch})"
fi
