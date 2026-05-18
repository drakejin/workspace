#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$1"
SPECS_DIR="${ROOT_DIR}/docs/specs"

if [[ ! -d "$SPECS_DIR" ]]; then
    echo "[skip] docs/specs: not found"
    exit 0
fi

count=0
for entry in "${SPECS_DIR}"/*/; do
    [[ -d "$entry" ]] || continue
    rm -rf "$entry"
    echo "[clean] docs/specs/$(basename "$entry")"
    ((count++))
done

echo ""
echo "done: ${count} specs cleaned"
