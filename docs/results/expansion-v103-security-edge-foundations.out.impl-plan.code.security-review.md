Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# Security Review Report: AeroVault

## Executive Summary

| Dimension | Rating |
|-----------|--------|
| **Overall Security Posture** | **Needs Improvement** |
| **Auth & Authorization** | Good foundation, uneven enforcement |
| **Input Validation** | Good |
| **Cryptography** | Good (with minor gaps) |
| **Session/Identity** | Fair |
| **Data Protection** | Good |
| **Threat Exposure** | Multiple protocol bypasses |

---

## Detailed Findings

### Finding #1: WebDAV Bypasses Entire Middleware Chain (Auth, Tenant, RateLimit, CORS, OTel)

| Field | Value |
|-------|-------|
| **Category** | Authentication / Authorization |
| **Severity** | **Critical** |
| **Title** | WebDAV handler bypasses auth, tenant, rate limiting, CORS, and access log middleware |
| **Location** | `cmd/server/main.go:119-129` (`buildDispatcher`) + `internal/api/webdav/dav.go:29-56` |
| **Description** | `buildDispatcher` intercepts ALL requests matching the WebDAV prefix (`cfg.WebDAV.Prefix`) BEFORE they reach the chi router, which is the only path that goes through `applyMiddleware`. The middleware chain (RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog) is never applied to WebDAV requests. |
| **Attack Scenario** | An attacker with network access can mount the WebDAV share (e.g., macOS Finder "Connect to Server") without any authentication. They can read, write, rename, and delete any object via the WebDAV protocol. In a multi-tenant deployment, they can set `X-Aero-Tenant: any-tenant` since the Tenant middleware never runs. |
| **Impact** | Complete bypass of all security controls for all WebDAV operations. Unauthenticated arbitrary file read/write/delete. No rate limiting. No audit logging. No CORS protection. |
| **Recommendation** | Move WebDAV routing into the chi router under the `r.Mount()` pattern, exactly like the S3 and REST routes. Remove the `buildDispatcher` pre-routing intercept and register WebDAV handlers through the normal chi middleware chain. The handler itself already reads `X-Aero-Tenant` via `mw.TenantFrom()`, so it will properly extract tenant when the middleware runs. |
| **Effort** | M (1-2 days) |

**Code snippet of the bypass:**
```go
// cmd/server/main.go:119-129
func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if davH != nil && cfg.WebDAV.Prefix != "" {
            p := req.URL.Path
            if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
                davH.ServeHTTP(w, req)  // ⚠️ BYPASSES ALL MIDDLEWARE
                return
            }
        }
        r.ServeHTTP(w, req)
    })
}
```

---

### Finding #2: Object Lock Legal Hold Stored as Overwritable Metadata

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Authorization |
| **Severity** | **High** |
| **Title** | Legal hold implemented as metadata field `_aero_legal_hold`, bypassable via overwrite |
| **Location** | `internal/api/s3compat/handler.go:93-99`, `internal/service/file_crud.go:371` |
| **Description** | The S3 handler stores Legal Hold status as `meta["_aero_legal_hold"] = "ON"` in the object metadata map. The `hardDeleteObject` function checks this metadata to block deletion. However, a PUT overwrite with `x-amz-object-lock-legal-hold` absent or set to "OFF" clears this metadata field entirely, removing the legal hold protection without any override authorization check. There is also no GOVERNANCE vs COMPLIANCE mode distinction — the expansion document already identifies this as a "pseudo-governance model." |
| **Attack Scenario** | 1. User uploads object with Legal Hold ON → object protected from deletion. 2. An attacker with write access to the bucket does a simple PUT (overwrite) without the Legal Hold header → the `_aero_legal_hold` metadata key is absent in the new PUT's metadata map → the metadata field is effectively cleared. 3. The old blob is replaced, and legal hold is gone. 4. The attacker can now delete the object via normal DELETE. |
| **Impact** | Legal hold provides no real protection. Compliance-governed data retention is illusory. Violates SEC 17a-4 / WORM storage requirements. |
| **Recommendation** | (1) Move Legal Hold to a dedicated column in the `objects` table (e.g., `legal_hold INTEGER DEFAULT 0`), separate from metadata. (2) Implement GOVERNANCE and COMPLIANCE retention modes with proper bypass authorization for GOVERNANCE mode. (3) In the `Put` path in `file_crud.go`, preserve the existing Legal Hold and LockedUntil state from the current object when an overwrite doesn't explicitly set new values. |
| **Effort** | M (2-3 days) |

