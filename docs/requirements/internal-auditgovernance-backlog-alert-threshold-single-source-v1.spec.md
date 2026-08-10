# Requirements Specification — `internal/auditgovernance`: single-source the 450s alert threshold (maxLag×0.5) instead of the hardcoded literal in alerts.yml

**Module:** `internal/auditgovernance` (threshold semantics) + `internal/config` (default owner) + `cmd/server` (parity pins) + `deploy/prometheus/alerts.yml` (threshold literal) + `internal/telemetry` (gauge surface)
**Direction:** "Single-source the 450s alert threshold (maxLag×0.5) instead of the hardcoded literal in alerts.yml" (direction 2 of `docs/auto/analyses/internal-auditgovernance-ef1a62fa.json`)
**Contract:** `docs/campaigns/campaign-aero-vault-b3.yaml:7` (item 2: "Ready 解耦（H1）：maxLag 翻转 → degraded + 450s 告警；终态行排除") / `docs/proposals/audit-contract-batch-aero-vault.md` B3-2; sibling shipped spec `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md`
**Date:** 2026-08-08 · **HEAD:** `15763e2` + worktree (verification basis) · **Score:** value 6 / risk reduction 6 / effort 2 / confidence 9

---

## 1. Status statement (what exists vs. what this direction requires)

**The direction's core acceptance is already shipped in the current worktree** (uncommitted). The analysis (2026-08-07 23:38) predates the worktree changes (2026-08-08 04:00–06:16) that implemented both halves of the pairing: a Go test that reads `alerts.yml` and derives the expected expr threshold from `config.Load()`'s `MaxLagSeconds/2`, and a config-side pin asserting the shipped default is 900 — i.e. a **stronger** mechanism than the proposed "test/CI grep" (a grep asserts an equality; these tests derive the expectation from the config loader itself, so neither side can drift silently). All four evidence citations describe the *pre-ship* state and are verified here against the *current* tree.

The **only remaining delta** is a cleanup + one enforcement pin:

- **R1 — delete the dead literal.** `const alertLagThresholdSeconds = 450` (`cmd/server/readyz_drill_test.go:32-37`) is defined but **never referenced** (repo-wide grep: only its definition and its own comments at `:33` and `:381`; Go permits unused package-level consts, so it compiles silently). Its doc comment claims to "pin" the pairing, but the pin it describes is now done by the derived parity test — and the parity test's own comment at `:381` claims "the old hand-kept alertLagThresholdSeconds literal is gone — no second constant to drift", which is **false while the const remains**. It is the only executable `450` literal in Go outside `alerts.yml` — exactly what the direction's acceptance ("no second literal 450 exists") prohibits.
- **R2 — make "no second literal 450" testable.** After R1, the only remaining `450` in Go is the arbitrary scrape fixture `age := int64(450)` / `want 450` in `internal/telemetry/metrics_test.go:175-180` (a gauge-surface test value, not a threshold — any value would do). Rename it to a non-magical value (e.g. `137`) and add a scoped scan pin (a sibling of `TestAlertsYMLAuditGovernanceExprParity` in the same file) asserting no `\b450\b` token exists in Go source outside comments/strings. This closes the acceptance with zero exemptions and fails CI if a future call site reintroduces the literal.

**Shipped inventory (verified this worktree):**

