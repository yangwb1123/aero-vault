# Requirements Specification — `cmd/server`: decouple `Ready()` — degraded state + 450s alert/`BacklogAge`; store-probe timeouts degrade instead of 503 (D1)

**Module:** `cmd/server`
**Direction:** "B3-2: decouple Ready() — replace maxLag hard-flip with degraded state + 450s alert/BacklogAge; store-probe timeouts degrade instead of 503 (D1)"
**Source analysis:** `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 2)
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 8 / effort 4 / confidence 8

---

## 1. Scope

`Runtime.Ready()` (`internal/auditgovernance/runtime.go:145-161`) returns hard errors in two situations: a binding **drain in progress** (`:150-152`) and **backlog older than `maxLag`** (`:157-159`, default 900 s — `internal/config/config_audit_governance.go:66`). `readyzHandler` (`cmd/server/http.go:51-74`) maps **any** `extra.Ready` error to HTTP **503** `runtime dependency unavailable` (`:65-69`) via `runtimeReadiness`/`readinessGroup` (`cmd/server/audit_governance.go:51-64`, `cmd/server/http.go:40-48`). A wedged audit sink (or a permanently-rejected fact, pre-B3-1) therefore grows the pending backlog until `maxLag` trips and the **whole node** is marked not-ready for LB/orchestrator — with **no degraded tier, no 450 s alert, and no `BacklogAge` surface** (`grep -rn "BacklogAge\|Degraded" internal/auditgovernance/ cmd/server/` → empty).

The D1 amplification is worse than the direction states and one citation is imprecise: `Ready()`'s two store probes (`HasPendingDrainingAuditGovernance` `:146`, `OldestPendingAuditGovernance` `:153`) run on the **caller's raw request context** (`extra.Ready(req.Context())`, `http.go:66`) — only the *storage* probe has the 2 s bound (`readyzProbeTimeout`, `http.go:34-38`; `store.Stat(probeCtx, ...)` `:59-61`). `r.httpTimeout` is used only in `New` (binding apply) and `Close` (drain wait), **not** in `Ready`. A hung store query therefore blocks `/readyz` past the probe budget and, when it eventually errors, yields 503. The terminal-row exclusion from lag is already correct (`OldestPendingAuditGovernance` `failed_at_ns=0` predicate, `internal/repository/audit_governance_claim.go:188-201`), so the remaining work is exactly the flip semantics + alert + internal probe timeout.

This spec scopes exactly one change: **decouple audit-governance backlog condition from node readiness** — `Ready()` hard-fails only on drain-in-progress and genuine (non-timeout) store errors; backlog lag and store-probe timeouts become a **degraded** state surfaced as `200` with a degraded marker, a `BacklogAge` surface, two gauges, and one 450 s alert rule. Out of scope (see §4): B3-1 (permanent-error classifier), B3-3 (fact ID determinism), B3-4 (relay counter family), B3-6 (`Validate()`), billing runtime readiness, drain semantics, and any migration/config-surface change.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `runtime.go:Ready` — "drain and maxLag errors; no degraded state, no BacklogAge, no alert; store calls use r.httpTimeout ctx" | `Ready` `runtime.go:145-161`: `HasPendingDrainingAuditGovernance(ctx)` `:146` → hard error `:147-149`; draining → hard error `:150-152`; `OldestPendingAuditGovernance(ctx)` `:153` → hard error `:154-156`; `ok && time.Since(oldest) > r.maxLag` → hard error `:157-159`; else nil `:160` | ✅ **errors confirmed.** ⚠️ **`r.httpTimeout` nuance incorrect:** `Ready` passes the caller `ctx` straight to the store; `r.httpTimeout` appears only in `New` (apply-desired-bindings) and `Close` (`:121-130`, drain wait `:126`). The unbounded caller ctx is precisely the D1 gap this spec closes (REQ-1). |
| E2 | `http.go:readyzHandler + readinessGroup` — "extra.Ready error → 503" | `readyzHandler` `:51-74` (drift: cited `:39-58`); `extra.Ready(req.Context())` `:66` → `http.Error(w, "runtime dependency unavailable", 503)` `:67-68`; `readinessGroup` `:40-48` (sequential `Ready`, first error wins); `readyzProbeTimeout = 2s` `:34-38` wraps **only** `store.Stat` `:59-61`; healthy body `{"ok":true}` `:71-73`; route `r.Get("/readyz", readyzHandler(repo, store, extraReady))` `:101` | ✅ **exact.** The `extra.Ready` call has no deadline of its own. |
| E3 | `audit_governance.go:runtimeReadiness/buildAuditGovernanceRuntime` + "AUDIT_GOVERNANCE_ENABLED gate at config_audit_governance.go:55" | `runtimeReadiness` `:51-64` (billing + audit runtimes into `readinessGroup`, nil when both absent); `buildAuditGovernanceRuntime` `:15-49`; `Enabled: getEnvBool("AUDIT_GOVERNANCE_ENABLED", false)` at `config_audit_governance.go:55`; wiring `cmd/server/main.go:70` (build), `:157` (`runtimeReadiness(billingRuntime, auditRuntime)` into `buildRouter`); second construction site `main.go:200` is `runMCP()` — no readiness/gauge wiring | ✅ **exact.** |
| E4 | `audit_governance_claim.go:OldestPendingAuditGovernance` — "failed_at_ns=0 exclusion already satisfies T-3 lag side" | `OldestPendingAuditGovernance` `:188-201`: `SELECT MIN(o.created_at_ns) ... WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`; `HasPendingDrainingAuditGovernance` `:203-210`: `... b.state='draining'` | ✅ **exact.** Dead rows are excluded from both lag and drain-pending. |
| E5 | `deploy/prometheus/alerts.yml` — "only EventOutboxTerminalFailures exists; no audit-governance lag/degraded rule; 450s threshold and metric name are proposed" | `EventOutboxTerminalFailures` at `:105-112` (integrity group `:92+`); `grep -n "audit" alerts.yml` → hits only the `:104` comment and `:112` description; **no** audit-governance rule | ✅ **holds.** 450 = `maxLag` default 900 × 0.5 (`config_audit_governance.go:66`). |
| E6 | "proposed: BacklogAge() method, degraded (non-503) readiness payload, maxLag×0.5=450s alert, store-query internal timeout → degrade" — `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 | `audit-contract-batch-aero-vault.md:9`: "删 `Ready()` 翻转、新增 `BacklogAge`、run() 循环 maxLag×0.5（450s）降级告警、store 查询内部超时降级非 503" | ✅ **present.** Metric names are not fixed by the proposal; this spec names them (REQ-4) — B3-4 must not re-define an oldest-age gauge under a second name (see §4 non-goals). |
| E7 | `config_audit_governance.go` default maxLag 900 | `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` at `:66`; validation `MaxLagSeconds > ClaimTTLSeconds` `:241`, `<= 604_800` `:251`; `docs/configuration.md:274` documents "Oldest undelivered outbox age that `/readyz` permits" (semantics change → REQ-6); `.env.example:197` | ✅ **exact.** |
| E8 | "draining behavior is pinned by TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference" | `runtime_test.go:197-233`: pins the **startup** rejection path (`applyDesiredBindings` unbound-backlog error, `runtime.go:202`) — it does **not** exercise `Ready()`'s drain branch. `grep -rn "\.Ready(" internal/auditgovernance/ cmd/server/` → only `http.go:66`; **no test anywhere asserts `Ready()`'s maxLag or drain errors** | ⚠️ **partially accurate.** The cited test pins the drain-removal startup gate, not `Ready()`'s drain branch; REQ-7.2 adds a dedicated `Ready`-drain pin (AC-4). The maxLag error string has zero dependents, so removing the flip is unconstrained by existing tests. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Ready() returns hard errors for drain-in-progress and backlog>maxLag (default 900s)" | ✅ **holds** (E1, E7). |
| "readyzHandler maps ANY extra.Ready error to HTTP 503 via runtimeReadiness" | ✅ **holds** — `http.go:66-68`; no error classification, no degraded tier (E2, E3). |
| "no degraded tier, no 450s alert, and no BacklogAge surface" | ✅ **holds** — `grep "BacklogAge\|Degraded"` across `internal/auditgovernance/`, `cmd/server/`, `internal/telemetry/` → empty; alerts.yml has no audit-governance rule (E5). |
| "Terminal-row exclusion from lag is already correct" | ✅ **holds** (E4) — T-3's lag side needs no repository work. |
| "A slow store query marks the whole node not-ready" | ✅ **holds, worse than stated** — `Ready()`'s probes run on the unbounded request ctx (`http.go:66`), so a hung store holds `/readyz` past the 2 s storage-probe budget and still yields 503 on eventual error (E1 nuance, E2). |
| "store calls use r.httpTimeout ctx" | ❌ **inaccurate as cited** — corrected in E1; the internal probe bound is part of the D1 work, not existing behavior. |

