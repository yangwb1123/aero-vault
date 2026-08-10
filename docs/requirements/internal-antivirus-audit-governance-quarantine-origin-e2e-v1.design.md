# Antivirus Quarantine-Origin Governance E2E — v1 Design

> Parent spec: `docs/requirements/internal-antivirus-audit-governance-quarantine-origin-e2e-v1.spec.md` (HEAD `15763e2`).
> Direction: **test-only** — one new file `internal/antivirus/governance_e2e_test.go` (package `antivirus`, ≤ 500-line hard gate). Zero production/schema/dependency/telemetry changes (D5).
> This design re-verifies the spec's 13-item evidence ledger against the same checkout and **corrects one blocking assertion defect (C1)** discovered during verification; every other claim is substantively exact.

---

## 1. Verification ledger (spec evidence re-checked on `15763e2`)

All line numbers below are as read on this checkout. `Verdict` is against the spec's citation; `Note` records drift (never substance-reversing).

| # | Spec claim | Verdict | Note |
|---|-----------|---------|------|
| E1 | AC-1a `:216-297`, AC-2 `:299-378`; `drainOutboxWithRelay :661-675` = events relay; grep 0 `auditgovernance` in package | ✅ | `drainOutboxWithRelay` is actually `:668-678` (`func` at `:671`) — drift only, still `events.NewEventOutboxRelay`; package has **6** .go files (5 tests + `worker.go`), grep = 0 in all — "5 files" is a miscount, substance holds |
| E2 | `worker.go` quarantine branch `:239-245` under pinned `system:antivirus` | ✅ exact | `controllerCtx` `:226-232` (`SubjectID: SystemActor`, `TenantID: obj.TenantID`, `Kind: PrincipalSystem`); tenant guard `:165-171`; `maxSignatureBytes` cap `:213-216` |
| E3 | `object_worker.go:50-88`; `SoftDeleteObjectByIDWithEvent` `:78`; `emit(EventDeleted)` `:80` | ✅ exact | `quarantineAuditEntry :94-106` (actor from principal — `system:antivirus` on this path); `quarantineReason="av_infected"` `:47-49` |
| E4 | Wrapper `repository.go:26-48` intercepts only `RecordAudit`/`InsertEvent` | ✅ exact | `WrapRepository :16-24`; `var _ :50`; `SoftDeleteObjectByIDWithEvent` inherited **unwrapped** via embedding |
| E5 | `event_outbox.go:186-233` direct-SQL tx | ✅ exact | `validateOutboxFacts` inside tx (malformed fact rolls delete back); governance-independent |
| E6 | cmd/server matrix: `putObject` only | ✅ exact | `newGovernanceE2E :182-227`, `putObject :229-237` (`svc.Put` `:231`), `startRelay :239-241`, gate `:362-408`, matrix `:413+`; zero `QuarantineObjectByID`/`ScanObjectByID` calls |
| E7 | s3compat GAP-1: "HTTP 200 + zero rows" detector | ✅ | count assertion at `:151` (spec cites `:138` — inside `governanceOutboxRow`, drift only); header `:1-33` exact |
| E8 | `bus.go:80-101` Publish swallow | ✅ exact | `"event insert failed"` warn + return on `InsertEvent` error |
| E9 | T-3 terminal semantics | ✅ | `isPermanentDeliveryError` def `:255` (spec `:87-97` = call site inside delivery loop); `failFact` `:115-132`; closed list 409/422 `:255-267`; `failed_at_ns=0` claim predicates (`audit_governance_claim.go:54,78,218`) |
| E10 | Activation gate | ✅ exact | `unboundGovernanceBacklog :107-128` scans only `audit_governance_outbox` (LEFT JOIN bindings); error type `binding.go:50-51`; `runtime.go` `New`→`applyDesiredBindings` at `:73`, message mapping `:208-227` |
| E11 | T-4 gap path | ✅ | `factFromGap` admin branch `facts.go:48-70` (`factFromAudit`, `DeterministicFactID` single call site); `listGovernanceAuditGaps` `audit_governance_write.go:235` (spec `:196-236` region) — `LEFT JOIN` + no action filter → quarantine row gap-eligible; `EnqueueAuditGovernance :111-160` + `UNIQUE(origin_kind, origin_id)` (`0039…up.sql:23`) |
| E12 | Atomic path + capture-off | ✅ | `InsertEventWithGovernance` def `:53` (spec cites `:79` = `OriginID` assignment line — drift only); `Capture` `runtime.go:140-143` |
| E13 | Package harness blocks | ✅ exact | `setupSvc :66-78`, `claimDueFacts :606-617`, `newScanPool :619-631`, `enqueueScan :632-645`, `waitForJobDone :646-660`, `EICAR` `antivirus.go:33`, `NewWorker :94-105`, `WithObjectController :102`; migrations `0003_events` + `0039_audit_governance_outbox` exist |

