# Requirements Specification — `internal/integration`: D1 drill at server level (/readyz stays 200 degraded with sink down + 450s gauge/alert path, read-path-timeout semantics pinned)

**Module:** `internal/integration` (server-level drill tests) + enabling seam `internal/readiness` (new)
**Direction:** "D1 drill at server level: /readyz stays 200 (degraded) with sink down + 450s gauge/alert path, and pin the read-path-timeout semantics" — direction 2 of `docs/auto/analyses/internal-integration-7479f0a2.json`
**Contract:** `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 / D1; `docs/campaigns/implementation-gate.md:22` ("D1 drill：sink 停 60min → /readyz 200 + 450s 告警；无重启循环")
**Sibling shipped specs:** `docs/requirements/internal-auditgovernance-d1-read-path-timeout-degrade-v1.spec.md` (read-path half: `runtime.go` + seam), `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` (REQ-1..4, AC-1)
**Date:** 2026-08-08 · **HEAD:** `15763e2` + uncommitted B3-campaign worktree (verification basis) · **Score:** value 8 / risk reduction 9 / effort 6 / confidence 8

---

## 1. Status statement (what exists vs. what this direction requires)

The D1 **read-path half is already shipped in the worktree** (unit + `cmd/server` seam level; see the sibling spec's shipped inventory S1–S9). What this direction requires — a **server-level drill in `internal/integration`** — does not exist:

| Gap | Evidence (verified this tree) |
|---|---|
| No production-shaped HTTP-server test wires the audit-governance checker into `/readyz` anywhere | `cmd/server/governance_e2e_test.go:182-228` (`newGovernanceE2E`) builds FileService + EventBus + `WrapRepository` + relay but **no HTTP server at all** and never calls `Ready`/`readyz`; `internal/integration/fullserver_test.go:136-142` mounts a `repo.Ping`-only `/readyz` stub that cannot exercise the audit checker |
| The seam that must be drilled is unexported in `package main` — unreachable from `internal/integration` | `readyzHandler` `cmd/server/http.go:90`, `readinessChecker`/`degradedChecker` `:28-41`, `readyzProbeTimeout` `:52`, `readinessGroup` `:54`, `runtimeReadiness` `cmd/server/audit_governance.go:73-87`, gauge callbacks `cmd/server/build.go:101-118` — all package-private; `internal/integration` cannot import `package main` |
| The analysis-time discrepancy "`extra.Ready` runs on the raw request context with no deadline" is **stale** | `readyzHandler` now calls `extra.Ready(probeCtx)` with the same 2s `readyzProbeTimeout` that bounds `repo.Ping` and `store.Stat` (`http.go:96-109`); the runtime additionally self-bounds with `storeProbeTimeout` (`internal/auditgovernance/runtime.go:22-26`, `:251-252`) |
| The analysis-time discrepancy "contract 读路径超时降级非 503 vs. implemented 503" is **resolved in the code** — the decision was made and shipped | Probe **timeout/cancel** → `isProbeCtxError` (`runtime.go:231-236`) → `recordDegraded(true, 0)` → `Ready` returns **nil** → `/readyz` **200** with the degraded marker (`http.go:113-121`). Genuine (non-context) store errors remain fail-closed **503** (`runtime.go:260-262`, `:273`; comment `:280-281` "Store errors remain fail-closed readiness failures"). Both halves pinned at unit + seam level (`runtime_ready_test.go:176/206`, `readyz_drill_test.go:161/444/258`). This direction is **verify-only** for the semantics; the new server-level wedge drill pins the timeout branch through the production assembly |

**Already shipped (cross-referenced, not re-implemented here):** unit degraded-on-maxLag pin `TestRuntimeReadyDegradesOnBacklogLag` (`internal/auditgovernance/runtime_test.go:616-670` — the direction's "runtime_test.go:415 pattern", relocated); unit timeout/fail-closed pins `TestRuntimeReadyDegradedSentinel`/`TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:176/206`); seam drill `TestReadyzBacklogLagDegradesNot503`, `TestReadyzDeadLetteredBacklog200AndGaugeZero`, `TestReadyzAuditGovernanceDegradedDrill`, `TestReadyzDrainStill503`, `TestReadyzExtraProbeTimeout` (`cmd/server/readyz_drill_test.go:212/288/444/258/161`); alert-expr parity `TestAlertsYMLAuditGovernanceExprParity` (`:381`) — threshold **derived** from `config.Load()`'s `BacklogAlertThresholdSeconds()` (`internal/config/config_audit_governance.go:48-50`) = 900/2 = **450** (`alerts.yml:187` expr, `for: 10m`, "/readyz stays 200" description); scrape-surface pins `internal/telemetry/metrics_test.go:171/192`; 450-literal scan `TestNoExecutable450LiteralOutsideAlertsYml` (`readyz_drill_test.go:540` — **scans `internal/`**, so the new test must never contain an executable `450`).

**Baseline (this tree):** `go build ./...` clean · `go vet` clean · `go test ./internal/auditgovernance/ ./internal/telemetry/ ./cmd/server/ -run 'TestReadyz|TestAlertsYML|TestAuditGovernance'` green (verified while producing this spec). The 500-line production gate excludes `*_test.go` (`Makefile:174-178`); the new test file is exempt but kept ~≤600 lines.

---

## 2. Evidence verification (direction citations vs. this tree)

| # | Direction citation (analysis-time) | Verified location (current tree) | Verdict |
|---|---|---|---|
| E1 | `cmd/server/governance_e2e_test.go:newGovernanceE2E,startRelay` — "harness never calls Ready/readyz" | `newGovernanceE2E` `:182-228`, `putObject` `:229-238`, `startRelay` `:239-243` — harness has no HTTP server and no readiness wiring | ✅ **accurate, still true** — this is the server-level gap |
| E2 | `cmd/server/audit_governance.go:runtimeReadiness (49-65)`; `cmd/server/http.go:readyzHandler,readyzProbeTimeout` | `runtimeReadiness` `:73-87` (drift); `readyzHandler` `http.go:90`, `readyzProbeTimeout` `:52`; `extra.Ready(probeCtx)` `:109` (drift: the analysis-time "raw request context" claim is stale — see §1); degraded branch `:113-121`, healthy body `:125-127` | ⚠️ **partially stale** — symbols exist; the unbounded-probe claim no longer holds |
| E3 | `internal/auditgovernance/runtime.go:Ready (163-181), BacklogAge (146-161)` | `Ready` `:293-294` → `probeAndRecord` `:251-290`; `BacklogAge` cache getter `:222-226` (the store-querying accessor is `PendingBacklogAge` `:198-206`); `isProbeCtxError` `:231-236`; timeout→degrade branches `:255-259`, `:268-272`; maxLag branch `:283-288`; fail-closed branches `:260-262`, `:273` | ⚠️ **line drift, substance resolved** — the "no timeout branch" claim is obsolete (shipped, sibling spec S2–S5) |
| E4 | `cmd/server/build.go:113-121`; `deploy/prometheus/alerts.yml:158-169` | gauge fns `build.go:101-118` (cache-fed, zero store I/O), registered `:153-155`; rule `alerts.yml:186-195`, expr `:187` `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` | ⚠️ **line drift** — content holds (the 450 literal is the shipped default 900 × 0.5, parity-pinned at `readyz_drill_test.go:381`) |
| E5 | `internal/auditgovernance/runtime_test.go:415 (unit-level degraded pin, sqlite)` | relocated to `TestRuntimeReadyDegradesOnBacklogLag` `runtime_test.go:616-670` (seed pending fact, wait past maxLag 4s, `Ready` nil + degraded, drain still hard-fails `:662-670`) | ⚠️ **relocated, behavior identical** |

**Problem-statement checks (direction claims vs. this tree):**

| Statement | Verdict |
|---|---|
| "D1 acceptance has no server-level test anywhere" | ✅ **still true** — `governance_e2e` has no HTTP server; `fullserver_test.go:136` is a Ping-only stub |
| "fullserver_test.go:136 stub cannot exercise the audit checker" | ✅ **true** — the stub never touches `Runtime.Ready`/`Degraded`/`BacklogAge` |
| "`extra.Ready` runs on the raw request context with no deadline → wedged DB hangs /readyz" | ❌ **stale** — `extra.Ready(probeCtx)` is 2s-bounded (`http.go:109`) + `storeProbeTimeout` inside (`runtime.go:251-252`) |
| "Ready() returns an error on store lookup failure → readyzHandler 503" | ⚠️ **half-true, by design** — *genuine* store errors fail closed (503, pinned `runtime_ready_test.go:206`, `readyz_drill_test.go:161`); *timeout/cancel* degrades (200, pinned `runtime_ready_test.go:176`, `readyz_drill_test.go:444`). The contract's "读路径超时降级非 503" is satisfied; the decision is **made and shipped** |

---

## 3. Requirements

### REQ-0 (enabling, zero behavior change) — readiness seam is importable from `internal/integration`

The `cmd/server` readiness seam moves to a new internal package so the server-level drill mounts **production code**, not a copy (the fullserver stub at `fullserver_test.go:136` is precisely the drift hazard this direction calls out; the repo's architecture already hosts HTTP handlers in internal packages — `internal/api/rest`, `internal/api/s3compat`, `internal/api/webdav`, `internal/mcp`).

**New file `internal/readiness/readiness.go`** (≤500 lines; imports `internal/repository`, `internal/storage`, `internal/auditgovernance` — no import cycle):

- `const ProbeTimeout = 2 * time.Second` — moved `readyzProbeTimeout` (`http.go:52`), comment cross-referencing the helm `readinessProbe timeoutSeconds: 10` bound.
- `type Checker interface { Ready(context.Context) error }` — moved `readinessChecker` (`http.go:28-30`).
- `type DegradedChecker interface { Degraded() bool; BacklogAge() time.Duration }` — moved `degradedChecker` (`http.go:34-41`).
- `type Group []Checker` with `Ready`/`Degraded`/`BacklogAge` methods — moved `readinessGroup` (`http.go:54-84`).
- `func NewGroup(checkers ...Checker) Checker` — moved `runtimeReadiness` (`audit_governance.go:73-87`): returns `nil` when empty (billing + audit both nil → `nil`).
- `func Handler(repo repository.Repository, store storage.Storage, extra Checker) http.HandlerFunc` — moved `readyzHandler` byte-for-byte (`http.go:90-127`): `pingCtx` → `probeCtx` → `extra.Ready(probeCtx)` → degraded marker `{"ok":true,"degraded":true,"backlog_age_seconds":N}` → healthy `{"ok":true}`.
- `func BacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64` / `func DegradedGaugeFn(...)` — moved `build.go:101-118` (cache-fed, zero store I/O per scrape).

**`cmd/server` deltas (delegation only):**

- `http.go`: delete the moved block (`:24-127`); `buildRouter` (`:143`) keeps its signature with `extraReady readiness.Checker` and registers `r.Get("/readyz", readiness.Handler(repo, store, extraReady))`.
- `audit_governance.go`: delete `runtimeReadiness` (`:73-87`); `main.go:157` passes `readiness.NewGroup(billingRuntime, auditRuntime)`.
- `build.go`: `registerGauges` (`:147`) uses `readiness.BacklogAgeGaugeFn(rt)` / `readiness.DegradedGaugeFn(rt)` (`:153-154`); `auditGovernanceDrainGaugesFn` (`:120-131`) **stays in `cmd/server`** (not in this direction's scope).
- Mechanical test updates (behavior identical — the existing seam pins become the regression net for the move): `http_test.go:72/96/116/148` `readyzHandler` → `readiness.Handler`; `readyz_drill_test.go` `serveReadyz` `:148`, checkers embedding `readinessChecker` → `readiness.Checker` (`:38-46`), `runtimeReadiness(nil, rt)` → `readiness.NewGroup(nil, rt)` (`:229/276/304/457`), `auditGovernanceBacklogAgeGaugeFn`/`DegradedGaugeFn` → `readiness.*` (`:294-295/459-460`).
- `internal/integration/fullserver_test.go:136-142`: replace the Ping-only stub with `readiness.Handler(repo, store, nil)` — `TestFullServer_Readyz` (`:204-213`) still asserts 200 (healthy path, `nil` extra), now against the production handler.

**Acceptance (enabling):** `go build ./...` · `go vet ./...` · `go test ./cmd/server/ ./internal/readiness/ ./internal/integration/` green; every pre-existing seam pin in `readyz_drill_test.go`/`http_test.go` passes unmodified in *behavior* (bodies of moved code byte-identical).

### REQ-1 (harness) — production-shaped governance server in `internal/integration` (no build tag; baseline CI)

New file `internal/integration/audit_governance_readyz_test.go` (`package integration`, sqlite — runs under plain `go test ./...`, no `//go:build integration`; the PG-gated sibling direction is out of scope). Helper `newGovernanceServer(t, sinkMode)` mirrors `cmd/server/main.go` assembly order + `governance_e2e_test.go:182-228` + `fullserver_test.go` router/middleware idiom:

