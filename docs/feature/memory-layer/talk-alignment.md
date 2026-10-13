---
doc_type: spec
date: 2026-10-13
---
# 구현 정렬표 — 발표 · drakejin 회사 메모리 층 대비

기준 자료 둘:

- **발표** "meta-repository와 memory-mcp" (OpenSearch Korea User Group, 2026-10-11) 스크립트와 슬라이드 31장
- **drakejin 회사** `workspace` 의 메모리 층 — `make memory-up` · `make daemon` · `make daemon-status`, `docs/guides/memory-layer-guide.md`

구현은 Go 한 모듈(`app/`: `memory-mcp`, `daemon`, `internal/**`). ✅ 일치 · 🟡 부분/의도적 차이 · ⬜ 미구현.
확인 방법은 각 행의 "검증" — e2e 이름은 `app/test/e2e/` 의 테스트다.

## 요약

| 범위 | 근접도 | 비고 |
| --- | --- | --- |
| 발표 §9·§14 meta-repository 구조·CLAUDE.md | ✅ | 관리 대상 repo 는 원격 골격대로 `main/{repo}` (발표의 `repos/`) |
| 발표 §11 요구사항 네 개 | ✅ | 팀/실(org alias), hot/cold, S3 스냅샷·cold store (LocalStack e2e) |
| 발표 §12 기능·인덱스·도구 | ✅ | 도구는 발표의 넷 + 그래프 둘. analyzer 보정, knowledge 인덱스 분리 |
| 발표 §13 데모 1~4·주간 compaction | ✅ | 데모 4 는 `scripts/memory/run.sh bench` 로 측정 (아래) |
| 발표 Q&A | 🟡 | 공식 MCP 서버 대신 직접 구현. 세션 시작 훅은 구현 |
| drakejin 회사 memory-up · daemon-status | ✅ | Neo4j 까지 올리고 그래프 상태도 보인다 |
| drakejin 회사 daemon (A·D·E·그래프·주간 감사, 단일 writer) | ✅ | B(cold sync)는 설계가 달라 "cold 소유" 로 대체 |
| drakejin 회사 archive CLI · audit · launchd 스케줄 | ✅ | cold 가 스냅샷이라 archive 는 스냅샷을 다시 쓴다 |
| drakejin 회사 질문 PR · 지식 제안 PR | 🟡 | 개인 레포라 PR 대신 원장(`scripts/memory/run.sh questions`)과 `/compaction` |

## 1. 발표 대비

### §9·§14 meta-repository

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 메타 정보 = spec, ADR, PRD, AI plan | `doc_type`: spec·adr·prd·plan (+knowledge, guide, note) | ✅ |
| `CLAUDE.md`, `repos/`, `docs/{repository,feature,specs,knowledge}` + INDEX.md | 같은 docs 구조 + `docs/issues`. 관리 대상 repo 는 `main/{repo}` | 🟡 경로명만 |
| CLAUDE.md: search_memory 먼저, "추론 없이", 저장 위치·frontmatter, 변경 내역 | 넷 다 + 에피소드 다섯 줄 + `related_docs` 로 근거 따라가기 | ✅ |
| 작업은 항상 새 worktree | `scripts/run-issues/run.sh` → `main/{repo}/.worktrees/{issue}` (색인 제외) | ✅ |

### §11 요구사항

| # | 발표 | 구현 | 상태 · 검증 |
| --- | --- | --- | --- |
| 1 | GitHub·로컬 문서 파일을 OpenSearch 에 | 로컬 파일. GitHub repo 는 `make init` 이 `main/` 에 clone(`docsOnly` 면 docs 만) → 색인 | ✅ |
| 2 | 팀/실 단위 분리 | `mem-{team}-*` + `mem-{team}-hot`, 실은 `MEMORY_TEAM_ORGS` → `mem-org-{org}-hot` | ✅ `TestOrgAliasSearchesEveryTeamOfTheOrg` |
| 3 | hot/cold | 60일(`MEMORY_HOT_DAYS`) | ✅ |
| 4 | cold 는 S3, 필요하면 바로 로드 | 스냅샷(repository-s3) + 원본·청크 JSONL 모두 S3 가능 | ✅ `TestS3ColdLayer` (`make -C app test-e2e-s3`, LocalStack) |

