.PHONY: init sync workspace docs-cleaner run-issues done-issue memory-up memory-down daemon daemon-status reconcile query questions answer compaction-report tier mcp build test test-e2e

init:
	@bash scripts/init/run.sh

sync:
	@bash scripts/sync/run.sh

workspace:
	@bash scripts/sync/_collect_workspace.sh main workspace.json

docs-cleaner:
	@bash scripts/docs-cleaner/run.sh

run-issues:
	@bash scripts/run-issues/run.sh $(ARGS)

done-issue:
	@bash scripts/done-issue/run.sh $(ARGS)

# ── 메모리 층 (OpenSearch + reconcile 데몬 + memory-mcp, Go) ──────────────────────────
# 평소 쓰는 명령은 셋뿐이다 (GUIDE.md).
#   memory-up      인프라 올리기 — 도구·빌드·스택·최초 색인까지 한 번에 (멱등)
#   daemon         데몬 실행 — 켜두면 색인·판정·증류가 알아서 돈다
#   daemon-status  데몬 상태 — 컨테이너·색인·데몬·판정 질문 점검
# 나머지는 데몬이 대신 하는 일의 수동 진입점이다. 전체 명령: bin/memory-mcp help

memory-up:
	@bash scripts/memory-up/run.sh

daemon:
	@bash scripts/daemon/run.sh

daemon-status:
	@bash scripts/memory/run.sh status

memory-down:
	@bash scripts/memory-down/run.sh

# 1회 색인 — 데몬이 켜져 있으면 필요 없다
reconcile:
	@bash scripts/reconcile/run.sh

# make query Q="검색어" [TEAM=팀] — 에이전트는 MCP search_memory 로 같은 검색을 한다
query:
	@bash scripts/memory/run.sh search $(if $(TEAM),-team $(TEAM)) "$(Q)"

# 판정이 사람에게 넘긴 질문 / 답하기: make answer KEY=<pairKey> A=latest|old|keep
questions:
	@bash scripts/memory/run.sh questions

answer:
	@bash scripts/memory/run.sh answer "$(KEY)" "$(A)"

# 주간 compaction 1·2단계 리포트 / 5단계 티어 이동 (DRY=1 미리보기)
compaction-report:
	@bash scripts/memory/run.sh compaction-report $(if $(TEAM),-team $(TEAM))

tier:
	@bash scripts/memory/run.sh tier $(if $(TEAM),-team $(TEAM)) $(if $(DRY),-dry-run)

# MCP 서버 (stdio) — .mcp.json 이 이걸 부른다
mcp:
	@bash scripts/mcp/run.sh

build:
	@bash scripts/build/run.sh

test:
	@bash scripts/test/run.sh

# 실제 OpenSearch(make memory-up)에 붙는 e2e + 커버리지
test-e2e:
	@bash scripts/test/run.sh e2e
