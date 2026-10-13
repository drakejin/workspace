# drakejin/workspace — 에이전트 규칙

이 레포는 drakejin 개인 repository 들을 한곳에서 다루는 meta-repository 다. 관리 대상 repository 는 `main/{repo}` 에 clone 되고,
그 repository 들의 메타 정보(spec, ADR, PRD, AI plan)는 `docs/` 에 쌓는다. 검색은 OpenSearch 기반 memory-mcp 가 맡는다.
레포 운영 규칙(명령어, 스크립트 컨벤션)은 [README.md](./README.md), 메모리 층 설치·사용법은 [GUIDE.md](./GUIDE.md) 를 따른다.

## 작업 규칙

1. **작업을 시작하면 `search_memory` 를 먼저 부른다** (MCP 서버 `drakejin-workspace-memory-mcp`). 파일 트리를 뒤지기 전에. 질의에는 repository 이름, 서비스명, 함수명, 에러 코드 같은 식별자를 그대로 넣는다. 결과가 부족하면 단어를 바꿔 두세 번 다시 부른다.
   날짜·기간·주제·프로젝트가 정해진 질문이면 필터(`date`, `time_range`, `topic`, `project`)를 쓴다 — 쓸 수 있는 값은 `search_filters` 로 본다.
2. 결과의 `source_path` 를 Read 해서 원문을 근거로 일한다. 스니펫을 답으로 쓰지 않는다. 근거 없는 history 는 지어내지 않는다 — 모르면 모른다고 적는다.
3. 검색 결과가 없을 때만 디렉토리를 훑는다. 이때도 전체를 읽지 말고 그 디렉토리의 `INDEX.md` 부터 읽는다.
4. 문서로 정보를 만들 때는 **추론 없이** 쓴다. 출처(커밋, PR, 대화, 파일 경로)가 확인되는 것만 적는다.
5. `main/{repo}` 작업은 항상 새 worktree 에서 한다: `scripts/run-issues/run.sh <issue> <repo>` → `main/{repo}/.worktrees/<issue>`. 끝나면 `scripts/done-issue/run.sh <issue> <repo>` — 커밋하지 않은 변경이 있으면 지우지 않고 잃게 될 파일을 보여 준다. 사용자가 버려도 된다고 할 때만 `--force`.
6. 근거를 따라가야 하면 `related_docs(path)` 로 그 문서와 이어진 문서(개정 이력, 근거, 링크, 같은 entity)를 본다.
7. 60일보다 오래된 맥락이 필요하면 `list_cold` 로 확인하고, `restore_cold` 전에 반드시 사용자에게 "cold 인덱스 X가 있는데 복원할까요"를 묻는다.
8. `main/` 아래 파일은 이 레포에 커밋하지 않는다 (pre-commit hook 이 막는다).

## 결과 문서 저장 위치

| 디렉토리 | 담는 것 | 성질 |
| --- | --- | --- |
| `docs/repository/{repo}/` | 그 repository 에만 해당하는 주제. ADR, 배경, 용어 매핑 | episodic |
| `docs/feature/{topic}/` | 여러 repository 에 걸친 특정 주제 | episodic |
| `docs/specs/{tech_spec}/` | 사람이 쓴 스펙과 AI 가 만든 스펙, 작업 plan | episodic |
| `docs/issues/{issue}/` | 이슈 조사 기록 | episodic |
| `docs/knowledge/` | episodic 에서 걸러져 살아남은 사실. 용어 정의, 시스템 불변식 | knowledge |
| `main/{repo}/docs/` | 관리 대상 repository 자신의 문서 (그 repo 에 커밋) | knowledge |

episodic 은 60일이 지나면 cold 로 간다. knowledge 는 노화하지 않고, 개정판의 `supersedes:` 선언으로만 밀린다.
설계 배경·안내처럼 낡지 않는 문서는 어디에 두든 frontmatter 를 `doc_type: guide` 로 둔다 — knowledge 처럼 노화하지 않아, README 가 링크하는 문서가 cold 로 가며 링크가 깨지지 않는다.
문서를 추가하면 같은 디렉토리의 `INDEX.md` 에 한 줄 요약을 추가한다.

