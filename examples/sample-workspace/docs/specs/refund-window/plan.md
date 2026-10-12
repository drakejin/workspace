---
team: demo
repo: order-api
doc_type: plan
date: 2026-10-11
---
# 환불 가능 기간 14일 확대 plan

## 목표

`RefundWindowDays` 를 7 에서 14 로 바꾼다. 결제 완료 시각 기준이다.

## 작업

1. order-api `RefundPolicy.windowDays` 설정값 변경
2. settlement-batch 정산 보류 기간을 14일로 맞춤
3. e2e: 결제 후 13일째 환불 성공, 15일째 거절

## 확인할 것

- 정산 보류가 늘어나면 판매자 정산일이 밀린다 — 사업 쪽 확인 필요
