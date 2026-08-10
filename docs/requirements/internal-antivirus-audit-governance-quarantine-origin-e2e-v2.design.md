# Antivirus Quarantine-Origin Governance E2E — v2 Design (amended, re-verified on live tree)

> Supersedes `internal-antivirus-audit-governance-quarantine-origin-e2e-v1.design.md` (all v1 content remains valid except where amended below).
> Parent spec: `internal-antivirus-audit-governance-quarantine-origin-e2e-v1.spec.md` (121 lines, unchanged).
> Direction: **test-only** — one new file `internal/antivirus/governance_e2e_test.go` (package `antivirus`, ≤ 500-line hard gate). Zero production/schema/dependency/telemetry changes (D5).
>
> **Verification basis of THIS version:** the **live working tree** — HEAD `15763e2` + uncommitted FR-1/2/3/4-era drift (the v1 ledger was pinned to HEAD). Re-verified 2026-08-08: `go build ./...` green, `go vet ./internal/antivirus ./internal/auditgovernance ./internal/config` clean, `go test ./internal/antivirus/ -count=1` green (10.1 s), plus a throwaway compile+run check of the §3 harness wiring (deleted after verification).
> **Amendments vs v1** (all test-side, D5-compliant): C1 pin pair re-confirmed on byte-identical `runtime.go`; §3 two-phase sink attach + govReceiver `"default"` echo + `eventRowID` ORDER BY + `object_events` presence pin; §4.7 WAL wording; §4.8 `poolCancel` reorder; §4.9 `ClaimTTLSeconds=55` budget; §5 mutation-guide truth + F8 mandatory; §8 `waitForRow` dumping helper.

---

## 1. Verification ledger (re-checked on the LIVE tree)

All line numbers below are as read on the live tree; `v1 pin` is what v1 (pinned to HEAD `15763e2`) recorded. Files **byte-identical to HEAD** are marked ★ — their v1 pins are untouched.

