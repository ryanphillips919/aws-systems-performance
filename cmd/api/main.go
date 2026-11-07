package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aws-systems-performance/internal/dynamolab"
	"aws-systems-performance/internal/httpapi"
	"aws-systems-performance/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func backendName(raw string) (string, error) {
	if raw == "" {
		return "postgres", nil
	}
	if raw == "postgres" || raw == "dynamodb" {
		return raw, nil
	}
	return "", fmt.Errorf("unknown STORAGE_BACKEND %q (choose postgres or dynamodb)", raw)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend, err := backendName(os.Getenv("STORAGE_BACKEND"))
	if err != nil {
		log.Fatal(err)
	}
	var db store.Store
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
		log.Fatalf("unknown STORAGE_BACKEND %q (choose postgres or dynamodb)", backend)
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
