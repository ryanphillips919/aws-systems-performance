package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"aws-systems-performance/internal/cache"
	"aws-systems-performance/internal/dynamolab"
	"aws-systems-performance/internal/httpapi"
	"aws-systems-performance/internal/sharding"
	"aws-systems-performance/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func backendName(raw string) (string, error) {
	if raw == "" {
		return "postgres", nil
	}
	if raw == "postgres" || raw == "dynamodb" || raw == "sharded-postgres" {
		return raw, nil
	}
	return "", fmt.Errorf("unknown STORAGE_BACKEND %q (choose postgres, dynamodb, or sharded-postgres)", raw)
}

func boolEnv(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return v, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend, err := backendName(os.Getenv("STORAGE_BACKEND"))
	if err != nil {
		log.Fatal(err)
	}
	var db store.Store
	cacheNamespace := backend
	switch backend {
	case "postgres":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			dsn = "postgresql://app:app@localhost:5432/analytics"
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer pool.Close()
		p := &store.Postgres{Pool: pool}
		if err := p.Initialize(ctx); err != nil {
			log.Fatalf("initialize postgres: %v", err)
		}
		db = p
	case "sharded-postgres":
		placement, urls, err := sharding.ConfigFromEnv(os.Getenv)
		if err != nil {
			log.Fatal(err)
		}
		sharded, closeShards, err := sharding.OpenPostgres(ctx, placement, urls)
		if err != nil {
			log.Fatal(err)
		}
		defer closeShards()
		db = sharded
		cacheNamespace = backend + ":" + placement.Fingerprint()[:16]
	case "dynamodb":
		d, err := dynamolab.New(dynamolab.DefaultTable)
		if err != nil {
			log.Fatal(err)
		}
		initCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = d.Initialize(initCtx)
		cancel()
		if err != nil {
			log.Fatalf("initialize DynamoDB Local: %v", err)
		}
		db = d
	default:
		log.Fatalf("unknown STORAGE_BACKEND %q (choose postgres, dynamodb, or sharded-postgres)", backend)
	}
	enabled, err := boolEnv("CACHE_ENABLED", false)
	if err != nil {
		log.Fatal(err)
	}
	if enabled {
		ttlSeconds := int64(60)
		if raw := os.Getenv("CACHE_TTL_SECONDS"); raw != "" {
			ttlSeconds, err = strconv.ParseInt(raw, 10, 32)
			if err != nil || ttlSeconds < 1 {
				log.Fatal("CACHE_TTL_SECONDS must be a positive integer")
			}
		}
		coalesce, err := boolEnv("CACHE_COALESCE", false)
		if err != nil {
			log.Fatal(err)
		}
		addr := os.Getenv("REDIS_ADDR")
		if addr == "" {
			addr = "localhost:6379"
		}
		client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 150 * time.Millisecond, ReadTimeout: 150 * time.Millisecond, WriteTimeout: 150 * time.Millisecond, PoolTimeout: 200 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true})
		defer client.Close()
		db = cache.New(db, client, time.Duration(ttlSeconds)*time.Second, cacheNamespace, coalesce)
		log.Printf("Redis cache enabled; ttl=%ds coalescing=%t", ttlSeconds, coalesce)
	}
	label := backend
	if label == "postgres" {
		label = "postgresql"
	}
	handler, err := httpapi.NewWithBackend(db, os.Getenv("PUBLIC_BASE_URL"), label)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: ":8000", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("listening on :8000; storage=%s", label)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
			_ = server.Close()
		}
	}
}
