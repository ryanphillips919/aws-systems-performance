package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgresql://app:app@localhost:5432/analytics"
	}
	count := 1000
	if raw := os.Getenv("SEED_COUNT"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 1 {
			log.Fatal("SEED_COUNT must be a positive integer")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(ctx)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inserted := int64(0)
	for i := 1; i <= count; i++ {
		tag, err := tx.Exec(ctx, `INSERT INTO links (code,user_id,destination_url,created_at) VALUES ($1,$2,$3,$4) ON CONFLICT (code) DO NOTHING`, fmt.Sprintf("seed%06d", i), fmt.Sprintf("user%03d", i%100), fmt.Sprintf("https://example.com/%d", i), start.Add(time.Duration(i)*time.Second))
		if err != nil {
			log.Fatal(err)
		}
		inserted += tag.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("Inserted %d of %d seed rows; existing rows retained", inserted, count)
}
