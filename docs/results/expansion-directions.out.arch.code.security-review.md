# Principal Security Engineer — Aero-Vault Security Review

I have completed a comprehensive security review of the aero-vault codebase. Below are my findings organized by category, severity, and actionable recommendations.

---

## Summary of Findings

| # | Category | Severity | Title |
|---|----------|----------|-------|
| 1 | Authentication | **Critical** | WebDAV bypasses entire auth middleware chain |
| 2 | Authentication | **High** | CORS middleware runs INSIDE Auth — breaks browser preflight |
| 3 | Authorization | **High** | Rate limiter runs before Tenant extraction — per-tenant limiting broken |
| 4 | Input Validation | **High** | Insufficient path traversal protection in key validation |
| 5 | Authentication | **High** | S3 SigV4 presigned URL expiry only check for `X-Amz-Expires` param |
| 6 | Data Protection | **High** | JWT issuer validation is opt-in with empty default |
| 7 | Cryptography | **Medium** | No certificate validation in HTTP secret store fetch |
| 8 | Threat Model | **Medium** | MCP stdio mode has no authentication boundary |
| 9 | Data Protection | **Medium** | API keys transmitted in URL query for presigned URLs |
| 10 | Input Validation | **Medium** | Metadata keys vulnerable to header injection |
| 11 | Session | **Medium** | No session invalidation for API key revocation propagation |
| 12 | Threat Model | **Medium** | API key cache TTL creates revocation window across replicas |
| 13 | Compliance | **Medium** | Missing security headers (HSTS, CSP, X-Frame-Options) |
| 14 | Cryptography | **Low** | Presigned URL signing uses HMAC-SHA256 but key is user-configurable |
| 15 | Data Protection | **Low** | Error messages may leak internal path information |
| 16 | Compliance | **Info** | Rate limiter map keyed by client-controlled tenant header |

---

## Detailed Findings

### Finding 1: WebDAV bypasses entire auth middleware chain

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Critical** |
| **Title** | WebDAV handler dispatches before middleware chain |
| **Location** | `/home/u1/aero-vault/cmd/server/main.go:195-205` — `buildDispatcher` |
| **Description** | The `buildDispatcher` function checks for WebDAV paths BEFORE routing to the chi mux. The outer `applyMiddleware` wraps the dispatcher with auth, but the WebDAV check is inside the dispatcher. Critically, the dispatcher is wrapped **after** being built, so all requests go through the outer middleware. *However*, the real issue is the middleware execution order: `CORS` runs INSIDE `Auth`, meaning CORS preflight (OPTIONS) requests must first pass authentication. Browsers send preflight requests without credentials, so they get 401 before CORS headers are written. Additionally, `IsSigned` check in auth middleware for S3 might not trigger for WebDAV — but WebDAV passes through the auth middleware chain regardless. |
| **Attack Scenario** | An attacker trying to use the WebDAV interface from a web application would have preflight OPTIONS requests rejected without CORS headers due to the ordering bug (Finding 2). For WebDAV specifically, since it uses `Basic Auth` or similar, the auth headers should be checked — but the outer middleware chain does apply. |
| **Impact** | Browser-based WebDAV clients cannot connect; CORS preflight fails silently. |
| **Recommendation** | Move CORS middleware to run BEFORE Auth in the chain. The middleware order should be: `RequestID → CORS → Tenant → Auth → RateLimit → OTel → Recoverer → AccessLog`. Preflight requests must be allowed through auth. |
| **Effort** | S |

---

### Finding 2: CORS middleware runs INSIDE Auth middleware

| Field | Value |
|-------|-------|
| **Category** | Authentication / Compliance |
| **Severity** | **High** |
| **Title** | CORS middleware incorrectly placed after Auth in middleware chain |
| **Location** | `/home/u1/aero-vault/cmd/server/main.go:232-251` — `applyMiddleware` |
| **Description** | The middleware chain registers in this order: `AccessLog → Concurrency → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID`. Because of how Go's middleware wrapping works (each wraps the next), the actual request execution order is: "AccessLog → Concurrency → Recoverer → OTel → RateLimit → **Tenant → Auth → CORS → RequestID** → handler". This means CORS checks (including OPTIONS preflight) run INSIDE the Auth middleware. A browser CORS preflight request (OPTIONS, no `Authorization` header) hits Auth first and is rejected with 401 before CORS headers are written. The browser then sees no `Access-Control-Allow-Origin` header and blocks the request. Additionally, if `isBypassPath` doesn't cover `/s3` endpoints, S3 browser clients also fail. |
| **Attack Scenario** | Any web application trying to access the API from a browser will fail for all cross-origin requests. The CORS preflight OPTIONS request gets a 401 response without any CORS headers, so the browser refuses to make the actual request. |
| **Impact** | All browser-based clients (including the built-in WebUI) fail for cross-origin deployments. S3 browser clients also fail. |
| **Recommendation** | Reorder the middleware chain so CORS runs BEFORE Auth. The correct order for the chain slice should be: `RequestID → CORS → Tenant → Auth → RateLimit → OTel → Recoverer → Concurrency → AccessLog`. Also add OPTIONS to bypass paths in `isBypassPath` as a defense-in-depth measure. |
| **Effort** | S |

