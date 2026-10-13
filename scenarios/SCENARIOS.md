# 시나리오 테스트 — drakejin-workspace 메모리 층

이 프로젝트의 기능이 처음부터 끝까지 실제로 동작하는지, **Claude 서브에이전트가 이 문서를 읽으면서 한 단계씩 실행해** 확인하는 시나리오다.
최종 목표는 G3: Claude Code 안에서 MCP 서버 `drakejin-workspace-memory-mcp` 로 문서를 가져오고, 검색 필터
— **날짜(`date`)·범위(`time_range`)·주제(`topic`)·프로젝트(`project`)** — 가 하나하나 맞게 동작하는 것.

## 0. 실행 규칙 (서브에이전트용)

1. 워크스페이스 루트(`~/Workspace/drakejin/workspace`)에서 실행한다. 맡은 그룹(G1~G8)만, 그 안의 단계는 **순서대로** 한다 — 앞 단계가 뒤 단계의 상태를 만든다.
2. 단계마다 `명령` 을 그대로 실행하고 `기대` 와 실제 출력을 비교해 **PASS / FAIL** 을 정한다. FAIL 이면 실제 출력의 핵심 줄을 증거로 남기고 다음 단계로 넘어간다 (멈추지 않는다).
   단, "선행 실패 시 중단" 이라고 적힌 단계가 실패하면 그 그룹의 남은 단계는 SKIP 으로 적는다.
3. 기대에 `D2` 처럼 적힌 날짜는 `.scenario.env` 의 값(`echo $D2`)이다 — 오늘(이 머신의 시간대)에서 2일 전. 경로 목록은 **순서까지** 맞아야 한다 (필터만 쓴 검색은 최신순).
4. **안전**: 같은 OpenSearch·Neo4j 에 사용자의 실제 메모리(workspace=`workspace`)가 있다. 시나리오는 자기 workspace·팀(`scn-<그룹>`)과
   임시 meta-repo 만 쓴다. 다음은 하지 않는다: 실제 워크스페이스에서 `make memory-daemon-start/stop`, `tier`, `audit -apply`, `archive`, `reload`,
   `curl -XDELETE` (teardown.sh 가 아닌 것), `docker compose down`. G6 은 워크스페이스 **사본**에서만 한다.
5. 매 셸마다 `source "$TMPDIR/drakejin-memory-scenario-<이름>/.scenario.env"` 를 다시 한다 (셸 상태는 이어지지 않는다).
   `$TMPDIR` 이 비어 있으면 `/tmp` 다. 셸이 zsh 일 수 있다 — `${PIPESTATUS[0]}` 대신 `; echo "exit=$?"` 로 종료 코드를 보고, 맨 앞이 `=` 인 낱말(`echo =====`)은 쓰지 않는다.
6. 그룹이 끝나면(실패해도) 반드시 `scenarios/teardown.sh <이름>` 을 한다.
7. 마지막에 §9 의 형식으로 결과를 보고한다.

준비물: Docker(OrbStack), Go(mise), jq, Claude Code CLI(`claude` 로그인). 스택이 없으면 먼저 `make memory-init` (실제 워크스페이스에서 해도 되는 유일한 메모리 명령 — 멱등).

## 1. 픽스처

`scenarios/fixture/` — `scenarios/setup.sh <이름>` 이 임시 디렉토리로 복사하고 git 레포로 만든다. `{{D:n}}` 은 오늘에서 n일 전.

| id | 경로 | 날짜 | doc_type | 성질 | project | topic | 기본 검색 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| A | `docs/repository/alpha-api/adr/0001-ledger-lock.md` | D2 | adr | episodic | alpha-api | payments | 보임 |
| B | `docs/repository/alpha-api/prd/2026-launch.md` | D10 | prd | episodic | alpha-api | onboarding | 보임 |
| C | `docs/repository/beta-web/adr/0001-ranking-cache.md` | D2 | adr | episodic | beta-web | search-ranking | 보임 |
| D | `docs/feature/payments/refund-flow.md` | D5 | spec | episodic | — | payments (경로) | 보임 |
| E | `docs/feature/search-ranking/bm25-tuning.md` | D20 | spec | episodic | beta-web (frontmatter project) | search-ranking (경로) | 보임 |
| F | `docs/specs/onboarding/plan.md` | D30 | plan | episodic | alpha-api (frontmatter repo) | onboarding (경로) | 보임 |
| G | `docs/issues/ISSUE-7/investigation.md` | D1 | note | episodic | beta-web | issue-7 (경로) | 보임 |
| H | `docs/knowledge/ledger-glossary.md` | D40 | knowledge | knowledge | alpha-api | payments | 보임 |
| I | `main/alpha-api/docs/architecture.md` | D15 | note | knowledge | alpha-api (경로) | architecture | 보임 |
| J | `docs/repository/alpha-api/adr/0000-legacy-queue.md` | D100 | adr | episodic | alpha-api | payments | 보임 (tier 전) |
| K | `docs/feature/payments/fee-policy-2026-q2.md` | D25 | spec | episodic | — | payments | **숨김** (판정: L 에 밀림) |
| L | `docs/feature/payments/fee-policy-2026-q3.md` | D3 | spec | episodic | — | payments | 보임 |
| M | `docs/knowledge/status-codes.md` | D50 | knowledge | knowledge | — | — | **숨김** (N 의 supersedes) |
| N | `docs/knowledge/status-codes-v2.md` | D4 | knowledge | knowledge | — | — | 보임 |
| R1 | `CLAUDE.md` | D0 (커밋 시각) | guide | knowledge | — | — | 보임 |
| R2 | `README.md` | D0 (커밋 시각) | guide | knowledge | — | — | 보임 |