| # | Claim | v1 pin (HEAD) | v2 re-pin (live tree) | Verdict |
|---|-------|---------------|----------------------|---------|
| E1 | AC-1a `:216-297`, AC-2 `:299-378`; `drainOutboxWithRelay` = events relay; grep 0 `auditgovernance`; "5 files" | `:216-297`, `:299-378`; `drainOutboxWithRelay :668-678`; 5 files | `TestScanObjectByIDQuarantineWritesAuditAndOutbox` `:216-298`; `TestQuarantineJobCompletesWithoutRelayThenRelayDrainsDisjoint` `:299-391` (FR-2/3 tests grew the file by 480 lines); `drainOutboxWithRelay :671-681` (func `:671`); still 6 .go files (5 tests + `worker.go`), grep 0 in all | ✅ substance exact; spans re-pinned |
| E2 | `worker.go` quarantine branch under `system:antivirus`; controllerCtx; tenant guard; cap | branch `:239-245`; controllerCtx `:226-232`; guard `:165-171`; cap `:213-216` | branch `:238-241` (`if w.quarantine` `:238`, `QuarantineObjectByID(controllerCtx, objectID, signature)` `:239`); controllerCtx `:227-233`; tenant guard `:167-170`; `maxSignatureBytes` cap `:213-216`; **new**: `SystemActor` const `:37` (= `access.SystemActorAntivirus`, FR-4), nil-guard `:155-157` (`"antivirus: object controller is required"`), `QuarantineObjectByID(ctx, objectID, signature)` 3-arg interface `:57` | ✅ re-pinned (2-arg→3-arg signature is FR-2/3 drift) |
| E3 | `object_worker.go:50-88`; `SoftDeleteObjectByIDWithEvent :78`; `emit :80` | `:50-88`, `:78`, `:80` | `QuarantineObjectByID :50-89` (3-arg `signature` param); `SoftDeleteObjectByIDWithEvent` call `:78` (**exact**); `s.emit(ctx, obj, EventDeleted)` `:85` (was `:80`); `quarantineReason = "av_infected"` `:41`; `quarantineAuditEntry :94-106` (actor from principal); `quarantineFacts :112-132` (deleted@1.1 + notify@1.1, FR-2/3) | ✅ re-pinned; emit still fires ⇒ `file.deleted` governance minting intact |
| E4 | Wrapper `repository.go:26-48` intercepts only `RecordAudit`/`InsertEvent` | ★ byte-identical | `WrapRepository :16-24`; `RecordAudit :26`; `InsertEvent :45`; `var _ :56`; `SoftDeleteObjectByIDWithEvent` inherited unwrapped | ✅ exact |
| E5 | `event_outbox.go:186-233` direct-SQL tx | `:186-233` | `SoftDeleteObjectByIDWithEvent :186-227` (tx opens `:187`); `validateOutboxFacts` inside tx (malformed fact rolls delete back); `insertOutboxFacts :229`; governance-independent | ✅ re-pinned |
| E6 | cmd/server matrix: `putObject` only | `:182-227`, `:229-237`, `:239-241`, gate `:362-408`, matrix `:413+` | **All exact on the live tree** (file is untracked campaign-era, 489 lines): `newGovernanceE2E :182-227`, `putObject :229-237`, `startRelay :239-241`, gate `:362-408`, matrix `:413+`; grep `QuarantineObjectByID\|ScanObjectByID` = 0 | ✅ exact |
| E7 | s3compat count assertion | `:138` (v1 already drifted to `:151`) | `:150` (`if !found || count != 1`; `governanceOutboxRow :113`) | ✅ re-pinned |
| E8 | `bus.go:80-101` Publish swallow | ★ byte-identical | `Publish :80-101`; `repo.InsertEvent :84`; `"event insert failed"` warn + return `:86` | ✅ exact |
| E9 | T-3 terminal semantics | `isPermanentDeliveryError :255`; `failFact :115-132`; claim predicates `:54,78,218` | `isPermanentDeliveryError :255-262` (unchanged in the dirty `relay.go`; call site `:87`; closed list `ErrReceiptConflict`/`ErrInvalidReceipt` + 409/422 `httpStatusError`); `model.go:36` `fmt.Sprintf("audit governance HTTP %d", …)` (was `:35`); **new** `FailAuditGovernance :182-195` (FR-era) + `requireGovernanceClaim :197`; claim predicates `delivered_at_ns=0 ∧ failed_at_ns=0` at `:54` (bindings JOIN) / `:78` (due) / `:191` (Fail) — all fenced `owner+token+live lease` | ✅ re-pinned |
| E10 | Activation gate | `runtime.go:73,208-227` | ★ `runtime.go` byte-identical; `New :55-77` (`err = applyDesiredBindings` `:73`, `return nil, err` `:76`); `applyDesiredBindings :208-227`; non-`%w` `fmt.Errorf` `:224` | ✅ exact |
| E11 | T-4 gap path | `facts.go:10-22,71-78`; `listGovernanceAuditGaps` `:235`; `EnqueueAuditGovernance :111-160`; `UNIQUE(origin_kind, origin_id)` `0039…up.sql:23` | `factFromAudit :10-22` (OriginKind `AuditOriginAdmin`); `factFromGap :48-70`; `auditFactKind :82-87` → `"admin"`; `factFromEvent :24-45` (`"file."+event.Type`); `listGovernanceAuditGaps :235` with `LEFT JOIN` no action filter `:239-241`; `EnqueueAuditGovernance :111-160`; migration `0039…up.sql:23` | ✅ exact |
| E12 | Atomic path + capture-off | `InsertEventWithGovernance :53`; `Capture` | `InsertEventWithGovernance :53-97` (file is dirty, but only the FR-2/3 deterministic-fact-ID additions changed it: `RETURNING id, created_at` + `DeterministicFactID` at `:75-86`; the cited shape is unchanged): `object_events` INSERT `:66-75`, `fact.OriginKind, fact.OriginID = AuditOriginFile, id :79-80`, `governanceCaptureActive :139-149` (no binding row → false) | ✅ re-pinned |
| E13 | Package harness blocks | `setupSvc :66-78`; `claimDueFacts :606-617`; `newScanPool :619-631`; `enqueueScan :632-645`; `waitForJobDone :646-660`; `EICAR` `antivirus.go:33`; `NewWorker :94-105`; `WithObjectController :102` | **All exact on the live tree** (helpers untouched by the 480-line FR-2/3 test additions): `setupSvc :66-79`, `quietLogger :585`, `waitFor :594-604`, `claimDueFacts :606-617`, `newScanPool :619-631` (registers `ScanObjectByID` under `access.SystemContext`), `enqueueScan :632-645` (TenantID `"default"`), `waitForJobDone :646-660` (8 s deadline), `EICAR :33`, `NewWorker :94-101`, `WithObjectController :102-105` | ✅ exact |

**Design-relevant facts (A1–A7), re-verified on the live tree:**

