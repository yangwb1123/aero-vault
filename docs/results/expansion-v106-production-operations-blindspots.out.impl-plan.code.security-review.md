Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# Security Review: Aero-Vault Object Storage Service

## Overall Assessment: Needs Improvement

This is a well-structured Go codebase with thoughtful security patterns in several areas (HMAC presigning, JWT verification, SSE envelope encryption, rate limiting). However, I've identified **Critical** and **High** severity issues that must be addressed before production deployment.

---

## Findings

### Finding 1: Information Disclosure — Error Messages Leak Internal Details

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Threat Model |
| **Severity** | **High** |
| **Title** | Error handler returns raw error messages to clients |
| **Location** | `internal/api/rest/handler.go:126` — `classify()` function |
| **Description** | The `classify()` function's default case returns `err.Error()` verbatim to the client as the error message in JSON responses. This leaks internal implementation details such as file paths, SQL errors, storage errors, and internal state. |
| **Attack Scenario** | An attacker sends malformed requests to trigger backend errors. The response reveals "storage put: unexpected EOF", "repo write: UNIQUE constraint failed", or "kms /wrap http 500: timeout". Each leak reduces the effort needed to craft more precise attacks. |
| **Impact** | Lowers barrier for further exploitation; aids reconnaissance. In cloud environments, can reveal infrastructure details (e.g., KMS endpoint URLs in error chains). |
| **Recommendation** | Map all known errors to user-safe messages. For the default case, log the full error server-side and return a generic "internal error" to the client. |
| **Effort** | S |

**Example fix:**
```go
// In classify() default case:
default:
    return "InternalError", "internal error", http.StatusInternalServerError
// And log the real error server-side in writeError:
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
    h.logger.Error("request error", "error", err, "path", r.URL.Path)
    code, message, status := classify(err)
    writeJSON(w, status, errorBody{Error: errorPayload{
        Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
    }})
}
```

---

### Finding 2: Authorization — MCP HTTP/stdio Transport Bypasses Auth

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | MCP tools can be called without authentication over stdio and HTTP |
| **Location** | `internal/mcp/transport.go` — `HTTPHandler()`, `ServeStdio()`; `cmd/server/main.go` (presumed wiring) |
| **Description** | The MCP `HTTPHandler` and `ServeStdio` functions execute arbitrary FileService operations (`read_file`, `write_file`, `delete_file`, `search`, `chat`) without requiring any authentication or authorization. The tenant defaults to `"default"`. The HTTP handler at `/mcp` is not wrapped in the auth middleware chain. The MCP `write_file` and `delete_file` tools provide direct write/delete access. |
| **Attack Scenario** | Any network actor reaching the `/mcp` endpoint can read, write, or delete any object without credentials. Combined with MCP's stdio mode, a local privilege escalation from an unprivileged process that can connect to the MCP socket gains full storage access. |
| **Impact** | Complete loss of confidentiality, integrity, and availability of all stored objects via unauthenticated MCP access. |
| **Recommendation** | Wire the MCP HTTP handler through the auth middleware chain. For the stdio path, require the parent process to supply credentials or authenticate via environment. At minimum, document this as a critical security limitation. |
| **Effort** | M (1-2 days) |

---

### Finding 3: Authorization — Admin Routes Not Protected by Path-Based Scope Checks

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | Admin endpoints rely on per-handler `requireAdmin()` instead of middleware-scoped routing |
| **Location** | `internal/api/rest/router.go` lines 70-78 — admin routes registered without admin scope middleware |
| **Description** | All `/v1/admin/*` routes are registered directly in `NewRouter()` without wrapping them in a middleware that enforces `ScopeAdmin`. Instead, each handler must manually call `h.requireAdmin()` which is inconsistent — some admin handlers may be added without the check. Furthermore, the `Usage` endpoint at `GET /v1/usage` is NOT gated by admin scope in the router but is accessible to any authenticated user. |
| **Attack Scenario** | A key with only `read+write` scope (no `admin` scope) could potentially access admin functionality if a handler accidentally lacks the `requireAdmin()` call, due to the missing middleware-level enforcement. |
| **Impact** | Privilege escalation — tenant users could gain administrative access. |
| **Recommendation** | Group admin routes under a sub-router with `reg.Require(auth.ScopeAdmin)` middleware. Remove per-handler `requireAdmin()` calls. |
| **Effort** | S |

