Now I have a comprehensive understanding of the codebase. Let me compile the full security review.

---

# Security Review: AeroVault Object Storage System

**Review scope:** `/home/u1/aero-vault/cmd/server/main.go`, `internal/` (all packages)
**Reviewer:** Principal Security Engineer
**Date:** 2026-07-12

---

## Finding 1 — Critical: MCP stdio mode bypasses all authentication

| Field | Value |
|-------|-------|
| **Category** | Authentication / Authorization |
| **Severity** | **Critical** |
| **Title** | MCP stdio transport operates with zero authentication |
| **Location** | `cmd/server/main.go:354-410` (`runMCP()`) · `internal/mcp/transport.go:17-45` · `internal/mcp/server.go:133` |
| **Description** | When run as `aero-vault mcp`, the server creates a full `FileService` with **no auth middleware** and **no tenant context**. The `tenantFor()` method falls back to `"default"`. All MCP tools (`write_file`, `delete_file`, `read_file`, `list_files`, `search`, `chat`) are accessible over stdin with zero authentication. The HTTP `/mcp` endpoint is behind the auth middleware chain, but the stdio path is not. |
| **Attack Scenario** | Any local process that can write to the `aero-vault mcp` process's stdin can read, write, or delete any object. If this runs in a container/SSH session where stdin is remotely accessible (e.g. via `docker exec`, SSH piping), any network attacker with access to that path can compromise all data. Claude Desktop / Cline use this path. |
| **Impact** | Complete unauthenticated data access — read, write, delete any object. AI budget can be exhausted via unrestricted `search`/`chat` calls. No audit trail for MCP actions. |
| **Recommendation** | Implement authentication for the stdio transport. Options: (a) require `--api-key` flag on `aero-vault mcp`, verified against the auth registry before dispatching; (b) implement MCP session-based auth where the client sends credentials in the `initialize` handshake; (c) at minimum, require `MCP_API_KEY` env var that gates all tool calls. |
| **Effort** | S (< 1 day) |

**Code evidence:**
```go
// cmd/server/main.go:354
func runMCP() error {
    cfg, err := config.Load()
    // ...
    svc := service.NewFileService(store, repo, logger)
    // No auth registry, no middleware, no tenant isolation
    server := mcp.NewServer(svc, repo, search, "default", logger) // hardcoded "default" tenant
    return mcp.ServeStdio(ctx, server, os.Stdin, os.Stdout) // stdin = no auth
}
```

---

## Finding 2 — Critical: Response header injection via user metadata

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Critical** |
| **Title** | User metadata values echo into HTTP response headers without CRLF sanitization |
| **Location** | `internal/api/rest/handler.go:930-937` (`writeMetadataHeaders`) · `internal/api/s3compat/handler.go:688-697` (`writeS3ObjectMeta`) |
| **Description** | User metadata values are written directly as HTTP response headers via `w.Header().Set("X-Meta-"+k, v)` (REST) and `w.Header().Set("x-amz-meta-"+k, v)` (S3). While Go's `net/http` `Header.Set()` rejects truly invalid header values, CR/LF sequences (`\r\n`, `\n`) embedded in metadata values can inject arbitrary HTTP headers, enabling HTTP response splitting. The `_aero_` prefix skip only protects system keys, not user-controlled values. |
| **Attack Scenario** | 1. Attacker uploads object with metadata: `X-Meta-Description: benign\r\nSet-Cookie: session=steal` <br>2. Any GET/HEAD to that object returns the injected `Set-Cookie` header <br>3. If served through a caching proxy (CDN, reverse proxy), the poisoned response can be served to other users (cache poisoning) |
| **Impact** | HTTP response splitting → cache poisoning, XSS (if content-type permits), session hijacking. Violates OWASP A03:2021 (Injection). |
| **Recommendation** | Sanitize metadata values before storing them or before writing as headers. Add CR/LF removal in the ingestion path: |
| **Effort** | S (< 1 day) |

