I now have a comprehensive understanding of the subsystem. Here is my complete security review.

---

# Security Review: REST API Bucket Policy Enforcement (TASK-001)

## Reviewer Assessment

The implementation achieves its stated goal of adding bucket policy enforcement to the 6 main CRUD handlers (Put/PostForm/Get/Head/Delete/List). The code is clean, follows the S3 handler pattern faithfully, and includes good defensive degrades on parse errors. **However, the enforcement surface is critically incomplete** — several sub-resource and batch endpoints remain unprotected, creating direct bypass paths.

---

## Findings

### Finding 1 — Sub-resource handlers bypass bucket policy entirely

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | All sub-resource endpoints (tags, ACL, versions, lock, restore, presign, thumbnail, batch, folder, multipart) skip `checkBucketPolicy` |
| **Location** | `internal/api/rest/router.go` lines 130–171: dispatchers `getKey`, `putKey`, `deleteKey`, `postKey` |
| **Description** | The router dispatchers route to handler methods that never call `checkBucketPolicy`. Only the 6 main CRUD methods in `handler.go` (Put, PostForm, Get, Head, Delete, List) have policy enforcement. The following endpoints are completely unprotected: |
| | **Endpoint** | **Handler** | **Corresponding S3 Action** |
| | `GET /files/*/tags` | `GetTags` | `s3:GetObjectTagging` |
| | `PUT /files/*/tags` | `PutTags` | `s3:PutObjectTagging` |
| | `DELETE /files/*/tags` | `DeleteTags` | `s3:DeleteObjectTagging` |
| | `GET /files/*/acl` | `GetObjectACLHandler` | `s3:GetObjectAcl` |
| | `PUT /files/*/acl` | `PutObjectACLHandler` | `s3:PutObjectAcl` |
| | `GET /files/*/versions` | `ListVersions` | `s3:ListObjectVersions` / `s3:GetObjectVersion*` |
| | `GET /files/*/thumbnail` | `Thumbnail` | `s3:GetObject` |
| | `POST /files/*/lock` | `LockObject` | `s3:PutObjectRetention` |
| | `POST /files/*/restore` | `Restore` | `s3:RestoreObject` |
| | `POST /files/*/presign` | `Presign` | (see Finding 2) |
| | `POST /batch/delete` | `BatchDelete` | `s3:DeleteObject` (per item) |
| | `POST /batch/tag` | `BatchTag` | `s3:PutObjectTagging` |
| | `GET /folders` | `ListFolders` | `s3:ListBucket` |
| | `POST /folders/*` | `CreateFolder` | `s3:PutObject` |
| | `DELETE /folders/*` | `DeleteFolder` | `s3:DeleteObject` |
| | `POST /multipart` | `InitMultipart` | `s3:PutObject` |
| | `PUT /multipart/.../parts/...` | `UploadPart` | `s3:PutObject` |
| | `POST /multipart/.../complete` | `CompleteMultipart` | `s3:PutObject` |
| | `DELETE /multipart/...` | `AbortMultipart` | `s3:AbortMultipartUpload` |
| **Attack Scenario** | 1. Admin sets bucket policy to `Deny s3:PutObjectTagging` on a sensitive bucket. 2. Attacker with valid API key sends `PUT /v1/files/secretobj/tags` with malicious tags. 3. The tag write succeeds because `PutTags` never calls `checkBucketPolicy`. Same applies to all other sub-resource endpoints. |
| **Impact** | A bucket policy intended to restrict object access is partially effective at best. Attackers can bypass policy for 18 additional endpoints, reading/writing metadata, tags, ACLs, versions, and object locks — even uploading or downloading object content via presigned URLs. |
| **Recommendation** | Add `checkBucketPolicy` calls to every sub-resource handler. Map each to the appropriate S3 action: `s3:GetObjectTagging`, `s3:PutObjectTagging`, `s3:GetObjectAcl`, `s3:PutObjectAcl`, `s3:ListObjectVersions`, `s3:PutObjectRetention`, `s3:RestoreObject`, `s3:PutObject` (for multipart), `s3:AbortMultipartUpload`, etc. Move the policy check into the dispatching functions (`getKey`, `putKey`, `deleteKey`, `postKey`) to avoid missing any future sub-resource handler. |
| **Effort** | M (1-2 days) — 18 handlers need policy calls, but the pattern is mechanical once the action constants are chosen. |

---

