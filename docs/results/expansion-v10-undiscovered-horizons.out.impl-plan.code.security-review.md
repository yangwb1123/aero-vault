Now I have a thorough understanding of the codebase. Let me compile the full security review.

---

# Security Review: aero-vault

## Executive Summary

I have performed a comprehensive security review of the aero-vault object storage system. The codebase shows strong security awareness in several areas — envelope encryption with AES-256-GCM, HMAC-signed webhooks, tenant isolation in storage keys, and a structured auth middleware chain. However, there are significant gaps in authentication perimeter coverage, input validation, and cryptographic posture that represent real-world exploit vectors.

**Total findings: 18** (3 Critical, 5 High, 6 Medium, 3 Low, 1 Info)

---

## Detailed Findings

### Finding 1: MCP stdio Mode Has Zero Authentication

| Field | Value |
|-------|-------|
| **Category** | Authentication / Authorization |
| **Severity** | **Critical** |
| **Title** | MCP stdio server bypasses all auth controls |
| **Location** | `cmd/server/main.go:111-184` (`runMCP()`), `internal/mcp/server.go` |
| **Description** | The MCP stdio server (`aero-vault mcp`) creates a completely separate server over stdin/stdout with no auth middleware. It uses a hardcoded tenant `"default"` and provides the full tool surface: `list_files`, `read_file`, `write_file`, `delete_file`, `search`, and optionally `chat`. Any process that can reach the MCP socket has unrestricted read/write/delete access to all objects. |
| **Attack Scenario** | An attacker who gains access to the host (e.g., via a compromised container, shared CI runner, or sidecar process) connects to the MCP stdio endpoint or exploits a process-launch injection to spawn `aero-vault mcp` with the same DB/storage credentials. They can then read, write, and delete any object without authentication. |
| **Impact** | Complete loss of confidentiality, integrity, and availability for all stored objects. Full data exfiltration and destruction. |
| **Recommendation** | 1. Add required authentication to MCP stdio (e.g., an `AUTH_MCP_TOKEN` env var that the client must pass as the first message, or a Unix socket with filesystem permission controls). 2. Document that stdio MCP inherits the host's trust boundary and must not be exposed across privilege boundaries. 3. Consider adding an explicit `--auth-token` flag. |
| **Effort** | S (add token handshake) |

---

### Finding 2: Missing Bucket/Key Path Traversal Validation

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Critical** |
| **Title** | Storage key path traversal via unvalidated bucket parameter |
| **Location** | `internal/service/file.go:92-98` (`storageKey`), `internal/api/s3compat/handler.go` (`keyFromURL`), `internal/api/rest/handler.go:21-24` (`keyFromPath`) |
| **Description** | The `storageKey()` function joins tenant, bucket, and key using `path.Join()`. Bucket names are sourced from URL parameters (`chi.URLParam(r, "bucket")`) and are **not validated** for path traversal characters like `..`. While `validateKey()` validates the object key (rejecting `..` and `/` prefix), bucket and tenant names are not validated. An S3 request like `GET /s3/../../etc/shadow` or `GET /v1/buckets/../admin` could produce a storage key that escapes the tenant/bucket prefix. | 
| **Attack Scenario** | An authenticated S3 user sends `GET /s3/../other-tenant/secret-file` (using S3 path-style format). The bucket parameter resolves to `".."`. `storageKey("malicious", "..", "other-tenant/secret-file")` → `path.Join("malicious", "..", "other-tenant/secret-file")` → `"other-tenant/secret-file"`. This could read data from a different tenant's namespace. On the local backend, this maps directly to filesystem traversal. |
| **Impact** | Cross-tenant data access (read/write/delete), arbitrary filesystem access on local backend. Complete tenant isolation bypass. |
| **Recommendation** | Validate bucket and tenant names with a strict regex: only allow `[a-zA-Z0-9._-]{1,255}` (following S3 bucket naming conventions). Reject any containing `..`, `/`, or other special characters. Add the validation in `defaults()` and in a new `validateBucket()` function called from every public-facing handler and `FileService` method. |
| **Effort** | S (add regex validation in `file.go` and call it in relevant service methods) |

---

