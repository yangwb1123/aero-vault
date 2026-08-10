# Requirements Specification — `cmd/server`: permanent-error classification (409/422/tenant-mismatch/invalid-receipt → terminal) with single-sentinel replacement in `deliverFact`

**Module:** `cmd/server`
**Direction:** "B3-1 remainder: permanent-error classification (422/409/tenant-mismatch/invalid-receipt → terminal) with single-sentinel replacement in deliverFact"
**Source analysis:** `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 1)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 9 / risk reduction 9 / effort 3 / confidence 9

---

## 1. Scope

`deliverFact` (`internal/auditgovernance/relay.go:80-102`) treats **only** `ErrReceiptConflict` as terminal (→ `failFact`, `:84-89`); every other publish error flows to `retryFact` (`:90-92`), which re-claims and re-POSTs the fact with per-delay exponential backoff (`boundedBackoff`, `relay.go:163-179`) capped at `maxBackoff` (300 s default, `internal/config/config_audit_governance.go:65`) and **no attempt or total-window cap** (grep `MaxAttempts` across the package → empty). Permanently rejected facts therefore retry forever: HTTP **409/422** (surfaced as `httpStatusError`, `model.go:34-37` from `validateReceipt`'s non-202 branch, `http.go:180-182`) and **tenant-mismatch / malformed receipt** (`ErrInvalidReceipt`, `model.go:26`, from `receiptMatches` mismatch `http.go:204` and receipt-shape failures `http.go:186/190/194`). Each wedged row permanently occupies the claim cycle and inflates `OldestPendingAuditGovernance` (dead-row exclusion already correct: `failed_at_ns=0` predicates, `audit_governance_claim.go:38/62/195`), which — via `Runtime.Ready()` (`runtime.go:145-160`, maxLag 900 s) → `readyzHandler` (`cmd/server/http.go:66-67`) — converts a single permanently-rejected fact into a **503 not-ready node** and unbounded re-POST churn of an event the receiver will never ledger.

The cmd/server connection is the observable surface: `buildAuditGovernanceRuntime`/`runtimeReadiness` (`cmd/server/audit_governance.go:15/:51`) wire `Runtime` into the readiness gate, so the wedged-row churn is what flips `/readyz` to 503. The implementation lives in `internal/auditgovernance` (the package `cmd/server` wires in); the repository layer (`0042` migration + `FailAuditGovernance` + claim/lag predicates) is already shipped in the work tree and is **not** re-implemented here.

This spec scopes exactly one change: **replace the single-sentinel check in `deliverFact` with a permanent-error classifier** that maps conflict / invalid-receipt / HTTP 409 / HTTP 422 → terminal (with retention), leaving 401/403/5xx (and everything else) transient. Out of scope (see §4): B3-2 (Ready/degraded semantics), B3-3 (fact ID determinism), B3-4 (telemetry), B3-6 (`Validate()` empty bindings), any attempt-cap configuration, and any repository/migration change.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `relay.go:deliverFact/failFact/retryFact/classifyRelayError` — "only `ErrReceiptConflict` → failFact" | `deliverFact` `:80-102`; single sentinel at `:84` `if errors.Is(err, ErrReceiptConflict)` → `failFact` `:85-89`; all other errors → `retryFact` `:90-92`; `failFact` `:111-122` (terminal-with-retention, claim-loss only warned); `retryFact` `:124-137`; `classifyRelayError` `:181-190` (used only as a log label in `retryFact` `:135`) | ✅ **exact.** The `:84` branch is the only terminal path in the relay. |
| E2 | "boundedBackoff caps delay at maxBackoff=300s default per config_audit_governance.go:65" | `boundedBackoff` `relay.go:163-179` — final `return min(max(jittered, initial/2), maximum)`; `internal/config/config_audit_governance.go:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` | ✅ **exact** (`:65` as cited; cap is per-delay, not per-attempt). |
| E3 | "NO attempt/total-window cap (grep: no MaxAttempts anywhere)" | `grep -rn "MaxAttempts" internal/auditgovernance/ internal/config/` → no hits; `RetryAuditGovernance` (`audit_governance_claim.go:137-152`) takes only `(id, owner, token, lastErr, next time.Time)` — no attempt predicate | ✅ **holds.** Retry is unbounded in count; only per-delay value is capped. |
| E4 | `http.go:validateReceipt/receiptMatches` — "non-202 and receipt identity mismatch map to transient sentinels" | `validateReceipt` `http.go:178-206`: non-202 → `&httpStatusError{Status}` `:180-182` (so 409/422 land here); media-type/body-size/JSON errors → `ErrInvalidReceipt` `:186/:190/:194`; conflict → `ErrReceiptConflict` `:201`; `receiptMatches` false → `ErrInvalidReceipt` `:204`. `receiptMatches` `:214-225`: requires `EventID==fact.ID` (`:217`), `TenantID==fact.TenantID` (`:217`), non-zero `AcceptedAt` (`:218`), status ∈ {ledgered, indexed, archived} (`:222-225`). 401 also triggers token invalidation in `Publish` `:126-127` | ✅ **exact.** 409/422 and `ErrInvalidReceipt` are precisely the errors that currently flow to `retryFact` via `deliverFact` `:90-92`. |
| E5 | `model.go:httpStatusError, ErrInvalidReceipt, ErrReceiptConflict` | `ErrInvalidReceipt` `:26`, `ErrReceiptConflict` `:27`, `httpStatusError{Status int}` + `Error()` `:34-37` ("audit governance HTTP %d") | ✅ **exact.** (Also `ErrInvalidEvent` `:25`, `ErrTokenUnavailable` `:28` — both transient.) |
| E6 | 0042 migration + `FailAuditGovernance` + `failed_at_ns=0` predicates — "terminal-state plumbing and T-3 claim/lag exclusion already exist" | `internal/repository/migrations/sqlite/0042_audit_governance_terminal_failed.up.sql` (ADD COLUMN `failed_at_ns INTEGER NOT NULL DEFAULT 0`) + matching `.down.sql` present; `FailAuditGovernance` `audit_governance_claim.go:159-172` (sets `failed_at_ns`, clears `claim_owner/claim_token`, zeroes `lease_expires_at_ns`, truncates `last_error` ≤ 512 B, lease-fenced `WHERE ... failed_at_ns=0 AND claim_owner=$4 AND claim_token=$5 AND lease_expires_at_ns > $6`); claim predicate `:62` `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0 AND o.available_at_ns <= $1`; `OldestPendingAuditGovernance` `:188-201` `MIN(o.created_at_ns) ... AND o.failed_at_ns=0`; `CleanupFailedAuditGovernance` `internal/repository/audit_governance_cleanup.go:113` | ✅ **all present.** No repository work needed; `failFact` is the only writer of `failed_at_ns`. |
| E7 | `runtime_test.go:TestRuntimeConflictingReceiptIsTerminalWithRetention` — "pins conflict-only terminality, proves the extension point" | `:117-186`: httptest sink answering 202+`conflict:true`; atomic `posts` counter; asserts `posts==1` (no re-POST across 500 ms of polls, `:163-171`); `ClaimAuditGovernance` returns 0 rows `:173-175`; `OldestPendingAuditGovernance` not-pending `:176-177`; retention: prune before window → 0, after window → 1 `:180-186`. Harness `runtimeConfig` `:39-46` (poll 10 ms, initial backoff 1 s, max backoff 2 s, retention 3600 s) | ✅ **exact.** This is the template the 409/422/tenant-mismatch tests mirror; its retention block is the block AC-4 extends. |
| E8 | "Ready() → readyzHandler converts a single permanently-rejected fact into a 503-ready node" | `Runtime.Ready` `runtime.go:145-160` (drain + `time.Since(oldest) > r.maxLag` at `:157`, maxLag 900 s default `config_audit_governance.go:66`); `readyzHandler` `cmd/server/http.go:46-68`, `extra.Ready(req.Context())` error → 503 `runtime dependency unavailable` `:66-67`; wiring `runtimeReadiness`/`buildAuditGovernanceRuntime` `cmd/server/audit_governance.go:15/:51` | ✅ **holds.** Dead rows are excluded from `OldestPendingAuditGovernance` (E6), so once B3-1 lands, terminal rows no longer drive this path. |
| E9 | "proposed: `isPermanentDeliveryError` classifier ... per docs/proposals/audit-contract-batch-aero-vault.md" | `docs/proposals/audit-contract-batch-aero-vault.md:8` — "B3-1：`isPermanentDeliveryError` 分类函数替换 `deliverFact` 单哨兵（conflict/无效回执/409/422 → 终态；401/403/5xx 保持瞬态，cap 300s 已满足）" | ✅ **present.** The classifier contract is: conflict/invalid-receipt/409/422 → dead; 401/403/5xx → transient. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Only ErrReceiptConflict is terminal; 409/422/tenant-mismatch/invalid-receipt flow to retryFact" | ✅ **holds** — `deliverFact` `:84` is the sole terminal branch (E1); `validateReceipt` produces exactly `httpStatusError{409/422}` and `ErrInvalidReceipt` for those cases (E4). |
| "Retries forever with bounded per-delay backoff and no attempt cap" | ✅ **holds** — `boundedBackoff` caps the *delay* at 300 s (E2); no `MaxAttempts`/total-window mechanism exists (E3). |
| "A wedged row inflates OldestPendingAuditGovernance → 503-ready node" | ✅ **holds** — pending query counts the row (E6) until `maxLag` trips `Ready` → `readyzHandler` 503 (E8). |
| "Unbounded re-POST churn of an event the receiver will never ledger" | ✅ **holds** — 409/422/`ErrInvalidReceipt` are deterministic rejections; the receiver's acceptance predicate (`receiptMatches`, E4) cannot change without a receiver-side contract change. |

---

## 3. Requirements

### REQ-1 — Permanent-error classifier

Add `isPermanentDeliveryError(err error) bool` to `internal/auditgovernance/relay.go`, adjacent to `classifyRelayError` (`:181-190`):

- **Permanent (terminal-with-retention):** `ErrReceiptConflict` · `ErrInvalidReceipt` · `*httpStatusError` with `Status` 409 or 422.
- **Transient (bounded-backoff retry):** every other error — all other `httpStatusError` statuses (401, 403, 400, 429, 4xx/5xx), `ErrInvalidEvent`, `ErrTokenUnavailable`, token-source errors, transport/network errors, context errors.
- Must classify via `errors.Is` / `errors.As` so wrapped sentinels (e.g., `fmt.Errorf("...: %w", err)`) classify identically.
- Exhaustive by construction: permanent membership is an explicit closed list; anything not in it is transient. No status-code ranges, no substring matching.

### REQ-2 — `deliverFact` uses the classifier (the single-sentinel replacement)

In `deliverFact` (`relay.go:80-102`), replace the single `errors.Is(err, ErrReceiptConflict)` branch (`:84`) with `isPermanentDeliveryError(err)`:

- Permanent → `failFact` (existing `:85-89` path, semantics unchanged: `failed_at_ns` set, row retained with `last_error` until `CleanupFailedAuditGovernance` after the retention window).
- Transient → `retryFact` (existing `:90-92` path, unchanged).
- **Behavior for `ErrReceiptConflict` must be byte-identical to today** — pinned by the existing `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117`), which must pass unmodified.
- Update the branch comment to document the four terminal classes (conflict receipt / invalid receipt / HTTP 409 / HTTP 422) and the transient remainder.

### REQ-3 — No other changes

- **No attempt cap, no new configuration** (no `MaxAttempts`, no env knob): the direction explicitly replaces the retry-forever failure mode with classification, not with a counter. `retryFact`/`boundedBackoff`/`failFact` are untouched.
- **No repository or migration changes**: `failed_at_ns` plumbing, `FailAuditGovernance`, claim/lag exclusion, and `CleanupFailedAuditGovernance` already exist (E6).
- **No `cmd/server` code changes**: `readyzHandler`/`runtimeReadiness` are already correct for terminal rows once the relay stops re-claiming them; the 503 flip (B3-2) is a separate direction.
- `classifyRelayError` (`:181-190`) stays as-is (log label only); add a cross-reference comment noting it must stay consistent with the classifier's sentinel list.

### REQ-4 — Tests (`internal/auditgovernance/runtime_test.go`)

Follow the exact harness pattern of `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`:117-186`): httptest sink with `/token` handler (`:124-128`), atomic POST counter, `repository.Open(sqlite)` + `Migrate`, `runtimeConfig(server.URL)` (`:39-46` gives poll 10 ms / backoff 1 s→2 s / retention 3600 s / cleanup 60 s), `WrapRepository`, `runtime.Start`, poll-until-first-POST with a 3 s deadline, observe-for-no-re-POST window, `runtime.Close`, then store-level assertions.

