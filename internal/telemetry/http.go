package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// WithMiddlewareTiming wraps each middleware layer with a duration histogram
// so the time spent in every middleware can be observed independently.
// The name is used as the `middleware` label in the middleware.duration_ms metric.
func WithMiddlewareTiming(name string, mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			mw(next).ServeHTTP(w, r)
			RecordMiddlewareLatency(r.Context(), name, time.Since(start))
		})
	}
}

// HTTPMiddleware wraps an http.Handler with a span per request and a
// metric for request count + latency. Safe to chain after RequestID/Tenant.
func HTTPMiddleware(serviceName string) func(http.Handler) http.Handler {
	tracer := otel.Tracer("aero-vault/http")
	meter := otel.Meter("aero-vault/http")
	reqCount, _ := meter.Int64Counter("http.server.requests")
	reqDur, _ := meter.Float64Histogram("http.server.duration_ms")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.target", r.URL.Path),
				),
			)
			defer span.End()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(sw, r.WithContext(ctx))
			dur := float64(time.Since(start).Milliseconds())
			span.SetAttributes(attribute.Int("http.status_code", sw.status))
			attrs := metric.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("status", strconv.Itoa(sw.status/100*100)),
			)
			reqCount.Add(ctx, 1, attrs)
			reqDur.Record(ctx, dur, attrs)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
