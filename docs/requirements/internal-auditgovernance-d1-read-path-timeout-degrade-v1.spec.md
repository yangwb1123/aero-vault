# Requirements Specification — `internal/auditgovernance`: D1 drill half — bound the readiness/gauge read path and degrade on timeout instead of 503

**Module:** `internal/auditgovernance` (read path) + seam consumers `cmd/server/http.go` (`readyzHandler`) and `cmd/server/build.go` (gauge callbacks)
**Direction:** "D1 drill half: bound the audit-governance readiness/gauge read path and degrade on timeout instead of 503" (direction 1 of `docs/auto/analyses/internal-auditgovernance-ef1a62fa.json`)
**Contract:** `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 / D1 ("read-path timeouts degrade instead of 503"); sibling shipped spec `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` (REQ-1..4, AC-1)
**Date:** 2026-08-08 · **HEAD:** `15763e2` + worktree (verification basis) · **Score:** value 9 / risk reduction 8 / effort 4 / confidence 9

---

## 1. Status statement (what exists vs. what this direction requires)

**This direction is already shipped in the current worktree.** Every acceptance check (a)–(d) has a production implementation and a passing test pin. The analysis (2026-08-07 23:38) predates the worktree changes (2026-08-08 04:00–06:16) that implemented the D1 read-path half; all five evidence citations therefore describe the *pre-ship* state and are verified here against the *current* tree. This spec is the **regression contract**: the implement stage is expected to be zero-production-delta — verify the pins below exist and pass, add a pin only if one is missing.

**Shipped inventory (verified this worktree):**

| # | Shipped item | Evidence (current worktree) |
|---|---|---|
| S1 | `Runtime.Ready`'s two store probes bounded by `storeProbeTimeout = 2s` (mirror of `readyzProbeTimeout`) | `internal/auditgovernance/runtime.go:22-26` (const), `:251-253` (`probeCtx` wraps both `HasPendingDrainingAuditGovernance` `:254` and `OldestPendingAuditGovernance` `:266`) |
| S2 | Probe timeout/cancel → degraded sentinel, `Ready` returns **nil**, Warn log — never 503 | `runtime.go:255-259` (drain probe), `:268-272` (backlog probe): `isProbeCtxError` `:228-233` → `recordDegraded(true, 0)` + `return nil`; `Ready` `:293-294` |
| S3 | Genuine (non-context) store errors stay fail-closed readiness failures | `runtime.go:260-262` (`"audit governance drain lookup failed"`), `:273` (`"audit governance backlog lookup failed"`); drain-in-progress hard error unchanged `:263-265` |
| S4 | maxLag flip → degraded, not error (B3-2) | `runtime.go:283-288` (`ok && age > r.maxLag` → Warn + `recordDegraded(true, age)` + nil); healthy `:289` |
| S5 | Degraded cache with single-lock (degraded, age) pair discipline; zero-I/O getters | `runtime.go:64-67` (field doc), `recordDegraded` `:235-244`, `Degraded()` `:213-219`, `BacklogAge()` `:222-226`; run-loop feed once per poll cycle `:320-323` |
| S6 | `/readyz` seam: `extra.Ready(probeCtx)` bounded by the same 2s `readyzProbeTimeout` as `repo.Ping` and `store.Stat`; degraded extra → **200** with marker body | `cmd/server/http.go:96-99` (ping bound), `:102-103` (probeCtx), `:104` (storage probe), `:109` (`extra.Ready(probeCtx)`), `:113-121` (degraded marker `{"ok":true,"degraded":true,"backlog_age_seconds":N}`), `:125-127` (healthy `{"ok":true}` byte-identical); `degradedChecker` `:34-41`, group aggregation `:65-84` |
| S7 | Gauge read path cache-fed: **zero store I/O per scrape** (strictly stronger than a probe-ctx bound — a scrape can never block on the store) | `cmd/server/build.go:101-108` (`auditGovernanceBacklogAgeGaugeFn` → `rt.BacklogAge()`), `:110-118` (`auditGovernanceDegradedGaugeFn` → `rt.Degraded()`), registered `:153-154`; instruments `internal/telemetry/metrics.go:364-372` (`audit_governance.backlog_age_seconds`), `:377-386` (`audit_governance.degraded`) |
| S8 | Wedge is alert-visible while readiness stays 200: `degraded==1` OR arm in the alert expr | `deploy/prometheus/alerts.yml:186-195` (`AuditGovernanceBacklogDegraded`; `expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` `:187`; `for: 10m`; `severity: warning`; description "/readyz stays 200") |
| S9 | Read-path timeout classification is separate from the delivery-path classifier | read: `runtime.go:228-233` `isProbeCtxError`; delivery: `internal/auditgovernance/relay.go:87` `isPermanentDeliveryError` — `DeadlineExceeded` remains **transient on delivery** (`relay_terminal_test.go:225`) |

**Baseline (this worktree):** `go build ./...` clean · `go vet` clean · `go test ./internal/auditgovernance/` ok (35.2s) · `go test ./internal/telemetry/` ok · `go test ./cmd/server/ -run 'TestReadyz|TestAlertsYML|TestAuditGovernance'` ok (8.9s). Production files all under the 500-line hard gate (`runtime.go` 353, `http.go` 242, `build.go` 220, `metrics.go` 489; the 500-line check excludes `*_test.go` per `Makefile:172-173`).

---

## 2. Evidence verification (direction citations vs. this worktree)

Every direction citation was checked against the repository **as it exists now** (HEAD `15763e2` + worktree). Citations E1–E4 describe the analysis-time (pre-ship) state; E5 was and remains accurate.

| # | Direction citation (analysis-time) | Verified location (current worktree) | Verdict |
|---|---|---|---|
| E1 | `cmd/server/http.go:59-66` — "probeCtx wraps store.Stat only; extra.Ready(req.Context()) unbounded" | `readyzHandler` `http.go:90-127`; `pingCtx` bound `:96-99`; `probeCtx` `:102-103` wraps **all three** probes: `store.Stat` `:104`, `extra.Ready(probeCtx)` `:109`; degraded branch `:113-121`; healthy `{"ok":true}` `:125-127` | ❌ **stale — shipped** (S6). `extra.Ready` no longer runs unbounded; the 2s budget now covers the whole extra readiness group. |
| E2 | `internal/auditgovernance/runtime.go:150-177` — "Ready/BacklogAge fail-closed on store errors, no timeout branch" | `Ready` `runtime.go:293-294` → `probeAndRecord` `:251-290`; `storeProbeTimeout` `:22-26`; timeout/cancel → degraded+nil `:255-259`, `:268-272`; genuine errors hard-fail `:260-262`, `:273`; maxLag → degraded `:283-288`; cache getters `:213-226` (the cited `BacklogAge(ctx)` store-querying accessor is now `PendingBacklogAge` `:198-206`) | ❌ **stale — shipped** (S2–S5). The DeadlineExceeded→degrade branch is exactly the D1 deliverable and exists on both probes. |
| E3 | `cmd/server/build.go:113-119` — "gauge callback: err→observe 0, unbounded store query per scrape" | `auditGovernanceBacklogAgeGaugeFn` `build.go:101-108`, `auditGovernanceDegradedGaugeFn` `:110-118`; registered `:153-154`; callbacks read only the cache getters (zero store I/O) | ❌ **stale — shipped, superseded by a stronger design** (S7). The per-scrape store query was removed entirely; the scrape callback is bounded *by construction* (no store I/O at all), and the store read that feeds the cache is bounded by `storeProbeTimeout` (S1). |
| E4 | `cmd/server/http_test.go:69` — "TestReadyzStorageProbeTimeout — storage probe only"; "no test pins timeout→degrade" | `TestReadyzStorageProbeTimeout` `http_test.go:71-88` (drift: cited `:69`) — still storage-Stat-only; the "no pin" claim is **obsolete**: `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176-204`, elapsed ∈ [1s, 5s], `Degraded()==true`, `BacklogAge()==0`) and `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447-466`, seam-level, 200 + marker, elapsed ∈ [1s, 5s]) | ⚠️ **partially stale** — the cited test is indeed storage-only, but the gap it evidences (no timeout→degrade pin) is closed by the new runtime-level and seam-level pins. |
| E5 | `internal/auditgovernance/relay_terminal_test.go:225` — "DeadlineExceeded is classified transient only on the delivery path" | `context.DeadlineExceeded` at `relay_terminal_test.go:225` inside `TestIsPermanentDeliveryErrorClosedList` `:200-243` (delivery classifier `isPermanentDeliveryError`, `relay.go:87`) | ✅ **accurate, still holds** — delivery-path classification unchanged; the read path now has its own classifier (`isProbeCtxError`, `runtime.go:228-233`, S9), so the two paths classify ctx errors independently (REQ-5). |

**Problem-statement checks (the direction's claims vs. current tree):**

| Statement | Verdict |
|---|---|
| "A wedged store hangs /readyz past the 2s probe contract" | ❌ **no longer true** — `extra.Ready(probeCtx)` is bounded (`http.go:109`); elapsed ∈ [1s, 5s] pinned at both levels (E4 pins). |
| "A wedged store returns 503 (restart-loop risk the D1 design exists to avoid)" | ❌ **no longer true** — probe timeout/cancel records degraded and returns nil (`runtime.go:255-259, 268-272`) → `/readyz` answers 200 with the marker (`http.go:113-121`); pinned by `TestReadyzAuditGovernanceDegradedDrill`. |
| "Backlog-age gauge observes 0 on store error, so the degraded alert goes silent precisely when the store is wedged" | ❌ **no longer true** — the wedge is carried by `audit_governance_degraded == 1` (cache-fed, S7) and the alert expr's OR arm (`alerts.yml:187`); pinned by `TestReadyzAuditGovernanceDegradedDrill` (ageGauge=0 ∧ degradedGauge=1) + `TestAlertsYMLAuditGovernanceExprParity`. |
| "Gauge callback has only the scrape ctx as bound" | ❌ **no longer true** — cache-fed callbacks do zero store I/O per scrape (S7); the store query is bounded by `storeProbeTimeout` in the probe path (S1). |
| "DeadlineExceeded is classified transient only on the delivery path" | ✅ **still true** (E5) — and now complemented by the read-path classifier (S9). |
| "no test pins timeout→degrade" | ❌ **no longer true** — see E4 pins. |

---

## 3. Requirements (contract + pin; all satisfied by the shipped worktree)

Each REQ states the behavior contract the D1 drill requires and names the pin that makes it testable. The implement stage's job is to verify the pins exist and pass — no production delta is expected.

### REQ-1 — The audit-governance read path is time-bounded (2s per probe, 2s at the seam)

`Runtime.Ready`'s two store probes (`HasPendingDrainingAuditGovernance`, `OldestPendingAuditGovernance`) run under `storeProbeTimeout = 2 * time.Second` (`runtime.go:22-26`, `:252-253`); `readyzHandler` calls `extra.Ready(probeCtx)` with the same 2s budget used for `repo.Ping` and the storage probe (`http.go:96-109`).

- *Pin (boundedness, deterministic lower bound):* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176-204`) — hanging stub returns only after the ctx deadline; `Ready(context.Background())` returns with elapsed ∈ [1s, 5s]. `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447-466`) — same bound through the real handler.
- *Pin (seam bound for non-degrading checkers — the fail-closed half of the same contract):* `TestReadyzExtraProbeTimeout` (`readyz_drill_test.go:164-180`) — a generic extra that returns `ctx.Err()` after the deadline yields 503 within [1s, 5s] (a checker that does not implement degrade semantics stays fail-closed; the audit runtime *does* degrade, REQ-2).

