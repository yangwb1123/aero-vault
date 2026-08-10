# Requirements Specification — `internal/auditgovernance`: complete permanent-error classification (422/409/tenant mismatch/invalid receipt → terminal ≤1 attempt) + partial index on the live-row predicate

**Module:** `internal/auditgovernance` (+ `internal/repository` — `audit_governance_*` table/queries/migrations; `internal/config`)
**Direction:** "Complete permanent-error classification (422/409/tenant mismatch/invalid receipt → terminal within ≤1 attempt) + partial index on the live-row predicate (B3.1 / T-3)" (analysis direction 2)
**Source analysis:** `docs/auto/analyses/internal-auditgovernance-ef1a62fa.json` (direction index 1 of 3)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (worktree carries uncommitted WIP — see §2.3) · **Score:** value 8 / risk reduction 9 / effort 3 / confidence 9
**Contract reference:** `docs/campaigns/implementation-gate.md:21` — item 1 (aero-vault): "死信终态（F3）：status/dead_at 列 + 部分索引；移植 sink `DeliveryError.Permanent` 分类（422/409/tenant mismatch/无效回执 → 终态 ≤1 次尝试）；瞬态有界重试 cap 300s；dead 行排除出 claim/lag. T-3：422 → 一个周期内终态".

---

## 1. Scope

The direction's **problem statement is stale relative to this checkout**: every prescribed behavior now exists in the worktree (uncommitted, part of the B3-1 campaign):

- `deliverFact` routes the **closed-list** `isPermanentDeliveryError` — `{ErrReceiptConflict, ErrInvalidReceipt, HTTP 409, HTTP 422}` — to the terminal `failFact` path (`relay.go:87`), replacing the old single-sentinel check (`errors.Is(err, ErrReceiptConflict)` only).
- Migration **0043** (sqlite + postgres) adds the partial indexes on the exact live-row predicate `delivered_at_ns=0 AND failed_at_ns=0`.
- The "no total-time terminal bound" half of the problem statement is closed by **0044** (`first_attempt_at_ns` anchor) + `cumulativeWindowExceeded` (`relay.go:145`): a transient-only failure stream goes terminal once `now - first_attempt_at_ns > MaxBackoffSeconds`.
- The only contract prescription *not* implemented as literally written is the **`status`/`dead_at` column shape** — replaced by a documented, test-pinned deviation (D1): 0042's `failed_at_ns` is the terminal marker and every consumer already predicates on it.

This spec therefore **verifies and locks** the direction's acceptance (T-3) against the implemented worktree, restates each supplied check as a testable assertion, and records what is committed vs. WIP so the merge gate has an explicit checklist. It does **not** add `status`/`dead_at` columns (D1 — a zero-behavior rename of an applied schema would violate I2), does not change `ErrInvalidEvent`/`ErrTokenUnavailable` terminality (D2), and does not touch the other two directions in the same analysis file (deterministic fact IDs; relay metrics + `Ready()` decoupling).

---

## 2. Evidence verification

Every cited file/symbol from the direction was checked against the repository on this checkout (line numbers = current worktree).

