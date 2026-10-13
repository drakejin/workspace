#!/usr/bin/env bash
# 시나리오 준비 — 픽스처를 임시 meta-repo 로 복사하고({{D:n}} → 오늘에서 n일 전), 시나리오 전용 workspace·팀을 정한다.
# 사용법: scenarios/setup.sh <name>     → 출력의 'source <env>' 를 그대로 실행하면 이 셸의 명령이 시나리오 meta-repo 를 가리킨다
# 다른 시나리오·실제 메모리와 섞이지 않게 workspace 와 팀은 scn-<name>, 루트는 ${TMPDIR}/drakejin-memory-scenario-<name>.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
NAME="${1:-}"
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
    echo "usage: $0 <name>   (소문자·숫자·하이픈)" >&2
    exit 2
fi
WS="scn-${NAME}"
ROOT="${TMPDIR:-/tmp}"
ROOT="${ROOT%/}/drakejin-memory-scenario-${NAME}"

# n일 전 날짜 (이 머신의 시간대, YYYY-MM-DD — 문서의 날도 이 시간대로 정한다) — BSD date(macOS) 와 GNU date 둘 다
days_ago() {
    date -v-"$1"d +%F 2>/dev/null || date -d "$1 days ago" +%F
}

if [[ -e "$ROOT" ]]; then
    echo "[scenario] ${ROOT} 가 이미 있습니다 — 먼저 scenarios/teardown.sh ${NAME}" >&2
    exit 1
fi
mkdir -p "$ROOT"
cp -R "${SCRIPT_DIR}/fixture/." "$ROOT/"

# 날짜 자리표시자 바꾸기
while IFS= read -r file; do
    for n in $(grep -o '{{D:[0-9]*}}' "$file" | sed 's/[^0-9]//g' | sort -u); do
        d="$(days_ago "$n")"
        sed -i.bak "s/{{D:${n}}}/${d}/g" "$file" && rm -f "${file}.bak"
    done
done < <(grep -rl '{{D:' "$ROOT")

# meta-repo 는 git 레포다 (tier 가 git rm 으로 지운다)
git -C "$ROOT" init -q
git -C "$ROOT" add -A
git -C "$ROOT" -c user.name=scenario -c user.email=scenario@example.invalid commit -q -m "scenario fixture"

ENV_FILE="${ROOT}/.scenario.env"
{
    echo "# scenarios/setup.sh ${NAME} 가 만든 파일 — source 해서 쓴다"
    echo "export SCENARIO_NAME=${NAME}"
    echo "export META_REPO_ROOT=${ROOT}"
    echo "export MEMORY_WORKSPACE=${WS}"
    echo "export MEMORY_DEFAULT_TEAM=${WS}"
    echo "export MEMORY_DISTILL_ENABLED=\${MEMORY_DISTILL_ENABLED:-false}"
    echo "export MEMORY_LLM_MODEL=\${MEMORY_LLM_MODEL:-haiku}"
    for n in 0 1 2 3 4 5 10 15 20 25 30 40 50 100; do
        echo "export D${n}=$(days_ago "$n")"
    done
} > "$ENV_FILE"

# Claude Code 가 이 시나리오 meta-repo 를 보게 하는 MCP 설정 (서버 이름은 .mcp.json 과 같다)
cat > "${ROOT}/.scenario-mcp.json" <<JSON
{
  "mcpServers": {
    "drakejin-workspace-memory-mcp": {
      "command": "bash",
      "args": ["${REPO_DIR}/scripts/mcp/run.sh"],
      "env": {
        "META_REPO_ROOT": "${ROOT}",
        "MEMORY_WORKSPACE": "${WS}",
        "MEMORY_DEFAULT_TEAM": "${WS}",
        "MEMORY_DISTILL_ENABLED": "false"
      }
    }
  }
}
JSON
printf '.scenario.env\n.scenario-mcp.json\n.cache/\n' > "${ROOT}/.git/info/exclude"

echo "[scenario] ${NAME}: root=${ROOT} workspace=${WS} team=${WS}"
echo "[scenario] 다음: source ${ENV_FILE}"
