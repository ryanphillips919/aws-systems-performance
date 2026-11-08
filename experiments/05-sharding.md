# 5. PostgreSQL sharding and key movement

## Question and prediction

How much ownership changes when shards are added or removed, and what does partitioning by short code do to other queries? Modulo placement will often move many keys when the divisor changes. A consistent hash ring should move only keys from the newly covered or removed ring ranges; virtual nodes can improve balance, though they do not guarantee even load. A logical-partition map lets an operator choose which partitions move, but does not move their data by itself.

## Architecture and placement

The Go API can wrap three independent PostgreSQL databases in `ShardedStore`. `Create`, `Get`, and atomic `Click` hash the short code to one owner; the same placement configuration must be used by every API process. Hashing uses the first 64 bits of SHA-256, so results are stable across processes.

- **Modulo:** `hash(code) % number_of_shards`. A changed divisor changes many assignments.
- **Consistent:** A sorted ring contains 128 virtual-node positions per shard by default. A key belongs to the first position clockwise, wrapping to the beginning. Adding or removing a shard changes the ring ranges it owns. More virtual nodes can improve distribution but use more ring entries and do not solve hot keys.
- **Two-level `partition`:** `hash(code) % SHARD_PARTITIONS` selects a fixed logical partition; a versioned table maps that partition to a physical shard. The running service currently generates its map deterministically as `partition % shard_count`; the simulator also shows a **selective** map change. Loading a custom map into the service and physically moving partitions are future work.

Each shard stores its shard ID and placement fingerprint in `shard_placement_metadata`. Startup rejects an incompatible fingerprint or shard identity, and refuses to adopt a populated database without metadata. Changing strategy, shard IDs/order, virtual-node count, partition count, or map version may require migration. This guard detects configuration mismatches; it is not a distributed migration protocol, cannot detect a missing/restored shard that has compatible metadata, and assumes all writers respect it. Never manually edit the metadata to bypass migration.

Because a user's codes can be scattered across shards, user listing fans out to every shard, merges by creation date descending and code ascending, then applies the global limit. Popularity similarly merges per-shard candidates by click count descending and code ascending. Fan-out uses bounded concurrency and propagates cancellation; a slow shard can dominate end-to-end latency, and an unavailable shard causes an error rather than partial results. Health checks all shards. Shard operation and fan-out metrics use fixed shard IDs, not codes or users.

Optional Redis wraps the sharded store. Its namespace includes the placement fingerprint, keeping its cached entries separate from single PostgreSQL and DynamoDB. Hits avoid shard reads; misses route normally. Clicks still update PostgreSQL and invalidate best-effort before and after. An old in-flight fill or failed invalidation can still serve a stale count. Cache keys do not provide a migration or consistency mechanism.

## Logical placement experiment

Run locally, without any database:

```sh
go run ./cmd/shardlab -keys 100000 -seed 42 > sharding-results.json
```

The JSON includes per-shard counts, min/max/average load, maximum relative imbalance (`max / average - 1`), and key movement for adding shard 4 or removing shard 3. It compares modulo and rings with 1, 16, 128, and 512 virtual nodes. For the partition map, the add scenario changes selected logical partitions; removal reassigns partitions owned by the removed shard. Human-readable summaries go to stderr.

[Recorded results](results/sharding-placement-100000.json) used 100,000 deterministic keys, seed 42 and 256 logical partitions. Modulo moved 74.75% of keys on addition and 66.54% on removal. The 128-vnode ring moved 23.20% and 32.16%, respectively. Moving 22 selected partitions to a fourth shard moved 8.48% of keys and left noticeable imbalance; the purpose was to show controlled movement, not optimal balance. These are logical placement results for this dataset and configuration, **not** physical migrations, latency measurements, or universal guarantees.

## Real PostgreSQL demonstration

Start the optional three-shard stack and seed deterministic codes:

```sh
docker compose -f compose.yaml -f compose.shards.yaml up -d --build
docker compose -f compose.yaml -f compose.shards.yaml exec api shardseed -count 1000
curl http://localhost:8000/health
curl http://localhost:8000/links/seed000001
```