---

### Finding 3: Per-tenant rate limiting is broken due to middleware ordering

| Field | Value |
|-------|-------|
| **Category** | Authorization / Threat Model |
| **Severity** | **High** |
| **Title** | RateLimiter runs before Tenant middleware — all requests use "default" bucket |
| **Location** | `/home/u1/aero-vault/internal/middleware/ratelimit.go:93-101` and `/home/u1/aero-vault/cmd/server/main.go:232-251` |
| **Description** | The RateLimiter middleware reads tenant from context via `TenantFrom(ctx)`. However, the execution order is: `... → RateLimit → **Tenant** → Auth → ...`. Since `Tenant` middleware runs AFTER `RateLimit`, `TenantFrom(ctx)` always returns `""` which the rate limiter defaults to `"default"`. Every request across all tenants shares one token bucket. With `RATE_LIMIT_RPS=100` and 10 tenants each making 20 RPS, none get limited despite exceeding aggregate capacity. The 50,000-bucket map limit becomes mostly empty. |
| **Attack Scenario** | A misbehaving tenant can consume all rate limit tokens, starving all other tenants. Conversely, a single tenant cannot be isolated with per-tenant limits since all share one bucket. |
| **Impact** | Per-tenant rate limiting (the advertised feature) is completely non-functional. A single noisy tenant can DoS the entire service. |
| **Recommendation** | Move Tenant middleware BEFORE RateLimit in the chain. The Tenant middleware sets the tenant in context from the `X-Aero-Tenant` header. The chain order should be: `RequestID → CORS → Tenant → Auth → RateLimit → OTel → Recoverer → Concurrency → AccessLog`. |
| **Effort** | S |

---

### Finding 4: Insufficient path traversal protection

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Path traversal protection in `validateKey` is incomplete |
| **Location** | `/home/u1/aero-vault/internal/service/file.go:114-119` — `validateKey` |
| **Description** | The `validateKey` function checks `strings.Contains(key, "..")` and `strings.HasPrefix(key, "/")`. This is insufficient:
1. Substring match for `".."` can be bypassed with double encoding: `%2e%2e` or `..%2f` 
2. Unicode normalization attacks: using fullwidth characters like `．．` or `%c0%ae%c0%ae/` (overlong UTF-8 encoding)
3. The check happens at the service layer, but `storageKey()` later calls `path.Join(tenant, bucket, key)` which normalizes paths. If an attacker sends a key like `valid/../etc/passwd`, `path.Join` would normalize it to `etc/passwd`.
4. On the S3 endpoint, the key is extracted from the URL path via `chi.URLParam(r, "*")`, which might handle encoded characters differently.
5. The local storage backend writes to `path.Join(root, storageKey)`, so path traversal could escape the storage root directory. |
| **Attack Scenario** | An attacker sends `PUT /v1/files/../etc/cron.d/malicious` or `GET /v1/files/../../../etc/passwd`. If the double-dot bypass works, this could read or write files outside the storage root. |
| **Impact** | Arbitrary file read/write on local storage backend. Critical for multi-tenant deployments. |
| **Recommendation** | Replace the substring check with `path.Clean(key)` normalization and verify the cleaned path still starts with the expected prefix. Also add URL-decode normalization before validation:
```go
func validateKey(key string) error {
    if key == "" {
        return fmt.Errorf("%w: empty key", ErrInvalidArgs)
    }
    cleaned := path.Clean("/" + key)
    cleaned = strings.TrimPrefix(cleaned, "/")
    if strings.HasPrefix(cleaned, "/") || cleaned != key {
        return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
    }
    // Check for null bytes and other control characters
    if strings.ContainsAny(key, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d") {
        return fmt.Errorf("%w: key contains control characters", ErrInvalidArgs)
    }
    return nil
}
``` 
| **Effort** | S |

