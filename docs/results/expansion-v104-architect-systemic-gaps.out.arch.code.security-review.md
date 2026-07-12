I now have a thorough understanding of the codebase. Here is my comprehensive security review.

---

# 🔒 Security Review: aero-vault

**Reviewer:** Principal Security Engineer  
**Target:** Full codebase (243 Go files)  
**Commit:** Current HEAD  
**Date:** 2026-07-12

---

## Executive Summary

**Overall Security Posture: Needs Improvement**

The codebase has a solid cryptographic foundation (AES-256-GCM, proper HMAC usage, parameterized SQL) but suffers from several critical configuration and integration gaps. Authentication is opt-in and disabled by default. The presigned URL feature generates URLs that are never validated server-side — a complete bypass of the authorization model. Several OWASP Top 10 controls (security headers, CSP, input size limits on certain paths) are absent.

---

## Detailed Findings

### [CRITICAL-1] Presigned URL Verification Not Wired (Bypass)

| Field | Value |
|-------|-------|
| **Category** | Authentication / Authorization |
| **Severity** | **Critical** |
| **Title** | Presigned URL verification endpoint does not exist |
| **Location** | `internal/storage/local_read.go:68-101` — `PresignGet`, `PresignPut`, `VerifyLocalSig` definition — but `VerifyLocalSig` is **never called** outside tests |
| **Description** | The presigned URL flow has two halves. The **generation** half works: `POST /v1/files/*/presign` → `service.PresignGet` → `storage.PresignGet` → `signLocal()` returns a URL like `{PublicURL}/{key}?expires=N&sig=HMAC&method=GET`. However, the **verification** half (`VerifyLocalSig`) is only referenced in test files. There is **no HTTP handler** that receives these presigned requests, validates the HMAC, checks expiration, and serves the object. The `PublicURL` config option suggests an external endpoint, but no route serves it. |
| **Attack Scenario** | An attacker who obtains a presigned URL (e.g., from logs, a compromised admin, or a misdirected email) could attempt to access the URL, but it would always fail — not because of security, but because the endpoint doesn't exist. **More critically:** If an operator configures `PublicURL` pointing to the aero-vault server itself (e.g. `http://localhost:8080/presigned`), the generated URLs are **unvalidated**: any request to `/presigned/{key}?...` hits the router and either 404s or falls through to regular auth, completely bypassing the presigned-signature check. |
| **Impact** | The presigned URL feature is broken and provides a **false sense of security**. Users who rely on time-limited, signature-verified access are exposed. |
| **Recommendation** | Wire a presigned URL handler that calls `VerifyLocalSig` before serving. Either add a dedicated route on the main server or document that `PublicURL` must point to a separate service that calls `VerifyLocalSig`. Given the current architecture, the simplest fix: mount a handler at `/files/` that validates presigned URLs and proxies to `FileService.Get`. |
| **Effort** | M |

---

### [CRITICAL-2] Authentication Disabled by Default

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Critical** |
| **Title** | Auth disabled when `AUTH_KEYS` is empty |
| **Location** | `internal/auth/auth.go:72` — `enabled` field; `internal/auth/auth_middleware.go:24-26` — early return when `!r.Enabled()` |
| **Description** | When `AUTH_KEYS` env var is empty (the default), the `Registry.Enabled()` returns false and the `Middleware()` is a complete pass-through — no authentication, no tenant extraction, no scope check. Every endpoint becomes publicly accessible. All admin operations (key management, tenant config, audit logs, quota changes) have no protection. |
| **Attack Scenario** | Default deployment with `AUTH_KEYS=""` can be accessed by anyone who reaches the server port. Full read/write/admin access to all data. |
| **Impact** | Complete data exposure. Full administrative access to tenant/quota/key management. |
| **Recommendation** | Change default to require authentication. Document in `.env.example` and `README.md` that this is the insecure "quickstart" mode. Consider adding a hard `FORCE_AUTH=true` config or at minimum logging a loud warning on startup when auth is disabled. |
| **Effort** | S |

---

