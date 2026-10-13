# 메모리 층 스크립트 공용 헬퍼. source 로만 쓴다 (실행 파일이 아니다).

# .env 를 읽는다. 이미 환경에 있는 값이 이긴다 — 'META_REPO_ROOT=... scripts/memory-daemon/run.sh once' 처럼 명령줄에서 준 값을
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

# 상주 진입점(launchd 가 부르는 데몬 run, .mcp.json 이 부르는 MCP 서버)용 빌드: memory_build_or_reuse <root> <바이너리 이름>
# 이 레포의 Go 코드를 고치는 중(컴파일 오류)이어도 .cache/bin 에 바이너리가 있으면 경고만 남기고 그것을 쓴다 — 여기서 멈추면
# launchd 는 30초마다 재시작을 되풀이하고 메모리 MCP 는 붙지 않는다. 바이너리가 없을 때만 실패한다.
# 빌드 출력과 경고는 모두 stderr 로 보낸다 (MCP 의 stdout 은 프로토콜 전용이다)
memory_build_or_reuse() {
    local root_dir="$1" bin="$2"
    memory_build "$root_dir" >&2 && return 0
    if [[ ! -x "${root_dir}/.cache/bin/${bin}" ]]; then
        echo "[memory] build: 실패했고 기존 .cache/bin/${bin} 도 없습니다 — 컴파일 오류를 고친 뒤 다시 실행하세요" >&2
        return 1
    fi
    echo "[memory] build: 실패 — 기존 .cache/bin/${bin} 로 계속합니다 (컴파일 오류를 고치면 다음 기동 때 새로 빌드한다)" >&2
}

# http 엔드포인트가 응답할 때까지 기다린다 (최대 5분)
memory_wait_http() {
    local name="$1" url="$2"
    echo "[memory-init] ${name}: waiting ${url}"
    for _ in $(seq 1 150); do
        if curl -sf "$url" >/dev/null 2>&1; then
            echo "[memory-init] ${name}: ready"
            return 0
        fi
        sleep 2
    done
    echo "[memory-init] ${name}: 5분 내 응답 없음 — 'docker logs dj-workspace-${name}' 확인 (Docker 메모리 3GB 이상)" >&2
    return 1
}

# launchd 잡에 넣을 PATH. launchd 는 로그인 셸의 PATH 를 모르므로 지금 보이는 docker·git·claude·mise·go 위치를 넣는다
memory_job_path() {
    local dirs=() bin
    for bin in docker git claude mise go curl jq; do
        if command -v "$bin" >/dev/null 2>&1; then
            dirs+=("$(dirname "$(command -v "$bin")")")
        fi
    done
    dirs+=(/usr/local/bin /opt/homebrew/bin /usr/bin /bin /usr/sbin /sbin)
    printf '%s\n' "${dirs[@]}" | awk '!seen[$0]++' | paste -sd: -
}

# launchd(macOS)를 쓸 수 있는가
memory_has_launchd() {
    [[ "$(uname -s)" == "Darwin" ]] && command -v launchctl >/dev/null 2>&1
}

# meta-repo 의 workspace 이름 — MEMORY_WORKSPACE 또는 루트 디렉토리 이름 (Go config 와 같은 규칙: 소문자, [a-z0-9-] 밖은 하이픈)
memory_workspace_name() {
    local ws="${MEMORY_WORKSPACE:-}"
    if [[ -z "$ws" ]]; then
        ws="$(basename "${META_REPO_ROOT:?}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9-]+/-/g; s/^-+//; s/-+$//')"
    fi
    echo "${ws:-workspace}"
}