### REQ-2 — Probe timeout/cancel degrades, never 503

`errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)` on either probe → Warn log, `recordDegraded(true, 0)` (age unknown), `Ready` returns **nil** (`runtime.go:255-259`, `:268-272`, classifier `:228-233`). At the seam this surfaces as HTTP 200 with `{"ok":true,"degraded":true,"backlog_age_seconds":0}` (`http.go:113-121`).

- *Pins:* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176`) — nil, `Degraded()==true`, `BacklogAge()==0`; subtest `c3-pre-canceled-ctx` of `TestRuntimeReadyFailClosedOnGenuineStoreError` (`:206-252`) — pre-canceled ctx returns immediately (< 1s), nil + degraded (the `context.Canceled` fork); `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447`) — 200, never 503, marker body, age 0.
- *Note on "degraded warn":* the Warn calls exist at `runtime.go:257` (drain probe) and `:269` (backlog probe); the test logger is `io.Discard`, so the pins assert the *behavior* (nil + degraded flag + age 0), not the log text — the log is evidence-cited, not separately pinned.

### REQ-3 — Genuine (non-timeout) store errors stay fail-closed: 503, unchanged

Non-context store errors on either probe → hard error from `Ready` (`runtime.go:260-262`, `:273` — error strings `"audit governance drain lookup failed"` / `"audit governance backlog lookup failed"`), and `readyzHandler` maps any `extra.Ready` error to 503 `runtime dependency unavailable` (`http.go:110-112`). Drain-in-progress stays a hard error (`runtime.go:263-265`).

- *Pins:* `TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:206`) — c1 drain-error and c2 backlog-error subtests assert the exact error strings and `Degraded()==false` (a genuine error is never recorded degraded); `TestReadyzImmediateExtraError` (`readyz_drill_test.go:182-195`) — non-deadline extra error → 503 immediately (< 1s, not delayed by the wrap); `TestReadyzDrainStill503` (`readyz_drill_test.go:261-289`) — draining binding + pending fact → 503 at the seam; `TestReadyzImmediateStorageError` (`http_test.go:115-146`) — storage-probe analog.

### REQ-4 — Gauge read path: bounded by construction, and a wedged store read is never silent

The backlog-age and degraded-flag gauge callbacks read only the runtime cache — zero store I/O per scrape (`build.go:101-108`, `:110-118`); the store read that fills the cache is bounded by `storeProbeTimeout` inside `probeAndRecord` (REQ-1), fed by the run loop once per poll cycle (`runtime.go:320-323`). On a probe timeout the pair is `(degraded=1, age=0)` — the wedge is **non-zero on the degraded gauge** even though the age is unknown — and the alert expr fires via the OR arm (`alerts.yml:187`). On a genuine store error the probe returns before recording, so the cache **retains the last recorded pair** (a transient read error never zeroes a live wedge value; documented at `runtime.go:320-323`).

- *Pins:* `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447`) — wedge: ageGauge=0 ∧ degradedGauge=1; `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:384-445`) — the expr threshold is **derived** from `config.Load()`'s `MaxLagSeconds/2` (450 for the shipped default 900, `internal/config/config_audit_governance.go:68`) and the `OR audit_governance_degraded == 1` arm is required — a regression dropping the arm fails CI; `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` (`internal/telemetry/metrics_test.go:171`) and `TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`:192`) — both series surface in the scrape; `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:291-382`) — phase 2 proves the cache-fed callback reports real ages (≥ 2s backdate) after one priming probe, i.e. the read is the cache, not a silent constant zero.
- *Freshness:* `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:348-395`) — with zero `Ready()` calls the loop flips the cache degraded on a 16s-backdated backlog and healthy after a test-controlled age restore, within one poll cycle; `TestRuntimeRunLoopSurvivesWedgedStore` (`:397-414`) — the loop keeps cycling through wedged probes and recovers.