**F3 hardening evidence (alert-swap audit, added with the design hardening):**

| # | Claim | Verified location | Verdict |
|---|---|---|---|
| F3-1 | No Alertmanager routing config ships in-repo | `grep -rn "alertmanager" deploy/ .github/ Makefile` → absent; `docs/analysis-v8-gaps-roadmap.md:440` lists it as a helm-chart gap | ✅ routing is operator-owned → REQ-5.1 |
| F3-2 | `warning` is the convention for delivery-path failure | `EventOutboxTerminalFailures` `alerts.yml:105-112` (warning `:109`); critical reserved for data-loss/integrity (`HighServer5xxRate` `:21`, `ScrubFoundCorruptObjects` `:118`) | ✅ → REQ-5.1 |
| F3-3 | `MaxLagSeconds` valid range (ClaimTTL, 604800] | `config_audit_governance.go:241` (`> ClaimTTLSeconds`) / `:251` (`<= 604_800`) | ✅ → REQ-5.2 |
| F3-4 | No shipped Grafana panel keys on readyz 503s | both `deploy/grafana/*.json`: zero `readyz`/`probe`/`up` refs; only aggregate 5xx (`status=~"5.."` `aero-vault-dashboard.json:201/:699`; `status="500"` `aero-vault-ai-ops-dashboard.json:84`) | ✅ → REQ-5.4 / REQ-7.3 |
| F3-5 | Helm readinessProbe is the only deploy consumer of the readyz 503 | `deploy/helm/aero-vault/templates/deployment.yaml:83-88` (`readinessProbe: httpGet {path: /readyz}`, period 10 s) | ✅ → REQ-5.4 |
| F3-6 | No promtool gate exists; `go test ./...` is the only artifact check | `.github/workflows/ci.yml:84-86`; no promtool in CI or Makefile | ✅ → REQ-5.3 |