- **A1 — `WithEventSink` mutates in place** (`file.go:138-140`; default `noopSink{}` `:134`; `noopSink` `:67-69`): the noop-sink PUT → attach-bus trick is safe and the `file.created` governance row is provably never minted. ✔
- **A2 — `cfg.Validate()` accepts `Enabled=true` with zero bindings** (`internal/config/config_audit_governance.go:165-193` — note the path, it is in `internal/config`, not `internal/auditgovernance`): `validateAuditGovernanceBindings` over an empty slice passes. ✔
- **A3 — failed activation gate is store-lossless** (`audit_governance_binding.go:18-52` ★ byte-identical): `replaceGovernanceBindings` inside the tx, unbound error returns `:49-51` before `updateGovernanceControl`/`tx.Commit()`, `defer tx.Rollback()` undoes it. ✔
- **A4 — order of gate checks** (`binding.go:34-41`): revision rollback → drift → replace + backlog. `Revision=2 > current 1` with any digest passes the drift check; `maxAuditGovernanceRevision = 1<<63-1` (`:16`). ✔
- **A5 — config literal with `ClaimTTLSeconds=55` satisfies every `Validate()` bound** (re-verified against the tightened live validation, `config_audit_governance.go`): HMACKey 32..4096 ∧ ≠ ClientSecret; loopback-HTTP URLs; `InitialBackoff=1 > 0`; **`MaxBackoff=2 ≥ 2` — exactly at the new floor added by the FR-era drift** (`validAuditGovernanceRetry`, diff adds `MaxBackoffSeconds >= 2`, and `2 ≥ InitialBackoff=1`); `MaxLag=60 > ClaimTTL=55`; `ClaimTTL=55 > 2*HTTPTimeout=10` **and** `≤ 60` (the two new budget bounds); `HTTPTimeout=5 ≤ 29`; `Poll=5 ≤ 60_000`; `DeliveredRetention=3600`; `CleanupInterval=60 ≤ retention`; `ReconcileBatchSize=8 ≥ 2`; binding `ClientSecretEnv` matches `^AUDIT_GOVERNANCE_CLIENT_SECRET_` + env-name pattern (`:297-298`), state `active`. **Runtime-verified**: the throwaway wiring check ran `cfg.Validate()` with `ClaimTTLSeconds=55` and `auditgovernance.New` with this literal — both passed. ✔
- **A6 — `make test-race` covers `./internal/...`** (Makefile:106-109) → `internal/antivirus` included; no Makefile change needed. ✔
- **A7 — package-local `waitFor :594` / `quietLogger :585` exist and are reusable**; `quiesce` does **not** exist in the package and must be added (cmd/server precedent `governance_e2e_test.go:340-349` — exact on live tree). ✔

**C1-critical drift audit:** `internal/auditgovernance/runtime.go`, `repository.go`, `token.go`, `types.go` are all **byte-identical to HEAD** (`git diff 15763e2` empty — not in the dirty set). `internal/repository/audit_governance_binding.go` is byte-identical too. The one C1-adjacent dirty file, `internal/repository/audit_governance_types.go`, changed only `AuditGovernanceFact` (`SourceID`/`FirstAttemptAt` fields) and the `AuditGovernanceStore` interface (`FailAuditGovernance`); `AuditGovernanceUnboundBacklogError` (`types.go:20-33`) and the `TenantIDs()` defensive copy are **unchanged**. `config_audit_governance.go` differs only by the `validAuditGovernanceRetry` `MaxBackoffSeconds >= 2` floor (A5). **The C1 finding and fix are fully valid on the live tree.**

---

## 2. Correction C1 (unchanged from v1 — re-confirmed)

**Finding:** `applyDesiredBindings` (`runtime.go:208-227`) returns `fmt.Errorf("audit governance unbound backlog blocks startup: refs=%s", …)` at `:224` — a **non-`%w` wrap**; `errors.As` on the `New` error is always false. `runtime.go` is **byte-identical to HEAD** (`git diff 15763e2 -- internal/auditgovernance/runtime.go` empty), so the v1 finding survives the drift. Sole-emitter re-grep: `"unbound backlog blocks startup"` appears only at `runtime.go:224` in non-test code.

**Fix (test-side only — D5):**
1. **Runtime-level pin (operator-visible contract):** `err != nil && rt2 == nil`; `strings.Contains(err.Error(), "audit governance unbound backlog blocks startup")`.
2. **Store-level typed pin (production contract):** `_, err := repo.ApplyAuditGovernanceBindings(ctx, 2, "any-digest", nil)` → `errors.As(err, &repository.AuditGovernanceUnboundBacklogError)` ∧ `backlog.TenantIDs()` ⊇ `"default"`. Precedent re-pinned on the live tree: `TestAuditGovernanceBindingDrainDeleteRestoreIsLossless` `internal/repository/audit_governance_test.go:245`, `errors.As` idiom `:264-265` (was `:255-264` at HEAD — the +229-line dirty diff moved it). Revision `2` (≠ current `1`) with any digest passes the drift check (A4); `nil` bindings pass `validGovernanceBindingStates`; the harness's single pending `"default"` row makes `TenantIDs() ⊇ "default"` deterministic.
3. **Ordering:** store-level call is idempotent-in-tx (A3) — may run before or after `rt2.New`; both fail, neither mutates. Digest asymmetry (`desiredDigest(cfg2)` vs literal `"any-digest"`) makes the pair safe **regardless of call ordering** (F8): whichever call commits first, the second trips `ErrAuditGovernanceRevisionDrift`.
4. **F8 bonus pin is MANDATORY** (v2 upgrade, closes the over-blocking residual cell-locally): after the failed `rt2.New`, `auditgovernance.New(cfg, …)` (revision 1, binding present) must **succeed** — proving the failed gate left the store lossless and that a hardcoded-message mutation cannot satisfy sub-2 alone. Keep `"any-digest"` literal exactly as written; replacing it with the real digest would reopen the commit-ordering hole.

