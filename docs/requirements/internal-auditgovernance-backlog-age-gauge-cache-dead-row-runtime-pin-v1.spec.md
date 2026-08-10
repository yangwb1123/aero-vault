# Requirements Specification — `internal/auditgovernance` (analysis label: `internal/replication`): backlog-age gauge reads the cached last-probe value (zero store I/O per scrape) + runtime-level dead-row (terminal-only) pin

**Module:** analysis label says `internal/replication`; the actual surface is `internal/auditgovernance` (`Runtime` cache getters + run-loop refresh) + `cmd/server/build.go` (gauge callback) + `internal/telemetry/metrics.go` (gauge registration) + `internal/repository/audit_governance_claim.go` (predicate). The analysis file `docs/auto/analyses/internal-replication-9317f27a.json` carries auditgovernance directions under a mislabeled module name — no `internal/replication` file is touched.
**Direction:** "B3-4/D1: backlog-age gauge performs a live store query on every /metrics scrape; cache it and pin the dead-row (terminal-only) runtime case"
**Source analysis:** `docs/auto/analyses/internal-replication-9317f27a.json` (direction 2 of 3)
**Design (authoritative):** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.design.md` — G3 (run-loop gauge freshness), D3 ("gauge callback reads the cache, never the store"), S11 ("failed-row case unpinned at runtime level")
**Sibling spec (D1-drill read-path half, shipped in the same worktree):** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v2.spec.md` (REQ-1..8, T1..T8)
**Date:** 2026-08-09 · **Verification basis:** current working tree (HEAD `15763e2` + uncommitted worktree; `git status` shows `runtime.go`, `metrics.go`, `runtime_ready_test.go`, `runtime_gauge_scrape_test.go` all carrying the D1/B3-4 delta)
**Score:** value 7 / risk reduction 6 / effort 3 / confidence 8

---

## 1. Module, scope & state on this checkout

**Delta scope (locked to the direction):** (a) the per-scrape store query behind `audit_governance_backlog_age_seconds` is replaced by a cached last-probe value, refreshed by the runtime loop, so a wedged repository can never stall `/metrics`; (b) the T-3 "terminal rows excluded from lag" case, pinned only at repository level, gets its runtime-level pin *including predicate preservation* (non-terminal rows remain visible). Scope is **test-only**: the production halves of both items already shipped in this worktree (verified §2); the two unpinned acceptance clauses (§3 REQ-1/REQ-2) are new tests.

**State on this checkout (verified live):**

| Item | Status |
|------|--------|
| Gauge callback reads zero-I/O cache getter `rt.BacklogAge().Seconds()` (`cmd/server/build.go:101-108`), registered `:153` | ✅ shipped |
| Cache written by `recordDegraded` under a single `degradedMu.Lock()`; `BacklogAge()`/`Degraded()` RLock getters (`internal/auditgovernance/runtime.go:240-246`, `:213-220`, `:222-228`) | ✅ shipped |
| Run loop refreshes cache once per poll cycle via `probeAndRecord(context.Background())` after `cleanupDelivered()` + `stopping()` guard (`runtime.go:319-324`) — design G3 | ✅ shipped |
| `PendingBacklogAge(ctx)` store-querying accessor (the renamed `BacklogAge(ctx)`; G1) at `runtime.go:198-207` | ✅ shipped |
| T-3 all-terminal runtime pin: `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299-343`) | ✅ shipped |
| Scrape-surface pin: `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31-105`) | ✅ shipped |
| Run-loop freshness pin (zero `Ready()` calls): `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`) | ✅ shipped |
| **T-3 "non-terminal rows still visible (predicate preserved)" at runtime level** — no test seeds a sibling still-pending row alongside a failed row | ❌ missing (§3 REQ-1) |
| **Store fake that panics on `OldestPendingAuditGovernance` proving the gauge callback performs zero store I/O per scrape** — `scriptedStore` has hang/err/lag modes but no panic mode (`runtime_ready_test.go:26-101`); the scrape test drives a real store, so a regression that re-adds a store query to the callback would pass | ❌ missing (§3 REQ-2) |

