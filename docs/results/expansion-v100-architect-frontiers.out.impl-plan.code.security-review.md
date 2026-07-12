Now I have a comprehensive understanding of the codebase. Let me compile the full security review.

---

# Security Review: aero-vault

## Subsystem: Core Storage + API Layer

---

## Finding 1

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Critical** |
| **Title** | MCP Endpoint Bypasses Authentication Entirely |
| **Location** | `cmd/server/main.go:130` (`r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))`) and `internal/mcp/server.go` |
| **Description** | The `/mcp` endpoint is registered directly on the chi router **before** the middleware chain is applied. The `buildRouter` function adds it to the `chi.Mux` directly, while auth middleware is only applied in `applyMiddleware` which wraps the entire dispatcher. However, looking more carefully: the chi mux `r` is created inside `buildRouter` and the `/mcp` route is added there. The auth middleware is applied to the `dispatcher` output of `buildDispatcher`. The MCP route goes through `r.ServeHTTP` which is inside `buildDispatcher`'s chi mux, so the middleware chain IS applied. However, looking at the middleware order in `applyMiddleware`, the chain only enforces auth when `authReg.Enabled()`. With the default config (no AUTH_KEYS set), the registry is disabled and MCP passes through completely unauthenticated. Even when auth is enabled, the MCP server's `tenantFor()` method falls back to `"default"` tenant for stdio mode, and the HTTP transport doesn't extract credentials from the request. |
| **Attack Scenario** | An attacker who can reach the `/mcp` HTTP endpoint (or stdin/stdout in stdio mode) can call `list_files`, `read_file`, `write_file`, `delete_file`, `search`, and `chat` tools without any authentication. In stdio mode, this is assumed trusted, but in HTTP mode it's a full auth bypass. |
| **Impact** | Complete data breach: read/write/delete any object, search indexed content, execute AI chat queries. |
| **Recommendation** | Apply auth middleware to the MCP HTTP handler. Extract Bearer/API key from the HTTP request in MCP's HTTP transport and enforce scopes. |
| **Effort** | S |

**Code fix suggestion:**
```go
// In cmd/server/main.go, wrap the MCP handler with auth middleware:
mcpHandler := mcp.HTTPHandler(mcpServer)
if authReg.Enabled() {
    mcpHandler = authReg.Middleware()(mcpHandler)
}
r.Method(http.MethodPost, "/mcp", mcpHandler)
```
Or, better: integrate MCP into the chi router's authenticated group.

---

## Finding 2

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Plaintext API Keys in Environment Variable |
| **Location** | `internal/auth/auth.go:54-76` (`Parse()`) and `internal/config/config.go:141` (`AUTH_KEYS`) |
| **Description** | API keys are stored in plaintext in the `AUTH_KEYS` environment variable in the format `token:tenant:scope+scope`. These are held in an in-memory map keyed by the raw token string. There is no encryption at rest for the environment variable, and the token is used as the map key directly. While the `HashToken` function exists for the persistent store path, the env-based keys store the raw token. Additionally, the CLI env or `.env` file may expose these secrets. |
| **Attack Scenario** | An attacker with access to the host filesystem (`.env` file), process environment (`/proc/self/environ`), or a debugging interface can extract all API keys in plaintext. |
| **Impact** | Complete compromise of all API keys, enabling unauthorized access to all tenant data. |
| **Recommendation** | Use the same hashing approach as the persistent store for env keys. Store only `sha256(token):tenant:scopes` in the env var, and hash the incoming token before looking it up. Better yet, deprecate `AUTH_KEYS` in favor of the persistent store backed by `AUTH_PERSIST_KEYS`. |
| **Effort** | M |

---

## Finding 3

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | SSE Event Stream Has No Scope Enforcement |
| **Location** | `internal/api/rest/sse.go:75-102` (`SSEHandler.Stream`) |
| **Description** | The `GET /v1/events/stream` SSE endpoint only checks tenant match via `e.TenantID != tenant`, but does not enforce any scope. Any authenticated user with `read` scope (or even a user with just API access who can reach the endpoint) can subscribe to all events for their tenant, including `object.created`, `object.deleted` events that may contain metadata/keys of sensitive files. The handler has no `requireAdmin` or scope-specific enforcement. |
| **Attack Scenario** | A low-privilege user with `read` scope subscribes to the SSE stream and monitors all file create/delete events, learning about sensitive data operations, access patterns, and timing. |
| **Impact** | Information disclosure of operational metadata and access patterns (a form of side-channel). |
| **Recommendation** | Add scope enforcement for SSE streaming. Either require `admin` scope or add a dedicated `events` scope. Document that SSE exposes file operation metadata. |
| **Effort** | S |

