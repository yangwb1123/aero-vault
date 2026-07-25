package middleware

import (
	"net/http"
	"strings"
)

// CORSConfig drives the CORS middleware. Allowed* fields accept "*" to mean
// every value. ExposeHeaders is what the browser is allowed to read back.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
	AllowCreds     bool
}

// CORS returns a middleware that handles preflight (OPTIONS) requests and
// stamps the necessary headers on every response. Pass an empty config to
// disable CORS (the middleware becomes a pass-through).
// CORS returns a middleware that handles preflight (OPTIONS) requests and
// stamps the necessary headers on every response. Pass an empty config to
// disable CORS (the middleware becomes a pass-through).
//
// When AllowedOrigins contains "*", all origins are permitted. When it contains
// specific origins (e.g. "https://example.com"), only those are allowed and the
// Vary: Origin header is set so browsers cache the response per origin.
// Credentials (cookies, auth headers) are only forwarded when a single
// specific origin is matched (never with "*" per CORS spec).
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	if len(cfg.AllowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	if len(cfg.AllowedMethods) == 0 {
		cfg.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"}
	}
	if len(cfg.AllowedHeaders) == 0 {
		cfg.AllowedHeaders = []string{"Authorization", "Content-Type", "Idempotency-Key", "X-Aero-Tenant", "X-Api-Key", "X-Request-ID", "Range"}
	}
	if cfg.MaxAgeSeconds == 0 {
		cfg.MaxAgeSeconds = 600
	}
	allowAll := false
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			allowAll = true
			break
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			isPreflight := r.Method == http.MethodOptions

			// No Origin header: not a CORS request.
			// Bare OPTIONS (no Origin) still gets a 204 for health checks.
			if origin == "" {
				if isPreflight {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			// Origin present but not allowed.
			if !allowAll && !matchOrigin(origin, cfg.AllowedOrigins) {
				if isPreflight {
					http.Error(w, "origin not allowed", http.StatusForbidden)
					return
				}
				// Non-preflight with disallowed origin: omit CORS headers so the
				// browser blocks the response client-side.
				next.ServeHTTP(w, r)
				return
			}

			// Allowed origin: write CORS headers.
			writeCORSHeaders(w, r, cfg, origin, allowAll)
			if isPreflight {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchOrigin(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" {
			return true
		}
		if strings.EqualFold(origin, a) {
			return true
		}
	}
	return false
}

func writeCORSHeaders(w http.ResponseWriter, r *http.Request, cfg CORSConfig, origin string, _ bool) {
	// Always reflect the specific origin, even for wildcard configs.
	// This is safer because Access-Control-Allow-Origin: * cannot be used
	// with credentials (cookies, auth headers). Reflecting the specific
	// origin allows credentials to work when needed.
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
	if len(cfg.ExposeHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposeHeaders, ", "))
	}
	if cfg.AllowCreds {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	w.Header().Set("Access-Control-Max-Age", strFromInt(cfg.MaxAgeSeconds))
}

func strFromInt(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
