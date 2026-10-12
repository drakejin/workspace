---
doc_type: spec
date: 2026-10-13
---
# 구현 정렬표 — 발표 · vms 메모리 층 대비

기준 자료 둘:

- **발표** "meta-repository와 memory-mcp" (OpenSearch Korea User Group, 2026-10-11) 스크립트와 슬라이드 31장
- **vms** `VMS-Holdings/workspace` 의 메모리 층 — `make memory-up` · `make daemon` · `make daemon-status`, `docs/guides/memory-layer-guide.md`

구현은 Go(`app/memory-mcp`, `app/daemon`, `internal/**`). ✅ 일치 · 🟡 부분/의도적 차이 · ⬜ 미구현.

## 요약

| 범위 | 근접도 | 비고 |
| --- | --- | --- |
| 발표 §9·§14 meta-repository 구조·CLAUDE.md | ✅ | 관리 대상 repo 는 원격 골격대로 `main/{repo}` (발표의 `repos/`) |
| 발표 §11 요구사항 네 개 | 🟡 | GitHub 은 `make init` clone 경유, S3 스냅샷은 설정만(미검증) |
| 발표 §12 기능·인덱스·도구 네 개 | ✅ | analyzer 보정, knowledge 인덱스 분리 |
| 발표 §13 데모·주간 compaction | 🟡 | 데모 4단계(유무 비교 수치) 미측정 |
| vms memory-up | ✅ | 도구 확인(mise) → 빌드 → 스택 → 헬스 대기 → 최초 색인, 멱등 |
| vms daemon (수렴 루프 A·D·E, 단일 writer) | ✅ | B(cold sync)는 설계가 달라 해당 없음 — 아래 |
| vms daemon-status | ✅ | 컨테이너·색인·데몬·판정 질문·할 일 |
| vms 그래프(Neo4j)·archive CLI·audit | ⬜ | 발표 범위 밖(knowledge graph 는 "다음 단계") |

## 1. 발표 대비

### §9·§14 meta-repository

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 메타 정보 = spec, ADR, PRD, AI plan | `doc_type`: spec·adr·prd·plan (+knowledge, guide, note) | ✅ |
| `CLAUDE.md`, `repos/`, `docs/{repository,feature,specs,knowledge}` + INDEX.md | 같은 docs 구조 + `docs/issues`. 관리 대상 repo 는 `main/{repo}` | 🟡 경로명만 |
| CLAUDE.md: search_memory 먼저, "추론 없이", 저장 위치·frontmatter, 변경 내역 | 넷 다 + 에피소드 다섯 줄 | ✅ |
| 작업은 항상 새 worktree | `make run-issues` → `main/{repo}/.worktrees/{issue}` (색인 제외) | ✅ |

### §11 요구사항

| # | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 1 | GitHub·로컬 문서 파일을 OpenSearch 에 | 로컬 파일. GitHub repo 는 `make init` 이 `main/` 에 clone → 색인 | 🟡 |
| 2 | 팀/실 단위 분리 | `mem-{team}-*` + `mem-{team}-hot` alias. "실" 상위 alias 없음 | 🟡 |
| 3 | hot/cold | 60일(`MEMORY_HOT_DAYS`) | ✅ |
| 4 | cold 는 S3, 필요하면 바로 로드 | raw·청크 JSONL 은 S3 지원(가짜 S3 로 테스트). 스냅샷 기본은 fs, s3 는 플러그인 필요해 미검증 | 🟡 |

### §12 기능 여섯 가지

| # | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 1 | OpenSearch 는 로컬 머신에 | docker-compose 단일 노드 2.19.2 (9202) | ✅ |
| 2 | meta-repo docs + 관리 대상 repo docs 색인 | 루트 안내 문서, `docs/**`, `main/{repo}/docs/**` | ✅ |
| 3 | 파일 워칭, 모든 수정은 재색인 | 데몬의 fsnotify watcher(힌트) + 60초 수렴 | ✅ |
| 4 | 임베딩 없이 BM25만 | BM25 + **식별자 분할 analyzer** (의도적 차이 1) | 🟡 |
| 5 | hot(60일)·cold(60일 경과) | episodic 만. knowledge 는 노화하지 않는다 (의도적 차이 2) | 🟡 |
| 6 | 60일 지난 사실은 hot 에 새 문서로, 원문은 cold | compaction 리포트 + `/compaction` + `make tier`(스냅샷 → hot 원본 제거 → 삭제) | ✅ |