**Fix:**
```go
func sanitizeMetadataValue(v string) string {
    v = strings.ReplaceAll(v, "\r", "")
    v = strings.ReplaceAll(v, "\n", "")
    return v
}

// Apply in writeMetadataHeaders and writeS3ObjectMeta
func writeMetadataHeaders(w http.ResponseWriter, meta map[string]string) {
    for k, v := range meta {
        if strings.HasPrefix(k, "_aero_") { continue }
        w.Header().Set("X-Meta-"+k, sanitizeMetadataValue(v))
    }
}
```

Also apply during `extractMetadataHeaders()` / `extractMetaHeaders()` so the malicious value never reaches storage:
```go
out[strings.TrimPrefix(lower, "x-amz-meta-")] = sanitizeMetadataValue(v[0])
```

---

## Finding 3 — High: Admin endpoints accessible without authentication (default config)

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | `requireAdmin` returns `true` when auth is disabled |
| **Location** | `internal/api/rest/admin.go:400-407` (`requireAdmin`) · `internal/auth/auth.go:100-103` (`Enabled()`) |
| **Description** | The `requireAdmin` guard checks `h.reg.Enabled()`. When `AUTH_KEYS` is empty (default dev config), the registry's `enabled` field is `false`, `Enabled()` returns `false`, and `requireAdmin` returns `true` unconditionally — granting admin access to anyone who can reach the server. This exposes all admin endpoints: `POST /v1/admin/keys` (create API keys), `POST /v1/admin/jwt` (issue JWT), `POST /v1/admin/tenants` (manage tenants), `GET /v1/admin/audit` (read audit log), `POST /v1/admin/jobs/{id}/retry`. |
| **Attack Scenario** | Attacker on the same network: `curl -X POST http://host:8080/v1/admin/keys -d '{"token":"evil-key","tenant":"*","scopes":["admin"]}'` → gains permanent admin access. Then: `curl -X DELETE http://host:8080/v1/files/some-key?hard=1` (delete all data). Or: `curl http://host:8080/v1/admin/jwt -d '{"sub":"evil","tenant":"*","scopes":["admin"]}'` → obtains signed JWT for persistent access. |
| **Impact** | Complete system compromise — data deletion, tenant manipulation, key creation, audit log tampering, budget manipulation. |
| **Recommendation** | When auth is disabled, require admin operations to use localhost-only binding or a Unix socket. Alternatively, require at least one mechanism: if `AUTH_KEYS` is empty AND no JWT secret AND no SigV4, reject all admin operations with 403. |
| **Effort** | S (< 1 day) |

**Fix:**
```go
func (h *AdminHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
    // When auth is completely disabled, only allow admin from localhost
    if !h.reg.Enabled() {
        host, _, err := net.SplitHostPort(r.RemoteAddr)
        if err != nil || host != "127.0.0.1" && host != "::1" {
            writeJSON(w, http.StatusForbidden, errorBody{Error: errorPayload{
                Code: "AccessDenied", Message: "admin: auth must be configured for remote access",
            }})
            return false
        }
        return true
    }
    k, ok := auth.FromContext(r.Context())
    if !ok || !k.Has(auth.ScopeAdmin) {
        writeJSON(w, http.StatusForbidden, ...)
        return false
    }
    return true
}
```

---

