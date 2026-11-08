package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aws-systems-performance/internal/cache"
	"aws-systems-performance/internal/sharding"
	"aws-systems-performance/internal/store"
	"github.com/redis/go-redis/v9"
)

type switchShard struct {
	*memoryStore
	down atomic.Bool
}

func (s *switchShard) Health(context.Context) error {
	if s.down.Load() {
		return errors.New("private connection string")
	}
	return nil
}
func (s *switchShard) List(ctx context.Context, user string, n int) ([]store.Link, error) {
	if s.down.Load() {
		return nil, fmt.Errorf("%w: private connection string", store.ErrUnavailable)
	}
	return s.memoryStore.List(ctx, user, n)
}
func (s *switchShard) Get(ctx context.Context, code string) (store.Link, error) {
	if s.down.Load() {
		return store.Link{}, fmt.Errorf("%w: private connection string", store.ErrUnavailable)
	}
	return s.memoryStore.Get(ctx, code)
}

type arbitraryFailure struct {
	*memoryStore
	err error
}

func (s arbitraryFailure) Get(context.Context, string) (store.Link, error) {
	return store.Link{}, s.err
}

func TestHealthAndFailureRecovery(t *testing.T) {
	p, _ := sharding.NewPlacement("modulo", []string{"a", "b", "c"}, 1, 1, "v1")
	shards := map[string]store.Store{}
	switches := map[string]*switchShard{}
	for _, id := range p.IDs {
		s := &switchShard{memoryStore: &memoryStore{links: map[string]store.Link{}}}
		shards[id] = s
		switches[id] = s
	}
	db, _ := sharding.NewStore(p, shards)
	// A Redis client with no server proves health checks never consult it.
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond})
	defer redisClient.Close()
	h := New(cache.New(db, redisClient, time.Minute, "health", false))
	check := func(path string, status int, contents ...string) {
		t.Helper()
		w := request(h, "GET", path, "")
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		for _, s := range contents {
			if !strings.Contains(w.Body.String(), s) {
				t.Fatalf("%s missing %s: %s", path, s, w.Body)
			}
		}
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("secret in response")
		}
	}
	check("/health", 200)
	check("/health/ready", 200, `"id":"b","healthy":true`)
	switches["b"].down.Store(true)
	check("/health/live", 200)
	check("/health", 503)
	check("/health/ready", 503, `"id":"b","healthy":false`)
	check("/health/serving", 200, `"status":"degraded"`)
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	check("/users/test/links", 503, "database unavailable")
	if !strings.Contains(logs.String(), "shard b list") {
		t.Fatalf("missing shard in logs: %s", logs.String())
	}
	check("/metrics", 200, `shard_operations_total{operation="list",result="error",shard="b"} 1`)
	for _, s := range switches {
		s.down.Store(true)
	}
	check("/health/live", 200)
	check("/health/serving", 503)
	for _, s := range switches {
		s.down.Store(false)
	}
	check("/health/ready", 200)
	check("/health/serving", 200, `"status":"ok"`)
	check("/users/test/links", 200)
}

func TestHTTPStorageFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{fmt.Errorf("%w: secret", store.ErrUnavailable), 503}, {errors.New("secret"), 500}, {store.ErrNotFound, 404}, {store.ErrUnsupported, 501}} {
		h := New(arbitraryFailure{&memoryStore{links: map[string]store.Link{}}, tc.err})
		w := request(h, "GET", "/links/key", "")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%v: %d %s", tc.err, w.Code, w.Body)
		}
	}
}
