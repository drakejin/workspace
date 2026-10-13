# drakejin/workspace

drakejin 개인 repository 들을 한 곳에서 다루는 **meta-repository** 다. 관리 대상 repository 는 `main/{repo}` 에 clone 되고,
그 repository 들의 메타 정보(spec, ADR, PRD, AI plan)는 `docs/` 에 쌓는다. 코딩 에이전트(Claude Code)는 파일 트리를 뒤지는 대신
**메모리 층**에 묻는다 — OpenSearch(BM25 검색) + Neo4j(knowledge graph) + reconcile 데몬 + memory-mcp.

| 문서 | 내용 |
| --- | --- |
| 이 README | 설치 → Claude 연결 → 활용 → 저장된 데이터 확인 (OpenSearch·Neo4j) → 명령어·구조 |
| [GUIDE.md](./GUIDE.md) | 메모리 층 안내서 — 원리, 샘플로 따라 하기, 판정·증류·compaction·cold·그래프 운영, 설정·트러블슈팅 |
| [CLAUDE.md](./CLAUDE.md) | 에이전트 작업 규칙 (search_memory 선행, 문서 위치, frontmatter, 에피소드 다섯 줄) |
| [docs/feature/memory-layer/](./docs/feature/memory-layer/INDEX.md) | 설계 배경 — 발표·drakejin 회사와의 대조 |

```
 사람·에이전트가 쓰는 문서                                   에이전트가 묻는 길
 docs/**, main/{repo}/docs/**, README·INDEX·CLAUDE·GUIDE      Claude Code ──(MCP stdio)──▶ memory-mcp
        │ 저장                                                    search_memory · search_filters · related_docs
        ▼                                                          docs_by_entity · list_cold · restore_cold · ingest_path
 reconcile 데몬 (단일 writer)                                            │
   boot + 60초 타이머 + 파일 watcher(3초)                                 ▼
   색인 → 판정(latest-wins/LLM/질문) → 증류(선택) → 그래프 ──▶  OpenSearch :9202  mem-{team}-{yyyy.MM} · mem-{team}-knowledge
        │                                                       Neo4j      :7689  Doc · Entity · Repo 그래프
        ▼                                                                │ 60일 지난 episodic: tier / ISM
 .cache/ (판정·증류·감사 원장, 데몬 상태, cold store, 스냅샷)  ◀───────────┘ cold: 스냅샷 + 원본·청크 JSONL (로컬 또는 S3)
```

---

## 1. 설치

### 1.1 준비물

| 도구 | 확인 | 비고 |
| --- | --- | --- |
| Docker (OrbStack 등) | `docker info` | 메모리 3GB 이상 (OpenSearch 1GB heap + Neo4j) |
| Go 1.25 | `go version` | `app/mise.toml` 이 고정한다. mise 가 있으면 `make memory-init` 이 설치까지 한다 |
| jq, git | `jq --version` | `make init` 이 seed 를 읽는다 |
| Claude Code CLI | `claude --version` | MCP 클라이언트. 판정·증류·`bench` 의 LLM 으로도 쓴다 (API 키 없음, 로그인만) |
| gh (선택) | `gh auth status` | 비공개 repo clone 이 HTTPS 로 실패할 때 fallback |

### 1.2 다섯 단계

```bash
git clone https://github.com/drakejin/workspace ~/Workspace/drakejin/workspace
cd ~/Workspace/drakejin/workspace

make init                            # 1) workspace.seed.json 의 repo 를 main/ 에 clone + pre-commit hook + INDEX.md 갱신
cp .env.example .env                 # 2) (선택) 설정 — 기본값으로도 돈다
make memory-init                     # 3) 빌드(.cache/bin) → OpenSearch·Neo4j 기동 → 템플릿·ISM·스냅샷 repo → 최초 색인 + 그래프
make memory-daemon-start             # 4) 데몬 상주. macOS 는 launchd 가 로그인 때 다시 켜고 죽으면 다시 띄운다 (§1.3)
scripts/memory-daemon/run.sh status  # 5) 확인 — 무엇이 꺼져 있으면 마지막 줄에 '할 일' 이 나온다
```

`make memory-init` 출력:

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
[graph] docs=12 entities=0 edges=12
[memory-init] reconcile: done

[memory-init] 다음: 'make memory-daemon-start' 로 데몬을 상주시키고, 'scripts/memory-daemon/run.sh status' 로 확인하세요
```

`make memory-daemon-start` 출력 (macOS), 그리고 한 번 더 불렀을 때:

```
[memory-daemon] launchd: com.drakejin.memory.workspace.daemon (workspace=workspace, root=/Users/jin/Workspace/drakejin/workspace — 로그인 때 다시 켜고, 죽으면 다시 띄운다)
[memory-daemon] started (pid 39975) — 로그 /Users/jin/Workspace/drakejin/workspace/.cache/daemon/daemon.log, 상태 'scripts/memory-daemon/run.sh status'

