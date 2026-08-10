# Design — `internal/auditgovernance`: complete permanent-error classification (422/409/tenant mismatch/invalid receipt → terminal ≤1 attempt) + pending partial index + cumulative-window bound

**Module:** `internal/auditgovernance` (+ `internal/repository` — `audit_governance_*` tables/queries/migrations; `internal/config`)
**Spec:** `docs/requirements/internal-auditgovernance-terminal-classification-ef1a62fa-v1.spec.md` (REQ-1..6, 17 ACs, D1..D3)
**Source analysis:** `docs/auto/analyses/internal-auditgovernance-ef1a62fa.json` direction 2 (1-based) / B3.1 / T-3
**Contract:** `docs/campaigns/implementation-gate.md:21` row 1 (aero-vault): "死信终态（F3）：status/dead_at 列 + 部分索引；移植 sink `DeliveryError.Permanent` 分类（422/409/tenant mismatch/无效回执 → 终态 ≤1 次尝试）；瞬态有界重试 cap 300s；dead 行排除出 claim/lag. T-3：422 → 一个周期内终态"
**Sibling designs:** `internal-auditgovernance-terminal-classification-v1.design.md` (earlier spec of the same direction, pre-0044; its only open delta — AC-2.4 re-POST count — is closed here by `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff`) · `internal-ai-audit-governance-permanent-error-terminal-classification-v1.design.md` (competing delta — flips `ErrInvalidEvent` to permanent; **not adopted**, D2) · deterministic-fact-ID designs (direction 1 of the same analysis; WIP hunks share the worktree — §7)
**Date:** 2026-08-08 · **HEAD:** `15763e2` · **Worktree:** dirty — implementation under design exists only uncommitted (§0)

---

## 0. Baseline caveat (verified, not trusted)

The evidence (spec summary) claims every supplied check is "implemented and passing" and the only remaining work is committing the WIP. **Verified true on substance, with two caveats:**

| State | What exists |
|---|---|
| HEAD `15763e2` | **No terminal classification.** `git show HEAD:internal/auditgovernance/relay.go` → 0 matches for `isPermanentDeliveryError\|failFact\|ErrReceiptConflict\|cumulativeWindowExceeded`; every publish error goes to `retryFact`. 0042 `failed_at_ns` + claim/fail/retry/complete repository paths + `AuditGovernanceStore` interface + relay counters **are committed**. |
| Worktree (this checkout) | The full direction is implemented but **uncommitted**: classifier/failFact/cumulative window (`relay.go`), conflict-receipt branch + `ErrReceiptConflict` (`http.go`/`model.go`), claim anchor `CASE WHEN first_attempt_at_ns=0` (`audit_governance_claim.go`), migrations **0043** + **0044** (both dialects, untracked), test pins (`relay_terminal_test.go`, `cumulative_window_test.go`, `audit_governance_pending_idx_test.go`, receipt hunks in `http_test.go`), and docs (`docs/configuration.md:273`, `CHANGELOG.md:10-16`). |
| Worktree (sibling direction 1) | Deterministic-fact-ID hunks are **interleaved in the same dirty tree**: `facts.go`, `redaction.go` (control-char rejection), `types.go` `SourceID`, `http_test.go` `TestTenantSourceIDRejectsControlChars`, `EnqueueAuditGovernance` ID recompute. These belong to the other direction's spec — see §7 attribution. |

**Consequences:**
1. This design is a **delta**: zero production code changes remain. The deliverable is a landing procedure + verified acceptance mapping, not new implementation.
2. The merge gate is commit attribution: this direction's hunks must not be committed under the sibling's message and vice-versa (§7).
3. All line numbers below are current-worktree; the spec's numbers are substantively correct (one cosmetic drift noted E11).

---

## 1. Verification register (evidence claims re-checked, not trusted)