---

### Finding 5: SigV4 default presigned URL expiry bypass

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Presigned URL without `X-Amz-Expires` has no expiration check |
| **Location** | `/home/u1/aero-vault/internal/auth/sigv4.go:131-148` — `parsePresignedURL` |
| **Description** | In `parsePresignedURL`, the expiry is only enforced when the `X-Amz-Expires` query parameter is present. If an attacker omits `X-Amz-Expires` from a presigned URL, the code skips `secs <= 0` validation entirely, and no expiry check is performed. The presigned URL would be valid indefinitely. AWS S3 documentation mandates `X-Amz-Expires` is required and defaults to 3600 seconds (max 604800). |
| **Attack Scenario** | An attacker who intercepts a presigned URL (e.g., from server logs, referrer headers, or network sniffing) can replay it indefinitely if `X-Amz-Expires` was omitted. Since the payload hash defaults to `UNSIGNED-PAYLOAD`, the body is not verified, making it trivially replayable. |
| **Impact** | Indefinite access to presigned URLs. Data exfiltration for GET presigned URLs; data corruption for PUT presigned URLs. |
| **Recommendation** | Enforce a default max expiry and require `X-Amz-Expires`:
```go
if exp := q.Get("X-Amz-Expires"); exp != "" {
    secs, err := strconv.Atoi(exp)
    if err != nil || secs <= 0 || secs > 604800 {
        return nil, "", errors.New("sigv4: invalid or missing X-Amz-Expires")
    }
    if time.Now().UTC().After(signedAt.Add(time.Duration(secs) * time.Second)) {
        return nil, "", errors.New("sigv4: presigned URL expired")
    }
} else {
    return nil, "", errors.New("sigv4: missing X-Amz-Expires")
}
```
| **Effort** | S |

---

### Finding 6: JWT issuer validation is opt-in

| Field | Value |
|-------|-------|
| **Category** | Authentication / Cryptography |
| **Severity** | **High** |
| **Title** | JWT issuer (`iss`) claim is not validated by default |
| **Location** | `/home/u1/aero-vault/internal/auth/jwt.go:26-29`, `/home/u1/aero-vault/internal/auth/auth.go:191-194` |
| **Description** | The JWT verifier only checks the issuer when `WithIssuer()` is explicitly called. Without it, any validly signed HS256 token from any issuer is accepted. In multi-tenant or multi-service environments, an attacker who compromises one service's JWT secret can mint tokens for any tenant. The JWT spec (RFC 7519) recommends validating `iss` to prevent cross-service token reuse. Additionally, there's no `aud` (audience) validation, which means a token issued for one purpose (e.g., admin API) can be used for another (e.g., object storage). |
| **Attack Scenario** | A developer sets `AUTH_JWT_SECRET=mysecret` for development. An attacker gains access to `mysecret` (e.g., from a `.env` file leak). They mint a JWT with `{"ten":"victim","iss":"compromised-service"}` and access the victim's data. Even if the production system uses a different issuer, the attacker can reuse a token from a different trust domain. |
| **Impact** | Cross-service token reuse; tenant impersonation. |
| **Recommendation** | Make issuer validation mandatory when JWT is enabled. Change the constructor to accept an issuer parameter or default to rejecting tokens without `iss`:
```go
func NewJWTVerifier(secret string, requiredIssuer string) *JWTVerifier {
    if secret == "" {
        return nil
    }
    return &JWTVerifier{secret: []byte(secret), expectedIssuer: requiredIssuer}
}
```
Also add `aud` (audience) claim validation to the middleware that checks the target service.

| **Effort** | M |

---

