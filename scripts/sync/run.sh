#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"
WORKSPACE_JSON="${ROOT_DIR}/workspace.json"

"${SCRIPT_DIR}/_pull_repos.sh" "$REPOS_DIR" || true
"${SCRIPT_DIR}/_collect_workspace.sh" "$REPOS_DIR" "$WORKSPACE_JSON"
"${SCRIPT_DIR}/_update_index.sh" "$REPOS_DIR" "${ROOT_DIR}/INDEX.md"
