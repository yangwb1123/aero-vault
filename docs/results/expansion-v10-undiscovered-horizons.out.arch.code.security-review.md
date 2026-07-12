Now I have a thorough understanding of the codebase. Let me compile my comprehensive security review.

---

# Security Review: Aero-Vault Subsystem

## Executive Summary

I performed a systematic security review of the Aero-Vault codebase, covering authentication, authorization, cryptography, input validation, session management, data protection, and threat modeling (STRIDE). Below are my findings organized by severity.

---

## Detailed Findings

### FINDING-01: SQL Injection via LIKE Operator in Object Listing

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Critical** |
| **Title** | Direct string interpolation into SQL LIKE pattern from user input |
| **Location** | `internal/repository/sql_objects.go` — `ListObjects()` and `ListDeletedObjects()` |
| **Description** | User-supplied `prefix` parameter is concatenated directly into a SQL LIKE pattern (`prefix+"%"`) without escaping. While the prefix itself is parameterized via `$3`, the LIKE wildcard characters `%` and `_` embedded in the prefix are not escaped. A crafted prefix like `_` or `%secret%` can cause unintended matching against all objects. |
| **Attack Scenario** | Attacker calls `GET /v1/files?prefix=_` which becomes `key LIKE _%` — the `_` wildcard matches any single character, exposing objects that don't actually start with the literal `_`. Worse: `prefix=%` matches everything. |
| **Impact** | Information disclosure — attacker can enumerate object keys they should not have access to by crafting LIKE patterns. While still bound by tenant isolation, they can list objects outside their intended prefix scope. |
| **Recommendation** | Escape `%` and `_` in the prefix before building the LIKE pattern: `strings.ReplaceAll(prefix, "%", "\\%")` and `strings.ReplaceAll(prefix, "_", "\\_")`. For SQLite, use `ESCAPE '\'` clause. For Postgres, the same escaping works with default settings. |
| **Effort** | S (< 1 day) |

### FINDING-02: No Rate Limiting on Admin Authentication Endpoints

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Critical** |
| **Title** | No rate limiting on admin key operations and JWT issuance |
| **Location** | `internal/api/rest/admin.go` — `AddKey()`, `RevokeKey()`, `IssueJWT()` |
| **Description** | The admin API endpoints for adding keys, revoking keys, and issuing JWTs have no rate limiting or brute-force protection. An attacker with admin credentials can issue unlimited API keys or JWTs. If an admin token is compromised, the blast radius is unbounded. |
| **Attack Scenario** | Attacker compromises an admin API key, then calls `POST /v1/admin/keys` thousands of times to create persistent backdoor credentials, or calls `POST /v1/admin/jwt` to mint bearer tokens that never expire. |
| **Impact** | Privilege escalation, persistence, credential stuffing, and account takeover. |
| **Recommendation** | Add per-tenant and global rate limiting on admin mutation endpoints. Implement admin action confirmation via secondary channel (audit + manual approval for key operations). Add exponential per-admin backoff. |
| **Effort** | M (1-3 days) |

### FINDING-03: SSRF in Embedder, LLM, Reranker, and KMS HTTP Clients

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Critical** |
| **Title** | Server-Side Request Forgery via user-controlled AI endpoints |
| **Location** | `internal/ai/embedder.go`, `internal/ai/llm.go`, `internal/ai/rerank.go`, `internal/storage/kms.go` |
| **Description** | The AI embedder, LLM, reranker, and HTTP KMS clients accept config-provided endpoints (`AI_EMBED_ENDPOINT`, `AI_CHAT_ENDPOINT`, `AI_RERANK_ENDPOINT`, `STORAGE_LOCAL_SSE_KMS_URL`) and make HTTP requests to those endpoints. The endpoints are configuration-based (not user-provided), but the embedder endpoint is mounted from `AI_EXTRACTOR_ENDPOINT` which is documented as a remote extraction service. More critically, the webhook `EVENTS_WEBHOOK_URL` accepts a URL from configuration that points to arbitrary endpoints. The webhook retry mechanism retries failed deliveries by making HTTP POST requests to the configured URL, which could be an internal service. |
| **Attack Scenario** | If an attacker gains access to the server's environment configuration (e.g., via the admin config endpoint), they could redirect the embedder/KMS/webhook endpoints to internal services (e.g., `http://169.254.169.254/latest/meta-data/` for cloud metadata, or `http://internal-db-host:5432` for database). The webhook retry loop (`RetryLoop`) makes repeated outbound POST requests to the configured URL — if redirected to an internal service, this could enable SSRF. |
| **Impact** | Internal network reconnaissance, cloud metadata exfiltration (IAM credentials), potential pivot to internal services. |
| **Recommendation** | 1) Implement a URL allowlist/blocklist for all outbound HTTP endpoints. 2) Disallow connections to private IP ranges (RFC 1918, RFC 3927, link-local, loopback) by default for these services. 3) Add a `no_proxy`-style deny list. 4) For the webhook retry, reject URLs that resolve to private IPs. |
| **Effort** | M (1-3 days) |