## Finding 4 — High: Middleware chain order causes tenant isolation bypass

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Tenant middleware runs before Auth middleware, corrupting tenant context |
| **Location** | `cmd/server/main.go:258-271` (`applyMiddleware`) · `internal/middleware/middleware.go:47-57` (`Tenant`) · `internal/auth/auth_middleware.go:72-80` (`authenticateBearer`) |
| **Description** | The middleware chain in `applyMiddleware()` applies middlewares in this order (outermost first): `access_log → concurrency → recoverer → otel → rate_limit → **tenant** → **auth** → cors → request_id`. The `Tenant` middleware extracts `X-Aero-Tenant` from the **original request headers** and stores it in context. Then `Auth` middleware runs **after** Tenant, and in `authenticateBearer()` it may override `req.Header.Set("X-Aero-Tenant", k.Tenant)` — but the **context already has the wrong tenant value**. All handlers use `mw.TenantFrom(ctx)` which returns the pre-auth value. This means: if a key belongs to `tenant=acme` but the request has no tenant header, tenant defaults to `"default"` — and the auth-override is lost. |
| **Attack Scenario** | 1. User with key `acme-key:acme:read+write` sends a request **without** `X-Aero-Tenant` header <br>2. Auth resolves the key, correctly detects the key belongs to tenant `acme`, overrides the header to `"acme"` <br>3. But Tenant middleware already extracted `"default"` into context <br>4. Handler calls `mw.TenantFrom(ctx)` → gets `"default"` <br>5. Object is created/accessed in the **wrong tenant** — either cross-tenant data access or data written to wrong partition |
| **Impact** | Cross-tenant data contamination, potential privilege escalation if an attacker can craft requests that land in another tenant's storage partition while authenticated for their own. This violates the tenant isolation model. |
| **Recommendation** | Two options: **(a)** Move Tenant middleware **after** Auth so the tenant override is visible to handlers — this requires reordering the chain to match the documented order: `RequestID → CORS → Auth → Tenant → ...`; **(b)** Have the Auth middleware **also** update the context (not just the header), so both the header and context reflect the key's tenant. Option (a) is architecturally cleaner and aligns with the documented chain order. |
| **Effort** | M (1-3 days — requires careful reordering and test updates) |

