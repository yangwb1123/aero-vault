# Design: Billing relay observability + `Ready()` decoupling + activation e2e — v1

> Parent spec: `docs/requirements/internal-billing-relay-observability-ready-decoupling-activation-e2e-v1.spec.md` (REQ-1..REQ-8, §5 AC-1..AC-4).
> Source analysis: `docs/auto/analyses/internal-billing-d0d7ddd3.json` (direction 3, D1 items 2/4/6). HEAD: `15763e2`.
> This design independently re-verified every evidence citation on this checkout, corrects four test-design defects (G1–G4) that would otherwise fail at implementation time, and maps each acceptance bullet to named tests with assertion anchors.

---

## 1. Evidence verification ledger (independent re-check on this checkout)

Every claim in the evidence/spec was re-verified. **Verdict: all substance claims hold; several line citations drift (noted, none changes the design).**

| # | Claim | Verified | Verdict |
|---|-------|----------|---------|
| V1 | Zero telemetry: `grep -rn "telemetry" internal/billing/` → exit 1 | exit 1, no hits; `grep -n "billing" internal/telemetry/metrics.go` → exit 1 (no billing instrument anywhere) | ✅ exact |
| V2 | Audit relay counters at `metrics.go:189/197/204/212` | `IncAuditGovernanceRelayAttempted/Delivered/Failed/Dead` exactly at `:189/:197/:204/:212`; struct fields `:58-61`, registrations `:105-108` on `aero-vault/domain` | ✅ exact |
| V3 | Backlog gauge `:364-371`, wired `build.go:153` | `RegisterAuditGovernanceBacklogAgeGauge` comment `:364-366`, func `:368` (block `:364-371`); `registerGauges` `build.go:147`, audit gauge wired `:153`; `auditGovernanceBacklogAgeGaugeFn` `:101-105` (cache-fed, zero store I/O) | ✅ holds |
| V4 | 450 s alert at `alerts.yml:186-193` | Rule `AuditGovernanceBacklogDegraded` at `:186` (`alert:` line), expr `:187`, `for: 10m` `:188`, `severity: warning` `:189`; comment `:176-184` documents "450s = maxLag default 900 × 0.5" | ✅ holds |
| V5 | `Ready` checks only projection presence `runtime.go:136-144` | `func (r *Runtime) Ready` `:136`, loop `:137-143`, store err `:139`, `!ok` → `ErrEntitlementUnavailable` `:141-142`; no backlog probe | ✅ exact |
| V6 | `CheckQuota` maps every lookup failure `:156` → `ErrEntitlementUnavailable` | `CheckQuota` `:147-171`: not-bound `:152`, store error `:156` ("projection lookup failed"), not-initialized `:159`; `Apply` `:174-183`, unbound `:178` | ✅ holds |
| V7 | 503 mapping `handler_helpers.go:33-34`, `s3compat/errors.go:120` | `errors.Is(err, service.ErrEntitlementUnavailable)` → `"EntitlementUnavailable"`/503 at `:33-34`; `{service.ErrEntitlementUnavailable, "ServiceUnavailable"}` at `:120` | ✅ byte-exact |
| V8 | `preflightQuota` ordering `file_crud.go:25-38` (local checks run **after** accountant) | `GetTenantQuota` `:26-28` → `CheckQuota` `:31` → `checkBytesQuota` `:35` → `checkObjectsQuota` `:38` | ✅ exact — degrade at `:31` falls through to `:35`/`:38` |
| V9 | Audit scope: `token.go:152-153` exact-match, `model.go:17` | `validTokenScopes` `:152-153` (`scopes[0] == RequiredScope`), `RequiredScope = "audit:event:write"` `model.go:17` (exported) | ✅ exact |
| V10 | Billing scope constants `models.go:6-7`, request `token.go:42`, asserted only `client_test.go:80` | `scopeEntitlementRead = "billing:entitlement:read"` `:6`, `scopeMeteringWrite = "metering:write"` `:7` (unexported); `ClientCredentials(ctx, scopeEntitlementRead, scopeMeteringWrite)` `token.go:42`; `slices.Equal(credentials.scopes, …)` `client_test.go:80` | ✅ exact |
| V11 | Billing tests: 2 quota/Ready + 3 HTTP-only | `runtime_test.go`: exactly `TestRuntimeFailsUnknownProjectionClosed` `:34`, `TestRuntimeEnforcesExplicitZeroAndPreservesProjectedUse` `:50`; `client_test.go`: `:32/:85/:103` (httptest only) | ✅ exact |
| V12 | Outbox relay surface `outbox.go` | `runOutbox` `:14-25` (poll loop), `deliverBatch` `:28-48` (per-fact goroutines), `deliverFact` `:50-68` (`AppendUsage` `:61` with stored `fact.ID`, `CompleteBillingUsage` `:64-67` warn-only on ack-lost), `retryFact` `:71-79`, `billingBackoff` `:82-90` (cap 5 min) | ✅ exact |
| V13 | Audit increment sites | `relay.go:83` attempted, `:112` delivered (after `CompleteAuditGovernance` nil), `:121` dead, `:163` failed | ✅ exact |
| V14 | Audit degraded-test mirror `runtime_test.go:615-700` | `TestRuntimeReadyDegradesOnBacklogLag` `:615` (`cfg.MaxLagSeconds = 4` `:633`, `PendingBacklogAge` `:652`, `Ready()` nil `:659-660`, draining still fails `:668-669`); `TestRuntimeBacklogAgeZeroWhenNoPending` `:676` | ✅ exact |
| V15 | `OldestPendingAuditGovernance` + predicate | func `:211` (spec cited `:188-201` — **drift ~20**); query `:215-219` predicate `o.delivered_at_ns=0 AND o.failed_at_ns=0` **+ JOIN audit_governance_bindings** (bound tenants only — see G4) | ✅ substance, line drift |
| V16 | Audit runtime seams | `PendingBacklogAge` `:198` (block `:191-207`), `Degraded` `:213`, `BacklogAge` `:222`, `recordDegraded` `:239-244` (single-lock pair write), `probeAndRecord` `:246-275` (probe timeout → degraded fail-open), `Ready` `:293-294` (calls probeAndRecord), run-loop feed `:322` (**error logs + skips recording, never stops the loop**) | ✅ exact |
| V17 | Audit config `MaxLagSeconds` | field `:32`, default 900 `:68`, invalid if `> ClaimTTLSeconds` `:265`, `> 604_800` invalid `:275` | ✅ exact |
| V18 | Billing config surface | `BillingConfig` `:19-31` — no lag field; `loadBillingConfig` `:45`; `ClaimTTLSeconds` default 30 `:55`; `readBillingBindings` `:71-109`; `validateBillingURL` `:130-144` — **`http://` allowed only with `AllowInsecureHTTP=true` AND loopback host** (see G1); `resolveBillingSecrets` `:110-125` (env-name pattern, secret from env) | ✅ exact |
| V19 | Billing repo claim/complete/retry | `ClaimBillingUsage` `:11-23`, predicates `status='pending'`/`'inflight'` only (`:34-35`, `:55-56`); `CompleteBillingUsage` `:128-134` (`status='delivered'`); `RetryBillingUsage` `:137-147` (spec cited `:136-147` — off 1) — **no oldest-pending read exists** | ✅ holds |
| V20 | 0038 schema | `status` CHECK `('pending','inflight','delivered')` `:32` (**no `'failed'` — T-3 not implemented**), `created_at_ns` `:38`, `UNIQUE(operation_id, dimension)` `:40`, due index `:43-44` | ✅ exact |
| V21 | Fact ID construction | `factsForMutation` `:145-158` (spec cited `:139-158` — off 6; call site `:133`); `newBillingFact` `:160-166`: `ID = operationID + "." + suffix`; `DeltaBytes=100, DeltaObjects=0` ⇒ exactly one fact (`bytes-allocated`) | ✅ holds |
| V22 | `Apply` mints fresh UUID | `runtime.go:181` `uuid.NewString()` per call (spec cited `:175` — off 6) | ✅ holds |
| V23 | Maintenance-path `Apply` | `meteredRepository.AddTenantUsage` `:30-38` (spec cited `:29-38` — off 1) → `runtime.Apply`; `WrapRepository` `:25-28`; nil-runtime passthrough | ✅ holds |
| V24 | `BillingStore` interface | `internal/repository/billing_types.go:54-61` (spec cited `:47-56` — off 7); `var _ BillingStore = (*sqlStore)(nil)`; `billing.Store` (`runtime.go:19-20`) embeds `repository.BillingStore` → interface growth propagates automatically | ✅ holds |
| V25 | Readiness seam | `cmd/server/http.go:37` comment "billing.Runtime (no Degraded) does not implement it" **exact** (grep-pin target); `degradedChecker` `:39-41`; readyz degraded payload `:117-122` (`200` + `{"ok":true,"degraded":true,"backlog_age_seconds":N}`); **`readyzProbeTimeout = 2s` `:52`, applied to `extra.Ready(probeCtx)` `:105`** — billing's new backlog probe is handler-bounded; pinned `http_test.go:190` (exact degraded body); `runtimeReadiness` `audit_governance.go:73-82` already appends `billingRuntime` | ✅ exact |
| V26 | Mirror-test precedents | `metrics_test.go` gauge surface test func `:171` (spec cited `:166-185` — off 5); `relay_metrics_test.go` `TestRuntimeRelayCountersTrackDeliveryOutcomes` `:88`, `scrapeProm` `:62-64`, own `TestMain` `:30-41` with `telemetry.EnablePrometheus()`; `readyz_drill_test.go` parity test func `:381` (spec cited `:371-410` — comment block `:369-380`), `config.Load()` + env neutralization `:384-389`, `expr:` count `:399`; 450-literal scan `:540-574` (**strips comments/strings — executable tokens only**) | ✅ holds |
| V27 | e2e wiring pieces | `buildBillingRuntime` `cmd/server/billing.go:12-26` (nil when disabled), `wrapBillingRepository` `:30-32`, `main.go:66/:77` and `:196/:207`; `billing.Runtime.Start` `:91-106` (`run` `:108-120` = projector + outbox loops); `RegisterAuditGovernanceBacklogAgeGauge` registration pattern single-shot (OTel duplicate-instrument rejection) | ✅ exact |

