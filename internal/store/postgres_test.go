package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &Postgres{Pool: pool}
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := "test-user-" + suffix
	codes := []string{"test-a-" + suffix, "test-b-" + suffix, "test-c-" + suffix}
	defer func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM links WHERE user_id=$1", user); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	for _, code := range codes {
		if _, err := db.Create(ctx, code, user, "https://example.com/"); err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, "UPDATE links SET created_at='2026-01-01' WHERE user_id=$1", user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, "UPDATE links SET created_at='2026-01-02' WHERE code=$1", codes[2])
	if err != nil {
		t.Fatal(err)
	}
	links, err := db.List(ctx, user, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 || links[0].Code != codes[2] || links[1].Code != codes[0] || links[2].Code != codes[1] {
		t.Fatalf("unexpected order: %+v", links)
	}
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := db.Click(ctx, codes[0]); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	link, err := db.Get(ctx, codes[0])
	if err != nil || link.ClickCount != n {
		t.Fatalf("click count: %+v, %v", link, err)
	}
	if _, err := db.Get(ctx, "missing-"+suffix); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := db.Click(ctx, "missing-"+suffix); err != ErrNotFound {
		t.Fatalf("missing click: %v", err)
	}
}