문서마다 고유 식별자 토큰이 있다 (A=`ScnAlphaLedgerLock`, B=`ScnAlphaLaunch`, C=`ScnBetaRankingCache`, D=`ScnRefundFlow`, E=`ScnBm25Tuning`,
F=`ScnOnboardingPlan`, G=`ScnIssue7`, H=`ScnLedgerGlossary`, I=`ScnAlphaArchitecture`, J=`ScnLegacyQueue`, K·L=`ScnFeePolicy`, M·N=`ScnStatusCodes`).

공통 준비 (모든 그룹의 첫 단계):

```bash
scenarios/setup.sh <이름>
source "${TMPDIR:-/tmp}/drakejin-memory-scenario-<이름>/.scenario.env"
```

기대: `[scenario] <이름>: root=… workspace=scn-<이름> team=scn-<이름>`. (선행 실패 시 중단)

---

## G1. 설치와 데몬 수명주기 — 이름 `life`

| # | 명령 | 기대 |
| --- | --- | --- |
| L1 | 공통 준비 | 위와 같다 |
| L2 | `make memory-init` | `[memory-init] opensearch: ready`, `[memory-init] neo4j: ready`, `[reconcile] scanned=16 indexed=16 … failed=0 … flags=1`, `[memory-init] reconcile: done`, 마지막 줄에 `make memory-daemon-start` 안내. (선행 실패 시 중단) |
| L3 | `make memory-daemon-start` | `[memory-daemon] launchd: com.drakejin.memory.scn-life.daemon (workspace=scn-life, root=<시나리오 루트> …)` 다음 `[memory-daemon] started (pid N)`. (선행 실패 시 중단) |
| L4 | `make memory-daemon-start` (한 번 더) | `[memory-daemon] already running (pid N)` — L3 와 같은 pid |
| L5 | `scripts/memory-daemon/run.sh status` | 첫 줄 `[memory-daemon] launchd: loaded (com.drakejin.memory.scn-life.daemon)`, `[status] index: workspace=scn-life 16 docs` (밀린 문서 포함), `[status] daemon: running pid=N` |
| L6 | `launchctl list com.drakejin.memory.scn-life.daemon >/dev/null && echo loaded; launchctl list com.drakejin.memory.workspace.daemon >/dev/null 2>&1 && echo real-loaded \|\| echo real-not-loaded` | `loaded` — 시나리오 잡은 실제 워크스페이스 잡(`…workspace.daemon`)과 이름이 다르다 (실제 잡 상태는 기록만 하고 바꾸지 않는다) |
| L7 | 새 문서를 쓰고 15초 안에 검색: `printf -- '---\ndate: %s\n---\n# 워처 새 문서\nScnWatchNew 를 확인한다.\n' "$D0" > "$META_REPO_ROOT/docs/specs/watch-new.md"` 후 최대 15초 동안 `scripts/memory/run.sh search ScnWatchNew` 를 1초 간격으로 | `docs/specs/watch-new.md` 가 나온다 (데몬의 파일 watcher 가 색인) |
| L8 | 같은 파일 본문의 `ScnWatchNew` 를 `ScnWatchEdited` 로 바꾸고 15초 안에 `search ScnWatchEdited` | 나온다. `search ScnWatchNew` 는 이제 0건 ("맞는 문서가 없습니다") |
| L9 | `rm "$META_REPO_ROOT/docs/specs/watch-new.md"` 후 15초 안에 `search ScnWatchEdited` | 0건 (tombstone) |
| L10 | `scripts/memory-daemon/run.sh once` | `[reconcile] 데몬(pid N)이 돌고 있어 1회 색인을 건너뜁니다` |
| L11 | `kill -9 $(cat "$META_REPO_ROOT/.cache/daemon/daemon.pid")` 후 최대 60초 동안 2초 간격으로 `cat "$META_REPO_ROOT/.cache/daemon/daemon.pid"` 와 `ps -p <pid>` | 다른 pid 로 다시 떠 있다 (launchd KeepAlive) |
| L12 | `make memory-daemon-stop` | `[memory-daemon] stopped …` |
| L13 | `make memory-daemon-stop` (한 번 더) | `[memory-daemon] not running` |
| L14 | `launchctl list com.drakejin.memory.scn-life.daemon >/dev/null 2>&1 && echo loaded \|\| echo gone; ls ~/Library/LaunchAgents \| grep scn-life \|\| echo no-plist; ps -p <L11 의 새 pid> >/dev/null && echo alive \|\| echo no-process; ls "$META_REPO_ROOT/.cache/daemon/daemon.pid" 2>/dev/null \|\| echo no-pidfile` | `gone`, `no-plist`, `no-process`, `no-pidfile` (launchd 는 데몬을 `.cache/bin/memory-daemon` 상대 경로로 띄우므로 pid 로 확인한다) |
| L15 | `scenarios/teardown.sh life` | `[scenario] life: 정리됨 …` |

