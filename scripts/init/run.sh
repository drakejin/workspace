#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPOS_DIR="${ROOT_DIR}/main"
WORKSPACE_JSON="${ROOT_DIR}/workspace.json"
SEED_JSON="${ROOT_DIR}/workspace.seed.json"

if [[ ! -f "$WORKSPACE_JSON" ]]; then
    if [[ ! -f "$SEED_JSON" ]]; then
        echo "error: workspace.json or workspace.seed.json not found"
        exit 1
    fi
    cp "$SEED_JSON" "$WORKSPACE_JSON"
    echo "[init] workspace.json created from workspace.seed.json"
    echo ""
else
    echo "[warning] workspace.json already exists. Using existing file."
    echo "          To recreate from workspace.seed.json, remove it first:"
    echo "              rm workspace.json && make init"
    echo ""
fi

github_host=$(jq -r '.githubHost' "$WORKSPACE_JSON")
org=$(jq -r '.org' "$WORKSPACE_JSON")

if [[ -z "$github_host" || "$github_host" == "null" ]]; then
    echo "error: githubHost not set in workspace.json"
    exit 1
fi

if [[ -z "$org" || "$org" == "null" ]]; then
    echo "error: org not set in workspace.json"
    exit 1
fi

"${SCRIPT_DIR}/_install_hooks.sh" "$ROOT_DIR"
echo ""
"${SCRIPT_DIR}/_clone_repos.sh" "$github_host" "$org" "$REPOS_DIR" "$WORKSPACE_JSON"
echo ""
"${SCRIPT_DIR}/../sync/run.sh"
echo ""

VERSION_FILE="${ROOT_DIR}/VERSION"
if [[ -f "$VERSION_FILE" ]]; then
    version=$(tr -d '[:space:]' < "$VERSION_FILE")
    if [[ -n "$version" ]]; then
        tmp=$(mktemp)
        jq --arg v "$version" '. + {version: $v}' "$WORKSPACE_JSON" > "$tmp" && mv "$tmp" "$WORKSPACE_JSON"
        echo "[init] workspace.json version set to ${version}"
    else
        echo "[init] VERSION file is empty, workspace.json.version not updated"
    fi
else
    echo "[init] VERSION file not found, workspace.json.version not updated"
fi
