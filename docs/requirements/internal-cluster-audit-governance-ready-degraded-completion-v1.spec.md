# Requirements Specification — `internal/cluster` campaign B3-2/D1 completion: read-path probe timeouts degrade (never 503), with `Degraded()` cache + run-loop probe + degraded gauge/alert arms

**Module:** `internal/cluster` (B3 campaign) — surface spans `internal/auditgovernance`, `cmd/server`, `internal/telemetry`, `deploy/prometheus/alerts.yml`
**Direction:** "Complete B3-2/D1: read-path probe timeouts must degrade (never 503), with Degraded() cache + run-loop probe + degraded gauge/alert arms" (direction 1 of `docs/auto/analyses/internal-cluster-56f4b39c.json`)
**Contract:** `docs/campaigns/implementation-gate.md:22` (B3-2 Ready 解耦 H1 — maxLag flip removed → degraded + maxLag×0.5 (450 s) alert; terminal rows excluded from `OldestPending`; read-path timeouts degrade non-503; D1 drill); approved sibling specs `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` (REQ-1/REQ-2) and `docs/requirements/internal-access-audit-governance-relay-metrics-ready-degraded-v1.spec.md` (REQ-3/REQ-5.2/AC-4); amendment `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v3-f11-f16-amendment.design.md` (OR arm, `for: 10m`)
**Date:** 2026-08-08 · **HEAD:** `15763e2` + worktree (verification basis) · **Score:** value 9 / risk reduction 9 / effort 3 / confidence 9

---

## 1. Status statement (what exists vs. what this direction requires)

**This direction is already implemented and pinned in the current worktree.** The analysis (2026-08-07 23:42, mtime of `internal-cluster-56f4b39c.json`) was written against the committed tree at `15763e2`, which shipped only the *flip half* (commit message: "B3-2 Ready decoupling — backlog degrades instead of 503; backlog-age gauge + 450s alert" — verified via `git show --stat 15763e2`: the maxLag flip, `BacklogAge` accessor, backlog-age gauge, age-arm-only alert, 2 tests). The D1 read-path half — probe timeout degradation, `Degraded()` cache, run-loop probe feed, degraded marker payload, degraded gauge, OR alert arm, drill tests — was completed afterwards in the worktree (uncommitted `M` files + untracked `runtime_ready_test.go` / `readyz_drill_test.go` / `relay_metrics_test.go`). Every direction citation in §2 therefore describes the **pre-ship** state; this spec is the **regression contract**: the implement stage is expected to be zero-production-delta — verify the pins below exist and pass; the only net-new work is the one-line doc fix in REQ-6.

**Shipped inventory (verified this worktree):**

