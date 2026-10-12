#!/usr/bin/env bash
# 데몬 상태 — 컨테이너 · OpenSearch · 색인 · 데몬 프로세스 · 판정 질문 · 증류를 한 화면에. 읽기만 한다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

if [[ ! -d node_modules ]]; then
    echo "[status] node_modules 없음 — 'make memory-up' 을 먼저 실행하세요"
    exit 0
fi
exec pnpm -s memory status
