# Requirements Specification — `internal/access`: relay metrics (attempted/delivered/failed/dead/oldest-age) + `Ready()` degraded flip instead of 503 (contract items 2 and 4, D1)

**Module:** `internal/access` (analysis label; implementation surface is `internal/auditgovernance` + `internal/telemetry` + `cmd/server` + `deploy/prometheus` — see §1)
**Direction:** "Relay metrics (attempted/delivered/failed/dead/oldest-age) + Ready() degraded flip instead of 503 (contract items 2 and 4, D1)" (direction 3)
**Source analysis:** `docs/auto/analyses/internal-access-f4571c58.json`
**Contract:** `docs/campaigns/implementation-gate.md:22` (gate item 2: Ready 解耦 H1 — maxLag flip removed → degraded + maxLag×0.5 (450 s) alert; terminal rows excluded from `OldestPending`; read-path timeouts degrade non-503; D1 drill) and `:24` (gate item 4: Relay metrics H6 — attempted/delivered/failed/dead/oldest-age; metrics feed H2 alerts; stalled relay detectable)
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 7 / risk reduction 6 / effort 5 / confidence 8

---

## 1. Module & scope

The analysis file labels this direction under `internal/access`, but — as the analysis itself states — **no cited evidence or required change lives in `internal/access/`** (verified: no `internal/access` symbol appears in the direction's evidence list; the layer merely feeds `RecordAudit`/`InsertEvent`). The audit-governance delivery pipeline implementing the contract lives in `internal/auditgovernance` (relay `relay.go`, runtime `runtime.go`), `internal/telemetry` (`metrics.go` — metric definitions), `cmd/server` (`http.go` — readiness handler), and `deploy/prometheus/alerts.yml` (alert rule). The module label is retained for traceability to the analysis.

**Problem (verified):** the relay has **zero telemetry** — `grep -rn "telemetry" internal/auditgovernance/` → no hits — despite the `event_outbox.*` counter family precedent (`internal/telemetry/metrics.go:91-98` + `IncEventOutbox*` helpers `:124-172`, incremented in `internal/events/event_outbox_relay.go:328/:347/:349`) and the `EventOutboxTerminalFailures` alert (`deploy/prometheus/alerts.yml:99-112`). A stalled or wedged audit sink is therefore **undetectable**: no series a SRE could alert on, and `Runtime.Ready()` (`internal/auditgovernance/runtime.go:145-161`) hard-errors when `OldestPendingAuditGovernance` age exceeds `maxLag` (`:157-159`, default 900 s — `internal/config/config_audit_governance.go:66`), which `readyzHandler` (`cmd/server/http.go:51-74`) maps to HTTP **503** (`:66-68`) — taking the whole node out of the LB pool with no degraded tier, no 450 s alert, no oldest-pending surface.

**In scope:** ① four relay counters (`attempted`/`delivered`/`failed`/`dead`) incremented at `deliverFact`/`failFact`/`retryFact`; ② an oldest-pending-age gauge (+ degraded flag gauge) fed by a cache getter; ③ `Ready()` maxLag hard flip → degraded (nil error), store-probe timeouts degrade instead of 503, drain-in-progress stays a hard 503; ④ `/readyz` 200-with-degraded-marker payload; ⑤ one 450 s alert rule mirroring the `event_outbox` rule. **Out of scope:** contract item 1 (permanent-error classification — sibling spec `internal-access-audit-governance-permanent-error-classification-v1.spec.md`), contract item 3 (deterministic fact IDs), B3-6 (`Validate()`), billing-runtime readiness, events-outbox changes, any migration/config-surface change.

**Composition with the sibling spec:** `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` (from analysis `cmd-server-7a3bfea7.json`, direction B3-2) already specifies the same contract item 2 in depth and — in its §4 non-goals — **requires this direction to reuse its gauge names** (`audit_governance.backlog_age_seconds`, `audit_governance.degraded`), its alert name (`AuditGovernanceBacklogDegraded`), and its `Degraded()`/`BacklogAge()` cache surface, adding only `relay_*` counters. This spec adopts those names verbatim (REQ-2/REQ-3/REQ-5) so the two specs compose without drift; where this spec and the sibling both specify the flip, the semantics are identical by construction.

---

## 2. Evidence verification

Every citation in the direction was checked against this checkout (`acfaaf4`).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | "zero telemetry — grep of internal/auditgovernance for telemetry/metrics returns nothing" | `grep -rn "telemetry" internal/auditgovernance/` → exit 1 (no hits); `grep -rn "metrics\." internal/auditgovernance/` → no hits | ✅ **exact.** No instrumentation, no metric registration, no telemetry import anywhere in the package. |
| E2 | "event_outbox counter precedent — metrics.go event_outbox.delivered_total/retried_total/failed_total" | `internal/telemetry/metrics.go:91-98`: `m.Int64Counter("event_outbox.delivered_total")` `:91`, `retried_total` `:92`, `failed_total` `:93`, `claim_lost_total` `:94`, `pruned_total` `:95`, `l2_*` `:96-98`, all on the `aero-vault/domain` meter (`otel.Meter("aero-vault/domain")` `:61`, inside `initDomain`/`domainOnce`); increment helpers `IncEventOutboxDelivered` `:124`, `IncEventOutboxRetried` `:130`, `IncEventOutboxFailed` `:137`; increments at `internal/events/event_outbox_relay.go:328` (`complete` success), `:347` (terminal failed), `:349` (retried) | ✅ **exact.** This is the template for REQ-1 (naming, helper shape, increment placement after the durable write). |
| E3 | "corresponding alert in deploy/prometheus/alerts.yml:99-111" | `deploy/prometheus/alerts.yml:99-112`: comment block `:99-104`, `- alert: EventOutboxTerminalFailures` `:105`, `expr: sum(rate(event_outbox_failed_total[15m])) > 0` `:106`, `for: 5m` `:107`, `severity: warning` `:109` (integrity group); `grep -n "audit" alerts.yml` → hits only the `:104` comment and `:112` description — **no audit-governance rule anywhere** | ✅ **exact** (rule block spans `:105-112`; the `:99-111` citation covers the comment+rule). Alert-path precedent for REQ-5: `warning` severity, `for: 5m`, `sum(rate(...))` shape. |
| E4 | "Ready() returns an error when OldestPending exceeds maxLag — runtime.go Ready" | `Runtime.Ready` `internal/auditgovernance/runtime.go:145-161`: drain lookup `:146` → hard error `:147-149`; draining → hard error `:150-152`; backlog lookup `:153` → hard error `:154-156`; **maxLag flip** `:157-159` `if ok && time.Since(oldest) > r.maxLag { return errors.New("audit governance backlog exceeds maximum lag") }`; `nil` `:160`. `grep -rn "\.Ready(" internal/auditgovernance/ cmd/server/` → only `http.go:66` — **no test anywhere asserts `Ready()`'s maxLag or drain errors** | ✅ **exact.** The flip's error string has zero dependents; removing it is unconstrained by existing tests (REQ-3, AC-1). |
| E5 | "cmd/server/http.go readyzHandler maps any readiness error to HTTP 503" | `readyzHandler` `cmd/server/http.go:51-74`; `extra.Ready(req.Context())` `:66` → `http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)` `:67-68`; `readinessGroup` `:40-48` (sequential, first error wins); `readyzProbeTimeout = 2 * time.Second` `:34-38` wraps **only** `store.Stat` `:59-61` — the `extra.Ready` call has **no deadline of its own**; healthy body `{"ok":true}` `:71-73`; route `r.Get("/readyz", readyzHandler(repo, store, extraReady))` `:101`; wiring `runtimeReadiness`/`buildAuditGovernanceRuntime` `cmd/server/audit_governance.go:15-49`, `:51-64` | ✅ **exact.** No error classification, no degraded tier; the 503-on-any-error claim holds. |
| E6 | "no audit_governance.* metric — verified absent" | `grep -rn "audit_governance\|AuditGovernance" internal/telemetry/ deploy/prometheus/alerts.yml` → exit 1 (no hits) | ✅ **exact.** No counter, gauge, or alert exists for the relay anywhere. |
| E7 | "OldestPendingAuditGovernance/HasPendingDrainingAuditGovernance — failed rows already excluded" | `OldestPendingAuditGovernance` `internal/repository/audit_governance_claim.go:188-201`: `SELECT MIN(o.created_at_ns) ... WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`; `HasPendingDrainingAuditGovernance` `:202-210`: same predicate + `b.state='draining'` | ✅ **exact.** Dead (`failed_at_ns>0`) rows are excluded from lag and from drain-pending — REQ-3's lag semantics need no repository change (AC-2). |
| E8 | "existing TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned assertion kept green" | `internal/repository/audit_governance_test.go:334-372`: fencing (stale owner/token cannot fail) `:347-350`, never re-claimed `:354-356`, **never pending** `:357-358` (`OldestPendingAuditGovernance` → `ok==false`), retention prune before→0 / after→1 `:361-365`, re-enqueueable after prune `:368-371` | ✅ **exact.** This test is the AC-2 invariant pin; it must remain untouched and green. |
| E9 | "counters incremented at deliverFact/failFact/retryFact" | `deliverFact` `internal/auditgovernance/relay.go:80-102` (Publish `:83-90`; success → `CompleteAuditGovernance` `:95-100`); `failFact` `:111-122` (terminal-with-retention); `retryFact` `:124-137` (bounded backoff reschedule). `grep -n "func " relay.go` → no telemetry call anywhere | ✅ **exact** — the three increment sites exist and are un-instrumented. |
| E10 | "450s constant is contract-specified, no in-repo evidence" | `docs/campaigns/implementation-gate.md:22` — "maxLag 翻转移除 → `degraded` + **maxLag×0.5（450s）告警**"; `MaxLagSeconds` default 900 (`internal/config/config_audit_governance.go:66`), validation `> ClaimTTLSeconds` `:241`, `<= 604_800` `:251`; `PollMilliseconds` default 1000 `:61`. 450 = 900 × 0.5; no other in-repo occurrence of 450 | ✅ **as stated.** 450 is contract-specified, fixed-literal (alerts.yml has no templating); REQ-5 documents it as `maxLag`-default-900 × 0.5 and adds a config-true degraded arm. |
| E11 | Contract items 2 and 4 | `docs/campaigns/implementation-gate.md:22` (item 2: 删 `Ready()` 翻转、`degraded` + 450 s 告警、终态行排除、读路径超时降级非 503; D1 drill) and `:24` (item 4: `internal/auditgovernance/relay.go`/`runtime.go` — 0 Observe; attempted/delivered/failed/dead/oldest-age; 指标可喂 H2 告警; stalled relay 可检测) | ✅ **contract located.** Both items are in this direction's title; item 1/3 are the sibling directions of the same analysis. |
| E12 | Sibling spec name-lock | `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-4 (`audit_governance.backlog_age_seconds`, `audit_governance.degraded`), REQ-5 (`AuditGovernanceBacklogDegraded`, expr `audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450`), §4 non-goal: "B3-4 ... must reuse the two gauges named in REQ-4 — it may add `relay_*` counters and its own rules but must not re-define an oldest-age gauge under another name" | ✅ **holds.** This direction IS the sibling's B3-4; REQ-2/REQ-5 adopt its names. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "The relay has zero telemetry" | ✅ **holds** (E1). |
| "Ready() returns an error when OldestPending exceeds maxLag, and readyzHandler maps any readiness error to HTTP 503" | ✅ **holds** (E4, E5). |
| "a lagging relay takes the whole node out of the LB pool; no degraded state, no 450s-oldest-pending alert, no metric a SRE could alert on" | ✅ **holds** (E4, E5, E3, E6). |
| "failed rows remain excluded from OldestPending" | ✅ **holds** (E7, E8) — the repository predicate already satisfies the contract's dead-row exclusion; this direction does not touch it. |

---

## 3. Requirements

### REQ-1 — Relay counters: attempted / delivered / failed / dead (contract item 4)

**`internal/telemetry/metrics.go`** — add to the struct block (after `mEventOutboxL2Rejected`, `:56`) four `metric.Int64Counter` fields, registered in `initDomain` (after the event_outbox block, `:98`) on the `aero-vault/domain` meter, and four helpers following the `IncEventOutbox*` shape (`:124-137`, lazy `initDomain()`, no attributes):

| Counter (OTel name) | Prometheus export (`_total`, dots→underscores) | Helper | Semantics |
|---|---|---|---|
| `audit_governance.relay_attempted_total` | `audit_governance_relay_attempted_total` | `IncAuditGovernanceRelayAttempted(ctx)` | one delivery attempt (claim processed) |
| `audit_governance.relay_delivered_total` | `audit_governance_relay_delivered_total` | `IncAuditGovernanceRelayDelivered(ctx)` | durable completion (receipt accepted + row completed) |
| `audit_governance.relay_failed_total` | `audit_governance_relay_failed_total` | `IncAuditGovernanceRelayFailed(ctx)` | transient failure → rescheduled (retry) |
| `audit_governance.relay_dead_total` | `audit_governance_relay_dead_total` | `IncAuditGovernanceRelayDead(ctx)` | terminal-with-retention (dead-letter; never re-claimed) |

**`internal/auditgovernance/relay.go`** — increment sites (the direction's "deliverFact/failFact/retryFact", E9):

- `deliverFact` **entry** (`:81`, first statement) → `attempted`. Counts every claim processed, including retries.
- `deliverFact` **success path** — after `CompleteAuditGovernance` returns nil (`:95-100`) → `delivered`. Placement mirrors the event_outbox precedent where `IncEventOutboxDelivered` fires only after the durable complete (`event_outbox_relay.go:328`); the acknowledgement-lost branch (`:96-99` warn-only) increments nothing (claim-lost is not in the contract's four-name list — non-goal).
- `retryFact` **entry** (`:125`) → `failed`. Analog of `event_outbox.retried_total` (`retry` reschedule, `event_outbox_relay.go:349`); the contract's "failed" is the transient, retry-scheduled class.
- `failFact` **entry** (`:112`) → `dead`. The terminal class — contract dead-letter terminology (`implementation-gate.md:21` "死信终态"; the repo column is `failed_at_ns`, a documented deviation owned by the T-3 sibling spec, not this one — the counter follows the **contract** name "dead").

The 4↔3 mapping (one counter pair shares `deliverFact`) is the direction's own "counters incremented at deliverFact/failFact/retryFact"; `attempted` = delivered + failed + dead + claim-lost path (delivered+failed+dead exactly, since claim-loss increments nothing).

### REQ-2 — Oldest-pending-age + degraded gauges (shared names, sibling-locked)

`internal/telemetry/metrics.go` — add `RegisterAuditGovernanceGauges(fn func(context.Context) (ageSeconds, degraded int64))` following the `RegisterQueueDepthGauge` observable-gauge pattern (`:326-334`), registering on the `aero-vault/domain` meter:

- `audit_governance.backlog_age_seconds` → `audit_governance_backlog_age_seconds` (oldest pending age, seconds; `0` when no pending or age unknown),
- `audit_governance.degraded` → `audit_governance_degraded` (0/1).

**Names are locked by the sibling spec** `cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-4 and its §4 non-goal: this direction must **not** re-name the gauge (no `oldest_pending_age_seconds`, no second gauge) — the direction's "oldest-pending-age gauge (names proposed — contract gives semantics only)" is resolved by the sibling's already-shipped naming. Callbacks must read **only** the REQ-3 cache getters — a `/metrics` scrape must never block on the store (a hung store degrades, it must not hang the scrape).

### REQ-3 — `Runtime`: degraded cache + `Ready()` flip replacement + probe timeout (contract item 2)

`internal/auditgovernance/runtime.go` (209 lines; +~55 → fine):

- **Cache surface.** Fields `mu sync.Mutex; degraded bool; backlogAge time.Duration`; getters `func (r *Runtime) Degraded() bool` and `func (r *Runtime) BacklogAge() time.Duration` — mutex-protected, **no store I/O** (this is what makes the `/readyz` payload and the `/metrics` gauge scrape hang-proof and deterministic). Freshness ≤ one poll interval (default 1 s — `config_audit_governance.go:61`).
- **Probe bound.** New package-level constant `storeProbeTimeout = 2 * time.Second` (comment must cross-reference `readyzProbeTimeout`, `cmd/server/http.go:38` — same rationale, same value, independent symbol). Both store probes in `Ready` (`:146`, `:153`) run through `probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)`.
- **`Ready()` semantics** (replaces `:145-161`):
  - probe ctx errors (`errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)`) on **either** store call → record degraded (age unknown → 0), **return nil** (never 503);
  - drain-in-progress → unchanged hard error `"audit governance binding drain is in progress"` (`:150-152`);
  - drain/backlog lookup non-context errors → unchanged hard errors `"audit governance drain lookup failed"` / `"audit governance backlog lookup failed"` (`:147-149`, `:154-156`);
  - **delete the maxLag hard flip** (`:157-159`) → record degraded with age `time.Since(oldest)`, **return nil**;
  - healthy (`!ok` or age ≤ maxLag) → record not-degraded (age `0` when `!ok`), return nil.
  - Resulting contract: `Ready()` errors only for drain-in-progress and genuine store failures; every condition that used to 503 the node is now `nil` + degraded state (E4/E5 — the error string has zero dependents).
- **Run-loop feed.** The `run()` loop (runtime.go run: calls `reconcile`, `deliverBatch`, `cleanupDelivered`) additionally probes once per poll cycle (after `cleanupDelivered`) through its own `probeCtx` and records the same cache — the contract's "run() 循环 maxLag×0.5（450s）降级告警" input (`implementation-gate.md:22`; the 450 s early-warning is realized by the alert rule REQ-5, which fires at 450 s = maxLag-default × 0.5, while `Degraded()` flips at `maxLag` — the sibling spec's resolved semantics, adopted verbatim). One shared helper `probeAndRecord(ctx)` used by both `Ready` and `run` keeps the two paths from drifting.