| # | Shipped item | Evidence (current worktree) |
|---|---|---|
| S1 | `Runtime.Ready`'s two store probes bounded by `storeProbeTimeout = 2s` (mirror of `readyzProbeTimeout`) | `internal/auditgovernance/runtime.go:26` (const), `probeAndRecord` `:251-253` wraps both `HasPendingDrainingAuditGovernance` and `OldestPendingAuditGovernance` |
| S2 | Probe timeout/cancel on **either** probe → degraded (age unknown → 0), `Ready()` returns **nil**, Warn log — never 503 | `runtime.go:255-259` (drain probe), `:268-272` (backlog probe): `isProbeCtxError` (`errors.Is` DeadlineExceeded/Canceled, `:231`) → `recordDegraded(true, 0)` + nil; `Ready` `:293-294` |
| S3 | Genuine (non-context) store errors stay fail-closed; drain-in-progress stays hard 503 | `runtime.go:260-262` (`"audit governance drain lookup failed"`), `:263-265` (`"audit governance binding drain is in progress"`), `:273` (`"audit governance backlog lookup failed"`) |
| S4 | maxLag flip → degraded, not error (B3-2 half shipped at `15763e2`, preserved) | `runtime.go:283-288` (`ok && age > r.maxLag` → Warn + `recordDegraded(true, age)` + nil); healthy `:289` |
| S5 | `Degraded()`/`BacklogAge()` cache with single-lock (degraded, age) pair discipline; zero-I/O getters | `runtime.go:213-219` (`Degraded`), `:222-226` (`BacklogAge`), `recordDegraded` `:239-244` (one Lock write, valid pairs only), `PendingBacklogAge` (store-querying accessor) `:198-206` |
| S6 | Run-loop probe after `cleanupDelivered` once per poll cycle feeds the cache; probe errors never stop the loop | `runtime.go:297-324` (`run`), probe at `:322` (`r.probeAndRecord(context.Background())`, after `cleanupDelivered` `:315`) |
| S7 | `/readyz` seam: `extra.Ready(probeCtx)` bounded by the same 2s `readyzProbeTimeout` as `repo.Ping` and `store.Stat`; degraded extra → **200** with marker body | `cmd/server/http.go:90-127` (`readyzHandler`): ping bound `:96-99`, probeCtx `:102-103`, storage probe `:104`, `extra.Ready(probeCtx)` `:109`, degraded branch `:113-121` (`{"ok":true,"degraded":true,"backlog_age_seconds":N}`), healthy `{"ok":true}` byte-identical `:125-127`; `degradedChecker` `:39`; group aggregation `readinessGroup.Ready` `:56-63` / `Degraded` `:67-73` / `BacklogAge` `:78-84` |
| S8 | Gauge read path cache-fed: **zero store I/O per scrape** — strictly stronger than a probe bound | `cmd/server/build.go:101-108` (`auditGovernanceBacklogAgeGaugeFn` → `rt.BacklogAge()` only), `:110-118` (`auditGovernanceDegradedGaugeFn` → `rt.Degraded()` only); registered `:153-155`; instruments `internal/telemetry/metrics.go:368` (`audit_governance.backlog_age_seconds`), `:382` (`audit_governance.degraded`), drain pair `:400` |
| S9 | Wedge alert-visible while readiness stays 200: `degraded == 1` OR arm + fixed-450 age arm | `deploy/prometheus/alerts.yml:186-195` (`AuditGovernanceBacklogDegraded`; expr `:187` `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`; `for: 10m` `:188`; `severity: warning` `:190`; description "/readyz stays 200" `:193`); v3 amendment A3/A4 (`internal-api-rest-...-v3-f11-f16-amendment.design.md:17`, `:121`) |
| S10 | Read-path timeout classification separate from delivery-path classifier | read: `runtime.go:231` `isProbeCtxError`; delivery: `internal/auditgovernance/relay.go:255` `isPermanentDeliveryError` (used at `:87`) — `DeadlineExceeded` remains **transient** on delivery (pinned `relay_terminal_test.go:225`) |

**Baseline (run this session):** `go build ./...` clean · `go vet ./internal/auditgovernance/ ./cmd/server/` clean · `gofmt -l` empty · `go test ./internal/auditgovernance/ -run 'TestRuntimeReady|TestRuntimeBacklogAge|TestRuntimeRunLoop'` ok (10.2s) · `go test ./cmd/server/ -run 'TestReadyz|TestAlertsYML|TestNoExecutable|TestAuditGovernance'` ok (8.8s) · `go test ./internal/telemetry/` ok. Production files under the 500-line gate (`runtime.go` 353, `http.go` 242, `build.go` 220, `metrics.go` 489; `*_test.go` excluded per `Makefile:164,174-175`).

---

## 2. Evidence verification (direction citations vs. this worktree)

Every citation in the direction's problem statement, evidence string, and acceptance was checked against the repository **as it exists now** (HEAD `15763e2` + worktree). Citations describe the analysis-time (pre-ship) state unless marked.

