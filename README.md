# AWS Systems Performance

A Go URL shortener built to explore how backend systems behave under load.

This project uses a working URL-shortening service as a testbed for database access patterns, caching, concurrency, and performance measurement. It runs locally with Docker Compose and supports PostgreSQL, a three-shard PostgreSQL mode, an alternative DynamoDB Local backend, optional Redis caching, Prometheus metrics, and k6 load testing.

The focus is on reproducible experiments and understanding the tradeoffs behind architectural decisions, rather than on a particular throughput number.

## Features

- **Working short links:** Create links, follow HTTP 302 redirects, track clicks, and browse links in the Link Lab interface.
- **Interchangeable storage:** PostgreSQL by default, sharded PostgreSQL, or DynamoDB Local with a Global Secondary Index for user-specific queries.
- **Optional Redis cache:** Cache-aside reads with configurable TTL, hit/miss metrics, and experiments around invalidation and concurrent misses.
- **Performance instrumentation:** Prometheus counters and latency histograms, plus k6 open-loop and closed-loop workloads.
- **Repeatable experiments:** Scripts, observations, and study notes covering database indexing, caching, concurrency, and performance.

## Architecture

```mermaid
flowchart TD
    Client[Browser or k6] --> API[Go HTTP API]
    API -->|link-detail lookup, if enabled| Cache[(Redis cache)]
    Cache -->|hit| API
    API -->|cache miss, disabled, or error| Storage{Selected backend}
    API -->|redirect or click: atomic update| Storage
    Storage --> PG[(PostgreSQL)]
    Storage --> DDB[(DynamoDB Local)]
    Storage --> Shards[(Three PostgreSQL shards)]
    Storage -->|link read result| API
    API -->|cache fill with TTL| Cache
    DDB -. maintains .-> GSI[UserLinksIndex GSI]
    Prometheus[Prometheus] -->|scrapes| API
```

The databases are alternative backends, not synchronized copies. PostgreSQL is the default and is the persistent source of truth for normal local use. DynamoDB Local is available to explore key-value access patterns and secondary indexes. Redis can avoid database reads for `GET /links/{code}` when enabled; redirects and click operations still update the authoritative database atomically.

## Quick start

Requirements: Docker and Docker Compose. Go 1.24+ is needed only to run Go tooling directly on your machine.

```sh
docker compose up -d --build
# Wait for the API to start; retry until the health check succeeds.
curl http://localhost:8000/health
docker compose exec api seed
```

