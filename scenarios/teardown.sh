#!/usr/bin/env bash
# 시나리오 정리 — 시나리오 데몬을 멈추고, 시나리오 팀의 인덱스·스냅샷, workspace 그래프, 임시 meta-repo 를 지운다.
# 시나리오 팀(scn-<name>)의 이름 그대로인 인덱스(mem-scn-<name>-YYYY.MM, mem-scn-<name>-knowledge)만 지운다 —
# 같은 스택의 실제 메모리도, 이름이 <name> 으로 시작하는 다른 시나리오(a 와 a-b)의 것도 건드리지 않는다.
# 사용법: scenarios/teardown.sh <name>
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
NAME="${1:-}"
if [[ ! "$NAME" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
    echo "usage: $0 <name>" >&2
    exit 2
fi
WS="scn-${NAME}"
ROOT="${TMPDIR:-/tmp}"
ROOT="${ROOT%/}/drakejin-memory-scenario-${NAME}"
# 스택 주소는 이 레포의 .env 를 따른다 (setup·데몬과 같은 스택을 지운다)
source "${REPO_DIR}/scripts/memory-common.sh"
memory_load_env "$REPO_DIR"
OS_URL="$OPENSEARCH_URL"
SNAPSHOT_REPO="${COLD_SNAPSHOT_REPOSITORY:-mem-cold}"
# 이 팀의 인덱스 이름 (app/internal/naming 과 같은 규칙). 접두사 mem-${WS}-* 로 고르면 mem-${WS}-b-* 까지 잡는다
OWN_INDEX="^mem-${WS}-([0-9]{4}[.][0-9]{2}|knowledge)\$"

fail() {
    echo "[scenario] ${NAME}: 정리 실패 — $*" >&2
    exit 1
}

curl -sf -o /dev/null "$OS_URL" || fail "OpenSearch(${OS_URL}) 에 닿지 않습니다 — 스택을 띄우고(make memory-init) 다시 하세요. ${ROOT} 는 남겨 둡니다"

if [[ -d "$ROOT" ]]; then
    META_REPO_ROOT="$ROOT" MEMORY_WORKSPACE="$WS" MEMORY_DEFAULT_TEAM="$WS" "${REPO_DIR}/scripts/memory-daemon/run.sh" stop ||
        fail "시나리오 데몬을 멈추지 못했습니다"
fi

# 접두사로 후보를 받아 OWN_INDEX 로 거른다
indices=$(curl -sf "${OS_URL}/_cat/indices/mem-${WS}-*?format=json&h=index&expand_wildcards=open,closed" |
    jq -r --arg re "$OWN_INDEX" '.[].index | select(test($re))') || fail "인덱스 목록을 읽지 못했습니다"
index_count=0
for i in $indices; do
    curl -sf -XDELETE "${OS_URL}/${i}" >/dev/null || fail "인덱스 ${i} 를 지우지 못했습니다"
    index_count=$((index_count + 1))
done
# 스냅샷 repository 가 아직 없으면(404) 지울 스냅샷도 없다. 이 팀 인덱스만 담은 스냅샷만 지운다
code=$(curl -s -o /dev/null -w '%{http_code}' "${OS_URL}/_snapshot/${SNAPSHOT_REPO}")
count=0
if [[ "$code" == "200" ]]; then
    snaps=$(curl -sf "${OS_URL}/_snapshot/${SNAPSHOT_REPO}/_all" |
        jq -r --arg re "$OWN_INDEX" '.snapshots[]? | select((.indices | length) > 0 and all(.indices[]; test($re))) | .snapshot') ||
        fail "스냅샷 목록을 읽지 못했습니다"
    for s in $snaps; do
        curl -sf -XDELETE "${OS_URL}/_snapshot/${SNAPSHOT_REPO}/${s}" >/dev/null || fail "스냅샷 ${s} 를 지우지 못했습니다"
        count=$((count + 1))
    done
elif [[ "$code" != "404" ]]; then
    fail "스냅샷 repository ${SNAPSHOT_REPO} 를 읽지 못했습니다 (HTTP ${code})"
fi
graph="그래프 workspace=${WS}"
if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx dj-workspace-neo4j; then
    docker exec dj-workspace-neo4j cypher-shell --format plain "MATCH (n {workspace: '${WS}'}) DETACH DELETE n" >/dev/null ||
        fail "그래프 workspace=${WS} 를 지우지 못했습니다"
else
    graph="그래프 건너뜀 (Neo4j 컨테이너가 떠 있지 않다)"
fi
rm -rf "$ROOT"
echo "[scenario] ${NAME}: 정리됨 (인덱스 ${index_count}개, 스냅샷 ${count}개, ${graph}, ${ROOT})"
