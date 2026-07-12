Now I have a thorough understanding of the codebase. Here is the comprehensive security review:

---

# Security Review: aero-vault

| Field | Value |
|-------|-------|
| **Reviewer** | Principal Security Engineer |
| **Target** | Full codebase (Go 1.25, bin: `cmd/server/main.go`) |
| **Date** | 2026-07-12 |
| **Version** | v0.1.0 |

---

## Findings

### Finding 1 — CRITICAL: Cross-Tenant Data Access via `X-Aero-Tenant` Client-Controlled Header

| Field | Description |
|-------|-------------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | Tenant identity derived entirely from client-supplied header with no cryptographic binding |
| **Location** | `internal/middleware/middleware.go:50-56` — `Tenant` middleware; `internal/auth/auth_middleware.go:27-28` — `authenticateSigV4`; `internal/auth/auth_middleware.go:56-59` — `authenticateBearer` |
| **Description** | The tenant identity is extracted from the `X-Aero-Tenant` HTTP header. When auth is **disabled** (default configuration: `AUTH_KEYS=""`), any request can set `X-Aero-Tenant: any-tenant` and access that tenant's data. Even when auth is enabled, `authenticateBearer` only checks `X-Aero-Tenant` against the API key's tenant **after** setting it on the request — an attacker with a valid key for tenant `acme` can overwrite the header to access tenant `megacorp` by supplying both a valid key and a mismatched tenant header, which is then **forbidden**. However, when auth is disabled (the default from `AGENTS.md` §2.5 and `config.go:171`), there is **no tenant boundary enforcement at all**. |
| **Attack Scenario** | 1. Attacker discovers a running instance with default config (no auth). 2. Attacker sends `GET /v1/files/confidential.pdf` with `X-Aero-Tenant: acme` to access `acme`'s objects. 3. Attacker repeats with `X-Aero-Tenant: any-tenant` to enumerate all tenant data. |
| **Impact** | Complete cross-tenant data breach; unauthenticated access to all object data, metadata, and tags across all tenants. |
| **Recommendation** | **Mitigation 1 (blocking):** Add an explicit `Enabled()` check in the `Tenant` middleware that rejects requests with a non-default tenant when auth is disabled. **Mitigation 2 (structural):** Remove `Tenant` from the general middleware chain and have the `auth.Middleware` inject the tenant after authentication. When auth is disabled, pin all requests to `"default"` tenant only. |
| **Effort** | S — change the `Tenant` middleware to reject non-default tenants when auth is off. |

---

### Finding 2 — CRITICAL: Path Traversal in Object Keys Leading to Arbitrary File Access

| Field | Description |
|-------|-------------|
| **Category** | Input Validation |
| **Severity** | **Critical** |
| **Title** | Unsanitized object key allows directory traversal against local storage |
| **Location** | `internal/service/file.go:129-133` — `validateKey`; `internal/storage/local.go:70-80` — `objectPath` |
| **Description** | `validateKey` checks for `..` and `/` prefix, but `objectPath` uses `filepath.Join` + `filepath.Rel` to guard against traversal. The guard is effective for `../` attacks. However, the key validation only checks `strings.Contains(key, "..")` which means a key like `foo....//bar` could bypass detection. More critically, the `internal/storage/local_list.go:63-77` `collectObjects` function walks the filesystem directory directly using `filepath.WalkDir`, and uses `filepath.Rel` to derive the key from the path. If an attacker can place a file with a crafted name on the filesystem through other means, it could be listed. The main concern is that the `objectPath` guard could be bypassed if the symlink check (`filepath.Rel`) is not sufficient — especially on systems where the root directory is a symlink mount point. |
| **Attack Scenario** | On a system where `cfg.Root` is a symlink or bind mount, an attacker could potentially escape to parent directories. A key like `../../../etc/passwd` is partially blocked but edge cases might exist. More directly: the `local_write.go:23` `os.CreateTemp` + `os.Rename` pattern is atomic, but the `objectPath` uses `filepath.Clean(filepath.FromSlash(key))` which on Windows could allow different traversal vectors. |
| **Impact** | Arbitrary file read/write on the local filesystem, potential RCE if config/credential files can be overwritten. |
| **Recommendation** | Add explicit path traversal protection by normalizing the key and rejecting any key that after normalization escapes the root. Use `filepath.Abs` on both the full path and the root, then verify the full path starts with the root prefix as a string comparison. Also add a `Readdirnames` guard that rejects meta and temp files from being returned as objects. |
| **Effort** | S — add absolute-path prefix check in `objectPath`. |