### [HIGH-3] Missing Security Headers (XSS/Clickjacking)

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **High** |
| **Title** | No security headers on HTTP responses — vulnerable to XSS and clickjacking in Web UI |
| **Location** | `internal/middleware/middleware.go` (entire file); `cmd/server/main.go:198-260` middleware chain — no security header middleware exists |
| **Description** | The response pipeline sets no `X-Frame-Options`, `X-Content-Type-Options`, `Content-Security-Policy`, or `Strict-Transport-Security` headers. While the REST API returns JSON (mitigating reflective XSS), the Web UI at `/ui` serves HTML and could be embedded in an iframe (clickjacking). The `/docs` (Swagger UI) path also lacks headers. Error responses include the raw error message (see HIGH-5). |
| **Attack Scenario** | An attacker could embed the web UI in an iframe on a malicious site to trick users into performing actions while authenticated (clickjacking). Without `X-Content-Type-Options: nosniff`, a browser might MIME-sniff a user-uploaded file served via the API as executable content. |
| **Impact** | Session hijacking via clickjacking; MIME-type confusion leading to XSS via uploaded content. |
| **Recommendation** | Add a security-headers middleware that stamps: `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `X-XSS-Protection: 0` (modern browsers), `Referrer-Policy: same-origin`, and a restrictive `Content-Security-Policy`. Apply before CORS in the chain. |
| **Effort** | S |

---

### [HIGH-4] DELETE /v1/admin/keys/{token} — Secret in URL Path

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Authentication |
| **Severity** | **High** |
| **Title** | API key token transmitted in URL path for revocation |
| **Location** | `internal/api/rest/admin.go:148-155` — `RevokeKey` handler; `internal/api/rest/router.go:114` — route `Delete("/admin/keys/{token}", adm.RevokeKey)` |
| **Description** | The API key token (the plaintext secret used for authentication) is sent as part of the URL path when revoking keys. This exposes the token to:
- **Server access logs** (the `AccessLog` middleware logs the full path)
- **HTTP referrer headers**
- **Browser history** (if invoked from a browser)
- **Proxy logs** and **router/firewall logs**
While the `AddKey` endpoint accepts the token in JSON body, the `RevokeKey` endpoint puts it in the URL. This asymmetry means a token being revoked is more exposed than when it was created. |
| **Attack Scenario** | An admin revokes a leaked key via `DELETE /v1/admin/keys/ak-prod-rw:acme:read+write`. The token appears in server logs verbatim. A subsequent log compromise reveals all revoked tokens — including their plaintext values, allowing an attacker to use them before the revocation propagates. |
| **Impact** | Credential disclosure through logs. Reuse of revoked API keys. |
| **Recommendation** | Accept the token in the request body (JSON `{"token":"..."}`) via `POST /v1/admin/keys/revoke` instead of a path parameter. If the DELETE verb must be preserved, accept the token in a header such as `X-Api-Key` or as a JSON body on DELETE (custom handling). |
| **Effort** | S |

---

### [HIGH-5] Error Messages Leak Internal Details

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure / Data Protection |
| **Severity** | **High** |
| **Title** | Internal error details returned to clients |
| **Location** | `internal/api/rest/handler.go:360-386` — `classify` function returns `err.Error()` as message for `InternalError` status (500); `internal/api/rest/admin.go` — various `err.Error()` calls in responses |
| **Description** | The `classify` function falls through to `"InternalError", err.Error(), 500` for unclassified errors. This exposes raw Go error messages, which may include:
- File paths (storage layer failures)
- SQL errors (database constraint names, table names)
- Network errors (internal hostnames, IPs)
- Stack traces (if wrapped with `fmt.Errorf("...%w", err)`) |
| **Attack Scenario** | An attacker sends malformed requests to trigger errors. The response reveals `"storage delete: remove /var/objects/acme/default/secret.docx: permission denied"` — revealing the filesystem layout and a hint that storage is local FS on a particular path. SQL errors might reveal schema details useful for injection (though SQL injection is properly mitigated via parameterized queries). |
| **Impact** | Information leakage aiding reconnaissance. |
| **Recommendation** | Return a generic `"InternalError"` message for 5xx responses. Log the full error server-side. Consider a config option to enable detailed error messages in development. For known error types that are safe (e.g., `ErrNotFound`, `ErrInvalidArgs`), return their specific messages. |
| **Effort** | S |

---

### [MEDIUM-6] Webhook SSRF via `EVENTS_WEBHOOK_URL`

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Input Validation |
| **Severity** | **Medium** |
| **Title** | Webhook URL accepts arbitrary internal/private URLs |
| **Location** | `internal/events/webhook.go:53-60` — `NewWebhook` constructor; `internal/api/rest/handler.go:249-272` — `PutBucketNotifications` and other event config endpoints |
| **Description** | The webhook URL is configured via `EVENTS_WEBHOOK_URL` (env var). While this is an operator-controlled configuration rather than user input, the notification-config endpoints (`PUT /v1/buckets/{bucket}/notification`) can set custom URLs per-bucket at runtime. There's no validation that the URL is external — an attacker with write access could point notifications at internal services (`http://localhost:5432/payload`, `http://metadata.google.internal/`, etc.). |
| **Attack Scenario** | An attacker with compromised admin credentials (or exploiting auth-disabled default) sets a bucket notification URL to `http://169.254.169.254/latest/meta-data/` (AWS/GCP metadata endpoint) or `http://internal-vault:8200/unwrap`. Each object event triggers a POST with event payload, potentially exfiltrating data or triggering side effects in internal services. |
| **Impact** | Server-side request forgery, data exfiltration via event bus, internal service enumeration. |
| **Recommendation** | Add URL validation: reject private/reserved IP ranges (RFC 1918, loopback, metadata IPs) for webhook URLs. Consider a blocklist/allowlist approach. Apply the same validation to bucket notification rules at write time. |
| **Effort** | M |

