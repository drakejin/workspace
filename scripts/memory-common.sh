# 메모리 층 스크립트 공용 헬퍼. source 로만 쓴다 (실행 파일이 아니다).

# .env 를 읽는다. 이미 환경에 있는 값이 이긴다 — 'META_REPO_ROOT=... make reconcile' 처럼 명령줄에서 준 값을
# .env 가 덮지 않게. docker compose 와 같은 규칙으로 읽는다: `export KEY=v`, 따옴표, 줄 끝 ` # 주석`.
memory_load_env() {
    local root_dir="$1" line key value
    if [[ -f "${root_dir}/.env" ]]; then
        while IFS= read -r line || [[ -n "$line" ]]; do
            line="${line#"${line%%[![:space:]]*}"}"
            [[ -z "$line" || "$line" == \#* || "$line" != *=* ]] && continue
            line="${line#export }"
            key="${line%%=*}"
            key="${key//[[:space:]]/}"
            value="${line#*=}"
            [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
            [[ -n "${!key+x}" ]] && continue
            if [[ "$value" =~ ^\"(.*)\"[[:space:]]*(#.*)?$ ]]; then
                value="${BASH_REMATCH[1]}"
            elif [[ "$value" =~ ^\'(.*)\'[[:space:]]*(#.*)?$ ]]; then
                value="${BASH_REMATCH[1]}"
            else
                value="${value%%[[:space:]]#*}"
                value="${value%"${value##*[![:space:]]}"}"
            fi
            export "${key}=${value}"
        done < "${root_dir}/.env"
    fi
    if [[ -z "${META_REPO_ROOT:-}" ]]; then
        export META_REPO_ROOT="$root_dir"
    fi
    export OPENSEARCH_URL="${OPENSEARCH_URL:-http://localhost:${OS_PORT:-9202}}"
    export MEMORY_GRAPH_URI="${MEMORY_GRAPH_URI:-bolt://localhost:${NEO4J_BOLT_PORT:-7689}}"
}

# go 를 찾는다. PATH 에 없으면 mise 설치본을 쓴다 (app/mise.toml 이 버전을 고정한다)
memory_find_go() {
    if command -v go >/dev/null 2>&1; then
        return 0
    fi
    local mise_go
    mise_go="$(ls -d "$HOME"/.local/share/mise/installs/go/1.25*/bin 2>/dev/null | tail -1 || true)"
    if [[ -n "$mise_go" ]]; then
        export PATH="${mise_go}:$PATH"
        return 0
    fi
    echo "[memory] go: not found — 'mise install' 또는 Go 1.25 를 설치하세요" >&2
    return 1
}

# .cache/bin/memory-mcp, .cache/bin/memory-daemon 을 빌드한다. go build 캐시 덕에 바뀐 게 없으면 즉시 끝난다
memory_build() {
    local root_dir="$1"
    memory_find_go || return 1
    (cd "${root_dir}/app" && go build -o "${root_dir}/.cache/bin/memory-mcp" ./memory-mcp && go build -o "${root_dir}/.cache/bin/memory-daemon" ./daemon)
}

# http 엔드포인트가 응답할 때까지 기다린다 (최대 5분)
memory_wait_http() {
    local name="$1" url="$2"
    echo "[memory-up] ${name}: waiting ${url}"
    for _ in $(seq 1 150); do
        if curl -sf "$url" >/dev/null 2>&1; then
            echo "[memory-up] ${name}: ready"
            return 0
        fi
        sleep 2
    done
    echo "[memory-up] ${name}: 5분 내 응답 없음 — 'docker logs dj-workspace-${name}' 확인 (Docker 메모리 3GB 이상)" >&2
    return 1
}
