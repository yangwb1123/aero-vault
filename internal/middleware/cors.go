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
	originAllowed := compileOriginCheck(cfg.AllowedOrigins)
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposeHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				if exposed != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposed)
				}
				if cfg.AllowCreds {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				w.Header().Set("Access-Control-Max-Age", strFromInt(cfg.MaxAgeSeconds))
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func compileOriginCheck(allowed []string) func(string) bool {
	for _, a := range allowed {
		if a == "*" {
			return func(string) bool { return true }
		}
	}
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[strings.ToLower(a)] = struct{}{}
	}
	return func(o string) bool {
		_, ok := set[strings.ToLower(o)]
		return ok
	}
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
