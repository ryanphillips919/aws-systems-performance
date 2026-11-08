// Package cache implements optional, best-effort Redis cache-aside for link reads.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

var validCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Store struct {
	backend      store.Store
	redis        *redis.Client
	ttl          time.Duration
	namespace    string
	coalesce     bool
	group        singleflight.Group
	operations   *prometheus.CounterVec
	latency      *prometheus.HistogramVec
	backendReads prometheus.Counter
}

func New(backend store.Store, client *redis.Client, ttl time.Duration, namespace string, coalesce bool) *Store {
	return &Store{backend: backend, redis: client, ttl: ttl, namespace: namespace, coalesce: coalesce,
		operations:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cache_operations_total", Help: "Redis cache operations by outcome"}, []string{"operation", "result"}),
		latency:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cache_operation_duration_seconds", Help: "Redis operation duration in seconds", Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}}, []string{"operation", "result"}),
		backendReads: prometheus.NewCounter(prometheus.CounterOpts{Name: "cache_backend_lookups_total", Help: "Primary database lookups made by cache-aside"}),
	}
}
func (c *Store) RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(c.operations, c.latency, c.backendReads)
	if instrumented, ok := c.backend.(interface{ RegisterMetrics(prometheus.Registerer) }); ok {
		instrumented.RegisterMetrics(reg)
	}
}
func (c *Store) Key(code string) (string, bool) {
	if !validCode.MatchString(code) {
		return "", false
	}
	return fmt.Sprintf("link:v1:%s:%s", c.namespace, code), true
}
func (c *Store) record(op, result string, start time.Time) {
	c.operations.WithLabelValues(op, result).Inc()
	c.latency.WithLabelValues(op, result).Observe(time.Since(start).Seconds())
}
func (c *Store) lookup(ctx context.Context, key string) (store.Link, bool) {
	start := time.Now()
	data, err := c.redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		c.record("get", "miss", start)
		return store.Link{}, false
	}
	if err != nil {
		c.record("get", "error", start)
		return store.Link{}, false
	}
	var link store.Link
	if err = json.Unmarshal(data, &link); err != nil {
		c.record("get", "error", start)
		return store.Link{}, false
	}
	c.record("get", "hit", start)
	return link, true
}
func (c *Store) fill(ctx context.Context, key string, link store.Link) {
	start := time.Now()
	data, err := json.Marshal(link)
	if err == nil {
		err = c.redis.Set(ctx, key, data, c.ttl).Err()
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	c.record("set", result, start)
}
func (c *Store) invalidate(ctx context.Context, key string) {
	start := time.Now()
	err := c.redis.Del(ctx, key).Err()
	result := "success"
	if err != nil {
		result = "error"
	}
	c.record("delete", result, start)
}
func (c *Store) Get(ctx context.Context, code string) (store.Link, error) {
	key, ok := c.Key(code)
	if !ok {
		c.backendReads.Inc()
		return c.backend.Get(ctx, code)
	}
	if link, hit := c.lookup(ctx, key); hit {
		return link, nil
	}
	load := func() (any, error) {
		c.backendReads.Inc()
		link, err := c.backend.Get(ctx, code)
		if err == nil {
			c.fill(ctx, key, link)
		}
		return link, err
	}
	if !c.coalesce {
		v, err := load()
		if err != nil {
			return store.Link{}, err
		}
		return v.(store.Link), nil
	}
	result := c.group.DoChan(key, load)
	select {
	case <-ctx.Done():
		return store.Link{}, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return store.Link{}, r.Err
		}
		return r.Val.(store.Link), nil
	}
}
func (c *Store) Click(ctx context.Context, code string) (store.Link, error) {
	key, ok := c.Key(code)
	if ok {
		c.invalidate(ctx, key)
	}
	link, err := c.backend.Click(ctx, code)
	if err == nil && ok {
		c.invalidate(ctx, key)
	}
	return link, err
}
func (c *Store) Create(ctx context.Context, code, user, dest string) (store.Link, error) {
	return c.backend.Create(ctx, code, user, dest)
}
func (c *Store) List(ctx context.Context, user string, limit int) ([]store.Link, error) {
	return c.backend.List(ctx, user, limit)
}
func (c *Store) Popular(ctx context.Context, limit int) ([]store.Link, error) {
	return c.backend.Popular(ctx, limit)
}
func (c *Store) Health(ctx context.Context) error { return c.backend.Health(ctx) }

// ShardHealth bypasses optional Redis and reports the authoritative backend.
func (c *Store) ShardHealth(ctx context.Context) []store.ShardHealth {
	if checker, ok := c.backend.(store.ShardHealthChecker); ok {
		return checker.ShardHealth(ctx)
	}
	return nil
}
