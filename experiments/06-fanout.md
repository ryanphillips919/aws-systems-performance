# 6. Fan-out, tail latency, and shard failure

## Question and prediction

How do parallel cross-shard queries behave when one shard is slow or unavailable? A point lookup uses the short code to select one database. A user listing or popularity ranking must contact all three: the placement key does not identify which shards contain a user's links or the global top links. Each shard returns candidates and the API merges them in global order. We predict that a required slow shard delays the whole query and an unavailable shard causes an explicit error.

Serial queries roughly add shard durations; parallel queries take approximately the slowest required shard plus dispatch, merging, and network overhead. The implementation dispatches concurrently and rejects partial results. A point read and a global query also perform different database work and return different amounts of data, so their latency difference cannot be attributed solely to fan-out.

## Verification and measured observations

We used an isolated local Docker Compose project, consistent hashing with 128 virtual nodes, 1,000 deterministic seeded links, and cache disabled except for its verification. Direct SQL counted **352, 323, and 325 links** on shards 1–3. Point reads for seed000006, seed000001, and seed000002 respectively succeeded. User user001 had 10 links; the merged listing and global popularity results matched independent SQL across the shards. Redis produced a cache hit over the sharded backend, and the integration test rejected incompatible placement metadata. See [verification](results/fanout-verification.json).

For the measurements, k6 offered 5 requests/s for 5 seconds per run; point, user, and popular workloads each ran twice per condition, with one mixed run. There were roughly 25–26 requests per run, no request errors, and no dropped iterations. HTTP client timings include more than database work. Both caching and artificial delay were disabled for the baseline. In the slow condition, shard 3 had an additional 200 ms delay for reads, listings, and popularity queries; the selected point lookup was on shard 1.

| Client measurement | Baseline runs | Slow-shard runs |
| --- | --- | --- |
| Point p50 | 1.25, 1.87 ms | 1.76, 1.90 ms |
| User listing p50 | 2.38, 2.32 ms | 202.30, 202.39 ms |
| User listing p95 | 4.21, 4.96 ms | 205.58, 204.51 ms |
| Popularity p50 | See raw JSON | 202.50, 202.18 ms |

Server-side user-listing metrics showed mean fan-out duration of 1.46 ms at baseline and 201.83 ms with injection; the shard 3 mean was 201.68 ms under injection. The mean number of shards contacted was three. These means include the injected delay and database work. They support the expected slowest-shard behavior in this controlled test; they do not prove a general production latency distribution.

![Baseline request percentiles](figures/fanout-latency-percentiles.png)
![Latency before and after slowing shard 3](figures/fanout-slow-shard.png)
![Mean shard and fan-out duration](figures/fanout-shard-vs-overall.png)

The request charts show the **median of two per-run percentiles**, not percentiles computed from pooled requests. The fourth chart compares server-side means, not shard p99s. With only about 25 requests per run, p95 and especially p99 are unstable; use longer runs and repeated trials for serious conclusions. Local scheduling, hardware and background load affect results. JSON files preserve the individual runs, actual achieved throughput, errors, and dropped iterations; a missing k6 dropped-iterations metric is represented as null.

In a separate controlled outage with the cache disabled, stopping shard 3 produced HTTP 200 for a shard 1 point lookup, HTTP 500 for a shard 3 point lookup, and HTTP 500 for both global queries. These are historical results from before the dependency-error changes; the current API returns HTTP 503 for identified connection failures. A shard 3 error metric appeared. After the shard restarted, health, the failed point lookup, and the user listing returned HTTP 200. [Failure results](results/fanout-failure.json).

## Longer benchmark configuration

The original results above are retained as short smoke tests. No longer benchmark measurements were collected for this revision because Docker was unavailable in the implementation environment; there are no new charts or p99 claims. The runner below uses separate point and user workloads, three repetitions per condition, 25 offered requests/s for 60 seconds per run, and a separate 10-second warmup. It keeps cache disabled, pins consistent placement and the same seed keys, records actual per-shard row counts and host details, and restores the baseline API after the injected-delay phase. The dataset must remain unchanged throughout the experiment; use a fresh isolated project and avoid concurrent writes or background load.

After starting and seeding the isolated lab as shown below, run:

~~~sh
python3 scripts/benchmark_fanout_long.py --output my-fanout-long
# Optional bounded configuration:
python3 scripts/benchmark_fanout_long.py --rate 25 --duration 60 --warmup 10 --repetitions 3 --output my-other-fanout-long
~~~

The output directory must be new. Each JSON file preserves the warmup and measured k6 summary, including offered rate, achieved throughput, request counts, average, p50/p95/p99, failures, checks and dropped iterations. Compare the individual run percentiles and their range; these are per-run values, not a pooled request p99. A missing dropped-iterations metric means k6 did not report any. The runner flags drops or throughput below 95% of the offered rate as signs to investigate; neither proves whether the generator or server is responsible. Inspect CPU, errors, and VU capacity on both before increasing load. k6 uses a fixed local Compose target and disables redirects. This local injection demonstrates the effect of one required slow shard, not AWS performance. Existing plotting scripts use only the historical short-run files and do not plot these new trials.

## Probability experiment

If every shard independently meets a threshold with probability p, an all-shards request meets it with probability p^N, ignoring coordination overhead. For p=0.99 and N=20, theory gives about 81.79%. Our seeded simulation gave 82.08% in 100,000 trials. It is simulated independent outcomes, not a measurement of PostgreSQL. Contention and shared failures can correlate shard delays, so the independence assumption may not hold in real deployments.

