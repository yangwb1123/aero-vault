# Design amendment (v3) — F11/F16 closure: `audit_governance_degraded` export (D3 seam, cache-driven, run-loop-refreshed) + OR'd alert expr against the shipped 15-rule set

**Module:** `internal/telemetry` + `cmd/server/build.go` + `deploy/prometheus/alerts.yml` (+ 2 test files); delta of `internal-api-rest-audit-governance-ready-degraded-relay-metrics-v2.design.md`
**Requirements:** v2 spec REQ-1..9 (D1-D3/H1-H3) — **unchanged**; this amendment adds one metric export, one alert-expr edit, one supplementary log line, and test updates. No new REQ.
**Design basis (shipped subset):** `15763e2` (S1-S5) + v2 design (unlanded D1-D3/H1-H3) + pressure test `docs/auto/runs/implement-the-d1-drill-read-path-half-bound-read-b1e25d99/artifacts/adversarial_review-9c87f3a7/meta/pressure_test_f11_f16.md` (recommendation 3b adopted).
**HEAD:** `15763e2` + uncommitted worktree · **Date:** 2026-08-08
**Status:** adopted amendment; the v2 design's F11/F16 "accepted" rows are superseded by this document (verdict: **3b warranted; 3a and 3c-as-the-fix rejected**, §7).

---

## 0. Scope