**Example fix:**
```go
r.Group(func(r chi.Router) {
    r.Use(reg.Require(auth.ScopeAdmin))
    r.Put("/admin/tenants/{tenant}/quota", adm.SetQuota)
    r.Put("/admin/tenants/{tenant}/budget", adm.SetBudget)
    // ... all admin routes
})
```

---

### Finding 4: Authorization — SigV4 Presigned URLs Use Local HMAC with Static Key

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Authorization |
| **Severity** | **High** |
| **Title** | Local presign scheme uses HMAC key directly without key rotation or expiration tracking |
| **Location** | `internal/storage/sign.go` — `signLocal()`; `internal/storage/local.go` — `SignKey` field |
| **Description** | The `SignKey` in `LocalConfig` is used to sign presigned URLs via HMAC-SHA256 over `method\nobjectKey\nexpires`. There's no key rotation, no key derivation per-tenant or per-bucket, and the key is likely derived from a single environment variable. If the key is compromised, all existing presigned URLs become forgeable. Additionally, the `expires` field is just a number embedded in the URL — an attacker who captures a presigned URL can extend its lifetime unless the expiry is validated against server time. |
| **Attack Scenario** | An attacker who obtains a valid presigned URL could modify the `expires` parameter to extend its validity indefinitely, because the signature only covers the original expiry value and the server uses current wall-clock time to check it. |
| **Impact** | Time-limited access tokens can be converted into permanent access. If the signing key leaks, all URLs can be forged. |
| **Recommendation** | (1) Validate that the expiry time embedded in the signature matches the current request time. (2) Add key derivation: `HMAC(signKey, tenant + bucket)` so each tenant/bucket pair has a unique derived key. (3) Document that the signing key must be rotated regularly. |
| **Effort** | M |

---

### Finding 5: Authorization — `ScopeAdmin` Grants Implicit `Read+Write`

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | ScopeAdmin has implicit full access to all read/write operations |
| **Location** | `internal/auth/auth.go:47-49` — `func (k Key) Has(s Scope) bool { if k.Scopes[ScopeAdmin] { return true } ... }` |
| **Description** | The `Has` method returns `true` for any scope check when the key has `ScopeAdmin`. This means an admin key automatically passes all `ScopeRead` and `ScopeWrite` checks throughout the system. While this is the intended design, it means there's no way to have a "read-only admin" or "audit-only admin" — any key with admin scope can also read and write all objects. |
| **Attack Scenario** | If a developer intends to issue a "read-only admin key for audits" with scopes `admin+read`, it still grants full write access because `Has(ScopeWrite)` returns `true` for admin keys. |
| **Impact** | Inability to create least-privilege admin keys. |
| **Recommendation** | Separate the ScopeAdmin from implicit read+write. Change `Has` to not short-circuit for admin on non-admin scopes, or introduce a separate `Scope` enum for super-admin that the router enforces explicitly. |
| **Effort** | S |

---

### Finding 6: Input Validation — SQL Injection via User-Controlled `Key LIKE` Pattern

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | SQL `LIKE` pattern constructed by concatenating user prefix with `%` |
| **Location** | `internal/repository/sql_objects.go:82` — `prefix+"%"` in `ListObjects` |
| **Description** | The `prefix` parameter from the HTTP query is concatenated directly with `%` to form a SQL `LIKE` pattern. While the prefix is passed as a bound parameter (safe from classic injection), `LIKE` patterns have special characters: if a user passes a prefix containing `%` or `_`, it becomes a wildcard in the SQL LIKE operation, potentially exposing keys the user shouldn't see through pattern matching abuse. |
| **Attack Scenario** | An attacker sends `prefix=_` to list all single-character keys (or keys where the first character is a specific character). Or sends `prefix=%secret` to bypass a prefix filter and enumerate all keys ending with "secret". |
| **Impact** | Information disclosure through LIKE pattern manipulation; bucket/key enumeration beyond intended scope. |
| **Recommendation** | Escape `%` and `_` characters in the prefix before constructing the LIKE pattern, or switch to indexed prefix matching using `>=` and `<` with `COLLATE` to avoid this class of issues entirely. |
| **Effort** | S |

