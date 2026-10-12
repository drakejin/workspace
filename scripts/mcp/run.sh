#!/usr/bin/env bash
# MCP 서버 (stdio) — .mcp.json 이 이걸 부른다. stdout 은 MCP 프로토콜 전용이라 아무것도 찍지 않는다
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

exec pnpm -s mcp