---

## 3. API changes

**Production API: none** (verified: zero `auditgovernance` references in `internal/antivirus` today; spec adds the first import, test-only).

**New test-surface API** (all in `internal/antivirus/governance_e2e_test.go`, package `antivirus`):

| Symbol | Kind | Purpose |
|--------|------|---------|
| `quiesce(t, d, cond)` | new helper (~10 lines) | negative-pin stability window (copy of `cmd/server/governance_e2e_test.go:340-349` idiom, never `waitFor` on a negative) |
| `govReceiver` (modes `202-echo`/`409`/`422`) | new helper (~60 lines) | `httptest` server; `/token` OAuth2 form (`grant_type=client_credentials`, `scope=audit:event:write`, `resource=audit-governance`; snake_case `{"access_token":…,"token_type":"Bearer","expires_in":3600,"scope":…}`); POST handler echoes `{"receipt":{…}}` 202 or errors 409/422; `wait_for=ledgered` query pin; `sync.Mutex`-guarded `[]post` + `atomic.Int64` `postCount`/`tokenCalls`. **V2: the 202 receipt must echo `tenant_id:"default"` — this DIFFERS from the cmd/server precedent, which echoes `e2eTenant="acme"` (`governance_e2e_test.go:134-141`); `receiptMatches` requires `receipt.TenantID == fact.TenantID` (`http.go:214-221`), so a blind copy-paste makes both rows terminal-fail (`ErrInvalidReceipt`).** Append-under-mutex-then-`postCount.Add` ordering (precedent `:127-133`) preserved — required for the happens-before of `waitFor(postCount==2)` → `posts[]` reads |
| `govRowCount(t, db, tenant)` / `govRowByID(t, db, id)` | new helpers (~25 lines) | raw-sqlite second connection (`database/sql`, `?` placeholders only — I1); row-shape reads |
| `eventRowID(t, db, objectID)` | new helper (~8 lines) | `SELECT id FROM object_events WHERE object_id=? AND type='deleted' ORDER BY id DESC LIMIT 1` — **v2 adds the `ORDER BY id DESC LIMIT 1`** (precedent `governance_e2e_test.go:248-262` has it; cheap robustness if step 1 ever gains a second event). `object_events.id` ≠ `objects.id` sequence |
| `waitForRow(t, db, originID, pred)` | **new v2 helper** (~15 lines) | `waitFor`-shaped polling that captures the last observed row and **dumps it on timeout** — the package-local `waitFor :594-604` fails with a bare `"condition not met within 8s"` and no row dump (N1). This is the single highest-value deterministic-debugging addition; `quiesce` stays dump-free |
| `waitForJobDone` / `newScanPool` / `enqueueScan` / `waitFor` / `quietLogger` / `EICAR` / `SystemActor` | **reused** package-internal | unchanged; `newScanPool :619-631` already mirrors `cmd/server/workers.go` (`access.SystemContext`, `JobScan` registry) |

**Reused production API (read-only, per main.go wiring):** `repository.Open`+`Migrate` → `storage.NewLocal` → `config.AuditGovernanceConfig` literal (A5, `ClaimTTLSeconds: 55`) → `auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)` → `auditgovernance.WrapRepository` → `events.New(wrepo, logger).WithRepository(wrepo)` → `service.NewFileService(store, wrepo, logger)` → `NewWorker(wrepo, svc.Storage(), NewSignatureScanner(nil), nil, true, logger).WithObjectController(svc)` → `jobs.NewQueue/NewPool` + `go pool.Run(ctx)`.