### §12 기능 여섯 가지

| # | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 1 | OpenSearch 는 로컬 머신에 | docker-compose 단일 노드 2.19.2 (9202) | ✅ |
| 2 | meta-repo docs + 관리 대상 repo docs 색인 | 루트 안내 문서, `docs/**`, `main/{repo}/docs/**` | ✅ |
| 3 | 파일 워칭, 모든 수정은 재색인 | 데몬의 fsnotify watcher(힌트) + 60초 수렴 | ✅ |
| 4 | 임베딩 없이 BM25만 | BM25 + **식별자 분할 analyzer** (의도적 차이 1) | 🟡 |
| 5 | hot(60일)·cold(60일 경과) | episodic 만. knowledge 는 노화하지 않는다 (의도적 차이 2) | 🟡 |
| 6 | 60일 지난 사실은 hot 에 새 문서로, 원문은 cold | compaction 리포트 + `/compaction` + `scripts/memory/run.sh tier`(쓰기 막기 → 스냅샷 → 삭제 → hot 원본 제거) | ✅ |

### §12 인덱스·도구

| 항목 | 발표 | 구현 | 상태 · 검증 |
| --- | --- | --- | --- |
| 인덱스 | `mem-{team}-{yyyy.MM}` + 읽기 alias | 같음 + `mem-{team}-knowledge`, alias `mem-{team}-hot`·`mem-org-{org}-hot` | ✅ |
| 문서 필드 | team, repo, doc_type, event_time, source_path, git_sha, header_path, text | 같음 + workspace, memory_kind, sha·stat, title, supersedes, superseded_by, deprecated, summary, entities | ✅ |
| 청킹 | 헤더 계층, 코드 블록 안에서 안 자름 | 같음 (긴 섹션은 문단 경계로 추가 분할) | ✅ |
| ISM | hot 60일 → 스냅샷 → 삭제 | `mem-hot-cold` + knowledge 용 `mem-knowledge-keep`(우선순위 200) | ✅ `TestISMMovesIndexToColdAndTierPrunesHot` (실제 ISM, 6~15분) |
| S3 층 | raw, 청크 JSONL, 스냅샷, JSONL `_bulk` 재적재 | `memory-mcp reload` (cold 로 간 달은 되살리지 않는다) | ✅ `TestColdTierRestoreAndReload`, `TestS3ColdLayer` |
| 도구 | search_memory(team, query, time_range) · ingest_path · list_cold · restore_cold | 같음 + 필터 project·topic·date·doc_type·org·size·include_superseded, 질의어 없이 필터로 가져오기, 필터 값 `search_filters`, 그래프 도구 `related_docs`·`docs_by_entity`. 서버 이름 `drakejin-workspace-memory-mcp` | ✅ `TestMCPToolsAreTheTalksFourPlusFiltersAndGraph`, `TestSearchFiltersDateRangeTopicProject`, 시나리오 G2·G3 |
| restore 전 확인 | 에이전트가 "복원할까요" 먼저 | 설명문 + `confirmed=true` 없으면 거부 | ✅ |

### §13 데모·운영

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 데모 1~3: 저장 → 워처 색인 → 배경 설명 없이 search_memory | e2e + 실제 `claude -p` 로 확인 (GUIDE §4.4) | ✅ |
| 데모 4: 유무 비교 수치 | `scripts/memory/run.sh bench` — 같은 질문을 memory-mcp 유무로 claude 에 시켜 도구 호출·턴·시간·토큰·비용·정답을 잰다 | ✅ (아래 측정) |
| 주간 compaction 5단계 | 1·2 `scripts/memory/run.sh compaction-report`, 3·4 `/compaction`, 5 `scripts/memory/run.sh tier` + ISM, 끝에 `scripts/memory/run.sh audit` | ✅ |

#### 데모 4단계 측정