### 2.1 Cited-symbol table

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `relay.go:deliverFact` — "fails the fact solely on ErrReceiptConflict; all other errors → retryFact" | `deliverFact` `relay.go:82-118`; branch `:87` `if isPermanentDeliveryError(err)` → `failFact`; all other errors → `retryFact` `:101`; closed list `isPermanentDeliveryError` `:255-265` = `{ErrReceiptConflict, ErrInvalidReceipt, httpStatusError{409}, httpStatusError{422}}` via `errors.Is`/`errors.As` | ❌ **problem statement outdated** — the closed-list classifier ships in the worktree; only `ErrInvalidEvent`, `ErrTokenUnavailable`, other HTTP statuses and transport/context errors remain transient (D2). |
| E2 | `http.go:validateReceipt` — "ErrInvalidReceipt, httpStatusError{Status} for 422/409, receiptMatches tenant/event_id check" | `validateReceipt` `http.go:178-212`: non-202 → `&httpStatusError{Status}` `:182` (409/422 surface here); non-JSON media type / oversized / unparseable body → `ErrInvalidReceipt` `:186/:190/:194`; `conflict:true` → `ErrReceiptConflict` `:201`; `receiptMatches` failure → `ErrInvalidReceipt` `:204`. `receiptMatches` `:214-225`: `EventID != fact.ID` **or** `TenantID != fact.TenantID` **or** zero `AcceptedAt`, or status ∉ {ledgered, indexed, archived} → false | ✅ **exact** — tenant mismatch is the `TenantID` branch of `ErrInvalidReceipt` (`:217`), not a separate sentinel. The analysis's "tenant-mismatch case is unreachable" phrasing is imprecise: the branch is reachable via a wrong `tenant_id` receipt and is now terminal by classification (E1). |
| E3 | `relay.go:boundedBackoff` — "per-attempt cap only; no attempt-count or total-time terminal bound" | `boundedBackoff` `relay.go:209-217` + pure core `boundedBackoffDelay` `:219-232`: per-attempt cap at `maximum`, deterministic per-ID jitter (±25 %, clamped) | ⚠️ **partially outdated** — per-attempt cap verified; the missing total-time bound is now implemented: `cumulativeWindowExceeded` `:145-147` checked first in `retryFact` `:153-157` → `failFact` (window == `MaxBackoffSeconds`, anchored by 0044's `first_attempt_at_ns`). There is still no *attempt-count* bound (D3). |
| E4 | `0039_audit_governance_outbox.up.sql` — "plain audit_governance_due_idx, no partial index" | `migrations/{sqlite,postgres}/0039_…up.sql`: `CREATE INDEX audit_governance_due_idx ON audit_governance_outbox (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` — full-table index, no `failed_at_ns`, no partial predicate | ✅ **holds** — superseded for the pending paths by 0043 (E5). |
| E5 | `0042_audit_governance_terminal_failed.up.sql` — "failed_at_ns column only, no partial index" | `migrations/{sqlite,postgres}/0042_…up.sql`: `ALTER TABLE audit_governance_outbox ADD COLUMN failed_at_ns INTEGER/BIGINT NOT NULL DEFAULT 0` only; no `CREATE INDEX` (pinned by `TestAuditGovernance0043DeviationHeaderPinned`) | ✅ **holds** — and is the D1 deviation baseline. The required partial index ships as a new **0043 pair** (I2: 0039/0042 are applied): `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns=0 AND failed_at_ns=0` + `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns=0 AND failed_at_ns=0`, up+down in both dialects; down = `DROP INDEX IF EXISTS` ×2 (reversible, 0036 convention). Header documents the status/dead_at deviation referencing `implementation-gate.md:21` | ✅ **shipped (WIP)** — see REQ-4. |
| E6 | `audit_governance_claim.go:38,62,88,195,207` — "failed_at_ns=0 filters verified already correct" | Postgres claim `:31-49` (predicate `:54`); SQLite claim inner select `:51-80` (predicate `:78`); `claimAuditGovernanceIDs` fenced UPDATE `:81-110` (predicate `:110`); `RetryAuditGovernance` `:163-170` (predicate `:169`); `FailAuditGovernance` `:186-193` (predicate `:191`); `OldestPendingAuditGovernance` `:213-220` (predicate `:218`); `HasPendingDrainingAuditGovernance` `:224-231` (predicate `:230`) | ✅ **exact** — dead rows excluded from claim, retry, fail re-entry, lag, and drain-pending. |
| E7 | `audit_governance_cleanup.go:CleanupFailedAuditGovernance` — "prunes terminal rows after retention" | `CleanupFailedAuditGovernance` `audit_governance_cleanup.go:113-135`: `WHERE failed_at_ns>0 AND failed_at_ns <= $1 ORDER BY failed_at_ns,id LIMIT $2` (PG `FOR UPDATE SKIP LOCKED` / SQLite batch) | ✅ **exact** — called from `cleanupDelivered` `relay.go:185-207` on the delivered-retention cadence. |
| E8 | `config` — "AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS=300 is only the per-attempt cap" | `config_audit_governance.go:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)`; validation `:247-249` (`>= InitialBackoffSeconds`) and `:258` (`<= 86_400`); wired as `maxBackoff` `runtime.go:96`; `DeliveredRetentionSeconds` default 604800 (7d) `:68` → `retention` `runtime.go:98` | ✅ **exact** — 300 s is both the per-attempt cap and, since 0044, the cumulative window (E3). |

### 2.2 Problem-statement checks

| Statement | Verdict |
|---|---|
| "Terminal-with-retention exists only for ErrReceiptConflict: deliverFact fails the fact solely on that sentinel" | ❌ **outdated** — `isPermanentDeliveryError` (`relay.go:255-265`) also lands `ErrInvalidReceipt` and HTTP 409/422 (E1); closed-list-pinned by `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:200`). |
| "httpStatusError (any 4xx, incl. 422/409), ErrInvalidReceipt … retried via retryFact with boundedBackoff indefinitely; no attempt-count or total-time terminal bound" | ⚠️ **partially outdated** — 409/422 + `ErrInvalidReceipt` are terminal (E1). The total-time bound now exists (0044 + `cumulativeWindowExceeded`); only the attempt-count bound is absent (D3, and the acceptance never required one). |
| "the tenant-mismatch case is unreachable today only because receiptMatches compares receipt.TenantID to fact.TenantID" | ⚠️ **imprecise** — the `TenantID` mismatch branch (`http.go:217`) is reachable with a wrong-tenant receipt and lands `ErrInvalidReceipt`; it is driven e2e by the `tenant-mismatch` table row (REQ-1 AC-1.2). |
| "0042 added only failed_at_ns with no partial index; 0039's due_idx is a plain index; retained dead rows keep growing the claim/lag scans" | ✅ **holds for 0042/0039 as written** — fixed by the 0043 partial-index pair (E5, REQ-4), which serves exactly the pending predicate so dead rows are out of the claim/lag scan index. |
| "there is no status/dead_at column shape as contracted" | ✅ **holds** — deliberate, documented deviation D1 (0043 header + `TestAuditGovernance0043DeviationHeaderPinned`). |

