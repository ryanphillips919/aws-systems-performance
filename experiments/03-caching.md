# 3. Caching
## Question
How do hit rate, TTL and invalidation affect latency and freshness?
## Prediction
A warm cache may reduce database work; longer TTLs can increase staleness.
## Procedure
Future phase: implement Redis lookaside, compare cold/warm traffic, vary TTL, perform updates and concurrent cold-key requests.
## Measurements
Record hit/miss rate, latency distribution, backend requests, stale reads, eviction and load after expiry.
## Observations
_Not yet measured._
## Explanation
Explain cache-aside ordering, TTL, invalidation races, eviction and thundering herds.
## Exam-style questions
1. Why can TTL alone fail a strict freshness requirement?
2. How can a thundering herd be mitigated and what tradeoffs result?