### REQ-4 — `cmd/server`: degraded (non-503) readiness payload

`cmd/server/http.go` (189 lines; +~35 → fine) + `cmd/server/audit_governance.go`:

- New optional interface next to `readinessChecker` (`http.go:30-31`):
  `type degradedChecker interface { Degraded() bool; BacklogAge() time.Duration }`.
- `readinessGroup` (`http.go:40-48`) gains `Degraded() bool` (OR over members implementing `degradedChecker`; non-implementers — e.g. `billing.Runtime` — contribute `false`) and `BacklogAge() time.Duration` (max over implementing members; `0` when none). `runtimeReadiness` (`audit_governance.go:51-64`) shape unchanged.
- `readyzHandler` (`http.go:65-69`): after `extra.Ready` succeeds, type-assert `extra.(degradedChecker)`; if implemented and `Degraded()` → **HTTP 200** with `Content-Type: application/json` and body
  `{"ok":true,"degraded":true,"backlog_age_seconds":<int64 seconds of BacklogAge()>}`.
- Healthy path stays **byte-identical** `{"ok":true}` (`http.go:71-73`) — `TestReadyzErrNotFoundIsReady` (`cmd/server/http_test.go:93-108`, body check `:103`) must stay green.
- `extra.Ready` error → 503 `runtime dependency unavailable` unchanged (`:66-68`); storage probe + `readyzProbeTimeout` unchanged.