`shardseed` adds missing codes and leaves existing records and counts intact. Its codes and user IDs are deterministic, but creation timestamps come from insertion time. It supplies the same `seed000001` code expected by the existing k6 scripts. Select `SHARD_STRATEGY=modulo`, `consistent`, or `partition` **before initializing fresh shard volumes**. The override defaults to consistent placement. An existing populated configuration cannot safely be switched without migration.

Inspect each independent database:

```sh
docker compose -f compose.yaml -f compose.shards.yaml exec shard1 psql -U app -d analytics -c 'SELECT code,user_id,click_count FROM links ORDER BY code LIMIT 10'
docker compose -f compose.yaml -f compose.shards.yaml exec shard2 psql -U app -d analytics -c 'SELECT code,user_id,click_count FROM links ORDER BY code LIMIT 10'
docker compose -f compose.yaml -f compose.shards.yaml exec shard3 psql -U app -d analytics -c 'SELECT code,user_id,click_count FROM links ORDER BY code LIMIT 10'
```

A local integration run created 45 uniquely named links and verified each through the same API, concurrent atomic redirects, globally ordered listings and top-N results, a mismatched-placement rejection, and Redis cache hits and invalidation. One run distributed the test records 14/14/17 across the three shards; its temporary records were removed. A separate 100-link sharded seed produced 36/26/38 records and was retained. This small sample demonstrates routing, not a balance guarantee.

For repeatable integration tests, use disposable shard databases. The test inserts uniquely named records and deletes only its own rows. It also creates and removes a separate schema for metadata safety checks; placement metadata remains in the test shard databases:

```sh
SHARD_TEST_DATABASE_URLS='postgresql://app:app@localhost:5441/analytics,postgresql://app:app@localhost:5442/analytics,postgresql://app:app@localhost:5443/analytics' \
REDIS_TEST_ADDR=localhost:6379 go test -race -count=1 -v ./internal/sharding
```

Adding or removing a server in a real deployment requires provisioning and verifying new owners, copying or moving records, coordinating writes, and a deliberate cutover. This lab implements none of that: no automatic rebalancing, dual writes, distributed transaction, consensus, or zero-downtime migration. Changing ownership without moving data makes records unreachable.

## Exam questions

1. Why can adding a shard to a modulo scheme move keys between two existing shards?
2. When a new physical shard joins a consistent ring, which keys can move?
3. Why do virtual nodes help balance, and why might a hot key remain a problem?
4. How does a fixed logical partition map reduce the need to recompute key-to-partition assignments?
5. What must happen to actual data when an operator changes partition ownership?
6. Why must this application's user listing contact every shard?
7. Why is it sufficient to request the top N from each shard before computing the global top N?
8. How can one slow shard affect p99 latency of a fan-out request?
9. What should the API do if one shard fails during an exact global ranking?
10. Does a Redis hit or singleflight eliminate cross-shard consistency and migration risks?

## Answers

1. Changing the divisor changes the remainder for many keys, not just keys destined for the new server.
2. Keys in ring ranges newly owned by the added shard; unaffected ranges retain their owners.
3. More positions tend to average out uneven ring ranges, but one exceptionally popular key still routes to one owner.
4. Keys stay mapped to stable logical partitions; only chosen partition-to-server entries need to change.
5. The corresponding rows must be copied or moved and writes coordinated before the new mapping is used.
6. The shard key is the code, not the user ID, so one user's links can be on any shard.
7. An item below its own shard's top N cannot be in the global top N because at least N items on that shard rank ahead of it, using the same ordering and tie-break.
8. If the request waits for every shard, its time includes the slowest response and the chance of a slow response grows with fan-out.
9. Fail the operation or explicitly return partial results under a different documented contract; this service fails instead of silently returning an incomplete ranking.
10. No. A cache hit can avoid a database read but can be stale; singleflight is per process, and neither moves data or coordinates placement changes.
