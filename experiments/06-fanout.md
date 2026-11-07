# 6. Fan-out
## Question
How does waiting for multiple shards affect end-to-end tail latency?
## Prediction
If all responses are required, larger fan-out tends to increase the chance of a slow overall request.
## Procedure
Future phase: use controlled shard latency distributions, vary shard count and compare independent with correlated delays, using identical timeout policies.
## Measurements
Record shard and overall p50/p95/p99, errors, timeout rate and fan-out width.
## Observations
_Not yet measured._
## Explanation
Explain maximum latency, independence assumptions, correlation and partial-result tradeoffs.
## Exam-style questions
1. If independent shard latencies have CDF F(t), what is the CDF of their maximum across n shards?
2. How do timeouts or partial results change correctness and latency?