**Design-relevant facts confirmed beyond the ledger** (each gates a harness decision):

- **A1 — `WithEventSink` mutates in place** (`file.go:138-145`; default `noopSink{}` `:134`): the noop-sink PUT → attach-bus trick is safe and the `file.created` governance row is provably never minted. ✔
- **A2 — `cfg.Validate()` accepts `Enabled=true` with zero bindings** (`config_audit_governance.go:165-193`): the binding loop over an empty slice passes → REQ-3 capture-off runtime construction is feasible. ✔
- **A3 — failed activation gate is store-lossless**: `ApplyAuditGovernanceBindings` (`audit_governance_binding.go:18-52`) runs `replaceGovernanceBindings` **inside the tx before** `unboundGovernanceBacklog`, but the unbound error returns before `tx.Commit()` → `defer tx.Rollback()` undoes the replacement. A failed `auditgovernance.New` leaves bindings/control revision untouched. ✔
- **A4 — order of gate checks** (`binding.go:34-41`): revision < current → `ErrAuditGovernanceRevisionRollback`; revision == current ∧ digest ≠ → `ErrAuditGovernanceRevisionDrift`; only then replace + backlog. The `Revision=2` bump in REQ-3 sub-scenario 2 is therefore mandatory exactly as the spec states. ✔
- **A5 — config literal satisfies every `Validate()` bound**: HMACKey 32..4096 ∧ ≠ ClientSecret; loopback-HTTP URLs; `InitialBackoff=1 > 0`, `MaxBackoff=2 ≥ 2`, `MaxLag=60 > ClaimTTL=30`, `HTTPTimeout=5 ≤ 29`, `Poll=5 ≤ 60_000`, `ClaimTTL=30 ≤ 60`, `DeliveredRetention=3600`, `CleanupInterval=60 ≤ retention`; binding `ClientSecretEnv` matches `^AUDIT_GOVERNANCE_CLIENT_SECRET_` + env-name pattern (`:288-291`), state `active`. ✔
- **A6 — `make test-race` covers `./internal/...`** (Makefile:106-109) → `internal/antivirus` included; no Makefile change needed (unlike the cmd/server e2e, G1 in the v2 design). ✔
- **A7 — package-local `waitFor :594` / `quietLogger :585` exist and are reusable**; `quiesce` does **not** exist in the package and must be added (cmd/server precedent `governance_e2e_test.go:340-349`). ✔

---

## 2. Correction C1 (BLOCKING in spec REQ-3 as written)

**Claim:** spec §3 REQ-3 sub-scenario 2 asserts `errors.As(err, &repository.AuditGovernanceUnboundBacklogError)` on the error returned by `auditgovernance.New`.

**Finding:** `applyDesiredBindings` (`runtime.go:220-223`) returns `fmt.Errorf("audit governance unbound backlog blocks startup: refs=%s", …)` — a **non-`%w` wrap**. `errors.As` unwraps only `%w`-chained errors, so on the `New` error the typed assertion is **always false**. The typed error never escapes the runtime (it is consumed by `errors.As` inside `applyDesiredBindings` to build the message).

