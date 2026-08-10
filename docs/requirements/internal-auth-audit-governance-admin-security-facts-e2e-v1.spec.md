# Requirements Specification — `internal/auth`: admin/security facts in the B3 e2e matrix (T-3/T-4 through the real auth surface)

**Module:** `internal/auth`
**Direction:** "Auth-originated security facts are absent from the B3 e2e matrix — T-3/T-4 acceptance never exercised through the auth surface"
**Source analysis:** `docs/auto/analyses/internal-auth-ae3d8e54.json` (direction 1)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = this checkout)
**Score:** value 8 / risk reduction 6 / effort 5 / confidence 8

---

## 1. Scope

The G4 matrix (`cmd/server/governance_e2e_test.go`, M1–M6, REQ-3) drives audit governance **exclusively with file-origin facts**: every test calls `putObject` → `service.Put` → `EventBus` → `InsertEventWithGovernance` (governance_e2e_test.go:375-489). The exact output of the module under analysis — **admin/security facts** (`OriginKind=admin`, `FactKind=security` for `key.*` / `tenant.status` / `tenant.delete`) produced when `internal/api/rest/admin.go` `AddKey` (:141), `RevokeKey` (:164), `SetTenantStatus` (:391) call `RecordAudit` → `auditedRepository.RecordAudit` (auditgovernance/repository.go:27-41) → `factFromAudit` (facts.go:11-27) — is verified only at unit level (`fact_id_test.go:93-137` direct store calls with explicit tenant `"acme"`; `relay_metrics_test.go:157` `WrapRepository.RecordAudit` with `Action: "key.add"`). No test asserts that an auth mutation:

1. lands **exactly one** outbox row with a **recomputable deterministic ID** (T-4),
2. is **delivered** to the sink (M1-equivalent for admin origin),
3. reaches the **422/409/conflict terminal state within ≤1 attempt** and is then excluded from `ClaimAuditGovernance` and `OldestPendingAuditGovernance` (T-3),
4. gets **identical backlog-age gauge semantics** to matrix rows (D1),
5. resolves the **operator-key actor tenant `"*"`** (auth.go:12) *inside* a governed fact (`ActorDigest` of `"*"`, never asserted anywhere).