| # | Shipped item | Evidence (current worktree) |
|---|---|---|
| S1 | `alerts.yml` read test: `TestAlertsYMLAuditGovernanceExprParity` — expr threshold **derived** as `cfg.AuditGovernance.MaxLagSeconds/2` from `config.Load()` (the same loader `main.go` uses), env-neutralized (`AUDIT_GOVERNANCE_ENABLED=false`, `AUDIT_GOVERNANCE_MAX_LAG_SECONDS=""` → `getEnvInt` falls back to the shipped default), so an ambient operator override can never shift what the static `alerts.yml` is compared against | `cmd/server/readyz_drill_test.go:384-445` (untracked file): `wantExpr` `:392-393` (`strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)`), `os.ReadFile("../../deploy/prometheus/alerts.yml")` `:395-398`, exactly two `expr: audit_governance_` rules file-wide `:400-404`, block-scoped assertions `:406-423` for `wantExpr`, `OR audit_governance_degraded == 1`, `for: 10m`, `severity: warning`, `/readyz stays 200` |
| S2 | Default side pinned at its owner: `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` — same env neutralization, asserts the shipped default is 900 | `internal/config/config_audit_governance_test.go:64-87` (assert `cfg.MaxLagSeconds == 900` `:82-83`); default lives at `internal/config/config_audit_governance.go:68` (`getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)`) |
| S3 | `alerts.yml` documents the derivation ("450s = maxLag default 900 × 0.5 early warning") in the rule comments and description | `deploy/prometheus/alerts.yml:176-195` — `expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` `:187`, `for: 10m`, `severity: warning`, description "/readyz stays 200" `:193` |
| S4 | Gauge scrape surface pinned (the value the alert fires on) | `internal/telemetry/metrics_test.go:171-187` (`TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape`, fixture 450 `:175-180`), `:192+` (degraded-flag 0/1 encoding) |
| S5 | Regression pins: maxLag flip still degrades `/readyz` (200, marker body) and terminal rows stay excluded from the lag gauge | `TestRuntimeReadyDegradesOnBacklogLag` (`internal/auditgovernance/runtime_test.go:618-670`), `TestReadyzBacklogLagDegradesNot503` (`cmd/server/readyz_drill_test.go:215-259`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676-699`), `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`internal/auditgovernance/runtime_ready_test.go:254-317`), `TestReadyzDeadLetteredBacklog200AndGaugeZero` (`readyz_drill_test.go:291-382`); store query predicate `delivered_at_ns=0 AND failed_at_ns=0` in `internal/repository/audit_governance_claim.go:211-225` |

**Baseline (this worktree):** `go build ./...` clean · `go vet` (auditgovernance, cmd/server, config, telemetry) clean · `go test ./internal/auditgovernance/ ./internal/config/ ./internal/telemetry/ ./cmd/server/` all ok (30.7s + 13.4s + fast). Both S1/S2 pins run green (`go test ./cmd/server/ -run TestAlertsYML...` ok; `go test ./internal/config/ -run TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold -v` PASS). `make check` covers the pins via `go test ./...` (`Makefile:18`) and CI via `go test ./...` + `-race ./internal/...` (`.github/workflows/ci.yml:85,88`) — **no Makefile/CI edit needed**. Production files all under the 500-line hard gate; the 500-line check excludes `*_test.go` (`Makefile:171-173`), so R1/R2 edits to `readyz_drill_test.go` (currently exactly 500 lines) are unblocked.

---

## 2. Evidence verification (direction citations vs. this worktree)

All four citations describe the analysis-time state. Line drift and one content amendment (the OR arm) are noted.

| # | Direction citation (analysis-time) | Verified location (current worktree) | Verdict |
|---|---|---|---|
| E1 | `deploy/prometheus/alerts.yml:163` — "expr: audit_governance_backlog_age_seconds > 450" | Rule `AuditGovernanceBacklogDegraded` `alerts.yml:186-195`; expr at `:187` — now `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` (v3 F11/F16 amendment, shipped in the worktree; description "maxLag default 900 × 0.5" `:193`; derivation comment `:179`) | ✅ **literal still present at `:187`** (it is the static source of truth the pins compare against); line drift `:163→:187` + OR-arm amendment |
| E2 | `internal/config/config_audit_governance.go:66` — "MaxLagSeconds default 900 — 450 = 900×0.5, nowhere derived in code" | `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` at `:68` (drift `:66→:68`) | ✅ **default still 900**; the "nowhere derived" claim is **obsolete** — the ×0.5 derivation is now mechanically performed in S1 (`config.Load()/2`) and pinned on the default side in S2 |
| E3 | `internal/telemetry/metrics.go:357` — "comment 'maxLag×0.5, default 450s'" | `RegisterAuditGovernanceBacklogAgeGauge` doc comment at `:364-368` ("alert at maxLag×0.5, default 450s" `:367`; drift `:357→:367`) | ✅ **still a comment only** — no code derivation here; the derivation lives in the S1 test (comment remains accurate documentation) |
| E4 | `internal/auditgovernance/runtime.go:149` — "comment 'alert threshold maxLag×0.5'" | Two comments: `PendingBacklogAge` doc "drives the degraded alert (maxLag×0.5)" `:194` and `probeAndRecord` "backlog-age gauge (alert threshold maxLag×0.5) drives operator" `:281` (drift `:149→:194/:281`) | ✅ **comments only** — runtime semantics use `maxLag` (`:283-288`) and the degraded flag, never the half-lag numeric; nothing to change in production code |

