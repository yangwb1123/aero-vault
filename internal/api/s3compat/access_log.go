package s3compat

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/service"
)

// accessLogResponseWriter captures the outcome without changing the response
// semantics of the S3 handler. In particular, a logging failure is handled
// after the handler has returned and therefore cannot replace its status.
type accessLogResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newAccessLogResponseWriter(w http.ResponseWriter) *accessLogResponseWriter {
	return &accessLogResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *accessLogResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessLogResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *accessLogResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := newAccessLogResponseWriter(w)
		next.ServeHTTP(sw, r)

		bucket := chi.URLParam(r, "bucket")
		if bucket == "" {
			return
		}
		entry := service.AccessLogEntry{
			Method:      r.Method,
			Key:         keyFromURL(r),
			Status:      sw.status,
			Latency:     time.Since(start),
			UserAgent:   r.UserAgent(),
			RemoteAddr:  r.RemoteAddr,
			RequestID:   mw.RequestIDFrom(r.Context()),
			Referer:     r.Referer(),
			Bytes:       sw.bytes,
			CompletedAt: time.Now().UTC(),
		}
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
		defer cancel()
		if err := h.svc.RecordBucketAccessLog(logCtx, mw.TenantFrom(r.Context()), bucket, entry); err != nil {
			h.logger.Warn("bucket access log write failed", "bucket", bucket, "key", entry.Key, "err", err)
		}
	})
}