This spec adds one **test-only** file — `cmd/server/governance_e2e_admin_test.go` (package `main`, sibling to `governance_e2e_test.go` which is at 489/500 lines — the hard gate forces a new file) — that drives `POST /v1/admin/keys` through the **real auth middleware chain + `AdminHandler` + wrapped repository** (mirroring `TestGovernanceE2EMatrixDelivered`'s real-wiring pattern), reusing the existing harness (`newGovernanceE2E` :243-292, `govReceiver` modes, `waitForRow`/`quiesce`/`rowFor` idioms). Zero production/schema/dependency footprint.

Out of scope (see §4): RevokeKey/SetTenantStatus e2e cells (cited as producers, acceptance covers AddKey), operator/empty-tenant re-tenancy pinning (direction 2 of the same analysis), B3-5 grep gate (direction 3), gap-reconcile replay e2e, persistent-store keys, full chi router assembly, Postgres, `/metrics` scraping.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `cmd/server/governance_e2e_test.go:362-489` — "activation-gate + matrix tests, all file-origin; no admin/key/tenant row" | Activation-gate + matrix tests span :375-489 (`TestGovernanceE2EActivationGateBoundTenant` :375, `…UnboundTenant` :418, `TestGovernanceE2EMatrixDelivered` :442, `…PermanentClasses` :462, `…Transient200` :488); harness `newGovernanceE2E` :243-292 wires repo → `auditgovernance.WrapRepository` → `events.New` → `service.NewFileService(…).WithEventSink(bus)` — **no AdminHandler, no auth.Registry**; `outboxRow` helper hardcodes `WHERE origin_kind='file'` :309-325; `wantFactID` recomputes only file-origin IDs :299-319. `grep -n "admin\|key\.add\|RecordAudit\|AddKey\|SetTenant" governance_e2e_test.go` → **zero hits**. Only admin-origin SQL anywhere in e2e is a *negative* count (`origin_kind='admin'` → 0, `internal/api/s3compat/audit_governance_delete_e2e_test.go:57`) | ✅ **exact** — gap confirmed |
| E2 | `internal/api/rest/admin.go:114-141` — "AddKey audit" | `AddKey` :113-142; `h.audit(r, "key.add", body.Tenant, fmt.Sprintf("token=%s scopes=%v", redactToken(body.Token), body.Scopes))` at **:141** | ✅ **exact** |
| E3 | `admin.go:146-167` — "RevokeKey audit" | `RevokeKey` :146-167; tenant via `TenantForKey` :151-155; `h.auditForTenant(r, "key.revoke", redactToken(tok), "", tenant)` at **:164**; 404 path (`!revoked`) skips audit :161-163 | ✅ **exact** |
| E4 | `admin.go:361-391` — "SetTenantStatus audit" | `SetTenantStatus` :360-391 (start-line drift :360 vs cited :361 — trivial); `h.audit(r, "tenant.status", tenant, body.Status)` at **:391** | ✅ (1-line drift) |
| E5 | `internal/auditgovernance/repository.go:27-33` — "RecordAudit capture path" | `RecordAudit` :27-41; `entry.TenantID = normalizedTenant(...)` :30, `!r.runtime.Capture(entry.TenantID)` → plain fall-through :31-33, else `factFromAudit` + `RecordAuditWithGovernance` :34-40 | ✅ **exact** |
| E6 | `internal/auditgovernance/facts.go:11-27` — "factFromAudit; auditFactKind 'security'" | `factFromAudit` :11-27 (`OriginKind: repository.AuditOriginAdmin` :17, `ActorDigest: r.digest(tenant, "actor", entry.Actor)` :18); `auditFactKind` :89-93 — `key.` prefix or `tenant.status`/`tenant.delete` → **"security"**, else "admin" | ✅ **exact** |
| E7 | `internal/repository/audit_governance_write.go:38-39` — "store-authoritative DeterministicFactID recompute" | `RecordAuditWithGovernance` :20-47: origin assigned `RETURNING id` :28-31, `fact.OriginKind = AuditOriginAdmin` :32, `created_at` canonicalization :34-36, **`fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(fact.TenantID), fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)` :38-39** | ✅ **exact** |
| E8 | `audit_governance_factid.go:28-49` — "deterministic-ID formula's admin branch" | ⚠️ **path drift:** the file is `internal/repository/audit_governance_factid.go` (not `internal/auditgovernance/`). `DeterministicFactID` :28-49 — **branchless** single formula (`source\0tenant\0eventType\0originKind\0originID\0unixSeconds(occurredBucket)` → `hex(SHA-256(frame))[:32]`); the "admin branch" is not a formula branch but the **admin-origin call site** at E7:38-39 (vs the file-origin site at :84-85) | ⚠️ **path/wording drift; substance holds** |
| E9 | `internal/auditgovernance/fact_id_test.go:86-152` — "unit-level gap-vs-atomic, no handler path" | `assertGapEqualsAtomic` :74-90; `TestDeterministicFactID_GapEqualsAtomic_Admin` :93-137 — direct `redactor.factFromAudit` + `store.RecordAuditWithGovernance` calls, explicit tenant `"acme"`, **no HTTP handler, no auth middleware, no Registry**; `…_File` :139-173 | ✅ **exact** |
| E10 | `internal/auditgovernance/relay_metrics_test.go:157` — "unit-level key.add" | `wrapped.RecordAudit(ctx, repository.AuditEntry{TenantID: tenant, Action: "key.add"})` at **:157** — `WrapRepository`-level, **no handler path** | ✅ **exact** |
| E11 | `internal/auth/auth.go:12` — "operator keys resolve tenant '*'" | `auth.go:12` doc comment: `` `tenant == "*"` means the key is allowed for any tenant (admin operator)``; `Key.Has` :46-49 — `ScopeAdmin` ⇒ any scope | ✅ **exact** |
| E12 | T-3 store half — "row excluded from ClaimAuditGovernance and OldestPendingAuditGovernance" | `ClaimAuditGovernance` claim predicate `failed_at_ns=0` (sqlite :67-97, postgres :35-66); `FailAuditGovernance` :182-196 sets `failed_at_ns` + clears claim (lease-fenced); `OldestPendingAuditGovernance` :211-223 `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`; `failed_at_ns` column added by migration `0042_audit_governance_terminal_failed` | ✅ **exact** |
| E13 | Relay terminal classification — "422/409/conflict terminal within ≤1 attempt" | `classifyRelayError` relay.go:246-255; `isPermanentDeliveryError` :258+ — HTTP 409/422 (`*httpStatusError`), `ErrReceiptConflict`, `ErrInvalidReceipt` are terminal; `failFact` :113-124 → `FailAuditGovernance` (row terminal, no re-claim possible) | ✅ **exact** |
| E14 | D1 input — "same backlog age gauge semantics as matrix rows" | `PendingBacklogAge` runtime.go:191-198 (store-querying accessor over `OldestPendingAuditGovernance`), `BacklogAge()` cache :219-222; gauge `audit_governance_backlog_age_seconds` (internal/telemetry/metrics.go:365+) fed from the same store query | ✅ **exact** |
| E15 | Real-wiring pieces required by the acceptance | `rest.NewAdminHandler(svc, repo, reg)` admin.go:34-35; admin routes mounted `router.go:224` (`r.Post("/admin/keys", adm.AddKey)` :340); `requireRESTScope` router.go:365-375 (POST ⇒ `ScopeWrite`); `Registry.Middleware()` auth_middleware.go:15-52 (`authenticateBearer` :138 → `contextWithKey`; `/v1/admin/keys` **not** in `isBypassPath` :105-111); `Registry.Require` :198-216; `requireAdmin` admin.go:459-469 (registry disabled → implicit admin); `auditForTenant` admin.go:414-427 (actor = `k.Tenant` from `auth.FromContext`); outbox **`fact_kind` column** stored with `CHECK (fact_kind IN ('admin','security','file'))` (migration `0039_audit_governance_outbox`) | ✅ **all present** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "G4 matrix drives governance exclusively with file-origin facts" | ✅ **holds** (E1 — zero admin/key references in the e2e file). |
| "admin/security facts … verified only at unit level (fact_id_test.go:86-152, relay_metrics_test.go:157)" | ✅ **holds** (E9, E10 — both bypass handler/middleware). |
| "No test asserts an auth mutation lands exactly one outbox row with a recomputable deterministic ID, is delivered, or reaches the 422/409/conflict terminal state within ≤1 attempt" | ✅ **holds** — the only admin-origin outbox assertion in the tree is a negative count (`s3compat` delete e2e :57). |
| "The deterministic-ID formula's admin branch … unproven on the real auth call path" | ✅ **holds** (E7/E8/E9 — formula proven only at store-unit level). |
| "actor-tenant resolution (operator keys resolve tenant `"*"` per auth.go:12) is never asserted inside a governed fact" | ✅ **holds** (E11 — no test recomputes an `ActorDigest` from an operator key; `fact_id_test.go:93` uses an explicit tenant and a raw string actor). |
| "T-3 exclusions … row excluded from ClaimAuditGovernance and OldestPendingAuditGovernance" | ✅ **holds** (E12 — predicates already correct; only the auth-surface e2e is missing). |

---

## 3. Requirements

### REQ-1 — Admin-surface governance harness (new file `cmd/server/governance_e2e_admin_test.go`, package `main`)

Build `newAdminGovernanceE2E(t *testing.T, mode string) *govHarness` on top of the existing `newGovernanceE2E` (governance_e2e_test.go:243-292 — same repo/`WrapRepository`/`events.New`/`FileService.WithEventSink` wiring, relay deliberately unstarted, `ClientSecret`-env binding for `e2eTenant="acme"`):

- **Registry + operator key:** `reg := &auth.Registry{}`; `reg.AddKey(ctx, auth.Key{Token: opToken, Tenant: "*", Scopes: map[auth.Scope]bool{auth.ScopeAdmin: true}}, "", "")` — the in-memory path (auth.go:362-378, `store == nil` → `r.keys[k.Token] = k; r.enabled = true`) — an **operator key** exactly as documented at auth.go:12.
- **Real middleware chain** (mirrors router.go:224 + `requireRESTScope` router.go:365-375 + `requireAdmin` admin.go:459-469): `reg.Middleware()` (auth_middleware.go:15-52; `/v1/admin/keys` is not an `isBypassPath` :105-111) → `reg.Require(auth.ScopeWrite)` (auth_middleware.go:198-216; POST ⇒ write scope) → `adm.AddKey` with `adm := rest.NewAdminHandler(svc, wrepo, reg)` (admin.go:34-35). Request carries `Authorization: Bearer <opToken>`; `authenticateBearer` :138 resolves it via `Lookup` and `contextWithKey` injects `Key{Tenant:"*", Scopes:{admin}}`.
- **AddKey request:** `POST /v1/admin/keys` with body `{"token":"svc-key-1","tenant":"acme","scopes":["read","write"]}` — bound-tenant body (non-empty tenant passes the :128-132 guard) → HTTP **201** `{"tenant":"acme","scopes":[...]}`.
- **Actor resolution consequence** (the direction's unasserted fact): `auditForTenant` (admin.go:414-427) reads `k, ok := auth.FromContext(r.Context())` → `actor = k.Tenant = "*"`; `RecordAudit` stores it; `factFromAudit` (facts.go:18) digests it into `ActorDigest`.

### REQ-2 — T-4: exactly one outbox row, deterministic ID, delivered (mode `202-echo`, mirroring `TestGovernanceE2EMatrixDelivered` :442-460)

- **Pre-start snapshot (B1 mirror, before `startRelay`):**
  - exactly **one** `audit_log` row (`action='key.add'`, `tenant_id='acme'`, `actor='*'`);
  - exactly **one** outbox row with `origin_kind='admin'` — `SELECT COUNT(*) FROM audit_governance_outbox WHERE origin_kind='admin'` == 1 (I3 exact-key; `audit_log.id` and `object_events.id` are separate AUTOINCREMENTs, so the lookup must filter by origin kind — do **not** reuse the file-scoped `outboxRow` helper :309-325 as-is);
  - row fields: `tenant_id=="acme"`, `fact_kind=="security"` (migration 0039 column), `origin_id == audit_log.id`, `action=="key.add"`, `attempts==0`, `delivered_at_ns==0`, `failed_at_ns==0`, `available_at_ns>0`, `claim_owner==""`;
  - **operator actor pin inside the governed fact:** `actor_digest == "hmac-sha256:" + base64.RawURLEncoding.EncodeToString(HMAC-SHA256(HMACKey, "aero-vault/audit-governance/v1", "acme", "actor", "*"))` — recomputed from the harness config `HMACKey` via the documented digest recipe (redaction.go:16, :29-35, `writeMACFields` :81-87); `target_digest` likewise with field `"target"`, value `"acme"`. This is the first-ever assertion of operator-key tenant resolution inside a governed fact.
- **Delivered leg:** `startRelay`; `waitForRow` (admin variant): `delivered_at_ns>0`, `failed_at_ns==0`, `attempts==1`, `last_error==""`, `claim_owner==""`; `quiesce(50ms)`: `postCount==1`, `tokenCalls==1`; first POST `event_id == outbox id`.
- **Deterministic ID (replay converges):** `row.id == repository.DeterministicFactID(source, "acme", "key.add", repository.AuditOriginAdmin, auditLogID, occurred)` where `source` = wire `source_system` from the first POST body (captured by `govReceiver`, same as `wantFactID` :299-319), `occurred` = `time.Parse(time.RFC3339Nano, audit_log.created_at)` — `audit_log.created_at` is TEXT RFC3339Nano stored verbatim (fact_id_test.go:85-92 documents the lossless canonicalization), so atomic capture and any gap-reconcile replay converge on the same bucket (E7:34-36).

### REQ-3 — T-3: 422/409/conflict terminal within ≤1 attempt, excluded from claim and lag (modes `422`, `409`, `202-conflict`, mirroring `TestGovernanceE2EMatrixPermanentClasses` :462-486)

Table over the three sink modes; each case: same `POST /v1/admin/keys` as REQ-2, then `startRelay`:

- `waitForRow`: `failed_at_ns>0`, `delivered_at_ns==0`, `attempts==1`, `last_error` contains the mode's sentinel — `"audit governance HTTP 422"` / `"audit governance HTTP 409"` / `"reports a conflict"` (E13 classification).
- `quiesce(50ms)`: `postCount==1` — no retry (terminal; the claim predicate `failed_at_ns=0` at E12 makes re-claim impossible — belt-and-braces).
- **Exclusions (E12):** `ClaimAuditGovernance(ctx, owner, token, 1, 10, time.Minute)` returns no row with this id (empty result, since the only pending row is terminal); `OldestPendingAuditGovernance` → `ok==false`.

### REQ-4 — D1: identical backlog-age semantics to matrix rows (both REQ-2 and REQ-3 harnesses)

- **Pending phase** (REQ-2 pre-start / REQ-3 pre-start): `OldestPendingAuditGovernance` → `ok==true`; `rt.PendingBacklogAge(ctx)` (runtime.go:191-198 — the store-querying accessor that feeds the `audit_governance_backlog_age_seconds` gauge via the readiness probe) → `ok==true` with `age > 0` — the admin row is the oldest pending exactly as a matrix file row would be.
- **Terminal phase** (after REQ-3's `failed_at_ns` lands): `OldestPendingAuditGovernance` → `ok==false`; `PendingBacklogAge` → `ok==false` — dead-row exclusion identical to matrix rows (E12/E14). No `/metrics` scrape in this e2e: the gauge's store-side input is the pinned surface, matching how D1 is pinned for file rows (runtime_ready_test.go dead-row exclusion phase).

---

## 4. Decisions & non-goals

- **D1 — New file, not an extension of `governance_e2e_test.go`.** The existing file is at 489/500 lines (hard gate); `governance_e2e_admin_test.go` reuses the shared helpers (`newGovernanceE2E`, `govReceiver`, `waitForRow`, `quiesce`, `rowFor`, `startRelay` — same package `main`) and adds only the admin harness, an admin-scoped `outboxRow` variant (origin-kind-filtered), and an admin `wantFactID` variant.
- **D2 — Real middleware chain, not direct handler calls.** The acceptance requires "real AdminHandler+Repository wiring"; the test drives `reg.Middleware() → reg.Require(ScopeWrite) → AddKey` with a bearer token so `auditForTenant`'s actor resolution (`auth.FromContext`) executes production code — this is what makes the operator-tenant `"*"` assertion meaningful (E11/E15). No full `rest.NewRouter` assembly (out of scope: CORS/idempotency/rate-limit rings add no governed output).
- **D3 — Actor/target digest pins recompute the documented HMAC recipe** (redaction.go:16,29-35,81-87) rather than importing the unexported `redactor`. The harness's fixed `HMACKey` (32 B, `newGovernanceE2E` :263) makes the recompute deterministic; the format (`hmac-sha256:` + base64url) is already the asserted shape elsewhere in the package.
- **D4 — T-3 covers 422, 409, and the conflict receipt** (`202-conflict` mode → `ErrReceiptConflict`): the direction's acceptance names 422/409 and the problem statement names "422/409/conflict"; all three share the same terminal path (E13) and cost one extra table row.
- **Non-goals:** RevokeKey/SetTenantStatus e2e cells (E2-E4 cited only as producers; the acceptance pins AddKey — RevokeKey's `TenantForKey` empty-tenant behavior is direction 2's territory); operator/empty-tenant re-tenancy to `"default"` (direction 2 — `normalizedTenant` facts.go:86-88); B3-5 grep-consistency gate (direction 3); gap-reconcile replay e2e (parity already unit-pinned, E9); persistent-store keys (`WithStore`); Postgres; metrics exposition; any production, migration, `go.mod`, or config change.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (T-4 e2e row) —** *"e2e row — POST /v1/admin/keys with bound-tenant body → exactly one outbox row, FactKind=security, ID == DeterministicFactID recomputed from the audit_log row (replay converges)."*
*Testable:* REQ-2 — `TestGovernanceE2EAdminSecurityFactDelivered` (mode `202-echo`): pre-start `COUNT(*)` over `origin_kind='admin'` == 1 (and zero `origin_kind='file'` rows); row `fact_kind=='security'`, `origin_id == audit_log.id`; `row.id == repository.DeterministicFactID(wireSource, "acme", "key.add", "admin", auditLogID, RFC3339Nano(created_at))` — recomputed purely from observed wire/DB inputs (the `wantFactID` pattern), asserted **both pre-start and after delivery** (delivery never rewrites the ID); relay delivers it: `attempts==1`, `delivered_at_ns>0`, `failed_at_ns==0`, exactly one POST with `event_id == row.id`. Replay convergence follows from the formula inputs being the durably stored `audit_log` row (E7 canonicalization), the same inputs `factFromGap` parses (facts.go:67-84).

**AC-2 (T-3 terminal) —** *"sink 422/409 on that row → terminal failed_at_ns set within ≤1 attempt, row excluded from ClaimAuditGovernance and OldestPendingAuditGovernance."*
*Testable:* REQ-3 — `TestGovernanceE2EAdminSecurityFactTerminal` (table over modes `422`, `409`, `202-conflict`): `waitForRow` `failed_at_ns>0 ∧ delivered_at_ns==0 ∧ attempts==1` with the mode's sentinel in `last_error`; `quiesce(50ms)` `postCount==1` (no retry); then `ClaimAuditGovernance` returns no row with this id and `OldestPendingAuditGovernance` returns `ok==false` — both exclusions asserted against the store after the terminal write (E12).

**AC-3 (D1 gauge semantics) —** *"same backlog age gauge semantics as matrix rows."*
*Testable:* REQ-4 — pending phase (both harnesses, before relay start): `OldestPendingAuditGovernance ok==true` and `PendingBacklogAge ok==true, age>0` for the admin row (the gauge's store input, E14 — identical to a matrix file row's contribution); terminal phase (REQ-3 rows): `ok==false` on both accessors. No wall-clock equality assertions (age>0 only); the dead-row exclusion is the D1 property, mirroring the file-row pin in runtime_ready_test.go.

*All checks run against real `AdminHandler` + `Registry` middleware + wrapped `Repository` wiring on SQLite, mirroring `TestGovernanceE2EMatrixDelivered`.*

---

## 6. Risks

- **Origin-ID collision between `audit_log.id` and `object_events.id`** — separate AUTOINCREMENTs; the admin row lookup must filter `origin_kind='admin'` (I3 exact-key). The REQ-2 `COUNT(*)` assertion catches any cross-contamination, and the new `adminOutboxRow` helper never omits the kind filter.
- **Line-count gate** — `governance_e2e_test.go` is 489/500; the new file must stay ≤500 (target ≈ 320-380) by reusing the shared harness helpers; any helper that must differ (row/wantFactID admin variants) lives in the new file.
- **Timing flakes** — mitigated by the proven matrix idioms: `waitForRow` state predicates (no sleeps), `quiesce` for negative/stability windows (A6: negatives must never use `waitFor`), counter/`>` assertions only, deterministic backdating-free design (D1 asserts `age>0`, never an exact age).
- **Digest-recipe drift** — the actor/target digest recompute duplicates the HMAC recipe in the test; the recipe is pinned by existing redaction unit tests, so a drift fails loudly on both sides. The recompute helper is centralized in one function in the new file.
- **Middleware-chain fidelity** — `reg.Middleware()` differs from the production mount (`requireRESTScope` is package-private to `rest`); the chain used (`Middleware` → `Require(ScopeWrite)` → `requireAdmin` inside the handler) is exactly the production semantics for a POST admin route (E15) — documented in REQ-1.
- **`make check`** — must pass gofmt/build/vet/test (SQLite + `httptest`, zero network); no new `go.mod` dependencies (I6); the test package `main` import set (`auth`, `rest`, `httptest`) is already present in `cmd/server` (http.go:15).
- **Scope creep** — REQ-2/3/4 cover only the supplied acceptance; directions 2 and 3 of the same analysis are explicitly excluded (§4).

*Verification basis: all citations re-checked on this checkout (`15763e2`); line numbers reflect the working tree as read during this spec's production.*
