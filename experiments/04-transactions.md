# 4. Transactions
## Question
Which concurrent update strategies preserve every click?
## Prediction
A read-modify-write without appropriate synchronization can lose updates; atomic updates should not.
## Procedure
Future phase: compare intentionally unsafe read-modify-write with atomic SQL UPDATE and an appropriate transaction/locking approach at controlled concurrency.
## Measurements
Record attempts, successful responses, final count, errors and latency for repeated trials.
## Observations
_Not yet measured._
## Explanation
Connect results to isolation, row locks, atomicity and lost updates.
## Exam-style questions
1. Show an interleaving that loses an increment.
2. When does a multi-statement operation require a transaction beyond a single atomic UPDATE?
