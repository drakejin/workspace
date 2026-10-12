# drakejin/workspace

drakejin 개인 리포지토리들을 한 곳에서 관리하는 워크스페이스(meta-repository)입니다.

여러 리포에 걸친 코드 탐색, 이슈 추적, 스펙 문서화를 컨텍스트 전환 없이 수행할 수 있도록 설계되었습니다.
리포들의 메타 정보(spec, ADR, PRD, AI plan)는 `docs/` 에 쌓고, 코딩 에이전트는 파일 트리를 뒤지는 대신
**메모리 층**(OpenSearch BM25 + reconcile 데몬 + memory-mcp)에 묻습니다.

- 메모리 층 설치·Claude 연결·사용법·예제: **[GUIDE.md](./GUIDE.md)**
- 에이전트 작업 규칙: [CLAUDE.md](./CLAUDE.md)
- 설계 배경과 발표 대비 정렬: [docs/specs/memory-mcp/](./docs/specs/memory-mcp/INDEX.md)

---

## Quick Start

```bash
cd ~/Workspace/drakejin/workspace
make init        # 리포 clone(main/) + git hook + workspace.json
make memory-up   # 메모리 층: 빌드 → OpenSearch → 최초 색인
make daemon      # 데몬 상주 (터미널 하나)
```

`make init`은 `workspace.seed.json`에 등록된 모든 리포지토리를 `main/` 디렉토리에 clone하고 git pre-commit hook을 설치합니다.

> 비공개 리포 인증은 `~/.gitconfig`의 PAT 설정 또는 `gh auth login` fallback으로 처리됩니다.

---

## 일상 워크플로

### 1. 최신 코드를 받고 싶을 때

```bash
make sync
```

main/master 브랜치인 리포만 git pull하고 `workspace.json`의 branch/commit 정보를 갱신합니다. 메모리 데몬이 켜져 있으면 바뀐 `main/{repo}/docs` 는 알아서 다시 색인됩니다.

### 2. 이슈 작업

```bash
make run-issues ARGS="ISSUE-1 books-guitar"   # main/books-guitar/.worktrees/ISSUE-1
make done-issue ARGS="ISSUE-1 books-guitar"
```

### 3. 워크스페이스 메타가 변경됐을 때

`VERSION` 파일과 `workspace.json.version`이 어긋나면 워크스페이스 메타(scripts, seed, docs 등)가 변경됐다는 신호입니다. `make init`을 다시 실행하면 동기화됩니다.

---

## 명령어

| 명령어 | 설명 |
|---|---|
| `make init` | 최초 세팅. 리포지토리 clone + git pre-commit hook 설치 + `workspace.json` 생성 |
| `make sync` | main/master 브랜치인 리포만 git pull + `workspace.json` 갱신 |
| `make workspace` | git pull 없이 `workspace.json`만 현재 상태로 갱신 |
| `make docs-cleaner` | `docs/issues`, `docs/specs` 내 문서 전체 삭제 (파괴적) |
| `make run-issues ARGS=...` | 이슈 worktree 생성(`main/{repo}/.worktrees/{issue}`) + `workspace.json` 갱신 |
| `make done-issue ARGS=...` | 이슈 worktree 제거 + `workspace.json` 갱신 |
| **`make memory-up`** | 메모리 층 인프라 올리기 — 도구·빌드·OpenSearch·최초 색인 (멱등) |
| **`make daemon`** | reconcile 데몬 상주 — 색인·판정·증류가 알아서 돈다 |
| **`make daemon-status`** | 데몬 상태 — 컨테이너·색인·데몬·판정 질문·할 일 |
| `make memory-down` | OpenSearch 종료 |
| `make reconcile` | 1회 색인 (데몬이 켜져 있으면 건너뜀) |
| `make query Q="..." [TEAM=]` | 검색 |
| `make questions` / `make answer KEY= A=` | 판정이 사람에게 넘긴 질문 보기 / 답하기 |
| `make compaction-report [TEAM=]` | 주간 compaction 1·2단계 리포트 |
| `make tier [TEAM=] [DRY=1]` | hot 기간이 지난 episodic → cold(스냅샷), hot 원본 제거 |
| `make mcp` | MCP 서버 (stdio) — `.mcp.json` 이 부른다 |
| `make build` / `make test` / `make test-e2e` | Go 빌드 / 단위 테스트(-race) / 실제 OpenSearch e2e + 커버리지 |

전체 운영 명령은 `bin/memory-mcp help`.

---

## 디렉토리 구조