### Finding 3: HS256-only JWT With Shared Secret — Privilege Escalation

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Authorization |
| **Severity** | **Critical** |
| **Title** | Symmetric JWT secret shared across all components allows token forgery |
| **Location** | `internal/auth/jwt.go:1-133`, `cmd/server/main.go:313-317` (`configureAuthSecrets`) |
| **Description** | JWT signing and verification both use HS256 with the same single `AUTH_JWT_SECRET`. Anyone with knowledge of this secret (which must be shared across all replicas) can forge JWTs for any tenant, with any scope, including `admin`. The `admin` JWT issuance endpoint (`POST /v1/admin/jwt`) is gated by admin scope, but an attacker who compromises the secret directly (e.g., via env dump, config leak, memory inspection) can mint tokens without ever calling this endpoint. Furthermore, the JWT has no `jti` (token ID), no `iat` (issued-at) enforcement, and no revocation mechanism — once issued, a token is valid until its `exp` claim. |
| **Attack Scenario** | 1. An attacker obtains `AUTH_JWT_SECRET` via a compromised CI/CD variable, config file, or runtime env dump. 2. They forge a JWT with `"ten": "*"`, `"scopes": ["admin"]`, `"exp": 9999999999`. 3. They use this token to call any admin API: create tenants, issue new API keys, delete all data, or reconfigure systems. |
| **Impact** | Complete compromise: adversary can mint tokens for any tenant, grant themselves admin access, exfiltrate all data, and pivot to infrastructure. |
| **Recommendation** | **Immediate**: Add `jti` to all issued tokens and maintain a server-side deny-list (e.g., in the `audit_log` or a dedicated `revoked_jwt` table). Check deny-list on every `Verify()` call. **Medium-term**: Support RS256/ES256 asymmetric signing where only the public key is available to the server and the private key stays with the trusted IdP. Document that `AUTH_JWT_SECRET` must be treated as a root-of-trust credential (same sensitivity as a database password). |
| **Effort** | **M** (medium: add jti + deny-list support + migration for existing tokens) |

---