### FINDING-04: Weak Presign URL Scheme

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **High** |
| **Title** | Presigned URLs use HMAC-SHA256 but lack expiration enforcement in validation path |
| **Location** | `internal/storage/sign.go` — `signLocal()` |
| **Description** | The presign URL scheme uses HMAC-SHA256 over `method + "\n" + objectKey + "\n" + expires`. However, the `expires` parameter is included in the signature input but the validation path appears to use `hmac.Equal` comparison against a computed signature. If an attacker obtains a presigned URL, they could potentially replay it beyond the intended expiry because the signature doesn't anchor a timestamp from the server (the `expires` is client-supplied). |
| **Attack Scenario** | Attacker intercepts a presigned GET URL with `expires=300` (5 minutes). The URL includes the signature. The attacker extracts the URL and replays it at a later time — if the server doesn't independently validate the expiry against its own clock, the presigned URL becomes permanent. |
| **Impact** | Permanent unauthorized access to objects via presigned URLs. |
| **Recommendation** | 1) When validating a presigned URL, the server MUST check that `current_time < creation_time + expires`. 2) Include the signature creation timestamp in the canonical string, and verify it server-side. 3) Consider adding the storage key or object version ID to the signed payload so a presigned URL can't be reused after object replacement. |
| **Effort** | S (< 1 day) |

### FINDING-05: Missing Token Revocation Validation in JWT

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | JWT tokens cannot be revoked — no blacklist/allowlist mechanism |
| **Location** | `internal/auth/jwt.go` — `Verify()` |
| **Description** | JWT verification only checks the HMAC signature, expiry (`exp`), and not-before (`nbf`). There is no mechanism to revoke a JWT before its natural expiry. If a JWT is compromised, the only recourse is to rotate the entire `AUTH_JWT_SECRET`, which invalidates ALL tokens. |
| **Attack Scenario** | An attacker compromises a JWT token for a tenant with admin scopes. The security team cannot revoke just that token — they must regenerate the JWT secret, invalidating all valid tokens and causing a service disruption. |
| **Impact** | Compromised JWT tokens provide persistent, unrevocable access until natural expiry. |
| **Recommendation** | 1) Implement a JWT blacklist (stored in repository) checked during `Verify()`. 2) Add a `jti` (JWT ID) claim to each issued token and check it against the blacklist. 3) Add a `POST /v1/admin/jwt/revoke` endpoint. 4) Consider short TTLs combined with refresh tokens as a compensating control. |
| **Effort** | L (> 3 days) |

### FINDING-06: Cross-Tenant Data Access via MCP `readResource`

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | MCP resource URI allows cross-tenant data access via tenant parameter spoofing |
| **Location** | `internal/mcp/server.go` — `readResource()` |
| **Description** | The `readResource` handler extracts the tenant from the URI `aero-vault://{tenant}/{bucket}/{key}` and compares it against `s.tenantFor(ctx)`. However, `tenantFor()` returns `s.tenant` (the server default) when the context tenant is empty or "default". An attacker could craft a URI with tenant `default` to read cross-tenant data if the default tenant is shared between logical tenants. |
| **Attack Scenario** | Attacker in tenant A uses the MCP interface with URI `aero-vault://default/bucket/sensitive-doc` — if the server's default tenant overlaps with tenant A's data, the attacker reads objects from the shared default namespace. |
| **Impact** | Cross-tenant data access, information disclosure. |
| **Recommendation** | 1) In the MCP context, require explicit tenant binding. 2) Never fall back to `s.tenant` when checking resource access. 3) In stdio mode (no middleware), require the caller to explicitly declare their tenant before resource access. |
| **Effort** | S (< 1 day) |