---

### [MEDIUM-7] Rate Limiting Disabled by Default + Tenant Bypass

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **Medium** |
| **Title** | Rate limiting is opt-in and runs before tenant extraction |
| **Location** | `internal/middleware/ratelimit.go:26-38` — `NewRateLimiter` returns nil when rps/burst ≤ 0; `internal/ratelimit.go:96-98` — `isAllowed` uses `TenantFrom(ctx)`; `internal/api/rest/router.go:72-81` — AI-specific rate limiter applied; middleware chain order in `cmd/server/main.go:236-253` |
| **Description** | Two issues: **1)** Rate limiting is configured via `RATE_LIMIT_RPS`/`RATE_LIMIT_BURST` with a default of 0, meaning the limiter is nil and requests pass through unrestricted. **2)** The global rate limiter is positioned **before** the `Tenant` middleware in the chain. When `isAllowed` calls `TenantFrom(ctx)`, the tenant hasn't been extracted from the header yet, so it falls back to `"default"`. This means ALL unauthenticated/anonymous requests share the same rate limit bucket — a single noisy tenant can exhaust the global budget for everyone. |
| **Attack Scenario** | An attacker sends a flood of requests. Without rate limiting enabled (default), the server is fully exposed to DoS. With rate limiting configured, an attacker spoofing different `X-Aero-Tenant` headers would get separate rate-limit buckets only if the tenant was extracted before rate limiting — but since it isn't, the attack still shares the single bucket. |
| **Impact** | Denial of service. No rate limiting in default config. |
| **Recommendation** | Apply sensible defaults (e.g., `RATE_LIMIT_RPS=100`). Move the rate-limit middleware **after** the `Tenant` middleware in the chain so per-tenant rate limiting actually works. Document the per-tenant behavior. |
| **Effort** | S |

---

### [MEDIUM-8] Weak JWT Key Management — Single Symmetric Secret

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | JWT uses HS256 with a single shared secret; no key rotation |
| **Location** | `internal/auth/jwt.go:37-42` — `NewJWTVerifier` stores secret as `[]byte`; `internal/auth/jwt.go:61-73` — signature uses HMAC-SHA256 |
| **Description** | JWT tokens are signed with HMAC-SHA256 (HS256) using a single symmetric secret (`AUTH_JWT_SECRET`). HS256 means any party that can verify a token can also forge one, since the signing and verification keys are identical. The code acknowledges this ("To roll forward to RS256/JWKS") but doesn't implement it. Additionally, there's no key rotation mechanism: changing `AUTH_JWT_SECRET` invalidates ALL existing tokens. |
| **Attack Scenario** | An attacker who compromises the `AUTH_JWT_SECRET` environment variable (via file read, process dump, config leak) can forge arbitrary JWT tokens with any tenant, scopes, and expiry. Since HS256 uses the same key for signing and verification, there's no separation of duties. |
| **Impact** | Complete authentication bypass for JWT-authenticated endpoints. |
| **Recommendation** | Consider RS256 (RSA) or Ed25519 for the JWT signing key to separate the signing key (server-side only) from public verification keys. Add support for key rotation (multiple valid issuers with different key IDs). Until then, document the risk and recommend short token TTLs and `AUTH_JWT_SECRET` rotation procedures. |
| **Effort** | L |