**Line-drift summary (non-substantive):** V15 (off ~20), V19/V23/V24 (off 1-7), V21 (off 6), V22 (off 6), V26 (off 5-10). All symbol-level claims exact.

---

## 2. Design corrections — defects found in the spec (must land in implementation)

### G1 — REQ-7 e2e missing `BILLING_ALLOW_INSECURE_HTTP=true` (setup would fail)
`billing.New` → `cfg.Validate()` → `validateBillingURL` (`config_billing.go:130-144`): plain `http://` is rejected unless `AllowInsecureHTTP=true` **and** the host is loopback. `httptest.NewServer` binds `127.0.0.1` (loopback ✅) but `AllowInsecureHTTP` defaults false → `New` returns error before any POST. **Fix:** REQ-7 env harness adds `t.Setenv("BILLING_ALLOW_INSECURE_HTTP", "true")`. Precedent: audit e2e config comment "loopback http allowed (secureEndpoint)".

### G2 — `Ready()`-based assertions need a seeded projection (three test sites would fail)
`Runtime.Ready` (`runtime.go:136-144`) loops **all bound tenants** and returns `ErrEntitlementUnavailable` when `GetBillingProjection` is `ok=false` — **before** the new backlog probe. `ApplyBillingUsage` (`billing_usage.go:15-40`) creates **no** projection row. Therefore, on a fresh test DB:
- AC-1 `TestBillingRuntimeReadyDegradesOnBacklogLag` — "seed one fact via `Apply`; sleep; `Ready()` returns nil" ⇒ `Ready` fails at the projection loop, never reaching the degraded branch.
- `TestBillingRuntimeBacklogAgeZeroWhenNoPending` — same failure on the healthy path.
- REQ-7 step 6 (`runtimeReadiness(billingRuntime, nil).Ready(ctx)` returns nil) — same.