## G2. 검색 필터 — 운영 CLI — 이름 `filters`

MCP 의 `search_memory` 와 같은 핸들러를 운영 CLI 로 확인한다. `S=scripts/memory/run.sh`.

| # | 명령 | 기대 (경로, 순서대로) |
| --- | --- | --- |
| F1 | 공통 준비 후 `scripts/memory-daemon/run.sh once` | `[reconcile] scanned=16 indexed=16`, 판정은 정확히 둘: `[judge] superseded_by docs/knowledge/status-codes.md → docs/knowledge/status-codes-v2.md` (knowledge 선언), `[judge] superseded docs/feature/payments/fee-policy-2026-q2.md ↔ …fee-policy-2026-q3.md (latest-wins: … 22.0일 > 7일)` 와 그 판정을 반영하는 `[judge] superseded_by docs/feature/payments/fee-policy-2026-q2.md → …fee-policy-2026-q3.md`. `[judge] … latestWins=1 llm=0 questions=0`, `[audit] 주간 감사: 이상 없음`. 이 셋 말고 다른 문서의 superseded 줄이 있으면 FAIL. (선행 실패 시 중단) |
| F2 | `$S search -date $D2` | 머리 `date=<D2> · 문서 2건 (필터, 최신순)` → A, C |
| F3 | `$S search -date $D1` | G |
| F4 | `$S search -from $D5 -to $D2` | A, C, L, N, D (양 끝 날 포함) |
| F5 | `$S search -from $D10 -to $D1` | G, A, C, L, N, D, B |
| F6 | `$S search -topic payments` | A, L, D, H, J (K 는 숨김) |
| F7 | `$S search -topic payments -superseded` | A, L, D, K, H, J — K 줄 끝에 `[superseded → docs/feature/payments/fee-policy-2026-q3.md]` |
| F8 | `$S search -topic search-ranking` | C, E |
| F9 | `$S search -topic onboarding` | B, F |
| F10 | `$S search -topic "ISSUE 7"` | G — 주제는 대소문자·공백을 맞춰 비교한다 (`issue-7`) |
| F11 | `$S search -project alpha-api` | A, B, I, F, H, J |
| F12 | `$S search -project beta-web` | G, C, E |
| F13 | `$S search -project alpha-api -topic payments` | A, H, J |
| F14 | `$S search -project alpha-api -topic payments -from $D30` | A |
| F15 | `$S search -type adr` | A, C, J |
| F16 | `$S search -type knowledge` | N, H (M 은 숨김) |
| F17 | `$S search ScnFeePolicy` | L 하나 (`… 1건 (BM25)`, score 표시) |
| F18 | `$S search -superseded ScnFeePolicy` | L, K (K 에 superseded 표시) |
| F19 | `$S search -project beta-web ScnRefundFlow` | `맞는 문서가 없습니다` (D 는 프로젝트가 없다) |
| F20 | `$S search -date 2026-1-2 x; echo "exit=$?"` | 에러 `date 는 YYYY-MM-DD 여야 합니다`, exit≠0 |
| F21 | `$S search; echo "exit=$?"` | 사용법 에러(exit 2) — 질의어도 필터도 없음 |
| F22 | `$S filters` | `문서 14건 (밀린·deprecate 된 문서 제외)`, `project: alpha-api(6) beta-web(3) (없음)(5)`, `topic: payments(5) onboarding(2) search-ranking(2) architecture(1) issue-7(1) (없음)(3)`, `doc_type: adr(3) spec(3) guide(2) knowledge(2) note(2) plan(1) prd(1)`, `date: <D100> ~ <D0>` |
| F23 | `$S filters -project beta-web` | `project=beta-web · 문서 3건`, `topic: search-ranking(2) issue-7(1)` |
| F24 | `$S search -project alpha-api -size 2` | 머리 `project=alpha-api · 문서 6건 중 최신 2건 (필터, 최신순 — …)` → A, B (잘린 가져오기는 전체 수를 알린다) |
| F25 | `$S search -date $D0` | `CLAUDE.md`, `README.md` (frontmatter 날짜가 없는 문서는 커밋 시각을 이 머신의 시간대에서 본 날, 각 줄에 `<D0>`) |
| F26 | `MEMORY_TIME_ZONE=Mars/Base $S filters; echo "exit=$?"` | 에러 `MEMORY_TIME_ZONE: IANA 시간대 이름이어야 합니다`, exit≠0 |
| F27 | `$S search -topic '!!!'; echo "exit=$?"` | 에러 `topic 에 글자·숫자가 없습니다: "!!!"`, exit≠0 — 필터를 조용히 빼고 전체 문서를 돌려주지 않는다 |
| F28 | `python3 -c 'import os,unicodedata as u; d=os.path.join(os.environ["META_REPO_ROOT"],"docs/feature",u.normalize("NFD","환불정책")); os.makedirs(d); open(os.path.join(d,u.normalize("NFD","메모.md")),"w").write("# 환불 메모\nScnNfdTopic\n")' && scripts/memory-daemon/run.sh once` | Finder 처럼 NFD 이름의 디렉토리·파일을 만든다. `[reconcile] scanned=17 indexed=1` |
| F29 | `$S search -topic 환불정책` | `docs/feature/환불정책/메모.md` 하나 — NFD 디렉토리의 주제도 친(NFC) 주제로 맞고, 경로도 NFC 다 |
| F30 | `$S ingest docs/knowledge/STATUS-CODES-V2.md && $S ingest docs/feature/환불정책/메모.md` | `1개 문서 적재 - docs/knowledge/status-codes-v2.md → …` (디스크의 철자), `1개 문서 적재 - docs/feature/환불정책/메모.md → …` |
| F31 | `$S search ScnStatusCodes; $S search ScnNfdTopic` | 각각 N 하나, `docs/feature/환불정책/메모.md` 하나 — 철자만 다른 ingest 가 문서를 하나 더 만들지 않는다 |
| F32 | `scripts/memory-daemon/run.sh once && ls "$META_REPO_ROOT/.cache/cold-store/scn-filters/chunks/docs/knowledge/"` | `removed=0`, 목록에 `status-codes-v2.md.jsonl` 이 있다 (진짜 문서의 cold store 청크가 지워지지 않는다) |
| F33 | `printf '\x00\x01BinaryProbe' > "$META_REPO_ROOT/docs/knowledge/probe-bin.md" && $S ingest docs/knowledge/probe-bin.md` | `0개 문서 적재`, `1개 건너뜀 — 데몬과 같은 게이트: …`, `- docs/knowledge/probe-bin.md (binary content)` |
| F34 | `scenarios/teardown.sh filters` | 정리됨 |

