# 5. Sharding
## Question
How much key movement follows a change in shard count?
## Prediction
Modulo hashing may remap many keys; consistent hashing should reduce movement, depending on placement.
## Procedure
Future phase: simulate a fixed key set under modulo, consistent hashing and two-level virtual partitions before and after adding/removing a shard.
## Measurements
Record moved-key fraction, distribution and imbalance using fixed seeds and shard counts.
## Observations
_Not yet measured._
## Explanation
Explain virtual nodes, hot spots, placement indirection and movement tradeoffs.
## Exam-style questions
1. Why does modulo placement move many keys when the shard count changes?
2. How does two-level placement change operational flexibility?
