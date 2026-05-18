#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"
WORKSPACE_JSON="${ROOT_DIR}/workspace.json"

usage() {
    echo "usage: run.sh <issue> <repo1> [repo2] ..."
    echo ""
    echo "example: run.sh ADSDATA-184 ads-genisys ads-serving"
    exit 1
}

if [[ $# -lt 2 ]]; then
    usage
fi

ISSUE="$1"
shift
REPOS=("$@")

for repo in "${REPOS[@]}"; do
    REPO_DIR="${REPOS_DIR}/${repo}"
    if [[ ! -d "${REPO_DIR}/.git" ]]; then
        echo "[run-issues] ${repo}: not a git repository, skipping"
        continue
    fi

    "${SCRIPT_DIR}/_create_worktree.sh" "$REPO_DIR" "$ISSUE"
done

"${SCRIPT_DIR}/_update_workspace.sh" "$REPOS_DIR" "$WORKSPACE_JSON"
