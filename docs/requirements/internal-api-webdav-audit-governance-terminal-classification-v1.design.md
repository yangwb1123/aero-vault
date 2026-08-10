# Design — B3-1/T-3 at the WebDAV surface: terminal classification e2e + cumulative 300s transient retry cap

**Module:** `internal/api/webdav` (pin point / T-3 vehicle — file-fact producer) · mechanism delta in `internal/auditgovernance` + `internal/repository` (+ `internal/config` untouched, D3)
**Spec:** `docs/requirements/internal-api-webdav-audit-governance-terminal-classification-v1.spec.md` (REQ-1..4, AC-1.1..4.2, D1..D5)
**Contract:** `docs/campaigns/implementation-gate.md:21` item 1 (aero-vault): "…瞬态有界重试 cap 300s… T-3：422 → 一个周期内终态；`Ready()` 含 dead 行 = true；批次继续"
**Siblings:** `internal-auditgovernance-terminal-classification-v1.design.md` (classification half — shipped in worktree, D1/D2/D3) · `cmd-server-audit-governance-ready-degraded-v1.*` (B3-2, landed at HEAD `15763e2`) · `internal-ai-audit-governance-permanent-error-terminal-classification-v1.*` (competing `ErrInvalidEvent` flip — **not adopted**, C1)
**Date:** 2026-08-08 · **HEAD:** `15763e2` · **Worktree:** dirty — classification half + 0042/0043 exist **only uncommitted** (§0)

---

## 0. Baseline caveat (verified, not trusted)

The evidence (spec summary) claims specific line numbers and gaps. **Every claim was re-checked against this worktree.** Verdict: substantively **accurate**; three cosmetic drifts (below) do not change any requirement.