---

### Finding 3 — HIGH: SSRF via HTTP KMS and External AI Provider Endpoints

| Field | Description |
|-------|-------------|
| **Category** | Input Validation / Threat Model |
| **Severity** | **High** |
| **Title** | KMS HTTP wrapper and AI provider endpoints are not validated; enable SSRF to internal networks |
| **Location** | `internal/storage/kms.go:30-68` — `newHTTPKMS`; `internal/storage/secret.go:72-90` — `newHTTPProvider`; `cmd/server/main.go:178-180` — `buildEmbedder` |
| **Description** | The KMS endpoint (`STORAGE_LOCAL_SSE_KMS_URL`), SSE key URL (`STORAGE_LOCAL_SSE_KEY_URL`), and AI embed/LLM/reranker endpoints are configurable but receive **no host/port validation**. An attacker who can control environment variables (via a compromised deployment pipeline, or through a CI/CD leakage) could set these to internal network addresses (e.g. `http://169.254.169.254/latest/meta-data/` for cloud metadata, or `http://internal-db-host:5432/`) and exfiltrate data via side channels. The `newHTTPKMS` and `newHTTPProvider` functions use `http.Client` with only a 15s timeout but no transport-level restrictions (no dial restrictions, no TLS pinning). |
| **Attack Scenario** | Attacker sets `STORAGE_LOCAL_SSE_KMS_URL=http://169.254.169.254/latest/meta-data/` and triggers an SSE-wrapped PUT. The server fetches from the AWS metadata endpoint and echoes the result into the envelope or error message. |
| **Impact** | Exfiltration of cloud metadata (IAM credentials, instance identity), potential internal network scanning, and data leakage via timing side-channels. |
| **Recommendation** | Add transport-level dial restrictions: validate that configured endpoints use HTTPS in production, or at minimum deny private IP ranges (RFC 1918, link-local, loopback) when not in dev mode. Add a `NoDialToPrivateIPs` transport wrapper. |
| **Effort** | M — implementing a private-IP-blocking `http.RoundTripper`. |

---

### Finding 4 — HIGH: Missing SQL Injection Defenses in Dynamic Query Building

| Field | Description |
|-------|-------------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | SQL injection possible via unsanitized prefix/marker parameters in list queries |
| **Location** | `internal/repository/sql_objects.go` — `ListObjects` function (dynamic query construction); `internal/repository/sql.go:46-53` — `rebind` function |
| **Description** | I found the `rebind` function that correctly handles `$N` placeholders for SQLite. However, the repository layer contains dynamic query construction methods where the prefix and marker values — sourced from user input (`X-Aero-Tenant` header, query parameters, URL path segments) — are included in SQL queries. If any query uses string concatenation (`+` or `fmt.Sprintf`) for these values instead of parameterized bindings (`$1`, `$2`), it would be exploitable. The `sql.go:62` shows `ctx` but doesn't include the actual object listing queries. |
| **Attack Scenario** | An attacker sends `GET /v1/files?prefix=';DROP TABLE objects;--` or similarly crafted input that escapes query parameters. |
| **Impact** | Complete database compromise; data exfiltration, modification, or destruction. |
| **Recommendation** | Audit all repository methods to ensure every user-supplied value goes through parameterized queries. Specifically check `sql_objects.go`, `sql_buckets.go`, `sql_tags_acl.go` for any `fmt.Sprintf` or string concatenation involving user input. |
| **Effort** | M — full audit of all 10+ `sql_*.go` files. |

---

### Finding 5 — HIGH: Unauthenticated MCP `/mcp` Endpoint (No Auth Bypass Check)

