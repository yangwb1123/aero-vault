# Internal Auth: Admin/Security Facts in the B3 E2E Matrix — v1 Design

> Parent spec: `docs/requirements/internal-auth-audit-governance-admin-security-facts-e2e-v1.spec.md` (REQ-1…REQ-4, AC-1…AC-3).
> Direction: **test-only** — one new file `cmd/server/governance_e2e_admin_test.go` (package `main`), zero production/schema/dependency footprint. Verification basis: HEAD `15763e2` (working tree as read).
> Sibling precedent: `activation-gate-scope-alignment-matrix-e2e-v2.design.md` (same harness, file-origin only).

---

## 1. Evidence verification ledger (all citations re-checked on this checkout)

Every citation in the spec's evidence table was re-verified against the tree at `15763e2`. **All substance holds; only line spans drift trivially.**

| # | Cited | Verified on this checkout | Verdict |
|---|-------|---------------------------|---------|
| E1 | `governance_e2e_test.go` matrix :362-489, all file-origin; harness :243-292; `outboxRow` hardcodes `origin_kind='file'` | File is **489 lines** (hard gate 500). Tests span **:362-489** (`ActivationGateBoundTenant` :362, `…UnboundTenant` :396, `MatrixDelivered` :413, `MatrixPermanentClasses` :430, `MatrixTransient200` :460); harness `newGovernanceE2E` :182-278; `outboxRow` :279-294 `WHERE origin_kind='file' AND origin_id=?`; `wantFactID` :299-319 (file-origin recompute via `repository.DeterministicFactID`); `waitForRow` :329, `quiesce` :346, `rowFor` :353, `startRelay` :239, `eventRowID` :261. `grep -n "admin\|key.add\|RecordAudit\|AddKey\|SetTenant"` → **zero hits**; no `AdminHandler`, no `auth.Registry`, no `auth.*` import | ✅ substance exact (spans shift −13…−18 lines; the shared helpers exist exactly as described) |
| E2 | `admin.go:114-141` AddKey audit at :141 | `AddKey` :114-142; `h.audit(r, "key.add", body.Tenant, fmt.Sprintf("token=%s scopes=%v", redactToken(body.Token), body.Scopes))` at **:141**; body guard `token/tenant/scopes required` :128-132; 201 `{"tenant","scopes"}` | ✅ **exact** |
| E3 | `admin.go:146-167` RevokeKey audit at :164 | `RevokeKey` :146-167; `h.auditForTenant(r, "key.revoke", redactToken(tok), "", tenant)` at **:165**; 404 path skips audit :161-163 | ✅ (1-line drift) |
| E4 | `admin.go:361-391` SetTenantStatus audit at :391 | `SetTenantStatus` :360-391; `h.audit(r, "tenant.status", tenant, body.Status)` at **:391** | ✅ **exact** |
| E5 | `auditgovernance/repository.go:27-33` RecordAudit capture | `RecordAudit` :27-41: `entry.TenantID = normalizedTenant(...)` :30, `!r.runtime.Capture(...)` → plain fall-through :31-33, else `factFromAudit` + `store.RecordAuditWithGovernance` :34-40 | ✅ **exact** |
| E6 | `facts.go:11-27` factFromAudit; `auditFactKind` security mapping | `factFromAudit` :11-27 (`OriginKind: AuditOriginAdmin` :17, `ActorDigest: r.digest(tenant,"actor",entry.Actor)` :18, `TargetDigest`/`DetailSHA256` :24-25); `auditFactKind` :82-86 — `key.` prefix or `tenant.status`/`tenant.delete` → `"security"`, else `"admin"` | ✅ exact (mapping at :82-86 vs cited :89-93) |
| E7 | `audit_governance_write.go:38-39` store-authoritative recompute | `RecordAuditWithGovernance` :20-47: `RETURNING id` :28-31, `fact.OriginKind = AuditOriginAdmin` :32, created_at canonicalization :34-36, **`fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(...), fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)` :38-39**; file-origin call site :84-85 | ✅ **exact** |
| E8 | `audit_governance_factid.go:28-49` "admin branch" | File is `internal/repository/audit_governance_factid.go` (spec's correction holds). `DeterministicFactID` :28-49 — **branchless** `source\0tenant\0eventType\0originKind\0decimal(originID)\0decimal(unixSeconds(Truncate(time.Second)))` → `hex(SHA-256(frame))[:32]`; "admin branch" = the write.go:38-39 call site | ✅ **exact** (branchless confirmed) |
| E9 | `fact_id_test.go:86-152` unit-level only | `TestDeterministicFactID_GapEqualsAtomic_Admin` :87-121, `…_File` :122-152 — direct `redactor.factFromAudit` + `store.RecordAuditWithGovernance`, tenant `"acme"`, raw string actor, **no handler/middleware/Registry** | ✅ exact (span :87-152 vs cited :86-152) |
| E10 | `relay_metrics_test.go:157` | `wrapped.RecordAudit(ctx, repository.AuditEntry{TenantID: tenant, Action: "key.add"})` at :157 — `WrapRepository`-level, no handler | ✅ **exact** |
| E11 | `auth.go:12` operator tenant `"*"` | Doc comment `tenant == "*"` ⇒ admin operator at :12; `Key.Has` :46-49 (`ScopeAdmin` ⇒ any scope); `checkScope` auth_middleware.go:189-196 (POST ⇒ `ScopeWrite`); `authenticateBearer` :138-172 skips X-Aero-Tenant pin for `"*"` and injects `Key` via `contextWithKey` | ✅ **exact** |
| E12 | T-3 store half (claim exclusion) | `ClaimAuditGovernance` (repository/audit_governance_claim.go): postgres predicate `delivered_at_ns=0 AND failed_at_ns=0` :35-66, sqlite :67-97; `FailAuditGovernance` :182-196 sets `failed_at_ns`, clears claim, guarded by `failed_at_ns=0`; `OldestPendingAuditGovernance` :211-223 `WHERE delivered_at_ns=0 AND failed_at_ns=0`; migration `0042` adds `failed_at_ns` | ✅ **exact** (file is `audit_governance_claim.go`, not `sql.go` — spans cited are inside it) |
| E13 | 422/409/conflict terminal ≤1 attempt | `classifyRelayError` :235-246; `isPermanentDeliveryError` :247-262 — closed list: `ErrReceiptConflict`/`ErrInvalidReceipt` + HTTP 409/422 via `*httpStatusError`; `failFact` :120 → `store.FailAuditGovernance` (terminal, no re-claim) | ✅ exact (spans :235/:247 vs cited :246-255/:258+; `failFact` :120 vs :113-124) |
| E14 | D1 gauge store input | `Runtime.PendingBacklogAge` :191-198 (func :198) — store-querying over `OldestPendingAuditGovernance`, `ok=false` when none; `BacklogAge()` cache :219-222 | ✅ **exact** |
| E15 | Real-wiring pieces | `rest.NewAdminHandler(svc, repo, reg)` admin.go:34-35; router.go:224 `adm := NewAdminHandler(...)`, :340 `r.Post("/admin/keys", adm.AddKey)`; `requireRESTScope` router.go:365-375 (POST ⇒ `ScopeWrite`); `Registry.Middleware()` auth_middleware.go:15-52; `isBypassPath` :105-111 (**no** `/v1/admin/keys`); `Registry.Require` :198-216; `requireAdmin` admin.go:458-469 (registry disabled ⇒ implicit admin); `auditForTenant` admin.go:414-427 (actor = `auth.FromContext` `k.Tenant`); outbox `fact_kind TEXT NOT NULL CHECK (fact_kind IN ('admin','security','file'))` (migration `0039`, plus `UNIQUE (origin_kind, origin_id)` and `actor_digest`/`target_digest`/`created_at_ns` columns); `Registry.AddKey` in-memory path auth.go:362-378 (`store == nil` → `r.keys[k.Token]=k; r.enabled=true`) | ✅ **all present** |
| — | Digest recipe (spec D3) | `redactionDomain = "aero-vault/audit-governance/v1"` redaction.go:16; `digest` :29-35 — `hmac.New(sha256.New, key)`, `writeMACFields(mac, redactionDomain, tenant, field, value)`, `"hmac-sha256:" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))`; `writeMACFields` :81-87 writes **each field followed by a NUL byte**; `tenantSourceID` :44-55 = `SourcePrefix("aero-vault") + "." + base64url(hmac(...))` (wire `source_system` == `fact.SourceID`, http.go:142-160) | ✅ **exact** |
| — | `factFromGap` / `normalizedTenant` | `factFromGap` :48-76 (recompute call :73-75); `normalizedTenant` :86-88 (`""` → `"default"`) | ✅ exact (span :48 vs cited :67-84) |
| — | `make test-race` coverage | Makefile:106 `test-race:` = `go test -race -count=1 -timeout 120s ./internal/...` — **`./cmd/server/` excluded** (per v2 design G1) → sweep step must call it explicitly | ✅ constraint, not citation |
| — | cmd/server import set | http.go:15,18 already imports `internal/api/rest` + `internal/auth`; governance test imports `auditgovernance/config/events/repository/service/storage` | ✅ |

**Verdict: the spec's claims are trustworthy.** All drifts are line-number-only; no citation misrepresents behavior, location, or wiring.

---

## 2. Scope & non-goals

**In scope** (spec REQ-1…REQ-4, all three acceptance bullets AC-1…AC-3 preserved verbatim and mapped in §8):

- `cmd/server/governance_e2e_admin_test.go` (package `main`, sibling of `governance_e2e_test.go` — **no edit to the shared file**, which sits at 489/500 lines).
- `TestGovernanceE2EAdminSecurityFactDelivered` — AC-1/T-4: `POST /v1/admin/keys` through the real `Registry.Middleware() → Registry.Require(ScopeWrite) → AdminHandler.AddKey` chain with an operator bearer key → exactly one `origin_kind='admin'` outbox row, `fact_kind='security'`, deterministic ID recomputed from observed inputs, delivered with `attempts==1`; **first-ever assertion of the operator tenant `"*"` actor digest inside a governed fact**.
- `TestGovernanceE2EAdminSecurityFactTerminal` — AC-2/T-3 (table over sink modes `422`, `409`, `202-conflict`): terminal `failed_at_ns` within `attempts==1`, single POST, excluded from `ClaimAuditGovernance` and `OldestPendingAuditGovernance`.
- AC-3/D1 gauge parity in both tests: pending phase `OldestPendingAuditGovernance ok==true` + `PendingBacklogAge ok==true, age>0`; terminal phase `ok==false` on both. No `/metrics` scrape (store-side input is the pinned surface, matching `runtime_ready_test.go` dead-row pinning).

**Non-goals** (spec §4, unchanged): RevokeKey/SetTenantStatus e2e cells; operator/empty-tenant re-tenancy to `"default"` (direction 2); B3-5 grep gate (direction 3); gap-reconcile replay e2e; persistent-store keys (`WithStore`); full chi router assembly (CORS/idempotency/rate-limit rings add no governed output); Postgres; metrics exposition; any production/schema/`go.mod`/config change.

---

## 3. Harness design

### 3.1 Wiring — `newAdminGovernanceE2E(t *testing.T, mode string) *govHarness`

Reuses `newGovernanceE2E` (governance_e2e_test.go:182-278) **as-is** — same repo/`WrapRepository`/`events.New`/`FileService.WithEventSink` wiring, relay deliberately unstarted, `ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E"` binding for `e2eTenant="acme"`, fixed 32-B `HMACKey "0123456789abcdef0123456789abcdef"`, `Revision: 1`. The admin overlay adds only:

```
h   := newGovernanceE2E(t, mode)                     // existing shared harness
reg := &auth.Registry{}
reg.AddKey(ctx, auth.Key{Token: opToken, Tenant: "*",
    Scopes: map[auth.Scope]bool{auth.ScopeAdmin: true}}, "", "")   // in-memory path, auth.go:362-378
repo2, _  := repository.Open(ctx, "sqlite", h.dsn)   // second handle, same WAL file (no Migrate — schema exists)
wrepo     := auditgovernance.WrapRepository(repo2, h.rt)          // same runtime → same redactor/key
adm       := rest.NewAdminHandler(h.svc, wrepo, reg)              // admin.go:34-35
// chain: reg.Middleware() → reg.Require(auth.ScopeWrite) → adm.AddKey
// request: POST /v1/admin/keys, Authorization: Bearer <opToken>,
//          body {"token":"svc-key-1","tenant":"acme","scopes":["read","write"]} → expect 201
```

`opToken` = fresh nonce per test (e.g. `"op-"+t.Name()`), **not** one of the shared consts (`e2eToken` is the *receiver's* OAuth token — collision would silently reuse state).

**Why a second repo handle:** `govHarness` (governance_e2e_test.go:175-180) exposes only `{svc, dsn, receiver, rt}` — not `repo`/`wrepo`. The shared file must stay untouched (489/500 lines), so the admin file re-opens `h.dsn`. WAL mode (`repository.Open` → `journal_mode=WAL`, `MaxOpenConns(1)` per pool) makes a second reader handle safe; the harness's raw `sql.Open` conns (one per `outboxRow`) are the established precedent. The store-facing assertions cast `repo2.(auditgovernance.Store)` exactly as the harness does at :263.

**Middleware-chain fidelity** (spec REQ-1/D2): production mounts `requireRESTScope` (router.go:365-375) which maps POST ⇒ `ScopeWrite` and calls `reg.Require`; `requireAdmin` runs inside `AddKey` (admin.go:458-469). The test chain `reg.Middleware() → reg.Require(auth.ScopeWrite) → adm.AddKey` replicates those semantics with the same code (only the thin scope-derivation shim is re-typed, since `requireRESTScope` is package-private to `rest`). `authenticateBearer` (auth_middleware.go:138-172) resolves the op key, skips the X-Aero-Tenant pin for `"*"`, passes `checkScope` (POST ⇒ write; `ScopeAdmin` ⇒ any), and injects `Key{Tenant:"*", Scopes:{admin}}` → `auditForTenant` (admin.go:414-427) records `actor="*"`.

**Actor-resolution consequence** (the direction's unasserted fact): `factFromAudit` digests `actor="*"` into `actor_digest` — asserted via the recipe-recompute helper (§3.4).

### 3.2 Request & response

`POST /v1/admin/keys` body `{"token":"svc-key-1","tenant":"acme","scopes":["read","write"]}`:
- passes the :128-132 guard (all three non-empty) → `reg.AddKey` in-memory → **201** `{"tenant":"acme","scopes":["read","write"]}`.
- `h.audit(r, "key.add", "acme", "token=****svc1 scopes=[read write]")` — `redactToken("svc-key-1")` = `"****svc1"` (admin.go:475-479). TenantID `"acme"` is bound-active in the harness config → governance capture path (repository.go:31-40), **not** the plain fall-through.

### 3.3 Outbox snapshot (admin-scoped helpers — new file only)

All new SQL raw SQLite `?` placeholders (I1). `audit_log.id` and `object_events.id` are separate AUTOINCREMENTs (migrations 0016, 0003) — **never** reuse the file-scoped `outboxRow`/`waitForRow`/`rowFor`/`eventRowID` helpers for admin rows (I3 exact-key).

- `adminAuditLogRow(t, dsn) (auditLogRow, error)` — `SELECT id, created_at, actor, action, tenant_id FROM audit_log WHERE action='key.add' AND tenant_id='acme' ORDER BY id DESC LIMIT 1` (fresh DB per test ⇒ exactly one row; `created_at` TEXT RFC3339Nano stored verbatim, insertAuditEntry audit.go:20-29).
- `adminOutboxRow(t, dsn, auditLogID) (govOutboxRow, error)` — `SELECT id,tenant_id,origin_kind,origin_id,fact_kind,actor_digest,target_digest,action,attempts,available_at_ns,claim_owner,last_error,delivered_at_ns,failed_at_ns,lease_expires_at_ns FROM audit_governance_outbox WHERE origin_kind='admin' AND origin_id=?` — `UNIQUE (origin_kind, origin_id)` (migration 0039) makes the lookup unambiguous; `fact_kind`/`actor_digest`/`target_digest` extend the shared `govOutboxRow` struct (new fields in the new file's local struct or a superset type).
- `adminWaitForRow(t, dsn, auditLogID, pred)` — copy of the `waitForRow` :329 idiom bound to `adminOutboxRow` (state predicates only, 10 s deadline, 5 ms poll).
- `adminRowFor(t, h, auditLogID)` — admin `rowFor` :353 idiom.
- `countOutbox(t, dsn, kind string) int` — `SELECT COUNT(*) FROM audit_governance_outbox WHERE origin_kind=?`.

### 3.4 Recompute helpers (recipe, not private imports — spec D3)

- `wantDigest(t, hmacKey, tenant, field, value) string` — implements redaction.go:29-35 + :81-87: `"hmac-sha256:" + base64.RawURLEncoding.EncodeToString(hmacSHA256(key, "aero-vault/audit-governance/v1" + "\x00" + tenant + "\x00" + field + "\x00" + value + "\x00"))` — **each field NUL-terminated including the last** (writeMACFields writes field then `{0}` per field). Centralized in one function; drift fails loudly on both sides (recipe is pinned by `redaction_test.go`).
- `wantAdminSource(t, hmacKey) string` — `tenantSourceID("acme")` recipe: `"aero-vault." + strings.TrimPrefix(wantDigest(t, key, "acme", "source-system", "acme"), "hmac-sha256:")` — 54 chars (`aero-vault.` = 11 + 43 base64url), shape pinned like `wantFactID` :299-319. After delivery assert `h.receiver.source == wantAdminSource(...)` — this also pins `tenantSourceID`'s output on the real wire (stronger than the shared file's shape-only pin).
- `adminWantFactID(t, dsn, source string, auditLogID int64) string` — reads the `audit_log` row (id + `created_at`), `occurred, _ := time.Parse(time.RFC3339Nano, createdRaw)`, returns `repository.DeterministicFactID(source, "acme", "key.add", repository.AuditOriginAdmin, auditLogID, occurred)` — same observed-input pattern as `wantFactID`, admin flavor. Replay-convergence follows from the atomic write's canonicalization (write.go:34-39) using the same durable inputs `factFromGap` parses (facts.go:48-76).

**Pre-start ID assertion without a wire POST:** pre-start the receiver has not yet seen `source_system`, so the pre-start leg recomputes `source := wantAdminSource(...)` (deterministic — fixed `HMACKey`); post-delivery the same assertion reruns with `h.receiver.source` (observed wire) and additionally asserts `h.receiver.source == wantAdminSource(...)`. Both legs must agree because wire `source_system` == `fact.SourceID` (http.go:142-160).

### 3.5 Receiver reuse

`govReceiver` modes `202-echo` / `409` / `422` / `202-conflict` (governance_e2e_test.go:66-170) are reused verbatim — zero new receiver code; the sentinels (`"audit governance HTTP 422"`, `"audit governance HTTP 409"`, `"reports a conflict"`) come from `classifyRelayError`/`isPermanentDeliveryError` (relay.go:235-262) exactly as in the matrix cells.

---

## 4. API changes

**Production: none.** No change to `router.go`, `admin.go`, `auth.go`, `auth_middleware.go`, `auditgovernance/*`, `repository/*`, migrations, config, `go.mod`, or the shared `governance_e2e_test.go`.

**Test surface (additions only, package `main`):**

| New symbol | Kind | Purpose |
|---|---|---|
| `TestGovernanceE2EAdminSecurityFactDelivered` | test | AC-1/T-4 delivered leg (REQ-2) |
| `TestGovernanceE2EAdminSecurityFactTerminal` | test, table | AC-2/T-3 over `422`/`409`/`202-conflict` (REQ-3) |
| `newAdminGovernanceE2E` | helper | §3.1 overlay (Registry + op key + second repo handle + AdminHandler + real chain) |
| `adminAuditLogRow`, `adminOutboxRow`, `adminWaitForRow`, `adminRowFor`, `countOutbox` | helpers | §3.3 admin-scoped store access (I3 exact-key) |
| `wantDigest`, `wantAdminSource`, `adminWantFactID` | helpers | §3.4 recipe/observed-input recompute |

Imports added: `crypto/hmac`, `crypto/sha256`, `encoding/base64`, `net/http` (already), `internal/api/rest`, `internal/auth` — all stdlib (I6); `rest`/`auth` already imported by `cmd/server/http.go:15,18`; `repository`/`auditgovernance` already imported by the shared test file.

---

## 5. Compatibility constraints

| # | Constraint | How the design complies |
|---|-----------|------------------------|
| C1 | **500-line hard gate** on the new file | Target ≈ 360-400 lines; all reuse via shared helpers; only admin-differing helpers live in the new file. Shared file untouched (489/500 — an edit risks the gate). |
| C2 | **I1 — SQL placeholders** | New SQL is raw SQLite `?` only; ns comparisons on `*_ns` int64 columns; `created_at` read as TEXT and parsed `time.RFC3339Nano` (byte-identical to the write path's flexTime scan, fact_id_test.go:85-92). |
| C3 | **I2 — migrations** | No migration edits. Design *depends on* `0016` (`audit_log`), `0039` (outbox incl. `fact_kind` CHECK + `UNIQUE (origin_kind, origin_id)` + digest columns), `0042` (`failed_at_ns`), `0043`/`0044` (indexes/anchors). A schema rebase that drops any of these fails the assertions loudly (never silently). |
| C4 | **I3 — exact-key, no reverse parsing** | Admin row lookup always filters `origin_kind='admin'`; never reuses file-scoped helpers; `UNIQUE (origin_kind, origin_id)` guarantees unambiguity. |
| C5 | **I4 — chain order; handler doesn't self-mount** | Test chain is the auth-relevant slice of production order (`Middleware` → `Require(ScopeWrite)` → in-handler `requireAdmin`); no router assembly. `isBypassPath` (auth_middleware.go:105-111) does not cover `/v1/admin/keys` — the middleware actually authenticates. |
| C6 | **I5 — opt-in safety defaults** | Test constructs its own `auth.Registry` (fresh ⇒ `store==nil` ⇒ in-memory AddKey path); governance is enabled only by the harness config. Zero effect on any flag-gated production path. |
| C7 | **I6 — stdlib only** | No new `go.mod` deps; imports already present in `cmd/server`. |
| C8 | **`make check`** | gofmt/build/vet/test must pass; SQLite + `httptest`, zero network (receiver is local `httptest.Server`). |
| C9 | **Race sweep** | `make test-race` (Makefile:106-109) targets `./internal/...` only — **`cmd/server` is excluded**, so the sweep step (§7) adds `go test -race -count=1 -timeout 120s ./cmd/server/` explicitly (v2 design G1 finding). |

---

## 6. Failure modes & mitigations

| # | Failure mode | Impact | Mitigation |
|---|--------------|--------|-----------|
| F1 | **Origin-ID collision** — `audit_log.id` vs `object_events.id` are independent AUTOINCREMENTs | Wrong row / cross-contamination | `adminOutboxRow` always filters `origin_kind='admin'` (C4); `countOutbox` asserts exactly 1 admin + 0 file rows pre-start (cross-origin noise fails loudly) |
| F2 | **Timing flakes** — claim racing the pre-start snapshot | Nondeterministic `attempts`/`claim_owner` | Relay deliberately unstarted until after the snapshot (B1 idiom); `adminWaitForRow` state predicates (10 s deadline), `quiesce` for negatives/stability (A6 — negatives never use waitFor); assertions are counter/`>`-only; D1 asserts `age>0`, never an exact age |
| F3 | **Digest-recipe drift** — test duplicates the HMAC recipe | Silent digest mismatch or test-only divergence | Single `wantDigest` helper implementing the documented recipe (redaction.go:16/29-35/81-87); recipe pinned by existing `redaction_test.go` — drift fails loudly on both sides |
| F4 | **Middleware-chain fidelity** — `requireRESTScope` is package-private | Test could drift from production scope derivation | Chain replicates the exact semantics (POST ⇒ `ScopeWrite` ⇒ in-handler `requireAdmin`); `authenticateBearer`'s `"*"` handling (skip tenant pin, `checkScope` pass) is exercised by the op key — the whole point of REQ-1 |
| F5 | **Registry state leakage** — `auth.Registry` is a singleton-like in-memory map | Cross-test key collisions / `enabled` leaks | Fresh `&auth.Registry{}` per test; unique `opToken` per test; registry `enabled` state is per-instance, not package-global |
| F6 | **Second sqlite handle** — WAL writer contention | `SQLITE_BUSY` flakes | Harness conns are readers; the only second-handle write is the REQ-3 exclusion `ClaimAuditGovernance`, which is a no-op UPDATE (no matching rows) executed after relay quiesce; WAL permits one writer, and the relay is quiesced first |
| F7 | **Line-count gate** — new file > 500 | Hard gate failure | Target ≈ 360-400; helpers are compact; any overflow is resolved by trimming assertion prose, never by touching the shared file |
| F8 | **Terminal-vs-retry misclassification** — sentinel drift in `last_error` | REQ-3 table fails on a non-terminal row | Sentinel substrings come from the same closed list as the matrix (relay.go:247-262); `quiesce(postCount==1)` plus claim-exclusion assertions (belt-and-braces — claim predicate `failed_at_ns=0` makes re-claim impossible) |
| F9 | **Schema drift** — 0039/0042 dropped or altered | Hard failures | Direct column reads (`fact_kind`, `failed_at_ns`, `actor_digest`) in every row snapshot; no tolerance code paths |

---

## 7. Migration & delivery steps

No schema/data migration (test-only). Delivery sequence:

1. **Add** `cmd/server/governance_e2e_admin_test.go` (package `main`, §3-§4 surface). No other file touched.
2. **Static gates:** `gofmt -l cmd/server/` (empty output) → `go build ./...` → `go vet ./...`.
3. **Focused run:** `go test ./cmd/server/ -run 'TestGovernanceE2EAdmin' -count=1 -v` (expect 1 + 3 table subtests green).
4. **Full gate:** `make check` (fmt/vet/build/test + `test-race-meta` + cli-check).
5. **Race sweep** (C9): `go test -race -count=1 -timeout 120s ./cmd/server/` — explicit, because `make test-race` scopes to `./internal/...`.
6. **Commit** — test-only; verify `git status` shows no production/schema/`go.mod`/`.env.example` changes beyond this file.

---

## 8. Testable acceptance mapping (spec §5 bullets preserved verbatim)

| Acceptance (verbatim from spec §5) | Testable mapping |
|---|---|
| **AC-1 (T-4 e2e row)** — *"e2e row — POST /v1/admin/keys with bound-tenant body → exactly one outbox row, FactKind=security, ID == DeterministicFactID recomputed from the audit_log row (replay converges)."* | `TestGovernanceE2EAdminSecurityFactDelivered` (mode `202-echo`): POST → 201; pre-start: `countOutbox("admin")==1 ∧ countOutbox("file")==0`; `adminOutboxRow`: `tenant_id=="acme"`, `fact_kind=="security"`, `origin_id==audit_log.id`, `action=="key.add"`, `attempts==0`, `delivered_at_ns==0`, `failed_at_ns==0`, `available_at_ns>0`, `claim_owner==""`; `row.id == adminWantFactID(wantAdminSource(...), auditLogID)` **pre-start** and `== adminWantFactID(h.receiver.source, auditLogID)` **post-delivery** (delivery never rewrites the ID); actor pin `row.actor_digest == wantDigest(key,"acme","actor","*")` + `row.target_digest == wantDigest(key,"acme","target","acme")` — **first-ever operator-tenant digest assertion**; `startRelay` → `adminWaitForRow(delivered>0 ∧ failed==0 ∧ attempts==1 ∧ last_error=="" ∧ claim_owner=="")`; `quiesce(50ms)`: `postCount==1 ∧ tokenCalls==1`; `firstPost().eventID == row.id`; `h.receiver.source == wantAdminSource(...)` |
| **AC-2 (T-3 terminal)** — *"sink 422/409 on that row → terminal failed_at_ns set within ≤1 attempt, row excluded from ClaimAuditGovernance and OldestPendingAuditGovernance."* | `TestGovernanceE2EAdminSecurityFactTerminal` (table: `{422, "audit governance HTTP 422"}`, `{409, "audit governance HTTP 409"}`, `{202-conflict, "reports a conflict"}`): POST → 201; `startRelay` → `adminWaitForRow(failed>0 ∧ delivered==0 ∧ attempts==1 ∧ last_error contains sentinel)`; `quiesce(50ms)` `postCount==1` (no retry); then `repo2.(auditgovernance.Store).ClaimAuditGovernance(ctx, "e2e-owner", "e2e-token", 1, 10, time.Minute)` returns **no row with this id** (claim predicate `failed_at_ns=0`, audit_governance_claim.go; revision 1 == harness `Revision:1`), and `OldestPendingAuditGovernance` → `ok==false` |
| **AC-3 (D1 gauge semantics)** — *"same backlog age gauge semantics as matrix rows."* | Pending phase (both tests, before `startRelay`): `OldestPendingAuditGovernance` → `ok==true` and `h.rt.PendingBacklogAge(ctx)` → `ok==true ∧ age>0` (admin row is the oldest pending, identical to a matrix file row's contribution to `audit_governance_backlog_age_seconds`). Terminal phase (`…Terminal` after `failed_at_ns` lands): `OldestPendingAuditGovernance` → `ok==false` and `PendingBacklogAge` → `ok==false` (dead-row exclusion). No wall-clock equality; no `/metrics` scrape (store-side input is the pinned surface, mirroring `runtime_ready_test.go`) |

*All checks run against real `AdminHandler` + `Registry` middleware + wrapped `Repository` on SQLite, mirroring `TestGovernanceE2EMatrixDelivered`.*

---

## 9. Risks (delta over spec §6)

- **Second-repo-handle assumption** (F6) is the only piece of the design not directly exercised by an existing test pattern; the safety argument is WAL + read-only harness conns + no-op claim after quiesce. If `-race`/CI ever shows `SQLITE_BUSY`, the fallback is adding `repo`/`wrepo` fields to `govHarness` in the shared file (a 2-line change that keeps the shared file at ≤ 491 lines — still under the gate).
- **Pre-start ID leg depends on the `tenantSourceID` recipe** (wantAdminSource); the post-delivery wire-source leg is the authoritative pin, and the equality assertion `h.receiver.source == wantAdminSource(...)` makes the pre-start leg redundant-but-consistent rather than load-bearing.
- **Operator-tenant assertion adds a new pin surface**: if a future change makes `auditForTenant` fall back to `"default"` for `"*"` actors (direction 2's territory), this test fails loudly — which is the point (spec REQ-1), but it means this test *is* the tripwire for direction-2 work and should be updated in that campaign.

*Verification basis: all citations re-checked on this checkout (`15763e2`); line numbers reflect the working tree as read during this design's production.*
