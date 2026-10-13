#!/usr/bin/env bash
# 이슈 worktree 하나를 지운다. 사용법: _remove_worktree.sh <repo-dir> <issue> [--force]
# 커밋하지 않은 변경·untracked 파일이 있으면 잃게 될 파일을 보여 주고 멈춘다 — --force 일 때만 버리고 지운다.
# gitignored 파일(.env, 빌드 산출물)은 git 이 변경으로 보지 않아 멈추지 않고 함께 지운다 — 지운 뒤 무엇을 지웠는지 보여 준다.
# 지우기는 git worktree remove 만 쓴다: git 이 거부하는 것(lock 건 worktree, worktree 가 아닌 디렉토리)은 --force 여도 남긴다.
set -euo pipefail

REPO_DIR="$1"
ISSUE="$2"
FORCE="${3:-}"
case "$FORCE" in
    ''|--force) ;;
    *) echo "usage: $(basename "$0") <repo-dir> <issue> [--force]" >&2; exit 2 ;;
esac
REPO_NAME=$(basename "$REPO_DIR")
WORKTREES_DIR="${REPO_DIR}/.worktrees"

found=0
failed=0

# worktree 의 커밋하지 않은 변경과 untracked 파일 (git status --short). worktree 가 아니면(.git 파일이 없다) 빈 문자열
pending_changes() {
    local wt_dir="$1"
    [[ -f "${wt_dir}/.git" ]] || return 0
    git -C "$wt_dir" -c core.quotePath=false status --short --untracked-files=all
}

# worktree 의 gitignored 파일 (git status --ignored 의 '!! ' 줄 — 통째로 무시된 디렉토리는 dir/ 한 줄). worktree 가 아니면 빈 문자열.
# git 은 이것을 변경으로 보지 않아 git worktree remove 가 --force 없이도 함께 지운다 (.env, 빌드 산출물 등) — 그래서 따로 보여 준다
ignored_files() {
    local wt_dir="$1" out
    [[ -f "${wt_dir}/.git" ]] || return 0
    out=$(git -C "$wt_dir" -c core.quotePath=false status --short --ignored) || return 1
    printf '%s\n' "$out" | grep '^!! ' || true
}