| # | Change | Surface |
|---|---|---|
| A1 | `RegisterAuditGovernanceDegradedGauge` — `Int64ObservableGauge("audit_governance.degraded")` (0/1), mirroring `RegisterAuditGovernanceBacklogAgeGauge` (`metrics.go:364-368`) | `internal/telemetry/metrics.go` (+~14 lines, 454 → ~468) |
| A2 | Wire inside the existing `if auditRuntime != nil` gate (`build.go:127`), cache getter only — zero store I/O per scrape | `cmd/server/build.go` (+3 lines; named `auditGovernanceDegradedGaugeFn` sibling of `auditGovernanceBacklogAgeGaugeFn` `:94-101`) |
| A3 | `AuditGovernanceBacklogDegraded` expr → `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`; description + group comment extended. `for: 10m`, `severity: warning`, rule name, rule count (15) unchanged | `deploy/prometheus/alerts.yml` (rule block `:181-186`) |
| A4 | Test updates: T3 allowlist grows to exactly `{backlog_age_seconds, degraded}`; parity test gains the OR-arm pin; new scrape-surface test; drill wedge assertion | `internal/telemetry/metrics_test.go`, `cmd/server/readyz_drill_test.go` |
| A5 | Supplementary (free, non-substitute): one warn log line in each `probeAndRecord` timeout branch (closes the wedge log blackout) | `internal/auditgovernance/runtime.go` (D1's timeout branches) |

**Explicitly out of scope:** dashboard panel (verified: no shipped panel references `audit_governance_*`; the v1-spec panel was never shipped — follow-up, §6.3); startup warning for non-default maxLag (already flagged v2 §6 step 10); `for` change (stays 10m — shipped artifact wins over the v1 spec's 5m, §6.1); severity change; new rule; config/env surface; `retain-last-known-age` (rejected, §7.1); `timeout warn log` as the fix (rejected, §7.2).

---

## 1. Verification register (evidence treated as untrusted → re-checked on this checkout)

| # | Claim | Re-verified location (working tree) | Verdict |
|---|---|---|---|
| V1 | Shipped alert rule: `expr: audit_governance_backlog_age_seconds > 450`, `for: 10m`, `severity: warning`, rule name `AuditGovernanceBacklogDegraded`, sole rule of group `aero-vault-audit-governance` at file end (`:176-186`) | `deploy/prometheus/alerts.yml` — group `:176`, comment `:177-180`, rule `:181-186` (`expr :182`, `for :183`, `severity :184`) | ✅ **exact** |
| V2 | Shipped set = **15 rules / 6 groups** (http 2 · ai-cost 2 · integrity 6 · ai-latency 3 · ai-search 1 · audit-governance 1); only `AuditGovernanceBacklogDegraded` references any `audit_governance_*` name; no `up`/scrape-failure rule; no set-operator (`and/or/unless`) anywhere in the file today | `grep -c '^\s*- alert:'` → **15**; `grep -n audit_governance` → only `:177` (comment) + `:182` (expr); `grep -n '\bor\b\|\band\b\|\bunless\b'` → zero | ✅ **exact** |
| V3 | Evaluation cadence 15 s ⇒ `for: 10m` = 40 evaluations | `deploy/prometheus/prometheus.yml:21-22` (`scrape_interval: 15s`, `evaluation_interval: 15s`); `rule_files:` `:27`; `metric_relabel_configs` drops `otel_scope_*` labels `:39-41` → both arms label-less after relabeling | ✅ **exact** |
| V4 | Gauge registration pattern: `Int64ObservableGauge` on `aero-vault/domain` meter, name `audit_governance.backlog_age_seconds` → exported `audit_governance_backlog_age_seconds` (dots→underscores, gauge no `_total`) | `internal/telemetry/metrics.go:364-368`; file-header comment `alerts.yml:3-6`; exported name confirmed live by `TestAuditGovernanceMetrics_SurfaceInScrape`-style scrape pins | ✅ **exact** (new instrument `audit_governance.degraded` → `audit_governance_degraded` by the same rule) |
| V5 | Zero existing `Degraded`/degraded-gauge surface in telemetry (no instrument-name collision, no duplicate-instrument risk) | grep across `internal/telemetry/` + `cmd/server/`: zero `audit_governance.degraded` / `RegisterAuditGovernanceDegraded` hits | ✅ **exact** |
| V6 | Registration gate `if auditRuntime != nil` (`build.go:127`) is the single wiring point; runtime-nil (feature off) ⇒ both gauges absent ⇒ both arms empty ⇒ rule inert (identical to shipped) | `cmd/server/build.go:121-131` (`registerGauges`, `main.go:154`); v2 design E5 | ✅ **exact** |
| V7 | Existing parity test `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:344-373`) uses block-scope `strings.Contains` for `expr: audit_governance_backlog_age_seconds > <maxLag/2>` — **stays green** under the OR'd expr (substring is a prefix of the amended line); pins `/readyz stays 200` + `severity: warning` | `cmd/server/readyz_drill_test.go:357-372` (contains-checks `:368-371`) | ✅ **exact** — and the gap: it does **not** pin the degraded arm; a regression dropping `OR audit_governance_degraded == 1` passes CI today ⇒ A4 adds the OR pin (mandatory) |
| V8 | Drill test `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:270-342`) calls `auditGovernanceBacklogAgeGaugeFn(rt)` directly — D3 must keep the **named function** (body swapped to the cache getter); the amendment's `auditGovernanceDegradedGaugeFn` follows the same named-fn pattern | `readyz_drill_test.go:273`, `:301`, `:319` (gauge calls); `build.go:94-101` (fn) | ✅ **exact** (also matches testing-coverage review conflict-2: named-fn retention + cache priming) |
| V9 | Dashboards: **zero** `audit_governance*` / `backlog` / `readyz` references in either shipped Grafana dashboard; only `ai_search_degraded_total` (unrelated `SearchDegraded` panel) | `grep` both `deploy/grafana/*.json` → single hit `ai_search_degraded_total` (ai-ops `:291`); `grep -c '"title"'` → 14 / 18 panels | ✅ **exact** — no panel impact (also V2's F3-4 audit) |
| V10 | Run-loop refresh: `run()` (`runtime.go:195-205`) cycles at `pollEvery` (default 1 s, `config_audit_governance.go:61`); D1 G3 adds `probeAndRecord(context.Background())` per cycle ⇒ degraded bit freshness ≤ poll interval, independent of `/readyz` and scrape traffic | `internal/auditgovernance/runtime.go:195-205`; v2 design D1 G3 | ✅ **exact** |
| V11 | Cache pair discipline: `recordDegraded` writes both fields under a single `degradedMu.Lock()` (D1 H0) ⇒ every scrape reads a valid pair; reachable pairs: `(1, 0)` timeout/unknown, `(1, age>maxLag)`, `(0, age≤maxLag)`, `(0, 0)` | v2 design D1 H0 + F6/T7 | ✅ **exact** (design-level; CI-enforced by T7 under `-race` via H3) |
| V12 | No rule/severity/runbook conflict: severity `warning` matches the repo taxonomy (critical reserved for data-loss/integrity: `HighServer5xxRate`, `ScrubFoundCorruptObjects`); the v1-spec routing contract (D5.1: same receivers as `EventOutboxTerminalFailures`, also warning) is unchanged by an expr edit; no shipped runbook doc references the rule outside `docs/requirements/` | `alerts.yml` full read; `grep -rn 'AuditGovernanceBacklogDegraded' --include='*.md'` → only requirements docs | ✅ **exact** |
| V13 | v1 sibling spec (`cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-4/REQ-5) already specified this exact surface — `audit_governance.degraded` gauge + `expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450` — with `for: 5m`; the **shipped** subset (15763e2) dropped the gauge and shipped `for: 10m` ⇒ this amendment restores the parent contract's degraded arm against the **shipped** artifact (`for: 10m` wins; divergence documented, §6.1) | `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md:99-131`; shipped `alerts.yml:182-183` | ✅ **exact** — framing: the amendment is the unlanded half of B3-2's own contract, not a net-new surface |
| V14 | No promtool in CI (zero-network `make check`); `go.yaml.in/yaml/v2 v2.4.4` already in module graph (T3's YAML parse, E14) | `which promtool` → absent; `go.mod:73`; `.github/workflows/ci.yml` gates | ✅ **exact** — PromQL validation is manual (this §3) + exact-string T3 pin; optional pre-merge `promtool check rules` documented, not gated |

---

## 2. Design

### A1 — `internal/telemetry/metrics.go`: degraded-flag gauge (after `RegisterAuditGovernanceBacklogAgeGauge`, `:364-368`)

```go
// RegisterAuditGovernanceDegradedGauge registers an observable gauge
// (audit_governance_degraded) whose value is read from fn on each scrape —
// the F11/F16 alert arm: 1 when the last run-loop probe recorded degraded
// (lag > configured maxLag, or store probe timeout/cancel — age unknown),
// 0 otherwise. Cache-fed via Runtime.Degraded(): zero store I/O per scrape.
func RegisterAuditGovernanceDegradedGauge(fn func(context.Context) int64) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("audit_governance.degraded", metric.WithInt64Callback(
		func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(fn(ctx))
			return nil
		}))
}
```

- Exported name: `audit_governance_degraded` (V4). No unit configured — no unit suffix (the `_seconds` in the sibling name is literal, not a unit attribute).
- No labels ⇒ after the shipped `otel_scope_*` labeldrop (`prometheus.yml:38-40`) the series is label-less, exactly like the age gauge — single series, single alert instance (§3.3).

### A2 — `cmd/server/build.go`: wire inside the existing gate (`:127`), cache-driven (D3 seam)

Keep the named functions (V8 — the drill test calls them directly):

```go
// auditGovernanceBacklogAgeGaugeFn … (D3: body swapped to the cache getter —
// per-scrape store query removed, REQ-5; freshness ≤ poll interval via run loop)
func auditGovernanceBacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64 {
	return func(ctx context.Context) int64 {
		return int64(rt.BacklogAge().Seconds())
	}
}

