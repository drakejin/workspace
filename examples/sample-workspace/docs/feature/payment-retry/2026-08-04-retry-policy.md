---
team: demo
doc_type: spec
date: 2026-08-04
---
# 결제 재시도 정책

## 결정

`PaymentRetryPolicy` 는 결제 대행사 응답이 5xx 이거나 타임아웃이면 3회 재시도한다. 간격은 1분 고정이다.
order-api 와 settlement-batch 가 같은 정책을 쓴다.