**Out of scope:** the 450s-alert-threshold single-source direction (sibling, `internal-auditgovernance-backlog-alert-threshold-single-source-v1.spec.md`), the D1-drill read-path half (sibling v2 spec, shipped), any production-code change (`runtime.go` / `build.go` / `metrics.go` / `http.go` / repository), any config/migration/`.env.example` change, any new metric, any alert/counter change.

---

## 2. Evidence verification (re-read on this checkout, not trusted)

| # | Direction citation | Verified location (current working tree) | Verdict |
|---|---|---|---|
| E1 | `cmd/server/build.go:113-120` gauge callback → `BacklogAge(ctx)` store query per scrape | `auditGovernanceBacklogAgeGaugeFn` at `cmd/server/build.go:101-108`: `return int64(rt.BacklogAge().Seconds())` — **zero store I/O** (cache getter); registration at `:153` under `if auditRuntime != nil` | ❌ **stale** — the per-scrape store query is *gone* (design D3 landed). The direction's premise held at the analysis snapshot; the cache swap shipped in this worktree |
| E2 | `internal/telemetry/metrics.go:352-365` `RegisterAuditGovernanceBacklogAgeGauge` | `metrics.go:368-378` — `Int64ObservableGauge("audit_governance.backlog_age_seconds")`, callback per scrape reads `fn(ctx)`; doc comment `:364-367` | ✅ **holds** (line drift 352-365 → 368-378) |
| E3 | `internal/auditgovernance/runtime.go:146-159` `BacklogAge` → `OldestPendingAuditGovernance` | Store-querying accessor renamed `PendingBacklogAge(ctx)` at `runtime.go:198-207` (G1 rename; `OldestPendingAuditGovernance` + `time.Since`); the zero-I/O `BacklogAge()` cache getter at `:222-228` (RLock on `degradedMu`); `Degraded()` `:213-220`; `recordDegraded` single-Lock write `:240-246`; `probeAndRecord` (both probes under `storeProbeTimeout = 2s`, `:26`) `:251-291`; run-loop refresh `:319-324` | ✅ **holds** (line drift + rename; semantics per design G1/G3/D3) |
| E4 | `internal/repository/audit_governance_claim.go:188-222` `OldestPending` predicate `WHERE delivered_at_ns=0 AND failed_at_ns=0` | `OldestPendingAuditGovernance` at `:211-222` — `SELECT MIN(o.created_at_ns) FROM audit_governance_outbox o JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` | ✅ **holds** (line drift: 211-222) |
| E5 | `internal/auditgovernance/runtime_test.go:471-497` empty-store-only coverage | `TestRuntimeBacklogAgeZeroWhenNoPending` at `runtime_test.go:685-710` — empty store: `PendingBacklogAge ok==false`, `Ready()==nil`; no dead-row case | ✅ **holds** (line drift; still empty-store only) |
| E6 | `internal/repository/audit_governance_test.go:419-449` repo-level dead-row pin (`OldestPending ok==false`) | `:419-449` is now `TestAuditGovernanceFirstAttemptAnchorPersists`. The dead-row pin is `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` at `:519-560`, `OldestPending ok==false` asserted at `:542` (Claim → fenced Fail → terminal). Repo-level *predicate preservation* (pending rows visible) is pinned at `:216` (`TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts`: `OldestPending ok==true` with pending rows present) | ❌ **stale line numbers** (content shipped, moved 419-449 → 519-560/542) |
| E7 | No runtime-level dead-row pin exists | `grep -n "Test.*Terminal\|AllTerminal" internal/auditgovernance/*_test.go` → `relay_terminal_test.go` (relay classification, not backlog-age), `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299`); the latter seeds exactly **one** fact and kills it — no surviving pending row, so "predicate preserved" is unpinned at runtime level | ✅ **verified absent** (the specific clause) |
| E8 | No store fake panics on `OldestPendingAuditGovernance` | `grep -n "panic" internal/auditgovernance/*_test.go` → single hit `relay_metrics_test.go:37` (`panic(err)` in `TestMain`, unrelated). `scriptedStore` (`runtime_ready_test.go:26-101`) modes: `hang`/`backlogHang` (block on `<-ctx.Done()`), `lag` (2h backdate), `drainErr`/`backlogErr` (immediate error) — no panic mode | ✅ **verified absent** |
| E9 | Run-loop refresh exists (the "refresh by runtime loop" half of the acceptance) | `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`): seed + WAL-backdate −16s, `Start(ctx)`, zero `Ready()` calls, deadline-poll `Degraded()==true`, cache `BacklogAge()>4s`; `restorePendingFactAge` (WAL second writer) drives the fall within one poll cycle (10 ms); pending row still pending at flip (cumulative-window terminal path excluded) | ✅ **verified live** (test passes, §6) |
| E10 | Scrape-surface test exists | `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31-105`) — real runtime over real SQLite; backdate −16s (`> maxLag 4s`); single-shot registration of the truncating callback (mirrors `auditGovernanceBacklogAgeGaugeFn` shape); `Ready()`-driven probes (never `Start()`); scrape → gauge > 0 pending, gauge == 0 after Claim+Fail | ✅ **verified live** (test passes, §6) |
| E11 | Single-registration rule for the OTel gauge | The test binary registers `audit_governance.backlog_age_seconds` exactly once (`runtime_gauge_scrape_test.go:31-105`); OTel rejects a duplicate instrument on the same meter (v2 spec §6, `metrics_test.go:114` idiom) — any new gauge proof must live inside the existing scrape test | ✅ **holds** (constrains REQ-2) |
| E12 | `-race` enforcement of the cache-pair contract | `Makefile:123-127` `test-race-meta` now includes `./internal/auditgovernance/` (with the sibling v2 delta); `TestRuntimeDegradedCacheConcurrentAccess` (`runtime_ready_test.go:461-517`) | ✅ **verified** |