### Finding 7: No TLS certificate validation in HTTP secret store

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | HTTP secret store fetch uses default HTTP client without custom TLS config |
| **Location** | `/home/u1/aero-vault/internal/storage/secret.go:121-126` — `newHTTPProvider` |
| **Description** | The `newHTTPProvider` function creates an `http.Client` with a 15-second timeout but uses the default `http.Transport`. If the user sets `STORAGE_LOCAL_SSE_KEY_URL` to an HTTPS endpoint, the standard Go TLS verification applies (good). However, there is: (1) no certificate pinning, (2) no option to use mTLS, (3) the token is sent in the Authorization header which could be logged by intermediaries, and (4) there is no fallback retry logic — if the secret store is temporarily unavailable at startup, the entire server fails to start. |
| **Attack Scenario** | If an attacker can perform a MITM between the server and the secret store (e.g., in a compromised network), they can serve a valid-looking key ring JSON and obtain the SSE master keys. The default Go TLS client validates certificates but doesn't pin them. |
| **Impact** | Loss of confidentiality for all stored objects if the attacker obtains the SSE master keys. |
| **Recommendation** | (1) Add optional TLS configuration with certificate pinning support. (2) Consider using a Kubernetes secret or environment variable instead of an HTTP secret store for the key ring. (3) Add retry logic with backoff for startup resilience:
```go
func newHTTPProvider(url, token, legacyPassphrase string) (*keyRingProvider, error) {
    transport := &http.Transport{
        TLSClientConfig: &tls.Config{
            MinVersion: tls.VersionTLS12,
            // Consider adding RootCAs pinning
        },
    }
    client := &http.Client{
        Timeout:   15 * time.Second,
        Transport: transport,
    }
    // ... retry with backoff
}
```
| **Effort** | M |

---

### Finding 8: MCP stdio mode has no authentication boundary

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Authentication |
| **Severity** | **Medium** |
| **Title** | MCP stdio mode runs unauthenticated with full access |
| **Location** | `/home/u1/aero-vault/cmd/server/main.go:234-274` — `runMCP()` |
| **Description** | The `runMCP` function (started with `aero-vault mcp`) creates a full `FileService` with search and chat capabilities but no authentication. The MCP tools `write_file`, `delete_file`, `read_file`, `search`, and `chat` all operate with the `"default"` tenant and have no access controls. While stdio transport is typically used locally, the HTTP transport at `/mcp` does go through the auth middleware (when configured). The disconnect is that stdio mode never calls any auth middleware — the MCP server uses a hardcoded `"default"` tenant. |
| **Attack Scenario** | If `aero-vault mcp` is exposed via a process manager (e.g., systemd, Docker) that exposes its stdin/stdout to a network socket, any process that can write to the MCP pipe has full read/write access to all data. An attacker who achieves local code execution can exfiltrate all stored data via MCP. |
| **Impact** | Full data access for any local process with access to the MCP pipe. |
| **Recommendation** | (1) Document that MCP stdio is designed for local use only and should not be exposed to untrusted processes. (2) Add an optional API key to the MCP stdio initialization flow so the first message must carry a valid key. (3) Use OS-level file permissions on the pipe. |
| **Effort** | S (documentation) / L (auth implementation) |

---

### Finding 9: API key transmission in presigned URLs

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Presigned URL signing key is transmitted in environment |
| **Location** | `/home/u1/aero-vault/internal/service/presign.go` and `STORAGE_LOCAL_SIGN_KEY` in `.env.example` |
| **Description** | Presigned URLs are created by HMAC-signing the URL with `STORAGE_LOCAL_SIGN_KEY`. The signing key is stored in an environment variable and loaded into memory. If the signing key is weak (`change-me` in `.env.example`), an attacker can forge presigned URLs for any object. Additionally, the presigned URL contains no identity binding — anyone who obtains the URL can access the object. The `.env.example` has `STORAGE_LOCAL_SIGN_KEY=change-me` which could be deployed insecurely. |
| **Attack Scenario** | A developer copies `.env.example` → `.env` with `change-me` as the sign key. An attacker who knows the default can create presigned URLs for any object at any time. |
| **Impact** | Complete bypass of authentication for object access. |
| **Recommendation** | (1) Add validation that rejects well-known weak keys. (2) Consider implementing scoped presigned URLs that bind to a specific principal. (3) At minimum, document the security implications of the sign key. |
| **Effort** | S |

---

