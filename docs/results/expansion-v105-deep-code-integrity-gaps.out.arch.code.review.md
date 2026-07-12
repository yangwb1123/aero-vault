Now I have a complete picture of the codebase. Let me compile the full code review.

---

# Staff Engineer Code Review: TASK-001 — REST API Bucket Policy Enforcement

**Codebase:** `/home/u1/aero-vault`
**Primary files:** `internal/api/rest/handler.go`, `internal/api/rest/handlers_test.go`
**Supporting files:** `internal/api/rest/management.go`, `internal/api/rest/acl.go`, `internal/api/rest/thumbnail.go`, `internal/api/rest/router.go`, `internal/api/s3compat/handler.go`, `internal/auth/policy.go`

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | 3 (checkBucketPolicy) | < 10 | ✅ |
| Function length | 26 lines (checkBucketPolicy) | < 50 lines | ✅ |
| Test coverage | ~12% of handler methods covered for policy | > 80% (for new code) | ⚠️ |
| Code duplication | Low (REST vs S3 pattern mismatch) | < 5% | ⚠️ |
| Documentation coverage | Good on public API, sparse on rationale | > 70% | ✅ |

---

## Findings

| # | Category | Severity | Title | Location | Description | Recommended State | Impact |
|---|----------|----------|-------|----------|-------------|-------------------|--------|
| 1 | Quality | **Critical** | Sub-resource handlers bypass bucket policy entirely | `management.go:31-87` (GetTags, PutTags, DeleteTags, ListVersions, LockObject), `acl.go:39-62` (GetObjectACLHandler, PutObjectACLHandler), `thumbnail.go:18` (Thumbnail) | Sub-resource operations dispatched via `getKey`/`putKey`/`deleteKey`/`postKey` in `router.go` bypass `checkBucketPolicy`. S3 compat path checks policy in parent handler BEFORE dispatching to sub-resources. REST sub-resource handlers are standalone and have zero policy enforcement. A bucket policy denying `s3:GetObject` can be bypassed via `GET /v1/files/key/tags` or `GET /v1/files/key/acl`. | Each sub-resource handler must call `checkBucketPolicy` at entry, or the dispatchers in `router.go` must check policy before dispatch. S3 pattern: `PutObject` → policy check → sub-resource dispatch. REST pattern must match. | **Critical** — Security bypass of bucket policy for tags, ACLs, versions, lock, thumbnails |
| 2 | Quality | **Critical** | Batch and folder operations bypass bucket policy | `handler.go:673-700` (BatchDelete, BatchTag), `handler.go:729-827` (ListFolders, CreateFolder, DeleteFolder) | Batch operations (`BatchDelete`, `BatchTag`) and folder operations (`ListFolders`, `CreateFolder`, `DeleteFolder`) completely bypass bucket policy checks. A bucket policy denying `s3:DeleteObject` is trivially bypassed via `POST /v1/batch/delete` with `{"bucket":"default","keys":["key"]}`. | Every operation that maps to an S3 action must enforce the bucket policy before proceeding. Batch operations should check the policy once per batch, not per key. | **Critical** — Batch and folder operations are unguarded |
| 3 | Quality | **Critical** | Presign endpoint bypasses bucket policy | `handler.go:325-355` (Presign) | The `Presign` handler generates pre-signed URLs for get/put without checking the bucket policy. A policy denying `s3:PutObject` is bypassed by generating a presigned PUT URL. | Add `checkBucketPolicy(w, r, ...)` before generating presigned URLs: `s3:GetObject` for op=get, `s3:PutObject` for op=put | **Critical** — Presign is an open backdoor around bucket policies |
| 4 | Quality | **Critical** | Multipart upload operations bypass bucket policy | `handler.go:358-410` (InitMultipart, UploadPart, CompleteMultipart, AbortMultipart) | All four multipart upload endpoints have zero bucket policy enforcement. S3 compat handles multipart via `PutObject` → policy check → `uploadId` dispatch, but REST has separate routes that are unguarded. | Add `checkBucketPolicy(w, r, "s3:PutObject")` to each multipart handler | **Critical** — Multipart completely unguarded |
| 5 | Quality | **High** | `checkBucketPolicy` hardcodes `service.DefaultBucket` | `handler.go:50` | S3 `checkBucketPolicy` accepts a `bucket` parameter. REST version hardcodes `service.DefaultBucket` (line 50: `h.svc.GetBucketConfig(..., service.DefaultBucket)`). While REST currently mostly uses `DefaultBucket`, `BatchDelete`, `BatchTag`, and bucket-config endpoints accept a `bucket` field, creating an inconsistency. | Add `bucket string` parameter to `checkBucketPolicy` (matching S3 signature). Pass the correct bucket from each caller. | **High** — Inconsistency with S3, limits future multi-bucket support |
| 6 | Quality | **Medium** | `PostForm` checks policy after expensive form parsing | `handler.go:99-135` | `PostForm` parses the full multipart form (32MB buffer) and extracts the file before checking bucket policy on line 126. A denied request wastes IO and memory. | Move policy check to immediately after key extraction (line 113), before form file extraction | **Medium** — Denial-of-waste on denied multipart uploads |
| 7 | Quality | **Medium** | `Restore` missing bucket policy check | `handler.go:662-670` | `Restore` handler bypasses policy. A policy denying `s3:PutObject` is bypassed via `POST /v1/files/key/restore`. | Add `checkBucketPolicy(w, r, "s3:PutObject")` | **High** — Another unguarded modification path |
| 8 | Quality | **Medium** | Test coverage limited to 6 CRUD handlers, missing 16+ handler paths | `handlers_test.go:312-522` | Tests only cover Put, Get, Head, Delete, List. No tests exist for policy enforcement on tags, ACLs, versions, lock, multipart, batch, folders, presign, restore, thumbnail, or bucket-version listing. | Add tests for all sub-resource, batch, folder, multipart, and presign handlers | **Medium** — Test gap means enforcement gaps go undetected |
| 9 | Quality | **Low** | `ListBucketVersions` missing policy check | `handler.go:924+` | `ListBucketVersions` (list all versions in a bucket) has no bucket policy check. Should check `s3:ListBucket`. | Add `checkBucketPolicy(w, r, "s3:ListBucket")` before listing | **Medium** — Listing endpoint unguarded |
| 10 | Quality | **Low** | Both `ListFolders` and `List` have no mutual policy reuse | `handler.go:298, 729` | `ListFolders` calls `svc.List` internally (the same operation as `List`) but bypasses the policy check. These could share a common guard. | Either check policy in `ListFolders` (as it maps to `s3:ListBucket`) or have `ListFolders` delegate to `h.List` | **Low** — Code duplication risk |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Sub-resource handlers missing policy enforcement (tags, ACL, versions, lock, thumbnail) | Critical | M | **P0** | 10 handler functions across 3 files. Each is a 2-line insertion (check + return). Consistent with S3 pattern. |
| Batch/folder operations missing policy enforcement | Critical | S | **P0** | 5 handler functions. Batch should check once per batch. |
| Presign missing policy check | Critical | S | **P0** | 1 handler, 2 code paths (get/put). |
| Multipart missing policy check | Critical | S | **P0** | 4 handler functions. All need `s3:PutObject`. |
| `checkBucketPolicy` hardcodes `DefaultBucket` | High | S | **P1** | Change signature to accept bucket, update all callers. |
| `PostForm` policy check ordering | Medium | S | **P2** | Move check before form file extraction. |
| Incomplete test coverage for policy | Medium | M | **P1** | 10+ new test functions needed. |
| `Restore` missing policy check | High | S | **P0** | 2-line fix. |
| `ListBucketVersions` missing policy check | Medium | S | **P2** | 2-line fix. |

