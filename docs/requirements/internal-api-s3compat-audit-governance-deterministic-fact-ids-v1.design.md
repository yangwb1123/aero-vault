# Design — deterministic fact IDs (contract item 3 / B3-3 / T-4): s3compat e2e + PG re-enqueue identity pin

**Module:** `internal/api/s3compat` (pin point) · mechanism in `internal/auditgovernance` + `internal/repository`
**Companion spec:** `docs/requirements/internal-api-s3compat-audit-governance-deterministic-fact-ids-v1.spec.md`
**Date:** 2026-08-08 · **Design basis:** HEAD `15763e2` + dirty worktree (spec's cited `f666fd2` is 4 commits behind; mechanism unchanged, all gates re-verified green at the current state)
**Scope:** test code only **plus one 3-function fail-loud production guard** (F9 adjudication, §2.4/REQ-6 — the only production change; unexported, zero API/SQL/config surface, zero migrations).

---

## 0. Evidence verification ledger (untrusted claims → verdicts)

Every claim in the supplied evidence was re-checked against the current worktree, not the cited commit:

| Evidence claim | Verdict | Notes |
|---|---|---|
| `facts.go` has no `uuid`; `factFromGap` calls `DeterministicFactID` at :70-73 | ✅ exact | `facts.go:70-73`; pinned by `TestNoUUIDInFactsGo` (`fact_id_test.go:195-205`) |
| `auditgovernance/repository.go:36-45` fact built before `origin_id`; store is ID authority | ✅ exact | `InsertEvent` :36-44: fact built, then `InsertEventWithGovernance` assigns origin post-`RETURNING` |
| `audit_governance_write.go`: `RETURNING id, created_at` :67/:70, canonicalization :83, `DeterministicFactID` :84-85 | ✅ exact | Also :38-39 (audit), :126-127 (enqueue — store-authoritative overwrite) |
| `sql_events.go` legacy `InsertEvent` never returns `created_at` | ✅ exact | `RETURNING id` only (:16 sqlite / :20 postgres); legacy path out of scope |
| `http.go` `governanceWire`: `EventID: fact.ID` :148, `IdempotencyKey: fact.ID` :153 | ✅ exact | Receiver idempotency chain |
| `authz_gate_test.go:146` `outboxCount` counts `event_outbox` only; harness is raw repo, no sink | ✅ exact | `newAuthzServer` :69-95 — the residual pin point (GAP-1) |
| No `audit_governance_e2e_test.go` in `internal/api/s3compat` | ✅ exact | GAP-1 confirmed |
| PG integration lacks prune→re-enqueue identity test | ✅ exact | 3 tests exist (`ConcurrentClaimsAndLeaseRecovery`, `InsertEventRoundTrip`, `PendingIndexPlans`); none re-enqueues after row removal — GAP-2 confirmed |
| `tenantSourceID` unexported (`redaction.go:43-50`); `writeMACFields` :74-79 | ✅ exact | Test-local HMAC replica is therefore required for an external-package e2e |
| Formula: `hex(SHA-256(source\0tenant\0eventType\0originKind\0decimal(originID)\0decimal(unixSeconds(bucket))))[:32]`, bucket = `UTC().Truncate(time.Second)` | ✅ exact | `repository/audit_governance_factid.go:27-40` |
| Wiring `WrapRepository → bus.WithRepository → svc.WithEventSink(bus)` mirrors `main.go:79-84` | ✅ exact | `main.go:83-86`; `events/bus.go:67,80`; `service/file.go:138`; `s3compat.NewRouter(svc, nil, authz)` |
| `mw.TenantFrom` defaults `"default"` | ✅ exact | `middleware/middleware.go:50-56` |
| Config: `Enabled/BaseURL/TokenURL/HMACKey/Revision/Bindings`, HMAC 32..4096 bytes, loopback-or-https receiver, `Runtime.New(cfg, store, logger)` applies bindings to DB | ✅ exact | `config/config_audit_governance.go`; `runtime.go:55,73,82-88` |
| Relay reconcile = `ListAuditGovernanceGaps` :27 → `factFromGap` :38 → `EnqueueAuditGovernance` :40; last `uuid.NewString` is claim token at `relay.go:62` | ✅ exact | |
| `UNIQUE (origin_kind, origin_id)` + `ON CONFLICT DO NOTHING` | ✅ exact | `migrations/{sqlite,postgres}/0039…up.sql`; `write.go:160` |
| Gates green at `f666fd2` | ⚠️ superseded | Re-verified green at current state: `go build ./...`, `go test ./internal/auditgovernance/ -run 'DeterministicFactID|NoUUID'`, `go test ./internal/api/s3compat/` |
| Spec REQ-4 cites helper `ApplyAuditGovernanceBindings` | ⚠️ naming | Resolves to the store **method** `AuditGovernanceStore.ApplyAuditGovernanceBindings(ctx, revision, manifestDigest, bindings)` (used via `applyBindingsConcurrently` in the PG file). The new PG test calls it directly — no missing helper |

**Verdict: evidence is trustworthy; the two residual gaps (GAP-1 s3compat e2e, GAP-2 PG re-enqueue identity) are real and are exactly the remaining work. Design below is test-only plus one 3-function fail-loud production guard (F9 adjudication, §2.4) — zero API/SQL/config surface.**

---

## 1. Design summary

Three additive changes: two tests pin the already-implemented mechanism at its two unverified boundaries, plus one production guard (§2.4) eliminating the F9 live-clock fallback found in review:

0. **F9 guard (production, 3 functions):** `listGovernanceAuditGaps`/`listGovernanceEventGaps` fail loud on unparseable/zero stored `created_at` instead of swallowing the parse; `factFromGap` drops its `now` parameter and returns an error on zero `OccurredAt` — a wall clock can no longer enter ID math by construction. In-tree writers provably never produce malformed timestamps (`audit_log.created_at` TEXT NOT NULL stamped RFC3339Nano by all three writers; `object_events.created_at` NOT NULL DEFAULT in both dialects), so the guard fires only on out-of-band writes and costs nothing on the happy path.

1. **`internal/api/s3compat/audit_governance_e2e_test.go`** (new file, package `s3compat`) — production-shaped wiring, one `PUT`, then asserts `audit_governance_outbox.id == DeterministicFactID(...)` recomputed absolutely from the DB row (incl. a test-local HMAC replica of the unexported `tenantSourceID`), then deletes the row and re-enqueues via the production gap path, asserting a byte-identical ID.
2. **`TestPostgresAuditGovernancePruneReenqueueSameID`** (new func in `internal/integration/audit_governance_postgres_test.go`, `//go:build integration`) — the store-boundary mirror of the sqlite `TestDeterministicFactID_PruneReenqueueSameID`: PG outbox row → delete → gap → `EnqueueAuditGovernance` → byte-identical ID.

Both tests are deterministic (no `Runtime.Start()`, no goroutines, no receiver POSTs, no clock dependence — the formula's time input is always read back from the DB row).

---

## 2. API changes

### 2.1 Production API: none (one internal hardening guard)

No exported symbol, no handler, no SQL, no config key, no `openapi.json`, no `go.mod` change. The mechanism (formula, store-authoritative assignment, `RETURNING id, created_at`, canonicalization, gap convergence) is complete and untouched. The only production change is the F9 guard in §2.4 — unexported functions only, no interface or signature change outside package `auditgovernance`.

### 2.2 Contract surface the tests bind to (de-facto API, must remain stable)

| Symbol | Role in the pin |
|---|---|
| `repository.DeterministicFactID(source, tenant, eventType, originKind, originID, occurredAt)` | The single formula definition; recomputed in the e2e from DB row values |
| `repository.AuditGovernanceStore` — `InsertEventWithGovernance`, `ListAuditGovernanceGaps`, `EnqueueAuditGovernance`, `ClaimAuditGovernance`, `ApplyAuditGovernanceBindings` | The store boundary; e2e asserts on rows it produces; PG test drives it directly |
| `auditgovernance.New(cfg, store, logger)` / `WrapRepository` / `(*Runtime).Close` | Production wiring (`main.go:83-86`); `New` applies bindings to the DB (capture on) |
| `events.New(repo, logger)` / `(*Bus).WithRepository` | Event persistence bridge; `Bus.Publish` swallows errors by design — the e2e row assertion is the only detector |
| `service.NewFileService(...).WithEventSink(bus).WithAuthorizer(allowAllProvider{})` | PUT → `file_crud.go:255` `emit(EventCreated)` path |
| `s3compat.NewRouter(svc, nil, authz)` + `mw.TenantFrom` default `"default"` | Adapter surface; no tenant header needed (same as `newAuthzServer`) |

**Explicit non-change:** `tenantSourceID` stays unexported. The e2e replicates its framing locally (HMAC-SHA256 key over `redactionDomain\0tenant\0"source-system"\0tenant\0`, each field NUL-terminated, `SourcePrefix + "." + base64url`). This duplication is deliberate — it is the only way an external-package test can recompute the absolute ID without widening the auditgovernance API, and a drift in the framing fails the e2e (the point of an end-to-end pin).

### 2.3 Additions

| Addition | Location | Shape |
|---|---|---|
| `TestS3CompatAuditGovernanceDeterministicFactID` (AC-1 + AC-2, one test) | `internal/api/s3compat/audit_governance_e2e_test.go` | ≤500 lines/file, single func; reuses `do` (`handler_test.go:39`), `allowAllProvider` (`authz_gate_test.go:28`), `outboxCount` second-connection pattern |
| `TestPostgresAuditGovernancePruneReenqueueSameID` (AC-3 PG clause) | `internal/integration/audit_governance_postgres_test.go` | `TestPostgres` prefix (PG gate `-run 'TestPostgres|TestPg'`), reuses `freshRepo`/`pgDSN` |

### 2.4 F9 guard (production — the one non-test change, adjudicated decision (a))

Review found a latent live-clock entry into ID math: `listGovernanceAuditGaps` swallowed the `time.Parse` error (`gap.OccurredAt, _ = …` → zero), and `factFromGap`'s delegation to `factFromAudit`/`factFromEvent` fell back to the relay's live `time.Now().UTC()` on zero `OccurredAt` — a wall-clock minted ID that flips across second boundaries on prune→re-enqueue (receiver double-ledger). Decision: **eliminate the fallback with a minimal fail-loud guard** (option (b) rejected: a wall-clock ID is unpinnable by a deterministic test — flaky by construction). Fail-loud chosen over skip-and-log: the repository layer has no logger, and a silent skip is permanent ledger loss for that origin.

Exact change (3 functions + 1 call site, ~12 lines, stdlib only):

1. **`sqlStore.listGovernanceAuditGaps`** (`audit_governance_write.go`): `t, err := time.Parse(time.RFC3339Nano, occurred); if err != nil || t.IsZero() { return nil, fmt.Errorf("audit governance gap scan: invalid audit_log.created_at %q (origin %d)", occurred, gap.OriginID) }` — the `IsZero()` clause closes the parse-succeeds-but-zero edge (`"0001-01-01T00:00:00Z"`). Adds `fmt` import.
2. **`sqlStore.listGovernanceEventGaps`** (`audit_governance_write.go`): after the `flexTime` scan, `if occurred.Time.IsZero() { return nil, fmt.Errorf("audit governance gap scan: zero object_events.created_at (origin %d)", gap.OriginID) }` — covers NULL-scan and out-of-band `''`.
3. **`(*redactor).factFromGap(gap) (repository.AuditGovernanceFact, error)`** (`facts.go`): `now` parameter removed; first statement `if gap.OccurredAt.IsZero() { return fact, errors.New("audit governance gap: zero OccurredAt — refusing to derive fact ID from wall clock") }`; delegation passes `time.Time{}` as the sub-constructor `now` (guard makes the fallback unreachable; zero-time arg makes clock use structurally impossible). Adds `errors` import. Callers: `relay.go:38` (now `fact, err := …; if err != nil { warn; continue }` — per-gap, healthy gaps still flow, junk gap retries each cycle with a visible warn) and `fact_id_test.go:74/:175` (mechanical: drop `time.Now()` arg, assert `err == nil`).
4. **Capture constructors `factFromAudit`/`factFromEvent` fallbacks: kept** — verified non-load-bearing: `RecordAuditWithGovernance` (`write.go:73-86`) and `InsertEventWithGovernance` (`write.go:83-85`) overwrite `fact.OccurredAt` from the stored timestamp *before* `DeterministicFactID` runs. One comment line added noting this, so a future edit cannot "simplify" them into ID math.

**Trigger condition (documented, narrow):** out-of-band writes only — `audit_log.created_at` is TEXT NOT NULL with no DB default and all three in-tree writers stamp RFC3339Nano; `object_events.created_at` is NOT NULL DEFAULT in both dialects (sqlite `strftime('%Y-%m-%dT%H:%M:%fZ','now')` / PG `now()`) and `flexTime` accepts 4 layouts. On PG the file side cannot even be malformed (`''` rejected by TIMESTAMPTZ, NULL blocked by NOT NULL).

**I1–I6:** no SQL text changes (I1 ✓); no migrations (I2 ✓); keys/middleware untouched (I3/I4 ✓); no new flags — fires only on the already-opt-in governance path (I5 ✓); stdlib `fmt`/`errors` only, no go.mod change (I6 ✓); ≤500 lines/file unaffected (+8/+6/+3 lines).

**Rollback:** revert the three guard hunks + the two test files — no migration, no data, no config, no operator action.

---

## 3. Compatibility constraints (AGENTS.md mapping)

| # | Constraint | Application |
|---|---|---|
| I1 | SQL placeholder discipline | No new repository SQL. E2e's second-connection queries use sqlite-native `?` literals (sqlite driver, no `rebind`); PG test uses `$N` via existing store methods only |
| I2 | Migrations immutable, dual-file | No migration touched. Both dialect pairs `0039/0040/0042/0043` already exist; PG test runs on them as-is |
| I3 | Keys opaque, no reverse parsing | Outbox `id` is a TEXT PK; tests read by exact id or by origin join, never parse `@v` or decode the ID |
| I4 | Middleware chain / handler self-wiring | s3compat router unchanged; tests mount `NewRouter` bare with tenant defaulted by `mw.TenantFrom` — identical to existing `newAuthzServer` (E6), consistent with "handler doesn't self-hang the chain" |
| I5 | Opt-in safe defaults | `cfg.Enabled=true` set explicitly in the test; default-off baseline and `nil`-worker paths untouched; `Runtime` never `Start()`ed (no goroutines, no receiver traffic) |
| I6 | Stdlib only | No new dependencies; `testing` only; file ≤500 lines; single test func ≤50 lines per convention |
| — | Determinism | No `time.Now()` in expected-value computation; the formula's `occurredAt` input always comes from the stored `o.occurred_at_ns` / gap `OccurredAt`. **Structural since §2.4:** `factFromGap` has no clock parameter and errors on zero `OccurredAt`, and both gap scans fail loud on unparseable/zero stored timestamps — the second-bucket truncation cannot race a clock boundary by construction, not by review gate |
| — | Cleanup | `t.Cleanup`: `srv.Close()` → `runtime.Close()` → `repo.Close()`; `t.TempDir()` for db + objects |

---

## 4. Failure modes (each → detection → blast radius)

| # | Failure | Detection | Blast radius |
|---|---|---|---|
| F1 | `tenantSourceID`/`writeMACFields` framing drifts (domain string, NUL framing, prefix, base64 variant) | E2e recompute mismatch (`o.id != expectedID`) | Local — e2e only; exactly the drift the pin exists to catch |
| F2 | Formula changes (separators, bucket truncation, hash, hex length) | `repository/audit_governance_factid_test.go` + e2e + PG test | Broad — all three layers |
| F3 | Occurred canonicalization regresses (caller's `now` stored instead of DB `created_at`) | `o.occurred_at_ns != e.created_at.UnixNano()` (REQ-2.2; PG precedent `InsertEventRoundTrip:144-151`) | SQLite+PG write path |
| F4 | Capture inactive (binding not applied / state ≠ active / revision drift) | Zero outbox rows → "exactly 1" assertion fails; `Runtime.New` also fails fast on invalid cfg | E2e only, but mirrors production misconfig |
| F5 | Event path broken (sink nil, bus bypassed, wrapped repo not used) | No row after PUT — `Bus.Publish` swallows errors by design, so this assertion is the *only* detector | The core GAP-1 coverage |
| F6 | Prune/gap semantics drift (tombstone left by prune, gap query wrong) | Gap count ≠ 1 or `gap.OriginID != o.origin_id` after DELETE | Reconcile path |
| F7 | Dedupe regression (`UNIQUE`/`ON CONFLICT` dropped) | Re-enqueue → 2 rows; optional double-enqueue `(false, nil)` assertion | All dialects |
| F8 | PG branch divergence (`::jsonb`, `flexTime`, µs precision) | PG test fails under `AERO_PG_DSN` gate only; sqlite e2e unaffected | PG-only |
| F9 | Clock-bucket race (live clock enters expected-ID math) | Flaky boundary failure ~ once per second-window; prune→re-enqueue mints a different ID across a second boundary → receiver double-ledger | **Eliminated by guard (§2.4):** gap scans return an error on unparseable/zero stored `created_at`; `factFromGap` refuses zero `OccurredAt` (error, no clock param). Trigger requires an out-of-band write (in-tree writers provably clean); pinned by guard tests (REQ-6) |
| F10 | Wire identity drift (`ClaimAuditGovernance` ↔ `governanceWire.EventID/IdempotencyKey`) | REQ-2.4 claim-ID assertion | Receiver idempotency contract |
| F11 | File-size/format gates | `make check` (≤500 lines, gofmt) | CI |

Non-goals (deliberately not asserted): receiver POST counts, relay delivery, terminal classification, degraded-mode behavior — those belong to other directions' tests.

---

## 5. Migration steps

**Schema/data:** none. `0039_audit_governance_outbox` + `0040_control` + `0042_terminal_failed` + `0043_pending_partial_index` (sqlite **and** postgres pairs) already exist and are applied by `repo.Migrate` on startup. No `.down.sql` runs (I2).

**Codebase transition (test-only + F9 guard, two commits-worth of additions):**

1. **Add** `internal/api/s3compat/audit_governance_e2e_test.go`:
   - Wire: `repository.Open("sqlite", file:…)` + `Migrate` → `storage.NewLocal` → `auditgovernance.New(cfg, store, nil)` (Enabled, loopback `httptest` URLs, HMAC ≥32 bytes, `Revision: 1`, one active `default` binding) → `WrapRepository` → `events.New(wrapped, nil).WithRepository(wrapped)` → `svc := NewFileService(store, wrapped, nil).WithEventSink(bus).WithAuthorizer(allowAllProvider{})` → `httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))`.
   - `PUT /b/k.txt` via `do`; then via a second sqlite connection: `SELECT o.id, o.origin_id, o.action, o.tenant_id, o.occurred_at_ns, e.created_at, e.type FROM audit_governance_outbox o JOIN object_events e ON e.id=o.origin_id WHERE o.origin_kind='file' AND o.tenant_id='default' AND e.bucket='b' AND e.key='k.txt'`.
   - AC-1 asserts (exactly 1 row; `action="file.created"`; `occurred_at_ns == created_at.UnixNano()`; `expectedSource` via HMAC replica; `expectedID = DeterministicFactID(expectedSource, "default", "file.created", "file", origin_id, time.Unix(0, occurred_at_ns))`; `o.id == expectedID` + `^[0-9a-f]{32}$`; `ClaimAuditGovernance` returns `fact.ID == o.id`).
   - AC-2 continues: `DELETE` the row → `ListAuditGovernanceGaps(ctx,"default",10)` → exactly 1 gap with matching OriginID/OccurredAt → rebuild fact (ID empty — store recomputes) → `EnqueueAuditGovernance` → `inserted==true` → re-read: exactly 1 row, `id` byte-identical; optional second enqueue → `(false, nil)`.
2. **Extend** `internal/integration/audit_governance_postgres_test.go` with `TestPostgresAuditGovernancePruneReenqueueSameID`: `freshRepo` → `store.ApplyAuditGovernanceBindings(ctx, 1, manifest, []BindingState{{TenantID, Active}})` → `InsertEventWithGovernance` (zero-`CreatedAt` event, fixed literal `SourceID` `"aero-vault.test-pg"`) → record id → `DELETE` row → gap (1) → rebuild fact with same literal SourceID → `EnqueueAuditGovernance` → byte-identical id, exactly 1 row. (Store-boundary mirror of the sqlite internal test; `factFromGap` is package-private and cannot run here.)
3. **Run gates** (below). **Update** spec §5 coverage markers only if an assertion changes shape.
4. **Rollback:** revert the two test files + the three §2.4 guard hunks (`audit_governance_write.go`, `facts.go`, `relay.go`) — no migration, no data, no config, nothing else to unwind. Operators: no deployment action; `AUDIT_GOVERNANCE_*` opt-in behavior unchanged.

---

## 6. Testable acceptance mapping

| AC (direction text) | Assertion | Concrete check | Test | Gate |
|---|---|---|---|---|
| AC-1: e2e `outbox.id` == recomputed SHA-256 | `InsertEventWithGovernance` returns `created_at` | `o.occurred_at_ns == e.created_at.UnixNano()` | NEW e2e REQ-2.2; PG branch already in `InsertEventRoundTrip` | `go test ./internal/api/s3compat/ -run AuditGovernance` |
| AC-1 | PUT through wrapped repo + active binding → exactly 1 outbox row, `origin_kind='file'`, `origin_id` = real `object_events.id` | row-count + join equality | NEW e2e REQ-2.1 | same |
| AC-1 | `id == DeterministicFactID(source, tenant, "file.created", "file", id, bucket(created_at))` recomputed from DB row, 32-hex | absolute recompute incl. HMAC source replica | NEW e2e REQ-2.3 | same |
| AC-1 | wire identity: claimed fact ID == pinned row ID | `ClaimAuditGovernance` result | NEW e2e REQ-2.4 | same |
| AC-2: reconcile-reuse byte-identical ID | row deleted → gap resurfaces with canonical OccurredAt | `len(gaps)==1`, fields match | NEW e2e REQ-3.1-3.2 (sqlite store-level twin: `TestDeterministicFactID_PruneReenqueueSameID`) | same |
| AC-2 | re-enqueued row ID byte-identical, exactly 1 row | re-read after `EnqueueAuditGovernance` | NEW e2e REQ-3.4-3.5 | same |
| AC-2 | no duplicate via `UNIQUE(origin_kind,origin_id)` | optional second enqueue `(false, nil)`; mechanism verified (`write.go:160`, `0039:23`) | NEW e2e REQ-3.5 (optional) | same |
| AC-2 | receiver `IdempotencyKey` stable | code chain `http.go:153` + identity assert (relay delivery out of scope) | NEW e2e REQ-3.6 | same |
| AC-3: unit, sqlite + postgres | `factFromGap` reproduces atomic ID (file+admin, zero-CreatedAt) | `TestDeterministicFactID_GapEqualsAtomic_Admin/_File` | ✅ existing (`fact_id_test.go:86-148`) | `go test ./internal/auditgovernance/ -run 'DeterministicFactID\|GapEqualsAtomic\|PruneReenqueue\|NoUUID'` |
| AC-3 | same property on PG migration pair | prune→gap→enqueue → byte-identical id at store boundary | **NEW** `TestPostgresAuditGovernancePruneReenqueueSameID` (REQ-4) | `go test -tags=integration ./internal/integration/ -run 'TestPostgresAuditGovernance'` (needs `AERO_PG_DSN`; auto-skips otherwise) |
| AC-3 | `uuid.NewString` gone from `facts.go` | `TestNoUUIDInFactsGo` + `grep -n "uuid" internal/auditgovernance/facts.go` → no output | ✅ existing (`fact_id_test.go:195-205`) | manual grep + `make check` |
| F9 | guard: unparseable/zero stored timestamp never mints an ID | `ListAuditGovernanceGaps` returns error on `created_at='garbage'` / `''` / `'0001-01-01T00:00:00Z'`; `factFromGap(zero OccurredAt)` → error | **NEW** sqlite guard tests (admin via `RecordAudit` verbatim stamp; file via second-connection `UPDATE`; unit on `factFromGap`) + **NEW** PG sibling (`INSERT INTO audit_log … 'garbage'` → error; file side unreachable on PG — TIMESTAMPTZ rejects `''`, NOT NULL blocks NULL) | `go test ./internal/repository/ ./internal/auditgovernance/` + PG gate |
| — | whole-repo gates | gofmt/build/vet/test, ≤500 lines/file | — | `make check` |

---

## 7. Residual risks / open items

- **PG gate skip risk:** the new PG test silently skips without `AERO_PG_DSN` (integration build tag + probe); CI coverage depends on `.github/workflows/integration-pg.yml` running it (`TestPostgres` prefix) — same exposure as the existing 3 PG tests, no new risk.
- **Deliberate coupling:** the HMAC source replica duplicates framing by design (F1); if the framing is ever intentionally changed, the e2e must be updated in the same change — a feature, not a bug.
- **F9 guard (implemented, §2.4):** expected-ID math can never take a live clock value — `factFromGap` has no clock parameter and errors on zero `OccurredAt`; gap scans fail loud on unparseable/zero stored timestamps. Remaining exposure: a malformed row stalls **that tenant's** reconcile with a warn each cycle until the operator fixes the row (audit integrity over availability — deliberate; healthy gaps of the same tenant still flow since the `factFromGap` error is per-gap). In-tree writers provably never produce malformed timestamps; only out-of-band writes trigger the guard.
- **Timing:** `go test ./internal/api/s3compat/` currently ~16s; the e2e adds one PUT + a few queries — negligible. No `Runtime.Start()` keeps it deterministic and fast.
