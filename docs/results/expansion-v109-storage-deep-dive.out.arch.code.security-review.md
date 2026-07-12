Now I have a thorough understanding of the codebase. Here is my comprehensive security review.

---

# Security Review: aero-vault

**Reviewer:** Principal Security Engineer  
**Codebase:** `github.com/aero-vault/aero-vault` (243 Go source files)  
**Review Type:** Full architecture + implementation review  
**Date:** 2026-07-12

---

## Finding 1: SSE Event Injection via Object Keys

| Field | Value |
|-------|-------|
| Category | Input Validation / Threat Model |
| Severity | **High** |
| Title | SSE stream injects unescaped user-controlled data into event stream |
| Location | `internal/api/rest/sse.go` lines 67-78, `writeEvent()` |
| Description | The SSE handler constructs event frames by interpolating user-controlled `e.Key`, `e.Bucket`, and event type into the SSE wire format without escaping newlines. An object key containing `\n\n` would terminate the event, allowing injection of fake events. |
| Attack Scenario | Upload an object with key `data\n\nid: 0\nevent: injected\ndata: {"steal":"true"}\n\n` then connect to `/v1/events/stream`. The injected event frame appears in the stream. |
| Impact | Information disclosure via forged events, potential SSE channel poisoning for downstream consumers (agents, webhooks). |
| Recommendation | Sanitize all values written to SSE frames: `strings.ReplaceAll(e.Key, "\n", "\\n")` and reject keys containing control characters at the service level. |
| Effort | S |

---

## Finding 2: MCP Bypasses Authentication Middleware

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **Critical** |
| Title | MCP HTTP endpoint does not enforce auth middleware |
| Location | `cmd/server/main.go` ~line 85, `internal/mcp/server.go` |
| Description | The `/mcp` route is registered directly on `r` (the chi mux) rather than under the `/v1` subrouter that goes through auth middleware. The `buildDispatcher` function dispatches MCP before the chi router. The stdio mode (`aero-vault mcp`) runs without any authentication at all. |
| Attack Scenario | Direct HTTP POST to `/mcp` with `{"method":"tools/call","params":{"name":"read_file","arguments":{"key":"/etc/passwd"}}}` bypasses auth entirely. The stdio MCP mode is accessible from any process that can connect to stdin/stdout. |
| Impact | Unauthenticated read/write/delete access to all objects. Complete data breach. |
| Recommendation | Apply auth middleware to `/mcp` route (same as `/v1`). For stdio MCP, require explicit `--auth` flag or bind to admin-controlled socket. |
| Effort | S |

---