## G3. Claude Code + drakejin-workspace-memory-mcp — 이름 `claude` (최종 목표)

`scenarios/claude-mcp.sh "<프롬프트>"` 는 Claude Code(`claude -p`, 기본 haiku)에 시나리오 meta-repo 의 MCP 서버
`drakejin-workspace-memory-mcp` 만 붙여 프롬프트를 시키고 `== tool_use <도구> <입력 JSON>`, `== tool_result`, `== answer`, `== stats` 를 출력한다.
판정은 **도구 입력(Claude 가 고른 필터)과 도구 결과(서버가 돌려준 문서)** 로 한다. 답의 말투는 보지 않는다.
haiku 가 지시를 어기면(엉뚱한 도구·필터) `CLAUDE_MODEL=sonnet` 으로 한 번 더 해 보고, 두 결과를 함께 적는다.

| # | 프롬프트 (그대로) | 기대 |
| --- | --- | --- |
| C1 | 공통 준비 후 `scripts/memory-daemon/run.sh once` | G2 F1 과 같다 (선행 실패 시 중단) |
| C2 | `search_filters 를 인자 없이 한 번 호출하고 결과를 그대로 보여 줘.` | `== mcp drakejin-workspace-memory-mcp:connected`, `== tools` 에 7개(docs_by_entity, ingest_path, list_cold, related_docs, restore_cold, search_filters, search_memory). `tool_use search_filters {}`, 결과가 G2 F22 와 같다 |
| C3 | `search_memory 를 query 없이 date="<D2 값>" 하나만 주고 정확히 한 번 호출해. 결과의 source_path 만 한 줄에 하나씩 적어.` (`<D2 값>` 은 `echo $D2` 로 바꿔 넣는다) | `tool_use search_memory {"date":"<D2>"}` (다른 키 없음), 결과 A, C |
| C4 | `search_memory 를 query 없이 time_range={"from":"<D5>","to":"<D2>"} 만 주고 정확히 한 번 호출해. source_path 만 적어.` | 입력에 time_range from/to, 결과 A, C, L, N, D |
| C5 | `search_memory 를 query 없이 topic="payments" 만 주고 정확히 한 번 호출해. source_path 만 적어.` | 결과 A, L, D, H, J |
| C6 | `search_memory 를 query 없이 project="beta-web" 만 주고 정확히 한 번 호출해. source_path 만 적어.` | 결과 G, C, E |
| C7 | `search_memory 를 query 없이 project="alpha-api", topic="payments", time_range={"from":"<D30>"} 로 정확히 한 번 호출해. source_path 만 적어.` | 결과 A 하나 |
| C8 | `search_memory 를 query="ScnFeePolicy", include_superseded=true 로 정확히 한 번 호출해. 각 결과의 source_path 와 superseded 표시가 있는지 적어.` | 결과 L, K. K 에 `[superseded → …q3.md]` |
| C9 (자연어) | `beta-web 프로젝트에서 최근 3일 안에 쓴 문서만 가져와 줘. 경로만.` | Claude 가 `project="beta-web"` 과 3일 이내를 뜻하는 날짜 조건(`time_range.from` ≈ `<D3>` 또는 `now-3d`, 또는 날짜별 `date`)을 골랐다. 최종 결과 문서가 G, C 뿐 (E 는 D20 이라 빠진다) |
| C10 (자연어) | `결제(payments) 주제 문서를 모두 가져와 줘. 밀린 판은 빼고. 경로만.` | `topic="payments"` 를 썼다. 결과 A, L, D, H, J (K 없음) |
| C11 (자연어) | `alpha-api 프로젝트의 온보딩 관련 문서가 뭐가 있어? 경로만.` | `project="alpha-api"` 와 `topic="onboarding"`(또는 그에 맞는 질의어)을 썼다. 결과에 B, F 가 있고 다른 프로젝트 문서는 없다 |
| C12 (자연어) | `<D2 값> 하루 동안 쓴 문서 목록을 줘.` | `date="<D2>"` (또는 그 하루만 덮는 time_range). 결과 A, C |
| C13 (자연어, 질의어) | `결제 수수료는 지금 몇 퍼센트야? 근거 문서 경로도.` | `search_memory` 를 불렀고(질의어 사용), 답이 3% 와 L 의 경로. K(2%)를 현행이라 하지 않는다 |
| C14 (자연어) | `어떤 프로젝트와 주제가 있고 각각 문서가 몇 개야?` | `search_filters` 를 불렀다. 답에 alpha-api 6, beta-web 3, payments 5 같은 수가 결과와 일치 |
| C15 | `scenarios/teardown.sh claude` | 정리됨 |