- sqlite repo (`file:`+`t.TempDir()`) + `Migrate`; `storage.NewLocal`; `cfg` per `drillRuntimeConfig` (`readyz_drill_test.go:80-100`): `Enabled`, `BaseURL`/`TokenURL` = `http://127.0.0.1:1` (sink down) or the receiver's URL, `HMACKey` 32B, `HTTPTimeoutSeconds 1`, `PollMilliseconds 5`, `BatchSize 10`, `ClaimTTLSeconds 3` (valid: `> 2×HTTPTimeout`, `config_audit_governance.go:261`), `InitialBackoffSeconds 1`, `MaxBackoffSeconds 2` (`≥ 2`, `:266`), **`MaxLagSeconds 4`** (`> ClaimTTL`, `:268`), `ReconcileBatchSize 20`, `DeliveredRetentionSeconds 3600`, `CleanupIntervalSeconds 60`, `CleanupBatchSize 20`, `Revision 1`, one active `acme` binding with a `ClientSecretEnv` matching `validAuditSecretEnv` (e.g. `AUDIT_GOVERNANCE_CLIENT_SECRET_ACME`).
- `rt := auditgovernance.New(...)` → `wrepo := auditgovernance.WrapRepository(repo, rt)` → `bus := events.New(wrepo)` + `bus.WithRepository(wrepo)` → `svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)`.
- chi router: `/healthz`; **`/readyz` = `readiness.Handler(wrepo, store, rt)`** (REQ-0); `server.ApplyMiddleware` 12-ring chain (the `fullserver_test.go:157-161` idiom); `httptest.Server`.
- `rt.Start(context.Background())` (live relay — the "sink 停 60min" drill runs the real loop); LIFO cleanup: receiver close → `rt.Close` → `repo.Close` (the `governance_e2e_test.go:219-224` order; `rt` before `repo` per `readyz_drill_test.go:99-108`).
- Helpers (ported idioms, not copies of production): `putObject` (`governance_e2e_test.go:229-238`), `waitForOutboxRow` (poll sqlite via the dsn, `governance_e2e_test.go:305-320`), `backdateOutboxRow` (second WAL writer `UPDATE audit_governance_outbox SET created_at_ns=? WHERE tenant_id='acme' AND delivered_at_ns=0 AND failed_at_ns=0` — `readyz_drill_test.go:131-146` idiom; `?` placeholders per I1), minimal 422-receiver (`govReceiver` shape, `governance_e2e_test.go:57-113`: `/token` + `/api/v1/events?wait_for=ledgered` → 422).