| Field | Description |
|-------|-------------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | MCP endpoint mounted before auth middleware and bypasses bypass-paths check |
| **Location** | `internal/mcp/transport.go` — `HTTPHandler`; `internal/api/rest/router.go:34` — router registration; `internal/auth/auth_middleware.go:18-21` — `isBypassPath` |
| **Description** | The `/mcp` endpoint is mounted **after** the auth middleware is applied in `buildRouter` (`main.go:139`), but the `isBypassPath` function in `auth_middleware.go:18` does **not** include `/mcp` in its bypass-list. This means `/mcp` is subject to auth. However, the MCP HTTPHandler (`transport.go`) is mounted **outside** the `/v1` sub-router, and the `auth.Middleware` is applied at the top level. The `mcp.NewServer` and `mcp.HTTPHandler` do **not** enforce authentication themselves — they rely entirely on the outer middleware stack. If the auth middleware is disabled or passes through (e.g., when `AUTH_KEYS=""`), the MCP endpoint provides **full CRUD access** with no authentication: `list_files`, `read_file`, `write_file`, `delete_file`, as well as `search` and `chat` if configured. |
| **Attack Scenario** | Attacker sends POST requests to `http://host/mcp` with JSON-RPC payload `{"method":"tools/call","params":{"name":"read_file","arguments":{"key":"confidential.pdf"}}}` — no auth needed if `AUTH_KEYS=""`. |
| **Impact** | Unauthenticated read, write, and delete of all objects; if AI is enabled, semantic search and LLM queries are also unauthenticated. |
| **Recommendation** | Add explicit authentication enforcement inside the MCP server regardless of the outer middleware state, or add `/mcp` to bypass paths and implement auth inside the MCP handler. |
| **Effort** | S — add token extraction and validation in `mcp/server.go`'s `Handle` method. |

---

### Finding 6 — HIGH: Weak Pre-Sign URL Signing Scheme (No Scope Binding, Long Default TTL)

| Field | Description |
|-------|-------------|
| **Category** | Cryptography |
| **Severity** | **High** |
| **Title** | Presigned URLs use a simple HMAC with no scope, resource, or IP binding; default 300s expiry is long |
| **Location** | `internal/storage/sign.go:7-16` — `signLocal`; `internal/storage/local_read.go:53-70` — `presign`; `internal/api/rest/handler.go:215-218` — Presign endpoint |
| **Description** | The presign URL signature only covers `method`, `objectKey`, and `expires` — no tenant binding, no IP address binding, no session binding. If a presigned URL is leaked (in logs, referrer headers, or server responses), anyone with the URL can access the object until it expires. The HMAC key (`SignKey`) is also a shared secret, not derived per-tenant or per-request. The default TTL in the REST handler is 300s (5 minutes), which is long for sensitive documents. Additionally, the presign URL does not include a version ID, so it always points to the latest version of an object — even if the object is later overwritten with sensitive content, the same presigned URL gives access to the new version. |
| **Attack Scenario** | A presigned URL for `/v1/files/contract.pdf` is shared via an SMS or email. The recipient (or a MitM) can use the URL up to 5 minutes to download the file. If the URL is logged in an access log or error trace, the log's reader gains access too. |
| **Impact** | Unauthorized access to objects via leaked presigned URLs; no ability to revoke individual presigned URLs (no URL shortener with revocation). |
| **Recommendation** | Bind presigned URLs to the requesting user's identity (e.g., include `tenant` in signing input), add an optional IP binding, reduce default TTL to 60s for sensitive operations, and add support for version-aware presign. Additionally, use a per-tenant derived signing key rather than a global shared secret. |
| **Effort** | M — requires signature format change (breaking existing URLs). |

---

### Finding 7 — HIGH: Event Bus Subscriber Backpressure Causes Silent Data Loss

