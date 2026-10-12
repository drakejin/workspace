# sample-workspace

memory-mcp 를 체험하기 위한 가상의 meta-repository 다. 등장하는 서비스(order-api, catalog-api)와 결정은 모두 예시다.
사용법은 루트의 [GUIDE.md](../../GUIDE.md).

| 문서 | 체험하는 기능 |
| --- | --- |
| `docs/repository/order-api/adr/0001-idempotency-key.md` | 식별자 검색(`IdempotencyKeyHeader`), 에피소드 다섯 줄 |
| `docs/repository/order-api/prd/2026-06-order-launch.md` | 60일 지난 문서 → compaction 리포트 → cold(스냅샷) → restore |
| `docs/feature/payment-retry/2026-08-04-retry-policy.md` · `2026-10-06-retry-policy.md` | 판정 latest-wins — 구판이 기본 검색에서 숨는다 |
| `docs/specs/refund-window/plan.md` | AI plan 문서, `time_range` 필터 |
| `docs/knowledge/order-status.md` · `order-status-v2.md` | knowledge 개정은 `supersedes:` 선언으로만 밀린다 |
| `main/catalog-api/docs/architecture.md` | 관리 대상 repository 의 docs 도 색인된다 (`repo` 필터) |