### REQ-5 — Read-path and delivery-path ctx-error classifications are separate

Delivery-path classification (`isPermanentDeliveryError`, `relay.go:87`) is untouched: `context.DeadlineExceeded` remains **transient** there (pinned by `relay_terminal_test.go:225`). The read path uses its own classifier `isProbeCtxError` (`runtime.go:228-233`). No shared classifier; changing one path's semantics cannot leak into the other.

- *Pins:* `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:200-243`, `DeadlineExceeded` at `:225`) + REQ-2's timeout pins (read path). The two are separate tests in separate files.

### REQ-6 — Regressions held: maxLag flip still degrades; terminal rows still excluded from lag

(a) A pending backlog older than `maxLag` degrades (Ready nil + `Degraded()==true` + age exposed) — never a hard error, never 503; (b) dead-lettered (`failed_at_ns != 0`) rows remain excluded from `OldestPendingAuditGovernance` (`internal/repository/audit_governance_claim.go`, predicate `delivered_at_ns=0 AND failed_at_ns=0`), so a fully dead-lettered backlog reports no pending and never blocks readiness.

- *Pins:* `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670` — the direction's "runtime_test.go:415 pattern" relocated; seed shape preserved, maxLag 4s, Ready nil, degraded, drain still hard-fails `:662-670`); `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:215-259`) — seam: 200 with the exact marker body for an 8s-backdated row (> maxLag 4s, 2× margin, deterministic backdate — no sleeps); `assertTerminalState` (`relay_terminal_test.go:119-128`, the `OldestPendingAuditGovernance` ok==false check at `:126-128` — the cited T-3 lines); `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:254-317`, lease-fenced Claim+Fail); `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676-699`, empty store); `TestReadyzDeadLetteredBacklog200AndGaugeZero` phases 0–1 (`readyz_drill_test.go:291-382`, empty + dead-lettered at the seam with gauge 0).

