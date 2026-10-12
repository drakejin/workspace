#!/usr/bin/env bash
# bin/memory-mcp, bin/memory-daemon 빌드
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${SCRIPT_DIR}/../memory-common.sh"

memory_build "$ROOT_DIR"
echo "[build] bin/memory-mcp, bin/memory-daemon"
