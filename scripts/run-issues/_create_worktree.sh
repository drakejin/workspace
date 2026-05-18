#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
REPO_NAME=$(basename "$REPO_DIR")

WORKTREE_NAME="$ISSUE"
WORKTREE_PATH="${REPO_DIR}/.git/worktrees/${WORKTREE_NAME}"
BRANCH_NAME="$ISSUE"

suffix=0
while [[ -d "$WORKTREE_PATH" ]]; do
    suffix=$((suffix + 1))
    WORKTREE_NAME="${ISSUE}-${suffix}"
    WORKTREE_PATH="${REPO_DIR}/.git/worktrees/${WORKTREE_NAME}"
    BRANCH_NAME="${ISSUE}-${suffix}"
done

main_branch=$(git -C "$REPO_DIR" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || echo "main")

if git -C "$REPO_DIR" show-ref --verify --quiet "refs/heads/${BRANCH_NAME}" 2>/dev/null; then
    git -C "$REPO_DIR" worktree add "$WORKTREE_PATH" "$BRANCH_NAME"
    echo "[run-issues] ${REPO_NAME}: worktree '${WORKTREE_NAME}' created (existing branch '${BRANCH_NAME}')"
else
    git -C "$REPO_DIR" worktree add -b "$BRANCH_NAME" "$WORKTREE_PATH" "origin/${main_branch}"
    echo "[run-issues] ${REPO_NAME}: worktree '${WORKTREE_NAME}' created (new branch '${BRANCH_NAME}' from origin/${main_branch})"
fi
