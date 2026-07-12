I now have a comprehensive understanding of the codebase. Let me compile the full security review.

---

# Security Review: aero-vault

## Overall Assessment

I reviewed 243+ Go source files across 24 packages. The codebase demonstrates a strong security-conscious design with proper parameterized queries, AES-256-GCM envelope encryption, HMAC-signed webhooks, multi-factor auth (JWT + API keys + SigV4), and well-defined authorization scopes. However, several significant issues need attention before production deployment.

---

## Detailed Findings

---

### 1. Pre-signed URL signing key stored in plaintext with weak default

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **High** |
| Title | Pre-signed URL HMAC key in plaintext env var with guessable default |
| Location | `.env.example:47`, `internal/storage/sign.go`, `internal/storage/local.go` |
| Description | The `STORAGE_LOCAL_SIGN_KEY` is stored in plaintext in environment configuration and defaults to `change-me`. This key is used for HMAC-SHA256 signing of pre-signed GET/PUT URLs. The `signLocal` function in `sign.go` builds the signature, but there is no key rotation mechanism, no key derivation per-tenant, and no expiry beyond the URL's `expires` parameter. |
| Attack Scenario | An attacker who obtains the server's environment file (e.g., via directory traversal, misconfigured backup, container escape) can forge unlimited pre-signed URLs for any object. The default `change-me` value means any deployment that doesn't change it is trivially exploitable. |
| Impact | Unauthorized read/write access to any object in storage via forged presigned URLs. |
| Recommendation | 1. Document that `STORAGE_LOCAL_SIGN_KEY` must be a high-entropy secret. 2. Add a warning log at startup when the key equals `change-me`. 3. Consider per-tenant derived signing keys. 4. Add a minimum key-length validation. 5. Add key rotation support. |
| Effort | S |

---

### 2. JWT uses HS256 symmetric key — no asymmetric option

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **High** |
| Title | JWT signing uses HS256 (symmetric HMAC) with secret in env var |
| Location | `internal/auth/jwt.go`, `.env.example:AUTH_JWT_SECRET`, `cmd/server/main.go:configureAuthSecrets` |
| Description | The JWT implementation uses HS256 (HMAC-SHA256), a symmetric algorithm. The same secret both signs and verifies tokens. The code's own comment acknowledges this: "To roll forward to RS256/JWKS, swap NewJWTVerifier for an asymmetric implementation." The `AUTH_JWT_SECRET` env var is read in plaintext. There's no JWKS endpoint support, no public-key distribution mechanism, and no key rotation. |
| Attack Scenario | Anyone with access to the `AUTH_JWT_SECRET` env var (developer laptops, CI/CD, config management, compromised container) can forge JWT tokens with any tenant, any scope, arbitrary expiry. Since HS256 is symmetric, the verifier and signer share the same secret — the holder of the secret is "god." |
| Impact | Complete authentication bypass. Attacker can mint tokens for `tenant=*` (admin) with all scopes, gaining full control over all tenants. |
| Recommendation | 1. Add RS256/ES256 support with a JWKS endpoint for key distribution. 2. At minimum, support a configuration option for asymmetric algorithms. 3. Document that HS256 deployments must treat `AUTH_JWT_SECRET` as the highest-value secret in the system. 4. Consider adding the `aud` (audience) claim validation. |
| Effort | M |

---

### 3. No brute-force protection on authentication endpoints

| Field | Value |
|-------|-------|
| Category | Authentication |
| Severity | **High** |
| Title | No rate limiting or lockout on failed authentication attempts |
| Location | `internal/auth/auth_middleware.go`, `internal/middleware/ratelimit.go` |
| Description | The auth middleware returns 401/403 on invalid credentials but does not track failed attempts per IP, per tenant, or per key. The global rate limiter applies uniformly to all requests after authentication, not specifically to auth failures. An attacker can brute-force API keys or JWT tokens with no throttling. |
| Attack Scenario | An attacker sends `Bearer X` with random keys in rapid succession. Since the in-memory key map is small (hundreds), a brute-force attack across the network can enumerate valid keys. For JWT, an attacker can try different `exp` values or signature manipulations. |
| Impact | Credential brute-force leading to account compromise. |
| Recommendation | 1. Add IP-based rate limiting for 401/403 responses (e.g., max 10 failures/minute/IP). 2. Add exponential backoff on repeated failed attempts for the same key hash. 3. Log all auth failures with structured fields (IP, key hash prefix, path). 4. Consider adding a `fail2ban`-compatible log format. |
| Effort | M |

