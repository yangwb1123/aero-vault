Based on my thorough review of the codebase, here is the comprehensive security review.

---

# Security Review Report: aero-vault

## Executive Summary

| Field | Value |
|-------|-------|
| **Engine** | 243 Go source files across 24 packages |
| **Auth Mechanisms** | Static API keys, HS256 JWT, AWS SigV4, Persistent hashed keys |
| **Encryption** | AES-256-GCM envelope encryption (local KEK & KMS) |
| **Database** | SQLite (default) / PostgreSQL |
| **AI Pipeline** | Embedder → Indexer → BM25/Vector → RAG Chat + Agent |

---

## Findings

### FINDING-01 — Static API Keys in Environment (Critical)

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Critical** |
| **Title** | Static API key tokens exposed in plaintext environment variable |
| **Location** | `internal/config/config.go:108` (`AUTH_KEYS` → `auth.Parse()` at `internal/auth/auth.go:53`) |
| **Description** | `AUTH_KEYS` env var contains plaintext tokens in `token:tenant:scope+scope` format. These are stored in-memory in `Registry.keys`. Any process with `/proc` access or env-dumping capability extracts all keys. No encryption at rest in environment. |
| **Attack Scenario** | Attacker with SSRF or container escape reads `/proc/1/environ` → extracts `AUTH_KEYS=prod-key:admin:read+write+admin` → full administrative API access. |
| **Impact** | Complete authentication bypass for all configured API keys. |
| **Recommendation** | Migrate entirely to `AUTH_PERSIST_KEYS=true` with SHA-256 hashed storage. Add a startup warning when plaintext env keys are detected in non-dev mode. Consider a `--require-hashed-keys` flag for production. |
| **Effort** | S (< 1 day) |

---

### FINDING-02 — MD5 ETag Integrity (Critical)

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Critical** |
| **Title** | MD5 used for ETag — cryptographically broken |
| **Location** | `internal/service/file_crud.go:146` (`md5WrapReader`), `internal/service/file_crud.go:45` (`ETagVerifier` uses `md5.New()`) |
| **Description** | MD5 is used for both ETag computation and Content-MD5 verification. Practical collision attacks exist (SHAttered, chosen-prefix). MD5 should not be used for integrity in a security boundary. |
| **Attack Scenario** | Attacker generates two files with identical MD5 but different content (chosen-prefix collision, ~$45k compute). Uploads malicious version, CDN/gateway caches malicious content under the legitimate ETag. Subsequent clients receive attacker-controlled data. |
| **Impact** | Integrity bypass, CDN cache poisoning, supply-chain style content substitution. |
| **Recommendation** | Compute ETag as SHA-256: `ETag: sha256-<hex>` format (like AWS S3 does for SHA-256 checksums). Keep MD5 for Content-MD5 backward compatibility but deprecate it. Add `x-amz-checksum-sha256` header support. |
| **Effort** | M (1–3 days) |

---

### FINDING-03 — Anonymous Public-Read Data Exposure (Critical)

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | Anonymous public-read grants unauthenticated GET/HEAD access to all objects (high risk when ACLs are not configured) |
| **Location** | `internal/auth/auth_middleware.go:68-72` (`authenticateBearer` anonRead path) → `internal/api/rest/handler.go:58` (`allowAnonymous`) |
| **Description** | When `AUTH_ANONYMOUS_PUBLIC_READ=true`, any request without credentials on GET/HEAD `/v1/files/*` passes through with an anonymous context marker. The handler calls `allowAnonymous` which checks object ACL, but most objects have **no ACL** configured (default behavior). Without an ACL, the code at `internal/api/rest/acl.go` likely allows access. |
| **Attack Scenario** | Operator enables `AUTH_ANONYMOUS_PUBLIC_READ=true` for a public bucket feature. Any object in the system without an explicit deny-ACL becomes world-readable — including objects in other tenants. Anonymous attacker enumerates `/v1/files/` with sequential keys. |
| **Impact** | Unauthenticated read access to all objects without explicit ACL restrictions. |
| **Recommendation** | Change default to **deny** anonymous access unless both (a) the object has a public-read ACL and (b) anonymous mode is enabled. Require explicit opt-in at the bucket level, not at the global config level. |
| **Effort** | S (< 1 day) |

---