### §12 인덱스·도구

| 항목 | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 인덱스 | `mem-{team}-{yyyy.MM}` + 읽기 alias | 같음 + `mem-{team}-knowledge`, alias `mem-{team}-hot` | ✅ |
| 문서 필드 | team, repo, doc_type, event_time, source_path, git_sha, header_path, text | 같음 + workspace, memory_kind, sha·stat, title, supersedes, superseded_by, summary, entities | ✅ |
| 청킹 | 헤더 계층, 코드 블록 안에서 안 자름 | 같음 (긴 섹션은 문단 경계로 추가 분할) | ✅ |
| ISM | hot 60일 → 스냅샷 → 삭제 | `mem-hot-cold` + knowledge 용 `mem-knowledge-keep`(우선순위 200) | ✅ |
| S3 층 | raw, 청크 JSONL, 스냅샷, JSONL `_bulk` 재적재 | `bin/memory-mcp reload` (cold 로 간 달은 되살리지 않는다) | ✅ |
| 도구 | search_memory(team, query, time_range) · ingest_path · list_cold · restore_cold | 같음 + repo·doc_type·size·include_superseded | ✅ |
| restore 전 확인 | 에이전트가 "복원할까요" 먼저 | 설명문 + `confirmed=true` 없으면 거부 | ✅ |

### §13 데모·운영

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 데모 1~3: 저장 → 워처 색인 → 배경 설명 없이 search_memory | e2e + 실제 `claude -p` 로 확인 (GUIDE §4.3) | ✅ |
| 데모 4: 유무 비교 수치 | 측정 도구 없음 | ⬜ |
| 주간 compaction 5단계 | 1·2 `make compaction-report`, 3·4 `/compaction`, 5 `make tier` + ISM | ✅ |

### Q&A

| 질문 | 발표 답 | 구현 | 상태 |
| --- | --- | --- | --- |
| 공식 MCP 서버 | 검색 도구는 공식 것, 적재만 직접 | 도구 네 개 직접 구현 | 🟡 |
| 한국어 | 기본 analyzer, 늘면 nori | standard + 식별자 분할 | ✅ |
| 충돌 | episodic 은 처리 안 함, knowledge 로 넘길 때 사람이 | vms 판정 게이트를 들였다 (의도적 차이 3) | 🟡 |
| search_memory 를 안 부르면 | 세션 시작 훅 실험 중 | 없음 (CLAUDE.md 규칙 + 도구 설명) | ⬜ |

## 2. vms 메모리 층 대비

### memory-up

| vms | 구현 | 상태 |
| --- | --- | --- |
| docker 확인 → mise 도구 → 의존성 → `compose up` → 헬스 5분 대기 → 최초 색인 → 다음 할 일 안내 | 같음 (의존성 = `go build`) | ✅ |
| 멱등 | 같음. 데몬이 돌고 있으면 최초 색인은 건너뛴다 | ✅ |
| 다른 스택과 병존(포트·컨테이너·프로젝트명 분리) | 9202, `dj-workspace-*`, `drakejin-workspace-memory` | ✅ |

### daemon