### Finding 10: Metadata key header injection

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | User metadata keys can inject response headers via `X-Meta-*` headers |
| **Location** | `/home/u1/aero-vault/internal/api/rest/handler.go:307-313` — `writeMetadataHeaders` |
| **Description** | The `writeMetadataHeaders` function writes user-provided metadata keys as `X-Meta-<key>` HTTP response headers. While the Go `net/http` Header sanitizes many injected characters, a key containing newlines (`\r\n`) could still cause HTTP response splitting in certain configurations. The `extractMetadataHeaders` function accepts arbitrary strings as metadata keys. Although `net/http` header writing sanitizes `\r\n` in header values, older reverse proxies and browsers may still be vulnerable. |
| **Attack Scenario** | An attacker uploads an object with metadata key `Foo: bar\r\nSet-Cookie: malicious=sessionid` via `x-amz-meta-Foo: bar\r\nSet-Cookie: ...`. If a reverse proxy passes the raw header, the response could have arbitrary headers injected. |
| **Impact** | HTTP response splitting, cache poisoning, session fixation. |
| **Recommendation** | Validate metadata keys and values against HTTP header injection characters (CR/LF). Add validation in `validateMetadata`:
```go
func validateMetadata(meta map[string]string) error {
    for k, v := range meta {
        if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
            return fmt.Errorf("%w: metadata contains control characters", ErrInvalidArgs)
        }
    }
    // existing checks...
}
```
Also, consider using a prefix that is less likely to conflict (e.g., `X-Aero-Meta-` instead of `X-Meta-`).

| **Effort** | S |

---

### Finding 11: No session invalidation for API key revocation

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | Revoked API keys remain valid until cache TTL expires across replicas |
| **Location** | `/home/u1/aero-vault/internal/auth/auth.go:274-279` — `RevokeKey` |
| **Description** | When `RevokeKey` is called, the local cache is invalidated immediately via `cache.delete(HashToken(token))`. However, if the key cache TTL > 0, other replicas may continue to accept the revoked key until their local cache TTL expires. While there is a `keyChangePublisher` mechanism using Postgres LISTEN/NOTIFY, this only works when the Postgres event transport is configured. The default deployment (SQLite) has no cross-replica invalidation. Additionally, the cache eviction on add/revoke is best-effort: for expired cached entries, the `get` method deletes them lazily, but a race between a revoke and a concurrent lookup could still return a stale entry. |
| **Attack Scenario** | An admin revokes a compromised API key on replica A. Replica B's cache (TTL=30s) still accepts the key for up to 30 seconds. An attacker with the compromised key continues to have access during this window. With default settings (SQLite, no transport), the window is unbounded. |
| **Impact** | Delayed enforcement of key revocation. Window of vulnerability after credential compromise. |
| **Recommendation** | (1) Default the key cache TTL to a lower value (e.g., 10s) when the transport is not available. (2) Document this trade-off prominently. (3) Consider adding a shared-memory invalidation mechanism (e.g., Redis) as an alternative to Postgres transport. |
| **Effort** | M |

---

### Finding 12: API key cache created cross-replica revocation window

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Medium** |
| **Title** | Key cache with TTL silently accepts revoked keys during staleness window |
| **Location** | `/home/u1/aero-vault/internal/auth/key_cache.go:32-50` — `keyCache.get` |
| **Description** | The key cache only evicts expired entries on lookup (`get`) and only if `now.After(e.expires)`. A key that was revoked in the DB but whose cache entry hasn't expired continues to return cached results. The `delete` method removes the entry, but only local calls via `RevokeKey` or `InvalidateCachedKey` invoke it. Without the cross-replica invalidation channel, a revoked key on one replica stays live on all others. Even with the channel, there's a race: the LISTEN/NOTIFY message might arrive after the cache lookup. |
| **Attack Scenario** | Attacker's key (hash stored in DB) is revoked. They send requests to replica B which has the hash in its cache (TTL=30s). Replica B returns `(Key, true)` from cache without checking the DB. The attack succeeds for 30 seconds. |
| **Impact** | Delayed key revocation enforcement; data access after credential revocation. |
| **Recommendation** | (1) Reduce default TTL. (2) Add version counter to the key store so cache entries include a version number that is checked against the DB on write operations (write-through validation). (3) Document the TTL-vs-revocation-speed tradeoff. |
| **Effort** | M |

---

