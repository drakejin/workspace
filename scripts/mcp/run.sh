#!/usr/bin/env bash
# MCP 서버 (stdio) — .mcp.json 이 이걸 부른다. stdout 은 MCP 프로토콜 전용이라 빌드 출력도 stderr 로 보낸다
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"
# 빌드가 실패해도(이 레포의 Go 코드를 고치는 중) 기존 바이너리가 있으면 그것으로 뜬다 — 출력은 모두 stderr
memory_build_or_reuse "$ROOT_DIR" memory-mcp

exec .cache/bin/memory-mcp serve
