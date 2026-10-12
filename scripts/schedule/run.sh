#!/usr/bin/env bash
# launchd(macOS) 등록/해제 — tier 정기 실행(6시간마다 깨워 하루 한 번), 데몬 상주(로그인 시 시작, 죽으면 재시작).
# 사용법: run.sh <install|uninstall|status> <tier|daemon> [--dry-run]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

usage() {
    echo "usage: run.sh <install|uninstall|status> <tier|daemon> [--dry-run]" >&2
    exit 1
}
[[ $# -lt 2 ]] && usage
action="$1"
job="$2"
dry_run="${3:-}"
[[ "$job" == "tier" || "$job" == "daemon" ]] || usage

label="com.drakejin.workspace.${job}"
plist="${HOME}/Library/LaunchAgents/${label}.plist"
log="${ROOT_DIR}/.cache/schedule/${job}.log"

# launchd 는 로그인 셸의 PATH 를 모른다 — 지금 보이는 docker·git·claude·mise 위치를 넣어 둔다
job_path() {
    local dirs=() bin
    for bin in docker git claude mise go curl jq; do
        if command -v "$bin" >/dev/null 2>&1; then
            dirs+=("$(dirname "$(command -v "$bin")")")
        fi
    done
    dirs+=(/usr/local/bin /opt/homebrew/bin /usr/bin /bin /usr/sbin /sbin)
    printf '%s\n' "${dirs[@]}" | awk '!seen[$0]++' | paste -sd: -
}

case "$action" in
    install)
        rendered="$(sed -e "s|__LABEL__|${label}|g" -e "s|__ROOT__|${ROOT_DIR}|g" -e "s|__PATH__|$(job_path)|g" \
            "${SCRIPT_DIR}/${job}.plist.template")"
        if [[ "$dry_run" == "--dry-run" ]]; then
            printf '%s\n' "$rendered"
            exit 0
        fi
        mkdir -p "$(dirname "$plist")" "$(dirname "$log")"
        printf '%s\n' "$rendered" > "$plist"
        plutil -lint "$plist" >/dev/null
        launchctl unload "$plist" 2>/dev/null || true
        launchctl load "$plist"
        echo "[schedule] ${job}: installed (${plist}) — 로그 ${log}"
        ;;
    uninstall)
        launchctl unload "$plist" 2>/dev/null || true
        rm -f "$plist"
        echo "[schedule] ${job}: uninstalled"
        ;;
    status)
        if launchctl list "$label" >/dev/null 2>&1; then
            echo "[schedule] ${job}: loaded (${plist})"
        else
            echo "[schedule] ${job}: not loaded"
        fi
        ;;
    *)
        usage
        ;;
esac
