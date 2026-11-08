// cachelab runs isolated, repeatable cache demonstrations against local services.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"aws-systems-performance/internal/cache"
	"aws-systems-performance/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

type counted struct {
	store.Store
	reads atomic.Int64
	delay time.Duration
}

func (b *counted) Get(ctx context.Context, code string) (store.Link, error) {
	b.reads.Add(1)
	select {
	case <-time.After(b.delay):
	case <-ctx.Done():
		return store.Link{}, ctx.Err()
	}
	return b.Store.Get(ctx, code)
}
func percentile(values []float64, p float64) float64 {
	sort.Float64s(values)
	i := int(float64(len(values)-1) * p)
	return values[i]
}
func metrics(reg *prometheus.Registry) map[string]float64 {
	out := map[string]float64{}
	families, err := reg.Gather()
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "cache_operations_total" {
			continue
		}
		for _, m := range f.Metric {
			var op, result string
			for _, l := range m.Label {
				if l.GetName() == "operation" {
					op = l.GetValue()
				}
				if l.GetName() == "result" {
					result = l.GetValue()
				}
			}
			out[op+"_"+result] = m.GetCounter().GetValue()
		}
	}
	return out
}
func newCache(b store.Store, r *redis.Client, ttl time.Duration, namespace string, coalesce bool) (*cache.Store, *prometheus.Registry) {
	c := cache.New(b, r, ttl, namespace, coalesce)
	reg := prometheus.NewRegistry()
	c.RegisterMetrics(reg)
	return c, reg
}
func main() {
	mode := flag.String("mode", "herd", "ttl, herd, race, eviction, or latency")
	addr := flag.String("redis", "localhost:6379", "Redis address")
	code := flag.String("code", "seed000001", "existing PostgreSQL short code")
	concurrency := flag.Int("concurrency", 30, "simultaneous herd requests")
	delay := flag.Duration("db-delay", 100*time.Millisecond, "artificial backend delay for herd overlap")
	state := flag.String("state", "warm", "latency mode: disabled, cold, or warm")
	requests := flag.Int("requests", 100, "latency requests")
	base := flag.String("url", "http://localhost:8000", "API base URL")
	flag.Parse()
	if *concurrency < 1 || *requests < 1 || *delay < 0 {
		log.Fatal("invalid concurrency, requests, or delay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r := redis.NewClient(&redis.Options{Addr: *addr, MaxRetries: -1, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	defer r.Close()
	if err := r.Ping(ctx).Err(); err != nil {
		log.Fatal(err)
	}
	namespace := fmt.Sprintf("lab%d", time.Now().UnixNano())
	cleanup := func(c *cache.Store) {
		key, ok := c.Key(*code)
		if ok {
			r.Del(context.Background(), key)
		}
	}
	var result any
	switch *mode {
	case "herd", "ttl":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			dsn = "postgresql://app:app@localhost:5433/analytics"
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()
		db := &store.Postgres{Pool: pool}
		if _, err = db.Get(ctx, *code); err != nil {
			log.Fatalf("seed link required: %v", err)
		}
		if *mode == "ttl" {
			b := &counted{Store: db}
			c, reg := newCache(b, r, time.Second, namespace, false)
			defer cleanup(c)
			snapshots := []map[string]any{}
			for i := 0; i < 3; i++ {
				if i == 2 {
					time.Sleep(1200 * time.Millisecond)
				}
				start := time.Now()
				_, err := c.Get(ctx, *code)
				if err != nil {
					log.Fatal(err)
				}
				snapshots = append(snapshots, map[string]any{"step": i + 1, "elapsed_ms": float64(time.Since(start).Microseconds()) / 1000, "backend_reads": b.reads.Load(), "cache_operations": metrics(reg)})
			}
			result = map[string]any{"mode": "ttl", "ttl_seconds": 1, "steps": snapshots}
		} else {
			runs := []map[string]any{}
			for _, enabled := range []bool{false, true} {
				b := &counted{Store: db, delay: *delay}
				c, reg := newCache(b, r, time.Second, namespace+strconv.FormatBool(enabled), enabled)
				defer cleanup(c)
				if _, err := c.Get(ctx, *code); err != nil {
					log.Fatal(err)
				}
				time.Sleep(1200 * time.Millisecond)
				before := b.reads.Load()
				mBefore := metrics(reg)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					_, err := c.Get(req.Context(), *code)
					if err != nil {
						http.Error(w, "read failed", 500)
						return
					}
					w.WriteHeader(200)
				}))
				start := make(chan struct{})
				var wg sync.WaitGroup
				var mu sync.Mutex
				times := []float64{}
				var failures atomic.Int64
				for i := 0; i < *concurrency; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						ts := time.Now()
						resp, err := http.Get(server.URL)
						elapsed := float64(time.Since(ts).Microseconds()) / 1000
						if err != nil {
							failures.Add(1)
						} else {
							resp.Body.Close()
							if resp.StatusCode != 200 {
								failures.Add(1)
							}
						}
						mu.Lock()
						times = append(times, elapsed)
						mu.Unlock()
					}()
				}
				ts := time.Now()
				close(start)
				wg.Wait()
				total := time.Since(ts)
				server.Close()
				after := metrics(reg)
				runs = append(runs, map[string]any{"coalescing": enabled, "incoming": *concurrency, "errors": failures.Load(), "backend_reads": b.reads.Load() - before, "cache_misses": after["get_miss"] - mBefore["get_miss"], "duration_ms": float64(total.Microseconds()) / 1000, "p50_ms": percentile(append([]float64(nil), times...), .5), "p95_ms": percentile(append([]float64(nil), times...), .95), "p99_ms": percentile(append([]float64(nil), times...), .99)})
			}
			result = map[string]any{"mode": "herd", "artificial_db_delay_ms": delay.Milliseconds(), "note": "single process; delay is deliberately injected to make overlapping misses observable", "runs": runs}
		}
	case "race":
		result = raceDemo(ctx, r, namespace, *code)
	case "eviction":
		result = evictionDemo(ctx, r, namespace)
	case "latency":
		result = latencyDemo(ctx, r, *base, *code, *state, *requests)
	default:
		log.Fatal("unknown mode")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		log.Fatal(err)
	}
}
