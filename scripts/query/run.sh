#!/usr/bin/env bash
# 검색 — make query Q="검색어" [TEAM=팀]. 에이전트는 MCP search_memory 로 같은 검색을 한다
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

if [[ $# -lt 1 || -z "$1" ]]; then
    echo "usage: make query Q=\"검색어\" [TEAM=팀]"
    exit 1
fi
query="$1"
shift
exec pnpm -s memory search "$query" "$@"
