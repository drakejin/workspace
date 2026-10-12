#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$1"
ISSUES_DIR="${ROOT_DIR}/docs/issues"

if [[ ! -d "$ISSUES_DIR" ]]; then
    echo "[skip] docs/issues: not found"
    exit 0
fi

count=0
for entry in "${ISSUES_DIR}"/*/; do
    [[ -d "$entry" ]] || continue
    rm -rf "$entry"
    echo "[clean] docs/issues/$(basename "$entry")"
    count=$((count + 1))
done

echo ""
echo "done: ${count} issues cleaned"