---

### [MEDIUM-9] No TLS by Default (Application Layer)

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Cryptography |
| **Severity** | **Medium** |
| **Title** | Server does not terminate TLS |
| **Location** | `cmd/server/main.go:260-270` — `http.Server` constructed without TLS config; `runServer` calls `ListenAndServe` (not `ListenAndServeTLS`) |
| **Description** | The HTTP server does not terminate TLS. All traffic between clients and server is in plaintext. While it's common to terminate TLS at a reverse proxy (nginx, envoy) in production, the server has no built-in support for HTTPS. The `.env.example` does not document TLS termination expectations. |
| **Attack Scenario** | Traffic on the wire between client and server is unencrypted. API keys, JWT tokens, uploaded objects, and admin commands are visible to any network observer on the same segment. |
| **Impact** | Credential and data exposure in transit. Non-compliance with regulations requiring encryption in transit (GDPR Art. 32, PCI DSS 4.1). |
| **Recommendation** | Document TLS expectations in README with example proxy configs (nginx, Caddy). Optionally add built-in TLS support via `ListenAndServeTLS` with config-provided cert paths for simpler deployments. Add `.env.example` notes on TLS. |
| **Effort** | S (documentation) / M (built-in TLS) |

---

### [MEDIUM-10] Cookie-less WebUI Stores API Key in `localStorage`

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Session Management |
| **Severity** | **Medium** |
| **Title** | Web UI stores API key in localStorage without encryption; no HttpOnly/Secure flags |
| **Location** | `internal/webui/static/index.html` (entire file) |
| **Description** | The Web UI stores the API key and tenant in `localStorage` (persisted until explicitly cleared). `localStorage` is accessible to any JavaScript executing on the same origin. While the UI uses `textContent` (mitigating stored XSS), an XSS vulnerability anywhere on the same origin (including the Swagger UI at `/docs` or a user-uploaded HTML file served by the API) could extract the API key. No `HttpOnly` or `Secure` flags are possible with `localStorage` (they only apply to cookies). |
| **Attack Scenario** | If an attacker finds an XSS vector (e.g., a metadata value rendered unsafely in a future feature), they can read `localStorage` and exfiltrate the API key. Even without XSS, any extension or script on the same origin has access. |
| **Impact** | API key theft via client-side attack. |
| **Recommendation** | Use `HttpOnly` + `Secure` + `SameSite=Strict` cookies for API key storage instead of `localStorage`. This prevents JavaScript access entirely. Alternatively, add a Content-Security-Policy that restricts script execution and a note in the Web UI warning that the key is stored insecurely. |
| **Effort** | M |

---