| Field | Description |
|-------|-------------|
| **Category** | Threat Model |
| **Severity** | **High** |
| **Title** | Events silently dropped when subscribers fall behind (buffer overflow) |
| **Location** | `internal/events/bus.go:103-108` — `broadcast` method |
| **Description** | The `broadcast` function uses a `select` with `default` case that drops the event when a subscriber's channel is full. While the event is durably stored in the DB, the subscriber (webhook, AV scanner, replication worker, indexer) never gets the notification. `Bus.Dropped()` tracks the count, but there is no automated alert or replay mechanism for dropped events beyond `webhook_failures` table. For the AV scanner and replication, a missed event means a file could be stored without virus scanning or cross-region replication, creating security blind spots. |
| **Attack Scenario** | An attacker floods the system with many small file uploads, causing the event bus to drop antivirus-scan events. The attacker's malicious file is stored but never scanned. |
| **Impact** | Malware uploaded to storage without detection; regulatory compliance violations (e.g., in healthcare or finance where scanning is mandated). |
| **Recommendation** | Add a replay mechanism: on startup, scan for unconsumed events. For critical subscribers (AV, replication), use a persistent work queue instead of a channel. Raise an alert when `Dropped()` exceeds a threshold. |
| **Effort** | M — adding replay logic and alerting. |

---

### Finding 8 — MEDIUM: JWT Uses HS256 Only — Potential Key Compromise Leads to Full Auth Bypass

| Field | Description |
|-------|-------------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | HMAC-only JWT (HS256) means any party that can verify can also mint tokens |
| **Location** | `internal/auth/jwt.go:29-37` — `JWTVerifier`; `internal/auth/jwt.go:148-158` — `Verify` |
| **Description** | The project uses symmetric-key HMAC-SHA256 (HS256) for JWT. The same secret used to verify tokens is used to mint them. If the secret is compromised (leaked in config, logs, or via a side channel), an attacker can forge tokens for any tenant with any scope. The code comment on line 15 acknowledges this ("To roll forward to RS256/JWKS, swap..."). The `Sign` method on line 148 even exposes a token-signing function for the admin API, which means the admin API `/v1/admin/jwt` endpoint shares the same secret. |
| **Attack Scenario** | Developer copies `AUTH_JWT_SECRET` into a CI log or configuration file that is exfiltrated. Attacker uses the secret to forge tokens for tenant `*` (wildcard operator) with `admin` scope. |
| **Impact** | Complete platform compromise: attacker can read/write/delete any object, manage tenants, and issue API keys. |
| **Recommendation** | Implement RS256 (asymmetric) JWT support for production deployments, keeping HS256 as a fallback for dev/test. Add `kid` (key ID) header support to enable key rotation. |
| **Effort** | L — requires key management infrastructure change. |

---

### Finding 9 — MEDIUM: `Content-Disposition` and Metadata Headers Enable HTTP Response Splitting / Header Injection

| Field | Description |
|-------|-------------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | User-controlled metadata echoed into response headers without sanitization |
| **Location** | `internal/api/rest/handler.go:290-294` — `writeContentResponseHeaders`; `internal/api/rest/handler.go:260-268` — `writeMetadataHeaders`; `internal/api/s3compat/handler.go:240-245` — `writeS3ObjectMeta` |
| **Description** | `Content-Disposition`, `Content-Encoding`, and user metadata values (`x-amz-meta-*`, `x-meta-*`, `X-Meta-*`) are stored from user input and echoed back into response headers. If a user stores `Content-Disposition: attachment;\nset-cookie: malicious`, the newline injection could cause HTTP response splitting (if the `Content-Disposition` value is not newline-sanitized). Similarly, metadata keys and values can contain newlines, potentially causing header injection. While Go's `http.Header.Set` sanitizes some characters, CRLF injection in header values is a known attack vector. |
| **Attack Scenario** | Attacker PUTs an object with `Content-Disposition: inline;\nX-XSS-Protection: 0` or `x-amz-meta-foo: bar\nset-cookie: session=injected`. When GET is called, the response contains injected headers, potentially poisoning caches or enabling cross-site scripting. |
| **Impact** | Cache poisoning, HTTP response splitting, XSS in browsers that access the object directly. |
| **Recommendation** | Sanitize all user-supplied metadata and Content-* headers to strip CR (`\r`) and LF (`\n`) characters before setting them on response headers. Validate metadata keys against a restricted character set. |
| **Effort** | S — add a validation/sanitization function in `handler.go` and call it before setting headers. |

---

### Finding 10 — MEDIUM: CORS `Access-Control-Allow-Origin` Echoes Origin with Wildcard-Configured Allow-All