---

### 4. Rate limiter vulnerable to tenant-header-based bucket exhaustion

| Field | Value |
|-------|-------|
| Category | Denial of Service |
| Severity | **Medium** |
| Title | Rate limiter keyed by client-controlled X-Aero-Tenant header — bucket exhaustion DoS |
| Location | `internal/middleware/ratelimit.go:55-70` |
| Description | The rate limiter uses `X-Aero-Tenant` (a client-controlled header) as the bucket key. The `rlMaxBuckets` cap is 50,000, and the eviction mechanism runs every 60 seconds. An attacker can send requests with random `X-Aero-Tenant` values to exhaust the bucket map, causing the rate limiter to reject legitimate requests. Even though there's an eviction guard, the eviction itself consumes the mutex and blocks all rate-limit checks during eviction. |
| Attack Scenario | 1. Attacker sends 50,001+ requests with unique `X-Aero-Tenant` values. 2. The rate limiter map fills to capacity. 3. The eviction check runs on each new tenant and may still reject when the eviction can't free space. 4. Legitimate requests from real tenants get 429 responses. |
| Impact | Service-wide DoS — all tenants rate-limited even if well-behaved. |
| Recommendation | 1. Add a secondary rate limit keyed by IP/network prefix for anonymous requests. 2. Use a Bloom filter or LRU cache instead of a simple map with periodic eviction. 3. Implement tenant allowlisting or capacity reservation for known tenants. 4. Document that `X-Aero-Tenant` is untrusted and can cause tenant-bucket collisions. |
| Effort | S |

---

### 5. User-controlled metadata written directly to response headers

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **Medium** |
| Title | User metadata keys reflected directly in HTTP response headers without sanitization |
| Location | `internal/api/rest/handler.go:293-299` (`writeMetadataHeaders`), `internal/service/file.go:80-86` |
| Description | The `writeMetadataHeaders` function iterates over user-supplied metadata and sets each key as `X-Meta-<key>` response headers. While Go's `http.Header.Set()` sanitizes CRLF sequences, other HTTP header injection vectors (non-ASCII characters, tab characters) may not be fully sanitized. The metadata keys pass through `extractMetadataHeaders` which lowercases only the `x-amz-meta-` prefix but preserves arbitrary characters in the key name. |
| Attack Scenario | An attacker PUTs an object with metadata key `foo\r\nSet-Cookie: malicious=value`. If a proxy or browser interprets the response before Go's sanitization, this could lead to HTTP response splitting or cookie injection. Though Go's header implementation prevents newlines, edge cases with exotic proxies or custom header processing could be exploited. |
| Impact | Potential HTTP response header injection, cache poisoning, or cross-site scripting in edge cases. |
| Recommendation | 1. Validate metadata keys against a strict regex: `^[a-zA-Z0-9._\-]+$`. 2. Reject metadata keys with control characters, non-ASCII, or suspicious patterns. 3. Expose the validation as a shared function used by both PUT and batch-tag endpoints. |
| Effort | S |

---

### 6. Idempotency key system stores full response body in database

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Medium** |
| Title | Idempotency replay stores full response body in DB, potentially leaking sensitive data |
| Location | `internal/repository/idempotency.go:45-58`, `internal/api/rest/idempotency.go` |
| Description | The `CompleteIdempotencyKey` function stores `response_body` (raw bytes) and `response_headers` (JSON) in the `idempotency_keys` table. For write operations that create/update objects, the response body contains the full object metadata including ETag, key, bucket, version_id. The stored data persists until TTL-based GC deletes it. No encryption-at-rest for the table is mentioned. |
| Attack Scenario | An attacker who gains read access to the database (e.g., via SQL injection in another part of the app, or a compromised replica) can read response bodies from idempotency records, potentially learning object keys, version IDs, and metadata for all objects written in the last TTL hours. |
| Impact | Information disclosure of object metadata. For large responses, memory pressure on the database. |
| Recommendation | 1. Encrypt the `response_body` column at the application layer using the SSE master key. 2. Or, store only a hash of the response and replay it from storage on cache hit. 3. Reduce default TTL from hours to minutes. 4. Add a note in the security docs about the idempotency table containing response data. |
| Effort | M |

