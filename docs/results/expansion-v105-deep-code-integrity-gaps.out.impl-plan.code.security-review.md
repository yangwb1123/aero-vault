---

# Principal Security Engineer — Aero-Vault Production Readiness Review

## Overall Security Posture: **Needs Improvement**

The codebase demonstrates strong security awareness in many areas (parameterized SQL, AES-256-GCM encryption, constant-time HMAC comparison, JWT validation), but has several **critical and high-severity issues** that must be addressed before production deployment under adversarial conditions.

---

## FINDINGS

---

### **F-01 | Critical | Authentication**

| Field | Value |
|-------|-------|
| **Title** | MCP stdio transport has zero authentication |
| **Location** | `internal/mcp/transport.go:27-55` (`ServeStdio`), `cmd/server/main.go` (`mcp` subcommand entry) |
| **Description** | The `aero-vault mcp` subcommand runs an MCP server over stdin/stdout with no authentication. Any process that can pipe data to the binary — including a compromised shell, CI pipeline, or container entrypoint — can invoke `list_files`, `read_file`, `write_file`, `delete_file`, `search`, and `chat` tools against the full storage backend. There is no API key check, no tenant boundary, and no audit trail tied to the caller identity. |
| **Attack Scenario** | An attacker who gains a foothold in the container (e.g. via a compromised dependency or exposed debug endpoint) runs `echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"key":"/etc/shadow"}}}' | aero-vault mcp` and exfiltrates the file. Or: `{"method":"delete_file","params":{"key":"prod/database_backup.sql"}}`. |
| **Impact** | Complete compromise of object storage — arbitrary read, write, delete of all objects. No audit trail tied to the attacker. |
| **Recommendation** | In `mcp` mode, require an `AUTH_TOKEN` environment variable. Before processing any tool call, validate a bearer token passed as the first line of stdin (or from an env var). Implement basic scope-checking: |
| **Effort** | **S** (< 1 day) |

