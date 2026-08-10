# Requirements Specification — deterministic fact IDs (contract item 3 / B3-3 / T-4) pinned by an s3compat e2e

**Module:** `internal/api/s3compat` (pin point) — mechanism under test lives in `internal/auditgovernance` + `internal/repository`
**Direction:** "Deterministic fact IDs (contract item 3, T-4) pinned by an s3compat e2e asserting outbox.id == recomputed SHA-256" (direction 2 of `docs/auto/analyses/internal-api-s3compat-eeefa063.json`)
**Source analysis:** `docs/auto/analyses/internal-api-s3compat-eeefa063.json` · **Date:** 2026-08-08 · **HEAD:** `f666fd2` (verification basis = this worktree)
**Score:** value 9 / risk reduction 8 / effort 8 / confidence 9
**Status statement:** the direction's *formula swap* is **already implemented and unit-tested in this worktree** (see §2): `DeterministicFactID` exists, all three repository write methods assign the ID store-authoritatively, `InsertEventWithGovernance` already returns `created_at`, `factFromGap` applies the identical bucket, and `uuid.NewString` no longer appears in `facts.go` (pinned by `TestNoUUIDInFactsGo`). The **residual, still-unverified requirement is the e2e pin at the s3compat adapter** — no test in `internal/api/s3compat` exercises the wrapped repo (GAP-1) — plus the postgres migration-pair clause of AC-3 (GAP-2). This document is a **verified-current-state acceptance contract**: every direction citation is checked against HEAD, the three supplied acceptance checks are preserved 1:1 and mapped to existing/new tests. Scope beyond the three ACs is explicitly excluded (§6).

---

## 1. Evidence verification (direction citations vs. current HEAD)

| # | Direction citation (analysis-time line refs) | Current HEAD location | Verdict |
|---|---------------------------------------------|----------------------|---------|
| E1 | `internal/auditgovernance/facts.go:17,39,48` — "`uuid.NewString()` in factFromAudit/factFromEvent/factFromGap" | `factFromAudit` :11, `factFromEvent` :29, `factFromGap` :48 — **no `uuid` reference anywhere in the file**; `factFromGap` ends with `fact.ID = repository.DeterministicFactID(fact.SourceID, fact.TenantID, fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)` :70-73 | ⚠️ **STALE — already fixed.** The claim was true at analysis HEAD (`acfaaf4`, 2026-08-07); the worktree (`f666fd2 "checkpoint pre-campaign worktree"`) removed all ID minting from the constructors. Pinned by `TestNoUUIDInFactsGo` (`internal/auditgovernance/fact_id_test.go:195-205`) |
| E2 | `internal/auditgovernance/repository.go:36-45` — "fact built before origin_id exists" | `auditedRepository.InsertEvent` :36-44: `fact := r.runtime.redactor.factFromEvent(event, time.Now().UTC())` :42, then `InsertEventWithGovernance(ctx, event, fact)` :43; `RecordAudit` :22-34 same shape | ✅ **structurally true and now irrelevant** — the fact is still built before origin assignment, but the constructors no longer mint an ID; the store assigns it after `RETURNING` (E3). The direction's proposed remedy (move computation inside `InsertEventWithGovernance`) is exactly what landed |
| E3 | `internal/repository/audit_governance_write.go:70-71` — "OriginID assigned post-RETURNING" | `fact.OriginKind, fact.OriginID = AuditOriginFile, id` :79, from `RETURNING id, created_at` :67 (sqlite) / :70 (postgres `::jsonb`); `fact.OccurredAt = occurred.Time` :83; `fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(fact.TenantID), fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)` :84-85. Same pattern in `RecordAuditWithGovernance` :38-39 and `EnqueueAuditGovernance` :126-127 | ✅ **exact, and the "proposed API change" is done** — both dialect INSERTs return `id, created_at` and the ID is computed after origin + occurred are known (REQ-2 canonicalization) |
| E4 | `internal/repository/sql_events.go:10-30` — "created_at never set/returned by InsertEvent" | `InsertEvent` :9-31: both dialect INSERTs end `RETURNING id` (:16 sqlite / :20 postgres) — `created_at` is never set nor returned on the legacy path | ✅ **exact.** The legacy path is unchanged and out of scope (I5/I6: governance capture is opt-in; the wrapped `InsertEventWithGovernance` is the governance path) |
| E5 | `internal/auditgovernance/http.go` `governanceWire` — "`IdempotencyKey: fact.ID`" | `governanceWire` :140-162: `EventID: fact.ID` :148, `IdempotencyKey: fact.ID` :153 | ✅ **exact.** Byte-identical `outbox.id` ⇒ byte-identical idempotency key across prune cycles (this is the receiver-idempotency the direction protects) |
| E6 | `internal/api/s3compat/authz_gate_test.go:146` — "outboxCount counts event_outbox — no governance row assertions" | `outboxCount` :146-162 queries `event_outbox` only; harness `newAuthzServer` :69-95 builds a **raw** repo (`repository.Open` + `Migrate`), no event sink (`NewRouter(svc, nil, authz)`), no binding, no wrapped repo | ✅ **exact — this is the residual pin point (GAP-1).** Zero audit-governance references in `internal/api/s3compat` |