---

## 3. Requirements

### REQ-1 — Runtime-level predicate preservation: dead rows excluded, pending rows still visible (T-3 second clause)

Add `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` in `internal/auditgovernance/runtime_ready_test.go` (sibling to the shipped all-terminal pin; reuse `newReadyRuntime`, `runtimeConfig`, `backdatePendingFact` — do **not** rewrite `TestRuntimeBacklogAgeZeroWhenAllTerminal`, which stays as the all-terminal preservation pin):

1. Seed **two** facts via `InsertEventWithGovernance` (`acme/b/k`, distinct `FactKind`/`Action` so the enqueue dedupe does not merge them).
2. `ClaimAuditGovernance(ctx, "acme", "tok", 1, 1, time.Minute)` (limit 1) → `FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "conflict:true")` — the lease-fenced public API (`internal/repository/audit_governance_claim.go:182-194`; fencing already pinned at `audit_governance_test.go:530-535`).
3. Assert the store predicate is preserved, not just dead-row-excluded:
   - `OldestPendingAuditGovernance(ctx)` → `ok==true` (the surviving pending row is the oldest pending).
   - `PendingBacklogAge(ctx)` → `ok==true`, `age > 0` (runtime accessor sees the surviving row; gauge source non-zero).
4. Assert the readiness semantics of a *mixed* backlog: `Ready(ctx) == nil`, `Degraded() == false` (age of the surviving row < maxLag 4 s — no backdating in this test), cache `BacklogAge() > 0` after the `Ready()` probe.
5. Optional deterministic age assertion: WAL-backdate the surviving row −16 s (existing `backdatePendingFact` helper), `Ready()` again → `Degraded()==true`, cache `BacklogAge() > 4s` **while the failed row is still present** — proving the lag is computed from the pending row only (the terminal row contributes 0 regardless of its `created_at_ns`).

*Testable:* all assertions on the store/runtime public API; no sleeps; no network (loopback publisher base URL, `runtimeConfig("http://127.0.0.1:1")`).

### REQ-2 — Panic-fake proof: the gauge callback performs zero store I/O per scrape (B3-4)