[memory-daemon] already running (pid 39975) — 로그 /Users/jin/Workspace/drakejin/workspace/.cache/daemon/daemon.log
```

`scripts/memory-daemon/run.sh status` 출력 (데몬이 떠 있을 때. 첫 줄은 macOS 에서만 나오는 launchd 적재 여부):

```
[memory-daemon] launchd: loaded (com.drakejin.memory.workspace.daemon)
[status] container opensearch: running
[status] container neo4j: running
[status] opensearch: yellow (http://localhost:9202)
[status] index: workspace=workspace 11 docs, 96 chunks (memory indices 6)
[status] last indexed: 2026-10-13T00:32:11.795Z
[status] graph: 12 docs, 0 entities, 12 edges (bolt://localhost:7689)
[status] daemon: running pid=39975 since 2026-10-13T00:32:29.33219Z, cycles=1
[status] heartbeat: 1s ago (interval 60s)
[status] last cycle: 2026-10-13T00:32:29.422157Z trigger=boot scanned=11 indexed=0 stat_updated=0 removed=0 skipped=0 failed=0 cold_owned=0 took=11ms
[status] last graph: docs=12 entities=0 edges=12
[status] judge: on, 답을 기다리는 질문 0건
[status] distill: off, cache 0건, 대기 0건
```

`memory indices` 는 클러스터 전체(다른 meta-repo·샘플 포함)의 메모리 인덱스 수다. 단일 노드라 `yellow` 가 정상이다. 포트는 같은 머신의 다른 메모리 스택(9200·9201, 7687·7688)과 겹치지 않게 잡았다:
OpenSearch `localhost:9202`, Neo4j bolt `7689` · 브라우저 `7476`, (선택) Dashboards `5603`.

### 1.3 상주·정기 실행 (macOS launchd)

| 명령 | 하는 일 |
| --- | --- |
| `make memory-daemon-start` | 데몬을 로그인 때 띄우고 죽으면 다시 띄운다 (launchd `com.drakejin.memory.<workspace>.daemon`, RunAtLoad + KeepAlive — meta-repo 마다 따로라 `META_REPO_ROOT=… make memory-daemon-start` 로 다른 meta-repo 의 데몬도 따로 띄운다. start 때의 메모리 설정을 잡에 담는다(비밀은 빼고 — 표 아래 참고)). launchd 가 없으면 nohup 백그라운드 — 머신을 다시 켜면 다시 start 한다. 이미 돌고 있으면 그대로 둔다 |
| `make memory-daemon-stop` | launchd 에서 내리고(plist 삭제) 떠 있는 데몬에 SIGTERM — 끝날 때까지 최대 60초 기다린다 |
| `scripts/memory-daemon/run.sh run` | 데몬을 포그라운드로 (로그를 터미널에서 볼 때. launchd 가 부르는 진입점이기도 하다) |
| `scripts/schedule/run.sh install` / `uninstall` | 6시간마다 깨워 하루 한 번 tier(60일 지난 episodic → cold) + 감사 보고 (`com.drakejin.workspace.tier`) |

로그는 데몬 `.cache/daemon/daemon.log`, tier `.cache/schedule/tier.log`. 데몬은 하나만 돈다 — start 는 떠 있으면 그대로 두고, 두 번째 `run` 은 `데몬이 이미 돌고 있습니다 (pid …)` 로 거부되며, `once`(와 `make memory-init` 의 최초 색인)는 `[reconcile] 데몬(pid …)이 돌고 있어 1회 색인을 건너뜁니다` 를 찍고 넘어간다.
데몬은 start 때의 메모리 설정(`.env` 와 명령줄의 `META_REPO_ROOT`·`OPENSEARCH_URL`·`OS_PORT`·`MEMORY_*`·`COLD_*`·`AWS_REGION`·`AWS_PROFILE`·`AWS_ENDPOINT_URL_S3`, `NEO4J_*` 는 포트만)을 launchd 잡에 담아 돈다.
이름에 `PASSWORD`·`SECRET`·`TOKEN` 이 든 값은 plist 에 쓰지 않는다 — 잡이 `.env`·AWS 기본 체인에서 다시 읽는다. 설정을 바꾸면 `make memory-daemon-stop && make memory-daemon-start`.

---

## 2. Claude 에 연결

### 2.1 이 레포에서 (project scope — 이미 등록돼 있다)

`.mcp.json` 이 MCP 서버 `drakejin-workspace-memory-mcp`(`bash scripts/mcp/run.sh`)를 등록한다. 처음 한 번 승인한다. 도구 이름은 `mcp__drakejin-workspace-memory-mcp__search_memory` 꼴이다.

```bash
cd ~/Workspace/drakejin/workspace
claude mcp get drakejin-workspace-memory-mcp      # Status: ⏸ Pending approval
claude                                            # 'drakejin-workspace-memory-mcp' 서버 사용 승인 → 세션 안에서 /mcp 로 ✔ connected 확인
```

`.claude/settings.json` 의 **SessionStart 훅**이 세션을 열 때마다 열린 디렉토리(`main/{repo}` 또는 그 worktree)의
repo·브랜치 이름으로 메모리를 검색해 맥락으로 붙인다. 판정 질문이 쌓여 있으면 그것도 알린다. 실패하면 조용히 넘어간다.

### 2.2 다른 디렉토리에서도 이 메모리를 쓰기 (user scope)

```bash
claude mcp add drakejin-workspace-memory-mcp -s user -- bash ~/Workspace/drakejin/workspace/scripts/mcp/run.sh
```

### 2.3 도구

| 도구 | 하는 일 |
| --- | --- |
| `search_memory(query?, project?, topic?, date?, time_range?, doc_type?, team?, org?, size?, include_superseded?)` | hot 영역 BM25 검색, 또는 필터로 가져오기. 필터: **프로젝트**(`project`), **주제**(`topic`), **날짜**(`date`, 그날 하루 — 문서의 날: frontmatter `date`, 없으면 커밋 시각을 `MEMORY_TIME_ZONE` 에서 본 날), **범위**(`time_range` from~to), 문서 종류. `query` 를 비우고 필터만 주면 맞는 문서를 최신순으로(문서 하나에 한 건) 가져온다. 결과는 `source_path › header_path (종류, project=, topic=, 날짜)`, 청크, (증류가 켜져 있으면) 요약 |
| `search_filters(project?, topic?, date?, time_range?, doc_type?, …)` | 필터에 쓸 수 있는 값과 문서 수 — 프로젝트·주제·문서 종류·날짜 범위·날짜별 수 |
| `related_docs(path, depth?)` | knowledge graph 에서 이어진 문서 — supersedes·derived_from·relates·링크·같은 entity·같은 repo |
| `docs_by_entity(entity)` | 증류가 뽑은 entity(`service:order-api` 등)를 다루는 문서 |
| `ingest_path(path, team?)` | 데몬이 아직 잡지 못한 파일·디렉토리를 바로 색인. 경로는 디스크의 철자(대소문자·NFC)로 맞추고, 데몬과 같은 게이트로 바이너리·`MEMORY_MAX_FILE_BYTES` 초과는 건너뛴다고 알린다 |
| `list_cold(team?)` | 60일이 지나 cold 로 간 인덱스 목록 (인덱스마다 그 인덱스를 담은 스냅샷 전부). 이 meta-repo 의 팀만 — 다른 workspace 의 팀이면 거부한다 |
| `restore_cold(index, confirmed?)` | cold 인덱스 복원 (그 인덱스를 담은 스냅샷 전부를 합친다). `confirmed=true` 가 없으면 "사용자에게 먼저 물으라" 고 돌려준다 |

앞의 넷(검색·적재·cold 둘)은 발표의 도구 그대로이고, `search_filters` 는 필터 값을 고르려고, 그래프 둘은 Neo4j 를 붙이며 더했다. 그래프를 끄면(`MEMORY_GRAPH_URI=off`) 그래프 도구는 "꺼져 있다" 를,
Neo4j 에 붙지 못하면 연결 오류를 돌려준다. 어느 쪽이든 나머지 다섯은 그대로 돈다.

---

## 3. 활용

### 3.1 에이전트가 일하는 방식

[CLAUDE.md](./CLAUDE.md) 가 규칙을 건다: **작업을 시작하면 `search_memory` 부터** → 결과의 `source_path` 를 Read 해서 원문을 근거로 → 결과가 없을 때만 그 디렉토리의 `INDEX.md` 부터 훑는다.
60일보다 오래된 맥락이 필요하면 `list_cold` 로 확인하고 **사용자에게 물은 뒤에만** `restore_cold` 한다.

예: "order-api 에서 이중 결제를 어떻게 막기로 했지?" → `search_memory("order-api 이중 결제 멱등")` → ADR 을 Read → `related_docs` 로 그 ADR 을 근거로 쓴 문서까지 확인하고 답한다.

### 3.2 문서 쓰기

| 무엇 | 위치 | 성질 |
| --- | --- | --- |
| 특정 repo 의 ADR·배경·용어 | `docs/repository/{repo}/` | episodic — 60일 뒤 cold |
| 여러 repo 에 걸친 주제 | `docs/feature/{topic}/` | episodic |
| 스펙·작업 plan | `docs/specs/{spec}/` | episodic |
| 이슈 조사 | `docs/issues/{issue}/` | episodic |
| 살아남은 사실·용어 정의 | `docs/knowledge/` | knowledge — 노화 없음, `supersedes:` 로만 밀린다 |
| 관리 대상 repo 자신의 문서 | `main/{repo}/docs/` (그 repo 에 커밋) | knowledge |

frontmatter(`team`, `repo`, `doc_type`, `date`, `supersedes`, `derived_from`, `relates`)와 에피소드 다섯 줄은 [CLAUDE.md](./CLAUDE.md#frontmatter).
저장하면 데몬이 3초 뒤 색인하고, 링크·`supersedes`·`derived_from` 은 그래프의 관계가 된다.

### 3.3 매일·매주

| 언제 | 할 일 | 명령 |
| --- | --- | --- |
| 아무 때나 | 검색해 보기 | `scripts/memory/run.sh search "PaymentRetryPolicy 재시도"` (`-team` / `-org` 는 질의어 앞에) |
| repo 최신화 | main/master 인 repo 만 pull + INDEX.md 에 새 repo 행 | `make sync` |
| 이슈 작업 | worktree 만들기 / 정리 | `scripts/run-issues/run.sh ISSUE-1 books-guitar` → `main/books-guitar/.worktrees/ISSUE-1` / `scripts/done-issue/run.sh ISSUE-1 books-guitar` (커밋하지 않은 변경이 있으면 남긴다 — 버리려면 `--force`) |
| 질문이 쌓이면 | 판정이 사람에게 넘긴 질문 | `scripts/memory/run.sh questions` → `scripts/memory/run.sh answer <pairKey> latest\|old\|keep` |
| 주 1회 | compaction — 60일 지난 사실을 knowledge 로 다시 쓰고 원문은 cold 로 | Claude 에서 `/compaction [team]` (리포트 `scripts/memory/run.sh compaction-report -team T`, 이동 `scripts/memory/run.sh tier -team T` — 먼저 `-dry-run`) |
| 주 1회 (데몬이 자동) | 정합성 감사 — 색인·파일·cold store·원장·링크 | `scripts/memory/run.sh audit` (고치려면 `-apply`) |
| 틀린 cold 문서 | deprecate·undeprecate·purge·edit (감사 기록 필수) | `scripts/memory/run.sh archive deprecate -team T -by <누가> -reason <왜> <path>` |
| 그래프 보기 | 문서 주변·entity·통계 / 다시 만들기 | `scripts/memory/run.sh graph neighbors <path>` · `scripts/memory/run.sh graph entity <name>` · `scripts/memory/run.sh graph stats` / `scripts/memory/run.sh graph rebuild` |
| 효과 재기 | memory-mcp 유무로 같은 질문을 claude 에 | `scripts/memory/run.sh bench -questions <meta-repo 밖 yaml> -model haiku` |

자세한 동작(판정 3단, 증류, cold 소유, 복원, archive, 그래프 재구축 계약)은 [GUIDE.md](./GUIDE.md).

---

## 4. 저장된 데이터 확인하기

메모리 층의 정본은 **git(hot 문서)과 cold 보관소**(스냅샷 + cold store)뿐이다. OpenSearch 의 hot 인덱스와 Neo4j 그래프는 파생물이라 지워도
데몬(꺼져 있으면 `scripts/memory-daemon/run.sh once`) / `scripts/memory/run.sh graph rebuild` 가 LLM 호출 없이 같은 결과로 다시 만든다 — 그래서 아래 조회는 마음 놓고 해도 된다.
쓰기는 하지 않는다: 데몬이 매 사이클 되맞추는 것은 `superseded_by`·`deprecated` 와 파일이 바뀐 문서뿐이라, 인덱스를 직접 고치면
그 문서 파일이 바뀌거나 인덱스를 지우고 다시 만들 때까지 틀린 채로 남는다.

### 4.1 OpenSearch — 색인과 검색

이름 규칙: `{team}` 은 frontmatter `team` (기본 `default`), workspace 는 meta-repo 이름(기본: 루트 디렉토리 이름 `workspace`).

| 이름 | 무엇 |
| --- | --- |
| `mem-{team}-{yyyy.MM}` | episodic 청크 — 문서의 유효 시각(`date`) 달. ISM `mem-hot-cold` (60일 → 스냅샷 → 삭제) |
| `mem-{team}-knowledge` | knowledge 청크 — 노화 없음. ISM `mem-knowledge-keep`. 밀린 knowledge 는 자기 달 인덱스로 옮겨 간다 |
| `mem-{team}-hot` | 읽기 alias — 그 팀의 hot 인덱스 전부 |
| `mem-org-{org}-hot` | org(실) alias — `MEMORY_TEAM_ORGS` 로 묶은 팀 전부 |
| 스냅샷 repo `mem-cold` | cold 스냅샷 (기본 fs: `./.cache/snapshots`) |

청크 한 건의 주요 필드: `workspace`, `team`, `repo`, `doc_type`, `memory_kind`(episodic/knowledge), `event_time`, `source_path`, `header_path`, `chunk_index`, `text`, `title`, `superseded_by`, `deprecated`, `summary`, `entities`, `content_sha`.

```bash
OS=http://localhost:9202

# 클러스터·인덱스·alias
curl -s "$OS/_cluster/health?pretty"
curl -s "$OS/_cat/indices/mem-*?v&h=index,docs.count,store.size&s=index"
curl -s "$OS/_cat/aliases/mem-*?v&h=alias,index"

# memory-mcp 와 같은 검색 (BM25, 식별자 분할 analyzer)
scripts/memory/run.sh search "ClickDeduplicator window"

# 원시 쿼리 — 어떤 청크가 몇 점으로 맞았나
curl -s "$OS/mem-default-hot/_search?pretty" -H 'content-type: application/json' -d '{
  "size": 5,
  "_source": ["source_path", "header_path", "memory_kind", "event_time", "superseded_by"],
  "query": {"multi_match": {"query": "memory daemon", "fields": ["title^3", "header_path^2", "text"]}}
}'

# 한 문서가 어떻게 청크로 잘렸나
curl -s "$OS/mem-default-hot/_search?pretty" -H 'content-type: application/json' -d '{
  "size": 50, "_source": ["chunk_index", "header_path"], "sort": ["chunk_index"],
  "query": {"term": {"source_path": "README.md"}}
}'

