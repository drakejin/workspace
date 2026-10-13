#!/usr/bin/env bash
# 메모리 데몬 — 색인·판정·증류·그래프·주간 감사를 도는 reconcile 데몬 (파생물의 유일한 writer, 하나만 돈다).
#   start   백그라운드로 상주시킨다 (make memory-daemon-start). macOS 는 launchd 에 올려 로그인 때 다시 켜고 죽으면 다시 띄운다
#   stop    상주를 멈춘다 (make memory-daemon-stop). launchd 에서 내리고, 떠 있는 데몬에 종료 신호를 보낸다
#   status  데몬·스택·색인·그래프·판정 질문 상태
#   run     포그라운드로 돈다 (launchd 가 부르는 진입점. 로그를 터미널에서 보고 싶을 때)
#   once    사이클 1회 (데몬이 떠 있으면 건너뛴다)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"

WAIT_SECONDS=60

# 대상 meta-repo(META_REPO_ROOT, 기본은 이 레포)에 맞춰 잡 이름·런타임 경로를 정한다 —
# 여러 meta-repo 의 데몬이 서로의 launchd 잡·락을 건드리지 않게 workspace 이름을 잡 이름에 넣는다
setup() {
    memory_load_env "$ROOT_DIR"
    WORKSPACE="$(memory_workspace_name)"
    LABEL="com.drakejin.memory.${WORKSPACE}.daemon"
    PLIST="${HOME}/Library/LaunchAgents/${LABEL}.plist"
    RUNTIME_DIR="${META_REPO_ROOT}/.cache/daemon"
    PID_FILE="${RUNTIME_DIR}/daemon.pid"
    LOG="${RUNTIME_DIR}/daemon.log"
    # start 가 올린 잡 이름 — 그 뒤 workspace 이름(.env)이 바뀌어도 stop·status 가 실제로 올라간 잡을 찾는다
    LABEL_FILE="${RUNTIME_DIR}/launchd.label"
}

# start 가 올린 잡 이름. 기록이 없으면 지금 설정의 이름
installed_label() {
    local label=""
    [[ -f "$LABEL_FILE" ]] && label="$(tr -d '[:space:]' < "$LABEL_FILE")"
    if [[ "$label" =~ ^com\.drakejin\.memory\.[a-z0-9-]+\.daemon$ ]]; then
        echo "$label"
    else
        echo "$LABEL"
    fi
}

# 잡 하나를 launchd 에서 내리고 plist 를 지운다. 내렸으면 0
unload_plist() {
    local plist="$1"
    [[ -f "$plist" ]] || return 1
    launchctl unload "$plist" 2>/dev/null || true
    rm -f "$plist"
}

# plist 를 임시 파일에 그려 검사한다 (memory_render_plist). 성공하면 start 가 제자리로 옮긴다
render_plist() {
    memory_render_plist "${SCRIPT_DIR}/daemon.plist.template" "$1" "$(memory_launchd_env_xml)" \
        "LABEL=${LABEL}" "ROOT=${ROOT_DIR}" "LOG=${LOG}"
}

usage() {
    echo "usage: $0 <start|stop|status|run|once>" >&2
    exit 2
}

# 떠 있는 데몬 pid. 없으면 빈 문자열. pid 파일만 믿지 않고 그 프로세스가 memory-daemon 인지 본다 (pid 재사용)
daemon_pid() {
    [[ -f "$PID_FILE" ]] || return 0
    local pid
    pid=$(tr -d '[:space:]' < "$PID_FILE")
    [[ "$pid" =~ ^[0-9]+$ ]] || return 0
    if ps -p "$pid" -o command= 2>/dev/null | grep -q "memory-daemon"; then
        echo "$pid"
    fi
}

