#!/usr/bin/env bash
# docs/issues/*/, docs/specs/*/ 하위 디렉토리를 모두 지운다 (파괴적). 바로 아래 파일(INDEX.md 등)은 남긴다.
# 지운 문서는 cold 로 가지 않는다 — 데몬이 다음 사이클에 메모리 색인과 cold store 청크에서도 빼서 검색에서 사라진다.
# 오래된 문서를 메모리에 남기려면 지우지 말고 scripts/memory/run.sh tier 로 cold 에 보낸다. 묻고 지운다 — CONFIRM=yes 면 묻지 않는다.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

if [[ "${CONFIRM:-}" != "yes" ]]; then
    # read -p 의 프롬프트는 터미널에서만 보이므로 경고는 따로 찍는다
    {
        echo "[docs-cleaner] docs/issues/*/, docs/specs/*/ 하위 디렉토리를 모두 지웁니다 (INDEX.md 처럼 바로 아래 파일은 남긴다)."
        echo "[docs-cleaner] 지운 문서는 cold 로 가지 않습니다 — 데몬이 메모리 색인과 cold 청크에서도 빼서 검색에서 사라집니다."
        echo "[docs-cleaner] 오래된 문서를 메모리에 남기려면 지우지 말고 'scripts/memory/run.sh tier' 로 cold 에 보내세요."
    } >&2
    answer=""
    read -r -p "[docs-cleaner] 계속할까요? [y/N] " answer || true
    if [[ "$answer" != "y" && "$answer" != "Y" ]]; then
        echo "[docs-cleaner] 취소"
        exit 1
    fi
fi

for name in issues specs; do
    "${SCRIPT_DIR}/_clean_dir.sh" "$ROOT_DIR" "$name"
done