**Fix:** all three seed `store.ApplyBillingProjection(ctx, repository.BillingProjection{TenantID: "acme", Revision: 1, Active: true, EffectiveAt: now.Add(-time.Minute), ProjectedAt: now})` before the first `Ready` (shape precedent: `runtime_test.go:52-63`). This is not a spec change to REQ-4's Ready semantics — it is the test precondition the spec omitted. **Assertion anchor:** the projection loop remains pinned by the unchanged `TestRuntimeFailsUnknownProjectionClosed` (`:34-48`).

### G3 — "`billing_relay_dead_total` series **present** at 0" is unachievable with OTel counters
OTel `Int64Counter` series materialize in a Prometheus scrape only after the **first `Add`**; the dead counter has no increment site until T-3 lands. `scrapeProm` reads absent series as 0. The spec's AC-4 wording "series present at 0" would fail (the audit mirror could assert presence because its test increments dead via the 202+conflict terminal path). **Fix:** the billing mirror asserts `billing_relay_dead_total == 0` **and** `billing_relay_delivered_total delta == 1`, `failed delta ≥ 1`, `attempted delta ≥ 2` — i.e., "dead not incremented", with the registration-level presence pinned instead by `TestBillingMetricsSurfaceInScrape`-style registration coverage (the four counters registered — `initDomain` block) rather than scrape presence.

### G4 — run-loop probe must warn-and-continue (mirror audit `:322`)
REQ-4 says "`runOutbox` calls `probeAndRecord(ctx)` once per poll cycle after `deliverBatch`" but does not specify error handling. Audit precedent (`runtime.go:319-323`): "A genuine store error logs and skips recording — it never stops the loop." **Fix:** billing's `runOutbox` call site: `if err := r.probeAndRecord(ctx); err != nil { r.logger.Warn(...) }` and continue. `Ready()` is the fail-closed path (returns the error → 503); the loop is not. A wedged store would otherwise hang the poll loop exactly like `ClaimBillingUsage` does today — no regression, but the probe must not add a second failure surface.