### 2.3 Commit-state and test-run evidence

- **Committed:** `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117`), `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`), `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`), the `failed_at_ns` claim/lag predicates, the `IncAuditGovernanceRelay*` counters (`telemetry/metrics.go:187-210`, landed with B3-2 `15763e2`).
- **Uncommitted WIP (this direction):** `relay.go` classifier/failFact/cumulative window; `claim.go` first-attempt anchor; `http.go`/`model.go`; migrations `0043` + `0044` (sqlite+postgres, untracked); tests `relay_terminal_test.go`, `audit_governance_pending_idx_test.go`, `cumulative_window_test.go` (untracked). Merge gate: the spec's ACs are the entry criteria for committing these files.
- **Test run (this checkout):**

```
go test ./internal/auditgovernance/ ./internal/repository/ -count=1
→ ok  github.com/aero-vault/aero-vault/internal/auditgovernance  30.949s
→ ok  github.com/aero-vault/aero-vault/internal/repository       31.992s
```

---

## 3. Requirements

### REQ-1 — Permanent delivery classes land terminal within ≤1 attempt

The relay MUST classify exactly `{ErrReceiptConflict, ErrInvalidReceipt, HTTP 409, HTTP 422}` as permanent; a permanent fact MUST be failed via `FailAuditGovernance` (`failed_at_ns>0`, `last_error` set, retained), MUST be POSTed exactly once (attempt ≤1), and MUST never be re-claimed or re-POSTed. Wrapped sentinels MUST classify identically (`errors.Is`/`errors.As`).