**Direction problem-statement check:**

| Statement | Verdict |
|---|---|
| "no enqueue — including gap-reconcile re-enqueue after prune — is receiver-idempotent" | ⚠️ was true at analysis HEAD; **now false** — all three write methods + `factFromGap` converge on `DeterministicFactID` (§2) |
| "the SHA-256(…)[:32] computation must move inside InsertEventWithGovernance (RETURNING id, created_at)" | ✅ **done** (E3) |
| "factFromGap must apply the identical bucket over gap.OccurredAt" | ✅ **done** (E1; bucket = `UTC().Truncate(time.Second)` inside `DeterministicFactID`) |
| "No s3compat test exercises the wrapped repo, so the adapter that mints real origin ids is the correct pin point" | ✅ **still true — the whole residual of this direction.** The s3compat PUT handler (`handler.go:105` → `svc.Put` → `emit` `service/file.go:308-324` → bus → wrapped `InsertEvent`) is the only place real `object_events.id` values are minted through the HTTP surface |

---

## 2. Already-implemented mechanism (the acceptance object)

All of the following is **verified present at HEAD** and is the object the new e2e must pin:

- **The formula, one definition** — `repository.DeterministicFactID(source, tenant, eventType, originKind, originID, occurredAt)` (`internal/repository/audit_governance_factid.go:27-40`): frame = `source \x00 tenant \x00 eventType \x00 originKind \x00 decimal(originID) \x00 decimal(unixSeconds(occurredBucket))` with `occurredBucket = occurredAt.UTC().Truncate(time.Second)`; ID = `hex(SHA-256(frame))[:32]` (first 32 hex chars, lowercase). Pure, clock-free. Input mapping pinned by the prior spec (`docs/requirements/internal-ai-audit-governance-deterministic-fact-ids-v1.spec.md` REQ-5): `eventType` = `fact.Action` (`"file."+event.Type` for file facts, i.e. `"file.created"` for `EventCreated`), `source` = per-tenant `tenantSourceID` (`redaction.go:43-50`), `time_bucket` = canonicalized occurred.
- **Store-authoritative assignment** in all three write methods (`audit_governance_write.go:38-39`, :84-85, :126-127) — the ID is recomputed from the fact's final fields immediately before the outbox insert, overwriting any caller-set ID.
- **Occurred canonicalization (REQ-2)** — `InsertEventWithGovernance` returns `id, created_at` (both dialects) and sets `fact.OccurredAt` from the stored value (`:83`), absorbing the DB-default precision (sqlite ms / postgres µs); `RecordAuditWithGovernance` parses the explicit `entry.CreatedAt` (:33-36).
- **Gap-path convergence** — `factFromGap` (`facts.go:48-74`) re-derives the identical raw inputs from the origin row and calls the same formula over `gap.OccurredAt` (:70-73); `EnqueueAuditGovernance` recomputes store-authoritatively (:126-127) and dedupes via `UNIQUE (origin_kind, origin_id)` + `ON CONFLICT … DO NOTHING` (:160; `migrations/{sqlite,postgres}/0039_audit_governance_outbox.up.sql:23`).
- **Receiver mapping** — `governanceWire` (`http.go:148,153`): `EventID = IdempotencyKey = fact.ID`; `receiptMatches` requires `receipt.EventID == fact.ID`.