**Example fix:**
```go
// Escape LIKE wildcards
escapedPrefix := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(prefix)
// Then use LIKE with ESCAPE clause
// `... AND key LIKE $3 ESCAPE '\' ...`
```

---

### Finding 7: Input Validation — No Validation on Batch Delete/Tag Keys

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Batch operations accept arbitrary key lists without validation |
| **Location** | `internal/api/rest/handler.go:328-356` — `BatchDelete` and `BatchTag` handlers |
| **Description** | The `BatchDelete` and `BatchTag` handlers accept a `[]string` of keys from the JSON body. These keys are passed directly to `service.BatchDelete`/`service.BatchSetTags` without any validation. While `service.Delete` calls `validateKey` internally for individual deletes through `Put`, the batch path for `Delete` does go through `validateKey` indirectly. However, batch tag (`SetTags`) does NOT validate keys, and neither does `BatchDelete` for each key. |
| **Attack Scenario** | An attacker sends a `BatchDelete` request with keys containing `..`, `/` prefix, or null characters to attempt path traversal or cause unexpected behavior. |
| **Impact** | Potential for storage backend path traversal or data corruption via specially crafted keys. |
| **Recommendation** | Validate each key in batch operations using the same `validateKey()` function used in `Put`. |
| **Effort** | S |

---

### Finding 8: Authorization — Webhook Event Payload Contains Sensitive Object Data

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Webhook event payloads include object metadata like ETag, size, content_type |
| **Location** | `internal/service/file.go:145-153` — `emit()` constructs event payload |
| **Description** | Every object event (created/deleted/accessed) is broadcast to all configured webhooks with the full payload including backend type, ETag, size, and content type. If webhook URLs are configured over plain HTTP (not HTTPS), this metadata is sent in cleartext. Additionally, there's no per-event-type filtering — events are sent for all operations (even reads) which could create a large amount of outbound traffic. |
| **Attack Scenario** | A misconfigured HTTP webhook URL leaks object metadata over the network in cleartext. The ETag leakage helps attackers track specific versions of objects. |
| **Impact** | Information leakage of object metadata. |
| **Recommendation** | (1) Warn/log when webhook URLs don't use HTTPS. (2) Allow per-event-type filtering for webhook delivery. (3) Consider whether `EventAccessed` events should be webhook-dispatched by default (they can be very high volume). |
| **Effort** | M |

---

### Finding 9: Cryptography — AES-GCM with `nil` Additional Data

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | GCM encryption uses nil Additional Data (authData) in object encryption |
| **Location** | `internal/storage/encrypt.go:68` — `gcm.Seal(nil, nonce, plaintext, nil)` |
| **Description** | In the `gcmSeal` function, the Additional Authenticated Data (AAD) parameter to `gcm.Seal` is `nil`. This means there is no binding between the ciphertext and its associated metadata (key ID, bucket, etc.). An attacker who can modify the storage layer could swap the ciphertext of one object with another without detection. |
| **Attack Scenario** | An attacker with storage-level access replaces the encrypted content of object A with the encrypted content of object B. The decryption succeeds (different plaintext), and integrity verification (GCM tag) passes. |
| **Impact** | Object substitution without detection when storage access is compromised. |
| **Recommendation** | Include the envelope JSON (or at minimum the key ID + storage key) as AAD: `gcm.Seal(nil, nonce, plaintext, envelopeJSON)`. The AAD is authenticated but not encrypted. |
| **Effort** | S |

---

### Finding 10: Session/Token — JWT `exp` Claim Enforcement Gap

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | JWT with no `exp` claim never expires |
| **Location** | `internal/auth/jwt.go:85` — `if c.Exp > 0 && now > c.Exp { return error }` |
| **Description** | The JWT verification only rejects tokens past their expiry if the `exp` claim is present and > 0. A forged or misconfigured token without an `exp` claim will pass verification and never expire. The `Sign()` method also only sets `exp` when `TTL > 0`. |
| **Attack Scenario** | An internal tool or admin script mints a JWT without setting TTL. This token never expires and becomes a permanent credential if leaked. Alternatively, an attacker who intercepts a token and removes its `exp` claim before using it... (however, modification breaks the signature, so the real attack is accidental — an admin issues a token without TTL and it never expires). |
| **Impact** | Potentially non-expiring tokens. |
| **Recommendation** | Enforce that `exp` must be present and must not be more than `MaxTokenAge` (e.g., 24h) in the future. Reject tokens without an `exp` claim. |
| **Effort** | S |

---

### Finding 11: Authorization — Access Log Does Not Redact Sensitive Headers

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Access logging captures API keys and tokens in plaintext |
| **Location** | `internal/middleware/middleware.go:92-101` — `AccessLog` logs `"path", r.URL.Path` |
| **Description** | The access log middleware logs the request path, but some operations embed API keys or tokens in the URL path (e.g., `DELETE /v1/admin/keys/{token}` in `admin.go:114`, or pre-signed URLs). The `RevokeKey` handler receives the token as a URL parameter. If access logs are collected centrally, these tokens would be logged in plaintext. |
| **Attack Scenario** | An admin revokes a key via `DELETE /v1/admin/keys/{token}`. The full token appears in access logs. A low-privilege developer with log access sees the token and uses it before the revocation propagates. |
| **Impact** | Credential leakage through logs. |
| **Recommendation** | (1) Redact sensitive URL path segments in access logging. (2) Use POST body for token values instead of URL parameters. (3) Ensure log pipelines treat all logs as potentially sensitive. |
| **Effort** | M |

---

### Finding 12: Authorization — Idempotency Key Can Be Used for Replay Attack on Any Tenant

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Spoofing) |
| **Severity** | **Medium** |
| **Title** | Idempotency keys scoped to tenant but not to authenticated identity |
| **Location** | `internal/api/rest/idempotency.go` — `ClaimIdempotencyKey` |
| **Description** | The idempotency key is scoped to `(tenant, key)` but does not incorporate the authenticated principal (API key identity). If two different users within the same tenant share the same idempotency key and target the same path, the second request's fingerprint check would be based on method+path, which might match. |
| **Attack Scenario** | User A initiates a PUT with `Idempotency-Key: foo`. The request claims the key. User B (same tenant, different key) also sends a request with `Idempotency-Key: foo` to the same path. Their request is rejected as a conflict, enabling User B to block User A's writes by pre-claiming idempotency keys. |
| **Impact** | Denial of service within a tenant. |
| **Recommendation** | Include the API key hash (or caller identity) in the idempotency key scope. |
| **Effort** | M |

---

### Finding 13: Cryptography — HMAC Comparison Not Time-Constant in Presigned URL Verification

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | `hmac.Equal` used correctly in most places, but presign verification path could leak timing |
| **Location** | `internal/storage/sign.go` — `hmacEqual()` wraps `hmac.Equal` (correct), but verify presign needs examination |
| **Description** | I found that `hmac.Equal` is used consistently for HMAC comparison, which is correct (constant-time). The SigV4 implementation also correctly uses `hmac.Equal`. No fix needed here — this is a positive finding confirming no timing attack vulnerability in HMAC comparison. |
| **Impact** | None (this is a good finding). |
| **Recommendation** | Continue using `hmac.Equal` for all MAC comparisons. |
| **Effort** | None |

---

### Finding 14: Authorization — Bucket Policy IP Conditions Can Be Bypassed via `X-Forwarded-For`

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | IP-based policy conditions use `RemoteAddr` which may be a reverse proxy IP |
| **Location** | `internal/api/rest/handler.go:37` and `internal/api/s3compat/handler.go:34` — `net.SplitHostPort(r.RemoteAddr)` |
| **Description** | Both the REST and S3 handlers extract the client IP from `r.RemoteAddr` for bucket policy IP conditions. When behind a reverse proxy (which is the standard deployment pattern), `RemoteAddr` is the proxy's IP, not the actual client. An attacker behind the proxy would bypass all IP-based restrictions in bucket policies. |
| **Attack Scenario** | A bucket policy specifies `"IpAddress": {"aws:SourceIp": ["10.0.0.0/8"]}` to restrict access to an internal network. The service is behind an nginx reverse proxy. `RemoteAddr` always shows the nginx IP (e.g., 127.0.0.1 or the proxy pod IP), so the IP check always passes, effectively disabling the restriction. |
| **Impact** | Complete bypass of IP-based access controls in bucket policies. |
| **Recommendation** | Check `X-Forwarded-For` header first, falling back to `RemoteAddr`. For security, validate that the immediate upstream (the last proxy in the chain) is trusted. |
| **Effort** | S |

---

### Finding 15: Rate Limiting — Tenant Bucket Bypass via Empty Tenant

| Field | Value |
|-------|-------|
| **Category** | Threat Model (DoS) |
| **Severity** | **Medium** |
| **Title** | Untenanted requests (empty tenant) are hashed to empty string bucket key |
| **Location** | `internal/middleware/ratelimit.go:82` — `if t == "" { t = "default" }` |
| **Description** | The rate limiter maps requests without a tenant header to "default" bucket. If an attacker sends requests without specifying a tenant, they compete with legitimate default-tenant traffic. Conversely, if no default tenant exists, an attacker can send requests with an extremely long or malicious tenant name, which still gets rate-limited via the per-bucket limit. |
| **Impact** | Risk of exhausting "default" rate limit capacity and starving legitimate requests. |
| **Recommendation** | Add a separate rate limit bucket for untented requests with stricter limits, so they don't compete with authenticated tenant traffic. |
| **Effort** | S |

---

### Finding 16: Authorization — MCP `resources/read` URI Parsing Allows Path Traversal

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | MCP resource URI parsing does not validate key path for `..` traversal |
| **Location** | `internal/mcp/server.go:248-274` — `readResource()` |
| **Description** | The MCP `readResource` handler parses a URI of the form `aero-vault://{tenant}/{bucket}/{key}` and passes the `parts[2]` directly as the key to `svc.Get`. While the tenant is validated against the authenticated tenant, the key is not validated for path traversal (`..`) or prefix `/` issues. |
| **Attack Scenario** | An attacker sends `"uri": "aero-vault://default/default/../../../etc/passwd"`. The `storageKey` function in `file.go` would join this to `default/default/../../../etc/passwd` which, after `path.Join`, could escape the intended storage path. Even though `objectPath` checks for `..`, the attack could still enumerate keys from other tenants if the bucket+tenant are guessed. |
| **Impact** | Potential path traversal to access objects in other tenants, or local file inclusion on the storage backend. |
| **Recommendation** | Apply `validateKey()` to the key portion of the URI before passing to `Get`. |
| **Effort** | S |