---

### Finding #3: Bucket Policy Only Checks Source IP, Not Full Condition Context

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Policy condition evaluation limited to IP address; full IAM condition engine unimplemented |
| **Location** | `internal/auth/policy.go:67-100` (`Eval`, `matchesConditions`), `internal/auth/condition.go` (full engine, unused by Policy) |
| **Description** | `Policy.Eval(action, sourceIP string)` only accepts sourceIP and checks only `IpAddress`/`NotIpAddress` conditions. Meanwhile, `condition.go` has a complete IAM condition engine supporting 18+ operators (StringEquals, Bool, DateGreaterThan, ArnLike, etc.) and a full `ConditionContext` struct with `SecureTransport`, `Referer`, `UserAgent`, `CurrentTime`, `MultiFactorAuthPresent`, etc. This engine is NOT wired into the policy evaluation path. Users can set `"Bool": {"aws:SecureTransport": "true"}` in their bucket policy, and it will be silently ignored (evaluated as "no match" → implicit deny, which incorrectly blocks the request OR — worse — if no statement matches, it defaults to Allow in `checkBucketPolicy` with its `err == nil` check). |
| **Attack Scenario** | A bucket policy specifies `"Condition": {"Bool": {"aws:SecureTransport": "true"}}` to enforce HTTPS-only access. The condition is parsed but not evaluated (the `matchesConditions` function falls through to `return true` since `IpAddress`/`NotIpAddress` are the only operators checked). HTTPS enforcement is silently defeated, and objects can be accessed over plain HTTP. |
| **Impact** | Condition-based access controls specified in bucket policies may be silently ineffective, creating a false sense of security. |
| **Recommendation** | Wire the condition engine from `condition.go` into `Policy.Eval()`. Replace the inline `matchesConditions` method with a call to the `ConditionBlock.Compile()` pipeline. Add a test matrix covering all condition operators against `ConditionContext` built from the actual HTTP request. |
| **Effort** | M (2-3 days) |

---

