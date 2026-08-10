# Requirements Specification — `internal/replication` analysis bucket: D1 drill — finish read-path timeout degradation (probe timeout + Degraded() sentinel + /readyz degraded payload)

**Module label:** `internal/replication` (direction selected from `docs/auto/analyses/internal-replication-9317f27a.json`); **touched code:** `internal/auditgovernance` (read path) + seam consumers `cmd/server/http.go` (`readyzHandler`), `cmd/server/build.go` (gauge callbacks), `deploy/helm/aero-vault/templates/deployment.yaml` (readinessProbe)
**Direction:** "D1 drill: finish read-path timeout degradation (probe timeout + Degraded() sentinel + /readyz degraded payload) — currently timeouts 503/fail-closed" (direction 1 of `docs/auto/analyses/internal-replication-9317f27a.json`)
**Contract:** `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 / D1 ("read-path timeouts degrade instead of 503"); approved delta design `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.design.md` (REQ-2/3/4/6, F1, H1/H2, T1–T5)
**Sibling specs (same drill, other analysis registrations):** `docs/requirements/internal-auditgovernance-d1-read-path-timeout-degrade-v1.spec.md`, `docs/requirements/internal-reconcile-d1-read-path-store-errors-timeouts-v1.spec.md` — this spec is the internal-replication analysis's duplicate direction and must stay consistent with both
**Date:** 2026-08-09 · **HEAD:** `15763e2` + worktree (verification basis) · **Score:** value 9 / risk reduction 8 / effort 6 / confidence 8

---

## 1. Status statement (what exists vs. what this direction requires)

**This direction is already shipped in the current worktree.** Every supplied acceptance check has a production implementation and a passing test pin (verified and executed on this tree, §2/§6). The analysis (2026-08-07) predates the worktree changes (2026-08-08) that implemented the D1 read-path half; all five evidence citations therefore describe the *pre-ship* state. This spec is the **regression contract**: the implement stage is expected to be zero-production-delta — verify the pins below exist and pass; the single required test delta is the acceptance-(a) conjunction pin (REQ-1 residual, §5).

**Shipped inventory (verified this worktree):**

| # | Shipped item | Evidence (current worktree) |
|---|---|---|
| S1 | `Runtime.Ready`'s two store probes bounded by `storeProbeTimeout = 2s` (mirror of `readyzProbeTimeout`) | `internal/auditgovernance/runtime.go:22-26` (const `:26`), `probeAndRecord` `:251-290` — `probeCtx` `:252` wraps both `HasPendingDrainingAuditGovernance` `:254` and `OldestPendingAuditGovernance` `:266` |
| S2 | Probe timeout/cancel → degraded sentinel, `Ready` returns **nil**, Warn log — never 503 | `runtime.go:255-259` (drain probe), `:268-272` (backlog probe): `isProbeCtxError` `:228-233` → `recordDegraded(true, 0)` + `return nil`; `Ready` `:293-294` |
| S3 | Genuine (non-context) store errors stay fail-closed readiness failures | `runtime.go:260-262` (`"audit governance drain lookup failed"`), `:273` (`"audit governance backlog lookup failed"`); drain-in-progress hard error unchanged `:263-265` |
| S4 | maxLag flip → degraded, not error (B3-2, shipped in 15763e2) | `runtime.go:283-288` (`ok && age > r.maxLag` → Warn + `recordDegraded(true, age)` + nil); healthy `:289` |
| S5 | Degraded cache with single-lock (degraded, age) pair discipline; zero-I/O getters | `runtime.go:64-67` (field doc), `recordDegraded` `:235-244`, `Degraded()` `:213-219`, `BacklogAge()` `:222-226`; run-loop feed once per poll cycle `:320-323` |
| S6 | `/readyz` seam: `extra.Ready(probeCtx)` bounded by the same 2s `readyzProbeTimeout` as `repo.Ping` and `store.Stat`; degraded extra → **200** with marker body | `cmd/server/http.go:91-127` (`readyzHandler`): `pingCtx` `:97`, `probeCtx` `:103`, `extra.Ready(probeCtx)` `:109`; degraded marker `{"ok":true,"degraded":true,"backlog_age_seconds":N}` `:113-122`; healthy `{"ok":true}` `:125-127`; `degradedChecker` `:40`, group aggregation `:66-84`; const `:53` |
| S7 | Gauge read path cache-fed: **zero store I/O per scrape** (strictly stronger than a probe-ctx bound) | `cmd/server/build.go:101-105` (`auditGovernanceBacklogAgeGaugeFn` → `rt.BacklogAge()`), `:110-114` (`auditGovernanceDegradedGaugeFn` → `rt.Degraded()`), registered `:153-154`; instruments `internal/telemetry/metrics.go:368-372` (`audit_governance.backlog_age_seconds`), `:382-386` (`audit_governance.degraded`) |
| S8 | Wedge is alert-visible while readiness stays 200: `degraded==1` OR arm in the alert expr | `deploy/prometheus/alerts.yml:184-192` (`AuditGovernanceBacklogDegraded`; `expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` `:186`; `for: 10m`; `severity: warning`; description "/readyz stays 200") |
| S9 | Read-path timeout classification is separate from the delivery-path classifier | read: `runtime.go:228-233` `isProbeCtxError`; delivery: `internal/auditgovernance/relay.go:255` `isPermanentDeliveryError` — `context.DeadlineExceeded` remains **transient on delivery** (`relay_terminal_test.go:242` in the transient list, `:225` closed-list assertion) |
| S10 | Helm readinessProbe window (H1): `timeoutSeconds: 10` so degraded pods stay in rotation | `deploy/helm/aero-vault/templates/deployment.yaml:85-91` (`timeoutSeconds: 10` at `:91`); pinned by `TestHelmReadinessProbeTimeoutSeconds` (`cmd/server/http_test.go:284-304`, also forbids `failureThreshold` on the block) |

**Baseline (this worktree, executed for this spec):** `go build ./...` clean · `go test ./internal/auditgovernance/ -run 'TestRuntimeReady|TestRuntimeBacklogAge|TestRuntimeRunLoop|TestRuntimeDegradedCache|TestIsPermanentDeliveryError'` PASS (14.9s) · `go test ./cmd/server/ -run 'TestReadyz|TestAlertsYMLAuditGovernanceExprParity|TestNoExecutable450Literal'` PASS (8.8s) · `go test ./internal/telemetry/ -run TestAuditGovernance` ok · `go test ./internal/config/ -run TestAuditGovernanceMaxLagDefault` ok. Production files all under the 500-line hard gate (`runtime.go` 353, `http.go` 242, `build.go` 220, `metrics.go` 489); the line-count check excludes `*_test.go` (`Makefile:172-173`), so the 701-line `runtime_test.go` is legal, and the analysis's "runtime_test.go is 498/500 → new-file mandate" was honored by creating `runtime_ready_test.go` (517 lines) — the D1 pins live there.

---

## 2. Evidence verification (direction citations vs. this worktree)

Every direction citation was checked against the repository **as it exists now** (HEAD `15763e2` + worktree). Citations E1–E4 describe the analysis-time (pre-ship) state; E5 was and remains accurate.

| # | Direction citation (analysis-time) | Verified location (current worktree) | Verdict |
|---|---|---|---|
| E1 | `internal/auditgovernance/runtime.go:162-179` — "fail-closed Ready, no `isProbeCtxError` fork; 'drain lookup failed'/'backlog lookup failed' → /readyz 503" | `Ready` `runtime.go:293-294` → `probeAndRecord` `:251-290`; `storeProbeTimeout` `:22-26`; timeout/cancel → degraded+nil `:255-259`, `:268-272` (`isProbeCtxError` `:228-233`); genuine errors hard-fail with the exact cited strings `:260-262`, `:273`; maxLag → degraded `:283-288`; cache getters `:213-226` (the analysis-time `BacklogAge(ctx)` store-querying accessor is now `PendingBacklogAge` `:198-206`) | ❌ **stale — shipped** (S1–S5). The DeadlineExceeded→degrade fork is exactly the D1 deliverable and exists on both probes. |
| E2 | `cmd/server/http.go:31-71` — "readyzProbeTimeout 2s only on storage Stat (`:59-61`); `extra.Ready(req.Context())` unbounded (`:66`)" | `readyzHandler` `http.go:91-127`; `pingCtx` bound `:97` (H2); `probeCtx` `:103` wraps **all three** probes: `store.Stat`, `extra.Ready(probeCtx)` `:109`; degraded branch `:113-122`; healthy `{"ok":true}` `:125-127`; const `:53` | ❌ **stale — shipped** (S6). `extra.Ready` no longer runs unbounded; the 2s budget now covers the whole extra readiness group. |
| E3 | `internal/auditgovernance/runtime_test.go:415-466` — "sleep-based maxLag pin only" | `TestRuntimeReadyDegradesOnBacklogLag` relocated to `runtime_test.go:618-670` (still the sleep-crossing idiom, 4.5s sleep, `Ready==nil` + drain hard-fail); `TestRuntimeBacklogAgeZeroWhenNoPending` at `:676-699`; **and** the new deterministic (no-sleep) backdate pins: `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:212-257`), `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`) | ⚠️ **partially stale** — the cited pin exists (relocated, same idiom), but the "only" is obsolete: D1 pins now cover timeout/cancel/error/dead-row shapes across `runtime_ready_test.go` + `readyz_drill_test.go`. |
| E4 | "no `Degraded()` (grep: zero hits in production code)"; "no `runtime_ready_test.go`" | `Degraded()` `runtime.go:213-219`, `BacklogAge()` `:222-226`, cache `:64-67`, consumers `http.go:118`, `build.go:110-114`; `runtime_ready_test.go` exists (517 lines, 6 tests + 1 subtest) | ❌ **stale — shipped** (S5/S6/S7). Both absences are closed. |
| E5 | "approved delta design … verified against this tree (register: all claims TRUE) but NOT implemented" | `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.design.md` exists; REQ-2/3/4/6, F1, H1/H2, T1–T5 present; every design claim now maps to a shipped line (S1–S10) | ⚠️ **partially stale** — register claims were TRUE at analysis time; the design is now **implemented** in the worktree. |

**Problem-statement checks (the direction's claims vs. current tree):**

| Statement | Verdict |
|---|---|
| "A wedged store hangs /readyz past the 2s budget and evicts the pod" | ❌ **no longer true** — `extra.Ready(probeCtx)` is bounded (`http.go:109`); elapsed ∈ [1s, 5s] pinned at both levels (`TestRuntimeReadyDegradedSentinel`, `TestReadyzAuditGovernanceDegradedDrill`). |
| "Ready() fail-closes on ANY store error including ctx timeout/cancel → /readyz 503" | ❌ **no longer true** — probe timeout/cancel records degraded and returns nil (`runtime.go:255-259`, `:268-272`) → `/readyz` 200 + marker (`http.go:113-122`); genuine errors stay 503 (S3, pinned by c1/c2 subtests). |
| "no storeProbeTimeout / no Degraded()" | ❌ **no longer true** — S1/S5. |
| "helm readinessProbe timeoutSeconds: 10 is only proposed (H1)" | ❌ **no longer true** — shipped at `deployment.yaml:91`, pinned by `TestHelmReadinessProbeTimeoutSeconds` (`http_test.go:284-304`). |

---

## 3. Requirements (contract + pin; all satisfied by the shipped worktree)

### REQ-1 — The audit-governance read path is time-bounded (2s per probe, 2s at the seam)

`Runtime.Ready`'s two store probes run under `storeProbeTimeout = 2 * time.Second` (`runtime.go:22-26`, `:252`); `readyzHandler` calls `extra.Ready(probeCtx)` with the same 2s budget used for `repo.Ping` (H2, `http.go:97`) and the storage probe (`http.go:103-110`).

- *Pin (boundedness, deterministic lower bound):* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:194-249`) — `scriptedStore` hang mode blocks on `<-ctx.Done()` (`:37-82`); `Ready(context.Background())` returns with elapsed ∈ [1s, 5s]. `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:445-468`) — same bound through the real handler.
- *Pin (seam bound for non-degrading checkers — the fail-closed half of the same contract):* `TestReadyzExtraProbeTimeout` (`readyz_drill_test.go:161-177`) — a generic extra that returns `ctx.Err()` after the deadline yields 503 within [1s, 5s] (a checker that does not implement degrade semantics stays fail-closed; the audit runtime *does* degrade, REQ-2).

