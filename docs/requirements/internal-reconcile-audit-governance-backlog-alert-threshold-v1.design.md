# Design — `internal/reconcile` analysis: 450s degraded-alert threshold vs. configurable maxLag (contract drift under operator tuning)

**Module (analysis label):** `internal/reconcile` — evidence spans `internal/config` (threshold owner) + `internal/auditgovernance` (gauge source) + `internal/telemetry` (gauge surface) + `cmd/server` (readyz seam, parity/scan pins, gauge callbacks) + `deploy/prometheus/alerts.yml` (threshold literal). **No `internal/reconcile` code is in scope** (decision D4 of the spec; direction 3 of `docs/auto/analyses/internal-reconcile-7a29db11.json` references zero reconcile files).
**Spec:** `docs/requirements/internal-reconcile-audit-governance-backlog-alert-threshold-v1.spec.md` (REQ-1..4, D1..D4)
**Contract:** `docs/campaigns/campaign-aero-vault-b3.yaml:7` (item 2: "Ready 解耦（H1）：maxLag 翻转 → degraded + 450s 告警；终态行排除") — sibling shipped design: `docs/requirements/internal-auditgovernance-backlog-alert-threshold-single-source-v1.design.md`
**HEAD:** `15763e2` + uncommitted worktree (verification basis) · **Date:** 2026-08-08

