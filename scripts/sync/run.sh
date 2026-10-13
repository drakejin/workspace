#!/usr/bin/env bash
# 최신화 — 기본 브랜치(origin/HEAD)나 main/master 에 있는 리포만 pull 하고, INDEX.md 에 없는 리포가 있으면 행을 추가한다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"

# pull 이 실패한 repo 가 있어도 INDEX.md 단계는 돈다 — 실패는 삼키지 않고 끝에 알린 뒤 0 이 아닌 값으로 끝낸다
pull_status=0
"${SCRIPT_DIR}/_pull_repos.sh" "$REPOS_DIR" || pull_status=$?
"${SCRIPT_DIR}/_update_index.sh" "$REPOS_DIR" "${ROOT_DIR}/INDEX.md"
if [[ $pull_status -ne 0 ]]; then
    echo "[sync] pull 하지 못한 repo 가 있습니다 — 위의 [fail] 줄을 확인하세요" >&2
    exit "$pull_status"
fi
