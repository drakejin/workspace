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
        ((skipped++))
        continue
    fi

    if git -C "$repo" pull origin "$branch" --quiet 2>/dev/null; then
        echo "[sync] ${name}: pulled '${branch}'"
        ((synced++))
    else
        echo "[fail] ${name}: pull failed on '${branch}'"
        ((failed++))
    fi
done

total=$((synced + skipped + failed))
echo ""
echo "done: ${total} repos / ${synced} synced / ${skipped} skipped / ${failed} failed"

[[ $failed -eq 0 ]]
