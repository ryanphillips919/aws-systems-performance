# 2. DynamoDB indexing: Scan vs Query

## Question
How does access-path choice affect the work needed to find one user's links, and what does an index cost?

## Prediction
With a fixed 100-user distribution, user001 owns 1% of the items. As the table grows, a filtered base-table Scan must evaluate the whole table, while a GSI Query evaluates that user's key range. The absolute number of matches still grows with the table. GetItem should address one known primary key, while a filtered code Scan evaluates the table. These are predictions about access paths, not AWS latency or pricing claims.

## Model and concepts

The base table uses `shortCode` as its string partition key, so `GetItem` needs the complete short code. A partition key determines how DynamoDB distributes and locates items; a sort key, when present, orders items within a partition-key value. Here there is no base-table sort key. The `UserLinksIndex` GSI uses `userId` as partition key and fixed-width UTC `createdAt` as sort key. Query requires a specific partition-key value and can order or constrain that sort-key range. Without the GSI, finding an arbitrary user's links requires a Scan because `userId` is not the table key. Equal timestamps have no guaranteed code tie-break in DynamoDB, unlike PostgreSQL.

The GSI projects **ALL** attributes, allowing a user listing to return a complete link without further base-table reads. This increases index storage and writes; in particular every click changes the projected `clickCount` and triggers index maintenance. A KEYS_ONLY or INCLUDE projection can reduce index size and some write work but would require additional base-table reads to return unprojected fields. Querying a GSI cannot fetch unprojected attributes automatically. A global popularity order by `clickCount` is not provided by this user-partitioned index; the API returns 501 in DynamoDB mode rather than hide a full-table scan behind a scalable-looking endpoint.

A GSI may use different partition and sort keys from the base table, and its reads are always eventually consistent. An LSI shares the base partition key, has an alternate sort key, must be created with the table, and supports strong reads; its per-partition-key item collection has additional size constraints. See [AWS secondary index documentation](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/SecondaryIndexes.html).

A Scan reads table items before applying its filter; a filter reduces returned data, not the data evaluated or read capacity. `ScannedCount` is the number evaluated on each page and `Count` is the number returned. Query uses the indexed partition key to select its range, and can also apply a filter after reading. Both can stop at 1 MB and must follow `LastEvaluatedKey` even if a filtered page returns zero items. See [AWS Scan documentation](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Scan.html).

Base-table GetItem supports eventually consistent and strongly consistent reads; our application uses strong reads for lookups. An acknowledged write may be missing from an eventual read, and asynchronous GSI propagation means the index may temporarily lag. A GSI does not support `ConsistentRead=true`. `dynamolab -mode consistency` writes a unique item, compares base eventual/strong reads and an immediate GSI query, then deletes it. DynamoDB Local's timing and replication behavior differ from AWS: an immediate match does not prove GSI strong consistency. [AWS read consistency](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.ReadConsistency.html), [DynamoDB Local differences](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html).

Conditional PutItem prevents duplicate short codes from overwriting links. UpdateItem uses atomic `ADD` plus `attribute_exists` so concurrent clicks are not lost and a missing link is not accidentally created. Transactions are a separate tool for changes that must succeed or fail together across multiple items.

In AWS, reads and writes consume capacity based on item sizes and consistency; indexes add storage and can add write costs and throttling. DynamoDB Local reports simulated consumed-capacity values; do not use them as AWS cost estimates. Hot partition-key values can concentrate traffic and throttle requests even if aggregate capacity looks sufficient. This dataset spreads links across 100 user keys, but a single very active user can still be hot in the GSI.

## Procedure

Start Compose. The experiment uses its own `url-links-experiment` table; `-reset` deletes only this table and its records, then recreates it. DynamoDB Local runs in memory, so restarting that container loses **both** the experiment and DynamoDB application tables.

```sh
docker compose up -d --build
docker compose exec -T api dynamolab -mode seed -count 1000 -reset
docker compose exec -T api dynamolab -mode compare -runs 3 > results.json
docker compose exec -T api dynamolab -mode consistency
```