### FINDING-04 — HTTP Response Splitting via Content-Disposition Metadata (High)

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Content-Disposition stored in metadata without sanitization enables HTTP response splitting |
| **Location** | `internal/service/file.go:423` (`addContentHeaders`), `internal/api/rest/handler.go:326` (`writeContentResponseHeaders`) |
| **Description** | `Content-Disposition` and `Content-Encoding` values from upload requests are stored verbatim in `_aero_content_disposition` / `_aero_content_encoding` metadata and echoed back as HTTP headers on GET/HEAD. CR/LF characters are not stripped. |
| **Attack Scenario** | Attacker uploads object with header `Content-Disposition: attachment\r\nSet-Cookie: malicious=value`. Every GET/HEAD response for this object will contain the injected header, allowing session fixation, cache poisoning, or XSS in browser contexts. |
| **Impact** | HTTP response splitting, cache poisoning, XSS via browser-based API consumers. |
| **Recommendation** | Sanitize both values before storing: reject or strip control characters (`\r`, `\n`, `\0`). Validate against RFC 6266 / RFC 7231 patterns. |
| **Effort** | S (< 1 day) |

---

### FINDING-05 — Search Index Orphans on Hard Delete Failure (High)

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering / Data Retention) |
| **Severity** | **High** |
| **Title** | Chunk cleaner failure during hard delete leaves orphaned search index entries |
| **Location** | `internal/service/file_crud.go:260-263` (`hardDeleteObject` chunk cleanup) |
| **Description** | Per AGENTS.md rule I3 (design constraint), `DeleteObjectChunks` failure is non-fatal — hard delete proceeds. But this means BM25/Qdrant/pgvector index entries containing document text become permanent orphans. The data is gone from storage/DB but still discoverable via search. |
| **Attack Scenario** | Attacker triggers hard delete on a sensitive document (e.g., PII records). The Qdrant chunk cleaner temporarily errors (network blip). The storage/blob row is removed, but full-text chunks remain in the search index. Any user with search access can still find the deleted content. |
| **Impact** | Data retention policy violation — deleted data remains discoverable indefinitely. |
| **Recommendation** | On chunk cleaner failure, enqueue a deferred cleanup job (via JobPool) rather than silently continuing. Implement a periodic GC that detects and removes chunks whose source object no longer exists. |
| **Effort** | M (1–3 days) |

---

### FINDING-06 — SSE Connections Exhaustion DoS (High)

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **High** |
| **Title** | No limit on concurrent SSE connections |
| **Location** | `internal/api/rest/sse.go:91` (`Stream` handler — sets `SetWriteDeadline(time.Time{})`) |
| **Description** | The SSE `/v1/events/stream` endpoint disables write deadlines and has no cap on concurrent connections. Each connection holds a goroutine, an event bus subscriber slot (buffered channel), and an HTTP file descriptor. An attacker can exhaust resources with moderate connection count. |
| **Attack Scenario** | Attacker opens 50,000 SSE connections from a botnet. Each connection creates a subscriber channel (64-buffer depth), a goroutine, and a file descriptor. Server runs out of memory/file handles → service unavailable for legitimate users. |
| **Impact** | Denial of service for all API consumers. |
| **Recommendation** | Add a configurable max-connections cap (default 100–500). Return 503 when exceeded. Add per-IP connection limiting. Consider closing SSE connections that have been idle > 30 minutes. |
| **Effort** | S (< 1 day) |

---

### FINDING-07 — Metadata Key Injection in Response Headers (High)

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | User-controlled metadata keys emitted as raw HTTP headers enable injection |
| **Location** | `internal/api/rest/handler.go:385-394` (`extractMetadataHeaders`), `handler.go:282-291` (`writeMetadataHeaders`) |
| **Description** | User-provided metadata keys are stored verbatim and echoed as `X-Meta-<key>` HTTP headers. A key containing `\r\n`, colons, or other HTTP-special characters allows arbitrary header injection. |
| **Attack Scenario** | PUT object with metadata header `X-Meta-foo\r\nSet-Cookie: session=injected`. GET response includes the injected cookie header, allowing session fixation against the web UI operator. |
| **Impact** | HTTP response injection, session fixation, cache poisoning. |
| **Recommendation** | Validate metadata keys against `^[a-zA-Z0-9_-]+$`. Reject keys with control characters, colons, or non-ASCII. Apply URL-percent-encoding or base64-encode keys for header emission. |
| **Effort** | S (< 1 day) |

