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

# plist 를 임시 파일에 그려 lint 한 뒤(memory_render_plist — 경로는 글자 그대로, XML escape) 보여 주거나 제자리로 옮긴다.
# lint 에 실패하면 LaunchAgents 에 아무것도 두지 않는다
install_job() {
    local tmp_dir="${TMPDIR:-/tmp}"
    # EXIT trap 이 지우므로 전역이다 (mv 로 옮긴 뒤면 지울 것이 없다)
    tmp_plist="$(mktemp "${tmp_dir%/}/tier.plist.XXXXXX")"
    trap 'rm -f "$tmp_plist"' EXIT
    if ! memory_render_plist "${SCRIPT_DIR}/tier.plist.template" "$tmp_plist" "$(memory_launchd_path_xml)" \
        "LABEL=${label}" "ROOT=${ROOT_DIR}"; then
        echo "[schedule] tier: plist 를 만들지 못했습니다 (plutil -lint 실패)" >&2
        exit 1
    fi
    if [[ "$dry_run" == "--dry-run" ]]; then
        cat "$tmp_plist"
        return 0
    fi
    mkdir -p "$(dirname "$plist")" "$(dirname "$log")"
    launchctl unload "$plist" 2>/dev/null || true
    mv "$tmp_plist" "$plist"
    launchctl load "$plist"
    echo "[schedule] tier: installed (${plist}) — 로그 ${log}"
}

case "$action" in
    install)
        install_job
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
