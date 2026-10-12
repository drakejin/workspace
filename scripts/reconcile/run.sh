#!/usr/bin/env bash
# 1회 색인 — 데몬이 켜져 있으면 필요 없다 (돌고 있으면 건너뛴다)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

exec pnpm -s reconcile
