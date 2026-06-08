// Package auth implements API-key authentication with scoped permissions.
//
// Configuration (env): AUTH_KEYS holds a comma-separated list of records of
// the form
//
//	"<key>:<tenant>:<scope1>+<scope2>+..."
//
// Example:
//
//	AUTH_KEYS="prod-rw:acme:read+write,prod-ro:acme:read,ops:*:admin"
//
// `tenant == "*"` means the key is allowed for any tenant (admin operator).
// Scopes are checked per-route. When AUTH_KEYS is empty, authentication is
// disabled (matches the MVP behavior). A configured key set also pins the
// X-Aero-Tenant: any tenant header must match the key's tenant unless the
// key has tenant "*".
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
	ScopeAdmin Scope = "admin"
)

// Key is a parsed API-key record.
type Key struct {
	Token  string
	Tenant string
	Scopes map[Scope]bool
}

func (k Key) Has(s Scope) bool {
	if k.Scopes[ScopeAdmin] {
		return true
	}
	return k.Scopes[s]
}

// Registry holds the in-memory map of token -> Key. It is read-only after
// construction so callers can share one instance across goroutines.
type Registry struct {
	mu       sync.RWMutex
	keys     map[string]Key
	enabled  bool
	jwt      *JWTVerifier
	sigv4    *SigV4Verifier
	store    PersistentStore // optional repo-backed store for runtime keys (hashed)
	anonRead bool            // allow unauthenticated GET/HEAD on object paths (ACL-gated)
	keyCache *keyCache       // optional bounded TTL cache for persisted-key lookups (nil = off)
	// keyChangePublisher, when set, is invoked after a local persisted-key
	// add/revoke with the affected token hash, so other replicas can drop it from
	// their caches immediately. nil = single-instance (local invalidation only).
	keyChangePublisher func(ctx context.Context, tokenHash string)
}

// Parse turns the AUTH_KEYS env string into a Registry. An empty string
// returns a disabled (pass-through) registry.
func Parse(raw string) (*Registry, error) {
	reg := &Registry{keys: map[string]Key{}}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return reg, nil
	}
	for _, rec := range strings.Split(raw, ",") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, ":", 3)
		if len(parts) != 3 {
			return nil, errors.New("AUTH_KEYS: bad record, want token:tenant:scope+scope")
		}
		k := Key{Token: parts[0], Tenant: parts[1], Scopes: map[Scope]bool{}}
		for _, sc := range strings.Split(parts[2], "+") {
			sc = strings.TrimSpace(sc)
			if sc == "" {
				continue
			}
			k.Scopes[Scope(sc)] = true
		}
		reg.keys[k.Token] = k
	}
	reg.enabled = len(reg.keys) > 0
	return reg, nil
}

func (r *Registry) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled || r.jwt != nil || r.sigv4 != nil || r.store != nil
}

// WithStore attaches an optional persistent store so runtime API keys survive
// restart and are shared across replicas (hashed at rest). Attaching a store
// enables authentication. Env-seeded keys (AUTH_KEYS) still work alongside it.
func (r *Registry) WithStore(s PersistentStore) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = s
	return r
}

// WithKeyCache enables a bounded, TTL'd read-through cache in front of the
// persistent-store lookup path so repeated requests with the same token skip
// the DB. Only positive store hits are cached (misses fall through); JWT and
// env-key results are never cached (already in-memory). ttl<=0 or capacity<=0
// disables it (no-op), keeping the default OFF posture.
//
// Tradeoff: a local RevokeKey invalidates the entry immediately, but a revoke
// performed on another replica is only honored after the entry's TTL elapses.
// Keep the TTL short (e.g. 30s) to bound this cross-replica staleness window.
func (r *Registry) WithKeyCache(ttl time.Duration, capacity int) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyCache = newKeyCache(ttl, capacity)
	return r
}

// WithKeyChangePublisher wires a cross-instance notifier invoked on a successful
// persisted-key AddKey/RevokeKey with the affected token hash. Paired with
// InvalidateCachedKey on the receiving side (via a transport such as Postgres
// LISTEN/NOTIFY), it propagates a revoke to every replica immediately instead of
// waiting out the key-cache TTL. No-op contribution when key caching is off.
func (r *Registry) WithKeyChangePublisher(fn func(ctx context.Context, tokenHash string)) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyChangePublisher = fn
	return r
}

// InvalidateCachedKey drops a token hash from the local key cache. Safe to call on
// any replica (no-op when caching is disabled); the key-change listener calls it to
// apply a remote add/revoke.
func (r *Registry) InvalidateCachedKey(tokenHash string) {
	r.mu.RLock()
	cache := r.keyCache
	r.mu.RUnlock()
	if cache != nil {
		cache.delete(tokenHash)
	}
}

// WithSigV4 enables AWS SigV4 verification for S3 requests.
func (r *Registry) WithSigV4(v *SigV4Verifier) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sigv4 = v
	return r
}