## G4. 판정·knowledge·그래프·세션 훅·질문 — 이름 `judge`

| # | 명령 | 기대 |
| --- | --- | --- |
| J1 | 공통 준비 후 `scripts/memory-daemon/run.sh once` | G2 F1 과 같다 (선행 실패 시 중단) |
| J2 | `scripts/memory/run.sh questions` | `답을 기다리는 질문이 없습니다` |
| J3 | `scripts/memory/run.sh search -superseded ScnStatusCodes` | N 과 M, M 에 `[superseded → docs/knowledge/status-codes-v2.md]` |
| J4 | `scripts/memory/run.sh graph stats` | `workspace=scn-judge`, `Doc=` 16 이상, `edges:` 에 `SUPERSEDES=2`, `DERIVED_FROM=1`, `LINKS_TO=` 1 이상 |
| J5 | `scripts/memory/run.sh graph neighbors -depth 1 docs/knowledge/status-codes-v2.md` | `docs/knowledge/status-codes.md (superseded) — SUPERSEDES` |
| J6 | `scripts/memory/run.sh graph neighbors -depth 1 docs/knowledge/ledger-glossary.md` | `docs/repository/alpha-api/adr/0001-ledger-lock.md (hot) — DERIVED_FROM(원장 잠금 결정에서 용어를 정리했다)`, `main/alpha-api/docs/architecture.md (hot) — LINKS_TO` |
| J7 | `scripts/memory/run.sh graph rebuild` 후 `graph stats` | `rebuild: docs=… (LLM 0회 …)`, stats 가 J4 와 같다 |
| J8 | `mkdir -p "$META_REPO_ROOT/main/alpha-api/src" && echo "{\"cwd\":\"$META_REPO_ROOT/main/alpha-api/src\",\"source\":\"startup\"}" \| scripts/memory/run.sh session-context` | `## memory — 이 세션과 관련된 문서 (자동 검색: "alpha-api")` 아래에 alpha-api 문서 경로들 (main/alpha-api/docs/architecture.md 포함) |
| J9 | `echo '{"cwd":"/tmp","source":"startup"}' \| scripts/memory/run.sh session-context` | 출력 없음 (meta-repo 밖) |
| J10 | 7일 안의 모순 쌍 — LLM 없이: 아래 **배송비 쌍** 블록을 실행한 뒤 `MEMORY_CLAUDE_BIN=/nonexistent scripts/memory-daemon/run.sh once` | `[judge] question …shipping-fee-a.md ↔ …shipping-fee-b.md` (LLM 을 못 써서 사람에게 넘김), `questions=1` |
| J11 | `scripts/memory/run.sh questions` | 질문 1건, `old: docs/feature/shipping-fee-a.md`, `new: docs/feature/shipping-fee-b.md`, pairKey 표시 |
| J12 | `scripts/memory/run.sh answer <J11 의 pairKey> latest` 후 `scripts/memory-daemon/run.sh once` | `사람: 최신이 사실`, 다음 once 에서 `superseded_by docs/feature/shipping-fee-a.md → docs/feature/shipping-fee-b.md`. `search ShippingFeePolicy` 는 b 하나 |
| J13 | `scripts/memory/run.sh audit` | `[audit] 이상 없음` (또는 `stale-question` 없이 발견 0건) |
| J14 | `scenarios/teardown.sh judge` | 정리됨 |