---

### 7. SigV4 credentials parsed from plaintext env var — no secret manager support

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Medium** |
| Title | S3 SigV4 credentials in plaintext env var with no rotation mechanism |
| Location | `internal/auth/sigv4.go:42-65`, `.env.example:S3_SIGV4_CREDENTIALS` |
| Description | The `S3_SIGV4_CREDENTIALS` env var contains `accessKey:secretKey:tenant:scope` in a single comma-separated string. The secret key is used to verify SigV4 signatures on incoming S3 requests. The entire credential set is loaded once at startup and never refreshed. No integration with AWS Secrets Manager, HashiCorp Vault, or any secrets rotation mechanism. |
| Attack Scenario | If an attacker obtains the environment file, they have the plaintext secret key for S3 SigV4 authentication, allowing them to sign arbitrary S3 requests as that credential. Since SigV4 verification is done locally (not against AWS STS), there's no upstream revocation. |
| Impact | Forgery of SigV4-signed S3 requests — full read/write access as the compromised credential's tenant. |
| Recommendation | 1. Support loading credentials from a secrets manager or encrypted file. 2. Add a log warning if credentials are set via env var (as opposed to a secrets manager). 3. Document rotation procedure. 4. Consider adding credential expiry checks. |
| Effort | S |

---

### 8. CORS allows wildcard origins with permissive defaults

| Field | Value |
|-------|-------|
| Category | Compliance |
| Severity | **Medium** |
| Title | CORS allows wildcard origin matching with liberal default methods/headers |
| Location | `internal/middleware/cors.go:40-50` |
| Description | When `CORS_ALLOWED_ORIGINS` is configured with `*`, the `matchOrigin` function returns `true` for any origin. The default `AllowedMethods` includes all HTTP methods. The default `AllowedHeaders` includes `Authorization`, `Idempotency-Key`, `X-Aero-Tenant`, `X-Api-Key`, `X-Request-ID`, `Range`. The `Access-Control-Allow-Credentials` is false by default (good), but the wildcard origin combined with broad headers opens the door for CSRF-style attacks on browsers, especially since the UI is served from the same origin as the API. |
| Attack Scenario | A malicious site (malicious.com) opens an `XMLHttpRequest` to the aero-vault API. If the user is authenticated via a browser-stored credential (e.g., session cookie, Basic auth), the CORS wildcard allows reading the response. While there's no cookie-based auth currently, future session middleware would create this risk. |
| Impact | Cross-origin data exfiltration in credential-less auth scenarios. Currently limited because auth tokens must be explicitly provided via `Authorization` header (not auto-sent by browsers). |
| Recommendation | 1. Document that CORS with wildcard origin + credential-based auth is a vulnerability. 2. Consider setting `Vary: Origin` more broadly. 3. Add a starter recommendation for explicit origin allowlists in production. 4. Do NOT add `Access-Control-Allow-Credentials: true` unless absolutely necessary. |
| Effort | S |

---

### 9. Webhook delivers full event payload to external URLs with no content-type restrictions

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Medium** |
| Title | Webhook delivers sensitive object metadata to external URLs with no transport encryption validation |
| Location | `internal/events/webhook.go:95-120` |
| Description | The webhook system POSTs full event payloads (tenant ID, bucket, key, object ID, request ID, payload) to configured external URLs. While HMAC signing is available via `EVENTS_WEBHOOK_SECRET`, there is no validation that the target URL uses HTTPS. The error messages include the target URL in logs (line 137: "webhook delivery failed" includes `url`). Failed deliveries are stored in the webhook_failures table, which can accumulate indefinitely (the 10-attempt dead letter is a fix but documented as conflating "permanently dead" with "succeeded"). |
| Attack Scenario | An operator configures an HTTP (not HTTPS) webhook URL. An attacker on the network path sees the plaintext event payload, learning object keys, tenant IDs, and operational metadata for every object event in the system. |
| Impact | Information disclosure of object lifecycle events. Potential SSRF if unvalidated URL targets internal services. |
| Recommendation | 1. Reject non-HTTPS webhook URLs at startup with a log warning (or enforce via a config flag). 2. Add HMAC signing by default (require explicit opt-out). 3. Add a separate dead-letter queue state instead of reusing the "succeeded" flag. 4. Consider rate-limiting outbound webhook deliveries per target URL. |
| Effort | S |

