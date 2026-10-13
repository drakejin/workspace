#!/usr/bin/env bash
# docs/<이름>/ 의 하위 디렉토리를 모두 지운다 — 바로 아래 파일(INDEX.md 등)은 남긴다. 사용법: _clean_dir.sh <root-dir> <issues|specs>
set -euo pipefail

ROOT_DIR="$1"
NAME="$2"
DIR="${ROOT_DIR}/docs/${NAME}"

if [[ ! -d "$DIR" ]]; then
    echo "[skip] docs/${NAME}: not found"
    exit 0
fi

count=0
for entry in "${DIR}"/*/; do
    [[ -d "$entry" ]] || continue
    # 끝의 / 를 떼고 지운다 — 디렉토리를 가리키는 심볼릭 링크면 링크만 지우고 가리키는 곳은 건드리지 않는다
    rm -rf -- "${entry%/}"
    echo "[clean] docs/${NAME}/$(basename "$entry")"
    count=$((count + 1))
done

echo ""
echo "done: ${count} ${NAME} cleaned"