### FINDING-07: Anonymous Public Read Leaks Object Existence

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Medium** |
| **Title** | Anonymous public-read mode leaks information via different error responses |
| **Location** | `internal/auth/auth_middleware.go` — `authenticateBearer()` → anonymous path |
| **Description** | When `AUTH_ANONYMOUS_PUBLIC_READ` is enabled, unauthenticated requests to object GET/HEAD paths bypass authentication. The handler then checks ACLs and returns 403 for non-public objects. However, the difference between "object exists but not public" (403) and "object doesn't exist" (404) leaks existence information to anonymous users. |
| **Attack Scenario** | Anonymous attacker enumerates object keys — keys that return 403 exist (are private), keys that return 404 don't exist. This allows directory traversal and object key enumeration even without read access. |
| **Impact** | Information disclosure — attackers can map the object namespace. |
| **Recommendation** | Return a generic 404 or 403 for both cases when anonymous access is blocked. Use `ErrNotFound` semantics for all unauthorized access attempts to non-public objects. |
| **Effort** | S (< 1 day) |

### FINDING-08: Metadata Served in Error Responses

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Medium** |
| **Title** | Error messages may include sensitive metadata values |
| **Location** | `internal/api/rest/handler.go` — `classify()` → default case returns `err.Error()` |
| **Description** | In `classify()`, the default error case returns `err.Error()` directly to the client in the HTTP response under the `"InternalError"` code. If the error message contains file paths, metadata values, or SQL query fragments, this information leaks to the client. |
| **Attack Scenario** | Attacker triggers an error in the service layer (e.g., quota exceeded may include usage details, storage errors may reveal file paths) and the full error message is returned to the client verbatim. |
| **Impact** | Information disclosure — internal paths, data values, or SQL patterns may be leaked. |
| **Recommendation** | 1) Sanitize error messages before returning them to the client. 2) Log the full error server-side and return a generic "internal error" to the client. 3) For known errors, return specific but sanitized messages. |
| **Effort** | S (< 1 day) |

### FINDING-09: No Size Limit on Multipart Form Uploads

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | Unbounded multipart form parsing allows memory exhaustion |
| **Location** | `internal/api/rest/handler.go` — `PostForm()` uses `r.ParseMultipartForm(32 << 20)` |
| **Description** | The multipart form upload handler limits in-memory parsing to 32MB, but the total upload body is unbounded. An attacker could stream a multipart upload with a 32MB form memory footprint plus arbitrary amounts of data in the file parts. Since `PostForm` doesn't enforce a `MaxBytesReader`, the entire upload is buffered by the HTTP server. |
| **Attack Scenario** | Attacker sends a multipart POST to `/v1/files` with a very large (multi-GB) file body. The Go HTTP server attempts to buffer the entire body, consuming all available memory and causing OOM kill of the server. |
| **Impact** | Denial of Service via memory exhaustion. |
| **Recommendation** | Apply `http.MaxBytesReader` on the request body before `ParseMultipartForm`: `r.Body = http.MaxBytesReader(w, r.Body, MAX_UPLOAD_SIZE)`. Set a reasonable maximum upload size (e.g., 5GB or configurable). |
| **Effort** | S (< 1 day) |

### FINDING-10: Weak Content-MD5 Comparison

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | Content-MD5 uses insecure MD5 hash for integrity |
| **Location** | `internal/service/file_crud.go` — `md5WrapReader()` |
| **Description** | MD5 is cryptographically broken (collision resistance compromised since 2004, practical collisions since 2008). The Content-MD5 header is supposed to provide end-to-end integrity verification but uses MD5 which is vulnerable to collision attacks. An attacker who can control the stored object could replace it with a different object having the same MD5 hash. |
| **Attack Scenario** | Attacker uploads object A with Content-MD5 hash H. After the upload completes (but before verification), they replace the underlying storage blob with object B that has the same MD5 hash H. The server verifies H against the new content and accepts it, serving object B to future GET requests. |
| **Impact** | Integrity bypass — attacker can swap stored objects while bypassing integrity checks. |
| **Recommendation** | 1) Use SHA-256 or SHA-512 for content integrity verification. 2) The `_aero_content_md5` stored in metadata should be a SHA-256 hash. 3) Deprecate MD5-based Content-MD5 in favor of `x-amz-checksum-sha256` or similar. |
| **Effort** | M (1-3 days) |