**Fix concept:**
```go
// In transport.go ServeStdio, after scanner.Scan():
if s.authToken != "" {
    line := scanner.Text()
    var auth envelope
    json.Unmarshal([]byte(line), &auth)
    if !validateMCPSession(auth, s.authToken) {
        fmt.Fprintf(out, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"unauthorized"}}`)
        return
    }
}
```

---

### **F-02 | Critical | Data Protection**

| Field | Value |
|-------|-------|
| **Title** | Presigned URL HMAC signing key can be exfiltrated via any log that dumps config |
| **Location** | `internal/storage/local_read.go:68-101` (`presign`, `VerifyLocalSig`), `internal/storage/local.go:15` (`SignKey`) |
| **Description** | The `STORAGE_LOCAL_SIGN_KEY` is stored in plaintext in `LocalConfig` and used as the HMAC key for presigned URL generation. If this config is ever logged, serialized to JSON, or dumped via `/admin/config` (which currently returns a stub), the entire presigning system is compromised — an attacker who knows the SignKey can forge unlimited time-limited URLs for any object. The presigning scheme is HMAC-SHA256 over `method\nobjectKey\nexpires`. |
| **Attack Scenario** | An attacker with access to the config dump (e.g. via a debug endpoint, error log, or server-status page) extracts `SignKey`. They generate `sig = HMAC-SHA256(key, "GET\nvault/private-keys.pem\n9999999999")` and download any object via a forged presigned URL. |
| **Impact** | Complete bypass of authentication for object access. Forged URLs cannot be revoked (no token revocation list for presigned URLs). |
| **Recommendation** | (1) Mark `SignKey` as sensitive and never log it (add a `sensitive` annotation or redact in `String()`/`MarshalJSON()`). (2) Move presigned URL signing to a derived key (e.g., `HKDF(primary_key, "presign-v1", tenant)`) so each tenant has an independent signing key. (3) Add a short-lived presigned URL cache so replays can be detected. |
| **Effort** | **M** (1-3 days) |

---

### **F-03 | High | Authentication**

| Field | Value |
|-------|-------|
| **Title** | Anonymous public-read bypasses authentication at middleware level but defers ACL enforcement to handlers — inconsistent coverage |
| **Location** | `internal/auth/auth_middleware.go:48-51` (`isObjectReadPath` → anonRead path), `internal/api/rest/handler.go:84-88` (`allowAnonymous` not shown but called at line 218) |
| **Description** | When `AnonymousPublicRead` is enabled, the auth middleware allows unauthenticated GET/HEAD requests for object paths through without credentials. It marks the context with `IsAnonymous(ctx) = true` and relies on downstream handlers to check ACLs before returning data. However, the check is done inconsistently — `allowAnonymous` is called in `Get` and `Head`, but other read-like operations (e.g., `GET /v1/files/*/thumbnail`) may not gate with ACL checks. The S3 compat handler's `GetObject` has its own separate policy check. This fragmented enforcement creates a risk that a handler path forgets the ACL gate. |
| **Attack Scenario** | An attacker requests `GET /v1/files/secret/project-plan.pdf` without credentials. The middleware sets `anonCtxKey=true` and passes it to the handler. If `allowAnonymous()` returns true but the handler fails to check the object ACL (because it was added by a developer who didn't know about the anonymous-read contract), the file is served. |
| **Impact** | Unauthorized read access to objects that should be private, bypassing ACLs. |
| **Recommendation** | Move the ACL enforcement into a single middleware layer rather than scattering it across handlers. Create an `ACLEnforcer` middleware that sits after Auth and checks object-level ACLs for anonymous requests, so no handler can forget the gate: |
| **Effort** | **M** (1-3 days) |

---

### **F-04 | High | Authentication**

| Field | Value |
|-------|-------|
| **Title** | JWT verification uses HS256 with shared secret but no issuer validation by default |
| **Location** | `internal/auth/jwt.go:32-35` (`WithIssuer` is opt-in), `internal/auth/auth.go:119-123` (`lookupJWT`) |
| **Description** | The JWT verifier accepts any HS256-signed token that matches the shared `AUTH_JWT_SECRET`, regardless of issuer. There is no `aud` (audience) validation, no `iss` (issuer) validation by default, and no token-type restriction (the `typ` header is read but not validated). An attacker who obtains or guesses the shared secret can forge tokens for any tenant and any scope. Even without the secret, if the system integrates with multiple JWT issuers (e.g., an internal IdP plus a sidecar proxy), tokens from one issuer could be used to access resources intended for another. |
| **Attack Scenario** | A developer sets up two services sharing the same `AUTH_JWT_SECRET` for simplicity. Service A issues tokens for tenant "internal" with scope "read". Service B uses the same secret but different issuer. An attacker intercepted from A can replay against B because B doesn't validate the issuer. Worse: if the secret leaks (logged, committed, etc.), universal token forgery is possible. |
| **Impact** | Token forgery or cross-service token reuse, leading to unauthorized access. |
| **Recommendation** | (1) Make `WithIssuer` required, not opt-in. (2) Add support for RS256/ES256 with JWKS (key rotation without secret redistribution). (3) Validate `aud` (audience) claim when configured. (4) Document that HS256 is only suitable for internal, single-issuer deployments. |
| **Effort** | **M** (1-3 days) |

---

### **F-05 | High | Information Disclosure**

| Field | Value |
|-------|-------|
| **Title** | Object keys and full paths leak in access logs and error responses |
| **Location** | `internal/middleware/middleware.go:74` (`r.URL.Path` logged in AccessLog), `internal/api/rest/handler.go:667-669` (`classify()` default case returns `err.Error()` with full error text) |
| **Description** | The access log middleware logs `r.URL.Path` for every request, which includes full object keys (e.g., `/v1/files/confidential/contracts/acme-corp-2026.pdf`). The `classify()` function returns the raw `err.Error()` text for unknown errors (default case). Because FileService methods return Go errors formed with `fmt.Errorf`, these may include internal details like storage key paths, object IDs, SQL constraints, or tenant IDs. |
| **Attack Scenario** | An attacker with log access (e.g., a monitoring dashboard, log aggregation tool) reads object keys and can map the entire storage hierarchy. An error from a malformed request (e.g., a path traversal attempt) returns an error message that reveals the storage root path: `open ./var/objects/../../../etc/passwd: no such file or directory`. |
| **Impact** | Sensitive data exposure via logs (may violate GDPR/SOC2 if object keys contain PII). Information gathering for further attacks. |
| **Recommendation** | (1) Redact sensitive path segments in access logs (e.g., log `r.Method` + `r.URL.RawQuery` + hashed path). (2) Never return `err.Error()` directly to the client in production — always map to a generic safe message. (3) Use structured error types with a `Public()` method for safe serialization. |
| **Effort** | **S** (< 1 day) |

---

### **F-06 | High | Input Validation**

| Field | Value |
|-------|-------|
| **Title** | Content-Disposition header injection via stored user metadata |
| **Location** | `internal/api/rest/handler.go:865-868` (`addContentHeaders`), `internal/api/rest/handler.go:884-886` (`writeContentResponseHeaders`), `internal/service/file_crud.go:50-90` (metadata stored as-is) |
| **Description** | User-supplied `Content-Disposition` headers are stored verbatim in metadata under `_aero_content_disposition` and replayed verbatim on GET/HEAD responses. If a user uploads an object with `Content-Disposition: attachment; filename="legit.pdf"\r\nX-CSRF: injected`, the raw header value is passed directly to `w.Header().Set(...)`, which — while Go's `http.Header.Set` validates header values — does not prevent injection of line breaks in certain contexts. More critically, a crafted `Content-Disposition` value with encoded special characters could cause browsers to misinterpret the file type (e.g., serving a `.txt` file as `Content-Disposition: attachment; filename="malware.exe"`). |
| **Attack Scenario** | Alice creates an object with metadata containing `Content-Disposition: inline; filename="receipt.pdf"`. The file is actually a JavaScript payload. Bob views it in a browser that trusts the storage domain, and the browser renders it as HTML because the disposition says "inline" and the `Content-Type` could be overrideable. |
| **Impact** | Stored XSS against users viewing objects in a web browser via the WebUI or direct GET. File-type spoofing for phishing/malware delivery. |
| **Recommendation** | Validate and sanitize `Content-Disposition` headers on write: (1) Restrict to known-disposition types (`inline` | `attachment`). (2) Strip or encode special characters (CR, LF, NULL) from the filename parameter. (3) When the file is retrieved via the Web UI, enforce `X-Content-Type-Options: nosniff` and `Content-Disposition: attachment` as a default. |
| **Effort** | **S** (< 1 day) |

---

### **F-07 | Medium | Data Protection**

| Field | Value |
|-------|-------|
| **Title** | Webhook failure persistence stores full event payload with potentially sensitive data |
| **Location** | `internal/events/webhook.go:139-148` (`persistFailure` stores raw `string(body)` as Payload in the `webhook_failures` table) |
| **Description** | When a webhook delivery fails, the complete event body (which includes object key, bucket, tenant, and the full event payload) is stored as a string in the repository's `webhook_failures` table. This payload can be retrieved via `GET /v1/admin/webhook-failures`. There is no TTL-based cleanup on this table, so failures accumulate indefinitely. If the payload contains sensitive object data (the `Payload` field is an arbitrary JSON object from the event bus), it persists in the database beyond its useful lifetime. |
| **Attack Scenario** | An admin operator lists webhook failures and sees complete event data including object keys and custom payload fields. If the admin endpoint is exposed or an attacker gains read access to the webhook_failures table (via SQL injection or direct DB access), they can reconstruct deleted object metadata. |
| **Impact** | Persistent storage of sensitive event data beyond operational need. Increased blast radius in case of database compromise. |
| **Recommendation** | (1) Add a TTL or retention limit to `webhook_failures` — auto-delete entries older than 7 days. (2) Do not store the full payload; store only `event_id`, `event_type`, `url`, `error`, `next_retry_at`. (3) If the payload must be stored, encrypt it at the application layer before persistence. |
| **Effort** | **S** (< 1 day) |

---

### **F-08 | Medium | Threat Model (DoS)**

| Field | Value |
|-------|-------|
| **Title** | Tenant rate limiter uses client-controlled tenant identifier, enabling cross-tenant bucket exhaustion |
| **Location** | `internal/middleware/ratelimit.go:120-125` (`isAllowed` → `TenantFrom(ctx)`) |
| **Description** | The rate limiter's token bucket is keyed by tenant, which comes from `TenantFrom(ctx)`. This tenant value originates from the `X-Aero-Tenant` header — a client-controlled value. While the auth middleware validates the tenant against the API key's tenant binding (for non-`*` tenants), an admin key (`tenant=*`) can use arbitrary tenant values. A malicious admin key holder (or an attacker who obtains one) can iterate through unique tenant strings, each creating a new token bucket up to `rlMaxBuckets` (50,000). This consumes memory and CPU. More importantly, the same attacker can consume rate limit budget for legitimate tenants by sending requests with their tenant ID. |
| **Attack Scenario** | Mallory has an admin key with `tenant=*`. Mallory sends 1000 requests with `X-Aero-Tenant: legitimate-tenant`, consuming that tenant's rate limit budget. Legitimate users of "legitimate-tenant" get 429 rate-limited responses. |
| **Impact** | Denial of service against specific tenants. Memory exhaustion from bucket map growth. |
| **Recommendation** | For rate limiting, derive the tenant from the authenticated key, not from the header. The `auth.FromContext(ctx).Tenant` is the authoritative tenant. In the rate limiter, use this value instead of `TenantFrom(ctx)`. |
| **Effort** | **S** (< 1 day) |

---

### **F-09 | Medium | Authentication**

| Field | Value |
|-------|-------|
| **Title** | API keys accepted via both `Authorization: Bearer` and `X-Api-Key` header — dual-path creates confusion |
| **Location** | `internal/auth/auth_middleware.go:92-100` (`extractToken`) |
| **Description** | The `extractToken` function reads the API token from the `Authorization: Bearer <token>`, `Authorization: ApiKey <token>`, **and** `X-Api-Key` headers. If both `Authorization` and `X-Api-Key` are present, `Authorization` takes precedence. This dual-header scheme is undocumented and creates confusion: a security audit might review only the `Authorization` header and miss that the `X-Api-Key` header also works. Additionally, if both headers are sent with different values, the system silently uses one (Authorization), creating ambiguity in audit logs about which credential was used. |
| **Attack Scenario** | A developer hardens service A to send `Authorization: Bearer <key>` but accidentally leaves a debug proxy that sets `X-Api-Key: <stale-key>`. If the Authorization header is stripped by a proxy but X-Api-Key passes through, the stale key is accepted. |
| **Impact** | Ambiguous credential handling. Potential use of stale or unintended credentials. Audit trail confusion. |
| **Recommendation** | (1) Remove the `X-Api-Key` header path. Require all API keys via `Authorization: Bearer`. (2) If backward compatibility requires the dual path, log a deprecation warning when `X-Api-Key` is used. |
| **Effort** | **S** (< 1 day) |

---

### **F-10 | Medium | Authentication**

| Field | Value |
|-------|-------|
| **Title** | CORS `*` wildcard origin allows cross-origin requests from any website |
| **Location** | `internal/middleware/cors.go:29-53` (`CORS`, `matchOrigin`) |
| **Description** | The CORS middleware accepts `*` as a valid origin in the `AllowedOrigins` list. When `*` is present, `Access-Control-Allow-Origin: *` is returned for any origin. Combined with `AllowCreds`, this mirrors the insecure `Access-Control-Allow-Origin: *` + `Access-Control-Allow-Credentials: true` pattern (though credentials are not enabled by default). Even without credentials, a wildcard CORS policy allows any website to issue cross-origin requests to the storage API, which could be used in cross-site search attacks (e.g., detecting whether a given file exists via timing or response size). |
| **Attack Scenario** | Evil.com's JavaScript makes a cross-origin request to `https://aero-vault.internal/v1/files/secret-report.pdf`. Even without credentials, the request reaches the server. If the server responds, the response content cannot be read by the script (no credentials), but the fact that the resource exists can be detected through error messages or timing. |
| **Impact** | Data existence disclosure via cross-origin timing attacks. Potential CSRF on state-changing endpoints if cookies/cached credentials were present. |
| **Recommendation** | (1) Reject `*` as an allowed origin when any other security-sensitive configuration is enabled. (2) In production, require explicit origin allow-lists. (3) Apply the principle of least privilege: CORS should only be enabled for specific origins that need browser-based access. |
| **Effort** | **S** (< 1 day) |

---

### **F-11 | Medium | Threat Model (Tampering)**

| Field | Value |
|-------|-------|
| **Title** | SigV4 streaming body chunk signatures are NOT verified after the initial handshake |
| **Location** | `internal/auth/sigv4_chunk.go:28-31` (comment: "Per-chunk signatures are not re-verified"), `internal/auth/sigv4.go:57` (`UNSIGNED-PAYLOAD`) |
| **Description** | The SigV4 implementation accepts `UNSIGNED-PAYLOAD` as the content hash (the default for presigned URLs and the option for header-signed requests). More critically, when a client uses the `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` transfer encoding (aws-cli's default for multipart uploads), the initial signature is verified but each individual chunk's signature is *not* verified. The `decodeStreamingBody` function strips the chunked transfer encoding and presents the raw body to handlers. This means a man-in-the-middle can splice the initial signed chunk header with different payload data, and the server will accept it. |
| **Attack Scenario** | Alice initiates a signed upload of `important.pdf`. Eve, in a MITM position on the network, replaces the body data after the first chunk header while preserving the final boundary. The server verifies the initial seed signature but processes the replaced content as the object body. |
| **Impact** | Data tampering during upload. Objects stored may differ from what the client intended to upload. |
| **Recommendation** | (1) Document clearly that `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` chunk signing verification is not implemented. (2) Require `X-Amz-Content-Sha256: <hash>` (full payload hash) for all uploads, rejecting `STREAMING-AWS4*` and `UNSIGNED-PAYLOAD` when data integrity is required. (3) Implement Content-MD5 verification at the application layer (the `Content-MD5` header is already accepted and stored as `_aero_content_md5`). |
| **Effort** | **M** (1-3 days) |

---

### **F-12 | Medium | Data Protection**

| Field | Value |
|-------|-------|
| **Title** | No TLS termination or enforcement at the application layer |
| **Location** | `cmd/server/main.go` (`runServer`), entire codebase — no TLS configuration |
| **Description** | The server listens on a plain HTTP socket. There is no TLS configuration, no cert loading, no HTTPS listener, and no mechanism to redirect HTTP to HTTPS. All data in transit — including API key tokens in `Authorization` headers, JWT tokens, S3 SigV4 credentials, object data, and SSE encryption keys transmitted to KMS — travels in cleartext over the network. While TLS termination can be handled by a reverse proxy (nginx, Envoy, AWS ALB), the application **does not enforce** TLS in any way: no `Strict-Transport-Security` header, no TLS client certificate support, no `upgrade-insecure-requests` directive. |
| **Attack Scenario** | An attacker on the same network segment (eavesdropping on a shared WiFi, compromised cloud VPC, or malicious ISP) captures HTTP traffic. They harvest API keys from `Authorization` headers and JWT tokens. With these credentials, they authenticate to the server and exfiltrate all objects. |
| **Impact** | Full credential and data exposure over the wire. Complete compromise of authentication and data confidentiality. |
| **Recommendation** | (1) Add TLS support directly in the server via `http.Server.TLSConfig` with ACME/Let's Encrypt auto-provisioning. (2) When running behind a reverse proxy, enforce TLS by: (a) rejecting `X-Forwarded-Proto: http` if behind a trusted proxy, (b) sending `Strict-Transport-Security: max-age=31536000; includeSubDomains` header on all responses. (3) Document the TLS expectations clearly. |
| **Effort** | **M** (1-3 days) |

---

### **F-13 | Low | Authentication**

| Field | Value |
|-------|-------|
| **Title** | JWT `nbf` (not-before) and `exp` (expiry) use Unix seconds without grace period for clock skew |
| **Location** | `internal/auth/jwt.go:85-92` (`decodeAndValidateClaims`) |
| **Description** | The JWT validation uses `time.Now().Unix()` without any configurable clock skew tolerance (`leeway`). Standard JWT libraries (e.g., `github.com/golang-jwt/jwt/v5`) default to a 1-minute leeway to account for clock differences between the issuer and verifier. Without this, a token that is valid according to the issuer's clock may be rejected by the verifier if their clocks differ by even a few seconds. Conversely, an expired token could still be accepted briefly. |
| **Attack Scenario** | Service A (issuer) issues a JWT at 10:00:00.000 with `exp: 3600` (expires at 11:00:00.000). Service B (verifier) has a clock 30 seconds behind. At 10:59:31 Service B's clock reads 10:59:01, so it still accepts the token. At 11:00:31 Service B's clock reads 11:00:01, and it rejects a token that should still be valid for 29 more seconds. |
| **Impact** | Tokens may be accepted up to a few seconds past expiry or rejected before they should be, causing intermittent authentication failures. |
| **Recommendation** | Add a configurable leeway (default 30 seconds) to JWT expiry and not-before checks. Use `time.Now().Add(-leeway).Unix()` for `nbf` and `time.Now().Add(leeway).Unix()` for `exp`. |
| **Effort** | **S** (< 1 day) |

---

### **F-14 | Low | Cryptography**

| Field | Value |
|-------|-------|
| **Title** | SSE data key generation uses `crypto/rand` but no explicit blocking-read safeguard |
| **Location** | `internal/storage/encrypt.go:57-64` (`generateDataKey`) |
| **Description** | The `generateDataKey` function calls `rand.Read(key)` and `rand.Read(nonce)` sequentially. On systems where `/dev/urandom` blocks (early boot, containers with limited entropy), this could cause latency spikes for object write operations. While Go's `crypto/rand` reads from `/dev/urandom` which typically doesn't block, in certain container environments or Linux kernel configurations, it may fall back to `/dev/random` behavior and block. |
| **Attack Scenario** | Not directly exploitable, but a DoS amplification vector: an attacker who can trigger many concurrent object writes (e.g., via a multipart upload with many parts) could cause the server to exhaust available entropy, blocking all writes until the entropy pool refills. |
| **Impact** | Potential Denial of Service for write operations under low-entropy conditions. |
| **Recommendation** | (1) Use `crypto/rand` with a read-all-or-fail pattern. (2) Batch key generation where possible. (3) Document that containers should ensure sufficient entropy (e.g., via `haveged` or `rng-tools`). |
| **Effort** | **S** (< 1 day) |

---

### **F-15 | Low | Compliance**

| Field | Value |
|-------|-------|
| **Title** | No Content-Security-Policy, X-Content-Type-Options, or other security headers |
| **Location** | Entire codebase, especially `internal/webui/web.go` and `cmd/server/main.go` |
| **Description** | The server does not emit security-related HTTP headers on any response: no `Content-Security-Policy`, no `X-Content-Type-Options: nosniff`, no `X-Frame-Options`, no `Referrer-Policy`, and no `Permissions-Policy`. The embedded Web UI (`/ui`) could be vulnerable to XSS if user-controlled data is rendered without proper sanitization. The REST API responses could be rendered as HTML by a browser if `Content-Type` is not correctly set. |
| **Attack Scenario** | A user clicks a link to `https://aero-vault/v1/files/report.pdf` in their browser. The browser receives the PDF but without `X-Content-Type-Options: nosniff`, it may sniff the content and render it as HTML if the file starts with `<html>`. An attacker could upload a file that appears to be a PDF but contains JavaScript, achieving stored XSS. |
| **Impact** | Browser-based attacks against users viewing objects. XSS in the context of the storage domain. |
| **Recommendation** | Apply a middleware that sets baseline security headers on all responses: |
| **Effort** | **S** (< 1 day) |

```go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Referrer-Policy", "no-referrer")
        w.Header().Set("X-XSS-Protection", "0") // modern browsers ignore this anyway
        next.ServeHTTP(w, r)
    })
}
```

---

## STRIDE Threat Model Summary

| Category | Key Threats | Severity |
|----------|-------------|----------|
| **S**poofing | MCP stdio has no auth (F-01). Anonymous read bypasses ACL (F-03). JWT without issuer (F-04). | Critical |
| **T**ampering | SigV4 streaming chunk signatures unverified (F-11). Content-Disposition injection (F-06). | Medium |
| **R**epudiation | MCP stdio audit trails not tied to caller identity (F-01). Webhook failure logs don't have caller info. | Medium |
| **I**nformation Disclosure | Object keys in access logs (F-05). Presign URL forging with leaked SignKey (F-02). Webhook failure payloads stored (F-07). No TLS (F-12). | Critical |
| **D**enial of Service | Rate limit tenant header spoofing (F-08). Entropy exhaustion on SSE writes (F-14). Webhook retry loop can amplify traffic. | Medium |
| **E**levation of Privilege | Cross-tenant access via X-Aero-Tenant header with wildcard key (F-08). Anonymous read without ACL (F-03). | High |

---

## OWASP Top 10 Mapping

| OWASP | Related Findings |
|-------|-----------------|
| **A01** Broken Access Control | F-01 (MCP no auth), F-03 (anon read bypass), F-08 (tenant header spoofing) |
| **A02** Cryptographic Failures | F-02 (SignKey leakage), F-12 (no TLS), F-14 (entropy for keygen) |
| **A03** Injection | F-06 (Content-Disposition header injection) |
| **A04** Insecure Design | F-01 (MCP no auth by design), F-07 (webhook payload storage) |
| **A05** Security Misconfiguration | F-10 (CORS wildcard), F-13 (JWT no clock skew), F-15 (security headers) |
| **A06** Vulnerable Components | Not assessed in this review |
| **A07** Identification/Auth Failures | F-04 (JWT issuer), F-09 (dual header API keys) |
| **A08** Software/Data Integrity Failures | F-11 (SigV4 chunk tampering) |
| **A09** Security Logging/Monitoring Failures | F-05 (keys in logs), F-01 (MCP no audit trail) |
| **A10** SSRF | Not found — webhook URLs are configured, not user-controlled |

---

## Final Summary

| Metric | Assessment |
|--------|-----------|
| **Overall Security Posture** | **Needs Improvement** |
| **Critical Issues** | 2 |
| **High Issues** | 4 |
| **Medium Issues** | 5 |
| **Low Issues** | 4 |

### Top 3 Critical Issues

1. **MCP stdio zero authentication (F-01)** — The `aero-vault mcp` subcommand provides unrestricted read/write/delete access to storage with no credential check. This is the single most dangerous attack surface. A compromised process on the host can exfiltrate or destroy all data.

2. **Presigned URL SignKey leakage risk (F-02)** — The HMAC key used to sign time-limited URLs is stored in plain config and could be logged, dumped, or exfiltrated. If compromised, all presigned URL security is negated — the attacker can forge access to any object.

3. **Anonymous public-read ACL enforcement is fragmented (F-03)** — The anonymous-read bypass relies on every handler checking ACLs individually. This is a fragile pattern; a single missed check exposes private data without authentication.

### Top 3 Quick Wins (High Impact, Low Effort)

1. **Add security headers middleware (F-15) — < 1 hour.** A single middleware adding `X-Content-Type-Options`, `X-Frame-Options`, and `Content-Security-Policy` across all responses blocks entire classes of browser-based attacks.

2. **Remove `X-Api-Key` header path (F-09) — < 1 hour.** Unifying on `Authorization: Bearer` eliminates a dual-header authentication path and strengthens audit clarity.

3. **Add JWT clock skew leeway (F-13) — < 2 hours.** A 30-second leeway in JWT `exp`/`nbf` validation prevents intermittent auth failures due to clock differences between issuer and verifier.

### Security Debt

- **No TLS in application layer** (F-12) — While a reverse proxy can terminate TLS, the application has no awareness or enforcement of transport security. Adding HSTS and certificate auto-provisioning would make it production-ready for direct exposure.

- **SigV4 streaming chunk verification** (F-11) — AWS SDK clients default to chunked upload; the fact that per-chunk signatures are not verified means AWS SDK uploads may not have end-to-end integrity guarantees. This should be documented or fixed.

- **Webhook failure retention** (F-07) — No TTL on webhook failures means the table grows unboundedly and retains potentially sensitive event data. A retention policy (e.g., auto-delete after 7 days) should be implemented.

- **Rate limiter tenant derivation** (F-08) — The use of client-controlled `X-Aero-Tenant` for rate limiting (vs. the authenticated tenant) enables cross-tenant DoS. This is a design debt item that should be fixed before multi-tenant production deployments.
