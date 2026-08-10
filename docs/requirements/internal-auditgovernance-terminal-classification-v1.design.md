# Design — `internal/auditgovernance`: complete terminal classification (409/422/invalid-receipt/tenant-mismatch → dead-letter, pending partial index, terminal-with-retention)

**Module:** `internal/auditgovernance` (+ `internal/repository` — `audit_governance_*` tables/queries; `internal/config`)
**Spec:** `docs/requirements/internal-auditgovernance-terminal-classification-v1.spec.md` (REQ-1..5, D1..D4)
**Contract:** `docs/campaigns/implementation-gate.md:21` (gate item 1 / T-3)
**Siblings:** `internal-ai-audit-governance-permanent-error-terminal-classification-v1.design.md` (competing delta — flips `ErrInvalidEvent` to permanent; **not adopted by this spec**, see §8 D2) · `cmd-server-…-ready-degraded-v1.design.md` (orthogonal: `Ready()` decoupling, direction 3 of the same analysis file)
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` · **Worktree:** dirty — the implementation under verification exists **only in the uncommitted worktree** (see §0; the spec's "HEAD `acfaaf4`" label is imprecise, substance unaffected)

---

## 0. Baseline caveat (verified, not trusted)

The evidence (spec summary) claims everything is "checked against HEAD `acfaaf4`". **The operative fact is stronger and different:**

| State | What exists |
|---|---|
| HEAD `acfaaf4` | **No terminal classification.** `git show HEAD:internal/auditgovernance/relay.go` → 0 matches for `isPermanentDeliveryError|failFact|ErrReceiptConflict`; `git ls-tree HEAD` → no 0042/0043 migrations. Every publish error goes to `retryFact`. |
| Worktree (this checkout) | The full direction is implemented but **uncommitted**: `relay.go`/`http.go`/`model.go`/`facts.go` modified (`M`), 0042 pair staged (`A`), 0043 pair + `relay_terminal_test.go` + `relay_metrics_test.go` + `fact_id_test.go` + `audit_governance_pending_idx_test.go` untracked (`??`). |

**Consequences for this design:**
1. All line numbers in the spec are correct **for the worktree**, which is the state this design extends. No implementation work may start before the baseline worktree is committed (§6 step 0) — otherwise the design's pins and the acceptance mapping have no committed anchor.
2. The spec's evidence table and acceptance matrix are **substantively accurate** (verified below, §1). The only mislabel is "HEAD `acfaaf4`" → "worktree on top of HEAD `acfaaf4`".
3. This design is a **delta**: the only remaining work from the supplied acceptance is exactly one new e2e test (REQ-2 AC-2.4 — transient re-POST count). Everything else is shipped-in-worktree, needs commit + `make check`, not new code.

---

## 1. Verification register (evidence claims re-checked, not trusted)

| # | Evidence claim | Verdict |
|---|---|---|
| E1 | `isPermanentDeliveryError` `relay.go:212-221` closed list `{ErrReceiptConflict, ErrInvalidReceipt, httpStatusError{409}, httpStatusError{422}}`; `deliverFact` `:82-118` routes permanent → `failFact` `:87-93`; `failFact` `:120-132` → `FailAuditGovernance` (terminal); `retryFact` `:134-148` → `RetryAuditGovernance`; `boundedBackoff` `:174-190` | ✅ **worktree-exact.** Read in full: classifier is exactly the closed list, wrapped sentinels classify identically (`errors.Is`/`errors.As`); `classifyRelayError` `:192-201` cross-referenced; `boundedBackoff` has deterministic per-ID jitter (±25%, floor `initial/2`, cap `maximum`). |
| E2 | `validateReceipt` `http.go:178-212`: non-202 → `httpStatusError`; non-JSON/oversized/unparseable → `ErrInvalidReceipt`; `conflict:true` → `ErrReceiptConflict`; `receiptMatches` `:214-225` (event_id **and tenant_id** mismatch, zero `accepted_at`, status ∉ {ledgered,indexed,archived} → `ErrInvalidReceipt`) | ✅ **exact.** Tenant-mismatch is a branch of `ErrInvalidReceipt`, not a separate sentinel. |
| E3 | `model.go:31-37` `httpStatusError` | ✅ **exact.** |
| E4 | 0042 = `failed_at_ns INTEGER NOT NULL DEFAULT 0` only, no index — both dialects; down = `DROP COLUMN` | ✅ **content-exact** (both `up`/`down` read; 0042 is **staged, not committed**). |
| E5 | 0043 partial-index pair exists in both dialects: `pending_claim_idx (available_at_ns, created_at_ns, id)` + `pending_lag_idx (created_at_ns)`, both `WHERE delivered_at_ns=0 AND failed_at_ns=0`; down = reversible `DROP INDEX IF EXISTS` ×2; header documents the `status`/`dead_at` deviation referencing `implementation-gate.md:21` | ✅ **exact** (all four files read; deviation note verbatim; index shape matches claim `ORDER BY` and lag `MIN`). |
| E6 | `failed_at_ns=0` predicates at claim.go `:37,62,88,194,207` (actual `:38,62,88,146,168,195,207` — cosmetic drift, two extra fences in the fenced UPDATE and `HasPendingDrainingAuditGovernance`); `OldestPendingAuditGovernance` `:188-201`; `HasPendingDrainingAuditGovernance` `:202-210` | ✅ **holds** — dead rows excluded from claim, fenced UPDATE, lag, and drain-pending; fencing requires owner+token+live lease (`:146,:168`). |
| E7 | `FailAuditGovernance` contract comment `audit_governance_types.go:92-95` ("never re-claimed, never re-POSTed, retained until prune"); impl `audit_governance_claim.go:159-186` fenced `SET failed_at_ns=$1` + `last_error` truncate to 512 | ✅ **exact.** |
| E8 | `config_audit_governance.go:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)`; validation `:240` (`>= InitialBackoffSeconds`) and `:250` (`<= 86_400`); wired `runtime.go:95`; `boundedBackoff` caps at `maximum` | ✅ **exact.** `docs/configuration.md:273` "Retry cap; facts retry indefinitely." — consistent with D3 (no attempt/age dead-letter). |
| E9 | 0039 `audit_governance_due_idx` lacks `failed_at_ns` | ✅ **holds** — superseded for the pending paths by 0043 (REQ-4). |
| E10 | All 9 pin tests exist: `TestRuntimePermanentDeliveryErrorsAreTerminal`, `TestIsPermanentDeliveryErrorClosedList`, `TestBoundedBackoffIsDeterministicAndCapped`, `TestRuntimeRelayCountersTrackDeliveryOutcomes`, `TestAuditGovernanceFailedFactReadsBackOneAttempt`, `TestAuditGovernancePendingIndexesServeClaimAndLagPlans`, `TestAuditGovernance0043DeviationHeaderPinned`, `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`, `TestRuntimeConflictingReceiptIsTerminalWithRetention` | ✅ **all present** (grep-verified by name and location). |
| E11 | Test-run evidence | ✅ **reproduced**: `go test ./internal/auditgovernance/ ./internal/repository/ -run 'Terminal|Permanent|Backoff|FailedFact|PendingIndex|Deviation|ConflictFail' -count=1` → `ok` (auditgovernance 4.176s, repository 2.785s; all 14 matching tests PASS). |
| E12 | AC-2.4 gap: "no test asserts a transient 5xx row is POSTed **more than once**" | ✅ **verified real.** `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`) waits only `retryPosts.Load() >= 1` before `Close`; grep across all package tests finds no `posts >= 2` / re-POST-count assertion for a transient row. The transient-table assertion in the sibling design (`internal-ai-…` §D-E "inter-POST gap ≤ 3s") is **phantom today** (verified absent). |
| E13 | D4: analysis file `docs/auto/analyses/internal-antivirus-4eff1e6c.json` mislabeled (tagged `internal-antivirus`, all directions target `internal/auditgovernance`) | ✅ **verified** — file exists, direction 1 title verbatim as quoted; no `internal/antivirus` code involved. |
| E14 | 500-line gate on files to be touched | ✅ `relay.go` 221 · `relay_terminal_test.go` 231 · `runtime_test.go` 209 · `http.go` 221 · `audit_governance_claim.go` 209 · `audit_governance_cleanup.go` 141 — all < 500; AC-2.4 adds ~60 lines to `relay_terminal_test.go` → ~291, still clear. |

Net: **the evidence is trustworthy on substance; every design-relevant fact reproduces.** The one spec imprecision (HEAD vs worktree) does not change any requirement or pin — it changes only the landing procedure (§6).

---

## 2. Design overview

The direction prescribes: (a) 409/422/invalid-receipt/tenant-mismatch → terminal dead-letter within ≤1 attempt; (b) transient retry bounded at 300s; (c) dead rows excluded from claim/lag; (d) partial index on the pending predicate; (e) retention-bounded prune. On this checkout, (a)–(e) are **implemented and pinned in the worktree** (§1), with one documented deviation (D1: `failed_at_ns` timestamp marker replaces the contract's `status`/`dead_at` columns).

The design therefore consists of:

1. **Baseline landing** (§6 step 0): commit the existing worktree as the direction's implementation — no code changes.
2. **AC-2.4 delta** (§4): one new runtime-level e2e test pinning transient re-POST (posts ≥ 2, strictly growing inter-POST gaps) — the only genuine gap found in verification.
3. **Governance pins already shipped** (REQ-1/3/4/5 ACs) — verified, no new code.

There are **no production API changes in this delta**. The API surface below is documented as-built so the acceptance mapping and failure modes are auditable against concrete symbols.

---

## 3. API changes

### 3.1 Shipped surface (as-built, to be committed — not new)

**Repository port** — `repository.AuditGovernanceStore` (`internal/repository/audit_governance_types.go:82-100`):
- **Added:** `FailAuditGovernance(ctx, id, owner, token, cause) error` — terminal-with-retention; fenced by owner+token+live lease (`audit_governance_claim.go:159-186`); sole writer of `failed_at_ns`; truncates `last_error` to 512 bytes.
- **Added:** `CleanupFailedAuditGovernance(ctx, cutoff, batch) (int64, error)` — prunes `failed_at_ns>0 AND failed_at_ns <= cutoff` (`audit_governance_cleanup.go:113-135`).
- **Extended:** `ClaimAuditGovernance`/`OldestPendingAuditGovernance`/`HasPendingDrainingAuditGovernance` predicates now exclude `failed_at_ns != 0` (signatures unchanged — backward compatible).
- `RetryAuditGovernance` unchanged (transient path; gains nothing).

**Schema (migrations, both dialects):**
- `0042_audit_governance_terminal_failed.{up,down}.sql` — `ADD COLUMN failed_at_ns INTEGER/BIGINT NOT NULL DEFAULT 0` / `DROP COLUMN`.
- `0043_audit_governance_pending_partial_index.{up,down}.sql` — two partial indexes (`pending_claim_idx`, `pending_lag_idx`) on exactly `delivered_at_ns=0 AND failed_at_ns=0` / `DROP INDEX IF EXISTS` ×2. Header carries the D1 deviation note.

**Config (env, `internal/config/config_audit_governance.go`):**
- `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` (default `300`, validation `>= InitialBackoffSeconds`, `<= 86400`) — consumed as `maxBackoff` in `Runtime` (`runtime.go:95`) and enforced by `boundedBackoff` (`relay.go:174-190`).
- `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS` (default `604800`, existing) — now also bounds failed-row prune via `retention` (`runtime.go:97`) in `cleanupDelivered` (`relay.go:150-172`).

**Internal (package-private, pinned by tests):** `isPermanentDeliveryError(error) bool` (`relay.go:212-221`) — closed list `{ErrReceiptConflict, ErrInvalidReceipt, *httpStatusError{409}, *httpStatusError{422}}`; `classifyRelayError` (`relay.go:192-201`) for log labels. **Membership is the behavioral contract** — the closed-list test (AC-1.1) pins both directions.

**Telemetry (existing counters, now meaningful):** `audit_governance_relay_dead_total` (terminal), `failed_total` (transient reschedule), `delivered_total`, `attempted_total`.

### 3.2 Delta (this design)

**One test-only change:** add `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` (AC-2.4) to `internal/auditgovernance/relay_terminal_test.go` — no production symbol changes. The store-claim `AuditGovernanceFact` struct exposes `Attempts` (claim read-back) but **not** `available_at_ns` history; the design deliberately avoids adding a repository read API for a test (I6 — no churn for test-only access), and pins `available_at_ns` monotonicity via the deterministic sink-observed gap proxy (§4.2).

---

## 4. AC-2.4 test design (the delta)

### 4.1 Test sketch (reuses existing harness — `terminalSink`, `pollUntil`, `observeWindow`, `runtimeConfig`)

```go
// TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff pins REQ-2 e2e
// (AC-2.4): a transient 5xx fact is POSTed more than once with the runtime
// still running, and the inter-POST gaps grow (deterministic proxy for
// available_at_ns strictly increasing between retries).
func TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff(t *testing.T) {
	ctx := context.Background()
	var posts atomic.Int32
	var mu sync.Mutex
	var gaps []time.Duration
	var last time.Time
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" { /* existing token reply */ return }
		now := time.Now()
		mu.Lock()
		if posts.Load() > 0 { gaps = append(gaps, now.Sub(last)) }
		last = now
		mu.Unlock()
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // 500 → transient class
	}))
	defer sink.Close()
	// …repository.Open + Migrate + binding + New(runtimeConfig(sink.URL), store, …)
	runtime.Start(ctx)
	pollUntil(t, 3*time.Second, func() bool { return posts.Load() >= 1 })
	observeWindow(t, 4500*time.Millisecond) // ≥ 2 full backoff windows
	if got := posts.Load(); got < 2 {
		t.Fatalf("transient fact POSTed %d times, want ≥ 2 (bounded retry)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gaps) < 2 || gaps[1] <= gaps[0] {
		t.Fatalf("backoff did not grow: gaps=%v (want gap2 > gap1)", gaps)
	}
	// after Close the row is still pending (failed_at_ns=0), never terminal
	runtime.Close()
}
```

### 4.2 Determinism argument (why `gap2 > gap1` always holds at harness config)

Harness `runtimeConfig` (`runtime_test.go:40-47`): `InitialBackoffSeconds=1`, `MaxBackoffSeconds=2`. `boundedBackoff(id, attempts, 1s, 2s)`:
- attempt 1: base 1s × jitter `[0.75, 1.25]` (digest-derived ±25%) → gap₁ ∈ [0.75s, 1.25s];
- attempt 2: base 2s (doubled, below cap) × same deterministic jitter → gap₂ = min(2s×[0.75,1.25], 2s) = [1.5s, 2.0s].

min(gap₂)=1.5s > max(gap₁)=1.25s ⇒ **strict growth is deterministic**, independent of the fact ID. Each retry schedules `available_at_ns = now + delay` with delay ≥ floor `initial/2` > 0, so observed growing gaps pin `available_at_ns` strictly increasing between retries. Observe window 4.5s > worst-case POST#3 at 1.25s + 2.0s = 3.25s (plus ≤20ms poll latency) ⇒ `len(gaps) ≥ 2` is guaranteed, with margin for CI jitter.

### 4.3 Why not a direct `available_at_ns` assertion

`AuditGovernanceFact` carries `Attempts` but not the timestamp history; the only persisted evidence is the row's *current* `available_at_ns` (readable after `Close` + lease expiry via a fresh claim, or via raw SQL in a repository-level test — the AC-1.3 pattern). A single current value cannot prove *strictly increasing between retries*; the gap sequence is the only complete observation. Repository-level variant (optional, not required): mirror `TestAuditGovernanceFailedFactReadsBackOneAttempt` with a 500-answer store wrapper — rejected as redundant since the runtime-level gap pin is strictly stronger.

### 4.4 Constraints honored

- Reuses `terminalSink` shape via a local handler (no changes to `terminalCase`); **no second `TestMain`** (`relay_metrics_test.go:30` owns the package's single `TestMain` — a second one is a compile error).
- No testify; `testing` only (I6). No new `go.mod` deps.
- `relay_terminal_test.go` 231 → ~291 lines < 500 (hard gate).
- Table-free standalone (single 4.5s observe window, no `t.Parallel` hazard with the shared prom handler — mirrors `TestRuntimeRelayCountersTrackDeliveryOutcomes` which is also non-parallel).

---

## 5. Compatibility constraints

| # | Constraint | Binding |
|---|---|---|
| C1 | **Closed-list semantics are the API.** Adding/removing a permanent class (e.g., the sibling's `ErrInvalidEvent` flip) requires updating `isPermanentDeliveryError` **and** both lists of `TestIsPermanentDeliveryErrorClosedList` — the pin fails otherwise. The sibling design (`internal-ai-…-v1.design.md`) flips `ErrInvalidEvent` permanent; **this spec deliberately keeps it transient** (D2: local-construction error, deterministic producers at `http.go:101/:105/:113`, direction acceptance never required it; flipping expands scope). If the sibling ever lands, C1 forces a coordinated pin update — the two designs are mutually exclusive on this point and must not both land. | I4-adjacent; test-enforced |
| C2 | **Migration immutability (I2).** 0042/0043 files are applied-on-startup by version; once any environment has applied them, they are immutable. `status`/`dead_at` (contract prescription) is deliberately **not** added — D1 documents this in the 0043 header and `TestAuditGovernance0043DeviationHeaderPinned` pins both the presence of the deviation note and the absence of `CREATE INDEX` in 0042. A 0044 rename would be zero-behavior churn. | I2, AC-4.3 |
| C3 | **SQL placeholder discipline (I1).** Claim/retry/fail statements use `s.rebind` with distinct `$N` per parameter (no reuse); new SQL in future work must follow. Timestamps are RFC3339Nano in wire/API; ns-integers in storage. | I1 |
| C4 | **Dialect parity.** Every schema/query change ships sqlite **and** postgres (0042/0043 both verified; EXPLAIN pins exist for SQLite, and the postgres plan shape is covered by the same index predicate). Down files must be reversible and `IF EXISTS`-safe (0043 convention, replay-safe). | I2 |
| C5 | **Claim fencing.** `FailAuditGovernance`/`RetryAuditGovernance` require the exact `(claim_owner, claim_token)` and a live lease (`lease_expires_at_ns <= now`); a stale or foreign owner cannot terminalize a row. Tests AC-3.2/AC-3.3 pin this. | AC-3.2/3.3 |
| C6 | **Opt-in runtime (I5).** `AUDIT_GOVERNANCE_ENABLED=false` (default) — all of this is dead code on the CI baseline path; nil-safe, no impact on core CRUD. | I5 |
| C7 | **Config validation envelope.** `MaxBackoffSeconds` must be `>= InitialBackoffSeconds` and `<= 86400`; misconfig fails `New` at startup (no silent drift). `DeliveredRetentionSeconds` range `3600..31536000` also bounds the failed-prune window. | `cfg.Validate()` |
| C8 | **`TestMain` singleton + prom handler sharing.** The metrics test owns the package `TestMain`; new runtime tests must not add one and must not run parallel with the prom-scraping test. | compile/race |
| C9 | **Wire/API backward compatibility.** No change to the POST payload, receipt schema, `/token` flow, or the `Store` method signatures consumed by existing callers; `failed_at_ns` defaults 0 for pre-existing rows (0042 `DEFAULT 0`), so rows written before 0042 remain claimable/pending — no data migration required. | E4 |
| C10 | **`ErrInvalidEvent` transient-by-design (D2) + no attempt/age dead-letter (D3).** "Facts retry indefinitely" (`docs/configuration.md:273`) remains true for transient classes — this is the documented behavior, not a gap. `Ready()`/`degraded` handling is a sibling direction (out of scope). | D2/D3 |

---

## 6. Failure modes

| # | Failure | Detection | Behavior | Recovery |
|---|---|---|---|---|
| F1 | `FailAuditGovernance` store write fails (claim lost between POST and fail) | warn log (`relay.go:128-131`) + `relay_dead_total` **not** incremented | The row stays pending with a live lease; lease expiry → re-claim → **re-POST** (≤1-attempt violated in this edge, by design: "lease re-claim is the recovery mechanism" `relay.go:117-119`) | Automatic after `ClaimTTLSeconds`; bounded by backoff; terminality is then re-attempted. Acceptable — permanent classes are still eventually terminal unless the DB is down entirely |
| F2 | `RetryAuditGovernance` store write fails | warn log | Fact re-claimed after lease expiry; retry count/backoff reset from claim's perspective | Automatic |
| F3 | `CleanupFailedAuditGovernance` fails | warn log (`cleanupDelivered`); next cycle retried (`nextCleanup` schedule) | Failed rows survive longer than retention — diagnosis data retained, no correctness impact | Automatic next cleanup tick |
| F4 | Receiver answers 500 forever (transient) | `failed_total` grows; `attempted_total` grows; lag grows | Bounded retry forever at ≤300s cap — **no** dead-letter, by D3 | Operator action (fix receiver / disable binding); monitoring via counters |
| F5 | Receiver answers 409/422 after a *successful* delivery (e.g., receiver bug) | terminal fail + `dead_total` | Event never ledgers; retained 7d for diagnosis, then pruned | None — receiver bug surfaced in retained `last_error` |
| F6 | `boundedBackoff` overflow (attempts huge) | — | `delay *= 2` loop bounded by `delay > maximum/2 → delay = maximum`; jitter capped by `min(max(...), maximum)`; deterministic per ID | None — cap is hard |
| F7 | Clock skew: retention cutoff `now.Add(-retention)` vs row `failed_at_ns` | — | Skewed clock prunes early/late; ns-precision monotonic storage reduces risk; same exposure exists for delivered prune | NTP discipline; same as existing delivered path |
| F8 | Misclassification introduced by future edit (e.g., adding a class to the closed list without test update) | `TestIsPermanentDeliveryErrorClosedList` fails both directions | — | Pin is the gate (C1) |
| F9 | Two relay replicas claim the same row (cluster) | — | Fenced UPDATE (`claim_owner`+live lease) + `SKIP LOCKED` (pg) / tx fenced UPDATE (sqlite); only one owner succeeds | Automatic — lease fencing |
| F10 | `ack` store write lost after successful POST | warn "acknowledgement lost" | Row re-claimed and re-POSTed; receiver answers idempotently `{duplicate:true, conflict:false, status:ledgered}` (contract A) | Automatic; duplicate-aware receipt path (`http_test.go:192-252` pins) |

---

## 7. Migration steps (landing + schema)

### 7.1 Landing procedure (this checkout)

0. **Commit the baseline worktree first** (§0) — staged 0042 pair, untracked 0043 pair + 3 test files, modified `relay.go`/`http.go`/`model.go`/`facts.go`/`runtime_test.go`/`http_test.go`/`fact_id_test.go`. This is the direction's implementation; the spec's pins have no committed anchor until then.
1. Author AC-2.4 (`TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff`, §4.1) in `relay_terminal_test.go`.
2. `make check` (gofmt · build · vet · full `go test`; AC-2.4 must pass with `-race` too — `make test-race`).
3. Commit the delta ("test(auditgovernance): pin transient re-POST with growing backoff (REQ-2 AC-2.4)").

### 7.2 Schema rollout (fresh + upgrade)

- **Fresh install:** `repo.Migrate` applies 0039 → 0040 → 0041 → 0042 → 0043 serially (both dialects); 0043's index creation requires 0042's `failed_at_ns` column — **ordering is mandatory and is enforced by the version-serial runner** (I2).
- **Upgrade (applied 0039/0042, no 0043):** startup applies 0043 only — additive `CREATE INDEX` on existing rows; no data rewrite; `failed_at_ns` already exists from 0042. Pre-0042 installs get both, in order.
- **Down/rollback (manual only, never auto — I2):** `0043.down` (drop both indexes, `IF EXISTS`-safe) then `0042.down` (`DROP COLUMN failed_at_ns`). Rollback is complete — 0043 is index-only, 0042 is a column with `DEFAULT 0` so dropping loses only terminal markers.
- **Post-rollback behavior:** terminal facts become claimable again (claim predicate no longer excludes them) → re-POSTed under the old transient path. Expected: rollback is a deliberate return to pre-direction behavior.
- **Multi-replica:** 0042/0043 are applied once by the version-serial runner at startup; concurrent startup races are handled by the existing migration locking (unchanged). 0043 index DDL is idempotent under `IF NOT EXISTS`-equivalent semantics only if the runner serializes — it does (I2 "单向自动执行, 按版本串跳过已应用").

---

## 8. Decisions & non-goals

| # | Decision | Rationale |
|---|---|---|
| D1 | **No `status`/`dead_at` columns** — `failed_at_ns` is the terminal marker; deviation documented in 0043 header + pinned by `TestAuditGovernance0043DeviationHeaderPinned` | 0039 is timestamp-led; all queries already predicate on `failed_at_ns`; renaming an applied schema violates I2; a 0044 rename is zero-behavior churn |
| D2 | **`ErrInvalidEvent` stays transient** — closed list excludes it, pin test includes it in the transient set | Local-construction error (deterministic producers), not a receiver rejection; direction's acceptance never required it terminal; the sibling design flipping it is **not adopted** (mutually exclusive with C1) |
| D3 | **No attempt/age dead-letter bound** | Acceptance requires classification-based terminality (≤1 attempt for permanent) + capped backoff for transient — both satisfied; "retry indefinitely" is documented behavior |
| D4 | **Module label** — analysis file `internal-antivirus-4eff1e6c.json` is mislabeled; direction is `internal/auditgovernance` + `internal/repository` (+ `config`) | Filename artifact; `internal/antivirus/` untouched |
| D5 | **AC-2.4 is test-only** — no production API added for `available_at_ns` history read-back | I6; the sink-gap proxy is strictly stronger than a single current-value assertion (§4.3) |

**Non-goals:** deterministic fact IDs (analysis direction 2); relay observability + `Ready()`/`BacklogAge` decoupling (direction 3 — `cmd-server-…-ready-degraded-v1.spec.md`); `ErrInvalidEvent` flip (sibling design); `status`/`dead_at` (D1); attempt/age bounds (D3); any `internal/antivirus` change (D4).

---

## 9. Testable acceptance mapping

| Supplied acceptance check | Requirement | Testable pin | Status |
|---|---|---|---|
| Classification: POST 409/422/tenant-mismatch/invalid-receipt → terminal ≤1 attempt (`failed_at_ns` set, never re-claimed, absent from lag) | REQ-1 | AC-1.1 `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:199`); AC-1.2 `TestRuntimePermanentDeliveryErrorsAreTerminal` (`:35`, 5 sink rows × exactly-1-POST + `assertTerminalState` `:126-146`); AC-1.3 `TestAuditGovernanceFailedFactReadsBackOneAttempt` (`pending_idx_test.go:210`, `attempts==1` read-back) | ✅ implemented & passing (verified §1 E10/E11) |
| Transient 5xx/network rows keep retrying with backoff cap ≤300s | REQ-2 | AC-2.1 transient half of closed list (400/401/403/404/410/429/500/501/503/`ErrInvalidEvent`/`ErrTokenUnavailable`/transport/deadline → false); AC-2.2 `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`, deterministic ∧ (200s, 300s] at max=300s); AC-2.3 `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`, 500-sink reschedule via `failed_total` delta) | ✅ mostly; **AC-2.4 new** (§4): `TestRuntimeTransientDeliveryIsRePostedWithGrowingBackoff` — posts ≥ 2 with runtime running, gap₂ > gap₁ (deterministic proxy for `available_at_ns` strictly increasing), row still pending after Close |
| Claim + `OldestPendingAuditGovernance` + `HasPendingDrainingAuditGovernance` exclude dead rows; failed row never reappears in claim (T-3 lock) | REQ-3 | AC-3.1 static SQL predicates (`claim.go:38,62,88,146,168,195,207`); AC-3.2 `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:419`); AC-3.3 `assertTerminalState` runtime probes | ✅ implemented & passing |
| New 0043 migration (sqlite+postgres): partial index on pending predicate | REQ-4 | AC-4.1 files (both dialects, up/down); AC-4.2 `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (`pending_idx_test.go:177`, EXPLAIN: no `SCAN o` on claim, lag `MIN` uses `pending_lag_idx`); AC-4.3 `TestAuditGovernance0043DeviationHeaderPinned` (`:251`) | ✅ shipped; `status`/`dead_at` deliberately deviated (D1) — documented in-file + pinned |
| Prune `CleanupFailedAuditGovernance` bounded by `DeliveredRetentionSeconds` | REQ-5 | AC-5.1 wiring (`runtime.go:97` retention; `relay.go:150-172` cleanupDelivered; `cleanup.go:113-135` cutoff predicate); AC-5.2 early/late prune (`relay_terminal_test.go:148-164` + `audit_governance_test.go:419` `-1h`→0 rows, `+1h`→1 row) | ✅ implemented & passing |

**Acceptance verdict:** all supplied checks are implemented and green on this checkout (verified independently, §1) **except one** — the transient e2e re-POST pin (AC-2.4), which is test-only and designed in §4. The only prescription intentionally not implemented is `status`/`dead_at` (D1), which is documented in-migration and pinned by test rather than silently dropped.

**Gate:** after AC-2.4 lands — `make check` (`gofmt -l` clean · `go build ./...` · `go vet ./...` · `go test ./...`; single-file ≤ 500 lines holds at ~291) + `make test-race` for the new test.