// auditGovernanceDegradedGaugeFn returns the degraded-flag gauge callback
// (0/1 from the cache getter; 1 = lag > configured maxLag or probe
// timeout/cancel — the F11/F16 alert arm; zero store I/O).
func auditGovernanceDegradedGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64 {
	return func(ctx context.Context) int64 {
		if rt.Degraded() {
			return 1
		}
		return 0
	}
}
```

Registration (both inside the existing `if auditRuntime != nil` gate — V6):

```go
	if auditRuntime != nil {
		telemetry.RegisterAuditGovernanceBacklogAgeGauge(auditGovernanceBacklogAgeGaugeFn(auditRuntime))
		telemetry.RegisterAuditGovernanceDegradedGauge(auditGovernanceDegradedGaugeFn(auditRuntime))
	}
```

**D3-seam property (stated precisely):** both audit gauges are cache-fed — scrapes never touch the store. The remaining per-scrape store queries (queue depth, storage gauges, `build.go:121-151`) are unchanged and out of scope; a *broad* DB wedge still hangs `/metrics` via those (the pressure test's side-note (i) — the invisible zone is exactly the audit-probe-specific wedge, which this amendment closes).

### A3 — `deploy/prometheus/alerts.yml`: OR'd expr + description (rule block `:181-186`; group comment `:177-180`)

```yaml
  - name: aero-vault-audit-governance
    # B3-2 (D1): a stalled audit relay degrades instead of failing /readyz.
    # Two signals feed the alert: the backlog-age gauge
    # (audit_governance_backlog_age_seconds, oldest pending non-terminal fact;
    # 450s = maxLag default 900 × 0.5 early warning) and the degraded flag
    # (audit_governance_degraded, cache-fed 0/1: lag > configured maxLag or
    # relay-store probe timeout — age unknown → 0). The OR is deliberate: a
    # probe timeout records age 0, so the age arm alone would reset `for: 10m`
    # on every timeout sample; the degraded arm keeps accumulation true until a
    # genuinely healthy probe (age ≤ 450 AND degraded=0) — no starvation reset.
    rules:
      - alert: AuditGovernanceBacklogDegraded
        expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Audit governance relay backlog degraded"
          description: "Oldest pending audit fact exceeded the 450s early warning (maxLag default 900 × 0.5), or the relay store probe degraded (degraded=1: lag > configured maxLag, or store probe timeout — age unknown). /readyz stays 200 (degraded); check relay_attempted/failed counters and the sink."