The payload body and status are byte-locked to the sibling spec's REQ-3 so both specs' handler tests assert identical strings.

### REQ-5 — Alert rule: oldest-pending age > 450 s / degraded, mirroring the `event_outbox` rule

`deploy/prometheus/alerts.yml`, `aero-vault-integrity` group, immediately after `EventOutboxTerminalFailures` (`:105-112`):

```yaml
      - alert: AuditGovernanceBacklogDegraded
        expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Audit governance relay backlog degraded"
          description: "Audit-governance outbox oldest pending age exceeded the fixed 450s early-warning threshold (half of the AUDIT_GOVERNANCE_MAX_LAG_SECONDS default of 900s; the audit_governance_degraded arm is the config-true signal for any non-default maxLag) or the relay store probe degraded. /readyz remains 200 with a degraded marker — inspect the audit sink and OldestPendingAuditGovernance. Route alongside EventOutboxTerminalFailures (same severity, same receiver family)."
```

- **REQ-5.1 — Severity.** `warning`, matching the delivery-path analog `EventOutboxTerminalFailures` (`:105-112`): backlog lag is delivery-path degradation with a durable L0 fallback (the authoritative `audit_log` row), not data loss; `critical` is reserved repo-wide for data-loss/integrity (`HighServer5xxRate` `:21`, `ScrubFoundCorruptObjects` `:118`).
- **REQ-5.2 — 450 is a fixed literal.** alerts.yml has no templating and `maxLag` is runtime config; 450 = `MaxLagSeconds` default 900 × 0.5 (E10). The description must present 450 as a fixed constant (never "config-true"/"maxLag×0.5" phrasing); the `audit_governance_degraded == 1` arm is the config-true signal (fires iff age > maxLag or probe timeout, any config). The rule fires while readiness is **200** — age ∈ (450, 900] is alert-but-not-degraded (early warning), age > 900 or probe-timeout is alert-and-degraded.
- **REQ-5.3 — Deploy ordering.** The rule lands **with or before** the binary (rule-first is safe: absent series → no fire; binary-first without the rule leaves the degraded state with no alert path and no 503 eviction — the wedge this direction removes).

