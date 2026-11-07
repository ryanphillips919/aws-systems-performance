# AWS Systems Performance: URL analytics

A local-first lab for Midterm 2: measure latency and throughput, understand database access paths and concurrency, then explore caching and distributed systems. The Go service defaults to **PostgreSQL** and can alternatively use **DynamoDB Local**. Redis is running but unused; AWS deployment is future work. Recorded measurements identify their conditions; local timings do not represent AWS performance.

## Start locally

Requires Docker with Compose. From the repository root:

```sh
docker compose up -d --build
# Wait for the API to start; retry until this returns status ok.
curl http://localhost:8000/health
docker compose exec api seed
curl http://localhost:8000/health
curl http://localhost:8000/links/seed000001
curl http://localhost:8000/users/user001/links
```

The API is at http://localhost:8000, and Prometheus is at http://localhost:9090. PostgreSQL is exposed on localhost port 5433 by default to avoid conflicts with an existing PostgreSQL service; set `POSTGRES_PORT` before starting Compose to choose another host port. Inside Compose it remains on port 5432. The health endpoint checks the configured backend and identifies it. Compose waits for database health before API startup; database initialization occurs at API startup. `docker compose logs -f api` shows startup errors. Stop with `docker compose down`; to delete **all local database data**, use `docker compose down -v`, then start and seed again. This permanently deletes all local database data, including non-seed links.

The seed script creates 1,000 predictable codes (`seed000001` onward), dates and 100 users. It inserts missing rows only, preserving existing click counts and edits; rerunning does not restore changed data. Use `docker compose exec -e SEED_COUNT=10000 api seed` to add a larger fixture. Reset the volume for identical initial state; don't do so if you want to preserve local data.

## API

- `POST /links` with `{"userId":"alice","destinationUrl":"https://example.com"}` creates a link with a random code and returns `shortUrl` along with the link fields.
- `GET /links/{code}` returns JSON without incrementing clicks; `POST /links/{code}/click` explicitly increments and returns it.
- `GET /u/{code}` atomically increments the click count once, then responds with HTTP 302 and the destination in `Location`. The browser follows the redirect; the service does not fetch the destination.
- `GET /users/{userId}/links?limit=50` returns newest first; `GET /analytics/popular?limit=10` returns highest counts first. Limits are 1–100. There is no pagination yet.
- `GET /health` checks the database; `GET /metrics` exposes Prometheus metrics.

## Create and use a short link

```sh
curl -sS -X POST http://localhost:8000/links \
  -H 'Content-Type: application/json' \
  -d '{"userId":"alice","destinationUrl":"https://example.com"}'
```

The response contains `code`, `destinationUrl`, `clickCount`, and `shortUrl`, for example `http://localhost:8000/u/<code>`. Open the returned short URL in a browser to visit the destination. To inspect the redirect without contacting the destination, substitute the returned code here and omit `-L`:

```sh
CODE=replace-with-returned-code
curl -i "http://localhost:8000/u/$CODE"
curl "http://localhost:8000/links/$CODE"
```

The first response has status `302` and a `Location` header; the second returns JSON, including the incremented `clickCount`, and does not itself count as a click. A missing link returns 404. `HEAD` is rejected rather than counted. Each accepted redirect is counted before the response, including requests from bots or retries; it does not prove the browser successfully loaded the destination.

The public origin defaults to `http://localhost:8000`. If clients access the service at another origin, set `PUBLIC_BASE_URL` to that HTTP(S) origin before `docker compose up -d`, for example `PUBLIC_BASE_URL=https://short.example docker compose up -d`. It must not contain credentials, a path, query, or fragment. The application does not derive it from request Host or forwarded headers.

Like the professor's [reference implementation](https://github.com/rstutsman/url-shortener/blob/p1/app.py), this service redirects clients and counts clicks. The reference keeps state in process memory; PostgreSQL stores our links and counts outside the API process so they survive an API restart. This illustrates the P0/P1 distinction between HTTP request handling in a backend process and persistent shared state across process lifetimes. Storage durability still depends on keeping the database volume; `docker compose down -v` deletes it.

## First benchmark

Start and seed as above. Run both from a second terminal, with no other load:

```sh
docker compose run --rm k6 run /benchmarks/open.js
docker compose run --rm k6 run /benchmarks/closed.js
```

Open-loop schedules 50 arrivals/s for 30 seconds; closed-loop uses 10 virtual users with 100 ms think time, each waiting for its response. Override, for example, with `docker compose run --rm -e RATE=100 -e DURATION=1m k6 run /benchmarks/open.js` or `docker compose run --rm -e VUS=20 -e THINK_TIME=0.1 k6 run /benchmarks/closed.js`. Open-loop also accepts `VUS` (preallocated) and `MAX_VUS`; each iteration makes one request. Check `dropped_iterations`: if k6 runs out of VUs it cannot deliver its scheduled load, so actual arrival rate differs from the target. Check status checks and `http_req_failed`, throughput (`http_reqs`/time), and k6 `http_req_duration` p50/p95/p99 in the summary (k6 may require `--summary-trend-stats 'avg,p(50),p(95),p(99),max'` to display all percentiles). Record the complete command, duration, machine, dataset, errors and achieved load in the [performance experiment](experiments/01-performance.md). Repeat runs and vary one factor at a time; this single hot-key workload is deliberately simple and not representative of every application.

Prometheus scrapes every 5 seconds. Example: `sum(rate(http_requests_total{route="/links/{code}"}[1m]))` and `histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket{route="/links/{code}"}[1m])))`. Use a sufficiently long run/window and allow scraping time. Histograms approximate quantiles using buckets; do not average instance p95s. k6 client timings and server middleware timings cover different intervals, so they need not match. Labels use route templates, bounded methods and status codes, never codes or user IDs; `/metrics` is excluded.

