#!/usr/bin/env bash
# tier 정기 실행 (macOS launchd) — 6시간마다 깨워 하루 한 번 tier(60일 지난 episodic → cold) + 감사 보고.
# tier 는 hot 원본을 git rm 하므로 켤지는 고른다. 사용법: run.sh <install|uninstall|status> [--dry-run]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${SCRIPT_DIR}/../memory-common.sh"

usage() {
    echo "usage: run.sh <install|uninstall|status> [--dry-run]" >&2
    exit 1
}
[[ $# -lt 1 || $# -gt 2 ]] && usage
action="$1"
dry_run="${2:-}"
case "$dry_run" in
    ''|--dry-run) ;;
    *) usage ;;
esac
if ! memory_has_launchd; then
    echo "[schedule] tier: launchd(macOS) 가 없습니다 — cron 등으로 'scripts/memory/run.sh tier-tick' 을 부르세요" >&2
    exit 1
fi

label="com.drakejin.workspace.tier"
plist="${HOME}/Library/LaunchAgents/${label}.plist"
log="${ROOT_DIR}/.cache/schedule/tier.log"

case "$action" in
    install)
        rendered="$(sed -e "s|__LABEL__|${label}|g" -e "s|__ROOT__|${ROOT_DIR}|g" -e "s|__PATH__|$(memory_job_path)|g" \
            "${SCRIPT_DIR}/tier.plist.template")"
        if [[ "$dry_run" == "--dry-run" ]]; then
            printf '%s\n' "$rendered"
            exit 0
        fi
        mkdir -p "$(dirname "$plist")" "$(dirname "$log")"
        printf '%s\n' "$rendered" > "$plist"
        plutil -lint "$plist" >/dev/null
        launchctl unload "$plist" 2>/dev/null || true
        launchctl load "$plist"
        echo "[schedule] tier: installed (${plist}) — 로그 ${log}"
        ;;
    uninstall)
        launchctl unload "$plist" 2>/dev/null || true
        rm -f "$plist"
        echo "[schedule] tier: uninstalled"
        ;;
    status)
        if launchctl list "$label" >/dev/null 2>&1; then
            echo "[schedule] tier: loaded (${plist})"
        else
            echo "[schedule] tier: not loaded"
        fi
        ;;
    *)
        usage
        ;;
esac