---

## 4. Decisions & non-goals

- **D1 — Bound is a package constant, not config.** `storeProbeTimeout = 2s` (`runtime.go:22-26`) mirrors `readyzProbeTimeout` (`http.go:52`) with a cross-reference comment; no new env knob, no config surface (sibling `cmd-server-audit-governance-ready-degraded-v1.spec.md` decision D1).
- **D2 — Degraded is a cache sentinel, not a live query.** `Degraded()`/`BacklogAge()` are zero-I/O cache getters (`runtime.go:213-226`); the probe runs in `Ready()` and the run loop. This is what makes the drill deterministic (blocking stubs, no store contention) and scrapes hang-proof.
- **D3 — Gauge callbacks are cache-fed (zero store I/O), which dominates the direction's "gauge callback bounded (probe ctx)" acceptance.** The direction assumed the gauge queries the store per scrape with the scrape ctx as its only bound; the shipped design removes the per-scrape store read entirely, so the callback is bounded by construction, and the (bounded) store read happens only in the probe path. Acceptance (c) is satisfied via this stronger mechanism; the pin inventory in REQ-4 makes the property testable.
- **D4 — Wedge signal = degraded-flag gauge + alert OR arm, not the age gauge.** Age is unknown on timeout (`recordDegraded(true, 0)`), so the age gauge reads 0 by design; the degraded gauge is the non-silent signal (`TestReadyzAuditGovernanceDegradedDrill` pins ageGauge=0 ∧ degradedGauge=1, `TestAlertsYMLAuditGovernanceExprParity` pins the OR arm).
- **D5 — Two-layer seam contract.** `readyzHandler` guarantees the 2s budget for any extra checker (`http.go:109`); the audit runtime additionally *self-degrades* on ctx errors so its contribution is 200 + marker, while a generic checker that fails after the deadline still 503s (bounded fail-closed). Both halves pinned (REQ-1).
- **Non-goals (do not expand scope):** alert-threshold single-sourcing (direction 2 of the same analysis), item-5 terminal-branch matrix + `audit:event:write` CI grep (direction 3), billing-runtime readiness (contributes `false`/`0` through the group, `http.go:36-38`), `readyzProbeTimeout`/storage-probe/database-probe branch behavior, alert severity/`for`/description content beyond what the pins already assert, drain semantics, config surface, migrations, and any new test beyond verifying the pins in §3.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**(a)** *Store stub whose `OldestPendingAuditGovernance`/`HasPendingDrainingAuditGovernance` block until ctx deadline → `Runtime.Ready` returns nil (degraded warn) and `readyzHandler` returns 200 within `readyzProbeTimeout`.*
**Testable:** `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176`) — `scriptedStore` (hanging probes block on `<-ctx.Done()`, `:49-78`): `Ready==nil`, `Degraded()==true`, `BacklogAge()==0`, elapsed ∈ [1s, 5s] (blocking stub ⇒ the response cannot precede the 2s deadline — deterministic lower bound; ≤ 5s is the timing-robust boundedness claim, the `TestReadyzStorageProbeTimeout` idiom). `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447`) — real `hangingAuditStore` through `runtimeReadiness` + `readyzHandler`: **200** (never 503), body contains `"degraded":true`, elapsed ∈ [1s, 5s].

