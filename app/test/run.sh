#!/usr/bin/env bash
# app/ 테스트 (app/Makefile 이 부른다)
#   (인자 없음)  단위 테스트 (-race)
#   e2e         실제 스택(루트의 make memory-init)에 붙는 e2e 포함 + 커버리지 (.cache/coverage.out)
#   slow        ISM 이 실제 시간으로 인덱스를 옮기는 e2e (6~15분, make memory-init 필요)
#   s3          cold 층을 S3(LocalStack)에 두는 e2e — 전용 스택(app/test/s3)을 올렸다가 내린다
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
source "${ROOT_DIR}/scripts/memory-common.sh"
memory_find_go
cd "${ROOT_DIR}/app"

S3_COMPOSE=(docker compose -f "${ROOT_DIR}/app/test/s3/docker-compose.yml")

require_stack() {
    memory_load_env "$ROOT_DIR"
    if ! curl -sf "${OPENSEARCH_URL}/_cluster/health" >/dev/null 2>&1; then
        echo "[test] opensearch: not reachable at ${OPENSEARCH_URL} — 'make memory-init' 먼저" >&2
        exit 1
    fi
}

# 그래프 e2e 는 Neo4j 가 있어야 한다. MEMORY_GRAPH_URI(bolt://host:port)의 포트로 TCP 연결만 확인한다
require_graph() {
    [[ "${MEMORY_GRAPH_URI}" == "off" ]] && return 0
    local hostport="${MEMORY_GRAPH_URI#*://}"
    hostport="${hostport%%/*}"
    local host="${hostport%%:*}" port="${hostport##*:}"
    [[ "$port" == "$hostport" ]] && port=7687
    if ! (exec 3<>"/dev/tcp/${host}/${port}") 2>/dev/null; then
        echo "[test] neo4j: not reachable at ${MEMORY_GRAPH_URI} — 'make memory-init' 먼저 (그래프 e2e 를 빼려면 MEMORY_GRAPH_URI=off)" >&2
        exit 1
    fi
}

case "${1:-}" in
    "")
        go test -race ./...
        ;;
    e2e)
        require_stack
        require_graph
        mkdir -p "${ROOT_DIR}/.cache"
        go test -race -tags e2e -count=1 -coverpkg=./internal/... -coverprofile="${ROOT_DIR}/.cache/coverage.out" ./...
        go tool cover -func="${ROOT_DIR}/.cache/coverage.out" | tail -1
        ;;
    slow)
        require_stack
        go test -tags e2e,e2e_slow -count=1 -timeout 30m -v -run 'TestISM' ./test/e2e/
        ;;
    s3)
        memory_load_env "$ROOT_DIR"
        trap '"${S3_COMPOSE[@]}" down -v >/dev/null 2>&1 || true' EXIT
        echo "[test] s3: LocalStack + OpenSearch(repository-s3) 기동 (9203, 4576)"
        "${S3_COMPOSE[@]}" up -d --build --wait
        go test -tags e2e,e2e_s3 -count=1 -timeout 10m -v -run 'TestS3' ./test/e2e/
        ;;
    *)
        echo "usage: $0 [e2e|slow|s3]" >&2
        exit 2
        ;;
esac
