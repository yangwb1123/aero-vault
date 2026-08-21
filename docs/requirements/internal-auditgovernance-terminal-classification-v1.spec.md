# Requirements Specification — `internal/auditgovernance`: complete terminal classification (409/422/invalid-receipt/tenant-mismatch → dead-letter, pending partial index, terminal-with-retention)

**Module:** `internal/auditgovernance` (+ `internal/repository` — `audit_governance_*` tables/queries; `internal/config`)
**Direction:** "Complete terminal classification: 409/422/invalid-receipt/tenant-mismatch → dead-letter (status/dead_at + partial index), stop infinite transient retry" (analysis direction 1)
**Source analysis:** `docs/auto/analyses/internal-antivirus-4eff1e6c.json` (direction 1) — ⚠️ **filename label is wrong**: the file is tagged `internal-antivirus`, but all three directions inside target `internal/auditgovernance` + `internal/repository`; `internal/antivirus/` (`antivirus.go`, `worker.go`, `hardening_test.go`, …) is not involved in any of them. The direction content is authoritative.
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` · **Score:** value 9 / risk reduction 9 / effort 5 / confidence 9
**Contract reference:** `docs/campaigns/implementation-gate.md:21` — item 1 (aero-vault): "死信终态（F3）：status/dead_at 列 + 部分索引；移植 sink `DeliveryError.Permanent` 分类（422/409/tenant mismatch/无效回执 → 终态 ≤1 次尝试）；瞬态有界重试 cap 300s；dead 行排除出 claim/lag. T-3：422 → 一个周期内终态".

---

## 1. Scope

The direction's **problem statement is stale relative to this checkout**: the relay already routes HTTP 409/422, `ErrInvalidReceipt` (tenant-mismatch, non-ledgered status, unparseable/non-JSON body) **and** `ErrReceiptConflict` to the terminal `failFact` path via the closed-list classifier `isPermanentDeliveryError` (`internal/auditgovernance/relay.go:82-118,212-221`), and migration **0043 already exists** in both dialects with the pending partial index. What the direction *prescribed* that is not implemented is exactly one thing: the **`status`/`dead_at` columns** — replaced by a **documented, test-pinned deviation** (see §2 D1): `0042` shipped `failed_at_ns` as the timestamp-led terminal marker; claim/lag/cleanup already predicate on `failed_at_ns=0` / `failed_at_ns>0`, so the contract's behavioral intent (terminal ≤1 attempt, dead rows excluded from claim/lag, retention prune) is fully satisfied.

This spec therefore **verifies and locks** the direction's acceptance checks against the implemented state, restates each supplied acceptance as a testable assertion, and adds exactly one small test gap found during verification (transient e2e re-POST pin, REQ-2 AC-2.4). It does **not** add `status`/`dead_at` columns (deviation D1 — renaming a shipped, applied schema would violate I2), and does not touch `ErrInvalidEvent` terminality (decision D2), attempt/age-based dead-letter bounds (decision D3), or the other two directions in the same analysis file (deterministic fact IDs; relay metrics + `Ready()` decoupling).

---

## 2. Evidence verification

Every cited file/symbol from the direction was checked against the repository on this checkout.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `relay.go:deliverFact/failFact/retryFact` — "only `ErrReceiptConflict` lands terminal; 409/422, `ErrInvalidReceipt`, `ErrInvalidEvent` flow to `retryFact` forever" | `deliverFact` `relay.go:82-118` routes `isPermanentDeliveryError(err)` → `failFact` `:87-93`; `failFact` `:120-132` → `FailAuditGovernance` (terminal, no retry); `retryFact` `:134-148` → `RetryAuditGovernance` with `boundedBackoff`; closed list `isPermanentDeliveryError` `:212-221` = `{ErrReceiptConflict, ErrInvalidReceipt, httpStatusError{409}, httpStatusError{422}}` | ⚠️ **problem statement outdated; direction already implemented.** `ErrInvalidReceipt` and HTTP 409/422 are terminal since the closed-list classifier shipped; only `ErrInvalidEvent` (plus other statuses/transport) remains transient — by design (D2). |
| E2 | `http.go:validateReceipt` — receipt validation classes | `validateReceipt` `http.go:178-212`: non-202 → `&httpStatusError{Status}` `:180-184`; non-JSON content-type / oversized / unparseable body → `ErrInvalidReceipt` `:186-194`; `conflict:true` → `ErrReceiptConflict` `:196-202`; `receiptMatches` `:214-225` (event_id **and tenant_id** mismatch or zero `accepted_at`, or status ∉ {ledgered,indexed,archived} → `ErrInvalidReceipt`) | ✅ **exact.** Tenant-mismatch is a branch of `ErrInvalidReceipt` (`receiptMatches` `:218`, `TenantID != fact.TenantID`), not a separate sentinel. |
| E3 | `model.go:httpStatusError` | `type httpStatusError` `model.go:31-37` (`Status int`, `Error()`) | ✅ **exact.** |
| E4 | migration `0042_audit_governance_terminal_failed.up.sql` — "failed_at_ns only, no status/dead_at, no partial index" | `migrations/{sqlite,postgres}/0042_audit_governance_terminal_failed.up.sql`: `ALTER TABLE … ADD COLUMN failed_at_ns … DEFAULT 0` only; no `CREATE INDEX` (pinned by `TestAuditGovernance0043DeviationHeaderPinned`) | ✅ **holds** — and is the deviation D1 baseline. |
| E5 | "a new 0043 pair is needed (I2: 0039/0042 applied)" | `migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` **already exist** (embedded via `migrationsFS`): `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns=0 AND failed_at_ns=0` + `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns=0 AND failed_at_ns=0`; down = two reversible `DROP INDEX IF EXISTS` | ⚠️ **superseded** — the 0043 pair already shipped (this direction's earlier campaign). Partial-index requirement done; `status`/`dead_at` deliberately not added (D1). |
| E6 | `audit_governance_claim.go:claimAuditGovernancePostgres/claimAuditGovernanceSQLite/claimAuditGovernanceIDs/OldestPendingAuditGovernance` — "failed_at_ns=0 verified" | Postgres claim `:31-49` (`WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`); SQLite claim inner select `:51-80` (same predicate); `claimAuditGovernanceIDs` `:81-109` (re-checks `failed_at_ns=0` on the fenced UPDATE); `OldestPendingAuditGovernance` `:188-201` (`MIN(o.created_at_ns) … WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`); additionally `HasPendingDrainingAuditGovernance` `:202-210` (same predicate + `b.state='draining'`) | ✅ **exact** — dead rows excluded from claim, lag, and drain-pending. |
| E7 | `audit_governance_types.go:FailAuditGovernance` | Interface method declared `audit_governance_types.go:92-95` ("never re-claimed, never re-POSTed, retained until prune"); implementation `audit_governance_claim.go:159-186` (`SET failed_at_ns=$1, …` fenced by owner+token+live lease; truncates `last_error` to 512) | ✅ **exact** (impl lives in claim file; type file holds the contract comment — fine). |
| E8 | `config_audit_governance.go` — "MaxBackoffSeconds default 300" | `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` `config_audit_governance.go:65`; validation `MaxBackoffSeconds >= InitialBackoffSeconds` `:240` and `<= 86_400` `:250`; wired into the runtime as `maxBackoff` `runtime.go:95`; `boundedBackoff` caps at `maximum` `relay.go:174-190` | ✅ **exact.** |
| E9 | `audit_governance_due_idx` "lacks failed_at_ns" | `0039` both dialects: `CREATE INDEX audit_governance_due_idx ON audit_governance_outbox (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` — no `failed_at_ns` | ✅ **holds** — superseded for the pending paths by 0043's partial indexes (REQ-4). |
| E10 | contract "status/dead_at 列 + 部分索引 … 终态 ≤1 次尝试 … cap 300s … dead 行排除出 claim/lag" | `docs/campaigns/implementation-gate.md:21` item 1 (quoted in header); deviation documented in `0043` header ("Deviation note (contract implementation-gate.md:21 item 1)… 0042 shipped failed_at_ns… deviation documented, not renamed (zero-behavior rename; I2)") | ✅ **present** — the deviation is an explicit, in-file governance record, pinned by test (REQ-4 AC-4.3). |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Only `ErrReceiptConflict` lands terminal (relay.go deliverFact → failFact)" | ❌ **outdated** — `isPermanentDeliveryError` (`relay.go:212-221`) also lands `ErrInvalidReceipt` and HTTP 409/422 (E1); closed-list-pinned by `TestIsPermanentDeliveryErrorClosedList`. |
| "HTTP 409/422, `ErrInvalidReceipt`, `ErrInvalidEvent` re-POSTed forever with bounded backoff (300s cap)" | ⚠️ **partially outdated** — 409/422 + `ErrInvalidReceipt` are now terminal; `ErrInvalidEvent` remains transient **by pinned design** (D2). 300s cap verified (E8). No attempt/age dead-letter bound exists (D3) — consistent with the acceptance, which requires classification-based terminality only. |
| "Migration 0042 added only `failed_at_ns`; contract requires status/dead_at + partial index; new 0043 pair needed" | ⚠️ **superseded** — 0042 as cited (E4); 0043 partial-index pair shipped in both dialects (E5); `status`/`dead_at` deliberately deviated with an in-file governance record (E10, D1). |
| "Dead-row exclusion from claim/lag functionally satisfied via `failed_at_ns=0`" | ✅ **holds** — E6, including `HasPendingDrainingAuditGovernance`. |
| "`audit_governance_due_idx` lacks `failed_at_ns`" | ✅ **holds** — E9; the 0043 partial indexes are what serve the pending predicate now. |

**Test-run evidence (this checkout):**

```
go test ./internal/auditgovernance/ ./internal/repository/ \
  -run 'Terminal|Permanent|Backoff|FailedFact|PendingIndex|Deviation|ConflictFail' -count=1
→ ok  github.com/aero-vault/aero-vault/internal/auditgovernance  3.947s
→ ok  github.com/aero-vault/aero-vault/internal/repository       2.713s
```

---

## 3. Requirements

### REQ-1 — Permanent delivery classes land terminal within ≤1 attempt

The relay MUST classify exactly `{ErrReceiptConflict, ErrInvalidReceipt, HTTP 409, HTTP 422}` as permanent; a permanent fact MUST be failed (`failed_at_ns>0`) and its immutable origin MUST receive a durable rejection tombstone, MUST be POSTed exactly once (attempt ≤1), and MUST never be re-claimed or re-POSTed. Wrapped sentinels MUST classify identically (`errors.Is`/`errors.As`). Window-terminal transient facts remain recoverable by gap reconciliation.

- **AC-1.1 (closed list, both directions).** `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:199`): the four permanent errors and their wrapped forms return true; the transient set — `httpStatusError` 400/401/403/404/410/429/500/501/503, `ErrInvalidEvent`, `ErrTokenUnavailable`, bare transport error, `context.DeadlineExceeded` — returns false. *Existing.*
- **AC-1.2 (e2e terminal table).** `TestRuntimePermanentDeliveryErrorsAreTerminal` (`relay_terminal_test.go:35`): five sink rows — `http409`, `http422`, `tenant-mismatch` (202 + receipt with wrong `tenant_id`), `non-ledgered-status` (202 + `status:"rejected"`), `unparseable-body` (202 + non-JSON) — each asserts: exactly 1 POST within an observe window exceeding the harness max backoff (2s), then after `Close`: `ClaimAuditGovernance` returns 0 rows, `OldestPendingAuditGovernance` reports none (`assertTerminalState` `:126-146`); the 409 case additionally asserts the retention prune (`assertTerminalRetention` `:148-164`). *Existing.*
- **AC-1.3 (attempt ≤1 at the repository level).** `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`audit_governance_pending_idx_test.go:210`): claim increments `attempts` once, `FailAuditGovernance` is the sole writer of `failed_at_ns`, both land on the same row → `failed_at_ns > 0` AND `attempts == 1` read back. *Existing.*
- **AC-1.4 (permanent origin is not resurrected).** `TestAuditGovernancePermanentRejectionTombstonesOrigin` (`audit_governance_rejected_origin_test.go`): a permanent rejection remains invisible to gap reconciliation while retained, stays invisible after two cleanup cycles, and a direct re-enqueue is a no-op; the existing deterministic-ID prune test continues to pin recovery for window-terminal transient rows.