---

## 3. Requirements

### REQ-1 — `Ready()` semantics: maxLag flip and probe timeouts become degraded, never 503

In `Runtime.Ready` (`internal/auditgovernance/runtime.go:145-161`):

- **Bound both store probes** with a new package-level constant `storeProbeTimeout = 2 * time.Second` in `runtime.go` (comment must cross-reference `readyzProbeTimeout`, `cmd/server/http.go:38` — same rationale, same value, independent symbol): `probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout); defer cancel()`; pass `probeCtx` to `HasPendingDrainingAuditGovernance` and `OldestPendingAuditGovernance`.
- **Probe ctx errors** (`errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)`) on **either** store call → record degraded (age unknown → 0), **return nil** (never 503).
- **Drain in progress** (`draining == true`) → unchanged hard error `"audit governance binding drain is in progress"` (`:150-152`). **Drain lookup error** (non-context) → unchanged hard error `"audit governance drain lookup failed"`. **Backlog lookup error** (non-context) → unchanged hard error `"audit governance backlog lookup failed"`.
- **Delete the maxLag hard flip** (`:157-159`, `if ok && time.Since(oldest) > r.maxLag { return errors.New(...) }`) — replaced by: `ok && time.Since(oldest) > r.maxLag` → record degraded with age `time.Since(oldest)`, return nil.
- **Healthy path** (`!ok` or age ≤ maxLag): record not-degraded with age (`0` when `!ok`), return nil.

Resulting contract: `Ready()` errors only for drain-in-progress and genuine store failures; every condition that used to 503 the node is now `nil` + degraded state. The error strings are unconstrained by existing tests (E8) and flow only into server logs (`http.Error` drops them, `http.go:67-68`); AC-4 pins the *behavior* (drain → 503 via the handler), not the strings.

### REQ-2 — `Degraded()` / `BacklogAge()` surface + run-loop feed

`Runtime` (`internal/auditgovernance/runtime.go`) gains a mutex-protected cache updated by `Ready()` **and** by the `run()` loop (the "run() 循环 maxLag×0.5 降级告警" input from the proposal):