**Existing test coverage (green, verified by running):**

| Test | Location | Pins |
|------|----------|------|
| `TestDeterministicFactID_GapEqualsAtomic_Admin` / `_File` | `internal/auditgovernance/fact_id_test.go:86-148` | factFromGap ID == atomic-capture ID (sqlite), both origins, zero-CreatedAt file event |
| `TestDeterministicFactID_PruneReenqueueSameID` | 同文件 :152-185 | claim → fail → retention-prune → gap → re-enqueue → **byte-identical ID** (sqlite store level) |
| `TestNoUUIDInFactsGo` | 同文件 :195-205 | source-read grep: `facts.go` contains no `uuid` |
| `TestDeterministicFactID_{FormatAndDeterminism,InputSensitivity,SecondBucket,EdgeInputs}` | `internal/repository/audit_governance_factid_test.go` | formula purity, input sensitivity, second-bucket boundary |
| `TestPostgresAuditGovernanceInsertEventRoundTrip` | `internal/integration/audit_governance_postgres_test.go:97-160` | PG `RETURNING id, created_at` + `::jsonb` branch, zero-CreatedAt event, 32-hex ID, occurred==origin `created_at.UnixNano()` |
| `TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery` | 同文件 :17-72 | PG gap listing + enqueue dedupe (tombstoned origin → `inserted=false`) |

---

## 3. Residual gap (what this direction still requires)

- **GAP-1 — the s3compat e2e (AC-1 + AC-2 at the adapter).** No test in `internal/api/s3compat` builds the production wiring `WrapRepository → bus.WithRepository → svc.WithEventSink(bus)` (main.go:79-84) nor asserts a single `audit_governance_outbox` row. The harness (`newAuthzServer`, E6) uses a raw repo and a noop sink, so a PUT writes `object_events`/governance rows **nowhere**. The adapter is the correct pin point: it is where the HTTP surface mints real `object_events.id` origin values.
- **GAP-2 — AC-3's postgres migration-pair clause, prune→re-enqueue identity.** The PG integration test asserts format/canonicalization/claim round-trip but **not** that a re-enqueued gap fact reproduces the byte-identical ID after the row is removed (the sqlite store-level test `PruneReenqueueSameID` has no PG mirror). `factFromGap` itself is package-private (covered on sqlite by internal tests); on PG the convergence property must be pinned at the store boundary: gap fields → `EnqueueAuditGovernance` → recomputed ID == pre-prune ID.
- **GAP-3 — F9 live-clock fallback (adjudicated: fixed, not accepted).** `listGovernanceAuditGaps` swallowed the `created_at` parse error and `factFromGap` fell back to the relay's wall clock on zero `OccurredAt` — a live-clock entry into ID math that flips IDs across second boundaries on prune→re-enqueue. Eliminated by REQ-6 (fail-loud guard); trigger (out-of-band writes only) documented there.

---

## 4. Requirements

### REQ-1 — s3compat e2e harness (production-shaped wiring)

New test file `internal/api/s3compat/audit_governance_e2e_test.go` (package `s3compat`, reusing the existing `do`/`allowAllProvider` helpers and the `outboxCount` second-connection pattern of `authz_gate_test.go`; ≤500 lines, no new dependencies). Wiring mirrors `cmd/server/main.go:79-84` + `authz_gate_test.go:69-95` + the runtime harness of `internal/auditgovernance/runtime_test.go:40-47`:

1. `repository.Open(ctx, "sqlite", file:…)` + `Migrate`; `storage.NewLocal`.
2. Construct `auditgovernance.New(cfg, store, logger)` with `cfg.Enabled=true`, loopback `httptest` receiver URL for `BaseURL`/`TokenURL` (`secureEndpoint` requires loopback-or-https, `http.go:33-44`), `HMACKey` ≥32 bytes, `Revision: 1`, `Bindings: [{TenantID: "default", ClientID: …, ClientSecretEnv: …, ClientSecret: …, State: "active"}]` — the binding is applied to the DB by `Runtime.New` (`applyDesiredBindings` :73, bound/states populated :82-88), so `Capture("default")` is true. Do **not** `Start()` the runtime: AC-1/AC-2 drive the store directly, keeping the test timing-free and deterministic.
3. `wrapped := auditgovernance.WrapRepository(repo, runtime)`; `bus := events.New(wrapped, nil)`; `svc := service.NewFileService(store, wrapped, nil).WithEventSink(bus).WithAuthorizer(allowAllProvider{})`; `httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))`.
4. `PUT /b/k.txt` (no tenant header — `mw.TenantFrom` defaults to `"default"`, `internal/middleware/middleware.go:50-56`).

Assertions (AC-1) in REQ-2, then the prune cycle (AC-2) in REQ-3. Cleanup: `runtime.Close()` + `srv.Close()` + `repo.Close()`.

### REQ-2 — AC-1: `audit_governance_outbox.id` == recomputed SHA-256

Via a second sqlite connection, read the origin + outbox row:

```sql
SELECT o.id, o.origin_id, o.action, o.tenant_id, o.occurred_at_ns,
       e.created_at, e.type
FROM audit_governance_outbox o
JOIN object_events e ON e.id = o.origin_id
WHERE o.origin_kind='file' AND o.tenant_id='default' AND e.bucket=? AND e.key=?
```

Assert, all from the DB row:

1. Exactly **1** outbox row; `o.origin_id` == the row's own `object_events.id`; `o.action == "file.created"` (`EventCreated` → `"file."+type`, `facts.go:29-43`); `e.type == "created"`.
2. **REQ-2 canonicalization:** `o.occurred_at_ns == e.created_at.UnixNano()` (the atomic path stored the DB-default `created_at`, not the constructor's `now` — PG-test precedent at `audit_governance_postgres_test.go:144-151`).
3. **The absolute formula:** recompute with the same HMAC key the test fed the runtime:
   - `expectedSource = "aero-vault." + base64.RawURLEncoding(HMAC-SHA256(key, "aero-vault/audit-governance/v1" \x00 "default" \x00 "source-system" \x00 "default" \x00))` — test-local replica of `tenantSourceID` (`redaction.go:43-50` + `writeMACFields` :74-79: fields written each followed by NUL, in order `redactionDomain, tenant, "source-system", tenant`). The e2e owns the key, so the replica is exact; it additionally pins the source-derivation framing, which no other test asserts end-to-end.
   - `expectedID = repository.DeterministicFactID(expectedSource, "default", "file.created", "file", o.origin_id, time.Unix(0, o.occurred_at_ns))`
   - assert `o.id == expectedID` **and** `o.id` matches `^[0-9a-f]{32}$`.
4. **Wire mapping:** `ClaimAuditGovernance(ctx, owner, token, …)` returns exactly this fact with `fact.ID == o.id` (ties `EventID`/`IdempotencyKey` at `http.go:148,153` to the pinned row).

### REQ-3 — AC-2: reconcile-reuse — re-enqueued row is byte-identical

Same test, continuing after REQ-2:

1. `DELETE FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?` (the direction's prune simulation; the production prune `CleanupFailedAuditGovernance`/`CleanupDeliveredAuditGovernance` leaves no origin tombstone, `audit_governance_cleanup.go` — same effect).
2. `gaps, err := store.ListAuditGovernanceGaps(ctx, "default", 10)` → exactly 1 gap; `gaps[0].OriginID == o.origin_id`; `gaps[0].OccurredAt` == the DB `created_at` (flexTime parse — the same value REQ-2 used).
3. Build the fact from the gap: `AuditGovernanceFact{SourceID: expectedSource, TenantID: "default", OriginKind: "file", FactKind: "file", Action: "file.created", OriginID: gaps[0].OriginID, OccurredAt: gaps[0].OccurredAt}` (ID may be empty — `EnqueueAuditGovernance` recomputes store-authoritatively, `write.go:126-127`). This is exactly `runtime.reconcile`'s sequence `ListAuditGovernanceGaps → factFromGap → EnqueueAuditGovernance` (`relay.go:18-49`; the three calls at :27, :38, :40) with `factFromGap`'s ID computation pinned by the AC-3 unit tests and the enqueue recomputation pinned here.
4. `inserted, err := store.EnqueueAuditGovernance(ctx, fact)` → `inserted == true`.
5. Re-read the outbox row: exactly 1 row, `id` **byte-identical** to the pre-delete ID. Optional hardening: enqueue the same fact again → `(false, nil)` via `UNIQUE(origin_kind, origin_id)` (`write.go:160`).
6. Consequence asserted by code reference: `governanceWire` maps the identical ID to an identical `IdempotencyKey` (`http.go:153`) — the receiver folds the re-delivery to Duplicate instead of double-ledgering.

### REQ-4 — AC-3 postgres clause: prune→gap→enqueue→same-ID on the PG migration pair

Extend `internal/integration/audit_governance_postgres_test.go` with `TestPostgresAuditGovernancePruneReenqueueSameID` (`//go:build integration`, `TestPostgres` prefix so it runs under `.github/workflows/integration-pg.yml`'s `-run 'TestPostgres|TestPg'` and `make test-integration`):

1. `freshRepo` + `ApplyAuditGovernanceBindings` (existing helpers), then `InsertEventWithGovernance` with a **zero-CreatedAt** event (the production shape) and a fact carrying a fixed literal `SourceID` (e.g. `"aero-vault.test-pg"`) — record the resulting outbox `id`.
2. `DELETE` the outbox row; `ListAuditGovernanceGaps` → exactly 1 gap with the recorded `OriginID` and canonical `OccurredAt`.
3. Rebuild the fact from the gap fields **with the same literal `SourceID`** and `EnqueueAuditGovernance` → the re-created row's `id` is byte-identical to the pre-delete `id`, exactly 1 row.
4. This is the store-boundary mirror of `TestDeterministicFactID_PruneReenqueueSameID` (sqlite, internal — the only place `factFromGap` itself runs); `factFromGap` is package-private and cannot run from `internal/integration`, so the PG pair asserts the identical convergence property through `EnqueueAuditGovernance`'s store-authoritative recompute (the same formula, same inputs).

### REQ-5 — AC-3 grep clause (already pinned — no new work)

`TestNoUUIDInFactsGo` (`fact_id_test.go:195-205`) already source-reads `facts.go` and fails on any `uuid` reference; the manual gate is `grep -n "uuid" internal/auditgovernance/facts.go` → no hits (the only remaining `uuid.NewString` in the package is the per-claim token at `relay.go:62`, out of scope — claim auth, never event identity).

### REQ-6 — F9 guard: no live clock can enter ID math (production change + pins)

Adjudication (a): the testing review surfaced a latent live-clock path — `listGovernanceAuditGaps` swallowed the `created_at` parse error and `factFromGap` fell back to the relay's wall clock on zero `OccurredAt`, so a prune→re-enqueue across a second boundary would mint a different ID (receiver double-ledger). Accepted-risk (option b) was rejected: a wall-clock-derived ID is unpinnable by a deterministic test (flaky by construction). Guard, fail-loud over skip-and-log (repository layer has no logger; silent skip = permanent ledger loss):

1. `sqlStore.listGovernanceAuditGaps`: parse error **or zero** (`err != nil || t.IsZero()`) → `return nil, fmt.Errorf("audit governance gap scan: invalid audit_log.created_at %q (origin %d)", occurred, gap.OriginID)` — the `IsZero()` clause also covers `"0001-01-01T00:00:00Z"`, which parses cleanly to zero.
2. `sqlStore.listGovernanceEventGaps`: `occurred.Time.IsZero()` → error (covers NULL-scan and out-of-band `''`; `flexTime.parse("")` returns nil with zero time).
3. `(*redactor).factFromGap(gap) (repository.AuditGovernanceFact, error)`: `now` parameter **removed**; zero `OccurredAt` → error; delegation passes `time.Time{}` (fallbacks unreachable after the guard). `relay.go` handles the error per-gap (warn + continue — healthy gaps of the same tenant still flow; the junk gap retries each cycle with a visible warn).
4. Capture constructors' `now` fallbacks stay (non-load-bearing — store canonicalization at `write.go:73-86`/`:83-85` overwrites `fact.OccurredAt` before the formula runs); a comment marks them as such.

Pins (NEW): sqlite — `RecordAudit` with verbatim `"garbage"`/`"0001-01-01T00:00:00Z"` CreatedAt → gap listing error; second-connection `UPDATE object_events SET created_at=''` → gap listing error; `factFromGap` unit on zero `OccurredAt` → error, no ID. PG — raw `INSERT INTO audit_log (created_at, …) VALUES ('garbage', …)` → gap listing error (file side unreachable on PG: `''` rejected by TIMESTAMPTZ, NULL blocked by NOT NULL). Existing callers `fact_id_test.go:74/:175` update mechanically (drop `time.Now()`, assert `err == nil`).

---

## 5. Acceptance checks (direction text preserved, made testable)

### AC-1 — T-4 e2e: `outbox.id` == recomputed SHA-256

> Direction text: *"s3compat e2e — PUT through the wrapped repo with active binding: audit_governance_outbox.id equals SHA-256(source|tenant|event_type|origin_kind|object_events.id|time_bucket(created_at))[:32] recomputed from the DB row (requires InsertEventWithGovernance to return created_at alongside id — proposed API change)"*

| Assertion | Coverage |
|-----------|----------|
| `InsertEventWithGovernance` returns `created_at` alongside `id` (the "proposed API change") | ✅ already implemented (`write.go:67,70,83`); PG branch covered by `TestPostgresAuditGovernanceInsertEventRoundTrip`; new e2e asserts it via `occurred_at_ns == created_at.UnixNano()` (REQ-2.2) |
| PUT through wrapped repo + active binding → exactly 1 `audit_governance_outbox` row, `origin_kind='file'`, `origin_id` = real `object_events.id` | **NEW** — REQ-1/REQ-2.1 (`internal/api/s3compat/audit_governance_e2e_test.go`) |
| `id` == `DeterministicFactID(source, tenant, "file.created", "file", object_events.id, created_at-bucket)` recomputed from the DB row, 32-hex | **NEW** — REQ-2.3 (absolute recomputation incl. the source-derivation replica) |
| Wire identity: claimed fact ID == pinned row ID | **NEW** — REQ-2.4 |

### AC-2 — T-4 reconcile-reuse: byte-identical re-enqueued ID

> Direction text: *"reconcile-reuse — delete the outbox row, run ListAuditGovernanceGaps → EnqueueAuditGovernance (runtime.reconcile, relay.go:9-44): the re-enqueued row has the byte-identical id (no duplicate via UNIQUE(origin_kind,origin_id), internal/repository/audit_governance_write.go), keeping the receiver IdempotencyKey stable across prune cycles"*

| Assertion | Coverage |
|-----------|----------|
| Row deleted → gap resurfaces with canonical `OccurredAt` | **NEW** — REQ-3.1-3.2 (e2e); sqlite store-level equivalent exists (`PruneReenqueueSameID` :152-185 via fail→prune) |
| Re-enqueued row ID byte-identical to pre-delete ID, exactly 1 row | **NEW** — REQ-3.4-3.5 (e2e); sqlite store-level equivalent exists (同 :170-185) |
| No duplicate via `UNIQUE(origin_kind, origin_id)` + `ON CONFLICT DO NOTHING` | ✅ mechanism verified (`write.go:160`, `0039:23` both dialects); optional e2e re-enqueue `(false, nil)` — REQ-3.5 |
| Receiver `IdempotencyKey` stable across prune cycles | ✅ code-reference chain `http.go:153` + REQ-3 identity; relay-delivery assertion belongs to the runtime tests (out of scope) |

### AC-3 — T-4 unit: gap-ID reproduction + uuid-free facts.go, sqlite **and** postgres

> Direction text: *"unit test asserting factFromGap(gap) reproduces the exact id factFromEvent/factFromAudit produced for the same origin within the same time bucket, on both sqlite and postgres migration pairs; grep that uuid.NewString no longer appears in facts.go"*

| Assertion | Coverage |
|-----------|----------|
| `factFromGap(gap)` reproduces the atomic-capture ID for the same origin within the same time bucket — file + admin origins, zero-CreatedAt file event | ✅ sqlite: `TestDeterministicFactID_GapEqualsAtomic_File`/`_Admin` (`fact_id_test.go:86-148`). Note the wording update: the constructors no longer produce IDs — the store does (E1/E3); the equality target is the store-assigned atomic ID, which is exactly what the existing tests assert |
| Same property on the postgres migration pair | ⚠️ partial — `TestPostgresAuditGovernanceInsertEventRoundTrip` pins format/canonicalization/claim; **NEW** `TestPostgresAuditGovernancePruneReenqueueSameID` (REQ-4) pins prune→gap→enqueue ID identity at the store boundary (`factFromGap` is package-private; its PG equivalence is enforced through `EnqueueAuditGovernance`'s recompute) |
| `uuid.NewString` no longer appears in `facts.go` | ✅ `TestNoUUIDInFactsGo` (`fact_id_test.go:195-205`) + manual gate `grep -n "uuid" internal/auditgovernance/facts.go` → no hits |

**Gate commands:**

```bash
grep -n "uuid" internal/auditgovernance/facts.go                      # no output (AC-3)
go test ./internal/auditgovernance/ -run 'DeterministicFactID|GapEqualsAtomic|PruneReenqueue|NoUUID' -count=1   # AC-3 sqlite (green today)
go test ./internal/api/s3compat/ -run 'AuditGovernance' -count=1     # AC-1 + AC-2 (NEW)
go test -tags=integration ./internal/integration/ -run 'TestPostgresAuditGovernance'   # AC-3 PG (AERO_PG_DSN; integration-pg.yml gate)
make check                                                            # gofmt/build/vet/test — must stay green; new file ≤500 lines
```

---

## 6. Scope discipline (explicit exclusions)

- **One production change, and only one:** the F9 guard (REQ-6 — three unexported functions: the two gap scans fail loud on unparseable/zero stored timestamps; `factFromGap` drops its clock parameter and errors on zero `OccurredAt`). Everything else — the formula, the write methods, `RETURNING id, created_at`, the migration set — is untouched. REQ-1…REQ-4 add test code only (`internal/api/s3compat/audit_governance_e2e_test.go`, one PG integration test). No `openapi.json`, no config, no go.mod changes.
- **Out of scope** (belong to other directions of the same analysis): the activation-gate matrix (unbound-tenant `capture=false`, receiver 200/409/422/tenant-mismatch — direction 1), terminal classification T-3, relay metrics/Ready degraded D1. The e2e deliberately does **not** start the relay goroutines and does **not** assert receiver POST counts — the DB-row identity is the T-4 pin, and `factFromGap`'s bucket logic is pinned by the deterministic unit tests (REQ-5), not by timing.
- **Deliberate test-side coupling:** the `expectedSource` HMAC replica (REQ-2.3) duplicates the framing of `tenantSourceID` by design — it is the only way an external-package test can recompute the absolute ID without widening the `auditgovernance` API surface; a drift in the source framing fails the e2e, which is the point of an end-to-end pin.
- The claim-token `uuid.NewString()` at `relay.go:62` remains (per-claim auth token, never event identity).
