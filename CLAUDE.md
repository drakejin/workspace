# meta-repository 사용법

이 repository는 여러 repository의 메타 정보(spec, ADR, PRD, AI plan)를 한곳에 모은 meta-repository다.
문서는 `docs/`에 쌓고, 검색은 OpenSearch 기반 memory-mcp가 맡는다. 에이전트는 파일 트리를 뒤지지 말고 먼저 묻는다.

## 작업 규칙

1. **작업을 시작하면 `search_memory`를 먼저 부른다.** 질의는 작업 대상 repository 이름, 서비스명, 함수명, 에러 코드 같은 식별자를 그대로 넣는다. 결과가 부족하면 단어를 바꿔 두세 번 다시 부른다.
2. 돌아온 청크의 `source_path`와 `header_path`를 근거로 일한다. 근거가 없는 history는 지어내지 않는다. 모르면 모른다고 적는다.
3. 검색 결과가 없을 때만 디렉토리를 훑는다. 이때도 전체를 읽지 말고 그 디렉토리의 `INDEX.md`부터 읽는다.
4. 문서로 정보를 만들 때는 **추론 없이** 쓴다. 출처(커밋, PR, 대화, 파일 경로)가 확인되는 것만 적는다.
5. 관리 대상 repository 작업은 항상 새 worktree에서 한다: `git -C repos/{repo} worktree add ../{repo}.wt/{branch} -b {branch}`.
6. cold 범위(60일 이전) 질의가 필요하면 `list_cold`로 확인하고, `restore_cold` 전에 반드시 사용자에게 "cold 인덱스 X가 있는데 복원할까요"를 묻는다.

## 결과 문서 저장 위치

| 디렉토리 | 담는 것 |
| --- | --- |
| `docs/repository/{repo}/` | 그 repository에만 해당하는 주제. ADR, 배경, 용어 매핑 |
| `docs/feature/{topic}/` | 여러 repository에 걸친 특정 주제 |
| `docs/specs/{tech_spec}/` | 사람이 쓴 스펙과 AI가 만든 스펙, 작업 plan |
| `docs/knowledge/` | episodic에서 걸러져 살아남은 사실. 추후 graph DB 적재용 |

문서를 추가하면 같은 디렉토리의 `INDEX.md`에 한 줄 요약을 추가한다.

## frontmatter

모든 문서 맨 위에 둔다. 빠진 값은 경로에서 추론하지만, 적어 두는 쪽이 검색 품질이 좋다.

```yaml
---
team: ads-data-platform     # 팀/실. 인덱스 mem-{team}-{yyyy.MM}이 갈린다
repo: attribution-collector # 관리 대상 repository. 여러 repo에 걸치면 생략
doc_type: adr               # spec | adr | prd | plan | knowledge | note
date: 2026-10-12            # event_time. 이 날짜로 hot/cold가 갈린다
---
```

## 에피소드 기록 (머지 전 세 줄)

결정이 있었던 작업은 문서 끝에 다섯 줄을 남긴다. 배경은 에이전트가, 가운데 세 줄은 사람이 쓴다. 결과는 비워 둔다.

```markdown
## 에피소드
- 배경: (에이전트) 커밋·PR·대화에서 뽑은 무슨 일이 있었나. 출처 링크 포함
- 선택지와 결정: (사람) 어떤 안들이 있었고 무엇을 골랐나
- 이유: (사람) 왜 우리 환경에 맞다고 봤나, 어떤 전제 위에서
- 그때의 생각: (사람) 확신했나 찜찜했나, 어떤 원칙을 따랐나
- 결과: (석 달 뒤 채움)
```

## 주간 compaction

주 1회 `/compaction`(.claude/commands/compaction.md)을 실행한다. 지우지 않고 다시 쓴다.

## 이 규칙의 변경 내역

예전 규칙으로 만들어진 문서를 구분하기 위해 남긴다.

| 날짜 | 변경 |
| --- | --- |
| 2026-10-12 | 최초 작성. search_memory 선행, 추론 없이, frontmatter, 에피소드 다섯 줄, 주간 compaction |
