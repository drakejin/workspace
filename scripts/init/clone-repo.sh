#!/usr/bin/env bash
# 리포 하나를 main/ 에 clone 한다. docs-only=true 면 docs/ 만 받는 sparse clone 이다
# (코드는 필요 없고 문서만 메모리 층에 색인하고 싶은 리포용).
set -euo pipefail

usage() {
    echo "Usage: $(basename "$0") <github-host> <org> <repo-name> <target-dir> [docs-only]" >&2
    exit 1
}

[[ $# -lt 4 || $# -gt 5 ]] && usage

github_host="$1"
org="$2"
repo_name="$3"
target_dir="$4"
docs_only="${5:-false}"

if [[ -d "${target_dir}/${repo_name}/.git" ]]; then
    echo "[skip] ${repo_name}: already exists"
    exit 0
fi

clone_url="https://${github_host}/${org}/${repo_name}.git"
clone_dest="${target_dir}/${repo_name}"
clone_args=(--quiet)
if [[ "$docs_only" == "true" ]]; then
    clone_args+=(--filter=blob:none --sparse)
fi

sparse_docs() {
    if [[ "$docs_only" == "true" ]]; then
        git -C "$clone_dest" sparse-checkout set docs
        echo "[clone] ${repo_name}: docs only (sparse-checkout docs/)"
    fi
}

clone_err=$(git clone "${clone_args[@]}" "$clone_url" "$clone_dest" 2>&1) && rc=0 || rc=$?

if [[ $rc -eq 0 ]]; then
    echo "[clone] ${repo_name}: ${clone_url}"
    sparse_docs
    exit 0
fi

if echo "$clone_err" | grep -qE "Authentication failed|could not read Username|Authorization failed|unable to access.*40[13]"; then
    echo "[fallback] ${repo_name}: https auth failed, retrying with gh"
    if gh repo clone "${org}/${repo_name}" "$clone_dest" -- "${clone_args[@]}" 2>/dev/null; then
        echo "[clone] ${repo_name}: gh repo clone ${org}/${repo_name}"
        sparse_docs
        exit 0
    fi
fi

echo "[fail] ${repo_name}: clone failed (${clone_url})" >&2
echo "$clone_err" | sed 's/^/  /' >&2
exit 1