### REQ-6 — Tests (four files; two new)

**6.1 `internal/auditgovernance/runtime_ready_test.go`** (new — `runtime_test.go` is 410/500, hard gate; harness `runtimeConfig` `runtime_test.go:39-46`, maxLag 4 s, poll 10 ms; real SQLite via `repository.Open` + `Migrate`):

- `TestRuntimeReadyMaxLagDegradesAndDeadRowExclusion` (AC-2 + AC-1 maxLag half): *Phase A* — seed 2 facts via `RecordAuditWithGovernance`, land both terminal via public API (`ClaimAuditGovernance` + `FailAuditGovernance`, lease-fenced `audit_governance_claim.go:159-172`); assert `OldestPendingAuditGovernance` → `ok==false`, `Ready(ctx)==nil`, `Degraded()==false`, `BacklogAge()==0` (dead-row exclusion preserved). *Phase B* — seed one live fact, backdate `created_at_ns` to now−8 s via a second `database/sql` connection on the same `file:` DSN (WAL, `sqlite.go:31`; deterministic, no sleeps); assert `Ready(ctx)==nil` (no hard error), `Degraded()==true`, `BacklogAge()>4*time.Second`. *Phase C* — seed one fresh live fact; assert `Ready(ctx)==nil`, `Degraded()==false`, `BacklogAge()<4*time.Second`.
- `TestRuntimeReadyDrainStillHardFails` (drain semantics unchanged): binding `state='draining'` + pending fact → `Ready(ctx)` returns the drain error; after `CompleteAuditGovernance` → `Ready(ctx)==nil`; empty store → nil. Pins that only the maxLag flip changed (AC-1 drain side).
- `TestRuntimeReadyStoreTimeoutDegrades` (AC-1 read-path half): `hangingStore` embedding `repository.AuditGovernanceStore` — override `HasPendingDrainingAuditGovernance`/`OldestPendingAuditGovernance` to block on `<-ctx.Done()` then return `ctx.Err()`, gated by an `atomic.Bool` healthy flag; `New(runtimeConfig("http://127.0.0.1:1"), hangingStore, logger)`. Assert `Ready(context.Background())` returns **nil** with elapsed ∈ [1 s, 5 s] (blocking stub ⇒ response cannot precede the 2 s `storeProbeTimeout`; upper bound proves boundedness — `TestReadyzStorageProbeTimeout` idiom, `http_test.go:69-88`); `Degraded()==true`; `BacklogAge()==0`. Flip healthy → nil/false.

