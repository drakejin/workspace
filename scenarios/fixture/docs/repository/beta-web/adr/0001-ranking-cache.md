---
date: {{D:2}}
topic: search-ranking
---
# ADR-0001 랭킹 캐시

## 결정

beta-web 은 검색 랭킹 결과를 `RankingCacheTTL` 10분으로 캐시한다. (ScnBetaRankingCache)
