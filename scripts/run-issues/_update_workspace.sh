#!/usr/bin/env bash
set -euo pipefail

REPOS_DIR="$1"
WORKSPACE_JSON="$2"

existing_json=$(cat "$WORKSPACE_JSON")

for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue

    name=$(basename "$repo")

    working=()
    current_branch=$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
    main_branch=$(git -C "$repo" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || echo "main")
    working+=("$(jq -n \
        --arg tw "main" \
        --arg tb "$current_branch" \
        --arg issue "" \
        '{targetWorktrees: $tw, targetBranch: $tb, issue: $issue}')")

    worktree_list=$(git -C "$repo" worktree list --porcelain 2>/dev/null || true)
    git_dir="${repo}.git"

    while IFS= read -r line; do
        if [[ "$line" == "worktree "* ]]; then
            wt_path="${line#worktree }"
            wt_branch=""
        elif [[ "$line" == "branch "* ]]; then
            wt_branch="${line#branch refs/heads/}"
        elif [[ -z "$line" && -n "${wt_path:-}" ]]; then
            if [[ "$wt_path" == "${git_dir}"* && -n "$wt_branch" ]]; then
                wt_name=$(basename "$wt_path")
                working+=("$(jq -n \
                    --arg tw "$wt_name" \
                    --arg tb "$wt_branch" \
                    --arg issue "$wt_name" \
                    '{targetWorktrees: $tw, targetBranch: $tb, issue: $issue}')")
            fi
            wt_path=""
            wt_branch=""
        fi
    done <<< "$worktree_list"
    # handle last entry if no trailing blank line
    if [[ -n "${wt_path:-}" && "$wt_path" == "${git_dir}"* && -n "${wt_branch:-}" ]]; then
        wt_name=$(basename "$wt_path")
        working+=("$(jq -n \
            --arg tw "$wt_name" \
            --arg tb "$wt_branch" \
            --arg issue "$wt_name" \
            '{targetWorktrees: $tw, targetBranch: $tb, issue: $issue}')")
    fi

    working_json=$(printf '%s\n' "${working[@]}" | jq -s '.')

    existing_json=$(echo "$existing_json" | jq \
        --arg name "$name" \
        --argjson working "$working_json" \
        '.workspaces = [.workspaces[] | if .name == $name then . + {working: $working} else . end]')
done

echo "$existing_json" | jq '.' > "$WORKSPACE_JSON"
echo "[run-issues] workspace.json updated"