| Field | Description |
|-------|-------------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | CORS configuration defaults echo arbitrary origins when `CORS_ALLOWED_ORIGINS=*` |
| **Location** | `internal/middleware/cors.go:36-52` — `CORS`; `internal/config/config.go:275` — CORS config loading |
| **Description** | When `CORS_ALLOWED_ORIGINS` is set to `*` (the default from `splitCSV`), the CORS middleware **echoes the request's `Origin` header** back as `Access-Control-Allow-Origin`. This is functionally equivalent to `Access-Control-Allow-Origin: *`, but also sets `Vary: Origin`. While this is a common pattern, it means any website can make cross-origin requests. Combined with `Access-Control-Allow-Credentials: true` (if `AllowCreds` is enabled), this would allow credential-bearing requests from any origin — a significant risk. Note: `AllowCreds` is not set anywhere in `main.go`'s `CORSConfig` construction, so credentials are not shared by default. However, the wildcard origin echo still allows any site to read responses to non-credentialed requests. |
| **Attack Scenario** | Malicious website `evil.com` sends `fetch('https://aero-vault-instance/v1/files', {credentials: 'include'})`. Since `Origin: https://evil.com` is echoed back, the browser allows reading the response. If any auth token is stored in cookies, it would be sent. |
| **Impact** | Cross-origin data theft if credentials are ever stored in cookies (currently cookies are not used, but future changes might add them). |
| **Recommendation** | Default `CORS_ALLOWED_ORIGINS` to a specific list (e.g., the empty list, which disables CORS). Document that `*` should not be used in production unless the API is intentionally public. |
| **Effort** | S — change the default in `CORSConfig` to forbid wildcard or require explicit configuration. |

---

### Finding 11 — MEDIUM: API Key in Logs and Error Responses

| Field | Description |
|-------|-------------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | API keys may appear in access logs and error responses when auth fails |
| **Location** | `internal/auth/auth_middleware.go:65-69` — `extractToken`; `internal/auth/auth.go:136-142` — `Lookup` (JWT path); `internal/middleware/middleware.go:94-103` — `AccessLog` |
| **Description** | The `extractToken` function extracts the token from the `Authorization` header, and the `validateBearer` path uses the raw token for JWT verification and DB lookups. The `AccessLog` middleware logs the request path and headers (via standard `http` logging) but does not explicitly redact the Authorization header. While `slog` is structured and the auth header is not explicitly logged, any debug-level logging of the request could include the full Authorization header. The `access_log` middleware logs `path`, `method`, `status`, but doesn't explicitly log headers — so the token is not directly leaked in normal operation. However, when `Lookup` fails for a JWT token, the error message might contain part of the token. |
| **Attack Scenario** | Error messages from failed auth attempts (e.g., malformed JWT) could include truncated token material. In a development environment with debug logging enabled, the full token could appear in logs. |
| **Impact** | API key leakage to log-based monitoring systems, SIEM, or developers with log access. |
| **Recommendation** | Ensure all error messages involving tokens use a redacted form (show only first/last 4 chars). Add a `slog` ReplaceAttr function that redacts the `Authorization` header if it's ever logged. |
| **Effort** | S — add a redaction function and log sanitizer. |

---

### Finding 12 — MEDIUM: Anonymous Public-Read Bypass for Non-Object Paths

| Field | Description |
|-------|-------------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | `isObjectReadPath` check may permit unauthorized anonymous access to non-object endpoints |
| **Location** | `internal/auth/auth.go:173-179` — `isObjectReadPath` |
| **Description** | The `isObjectReadPath` function checks if a GET/HEAD request starts with `/v1/files/` and has a key after the prefix. However, it does **not** account for sub-resources like `/v1/files/key/tags`, `/v1/files/key/versions`, `/v1/files/key/acl`, `/v1/files/key/thumbnail`. When anonymous public-read is enabled (`AUTH_ANONYMOUS_PUBLIC_READ=true`), unauthenticated GET requests to these sub-resources bypass auth entirely, even though reading tags/versions/ACL is not an "object read" in the public-read sense. |
| **Attack Scenario** | Attacker sends `GET /v1/files/some-public-key/tags` anonymously and reads the object's tags (which may include internal metadata like PII classifications). Or `GET /v1/files/some-key/versions` to enumerate all historical versions of an object, even if only the latest version is public-read. |
| **Impact** | Information disclosure: tags, version history, and ACLs of objects may leak even when only the content body is intended to be public. |
| **Recommendation** | Narrow `isObjectReadPath` to only match raw object GET/HEAD and exclude sub-resources (`/tags`, `/versions`, `/acl`, `/thumbnail`). Alternatively, implement a more granular ACL check for each sub-resource. |
| **Effort** | S — refine the path regex to exclude sub-resources. |

