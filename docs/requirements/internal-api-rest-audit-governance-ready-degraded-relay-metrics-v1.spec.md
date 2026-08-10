# Requirements Specification — `internal/api/rest` (analysis label): `Ready()` decoupling to degraded + 450s alert, with relay metrics attempted/delivered/failed/dead/oldest-age (B3-2 D1 drill + B3-4)

**Module:** `internal/api/rest` (analysis label; implementation surface is `internal/auditgovernance` + `internal/telemetry` + `cmd/server` + `deploy/prometheus` — see §1)
**Direction:** "Ready() decoupling to degraded + 450s alert, with relay metrics attempted/delivered/failed/dead/oldest-age (B3-2 D1 drill + B3-4)" (direction 2)
**Source analysis:** `docs/auto/analyses/internal-api-rest-8e390260.json`
**Contract:** `docs/campaigns/implementation-gate.md:22` (gate item 2: Ready 解耦 H1 — maxLag 翻转移除 → degraded + maxLag×0.5 (450 s) alert; terminal rows excluded from `OldestPending`; read-path timeouts degrade non-503; D1 drill) and `:24` (gate item 4: relay metrics H6 — attempted/delivered/failed/dead/oldest-age; stalled relay detectable)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (+ uncommitted worktree; verification basis = current working tree)
**Score:** value 9 / risk reduction 8 / effort 5 / confidence 9

---

## 1. Module & scope

The analysis labels this direction under `internal/api/rest`, but — as the analysis itself notes — **no change is required in `internal/api/rest/`**: the only REST touchpoints are the `/readyz` route (registered in `cmd/server/http.go:101`, outside `api/rest`) and the write-path ingress `AdminHandler.auditForTenant` (`internal/api/rest/admin.go:404-425` → `repo.RecordAudit`), which is the health premise of the D1 drill (the write path stays healthy while the relay lags). The module label is retained for traceability to the analysis; the actual surface is `internal/auditgovernance` (`runtime.go`, `relay.go`), `internal/telemetry` (`metrics.go`), `cmd/server` (`http.go`, `build.go`), `deploy/prometheus/alerts.yml`, and test files in `internal/repository`.

**State at verification time (critical):** commit `15763e2` ("feat(gov): B3-2 Ready decoupling — backlog degrades instead of 503; backlog-age gauge + 450s alert") **already implemented a large subset** of this direction on this checkout. The direction's problem statement is therefore **partially stale**. This spec (a) pins what is implemented but unpinned, and (b) specifies the delta that is still missing. Verified inventory:

| # | Item | Status on this checkout |
|---|------|------------------------|
| S1 | `Ready()` maxLag flip → degraded (nil + warn), no 503 | ✅ **implemented** (`runtime.go:162-182`, flip `:178-181`) |
| S2 | `BacklogAge()` accessor over `OldestPendingAuditGovernance` | ✅ **implemented** (`runtime.go:151-159`) |
| S3 | Gauge `audit_governance.backlog_age_seconds` | ✅ **implemented** (registered `metrics.go:354-365`; wired `cmd/server/build.go:113-120` when `auditRuntime != nil`) |
| S4 | Alert `AuditGovernanceBacklogDegraded`, expr `audit_governance_backlog_age_seconds > 450` | ✅ **implemented** (`deploy/prometheus/alerts.yml:162-169`) |
| S5 | Relay counters `attempted/delivered/failed/dead` | ✅ **implemented** (registered `metrics.go:103-106`; incremented `relay.go:83/:112/:121/:135`) |
| S6 | **Distinct degraded signal** (`Degraded()` / degraded state readable by `/readyz`) | ❌ **missing** — `Ready()` returns bare `nil`; no accessor distinguishes degraded from healthy |
| S7 | **`/readyz` degraded payload** (200 + marker) and 200-drill test | ❌ **missing** — `readyzHandler` always emits `{"ok":true}` (`http.go:71-73`); `http_test.go` passes `nil` extra everywhere |
| S8 | **Read-path probe timeout → degrade, never 503** (D1 read-path half) | ❌ **missing** — `Ready()`'s two store probes run on the caller's unbounded context (`runtime.go:163`, `:174` via `BacklogAge`); a hung store holds `/readyz` and 503s on eventual error |
| S9 | **Alert-rule pin test** (the acceptance "grep" made testable) | ❌ **missing** — no test references `deploy/prometheus/alerts.yml` |
| S10 | **Gauge scrape-surface pin** (oldest-age "counter-registration test") | ❌ **missing** — no test asserts `audit_governance_backlog_age_seconds` in a scrape |
| S11 | **T-3 gauge-level pin** (failed row → oldest-age gauge 0) | ❌ **missing** — repo-level pin exists (`audit_governance_test.go:442`), runtime-level `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:473`) covers the **empty** store only |

