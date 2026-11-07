# 2. Indexing
## Question
How do indexes affect read efficiency and write cost in PostgreSQL and DynamoDB?
## Prediction
A selective indexed lookup should examine less data than a scan; maintaining indexes adds write work.
## Procedure
Future phase: compare PostgreSQL EXPLAIN (ANALYZE, BUFFERS) with and without an index on representative data; compare a DynamoDB Scan with a GSI Query for the same user and dataset, including pagination.
## Measurements
Capture plans, rows examined/returned, latency, write costs and DynamoDB consumed capacity with conditions and dataset size.
## Observations
_Not yet measured._
## Explanation
Relate access paths and selectivity to results; discuss GSI eventual consistency and index maintenance.
## Exam-style questions
1. Why might an index not be chosen for a low-selectivity query?
2. Why can a GSI Query return stale data immediately after a write?
