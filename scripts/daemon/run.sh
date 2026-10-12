#!/usr/bin/env bash
# 데몬 실행 — 상주한다 (boot 1회 + 타이머 + 파일 watcher). 켜두면 색인·판정·증류가 알아서 돈다.
# 판정·증류 LLM = 로컬 claude CLI. API 키 주입 없음. 없으면 판정은 질문으로, 증류는 다음 사이클로 폴백
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

claude_bin="${MEMORY_CLAUDE_BIN:-claude}"
if command -v "$claude_bin" >/dev/null 2>&1; then
    echo "[daemon] LLM 백엔드: 로컬 claude CLI ($("$claude_bin" --version 2>/dev/null | head -1))"
else
    echo "[daemon] claude CLI 없음 — 판정은 질문으로, 증류는 다음 사이클로 폴백"
fi

exec pnpm -s daemon