### G5 — `relay_metrics_test.go` needs its own `TestMain`
The billing package has no `TestMain` today. The scrape mirror requires one (pattern `auditgovernance/relay_metrics_test.go:30-41`): `os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")` + `telemetry.EnablePrometheus()` into a package-level `promHandler` (per-binary globals — no collision with `internal/telemetry`'s own TestMain or the audit package's binary). All existing billing tests run under it unchanged.

---

## 3. Scope & non-goals

**In scope:** REQ-1..REQ-8 as specced, with G1-G5 corrections. Nine production files + six test files touched; one doc comment flip; one alerts.yml rule.

**Non-goals (unchanged from spec §4):** T-3 terminal `failed` state (0038 CHECK lacks `'failed'` — verified V20; only the `billing.relay_dead_total` name is locked now); T-4 deterministic ID generation (`uuid.NewString()` at `runtime.go:181` stays); `Apply` failure degrade; billing degraded-flag gauge + `OR degraded == 1` alert arm; `applyDesiredBindings` store gate for billing; any schema migration (REQ-2 is read-only on 0038; `UNIQUE(operation_id, dimension)` already supports T-4 replay).

---

## 4. API changes (per file / symbol)

### 4.1 `internal/telemetry/metrics.go` (REQ-1, REQ-6)
- Four `metric.Int64Counter` fields after the audit block (`:58-61`): `mBillingRelayAttempted/Delivered/Failed/Dead`.
- Registrations in `initDomain()` after `:105-108`: `billing.relay_attempted_total`, `billing.relay_delivered_total`, `billing.relay_failed_total`, `billing.relay_dead_total` (Prometheus: `billing_relay_*_total`).
- Helpers (lazy `initDomain()`, no attributes): `IncBillingRelayAttempted(ctx)`, `IncBillingRelayDelivered(ctx)`, `IncBillingRelayFailed(ctx)`, `IncBillingRelayDead(ctx)` — doc comments copy the audit semantics (delivered fires only after durable complete; dead = "terminal-with-retention; increment site owned by the T-3 sibling").
- `RegisterBillingBacklogAgeGauge(fn func(context.Context) int64)` — mirror of `:364-371`: observable gauge `billing.backlog_age_seconds` on `aero-vault/domain`, callback per scrape.

### 4.2 `internal/repository` (REQ-2)
- `billing_outbox.go`: new method on `*sqlStore`:
  ```go
  func (s *sqlStore) OldestPendingBillingUsage(ctx context.Context) (time.Time, bool, error)
  // SELECT MIN(created_at_ns) FROM billing_usage_outbox WHERE status IN ('pending','inflight')
  ```
  Returns `time.Unix(0, ns).UTC()`; `ok=false` when `MIN` is NULL. **No placeholders → one portable query serves SQLite and Postgres** (I1: nothing to rebind; the dialect split used by claim is unnecessary here). Postgres needs no `FOR UPDATE` — read-only.
- `billing_types.go`: add `OldestPendingBillingUsage` to the `BillingStore` interface (`:54-61`). `var _ BillingStore = (*sqlStore)(nil)` enforces; `billing.Store` (`runtime.go:19-20`) embeds it → no change needed in `internal/billing`.

### 4.3 `internal/config/config_billing.go` (REQ-3)
- `BillingConfig`: add `MaxLagSeconds int` (after `ClaimTTLSeconds`).
- `loadBillingConfig` (`:45`): `MaxLagSeconds: getEnvInt("BILLING_MAX_LAG_SECONDS", 900)`.
- `Validate()` (`:127-146`): add to the invalid set — `c.MaxLagSeconds <= 0 || c.MaxLagSeconds <= c.ClaimTTLSeconds || c.MaxLagSeconds > 604_800` (mirror `config_audit_governance.go:265/:275`). Verified: `config_billing_test.go` (53 lines, 3 tests) constructs no full `Enabled:true` literal → nothing existing breaks; add one Validate test.