| # | Direction citation | Verified location (current worktree) | Verdict |
|---|---|---|---|
| E1 | `internal/auditgovernance/runtime.go:155-182` — "fail-closed Ready; comment 'Store errors remain fail-closed readiness failures'; no Degraded(), no run-loop probe" | `Ready` is now a one-line delegation `:293-294` into `probeAndRecord` `:251-290`; `Degraded()` `:213-219`; `BacklogAge()` `:222-226`; run-loop probe `:320-323`; `storeProbeTimeout` `:26` | ❌ **stale — shipped** (S1–S6). All four claimed gaps exist in the worktree. |
| E2 | `cmd/server/http.go:34-38,59-66` — "readyzProbeTimeout wraps only store.Stat; extra.Ready(req.Context()) unbounded; always writes {\"ok\":true} :70-73" | `readyzHandler` `:90-127`; `extra.Ready(probeCtx)` `:109` — the 2s budget now covers the whole extra readiness group; degraded marker branch `:113-121`; healthy `{"ok":true}` `:125-127` | ❌ **stale — shipped** (S7). The unbounded-ctx and always-`ok:true` gaps are closed. |
| E3 | `cmd/server/audit_governance.go:51-64` — "readinessGroup maps any error to 503" | `runtimeReadiness` shape unchanged `:51-64`; `readinessGroup` aggregation (with `Degraded`/`BacklogAge`) now lives in `http.go:44-84`; the 503-on-`Ready`-error mapping is unchanged (`http.go:110-112`) and remains correct for drain/genuine errors (S3) | ✅ **holds** (fail-closed half preserved; group moved file). |
| E4 | `internal/telemetry/metrics.go:354-361` — "registers only backlog_age; no audit_governance.degraded gauge" | Both gauges registered: backlog age `:364-372`, degraded `:377-386`, plus drain pair `:390+` | ❌ **stale — shipped** (S8). |
| E5 | `deploy/prometheus/alerts.yml:163` — "expr only 'audit_governance_backlog_age_seconds > 450'; no degraded arm" | Rule `:186-195`; expr `:187` now `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` (v3 amendment A3); second rule `AuditGovernanceEnabledUnbound` `:202+` | ❌ **stale — shipped** (S9). |
| E6 | `runtime_test.go:412-467` — "pins only the maxLag flip, no timeout pin exists" | Flip pins preserved at `runtime_test.go:616` (`TestRuntimeReadyDegradesOnBacklogLag`) and `:676` (`TestRuntimeBacklogAgeZeroWhenNoPending`); timeout/degraded pins added in the new `internal/auditgovernance/runtime_ready_test.go:176` (`TestRuntimeReadyDegradedSentinel`), `:206` (fail-closed, incl. pre-canceled ctx), `:254` (all-terminal), `:348` (run-loop feed), `:397` (wedge survival), `:416` (concurrent pair discipline) | ⚠️ **stale location, substance shipped** — the "no timeout pin" gap is closed; the flip pins are kept. |
| E7 | `git 15763e2` — "half-shipped flip only" | `git show --stat 15763e2` + commit message: flip + `BacklogAge` + backlog-age gauge + 450s age-arm alert + 2 tests. No `Degraded()`, no probe-timeout branch, no degraded gauge/marker, no run-loop probe, no drill tests in the commit | ✅ **accurate history.** The D1 half is worktree-completed (S1–S10). |
| E8 | Acceptance: "no store call on /metrics scrape — build.go:113 gauge reads cache getter only" | The gauge callbacks are `cmd/server/build.go:101-108`/`:110-118` (the direction's bare "build.go" = `cmd/server/build.go`); both call only the zero-I/O cache getters; registration `:153-155` | ✅ **holds** (S8) — and stronger than cited: the per-scrape store query was removed entirely. |
| E9 | Sibling relay-metrics spec REQ-5.2 — description "never 'maxLag×0.5' phrasing"; AC-4 — "expr referencing exactly the two emitted names" | Description `alerts.yml:193` states the fixed 450 early-warning plus the config-true arm ("lag > configured maxLag"); the exact forbidden substring `maxLag×0.5` (no spaces) is **absent** ("maxLag default 900 × 0.5" with spaces and "default" — a fixed constant of the fixed default, not a config-derived claim); parity test pins both names + OR + severity + `for` (E10) | ✅ **compliant** — both names appear, exactly two `audit_governance_*` exprs in the file (parity test asserts 2). |
| E10 | Acceptance: "AC-4 YAML-pin test" | Realized as `TestAlertsYMLAuditGovernanceExprParity` (`cmd/server/readyz_drill_test.go:381-443`): block-scoped pin of rule marker, expr with the **config-derived** threshold `MaxLagSeconds/2` (450 at the shipped default — drift-proof against a maxLag-default change), `OR audit_governance_degraded == 1`, `for: 10m`, `severity: warning`, `/readyz stays 200`, exactly-2 `expr: audit_governance_` occurrences; plus `TestNoExecutable450LiteralOutsideAlertsYml` (`:540-576`). String pin, not YAML-parse — stdlib-first decision (I6; `go.mod` has no direct YAML dependency, only indirect `go.yaml.in/yaml/v2`), documented in the test comment "no YAML dependency promotion" | ✅ **holds in substance** — stronger than a YAML-parse in the threshold derivation; see D5. |
| E11 | Residual drift found during verification (not cited by the direction) | `docs/configuration.md:275` still documents `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` as "Oldest undelivered outbox age that `/readyz` permits." — **false** under the shipped semantics (any age is permitted; age > maxLag degrades, `/readyz` stays 200). Required fix by the approved cmd-server spec REQ-6, never landed | ⚠️ **the only remaining delta** → REQ-6. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Ready() hard-fails only on drain-in-progress and genuine non-timeout store errors" | ✅ **now holds** (`runtime.go:251-290`: timeout/cancel → nil+degraded; only drain/genuine errors return errors) |
| "passes the raw unbounded request ctx to the store" | ❌ **stale — shipped**: `extra.Ready(probeCtx)` (`http.go:109`) with the same 2s budget as the other probes; inside, probes run under `storeProbeTimeout` (`runtime.go:252-253`) |
| "always writes {\"ok\":true}" | ❌ **stale — shipped**: degraded branch writes the marker body (`http.go:113-121`) |
| "no Degraded() method, no run-loop probe after cleanupDelivered, no audit_governance.degraded gauge" | ❌ **stale — shipped**: S5, S6, S8 |
| "a wedged store still 503s the whole node via /readyz" | ❌ **stale — shipped**: a wedged store now returns 200 + marker within the 2s probe budget (AC-1 pins); genuine store errors and drain still 503 (AC-4 pins) |

---

## 3. Requirements (contract + pin; all satisfied by the shipped worktree)

Each REQ states the behavior contract this direction requires and names the pin that makes it testable. The implement stage's job is to verify the pins exist and pass — no production delta is expected beyond REQ-6.

### REQ-1 — Read path is time-bounded (2s per probe, 2s at the seam); probe timeout/cancel degrades, never 503

`Runtime.Ready`'s two store probes run under `storeProbeTimeout = 2 * time.Second` (`runtime.go:26`, `:251-253`), and `readyzHandler` calls `extra.Ready(probeCtx)` with the same 2s budget used for `repo.Ping` and the storage probe (`http.go:96-109`). `errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)` on **either** probe → Warn log, `recordDegraded(true, 0)` (age unknown), `Ready` returns **nil** (`runtime.go:255-259`, `:268-272`, classifier `:231`) — the wedged-store shape degrades the audit-governance readiness contribution; it never 503s the node.

- *Pin (boundedness, deterministic lower bound):* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176-204`) — hanging stub returns only after the ctx deadline; `Ready(context.Background())` nil with elapsed ∈ [1s, 5s], `Degraded()==true`, `BacklogAge()==0`. `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:444-466`) — same bound through the real handler: status 200, body contains `"degraded":true`, elapsed ∈ [1s, 5s], ageGauge=0 ∧ degradedGauge=1.
- *Pin (Canceled fork):* subtest `c3-pre-canceled-ctx` of `TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:206-252`) — pre-canceled ctx returns immediately (< 1s), nil + degraded.
- *Pin (fail-closed half):* `TestReadyzExtraProbeTimeout` (`readyz_drill_test.go:161-178`) — a generic extra that returns `ctx.Err()` after the deadline yields 503 within [1s, 5s] (a checker without degrade semantics stays fail-closed).

### REQ-2 — `Degraded()`/`BacklogAge()` cache + run-loop probe feed

`Runtime` keeps a mutex-protected cache (`runtime.go:64-67` fields; `recordDegraded` `:239-244` writes BOTH fields under one Lock so only valid (degraded, age) pairs are observable). `Degraded()` `:213-219` and `BacklogAge()` `:222-226` are zero-I/O getters. The cache is refreshed by `probeAndRecord` from two feeds: every `Ready()` call and the `run()` loop once per poll cycle **after `cleanupDelivered`** (`:315`, probe `:322`), so freshness ≤ one poll interval (default 1s — `config_audit_governance.go:61`) independent of `/readyz` traffic; probe errors never stop the loop.

- *Pins:* `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:348-395`) — with zero `Ready()` calls the loop flips the cache degraded on a 16s-backdated backlog (SQL backdate, no sleeps) and healthy after a test-controlled age restore, within one poll cycle; the flip is proven to be the healthy-probe path (backlog still pending). `TestRuntimeRunLoopSurvivesWedgedStore` (`:397-414`) — the loop cycles through wedged probes and recovers. `TestRuntimeDegradedCacheConcurrentAccess` (`:416-472`) — concurrent writers/readers observe only valid pairs (-race enforced via `make test-race-meta`).

### REQ-3 — Seam: degraded (non-503) readiness payload; healthy body byte-identical; drain/genuine errors 503

`readyzHandler` (`http.go:90-127`): after `extra.Ready` succeeds, type-assert `extra.(degradedChecker)` (`:117`); if implemented and `Degraded()` → HTTP **200** with `Content-Type: application/json` and body `{"ok":true,"degraded":true,"backlog_age_seconds":<int64 seconds of BacklogAge()>}` (`:113-121`). Healthy path stays byte-identical `{"ok":true}` (`:125-127`). `extra.Ready` error → 503 `runtime dependency unavailable` unchanged (`:110-112`).

- *Pins:* `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:212-256`) — backdated live row (8s > maxLag 4s) → 200 with exact marker body; `TestReadyzDrainStill503` (`:258-289`) — draining binding + pending fact → 503 `runtime dependency unavailable` (boundary control: without it a bug skipping `extra` would pass the degraded tests vacuously); `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`:288-380`) — empty store and fully dead-lettered backlog → 200 `{"ok":true}`, gauges 0; phase 2 (live row, 2s backdate) proves the cache-fed gauge reports real ages after one priming probe.