- Fields: `mu sync.Mutex; degraded bool; backlogAge time.Duration` (one small helper `probeAndRecord(ctx context.Context)` shared by `Ready` and `run`; `run` calls it once per poll cycle after `cleanupDelivered()`, with `context.WithTimeout(context.Background(), storeProbeTimeout)`).
- `func (r *Runtime) Degraded() bool` — cache getter, **no store I/O**. `true` iff the most recent probe timed out, or pending backlog exists with age > `maxLag`.
- `func (r *Runtime) BacklogAge() time.Duration` — cache getter, **no store I/O**; `0` when no pending (dead-row exclusion comes from the repository predicate, E4) or when the probe timed out (age unknown).
- Cache semantics documented: reflects the most recent probe (≤ one poll interval stale, default 1 s — `PollMilliseconds` 1000, `config_audit_governance.go:61`).

### REQ-3 — cmd/server: degraded (non-503) readiness payload

In `cmd/server/http.go` (+ aggregation in `cmd/server/audit_governance.go`):

- New optional interface next to `readinessChecker` (`http.go:30-31`):
  `type degradedChecker interface { Degraded() bool; BacklogAge() time.Duration }`.
- `readinessGroup` (`http.go:40-48`) gains `Degraded() bool` (OR over members that implement `degradedChecker`; non-implementers — e.g. `billing.Runtime` — contribute `false`) and `BacklogAge() time.Duration` (max over implementing members; `0` when none). `runtimeReadiness` (`audit_governance.go:51-64`) shape unchanged.
- `readyzHandler` (`http.go:65-69`): after `extra.Ready` succeeds, type-assert `extra.(degradedChecker)`; if implemented and `Degraded()` → **HTTP 200** with `Content-Type: application/json` and body
  `{"ok":true,"degraded":true,"backlog_age_seconds":<int64 seconds of BacklogAge()>}`.
- Healthy path stays **byte-identical** `{"ok":true}` (`http.go:71-73`) — the existing `TestReadyzErrNotFoundIsReady` body assertion (`http_test.go:93-108`, body check `:103`) must stay green.
- `extra.Ready` error → 503 `runtime dependency unavailable` unchanged (`:66-68`). The storage probe (`:59-61`) and `readyzProbeTimeout` unchanged.

### REQ-4 — Telemetry: two gauges, cache-fed, scrape-safe

- `internal/telemetry/metrics.go` (following the `RegisterQueueDepthGauge` observable-gauge pattern, `:326-334`): add
  `RegisterAuditGovernanceGauges(fn func(context.Context) (ageSeconds, degraded int64))` registering two `Int64ObservableGauge`s on the `aero-vault/domain` meter:
  - `audit_governance.backlog_age_seconds` → exported as `audit_governance_backlog_age_seconds` (OTel dots→underscores; gauges get no `_total`, per the file header comment),
  - `audit_governance.degraded` → `audit_governance_degraded` (0/1).
- Callbacks read **only** the cache getters from REQ-2 — a `/metrics` scrape must never block on the store (a hung store degrades, it must not hang the scrape).
- Wiring: `cmd/server/main.go` `run()` — when `auditRuntime != nil`, register once alongside `registerGauges(repo)` (`:154`), callback = `(int64(auditRuntime.BacklogAge().Seconds()), bool→int64(auditRuntime.Degraded()))`. Registration is unconditional w.r.t. `PROMETHEUS_ENABLED` (lazy binding to the installed provider, mirroring `registerGauges`); `runMCP()` (`main.go:171`) does **not** register (no gauges today, E3).

### REQ-5 — Alert rule: 450 s / degraded, observable while readiness stays 200 — plus alert-path hardening (F3)

`deploy/prometheus/alerts.yml`, `aero-vault-integrity` group (after `EventOutboxTerminalFailures`, `:105-112`):

```yaml
      - alert: AuditGovernanceBacklogDegraded
        expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Audit governance backlog degraded"
          description: "Audit-governance outbox oldest pending age exceeded the 450s fixed early-warning threshold (calibrated to the AUDIT_GOVERNANCE_MAX_LAG_SECONDS default of 900s; the audit_governance_degraded arm is the config-true signal for any non-default maxLag) or the relay store probe degraded. /readyz remains 200 with a degraded marker — inspect the audit sink and OldestPendingAuditGovernance. Route alongside EventOutboxTerminalFailures (same severity, same receiver family)."
```