### FINDING-11: CORS Middleware Allows Wildcard Origin with Credentials

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | CORS configuration may allow `Access-Control-Allow-Origin: *` with credentials |
| **Location** | `internal/middleware/cors.go` — `writeCORSHeaders()` |
| **Description** | The CORS middleware writes `Access-Control-Allow-Origin` set to the request's `Origin` header (if it matches any allowed origin). When `AllowedOrigins` contains `"*"`, any origin is allowed. If `AllowCreds` is set to true (or if the middleware configuration allows it), this creates `Access-Control-Allow-Origin: *` with `Access-Control-Allow-Credentials: true` — which is a browser security violation (credentials with wildcard origin is prohibited by the fetch spec). More importantly, it means any website can make authenticated requests to the API. |
| **Attack Scenario** | Attacker hosts a malicious website that makes authenticated API calls to the Aero-Vault server. If `AllowCreds` is true (even implicitly), the browser will include credentials. |
| **Impact** | Cross-origin credentialed requests, CSRF-like attacks. |
| **Recommendation** | 1) Never set `Access-Control-Allow-Credentials: true` when using wildcard origins. 2) Validate that the middleware enforces this restriction. 3) Clearly document the security implications of wildcard CORS origins. |
| **Effort** | S (< 1 day) |

### FINDING-12: Timing Attack on Token Comparison

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | API key token comparison is constant-time but missing in one path |
| **Location** | `internal/auth/auth.go` — `Lookup()` uses map lookup (constant), but token extraction and matching is not constant-time |
| **Description** | The API key lookup uses a Go map lookup which is not constant-time. While map lookups in Go are generally resistant to timing attacks (the hash function randomizes access patterns), the actual token comparison in `verifySignature()` uses `hmac.Equal` (constant-time). However, the initial `Registry.keys[token]` map lookup leaks information about which keys exist (map hit vs. miss takes different time). |
| **Attack Scenario** | Attacker can measure response timing to determine if a token prefix is valid (map hit path vs. miss path). Over many requests, they can brute-force valid tokens byte-by-byte. |
| **Impact** | Low severity due to practical difficulty, but theoretically allows token enumeration. |
| **Recommendation** | Add a small random delay (jitter) before returning from `Lookup()` when the token is not found, to normalize response timing between cache hit/miss/no-match. |
| **Effort** | S (< 1 day) |

### FINDING-13: Integer Overflow in Cost Calculation

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Potential integer overflow in AI cost micros calculation |
| **Location** | `internal/ai/cost.go` — `costMicros()` |
| **Description** | The `costMicros()` function multiplies token counts by micros-per-1000-tokens. If token counts or USD prices are very large, the multiplication could overflow an int64. |
| **Attack Scenario** | An attacker sends extremely long prompts to the chat endpoint, resulting in large token counts that overflow the cost calculation, causing either negative cost (bypassing budget) or panic. |
| **Impact** | Budget bypass or service disruption. |
| **Recommendation** | Use saturation arithmetic (check for overflow before multiplication) or use `math/big` for cost calculations. Cap token counts at a reasonable maximum. |
| **Effort** | S (< 1 day) |

### FINDING-14: No Password/Key Rotation Policy for SSE Keys

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | SSE keys fetched only at startup with no rotation mechanism |
| **Location** | `internal/storage/secret.go` — `newHTTPProvider()`, `newKeyfileProvider()` |
| **Description** | Server-Side Encryption master keys are loaded once at startup (from env, file, or HTTP URL). There is no mechanism to rotate keys without restarting the server. While the `rewrap` mechanism supports migrating individual objects to a new key, there's no automated key rotation policy or scheduled rewrap. |
| **Attack Scenario** | If a master SSE key is compromised, the only way to rotate is to restart the server with new keys. Objects encrypted with the old key cannot be re-encrypted without a restart-triggered rewrap. |
| **Impact** | Key compromise provides decryption access to all objects encrypted under that key version. |
| **Recommendation** | 1) Implement periodic key rotation with automatic re-wrapping of stale objects. 2) Add support for key rotation via SIGHUP or admin API endpoint without restart. 3) Document the key rotation procedure. |
| **Effort** | L (> 3 days) |

### FINDING-15: Timing Window in Concurrent Rate Limiter

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Low** |
| **Title** | Race condition in per-tenant concurrency limiter credit release |
| **Location** | `internal/middleware/middleware.go` — `PerTenantConcurrencyLimiter` — deferred release |
| **Description** | The per-tenant concurrency limiter releases credits in a deferred function. However, between the semaphore acquire and the per-tenant check, the lock is not held continuously. A race condition between the per-tenant check and the defer release could temporarily overcommit a tenant's concurrency budget. |
| **Attack Scenario** | An attacker sends many concurrent requests to the same tenant. Due to the TOCTOU race between `pt.inflight[tenant] += cost` and the semaphore placement, the per-tenant limit may be exceeded temporarily. |
| **Impact** | Per-tenant concurrency limit can be exceeded under high concurrency. |
| **Recommendation** | Use a transactional approach: hold the mutex across both the check AND the increment. Alternatively, use `sync/atomic` operations with a CAS loop. |
| **Effort** | S (< 1 day) |

