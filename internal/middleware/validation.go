package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MaxBodySize limits the request body to maxBytes. If the Content-Length header
// exceeds the limit the request is rejected immediately (without reading any
// body) with 413 Request Entity Too Large. Otherwise the body reader is wrapped
// with io.LimitReader so streaming reads also honour the cap.
//
// A maxBytes value of 0 or less disables limiting (zero-cost pass-through).
// ErrBodyTooLarge is surfaced to the handler when an unknown-length
// (chunked) request body exceeds MaxBodySize. A plain LimitReader silently
// truncated oversize chunked bodies and stored corrupt objects with 200 +
// ETag; the error reader fails the read instead, so the write path aborts
// and maps the sentinel to 413 (REST BodyTooLarge / S3 EntityTooLarge).
var ErrBodyTooLarge = errors.New("request body too large")

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
			// Unknown-length bodies flow through a reader that errors past
			// maxBytes instead of reporting a silent EOF.
			r.Body = io.NopCloser(&maxBytesReader{Reader: r.Body, remaining: maxBytes})
			next.ServeHTTP(w, r)
		})
	}
}

// maxBytesReader delivers up to maxBytes bytes, then probes the source once:
// any remaining byte fails the read with ErrBodyTooLarge (chunked bodies can
// no longer truncate silently).
type maxBytesReader struct {
	io.Reader
	remaining int64
	over      bool
}

func (m *maxBytesReader) Read(p []byte) (int, error) {
	if m.over {
		return 0, ErrBodyTooLarge
	}
	if m.remaining <= 0 {
		var probe [1]byte
		if n, _ := m.Reader.Read(probe[:]); n > 0 {
			m.over = true
			return 0, ErrBodyTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > m.remaining {
		p = p[:m.remaining]
	}
	n, err := m.Reader.Read(p)
	m.remaining -= int64(n)
	return n, err
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
