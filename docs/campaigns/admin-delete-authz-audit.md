# Authz Audit — `admin files delete` surface (design: admin-files-delete-cli-v1)

**Baseline:** HEAD `acfaaf4` · **Method:** code reading (all claims file:line-verified) + empirical chi v5.1.0 routing harness (adversarial path corpus) + chain-order verification. `make check` unaffected (no repo changes made).

## Verdict table

| # | Audit concern | Verdict |
|---|---------------|---------|
| 1 | Operator vs tenant-scoped key semantics (`tenant="*"` bypass) | ⚠️ **Conditional gap** — tenant-scoped admin keys are cross-tenant-capable when `ACCESS_CONTROL_ENABLED=false` (the default). Design F2/C3 overstate the 403 protection. |
| 2 | Cross-tenant `X-Aero-Tenant` mismatch | ✅ Header pinning correct (403 pre-handler); path-vs-header divergence is the admin feature; CLI constraint (C3) accurate. |
| 3 | Fail-closed unknown/empty tenant | ⚠️ Unknown→404 ✓, disabled→403 ✓, **empty tenant silently normalizes to `"default"`** (footgun; inconsistent with sibling `DeleteTenant`). |
| 4 | WORM/legal-hold bypass via admin hard delete | ✅ No bypass (service per-version check + repo in-tx check). **Design F5 factual error: 409 `ObjectLocked`, not 412.** |
| 5 | Audit-log completeness for hard deletes | ✅ Atomic single row (`file.delete`, `detail=hard`), actor=`apikey:<hash>`, no double-audit. |
| 6 | Key-traversal in chi `{tenant}/*` catch-all | ✅ No traversal reachable (escaped params + `validateKey` + DB-driven StorageKey + storage guard). ⚠️ But **D3's "租户名不允许 `/`，上游校验" is false** — no tenant-ID validation exists. |
| 7 | "No new failure surface" claim under adversarial inputs | ⚠️ Holds for error taxonomy; **4 doc deltas**: F5 status code, F2 conditional 403, undocumented bucket-policy bypass, uncovered empty-tenant case. |
| 8 | I3 / I4 / I5 discipline | ✅ All hold. |

---

## 1. Operator vs tenant-scoped key semantics

**Chain for the new endpoint:** `requireRESTScope` (router.go:331-345: DELETE→`ScopeWrite`; `Key.Has`, auth.go:56-59: admin scope satisfies any scope) → `requireAdmin` (admin.go:457-465) → `svc.Delete` → `authorizeObject` → `authorize` (access.go:76-98):

```go
if err := s.requireActiveTenant(ctx, resource.TenantID); err != nil { return err }
if s.authorizer == nil { return nil }   // ← everything below is skipped
```

- `ACCESS_CONTROL_ENABLED` defaults **false** (main.go:216); `buildAccessManager` returns nil (access.go:11-25) → `WithAuthorizer(nil)` → **the entire tenant-membership/ACL layer is skipped in the default config**.
- With authorizer nil, **nothing binds the path tenant to the key tenant**. A tenant-scoped key `acme:admin` + `DELETE /v1/admin/files/other/<key>?hard=1` **succeeds**: `authenticateBearer` only pins the *header* (`X-Aero-Tenant`), and the handler takes the tenant from the *path* (design D5; AC-1 asserts this).
- Pre-existing pattern: `SetQuota`/`SetBudget`/`DeleteTenant`/`PutBucketQuota` are all path-tenant + `requireAdmin`-only (admin.go:64-75, 140-160, 326-357). The *pattern* is inherited; what is **new** is the destructive reach: REST (`handler.go:243` uses `mw.TenantFrom`), s3compat, and WebDAV all resolve the tenant from the pinned header, so tenant-scoped keys could **never delete another tenant's objects before**. This is the first object-delete surface with a path-addressable tenant under admin-scope-only gating.
- With access manager ON: `tenantMatches` (authorizer.go:67-69) → 403 `tenant_mismatch` ✓ — **F2 is correct only in this configuration**. Within its own tenant, `isAdministrator` (authorizer.go:126-135) allows regardless of object ACLs (matching explicit deny still wins — deny is checked first, authorizer.go:46-49).
- **Recommendation:** either (a) document "admin-scoped keys are trusted operators; the tenant field is identity, not an authority boundary" (consistent with every existing admin route) — zero code, or (b) add a principal-vs-path-tenant check in `DeleteFile` (~5 lines, deviates from sibling handlers). Audit recommends (a) plus fixing the design wording; the CLI usage note should warn that tenant-scoped admin keys are operator-equivalent in the default config.

## 2. Cross-tenant `X-Aero-Tenant` mismatch