## Tests and development

Install Go 1.24 or newer. `go test ./...` runs API tests and skips the PostgreSQL integration test unless a database is configured. `go vet ./...` and `go build ./...` check the project. For real database and concurrent increment tests, start Compose and run:

```sh
TEST_DATABASE_URL=postgresql://app:app@localhost:5433/analytics go test -v ./internal/store
```

Use a disposable database. The test creates uniquely named records and deletes them afterward. `go test -race ./...` additionally checks Go code for data races where supported. The API returns JSON errors and uses HTTP 400 for invalid input, 404 for missing links, 500 for unexpected storage errors, and 503 when the database health check fails.

The Go service lives in `cmd/api`, `internal/httpapi`, and `internal/store`; `cmd/seed` is the fixture utility. The Compose stack, PostgreSQL integration test, and short k6 smoke runs have been checked locally.

## Design and study notes

A small Go storage interface separates the API from SQL; PostgreSQL is the default; DynamoDB Local is the second implementation. `pgxpool` reuses connections, while each operation applies a five-second database deadline and propagates request cancellation. Pool sizing and connection contention can affect benchmark results. The primary key supports code lookup, `(user_id, created_at DESC, code)` supports per-user ordered retrieval, and `(click_count DESC, code)` supports popularity. Indexes speed reads but consume space and make writes more expensive, particularly updating the popularity index on every click. Stable code tie-breakers make equal values deterministic. The single SQL `UPDATE ... click_count + 1 RETURNING` avoids the lost-update race of separate reads and writes. Each single-statement update is atomic; operations requiring several statements must use an explicit transaction. Startup DDL is adequate for a lab, not a migration system.

Throughput is completed requests per unit time; latency is per-request time. Increasing offered load can produce queues, saturation and sharply rising tail latency. Open-loop schedules arrivals independent of response time until the generator hits its VU limit; closed-loop users wait for each response, so offered load falls as latency rises. This can conceal overload. A percentile describes a distribution, not a maximum; select an SLO threshold and measure its violation rate over a defined window. Fan-out can amplify tail latency because a request waits for multiple responses. The [six experiment templates](experiments/) contain predictions, procedures and exam questions. Do not claim observed effects until measured.

The DynamoDB table uses `shortCode` as its partition key and a `userId` / `createdAt` GSI. A GSI Query accesses a key range while Scan reads broadly; measure consumed capacity as well as latency. GSI reads are eventually consistent, index writes cost capacity/storage, and a global popularity ranking requires a separate design. Redis lookaside, TTL, invalidation and herd control are later experiments, as are sharding and fan-out.

## DynamoDB Local and indexing lab

```mermaid
flowchart LR
    API[Go API] -->|postgres default| PG[(PostgreSQL)]
    API -->|dynamodb option| DDB[(DynamoDB Local base table)]
    DDB -. maintains .-> GSI[UserLinksIndex GSI]
    API -->|user Query| GSI
```

`docker compose up -d --build` starts both local databases; the API defaults to PostgreSQL. To use the DynamoDB API backend:

```sh
STORAGE_BACKEND=dynamodb docker compose up -d api
curl http://localhost:8000/health
```

Return to PostgreSQL with `STORAGE_BACKEND=postgres docker compose up -d api`. The `seed` command and existing k6 workloads use PostgreSQL fixtures by default; selecting DynamoDB does not copy those fixtures into its application table. DynamoDB Local is **in-memory**: restarting or recreating its container deletes all DynamoDB tables, including application links and experiment data. PostgreSQL uses a separate persistent volume. The local-only SDK client uses dummy credentials and rejects endpoints other than localhost or the Compose `dynamodb` host; this phase cannot access real AWS through that client.

The base table has `shortCode` as partition key. `UserLinksIndex` uses `userId` and fixed-width UTC `createdAt` as partition/sort keys with an ALL projection. The GSI supports the user listing, but results can be stale and ties on creation time have no deterministic code ordering. Direct lookups use strong reads. Click updates are atomic and duplicate codes cannot overwrite records. Global popularity returns HTTP 501 in DynamoDB mode: neither the base key nor this user-partitioned GSI supports an efficient global ranking. [The indexing guide](experiments/02-indexing.md) explains projection/write costs, consistency, key choices, pagination, and exam questions.

Run the isolated, deterministic experiment (reset deletes only its experiment table):

```sh
docker compose exec -T api dynamolab -mode seed -count 1000 -reset
docker compose exec -T api dynamolab -mode compare -runs 3 > results.json
docker compose exec -T api dynamolab -mode consistency
```

Try 100, 1,000 and 10,000 records. The comparison measures complete Scan versus GSI Query for `user001`, and Scan versus GetItem for `seed000001`; it checks equivalent results and reports examined and returned counts, pages, elapsed time, and SDK capacity metadata. `-page-limit 2` forces pagination. JSON goes to stdout and a readable summary to stderr. [A measured 1,000-item local example](experiments/results/dynamodb-local-1000.json) is included. Local timing and simulated capacity do not predict AWS performance or bills; real GSI replication lag is also not reproduced reliably.

To run real local integration tests (isolated temporary tables):

```sh
DYNAMODB_TEST_ENDPOINT=http://localhost:8001 go test -count=1 -v ./internal/dynamolab
```

## AWS cost and safety

Local only by default. The [Terraform scaffold](terraform/) creates nothing. Budget is $20 maximum, preferably under $5; obtain explicit permission before creating any AWS resources or running `terraform apply`. Avoid NAT gateways and expensive managed services; estimate costs before deployment and verify teardown and billing afterward. Never commit credentials, secrets, or Terraform state. Local Compose passwords are disposable development values only.