### REQ-2 (AC-1) — sink down, backlog > maxLag: `/readyz` stays 200 (degraded), no restart loop

`TestGovernanceServerReadyzDegradesWithSinkDown`: harness with unreachable sink → phase 0: GET `/readyz` → 200 `{"ok":true}` (fresh-store negative control; healthy body byte-identical); `putObject` acme → outbox row appears (wait, `deliveredAtNS==0 && failedAtNS==0`); **backdate** `created_at_ns` to 8s (`> maxLag 4s`, 2× margin — the deterministic realization of the acceptance's "wait until backlog > MaxLag", zero sleeps); phase 1: GET `/readyz` → 200, body `{"ok":true,"degraded":true,"backlog_age_seconds":8}` (with the truncation-floor fallback parse of `readyz_drill_test.go:230-243`), elapsed `< 1s` (degrade comes from the probe/cache, not a hang); phase 2 (no restart loop): ≥ 2 further GETs (≥ 1 poll cycle apart; poll 5ms) all 200 with the marker while the row stays pending (relay keeps failing delivery to the dead sink — retry backoff 1s→2s) and `degradedGauge==1` throughout. Every GET that ever returns 503 fails the test.

### REQ-3 (AC-2) — backlog-age gauge reads > maxLag×0.5 with the sink down

`TestGovernanceServerBacklogGaugeExceedsHalfMaxLag`: same sink-down state → `readiness.BacklogAgeGaugeFn(rt)(ctx) > int64(cfg.AuditGovernance.MaxLagSeconds/2)` (4s → threshold 2s; the relay run loop refreshes the cache each poll cycle — `runtime.go:318-323` — so no manual priming) and `readiness.DegradedGaugeFn(rt)(ctx) == 1`. The threshold is **derived from the harness config**, never a literal — `TestNoExecutable450LiteralOutsideAlertsYml` (`readyz_drill_test.go:540`) scans `internal/` and the new file must not trip it. The acceptance's "450s at default 900s maxLag" mapping is already pinned at its owners and **not re-pinned here**: `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381`, expr threshold = `config.Load()` default 900 × 0.5 = 450, `alerts.yml:187`) + `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (`config_audit_governance_test.go:64-87`). The invariant this test pins is the **ratio** maxLag×0.5; the alert expr's absolute 450 is the shipped-default instance of the same ratio. (Optional, out of the required path: a `PROMETHEUS_ENABLED` scrape assertion is already covered by `internal/telemetry/metrics_test.go:171/192` — do not duplicate.)

### REQ-4 (AC-3) — wedged store: `/readyz` bounded, degrades to 200 (never 503, never hangs)

`TestGovernanceServerReadyzBoundedWhenStoreWedged`: wrap the repository (`type wedgedGovRepo struct { repository.Repository }`) overriding only `HasPendingDrainingAuditGovernance` + `OldestPendingAuditGovernance` to `<-ctx.Done()` then return `ctx.Err()` (the `hangingAuditStore` idiom, `readyz_drill_test.go:425-441`); build the harness with the wrapper for both `runtime.New` and `readiness.Handler`; relay not started (isolates the probe path). GET `/readyz` → **200**, body contains `"degraded":true` and `"backlog_age_seconds":0` (age unknown), elapsed ∈ **[1s, 5s]** — deterministic lower bound: the response cannot precede the 2s `storeProbeTimeout` (`runtime.go:251-252`); ≤ 5s proves boundedness (the `TestReadyzStorageProbeTimeout` idiom, `http_test.go:71-88`). Gauge pair: `BacklogAgeGaugeFn==0 ∧ DegradedGaugeFn==1` (the alert OR-arm signal, `alerts.yml:187`). This pins the **resolved decision**: probe timeout/cancel → 200 degraded; genuine store errors → 503 fail-closed is already pinned at unit + seam level (`runtime_ready_test.go:206`, `readyz_drill_test.go:161/258`) and is not re-pinned here (verify-only, §4 D1).

### REQ-5 (AC-4, T-3) — dead-only backlog at server level: `/readyz` 200 and gauge 0

`TestGovernanceServerDeadLetteredBacklogReadyAndGaugeZero`: sink mode `422` (permanent class — the production dead-letter path through the live relay) → `putObject` acme → `waitForOutboxRow(failedAtNS > 0 && deliveredAtNS == 0 && attempts == 1)` (one POST, terminal within one poll cycle) → GET `/readyz` → **200 `{"ok":true}` byte-identical** (terminal rows are excluded from `OldestPendingAuditGovernance` — `internal/repository/audit_governance_claim.go:211` predicate `delivered_at_ns=0 AND failed_at_ns=0` — so a fully dead-lettered backlog is *not* degraded); `BacklogAgeGaugeFn(rt)(ctx) == 0`; `DegradedGaugeFn(rt)(ctx) == 0`. Contrast with REQ-2: the *same* harness, sink down (pending) → degraded; sink 422 (dead-lettered) → healthy — the two shapes cannot be conflated.

---

## 4. Decisions & non-goals

- **D1 — Read-path timeout semantics (the direction's "decision needed"):** **degrade-on-timeout, fail-closed on genuine store errors** — the decision is *already made and shipped* (`isProbeCtxError` `runtime.go:231-236`; genuine-error branches `:260-262`, `:273`; seam `http.go:109-121`; pins `runtime_ready_test.go:176/206`, `readyz_drill_test.go:444/161`). The contract's "读路径超时降级非 503" is satisfied by the timeout branch; the analysis's "implemented 503" was the genuine-error branch, which is a documented, kept fail-closed design (`runtime.go:280-281`). This direction is **verify-only**: REQ-4 re-pins the timeout branch through the production assembly; no production semantics change.
- **D2 — Enabling seam extraction (REQ-0).** The server-level drill must exercise production code; `cmd/server` is `package main` and unreachable from `internal/integration`. A harness-local re-implementation of `readyzHandler` would duplicate the exact seam the direction flags as drifted (`fullserver_test.go:136`); the repo's architecture already places HTTP handlers in internal packages. Scope is strictly a **pure move + delegation**: zero behavior change, moved bodies byte-identical, `cmd/server` tests mechanically updated (they become the move's regression net). `auditGovernanceDrainGaugesFn` and everything unrelated to readiness stay put.
- **D3 — "Wait until backlog > MaxLag" is realized by backdating, not sleeping.** `UPDATE ... SET created_at_ns = now-8s` via a second WAL writer is the house idiom (`readyz_drill_test.go:131-146`; `internal/reconcile lifecycle_test.go` precedent): deterministic, no wall-clock waits, no flake. The outbox row's `created_at_ns` is the backlog-age anchor (`PendingBacklogAge` `runtime.go:198-206` → `OldestPendingAuditGovernance`).
- **D4 — Gauge asserted via the direct callback** (`readiness.BacklogAgeGaugeFn`), which is the *same function* `registerGauges` hands to telemetry (`build.go:153`). The scrape surface (`metrics_test.go:171/192`) and the alert expr parity (`readyz_drill_test.go:381`) are already pinned elsewhere; a `/metrics` server in the harness would duplicate them (and re-register global instruments).
- **D5 — T-3 dead-lettering via the live relay + 422 receiver** (production path: POST → permanent classification → `failFact`), not the seam-level shortcut (direct `ClaimAuditGovernance`+`FailAuditGovernance` API calls, `readyz_drill_test.go:288` phase 1) — the server-level drill proves the *relay* dead-letters and readiness follows.
- **Non-goals (do not expand scope):** the PG behavioral dead-row pin (direction 1 of the same analysis — `audit_governance_postgres_test.go`), B3-6 activation-gate/boot-failure work (direction 3), billing-runtime readiness, drain-still-503 re-pin at server level (already seam-pinned `readyz_drill_test.go:258`), any change to `alerts.yml`/config surface/migrations, `/metrics` scrape in the harness (D4), `auditGovernanceDrainGaugesFn` relocation (D2).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**(1)** *D1 (server-level): harness with unreachable sink (`BaseURL http://127.0.0.1:1`), PUT bound-tenant object, wait until backlog > MaxLag → GET `/readyz` returns 200 `{"ok":true}` (degraded, no restart loop).*
**Testable:** `TestGovernanceServerReadyzDegradesWithSinkDown` (REQ-2) — `newGovernanceServer(t, "down")`; `putObject` acme; `waitForOutboxRow(pending)`; `backdateOutboxRow(8s)` (the deterministic realization of "wait until backlog > MaxLag", maxLag 4s); GET → **200** + exact marker body `{"ok":true,"degraded":true,"backlog_age_seconds":8}`; ≥ 2 further GETs across poll cycles → still 200 + marker while the row stays pending (sink down, relay retrying) — **any 503 fails the test**; pre-backdate GET → 200 `{"ok":true}` (negative control).

**(2)** *D1: BacklogAge gauge callback (build.go) reads age > maxLag×0.5 (450s at default 900s maxLag) with sink down — assert via direct callback or PROMETHEUS_ENABLED scrape.*
**Testable:** `TestGovernanceServerBacklogGaugeExceedsHalfMaxLag` (REQ-3) — after the sink-down state of (1), `readiness.BacklogAgeGaugeFn(rt)(ctx) > int64(cfg.AuditGovernance.MaxLagSeconds/2)` (4s → 2s; derived from harness config, **no executable 450 literal** — enforced by `TestNoExecutable450LiteralOutsideAlertsYml` which scans `internal/`) and `DegradedGaugeFn==1`. The default-config instance (900 → 450) is pinned at its owners: `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381`) + config test; scrape surface at `metrics_test.go:171/192`.