- **AC-1.1 (closed list, both directions).** `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:200`): the four permanent errors and their wrapped forms return true; the transient set — `httpStatusError` 400/401/403/404/410/429/500/501/503, `ErrInvalidEvent`, `ErrTokenUnavailable`, bare transport error, `context.DeadlineExceeded` — returns false. *Existing (WIP).*
- **AC-1.2 (e2e terminal table — the supplied T-3 classification check).** `TestRuntimePermanentDeliveryErrorsAreTerminal` (`relay_terminal_test.go:36`): five sink rows — `http409`, `http422`, `tenant-mismatch` (202 + receipt with wrong `tenant_id`, isolating the `receiptMatches` TenantID branch), `non-ledgered-status` (202 + `status:"rejected"`), `unparseable-body` (202 + non-JSON) — each asserts: exactly 1 POST within an observe window (2.6 s) exceeding the harness max backoff (2 s); after `Close`, `ClaimAuditGovernance` returns 0 rows and `OldestPendingAuditGovernance` reports none (`assertTerminalState` `:126-146`); the 409 case additionally asserts the retention prune (`assertTerminalRetention` `:148-164`). *Existing (WIP).*
- **AC-1.3 (attempt ≤1 + last_error at the repository level).** `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`audit_governance_pending_idx_test.go:210`): claim increments `attempts` once, `FailAuditGovernance` is the sole writer of `failed_at_ns` and writes `last_error` (`claim.go:191`), both land on the same row → `failed_at_ns > 0` AND `attempts == 1` read back. *Existing (WIP).*

### REQ-2 — Transient classes keep retrying with backoff capped ≤300 s

Every error outside REQ-1's closed list MUST be rescheduled via `RetryAuditGovernance` with `boundedBackoff`; the delay MUST be deterministic per fact ID and MUST never exceed the configured cap (default 300 s, `config_audit_governance.go:65`).

- **AC-2.1 (transient classification).** `TestIsPermanentDeliveryErrorClosedList` transient half (AC-1.1). *Existing (WIP).*
- **AC-2.2 (cap + determinism).** `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`): for `attempts=20`, `initial=1s`, identical fact ID yields identical delay in `(200s, 300s]` when `maximum=300s` — pins the 300 s default cap, not merely "some cap". *Existing (committed).*
- **AC-2.3 (e2e transient keeps retrying).** `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (`relay_terminal_test.go:245`): a 500-sink fact is POSTed ≥ 2 times over ≥ 2 backoff windows with the runtime still running and the inter-POST gaps strictly grow (deterministic proxy for `available_at_ns` strictly increasing; at harness config 1 s→2 s ±25 % jitter, `min(gap₂) > max(gap₁)` for every fact ID). *Existing (WIP).* Also `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`): the 500-sink fact reschedules (`failed_total` delta ≥ 1) while only the conflict fact goes dead. *Existing (WIP).*

### REQ-3 — Dead rows excluded from claim/lag/drain-pending (T-3 lock)

A row with `failed_at_ns > 0` MUST be invisible to `ClaimAuditGovernance`, `OldestPendingAuditGovernance`, and `HasPendingDrainingAuditGovernance`; a failed row MUST never reappear in a later claim; `FailAuditGovernance`/`RetryAuditGovernance`/`CompleteAuditGovernance` MUST be fenced by the claim identity (owner+token+live lease).

- **AC-3.1 (SQL predicate).** All claim/retry/fail writes and both pending reads carry `failed_at_ns=0` (`audit_governance_claim.go:54,78,110,169,191,218,230`). *Verified statically.*
- **AC-3.2 (repository lock test).** `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`): after `FailAuditGovernance`, a fresh claim returns 0 rows and `OldestPendingAuditGovernance` reports none; a stale owner/token cannot fail the fact (fencing); the failed row is not pruned before the window and is pruned after; the origin is re-enqueueable post-prune. *Existing (modified, WIP).*
- **AC-3.3 (runtime lock).** `assertTerminalState` (`relay_terminal_test.go:126-146`) runs the same two probes through the live store after `Close` for every permanent class, plus the window-terminalized transient stream (`relay_terminal_test.go:306-310`). *Existing (WIP).*

### REQ-4 — 0043 migration pair (sqlite + postgres): partial index on the live-row predicate

Migration 0043 MUST exist in both dialects and MUST add partial indexes whose predicate is exactly the pending predicate `delivered_at_ns=0 AND failed_at_ns=0`, serving (a) the claim path's range + ORDER BY and (b) the lag `MIN` path. The `status`/`dead_at` columns prescribed by the contract are **not** added — replaced by `failed_at_ns` with an in-file documented deviation (D1). (Already shipped as WIP; ACs are lock tests.)

