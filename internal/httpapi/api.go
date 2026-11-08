package httpapi

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aws-systems-performance/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed site/index.html
var indexHTML []byte

type API struct {
	store         store.Store
	requests      *prometheus.CounterVec
	latency       *prometheus.HistogramVec
	publicBaseURL string
	backend       string
}

const DefaultPublicBaseURL = "http://localhost:8000"

// NewWithBaseURL validates the configured public origin instead of trusting request headers.
func NewWithBaseURL(s store.Store, base string) (http.Handler, error) {
	return NewWithBackend(s, base, "postgresql")
}

func NewWithBackend(s store.Store, base, backend string) (http.Handler, error) {
	if base == "" {
		base = DefaultPublicBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL must be an http(s) origin without credentials, path, query, or fragment")
	}
	base = strings.TrimSuffix(base, "/")
	registry := prometheus.NewRegistry()
	a := &API{store: s, publicBaseURL: base, backend: backend,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests"}, []string{"method", "route", "status"}),
		latency:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration in seconds", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"method", "route"}),
	}
	registry.MustRegister(a.requests, a.latency)
	if instrumented, ok := s.(interface{ RegisterMetrics(prometheus.Registerer) }); ok {
		instrumented.RegisterMetrics(registry)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("POST /links", a.create)
	mux.HandleFunc("GET /links/{code}", a.get)
	mux.HandleFunc("GET /u/{code}", a.redirect)
	mux.HandleFunc("GET /users/{userId}/links", a.list)
	mux.HandleFunc("POST /links/{code}/click", a.click)
	mux.HandleFunc("GET /analytics/popular", a.popular)
	mux.HandleFunc("GET /health", a.health)
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return a.instrument(mux), nil
}

func New(s store.Store) http.Handler {
	h, _ := NewWithBaseURL(s, DefaultPublicBaseURL)
	return h
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (a *API) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request panic: %v", recovered)
				if sw.status == 0 {
					writeError(sw, 500, "internal server error")
				}
			}
			status := sw.status
			if status == 0 {
				status = 200
			}
			route := "unmatched"
			if r.Pattern != "" {
				parts := strings.SplitN(r.Pattern, " ", 2)
				route = parts[len(parts)-1]
			}
			method := r.Method
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				method = "OTHER"
			}
			a.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
			a.latency.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(sw, r)
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}
func writeError(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func handleError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "link not found")
		return
	}
	if errors.Is(err, store.ErrUnsupported) {
		writeError(w, http.StatusNotImplemented, "operation not supported by storage backend")
		return
	}
	log.Printf("storage error: %v", err)
	writeError(w, 500, "internal server error")
}
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UserID      string `json:"userId"`
		Destination string `json:"destinationUrl"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid JSON body")
		return
	}
	parsed, err := url.Parse(input.Destination)
	if strings.TrimSpace(input.UserID) == "" || utf8.RuneCountInString(input.UserID) > 200 || err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, 400, "userId and an http(s) destinationUrl are required")
		return
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		log.Printf("generate code: %v", err)
		writeError(w, 500, "internal server error")
		return
	}
	link, err := a.store.Create(r.Context(), base64.RawURLEncoding.EncodeToString(random[:]), input.UserID, input.Destination)
	if err != nil {
		handleError(w, err)
		return
	}
	respond(w, 201, struct {
		store.Link
		ShortURL string `json:"shortUrl"`
	}{link, a.publicBaseURL + "/u/" + url.PathEscape(link.Code)})
}

func (a *API) redirect(w http.ResponseWriter, r *http.Request) {
	// ServeMux also matches HEAD to GET; only an actual GET counts as a click.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	link, err := a.store.Click(r.Context(), r.PathValue("code"))
	if err != nil {
		handleError(w, err)
		return
	}
	http.Redirect(w, r, link.DestinationURL, http.StatusFound)
}
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	link, err := a.store.Get(r.Context(), r.PathValue("code"))
	if err != nil {
		handleError(w, err)
		return
	}
	respond(w, 200, link)
}
func (a *API) click(w http.ResponseWriter, r *http.Request) {
	link, err := a.store.Click(r.Context(), r.PathValue("code"))
	if err != nil {
		handleError(w, err)
		return
	}
	respond(w, 200, link)
}
func limit(r *http.Request, fallback int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		return 0, fmt.Errorf("limit must be between 1 and 100")
	}
	return n, nil
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	n, err := limit(r, 50)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	links, err := a.store.List(r.Context(), r.PathValue("userId"), n)
	if err != nil {
		handleError(w, err)
		return
	}
	respond(w, 200, links)
}
func (a *API) popular(w http.ResponseWriter, r *http.Request) {
	n, err := limit(r, 10)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	links, err := a.store.Popular(r.Context(), n)
	if err != nil {
		handleError(w, err)
		return
	}
	respond(w, 200, links)
}
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Health(r.Context()); err != nil {
		log.Printf("health: %v", err)
		writeError(w, 503, "database unavailable")
		return
	}
	respond(w, 200, map[string]string{"status": "ok", "storage": a.backend})
}