**V2 — two-phase sink attach (N2):** the wiring bullet in v1 listed `.WithEventSink(bus)` inside the construction chain, contradicting A1/F3. The amended sequence is explicit:
- **Phase 1:** `svc := service.NewFileService(store, wrepo, logger)` (default `noopSink{}`) → `svc.Put(...)` the EICAR object → pre-start `COUNT==1` pin (only `file.deleted`; no `file.created` pollution — with the bus attached during the PUT, the created event would mint a 3rd governance row and fail the pin).
- **Phase 2:** `svc.WithEventSink(bus)` (in-place mutation, `file.go:138-140`) → `enqueueScan` → job runs. Add a code comment on the attach line: `// MUST be after the PUT — see F3`.
- `WithDeleteFailOpen` (`file.go:114-118`, FR-1): **not required** by this harness — the quarantine controller ctx pins `Kind: PrincipalSystem` + `SubjectID: SystemActor` (= `access.SystemActorAntivirus`), which is `access.IsSystemDeleteExempt` (`permissions.go:29-33`, exact-tenant match), so the fail-closed delete gate (`service/access.go:93-100`) passes without an opt-out. A `WithDeleteFailOpen(true)` call would also compile if the harness ever needs the legacy baseline; do not use it here — it would mask a future exemption regression.
- `rt.Start(context.Background())` is called **explicitly at the assertion point**, never before (deterministic pre-start snapshot; reconcile runs only inside the run loop). `poolCancel` registration: see constraint 8.
- **Verified compiles+runs**: the throwaway wiring check (3-arg `QuarantineObjectByID` interface satisfied by `FileService.QuarantineObjectByID :50`, two-phase sink, `ClaimTTLSeconds=55` through `Validate()` and `auditgovernance.New`, `newScanPool`/`pool.Run`) passed on the live tree and was deleted.

---

## 4. Compatibility constraints

