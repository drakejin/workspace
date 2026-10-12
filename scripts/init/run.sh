#!/usr/bin/env bash
# 최초 세팅 — git hook 설치 → workspace.seed.json 의 리포를 main/ 에 clone → sync.
# 다시 실행해도 안전하다 (이미 있는 리포는 건너뛴다). seed 에 리포를 추가했으면 다시 실행한다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"
SEED_JSON="${ROOT_DIR}/workspace.seed.json"

if [[ ! -f "$SEED_JSON" ]]; then
    echo "[init] error: workspace.seed.json not found" >&2
    exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
    echo "[init] error: jq 가 필요합니다 (brew install jq)" >&2
    exit 1
fi

github_host=$(jq -r '.githubHost // empty' "$SEED_JSON")
org=$(jq -r '.org // empty' "$SEED_JSON")
if [[ -z "$github_host" || -z "$org" ]]; then
    echo "[init] error: workspace.seed.json 에 githubHost 와 org 가 필요합니다" >&2
    exit 1
fi

"${SCRIPT_DIR}/_install_hooks.sh" "$ROOT_DIR"
echo ""
"${SCRIPT_DIR}/_clone_repos.sh" "$github_host" "$org" "$REPOS_DIR" "$SEED_JSON"
echo ""
"${SCRIPT_DIR}/../sync/run.sh"
