---
date: {{D:2}}
topic: payments
---
# ADR-0001 원장 행 잠금

## 결정

alpha-api 는 잔액을 바꿀 때 `LedgerRowLock` 으로 원장 행을 `SELECT ... FOR UPDATE` 잠근다. (ScnAlphaLedgerLock)

## 대안

- 낙관적 버전 컬럼: 충돌이 잦은 마감 시간대에 재시도가 폭증했다.
