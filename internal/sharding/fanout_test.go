package sharding

import (
	"aws-systems-performance/internal/store"
	"context"
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type barrierShard struct {
	*fake
	arrived *atomic.Int32
	all     chan struct{}
}

func (b barrierShard) List(ctx context.Context, _ string, _ int) ([]store.Link, error) {
	if b.arrived.Add(1) == 3 {
		close(b.all)
	}
	select {
	case <-b.all:
		return []store.Link{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestFanoutConcurrent(t *testing.T) {
	p, _ := NewPlacement("modulo", []string{"a", "b", "c"}, 1, 1, "v1")
	var arrived atomic.Int32
	all := make(chan struct{})
	shards := map[string]store.Store{}
	for _, id := range p.IDs {
		shards[id] = barrierShard{&fake{links: map[string]store.Link{}}, &arrived, all}
	}
	s, _ := NewStore(p, shards)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.List(ctx, "u", 10); err != nil {
		t.Fatal(err)
	}
	if arrived.Load() != 3 {
		t.Fatal(arrived.Load())
	}
}
func TestLabDelayAndMetrics(t *testing.T) {
	p, _ := NewPlacement("modulo", []string{"a", "b", "c"}, 1, 1, "v1")
	shards := map[string]store.Store{}
	for _, id := range p.IDs {
		shards[id] = &fake{links: map[string]store.Link{}}
	}
	s, _ := NewStore(p, shards)
	if err := s.EnableLocalDelay("b", 50*time.Millisecond, []string{"postgres://a/db", "postgres://b/db", "postgres://example.com/db"}); err == nil {
		t.Fatal("accepted nonlocal host")
	}
	urls := []string{"postgres://a/db", "postgres://b/db", "postgres://c/db"}
	if err := s.EnableLocalDelay("missing", time.Second, urls); err == nil {
		t.Fatal("accepted unknown shard")
	}
	if err := s.EnableLocalDelay("b", 50*time.Millisecond, urls); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := s.List(context.Background(), "sensitive-user", 10); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 45*time.Millisecond {
		t.Fatal("did not wait for slow shard")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := s.List(ctx, "u", 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	s.RegisterMetrics(reg)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() == "shard_fanout_shards_contacted" {
			for _, m := range f.Metric {
				if m.GetHistogram().GetSampleCount() == 2 && m.GetHistogram().GetSampleSum() == 6 {
					found = true
				}
			}
		}
		if strings.Contains(fmt.Sprint(f), "sensitive-user") {
			t.Fatal("user leaked into metric")
		}
	}
	if !found {
		t.Fatal("fan-out width missing")
	}
}
