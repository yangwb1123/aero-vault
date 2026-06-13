package middleware

import (
	"context"
	"net/http"
	"time"
)

// RequestTimeout injects a per-request context deadline. When d is zero the
// middleware is a no-op. Downstream handlers that honour context cancellation
// (all AI pipeline calls do) return naturally when the deadline fires; the
// response is written by the handler itself, so SSE streams can flush a
// structured error frame before closing.
func RequestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