Open [Link Lab](http://localhost:8000) to create and browse short links. After seeding, try user ID `user001` to explore the sample data. The seed utility adds missing rows without resetting existing links or click counts.

| Local service | Address |
| --- | --- |
| Link Lab / HTTP API | [http://localhost:8000](http://localhost:8000) |
| Prometheus | [http://localhost:9090](http://localhost:9090) |
| PostgreSQL | `localhost:5433` |
| DynamoDB Local | `localhost:8001` |
| Redis | `localhost:6379` |

The database, cache, and DynamoDB ports are bound to localhost for development.

## Create a link

```sh
curl -sS -X POST http://localhost:8000/links \
  -H 'Content-Type: application/json' \
  -d '{"userId":"alice","destinationUrl":"https://example.com"}'
```

The response includes a generated `code` and `shortUrl`. To inspect a redirect without contacting the destination website:

```sh
CODE=replace-with-returned-code
curl -i "http://localhost:8000/u/$CODE"
curl "http://localhost:8000/links/$CODE"
```

The first request returns HTTP 302 with a `Location` header and increments the click count. The second returns the link as JSON without counting another click. With caching enabled, link details can be stale. A counted redirect means the shortener issued a redirect, not necessarily that the destination page loaded.

## Main API endpoints

| Endpoint | Purpose |
| --- | --- |
| `POST /links` | Create a short link |
| `GET /u/{code}` | Redirect to the destination and count a click |
| `GET /links/{code}` | Retrieve link details without counting a click |
| `POST /links/{code}/click` | Explicitly increment a link's click count |
| `GET /users/{userId}/links` | List a user's links, newest first |
| `GET /analytics/popular` | List popular links (PostgreSQL backend) |
| `GET /health` | Check service and selected database health |
| `GET /metrics` | Expose Prometheus metrics |

## Explore the experiments

### 1. Load testing and latency

The k6 workloads contrast fixed-rate request arrivals with a fixed number of virtual users. Measure achieved throughput, request failures, dropped iterations, and latency percentiles.

```sh
docker compose run --rm k6 run /benchmarks/open.js
docker compose run --rm k6 run /benchmarks/closed.js
```

The default open-loop test schedules 50 requests per second for 30 seconds. The default closed-loop test uses 10 virtual users with a 100 ms think time. Both initially target a single seeded link, so results describe that workload rather than general capacity.

See [Performance experiments](experiments/01-performance.md).

### 2. DynamoDB: Scan vs. Query

The DynamoDB table uses `shortCode` as its partition key. A `UserLinksIndex` GSI uses `userId` and `createdAt` to support listing a user's links in timestamp order. The experiment compares filtered scans with key-based queries and point lookups.

```sh
docker compose exec -T api dynamolab -mode seed -count 1000 -reset
docker compose exec -T api dynamolab -mode compare -runs 5 -warmups 1 -seed 1 > results.json
```

The experiment uses its own table, separate from application links; `-reset` deletes and recreates that table. Recorded local results are available for [1,000 items](experiments/results/dynamodb-local-1000.json) and [10,000 items](experiments/results/dynamodb-local-10000.json). DynamoDB Local's timing, capacity reporting, and consistency behavior should not be treated as production AWS measurements.

See [DynamoDB indexing](experiments/02-indexing.md).

### 3. Redis: cache-aside and consistency

Caching is off by default. Enable it for link-detail reads:

```sh
CACHE_ENABLED=true CACHE_TTL_SECONDS=60 docker compose up -d --no-deps api
```

Try a cold read, a warm read, and a read after the TTL expires. The caching experiments also explore eviction, cache invalidation races, and concurrent misses. `CACHE_COALESCE=true` optionally coalesces overlapping cache misses within one API process, not across instances.

Redis is an optimization, not a durability or correctness layer. If Redis is unavailable, ordinary lookups fall back to the database. Best-effort invalidation cannot eliminate every stale-read race. To disable caching again:

```sh
CACHE_ENABLED=false docker compose up -d --no-deps api
```

See [Caching experiments](experiments/03-caching.md) for commands and measured local results.

### 4. PostgreSQL sharding and consistent hashing

The sharded backend routes a short code to one of three independent PostgreSQL databases. User listings and popularity rankings fan out to all shards and merge the results; failures return an error rather than partial data. Redis can wrap the sharded backend using a separate placement-specific key namespace.

```mermaid
flowchart LR
    C[Client] --> A[Go API]
    A -->|optional link read| R[(Redis)]
    A --> S[Sharded store]
    S --> P1[(PostgreSQL shard 1)]
    S --> P2[(PostgreSQL shard 2)]
    S --> P3[(PostgreSQL shard 3)]
```

Start the optional stack and seed it without resetting existing data:

```sh
docker compose -f compose.yaml -f compose.shards.yaml up -d --build
docker compose -f compose.yaml -f compose.shards.yaml exec api shardseed -count 1000
curl http://localhost:8000/health
```

`SHARD_STRATEGY` supports `modulo`, `consistent` (default), and `partition`. The override supplies three database URLs and shard IDs; `SHARD_VNODES` defaults to 128, `SHARD_PARTITIONS` to 256, and `SHARD_MAP_VERSION` to `v1`. The partition map is currently generated deterministically at startup. Placement metadata on every shard rejects incompatible settings and populated, unmarked databases. **Changing placement does not migrate data**: use fresh shard databases or perform an explicit migration before changing configuration. Do not bypass the metadata check.

Compare logical ownership and load distribution without modifying databases:

```sh
go run ./cmd/shardlab -keys 100000 -seed 42 > sharding-results.json
```

The [sharding guide](experiments/05-sharding.md) includes results, SQL inspection commands, an integration test, fan-out behavior, and migration limitations. The simulation measures ownership changes, not completed data transfers.

To return the API to its default single-PostgreSQL backend, run `docker compose up -d --build api`. The shard containers and volumes remain separate; no rows are automatically migrated or deleted. The k6 scripts work in sharded mode after running `shardseed`.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_BACKEND` | `postgres` | Choose `postgres`, `sharded-postgres`, or `dynamodb` |
| `CACHE_ENABLED` | `false` | Enable Redis caching for link-detail reads |
| `CACHE_TTL_SECONDS` | `60` | Lifetime of cached link records |
| `CACHE_COALESCE` | `false` | Coalesce concurrent misses within one process |
| `PUBLIC_BASE_URL` | `http://localhost:8000` | Origin used when generating short URLs |
| `POSTGRES_PORT` | `5433` | PostgreSQL host port |
| `SHARD_IDS` | `shard1,shard2,shard3` | Ordered, stable shard identifiers |
| `SHARD_DATABASE_URLS` | Set by `compose.shards.yaml` | Comma-separated PostgreSQL URLs in ID order |
| `SHARD_STRATEGY` | `consistent` | Choose `modulo`, `consistent`, or `partition` |
| `SHARD_VNODES` | `128` | Virtual nodes per shard on the ring |
| `SHARD_PARTITIONS` | `256` | Number of logical partitions |
| `SHARD_MAP_VERSION` | `v1` | Version of the generated partition map |

To run the API using DynamoDB Local:

```sh
STORAGE_BACKEND=dynamodb docker compose up -d api
curl http://localhost:8000/health
```

The PostgreSQL seed utility does not populate DynamoDB's application table. DynamoDB Local runs in memory, so restarting or recreating its container discards its tables and data. Switching back to PostgreSQL does not copy data between the backends. The DynamoDB client is restricted to local endpoints and uses dummy credentials.

## Observability

The Go service exposes request counts and latency histograms at `/metrics`. When caching is enabled, additional counters track hits, misses, errors, and database lookups through the cache layer.

Example PromQL queries:

```promql
sum(rate(http_requests_total[1m]))
```

```promql
histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket[1m])))
```

```promql
sum(rate(cache_operations_total{operation="get",result="hit"}[1m]))
/
sum(rate(cache_operations_total{operation="get",result=~"hit|miss"}[1m]))
```

In sharded mode, `shard_operations_total`, `shard_operation_duration_seconds`, `shard_fanout_duration_seconds` expose per-shard operations and fan-out duration with bounded labels. Cache hits avoid shard reads.

The histogram-derived percentile is approximate. Client-side k6 latency and server-side HTTP latency cover different parts of a request, so they may differ. Cache metrics appear only when caching is enabled; the hit ratio excludes Redis errors and is undefined when there are no hits or misses.

## Tests and development

```sh
go test ./...
go vet ./...
go build ./...
```

Integration tests need local services. With Docker Compose running:

```sh
TEST_DATABASE_URL=postgresql://app:app@localhost:5433/analytics go test -v ./internal/store
DYNAMODB_TEST_ENDPOINT=http://localhost:8001 go test -count=1 -v ./internal/dynamolab
REDIS_TEST_ADDR=localhost:6379 TEST_DATABASE_URL=postgresql://app:app@localhost:5433/analytics DYNAMODB_TEST_ENDPOINT=http://localhost:8001 go test -count=1 -v ./internal/cache
```

Tests requiring external services are skipped when their environment variables are not set. Integration tests should use disposable development data.

## Design decisions and limitations

- **Explicit database access patterns:** PostgreSQL supports a global popularity query with an index. The current DynamoDB key design does not; `/analytics/popular` returns 501 in DynamoDB mode rather than disguising a table scan as a scalable implementation.
- **Atomic click accounting:** PostgreSQL uses an atomic SQL update; DynamoDB uses a conditional atomic update. Redirects count before sending their HTTP response.
- **Caching tradeoffs:** Link details may be stale after concurrent writes or cache fills. The database remains authoritative.
- **Local-first environment:** This repository runs with Docker Compose. Its Terraform directory is currently a scaffold, not a deployed AWS environment.
- **Benchmark interpretation:** Results depend on dataset, workload, machine, and caching conditions. Measurements from a local test are not claims about production scalability.

## Next steps

Planned work includes fan-out and tail-latency experiments, richer benchmark reports, and a possible short-lived AWS deployment with multiple application instances, RDS, and shared caching. Any AWS deployment requires separate approval and a cost estimate within the project's $20 maximum budget, preferably below $5.

## Background

This project started as a practical way to reinforce concepts from a Scalable Software Systems course, including material for a midterm. It has since become a place to explore those ideas through working code, controlled experiments, and measurable results. The goal is to understand not only how to build a service, but how it behaves as workloads and architecture change.

## Cleanup

```sh
docker compose down
```

This stops the services and removes the in-memory DynamoDB Local data. To also remove all local PostgreSQL data, use `docker compose down -v`; this permanently deletes the database volume.