### Finding #4: Bucket Policy Not Enforced on WebDAV or MCP

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Bucket policy enforcement only in S3 and REST handlers; WebDAV and MCP bypass |
| **Location** | `internal/api/rest/handler.go` (`checkBucketPolicy`), `internal/api/s3compat/handler.go` (`checkBucketPolicy`), `internal/api/webdav/dav.go`, `internal/mcp/server.go` |
| **Description** | Both the REST and S3 handlers call `checkBucketPolicy()` before every operation (GET, PUT, DELETE, etc.). However, WebDAV (`dav.go`) and MCP (`mcp/server.go`) never call this function. Since WebDAV also bypasses the middleware chain entirely (Finding #1), bucket policies are completely unevaluated for WebDAV traffic. MCP goes through the middleware but skips bucket policy checks. |
| **Attack Scenario** | A bucket policy has a Deny rule blocking `s3:DeleteObject` from external IP ranges. An attacker who can reach the WebDAV endpoint (which has no auth — Finding #1) can delete objects at will, bypassing the policy. Alternatively, an MCP client (with valid auth) can perform operations unrestricted by the bucket policy. |
| **Impact** | Bucket policies are only half-enforced. WebDAV and MCP serve as policy bypass vectors. |
| **Recommendation** | Add `checkBucketPolicy` calls to WebDAV (`OpenFile`, `RemoveAll`, `Rename`) and MCP (`callTool` switch cases). For a more robust approach, extract policy checking into the middleware layer so it applies uniformly across all protocols. |
| **Effort** | M (1-2 days) |

---

### Finding #5: No SSRF Protection on Outbound HTTP Clients

| Field | Value |
|-------|-------|
| **Category** | Input Validation / Threat Model |
| **Severity** | **High** |
| **Title** | Multiple outbound HTTP clients accept user-configurable URLs without validation |
| **Location** | `cmd/server/main.go:232-233` (`buildEmbedder`), `cmd/server/main.go:254-255` (`buildLLM`), `cmd/server/main.go:264-265` (`buildReranker`), `internal/storage/secret.go:118-145` (`newHTTPProvider`), `internal/storage/kms.go`, internal/ai/extractor_remote.go |
| **Description** | The system makes outbound HTTP requests to user-configured URLs for: AI embedding (`AI_ENDPOINT`), LLM chat (`AI_CHAT_ENDPOINT`), re-ranking (`AI_RERANK_ENDPOINT`), content extraction (`AI_EXTRACTOR_ENDPOINT`), encryption key retrieval (`STORAGE_LOCAL_SSE_KEY_URL`), and KMS operations (`STORAGE_LOCAL_SSE_KMS_URL`). None of these perform URL validation (no allowlist, no hostname verification for plain HTTP, no private IP rejection). An attacker who can set these env vars (or a supply-chain compromise) can make the server connect to internal services. |
| **Attack Scenario** | An attacker with configuration access sets `AI_ENDPOINT=http://169.254.169.254/latest/meta-data/` (AWS metadata service). The embedder sends requests to the metadata endpoint, exposing cloud provider credentials. Or they set `AI_CHAT_ENDPOINT=http://internal-db-host:5432/` to probe internal databases. |
| **Impact** | Server-Side Request Forgery (SSRF). Internal network scanning, cloud metadata service access, potential credential leakage. |
| **Recommendation** | (1) Add URL validation in each HTTP client constructor: reject private IP ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 169.254.0.0/16) and link-local addresses. (2) Require HTTPS for all external endpoints by default (make HTTP opt-in with explicit warnings). (3) Add a configurable proxy/allowlist. (4) Add a timeout to all HTTP clients (most already have them, verify). |
| **Effort** | M (1-2 days) |

---

### Finding #6: MCP stdio Mode Has No Authentication

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | MCP stdio transport (`ServeStdio`) offers no authentication or authorization |
| **Location** | `internal/mcp/transport.go` (`ServeStdio`), `internal/mcp/server.go:141-193` (`toolWriteFile`, `toolDeleteFile`, `toolReadFile`) |
| **Description** | The stdio MCP transport reads JSON-RPC requests from stdin with zero authentication. Any process with access to the stdin/stdout pipe (e.g., unprivileged local process if launched via a shared pipe) can call `write_file`, `delete_file`, `read_file`, `list_files`, `search`, and `chat` tools as the default tenant ("default"). The `tenantFor()` function falls back to the hardcoded `"default"` tenant from the constructor in stdio mode since no `X-Aero-Tenant` middleware header exists. |
| **Attack Scenario** | A malicious process on the same machine (or a compromised Claude Desktop extension) can send JSON-RPC requests to the stdin of the running MCP server, reading, writing, or deleting any object in the "default" tenant without any key or token. |
| **Impact** | Unauthenticated local access to all data in the "default" tenant via the MCP protocol. |
| **Recommendation** | (1) For stdio mode, read API key from environment variable (`MCP_API_KEY`) and reject requests without a valid key. (2) For HTTP mode, ensure it's behind the middleware chain (it currently is). (3) Document that stdio mode is inherently trust-based and intended for local-only use. |
| **Effort** | S (half day) |

---

### Finding #7: SSE Master Key Passphrase in Environment Variable

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **Medium** |
| **Title** | Encryption master key derived from passphrase in environment variable |
| **Location** | `internal/storage/local.go` (SSEKey config), `internal/storage/secret.go:70-79` (`deriveSSEKey`), `internal/storage/secret.go:82-90` (`envProvider`) |
| **Description** | The default SSE encryption mode derives a 32-byte AES key from a passphrase stored in `STORAGE_LOCAL_SSE_KEY` environment variable using `HMAC-SHA256("aero-vault/sse/v1", passphrase)`. While HMAC derivation is sound, an env var is visible via `/proc/self/environ`, process listing (`ps aux`), core dumps, and container orchestrator metadata. The key ring file (`STORAGE_LOCAL_SSE_KEYFILE`) is more secure but still requires file permissions. There are no warnings in the default config or startup logs about the security implications of env-var-based key material. |
| **Attack Scenario** | An attacker who gains access to the host (e.g., via container escape, debug endpoint, or `/proc` read) dumps `/proc/<pid>/environ` and extracts `STORAGE_LOCAL_SSE_KEY`. They can decrypt every object encrypted under the `envProvider` key (all objects since there's a single key). |
| **Impact** | Complete compromise of encryption-at-rest if host is breached. |
| **Recommendation** | (1) Document that `STORAGE_LOCAL_SSE_KEY` is intended for development only and recommend key file or KMS for production. (2) Consider logging a warning at startup when using the env-var provider. (3) Add support for key rotation from the start (the `keyRingProvider` already exists — the default should encourage `STORAGE_LOCAL_SSE_KEYFILE` with a key ring JSON). |
| **Effort** | S (documentation + warning) |

---

### Finding #8: Anonymous Public Read Flagged as Auth Bypass Risk

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Anonymous public read must be gated by ACL check at every GET path |
| **Location** | `internal/auth/auth_middleware.go:70-73`, `internal/api/rest/handler.go` (`allowAnonymous`), rest of GET paths |
| **Description** | When `AUTH_ANONYMOUS_PUBLIC_READ=true`, unauthenticated requests to object GET/HEAD paths are admitted with an `anonCtxKey` marker. The handler uses `allowAnonymous()` to check ACLs. However, this depends on every GET path calling `allowAnonymous()` consistently. If any handler path (e.g., `GET /v1/buckets/{bucket}/versions` or `GET /v1/files/{key}/versions`) fails to call `allowAnonymous()`, anonymous access could leak object data or metadata without ACL enforcement. |
| **Attack Scenario** | An anonymous user accesses `GET /v1/files/sensitive-key/versions` — if this sub-resource dispatch path doesn't check `allowAnonymous()`, the version listing (which includes object metadata) is returned without ACL check. |
| **Impact** | Data leakage via unauthenticated access to object metadata and versions. |
| **Recommendation** | (1) Audit all GET/HEAD paths in the REST handler to ensure `allowAnonymous()` is called. (2) Consider moving the anonymous check into middleware (after tenant extraction) so it's applied consistently at the framework level rather than per-handler. |
| **Effort** | S (audit + fix) |

---

### Finding #9: Idempotency Key Can Enable Replay Attacks

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Data Protection |
| **Severity** | **Medium** |
| **Title** | Idempotency key replay window depends on TTL configuration |
| **Location** | `cmd/server/main.go` (idempotency usage), internal/api/rest/router.go, idempotency middleware |
| **Description** | The idempotency mechanism caches response bodies keyed by `Idempotency-Key` header. When `IDEMPOTENCY_HASH_BODY` is enabled, body hash is folded into the key. But when it's not, the same idempotency key with different bodies replays the first response — allowing a form of replay. The TTL (`IDEMPOTENCY_TTL_HOURS`, default likely high) controls the replay window. If TTL is long (e.g., default 24h), a leaked idempotency key can be replayed within that window. |
| **Attack Scenario** | 1. User sends PUT with `Idempotency-Key: abc123` to create a sensitive file. 2. Response is cached. 3. Attacker intercepts the idempotency key (visible in client-side code, logs, etc.). 4. Within the TTL window, attacker replays the same idempotency key — server returns the cached success response without executing, but the attacker learns the response (including ETag, metadata) and potentially confirms the existence of a known object. |
| **Impact** | Information disclosure via idempotency response replay. |
| **Recommendation** | (1) Enable `IDEMPOTENCY_HASH_BODY` by default. (2) Keep TTL short (e.g., 1 hour). (3) Log when an idempotency key is replayed. |
| **Effort** | S (configuration change) |

---

### Finding #10: AccessLog `WriteAccessLog` Is a No-Op

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **Medium** |
| **Title** | Server access logging configured but produces no persistent audit trail |
| **Location** | `internal/repository/sql_buckets.go` (WriteAccessLog returns nil) |
| **Description** | The `WriteAccessLog` method in the repository layer exists, has a proper signature, and is referenced in the codebase, but its implementation is an empty function (`return nil`). The expansion analysis doc (direction B) identifies this as a "pipeline break" — the S3 logging configuration is accepted, persisted, returned on GET, but never drives actual log generation. There is no persistent audit trail of who accessed what object. |
| **Attack Scenario** | A security incident occurs (e.g., data exfiltration via Finding #1's WebDAV bypass). The security team checks access logs to trace the attack. There are no logs — `WriteAccessLog` was never implemented. No forensic trail exists. |
| **Impact** | No audit trail for security investigations. Non-compliant with SOC2, HIPAA, PCI DSS requirements for access logging. |
| **Recommendation** | (1) Implement `WriteAccessLog` with batch buffered writing to the configured target bucket (as outlined in expansion direction B). (2) Wire it into the `AccessLog` middleware. (3) Add recursion protection so writing a log entry doesn't trigger another access log event. |
| **Effort** | M (1-3 days) |

---

### Finding #11: Object Lock Bypass via Service Layer

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | lockBeforeOverwrite only checks non-versioned overwrites; versioned overwrites bypass |
| **Location** | `internal/service/file_crud.go:221-230` (`checkLockBeforeOverwrite`) |
| **Description** | `checkLockBeforeOverwrite` only checks object locks when `!versioning`. When a bucket has versioning enabled, it returns nil immediately without checking the current object's lock status. This means objects in versioned buckets with Object Lock enabled can be overwritten (creating a new version) without checking the current version's lock status. Additionally, the GOVERNANCE/COMPLIANCE distinction is absent entirely. |
| **Attack Scenario** | 1. Bucket has versioning + Object Lock enabled. 2. Object locked with GOVERNANCE mode for 30 days. 3. User with write access uploads a new version of the same object using PUT. 4. The old version is preserved (versioning), but the new version replaces the "current" object, effectively bypassing the lock for read access (GET returns the new unlocked version). |
| **Impact** | Object Lock can be bypassed in versioned buckets. Locked objects can be "superseded" by a newer version. |
| **Recommendation** | (1) In `checkLockBeforeOverwrite`, always check the current object's lock status regardless of versioning. (2) When versioning is enabled and the current version is locked, the overwrite should still be blocked (S3 behavior: locked objects cannot be overwritten even in versioned buckets). (3) Implement retention mode distinction (GOVERNANCE vs COMPLIANCE). |
| **Effort** | S-M (1 day) |

---

### Finding #12: ChunkCleaner Failure Logged But Silently Swallowed on Hard Delete

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | ChunkCleaner failure does not block hard delete, can cause index/data inconsistency |
| **Location** | `internal/service/file_crud.go:364-366` |
| **Description** | In `hardDeleteObject`, the `chunkCleaner.DeleteObjectChunks` call logs a warning on failure but does not block the deletion. This means the object metadata and storage blob are deleted even if the index chunks (BM25, vector) fail to clean up. The AGENTS.md explicitly says "ChunkCleaner.DeleteObjectChunks failure must not block hard delete." However, this creates orphaned index entries pointing to non-existent objects. When an administrator later tries to reconcile, there's no reference to clean up. |
| **Attack Scenario** | An attacker exploits a race condition or resource exhaustion to make chunk deletion fail during a large-scale delete. The DB and storage are cleaned, but vector/BM25 index chunks remain, pointing to non-existent objects. These are not cleaned by any existing GC path (the reconcile lifecycle only handles DB and storage). |
| **Impact** | Orphaned index entries that may cause search to return references to deleted objects. |
| **Recommendation** | (1) Add a dedicated GC for orphaned index chunks (scan chunks whose object_id doesn't exist in the objects table). (2) Log the error with structured fields so monitoring can alert. |
| **Effort** | M (1-2 days) |

---

### Finding #13: MCP chat tool exposed without AI config guard for tool listing

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Low** |
| **Title** | Chat tool properly gated but search tool listed even when AI is disabled |
| **Location** | `internal/mcp/server.go:73-76`, `internal/mcp/server.go:80-111` |
| **Description** | The `listTools` response conditionally includes the `chat` tool only when `s.chat != nil`, but always includes `search` as a listed tool even when `s.search == nil` (the `search` tool at line 73 only errors at call-time). The AGENTS.md says "chat tool only when `s.chat != nil`" which is implemented for chat but not for search. Though `toolSearch` does check `s.search == nil` and returns an error, the tool appearing in the capability list suggests the feature is available. |
| **Attack Scenario** | An MCP client sees a `search` tool in the capability listing. They call it and get an error. While this is not a direct security issue, it leaks system configuration state (AI is not configured) which may be useful in reconnaissance. |
| **Impact** | Minor information disclosure. |
| **Recommendation** | Gate the `search` tool the same way as `chat` — only include it in `listTools` when `s.search != nil`. |
| **Effort** | S (15 minutes) |

---

### Finding #14: No Rate Limiting on Admin Endpoints

| Field | Value |
|-------|-------|
| **Category** | Threat Model (DoS) |
| **Severity** | **Low** |
| **Title** | Admin API endpoints (key creation, tenant management) not separately rate-limited |
| **Location** | `internal/api/rest/router.go` (admin routes), `internal/middleware/middleware.go` |
| **Description** | The admin endpoints (`/v1/admin/keys`, `/v1/admin/tenants`, `/v1/admin/jwt`, etc.) are behind the global rate limiter but are not separately rate-limited. An authenticated admin key can be used to create unlimited API keys, JWT tokens, or tenants, potentially exhausting resources. While authenticated, the rate limiter's global nature means a burst of admin operations can exhaust the global token bucket, affecting regular operations. |
| **Attack Scenario** | A compromised admin API key is used to rapidly create thousands of API keys or tenants, exhausting DB storage and causing DoS for the service. |
| **Impact** | Resource exhaustion via admin API abuse. |
| **Recommendation** | (1) Add a separate rate limiter for admin endpoints (lower RPS). (2) Add per-tenant rate limiting for admin operations. (3) Audit-log all admin operations (the audit log path exists). |
| **Effort** | S (half day) |

---

### Finding #15: JWT "iss" Claim Check Is Optional

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | JWT issuer verification is opt-in; default configuration accepts any issuer |
| **Location** | `internal/auth/jwt.go:41-45` (`WithIssuer`), `cmd/server/main.go:299-300` (issuer config) |
| **Description** | The JWT verifier only checks the `iss` claim when `WithIssuer()` has been explicitly called. Without it, any JWT signed with the correct secret is accepted regardless of the issuer claim. In multi-tenant deployments where different tenants might theoretically have different issuers, this could allow cross-tenant token misuse. Additionally, there's no `aud` (audience) claim checking at all. |
| **Attack Scenario** | A JWT token issued by "tenant-a-issuer" is used to access tenant B's data. Since there's no issuer validation, it works if the signing secret is shared (which is the default — single `AUTH_JWT_SECRET` for all). |
| **Impact** | Cross-tenant JWT token reuse when a shared signing secret is used. |
| **Recommendation** | (1) Enable `WithIssuer()` by default when `AUTH_JWT_ISSUER` is configured. (2) Consider adding `aud` claim validation for multi-tenant deployments. |
| **Effort** | S (configuration hardening) |

---

## STRIDE Threat Model Summary

| Category | Threat | Severity | Finding # |
|----------|--------|----------|-----------|
| **S**poofing | SigV4 credentials parsed from env var; no credential rotation mechanism | Med | 7 |
| **S**poofing | JWT issuer pinning is opt-in | Low | 15 |
| **T**ampering | Object Lock Legal Hold stored as metadata, destroyable by overwrite | High | 2 |
| **T**ampering | Idempotency-key replay can replay cached responses | Med | 9 |
| **R**epudiation | `WriteAccessLog` is a no-op — no audit trail | Med | 10 |
| **R**epudiation | No persistent audit trail for WebDAV operations | High | 1, 10 |
| **I**nformation Disclosure | WebDAV bypasses all auth | Critical | 1 |
| **I**nformation Disclosure | Anonymous public read paths may miss ACL checks | Med | 8 |
| **I**nformation Disclosure | SSRF potential through AI/http endpoints | High | 5 |
| **D**enial of Service | No per-admin-endpoint rate limiting | Low | 14 |
| **D**enial of Service | Concurrency limiter can be exhausted by a single tenant | Low | (default, PerTenant exists) |
| **E**levation of Privilege | Bucket policy not enforced on WebDAV/MCP | High | 4 |
| **E**levation of Privilege | Object Lock bypass in versioned buckets | Med | 11 |
| **E**levation of Privilege | Policy conditions beyond IP silently ignored | High | 3 |

---

## Compliance Alignment

| Standard | Requirement | Status | Finding |
|----------|------------|--------|---------|
| OWASP Top 10 | A1: Broken Access Control | ❌ Needs improvement | 1, 2, 3, 4 |
| OWASP Top 10 | A3: Injection | ✅ Acceptable | (SQL uses parameterized queries with rebind) |
| OWASP Top 10 | A5: Security Misconfiguration | ⚠️ Fair | 7, 15 |
| OWASP Top 10 | A6: Vulnerable Components | ✅ Good | Stdlib-only approach limits surface |
| OWASP Top 10 | A8: Software/Data Integrity | ⚠️ Fair | 9 (idempotency replay), 12 (orphaned chunks) |
| OWASP Top 10 | A10: SSRF | ❌ Needs improvement | 5 |
| SOC2 | Access Control | ❌ Needs improvement | 1, 4 |
| SOC2 | Audit Logging | ❌ Needs improvement | 10 |
| SOC2 | Data Protection | ⚠️ Fair | 2, 7 |
| PCI DSS | Requirement 7 (Access Control) | ❌ Not compliant | 1, 4 |
| PCI DSS | Requirement 10 (Audit Trails) | ❌ Not compliant | 10 |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

### Top 3 Critical Issues
1. **WebDAV Bypasses Auth & All Middleware** — Any request to the WebDAV prefix skips authentication, tenant extraction, rate limiting, CORS, and access logging. Unauthenticated remote file read/write/delete. **(Finding #1)**
2. **Object Lock Is Illusory** — Legal Hold is stored as overwritable metadata with no GOVERNANCE/COMPLIANCE mode distinction. Locked objects in versioned buckets can be superseded. **(Findings #2, #11)**
3. **Bucket Policy Conditions Only Check IP** — The full condition engine (18+ IAM operators) exists in `condition.go` but is NOT wired into policy evaluation. Users who configure HTTPS-only, referer, or time-based conditions get silent no-ops. **(Finding #3)**

### Top 3 Quick Wins
1. **Make WriteAccessLog a real implementation** (~1 day). The infrastructure (repository method, SQL schema, config CRUD) is all ready. Just implement the batch buffer writer and wire it into the middleware. Immediate compliance improvement.
2. **Fix the condition engine wiring** (~2 days). The `condition.go` engine is already complete. Wire `ParseConditionBlock` + `Compile()` into `Policy.Eval()` and replace the inline `matchesConditions`. Zero new external dependencies.
3. **Add SSRF protection to HTTP clients** (~1 day). Validate URLs in all outbound HTTP client constructors to reject private IP ranges. Add a startup warning for any HTTP (non-HTTPS) endpoint.

### Security Debt
| Debt Item | Effort | Priority |
|-----------|--------|----------|
| WebDAV middleware chain integration | M | Critical |
| Object Lock: dedicated columns + GOVERNANCE/COMPLIANCE | M | High |
| Bucket policy: wire condition engine | M | High |
| Bucket policy: enforce on WebDAV/MCP | M | High |
| SSRF protection on all outbound HTTP clients | M | High |
| Access log implementation | M | High |
| MCP stdio authentication | S | Medium |
| Object Lock: versioned bucket path check | S | Medium |
| Anonymous read ACL audit | S | Medium |
| Idempotency TTL hardening | S | Low |
| Admin endpoint rate limiting | S | Low |
| JWT issuer pinning by default | S | Low |
| MCP search tool gating | S | Info |

### Key Strengths
- **Auth middleware chain is clean** — proper RequestID → CORS → Auth → Tenant → RateLimit ordering, bypass paths explicitly listed, scope-based access control.
- **Cryptography is sound** — AES-256-GCM with envelope encryption, proper key derivation via HMAC-SHA256, KEK wrapping works correctly, rewrap-on-start for rotation.
- **SQL parameterization** — The `rebind` system ensures all SQL uses parameterized queries, preventing SQL injection.
- **Feature-gated defaults** — AI, Postgres, Qdrant, WebDAV, events features all default to off. The CI baseline is minimal and secure.
- **Good use of context propagation** — RequestID, Tenant, Auth Key all flow through `context.Context`.