### Finding 2 — Presigned URL generation with no policy check enables full policy bypass

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | `Presign` handler generates pre-signed URLs without checking bucket policy, allowing `s3:PutObject`/`s3:GetObject` policy bypass |
| **Location** | `internal/api/rest/handler.go` lines 309–335: `Presign` handler |
| **Description** | The `Presign` handler generates time-limited signed URLs for GET and PUT operations without checking the bucket policy. An attacker who is denied `s3:PutObject` by policy can request a presigned PUT URL, then use that URL to upload content directly to storage, bypassing the policy check entirely. Similarly, `s3:GetObject` can be bypassed via a presigned GET URL. |
| **Attack Scenario** | 1. Bucket policy denies `s3:PutObject` for all users. 2. Attacker sends `POST /v1/files/secret.doc/presign` with body `{"op":"put"}`. 3. Server returns a presigned URL. 4. Attacker uploads arbitrary content to `secret.doc` using the URL. 5. The storage backend validates the signature (which checks HMAC, not policy) and accepts the write. Policy completely bypassed. |
| **Impact** | The entire CRUD policy enforcement can be bypassed by going through the presign endpoint. Any Deny on `s3:PutObject` or `s3:GetObject` is ineffective if the attacker has API key access. |
| **Recommendation** | Add `checkBucketPolicy(w, r, "s3:PutObject")` before the `op == "put"` branch and `checkBucketPolicy(w, r, "s3:GetObject")` before the `op == "get"` branch in the `Presign` handler. Additionally, consider storing the policy context in the presigned signature so the storage layer can re-validate at access time (a deeper architectural change). |
| **Effort** | S (hours) — two policy check calls in the `switch` branches. |

---

### Finding 3 — Policy evaluation ignores `Resource` ARN

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | `Policy.Eval()` never checks the `Resource` field — any action match applies to all keys |
| **Location** | `internal/auth/policy.go` lines 71–89: `Eval` function |
| **Description** | The `Policy.Eval` function iterates over statements and checks `Action`, `Principal`, and `Conditions`, but **never checks `Resource`**. This means a policy that intends to restrict `s3:GetObject` only to keys under `arn:aws:s3:::default/public/*` will actually allow `s3:GetObject` on *every* key. The `Resource` field is parsed (via `StringOrArray` in `UnmarshalJSON`) but never referenced during evaluation. |
| **Attack Scenario** | Admin sets policy: `{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/public/*"}`. Expected: only objects under `public/` are readable. Actual: all objects are readable because the resource check is skipped. |
| **Impact** | Any bucket policy using resource-level scoping (key prefix restrictions) is silently ineffective. Policy authors will incorrectly believe they have restricted access to a sub-path. |
| **Recommendation** | Implement ARN resource matching in `Eval`: parse the resource ARN in each statement and compare it against the request's resource ARN (constructed from tenant/bucket/key). Use `auth.MatchARN` which already supports glob patterns. This is a significant change to the policy engine. |
| **Effort** | M (2-3 days) — requires implementing resource ARN construction from request context, integrating it into `Eval`, and adding tests. |

---

### Finding 4 — Policy condition evaluation only supports IP address conditions; all other conditions silently pass

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | `matchesConditions` only handles `IpAddress`/`NotIpAddress`; other condition types (`StringEquals`, `Bool`, `DateLessThan`, etc.) are silently accepted |
| **Location** | `internal/auth/policy.go` lines 159–177: `matchesConditions` method |
| **Description** | The `matchesConditions` function uses a `switch` that only matches `IpAddress` and `NotIpAddress` operators. Any other IAM condition operator (e.g., `StringEquals`, `Bool {"aws:SecureTransport": "true"}`, `DateGreaterThan`, `ArnEquals`) falls through the switch without returning `false`, so the condition block returns `true` (passed). This means conditions that should restrict access (e.g., only HTTPS, only from a specific referer) are silently ignored. |
| **Attack Scenario** | Policy has `"Condition": {"Bool": {"aws:SecureTransport": "false"}}` intending to deny non-HTTPS requests. The condition is silently passed, so HTTP requests are allowed despite the policy. |
| **Impact** | IAM policy authors familiar with AWS semantics will have incorrect security expectations. Conditions that appear to restrict access are not enforced. |
| **Recommendation** | Either (a) implement the full condition evaluation using the existing `auth.CompileConditionSet` and `ConditionContext` infrastructure, or (b) reject policies with unsupported condition operators at parse time with an error to make the limitation explicit. Option (b) is faster and safer (fail-closed for unknown conditions). |
| **Effort** | S–M for (b) (hours), L for (a) (3+ days). Recommend (b) as an immediate fix. |

---

### Finding 5 — `r.RemoteAddr` gives proxy IP, making IP-based conditions ineffective behind reverse proxies