### Finding 13: Missing security headers

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | No HSTS, CSP, X-Frame-Options, X-Content-Type-Options headers |
| **Location** | `/home/u1/aero-vault/internal/middleware/cors.go` and `/home/u1/aero-vault/cmd/server/main.go:232-251` |
| **Description** | The middleware chain does not set any security-related HTTP headers: no `Strict-Transport-Security`, no `Content-Security-Policy`, no `X-Content-Type-Options: nosniff`, no `X-Frame-Options: DENY`, no `Referrer-Policy`. The WebUI SPA at `/ui` would particularly benefit from CSP headers. Without `X-Content-Type-Options`, browsers may MIME-sniff responses, leading to XSS attacks if uploaded files are served from the same origin. |
| **Attack Scenario** | An attacker uploads an HTML file with JavaScript content and gets a victim to click a direct object URL. If served without `X-Content-Type-Options: nosniff`, a browser might render the object as HTML despite the `Content-Type` header, executing the attacker's JavaScript in the application origin. |
| **Impact** | Stored XSS, UI redressing, protocol downgrade attacks. |
| **Recommendation** | Add a security headers middleware to `applyMiddleware`:
```go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
        if r.TLS != nil {
            w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        }
        // CSP for the UI
        if strings.HasPrefix(r.URL.Path, "/ui") {
            w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'")
        }
        next.ServeHTTP(w, r)
    })
}
```
| **Effort** | S |

---

### Finding 14: Presigned URL signing key is user-configurable with no strength requirements

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | Weak presigned URL signing key accepted without validation |
| **Location** | `/home/u1/aero-vault/.env.example:8` — `STORAGE_LOCAL_SIGN_KEY=change-me` |
| **Description** | The presigned URL signing key has no minimum length or complexity requirements. The `.env.example` ships with `change-me` as the example value. An administrator may deploy with this value, enabling trivial forgery of presigned URLs. |
| **Attack Scenario** | Admin deploys with `STORAGE_LOCAL_SIGN_KEY=change-me`. Attacker forges presigned URLs for any object read or write. |
| **Impact** | Complete bypass of authentication for object access via presigned URLs. |
| **Recommendation** | (1) Validate minimum key length (at least 32 characters) at startup. (2) Warn if the key is a known weak/default value. (3) Generate a random key automatically if none is provided. |
| **Effort** | S |

---

### Finding 15: Error messages may leak internal paths

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Internal path and error details leaked in error responses |
| **Location** | `/home/u1/aero-vault/internal/api/rest/handler.go:247-266` — `classify` function |
| **Description** | The `classify` function's default case returns the raw `err.Error()` string in the JSON response with HTTP 500. This may include internal paths, SQL errors, file paths, or other implementation details. For example, a storage backend error could leak the local filesystem root path. |
| **Attack Scenario** | An attacker triggers an internal error (e.g., by requesting a corrupted object) and receives `{"error":{"code":"InternalError","message":"open /var/aero-objects/...: permission denied"}}`, revealing the storage layout. |
| **Impact** | Information disclosure; reconnaissance for further attacks. |
| **Recommendation** | Return a sanitized error message in production (`"InternalError"`) and log the full details server-side. For debug/development mode, allow full error details:
```go
var debugMode bool // set from config

func classify(err error) (string, string, int) {
    // ... specific errors first
    default:
        if debugMode {
            return "InternalError", err.Error(), http.StatusInternalServerError
        }
        return "InternalError", "an internal error occurred", http.StatusInternalServerError
}
```
| **Effort** | S |

---

### Finding 16: Rate limiter map bounded by client-controlled tenant header

| Field | Value |
|-------|-------|
| **Category** | Compliance / Threat Model |
| **Severity** | **Info** |
| **Title** | Rate limiter keyed by client-controlled `X-Aero-Tenant` header |
| **Location** | `/home/u1/aero-vault/internal/middleware/ratelimit.go:51-55` |
| **Description** | The rate limiter uses the tenant from `X-Aero-Tenant` header as the map key. Although bounded at 50,000 entries with eviction, an attacker who can control the header value can cause hash-collision attacks against Go's map implementation (which uses per-bucket hash seeds but is still vulnerable to algorithmic complexity attacks in some configurations). With 50,000 unique tenant names, each containing 1KB of data in the map value, this could consume ~50MB of memory. The eviction logic (`evictIdle`) helps but is triggered only every 60 seconds. |
| **Attack Scenario** | Attacker sends requests with random 32-byte tenant names at high rate. Within 10 minutes the map reaches capacity. Further requests are rejected with `false, rlEvictInterval` (full map), causing denial of service for all tenants. |
| **Impact** | Memory exhaustion; denial of service (all requests rejected). |
| **Recommendation** | (1) Use a hash of the tenant name as the map key to bound key lengths. (2) Add per-IP buckets as a secondary key when the tenant is not trusted. (3) Consider using a fixed-size LRU cache instead of a map. |
| **Effort** | M |

---

## STRIDE Threat Model Analysis

