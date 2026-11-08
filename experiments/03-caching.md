# 3. Redis cache-aside experiments

## Concepts and design

A cache-aside read checks Redis for a short code; a **hit** returns a cached link, and a **miss** reads the authoritative PostgreSQL or DynamoDB backend, then fills Redis. Missing database records are not cached. Caching is optional and disabled by default. `link:v1:<backend>:<code>` isolates backend keys; codes are validated and bounded. Redis failures are recorded and reads fall back to the database. A successful create writes the database only; the first later read fills the cache. Failed writes never create cache entries.

A TTL bounds the lifetime of a cache entry from its fill; expiration removes an entry when its time is up. Eviction removes an entry earlier under memory pressure according to a policy. Both cause a subsequent cache miss, but they are different mechanisms. A long TTL can improve hit rate while increasing the duration of a stale value. Redis is not the source of truth: click increments and redirects always update the primary database atomically. A click deletes its cache key before its database update and again after a successful update; database errors do not create cached values. Redis errors during invalidation are recorded but do not undo a successful database update. List and popular operations bypass the cache.

**This does not guarantee freshness.** An in-flight miss can read the old database value, a click can commit and delete the cache key, and then that miss can repopulate Redis with the old value. Failed deletes and other application instances can also leave stale entries until TTL or eviction. A version check or generation counter could mitigate stale fills, but is not implemented. TTL is not a guarantee that values remain fresh during that interval.

A thundering herd occurs when many requests miss a popular key together and all hit the database. Optional `CACHE_COALESCE=true` uses Go `singleflight` to combine overlapping fills for the same key **within one API process**. It is not a distributed lock: separate replicas can each fetch the key. The initiating request's context is used for the shared backend fetch, so its cancellation can fail other waiters. The experiment deliberately adds a fixed database delay to ensure overlapping requests; it is not a normal workload.

Prometheus exports `cache_operations_total{operation,result}`, `cache_operation_duration_seconds`, and `cache_backend_lookups_total`. Labels use fixed operation/result values and never include keys or users. A failed Redis lookup is an `error`, not a `miss`; backend lookups count cache-decorator database reads, not Click, List, or Popular. Metrics exist only when cache is enabled and reset when the process restarts.

## Setup

Requires Docker Compose and PostgreSQL fixtures. Run from the project root:

```sh
docker compose up -d --build
docker compose exec api seed
```

The commands below use the `cachelab` binary included in the API image. It produces JSON on stdout; redirect it to a file to retain measurements. It assumes the API uses PostgreSQL and code `seed000001`. `CACHE_ENABLED`, `CACHE_TTL_SECONDS` and `CACHE_COALESCE` take effect when the API is recreated. The examples restart **only** the API and do not restart the in-memory DynamoDB Local container.

## A. Disabled, cold, and warm reads

**Prediction:** Disabled reads go straight to PostgreSQL. A cold cache adds a Redis miss and fill to the database lookup; a warm hit can avoid the database. The result depends on network, cache overhead and workload.

```sh
CACHE_ENABLED=false docker compose up -d --no-deps api
docker compose exec -T api cachelab -mode latency -state disabled -redis redis:6379 -requests 100 > disabled.json
CACHE_ENABLED=true CACHE_TTL_SECONDS=60 docker compose up -d --no-deps api
docker compose exec -T api cachelab -mode latency -state cold -redis redis:6379 -requests 100 > cold.json
docker compose exec -T api cachelab -mode latency -state warm -redis redis:6379 -requests 100 > warm.json
```

The cold run deletes **only** the PostgreSQL cache key for the test link before every request; the warm run prepopulates it before counting. Each state uses one sequential HTTP client, reports count, failures and error rate, achieved throughput, p50/p95/p99, and deltas of cache hits, misses and cache-driven database lookups. Disabled mode reports those cache counters as null because they do not exist, not zero database reads. The elapsed throughput interval includes cold-run DEL preparation, while request latency does not; do not directly attribute throughput differences solely to the API. These are not saturation tests. Keep other traffic idle because counter deltas cover the whole API instance.

## B. TTL

**Prediction:** A first read misses, a second read before expiry hits, and a read after the one-second TTL misses and fetches again.

```sh
docker compose exec -T api cachelab -mode ttl -redis redis:6379 > ttl.json
```

The utility uses a unique namespace, one-second TTL and a 1.2-second pause. It reports each cumulative hit/miss and backend-read count, then removes its key.

## C. Eviction

**Prediction:** A one-second key expires on its own; separate five-minute keys may disappear early if a small cache reaches its memory limit under `allkeys-lru`.

```sh
docker compose --profile experiments up -d redis-eviction
docker compose --profile experiments run --rm --no-deps --entrypoint cachelab api -mode eviction -redis redis-eviction:6379 > eviction.json
docker compose --profile experiments stop redis-eviction
```

