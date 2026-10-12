---
doc_type: spec
date: 2026-10-12
---
# 발표 대비 구현 정렬표

기준 자료: "meta-repository와 memory-mcp" 발표 스크립트(2026-10-11)와 슬라이드 31장.
이 문서는 작업 단계가 끝날 때마다 갱신한다. ✅ 일치 · 🟡 부분/의도적 차이 · ⬜ 미구현.

## 요약

| 범위 | 근접도 | 비고 |
| --- | --- | --- |
| 1부(§1~6) 개인 메모리 | 대상 아님 | 에피소드 다섯 줄만 CLAUDE.md 템플릿으로 반영 |
| §9 meta-repository | ✅ 100% | |
| §11 요구사항 네 개 | 🟡 90% | GitHub 원격은 repos/ 체크아웃 경유, S3 스냅샷은 미검증 |
| §12 기능 여섯 가지·인덱스 설계·도구 네 개 | ✅ 95% | analyzer 보정, tier 보완(아래) |
| §13 데모·주간 compaction | 🟡 85% | 데모 4단계(유무 비교 수치) 미측정 |
| §14 실제 구조·worktree·CLAUDE.md | ✅ 100% | |
| §15~16 조합·knowledge | 🟡 발표와 같은 상태 | knowledge graph는 발표도 "다음 단계" |
| Q&A 답변과의 일치 | 🟡 80% | 세션 시작 훅, 공식 MCP 서버 도구 재사용 안 함 |

## 항목별

### §9·§14 meta-repository

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 메타 정보 = spec, ADR, PRD, AI plan | `doc_type`: spec·adr·prd·plan (+ knowledge, note) | ✅ |
| `CLAUDE.md`, `repos/`, `docs/{repository,feature,specs,knowledge}` + 디렉토리마다 INDEX.md | 같은 구조 | ✅ |
| CLAUDE.md: search_memory 먼저, "추론 없이", 저장 위치·frontmatter, 규칙 변경 내역 | 네 가지 다 있음 | ✅ |
| 작업은 항상 새 worktree | CLAUDE.md 규칙 + 워처가 `repos/{repo}.wt/{branch}/docs`까지 색인 | ✅ |

### §11 요구사항

| # | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 1 | GitHub·로컬 문서 파일을 OpenSearch에 | 로컬 파일. GitHub repository는 `repos/`에 체크아웃해서 적재 (GitHub API 직접 수집 없음) | 🟡 |
| 2 | 팀/실 단위 분리 | `mem-{team}-*` 인덱스와 `mem-{team}-hot` alias. "실" 상위 alias는 없음 | 🟡 |
| 3 | hot/cold | 60일(`MEMORY_HOT_DAYS`) | ✅ |
| 4 | cold는 S3, 필요하면 바로 로드 | raw·JSONL은 S3 지원(테스트는 가짜 S3), 스냅샷 기본은 fs repository. S3 스냅샷은 설정만 있고 `repository-s3` 플러그인이 필요해 미검증 | 🟡 |

### §12 기능 여섯 가지

| # | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 1 | OpenSearch는 로컬 머신에 | docker-compose 단일 노드 2.19.2 | ✅ |
| 2 | meta-repo docs + 관리 대상 repo docs 전체 색인 | 같음 | ✅ |
| 3 | 파일 워칭, 모든 수정은 재색인 | `app/file-watcher`: 추가·수정·삭제, 디바운스 + 직렬 큐, 시작 시 전체 재적재 | ✅ |
| 4 | 임베딩 없이 BM25만 | BM25만. **식별자 분할 analyzer 추가** (아래 "의도적 차이" 1) | 🟡 |
| 5 | hot(60일)·cold(60일 경과) | 같음 | ✅ |
| 6 | 60일 지난 사실은 hot에 새 문서로, 원문은 cold로 | compaction 리포트 + `/compaction` 명령(에이전트가 docs/knowledge에 다시 씀) + tier | ✅ |

### §12 인덱스 설계

| 레이어 | 발표 | 구현 | 상태 |
| --- | --- | --- | --- |
| 인덱스 | `mem-{team}-{yyyy.MM}` + hot 읽기 alias | 같음. alias 이름은 `mem-{team}-hot` | ✅ |
| 문서 필드 | team, repo, doc_type, event_time, source_path, git_sha, header_path, text | 같음 + `chunk_index`, `ingested_at` | ✅ |
| 청킹 | 헤더 계층 단위, 코드 블록 안에서 안 자름 | 같음. 4000자 넘는 섹션은 문단 경계에서 추가로 나눔 | ✅ |
| ISM | hot 60일 → 스냅샷 → 삭제 | 정책 `mem-hot-cold`, ism_template으로 `mem-*`에 자동 부착 (explain으로 확인) | ✅ |
| S3 층 | raw, 청크 JSONL, 스냅샷. JSONL에서 `_bulk` 재적재 | `pnpm memory reload` (e2e 검증) | ✅ |

