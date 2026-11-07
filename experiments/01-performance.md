# 1. Performance: open-loop vs closed-loop

## Question
How do latency, throughput, and errors change as offered load approaches saturation, and how do load models affect what we observe?
## Prediction
At sufficient load queues and tail latency will grow; closed-loop throughput may flatten because users wait. This is a hypothesis, not a result.
## Procedure
Seed the same dataset. Run `open.js` at increasing RATE values and `closed.js` at increasing VUS, holding duration and environment fixed. Repeat, allow recovery, and note generator CPU and dropped iterations. See README for commands.
## Measurements
Record date, hardware, versions, dataset, commands, target and achieved rates, dropped iterations, errors, p50/p95/p99, CPU, and any SLO threshold and violation rate.
## Observations
_Not yet measured._
## Explanation
Explain queueing, the throughput knee, histogram approximations, and coordinated omission/load-generator limits in terms of actual observations.
## Exam-style questions
1. Why can a closed-loop benchmark hide saturation?
2. What can p99 reveal that mean latency cannot, and how would you check an SLO?