Extend `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`internal/auditgovernance/runtime_gauge_scrape_test.go`) with a panic phase, **inside the same test function** (E11: the single-shot registration must not be duplicated; OTel drops a duplicate instrument):

1. Refactor the test's store construction to wrap the real repo in `scriptedStore` (same package, `runtime_ready_test.go:26-101`) — `New()` still runs `ApplyAuditGovernanceBindings` through the delegate.
2. Add a `panicBacklog bool` mode to `scriptedStore` (guarded by the existing `mu`; `setMode` total-reset clears it, same discipline as `setBacklogHang`): `OldestPendingAuditGovernance` calls `panic("store query from gauge callback")` when set. No other method panics.
3. Phase sequence (existing phases unchanged):
   - Seed one fact, backdate −16 s, register the truncating callback (`int64(rt.BacklogAge().Seconds())`, production shape), `Ready()` → cache age > maxLag; scrape → `audit_governance_backlog_age_seconds > 0` (existing pending phase).
   - `scripted.setPanicBacklog(true)` — **then never call `Ready()` or `Start()` again** (a probe would panic the runtime goroutine by design; the cache is deliberately frozen at the last-probe value, which is exactly the semantics under test).
   - Scrape again via `scrapeProm` → must return the **same cached value** (no panic, no change). Any regression that re-adds a store query to the callback panics inside the OTel callback → the test binary crashes → loud failure.
   - Restore health (`setMode(false,false,nil,nil)`), `Ready()`, Claim+Fail, `Ready()` → scrape == 0 (existing dead-only phase).
4. The "refresh by run loop" half of the acceptance is already pinned by `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (E9) — the composed proof is: *callback reads cache* (this panic phase) + *run loop keeps cache fresh with zero `/readyz` traffic* (E9, T6).

*Testable:* the panic is unreachable in the healthy path (probes are `Ready()`-driven before arming), deterministic, and needs no timing assertions; the scrape assertions reuse `scrapeProm`/`scrapeValue` (`relay_metrics_test.go:62-80`).

### REQ-3 — Scrape-surface emission pin (preservation, shipped)

`audit_governance_backlog_age_seconds` must remain emitted by the Prometheus handler with the production callback shape. **Already pinned** by `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31-105`): series present in the `/metrics` body, > 0 with a stale pending fact, exactly 0 when only dead rows remain. This is the B3-4 acceptance's third clause and the 450s alert source (`alerts.yml` `AuditGovernanceBacklogDegraded`); keep the test green, extend it only per REQ-2.

---

## 4. Decisions & non-goals

- **D1 — Test-only delta.** Both production halves (cache swap D3, run-loop refresh G3, `PendingBacklogAge` rename G1) are already in the working tree (§2 E1/E3/E9). The remaining work is two test clauses; no production file changes.
- **D2 — Panic proof must live inside the single-registration scrape test** (E11): a second test registering the gauge is dropped by OTel, so the panic phase extends `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` rather than a new test with its own registration.
- **D3 — Panic mode is an addition to `scriptedStore`, not a new fake**: the store already centralizes probe injection (`hang`/`lag`/`backlogHang`/`*Err`), and the delegate-based construction is required for `New()`'s binding apply. Guarded by the existing `mu`; cleared by `setMode`'s total reset.
- **D4 — `TestRuntimeBacklogAgeZeroWhenAllTerminal` is a preservation pin, not a rewrite target**: REQ-1 adds a sibling test for the mixed-backlog case; the all-terminal semantics (ok==false, age 0, `Ready()==nil`) stay asserted verbatim.
- **D5 — No panic-recovery in production**: the panicking fake is a test-only proof that the callback never reaches the store; `runtime.go` unchanged (a store panic remains a crash — out of scope, and the run-loop/wedge survival is already pinned by `TestRuntimeRunLoopSurvivesWedgedStore`).
- **Non-goals:** alert-threshold single-source work (sibling direction), `/readyz` payload/helm (sibling D1-drill v2 spec), any config knob, any new metric, repository predicate changes, `docs/configuration.md` wording.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