**(b)** *Immediate (non-timeout) store error still fails /readyz 503 (fail-closed unchanged).*
**Testable:** `TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:206`) — c1 drain-error / c2 backlog-error subtests: `Ready` returns the exact hard-error strings and `Degraded()==false`; `TestReadyzImmediateExtraError` (`readyz_drill_test.go:182`) — 503 `< 1s` (the deadline wrap neither delays nor swallows non-deadline errors); `TestReadyzDrainStill503` (`:261`) — drain-in-progress → 503 at the seam.

**(c)** *Gauge callback bounded (probe ctx) and a store-error read reports non-zero/absent rather than silently 0, so the maxLag×0.5 alert fires on wedge.*
**Testable:** bounded by construction — callbacks are cache-fed (`build.go:101-118`; `TestReadyzDeadLetteredBacklog200AndGaugeZero` phase 2 proves the callback returns cache values, `readyz_drill_test.go:291`); the store read that feeds the cache is probe-bounded (REQ-1 pins). Wedge non-silent — `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:447`): ageGauge=0 ∧ **degradedGauge=1** on the hung store. Alert fires on wedge — `TestAlertsYMLAuditGovernanceExprParity` (`:384`): expr = `audit_governance_backlog_age_seconds > <config.MaxLagSeconds/2> OR audit_governance_degraded == 1` (threshold derived from the shipped default 900 → 450 via `config.Load()`), `for: 10m`, `severity: warning`; scrape-surface pins `metrics_test.go:171,192`. Genuine-error reads retain the last recorded pair (never silently 0 a live wedge; `runtime.go:320-323`, `TestRuntimeRunLoopSurvivesWedgedStore` `:397`).