**In scope:** ① preserve the S1–S5 semantics (already shipped) and add the missing pins; ② the S6–S8 delta (degraded signal + `/readyz` degraded payload + probe-timeout degrade); ③ the S9–S11 pins. **Out of scope:** B3-1 (permanent-error classification — sibling direction 1 of the same analysis), B3-3 (deterministic fact IDs — direction 3), B3-6 (`Validate()`), billing-runtime readiness, `readyzProbeTimeout`/storage-probe branch, `/healthz`, any migration/config-surface change, any `internal/api/rest` handler change, and any new metric beyond the shipped counter family + age gauge (in particular **no** `audit_governance.degraded` gauge: the acceptance's "degraded counter/alert" is satisfied by the shipped alert arm, §5 AC-1; see §4 D3).

---

## 2. Evidence verification

Every citation in the direction was checked against the current working tree (HEAD `15763e2` + uncommitted changes).

| # | Direction citation | Verified location (current tree) | Verdict |
|---|---|---|---|
| E1 | "`Runtime.Ready` (`internal/auditgovernance/runtime.go:145-159`) still returns a hard error when `time.Since(oldest) > maxLag`" | `Ready` now at `runtime.go:162-182`; `BacklogAge` `:151-159`; the maxLag flip at `:178-181` returns **nil** and logs `"audit governance relay degraded"` (warn) — no error, no 503. Drain-in-progress still hard-errors `:167-169`; store errors still hard-error `:164-166`, `:175-177` | ❌ **stale — already fixed by `15763e2`** (S1/S2). The direction's citation lines `145-159` match the pre-fix layout. |
| E2 | "`cmd/server/http.go:66-68` maps to /readyz HTTP 503 'runtime dependency unavailable'" | `readyzHandler` `cmd/server/http.go:51-73`; `extra.Ready(req.Context())` `:66` → `http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)` `:67-68`; `readyzProbeTimeout = 2s` `:34-38` wraps **only** the storage probe `:59-61`; healthy body `{"ok":true}` `:71-73`; route `r.Get("/readyz", readyzHandler(repo, store, extraReady))` `:101` | ✅ **holds.** The 503 mapping still exists verbatim — and because `Ready()` now returns nil on lag, the mapping is inert for the lag case. No degraded payload path exists (S7). |
| E3 | "a backlog in the relay takes the whole instance out of rotation even though the write path (audit_log/object_events) is healthy" | Write path: `AdminHandler.auditForTenant` (`internal/api/rest/admin.go:404-425`) → `repo.RecordAudit` (best-effort, swallows errors); relay: `deliverBatch`/`deliverFact` (`relay.go:58-113`) | ✅ **premise holds**; the D1 drill premise (write path independent of relay lag) is unchanged. |
| E4 | "`internal/repository/audit_governance_claim.go:169-175` — `OldestPending` excludes `failed_at_ns`" | `OldestPendingAuditGovernance` now `:188-196`; predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` at `:195`; `HasPendingDrainingAuditGovernance` `:202-208` (same terminal exclusion + `b.state='draining'` `:205-207`) | ✅ **holds** (line drift only). D1 baseline for "oldest" is correct — T-3's lag side needs no repository change. |
| E5 | "`internal/telemetry/metrics.go:91-98` registers `event_outbox.*` but no `audit_governance.relay_*` counters" | `event_outbox.*` now `:95-102`; **`audit_governance.relay_attempted/delivered/failed/dead_total` now `:103-106`**; age gauge `audit_governance.backlog_age_seconds` via `RegisterAuditGovernanceBacklogAgeGauge` `:354-365` | ⚠️ **stale — counters shipped by `15763e2`** (S5). The oldest-age observability shipped as an observable **gauge**, not a counter (the correct instrument for an age; see §4 D2). |
| E6 | "`internal/auditgovernance/relay.go:82-104` (`deliverFact`/`failFact`/`retryFact` = instrumentation points)" | Instrumented on this checkout: `telemetry.IncAuditGovernanceRelayAttempted` `relay.go:83` (entry of `deliverFact`); `IncAuditGovernanceRelayDelivered` `:112` (only after `CompleteAuditGovernance` returns nil); `IncAuditGovernanceRelayDead` `:121` (entry of `failFact`); `IncAuditGovernanceRelayFailed` `:135` (entry of `retryFact`) | ⚠️ **stale — increments shipped** (S5). The cited lines are still the right instrument points; they are now wired. |
| E7 | "`deploy/prometheus/alerts.yml` has no audit-relay lag/dead-row rules (only L2 outbox rules at lines 104-112)" | `EventOutboxTerminalFailures` now `alerts.yml:104-112`; **new group `aero-vault-audit-governance` `:156-169` with rule `AuditGovernanceBacklogDegraded`: `expr: audit_governance_backlog_age_seconds > 450`, `for: 10m`, `severity: warning`, description "Oldest pending audit fact is {{ $value }}s old (> 450s = maxLag×0.5)…"** | ⚠️ **stale — rule shipped** (S4). Note: a **case-sensitive** `grep -E 'audit.*(lag|oldest|dead)'` matches only the comment (`:158`) and description (`:169`), **not** the `expr` line — the acceptance's grep must be pinned as a YAML parse (§5 AC-2), not a raw grep. |
| E8 | "`internal/auditgovernance/runtime_test.go:358` `TestRelayLogsNeverExposeRawFactInputs` (test harness precedent)" | `runtime_test.go:368` (drift from :358); harness helpers `runtimeConfig` (maxLag 4 s, poll 10 ms) at `runtime_test.go:40-46`; `lockedBuffer` `:23-36` | ✅ **holds** (line drift). `runtime_test.go` is **498 lines** — at the ≤500-line hard gate, so all new runtime tests must go to a **new file** `runtime_ready_test.go` (see §3 REQ-6). |
| E9 | "terminal rows excluded from `OldestPendingAuditGovernance` … T-3 baseline partially correct" | repo-level pin: `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`internal/repository/audit_governance_test.go:419`; claim+fail at `:432-436`; never re-claimed `:438-440`; `OldestPending ok==false` `:442`; retention prune `:444-449`) | ✅ **holds.** The `OldestPending` half of T-3 is pinned at repository level; the **gauge half is not** (S11). |
| E10 | Test coverage of the shipped subset | `TestRuntimeReadyDegradesOnBacklogLag` `runtime_test.go:415-467` (lag > maxLag 4 s → `Ready` nil; draining → error); `TestRuntimeBacklogAgeZeroWhenNoPending` `:473-497` (empty store only); `TestAuditGovernanceMetrics_SurfaceInScrape` `internal/telemetry/metrics_test.go:82-108` (four counter names, value 1); `TestRuntimeRelayCountersTrackDeliveryOutcomes` `internal/auditgovernance/relay_metrics_test.go:88` (per-binding sink e2e: exactly one delivered + one dead, ≥ attempted/failed); `cmd/server/http_test.go:69-129` (three readyz storage-probe tests, **all with `nil` extra**) | ✅ **as cited** — the gaps are exactly S6–S11 (no degraded sentinel test, no `/readyz`-200 drill, no gauge scrape pin, no alerts.yml pin, no failed-row gauge pin). |
| E11 | "the contract requires the maxLag flip to become a degraded state plus a 450s alert, and read-path timeouts to degrade instead of 503 (D1 drill)" | `docs/campaigns/implementation-gate.md:22` — "maxLag 翻转移除 → `degraded` + **maxLag×0.5（450s）告警**；终态行排除出 `OldestPending`；**读路径超时降级非 503**；D1 drill：sink 停 60min → `/readyz` 200 + 450s 告警；无重启循环"; `MaxLagSeconds` default 900 (`internal/config/config_audit_governance.go:66`), validation `> ClaimTTLSeconds` `:241`, `<= 604_800` `:251` → 450 = 900 × 0.5 | ✅ **holds.** The "degraded state" (observable signal) and "read-path timeouts degrade" clauses are **not yet implemented** (S6/S8). |
| E12 | "B3-4 is a hard gap: zero telemetry calls in `internal/auditgovernance/*.go`" | `grep -rn "telemetry\." internal/auditgovernance/*.go` → `relay.go:83/:112/:121/:135` (four increments); import in `relay.go` | ❌ **stale — instrumentation shipped by `15763e2`** (S5). The remaining B3-4 gap is the **oldest-age pin** (S10) and the alert-rule pin (S9), not the counters themselves. |