- **AC-4.1 (files).** `migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` exist and are embedded; up creates `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns = 0 AND failed_at_ns = 0` and `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; down is reversible (`DROP INDEX IF EXISTS` ×2). *Verified statically.*
- **AC-4.2 (plans — the supplied EXPLAIN check).** `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (`audit_governance_pending_idx_test.go:177`): seeds 55k history + 300 pending rows (heavy `available_at_ns` ties), runs `ANALYZE`, then asserts via `EXPLAIN QUERY PLAN` that the exact SQLite claim inner-select shape uses `audit_governance_pending_claim_idx` with no full-table scan (`SCAN o` absent) and the isolated lag `MIN` probe uses `audit_governance_pending_lag_idx`. Seed/plan shape is pinned to modernc v1.50.1 (SQLite 3.53.1). *Existing (WIP).*
- **AC-4.3 (deviation pinned, not re-shipped).** `TestAuditGovernance0043DeviationHeaderPinned` (`audit_governance_pending_idx_test.go:251`): 0043 headers (both dialects) contain `failed_at_ns`, `status`, `dead_at`, `implementation-gate` (the deviation is documented, referencing the contract row); 0042 files contain `failed_at_ns` and **no** `CREATE INDEX` (the index was not smuggled into 0042). *Existing (WIP).*

### REQ-5 — Terminal-with-retention prune bounded by `DeliveredRetentionSeconds`

Failed rows MUST be retained for diagnosis until the retention window and then pruned by `CleanupFailedAuditGovernance`; the window MUST be the delivered-retention window (default 7 d).

- **AC-5.1 (wiring).** `retention: time.Duration(cfg.DeliveredRetentionSeconds) * time.Second` (`runtime.go:98`); `cleanupDelivered` calls `CleanupFailedAuditGovernance(ctx, now.Add(-r.retention), …)` on the cleanup cadence (`relay.go:196`); `CleanupFailedAuditGovernance` deletes `failed_at_ns>0 AND failed_at_ns <= cutoff` (`audit_governance_cleanup.go:123/:134`). *Verified statically.*
- **AC-5.2 (early/late prune).** `assertTerminalRetention` (`relay_terminal_test.go:148-164`, exercised by the `http409` row) and `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`): cleanup with `now.Add(-1h)` deletes 0 rows; cleanup with `now.Add(+1h)` deletes exactly 1. *Existing.*

### REQ-6 — Total-time terminal bound for transient streams (0044 anchor)

A transient-only failure stream MUST go terminal once `now - first_attempt_at_ns` strictly exceeds the cumulative window (== `MaxBackoffSeconds`); the anchor MUST be set exactly once, inside the fenced claim (`CASE WHEN first_attempt_at_ns=0`), and a zero/negative elapsed (un-anchored row, DB clock ahead) MUST never be window-terminal. This closes the direction's "no total-time terminal bound" gap; the supplied acceptance (transient retries with backoff ≤300 s within the window) is preserved.

- **AC-6.1 (boundary + safe direction).** `TestCumulativeWindowExceededBoundary` (`cumulative_window_test.go:35`) and `TestCumulativeWindowDecisionMonotone` (`:73`): `==` boundary stays transient; zero anchor and negative elapsed never terminal; decision monotone in `now`. *Existing (WIP).*
- **AC-6.2 (e2e).** `TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow` (`cumulative_window_test.go:111`): a 5xx fact stream lands `failed_at_ns>0` after the window, never re-claimed, absent from lag. *Existing (WIP).*
- **AC-6.3 (multi-worker race).** `TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome` (`cumulative_window_test.go:208`): stale workers holding expired leases compute the same direction; fenced fail/retry writes land at most one outcome. *Existing (WIP).*

---

## 4. Decisions (verified, governing — not implementation gaps)

| # | Decision | Evidence | Rationale |
|---|---|---|---|
| D1 | **No `status`/`dead_at` columns.** `failed_at_ns` (0042) is the terminal marker; the 0043 header documents the deviation from `implementation-gate.md:21` ("Deviation note … zero-behavior rename; I2") and `TestAuditGovernance0043DeviationHeaderPinned` pins the documentation | E4, E5, AC-4.3 | 0039 is a timestamp-led schema; claim/lag/drain/cleanup all already predicate on `failed_at_ns`; adding status/dead_at would be a zero-behavior rename of an applied schema (I2 forbids editing applied files; a rename migration is pure churn with no behavioral delta). |
| D2 | **`ErrInvalidEvent` and `ErrTokenUnavailable` stay transient.** Closed list excludes them; `TestIsPermanentDeliveryErrorClosedList` pins them transient | E1, AC-1.1 | `ErrInvalidEvent` is a local-construction error (`http.go:101-113`: invalid outbound fact, missing binding, marshal failure), not a receiver rejection; `ErrTokenUnavailable` resolves on the next token refresh. The acceptance requires terminality only for 409/422/tenant-mismatch/invalid receipt. Changing these would expand scope. |
| D3 | **No attempt-count dead-letter bound.** Terminality is error-class-based plus the cumulative time window (REQ-6); there is deliberately no max-attempts counter | E3, REQ-6 | The acceptance requires classification-based terminality ≤1 attempt for permanent classes and capped backoff for transient ones — both satisfied. The direction's "no attempt-count **or** total-time bound" complaint is closed on the time axis; an attempt-count bound is out of scope. |