### REQ-2 — Probe timeout/cancel degrades, never 503

`errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)` on either probe → Warn log, `recordDegraded(true, 0)` (age unknown), `Ready` returns **nil** (`runtime.go:255-259`, `:268-272`, classifier `:228-233`). At the seam this surfaces as HTTP 200 with `{"ok":true,"degraded":true,"backlog_age_seconds":0}` (`http.go:113-122`).

- *Pins:* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:194`) — nil, `Degraded()==true`, `BacklogAge()==0`; its `backlog-probe-only` subtest pins the literal acceptance shape (only `OldestPendingAuditGovernance` wedges via the `setBacklogHang` overlay — the RG-1 residual of the sibling internal-reconcile spec is **closed**); subtest `c3-pre-canceled-ctx` of `TestRuntimeReadyFailClosedOnGenuineStoreError` (`:251-297`) — pre-canceled ctx returns immediately (< 1s), nil + degraded (the `context.Canceled` fork); `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:445`) — 200, never 503, marker body, age 0.

### REQ-3 — Genuine (non-timeout) store errors stay fail-closed: 503, unchanged

Non-context store errors on either probe → hard error from `Ready` (`runtime.go:260-262`, `:273` — error strings `"audit governance drain lookup failed"` / `"audit governance backlog lookup failed"`), and `readyzHandler` maps any `extra.Ready` error to 503 `runtime dependency unavailable` (`http.go:110-112`). Drain-in-progress stays a hard error (`runtime.go:263-265`).

- *Pins:* `TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:251-297`) — c1 drain-error and c2 backlog-error subtests assert the exact error strings and `Degraded()==false` (a genuine error is never recorded degraded); `TestReadyzImmediateExtraError` (`readyz_drill_test.go:179-192`) — non-deadline extra error → 503 immediately (< 1s); `TestReadyzDrainStill503` (`:258-286`) — draining binding + pending fact → 503 at the seam; `TestReadyzImmediateStorageError` (`http_test.go:115-145`) — storage-probe analog.

### REQ-4 — Gauge read path: bounded by construction, and a wedged store read is never silent

The backlog-age and degraded-flag gauge callbacks read only the runtime cache — zero store I/O per scrape (`build.go:101-105`, `:110-114`); the store read that fills the cache is bounded by `storeProbeTimeout` inside `probeAndRecord` (REQ-1), fed by the run loop once per poll cycle (`runtime.go:320-323`). On a probe timeout the pair is `(degraded=1, age=0)` — the wedge is **non-zero on the degraded gauge** even though the age is unknown — and the alert expr fires via the OR arm (`alerts.yml:186`). On a genuine store error the probe returns before recording, so the cache **retains the last recorded pair**.

- *Pins:* `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:463-467`) — wedge: ageGauge=0 ∧ degradedGauge=1; `TestAlertsYMLAuditGovernanceExprParity` (`:382-443`) — the expr threshold is **derived** from `config.Load()`'s `BacklogAlertThresholdSeconds()` (= `MaxLagSeconds/2`, `internal/config/config_audit_governance.go:42-50`; 450 for the shipped default 900, `:114`) and the `OR audit_governance_degraded == 1` arm is required; `TestNoExecutable450LiteralOutsideAlertsYml` (`:541-577`) — no other executable 450 literal in the Go tree; `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` (`internal/telemetry/metrics_test.go:171`) and `TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`:192`) — both series surface in the scrape; `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:288-380`) — phase 2 proves the cache-fed callback reports real ages (2s backdate, no sleeps).
- *Freshness:* `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`) — with zero `Ready()` calls the loop flips the cache degraded on a 16s-backdated backlog and healthy after a test-controlled age restore; `TestRuntimeRunLoopSurvivesWedgedStore` (`:442-459`) — the loop keeps cycling through wedged probes and recovers.