```

- **`{{ $value }}` removed from the description** — deliberate: when only the degraded arm matches, the OR result sample carries the *degraded* value (1), not an age; the old phrasing would read "Oldest pending audit fact is 1s old". The pins survive: `/readyz stays 200` (V7), `severity: warning`, rule name, `for: 10m`.
- `OR` (uppercase) is valid PromQL — keywords are case-insensitive; the T3 pin uses the exact shipped string.
- Comment-only edit at `:177-180`; no test parses comments.

### A4 — Tests (delta rows in §8)

1. **OR-arm pin (mandatory)** — extend `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:344-373`) with one block-scope `strings.Contains(block, "OR audit_governance_degraded == 1")`. Without it, a regression dropping the degraded arm (the entire point of this amendment) passes CI (V7).
2. **T3 allowlist growth** — the v2 design's new `TestAlertsYMLAuditGovernanceRuleConsistency` (`metrics_test.go`) pins: rule exists; expr references **exactly** `audit_governance_backlog_age_seconds` and `audit_governance_degraded`, with `> 450` and `== 1` joined by `OR`; **no other** `audit_governance_*` name in any expr (keeps the B3-4 relay-counter collision guard); `for: 10m`; `severity: warning`.
3. **Scrape-surface test** — `TestAuditGovernanceDegradedGaugeSurfaceInScrape` (`metrics_test.go`, T4 idiom): register once (OTel duplicate rejection; single-shot like `TestObservableGauges_SurfaceInScrape` `:168`), captured callback → `1`; `scrapeValue(body, "audit_governance_degraded") == (1, true)`; flip → `0`; re-scrape → `(0, true)`. (V13: the v1 spec's AC-3 already asserted exactly this pair.)
4. **Drill wedge assertion (the F11 end-to-end pin)** — in `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:270-342`): after D3's cache-priming probes, assert `auditGovernanceDegradedGaugeFn(rt)(ctx) == 0` in the empty and dead-lettered phases (dead rows ⇒ not degraded); and in the T2 hanging-store drill (`TestReadyzAuditGovernanceDegradedDrill`), after one `Ready()` probe on the hanging store, assert `ageGauge == 0 ∧ degradedGauge == 1` — the wedge is visible in `/metrics` while age reads 0 (REQ-3's "age unknown → 0" pin preserved, degraded arm carries the signal).

### A5 — `internal/auditgovernance/runtime.go`: wedge log line (supplementary, §7.2)

In **both** `probeAndRecord` timeout branches (D1's code), before `recordDegraded(true, 0)`:

```go
r.logger.Warn("audit governance store probe timed out — degraded", "probe", "drain")
// …and the age-probe branch: "probe", "backlog"
```

One line each; closes the wedge log blackout (today the `runtime.go:179` warn requires a *successful* probe — a timeout records degraded silently). This is **not** the fix (logs are a non-shipped signal); it is free hardening that ships with the amendment regardless.

### A6 — No other surfaces

No config/env (`.env.example`, validation), no migration, no helm/Makefile change, no counter change, no `internal/api/rest` change, `runtime.go` otherwise untouched by this amendment (the cache is D1's). The v2 design's `for: 10m`/severity/rule-count pins are untouched.

---

## 3. PromQL validation (`expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`)

No promtool in CI (V14) — validation is this analysis + the exact-string T3/parity pins; a pre-merge `promtool check rules deploy/prometheus/alerts.yml` is documented as an optional manual step.

1. **Lexing:** both metric names match `[a-zA-Z_:][a-zA-Z0-9_:]*`; literals `450`, `1` are valid numbers; `>` and `==` are comparison operators; `OR` is the set-union keyword (case-insensitive). No `bool` modifier — both operands are instant-vector selectors, so the comparisons are **filters**, not scalar bools.
2. **Precedence:** comparisons bind tighter than set operators ⇒ parses as `(audit_governance_backlog_age_seconds > 450) OR (audit_governance_degraded == 1)`. (PromQL precedence: `* /` > `+ -` > `== != > < >= <=` > `and unless` > `or`.)
3. **Vector semantics:** `a > 450` yields the subset of `a`'s samples with value > 450 (empty when absent or ≤ 450); `b == 1` yields `b`'s samples with value exactly 1 (empty when absent or 0). `or` = set union; on label conflict the left operand wins — irrelevant here since both arms are label-less after the shipped `otel_scope_*` labeldrop (V3).
4. **Alert condition:** the rule fires/pends when the union is **non-empty** — i.e., age > 450 **or** degraded == 1. An empty result (age ≤ 450 and degraded ≠ 1, or both series absent) is the only false evaluation. This is exactly the required semantics — no `absent()`-style staleness tricks.
5. **`== 1` vs `> 0`:** `== 1` pins the 0/1 encoding contract — a future encoding drift to e.g. 2 fails loudly instead of silently firing. The value is `bool → int64` (A2), so 1 is the only true form. Kept.

**Truth table** (default config maxLag 900; `d` = degraded bit, `age` = gauge seconds; pairs are the only reachable `recordDegraded` outputs, V11):

| d | age | Meaning | Age arm | Degraded arm | OR |
|---|---|---|---|---|---|
| 0 | 0 | healthy / empty backlog | ✗ | ✗ | **false** (resets `for`) |
| 0 | 1..450 | healthy | ✗ | ✗ | **false** (resets `for`) |
| 0 | 451..900 | pre-warning band | ✓ | ✗ | **true** — shipped semantics, unchanged |
| 0 | > 900 | unreachable (`d` would be 1) | — | — | — |
| 1 | 0 | wedge / probe timeout (age unknown, REQ-3) | ✗ | ✓ | **true** — F11/F16 closure |
| 1 | > maxLag (any config) | degraded with known age | ✓ (if >450) | ✓ | **true** — incl. the D5 band when maxLag ≤ 450 |

---

## 4. `for: 10m` + 0-age/degraded=1 accumulation — no F16 starvation reset (proof)

**Mechanics (verified):** evaluation every 15 s (`prometheus.yml:22`) ⇒ `for: 10m` = 40 consecutive non-empty evaluations; **any** false evaluation resets the pending counter to 0 (standard Prometheus semantics; the pressure test's F16 mechanics).

**Reset condition with the OR:** a false evaluation requires *both* arms empty — i.e. a sample with `degraded=0` **and** `age ≤ 450`, or no samples at all. Given V11's reachable-pair set, the only such sample is a **successful probe showing a healthy store with no significant backlog** — by definition, genuine recovery.

- **Persistent wedge (F11):** every run-loop probe times out at `storeProbeTimeout` → cache holds `(1, 0)` continuously (run loop keeps cycling; probes are bounded, D1) → every evaluation is true via the degraded arm → **fires exactly ~10 m after wedge onset**. The wedge's only Prometheus signal post-D3/H1 now exists.
- **F16 interleaving (460→0→460, including the drain-probe clobber and `/readyz`-triggered probes):** each sample is either `(0, 460)` (age arm true), `(1, 0)` (degraded arm true), or `(1, 460)` (both) — **every sample is true**; pending never resets; the alert fires after 10 m of the compound sick-store + down-sink failure. The clobber no longer *produces a false sample*; it produces a true one. Starvation is structurally impossible, not papered over.
- **F18 caller-cancel blips:** `(1, 0)` blips are true for the OR — they can only extend the pending window, never reset it (H1 removes the 1 s k8s cancel; run-loop probes use `context.Background()`).
- **Recovery:** first healthy `(0, ≤450)` evaluation resets pending (alerting side: a FIRING alert clears at the next false evaluation — same as shipped; `keep_firing_for` is not used, unchanged).
- **Series continuity:** the gauge callback always observes 0/1 while the process lives (run-loop refresh ≤ 1 s poll, V10; probes bounded ⇒ the loop always completes) — no staleness window within the wedge. Process death stales all series and the rule goes inactive (pre-existing `up`-less gap, out of scope).
- **Metric absence (feature off):** `auditRuntime == nil` ⇒ neither gauge registered ⇒ both arms empty ⇒ rule inert — identical to shipped behavior (V6).

---

## 5. Band semantics (450 pre-warning, D5, maxLag < 900)

| Config | Band | Before amendment | After amendment |
|---|---|---|---|
| default (maxLag 900) | (450, 900] | alert at 450 — pre-warning 450 s before the readiness-degraded flip | **unchanged** (degraded=0 there, age arm fires; §3 table row 3). The OR adds *no* earlier firing in this band. |
| default (maxLag 900) | wedge (age unknown → 0) | **silent forever** (F11) | alert via degraded arm, ~10 m after onset (§4) |
| maxLag < 900 | [maxLag, 450] | degraded per `/readyz` but **below the 450 literal — invisible** (D5 gap) | alert via degraded arm — fires **at** the degradation threshold (config-true signal; §3 row 6). For maxLag < 450 the "450 pre-warning" is moot by construction (degradation precedes the fixed constant); documented trade-off, not a regression. |
| maxLag > 900 | (450, maxLag] | alert fires while `/readyz` healthy (spurious band — shipped) | **unchanged** — the degraded arm adds nothing (degraded=1 ⇒ age > maxLag > 900 > 450, already covered by the age arm). Residual, documented; the v2 design's already-flagged startup warning follow-up is the eventual mitigation. |

Net: the OR closes the D5 `[maxLag, 450]` band for maxLag < 900, leaves the default pre-warning and the maxLag > 900 spurious band exactly as shipped, and adds the wedge signal that did not exist.

---

## 6. Artifact-set impact (shipped 15-rule alerts.yml, dashboards, tests, docs)

### 6.1 Rule-set conflicts — none

- Rule **count stays 15** / 6 groups; the amendment edits one expr + one description + one comment in the existing `AuditGovernanceBacklogDegraded` block (V1/V2).
- No other rule references either metric name; `audit_governance_degraded` collides with nothing (`ai_search_degraded_total`, `relay_*` counters, `webhook_*`, `jobs_*` — distinct names).
- **`for: 10m` divergence note:** the v1 sibling spec specified `for: 5m`; the **shipped** artifact is `for: 10m` (V13). This amendment validates against the shipped set and keeps 10 m — the accumulation proof (§4) holds for any `for`; 10 m is the shipped truth and T3 pins it.

### 6.2 Severity / runbook conflicts — none

- `severity: warning` unchanged; repo taxonomy intact (critical reserved for data-loss/integrity rules `HighServer5xxRate`, `ScrubFoundCorruptObjects`).
- v1-spec D5.1 routing contract (same Alertmanager receivers as `EventOutboxTerminalFailures`, also warning) unaffected by an expr edit; the description remains the runbook and now names both firing conditions; `/readyz stays 200` pin preserved (V7).
- No shipped runbook/ops doc references the rule outside `docs/requirements/` (V12) — nothing to update there.

### 6.3 Dashboard / panel impact — none required (verified zero)

- Both shipped dashboards have **zero** `audit_governance*` / `backlog` / `readyz` references (V9) — no panel reads the age gauge today, so the OR'd expr and the new series break nothing and require no panel change.
- The v1 spec's `TestGrafanaAuditGovernancePanel` + "Audit-governance backlog (degraded)" panel were **never shipped** (V9) — restoring the second, non-paging consumer is an explicit follow-up, not part of this amendment (packaging scope lock).
- The `SearchDegraded` panel (`ai_search_degraded_total`, ai-ops `:291`) is unrelated.

### 6.4 Test-surface impact (existing tests)

| Test | Impact of A1-A4 |
|---|---|
| `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:344-373`) | **Stays green** (substring still contained); extended with the mandatory OR-arm pin (A4.1) |
| `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`:270-342`) | Unaffected by A1-A3 (uses the named fn — V8; D3's cache-priming fix is the base design's); extended with degraded-gauge assertions (A4.4) |
| `TestReadyzBacklogLagDegradesNot503` (`:207-237`) | Base-design D2 update (marker body) — unchanged by this amendment |
| `TestAuditGovernanceMetrics_SurfaceInScrape` (`metrics_test.go:106`) | Unaffected (asserts specific relay-counter names, not whole-body) |
| T4 (v2) `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` | Unaffected (different instrument name; single-shot registration each) |
| Runtime tests (T1b/T1c/T5/T6/T7) | Unaffected — the cache semantics are D1's; this amendment only exports `Degraded()` (no runtime behavior change) |
| T3 (v2) `TestAlertsYMLAuditGovernanceRuleConsistency` | **Updated** — allowlist grows to exactly `{backlog_age_seconds, degraded}` (A4.2) |

