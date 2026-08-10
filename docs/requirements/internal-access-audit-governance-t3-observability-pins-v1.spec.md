# Requirements Specification — `internal/access` (analysis label): T-3 gauge half + observability instruments — dead-only backlog → gauge 0 runtime pin, backlog-age scrape-surface pin, YAML-parsed 450s alert-rule pin

**Module:** `internal/access` (analysis label; implementation surface is `internal/auditgovernance` + `internal/telemetry` + `cmd/server` + `deploy/prometheus` — the governance pipeline the access module feeds; `internal/access/` itself emits zero audit events, see §1)
**Direction:** "Pin the T-3 gauge half and the observability instruments: dead-only backlog → gauge 0 runtime pin, backlog-age scrape-surface pin, and YAML-parsed 450s alert-rule pin"
**Source analysis:** `docs/auto/analyses/internal-access-f4571c58.json`
**Contract:** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.spec.md` §3 REQ-6 (T3/T4/T5 pins) and §2 E7 (the acceptance grep "must be pinned as a YAML parse (§5 AC-2), not a raw grep")
**Date:** 2026-08-08 · **HEAD:** working tree as read during this spec's production
**Score:** value 8 / risk reduction 7 / effort 3 / confidence 9

---

## 1. Module & scope

The analysis labels this direction under `internal/access`. As the analysis itself confirms, `internal/access/` (PDP: ACLs/shares/departments/publish) contains **zero audit emission** — `grep RecordAudit/InsertEvent internal/access/` → no hits — so its B3 surface is entirely the governance pipeline it feeds: the audit-governance relay's dead-letter terminality (T-3) and its observability instruments (backlog-age gauge + degraded alert). The actual implementation surface is:

- `internal/auditgovernance/runtime.go` (`BacklogAge`), `runtime_test.go` (test seams),
- `internal/telemetry/metrics.go` (`RegisterAuditGovernanceBacklogAgeGauge`), `metrics_test.go` (scrape idiom),
- `cmd/server/build.go` (gauge callback `auditGovernanceBacklogAgeGaugeFn`), `cmd/server/readyz_drill_test.go` (existing seam pin + alerts.yml grep pin),
- `deploy/prometheus/alerts.yml` (the shipped alert rule),
- `internal/repository/audit_governance_claim.go` + `audit_governance_test.go` (repo-level terminal pin — already shipped).

**State at verification time (critical):** the direction's problem statement is **partially stale in two places**, both corrected in §2 (E2, E4). Verified inventory:

| # | Item | Status on this checkout |
|---|------|------------------------|
| S1 | Dead-row exclusion from claim/lag: `OldestPendingAuditGovernance` / `HasPendingDrainingAuditGovernance` filter `failed_at_ns=0` | ✅ **implemented** (`audit_governance_claim.go:188-196`, `:202-208`) |
| S2 | `FailAuditGovernance` lands terminal `failed_at_ns` (never re-claimed, retention-pruned) | ✅ **implemented** (`audit_governance_claim.go:155-173`; repo pin `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`, `audit_governance_test.go:519`) |
| S3 | Gauge `audit_governance.backlog_age_seconds` registered | ✅ **implemented** (`metrics.go:354-360`; wired `build.go:98-105,127` — callback maps `err != nil \|\| !ok` → `0`) |
| S4 | Alert `AuditGovernanceBacklogDegraded`, expr `audit_governance_backlog_age_seconds > 450`, `for: 10m` | ✅ **implemented** (`deploy/prometheus/alerts.yml:162-169`) |
| S5 | Runtime-layer T-3 pin: dead-only backlog → `BacklogAge()` ok=false | ❌ **missing** — `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:473-497`) covers the **empty store** only; no dead-row arm, no same-age pending control |
| S6 | Gauge scrape-surface pin for `audit_governance.backlog_age_seconds` | ❌ **missing** — `TestObservableGauges_SurfaceInScrape` (`metrics_test.go:114-135`) covers `jobs_pending`/`storage_bytes`/`storage_objects` only |
| S7 | Alert-rule pin in the **structural (YAML-parse) form** the spec mandates | ⚠️ **half — grep form only** — `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:336-359`) substring-greps name marker + expr (450 via const) + severity + "/readyz stays 200"; **`for: 10m` and the description's `maxLag×0.5` cross-reference are unpinned** |
| S8 | Seam pin: dead-lettered backlog → gauge 0, `/readyz` 200, live-row control | ✅ **implemented** — `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:262-322`) via claim+Fail → `BacklogAge` ok=false, `gauge==0`, 200; 2s-backdated live row → gauge ≥ 2 |

**In scope:** the three pins only — S5 (runtime layer, REQ-1), S6 (telemetry scrape surface, REQ-2), S7 (structural alert-rule pin, REQ-3). **Out of scope:** the sibling direction 1 of the same analysis (Degraded() sentinel + `/readyz` degraded payload + store-probe timeout — `internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.spec.md` S6-S8), the sibling direction 3 (test-race-meta Makefile gate), B3-1 permanent-error classification, any production-code change (this direction is **test-only**, zero production footprint), any alerts.yml/config/metric content change, any new instrument, and the existing S1/S2/S4/S8 pins (preserve, don't duplicate).

---

## 2. Evidence verification

Every direction citation was checked against the current working tree.

| # | Direction citation | Verified location (current tree) | Verdict |
|---|---|---|---|
| E1 | "`TestRuntimeBacklogAgeZeroWhenNoPending` (runtime_test.go:470-497) covers only the empty store" | Comment `:470`, func `:473`, ends `:497`. Body: `InsertEventWithGovernance` never called; only the empty-store arm (`BacklogAge` ok=false, `Ready` nil). No dead-row, no pending-control arm. File is **498/500 lines** — new runtime tests must go in a new file | ✅ **holds exactly** (S5). |
| E2 | "a fully dead-lettered backlog reporting ok=false → gauge 0 … is unverified at the Runtime layer" | ✅ **holds for the Runtime layer** — no `internal/auditgovernance` test lands a `FailAuditGovernance` and asserts `BacklogAge`. ⚠️ **correction:** the seam **is** pinned — `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:262-322`, claim+Fail at `:292-304`, `BacklogAge ok==false` `:305-307`, `gauge==0` `:308-310`, live-row control `:312-322`) drives the **real** callback (`auditGovernanceBacklogAgeGaugeFn`, `build.go:98-105`). The missing piece is exactly the Runtime-layer input pin + same-age control | ✅/⚠️ **holds with the seam correction** (S5 vs S8). |
| E3 | "`OldestPendingAuditGovernance`/`HasPendingDrainingAuditGovernance` filter `failed_at_ns=0`; `FailAuditGovernance`" | `audit_governance_claim.go`: `OldestPendingAuditGovernance` `:188-196` (predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` `:195`); `HasPendingDrainingAuditGovernance` `:202-208` (same exclusion + `b.state='draining'`); `FailAuditGovernance` `:155-173` (terminal `failed_at_ns`, lease-fenced, never re-claimed). Interface: `repository.AuditGovernanceStore` (`audit_governance_types.go:88-103`) embeds all of them; `Runtime.Store` = that interface (`runtime.go:18-20`) | ✅ **holds** (S1/S2). |
| E4 | "`TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` — repo-level pin exists" | `internal/repository/audit_governance_test.go:519` (claim+fail → terminal, never re-claimed, `OldestPending` ok=false, retention prune) | ✅ **holds** (S2). |
| E5 | "`audit_governance.backlog_age_seconds` gauge (`metrics.go:354-360`) has no scrape-surface assertion" | `RegisterAuditGovernanceBacklogAgeGauge` `metrics.go:354-360` (`Int64ObservableGauge("audit_governance.backlog_age_seconds")`); `grep audit_governance_backlog_age_seconds --include='*_test.go'` → **zero hits**; `TestObservableGauges_SurfaceInScrape` (`metrics_test.go:114-135`) covers only `jobs_pending`/`storage_bytes`/`storage_objects`. Line-exact idiom `scrapeValue` at `metrics_test.go:60-76`; counter pins assert exact value 1 (`:82-108`) | ✅ **holds exactly** (S6). |
| E6 | "alert rule (`alerts.yml:162-169`, expr `> 450`, `for: 10m`) is unpinned — the expr is not text-grep-matchable (spec E7), so threshold/expr drift would sail through G4 silently" | Rule at `alerts.yml:162-169` (name `:162`, expr `:163`, `for: 10m` `:164`, description with `"> 450s = maxLag×0.5"` `:169`). ⚠️ **partially stale — corrected:** `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:336-359`) now grep-pins the name marker, the expr with the 450 threshold via `alertLagThresholdSeconds` const (`:33`), `severity: warning`, and "/readyz stays 200" — **threshold drift 450→500 would fail CI today**. What is genuinely unpinned: **`for: 10m`** (never asserted), the **description's `maxLag×0.5` cross-reference**, and the **structural form** the spec mandates — the current pin is `strings.Contains` on a block-scope-to-EOF slice (`:343-357`), which cannot detect a duplicated/relocated rule, a `for:` value change, or expr reformatting; the E7 note ("must be pinned as a YAML parse, not a raw grep") stands | ⚠️ **partially stale — the expr threshold is pinned (const-grep); the delta is `for:`, description cross-ref, and the YAML-parse form** (S7). |
| E7 | "`MaxLagSeconds` default 900 → 450 = 0.5×" | `internal/config/config_audit_governance.go:66` — `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)`; validation `> ClaimTTLSeconds` `:249`, `<= 604_800` `:259` | ✅ **holds** (S4 baseline). |
| E8 | Claim→fail test idiom availability | `ClaimAuditGovernance(ctx, "worker", "token", 1, 10, time.Minute)` + `FailAuditGovernance(ctx, id, "worker", "token", "dead")` — `cumulative_window_test.go:257`, `fact_id_test.go:55`, `readyz_drill_test.go:292-304`; `New()` applies the configured acme binding revision 1 internally (`runtime_test.go:415-467` comment); `runtimeConfig` helper `runtime_test.go:40-46`; `InsertEventWithGovernance` `audit_governance_write.go:53` | ✅ **holds** — REQ-1 directly implementable with the in-package idiom. |
| E9 | "cf. `internal/telemetry/metrics_test.go` counter pins" (line-exact idiom) | `scrapeValue` `:60-76`; `TestAuditGovernanceMetrics_SurfaceInScrape` `:82-108` (four relay counters, exact value 1); single-`EnablePrometheus` TestMain `main_test.go:8-16` (shared handler, OTel no-op mode) | ✅ **holds** — REQ-2 reuses the idiom. |
| E10 | I6 stdlib constraint for the YAML parse | `go.mod` has `go.yaml.in/yaml/v2` **indirect only**; existing pin's doc comment explicitly: "Stdlib-only (os.ReadFile + strings, I6 — no YAML dependency promotion)" (`readyz_drill_test.go:333-335`) | ✅ **holds** — REQ-3 defaults to a stdlib structural parse (indentation-scoped key:value extraction), strictly stronger than the current grep; a real YAML parser is permitted only with the I6 dependency justification, not required |