---

### 10. Admin JWT issuance endpoint allows arbitrary scope assignment

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **Medium** |
| Title | Admin API can issue JWTs with arbitrary scopes and tenant without additional verification |
| Location | `internal/api/rest/admin.go:155-185` |
| Description | The `POST /v1/admin/jwt` endpoint allows any admin-scoped key to issue JWTs for any tenant with any scope. There is no separate audit escalation requirement (e.g., MFA, second approver), no rate limiting specific to this endpoint, and no logging of the JWT itself for forensic analysis. The `sub` field is optional and can be empty. |
| Attack Scenario | An attacker who compromises an admin API key can issue unlimited JWTs for any tenant with any scope (`read`, `write`, `admin`), then use those JWTs to access data across tenants. The audit log records "jwt.issue" but cannot distinguish legitimate from malicious issuance without the JWT content. |
| Impact | Privilege escalation from admin-key access to persistent cross-tenant access via long-lived JWTs. |
| Recommendation | 1. Log the issued JWT's details (tenant, scopes, expiry) in the audit log. 2. Add optional MFA requirement for JWT issuance. 3. Add rate limiting to the `/admin/jwt` endpoint (e.g., max 10 tokens/hour per requesting key). 4. Consider a separate "token issuer" scope distinct from general admin. |
| Effort | S |

---

### 11. Soft-delete retention purge may bypass WORM/lock protections

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Medium** |
| Title | Retention GC deletes soft-deleted objects without checking WORM/lock status |
| Location | `internal/reconcile/retention.go` (inferred from reconcile), `internal/repository/sql_objects.go:149` |
| Description | The retention reconciliation job purges soft-deleted objects after `RECONCILE_RETENTION_DAYS`. The `HardDeleteObject` function in `sql_objects.go` deletes rows unconditionally by `tenant_id`, `bucket`, `key` without checking the `locked_until` field. If an attacker or bug soft-deletes a locked object, the retention GC can permanently delete it, violating WORM compliance. |
| Attack Scenario | An object has `locked_until` set to a future date under WORM compliance. It is accidentally soft-deleted. The retention GC runs and hard-deletes it because the DELETE query doesn't check `locked_until`. The object is permanently lost, violating regulatory retention requirements. |
| Impact | Permanent loss of WORM-protected data, regulatory compliance violation. |
| Recommendation | 1. Add `AND (locked_until IS NULL OR locked_until < now())` to the hard-delete query in retention GC. 2. Log a warning when retention GC skips a locked object. 3. Consider moving locked objects to a separate archive table instead of hard-deleting. |
| Effort | S |

---

### 12. No security headers set on responses

| Field | Value |
|-------|-------|
| Category | Compliance |
| Severity | **Low** |
| Title | Missing HTTP security headers (HSTS, X-Content-Type-Options, CSP, X-Frame-Options) |
| Location | `internal/middleware/middleware.go`, `cmd/server/main.go:applyMiddleware` |
| Description | The middleware chain does not include security headers middleware. Missing headers include: `Strict-Transport-Security`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy`, `X-Frame-Options: DENY`, `Referrer-Policy`. The UI is served from the same origin, making these especially important for protecting users of the web UI. |
| Attack Scenario | A user with admin privileges accesses the web UI over HTTP (not HTTPS) and opens a link to a malicious site in another tab. Without CSP and X-Frame-Options, clickjacking and XSS attacks are easier to execute. |
| Impact | Increased attack surface for browser-based attacks against the web UI. |
| Recommendation | Add a `SecurityHeaders` middleware that sets: `Strict-Transport-Security: max-age=31536000; includeSubDomains`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'self'`, `Referrer-Policy: strict-origin-when-cross-origin`. |
| Effort | S |

---