**Problem-statement checks (current tree):**

| Statement | Verdict |
|---|---|
| "`Ready()` still returns a hard error when lag > maxLag, mapped to /readyz 503" | ❌ **stale** — `Ready` returns nil + warn (`runtime.go:178-181`); the 503 mapping (`http.go:67-68`) is now unreachable for the lag case. |
| "the contract requires maxLag flip → degraded state + 450s alert" | ⚠️ **half done** — alert shipped (E7); the *degraded state* (distinct signal + `/readyz` marker) is not (S6/S7). |
| "read-path timeouts to degrade instead of 503 (D1 drill)" | ❌ **unimplemented** — `Ready()`'s probes use the unbounded caller ctx (`runtime.go:163`, `:174`); only the storage probe has the 2 s bound (`http.go:59-61`). |
| "terminal rows already excluded from `OldestPending` — T-3 baseline partially correct" | ✅ **holds** (E4, E9); gauge half unpinned. |
| "zero telemetry calls in `internal/auditgovernance/*.go`; no `audit_governance.relay_*` counters; alerts.yml has no audit-relay rules" | ❌ **stale** — all three shipped (E5/E6/E7); pins for gauge + rule are missing. |

---

## 3. Requirements

### REQ-1 — Preserve the shipped `Ready()` decoupling contract (S1/S2; pin, don't re-implement)