# 문서 수·성질별 분포
curl -s "$OS/mem-default-hot/_search?pretty&size=0" -H 'content-type: application/json' -d '{
  "aggs": {"docs": {"cardinality": {"field": "source_path"}},
           "kinds": {"terms": {"field": "memory_kind"}},
           "types": {"terms": {"field": "doc_type"}}}
}'

# 판정·선언으로 밀린 문서, deprecate 된 문서
curl -s "$OS/mem-default-*/_search?pretty" -H 'content-type: application/json' -d '{
  "size": 20, "_source": ["source_path", "superseded_by", "deprecated"], "collapse": {"field": "source_path"},
  "query": {"bool": {"should": [{"exists": {"field": "superseded_by"}}, {"term": {"deprecated": true}}]}}
}'

# ISM 이 인덱스를 어느 상태로 보고 있나 / cold 스냅샷
curl -s "$OS/_plugins/_ism/explain/mem-*?pretty"
curl -s "$OS/_cat/snapshots/mem-cold?v"
scripts/memory/run.sh list-cold            # 같은 내용을 팀 단위로 (.env 를 읽는 운영 CLI 진입점)
```

분석기가 식별자를 어떻게 쪼개는지 보려면:

```bash
curl -s "$OS/mem-default-knowledge/_analyze?pretty" -H 'content-type: application/json' \
  -d '{"field": "text", "text": "ClickDeduplicator.window"}'