### 13. Anonymous public-read bypasses bucket policy enforcement

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **Medium** |
| Title | Anonymous public-read path may bypass bucket policy checks for object reads |
| Location | `internal/auth/auth_middleware.go:66-70`, `internal/api/rest/handler.go:72` (`allowAnonymous`) |
| Description | When `AUTH_ANONYMOUS_PUBLIC_READ=true`, the auth middleware allows unauthenticated GET/HEAD requests to pass through with `anonCtxKey` set to `true`. The handler's `allowAnonymous` method is expected to enforce object ACLs, but the implementation needs to be verified. The `isObjectReadPath` function checks paths under `/v1/files/`, which means direct S3-compat paths (`/s3/bucket/key`) may bypass anonymous-read checks entirely (since S3 handler's `GetObject` doesn't call `allowAnonymous`). |
| Attack Scenario | A deployer enables anonymous public-read for REST but the S3-compat endpoint (`/s3/bucket/key`) does not apply the same anonymous-read check, allowing unauthenticated object reads via S3 API even when ACLs should deny them. |
| Impact | Information disclosure: public access to supposedly ACL-gated objects via the S3 API. |
| Recommendation | 1. Audit the `allowAnonymous` / ACL enforcement path for the S3-compat handler. 2. Ensure both REST and S3 handlers use the same ACL enforcement logic. 3. Add integration tests covering anonymous reads through both protocols. |
| Effort | S |

---

### 14. PII detector regex for credit cards may cause ReDoS

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **Low** |
| Title | Credit card regex has potential ReDoS vulnerability on crafted input |
| Location | `internal/ai/pii.go:51` |
| Description | The credit card detection regex `\b(?:\d[ \-]?){13,19}\b` uses a repeating group with optional whitespace/dash. While Go's RE2 regex engine is not vulnerable to catastrophic backtracking in the same way as PCRE, the `ReplaceAllStringFunc` callback calls `luhn()` on each match which is O(n). The `Redact` function iterates over the entire text for each PII rule, making it O(rules × text_length). For large documents (>1MB), this could cause latency spikes. |
| Attack Scenario | An attacker uploads a large text file containing millions of digit-like patterns, causing the PII scanner to consume excessive CPU during indexing. |
| Impact | Denial of service via CPU exhaustion during PII scanning of large documents. |
| Recommendation | 1. Add a size limit for PII scanning (e.g., skip documents > 100KB). 2. Simplify the credit card regex to reduce false positives before Luhn check. 3. Consider setting a CPU deadline/context timeout for PII operations. |
| Effort | S |

---

### 15. MCP tool without authentication in stdio mode

| Field | Value |
|-------|-------|
| Category | Authentication |
| Severity | **Low/Info** |
| Title | MCP stdio server operates without auth by design, but capabilities are broad |
| Location | `cmd/server/main.go:30-38`, `internal/mcp/server.go` |
| Description | The `aero-vault mcp` subcommand runs the MCP server over stdio without any authentication. This is expected for local MCP (Claude Desktop) usage. However, the MCP tools include `write_file` (arbitrary object creation), `delete_file` (arbitrary object deletion), and `read_file` with audit recording. The HTTP MCP endpoint (`/mcp`) relies on the chi middleware chain (including auth). |
| Attack Scenario | If the MCP stdio mode is accidentally exposed via a shell wrapper or container entrypoint, an attacker who can write to the process's stdin can execute arbitrary file operations with the server's permissions. |
| Impact | Unauthenticated file operations through MCP transport. |
| Recommendation | 1. Document MCP stdio mode as unauthenticated by design. 2. Add a startup log message clearly stating "MCP stdio mode — no authentication." 3. For HTTP MCP, verify that the auth middleware chain is applied (it is, via chi router). |
| Effort | S |

---

## STRIDE Analysis

| Category | Finding | Severity |
|----------|---------|----------|
| **S**poofing | JWT HS256 symmetric key forgery if secret leaks | High |
| **S**poofing | SigV4 credentials in plaintext env var | Medium |
| **S**poofing | No brute-force protection on auth | High |
| **T**ampering | Pre-signed URL key in plaintext with guessable default | High |
| **T**ampering | User metadata in response headers without validation | Medium |
| **R**epudiation | Admin action audit trail exists but JWT issuance lacks JWT content logging | Medium |
| **R**epudiation | Idempotency replay can obscure whether a write was performed | Low |
| **I**nformation Disclosure | Webhook delivers plaintext event data over HTTP | Medium |
| **I**nformation Disclosure | Idempotency table stores response body in plaintext | Medium |
| **I**nformation Disclosure | Anonymous public-read may bypass ACLs via S3 API | Medium |
| **I**nformation Disclosure | Error messages include internal error details (not sanitized for external users) | Low |
| **D**enial of Service | Rate limiter bucket exhaustion via client-controlled tenant header | Medium |
| **D**enial of Service | PII scanner CPU exhaustion on large documents | Low |
| **D**enial of Service | No limit on multipart upload part count | Low (already mitigated by per-part validation) |
| **E**levation of Privilege | Admin JWT issuance allows arbitrary scope/tenant assignment | Medium |
| **E**levation of Privilege | Retention GC may bypass WORM/lock protections | Medium |
| **E**levation of Privilege | No cross-tenant data access verification in batch operations | Low (tenant scope from middleware) |