---

## In-depth Analysis

### 1. Architecture Divergence: REST vs S3 Sub-resource Patterns

The root cause of the critical security gaps is an architectural divergence between REST and S3 in how sub-resources are dispatched.

**S3 pattern** (s3compat/handler.go):
```
PutObject → checkBucketPolicy → dispatch sub-resource by query param (tagging, acl, uploadId, restore)
GetObject → checkBucketPolicy → dispatch sub-resource by query param (tagging, acl, uploadId)
DeleteObject → checkBucketPolicy → dispatch sub-resource by query param (tagging, uploadId)
```

**REST pattern** (rest/router.go):
```
putKey → suffix match (tag, acl) → PutTags/PutObjectACLHandler/Put (NO policy check in dispatcher)
getKey → suffix match (tag, acl, version, thumbnail) → GetTags/GetObjectACLHandler/ListVersions/Thumbnail/Get (NO policy check in dispatcher)
deleteKey → suffix match (tag) → DeleteTags/Delete (NO policy check in dispatcher)
```

The REST dispatchers (`putKey`, `getKey`, `deleteKey`, `postKey`) do the suffix routing BEFORE any policy check. In the S3 pattern, the main CRUD handler checks policy BEFORE dispatching to sub-resource code paths.

There are two equally valid fix approaches:

