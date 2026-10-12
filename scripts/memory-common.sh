#!/usr/bin/env bash
# 메모리 층 스크립트 공용 헬퍼. source 로만 쓴다.

# .env 를 읽는다. 이미 환경에 있는 값이 이긴다 —
# 'META_REPO_ROOT=... make reconcile' 처럼 명령줄에서 준 값을 .env 가 덮지 않게.
memory_load_env() {
    local root_dir="$1" line key value
    if [[ -f "${root_dir}/.env" ]]; then
        while IFS= read -r line || [[ -n "$line" ]]; do
            [[ "$line" =~ ^[[:space:]]*# || "$line" != *=* ]] && continue
            key="${line%%=*}"
            key="${key//[[:space:]]/}"
            value="${line#*=}"
            [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
            [[ -n "${!key+x}" ]] && continue
            export "${key}=${value}"
        done < "${root_dir}/.env"
    fi
    if [[ -z "${META_REPO_ROOT:-}" ]]; then
        export META_REPO_ROOT="$root_dir"
    fi
    export OPENSEARCH_URL="${OPENSEARCH_URL:-http://localhost:${OS_PORT:-9202}}"
}
