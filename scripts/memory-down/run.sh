#!/usr/bin/env bash
# 스택 종료. hot 볼륨은 남는다 (지우려면 docker compose down -v — 파생물이라 memory-up 이 다시 만든다)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "$ROOT_DIR"

# 선택 프로필(dashboards)로 띄운 컨테이너까지 내린다
docker compose --profile dashboards down
echo "[memory-down] stack: stopped"