```

**화면으로 보기 (OpenSearch Dashboards, 선택)**

```bash
docker compose --profile dashboards up -d     # http://localhost:5603
# 인덱스 패턴 만들기 (UI: Dashboards Management → Index patterns → mem-* / 시간 필드 event_time)
curl -s -XPOST localhost:5603/api/saved_objects/index-pattern/mem-all -H 'osd-xsrf: true' \
  -H 'content-type: application/json' -d '{"attributes":{"title":"mem-*","timeFieldName":"event_time"}}'
```

Discover 에서 `mem-*` 를 고르고 필드 `source_path`, `header_path`, `memory_kind` 를 열로 추가하면 청크가 시간순으로 보인다.
Dev Tools 콘솔에서는 위 curl 의 본문을 그대로 쓴다. `scripts/memory-down/run.sh` 가 Dashboards 까지 내린다.

### 4.2 Neo4j — knowledge graph

| 노드 | 속성 | 무엇 |
| --- | --- | --- |
| `Doc` | `workspace, path, title, team, repo, docType, kind, status, summary` | 문서. `status`: `hot` · `superseded` · `deprecated` · `unindexed`(링크로만 알려졌다 — cold 로 갔거나 오타) |
| `Entity` | `workspace, type, name` | 증류가 뽑은 것 (`service:order-api`, `concept:idempotency` …) |
| `Repo` | `workspace, name` | 관리 대상 repository |

| 관계 | 출처 |
| --- | --- |
| `SUPERSEDES` (새 → 옛) | 개정판의 `supersedes:` 선언, 판정 결과 |
| `DERIVED_FROM` / `RELATES` (`why` 속성) | frontmatter `derived_from:` / `relates:` 의 `{path, why}` |
| `LINKS_TO` | 본문의 마크다운 링크 (`.md`) |
| `MENTIONS` | 증류 entity |
| `IN_REPO` | 문서의 `repo` |

**브라우저**: http://localhost:7476 → Connect URL `bolt://localhost:7689`, Authentication `No authentication`.
**셸**: `docker exec -it dj-workspace-neo4j cypher-shell`

