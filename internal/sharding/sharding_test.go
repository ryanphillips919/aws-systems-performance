package sharding

import (
	"aws-systems-performance/internal/store"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestPlacementDeterministicAndMovement(t *testing.T) {
	ids := []string{"a", "b", "c"}
	more := []string{"a", "b", "c", "d"}
	less := []string{"a", "b"}
	for _, strategy := range []string{"modulo", "consistent", "partition"} {
		t.Run(strategy, func(t *testing.T) {
			p, err := NewPlacement(strategy, ids, 128, 256, "v1")
			if err != nil {
				t.Fatal(err)
			}
			same, _ := NewPlacement(strategy, ids, 128, 256, "v1")
			if p.Fingerprint() != same.Fingerprint() {
				t.Fatal("fingerprint mismatch")
			}
			added, _ := NewPlacement(strategy, more, 128, 256, "v2")
			removed, _ := NewPlacement(strategy, less, 128, 256, "v2")
			moves := 0
			for i := 0; i < 5000; i++ {
				key := fmt.Sprint(i)
				old := p.Owner(key)
				if old != same.Owner(key) {
					t.Fatal("nondeterministic")
				}
				n := added.Owner(key)
				if old != n {
					moves++
					if strategy == "consistent" && n != "d" {
						t.Fatalf("unrelated move %s to %s", old, n)
					}
				}
				n = removed.Owner(key)
				if strategy == "consistent" && old != "c" && n != old {
					t.Fatal("removal moved unaffected key")
				}
			}
			if moves == 0 {
				t.Fatal("addition moved no keys")
			}
		})
	}
}
func TestInvalidPlacement(t *testing.T) {
	for _, tc := range []struct {
		s       string
		ids     []string
		v, p    int
		version string
	}{{"consistent", []string{"a", "a"}, 1, 1, "v"}, {"consistent", []string{"a"}, 0, 1, "v"}, {"partition", []string{"a"}, 1, 0, "v"}, {"partition", []string{"a"}, 1, 2, ""}, {"bad", []string{"a"}, 1, 1, "v"}} {
		if _, err := NewPlacement(tc.s, tc.ids, tc.v, tc.p, tc.version); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

type fake struct {
	mu    sync.Mutex
	links map[string]store.Link
	fail  bool
}

func (f *fake) Create(_ context.Context, code, user, dest string) (store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := store.Link{Code: code, UserID: user, DestinationURL: dest, CreatedAt: time.Now().UTC()}
	f.links[code] = l
	return l, nil
}
func (f *fake) Get(_ context.Context, code string) (store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.links[code]
	if !ok {
		return v, store.ErrNotFound
	}
	return v, nil
}
func (f *fake) Click(ctx context.Context, code string) (store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.links[code]
	if !ok {
		return v, store.ErrNotFound
	}
	v.ClickCount++
	f.links[code] = v
	return v, nil
}
func (f *fake) List(ctx context.Context, user string, n int) ([]store.Link, error) {
	if f.fail {
		return nil, errors.New("unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Link{}
	for _, l := range f.links {
		if l.UserID == user {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].Code < out[j].Code
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}
func (f *fake) Popular(ctx context.Context, n int) ([]store.Link, error) {
	if f.fail {
		return nil, errors.New("unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Link{}
	for _, l := range f.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClickCount != out[j].ClickCount {
			return out[i].ClickCount > out[j].ClickCount
		}
		return out[i].Code < out[j].Code
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}
func (f *fake) Health(context.Context) error {
	if f.fail {
		return errors.New("unavailable")
	}
	return nil
}
func TestRoutingAndFanout(t *testing.T) {
	ids := []string{"a", "b", "c"}
	p, _ := NewPlacement("consistent", ids, 128, 256, "v1")
	backends := map[string]store.Store{}
	fakes := map[string]*fake{}
	for _, id := range ids {
		f := &fake{links: map[string]store.Link{}}
		backends[id] = f
		fakes[id] = f
	}
	s, err := NewStore(p, backends)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	codes := []string{}
	for i := 0; i < 30; i++ {
		code := fmt.Sprintf("test%d", i)
		codes = append(codes, code)
		if _, err = s.Create(ctx, code, "u", "https://example.com"); err != nil {
			t.Fatal(err)
		}
		if _, ok := fakes[p.Owner(code)].links[code]; !ok {
			t.Fatal("wrong shard")
		}
		if _, err = s.Get(ctx, code); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range fakes {
		for code, l := range f.links {
			l.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			l.ClickCount = 1
			f.links[code] = l
		}
	}
	list, err := s.List(ctx, "u", 5)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(codes)
	for i := range list {
		if list[i].Code != codes[i] {
			t.Fatalf("list tie order: %+v", list)
		}
	}
	if _, err = s.Click(ctx, codes[7]); err != nil {
		t.Fatal(err)
	}
	popular, err := s.Popular(ctx, 3)
	if err != nil || popular[0].Code != codes[7] || popular[1].Code != codes[0] {
		t.Fatalf("popular %+v %v", popular, err)
	}
	fakes["b"].fail = true
	if got, err := s.List(ctx, "u", 5); err == nil || got != nil {
		t.Fatalf("partial list: %+v %v", got, err)
	}
	if got, err := s.Popular(ctx, 5); err == nil || got != nil {
		t.Fatalf("partial popularity: %+v %v", got, err)
	}
	if err := s.Health(ctx); err == nil {
		t.Fatal("health missed failed shard")
	}
}

func TestConfig(t *testing.T) {
	env := map[string]string{"SHARD_DATABASE_URLS": "postgres://a,postgres://b,postgres://c"}
	get := func(key string) string { return env[key] }
	p, urls, err := ConfigFromEnv(get)
	if err != nil || p.Strategy != "consistent" || p.VNodes != 128 || len(urls) != 3 {
		t.Fatalf("config: %+v %v %v", p, urls, err)
	}
	env["SHARD_DATABASE_URLS"] = "postgres://only-one"
	if _, _, err = ConfigFromEnv(get); err == nil {
		t.Fatal("wrong URL count accepted")
	}
	env["SHARD_DATABASE_URLS"] = "postgres://a,postgres://b,postgres://c"
	env["SHARD_VNODES"] = "bad"
	if _, _, err = ConfigFromEnv(get); err == nil {
		t.Fatal("bad virtual-node count accepted")
	}
}

type waitingShard struct{ *fake }

func (w waitingShard) List(ctx context.Context, _ string, _ int) ([]store.Link, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestFanoutCancellation(t *testing.T) {
	p, _ := NewPlacement("modulo", []string{"a", "b"}, 128, 256, "v1")
	f := &fake{links: map[string]store.Link{}}
	s, err := NewStore(p, map[string]store.Store{"a": f, "b": waitingShard{f}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	links, err := s.List(ctx, "u", 10)
	if links != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("links=%v err=%v", links, err)
	}
}
