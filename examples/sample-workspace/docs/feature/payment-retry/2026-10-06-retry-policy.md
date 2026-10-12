---
team: demo
doc_type: spec
date: 2026-10-06
---
# 결제 재시도 정책

## 결정

`PaymentRetryPolicy` 는 결제 대행사 응답이 5xx 이거나 타임아웃이면 5회 재시도한다. 간격은 지수 백오프(1s, 2s, 4s, 8s, 16s)다.
order-api 와 settlement-batch 가 같은 정책을 쓴다. 멱등 키(ADR-0001)가 있어 재시도 횟수를 늘려도 이중 결제가 나지 않는다.