`examples/sample-workspace` 사본(15 문서, 증류 켬)에서 `scripts/memory/run.sh bench -questions examples/sample-bench.yaml -model haiku -runs 2` (2026-10-13).
두 조건 모두 내장 도구는 Read·Glob·Grep 뿐이고 같은 규칙(샘플의 CLAUDE.md)을 받는다 — 차이는 memory MCP 유무뿐이다.

| 조건 | 정답률 | 도구 호출 | 턴 | 시간 | 입력 토큰 | 비용 |
| --- | --- | --- | --- | --- | --- | --- |
| with (memory-mcp) | 8/8 | 1.5 | 2.5 | 9.5s | 35,921 | $0.0152 |
| without (파일 도구만) | 7/8 | 7.0 | 7.9 | 24.0s | 81,779 | $0.0269 |

- with 는 거의 매번 `search_memory` 한 번으로 근거 문서를 찾고 끝났다
- 차이는 문서가 흩어진 질문에서 컸다: "order-api 는 주문 금액을 어디에서 읽나" 는 without 이 19·16회 도구를 부르다 한 번은 최대 턴(15)에 걸려 실패했다. with 는 1~2회
- 15 문서짜리 샘플이라 절대값보다 방향을 본다. 발표의 주장("배경 설명 없이, 더 적은 탐색으로")과 같은 방향이다
- 첫 측정에서는 with 쪽 memory 도구 호출이 0회였다 — `--setting-sources ""` 가 CLAUDE.md 를 읽지 않고, `--allowedTools` 는 도구를 제한하지 않아서였다. 지금은 `--tools` 로 내장 도구를 좁히고 CLAUDE.md 를 `--append-system-prompt-file` 로 넘긴다

### Q&A

| 질문 | 발표 답 | 구현 | 상태 |
| --- | --- | --- | --- |
| 공식 MCP 서버 | 검색 도구는 공식 것, 적재만 직접 | 도구 일곱 개 직접 구현 (workspace 격리·cold 확인 절차·필터·그래프가 필요해서) | 🟡 |
| 한국어 | 기본 analyzer, 늘면 nori | standard + 식별자 분할 | ✅ |
| 충돌 | episodic 은 처리 안 함, knowledge 로 넘길 때 사람이 | drakejin 회사 판정 게이트를 들였다 (의도적 차이 3) | 🟡 |
| search_memory 를 안 부르면 | 세션 시작 훅 실험 중 | SessionStart 훅이 repo·브랜치로 검색해 맥락을 붙인다 | ✅ `TestSessionContextHookInjectsRepoHistory` |
| 그다음은 | knowledge graph | Neo4j — 문서·entity·repo 와 supersedes·derived_from·relates·링크·mentions | ✅ `TestKnowledgeGraphAndSupersededRelocation` |

## 2. drakejin 회사 메모리 층 대비

### memory-up · daemon-status

| drakejin 회사 | 구현 | 상태 |
| --- | --- | --- |
| docker 확인 → mise 도구 → 의존성 → `compose up` → 헬스 5분 대기 → 최초 색인 → 다음 할 일 안내 | 같음 — `make memory-init` (의존성 = `go build`, 스택 = OpenSearch + Neo4j) | ✅ |
| 멱등, 다른 스택과 병존 | 같음. 9202·7689, `dj-workspace-*`, compose 프로젝트 `drakejin-workspace-memory` | ✅ |
| 상태: 컨테이너 · OpenSearch · 색인 건수 · 마지막 색인 · 데몬 · 대기 질문 · 할 일 | 같음 — `scripts/memory-daemon/run.sh status` + 그래프·heartbeat·마지막 사이클·증류 대기·REFUSED/PARTIAL 표시 | ✅ |

### daemon

