#!/usr/bin/env bash
set -euo pipefail

REPOS_DIR="$1"

synced=0
skipped=0
failed=0

for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue

    name=$(basename "$repo")
    branch=$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null)

    if [[ "$branch" != "main" && "$branch" != "master" ]]; then
        echo "[skip] ${name}: on branch '${branch}'"
        skipped=$((skipped + 1))
        continue
    fi

    if git -C "$repo" pull --ff-only origin "$branch" --quiet 2>/dev/null; then
        echo "[sync] ${name}: pulled '${branch}'"
        synced=$((synced + 1))
    else
        echo "[fail] ${name}: pull failed on '${branch}'" >&2
        failed=$((failed + 1))
    fi
done

total=$((synced + skipped + failed))
echo "[sync] summary: ${total} repos / ${synced} synced / ${skipped} skipped / ${failed} failed"

[[ $failed -eq 0 ]]
