# 메모리 층 가이드

이 워크스페이스의 문서(`docs/`, `main/{repo}/docs/`, 루트 안내 문서)를 OpenSearch 에 색인하고 Neo4j 에 관계를 그려 두어,
Claude Code 가 파일 트리를 뒤지는 대신 `search_memory` 로 묻게 하는 **메모리 층**의 안내서다.
설치·연결·데이터 확인의 요약은 [README.md](./README.md), 여기서는 원리와 운영을 끝까지 다룬다.

## 목차

1. [무엇이 도는가](#1-무엇이-도는가)
2. [설치 — memory-init · memory-daemon-start · status](#2-설치--memory-init--memory-daemon-start--status)
3. [Claude 에 연결](#3-claude-에-연결)
4. [샘플로 따라 하기](#4-샘플로-따라-하기)
5. [내 문서로 쓰기](#5-내-문서로-쓰기)
6. [운영](#6-운영)
7. [cold 층 — 스냅샷, cold store, S3](#7-cold-층--스냅샷-cold-store-s3)
8. [설정 레퍼런스](#8-설정-레퍼런스)
9. [트러블슈팅](#9-트러블슈팅)
10. [테스트](#10-테스트)

---

## 1. 무엇이 도는가

```
 문서가 들어가는 길                                          에이전트가 묻는 길
 docs/**, main/{repo}/docs/**, README·INDEX·CLAUDE·GUIDE      Claude Code
        │ 저장                                                    │ MCP stdio (scripts/mcp/run.sh → .cache/bin/memory-mcp)
        ▼                                                          ▼
 reconcile 데몬 (.cache/bin/memory-daemon) — 파생물의 유일한 writer   memory-mcp: search_memory · search_filters · related_docs
   트리거: boot 1회 + 60초 타이머 + 파일 watcher(3초 debounce)              docs_by_entity · ingest_path · list_cold · restore_cold
   A. hot 스캔: stat → sha 비교, 바뀐 것만 재색인 / 지운 건 색인에서 뺀다      │
   D. 판정: 비슷한 episodic 끼리 — latest-wins → 로컬 claude → 사람에게 질문     ▼
   C. 수렴: superseded_by·deprecated 를 선언·원장대로             OpenSearch :9202
   E. 증류(선택): 요약·entity                                       mem-{team}-{yyyy.MM}   episodic (60일 뒤 cold)
   F. 그래프: Doc·Entity·Repo 와 관계를 Neo4j 에 통째로             mem-{team}-knowledge   knowledge (노화 없음)
   주간 감사: 색인·파일·cold store·원장·링크 대조 (보고만)          mem-{team}-hot · mem-org-{org}-hot (읽기 alias)
        │                                                        Neo4j :7689  knowledge graph
        ▼                                                                 │ tier / ISM: 스냅샷 → 삭제
 .cache/judge · distill · archive (원장) · daemon (상태)          cold: 스냅샷(.cache/snapshots 또는 S3) + 원본·청크 JSONL(.cache/cold-store 또는 S3)
```

원칙 셋:

1. **콘텐츠의 주인은 git(hot) 과 cold 보관소뿐이다.** 인덱스와 그래프는 파생물이라 지워도 데몬이 같은 결과로 다시 만든다 — 판정·증류는 원장에서 복원하므로 LLM 재호출 0회.
2. **파생물의 writer 는 데몬 하나다.** 인덱스를 직접 고치지 않는다. 문서를 고치면 데몬이 따라 맞춘다. 데몬은 pid 락으로 하나만 돈다.
3. **검색은 경로를 돌려주고, 본문은 원문을 읽는다.** 에이전트는 결과의 `source_path` 를 Read 한다.

문서는 두 성질로 나뉜다.

| 성질 | 어떤 문서 | 저장 | 판정 | 노화 |
| --- | --- | --- | --- | --- |
| episodic (시간이 지나면 낡는 기록) | `docs/` 의 spec·adr·prd·plan·note, `docs/issues/` | 유효 시각(`date`) 달의 인덱스 | 비슷한 문서끼리 자동 판정 | 60일 뒤 cold |
| knowledge (누가 바꾸기 전까지 유효한 사실) | `docs/knowledge/`, `doc_type: knowledge·guide`, 루트 `README`·`INDEX`·`CLAUDE`·`GUIDE`, `main/{repo}/docs/` | `mem-{team}-knowledge` | 개정판의 `supersedes:` 선언만 | 없음. 밀리면 자기 달 인덱스로 옮겨 가 그때부터 노화 |

색인 범위 밖: 루트의 다른 디렉토리(`scenarios/`·`examples/`·`.claude/`·`app/`·`scripts/`), `main/{repo}` 의 코드 트리, `.worktrees/`, `node_modules`·`.git`·`.cache`·`dist`·`build`·`vendor` 등, `.md` 가 아닌 파일, `MEMORY_MAX_FILE_BYTES` 초과, 바이너리.
범위 밖이어도 디스크에 있는 파일을 가리키는 링크는 산 링크다 — 감사는 `broken-link` 로 보지 않고, 그래프는 그 노드를 `out-of-scope` 로 둔다.
크기·바이너리 게이트는 데몬과 `ingest_path` 가 같은 규칙 하나를 쓴다 — `ingest_path` 도 이런 파일은 색인하지 않고 `N개 건너뜀` 으로 알린다.

문서의 정체(`source_path`)는 디스크에 있는 철자를 유니코드 NFC 로 맞춘 경로다. macOS(APFS)는 대소문자와 NFC/NFD 를 구분하지 않아
철자만 다른 경로가 같은 파일을 연다 — `ingest_path` 에 `docs/knowledge/LIMITS.md`(실제는 `limits.md`)나 Finder 가 NFD 로 만든 한글 이름을
NFC 로 친 경로를 줘도 디스크의 철자 하나로 적재하고, 주제(`topic`)·프로젝트(`project`)도 NFC 로 맞춰 비교한다.

---

## 2. 설치 — memory-init · memory-daemon-start · status

준비물(Docker 3GB+, Go 1.25 — mise 면 자동, jq, Claude Code CLI)과 다섯 단계는 [README §1](./README.md#1-설치). 여기서는 각 단계가 하는 일.

### 2.1 `make memory-init` — 인프라 올리기 (멱등)

1. Docker 확인 → 2. Go 1.25 (`app/mise.toml`, mise 가 있으면 설치) → 3. 빌드(`.cache/bin/memory-mcp`, `.cache/bin/memory-daemon`)
→ 4. `docker compose up -d` (OpenSearch 2.19.2 + Neo4j 5.26) → 5. 둘 다 응답할 때까지 최대 5분 대기
→ 6. `memory-daemon -once`: 인덱스 템플릿·매핑·ISM 정책 둘·스냅샷 repository·org alias 를 맞추고 사이클 1회(색인 + 판정 + 그래프).

```
[memory-init] docker: ok
[memory-init] tools: go1.25.6
[memory-init] build: .cache/bin/memory-mcp, .cache/bin/memory-daemon
[memory-init] stack: started
[memory-init] opensearch: waiting http://localhost:9202/_cluster/health
[memory-init] opensearch: ready
[memory-init] neo4j: waiting http://localhost:7476
[memory-init] neo4j: ready
[reconcile] scanned=11 indexed=11 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 flags=0 trigger=boot took=365ms
[graph] docs=14 entities=0 edges=29
[memory-init] reconcile: done

[memory-init] 다음: 'make memory-daemon-start' 로 데몬을 상주시키고, 'scripts/memory-daemon/run.sh status' 로 확인하세요
```

다시 실행해도 안전하다. 데몬이 이미 돌고 있으면 6 의 색인은 데몬에 맡기고 건너뛴다.

### 2.2 `make memory-daemon-start` — 상주

```bash
make memory-daemon-start
```

```
[memory-daemon] launchd: com.drakejin.memory.workspace.daemon (workspace=workspace, root=/Users/jin/Workspace/drakejin/workspace — 로그인 때 다시 켜고, 죽으면 다시 띄운다)
[memory-daemon] started (pid 39975) — 로그 /Users/jin/Workspace/drakejin/workspace/.cache/daemon/daemon.log, 상태 'scripts/memory-daemon/run.sh status'
```

macOS 는 launchd(`com.drakejin.memory.<workspace>.daemon` — meta-repo 마다 따로, RunAtLoad + KeepAlive)에 올린다 — 로그인 때 다시 켜고, 죽으면 다시 띄운다 (kill -9 뒤 약 19초 만에 다시 떴다).
launchd 가 없는 머신은 nohup 백그라운드라(`[memory-daemon] background: nohup (이 머신을 다시 켜면 다시 start 하세요)`) 머신을 다시 켜면 다시 start 한다.
이미 돌고 있으면 그대로 둔다 (`[memory-daemon] already running (pid 39975) — 로그 …`).

멈출 때는 `make memory-daemon-stop` — launchd 에서 내리고(plist 삭제) 떠 있는 데몬에 SIGTERM 을 보내 끝날 때까지 최대 60초 기다린다.

```
[memory-daemon] stopped (launchd 에서 내림)
```

데몬 로그(`.cache/daemon/daemon.log`)는 이렇게 시작한다. 터미널에서 포그라운드로 보려면 `scripts/memory-daemon/run.sh run` (launchd 가 부르는 진입점도 이것이다).

```
[daemon] LLM 백엔드: 로컬 claude CLI (2.1.283 (Claude Code))
[reconcile] scanned=11 indexed=0 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 flags=0 trigger=boot took=11ms
[graph] docs=14 entities=0 edges=29
[daemon] watching /Users/jin/Workspace/drakejin/workspace (루트 문서, docs/, main/*/docs/), interval=1m0s, debounce=3s, judge=on, distill=off, graph=on
```

문서를 저장하면 3초 뒤 색인된다. 변화가 없으면 사이클 비용은 stat-walk 수 ms. 꺼져 있던 동안의 변경은 다음 boot 가 흡수한다.
사이클은 직렬로 하나만 돌고, 도는 중에 들어온 트리거는 한 번으로 합쳐 끝난 뒤 다시 돈다.
데몬은 `.cache/daemon/daemon.lock` 을 flock 으로 쥔다 — 두 번째 데몬은 `데몬이 이미 돌고 있습니다` 로 거부되고, 데몬이 죽으면 커널이 락을 푼다.
그래서 상주 중에 `run` 으로 하나 더 띄울 수는 없다 — 먼저 `make memory-daemon-stop`.

| 로그 | 뜻 |
| --- | --- |
| `[reconcile] scanned= indexed= stat_updated= removed= skipped= failed= cold_owned= flags=` | 스캔한 파일 / 다시 색인 / mtime 만 바뀜 / 색인에서 뺌 / 건너뜀 / 쓰기 실패 / cold 소유라 되살리지 않음 / superseded_by·deprecated 수렴 |
| `[reconcile] skip <path> (<이유>)` · `fail <path> (<오류>)` | 색인하지 않은 파일(`size N > MAX`·`binary content`·frontmatter 오류) / 쓰다 실패한 파일 — 실패는 다음 사이클이 다시 시도하고, 나머지 파일은 계속 돈다 |
| `[judge] superseded A ↔ B (latest-wins …)` · `(llm: 모순, 고확신 …)` · `question …` | 판정 결과 |
| `[distill] path: summary N자, entities M` | 증류 |
| `[graph] docs= entities= edges=` | 그래프 재구축 (바뀐 게 있을 때만) |
| `[audit] 주간 감사: …` | 7일마다 정합성 감사 요약 |
| `[reconcile] matched N files > sanityMaxFiles` / `스캔이 일부만 됐습니다` | 루트를 잘못 잡은 것으로 보고 색인 거부 / 읽지 못한 디렉토리가 있어 이번 사이클은 삭제 보류 (status 에 `REFUSED` · `PARTIAL-SCAN`) |

### 2.3 `scripts/memory-daemon/run.sh status`

데몬이 떠 있을 때 (macOS 면 첫 줄에 launchd 적재 여부):

```
[memory-daemon] launchd: loaded (com.drakejin.memory.workspace.daemon)
[status] container opensearch: running
[status] container neo4j: running
[status] opensearch: yellow (http://localhost:9202)
[status] index: workspace=workspace 11 docs, 97 chunks (memory indices 6)
[status] last indexed: 2026-10-13T00:32:11.795Z
[status] graph: 14 docs, 0 entities, 29 edges (bolt://localhost:7689)
[status] daemon: running pid=39975 since 2026-10-13T00:32:29.33219Z, cycles=1
[status] heartbeat: 1s ago (interval 60s)
[status] last cycle: 2026-10-13T00:32:29.422157Z trigger=boot scanned=11 indexed=0 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 took=11ms
[status] last graph: docs=14 entities=0 edges=29
[status] judge: on, 답을 기다리는 질문 0건
[status] distill: off, cache 0건, 대기 0건
```

데몬이 꺼져 있으면 `daemon: stopped` 아래로 마지막으로 돈 데몬의 사이클이 그대로 보인다.

꺼진 게 있으면 마지막 `할 일` 줄이 무엇을 실행할지 알려 준다. 단일 노드라 `yellow` 가 정상이다.

---

## 3. Claude 에 연결

### 3.1 등록

| 범위 | 방법 |
| --- | --- |
| 이 레포 (project) | 이미 `.mcp.json` 에 `drakejin-workspace-memory-mcp` 로 있다. `claude` 를 열어 한 번 승인 → `/mcp` 로 확인 |
| 모든 디렉토리 (user) | `claude mcp add drakejin-workspace-memory-mcp -s user -- bash ~/Workspace/drakejin/workspace/scripts/mcp/run.sh` |
| 다른 meta-repo (예: 샘플) | **그 디렉토리에서**: `cd ~/memory-demo && claude mcp add memory-demo -s local -e META_REPO_ROOT=$HOME/memory-demo -e MEMORY_DEFAULT_TEAM=demo -- bash ~/Workspace/drakejin/workspace/scripts/mcp/run.sh` (`-s local` 은 명령을 친 디렉토리에 묶인다) |

`scripts/mcp/run.sh` 는 빌드(바뀐 게 없으면 즉시)한 뒤 `memory-mcp serve` 를 stdio 로 띄운다. 빌드 출력은 stderr 로만 가서 MCP 프로토콜을 깨지 않는다.
여러 meta-repo 가 같은 OpenSearch·Neo4j 를 써도 된다 — 모든 질의·삭제·판정·그래프가 `workspace`(기본: 루트 디렉토리 이름)로 갈린다.
단 **팀 이름은 meta-repo 하나에만** 쓸 수 있다. 인덱스 단위로 움직이는 tier·ISM·restore 가 남의 문서를 지우지 않게 하려는 규칙이라,
다른 meta-repo 가 쓰는 팀으로 적재하면 `다른 workspace 가 쓰는 팀입니다` 로 거부된다. 스냅샷 repository 는 클러스터가 같이 쓰지만
`list_cold`·`restore_cold` 도 이 meta-repo 의 팀(문서를 둔 팀, 또는 모든 달이 cold 로 갔어도 이 meta-repo 의 cold store 에 보관본이 있는 팀)만 다루고,
다른 meta-repo 의 팀이면 같은 문구로 거부한다. 팀 이름 `org`·`org-*` 는 org alias 자리라 쓸 수 없다.

### 3.2 도구

| 도구 | 입력 | 하는 일 |
| --- | --- | --- |
| `search_memory` | `query?`, `project?`, `topic?`, `date?`(YYYY-MM-DD), `time_range?{from,to}`, `doc_type?`, `team?`, `org?`, `size?`, `include_superseded?` | hot BM25 검색, 또는 질의어 없이 필터로 가져오기(최신순, 문서 하나에 한 건). 밀린 문서·deprecate 된 문서는 기본으로 숨긴다 |
| `search_filters` | `project?`, `topic?`, `date?`, `time_range?`, `doc_type?`, `team?`, `org?`, `include_superseded?` | 필터에 쓸 수 있는 값과 문서 수 — 프로젝트·주제·문서 종류·날짜 범위·날짜별 수 |
| `related_docs` | `path`, `depth?`(1~3, 기본 2) | 그래프에서 이어진 문서와 그 경로(관계·이유). depth 2 면 같은 entity·같은 repo 를 다루는 문서도 |
| `docs_by_entity` | `entity` (`order-api` 또는 `service:order-api`, 부분 일치) | 증류 entity 로 문서 찾기 |
| `ingest_path` | `path`(meta-repo 기준), `team?` | 데몬보다 먼저 색인 (meta-repo 밖 경로는 거부). 경로는 디스크의 철자(대소문자·NFC)로 맞춘다. 데몬과 같은 게이트 — 바이너리·`MEMORY_MAX_FILE_BYTES` 초과는 색인하지 않고 `N개 건너뜀 … - <path> (<이유>)` 로 알린다 |
| `list_cold` | `team?` | cold 인덱스 목록 (그 인덱스를 담은 스냅샷 전부·최근 시각, 복원 여부). 이 meta-repo 의 팀만 |
| `restore_cold` | `index`, `confirmed?` | cold 인덱스 복원 — 그 인덱스를 담은 스냅샷 전부를 합친다. `confirmed=true` 없이는 복원하지 않고 "사용자에게 먼저 물으라" 고 돌려준다 |

### 3.3 SessionStart 훅

`.claude/settings.json` 이 세션을 열 때마다 `scripts/session-context/run.sh` 를 부른다. 세션 디렉토리가 `main/{repo}` 나 그 worktree
(`main/{repo}/.worktrees/{issue}`)면 repo 이름과 브랜치 이름의 단어로 메모리를 검색해 상위 5건의 경로를 맥락으로 붙인다.
판정 질문이 쌓여 있으면 "사용자에게 `scripts/memory/run.sh questions` 를 알려 줄 것" 도 붙인다. 발표 Q&A 의 "search_memory 를 안 부르면?" 에 대한 답이다.
빌드·검색이 실패하면 아무것도 출력하지 않는다 (세션을 막지 않는다).

---

## 4. 샘플로 따라 하기

[`examples/sample-workspace/`](./examples/sample-workspace/README.md) 는 가상의 서비스(order-api, catalog-api)로 만든 작은 meta-repo 다. `tier` 가 cold 로 보낸 원본을 지우므로 **복사해서** 쓴다.

```bash
cp -R ~/Workspace/drakejin/workspace/examples/sample-workspace ~/memory-demo
cd ~/Workspace/drakejin/workspace
export META_REPO_ROOT=$HOME/memory-demo MEMORY_DEFAULT_TEAM=demo   # 이 셸에서 부르는 scripts/ 가 샘플을 가리킨다
```

| 문서 | 체험하는 기능 |
| --- | --- |
| `docs/repository/order-api/adr/0001-idempotency-key.md` | 식별자 검색(`IdempotencyKeyHeader`), 에피소드 다섯 줄 |
| `docs/feature/payment-retry/2026-08-04-…` · `2026-10-06-…` | 판정 latest-wins — 구판이 기본 검색에서 숨는다 |
| `docs/knowledge/order-status.md` · `order-status-v2.md` | knowledge 개정은 `supersedes:` 선언으로만, 그래프의 `SUPERSEDES` |
| `main/catalog-api/docs/architecture.md` | 관리 대상 repo 의 docs 도 색인된다 (`project` 필터) |
| `docs/repository/order-api/prd/2026-06-order-launch.md` | 60일 지난 문서 → compaction 리포트 → cold → restore |
| `docs/specs/refund-window/plan.md` | AI plan 문서 |

### 4.1 색인 · 판정 · 증류 · 그래프

판정이 LLM 까지 가는 경우를 보려고, 7일 안에 서로 다른 말을 하는 두 문서를 더한다. 증류도 켠다 (모델은 가벼운 haiku).

```bash
cat > ~/memory-demo/docs/specs/refund-window/policy-a.md <<'EOF'
---
team: demo
date: 2026-10-10
---
# 환불 가능 기간
RefundWindowDays 는 7일이다. 결제 완료 시각 기준.
EOF
cat > ~/memory-demo/docs/specs/refund-window/policy-b.md <<'EOF'
---
team: demo
date: 2026-10-12
---
# 환불 가능 기간
RefundWindowDays 는 14일이다. 결제 완료 시각 기준.
EOF
MEMORY_DISTILL_ENABLED=true MEMORY_LLM_MODEL=haiku scripts/memory-daemon/run.sh once
```

실제 출력 (로컬 claude, 약 2분):

```
[judge] superseded_by docs/knowledge/order-status.md → docs/knowledge/order-status-v2.md
[reconcile] scanned=16 indexed=16 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 flags=1 trigger=boot took=625ms
[judge] superseded docs/feature/payment-retry/2026-08-04-retry-policy.md ↔ docs/feature/payment-retry/2026-10-06-retry-policy.md (latest-wins: 유효 시각 차 63.0일 > 7일)
[judge] superseded docs/specs/refund-window/policy-a.md ↔ docs/specs/refund-window/policy-b.md (llm: 모순, 고확신 — 신판이 사실)
[judge] candidates=3 latestWins=1 llm=1 questions=0 idempotentSkip=1
[judge] superseded_by docs/feature/payment-retry/2026-08-04-retry-policy.md → docs/feature/payment-retry/2026-10-06-retry-policy.md
[judge] superseded_by docs/specs/refund-window/policy-a.md → docs/specs/refund-window/policy-b.md
[graph] docs=16 entities=0 edges=15
[audit] 주간 감사: 이상 없음
[distill] CLAUDE.md: summary 93자, entities 5
[distill] README.md: summary 91자, entities 3
…
[reconcile] scanned=16 indexed=0 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 flags=0 trigger=settle took=10ms
[graph] docs=16 entities=27 edges=53
```

- `order-status-v2.md` 의 `supersedes:` 가 구판을 밀었다 (`flags=1`)
- 결제 재시도 두 문서는 비슷하고 유효 시각이 7일 넘게 차이 나서 LLM 없이 최신이 이겼다
- 환불 기간 두 문서는 7일 이내라 로컬 claude 가 "모순, 신판이 사실" 로 판정했다. 확신이 낮거나 claude 를 쓸 수 없었다면 질문으로 남는다 (4.5)
- 증류가 끝나면 데몬이 한 번 더 돌아(`trigger=settle`) entity 를 그래프에 넣는다

### 4.2 검색

```bash
scripts/memory/run.sh search IdempotencyKeyHeader
```

```
team=demo · 청크 2건 · 문서 2건 (BM25)

[1] docs/repository/order-api/adr/0001-idempotency-key.md › ADR-0001 결제 요청 멱등 키 > 결정  (adr, project=order-api, 2026-10-08; score 7.73)
요약: order-api의 결제 요청에 멱등성 키를 도입하여 이중 결제 문제를 방지하는 아키텍처 결정을 기술합니다.
## 결정

order-api 가 결제 대행사로 보내는 모든 결제 요청에 `IdempotencyKeyHeader`(`Idempotency-Key`)를 붙인다.
…
```

머리의 `청크 N건 · 문서 M건` 은 맞은 청크 수와 그 청크들이 나온 문서 수다 — 질의어 검색은 청크(제목 단위로 자른 조각) 단위라 한 문서의 여러 절이
따로 나와 결과 수(`size`)를 나눠 쓸 수 있다 (여기서는 ADR 과 샘플 README 가 한 청크씩. 필터로 가져오기의 `문서 N건` 은 문서 수). 함수명·필드명이 그대로 맞는다 — 식별자 분할 analyzer 가 `ClickDeduplicator.window` 를 원형과 `click`·`deduplicator`·`window` 로 함께 색인한다.
`요약:` 줄은 증류가 붙인 것이다. 밀린 구판까지 보려면 `-superseded` (플래그는 질의어 앞에):

```bash
scripts/memory/run.sh search -superseded PaymentRetryPolicy
```

```
[1] docs/feature/payment-retry/2026-10-06-retry-policy.md › 결제 재시도 정책 > 결정  (spec, topic=payment-retry, 2026-10-06; score 6.04)
[2] docs/feature/payment-retry/2026-08-04-retry-policy.md › 결제 재시도 정책 > 결정  (spec, topic=payment-retry, 2026-08-04; score 2.39)  [superseded → docs/feature/payment-retry/2026-10-06-retry-policy.md]
```

`main/catalog-api/docs` 만 보려면 `scripts/memory/run.sh search -project catalog-api CatalogItem`.

**필터로 가져오기** — 날짜(`-date`), 범위(`-from`/`-to`), 주제(`-topic`), 프로젝트(`-project`), 종류(`-type`). 질의어 없이 필터만 주면 맞는 문서를
최신순으로 하나씩 가져온다 (MCP `search_memory` 의 `date`·`time_range`·`topic`·`project`·`doc_type` 과 같다). 어떤 값이 있는지는 `filters`:

```bash
scripts/memory/run.sh filters
scripts/memory/run.sh search -topic payment-retry
scripts/memory/run.sh search -project order-api -from 2026-10-01
```

```
team=demo · 문서 12건 (밀린·deprecate 된 문서 제외)
project: order-api(4) catalog-api(1) (없음)(7)
topic: payment-retry(2) refund-window(1) (없음)(9)
doc_type: guide(6) adr(1) knowledge(1) note(1) plan(1) prd(1) spec(1)
date: 2026-06-15 ~ 2026-10-13
날짜별(최신 7일): 2026-10-13(6) 2026-10-11(1) 2026-10-09(1) 2026-10-08(1) 2026-10-06(1) 2026-10-02(1) 2026-06-15(1)

team=demo · topic=payment-retry · 문서 2건 (필터, 최신순)
[1] docs/feature/payment-retry/INDEX.md › docs/feature/payment-retry  (guide, topic=payment-retry, 2026-10-13)
[2] docs/feature/payment-retry/2026-10-06-retry-policy.md › 결제 재시도 정책  (spec, topic=payment-retry, 2026-10-06)

team=demo · project=order-api, time_range=2026-10-01~ · 문서 3건 (필터, 최신순)
[1] docs/repository/order-api/INDEX.md › docs/repository/order-api  (guide, project=order-api, 2026-10-13)
[2] docs/specs/refund-window/plan.md › 환불 가능 기간 14일 확대 plan  (plan, project=order-api, topic=refund-window, 2026-10-11)
[3] docs/repository/order-api/adr/0001-idempotency-key.md › ADR-0001 결제 요청 멱등 키  (adr, project=order-api, 2026-10-08)
```

(4.1 의 환불 기간 두 문서를 더하기 전 샘플에서 찍은 출력이다. 주제는 `docs/feature|specs|issues/{주제}/` 경로나 frontmatter `topic`,
프로젝트는 `docs/repository/{프로젝트}/`·`main/{프로젝트}/docs/` 경로나 frontmatter `repo`/`project` 에서 정한다. `-from` 처럼 날짜만 주면 그날 전체가 들어간다.
주제는 NFC·소문자로, 글자·숫자가 아닌 것은 하이픈으로 맞춰 비교한다 — Finder 가 NFD 로 만든 디렉토리 이름도 친 그대로 맞는다.
글자·숫자가 하나도 없는 주제(`-topic '!!!'`)나 공백뿐인 프로젝트는 필터를 조용히 빼지 않고 `topic 에 글자·숫자가 없습니다` 로 거부한다.)

### 4.3 그래프

```bash
scripts/memory/run.sh graph stats                # 통계
scripts/memory/run.sh graph entity order-api     # entity 로 문서 찾기
```

```
workspace=memory-demo
nodes: Doc=16 Entity=27 Repo=2
edges: IN_REPO=5 LINKS_TO=7 MENTIONS=38 SUPERSEDES=3

service:order-api — README.md, docs/feature/payment-retry/2026-08-04-retry-policy.md, docs/repository/order-api/adr/0001-idempotency-key.md, docs/specs/refund-window/plan.md
api:order-api — docs/feature/payment-retry/2026-10-06-retry-policy.md, main/catalog-api/docs/architecture.md
repo:order-api — docs/repository/order-api/INDEX.md
```

entity 이름은 증류(LLM)가 뽑아 같은 대상이 `service:`·`api:`·`repo:` 로 갈릴 수 있다 — 부분 일치로 함께 찾는다.

```bash
scripts/memory/run.sh graph neighbors -depth 1 docs/knowledge/order-status-v2.md
```

```
docs/knowledge/order-status-v2.md 와 이어진 문서 2건 (knowledge graph)
- [1] docs/knowledge/INDEX.md (hot) — LINKS_TO
- [1] docs/knowledge/order-status.md (superseded) — SUPERSEDES(현행 판 → 밀린 판)
```

`SUPERSEDES` 셋은 knowledge 선언 하나와 판정 둘이다. Neo4j 브라우저에서 그림으로 보려면 [README §4.2](./README.md#42-neo4j--knowledge-graph).

### 4.4 Claude 에서 물어보기

3.1 의 `memory-demo` 서버를 등록하고 `cd ~/memory-demo && claude`.

| 프롬프트 | Claude 가 하는 일 |
| --- | --- |
| "결제 재시도는 지금 몇 번 해? 근거 문서도" | `search_memory("PaymentRetryPolicy …")` → 현행 문서(5회, 2026-10-06)를 Read 하고 경로로 답한다. 구판(3회)은 숨겨져 있다 |
| "order-api 에서 이중 결제를 어떻게 막기로 했지?" | ADR-0001 을 찾아 `IdempotencyKeyHeader` 결정과 버린 대안을 설명한다 |
| "주문 상태 값 목록이 뭐야? 무엇을 근거로 정했어?" | knowledge v2 의 네 상태 + `related_docs` 로 구판·근거 문서까지 |
| "주문 API 1차 출시 때 활성 주문을 어떻게 정의했지?" | (4.6 의 tier 뒤라면) hot 에 없으니 `list_cold` → "cold 인덱스 mem-demo-2026.06 가 있는데 복원할까요?" 라고 **먼저 묻는다** |

비대화식으로 한 줄 확인:

```bash
(cd ~/memory-demo && claude -p "memory MCP 의 search_memory 로 'PaymentRetryPolicy' 를 검색하고, 현재 결제 재시도 횟수와 근거 문서 경로만 한 줄로 답해." \
  --allowedTools "mcp__memory-demo__search_memory" < /dev/null)
# → 결제 재시도 횟수는 5회, 근거 문서는 docs/feature/payment-retry/2026-10-06-retry-policy.md 입니다.
```

(괄호로 감싸 셸의 현재 디렉토리는 워크스페이스 루트에 남긴다 — 아래 명령들은 루트에서 부른다.)

### 4.5 판정이 사람에게 묻는 경우

LLM 판정의 확신이 낮거나 claude 를 쓸 수 없으면 질문으로 남는다 (`MEMORY_CLAUDE_BIN=/nonexistent scripts/memory-daemon/run.sh once` 로 재현할 수 있다).

```bash
scripts/memory/run.sh questions
scripts/memory/run.sh answer <pairKey> latest     # latest=최신이 사실 · old=구판이 사실 · keep=둘 다 유지
scripts/memory-daemon/run.sh once                 # 데몬이 켜져 있으면 다음 사이클이 반영한다
```

답은 `.cache/judge/{team}.jsonl` 원장에 남는다. 같은 내용 쌍(본문 sha 쌍)은 다시 묻지 않고, 본문이 바뀌면 다시 판정한다.
결론은 경로가 아니라 내용을 따른다 — 파일 이름을 바꾸거나 옮겨도(`git mv`) 결론이 새 경로로 따라가고(원장의 경로는 판정 때 그대로 남는다),
이긴 문서를 지우면 밀렸던 문서가 다시 보인다.
14일 넘게 답이 없는 질문은 주간 감사가 `stale-question` 으로 보고한다.

### 4.6 compaction 과 cold

```bash
scripts/memory/run.sh compaction-report -team demo   # 1. 수명 확인(60일 지난 episodic) · 2. 충돌 후보(같은 repo·주제 묶음)
scripts/memory/run.sh tier -team demo -dry-run        # 미리보기
scripts/memory/run.sh tier -team demo                 # 쓰기 막기 → 스냅샷 → 인덱스 삭제 → hot 원본 제거
```

```
## 1. 수명 확인 — 경과한 episodic 문서 2건
- `docs/repository/order-api/prd/2026-06-order-launch.md` (prd, order-api, 2026-06-15)
- `docs/feature/payment-retry/2026-08-04-retry-policy.md` (spec, 2026-08-04)

## 2. 충돌 후보 — 같은 repo·같은 주제를 다룬 문서 묶음 0개
```

```
tiered           mem-demo-2026.06  newest=2026-06-20T00:00:00.000Z
  - hot 에서 뺌: docs/knowledge/order-status.md
  - hot 에서 뺌: docs/repository/order-api/prd/2026-06-order-launch.md
tiered           mem-demo-2026.08  newest=2026-08-04T00:00:00.000Z
  - hot 에서 뺌: docs/feature/payment-retry/2026-08-04-retry-policy.md
kept-hot         mem-demo-2026.10  newest=2026-10-12T00:00:00.000Z

hot 원본 3개를 cold 로 넘겼습니다 (git rm). 원본은 cold store raw 와 스냅샷에 있습니다 — 'git commit' 하세요.
```

현행 knowledge(`order-status-v2.md`)는 노화하지 않는다. 하지만 v2 에 밀린 구판 `order-status.md`(2026-06-20)는 자기 달 인덱스로 옮겨 가 있어서
episodic 과 같이 cold 로 간다 — 리포트(episodic 만 보는 수명 확인)에는 없지만 tier 에는 나온다.
지우기 전에 cold store 에 같은 내용(sha)이 있는지 확인하므로 그 사이에 고친 문서는 지우지 않는다. git 레포면 `git rm` 으로 지워 커밋만 하면 된다.

```bash
scripts/memory/run.sh list-cold
scripts/memory/run.sh restore mem-demo-2026.06    # CLI 는 사람이 치는 것이라 바로 복원한다
scripts/memory/run.sh search ActiveOrder          # 복원된 PRD 가 다시 검색된다
```

```
team=demo cold 인덱스 2개 (repository mem-cold)
- mem-demo-2026.06  snapshot=cold-mem-demo-2026.06-20261013t003025-064934000  at=2026-10-13T00:30:25.062Z
- mem-demo-2026.08  snapshot=cold-mem-demo-2026.08-20261013t003025-237458000  at=2026-10-13T00:30:25.062Z

mem-demo-2026.06 을(를) snapshot cold-mem-demo-2026.06-20261013t003025-064934000 에서 hot 으로 복원했습니다. 이제 search_memory 에 포함됩니다 (ISM 자동 관리 제외).

[1] docs/repository/order-api/prd/2026-06-order-launch.md › 주문 API 1차 출시 PRD > 활성 주문 정의  (prd, project=order-api, 2026-06-15; score 3.03)
```

복원본은 검색 alias 를 다시 붙이고 그 사이 deprecate 된 표시를 다시 입힌다. ISM·tier 대상에서 빠지고(`auto_manage=false`), 데몬도 건드리지 않는다.

한 달이 스냅샷 여러 개에 나뉘어 있을 수 있다 — 그 달을 tier 한 뒤 그 달 날짜의 문서가 늦게 들어오면(예: seed 에 repo 를 더해 옛 커밋의 문서가 들어온다)
인덱스가 hot 에 다시 생기고, 다음 tier 가 또 스냅샷을 뜬다. 이때 `list-cold` 는 `snapshot=A,B` 로 전부 보여 주고, 복원은 전부를 합친다
(같은 문서는 더 최근 스냅샷의 것). 이미 복원돼 있으면 빠진 스냅샷 문서만 더한다. 그 달의 인덱스가 hot 에 다시 열려 있는 동안은
(`[hot 에 다시 열림 — 다음 tier 뒤에 복원할 수 있다]`) 복원·archive 를 거부한다 — 데몬이 관리하는 인덱스에 cold 문서를 섞으면
데몬이 파일 없는 문서로 보고 지운다. 다음 tier 가 그 달을 다시 cold 로 보낸 뒤에 한다.

### 4.7 효과 재기 — `bench` (발표 데모 4단계)

같은 질문을 memory-mcp 가 있을 때(`with`: 파일 도구 + memory 도구)와 없을 때(`without`: Read·Glob·Grep 만) claude 에 시켜
도구 호출 수·턴·시간·토큰·비용·정답 여부를 나란히 잰다. 질문 파일은 **meta-repo 밖**에 둔다 — 안에 있으면 without 쪽이 정답을 Grep 할 수 있어 거부한다.

```bash
scripts/memory/run.sh bench -questions ~/Workspace/drakejin/workspace/examples/sample-bench.yaml -model haiku
# → ~/memory-demo/.cache/bench/{시각}.md (META_REPO_ROOT 아래)
```

질문 형식 (`examples/sample-bench.yaml`):

```yaml
questions:
  - id: retry-count
    prompt: 결제 재시도는 지금 몇 회 하나? 숫자와 근거 문서 경로(source_path)를 한 줄로 답해.
    expect: ["5", "2026-10-06-retry-policy.md"]     # 모두 들어 있으면 정답
```

샘플에서 잰 결과는 [docs/feature/memory-layer/talk-alignment.md](./docs/feature/memory-layer/talk-alignment.md#데모-4단계-측정).

---

## 5. 내 문서로 쓰기

### 5.1 어디에, 어떤 frontmatter 로

위치 표는 [CLAUDE.md](./CLAUDE.md#결과-문서-저장-위치). 문서를 추가하면 같은 디렉토리의 `INDEX.md` 에 한 줄을 더한다.

```yaml
---
team: default                # 생략하면 MEMORY_DEFAULT_TEAM
repo: books-guitar           # 프로젝트 — project: 로 써도 된다. 여러 repo 에 걸치면 생략 (main/{repo}/docs 와 docs/repository/{repo}/ 는 경로로 안다)
topic: score-renderer        # 주제 — 생략하면 docs/{feature,specs,issues}/{주제}/ 경로에서 정한다 (topic 필터)
doc_type: adr                # spec | adr | prd | plan | knowledge | guide | note (생략하면 경로로 추론. 낡지 않는 설계·안내 문서는 guide)
date: 2026-10-13             # 유효 시각 — hot/cold 와 판정 순서가 이걸로 갈린다 (생략하면 마지막 커밋 시각, 커밋 전이면 파일 mtime)
supersedes: docs/knowledge/score-format.md        # (개정판만) 밀어내는 구판
derived_from:                                     # (선택) 근거 문서 — 그래프 DERIVED_FROM. why 가 없으면 버린다
  - path: docs/specs/score-renderer/plan.md
    why: 렌더러 선택 근거를 이 plan 의 비교표에서 가져왔다
relates:                                          # (선택) 관련 문서 — 그래프 RELATES
  - path: docs/repository/books-guitar/adr/0002-export.md
    why: 같은 렌더러를 내보내기에도 쓴다
---
```

본문의 마크다운 링크(`[..](../x.md)`)도 그래프의 `LINKS_TO` 가 된다. 링크 대상이 디스크에도 hot 에도 cold 에도 없거나 디스크의 철자와 대소문자만 다르면(GitHub 에서 깨진다)
주간 감사가 `broken-link` 로 알린다 (`scenarios/` 처럼 색인 범위 밖이어도 디스크에 그 철자로 있으면 산 링크다).
knowledge 는 근거가 있어야 한다 — `derived_from` 이나 `## 출처` 절이 없으면 감사가 `unsourced-fact` 로 알린다.

### 5.2 예제 — Claude 에게 ADR 을 쓰게 하기

데몬을 켜 둔 상태에서 워크스페이스에서 `claude` 를 열고:

> books-guitar 에서 악보 렌더러를 VexFlow 로 고른 결정을 ADR 로 남겨 줘. 대안은 abcjs 와 직접 SVG 였어.
> `docs/repository/books-guitar/adr/0001-score-renderer.md` 에 frontmatter 를 붙여 쓰고, 문서 끝에 에피소드 다섯 줄을 남겨.
> 배경은 네가 커밋·대화에서 확인되는 것만 쓰고, 선택지·이유·그때의 생각은 비워 두고 나한테 물어봐. INDEX.md 도 갱신해.

저장되면 데몬 로그에 `indexed=1` 이 찍힌다. 새 세션에서 "악보 렌더러 왜 VexFlow 였지?" 라고 물으면 `search_memory` 로 이 ADR 을 찾아 답한다.

### 5.3 예제 — knowledge 개정

사실이 바뀌면 구판을 고치지 않고 **새 문서**를 쓰고 `supersedes:` 로 구판을 가리킨다. 다음 사이클에 구판은 기본 검색에서 숨고
(`include_superseded` 로는 보인다), knowledge 인덱스를 떠나 자기 달 인덱스로 옮겨 가 그때부터 노화한다. 밀린 사슬은 그래프에서 `SUPERSEDES*` 로 보인다.
knowledge 는 유사도로 판정하지 않으므로 선언이 없으면 구판이 계속 살아 있다.

### 5.4 예제 — 관리 대상 repository 의 문서

`main/{repo}/docs/` 의 문서는 그 repo 의 것이라 이 레포에 커밋하지 않지만, 데몬이 색인한다 (knowledge, `repo` 자동).
`make sync` 로 받은 변경도 다음 사이클에 반영된다. 작업은 `scripts/run-issues/run.sh <issue> <repo>` 로 만든 `main/{repo}/.worktrees/` 에서 하고, worktree 사본은 색인하지 않는다.

---

## 6. 운영

| 주기 | 할 일 | 명령 |
| --- | --- | --- |
| 매일 | 데몬이 살아 있나 | `scripts/memory-daemon/run.sh status` (상주는 `make memory-daemon-start`) |
| 질문이 쌓이면 | 판정 질문에 답하기 | `scripts/memory/run.sh questions` → `scripts/memory/run.sh answer <pairKey> <답>` |
| 주 1회 | compaction | Claude 에서 `/compaction [team]` |
| 매일 (자동) | 60일 지난 episodic → cold | `scripts/schedule/run.sh install` (6시간마다 깨워 하루 한 번 tier + 감사 보고) |
| 주 1회 (데몬이 자동) | 정합성 감사 | 데몬 로그 `[audit]`, 직접 `scripts/memory/run.sh audit [-apply]` |
| 필요할 때 | 틀린 cold 문서 고치기 | `scripts/memory/run.sh archive ...` (6.4) |
| 매핑을 바꿨을 때 | 청크 JSONL 에서 전체 재적재 | `scripts/memory/run.sh reload [-team T]` |
| 그래프가 이상할 때 | 그래프 다시 만들기 | `scripts/memory/run.sh graph rebuild` (LLM 0회) |

### 6.1 주간 compaction — `/compaction`

[`.claude/commands/compaction.md`](./.claude/commands/compaction.md) 의 절차: 1·2 리포트(`scripts/memory/run.sh compaction-report -team T`) → 원문 대조 → 3 유효하지 않은 정의 분리 → 4 살아남은 사실을 오늘 날짜 knowledge 로 다시 쓰기(`derived_from` 에 근거) → 5 `scripts/memory/run.sh tier -team T` (먼저 `-dry-run`) → 6 `scripts/memory/run.sh audit`.
**지우지 않고 다시 쓴다.** cold 로 간 원문은 스냅샷과 cold store 에 남는다.

### 6.2 tier 와 ISM

둘 다 60일 지난 episodic(과 밀린 knowledge)을 cold 로 보낸다. 기준이 달라서 둘 다 둔다.

| | `tier` | ISM `mem-hot-cold` |
| --- | --- | --- |
| 기준 | 인덱스 안 문서의 **유효 시각** 중 가장 최근 것 | 인덱스 **생성 시각** |
| 하는 일 | 쓰기 막기 → 스냅샷(끝까지 뜬 것만 믿는다) → 스냅샷 목록(manifest) 기록 → 인덱스 삭제 → hot 원본 제거(sha 가 cold store raw 와 같을 때만) | 스냅샷 → 인덱스 삭제 (파일은 모른다) |
| 범위 | 이 workspace 의 팀 인덱스만 (`-team` 이 없으면 이 workspace 의 모든 팀) | 클러스터의 모든 `mem-*` 월별 인덱스 |
| 놓치는 것 | 돌리지 않으면 아무 일도 없다 | 늦게 적재된 오래된 문서 (생성은 최근) |

순서에는 이유가 있다. 쓰기를 막아 스냅샷과 목록이 정확히 같은 내용이 되고, 인덱스를 원본보다 먼저 지워 그 사이에 도는 데몬 사이클이
남은 파일을 cold 소유로 보게 한다 (원본을 먼저 지우면 데몬이 tombstone 으로 cold store 청크까지 지운다).

문서가 하나도 없는 인덱스(그 달의 유일한 문서를 지우거나 옮겼다)는 스냅샷하지 않는다 — 빈 스냅샷은 "그 달을 cold 가 가졌다" 는 표시가 되어
그 달에 나중에 쓴 hot 문서까지 cold 소유로 읽힌다. 달(인덱스 이름의 연.월)이 hot 기간을 다 지났으면 스냅샷 없이 지우고(`dropped-empty`,
미리보기는 `would-drop-empty`), 아니면 그대로 둔다(`kept-hot`). 스냅샷 뒤 인덱스 삭제가 실패하면 쓰기 막기를 풀어 인덱스를 hot 으로 돌려놓고,
뒤의 인덱스에서 실패해도 그때까지 한 일(지운 인덱스, hot 에서 뺀 원본)을 `[tier] 중간에 실패했습니다 — 그 전에 한 일:` 아래에 에러와 함께 출력한다.

ISM 이 먼저 옮긴 달은 hot 원본이 남아 데몬 로그에 `cold_owned` 가 찍힌다. 다음 `scripts/memory/run.sh tier` 가 그 원본을 치운다(`pruned`).
knowledge 인덱스는 `mem-knowledge-keep`(우선순위 200)이라 ISM 이 건드리지 않는다.

### 6.3 정합성 감사 — `audit`

`scripts/memory/run.sh audit` 는 보고만 하고, `-apply` 를 붙이면 고칠 수 있는 것을 고친다.

| 종류 | 뜻 | `-apply` |
| --- | --- | --- |
| `orphan-index` | 색인에는 있는데 파일이 없다. 같은 파일이 디스크에 다른 철자(대소문자·NFD)로 있으면 옛 철자의 색인이라고 디스크의 철자를 알린다 | 색인에서 뺀다 (청크 JSONL 도 — 다른 철자로 살아 있는 파일의 것은 남긴다) |
| `unindexed` | 파일이 있는데 색인에도 cold 에도 없다 | 색인한다 |
| `stale-content` | 색인 내용(sha)이 파일과 다르다 (stat 이 보존된 복사 등) | 다시 색인한다 |
| `orphan-chunks` | cold store 에 청크만 남았다 | 지운다 (raw 원본은 이력이라 남긴다) |
| `broken-link` | 링크 대상이 디스크에도 hot 에도 cold 에도 없다. 디스크의 철자와 대소문자만 다른 링크도 알린다(GitHub 에서 깨진다 — 설명에 디스크의 철자). 색인 범위 밖이어도 디스크에 그 철자로 있으면 산 링크다 | 보고만 |
| `unsourced-fact` | knowledge 에 근거가 없다 | 보고만 |
| `stale-question` | 14일 넘게 답을 기다리는 판정 질문 | 보고만 |
| `corrupted-ledger` | 원장에 읽을 수 없는 줄 | 보고만 (그 줄은 건너뛴다) |
| `partial-scan` | 읽지 못한 디렉토리가 있다 (권한) | 보고만 — 그 감사에서는 지우는 조치(`orphan-index`·`orphan-chunks`)를 건너뛴다 |

### 6.4 cold 문서 조치 — `archive`

cold 로 간 문서는 git 에 없으니 파일을 고쳐서 바꿀 수 없다. 사람이 감사 기록(`-by`, `-reason`)과 함께 조치한다 → `.cache/archive/{team}.jsonl`.

```bash
scripts/memory/run.sh archive deprecate   -by jin -reason "결정 번복" docs/repository/x/adr/0001.md   # 기본 검색에서 숨긴다. 복원해도 다시 입혀진다
scripts/memory/run.sh archive undeprecate -by jin -reason "되돌림" ...
scripts/memory/run.sh archive edit        -by jin -reason "오타" -file /tmp/fixed.md ...       # 같은 달 안에서만. 스냅샷을 다시 쓴다
scripts/memory/run.sh archive purge       -by jin -reason "개인정보" ...                     # raw·청크·스냅샷에서 지운다. hot 에 파일이 있으면 거부
```

edit·purge 는 그 인덱스를 담은 스냅샷 **전부**를 검색 alias 없이 잠깐 합쳐 복원해 고치고 새 스냅샷 하나로 바꾼다. 남아야 할 문서가 새 스냅샷에
모두 들어 있을 때만 옛 스냅샷(과 그 목록 줄)을 지운다. 다른 인덱스가 섞인 스냅샷과, 그 달의 인덱스가 hot 에 다시 열려 있는 경우는 거부한다.

### 6.5 org("실") 단위 검색

`MEMORY_TEAM_ORGS=payments=commerce,orders=commerce` 면 `mem-org-commerce-hot` alias 가 두 팀의 인덱스를 묶는다.
`search_memory(org="commerce", …)` · `scripts/memory/run.sh search -org commerce …` 가 두 팀을 함께 찾는다. 설정을 바꾸면 `scripts/memory/run.sh bootstrap`(또는 `make memory-daemon-stop && make memory-daemon-start`)이 이 workspace 팀들의 alias 를 맞춘다 (다른 meta-repo 의 alias 는 건드리지 않는다).

### 6.6 그래프 재구축 계약

그래프는 hot 문서(색인), 파일의 링크·frontmatter, 판정 원장, 증류 캐시만으로 만든다 — LLM 0회. 데몬은 문서·관계·entity 가 바뀐 사이클에만 workspace 그래프를
한 트랜잭션으로 통째로 바꾼다. Neo4j 를 지워도 `scripts/memory/run.sh graph rebuild` 한 번이면 같다. Neo4j 가 꺼져 있으면 그래프 단계만 건너뛰고(`graph: failed …`) 나머지는 돈다.

---

## 7. cold 층 — 스냅샷, cold store, S3

cold 는 두 가지를 같이 둔다 (발표의 "S3 층").

| | 무엇 | 기본 위치 | S3 로 |
| --- | --- | --- | --- |
| 스냅샷 | OpenSearch 인덱스 통째 — `restore_cold` 의 원천 | fs repository `mem-cold` = `./.cache/snapshots` | `COLD_SNAPSHOT_TYPE=s3`, `COLD_SNAPSHOT_S3_BUCKET` (+ OpenSearch 에 repository-s3 플러그인·keystore) |
| cold store | 문서 원본(`{team}/raw/{path}`)과 청크 JSONL(`{team}/chunks/{path}.jsonl`) — `reload`·`archive`·cold 소유 판단의 원천 | `./.cache/cold-store` | `COLD_STORE_S3_BUCKET`, `COLD_STORE_S3_PREFIX` → `s3://{bucket}/{prefix}/{workspace}/` |

S3 호환 저장소(LocalStack·MinIO)는 `AWS_ENDPOINT_URL_S3` + `COLD_STORE_S3_PATH_STYLE=true`. 자격 증명은 AWS 기본 체인.
OpenSearch 쪽 S3 설정 예는 `app/test/s3/` (repository-s3 플러그인을 넣은 이미지 + `s3.client.default.*` 설정) — `make -C app test-e2e-s3` 가 이 구성으로 tier·복원·재적재를 확인한다.

**cold 소유.** cold 가 그 내용을 갖고 있으면 hot 에 파일이 남아 있어도 데몬이 되살리지 않는다(`cold_owned`) — tier 직후 커밋 전이거나
ISM 이 먼저 옮긴 경우다. "갖고 있다" 의 판단:

- 그 달의 스냅샷이 모두 목록(`{team}/manifests/{index}.jsonl`, tier·archive 가 남긴다)으로 덮이면: 목록에 그 (경로, 내용 sha) 가 있을 때.
  tier·archive 의 스냅샷(이름 `cold-…`)은 문서가 없었어도 표시 줄을 남기고, 줄이 없어도(목록을 쓰기 전에 끊김) 목록이 덮는 것으로 본다
- 목록이 덮지 못하는 스냅샷(ISM 이 만든 `ism-cold-…`)이 하나라도 있으면: 그 달이 스냅샷에 있고 cold store 청크의 내용 sha 가 파일과 같을 때
  (ISM 의 스냅샷과 삭제 사이에 그 달로 쓴 내용은 놓칠 수 있다)

로컬 cold store 는 임시 파일에 다 쓰고 rename 으로 바꿔, 쓰다 끊겨도 정본이 잘리지 않는다. 목록 읽기-고치기-쓰기는 잠금 파일
(`{COLD_STORE_DIR}/.locks/`, S3 cold store 도 이 로컬 디렉토리)로 직렬화해 tier-tick 과 archive 가 겹쳐도 줄을 잃지 않는다 — 같은 머신의 프로세스 사이만 막는다.

내용을 고치면(sha 가 바뀌면) 새 문서로 보고 다시 색인한다. 그 달이 복원돼 열려 있으면 복원본에 쓰고, 다음 사이클부터는 그대로 둔다.
복원은 스냅샷이 쓰기 막힌 채 떠졌더라도 막기를 풀어 되살린다.

---

## 8. 설정 레퍼런스

`.env`(복사: `cp .env.example .env`) 또는 환경변수. 명령줄에서 준 값이 `.env` 보다 우선한다.
`make memory-daemon-start` 로 띄운 데몬(launchd)은 start 때의 설정 중 `META_REPO_ROOT`·`OPENSEARCH_URL`·`OS_PORT`·`NEO4J_BOLT_PORT`·`NEO4J_HTTP_PORT`·`COLD_*`·`MEMORY_*`·`AWS_REGION`·`AWS_PROFILE`·`AWS_ENDPOINT_URL_S3`(`.env` 와 명령줄 값. 이름에 `PASSWORD`·`SECRET`·`TOKEN` 이 든 것은 빼고 — 잡이 `.env`·AWS 기본 체인에서 다시 읽는다)를 잡에 담아 돈다 — 이 값을 바꾸면 `make memory-daemon-stop && make memory-daemon-start`. 데몬이 쓰지 않는 `OS_DASH_PORT` 는 담지 않는다.

| 변수 | 기본 | 뜻 |
| --- | --- | --- |
| `OS_PORT` · `NEO4J_BOLT_PORT` · `NEO4J_HTTP_PORT` · `OS_DASH_PORT` | `9202` · `7689` · `7476` · `5603` | 스택 호스트 포트 (docker compose 와 스크립트가 같이 쓴다) |
| `OPENSEARCH_URL` | `http://localhost:${OS_PORT}` | OpenSearch |
| `META_REPO_ROOT` | 이 레포 | 색인할 meta-repository 루트 |
| `MEMORY_WORKSPACE` | 루트 디렉토리 이름 | 같은 스택을 쓰는 meta-repo 끼리의 격리 축 (소문자·숫자·하이픈) |
| `MEMORY_DEFAULT_TEAM` | `default` | frontmatter 에 team 이 없을 때 |
| `MEMORY_TEAM_ORGS` | — | `팀=org,팀=org` — org alias |
| `MEMORY_HOT_DAYS` | `60` | hot/cold 경계. tier 는 바로 따른다. ISM 정책은 bootstrap 이 고치지만 **이미 정책이 붙은 인덱스는 처음 붙은 기간을 유지한다** (새로 만드는 인덱스부터) |
| `MEMORY_TIME_ZONE` | 이 머신의 시간대 | 문서의 날(`date`·`time_range` 날짜 필터, `search_filters` 날짜별 수)을 정하는 IANA 시간대 (예: `Asia/Seoul`). frontmatter `date` 는 적힌 날 그대로, 없으면 커밋·mtime 시각을 이 시간대에서 본 날. 바꾸면 `scripts/memory/run.sh ingest .` 로 이미 색인된 문서의 날을 다시 쓴다 (데몬은 내용이 바뀐 문서만 다시 쓴다) |
| `MEMORY_RECONCILE_INTERVAL_SECONDS` | `60` | 데몬 타이머 |
| `MEMORY_RECONCILE_DEBOUNCE_MS` | `3000` | watcher 힌트 debounce |
| `MEMORY_MAX_FILE_BYTES` | `2097152` | 이보다 큰 파일은 건너뛴다 (데몬·`ingest_path` 모두) |
| `MEMORY_SANITY_MAX_FILES` | `5000` | 넘으면 설정 오류로 보고 색인 거부 (`REFUSED`) |
| `MEMORY_JUDGE_ENABLED` | `true` | 판정 |
| `MEMORY_JUDGE_WINDOW_DAYS` | `7` | 이보다 차이 나면 latest-wins, 이내면 LLM |
| `MEMORY_JUDGE_MIN_SCORE` | `0.3` | 이웃 유사도(자기 자신 대비) 하한 |
| `MEMORY_DISTILL_ENABLED` | `false` | 증류 (본문이 LLM 으로 간다) |
| `MEMORY_DISTILL_CONCURRENCY` | `2` | 증류 동시 실행 |
| `MEMORY_CLAUDE_BIN` / `MEMORY_LLM_MODEL` / `MEMORY_LLM_TIMEOUT_MS` | `claude` / `opus` / `120000` | 판정·증류 LLM. 격리 실행(`--setting-sources ""`, 도구 없음, JSON 스키마), 실패하면 폴백 |
| `MEMORY_GRAPH_URI` | `bolt://localhost:${NEO4J_BOLT_PORT}` | Neo4j. `off` 면 그래프를 끈다 |
| `MEMORY_GRAPH_USER` / `MEMORY_GRAPH_PASSWORD` / `MEMORY_GRAPH_DATABASE` | — / — / `neo4j` | 인증을 켠 Neo4j 일 때만 |
| `COLD_SNAPSHOT_REPOSITORY` / `COLD_SNAPSHOT_TYPE` | `mem-cold` / `fs` | 스냅샷 repository |
| `COLD_SNAPSHOT_FS_LOCATION` | `/usr/share/opensearch/snapshots` | fs repository 경로 (컨테이너 안 — `path.repo`) |
| `COLD_SNAPSHOT_S3_BUCKET` | — | `COLD_SNAPSHOT_TYPE=s3` 일 때 필수 |
| `COLD_STORE_DIR` | `.cache/cold-store` | 로컬 cold store. S3 cold store 여도 목록 쓰기 잠금 파일(`.locks/`)은 여기 둔다 |
| `COLD_STORE_S3_BUCKET` / `COLD_STORE_S3_PREFIX` / `AWS_REGION` | — / `memory` / `ap-northeast-2` | 값이 있으면 S3 cold store |
| `AWS_ENDPOINT_URL_S3` / `COLD_STORE_S3_PATH_STYLE` | — / `false` | S3 호환 저장소 |

---

## 9. 트러블슈팅

| 증상 | 원인 | 조치 |
| --- | --- | --- |
| `make memory-init` 이 5분 기다리다 실패 | Docker 메모리 부족 | Docker 메모리 3GB 이상, `docker logs dj-workspace-opensearch` / `dj-workspace-neo4j` |
| 컨테이너 이름·포트 충돌 | 같은 이름의 옛 컨테이너, 다른 스택 | `docker ps -a --filter name=dj-workspace`, `.env` 의 `OS_PORT` 등 |
| 검색 0건 | 색인 안 됨 / 팀이 다름 / workspace 가 다름 | `scripts/memory-daemon/run.sh status` 의 docs 수, `scripts/memory/run.sh search -team <팀> <질의>`, `MEMORY_WORKSPACE` |
| 문서가 검색에 안 나온다 | 색인 범위 밖 (§1) — 범위 밖 파일은 로그에도 남지 않는다 | §1 의 범위 확인, `scripts/memory/run.sh ingest <path>` (범위 밖이면 "색인 대상 문서가 없습니다") |
| 같은 파일이 철자만 다른 두 문서로 검색된다 (`LIMITS.md` 와 `limits.md`, NFC·NFD 한글 경로) | 이전 판의 `ingest_path` 가 친 철자를 그대로 `source_path` 로 썼다 | 데몬 사이클(상주 데몬 또는 `scripts/memory-daemon/run.sh once`)이 디스크의 철자(NFC)로 다시 쓰고 옛 철자의 문서를 색인에서 뺀다 (`removed=`). 같은 파일이 디스크에 있으니 cold store 청크는 지우지 않는다 |
| `[reconcile] fail <path>` · status 의 `failed=` | 그 파일만 쓰다 실패 (다른 workspace 의 팀, 일시적인 오류) | 오류 문구대로 고친다. 일시적이면 다음 사이클이 다시 시도한다 |
| 구판·신판이 나란히 뜬다 | 판정 질문 대기 / knowledge 에 `supersedes` 누락 | `scripts/memory/run.sh questions` / 개정판에 `supersedes:` |
| 판정이 전부 질문으로 온다 | `claude` CLI 미설치·미로그인 | `claude` 로 로그인, `MEMORY_CLAUDE_BIN` |
| `다른 workspace 가 쓰는 팀입니다` | 다른 meta-repo 가 같은 팀 이름을 쓴다 | 이 meta-repo 는 다른 `MEMORY_DEFAULT_TEAM`/frontmatter team |
| `데몬이 이미 돌고 있습니다` | 데몬 중복 실행 | `scripts/memory-daemon/run.sh status` 로 pid 확인 |
| `cold_owned` 가 계속 찍힌다 | cold 로 간 원본이 hot 에 남아 있다 (ISM 이 옮긴 경우 등) | `scripts/memory/run.sh tier` 가 정리한다 |
| `graph: failed …` / 그래프 도구가 "꺼져 있다" | Neo4j 가 꺼져 있다 / `MEMORY_GRAPH_URI=off` | `make memory-init`, 그다음 `scripts/memory/run.sh graph rebuild` |
| `related_docs` 에 entity 경로가 없다 | 증류가 꺼져 있다 | `.env` 에 `MEMORY_DISTILL_ENABLED=true` 후 `make memory-daemon-stop && make memory-daemon-start` (본문이 LLM 으로 간다) |
| Claude 에 도구가 안 보인다 | `.mcp.json` 승인 전 | `claude mcp get drakejin-workspace-memory-mcp` → `claude` 실행해 승인, 세션에서 `/mcp` |
| `.cache` 를 지웠다 | 파생물·원장·**cold 정본** 삭제 | 인덱스·그래프는 `make memory-daemon-stop` → `make memory-init` → `make memory-daemon-start` 로 다시 만들고 (데몬이 지워진 락을 모른 채 돌지 않게 먼저 멈춘다), 판정은 다시 묻고 증류는 다시 한다. 하지만 `cold-store/`·`snapshots/` 에만 있던 원본(tier 가 git 에서 지운 것)은 git 히스토리에서 되살려야 한다 — 이 둘은 지우지 않는다 |

---

## 10. 테스트

| 명령 | 무엇 | 필요 |
| --- | --- | --- |
| `make -C app test` | 단위 테스트 (`go test -race ./...`) | 없음 |
| `make -C app test-e2e` | 블랙박스 e2e — MCP 도구 7개, 검색 필터(날짜·범위·주제·프로젝트), 데몬 수명주기(판정 3단·증류·재구축 계약·tombstone·cold 소유·단일 writer), 그래프, 감사, archive, org, 세션 훅, 운영 CLI 전 과정 + 커버리지 | 루트에서 `make memory-init` |
| `make -C app test-e2e-slow` | ISM 이 실제 시간으로 인덱스를 스냅샷·삭제하고 tier 가 hot 원본을 치우는 흐름 (6~15분) | 루트에서 `make memory-init` |
| `make -C app test-e2e-s3` | 스냅샷(repository-s3)과 cold store 를 S3(LocalStack)에 두고 tier·복원·재적재 | Docker (전용 스택을 올렸다 내린다) |

`make -C app` 은 현재 디렉토리를 바꾸지 않는다 — 워크스페이스 루트에서 그대로 이어 부른다.

e2e 는 고유 팀·workspace 로 돌고 끝나면 자기 인덱스·스냅샷·그래프만 지운다. slow 는 공용 ISM 정책을 건드리지 않고 테스트 전용 정책을 그 인덱스에만 붙이며,
ISM 주기를 잠깐 1분으로 줄였다가 되돌린다.

### 시나리오 테스트

자동 테스트와 별개로 [scenarios/SCENARIOS.md](./scenarios/SCENARIOS.md) 가 있다 — Claude 서브에이전트가 문서를 읽으며 실제 명령을 하나씩 실행해
PASS/FAIL 을 보고하는 시나리오(G1 데몬 수명주기 … G3 Claude Code 로 필터 검색 … G8 자동 테스트). 날짜는 실행하는 날 기준으로 만든다.