- **REQ-5.1 — Severity/paging contract (resolves the warning-vs-paging assumption).** `severity: warning` is a requirement, not an assumption: the repo ships no Alertmanager routing config (F3-1) — routing is operator-owned — so the rule carries its routing intent in-band (the description names the sibling rule and receiver family). `warning` matches the delivery-path analog `EventOutboxTerminalFailures` (`:105-112`, also warning): backlog lag is delivery-path degradation with a durable L0 fallback, not data loss; `critical` is reserved repo-wide for data-loss/integrity (`HighServer5xxRate` `:21`, `ScrubFoundCorruptObjects` `:118`). Deploy contract: Alertmanager must route this rule to the **same receivers as `EventOutboxTerminalFailures`**; verification = the REQ-5.3 startup warning names rule + severity, plus the release note.
- **REQ-5.2 — 450-literal vs non-default maxLag (drift guard).** 450 is a fixed constant valid only as `maxLag` default 900 × 0.5; alerts.yml has no templating and valid `MaxLagSeconds` ∈ (ClaimTTL, 604800] (F3-3). The description must **not** claim config-true derivation (no "maxLag×0.5" phrasing — pinned by REQ-7.3). The `audit_governance_degraded == 1` arm is the config-true signal (fires iff age > maxLag or probe timeout, any config). `buildAuditGovernanceRuntime` (`cmd/server/audit_governance.go:15-49`) logs a startup **warning** when `MaxLagSeconds != 900` (age arm calibrated for the default).
- **REQ-5.3 — Deploy atomicity gate.** Binary-without-rule is a fully silent wedge (readyz 200, no series, no rule, old 503 eviction gone). Three enforced layers: (1) **CI artifact gate** — the REQ-7.3 test YAML-parses `../../deploy/prometheus/alerts.yml` (the repo artifact; `go test ./...` in `.github/workflows/ci.yml:84-86` is the only gate — no promtool, F3-6); (2) **fleet gate** — `buildAuditGovernanceRuntime` logs a startup warning naming the rule when the runtime is enabled ("deployed Prometheus must contain alert AuditGovernanceBacklogDegraded; without it the degraded state has no alert path"); (3) **ordering** — alerts.yml lands **with or before** the binary, never after (rule-first is safe: absent series → no fire since `absent()` is not in the expr; binary-first is the wedge).
- **REQ-5.4 — Single-path signal fan-in (documented property).** Old consumers: helm `readinessProbe` on `/readyz` (`deployment.yaml:83-88`) → LB eviction — **intentionally removed** (the decoupling goal; probe unchanged, still 503s on drain/genuine errors); dashboards — **audited: no shipped panel keys on readyz 503s** (F3-4; only aggregate 5xx panels lose a driver). New signal: exactly one machine paging path (Prometheus → Alertmanager); the `/readyz` degraded payload is inspection-only. Accepted single point of failure; the dashboard panel test (REQ-7.3) restores a second, non-paging consumer in the shipped AI & Ops dashboard.
- The rule fires while readiness is **200** (both arms): age ∈ (450, 900] is alert-but-not-degraded (early warning before the flip); age > 900 or probe-timeout is alert-and-degraded. 450 = `maxLag` default 900 × 0.5 (E5/E7); a literal in the rule (alerts.yml has no templating).

### REQ-6 — Config doc semantics

`docs/configuration.md:274` — `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` description must stop saying "that `/readyz` permits" and state the new contract: oldest-pending age above which the audit-governance readiness contribution is **degraded** (readyz stays 200 with `degraded:true`; alert age arm at 450 s = half the **default**). `.env.example:197` value unchanged.

### REQ-7 — Tests (five files; new file `internal/auditgovernance/runtime_ready_test.go` — `runtime_test.go` is 400/500 lines, hard gate)

**7.1 `internal/auditgovernance/runtime_ready_test.go`** (harness: `runtimeConfig`, `runtime_test.go:39-46` — maxLag 4 s, poll 10 ms; real SQLite via `repository.Open`+`Migrate`):