```
.
├── workspace.seed.json    # 리포 목록 seed (Git 트래킹)
├── workspace.json         # 로컬 상태 (gitignore, init 시 seed에서 생성)
├── VERSION                # 워크스페이스 메타 변경 감지용 timestamp
├── INDEX.md               # 리포지토리 인덱스
├── CLAUDE.md              # 에이전트 작업 규칙
├── GUIDE.md               # 메모리 층 가이드
├── Makefile               # 명령어 진입점
├── docker-compose.yml     # OpenSearch 단일 노드 (localhost:9202)
├── .mcp.json              # Claude Code 에 memory MCP 등록
├── .claude/commands/      # /compaction
├── main/                  # 모든 리포지토리가 clone되는 디렉토리 (커밋 금지)
│   └── {repo}/
│       ├── docs/          # 그 리포 자신의 문서 — 메모리 층이 색인한다 (knowledge)
│       └── .worktrees/    # 이슈 worktree (색인 제외)
├── docs/
│   ├── repository/{repo}/ # 그 리포에만 해당하는 주제. ADR, 배경, 용어 매핑
│   ├── feature/{topic}/   # 리포를 넘나드는 특정 주제
│   ├── specs/{spec}/      # 사람·AI 스펙, 작업 plan
│   ├── issues/{issue}/    # 이슈 조사 기록
│   └── knowledge/         # compaction 에서 살아남은 사실
├── app/
│   ├── memory-mcp/        # MCP 서버(stdio) + 운영 CLI  → bin/memory-mcp
│   └── daemon/            # reconcile 데몬              → bin/memory-daemon
├── internal/              # 메모리 층 Go 패키지 (config, markdown, document, opensearch, ingest,
│                          #   search, cold, compaction, llm, daemon, mcpserver, ops)
├── test/e2e/              # 실제 OpenSearch e2e (-tags e2e)
├── examples/sample-workspace/  # 체험용 가상 meta-repo (GUIDE.md §4)
├── judge-state/           # 판정 원장 (데몬이 만든다, 커밋한다)
├── knowledge-cache/       # 증류 캐시 (데몬이 만든다, 커밋한다)
└── scripts/
    ├── hooks/             # 공유 git hooks
    ├── init/  sync/  docs-cleaner/  run-issues/  done-issue/
    ├── memory-up/  memory-down/  daemon/  reconcile/  memory/  mcp/  build/  test/
    └── memory-common.sh   # 메모리 층 스크립트 공용 헬퍼
```

모든 디렉토리의 `INDEX.md` 는 사람에게는 진입점이고, 에이전트에게는 검색 결과가 없을 때 전체 대신 INDEX 만 읽게 하는 장치입니다.

---

## 설정 파일

### workspace.seed.json (정적, Git 트래킹)

리포 목록의 seed.

```json
{
  "githubHost": "github.com",
  "org": "drakejin",
  "workspaces": [
    { "name": "books-guitar", "mainBranch": "main" }
  ]
}
```

### workspace.json (동적, gitignore됨)

각 사용자의 로컬 상태를 추적합니다. `make init` 시 seed에서 생성된 후 `make sync` / `make workspace` / `scripts/run-issues/_update_workspace.sh`로 자동 갱신됩니다.

### .env (선택, gitignore됨)

메모리 층 설정. `.env.example` 을 복사해서 씁니다. 항목 설명은 [GUIDE.md §7](./GUIDE.md#7-설정-레퍼런스).

---

## 새 리포지토리 추가

1. `workspace.seed.json`의 `workspaces` 배열에 항목을 추가
   ```json
   { "name": "new-repo", "mainBranch": "main" }
   ```
2. `make init` 실행 — 데몬이 `main/new-repo/docs/` 를 다음 사이클에 색인합니다

---

## 컨벤션

### Makefile 규칙

- 모든 명령어는 `.PHONY`로 선언한다
- 각 타겟은 `scripts/{기능명}/run.sh`를 호출한다
- 타겟 이름은 kebab-case를 사용한다 (예: `docs-cleaner`)

### Bash 스크립트 규칙

- 모든 스크립트는 `#!/usr/bin/env bash`와 `set -euo pipefail`로 시작한다
- `run.sh`: 각 기능의 진입점. Makefile에서 직접 호출되는 스크립트
- `_*.sh`: run.sh에서 호출되는 내부 스크립트. 기능 단위로 분리한다
- 외부에서 직접 호출 가능한 유틸리티는 underscore 없이 명명한다
- 출력 형식은 `[action] target: message` 패턴을 따른다
- 컬러 코드를 사용하지 않는다

### Go 규칙

- Go 1.25 (`mise.toml`). `gofmt`, `go vet` 통과
- 외부 세계(OpenSearch, S3, 파일, LLM)에 닿는 컴포넌트는 인터페이스 + unexported 구현, 생성자 주입
- 테스트는 `go test -race ./...`, 실제 OpenSearch 가 필요한 테스트는 `test/e2e` (`-tags e2e`)

---

## 주의사항

- `main/` 디렉토리의 파일을 이 워크스페이스 리포에 커밋하면 안 됩니다 (pre-commit hook으로 방지됨)
- `workspace.json`, `.env`, `.data/`, `bin/` 은 gitignore되어 있으며 각자 로컬 상태를 가집니다
- `judge-state/`, `knowledge-cache/` 는 커밋합니다 — 인덱스를 지워도 판정·증류 결과를 LLM 재호출 없이 복원하는 원장입니다
- clone/pull은 HTTPS를 사용합니다. 비공개 리포 인증을 위해 PAT 기반 `~/.gitconfig` 설정이 필요합니다
- HTTPS 인증이 실패하면 `make init`은 자동으로 `gh repo clone` fallback을 시도합니다 (`gh auth login` 필요)
