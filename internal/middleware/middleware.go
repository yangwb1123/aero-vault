package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
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