### 6.5 Docs to update

- v2 design §5 rows F11/F16: "accepted, amendment flagged" → "**closed by v3 (A1-A4)**"; F7 row: D5 band for maxLag < 900 → "closed by the degraded arm"; §6 migration steps gain the metric/expr step; §7 T3 row updated; compat list gains "the degraded gauge is additive and cache-fed".
- `docs/CHANGELOG.md`: release note (rule expr change + new metric + wedge log).
- No `docs/configuration.md` change (the `:274` wording follow-up remains flagged, unchanged).

---

## 7. Alternatives (documented, rejected)

### 7.1 Retain-last-known-age (pressure-test 3a) — **rejected**

- Closes F16 only; the clean-baseline wedge (last known 0 stays 0) and the compound wedge+sink case stay silent (F11 remains).
- Violates the pinned parent contract: REQ-3 "age unknown → 0" (`cmd-server-audit-governance-ready-degraded-v1.spec.md:67`) and the v2 design's pinned T1b assertion `BacklogAge()==0` on the hanging-store case — a spec + pinned-test amendment for a *partial* fix.
- Semantic hazard: without an exported freshness discriminator, a stale 460 is indistinguishable from a live 460 — "unknown" becomes "misleadingly known". The discriminator it would need is exactly the degraded bit this amendment exports.

