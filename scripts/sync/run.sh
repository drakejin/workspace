#!/usr/bin/env bash
# 최신화 — 기본 브랜치(origin/HEAD)에 있는 리포만 pull 하고, INDEX.md 에 없는 리포가 있으면 행을 추가한다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"

"${SCRIPT_DIR}/_pull_repos.sh" "$REPOS_DIR" || true
"${SCRIPT_DIR}/_update_index.sh" "$REPOS_DIR" "${ROOT_DIR}/INDEX.md"
