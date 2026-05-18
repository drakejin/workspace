#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GITHUB_HOST="$1"
ORG="$2"
REPOS_DIR="$3"
WORKSPACE_JSON="$4"

mkdir -p "$REPOS_DIR"

repos=$(jq -r '.workspaces[].name' "$WORKSPACE_JSON")

cloned=0
skipped=0
failed=0

echo "git remote: https://${GITHUB_HOST}/${ORG}"
echo ""

while IFS= read -r repo_name; do
    [[ -z "$repo_name" ]] && continue

    output=$("${SCRIPT_DIR}/clone-repo.sh" "$GITHUB_HOST" "$ORG" "$repo_name" "$REPOS_DIR" 2>&1) && rc=0 || rc=$?
    echo "$output"

    if [[ $rc -ne 0 ]]; then
        failed=$((failed + 1))
    elif echo "$output" | grep -q '^\[skip\]'; then
        skipped=$((skipped + 1))
    else
        cloned=$((cloned + 1))
    fi
done <<< "$repos"

total=$((cloned + skipped + failed))
echo ""
echo "done: ${total} repos / ${cloned} cloned / ${skipped} skipped / ${failed} failed"

[[ $failed -eq 0 ]]