# worktree 의 git 등록 디렉토리(.git 파일의 gitdir: .git/worktrees/<id>). worktree 가 아니면 빈 문자열
admin_dir() {
    local wt_dir="$1" gitdir=""
    [[ -f "${wt_dir}/.git" ]] && gitdir=$(sed -n 's/^gitdir: //p' "${wt_dir}/.git")
    [[ -z "$gitdir" || "$gitdir" == /* ]] || gitdir="${wt_dir}/${gitdir}"
    echo "$gitdir"
}

# git status --short 줄 중 그 경로가 아직 worktree 에 있는 줄. 이름을 바꾼 줄(R  old -> new)은 새 이름으로 본다
present_entries() {
    local wt_dir="$1" line path
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        path="${line:3}"
        path="${path##* -> }"
        [[ "$path" == \"*\" ]] && path="${path:1:${#path}-2}"
        path="${path%/}"
        if [[ -e "${wt_dir}/${path}" || -L "${wt_dir}/${path}" ]]; then
            printf '%s\n' "$line"
        fi
    done <<< "$2"
}

# before 의 줄 중 after 에 없는 줄
lines_not_in() {
    [[ -n "$1" ]] || return 0
    grep -vxF -f <(printf '%s\n' "$2") <<< "$1" || true
}

# 목록을 들여 써서 찍는다 (빈 목록이면 아무것도 찍지 않는다)
print_list() {
    [[ -n "$1" ]] || return 0
    printf '%s\n' "$1" | sed 's/^/    /'
}

# 두 목록을 한 목록으로 (빈 쪽은 뺀다)
join_lists() {
    printf '%s\n%s\n' "$1" "$2" | sed '/^$/d'
}

# git worktree remove 가 실패했을 때 실제로 무엇이 남았는지 알린다: report_failure <wt_dir> <err> <등록 디렉토리> <지우기 전 목록>
# git 은 검사(lock·worktree 아님·변경)를 모두 통과해야 지우기 시작하고, 지우다 실패해도 worktree 등록까지 지운다 —
# 등록이 그대로고 목록의 파일도 그대로일 때만 아무것도 잃지 않았다고 말한다
report_failure() {
    local wt_dir="$1" err="$2" admin="$3" before="$4" wt_name after lost
    wt_name=$(basename "$wt_dir")
    after=$(present_entries "$wt_dir" "$before")
    lost=$(lines_not_in "$before" "$after")
    if [[ -z "$lost" && ( -z "$admin" || -d "$admin" ) ]]; then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 를 지우지 못해 남겨 둡니다 — ${err}" >&2
        if [[ -n "$before" ]]; then
            echo "[done-issue] ${REPO_NAME}: 커밋하지 않은 변경은 그대로 있습니다:" >&2
            print_list "$before" >&2
        fi
        echo "[done-issue] ${REPO_NAME}: lock 이면 'git worktree unlock', 이미 없는 worktree 기록이면 'git -C ${REPO_DIR} worktree prune' 후 확인하세요" >&2
        return 0
    fi
    echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 를 지우다가 실패했습니다 — ${err}" >&2
    if [[ -n "$after" ]]; then
        echo "[done-issue] ${REPO_NAME}: 남아 있는 것:" >&2
        print_list "$after" >&2
    fi
    if [[ -n "$lost" ]]; then
        echo "[done-issue] ${REPO_NAME}: 다음은 이미 지워졌습니다:" >&2
        print_list "$lost" >&2
    fi
    if [[ -n "$admin" && ! -d "$admin" ]]; then
        echo "[done-issue] ${REPO_NAME}: git 이 worktree 등록도 지웠습니다 — '${wt_dir}' 는 더 이상 worktree 가 아니니 남은 파일을 확인한 뒤 직접 정리하세요" >&2
    else
        echo "[done-issue] ${REPO_NAME}: 남은 파일을 확인한 뒤 다시 실행하세요" >&2
    fi
}

# worktree 하나를 지운다. 지웠으면 0, 남겼으면 1
remove_one() {
    local wt_dir="$1" wt_name changes ignored admin err
    wt_name=$(basename "$wt_dir")
    if ! changes=$(pending_changes "$wt_dir") || ! ignored=$(ignored_files "$wt_dir"); then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 의 상태를 읽지 못해 남겨 둡니다" >&2
        return 1
    fi
    if [[ -n "$changes" && "$FORCE" != "--force" ]]; then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 에 커밋하지 않은 변경이 있어 남겨 둡니다 — 지우면 잃는 것 (!! 는 gitignored):" >&2
        print_list "$(join_lists "$changes" "$ignored")" >&2
        echo "[done-issue] ${REPO_NAME}: 커밋·stash 하거나, 버려도 되면 --force 를 붙여 다시 실행하세요" >&2
        return 1
    fi
    admin=$(admin_dir "$wt_dir")
    if ! err=$(git -C "$REPO_DIR" worktree remove ${FORCE:+--force} "$wt_dir" 2>&1); then
        report_failure "$wt_dir" "$err" "$admin" "$(join_lists "$changes" "$ignored")"
        return 1
    fi
    echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' removed"
    # 버렸다는 안내는 git 이 실제로 지운 뒤에만 한다 — 거부하면(lock) 아무것도 잃지 않았다
    if [[ -n "$changes" ]]; then
        echo "[done-issue] ${REPO_NAME}: --force — worktree '${wt_name}' 의 다음 변경을 버렸습니다:"
        print_list "$changes"
    fi
    if [[ -n "$ignored" ]]; then
        echo "[done-issue] ${REPO_NAME}: worktree '${wt_name}' 의 gitignored 파일도 함께 지웠습니다:"
        print_list "$ignored"
    fi
}

# worktree 는 main/{repo}/.worktrees/{issue}[-N] 에 있다 (run-issues/_create_worktree.sh).
# .git/worktrees/ 는 git 의 내부 관리 디렉토리라 거기를 직접 지우면 안 된다.
for wt_dir in "${WORKTREES_DIR}/${ISSUE}" "${WORKTREES_DIR}/${ISSUE}"-[0-9]*; do
    [[ -d "$wt_dir" ]] || continue
    found=1
    remove_one "$wt_dir" || failed=1
done

if [[ $found -eq 0 ]]; then
    echo "[done-issue] ${REPO_NAME}: no worktree found for issue '${ISSUE}'"
fi
exit "$failed"
