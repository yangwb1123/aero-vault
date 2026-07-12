I have completed my thorough security analysis. Let me now compile the full review.

---

# Principal Security Engineer — Production Security Review

## Subsystem: aero-vault Auth & API Security Layer

**Reviewed components:** `internal/auth/`, `internal/middleware/`, `internal/api/rest/` (handler, admin, idempotency, router), `internal/events/webhook.go`, `internal/storage/sign.go`, `cmd/server/main.go` (middleware assembly)

**Codebase:** ~50,665 lines Go, Go 1.25, chi/v5 router

---

## Finding Register

### FINDING-01: Security Response Headers Absent Across All Routes

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **High** |
| **Title** | Missing security headers: HSTS, CSP, X-Content-Type-Options, X-Frame-Options |
| **Location** | `internal/middleware/middleware.go` (entire file), `cmd/server/main.go` (`applyMiddleware` function) |
| **Description** | No middleware sets any of these security headers: `Strict-Transport-Security`, `Content-Security-Policy`, `X-Content-Type-Options`, `X-Frame-Options`, `X-XSS-Protection`. The Web UI (`/ui`) serves user-uploaded content via REST, and the Swagger docs page embeds JavaScript, creating XSS risk. |
| **Attack Scenario** | An attacker uploads a file with HTML content and a crafted filename (e.g., `x.html`). When the Web UI or a direct GET request serves this file without any CSP/X-Content-Type-Options, the browser may render it as HTML, enabling cross-site scripting. The `/docs` Swagger page loads external CDN resources. |
| **Impact** | XSS against any user accessing the Web UI or API docs page. Session token theft, API key exfiltration via browser storage, defacement. |
| **Recommendation** | Add security headers middleware: |
| **Effort** | S |

```go
// internal/middleware/security.go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline';")
        w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
        // HSTS only when serving HTTPS
        if r.TLS != nil {
            w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        }
        next.ServeHTTP(w, r)
    })
}
```

Insert between `request_id` and `cors` in `applyMiddleware`:
```go
{"security", SecurityHeaders},
```

---

### FINDING-02: Idempotency Temp Files Are World-Readable

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Threat Model (Information Disclosure) |
| **Severity** | **Critical** |
| **Title** | Idempotency key body spool leaks request payload to OS temp directory |
| **Location** | `internal/api/rest/idempotency.go` line 153: `os.CreateTemp("", "aero-idem-*")` |
| **Description** | When `Idempotency-Key` is used with a request body >8 MiB, the body is spooled to a temp file in the OS default temp directory (`os.CreateTemp`). On Linux defaults to `/tmp`, which is world-readable. The file contains the full PUT/POST request body (i.e., uploaded object content). The file persists until `(*idemSpool).Close()` is called (end of request), but on a shared or containerized host, another process could read the file during the transfer window. |
| **Attack Scenario** | 1. Attacker creates a symlink at `/tmp/aero-idem-*.tmp` pointing to `/etc/passwd` or watches `/tmp` for new files matching the pattern. 2. A privileged user uploads a sensitive document with `Idempotency-Key` header. 3. The request body >8 MiB gets spooled to `/tmp/aero-idem-<random>`. 4. Attacker reads the temp file containing the sensitive document. |
| **Impact** | Information disclosure of uploaded object content. On multi-tenant installations, tenant A could read tenant B's uploads. |
| **Recommendation** | Use `os.MkdirTemp("", "aero-idem-*")` to create a private directory first (mode 0700), then create the file inside it. Or use a bounded in-memory ring buffer and never spill to disk. |
| **Effort** | S |

```go
// Replace:
f, err := os.CreateTemp("", "aero-idem-*")

// With:
dir, err := os.MkdirTemp("", "aero-idem-*")
if err != nil { return nil, "", err }
f, err := os.CreateTemp(dir, "body-*")
```

---