```cypher
// workspace 별 노드 수 (여러 meta-repo 가 같은 Neo4j 를 쓴다)
MATCH (n) RETURN n.workspace AS workspace, labels(n)[0] AS label, count(*) AS n ORDER BY workspace, label;

// 이 레포의 문서와 상태
MATCH (d:Doc {workspace: 'workspace'}) RETURN d.path, d.kind, d.status ORDER BY d.path;

// 한 문서에서 두 걸음 안의 모든 것 (브라우저에서 그림으로 보인다)
MATCH p = (d:Doc {workspace: 'workspace', path: 'README.md'})-[*1..2]-(x) RETURN p;

// knowledge 개정 사슬
MATCH p = (:Doc {workspace: 'workspace'})-[:SUPERSEDES*]->(:Doc) RETURN p;

// 근거(derived_from)와 그 이유
MATCH (a:Doc {workspace: 'workspace'})-[r:DERIVED_FROM]->(b:Doc) RETURN a.path, r.why, b.path;

// 깨진 링크 — 링크 대상이 hot 에 없다
MATCH (s:Doc {workspace: 'workspace'})-[r]->(d:Doc {status: 'unindexed'}) RETURN s.path, type(r), d.path;

// entity 별 문서 (증류를 켰을 때)
MATCH (e:Entity {workspace: 'workspace'})<-[:MENTIONS]-(d:Doc) RETURN e.type, e.name, collect(d.path) AS docs;
```

같은 것을 CLI 로: `scripts/memory/run.sh graph stats` (통계) · `scripts/memory/run.sh graph neighbors -depth 2 docs/knowledge/x.md` · `scripts/memory/run.sh graph entity order-api`.

### 4.3 파일로 남는 것 — `.cache/`

```
.cache/                     # git 에 넣지 않는 로컬 상태 전부 (gitignore). 지워도 되는 것과 안 되는 것이 섞여 있다
├── cold-store/{team}/      # ⚠ 정본 — raw/{path} 원본, chunks/{path}.jsonl 청크, manifests/{index}.jsonl 스냅샷 목록
├── cold-store/.locks/      # 스냅샷 목록 쓰기 잠금 파일 (tier·archive 가 겹쳐도 줄을 잃지 않게) — 지워도 된다
├── snapshots/              # ⚠ 정본 — fs 스냅샷 repository (OpenSearch 컨테이너의 path.repo)
├── judge/{team}.jsonl      # 판정 원장 — 결정·질문·사람의 답 (지우면 다시 묻는다)
├── distill/{team}.jsonl    # 증류 캐시 — 본문 sha → 요약·entity (지우면 다시 증류한다)
├── archive/{team}.jsonl    # cold 문서 조치 감사 기록 (누가·왜) — deprecate 표시의 정본
├── daemon/                 # 락·pid·daemon.log·status.json(마지막 사이클)·*.tick.json(주간 감사·tier 시각)
├── bin/                    # memory-mcp, memory-daemon (scripts 가 부를 때 빌드, make -C app build) — 지워도 된다
├── schedule/ · bench/ · coverage.out   # tier launchd 로그, bench 리포트, 커버리지 — 지워도 된다
```