func (r *Registry) Lookup(ctx context.Context, token string) (Key, bool) {
	r.mu.RLock()
	k, ok := r.keys[token]
	store := r.store
	jwt := r.jwt
	cache := r.keyCache
	r.mu.RUnlock()
	if ok {
		return k, true
	}
	// Persisted (hashed) runtime keys.
	if store != nil {
		hash := HashToken(token)
		now := time.Now()
		// Read-through cache: serve fresh positive hits without touching the DB.
		if cache != nil {
			if ck, hit := cache.get(hash, now); hit {
				return ck, true
			}
		}
		if pk, found, err := store.GetAPIKeyByHash(ctx, hash); err == nil && found {
			var keyExpiry time.Time
			if pk.ExpiresAt != "" {
				if exp, perr := time.Parse(time.RFC3339, pk.ExpiresAt); perr == nil {
					if now.After(exp) {
						return Key{}, false // expired
					}
					keyExpiry = exp
				}
			}
			// Best-effort last-used bookkeeping; never block auth on it.
			_ = store.TouchAPIKey(ctx, hash, now.UTC().Format(time.RFC3339Nano))
			resolved := Key{Token: token, Tenant: pk.TenantID, Scopes: parseScopeString(pk.Scopes)}
			if cache != nil {
				// Never cache past the key's own expiry.
				cache.put(hash, resolved, keyExpiry, now)
			}
			return resolved, true
		}
	}
	// Fall through to JWT verification when configured.
	if jwt != nil {
		if k, err := jwt.Verify(token); err == nil {
			return k, true
		}
	}
	return Key{}, false
}

// WithJWT enables JWT verification alongside static API keys.
func (r *Registry) WithJWT(secret string) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jwt = NewJWTVerifier(secret)
	return r
}

// JWT returns the verifier (or nil) so /admin/keys can sign new tokens.
func (r *Registry) JWT() *JWTVerifier { return r.jwt }

// WithAnonymousPublicRead allows unauthenticated GET/HEAD on object paths to
// pass through (flagged anonymous); the handler then serves only public-read
// objects and 403s otherwise. Off by default — the secure posture.
func (r *Registry) WithAnonymousPublicRead(enabled bool) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anonRead = enabled
	return r
}

// anonCtxKey marks a request that the auth layer admitted without credentials.
type anonCtxKeyT int

const anonCtxKey anonCtxKeyT = 0

// IsAnonymous reports whether the request was admitted as an unauthenticated
// (anonymous) reader — the handler must enforce object ACLs in that case.
func IsAnonymous(ctx context.Context) bool {
	v, _ := ctx.Value(anonCtxKey).(bool)
	return v
}

// isObjectReadPath matches REST object GET/HEAD routes (an object key under
// /v1/files/), where anonymous public-read may apply.
func isObjectReadPath(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	const prefix = "/v1/files/"
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix)
}

