#!/usr/bin/env bash
# 테스트 — 인자 없이: 단위 테스트(-race). e2e: 실제 OpenSearch(make memory-up)에 붙는 e2e 포함, 커버리지 출력
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"
source "${SCRIPT_DIR}/../memory-common.sh"
memory_find_go

if [[ "${1:-}" == "e2e" ]]; then
    memory_load_env "$ROOT_DIR"
    go test -race -tags e2e -count=1 -coverpkg=./internal/... -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -1
else
    go test -race ./...
fi