![Independent probability simulation](figures/fanout-probability.png)

## Reproduce locally

These commands use separate project resources and ports so they do not reset the default stack. Requirements: Docker Compose supporting !override, Python 3, and Go for the tests and simulation. The experiment uses no AWS resources. The deterministic seed adds records without resetting existing ones; use a fresh isolated project for directly comparable counts.

~~~sh
docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml up -d --build
# Retry until healthy:
curl http://localhost:18080/health
docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml exec -T api shardseed -count 1000
python3 scripts/verify_fanout.py
~~~

The verification script checks physical shard counts, one point read per shard, and independent SQL comparisons. To inspect counts directly:

~~~sh
for shard in shard1 shard2 shard3; do
  docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml exec -T "$shard" psql -U app -d analytics -c 'SELECT count(*) FROM links;'
done
~~~

Verify Redis separately and restore the cache-disabled baseline. Retry the verification if the API is still starting.

~~~sh
CACHE_ENABLED=true docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml up -d --no-deps api
python3 scripts/verify_fanout.py
CACHE_ENABLED=false docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml up -d --no-deps api
~~~

Run each workload at least twice, saving distinct output filenames. The example writes a new file rather than overwriting the checked-in measurements. k6 runs inside the Compose network and does not follow external redirects.

~~~sh
docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml run --rm --no-deps \
  -e MODE=user -e RATE=5 -e DURATION=5s k6 run --quiet /benchmarks/fanout.js > my-user-baseline.json
~~~

Set MODE to point, user, popular, or mixed; RATE, DURATION, CODE, and USER_ID are configurable. The default point code belongs to shard 1 with the default consistent placement. Capture server-side metrics after each phase:

~~~sh
python3 scripts/capture_fanout_metrics.py > my-baseline-metrics.json
~~~

Enable an extra 200 ms only on shard 3 and repeat the k6 runs and metrics capture. The API is recreated, resetting its in-process metrics; database volumes are retained. Injection is opt-in, limited to recognized local shard endpoints, bounded to five seconds, and has no public control endpoint.

~~~sh
CACHE_ENABLED=false FANOUT_LAB_ENABLED=true FANOUT_DELAY_SHARD=shard3 FANOUT_DELAY_MS=200 \
  docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml up -d --no-deps api
# Retry health, then repeat k6 runs and capture metrics:
python3 scripts/capture_fanout_metrics.py > my-slow-metrics.json
~~~

Disable injection before the failure experiment. The script stops only shard 3 in the isolated fanout-lab project and attempts to restart it in a finally block. It requires the seeded data and cache-disabled API; wait for health after the API restart. It verifies HTTP 503 responses for the failed shard and fan-out requests, the shard error metric, and recovery.

~~~sh
CACHE_ENABLED=false FANOUT_LAB_ENABLED=false docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml up -d --no-deps api
python3 scripts/fanout_failure.py > my-failure.json
~~~

Run the real-database sharding tests, including placement incompatibility, cancellation and recovery checks. Set REDIS_TEST_ADDR=localhost:6379 if a disposable Redis is available there.

~~~sh
SHARD_TEST_DATABASE_URLS='postgresql://app:app@localhost:15441/analytics,postgresql://app:app@localhost:15442/analytics,postgresql://app:app@localhost:15443/analytics' go test -race -count=1 -v ./internal/sharding
~~~

Reproduce the independent simulation and regenerate the four figures from the checked-in JSON:

~~~sh
go run ./cmd/fanoutlab -p .99 -trials 100000 -seed 42 > my-probability.json
python3 -m venv .venv
.venv/bin/pip install -r scripts/requirements-plot.txt
.venv/bin/python scripts/plot_fanout.py
~~~

The plotting script reads the named checked-in result files; it does not automatically read the my-*.json examples. To stop the isolated services while retaining their volumes:

~~~sh
docker compose -p fanout-lab -f compose.yaml -f compose.shards.yaml -f compose.fanout.yaml stop
~~~

## Exam questions and answers

1. **What is fan-out?** Sending one logical request to multiple downstream services or partitions and combining their responses.
2. **Why do our user listings fan out?** Links are partitioned by short code, so one user's links can reside on any shard.
3. **Why does parallel fan-out still wait for a slow shard?** A complete result needs every required response, so its duration is at least the maximum required response time plus overhead.
4. **How does serial execution differ?** Serial request durations approximately add; parallel execution overlaps them but consumes concurrent resources.
5. **If independent shard completion probability is 0.99, what is it for three required shards?** 0.99^3, approximately 97.03%, before coordination overhead.
6. **What if shard delays are correlated?** The p^N calculation no longer follows from the individual probabilities alone; a shared bottleneck can slow or fail shards together.
7. **What happens if one shard fails?** A lookup routed to a healthy shard can succeed, while complete global queries fail explicitly rather than returning silently incomplete rankings or lists.
8. **Why can't we average shard p99s to obtain request p99?** The request depends on the joint distribution and maximum of shard latencies, plus overhead; individual percentiles discard that joint information.
9. **What do timeouts and partial results trade off?** A timeout bounds waiting but can increase errors; partial results improve availability only if the API clearly communicates incompleteness and the application accepts it.
10. **Why are 25 samples insufficient to characterize p99?** The estimate is dominated by at most the slowest observation and varies substantially between runs.