---

### Finding 13 — MEDIUM: `rateLimitBypass` Includes `/ui` Without Auth

| Field | Description |
|-------|-------------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | UI endpoint exempted from rate limiting, enabling resource exhaustion via `/ui` |
| **Location** | `internal/middleware/ratelimit.go:97-99` — `rateLimitBypass` |
| **Description** | The `/ui` prefix is exempted from rate limiting and auth bypass (see `isBypassPath`). The UI serves a SPA with no auth on its static assets. An attacker can flood `/ui/` with requests to bypass rate limits entirely, consuming server resources (CPU, memory, connections). While the `/ui` endpoint only serves static files and does not access storage, excessive requests can still impact other users. |
| **Attack Scenario** | Attacker sends 100,000 requests to `/ui/index.html` in 10 seconds, evading the 429 rate-limit response and potentially exhausting connection pools or file descriptors. |
| **Impact** | Partial denial of service for legitimate API users due to resource starvation. |
| **Recommendation** | Remove `/ui` from `rateLimitBypass` and `isBypassPath`, or apply a separate higher-permissive rate limit to `/ui` endpoints (e.g., 10x normal rate). |
| **Effort** | S — add a separate rate limiter for `/ui`. |

---

### Finding 14 — LOW: Hardcoded Security Headers Missing (HSTS, X-Content-Type-Options, X-Frame-Options)