# cond 가 참이 될 때까지 최대 WAIT_SECONDS 초 기다린다
wait_for() {
    local cond="$1"
    for _ in $(seq 1 "$WAIT_SECONDS"); do
        if eval "$cond"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

start() {
    setup
    memory_build "$ROOT_DIR"
    local pid
    pid=$(daemon_pid)
    if [[ -n "$pid" ]]; then
        echo "[memory-daemon] already running (pid ${pid}) — 로그 ${LOG}"
        return 0
    fi
    mkdir -p "$RUNTIME_DIR"
    if memory_has_launchd; then
        mkdir -p "$(dirname "$PLIST")"
        local tmp old
        tmp="$(mktemp "${RUNTIME_DIR}/daemon.plist.XXXXXX")"
        if ! render_plist "$tmp"; then
            rm -f "$tmp"
            echo "[memory-daemon] plist 를 만들지 못했습니다 (plutil -lint 실패)" >&2
            return 1
        fi
        old="$(installed_label)"
        [[ "$old" != "$LABEL" ]] && { unload_plist "${HOME}/Library/LaunchAgents/${old}.plist" || true; }
        launchctl unload "$PLIST" 2>/dev/null || true
        mv "$tmp" "$PLIST"
        launchctl load "$PLIST"
        printf '%s\n' "$LABEL" > "$LABEL_FILE"
        echo "[memory-daemon] launchd: ${LABEL} (workspace=${WORKSPACE}, root=${META_REPO_ROOT} — 로그인 때 다시 켜고, 죽으면 다시 띄운다)"
    else
        nohup bash "${SCRIPT_DIR}/run.sh" run >> "$LOG" 2>&1 &
        echo "[memory-daemon] background: nohup (이 머신을 다시 켜면 다시 start 하세요)"
    fi
    if ! wait_for '[[ -n "$(daemon_pid)" ]]'; then
        echo "[memory-daemon] ${WAIT_SECONDS}초 안에 뜨지 않았습니다 — 로그:" >&2
        tail -20 "$LOG" >&2 || true
        return 1
    fi
    echo "[memory-daemon] started (pid $(daemon_pid)) — 로그 ${LOG}, 상태 'scripts/memory-daemon/run.sh status'"
}

stop() {
    setup
    local was_loaded=0 pid label
    if memory_has_launchd; then
        label="$(installed_label)"
        unload_plist "${HOME}/Library/LaunchAgents/${label}.plist" && was_loaded=1
        [[ "$label" != "$LABEL" ]] && unload_plist "$PLIST" && was_loaded=1
        rm -f "$LABEL_FILE"
    fi
    pid=$(daemon_pid)
    if [[ -n "$pid" ]]; then
        # 종료 신호를 받으면 진행 중인 사이클·증류를 마치고 락을 푼다
        kill -TERM "$pid" 2>/dev/null || true
        if ! wait_for '[[ -z "$(daemon_pid)" ]]'; then
            echo "[memory-daemon] pid ${pid} 가 ${WAIT_SECONDS}초 안에 끝나지 않았습니다 (증류 LLM 호출 중일 수 있다) — 'kill ${pid}' 로 다시 보내세요" >&2
            return 1
        fi
        echo "[memory-daemon] stopped (pid ${pid})"
    elif [[ $was_loaded -eq 1 ]]; then
        echo "[memory-daemon] stopped (launchd 에서 내림)"
    else
        echo "[memory-daemon] not running"
    fi
}

status() {
    setup
    memory_build "$ROOT_DIR"
    if memory_has_launchd; then
        local label
        label="$(installed_label)"
        if launchctl list "$label" >/dev/null 2>&1; then
            echo "[memory-daemon] launchd: loaded (${label})"
        else
            echo "[memory-daemon] launchd: not loaded"
        fi
    fi
    exec .cache/bin/memory-mcp status
}

run() {
    setup
    memory_build "$ROOT_DIR"
    local claude_bin="${MEMORY_CLAUDE_BIN:-claude}"
    if command -v "$claude_bin" >/dev/null 2>&1; then
        echo "[daemon] LLM 백엔드: 로컬 claude CLI ($("$claude_bin" --version 2>/dev/null | head -1))"
    else
        echo "[daemon] claude CLI 없음 — 판정은 질문으로, 증류는 다음 사이클로 폴백"
    fi
    exec .cache/bin/memory-daemon
}

once() {
    setup
    memory_build "$ROOT_DIR"
    exec .cache/bin/memory-daemon -once
}

case "${1:-}" in
    start) start ;;
    stop) stop ;;
    status) status ;;
    run) run ;;
    once) once ;;
    *) usage ;;
esac