**Fix (test-side only; production unchanged — D5 forbids a `%w` change):**
1. **Runtime-level pin (operator-visible contract):** `err != nil && rt2 == nil`; `strings.Contains(err.Error(), "audit governance unbound backlog blocks startup")`. Pin the substring, not the full text (`refs=` are opaque per-tenant digests).
2. **Store-level typed pin (production contract):** call the store directly — `_, err := repo.ApplyAuditGovernanceBindings(ctx, 2, "any-digest", nil)` → `errors.As(err, &repository.AuditGovernanceUnboundBacklogError)` and `backlog.TenantIDs()` contains `"default"`. Precedent: `internal/repository/audit_governance_test.go:255-264` (`TestAuditGovernanceBindingDrainDeleteRestoreIsLossless`). Revision `2` (≠ current `1`) with any digest passes the drift check (A4) and reaches the backlog check deterministically.
3. **Ordering:** the store-level call is idempotent-in-tx (A3) and may run before or after `rt2.New`; both fail, neither mutates.

This preserves the direction's intent 1:1: the gate's *behavior* (startup blocked, message emitted) is pinned at runtime level; the *typed error* is pinned at the store level where it actually exists.

---

## 3. API changes

**Production API: none** (verified: zero `auditgovernance` references in `internal/antivirus` today; spec adds the first import, test-only).

**New test-surface API** (all in `internal/antivirus/governance_e2e_test.go`, package `antivirus`):

| Symbol | Kind | Purpose |
|--------|------|---------|
| `quiesce(t, d, cond)` | new helper (~10 lines) | negative-pin stability window (A6/A7; copy of `cmd/server/governance_e2e_test.go:340-349` idiom, never `waitFor` on a negative) |
| `govReceiver` (modes `202-echo`/`409`/`422`) | new helper (~60 lines) | `httptest` server; `/token` OAuth2 form (`grant_type=client_credentials`, `scope=audit:event:write`, `resource=audit-governance`; response `{"access_token":…,"token_type":"Bearer","expires_in":3600,"scope":…}` snake_case — A2-style wire trap, precedent `runtime_test.go:57`); POST handler echoes `{"receipt":{…}}` 202 or errors 409/422; `wait_for=ledgered` query pin; `sync.Mutex`-guarded `[]post{eventID, at}` + `atomic.Int64` `postCount`/`tokenCalls` (A7 -race hygiene). The cmd/server receiver is package `main` — not importable; local copy required (spec D3) |
| `govRowCount(t, db, tenant)` / `govRowByID(t, db, id)` | new helpers (~25 lines) | raw-sqlite second connection (`database/sql`, `?` placeholders only — I1); row-shape reads |
| `eventRowID(t, db, objectID)` | new helper (~8 lines) | `SELECT id FROM object_events WHERE object_id=? AND type='deleted'` (A4-precedent from the v2 design; `object_events.id` ≠ `objects.id` sequence) |
| `waitForJobDone` / `newScanPool` / `enqueueScan` / `waitFor` / `quietLogger` / `EICAR` / `SystemActor` | **reused** package-internal | unchanged; `newScanPool` already mirrors `cmd/server/workers.go` (`access.SystemContext`, `JobScan` registry) |

**Reused production API (read-only, per main.go wiring):** `repository.Open`+`Migrate` → `storage.NewLocal` → `config.AuditGovernanceConfig` literal → `auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)` → `auditgovernance.WrapRepository` → `events.New(wrepo, logger).WithRepository(wrepo)` → `service.NewFileService(store, wrepo, logger).WithEventSink(bus)` → `NewWorker(wrepo, svc.Storage(), NewSignatureScanner(nil), nil, true, logger).WithObjectController(svc)` → `jobs.NewQueue/NewPool` + `go pool.Run(ctx)`. `rt.Start(context.Background())` is called **explicitly at the assertion point**, never before (deterministic pre-start snapshot; reconcile runs only inside the run loop).