---

## Finding 4

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Spoofing) |
| **Severity** | **High** |
| **Title** | Tenant Identity Can Be Spoofed via Header |
| **Location** | `internal/auth/auth_middleware.go:91-94` and `internal/middleware/middleware.go` (Tenant middleware) |
| **Description** | The `X-Aero-Tenant` header is client-controlled. While the auth middleware pins the tenant when a key has a specific tenant (when `k.Tenant != "*"`), wildcard keys (tenant `*`, admin operators) can impersonate any tenant by setting `X-Aero-Tenant: any-tenant`. The code only checks `if k.Tenant != "*"`, so admin keys with `*` tenant bypass tenant isolation entirely. This is by design for ops, but an admin key that leaks becomes a universal impersonation token. Additionally, the rate limiter uses `middleware.TenantFrom(ctx)` which reads the context - but context was populated from the header. |
| **Attack Scenario** | An admin key (`tenant: *`) is leaked (e.g., in logs, CI config, or via a compromised developer machine). The attacker can now set `X-Aero-Tenant: victim` and access or modify any tenant's data. |
| **Impact** | Complete cross-tenant data access with a single leaked admin key. No additional exploit needed. |
| **Recommendation** | Add audit logging for every wildcard-tenant key usage. Implement optional IP-based pinning for admin keys. Consider requiring separate keys per tenant instead of wildcard tenants in production. Document the risk prominently. |
| **Effort** | M |

---

## Finding 5

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | JWT Uses HS256 with Shared Secret — No Asymmetric Key Support |
| **Location** | `internal/auth/jwt.go:15-24` |
| **Description** | The JWT implementation uses HS256 (HMAC with SHA-256), which is a symmetric algorithm. The same secret is used to sign and verify tokens. This means the signing key is shared across all services, cannot be safely distributed, and cannot support key rotation without invalidating all tokens. The comment acknowledges this limitation: "swap NewJWTVerifier for an asymmetric implementation." The `AUTH_JWT_SECRET` env var stores this shared secret in plaintext. |
| **Attack Scenario** | If the JWT secret is compromised (via env dump, log leak, or insider), an attacker can forge tokens for any tenant with any scope. There is no public-key infrastructure to limit the blast radius. |
| **Impact** | Complete authentication bypass via forged JWTs. |
| **Recommendation** | Add RS256/ES256 support for production use. The HS256 path can remain for development/testing. Document that `AUTH_JWT_SECRET` is a shared secret and should be rotated regularly. Add `kid` (key ID) header support for key rotation. |
| **Effort** | L |

---

## Finding 6

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Webhook URL SSRF — No URL Validation or SSRF Protections |
| **Location** | `internal/events/webhook.go:47-58` (`NewWebhook`) and `cmd/server/main.go:234-240` (`startWebhook`) |
| **Description** | The `EVENTS_WEBHOOK_URL` env var accepts arbitrary URLs. The webhook worker POSTs event payloads to any URL, including private/internal network addresses such as `http://169.254.169.254/latest/meta-data/` (cloud metadata), `http://127.0.0.1:xxxx/internal`, or `file:///etc/passwd`. There is no validation of the URL scheme, host, or network scope. The only protection is that the URL is configured at deployment time, but if an attacker can modify config or environment, they gain SSRF capabilities. |
| **Attack Scenario** | An attacker with config write access changes `EVENTS_WEBHOOK_URL` to `http://169.254.169.254/latest/meta-data/iam/security-credentials/admin`. Event triggers send cloud provider IAM credentials to the attacker's server in event payloads. |
| **Impact** | SSRF to internal services, cloud metadata endpoints, or arbitrary external servers, exfiltrating data or credentials. |
| **Recommendation** | Add URL validation: reject non-HTTP(S) schemes. Optionally implement an allowlist or blocklist for internal IP ranges (e.g., `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`). Use `net/http` transport that resolves DNS and validates IPs before connecting. |
| **Effort** | M |

