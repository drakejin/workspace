# drakejin/workspace

drakejin 개인 리포지토리들을 한 곳에서 관리하는 워크스페이스입니다.

여러 리포에 걸친 코드 탐색, 이슈 추적, 스펙 문서화를 컨텍스트 전환 없이 수행할 수 있도록 설계되었습니다.

---

## Quick Start

```bash
cd /Users/jin/Workspace/drakejin/workspace
make init
```

`make init`은 `workspace.seed.json`에 등록된 모든 리포지토리를 `main/` 디렉토리에 clone하고 git pre-commit hook을 설치합니다.

> 비공개 리포 인증은 `~/.gitconfig`의 PAT 설정 또는 `gh auth login` fallback으로 처리됩니다.

---

## 일상 워크플로

### 1. 최신 코드를 받고 싶을 때

```bash
make sync
```

main/master 브랜치인 리포만 git pull하고 `workspace.json`의 branch/commit 정보를 갱신합니다.

### 2. 워크스페이스 메타가 변경됐을 때

`VERSION` 파일과 `workspace.json.version`이 어긋나면 워크스페이스 메타(scripts, seed, docs 등)가 변경됐다는 신호입니다. `make init`을 다시 실행하면 동기화됩니다.

---

## 명령어

| 명령어 | 설명 |
|---|---|
| `make init` | 최초 세팅. 리포지토리 clone + git pre-commit hook 설치 + `workspace.json` 생성 |
| `make sync` | main/master 브랜치인 리포만 git pull + `workspace.json` 갱신 |
| `make workspace` | git pull 없이 `workspace.json`만 현재 상태로 갱신 |
| `make docs-cleaner` | `docs/issues`, `docs/specs` 내 문서 전체 삭제 (파괴적) |
| `make run-issues ARGS=...` | 이슈 worktree 생성 + `workspace.json` 갱신 |
| `make done-issue ARGS=...` | 이슈 worktree 제거 + `workspace.json` 갱신 |

`make init`이 수행하는 일:

- `workspace.json`이 없으면 `workspace.seed.json`에서 생성
- 등록된 모든 리포지토리를 `main/` 디렉토리에 clone (이미 있으면 skip)
- git pre-commit hook 설치 — `main/` 디렉토리 실수 커밋 방지, 매 commit 시 `VERSION` 자동 갱신
- `make sync`를 호출해 branch/commit 정보 갱신
- `VERSION` 파일 내용을 `workspace.json.version`에 기록

---

## 디렉토리 구조

```
.
├── workspace.seed.json    # 리포 목록 seed (Git 트래킹)
├── workspace.json         # 로컬 상태 (gitignore, init 시 seed에서 생성)
├── VERSION                # 워크스페이스 메타 변경 감지용 timestamp
├── INDEX.md               # 리포지토리 인덱스
├── Makefile               # 명령어 진입점
├── main/                  # 모든 리포지토리가 clone되는 디렉토리
│   └── books-guitar/
├── docs/
│   ├── issues/            # 이슈 기반 문서
│   ├── specs/             # User Story 스펙 문서
│   └── main/              # 리포별 도메인 문서
│       └── {repo}/
│           ├── app/
│           ├── business/
│           ├── monitor/
│           └── knowledge/
│               ├── INDEX.md
│               └── {YYYYMMDD}_{name}/
│                   └── README.md
└── scripts/
    ├── hooks/             # 공유 git hooks
    │   └── pre-commit
    ├── init/              # make init
    ├── sync/              # make sync, make workspace
    ├── docs-cleaner/      # make docs-cleaner
    ├── run-issues/        # 이슈 worktree 생성 + workspace.json 갱신
    └── done-issue/        # 이슈 worktree 제거 + workspace.json 갱신
```

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

- `githubHost`: GitHub 호스트 (기본 `github.com`)
- `org`: GitHub organization 또는 user
- `workspaces[]`: clone 대상 리포 목록
  - `name`: 리포 이름
  - `mainBranch`: 리포의 기본 브랜치

### workspace.json (동적, gitignore됨)

각 사용자의 로컬 상태를 추적합니다. `make init` 시 seed에서 생성된 후 `make sync` / `make workspace` / `scripts/run-issues/_update_workspace.sh`로 자동 갱신됩니다.

```json
{
  "githubHost": "github.com",
  "org": "drakejin",
  "workspaces": [
    {
      "name": "books-guitar",
      "mainBranch": "main",
      "workingBranch": "main",
      "lastCommitHash": "...",
      "working": [
        { "targetWorktrees": "main", "targetBranch": "main", "issue": "" }
      ]
    }
  ]
}
```

---

## 새 리포지토리 추가

1. `workspace.seed.json`의 `workspaces` 배열에 항목을 추가
   ```json
   { "name": "new-repo", "mainBranch": "main" }
   ```
2. `make init` 실행

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

---

## 주의사항

- `main/` 디렉토리의 파일을 이 워크스페이스 리포에 커밋하면 안 됩니다 (pre-commit hook으로 방지됨)
- `workspace.json`은 gitignore되어 있으며 각자 로컬 상태를 가집니다
- clone/pull은 HTTPS를 사용합니다. 비공개 리포 인증을 위해 PAT 기반 `~/.gitconfig` 설정이 필요합니다
- HTTPS 인증이 실패하면 `make init`은 자동으로 `gh repo clone` fallback을 시도합니다 (`gh auth login` 필요)