---

### FINDING-08 — JWT Algorithm Restricted to HS256 (Medium)

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | JWT only supports HS256 with static shared secret — no key rotation or asymmetric signing |
| **Location** | `internal/auth/jwt.go:38-40` (`NewJWTVerifier`), `jwt.go:80-84` (hardcoded HS256 check) |
| **Description** | JWT verification is hardcoded to HS256. There is no key rotation mechanism, no JWKS endpoint, no support for RS256/ES256. The same secret signs and verifies. If the secret leaks, all existing tokens are invalid and no graceful rotation path exists. |
| **Attack Scenario** | Attacker leaks the `AUTH_JWT_SECRET` from config. Can forge tokens for any tenant with any scope, including `admin`. Since there's no `kid` header support, loading a new secret requires restart, and all outstanding tokens become invalid simultaneously. |
| **Impact** | Authentication bypass if secret leaked. Operational disruption during key rotation. |
| **Recommendation** | Add support for multiple verification keys keyed by `kid` header. Implement a zero-downtime rotation API. Consider RS256 with JWKS for production (delegates key management to an IdP). |
| **Effort** | L (> 3 days) |

---

### FINDING-09 — JWT Token Expiration Oracle (Medium)

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Information Disclosure) |
| **Severity** | **Medium** |
| **Title** | JWT verification returns different error messages for expired vs. bad-signature tokens |
| **Location** | `internal/auth/jwt.go:63` ("jwt: expired"), `jwt.go:127` ("jwt: bad signature") |
| **Description** | The JWT verifier returns distinct error strings for expired tokens vs. invalid signatures. An attacker can brute-force JWT secrets offline and verify correctness by observing the error message: "bad signature" means the HMAC computed correctly but an invalid secret was used for signing; "expired" indicates the secret was correct but the token's time window has passed. This reduces the effective key space for brute-force attacks. |
| **Attack Scenario** | Attacker captures an expired JWT. They brute-force the secret using the token. For each candidate secret, they regenerate the HMAC. If the server returns "expired" (rather than "bad signature"), they've found the correct secret → can forge tokens for any tenant. |
| **Impact** | Reduced brute-force resistance; information leakage aiding secret recovery. |
| **Recommendation** | Return a generic "invalid token" error for all JWT verification failures. Log the specific reason server-side for debugging only. |
| **Effort** | S (< 1 day) |

---

### FINDING-10 — No Default Security Headers (Medium)

