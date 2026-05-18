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
        echo "[done-issue] ${repo}: not a git repository, skipping"
        continue
    fi

    "${SCRIPT_DIR}/_remove_worktree.sh" "$REPO_DIR" "$ISSUE"
done

UPDATE_SCRIPT="${ROOT_DIR}/scripts/run-issues/_update_workspace.sh"
"${UPDATE_SCRIPT}" "$REPOS_DIR" "$WORKSPACE_JSON"