This service is separate from normal Redis, has a 2 MiB limit and no published port or persistence. The utility verifies the limit and policy, reports `expired_keys` and `evicted_keys` changes and counts long-TTL keys missing under pressure. Never point this experiment at a shared Redis instance: `allkeys-lru` can evict any key in that instance. Redis LRU is approximate; exact victims and counts vary.

## D. Thundering herd and request coalescing

**Prediction:** After expiry, concurrent clients may all miss and fetch the database. In one process, singleflight should reduce overlapping database fetches, though it does not necessarily reduce the number of initial Redis misses.

```sh
docker compose exec -T api cachelab -mode herd -redis redis:6379 -concurrency 30 -db-delay 100ms > herd.json
```

The utility populates an isolated key, waits for its TTL to expire, then releases concurrent HTTP requests against an in-process handler. It repeats with coalescing enabled. It reports incoming requests, failures, Redis misses, backend reads, total duration and p50/p95/p99 request latencies. It uses real PostgreSQL reads with an explicit artificial delay; this is designed to expose overlapping misses, not to predict a production speedup or represent multi-instance coordination.

## E. Invalidation race

**Prediction:** Even deleting a key before and after a successful write cannot stop an older in-flight cache fill from writing after the final delete.

```sh
docker compose exec -T api cachelab -mode race -redis redis:6379 > race.json
```

A controlled in-memory primary source and real Redis force this sequence: A reads old state and pauses; B increments the source and invalidates; A resumes and caches the old state. The output compares the current source count and cached count. This isolated teaching demonstration does not modify a PostgreSQL link.

## Observed local smoke results

On Docker Desktop 4.94 / WSL 2, with PostgreSQL and Redis on the same machine, `seed000001`, 100 sequential requests per state, a 60-second TTL and one run per state, [disabled](results/cache-disabled.json), [cold](results/cache-cold.json), and [warm](results/cache-warm.json) completed without failures (0% error rate). The cold run recorded 100 misses and 100 cache-driven backend reads; the warm run recorded 100 hits and zero cache-driven backend reads. Disabled mode has no cache metrics. The JSON files contain measured p50/p95/p99 and throughput. These short single-client samples and sequential ordering do not establish a general speedup; host hardware/background load were not controlled and cold throughput includes DEL work.

The [TTL run](results/cache-ttl.json) observed miss, hit, miss and two backend reads. In the [herd run](results/cache-herd.json), 30 concurrent requests produced 30 misses in both conditions; backend reads were 30 without coalescing and 1 with it, with an artificial 100 ms backend delay. The [race run](results/cache-race.json) observed source count 1 and stale cache count 0. The isolated [eviction run](results/cache-eviction.json) observed one TTL expiry and 20 evictions while long-TTL keys were present. Repeat measurements on your own machine; none of these values describe AWS ElastiCache or production performance.

Restore the default when finished:

```sh
CACHE_ENABLED=false docker compose up -d --no-deps api
```

## Exam questions

1. A cache has a five-minute TTL. Can it return a stale value one second after a write?
2. A key has four minutes of TTL remaining but is absent. How could that happen?
3. Why can one popular key overwhelm a database immediately after expiration?
4. What happens to ordinary reads if Redis is unavailable in this implementation?
5. Why can deleting the key after a write still allow stale data to return?
6. Why does deleting both before and after a write still not guarantee consistency?
7. Why does singleflight not eliminate a herd across three API instances?
8. What does a 95% hit rate imply about database load, and what else must be considered?
9. How does a longer TTL affect hit rate and freshness?
10. Why keep click counts in the database instead of relying on Redis?
11. When could adding Redis increase latency rather than reduce it?
12. Does a cache miss necessarily mean a thundering herd?

## Answers

1. Yes. The database may change immediately after the cached value was filled; TTL only bounds its lifetime.
2. Redis may evict it for memory pressure, or an explicit invalidation may delete it.
3. Many simultaneous misses independently issue the same expensive database read before any fill completes.
4. Redis errors are counted and the read falls back to the database; backend failure can still fail the request.
5. An earlier cache fill can finish after the deletion and put the old value back.
6. A reader can fetch old data before the database write and fill the cache after both deletes.
7. singleflight coordinates only goroutines in one process; each instance may issue its own backend read.
8. Most eligible reads may avoid the database, but writes, uncached operations, misses, failures and traffic volume still matter.
9. A longer TTL can increase hits but makes old entries available for longer without successful invalidation.
10. The database owns the atomic durable update; an independent Redis counter introduces synchronization and loss risks.
11. Misses add Redis round trips, network delays and fills; low reuse or a fast database may erase any benefit.
12. No. A miss is one absent lookup; a herd is a burst of concurrent misses that produces excess backend work.
