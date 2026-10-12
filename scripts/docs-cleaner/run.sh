#!/usr/bin/env bash
# docs/issues/*, docs/specs/* 를 모두 지운다 (파괴적). 묻고 지운다 — CONFIRM=yes 면 묻지 않는다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

if [[ "${CONFIRM:-}" != "yes" ]]; then
    answer=""
    read -r -p "[docs-cleaner] docs/issues/*, docs/specs/* 를 모두 지웁니다. 계속할까요? [y/N] " answer || true
    if [[ "$answer" != "y" && "$answer" != "Y" ]]; then
        echo "[docs-cleaner] 취소"
        exit 1
    fi
fi

"${SCRIPT_DIR}/_clean_issues.sh" "$ROOT_DIR"
"${SCRIPT_DIR}/_clean_specs.sh" "$ROOT_DIR"