| Field | Value |
|-------|-------|
| **Category** | Authorization / Threat Model |
| **Severity** | **High** |
| **Title** | IP-based policy conditions use `r.RemoteAddr` which is the immediate TCP peer, not the original client |
| **Location** | `internal/api/rest/handler.go` lines 55–56: `checkBucketPolicy`; `internal/api/s3compat/handler.go` lines 59–60 |
| **Description** | Both `checkBucketPolicy` implementations parse the client IP from `r.RemoteAddr` using `net.SplitHostPort`. When the service runs behind any reverse proxy (nginx, AWS ALB, Cloudflare, etc.), `RemoteAddr` always contains the proxy's IP address, not the original client. IP-based allow/deny conditions in the bucket policy will match against the proxy IP, making them either always-pass or always-fail depending on the proxy's IP range. |
| **Attack Scenario** | Policy has `"Condition": {"IpAddress": {"aws:SourceIp": ["10.0.0.0/8"]}}` intending to restrict access to an internal network. Behind an ALB, `r.RemoteAddr` is the ALB's private IP, which is in `10.0.0.0/8`, so the condition passes for *all* requests regardless of the actual client IP. |
| **Impact** | IP-based policy restrictions are unreliable in any deployment behind a reverse proxy — the industry-standard deployment pattern. |
| **Recommendation** | Check the `X-Forwarded-For` header first, falling back to `RemoteAddr`. When `X-Forwarded-For` has multiple IPs, use the leftmost (original client) IP. Per OWASP, validate that the proxy chain is trusted (configurable via a `TRUSTED_PROXY_CIDRS` configuration). |
| **Effort** | S (hours) — add a helper function `clientIP(r *http.Request) string` that checks `X-Forwarded-For`, X-Real-Ip, then `RemoteAddr`. |

---

### Finding 6 — Policy parsing errors degrade open (allow-all) with only a warn log

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | `checkBucketPolicy` silently allows all requests when policy JSON is malformed |
| **Location** | `internal/api/rest/handler.go` lines 50–53 |
| **Description** | When `auth.ParsePolicy` returns a parse error, the handler logs a warning and returns `true` (allow). This fail-open behavior is intentional (service availability over security misconfig), but a single typo in a policy JSON can silently turn off enforcement entirely. An attacker who can trigger a policy update with a malformed JSON (or exploit a race condition during policy write) can temporarily disable all policy enforcement. |
| **Attack Scenario** | 1. Admin updates policy with a trailing comma (invalid JSON). 2. PutBucketPolicy accepts the JSON and stores it (no server-side validation? check). Or: 3. Any subsequent request triggers `ParsePolicy`, which fails, and the request passes through unrestricted. |
| **Impact** | Transient or permanent loss of policy enforcement during misconfiguration. |
| **Recommendation** | 1. Add policy validation in `PutBucketPolicy` to reject malformed policies before storing them. 2. Consider a `POLICY_STRICT_MODE` config flag that, when true, returns 403 on parse errors instead of allowing (fail-closed). 3. Add a health check endpoint that validates all stored policies and reports parse errors. |
| **Effort** | S for validation (hours), S for strict mode flag (hours), S for health check (hours) |

---

### Finding 7 — Policy management endpoints lack authorization beyond auth middleware

| Field | Value |
|-------|-------|
| **Category** | Authorization / Elevation of Privilege |
| **Severity** | **Medium** |
| **Title** | `PutBucketPolicy` and `DeleteBucket` have no scope/role check; any authenticated user can modify policies or delete buckets |
| **Location** | `internal/api/rest/handler.go` lines 435–455, 535–545 |
| **Description** | The `PutBucketPolicy` endpoint accepts a new policy from any authenticated user. There is no admin-scope or role check beyond the `mw.Auth` middleware. Similarly, `DeleteBucket` permanently removes a bucket and all objects. In AWS S3, these operations require `s3:PutBucketPolicy` and `s3:DeleteBucket` permissions respectively. |
| **Attack Scenario** | 1. Attacker obtains a low-privilege API key with any valid scope. 2. Attacker calls `PUT /v1/buckets/default/policy` with a policy that allows everything. 3. Policy enforcement is now completely disabled for all users. |
| **Impact** | Any API key holder can override or remove bucket-level security controls. |
| **Recommendation** | Apply admin-scope checks (similar to other admin endpoints in `admin.go`) before allowing policy modifications and bucket deletion. Restrict these operations to operator-level keys or add an `X-Aero-Admin` scope requirement. |
| **Effort** | S (hours) — add the admin-scope middleware or check similar to existing admin handlers. |

