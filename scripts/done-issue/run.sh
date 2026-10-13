#!/usr/bin/env bash
# 이슈 작업 공간 정리 — main/{repo}/.worktrees/{issue} worktree 를 지운다 (브랜치는 남긴다).
# 커밋하지 않은 변경·untracked 파일이 남은 worktree 는 잃게 될 파일을 보여 주고 남겨 둔다 (exit 1). 버려도 되면 --force.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"

usage() {
    echo "usage: run.sh [--force] <issue> <repo1> [repo2] ..." >&2
    echo "example: run.sh ISSUE-1 books-guitar" >&2
    echo "  --force  커밋하지 않은 변경·untracked 파일이 있어도 버리고 지운다" >&2
    exit 1
}

force=""
args=()
for arg in "$@"; do
    case "$arg" in
        --force) force="--force" ;;
        -*) usage ;;
        *) args+=("$arg") ;;
    esac
done
[[ ${#args[@]} -lt 2 ]] && usage

ISSUE="${args[0]}"
failed=0

for repo in "${args[@]:1}"; do
    REPO_DIR="${REPOS_DIR}/${repo}"
    if [[ ! -d "${REPO_DIR}/.git" ]]; then
        echo "[done-issue] ${repo}: not a git repository, skipping"
        continue
    fi
    "${SCRIPT_DIR}/_remove_worktree.sh" "$REPO_DIR" "$ISSUE" ${force:+"$force"} || failed=1
done
exit "$failed"
