#!/usr/bin/env bash
set -euo pipefail

REPOS_DIR="$1"
INDEX_MD="$2"

if [[ ! -f "$INDEX_MD" ]]; then
    echo "[index] INDEX.md: not found, skipping"
    exit 0
fi

if ! command -v claude >/dev/null 2>&1; then
    echo "[index] INDEX.md: claude CLI not found, skipping"
    exit 0
fi

missing=()
for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue
    name=$(basename "$repo")
    if ! grep -q "\\*\\*${name}\\*\\*" "$INDEX_MD"; then
        missing+=("$name")
    fi
done

if [[ ${#missing[@]} -eq 0 ]]; then
    echo "[index] INDEX.md: all repos listed"
    exit 0
fi

echo "[index] INDEX.md: ${#missing[@]} new repo(s) detected"
for name in "${missing[@]}"; do
    echo "[index]   - ${name}"
done

missing_list=$(printf '  - %s\n' "${missing[@]}")

prompt=$(cat <<EOF
${INDEX_MD} 파일을 업데이트해줘.

다음 repo 들이 INDEX.md 에 누락되어 있어:
${missing_list}

작업 내용:
1. 각 repo (${REPOS_DIR}/<name>) 의 README, 빌드 파일 (package.json, go.mod, build.gradle.kts, pyproject.toml 등) 을 읽어서 언어/역할 파악
2. INDEX.md 의 기존 카테고리 중 가장 적절한 곳에 추가:
   - 공통 라이브러리 / 인터페이스 정의
   - 광고 서빙
   - 데이터 수집 / 처리
   - DSP / 외부 API
   - 인프라 / 배포
   - 팀 도구
3. 카테고리가 명확하지 않으면 ## 미분류 섹션에 추가
4. 표 형식: | **{name}** | {언어} | {설명} | {참조 시점} |
5. INDEX.md 에 이미 있던 항목은 절대 수정하지 마
6. ${INDEX_MD} 파일만 수정해. 다른 파일은 건드리지 마
EOF
)

claude --dangerously-skip-permissions -p "$prompt"

echo "[index] INDEX.md: updated"