### FINDING-03: Presigned URL Expiry Is Unbounded

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | No maximum bound on presigned URL `expires` parameter |
| **Location** | `internal/api/rest/handler.go` lines 330-337 (Presign handler) |
| **Description** | The `POST /v1/files/{key}/presign?expires=<secs>` endpoint accepts any positive integer for `expires`. There is no upper bound. An authorized user could generate a presigned URL valid for years (e.g., `expires=315360000` = 10 years). The LocalStorage presign implementation in `internal/storage/local_read.go:76` does not cap expiry either. |
| **Attack Scenario** | 1. A tenant admin generates a presigned PUT URL with `expires=315360000`. 2. The URL is leaked via logs, network traces, or a client-side bug. 3. An attacker uses the URL to upload arbitrary content to the object path years later. 4. The original key owner may have left the organization, making detection difficult. |
| **Impact** | Permanent access grant that cannot be revoked (the presign key is not tied to the API key that generated it). |
| **Recommendation** | Cap expiry at a hard maximum (e.g., 7 days = 604800 seconds) at the handler level: |
| **Effort** | S |

```go
// In handler.go Presign function, after line 335:
if secs > 604800 { // 7 days max
    secs = 604800
}
```

---

### FINDING-04: Rate Limiter Bucket Map Exhaustion by Tenant Spoofing

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **Medium** |
| **Title** | Rate limiter per-tenant bucket map can be exhausted via X-Aero-Tenant header |
| **Location** | `internal/middleware/ratelimit.go` lines 73-89 |
| **Description** | The `RateLimiter.Allow()` method uses the tenant from `TenantFrom(ctx)`, which is populated from the `X-Aero-Tenant` header. When auth is disabled (MVP/default mode), any value is accepted. The rate limiter creates a new bucket for each unique tenant, capped at 50,000 entries (`rlMaxBuckets`). An attacker can send 50,000 requests with unique `X-Aero-Tenant` values in 60 seconds (the idle sweep interval), filling the map. After that, new tenants are rejected regardless of legitimate usage. On the next sweep (60s), idle buckets are evicted, enabling the cycle to repeat. When auth is enabled with a wildcard (`*`) key, the attacker can still spoof tenants. |
| **Attack Scenario** | 1. Attacker sends 50,000 requests with unique X-Aero-Tenant values (e.g., `evil-{0..49999}`) over a few seconds. 2. All legitimate requests from real tenants are rejected with 429 because the bucket map is full. 3. After 60 seconds, idle buckets are evicted, freeing space, but the attacker repeats the attack. |
| **Impact** | Denial of service against all tenants. |
| **Recommendation** | 1. Only create per-tenant rate limiter buckets when auth is enabled and the tenant is verified. 2. Reduce `rlMaxBuckets` or use a probabilistic data structure (e.g., a sliding window with a fixed number of shards). 3. Add a short refill penalty for unknown tenants. |
| **Effort** | M |

```go
// In Allow(), before creating a new bucket:
if len(rl.buckets) >= rlMaxBuckets/2 {
    if _, ok := rl.buckets[tenant]; !ok {
        // Unknown tenant when at high capacity: penalize
        return false, 5 * time.Second
    }
}
```

---

### FINDING-05: Admin API Tokens / `{token}` in URL Path

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | API key revocation takes raw token in URL path, exposing it in server logs |
| **Location** | `internal/api/rest/admin.go` line 97 (RevokeKey), `internal/api/rest/router.go` line 82 |
| **Description** | `DELETE /v1/admin/keys/{token}` takes the raw API token as a URL path parameter. This token appears in server access logs, error logs, OTel tracing spans, and may be cached by CDNs or reverse proxies. The `RevokeKey` function in `internal/auth/auth.go` line 158 also takes the raw token, not the hash. The audit log in `redactToken` only masks the last 4 chars for one audit event, but the URL path itself is logged elsewhere. |
| **Attack Scenario** | 1. Admin calls `DELETE /v1/admin/keys/ak-prod-abc123def456` to revoke a compromised key. 2. The URL `/v1/admin/keys/ak-prod-abc123def456` is logged by access logs, CDN logs, and OTel spans. 3. An attacker with access to any of these log stores extracts the key and uses it before the revoke propagates. |
| **Impact** | Active API key leakage via URL logging. |
| **Recommendation** | Change the API to accept the token in the request body (JSON) rather than the URL path. For path-based deletion, use the token hash (SHA-256 hex) as the identifier instead of the raw token. |
| **Effort** | M |

