#!/usr/bin/env bash
# 스택 종료. hot 볼륨은 남는다 (지우려면 docker compose down -v — 파생물이라 `make memory-init` 이 다시 만든다).
# 데몬은 멈추지 않는다 — 상주 데몬이 있으면 먼저 `make memory-daemon-stop` (안 그러면 스택 없이 사이클 실패를 계속 기록한다)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"

# 선택 프로필(dashboards)로 띄운 컨테이너까지 내린다
docker compose --profile dashboards down
echo "[memory-down] stack: stopped"