**배송비 쌍** (J10·M1 이 쓴다 — 같은 프로젝트(없음)·같은 종류(note)·7일 이내, 금액만 다르다):

```bash
printf -- '---\ndate: %s\n---\n# 배송비 정책\n\n## 결정\n\nShippingFeePolicy 는 주문 금액과 상관없이 배송비 3000원을 받는다. 제주와 도서 지역도 같은 금액이다.\n' "$D3" > "$META_REPO_ROOT/docs/feature/shipping-fee-a.md"
printf -- '---\ndate: %s\n---\n# 배송비 정책\n\n## 결정\n\nShippingFeePolicy 는 주문 금액과 상관없이 배송비 5000원을 받는다. 제주와 도서 지역도 같은 금액이다.\n' "$D1" > "$META_REPO_ROOT/docs/feature/shipping-fee-b.md"
```

(셸이 zsh 여도 되게 반복문 없이 두 줄로 쓴다.)

## G5. cold 층 — compaction·tier·복원·archive·감사·재적재 — 이름 `cold`

| # | 명령 | 기대 |
| --- | --- | --- |
| K1 | 공통 준비 후 `scripts/memory-daemon/run.sh once` | G2 F1 과 같다 (선행 실패 시 중단) |
| K2 | `scripts/memory/run.sh compaction-report -team scn-cold` | `1. 수명 확인 — 경과한 episodic 문서 1건` 에 J. K(D25)·knowledge 는 없다 |
| K3 | `scripts/memory/run.sh tier -team scn-cold -dry-run` | `would-tier mem-scn-cold-<J 의 연.월>`, 나머지는 `kept-hot`. 파일은 그대로 |
| K4 | `scripts/memory/run.sh tier -team scn-cold` | `tiered mem-scn-cold-<J 의 연.월>` 과 `- hot 에서 뺌: docs/repository/alpha-api/adr/0000-legacy-queue.md`. 파일이 사라지고 `git -C "$META_REPO_ROOT" status --short` 에 `D  docs/repository/alpha-api/adr/0000-legacy-queue.md` |
| K5 | `scripts/memory/run.sh search ScnLegacyQueue` | 0건 (cold 는 검색되지 않는다) |
| K6 | `scripts/memory/run.sh list-cold` | `cold 인덱스 1개`, K4 의 인덱스와 스냅샷 이름 |
| K7 | Claude 로: `source …; scenarios/claude-mcp.sh "레거시 야간 배치 큐(NightlySettleQueue) 결정 문서를 찾아 줘. hot 에 없으면 cold 를 확인하되, 복원은 하지 말고 무엇을 할지만 말해."` | `search_memory` 가 0건 → `list_cold` 호출 → 답에 cold 인덱스 이름과 "복원할까요" 류의 확인 질문. `restore_cold` 를 `confirmed=true` 로 부르지 않았다 |
| K8 | Claude 로: `"restore_cold 를 index=\"<K6 의 인덱스>\" 로 confirmed 없이 한 번만 호출하고 결과를 그대로 보여 줘."` | tool_result 에 `복원하지 않았습니다 … 복원할까요` — 확인 없이는 복원하지 않는다 |
| K9 | `scripts/memory/run.sh restore <K6 의 인덱스>` 후 `search ScnLegacyQueue` | `복원했습니다`, 검색에 J |
| K10 | `scripts/memory/run.sh archive deprecate -team scn-cold -by scenario -reason test docs/repository/alpha-api/adr/0000-legacy-queue.md` 후 `search ScnLegacyQueue`, `search -superseded ScnLegacyQueue` | `deprecated`, 기본 검색 0건, `-superseded` 로는 J 와 줄 끝 `[deprecated]` |
| K11 | `scripts/memory/run.sh archive deprecate -team scn-cold docs/repository/alpha-api/adr/0000-legacy-queue.md; echo "exit=$?"` | `-by 와 -reason 이 필요합니다`, exit≠0 |
| K12 | `archive undeprecate -team scn-cold -by scenario -reason test <J 경로>` 후 `search ScnLegacyQueue` | `deprecated 해제`, J 가 다시 보인다 |
| K13 | `git -C "$META_REPO_ROOT" show HEAD:docs/repository/alpha-api/adr/0000-legacy-queue.md \| sed 's/NightlySettleQueue/NightlySettleQueueV2/' > "${TMPDIR:-/tmp}/scn-edit.md"` 후 `scripts/memory/run.sh archive edit -team scn-cold -by scenario -reason test -file "${TMPDIR:-/tmp}/scn-edit.md" docs/repository/alpha-api/adr/0000-legacy-queue.md` | `edited — … 스냅샷 1개를 다시 썼습니다`, `search NightlySettleQueueV2` 에 J |
| K14 | `archive purge -team scn-cold -by scenario -reason test <J 경로>` 후 `search -superseded ScnLegacyQueue` | `purged`, 0건. `list-cold` 는 그 인덱스를 여전히 보여도 된다 (빈 스냅샷) |
| K15 | `scripts/memory/run.sh audit` | 고칠 것(orphan-index·unindexed·stale-content·orphan-chunks) 0건 |
| K16 | 인덱스를 지우고 cold store 에서 재적재: `curl -s -XDELETE "localhost:9202/mem-scn-cold-*" && scripts/memory/run.sh reload -team scn-cold` 후 `search ScnRefundFlow` | `N chunks 재적재`, D 가 다시 검색된다 (시나리오 팀 인덱스만 지운다) |
| K17 | `scenarios/teardown.sh cold` | 정리됨 |