- `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` (AC-2) — three phases on one store:
  - *Phase A (dead rows only):* seed 2 facts via `RecordAuditWithGovernance`; land both terminal via the public API `ClaimAuditGovernance(ctx,"t","tok",1,10,time.Minute)` + `FailAuditGovernance(ctx,id,"t","tok","dead")` (lease-fenced, `audit_governance_claim.go:159-172`). Assert: `OldestPendingAuditGovernance` returns `ok==false`; `Ready(ctx)==nil`; `Degraded()==false`; `BacklogAge()==0` (lag exclusion).
  - *Phase B (live row older than maxLag):* seed one live fact; backdate deterministically — open a second `database/sql` connection to the same `file:` DSN (WAL mode, `sqlite.go:31`, allows a second writer; the repo's `MaxOpenConns(1)` serializes its own writes) and `UPDATE audit_governance_outbox SET created_at_ns = <now-8s> WHERE id=?`. Assert: `Ready(ctx)==nil` (no hard error), `Degraded()==true`, `BacklogAge() > 4*time.Second`.
  - *Phase C (young live row):* seed another live fact (fresh `created_at_ns`). Assert: `Ready(ctx)==nil`, `Degraded()==false`, `BacklogAge() < 4*time.Second`.
- `TestRuntimeReadyDrainHardFails` (AC-4): binding `state='draining'` (`ApplyAuditGovernanceBindings`) + pending fact → `Ready(ctx)` returns error mentioning `"drain is in progress"`; after `CompleteAuditGovernance` of the row → `Ready(ctx)==nil`. Empty store → `Ready(ctx)==nil`.
- `TestRuntimeReadyStoreTimeoutDegrades` (AC-1, unit half): `hangingStore` — embed `repository.AuditGovernanceStore`, override `ApplyAuditGovernanceBindings` (→ nil), `HasPendingDrainingAuditGovernance` and `OldestPendingAuditGovernance` (block on `<-ctx.Done()` then return `ctx.Err()`), gated by an `atomic.Bool` healthy flag. `New(runtimeConfig("http://127.0.0.1:1"), hangingStore, logger)` (loopback http passes `secureEndpoint`, `http.go:60-72`; no network at construction, `newPublisher` `http.go:30-59`). Assert: `Ready(context.Background())` returns **nil** within elapsed ∈ [1 s, 5 s] (blocking stub ⇒ response cannot precede the 2 s `storeProbeTimeout`; upper bound proves boundedness — mirror `TestReadyzStorageProbeTimeout` idiom, `http_test.go:69-88`); `Degraded()==true`; `BacklogAge()==0`. Flip flag healthy → `Ready(ctx)==nil`, `Degraded()==false`.

**7.2 `cmd/server/http_test.go`** (+ `cmd/server/audit_governance_test.go`):

- `TestBuildAuditGovernanceRuntimeAlertWarnings` (REQ-5.2/5.3, `audit_governance_test.go` — 45 → ~60 lines): valid enabled `config.AuditGovernanceConfig` with `MaxLagSeconds: 3600` → captured slog buffer contains the REQ-5.2 warning; `MaxLagSeconds: 900` → absent; enabled runtime always logs the REQ-5.3 rule-presence warning.

- `TestReadyzDegradedExtraReturns200WithMarker` (pure handler): fake extra `{Ready→nil, Degraded→true, BacklogAge→123s}` → status **200**, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`.
- `TestReadyzHealthyExtraReturns200Unchanged`: same fake with `Degraded→false` → status 200, body exactly `{"ok":true}` (healthy payload byte-identity guard).
- `TestReadyzAuditGovernanceDegradedDrill` (AC-1, end-to-end): build `auditgovernance.New` with the hanging store from 7.1 (same embedded-interface fake idiom, `http_test.go:27-56`); `extra := runtimeReadiness(nil, runtime)`; `readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)`. Assert: status **200** (never 503), body contains `"degraded":true`, elapsed ∈ [1 s, 5 s]; set fake healthy → status 200, body `{"ok":true}` (recovery restores `ok:true`).

**7.3 `internal/telemetry/metrics_test.go`** (single-registration rule — the file's shared `EnablePrometheus` handler, `prometheus_test.go:1-24`; register the gauge pair **once** across the package, e.g. TestMain or one test, since OTel rejects duplicate instrument registration):

- Register `fn` returning `(450, 1)` → scrape body contains `audit_governance_backlog_age_seconds 450` and `audit_governance_degraded 1`; re-scrape after `fn` returns `(0, 0)` → `audit_governance_degraded 0`.
- `TestAlertsYMLAuditGovernanceRuleConsistency` (grep-consistency, mirroring B3-4's contract requirement): read `../../deploy/prometheus/alerts.yml` (package-relative from `internal/telemetry`), assert rule `AuditGovernanceBacklogDegraded` exists with `expr` referencing exactly the two emitted names `audit_governance_degraded` and `audit_governance_backlog_age_seconds`, and no other `audit_governance_*` name (guards rule/metric drift in both directions). **Hardened (REQ-5.1/5.2):** YAML-parse (not line-grep); assert `severity: warning`; assert the description does **not** contain `maxLag×0.5` (pins the fixed-constant phrasing — drift in either direction fails). This test is the **CI artifact gate** for the alert swap (REQ-5.3) — it runs in `go test ./...`.
- `TestGrafanaAuditGovernancePanel` (REQ-5.4): read `../../deploy/grafana/aero-vault-ai-ops-dashboard.json`, assert a panel whose target exprs reference exactly `audit_governance_degraded` and `audit_governance_backlog_age_seconds` (second consumer of the degraded signal; survives dashboard edits).

---

## 4. Decisions & non-goals

- **D1 — Internal probe timeout is a package constant, not config.** `storeProbeTimeout = 2 s` in `internal/auditgovernance`, mirroring the `readyzProbeTimeout` D1 decision of `cmd-server-readyz-probe-timeout-v1.spec.md`. Not derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (relay HTTP bound, default 5 s — too slow for a readiness probe) nor from `STORAGE_READ_TIMEOUT`; no new env knob, no `.env.example`/validation/docs surface.
- **D2 — `Degraded()`/`BacklogAge()` are cache getters, not live queries.** `/readyz` payload and `/metrics` scrape perform zero store I/O; freshness ≤ one poll interval (default 1 s). The live probe happens inside `Ready()` and the `run()` loop only. This is what makes the D1 drill ("never 503, recovery restores ok:true") deterministic and scrapes hang-proof.
- **D3 — Degraded payload keeps `"ok":true` with HTTP 200** — LB/orchestrator keep the node; the marker (`degraded:true` + `backlog_age_seconds`) makes the state observable. Healthy body stays byte-identical (existing test assertion preserved).
- **D4 — Alert threshold is a literal 450 s = maxLag × 0.5 at default 900 s**, derived in the rule description; alerts.yml has no templating and `maxLag` is runtime config, so no expression can reference it. **Hardened (F3):** the description presents 450 as a fixed constant (never "config-true"), the `audit_governance_degraded` arm is the config-true signal, and non-default `maxLag` triggers a startup warning (REQ-5.2). Severity `warning` is a routing contract consistent with `EventOutboxTerminalFailures` (REQ-5.1); deploy atomicity is gated by the CI artifact test + startup warning + with-or-before ordering (REQ-5.3); the single-path fan-in is a documented property with a dashboard consumer restored by REQ-5.4.
- **D5 — Gauge registration is unconditional when the runtime exists** (lazy binding to the installed provider), mirroring `registerGauges(repo)` at `main.go:154`; `runMCP()` does not register.
- **Non-goals:** B3-1 (permanent-error classifier — direction 1 of the same analysis), B3-3 (fact ID determinism), B3-6 (`Validate()` empty bindings); **B3-4 (relay counter family) must reuse the two gauges named in REQ-4** — it may add `relay_*` counters and its own rules but must not re-define an oldest-age gauge under another name; billing-runtime readiness (no degraded tier); drain semantics (pinned by AC-4); `readyzProbeTimeout`/storage-probe branch; `/healthz`; alert severity/`for` beyond the proposed rule; any migration, `go.mod` change, or config-surface addition.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (D1 drill) —** *"store fake whose OldestPending/HasPendingDraining call hangs past probe timeout → /readyz returns 200 (degraded marker), never 503; recovery restores ok:true."*
*Testable:* `TestReadyzAuditGovernanceDegradedDrill` (REQ-7.2) + `TestRuntimeReadyStoreTimeoutDegrades` (REQ-7.1): the fake blocks on `ctx.Done()` for both store probes; with `storeProbeTimeout = 2 s` the handler must return **200** with body containing `"degraded":true` and elapsed ∈ [1 s, 5 s] (the blocking stub makes the lower bound deterministic — the response cannot precede the deadline — and the upper bound is the boundedness claim). Flip the fake to healthy → 200 `{"ok":true}`. "Never 503" is asserted by the 200 status on the hung path.

**AC-2 (T-3 lag exclusion) —** *"seed outbox with only dead (failed_at_ns>0) rows → BacklogAge/Ready reports zero pending (lag exclusion), while a live row older than maxLag flips to degraded."*
*Testable:* `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` (REQ-7.1), phases A/B/C: dead rows only → `OldestPendingAuditGovernance ok==false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`; + one live row backdated 8 s (> maxLag 4 s) → `Ready()==nil` (no hard error), `Degraded()==true`, `BacklogAge()>4s`; + one fresh live row → `Degraded()==false`. Backdating via `UPDATE audit_governance_outbox SET created_at_ns` on a second WAL connection is deterministic (no sleeps).

**AC-3 (D1 alert observability) —** *"alert rule (oldest-age > 450s or degraded state) fires on metrics exposition while readiness remains 200 — asserts the degraded path is observable, not just silent."*
*Testable:* REQ-7.3 — the two gauges surface in the `/metrics` scrape (`audit_governance_backlog_age_seconds 450`, `audit_governance_degraded 1`), and `TestAlertsYMLAuditGovernanceRuleConsistency` pins rule `AuditGovernanceBacklogDegraded` (`expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450`, `for: 5m`, `severity: warning`, description without `maxLag×0.5`) to exactly the emitted names — YAML-parsed, as the CI artifact gate (REQ-5.3). Combined with AC-1/AC-2 (readiness 200 while degraded), the alert fires **while** readiness stays 200 — the degraded path is observable end-to-end, never a silent 503. The dashboard panel (`TestGrafanaAuditGovernancePanel`) restores a second consumer (REQ-5.4); the startup-warning test pins the fleet gate (REQ-5.2/5.3).

**AC-4 (T-3 drain pin) —** *"drain-in-progress still hard-fails readiness (only maxLag flip changes semantics; draining behavior is pinned by TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference)."*
*Testable:* the cited test pins the **startup** drain gate (E8); `TestRuntimeReadyDrainHardFails` (REQ-7.1) adds the missing `Ready()`-branch pin: draining binding + pending fact → `Ready(ctx)` returns the drain error → through `readyzHandler` (REQ-3, unchanged `:66-68`) that is HTTP **503**; after the row completes → `Ready()==nil` → 200. Only the maxLag flip changes semantics; drain is hard-fail before and after.

---

## 6. Risks

- **503 for genuine store failure vs. degraded-timeout confusion** — a wedged store produces `context.DeadlineExceeded` from the internal probe (degraded), while SQL errors remain hard 503. `errors.Is` on the probe ctx is the only classifier; a store that returns a non-context error *after* the deadline would be misclassified as hard-fail — mitigated by probing only through `probeCtx` and the AC-1 fake returning `ctx.Err()` (the realistic wedged-store shape).
- **`Degraded()` cache staleness** — between probes (≤ poll interval, default 1 s) the payload reflects the last probe; a drain started in that window is caught by `Ready()`'s live drain check before the degraded check, so the hard-fail path is never stale. Documented in REQ-2.
- **Timing flake** — mitigated by the proven idioms: blocking stubs (response cannot precede the deadline ⇒ deterministic lower bound), backdating via SQL instead of sleeps, counter/`>` assertions only (no wall-clock equality). AC-2's 8 s backdate vs. 4 s maxLag gives 2× margin.
- **Hard gates** — `runtime.go` 209 lines + ~45 = OK; `runtime_test.go` at 400/500 ⇒ new `runtime_ready_test.go` (mandatory); `http_test.go` 129 + ~60 = OK; `audit_governance.go` 65 + ~25 = OK; `metrics.go` 393 + ~20 = OK; `alerts.yml` is YAML (no Go gate). Gauge registration is single-shot (OTel duplicate-instrument error) — the REQ-7.3 single-registration rule mirrors `prometheus_test.go`'s TestMain pattern.
- **B3-4 metric-name collision** — B3-2 owns `audit_governance_backlog_age_seconds` / `audit_governance_degraded`; B3-4 must add `relay_*` counters without re-defining an oldest-age gauge (non-goal note; the grep-consistency test in REQ-7.3 will flag any second `audit_governance_*` name in the alert expr).
- **Silent wedge from deploy lag (F3.3)** — binary deployed without the alerts.yml rule leaves the degraded state with no alert path and no 503 eviction. Gated by REQ-5.3: CI artifact test (YAML parse), boot-time warning naming the rule, and with-or-before ordering. Residual risk: operator-side Alertmanager routing drops the rule (REQ-5.1 contract; no in-repo routing config exists — F3-1).
- **Non-default `maxLag` alert drift (F3.2)** — the 450-literal age arm is calibrated to the default; at `maxLag=3600` it fires 8× early, at `maxLag<450` it is redundant. Gated by REQ-5.2: fixed-constant description (pinned by test), config-true degraded arm, startup warning on non-default.
- **`make check`** — implementation must pass gofmt/build/vet/test (SQLite + local FS, zero network beyond `httptest`); all line numbers above re-confirmed on this checkout (`acfaaf4`).

*Verification basis: all citations re-checked on this checkout; line numbers reflect the working tree as read during this spec's production.*
