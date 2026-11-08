package cache

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aws-systems-performance/internal/dynamolab"
	"aws-systems-performance/internal/httpapi"
	"aws-systems-performance/internal/store"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

type backend struct {
	mu    sync.Mutex
	link  store.Link
	reads atomic.Int64
	gate  chan struct{}
}

func (b *backend) Get(ctx context.Context, code string) (store.Link, error) {
	b.reads.Add(1)
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return store.Link{}, ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if code != b.link.Code {
		return store.Link{}, store.ErrNotFound
	}
	return b.link, nil
}
func (b *backend) Click(_ context.Context, code string) (store.Link, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if code != b.link.Code {
		return store.Link{}, store.ErrNotFound
	}
	b.link.ClickCount++
	return b.link, nil
}
func (b *backend) Create(_ context.Context, code, user, dest string) (store.Link, error) {
	return store.Link{}, errors.New("creation failed")
}
func (b *backend) List(context.Context, string, int) ([]store.Link, error) { return nil, nil }
func (b *backend) Popular(context.Context, int) ([]store.Link, error)      { return nil, nil }
func (b *backend) Health(context.Context) error                            { return nil }
func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDIS_TEST_ADDR to run Redis integration tests")
	}
	c := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func testStore(t *testing.T, ttl time.Duration, coalesce bool) (*Store, *backend, *redis.Client) {
	t.Helper()
	client := redisClient(t)
	b := &backend{link: store.Link{Code: "abc", UserID: "u", DestinationURL: "https://example.com"}}
	c := New(b, client, ttl, fmt.Sprintf("test%d", time.Now().UnixNano()), coalesce)
	key, _ := c.Key("abc")
	t.Cleanup(func() { client.Del(context.Background(), key) })
	return c, b, client
}
func TestKeyIsolation(t *testing.T) {
	a := New(nil, nil, time.Minute, "postgres", false)
	b := New(nil, nil, time.Minute, "dynamodb", false)
	ka, _ := a.Key("abc")
	kb, _ := b.Key("abc")
	if ka == kb {
		t.Fatal("backend keys collide")
	}
	for _, bad := range []string{"", "a:b", "../../../x", string(make([]byte, 100))} {
		if _, ok := a.Key(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestRedisCacheLifecycle(t *testing.T) {
	c, b, client := testStore(t, 100*time.Millisecond, false)
	ctx := context.Background()
	key, _ := c.Key("abc")
	if _, err := c.Get(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if b.reads.Load() != 1 {
		t.Fatal("cache hit read backend")
	}
	time.Sleep(160 * time.Millisecond)
	if _, err := c.Get(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if b.reads.Load() != 2 {
		t.Fatal("TTL did not expire")
	}
	if _, err := c.Click(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if n := client.Exists(ctx, key).Val(); n != 0 {
		t.Fatal("click did not invalidate")
	}
	l, err := c.Get(ctx, "abc")
	if err != nil || l.ClickCount != 1 {
		t.Fatalf("stale after click: %+v %v", l, err)
	}
	if _, err = c.Get(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	missing, _ := c.Key("missing")
	if client.Exists(ctx, missing).Val() != 0 {
		t.Fatal("cached missing record")
	}
	if _, err = c.Create(ctx, "new", "u", "https://example.com"); err == nil {
		t.Fatal("expected failure")
	}
	newKey, _ := c.Key("new")
	if client.Exists(ctx, newKey).Val() != 0 {
		t.Fatal("cached failed create")
	}
}
func TestRedisFailureFallsBack(t *testing.T) {
	b := &backend{link: store.Link{Code: "abc"}}
	c := New(b, redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1}), time.Second, "unavailable", false)
	defer c.redis.Close()
	reg := prometheus.NewRegistry()
	c.RegisterMetrics(reg)
	_, err := c.Get(context.Background(), "abc")
	if err != nil || b.reads.Load() != 1 {
		t.Fatalf("fallback: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "cache_operations_total" {
			for _, metric := range family.Metric {
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["operation"] == "get" && labels["result"] == "error" && metric.GetCounter().GetValue() == 1 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("Redis lookup error not recorded")
	}
}
func TestConcurrentRedirects(t *testing.T) {
	c, b, _ := testStore(t, time.Minute, false)
	h := httpapi.New(c)
	const n = 30
	var wg sync.WaitGroup
	errs := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/u/abc", nil))
			errs <- w.Code
		}()
	}
	wg.Wait()
	close(errs)
	for status := range errs {
		if status != 302 {
			t.Fatal(status)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.link.ClickCount != n {
		t.Fatalf("lost clicks: %d", b.link.ClickCount)
	}
}
func TestCoalescing(t *testing.T) {
	for _, coalesce := range []bool{false, true} {
		t.Run(fmt.Sprint(coalesce), func(t *testing.T) {
			c, b, _ := testStore(t, time.Minute, coalesce)
			b.gate = make(chan struct{})
			const n = 20
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := c.Get(context.Background(), "abc")
					if err != nil {
						t.Error(err)
					}
				}()
			}
			close(start)
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if (!coalesce && b.reads.Load() == n) || (coalesce && b.reads.Load() == 1) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			close(b.gate)
			wg.Wait()
			want := int64(n)
			if coalesce {
				want = 1
			}
			if b.reads.Load() != want {
				t.Fatalf("reads=%d want=%d", b.reads.Load(), want)
			}
		})
	}
}

func TestCacheMetrics(t *testing.T) {
	c, _, _ := testStore(t, time.Minute, false)
	reg := prometheus.NewRegistry()
	c.RegisterMetrics(reg)
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "abc"); err != nil {
			t.Fatal(err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "cache_operations_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, l := range metric.Label {
				labels[l.GetName()] = l.GetValue()
				if l.GetValue() == "abc" {
					t.Fatal("code leaked into labels")
				}
			}
			if labels["operation"] == "get" {
				results[labels["result"]] = metric.GetCounter().GetValue()
			}
		}
	}
	if results["hit"] != 1 || results["miss"] != 1 {
		t.Fatalf("unexpected counters: %v", results)
	}
}

func TestCacheWithPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL and REDIS_TEST_ADDR for PostgreSQL cache integration")
	}
	client := redisClient(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.Postgres{Pool: pool}
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("cachetest%d", time.Now().UnixNano())
	if _, err = db.Create(ctx, code, "cachetest", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, "DELETE FROM links WHERE code=$1", code)
	c := New(db, client, time.Minute, "postgres-test", true)
	key, _ := c.Key(code)
	defer client.Del(ctx, key)
	if _, err = c.Get(ctx, code); err != nil {
		t.Fatal(err)
	}
	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := c.Click(ctx, code); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := db.Get(ctx, code)
	if err != nil || l.ClickCount != n {
		t.Fatalf("count: %+v %v", l, err)
	}
	cached, err := c.Get(ctx, code)
	if err != nil || cached.ClickCount != n {
		t.Fatalf("cached count: %+v %v", cached, err)
	}
}

func TestCacheWithDynamoDB(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_TEST_ENDPOINT and REDIS_TEST_ADDR for DynamoDB cache integration")
	}
	client := redisClient(t)
	t.Setenv("DYNAMODB_ENDPOINT", endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("cache-test-%d", time.Now().UnixNano())
	db, err := dynamolab.New(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer db.Client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	if _, err = db.Create(ctx, "abc", "cachetest", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	c := New(db, client, time.Minute, name, false)
	key, _ := c.Key("abc")
	defer client.Del(context.Background(), key)
	if _, err = c.Get(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Click(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	l, err := c.Get(ctx, "abc")
	if err != nil || l.ClickCount != 1 {
		t.Fatalf("cached count: %+v %v", l, err)
	}
}