### REQ-4 — Telemetry: both gauges cache-fed, scrape-safe

`internal/telemetry/metrics.go` registers `audit_governance.backlog_age_seconds` (`:368`) and `audit_governance.degraded` (`:382`) on the `aero-vault/domain` meter. Callbacks (`cmd/server/build.go:101-108`, `:110-118`) read **only** the REQ-2 cache getters — a `/metrics` scrape performs zero store I/O and can never block on a hung store (the store read that fills the cache is bounded by `storeProbeTimeout`, REQ-1). Wiring `cmd/server/build.go:153-155` registers both when `auditRuntime != nil` (unconditional w.r.t. `PROMETHEUS_ENABLED` — lazy binding, mirroring `registerGauges(repo)`).

- *Pins:* `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` (`internal/telemetry/metrics_test.go:171-190`) and `TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`:192-213`) — both series surface in the scrape with the callback values; `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:444`) asserts the wedge is `/metrics`-visible (`ageGauge=0 ∧ degradedGauge=1`).

### REQ-5 — Alert rule: both arms, warning, 10m; pin derives the threshold from config

`deploy/prometheus/alerts.yml:186-195`, rule `AuditGovernanceBacklogDegraded`:

```yaml
      - alert: AuditGovernanceBacklogDegraded
        expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Audit governance relay backlog degraded"
          description: "Oldest pending audit fact exceeded the 450s early warning (maxLag default 900 × 0.5), or the relay store probe degraded (degraded=1: lag > configured maxLag, or store probe timeout — age unknown). /readyz stays 200 (degraded); check relay_attempted/failed counters and the sink."