| vms 개념 | 구현 | 상태 |
| --- | --- | --- |
| 파생물의 유일한 writer, 데몬은 하나 | pid 락. 두 번째 데몬 거부, 1회 색인은 건너뜀 | ✅ |
| 트리거 boot · 60s 타이머 · watcher(debounce 3s, 내용은 버리는 힌트) | 같음. watcher 는 색인 범위만 연다 | ✅ |
| 직렬 1개 + 최대 1회 재실행 | `daemon.Serial` | ✅ |
| A. hot 스캔: targets → docType → 크기·바이너리 게이트 → stat/sha diff → 변경분만 | 같음 + sanityMaxFiles. 부분 스캔이면 삭제 보류 | ✅ |
| tombstone (hot 에서 지운 문서는 색인에서 제거) | 같음. 복원된 cold 인덱스는 건드리지 않음 | ✅ |
| B. cold sync (S3 manifest → 인덱스) | 해당 없음 — cold 가 문서 단위 S3 가 아니라 인덱스 스냅샷(발표 설계). 대신 "cold 소유" 규칙: 같은 내용이 cold store 에 있으면 되살리지 않는다 | 🟡 |
| D. 판정: 같은 project 의 episodic 만, latest-wins(>7일) → LLM → 질문 | 같음 (workspace·팀 범위, 상대 점수 0.3) | ✅ |
| knowledge 는 `supersedes:` 선언만 | 같음 | ✅ |
| 판정 원장(judge-state), (본문 sha, 본문 sha) 쌍으로 멱등 | 같음 | ✅ |
| 질문 PR (`memory/question-*`) | `make questions` / `make answer` (개인 레포라 PR 대신 원장) | 🟡 |
| E. 증류 큐: 본문 sha 캐시, 비동기, 기본 꺼짐 | 같음 (`knowledge-cache/`), 부팅 백필 | ✅ |
| LLM = 로컬 claude CLI, 격리, API 키 제거, 실패는 폴백 | 같음 | ✅ |
| 재구축 계약: 파생물 = f(hot, cold, 캐시), LLM 0회 | e2e 로 검증 (인덱스 삭제 → 같은 결과) | ✅ |
| 그래프 F1·F2, 주간 audit | 없음 | ⬜ |

### daemon-status

| vms | 구현 | 상태 |
| --- | --- | --- |
| 컨테이너 · OpenSearch · 색인 건수 · 마지막 색인 · 데몬 프로세스 · 대기 질문 · 할 일 | 같음 + heartbeat·마지막 사이클·증류 대기 | ✅ |

## 의도적 차이

1. **식별자 분할 analyzer.** 기본 standard analyzer 는 `ClickDeduplicator.window` 를 토큰 하나로 묶어 `ClickDeduplicator window` 질의가 0건이었다. `word_delimiter_graph`(원형 보존)로 발표의 "식별자는 강한 어휘 신호"가 실제로 성립하게 했다.
2. **knowledge 는 노화하지 않는다.** 발표는 모든 문서를 월별 인덱스에 넣지만, vms 는 knowledge 를 노화시키지 않는다. knowledge·안내 문서·관리 대상 repo 문서는 `mem-{team}-knowledge` 에 두고 ISM keep 정책을 붙였다.
3. **episodic 충돌도 판정한다.** 발표는 episodic 충돌을 무시하지만, vms 데몬의 판정 게이트를 들여 최신 문서가 구판을 기본 검색에서 숨기게 했다 (`include_superseded` 로는 보인다).
4. **ISM + `make tier` 병행, 그리고 cold 소유.** ISM 은 인덱스 생성일 기준이라 늦게 적재된 오래된 문서는 `tier` 가 맡는다. tier 는 스냅샷 뒤 hot 원본을 지운다(vms 의 "git 삭제"). 이미 cold 에 같은 내용이 있는 원본은 데몬이 되살리지 않는다.
5. **workspace 격리 + 팀 소유.** 여러 meta-repo 가 OpenSearch 하나를 쓸 수 있게 모든 질의를 workspace 로 거르고, 인덱스 단위로 움직이는 tier·ISM·restore 가 남의 문서를 지우지 않게 팀 하나는 workspace 하나에만 속하게 했다.

## 변경 내역

| 날짜 | 단계 | 근접도 |
| --- | --- | --- |
| 2026-10-12 | TS: 골격·코어·MCP·워처·cold e2e | 발표 약 90% |
| 2026-10-13 | 원격 `drakejin/workspace` 골격(main/, init·sync·run-issues) 위로 이동, vms memory-up·daemon·daemon-status 반영 | 발표 약 90%, vms 데몬 개념 반영 |
| 2026-10-13 | Go 포팅, TypeScript 제거. 설계 보정: knowledge 인덱스, cold 소유, workspace 격리·팀 소유, 부분 스캔 보호, 종료 신호 전파 | 발표 약 90%, vms memory-up·daemon·daemon-status 약 90% (그래프·audit 제외) |