- **REQ-4.1 — HTTP 409 and 422 are terminal** (AC-1): table-driven over status 409 and 422; sink answers status with no receipt body (non-202 → `httpStatusError`).
- **REQ-4.2 — Tenant-mismatch and malformed receipts are terminal** (AC-2): sink answers 202 + `{"receipt":{...}}` with (a) `tenant_id` ≠ fact's tenant, (b) non-ledgered/missing status, (c) unparseable body — each mapping to `ErrInvalidReceipt` (`http.go:186/190/194/204`).
- **REQ-4.3 — 401/403/5xx stay transient** (AC-3): table-driven over 401, 403, 500; assert re-POST occurs and row remains claimable.
- **REQ-4.4 — Retention prune of 409-failed rows** (AC-4): extend the existing retention-assertion block (verbatim pattern of `:180-186`) for a 409-terminal row.

---

## 4. Decisions & non-goals

- **D1 — Classifier lives in `relay.go` next to `classifyRelayError`**, not in `model.go` next to the sentinels: it is relay *policy* over the sentinel set, not part of the error model; the proposal (`audit-contract-batch-aero-vault.md:8`) names it as the `deliverFact` replacement. Unexported.
- **D2 — Permanent list is exactly {conflict, invalid-receipt, 409, 422}.** 400/404/429 are *not* classified permanent: 429 is backpressure (transient by definition) and 400/404 are outside the direction's cited evidence. AC-3 guards the transient side (401/403/5xx) so the 300 s cap boundary is provably not over-tightened.
- **D3 — No attempt cap / no config surface.** The failure mode is "retry what can never succeed", which classification fixes structurally; an `AUDIT_GOVERNANCE_MAX_ATTEMPTS` knob would add config + validation + docs for a mechanism the classifier makes redundant (and the direction explicitly scopes it out).
- **Non-goals:** B3-2 (`Ready()` maxLag flip, degraded state, 450 s alert — separate direction 2 of the same analysis), B3-3 (fact ID determinism), B3-4 (relay telemetry — direction 3), B3-6 (`Validate()` empty bindings), events-outbox behavior, any `cmd/server` handler change, any migration/`go.mod` change.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — 409/422 are terminal.** *"httptest sink answering 409 and 422 → exactly 1 POST per fact (assert posts counter, mirroring TestRuntimeConflictingReceiptIsTerminalWithRetention), then ClaimAuditGovernance returns 0 rows and OldestPendingAuditGovernance reports not-pending."*
*Testable (REQ-4.1):* table over 409, 422. Sink answers `w.WriteHeader(status)` only (no body). Assert, per case: first POST observed within 3 s (`posts.Load() >= 1`); after `runtime.Close()` and a ≥500 ms observe window, `posts.Load() == 1` (exactly one POST, never re-POSTed); `store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute)` returns `len == 0` (proves `failed_at_ns > 0` — `FailAuditGovernance` is the only writer and its failure would leave the row claimable); `store.OldestPendingAuditGovernance(ctx)` returns `ok == false`.