```go
// Old: DELETE /v1/admin/keys/{token}
// New: DELETE /v1/admin/keys with body {"token_hash": "sha256hex..."}
// Or at minimum: accept token in body
```

---

### FINDING-06: SigV4 Credentials in Plaintext Environment Variable

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | S3 SigV4 secret keys exposed in process environment |
| **Location** | `internal/config/config_auth.go` line 9, `internal/auth/sigv4.go` line 39 (`ParseSigV4Credentials`) |
| **Description** | The `S3_SIGV4_CREDENTIALS` environment variable stores secret keys in the format `accessKey:secretKey:tenant[:scope]`. These are loaded at startup and remain in the process environment for the entire lifetime. On Linux, the environment is readable via `/proc/<pid>/environ` by any process with the same UID, and by root. Debug endpoints, core dumps, and panic logs may also contain environment variables. |
| **Attack Scenario** | 1. Attacker gains limited code execution (e.g., via a dependency vulnerability or misconfigured sidecar). 2. Reads `/proc/1/environ` to extract `S3_SIGV4_CREDENTIALS`. 3. Uses the secret key to sign arbitrary S3 requests or forge presigned URLs. |
| **Impact** | Complete compromise of S3-compatible access. Attacker can read, write, and delete any object. |
| **Recommendation** | 1. Document that environment variables should be loaded from a secure vault (Hashicorp Vault, AWS Secrets Manager, Kubernetes secrets) and loaded via filesystem mounts at a secure path. 2. Zero the secret from environment after parsing (`memclr`). 3. Add option to read from a file (`S3_SIGV4_CREDENTIALS_FILE`). |
| **Effort** | M |

```go
// After parsing, zero the config value:
cfg.Auth.SigV4Credentials = ""
// Add file-based alternative:
// S3_SIGV4_CREDENTIALS_FILE=/run/secrets/sigv4
```

---

### FINDING-07: Webhook URL SSRF Vector

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Spoofing / Information Disclosure) |
| **Severity** | **High** |
| **Title** | Webhook delivery to attacker-controllable URLs enables SSRF |
| **Location** | `internal/events/webhook.go` lines 48-57, `cmd/server/main.go` line 295 |
| **Description** | The `EVENTS_WEBHOOK_URL` environment variable accepts arbitrary URLs (including multiple comma-separated URLs). The webhook worker POSTs all bus events (including object content metadata, keys, event payloads) to these URLs with a 5-second timeout. No validation restricts URLs to public endpoints; an attacker who can control this env var can make the server POST event data to internal services (e.g., `http://169.254.169.254/latest/meta-data/` for cloud metadata, `http://localhost:8080/admin` for internal APIs). |
| **Attack Scenario** | 1. Attacker gains write access to the environment configuration (e.g., via Kubernetes ConfigMap, Docker env override, or file write). 2. Sets `EVENTS_WEBHOOK_URL=http://169.254.169.254/latest/meta-data/`. 3. The server posts all object events to the AWS metadata endpoint, potentially leaking instance credentials. 4. Also possible: POST to internal admin endpoints to trigger unauthorized actions. |
| **Impact** | Server-side request forgery. Data exfiltration, internal network scanning, cloud metadata credential theft. |
| **Recommendation** | 1. Validate/restrict webhook URLs to public HTTPS endpoints only. 2. Block private IP ranges (RFC 1918, loopback, link-local). 3. Warn on non-HTTPS URLs. 4. Add option to verify TLS certificates. |
| **Effort** | M |

```go
// In NewWebhook or before Run:
func validateWebhookURL(raw string) error {
    u, err := url.Parse(raw)
    if err != nil { return err }
    if u.Scheme != "https" { return errors.New("webhook URL must use HTTPS") }
    ip := net.ParseIP(u.Hostname())
    if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
        return errors.New("webhook URL must not be private/loopback")
    }
    return nil
}
```