---

### Finding 17: Authorization — Tenant Isolation Not Enforced for Webhook Failures

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | `ListWebhookFailures` and `ListJobs` don't filter by tenant |
| **Location** | `/v1/admin/webhook-failures` and `/v1/admin/jobs` |
| **Description** | The admin endpoints for listing webhook failures and jobs return ALL records across all tenants. While these endpoints require admin scope, a read-only admin (if introduced per Finding 5) could list every tenant's webhook failures, which contain raw event payloads. |
| **Impact** | Cross-tenant information disclosure if granular admin permissions are added later. |
| **Recommendation** | Add tenant filtering to these list endpoints. For now, document this limitation. |
| **Effort** | S |

---

### Finding 18: Data Protection — Webhook Payloads Include Full Object Content in Event Payload

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Event payload contains full object key and bucket name which could be sensitive |
| **Location** | `internal/events/webhook.go:78-88` — Event serialization |
| **Description** | Webhook event payloads include `key` and `bucket` fields from the event payload. In a multi-tenant system, the object key might itself contain sensitive information (e.g., `reports/company-budget-2026.pdf`, `passports/...`). Webhook receivers may not have the same access control restrictions. |
| **Attack Scenario** | The webhook endpoint is a third-party service. Even without access to object content, the webhook receiver learns that a file named `passport-scan-1234.jpg` was uploaded, which is itself PII exposure. |
| **Impact** | Indirect information leakage through event metadata. |
| **Recommendation** | Document that webhook recipients receive event metadata including object keys. For sensitive deployments, encrypt or redact keys before dispatching to webhooks. |
| **Effort** | M |

