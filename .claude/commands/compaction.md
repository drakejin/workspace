---
description: 주간 compaction — 60일 경과 문서에서 살아남은 사실을 다시 쓰고 cold로 보낸다
argument-hint: "[team]"
---

주간 compaction을 수행한다. 지우지 않고 다시 쓴다. 모든 단계는 **추론 없이**: 원문에서 확인되는 것만 옮긴다.

1. **수명 확인 · 2. 충돌 탐지** — 리포트를 뽑는다.
   ```bash
   pnpm -s memory compaction-report --team ${ARGUMENTS:-$MEMORY_DEFAULT_TEAM}
   ```
2. 리포트의 "충돌 후보" 묶음마다 원문(`source_path`)을 열어 정의가 실제로 다른지 확인한다. 표현만 다르고 같은 정의면 넘어간다.
3. **분리** — 유효하지 않은 정의를 `docs/knowledge/deprecated/{주제}.md`로 떼어 낸다. frontmatter `doc_type: knowledge`, 본문에 "무엇이, 언제, 어떤 문서로 대체됐나"와 원문 경로를 적는다.
4. **다시 쓰기** — "수명 확인" 목록의 문서 중 아직 유효한 사실·정의는 오늘 날짜로 `docs/knowledge/{주제}.md`에 다시 쓴다.
   - frontmatter: `doc_type: knowledge`, `date: <오늘>`, 해당되면 `repo`
   - 본문 끝에 `## 출처`로 원문 `source_path`와 `git_sha`를 남긴다
   - 충돌하면 최신이 이긴다. 최신이 거짓 같아 보이면 고르지 말고 `## 검수 필요`에 두 정의를 나란히 적고 사용자에게 알린다
   - `docs/knowledge/INDEX.md`에 한 줄씩 추가한다
   - 파일 워처가 떠 있으면 저장만 하면 색인된다. 아니면 `pnpm -s memory ingest docs/knowledge`
5. **티어 이동** — ISM이 생성일 기준으로 처리하지 못한, 늦게 적재된 인덱스까지 정리한다.
   ```bash
   pnpm -s memory tier --dry-run   # 확인
   pnpm -s memory tier
   ```

마지막에 다시 쓴 사실 수, 분리한 정의 수, cold로 보낸 인덱스, 검수 필요 항목을 보고한다.