### [LOW-11] Tenant ID Not Validated for Malicious Input

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | Tenant ID from `X-Aero-Tenant` header not sanitized |
| **Location** | `internal/middleware/middleware.go:48-55` — `Tenant` middleware; `internal/service/file.go:200` — `storageKey` uses tenant in path join |
| **Description** | The tenant ID is extracted from the `X-Aero-Tenant` header and used directly in path construction: `storageKey(tenant, bucket, key)` → `path.Join(tenant, bucket, key)`. While the local storage backend's `objectPath` validates the final path against traversal (`..`), the tenant ID itself is not validated for character set or length. A tenant name like `../../etc` would be rejected by `objectPath`, but only after reaching the storage layer. In cloud backends (S3, OSS, COS), the tenant may be used as a prefix in object keys without traversal checks. |
| **Attack Scenario** | A user with a tenant named `../../../etc/passwd` (if the auth provider allows it) could cause the storage key to resolve to an unexpected path. With cloud storage backends, the tenant might create objects with keys that collide or override other tenants' objects in shared buckets. |
| **Impact** | Limited: local FS has defense-in-depth via `objectPath`. Cloud backends depend on their own key handling. |
| **Recommendation** | Add tenant ID validation: reject characters like `/`, `..`, `\`, `%00` (null byte). Consider a regex like `^[a-zA-Z0-9._-]{1,64}$` on the tenant ID. Validate at the middleware layer before storing on context. |
| **Effort** | S |

---

### [LOW-12] No Input Size Limits on Non‑AI Request Bodies

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **Low** |
| **Title** | No limit on request body size for admin endpoints |
| **Location** | `internal/api/rest/admin.go` — most admin handlers use `json.NewDecoder(r.Body).Decode(&body)` without `http.MaxBytesReader`; `internal/api/rest/handler.go:82` — `PostForm` uses `ParseMultipartForm(32 << 20)` (32 MiB limit) |
| **Description** | Most admin endpoints accept unbounded request bodies. While JSON decoding will eventually fail for huge payloads due to resource limits, an attacker could send a multi-gigabyte JSON payload to exhaust server memory. Only the `PostForm` handler has a memory limit via `ParseMultipartForm` (32 MiB). The admin and AI request handlers lack `http.MaxBytesReader`. |
| **Attack Scenario** | An authenticated (or anonymous, if auth is disabled) attacker sends a POST to `/v1/admin/keys` with a 2GB JSON body. The server attempts to decode it, exhausting available memory and causing a crash or OOM kill. |
| **Impact** | Denial of service via memory exhaustion. |
| **Recommendation** | Wrap request bodies with `http.MaxBytesReader(w, r.Body, maxSize)` for all endpoints. Set a sensible limit (e.g., 1 MiB for config payloads, 10 MiB for search queries). |
| **Effort** | S |

---

### [INFO-13] Middleware Chain Order Mismatches Documentation

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Info** |
| **Title** | Middleware chain order differs from documented invariant |
| **Location** | `cmd/server/main.go:236-253` (code) vs `AGENTS.md` §2.5 (documentation) |
| **Description** | The documentation states: `RequestID → CORS → Auth → Tenant → RateLimit(global) → OTel → Recoverer → AccessLog`. The actual runtime order from outermost to innermost (first to last to process a request) is: `AccessLog → Concurrency → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID`. This means:
- Rate limiting runs **before** tenant extraction → all tenants share one bucket
- Auth runs **before** CORS → CORS preflight requests require authentication
- RequestID runs **last** → auth failures don't get request IDs in responses |
| **Attack Scenario** | CORS preflight (OPTIONS) requests fail authentication for cross-origin callers, breaking browser-based SDK usage. Request IDs are not set for auth failures, making incident response harder. Rate limiting is not per-tenant. |
| **Impact** | Operational issues: CORS failures for JS SDKs, inaccurate access logs, ineffective per-tenant rate limiting. |
| **Recommendation** | Reorder the middleware chain to match the documented invariant. Specifically: move `RequestID` to outermost, `CORS` before `Auth`, and `Tenant` before `RateLimit`. |
| **Effort** | S |

---

### [INFO-14] Web UI CSP and Security Headers

| Field | Value |
|-------|-------|
| **Category** | Compliance / Web Security |
| **Severity** | **Info** |
| **Title** | Web UI index.html has no `<meta>` CSP tag |
| **Location** | `internal/webui/static/index.html` |
| **Description** | The Web UI's `index.html` does not include a `<meta http-equiv="Content-Security-Policy">` tag. While the server could add CSP via HTTP headers (see HIGH-3), the HTML itself provides no fallback protection. No CSP means inline scripts (the UI has many inline `<script>` blocks) are permitted, but so is any injected script if an XSS vector is found. |
| **Attack Scenario** | If a future change adds `innerHTML` rendering of object content (e.g., preview pane), the CSP would not restrict script execution. |
| **Impact** | No defense-in-depth against XSS. |
| **Recommendation** | Add a `<meta>` CSP tag as a fallback: `content="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"`. Since the UI has inline scripts, `'unsafe-inline'` is needed unless scripts are moved to a separate file. |
| **Effort** | S |

---

## STRIDE Analysis

| Threat | Finding | Severity |
|--------|---------|----------|
| **Spoofing** | Auth disabled by default (CRITICAL-2); JWT HS256 shared secret (MEDIUM-8) | Critical |
| **Tampering** | Presigned URL verification missing (CRITICAL-1) — object integrity not validated via presign; ETag verification opt-in only | Critical |
| **Repudiation** | Audit logging is thorough for admin actions; access logging exists without request IDs for auth failures (INFO-13); no event hash chains | Low |
| **Information Disclosure** | Error messages leak details (HIGH-5); API key in URL logs (HIGH-4); presigned URLs in logs but feature non-functional; tenant in path without validation (LOW-11) | High |
| **Denial of Service** | Rate limiting disabled by default (MEDIUM-7); no body size limits on endpoints (LOW-12); `rlMaxBuckets` caps tenant map at 50k (good) | Medium |
| **Elevation of Privilege** | Tenant tenant bypass via path (LOW-11); HS256 key leakage → full JWT forgery (MEDIUM-8); admin scope not checked if auth disabled (CRITICAL-2) | Critical |

---

## OWASP Top 10 (2021) Coverage

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| **A01: Broken Access Control** | ❌ **FAIL** | Auth disabled by default (CRITICAL-2); presigned URL verification missing (CRITICAL-1) |
| **A02: Cryptographic Failures** | ⚠️ **WEAK** | HS256 shared key (MEDIUM-8); no TLS (MEDIUM-9); AES-256-GCM is good |
| **A03: Injection** | ✅ **PASS** | Parameterized SQL via `rebind`; no command injection; no eval() |
| **A04: Insecure Design** | ⚠️ **WEAK** | Presigned URL design incomplete (CRITICAL-1); middleware order mismatch (INFO-13) |
| **A05: Security Misconfiguration** | ❌ **FAIL** | No security headers (HIGH-3); auth disabled by default (CRITICAL-2); CORS preflight needs auth (INFO-13) |
| **A06: Vulnerable Components** | ⚠️ **NEEDS REVIEW** | Go stdlib + chi + minio-go — audit `go.mod` for known CVEs |
| **A07: Identification/Auth Failures** | ❌ **FAIL** | Auth disabled by default; API key in URL (HIGH-4); JWT HS256 shared key (MEDIUM-8) |
| **A08: Software/Data Integrity** | ⚠️ **WEAK** | ETag verification opt-in; no checksum validation on hard delete |
| **A09: Security Logging/Monitoring** | ⚠️ **ADEGUATE** | Audit logging exists; access logging without request ID for auth failures; no log redaction |
| **A10: SSRF** | ⚠️ **WEAK** | Webhook URL not validated (MEDIUM-6); copy source header validated only for path, not host |

---

## Overall Security Debt

### Top 3 Critical Issues

1. **Presigned URL verification is not wired (CRITICAL-1)** — Generated URLs are never validated. The feature is non-functional and a security trap.
2. **Authentication disabled by default (CRITICAL-2)** — Any deployment using default config has zero access control.
3. **Security headers completely absent (HIGH-3)** — Web UI is vulnerable to clickjacking; MIME-sniffing risk for uploaded content.

### Top 3 Quick Wins (High Impact, Low Effort)

1. **Add security headers middleware** — 30 lines of Go code to stamp `X-Frame-Options`, `X-Content-Type-Options`, `CSP`, and `Referrer-Policy`. This fixes HIGH-3.
2. **Wrap admin/AI endpoints with `http.MaxBytesReader`** — Prevent memory exhaustion DoS. 10 lines per handler.
3. **Add `AUTH_FORCE=true` warning on startup** — When auth is disabled, log a prominent `[SECURITY]` warning with instructions to configure `AUTH_KEYS`. This mitigates CRITICAL-2 operator error.

### Security Debt Items

| Item | Effort | Priority |
|------|--------|----------|
| Wire presigned URL verification handler | M | Critical |
| Add security headers middleware | S | High |
| Move API key revocation to request body | S | High |
| Sanitize error messages for 5xx responses | S | High |
| Validate webhook URLs against SSRF | M | Medium |
| Reorder middleware chain (tenant before rate-limit) | S | Medium |
| Set sensible default rate limits | S | Medium |
| Add built-in TLS support | M | Medium |
| Migrate JWT to asymmetric signing (RS256/Ed25519) | L | Medium |
| Add per-API-key rate limiting | L | Low |

---

**Final Verdict: Needs Improvement — 2 Critical, 3 High, 4 Medium, 2 Low, 2 Info findings.** The cryptographic primitives and SQL handling are well-implemented, but the authentication model, presigned URL implementation, and several operational security controls have significant gaps that must be addressed before production deployment in an adversarial environment.