**Option A (Recommended):** Add policy checks to each sub-resource handler individually. This is explicit and makes testing straightforward.

**Option B:** Move the policy check into the dispatchers (`putKey`, `getKey`, `deleteKey`) so all sub-resources are automatically covered. However, this requires mapping each suffix to the correct S3 action, which adds complexity to the dispatcher.

I recommend **Option A** for consistency with the existing codebase pattern and testability.

### 2. The `checkBucketPolicy` Method Quality

The implemented method itself is clean and well-structured:

```go
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, action string) bool {
    cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), service.DefaultBucket)
    if err != nil || cfg.Policy == "" {
        return true                  // ← grace: no policy = pass
    }
    p, perr := auth.ParsePolicy(cfg.Policy)
    if perr != nil {
        h.logger.Warn("...", "bucket", service.DefaultBucket, "err", perr)
        return true                  // ← grace: parse error = pass (with warn log)
    }
    host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
    if splitErr != nil {
        host = r.RemoteAddr
    }
    if !auth.Allowed(p, action, host) {
        h.writeError(w, r, service.ErrForbidden)
        return false
    }
    return true
}
```

**Strengths:**
- Graceful degradation on parse errors (warn log, skip enforcement)
- Correct Deny-wins semantics (matching AWS IAM)
- Clean boolean return for if-let pattern at call sites
- Zero overhead when no policy is set (`cfg.Policy == ""` short-circuit)
- `net.SplitHostPort` error handling (falls back to full RemoteAddr)