**Problem-statement checks (current tree):** ① "runtime/metrics half of T-3 is unpinned" — ✅ holds (S5: empty-store-only; S6: no scrape-surface pin). ② "fully dead-lettered backlog … unverified at the Runtime layer" — ✅ holds narrowly (S8 seam pin exists; Runtime-layer pin does not). ③ "threshold/expr drift would sail through G4 silently" — ❌ **stale** (E6 correction): the const-pinned expr grep already fails on threshold drift; the unpinned surface is `for: 10m`, the description cross-reference, and the structural form.

---

## 3. Requirements

All requirements are **test-only** (zero production-code footprint). Hard gates: `runtime_test.go` is 498/500 lines → all new runtime-layer tests go in a **new file** `internal/auditgovernance/runtime_ready_test.go` (same package, reuses `runtimeConfig` and the `repo.(Store)` idiom); `metrics_test.go` 135 + ~45 = OK; `readyz_drill_test.go` 357 + ~60 = OK.

### REQ-1 — Runtime-layer T-3 pin: fully dead-lettered backlog → `BacklogAge()` ok=false, with a same-age pending control (S5)

New test **`TestRuntimeBacklogAgeZeroWhenAllTerminal`** in `internal/auditgovernance/runtime_ready_test.go`, mirroring the `TestRuntimeReadyDegradesOnBacklogLag` setup (`runtime_test.go:415-467`: sqlite `file:` tempdir, `Migrate`, `New(runtimeConfig("http://127.0.0.1:1"), store, discardLogger)` — loopback base URL, relay never started):

