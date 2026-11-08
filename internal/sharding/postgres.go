package sharding

import (
	"context"
	"fmt"
	"strings"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenPostgres initializes distinct shard databases and rejects mismatched placement.
func OpenPostgres(ctx context.Context, p *Placement, urls []string) (*Store, func(), error) {
	if p == nil || len(urls) != len(p.IDs) {
		return nil, nil, fmt.Errorf("SHARD_DATABASE_URLS must contain one URL per shard ID")
	}
	seen := map[string]bool{}
	for _, url := range urls {
		if strings.TrimSpace(url) == "" || seen[url] {
			return nil, nil, fmt.Errorf("shard database URLs must be nonempty and distinct")
		}
		seen[url] = true
	}
	var pools []*pgxpool.Pool
	closePools := func() {
		for _, pool := range pools {
			pool.Close()
		}
	}
	shards := map[string]store.Store{}
	for i, id := range p.IDs {
		pool, err := pgxpool.New(ctx, urls[i])
		if err != nil {
			closePools()
			return nil, nil, fmt.Errorf("shard %s connection: %w", id, err)
		}
		pools = append(pools, pool)
		db := &store.Postgres{Pool: pool}
		if err := db.Initialize(ctx); err != nil {
			closePools()
			return nil, nil, fmt.Errorf("initialize shard %s: %w", id, err)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = VerifyMetadata(checkCtx, pool, id, p)
		cancel()
		if err != nil {
			closePools()
			return nil, nil, fmt.Errorf("initialize shard %s: %w", id, err)
		}
		shards[id] = db
	}
	result, err := NewStore(p, shards)
	if err != nil {
		closePools()
		return nil, nil, err
	}
	return result, closePools, nil
}
