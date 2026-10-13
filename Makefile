# 워크스페이스 명령 — 각 타겟은 scripts/{기능}/run.sh 를 부른다. 평소 쓰는 것만 둔다.
#   그 밖의 운영 명령은 스크립트를 직접 부른다:  scripts/memory/run.sh <명령> (검색·판정 질문·tier·audit·archive·graph ...),
#   scripts/memory-daemon/run.sh status|run|once, scripts/run-issues/run.sh, scripts/done-issue/run.sh — README §5
#   Go 개발(build·test·e2e)은 app/Makefile.
.PHONY: init sync memory-init memory-daemon-start memory-daemon-stop

# 관리 대상 repo 를 main/ 에 clone + git hook + INDEX.md 갱신 (멱등)
init:
	@bash scripts/init/run.sh

# main/master 브랜치인 repo 만 pull + INDEX.md 에 새 repo 행
sync:
	@bash scripts/sync/run.sh

# 메모리 층 준비 — 빌드 → OpenSearch·Neo4j 기동 → 템플릿·ISM·스냅샷 repository → 최초 색인 (멱등)
memory-init:
	@bash scripts/memory-init/run.sh

# 데몬 상주 / 중지 (macOS 는 launchd: 로그인 때 다시 켜고 죽으면 다시 띄운다)
memory-daemon-start:
	@bash scripts/memory-daemon/run.sh start

memory-daemon-stop:
	@bash scripts/memory-daemon/run.sh stop
