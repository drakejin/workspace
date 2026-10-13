#!/usr/bin/env bash
# 운영 CLI 진입점 — .env 를 읽고(바뀐 게 있으면 빌드하고) memory-mcp <command> 를 부른다.
# 사용법: scripts/memory/run.sh <command> [flags]   명령 목록: scripts/memory/run.sh help
#   search · questions · answer · compaction-report · tier · audit · archive · graph · list-cold · restore · reload · bench ...
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"
memory_build "$ROOT_DIR"

exec .cache/bin/memory-mcp "$@"