### 4.4 `internal/billing/runtime.go` (REQ-4, REQ-5)
- New fields: `maxLag time.Duration`, `degradedMu sync.RWMutex`, `degraded bool`, `backlogAge time.Duration`.
- `New` (`:43`): `maxLag: time.Duration(cfg.MaxLagSeconds) * time.Second` (Validate runs first — `:48`).
- `PendingBacklogAge(ctx) (time.Duration, bool, error)` — mirror `:198-207`: `OldestPendingBillingUsage` → `time.Since(oldest)`.
- `Degraded() bool`, `BacklogAge() time.Duration` — mutex-protected cache getters, zero store I/O → `*Runtime` satisfies `degradedChecker`.
- `recordDegraded(degraded bool, age time.Duration)` — single-lock pair write (mirror `:239-244`).
- `probeAndRecord(ctx) error` — mirror `:246-275` minus the drain probe: `PendingBacklogAge`; store error → return error (Ready fail-closed; loop warn-continues per G4); `ok && age > maxLag` → warn + `recordDegraded(true, age)`; else `recordDegraded(false, age)` (0 when `!ok`).
- `Ready` (`:136-144`): projection-presence loop **unchanged**, then `if err := r.probeAndRecord(ctx); err != nil { return err }`; return nil.
- `CheckQuota` (`:147-171`): **only the store-error branch (`:154-157`)** becomes `logger.Warn("billing projection lookup failed — degrading to local tenant quota", "tenant", tenant, "err", err)` + `return nil`. Not-bound (`:152`) and not-initialized (`:158-160`) stay `ErrEntitlementUnavailable` (activation gate + existing pin `runtime_test.go:34`).

### 4.5 `internal/billing/outbox.go` (REQ-1, REQ-4)
- First `telemetry` import in the package.
- `runOutbox` (`:14-25`): after `deliverBatch(ctx)` — `if err := r.probeAndRecord(ctx); err != nil { r.logger.Warn("billing backlog probe failed", "err", err) }` (G4; cache freshness ≤ poll interval, default 1 s).
- `deliverFact` (`:50`): first statement `telemetry.IncBillingRelayAttempted(context.Background())`.
- `deliverFact` success path: after `CompleteBillingUsage` nil (`:64-67`) → `telemetry.IncBillingRelayDelivered(...)` (the ack-lost warn branch `:66` increments nothing — audit mirror `relay.go:112` placement).
- `retryFact` (`:72`): `telemetry.IncBillingRelayFailed(...)`.

### 4.6 `internal/billing/models.go` + references (REQ-8)
- `models.go:6-7`: export — `ScopeEntitlementRead = "billing:entitlement:read"`, `ScopeMeteringWrite = "metering:write"` (precedent: `auditgovernance.RequiredScope` exported).
- Update references by name: `token.go:42`, `client_test.go:80`. Literal grep (V10): each literal appears exactly once in the Go tree after this change (verified: no other occurrence today) — the REQ-8 gate premise holds.

### 4.7 `cmd/server` (REQ-6, REQ-4)
- `build.go`: `billingBacklogAgeGaugeFn(rt *billing.Runtime) func(context.Context) int64` — `int64(rt.BacklogAge().Seconds())`, zero store I/O (mirror `:101-105`). `registerGauges` signature → `registerGauges(repo repository.Repository, auditRuntime *auditgovernance.Runtime, billingRuntime *billing.Runtime)`; inside `if billingRuntime != nil { telemetry.RegisterBillingBacklogAgeGauge(billingBacklogAgeGaugeFn(billingRuntime)) }`. **One call site:** `main.go:154` (verified — no test call sites).
- `http.go:37`: flip the comment — "billing.Runtime (no Degraded) does not implement it" → billing implements `degradedChecker` via REQ-4 (grep-pin: the phrase `no Degraded` disappears from the tree).
- `main.go:66/:196` flow unchanged (nil runtime when disabled → gauge guard holds).

### 4.8 `deploy/prometheus/alerts.yml` (REQ-6)
New group `aero-vault-billing` with the single-arm rule (acceptance-locked):
```yaml
- alert: BillingBacklogDegraded
  expr: billing_backlog_age_seconds > 450
  for: 10m
  labels: { severity: warning }
  annotations:
    summary: "Billing relay backlog degraded"
    description: "Oldest pending billing usage fact exceeded the 450s early warning (maxLag default 900 × 0.5). /readyz stays 200 (degraded); check billing_relay_attempted/failed counters and the sink."
```
No degraded-flag arm (spec §4). The 450 literal appears only here (executable-token scan V26).

---

## 5. Compatibility constraints

