// shardseed inserts deterministic codes for local sharded k6 workloads.
package main

import (
	"aws-systems-performance/internal/sharding"
	"aws-systems-performance/internal/store"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	count := flag.Int("count", 1000, "number of fixture records")
	flag.Parse()
	if *count < 1 {
		log.Fatal("count must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, urls, err := sharding.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	db, closeDB, err := sharding.OpenPostgres(ctx, p, urls)
	if err != nil {
		log.Fatal(err)
	}
	defer closeDB()
	inserted := 0
	for i := 1; i <= *count; i++ {
		code := fmt.Sprintf("seed%06d", i)
		_, err := db.Get(ctx, code)
		if err == nil {
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			log.Fatal(err)
		}
		if _, err = db.Create(ctx, code, fmt.Sprintf("user%03d", i%100), fmt.Sprintf("https://example.com/%d", i)); err != nil {
			log.Fatal(err)
		}
		inserted++
	}
	fmt.Printf("Inserted %d of %d links; existing records retained.\n", inserted, *count)
}
