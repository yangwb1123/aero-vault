Now I have a comprehensive understanding of the entire codebase. Let me compile the full security review.

---

# 🔒 Principal Security Engineer — Aero-Vault Subsystem Review

## Executive Summary

**Overall Security Posture: Needs Improvement**

The codebase demonstrates strong security awareness in several areas (parameterized SQL, AEAD encryption, proper nonce generation, audit logging), but has **critical gaps** in authentication enforcement on certain endpoints and secrets management. The most urgent issue is the `/mcp` endpoint, which is mounted without authentication middleware and provides full read/write/delete access when the server has auth disabled (the default configuration). Combined with plaintext secrets in environment variables, this creates a direct path to unauthorized data access.

---

## Finding 1: MCP HTTP Endpoint Lacks Authentication

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **🔴 Critical** |
| **Title** | MCP `/mcp` endpoint bypasses auth middleware |
| **Location** | `cmd/server/main.go:246` — `r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))` |
| **Description** | The MCP HTTP endpoint at `/mcp` is mounted directly on the chi router (`r`), NOT through the `applyMiddleware` chain. The middleware chain wraps the entire `dispatcher` handler, but the MCP route is registered on the inner router that the dispatcher delegates to. More critically, the MCP handler itself never calls `auth.Registry.Middleware()` — tools such as `list_files`, `read_file`, `write_file`, `delete_file`, `search`, and `chat` are fully accessible without any authentication. When auth is disabled (the default — `AUTH_KEYS=""`), this provides unauthenticated full CRUD access to all data. |
| **Attack Scenario** | 1. Attacker discovers `POST /mcp` endpoint (it's not in the bypass list but will be served). 2. Sends JSON-RPC `tools/call` with method `write_file` or `read_file`. 3. Reads or modifies any object in the default tenant without any credentials. 4. With a list of bucket/keys obtained from `list_files`, exfiltrates all data. |
| **Impact** | Complete data compromise — read, write, and delete all objects without authentication. Data exfiltration, data destruction, ransomware scenario. |
| **Recommendation** | Apply the auth middleware to the MCP route. Either mount it inside the `v1` router or explicitly apply `authReg.Middleware()` to the MCP handler: |
| **Effort** | S (< 1 day) |

```go
// In buildRouter, either:
// Option A: Apply auth wrapper
r.Method(http.MethodPost, "/mcp", authReg.Middleware()(mcp.HTTPHandler(mcpServer)))

// Option B (better): Require at minimum read scope
mcpWithAuth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // Auth check
    if authReg.Enabled() {
        if _, ok := auth.FromContext(r.Context()); !ok {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
    }
    mcp.HTTPHandler(mcpServer).ServeHTTP(w, r)
})
r.Method(http.MethodPost, "/mcp", mcpWithAuth)

// Option C (recommended): Move MCP under /v1/ to inherit its middleware
r.Mount("/v1/mcp", mcpHTTPRouter)
```

---

## Finding 2: API Keys and SigV4 Secrets in Plaintext Environment Variables

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Secrets Management |
| **Severity** | **🔴 Critical** |
| **Title** | API keys and SigV4 secrets stored in plaintext environment variables |
| **Location** | `internal/auth/auth.go:61` (`auth.Parse` reads `AUTH_KEYS`), `internal/auth/sigv4.go:62` (`ParseSigV4Credentials` reads `S3_SIGV4_CREDENTIALS`) |
| **Description** | API keys are passed as plaintext colon-delimited tokens in the `AUTH_KEYS` environment variable. Similarly, S3 SigV4 access keys and secret keys are passed in plaintext via `S3_SIGV4_CREDENTIALS`. Both are parsed at startup and held in memory as plaintext strings. The env-seeded keys in `Registry.keys` are never hashed — they are stored and compared as plaintext. Persisted keys are correctly hashed with SHA-256, but the in-memory env keys are not. If an attacker gains process memory access or env dump, all credentials are immediately recoverable. |
| **Attack Scenario** | 1. Attacker exploits a server-side request forgery (SSRF), debug endpoint, or `/proc/self/environ` leak. 2. Reads `AUTH_KEYS` and `S3_SIGV4_CREDENTIALS` from the environment. 3. Uses the extracted keys to authenticate as any tenant, including `*` (admin). 4. Exfiltrates all data, escalates privileges. |
| **Impact** | Full credential compromise leading to data breach, privilege escalation, and lateral movement. |
| **Recommendation** | 1. Add a `WithSecretsProvider` method to `Registry` that reads from a secrets vault (HashiCorp Vault, AWS Secrets Manager, encrypted file). 2. For in-memory env keys, zero-out the token after hashing: store a SHA-256 hash of the env key (same as persisted keys) and compare hashes at lookup time. 3. Document that `AUTH_KEYS` should only be used in development; production must use persisted keys or a vault. |
| **Effort** | M (1-3 days) |

```go
// In auth.Parse: immediately hash and discard plaintext
func Parse(raw string) (*Registry, error) {
    reg := &Registry{keys: map[string]Key{}}
    // ... parse as before ...
    for _, rec := range parts {
        // ... extract fields ...
        tokenHash := HashToken(k.Token)
        // Store hashed version only
        k.Token = "" // clear plaintext
        reg.hashedKeys[tokenHash] = k
    }
    return reg, nil
}

// Lookup becomes:
func (r *Registry) Lookup(ctx context.Context, token string) (Key, bool) {
    hash := HashToken(token)
    k, ok := r.hashedKeys[hash]
    if ok { return k, true }
    // ... continue with store lookup ...
}
```

---

## Finding 3: No Authentication on Web UI and OpenAPI Endpoints

| Field | Value |
|-------|-------|
| **Category** | Authentication / Information Disclosure |
| **Severity** | **🟠 High** |
| **Title** | `/ui`, `/docs`, `/openapi.json` bypass authentication and may leak attack surface |
| **Location** | `internal/auth/auth_middleware.go:28-29` — `isBypassPath()` and `cmd/server/main.go:244` |
| **Description** | The auth bypass list includes `/ui`, `/docs`, and `/openapi.json`. While this is intentional (health checks, docs readability), `openapi.json` exposes the complete API specification — including all admin endpoints — to anyone on the network without authentication. Combined with the MCP auth gap (Finding 1), an unauthenticated attacker can learn every available endpoint from the OpenAPI spec. The Web UI (`/ui`) also gets no auth even though it provides search, file listing, and chat functionality. |
| **Attack Scenario** | 1. Attacker fetches `GET /openapi.json` to discover all API endpoints. 2. Identifies admin-only routes: `/v1/admin/keys`, `/v1/admin/tenants`, `/v1/admin/jwt`. 3. If auth is accidentally disabled (default config), accesses these directly. 4. If auth is enabled but MCP gap exists, uses MCP to pivot. |
| **Impact** | Information disclosure of API surface area. In the default (no-auth) configuration, full admin access. |
| **Recommendation** | 1. Gate `/openapi.json` behind authentication in production. 2. Add auth to `/ui` with session management or an API-key-based cookie. 3. At minimum, document clearly that these endpoints must be firewalled in production. |
| **Effort** | S (< 1 day) |

---

## Finding 4: Rate Limiter Tenant-Bucket Spoofing via `X-Aero-Tenant` Header

| Field | Value |
|-------|-------|
| **Category** | Authorization / Denial of Service |
| **Severity** | **🟠 High** |
| **Title** | Rate limiter uses user-controlled `X-Aero-Tenant` header as bucket key |
| **Location** | `internal/middleware/ratelimit.go:92-93` — `isAllowed()` reads tenant from context, which originates from `X-Aero-Tenant` header |
| **Description** | The rate limiter creates per-tenant token buckets keyed by the `X-Aero-Tenant` header value. An attacker can spoof this header to impersonate any tenant's rate limit bucket, either exhausting another tenant's budget or bypassing their own limits by cycling through random tenant IDs. Additionally, the map is bounded at 50,000 entries but this creates a memory exhaustion attack vector: an attacker can send requests with 50,000 different `X-Aero-Tenant` values to fill the map, then a 51,001st request slows to a crawl as it triggers the eviction path. The eviction itself is O(n) with 50k entries under `rl.mu`, blocking ALL rate-limited requests during eviction. |
| **Attack Scenario** | 1. Attacker sends requests with random `X-Aero-Tenant: victim-a`, `X-Aero-Tenant: victim-b`, etc. 2. Legitimate tenant "acme-corp" gets rate-limited because its token bucket was exhausted by the attacker. 3. OR: Attacker sends 50,001 unique tenant values, triggering O(50k) eviction scans under a global mutex, causing request latency spikes for all tenants. |
| **Impact** | Cross-tenant rate-limit exhaustion (DoS) and potential global latency degradation. |
| **Recommendation** | 1. Use the authenticated tenant identity from `auth.FromContext(ctx)` instead of the user-supplied header. 2. Only fall back to the header for unauthenticated anonymous requests. 3. Reduce `rlMaxBuckets` to a smaller value (e.g. 10,000). 4. Add random sampling to eviction instead of full scan. |
| **Effort** | M (1-2 days) |

```go
func (rl *RateLimiter) isAllowed(ctx context.Context) (bool, time.Duration) {
    // Prefer authenticated tenant identity
    if k, ok := auth.FromContext(ctx); ok {
        return rl.Allow(k.Tenant)
    }
    // Fall back to header only for anonymous
    t := TenantFrom(ctx)
    if t == "" {
        t = "default"
    }
    return rl.Allow(t)
}
```

---

## Finding 5: S3 Policy Parser Accepts Arbitrary JSON with No Validation

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **🟠 High** |
| **Title** | Bucket policy parser accepts unvalidated JSON with wildcard expansion risks |
| **Location** | `internal/auth/policy.go:80-86` — `ParsePolicy()` does basic unmarshal, `matchesAction()` does prefix wildcard matching |
| **Description** | The bucket policy parser accepts any valid JSON without schema validation, action whitelisting, or resource validation. The `matchesAction()` function matches prefix wildcards (e.g., `s3:*` matches everything), and there is no validation that `Resource` is actually enforced — the `Eval()` function never checks `matchesResource()`. This means a policy that specifies `"Resource": "arn:aws:s3:::specific-bucket/*"` is silently ignored; every statement that matches action/principal/conditions is applied regardless of resource. A user can write `{"Effect":"Allow","Action":"s3:*","Principal":"*","Resource":"*"}` and it will grant full access despite the admin having intended to scope it to a specific path. |
| **Attack Scenario** | 1. Admin sets a bucket policy: `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Principal":"*","Resource":"arn:aws:s3:::bucket/public/*"}]}` intending to allow only `public/` prefix. 2. The `Resource` field is parsed but never checked — the policy grants `s3:GetObject` to ALL objects in the bucket. 3. An unauthenticated user accesses any object in the bucket, including private data. |
| **Impact** | Bucket policy bypass: resource restrictions are silently ignored. Data exposure via misconfigured policies. |
| **Recommendation** | 1. Implement proper JSON schema validation for policy documents. 2. Implement `matchesResource()` in `Eval()` to enforce resource constraints. 3. At minimum, log a warning when a policy has non-empty `Resource` fields so admins know they are not enforced. 4. Add action allow-listing to prevent dangerous patterns. |
| **Effort** | M (2-3 days) |

```go
func (p *Policy) Eval(action, resource, sourceIP string) PolicyEffect {
    allow := false
    for _, stmt := range p.Statement {
        if !stmt.matchesAction(action) { continue }
        if !stmt.matchesResource(resource) { continue }  // MISSING
        if !stmt.matchesPrincipal() { continue }
        if !stmt.matchesConditions(sourceIP) { continue }
        // ...
    }
    return EffectImplicitDeny
}
```

---

## Finding 6: Webhook Dead-Letter Transition Loses Failure Data

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Repudiation |
| **Severity** | **🟡 Medium** |
| **Title** | Webhook retries conflate "dead-letter" with "succeeded" causing silent data loss |
| **Location** | `internal/events/webhook.go:199-200` — `MarkWebhookSucceeded` called on dead-lettered events |
| **Description** | After 10 failed retry attempts, the webhook retry loop calls `MarkWebhookSucceeded` to permanently stop retrying. The comment explains this is because the schema lacks a "dead-letter" state, but this means operators cannot distinguish between successfully delivered webhooks and those that were permanently abandoned. Additionally, the maximum 10 retries mean events can be lost after ~8.5 hours (30s, 1m, 2m, 4m, 8m, 16m, 32m, 64m, 128m, 256m = ~8.5h). There is no dead-letter queue (DLQ) or alerting when events reach the dead-letter state. |
| **Attack Scenario** | 1. Webhook endpoint is down for 9 hours (e.g., during maintenance). 2. All events during this period are retried 10 times and then permanently marked as "succeeded." 3. The operator sees no failed webhooks in the failure listing. 4. Critical security events (object.deleted, audit events) are permanently lost without detection. |
| **Impact** | Silent data loss of webhook events, including security-relevant events (access logs, object mutations). Loss of audit trail. |
| **Recommendation** | 1. Add a `status` column to `webhook_failures` (enum: `pending`, `succeeded`, `dead_letter`). 2. Add an alerting mechanism (webhook or log line) when events are dead-lettered. 3. Consider a configurable max_attempts. 4. Add a recovery mechanism for operators to re-queue dead-lettered events. |
| **Effort** | M (1-2 days) |

---

## Finding 7: JWT Revocation Not Supported

| Field | Value |
|-------|-------|
| **Category** | Authentication / Session Management |
| **Severity** | **🟡 Medium** |
| **Title** | No JWT revocation mechanism — compromised tokens remain valid until expiry |
| **Location** | `internal/auth/jwt.go` — `Verify()` checks only signature and expiry |
| **Description** | JWTs are verified purely via signature and expiry. There is no blocklist/deny-list mechanism. Once a JWT is issued via `POST /v1/admin/jwt`, it cannot be revoked. The Admin API has `RevokeKey` for API keys but no corresponding `RevokeJWT` or `AddJWTToDenyList`. Combined with HS256 (symmetric key) — where anyone who knows the secret can mint valid tokens — a compromised `AUTH_JWT_SECRET` means an attacker can forge tokens with arbitrary scopes that cannot be revoked until expiry. |
| **Attack Scenario** | 1. Attacker steals `AUTH_JWT_SECRET` from environment/memory. 2. Forges a JWT with `{"ten":"*","scopes":["admin"],"exp":9999999999}`. 3. Uses the forged JWT to access all data as operator/admin. 4. Even after secret rotation, the forged JWT remains valid until its expiry date. |
| **Impact** | Long-lived forged tokens, inability to revoke compromised credentials. |
| **Recommendation** | 1. Add an in-memory or Redis-backed JWT deny list checked during `Verify()`. 2. Add `POST /v1/admin/jwt/revoke` that adds `jti` (JWT ID) to the deny list. 3. Add short TTLs (15-60 min) for issued JWTs by default. 4. Consider RS256/ES256 instead of HS256 so only the issuer can mint tokens. |
| **Effort** | M (2-3 days) |

---

## Finding 8: No Security Headers on HTTP Responses

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **🟡 Medium** |
| **Title** | Missing security headers: HSTS, CSP, X-Content-Type-Options, X-Frame-Options |
| **Location** | `cmd/server/main.go:252-274` — `applyMiddleware` applies CORS but no security headers |
| **Description** | The server sets no security-related HTTP headers beyond CORS. The following headers are absent: `Strict-Transport-Security` (HSTS), `Content-Security-Policy` (CSP), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` (or SAMEORIGIN), `Referrer-Policy`, and `Permissions-Policy`. While the REST API primarily returns JSON (limiting XSS surface), the Web UI endpoint at `/ui` and the SSE event-stream at `/v1/events/stream` are served without these protections. The SSE endpoint is particularly concerning as it streams user-controlled data (object events) that could be rendered in a browser context. |
| **Attack Scenario** | 1. Attacker uploads an HTML file containing JavaScript to the object store. 2. User accesses the file via the Web UI or direct GET. 3. Without `X-Content-Type-Options: nosniff` and CSP, the browser may render the HTML and execute the embedded script (stored XSS). |
| **Impact** | Stored cross-site scripting via the Web UI if files are served with attacker-controlled content type. |
| **Recommendation** | Add a security headers middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'self'`, `Strict-Transport-Security: max-age=31536000; includeSubDomains`, and `Referrer-Policy: no-referrer`. |
| **Effort** | S (< 1 day) |

---

## Finding 9: Metadata/Header Injection via Stored Content-Disposition

| Field | Value |
|-------|-------|
| **Category** | Input Validation / Injection |
| **Severity** | **🟡 Medium** |
| **Title** | User-supplied `Content-Disposition` stored and returned verbatim — potential HTTP response header injection |
| **Location** | `internal/api/rest/handler.go:326-330` — `writeContentResponseHeaders()` sets user-controlled `Content-Disposition` as an HTTP response header |
| **Description** | The `Content-Disposition` and `Content-Encoding` headers from PUT requests are stored in metadata under `_aero_content_disposition` and `_aero_content_encoding`. On GET/HEAD, these are echoed back verbatim as HTTP response headers. A user who PUTs an object with a malicious `Content-Disposition` header (e.g., containing CRLF characters) could inject arbitrary HTTP response headers. While the Go `net/http` server protects against basic CRLF injection in header values, the value ends up in the ResponseWriter header set, and if it contains special characters, it could enable certain attacks like content type override. The S3 compat path has the same issue in `writeS3ObjectMeta()` at `internal/api/s3compat/handler.go:277`. |
| **Attack Scenario** | 1. Attacker PUTs an object with `Content-Disposition: attachment; filename="evil.html%0d%0aSet-Cookie: malicious=value"`. 2. Another user GETs the object. 3. If the CRLF bypass works (mitigated by Go's header sanitization), the user gets an injected `Set-Cookie` header. |
| **Impact** | While Go's `net/http` recent versions strip CRLF from header values, this pattern violates defense-in-depth. In older Go versions or if the S3 compat path uses a different HTTP library, this could enable header injection. |
| **Recommendation** | 1. Validate or sanitize `Content-Disposition` and `Content-Encoding` on write. 2. Use `http.Header.Set()` (already done, which is safer). 3. Strip CR/LF characters from metadata before returning as headers. |
| **Effort** | S (< 1 day) |

```go
func sanitizeHeaderValue(v string) string {
    v = strings.ReplaceAll(v, "\r", "")
    v = strings.ReplaceAll(v, "\n", "")
    return v
}

func writeContentResponseHeaders(w http.ResponseWriter, meta map[string]string) {
    if v, ok := meta["_aero_content_disposition"]; ok && v != "" {
        w.Header().Set("Content-Disposition", sanitizeHeaderValue(v))
    }
    // ...
}
```

---

## Finding 10: Idempotency-Key Without Body Hash by Default

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **🟡 Medium** |
| **Title** | Idempotency key deduplication does not hash request body by default |
| **Location** | `internal/api/rest/idempotency.go` — `IDEMPOTENCY_HASH_BODY` config flag controls body hashing |
| **Description** | The idempotency middleware caches responses keyed by `Idempotency-Key` header + request method + path. When `Idempotency-Hash-Body` is disabled (default), two different PUT requests with the same idempotency key are treated as identical — the second request gets the first response without actually writing the second payload. While this is intentional for retry semantics, it creates a risk: if a client reuses an idempotency key across different payloads (by accident or malice), the second payload is silently dropped. A client sending a "create entity with PII" request followed by "create entity without PII" with the same key would result in the PII version being stored; the second appears to succeed but is a no-op. |
| **Attack Scenario** | 1. Client A sends `PUT /v1/files/contract.docx` with `Idempotency-Key: abc-123` containing sensitive data. 2. Client B (or same client with a bug) sends a corrected version with `Idempotency-Key: abc-123` but different content. 3. The second request returns the first response without storing the corrected content. 4. The system believes it has the corrected data but actually has the original (potentially sensitive) version. |
| **Impact** | Silent data inconsistency. In worst case, sensitive data retained when it should have been overwritten. |
| **Recommendation** | Enable body hashing by default (`IDEMPOTENCY_HASH_BODY=true`), or at minimum log a prominent warning when it is disabled in production. |
| **Effort** | S (< 1 day) |

---

## Finding 11: Admin Endpoints Lack Rate Limiting

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **🟡 Medium** |
| **Title** | Admin API endpoints have no dedicated rate limiting |
| **Location** | `cmd/server/main.go` — admin routes under `/v1/admin/*` are limited only by the global rate limiter |
| **Description** | Admin endpoints (`/v1/admin/keys`, `/v1/admin/tenants`, `/v1/admin/jwt`, etc.) use the same global rate limiter as regular API endpoints. A misbehaving tenant that exhausts the global rate limit also blocks admin operations. Conversely, an attacker with a valid admin key can perform rapid admin operations (creating/deleting tenants, issuing keys) without dedicated rate limiting. Admin endpoints like `POST /v1/admin/jwt` (token issuance) and `POST /v1/admin/keys` (key creation) should have stricter, independent rate limits. |
| **Attack Scenario** | 1. Attacker with admin credentials issues 10,000 JWTs in rapid succession, exhausting storage and making token management impossible. 2. OR: High-volume tenant operations exhaust the global rate limit, preventing legitimate admin access during an incident response. |
| **Impact** | Rate-limit bypass for admin actions. Potential resource exhaustion. |
| **Recommendation** | 1. Add a separate `AdminRateLimiter` with stricter limits. 2. Apply it specifically to mutation-heavy admin routes. 3. Add per-IP rate limiting for admin endpoints. |
| **Effort** | M (1-2 days) |

---

## Finding 12: S3 Presigned URL with Unlimited Expiry via Configuration

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **🟡 Medium** |
| **Title** | No maximum presigned URL TTL enforcement; loopback and internal network bypasses |
| **Location** | `internal/api/rest/handler.go:225-228` — `Presign()` allows arbitrary `expires` value; `internal/storage/sign.go` — presigned URL verification |
| **Description** | The presigned URL endpoint accepts any `expires` value from the requesting client (defaulting to 300s but without an upper bound). A user with write scope can generate a presigned URL valid for 10 years. Additionally, the presigned URL verification in `sign.go` verifies the HMAC but doesn't restrict IP ranges. A presigned URL intended for a specific client can be used from any network if leaked. |
| **Attack Scenario** | 1. Internal user with write scope creates a presigned PUT URL good for 10 years. 2. User leaves the organization but the URL remains valid. 3. Attacker discovers the URL (e.g., from logs, browser history, network traffic) and uses it to write malicious objects. |
| **Impact** | Long-lived presigned URLs that survive employee departures. |
| **Recommendation** | 1. Enforce a maximum TTL (e.g., 7 days) on presigned URLs at the service layer regardless of what the client requests. 2. Document current behavior clearly. 3. Consider optional IP-binding for presigned URLs. |
| **Effort** | S (< 1 day) |

```go
// In service layer
func (s *FileService) PresignPut(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
    const maxPresignTTL = 7 * 24 * time.Hour
    if expiry > maxPresignTTL {
        expiry = maxPresignTTL
    }
    // ... rest of implementation
}
```

---

## Finding 13: Cross-Replica Key Cache Poisoning via GUID Collision

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **🟡 Medium** |
| **Title** | Key cache TTL creates window for cross-replica stale authentication |
| **Location** | `internal/auth/auth.go:68-72` — `WithKeyCache` comment acknowledges stale window |
| **Description** | The key cache TTL (default configurable via `AUTH_KEY_CACHE_TTL_SECONDS`) creates a window where a revoked API key is still accepted by other replicas. The cross-replica invalidation via Postgres LISTEN/NOTIFY attempts to mitigate this, but the transport starts only when `cfg.Events.TransportDSN != ""` — meaning if Postgres transport is not configured (which is the default with SQLite), cross-replica invalidation is entirely disabled. Any key revocation on one replica is invisible to other replicas until the cache TTL expires. Additionally, the key change listener accepts ANY event on the `aero_key_invalidate` channel without authentication — an attacker who can publish to the channel (e.g., via direct Postgres connection) can poison the cache. |
| **Attack Scenario** | 1. Attacker gains SQL access to the shared database. 2. Publishes a `NOTIFY aero_key_invalidate, 'fakehash'` event. 3. All replicas invalidate the cache entry for `fakehash`, causing a cache-miss amplification attack (every request re-queries the database). |
| **Impact** | Stale authentication decisions, cache poisoning, potential denial of service via cache-miss storms. |
| **Recommendation** | 1. Default the key cache to OFF when not in multi-replica mode. 2. In multi-replica mode, require Postgres transport and validate the event payload. 3. Add a nonce to invalidation events to prevent replay. |
| **Effort** | M (1-2 days) |

---

## Finding 14: Soft-Delete Bypass via Hard-Delete Query Parameter

| Field | Value |
|-------|-------|
| **Category** | Authorization / Data Protection |
| **Severity** | **🟠 High** |
| **Title** | Hard-delete via `?hard=1` parameter is accessible to any user with write scope |
| **Location** | `internal/api/rest/handler.go:194-195` — `h.svc.Delete()` with `hard` from query parameter |
| **Description** | Any authenticated user with write scope can permanently delete objects by appending `?hard=1` to the DELETE request. There is no separate scope or permission check for hard deletes versus soft deletes. This means a user who is authorized to "delete" (write scope) can bypass all soft-delete protections, retention policies, and versioning by always using hard delete. The service layer blocks hard deletes on retention-locked objects, but in the default configuration without object lock, all objects are permanently deletable. |
| **Attack Scenario** | 1. Organization uses soft-delete with a 30-day retention policy for compliance. 2. Rogue contractor with write scope deletes critical data with `?hard=1`. 3. Data is permanently unrecoverable — no retention period protection. 4. Compliance violation (GDPR right to deletion is abused as data destruction). |
| **Impact** | Permanent data loss circumventing retention policies. Compliance violations. |
| **Recommendation** | 1. Gate hard-delete behind admin scope only. 2. Or add a bucket-level configuration to disable hard delete. 3. Log all hard deletes with full audit trail. |
| **Effort** | S (< 1 day) |

```go
// In handler:
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
    hard := r.URL.Query().Get("hard") == "1"
    if hard {
        // Require admin scope for hard delete
        k, ok := auth.FromContext(r.Context())
        if !ok || !k.Has(auth.ScopeAdmin) {
            h.writeError(w, r, service.ErrForbidden)
            return
        }
    }
    // ...
}
```

---

## Finding 15: TenantID in Storage Key Creates Information Disclosure via Filename

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Information Disclosure |
| **Severity** | **🟢 Low** |
| **Title** | Tenant ID embedded in storage key path leaks tenant information |
| **Location** | `internal/service/file.go:125` — `storageKey()` builds `path.Join(tenant, bucket, key)` |
| **Description** | The storage key is constructed as `{tenant}/{bucket}/{key}`. For local storage, this means tenant IDs appear as directory names on the filesystem. Any process with filesystem access to the storage directory can enumerate tenant IDs. If the storage backend is S3, tenant IDs leak into S3 object key prefixes, potentially visible through S3 listing operations (if IAM misconfigured) or cost allocation reports. A tenant naming their tenant "acme-corp-confidential" would expose that string in every storage operation. |
| **Attack Scenario** | 1. Operating system user with read access to the `./var/objects` directory runs `ls -la`. 2. Sees directories named by tenant, revealing the full list of customers and their data volumes. 3. This information enables competitive intelligence gathering. |
| **Impact** | Low — tenant ID disclosure without data contents. Part of defense-in-depth. |
| **Recommendation** | 1. Use opaque tenant identifiers (UUIDs) instead of human-readable names in storage keys. 2. Map human-readable tenant names to UUIDs at the service layer. |
| **Effort** | L (> 3 days, involves schema migration) |

---

## STRIDE Summary

| Category | Risk | Key Findings |
|----------|------|-------------|
| **S**poofing | **High** | API keys in plaintext env (Finding 2), JWT symmetric key cannot rotate (Finding 7), MCP no auth (Finding 1) |
| **T**ampering | **Medium** | No body hash in idempotency by default (Finding 10), SSE-GCM protects data at rest but ETag verification is opt-in |
| **R**epudiation | **Low** | Audit logging present but webhook dead-letter conflated with success (Finding 6) |
| **I**nformation Disclosure | **High** | OpenAPI/docs unauthenticated (Finding 3), tenant IDs in storage paths (Finding 15), S3 policy resource bypass (Finding 5) |
| **D**enial of Service | **High** | Rate limiter tenant header spoofing (Finding 4), rate limiter map exhaustion (Finding 4), admin endpoints no dedicated limits (Finding 11) |
| **E**levation of Privilege | **High** | MCP no auth (Finding 1), hard-delete from write scope (Finding 14), S3 policy resource ignore (Finding 5) |

---

## Compliance OWASP Top 10 (2021) Mapping

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| A01: Broken Access Control | ❌ **Fail** | MCP no auth (Finding 1), hard-delete authorization (Finding 14) |
| A02: Cryptographic Failures | ⚠️ **Partial** | Good AES-256-GCM, but env secrets plaintext (Finding 2), JWT symmetric (Finding 7) |
| A03: Injection | ✅ **Pass** | Parameterized SQL throughout, key validation prevents path traversal |
| A04: Insecure Design | ⚠️ **Partial** | MCP auth gap is a design flaw (Finding 1) |
| A05: Security Misconfiguration | ❌ **Fail** | Default no-auth, open security headers missing (Finding 8) |
| A06: Vulnerable Components | ✅ **Pass** | Standard library, well-maintained deps |
| A07: Identification & Auth Failures | ❌ **Fail** | MCP bypass (Finding 1), JWT no revocation (Finding 7) |
| A08: Software & Data Integrity | ⚠️ **Partial** | Idempotency body hash optional (Finding 10) |
| A09: Security Logging & Monitoring | ⚠️ **Partial** | Good HTTP logging, but webhook dead-letter silences failures (Finding 6) |
| A10: SSRF | ✅ **Pass** | Webhook URL from config, no user-controlled outbound requests |

---

## Final Recommendations

### 🚨 Top 3 Critical Issues (Must Fix Before Production)

| # | Issue | Effort |
|---|-------|--------|
| **1** | **MCP /mcp endpoint has no authentication** — provides unauthenticated CRUD access to all data when auth is disabled (default). Apply auth middleware. | S |
| **2** | **API keys and SigV4 secrets in plaintext environment variables** — any env leak exposes all credentials. Hash env keys in memory, recommend vault-backed secrets for production. | M |
| **3** | **Rate limiter tenant-bucket spoofing** — attacker controls `X-Aero-Tenant` header, can exhaust other tenants' rate limits or trigger global lock contention. Use authenticated tenant identity. | M |

### ⚡ Top 3 Quick Wins (High Impact, Low Effort)

| # | Issue | Effort |
|---|-------|--------|
| **1** | Add security headers middleware (HSTS, CSP, nosniff, X-Frame-Options) — 5 lines of code, mitigates XSS in Web UI. | S |
| **2** | Enforce max presigned URL TTL (e.g., 7 days) at service layer — prevents permanent backdoor URLs. | S |
| **3** | Gate hard-delete (`?hard=1`) behind admin scope — prevents data destruction by write-scope users, preserving retention policies. | S |

### 🏗️ Security Debt Priority

| Priority | Area | Effort | Business Impact |
|----------|------|--------|----------------|
| P0 | MCP auth, env secrets, rate limiter auth | S-M | Direct compromise |
| P1 | Security headers, hard-delete gate, presigned TTL | S | Compliance, data loss |
| P2 | JWT revocation, S3 policy resource enforcement | M | Tenant isolation |
| P3 | Webhook DLQ, cross-replica cache hardening | M | Reliability, multi-replica |
| P4 | Tenant UUIDs in storage keys, admin rate limiting | L | Defense-in-depth |

---

The codebase has strong bones — proper SQL parameterization, well-implemented AEAD encryption, audit logging — but the **authentication gaps** (MCP endpoint, rate limiter tenant spoofing) and **secrets management** need immediate attention before production deployment. The default "no auth" configuration combined with the unauthenticated MCP endpoint is a critical risk: any network-adjacent attacker can compromise all data without credentials.