```

- **Both arms:** the age arm fires at 450 s = `MaxLagSeconds` default 900 × 0.5 (early warning, alert-but-not-degraded for age ∈ (450, 900]); the `degraded == 1` arm is the config-true signal (fires iff age > configured maxLag **or** probe timeout, any config) — and the only signal that accumulates through a probe-timeout wedge (timeout records age 0, so the age arm alone would reset `for` on every timeout sample; the OR keeps accumulation true until a genuinely healthy probe — v3 amendment §4 proof).
- **Rule fires while readiness is 200** — both arms; the description names it (`/readyz stays 200`).
- **`for: 10m` wins over the sibling specs' 5m** — v3 amendment §6.1: shipped artifact wins; evaluation cadence 15s (`deploy/prometheus/prometheus.yml:21-22`) ⇒ 10m = 40 evaluations.
- **Pin (AC-4; CI artifact gate):** `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381-443`) — derives `wantExpr` from `config.Load()`'s `MaxLagSeconds/2` (450 at the shipped default), requires the `OR audit_governance_degraded == 1` arm, `for: 10m`, `severity: warning`, `/readyz stays 200`, and exactly 2 `expr: audit_governance_` occurrences in the file (guards rule/metric drift in both directions, incl. the B3-4 relay-counter collision). `TestNoExecutable450LiteralOutsideAlertsYml` (`:540-576`) — the only executable 450 threshold literal in the Go tree is the alerts.yml expr. Runs in `go test ./...` (`.github/workflows/ci.yml:84-86` is the only artifact gate — no promtool, F3-6 of the cmd-server spec).

### REQ-6 — Config doc sync (the only remaining delta)

`docs/configuration.md:275` — `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` currently reads "Oldest undelivered outbox age that `/readyz` permits." — **false** under the shipped semantics: `/readyz` permits any age; ages above `maxLag` degrade (200 with `degraded:true`), alert at 450 s = half the **default**. Reword to: oldest-pending age above which the audit-governance readiness contribution is **degraded** (readyz stays 200 with a degraded marker; the alert age arm fires at 450 s, half the default). `.env.example:198` (value `900`) unchanged. This is the unlanded REQ-6 of the approved cmd-server spec; it is included because it is the only verified drift of the shipped contract. No validation/config-surface change.

---

## 4. Decisions & non-goals

- **D1 — `storeProbeTimeout` is a package constant, not config.** 2 s, mirroring `readyzProbeTimeout` (`http.go:52`; the `:45-51` comment cross-references the seam); not derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (5s default — too slow for a readiness probe) nor `STORAGE_READ_TIMEOUT`; no new env knob, `.env.example`, validation, or docs surface.
- **D2 — `Degraded()`/`BacklogAge()` are cache getters, not live queries.** `/readyz` payload and `/metrics` scrape perform zero store I/O; the live probe happens only inside `Ready()` and the `run()` loop (both bounded). This is what makes the D1 drill deterministic and scrapes hang-proof.
- **D3 — Degraded payload keeps `"ok":true` with HTTP 200** — LB/orchestrator keep the node; the marker makes the state observable. Healthy body stays byte-identical (existing pins preserved). Drain-in-progress and genuine (non-context) store errors remain hard 503 — only the maxLag flip and probe timeouts change semantics.
- **D4 — Alert expr operand order differs from the direction's literal.** The direction's acceptance string is `audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450`; the shipped expr is `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`. PromQL `or` is commutative — the acceptance is a *semantic* statement (both arms present, OR connective); the v3 amendment (A3/A4) pinned the shipped form, and the parity test requires both arms, so a regression dropping either fails CI. The direction's literal string is superseded by the amendment.
- **D5 — The "AC-4 YAML-pin" is realized as a stdlib string/parity pin, not a YAML parse** (`readyz_drill_test.go:381` comment: "os.ReadFile + strings + strconv, I6 — no YAML dependency promotion"; `go.mod` has no direct YAML dependency). It is stronger than a YAML-parse in one dimension — the 450 threshold is **derived** from `config.Load()` (`MaxLagSeconds/2`), so a maxLag-default change re-derives the pin instead of silently drifting — and pins severity/`for`/rule-name/description marker in block scope.
- **D6 — Cache pair semantics:** (true, 0) timeout/unknown · (true, age>maxLag) lag · (false, age≤maxLag) healthy · (false, 0) no pending. Degraded ⇒ age==0 or age>maxLag — pinned by `TestRuntimeDegradedCacheConcurrentAccess` and the drill tests.
- **Non-goals:** direction 2 (reconcile-path multi-replica coordination / singleton gating) and direction 3 (singleton lease-semantics pins) of the same `internal-cluster-56f4b39c.json` analysis — separate specs; B3-1 (permanent-error classification), B3-3 (fact ID determinism), B3-4 (relay `relay_*` counters — must reuse the two REQ-4 gauge names, no second oldest-age gauge), B3-6 (`Validate()`); billing-runtime readiness; events-outbox changes; drain semantics; `readyzProbeTimeout`/storage-probe branch; `/healthz`; Grafana panels (deferred by v3 amendment §6.3); any migration, `go.mod` change, or config-surface addition.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (D1 drill)** — *"store whose OldestPendingAuditGovernance returns context.DeadlineExceeded → Ready()==nil and Degraded()==true, /readyz 200 with degraded marker."*
*Testable:* `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:176`) — hanging store: `Ready()==nil`, `Degraded()==true`, `BacklogAge()==0`, elapsed ∈ [1s, 5s]; `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:444`) — same wedge through `runtimeReadiness(nil, rt)` + `readyzHandler`: status **200** (never 503), body contains `"degraded":true`, elapsed ∈ [1s, 5s], wedge `/metrics`-visible (`degradedGauge==1`, `ageGauge==0`). Recovery: the same runtime returns `{"ok":true}` on health (flip-flag subtests and `TestReadyzBacklogLagDegradesNot503`'s healthy control). "Never 503" is asserted by the 200 status on the hung path; the drain control (`TestReadyzDrainStill503`) proves 503 is still reachable for the fail-closed branches.

**AC-2 (run-loop probe + scrape-safety)** — *"run() loop probe after cleanupDelivered feeds cache (no store call on /metrics scrape — build.go:113 gauge reads cache getter only)."*
*Testable:* `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` (`runtime_ready_test.go:348`) — zero `Ready()` calls, cache flips degraded and healthy via the loop within one poll cycle; `TestRuntimeRunLoopSurvivesWedgedStore` (`:397`) — loop survives wedged probes; `cmd/server/build.go:101-118` callbacks call only `rt.BacklogAge()`/`rt.Degraded()` (zero store I/O — verified by inspection; the store read that fills the cache is `probeAndRecord` under `storeProbeTimeout`); `TestReadyzDeadLetteredBacklog200AndGaugeZero` phase 2 (`readyz_drill_test.go:288`) — the gauge callback reports the real backdated age (≥ 2s), i.e. the scrape reads the cache, not a silent constant zero.

**AC-3 (alert arms)** — *"alerts.yml AuditGovernanceBacklogDegraded rule contains 'audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450'."*
*Testable:* rule at `alerts.yml:186-195`; `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381`) — CI artifact gate asserting the rule exists with the config-derived age arm, the `OR audit_governance_degraded == 1` arm (D4: operand order per the v3 amendment), `for: 10m`, `severity: warning`, `/readyz stays 200`, and exactly two `audit_governance_*` exprs; `TestNoExecutable450LiteralOutsideAlertsYml` (`:540`) — no executable 450 outside the rule; `TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`metrics_test.go:192`) — the degraded series exists to feed the arm. Combined with AC-1 (readiness 200 while degraded), the alert fires **while** readiness stays 200 — the degraded path is observable end-to-end, never a silent 503.