---

## OWASP Top 10 Mapping

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| A01: Broken Access Control | ⚠️ Needs Attention | Anonymous read bypass through S3 API, admin JWT scope assignment too broad |
| A02: Cryptographic Failures | ⚠️ Needs Attention | HS256 only, presigned key plaintext, no secrets rotation |
| A03: Injection | ✅ Good | All SQL uses parameterized queries via `s.rebind()`, no command injection |
| A04: Insecure Design | ⚠️ Needs Attention | Rate limiter DoS via tenant header, retention GC misses WORM |
| A05: Security Misconfiguration | ⚠️ Needs Attention | No security headers, CORS wildcard defaults, permissive env defaults |
| A06: Vulnerable Components | ⚠️ Moderate | Go stdlib + chi — audit go.mod for known CVEs |
| A07: Identification & Auth Failures | ⚠️ Needs Attention | No brute-force protection, no MFA, JWT forgery risk |
| A08: Software & Data Integrity | ✅ Good | HMAC-signed webhooks, content-MD5 verification |
| A09: Security Logging & Monitoring | ⚠️ Needs Attention | Auth failures logged but JWT content not logged, no failed-login rate alerts |
| A10: SSRF | ⚠️ Needs Attention | Webhook delivers to arbitrary URLs, no HTTPS enforcement |

---

## Final Summary

| Dimension | Assessment |
|-----------|------------|
| **Overall Security Posture** | **Needs Improvement** |
| **Architecture & Design** | Strong. Good separation of concerns, defense-in-depth with multiple auth mechanisms, proper encryption design with envelope format and key rotation support, parameterized queries everywhere. |
| **Implementation Quality** | Solid Go code with good practices. Thread-safe data structures, proper use of standard library crypto, HMAC constant-time comparison, comprehensive error wrapping. |
| **Production Readiness** | Rough edges for production. Default secrets, missing brute-force protection, no security headers, no HTTPS enforcement for webhooks. These are fixable but need addressing before production. |

### Top 3 Critical Issues

1. **Pre-signed URL signing key defaults to `change-me` and is stored in plaintext** — Attackers who obtain the `.env` file can forge unlimited pre-signed URLs. Fix: require high-entropy key, validate at startup, add rotation support.

2. **JWT uses symmetric HS256 with plaintext secret** — Anyone with `AUTH_JWT_SECRET` can forge tokens for any tenant with any scope. Fix: add RS256/ES256 support with JWKS, or at minimum document that this is a critical secret.

3. **No brute-force protection on authentication** — API keys and JWTs can be brute-forced with no throttling. Fix: IP-based rate limiting on 401/403 responses, exponential backoff, structured auth failure logging.

### Top 3 Quick Wins (High Impact, Low Effort)

1. **Add security headers middleware** — `Strict-Transport-Security`, `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`. One file, 30 minutes.

2. **Warn on default secrets at startup** — Check `STORAGE_LOCAL_SIGN_KEY == "change-me"` and `AUTH_JWT_SECRET == ""` and log with `WARN` level. 15 minutes.

3. **Enforce HTTPS for webhook URLs** — Reject non-HTTPS webhook URLs at startup. Add a flag to allow HTTP for development. 30 minutes.

### Security Debt

| Issue | Priority | Estimated Effort |
|-------|----------|-----------------|
| HS256 → RS256/ES256+JWKS | High | 3-5 days |
| Brute-force protection | High | 1-2 days |
| Secrets rotation for presigned URLs | Medium | 1 day |
| WORM retention GC fix | Medium | ½ day |
| Security headers | Low | ½ day |
| PII scanner DoS protection | Low | ½ day |
| Webhook HTTPS enforcement | Low | ½ day |
| CORS credential documentation | Low | ½ day |
| Idempotency table encryption | Medium | 1-2 days |
| Anonymous read ACL audit across protocols | Medium | 1 day |