| Constraint | Guarantee |
|---|---|
| **No DB migration** | REQ-2 reads existing 0038 columns; allowlist `IN ('pending','inflight')` forward-excludes T-3's `'failed'` without schema change. |
| **Interface growth is safe** | `BillingStore` gains one method; the only implementer is `*sqlStore` (V24, enforced by `var _`). `billing.Store` embeds it. No other implementation exists anywhere (`grep BillingStore` → runtime.go + billing_types.go only). |
| **Disabled-billing deployments unaffected** | All new production paths are gated: `registerGauges` nil-guard, `New` unchanged for `Enabled=false` (returns early `:45`), `WrapRepository` nil passthrough, `runtimeReadiness` skips nil billing runtime. Config additions are additive env vars with defaults. |
| **No new dependencies** | stdlib + existing OTel/promhttp; tests stdlib-only (I6). |
| **Existing tests stay green unchanged** | `TestRuntimeFailsUnknownProjectionClosed`, `TestClientUsesMachineTokenAndServerBoundMeteringBody`, `readyz_drill_test.go` audit pins (`:381`, `:540`), `http_test.go:190`, telemetry audit gauge test — none reference billing symbols. The 450-literal scan strips comments/strings (V26) — derived thresholds (`cfg.Billing.MaxLagSeconds/2`) contain no literal. |
| **Counter-name collision** | `billing.*` prefix unused in `metrics.go` (V1) — zero collision. |
| **Semantic change (intentional, acceptance-driven)** | `CheckQuota` store error: 503 → local baseline. Only that branch; the two fail-closed branches are re-pinned by the untouched existing test. |
| **`registerGauges` signature** | Internal to `cmd/server`; single call site `main.go:154`. |

---

## 6. Failure modes & mitigations

| # | Failure mode | Behavior | Mitigation |
|---|---|---|---|
| F1 | Store wedges during `Ready` backlog probe | `Ready` returns error → `/readyz` 503 (fail-closed), bounded by `readyzProbeTimeout` = 2 s (`http.go:52/:105`) — no hang. Audit's probe-timeout→degraded fail-open is **not** mirrored (billing's projection loop already fail-closes on store error today; no regression). Gauge keeps last cached value (no alert reset). | Documented deviation; alert is gauge-driven and unaffected. |
| F2 | Run-loop probe store error | Warn + skip recording, loop continues (G4) — identical exposure to the existing `ClaimBillingUsage` error path. | Mirror audit `:322`. |
| F3 | Binding removed from config while facts in-flight | Facts for the unbound tenant are undeliverable (`deliverFact` → "billing tenant binding missing" → retry forever) and **count in the backlog age** — audit's query excludes them via its bindings JOIN (V15); billing has no bindings table, so REQ-2's allowlist query cannot join. | Accepted semantics: undeliverable = needs operator attention; alert fires until the operator deletes rows or restores the binding. Documented in spec §4-adjacent; no code change. |
| F4 | `dead` counter never incremented (T-3 pending) | Series absent from scrape (OTel lazy counters) — G3 fix asserts value 0, not presence. | Registration covered by a surface test; increment site reserved in doc comments. |
| F5 | Gauge staleness between run-loop feeds | ≤ poll interval (default 1 s) + `/readyz` probe cadence; `Ready` self-probes so the first probe is synchronous. | Mirror audit cache design (V16). |
| F6 | e2e timing flake | First POST arrives immediately (fact inserted with `next_attempt_at_ns = now`); retry case second POST ≈ backoff(1) ≈ 1 s ± jitter; 5 s window + ≥ 2 quiesce polls (2 s) — both well inside claim TTL 30 s. | Poll cadence: `BILLING_OUTBOX_POLL_MILLISECONDS=200` in the e2e to shrink windows; retry assertion uses `waitFor` then `quiesce`, never `waitFor` for negatives (audit e2e precedent A6). |
| F7 | Duplicate gauge registration | OTel rejects duplicate instruments on the same meter. | Single registration point in `registerGauges` (one call, `main.go:154`); tests use single-shot registration pattern (V26). |
| F8 | `Ready` healthy-path change breaks activation gate | Missing projection still fails `Ready` (loop unchanged) and `CheckQuota` (not-bound/not-initialized unchanged). | G2 preconditions + existing pins (`runtime_test.go:34`). |
| F9 | Alert starvation on probe-timeout age-0 samples | Billing has no degraded-flag OR arm; a store wedge keeps the cache at its last value (F1) — the audit's starvation-reset problem (age-0 on timeout) does not arise because billing never records 0 on probe failure. | Documented in the rule description; single-arm contract preserved. |

---

## 7. Migration & delivery steps