| Direction acceptance clause | Status | Testable pin |
|---|---|---|
| **T-3:** runtime-level test seeding a pending row then `FailAuditGovernance(owner, token, cause)` → `BacklogAge() ok==false` (gauge 0) ∧ `Ready()==nil` | ✅ shipped | `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299-343`): seed → Claim → fenced `FailAuditGovernance(ctx, id, "acme", "tok", "conflict:true")` → `OldestPendingAuditGovernance ok==false` ∧ `PendingBacklogAge ok==false` ∧ cache `BacklogAge()==0` ∧ `Ready()==nil` ∧ `Degraded()==false`. Gauge-0 surface: `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` dead-only phase (`runtime_gauge_scrape_test.go:86-104`, scrape == 0) |
| **T-3:** non-terminal rows still visible (predicate preserved) | ❌ **REQ-1** | `TestRuntimeDeadRowsExcludedWhilePendingRowsVisible` (new): two facts → fail one → `OldestPending ok==true` ∧ `PendingBacklogAge ok==true, age>0` ∧ `Ready()==nil` ∧ `Degraded()==false` ∧ cache `BacklogAge()>0`; optional −16 s backdate phase keeps the failed row present while `Degraded()==true` (lag from pending row only) |
| **B3-4:** store fake that panics on `OldestPending` proving the gauge callback reads a cached last-probe value (refresh by runtime loop) instead of querying per scrape | ❌ **REQ-2** | Panic phase inside `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime`: `scriptedStore` gains `panicBacklog` mode; after the pending phase (cache > 0), arm panic and scrape → same cached value, no panic (any store call from the callback crashes the binary). Run-loop refresh half already pinned: `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`, zero `Ready()` calls) |
| **B3-4:** scrape-surface test asserting `audit_governance_backlog_age_seconds` still emitted | ✅ shipped | `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31-105`): series present via `scrapeProm`/`scrapeValue` (line-exact parse), > 0 pending, == 0 dead-only |

**Preservation pins (must stay green, not re-written):** `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:685`), `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393`), `TestRuntimeRunLoopSurvivesWedgedStore` (`:442`), `TestRuntimeDegradedCacheConcurrentAccess` (`:461`, `-race` via `test-race-meta`, `Makefile:123-127`), `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31`), `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`, repo-level terminal pin), `TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts` (`audit_governance_test.go:183`, repo-level predicate-preservation pin).

---

## 6. Risks & gates

- **Stale-citation hazard (documented in §2):** the direction's `build.go:113-120` per-scrape query and `audit_governance_test.go:419-449` no longer hold — the cache swap shipped (E1) and the repo-level pin moved (E6). Implementers must read current lines, not the analysis numbers.
- **OTel duplicate-instrument rejection (E11):** REQ-2 must extend the existing scrape test's single registration; a separate test with its own `RegisterAuditGovernanceBacklogAgeGauge` call is silently dropped and cannot prove anything.
- **Panic-mode safety:** `panicBacklog` must be armed only after the final `Ready()` of the pending phase, and never while `Start()` is running — the panic is the proof, and it must be reachable only by the gauge callback.
- **Flake profile:** REQ-1 uses no sleeps (WAL backdating for the optional age phase); REQ-2 needs no timing assertions; both reuse the deterministic harness (`runtimeConfig` maxLag 4 s / poll 10 ms, `runtime_test.go:41-49`).
- **Hard gates:** test-only delta — `gofmt`/`build`/`vet` untouched; new code confined to `*_test.go` (the 500-line single-file gate excludes test files, `Makefile:178-182`); no new `go.mod` dependencies (I6).
- **Live evidence (run during spec production):** `go test ./internal/auditgovernance/ ./internal/telemetry/ ./internal/repository/ -count=1` → `ok` (32.9 s / 0.02 s / 39.3 s); the three shipped pins cited in §2 E9/E10 ran green in the auditgovernance pass.
- **Gate:** `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` incl. `./internal/auditgovernance/` / cli-check), SQLite + local FS, zero network/Docker.

*Verification basis: every citation re-read on this checkout (HEAD `15763e2` + uncommitted worktree); line numbers reflect the working tree as read during this spec's production; package tests re-run live on 2026-08-09 (§6).*