**`cold-store/` 와 `snapshots/` 는 지우면 안 된다.** tier 가 git 에서 지운 원본이 여기에만 있다. 오래 보관하려면 S3 로 둔다
(`COLD_STORE_S3_BUCKET`, `COLD_SNAPSHOT_TYPE=s3` — GUIDE §7).

```bash
scripts/memory/run.sh questions                       # 판정 원장 중 답을 기다리는 질문
tail -3 .cache/judge/default.jsonl | jq .             # 원장 그대로
jq -c '{sourcePath, summary, entities}' .cache/distill/default.jsonl | head
cat .cache/daemon/status.json | jq .lastCycle         # 마지막 사이클
```

---

## 5. 명령어

### 5.1 루트 Makefile — 평소 쓰는 다섯 개

| 명령어 | 설명 |
| --- | --- |
| `make init` | `workspace.seed.json` 의 repo 를 `main/` 에 clone (`docsOnly: true` 면 `docs/` 만) + pre-commit hook + `make sync`. 멱등 |
| `make sync` | main/master 브랜치인 repo 만 pull, `INDEX.md` 에 없는 repo 행 추가 (로컬 claude, 읽기 도구만) |
| **`make memory-init`** | 메모리 층 올리기 — 도구·빌드·OpenSearch·Neo4j·템플릿·ISM·스냅샷 repository·최초 색인 (멱등) |
| **`make memory-daemon-start`** | reconcile 데몬(색인·판정·증류·그래프·주간 감사) 상주 — macOS 는 launchd, 없으면 nohup. 이미 돌고 있으면 그대로 둔다 |
| **`make memory-daemon-stop`** | 데몬 상주 해제 — launchd 에서 내리고 SIGTERM, 최대 60초 기다린다 |

### 5.2 scripts 로 부르는 운영 명령

루트 타겟이 없는 명령은 스크립트를 직접 부른다. `scripts/*/run.sh` 는 모두 실행 권한이 있어 `bash` 없이 부를 수 있다.

| 명령어 | 설명 |
| --- | --- |
| **`scripts/memory-daemon/run.sh status`** | 컨테이너·색인·그래프·데몬·판정 질문·할 일 (macOS 면 첫 줄에 launchd 적재 여부) |
| `scripts/memory-daemon/run.sh run` | 데몬 포그라운드 실행 (launchd 가 부르는 진입점) |
| `scripts/memory-daemon/run.sh once` | 1회 색인 (데몬이 켜져 있으면 건너뜀) |
| `scripts/memory-down/run.sh` | 스택 종료 (hot 볼륨은 남는다). 데몬은 멈추지 않으니 상주 데몬이 있으면 먼저 `make memory-daemon-stop` |
| `scripts/schedule/run.sh install` / `uninstall` / `status` | tier 정기 실행 launchd 등록 / 해제 / 확인 (tier 만 맡는다 — 데몬 상주는 `make memory-daemon-start`) |
| `scripts/run-issues/run.sh <issue> <repo>` / `scripts/done-issue/run.sh [--force] <issue> <repo>` | 이슈 worktree 생성(`main/{repo}/.worktrees/{issue}`) / 제거. 커밋하지 않은 변경·untracked 파일이 있으면 잃게 될 파일을 보여 주고 남긴다(exit 1) — `--force` 면 버리고 지운다. lock 건 worktree 와 worktree 가 아닌 디렉토리는 `--force` 여도 지우지 않는다 |
| `scripts/docs-cleaner/run.sh` | `docs/issues`, `docs/specs` 문서 전체 삭제 (파괴적 — 확인을 묻는다. `CONFIRM=yes` 면 묻지 않는다) |
| `scripts/mcp/run.sh` | MCP 서버 (stdio) 수동 실행 (`.mcp.json` 이 부르는 진입점) |
| `scripts/memory/run.sh search [-project P] [-topic T] [-date YYYY-MM-DD] [-from D] [-to D] [-type K] [-team T \| -org O] [-size N] [-superseded] [query]` | 검색 — 질의어 없이 필터만 주면 최신순으로 가져온다 (플래그는 질의어 앞에) |
| `scripts/memory/run.sh filters [-project P] [-topic T] [-date D] [-from D] [-to D] [-type K]` | 검색 필터에 쓸 수 있는 값과 문서 수 (MCP `search_filters` 와 같다) |
| `scripts/memory/run.sh questions` / `answer <pairKey> latest\|old\|keep` | 판정 질문 보기 / 답하기 |
| `scripts/memory/run.sh compaction-report [-team T]` | 주간 compaction 1·2단계 리포트 (수명 확인·충돌 후보) |
| `scripts/memory/run.sh tier [-team T] [-dry-run] [-keep-files]` | 60일 지난 episodic(과 밀린 knowledge) → 쓰기 막기 → 스냅샷 → 인덱스 삭제 → hot 원본 제거(`git rm`). `-team` 이 없으면 이 workspace 의 모든 팀 |
| `scripts/memory/run.sh audit [-apply]` | 정합성 감사 (고칠 수 있는 것: 고아 색인·미색인·내용 불일치·고아 청크) |
| `scripts/memory/run.sh archive <deprecate\|undeprecate\|purge\|edit> [-team T] -by B -reason R [-superseded-by P] [-file F] <path>` | cold 문서 조치 |
| `scripts/memory/run.sh graph <neighbors [-depth N] <path> \| entity <name> \| stats \| rebuild>` | knowledge graph 조회 / 재구축 (LLM 0회) |
| `scripts/memory/run.sh bench -questions F [-model M] [-modes with,without] [-runs N]` | memory-mcp 유무 비교 측정 → `.cache/bench/*.md` (F 는 meta-repo 밖) |

