# Requirements Specification — `internal/billing`: relay observability + `Ready()` decoupling + activation e2e (D1, items 2/4/6)

**Module:** `internal/billing` (+ `internal/telemetry`, `internal/repository`, `internal/config`, `cmd/server`, `deploy/prometheus`)
**Direction:** "Relay observability + Ready() decoupling + activation e2e (D1, items 2/4/6)" (direction 3 of `internal-billing-d0d7ddd3.json`)
**Source analysis:** `docs/auto/analyses/internal-billing-d0d7ddd3.json`
**Contract:** `docs/campaigns/implementation-gate.md:22` (item 2 — Ready 解耦 H1: maxLag 翻转 → `degraded` + maxLag×0.5（450s）告警, D1 drill) · `:24` (item 4 — Relay metrics H6: attempted/delivered/failed/dead/oldest-age) · `:26` (item 6 — 激活门 F-03: enabled + bindings + 首个事件验证). Reference implementation: `internal/auditgovernance` (B3-1..B3-6 shipped).
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = this checkout)
**Score:** value 7 / risk_reduction 7 / effort 6 / confidence 8

---

## 1. Module & scope

The direction's three gaps, all verified against the worktree:

**(a) Zero relay telemetry.** `grep -rn "telemetry" internal/billing/` → **no hits** (exit 1). The audit-governance relay — the in-repo mirror this direction copies — has `IncAuditGovernanceRelayAttempted/Delivered/Failed/Dead` (`internal/telemetry/metrics.go:187-212`), the backlog-age gauge (`RegisterAuditGovernanceBacklogAgeGauge`, `:364-371`) wired at `cmd/server/build.go:153`, and the 450 s alert (`deploy/prometheus/alerts.yml:186-193`). Billing has none of the three: a stalled or wedged billing relay is undetectable.

**(b) Availability-unsafe `Ready()`/quota path.** `billing.Runtime.Ready` (`internal/billing/runtime.go:136-144`) checks only projection presence — a stuck outbox never surfaces. `CheckQuota` maps **every** projection lookup failure (`:156`) to `ErrEntitlementUnavailable`, which REST maps to HTTP 503 (`internal/api/rest/handler_helpers.go:33-34`) and S3 to `ServiceUnavailable` (`internal/api/s3compat/errors.go:120`) — a transient store failure takes the whole write path down instead of degrading to the local `tenant_quotas` baseline (`preflightQuota` `internal/service/file_crud.go:25-38`: `CheckQuota` call `:31`, local `checkBytesQuota`/`checkObjectsQuota` `:35`/`:38`, run **after** the accountant — the degrade semantics below let them fire).

**(c) Activation-gate parity gaps.** Audit governance enforces the exact token scope `audit:event:write` (`internal/auditgovernance/token.go:152-153` + `model.go:17`) and gates activation on applied bindings (`applyDesiredBindings`, `runtime.go:94`/`:329`). Billing requests `scopeEntitlementRead`+`scopeMeteringWrite` (`token.go:42`, constants `models.go:6-7`) with the scope asserted **only** in a unit test (`client_test.go:80`), and there is **no first-event relay e2e** (`runtime_test.go` covers only quota/`Ready`; `client_test.go` covers HTTP only).

**In scope:** ① four relay counters (`attempted`/`delivered`/`failed`/`dead` — the four-name surface; the `dead` increment site is contractually owned by the T-3 sibling, see §4); ② a backlog-age gauge (`billing_backlog_age_seconds`) + `Ready()` degraded (nil) semantics on backlog > `maxLag`, run-loop-fed cache; ③ `CheckQuota` degrade-on-lookup-failure (store error → nil + warn; unbound/not-initialized stay fail-closed); ④ `BILLING_MAX_LAG_SECONDS` knob; ⑤ 450 s alert (`billing_backlog_age_seconds > 450`); ⑥ env-driven first-event activation e2e (`BILLING_ENABLED=true` + binding + one `Apply` → one POST) + token-scope parity (exported constants + grep gate).

**Out of scope:** permanent-error classification / terminal `failed` state / dead-letter (T-3 sibling — spec `docs/requirements/internal-billing-outbox-terminal-failed.spec.md`, **not yet implemented**: migration `0038_snaplink_billing.up.sql:32` CHECK is still `('pending','inflight','delivered')`); deterministic fact-ID generation across calls (T-4 sibling — `uuid.NewString()` at `runtime.go:175` stays); `Apply` failure semantics (atomic accounting must not be silently dropped); `readyz` payload format (already supports any `degradedChecker`).