1. **Telemetry** (`metrics.go`): counters + gauge registration (4.1) → `internal/telemetry` surface tests (mirror `metrics_test.go:171`).
2. **Repository** (`billing_outbox.go`, `billing_types.go`): `OldestPendingBillingUsage` + interface (4.2) → repository-level test (empty → ok=false; seeded row → age > 0; delivered rows excluded; status-allowlist exclusion of any non-(pending,inflight) row).
3. **Config** (`config_billing.go`): field + env + Validate (4.3) → extend `config_billing_test.go` (default 900; invalid `<=0`, `<= ClaimTTLSeconds`, `> 604_800`).
4. **Runtime** (`runtime.go`): fields, `PendingBacklogAge`, cache getters, `recordDegraded`, `probeAndRecord`, `Ready` extension, `CheckQuota` degrade (4.4) → new runtime tests (AC-1/AC-2 list in §8).
5. **Outbox** (`outbox.go`): probe feed + counter sites (4.5) → `relay_metrics_test.go` with its own `TestMain` (G5).
6. **Models/scope** (`models.go`, `token.go`, `client_test.go`): export + rename references (4.6) → scope grep gate (REQ-8; literal count == 1 each, tree-wide).
7. **Server assembly** (`build.go`, `main.go:154`, `http.go:37` comment) (4.7) → `readyz` degraded drill over billing (payload pin `http_test.go:190` shape) + alert parity test.
8. **Alerts** (`alerts.yml`) (4.8) → `TestAlertsYMLBillingExprParity` + extended 450 scan.
9. **E2E** (`cmd/server/billing_activation_e2e_test.go`) (REQ-7 + G1/G2): env harness → `config.Load()` → `buildBillingRuntime` → `wrapBillingRepository` → seed projection (G2) → `Start` → one `Apply` → assertions.
10. **Gates:** `make check` (gofmt, build, vet, test — SQLite baseline), then explicit `go test -race -count=1 -timeout 120s ./cmd/server/` (**`make test-race` excludes `cmd/server`** — Makefile precedent G1 of the audit e2e design) and `go test -race ./internal/billing/ ./internal/repository/ ./internal/config/ ./internal/telemetry/`. No `go.mod` change → no `go mod tidy`.
11. **Commit:** production + tests + alerts.yml + docs in one commit; the flip of `http.go:37` is grep-pinned (phrase `no Degraded` gone).

---

## 8. Testable acceptance mapping (spec §5 bullets → tests → anchors → gates)

### AC-1 — `Ready()` degrades on backlog, never fail-closed
- `TestBillingRuntimeReadyDegradesOnBacklogLag` (`internal/billing/runtime_test.go`) — mirror `runtime_test.go:615-670`: real SQLite store (`openRuntimeTestStore`), seed projection (G2), `Runtime` literal or `New` with `maxLag: 4*time.Second`, binding → client with unreachable sink (`http://127.0.0.1:1`; literal construction avoids URL validation — either path fine), `Apply` one fact, sleep 4.5 s, assert `PendingBacklogAge` ok=true age>4 s, `Ready()==nil`, `Degraded()==true`, `BacklogAge() >= 4s`, warn logged. **No run loop needed** — `Ready` self-probes (V16 pattern).
- `TestBillingRuntimeBacklogAgeZeroWhenNoPending` — mirror `:676-700`: seeded projection, empty backlog → `ok=false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`.
- `/readyz` payload: `serveReadyz(t, runtimeReadiness(billingRuntime, nil))` → 200 + exact body `{"ok":true,"degraded":true,"backlog_age_seconds":N}` (mirror `readyz_drill_test.go:227-241`, wire pin `http_test.go:190`) — needs projection seeded + aged fact.

### AC-2 — `CheckQuota` degrade to local baseline
- `TestBillingCheckQuotaDegradesOnProjectionLookupFailure` — store double embedding the real store: `type failingProjectionStore struct { billing.Store }` overriding `GetBillingProjection` to return a transient error → `CheckQuota` returns nil + warn fires. Existing `TestRuntimeFailsUnknownProjectionClosed` re-pins the two unchanged fail-closed branches.
- `TestQuotaLocalBaselineEnforcedAfterAccountantDegrade` (`internal/service/`) — fake `UsageAccountant` returning nil → `preflightQuota` over the local hard limit still fails `ErrQuotaExceeded` (pins `file_crud.go:31`→`:35` ordering).

### AC-3 — gauge + 450 s alert
- `TestBillingBacklogAgeGaugeSurfaceInScrape` (`internal/telemetry/metrics_test.go`) — single-shot registration, scrape `billing_backlog_age_seconds` = callback value, flip → 0 (mirror `metrics_test.go:171`).
- `TestAlertsYMLBillingExprParity` (`cmd/server/readyz_drill_test.go` extension) — `t.Setenv("BILLING_ENABLED","false")` + `t.Setenv("BILLING_MAX_LAG_SECONDS","")`; `wantExpr := "expr: billing_backlog_age_seconds > " + strconv.Itoa(cfg.Billing.MaxLagSeconds/2)` (derived, no literal); marker `alert: BillingBacklogDegraded`; `for: 10m`; `severity: warning`; **exactly one `expr: billing_` file-wide**.
- Extend `TestNoExecutable450LiteralOutsideAlertsYml` — no action needed if new Go code uses derived thresholds (scan strips comments/strings, V26; run it before commit to confirm).

