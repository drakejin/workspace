#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$1"
HOOKS_SRC="${ROOT_DIR}/scripts/hooks"
HOOKS_DST="${ROOT_DIR}/.git/hooks"

for hook in "${HOOKS_SRC}"/*; do
    [[ -f "$hook" ]] || continue
    hook_name=$(basename "$hook")
    ln -sf "../../scripts/hooks/${hook_name}" "${HOOKS_DST}/${hook_name}"
    echo "[hook] ${hook_name}: installed"
done