---

## 2. Evidence verification

Every direction citation checked against this checkout.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | "zero telemetry — grep of internal/telemetry finds no billing counters" | `grep -rn "telemetry" internal/billing/` → exit 1 (no hits); `grep -n "Billing\|billing" internal/telemetry/metrics.go` → no billing instrument | ✅ **exact.** No instrumentation anywhere in the package. |
| E2 | "auditgovernance has IncAuditGovernanceRelayAttempted/Delivered/Failed/Dead (metrics.go:185-210)" | `internal/telemetry/metrics.go`: struct fields `:58-61`, registrations `:105-108` on `aero-vault/domain`; helpers `IncAuditGovernanceRelayAttempted` `:189`, `Delivered` `:197`, `Failed` `:204`, `Dead` `:212`; increment sites `internal/auditgovernance/relay.go:83` (attempted), `:112` (delivered — after `CompleteAuditGovernance` nil), `:121` (dead), `:163` (failed) | ✅ **holds** (citation `:185-210` covers the four helper block `:187-212`, off by 2). |
| E3 | "RegisterAuditGovernanceBacklogAgeGauge (:354) wired at build.go:113" | `RegisterAuditGovernanceBacklogAgeGauge` `metrics.go:364-371` (comment `:364-366`, observable gauge `audit_governance.backlog_age_seconds`); wired `cmd/server/build.go:153` via `auditGovernanceBacklogAgeGaugeFn` (`:101-105`, cache-fed, zero store I/O) inside `registerGauges` (`:147`, called `main.go:154`) | ✅ **holds** (line drift `:354→:364`, `:113→:153`). |
| E4 | "450s alert (alerts.yml:163)" | Rule `AuditGovernanceBacklogDegraded` `deploy/prometheus/alerts.yml:186-193` (comment `:176-184`: "450s = maxLag default 900 × 0.5 early warning"); expr `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`, `for: 10m`, `severity: warning`; pinned by `cmd/server/readyz_drill_test.go:371-410` (threshold derived from `config.Load()` default ÷ 2, `expr:` count == 2) and `:534` (no 450 literal outside alerts.yml) | ✅ **holds** (rule block at `:186-193`; the `:163` citation is comment-adjacent). |
| E5 | "billing.Runtime.Ready (runtime.go:134) checks only projection presence" | `Runtime.Ready` `internal/billing/runtime.go:136-144`: loops bindings → `GetBillingProjection`; store error → `"billing projection lookup failed"`, `!ok` → `fmt.Errorf("%w: tenant %q", ErrEntitlementUnavailable, tenant)` — no backlog probe | ✅ **exact** (`:134` = `func (r *Runtime) Ready` doc-adjacent; body `:136-144`). |
| E6 | "CheckQuota/Apply map every lookup failure to ErrEntitlementUnavailable" | `CheckQuota` `runtime.go:147-171`: not-bound `:152`, store error `:156` ("projection lookup failed"), not-initialized `:159` — all `ErrEntitlementUnavailable`; `Apply` `:174-183`: not-bound `:178` (its `ApplyBillingUsage` errors propagate raw — not an entitlement mapping) | ✅ **holds** for `CheckQuota` verbatim; `Apply`'s unbound-tenant branch also maps. |
| E7 | "→ HTTP 503 (handler_helpers.go:33-34, s3compat/errors.go:120)" | `internal/api/rest/handler_helpers.go:33-34`: `errors.Is(err, service.ErrEntitlementUnavailable)` → `"EntitlementUnavailable"` / `http.StatusServiceUnavailable`; `internal/api/s3compat/errors.go:120`: `{service.ErrEntitlementUnavailable, "ServiceUnavailable"}` | ✅ **exact** (both cited lines verbatim). |
| E8 | "auditgovernance New() rejects unbound backlog via applyDesiredBindings" | `applyDesiredBindings` `internal/auditgovernance/runtime.go:94` (call from `New`) and `:329` (definition) — activation gate on the audit side; billing `New` (`runtime.go:43-96`) applies no store-side binding gate (bindings map built from config only) | ✅ **holds**. |
| E9 | "token.go:153 exact scope audit:event:write; model.go:17" | `validTokenScopes` `internal/auditgovernance/token.go:152-153` (exact match `scopes[0] == RequiredScope`), invoked after `ClientCredentials(ctx, RequiredScope)` `:64-65`; `RequiredScope = "audit:event:write"` `model.go:17` (exported — referenced cross-package by e2es) | ✅ **exact**. |
| E10 | "billing/token.go:34 scopeEntitlementRead+scopeMeteringWrite" | Constants `internal/billing/models.go:6-7` (`billing:entitlement:read`, `metering:write`, **unexported**); requested at `token.go:42`; asserted only in unit test `client_test.go:80` | ✅ **holds** (definition at `models.go:6-7`, request at `token.go:42` — the `:34` citation points at the constants block region of the same file). |
| E11 | "runtime_test.go covers only quota/Ready; client_test.go covers HTTP only" | `internal/billing/runtime_test.go`: exactly 2 tests — `TestRuntimeFailsUnknownProjectionClosed` `:34-48` (missing projection → `ErrEntitlementUnavailable`; `Ready` same), `TestRuntimeEnforcesExplicitZeroAndPreservesProjectedUse` `:50+` — **no backlog/degraded test**; `client_test.go`: 3 tests — `:32` (token + body shape), `:85` (token invalidation), `:103` (reservation commit wire) — httptest only, no runtime relay | ✅ **exact**. |
| E12 | "mirror of auditgovernance runtime_test BacklogAge/Ready degraded assertions" | `internal/auditgovernance/runtime_test.go:615-670` `TestRuntimeReadyDegradesOnBacklogLag` (`cfg.MaxLagSeconds = 4` `:633`, sleep 4.5 s, `PendingBacklogAge > maxLag` `:652-658`, `Ready()` nil `:659-660`, draining still fails `:668-669`) and `:676-700` `TestRuntimeBacklogAgeZeroWhenNoPending` (empty → `ok=false`, `Ready()` nil) | ✅ **exact** — REQ-4's test mirrors. |
| E13 | "mirror of relay_metrics_test.go" | `internal/auditgovernance/relay_metrics_test.go:88` `TestRuntimeRelayCountersTrackDeliveryOutcomes`: baseline scrape → runtime start → POST wait (3 s deadline) → scrape deltas (delivered=1, dead=1, attempted≥3, failed≥1); `scrapeProm` `:62-64` | ✅ **exact** — REQ-1's test mirror. |
| E14 | "gauge registered and scraped" (acceptance 3) | `internal/telemetry/metrics_test.go:166-185` `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`: single-shot registration, scrape → `audit_governance_backlog_age_seconds` = 137 → callback flip → 0 | ✅ **exact** — REQ-6's test mirror. |
| E15 | "degrade-on-lookup-failure semantics do not exist in billing today" | `preflightQuota` `internal/service/file_crud.go:25-38`: `GetTenantQuota` `:26-28` → `usageAccountant.CheckQuota` `:31` → local `checkBytesQuota` `:35` / `checkObjectsQuota` `:38` — the local baseline **already runs after** the accountant; today every `CheckQuota` entitlement error 503s before it. No degrade branch exists | ✅ **exact**. |
| E16 | Outbox relay surface for counter/gauge sites | `internal/billing/outbox.go`: `runOutbox` `:14-25`, `deliverBatch` `:28-48`, `deliverFact` `:50-68` (un-instrumented; `AppendUsage` `:61` with `fact.ID`; `CompleteBillingUsage` `:64-67`), `retryFact` `:71-79`; `billingBackoff` `:82-90` (cap 5 min) | ✅ **exact** — REQ-1/REQ-4 increment/probe sites. |
| E17 | No oldest-pending query in billing repo | `internal/repository/billing_outbox.go`: claim `:11-23` (predicates `status='pending'`/`inflight` only), complete `:128-134`, retry `:136-147` — no `MIN(created_at_ns)` read; schema `0038_snaplink_billing.up.sql`: `status` CHECK `:32`, `created_at_ns` `:38`, due index `:44` | ✅ **exact** — REQ-2 is a new query. |
| E18 | Config surface for `maxLag` | `internal/config/config_billing.go`: `BillingConfig` `:19-31` — **no** lag field; reference `config_audit_governance.go`: `MaxLagSeconds` `:32`, default 900 `:68`, validation `:265` (`> ClaimTTLSeconds` invalid), `:275` (`<= 604_800`) | ✅ **exact** — REQ-3 mirror. |
| E19 | Readiness seam already supports degraded billing | `cmd/server/http.go`: `degradedChecker` `:39-40` (`Degraded() bool`, `BacklogAge() time.Duration`), comment `:37` "billing.Runtime (no Degraded) does not implement it" (**must flip**), `readyzHandler` degraded payload `:117-122` (`200` + `{"ok":true,"degraded":true,"backlog_age_seconds":N}`), pinned `http_test.go:190` / `readyz_drill_test.go:231`; `runtimeReadiness` `cmd/server/audit_governance.go:73-82` already appends `billingRuntime` | ✅ **exact** — billing implementing the interface is sufficient; no seam change needed besides the comment. |

