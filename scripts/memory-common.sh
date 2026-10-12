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

# go 를 찾는다. PATH 에 없으면 mise 설치본을 쓴다 (mise.toml 이 버전을 고정한다)
memory_find_go() {
    if command -v go >/dev/null 2>&1; then
        return 0
    fi
    local mise_go
    mise_go="$(ls -d "$HOME"/.local/share/mise/installs/go/1.25*/bin 2>/dev/null | tail -1)"
    if [[ -n "$mise_go" ]]; then
        export PATH="${mise_go}:$PATH"
        return 0
    fi
    echo "[memory] go: not found — 'mise install' 또는 Go 1.25 를 설치하세요" >&2
    return 1
}

# bin/memory-mcp, bin/memory-daemon 을 빌드한다. go build 캐시 덕에 바뀐 게 없으면 즉시 끝난다
memory_build() {
    local root_dir="$1"
    memory_find_go || return 1
    (cd "$root_dir" && go build -o bin/memory-mcp ./app/memory-mcp && go build -o bin/memory-daemon ./app/daemon)
}