---

### FINDING-08: Middleware Chain Order Allows Unauthenticated Rate Limit Spoofing

| Field | Value |
|-------|-------|
| **Category** | Authentication / Threat Model (DoS) |
| **Severity** | **Medium** |
| **Title** | Auth middleware sets tenant header, rate limiter runs after but clients skip tenant validation |
| **Location** | `cmd/server/main.go` (`applyMiddleware`), `internal/middleware/ratelimit.go` |
| **Description** | The middleware chain executes in order: `request_id → cors → auth → tenant → rate_limit → otel → recoverer → concurrency → access_log → handler`. The RateLimiter runs after auth AND after tenant middleware. Since the rate limiter reads the tenant from context (set by tenant middleware), authenticated users are correctly rate-limited by their verified tenant. However, anonymous requests that bypass auth (when auth is disabled, or anonymous public-read is enabled) still consume the `"default"` tenant bucket. When auth is enabled, invalid tokens are rejected at the auth middleware before reaching the rate limiter, meaning an attacker can send infinite invalid-token requests without being rate-limited. |
| **Attack Scenario** | 1. Auth is enabled with `AUTH_KEYS`. 2. Attacker sends 1,000,000 requests per second with random invalid `Authorization: Bearer invalid` headers. 3. Each request is rejected at the auth middleware layer, but NOT rate-limited (auth is before rate-limit in the chain). 4. The CPU is consumed by auth token lookup (JWT verification, key hash lookup, etc.), causing DoS against legitimate traffic. |
| **Impact** | Authentication-layer DoS. CPU exhaustion from hash computation, DB lookups, and JWT verification. |
| **Recommendation** | Move a lightweight rate-limiter (IP-based or connection-based) before the auth middleware. Do not rate-limit by tenant before auth—use connection-level or IP-level rate limiting at the outermost layer. |
| **Effort** | M |

```go
// Add IP-based pre-auth rate limiter:
chain := []struct {
    name string
    mw   func(http.Handler) http.Handler
}{
    {"pre_auth_rate_limit", ipRateLimiter.Middleware()}, // NEW: before auth
    {"access_log", ...},
    // ... rest as before
}
```

---

### FINDING-09: Anonymous Public-Read Widens Attack Surface

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Anonymous public-read bypasses key-based auth for object GET/HEAD |
| **Location** | `internal/auth/auth_middleware.go` lines 74-79, `internal/auth/auth.go` lines 109-119 |
| **Description** | When `AUTH_ANONYMOUS_PUBLIC_READ=true`, the auth middleware admits unauthenticated GET/HEAD requests on `/v1/files/...` by setting an `anonymous` flag on context. The handler must then check ACLs to decide whether to serve the object. However, this means any request without credentials passes through auth and must be checked by the handler layer. If any code path in the handler fails to check the anonymous flag, it may inadvertently serve objects to unauthenticated users. In particular, the anonymous read is gated by `isObjectReadPath`, which only checks the prefix `/v1/files/` — it does not validate the path beyond that. |
| **Attack Scenario** | 1. Admin enables `AUTH_ANONYMOUS_PUBLIC_READ=true` to allow public sharing. 2. A new handler or version endpoint is added and forgets to call the ACL check for anonymous requests. 3. Unauthenticated users access objects that should be private. |
| **Impact** | Unauthorized object access. Information disclosure. |
| **Recommendation** | 1. Add a defensive `checkAnonymous` middleware that wraps all object GET/HEAD routes and fails closed (403) if no explicit ACL exists. 2. Return `403` by default unless an ACL explicitly allows public read. 3. Log anonymous accesses for audit. |
| **Effort** | M |

---