### §12 MCP 도구

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| `search_memory(team, query, time_range)` | 같음 + `repo`, `doc_type`, `size` 선택 인자 | ✅ |
| `ingest_path(path, team)` | 같음. meta-repo 밖 경로 거부 | ✅ |
| `list_cold(team)` | 같음 | ✅ |
| `restore_cold(index)` + "복원할까요"를 먼저 묻기 | 같음 + `confirmed=true`가 없으면 복원 거부 (설명문에만 기대지 않는 강제 장치) | ✅ |

### §13 데모·운영

| 발표 | 구현 | 상태 |
| --- | --- | --- |
| 데모 1: ADR 저장 → 워처 색인 로그 | `indexed docs/... → mem-team-yyyy.MM (n chunks)` | ✅ e2e |
| 데모 2·3: 배경 설명 없이 search_memory, source_path 근거 | e2e에서 MCP 클라이언트로 검증 | ✅ |
| 데모 4: memory-mcp 유무 도구 호출 수·시간 비교 | 측정 도구 없음. 발표 당일 수치도 비어 있음 | ⬜ |
| 주간 compaction 5단계 | 1·2 = `compaction-report`, 3·4 = `/compaction`(에이전트), 5 = ISM + `tier` | ✅ |

### Q&A 답변과의 일치

| 질문 | 발표 답 | 구현 | 상태 |
| --- | --- | --- | --- |
| 공식 MCP 서버 | SearchIndexTool·GenericOpenSearchApiTool은 그대로 쓰고 적재만 직접 | 도구 네 개를 직접 구현. 공식 서버는 병행 등록하면 됨 | 🟡 |
| 한국어 | 기본 analyzer, 늘면 nori | standard 기반 + 식별자 분할. nori 없음 | ✅ |
| 60일 | ISM 숫자 하나로 바꿈 | `MEMORY_HOT_DAYS` 하나로 ISM과 tier가 같이 바뀜 | ✅ |
| searchable snapshot | 다음 옵션, 지금은 restore | restore | ✅ |
| 보안 | 지금은 논리 분리만 | security 플러그인 끔, 인덱스/alias 분리 | ✅ |
| 충돌 | episodic에서는 처리 안 함, knowledge로 넘길 때만 | compaction 리포트가 후보만 내고 판단은 에이전트·사람 | ✅ |
| search_memory를 안 부르면 | 세션 시작 훅에서 자동 검색 실험 중 | 없음 | ⬜ |

## 의도적 차이

1. **식별자 분할 analyzer.** 발표는 "기본 analyzer + BM25로 식별자가 잘 맞는다"고 하지만, 기본 standard analyzer는 `ClickDeduplicator.window`를 토큰 하나로 묶어서 `ClickDeduplicator window` 질의가 0건이었다. `word_delimiter_graph`(원형 보존, camelCase·구두점 분할)를 넣어 발표의 주장이 실제로 성립하게 했다. 한국어 처리는 그대로 standard다.
2. **ISM + `tier` 병행.** ISM의 `min_index_age`는 인덱스 생성일 기준이라, 오래된 문서를 오늘 처음 적재하면 그 월별 인덱스는 60일 뒤에야 cold로 간다. `pnpm memory tier`는 인덱스 안의 최신 `event_time`으로 판단해서 이 틈을 메운다.
3. **복원본은 ISM에서 뺀다.** 스냅샷 restore는 원래 생성일을 유지해서 그대로 두면 ISM이 바로 다시 cold로 보낸다. restore 시 `auto_manage=false`를 넣고 tier도 건너뛴다.

## 변경 내역

| 날짜 | 단계 | 근접도 |
| --- | --- | --- |
| 2026-10-12 | Phase 1 골격·CLAUDE.md·compose | 25% |
| 2026-10-12 | Phase 2 memory-core | 55% |
| 2026-10-12 | Phase 3·4 memory-mcp·file-watcher, analyzer 보정 | 80% |
| 2026-10-12 | Phase 5 cold 왕복 e2e, compaction 명령, team 접두어 충돌 수정 | 약 90% (발표 범위 §9~§16 기준) |
| 2026-10-12 | 리뷰 반영: 재색인을 "새로 쓰고 낡은 것만 삭제"로, JSONL 손상 줄 건너뛰기, compaction 잘림 경고 | 약 90% (기능 범위 변화 없음, 운영 안전성 보강) |
