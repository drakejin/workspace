#!/usr/bin/env bash
# tier 정기 실행 (macOS launchd) — 6시간마다 깨워 하루 한 번 tier(60일 지난 episodic → cold) + 감사 보고.
# 데몬처럼 대상 meta-repo(META_REPO_ROOT, 기본은 이 레포)마다 잡을 따로 둔다: com.drakejin.memory.<workspace>.tier.
# install 때의 메모리 설정(.env 와 명령줄, 비밀은 뺀다 — memory_launchd_env_xml)을 잡에 담는다.
# tier 는 hot 원본을 git rm 하므로 켤지는 고른다. 사용법: run.sh <install|uninstall|status> [--dry-run]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${SCRIPT_DIR}/../memory-common.sh"

# 예전 판의 고정 잡 이름 — 머신에 하나뿐이었고, 명령줄 환경 없이 이 레포의 .env 가 가리키는 meta-repo 만 돌았다.
# 그 meta-repo 에 install·uninstall 하면 내리고 지운다 (새 이름으로 옮긴다)
LEGACY_LABEL="com.drakejin.workspace.tier"
AGENTS_DIR="${HOME}/Library/LaunchAgents"

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

# 대상 meta-repo 에 맞춰 잡 이름·로그 경로를 정한다 (memory-daemon 의 setup 과 같은 규칙)
memory_load_env "$ROOT_DIR"
workspace="$(memory_workspace_name)"
label="com.drakejin.memory.${workspace}.tier"
plist="${AGENTS_DIR}/${label}.plist"
log="${META_REPO_ROOT}/.cache/schedule/tier.log"
# install 이 깐 잡 이름 — 그 뒤 workspace 이름(.env)이 바뀌어도 uninstall·status 가 실제로 깐 잡을 찾는다
label_file="${META_REPO_ROOT}/.cache/schedule/launchd.label"

# install 이 깐 잡 이름. 기록이 없으면 지금 설정의 이름
installed_label() {
    local recorded=""
    [[ -f "$label_file" ]] && recorded="$(tr -d '[:space:]' < "$label_file")"
    if [[ "$recorded" =~ ^com\.drakejin\.memory\.[a-z0-9-]+\.tier$ ]]; then
        echo "$recorded"
    else
        echo "$label"
    fi
}

# 잡 하나를 launchd 에서 내리고 plist 를 지운다. 내렸으면 0, plist 가 없으면 1
unload_job() {
    local job_plist="${AGENTS_DIR}/$1.plist"
    [[ -f "$job_plist" ]] || return 1
    launchctl unload "$job_plist" 2>/dev/null || true
    rm -f "$job_plist"
}

# 예전 고정 잡이 돌던 meta-repo — 그 잡은 명령줄 환경 없이 이 레포의 .env 만 읽었다
legacy_meta_root() {
    (unset META_REPO_ROOT MEMORY_WORKSPACE; memory_load_env "$ROOT_DIR"; echo "$META_REPO_ROOT")
}

# 예전 고정 잡을 옮긴다 — 지금 대상 meta-repo 의 것이면 내리고 지운다. 다른 meta-repo 의 것이면 그대로 두고 알린다
migrate_legacy_job() {
    [[ -f "${AGENTS_DIR}/${LEGACY_LABEL}.plist" ]] || return 0
    local legacy_root
    legacy_root="$(legacy_meta_root)"
    if [[ "$legacy_root" != "$META_REPO_ROOT" ]]; then
        echo "[schedule] tier: 예전 잡 ${LEGACY_LABEL} 은 다른 meta-repo(${legacy_root})의 것이라 그대로 둡니다 — 그 meta-repo 로 install 하면 옮긴다"
        return 0
    fi
    unload_job "$LEGACY_LABEL" || true
    echo "[schedule] tier: 예전 잡 ${LEGACY_LABEL} 을 내렸습니다"
}

# plist 를 임시 파일에 그려 lint 한 뒤(memory_render_plist — 경로는 글자 그대로, XML escape) 보여 주거나 제자리로 옮긴다.
# lint 에 실패하면 LaunchAgents 에 아무것도 두지 않는다
install_job() {
    local tmp_dir="${TMPDIR:-/tmp}" old
    # EXIT trap 이 지우므로 전역이다 (mv 로 옮긴 뒤면 지울 것이 없다)
    tmp_plist="$(mktemp "${tmp_dir%/}/tier.plist.XXXXXX")"
    trap 'rm -f "$tmp_plist"' EXIT
    if ! memory_render_plist "${SCRIPT_DIR}/tier.plist.template" "$tmp_plist" "$(memory_launchd_env_xml)" \
        "LABEL=${label}" "ROOT=${ROOT_DIR}" "LOG=${log}"; then
        echo "[schedule] tier: plist 를 만들지 못했습니다 (plutil -lint 실패)" >&2
        exit 1
    fi
    if [[ "$dry_run" == "--dry-run" ]]; then
        cat "$tmp_plist"
        return 0
    fi
    mkdir -p "$AGENTS_DIR" "$(dirname "$log")"
    migrate_legacy_job
    old="$(installed_label)"
    if [[ "$old" != "$label" ]]; then
        unload_job "$old" || true
    fi
    launchctl unload "$plist" 2>/dev/null || true
    mv "$tmp_plist" "$plist"
    launchctl load "$plist"
    printf '%s\n' "$label" > "$label_file"
    echo "[schedule] tier: installed ${label} (workspace=${workspace}, root=${META_REPO_ROOT}) — 로그 ${log}"
}

uninstall_job() {
    local old removed=0
    old="$(installed_label)"
    unload_job "$old" && removed=1
    if [[ "$old" != "$label" ]] && unload_job "$label"; then
        removed=1
    fi
    rm -f "$label_file"
    migrate_legacy_job
    if [[ $removed -eq 1 ]]; then
        echo "[schedule] tier: uninstalled ${old}"
    else
        echo "[schedule] tier: ${old} 은 설치되어 있지 않습니다"
    fi
}

status_job() {
    local current
    current="$(installed_label)"
    if launchctl list "$current" >/dev/null 2>&1; then
        echo "[schedule] tier: loaded ${current} — 로그 ${log}"
    else
        echo "[schedule] tier: not loaded (${current})"
    fi
    if [[ -f "${AGENTS_DIR}/${LEGACY_LABEL}.plist" ]]; then
        echo "[schedule] tier: 예전 잡 ${LEGACY_LABEL} 도 있습니다 — 그 meta-repo 로 install 하면 옮긴다"
    fi
}

case "$action" in
    install) install_job ;;
    uninstall) uninstall_job ;;
    status) status_job ;;
    *) usage ;;
esac
