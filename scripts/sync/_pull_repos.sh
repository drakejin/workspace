#!/usr/bin/env bash
# main/ 의 리포마다 기본 브랜치(origin/HEAD — clone 이 원격의 기본 브랜치로 정한다, run-issues 와 같은 규칙)나
# main/master 에 있으면 pull 한다. 다른 브랜치에서 작업 중인 리포, 커밋이 없는 리포, detached HEAD 는 건너뛴다.
# 하나라도 pull 에 실패하면 끝까지 돈 뒤 요약을 찍고 0 이 아닌 값으로 끝난다.
# main/master 도 받는 까닭: origin/HEAD 는 clone 때 정해지고 fetch 가 고치지 않아, 원격이 기본 브랜치 이름을 바꾸면
# (master→main) 낡는다 (고치려면 git remote set-head origin -a). origin/HEAD 가 없는 리포(예전 clone)도 main/master 로 받는다.
set -euo pipefail

REPOS_DIR="$1"

synced=0
skipped=0
failed=0

# default_branch <repo> — 그 리포의 기본 브랜치 이름 (origin/HEAD, 없으면 main)
default_branch() {
    local head
    head=$(git -C "$1" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null) || head="origin/main"
    echo "${head#origin/}"
}

# pullable <현재 브랜치> <기본 브랜치> — 기본 브랜치이거나 main/master 면 참
pullable() {
    [[ "$1" == "$2" || "$1" == "main" || "$1" == "master" ]]
}

# unpullable_reason <repo> — pull 할 브랜치가 없는 까닭. 있으면 빈 문자열.
# 커밋이 없는 repo(unborn HEAD — git init 만 했거나 빈 원격을 clone 했다)는 rev-parse 가 실패하므로 set -e 로 sync 전체가
# 멈추지 않게 여기서 거른다
unpullable_reason() {
    if ! git -C "$1" rev-parse --verify --quiet HEAD >/dev/null 2>&1; then
        echo "커밋이 없다 (unborn HEAD)"
    elif ! git -C "$1" symbolic-ref --quiet HEAD >/dev/null 2>&1; then
        echo "detached HEAD"
    fi
}

for repo in "${REPOS_DIR}"/*/; do
    [[ -d "${repo}/.git" ]] || continue

    name=$(basename "$repo")
    reason=$(unpullable_reason "$repo")
    if [[ -n "$reason" ]]; then
        echo "[skip] ${name}: ${reason}"
        skipped=$((skipped + 1))
        continue
    fi
    branch=$(git -C "$repo" symbolic-ref --quiet --short HEAD)
    default=$(default_branch "$repo")

    if ! pullable "$branch" "$default"; then
        echo "[skip] ${name}: on branch '${branch}' (기본 브랜치 '${default}')"
        skipped=$((skipped + 1))
        continue
    fi

    if git -C "$repo" pull --ff-only origin "$branch" --quiet 2>/dev/null; then
        echo "[sync] ${name}: pulled '${branch}'"
        synced=$((synced + 1))
    else
        echo "[fail] ${name}: pull failed on '${branch}'" >&2
        failed=$((failed + 1))
    fi
done

total=$((synced + skipped + failed))
echo "[sync] summary: ${total} repos / ${synced} synced / ${skipped} skipped / ${failed} failed"

[[ $failed -eq 0 ]]