**AC-2 — Tenant-mismatch / malformed receipts are terminal.** *"receipt with tenant_id != fact.TenantID and malformed/non-ledgered receipt (ErrInvalidReceipt) → terminal within ≤1 attempt (failed_at_ns set, no re-POST)."*
*Testable (REQ-4.2):* table over (a) `{"receipt":{"event_id":"<fact.ID>","tenant_id":"other","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}` (mismatch → `receiptMatches` false, `http.go:217`); (b) `{"receipt":{"event_id":"<fact.ID>","tenant_id":"acme","status":"rejected","accepted_at":"..."}}` (non-ledgered status → `http.go:222-225`); (c) body `not-json` (`http.go:194`). Each: `posts == 1` after the observe window, claim returns 0 rows, `OldestPendingAuditGovernance` `ok == false`. "≤1 attempt" is satisfied by exactly-1-POST-and-never-again (the first POST is the single attempt; the terminal state is entered from its response).

**AC-3 — 401/403/5xx stay transient.** *"assert re-POST occurs with backoff ≤ maxBackoff and row remains claimable (verifies cap-300s boundary is not over-tightened)."*
*Testable (REQ-4.3):* table over 401, 403, 500. Sink answers status (401 also requires the `/token` handler to keep serving, as `Publish` invalidates the token on 401, `http.go:126-127`). Assert: `posts.Load() >= 2` within a 5 s window (re-POST occurs); each inter-POST gap ≤ 2 s = `runtimeConfig.MaxBackoffSeconds` (`runtime_test.go:45`) — the per-delay cap is respected and, at these configured values, strictly below the default 300 s cap, proving the classifier does not tighten the transient boundary; and after the backoff elapses, `ClaimAuditGovernance(ctx, "observer2", "token", 1, 10, time.Minute)` returns `len >= 1` (row remains claimable — `RetryAuditGovernance` path, `failed_at_ns` still 0). Timing is deterministic per the existing harness pattern (poll 10 ms, backoff 1 s→2 s).