### 7.2 Scope-lock fallback: timeout warn log (pressure-test 3c) — **evaluated, rejected as the fix; the log line itself adopted as A5**

- What it closes: the wedge log blackout (today a probe timeout records degraded silently — the `runtime.go:179` warn requires a successful probe). One line, zero risk.
- Why it is **not** a fix: (i) logs are a **non-shipped** observability surface — no log-shipping/collection config exists in `deploy/` (grep-verified; the shipped stack consumes `/metrics` and k8s status only); (ii) the alert silence remains — the wedge stays un-paged; (iii) post-D3/H1 the wedge has *zero* automated signals (the D3 trade removed the accidental `/metrics` hang, H1 removed the accidental eviction) — a log line inverts none of that.
- Verdict: if the "no new metric" scope lock were immovable, land the log + re-file the metric as a follow-up (pressure-test 3c). Since the amendment is adopted, A5 lands the log line as free supplementary hardening **and** the metric+OR as the fix. Rejected-alternative status is documented here for the gate.

---

## 8. Testable acceptance mapping (amendment rows; v2 §7 table stands for everything else)

| Acceptance | Test | File / mechanics | Assertion surface |
|---|---|---|---|
| Degraded gauge exports 0/1, cache-fed | `TestAuditGovernanceDegradedGaugeSurfaceInScrape` | `internal/telemetry/metrics_test.go` (T4 idiom, single-shot registration, `scrapeValue`) | callback → 1 ⇒ scrape contains `audit_governance_degraded 1`; flip → 0 ⇒ `… 0` (line-exact) |
| OR'd expr pinned (both arms, both directions) | `TestAlertsYMLAuditGovernanceRuleConsistency` (T3, updated) + parity test OR pin | `metrics_test.go` (YAML-parse, `go.yaml.in/yaml/v2` E14) + `readyz_drill_test.go:344-373` | rule exists; expr references exactly `{backlog_age_seconds, degraded}` with `> 450` + `== 1` + `OR`, no other `audit_governance_*` name in any expr; `for: 10m`; `severity: warning`; block contains `OR audit_governance_degraded == 1` and `/readyz stays 200` |
| Wedge visible in `/metrics` with age 0 (F11 end-to-end) | Drill extension of `TestReadyzAuditGovernanceDegradedDrill` + `TestReadyzDeadLetteredBacklog200AndGaugeZero` | `cmd/server/readyz_drill_test.go` | hanging store → after one `Ready()` probe: `ageGauge == 0` ∧ `degradedGauge == 1`; dead-lettered/empty phases (after priming) → `degradedGauge == 0` (dead rows ⇒ not degraded, re-pin of T5 at the scrape seam) |
| Log line on probe timeout (A5) | T1b's hanging-store subtest (warn-log presence is asserted by the drill's existing elapsed/branch pins; no slog capture — log is non-contractual) | `internal/auditgovernance/runtime_ready_test.go` | timeout branch returns nil + records `(1, 0)` (unchanged); the warn line is code-reviewed, not string-pinned |
| F16 accumulation semantics | PromQL truth-table (§3.4) + §4 proof — pinned **compositionally**: T6 (cache-rise) + degraded-gauge surface (above) + T3 exact-string rule | — | the OR's reset-only-on-health property is a rule-level property; the shipped status is the honest CI bound (no Prometheus in `go test`) |

