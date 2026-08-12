package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrBodyTooLarge is returned by the wrapped request-body reader when a
// chunked/unknown-length body exceeds maxBytes. Unlike a clean EOF it is
// distinguishable from a body that ends exactly at the limit; adapters map
// it to 413 (RequestEntityTooLarge).
var ErrBodyTooLarge = errors.New("request body exceeds maximum allowed size")

// limitErrReader is a drop-in replacement for io.LimitReader that does not
// silently truncate: when the underlying reader still yields bytes past the
// limit it returns ErrBodyTooLarge instead of a clean EOF, so downstream
// adapters can reject the request rather than storing a truncated object.
//
// A body that ends exactly at the limit is indistinguishable from truncation
// without reading one byte past the cap, so Read peeks at byte limit+1 to
// decide: underlying byte present → ErrBodyTooLarge; underlying EOF → clean
// EOF. Transport-level errors (client abort, etc.) pass through untouched.
type limitErrReader struct {
	r     io.Reader
	limit int64
	n     int64
}

func (l *limitErrReader) Read(p []byte) (int, error) {
	if len(p) == 0 { // io.Reader contract: (0, nil), consume nothing.
		return 0, nil
	}
	if l.n >= l.limit {
		// Peek one byte past the cap to distinguish "exactly at limit"
		// (clean EOF) from "truncated" (sentinel). The peeked byte is
		// discarded — the request is already rejected.
		var one [1]byte
		for { // Tolerate underlying (0, nil) empty reads (allowed, discouraged).
			n, err := l.r.Read(one[:])
			if n > 0 {
				return 0, ErrBodyTooLarge
			}
			if err != nil {
				return 0, err
			}
		}
	}
	if int64(len(p)) > l.limit-l.n {
		p = p[:l.limit-l.n]
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	return n, err
}

func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reject early when Content-Length is known and already too large.
			if r.ContentLength > maxBytes {
				w.Header().Set("Connection", "close")
				http.Error(w, fmt.Sprintf("request body too large: max %d bytes", maxBytes),
					http.StatusRequestEntityTooLarge)
				return
			}
			// Wrap the body with a limit reader that surfaces an explicit
			// ErrBodyTooLarge when an unknown-length (chunked) body exceeds
			// maxBytes — a clean io.EOF at the cap would silently truncate
			// the upload and let a corrupt object be stored (adapters map
			// the sentinel to 413).
			r.Body = io.NopCloser(&limitErrReader{r: r.Body, limit: maxBytes})

			next.ServeHTTP(w, r)
		})
	}
}

// SecureHeaders stamps a minimal set of security-related HTTP response headers
// on every request. This middleware should be registered early in the chain
// (after RequestID, before CORS) so all downstream responses inherit the headers.
//
// Headers set:
//   - Strict-Transport-Security (max-age=31536000; includeSubDomains)
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Permissions-Policy: (disables geolocation, camera, microphone by default)
func SecureHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy",
				"geolocation=(), camera=(), microphone=()")
			next.ServeHTTP(w, r)
		})
	}
}

// EnforceContentType returns a middleware that rejects requests whose
// Content-Type header does not contain the expected content type string
// (matched as a case-insensitive substring). If the request has no body
// (GET, HEAD, DELETE) or no Content-Type is set, the check is skipped.
//
// This is useful for JSON-only route groups such as /v1/*:
//
//	r.Group(func(r chi.Router) {
//	    r.Use(middleware.EnforceContentType("application/json"))
//	    r.Post("/objects", ...)
//	})
func EnforceContentType(expected string) func(http.Handler) http.Handler {
	// Empty expected string means no enforcement (pass-through).
	if expected == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	expectedLower := strings.ToLower(expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only enforce for methods that carry a body.
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions,
				http.MethodDelete, http.MethodTrace, http.MethodConnect:
				next.ServeHTTP(w, r)
				return
			}
			ct := r.Header.Get("Content-Type")
			if ct == "" || !strings.Contains(strings.ToLower(ct), expectedLower) {
				http.Error(w,
					fmt.Sprintf("unexpected Content-Type: expected %q", expected),
					http.StatusUnsupportedMediaType)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