**Problem-statement checks:** (a) "relay invisible — zero telemetry" ✅ (E1); (b) "stuck outbox never surfaces; CheckQuota 503s the write path" ✅ (E5-E7, E15); (c) "no scope-parity e2e, no first-event relay e2e" ✅ (E9-E11).

---

## 3. Requirements

### REQ-1 — Relay counters: attempted / delivered / failed / dead (contract item 4)

**`internal/telemetry/metrics.go`** — mirror of the audit block (`:58-61` fields, `:105-108` registration, `:187-212` helpers): four `metric.Int64Counter` fields on `aero-vault/domain` + helpers `IncBillingRelayAttempted/Delivered/Failed/Dead(ctx)` (lazy `initDomain()`, no attributes):

| Counter (OTel) | Prometheus (`_total`, dots→underscores) | Semantics |
|---|---|---|
| `billing.relay_attempted_total` | `billing_relay_attempted_total` | one delivery attempt (claimed fact processed, incl. retries) |
| `billing.relay_delivered_total` | `billing_relay_delivered_total` | durable completion: receipt accepted **and** `CompleteBillingUsage` returned nil |
| `billing.relay_failed_total` | `billing_relay_failed_total` | transient failure → rescheduled (`retryFact`) |
| `billing.relay_dead_total` | `billing_relay_dead_total` | terminal-with-retention (dead-letter class; **registered now, increment site owned by the T-3 sibling** — see §4) |