| Field | Value |
|-------|-------|
| **Category** | Compliance (OWASP) |
| **Severity** | **Medium** |
| **Title** | Missing security headers on HTTP responses |
| **Location** | `internal/middleware/middleware.go` (entire file — no security headers middleware) |
| **Description** | The application does not set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, `Strict-Transport-Security`, or `Referrer-Policy`. While the REST API is primarily machine-to-machine, the web UI (`/ui`) and SSE endpoint are browser-consumable. |
| **Attack Scenario** | An attacker tricks an admin into visiting a malicious page while authenticated to the aero-vault web UI. Due to missing `X-Frame-Options`, the attacker can embed the web UI in an iframe (clickjacking). Or, due to missing `X-Content-Type-Options`, a browser may MIME-sniff a response. |
| **Impact** | Clickjacking, MIME-sniffing attacks against browser-based consumers. |
| **Recommendation** | Add a middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'self'`, `Strict-Transport-Security: max-age=31536000; includeSubDomains`. |
| **Effort** | S (< 1 day) |

---

### FINDING-11 — Rate Limiter Default Disabled (Medium)

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **Medium** |
| **Title** | Rate limiting defaults to disabled (RPS=0) |
| **Location** | `internal/config/config.go:245` (`RATE_LIMIT_RPS` defaults to 0) |
| **Description** | Both global and AI-specific rate limiters default to zero, meaning disabled. A production deployment without explicit rate limiting configuration is vulnerable to brute-force and resource exhaustion attacks. |
| **Attack Scenario** | Attacker sends 1M requests/minute to `/v1/search` or `/v1/files/`. No rate limiting is applied. The embedder/LLM endpoints are called for each request, incurring unbounded cost. Database connections, storage bandwidth, and AI API costs spiral. |
| **Impact** | Unbounded resource consumption, AI API cost explosion, denial of service for legitimate users. |
| **Recommendation** | Set sensible defaults (e.g., RPS=100, Burst=200, AI_RPS=10, AI_Burst=20). Fail closed with a warning log when zero is configured in production-like mode. |
| **Effort** | S (< 1 day) |

---

### FINDING-12 — Idempotency Cache Key Not Tenant-Scoped (Medium)

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | Idempotency-Key responses may be reused across tenants |
| **Location** | `internal/api/rest/idempotency.go` (referenced in `router.go:42`) |
| **Description** | The idempotency middleware uses the `Idempotency-Key` header and optionally the body hash for deduplication. If the cache key does not include the tenant ID, two tenants with the same key+body may receive each other's cached responses. |
| **Attack Scenario** | Tenant A issues a PUT with `Idempotency-Key: abc`. Tenant B (or an attacker posing as Tenant B) sends the same key+body. The cached response from Tenant A's request is returned, which could contain Tenant A's object metadata or version ID. |
| **Impact** | Cross-tenant information disclosure through response caching. |
| **Recommendation** | Include `tenant_id` in the idempotency cache key (as a prefix or hash component) to ensure tenant isolation. |
| **Effort** | S (< 1 day) |

---

### FINDING-13 — API Key Last-4 in Audit Logs (Medium)

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Partial API key disclosure in audit logs |
| **Location** | `internal/api/rest/admin.go:107` (`AddKey` audit call), `admin.go:214` (`redactToken`) |
| **Description** | The `redactToken` function reveals the last 4 characters of an API key: `"****aBcD"`. An attacker with audit log access can use this as an oracle. |
| **Attack Scenario** | Attacker gains read-only access to the `audit_log` table (e.g., via a monitoring dashboard). They see `key.add: token=****XyZ1`. Combined with knowledge that the key was added for tenant "acme", they can reduce brute-force space for the remaining prefix. |
| **Impact** | Partial disclosure of API key material; reduces effective key entropy. |
| **Recommendation** | Store and log a fully independent key ID (UUID v4) rather than revealing any part of the token. The token's hash is the DB identity; use a separate opaque identifier for operational correlation. |
| **Effort** | S (< 1 day) |

---

### FINDING-14 — Auth Disabled by Default (Low)

| Field | Value |
|-------|-------|
| **Category** | Configuration |
| **Severity** | **Low** |
| **Title** | Authentication disabled by default (AUTH_KEYS empty) |
| **Location** | `internal/config/config.go:108` (`AUTH_KEYS` defaults to `""`), `internal/auth/auth.go:53` (empty → disabled) |
| **Description** | With no `AUTH_KEYS` configured, authentication is completely disabled. All routes (including admin) are open. While documented as MVP behavior, this creates a high-risk default for inexperienced operators. |
| **Attack Scenario** | Operator deploys to a cloud VM with default config. Anyone who discovers the IP can read, write, delete data across all tenants, including admin operations. |
| **Impact** | Complete data exposure in uninformed deployments. |
| **Recommendation** | Require explicit opt-in to disable auth (`AUTH_DISABLED=true`). Default should fail-closed with startup error: "AUTHENTICATION REQUIRED: configure AUTH_KEYS, AUTH_JWT_SECRET, or set AUTH_DISABLED=true explicitly." |
| **Effort** | S (< 1 day) |

---

### FINDING-15 — Bucket Policy IP Conditions Use Untrusted RemoteAddr (Low)

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | Bucket policy IP conditions based on `r.RemoteAddr`, not `X-Forwarded-For` |
| **Location** | `internal/auth/policy.go:108` (`Eval`), `internal/api/rest/handler.go:58` (`checkBucketPolicy` extracts IP from `r.RemoteAddr`) |
| **Description** | The IP-based condition in bucket policies (`IpAddress`/`NotIpAddress`) uses `r.RemoteAddr`, which in cloud/reverse-proxy deployments is the proxy's IP, not the client's. The policy `{"Effect":"Deny","Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}}` would be ineffective behind a load balancer. |
| **Attack Scenario** | Operator sets IP-restricted bucket policy expecting it to limit access to corporate VPN range. Behind a cloud LB, `RemoteAddr` is always the LB's IP. Anyone reaching the LB bypasses the IP restriction. |
| **Impact** | IP-based access controls in bucket policies silently ineffective behind proxies. |
| **Recommendation** | Add configurable `TRUSTED_PROXY_CIDRS` to extract real client IP from `X-Forwarded-For`. Document this behavior. AWS S3 has the same limitation — document clearly. |
| **Effort** | S (< 1 day) |

---

## STRIDE Threat Model Summary

| Threat | Risk | Key Mitigations | Gaps |
|--------|------|-----------------|------|
| **S**poofing | **High** | API key hashing, JWT verification, SigV4 | HS256 only, no public-key crypto for JWT |
| **T**ampering | **Medium** | AES-256-GCM SSE, Content-MD5, ETag verification | MD5 ETag collision risk |
| **R**epudiation | **Medium** | Audit logging for admin actions | No event signing for non-repudiation; webhook uses HMAC |
| **I**nformation Disclosure | **High** | SSE at rest, tenant isolation, PII detection | Metadata header injection, anonymous read bypass, audit log partial key disclosure |
| **D**enial of Service | **High** | Rate limiter, concurrency limiter, circuit breaker | All limiters default to off; SSE unbounded connections |
| **E**levation of Privilege | **High** | Scope-based auth, tenant isolation, admin gate | Static env keys, no default auth, JWT oracle |

---

## OWASP Top 10 Compliance

| OWASP | Status | Notes |
|-------|--------|-------|
| **A01: Broken Access Control** | ⚠️ Needs Improvement | Anonymous read bypass (F-03), auth disabled by default (F-14) |
| **A02: Cryptographic Failures** | ⚠️ Needs Improvement | MD5 for ETag (F-02), JWT HS256 only (F-08) |
| **A03: Injection** | ⚠️ Needs Improvement | Metadata header injection (F-04, F-07), query param SQL uses parameterized queries ✅ |
| **A04: Insecure Design** | ⚠️ Needs Improvement | SSE DoS (F-06), search index orphans (F-05) |
| **A05: Security Misconfiguration** | ⚠️ Needs Improvement | Auth disabled by default (F-14), rate limit off (F-11) |
| **A06: Vulnerable Components** | ✅ Good | Direct dependencies include `chi`, `uuid`, standard crypto |
| **A07: ID & Auth Failures** | ⚠️ Needs Improvement | Env key exposure (F-01), JWT oracle (F-09) |
| **A08: Software/Data Integrity** | ⚠️ Needs Improvement | MD5 ETag (F-02), hard delete orphan (F-05) |
| **A09: Security Logging Failures** | ✅ Adequate | Audit logging, request IDs, structured access logging |
| **A10: SSRF** | ✅ Good | Webhook/LLM/Embedder endpoints are configurable URLs |

---

## Final Summary

### Overall Security Posture: Needs Improvement

The codebase demonstrates strong security awareness in many areas:
- Parameterized SQL queries throughout (no SQL injection ✅)
- AES-256-GCM envelope encryption with KMS support ✅
- Scope-based authorization with tenant isolation ✅
- Secure defaults for many feature flags (all opt-in) ✅
- Proper secret management for API keys (SHA-256 hashing) ✅

However, there are **Critical** and **High** severity issues that should be addressed before production deployment.

### Top 3 Critical Issues

1. **Static API keys in environment variable** (F-01) — any env disclosure leaks all credentials
2. **MD5 ETag collision attack** (F-02) — broken cryptographic primitive for integrity
3. **Anonymous public-read data exposure** (F-03) — design flaw in anonymous access path

### Top 3 Quick Wins (High Impact, Low Effort)

1. **Metadata header sanitization** (F-04, F-07) — strip CR/LF from stored headers → < 1 day
2. **Default security headers** (F-10) — add middleware → < 1 day
3. **Generic JWT errors** (F-09) — remove oracle → < 1 hour

### Security Debt

| Issue | Type | Effort |
|-------|------|--------|
| HMAC-based integrity for events + logs | Non-repudiation | M |
| RS256/JWKS support for JWT | Key management | L |
| Per-object ACL default-deny | Access control | M |
| Background GC for orphaned search chunks | Data retention | M |
| Cross-replica SSE connection limits | DoS resilience | S |
| Rate limiter sensible defaults | Configuration | S |