---

## 4. Compatibility constraints

1. **I1** — all raw assertions use `?` placeholders (harness connection is raw SQLite, not `rebind`).
2. **I4** — the harness reproduces the production assembly order exactly (config → wrap → bus → sink → controller → worker); `Auth ≺ Tenant ≺ RateLimit` chain irrelevant (no HTTP handlers in package tests).
3. **I5/I6** — sqlite+local FS; zero network beyond `httptest`; stdlib + existing imports only; **no new `go.mod` dependency**.
4. **D5 hard gate** — no production code, schema, migration, telemetry, env knob. C1's fix is assertion-side; a production `%w` change is out of scope.
5. **≤ 500-line hard gate** on the new file: budget ≈ 410 lines (harness 60 + receiver 60 + row helpers 35 + `quiesce` 10 + 3 tests ≈ 240 + imports). If the budget is exceeded, split helpers into `governance_e2e_helpers_test.go` (same package) rather than growing the file — both files stay under the gate.
6. **Existing tests untouched** — `antivirus_test.go` AC-1a/AC-2/AC-3a/AC-4, `hardening_test.go`, `perf_test.go`, `truncation_test.go`, `cmd/server` and `s3compat` e2e files are read-only inputs. The new tests must not disturb the package's shared state (each test builds its own repo/store; no globals).
7. **WAL concurrency** — `repository.Open` uses WAL + `MaxOpenConns(1)`; the raw assertion connection is a WAL **reader** → never `SQLITE_BUSY`; all writes are single-goroutine until the first `waitFor`.
8. **`t.Cleanup` LIFO** (F1 precedent from the v2 design): register `receiver.Close()` **first**, `poolCancel` second, `repo.Close()` third, `rt.Close()` **last** → execution order `rt.Close → repo.Close → poolCancel → receiver.Close` (Close bound `claimTTL+httpTimeout`; use `context.Background()` for `rt.Start`, not `t.Context()`).
9. **Claim race tolerance** — admin-row identity is pinned before terminal state; `attempts`/`delivered_at_ns` are only ever pinned via `waitFor` on terminal predicates, never at count-observation time (claim races enqueue; terminal state is the race-free pin).
10. **Token-cache coupling** — `tokenCalls==1` with 2 POSTs depends on `tokenSource` caching (`expires_in` 3600 > test duration, `token.go:47-60`). Pin `postCount==2` first, `tokenCalls` second.

---

## 5. Failure modes

