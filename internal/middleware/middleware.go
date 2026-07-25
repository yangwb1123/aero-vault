package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxTenantID
)

// TenantHeader is read by the Tenant middleware. Both REST and S3-compatible
// callers send `X-Aero-Tenant`. Missing or empty header falls back to
// "default", keeping the API back-compat with single-tenant deployments.
const TenantHeader = "X-Aero-Tenant"

// RequestID generates or accepts an X-Request-ID and threads it through context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

// Tenant extracts X-Aero-Tenant and stashes it on context.
func Tenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := r.Header.Get(TenantHeader)
		if t == "" {
			t = "default"
		}
		ctx := context.WithValue(r.Context(), ctxTenantID, t)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TenantFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxTenantID).(string)
	if v == "" {
		return "default"
	}
	return v
}

// Recoverer turns panics into 500s and logs the stack.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic",
						"panic", rec,
						"path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()),
						"stack", string(debug.Stack()),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog writes one slog line per request, including tenant.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"bytes", sw.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
				"tenant", TenantFrom(r.Context()),
			)
		})
	}
}

// Auth is the placeholder authentication middleware.
func Auth(next http.Handler) http.Handler { return next }

// ConcurrencyLimiter limits in-flight requests by a weighted buffered channel
// (a counting semaphore). GET/HEAD/OPTIONS cost 1; PUT/POST/DELETE cost 2.
// When full, new requests get 429 Too Many Requests with Retry-After: 1.
// A max of 0 or less disables limiting entirely (zero-cost pass-through).
type ConcurrencyLimiter struct {
	sem chan struct{}
}

// NewConcurrencyLimiter creates a limiter that allows up to max weighted units.
func NewConcurrencyLimiter(max int) *ConcurrencyLimiter {
	if max <= 0 {
		return &ConcurrencyLimiter{}
	}
	return &ConcurrencyLimiter{sem: make(chan struct{}, max)}
}

func reqWeight(method string) int {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return 1
	default:
		return 2
	}
}

// Middleware wraps an http.Handler with concurrency limiting.
func (cl *ConcurrencyLimiter) Middleware() func(http.Handler) http.Handler {
	if cl.sem == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cost := reqWeight(r.Method)
			// Non-blocking acquire of all slots at once.
			acquired := 0
			for i := 0; i < cost; i++ {
				select {
				case cl.sem <- struct{}{}:
					acquired++
				default:
					// Failed to acquire all; release what we got.
					for j := 0; j < acquired; j++ {
						<-cl.sem
					}
					w.Header().Set("Retry-After", "1")
					http.Error(w, "too many concurrent requests", http.StatusTooManyRequests)
					return
				}
			}
			defer func() {
				for i := 0; i < cost; i++ {
					<-cl.sem
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush proxies http.Flusher through the wrapper so SSE handlers below can
// stream. Embedding http.ResponseWriter only promotes methods declared on
// that interface — Flush is not one of them, so we forward explicitly.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// PerTenantConcurrencyLimiter extends concurrency limiting with per-tenant
// tracking so a single misbehaving tenant can't exhaust the global pool.
type PerTenantConcurrencyLimiter struct {
	global    *ConcurrencyLimiter
	perTenant int
	inflight  map[string]int
	mu        sync.Mutex
}

// NewPerTenantConcurrencyLimiter creates a combined global + per-tenant limiter.
func NewPerTenantConcurrencyLimiter(globalMax, perTenantMax int) *PerTenantConcurrencyLimiter {
	return &PerTenantConcurrencyLimiter{
		global:    NewConcurrencyLimiter(globalMax),
		perTenant: perTenantMax,
		inflight:  make(map[string]int),
	}
}

// Middleware returns an HTTP middleware that enforces both limits.
func (pt *PerTenantConcurrencyLimiter) Middleware() func(http.Handler) http.Handler {
	if pt.global == nil || pt.global.sem == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cost := reqWeight(r.Method)
			tenant := TenantFrom(r.Context())

			// Acquire global slot(s) first.
			acquired := 0
			for i := 0; i < cost; i++ {
				select {
				case pt.global.sem <- struct{}{}:
					acquired++
				default:
					for j := 0; j < acquired; j++ {
						<-pt.global.sem
					}
					w.Header().Set("Retry-After", "1")
					http.Error(w, "too many concurrent requests", http.StatusTooManyRequests)
					return
				}
			}

			// Check per-tenant budget.
			if pt.perTenant > 0 {
				pt.mu.Lock()
				if pt.inflight[tenant] >= pt.perTenant {
					pt.mu.Unlock()
					for i := 0; i < cost; i++ {
						<-pt.global.sem
					}
					w.Header().Set("Retry-After", "1")
					http.Error(w, "tenant has too many concurrent requests", http.StatusTooManyRequests)
					return
				}
				pt.inflight[tenant] += cost
				pt.mu.Unlock()
			}

			defer func() {
				for i := 0; i < cost; i++ {
					<-pt.global.sem
				}
				if pt.perTenant > 0 {
					pt.mu.Lock()
					pt.inflight[tenant] -= cost
					if pt.inflight[tenant] <= 0 {
						delete(pt.inflight, tenant)
					}
					pt.mu.Unlock()
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
