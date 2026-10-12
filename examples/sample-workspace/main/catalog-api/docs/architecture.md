---
team: demo
doc_type: note
date: 2026-10-02
---
# catalog-api 구조

상품 정보는 `CatalogItem` 테이블이 정본이고, 검색용 사본은 매시간 `CatalogIndexer` 가 갱신한다.
order-api 는 주문 시점의 가격을 `CatalogItem.price` 에서 읽어 주문에 복사해 둔다 (가격이 바뀌어도 주문 금액은 그대로).