**AC-4 — Retention prune of 409-failed rows.** *"CleanupFailedAuditGovernance prunes 409-failed rows only after retention window (extends the existing retention-assertion block)."*
*Testable (REQ-4.4):* in the 409 terminal test (AC-1 body), after the terminal assertions append the verbatim pattern of `runtime_test.go:180-186`: `CleanupFailedAuditGovernance(ctx, now.Add(-time.Hour), 10)` → `n == 0` (row survives before window); `CleanupFailedAuditGovernance(ctx, now.Add(time.Hour), 10)` → `n == 1` (pruned after window). This is the existing conflict-assertion block with the 409 case added; it also proves 409-failed rows share the conflict retention semantics (terminal-with-retention) rather than being hard-deleted or left forever.

---

## 6. Risks

- **Misclassification of a transient error as permanent** — a receiver-side transient 409/422 (e.g., eventual-consistency hiccup) would dead-letter a recoverable fact. Mitigated: the permanent list is exactly the direction's four classes (D2); AC-3 pins 401/403/5xx transient so the boundary is regression-guarded; `last_error` retains the full cause for diagnosis until the retention prune (7 d default, `config_audit_governance.go:68`).
- **Timing flake on loaded CI** — mitigated by the existing harness pattern already proven at `runtime_test.go:117-186`: atomic counters, poll-until-first-POST with 3 s deadline, no wall-clock equality assertions (only `>=`/`==` on counters and `<=` on inter-POST gaps), and tiny configured backoff (1 s/2 s) that makes the observe windows 5× the poll cycle.
- **Classifier/sentinel-list drift** — `isPermanentDeliveryError` and `classifyRelayError` both enumerate the sentinels; REQ-3 mandates a cross-reference comment, and AC-2 exercises every `ErrInvalidReceipt` producer (`http.go:186/190/194/204`), so drift surfaces in tests.
- **Line-count gate** — `relay.go` is 191 lines (≤500 ✓); the classifier adds ~10 lines. `runtime_test.go` is 400 lines; the added table-driven tests reuse the existing sink/harness helpers and must keep the file ≤500 lines (split helpers into a shared test file if needed — a test-side refactor, not a scope change).
- **`Ready()` 503 amplification** — not fixed here (B3-2), but B3-1 removes the *cause* (wedged rows no longer accumulate in pending); AC-1/AC-2's not-pending assertions are exactly the property that keeps the `readyzHandler` bridge green for terminal rows.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); `make check` gate applies to the eventual implementation (gofmt/build/vet/test — SQLite + local FS, zero network beyond `httptest`).*