---

### Finding 19: Threat Model — Object Lock/WORM Bypass via Direct Storage Access

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Medium** |
| **Title** | Retention locks are only enforced at the service layer — direct storage operations bypass them |
| **Location** | `internal/service/file_crud.go:257` — `hardDeleteObject` checks `LockedUntil`, but storage layer doesn't enforce |
| **Description** | The retention lock (`LockedUntil`) is only checked in the `FileService.hardDeleteObject` method. The underlying `Storage.Delete` has no awareness of retention locks. Any code path that calls `store.Delete` directly without going through `FileService.hardDeleteObject` can bypass WORM protection. This includes multiple reconciliation paths. |
| **Attack Scenario** | The retention GC (`reconcile/retention.go`) calls `store.Delete` directly after checking soft-delete time, but does not check retention locks. An attacker who soft-deletes an object could have it permanently purged by the GC even if it's under retention lock. |
| **Impact** | Bypass of compliance-mandated object retention/WORM protections. |
| **Recommendation** | (1) Check `LockedUntil` before purging in GC paths. (2) Implement retention enforcement at the storage layer as a secondary defense. |
| **Effort** | M |

---

### Finding 20: Input Validation — Content-Disposition Header Injection

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Info Disclosure) |
| **Severity** | **Medium** |
| **Title** | User-controlled `Content-Disposition` echoed back in GET responses without sanitization |
| **Location** | `internal/api/rest/handler.go:410` — `w.Header().Set("Content-Disposition", v)` via `_aero_content_disposition` |
| **Description** | The `Content-Disposition` header from a PUT request is stored in metadata and echoed back verbatim in GET/HEAD responses. A malicious uploader can inject arbitrary response headers via newline characters in the value, or set a `filename` that causes the browser to save a malicious file with an executable extension. |
| **Attack Scenario** | User A uploads a file with `Content-Disposition: attachment; filename="malware.exe"`. When User B downloads the file via a browser, it's saved as `malware.exe` instead of the original filename. |
| **Impact** | Potential for reflected header injection, CRLF injection into HTTP responses. |
| **Recommendation** | Validate/cleanse `Content-Disposition` values. Strip newline characters (`\r\n`) to prevent header injection. Validate filename extensions per security policy. |
| **Effort** | S |

