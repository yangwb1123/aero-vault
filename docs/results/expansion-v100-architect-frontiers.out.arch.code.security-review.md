Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# 🔒 Security Review: Aero-Vault Codebase

**Reviewer:** Principal Security Engineer  
**Codebase:** `github.com/aero-vault/aero-vault` (243 Go source files)  
**Commit:** `1d98e54` (latest)  
**Review Date:** 2026-07-12  

---

## Executive Summary

Aero-Vault is a well-architected object storage system with strong foundations: parameterized SQL queries throughout, AES-256-GCM envelope encryption, per-tenant rate limiting, audit logging for admin actions, and a clear middleware chain. However, several **critical** and **high-severity** issues exist that would compromise the system under adversarial conditions.

---

## Detailed Findings

### 🔴 CRITICAL

---

| Field | Description |
|-------|-------------|
| **Category** | Cryptography / Threat Model (Tampering) |
| **Severity** | **Critical** |
| **Title** | SigV4 Streaming Upload Chunk Signatures Not Verified |
| **Location** | `internal/auth/sigv4_chunk.go:31-39` — `decodeStreamingBody()` |
| **Description** | When a SigV4-signed upload uses chunked transfer encoding (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`), the AWS protocol requires each chunk to carry an individual HMAC signature that the server must re-verify sequentially. This implementation strips the chunk framing and trusts the client-provided header-boundary signature only, without re-verifying any per-chunk HMAC. |
| **Attack Scenario** | A man-in-the-middle (e.g., compromised VPC endpoint, malicious proxy) intercepts a SigV4-signed multipart upload from a legitimate client. The attacker modifies the data payload of every chunk after the first. The server accepts the tampered body because no per-chunk signature check is performed. The on-disk object differs from what the client intended, yet the ETag matches (computed after our de-chunking). |
| **Impact** | Any client using `aws-sdk-go` default streaming upload (which uses chunked encoding) is vulnerable to undetected data tampering. This violates AWS SigV4 integrity guarantees. |
| **Recommendation** | Implement per-chunk signature verification per the [AWS SigV4 chunked upload spec](https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-streaming.html). Each chunk's `chunk-signature=<hex>` must be HMAC-verified against the seed signature before the data is accepted. |
| **Effort** | M (2-3 days for correct implementation + tests) |

---

| Field | Description |
|-------|-------------|
| **Category** | Authentication / Cryptography |
| **Severity** | **Critical** |
| **Title** | API Key Hashes Use Plain SHA-256 (No KDF) |
| **Location** | `internal/auth/store.go:34-37` — `HashToken()` |
| **Description** | Persisted API key tokens are hashed with a single round of SHA-256 before storage. SHA-256 is a fast hash designed for integrity, not password/key storage. An attacker who obtains the `api_keys` table can brute-force API keys at billions of attempts per second using GPU/ASIC hardware. |
| **Attack Scenario** | Database dump is obtained via SQL injection, backup exposure, or rogue DBA. The attacker runs hashcat on the `token_hash` column. At ~20 GH/s for SHA-256 on consumer GPUs, a 16-char alphanumeric key (95 bits) can be brute-forced in manageable time for short keys. Each cracked key grants authenticated access with its associated scopes. |
| **Impact** | Total compromise of all persisted API keys, with potential lateral movement across tenants. |
| **Recommendation** | Replace `HashToken` with a memory-hard KDF: PBKDF2 (≥310k iterations per OWASP 2023), bcrypt (cost ≥12), or argon2id (preferred). Alternatively, generate cryptographically random opaque identifiers as the stored hash and keep the plaintext token one-way-only. |
| **Effort** | M (2-3 days including migration path for existing hashes) |

---

### 🟠 HIGH

---

| Field | Description |
|-------|-------------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | MCP Endpoint Lacks Scope Enforcement |
| **Location** | `internal/mcp/server.go:44-52` — `Server.callTool()` dispatch; `internal/api/rest/router.go:36-52` — router mount |
| **Description** | The MCP server exposes `write_file` and `delete_file` tools that call `svc.Put()` and `svc.Delete()` directly, but the MCP handler never checks the caller's auth scopes. Any authenticated user (even with only `read` scope) who can reach the `/mcp` endpoint can create, modify, or delete objects. The standard REST routes enforce scope via `checkScope()` in the auth middleware, but the MCP path has no equivalent. |
| **Attack Scenario** | An attacker obtains a read-only API key (scope: `read`). They call POST `/mcp` with `{"method":"tools/call","params":{"name":"write_file","arguments":{"key":"malicious.txt","content":"pwned"}}}`. The request passes auth middleware (scope `read` is sufficient for a POST to `/mcp` since the middleware only checks method→scope mapping for the generic path, not the MCP-specific method dispatch). The file is written to storage. |
| **Impact** | Unauthorized data modification/deletion with a read-only credential. Violates the scope-based authorization model. |
| **Recommendation** | Add scope checks to each MCP tool dispatch. The server should check `auth.FromContext(ctx)` and validate that the caller has the required scope (read for `list_files`/`read_file`/`search`/`chat`; write for `write_file`/`delete_file`). |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | JWT Uses Only HS256 (Symmetric) — No Asymmetric Key Support |
| **Location** | `internal/auth/jwt.go:21-25` — `JWTVerifier` struct; `jwt.go:61-68` — `decodeJWTHeader()` |
| **Description** | The JWT implementation only supports HS256 (HMAC-SHA256 symmetric signing). The same secret is used to both sign and verify tokens. There is no support for RS256, ES256, or JWKS key rotation. If the `AUTH_JWT_SECRET` is compromised, an attacker can forge tokens for any tenant. The `jwtHeader` parsing rejects any algorithm other than HS256 with an error, but the error message ("jwt: unsupported alg") is returned to the caller via the HTTP response, leaking server configuration. |
| **Attack Scenario** | An attacker discovers `AUTH_JWT_SECRET` via log exposure, env dump, or misconfiguration. With the symmetric key, they can forge valid JWT tokens for any tenant and any scope, including `admin`. Alternatively, the `alg` error message reveals the supported algorithm, enabling targeted attacks. |
| **Impact** | Full authentication bypass if the secret is compromised. No forward migration path without a breaking change. |
| **Recommendation** | Add RS256/ES256 support alongside HS256 (opt-in). Implement JWKS endpoint support for key rotation. Return a generic "invalid token" error rather than revealing the supported algorithm. |
| **Effort** | L (3-5 days for asymmetric support + JWKS + migration) |

---

| Field | Description |
|-------|-------------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Bucket Policy Parse Failure Silently Falls Back to Allow-All |
| **Location** | `internal/api/rest/handler.go:58-68` — `checkBucketPolicy()`; `internal/api/s3compat/handler.go:65-75` — `checkBucketPolicy()` |
| **Description** | When `auth.ParsePolicy(cfg.Policy)` returns an error (malformed JSON, invalid policy document), the code logs a warning but returns `true` (allow the request). A malformed policy silently grants full access to the bucket instead of denying all actions. |
| **Attack Scenario** | An admin accidentally sets a malformed bucket policy (e.g., via the `putBucketPolicy` S3-compat endpoint with a typo). The policy is stored successfully (no validation on write). From that moment, all policy enforcement for that bucket is disabled, allowing any authenticated user full access regardless of the intended policy. This could go unnoticed since it only surfaces as a WARN-level log line. |
| **Impact** | Complete bypass of bucket-level access controls on misconfiguration. No alerts or error responses to the client. |
| **Recommendation** | On policy parse failure, **fail closed**: return a 403 Forbidden instead of allowing the request. Add policy validation on write (the `PutBucketPolicy`/`SetBucketPolicy` handlers should validate the policy before storing). |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | No Input Validation on Tenant IDs, Bucket Names, or Tag Keys |
| **Location** | `internal/service/file.go:67-74` — `validateKey()` validates keys only; no equivalent for tenant/bucket/tag validation |
| **Description** | The codebase validates object keys (empty, `/` prefix, `..` traversal) but does not validate tenant IDs, bucket names, or tag keys/values for length, character set, or injection-friendly characters. While SQL injection is prevented (parameterized queries), these values are used in storage paths (`storageKey = path.Join(tenant, bucket, key)`), logs, response headers, and events — where special characters could cause issues. |
| **Attack Scenario** | A tenant ID like `../../../etc` combined with `validateKey`'s check for `..` in the key could still pass since the tenant ID is not validated. The `storageKey` uses `path.Join(tenant, bucket, key)` which would resolve `../` sequences. An attacker could write or read files outside the intended storage directory. |
| **Impact** | Potential path traversal through unvalidated tenant/bucket identifiers, leading to data access outside the intended scope. |
| **Recommendation** | Add strict validation for tenant IDs (alphanumeric + `-._`, max 64 chars), bucket names (DNS-compliant, 3-63 chars, no `..`), and tag keys/values (Unicode but no control characters, max 128/256 chars). Validate these at the service boundary in `file.go`. |
| **Effort** | M (1-2 days) |

---

### 🟡 MEDIUM

---

| Field | Description |
|-------|-------------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Internal Error Messages Leak Sensitive Details to Clients |
| **Location** | `internal/api/rest/handler.go:202-224` — `classify()` fallthrough to `default: "InternalError", err.Error()`; throughout error responses |
| **Description** | The `classify` function's default case returns `err.Error()` verbatim to the client as the HTTP response body. Many error paths propagate internal details (SQL errors, storage backend errors, file paths, stack traces are stripped by middleware but error messages may contain internal paths/config). |
| **Attack Scenario** | An attacker sends malformed requests that trigger edge-case errors. The response body contains internal details: `"storage put: s3 upload: AccessDenied: Access Denied"` or SQL constraint errors revealing schema details. This aids further attack refinement. |
| **Impact** | Information disclosure aiding reconnaissance. |
| **Recommendation** | In production mode, return a generic "InternalError" message and log the full error server-side. Use a whitelist of safe error messages for external consumption. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | Missing Security Headers |
| **Location** | Entire HTTP response path — `internal/middleware/middleware.go`, `internal/middleware/cors.go`, main.go middleware chain |
| **Description** | The server does not set standard security response headers: `X-Content-Type-Options`, `X-Frame-Options`, `X-XSS-Protection`, `Content-Security-Policy`, or `Strict-Transport-Security`. The `/ui` embedded SPA serves without CSP, making it potentially vulnerable to XSS if user content is rendered. |
| **Attack Scenario** | An attacker uploads a file named `image.html` containing JavaScript through the REST API. When accessed via `/ui` (the SPA), the browser may render it as HTML (no `X-Content-Type-Options: nosniff`). The script executes in the origin of the vault, accessing stored data. |
| **Impact** | XSS attacks against the Web UI, data exfiltration via script execution. |
| **Recommendation** | Add a middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'self'` (for `/ui` routes), and `Strict-Transport-Security: max-age=31536000` (when TLS is configured). |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Admin `ListKeys` Returns Unredacted Scope and Tenant Information |
| **Location** | `internal/auth/auth.go:164-180` — `ListKeys()`; `internal/api/rest/admin.go:106-110` — `ListKeys` handler |
| **Description** | The `ListKeys` admin endpoint returns `Key` objects with full scope and tenant information for all registered keys. While tokens are redacted, the endpoint does not filter by tenant — an admin with `*` tenant scope can list keys belonging to all tenants. Persisted keys return labels (human-readable hints) that may leak which services or users a key was issued for. |
| **Attack Scenario** | A compromised admin key holder lists all API keys, identifies high-privilege key labels, and targets those tenants or workloads for lateral movement. |
| **Impact** | Leak of key metadata aiding targeted attacks. |
| **Recommendation** | Add tenant-scoped filtering to `ListKeys`: non-`*`-tenant admins should only see keys for their own tenant. Remove labels from the API response or make them optional/no-op for non-admin tenants. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | Authorization Header Prefix Matching Is Case-Sensitive |
| **Location** | `internal/auth/auth_middleware.go:108-117` — `extractToken()` |
| **Description** | The `extractToken` function checks for exact prefix casing: `"Bearer "`, `"ApiKey "`, `"bearer "`, `"apikey "`. HTTP header values like `"BEARER token"` or `"AUTHORIZATION: Bearer Token"` (mixed casing) would fail authentication because no prefix matches, causing the function to fall through to `X-Api-Key` header check or return empty. |
| **Attack Scenario** | A legitimate client sends `Authorization: BEARER <token>` (all caps). Authentication fails with "missing Authorization header", the request is rejected, and the client may retry with the raw token in the `X-Api-Key` header (which is accepted as a fallback), creating a credential exposure vector (tokens in custom headers are more likely logged than standard Authorization headers). |
| **Impact** | Authentication failures for some clients; potential credential exposure through fallback `X-Api-Key` header. |
| **Recommendation** | Normalize the Authorization header prefix by trimming leading whitespace and comparing case-insensitively before stripping the prefix. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | SSE Key HTTP Provider Transmits Keys in Clear if URL is HTTP |
| **Location** | `internal/storage/secret.go:122-139` — `newHTTPProvider()` |
| **Description** | The SSE key ring provider can fetch master keys from an HTTP(S) URL. If a user configures an HTTP URL (not HTTPS), the master key material is transmitted in clear text over the network. The documentation does not warn about this requirement. |
| **Attack Scenario** | An operator configures `STORAGE_LOCAL_SSE_KEY_URL=http://vault.internal:8200/v1/keys/sse` (HTTP, no TLS). An attacker on the same network segment captures the HTTP response and obtains the master key ring, enabling decryption of all SSE-encrypted objects. |
| **Impact** | Complete compromise of encryption-at-rest. |
| **Recommendation** | Add a validation check that rejects non-HTTPS URLs for SSE key providers, or at minimum log a WARN-level alert. Document the HTTPS requirement prominently. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **Medium** |
| **Title** | No Body Size Limit on Unauthenticated Multi-tenant Rate Limiter Bucket Creation |
| **Location** | `internal/middleware/ratelimit.go:61-83` — `Allow()` |
| **Description** | The per-tenant rate limiter creates a new bucket for each unique tenant key. While the map is bounded at 50,000 entries with an eviction strategy, an attacker can still cause 50,000 buckets to be created in memory by sending requests with unique `X-Aero-Tenant` header values. Each bucket holds a struct with a float64 and time.Time. The map is also guarded by a mutex on every request, potentially causing contention under high load. |
| **Attack Scenario** | An attacker sends 50,000 requests with 50,000 unique `X-Aero-Tenant` header values. Each request fills a new bucket in the rate limiter map (after the first 50,000, eviction kicks in but the map stays full). After the rate is exhausted, the server serves 429 for all tenants due to map full check (`return false, rlEvictInterval`). |
| **Impact** | Global denial of service for all tenants by exhausting the rate limiter bucket map. |
| **Recommendation** | Add a maximum tenant ID length validation (reject long tenant IDs at middleware level). Consider using a LRU cache instead of a plain map for rate limiter buckets. |
| **Effort** | M (1-2 days) |

---

### 🔵 LOW

---

| Field | Description |
|-------|-------------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | Admin API Token Creation Has No Minimum Length or Complexity Requirements |
| **Location** | `internal/api/rest/admin.go:113-140` — `AddKey()` handler |
| **Description** | The admin API key creation endpoint accepts arbitrary tokens without any minimum length, character set, or entropy requirements. A token could be a single character. Short tokens are trivially brute-forced. |
| **Attack Scenario** | An admin creates an API key with token `"a"`. An attacker enumerating API keys via timing side-channel or login brute-force discovers the short key and gains unauthorized access. |
| **Impact** | Weak keys weaken the authentication boundary. |
| **Recommendation** | Enforce a minimum token length of 16 characters (or auto-generate tokens with crypto/rand on key creation, making the token field optional). |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Webhook Payload Contains Full Event Details Including Keys |
| **Location** | `internal/events/webhook.go:75-85` — `deliver()` body marshaling |
| **Description** | Webhook deliveries include the full event payload including object `key`, `bucket`, `type`, and custom `payload` map. This data is transmitted to the webhook URL over plain HTTP if HTTPS is not configured, and the payload is not encrypted at the application layer (only HMAC-signed if a secret is configured). |
| **Attack Scenario** | A misconfigured webhook URL (`http://`) causes event data (including object keys and metadata) to traverse the network in clear text. Object keys may encode sensitive information (e.g., `invoices/john-doe-ssn-123-45-6789.pdf`). |
| **Impact** | Potential leakage of metadata through webhook delivery. |
| **Recommendation** | Document that webhook URLs should use HTTPS. Add a startup-time warning if `EVENTS_WEBHOOK_URL` starts with `http://`. Consider supporting payload encryption at the application layer. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Session Management |
| **Severity** | **Low** |
| **Title** | JWT Issuer Enforcement Not Used by Default |
| **Location** | `internal/auth/jwt.go:33-43` — `WithIssuer()` |
| **Description** | The JWT issuer (`iss` claim) enforcement is opt-in via `WithIssuer()`. By default, no issuer check is performed, meaning any validly-signed JWT (with any issuer) is accepted. This could allow cross-service token reuse if the same secret is (mis)shared. |
| **Attack Scenario** | Two services share the same `AUTH_JWT_SECRET` for operational convenience. Without issuer pinning, a token issued for Service A (with `iss: service-a`) is accepted by Service B, granting unintended access. |
| **Impact** | Cross-service token reuse vulnerability when secrets are shared. |
| **Recommendation** | Make issuer pinning the default when `AUTH_JWT_ISSUER` is configured, and enforce it on verify. When `AUTH_JWT_ISSUER` is set, the `Sign` method already stamps it — verify should reject tokens without the matching `iss`. |
| **Effort** | S (< 1 day) |

---

| Field | Description |
|-------|-------------|
| **Category** | Threat Model (Spoofing) |
| **Severity** | **Low** |
| **Title** | MCP HTTP Handler Does Not Validate JSON-RPC Content-Type |
| **Location** | `internal/mcp/transport.go:46-68` — `HTTPHandler()` |
| **Description** | The MCP HTTP handler accepts POST requests without checking the `Content-Type` header. The JSON-RPC spec recommends `application/json` or `application/json-rpc`. Cross-site request forgery via form-encoded POST could reach this endpoint if CORS is configured permissively (the `/mcp` path is not in the CORS bypass list). |
| **Attack Scenario** | CORS is configured with `AllowedOrigins: ["*"]`. A malicious page makes a `mode: no-cors` or form POST to `/mcp` with a crafted JSON-RPC body. While `application/json` would trigger a preflight, a form-post with `Content-Type: text/plain` would not, potentially executing a tool call. |
| **Impact** | Limited CSRF vector for MCP tool execution. |
| **Recommendation** | Enforce `Content-Type: application/json` on the MCP HTTP endpoint. Reject other content types with 415 Unsupported Media Type. |
| **Effort** | S (< 1 day) |

---

## STRIDE Analysis Summary

| Category | Risk Level | Key Findings |
|----------|-----------|-------------|
| **S**poofing | **High** | SHA-256 key hashing (no KDF); HS256-only JWT; sigv4 chunk verification missing; Bearer prefix case-sensitivity |
| **T**ampering | **Critical** | SigV4 streaming chunk signatures not verified; webhook HMAC is good but missing on payload; SSE envelope encryption is strong |
| **R**epudiation | **Low** | Admin actions audited; regular CRUD only generates events (not audit trail); operator could deny after the fact |
| **I**nformation Disclosure | **Medium** | Error messages leak internals; missing security headers; webhook payloads in clear; ListKeys leaks metadata; no X-Content-Type-Options |
| **D**enial of Service | **Medium** | Rate limiter bucket map exhaustion (50K limit); no max body size on List operations; idempotency spool uses disk temp files |
| **E**levation of Privilege | **High** | MCP endpoint lacks scope enforcement; bucket policy parse failure falls open; no tenant/bucket input validation enables path traversal |

---

## Compliance Gap Analysis

### OWASP Top 10 (2021)

| A# | Category | Status |
|----|----------|--------|
| A01 | Broken Access Control | ⚠️ **Partial** — MCP scope bypass, policy-fall-open |
| A02 | Cryptographic Failures | ⚠️ **Partial** — SHA-256 for keys, HS256-only JWT |
| A03 | Injection | ✅ **Good** — parameterized SQL everywhere |
| A04 | Insecure Design | ⚠️ **Needs work** — SigV4 chunk verification missing |
| A05 | Security Misconfiguration | ⚠️ **Partial** — missing security headers, CORS defaults |
| A06 | Vulnerable Components | ✅ **Not reviewed** (Go modules) |
| A07 | Identification/Auth Failures | ⚠️ **Needs work** — SHA-256 key hashing |
| A08 | Data Integrity Failures | ❌ **Critical** — SigV4 chunk tampering |
| A09 | Logging/Monitoring | ✅ **Good** — structured JSON logging, OTel metrics, audit |
| A10 | SSRF | ✅ **Good** — webhook URLs configurable, no user-controlled fetch |

### GDPR / SOC2 / PCI Considerations

- **PII Detection**: Built-in `PIIDetector` with email, phone, SSN, credit card patterns is good, but the regex-based detection is noisy and the redaction is best-effort only.
- **Audit Trail**: Admin actions are audited; regular object access via events (not persisted audit). SOC2 requires tamper-proof audit logs for all data access.
- **Data at Rest**: AES-256-GCM envelope encryption is strong. Key rotation via re-wrapping is supported.
- **Data in Transit**: No TLS enforcement at the application layer (delegated to infra/reverse proxy).

---

## Final Summary

| Metric | Rating |
|--------|--------|
| **Overall Security Posture** | ⚠️ **Needs Improvement** |
| **Critical Issues** | 2 |
| **High Issues** | 4 |
| **Medium Issues** | 5 |
| **Low Issues** | 4 |
| **Total** | **15 findings** |

### 🔴 Top 3 Critical Issues

1. **SigV4 Streaming Chunk Verification Missing** (`internal/auth/sigv4_chunk.go`) — MitM can silently tamper with streaming upload bodies. Fix: implement per-chunk HMAC verification.
2. **API Key Hashes Use Plain SHA-256** (`internal/auth/store.go`) — If DB is breached, all persisted keys can be brute-forced. Fix: switch to argon2id.
3. **MCP Endpoint Lacks Scope Enforcement** (`internal/mcp/server.go`) — Read-only users can write/delete objects via MCP tools. Fix: add scope checks to each tool dispatch.

### 🟢 Top 3 Quick Wins (High Impact, Low Effort)

1. **MCP scope enforcement** — Add `auth.FromContext(ctx)` scope checks to `callTool()` dispatch. ~4 hours.
2. **Bucket policy parse-fail-close** — Change `checkBucketPolicy` to deny on parse error. ~2 hours.
3. **Security headers middleware** — Add `X-Content-Type-Options`, `X-Frame-Options`, `CSP` to the middleware chain. ~2 hours.

### 📊 Security Debt

- **SHA-256 token hashing** requires a migration path (dual-hash support during transition).
- **No asymmetric JWT** means no key rotation without breaking all existing tokens.
- **Missing security headers** is a quick fix but creating a tracking item for all headers including CSP is important.
- **Error message sanitization** — many error paths need audit for information disclosure.