**Problem-statement checks (the direction's claims vs. current tree):**

| Statement | Verdict |
|---|---|
| "The B3-2 alert expr is a hardcoded literal `> 450` while its documented semantics is maxLag×0.5" | ✅ **still true as a static artifact** — `alerts.yml:187` must contain the literal because it is a static Prometheus rule file; the single-sourcing contract is that the literal is *pinned* to the derivation, not that the file is templated (D1) |
| "Nothing pins the pairing (no test reads alerts.yml)" | ❌ **obsolete** — `TestAlertsYMLAuditGovernanceExprParity` reads `alerts.yml` (S1) |
| "The 'maxLag×0.5, default 450s' derivation lives only in comments" | ❌ **obsolete** — derived in S1 (`config.Load()/2`, const-free) and pinned on the default side in S2; comments are now corroboration, not the only record |
| "A config-default change would violate contract item 2's '450s alert' with zero test failure" | ❌ **obsolete** — a 900→N default change fails S2 (asserts 900) and, once `alerts.yml` does not follow, S1 (expr ≠ default/2). Contract item 2 (`campaign-aero-vault-b3.yaml:7`) is now test-protected |
| "No second literal 450 exists outside alerts.yml+config default" | ⚠️ **partially** — the dead const `alertLagThresholdSeconds = 450` (`readyz_drill_test.go:37`) is the only executable literal (R1); the `metrics_test.go:175-180` fixture 450 is an arbitrary test value, not a threshold (R2 rename); all other `450` occurrences are comments (`metrics.go:367`, `runtime_test.go:617`, `readyz_drill_test.go:34`, `alerts.yml:179,184,193`) |

---

## 3. Requirements (contract + pin; all satisfied by the shipped worktree, plus R1/R2)

Each REQ states the behavior contract the direction requires and names the pin that makes it testable.

### REQ-1 — The alerts.yml expr threshold is pinned to the shipped default MaxLagSeconds/2, derived, const-free

The `AuditGovernanceBacklogDegraded` expr threshold must equal `shipped-default MaxLagSeconds / 2` (450 for 900), and the test that asserts it must **derive** the expectation from the config loader — never a hand-written literal — so either side drifting alone fails CI.

- *Pin (shipped):* `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:384-445`) — `wantExpr = "expr: audit_governance_backlog_age_seconds > " + strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)` (`:392-393`) with `cfg` from `config.Load()`; `os.ReadFile` on `deploy/prometheus/alerts.yml` (`:395-398`); block-scoped assertions include `wantExpr`, the `OR audit_governance_degraded == 1` arm, `for: 10m`, `severity: warning`, `/readyz stays 200` (`:406-423`); exactly two `expr: audit_governance_` rules file-wide (`:400-404`).
- *Pin (default side, shipped):* `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (`internal/config/config_audit_governance_test.go:64-87`) — asserts 900 through `loadAuditGovernanceConfig()` with the same env neutralization; a unilateral 900→N change fails this test and/or REQ-1's parity test, never silently.

### REQ-2 — No executable 450 literal exists outside alerts.yml

After R1, the only Go occurrences of `450` are comments and the arbitrary metrics fixture. A scoped scan pin must fail CI if a future call site reintroduces the literal (the "no second literal 450" acceptance, made testable).

- *Pin (to add, R2):* a sibling test in `cmd/server/readyz_drill_test.go` that walks `cmd/`, `internal/`, `mcp/`, `sdk/go/` `*.go` files, strips `//` comments (and string literals), and asserts no `\b450\b` token remains. Stdlib-only (`os`/`filepath`/`strings`/`regexp` — I6). Zero exemptions required **after** R2's fixture rename.
- *Cleanup (R1):* delete the dead `const alertLagThresholdSeconds = 450` and its superseded doc comment (`readyz_drill_test.go:32-37`); the parity test's comment at `:381` ("the old hand-kept alertLagThresholdSeconds literal is gone") becomes accurate. No production-code delta anywhere.

