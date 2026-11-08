package sharding

import (
	"aws-systems-performance/internal/store"
	"context"
	"fmt"
	"net/url"
	"time"
)

// EnableLocalDelay must be called before serving requests. It intentionally
// affects only reads and only explicitly configured local lab endpoints.
func (s *Store) EnableLocalDelay(id string, delay time.Duration, urls []string) error {
	if delay <= 0 || delay > 5*time.Second {
		return fmt.Errorf("fan-out lab delay must be greater than zero and at most 5s")
	}
	if len(urls) != len(s.placement.IDs) {
		return fmt.Errorf("fan-out lab requires all local shard URLs")
	}
	for i, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid local shard URL")
		}
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != s.placement.IDs[i] {
			return fmt.Errorf("fan-out lab delay requires local shard endpoints")
		}
	}
	target, ok := s.shards[id]
	if !ok {
		return fmt.Errorf("unknown fan-out lab shard %q", id)
	}
	s.shards[id] = delayedStore{Store: target, delay: delay}
	return nil
}

type delayedStore struct {
	store.Store
	delay time.Duration
}

func (d delayedStore) wait(ctx context.Context) error {
	timer := time.NewTimer(d.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (d delayedStore) Get(ctx context.Context, code string) (store.Link, error) {
	if err := d.wait(ctx); err != nil {
		return store.Link{}, err
	}
	return d.Store.Get(ctx, code)
}
func (d delayedStore) List(ctx context.Context, user string, limit int) ([]store.Link, error) {
	if err := d.wait(ctx); err != nil {
		return nil, err
	}
	return d.Store.List(ctx, user, limit)
}
func (d delayedStore) Popular(ctx context.Context, limit int) ([]store.Link, error) {
	if err := d.wait(ctx); err != nil {
		return nil, err
	}
	return d.Store.Popular(ctx, limit)
}