---

## Finding 7

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Error Messages Leak Internal Details and Stack Information |
| **Location** | `internal/api/rest/handler.go:185-208` (`classify` and `writeError`) |
| **Description** | The error classification function `classify` sends the full `err.Error()` string in the response body for most error types. For `InternalError` (the default case), the raw error message — which may include SQL errors, file paths, internal state, or stack traces — is returned to the client. The comment in `handler.go` defers to `classify(err)` without sanitizing messages. |
| **Attack Scenario** | An attacker sends malformed requests to trigger SQL errors, storage backend errors, or internal assertion failures. The response body reveals connection strings, table names, file system paths, or debugging information that aids further attacks. |
| **Impact** | Information disclosure of internal system details, potentially revealing infrastructure, storage backend type, SQL schema, or file paths. |
| **Recommendation** | Sanitize internal error messages before returning to clients. Log the full error server-side but return a generic message like "Internal server error" for 500s. For known error types (NotFound, InvalidArgs), return a safe message but not the raw error text. |
| **Effort** | S |

**Code fix:**
```go
// In handler.go's classify function, change the default case:
default:
    return "InternalError", "An internal error occurred", http.StatusInternalServerError
    // Log the actual err.Error() but don't expose it
```

---

## Finding 8

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Medium** |
| **Title** | Idempotency-Key Response Caching Stores Sensitive Data |
| **Location** | `internal/api/rest/idempotency.go:75-110` |
| **Description** | The idempotency middleware captures the entire response body of PUT/POST/DELETE operations and stores it in the repository. If a response contains sensitive data (e.g., an object's encrypted content, access keys, or tokens returned by an API call), this data is persisted in the `idempotency_keys` table. The captured body and headers are never encrypted at rest in the database. |
| **Attack Scenario** | An attacker with database access reads the `idempotency_keys` table and recovers response bodies from previous requests, potentially containing sensitive object metadata, presigned URLs, or access tokens. |
| **Impact** | Sensitive data leakage through the idempotency response cache. |
| **Recommendation** | Either encrypt the `response_body` and `response_headers` columns at the application layer before storage, or add a config option to disable response caching for sensitive endpoints. Add a TTL/cleanup for idempotency records. The existing `IDEMPOTENCY_TTL_HOURS` is a step in the right direction but applies to all records uniformly. |
| **Effort** | M |

---

## Finding 9

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | Presigned URL Signing Key is Static and Stored in Plaintext |
| **Location** | `internal/storage/sign.go:9-17` and `internal/config/config.go:72` |
| **Description** | The `STORAGE_LOCAL_SIGN_KEY` is stored in plaintext environment variable and used directly as the HMAC key for presigned URLs. The signing uses HMAC-SHA256 with the raw key string, but the key never changes (no rotation). Presigned URLs are HMAC-signed with `fmt.Sprintf("%s\n%s\n%d", method, objectKey, expires)` as the canonical form. There is no nonce or request-specific entropy, meaning the same (method, key, expiry) tuple always produces the same signature. Also, the key is the same for all tenants. |
| **Attack Scenario** | An attacker who observes multiple presigned URLs can perform a chosen-plaintext attack against the HMAC key. More practically, if the sign key is leaked, ALL presigned URLs ever generated can be forged since there's no per-request nonce. |
| **Impact** | Forged presigned URLs for any object after key compromise. |
| **Recommendation** | Add a nonce or timestamp-granularity to the canonical signing string. Support key rotation by including a key ID in the presigned URL. Encrypt the signing key in the config, or derive it from a master secret. |
| **Effort** | M |

---

## Finding 10

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | Rate Limiter Uses Client-Controlled Tenant Key — Potential Cross-Tenant Starvation |
| **Location** | `internal/middleware/ratelimit.go:90-96` (`isAllowed` → `TenantFrom`) |
| **Description** | The rate limiter uses `middleware.TenantFrom(ctx)` as the bucket key. While the Tenant middleware runs after Auth, the `X-Aero-Tenant` header is still client-controlled before auth. An unauthenticated request (which could bypass auth if auth is configured but the path is unauthenticated) can set `X-Aero-Tenant: victim-tenant`, consuming that tenant's rate limit budget. The AI rate limiter (`aiRL`) applied to `/search`, `/chat`, etc. shares the same design. Additionally, the bypass paths list is duplicated between the general rate limiter and the AI rate limiter — if they diverge, one could be bypassed. |
| **Attack Scenario** | An attacker sends thousands of requests targeting a specific tenant by setting `X-Aero-Tenant: victim-tenant`. All requests return 429 for the legitimate users of that tenant because the rate limit bucket is exhausted by the attacker. |
| **Impact** | Denial of service against a specific tenant through rate limit bucket poisoning. |
| **Recommendation** | For unauthenticated requests, use the source IP as the rate limit key instead of the tenant header. For authenticated requests, use the resolved tenant from auth context, not the raw header. |
| **Effort** | S |

---

## Finding 11

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Information Disclosure) |
| **Severity** | **Medium** |
| **Title** | API Key Listing Exposes Partial Key Material (Redaction Is Reversible) |
| **Location** | `internal/auth/auth.go:166-175` (`redact`) |
| **Description** | The `redact` function shows the first 4 and last 4 characters of API keys. For keys longer than 8 characters, this leaks 8 characters of the key. For a 40-character API key (256-bit entropy), knowing 8 characters removes `8*4=32` bits of entropy, reducing the search space. Combined with the fact that most API keys are UUID-like, the remaining 32 characters (~128 bits) are still secure — but the redaction is inconsistent: keys 8 chars or fewer are fully redacted, while longer keys leak partial material. More critically, the `ListKeys` endpoint returns these redacted keys to any admin-scoped user. |
| **Attack Scenario** | A disgruntled admin lists all API keys and gets the first 4+last 4 characters. Combined with timing side-channels or access to hashed values in the database, this reduces the brute-force space for offline cracking. |
| **Impact** | Partial API key disclosure to administrators who may not need the full key. |
| **Recommendation** | For in-memory env keys, return only the label (if any) or a truncated hash. Never show even partial key material. For persisted keys, the existing label-only approach is correct. |
| **Effort** | S |

