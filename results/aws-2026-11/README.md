# AWS benchmark evidence — November 2026

The JSON files are the k6 summaries from the named original local `benchmark-*` directories. They contain aggregate measurements and synthetic fixture identifiers; host diagnostics, infrastructure addresses, instance identity documents, and full environment configuration are intentionally excluded. The original local folders are retained separately and ignored by Git.

## Environment and workload

Six `t3.small` EC2 instances in `us-east-2`, all in one availability zone: two Go API replicas, three independent PostgreSQL shards, and one infrastructure host running Nginx, Redis, Prometheus, and k6. Both replicas used the same consistent-hashing configuration (128 virtual nodes, three ordered shard IDs, map version `v1`). Redis was running but application caching was disabled. The database contained 1,000 deterministic synthetic links; recorded counts were 352, 323, and 325 per shard.

Each k6 run used the constant-arrival-rate executor for approximately 30 seconds; `rate` is offered requests/second and the JSON records achieved request rate, counts, latency in milliseconds, HTTP failure rate, checks, and dropped iterations. A `null` dropped-iterations value means the metric was not reported by k6, not an independently measured zero. Most runs use a user-list request that fans out to all three shards. The point-read run instead gets a single seeded link from its owning shard. The slow-shard run adds 200 ms to reads on shard 3 in **both API processes**, not to PostgreSQL itself; no delay is enabled in the other runs.

| Curated summary | Original directory | Workload / condition |
| --- | --- | --- |
| `baseline-20.json` | `benchmark-first-run` | Fan-out, 20 offered RPS |
| `slow-shard-20.json` | `benchmark-delayed` | Fan-out, 20 offered RPS, 200 ms injected delay |
| `fanout-40-first.json` | `benchmark-ramp-40` | Fan-out, 40 offered RPS, additional run |
| `fanout-40.json` | `benchmark-rps-40` | Fan-out, 40 offered RPS, charted ramp run |
| `fanout-60.json` | `benchmark-rps-60` | Fan-out, 60 offered RPS |
| `fanout-80.json` | `benchmark-rps-80` | Fan-out, 80 offered RPS |
| `fanout-100.json` | `benchmark-rps-100` | Fan-out, 100 offered RPS |
| `point-50.json` | `benchmark-compare-point` | Single-shard point read, 50 offered RPS |
| `fanout-50.json` | `benchmark-compare-user` | Three-shard fan-out, 50 offered RPS |

The two 40 RPS runs are retained individually. Their broader orchestration windows overlap, so concurrent background activity cannot be ruled out. Only one run exists for most other conditions. API request-counter deltas in the original folders showed both replicas received requests. Run order, warmup and CPU-credit state were not controlled sufficiently to support a capacity or sustained-performance claim.

The point and fan-out requests return different data and do different work, so their comparison does not isolate fan-out overhead. A low observed p99 in these short, single runs is not a reliable tail-latency estimate. The tests did not find a throughput ceiling or evaluate cache performance. The generator shared a host with other services and T3 CPU credits can affect sustained performance.

## Reproduce and regenerate

The infrastructure used for these results was destroyed. A new AWS test requires a reviewed Terraform plan, cost estimate, and explicit deployment and load-test authorization. See [the deployment runbook](../../deploy/aws/README.md) for configuration, seeding, private endpoint, bounded benchmark commands, metadata capture, and teardown. Hardware, placement, cache state, offered rate, duration, dataset, delay, generator capacity, and credit balances must be recorded for a comparable new run.

To regenerate the figures from these committed summaries without AWS:

```sh
python3 -m venv .venv
.venv/bin/pip install -r scripts/requirements-plot.txt
.venv/bin/python scripts/plot_aws_results.py
```

The three PNGs are written to `docs/images/`. They plot individual run summaries, not pooled request distributions.