**Preservation pins (unchanged):** all v2 §7 preservation pins + `TestAlertsYMLAuditGovernanceExprParity`'s existing pins (threshold derivation via `config.Load()`, severity, `/readyz stays 200`).

---

## 9. Migration & rollout

No DB/config/wire migration. Deploy ordering per the v1 spec's D5.3 contract: `alerts.yml` lands **with or before** the binary — rule-first is safe (absent series ⇒ degraded arm empty ⇒ no behavior change); binary-first is also safe (old rule doesn't reference the new series; the age-arm behavior is byte-identical until the rule reload) — but the contract's rule-first ordering is kept.

1. Land v2 D1-D3/H1-H3 per v2 §6 (renames → runtime cache → http → build cache-swap → helm → tests → gate).
2. Land A1/A2 (gauge + wiring) — `make check` green (A4.3 test included).
3. Land A3 (`alerts.yml` expr + description + comment) + A4.1/A4.2 pins in the **same** change (rule and its pins must not split).
4. Land A5 (log lines) with D1's `probeAndRecord` (or in this amendment's commit — either; both green).
5. Rollout observation: with `PROMETHEUS_ENABLED=true`, after enabling audit governance, verify `/metrics` contains `audit_governance_degraded 0`; induce a relay-store hang (outbox-table lock) and observe the gauge flip to 1 within one poll interval and the alert fire ~10 m later while `/readyz` returns 200 degraded and the pod stays Ready (H1).
6. Follow-ups (unchanged flags): dashboard panel for the gauge pair; startup warning for non-default maxLag; `docs/configuration.md:274` wording.

