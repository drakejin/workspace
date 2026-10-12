# 각 타겟은 scripts/{기능}/run.sh 를 부른다. 사용자 값(Q, TEAM, KEY ...)은 붙여 넣지 않고 환경변수로 넘긴다
# (따옴표·$ 가 들어 있어도 깨지지 않게).
.PHONY: init sync docs-cleaner run-issues done-issue \
	memory-up memory-down daemon daemon-status daemon-service daemon-service-stop reconcile \
	query questions answer compaction-report tier tier-schedule tier-unschedule audit archive \
	graph-query graph-rebuild bench mcp build test test-e2e test-e2e-slow test-e2e-s3

# ── 워크스페이스 ────────────────────────────────────────────────────────────────
init:
	@bash scripts/init/run.sh

sync:
	@bash scripts/sync/run.sh

docs-cleaner:
	@bash scripts/docs-cleaner/run.sh

# make run-issues ARGS="ISSUE-1 books-guitar"
run-issues:
	@bash scripts/run-issues/run.sh $(ARGS)

done-issue:
	@bash scripts/done-issue/run.sh $(ARGS)

# ── 메모리 층 — 평소 쓰는 셋 ──────────────────────────────────────────────────────
#   memory-up      인프라 올리기 — 도구·빌드·스택(OpenSearch + Neo4j)·최초 색인 (멱등)
#   daemon         데몬 실행 — 켜두면 색인·판정·증류·그래프·주간 감사가 알아서 돈다
#   daemon-status  데몬 상태 — 컨테이너·색인·그래프·데몬·판정 질문·할 일
memory-up:
	@bash scripts/memory-up/run.sh

daemon:
	@bash scripts/daemon/run.sh

daemon-status:
	@bash scripts/memory/run.sh status

# ── 상주·스케줄 (macOS launchd) ──────────────────────────────────────────────────
daemon-service:
	@bash scripts/schedule/run.sh install daemon

daemon-service-stop:
	@bash scripts/schedule/run.sh uninstall daemon

tier-schedule:
	@bash scripts/schedule/run.sh install tier

tier-unschedule:
	@bash scripts/schedule/run.sh uninstall tier

# ── 수동 진입점 (데몬이 대신 하는 일, 그리고 사람이 하는 일) ───────────────────────────
memory-down:
	@bash scripts/memory-down/run.sh

# 1회 색인 — 데몬이 켜져 있으면 건너뛴다
reconcile:
	@bash scripts/reconcile/run.sh

# make query Q="검색어" [TEAM=팀 | ORG=org] — 에이전트는 MCP search_memory 로 같은 검색을 한다
query:
	@bash scripts/memory/run.sh search $${TEAM:+-team "$$TEAM"} $${ORG:+-org "$$ORG"} -- "$$Q"

# 판정이 사람에게 넘긴 질문 / 답하기: make answer KEY=<pairKey> A=latest|old|keep
questions:
	@bash scripts/memory/run.sh questions

answer:
	@bash scripts/memory/run.sh answer "$$KEY" "$$A"

# 주간 compaction 1·2단계 리포트 / 5단계 티어 이동 (DRY=1 미리보기)
compaction-report:
	@bash scripts/memory/run.sh compaction-report $${TEAM:+-team "$$TEAM"}

tier:
	@bash scripts/memory/run.sh tier $${TEAM:+-team "$$TEAM"} $$([ "$${DRY:-0}" = 1 ] && echo -dry-run)

# 정합성 감사 (APPLY=1 이면 고칠 수 있는 것을 고친다)
audit:
	@bash scripts/memory/run.sh audit $$([ "$${APPLY:-0}" = 1 ] && echo -apply)

# cold 문서 조치: make archive ACTION=deprecate|undeprecate|purge|edit DOC=<path> BY=<누가> REASON=<왜> [FILE=<새 본문>] [TEAM=]
archive:
	@bash scripts/memory/run.sh archive "$$ACTION" $${TEAM:+-team "$$TEAM"} -by "$$BY" -reason "$$REASON" $${FILE:+-file "$$FILE"} "$$DOC"

# knowledge graph: make graph-query DOC=<path> [DEPTH=2] / make graph-query ENTITY=<name> / make graph-rebuild
graph-query:
	@if [ -n "$$ENTITY" ]; then bash scripts/memory/run.sh graph entity "$$ENTITY"; \
	elif [ -n "$$DOC" ]; then bash scripts/memory/run.sh graph neighbors -depth "$${DEPTH:-2}" "$$DOC"; \
	else bash scripts/memory/run.sh graph stats; fi

graph-rebuild:
	@bash scripts/memory/run.sh graph rebuild

# 발표 데모 4단계 — memory-mcp 유무 비교: make bench QUESTIONS=<meta-repo 밖의 질문 yaml> [MODEL=haiku] [RUNS=1]
bench:
	@bash scripts/memory/run.sh bench $${QUESTIONS:+-questions "$$QUESTIONS"} $${MODEL:+-model "$$MODEL"} $${RUNS:+-runs "$$RUNS"}

# MCP 서버 (stdio) 수동 실행 — .mcp.json 은 scripts/mcp/run.sh 를 직접 부른다
mcp:
	@bash scripts/mcp/run.sh

build:
	@bash scripts/build/run.sh

test:
	@bash scripts/test/run.sh

# 실제 스택(make memory-up)에 붙는 e2e + 커버리지
test-e2e:
	@bash scripts/test/run.sh e2e

test-e2e-slow:
	@bash scripts/test/run.sh slow

test-e2e-s3:
	@bash scripts/test/run.sh s3