| drakejin 회사 개념 | 구현 | 상태 · 검증 |
| --- | --- | --- |
| 파생물의 유일한 writer, 데몬은 하나 | pid 락. 두 번째 데몬 거부, 1회 색인은 건너뜀 | ✅ `TestDaemonLifecycle/writer_는_하나` |
| 트리거 boot · 60s 타이머 · watcher(debounce 3s, 내용은 버리는 힌트) | 같음. watcher 는 색인 범위만 연다 | ✅ `TestDaemonRunWatchAndShutdown` |
| 직렬 1개 + 최대 1회 재실행 | `daemon.Serial` | ✅ |
| A. hot 스캔 → stat/sha diff → 변경분만 | 같음 + sanityMaxFiles. 부분 스캔이면 삭제 보류 | ✅ |
| B. cold sync (S3 manifest → 인덱스) | 해당 없음 — cold 가 인덱스 스냅샷(발표 설계). 대신 "cold 소유": 같은 내용이 cold 에 있으면 되살리지 않는다 | 🟡 |
| D. 판정: episodic 만, latest-wins(>7일) → LLM → 질문 | 같음 (workspace·팀 범위, 상대 점수 0.3). 실제 claude(haiku)로 모순 판정 확인 | ✅ |
| knowledge 는 `supersedes:` 선언만 | 같음. 밀린 knowledge 는 자기 달 인덱스로 옮겨 노화 시작 | ✅ |
| 판정 원장, (본문 sha, 본문 sha) 쌍으로 멱등 | `.cache/judge/{team}.jsonl` | ✅ |
| 질문 PR (`memory/question-*`) | `scripts/memory/run.sh questions` / `scripts/memory/run.sh answer` + 세션 훅 알림 | 🟡 |
| E. 증류 큐: 본문 sha 캐시, 비동기, 기본 꺼짐 | 같음 (`.cache/distill/`), 부팅 백필. 실제 claude 로 15건 확인 | ✅ |
| LLM = 로컬 claude CLI, 격리, API 키 제거, 실패는 폴백 | 같음 | ✅ |
| 재구축 계약: 파생물 = f(hot, cold, 캐시), LLM 0회 | 인덱스 삭제 → 같은 결과, 그래프 rebuild → 같은 통계 | ✅ `TestDaemonLifecycle/재구축_계약`, `graph_CLI` |
| 그래프 (`graph-rebuild`, `graph-query`) | Neo4j. 사이클마다 바뀌었을 때만 workspace 그래프 통째 교체 | ✅ |
| 주간 audit (고아·링크·근거 없는 knowledge·deep pass) | `scripts/memory/run.sh audit [-apply]`, 데몬이 7일마다 보고 | ✅ `TestAuditFindsDriftAndApplyFixesIt` |
| archive CLI (edit·deprecate·purge, `--by`·`--reason` 필수) | 같음 + undeprecate. 스냅샷을 다시 쓴다 | ✅ `TestArchiveActionsOnColdDocument` |
| launchd 스케줄 (6h 기동, 24h 가드) | `scripts/schedule/run.sh install` (tier-tick), `make memory-daemon-start` | ✅ |
| 지식 제안 PR → 머지된 것만 이동 | `/compaction` 이 knowledge 를 다시 쓰고 사람이 확인 뒤 `scripts/memory/run.sh tier` | 🟡 |

## 의도적 차이