| Evidence claim | Verified (this worktree) | Verdict |
|---|---|---|
| `isPermanentDeliveryError` closed list `relay.go:227-240` = `{ErrReceiptConflict, ErrInvalidReceipt, httpStatusError{409}, httpStatusError{422}}`; `ErrInvalidEvent` absent; routed in `deliverFact` `:82-118` | Function at `relay.go:227-240` (comment `:219`); routed `:87-93`; `ErrInvalidEvent` not in list — transient | ✅ exact |
| `TestIsPermanentDeliveryErrorClosedList` `relay_terminal_test.go:199`; `TestRuntimePermanentDeliveryErrorsAreTerminal` `:35` (5-row table) | Functions at `:200` and `:36` (comment line drift only); 5 rows `{http409, http422, tenant-mismatch, non-ledgered-status, unparseable-body}` | ✅ (drift +1) |
| 0043 partial index shipped both dialects | `migrations/{sqlite,postgres}/0043_…up.sql` — `pending_claim_idx (available_at_ns, created_at_ns, id)` + `pending_lag_idx (created_at_ns)`, both `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; EXPLAIN pin `audit_governance_pending_idx_test.go:177`, header pin `:251` | ✅ exact |
| Dead-row predicates at `audit_governance_claim.go:38,62,88,146,195,207` | `:38` (PG claim), `:62` (SQLite inner), `:88` (fenced claim UPDATE), `:146` (retry fence), `:168` (fail fence), `:195` (OldestPending), `:207` (HasPendingDraining) | ✅ exact |
| `MaxBackoffSeconds=300` default `config_audit_governance.go:65`; validation `:240`/`:250`; `boundedBackoff` `relay.go:181-190` | `getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` `:65`; `validAuditGovernanceRetry` `:239-242`, `boundedAuditGovernanceTiming` `:244-252`; `boundedBackoff` `relay.go:181-190` | ✅ exact |
| **GAP-1**: `retryFact` `relay.go:134-148` has no cumulative window / attempt bound | Read in full: `retryFact` → `RetryAuditGovernance(ctx, id, owner, token, lastErr, next)` (`audit_governance_claim.go:137`) — **no deadline/attempt param**; `grep -rn "cumulative\|maxAttempts" internal/auditgovernance/` (non-test) → 0 hits; `RetryEventOutbox` maxAttempts branch (`event_outbox.go:365`) has **no governance mirror** | ✅ exact — **GAP real** |
| **GAP-2**: zero `auditgovernance` imports in `internal/api/webdav`; harness `newWebdavRelayHarness` `dav_relay_test.go:37-90` builds bus from raw repo | `grep -rn auditgovernance internal/api/webdav/` → exit 1, 0 hits; harness `:47-90` (comment `:37`): `bus := events.New(repo, logger)` (:75) with **raw** repo; only `events.NewEventOutboxRelay` started | ✅ exact — **GAP real** |
| Production chain `main.go:70-82` = `buildAuditGovernanceRuntime` → `Start` → `WrapRepository` → `bus.WithRepository` | `cmd/server/main.go:76-82` verbatim; `auditgovernance.WrapRepository` nil-safe (`repository.go:25-34`); bus captures repo (`events/bus.go:64-67`); `Publish` → `repo.InsertEvent` (:84) | ✅ exact |
| `InsertEventWithGovernance` wrapper `auditgovernance/repository.go:36-49`; impl sets `origin_kind='file'` | Wrapper `InsertEvent` `repository.go:36-49`; impl `audit_governance_write.go:53-`, `fact.OriginKind, fact.OriginID = AuditOriginFile, id`; `facts.go:40` `FactKind:"file", Action:"file."+type` | ✅ (facts.go :40 vs cited :42) |
| `runtime.go:151-160` BacklogAge / `:162-185` Ready | `BacklogAge` `:151`, `Ready` `:162` | ✅ exact |
| **GAP-3**: acceptance `sqlite_master` clause unpinned at a surface harness | `docs/auto/analyses/internal-api-webdav-c346cab0.json` acceptance text includes "sqlite_master shows the partial index (proposed)" — 0043 shipped ⇒ becomes a lock test | ✅ exact |
| Test-run evidence | Reproduced: `go test ./internal/auditgovernance/ ./internal/repository/ -run 'Terminal|Permanent|ConflictFail|Backoff|PendingIndexes|0043' -count=1` → ok (8.999s / 1.123s); `go test ./internal/api/webdav/ -count=1` → ok (41.16s) | ✅ reproduced |

**Baseline state:** the classification half (relay closed list, 0042/0043, dead-row predicates) exists **only in the uncommitted worktree** (git status: `M internal/auditgovernance/relay.go …`, untracked 0042/0043 pairs + test files). **Consequence: no implementation may start before the sibling campaign's worktree is committed** (§7 step 0) — the design's pins have no committed anchor otherwise.

---

## 1. Design overview

The spec prescribes four deliverables. Two are test-only, one is a lock test, one is production:

| Req | Nature | Delta |
|---|---|---|
| REQ-1 — webdav governance harness (AC-1.1..1.3) | test infra | `newWebdavRelayHarness` gains a variadic governance mode; new file `internal/api/webdav/dav_governance_test.go` |
| REQ-2 — T-3 permanent-class e2e (AC-2.1..2.4) | test-only | three surface table rows (409/422/malformed) × terminal assertions |
| REQ-3 — cumulative 300s cap (AC-3.1..3.4) | **production** | migration 0044 (`first_attempt_at_ns`), claim-anchor SET (both dialects), `AuditGovernanceFact.FirstAttemptAt` read-back, pure window decision + `retryFact` terminal branch |
| REQ-4 — `sqlite_master` partial-index lock (AC-4.1..4.2) | test-only | sqlite_master query on the harness DB (0043 already shipped) |

Production surface is **exactly one behavioral change**: a fact failing transiently (e.g. 500) goes terminal once `now − firstAttempt > MaxBackoffSeconds` (default 300s), with `last_error` retained and full dead-row semantics. Everything else is pins.

---

## 2. API changes

### 2.1 Schema — migration 0044 (both dialects, I2)

`internal/repository/migrations/{sqlite,postgres}/0044_audit_governance_first_attempt_anchor.{up,down}.sql`:

```sql
-- up (sqlite)
ALTER TABLE audit_governance_outbox
  ADD COLUMN first_attempt_at_ns INTEGER NOT NULL DEFAULT 0;
-- up (postgres)
ALTER TABLE audit_governance_outbox
  ADD COLUMN first_attempt_at_ns BIGINT NOT NULL DEFAULT 0;