`Runtime.Ready` (`internal/auditgovernance/runtime.go:162-182`) must keep exactly these semantics, already shipped by `15763e2`:

- **Backlog beyond `maxLag`** (`:178-181`): `BacklogAge` ok && age > maxLag → warn log, **return nil** — never an error, never 503.
- **Drain in progress** (`:167-169`): unchanged hard error `"audit governance binding drain is in progress"`.
- **Store errors** (non-context): unchanged hard errors `"audit governance drain lookup failed"` / `"audit governance backlog lookup failed"` (`:164-166`, `:175-177`).
- **Terminal rows** excluded from the age baseline by the repository predicate (`claim.go:195`), unchanged.

Tests: existing `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:415`) already pins the lag-nil and drain-error branches — keep it green verbatim. Do not alter error strings (unconstrained by tests; flowed only into server logs).

### REQ-2 — Read-path probe timeout → degraded, never 503 (S8; the unimplemented D1 read-path half)

In `internal/auditgovernance/runtime.go`:

- Add a package-level constant `storeProbeTimeout = 2 * time.Second` with a comment cross-referencing `readyzProbeTimeout` (`cmd/server/http.go:34-38` — same rationale, same value, independent symbol; not derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS`).
- `Ready()` must run **both** store probes (`HasPendingDrainingAuditGovernance` `:163`, `OldestPendingAuditGovernance` via `BacklogAge` `:174`) under `context.WithTimeout(ctx, storeProbeTimeout)`.
- **Probe ctx timeout/cancel** (`errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)`) on either probe → record degraded (age unknown), **return nil** — a wedged store degrades the audit-governance readiness contribution, it never 503s the node.
- Non-context store errors keep the REQ-1 hard-error behavior (fail-closed for genuine failures, per §2 E11's "store errors remain fail-closed" note in `runtime.go:171-173`).
- `BacklogAge` may keep its caller-ctx signature (used by the gauge callback in `cmd/server/build.go:113-120`, which must remain store-blocking-free per REQ-5); the probe bound lives in `Ready()` only. If `BacklogAge` is reused for the degraded-age recording (REQ-3), the timeout must be applied where the probe happens.

### REQ-3 — Distinct degraded signal (S6; the acceptance's "nil error + degraded signal")

The maxLag flip and the REQ-2 probe-timeout must produce a **distinct, readable degraded state**, not a bare `nil`:

- `Runtime` gains two mutex-protected cache fields (`degraded bool`, `backlogAge time.Duration`) written by `Ready()` on every call (all branches: degraded=true for lag>maxLag and probe-timeout; degraded=false otherwise; age = `time.Since(oldest)` when ok, else 0).
- Expose `func (r *Runtime) Degraded() bool` and `func (r *Runtime) BacklogAge() time.Duration` — **cache getters, zero store I/O** (this is what makes the `/readyz` payload and gauge scrape hang-proof). Freshness ≤ one probe (every `/readyz` call re-probes via `Ready()`, so the payload reflects the live state on read).
- No new config knob; the constant from REQ-2 governs the probe bound.

### REQ-4 — `/readyz` degraded payload: 200 + marker (S7; the acceptance's "/readyz stays 200 while a degraded counter/alert fires")

In `cmd/server/http.go`:

- Add next to `readinessChecker` (`:30-31`): `type degradedChecker interface { Degraded() bool; BacklogAge() time.Duration }`.
- `readyzHandler` (`:51-73`): after `extra.Ready` succeeds, type-assert `extra.(degradedChecker)`; when implemented and `Degraded()` → **HTTP 200**, `Content-Type: application/json`, body `{"ok":true,"degraded":true,"backlog_age_seconds":<int64 seconds>}`.
- Healthy path stays **byte-identical** `{"ok":true}` (`:71-73`) — the existing body assertion in `TestReadyzErrNotFoundIsReady` (`http_test.go:93-108`) must stay green.
- `extra.Ready` error → 503 `runtime dependency unavailable` unchanged (`:66-68`). Storage probe and `readyzProbeTimeout` unchanged. `readinessGroup` (`:40-48`) needs no change unless a member implements `degradedChecker`; with one audit runtime, pass the runtime (or a small wrapper) as `extra` and let the type assertion see it.

### REQ-5 — Relay counters + oldest-age gauge: preserve surface and wiring (S5/S3; the acceptance's "registered in metrics.go and incremented in deliverFact/failFact/retryFact")

Already shipped; requirements are **preservation + pinning**:

- Four counters stay registered in `metrics.go:103-106` (`audit_governance.relay_attempted_total` / `relay_delivered_total` / `relay_failed_total` / `relay_dead_total`) and incremented exactly at `relay.go:83` (attempted, every `deliverFact` entry), `:112` (delivered, only after `CompleteAuditGovernance` nil), `:121` (dead, every `failFact` entry), `:135` (failed, every `retryFact` entry).
- The oldest-age observability is the **observable gauge** `audit_governance.backlog_age_seconds` (`metrics.go:354-365`, `RegisterAuditGovernanceBacklogAgeGauge`), wired in `cmd/server/build.go:113-120` only when `auditRuntime != nil` (registration unconditional w.r.t. `PROMETHEUS_ENABLED`, mirroring `registerGauges`). Its callback reads `BacklogAge` (REQ-3 cache getter) — a scrape must never block on the store.
- **Pin (S10):** a metrics test must assert the gauge series surfaces in a scrape (see REQ-6 T5) — this is the acceptance's "assert via metrics reader or counter-registration test" for the oldest-age instrument.

### REQ-6 — Missing acceptance pins (S9/S10/S11; the acceptance checks made testable)

Five new/extended tests. Hard gates: `runtime_test.go` is 498/500 lines → new runtime tests go in a **new file `internal/auditgovernance/runtime_ready_test.go`**; `http_test.go` 129 lines (+~50 OK); `metrics_test.go` 135 lines (+~30 OK). Harness: reuse `runtimeConfig` (`runtime_test.go:39-46`, maxLag 4 s, poll 10 ms) and the embedded-interface fake idiom (`cmd/server/http_test.go:27-56`).

- **T1 (AC-1, runtime half) — `runtime_ready_test.go`: `TestRuntimeReadyDegradedSentinel`.** Seed one pending fact via `InsertEventWithGovernance`; backdate `created_at_ns` deterministically via a second `database/sql` connection to the same `file:` DSN (`UPDATE audit_governance_outbox SET created_at_ns = <now-8s> WHERE id=?`, no sleeps — WAL allows a second writer). Assert: `Ready(ctx) == nil` **and** `Degraded() == true` **and** `BacklogAge() > 4*time.Second` (the distinct degraded sentinel); then drain the row (complete) and assert `Degraded() == false`. A second case with a hanging store fake (probe blocks on `<-ctx.Done()`, returns `ctx.Err()`; loopback base URL so `New()` makes no network calls) asserts: `Ready(context.Background())` returns **nil** with elapsed ∈ [1 s, 5 s] (blocking stub ⇒ response cannot precede the 2 s probe timeout; upper bound proves boundedness — mirror `TestReadyzStorageProbeTimeout` idiom, `http_test.go:69-88`), `Degraded() == true`, `BacklogAge() == 0` (age unknown).
- **T2 (AC-1, handler half) — `cmd/server/http_test.go`:** `TestReadyzDegradedExtraReturns200WithMarker` (fake `{Ready→nil, Degraded→true, BacklogAge→123s}` → status **200**, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`); `TestReadyzHealthyExtraReturns200Unchanged` (same fake `Degraded→false` → 200, body exactly `{"ok":true}`); `TestReadyzAuditGovernanceDegradedDrill` (real `auditgovernance.New` + hanging store + `readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)` → **200** never 503, body contains `"degraded":true`, elapsed ∈ [1 s, 5 s]; fake healthy → 200 `{"ok":true}`).
- **T3 (AC-2) — `internal/telemetry/metrics_test.go`: `TestAlertsYMLAuditGovernanceRuleConsistency`.** YAML-parse `../../deploy/prometheus/alerts.yml` (package-relative; no promtool in CI, `go test ./...` is the artifact gate): assert rule `AuditGovernanceBacklogDegraded` exists with `expr` referencing exactly `audit_governance_backlog_age_seconds` and threshold `> 450`, `for: 10m`, `severity: warning`, and no other `audit_governance_*` name in the expr (guards rule/metric drift both ways). This is the acceptance's "grep 'audit.*(lag|oldest|dead)' + duration 450s" in a robust form — the case-sensitive grep matches only the rule's comment/description lines today (§2 E7), so the pin **must** be a YAML parse asserting the 450 s threshold.
- **T4 (AC-3, oldest-age surface) — `internal/telemetry/metrics_test.go`: `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`.** Register `RegisterAuditGovernanceBacklogAgeGauge` once (OTel rejects duplicate instruments) with a fixed callback returning `450` → scrape body line-exact `audit_governance_backlog_age_seconds 450` (reuse `scrapeValue`, `metrics_test.go:61-75`); re-scrape after callback returns `0` → `0`. Registration must be single-shot across the package (TestMain or one test), mirroring `TestObservableGauges_SurfaceInScrape` (`:114`).
- **T5 (AC-4) — `runtime_ready_test.go`: `TestRuntimeBacklogAgeZeroWhenAllTerminal`.** Seed one fact; `ClaimAuditGovernance(ctx,"t","tok",1,1,time.Minute)` + `FailAuditGovernance(ctx,id,"t","tok","conflict:true")` (lease-fenced public API, `claim.go:159-172`). Assert: `OldestPendingAuditGovernance` ok==false (already pinned at repo level `audit_governance_test.go:442`; re-pin at runtime level) **and** `runtime.BacklogAge(ctx)` ok==false (gauge 0) **and** `Ready(ctx) == nil` **and** `Degraded() == false` — a fully dead-lettered backlog contributes neither to `OldestPending` nor to the oldest-age gauge, and never blocks readiness.

