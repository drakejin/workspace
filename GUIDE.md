# 메모리 층 가이드 — 설치부터 Claude 연결, 사용, 예제까지

이 워크스페이스의 문서(`docs/`, `main/{repo}/docs/`)를 OpenSearch 에 색인해 두고, Claude Code 가 파일 트리를 뒤지는 대신
`search_memory` 로 묻게 하는 **메모리 층**의 안내서다. 처음 켜는 사람이 위에서부터 따라 하면 되게 썼다.

## 목차

1. [무엇이 도는가](#1-무엇이-도는가)
2. [설치 — memory-up · daemon · daemon-status](#2-설치--memory-up--daemon--daemon-status)
3. [Claude 에 memory-mcp 연결하기](#3-claude-에-memory-mcp-연결하기)
4. [샘플 문서로 체험하기](#4-샘플-문서로-체험하기)
5. [내 문서로 예제 만들기](#5-내-문서로-예제-만들기)
6. [운영 — 주간 compaction, 상태 점검](#6-운영--주간-compaction-상태-점검)
7. [설정 레퍼런스](#7-설정-레퍼런스)
8. [트러블슈팅](#8-트러블슈팅)

---

## 1. 무엇이 도는가

```
 문서가 들어가는 길                                       에이전트가 묻는 길
 ───────────────────                                       ────────────────────
 docs/**, main/{repo}/docs/**, README·INDEX·CLAUDE          Claude Code
        │ 저장                                                  │ search_memory / list_cold / restore_cold / ingest_path
        ▼                                                       ▼
 reconcile 데몬 (bin/memory-daemon)                         memory-mcp (bin/memory-mcp, stdio)
   boot 1회 + 60초 타이머 + 파일 watcher(3초 debounce)           │ BM25 검색
   A. stat → sha 비교, 바뀐 것만 재색인 / 지운 건 색인에서 뺀다   ▼
   D. 판정: 비슷한 episodic 문서끼리 어느 쪽이 현행인가   ──▶  OpenSearch (localhost:9202)
   E. 증류: 요약·entity (선택)                                   mem-{team}-{yyyy.MM}   episodic, 60일 뒤 cold
        │                                                       mem-{team}-knowledge   knowledge, 노화 없음
        ▼                                                       mem-{team}-hot         읽기 alias
 judge-state/ · knowledge-cache/  (git 에 커밋하는 원장)         │ ISM / make tier: 스냅샷 → 삭제
                                                                ▼
                                                         cold: 스냅샷(.data/snapshots) + 원본·청크 JSONL(.data/cold-store 또는 S3)
```

원칙 셋:

1. **콘텐츠의 주인은 git(hot) 과 cold 보관소뿐이다.** 인덱스는 파생물이라 지워도 데몬이 같은 결과로 다시 만든다 (판정·증류는 원장에서 복원, LLM 재호출 0회).
2. **파생물의 writer 는 데몬 하나다.** 인덱스를 직접 고치지 않는다. 문서를 고치면 데몬이 따라 맞춘다.
3. **검색은 경로를 돌려주고, 본문은 원문을 읽는다.** 에이전트는 결과의 `source_path` 를 Read 한다.

문서는 두 성질로 나뉜다.

| 성질 | 어떤 문서 | 저장 | 판정 | 노화 |
| --- | --- | --- | --- | --- |
| episodic (시간이 지나면 낡는 기록) | `docs/` 의 spec·adr·prd·plan·note, `docs/issues/` | 월별 인덱스 | 비슷한 문서끼리 자동 판정 | 60일 뒤 cold |
| knowledge (누가 바꾸기 전까지 유효한 사실) | `docs/knowledge/`, 모든 `INDEX.md`·루트 안내 문서, `main/{repo}/docs/` | knowledge 인덱스 | 개정판의 `supersedes:` 선언만 | 없음 |

---

## 2. 설치 — memory-up · daemon · daemon-status

### 준비물

| 도구 | 확인 | 비고 |
| --- | --- | --- |
| Docker (OrbStack 등) | `docker info` | 메모리 2GB 이상 |
| Go 1.25 | `go version` | `mise.toml` 이 고정. mise 가 있으면 `memory-up` 이 설치까지 한다 |
| Claude Code CLI | `claude --version` | MCP 클라이언트. 판정·증류 LLM 으로도 쓴다 (API 키 없음) |

### 2.1 인프라 올리기 — `make memory-up`

```bash
cd ~/Workspace/drakejin/workspace
cp .env.example .env      # 선택. 기본값으로도 돈다
make memory-up
```

도구 확인 → 빌드(`bin/memory-mcp`, `bin/memory-daemon`) → OpenSearch 기동 → 헬스 대기 → 최초 색인까지 한 번에 한다. 다시 실행해도 안전하다.

```
[memory-up] docker: ok
[memory-up] tools: go1.25.6
[memory-up] build: bin/memory-mcp, bin/memory-daemon
[memory-up] stack: started
[memory-up] opensearch: waiting http://localhost:9202
[memory-up] opensearch: ready
[reconcile] scanned=10 indexed=10 stat_updated=0 removed=0 skipped=0 cold_owned=0 flags=0 trigger=boot took=315ms
[memory-up] reconcile: done

[memory-up] 다음: 'make daemon' 으로 데몬을 켜고, 'make daemon-status' 로 확인하세요
```

OpenSearch 는 `localhost:9202`, 컨테이너 이름은 `dj-workspace-opensearch` 다. 같은 머신의 다른 메모리 스택(9200, 9201)과 겹치지 않게 잡았다.

### 2.2 데몬 실행 — `make daemon`

```bash
make daemon      # 터미널 하나를 계속 점유한다. 끄려면 Ctrl-C
```

```
[daemon] LLM 백엔드: 로컬 claude CLI (2.1.283 (Claude Code))
[reconcile] scanned=10 indexed=0 stat_updated=0 removed=0 skipped=0 cold_owned=0 flags=0 trigger=boot took=9ms
[daemon] watching .../workspace (루트 문서, docs/, main/*/docs/), interval=1m0s, debounce=3s, judge=on, distill=off
```

이제 문서를 저장하면 3초 뒤 색인된다. 변화가 없으면 사이클 비용은 stat-walk 수 ms 다. 데몬이 꺼져 있던 동안의 변경은 다음 기동 때 흡수된다.
데몬은 하나만 돈다 — 두 번째 `make daemon` 은 `데몬이 이미 돌고 있습니다 (pid …)` 로 거부되고, `make reconcile` 은 건너뛴다.

### 2.3 데몬 상태 — `make daemon-status`

```
[status] container opensearch: running
[status] opensearch: yellow (http://localhost:9202)
[status] index: workspace=workspace 10 docs, 37 chunks (memory indices 2)
[status] last indexed: 2026-10-12T18:44:02.085Z
[status] daemon: running pid=21242 since 2026-10-12T18:19:24Z, cycles=12
[status] heartbeat: 6s ago (interval 60s)
[status] last cycle: ... trigger=watch scanned=10 indexed=1 stat_updated=0 removed=0 skipped=0 cold_owned=0 took=104ms
[status] judge: on, 답을 기다리는 질문 0건
[status] distill: off, cache 0건, 대기 0건
```

꺼진 게 있으면 마지막에 `[status] 할 일: …` 로 무엇을 실행하면 되는지 알려 준다. (단일 노드라 `yellow` 가 정상이다.)

---

## 3. Claude 에 memory-mcp 연결하기

MCP 서버는 `scripts/mcp/run.sh` 하나다 — 빌드하고 `bin/memory-mcp serve` 를 stdio 로 띄운다. 도구는 네 개다.

| 도구 | 하는 일 |
| --- | --- |
| `search_memory(team?, query, time_range?, repo?, doc_type?, size?, include_superseded?)` | hot 영역 BM25 검색. 결과는 `source_path › header_path` 와 청크 |
| `ingest_path(path, team?)` | 데몬이 아직 잡지 못한 파일·디렉토리를 바로 색인 |
| `list_cold(team?)` | 60일이 지나 cold 로 간 인덱스 목록 |
| `restore_cold(index, confirmed?)` | cold 인덱스 복원. `confirmed=true` 가 없으면 복원하지 않고 "사용자에게 먼저 물으라"고 돌려준다 |

### 3.1 이 워크스페이스에서 쓰기 (project scope — 이미 등록돼 있다)

레포 루트의 `.mcp.json` 이 `memory` 서버를 등록한다.

```bash
cd ~/Workspace/drakejin/workspace
claude mcp get memory
#   Scope: Project config (shared via .mcp.json)
#   Status: ⏸ Pending approval (run `claude` to approve)
claude                     # 처음 한 번 'memory' 서버 사용을 승인한다
```

승인 뒤 `claude mcp get memory` 가 `✔ Connected` 를 보이면 끝이다. 세션 안에서는 `/mcp` 로도 확인한다.
`CLAUDE.md` 가 "작업을 시작하면 `search_memory` 를 먼저 부른다"를 규칙으로 걸어 두었으니, 별도 지시 없이도 검색부터 한다.

### 3.2 다른 프로젝트에서도 이 메모리를 쓰기 (user scope)

```bash
claude mcp add memory -s user -- bash ~/Workspace/drakejin/workspace/scripts/mcp/run.sh
```

어느 디렉토리에서 `claude` 를 열어도 이 워크스페이스의 메모리를 검색한다 (`run.sh` 가 자기 레포를 루트로 잡는다).

### 3.3 다른 meta-repo(예: 샘플)를 가리키기

환경변수로 루트와 기본 팀을 넘긴다.

```bash
claude mcp add memory-demo -s local \
  -e META_REPO_ROOT=$HOME/memory-demo -e MEMORY_DEFAULT_TEAM=demo \
  -- bash ~/Workspace/drakejin/workspace/scripts/mcp/run.sh
```

여러 meta-repo 가 같은 OpenSearch 를 써도 된다. 문서는 `workspace`(기본: 루트 디렉토리 이름)로 격리되어 검색·삭제·판정이 섞이지 않는다.
단 **팀 이름은 meta-repo 하나에만** 쓸 수 있다 — 다른 meta-repo 가 이미 쓰는 팀으로 적재하면 `다른 workspace 가 쓰는 팀입니다` 로 거부된다.

---

## 4. 샘플 문서로 체험하기

`examples/sample-workspace/` 는 가상의 서비스(order-api, catalog-api)로 만든 작은 meta-repo 다. 문서마다 체험할 기능이 정해져 있다.

| 문서 | 체험하는 기능 |
| --- | --- |
| `docs/repository/order-api/adr/0001-idempotency-key.md` | 식별자 검색(`IdempotencyKeyHeader`), 에피소드 다섯 줄 |
| `docs/feature/payment-retry/2026-08-04-…` · `2026-10-06-…` | 판정 latest-wins — 구판이 기본 검색에서 숨는다 |
| `docs/knowledge/order-status.md` · `order-status-v2.md` | knowledge 개정은 `supersedes:` 선언으로만 밀린다 |
| `main/catalog-api/docs/architecture.md` | 관리 대상 repository 의 docs 도 색인된다 (`repo` 필터) |
| `docs/repository/order-api/prd/2026-06-order-launch.md` | 60일 지난 문서 → compaction 리포트 → cold → restore |
| `docs/specs/refund-window/plan.md` | AI plan 문서 |

`make tier` 는 cold 로 보낸 원본을 hot 에서 지우므로, **샘플은 복사해서 쓴다** (레포 안의 샘플을 지우지 않게).

```bash
cp -R ~/Workspace/drakejin/workspace/examples/sample-workspace ~/memory-demo
cd ~/Workspace/drakejin/workspace
export META_REPO_ROOT=$HOME/memory-demo MEMORY_DEFAULT_TEAM=demo   # 이 셸의 make 명령이 샘플을 가리킨다
```

### 4.1 색인과 판정

```bash
make reconcile
```

```
[judge] superseded_by docs/knowledge/order-status.md → docs/knowledge/order-status-v2.md
[reconcile] scanned=13 indexed=13 stat_updated=0 removed=0 skipped=0 cold_owned=0 flags=1 trigger=boot took=399ms
[judge] superseded docs/feature/payment-retry/2026-08-04-retry-policy.md ↔ docs/feature/payment-retry/2026-10-06-retry-policy.md (latest-wins: 유효 시각 차 63.0일 > 7일)
```

- knowledge `order-status-v2.md` 가 `supersedes:` 로 구판을 밀었다
- 결제 재시도 정책 두 문서는 내용이 비슷하고 유효 시각이 7일 넘게 차이 나서, LLM 없이 최신이 이겼다 (latest-wins)

### 4.2 검색

```bash
make query Q="PaymentRetryPolicy 재시도 횟수"
```

```
team=demo · 4건 (BM25)

[1] docs/feature/payment-retry/2026-10-06-retry-policy.md › 결제 재시도 정책 > 결정  (spec, 2026-10-06; score 5.97)
## 결정
`PaymentRetryPolicy` 는 결제 대행사 응답이 5xx 이거나 타임아웃이면 5회 재시도한다. 간격은 지수 백오프(1s, 2s, 4s, 8s, 16s)다.
…
```

밀린 구판까지 보려면 `bin/memory-mcp search -superseded PaymentRetryPolicy`:

```
[1] docs/feature/payment-retry/2026-10-06-retry-policy.md › 결제 재시도 정책 > 결정  (spec, 2026-10-06; score 5.34)
[2] docs/feature/payment-retry/2026-08-04-retry-policy.md › 결제 재시도 정책 > 결정  (spec, 2026-08-04; score 2.14)  [superseded → docs/feature/payment-retry/2026-10-06-retry-policy.md]
```

함수명·필드명이 그대로 맞는다 — `ClickDeduplicator.window` 같은 식별자는 원형과 조각(`click`, `deduplicator`, `window`) 모두로 색인된다.

```bash
make query Q=IdempotencyKeyHeader
bin/memory-mcp search -repo catalog-api CatalogItem      # main/catalog-api/docs 만
```

### 4.3 Claude 에서 물어보기

3.3 처럼 `memory-demo` 서버를 등록하고 샘플 디렉토리에서 연다.

```bash
cd ~/memory-demo && claude
```

예시 프롬프트와 기대 동작:

| 프롬프트 | Claude 가 하는 일 |
| --- | --- |
| "결제 재시도는 지금 몇 번 해? 근거 문서도 알려줘" | `search_memory("PaymentRetryPolicy …")` → 현행 문서(5회, 2026-10-06)를 Read 하고 경로를 근거로 답한다. 구판(3회)은 숨겨져 있다 |
| "order-api 에서 이중 결제를 어떻게 막기로 했지?" | ADR-0001 을 찾아 `IdempotencyKeyHeader` 결정과 버린 대안을 설명한다 |
| "주문 상태 값 목록이 뭐야?" | knowledge `order-status-v2.md` 의 네 가지 상태로 답한다 |
| "주문 API 1차 출시 때 활성 주문을 어떻게 정의했지?" | (4.4 의 tier 뒤라면) hot 에 없으니 `list_cold` → "cold 인덱스 mem-demo-2026.06 가 있는데 복원할까요?" 라고 **먼저 묻는다** |

비대화식으로 한 줄 확인하려면 (실제로 돌려 본 결과):

```bash
cd ~/memory-demo
claude -p "memory MCP 의 search_memory 로 'PaymentRetryPolicy' 를 검색하고, 현재 결제 재시도 횟수와 근거 문서 경로(source_path)만 한 줄로 답해." \
  --allowedTools "mcp__memory-demo__search_memory" < /dev/null
# → 결제 재시도 횟수는 5회, 근거 문서는 docs/feature/payment-retry/2026-10-06-retry-policy.md 입니다.
```

### 4.4 compaction 과 cold

```bash
make compaction-report TEAM=demo
```

```
## 1. 수명 확인 — 경과한 episodic 문서 2건
- `docs/repository/order-api/prd/2026-06-order-launch.md` (prd, order-api, 2026-06-15)
- `docs/feature/payment-retry/2026-08-04-retry-policy.md` (spec, 2026-08-04)

## 2. 충돌 후보 — 같은 repo·같은 주제를 다룬 문서 묶음 0개
```

knowledge(`order-status.md`, 2026-06)는 오래돼도 목록에 없다 — 노화하지 않는다.

```bash
make tier TEAM=demo DRY=1      # 미리보기
make tier TEAM=demo
```

```
tiered           mem-demo-2026.06  newest=2026-06-15T00:00:00.000Z
  - hot 에서 뺌: docs/repository/order-api/prd/2026-06-order-launch.md
tiered           mem-demo-2026.08  newest=2026-08-04T00:00:00.000Z
  - hot 에서 뺌: docs/feature/payment-retry/2026-08-04-retry-policy.md
kept-hot         mem-demo-2026.10  newest=2026-10-11T00:00:00.000Z
```

스냅샷을 뜬 뒤 hot 원본을 지우고(git 레포면 `git rm`) 인덱스를 지운다. 원본은 cold store raw 와 스냅샷에 남는다.
지우기 전에 cold store 에 같은 내용(sha)이 있는지 확인하므로, 그 사이에 고친 문서는 지우지 않는다.

```bash
bin/memory-mcp list-cold -team demo
bin/memory-mcp restore mem-demo-2026.06       # CLI 는 사람이 치는 것이라 바로 복원한다
make query Q=ActiveOrder TEAM=demo            # 복원된 PRD 가 다시 검색된다
```

복원본은 ISM·tier 대상에서 빠진다 (바로 다시 cold 로 가지 않는다). 데몬도 복원본을 건드리지 않는다.

### 4.5 판정이 사람에게 묻는 경우

유효 시각이 7일 이내인 비슷한 문서가 서로 다른 말을 하면, 데몬이 로컬 `claude` 로 판정한다. 확신이 낮거나 CLI 를 쓸 수 없으면 질문으로 남긴다.

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
make reconcile
make questions
make answer KEY=<pairKey> A=latest     # latest=최신이 사실 · old=구판이 사실 · keep=둘 다 유지
make reconcile                          # 데몬이 켜져 있으면 다음 사이클이 반영한다
```

같은 내용 쌍은 다시 묻지 않는다. 문서 본문이 바뀌면 다시 판정한다.

### 4.6 증류 켜기 (선택)

증류는 문서마다 한두 문장 요약과 entity 를 뽑아 검색 결과에 붙인다. **문서 본문이 LLM(로컬 claude)으로 간다** — 기밀 범위를 정한 뒤 켠다.

```bash
MEMORY_DISTILL_ENABLED=true make reconcile
make query Q=IdempotencyKeyHeader      # 결과에 "요약: …" 줄이 붙는다
```

결과는 `knowledge-cache/{team}.jsonl` 에 본문 sha 를 키로 쌓인다. 같은 본문은 다시 증류하지 않고, 인덱스를 지워도 LLM 없이 복원된다.

---

## 5. 내 문서로 예제 만들기

### 5.1 어디에 쓰나

| 무엇 | 위치 | 성질 |
| --- | --- | --- |
| 특정 repo 의 ADR·배경·용어 매핑 | `docs/repository/{repo}/` | episodic |
| 여러 repo 에 걸친 주제 | `docs/feature/{topic}/` | episodic |
| 스펙·작업 plan | `docs/specs/{spec}/` | episodic |
| 이슈 조사 | `docs/issues/{issue}/` | episodic |
| 살아남은 사실·용어 정의 | `docs/knowledge/` | knowledge |

문서를 추가하면 같은 디렉토리의 `INDEX.md` 에 한 줄을 더한다. 맨 위에는 frontmatter 를 둔다.

```yaml
---
team: default          # 생략하면 MEMORY_DEFAULT_TEAM
repo: books-guitar     # 여러 repo 에 걸치면 생략
doc_type: adr          # spec | adr | prd | plan | knowledge | guide | note (생략하면 경로로 추론)
date: 2026-10-13       # 유효 시각 — hot/cold 와 판정 순서가 이걸로 갈린다
---
```

### 5.2 예제 1 — Claude 에게 ADR 을 쓰게 하기

데몬을 켜 둔 상태에서 워크스페이스에서 `claude` 를 열고:

> books-guitar 에서 악보 렌더러를 VexFlow 로 고른 결정을 ADR 로 남겨 줘. 대안은 abcjs 와 직접 SVG 였어.
> `docs/repository/books-guitar/adr/0001-score-renderer.md` 에 frontmatter 를 붙여 쓰고, 문서 끝에 에피소드 다섯 줄을 남겨.
> 배경은 네가 커밋·대화에서 확인되는 것만 쓰고, 선택지·이유·그때의 생각은 비워 두고 나한테 물어봐. INDEX.md 도 갱신해.

저장되면 데몬 로그에 `indexed=1` 이 찍힌다. 새 세션에서 "악보 렌더러 왜 VexFlow 였지?" 라고 물으면 Claude 가 `search_memory` 로 이 ADR 을 찾아 답한다.

에피소드 다섯 줄 — 배경은 에이전트가, 가운데 세 줄은 사람이 머지 전에 쓴다. 결과는 석 달 뒤 채운다.

```markdown
## 에피소드
- 배경: (에이전트) 커밋·PR·대화에서 뽑은 무슨 일이 있었나. 출처 링크 포함
- 선택지와 결정: (사람) 어떤 안들이 있었고 무엇을 골랐나
- 이유: (사람) 왜 우리 환경에 맞다고 봤나, 어떤 전제 위에서
- 그때의 생각: (사람) 확신했나 찜찜했나, 어떤 원칙을 따랐나
- 결과: (석 달 뒤 채움)
```

### 5.3 예제 2 — knowledge 개정

사실이 바뀌면 구판을 고치지 않고 **새 문서**를 쓰고 `supersedes:` 로 구판을 가리킨다.

```yaml
---
doc_type: knowledge
date: 2026-11-01
supersedes: docs/knowledge/score-format.md
---
```

다음 사이클에 구판은 기본 검색에서 숨는다 (`include_superseded` 로는 보인다). knowledge 는 유사도로 판정하지 않으므로 선언이 없으면 구판이 계속 살아 있다.

### 5.4 예제 3 — 관리 대상 repository 의 문서

`main/{repo}/docs/` 에 있는 문서는 그 repo 의 것이라 이 레포에 커밋하지 않지만, 데몬이 색인한다 (knowledge, `repo` 필드 자동).
`make sync` 로 받은 변경도 다음 사이클에 반영된다. 작업은 `make run-issues` 로 만든 `main/{repo}/.worktrees/` 에서 하고, worktree 사본은 색인하지 않는다.

---

## 6. 운영 — 주간 compaction, 상태 점검

| 주기 | 할 일 | 명령 |
| --- | --- | --- |
| 매일 | 데몬이 켜져 있나 | `make daemon-status` |
| 질문이 쌓이면 | 판정 질문에 답하기 | `make questions` → `make answer KEY= A=` |
| 주 1회 | compaction — 60일 지난 사실을 knowledge 로 다시 쓰고 원문은 cold 로 | Claude 에서 `/compaction [team]` |
| 매핑을 바꿨을 때 | 청크 JSONL 에서 전체 재적재 | `bin/memory-mcp reload -team T` |

`/compaction` 은 `.claude/commands/compaction.md` 의 절차를 따른다: 리포트(수명 확인·충돌 후보) → 원문 대조 → 유효하지 않은 정의 분리 → 살아남은 사실을 오늘 날짜 knowledge 로 다시 쓰기 → `make tier`. **지우지 않고 다시 쓴다.**

cold 로 간 문서를 되살리려면 `restore_cold`(인덱스 단위) 하거나, 내용을 오늘 날짜로 다시 쓴다.

---

## 7. 설정 레퍼런스

`.env`(또는 환경변수). 명령줄에서 준 값이 `.env` 보다 우선한다.

| 변수 | 기본 | 뜻 |
| --- | --- | --- |
| `OPENSEARCH_URL` | `http://localhost:9202` | OpenSearch |
| `META_REPO_ROOT` | 이 레포 | 색인할 meta-repository 루트 |
| `MEMORY_WORKSPACE` | 루트 디렉토리 이름 | 같은 OpenSearch 를 쓰는 meta-repo 끼리의 격리 축 |
| `MEMORY_DEFAULT_TEAM` | `default` | frontmatter 에 team 이 없을 때 |
| `MEMORY_HOT_DAYS` | `60` | hot/cold 경계 (ISM 과 tier 가 같이 바뀐다) |
| `MEMORY_RECONCILE_INTERVAL_SECONDS` | `60` | 데몬 타이머 |
| `MEMORY_RECONCILE_DEBOUNCE_MS` | `3000` | watcher 힌트 debounce |
| `MEMORY_MAX_FILE_BYTES` | `2097152` | 이보다 큰 파일은 건너뛴다 |
| `MEMORY_SANITY_MAX_FILES` | `5000` | 넘으면 설정 오류로 보고 색인 거부 |
| `MEMORY_JUDGE_ENABLED` | `true` | 판정 게이트 |
| `MEMORY_JUDGE_WINDOW_DAYS` | `7` | 이보다 차이 나면 latest-wins, 이내면 LLM |
| `MEMORY_JUDGE_MIN_SCORE` | `0.3` | 이웃 유사도(자기 자신 대비) 하한 |
| `MEMORY_DISTILL_ENABLED` | `false` | 증류 (본문이 LLM 으로 간다) |
| `MEMORY_DISTILL_CONCURRENCY` | `2` | 증류 동시 실행 |
| `MEMORY_CLAUDE_BIN` / `MEMORY_LLM_MODEL` / `MEMORY_LLM_TIMEOUT_MS` | `claude` / `opus` / `120000` | 판정·증류 LLM |
| `COLD_SNAPSHOT_TYPE` | `fs` | `s3` 면 OpenSearch 에 repository-s3 플러그인과 keystore 자격 증명이 필요 |
| `COLD_SNAPSHOT_S3_BUCKET` | — | s3 스냅샷 버킷 |
| `COLD_STORE_DIR` | `.data/cold-store` | 원본·청크 JSONL 로컬 보관소 |
| `COLD_STORE_S3_BUCKET` / `COLD_STORE_S3_PREFIX` / `AWS_REGION` | — / `memory` / `ap-northeast-2` | 값이 있으면 S3 (`{prefix}/{workspace}/`), 자격 증명은 AWS 기본 체인 |

---

## 8. 트러블슈팅

| 증상 | 원인 | 조치 |
| --- | --- | --- |
| `make memory-up` 이 OpenSearch 를 5분 기다리다 실패 | Docker 메모리 부족 | Docker 메모리 2GB 이상, `docker logs dj-workspace-opensearch` |
| 컨테이너 이름 충돌 | 같은 이름의 옛 컨테이너 | `docker ps -a --filter name=dj-workspace` 로 확인 후 정리 |
| 검색 0건 | 색인 안 됨 / 팀이 다름 | `make daemon-status` 의 docs 수, `make query TEAM=…` |
| 문서가 검색에 안 나옴 | 색인 범위 밖 (`main/{repo}/src`, `.worktrees`, `.md` 아님, 2MB 초과) | 데몬 로그의 `skip` 사유 |
| 구판·신판이 나란히 뜬다 | 판정 질문 대기 / knowledge 에 `supersedes` 누락 | `make questions` / 개정판에 `supersedes:` |
| 판정이 전부 질문으로 온다 | `claude` CLI 미설치·미인증 | `claude` 로 로그인, `MEMORY_CLAUDE_BIN` |
| `다른 workspace 가 쓰는 팀입니다` | 다른 meta-repo 가 같은 팀 이름을 쓴다 | 이 meta-repo 는 다른 `MEMORY_DEFAULT_TEAM`/frontmatter team |
| `데몬이 이미 돌고 있습니다` | 데몬 중복 실행 | `make daemon-status` 로 pid 확인 |
| `cold_owned` 가 계속 찍힌다 | cold 로 간 원본이 hot 에 남아 있다 (ISM 이 옮긴 경우 등) | `make tier` 가 정리한다 |
| 오래된 문서를 다시 쓰고 싶다 | cold 소유 | 오늘 날짜로 다시 쓰거나 `restore_cold` |
| Claude 에 도구가 안 보인다 | `.mcp.json` 승인 전 | `claude mcp get memory` → `claude` 실행해 승인, 세션에서 `/mcp` |