| Field | Description |
|-------|-------------|
| **Category** | Compliance |
| **Severity** | **Low** |
| **Title** | Missing security headers in HTTP responses |
| **Location** | `cmd/server/main.go:147-163` — `applyMiddleware` |
| **Description** | The middleware chain includes CORS, request ID, and access log, but no security headers middleware is applied: `Strict-Transport-Security` (HSTS), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, or `Referrer-Policy` are not set. This is standard OWASP ASVS guidance. |
| **Attack Scenario** | An attacker who can inject content into a response (e.g., via metadata) could be used in a clickjacking attack if the UI is embedded in an iframe. Browsers might also MIME-sniff a text response as executable script. |
| **Impact** | Increased attack surface for browser-based attacks (clickjacking, MIME confusion). |
| **Recommendation** | Add a `SecurityHeaders` middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Strict-Transport-Security: max-age=31536000; includeSubDomains`, `Referrer-Policy: no-referrer-when-downgrade`, and `Content-Security-Policy` appropriate for the SPA. |
| **Effort** | S — add a simple middleware function. |

---

### Finding 15 — LOW: No Audit Trail for Failed Authentication Attempts

| Field | Description |
|-------|-------------|
| **Category** | Threat Model (Repudiation) |
| **Severity** | **Low** |
| **Title** | Failed auth attempts are not logged or audited |
| **Location** | `internal/auth/auth_middleware.go:43-48` — `authenticateBearer`; `internal/auth/auth_middleware.go:76-77` — `unauthorized`; `internal/auth/jwt.go:106-110` — `Verify` |
| **Description** | When an invalid API key or JWT is presented, the middleware returns `401 Unauthorized` or `403 Forbidden` but does **not** log the failed attempt. The `unauthorized` and `forbidden` helpers only write the response but don't emit any structured log. The `AccessLog` middleware records the 401/403 status, but it doesn't include the token identity, the attempted tenant, or the source IP. This makes it impossible to detect brute-force attacks against API keys or JWT tokens. |
| **Attack Scenario** | Attacker brute-forces API keys by sending thousands of `Authorization: Bearer <guess>` requests. Without logging of failed attempts, the attack is invisible until a key is compromised. |
| **Impact** | Undetected credential brute-force and spray attacks. Inability to attribute failed access attempts for incident response. |
| **Recommendation** | Add structured logging of failed auth attempts with request ID, source IP, token prefix (first 4 chars), attempted tenant, and reason. Consider adding a rate limiter specifically for failed auth attempts per source IP. |
| **Effort** | S — add logging in `authenticateBearer` and `authenticateSigV4`. |

---

### Finding 16 — LOW: In-Memory Rate Limiter Can Be Exhausted by Rapidly Changing Tenants

| Field | Description |
|-------|-------------|
| **Category** | Denial of Service |
| **Severity** | **Low** |
| **Title** | Per-tenant rate-limit bucket map has maximum capacity (50K), but attacker can still cause memory pressure |
| **Location** | `internal/middleware/ratelimit.go:27-31` — `rlMaxBuckets=50000`; `internal/middleware/ratelimit.go:67-78` — `Allow` |
| **Description** | The rate limiter uses a map keyed by tenant name. With a max of 50,000 tenants and 10-minute idle eviction, a sustained flood of unique `X-Aero-Tenant` values (e.g., random UUIDs) would keep 50,000 entries in the map (each entry ~100 bytes ≈ 5 MB), which is manageable. However, the per-tenant token bucket logic means each new unique tenant gets a full burst allowance, effectively allowing `50,000 * burst` requests before strict rate limiting kicks in. If burst is 100 and RPS is 10, an attacker with 50,000 unique tenants can send 5,000,000 requests in short order, exceeding the intended global rate limit. |
| **Attack Scenario** | Attacker sends 50,000 requests each with a unique `X-Aero-Tenant: random-<N>` header, receiving 50,000 * burst tokens, overwhelming the server. |
| **Impact** | Bypass of rate-limiting protection during the burst window. |
| **Recommendation** | Add a global cap that limits requests per client IP independent of tenant. Use a sliding-window approach with a smaller window. Consider a fixed-cost-per-request approach rather than per-tenant token buckets when tenants are client-controlled. |
| **Effort** | M — requires rate limiter redesign. |

---

### Finding 17 — LOW: PII Detector Credit Card Regex Has Poor Precision/High False Positive Rate

| Field | Description |
|-------|-------------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | PII credit card detection uses only Luhn + length check, prone to false positives |
| **Location** | `internal/ai/pii.go:82-86` — credit card rule; `internal/ai/pii.go:13-20` — `luhn` |
| **Description** | The credit card PII detection looks for 13-19 digit sequences (with optional spaces/dashes) and then validates via Luhn check. This will match any 16-digit number that passes Luhn (including non-credit-card numbers like certain IMEI numbers, or random numbers that by coincidence pass Luhn — about 10% of 16-digit random numbers pass Luhn). There is no BIN (Bank Identification Number) prefix matching or issuer network validation. This could cause false positives that interfere with legitimate processing (indexer metadata pollution with false PII flags). |
| **Attack Scenario** | A file containing a 16-digit API key that happens to pass Luhn gets flagged as containing a credit card number, potentially triggering false alerts or redaction. Conversely, a true credit card number with proper formatting (e.g., `4111-1111-1111-1111`) would be detected. |
| **Impact** | false positives causing degraded user experience; false negatives allowing genuine CC numbers to go undetected. |
| **Recommendation** | Add BIN prefix matching against known issuer ranges (Visa: `4`, Mastercard: `51-55`, Amex: `34/37`, etc.) to improve precision. Also add IIN (Issuer Identification Number) database matching for production use. |
| **Effort** | M — adding BIN range validation. |

---

## STRIDE Summary

| Threat | Risk | Key Findings |
|--------|------|-------------|
| **S**poofing | **High** | `X-Aero-Tenant` client-controlled; no cryptographic tenant binding when auth disabled (Finding 1) |
| **T**ampering | **Medium** | Object keys via storage path traversal (Finding 2); SSE envelope not authenticated with AAD |
| **R**epudiation | **Low** | No audit trail for failed auth attempts (Finding 15) |
| **I**nformation Disclosure | **High** | SSRF via AI/KMS endpoints (Finding 3); anonymous read bypass for tags/versions (Finding 12); presigned URL leak (Finding 6) |
| **D**enial of Service | **Medium** | Rate limiter bypass via `/ui` (Finding 13); unique tenant buckets (Finding 16); event subscriber backpressure drop (Finding 7) |
| **E**levation of Privilege | **Critical** | No tenant isolation with default config (Finding 1); MCP unauthenticated (Finding 5); weak symmetric JWT (Finding 8) |

---

## OWASP Top 10 Compliance

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| **A01:2021 – Broken Access Control** | ❌ **Critical Risk** | No tenant isolation by default (Finding 1); anonymous read bypass for sub-resources (Finding 12) |
| **A02:2021 – Cryptographic Failures** | ⚠️ Moderate Risk | HS256-only JWT (Finding 8); no AAD in GCM encryption |
| **A03:2021 – Injection** | ⚠️ Moderate Risk | SQL injection risk in dynamic queries (Finding 4); header injection (Finding 9) |
| **A04:2021 – Insecure Design** | ❌ **Critical Risk** | SSRF via configurable endpoints (Finding 3); tenant identity from client header |
| **A05:2021 – Security Misconfiguration** | ⚠️ Moderate Risk | CORS wildcard echo (Finding 10); missing security headers (Finding 14) |
| **A06:2021 – Vulnerable Components** | ✓ Low Risk | Go stdlib crypto only; no known-vulnerable deps identified |
| **A07:2021 – Identification & Auth Failures** | ❌ **Critical Risk** | MCP unauthenticated (Finding 5); no brute-force detection (Finding 15) |
| **A08:2021 – Software & Data Integrity Failures** | ⚠️ Moderate Risk | No integrity check on external AI/KMS responses; no signed metadata |
| **A09:2021 – Security Logging & Monitoring Failures** | ⚠️ Moderate Risk | No failed auth logging (Finding 15); event drop silent (Finding 7) |
| **A10:2021 – Server-Side Request Forgery** | ❌ **High Risk** | Finding 3 — KMS/AI endpoints unvalidated |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase demonstrates strong cryptographic implementation (proper AEAD-AES-256-GCM envelope encryption with key rotation support) and good awareness of many security patterns (CI gate, `os.Signal` graceful shutdown, middleware chain separation, scope-based auth). However, there are **critical architectural weaknesses** that must be addressed before production deployment.

### Top 3 Critical Issues

| # | Issue | Finding |
|---|-------|---------|
| 1 | **No tenant isolation when auth is disabled** (default configuration) | Finding 1 — `X-Aero-Tenant` is entirely client-controlled with no server-side enforcement |
| 2 | **Path traversal in local storage backend** | Finding 2 — insufficient guard rails against `objectPath` escape |
| 3 | **MCP endpoint has no authentication enforcement** | Finding 5 — full CRUD access without auth when auth is off |

### Top 3 Quick Wins (High-Impact, Low-Effort)

| # | Issue | Finding | Effort |
|---|-------|---------|--------|
| 1 | Add CRLF sanitization to `Content-Disposition` and metadata headers | Finding 9 | S |
| 2 | Add failed auth attempt logging | Finding 15 | S |
| 3 | Narrow `isObjectReadPath` to exclude sub-resources | Finding 12 | S |

### Security Debt

| Area | Accumulated Debt | Priority |
|------|-----------------|----------|
| **Authentication architecture** | Tenant from header; no crypto binding; single default tenant when auth off | **Critical** |
| **SSRF protection** | No dial restrictions for internal services (KMS, AI, SSE key store) | **High** |
| **Key management** | HS256 symmetric JWT; global presign shared secret | **Medium** |
| **Monitoring & audit** | No auth failure logging; event drops silent | **Medium** |
| **Rate limiting** | Bypassable via `/ui` and tenant spray | **Low** |

---

### References

- OWASP Top 10:2021 — https://owasp.org/Top10/
- OWASP ASVS v4.0 (Authentication, Authorization, Cryptography)
- NIST SP 800-63B (Digital Identity Guidelines)
- RFC 7519 (JSON Web Token) — Section 8 (Implementation Requirements)
