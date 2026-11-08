package sharding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aws-systems-performance/internal/cache"
	"aws-systems-performance/internal/httpapi"
	"aws-systems-performance/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestRealShardedPostgres(t *testing.T) {
	raw := os.Getenv("SHARD_TEST_DATABASE_URLS")
	if raw == "" {
		t.Skip("set SHARD_TEST_DATABASE_URLS to three disposable PostgreSQL shard databases")
	}
	urls := strings.Split(raw, ",")
	ids := []string{"shard1", "shard2", "shard3"}
	p, err := NewPlacement("consistent", ids, 128, 256, "v1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, closeStore, err := OpenPostgres(ctx, p, urls)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	bad, _ := NewPlacement("modulo", ids, 128, 256, "v1")
	if _, closeBad, err := OpenPostgres(ctx, bad, urls); err == nil {
		closeBad()
		t.Fatal("incompatible placement accepted")
	}
	pools := []*pgxpool.Pool{}
	for _, dsn := range urls {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, pool)
	}
	defer func() {
		for _, pool := range pools {
			pool.Close()
		}
	}()
	user := fmt.Sprintf("shardtest%d", time.Now().UnixNano())
	defer func() {
		for _, pool := range pools {
			if _, err := pool.Exec(context.Background(), "DELETE FROM links WHERE user_id=$1", user); err != nil {
				t.Error(err)
			}
		}
	}()
	codes := []string{}
	distribution := map[string]int{}
	for i := 0; i < 45; i++ {
		code := fmt.Sprintf("%s_%d", user, i)
		if _, err := s.Create(ctx, code, user, "https://example.com"); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, code)
		distribution[p.Owner(code)]++
	}
	for i, id := range ids {
		var actual int
		if err := pools[i].QueryRow(ctx, "SELECT count(*) FROM links WHERE user_id=$1", user).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual == 0 || actual != distribution[id] {
			t.Fatalf("%s count=%d expected=%d", id, actual, distribution[id])
		}
	}
	t.Logf("physical shard distribution: %v", distribution)
	h := httpapi.NewWithBackend
	handler, err := h(s, "", "sharded-postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/links/"+code, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d", code, w.Code)
		}
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/u/"+codes[0], nil))
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for code := range statuses {
		if code != 302 {
			t.Fatal(code)
		}
	}
	l, err := s.Get(ctx, codes[0])
	if err != nil || l.ClickCount != 30 {
		t.Fatalf("clicks=%+v %v", l, err)
	}
	// Give the test's records identical timestamps to verify deterministic tie ordering.
	for _, pool := range pools {
		if _, err := pool.Exec(ctx, "UPDATE links SET created_at='2026-01-01' WHERE user_id=$1", user); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(codes)
	for i := range list {
		if list[i].Code != codes[i] {
			t.Fatalf("global list order: %+v", list)
		}
	}
	// Compare the merged top N with independently gathered per-shard results.
	expected := []store.Link{}
	for _, pool := range pools {
		links, err := (&store.Postgres{Pool: pool}).Popular(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, links...)
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].ClickCount != expected[j].ClickCount {
			return expected[i].ClickCount > expected[j].ClickCount
		}
		return expected[i].Code < expected[j].Code
	})
	if len(expected) > 10 {
		expected = expected[:10]
	}
	got, err := s.Popular(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(expected) {
		t.Fatal("popular length")
	}
	for i := range got {
		if got[i].Code != expected[i].Code {
			t.Fatal("global popular ordering mismatch")
		}
	}
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Log("Redis portion skipped: REDIS_TEST_ADDR not set")
		return
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	cached := cache.New(s, client, time.Minute, "shardtest"+user, false)
	key, _ := cached.Key(codes[0])
	defer client.Del(context.Background(), key)
	handler, err = h(cached, "", "sharded-postgres")
	if err != nil {
		t.Fatal(err)
	}
	getMetric := func() float64 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		prefix := `shard_operations_total{operation="get",result="success",shard="` + p.Owner(codes[0]) + `"} `
		for _, line := range strings.Split(w.Body.String(), "\n") {
			if strings.HasPrefix(line, prefix) {
				value, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
		}
		return 0
	}
	for i := 0; i < 2; i++ {
		before := getMetric()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/links/"+codes[0], nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		after := getMetric()
		if i == 0 && after != before+1 {
			t.Fatalf("cache miss did not reach shard: %v -> %v", before, after)
		}
		if i == 1 && after != before {
			t.Fatalf("cache hit reached shard: %v -> %v", before, after)
		}
	}
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `operation="get",result="hit"`) || !strings.Contains(metrics.Body.String(), "shard_operations_total") {
		t.Fatal("missing cache or shard metrics")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/links/"+codes[0]+"/click", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/links/"+codes[0], nil))
	var gotLink store.Link
	if err := json.Unmarshal(w.Body.Bytes(), &gotLink); err != nil {
		t.Fatal(err)
	}
	expectedLink, err := s.Get(ctx, codes[0])
	if err != nil || gotLink.ClickCount != expectedLink.ClickCount {
		t.Fatalf("cache invalidation %+v %+v %v", gotLink, expectedLink, err)
	}
}