---

## 10. Risks & gates

- **Stale-citation hazard:** line numbers above are the working tree as read on 2026-08-08 (HEAD `15763e2` + uncommitted); implementers re-grep (v2 §8 precedent).
- **The OR pin is mandatory:** without A4.1, CI cannot distinguish the amended rule from the shipped one — the amendment's whole value is in the arm the current parity test does not check.
- **Duplicate-instrument rejection:** the degraded gauge registers exactly once in the test binary and once in production (A2 gate) — OTel rejects duplicates on the same meter (T4 discipline).
- **Line gates:** `metrics.go` 454 → ~468; `build.go` 192 → ~197 (named fns kept, net +3 registration lines); `runtime.go` +2 log lines (D1's ~276 stands); `alerts.yml` block +2 comment lines; all < 500. `runtime_test.go` untouched (498).
- **`for` interplay:** 10 m with degraded=1 under a persistent wedge means alert latency = wedge onset + 10 m (40 evals). This is the shipped `for` value; no change proposed (a shorter `for` for the degraded arm would require rule splitting — out of scope, documented).
- **Gate:** `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` / cli-check), zero network/Docker; no new go.mod dependency beyond T3's already-promoted `go.yaml.in/yaml/v2` (E14). Optional pre-merge `promtool check rules` documented, not gated (V14).

*Verification basis: every claim above re-checked on this checkout on 2026-08-08; PromQL semantics follow the Prometheus 2.x language reference (operator precedence, comparison filtering, `or` union, `for` accumulation/reset). The v1 sibling spec's identical degraded-gauge/OR contract (V13) corroborates the design; the shipped artifact (15 rules, `for: 10m`, warning, description with `/readyz stays 200`) is the validation target.*