**(3)** *D1: with a stubbed slow/hung store, `/readyz` answers within a bounded window (mirrors `readyzProbeTimeout` pattern) and either degrades to 200 or fails closed deterministically — resolving the contract's "读路径超时降级非 503" vs. implemented 503 (decision needed; propose the degrade-on-timeout branch, verify-only today).*
**Testable:** decision = **degrade-on-timeout** — already shipped (`runtime.go:231-236`), this direction is verify-only. `TestGovernanceServerReadyzBoundedWhenStoreWedged` (REQ-4) — `wedgedGovRepo` hanging on the two probe methods: GET → **200** + `"degraded":true` + age 0, elapsed ∈ **[1s, 5s]** (cannot precede the 2s `storeProbeTimeout`; ≤ 5s = boundedness), gauges `(0, 1)`. The fail-closed half (genuine store error → 503) remains pinned deterministically at unit + seam (`runtime_ready_test.go:206`, `readyz_drill_test.go:161/258`) — unchanged, cited not duplicated.

**(4)** *T-3: dead-only backlog at server level → `/readyz` 200 and gauge reads 0.*
**Testable:** `TestGovernanceServerDeadLetteredBacklogReadyAndGaugeZero` (REQ-5) — sink mode `422`: `putObject` acme → `waitForOutboxRow(failedAtNS>0 && attempts==1)` (live relay dead-letters in one poll cycle) → GET `/readyz` → **200 `{"ok":true}` byte-identical**; `BacklogAgeGaugeFn==0`; `DegradedGaugeFn==0`.