**6.2 `internal/auditgovernance/relay_metrics_test.go`** (new; AC-3 counter half):

- `TestRuntimeRelayCountersTrackDeliveryOutcomes` — install the global meter provider once per binary via the exported `telemetry.EnablePrometheus()` (`internal/telemetry/prometheus.go:30`; single call, TestMain idiom mirroring `internal/telemetry/main_test.go` — the auditgovernance test binary has its own global, no cross-package conflict); run a runtime against three per-tenant httptest sinks — one answering 202 (success), one 202+`conflict:true` (terminal), one 500 (transient) — with three bindings; poll until all three sinks saw ≥ 1 POST; scrape the returned handler; assert `audit_governance_relay_attempted_total >= 3`, `audit_governance_relay_delivered_total == 1`, `audit_governance_relay_dead_total == 1`, `audit_governance_relay_failed_total >= 1` (exact `==` on delivered/dead is safe — exactly one success and one terminal fact exist regardless of retries; `>=` on attempted/failed absorbs post-window retry rounds; no wall-clock equality).

**6.3 `cmd/server/http_test.go`** (extend; 129 → ~220 lines — AC-1 handler half, "extend readyzHandler test"):

- `TestReadyzDegradedExtraReturns200WithMarker` — fake extra implementing `degradedChecker` (`Ready→nil`, `Degraded→true`, `BacklogAge→123s`): status **200**, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`.
- `TestReadyzHealthyExtraReturns200Unchanged` — same fake with `Degraded→false`: status 200, body exactly `{"ok":true}` (healthy payload byte-identity guard).
- `TestReadyzAuditGovernanceMaxLagDrill` (AC-1, end-to-end): real `auditgovernance.Runtime` + SQLite store with one backdated live row (maxLag 4 s, backdate 8 s, `runtimeConfig` shape); `extra := runtimeReadiness(nil, runtime)`; `readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)` → status **200**, body contains `"degraded":true` (never 503); complete the row → status 200, body `{"ok":true}` (recovery restores).

**6.4 `internal/telemetry/metrics_test.go`** (extend; shared-handler scrape pattern, `metrics_test.go:1-24`):

- `TestAuditGovernanceMetrics_SurfaceInScrape` (AC-3 surface half): call the four `IncAuditGovernanceRelay*` helpers once each; register `RegisterAuditGovernanceGauges` with a fixed callback returning `(450, 1)` — **once** in the package (OTel rejects duplicate instrument registration; mirror the `TestObservableGauges_SurfaceInScrape` single-registration rule, `metrics_test.go:56-77`); scrape; assert body contains `audit_governance_relay_attempted_total 1`, `audit_governance_relay_delivered_total 1`, `audit_governance_relay_failed_total 1`, `audit_governance_relay_dead_total 1`, `audit_governance_backlog_age_seconds 450`, `audit_governance_degraded 1`.
- `TestAlertsYMLAuditGovernanceRelayRuleConsistency` (AC-4; CI artifact gate): YAML-parse `../../deploy/prometheus/alerts.yml`, assert rule `AuditGovernanceBacklogDegraded` exists with `expr` referencing exactly the two emitted names `audit_governance_degraded` and `audit_governance_backlog_age_seconds`, `severity: warning`, and description containing `450` but **not** `maxLag×0.5` (pins the fixed-literal phrasing; drift in either direction fails). If the sibling `cmd-server-ready-degraded` spec lands, its identically-asserting test and this one merge (same file, same rule — consistent by construction).

---

## 4. Decisions & non-goals

- **D1 — Counter semantics follow the direction's own site mapping.** `attempted` at `deliverFact` entry; `delivered` after the durable `CompleteAuditGovernance` (event_outbox placement precedent, `event_outbox_relay.go:328`); `failed` at `retryFact` entry (transient/rescheduled — analog of `retried_total`); `dead` at `failFact` entry (terminal-with-retention — analog of `failed_total`, contract dead-letter term). The acknowledgement-lost branch (`deliverFact:96-99`) increments nothing — claim-lost is not in the contract's four-name list.
- **D2 — Gauge/alert names are sibling-locked, not newly proposed.** The direction's "names proposed — contract gives semantics only" is resolved by the already-shipped sibling spec (`cmd-server-audit-governance-ready-degraded-v1.spec.md`): `audit_governance.backlog_age_seconds` + `audit_governance.degraded`, alert `AuditGovernanceBacklogDegraded`. This spec adds only the `audit_governance.relay_*` counter family. Any rename breaks the sibling's tests and its non-goal contract — forbidden.
- **D3 — `Degraded()`/`BacklogAge()` are cache getters, not live queries.** `/readyz` payload and `/metrics` scrape perform zero store I/O; the live probe happens only in `Ready()` and the `run()` loop, both bounded by `storeProbeTimeout = 2 s` (a package constant, not config — same rationale/value as `readyzProbeTimeout`, `http.go:34-38`). This makes AC-1 deterministic and scrapes hang-proof.
- **D4 — Degraded payload keeps `"ok":true` with HTTP 200** — LB/orchestrator keep the node; the marker (`degraded:true` + `backlog_age_seconds`) makes the state observable. Healthy body stays byte-identical (existing test assertion preserved). Drain-in-progress and genuine (non-context) store errors remain hard 503 — only the maxLag flip and probe timeouts change semantics.
- **D5 — Alert severity `warning`, fixed 450 literal, `for: 5m`** — mirrors `EventOutboxTerminalFailures` (`alerts.yml:105-112`); 450 = maxLag default 900 × 0.5, contract-specified (E10), described as a fixed constant; the `audit_governance_degraded` arm is the config-true signal.
- **Non-goals:** contract item 1 (permanent-error classification, `isPermanentDeliveryError`, migration/partial index — sibling `internal-access-audit-governance-permanent-error-classification-v1.spec.md`); contract item 3 (deterministic fact IDs); B3-6 (`Validate()`); billing-runtime readiness; events-outbox changes; claim-lost/acknowledgement-lost counters; any further alert rule beyond the one 450 s rule (the contract's "指标可喂 H2 告警" is satisfied by the metrics existing and one rule shipping; additional rules are not in this direction); Grafana panels and the deploy-atomicity/startup-warning machinery (owned by the sibling spec's REQ-5.3/5.4); any migration, `go.mod` change, or config-surface addition; `readyzProbeTimeout`/storage-probe branch; `/healthz`.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (D1 drill) —** *"with maxLag exceeded, /readyz returns 200 with a degraded signal (or a distinct non-503 status) and read paths time out/degrade instead of 503 (extend readyzHandler test)."*
*Testable:* `TestReadyzAuditGovernanceMaxLagDrill` + `TestReadyzDegradedExtraReturns200WithMarker` (REQ-6.3): real runtime + backdated row > maxLag 4 s → readyzHandler returns **200** with body containing `"degraded":true`, never 503; completion of the row → 200 `{"ok":true}`. Read-path half: `TestRuntimeReadyStoreTimeoutDegrades` (REQ-6.1) — store probes blocking on `ctx.Done()` → `Ready()` returns nil within [1 s, 5 s] (2 s `storeProbeTimeout`), `Degraded()==true`; recovery restores nil/false. "Never 503" is asserted by the 200 status on both the lag and the hung-store paths.

**AC-2 (dead-row exclusion) —** *"failed rows remain excluded from OldestPending (existing TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned assertion kept green)."*
*Testable:* the repository test (`internal/repository/audit_governance_test.go:334-372`, never-pending assertion `:357-358`) is **untouched and green**; `TestRuntimeReadyMaxLagDegradesAndDeadRowExclusion` Phase A (REQ-6.1) re-pins it at the runtime layer: dead-only store → `OldestPendingAuditGovernance ok==false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`. No repository change is required (E7).

**AC-3 (metrics surface) —** *"/metrics exposes audit_governance attempted/delivered/failed/dead counters incremented at deliverFact/failFact/retryFact and an oldest-pending-age gauge (names proposed — contract gives semantics only, no repo evidence)."*
*Testable:* `TestRuntimeRelayCountersTrackDeliveryOutcomes` (REQ-6.2) proves the increment sites: success/terminal/transient facts through a real runtime produce `relay_attempted_total >= 3`, `relay_delivered_total == 1`, `relay_dead_total == 1`, `relay_failed_total >= 1` in the scrape. `TestAuditGovernanceMetrics_SurfaceInScrape` (REQ-6.4) pins the names/values (`..._attempted_total 1` etc.) and the oldest-pending-age + degraded gauges (`audit_governance_backlog_age_seconds 450`, `audit_governance_degraded 1`). Names are sibling-locked (D2).

**AC-4 (alert rule) —** *"proposed: alert rule on oldest-pending age > 450s in deploy/prometheus/alerts.yml mirroring the event_outbox rule (450s constant is contract-specified, no in-repo evidence)."*
*Testable:* REQ-5 ships `AuditGovernanceBacklogDegraded` (`expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450`, `for: 5m`, `severity: warning`) immediately after `EventOutboxTerminalFailures` (`alerts.yml:105-112`); `TestAlertsYMLAuditGovernanceRelayRuleConsistency` (REQ-6.4) YAML-parses the repo artifact and pins rule name, exact two-name expr, severity, and fixed-literal description — the CI artifact gate (`go test ./...`). The 450 constant's provenance (maxLag default 900 × 0.5, contract `implementation-gate.md:22`) is documented in the rule description, not claimed config-true.

---

## 6. Risks

- **Metric-name drift between this spec and the sibling ready-degraded spec** — the gauge/alert names are shared; the REQ-6.4 consistency test pins the alert expr to exactly the two emitted names, so any rename fails `go test ./...` (drift guard in both directions). If both specs land, their identical assertions merge (noted in REQ-6.4).
- **Counter-value flake in the runtime wiring test** — a retried (500) fact may be re-claimed within the observe window, raising `attempted`/`failed` again. Mitigated by asserting exact `==` only on `delivered`/`dead` (exactly one success and one terminal fact exist — invariants regardless of retries) and `>=` on `attempted`/`failed`; no wall-clock equality; poll-until-seen-before-scrape.
- **Duplicate instrument registration** — OTel rejects re-registering the same instrument name. REQ-6.4's gauge registration is single-shot per package (shared-handler idiom, `metrics_test.go`); REQ-6.2's `telemetry.EnablePrometheus()` is single-shot per binary (TestMain), and it is safe because each Go test binary has its own global meter provider — no cross-package conflict with `internal/telemetry`'s TestMain.
- **Timing flake** — the proven idioms are used throughout: blocking stubs (response cannot precede the 2 s probe deadline ⇒ deterministic lower bound), SQL backdating instead of sleeps (8 s vs 4 s maxLag = 2× margin), `>=`/`>` assertions only, poll-with-deadline (3 s) for sink POST visibility.
- **Ready() behavior change with zero existing coverage** — the maxLag error string has no dependents (E4/E5); AC-1 tests replace the missing coverage, and `TestRuntimeReadyDrainStillHardFails` pins that drain semantics are untouched.
- **Hard gates** — file sizes on this checkout: `runtime.go` 209 + ~55 = OK; `relay.go` 191 + ~10 = OK; `metrics.go` 393 + ~45 = OK; `http.go` 189 + ~35 = OK; `http_test.go` 129 + ~90 = OK; `runtime_test.go` 410/500 ⇒ new `runtime_ready_test.go`/`relay_metrics_test.go` (mandatory, per sibling precedent). No new `go.mod` dependency (prometheus exporter and `database/sql` already in tree); `go test ./...` stays SQLite + local FS + httptest (zero network beyond loopback).
- **Silent wedge from deploy lag** — binary without the alerts.yml rule leaves the degraded state with no alert path and no 503 eviction. Gated by REQ-5.3 (rule lands with-or-before the binary) and the REQ-6.4 artifact test.

*Verification basis: all citations re-checked on this checkout (`acfaaf4`); line numbers reflect the working tree as read during this spec's production.*