### REQ-2 — Transient classes keep retrying with backoff capped ≤300s

Every error outside REQ-1's closed list MUST be rescheduled via `RetryAuditGovernance` with `boundedBackoff`; the delay MUST be deterministic per fact ID and MUST never exceed the configured cap (default 300s).

- **AC-2.1 (transient classification).** `TestIsPermanentDeliveryErrorClosedList` transient half (AC-1.1). *Existing.*
- **AC-2.2 (cap + determinism).** `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`): for `attempts=20`, `initial=1s`, identical fact ID yields identical delay in `(200s, 300s]` when `maximum=300s` — pins the 300s default cap (`config_audit_governance.go:65`), not merely "some cap". *Existing.*
- **AC-2.3 (e2e transient 5xx).** `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`): a 500-sink fact keeps being rescheduled — `audit_governance_relay_failed_total` delta ≥1 (i.e., `RetryAuditGovernance` executed, `available_at_ns` advanced) while `audit_governance_relay_dead_total` delta == 1 counts only the terminal conflict fact and `delivered` delta == 1 only the success fact. *Existing.*
- **AC-2.4 (e2e re-POST count — **new, was the one verification gap; now implemented**).** `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (`relay_terminal_test.go`): a 500-sink fact is POSTed **more than once** (≥ 2 posts over ≥ 2 backoff windows with the runtime still running) and the inter-POST gaps strictly grow — the deterministic proxy for `available_at_ns` strictly increasing between retries (at harness config 1s→2s ±25% jitter, gap₁∈[0.75,1.25]s < gap₂∈[1.5,2.0]s, so the assertion holds for every fact ID). *Implemented.*

### REQ-3 — Dead rows excluded from claim/lag/drain-pending (T-3 lock)

A row with `failed_at_ns > 0` MUST be invisible to `ClaimAuditGovernance`, `OldestPendingAuditGovernance`, and `HasPendingDrainingAuditGovernance`; a failed row MUST never reappear in a later claim, and `FailAuditGovernance` MUST be fenced by the claim identity (owner+token+live lease).

- **AC-3.1 (SQL predicate).** All three claim queries and both pending queries carry `failed_at_ns=0` (`audit_governance_claim.go:37,62,88,194,207`). *Verified statically.*
- **AC-3.2 (repository lock test).** `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:419`): after window-terminal `FailAuditGovernance`, a fresh claim returns 0 rows and `OldestPendingAuditGovernance` reports none; a stale owner/token cannot fail the fact (fencing); the failed row is not pruned before the window and is pruned after; the origin is re-enqueueable post-prune. Permanent-origin exclusion is pinned separately by AC-1.4. *Existing.*
- **AC-3.3 (runtime lock).** `assertTerminalState` (`relay_terminal_test.go:126-146`) runs the same two probes through the live store after `Close` for every permanent class. *Existing.*

### REQ-4 — 0043 migration pair (sqlite + postgres): pending partial index

Migration 0043 MUST exist in both dialects and MUST add a partial index whose predicate is exactly the pending predicate `delivered_at_ns=0 AND failed_at_ns=0`, serving (a) the claim path's range + ORDER BY and (b) the lag `MIN` path. The `status`/`dead_at` columns prescribed by the contract are **not** added — replaced by `failed_at_ns` with an in-file documented deviation (D1). (This requirement is **already shipped**; ACs are lock tests.)

- **AC-4.1 (files).** `migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` exist and are embedded; up creates `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id) WHERE delivered_at_ns=0 AND failed_at_ns=0` and `audit_governance_pending_lag_idx (created_at_ns) WHERE delivered_at_ns=0 AND failed_at_ns=0`; down is reversible (`DROP INDEX IF EXISTS` ×2). *Verified statically.*
- **AC-4.2 (plans).** `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (`audit_governance_pending_idx_test.go:177`): the exact SQLite claim inner-select shape uses `audit_governance_pending_claim_idx` with no full-table scan (`SCAN o` absent); the isolated lag `MIN` probe uses `audit_governance_pending_lag_idx`. *Existing.*
- **AC-4.3 (deviation pinned, not re-shipped).** `TestAuditGovernance0043DeviationHeaderPinned` (`audit_governance_pending_idx_test.go:251`): 0043 headers (both dialects) contain `failed_at_ns`, `status`, `dead_at`, `implementation-gate` (the deviation is documented, referencing the contract row); 0042 files contain `failed_at_ns` and **no** `CREATE INDEX` (the index was not smuggled into 0042). *Existing.*

