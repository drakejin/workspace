#!/usr/bin/env bash
# 이슈 작업 공간 정리 — main/{repo}/.worktrees/{issue} worktree 를 지운다 (브랜치는 남긴다).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"

usage() {
    echo "usage: run.sh <issue> <repo1> [repo2] ..." >&2
    echo "example: run.sh ISSUE-1 books-guitar" >&2
    exit 1
}

[[ $# -lt 2 ]] && usage

ISSUE="$1"
shift

for repo in "$@"; do
    REPO_DIR="${REPOS_DIR}/${repo}"
    if [[ ! -d "${REPO_DIR}/.git" ]]; then
        echo "[done-issue] ${repo}: not a git repository, skipping"
        continue
    fi
    "${SCRIPT_DIR}/_remove_worktree.sh" "$REPO_DIR" "$ISSUE"
done