func TestMetadataSafety(t *testing.T) {
	raw := os.Getenv("SHARD_TEST_DATABASE_URLS")
	if raw == "" {
		t.Skip("set SHARD_TEST_DATABASE_URLS to test metadata protection")
	}
	dsn := strings.Split(raw, ",")[0]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("shardmeta_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.Postgres{Pool: pool}
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := NewPlacement("consistent", []string{"shard1", "shard2", "shard3"}, 128, 256, "v1")
	if _, err = db.Create(ctx, "existing", "u", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	if err = VerifyMetadata(ctx, pool, "shard1", p); err == nil {
		t.Fatal("adopted populated unmarked database")
	}
	if _, err = pool.Exec(ctx, "DELETE FROM links WHERE code='existing'"); err != nil {
		t.Fatal(err)
	}
	if err = VerifyMetadata(ctx, pool, "shard1", p); err != nil {
		t.Fatal(err)
	}
	if err = VerifyMetadata(ctx, pool, "shard1", p); err != nil {
		t.Fatal(err)
	}
	bad, _ := NewPlacement("modulo", []string{"shard1", "shard2", "shard3"}, 128, 256, "v1")
	if err = VerifyMetadata(ctx, pool, "shard1", bad); err == nil {
		t.Fatal("incompatible placement accepted")
	}
	if err = VerifyMetadata(ctx, pool, "shard2", p); err == nil {
		t.Fatal("wrong shard identity accepted")
	}
}

func TestRealPlacementStrategies(t *testing.T) {
	raw := os.Getenv("SHARD_TEST_DATABASE_URLS")
	if raw == "" {
		t.Skip("set SHARD_TEST_DATABASE_URLS for real placement strategy tests")
	}
	urls := strings.Split(raw, ",")
	if len(urls) != 3 {
		t.Fatal("expected three URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, strategy := range []string{"modulo", "consistent", "partition"} {
		t.Run(strategy, func(t *testing.T) {
			schema := fmt.Sprintf("shardstrategy_%d", time.Now().UnixNano())
			testURLs := make([]string, 0, 3)
			admins := make([]*pgxpool.Pool, 0, 3)
			defer func() {
				for _, admin := range admins {
					if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
						t.Error(err)
					}
					admin.Close()
				}
			}()
			for _, dsn := range urls {
				admin, err := pgxpool.New(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				admins = append(admins, admin)
				if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				q := u.Query()
				q.Set("search_path", schema)
				u.RawQuery = q.Encode()
				testURLs = append(testURLs, u.String())
			}
			placement, err := NewPlacement(strategy, []string{"shard1", "shard2", "shard3"}, 128, 256, "v1")
			if err != nil {
				t.Fatal(err)
			}
			s, closeStore, err := OpenPostgres(ctx, placement, testURLs)
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore()
			for i := 0; i < 15; i++ {
				code := fmt.Sprintf("key%d", i)
				if _, err := s.Create(ctx, code, "u", "https://example.com"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Get(ctx, code); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