// AddKey registers an API key at runtime — used by the Admin API. When a
// persistent store is attached the key is stored hashed (and survives restart);
// otherwise it is held in memory. expiresAt (RFC3339, "" = none) and label are
// only used by the persistent path.
func (r *Registry) AddKey(ctx context.Context, k Key, expiresAt, label string) error {
	r.mu.Lock()
	store := r.store
	cache := r.keyCache
	pub := r.keyChangePublisher
	if store == nil {
		r.keys[k.Token] = k
		r.enabled = true
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if label == "" {
		label = redact(k.Token)
	}
	// Drop any stale cache entry so the new record is observed promptly.
	if cache != nil {
		cache.delete(HashToken(k.Token))
	}
	if err := store.PutAPIKey(ctx, PersistedKey{
		TokenHash: HashToken(k.Token),
		TenantID:  k.Tenant,
		Scopes:    scopesToString(k.Scopes),
		Label:     label,
		ExpiresAt: expiresAt,
	}); err != nil {
		return err
	}
	// Tell other replicas to drop any cached copy of this hash.
	if pub != nil {
		pub(ctx, HashToken(k.Token))
	}
	return nil
}

// RevokeKey removes an API key from the in-memory set and, when present, the
// persistent store. Returns whether anything was removed.
func (r *Registry) RevokeKey(ctx context.Context, token string) (bool, error) {
	r.mu.Lock()
	_, ok := r.keys[token]
	if ok {
		delete(r.keys, token)
	}
	store := r.store
	cache := r.keyCache
	pub := r.keyChangePublisher
	r.mu.Unlock()
	// Invalidate the local cache so a revoke takes effect immediately.
	if cache != nil {
		cache.delete(HashToken(token))
	}
	if store != nil {
		deleted, err := store.DeleteAPIKeyByHash(ctx, HashToken(token))
		if err != nil {
			return false, err
		}
		// Tell other replicas to drop any cached copy of this hash.
		if pub != nil {
			pub(ctx, HashToken(token))
		}
		return ok || deleted, nil
	}
	return ok, nil
}

// ListKeys returns a snapshot of registered keys (tokens redacted), merging the
// in-memory env keys with any persisted keys.
func (r *Registry) ListKeys(ctx context.Context) []Key {
	r.mu.RLock()
	out := make([]Key, 0, len(r.keys))
	for _, k := range r.keys {
		k2 := k
		k2.Token = redact(k.Token)
		out = append(out, k2)
	}
	store := r.store
	r.mu.RUnlock()
	if store != nil {
		if recs, err := store.ListAPIKeys(ctx, ""); err == nil {
			for _, pk := range recs {
				// The plaintext token isn't recoverable; show the stored label.
				out = append(out, Key{Token: pk.Label, Tenant: pk.TenantID, Scopes: parseScopeString(pk.Scopes)})
			}
		}
	}
	return out
}

func redact(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

type ctxKey int

const ctxKeyKey ctxKey = 0

// FromContext extracts the authenticated Key from ctx (if any).
func FromContext(ctx context.Context) (Key, bool) {
	v := ctx.Value(ctxKeyKey)
	if v == nil {
		return Key{}, false
	}
	k, ok := v.(Key)
	return k, ok
}

// Middleware authenticates each request, rejecting missing/invalid keys when
// the registry is enabled. When disabled, requests pass through unchanged.
//
// Authorization header format: "Bearer <token>" or "ApiKey <token>".
func (r *Registry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !r.Enabled() {
				next.ServeHTTP(w, req)
				return
			}
			// Health probes, Prometheus scraping, public UI, OpenAPI doc bypass auth.
			path := req.URL.Path
			if path == "/healthz" || path == "/readyz" || path == "/metrics" ||
				path == "/openapi.json" || path == "/docs" ||
				strings.HasPrefix(path, "/ui") {
				next.ServeHTTP(w, req)
				return
			}
			// AWS SigV4 (S3 clients): verify and resolve to a Key.
			if r.sigv4 != nil && IsSigned(req) {
				k, err := r.sigv4.Verify(req)
				if err != nil {
					forbidden(w, err.Error())
					return
				}
				if k.Tenant != "*" {
					req.Header.Set("X-Aero-Tenant", k.Tenant)
				}
				required := ScopeWrite
				switch req.Method {
				case http.MethodGet, http.MethodHead, http.MethodOptions:
					required = ScopeRead
				}
				if !k.Has(required) {
					forbidden(w, "missing scope: "+string(required))
					return
				}
				decodeStreamingBody(req)
				ctx := context.WithValue(req.Context(), ctxKeyKey, k)
				next.ServeHTTP(w, req.WithContext(ctx))
				return
			}
			token := extractToken(req)
			if token == "" {
				// Anonymous public-read: admit object GET/HEAD without a token;
				// the handler enforces the object's ACL.
				if r.anonRead && isObjectReadPath(req.Method, req.URL.Path) {
					ctx := context.WithValue(req.Context(), anonCtxKey, true)
					next.ServeHTTP(w, req.WithContext(ctx))
					return
				}
				unauthorized(w, "missing Authorization header")
				return
			}
			k, ok := r.Lookup(req.Context(), token)
			if !ok {
				unauthorized(w, "invalid API key")
				return
			}
			// Pin tenant if the key is tenant-scoped.
			if k.Tenant != "*" {
				if hdr := req.Header.Get("X-Aero-Tenant"); hdr != "" && hdr != k.Tenant {
					forbidden(w, "tenant mismatch")
					return
				}
				req.Header.Set("X-Aero-Tenant", k.Tenant)
			}
			// Method-based scope check. Read methods include WebDAV's PROPFIND/PROPPATCH.
			required := ScopeWrite
			switch req.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND", "PROPPATCH":
				required = ScopeRead
			}
			if !k.Has(required) {
				forbidden(w, "missing scope: "+string(required))
				return
			}
			ctx := context.WithValue(req.Context(), ctxKeyKey, k)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	}
}

// Require returns a per-route guard that enforces a scope. Pair with chi:
//
//	r.With(reg.Require(auth.ScopeWrite)).Put(...)
//
// When the registry is disabled, the guard is a pass-through.
func (r *Registry) Require(s Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Use Enabled() (env keys OR jwt OR sigv4 OR store), not the env-keys-only
			// `enabled` field, so a JWT/SigV4/store-only config still enforces scopes.
			if !r.Enabled() {
				next.ServeHTTP(w, req)
				return
			}
			k, ok := FromContext(req.Context())
			if !ok {
				unauthorized(w, "not authenticated")
				return
			}
			if !k.Has(s) {
				forbidden(w, "missing scope: "+string(s))
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		for _, prefix := range []string{"Bearer ", "ApiKey ", "bearer ", "apikey "} {
			if strings.HasPrefix(h, prefix) {
				return strings.TrimSpace(h[len(prefix):])
			}
		}
	}
	// AWS SDK doesn't send Authorization with --no-sign-request; allow
	// X-Api-Key as a fallback for S3-compat callers.
	if h := r.Header.Get("X-Api-Key"); h != "" {
		return h
	}
	return ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="aero-vault"`)
	http.Error(w, msg, http.StatusUnauthorized)
}

func forbidden(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusForbidden)
}