---

## 4. Decisions & non-goals

- **D1 — Probe bound is a package constant, not config.** `storeProbeTimeout = 2 s` in `internal/auditgovernance`, mirroring `readyzProbeTimeout` (`http.go:38`); not derived from the relay HTTP timeout (default 5 s, too slow for a readiness probe); no new env knob, no `.env.example`/validation/docs surface.
- **D2 — Oldest-age is a gauge, not a counter.** The acceptance's "counters … oldest-age" is satisfied by the shipped observable gauge `audit_governance.backlog_age_seconds` (an age is a state, not an event; the sibling spec `cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-4 name-locked this name). No second oldest-age instrument may be added.
- **D3 — No `audit_governance.degraded` gauge/counter.** The acceptance's "degraded counter/alert" is satisfied by the shipped alert (`alerts.yml:163`, `> 450` — fires exactly when the default-maxLag degraded condition holds) plus the AC-1 drill proving it fires while `/readyz` is 200. Adding a degraded flag gauge is the sibling direction's lock-in (`cmd-server-…` REQ-4) but is **not** in this direction's acceptance — out of scope.
- **D4 — Degraded signal is a cache, not a live query.** `Degraded()`/`BacklogAge()` do zero store I/O; `/readyz` re-probes live via `Ready()` on every request (so the payload is fresh), while the gauge scrape reads the last probe result (never blocks).
- **D5 — Alert threshold stays a literal 450 = maxLag default 900 × 0.5**, `for: 10m`, `severity: warning` — all as shipped; no expression change (alerts.yml has no templating). Non-default `maxLag` drift is out of scope (the sibling spec's startup-warning requirement is not in this direction's acceptance).
- **Non-goals:** B3-1 (permanent-error classification — direction 1 of the same analysis, sibling spec `internal-auditgovernance-terminal-classification-v1.spec.md`), B3-3 (deterministic fact IDs — direction 3), B3-6 (`Validate()`), billing-runtime readiness, `readyzProbeTimeout`/storage-probe branch, `/healthz`, any `internal/api/rest` handler or OpenAPI change, any migration/config-surface change, `docs/configuration.md:274` wording (currently stale: "Oldest undelivered outbox age that `/readyz` permits" — flagged for a follow-up, not a requirement here).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (D1 drill) —** *"unit test asserts `Ready()` returns a distinct degraded sentinel (nil error + degraded signal) when lag > maxLag, and /readyz stays 200 while a degraded counter/alert fires."*
*Testable:* **T1 + T2** (REQ-6). T1: seeded fact backdated 8 s (> maxLag 4 s) → `Ready()==nil` ∧ `Degraded()==true` ∧ `BacklogAge()>4s`; hanging store → `Ready()==nil` within [1 s, 5 s] ∧ `Degraded()==true`. T2: `readyzHandler` with a degraded real runtime returns **200** with `"degraded":true` (never 503), recovery restores 200 `{"ok":true}`; **T3** pins the alert (`> 450`) whose expr is satisfiable by the same gauge the drill drives — i.e., the alert fires while `/readyz` is 200. The "distinct sentinel" is `Degraded()` (REQ-3); "nil error" is `Ready()==nil` (REQ-1).