## frontmatter

모든 문서 맨 위에 둔다. 빠진 값은 경로에서 추론하지만, 적어 두는 쪽이 검색 품질이 좋다.

```yaml
---
team: default                # 팀/실. 인덱스 mem-{team}-... 가 갈린다
repo: books-guitar           # 프로젝트(관리 대상 repository). project: 로 써도 된다. 여러 repo 에 걸치면 생략
topic: score-renderer       # 주제. 생략하면 docs/{feature,specs,issues}/{주제}/ 경로에서 정한다
doc_type: adr                # spec | adr | prd | plan | knowledge | guide | note
date: 2026-10-13             # 유효 시각. 이 날짜로 hot/cold 와 판정 순서가 갈린다
supersedes: docs/knowledge/old.md   # (개정판만) 밀어내는 구판 경로
derived_from:                # (선택) 근거 문서 — why 없는 항목은 버려진다
  - path: docs/specs/x/plan.md
    why: 이 plan 의 비교표에서 결론을 가져왔다
relates:                     # (선택) 관련 문서
  - path: docs/repository/books-guitar/adr/0002-export.md
    why: 같은 결정을 내보내기에도 쓴다
---
```

`supersedes`·`derived_from`·`relates` 와 본문의 마크다운 링크는 knowledge graph 의 관계가 된다 — `related_docs` 로 근거와 주변 문서를 따라간다.
knowledge 문서는 근거(`derived_from` 또는 `## 출처` 절)를 반드시 남긴다. 없으면 주간 감사가 `unsourced-fact` 로 알린다.

작성자·작성일을 본문에 쓰지 않는다 — git 이 정본이다.

## 에피소드 기록 (다섯 줄)

결정이 있었던 작업은 문서 끝에 다섯 줄을 남긴다. 배경은 에이전트가 쓰고, 가운데 세 줄(선택지와 결정·이유·그때의 생각)은 사람이 머지 전에 쓴다. 결과는 비워 둔다.

```markdown
## 에피소드
- 배경: (에이전트) 커밋·PR·대화에서 뽑은 무슨 일이 있었나. 출처 링크 포함
- 선택지와 결정: (사람) 어떤 안들이 있었고 무엇을 골랐나
- 이유: (사람) 왜 우리 환경에 맞다고 봤나, 어떤 전제 위에서
- 그때의 생각: (사람) 확신했나 찜찜했나, 어떤 원칙을 따랐나
- 결과: (석 달 뒤 채움)
```

## 주간 compaction

주 1회 `/compaction` ([.claude/commands/compaction.md](./.claude/commands/compaction.md)). 지우지 않고 다시 쓴다.

## 이 규칙의 변경 내역

예전 규칙으로 만들어진 문서를 구분하기 위해 남긴다.

| 날짜 | 변경 |
| --- | --- |
| 2026-05-19 | 최초. "README.md 를 따른다" 한 줄 |
| 2026-10-13 | 메모리 층 도입. search_memory 선행, 추론 없이, 저장 위치·frontmatter, 에피소드 다섯 줄, 주간 compaction, worktree 는 `main/{repo}/.worktrees/` |
| 2026-10-13 | knowledge graph. `related_docs` 로 근거 따라가기, frontmatter `derived_from`·`relates`, knowledge 는 근거 필수 |
| 2026-10-13 | MCP 서버 이름 `drakejin-workspace-memory-mcp`. 검색 필터(날짜·범위·주제·프로젝트)와 `search_filters`, frontmatter `topic`·`project` |
| 2026-10-13 | 낡지 않는 설계·안내 문서는 `doc_type: guide`. 에피소드 절 제목을 "다섯 줄" 로 (사람이 쓰는 가운데 세 줄은 그대로 머지 전) |