### REQ-5 — Read-path and delivery-path ctx-error classifications are separate

Delivery-path classification (`isPermanentDeliveryError`, `relay.go:255`) is untouched: `context.DeadlineExceeded` remains **transient** there (`relay_terminal_test.go:242` in the transient list). The read path uses its own classifier `isProbeCtxError` (`runtime.go:228-233`). No shared classifier.

- *Pins:* `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:200-243`) + REQ-2's timeout pins (read path). The two are separate tests in separate files.

### REQ-6 — Regressions held: maxLag flip still degrades; terminal rows still excluded from lag

(a) A pending backlog older than `maxLag` degrades (Ready nil + degraded + age exposed) — never a hard error, never 503; (b) dead-lettered (`failed_at_ns != 0`) rows remain excluded from `OldestPendingAuditGovernance` (`internal/repository/audit_governance_claim.go:211`, predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` in the query at `:216-219`), so a fully dead-lettered backlog reports no pending and never blocks readiness.

- *Pins:* `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670`, the direction's "runtime_test.go:415 pattern" relocated — seed shape preserved, maxLag 4s, `Ready` nil, drain still hard-fails); `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:212-257`) — seam: 200 with the exact marker body for an 8s-backdated row (> maxLag 4s, 2× margin, deterministic backdate via second WAL connection — no sleeps); `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299-392`, lease-fenced Claim+Fail); `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676-699`, empty store); `TestReadyzDeadLetteredBacklog200AndGaugeZero` phases 0–1 (`readyz_drill_test.go:288-380`, empty + dead-lettered at the seam with gauge 0).

---

## 4. Decisions & non-goals

- **D1 — Bound is a package constant, not config.** `storeProbeTimeout = 2s` (`runtime.go:22-26`) mirrors `readyzProbeTimeout` (`http.go:53`) with a cross-reference comment; no new env knob, no config surface (sibling spec decision D1).
- **D2 — Degraded is a cache sentinel, not a live query.** `Degraded()`/`BacklogAge()` are zero-I/O cache getters (`runtime.go:213-226`); the probe runs in `Ready()` and the run loop. This is what makes the drill deterministic (blocking stubs, no store contention) and scrapes hang-proof.
- **D3 — Gauge callbacks are cache-fed (zero store I/O), which dominates the direction's "probe ctx bound" acceptance.** The direction assumed the gauge queries the store per scrape; the shipped design removes the per-scrape store read entirely, so the callback is bounded by construction, and the (bounded) store read happens only in the probe path. Acceptance (c) of the analysis's sibling direction is satisfied via this stronger mechanism.
- **D4 — Wedge signal = degraded-flag gauge + alert OR arm, not the age gauge.** Age is unknown on timeout (`recordDegraded(true, 0)`), so the age gauge reads 0 by design; the degraded gauge is the non-silent signal (`TestReadyzAuditGovernanceDegradedDrill` pins ageGauge=0 ∧ degradedGauge=1, `TestAlertsYMLAuditGovernanceExprParity` pins the OR arm).
- **D5 — Two-layer seam contract.** `readyzHandler` guarantees the 2s budget for any extra checker (`http.go:109`); the audit runtime additionally *self-degrades* on ctx errors so its contribution is 200 + marker, while a generic checker that fails after the deadline still 503s (bounded fail-closed). Both halves pinned (REQ-1).
- **D6 — Module label.** The analysis bucket says `internal/replication`, but the direction touches zero files in `internal/replication` — the audit-governance relay is a separate package, and the "replication" label is a campaign-bucket artifact (precedent: the internal-reconcile registration of the same drill). This spec is filed under the bucket name for traceability; the code contract is `internal/auditgovernance` + `cmd/server` + `deploy/helm`.
- **Non-goals (do not expand scope):** alert-threshold single-sourcing beyond the derived-threshold pins already shipped (direction 2 of the same analysis), per-scrape-gauge query removal beyond the cache-fed design already shipped (direction 3 of the same analysis), billing-runtime readiness (contributes `false`/`0` through the group, `http.go:36-38`), `readyzProbeTimeout`/storage-probe/database-probe branch behavior beyond what the pins already assert, drain semantics, config surface, migrations, `deploy/` chart changes beyond the shipped `timeoutSeconds: 10`, and any new test beyond the acceptance-(a) conjunction pin (REQ-1 residual, §5).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**(a)** *Backdate `created_at_ns` via second WAL connection → `Ready()==nil` AND `Degraded()==true` AND `BacklogAge()>maxLag`.*
**Testable:** the seam pins the full conjunction end-to-end: `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:212-257`) — `backdateDrillFact` (`:126-140`, second raw SQLite connection on the same file DSN, `UPDATE audit_governance_outbox SET created_at_ns=…` — no sleeps) then real `runtimeReadiness` + `readyzHandler`: 200 (⇒ `extra.Ready` returned nil) with the exact body `{"ok":true,"degraded":true,"backlog_age_seconds":8}` (⇒ `Degraded()==true` and `BacklogAge()==8s > maxLag 4s`), elapsed < 1s. The runtime-level atoms are pinned separately: `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670`, `Ready()==nil` on lag) and `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:393-440`, WAL-backdated row → `Degraded()==true` ∧ `BacklogAge()>maxLag`).
**Residual (one required test delta, zero production delta):** no single runtime-level test asserts the four-atom conjunction (backdate → `Ready()==nil` ∧ `Degraded()==true` ∧ `BacklogAge()>maxLag`) in one flow — `TestRuntimeReadyDegradesOnBacklogLag` asserts `Ready()==nil` and `PendingBacklogAge()>maxLag` but not the cache getters. Required pin: after the `Ready()==nil` assertion in that test (`runtime_test.go:642-643`), assert `runtime.Degraded()==true` and `runtime.BacklogAge() > 4*time.Second` (the probe has already recorded the pair). Optional (removes the 4.5s sleep): convert the crossing to the WAL-backdate technique used by `TestReadyzBacklogLagDegradesNot503`. Either way the acceptance shape becomes a single-flow assertion.

**(b)** *Hanging-store fake (probe blocks on `<-ctx.Done()`, returns `ctx.Err()`) → `Ready(background)==nil`, elapsed ∈ [1s, 5s], `Degraded()==true`, `BacklogAge()==0`.*
**Testable:** `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:194-249`) — `scriptedStore` hang mode (`:37-82`, `<-ctx.Done()` then `ctx.Err()`): `Ready==nil`, elapsed ∈ [1s, 5s] (blocking stub ⇒ the response cannot precede the 2s deadline — deterministic lower bound; ≤ 5s is the timing-robust boundedness claim), `Degraded()==true`, `BacklogAge()==0` (age unknown). The `backlog-probe-only` subtest pins the literal shape (only `OldestPendingAuditGovernance` wedges — the drain probe healthy). PASSED on this tree (4.35s).

**(c)** *Erroring-store fake → `'drain lookup failed'`/`'backlog lookup failed'` AND `Degraded()==false` (fail-closed branches preserved).*
**Testable:** `TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:251-297`) — `scriptedStore` drainErr/backlogErr modes inject immediate **non-context** errors: c1 `"audit governance drain lookup failed"`, c2 `"audit governance backlog lookup failed"`, both with `Degraded()==false` (a genuine error is never recorded degraded); c3 pre-canceled ctx → nil + degraded (< 1s, the `context.Canceled` fork). Seam side: `TestReadyzImmediateExtraError` (`readyz_drill_test.go:179-192`, 503 < 1s) and `TestReadyzDrainStill503` (`:258-286`, drain-in-progress → 503). PASSED on this tree (0.51s).

**(d)** *cmd/server HTTP test: `/readyz` returns 200 with `"degraded":true` payload under probe timeout.*
**Testable:** `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:445-468`) — real `auditgovernance.New` over `hangingAuditStore` (`:434-442`, both probes block on `<-ctx.Done()`) through `runtimeReadiness` + `readyzHandler`: **200** (never 503), body contains `"degraded":true`, elapsed ∈ [1s, 5s]; wedge also visible in the gauges (ageGauge=0 ∧ degradedGauge=1). PASSED on this tree (2.18s).

**(e)** *Helm readinessProbe `timeoutSeconds: 10` (H1) so degraded pods stay in rotation (proposed, "no repo artifact today" at analysis time).*
**Testable:** shipped — `deploy/helm/aero-vault/templates/deployment.yaml:85-91` (`timeoutSeconds: 10`); pinned by `TestHelmReadinessProbeTimeoutSeconds` (`cmd/server/http_test.go:284-304`, asserts the readinessProbe block contains `timeoutSeconds: 10` and no `failureThreshold`). PASSED on this tree. Worst-case degraded-path latency = ping 2s + storage 2s + audit probes 2s = 6s < 10s (design F12).

---

## 6. Risks & gates

- **Pin-drift risk (low):** the acceptance pins live in four packages (`internal/auditgovernance`, `cmd/server`, `internal/telemetry`, `internal/config`); the `make check` gate (gofmt, `go build ./...`, `go vet ./...`, `go test ./...`, `test-race-meta` — which includes `./internal/auditgovernance/` per `Makefile:123-126`, 500-line production-file check) covers them all. Any refactor of `probeAndRecord`, the cache pair, the marker body, or the alert expr breaks the named pins.
- **Timing flake (mitigated):** all boundedness assertions use the proven blocking-stub idiom (response cannot precede the deadline ⇒ deterministic lower bound; ≤ 5s upper bound only proves boundedness); backdating via a second WAL writer replaces sleeps (`backdateDrillFact`, `backdatePendingFact`, `restorePendingFactAge`); no wall-clock equality anywhere.
- **Concurrency:** the (degraded, age) pair discipline is single-lock (`recordDegraded`, `runtime.go:235-244`); `TestRuntimeDegradedCacheConcurrentAccess` (`runtime_ready_test.go:461-517`) is meaningful only under `-race` — enforced by `make test-race-meta` (`Makefile:123-126`); run `make check` before merge.
- **Test runtime:** the drill tests add ~2s per blocking-stub test (6 blocking tests across `internal/auditgovernance` + `cmd/server`); full packages green on this tree (§1 baseline).
- **Line-count gates:** production files all < 500 (`runtime.go` 353, `http.go` 242, `build.go` 220, `metrics.go` 489); test files excluded from the check (`Makefile:172-173`) — keep the one new assertion in `runtime_test.go` inside the existing test, do not create new files.

*Verification basis: all citations re-checked on this worktree (HEAD `15763e2` + uncommitted changes) and the named pins executed green on 2026-08-09. Line numbers reflect the tree as read during this spec's production. Full evidence chain in §2; the stage artifact mirrors this document.*