Use 100, 1,000, or 10,000 items, repeating seed and compare for each. `-user user001`, `-code seed000001`, and `-page-limit 0` are defaults. Set `-page-limit 2` to force multiple pages and inspect pagination; service size limits also apply. Seeding uses batches of at most 25 and retries unprocessed items. Setup is outside timed measurements.

Each comparison obtains the full user result set through Scan and GSI Query and the requested code through Scan and GetItem. It checks equality of all returned fields, failing rather than reporting a misleading comparison if they differ. The table must be stable: concurrent writes, eventual reads, or GSI propagation can legitimately produce mismatches. Query order is newest first; Scan order is unspecified, so equality ignores ordering. Query ordering is tested separately. JSON goes to stdout; brief human-readable counts go to stderr. Each run records elapsed time, pages, items evaluated and returned, and SDK-reported capacity. The GetItem examined value is an indicator of whether the key returned an item, not an AWS ScannedCount field.

## Measurements and observations

A real local smoke measurement is in [dynamodb-local-1000.json](results/dynamodb-local-1000.json): three runs, 1,000 records, 100 users, `user001` and `seed000001`, default page sizes. Each user Scan evaluated 1,000 items and returned 10; each GSI Query evaluated and returned 10. Each code Scan evaluated 1,000 items and returned one; each GetItem found one. Results matched. Each operation used one page at this size; integration tests separately force pagination. A second [10,000-item run](results/dynamodb-local-10000.json) examined 10,000 items to return 100 per user Scan, and 100 to return 100 per Query; the Scans needed two pages. Both datasets used three runs and the same 100-user distribution. The runs used Docker Desktop 4.94.0 in WSL 2, with DynamoDB Local image digest `sha256:6ada631ec7aa5fc83446855eb17233e3dad184fec47477e8140c6c569cfe0c86`; host hardware and competing background load were not captured.

The file contains the measured elapsed times and simulated capacity metadata. These brief sequential runs on Docker Desktop/WSL are a functional illustration, not a production latency comparison, throughput study, or AWS cost measurement. Repeat with recorded machine, version, load, dataset and timing conditions; don't compare local PostgreSQL with remote AWS DynamoDB and attribute all differences to the database.

## Exam-style questions

1. Which keys are required for GetItem on this table? What changes with a composite base key?
2. Design a GSI for listing a user's links in date order. What happens if many links share an identical timestamp?
3. Why does a Scan that returns only ten items potentially consume substantial capacity?
4. Why might a recently created link be absent from a GSI Query?
5. Which reads can be strongly consistent, and can a GSI use `ConsistentRead=true`?
6. Why does this GSI not support an efficient global popularity ranking?
7. What happens when many requests target the same partition key?
8. What extra costs can a GSI introduce, especially when projected click counts change?
9. Why must a client continue when a filtered page contains zero items but has a last evaluated key?
10. How do a conditional put and atomic update protect link creation and clicks?

## Answers

1. The complete `shortCode`; a composite key requires both partition and sort key values.
2. Partition by `userId` and sort by a consistent UTC `createdAt`. Tied GSI sort-key values do not guarantee code order; a unique composite sort value would be needed for deterministic ties.
3. The filter is applied after the Scan reads/evaluates items; capacity depends on data read, not only matches returned.
4. Base-table writes propagate asynchronously to the GSI, whose queries are eventually consistent.
5. Tables and LSIs support strong reads for supported operations; GSIs do not. A strongly consistent Scan is not a snapshot across a changing table.
6. The GSI is partitioned by user and ordered by creation time, not by a global click count. Global ranking needs a different model or fan-out/aggregation.
7. Traffic can concentrate on the key's partition, creating a hot spot and throttling; distributing keys or carefully sharding can help, with extra query complexity.
8. Index storage and write capacity increase; writes changing projected values require maintenance and can contribute to throttling.
9. The limit applies before filtering, so matches may exist on later pages even if this page returned none.
10. A conditional put rejects an existing code, and an atomic update modifies the count without a read-modify-write race; an existence condition prevents creating a missing link.