## 5. Non-goals (explicitly excluded)

- **Deterministic fact IDs** (analysis direction 1 of the same file) — separate direction; `facts.go`/`audit_governance_factid.go` work is out of scope here.
- **Relay metrics + `Ready()`/backlog-age decoupling, 450 s alert** (analysis direction 3 of the same file) — separate direction, covered by `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` and landed at `15763e2`; the `IncAuditGovernanceRelay*` counters already in `relay.go` are that direction's surface, not this one's.
- **Adding `status`/`dead_at` columns** (D1), **`ErrInvalidEvent`/`ErrTokenUnavailable` terminality** (D2), **attempt-count dead-letter bound** (D3).

## 6. Acceptance matrix (supplied checks → requirement → testable pin → status)

| Supplied acceptance check (T-3) | Requirement | Testable pin | Status |
|---|---|---|---|
| Receipts returning 422, 409, malformed receipt, tenant-mismatch → row lands `failed_at_ns>0` with `attempts<=1` and `last_error` set | REQ-1 | AC-1.2 `TestRuntimePermanentDeliveryErrorsAreTerminal` (5 table rows incl. `tenant-mismatch` and `unparseable-body`, exactly 1 POST); AC-1.3 `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`failed_at_ns>0` ∧ `attempts==1`, `last_error` written by `FailAuditGovernance` `claim.go:191`) | ✅ implemented (WIP), passing |
| Never re-claimed by `ClaimAuditGovernance`; excluded from `OldestPendingAuditGovernance` | REQ-3 | AC-3.1 static predicates (`claim.go:54,78,110,218`); AC-3.2 `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`; AC-3.3 `assertTerminalState` for every permanent class + window-terminal stream | ✅ implemented (WIP), passing |
| Transient errors (5xx/transport) keep retrying with bounded backoff ≤300 s | REQ-2 | AC-2.1 closed-list transient half; AC-2.2 `TestBoundedBackoffIsDeterministicAndCapped` (cap ∈ (200 s, 300 s]); AC-2.3 `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (≥2 POSTs, growing gaps) + 500-sink reschedule via `failed_total` | ✅ implemented (WIP), passing |
| Migration test: partial index (`WHERE delivered_at_ns=0 AND failed_at_ns=0`) exists | REQ-4 | AC-4.1 0043 up/down files both dialects (`pending_claim_idx`, `pending_lag_idx`); AC-4.3 `TestAuditGovernance0043DeviationHeaderPinned` (0042 carries no `CREATE INDEX`) | ✅ implemented (WIP), passing |
| `EXPLAIN QUERY PLAN` for claim/OldestPending selects the partial index | REQ-4 | AC-4.2 `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (claim → `audit_governance_pending_claim_idx`, no `SCAN o`; isolated lag MIN → `audit_governance_pending_lag_idx`) | ✅ implemented (WIP), passing |
| (Problem statement) no total-time terminal bound | REQ-6 | AC-6.1/6.2/6.3 `cumulative_window_test.go` (boundary, monotone, e2e, multi-worker race) | ✅ implemented (WIP), passing |

**Remaining work from the supplied acceptance:** none — all checks are implemented in the worktree and passing (`go test ./internal/auditgovernance/ ./internal/repository/ -count=1` → ok). The merge gate for this direction is: commit the uncommitted WIP listed in §2.3 with these ACs green. The only prescription intentionally not implemented literally is `status`/`dead_at` (D1), which is documented in-migration and pinned by test rather than silently dropped.
