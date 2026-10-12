#!/usr/bin/env bash
# .cache/bin/memory-mcp, .cache/bin/memory-daemon 빌드 (app/ 에서)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${SCRIPT_DIR}/../memory-common.sh"

memory_build "$ROOT_DIR"
echo "[build] .cache/bin/memory-mcp, .cache/bin/memory-daemon"