---

## 6. Risks & gates

- **Seam-move blast radius (REQ-0, the main risk):** `readyzHandler`/`readinessGroup`/`runtimeReadiness`/gauge fns are referenced by `http_test.go` and `readyz_drill_test.go`; the updates are mechanical renames, and the pre-existing pins become the move's regression net. Gate: the four packages (`cmd/server`, `internal/readiness`, `internal/integration`, `internal/auditgovernance`) green under `make check` (`gofmt -l` · `go build ./...` · `go vet ./...` · `go test ./...`; 500-line gate excludes `*_test.go`, `Makefile:174-178`; new `internal/readiness/readiness.go` ≈ 140 lines).
- **Concurrency:** the drill starts the live relay (goroutine) + run loop probes against the same repo as `httptest` requests — run `make test-race` (`test-race-meta` covers `internal/auditgovernance`) before merge; LIFO cleanup order (`rt.Close` before `repo.Close`) is the `readyz_drill_test.go:99-108` discipline.
- **Timing flake (mitigated):** no sleeps — backdating replaces "wait until lag" (D3); the wedge lower bound is deterministic (response cannot precede the 2s probe deadline); the ≤ 5s upper bound only proves boundedness (proven idiom).
- **Test runtime:** the wedge drill costs ~2s (one blocking probe); the other drills are sub-second. Poll 5ms keeps the relay cycle tight.
- **450-literal scan:** the new file must derive thresholds from config (`MaxLagSeconds/2`), never hardcode 450 — `TestNoExecutable450LiteralOutsideAlertsYml` scans `internal/` (`readyz_drill_test.go:540`).
- **I1:** any test SQL uses `?` placeholders; created_at handling stays RFC3339Nano on the write path, `created_at_ns` integer in the backdate.

*Verification basis: all citations re-checked on this tree (HEAD `15763e2` + uncommitted B3-campaign worktree); line numbers reflect the tree as read while producing this spec. Full evidence chain in §2; the stage artifact mirrors this document.*