### REQ-3 — The static comparison is anchored to the shipped default, not the operator's runtime value

`AUDIT_GOVERNANCE_MAX_LAG_SECONDS` remains operator-tunable at runtime; the static `alerts.yml` literal cannot follow it, so the parity pins must compare against the **shipped default** (env-neutralized), and the config-true signal for a non-default maxLag is the `audit_governance_degraded` OR arm — documented in the v3 amendment spec and in `alerts.yml:176-182`.

- *Pins (shipped):* S1's env neutralization (`t.Setenv("AUDIT_GOVERNANCE_ENABLED","false")`, `t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS","")` — empty → `getEnvInt` falls back to the shipped default); S2's identical neutralization; the OR-arm assertion in S1 (`readyz_drill_test.go:412-423`); degraded-gauge 0/1 encoding pin (`metrics_test.go:192+`).

### REQ-4 — Regression: maxLag flip still degrades /readyz (200, never 503); gauge semantics unchanged (terminal rows excluded)

Single-sourcing must not alter the B3-2 degraded behavior or the T-3 gauge contract: a pending backlog older than maxLag degrades (Ready nil + `Degraded()==true` + age exposed), a fully dead-lettered backlog reports no pending (gauge 0), and `/readyz` stays 200 with the marker body.

- *Pins (shipped, unchanged by this direction):* `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670` — the direction's "runtime_test.go:415 pattern", relocated; the cited `:415` is the pre-ship line), `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:215-259`, deterministic 8s backdate vs. 4s maxLag = 2× margin), terminal exclusion at the store query (`audit_governance_claim.go:211-225`, predicate `delivered_at_ns=0 AND failed_at_ns=0`), `assertTerminalState` (`relay_terminal_test.go:119-128` — the cited T-3 `OldestPendingAuditGovernance ok==false` check at `:126-128`), `TestRuntimeBacklogAgeZeroWhenAllTerminal` (`runtime_ready_test.go:254-317`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`runtime_test.go:676-699`), `TestReadyzDeadLetteredBacklog200AndGaugeZero` phases 0–1 (`readyz_drill_test.go:291-382`).

---

## 4. Decisions & non-goals

- **D1 — Static alerts.yml stays; the pins are the single-sourcing mechanism.** The direction's title ("single-source ... instead of the hardcoded literal") is satisfied by *pinning the literal to a derivation* (S1 derives the expected expr from the config loader; S2 pins the default at its owner), not by templating/generating `alerts.yml` from config. A generation step would add deploy-tooling surface for no behavioral gain (effort 2 direction); it is rejected.
- **D2 — Go test beats CI grep.** The proposed "test/CI grep" is superseded by `TestAlertsYMLAuditGovernanceExprParity`, which runs under `go test ./...` (`Makefile:18`, CI `:85`) with no new grep rules, no Makefile edit, and no YAML dependency (`os.ReadFile` + `strings` + `strconv`, I6). The R2 scan follows the same pattern in the same file.
- **D3 — Env neutralization is load-bearing.** Both S1 and S2 set `AUDIT_GOVERNANCE_MAX_LAG_SECONDS=""` so the expectation is computed from the *shipped default*, not an ambient override — an operator tuning the knob cannot silently re-anchor the static alert comparison (REQ-3). Non-default maxLag is signaled by the degraded arm (`alerts.yml:187`), per the v3 amendment design.
- **D4 — R2's scan scope is Go source, comments/strings excluded, zero exemptions after the fixture rename.** `metrics_test.go:175-180`'s `450` is an arbitrary scrape fixture (any value would do); renaming it to `137` keeps the "no second literal 450" acceptance literal (no allowlist) at the cost of a 3-line mechanical test edit. The alternative — an allowlist for that fixture — is rejected as it re-creates exactly the drift channel the acceptance removes.
- **Non-goals (do not expand scope):** D1 read-path timeout/degrade (direction 1 of the same analysis — sibling spec `internal-auditgovernance-d1-read-path-timeout-degrade-v1.spec.md`, already shipped), item-5 terminal-branch matrix + `audit:event:write` CI grep (direction 3), templating `alerts.yml`, per-config (non-default) alert thresholds, alert severity/`for`/description content beyond what the pins already assert, the `AuditGovernanceEnabledUnbound` drain companion rule (`alerts.yml:197-205`), gauge naming/scrape-surface changes, config surface, migrations.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**(1)** *A test/CI grep reads alerts.yml and asserts the audit-governance expr threshold equals default MaxLagSeconds/2 (450).*
**Shipped:** `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:384-445`) — reads `deploy/prometheus/alerts.yml` and asserts the `AuditGovernanceBacklogDegraded` expr equals `strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)` derived from `config.Load()` (env-neutralized → shipped default 900 → 450), plus the OR arm, `for: 10m`, `severity: warning`, `/readyz stays 200`, and exactly two `expr: audit_governance_` rules. Run it: `go test ./cmd/server/ -run TestAlertsYMLAuditGovernanceExprParity -count=1` → PASS (verified).

