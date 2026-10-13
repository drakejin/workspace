---
description: 주간 compaction — 60일 지난 episodic 문서에서 살아남은 사실을 knowledge 로 다시 쓰고 원문은 cold 로 보낸다
argument-hint: "[team]"
---

주간 compaction 을 수행한다. 지우지 않고 다시 쓴다. 모든 단계는 **추론 없이**: 원문에서 확인되는 것만 옮긴다.
팀(T)은 `$ARGUMENTS`. 비었으면 `.env` 의 `MEMORY_DEFAULT_TEAM`(없으면 `default`)을 **명시해서** 쓴다 — `scripts/memory/run.sh tier` 에 `-team` 을 빼면 모든 팀을 옮긴다.

1. **수명 확인 · 2. 충돌 탐지** — 리포트를 뽑는다.
   ```bash
   scripts/memory/run.sh compaction-report -team T
   ```
2. 리포트의 "충돌 후보" 묶음마다 원문(`source_path`)을 Read 해서 정의가 실제로 다른지 확인한다. 표현만 다르고 같은 정의면 넘어간다.
3. **분리** — 유효하지 않은 정의를 `docs/knowledge/deprecated/{주제}.md` 로 떼어 낸다. frontmatter `doc_type: knowledge`, 본문에 "무엇이, 언제, 어떤 문서로 대체됐나"와 원문 경로를 적는다.
4. **다시 쓰기** — "수명 확인" 목록의 문서 중 아직 유효한 사실·정의는 오늘 날짜로 `docs/knowledge/{주제}.md` 에 다시 쓴다.
   - frontmatter: `doc_type: knowledge`, `date: <오늘>`, 해당되면 `repo`. 기존 knowledge 를 개정하면 새 문서에 `supersedes: <구판 경로>`
   - 근거를 남긴다: frontmatter `derived_from: [{path: <원문>, why: <무엇을 가져왔나>}]` 와 본문 끝 `## 출처` 의 원문 `source_path`.
     원문은 곧 cold 로 가지만 감사는 cold 에 있는 근거를 깨진 링크로 보지 않는다 (그래프에서는 `unindexed` 노드로 남는다)
   - 충돌하면 최신이 이긴다. 최신이 거짓 같아 보이면 고르지 말고 `## 검수 필요` 에 두 정의를 나란히 적고 사용자에게 알린다
   - `docs/knowledge/INDEX.md` 에 한 줄씩 추가한다. 데몬이 떠 있으면 저장만 하면 색인된다 (아니면 `scripts/memory-daemon/run.sh once`)
5. **티어 이동** — 미리보기 후 실행한다. 쓰기 막기 → 스냅샷 → 인덱스 삭제 → hot 원본 제거(`git rm`).
   ```bash
   scripts/memory/run.sh tier -team T -dry-run
   scripts/memory/run.sh tier -team T
   ```
   지워진 원본은 cold store raw 와 스냅샷에 있다. 사용자에게 커밋을 제안한다.
6. **감사** — `scripts/memory/run.sh audit` 로 깨진 링크(`broken-link`)·근거 없는 knowledge(`unsourced-fact`)가 없는지 본다.
   이미 cold 에 있는 문서가 틀렸다면 고치지 말고 사용자에게 `scripts/memory/run.sh archive deprecate -team T -by <누가> -reason <왜> <path>` 를 제안한다.

마지막에 다시 쓴 사실 수, 분리한 정의 수, cold 로 보낸 인덱스와 파일, 검수 필요 항목을 보고한다.
