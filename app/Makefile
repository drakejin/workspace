# Go 모듈(app/) 개발용. 루트 Makefile 은 워크스페이스 명령만 둔다.
#   make build          ../.cache/bin/memory-mcp, memory-daemon 빌드
#   make test           단위 테스트 (-race)
#   make test-e2e       실제 스택(루트의 make memory-init)에 붙는 e2e + 커버리지 (../.cache/coverage.out)
#   make test-e2e-slow  ISM 이 실제 시간으로 인덱스를 cold 로 옮기는 e2e (6~15분)
#   make test-e2e-s3    cold 층을 S3(LocalStack)에 두는 e2e — 전용 스택(test/s3)을 올렸다 내린다
.PHONY: build test test-e2e test-e2e-slow test-e2e-s3

build:
	@bash -c 'source ../scripts/memory-common.sh && memory_build "$$(cd .. && pwd)"'
	@echo "[build] ../.cache/bin/memory-mcp, ../.cache/bin/memory-daemon"

test:
	@bash test/run.sh

test-e2e:
	@bash test/run.sh e2e

test-e2e-slow:
	@bash test/run.sh slow

test-e2e-s3:
	@bash test/run.sh s3