## G6. 워크스페이스 명령 — 이름 `workspace` (사본에서)

이 그룹은 공통 준비(setup.sh)를 하지 않고, 정리도 teardown.sh 대신 W12 다. 실제 워크스페이스를 바꾸지 않게 사본을 만든다:
`W="${TMPDIR:-/tmp}/drakejin-workspace-copy" && rm -rf "$W" && rsync -a --exclude .cache --exclude main ~/Workspace/drakejin/workspace/ "$W/"` — 이후 명령은 `cd "$W"` 에서.

| # | 명령 | 기대 |
| --- | --- | --- |
| W1 | `grep -E '^[a-z][a-z0-9-]*:' Makefile \| cut -d: -f1` | 정확히 `init sync memory-init memory-daemon-start memory-daemon-stop` |
| W2 | `make -n init sync memory-init memory-daemon-start memory-daemon-stop` | 각각 `bash scripts/{init,sync,memory-init}/run.sh`, `bash scripts/memory-daemon/run.sh start`, `… stop` |
| W3 | `grep -E '^[a-z][a-z0-9-]*:' app/Makefile \| cut -d: -f1` | `build test test-e2e test-e2e-slow test-e2e-s3 mcp` |
| W4 | `make init` | `[hook] pre-commit: installed`, books-guitar clone (`[clone] books-guitar: …` 또는 이미 있으면 `[skip]`), `[init] summary: 1 repos / … / 0 failed`, 이어서 sync 출력. (GitHub 인증 실패면 FAIL 로 적고 W5~W7 은 SKIP) |
| W5 | `make sync` | `[index] INDEX.md: all repos listed` 와 pull 결과 |
| W6 | `scripts/run-issues/run.sh SCN-TEST books-guitar` 후 `git -C main/books-guitar worktree list` | `worktree 'SCN-TEST' created`, 목록에 `main/books-guitar/.worktrees/SCN-TEST` |
| W7 | `scripts/done-issue/run.sh SCN-TEST books-guitar` 후 `git -C main/books-guitar worktree list` 와 `git -C main/books-guitar branch -D SCN-TEST` | `worktree 'SCN-TEST' removed`, 목록에서 사라짐 |
| W8 | `mkdir -p docs/specs/scn-x && echo x > docs/specs/scn-x/a.md && scripts/docs-cleaner/run.sh < /dev/null; echo "exit=$?"; ls docs/specs` | `[docs-cleaner] 취소`, exit=1, `scn-x` 가 남아 있다 |
| W9 | `CONFIRM=yes scripts/docs-cleaner/run.sh; ls docs/specs` | `[clean] docs/specs/scn-x`, `docs/specs` 에 INDEX.md 만 |
| W10 | `scripts/schedule/run.sh install --dry-run \| grep -E 'Label\|tier-tick'` | `com.drakejin.workspace.tier` 와 `tier-tick` — 설치는 하지 않는다 |
| W11 | `scripts/memory/run.sh help` | 명령 목록에 `search … [-project P] [-topic T] [-date YYYY-MM-DD] [-from D] [-to D]`, `filters`, `tier`, `archive`, `graph` 등 |
| W12 | `rm -rf "$W"` | 사본 삭제 (실제 워크스페이스의 `main/` 은 건드리지 않았다) |

