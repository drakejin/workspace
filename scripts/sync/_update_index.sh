#!/usr/bin/env bash
# INDEX.md 에 없는 리포가 main/ 에 생기면 표에 행을 추가한다.
# 리포를 읽고 행을 쓰는 일만 로컬 claude CLI 에 맡긴다 — 읽기 도구만 주고, 결과는 구조화된 JSON 으로 받아
# 이 스크립트가 직접 INDEX.md 에 넣는다 (LLM 에 파일 쓰기 권한을 주지 않는다). claude 가 없으면 건너뛴다.
set -euo pipefail

REPOS_DIR="$1"
INDEX_MD="$2"

if [[ ! -f "$INDEX_MD" ]]; then
    echo "[index] INDEX.md: not found, skipping"
    exit 0
fi

missing=()
for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue
    name=$(basename "$repo")
    if ! grep -q "\*\*${name}\*\*" "$INDEX_MD"; then
        missing+=("$name")
    fi
done

if [[ ${#missing[@]} -eq 0 ]]; then
    echo "[index] INDEX.md: all repos listed"
    exit 0
fi
echo "[index] INDEX.md: ${#missing[@]} new repo(s) — ${missing[*]}"

if ! command -v claude >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
    echo "[index] INDEX.md: claude CLI 또는 jq 가 없어 건너뜁니다 — 직접 추가하세요"
    exit 0
fi

categories=$(grep '^## ' "$INDEX_MD" | grep -v '이 문서를' | sed 's/^## //')
repo_paths=()
for name in "${missing[@]}"; do
    repo_paths+=("${REPOS_DIR}/${name}")
done
schema='{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"language":{"type":"string"},"description":{"type":"string"},"when":{"type":"string"},"category":{"type":"string"}},"required":["name","language","description","when","category"],"additionalProperties":false}}},"required":["rows"],"additionalProperties":false}'
prompt=$(cat <<PROMPT
다음 repository 들의 README 와 빌드 파일(go.mod, package.json, build.gradle.kts, pyproject.toml 등)을 읽고 INDEX.md 표의 행을 만들어줘.
repository 경로: ${repo_paths[*]}
각 행: name(디렉토리 이름 그대로), language, description(한 문장), when(이 repo 를 언제 참조하나, 한 문장), category.
category 는 다음 중 하나를 그대로 쓰고, 맞는 게 없으면 "미분류":
${categories}
확인되지 않는 내용은 추론하지 말고 "-" 로 둔다.
PROMPT
)

out=$(cd "$REPOS_DIR" && claude -p "$prompt" --output-format json --json-schema "$schema" \
    --tools "Read,Glob,Grep" --allowedTools "Read,Glob,Grep" --setting-sources "" --no-session-persistence < /dev/null 2>/dev/null) || {
    echo "[index] INDEX.md: claude 호출 실패 — 건너뜁니다" >&2
    exit 0
}

rows=$(printf '%s' "$out" | jq -c '.structured_output.rows[]?' 2>/dev/null || true)
if [[ -z "$rows" ]]; then
    echo "[index] INDEX.md: claude 응답에 행이 없어 건너뜁니다" >&2
    exit 0
fi

while IFS= read -r row; do
    name=$(printf '%s' "$row" | jq -r '.name')
    # 요청한 repo 의 행만 받는다
    if ! printf '%s\n' "${missing[@]}" | grep -qxF -- "$name"; then
        echo "[index] INDEX.md: 요청하지 않은 행 건너뜀 (${name})" >&2
        continue
    fi
    category=$(printf '%s' "$row" | jq -r '.category')
    # 표가 깨지지 않게 칸 안의 | 와 줄바꿈을 지운다
    line=$(printf '%s' "$row" | jq -r 'map_values(gsub("[|\n\r]"; " ")) | "| **\(.name)** | \(.language) | \(.description) | \(.when) |"')
    if ! grep -qx "## ${category}" "$INDEX_MD"; then
        category="미분류"
        if ! grep -qx "## 미분류" "$INDEX_MD"; then
            printf '\n## 미분류\n\n| Repository | 언어 | 설명 | 참조 시점 |\n|---|---|---|---|\n' >> "$INDEX_MD"
        fi
    fi
    # 그 카테고리 표의 마지막 행 뒤에 넣는다
    tmp=$(mktemp)
    # awk -v 는 역슬래시를 해석하므로 값은 환경변수로 넘긴다
    HEADING="## ${category}" LINE="$line" awk '
        BEGIN { heading = ENVIRON["HEADING"]; line = ENVIRON["LINE"] }
        $0 == heading { inside = 1 }
        inside && /^\|/ { last = NR }
        inside && /^## / && $0 != heading { inside = 0 }
        { lines[NR] = $0 }
        END {
            for (i = 1; i <= NR; i++) {
                print lines[i]
                if (i == last) print line
            }
        }' "$INDEX_MD" > "$tmp" && mv "$tmp" "$INDEX_MD"
    echo "[index] INDEX.md: ${name} → ${category}"
done <<< "$rows"