---

### Finding 21: Cryptography — AES-GCM Nonce Reuse with Deterministic Data Key

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | GCM nonce generated with `crypto/rand` which is good, but same key reused across objects |
| **Location** | `internal/storage/encrypt.go:49-53` — `generateDataKey()` |
| **Description** | Each object gets a unique random nonce (12 bytes from `crypto/rand`), which is correct. The data key is also randomly generated per object. However, the nonce is only 96 bits from a CSPRNG, which means after ~2^48 encryptions there's a >50% chance of nonce collision (birthday bound). Since the service handles individual objects (not packets), this is acceptable for practical object counts. |
| **Impact** | Negligible for current scale. |
| **Recommendation** | For future-proofing, consider a 12-byte nonce with a counter prefix or use XChaCha20-Poly1305 (96-bit nonce is tight for very large deployments). Not urgent. |
| **Effort** | L (long-term improvement) |

---

## STRIDE Analysis Summary

| Threat | Key Findings |
|--------|-------------|
| **Spoofing** | MCP transport has no auth (Finding 2). JWT with no `exp` never expires (Finding 10). SigV4 implementation is sound. |
| **Tampering** | Storage-level retention lock bypass (Finding 19). AES-GCM AAD is nil (Finding 9). Content-Disposition injection (Finding 20). |
| **Repudiation** | Audit logging exists for all admin operations (positive). However, presigned URL logging logs keys (Finding 11). |
| **Info Disclosure** | Error messages leak internals (Finding 1). Webhook payloads expose keys/buckets (Finding 18). User-controlled headers echoed unsanitized (Finding 20). |
| **Denial of Service** | Idempotency key exhaustion (Finding 12). Rate limiter tenant hash map bounded (positive). No timeout on MCP read_resource body size (4 MiB limit exists). |
| **Elevation of Privilege** | MCP bypasses auth (Finding 2). Admin scope implicitly grants read+write (Finding 5). Admin routes not group-protected (Finding 3). IP condition bypass (Finding 14). |

