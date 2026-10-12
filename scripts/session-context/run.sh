#!/usr/bin/env bash
# Claude Code SessionStart 훅 (.claude/settings.json) — 세션 디렉토리에 맞는 메모리를 맥락으로 넣는다.
# 세션을 막으면 안 되므로 빌드·검색이 실패해도 아무것도 출력하지 않고 0 으로 끝난다.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"
memory_build "$ROOT_DIR" >/dev/null 2>&1 || exit 0
"${ROOT_DIR}/.cache/bin/memory-mcp" session-context 2>/dev/null || true
exit 0