### REQ-5 — Terminal-with-retention prune bounded by `DeliveredRetentionSeconds`

Failed rows MUST be retained for diagnosis until the retention window and then pruned by `CleanupFailedAuditGovernance`; the window MUST be the delivered-retention window (default 7d). Permanent rejection tombstones survive that row cleanup; window-terminal transient rows do not create tombstones and may resurface for recovery.

- **AC-5.1 (wiring).** `retention: time.Duration(cfg.DeliveredRetentionSeconds) * time.Second` (`runtime.go:97`); `cleanupDelivered` calls `CleanupFailedAuditGovernance(ctx, now.Add(-r.retention), …)` on the same cadence (`relay.go:150-172`); `CleanupFailedAuditGovernance` deletes `failed_at_ns>0 AND failed_at_ns <= cutoff` (`audit_governance_cleanup.go:113-135`). *Verified statically.*
- **AC-5.2 (early/late prune).** `assertTerminalRetention` (`relay_terminal_test.go:148-164`, exercised by the `http409` row) and `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:419`): cleanup with `now.Add(-1h)` deletes 0 rows; cleanup with `now.Add(+1h)` deletes exactly 1. *Existing.*

---

## 4. Decisions (verified, governing — not implementation gaps)

| # | Decision | Evidence | Rationale |
|---|---|---|---|
| D1 | **No `status`/`dead_at` columns.** `failed_at_ns` (0042) is the terminal marker; the 0043 header documents the deviation from `implementation-gate.md:21` ("Deviation note … zero-behavior rename; I2") and `TestAuditGovernance0043DeviationHeaderPinned` pins the documentation | E4, E5, E10, AC-4.3 | 0039 is a timestamp-led schema; claim/lag/drain/cleanup all already predicate on `failed_at_ns`; adding status/dead_at would be a zero-behavior rename of an applied schema (I2 forbids editing applied files; a 0044 rename is pure churn with no behavioral delta). |
| D2 | **`ErrInvalidEvent` stays transient.** Closed list excludes it; `TestIsPermanentDeliveryErrorClosedList` pins it transient | E1, AC-1.1 | It is a local-construction error (`Publish` `http.go:101-113`: invalid outbound fact, missing binding, marshal failure), not a receiver rejection; the direction's *acceptance* never required it terminal (only 409/422/tenant-mismatch/invalid receipt). Changing it would expand scope. |
| D3 | **No attempt/age dead-letter bound.** Terminality is error-class-based only; there is deliberately no max-attempts/max-age dead-letter | E1, E8 | The acceptance requires classification-based terminality ≤1 attempt for permanent classes and capped backoff for transient ones — both satisfied; an additional bound is out of scope. |
| D4 | **Module label.** The analysis filename says `internal-antivirus`; the direction is entirely `internal/auditgovernance` + `internal/repository` (+ `internal/config`). `internal/antivirus/` is untouched | §1 | Filename artifact of the analysis campaign; no antivirus code is involved. |