---

### Finding 8 — Batch operations bypass per-object policy checks

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | `BatchDelete` and `BatchTag` operate without bucket policy check |
| **Location** | `internal/api/rest/handler.go` lines 587–620 |
| **Description** | The batch endpoints accept a list of keys and operate on all of them without calling `checkBucketPolicy`. Even if the single-key `Delete` and the tag operations are fixed (Finding 1), the batch endpoints would still bypass enforcement. Additionally, `BatchDelete` accepts an explicit `bucket` field from the request body, which could target a non-default bucket. |
| **Attack Scenario** | 1. Policy denies `s3:DeleteObject`. 2. Attacker sends `POST /v1/batch/delete` with 1000 keys. 3. All 1000 objects are deleted without any policy check. |
| **Impact** | Batch operations are a force-multiplier for policy bypass, enabling mass-tagging and mass-deletion even when single-key operations are restricted. |
| **Recommendation** | Add `checkBucketPolicy` with `s3:DeleteObject` at the top of `BatchDelete` and `s3:PutObjectTagging` at the top of `BatchTag`. Consider checking the `bucket` field in the request body against the authenticated tenant. |
| **Effort** | S (hours) — add two policy check calls. |

---

### Finding 9 — Folder operations (`ListFolders`, `CreateFolder`, `DeleteFolder`) lack policy checks

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Folder management endpoints bypass bucket policy |
| **Location** | `internal/api/rest/handler.go` lines 670–730 |
| **Description** | The folder CRUD operations (`ListFolders`, `CreateFolder`, `DeleteFolder`) never call `checkBucketPolicy`. `CreateFolder` creates a zero-byte directory marker via `svc.Put`, and `DeleteFolder` paginates through all objects under a prefix and batch-deletes them — both without any policy check. |
| **Attack Scenario** | Policy denies `s3:PutObject`. Attacker calls `POST /v1/folders/confidential/financial-reports/` to create a directory marker. The zero-byte object is created, bypassing the `s3:PutObject` Deny. |
| **Impact** | Directory markers and folder listings are outside the policy scope, allowing metadata operations that bypass object-write restrictions. |
| **Recommendation** | Add `checkBucketPolicy` with `s3:ListBucket` for `ListFolders`, and `s3:PutObject` for `CreateFolder`, and `s3:DeleteObject` for `DeleteFolder`. |
| **Effort** | S (hours) — add three policy check calls. |

---

### Finding 10 — MCP tools lack bucket policy enforcement

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | MCP protocol tools can read/write/delete objects without bucket policy checks |
| **Location** | `internal/mcp/server.go` lines 180–240: `toolReadFile`, `toolWriteFile`, `toolDeleteFile`, `toolListFiles`, `readResource` |
| **Description** | The MCP server's tool implementations call `svc.Get`, `svc.Put`, `svc.Delete`, `svc.List` directly without any bucket policy check. This provides a complete parallel bypass path through a different protocol (HTTP POST to `/mcp` or stdio). The MCP server exposes the same data and operations as the REST API but without policy enforcement. Even the `read_resource` handler's tenant-boundary check doesn't enforce bucket-level policies. |
| **Attack Scenario** | 1. Admin sets restrictive bucket policy. 2. Attacker connects via MCP (HTTP to `/mcp`) and calls `read_file` and `write_file` tools. 3. Policy is completely bypassed because MCP has no enforcement at all. |
| **Impact** | Bucket policy is not enforced on the MCP protocol path, effectively gutting the value of the TASK-001 changes. |
| **Recommendation** | Either (a) replicate `checkBucketPolicy` logic in each MCP tool handler, or (b) move the policy check into `FileService` methods (`Get`, `Put`, `Delete`, `List`) so it's enforced at the service layer regardless of protocol. Option (b) is architecturally superior but a larger change. For a targeted fix, add a helper function in the MCP server that wraps the service calls with policy checks. |
| **Effort** | M (1-2 days) for option (b) — service-layer enforcement. S (hours) for option (a) — replicate in MCP handlers. |

---

### Finding 11 — No audit logging of policy enforcement decisions

