#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GITHUB_HOST="$1"
ORG="$2"
REPOS_DIR="$3"
SEED_JSON="$4"

mkdir -p "$REPOS_DIR"

cloned=0
skipped=0
failed=0

echo "[init] remote: https://${GITHUB_HOST}/${ORG}"

# 한 줄에 한 리포: 이름<TAB>docsOnly
while IFS=$'\t' read -r repo_name docs_only; do
    [[ -z "$repo_name" ]] && continue

    output=$("${SCRIPT_DIR}/clone-repo.sh" "$GITHUB_HOST" "$ORG" "$repo_name" "$REPOS_DIR" "$docs_only" 2>&1) && rc=0 || rc=$?
    echo "$output"

    if [[ $rc -ne 0 ]]; then
        failed=$((failed + 1))
    elif echo "$output" | grep -q '^\[skip\]'; then
        skipped=$((skipped + 1))
    else
        cloned=$((cloned + 1))
    fi
done < <(jq -r '.workspaces[] | [.name, (.docsOnly // false | tostring)] | @tsv' "$SEED_JSON")

total=$((cloned + skipped + failed))
echo "[init] summary: ${total} repos / ${cloned} cloned / ${skipped} skipped / ${failed} failed"

[[ $failed -eq 0 ]]
