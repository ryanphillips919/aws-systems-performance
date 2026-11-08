package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/redis/go-redis/v9"
)

type raceBackend struct {
	mu     sync.Mutex
	value  store.Link
	read   chan struct{}
	resume chan struct{}
}

func (b *raceBackend) Get(ctx context.Context, _ string) (store.Link, error) {
	b.mu.Lock()
	v := b.value
	b.mu.Unlock()
	select {
	case b.read <- struct{}{}:
	default:
	}
	select {
	case <-b.resume:
	case <-ctx.Done():
		return store.Link{}, ctx.Err()
	}
	return v, nil
}
func (b *raceBackend) Click(context.Context, string) (store.Link, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.value.ClickCount++
	return b.value, nil
}
func (b *raceBackend) Create(context.Context, string, string, string) (store.Link, error) {
	return store.Link{}, nil
}
func (b *raceBackend) List(context.Context, string, int) ([]store.Link, error) { return nil, nil }
func (b *raceBackend) Popular(context.Context, int) ([]store.Link, error)      { return nil, nil }
func (b *raceBackend) Health(context.Context) error                            { return nil }
func raceDemo(ctx context.Context, r *redis.Client, namespace, code string) any {
	b := &raceBackend{value: store.Link{Code: code, ClickCount: 0}, read: make(chan struct{}, 1), resume: make(chan struct{})}
	c, _ := newCache(b, r, time.Minute, namespace, false)
	key, ok := c.Key(code)
	if !ok {
		log.Fatal("invalid code")
	}
	defer r.Del(context.Background(), key)
	done := make(chan error, 1)
	go func() { _, err := c.Get(ctx, code); done <- err }()
	<-b.read
	updated, err := c.Click(ctx, code)
	if err != nil {
		log.Fatal(err)
	}
	close(b.resume)
	if err := <-done; err != nil {
		log.Fatal(err)
	}
	stale, err := c.Get(ctx, code)
	if err != nil {
		log.Fatal(err)
	}
	return map[string]any{"mode": "race", "backend_click_count": updated.ClickCount, "cached_click_count": stale.ClickCount, "stale": stale.ClickCount != updated.ClickCount, "note": "controlled in-memory source plus real Redis; old fill completes after invalidation"}
}
func readMetrics(base string) (map[string]float64, error) {
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cache_backend_lookups_total ") {
			fields := strings.Fields(line)
			out["backend_lookups"], _ = strconv.ParseFloat(fields[len(fields)-1], 64)
		}
		for _, kind := range []string{"hit", "miss", "error"} {
			if strings.HasPrefix(line, `cache_operations_total{operation="get",result="`+kind+`"} `) {
				fields := strings.Fields(line)
				out[kind], _ = strconv.ParseFloat(fields[len(fields)-1], 64)
			}
		}
	}
	return out, scanner.Err()
}
func latencyDemo(ctx context.Context, r *redis.Client, base, code, state string, n int) any {
	if state != "disabled" && state != "cold" && state != "warm" {
		log.Fatal("state must be disabled, cold or warm")
	}
	if strings.IndexFunc(code, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-')
	}) >= 0 || len(code) > 64 || len(code) == 0 {
		log.Fatal("invalid code")
	}
	resp, err := http.Get(base + "/health")
	if err != nil {
		log.Fatal(err)
	}
	var health struct {
		Storage string `json:"storage"`
	}
	err = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || health.Storage != "postgresql" {
		log.Fatal("latency mode requires a healthy PostgreSQL-backed API")
	}
	metricsBefore, err := readMetrics(base)
	if err != nil {
		log.Fatal(err)
	}
	_, enabled := metricsBefore["backend_lookups"]
	if enabled == (state == "disabled") {
		log.Fatal("API cache configuration does not match requested state")
	}
	target := base + "/links/" + url.PathEscape(code)
	client := &http.Client{Timeout: 5 * time.Second}
	key := "link:v1:postgres:" + code
	if state == "warm" {
		resp, err := client.Get(target)
		if err != nil {
			log.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Fatal("warm-up failed")
		}
		metricsBefore, err = readMetrics(base)
		if err != nil {
			log.Fatal(err)
		}
	}
	values := make([]float64, 0, n)
	failures := 0
	allStart := time.Now()
	for i := 0; i < n; i++ {
		if state == "cold" {
			if err := r.Del(ctx, key).Err(); err != nil {
				log.Fatal(err)
			}
		}
		start := time.Now()
		resp, err := client.Get(target)
		if err != nil {
			failures++
		} else {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				failures++
			}
		}
		values = append(values, float64(time.Since(start).Microseconds())/1000)
	}
	elapsed := time.Since(allStart)
	after, err := readMetrics(base)
	if err != nil {
		log.Fatal(err)
	}
	deltas := map[string]any{}
	for _, k := range []string{"hit", "miss", "error", "backend_lookups"} {
		if !enabled {
			deltas[k] = nil
		} else {
			deltas[k] = after[k] - metricsBefore[k]
		}
	}
	return map[string]any{"mode": "latency", "state": state, "requests": n, "failures": failures, "error_rate": float64(failures) / float64(n), "elapsed_seconds": elapsed.Seconds(), "requests_per_second": float64(n) / elapsed.Seconds(), "p50_ms": percentile(append([]float64(nil), values...), .5), "p95_ms": percentile(append([]float64(nil), values...), .95), "p99_ms": percentile(append([]float64(nil), values...), .99), "cache_metric_deltas": deltas, "note": "single sequential client; throughput includes cold-state DEL preparation; disabled mode has no backend counter"}
}
func infoValue(s, key string) int64 {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, key+":") {
			v, _ := strconv.ParseInt(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), 10, 64)
			return v
		}
	}
	return 0
}
func evictionDemo(ctx context.Context, r *redis.Client, namespace string) any {
	conf, err := r.ConfigGet(ctx, "maxmemory*").Result()
	if err != nil {
		log.Fatal(err)
	}
	max, err := strconv.ParseInt(conf["maxmemory"], 10, 64)
	if err != nil || max <= 0 || conf["maxmemory-policy"] != "allkeys-lru" {
		log.Fatal("eviction demo requires a disposable Redis configured with maxmemory and allkeys-lru")
	}
	prefix := "lab:eviction:" + namespace + ":"
	beforeInfo, err := r.Info(ctx, "stats").Result()
	if err != nil {
		log.Fatal(err)
	}
	expiredBefore := infoValue(beforeInfo, "expired_keys")
	expired := prefix + "expiry"
	if err := r.Set(ctx, expired, "ttl", time.Second).Err(); err != nil {
		log.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	ttlGone := r.Exists(ctx, expired).Val() == 0
	info, err := r.Info(ctx, "stats").Result()
	if err != nil {
		log.Fatal(err)
	}
	before := infoValue(info, "evicted_keys")
	keys := []string{}
	payload := strings.Repeat("x", 4096)
	for i := 0; i < 5000; i++ {
		key := prefix + strconv.Itoa(i)
		if err := r.Set(ctx, key, payload, 5*time.Minute).Err(); err != nil {
			log.Fatal(err)
		}
		keys = append(keys, key)
		if i%50 == 0 {
			info, err = r.Info(ctx, "stats").Result()
			if err != nil {
				log.Fatal(err)
			}
			if infoValue(info, "evicted_keys") > before {
				break
			}
		}
	}
	info, err = r.Info(ctx, "stats").Result()
	if err != nil {
		log.Fatal(err)
	}
	evicted := infoValue(info, "evicted_keys") - before
	missing := 0
	for _, key := range keys {
		if r.Exists(ctx, key).Val() == 0 {
			missing++
		}
	}
	if len(keys) > 0 {
		r.Del(ctx, keys...)
	}
	return map[string]any{"mode": "eviction", "policy": conf["maxmemory-policy"], "maxmemory_bytes": max, "expired_ttl_key": ttlGone, "expired_keys_delta": infoValue(info, "expired_keys") - expiredBefore, "long_ttl_seconds": 300, "keys_written": len(keys), "long_ttl_keys_missing": missing, "evicted_keys_delta": evicted, "note": "Use a dedicated disposable Redis: allkeys-lru can evict any key in that instance."}
}