### AC-4 — counters + first-event e2e + scope parity
- `TestBillingRelayCountersTrackDeliveryOutcomes` (`internal/billing/relay_metrics_test.go`, new; own TestMain per G5) — mirror `relay_metrics_test.go:88`: two bindings (`succ` 202 / `retry` 500-then-202), scrape-baseline/delta pattern: delivered delta == 1, failed delta ≥ 1, attempted delta ≥ 2, **dead == 0 (G3 — value assertion, not series presence)**.
- `TestBillingActivationFirstEventE2E` (`cmd/server/billing_activation_e2e_test.go`, package main) — env harness: `BILLING_ENABLED=true`, `BILLING_ALLOW_INSECURE_HTTP=true` (G1), `BILLING_BASE_URL`/`BILLING_TOKEN_URL` → fake `httptest.Server`s (token endpoint records requested scopes, returns snake_case OAuth2 body `{"access_token":…,"token_type":"Bearer","expires_in":3600}` — SDK wire shape per audit e2e A1; usage endpoint at `/api/v1/metering/usage` records POST bodies + count, scripts 500-then-202 for the retry leg), bindings file `{"bindings":[{"tenant_id":"acme","client_id":"e2e-client","client_secret_env":"BILLING_E2E_CLIENT_SECRET"}]}` + secret env, `BILLING_OUTBOX_POLL_MILLISECONDS=200`. Seed projection (G2). One `wrapped.AddTenantUsage(ctx,"acme",100,0)` → exactly one POST within 5 s (`waitFor`), `quiesce` ≥ 2 polls with count stable; body `id` matches `^<uuid>\.bytes-allocated$` and is byte-identical across the 500→202 retry (relay delivers stored `fact.ID`, `outbox.go:61`); captured token scopes == `[]string{ScopeEntitlementRead, ScopeMeteringWrite}`; `runtimeReadiness(billingRuntime, nil).Ready(ctx)` nil with projection seeded.
- Scope grep gate (REQ-8): literals exactly once each in the Go tree (`models.go:6-7`); references by name in `token.go:42`, `client_test.go:80`, harness — drift fails compile.

---

## 9. Risks & tripwires

| # | Tripwire | Note |
|---|----------|------|
| R1 | T-3 lands before this direction | T-3's terminal branch must increment `billing.relay_dead_total` (name locked here and in T-3's spec §3/§7); AC-4's dead==0 assertion then flips to the T-3 mirror's dead==1. Non-overlapping symbols otherwise (T-3: `deliverFact` classification + `failFact` + migration 0039/0042-style; this: counters/gauge/Ready/CheckQuota). |
| R2 | T-4 lands before this direction | T-4 replaces `uuid.NewString()` at `runtime.go:181` with `DeterministicFactID` — REQ-7's ID pin (`^<uuid>\.bytes-allocated$`) would need its format updated to T-4's. REQ-7's retry byte-identity pin survives and strengthens. |
| R3 | `registerGauges` call-site drift | Only `main.go:154` today; any new call site must pass the billing runtime (or nil). |
| R4 | 450 literal sneaks into Go code | Derived thresholds only; run the 450 scan before commit (it strips comments/strings, so doc comments are safe). |
| R5 | `relay_metrics_test.go` TestMain vs other billing tests | One TestMain per package binary; existing billing tests are unaffected (they don't scrape); OTLP env neutralization matches audit precedent. |
| R6 | E2E `config.Load()` env sensitivity | Neutralize unrelated env (mirror `readyz_drill_test.go:384-389`: `t.Setenv` for every BILLING_* consumed); audit e2e precedent neutralizes `AUDIT_GOVERNANCE_ENABLED` etc. |
| R7 | `CheckQuota` degrade hides entitlement errors from callers | Deliberate acceptance-driven semantic (503 → local baseline); the warn log is the visibility surface; the audit-governance analog logs + counter (`ai_search_degraded` pattern) — no new metric per spec §4 (no degraded-flag gauge). |

**Net change surface:** 9 production files (`metrics.go`, `billing_outbox.go`, `billing_types.go`, `config_billing.go`, `runtime.go`, `outbox.go`, `models.go`, `build.go`, `http.go` comment, `alerts.yml`), 6 new/extended test files. No schema migration, no dependencies, no router/API surface changes (no new HTTP endpoints; `/readyz` payload shape unchanged — billing simply joins the existing degraded payload contract).