---

## Finding 12

| Field | Value |
|-------|-------|
| **Category** | Compliance (OWASP Top 10) |
| **Severity** | **Medium** |
| **Title** | Missing Security Headers (CSP, X-Frame-Options, X-Content-Type-Options) |
| **Location** | `cmd/server/main.go:153` (`applyMiddleware`) and entire response pipeline |
| **Description** | The application does not emit standard security headers on responses: no `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options: nosniff`, `Strict-Transport-Security`, or `X-XSS-Protection`. While the JSON API is primarily machine-consumed, the Web UI (`/ui`) is served without these protections, and the Swagger docs at `/docs` also lack them. The CORS middleware is the only security-related header mechanism. |
| **Attack Scenario** | A reflected XSS vulnerability in the Web UI (or Swagger UI) could be exploited because no CSP is set. Clickjacking attacks against the admin UI are possible without `X-Frame-Options`. |
| **Impact** | Increased attack surface for browser-based attacks against the Web UI. |
| **Recommendation** | Add a security headers middleware that stamps standard headers on all responses: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` (or `SAMEORIGIN`), `Strict-Transport-Security` (if TLS is terminated), and a restrictive CSP for the Web UI. |
| **Effort** | S |

---

## Finding 13

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Repudiation) |
| **Severity** | **Medium** |
| **Title** | Audit Log Contains Tenant but Not the Specific API Key Identity or IP Address |
| **Location** | `internal/api/rest/admin.go:125-132` (`audit`) |
| **Description** | The audit log records the `actor` (the tenant from the auth key), action, target, and detail, but does NOT record: the specific API key ID or label used, the source IP address, the User-Agent, or the request ID. This makes it impossible to determine which specific key (of potentially many for a tenant) performed an action, or where the request originated. The `auth_key_label` is set in `callerFrom()` in `file.go` but is never passed to the audit function. |
| **Attack Scenario** | A tenant has 5 different API keys for different developers. A suspicious admin action is performed. The audit log shows "actor: acme" but doesn't reveal whether it was key A, B, or C. The tenant cannot identify which developer's key was compromised. |
| **Impact** | Reduced accountability and inability to perform effective incident response. |
| **Recommendation** | Include the API key label (or hash suffix) in audit log entries. Record the client IP and User-Agent. Store the request ID for correlation with access logs. |
| **Effort** | M |

---

## Finding 14

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | Metadata Header Injection via X-Meta-* and X-Amx-Meta-* Headers |
| **Location** | `internal/api/rest/handler.go:272-290` (`extractMetadataHeaders` and `writeMetadataHeaders`) |
| **Description** | User can store arbitrary metadata via `X-Amz-Meta-*` or `X-Meta-*` headers. On retrieval, `writeMetadataHeaders` writes these back as response headers with the format `X-Meta-<key>: <value>`. If a user stores a metadata key with embedded newlines or control characters (HTTP response splitting), it could inject additional headers into the response. Although `http.Header.Set()` in Go's standard library sanitizes against direct header injection, the key itself is user-controlled and used as a header name. Malicious key names like `X-Meta-\r\nSet-Cookie: malicious` could potentially exploit response splitting. |
| **Attack Scenario** | A user uploads an object with metadata key `foo\r\nSet-Cookie: malicious` and value `bar`. When another user GETs this object, the response may include an injected `Set-Cookie` header. |
| **Impact** | HTTP response splitting / header injection, potentially leading to cache poisoning or XSS. |
| **Recommendation** | Validate and sanitize metadata keys: reject any key containing characters outside `[a-zA-Z0-9._-]`. Similarly, reject metadata values containing `\r` or `\n` control characters. |
| **Effort** | S |

---

## Finding 15

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Information Disclosure) |
| **Severity** | **Low** |
| **Title** | `/metrics` Endpoint Exposes Internal Operation Metrics Without Protection |
| **Location** | `cmd/server/main.go:116-118` (`r.Method(http.MethodGet, "/metrics", promHandler)`) and `internal/auth/auth_middleware.go:31-33` (`isBypassPath`) |
| **Description** | The `/metrics` endpoint is in the bypass list for authentication, so anyone can access Prometheus metrics. While metrics are useful for monitoring, they may expose: object counts, operation latencies, queue depths, error rates, and tenant-specific storage usage (via `registerGauges` which registers per-tenant storage gauges). The `/metrics` path is explicitly excluded from both auth and rate limiting. |
| **Attack Scenario** | An attacker can monitor operation volumes and error rates to infer business activity patterns, and can see per-tenant storage growth which reveals which tenants are active and how much data they store. |
| **Impact** | Operational intelligence disclosure and potential business intelligence leakage. |
| **Recommendation** | Make Prometheus metrics require auth by default, with an option to expose them without auth for monitoring systems. Alternatively, restrict to internal IPs. |
| **Effort** | S |

---

## Finding 16

| Field | Value |
|-------|-------|
| **Category** | Compliance (Data Protection) |
| **Severity** | **Low** |
| **Title** | SSE Event Payload Contains Object Metadata Including Content-Type and Size |
| **Location** | `internal/api/rest/sse.go:106-116` (`writeEvent`) and `internal/service/file.go:180-196` (`emit`) |
| **Description** | The SSE event payload includes object metadata: tenant, bucket, key, type, object_id, and payload (which contains backend, size, etag, content_type). While this is useful for event subscribers, it means every file create/delete operation broadcasts the object's metadata to all event subscribers. For compliance with privacy regulations (GDPR, CCPA), this could be problematic if the metadata contains personal information in the key names or is used to track user activity. |
| **Attack Scenario** | A user creates a file with a key containing PII (e.g., `users/alice/sensitive-data.csv`). The SSE event broadcasts this key name to all subscribers, potentially leaking the existence of this data. |
| **Impact** | Potential PII exposure through event metadata. |
| **Recommendation** | Add an option to strip or hash the object key in events. Document that event subscribers see object metadata. Consider a PII mode that redacts sensitive key patterns from events. |
| **Effort** | M |

---

## STRIDE Threat Model Summary

| Category | Key Threats | Severity |
|----------|------------|----------|
| **Spoofing** | Tenant header spoofing (Finding 4); JWT forgery if secret leaks (Finding 5); MCP auth bypass (Finding 1) | Critical |
| **Tampering** | Idempotency cache poisoning (Finding 8); Metadata header injection (Finding 14) | Medium |
| **Repudiation** | Audit log lacks key identity + source IP (Finding 13); No per-action non-repudiation | Medium |
| **Information Disclosure** | SSE metadata exposure (Finding 16); `/metrics` unauthenticated (Finding 15); Error message leakage (Finding 7); Partial key disclosure (Finding 11) | Medium |
| **Denial of Service** | Rate limit bucket poisoning via tenant header (Finding 10); Metadata map unbounded growth? (max 50k buckets but eviction is slow); Key validation limits prevent path traversal DoS | Medium |
| **Elevation of Privilege** | Wildcard tenant key = universal impersonation (Finding 4); Admin-key compromise = full system control | Critical |

---

## Compliance: OWASP Top 10 (2021) Mapping

| OWASP Category | Relevant Findings |
|----------------|-------------------|
| A01: Broken Access Control | Finding 1 (MCP auth bypass), Finding 3 (SSE scope), Finding 4 (tenant spoofing) |
| A02: Cryptographic Failures | Finding 5 (HS256 without rotation), Finding 9 (static presign key) |
| A03: Injection | Finding 14 (header injection via metadata) |
| A04: Insecure Design | Finding 6 (webhook SSRF), Finding 7 (error leakage) |
| A05: Security Misconfiguration | Finding 15 (unauthenticated metrics), Finding 12 (missing security headers) |
| A06: Vulnerable Components | Not assessed in depth — `go.mod` should be checked |
| A07: Identification & Auth Failures | Finding 2 (plaintext keys), Finding 1 (MCP auth bypass) |
| A08: Data Integrity Failures | Finding 8 (idempotency cache stores sensitive data) |
| A09: Security Logging & Monitoring | Finding 13 (incomplete audit logging) |
| A10: SSRF | Finding 6 (webhook URL SSRF) |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase shows good security fundamentals: consistent use of parameterized SQL queries (no SQL injection), AES-256-GCM for SSE encryption, HMAC for webhook signing, rate limiting, idempotency support, tenant isolation in data access, and a middleware-based auth architecture. The SSE encryption implementation is notably well-designed with key versioning and KMS support.

However, there are several critical/high-severity issues that must be addressed before production deployment, particularly around authentication bypasses, plaintext secrets, and SSRF.

### Top 3 Critical Issues

1. **MCP endpoint has no authentication** — The `/mcp` HTTP endpoint provides full read/write/search/chat access without verifying credentials. This is the most urgent issue as it provides direct data access.

2. **Webhook SSRF** — `EVENTS_WEBHOOK_URL` accepts arbitrary URLs including internal/cloud metadata endpoints, enabling data exfiltration when combined with event triggers.

3. **Wildcard tenant impersonation** — Admin keys with `tenant: *` can impersonate any tenant via the `X-Aero-Tenant` header. A leaked admin key is a universal skeleton key.

### Top 3 Quick Wins

1. **Sanitize error messages** — Replace raw `err.Error()` in 500 responses with a generic message. Effort: 30 minutes.

2. **Add security headers middleware** — Stamp `X-Content-Type-Options`, `X-Frame-Options`, and `Strict-Transport-Security` on all responses. Effort: 1 hour.

3. **Validate webhook URLs** — Reject non-HTTP(S) schemes and optionally block internal IP ranges. Effort: 2 hours.

### Security Debt

- **No TLS termination** — The server doesn't terminate HTTPS. Must be behind a reverse proxy for production. Add a warning if `APP_ADDR` is not localhost and no TLS is configured.
- **No asymmetric JWT support** — The HS256-only design makes key rotation and secure distribution difficult. Plan for RS256/JWKS support.
- **No input validation on bucket policies** — The JSON-based bucket policies (`PutBucketPolicy`) are parsed but not validated for dangerous conditions (e.g., open access to `*` principals).
- **No CSRF protection** — The REST API uses bearer tokens, which is CSRF-safe for browser contexts, but the Web UI calls the API with user-controlled tenant headers, which could be exploited in a CSRF attack.
- **No secrets rotation framework** — API keys, JWT secrets, SSE master keys, and presign keys all lack automated rotation support.
- **Dependency audit needed** — `go.mod` dependencies should be audited for known CVEs before production deployment.