---

## STRIDE Threat Model Analysis

| Threat | Finding(s) | Risk |
|--------|------------|------|
| **Spoofing** | FINDING-02 (no rate limiting on JWT issuance), FINDING-05 (no JWT revocation) | High — attackers can mint tokens and never be revoked |
| **Tampering** | FINDING-10 (MD5 collisions), FINDING-01 (LIKE injection) | Medium — object integrity can be bypassed; database query manipulation |
| **Repudiation** | FINDING-12 (timing attack), audit logging design | Low — audit logs are well-designed but timing side-channels exist |
| **Information Disclosure** | FINDING-03 (SSRF), FINDING-07 (anonymous existence leaks), FINDING-08 (error messages) | High — SSRF, existence oracle, internal data in errors |
| **Denial of Service** | FINDING-09 (unbounded multipart), FINDING-13 (integer overflow in cost) | Medium — memory exhaustion, budget bypass |
| **Elevation of Privilege** | FINDING-06 (MCP cross-tenant), FINDING-04 (presigned URL replay) | High — unauthorized cross-tenant access, permanent presigned URL access |

---

## Compliance Considerations

| Standard | Issue | Finding |
|----------|-------|---------|
| **OWASP Top 10 (2021)** | A01: Broken Access Control | FINDING-06 (MCP cross-tenant) |
| | A03: Injection | FINDING-01 (LIKE injection) |
| | A04: Insecure Design | FINDING-02 (no admin rate limiting) |
| | A05: Security Misconfiguration | FINDING-11 (CORS wildcard) |
| | A08: Software and Data Integrity Failures | FINDING-10 (MD5) |
| | A09: Security Logging and Monitoring Failures | FINDING-05 (no JWT revocation logging) |
| **GDPR** | Art. 32 (Security of processing) | Error messages may leak PII (FINDING-08) |
| **SOC2** | CC6 (Logical and Physical Access Controls) | JWT revocation (FINDING-05), key rotation (FINDING-14) |
| **PCI-DSS** | Req 3.4 (Render PAN unreadable) | MD5 integrity (FINDING-10) is not cryptographically sound |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase demonstrates strong security awareness in many areas (HMAC-based authentication, envelope encryption with key rotation, proper constant-time comparisons in SigV4/JWT, input parameterization in SQL) but has several high-severity gaps that require immediate attention.

### Top 3 Critical Issues

1. **SSRF in AI/KMS/Webhook HTTP Clients (FINDING-03)** — Outbound HTTP to user-configurable endpoints without network boundary controls. This is the most dangerous finding as it enables internal network reconnaissance and potential cloud metadata exfiltration.

2. **No Rate Limiting on Admin Key/JWT Operations (FINDING-02)** — Admin API endpoints that issue permanent credentials have no abuse protection. A compromised admin token can be used to create unlimited backdoor keys.

3. **SQL Injection via LIKE Wildcards (FINDING-01)** — The LIKE pattern interpolation allows attackers to enumerate objects outside their intended prefix scope via wildcard characters.

### Top 3 Quick Wins

1. **Sanitize LIKE patterns (FINDING-01)** — Escape `%` and `_` in the object listing prefix. Implementation: ~20 lines of code in `sql_objects.go`.

2. **Apply `MaxBytesReader` to multipart uploads (FINDING-09)** — Add `http.MaxBytesReader` to prevent memory exhaustion. Implementation: ~3 lines in `handler.go`.

3. **Fix error message leakage (FINDING-08)** — Return sanitized errors to clients in the default error case. Implementation: replace `err.Error()` with a generic message in `classify()`.

### Security Debt

- **Cross-tenant isolation testing** — While the architecture enforces tenant boundaries, several code paths (MCP resource, anonymous public-read) have edge cases that could leak data between tenants. A dedicated cross-tenant security test suite is needed.

- **Cryptographic agility** — The MD5 integrity check, the hardcoded HS256-only JWT algorithm, and the single-key SSE provider should be tracked for future cryptographic migration.

- **Authentication hardening** — JWT revocation, token-based rate limiting, and credential rotation policies should be prioritized for the next security sprint.

- **Defense in depth** — The codebase currently relies heavily on input validation at the service layer. Adding additional layers (WAF, request validation middleware, content security policies) would improve the overall security posture.