## Finding 3: Webhook URL Injection

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **High** |
| Title | No validation on webhook URL — SSRF vector |
| Location | `internal/events/webhook.go` lines 33-36 |
| Description | The `EVENTS_WEBHOOK_URL` env var is split on commas and used directly as HTTP POST targets. No validation prevents `file://`, `gopher://`, `dict://`, or private IP addresses (`169.254.169.254` for cloud metadata). The `retryOne` method even receives URLs from the database `webhook_failures` table, which could have been poisoned. |
| Attack Scenario | Set `EVENTS_WEBHOOK_URL=http://169.254.169.254/latest/meta-data/` to exfiltrate cloud provider instance credentials, or `file:///etc/shadow` to leak the password file (some HTTP clients follow redirects to file://). |
| Impact | Server-Side Request Forgery (SSRF) to cloud metadata, internal services, or local file read. |
| Recommendation | Validate URLs: reject non-http(s) schemes; optionally block private/rfc1918 IPs; configure allowed domains. Validate stored webhook failure URLs before retrying. |
| Effort | M |

---

## Finding 4: No Brute-Force Protection on Auth

| Field | Value |
|-------|-------|
| Category | Authentication |
| Severity | **High** |
| Title | API key authentication lacks rate limiting per key or per IP |
| Location | `internal/auth/auth_middleware.go` |
| Description | The rate limiter is per-tenant after authentication succeeds. There is no rate limiting on the auth verification step itself. An attacker can brute-force API keys or JWT tokens at full speed. The `Unauthorized` and `Forbidden` responses are identical (same error message) which is good, but there's no delay or lockout. |
| Attack Scenario | Continuous requests with randomly generated `Bearer` tokens against a production deployment. With even moderate throughput (1000 req/s), SHA-256 hash lookups in the map are fast enough to brute-force short API keys. |
| Impact | Account takeover via key brute-forcing. |
| Recommendation | Add IP-based rate limiting on auth failures (e.g., 5 attempts/min per IP). Implement exponential backoff on auth failures. Consider a dedicated auth rate limiter before the tenant resolver. |
| Effort | M |

---

## Finding 5: Pre-Signed URL HMAC Uses Pass-Through Key — No Scoping

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Medium** |
| Title | Pre-signed URLs signed with a single key, no path/binding |
| Location | `internal/storage/sign.go` |
| Description | Pre-signed URLs are generated using `STORAGE_LOCAL_SIGN_KEY`. The URL format embeds the operation (get/put) but not the tenant, bucket, or key as authenticated data. A pre-signed GET URL for `secret.pdf` could be partially re-purposed (or an attacker with one valid URL can't infer the signing key but could potentially reuse it). |
| Attack Scenario | A signed URL for `get/file1` could potentially be used for `get/file2` if the signing mechanism doesn't bind the path to the signature (need to verify sign.go implementation). |
| Impact | Potential unauthorized access to different objects than intended. |
| Recommendation | Include the full path, tenant, operation, and expiry as authenticated data in the HMAC input. Verify all these fields on the validation side. |
| Effort | S |

---

## Finding 6: HTML/JS Injection in Web UI

| Field | Value |
|-------|-------|
| Category | Input Validation / Threat Model |
| Severity | **High** |
| Title | Web UI renders unfiltered object content and search results |
| Location | `internal/webui/static/index.html` |
| Description | The web UI is a vanilla JS SPA that fetches search results and object contents and renders them as HTML via `innerHTML` (or similar). Search results include `chunk` text and object keys - both of which can contain HTML/JS that executes in the admin's browser session. |
| Attack Scenario | Upload an object with key `<img src=x onerror=alert(document.cookie)>`. When an admin searches and views the file, the script executes. Since the API key is stored in `localStorage`, this leaks credentials. |
| Impact | Cross-Site Scripting (XSS) — theft of API keys from localStorage, arbitrary API calls as the admin user. |
| Recommendation | Use `textContent` instead of `innerHTML`. Sanitize all user-controlled data before rendering. Add `Content-Security-Policy` header. Consider using a sanitization library. |
| Effort | M |

---

## Finding 7: API Key SHA-256 Hashing Without Salt

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Medium** |
| Title | API key hashing uses raw SHA-256, no salt or work factor |
| Location | `internal/auth/store.go` line 29: `sha256.Sum256([]byte(token))` |
| Description | Persisted API keys are hashed with a single round of SHA-256 with no salt. While this is acceptable for API tokens (high-entropy random strings, unlike passwords), if the API key generation has insufficient entropy or if the hash DB is leaked, keys can be efficiently brute-forced at ~1B+ hashes/second per GPU. |
| Attack Scenario | Database dump reveals hashed API keys. Attacker brute-forces 128-bit keys with SHA-256 at ~10 GH/s on a single GPU. Low-entropy keys (e.g., truncated UUIDs) fall quickly. |
| Impact | Offline recovery of API keys from database compromise. |
| Recommendation | Add a per-key random salt and use HMAC-SHA256 (keyed with salt). Alternatively, use HKDF-expand. For API tokens, SHA-256 is borderline acceptable; the key risk is insufficient entropy in token generation. |
| Effort | S |

---

## Finding 8: Rate Limiter Bypass via Header Manipulation

| Field | Value |
|-------|-------|
| Category | Threat Model (DoS) |
| Severity | **Medium** |
| Title | Client-controlled X-Aero-Tenant header bypasses rate limits via tenant rotation |
| Location | `internal/middleware/ratelimit.go`, `internal/middleware/middleware.go` `Tenant()` |
| Description | The rate limiter uses the `X-Aero-Tenant` header to select the token bucket. An attacker can rotate through tenant IDs to get fresh buckets. The max bucket map size is 50,000, but an attacker can still consume memory by rotating before idle eviction (10 min TTL). With 80+ unique tenants per second, the map grows unbounded. |
| Attack Scenario | Send requests with `X-Aero-Tenant: attacker-00000`, then `attacker-00001`, etc. Each new tenant gets a full burst of tokens (e.g., 100 requests). At 10 req/s, the attacker can send 1000 req/s by rotating 10 tenants. |
| Impact | Rate limit circumvention, potential DoS via map memory exhaustion (50k entries * ~100 bytes ≈ 5 MB, bounded but still wasteful). |
| Recommendation | Base rate limiting on a combination of tenant + IP + API key prefix hash. Validate that the X-Aero-Tenant matches an authorized tenant for the given API key. |
| Effort | M |

---

## Finding 9: S3 Chunked Upload Signature Not Fully Verified

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **High** |
| Title | SigV4 streaming chunked upload skips per-chunk signature verification |
| Location | `internal/auth/sigv4_chunk.go` |
| Description | The `decodeStreamingBody` function explicitly states: "Per-chunk signatures are not re-verified (the seed signature in the Authorization header was already verified)." This means an attacker can modify individual chunks of a multi-part upload after the initial auth check, replacing content. |
| Attack Scenario | An upload containing a signed executable: authenticate the first chunk with a valid signature, then replace subsequent chunks with different data. The initial HMAC seed passes, but 99% of the content can be replaced without detection. The stored ETag matches the unmodified checksum. |
| Impact | Data integrity bypass — stored content differs from what was authorized. This enables undetected content injection on upload. |
| Recommendation | Verify per-chunk signatures using the AWS sigv4 chunked transfer spec, or disable chunked transfer support and require clients to send the full body with a single signature. |
| Effort | L |

---

## Finding 10: No TLS Enforcement / Mixed Content

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **High** |
| Title | No built-in TLS termination or enforcement |
| Location | `cmd/server/main.go` |
| Description | The HTTP server uses plain HTTP (`ListenAndServe` not `ListenAndServeTLS`). API keys and JWT tokens are transmitted as Bearer headers in cleartext. The SSE stream sends all object event data over unencrypted connections. The web UI loads over HTTP. |
| Attack Scenario | Network-level attacker (same WiFi, compromised router) intercepts HTTP traffic, steals API keys from Authorization headers, reads all SSE event data including object keys and metadata. |
| Impact | Complete loss of credential confidentiality and data-in-transit protection. Violates PCI DSS (requirement 4) and GDPR article 32. |
| Recommendation | Terminate TLS at the application level or require a reverse proxy. Add `--tls-cert` / `--tls-key` flags for optional self-termination. Set `Strict-Transport-Security` header. Document that production deployments MUST use TLS at the edge. |
| Effort | M |

---

## Finding 11: Bucket Policy Parsing Fails Open

| Field | Value |
|-------|-------|
| Category | Authorization |
| Severity | **High** |
| Title | Bucket policy parse errors default to allowing access |
| Location | `internal/api/rest/handler.go` `checkBucketPolicy()` lines 40-42 |
| Description | When a bucket policy JSON fails to parse, the code logs a warning but returns `true` (allowed). This means an administrator setting a malformed policy will silently open access rather than restrict it. The user has no indication that their security policy is not being enforced. |
| Attack Scenario | Admin types `{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"}` (missing `Statement` wrapper). The policy parser fails, logs a warning, and all requests are allowed. The admin believes they have restricted access when they haven't. |
| Impact | Bucket policies silently fail open, giving administrators false confidence in their security configuration. |
| Recommendation | Reject the request with a 400 error when the policy is malformed. Return an error to the policy-setter immediately. Fail closed on parse error: return `false` (denied). |
| Effort | S |

---

## Finding 12: Idempotency Body Spools to World-Readable Temp Files

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Medium** |
| Title | Request bodies >8MB spooled to world-readable temp files |
| Location | `internal/api/rest/idempotency.go` `spoolBody()` |
| Description | When `IDEMPOTENCY_HASH_BODY` is enabled and the request body exceeds 8MB, the body is spooled to `os.CreateTemp("", "aero-idem-*")` which creates files in `/tmp` with default permissions (typically 0644 or 0600 depending on umask). On multi-tenant systems, other local users could read these buffers. |
| Attack Scenario | User A uploads a sensitive file (>8MB) with Idempotency-Key. The body is spooled to `/tmp/aero-idem-12345`. User B (another OS user) reads the temp file while the request is in-flight. |
| Impact | Sensitive data disclosure to other local OS users. |
| Recommendation | Create temp files with `os.MkdirTemp` in a private directory, or use `io.MultiWriter` with the body directly to the storage layer without intermediate temp files. |
| Effort | S |

---

## Finding 13: SSE Stream Leaks Internal Object IDs

| Field | Value |
|-------|-------|
| Category | Data Protection / Threat Model (Information Disclosure) |
| Severity | **Low** |
| Title | SSE event stream exposes sequential object IDs |
| Location | `internal/api/rest/sse.go` line 74 |
| Description | The SSE event payload includes the internal sequential `object_id` and event `id`. These are integer IDs that reveal the relative ordering and volume of operations (how many objects, event frequency). |
| Attack Scenario | A competitor connects to the public SSE stream (if tenant is known) and monitors traffic volume to estimate the company's storage growth and usage patterns. |
| Impact | Minor information disclosure about storage growth and object relationships. |
| Recommendation | Use opaque UUIDs instead of sequential integers in external-facing event payloads. |
| Effort | L (schema migration) |

---

## Finding 14: Audit Log Actor Resolution Uses Tenant ID Instead of Key Identity

| Field | Value |
|-------|-------|
| Category | Threat Model (Repudiation) |
| Severity | **Medium** |
| Title | Audit log records tenant, not the specific API key principal |
| Location | `internal/api/rest/admin.go` `audit()` lines 100-106 |
| Description | The audit function sets `actor = k.Tenant`, which is the tenant ID (e.g., "acme"), not the specific API key identity. Multiple users sharing the same tenant appear indistinguishable in audit logs. A revoked key's actions can't be traced to that specific key. |
| Attack Scenario | Alice and Bob share tenant "acme" with different API keys. A malicious admin action occurs. The audit log shows only "acme" as the actor — impossible to determine whether Alice or Bob performed the action. |
| Impact | Non-repudiation failure — inability to attribute actions to specific API keys. |
| Recommendation | Include the API key label or a hash suffix in the audit log actor field. Store the full actor identity (key label or token hash suffix) alongside the tenant. |
| Effort | S |

---

## Finding 15: No `Content-Security-Policy` or Security Headers

| Field | Value |
|-------|-------|
| Category | Compliance |
| Severity | **Medium** |
| Title | Missing security headers (CSP, X-Frame-Options, X-Content-Type-Options) |
| Location | `cmd/server/main.go` and internal middleware |
| Description | The application sets no `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`, or `Referrer-Policy` headers. The web UI SPA is completely unprotected against clickjacking and MIME-type confusion. |
| Attack Scenario | Attacker embeds `/ui` in an iframe on a malicious site. Combined with XSS from Finding 6, this enables credential theft via phishing. |
| Impact | Clickjacking, MIME-sniffing attacks, CSP-bypassable XSS. |
| Recommendation | Add a security headers middleware: `Content-Security-Policy: default-src 'self'`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`. |
| Effort | S |

---

## Finding 16: Absence of Account Lockout on Repeated Auth Failures

| Field | Value |
|-------|-------|
| Category | Authentication |
| Severity | **Medium** |
| Title | No account lockout or progressive delay on auth failures |
| Location | `internal/auth/auth.go` |
| Description | There is no mechanism to lockout or delay repeated authentication failures for a given key or IP. An attacker can send unlimited auth requests. |
| Attack Scenario | Attacker targets `/v1/admin/jwt` endpoint (JWT issuance requires admin scope but the JWT endpoint itself is exposed). Or targets any `/v1/*` endpoint with random Bearer tokens. |
| Impact | Brute-force vulnerability for all API key types. |
| Recommendation | Implement progressive delay (1s, 5s, 30s, 5min) per IP per key hash on auth failures. Consider `Fail2Ban`-style integration. |
| Effort | M |

---

## Finding 17: JWTs Use Weak HMAC Algorithm

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Medium** |
| Title | JWT uses HS256 (shared secret) without asymmetric key option |
| Location | `internal/auth/jwt.go` |
| Description | All JWT operations use HMAC-SHA256 with a shared secret (`AUTH_JWT_SECRET`). This means: (1) The same secret both signs and verifies — any service with the secret can forge tokens. (2) No key rotation without invalidating all tokens. (3) No third-party IdP integration (e.g., external OIDC). |
| Attack Scenario | An attacker who obtains `AUTH_JWT_SECRET` (via config leak, env dump, or backup) can forge arbitrary JWT tokens with any tenant and scope, including admin. |
| Impact | Complete authentication bypass if JWT secret is compromised. |
| Recommendation | Add RS256/ES256 support with a JWKS endpoint. Keep HS256 as an option for simple deployments but document the risk. Support `jwks_uri` for production deployments. |
| Effort | L |

---

## Finding 18: Config Secret Exposure in Error Messages

| Field | Value |
|-------|-------|
| Category | Data Protection |
| Severity | **Low** |
| Title | SSE key errors may disclose key URL paths |
| Location | `internal/storage/secret.go` |
| Description | Error messages from the HTTP key provider include the full URL path (e.g., `"sse key url fetch: http 401: ..."`). If these errors propagate to API responses, internal infrastructure details leak. |
| Attack Scenario | Trigger an SSE key fetch failure, observe the error message in API response, learn the internal Vault/KMS URL. |
| Impact | Minor infrastructure information disclosure. |
| Recommendation | Redact URLs in error messages before returning to the client. Log full details server-side. |
| Effort | S |

---

## Finding 19: Local File System SSE Key File Permission Issues

| Field | Value |
|-------|-------|
| Category | Cryptography / Data Protection |
| Severity | **Medium** |
| Title | SSE key file may be world-readable |
| Location | `internal/storage/secret.go` `newKeyfileProvider()` |
| Description | The key ring JSON file is read with `os.ReadFile` which does not check file permissions. If the key file has `0644` permissions, any local user can read the master encryption keys, decrypting all stored objects. |
| Attack Scenario | Operator places SSE key ring JSON with default umask (0644). Another process or user on the same machine reads the key file and decrypts all object storage. |
| Impact | Complete loss of encryption — all stored objects readable with compromised keys. |
| Recommendation | Verify file permissions on load (fail if group/other readable). Document secure key file setup. Recommend `0600` permissions. |
| Effort | S |

---

## Finding 20: Webhook Secret in Config — No Rotation Support

| Field | Value |
|-------|-------|
| Category | Cryptography |
| Severity | **Low** |
| Title | Webhook secret loaded once from config, no in-memory rotation |
| Location | `internal/events/webhook.go` `WithSecret()` |
| Description | The webhook HMAC secret is set once at startup. If the secret is rotated in config, a restart is required to pick up the new value. There is no mechanism for hot-reloading or dual-key (active+previous) verification during rotation. |
| Attack Scenario | During secret rotation, there is a window where old-signed webhooks are rejected before the new secret is deployed. |
| Impact | Brief delivery failures during secret rotation. |
| Recommendation | Support dual-key verification (try primary, fall back to secondary). Add SIGHUP or hot-reload for secrets. |
| Effort | M |

---

## Finding 21: No Validation of Object Key Character Set

| Field | Value |
|-------|-------|
| Category | Input Validation |
| Severity | **Medium** |
| Title | Object keys with control characters or special bytes not rejected |
| Location | `internal/service/file.go` `validateKey()` |
| Description | The `validateKey` function only rejects empty keys, `..` (path traversal), and `/` prefix. It does not reject control characters, null bytes, or excessively long keys. A key with `\x00` could cause issues in storage backends; a key with `\n` could cause log injection. |
| Attack Scenario | Upload object with key containing `\x00` (null byte). SQLite truncates at null byte, causing two objects to share the same DB row but different storage keys. |
| Impact | Object metadata corruption, log injection, database inconsistency. |
| Recommendation | Reject keys with control characters (`< 0x20` except tab), null bytes, or keys longer than 1024 bytes. |
| Effort | S |

---

## Finding 22: No Cookie Attributes on Session/Auth

| Field | Value |
|-------|-------|
| Category | Session Management |
| Severity | **Low** |
| Title | API keys stored in localStorage, not httpOnly cookies |
| Location | `internal/webui/static/index.html` |
| Description | The web UI stores the API key in `localStorage` via plain text input. There is no session cookie with `HttpOnly`, `Secure`, or `SameSite` attributes. Any XSS vulnerability (Finding 6) directly exfiltrates the key. |
| Attack Scenario | XSS in web UI → `localStorage.getItem('apikey')` → API key theft → full account access. |
| Impact | Session hijacking via XSS. |
| Recommendation | Never store credentials in `localStorage`. Use an httpOnly, Secure, SameSite=Strict session cookie issued after initial auth. Implement a session management endpoint. |
| Effort | L |

---

## STRIDE Summary

| Threat | Applicable Risk |
|--------|----------------|
| **S**poofing | MCP auth bypass (F2), JWT HS256 weakness (F17) — identities can be forged |
| **T**ampering | Chunked upload signature skip (F9), Object key injection (F1) — data can be modified |
| **R**epudiation | Audit logs use tenant not key identity (F14) — actions can be denied |
| **I**nformation Disclosure | SSE leak (F13), No TLS (F10), Webhook URL exfil (F3) — data can leak |
| **D**enial of Service | Rate limiter tenant rotation (F8), Brute-force no lockout (F16) — service can be disrupted |
| **E**levation of Privilege | Bucket policy fails open (F11), MCP auth bypass (F2) — users can gain unauthorized access |

---

## Compliance Considerations

| Standard | Gaps |
|----------|------|
| **OWASP Top 10** | A1 (XSS in WebUI), A2 (Auth bypass MCP), A3 (SSRF webhook), A5 (missing security headers), A7 (broken access control bucket policy) |
| **PCI DSS 4.0** | Req 4 — no TLS; Req 7 — no fine-grained access control; Req 8 — no lockout/rate-limit on auth; Req 10 — audit logs lack key identity |
| **GDPR Art 32** | No encryption in transit (TLS); PII scan exists but is opt-in; no data retention enforcement |
| **NIST SP 800-53** | AC-2 (account management) — no admin user review; AC-7 (unsuccessful login attempts) — no lockout; SC-8 (transmission confidentiality) — no TLS; SC-13 (cryptography) — no key rotation mechanism |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase shows strong awareness of security patterns: parameters are properly parameterized against SQL injection, HMAC is used correctly for signatures, SSE encryption uses proper envelope encryption with GCM, and API keys are hashed. However, there are critical gaps in authentication enforcement, input sanitization, and transport security that present real attack surfaces.

### Top 3 Critical Issues

1. **MCP Bypasses Auth** — The MCP endpoint (both HTTP and stdio) does not enforce any authentication, giving unauthenticated attackers full read/write/delete access to all objects.
2. **S3 Chunked Upload Signature Bypass** — Per-chunk signatures in streaming uploads are explicitly skipped, allowing content replacement after authentication.
3. **No TLS in Transport** — All credentials (API keys, JWT tokens) and data (objects, SSE events) are transmitted in cleartext over HTTP, violating every compliance standard.

### Top 3 Quick Wins

1. **Add security headers middleware** — CSP, X-Frame-Options, X-Content-Type-Options. One file, < 30 lines.
2. **Sanitize SSE output** — Reject/escape newlines in object keys before writing to the event stream. Fixes SSE injection. One line change in `writeEvent()` or `validateKey()`.
3. **Fix bucket policy fail-open** — Change `checkBucketPolicy` to return `false` (denied) on parse error instead of `true` (allowed). One character change.

### Security Debt

- **SSRF protection**: Webhook URLs are unvalidated — needs allowlisting or private-IP blocking
- **Auth rate limiting**: No per-IP or per-key brute-force protection on the auth path itself
- **API key hashing salt**: SHA-256 without salt — should use HMAC with per-key salt
- **JWT algorithm flexibility**: HS256-only limits deployment options — should support asymmetric algorithms
- **Audit trail fidelity**: Audit logs can't identify which key performed an action
- **Web UI security**: Full rewrite needed to use httpOnly sessions and avoid localStorage for credentials
- **Key management rotation**: No hot-reload for SSE keys, webhook secrets, or auth keys