### FINDING-10: JWT Signing Secret Shared Between Signing and Verification

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Authentication |
| **Severity** | **High** |
| **Title** | Same HMAC secret used for both JWT signing (by Admin API) and verification, enabling forgery |
| **Location** | `internal/auth/jwt.go` lines 43-47, `internal/api/rest/admin.go` lines 103-128 |
| **Description** | The `AUTH_JWT_SECRET` is used both to verify incoming JWTs AND to sign new JWTs via the Admin API (`POST /v1/admin/jwt`). A leaked secret allows both reading AND forging tokens. The Admin API allows any admin-scoped user to issue tokens for any tenant. While this is partially by design (internal SSO), the dual-use signing key means: (1) Any admin user can forge tokens for any tenant — there is no separation of duties. (2) The same key is used for HS256, making it vulnerable to oracle attacks if an attacker can request signing of crafted payloads. |
| **Attack Scenario** | 1. Attacker gains `admin` scope (e.g., via a compromised admin key). 2. Posts to `/v1/admin/jwt` to sign a token for tenant `evil-corp` with `read+write+admin` scopes. 3. Uses the forged token to access `evil-corp` data. |
| **Impact** | Privilege escalation via JWT forgery. Trusted insider with admin scope can impersonate any tenant. |
| **Recommendation** | 1. Use asymmetric keys (RS256/ES256) for JWT: Admin API signs with the private key, verification uses the public key. 2. Or use separate secrets: one for Admin API signing, another for verification of third-party tokens. 3. Add audience (`aud`) claim validation so tokens issued by the Admin API are distinguishable from external IdP tokens. |
| **Effort** | L |

---