### Finding 4: S3 API and REST Endpoints Missing Scope-Enforcement Middleware on Individual Routes

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | S3 and REST sub-routers rely on outer middleware's coarse method-based scope check |
| **Location** | `cmd/server/main.go:231-244` (`applyMiddleware`), `internal/auth/auth_middleware.go:104-118` (`checkScope`), `internal/api/rest/router.go` |
| **Description** | The auth middleware applies a simple method-to-scope mapping: GET/HEAD → `read`, PUT/POST/DELETE → `write`. This is applied at the outermost middleware layer to ALL routes equally. There is no per-route scope enforcement. This means: 1) A key with only `read` scope can call `POST /v1/admin/keys` (mapped as write) — the admin handler has its own `requireAdmin()` check, but a read-scoped key hitting `POST /v1/buckets` would succeed. 2) The granularity is binary (read/write/admin) with no resource-level scoping. 3) Bucket-policy evaluation in S3Compat is the only resource-level control, and it only checks S3 actions (not REST admin operations). |
| **Attack Scenario** | An API key issued with `read` scope can still call `DELETE /v1/buckets/{bucket}` or `POST /v1/batch/delete` because these are HTTP DELETE/POST methods that map to `write` scope. If the key was intended to be read-only, this is a privilege escalation. More critically, an S3 credential with only `read` scope can call `PUT /{bucket}/{key}` to upload arbitrary content. |
| **Impact** | Read-scoped keys can perform write operations. Bucket deletion, object overwrite, and data destruction possible with "read-only" keys. |
| **Recommendation** | 1. Apply `auth.Require(ScopeAdmin)` middleware to all admin routes (`/v1/admin/*`) explicitly in the route registration (not relying on the admin handler's check). 2. Add per-route scope metadata and enforce at the authorization layer. 3. Consider adding resource-scoped permissions (`bucket:default:read` granularity). 4. At minimum, fix the mismatch: reading `PUT /v1/buckets/{bucket}` requires write scope, which is correct, but the coarse method-based check should be complemented with route-level checks. |
| **Effort** | M (add route metadata + middleware to enforce) |

---

### Finding 5: No TLS Support — Credentials in Cleartext

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Server only listens on plain HTTP; no HTTPS support |
| **Location** | `cmd/server/main.go:248` (`srv.ListenAndServe()`) |
| **Description** | The HTTP server uses `http.ListenAndServe()` with no TLS configuration. All API keys, JWTs, and data are transmitted over cleartext HTTP. There is no `APP_TLS_CERT` / `APP_TLS_KEY` configuration option. While this may be acceptable behind a TLS-terminating reverse proxy in some deployments, the absence of built-in TLS means: 1) Default/minimal configurations expose credentials in transit. 2) There's no way to enforce HTTPS-only access at the application level. 3) Compliance frameworks (PCI DSS, HIPAA, SOC2) require encryption in transit. |
| **Attack Scenario** | An attacker on the same network (LAN, WiFi, cloud VPC) performs ARP spoofing or passive sniffing and captures `Authorization: Bearer <api_key>` headers in plaintext. They can then replay these keys to access all data. |
| **Impact** | Credential theft, session hijacking, data exposure. |
| **Recommendation** | 1. Add `APP_TLS_CERT` / `APP_TLS_KEY` configuration options. 2. When TLS is configured, add a `Strict-Transport-Security` header and redirect HTTP to HTTPS. 3. Document that a reverse proxy is required for TLS in production when built-in TLS is not used. 4. Add a `TLS_MIN_VERSION` config defaulting to 1.3. |
| **Effort** | S (add Let's Encrypt / cert config) |

---

### Finding 6: API Keys Transmitted in Request URL via `X-Api-Key` Header

| Field | Value |
|-------|-------|
| **Category** | Authentication / Data Protection |
| **Severity** | **High** |
| **Title** | API key can be passed in `X-Api-Key` header — logged by proxies |
| **Location** | `internal/auth/auth_middleware.go:120-128` (`extractToken`) |
| **Description** | The `extractToken` function supports API keys via the `X-Api-Key` header in addition to the standard `Authorization: Bearer` header. Custom headers like `X-Api-Key` are more likely to be logged by reverse proxies, load balancers, CDNs, and API gateways than the standard `Authorization` header (which is frequently redacted by default). |
| **Attack Scenario** | A cloud load balancer or proxy logs all request headers for debugging. The `X-Api-Key` header value appears in log files. An attacker who gains access to the logging system extracts all API keys. |
| **Impact** | API key compromise, unauthorized data access. |
| **Recommendation** | 1. Remove `X-Api-Key` header support and require `Authorization: Bearer` only (the standard approach). 2. If backward compatibility requires keeping it, add a startup warning log that `X-Api-Key` is deprecated. 3. In any case, the `extractToken` function should mask the value in trace logs. |
| **Effort** | S (remove X-Api-Key check) |

---

### Finding 7: Internal Error Details Leaked in HTTP Responses

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **High** |
| **Title** | Internal error messages leaked to client in default error classification |
| **Location** | `internal/api/rest/handler.go:362-383` (`classify`) |
| **Description** | The `classify` function's `default` case returns `err.Error()` as the HTTP response body with status 500. This means any unclassified error (including internal filesystem errors, SQL errors, nil pointer dereferences caught by Recoverer, storage backend errors) will be sent verbatim to the client. This can leak: filesystem paths, SQL query fragments, internal IP addresses, and stack trace information embedded in error messages. |
| **Attack Scenario** | An attacker sends a malformed request that triggers a deep internal error (e.g., storage backend timeout, database constraint violation). The server returns 500 `{"error":{"code":"InternalError","message":"s3 upload: operation error S3: PutObject, ... : RequestError: send request failed ..."}}`. The attacker learns internal infrastructure details (S3 bucket names, region, network topology). |
| **Impact** | Information disclosure aiding further attacks. Internal path/network topology exposure. |
| **Recommendation** | 1. Replace the `default` case with a generic message: `"InternalError", "an internal error occurred", 500`. 2. Log the actual error details server-side. 3. For development, add an `APP_DEBUG_ERRORS` mode that includes details. 4. Ensure `Recoverer` also uses a generic message for panics. |
| **Effort** | S (change one string) |

---

### Finding 8: Weak Token Validation in AUTH_KEYS — No Minimum Length or Entropy Check

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | API key tokens have no minimum length or entropy requirements |
| **Location** | `internal/auth/auth.go:48-69` (`Parse`), `internal/api/rest/admin.go:122-142` (`AddKey`) |
| **Description** | Both the static `AUTH_KEYS` env-var parser and the runtime `POST /v1/admin/keys` endpoint accept any non-empty string as an API key token. There is no minimum length check, no character set requirement, and no entropy validation. An administrator (or automated tool) could create keys as short as 1 character, making them trivially brute-forceable. The `POST /v1/admin/keys` endpoint only checks `body.Token == ""`, so a single-character token like `"a"` is accepted. |
| **Attack Scenario** | An admin creates a quick API key `"test"` for testing, then forgets to revoke it. An attacker enumerates short API keys against public endpoints. With 62^4 = ~14M four-character alphanumeric keys, at 1000 req/s (within rate limits), a brute-force takes ~4 hours. A single-character key takes seconds. |
| **Impact** | Unauthorized access through weak/guessable keys. |
| **Recommendation** | 1. Enforce minimum token length of 32 characters for runtime `AddKey`. 2. Recommend (via `warn` log) that static `AUTH_KEYS` tokens be at least 32 characters. 3. Consider requiring mixed-case + digits. 4. Optionally, auto-generate keys when `AddKey` receives a token that's too short. |
| **Effort** | S (add length check) |

---

### Finding 9: Missing Security Headers

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | No security-related HTTP response headers |
| **Location** | `cmd/server/main.go:231-244` (`applyMiddleware`), CORS handler in `internal/middleware/cors.go` |
| **Description** | The server does not set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, or `Referrer-Policy`. The web UI is served at `/ui` without these protections. While the REST API is primarily machine-to-machine, the Web UI and Swagger docs (`/docs`) are accessible in a browser. This opens up clickjacking, MIME-sniffing, and XSS risks for the browser-facing endpoints. |
| **Attack Scenario** | An attacker creates a malicious page that iframes the aero-vault Web UI at `/ui`. A victim authenticated to aero-vault visits the attacker's page, and the attacker tricks them into clicking buttons that trigger authenticated API calls (clickjacking). |
| **Impact** | CSRF-like attacks on browser-accessible surfaces, reduced security posture for auditors. |
| **Recommendation** | Add a security headers middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` (or SAMEORIGIN for the Web UI), and `Referrer-Policy: no-referrer`. For the Web UI specifically, add `Content-Security-Policy`. Document that for API-only deployments these are informational. |
| **Effort** | S (add middleware) |

---

### Finding 10: Content-MD5 Uses Broken MD5 Hash

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | MD5 used for Content-MD5 verification and ETag computation |
| **Location** | `internal/service/file_crud.go:97-124` (`md5WrapReader`, `NewETagVerifier`) |
| **Description** | Content-MD5 verification and ETag computation both use MD5 (`crypto/md5`). MD5 is cryptographically broken — collision attacks are practical (well-known chosen-prefix collisions). While Content-MD5 is a transport integrity check (the client sends the MD5, server verifies), ETag is used for conditional requests (`If-Match`, `If-None-Match`). An attacker could craft two different objects with the same ETag, confusing cache and conditional-request semantics. |
| **Attack Scenario** | An attacker uploads a benign file, then uploads a malicious file with the same MD5 ETag. A client requests the benign file with `If-None-Match: "<etag>"` — the server returns 304 Not Modified (not sending the body), but the client's cached copy is actually the malicious file (if cache poisoning was achieved). |
| **Impact** | Conditional request bypass, cache poisoning in edge cases. |
| **Recommendation** | 1. Compute ETags using SHA-256 instead of MD5. This is a breaking change for existing clients that depend on MD5-based ETags — document the migration. 2. Accept both MD5 and SHA-256 Content-MD5 headers (or use `x-amz-checksum-sha256` in addition to Content-MD5). 3. At minimum, document that MD5 is used and note the risks for high-security deployments. |
| **Effort** | L (migration effort for existing objects' ETags) |

---

### Finding 11: Rate Limiter Tenant Key is Client-Controlled — Potential DoS Bypass

| Field | Value |
|-------|-------|
| **Category** | Denial of Service (Threat Model) |
| **Severity** | **Medium** |
| **Title** | Rate limiter uses client-controlled `X-Aero-Tenant` header as bucket key |
| **Location** | `internal/middleware/ratelimit.go:111-116` (`isAllowed`) |
| **Description** | The rate limiter derives the tenant from `TenantFrom(ctx)`, which originates from the `X-Aero-Tenant` request header (client-controlled). An attacker can rotate through arbitrary tenant names to bypass per-tenant rate limits. While the bucket map has a 50K capacity limit, a sophisticated attacker with many distinct tenant names could exhaust this limit and force eviction/idle-state on legitimate tenants. |
| **Attack Scenario** | An attacker sends requests with rotating `X-Aero-Tenant: attacker-00001`, `X-Aero-Tenant: attacker-00002`, etc. Each new tenant gets a fresh bucket with full burst capacity, effectively bypassing the per-tenant rate limit. The attacker can saturate the server while staying under the limit per "tenant". If they exhaust 50K buckets, legitimate tenants may be evicted and re-created (though they'd get a fresh full bucket). |
| **Impact** | Rate limiting bypass, resource exhaustion, degraded service for legitimate tenants. |
| **Recommendation** | 1. Require authentication for the AI endpoints that have their own rate limiter, so the tenant is tied to the authenticated key rather than the header. 2. Add a global cap that limits rate independent of tenant (this exists via `RATE_LIMIT_RPS` but applies to all tenants summed — add a hard per-IP limiter). 3. Consider adding a source-IP based secondary limiter. |
| **Effort** | M (add IP-based secondary limiter) |

---

### Finding 12: Access Logs Leak Object Keys in Request Path

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Sensitive object keys logged in access log paths |
| **Location** | `internal/middleware/middleware.go:66-77` (`AccessLog`) |
| **Description** | The access log middleware logs `r.URL.Path` for every request. For GET/PUT operations, this includes the full object key path, e.g., `/v1/files/medical-records/patient-12345.pdf`. If object keys encode sensitive information (patient IDs, SSNs, proprietary project names), this leaks into log aggregators, log monitoring systems, and potentially log retention. |
| **Attack Scenario** | A company uses aero-vault for storing HR documents with keys like `employees/salary/john.doe.2026.json`. An engineer with access to the log aggregation system (e.g., Grafana Loki, Datadog, ELK) can browse all object accesses and reconstruct sensitive business data. |
| **Impact** | Sensitive data exposure through secondary systems with weaker access controls. GDPR/PII compliance risk. |
| **Recommendation** | 1. Add a log level for path: log the path at `debug` level only, or truncate/hash it at `info` level. 2. Add a configuration option `APP_LOG_REDACT_PATHS` to redact the path segment beyond `/v1/files/` or `/s3/`. 3. Document that object keys should not contain PII. |
| **Effort** | S (add path redaction option) |

---

### Finding 13: No Audit Trail for Regular Object Operations

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Repudiation) |
| **Severity** | **Medium** |
| **Title** | Object read operations are not audited |
| **Location** | `internal/repository/audit.go` (presumed), `internal/api/rest/admin.go:232-240` (`audit`) |
| **Description** | The audit-logging system only records admin actions (tenant create/delete, quota changes, key operations). Regular object operations (GET, PUT, DELETE) are not recorded in the audit log. Events are published to the event bus (which can be consumed by webhooks), but there's no built-in audit trail for who accessed which object. This means: 1) You cannot investigate a data breach after the fact. 2) There's no non-repudiation for object access. 3) Compliance frameworks (SOC2, SOX) require access logging. |
| **Attack Scenario** | An insider with valid credentials exfiltrates sensitive data by reading objects. There is no audit record of which objects they accessed, when, or with which key. Forensic investigation is impossible. |
| **Impact** | Inability to detect or investigate data breaches. Compliance failure. |
| **Recommendation** | 1. Add optional read-access audit logging gated by `AUDIT_READ_ACCESS` config. 2. Record `GET`, `PUT`, `DELETE` operations in the audit_log table with actor (API key label), tenant, bucket, key, and timestamp. 3. Add a `GET /v1/admin/audit/objects` endpoint to query object access history. |
| **Effort** | M (add instrumentation + DB migration) |

---

### Finding 14: JWT Has No Revocation Mechanism

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | No JWT token revocation |
| **Location** | `internal/auth/jwt.go` (entire file) |
| **Description** | JWT tokens can be revoked only by their expiry. There is no `jti` (JWT ID) claim, no blacklist/deny-list, and no token invalidation endpoint. Once issued, a JWT is valid until its `exp` timestamp, even if the issuing admin wants to revoke it immediately. The `AUTH_JWT_SECRET` rotation would invalidate ALL tokens including legitimate ones. |
| **Attack Scenario** | An admin accidentally issues a JWT with `admin` scope and `exp` set to 1 year. They realize the mistake but have no way to revoke the token. The only option is to rotate `AUTH_JWT_SECRET`, which invalidates ALL tokens and requires all legitimate clients to re-authenticate. |
| **Impact** | No revocation leads to operational risk; secret rotation causes service disruption. |
| **Recommendation** | 1. Add `jti` (UUID) to every issued JWT. 2. Add a `revoked_jti` table in the database. 3. On `Verify()`, check if the `jti` is in the revoked list. 4. Add `POST /v1/admin/jwt/revoke { "jti": "..." }` endpoint. 5. Add `AUTH_JWT_REVOKED_CACHE_SIZE` and `AUTH_JWT_REVOKED_CACHE_TTL` to avoid DB load on every verify. |
| **Effort** | M (add table + middleware check + admin endpoint) |

---

### Finding 15: Webhook Payload Includes Sensitive Object Metadata

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Webhook events may expose object metadata to third-party endpoints |
| **Location** | `internal/events/webhook.go:82-96` (`deliver`) |
| **Description** | The webhook delivers event payloads containing object metadata (key, bucket, size, content_type, etag, backend info) to external URLs. If `EVENTS_WEBHOOK_URL` points to a third-party service, sensitive object keys and metadata are shared with that service. There is no configuration to selectively filter which events or which metadata fields are sent. |
| **Attack Scenario** | A company uses a third-party analytics service as a webhook target. The object key naming convention includes customer names/project codes. This customer information leaks to a third party, potentially violating GDPR data processing agreements. |
| **Impact** | Unintended data sharing with third parties. |
| **Recommendation** | 1. Add `EVENTS_WEBHOOK_FILTER` to select which event types trigger webhooks. 2. Add `EVENTS_WEBHOOK_REDACT_KEYS` to mask object keys in webhook payloads. 3. Document that webhook URLs must be trusted endpoints. |
| **Effort** | S (add config filtering) |

---

### Finding 16: No Brute-Force Protection on Login/Auth Endpoints

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | Authentication failures are logged but not rate-limited per-key |
| **Location** | `internal/auth/auth_middleware.go:78-95` (`authenticateBearer`) |
| **Description** | Failed authentication attempts (invalid API key, bad JWT, wrong tenant) return 401/403 but are not tracked per-key. An attacker can brute-force API keys with no per-account lockout. The global rate limiter caps total requests but doesn't distinguish between failed auth attempts from the same source. |
| **Attack Scenario** | An attacker has a list of potential API key tokens (~10M possibilities). They launch a distributed brute force across multiple IPs. The global rate limiter (if set to 100 RPS) still allows 360K attempts/hour — enough to crack short keys in hours. |
| **Impact** | Credential guessing is feasible, especially for short tokens. |
| **Recommendation** | 1. Track failed auth attempts per source IP in memory with a sliding window. 2. After N failures (e.g., 10 in 1 minute), apply a temporary block on that IP. 3. Consider adding `AUTH_FAILURE_DELAY_MS` to introduce artificial delay on failure. |
| **Effort** | M (add sliding-window failure tracker) |

---

### Finding 17: ETag Generated by Storage Backend, Not Verified on Read by Default

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | On-read ETag verification is opt-in and off by default |
| **Location** | `internal/service/file_crud.go:50-70` (`ReadVerificationConfig`), `internal/service/file.go:58-71` (`FileService.WithReadVerification`) |
| **Description** | The `ReadVerificationConfig` is off by default. Without it, there is no on-read integrity check that the bytes returned from storage match the ETag stored in metadata. If the storage layer silently corrupts or swaps data (e.g., due to bit rot or a malicious storage provider), the corruption goes undetected until the next write. |
| **Attack Scenario** | A storage backend (especially S3, OSS, COS) experiences a "silent data corruption" event where an object's bytes are partially replaced. Without read verification, the corrupted bytes are served to clients. |
| **Impact** | Undetected data corruption served to users. |
| **Recommendation** | 1. Enable read verification by default for objects under a configurable size threshold (e.g., 10MB). 2. For larger objects, use byte-range sampling. 3. Log a warning on verification failure and consider marking the object as corrupt in metadata. |
| **Effort** | S (change default config) |

---

### Finding 18: No `Connection` or `Upgrade` Header Validation for SSE

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Info** |
| **Title** | SSE endpoint does not validate `Accept` header |
| **Location** | `internal/api/rest/sse.go` (presumed) |
| **Description** | The SSE streaming endpoint (`GET /v1/events/stream`) should require `text/event-stream` in the `Accept` header and return a clear error for clients that cannot process SSE. This is more of a correctness issue than a security vulnerability. |
| **Recommendation** | Validate `Accept: text/event-stream` header and return 406 Not Acceptable for incompatible clients. |
| **Effort** | S (add header check) |

---

## STRIDE Analysis Summary

| Category | Findings | Rating |
|----------|----------|--------|
| **S**poofing | #3 (JWT forgery), #8 (weak keys), #16 (no brute-force protection) | 🔴 **Weak** |
| **T**ampering | #10 (MD5 ETags), #17 (read-verify off) | 🟡 **Moderate** |
| **R**epudiation | #13 (no object audit), #1 (MCP no auth) | 🔴 **Weak** |
| **I**nformation Disclosure | #7 (error leakage), #12 (log leakage), #15 (webhook metadata) | 🟡 **Moderate** |
| **D**enial of Service | #11 (rate limit bypass), #2 (path traversal) | 🟡 **Moderate** |
| **E**levation of Privilege | #2 (path traversal), #4 (scope bypass), #1 (MCP no auth) | 🔴 **Weak** |

---

## Overall Security Posture

| Dimension | Rating |
|-----------|--------|
| **Authentication** | 🟡 Needs Improvement |
| **Authorization** | 🟡 Needs Improvement |
| **Input Validation** | 🟡 Needs Improvement |
| **Cryptography** | 🟡 Needs Improvement |
| **Data Protection** | 🟡 Needs Improvement |
| **Compliance Readiness** | 🔴 Weak |
| **Overall** | **🔴 Needs Improvement** |

### Top 3 Critical Issues

1. **MCP stdio has zero authentication** (#1) — any process with access to the binary/stdin can read/write/delete all data without any auth check.
2. **Bucket/key path traversal via unvalidated bucket names** (#2) — can break tenant isolation and access any storage path.
3. **JWT symmetric secret with no revocation** (#3) — compromise of `AUTH_JWT_SECRET` gives attacker full control with no way to revoke.

### Top 3 Quick Wins

1. **Replace generic 500 error messages** (#7) — one line change in `classify()` to stop leaking internal details.
2. **Add bucket/tenant name validation** (#2) — ~20 lines of regex validation in `validateKey` equivalent for bucket names.
3. **Add minimum API key length enforcement** (#8) — check `len(token) >= 32` in `AddKey` handler.

### Security Debt

- **No TLS** — HTTPS should be built-in, not deferred to reverse proxy config
- **No read audit trail** — can't investigate data breaches or meet compliance requirements
- **MD5-based ETags** — breaking change to migrate, but needed for long-term cryptographic hygiene
- **Missing security headers** — trivial to add, necessary for compliance frameworks
- **No JWT revocation** — operational burden when tokens need to be invalidated

### Compliance Considerations

| Standard | Status | Action Required |
|----------|--------|----------------|
| OWASP Top 10 (2021) | ❌ Fails A01 (Broken Access Control), A02 (Crypto Failures), A04 (Insecure Design), A05 (Security Misconfiguration) | Multiple findings above |
| PCI DSS 4.0 | ❌ Fails Req 3 (protect stored data), Req 4 (encrypt in transit), Req 7 (access control), Req 8 (auth), Req 10 (audit trails) | TLS, audit, encryption, access control |
| GDPR | ❌ Fails Art 5 (integrity/confidentiality), Art 32 (security of processing) | Audit trail, encryption, access control |
| SOC 2 | ❌ Fails CC6 (logical/physical access), CC7 (monitoring) | Audit trail, access reviews |

---

## Final Assessment

The aero-vault codebase demonstrates strong security awareness in the SSE encryption layer, webhook HMAC signing, and structured tenant isolation via storage key prefixes. However, the **perimeter auth model has critical gaps** — MCP stdio bypasses all controls, the JWT system is fragile, and input validation for bucket/tenant names is absent. The lack of TLS, read auditing, and brute-force protection makes production deployment against adversarial conditions risky without a reverse proxy and additional infrastructure controls.

**Recommendation:** Address the 3 critical issues (MCP auth, path traversal, JWT revocation) before production deployment of any multi-tenant or internet-facing instance. The quick wins should follow immediately. A deeper security investment is needed for compliance-driven environments.