-- down (both): ALTER TABLE audit_governance_outbox DROP COLUMN first_attempt_at_ns;
```

Header comment: anchor of the cumulative transient-retry window (contract item 1 "瞬态有界重试 cap 300s"); set once at first claim; the 0043 pending predicate (`delivered_at_ns=0 AND failed_at_ns=0`) is **unchanged** — the anchor is deliberately not part of any index (claim index still serves the plan; the column is read via heap, EXPLAIN pin unaffected).

### 2.2 Repository — claim anchor + read-back (`internal/repository/audit_governance_claim.go`, `audit_governance_types.go`)

- `auditGovernanceCols` (const, `claim.go:11-13`) gains `first_attempt_at_ns` **appended last** — the `RETURNING` lists of both claim paths follow automatically; `scanAuditGovernanceRow` (`:101-113`) scans it into `int64` → `timeFromUnixNano`; `first_attempt_at_ns=0` ⇒ zero `time.Time` (safe default, §5 FM-3).
- `AuditGovernanceFact` (`audit_governance_types.go:36-`) gains `FirstAttemptAt time.Time` with the same read-back-only comment as `Attempts` (never written by callers).
- **Both claim SETs set the anchor exactly once, atomically, in the existing fenced UPDATE** (no new statement, no extra round-trip):

```sql
-- claimAuditGovernancePostgres (8 params; $7 is the anchor time):
SET attempts=attempts+1,claim_owner=$1,claim_token=$2,lease_expires_at_ns=$3,
    first_attempt_at_ns=CASE WHEN first_attempt_at_ns=0 THEN $7 ELSE first_attempt_at_ns END
WHERE id IN ( … ) RETURNING …  -- args: owner, token, lease, now, now, revision, limit, now
-- claimAuditGovernanceIDs (sqlite, 7 params; $7 is the anchor time):
SET attempts=attempts+1,claim_owner=$1,claim_token=$2,lease_expires_at_ns=$3,
    first_attempt_at_ns=CASE WHEN first_attempt_at_ns=0 THEN $7 ELSE first_attempt_at_ns END
WHERE id=$4 AND delivered_at_ns=0 AND failed_at_ns=0
AND available_at_ns <= $5 AND lease_expires_at_ns <= $6
RETURNING …  -- args: owner, token, lease, id, now, now, now
```

  `CASE WHEN …=0 THEN now ELSE keep` is idempotent across lease re-claims and ack-lost re-claims. **The anchor must be a NEW placeholder `$7`, never an existing `$N`**: `rebind` (`repository/sql.go:42-61`) rewrites *every* `$N` occurrence to `?` in textual order (numeric values ignored) — reusing `$4`/`$5` would emit one extra `?` per reuse and misbind on SQLite (I1: 同值也须新占位符). The new `$7` gets a fresh argument (`now.UnixNano()`), keeping PG and SQLite param lists in lockstep. The column is never reset by `RetryAuditGovernance`/`FailAuditGovernance`/`CompleteAuditGovernance` (their SET lists unchanged).

- **No signature changes.** `RetryAuditGovernance` keeps its `(…, lastErr string, next time.Time)` shape — the window decision lives in the relay, not the store (keeps the store API stable; the sibling events outbox's `maxAttempts` param is not mirrored, D3: governance is time-based, not attempt-based).

### 2.3 Relay — pure window decision + terminal branch (`internal/auditgovernance/relay.go`)

```go
// cumulativeWindowExceeded is the REQ-3 terminal decision, pure and pinned
// (AC-3.2/3.4): strictly greater — the == boundary stays transient. A zero
// anchor (row not yet claimed, or pre-anchor read) is never terminal.
func cumulativeWindowExceeded(firstAttempt, now time.Time, window time.Duration) bool {
	return !firstAttempt.IsZero() && now.Sub(firstAttempt) > window
}
```

```go
func (r *Runtime) retryFact(fact repository.AuditGovernanceFact, cause error) {
	// REQ-3: the cumulative retry window (== MaxBackoffSeconds, D3). Once
	// now - firstAttempt exceeds it, a transient-only failure stream is
	// terminal-with-retention — the same dead-row semantics as permanent
	// classes (failFact, never re-claimed, pruned after retention).
	if cumulativeWindowExceeded(fact.FirstAttemptAt, time.Now().UTC(), r.maxBackoff) {
		r.failFact(fact, cause)
		return
	}
	telemetry.IncAuditGovernanceRelayFailed(context.Background())
	… // unchanged: boundedBackoff + RetryAuditGovernance + onRetry hook
}
```

The window check **precedes** `IncAuditGovernanceRelayFailed` (a window-terminalized fact counts `dead_total`, not `failed_total` — the counters stay meaningful per class). `failFact` already logs `attempt` and `err` and increments `dead_total`.

### 2.4 Test-only API — harness governance mode (`internal/api/webdav/dav_relay_test.go` + new `dav_governance_test.go`)

`newWebdavRelayHarness` gains a **variadic** option — the five existing callers compile unchanged (compat constraint C8):

```go
// governanceHarness (defined in dav_governance_test.go) selects the REQ-1
// wiring mode: mirrors cmd/server/main.go:70-82.
type governanceHarness struct {
	cfg      config.AuditGovernanceConfig // Enabled, shrink timings, binding tenant "default"
	respond  func(http.ResponseWriter, *http.Request) // fact-POST responder; nil ⇒ healthy 202 receipt
	runtime  **auditgovernance.Runtime     // out-param: filled after Start (AC-2.3 probes)
}
func newWebdavRelayHarness(t *testing.T, relayOpts *events.EventOutboxRelayOptions,
	busWiring *webdavBusWiring, gov ...*governanceHarness) (*httptest.Server, *service.FileService, string)
