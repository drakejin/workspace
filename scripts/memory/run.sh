#!/usr/bin/env bash
# 운영 CLI 진입점 — make daemon-status / query / tier / questions / answer ... 가 부른다.
# 사용법: scripts/memory/run.sh <command> [flags]   (.cache/bin/memory-mcp help)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"
memory_build "$ROOT_DIR"

exec .cache/bin/memory-mcp "$@"