1. **식별자 분할 analyzer.** 기본 standard analyzer 는 `ClickDeduplicator.window` 를 토큰 하나로 묶어 `ClickDeduplicator window` 질의가 0건이었다. `word_delimiter_graph`(원형 보존)로 발표의 "식별자는 강한 어휘 신호"가 실제로 성립하게 했다.
2. **knowledge 는 노화하지 않는다.** 발표는 모든 문서를 월별 인덱스에 넣지만, drakejin 회사는 knowledge 를 노화시키지 않는다. knowledge·안내 문서·관리 대상 repo 문서는 `mem-{team}-knowledge` 에 두고 ISM keep 정책을 붙였다. 밀리면 자기 달 인덱스로 옮겨 그때부터 노화한다.
3. **episodic 충돌도 판정한다.** 발표는 episodic 충돌을 무시하지만, drakejin 회사 데몬의 판정 게이트를 들여 최신 문서가 구판을 기본 검색에서 숨기게 했다 (`include_superseded` 로는 보인다).
4. **ISM + `scripts/memory/run.sh tier` 병행, 그리고 cold 소유.** ISM 은 인덱스 생성일 기준이라 늦게 적재된 오래된 문서는 `tier` 가 맡는다. tier 는 스냅샷 뒤 hot 원본을 지운다(drakejin 회사의 "git 삭제"). ISM 이 먼저 옮긴 달의 원본은 다음 tier 가 치운다(`pruned`). 이미 cold 에 같은 내용이 있는 원본은 데몬이 되살리지 않는다.
5. **workspace 격리 + 팀 소유.** 여러 meta-repo 가 OpenSearch·Neo4j 하나를 쓸 수 있게 모든 질의를 workspace 로 거르고, 인덱스 단위로 움직이는 tier·ISM·restore 가 남의 문서를 지우지 않게 팀 하나는 workspace 하나에만 속하게 했다.
6. **cold 가 스냅샷이라 archive 는 스냅샷을 다시 쓴다.** drakejin 회사의 cold 는 문서 단위 S3 오브젝트지만 여기의 cold 는 인덱스 스냅샷(발표 설계)이다. edit·purge 는 그 인덱스만 담은 스냅샷을 alias 없이 잠깐 복원해 고친 뒤 새 스냅샷으로 바꾼다.
7. **git 에 넣지 않는 상태는 전부 `.cache/` (gitignore).** drakejin 회사는 판정·증류 원장을 커밋하지만, 이 레포는 개인 레포라 원장까지 `.cache/` 에 둔다.
   대가: `.cache/judge`·`distill` 을 지우면 사람의 판정 답과 증류 결과가 사라진다 (인덱스·그래프는 다시 만들어진다). 기본 cold 층(`.cache/cold-store`, `.cache/snapshots`)은
   정본이라 지우면 안 된다 — 오래 보관하려면 S3 로 둔다.
8. **tier 는 스냅샷 목록(manifest)을 남긴다.** 쓰기를 막은 인덱스를 스냅샷하고 그 (경로, 내용 sha) 목록을 cold store 에 남겨, "이 내용은 cold 에 있다" 를 시계 비교 없이 정확히 판단한다.
   ISM 이 만든 스냅샷은 목록이 없어 달 단위로 본다.

## 변경 내역

| 날짜 | 단계 | 근접도 |
| --- | --- | --- |
| 2026-10-12 | TS: 골격·코어·MCP·워처·cold e2e | 발표 약 90% |
| 2026-10-13 | 원격 `drakejin/workspace` 골격(main/, init·sync·run-issues) 위로 이동, drakejin 회사 memory-up·daemon·daemon-status 반영 | 발표 약 90%, drakejin 회사 데몬 개념 반영 |
| 2026-10-13 | Go 포팅, TypeScript 제거. 설계 보정: knowledge 인덱스, cold 소유, workspace 격리·팀 소유, 부분 스캔 보호, 종료 신호 전파 | 발표 약 90%, drakejin 회사 약 90% (그래프·audit 제외) |
| 2026-10-13 | Go 를 `app/` 한 모듈로, 생성물은 `.cache/`, 설정은 seed 하나. Neo4j knowledge graph, audit, archive, org alias, SessionStart 훅, bench, launchd. 실제 ISM·S3(LocalStack)·실제 claude 판정·증류 검증 | 발표 약 97%, drakejin 회사 약 95% (질문·지식 PR 은 원장으로 대체) |
| 2026-10-13 | 루트 Makefile 을 다섯 개(init·sync·memory-init·memory-daemon-start/stop)로 줄이고 Go 개발 타겟은 app/Makefile, 나머지는 scripts 로 | 근접도 변화 없음 |
| 2026-10-13 | MCP 서버 `drakejin-workspace-memory-mcp`, 검색 필터(날짜·범위·주제·프로젝트) + 필터만으로 가져오기 + `search_filters`, 판정 이웃은 같은 프로젝트 안, 시나리오 테스트(scenarios/) | 발표 §12 도구 확장 |