- `authenticateBearer` (auth_middleware.go:148-158): `k.Tenant != "*"` + mismatched header → **403 "tenant mismatch" before any handler runs**; empty header → pinned to key tenant. Operator keys (`*`) intentionally unpinned.
- Presigned/anon paths are GET/PUT-only — irrelevant to DELETE. `isBypassPath` does not include `/v1/admin` (auth_middleware.go:105-112) → anonymous admin → 401.
- CLI `do()` sends the header only when `AERO_TENANT` is set (cli.go:57-60) — design C3 usage constraint accurate.
- Header-vs-path divergence is the admin feature itself; the only enforcement of the caller's tenant boundary is the access layer (finding 1).

## 3. Fail-closed for unknown/empty tenant

- **Unknown tenant** → `GetObject` → `ErrNotFound` → 404 (classify, handler_helpers.go:37-38) ✓ fail-closed, no info leak (object vs tenant not distinguished).
- **Disabled tenant** → `requireActiveTenant` → 403 `TenantDisabled` (access.go:103-115), always enforced in the server (`WithTenantStatusEnforcement`, main.go:95). Note the middleware-level disabled check uses the *header* tenant only; the *service-level* check uses the *target* tenant and is the one that fires here ✓.
- **Empty tenant: NOT fail-closed.** Empirically chi matches `/v1/admin/files//key` with `tenant=""`; `checkedObjectDefaults` → `defaults("")` → `"default"` (file.go:264-269) → **silently deletes from the default tenant**. CLI `admin files delete "" key` does the same. Sibling `DeleteTenant("")` → repo lookup → 404 (tenants.go:74-90), so the new endpoint is inconsistent with its siblings. Fix: reject empty tenant in `adminFilesDelete` (usage, exit 2) and/or handler 400.
- Tenant literally named `"*"` is creatable (no validation) and addressable in the path — no auth bypass (the `"*"` bypass is key-tenant-based, authorizer.go:67), but semantically collides with the operator wildcard in docs/audit.

## 4. WORM/legal-hold bypass

- Hard delete: `versionsForHardDelete` → `checkObjectProtection` **per version** (file_delete.go:57-70; file_crud.go:273-290: `LockedUntil`, `_aero_legal_hold` metadata, `legal_holds` table) **plus** an in-transaction `legal_holds` re-check in `HardDeleteObjectWithEvent` (event_outbox.go:110-116). Double-gated, **no bypass**. Admin hard delete is byte-identical to REST hard delete here (same `svc.Delete`).
- **Design F5 factual error:** `ErrLocked` maps to **409 `ObjectLocked`** via `classifyLock` (management.go:223-228), not 412 `PreconditionFailed`. REST delete of a locked object also returns 409. Fix F5 and the AC tests should assert 409.
- Soft delete (no `--hard`) performs **no protection check** (`softDeleteObject`, file_delete.go:76-98) — pre-existing REST semantics; the blob survives and the object is restorable (`Restore`, file_restore.go:14-46). Inherited, not new. WORM data is never destroyed by any path.
- `DELETE /v1/legal-hold` is write-scope-gated, not admin-gated (router.go:426) — pre-existing; admin-scoped keys satisfy write scope, so hold-removal-then-hard-delete is an existing operator capability, unchanged by this design.

## 5. Audit-log completeness

- `HardDeleteObjectWithEvent` (event_outbox.go:102-142): one transaction = legal-hold re-check → access-state cleanup → `DELETE FROM objects` → `insertAuditEntry` → `insertOutboxFacts` → commit. Zero rows → `ErrNotFound` + rollback (no phantom audit/outbox rows). **Exactly one audit row** (`file.delete`, `detail=hard|soft`) per successful admin delete, atomic with the delete ✓.
- Actor = `access.PrincipalFrom` → `PrincipalForKey` → `"apikey:<hash24>"` (principal.go:11-38) — deletions are attributable to a stable key hash for both operator and tenant-scoped keys; `""` only when no principal (legal per C9). `TenantID` = target tenant; `Target` = `default/<key>` (D7 consistent).
- **No double-audit:** the new handler correctly does *not* call `h.audit()` (unlike quota/key handlers, which write their own rows) — the delete audit comes from the service atomically.
- F7 (storage-first ordering, file_delete.go:14-17: blob delete before metadata tx → failure leaves zero residue) and F8 (tx rollback on audit INSERT failure) verified. `preflightQuota` on the delete path does hit the DB (file_crud.go:22-36) → DB outage → 500 before any mutation (fail-closed, pre-existing).

## 6. Key-traversal in the `{tenant}/*` catch-all

