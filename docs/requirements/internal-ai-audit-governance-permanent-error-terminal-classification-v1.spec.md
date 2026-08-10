# Requirements Specification — permanent-error terminal classification for 422/409/tenant-mismatch/invalid-receipt (B3.1 / G4 / T-3)

**Module:** `internal/ai` (analysis label; implementation surface is `internal/auditgovernance` + `internal/repository` — see §1)
**Direction:** "Permanent-error terminal classification for 422/409/tenant-mismatch/invalid-receipt (B3.1 / G4 / T-3)" (direction 2 of `internal-ai-99180452.json`)
**Source analysis:** `docs/auto/analyses/internal-ai-99180452.json`
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` (verification basis = this checkout, including uncommitted worktree)
**Score:** value 8 / risk reduction 9 / effort 4 / confidence 9

---

## 1. Module & scope

The analysis file labels this direction under `internal/ai`; **no cited evidence or required change lives in `internal/ai/`** (verified: `grep -rn "AuditGovernance" internal/ai/` → no hits). The audit-governance relay implementing the contract lives in `internal/auditgovernance` (relay, HTTP publisher, runtime) and `internal/repository` (outbox schema, claim/lag/fail/cleanup queries, migrations). The module label is retained for traceability; all requirements target those two packages.

**Problem (as cited by the direction):** `deliverFact` treats only `ErrReceiptConflict` as terminal; every other permanent rejection — HTTP 422, HTTP 409 (surfaced as `*httpStatusError`), `ErrInvalidReceipt` (malformed/tenant-mismatched receipt), and publisher-side `ErrInvalidEvent` — falls into `retryFact` and is retried **forever** (per-delay cap 300 s default, no attempt-count terminal transition; `docs/configuration.md:273` states "facts retry indefinitely"). Separately, the dead predicate has no index: 0042 added only `failed_at_ns` (no `status`/`dead_at` columns, no partial index), and the Postgres claim scan (`delivered_at_ns=0 AND failed_at_ns=0`) rides the 0039 `audit_governance_due_idx` which does not include `failed_at_ns`.

**Worktree state (verified — the direction is partially implemented by a prior campaign):** at this checkout the classifier `isPermanentDeliveryError` (relay.go:212) and terminal tests exist, and untracked migrations `0043_audit_governance_pending_partial_index.{up,down}.sql` (both dialects) carry the dead-predicate partial indexes. **The one material divergence from the direction's problem statement:** the current classifier classifies `ErrInvalidEvent` as **transient** (relay_terminal_test.go:222-224 pins it in the transient list), while the direction explicitly lists publisher-side `ErrInvalidEvent` among the permanent rejections. REQ-1 closes this delta. All other requirements pin behavior that must survive.

**In scope:** ① the permanent-error classifier (closed list incl. `ErrInvalidEvent`), ② `deliverFact` routing through it, ③ the dead-predicate partial index in both dialects (pin + assertions), ④ tests pinning T-3 (terminal classes, transient retry cap, index existence, no unbounded retry path), ⑤ doc sync for `docs/configuration.md:273`. **Out of scope:** B3-2 (`Ready()` maxLag flip / degraded state), B3-3 (deterministic fact IDs), B3-4 (relay telemetry — direction 3 of the same analysis), any attempt-cap configuration knob, events-outbox changes, `status`/`dead_at` column rename (0042 deviation documented, see REQ-4).

---

## 2. Evidence verification

Every citation in the direction was checked against this checkout (HEAD `acfaaf4` + uncommitted worktree).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `relay.go:84-113` — "deliverFact only fails on ErrReceiptConflict; retryFact has no max-attempt path" | `deliverFact` `relay.go:82-113`, classifier branch `:87` `if isPermanentDeliveryError(err)` → `failFact` `:97-99`; transient → `retryFact` `:101-102`; `failFact` `:120-132` (terminal-with-retention, claim-loss only warned); `retryFact` `:134-148`; `boundedBackoff` `:174-190` — final `return min(max(jittered, initial/2), maximum)` (**per-delay cap only**); `classifyRelayError` `:192-201` (log label only); `isPermanentDeliveryError` `:204-221` | ✅ **structure present** (prior campaign). **Divergence:** classifier's closed list = {`ErrReceiptConflict`, `ErrInvalidReceipt`, 409, 422} — `ErrInvalidEvent` is *not* permanent (see E1a). No attempt-count terminal transition anywhere: `grep -rn "MaxAttempts" internal/auditgovernance/` → no hits. |
| E1a | Problem statement: "publisher-side `ErrInvalidEvent`" is a permanent rejection | `Publish` returns `ErrInvalidEvent` at `http.go:101` (`validOutboundFact` fail), `:105` (binding missing), `:113` (`json.Marshal` fail) — all deterministic publisher-side failures retry cannot fix; `model.go:24`. Current classifier (`relay.go:212-221`) **omits it**; `relay_terminal_test.go:222-224` pins it transient | ⚠️ **delta to close** — REQ-1 adds `ErrInvalidEvent` to the permanent set and flips the closed-list pin. |
| E2 | `http.go:validateReceipt` — "non-202 → httpStatusError, malformed body → ErrInvalidReceipt" | `validateReceipt` `http.go:178-206`: non-202 → `&httpStatusError{Status: response.StatusCode}` `:182` (409/422 land here); media-type/body-size/JSON errors → `ErrInvalidReceipt` `:186/:190/:194`; `receiptMatches` false → `ErrInvalidReceipt` `:204`; conflict → `ErrReceiptConflict` `:201`; `receiptMatches` `:214-225` requires `EventID==fact.ID` (`:217`), `TenantID==fact.TenantID` (`:217`), non-zero `AcceptedAt` (`:218`), status ∈ {ledgered, indexed, archived} (`:222-225`); 401 token invalidation in `Publish` `:126-127` | ✅ **exact.** |
| E3 | `model.go:25-35` — sentinels + `httpStatusError` | `model.go:23-28` (`ErrInvalidConfig` :23, `ErrInvalidEvent` :24, `ErrInvalidReceipt` :25, `ErrReceiptConflict` :26, `ErrTokenUnavailable` :27); `httpStatusError{Status int}` + `Error()` `:30-37` ("audit governance HTTP %d") | ✅ **exact** (cited range 25-35 = sentinels + struct start). |
| E4 | 0039 sqlite — "no status/dead_at, due_idx without failed_at_ns" | `internal/repository/migrations/sqlite/0039_audit_governance_outbox.up.sql`: table has no `status`/`dead_at`; `audit_governance_due_idx (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` + `audit_governance_tenant_idx (tenant_id, created_at_ns)` — **no `failed_at_ns`, no partial predicate**; identical in `postgres/0039...up.sql` | ✅ **exact** (both dialects). |
| E5 | 0042 — "failed_at_ns only, no partial index" | `internal/repository/migrations/sqlite/0042_audit_governance_terminal_failed.up.sql` + `postgres/0042...up.sql`: `ADD COLUMN failed_at_ns INTEGER/BIGINT NOT NULL DEFAULT 0` only; no `CREATE INDEX`; `.down.sql` (both dialects) `DROP COLUMN` | ✅ **exact** (both dialects). |
| E6 | Dead-predicate partial index requirement | Untracked `internal/repository/migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql`: `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns = 0 AND failed_at_ns = 0` + `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; down = `DROP INDEX IF EXISTS` ×2; header documents the `status`/`dead_at` deviation vs `implementation-gate.md:21` | ✅ **present in worktree** — REQ-3 pins it (must survive; no re-ship into 0042). |
| E7 | `audit_governance_claim.go` — "failed_at_ns=0 predicates already exclude dead rows" | `failed_at_ns=0` predicates at `:38` (Postgres claim), `:62` (SQLite claim), `:88` (`claimAuditGovernanceIDs`), `:146` (`RetryAuditGovernance`), `:168` (`FailAuditGovernance`, fenced `claim_owner=$4 AND claim_token=$5 AND lease_expires_at_ns > $6`), `:195` (`OldestPendingAuditGovernance` `MIN(o.created_at_ns)`), `:207` (`HasPendingDrainingAuditGovernance`); `CleanupFailedAuditGovernance` at `audit_governance_cleanup.go:113` (`DELETE ... WHERE failed_at_ns>0 AND failed_at_ns <= $1`, both dialects) | ✅ **all present.** Dead rows are excluded from claim/lag today; AC-1 asserts the terminal classes feed this state. |
| E8 | "boundedBackoff caps delay at 300 s default" | `boundedBackoff` `relay.go:174-190`; `internal/config/config_audit_governance.go:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)`; retention default `:68` `DeliveredRetentionSeconds: getEnvInt(..., 604800)`; `docs/configuration.md:273` "Retry cap; facts retry indefinitely." | ✅ **exact.** No attempt-count path anywhere (E1). |
| E9 | Harness for terminal tests | `runtimeConfig` `runtime_test.go:40-47` (poll 10 ms, initial backoff 1 s, max backoff 2 s, retention 3600 s, cleanup 60 s); `TestRuntimeConflictingReceiptIsTerminalWithRetention` `runtime_test.go:117-186` (atomic `posts` counter, `posts==1` `:163-171`, claim 0 rows `:173-175`, `OldestPending` not-pending `:176-177`, retention before→0/after→1 `:180-186`); `TestBoundedBackoffIsDeterministicAndCapped` `:189-205` (determinism + cap, incl. the 300 s pin `:202-204`) | ✅ **exact.** |
| E10 | Test coverage already in worktree | `relay_terminal_test.go`: `TestRuntimePermanentDeliveryErrorsAreTerminal` (table: http409 [retention], http422, tenant-mismatch, non-ledgered-status, unparseable-body; `runTerminalCase` observe window 2.6 s > harness max backoff 2 s; `assertTerminalState` = exactly-1-POST + not-claimable + not-pending; `assertTerminalRetention` = before→0/after→1); `TestIsPermanentDeliveryErrorClosedList` (permanent + transient closed lists, wrapped-sentinel classification); `relay_metrics_test.go` `TestRuntimeRelayCountersTrackDeliveryOutcomes` (500-retry client → re-POST, `failed` counter ≥1, invariant attempted ≥ delivered+failed+dead); `internal/repository/audit_governance_pending_idx_test.go` `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (EXPLAIN-qualified index use for claim + lag on seeded 55k-row store), `TestAuditGovernance0043DeviationHeaderPinned` (0043 header tokens; 0042 must not contain `CREATE INDEX`) | ✅ **present** — REQ-5 pins/extents these. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Only conflict:true receipts reach failFact" | ⚠️ **stale at this checkout** — prior campaign already replaced the single sentinel with `isPermanentDeliveryError` (E1); the *remaining* gap is `ErrInvalidEvent` (E1a). |
| "Permanent rejections retry indefinitely, no attempt-count terminal transition" | ✅ **holds for the transient remainder** — no `MaxAttempts` anywhere in the package; per-delay cap only (E1/E8). |
| "Dead predicate has no index; Postgres claim scan rides 0039 due_idx without failed_at_ns" | ✅ **was true at 0039/0042** (E4/E5); 0043 in the worktree closes it (E6) — REQ-3 pins the closure. |
| "A permanently rejected fact stays in OldestPending/lag" | ✅ dead rows are excluded from `OldestPendingAuditGovernance` by `failed_at_ns=0` (E7); AC-1 asserts terminal classes land there. |

---

## 3. Requirements

### REQ-1 — Permanent-error classifier, closed list incl. `ErrInvalidEvent`

`isPermanentDeliveryError(err error) bool` (`relay.go:212`) classifies via `errors.Is`/`errors.As` (wrapped sentinels classify identically):

- **Permanent (terminal-with-retention → `failFact`):** `ErrReceiptConflict` · `ErrInvalidReceipt` · `ErrInvalidEvent` · `*httpStatusError` with `Status == 409` or `Status == 422`.
- **Transient (bounded-backoff → `retryFact`):** every other error — all other `httpStatusError` statuses (401, 403, 400, 429, 5xx), `ErrTokenUnavailable`, token-source errors, transport/network errors, context errors.
- Exhaustive by construction: permanent membership is an explicit closed list; anything not listed is transient. No status-code ranges, no substring matching.
- **Delta to close:** add `ErrInvalidEvent` to the permanent branch (currently transient at `relay.go:212-221`) and move it from the transient list to the permanent list in `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:222-224`). Rationale (E1a): every `ErrInvalidEvent` producer is a deterministic publisher-side failure (`validOutboundFact` / missing binding / marshal) that retry cannot fix; the direction's problem statement names it a permanent rejection.

### REQ-2 — `deliverFact` routes through the classifier (single decision point)

`deliverFact` (`relay.go:82-113`) must use exactly one classifier call: permanent → `failFact` (`:97-99`), transient → `retryFact` (`:101-102`), success → `CompleteAuditGovernance` (`:104-112`).

- `ErrReceiptConflict` behavior stays byte-identical to `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117`) — must pass unmodified.
- `failFact`/`retryFact` bodies are unchanged: `failFact` sets `failed_at_ns` via `FailAuditGovernance` (fenced by owner+token+live lease, `claim.go:159-172`), keeps the row + `last_error` (≤512 B) until `CleanupFailedAuditGovernance`; `retryFact` schedules via `RetryAuditGovernance` with `boundedBackoff`.
- Keep `classifyRelayError` (`relay.go:192-201`) as-is (log label) with the cross-reference comment to the classifier.

### REQ-3 — Dead-predicate partial index, both dialects (pin 0043)

Migrations `internal/repository/migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` must remain the *only* home of the partial indexes (I2: 0042 is applied — never edited; `TestAuditGovernance0043DeviationHeaderPinned` guards this):

- `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns = 0 AND failed_at_ns = 0` — serves the claim path `WHERE ... AND available_at_ns <= $N AND lease_expires_at_ns <= $N ... ORDER BY available_at_ns, created_at_ns, id` (SQLite `claimAuditGovernanceSQLite` `claim.go:61-64`; Postgres `claimAuditGovernancePostgres` `:37-39`).
- `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns = 0 AND failed_at_ns = 0` — serves `MIN(o.created_at_ns)` in `OldestPendingAuditGovernance` (`:188-201`).
- `.down` drops both indexes (reversible; no column changes).
- 0039's full `audit_governance_due_idx`/`audit_governance_tenant_idx` are untouched.
- The up-migration header (both dialects) documents the `status`/`dead_at` (contract `implementation-gate.md:21` item 1) vs shipped `failed_at_ns` deviation — no column rename (zero-behavior churn; I2 forbids editing 0042).

### REQ-4 — No attempt cap, no new configuration

- No `MaxAttempts`/attempt-count transition is added for the audit-governance relay: the failure mode "retry what can never succeed" is fixed structurally by classification (REQ-1), not by a counter. `boundedBackoff`/`retryFact` are untouched; the 300 s default cap (`config_audit_governance.go:65`) remains the transient horizon.
- No changes to `internal/repository/audit_governance_claim.go` / `audit_governance_cleanup.go`: `failed_at_ns=0` claim/lag exclusion and `CleanupFailedAuditGovernance` retention pruning already implement terminal-with-retention (E7).

### REQ-5 — Docs sync

`docs/configuration.md:273` — the row for `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` currently reads "Retry cap; facts retry indefinitely." Update to state that transient failures retry with bounded backoff capped at this value, while permanent rejections (conflict/invalid receipt/invalid event, HTTP 409/422) land terminal-with-retention and are pruned after `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS`.

### REQ-6 — Tests (`internal/auditgovernance/relay_terminal_test.go`, `runtime_test.go`, `internal/repository/audit_governance_pending_idx_test.go`)

Follow the existing harness pattern (`runtimeConfig` `runtime_test.go:40-47`: poll 10 ms / backoff 1 s→2 s / retention 3600 s / cleanup 60 s; httptest sink with `/token`; atomic POST counter; poll-until-first-POST 3 s deadline; observe window > harness max backoff).

- **REQ-6.1 — Terminal table (AC-1):** table over the four receiver-observable permanent classes — HTTP 409 (status-only), HTTP 422 (status-only), tenant-mismatch receipt (`tenant_id` ≠ fact's), malformed receipt (non-ledgered status; unparseable body). Each case: exactly 1 POST, `failed_at_ns > 0` (proven by claim returning 0 rows + direct read-back), not claimable, `OldestPendingAuditGovernance` → `ok == false`, retention prune before window → 0 / after window → 1.
- **REQ-6.2 — Transient retry (AC-2):** table over 5xx (500/503) and a network error (connection refused / closed server): re-POST occurs (≥2 attempts), row remains claimable (`ClaimAuditGovernance` returns ≥1 row after backoff elapses), and each inter-POST gap ≤ harness `MaxBackoffSeconds` (2 s; strictly below the 300 s default, proving the classifier does not tighten the transient boundary). Keep the existing 300 s pin in `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:202-204`: backoff > 200 s and ≤ 300 s at max=300 s).
- **REQ-6.3 — Classifier closed list (AC-4):** `TestIsPermanentDeliveryErrorClosedList` pins both directions with `ErrInvalidEvent` in the **permanent** list (flip from `relay_terminal_test.go:222-224`), wrapped-sentinel classification via `fmt.Errorf("%w: ...")`, and the transient set {400, 401, 403, 404, 429, 5xx statuses, `ErrTokenUnavailable`, transport reset, `context.DeadlineExceeded`}.
- **REQ-6.4 — Index assertions (AC-3):** `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (EXPLAIN-qualified claim + lag on the seeded store) and `TestAuditGovernance0043DeviationHeaderPinned` (0043 header tokens; 0042 contains no `CREATE INDEX`) keep passing on both dialect files via `migrationsFS`.
- **REQ-6.5 — Grep-assert (AC-4):** assert exactly one `retryFact(` call site in `relay.go`, reachable only from the `!isPermanentDeliveryError` branch (i.e., `grep -rn "retryFact(" internal/auditgovernance/` → `relay.go:101` only, guarded by the classifier).

---

## 4. Decisions & non-goals

- **D1 — `ErrInvalidEvent` is permanent.** The direction's problem statement lists it among the permanent rejections; every producer (`http.go:101/:105/:113`) is deterministic publisher-side (E1a). This **reverses** the prior campaign's choice (cmd-server spec D2 / internal-access spec REQ-1 kept it transient) — evidence-backed: a fact that fails `validOutboundFact`/binding lookup/marshal cannot become deliverable by re-POSTing. The closed-list test flips accordingly (REQ-6.3).
- **D2 — Permanent set is exactly {conflict, invalid-receipt, invalid-event, 409, 422}.** 400/404/429 are not permanent: 429 is backpressure (transient by definition); 400/404 are outside the direction's cited classes. AC-2 guards the transient boundary.
- **D3 — No attempt cap / no config surface** (REQ-4): the direction explicitly scopes out an attempt-count transition; classification fixes the structural failure mode.
- **D4 — Index in a new 0043 migration, not 0042** (REQ-3): I2 forbids editing applied migrations; 0042 is committed (E5). Deviation vs contract `status`/`dead_at` documented in the migration header (REQ-3, pinned by `TestAuditGovernance0043DeviationHeaderPinned`).
- **Non-goals:** B3-2 (`Ready()` maxLag flip, degraded state, 450 s alert — direction 2 of the sibling analysis), B3-3 (fact ID determinism — direction 1), B3-4 (relay telemetry — direction 3), B3-6 (`Validate()` empty bindings), events-outbox behavior, any `cmd/server` change, any `go.mod` change.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — 422/409/malformed/tenant-mismatch receipts are terminal.** *For each of 422, 409, malformed receipt, and tenant-mismatch receipt, a httptest receiver returns the code → exactly 1 POST, row terminal (`failed_at_ns>0`), not claimable, excluded from `OldestPendingAuditGovernance`, pruned only after retention.*
*Testable (REQ-6.1):* table over the four classes (malformed split into non-ledgered-status and unparseable-body). Assert per case: first POST within 3 s; after the observe window (2.6 s > harness max backoff 2 s) `posts == 1`; `ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute)` returns `len == 0`; direct read-back of the row's `failed_at_ns` is `> 0`; `OldestPendingAuditGovernance(ctx)` returns `ok == false`; `CleanupFailedAuditGovernance(ctx, now.Add(-time.Hour), 10)` → `0` (before window) then `CleanupFailedAuditGovernance(ctx, now.Add(time.Hour), 10)` → `1` (after window).

**AC-2 — Transient 5xx/network errors still retry, delay capped ≤300 s.** *Assert transient 5xx/network errors still retry with delay capped ≤300s.*
*Testable (REQ-6.2):* table over 500, 503, and connection-refused (closed server). Assert re-POST (≥2 attempts) within a 5 s window; each inter-POST gap ≤ 2 s (= harness `MaxBackoffSeconds`, provably below the 300 s default); row remains claimable after backoff elapses (`failed_at_ns == 0`, claim returns ≥1 row). The 300 s default cap itself is pinned at the unit level (`TestBoundedBackoffIsDeterministicAndCapped` `runtime_test.go:202-204`: 200 s < delay ≤ 300 s at max=300 s).

**AC-3 — Dead-predicate partial index in both sqlite and postgres migrations.** *Assert dead-predicate partial index (`failed_at_ns=0`) exists in both sqlite and postgres migrations.*
*Testable (REQ-6.4):* `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` asserts EXPLAIN-qualified use of `audit_governance_pending_claim_idx`/`audit_governance_pending_lag_idx` (both carrying `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`) for the claim and lag queries; `TestAuditGovernance0043DeviationHeaderPinned` asserts both dialect files (`sqlite/0043...up.sql`, `postgres/0043...up.sql`) carry the indexes and that neither 0042 file contains `CREATE INDEX` (no re-ship).

**AC-4 — No unbounded retry path remains.** *Grep-assert no unbounded retry path remains (`retryFact` reachable only from non-permanent classes).*
*Testable (REQ-6.3/REQ-6.5):* `grep -rn "retryFact(" internal/auditgovernance/` yields exactly one call site (`relay.go:101`), guarded by `if isPermanentDeliveryError(err) → failFact; return` — i.e., reachable only when the classifier says non-permanent; `TestIsPermanentDeliveryErrorClosedList` pins the closed list in both directions (with `ErrInvalidEvent` permanent after D1), so any future permanent class added without classifier coverage fails CI.

---

## 6. Risks

- **Misclassification of a transient error as permanent** — 409/422 are receiver-authoritative rejections in this contract (the receiver's acceptance predicate `receiptMatches`, E2, is fixed), so dead-lettering is correct; 429/other 4xx/5xx stay transient (D2). AC-2 guards the boundary; `last_error` retains the full cause until the retention prune (7 d default).
- **`ErrInvalidEvent` flip regression** — flipping it permanent (D1) changes behavior for a malformed fact from "retry forever" to "terminal-with-retention". Mitigated: producers are deterministic (E1a); the closed-list test update (REQ-6.3) is the pin; `TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference` (`runtime_test.go:207-243`) is unaffected (it exercises `New()` rejection of unsafe binding removal, not delivery classification).
- **Timing flake on loaded CI** — mitigated by the existing harness pattern already proven in `runtime_test.go:117-186` and `relay_terminal_test.go`: atomic counters, poll-until with 3 s deadline, no wall-clock equality (only `==`/`>=` on counters and `<=` on gaps), observe window 2.6 s = 5× the poll cycle and > max backoff 2 s.
- **Index-regression (index dropped or re-shipped into 0042)** — pinned by the EXPLAIN test and `TestAuditGovernance0043DeviationHeaderPinned` (REQ-6.4); I2 forbids editing 0042.
- **File-size gates** — `relay.go` 221 lines, `runtime_test.go` 410, `relay_terminal_test.go` 231, `audit_governance_pending_idx_test.go` 285: all ≤500 ✓; the classifier flip and closed-list edit add <10 lines.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4` + worktree); `make check` (gofmt/build/vet/test — SQLite + local FS, zero network beyond `httptest`) applies to the implementation.*
