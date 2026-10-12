#!/usr/bin/env bash
# 인프라 올리기 — 도구 확인 → 빌드 → 스택 기동 → OpenSearch 대기 → 최초 색인까지 한 번에.
# 멱등이다. 이미 떠 있으면 대기와 색인만 다시 돈다. 데몬이 돌고 있으면 색인은 데몬에 맡긴다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_load_env "$ROOT_DIR"

# 0) Docker 가 떠 있어야 스택을 올릴 수 있다
if ! docker info >/dev/null 2>&1; then
    echo "[memory-up] docker: not running — Docker(OrbStack) 를 먼저 실행하세요 (메모리 2GB 이상)"
    exit 1
fi
echo "[memory-up] docker: ok"

# 1) 도구 — Go 1.25 (mise.toml). mise 가 있으면 설치까지 한다
if command -v mise >/dev/null 2>&1; then
    mise trust "${ROOT_DIR}/mise.toml" >/dev/null 2>&1 || true
    mise install >/dev/null 2>&1 || true
fi
memory_find_go
echo "[memory-up] tools: $(go version | awk '{print $3}')"

# 2) 빌드
memory_build "$ROOT_DIR"
echo "[memory-up] build: bin/memory-mcp, bin/memory-daemon"

# 3) 스택 기동
mkdir -p .data/snapshots
docker compose up -d
echo "[memory-up] stack: started"

# 4) OpenSearch 가 응답할 때까지 대기 (최대 5분)
echo "[memory-up] opensearch: waiting ${OPENSEARCH_URL}"
ready=0
for _ in $(seq 1 150); do
    if curl -sf "${OPENSEARCH_URL}/_cluster/health" >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 2
done
if [[ "$ready" -ne 1 ]]; then
    echo "[memory-up] opensearch: 5분 내 응답 없음 — 'docker logs dj-workspace-opensearch' 확인 (Docker 메모리 2GB 이상)"
    exit 1
fi
echo "[memory-up] opensearch: ready"

# 5) 최초 색인 — 템플릿·ISM·스냅샷 repository 를 맞추고 사이클 1회. 이후엔 데몬이 이어서 한다
bin/memory-daemon -once
echo "[memory-up] reconcile: done"
echo ""
echo "[memory-up] 다음: 'make daemon' 으로 데몬을 켜고, 'make daemon-status' 로 확인하세요"