**Scope lock: verification-only design.** Every acceptance check of the direction maps to a shipped, green pin; zero production-code delta is required. One **optional** test-only delta (R1: extend the derivation table with the acceptance's own example `maxLag=4 → 2`) is specified with an exact patch; adopting or rejecting it does not affect acceptance (spec REQ-2). No config surface, no DB migration, no wire/API change, no `alerts.yml` content change.

---

## 1. Verification register (evidence re-checked, not trusted)

The supplied evidence claims: (i) two deliverables exist, (ii) the direction is already fully implemented and green at HEAD `15763e2` + worktree, (iii) all three acceptance checks map to shipped pins, (iv) four citations hold with documented line drift, one claim obsolete. **Every row was re-read and every pin executed on this tree; all claims verified true.**

| Evidence claim | Verified location (this worktree) | Verdict |
|---|---|---|
| Deliverable 1: `docs/requirements/internal-reconcile-audit-governance-backlog-alert-threshold-v1.spec.md` | Exists, 111 lines, house format mirroring the sibling spec (REQ-1..4, D1..D4, §6 verification steps) | ✅ |
| Deliverable 2: `docs/auto/runs/450s-degraded-alert-threshold-hardcoded-in-alert-f796656a/artifacts/requirements-10762e10/requirements.md` + `.meta.json` | Both exist; `.meta.json` `{"version":1,"fingerprint":"3f8a…ab136","created_at":"2026-08-08T23:51:45Z"}` | ✅ |
| Commit `15763e2` ships the direction ("B3-2 Ready decoupling — backlog degrades instead of 503; backlog-age gauge + 450s alert") | `git show 15763e2` — exact title; touches `cmd/server/build.go`, `alerts.yml`, tests | ✅ |
| (a) `BacklogAlertThresholdSeconds()` at `config_audit_governance.go:42-50` is the single ×0.5 site | Func at `:49-51` (`return c.MaxLagSeconds / 2`, floor), doc `:42-48` states it is "what the alerts.yml parity pin derives from"; repo-wide grep: only consumer is the parity test `readyz_drill_test.go:389` | ✅ (func spans `:49-51`, doc `:42-48` — evidence's `:42-50` is the doc+func span) |
| (a) `TestAlertsYMLAuditGovernanceExprParity` at `readyz_drill_test.go:381-419` reads alerts.yml and derives expr from env-neutralized `config.Load()` | Func at `:381`; `wantExpr = "expr: audit_governance_backlog_age_seconds > " + strconv.Itoa(cfg.AuditGovernance.BacklogAlertThresholdSeconds())` `:388-389`; `t.Setenv("AUDIT_GOVERNANCE_ENABLED","false")` + `MAX_LAG_SECONDS=""` `:383-384`; reads `../../deploy/prometheus/alerts.yml` `:391`; exactly 2 `expr: audit_governance_` file-wide `:399-401`; block asserts expr / `OR audit_governance_degraded == 1` / `for: 10m` / `severity: warning` / `/readyz stays 200` `:408-418`. **Executed: PASS** | ✅ |
| (a) `TestAuditGovernanceBacklogAlertThresholdDerived` at `:87-138` (900/1800/901 in derived arithmetic) | Func at `internal/config/config_audit_governance_test.go:87`; loader path asserts `900/2` env-neutralized `:95-105`; table `{900,1800,901}` with `maxLag/2` wants `:108-127` (`901/2=450` pins floor, an odd delta; ordering `threshold < maxLag` `:126-128`); zero-value → 0 `:133-137`. **Executed: PASS (incl. subtests)** | ✅ |
| (a) `TestNoExecutable450LiteralOutsideAlertsYml` at `:540-583`, zero exemptions | Func at `:540`; `\b450\b` scan over `{cmd, internal, sdk/go}` `.go` files with comments/strings stripped, roots anchored `../..`, loud `WalkDir` errors `:549-577`. **Executed: PASS** — the dead `const alertLagThresholdSeconds` is gone (only its mention survives in the parity test comment `:378`) and the scrape fixture is renamed `450→137` (`metrics_test.go:175-180`) | ✅ |
| (b) e2e: `TestReadyzDeadLetteredBacklog200AndGaugeZero` phase 2 at `:338-364`: maxLag=4s, backdate exactly 2s → gauge > 0, degraded 0 | Func at `cmd/server/readyz_drill_test.go:288`; `drillRuntimeConfig()` `MaxLagSeconds: 4` `:100` (config func `:95-109`); phase 1 `:310-336`, phase 2 `:338-364` — `backdateDrillFact(t, dsn, 2*time.Second)` `:346`, `gauge(ctx) <= 0` fails `:353-354`, `/readyz` 200 `:356-360`, degradedGauge 0 `:361-363`. **Executed: PASS** | ✅ |
| (b) degraded flip via `TestReadyzBacklogLagDegradesNot503` at `:212-257` | Func at `:212`; 8s backdate vs maxLag 4s → `200` + body `{"ok":true,"degraded":true,"backlog_age_seconds":8}` `:231`; pre-asserted `PendingBacklogAge > 4s` `:217-226`. **Executed: PASS** | ✅ |
| (b) scrape path `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` | Func at `internal/auditgovernance/runtime_gauge_scrape_test.go:31` (75 lines, ends `:105`); real runtime + SQLite, 16s backdate → scrape `audit_governance_backlog_age_seconds > 0` `:80`, dead-rows → 0 `:102`. **Executed: PASS** (with the `TestRuntime*` suite) | ✅ |
| (b) rule side: parity pin + `degraded == 1` OR arm at `alerts.yml:187` | Rule `AuditGovernanceBacklogDegraded` at `alerts.yml:186-193`; expr `:187` = `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`; derivation comment `:176-182`; `for: 10m` `:188`, `severity: warning` `:190`, "/readyz stays 200" `:191-193` | ✅ |
| (c) dead-row exclusion predicate at `audit_governance_claim.go:211-225` | `OldestPendingAuditGovernance` func at `:211`; `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` `:218`; returns `ok=false` when none | ✅ |
| (c) asserted in gauge path: phase-1 `:310-336`, `TestRuntimeBacklogAgeZeroWhenAllTerminal`/`WhenNoPending`, scrape dead-rows → 0 | Phase 1 (Claim+Fail → `PendingBacklogAge ok=false`, gauge 0, degradedGauge 0, 200) `:310-336`; `TestRuntimeBacklogAgeZeroWhenAllTerminal` func at `runtime_ready_test.go:299` (41 lines); `TestRuntimeBacklogAgeZeroWhenNoPending` func at `runtime_test.go:676`; scrape dead-rows → `gauge == 0` `runtime_gauge_scrape_test.go:102`. **All executed: PASS** | ✅ |
| Line drift `config:66→114` (default 900) | `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` at `config_audit_governance.go:114`; default side pinned by `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` func at `config_audit_governance_test.go:64-85` (env-neutralized, asserts 900). **Executed: PASS** | ✅ |
| Line drift `runtime.go:146-158→198-293` | `PendingBacklogAge` `:198-206` (doc `:191-197` "drives the degraded alert (maxLag×0.5); terminal rows are excluded by the store query"); `Degraded` `:213-217`; `BacklogAge` `:222-227`; `probeAndRecord` `:251-288` — probe timeout/cancel → `recordDegraded(true, 0)` + nil `:266-277`; flip `age > r.maxLag` → warn + `recordDegraded(true, age)` + nil `:281-286`; store errors stay fail-closed `:267-268,276-277`; `Ready` `:293-295` | ✅ |
| Line drift `metrics.go:354-358→364-385` | `RegisterAuditGovernanceBacklogAgeGauge` doc `:364-367` ("alert at maxLag×0.5, default 450s" — comment only), func `:368-373`; `RegisterAuditGovernanceDegradedGauge` `:378-385`; consumers `cmd/server/build.go:94-105` (`auditGovernanceBacklogAgeGaugeFn` — truncating `int64(rt.BacklogAge().Seconds())`, zero store I/O) + registration `:153-154` | ✅ |
| "No test tying rule to config constant" marked obsolete | ✅ obsolete — the parity pin ties them; drift fails CI in both directions (default side `config test :64-85`, rule side `readyz_drill_test.go:381-419`, second-literal channel closed by `:540-583`) | ✅ |
| Scope note D4: labeled `internal/reconcile`, touches zero reconcile files | `git show --stat 15763e2` + grep of touched files: no `internal/reconcile` path | ✅ |
| Spec §6 test runs all green | Independently re-executed: `go test ./cmd/server/` (4 pins, ok 0.672s) · `./internal/config/` (ok) · `./internal/auditgovernance/` (4 pins, ok 5.487s) · `./internal/telemetry/` (2 pins, ok) | ✅ |

**Problem-statement checks (the direction's claims vs. current tree):**

| Statement | Verdict |
|---|---|
| "alerts.yml hardcodes `> 450`" | ✅ true as a static artifact — `alerts.yml:187` must contain the literal; single-sourcing = the literal is *pinned* to the derivation (spec D1), not templated |
| "An operator maxLag override makes the age arm fire at the wrong ratio (1800 → 0.25×; 60 → 7.5×)" | ✅ still true for the static age arm under override — accepted boundary, documented in `alerts.yml:176-182` + accessor doc; the config-true mitigation is the `degraded == 1` OR arm firing at `age > configured maxLag` (premature paging bounded to ≤ maxLag; no silent SLA miss) |
| "No test or generation step ties the rule to the config constant" | ❌ obsolete — `TestAlertsYMLAuditGovernanceExprParity` (executed PASS) |
| "Gauge and rule drift whenever the config default changes" | ❌ obsolete — drift fails CI in both directions; the executable-`450` ban prevents a second literal channel |

---

## 2. Design

### D1 (SHIPPED — documented, not re-implemented) — the config accessor is the single derivation site

`AuditGovernanceConfig.BacklogAlertThresholdSeconds() int { return c.MaxLagSeconds / 2 }` (`internal/config/config_audit_governance.go:49-51`). Value receiver, zero I/O, floor semantics (safe direction for a warning alert: fires before the `Ready()` warn). All `450`-shaped values derive from it; the alerts.yml literal is compared against it, never hand-maintained. The derivation ordering invariant holds for every valid config (`MaxLagSeconds > ClaimTTLSeconds > 2×HTTPTimeoutSeconds ≥ 2` ⇒ `MaxLagSeconds ≥ 4` ⇒ `MaxLagSeconds/2 < MaxLagSeconds` — alert age arm precedes the Ready warn).

### D2 (SHIPPED — documented, not re-implemented) — parity pin reads the rule file

`TestAlertsYMLAuditGovernanceExprParity` (`cmd/server/readyz_drill_test.go:381-419`) reads `deploy/prometheus/alerts.yml` and asserts the `AuditGovernanceBacklogDegraded` expr equals the accessor-derived value from env-neutralized `config.Load()` — the same loader `main.go` uses. Stdlib-only (`os`/`filepath`/`strings`/`strconv` — I6; no YAML dependency). Companion `TestNoExecutable450LiteralOutsideAlertsYml` (`:540-583`) bans a second literal across `cmd/`, `internal/`, `sdk/go` (comments/strings stripped; zero exemptions in-tree).

### D3 (SHIPPED — documented, not re-implemented) — env neutralization is load-bearing

Both default-side and rule-side pins set `AUDIT_GOVERNANCE_MAX_LAG_SECONDS=""` so expectations compute from the *shipped default* (900), never an ambient operator override. An override cannot silently re-anchor the static comparison.

### D4 (SHIPPED — documented, not re-implemented) — degraded-not-503 seam + gauge semantics

`probeAndRecord` (`internal/auditgovernance/runtime.go:251-288`): probe timeout/cancel → degraded, age 0, `Ready()` nil; genuine store errors and drain-in-progress stay fail-closed 503; backlog `age > maxLag` → degraded marker, `/readyz` 200. Gauges: `audit_governance_backlog_age_seconds` (cache-fed via `BacklogAge()`, zero store I/O per scrape) + `audit_governance_degraded` 0/1 (`internal/telemetry/metrics.go:364-385`, wired `cmd/server/build.go:153-154`). Alert OR arm (`alerts.yml:187`) keeps `for: 10m` accumulation true across probe-timeout samples (age-0 starvation reset prevented).

### R1 (OPTIONAL, test-only) — pin the acceptance's own example at the derivation site

The acceptance (b) example `maxLag=4 → threshold 2` is currently pinned only at the e2e seam (drill config `MaxLagSeconds: 4`, `readyz_drill_test.go:100`). Extend the derivation table in `TestAuditGovernanceBacklogAlertThresholdDerived` so the example value is also pinned at the derivation site:

```go
-	for _, maxLag := range []int{900, 1800, 901} {
+	for _, maxLag := range []int{900, 1800, 901, 4} {
```

`4/2 == 2` is derived arithmetic (no banned `450` token). Note the site nuance: the table test runs pure accessor arithmetic and ordering on the `validAuditGovernanceConfig()` shape (ClaimTTL 30) *without* calling `Validate()` — `4` would not be a *valid* config there, but validity is not required for the accessor assertion (`4/2 == 2`, `2 < 4` holds trivially); the `4` value is a valid config only in the drill shape (`readyz_drill_test.go:100`, ClaimTTL 3, maxLag 4) where the acceptance's own example lives. The existing `threshold < maxLag` assertion covers the ordering. **Decision: adopt R1 (verified green, §8)** — one-line, zero-risk, and it closes the only gap between the direction's own acceptance wording and the pin set. Rejection also acceptable (the table `{900,1800,901}` already pins the derivation rule); either way acceptance is unaffected.

---

## 3. API changes

**Net: zero new API in this design.** The direction's API surface was already delivered by `15763e2` + worktree:

| Surface | Change (already shipped) | Kind |
|---|---|---|
| `internal/config` — `AuditGovernanceConfig.BacklogAlertThresholdSeconds() int` | **New public accessor** — the single `MaxLagSeconds / 2` derivation site (replaces the deleted dead `const alertLagThresholdSeconds = 450`) | additive, value receiver, zero I/O |
| `internal/auditgovernance.Runtime` — `PendingBacklogAge(ctx) (time.Duration, bool, error)`, `BacklogAge() time.Duration`, `Degraded() bool` | **New public accessors** — store-querying + zero-I/O cache getters feeding gauge + /readyz | additive |
| `internal/telemetry` — `RegisterAuditGovernanceBacklogAgeGauge(fn)`, `RegisterAuditGovernanceDegradedGauge(fn)` | **New gauge registration** — `audit_governance_backlog_age_seconds`, `audit_governance_degraded` | additive, opt-in (registered only when the relay runtime is enabled) |
| `/readyz` body | **New degraded marker** — `{"ok":true,"degraded":true,"backlog_age_seconds":N}` when lag > maxLag; healthy body byte-identical (`{"ok":true}`) | backward-compatible (200 status preserved; new fields only in the degraded state) |
| `deploy/prometheus/alerts.yml` | New rule `AuditGovernanceBacklogDegraded` (warning, 10m, OR arm) | deploy artifact, not API |
| Env surface | Unchanged — `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` (default 900) is the sole operator knob; **no new env var** | none |

## 4. Compatibility constraints

- **C1 — No config surface change:** no new env vars, no renamed vars, no default changes (`MaxLagSeconds` default stays 900). An operator's existing `AUDIT_GOVERNANCE_*` set loads identically (strict-bool parsing predates this work).
- **C2 — No wire/DB change:** no schema migration, no wire format change, no HTTP status-code semantics change for existing paths (drain/store-error still 503; healthy still 200; only the degraded state adds body fields).
- **C3 — Static alerts.yml contract:** the rule file is not templated (spec D1); the age-arm literal must stay pinned to the shipped default — env-neutralized pins are load-bearing (D3), so a test-time ambient override can never re-anchor the comparison.
- **C4 — Floor semantics:** `MaxLagSeconds / 2` is Go floor division; an odd maxLag keeps `threshold < maxLag` (the alert fires before the Ready warn). A future ceil change must fail `901/2 == 450` (a ceil yields 451).
- **C5 — Alert ordering invariant:** `threshold (maxLag/2) < Ready warn (maxLag)` for every *valid* config (validation chain guarantees `MaxLagSeconds ≥ 4`); the zero value returns 0 and is never a valid config.
- **C6 — I6 stdlib-only:** the pins use `os`/`filepath`/`strings`/`strconv`/`regexp`; no YAML dependency, no testify, no new `go.mod` deps.
- **C7 — Gate compliance:** pins live in `*_test.go` (exempt from the 500-line / gocyclo gates); `make check` (`go test ./...` at `Makefile:18`) covers them; `TestNoExecutable450LiteralOutsideAlertsYml` must stay within `{cmd, internal, sdk/go}` roots with loud `WalkDir` errors (a relocated test walking nothing must fail, not pass).
- **C8 — Gauge semantics:** `audit_governance_backlog_age_seconds` is cache-fed (freshness ≤ poll interval + /readyz probe cadence), truncating `int64(Seconds())`; a scrape must never block on the store; dead-lettered rows and probe timeouts report 0 (the degraded flag — not the gauge — is the wedge signal).
- **C9 — Scope:** `internal/reconcile` stays untouched (D4); sibling directions (sweeper-event capture, D1 read-path drill) are out of scope.

## 5. Failure modes

| # | Failure | Detection / behavior | Mitigation (shipped) |
|---|---|---|---|
| FM1 | Default drifts 900→N | `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` fails for any N; even N also fails the parity pin (`N/2 ≠ 450`; odd N like 901 keeps `N/2 = 450`, so the default pin — not the parity pin — is the 900 anchor) | env-neutralized default pin at the config owner |
| FM2 | Floor→ceil regression in the accessor | `901/2 == 450` assertion fails (ceil yields 451) | derived-arithmetic table `{900,1800,901}` |
| FM3 | alerts.yml expr literal edited away from the derivation | `TestAlertsYMLAuditGovernanceExprParity` fails (expr mismatch); OR-arm/`for`/severity/description regressions also caught by block asserts | parity pin reads the rule file |
| FM4 | A second `450` literal reintroduced in Go | `TestNoExecutable450LiteralOutsideAlertsYml` fails with explicit file:count hits | `\b450\b` scan, comments/strings stripped, zero exemptions |
| FM5 | Operator maxLag override (e.g., 1800) — static age arm fires at 0.25× | By design (spec D1): age arm stays at the shipped 0.5 ratio of the *default*; the `degraded == 1` OR arm is the config-true signal at `age > configured maxLag` | documented contract `alerts.yml:176-182` + accessor doc; premature paging bounded to ≤ maxLag, no silent SLA miss |
| FM6 | Lowered maxLag (e.g., 60) — age arm fires at 7.5×maxLag | Bounded by the OR arm: degraded fires at 60s | same as FM5 |
| FM7 | Dead-lettered backlog re-arms the alert/gauge | Store predicate `delivered_at_ns=0 AND failed_at_ns=0` excludes terminal rows; `PendingBacklogAge ok=false` → gauge 0, degraded false, /readyz 200 | phase-1 pin + `WhenAllTerminal`/`WhenNoPending` + scrape dead-rows → 0 |
| FM8 | Probe-timeout starvation reset (age 0 every sample) | Age arm alone would reset `for: 10m` on each timeout sample | OR arm `degraded == 1` keeps accumulation true until a genuinely healthy probe; the timeout sample shape (age 0 ∧ degraded 1) is pinned by `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:444`) + `TestRuntimeReadyDegradedSentinel` (`runtime_ready_test.go:194`) |
| FM9 | Gauge/seam regression: lag > maxLag 503s again | `TestReadyzBacklogLagDegradesNot503` (200 + marker) fails; healthy/drain/store-error 503 paths pinned by `TestReadyzExtraProbeTimeout`/`TestReadyzImmediateExtraError`/`TestReadyzDrainStill503` | B3-2/D1 pins |
| FM10 | Scrape path broken (gauge constant zero) | `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (real runtime + SQLite, 16s backdate → > 0) and surface pins fail | real-scrape + surface pins |
| FM11 | Env-neutralization regressed (override re-anchors the static comparison) | Parity/default pins fail under an ambient `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` | `t.Setenv(…, "")` in the §7 (a) pins (D3) — `TestAlertsYMLAuditGovernanceExprParity`, `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`, and the derived-table loader subtest all neutralize |

## 6. Migration steps

**None required — this is a verification-only design.** For completeness, the operator-facing sequence already shipped with `15763e2`:

1. **Deploy the binary + rule file together** (the alert is new in this commit): apply `deploy/prometheus/alerts.yml` with the deployment so `AuditGovernanceBacklogDegraded` exists when the gauge does.
2. **No env changes**: existing `AUDIT_GOVERNANCE_*` configs keep working; `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` remains the sole threshold knob (default 900; alert age arm = 450 = default×0.5).
3. **Behavior change to observe**: a stalled relay no longer fails `/readyz` (200 + `degraded:true` marker + alert) — orchestration relying on 503-to-restart must switch to the alert/gauge.
4. **Verify in staging**: run the pin suites (`§8`) and confirm the new Prometheus series (`audit_governance_backlog_age_seconds`, `audit_governance_degraded`) appear with the relay enabled.
5. **Optional R1** (if adopted): one-line test-table extension; requires only `go test ./internal/config/` re-run.

## 7. Testable acceptance mapping

| Supplied acceptance (verbatim intent) | Requirement | Testable pin (all shipped, executed green) | Go test invocation |
|---|---|---|---|
| (a) "test/grep that the alert expression threshold is derived from the same constant as config default (… asserting expr == maxLag/2)" | REQ-1 | `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381-419`) + `TestAuditGovernanceBacklogAlertThresholdDerived` (`config_audit_governance_test.go:87-138`) + `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (`:64-85`) + `TestNoExecutable450LiteralOutsideAlertsYml` (`readyz_drill_test.go:540-583`) — all four pins env-neutralized (`AUDIT_GOVERNANCE_MAX_LAG_SECONDS=""`), the FM11/D3 contract | `go test ./cmd/server/ -run 'TestAlertsYMLAuditGovernanceExprParity\|TestNoExecutable450LiteralOutsideAlertsYml'` · `go test ./internal/config/ -run 'TestAuditGovernanceBacklogAlertThresholdDerived\|TestAuditGovernanceMaxLagDefault'` |
| (b) "e2e asserting the gauge reports 0.5×maxLag at the degraded flip for a non-default maxLag (maxLag=4s → threshold 2s), verifying rule and gauge agree" | REQ-2 | `TestReadyzDeadLetteredBacklog200AndGaugeZero` phase 2 (`readyz_drill_test.go:338-364`, maxLag 4s / backdate 2s → gauge > 0, degraded 0) + `TestReadyzBacklogLagDegradesNot503` (`:212-257`, flip, 200 marker) + `TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime` (`runtime_gauge_scrape_test.go:31-105`) + rule side = REQ-1 pins + `degraded == 1` OR arm (`alerts.yml:187`) + **FM8 wedge pins** `TestReadyzAuditGovernanceDegradedDrill` (`readyz_drill_test.go:444`; probe hang → 200 marker, ageGauge 0 ∧ degradedGauge 1 — the timeout sample that keeps `for: 10m` accumulation on the OR arm) + `TestRuntimeReadyDegradedSentinel`/`TestRuntimeReadyFailClosedOnGenuineStoreError` (`runtime_ready_test.go:194-296`) + **FM10 surface pins** `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`/`TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`metrics_test.go:171-214`) | `go test ./cmd/server/ -run 'TestReadyzDeadLetteredBacklog200AndGaugeZero\|TestReadyzBacklogLagDegradesNot503'` · `go test ./internal/auditgovernance/ -run 'TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime'` · `go test ./internal/telemetry/ -run 'TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape\|TestAuditGovernanceDegradedGaugeSurfaceInScrape'` |
| (c) "keep the dead-row exclusion property (failed_at_ns=0 predicate) asserted in the gauge path" | REQ-3 | Store predicate `audit_governance_claim.go:211-225` + `TestReadyzDeadLetteredBacklog200AndGaugeZero` phase 1 (`:310-336`) + `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:299`) + `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676`) + scrape dead-rows → 0 (`runtime_gauge_scrape_test.go:102`) | `go test ./cmd/server/ -run 'TestReadyzDeadLetteredBacklog200AndGaugeZero'` · `go test ./internal/auditgovernance/ -run 'TestRuntimeBacklogAgeZeroWhenAllTerminal\|TestRuntimeBacklogAgeZeroWhenNoPending'` |
| T-3/D1 (anchor contracts) | REQ-4 | `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618`) + `TestReadyzBacklogLagDegradesNot503` (`:212-257`) + 503 paths (storage/extra probe) `TestReadyzExtraProbeTimeout`/`TestReadyzImmediateExtraError`/`TestReadyzDrainStill503` (`:161-286`) — the audit-governance probe-timeout path is the *opposite* shape (degraded, never 503): `TestReadyzAuditGovernanceDegradedDrill` (`:444`, FM8) | `go test ./internal/auditgovernance/ -run 'TestRuntimeReadyDegradesOnBacklogLag'` |

## 8. Verification steps (executed at design time, all green)

1. ✅ `go test ./cmd/server/ -run 'TestAlertsYMLAuditGovernanceExprParity|TestNoExecutable450LiteralOutsideAlertsYml|TestReadyzBacklogLagDegradesNot503|TestReadyzDeadLetteredBacklog200AndGaugeZero|TestReadyzAuditGovernanceDegradedDrill' -count=1` — **ok** (2.590s).
2. ✅ `go test ./internal/config/ -run 'TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold|TestAuditGovernanceBacklogAlertThresholdDerived' -count=1` — **ok**.
3. ✅ `go test ./internal/auditgovernance/ -run 'TestRuntimeReadyDegradesOnBacklogLag|TestRuntimeBacklogAgeZeroWhenAllTerminal|TestRuntimeBacklogAgeZeroWhenNoPending|TestRuntimeBacklogAgeGaugeScrapeFromRealRuntime|TestRuntimeReadyDegradedSentinel|TestRuntimeReadyFailClosedOnGenuineStoreError' -count=1` — **ok** (10.119s).
4. ✅ `go test ./internal/telemetry/ -run 'TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape|TestAuditGovernanceDegradedGaugeSurfaceInScrape' -count=1` — **ok**.
5. ✅ **R1 applied and executed** (adopted): the derivation table now includes `4` (`config_audit_governance_test.go:115`); `go test ./internal/config/ -run TestAuditGovernanceBacklogAlertThresholdDerived -count=1` — **ok** (`maxLag=4 → 4/2 == 2`, `2 < 4` ordering asserted in the table loop); full package re-run ok.
6. `make check` full pass recommended before commit. No Makefile/CI edits needed (all pins run under `go test ./...`).

## 9. Risks

- **Pin-drift risk (low):** pins are distributed across `cmd/server` + `internal/config` + `internal/auditgovernance` + `internal/telemetry`; `make check` covers all. The 450-ban has zero exemptions, so any reintroduction fails CI explicitly.
- **Static-rule boundary (accepted, documented):** operator maxLag overrides do not shift the static age arm; the `degraded == 1` arm is the config-true signal. A future demand for per-config half-lag thresholds would require templating the rule file — rejected for effort/benefit (spec D1).
- **Verification-only scope:** this design adds no production code; the implement stage must re-run §8 and (optionally) apply R1 — nothing else. Worktree note: pins landed in commit `15763e2` + the uncommitted audit-governance worktree; nothing here depends on commit boundaries.

*Verification basis: every citation re-read and every pin executed on this tree (HEAD `15763e2` + uncommitted changes); line numbers reflect the tree as read. Full evidence chain in §1.*