```

When `len(gov) == 1` (nil ⇒ existing behavior byte-identical): after `repo.Migrate` — `store := repo.(auditgovernance.Store)`; `rt, err := auditgovernance.New(gov.cfg, store, logger)`; `rt.Start(ctx)`; `t.Cleanup(rt.Close)` (registered **after** `repo.Close`'s cleanup ⇒ LIFO: runtime stops before repo closes); `wrapped := auditgovernance.WrapRepository(repo, rt)`; `*gov.runtime = rt`; then **both** `svc := service.NewFileService(store, wrapped, logger)` and `bus := events.New(wrapped, logger)` use the wrapped repo — main.go order `WrapRepository → bus.WithRepository(wrapped) → svc.WithEventSink(bus)`.

The governance receiver (per-case, `terminalSink` shape of `relay_terminal_test.go:166-192`): one httptest server answering `/token` with `{"access_token":"token","token_type":"Bearer","expires_in":60}` and the fact path `api/v1/events` (`model.go:19`) with the case responder; healthy responder = 202 + `{"receipt":{"event_id":<echo>,"tenant_id":"default","status":"ledgered","accepted_at":<RFC3339>}}` (echoes POST body — the `tenantMismatchReceipt` shape of `relay_terminal_test.go:186-196`, correct tenant).

### 2.5 No other production API changes

- No config knob (D3: window reuses `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS`).
- No `status`/`dead_at` columns (D1), `ErrInvalidEvent` stays transient (D2).
- No webdav adapter production change (D5) — the adapter is the pin point only.
- Wire/API: POST payload, receipt schema, `/token` flow unchanged (C10).

---

## 3. Compatibility constraints

| # | Constraint | Binding |
|---|---|---|
| C1 | **`ErrInvalidEvent` transient (D2) is exclusive with the sibling flip** (`internal-ai-…-v1.design.md`). Both designs touch `isPermanentDeliveryError` + `TestIsPermanentDeliveryErrorClosedList`; only one may land. This design does **not** modify the closed list. | test-enforced; merge order |
| C2 | **Migration immutability (I2).** 0044 is a **new** pair; 0039/0042/0043 files untouched (the spec's AC-3.1 "new migration pair" — never edit applied files). 0044 must apply after 0043 (anchor column is independent of the 0043 indexes, but version-serial order is 0039→…→0044). | I2 |
| C3 | **SQL placeholder discipline (I1).** The anchor uses a **new `$7`** position in both claim SETs (fresh argument per occurrence — `rebind` rewrites every `$N` textually, `sql.go:42-61`; reusing an existing `$N` misbinds SQLite); `CASE WHEN` is dialect-neutral; timestamps ns-integers in storage. | I1 |
| C4 | **Dialect parity.** Both claim paths get the identical anchor SET + RETURNING column; scan order matches `auditGovernanceCols` in both. The 0043 EXPLAIN pin (`TestAuditGovernancePendingIndexesServeClaimAndLagPlans`) must stay green — adding a heap column does not change index usage (verify in §7 step 2). | test |
| C5 | **Claim fencing unchanged.** Anchor is set inside the same fenced UPDATE (owner+token+live lease); a stale owner cannot anchor/reset. | existing pins |
| C6 | **Behavioral delta for existing deployments (documented, intended).** Transient-only failure streams previously retried forever (docs/configuration.md:273 "facts retry indefinitely"); after this design they dead-letter at the cumulative 300s default. `Ready()`/`BacklogAge` now clear 300s after a stuck 500 receiver — the B3-2 450s alert and lag gauge remain coherent (dead rows were already excluded). **docs/configuration.md:273 must be updated** (§7 step 6). | docs |
| C7 | **Window == `MaxBackoffSeconds` (D3)** — single "cap 300s" covers per-attempt delay and total span; harness shrinks to 2s exactly as it already shrinks `maxBackoff`; validation envelope `>= InitialBackoffSeconds`, `<= 86400` unchanged. | AC-3.2/3.3 |
| C8 | **Harness backward compatibility.** Variadic `gov` param: 5 existing `newWebdavRelayHarness` callers unchanged; `gov==nil` path byte-identical. `dav_relay_test.go` (504 lines, test file — exempt from the 500-line gate per `Makefile:172` `-not -name '*_test.go'`) is touched only by the ~12-line variadic branch; all new code goes in `dav_governance_test.go`. No `TestMain` in `internal/api/webdav` (verified) — no singleton constraint. | compile; make check |
| C9 | **Opt-in (I5).** Governance stays flag-gated; `WrapRepository(nil)` is a no-op; CI baseline path untouched. | I5 |
| C10 | **Wire compatibility.** No change to POST/receipt/token; `Store` method signatures unchanged (§2.2); pre-existing rows anchor at their first post-upgrade claim (0044 `DEFAULT 0`), no backfill. | C9/C2 |
| C11 | **Pure decision pinned.** `cumulativeWindowExceeded` is package-level and unit-tested with injected times — no wall-clock 300s wait in CI (AC-3.4). | AC-3.2/3.4 |

---

## 4. Failure modes

| # | Failure | Detection | Behavior | Recovery |
|---|---|---|---|---|
| FM-1 | Claim UPDATE anchors but RETURNING/SELECT read fails (DB error between SET and scan) | claim error log | Fact not delivered this cycle; lease expires → re-claim; anchor already set (`CASE WHEN`) ⇒ window decision still correct; attempts increments again | Automatic (lease re-claim) |
| FM-2 | Relay clock vs DB clock skew | — | Anchor = DB-now at claim; decision = relay-now; skew shifts the effective window by ±skew (boundary fuzz only — the strict `>` decision is per single comparison, not cumulative accumulation) | NTP discipline; same exposure as retention cutoff (sibling F7) |
| FM-3 | `FirstAttemptAt` zero on read-back (row claimed pre-0044-upgrade — impossible in practice: migration applies before runtime starts; or DB default) | — | Zero ⇒ `cumulativeWindowExceeded=false` ⇒ transient retry continues; **safe direction** (no premature terminality); anchor is set at the next claim | Automatic |
| FM-4 | Window-terminalized fail write fails (claim lost between POST and fail) | warn log (`failFact` `:128-131`), `dead_total` not incremented | Row re-claimed (anchor persists), one more POST, fail re-attempted next cycle — the ≤1-attempt property is violated only in this DB-failure edge, identical to permanent-class F1 | Automatic after claim TTL |
| FM-5 | Receiver alternates 500/409: a 409 lands terminal at ≤1 attempt regardless of window (permanent check precedes the window branch in `deliverFact`); a 500 stream terminalizes at the window | counters | Both classes share `failFact` dead semantics | None (intended) |
| FM-6 | 0044 down-migration applied | — | Anchors dropped; rows retry forever again (pre-direction behavior); terminal rows become claimable only if 0042 also rolled back | Rollback is deliberate (I2) |
| FM-7 | Reconcile re-enqueues a gap whose outbox row was window-terminalized and retention-pruned | — | `EnqueueAuditGovernance` `ON CONFLICT (origin_kind,origin_id) DO NOTHING` (`audit_governance_write.go:160`) suppresses re-enqueue while the row exists; after the retention prune the gap reappears ⇒ new row, fresh anchor, **new 300s window** — bounded per row, intended | None (bounded by design) |
| FM-8 | `docs/configuration.md:273` left stale ("retry indefinitely") | doc review | Operators misjudge dead-lettering | §7 step 6 |
| FM-9 | Harness leak: runtime still running when receiver/repo closes (test bug) | test hang under `-race` | LIFO teardown (§2.4): `rt.Close` registered after `repo.Close` ⇒ runs first; receiver closed by the case before the harness (registered first ⇒ runs last) | Test-structure invariant |
| FM-10 | Boundary regression: `==` window treated as terminal (edit of `>` to `>=`) | AC-3.2 boundary pin fails | Premature dead-letter of a fact at exactly window | Pin is the gate |

---

## 5. Migration steps

### 5.1 Landing procedure (this checkout)

0. **Commit the sibling campaign's worktree first** (§0) — the classification half (relay closed list, 0042/0043, all `internal/auditgovernance` + `internal/repository` M/?? files) and this spec/design docs. No pins have a committed anchor before that.
1. **Production delta** (one commit, "feat(auditgovernance): cumulative 300s transient retry cap (0044 first-attempt anchor)"):
   - 0044 up/down pair, both dialects (§2.1);
   - `audit_governance_claim.go`: `auditGovernanceCols` + anchor SET in both claim paths + `scanAuditGovernanceRow` (+`types.go` field);
   - `relay.go`: `cumulativeWindowExceeded` + `retryFact` branch (§2.3).
2. **Pins for the production delta** (same commit or test commit):
   - `TestCumulativeWindowExceededBoundary` (`internal/auditgovernance/relay_terminal_test.go` or `runtime_test.go`): pure-function table — within-window false; `==` boundary false; `>` true; zero anchor false; production default 300s with injected times (AC-3.2/3.4).
   - `TestAuditGovernanceFirstAttemptAnchorPersists` (`internal/repository/audit_governance_test.go`): enqueue → `first_attempt_at_ns==0`; claim #1 → anchored; retry → unchanged; lease-expire + claim #2 → unchanged (AC-3.1).
   - Re-run `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` — must stay green (C4).
3. **Harness + surface tests** (`internal/api/webdav/dav_governance_test.go`, one commit): variadic branch in `dav_relay_test.go`; `governanceHarnessConfig` (mirror of `runtimeConfig` `runtime_test.go:40-49`, tenant `"default"` instead of `"acme"`); the four test functions of §6; helpers `governanceRows(t, dsn)` / `sqliteMasterIndexes(t, dsn)` (raw SQL, `_ "modernc.org/sqlite"` already imported by the package).
4. **Docs**: `docs/configuration.md:273` (transient retry now cumulative-capped at `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS`; dead-letter semantics identical to permanent classes), `docs/CHANGELOG.md`.
5. **Gate**: `make check` (gofmt · build · vet · full `go test`) + `make test-race` (the new e2e's timing windows must hold under `-race`: AC-3.3 observe budget ~5s vs worst-case terminal at ~3.25s ⇒ ≥1.75s margin; AC-2.x observe window 2.6s vs max backoff 2s — same margin as the repo-level twin `runTerminalCase`).

### 5.2 Schema rollout

- **Fresh install:** version-serial runner applies 0039→0040→0041→0042→0043→**0044** (both dialects); 0044's `ADD COLUMN` requires no prior object beyond the table (0039) — ordering is enforced by the runner (I2).
- **Upgrade (applied ≤0043):** 0044 only — additive column `DEFAULT 0`; zero rewrite; existing rows anchor at their first post-upgrade claim (C10).
- **Rollback (manual only, I2):** `0044.down` (`DROP COLUMN first_attempt_at_ns`) — complete; behavior returns to retry-forever (pre-direction). If 0042 is also rolled back, window-terminalized rows become claimable again — deliberate return to pre-terminal state.
- **Multi-replica:** one migration runner applies 0044 at startup; concurrent startups serialized by the existing runner (unchanged, same as 0042/0043). The anchor `CASE WHEN` is race-free across replicas (fenced claim UPDATE wins exactly one).

---

## 6. Testable acceptance mapping (spec §6 → concrete tests)

All ACs in `internal/api/webdav/dav_governance_test.go` unless noted; every surface case = own httptest receiver + tempdir DB + harness (the `runTerminalCase` isolation shape of `relay_terminal_test.go:58-97`), `t.Run` + `t.Parallel`.

| Spec AC | Test function | Assertions (exact) |
|---|---|---|
| AC-1.1 (wiring shape) | `TestWebDAVGovernanceHarness_WiresMainChain` | harness builds `auditgovernance.New` over `repo.(Store)` → `Start` → `WrapRepository` → `bus.WithRepository(wrapped)` → `svc.WithEventSink(bus)` (main.go:70-82 order); a WebDAV PUT enqueues a governance row on the **file** path (`origin_kind='file'`, `fact_kind='file'`, `action='file.created'`) — proves the wrapped repo reached the bus |
| AC-1.2 (exactly one row per event) | `TestWebDAVGovernance_FileFactsDeliveredExactlyOnce` | healthy receiver (202 + ledgered echo receipt): PUT+DELETE ⇒ exactly **2** rows (`file.created`, `file.deleted` — `factFromEvent` `facts.go:40`), both `delivered_at_ns>0`, exactly 2 POSTs total (receiver counter), stable over 2.6s observe window; the in-tx audit row (`Detail="hard"`, `event_outbox.go` `insertAuditEntry`) enqueues **no** governance row ⇒ row count == 2, not 3 |
| AC-1.3 (config validity) | (implicit — `governanceHarnessConfig` self-honors C-6 + `validAuditGovernanceRetry`; asserted once in `TestWebDAVGovernanceHarness_WiresMainChain` via `cfg.Validate()` nil) | ClaimTTL 3s > 2×HTTPTimeout 1s; MaxBackoffSeconds 2 ≥ InitialBackoffSeconds 1; `Enabled=true` |
| AC-2.1 (≤1 attempt, terminal) | `TestWebDAVGovernance_PermanentClassesTerminal` (table `{http409, http422, malformed-receipt}`; malformed = 202 + non-JSON body) | per case, raw SQL `SELECT failed_at_ns, attempts, last_error`: every row the PUT+DELETE cycle enqueued ⇒ `failed_at_ns>0` AND `attempts==1` AND `last_error` non-empty (409/422 → `httpStatusError` text; malformed → `ErrInvalidReceipt` text) |
| AC-2.2 (never re-claimed) | same test, post-observe | `ClaimAuditGovernance(ctx, observer, token, 1, 10, time.Minute)` → **0 rows** after observe window ≥ 2× max backoff (predicate `claim.go:38,62,88`) |
| AC-2.3 (absent from lag + Ready) | same test | `OldestPendingAuditGovernance` → `ok==false`; `runtime.BacklogAge` → `ok==false`; `runtime.Ready(ctx)` → `nil` (out-param from harness §2.4) |
| AC-2.4 (exactly one POST per row) | same test | receiver POST counter == 2 (rows enqueued), **no growth** during the observe window — the absolute-counting discipline of `TestWebDAVDelete_CompositionL1L2` (`dav_relay_test.go:229`) |
| AC-3.1 (window anchor persistence) | `TestAuditGovernanceFirstAttemptAnchorPersists` (`internal/repository/audit_governance_test.go`) + 0044 file presence (both dialects) | enqueued row `first_attempt_at_ns==0`; claim #1 anchors (≠0); `RetryAuditGovernance` leaves it; lease-expire + claim #2 leaves it (CASE WHEN idempotent); 0043 pending predicate unchanged |
| AC-3.2 (pure window decision) | `TestCumulativeWindowExceededBoundary` (`internal/auditgovernance/`) | injected times: `now−firstAttempt ≤ window` → false (incl. `==` boundary); `>` → true; zero anchor → false; window == `MaxBackoffSeconds` default **300s** read from `config_audit_governance.go:65` (no wall-clock wait) |
| AC-3.3 (e2e: 500 → terminal after window) | `TestWebDAVGovernance_Transient500TerminalAfterWindow` | harness window 2s (MaxBackoffSeconds=2): trace — claim#1 t≈0 (anchor), POST#1 500, retry (within window); claim#2 t≈1.25s (attempts=2), POST#2 500, retry; claim#3 t≈3.25s, POST#3 500, decision `now−anchor > 2s` ⇒ terminal: `failed_at_ns>0`, `attempts==3 ≥ 2`, `last_error` contains the 500 status text, POSTs **stop growing** (observe ≥ 2s post-terminal), `ClaimAuditGovernance` → 0 rows, `OldestPendingAuditGovernance` → none, `Ready()==nil`; bounded wall-clock ~5s |
| AC-3.4 (defaults at production config) | `TestCumulativeWindowExceededBoundary` | same decision function with window 300s + injected times ⇒ terminal only beyond the cumulative window (no 300s wait) |
| AC-4.1 (names + predicate) | `TestWebDAVGovernance_SqliteMasterPartialIndexes` | `SELECT name, sql FROM sqlite_master WHERE type='index' AND tbl_name='audit_governance_outbox'` on the **harness DB** contains `audit_governance_pending_claim_idx` + `audit_governance_pending_lag_idx`, each `sql` containing `WHERE delivered_at_ns = 0 AND failed_at_ns = 0` (regression: 0043 dropped/rewritten non-partial ⇒ fail) |
| AC-4.2 (both dialects) | static (existing `TestAuditGovernance0043DeviationHeaderPinned` `audit_governance_pending_idx_test.go:251` covers the sqlite header; postgres file presence verified in this design's §0) | 0043 up/down present in `migrations/{sqlite,postgres}` |

**Gate:** `make check` + `make test-race` green; non-test files after delta: `relay.go` 236→~248 · `audit_governance_claim.go` 209→~216 · `audit_governance_types.go` 102→~110 — all < 500 (hard gate); new test files < 500 each (convention).

---

## 7. Decisions & non-goals

| # | Decision | Rationale |
|---|---|---|
| D1 | Anchor = **first claim time** (0044), not `created_at_ns` | `created_at_ns` measures enqueue; rows pending for hours before first claim (runtime down, backoff) would be terminal on the *first* transient failure — wrong semantics. Anchor must be first attempt. |
| D2 | Window decision in **relay** (from claim read-back), not in `RetryAuditGovernance` SQL | AC-3.2 requires a pure, injected-time unit test; keeps store signatures stable (C10); the store stays a dumb scheduler |
| D3 | Window == `MaxBackoffSeconds` (spec D3) — no new knob; harness shrinks to 2s | Single "cap 300s" per contract item 1; I5/I6-lean |
| D4 | Harness mode via **variadic option** on the existing `newWebdavRelayHarness` (spec REQ-1 wording), out-param for the runtime | 5 existing callers unchanged (C8); the spec names this harness as the mode carrier |
| D5 | `ErrInvalidEvent` untouched; closed list untouched (spec D2); no `status`/`dead_at` (spec D1) | Scope bound; sibling `internal-ai-…` flip is mutually exclusive (C1) |
| D6 | Window-terminalized facts count `dead_total` (via `failFact`), never `failed_total` | Counter semantics per class stay meaningful (§2.3) |

**Non-goals:** activation-gate matrix e2e (direction 1 of `internal-api-webdav-c346cab0.json`); deterministic fact IDs (direction 3 / T-4, sibling at s3compat); relay metrics / `Ready()` decoupling / 450s alert (B3-2/B3-4, landed at HEAD); events-outbox `maxAttempts` mirroring (governance is time-based); webdav adapter production changes (D5); any `internal/api/s3compat` change.
