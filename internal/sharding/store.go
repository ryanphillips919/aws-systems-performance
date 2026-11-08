package sharding

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
)

type Store struct {
	placement  *Placement
	shards     map[string]store.Store
	operations *prometheus.CounterVec
	durations  *prometheus.HistogramVec
	fanout     *prometheus.HistogramVec
	contacted  *prometheus.HistogramVec
}

func NewStore(p *Placement, shards map[string]store.Store) (*Store, error) {
	if p == nil || len(shards) != len(p.IDs) {
		return nil, fmt.Errorf("placement and shard set must match")
	}
	for _, id := range p.IDs {
		if shards[id] == nil {
			return nil, fmt.Errorf("missing shard %s", id)
		}
	}
	return &Store{placement: p, shards: shards, operations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shard_operations_total", Help: "Operations sent to each shard"}, []string{"shard", "operation", "result"}), durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shard_operation_duration_seconds", Help: "Time spent in each shard operation"}, []string{"shard", "operation"}), fanout: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shard_fanout_duration_seconds", Help: "Time spent on cross-shard operations"}, []string{"operation"}), contacted: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shard_fanout_shards_contacted", Help: "Number of shard calls initiated by a fan-out query", Buckets: []float64{1, 2, 3, 5, 10, 20, 32}}, []string{"operation"})}, nil
}
func (s *Store) RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(s.operations, s.durations, s.fanout, s.contacted)
}
func (s *Store) observe(id, op string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	s.operations.WithLabelValues(id, op, result).Inc()
	s.durations.WithLabelValues(id, op).Observe(time.Since(start).Seconds())
}
func (s *Store) Create(ctx context.Context, code, user, dest string) (store.Link, error) {
	id := s.placement.Owner(code)
	start := time.Now()
	l, err := s.shards[id].Create(ctx, code, user, dest)
	s.observe(id, "create", start, err)
	if err != nil {
		return l, fmt.Errorf("shard %s: %w", id, err)
	}
	return l, nil
}
func (s *Store) Get(ctx context.Context, code string) (store.Link, error) {
	id := s.placement.Owner(code)
	start := time.Now()
	l, err := s.shards[id].Get(ctx, code)
	s.observe(id, "get", start, err)
	if err != nil {
		return l, fmt.Errorf("shard %s: %w", id, err)
	}
	return l, nil
}
func (s *Store) Click(ctx context.Context, code string) (store.Link, error) {
	id := s.placement.Owner(code)
	start := time.Now()
	l, err := s.shards[id].Click(ctx, code)
	s.observe(id, "click", start, err)
	if err != nil {
		return l, fmt.Errorf("shard %s: %w", id, err)
	}
	return l, nil
}
func (s *Store) collect(ctx context.Context, op string, user string, limit int) ([]store.Link, error) {
	start := time.Now()
	var contacted atomic.Int64
	defer func() {
		s.fanout.WithLabelValues(op).Observe(time.Since(start).Seconds())
		s.contacted.WithLabelValues(op).Observe(float64(contacted.Load()))
	}()
	parts := make([][]store.Link, len(s.placement.IDs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, id := range s.placement.IDs {
		i, id := i, id
		g.Go(func() error {
			started := time.Now()
			contacted.Add(1)
			var links []store.Link
			var err error
			if op == "list" {
				links, err = s.shards[id].List(gctx, user, limit)
			} else {
				links, err = s.shards[id].Popular(gctx, limit)
			}
			s.observe(id, op, started, err)
			if err != nil {
				log.Printf("shard %s %s: %v", id, op, err)
				return fmt.Errorf("shard %s %s: %w", id, op, err)
			}
			parts[i] = links
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	all := []store.Link{}
	for _, items := range parts {
		all = append(all, items...)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if op == "list" && !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		if op == "popular" && a.ClickCount != b.ClickCount {
			return a.ClickCount > b.ClickCount
		}
		return a.Code < b.Code
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
func (s *Store) List(ctx context.Context, user string, limit int) ([]store.Link, error) {
	return s.collect(ctx, "list", user, limit)
}
func (s *Store) Popular(ctx context.Context, limit int) ([]store.Link, error) {
	return s.collect(ctx, "popular", "", limit)
}
func (s *Store) Health(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, id := range s.placement.IDs {
		id := id
		g.Go(func() error {
			start := time.Now()
			err := s.shards[id].Health(gctx)
			s.observe(id, "health", start, err)
			if err != nil {
				return fmt.Errorf("shard %s health: %w", id, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// ShardHealth checks every shard independently, even when another fails.
func (s *Store) ShardHealth(ctx context.Context) []store.ShardHealth {
	results := make([]store.ShardHealth, len(s.placement.IDs))
	var wg sync.WaitGroup
	for i, id := range s.placement.IDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			start := time.Now()
			err := s.shards[id].Health(ctx)
			s.observe(id, "health", start, err)
			results[i] = store.ShardHealth{ID: id, Healthy: err == nil}
		}(i, id)
	}
	wg.Wait()
	return results
}