| # | Evidence claim | Verdict (this checkout) |
|---|---|---|
| E1 | `deliverFact` routes closed-list `isPermanentDeliveryError` → `failFact`; list = `{ErrReceiptConflict, ErrInvalidReceipt, HTTP 409, HTTP 422}` via `errors.Is`/`errors.As` | ✅ **exact** — `relay.go:82-118` (permanent branch `:87` → `failFact`, transient → `retryFact` `:101`); `isPermanentDeliveryError` `relay.go:255-265`; `classifyRelayError` `:232-240` cross-referenced in comments |
| E2 | `validateReceipt` `http.go:178-212`: non-202 → `httpStatusError`; non-JSON/oversized/unparseable → `ErrInvalidReceipt`; `conflict:true` → `ErrReceiptConflict`; `receiptMatches` `:214-225` — tenant mismatch is the `TenantID` branch of `ErrInvalidReceipt` | ✅ **exact** — conflict branch `:199-202`; `receiptMatches` event/tenant/accepted-at/status predicate; `TenantID != fact.TenantID` at `:217`; e2e-driven by the `tenant-mismatch` harness row |
| E3 | `boundedBackoff` per-attempt cap only; total-time bound now closed by 0044 anchor + `cumulativeWindowExceeded` (`relay.go:145`) | ✅ **exact** — `boundedBackoff` `:209-217` + pure `boundedBackoffDelay` `:219-232` (deterministic ±25 % per-ID jitter, cap `maximum`, floor `initial/2`); `cumulativeWindowExceeded` `:145-147` checked first in `retryFact` `:153-157` → `failFact` |
| E4 | 0039 plain `audit_governance_due_idx`, no partial predicate | ✅ **holds** — committed file read; superseded for pending paths by 0043 |
| E5 | 0042 adds only `failed_at_ns`, no index | ✅ **holds** — committed; pinned by `TestAuditGovernance0043DeviationHeaderPinned` |
| E6 | 0043 partial-index pair, both dialects, exact predicate `delivered_at_ns=0 AND failed_at_ns=0`, reversible down, header documents D1 deviation | ✅ **exact** — all four files read; `pending_claim_idx (available_at_ns, created_at_ns, id)` + `pending_lag_idx (created_at_ns)`; down = `DROP INDEX IF EXISTS` ×2; deviation note cites `implementation-gate.md:21` |
| E7 | 0044 `first_attempt_at_ns` anchor, both dialects, set once inside fenced claim | ✅ **exact** — up = `ADD COLUMN … NOT NULL DEFAULT 0` (deliberately unindexed), down = `DROP COLUMN`; claim anchor `CASE WHEN first_attempt_at_ns=0` at `audit_governance_claim.go:51` (claim) and `:109` (fenced UPDATE); RETURNING lists `:12-18` |
| E8 | `failed_at_ns=0` filters at claim.go `:54,:78,:110,:169,:191,:218,:230` | ✅ **exact** — grep-verified all seven sites (claim join, SQLite inner select, fenced UPDATE, retry, fail, lag MIN, drain EXISTS) |
| E9 | `CleanupFailedAuditGovernance` prunes terminal rows after retention | ✅ **exact** — `audit_governance_cleanup.go:113-135`, `failed_at_ns>0 AND failed_at_ns <= cutoff`, PG `FOR UPDATE SKIP LOCKED` / SQLite batch; called from `cleanupDelivered` `relay.go:185-207` on the delivered-retention cadence |
| E10 | Config: default 300 s (`config_audit_governance.go:65`), validation `>= InitialBackoffSeconds` / `<= 86_400` | ✅ **exact plus one WIP addition** — validation now also enforces window floor `MaxBackoffSeconds >= 2` (`:247-249`); default and upper cap unchanged; wired `runtime.go:96` (`maxBackoff`) / `:98` (`retention` = `DeliveredRetentionSeconds`, default 604800) |
| E11 | All 17 AC test pins exist at cited locations | ✅ **all present** — names and files verified (12/12 greps hit); cosmetic drift only: `assertTerminalState` is at `relay_terminal_test.go:117` (spec cites :126), `assertTerminalRetention` at `:134` (spec :148); substance identical |
| E12 | `go test ./internal/auditgovernance/ ./internal/repository/ -count=1` → ok (30.9 s / 32.0 s) | ✅ **reproduced** — `ok` 31.675 s / 33.838 s; additionally `gofmt -l` clean and `go vet` clean on both packages |
| E13 | "Remaining work: none — commit the WIP" | ✅ **holds for code** — no AC gap found (the earlier spec's AC-2.4 gap is closed: `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` `relay_terminal_test.go:245` asserts posts ≥ 2 AND ≥ 2 strictly growing inter-POST gaps AND post-window dead-row probes). Remaining work is commit attribution (§7) |

Net: **the evidence is trustworthy on substance; every design-relevant fact reproduces.** The design below therefore documents as-built API surface, failure modes, migration steps, and the landing gate rather than new code.

---

## 2. API changes (as-built vs. HEAD `15763e2`)

### 2.1 Public surface (package `internal/auditgovernance`)

| Symbol | Change | Kind |
|---|---|---|
| `ErrReceiptConflict` | **New exported sentinel** (`model.go:27`): receiver answered `{conflict:true}` — terminal-with-retention, distinct from `ErrInvalidReceipt` | additive |
| `validateReceipt` | New branch: `conflict:true` → `ErrReceiptConflict` (`http.go:199-202`); previously `conflict:true` fell into `receiptMatches` and surfaced as `ErrInvalidReceipt` | behavior change (both terminal — wire-visible only via `last_error` text) |
| `receiptMatches` | `Conflict` removed from the predicate (`http.go:214-225`); `Duplicate` deliberately ignored (contract A: idempotent re-POST `{duplicate:true, conflict:false, status:ledgered}` must complete like a first POST) | behavior change (acceptance widening) |
| `Runtime.deliverFact` | Permanent classes → `failFact` (new, unexported); transient → `retryFact`; nil → `CompleteAuditGovernance` | behavior change |
| `Runtime.retryFact` | New first check `cumulativeWindowExceeded` → `failFact` before scheduling retry; `onRetry` observation hook (test-only, nil in prod) | behavior change |
| `Runtime.cleanupDelivered` | Now also calls `CleanupFailedAuditGovernance` on the delivered-retention cadence | behavior change (prune added) |
| `isPermanentDeliveryError`, `failFact`, `cumulativeWindowExceeded`, `boundedBackoffDelay` | New unexported functions (`relay.go`) — pure/testable, no interface change | additive |

### 2.2 Repository surface (`internal/repository`)

| Symbol | Change |
|---|---|
| `AuditGovernanceFact.FirstAttemptAt` | **New field** (read-back only; never written by callers; zero = never claimed / pre-0044) — `audit_governance_types.go:61-66` |
| `AuditGovernanceStore` interface | **No signature changes** — `Claim/Retry/Complete/Fail/Cleanup` were already committed at HEAD; only `FailAuditGovernance`'s effect is newly reachable |
| claim SQL | Anchor write `first_attempt_at_ns=CASE WHEN first_attempt_at_ns=0 THEN $4 ELSE first_attempt_at_ns END` inside both fenced claim statements; `$4` is a **new dedicated placeholder** (I1 — no reuse) |
| Migrations | **0043** (2 partial indexes, both dialects) and **0044** (`first_attempt_at_ns` column, both dialects) — §5 |
| `EnqueueAuditGovernance` | **Unchanged by this direction** (its worktree ID-recompute hunks belong to direction 1 — §7) |

### 2.3 Config / telemetry / docs

- `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS`: default 300 unchanged; validation floor tightened from `>= InitialBackoffSeconds` to `>= 2 AND >= InitialBackoffSeconds` (`config_audit_governance.go:247-249`, upper `<= 86_400` unchanged) — **only breaking change in the whole delta**, and only for configs with cap < 2 s.
- Telemetry: no new counters in this direction (relay counters landed with B3-2); `failFact` uses existing `IncAuditGovernanceRelayDead`, retry window-terminalization counts `dead_total` never `failed_total` (`relay.go:153-157` comment).
- Docs already updated in worktree: `docs/configuration.md:273` (cap = per-attempt **and** cumulative window), `CHANGELOG.md:10-16` (behavior change for stuck transient receivers: dead-letter after window instead of retry-forever).

**No HTTP/REST/CLI/S3/MCP surface changes; no new env vars; no new dependencies.**

---

## 3. Compatibility constraints

| # | Constraint | Governing rule |
|---|---|---|
| C1 | **Migration files are append-only.** 0039/0042 are applied in every deployed DB; neither may be edited. The D1 deviation (`failed_at_ns` replaces the contracted `status`/`dead_at` — a zero-behavior rename) lives **in the 0043 header** and is test-pinned (`TestAuditGovernance0043DeviationHeaderPinned`), never re-shipped as a rename migration | I2 |
| C2 | **Placeholder discipline in the anchor.** `$4` is appended in textual SET-before-WHERE order with its own argument; rebind rewrites by text order and ignores numeric values (I1). The claim.go comment documents the positional requirement | I1 |
| C3 | **Upgrade is row-safe.** Pre-0044 rows read `first_attempt_at_ns=0` → never window-terminal until their first post-upgrade claim sets the anchor (`CASE WHEN first_attempt_at_ns=0`); a zero anchor and negative elapsed (DB clock ahead) are never terminal (safe direction) | E3, AC-6.1 |
| C4 | **Config floor is a deliberate break.** Deployments with `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS < 2` (previously valid if ≥ initial) now fail boot validation. Default (300) and all in-range values unaffected; `docs/configuration.md` documents `2..86400` | E10 |
| C5 | **Dead-row exclusion composes with B3-2 `Ready()`.** `OldestPendingAuditGovernance`/`HasPendingDrainingAuditGovernance` already filter `failed_at_ns=0` (E8) → a terminal fact clears `BacklogAge`; no change to `/readyz` semantics | E8 |
| C6 | **D2/D3 scope guard.** `ErrInvalidEvent`/`ErrTokenUnavailable` stay transient (a local-construction error and a refreshable token, not receiver rejections); no attempt-count dead-letter (time-window only). The competing sibling design that flips `ErrInvalidEvent` to permanent is **not adopted** | D2/D3 |
| C7 | **Cross-direction dedupe interaction.** Terminal rows are retained (7 d default); `EnqueueAuditGovernance` dedupes `ON CONFLICT (origin_kind, origin_id) DO NOTHING` — while a failed row is retained, a reconcile re-enqueue of the same origin is a no-op; after the prune, a fresh fact is enqueued (documented recovery path, `audit_governance_cleanup.go` header) | §4 FM-6 |
| C8 | **SQLite floor.** 0044 `DROP COLUMN` requires SQLite ≥ 3.35 — satisfied by modernc v1.50.1 (SQLite 3.53.1, the version the EXPLAIN plan pins target) | AC-4.2 |
| C9 | **CLI/API stability.** `audit_governance` CLI commands, `/v1` endpoints, and the `Store` interface keep their signatures; only observable behavior (dead-lettering) changes | §2 |

---

## 4. Failure modes

| # | Failure mode | Behavior | Mitigation / pin |
|---|---|---|---|
| FM-1 | Classifier drift (new receiver rejection class added without updating the closed list) | Wrong class retried forever until cumulative window (late terminality) or dead-lettered too early (lost delivery) | Closed-list unit test pins **both** directions (`TestIsPermanentDeliveryErrorClosedList`); `classifyRelayError` cross-ref comment; the list is deliberately a single site (`relay.go:255-265`) |
| FM-2 | `failFact` store write fails (DB down) | Warn + return; row stays claimed until lease expiry, then re-claimed and **re-POSTed** (attempt ≥ 2); receiver answers the same permanent error idempotently → terminal on the retry. `attempts==1` is a normal-operation guarantee, not a store-failure guarantee | `failFact` `relay.go:120-137` (warn, never in-loop retry — lease re-claim is the recovery); AC-1.3 pins `attempts==1` under healthy store |
| FM-3 | Clock skew (DB ahead of relay at claim) | Anchor = DB-now > relay-now → negative elapsed → `now.Sub(firstAttempt) > window` false → **never** window-terminal (safe direction); repeated claims extend the negative window only until DB-now catches up | `cumulativeWindowExceeded` zero/negative guards; AC-6.1 boundary + monotone tests; config floor ≥ 2 s keeps margin |
| FM-4 | Multi-worker race (stale worker with expired lease computes fail while current worker computes retry, or vice versa) | Both compute the same direction because the decision is a pure function of `(firstAttempt, now, window)` and monotone in now; the fenced writes (`owner+token+live lease`) then land **at most one outcome** | AC-6.3 `TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome`; fenced UPDATE predicates (`claim.go:169,:191`) |
| FM-5 | Ack-lost (receiver ledgers, `CompleteAuditGovernance` fails) | Row re-claimed → re-POSTed → receiver answers `{duplicate:true, conflict:false, status:ledgered}` → accepted (contract A: `Duplicate` intentionally absent from the acceptance predicate) → complete. At-least-once preserved | `receiptMatches` comment + `TestReceiptDuplicateSemanticsContract` |
| FM-6 | Reconcile re-enqueues an origin whose fact is terminal-failed (gap still open) | Dedupe `DO NOTHING` while the failed row is retained; after the retention prune, a fresh fact re-attempts — the documented post-prune recovery path | C7; `CleanupFailedAuditGovernance` header note |
| FM-7 | Retention prune races diagnosis (operator needs the failed row after 7 d) | Window is `DeliveredRetentionSeconds` (default 604800 s, config `>= 3600`); prune only touches `failed_at_ns > 0` rows older than the cutoff, batched (`LIMIT`, PG `SKIP LOCKED`) — pending rows can never be pruned | E9, AC-5.2; `docs/configuration.md` |
| FM-8 | 0043 indexes not selected (plan regression after SQLite/PG upgrade) | Claim/lag queries degrade to full scans over retained dead rows — the exact problem the direction fixes | AC-4.2 EXPLAIN pins (SQLite, modernc v1.50.1); PG parity covered by `internal/integration/audit_governance_postgres_test.go`; seed shape (55k history + 300 pending) documented in the test |
| FM-9 | Manual 0044 down (I2 rollback) | Anchors dropped → behavior returns to retry-forever; already-failed rows remain `failed_at_ns>0`; never re-claimed | Down file header documents the exact restoration |
| FM-10 | Window boundary off-by-one | `==` stays transient (fact terminalizes only when `now - firstAttempt > window`) — prevents accidental terminalization exactly at the cap under jitter | AC-6.1 boundary test |
| FM-11 | Misconfigured binding emits invalid events (`ErrInvalidEvent`) or token outage (`ErrTokenUnavailable`) | Stays transient (D2): stream retries with growing backoff until the cumulative window, then dead-letters; operators see `failed_total` growth + the B3-2 450 s alert rather than silent loss | AC-1.1 transient half; D2 rationale in spec §4 |
| FM-12 | `retryFact` window-terminalization counters | Window-terminalized stream counts `dead_total`, never `failed_total` (window check precedes the failed counter) — counters stay class-meaningful for alerting | `relay.go:153-157` comment; `TestRuntimeRelayCountersTrackDeliveryOutcomes` |

---

## 5. Migration steps

Migration pair 0043 then 0044, both dialects, embedded via `//go:embed`, auto-applied in version order by `repo.Migrate` at startup (I2: skip-applied by version, no checksum, `.down.sql` never auto-run).

**0043 `_up` (sqlite + postgres):**
```sql
CREATE INDEX audit_governance_pending_claim_idx ON audit_governance_outbox
  (available_at_ns, created_at_ns, id)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;
CREATE INDEX audit_governance_pending_lag_idx ON audit_governance_outbox
  (created_at_ns)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;
```
- Claim path: `(available_at_ns, created_at_ns, id)` serves the range predicate **and** the ORDER BY in index order (no temp sort); `lease_expires_at_ns` stays a residual filter.
- Lag path: `(created_at_ns)` serves the `MIN(created_at_ns)` probe in `OldestPendingAuditGovernance`.
- `_down`: `DROP INDEX IF EXISTS` ×2 (reversible, replay-safe, 0036 convention).

**0044 `_up` (sqlite + postgres):**
```sql
ALTER TABLE audit_governance_outbox
  ADD COLUMN first_attempt_at_ns INTEGER NOT NULL DEFAULT 0;
```
- Deliberately **not indexed** (read via heap in the claim RETURNING; the 0043 partial indexes still serve the plan).
- `_down`: `ALTER TABLE … DROP COLUMN first_attempt_at_ns` (SQLite ≥ 3.35 — OK on modernc v1.50.1 / SQLite 3.53.1).

**Operational steps:**
1. Deploy binary with 0043 + 0044; `repo.Migrate` applies both at first startup. No backfill, no data rewrite, no lock window of note (index builds on the outbox table; batched by the migration runner).
2. Zero rows touched: pending rows keep `first_attempt_at_ns=0` until their next claim, which sets the anchor exactly once (`CASE WHEN first_attempt_at_ns=0`, idempotent across lease re-claims, ack-lost re-claims, crash recovery).
3. Multi-replica: each instance runs the same idempotent upsert-checked migrations; anchor idempotency makes concurrent claims converge.
4. Rollback (manual, I2): run 0044 down then 0043 down; behavior returns to pre-window retry-forever with dead rows still excluded (0042 untouched).
5. Config check before rollout: any `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS < 2` in deployment manifests now fails validation (C4) — fix manifests first.

---

## 6. Testable acceptance mapping (T-3 supplied → REQ → pin → status)

| Supplied acceptance (T-3, analysis direction 2) | REQ | Pin (file:line, current worktree) | Status |
|---|---|---|---|
| Receipts returning **422, 409, malformed receipt, tenant-mismatch** → row lands `failed_at_ns>0` with `attempts<=1` and `last_error` set | REQ-1 | AC-1.1 `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:200`) — 4 permanent + wrapped forms true, 9 transient forms false; AC-1.2 `TestRuntimePermanentDeliveryErrorsAreTerminal` (`:36`, 5 harness rows: `http409`, `http422`, `tenant-mismatch`, `non-ledgered-status`, `unparseable-body`; exactly 1 POST per row within observe window > harness max backoff; `assertTerminalState` `:117` probes claim=0 + lag=none after Close); AC-1.3 `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`audit_governance_pending_idx_test.go:210`, `failed_at_ns>0 ∧ attempts==1`, `last_error` from `FailAuditGovernance` `claim.go:191`) | ✅ WIP, passing |
| Never re-claimed by `ClaimAuditGovernance`; excluded from `OldestPendingAuditGovernance` | REQ-3 | AC-3.1 static predicates `claim.go:54,78,110,169,191,218,230`; AC-3.2 `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`, fencing + early/late prune); AC-3.3 `assertTerminalState` for all permanent classes + window-terminal stream (`relay_terminal_test.go:306-310`) | ✅ WIP, passing |
| Transient errors (5xx/transport) keep retrying with bounded backoff **≤300 s** | REQ-2 | AC-2.1 closed-list transient half; AC-2.2 `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`, cap ∈ (200 s, 300 s] at attempts=20, deterministic per ID — committed); AC-2.3 `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (`relay_terminal_test.go:245`, posts ≥ 2, ≥ 2 strictly growing inter-POST gaps, then window-terminal dead-row probes); `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`, 500-sink reschedules via `failed_total`) | ✅ WIP, passing |
| Migration test: partial index `WHERE delivered_at_ns=0 AND failed_at_ns=0` exists | REQ-4 | AC-4.1 0043 up/down files both dialects (E6); AC-4.3 `TestAuditGovernance0043DeviationHeaderPinned` (`audit_governance_pending_idx_test.go:251`, 0042 carries no `CREATE INDEX`, 0043 headers document the deviation) | ✅ WIP, passing |
| `EXPLAIN QUERY PLAN` for claim/OldestPending selects the partial index | REQ-4 | AC-4.2 `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (`audit_governance_pending_idx_test.go:177`, 55k history + 300 pending seeds, claim → `audit_governance_pending_claim_idx` with no `SCAN o`; lag MIN → `audit_governance_pending_lag_idx`) | ✅ WIP, passing |
| (Problem statement) no total-time terminal bound | REQ-6 | AC-6.1 `TestCumulativeWindowExceededBoundary`/`TestCumulativeWindowDecisionMonotone` (`cumulative_window_test.go:35/:73`, `==` transient, zero/negative never terminal, monotone); AC-6.2 `TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow` (`:111`); AC-6.3 `TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome` (`:208`) | ✅ WIP, passing |
| (Retention, contract) terminal-with-retention prune | REQ-5 | AC-5.1 wiring (`runtime.go:98` retention = `DeliveredRetentionSeconds`; `cleanupDelivered` → `CleanupFailedAuditGovernance` `relay.go:196`; `audit_governance_cleanup.go:113-135`); AC-5.2 `assertTerminalRetention` (`relay_terminal_test.go:134`, −1 h → 0 deleted, +1 h → 1) + repo-level early/late prune | ✅ WIP, passing |

**Supplementary pins (beyond the 17 ACs, all green in the reproduced run):** `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117`, committed) · `TestReceiptConflictIsTerminalSentinel` + `TestReceiptDuplicateSemanticsContract` (`http_test.go`, WIP) · `TestAuditGovernanceFirstAttemptAnchorPersists` (`audit_governance_test.go:419`) · `TestAuditGovernanceCumulativeWindowEnvelope` / `TestAuditGovernanceMaxBackoffDefaultIsCumulativeWindow` (`config_audit_governance_test.go:24/:51`) · PG lease-recovery anchor parity (`internal/integration/audit_governance_postgres_test.go`).

**Gate evidence reproduced:** `go test ./internal/auditgovernance/ ./internal/repository/ -count=1` → `ok` (31.675 s / 33.838 s); `gofmt -l` clean; `go vet` clean.

---

## 7. Landing procedure (merge gate)

The spec's "nothing remains beyond committing the WIP" is accurate **only if hunks are attributed per direction** — the worktree interleaves direction 2 (this design) with direction 1 (deterministic fact IDs).

| File | Direction 2 hunks (commit A) | Direction 1 hunks (commit B) |
|---|---|---|
| `internal/auditgovernance/relay.go` | `isPermanentDeliveryError`, `failFact`, `cumulativeWindowExceeded`, retry window check, cleanup wiring | — |
| `internal/auditgovernance/http.go` | conflict branch + `ErrReceiptConflict`, `receiptMatches` predicate | — |
| `internal/auditgovernance/model.go` | `ErrReceiptConflict` sentinel + envelope docs | — |
| `internal/auditgovernance/http_test.go` | `TestReceiptConflictIsTerminalSentinel`, `TestReceiptDuplicateSemanticsContract` | `TestTenantSourceIDRejectsControlChars` |
| `internal/auditgovernance/facts.go` / `redaction.go` | — | all hunks |
| `internal/repository/audit_governance_claim.go` | `first_attempt_at_ns` anchor (SET/RETURNING/CASE WHEN) | — |
| `internal/repository/audit_governance_types.go` | `FirstAttemptAt` field | `SourceID` field |
| `internal/repository/audit_governance_cleanup.go` | `CleanupFailedAuditGovernance` | — |
| `migrations/{sqlite,postgres}/0043_*,0044_*` | all | — |
| `internal/auditgovernance/{relay_terminal,cumulative_window}_test.go`; `internal/repository/audit_governance_pending_idx_test.go` | all | — |
| `internal/config/config_audit_governance.go(+_test.go)` | window floor + envelope tests | — |
| `docs/configuration.md:273`, `docs/CHANGELOG.md:10-16` | cumulative-window entries | (fact-ID entries exist separately in the same worktree) |

**Sequence:**
1. Commit A (this direction) with the spec's 17 ACs green — entry criteria: `go test ./internal/auditgovernance/ ./internal/repository/ ./internal/config/ -count=1`, `gofmt -l` empty, `go vet` clean on the touched packages, then full `make check`.
2. Commit B (direction 1) referencing its own spec (`…deterministic-fact-ids…`), re-running the same gates.
3. If hunk-level splitting is impractical (`git add -p` conflicts), land **one commit** with both specs cited in the message and the per-direction AC mapping from each spec — never attribute direction-1 hunks to this direction's commit.

**Out of scope, unchanged:** `ErrInvalidEvent`/`ErrTokenUnavailable` terminality (D2), attempt-count dead-letter (D3), `status`/`dead_at` columns (D1 — documented deviation, test-pinned), Ready()/metrics surface (landed at `15763e2`).