전체 목록은 `scripts/memory/run.sh help` — 위 표에 없는 명령(`list-cold`, `restore`, `reload`, `ingest`, `bootstrap`, `tier-tick` …)도
이 진입점으로 부른다 (`.env` 를 읽고, 바뀐 게 있으면 빌드한다. `.cache/bin/memory-mcp` 를 직접 부르면 `.env` 가 빠진다).

### 5.3 app/Makefile — Go 개발 (`make -C app <타겟>`)

| 명령어 | 설명 |
| --- | --- |
| `make build` | `../.cache/bin` 에 memory-mcp, memory-daemon 빌드 |
| `make test` | 단위 테스트 (`-race`) |
| `make test-e2e` | 실제 스택(루트의 `make memory-init`)에 붙는 e2e + 커버리지 (`../.cache/coverage.out`) |
| `make test-e2e-slow` | ISM 이 실제 시간으로 인덱스를 cold 로 옮기는 e2e (6~15분, 루트의 `make memory-init` 스택 필요) |
| `make test-e2e-s3` | cold 층을 S3(LocalStack)에 두는 e2e — 전용 스택을 올렸다 내린다 |
| `make mcp` | MCP 서버 (stdio) 수동 실행 (`scripts/mcp/run.sh` 와 같다) |

테스트 타겟은 모두 `app/test/run.sh` 를 부른다.

---

## 6. 디렉토리 구조

```
.
├── workspace.seed.json     # 관리 대상 repo 목록 — 유일한 워크스페이스 설정
├── INDEX.md                # repository 인덱스 (make sync 가 새 repo 행을 더한다)
├── README.md · GUIDE.md · CLAUDE.md
├── Makefile                # 평소 쓰는 다섯 개(init sync memory-init memory-daemon-start memory-daemon-stop) → scripts/{기능}/run.sh
├── docker-compose.yml      # OpenSearch(9202) + Neo4j(7689/7476) + Dashboards(5603, 선택 프로필)
├── .env.example            # 메모리 층 설정 (복사해서 .env)
├── .mcp.json               # Claude Code 에 memory MCP 등록
├── .claude/
│   ├── settings.json       # SessionStart 훅 → scripts/session-context
│   └── commands/compaction.md   # /compaction
├── main/                   # 관리 대상 repo clone (gitignore, 커밋 금지)
│   └── {repo}/
│       ├── docs/           # 그 repo 자신의 문서 — 색인한다 (knowledge)
│       └── .worktrees/     # 이슈 worktree — 색인 제외
├── docs/                   # repository/ · feature/ · specs/ · issues/ · knowledge/  (§3.2)
├── scenarios/              # 시나리오 테스트 — SCENARIOS.md, 픽스처, setup·teardown·claude-mcp 스크립트
├── examples/
│   ├── sample-workspace/   # 체험용 가상 meta-repo (GUIDE.md §4)
│   └── sample-bench.yaml   # 샘플용 bench 질문 (meta-repo 밖에 둔다)
├── app/                    # Go 모듈 전부 (github.com/drakejin/workspace/app)
│   ├── Makefile            # Go 개발 — build test test-e2e test-e2e-slow test-e2e-s3 mcp (§5.3)
│   ├── go.mod · go.sum · mise.toml
│   ├── memory-mcp/         # MCP 서버(stdio) + 운영 CLI      → .cache/bin/memory-mcp
│   ├── daemon/             # reconcile 데몬                  → .cache/bin/memory-daemon
│   ├── internal/           # config naming model markdown document opensearch ingest search cold compaction
│   │                       #   state llm daemon graph audit archive bench mcpserver ops
│   └── test/
│       ├── run.sh          # 테스트 진입점 — 단위·e2e·slow·s3 (app/Makefile 이 부른다)
│       ├── e2e/            # 실제 스택 e2e (-tags e2e, e2e_slow, e2e_s3)
│       └── s3/             # S3 e2e 전용 스택 (LocalStack + repository-s3 OpenSearch)
├── scripts/                # init sync docs-cleaner run-issues done-issue hooks
│                           #   memory-init memory-down memory-daemon memory mcp schedule session-context
│                           #   memory-common.sh (source 전용 헬퍼)
└── .cache/                 # 생성물 전부 (gitignore, §4.3)
```

모든 `INDEX.md` 는 사람에게는 진입점이고, 에이전트에게는 검색 결과가 없을 때 디렉토리 전체 대신 읽게 하는 장치다.

---

## 7. 설정

### 7.1 workspace.seed.json