---

## Conformance with OWASP Top 10

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| A01: Broken Access Control | **FAIL** | MCP no auth, admin scope issues, IP condition bypass |
| A02: Cryptographic Failures | **PASS** | AES-256-GCM, HMAC-SHA256, good CSPRNG usage |
| A03: Injection | **PASS** | SQL uses parameterized queries; minor LIKE wildcard issue |
| A04: Insecure Design | **MINOR** | Error disclosure, no rate-limit on admin endpoints |
| A05: Security Misconfiguration | **MINOR** | CORS default allows Methods, CORS per-bucket not rate-limited |
| A06: Vulnerable Components | **PASS** | Go stdlib only, minimal dependencies |
| A07: Auth Failures | **CRITICAL** | MCP no auth, admin route inconsistency |
| A08: Software Integrity | **PASS** | Go build, no remote code loading |
| A09: Logging Failures | **MEDIUM** | Tokens in URL logged (Finding 11) |
| A10: SSRF | **PASS** | Webhook HTTP client has 5s timeout (reasonable) |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase has strong foundations (Go stdlib, parameterized SQL, proper HMAC usage, AES-256-GCM envelope encryption, rate limiting). However, the security posture is undermined by several critical gaps:

- **MCP transport has zero authentication** — this is the single most critical issue and must be addressed before any production deployment.
- **Authorization is inconsistently applied** — admin routes rely on per-handler checks rather than middleware-level enforcement, creating risk of privilege escalation.

### Top 3 Critical Issues

1. **[Critical] MCP Transport Bypasses Auth** — Unauthenticated access to all CRUD operations via `/mcp` HTTP and stdio.
2. **[Critical] Admin Routes Not Group-Protected** — Missing middleware-level `ScopeAdmin` enforcement on `/v1/admin/*` routes.
3. **[High] Error Messages Leak Internals** — Raw error chains returned to clients in production.

### Top 3 Quick Wins

1. **S** — Fix error disclosure: Log full errors server-side, return generic messages to clients.
2. **S** — Group admin routes with `reg.Require(auth.ScopeAdmin)` middleware.
3. **S** — Validate batch delete/tag keys with the existing `validateKey()` function.

### Security Debt

| Issue | Effort | Impact |
|-------|--------|--------|
| AES-GCM AAD binding to object identity | S | Low — defense-in-depth |
| IP-based policy checks via `RemoteAddr` | S | High — fix for proxy deployments |
| JWT `exp` enforcement | S | Medium — prevent non-expiring tokens |
| Tenant budget locking in GC paths | M | Medium — retention compliance |
| Per-tenant webhook filtering | M | Low — multi-tenant hardening |
| Content-Disposition header sanitization | S | Medium — CRLF injection prevention |
| Key rotation for presign URLs | M | Medium — presigned URL security |

### Compliance Considerations

- **GDPR/SOC2**: The PII detector in the AI pipeline is a strong positive. However, webhook payloads with object keys could leak PII. Access logs capturing tokens in URLs is a GDPR concern under Article 32 (security of processing).
- **PCI DSS**: The credit card Luhn validation in PII detection is correct. However, any storage of scanned documents containing credit card numbers would require the SSE-at-rest encryption to be active (it is, with AES-256-GCM).
- **Security Headers**: The service does not set `Strict-Transport-Security`, `X-Content-Type-Options`, `X-Frame-Options`, or `Content-Security-Policy` headers. S3-compatible clients typically don't depend on these, but the REST and Web UI endpoints should serve them.