**AC-4 (T-3 preserved)** — *"all-terminal backlog → BacklogAge ok=false, Degraded()==false, gauge 0 (existing pin runtime_test.go:470-496 kept)."*
*Testable:* `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:254`) — Claim+Fail via the public lease-fenced API: `OldestPendingAuditGovernance ok==false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`; `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:288`) — same at the seam (200 `{"ok":true}`, both gauges 0, empty-store phase 0 included); the pre-existing pins are kept — `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676`) and `TestRuntimeReadyDegradesOnBacklogLag` (`:616`; the maxLag flip still degrades, never hard-errors).

---

## 6. Risks

- **Stale citations → wasted implementation.** Every production-code citation in the direction describes the pre-ship tree; an implement stage that "fixes" them would rewrite shipped, pinned code. Mitigation: this spec is a regression contract — §1 inventory + §3 pins; implement delta is REQ-6 only.
- **Parity pin is a string pin, not a YAML parse.** Accepted trade (D5): stdlib-first (I6), config-derived threshold, block-scoped, both arms, exactly-2-expr guard. A structural alerts.yml change (rule moved out of block scope) could bypass the block check only by also moving the marker — the count check (`expr: audit_governance_`) still fires.
- **Operand-order / `for:` divergence from sibling specs.** The direction's literal expr string and the sibling specs' `for: 5m` are superseded by the v3 amendment (D4, REQ-5); the parity test pins the shipped form. Any future re-alignment must amend the parity test in the same commit.
- **Description phrasing.** `alerts.yml:193` contains "maxLag default 900 × 0.5" — semantically the fixed-constant derivation (default is fixed) and the config-true arm is separately stated ("lag > configured maxLag"); the sibling REQ-5.2's exact forbidden substring `maxLag×0.5` is absent. If a reviewer requires the stricter phrasing, the description edit is one line and does not affect the pins (`/readyz stays 200` is the only description pin).
- **Timing flake.** Mitigated by the proven idioms: blocking stubs (response cannot precede the deadline ⇒ deterministic lower bound), SQL backdating instead of sleeps (WAL second-writer, `readyz_drill_test.go` idiom), `>`/range assertions only (no wall-clock equality); 8s backdate vs 4s maxLag gives 2× margin; run-loop tests use ≥ 2× poll-interval slack.
- **Gauge registration is single-shot** (OTel rejects duplicate instruments) — the telemetry tests follow the existing single-registration discipline of `metrics_test.go` (shared `EnablePrometheus` handler per binary).
- **`make check`** — implementation must pass gofmt/build/vet/test (SQLite + local FS, zero network beyond `httptest`); verified green on this checkout (§1 baseline). Line numbers above reflect the worktree as read during this spec's production.

*Verification basis: all citations re-checked on this checkout (HEAD `15763e2` + worktree); the analysis `docs/auto/analyses/internal-cluster-56f4b39c.json` (mtime 2026-08-07 23:42) predates the worktree implementation of the D1 half.*