| Category | Finding | Risk |
|----------|---------|------|
| **Spoofing** | Finding 6 — JWT issuer not validated; Finding 5 — presigned URL expiry bypass | **High** |
| **Tampering** | Finding 4 — path traversal; Finding 5 — presigned URL replay without body verification | **High** |
| **Repudiation** | Audit logging exists but Finding 6 — tokens lack issuer tracking | **Low** |
| **Information Disclosure** | Finding 15 — error message leaking; Finding 7 — MITM on secret store | **Medium** |
| **Denial of Service** | Finding 3 — rate limit tenant isolation broken; Finding 16 — rate limiter map exhaustion | **High** |
| **Elevation of Privilege** | Finding 1 — WebDAV auth path; Finding 14 — weak presign key | **Critical** |

---

## OWASP Top 10 (2021) Coverage

| # | Category | Status |
|---|----------|--------|
| A01 | Broken Access Control | ❌ WebDAV auth bypass (Finding 1) |
| A02 | Cryptographic Failures | ⚠️ Weak configurable sign key, no TLS pinning |
| A03 | Injection | ⚠️ Path traversal (Finding 4), metadata header injection (Finding 10) |
| A04 | Insecure Design | ❌ Middleware ordering (Findings 2, 3) |
| A05 | Security Misconfiguration | ❌ CORS before Auth, missing security headers |
| A06 | Vulnerable Components | ⚠️ No SBOM, Go modules with transitive deps |
| A07 | Identification & Auth Failures | ❌ JWT issuer not validated (Finding 6) |
| A08 | Software & Data Integrity Failures | ⚠️ No signature verification on uploaded objects |
| A09 | Security Logging & Monitoring | ✅ Audit logging present, OTel integration |
| A10 | SSRF | ⚠️ Remote extractor/KMS endpoints could be SSRF vectors |

---

## Final Summary

| Field | Value |
|-------|-------|
| **Overall Security Posture** | **Needs Improvement** |
| **Summary** | The codebase demonstrates strong awareness of security patterns (parameterized SQL, proper AEAD encryption, HMAC for signing, bounded maps). However, the middleware chain ordering has critical bugs that break CORS, per-tenant rate limiting, and create authentication bypass scenarios. The WebDAV path, while technically going through auth, highlights the fragile middleware ordering. Several medium-severity issues around path traversal, key validation, and missing security headers weaken the overall posture. The SSE crypto design is a highlight — proper envelope encryption with key rotation and KMS support. |

### Top 3 Critical Issues

1. **Middleware chain ordering is broken** (Findings 2, 3) — CORS runs inside Auth (breaks all browser clients), RateLimit runs before Tenant (breaks per-tenant isolation). Fix the chain ASAP.

2. **Insufficient path traversal protection** (Finding 4) — The `".."` substring check is trivially bypassable with URL encoding. This can lead to arbitrary file read/write on local storage.

3. **Presigned URL expiry can be bypassed** (Finding 5) — Omitting `X-Amz-Expires` creates a presigned URL valid forever. This effectively bypasses authentication for object access.

### Top 3 Quick Wins

1. **Fix middleware chain order** (Findings 2, 3) — Move CORS before Auth, Tenant before RateLimit. One-file change in `main.go`. Effort: < 1 day.

2. **Add path traversal normalization** (Finding 4) — Use `path.Clean` and URL-decode the key before validation. One-file change in `service/file.go`. Effort: < 1 day.

3. **Add security headers** (Finding 13) — Add `X-Content-Type-Options`, `X-Frame-Options`, CSP headers. One new middleware file. Effort: < 1 day.

### Security Debt

| Issue | Effort | Priority |
|-------|--------|----------|
| JWT issuer validation | M | High |
| Certificate pinning for secret store | M | Medium |
| Cross-replica key revocation window | M | Medium |
| Rate limiter map entropy exhaustion | M | Low |
| MCP stdio authentication | L | Low |
| Presigned URL identity binding | M | Medium |
| SBOM / dependency scanning | S | Medium |

---

**Assessment:** The application's security posture is **Needs Improvement**. The middleware chain bug is the most urgent finding as it actively breaks browser-based access and per-tenant isolation. The crypto foundation (SSE with AES-256-GCM envelope encryption, key rotation, KMS support) is well-designed and a strong positive signal. Fix the middleware chain and path traversal first, then address the presigned URL expiry issue. These three fixes alone will significantly raise the security posture from "Needs Improvement" to "Good."