## 5. Non-goals (explicitly excluded)

- **Deterministic fact IDs** (analysis direction 2 of the same file) — separate direction; `facts.go`/`audit_governance_factid.go` work is out of scope here.
- **Relay observability + `Ready()`/`BacklogAge` decoupling, 450s alert** (analysis direction 3) — separate direction; covered by `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md`.
- **`internal/antivirus` module** — no changes (see D4).
- **Adding `status`/`dead_at` columns** (D1), **`ErrInvalidEvent` terminality** (D2), **attempt/age dead-letter bounds** (D3).

## 6. Acceptance matrix (supplied checks → requirement → test)

| Supplied acceptance check | Requirement | Testable pin | Status |
|---|---|---|---|
| T-3: claim + `OldestPendingAuditGovernance` + `HasPendingDrainingAuditGovernance` exclude dead rows (`failed_at_ns=0`); lock: failed row never reappears in `ClaimAuditGovernance` | REQ-3 | AC-3.1 static SQL predicates; AC-3.2 `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`; AC-3.3 `assertTerminalState` | ✅ implemented & passing |
| New 0043 migration (sqlite+postgres): status/dead_at + partial index on pending predicate | REQ-4 | AC-4.1 files; AC-4.2 `TestAuditGovernancePendingIndexesServeClaimAndLagPlans`; AC-4.3 `TestAuditGovernance0043DeviationHeaderPinned` | ✅ partial index shipped; **status/dead_at deliberately deviated (D1), deviation documented + pinned** |
| Classification test: POST 409/422/tenant-mismatch/invalid receipt → terminal within attempt ≤1 (`failed_at_ns` set, never re-claimed) | REQ-1 | AC-1.2 `TestRuntimePermanentDeliveryErrorsAreTerminal` (exactly 1 POST, never re-claimed, absent from lag); AC-1.3 `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`attempts==1`) | ✅ implemented & passing |
| Transient 5xx/network rows keep retrying with backoff cap ≤300s | REQ-2 | AC-2.1 closed list; AC-2.2 `TestBoundedBackoffIsDeterministicAndCapped`; AC-2.3 500-sink reschedule via `failed` counter; **AC-2.4 `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (posts ≥ 2 + growing gaps)** | ✅ implemented & passing |
| Prune path `CleanupFailedAuditGovernance` bounded by `DeliveredRetentionSeconds` | REQ-5 | AC-5.1 wiring; AC-5.2 early/late prune assertions (runtime + repo) | ✅ implemented & passing |
| Permanent rejection tombstone survives failed-row cleanup and blocks gap re-enqueue | REQ-1/REQ-5 | AC-1.4 `TestAuditGovernancePermanentRejectionTombstonesOrigin` | ✅ implemented & passing |

**Remaining work from the supplied acceptance:** none — AC-1.4 and AC-2.4 are implemented and passing with the full suite. The only prescription intentionally not implemented is `status`/`dead_at` (D1), which is documented in-migration and pinned by test rather than silently dropped.