| # | Mode | Detection / mitigation |
|---|------|------------------------|
| F1 | **C1 regression in test** (errors.As on `New` error) | avoided by design — runtime message pin + store-level typed pin (C1) |
| F2 | Broken wiring (unwrapped bus repo, missing `WithObjectController`/`WithEventSink`, quarantine emit removed) → job succeeds + 0 rows, today silent | `COUNT==2` + `postCount==2` pair is the loud detector (direction's F5, promoted to the quarantine emitter) |
| F3 | `file.created` pollution minting a 3rd governance row | noop-sink PUT + attach bus after PUT (A1); pre-start `COUNT==1` (only `file.deleted`) is itself a pollution detector |
| F4 | Claim race on admin row between COUNT and SELECT | identity-fields-only pin + terminal-state `waitFor` (constraint 9) |
| F5 | Relay-start race (reconcile tick vs. snapshot) | relay deliberately unstarted; pre-start snapshot taken before `rt.Start`; `COUNT==2` via `waitFor` (never fixed sleep) |
| F6 | Retry storm on transient paths | 409/422 are terminal (E9 closed list); `quiesce(50ms, postCount==2)` proves no re-POST; T-3 exclusion via `ClaimAuditGovernance`→0 rows + `OldestPendingAuditGovernance`→`ok==false` |
| F7 | Token-cache removal in production | `postCount==2` remains the primary pin; `tokenCalls==1` degrades to a warning-level secondary pin (spec §6) |
| F8 | Store mutation by failed `rt2.New` | impossible — tx rollback (A3); optional bonus pin: a subsequent `auditgovernance.New(original cfg)` succeeds, proving revision-1 state intact |
| F9 | SQLite locking / placeholder binding | WAL reader (constraint 7); `?`-only discipline (I1) |
| F10 | 500-line gate | helper budget §4.5; split-file fallback |
| F11 | Wire-format drift (token response field names, `wait_for` param, receipt envelope) | receiver mirrors the v2 design's hardened wire shapes (snake_case token JSON, `wait_for=ledgered`, `{"receipt":{…}}` 202 envelope); POST body `event_id` cross-checked against outbox `id` |

---

## 6. Migration steps

No schema/dependency migration (test-only). Adoption sequence:

1. **Spec** — already delivered (121 lines, `internal-antivirus-audit-governance-quarantine-origin-e2e-v1.spec.md`). Design supersedes it only at assertion C1.
2. **Implement** `internal/antivirus/governance_e2e_test.go` (package `antivirus`) per §3: harness → `quiesce`/receiver/row helpers → REQ-1 → REQ-2 → REQ-3 (C1-corrected). Reuse `waitForJobDone`/`newScanPool`/`enqueueScan`/`waitFor`/`quietLogger`/`EICAR`.
3. **Local gates:** `gofmt -l` clean; `go build ./...`; `go vet ./...`; `go test ./internal/antivirus/ -count=1`; new file ≤ 500 lines.
4. **Full gates:** `make check` (SQLite+local FS, zero network beyond `httptest`); `make test-race` (covers `./internal/...` incl. this package — A6).
5. **Docs:** no production docs change. If the repo convention requires, add a CHANGELOG line under the campaign run; do not touch `docs/configuration.md`/`docs/api.md` (no env/API surface).
6. **Out of scope (future directions):** direction 2 (gap-capture staleness gauge) and direction 3 (T-4 fact-ID recomputation pin for the quarantine shape) — both would be separate specs; direction 3 would re-use the `eventRowID` helper and the wire-derived `source_system` recomputation idiom from the v2 design.

---

## 7. Testable acceptance mapping

| Direction check | Spec REQ | Test | Concrete assertions (all `waitFor`/`quiesce`-pinned, race-free) |
|---|---|---|---|
| **AC-1** — EICAR quarantine ⇒ exactly 2 governance rows (`file.deleted` atomic + `file.delete` gap), both terminal-delivered, `Attempts==1` | **REQ-1** `TestQuarantineGovernanceE2EDualPathDelivered` (receiver `202-echo`) | steps 1-5 | (1) `waitForJobDone(t, repo, obj.ID, 1).Attempts == 1`, `JobSucceeded`; `GetObjectByID.DeletedAt != nil`; `audit_log` = 1 row (`actor == SystemActor`, `action == file.delete`, `target == default/eicar.txt`, detail ⊇ `av_infected`); `event_outbox` = 2 due facts (AC-1a pins re-asserted under governance). (2) pre-start: `govRowCount == 1`, `origin_kind='file'`, `action='file.deleted'`, `fact_kind='file'`, `origin_id == eventRowID(t, db, obj.ID)`, `attempts==0`, `delivered_at_ns==0`, `failed_at_ns==0`, `available_at_ns>0`. (3) after `rt.Start`: `waitFor(govRowCount == 2)`; second row identity `origin_kind='admin'`, `action='file.delete'`, `fact_kind='admin'`, `origin_id == (SELECT id FROM audit_log WHERE action='file.delete' AND detail='av_infected' AND tenant_id='default')`. (4) `waitFor` both rows `delivered_at_ns>0 ∧ failed_at_ns==0 ∧ attempts==1 ∧ claim_owner=='' ∧ last_error==''`. (5) `quiesce(50ms, postCount==2)`; each POST `event_id` ∈ outbox `id`s; `tokenCalls == 1` |
| **AC-2** — 409/422 ⇒ both facts terminal-dead ≤1 attempt, T-3-excluded, quarantine still commits | **REQ-2** `TestQuarantineGovernanceE2EPermanentDeliveryTerminal` (subtests `409`/`422`) | steps 1-4 | (1) same as REQ-1.1 (quarantine commits — relay failure never touches job/audit/events). (2) `waitFor` each row `failed_at_ns>0 ∧ delivered_at_ns==0 ∧ attempts==1 ∧ last_error ⊇ "audit governance HTTP 409"` (resp. `422` — the M2/M3 sentinel text). (3) `quiesce(50ms, postCount==2)` — exactly one POST per fact, no re-claim. (4) post-death: `ClaimAuditGovernance(ctx, owner, token, cfg.Revision, 10, time.Minute)` → 0 rows; `OldestPendingAuditGovernance(ctx)` → `ok==false`; rows retained with `last_error` |
| **AC-3** — binding absent: quarantine commits, zero rows, gate blocks startup with unbound-backlog error | **REQ-3** `TestQuarantineGovernanceE2ECaptureOffAndActivationGate` | sub-1 (capture-off): `cfg.Bindings = nil`, `Enabled` true | quarantine commits (job `Attempts==1`, soft-delete, 1 audit row, 2 events-outbox facts); `govRowCount == 0`; `rt.Start` + `quiesce(300ms, postCount==0 && tokenCalls==0)` — unbound quarantine is correctly unmonitored, never queued |
| | | sub-2 (activation gate): REQ-1 harness (relay unstarted → 1 pending row), `cfg2 := cfg; cfg2.Bindings = nil; cfg2.Revision = 2` | `rt2, err := auditgovernance.New(cfg2, store, logger)` → `err != nil && rt2 == nil`; `strings.Contains(err.Error(), "audit governance unbound backlog blocks startup")`; **store-level (C1-corrected):** `_, err = store.ApplyAuditGovernanceBindings(ctx, 2, "any-digest", nil)` → `errors.As(err, &repository.AuditGovernanceUnboundBacklogError)` ∧ `TenantIDs()` ⊇ `"default"`; optional bonus (F8): `auditgovernance.New(cfg, …)` (revision 1, binding present) succeeds — failed gate left the store lossless |

*Mutation guide:* deleting `WithObjectController`, unwrapping the bus repo, removing `WithEventSink`, deleting the quarantine `emit`, or switching the receiver off `202-echo`/`409`/`422` each fails exactly one of the cells above — no cell passes vacuously, and negatives are `quiesce`-pinned (never `waitFor`-true-at-t=0).

---

## 8. Risks

- **Timing flake** — mitigated by the proven envelope (5 ms poll, `waitFor` deadlines with predicate dumps, `quiesce` negatives, counter/`>`-only assertions); identical to passing `TestGovernanceE2EActivationGateBoundTenant`/M1-M6.
- **Admin-row birth ordering** — reconcile runs only inside the run loop; pre-start snapshot deterministic; `COUNT==2` via `waitFor` only.
- **C1 exposure** — the spec's `errors.As` sentence is corrected here; a reviewer must re-derive from `runtime.go:220-223` (non-`%w` `fmt.Errorf`). Documented in §2.
- **Line-count pressure** — ~410-line budget; split-file fallback defined (constraint 5).
- **Hard gates** — `make check` + `make test-race` apply unchanged (A6); I1 discipline; no new deps (I6).

*Verification basis: every ledger row re-read on this checkout (`15763e2`); `go build ./...`, `go vet`, `gofmt -l`, and `go test ./...` all green at design time.*