**(2)** *No second literal 450 exists outside alerts.yml+config default.*
**Testable after R1+R2:** (R1) delete the dead `const alertLagThresholdSeconds = 450` (`readyz_drill_test.go:32-37`) — repo-wide grep then shows no executable `450` in Go; (R2) rename the metrics fixture `450`→`137` (`metrics_test.go:175-180`) and add the scan sibling test asserting no `\b450\b` token in Go source outside comments/strings. Remaining `450` occurrences are then only: `alerts.yml` (the source, `:179,184,187,193`) and comments (`metrics.go:367`, `runtime_test.go:617`, `readyz_drill_test.go:34`).

**(3)** *D1 drill acceptance: changing MaxLagSeconds in the test config must shift the alert threshold derivation consistently, keeping /readyz 200-degraded behavior (runtime_test.go:415) and gauge semantics unchanged (T-3 terminal exclusion intact).*
**Testable, shipped:** "shifts consistently" = the parity expectation is *computed* from the config default at test time, so a default edit (`config_audit_governance.go:68`, 900→N) fails S2 (asserts 900) **and** S1 (expr ≠ N/2 until `alerts.yml` follows); an `alerts.yml` literal edit fails S1. The drill's test-local config (`drillRuntimeConfig`, `MaxLagSeconds: 4`) is deliberately not consulted by S1 (env neutralization, D3) — the alert stays anchored to the shipped default while the drill exercises its own seam behavior: `/readyz` 200-degraded kept — `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618-670`, the relocated `:415` pattern) + `TestReadyzBacklogLagDegradesNot503` (`readyz_drill_test.go:215-259`); gauge semantics unchanged — terminal rows still excluded by `OldestPendingAuditGovernance` (`audit_governance_claim.go:211-225`), pinned by `TestRuntimeBacklogAgeZeroWhenAllTerminal`/`WhenNoPending` and `TestReadyzDeadLetteredBacklog200AndGaugeZero` (REQ-4).

---

## 6. Risks & gates

- **Pin-drift risk (low):** the pins live in `cmd/server` + `internal/config` + `internal/telemetry`; `make check` (`gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`, 500-line production check) covers them all. R1/R2 touch only `*_test.go` (excluded from the 500-line gate, `Makefile:171-173`).
- **R2 scan brittleness (mitigated):** scoped to Go files with comment/string stripping and a single token regex (`\b450\b`); zero exemptions after the fixture rename, so there is no allowlist to drift. If a legitimate future `450` appears (e.g., a new unrelated timeout), the scan forces an explicit decision — which is the point of the acceptance.
- **No production delta:** R1/R2 are test-file-only; the direction's core acceptance was already shipped in the worktree (S1/S2). The implement stage is verification + the two small test edits.
- **Worktree note:** the shipped pins are part of the uncommitted worktree (HEAD `15763e2` + changes; `readyz_drill_test.go` is untracked). Nothing in this direction depends on commit boundaries, but R1/R2 should land in the same changeset as the rest of the audit-governance worktree to keep the gate green.

*Verification basis: all citations re-checked on this worktree (HEAD `15763e2` + uncommitted changes); line numbers reflect the tree as read during this spec's production. Full evidence chain in §2.*