1. **I1** — all raw assertions use `?` placeholders (harness connection is raw SQLite, not `rebind`).
2. **I4** — the harness reproduces the production assembly order exactly (config → wrap → bus → sink → controller → worker); `Auth ≺ Tenant ≺ RateLimit` chain irrelevant (no HTTP handlers in package tests).
3. **I5/I6** — sqlite+local FS; zero network beyond `httptest`; stdlib + existing imports only; **no new `go.mod` dependency**.
4. **D5 hard gate** — no production code, schema, migration, telemetry, env knob. C1's fix is assertion-side; a production `%w` change is out of scope.
5. **≤ 500-line hard gate** on the new file: budget ≈ 430 lines (harness 60 + receiver 60 + row helpers 45 incl. `waitForRow` + `quiesce` 10 + 3 tests ≈ 240 + imports). If exceeded, split helpers into `governance_e2e_helpers_test.go` (same package) — both files stay under the gate.
6. **Existing tests untouched** — `antivirus_test.go` AC-1a/AC-2/AC-3a/AC-4 (`:216-298`, `:299-391`, …), `hardening_test.go`, `perf_test.go`, `truncation_test.go`, `cmd/server` and `s3compat` e2e files are read-only inputs. The new tests must not disturb the package's shared state (each test builds its own repo/store; no globals).
7. **WAL concurrency (v2 wording fix)** — `repository.Open` (`sqlite.go:26-31`) sets **`MaxOpenConns(1)` + `PRAGMA journal_mode=WAL`**. The real invariant is **"all writes — pool worker, relay run loop, per-fact `deliverFact` goroutines — serialize on the single connection; the raw reader never writes"** (v1's "all writes are single-goroutine until the first `waitFor`" was inaccurate on both sides of that boundary). The raw helpers are **SELECT-only** — a `PRAGMA journal_mode` on the reader would need the exclusive lock; the design's helpers never do that. No `t.Parallel` exists in `internal/antivirus`; each test owns a `t.TempDir()` DB.
8. **`t.Cleanup` LIFO (v2 reorder)** — register **`poolCancel` first** (i.e., registration order `poolCancel → receiver.Close → repo.Close → rt.Close`) ⇒ execution `poolCancel → rt.Close → repo.Close → receiver.Close`: the pool stops before the repo closes (v1 registered the pool after the repo — benign today only because `waitForJobDone` proves the worker idle, but wrong-by-construction: a closed-DB claim-fail warn). Keep `context.Background()` for both `rt.Start` and the pool ctx (`t.Context()` cancels before Cleanup).
9. **Claim race tolerance (v2 budget)** — single claimer (only `rt`; JobPool claims `jobs` only, harness runs no events relay) ⇒ re-claim is lease-expiry-driven, never contention-driven. Identity columns (`origin_kind/origin_id/action/fact_kind`) are immutable at insert, so the identity SELECT after `waitFor(govRowCount==2)` is safe mid-claim. Step 2's direct (non-`waitFor`) pins of `attempts==0 ∧ delivered_at_ns==0` are valid **only because no claimer exists pre-Start** — state that precondition in a comment. **Budget invariant: `waitForDeadline ≤ 10 s < ClaimTTL (55 s) < MaxLag (60 s)`** — a CI stall >55 s mid-flight re-claims loudly (attempts=2, re-POST), never silently.
10. **Token-cache coupling** — `tokenCalls==1` with 2 POSTs depends on `tokenSource` caching (`token.go:47-60`, mutex held across check+fetch — byte-identical ★): concurrent first deliveries serialize, the second sees the cache. Pin `postCount==2` first, `tokenCalls` second.

---

## 5. Failure modes

| # | Mode | Detection / mitigation |
|---|------|------------------------|
| F1 | **C1 regression in test** (errors.As on `New` error) | avoided by design — runtime message pin + store-level typed pin (C1) |
| F2 | Broken wiring → quarantine commits but zero governance rows, today silent | **v2 descriptor fix:** the signature is *job fails* when `WithObjectController` is missing (`worker.go:155-157` nil-guard → `waitForJobDone` 8 s timeout, 0 rows everywhere), and *job succeeds + 0 rows* for unwrapped-bus / missing `WithEventSink` / deleted quarantine `emit`. Detectors: `COUNT==2` + `postCount==2` pair; `waitForJobDone` timeout for the controller case; **v2 distinguishing observable** (see below) |
| F2a | **v2 new: `object_events` presence pin** — distinguishes the three silent wiring regressions: `eventRowID(t, db, obj.ID)` must return a row (no `sql.ErrNoRows`). **Mechanism (verified in production code):** `SoftDeleteObjectByIDWithEvent` (`event_outbox.go:186-227`) writes only `objects`/`audit_log`/`event_outbox` — never `object_events`; the `object_events` row is written only by the bus path: `s.emit :85` → `sink.Publish` (`bus.go:80-101`) → `repo.InsertEvent` (`sql_events.go:9-26`), where the wrapped repo mints the `file.deleted` governance fact (`InsertEventWithGovernance :53-97`). Therefore: **row present ⇒ unwrapped-bus regression (M2)** (raw `InsertEvent` still writes the row); **row absent ⇒ noop-sink (M3) or deleted `emit` (M4)**. `type='deleted'` matches `EventDeleted EventType = "deleted"` (`repository.go:193`). ~3 lines; split `errors.Is(sql.ErrNoRows)` |
| F3 | `file.created` pollution minting a 3rd governance row | noop-sink PUT + **two-phase bus attach (v2 §3)**; pre-start `COUNT==1` is itself a pollution detector |
| F4 | Claim race on admin row between COUNT and SELECT | identity-fields-only pin + terminal-state `waitFor`; budget `waitFor ≤10s < ClaimTTL 55s < MaxLag 60s` (constraint 9); widened lease = 5.5× stall margin vs v1's 30 s |
| F5 | Relay-start race (reconcile tick vs. snapshot) | relay deliberately unstarted; pre-start snapshot before `rt.Start`; `COUNT==2` via `waitFor`. `rt.Start` → run loop `time.NewTimer(0)` fires immediately; `reconcile()` runs **before** `deliverBatch()` (`runtime.go:200-206`); `batchSize=16 ≥ 2` + `ORDER BY available_at_ns, created_at_ns, id` make the two-row claim deterministic |
| F6 | Retry storm on transient paths | 409/422 are terminal (E9 closed list); `quiesce(50ms, postCount==2)` proves no re-POST; T-3 exclusion via `ClaimAuditGovernance`→0 rows + `OldestPendingAuditGovernance`→`ok==false`. **v2: pair the proofs explicitly — "quiesce is the timing proof; the claim predicates (`failed_at_ns=0`, `claim.go:54,78,191`) are the logical proof"** |
| F7 | Token-cache removal in production | `postCount==2` remains the primary pin; `tokenCalls==1` degrades to a warning-level secondary pin (spec §6) |
| F8 | Store mutation by failed `rt2.New` | impossible — tx rollback (A3). **v2: the re-`New(cfg)` bonus pin is MANDATORY** (§2.4): `auditgovernance.New(cfg, …)` (rev 1, binding present) must succeed after the failed gate — proves revision-1 state intact |
| F9 | SQLite locking / placeholder binding | WAL + `MaxOpenConns(1)` serialization (constraint 7); `?`-only discipline (I1); raw helpers SELECT-only |
| F10 | 500-line gate | helper budget §4.5; split-file fallback |
| F11 | Wire-format drift (token response field names, `wait_for` param, receipt envelope) | receiver mirrors the hardened wire shapes (snake_case token JSON, `wait_for=ledgered` — `http.go:37` sets it, `governance_e2e_test.go:105` rejects otherwise; `{"receipt":{…}}` 202 envelope); POST body `event_id` cross-checked against outbox `id`; `receiptMatches` tenant equality (`http.go:214-221`) |

---

## 6. Migration steps

No schema/dependency migration (test-only). Adoption sequence:

1. **Spec** — delivered (121 lines). **Design v2 supersedes it only at assertion C1** (§2) and adds the §3/§5 amendments.
2. **Implement** `internal/antivirus/governance_e2e_test.go` (package `antivirus`) per §3: harness (two-phase sink) → `quiesce`/receiver/`waitForRow`/row helpers → REQ-1 (with `object_events` presence pin) → REQ-2 → REQ-3 (C1-corrected, F8 mandatory). Reuse `waitForJobDone`/`newScanPool`/`enqueueScan`/`waitFor`/`quietLogger`/`EICAR`.
3. **Local gates:** `gofmt -l` clean; `go build ./...`; `go vet ./...`; `go test ./internal/antivirus/ -count=1`; new file ≤ 500 lines.
4. **Full gates:** `make check` (SQLite+local FS, zero network beyond `httptest`); `make test-race` (covers `./internal/...` incl. this package — A6).
5. **Docs:** no production docs change. If the repo convention requires, add a CHANGELOG line under the campaign run; do not touch `docs/configuration.md`/`docs/api.md` (no env/API surface).
6. **Out of scope (future directions):** direction 2 (gap-capture staleness gauge) and direction 3 (T-4 fact-ID recomputation pin for the quarantine shape) — both would be separate specs; direction 3 would re-use the `eventRowID` helper and the wire-derived `source_system` recomputation idiom from the v2 design.

---

## 7. Testable acceptance mapping

| Direction check | Spec REQ | Test | Concrete assertions (all `waitFor`/`quiesce`-pinned, race-free) |
|---|---|---|---|
| **AC-1** — EICAR quarantine ⇒ exactly 2 governance rows (`file.deleted` atomic + `file.delete` gap), both terminal-delivered, `Attempts==1` | **REQ-1** `TestQuarantineGovernanceE2EDualPathDelivered` (receiver `202-echo`) | steps 1-5 | (1) `waitForJobDone(t, repo, obj.ID, 1).Attempts == 1`, `JobSucceeded`; `GetObjectByID.DeletedAt != nil`; `audit_log` = 1 row (`actor == SystemActor`, `action == file.delete` — `AuditActionFileDelete = "file.delete"`, `audit.go:13` —, `target == default/eicar.txt`, detail ⊇ `av_infected`); `event_outbox` = 2 due facts (deleted@1.1 + notify@1.1 — AC-1a pins re-asserted under governance). (2) pre-start: `govRowCount == 1`, `origin_kind='file'`, `action='file.deleted'`, `fact_kind='file'`, `origin_id == eventRowID(t, db, obj.ID)`, `attempts==0`, `delivered_at_ns==0`, `failed_at_ns==0`, `available_at_ns>0`; **v2: `eventRowID` must return a row (F2a presence pin)**. (3) after `rt.Start`: `waitFor(govRowCount == 2)`; second row identity `origin_kind='admin'`, `action='file.delete'`, `fact_kind='admin'`, `origin_id == (SELECT id FROM audit_log WHERE action='file.delete' AND detail='av_infected' AND tenant_id='default')`. (4) `waitForRow` both rows `delivered_at_ns>0 ∧ failed_at_ns==0 ∧ attempts==1 ∧ claim_owner=='' ∧ last_error==''`. (5) `quiesce(50ms, postCount==2)`; each POST `event_id` ∈ outbox `id`s; `tokenCalls == 1` |
| **AC-2** — 409/422 ⇒ both facts terminal-dead ≤1 attempt, T-3-excluded, quarantine still commits | **REQ-2** `TestQuarantineGovernanceE2EPermanentDeliveryTerminal` (subtests `409`/`422`) | steps 1-4 | (1) same as REQ-1.1 (quarantine commits — relay failure never touches job/audit/events). (2) `waitForRow` each row `failed_at_ns>0 ∧ delivered_at_ns==0 ∧ attempts==1 ∧ last_error ⊇ "audit governance HTTP 409"` (resp. `422` — `model.go:36` exact text). (3) `quiesce(50ms, postCount==2)` — exactly one POST per fact, no re-claim. (4) post-death: `ClaimAuditGovernance(ctx, owner, token, cfg.Revision, 10, time.Minute)` → 0 rows; `OldestPendingAuditGovernance(ctx)` → `ok==false`; rows retained with `last_error` |
| **AC-3** — binding absent: quarantine commits, zero rows, gate blocks startup with unbound-backlog error | **REQ-3** `TestQuarantineGovernanceE2ECaptureOffAndActivationGate` | sub-1 (capture-off): `cfg.Bindings = nil`, `Enabled` true | quarantine commits (job `Attempts==1`, soft-delete, 1 audit row, 2 events-outbox facts); `govRowCount == 0`; `rt.Start` + `quiesce(300ms, postCount==0 && tokenCalls==0)` — unbound quarantine is correctly unmonitored, never queued |
| | | sub-2 (activation gate): REQ-1 harness (relay unstarted → 1 pending row), `cfg2 := cfg; cfg2.Bindings = nil; cfg2.Revision = 2` | `rt2, err := auditgovernance.New(cfg2, store, logger)` → `err != nil && rt2 == nil`; `strings.Contains(err.Error(), "audit governance unbound backlog blocks startup")`; **store-level (C1):** `_, err = store.ApplyAuditGovernanceBindings(ctx, 2, "any-digest", nil)` → `errors.As(err, &repository.AuditGovernanceUnboundBacklogError)` ∧ `TenantIDs()` ⊇ `"default"`; **v2 MANDATORY (F8):** `auditgovernance.New(cfg, …)` (revision 1, binding present) **succeeds** — failed gate left the store lossless |

**Mutation guide (v2 — corrected to its true content; the v1 sentence "each wiring regression fails exactly one cell" was false):**

| Mutation | REQ-1 | REQ-2 | sub-1 | sub-2 | Observable signature |
|---|---|---|---|---|---|
| M1 delete `WithObjectController` | ✗ step 1 | ✗ step 1 | ✗ step 1 | ✗ gate never fires | **job FAILS** (nil-guard `worker.go:155-157`) → `waitForJobDone` 8 s timeout; 0 rows everywhere |
| M2 unwrap bus repo | ✗ step 2 (COUNT 0≠1); **F2a presence pin PASSES** | ✗ step 2 | ✅ | ✗ | job OK, quarantine commits (audit 1, events-outbox 2 — direct SQL, bus-independent), governance COUNT 0, **`object_events` row present** |
| M3 remove `WithEventSink` | ✗ step 2; **F2a pin FAILS (ErrNoRows)** | ✗ step 2 | ✅ | ✗ | **identical to M2 except the F2a pin**: no `object_events` row |
| M4 delete quarantine `emit` | ✗ step 2; **F2a pin FAILS** | ✗ step 2 | ✅ | ✗ | identical to M3 (noop sink ⇒ same observables) |
| M5a receiver not 202-echo | ✗ step 4 (rows die) | ✅ | ✅ | ✅ | rows `failed_at_ns>0` instead of delivered |
| M5b/c receiver not 409/422 | ✅ | ✗ step 2 (rows delivered) | ✅ | ✅ | rows delivered instead of dead |

Truth table: **M5a/M5b/c each fail exactly one cell; M1 fails all four (job-failure signature); M2/M3/M4 share one signature** (quarantine commits, zero governance rows) and fail REQ-1/REQ-2/sub-2 while **sub-1 stays green** — sub-1 is *not* a wiring detector by construction (D4: capture-off and broken wiring are observationally identical); F2/F2a owns that role. The F2a `object_events` presence pin is what distinguishes M2 (present) from M3/M4 (absent). Every listed mutation still fails the suite loudly; no cell passes vacuously except sub-1 under M2/M3/M4, which is inherent to D4 and must never be claimed as a wiring detector.

---

## 8. Risks

- **Timing flake** — mitigated by the proven envelope (5 ms poll, `waitForRow` 10 s deadlines **with last-row dumps on timeout** — v2 replaces the v1 "predicate dumps" claim, which was false for the reused `waitFor :594-604`; `quiesce` negatives, counter/`>`-only assertions); identical to passing `TestGovernanceE2EActivationGateBoundTenant`/M1-M6. Stall window: `waitFor ≤10s < ClaimTTL 55s < MaxLag 60s` (constraint 9).
- **Admin-row birth ordering** — reconcile runs only inside the run loop; pre-start snapshot deterministic; `COUNT==2` via `waitFor` only.
- **C1 exposure** — the spec's `errors.As` sentence is corrected here; a reviewer must re-derive from `runtime.go:208-227` (non-`%w` `fmt.Errorf` at `:224`, byte-identical). Documented in §2.
- **Line-count pressure** — ~430-line budget; split-file fallback defined (constraint 5).
- **Hard gates** — `make check` + `make test-race` apply unchanged (A6); I1 discipline; no new deps (I6).

*Verification basis: every ledger row re-read on the LIVE tree (HEAD `15763e2` + uncommitted FR-1/2/3/4 drift); `go build ./...`, `go vet ./...` (affected packages), and `go test ./internal/antivirus/ -count=1` all green at amendment time; the §3 wiring (incl. `ClaimTTLSeconds=55` through `Validate()`) was compile+run-checked with a throwaway test, then removed.*