Empirical corpus (chi v5.1.0): raw `..`, `%2e%2e%2f`, `..%2f..%2fetc%2fpasswd`, empty segments, double slashes, `%2F`, `%00`, trailing slash, tenant-position traversal.

- chi v5.1.0 routes on `r.URL.RawPath` (escaped) and **`URLParam` returns the escaped segment text** (mux.go:431-437 + harness: `%2e%2e%2fsecret` → `star="%2e%2e%2fsecret"`). No `PathUnescape`/`QueryUnescape` exists between router and service (grep over `api/rest` + `cli`).
- Raw `..` in key → `validateKey` rejects (file.go:191-203: empty / >200 chars / contains `..` / leading `/`). Encoded `..` stays literal → literal DB lookup; the storage blob key is always the **DB row's `StorageKey`** (computed at Put time via `path.Join` under `validateKey`) — the storage layer never sees the request key. Storage independently rejects empty/`/`/`..` keys (storage.go:23). **No traversal path exists.**
- Escaped-key convention: keys with spaces/unicode are stored in escaped form consistently across PUT/GET/DELETE. **The new handler must not add unescaping** — D5 correctly reuses `keyFromPath` verbatim.
- Minor: `/acme//key` → `star="/key"` → `TrimPrefix` → `key` (double slash collapses; cosmetic, same-key semantics).
- **D3 claim "租户名不允许 `/`，上游校验" is false:** no tenant-ID validation exists — `CreateTenant` only checks non-empty (admin.go:243-246); `UpsertTenant` is free-form (tenants.go:16-32). Consequences (all fail-closed but functional gaps): (a) tenant `"a/b"` is *unreachable and ambiguous* via the path-style route (silently targets tenant `"a"`, key `"b/…"`); (b) tenants with escapable chars (e.g. `"a b"`) are unreachable — CLI escapes `a%20b`, chi returns `a%20b`, DB tenant is `a b` → 404; (c) tenant `"*"` is creatable. Fix the doc: the path-style admin surface requires path-safe tenant names, or add tenant-ID validation (global change, out of scope).

## 7. "No new failure surface" claim — adversarial inputs

Holds for the error taxonomy: every failure flows through pre-existing `classify` mappings (handler_helpers.go:26-62 + classifyLock), D6 reuses `classify`, no new sentinels/retries/telemetry; adminRL + global concurrency (write weight 2) apply; idempotency intentionally not applied (retry → 404, same as other admin routes). Four deltas to fix in the design doc:

1. **F5:** locked hard delete → **409 `ObjectLocked`**, not 412.
2. **F2/C3:** the 403 tenant-scope protection is **conditional on `ACCESS_CONTROL_ENABLED`**; in the default config tenant-scoped admin keys are cross-tenant-capable (finding 1) — the "纵深防御" framing needs the config caveat.
3. **Undocumented bucket-policy bypass:** REST `Handler.Delete` enforces `checkBucketPolicy("s3:DeleteObject")` (handler.go:238-245); the designed admin `DeleteFile` does not → admin hard delete ignores IAM-style bucket policy (including source-IP deny) that protects the same object at the REST surface. Defensible (policy is a data-plane protocol guard), but it is a semantic divergence between the two delete surfaces and must be an explicit documented decision — §5 currently omits it.
4. **Uncovered empty-tenant case** (finding 3): `DELETE /v1/admin/files//key` and CLI `admin files delete "" key` silently target `"default"`.

## 8. I3 / I4 / I5 discipline

- **I3 ✓** — handler performs no key re-validation (D5/C8); service `validateKey` + storage guard; storage keys come from DB rows; the design's "don't unescape" convention is correct.
- **I4 ✓** — zero middleware-chain changes; route registered inside the existing admin group (inherits `adminRL`); chain order verified in http.go:146-172 (request_id outermost → … → auth → tenant → rate_limit → … → access_log innermost — matches AGENTS.md §2.5; Auth≺Tenant≺RateLimit holds); handlers don't self-attach middleware.
- **I5 ✓** — no new opt-in flags; nil authorizer preserves the CI baseline (`authorize` early-returns); the endpoint is always-on like every other admin route.

## Recommended doc fixes before implementation

1. F5 → 409 `ObjectLocked` (and AC-1 locked-delete test asserts 409).
2. C3/F2 → add `ACCESS_CONTROL_ENABLED` conditionality; state the operator-equivalence of tenant-scoped admin keys in the default config.
3. §5 → add a bucket-policy-bypass row (documented decision) and an empty-tenant row (reject with 400 / CLI exit 2).
4. D3/C8 → correct the "上游校验" claim; document the path-safe tenant-name constraint (no `/`, no escapable chars, avoid `"*"`).
5. CLI: reject empty tenant argument (exit 2, usage error) — one line.
