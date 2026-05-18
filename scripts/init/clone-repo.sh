#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "Usage: $(basename "$0") <github-host> <org> <repo-name> <target-dir>"
    exit 1
}

[[ $# -ne 4 ]] && usage

github_host="$1"
org="$2"
repo_name="$3"
target_dir="$4"

if [[ -d "${target_dir}/${repo_name}/.git" ]]; then
    echo "[skip] ${repo_name}: already exists"
    exit 0
fi

clone_url="https://${github_host}/${org}/${repo_name}.git"
clone_dest="${target_dir}/${repo_name}"

clone_err=$(git clone "$clone_url" "$clone_dest" --quiet 2>&1) && rc=0 || rc=$?

if [[ $rc -eq 0 ]]; then
    echo "[clone] ${repo_name}: ${clone_url}"
    exit 0
fi

if echo "$clone_err" | grep -qE "Authentication failed|could not read Username|Authorization failed|unable to access.*40[13]"; then
    echo "[fallback] ${repo_name}: https auth failed, retrying with gh"
    if gh repo clone "${org}/${repo_name}" "$clone_dest" -- --quiet 2>/dev/null; then
        echo "[clone] ${repo_name}: gh repo clone ${org}/${repo_name}"
        exit 0
    fi
fi

echo "[fail] ${repo_name}: clone failed (${clone_url})"
echo "$clone_err" | sed 's/^/  /'
exit 1