# launchd 잡에 넣어도 되는 환경변수 이름인가 — 허용 목록 하나로 정한다. 메모리 설정(Go config 와 스크립트가 읽는 이름)만 넣고,
# 이름에 숫자가 든 것(COLD_STORE_S3_BUCKET, AWS_ENDPOINT_URL_S3)도 받는다. NEO4J_* 는 스택 포트만 (Go 는 NEO4J_* 를 읽지 않는다).
# 목록 안이라도 이름에 PASSWORD·SECRET·TOKEN 이 든 것(MEMORY_GRAPH_PASSWORD 등)은 비밀이라 빼고, 잡이 .env·AWS 기본 체인에서 다시 읽는다
memory_launchd_env_allowed() {
    local allowed='^(META_REPO_ROOT|OPENSEARCH_URL|OS_PORT|NEO4J_BOLT_PORT|NEO4J_HTTP_PORT|COLD_[A-Z0-9_]+|MEMORY_[A-Z0-9_]+|AWS_REGION|AWS_PROFILE|AWS_ENDPOINT_URL_S3)$'
    local secret='PASSWORD|SECRET|TOKEN'
    [[ "$1" =~ $allowed ]] && ! [[ "$1" =~ $secret ]]
}

# launchd 잡의 PATH 항목 (<key>PATH</key><string>…</string>)
memory_launchd_path_xml() {
    printf '<key>PATH</key><string>%s</string>' "$(memory_job_path | memory_xml_escape)"
}

# launchd 잡의 EnvironmentVariables <dict> 안쪽. start 때의 메모리 설정(.env 와 명령줄)을 잡에 박아 둔다 —
# launchd 는 셸 환경을 넘기지 않는다. 넣는 이름은 memory_launchd_env_allowed 가 정한다
memory_launchd_env_xml() {
    local name
    memory_launchd_path_xml
    while IFS= read -r name; do
        memory_launchd_env_allowed "$name" || continue
        printf '<key>%s</key><string>%s</string>' "$name" "$(printf '%s' "${!name}" | memory_xml_escape)"
    done < <(compgen -e | LC_ALL=C sort)
}

memory_xml_escape() {
    sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

# launchd plist 를 그린다: memory_render_plist <템플릿> <출력> <EnvironmentVariables 안쪽 XML> [KEY=값 ...]
# 템플릿의 __ENV__ 는 셋째 인자(이미 XML)로, __KEY__ 는 값을 XML escape 해서 바꾼다. sed·gsub 는 값의 & \ | 를 특수 문자로
# 읽으므로 글자 그대로 바꾸고, 바꾼 값은 다시 훑지 않는다. 채우지 않은 자리표시자가 남거나 plutil -lint 가 거부하면 실패한다 —
# 부르는 쪽은 임시 파일에 그려 성공했을 때만 LaunchAgents 로 옮긴다
memory_render_plist() {
    local template="$1" out="$2" env_xml="$3" pair key keys=""
    local -a vars=("PLIST_V_ENV=${env_xml}")
    shift 3
    for pair in "$@"; do
        key="${pair%%=*}"
        if [[ ! "$key" =~ ^[A-Z][A-Z0-9_]*$ || "$pair" != *=* ]]; then
            echo "[memory] plist: 치환 인자는 KEY=값 이어야 합니다 (${pair})" >&2
            return 1
        fi
        keys="${keys} ${key}"
        vars+=("PLIST_V_${key}=$(printf '%s' "${pair#*=}" | memory_xml_escape)")
    done
    env "${vars[@]}" PLIST_KEYS="ENV${keys}" awk '
        BEGIN { n = split(ENVIRON["PLIST_KEYS"], k, " "); for (i = 1; i <= n; i++) val[k[i]] = ENVIRON["PLIST_V_" k[i]] }
        {
            line = $0; rendered = ""
            while (match(line, /__[A-Z][A-Z0-9_]*__/)) {
                key = substr(line, RSTART + 2, RLENGTH - 4)
                if (!(key in val)) { printf "[memory] plist: %s 의 __%s__ 를 채우지 않았습니다\n", FILENAME, key > "/dev/stderr"; exit 1 }
                rendered = rendered substr(line, 1, RSTART - 1) val[key]
                line = substr(line, RSTART + RLENGTH)
            }
            print rendered line
        }
    ' "$template" > "$out" || return 1
    plutil -lint "$out" >/dev/null
}
