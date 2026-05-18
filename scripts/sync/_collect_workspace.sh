#!/usr/bin/env bash
set -euo pipefail

REPOS_DIR="$1"
OUTPUT="$2"

existing_json="{}"
if [[ -f "$OUTPUT" ]]; then
    existing_json=$(cat "$OUTPUT")
fi

github_host=$(echo "$existing_json" | jq -r '.githubHost // ""')
org=$(echo "$existing_json" | jq -r '.org // ""')
version=$(echo "$existing_json" | jq -r '.version // ""')

entries=()

for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue

    name=$(basename "$repo")
    current_branch=$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
    main_branch=$(git -C "$repo" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || echo "")
    last_commit=$(git -C "$repo" rev-parse HEAD 2>/dev/null || echo "")

    extra=$(echo "$existing_json" | jq --arg name "$name" '.workspaces // [] | map(select(.name == $name)) | .[0] // {} | del(.name, .mainBranch, .workingBranch, .lastCommitHash)')

    entries+=("$(jq -n \
        --arg name "$name" \
        --arg mainBranch "$main_branch" \
        --arg workingBranch "$current_branch" \
        --arg lastCommitHash "$last_commit" \
        --argjson extra "$extra" \
        '{name: $name, mainBranch: $mainBranch, workingBranch: $workingBranch, lastCommitHash: $lastCommitHash} + $extra'
    )")
done

jq -n \
    --arg gh "$github_host" \
    --arg org "$org" \
    --arg version "$version" \
    --argjson ws "$(printf '%s\n' "${entries[@]}" | jq -s '.')" \
    'if $version == "" then {githubHost: $gh, org: $org, workspaces: $ws} else {githubHost: $gh, org: $org, version: $version, workspaces: $ws} end' > "$OUTPUT"

echo "wrote ${#entries[@]} repos to ${OUTPUT}"
