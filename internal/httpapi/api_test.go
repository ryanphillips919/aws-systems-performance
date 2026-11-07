package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"aws-systems-performance/internal/store"
)

type memoryStore struct{ links map[string]store.Link }

func (m *memoryStore) Health(context.Context) error { return nil }
func (m *memoryStore) Create(_ context.Context, code, user, dest string) (store.Link, error) {
	l := store.Link{Code: code, UserID: user, DestinationURL: dest, CreatedAt: time.Now().UTC()}
	m.links[code] = l
	return l, nil
}
func (m *memoryStore) Get(_ context.Context, code string) (store.Link, error) {
	l, ok := m.links[code]
	if !ok {
		return store.Link{}, store.ErrNotFound
	}
	return l, nil
}
func (m *memoryStore) Click(ctx context.Context, code string) (store.Link, error) {
	l, err := m.Get(ctx, code)
	if err != nil {
		return l, err
	}
	l.ClickCount++
	m.links[code] = l
	return l, nil
}
func (m *memoryStore) List(_ context.Context, user string, limit int) ([]store.Link, error) {
	out := []store.Link{}
	for _, l := range m.links {
		if l.UserID == user {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Code < out[j].Code
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *memoryStore) Popular(_ context.Context, limit int) ([]store.Link, error) {
	out := []store.Link{}
	for _, l := range m.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClickCount > out[j].ClickCount })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}
func TestLifecycleAndValidation(t *testing.T) {
	m := &memoryStore{links: map[string]store.Link{}}
	h := New(m)
	for _, body := range []string{`{}`, `{"userId":" ","destinationUrl":"https://example.com"}`, `{"userId":"a","destinationUrl":"ftp://example.com"}`, `{broken`, `{"userId":"a","destinationUrl":"https://example.com"} {}`} {
		if got := request(h, "POST", "/links", body).Code; got != 400 {
			t.Errorf("invalid %s: %d", body, got)
		}
	}
	w := request(h, "POST", "/links", `{"userId":"alice","destinationUrl":"https://example.com/a"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var link store.Link
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if link.Code == "" || link.UserID != "alice" || link.ClickCount != 0 {
		t.Fatalf("bad link: %+v", link)
	}
	if got := request(h, "GET", "/links/"+link.Code, ""); got.Code != 200 {
		t.Fatal(got.Code)
	}
	if got := request(h, "POST", "/links/"+link.Code+"/click", ""); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(`"clickCount":1`)) {
		t.Fatalf("click: %s", got.Body)
	}
	if got := request(h, "GET", "/users/alice/links", ""); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(link.Code)) {
		t.Fatalf("list: %s", got.Body)
	}
	if got := request(h, "GET", "/analytics/popular", ""); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(link.Code)) {
		t.Fatalf("popular: %s", got.Body)
	}
	for _, path := range []string{"/users/alice/links?limit=0", "/analytics/popular?limit=101", "/analytics/popular?limit=x"} {
		if got := request(h, "GET", path, "").Code; got != 400 {
			t.Errorf("%s: %d", path, got)
		}
	}
	if got := request(h, "GET", "/links/missing", "").Code; got != 404 {
		t.Fatal(got)
	}
	if got := request(h, "POST", "/links/missing/click", "").Code; got != 404 {
		t.Fatal(got)
	}
}
func TestListOrder(t *testing.T) {
	now := time.Now()
	m := &memoryStore{links: map[string]store.Link{"b": {Code: "b", UserID: "a", CreatedAt: now}, "a": {Code: "a", UserID: "a", CreatedAt: now}, "c": {Code: "c", UserID: "a", CreatedAt: now.Add(time.Second)}, "d": {Code: "d", UserID: "b", CreatedAt: now}}}
	w := request(New(m), "GET", "/users/a/links?limit=2", "")
	var got []store.Link
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Code != "c" || got[1].Code != "a" {
		t.Fatalf("order: %+v", got)
	}
}
func TestMetrics(t *testing.T) {
	h := New(&memoryStore{links: map[string]store.Link{}})
	request(h, "GET", "/links/secret-code", "")
	request(h, "GET", "/unknown", "")
	text := request(h, "GET", "/metrics", "").Body.String()
	for _, s := range []string{`http_requests_total{method="GET",route="/links/{code}",status="404"} 1`, `route="unmatched"`, `http_request_duration_seconds_bucket`, `le="0.005"`, `le="10"`} {
		if !strings.Contains(text, s) {
			t.Error("missing", s)
		}
	}
	for _, s := range []string{"secret-code", `route="/metrics"`} {
		if strings.Contains(text, s) {
			t.Error("unexpected", s)
		}
	}
}
func TestHealthFailure(t *testing.T) {
	h := New(brokenStore{&memoryStore{links: map[string]store.Link{}}})
	w := request(h, "GET", "/health", "")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

type brokenStore struct{ *memoryStore }

func (brokenStore) Health(context.Context) error { return fmt.Errorf("unavailable") }
