---
team: demo
repo: order-api
doc_type: prd
date: 2026-06-15
---
# 주문 API 1차 출시 PRD

## 범위

- 장바구니에서 주문 생성 (`POST /orders`)
- 카드 결제 1종. 간편결제는 2차
- 주문 상태는 `CREATED`, `PAID` 두 가지로 시작한다

## 활성 주문 정의

결제가 끝나고 배송이 시작되지 않은 주문을 활성 주문(ActiveOrder)이라 부른다.
