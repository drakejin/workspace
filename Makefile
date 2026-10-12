.PHONY: init sync workspace docs-cleaner run-issues done-issue memory-up memory-down daemon daemon-status reconcile query mcp

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

# ── 메모리 층 (OpenSearch + reconcile 데몬 + memory-mcp) ─────────────────────────────
# 평소 쓰는 명령은 셋뿐이다 (README "메모리 층 시작하기").
#   memory-up      인프라 올리기 — 도구·스택·최초 색인까지 한 번에 (멱등)
#   daemon         데몬 실행 — 켜두면 색인·판정·증류가 알아서 돈다
#   daemon-status  데몬 상태 — 컨테이너·색인·데몬·판정 질문 점검
# 나머지는 데몬이 대신 하는 일의 수동 진입점이다. 운영 명령은 'pnpm memory' (README "운영 CLI")

memory-up:
	@bash scripts/memory-up/run.sh

daemon:
	@bash scripts/daemon/run.sh

daemon-status:
	@bash scripts/daemon-status/run.sh

memory-down:
	@bash scripts/memory-down/run.sh

# 1회 색인 — 데몬이 켜져 있으면 필요 없다
reconcile:
	@bash scripts/reconcile/run.sh

# make query Q="검색어" [TEAM=팀] — 에이전트는 MCP search_memory 로 같은 검색을 한다
query:
	@bash scripts/query/run.sh "$(Q)" $(if $(TEAM),--team $(TEAM))

# MCP 서버 (stdio) — .mcp.json 이 이걸 부른다
mcp:
	@bash scripts/mcp/run.sh
