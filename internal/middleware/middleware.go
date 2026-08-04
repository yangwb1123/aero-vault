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
	ctxTenantStatusVerified
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
	return TenantWithStatus(nil)(next)
}

func TenantFrom(ctx context.Context) string {
	v, ok := TenantFromContext(ctx)
	if !ok {
		return "default"
	}
	return v
}

// TenantFromContext distinguishes an explicit "default" tenant from a context
// where the Tenant middleware has not run.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxTenantID).(string)
	return v, ok && v != ""
}

// TenantStatusVerified reports whether the Tenant middleware admitted this
// exact tenant after consulting the configured status lookup.
func TenantStatusVerified(ctx context.Context, tenant string) bool {
	verified, _ := ctx.Value(ctxTenantStatusVerified).(string)
	return verified != "" && verified == tenant
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
	var globalSem chan struct{}
	if pt.global != nil {
		globalSem = pt.global.sem
	}
	if globalSem == nil && pt.perTenant <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cost := reqWeight(r.Method)
			tenant := TenantFrom(r.Context())
			if !acquireSlots(globalSem, cost) {
				rejectConcurrency(w, "too many concurrent requests")
				return
			}
			if !pt.acquireTenant(tenant, cost) {
				releaseSlots(globalSem, cost)
				rejectConcurrency(w, "tenant has too many concurrent requests")
				return
			}
			defer func() {
				releaseSlots(globalSem, cost)
				pt.releaseTenant(tenant, cost)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func acquireSlots(sem chan struct{}, cost int) bool {
	if sem == nil {
		return true
	}
	for acquired := 0; acquired < cost; acquired++ {
		select {
		case sem <- struct{}{}:
		default:
			releaseSlots(sem, acquired)
			return false
		}
	}
	return true
}

func releaseSlots(sem chan struct{}, cost int) {
	for i := 0; i < cost && sem != nil; i++ {
		<-sem
	}
}

func (pt *PerTenantConcurrencyLimiter) acquireTenant(tenant string, cost int) bool {
	if pt.perTenant <= 0 {
		return true
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.inflight[tenant]+cost > pt.perTenant {
		return false
	}
	pt.inflight[tenant] += cost
	return true
}

func (pt *PerTenantConcurrencyLimiter) releaseTenant(tenant string, cost int) {
	if pt.perTenant <= 0 {
		return
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.inflight[tenant] -= cost
	if pt.inflight[tenant] <= 0 {
		delete(pt.inflight, tenant)
	}
}

func rejectConcurrency(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, message, http.StatusTooManyRequests)
}