**`internal/billing/outbox.go`** — increment sites (first `telemetry` import in the package):
- `deliverFact` entry (`:51`, first statement) → `attempted`.
- `deliverFact` success path — after `CompleteBillingUsage` returns nil (`:64-67`) → `delivered`. Placement mirrors the audit precedent (`relay.go:112` fires only after the durable complete; the acknowledgement-lost branch `:66` warn-only increments nothing).
- `retryFact` entry (`:72`) → `failed` (transient reschedule class).

The 4↔3 mapping is the audit mirror's own shape (`attempted` = delivered + failed + dead + claim-lost path); today `dead` is the registered-at-zero surface.

### REQ-2 — Repository: `OldestPendingBillingUsage` (backlog-age source)

**`internal/repository/billing_outbox.go`** — mirror of `OldestPendingAuditGovernance` (`internal/repository/audit_governance_claim.go:188-201`):

```sql
SELECT MIN(created_at_ns) FROM billing_usage_outbox WHERE status IN ('pending','inflight')
```

- Returns `(time.Time, bool, error)` (`ok=false` when no pending row); `time.Unix(0, ns).UTC()`.
- SQLite + Postgres variants (I1: fresh `$N` placeholders per dialect as needed).
- **Status allowlist, not negation** — `IN ('pending','inflight')` auto-excludes any future terminal status (T-3's `'failed'`) without another migration; a fully dead-lettered backlog reports `ok=false` (mirror of the audit dead-row exclusion, `audit_governance_claim.go:188` predicate `delivered_at_ns=0 AND failed_at_ns=0`).
- Added to the `BillingStore` interface (`internal/repository/billing_types.go:47-56`); `var _ BillingStore = (*sqlStore)(nil)` keeps the contract enforced.

### REQ-3 — Config: `BILLING_MAX_LAG_SECONDS` (the acceptance's `maxLag`)

**`internal/config/config_billing.go`** — mirror of `config_audit_governance.go:32/:68/:265/:275`:
- Field `MaxLagSeconds int` on `BillingConfig`; `getEnvInt("BILLING_MAX_LAG_SECONDS", 900)` in `loadBillingConfig`.
- `Validate()` additions (mirror wording): invalid if `MaxLagSeconds <= 0` or `MaxLagSeconds <= ClaimTTLSeconds`; invalid if `MaxLagSeconds > 604_800`.
- The 450 s alert threshold is **derived** from the default × 0.5 in the parity test (REQ-6), never a second hardcoded Go constant (precedent: `readyz_drill_test.go:371-410` + `:534`).

### REQ-4 — `Runtime`: degraded cache + backlog-aware `Ready()` (contract item 2, acceptance 1)

**`internal/billing/runtime.go`** (199 lines; +~60 → fine):
- **Fields:** `maxLag time.Duration` (from `cfg.MaxLagSeconds`), `degradedMu sync.RWMutex`, `degraded bool`, `backlogAge time.Duration`.
- **`PendingBacklogAge(ctx) (time.Duration, bool, error)`** — mirror of `auditgovernance/runtime.go:191-207`: `OldestPendingBillingUsage` → `time.Since(oldest)`; store error propagates.
- **Cache getters** `Degraded() bool` and `BacklogAge() time.Duration` — mutex-protected, **zero store I/O** — make `*Runtime` satisfy `degradedChecker` (`cmd/server/http.go:39-40`). **`cmd/server/http.go:37` comment "billing.Runtime (no Degraded) does not implement it" must be rewritten** (grep-pin: the word `no Degraded` disappears; the `readyzHandler` degraded payload `:117-122` then covers billing unchanged — pinned by `http_test.go:190`).
- **`recordDegraded(degraded bool, age time.Duration)`** — single-lock pair write (mirror of `runtime.go:239-244`).
- **`Ready(ctx)`** — the existing projection-presence loop (`:136-144`) is **unchanged** (missing projection still fails — activation gate, pinned by `TestRuntimeFailsUnknownProjectionClosed`), then:
  - `PendingBacklogAge`: store error → return error (fail-closed; genuine store failure, same class as the projection-check errors);
  - `ok && age > maxLag` → `logger.Warn("billing usage backlog exceeds maximum lag", "age_seconds", age.Seconds(), "max_lag_seconds", maxLag.Seconds())`, `recordDegraded(true, age)`, **return nil** — never fail-closed on a stuck outbox;
  - else → `recordDegraded(false, age)` (0 when `!ok`), return nil.
- **Run-loop feed:** `runOutbox` (`outbox.go:14-25`) calls `probeAndRecord(ctx)` once per poll cycle after `deliverBatch` (mirror of the audit run-loop refresh) — cache freshness ≤ poll interval (default 1 s), so the gauge and `/readyz` read current state between probes.

### REQ-5 — `CheckQuota` degrade-on-lookup-failure (acceptance 2)

**`internal/billing/runtime.go`** — `CheckQuota` (`:147-171`):
- The **store-error branch only** (`:154-157`, currently `return fmt.Errorf("%w: projection lookup failed", ErrEntitlementUnavailable)`) becomes: `logger.Warn("billing projection lookup failed — degrading to local tenant quota", "tenant", tenant, "err", err)` and **`return nil`**.
- Unchanged, fail-closed (activation gate preserved): not-server-bound (`:152`) and projection-not-initialized (`:158-160`) still return `ErrEntitlementUnavailable` (503).
- Degrade semantics: `preflightQuota` (`internal/service/file_crud.go:25-38`) already runs `checkBytesQuota`/`checkObjectsQuota` (`:35`/`:38`) **after** the `CheckQuota` call (`:31`) — a nil return falls through to the local `tenant_quotas` baseline, which remains enforced. `Apply` is **not** degraded (atomic local-gauge + outbox insert must never be silently dropped; its failures surface as raw repo errors, not entitlement 503s — out of the acceptance's scope).

### REQ-6 — Backlog-age gauge + 450 s alert (acceptance 3)

- **`internal/telemetry/metrics.go`** — `RegisterBillingBacklogAgeGauge(fn func(context.Context) int64)` (mirror of `:364-371`): observable gauge `billing.backlog_age_seconds` → scrape name **`billing_backlog_age_seconds`**, on `aero-vault/domain`, callback value from `fn` per scrape.
- **`cmd/server/build.go`** — `billingBacklogAgeGaugeFn(rt *billing.Runtime)` reading `rt.BacklogAge()` (zero store I/O per scrape — mirror of `:101-105`); `registerGauges` signature becomes `registerGauges(repo, auditRuntime, billingRuntime)` (call site `main.go:154` updated) and registers when non-nil (mirror of `:153`).
- **`deploy/prometheus/alerts.yml`** — new group `aero-vault-billing` with rule **`BillingBacklogDegraded`**:
  ```yaml
  - alert: BillingBacklogDegraded
    expr: billing_backlog_age_seconds > 450
    for: 10m
    labels: { severity: warning }
    annotations:
      summary: "Billing relay backlog degraded"
      description: "Oldest pending billing usage fact exceeded the 450s early warning (maxLag default 900 × 0.5). /readyz stays 200 (degraded); check billing_relay_attempted/failed counters and the sink."
  ```
  Single arm (acceptance-locked: `billing_backlog_age_seconds > 450`); no degraded-flag gauge for billing (not in the acceptance — §4).

### REQ-7 — First-event activation e2e (contract item 6, acceptance 4)

**`cmd/server/billing_activation_e2e_test.go`** (package `main`) — env-driven assembly mirror of the audit production-assembly e2e pattern (`cmd-server-audit-governance-production-assembly-e2e-v1.spec.md` REQ-1), with zero production footprint:

1. **Env harness:** `t.Setenv` `BILLING_ENABLED=true`, `BILLING_BASE_URL`/`BILLING_TOKEN_URL` → fake `httptest.Server`s, `BILLING_BINDINGS_FILE` → temp file `{"bindings":[{"tenant_id":"acme","client_id":"e2e-client","client_secret_env":"BILLING_E2E_CLIENT_SECRET"}]}`, plus the secret env var. (Bindings-file parsing is exercised — `readBillingBindings`, `config_billing.go:71-109`.)
2. **Assembly:** `config.Load()` → `buildBillingRuntime(cfg, repo, logger)` → `wrapBillingRepository(repo, runtime)` → `runtime.Start`.
3. **Fake Snaplink:** token endpoint records the requested scopes and returns a bearer token; usage endpoint records every POST body + count, returns **202**.
4. **One `Apply`:** `wrapped.AddTenantUsage(ctx, "acme", 100, 0)` (maintenance-path `Apply`, `internal/billing/repository.go:29-38`; `deltaObjects=0` ⇒ exactly one fact ⇒ exactly **one** POST — `factsForMutation`, `internal/repository/billing_usage.go:139-158`).
5. **Assertions:**
   - exactly one usage POST arrives **within 5 s** of `Apply` (poll 1 s + delivery; well inside one claim interval — default `ClaimTTLSeconds` 30);
   - no second POST: count stays 1 for ≥ 2 further poll cycles after first arrival;
   - **deterministic fact ID**: the POST body `id` matches `^<uuid>\.bytes-allocated$` (`operationID + "." + suffix`, `newBillingFact` `billing_usage.go:160-166`) **and** a retry case — first POST answered 500, second 202 — carries a **byte-identical** `id` (the relay delivers the stored `fact.ID`, `outbox.go:61`, never re-mints per attempt);
   - **token scope parity:** captured scopes == `[]string{ScopeEntitlementRead, ScopeMeteringWrite}` (REQ-8 names).
6. **`Ready()` contribution:** `runtimeReadiness(billingRuntime, nil).Ready(ctx)` returns nil while the (healthy) relay is live — the activation e2e doubles as the enabled-path readiness pin.

### REQ-8 — Scope export + grep-consistency gate (acceptance 4)

- **`internal/billing/models.go:6-7`** — export the constants: `ScopeEntitlementRead = "billing:entitlement:read"`, `ScopeMeteringWrite = "metering:write"` (precedent: `auditgovernance.RequiredScope` is exported, `model.go:17`, and referenced by cross-package e2es). Update the two existing references: `token.go:42`, `client_test.go:80`.
- **Grep gate** (test or documented CI check, mirror of the audit scope gate): the literals `"billing:entitlement:read"` and `"metering:write"` appear **exactly once each** in the Go tree (`models.go:6-7`); `token.go`, `client_test.go`, and the REQ-7 harness reference the exported constants by name — a drift in token.go's request or the harness's assertion fails CI at compile time.

---

## 4. Non-goals (explicitly out of scope)

| Exclusion | Owner / reason |
|---|---|
| Terminal `failed` state, permanent-error classification (`isPermanentDeliveryError` analog), dead-row retention, `RetryBillingUsage` cap | T-3 sibling direction — spec `docs/requirements/internal-billing-outbox-terminal-failed.spec.md`, **not implemented** (E17: 0038 CHECK lacks `'failed'`). Its §3 does **not** register metrics — this spec registers the four-name surface and **locks the `billing.relay_dead_total` name**; the T-3 implementation increments it on its terminal branch (same name-lock pattern the audit sibling specs used). Until then the mirror test asserts `dead == 0` with the series present. |
| Deterministic fact-ID **generation** (SHA-256 key material, reconcile reuse) | T-4 sibling direction (T-4 acceptance pins cross-call ID determinism; REQ-7's "deterministic fact ID" is the in-scope, weaker contract: the ID is fixed at insert and stable across claim/retry/delivery — `outbox.go:61`). |
| `Apply` failure degrade | Atomic accounting (local gauge + outbox in one transaction, `ApplyBillingUsage`) must not silently drop usage; not in the acceptance. |
| Billing degraded-flag gauge, `OR degraded == 1` alert arm | Acceptance 3 pins the single-arm `billing_backlog_age_seconds > 450`; the degraded flag exists only as the in-memory `/readyz` marker. |
| `readyz` payload / probe-timeout changes, `draining` semantics, `applyDesiredBindings` store gate for billing | Billing has no drain concept; `readyzHandler` already renders any `degradedChecker` (`http.go:117-122`). |

---

## 5. Acceptance (direction-supplied, preserved verbatim, made testable)

> AC-1 — **D1: `Ready()` returns nil (degraded, 200 /readyz) when outbox backlog exceeds maxLag — warns + gauge, never fail-closed; mirror of auditgovernance runtime_test BacklogAge/Ready degraded assertions**

REQ-4 (+ REQ-2/REQ-3/REQ-6). Tests:
- `TestBillingRuntimeReadyDegradesOnBacklogLag` (`internal/billing/runtime_test.go`) — mirror of `auditgovernance/runtime_test.go:615-670`: `cfg.MaxLagSeconds = 4`; seed one fact via `Apply` with an unreachable sink (`http://127.0.0.1:1`); sleep 4.5 s; `PendingBacklogAge` `ok=true`, age > 4 s; `Ready()` returns **nil**; `Degraded()==true`; `BacklogAge() >= 4s`.
- `TestBillingRuntimeBacklogAgeZeroWhenNoPending` — mirror of `:676-700`: empty backlog → `PendingBacklogAge` `ok=false`; `Ready()` nil; `Degraded()==false` (and `BacklogAge()==0` → gauge 0).
- `TestBillingBacklogAgeGaugeSurfaceInScrape` (`internal/telemetry/metrics_test.go`) — mirror of `metrics_test.go:166-185`: single-shot registration, scrape shows `billing_backlog_age_seconds` = callback value, callback flip → 0.
- `TestAlertsYMLBillingExprParity` (`cmd/server/readyz_drill_test.go` or new file) — mirror of `:371-410`: `wantExpr := "expr: billing_backlog_age_seconds > " + strconv.Itoa(cfg.Billing.MaxLagSeconds/2)` derived via `config.Load()` (env neutralized), rule marker `alert: BillingBacklogDegraded` present, `for: 10m`, `severity: warning`, exactly one `expr: billing_` file-wide. `TestNoExecutable450LiteralOutsideAlertsYml` (`:534`) extended: the 450 literal exists only in alerts.yml.
- `/readyz` degraded payload over billing: `serveReadyz(t, runtimeReadiness(billingRuntime, nil))` returns 200 + `{"ok":true,"degraded":true,"backlog_age_seconds":N}` (mirror of `readyz_drill_test.go:227-241`).

> AC-2 — **D1: transient CheckQuota projection lookup failure degrades to local tenant_quotas enforcement instead of returning ErrEntitlementUnavailable/503 (proposed semantics, pinned by new test)**

REQ-5. Tests:
- `TestBillingCheckQuotaDegradesOnProjectionLookupFailure` (`internal/billing/runtime_test.go`): store double whose `GetBillingProjection` returns a transient error → `CheckQuota` returns **nil** (and the warn log fires). Unchanged branches re-pinned by the existing `TestRuntimeFailsUnknownProjectionClosed` (`:34-48`): not-bound and not-initialized still `ErrEntitlementUnavailable`.
- `TestQuotaLocalBaselineEnforcedAfterAccountantDegrade` (`internal/service/`): fake accountant returning nil (degraded) → `preflightQuota` write over the local hard limit still fails `ErrQuotaExceeded` (pins the ordering at `file_crud.go:31`→`:35`).

> AC-3 — **D1: billing backlog-age gauge registered and scraped; Prometheus alert at 450s analog (billing_backlog_age_seconds > 450) lands in alerts.yml**

REQ-6 (+ REQ-3). Tests: `TestBillingBacklogAgeGaugeSurfaceInScrape`, `TestAlertsYMLBillingExprParity`, `TestNoExecutable450LiteralOutsideAlertsYml` (all listed under AC-1).

> AC-4 — **Items 4/6: counters attempted/delivered/failed/dead increment on the relay paths (mirror of relay_metrics_test.go); first-event e2e: BILLING_ENABLED=true + tenant binding + one Apply → one Snaplink POST with deterministic fact ID within claim interval; token request scopes asserted equal to the metering scope contract (grep-consistency check across token.go and the e2e harness)**

REQ-1 + REQ-7 + REQ-8. Tests:
- `TestBillingRelayCountersTrackDeliveryOutcomes` (`internal/billing/relay_metrics_test.go`) — mirror of `relay_metrics_test.go:88` (scrape-baseline/delta pattern, `scrapeProm` analog): two tenants — `succ` (202) and `retry` (500-then-202); asserts `billing_relay_delivered_total` delta == 1, `billing_relay_failed_total` delta ≥ 1, `billing_relay_attempted_total` delta ≥ 2, `billing_relay_dead_total` series **present at 0** (increment site lands with T-3, §4).
- `TestBillingActivationFirstEventE2E` (REQ-7): env-driven (`BILLING_ENABLED=true` + bindings file) → one `Apply` → exactly one usage POST within 5 s (claim interval), deterministic fact ID (format pin + byte-identical across retry), no retry after 202.
- Scope parity: e2e captured-scopes assertion (`[]string{ScopeEntitlementRead, ScopeMeteringWrite}`) + grep gate (REQ-8: literals only in `models.go:6-7`).

---

## 6. Test matrix (new/changed)

| Test | Location | Pins |
|---|---|---|
| `TestBillingRuntimeReadyDegradesOnBacklogLag` | `internal/billing/runtime_test.go` | AC-1 |
| `TestBillingRuntimeBacklogAgeZeroWhenNoPending` | `internal/billing/runtime_test.go` | AC-1 |
| `TestBillingCheckQuotaDegradesOnProjectionLookupFailure` | `internal/billing/runtime_test.go` | AC-2 |
| `TestQuotaLocalBaselineEnforcedAfterAccountantDegrade` | `internal/service/` | AC-2 |
| `TestBillingBacklogAgeGaugeSurfaceInScrape` | `internal/telemetry/metrics_test.go` | AC-3 |
| `TestAlertsYMLBillingExprParity` (+ extend `TestNoExecutable450LiteralOutsideAlertsYml`) | `cmd/server/readyz_drill_test.go` | AC-3 |
| billing `/readyz` degraded payload | `cmd/server/readyz_drill_test.go` (extend existing drill) | AC-1 |
| `TestBillingRelayCountersTrackDeliveryOutcomes` | `internal/billing/relay_metrics_test.go` (new file) | AC-4 |
| `TestBillingActivationFirstEventE2E` | `cmd/server/billing_activation_e2e_test.go` (new file) | AC-4 |
| scope grep gate | test or CI step (REQ-8) | AC-4 |

All new tests stdlib-only (`testing`, I6 — no YAML/assertion deps). Existing tests that must stay green unchanged: `TestRuntimeFailsUnknownProjectionClosed` (`runtime_test.go:34`), `TestClientUsesMachineTokenAndServerBoundMeteringBody` (`client_test.go:32`), `readyz_drill_test.go` audit pins (`:371`, `:534`).

## 7. Composition with sibling directions (same analysis file)

- **T-3** (`internal-billing-outbox-terminal-failed.spec.md`, direction 1): owns the terminal `failed` state. This spec registers `billing.relay_dead_total` and reserves its increment site (name locked); T-3's `failFact` analog increments it. T-3's claim/lag queries must keep excluding terminal rows — REQ-2's status **allowlist** is forward-compatible with `'failed'` by construction.
- **T-4** (`deterministic-billing-fact-ids-b3-3-f4-analog-en-2e0866cf`, direction 2): owns cross-call deterministic ID generation (replaces `uuid.NewString()`, `runtime.go:175`). REQ-7's ID pins are format/retry-stability — they remain valid and are strengthened by T-4 (the retry case then also pins cross-call determinism).
- Both siblings touch `runtime.go`/`billing_usage.go`; no overlapping symbols with REQ-1..REQ-8 except the reserved `dead` increment site (documented here and in T-3's spec).