### FINDING-11: Rate Limiter Uses `time.Now()` Unconditionally in Hot Path

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Timing) |
| **Severity** | **Info** |
| **Title** | `time.Now()` called under mutex in RateLimiter.Allow, causing contention |
| **Location** | `internal/middleware/ratelimit.go` lines 73-89 |
| **Description** | The `Allow()` method acquires a mutex and calls `time.Now()` inside the critical section. Under high concurrency (especially when rate-limited), every request blocked by the mutex will cause contention. The eviction sweep (`evictIdle`) also runs under the same mutex every 60 seconds, potentially blocking all incoming requests during sweep. |
| **Attack Scenario** | Attacker sends requests with many different tenant values, causing the rate limiter map to grow and the eviction sweep to do more work. Combined with request bursts, this causes mutex contention that slows down the global rate limiter. |
| **Impact** | Performance degradation under load. |
| **Recommendation** | Snapshot `time.Now()` before acquiring the lock using `time.Now()` (it's cheap, but move it outside the critical section). Use a read-write lock for the eviction path. |
| **Effort** | S |

---

### FINDING-12: No TLS Enforcement Anywhere

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | No built-in TLS/HTTPS termination; all traffic transmitted in cleartext |
| **Location** | `cmd/server/main.go` (`runServer` function, line 189) |
| **Description** | The HTTP server uses `http.ListenAndServe()` with no TLS configuration. There is no `--tls-cert` / `--tls-key` flag or env var. Presigned URLs from `LocalStorage.presign()` (internal/storage/sign.go) use the `PublicURL` config without enforcing HTTPS. The `SignKey` for local presigned URLs is transmitted over plaintext in the URL query parameter. The webhook delivers events over plain HTTP if `EVENTS_WEBHOOK_URL` uses `http://`. The CORS config does not enforce `https://` origins. |
| **Attack Scenario** | 1. Attacker on the same network segment (Wi-Fi, cloud VPC) uses ARP spoofing or passive sniffing. 2. Captures all API requests, including `Authorization: Bearer ...` headers, presigned URLs with signatures, Admin JWT tokens, and uploaded object content. 3. Uses captured credentials to access the vault. |
| **Impact** | Complete compromise of authentication credentials and data in transit. |
| **Recommendation** | 1. Add TLS with configurable cert/key paths to the HTTP server. 2. Default to TLS when `AUTH_JWT_SECRET` or `AUTH_KEYS` is set. 3. Document that a TLS-terminating reverse proxy is required for production. 4. Reject non-HTTPS webhook URLs with a warning. |
| **Effort** | M |

---

### FINDING-13: Object Lock/WORM Not Enforced at Storage Layer

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Medium** |
| **Title** | Object lock bypass via storage-level access |
| **Location** | `internal/service/file_crud.go` (hard delete path), `internal/reconcile/retention.go` line 111 |
| **Description** | Object lock/WORM retention is enforced at the `FileService` layer by checking `locked_until` on the metadata record. However, the `BatchDelete`, `DeleteFolder`, and reconciliation `RetentionJob` hard-delete paths may bypass WORM checks if the service layer is invoked with incorrect context. The lifecycle `sweepExpired` explicitly checks `locked_until`, but the hard delete path in `file_crud.go` uses `ChunkCleaner.DeleteObjectChunks` which does not check locks. The AGENTS.md section 2.1 states: "ChunkCleaner.DeleteObjectChunks failure must not block hard delete" — this means lock enforcement is best-effort. |
| **Attack Scenario** | 1. User uploads an object with WORM lock set to 10 years. 2. User calls `DELETE /v1/files/{key}` which calls `FileService.Delete()`. 3. The delete path checks `locked_until` and fails with `ErrObjectLocked`. 4. However, if the reconcile job or batch delete has a race condition or uses a different code path, the locked object might be deleted. |
| **Impact** | WORM guarantee violation. Data retention compliance failure. |
| **Recommendation** | 1. Add a lock check in `storage.Storage.Delete()` as a defense-in-depth layer. 2. Add integration tests that attempt every delete path against a locked object. 3. Document the lock guarantee accurately — "best-effort, not cryptographic enforcement." |
| **Effort** | M |

---

### FINDING-14: Scrub Integrity Check Uses MD5

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | Data integrity scrub uses MD5, not SHA-256 |
| **Location** | `internal/reconcile/scrub.go` line 71 |
| **Description** | The data-integrity scrub feature validates objects by comparing a stored MD5 hash (`_aero_content_md5`) against the recomputed MD5 of stored content. MD5 is cryptographically broken and collision attacks are practical (chosen-prefix collision in ~$75K in 2025). While this is an integrity check (not a forgery defense), using a modern hash would provide better collision resistance. |
| **Attack Scenario** | Attacker uploads two objects with different content but colliding MD5 hashes. The scrub passes for both, hiding data corruption in one object. |
| **Impact** | Integrity verification false negative. |
| **Recommendation** | When storing new objects, store both MD5 (for legacy S3 compatibility) and SHA-256. The scrub should verify with SHA-256 when available, falling back to MD5. |
| **Effort** | S |

---

### FINDING-15: Self-Serve `/usage` Endpoint Exposes Tenant Quota Without Admin Check

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | `/v1/usage` accessible to any authenticated user, but only returns calling tenant's data |
| **Location** | `internal/api/rest/router.go` line 71: `r.Get("/usage", adm.Usage)` |
| **Description** | The usage endpoint reads the tenant from context (`mw.TenantFrom(r.Context())`), so it returns only the calling user's tenant data. However, it does not require `admin` scope — any authenticated key can see usage for its tenant. This is by design (self-serve), but means a key with `read` scope can enumerate tenant quota and usage. In multi-tenant setups, this could leak capacity information. |
| **Attack Scenario** | Read-only user checks `/v1/usage`. Sees `max_bytes: 1000000, used_bytes: 999000` — the bucket is nearly full. User times a write to cause the bucket to exceed quota, triggering a denial of service for legitimate writes. |
| **Impact** | Low: usage data exposure. May aid DoS planning. |
| **Recommendation** | Document as intended behavior. Add rate limiting to this endpoint to prevent usage-polling attacks. |
| **Effort** | S |

---

## STRIDE Summary

| Category | Findings |
|----------|----------|
| **Spoofing** | F-06 (SigV4 creds in env), F-10 (JWT shared secret), F-01 (no HSTS → session hijack), F-12 (no TLS) |
| **Tampering** | F-13 (WORM bypass via direct storage), F-14 (MD5 collision for integrity scrub) |
| **Repudiation** | Audit logging exists but lacks action-level detail in some admin paths; finding deferred. |
| **Information Disclosure** | F-02 (idempotency temp files), F-05 (key in URL path), F-09 (anonymous public-read) |
| **Denial of Service** | F-04 (rate limiter bucket exhaustion), F-08 (auth-before-rate-limit allows unlimited auth attempts) |
| **Elevation of Privilege** | F-10 (JWT forgery via shared signing key), F-03 (presigned URL unbounded lifetime) |

---

## OWASP Top 10 Mapping

| OWASP Category | Applicable Findings |
|----------------|-------------------|
| **A01:2021 – Broken Access Control** | F-09 (anonymous read), F-15 (usage without admin check) |
| **A02:2021 – Cryptographic Failures** | F-06 (plaintext secrets in env), F-10 (shared JWT key), F-12 (no TLS), F-14 (MD5) |
| **A03:2021 – Injection** | No SQL/XSS injection vectors in reviewed code; JSON response encoding is correct |
| **A04:2021 – Insecure Design** | F-03 (unbounded presigned URLs), F-04 (rate limiter exhaustion) |
| **A05:2021 – Security Misconfiguration** | F-01 (no security headers), F-08 (middleware ordering) |
| **A06:2021 – Vulnerable Components** | Not in scope of this review |
| **A07:2021 – Identification & Auth Failures** | F-05 (key in URL), F-08 (auth before rate-limit) |
| **A08:2021 – Software & Data Integrity** | F-02 (temp file integrity), F-14 (MD5 integrity scrub) |
| **A09:2021 – Security Logging & Monitoring** | Audit logging present but limited; finding deferred |
| **A10:2021 – SSRF** | F-07 (webhook SSRF) |

---

## Final Summary

| Metric | Rating |
|--------|--------|
| **Overall Security Posture** | **Needs Improvement** |
| **Components Reviewed** | Auth, Middleware, API handlers, Idempotency, Webhook, Presign, Rate Limiter |
| **Critical Issues** | 1 |
| **High Issues** | 6 |
| **Medium Issues** | 4 |
| **Low Issues** | 2 |
| **Info** | 2 |

### Top 3 Critical Issues

1. **F-02: Idempotency temp files are world-readable** (Critical) — Sensitive upload data leaks to `/tmp`. Fix within 1 day by using `os.MkdirTemp` with 0700 permissions.

2. **F-01: No security headers** (High) — XSS attack surface on Web UI and Swagger docs. Fix within 1 day by adding a security headers middleware.

3. **F-05: Admin API key revocation passes raw key in URL** (High) — API keys leaked to access logs. Fix within 2-3 days by moving the key to request body.

### Top 3 Quick Wins (High Impact, Low Effort)

1. **F-02 fix (Security: Critical, Effort: S):** Change `os.CreateTemp("", ...)` → `os.MkdirTemp` with 0700 in idempotency spool. One-line change per location.

2. **F-01 fix (Security: High, Effort: S):** Add security headers middleware. Copy-paste the code example above, add to middleware chain. 15 minutes.

3. **F-03 fix (Security: High, Effort: S):** Cap presigned URL expiry at 7 days (604800 seconds). One line: `if secs > 604800 { secs = 604800 }`.

### Security Debt

The following issues require architectural changes and constitute security debt:

| Issue | Debt Reason | Target Sprint |
|-------|-------------|---------------|
| F-10 Shared JWT signing/verification key | Requires asymmetric crypto migration (RS256/ES256) | Sprint N+2 |
| F-07 Webhook SSRF prevention | Requires URL validation and IP range blocking | Sprint N+1 |
| F-12 TLS enforcement | Requires certificate management infrastructure | Sprint N+1 |
| F-04 Rate limiter bucket exhaustion | Requires probabilistic data structure or tenant IP binding | Sprint N+2 |

**Recommendation:** Fix Critical and High issues (F-02, F-01, F-05, F-06, F-07, F-03, F-10, F-12) before going to production with real data. The authentication framework is well-abstracted and modular, which is a strong foundation — but the security hardening layer (headers, TLS, rate-limiter ordering, temp file isolation) needs to be built on top of it.