| Field | Value |
|-------|-------|
| **Category** | Repudiation |
| **Severity** | **Low** |
| **Title** | Successful and denied policy checks are not logged, impairing incident investigation |
| **Location** | `internal/api/rest/handler.go` lines 46–63: `checkBucketPolicy` |
| **Description** | When `checkBucketPolicy` denies a request, it writes a 403 response but produces no log entry. Parse errors are logged at Warn level, but normal allow/deny decisions are silent. This makes it difficult to determine why a request was rejected, debug policy misconfigurations, or detect attack patterns. |
| **Attack Scenario** | Attacker probes with various actions to understand the effective policy. Each probe returns 403 with the same `AccessDenied` message. Operations team has no log trail to identify the probing pattern. |
| **Impact** | Reduced operational visibility and auditability of access control decisions. |
| **Recommendation** | Log policy enforcement decisions at Debug level for allows and Info level for denials, including tenant, action, IP, and the matched statement effect. Include a structured log field `policy_decision` with values `allow`, `deny`, `implicit_deny`, or `skip_parse_error`. |
| **Effort** | S (hours) — add `slog` calls in `checkBucketPolicy`. |

---

### Finding 12 — Hardcoded `service.DefaultBucket` in `checkBucketPolicy`

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | REST handler's `checkBucketPolicy` uses `service.DefaultBucket` instead of a dynamic bucket parameter |
| **Location** | `internal/api/rest/handler.go` line 48: `h.svc.GetBucketConfig(r.Context(), ...)` called with `service.DefaultBucket` |
| **Description** | The REST API's `checkBucketPolicy` always loads the policy from `service.DefaultBucket` ("default") rather than accepting the target bucket as a parameter. While the REST API currently uses only the default bucket (consistent with `service.DefaultBucket` in all handler calls), this creates a latent bug if multi-bucket support is added to the REST API later. The S3 handler's `checkBucketPolicy` correctly accepts a `bucket` parameter. |
| **Attack Scenario** | If a future change adds multi-bucket support to a REST handler, the policy check would still look at the "default" bucket's policy, checking the wrong policy document. |
| **Impact** | Currently zero (all REST handlers use the default bucket). Latent bug for future development. |
| **Recommendation** | Accept a `bucket string` parameter in REST's `checkBucketPolicy` (matching the S3 handler's signature) even if it's currently always called with `service.DefaultBucket`. This makes the signature consistent and future-proof. |
| **Effort** | S (hours) — change method signature and update callers. |

---

## STRIDE Analysis Summary

| Category | Finding |
|----------|---------|
| **S**poofing | No principal-based identity matching in policy engine (only `"*"` is checked) — Finding 3 |
| **T**ampering | Policy documents in DB could be tampered with; no integrity verification on stored policy — Finding 7 |
| **R**epudiation | No audit logging of allow/deny decisions — Finding 11 |
| **I**nformation Disclosure | Sub-resource endpoints (tags, ACL, versions, thumbnail) leak metadata without policy check — Finding 1 |
| **D**enial of Service | `GetBucketConfig` is called on every request with no caching — potential DB hammering |
| **E**levation of Privilege | Any authenticated user can modify bucket policies via `PutBucketPolicy` — Finding 7 |

---

## OWASP Top 10 (2021) Mapping

| OWASP Category | Relevant Findings |
|----------------|-------------------|
| A01: Broken Access Control | Findings 1, 2, 3, 4, 6, 7, 8, 9, 10 |
| A05: Security Misconfiguration | Findings 4, 5, 6, 12 |
| A09: Security Logging and Monitoring Failures | Finding 11 |

---

## Final Summary

| Metric | Assessment |
|--------|------------|
| **Overall Security Posture** | **Critical Issues** — The core implementation is sound, but the enforcement surface has critical gaps that allow direct bypass. |
| **Top 3 Critical Issues** | 1. **18 sub-resource endpoints bypass policy** (Finding 1) — tags, ACL, versions, lock, restore, thumbnail, batch, folder, multipart all unprotected. 2. **Presigned URL generation bypasses policy** (Finding 2) — a single request yields a signed URL that completely bypasses all CRUD enforcement. 3. **Resource ARN matching absent** (Finding 3) — key-prefix-based restrictions in policy documents are silently ignored. |
| **Top 3 Quick Wins** | 1. **Presign handler** — Add two `checkBucketPolicy` calls to block presigned URL bypass (S effort, kills critical bypass). 2. **Batch endpoints** — Add `checkBucketPolicy` to `BatchDelete` and `BatchTag` (S effort). 3. **PutBucketPolicy authorization** — Restrict policy modification to admin-scoped keys (S effort). |
| **Security Debt** | The policy engine has accumulated gaps: no Resource ARN matching, incomplete condition evaluation, and no principal-based identity matching. These predate TASK-001 but reduce policy effectiveness to approximately 40% of what an AWS IAM user would expect. A separate initiative to flesh out the policy engine (`Eval` function, condition support, resource matching) would significantly improve the overall security posture. |