```json
{
  "githubHost": "github.com",
  "org": "drakejin",
  "workspaces": [
    { "name": "books-guitar", "mainBranch": "main" },
    { "name": "big-repo", "mainBranch": "main", "docsOnly": true }
  ]
}
```

repo 를 추가하면 `make init` 을 다시 실행한다 — 이미 있는 repo 는 건너뛰고, 데몬이 다음 사이클에 `main/{repo}/docs/` 를 색인한다.
`docsOnly: true` 는 sparse checkout 으로 `docs/` 만 받는다 (코드가 큰 repo 의 문서만 메모리에 넣을 때).

### 7.2 .env

메모리 층 설정. `.env.example` 에 전 항목과 기본값이 있다. 명령줄에서 준 값(`META_REPO_ROOT=... scripts/memory-daemon/run.sh once`)이 `.env` 보다 우선한다.
자주 바꾸는 것: `MEMORY_DEFAULT_TEAM`, `MEMORY_TEAM_ORGS`(org alias), `MEMORY_DISTILL_ENABLED`(본문이 LLM 으로 간다), `MEMORY_GRAPH_URI=off`(그래프 끄기),
`COLD_STORE_S3_BUCKET` · `COLD_SNAPSHOT_TYPE=s3`(cold 층을 S3 로). 전체 표는 [GUIDE.md §8](./GUIDE.md#8-설정-레퍼런스).

---

## 8. 개발

- Go 1.25 (`app/mise.toml`). 모든 Go 코드는 `app/` 아래 한 모듈. `gofmt`, `go vet` 통과
- 외부 세계(OpenSearch, Neo4j, S3, 파일, LLM)에 닿는 컴포넌트는 작은 인터페이스 + unexported 구현, 생성자 주입
- Go 개발 명령은 `app/Makefile` 에 있다 (`make -C app <타겟>`, §5.3). 테스트 타겟은 `app/test/run.sh` 를 부른다
- `make -C app test` = `go test -race ./...` (스택 없이). `make -C app test-e2e`·`test-e2e-slow` 는 루트의 `make memory-init` 으로 스택이 떠 있어야 하고, test-e2e 는 `.cache/coverage.out` 에 커버리지를 남긴다
- e2e 는 고유 팀·workspace 로 돌고 끝나면 자기 인덱스·스냅샷·그래프만 지운다 — 실제 메모리와 같은 스택을 써도 섞이지 않는다

### 시나리오 테스트 — Claude 가 문서를 읽으며 확인한다

[scenarios/SCENARIOS.md](./scenarios/SCENARIOS.md) 는 이 프로젝트 전체(설치·데몬·검색 필터·Claude Code MCP·판정·그래프·cold·워크스페이스 명령·LLM·자동 테스트)를
단계별 `명령 → 기대` 로 적은 시나리오다. Claude 서브에이전트에게 "이 문서의 G3 을 실행하고 PASS/FAIL 을 보고해" 라고 시키면 된다.
각 그룹은 `scenarios/setup.sh <이름>` 이 만든 임시 meta-repo(픽스처 `scenarios/fixture/`, 날짜는 오늘 기준)와 전용 workspace·팀에서 돌고
`scenarios/teardown.sh <이름>` 으로 자기 것만 지운다. `scenarios/claude-mcp.sh "<프롬프트>"` 는 Claude Code 에 `drakejin-workspace-memory-mcp` 를 붙여
부른 도구·입력·결과를 그대로 보여 준다 — 필터(날짜·범위·주제·프로젝트)를 Claude 가 맞게 골라 문서를 가져오는지 그걸로 판정한다.

### 컨벤션

- Makefile: 루트는 평소 쓰는 다섯 개(`init`, `sync`, `memory-init`, `memory-daemon-start`, `memory-daemon-stop`)만 둔다. 루트 타겟은 `.PHONY` 이고 각각 `scripts/{기능}/run.sh` 를 부른다, kebab-case. 그 밖의 운영 명령은 타겟을 늘리지 않고 스크립트를 직접 부른다 (값은 인자·플래그로). Go 개발 타겟은 `app/Makefile` (테스트는 `app/test/run.sh`)
- Bash: `#!/usr/bin/env bash` + `set -euo pipefail`. `run.sh` 는 진입점, `_*.sh` 는 내부 스크립트. 출력은 `[action] target: message`, 컬러 없음
- 커밋: `<type>: <description>` (feat, fix, refactor, docs, test, chore, perf, ci)

## 9. 주의사항

- `main/` 아래 파일은 이 레포에 커밋하지 않는다 (pre-commit hook 이 막는다)
- `.cache/` 는 gitignore 지만 통째로 지우면 안 된다 — `cold-store/`·`snapshots/` 는 cold 층의 정본이고, `judge/`·`distill/` 을 지우면 사람의 답과 증류 결과가 사라진다 (§4.3)
- 팀 이름 `org`, `org-*` 는 쓸 수 없다 (org alias `mem-org-{org}-hot` 자리)
- 증류(`MEMORY_DISTILL_ENABLED=true`)는 문서 본문을 로컬 claude 로 보낸다 — 기밀 범위를 정한 뒤 켠다
- clone/pull 은 HTTPS. 비공개 repo 는 PAT 기반 `~/.gitconfig` 가 필요하고, 실패하면 `gh repo clone` 으로 다시 시도한다