**Weaknesses:**
- Hardcoded `service.DefaultBucket` (issue #5)
- Uses `h.svc.GetBucketConfig` which fetches the full bucket config from the database, when only the `Policy` field is needed. Consider a lighter `GetBucketPolicy` method if performance becomes a concern. (Minor — this is one DB query per request.)

### 3. S3 Action Mapping Consistency

The REST handlers use the same S3 action names (`s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucket`) as the S3 compat layer. This is correct design — the same policy JSON document can control both protocols.

However, the `Head` handler uses `s3:GetObject` which correctly maps through `s3Actions["HeadObject"] = "s3:GetObject"` in the auth package. Clean.

### 4. Test Coverage Analysis

The 7 existing policy tests are well-structured:
- `TestBucketPolicyDenyPut` — Tests Deny+Allow interaction
- `TestBucketPolicyDenyGet` — Tests symmetric GET/HEAD denial
- `TestBucketPolicyDenyDelete` — Tests selective denial
- `TestBucketPolicyImplicitDeny` — Tests IAM "default deny" semantics
- `TestBucketPolicyList` — Tests list-specific action
- `TestBucketPolicyNoPolicyDoesNotBlock` — Tests backward compatibility

Each test follows the **Arrange-Act-Assert** pattern and properly cleans up the policy at the end. The `bodyPolicy` helper is clean and reusable.

**Missing test scenarios:**
1. Policy deny on sub-resource handlers (tags, ACL, versions, lock, thumbnail)
2. Policy deny on batch operations
3. Policy deny on folder operations (ListFolders, CreateFolder, DeleteFolder)
4. Policy deny on Presign
5. Policy deny on multipart upload
6. Policy deny on Restore
7. IP-address condition in policy (requires mocking RemoteAddr)
8. Wildcard action policy (s3:PutObject vs s3:*)
9. Policy with multiple conditions

### 5. Logging Assessment

The single logging call in the implementation is appropriate:

```go
h.logger.Warn("bucket policy parse error, skipping enforcement",
    "bucket", service.DefaultBucket, "err", perr)
```

**Strengths:**
- Uses structured logging (key-value pairs)
- Uses `Warn` level (appropriate for configuration errors that don't block requests)
- Includes bucket context and error details

**Suggestion:** Add an `Info` or `Debug` log when policy actually denies a request, which is useful for audit and debugging. Example:
```go
h.logger.Info("bucket policy denied request", "action", action, "host", host, "bucket", service.DefaultBucket)
```

### 6. Request Correlation

The error response includes `RequestID` from context:
```go
writeJSON(w, status, errorBody{Error: errorPayload{
    Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
}})
```

This is good practice. However, the denied request log (if added) should also include the RequestID for correlation:
```go
h.logger.Info("bucket policy denied request", "action", action, "request_id", mw.RequestIDFrom(r.Context()))
```

---

## Final Summary

| Dimension | Rating | Notes |
|-----------|--------|-------|
| **Overall Code Quality** | **Good (with critical gaps)** | The implemented `checkBucketPolicy` method is clean, correct, and idiomatic. The 6 main CRUD handlers are properly guarded. However, **~70% of REST endpoints remain unguarded**, including tags, ACLs, versions, lock, multipart, batch operations, folder operations, presign, and restore. |
| **Critical Quality Issues** | **11 unguarded endpoints** | Tags (3), ACL (2), versions (1), lock (1), multipart (4), batch (2), folders (3), presign (1), restore (1), thumbnail (1), bucket-version-listing (1) = **20 missing policy checks** across **16 handler functions** (some overlap between batch/folder handlers). | 
| **Maintainability Concerns** | Architecture divergence from S3 pattern | REST uses URL-suffix sub-resource dispatch (router.go `putKey/getKey/deleteKey`), S3 uses header/query-param dispatch within main handlers. Each protocol needs its own policy checks — they can't share enforcement logic through middleware. |
| **Technical Debt** | P0 debt requiring immediate remediation | The implementation achieved the stated goal (6 CRUD handlers) but missed the full scope of REST endpoints. The 20 missing check sites represent **2-3 hours of work** across 5 files. |
| **Quick Wins** | 10-minute fixes | `Restore`, `ListBucketVersions`, `ListFolders`, `CreateFolder`, `DeleteFolder` each need 2 lines added. `checkBucketPolicy` signature update (add `bucket` param) affects all callers but is mechanical. |

### Recommended Action Plan

1. **P0** (immediate): Add `checkBucketPolicy` to all unguarded handlers:
   - `management.go`: GetTags, PutTags, DeleteTags, ListVersions, LockObject (5 handlers)
   - `acl.go`: GetObjectACLHandler, PutObjectACLHandler (2 handlers)
   - `thumbnail.go`: Thumbnail (1 handler)
   - `handler.go`: Restore, Presign, BatchDelete, BatchTag, ListFolders, CreateFolder, DeleteFolder, ListBucketVersions, InitMultipart, UploadPart, CompleteMultipart, AbortMultipart (12 handlers)

2. **P1** (soon): Fix `checkBucketPolicy` signature to accept `bucket string` parameter, matching S3 compat. Update all callers.

3. **P1** (soon): Add comprehensive policy enforcement tests for all handlers.

4. **P2** (backlog): Move `PostForm` policy check earlier in the handler flow.

5. **P2** (backlog): Add audit logging when policy denies a request.

---

## Closing

The core implementation is sound — `checkBucketPolicy` is clean, correct, well-documented, and properly follows the S3 IAM semantics. The critical gaps are not in the quality of the implementation but in its **scope**: the task description identified 6 handlers (Put, PostForm, Get, Head, Delete, List) and those are correctly handled. However, the REST package contains ~22 handler functions total, and the remaining ~16 were not included in the security analysis.

This is a classic **scoping issue** rather than a code quality issue — the gap analysis in the architecture phase should have enumerated all REST endpoints and mapped each to its S3 action. Going forward, I recommend the team adopt a **endpoint×action matrix** when implementing authorization features, ensuring every handler is accounted for.