**Relates to:** AGENTS.md §2.5 "Middleware 链顺序固定" (but the documented order doesn't match the code).

---

## Finding 5 — High: SQL LIKE wildcard injection in key prefix enumeration

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | User-controlled prefix allows LIKE wildcard characters, enabling key enumeration |
| **Location** | `internal/repository/sql_objects.go:125-132` (`ListObjects` — `key LIKE $3 AND key > $4` where `$3 = prefix + "%"`) |
| **Description** | `ListObjects` uses `key LIKE $3` with `$3 = prefix + "%"`. While the query is parameterized (safe from SQL injection), SQLite and Postgres `LIKE` operations treat `%` and `_` as wildcards. An attacker providing `prefix=%` forces a full table scan across all keys in the tenant. A prefix of `___` matches any 3-character key. This allows enumerating keys the caller might not have been intended to discover through the API. |
| **Attack Scenario** | `GET /v1/files?prefix=%25` (URL-encoded `%`) → returns ALL object keys in the tenant  <br>`GET /v1/files?prefix=___` → returns all 3-character keys  <br>`GET /v1/files?prefix=secret_` → enumerates keys matching `secret<one char>` |
| **Impact** | Information disclosure — attackers can enumerate object keys beyond intended access patterns. On large datasets, `prefix=%` can cause a full table scan leading to DoS from CPU/memory exhaustion. |
| **Recommendation** | Escape `%` and `_` in the prefix before constructing the LIKE pattern: |
| **Effort** | S (< 1 day) |

**Fix:**
```go
func escapeLike(s string) string {
    s = strings.ReplaceAll(s, "%", "\\%")
    s = strings.ReplaceAll(s, "_", "\\_")
    return s
}
// Then: $3 = escapeLike(prefix) + "%"
```

---

## Finding 6 — High: Internal error details leaked to API clients

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Unclassified errors expose raw error messages to clients |
| **Location** | `internal/api/rest/handler.go:406-424` (`classify()` — default case returns `err.Error()`) · `internal/api/rest/admin.go` (many raw `err.Error()` returns) · `internal/api/s3compat/handler.go` (`writeS3Error`) |
| **Description** | The `classify()` function has a default case that returns `err.Error()` directly to the client as `InternalError`. Many admin handler error paths also return `err.Error()` to clients. This can leak SQL query fragments, file system paths, internal IP addresses, and stack traces. Violates OWASP A04:2021 (Insecure Design — Information Leakage). |
| **Attack Scenario** | Send a malformed request that triggers a database constraint violation: `GET /v1/files/../../etc/passwd` or similar edge case. Server responds with `{"error":{"code":"InternalError","message":"pq: relation \"x_objects\" does not exist"}}` — revealing database schema. |
| **Impact** | Assists attackers in reconnaissance for further attacks (schema enumeration, version fingerprinting, infrastructure discovery). |
| **Recommendation** | Replace the default case in `classify()` with a safe generic message. Always log the full error server-side: |
| **Effort** | S (< 1 day) |

**Fix:**
```go
func classify(err error) (string, string, int) {
    // ... existing cases ...
    default:
        slog.Error("unexpected error serving request", "err", err)
        return "InternalError", "an internal error occurred", http.StatusInternalServerError
    // was: return "InternalError", err.Error(), http.StatusInternalServerError
}
```

---

## Finding 7 — Medium: Pre-signed URL generation has no usage restrictions

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Any authenticated user can generate pre-signed URLs for any key |
| **Location** | `internal/api/rest/handler.go:154-185` (`Presign` handler) · `internal/auth/auth_middleware.go:68-70` (`checkScope` — POST requires write scope) |
| **Description** | The `POST /v1/files/*key/presign` endpoint is guarded by auth scope (POST → `write` scope required). Any user with write scope can generate pre-signed GET or PUT URLs for **any key**, with any expiration up to the configured max. There is no restriction on which keys can be pre-signed, no IP-binding on pre-signed URLs, and the signature uses HMAC with the local signing key (`SignKey`). If the signing key is compromised or a tenant user abuses pre-sign, they can distribute unlimited time-bound access URLs. |
| **Attack Scenario** | A tenant user with write scope generates a pre-signed GET URL for a sensitive object with 7-day expiry, then shares it publicly. There is no audit trail linking the pre-signed URL to the original requestor. |
| **Impact** | Uncontrolled token distribution, difficulty in auditing data access via pre-signed URLs, potential for abuse by compromised tenant accounts. |
| **Recommendation** | Add optional restrictions: (1) scope pre-signed URLs to specific key prefixes; (2) add IP-binding to pre-signed tokens; (3) log pre-signed URL generation in audit; (4) enforce maximum expiration period. |
| **Effort** | M (1-3 days) |

---

## Finding 8 — Medium: Gzip bomb / decompression bomb via `Content-Encoding: gzip`

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | Auto-decompression of gzip content without size limits |
| **Location** | `internal/service/file_crud.go:326-335` (auto-decompress in `Get()`) · `internal/service/file.go:126-140` (metadata service values `_aero_content_encoding`) |
| **Description** | When an object has `_aero_content_encoding: gzip` metadata, the `Get` method automatically decompresses it using `gzip.NewReader`. There is **no decompressed-size limit**. An attacker can upload a small gzip bomb (e.g., 10MB compressed expands to several GB decompressed) and trigger a memory exhaustion DoS by requesting it. The reader streams through `gzip.Reader` which decompresses in memory. |
| **Attack Scenario** | 1. Attacker uploads a gzip bomb with metadata `Content-Encoding: gzip` <br>2. Victim/crawler requests the object <br>3. Server auto-decompresses → memory exhaustion → OOM kill |
| **Impact** | Denial of service through memory exhaustion. Repeated attacks can crash the server. |
| **Recommendation** | Add a `MaxDecompressedSize` config option and limit decompression. Use `io.LimitReader` on the decompressed stream: |
| **Effort** | S (< 1 day) |

**Fix:**
```go
const maxDecompressedBytes = 500 * 1024 * 1024 // 500 MB
gr, err := gzip.NewReader(rc)
if err != nil { ... }
limitedReader := io.LimitReader(gr, maxDecompressedBytes)
// Use limitedReader instead of gr for further reading
```

---

## Finding 9 — Medium: Encryption buffers entire object in memory

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Denial of Service |
| **Severity** | **Medium** |
| **Title** | SSE encrypt/decrypt reads entire plaintext into memory before processing |
| **Location** | `internal/storage/encrypt.go:320-330` (`encryptReader` — `io.ReadAll(r)`) · `internal/storage/encrypt.go:335-340` (`decryptReader` — `io.ReadAll(r)`) |
| **Description** | Both `encryptReader()` and `decryptReader()` call `io.ReadAll()` on the entire input before encryption/decryption. For large objects (>1GB), this allocates a correspondingly large byte slice, risking OOM under concurrent access. While the GCM mode requires the full ciphertext for tag verification, this is a deliberate design trade-off. The code comments note this limitation ("fine for objects up to ~hundreds of MB"). |
| **Attack Scenario** | Upload 10 × 500MB objects with SSE enabled → server allocates 5GB+ for simultaneous encryption/decryption buffers → OOM. |
| **Impact** | Denial of service via memory exhaustion when SSE is enabled on large or numerous objects. |
| **Recommendation** | For objects exceeding a threshold (e.g., 64MB), use chunked encryption (AES-256-CTR + HMAC per chunk) that streams without full buffering. File this as a known limitation in documentation and add a configurable `SSEMaxMemory` limit that rejects objects exceeding it with a clear error. |
| **Effort** | L (> 3 days for streaming SSE) |

---

## Finding 10 — Medium: No rate limiting on MCP HTTP endpoint

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | MCP HTTP `/mcp` endpoint bypasses AI-specific rate limiter |
| **Location** | `cmd/server/main.go:201` (`r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))`) · `internal/api/rest/router.go:84-90` (AI rate limiter only applies to `/v1/search`, `/v1/chat`, `/v1/chat/stream`, `/v1/agent`, `/v1/lineage`) |
| **Description** | The `/mcp` HTTP endpoint is mounted directly on the root router, not under `/v1`, so it doesn't inherit the AI-specific rate limiter (`aiRL.Middleware()`). The MCP `search` and `chat` tools can be called without the independent AI RPS limits. The global rate limiter applies, but it's shared with all other requests. |
| **Attack Scenario** | Attacker sends 10,000 MCP POST requests with `tools/call` method `search` → consumes embedding API quota and degrades AI service for legitimate users — all while bypassing the dedicated AI rate limiter. |
| **Impact** | AI resource exhaustion, embedding API cost spike, degraded AI service quality. |
| **Recommendation** | Apply the AI rate limiter to the `/mcp` endpoint, or add a dedicated MCP rate limiter. Alternatively, route MCP through the same middleware chain as `/v1`. |
| **Effort** | S (< 1 day) |

---

## Finding 11 — Medium: No scope enforcement on multipart operations beyond method-level

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Multipart upload is not covered by idempotency protection |
| **Location** | `internal/api/rest/router.go:71-75` (multipart routes outside idempotency group) |
| **Description** | The idempotency middleware group covers `POST/PUT/DELETE /files/*`, but NOT `POST /multipart`, `PUT /multipart/{uploadID}/parts/{n}`, `POST /multipart/{uploadID}/complete`, or `DELETE /multipart/{uploadID}`. This means a network retry of `CompleteMultipart` can create duplicate object versions. This is a data integrity risk, not an auth bypass, but it enables subtle data corruption. |
| **Attack Scenario** | Client sends `CompleteMultipart`, network timeout occurs, client retries → two object versions created. The `write_file` MCP tool bypasses idempotency entirely (no idempotency key support). |
| **Impact** | Duplicate versions, data integrity issues, storage waste. |
| **Recommendation** | Add idempotency protection to `CompleteMultipart` and `UploadPart`. The `deep-production-gaps-v1.out.impl-plan.md` already identifies this as TASK-019/TASK-020/TASK-021. |
| **Effort** | M (1-3 days) |

---

## Finding 12 — Medium: Webhook HMAC signing is optional

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | Webhook delivery can operate without HMAC signing |
| **Location** | `internal/events/webhook.go:28-65` (`NewWebhook` — no secret required) · `internal/events/webhook.go:91-97` (`WithSecret` is optional) |
| **Description** | The webhook `WithSecret()` method is optional — if not called, outbound webhooks are sent without any HMAC signature. This means the receiving endpoint cannot verify that the webhook was genuinely sent by the AeroVault server. The webhook payload can be forged by anyone who can observe or guess the event format. |
| **Attack Scenario** | If webhook URLs are HTTPS (which they should be), the risk is limited. But if an attacker can MITM the connection or if the webhook URL is `http://`, they can replay or spoof webhook payloads to the receiver. |
| **Impact** | Webhook receiver cannot verify authenticity of events. Potential for forged event processing at the receiving end. |
| **Recommendation** | Make signing mandatory when a webhook URL is configured. Require `EVENTS_WEBHOOK_SECRET` as a required configuration when `EVENTS_WEBHOOK_URL` is set. Log a warning if webhook URL is set without a secret. |
| **Effort** | S (< 1 day) |

---

## Finding 13 — Medium: Path traversal protection is incomplete

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Key validation does not block all path traversal vectors |
| **Location** | `internal/service/file.go:150-157` (`validateKey`) |
| **Description** | The `validateKey` function blocks keys containing `..` or starting with `/`. However, it does not block: URL-encoded path separators (`%2F`), backslash path separators (`\` — relevant on Windows storage backends), null bytes (`\x00`), or symbolic link traversal in local storage. Additionally, the storage backends (especially `local`) use `path.Join(tenant, bucket, key)` which can produce unexpected paths with crafted inputs. |
| **Attack Scenario** | If a key contains `\..\..\..\etc\passwd` (backslash on a system where the local backend runs on Windows), or if the local storage backend follows symlinks placed in the storage directory. The null byte could truncate the path in C-based storage layers (though Go's path handling is safer). |
| **Impact** | Potential arbitrary file read/write on local storage backend, depending on OS and storage configuration. |
| **Recommendation** | Strengthen validation: reject keys with null bytes, backslashes on Windows, and URL-encoded separators. Add a max key length limit. On local storage, sanitize the full storage path to ensure it stays within the storage root. |
| **Effort** | S (< 1 day) |

---

## Finding 14 — Low: Limited PII detection scope

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | PII Detector covers only basic patterns |
| **Location** | `internal/ai/pii.go:49-58` (`NewPIIDetector` — 5 rules) |
| **Description** | The PIIDetector only detects: email, phone (loose international), credit card (with Luhn), US SSN, and IPv4 addresses. Missing patterns include: passport numbers, driver's license numbers, IBAN, bank routing numbers, national ID numbers, addresses, dates of birth, and region-specific PII. This means the PII redaction feature provides a false sense of security — data may still contain redacted PII that slips through. |
| **Attack Scenario** | A European user indexes documents containing Italian fiscal codes (`Codice Fiscale`, 16-char alphanumeric ID) which are treated as PII under GDPR. The detector misses them entirely because no regex rule exists for this pattern. The `MapPII` report returns "0 PII found" leading to a false sense of compliance. |
| **Impact** | False sense of PII compliance. GDPR/SOC2 auditors who discover unredacted PII in indexed content would flag this as a finding. |
| **Recommendation** | Document the limited scope of the PII detector in the product documentation. Add known patterns for target jurisdictions. Consider integrating with a dedicated PII detection service (Presidio, Amazon Comprehend, etc.) for production deployments. |
| **Effort** | S (< 1 day) for documentation; M (1-3 days) for additional patterns |

---

## Finding 15 — Low: Audit coverage gap for object CRUD

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Low** |
| **Title** | Regular object CRUD operations are not audited |
| **Location** | `internal/api/rest/admin.go:424-434` (only admin actions are audited) · `internal/api/rest/handler.go` (object CRUD — no audit calls) |
| **Description** | The audit log only covers admin operations: tenant management, key management, quota changes, webhook failures. Regular object CRUD (PUT, GET, DELETE, multipart) is not audited. An attacker who gains write access to the system can delete objects without leaving an audit trail. |
| **Attack Scenario** | Compromised API key with write scope deletes all objects in a bucket. No audit record exists to determine how, when, or by whom the deletion occurred. |
| **Impact** | Inability to perform forensic analysis after a security incident. Violates A01:2021 (Broken Access Control — lack of audit trail) and SOC2/PCI-DSS monitoring requirements. |
| **Recommendation** | Add optional audit logging for all object mutations (PUT, DELETE, multipart completions) configurable via `AUDIT_OBJECT_OPS` boolean. When enabled, every mutation logs tenant, actor, key, action, timestamp. Consider also logging GET for sensitive paths. |
| **Effort** | M (1-3 days) |

---

## Finding 16 — Low: JWT only supports HS256 (symmetric)

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | JWT support limited to HS256 — no asymmetric key support |
| **Location** | `internal/auth/jwt.go:40-50` (`jwtHeader` — hardcoded `HS256`) |
| **Description** | The JWT implementation only supports HMAC-SHA256 (HS256). This is a symmetric algorithm: the same secret is used for signing and verification. This means: (a) any service that can verify tokens can also forge them; (b) the secret must be shared with the JWT issuer, creating a broader attack surface; (c) no support for JWKS/rotation without redeploying. |
| **Attack Scenario** | If the `AUTH_JWT_SECRET` leaks (e.g., via env var dump, debug endpoint, or insider threat), the attacker can forge tokens for any tenant with any scope, including admin. |
| **Impact** | Forged JWT tokens lead to complete authentication bypass if the secret is compromised. |
| **Recommendation** | Add RS256/ES256 support for production deployments where asymmetric keys separate the signing and verification roles. This allows the verification key to be public (or at least read-only) while the signing key stays with the IdP. |
| **Effort** | M (1-3 days) |

---

## STRIDE Threat Model Summary

| Category | Threat | Risk Level | Key Mitigation |
|----------|--------|------------|----------------|
| **S**poofing | MCP stdio accepts requests with no identity | **Critical** | Add auth to stdio transport |
| **S**poofing | No webhook HMAC → receiver can't verify sender | Medium | Make HMAC mandatory |
| **T**ampering | Metadata header injection can alter responses | **Critical** | Sanitize CRLF in metadata |
| **T**ampering | No read-path integrity check (ETag verification) | Low-Medium | Configuration `StorageVerifyOnRead` |
| **R**epudiation | No audit for object CRUD operations | Low-Medium | Add optional audit logging |
| **R**epudiation | MCP actions leave no audit trail | Medium | Add MCP audit logging |
| **I**nformation Disclosure | Error messages leak internal details | **High** | Replace raw errors in `classify()` |
| **I**nformation Disclosure | LIKE wildcard injection in prefix enumeration | **High** | Escape `%` and `_` in LIKE patterns |
| **I**nformation Disclosure | Metadata echo in response headers | **Critical** | Sanitize metadata values |
| **D**enial of Service | Gzip bomb via auto-decompression | Medium | Add `MaxDecompressedSize` |
| **D**enial of Service | Full-buffer encryption on large objects | Medium | Chunked SSE for large files |
| **D**enial of Service | LIKE `%` prefix causes full table scan | Medium | Escape wildcards (overlaps with Info Disclosure) |
| **D**enial of Service | No per-IP rate limiting on public endpoints | Medium-Low | Add IP-based rate limiting |
| **E**levation of Privilege | Middleware chain order: tenant before auth | **High** | Reorder middleware chain |
| **E**levation of Privilege | Admin endpoints open when auth disabled | **High** | Gate admin to localhost when auth off |
| **E**levation of Privilege | Every key with write scope can pre-sign any key | Medium | Add key prefix restrictions |

---

## Compliance Checklist

| Standard | Requirement | Status |
|----------|-------------|--------|
| **OWASP Top 10 A01:2021** | Broken Access Control | ❌ Multi-tenant isolation broken (Finding 4 — middleware order) |
| **OWASP Top 10 A03:2021** | Injection | ❌ CRLF injection via metadata (Finding 2), LIKE injection (Finding 5) |
| **OWASP Top 10 A04:2021** | Insecure Design — Information Leakage | ❌ Raw errors leaked (Finding 6) |
| **OWASP Top 10 A05:2021** | Security Misconfiguration | ❌ Admin open (Finding 3), no mandatory webhook HMAC (Finding 12) |
| **OWASP Top 10 A06:2021** | Vulnerable Components | ✅ Go stdlib mostly used, chi/v5 well-maintained |
| **OWASP Top 10 A07:2021** | Identification & Authentication Failures | ❌ MCP stdio has no auth (Finding 1) |
| **OWASP Top 10 A09:2021** | Security Logging & Monitoring Failures | ⚠️ No object CRUD audit (Finding 15), no MCP audit |
| **GDPR Art. 32** | Security of processing | ⚠️ PII detection is incomplete (Finding 14) |
| **PCI-DSS Req. 7** | Restrict access by need-to-know | ⚠️ Pre-signed URL abuse (Finding 7) |
| **PCI-DSS Req. 10** | Track and monitor access | ⚠️ No object-level audit (Finding 15) |
| **SOC2 CC6** | Logical & physical access controls | ⚠️ Tenant isolation risk (Finding 4) |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase has strong architectural foundations (opt-in defaults, single service entry point, contract-tested storage interfaces), but contains **2 critical vulnerabilities**, **3 high-severity issues**, and several medium-severity concerns that must be addressed before production deployment.

### Top 3 Critical Issues

| # | Finding | Effort | Impact |
|---|---------|--------|--------|
| 1 | **MCP stdio has zero authentication** (Finding 1) | S | Complete unauthenticated data access via `aero-vault mcp` |
| 2 | **Response header injection via metadata** (Finding 2) | S | HTTP response splitting → cache poisoning, XSS |
| 3 | **Middleware chain order breaks tenant isolation** (Finding 4) | M | Cross-tenant data access, privilege escalation |

### Top 3 Quick Wins

| # | Finding | Effort | Fix |
|---|---------|--------|-----|
| 1 | **Header injection** (Finding 2) | S | Add `sanitizeMetadataValue()` — 4 lines of code |
| 2 | **Raw error leakage** (Finding 6) | S | Replace `err.Error()` with generic message in `classify()` default |
| 3 | **Admin open when auth disabled** (Finding 3) | S | Gate admin to localhost when `auth.Enabled() == false` |

### Security Debt

| Issue | Effort | Priority |
|-------|--------|----------|
| SSE buffering entire objects in memory (Finding 9) | L > 3 days | Medium |
| No object CRUD audit trail (Finding 15) | M 1-3 days | Medium |
| No multi-part upload idempotency (Finding 11) | M 1-3 days | Medium |
| Pre-signed URL restrictions (Finding 7) | M 1-3 days | Low-Medium |
| Limited PII detection scope (Finding 14) | S-M | Low (document as known limitation) |
| JWT asymmetric key support (Finding 16) | M 1-3 days | Low (acceptable for internal SSO) |

**Note:** Many of these findings correlate with the `deep-production-gaps-v1` expansion work. The architecture plan (`docs/results/deep-production-gaps-v1.out.impl-plan.md`) already identifies the idempotency gap (TASK-019/020/021), CORS middleware reordering (TASK-008/009/010), and read-path integrity (TASK-001-007). Several security findings (Findings 1, 2, 3, 4, 6) should be prioritized **before** those feature expansions, as they represent fundamental security hardening rather than feature work.

### Priority Remediation Path

1. **Week 1 (Days 1-2):** Fix Findings 1, 2, 3, 6 — all S-effort, critical/high severity
2. **Week 1 (Days 3-5):** Fix Findings 5, 12, 13 — all S-effort, medium severity
3. **Week 2:** Fix Findings 4, 7 — middleware chain reorder + scope hardening
4. **Backlog:** Findings 8, 9, 10, 11, 14, 15, 16
