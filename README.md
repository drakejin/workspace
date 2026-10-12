# meta-workspace

여러 repository의 개발문서(spec, ADR, PRD, AI plan)를 한곳에 모으는 meta-repository와, 그 문서를 OpenSearch BM25로 코딩 에이전트에게 먹이는 memory-mcp.
에이전트는 파일 트리를 뒤지는 대신 OpenSearch에 묻습니다.

```
meta-workspace/
├── CLAUDE.md              # 에이전트 작업 규칙과 변경 내역
├── .mcp.json              # memory MCP 서버 등록
├── .claude/commands/      # /compaction (주간 compaction 절차)
├── docker-compose.yml     # OpenSearch 단일 노드 (standalone)
├── repos/                 # 관리 대상 repository (서브모듈 또는 worktree)
├── docs/
│   ├── repository/{repo}/ # 그 리포에만 해당하는 주제. INDEX.md
│   ├── feature/{topic}/   # 리포를 넘나드는 주제. INDEX.md
│   ├── specs/{spec}/      # 사람/AI 스펙, 작업 plan. INDEX.md
│   └── knowledge/         # compaction에서 살아남은 사실. INDEX.md
├── app/
│   ├── memory-mcp/        # MCP 서버(stdio) + 운영 CLI
│   └── file-watcher/      # docs 변경 감지 → 재색인
└── packages/memory-core/  # 청킹·메타 추출·색인·검색·hot/cold 공용 로직
```

## 시작

```bash
pnpm install
cp .env.example .env        # 필요하면 MEMORY_DEFAULT_TEAM 등 수정
pnpm up                     # OpenSearch 기동 (localhost:9202)
pnpm memory bootstrap       # 인덱스 템플릿, ISM 정책, 스냅샷 repository
pnpm watch                  # 파일 워처: 시작 시 전체 적재 후 변경 감시
```

Claude Code는 `.mcp.json`으로 memory 서버를 바로 씁니다. 다른 클라이언트는 `pnpm -s mcp`를 stdio 서버로 등록하면 됩니다.

## 동작

| 구성 | 내용 |
| --- | --- |
| 색인 대상 | `docs/**`, `repos/{repo}/docs/**`, `repos/{repo}.wt/{branch}/docs/**`의 마크다운 |
| 청킹 | 마크다운 헤더 계층 단위(`header_path = A > B > C`). 코드 블록 안에서는 자르지 않음 |
| 메타 | frontmatter(`team`, `repo`, `doc_type`, `date`)가 우선. 없으면 경로에서 추론하고, 시간은 git 커밋 시각 → mtime 순 |
| 인덱스 | `mem-{team}-{yyyy.MM}` 월별 인덱스 + 읽기 alias `mem-{team}-hot` |
| 검색 | BM25만 사용(임베딩 없음). 식별자 분할 analyzer라서 `ClickDeduplicator.window`를 `ClickDeduplicator`나 `window`로도 찾음 |
| hot/cold | ISM: 인덱스 생성 후 60일 → 스냅샷(`mem-cold` repository) → 삭제. 오래된 문서를 늦게 적재한 인덱스는 `pnpm memory tier`가 최신 event_time 기준으로 처리 |
| cold 층 | 스냅샷(기본 `./.data/snapshots`, s3 가능) + raw 원본과 청크 JSONL(기본 `./.data/cold-store`, `COLD_STORE_S3_BUCKET`이면 S3) |

### MCP 도구

| 도구 | 역할 |
| --- | --- |
| `search_memory(team?, query, time_range?, repo?, doc_type?, size?)` | hot alias에 BM25 검색. `source_path`, `header_path`가 붙은 청크를 돌려줌 |
| `ingest_path(path, team?)` | 워처가 놓친 파일/디렉토리 수동 적재. meta-repo 밖 경로는 거부 |
| `list_cold(team?)` | 스냅샷에 있는 cold 인덱스 목록 |
| `restore_cold(index, confirmed?)` | 스냅샷 restore. `confirmed=true`가 없으면 복원하지 않고 "사용자에게 먼저 물으라"고 돌려줌 |

### 운영 CLI

```bash
pnpm memory search "ClickDeduplicator window" --team ads
pnpm memory ingest docs/specs
pnpm memory compaction-report --team ads   # 주간 compaction 1·2단계 (수명 확인, 충돌 후보)
pnpm memory tier --dry-run                 # 5단계 티어 이동
pnpm memory list-cold --team ads
pnpm memory restore mem-ads-2026.07
pnpm memory reload --team ads              # 청크 JSONL → _bulk 재적재 (매핑 변경·임베딩 추가 시)
```

주간 compaction은 Claude Code에서 `/compaction [team]`으로 실행합니다.

### S3 cold

- raw/JSONL: `COLD_STORE_S3_BUCKET`, `COLD_STORE_S3_PREFIX`, `AWS_REGION`을 설정하면 로컬 디렉토리 대신 S3에 씁니다. 자격 증명은 AWS SDK 기본 체인을 따릅니다.
- 스냅샷: `COLD_SNAPSHOT_TYPE=s3`, `COLD_SNAPSHOT_S3_BUCKET`을 설정합니다. OpenSearch 컨테이너에 `repository-s3` 플러그인과 keystore 자격 증명이 있어야 합니다. 기본 compose는 fs repository만 씁니다.

## 테스트

```bash
pnpm test            # 단위 + 통합 (OpenSearch 불필요)
pnpm up && pnpm test:e2e   # 실제 OpenSearch에 대한 블랙박스 e2e
pnpm test:coverage
```
