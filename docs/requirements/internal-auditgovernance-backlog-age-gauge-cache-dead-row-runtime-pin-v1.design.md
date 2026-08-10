# Design — `internal/auditgovernance` (analysis label: `internal/replication`): backlog-age gauge cache + dead-row runtime pin — test-only delta design (REQ-1 mixed-backlog pin, REQ-2 panic-fake proof)

**Module:** `internal/auditgovernance` (test files only: `runtime_ready_test.go`, `runtime_gauge_scrape_test.go`) — the analysis label `internal/replication` is traceability-only; no `internal/replication` file exists on this surface (see §1).
**Spec:** `docs/requirements/internal-auditgovernance-backlog-age-gauge-cache-dead-row-runtime-pin-v1.spec.md` (REQ-1..3, D1..D5, §5 acceptance table) · **Authoritative upstream design:** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.design.md` (G1 rename, G3 run-loop freshness, D3 cache-fed callback, S11 failed-row runtime pin) · **HEAD:** `15763e2` + uncommitted worktree · **Date:** 2026-08-09
**Scope lock:** test-only delta. REQ-1 (new runtime-level test, mixed backlog) + REQ-2 (panic-fake phase inside the existing scrape test) + REQ-3 (preservation). **Zero production changes**: `runtime.go`, `cmd/server/build.go`, `internal/telemetry/metrics.go`, `internal/repository/*`, config, migration, `.env.example`, alerts, dashboards — none touched.

---

## 1. Verification register (evidence re-read on this checkout, not trusted)

| Evidence claim | Re-verified location (working tree) | Verdict |
|---|---|---|
| Direction surface is `internal/auditgovernance` + `cmd/server/build.go` + `internal/telemetry`, no `internal/replication` file | `ls internal/auditgovernance/` (26 files, no replication pkg); `git grep -l replication internal/` → only unrelated replication worker pkg; analysis file `docs/auto/analyses/internal-replication-9317f27a.json` mislabels the module | ✅ **exact** (documented in spec header) |
| `auditGovernanceBacklogAgeGaugeFn` (build.go:101-108) reads zero-I/O `BacklogAge()` cache getter | `cmd/server/build.go:101-108` — `return int64(rt.BacklogAge().Seconds())`; registration `:153` under `if auditRuntime != nil`; comment documents D3/REQ-5 (no store I/O per scrape) | ✅ **exact** |
| Cache getter `BacklogAge()` at runtime.go:222-228; `Degraded()` :213-220; `recordDegraded` single-Lock pair-write :240-246 | `internal/auditgovernance/runtime.go:222-228` (RLock, returns `r.backlogAge`), `:213-220`, `:239-246` (`degradedMu.Lock()` writes BOTH fields) | ✅ **exact** |
| Run-loop refresh per poll cycle at runtime.go:319-324 (design G3) | `runtime.go:322` — `probeAndRecord(context.Background())` after `cleanupDelivered()` + `stopping()` guard in `run()` (`:297-325`); genuine store error logs + skips, never stops the loop | ✅ **holds** (line drift 319-324 → 322) |
| Store-querying accessor renamed `PendingBacklogAge` at runtime.go:198-207 (G1) | `runtime.go:198-207` — `OldestPendingAuditGovernance` + `time.Since(oldest)`, `ok==false` on empty; doc comment contrasts with the zero-I/O `BacklogAge()` | ✅ **exact** |
| `probeAndRecord` :251-291, `storeProbeTimeout = 2s` :26, `Ready` :293 | `runtime.go:251-291` (both probes under shared timeout; probe ctx-error → `recordDegraded(true,0)` + nil; genuine error → fail-closed), `:26`, `:293` | ✅ **exact** |
| Store predicate at audit_governance_claim.go:211-222: `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` | `internal/repository/audit_governance_claim.go:211-222` — `OldestPendingAuditGovernance` `:211`, query `:218` (same predicate, joined to bindings) | ✅ **exact** |
| T-3 all-terminal runtime pin `TestRuntimeBacklogAgeZeroWhenAllTerminal` (runtime_ready_test.go:299-343) | `runtime_ready_test.go:299-343` — seed → Claim → fenced `FailAuditGovernance(id,"acme","tok","conflict:true")` → `OldestPending ok==false` ∧ `PendingBacklogAge ok==false` ∧ cache `BacklogAge()==0` ∧ `Ready()==nil` ∧ `Degraded()==false` | ✅ **exact** (live PASS, §6) |
| Scrape-surface pin `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (runtime_gauge_scrape_test.go:31-105) | `runtime_gauge_scrape_test.go:31-105` — real runtime over real SQLite, WAL backdate −16 s (> maxLag 4 s), single-shot registration of the truncating callback (`int64(rt.BacklogAge().Seconds())`, production shape), `Ready()`-driven probes, scrape > 0 pending / == 0 after Claim+Fail | ✅ **exact** (live PASS, §6) |
| Run-loop freshness pin (T6) `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` :393-440 | `runtime_ready_test.go:393-440` — zero `Ready()` calls, deadline-poll `Degraded()`, `restorePendingFactAge` WAL fall within one 10 ms poll | ✅ **exact** (live PASS, §6) |
| `runtime_test.go:471-497` → :685-710 still empty-store only | `runtime_test.go:685-710` — `TestRuntimeBacklogAgeZeroWhenNoPending`: empty store `PendingBacklogAge ok==false`, `Ready()==nil`; **no dead-row case** | ✅ **holds** (drift; still empty-only) |
| `audit_governance_test.go:419-449` (repo dead-row pin) stale — moved to :519-560, `ok==false` at :542 | `internal/repository/audit_governance_test.go:419-449` is now `TestAuditGovernanceFirstAttemptAnchorPersists`; the pin is `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` `:519-560` (`OldestPending ok==false` at `:546`; fencing at `:530-535`); predicate-preservation at repo level pinned by `TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts` (`:183`, `ok==true` at `:216`) | ❌ **stale line numbers** (content shipped + moved; spec E6 documents this) |
| REQ-1 (mixed-backlog runtime pin) missing | `grep -rn "TestRuntimeDeadRowsExcludedWhilePendingRowsVisible" internal/` → zero hits; all-terminal pin seeds exactly one fact and kills it — no surviving pending row | ✅ **verified absent** |
| REQ-2 (panic fake) missing | `grep -rn "panicBacklog\|setPanicBacklog" internal/` → zero hits; `scriptedStore` (`runtime_ready_test.go:26-101`) modes: `hang`/`backlogHang`/`lag`/`drainErr`/`backlogErr` — no panic mode; only `panic(` in tests is `relay_metrics_test.go:37` (`TestMain`) | ✅ **verified absent** |
| OTel drops duplicate instruments → single-registration constraint | Scrape test registers `audit_governance.backlog_age_seconds` exactly once; `RegisterAuditGovernanceBacklogAgeGauge` ignores the registration result (`_, _ = m.Int64ObservableGauge(...)`, metrics.go:368-378) — a second registration is silently dropped; `metrics_test.go:114` single-shot idiom | ✅ **holds** (constrains REQ-2, spec E11) |
| `-race` enforcement of the cache-pair contract | `TestRuntimeDegradedCacheConcurrentAccess` (`runtime_ready_test.go:461-517`) under `test-race-meta` (`Makefile:123-127` incl. `./internal/auditgovernance/`) | ✅ **verified** |
| Deterministic harness: `runtimeConfig` maxLag 4 s / poll 10 ms | `runtime_test.go:41-49` — `MaxLagSeconds=4`, `PollMilliseconds=10`, `ClaimTTLSeconds=3`, HTTP 1 s | ✅ **exact** |
| Live test state | `go test ./internal/auditgovernance/ -run 'TestRuntimeBacklogAge\|TestRuntimeRunLoop\|TestRuntimeReady\|TestRuntimeDegraded' -count=1` → all PASS (10.5 s); see §6 | ✅ **verified live** |

**Design-level gaps found during verification (resolved in §2):**

- **G1 — REQ-2 needs the panic to be reachable only from the gauge callback.** The scrape test currently drives a *real* repository store (`runtime_gauge_scrape_test.go:38-46`), which cannot panic on demand. Closure (spec D3): wrap the real repo in `scriptedStore` (same package; `New()`'s `ApplyAuditGovernanceBindings` delegates through it) and add a `panicBacklog` mode. The wrapper is already proven safe for `New()` — `newReadyRuntime` uses the identical construction (`runtime_ready_test.go:145-169`).
- **G2 — Panic-arming discipline is a state-machine, not a boolean.** `setMode` is the total-reset primitive (clears `backlogHang` today); `panicBacklog` must join that reset, and the arm must be a *separate* post-`setMode` overlay (mirroring `setBacklogHang`, `runtime_ready_test.go:102-109`) so a mode transition can never leave the panic armed while a probe is about to run.
- **G3 — REQ-1's optional backdate phase must target the surviving row only.** `backdatePendingFact` rewrites **all** pending rows of tenant `acme` (`WHERE tenant_id='acme' AND delivered_at_ns=0 AND failed_at_ns=0`, `runtime_ready_test.go:353-367`) — safe here because after the fail exactly one pending row remains; the failed row's `created_at_ns` is irrelevant (predicate excludes it), which is precisely the assertion.
- **G4 — Dedupe hazard for REQ-1 seeding.** Fact IDs are store-authoritative deterministic hashes (`internal/repository/audit_governance_factid.go`); two facts colliding on the ID-deriving fields merge into one row. The two seeds must differ in `FactKind`/`Action` (spec REQ-1.1), as `InsertEventWithGovernance` also computes the ID from the fact's final fields.

---

## 2. Design (delta)

### D1 — `scriptedStore` gains `panicBacklog` mode (`runtime_ready_test.go`, test-fake API addition)

New field + overlay setter + predicate check, same discipline as `backlogHang`:

```go
// panicBacklog (overlay, set via setPanicBacklog) makes
// OldestPendingAuditGovernance panic — the REQ-2 proof that the gauge
// callback performs zero store I/O per scrape: a regression that re-adds a
// store query to the callback panics inside the OTel callback (test binary
// crash = loud failure). Arm only after the final Ready() of a phase; setMode
// clears it (same total-reset discipline as backlogHang).
type scriptedStore struct {
    // ...existing fields...
    panicBacklog bool
}

// setPanicBacklog overlays the backlog-probe panic; setMode clears it.
// Apply it only after the final setMode + Ready() of a scenario — while armed,
// any probe (Ready()/Start()) panics by design.
func (s *scriptedStore) setPanicBacklog(p bool) { s.mu.Lock(); defer s.mu.Unlock(); s.panicBacklog = p }

func (s *scriptedStore) OldestPendingAuditGovernance(ctx context.Context) (time.Time, bool, error) {
    s.mu.Lock()
    hang, backlogHang, lag, backlogErr, panicBacklog := s.hang, s.backlogHang, s.lag, s.backlogErr, s.panicBacklog
    s.mu.Unlock()
    if panicBacklog {
        panic("store query from gauge callback") // REQ-2: must be unreachable from the gauge path
    }
    // ...existing hang/lag/err branches unchanged...
}
```

Three required edits: `setMode` (`runtime_ready_test.go:66-75`) gains `s.panicBacklog = false` in the same lock as the `backlogHang` clear; the `OldestPendingAuditGovernance` snapshot (`:78-86`) gains the new field; the panic branch goes before the hang branch (panic is orthogonal to hang). No other method panics; `HasPendingDrainingAuditGovernance` untouched.

### D2 — REQ-1: `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` (`runtime_ready_test.go`, new sibling to the all-terminal pin)

Same harness as `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`newReadyRuntime`, `runtimeConfig("http://127.0.0.1:1")` — loopback, no network), **never `Start()`** (Ready-driven only, so no claim/retry races):

1. **Seed two distinct facts** via `InsertEventWithGovernance` (same tenant `acme`; distinct `FactKind`/`Action` — G4, deterministic-ID dedupe must not merge them). Assert both are pending (`OldestPending ok==true` baseline).
2. **Terminalize one**: `ClaimAuditGovernance(ctx, "acme", "tok", 1, 1, time.Minute)` (limit 1) → `FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "conflict:true")` — the lease-fenced public API (fencing already pinned at repo level, `audit_governance_test.go:530-535`).
3. **Assert predicate preservation** (the T-3 second clause):
   - `OldestPendingAuditGovernance(ctx)` → `ok==true` (surviving pending row is the oldest pending);
   - `PendingBacklogAge(ctx)` → `ok==true`, `age > 0` (runtime accessor sees the surviving row; gauge source non-zero);
   - `Ready(ctx) == nil` ∧ `Degraded() == false` (surviving row's age < maxLag 4 s — no backdating in this phase);
   - cache `BacklogAge() > 0` after the `Ready()` probe.
4. **Optional deterministic age phase** (G3): WAL-backdate the surviving row −16 s via the existing `backdatePendingFact` helper (`runtime_ready_test.go:353-367` — matches *only* the still-pending row), `Ready()` again → `Degraded()==true` ∧ cache `BacklogAge() > 4s` **while the failed row is still present** — proving the lag is computed from the pending row only; a terminal row contributes 0 regardless of its `created_at_ns`.

*No sleeps; no timing assertions; the 2 s `storeProbeTimeout` never engages (healthy store).*

### D3 — REQ-2: panic phase folded into `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go`)

The single-registration constraint (E11) forces the panic proof into the existing scrape test — a separate test with its own `RegisterAuditGovernanceBacklogAgeGauge` is silently dropped by OTel and can prove nothing.

1. **Wrap the store**: replace the bare `repo.(Store)` with `scripted := &scriptedStore{store: store}` and pass `scripted` to `New()` — identical to `newReadyRuntime` (`runtime_ready_test.go:161-163`); `New()`'s binding apply and every existing phase keep working through the delegate.
2. **Phase sequence** (existing phases untouched, panic phase inserted between pending and dead-only):
   - *(unchanged)* seed one fact, backdate −16 s, register the truncating callback, `Ready()` → cache age > maxLag, scrape > 0.
   - *(new)* `scripted.setPanicBacklog(true)` — **then no further `Ready()`/`Start()` calls** (a probe would panic the runtime goroutine by design; the cache is deliberately frozen at the last-probe value — exactly the semantics under test). `scrapeProm(t, "audit_governance_backlog_age_seconds")` → **same cached value, no panic**. Any regression re-adding a store query to the callback panics inside the OTel callback → test binary crash → loud failure.
   - *(unchanged)* restore health (`setMode(false, false, nil, nil)` — clears `panicBacklog` per D1), `Ready()`, Claim + Fail, `Ready()` → scrape == 0.
3. **Composition of the proof**: *callback reads cache, never the store* (this panic phase) + *run loop keeps the cache fresh with zero `/readyz` traffic* (T6, `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls`) + *dead rows excluded* (T5 + REQ-1) + *series emitted* (REQ-3).

### D4 — Preservation pins (must stay green, not re-written)

`TestRuntimeBacklogAgeZeroWhenAllTerminal` (:299), `TestRuntimeBacklogAgeZeroWhenNoPending` (runtime_test.go:685), `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (:393), `TestRuntimeRunLoopSurvivesWedgedStore` (:442), `TestRuntimeDegradedCacheConcurrentAccess` (:461), `TestRuntimeReadyDegradedSentinel` (:194), `TestRuntimeReadyFailClosedOnGenuineStoreError` (:251), repo pins `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` + `TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts`. The scrape test is extended **only** by the store wrap + panic phase (D3); its existing assertions stay verbatim.

---

## 3. API changes

| Surface | Change | Kind |
|---|---|---|
| `scriptedStore.panicBacklog` (field) + `setPanicBacklog(bool)` | **Add** (test fake only, package-internal) | Test-only API |
| `setMode` total-reset | **Extend**: also clears `panicBacklog` | Test-only |
| `OldestPendingAuditGovernance` (scripted) | **Extend**: panic branch before hang/err branches | Test-only |
| `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` | **Add** (`runtime_ready_test.go`) | New test |
| `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` | **Extend**: scripted wrap + panic phase (D3) | Test-only |
| `Runtime.BacklogAge()` / `Degraded()` / `PendingBacklogAge(ctx)` / `probeAndRecord` / `recordDegraded` (`runtime.go`) | **Unchanged** | None |
| `auditGovernanceBacklogAgeGaugeFn` (`build.go:101-108`) | **Unchanged** | None |
| `RegisterAuditGovernanceBacklogAgeGauge` (`metrics.go:364-378`) | **Unchanged** (single registration per binary preserved) | None |
| Config / schema / migration / `.env.example` / alerts / dashboards | **Unchanged** | None |

The direction's original production API (per-scrape `BacklogAge(ctx)` store query) is already gone from this tree (E1/E3); no production API is added, removed, or renamed by this delta.

---

## 4. Compatibility constraints

- **C1 — OTel single-registration (hard).** `RegisterAuditGovernanceBacklogAgeGauge` ignores its registration result (`_, _ =`, metrics.go:371); a duplicate observable-gauge registration on the `aero-vault/domain` meter is dropped. The panic proof **must** live inside `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime`; any new test registering the gauge again is a silent no-op (D3).
- **C2 — Prometheus handler skip guard.** The scrape test skips when `promHandler == nil` (test-main side effect, `relay_metrics_test.go:30-44`). The panic phase inherits that skip — it proves nothing when run standalone, by design (same as the existing phases).
- **C3 — Panic-arming state machine (hard).** `panicBacklog` is armed only after the final `Ready()` of the pending phase; `Ready()`/`Start()` are never called while armed; `setMode` (the total reset) clears it. Violations are deterministic crashes — the panic is the proof signal, not a flake.
- **C4 — Delegate construction required.** `New()` runs `ApplyAuditGovernanceBindings` during construction; the scripted wrapper delegates all non-probe methods unconditionally (existing contract, `runtime_ready_test.go:111-144`), so both the REQ-2 wrap and `newReadyRuntime` are safe.
- **C5 — Seed dedupe.** Fact IDs are store-authoritative deterministic hashes; REQ-1's two seeds must differ in `FactKind`/`Action` (G4). Terminalization goes through the lease-fenced public API only (never direct SQL) so the repo-level fencing contract stays the single authority.
- **C6 — Determinism / CI.** No sleeps, no network (loopback base URL), no timing assertions; WAL second-writer backdating is the only time control (plain DSN — the run loop is **not** started in REQ-1/REQ-2's scrape test, so no busy-timeout pragma needed; contrast `restorePendingFactAge`'s pragma, needed only when the loop is live, T6). `-race` unaffected: `panicBacklog` is guarded by the existing `scriptedStore.mu`; the cache-pair concurrency contract is pinned separately (`TestRuntimeDegradedCacheConcurrentAccess`, C7).
- **C7 — Engineering gates (I6, 500-line rule).** No new `go.mod` dependencies; new code confined to `*_test.go` (excluded from the 500-line single-file gate); `gofmt`/`build`/`vet`/`go test ./...`/`test-race-meta` must stay green (`make check`).
- **C8 — Scope.** Zero production diffs; the sibling alert-threshold direction, `/readyz` payload/helm, config wording, and alert/config/metric changes are out of scope (spec §4 non-goals).

---

## 5. Failure modes

| # | Failure mode | Trigger | Detection / behavior |
|---|---|---|---|
| F1 | Gauge callback regresses to a per-scrape store query | A future edit re-adds `PendingBacklogAge`/`OldestPendingAuditGovernance` to the callback chain | REQ-2 panic phase: callback panics inside the OTel observe → **test binary crash** (loud). This is the direction's core regression; no alert could catch it earlier (scrape-side failure, D1-drill surface) |
| F2 | Store predicate regresses (terminal rows counted / pending rows hidden) | Predicate edit drops `failed_at_ns=0` or adds a terminal filter | REQ-1: `ok==false` on the mixed backlog / `BacklogAge()==0` asserts fail; T5 all-terminal pin also breaks |
| F3 | Cache freshness regresses (run loop stops refreshing) | `probeAndRecord` removed from `run()` or gated | T6 (`TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls`) fails — deadline-polled `Degraded()`/`BacklogAge()` |
| F4 | Cache pair-write regresses (degraded/age torn) | `recordDegraded` split into two lock acquisitions | `TestRuntimeDegradedCacheConcurrentAccess` fails under `test-race-meta` |
| F5 | Wedged store stalls a scrape | A future callback calls the store directly with the caller ctx | REQ-2: the panic fake can't fire (no store call = no panic) — but F1 covers the direction; a *non-panicking* store query would still be caught by F1 only if it panics... residual: a wedged non-panicking query would hang the scrape; guarded by REQ-5 semantics (cache-only) + F1 crash proof for the regression class |
| F6 | Panic-fake misuse in test edits | `Ready()`/`Start()` called while armed, or `setMode` forgets the clear | Deterministic panic with the sentinel message "store query from gauge callback" — points at the misuse; `setMode` clear (D1) makes stale arms impossible across mode transitions |
| F7 | REQ-1 seed dedupe merges the two facts | Identical ID-deriving fields | Baseline assert (`OldestPending ok==true` after seeding both) fails with count 1 — immediate, no flakes |
| F8 | Scrape-series regression (gauge dropped/renamed) | Registration removed or metric renamed | REQ-3: `scrapeProm` returns not-found → `t.Fatal`; the 450 s alert (`AuditGovernanceBacklogDegraded`, alerts.yml) would otherwise go silent with all other probes green |
| F9 | All-terminal probe timeout reports 0 (fail-open gauge) | Store wedges with a dead-lettered backlog | By design (probe timeout → `recordDegraded(true, 0)`); the degraded signal is alert-driven (B3-2), not gauge-driven — unchanged by this delta, pinned by T1/T2 wedged-store tests |

---

## 6. Migration steps

No data/schema/config migration — the delta is test-only (spec D1). Implementation order:

1. **D1** — add `panicBacklog` to `scriptedStore`: field, `setPanicBacklog`, `setMode` clear, panic branch in `OldestPendingAuditGovernance` (`runtime_ready_test.go`).
2. **D2/REQ-1** — add `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` next to the all-terminal pin; run it in isolation first (`-run TestRuntimeDeadRowsExcludedWhilePendingRowsVisible -count=1`).
3. **D3/REQ-2** — wrap the scrape test's store in `scriptedStore`; insert the panic phase; keep existing phases verbatim.
4. **Gate** — `go test ./internal/auditgovernance/ ./internal/telemetry/ ./internal/repository/ -count=1`, then `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` incl. `./internal/auditgovernance/` / cli-check).
5. **Rollback** — revert the three test-file hunks; production code is untouched, so rollback is trivially complete.

Expected footprint: ~+40 lines in `runtime_ready_test.go` (panic mode + REQ-1), ~+20 lines in `runtime_gauge_scrape_test.go` (wrap + panic phase).

---

## 7. Testable acceptance mapping

| Direction acceptance clause | Pin (test → assertion) | Status |
|---|---|---|
| T-3 runtime-level: pending row → `FailAuditGovernance(owner, token, cause)` → `BacklogAge() ok==false` (gauge 0) ∧ `Ready()==nil` | `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299-343`): `OldestPending ok==false` ∧ `PendingBacklogAge ok==false` ∧ cache `BacklogAge()==0` ∧ `Ready()==nil` ∧ `Degraded()==false`; gauge-0 surface: scrape test dead-only phase (scrape == 0) | ✅ shipped |
| T-3 non-terminal rows still visible (predicate preserved) | **REQ-1** → `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` (new): 2 seeds → fail 1 → `OldestPending ok==true` ∧ `PendingBacklogAge ok==true, age>0` ∧ `Ready()==nil` ∧ `Degraded()==false` ∧ cache `BacklogAge()>0`; optional −16 s backdate phase: `Degraded()==true` ∧ cache > 4 s with the failed row present | ❌ **this delta** |
| B3-4: store fake panics on `OldestPending` → gauge callback reads cached last-probe value (refresh by run loop), never queries per scrape | **REQ-2** → panic phase in `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime`: arm `setPanicBacklog(true)` after pending phase → `scrapeProm` returns the same cached value, no panic (store call from callback = binary crash). Run-loop refresh half: `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`:393-440`, zero `Ready()` calls) | ❌ **this delta** |
| B3-4: scrape-surface test asserting `audit_governance_backlog_age_seconds` emitted | `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`:31-105`): series present via `scrapeProm` (line-exact), > 0 pending, == 0 dead-only | ✅ shipped |

**Acceptance = green CI on:** `go test ./internal/auditgovernance/ -count=1` (all pins incl. the two new ones) + `make check` (race suite incl. `./internal/auditgovernance/`). Every clause maps to exactly one test; no clause is asserted by alert config or manual inspection.

---

## 8. Risks & gates

- **Stale-citation hazard:** the direction's `build.go:113-120` per-scrape query and `audit_governance_test.go:419-449` do not hold on this tree (E1/E6) — implementers must read the verified locations in §1, not the analysis numbers.
- **OTel duplicate-instrument rejection (C1):** the highest-stakes mechanical risk of REQ-2; mitigated by D3's single-test folding.
- **Panic-mode safety (C3/F6):** armed state is a test-only fake with a total-reset primitive; the sentinel message makes misuse self-diagnosing.
- **Flake profile:** zero sleeps, zero timing assertions, WAL-backdate determinism; the 2 s probe bound engages only in wedged-store tests (untouched).
- **Hard gates:** test-only delta; `gofmt`/`build`/`vet` untouched; no new `go.mod` dependencies (I6); single-file 500-line gate excludes `*_test.go` (Makefile:178-182); `make check` + `test-race-meta` enforce the cache-pair contract.

*Verification basis: every citation re-read on this checkout (HEAD `15763e2` + uncommitted worktree); line numbers reflect the working tree as read on 2026-08-09; targeted package tests re-run live (§6 live evidence: `go test ./internal/auditgovernance/ -run 'TestRuntimeBacklogAge|TestRuntimeRunLoop|TestRuntimeReady|TestRuntimeDegraded' -count=1` → all PASS, 10.5 s).*