## G7. LLM 판정·증류·entity — 이름 `llm` (claude CLI 필요, 비용 발생)

| # | 명령 | 기대 |
| --- | --- | --- |
| M1 | 공통 준비 후 `export MEMORY_DISTILL_ENABLED=true MEMORY_LLM_MODEL=haiku` 그리고 G4 의 **배송비 쌍** 블록을 실행한 뒤 `scripts/memory-daemon/run.sh once` | 1~4분. `[judge] superseded …shipping-fee-a.md ↔ …shipping-fee-b.md (llm: …)` (LLM 이 모순·신판으로 판정) 또는 확신이 낮으면 `question` — 둘 중 하나면 PASS, 어느 쪽인지 적는다. 배송비 쌍과 F1 의 두 판정 말고 다른 문서의 superseded 줄이 있으면 FAIL. `[distill] …: summary N자, entities M` 줄이 문서 수만큼, 끝에 `trigger=settle` 사이클과 `[graph] … entities=` 1 이상 |
| M2 | `scripts/memory/run.sh search ScnAlphaLedgerLock` | 결과에 `요약:` 줄 |
| M3 | `scripts/memory/run.sh graph stats` | `Entity=` 1 이상, `MENTIONS=` 1 이상 |
| M4 | `scripts/memory/run.sh graph entity alpha-api` | 한 줄 이상 `<type>:<name> — <경로들>` |
| M5 | Claude 로: `scenarios/claude-mcp.sh "docs_by_entity 로 alpha-api 를 찾아 결과를 그대로 보여 줘."` | `tool_use docs_by_entity`, 결과가 M4 와 같은 내용 |
| M6 | `scenarios/teardown.sh llm` | 정리됨 |

## G8. 자동 테스트 — 이름 없음

| # | 명령 | 기대 |
| --- | --- | --- |
| T1 | `make -C app test` | 모든 패키지 `ok` |
| T2 | `make -C app test-e2e` | `ok … test/e2e`, 마지막 줄 `total: (statements) NN.N%` 이 80% 이상 |
| T3 (선택) | `make -C app test-e2e-s3` | `--- PASS: TestS3ColdLayer` (LocalStack 이미지 내려받기에 몇 분) |
| T4 (선택, 6~15분) | `make -C app test-e2e-slow` | `--- PASS: TestISMMovesIndexToColdAndTierPrunesHot` |

## 9. 결과 보고 형식

```
## G<n> <이름> — PASS x / FAIL y / SKIP z
| # | 결과 | 증거 (실제 출력 핵심 줄) |
| L1 | PASS | [scenario] life: root=… |
| L7 | FAIL | 15초 안에 안 나옴 — search 출력: "… 맞는 문서가 없습니다" |
...
teardown: 했음 / 못 함 (이유)
```

FAIL 은 재현 명령과 실제 출력(필요한 만큼)을 함께 적는다. 추측으로 PASS 를 적지 않는다.