**AC-2 (alert rule) —** *"alert rule for 450s backlog age exists in `deploy/prometheus/alerts.yml` (grep 'audit.*(lag|oldest|dead)' + duration 450s)."*
*Testable:* **T3** (REQ-6). The rule already exists (`alerts.yml:162-169`); T3 YAML-parses the artifact and asserts rule `AuditGovernanceBacklogDegraded`, expr `audit_governance_backlog_age_seconds > 450`, `for: 10m`, `severity: warning`. Note: the literal case-sensitive grep matches only the rule's comment/description today (E7) — the pin is the parse, with the 450 s threshold asserted structurally (this is the CI artifact gate: `go test ./...`).

**AC-3 (relay counters) —** *"relay counters attempted/delivered/failed/dead/oldest-age registered in `metrics.go` and incremented in `deliverFact`/`failFact`/`retryFact` (assert via metrics reader or counter-registration test)."*
*Testable:* **shipped and pinned** — `TestAuditGovernanceMetrics_SurfaceInScrape` (`metrics_test.go:82`) asserts the four counter names/values in a scrape (metrics reader); `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`) asserts the increment semantics per outcome; **T4** adds the missing oldest-age half: `audit_governance_backlog_age_seconds` surfaces in a scrape with the registered callback value (counter-registration test). "Incremented in `deliverFact`/`failFact`/`retryFact`" verified at `relay.go:83/:112/:121/:135` (§2 E6).