**(d)** *Regression: maxLag flip still degrades (runtime_test.go:415 pattern) and terminal rows still excluded from OldestPending (T-3, relay_terminal_test.go:125-128).*
**Testable:** `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670`, the relocated `:415` pattern — seed pending fact, wait past maxLag, `Ready==nil` + degraded, drain still hard-fails) and `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:215`, 200 + exact marker body, deterministic 8s backdate vs. 4s maxLag = 2× margin); terminal exclusion pinned at the store query semantics (`assertTerminalState`, `relay_terminal_test.go:126-128`) and at runtime + seam (`TestRuntimeBacklogAgeZeroWhenAllTerminal`, `TestRuntimeBacklogAgeZeroWhenNoPending`, `TestReadyzDeadLetteredBacklog200AndGaugeZero` phases 0–1).

---

## 6. Risks & gates

- **Pin-drift risk (low):** the acceptance pins live in three packages (`internal/auditgovernance`, `cmd/server`, `internal/telemetry`); the `make check` gate (`gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`, 500-line production-file check) covers them all. Any refactor of `probeAndRecord`, the cache pair, or the marker body breaks the named pins.
- **Timing flake (mitigated):** all boundedness assertions use the proven blocking-stub idiom (response cannot precede the deadline ⇒ deterministic lower bound; ≤ 5s upper bound only proves boundedness); backdating via a second WAL writer replaces sleeps (`backdateDrillFact`, `backdatePendingFact`); no wall-clock equality anywhere.
- **Concurrency:** the (degraded, age) pair discipline is single-lock (`recordDegraded`, `runtime.go:235-244`); `TestRuntimeDegradedCacheConcurrentAccess` (`runtime_ready_test.go:416`) is meaningful only under `-race` — run `make test-race` before merge.
- **Test runtime:** the new drill tests add ~2s per blocking-stub test (6 blocking tests across `internal/auditgovernance` + `cmd/server`); the full packages are green on this worktree (§1 baseline).
- **Line-count gates:** production files all < 500 (`runtime.go` 353, `http.go` 242, `build.go` 220, `metrics.go` 489, `config_audit_governance.go` < 300); test files are excluded from the 500-line check (`Makefile:172-173`) — `readyz_drill_test.go` is exactly 500 and `runtime_ready_test.go` 472; keep new pins in existing test files, do not create new ones beyond the current set.

*Verification basis: all citations re-checked on this worktree (HEAD `15763e2` + uncommitted changes); line numbers reflect the tree as read during this spec's production. Full evidence chain in §2; the stage artifact mirrors this document.*