1. **Same-age pair, no sleeps:** seed fact A and fact B via `store.InsertEventWithGovernance(ctx, repository.Event{...}, repository.AuditGovernanceFact{...})` with **identical** `CreatedAt`/`OccurredAt` (`time.Now().UTC()` captured once, reused for both; distinct `Key`s so the rows differ). Both rows are pending and equally fresh — any age the dead row *would* have reported equals B's age.
2. **Terminalize A via the lease-fenced public API:** `store.ClaimAuditGovernance(ctx, "acme", "tok", 1, 10, time.Minute)` returns both rows (revision 1 = the binding `New()` applied); `store.FailAuditGovernance(ctx, a.ID, "acme", "tok", "conflict:true")`.
3. **Pending control (acceptance's "while a same-age pending row shows >0"):** `runtime.BacklogAge(ctx)` → `ok==true`, `age > 0` — B (same age as dead A) drives the age; `runtime.Ready(ctx)` → nil.
4. **Dead-only (acceptance's "gauge 0 / never triggers the 450s alert"):** `store.FailAuditGovernance(ctx, b.ID, "acme", "tok", "conflict:true")`; now `runtime.BacklogAge(ctx)` → `(0, false, nil)` — the fully dead backlog reports ok=false, the exact input the gauge callback maps to 0 (`build.go:98-105`); `runtime.Ready(ctx)` → nil (dead-only never blocks readiness, B3-1 interplay preserved from `TestRuntimeBacklogAgeZeroWhenNoPending`).
5. **Sub-assertion:** `store.OldestPendingAuditGovernance(ctx)` → ok=false after step 4 (runtime-level re-pin of the repo contract, `claim.go:188-196`).

Determinism: no sleeps, no backdating — both rows share the captured timestamp; `age > 0` holds for any nonzero elapsed; assertions are on the `ok` flag and a strict-`>` bound only (the existing `runtime_test.go:470-497` idiom).

### REQ-2 — Scrape-surface pin for `audit_governance.backlog_age_seconds` (S6)

New test **`TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`** in `internal/telemetry/metrics_test.go`, following the `TestObservableGauges_SurfaceInScrape` pattern (`:114-135`) and the line-exact `scrapeValue` idiom (`:60-76`):

1. **Single-shot registration** (OTel rejects duplicate instruments; the telemetry test binary registers this gauge exactly once — TestMain's shared handler, `main_test.go:8-16`): `RegisterAuditGovernanceBacklogAgeGauge(fn)` where `fn` reads a captured `atomic.Int64`.
2. Set the captured value to `450`; scrape via `sharedPromHandler`; assert `scrapeValue(body, "audit_governance_backlog_age_seconds") == (450, true)` — surface pin with the exact exported name (`dots → underscores`, no `_total`) **and** value path, matching the counter pins' exact-value style (`:82-108`).
3. Set the captured value to `0` (the dead-only/empty shape the gauge callback emits for `ok=false`); re-scrape; assert `scrapeValue(body, "audit_governance_backlog_age_seconds") == (0, true)` — the acceptance's "gauge scrape value 0" at the surface.

The runtime-shaped half (real callback + real dead rows → 0, live row → >0) is already pinned at the seam by `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:262-322`) — REQ-2 is the instrument-surface half, not a duplicate of the seam drill.

### REQ-3 — Structural (YAML-parsed) alert-rule pin (S7)

Upgrade **`TestAlertsYMLAuditGovernanceExprParity`** in place (`cmd/server/readyz_drill_test.go:336-359`) from substring-grep to a **structural rule-block parse** of `deploy/prometheus/alerts.yml` (package-relative `../../deploy/prometheus/alerts.yml`, as today). Default implementation is stdlib-only per I6 (indentation-scoped `key: value` extraction — a minimal YAML-subset parse; a real YAML parser is permitted only with the I6 dependency justification). The parse must:

1. Locate the group `aero-vault-audit-governance` and, inside its `rules`, the rule with **`alert:` == `AuditGovernanceBacklogDegraded`** (exact value equality — rejects a renamed/duplicated rule, which the current block-scope grep cannot).
2. Assert **`expr:` == `audit_governance_backlog_age_seconds > 450`** (exact string; threshold DERIVED from `config.Load().AuditGovernance.MaxLagSeconds/2` with `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` env-neutralized — the `alertLagThresholdSeconds` const is deleted (post-review decision); mechanical 900×0.5 default coupling, E7).
3. Assert **`for:` == `10m`** (exact — **new**, currently unpinned anywhere).
4. Assert `severity:` == `warning` (preserved from the current pin).
5. Assert `annotations.description` **cross-references maxLag×0.5** (contains `maxLag×0.5` — **new**; the shipped description at `alerts.yml:169` reads "(> 450s = maxLag×0.5)…"). The existing "/readyz stays 200" description assertion is preserved.

Recommended (not required): assert no other rule in the file references `audit_governance_` in any `expr:` (spec T3's both-ways drift guard). This upgrade keeps a **single** alerts.yml pin in the tree (spec T3's alternative location `internal/telemetry/metrics_test.go` is acceptable but would duplicate the artifact walk — see §4 D2).

---

## 4. Decisions & non-goals

- **D1 — Test-only direction, zero production footprint.** No changes to `runtime.go`, `metrics.go`, `build.go`, `alerts.yml`, config, migrations, or go.mod (I6). The shipped semantics (S1-S4) are preserved as-is; only pins are added/upgraded.
- **D2 — Pin placement.** Runtime-layer pins go in the **new** file `internal/auditgovernance/runtime_ready_test.go` (hard gate: `runtime_test.go` at 498/500 lines — mandatory, per the sibling spec's REQ-6 precedent). The alerts.yml pin is upgraded **in place** at `cmd/server/readyz_drill_test.go` (single artifact pin; same package-relative path already in use). The telemetry surface pin extends `metrics_test.go` (135 + ~45 = OK).
- **D3 — No duplication of the seam pin.** The acceptance's "gauge scrape value 0" is decomposed: the runtime-shaped assertion (real callback + dead rows → 0) already exists (`TestReadyzDeadLetteredBacklog200AndGaugeZero`); REQ-1 adds the Runtime-layer input pin, REQ-2 adds the instrument-surface assertion.
- **D4 — Same-age control without sleeps.** Both rows share one captured timestamp, so the pending control is exactly the dead row's would-be age; assertions use the `ok` flag + strict-`>` bound only (no wall-clock equality, no backdating, no second connection — simpler than the drill test's `UPDATE created_at_ns` idiom and equally deterministic for this shape).
- **D5 — Structural parse, not grep.** The E7 note and spec T3 are honored: the alert pin must be a structural key:value parse with exact-equality assertions, stdlib-first (I6); the current `strings.Contains` pin is the baseline the upgrade replaces.
- **Non-goals:** direction 1 of the same analysis (`Degraded()` sentinel, `/readyz` degraded payload, store-probe timeout — sibling spec S6-S8), direction 3 (test-race-meta Makefile gate), B3-1 permanent-error classification, any production behavior change, any new metric or alert, the counter pins (shipped, `metrics_test.go:82-108`), and the empty-store arm of `TestRuntimeBacklogAgeZeroWhenNoPending` (preserved).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (T-3 dead-only runtime pin) —** *"seed one fact via `InsertEventWithGovernance`, terminally fail it (`FailAuditGovernance`), assert `BacklogAge()==ok:false` and gauge scrape value 0 while a same-age pending row shows >0 — fully dead backlog never triggers the 450s alert."*
*Testable:* **REQ-1** (T: `TestRuntimeBacklogAgeZeroWhenAllTerminal`) + **REQ-2** (T: value-0 scrape arm) + existing seam pin `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:262-322`, real callback → gauge 0, `/readyz` 200). Sequence: same-age pair A/B → fail A → `BacklogAge` ok=true, age>0 (pending control); fail B → `BacklogAge` `(0,false,nil)` (gauge input 0) ∧ `OldestPending` ok=false ∧ `Ready()==nil`. Since the 450s alert expr is fed only by the gauge (`alerts.yml:163`), a fully dead backlog provably never fires it.

**AC-2 (scrape-surface pin) —** *"telemetry test asserts `audit_governance.backlog_age_seconds` surfaces in a scrape (line-exact parse idiom, cf. `internal/telemetry/metrics_test.go` counter pins)."*
*Testable:* **REQ-2** (T: `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`) — single-shot registration with a captured callback; scrape asserts `scrapeValue(body, "audit_governance_backlog_age_seconds") == (450, true)` then `== (0, true)` after flipping the captured value; line-exact parse, no substring matching (`metrics_test.go:60-76`).

**AC-3 (alert-rule pin) —** *"test YAML-parses `deploy/prometheus/alerts.yml` and asserts rule name == `AuditGovernanceBacklogDegraded`, expr == `'audit_governance_backlog_age_seconds > 450'`, for == 10m, description cross-references maxLag×0.5."*
*Testable:* **REQ-3** (T: upgraded `TestAlertsYMLAuditGovernanceExprParity`) — structural key:value parse (stdlib, I6) with exact-equality on `alert:`/`expr:`/`for:`/`severity:` and `description` containing `maxLag×0.5`; threshold derived from `config.Load().AuditGovernance.MaxLagSeconds/2` (env-neutralized; = 450 while the shipped default is 900 — the `alertLagThresholdSeconds` const is deleted; the default side is dual-anchored by `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` in `internal/config`). Drift of the rule name, expr, threshold, `for:` duration, or the description cross-reference fails `make check`.

---

## 6. Risks

- **Duplicated-instrument panic** — OTel rejects re-registration of `audit_governance.backlog_age_seconds`; REQ-2 must be the single registration in the telemetry test binary (TestMain shared handler, `main_test.go:8-16`), mirroring `TestObservableGauges_SurfaceInScrape`.
- **Hard gates** — `runtime_test.go` at 498/500 lines forces the new `runtime_ready_test.go` (mandatory); `metrics_test.go` 135 + ~45 = OK; `readyz_drill_test.go` 357 + ~60 = OK; no file exceeds 500 lines; `make check` (gofmt/build/vet/test) is the gate.
- **Timing flake** — none by construction: shared captured timestamp, `ok`-flag + strict-`>` assertions, no sleeps/backdating (D4); the relay is never started, so `Close()` is non-blocking (drill-test precedent).
- **Stale-claim drift** — the direction's "threshold/expr drift would sail through G4 silently" is corrected (E6): the expr grep already catches threshold drift; the delta shipped by this spec is `for: 10m`, the description cross-reference, and the structural form. REQ-3 derives the threshold from the config default (`MaxLagSeconds/2`, const deleted) so config-default drift (900→1800) fails CI — dual-anchored with the config-package mirror pin `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`.
- **Scope bleed** — the sibling direction 1 (Degraded sentinel / degraded `/readyz` payload) is deliberately not touched; REQ-1's `Ready()==nil` assertions preserve the current degraded-on-lag semantics (`runtime.go:170-181`) without adding a sentinel.

*Verification basis: every citation re-checked on the working tree at spec-production time; line numbers reflect the tree as read.*