**AC-4 (T-3 pin) —** *"pin test that a `failed_at_ns>0` row contributes neither to `OldestPending` nor to the oldest-age gauge."*
*Testable:* **T5** (REQ-6) + existing `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:419`, `OldestPending ok==false` at `:442`). T5 seeds one row, lands it terminal via the fenced public API, and asserts `OldestPending` ok==false **and** `BacklogAge` ok==false (gauge 0) **and** `Ready()==nil` ∧ `Degraded()==false` — the dead row contributes to neither input.

---

## 6. Risks

- **Re-implementing shipped semantics** — REQ-1/REQ-5 are preservation requirements; the existing tests (`TestRuntimeReadyDegradesOnBacklogLag`, the two counter tests) are the regression net. Any change to the maxLag branch must keep `runtime_test.go:415` green.
- **Timing flake** — mitigated by the proven idioms: blocking stubs (response cannot precede the 2 s probe deadline ⇒ deterministic lower bound), backdating via `UPDATE created_at_ns` on a second WAL connection instead of sleeps, and `>`/interval assertions only (no wall-clock equality). T1's 8 s backdate vs. 4 s maxLag gives 2× margin.
- **Duplicated-instrument panic** — OTel rejects re-registration of `audit_governance.backlog_age_seconds` (already registered by `cmd/server/build.go` in prod wiring); T4 must register exactly once in the test binary (single-shot TestMain or one test), mirroring `TestObservableGauges_SurfaceInScrape`'s shared-handler pattern (`metrics_test.go:114`).
- **Hard gates** — `runtime_test.go` at 498/500 lines forces the new `runtime_ready_test.go` (mandatory, per I-gate and the sibling spec's precedent); `http_test.go` 129 + ~50 = OK; `metrics_test.go` 135 + ~30 = OK; `runtime.go` 231 + ~40 = OK; `metrics.go` 444 + ~0 (no new registration — the gauge exists) = OK. All new tests use SQLite + local FS + `httptest` (zero network/Docker), and no `go.mod` change.
- **Alert/metric name drift** — T3 pins the rule expr to exactly the shipped gauge name and forbids other `audit_governance_*` names in the expr; T4 pins the gauge export name (`dots → underscores`, no `_total`). Drift in either direction fails `make check`.
- **`make check`** — implementation must pass gofmt/build/vet/test; all line numbers above re-confirmed on this checkout (working tree at HEAD `15763e2` + uncommitted changes).

*Verification basis: every citation re-checked on this checkout; line numbers reflect the working tree as read during this spec's production.*
