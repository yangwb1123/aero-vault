# Requirements Specification — `internal/config`: derive the 450s degraded-alert threshold from config (maxLag×0.5 accessor + parity pins)

**Direction:** "Derive the 450s degraded-alert threshold from config instead of the hardcoded alerts.yml literal" (direction 1 of `docs/auto/analyses/internal-config-a932ee1e.json`)

**Contract mapping:** B3-2 degraded-alert contract (`cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-5/REQ-5.2) · D3 (config is the single source of timing truth) · `docs/campaigns/implementation-gate.md:25` item 5 (grep-consistency check green) · T-3 preserved (`TestRuntimeReadyDegradesOnBacklogLag`)

## Why this direction exists (verified)

B3-2's degraded-alert contract is `maxLag×0.5`: the alert's age arm fires at **450s = the shipped `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` default (900) × 0.5**, while `Runtime.Ready()`'s warn fires at `maxLag` itself. `internal/config` is the declared single source of timing truth (D3), yet the ×0.5 relationship has **no representation in `internal/config`**: `loadAuditGovernanceConfig` defaults `MaxLagSeconds=900` (`config_audit_governance.go:68`), validation accepts any `MaxLagSeconds ∈ (ClaimTTLSeconds, 604800]` (`:265`, `:275`), and no accessor expresses the alert threshold.

**Key verified finding — the analysis's premise is partially stale.** The direction states "there is no accessor/test pinning the 450s relationship". That was true at analysis time (mtime Aug 7 23:13); the worktree has since shipped the pins — but **outside `internal/config`**:

- `cmd/server/readyz_drill_test.go:381` `TestAlertsYMLAuditGovernanceExprParity` asserts the shipped alerts.yml expr threshold equals `config.Load().AuditGovernance.MaxLagSeconds/2` — the ×0.5 arithmetic lives **inline in a cmd/server test**, not in config.
- `cmd/server/readyz_drill_test.go:540` `TestNoExecutable450LiteralOutsideAlertsYml` bans executable `450` literals outside alerts.yml.
- `internal/config/config_audit_governance_test.go:64` `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` pins the default side (900) through the same loader.

So the alert is coupled to operator config **by test only, via inline arithmetic in another package** — config still cannot state or be tested for the threshold it owns. The net-new work is exactly acceptance (a): a config accessor plus unit tests, with the existing parity pin re-routed through it so the accessor becomes the **single** ×0.5 derivation site. There is no production-behavior delta beyond the accessor; this is a regression/coupling contract.

## Evidence verification (every citation checked against the working tree)

| # | Citation (analysis) | Verified state |
|---|---|---|
| E1 | `deploy/prometheus/alerts.yml:163` expr `> 450`, 'maxLag×0.5' comment | ✅ present, line drifted: comment `:177-178` ("450s = maxLag default 900 × 0.5 early warning"), expr `:187` `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` (v3 F11/F16 OR-arm amendment), rule `AuditGovernanceBacklogDegraded`, `for: 10m`, `severity: warning`, description "maxLag default 900 × 0.5" |
| E2 | `config_audit_governance.go` MaxLagSeconds default 900; `boundedAuditGovernanceTiming` accepts up to 604800 | ✅ exact: `getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` `:68`; `MaxLagSeconds > ClaimTTLSeconds` `:265`; `MaxLagSeconds <= 604_800` `:275`; **no threshold accessor exists** (`grep -rn "AlertThreshold\|/ 2" internal/config/*.go` → only the test name) |
| E3 | `runtime.go:146-152` BacklogAge docstring "drives the degraded alert (maxLag×0.5)" | ✅ present, line drifted: `PendingBacklogAge` docstring `:191-197` ("feeds the audit_governance_backlog_age_seconds gauge that drives the degraded alert (maxLag×0.5)") |
| E4 | `runtime.go:174-183` Ready() warns only when age > maxLag | ✅ present, line drifted: `probeAndRecord` `:270-288` — `age > r.maxLag` → warn "audit governance relay degraded" + `recordDegraded(true, age)` + return nil (readyz 200); `Ready()` `:293` |
| E5 | `config_audit_governance_test.go` pins D3 defaults; no alert-threshold pin | ⚠️ partially stale: `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` `:64-89` pins default 900 via the loader (env-neutralized) and documents the cmd/server parity test as the derivation site — but the **accessor still does not exist** and no config test asserts `900 → 450` / `1800 → 900` |

## Verified current state (green baseline, do not regress)

- `TestAlertsYMLAuditGovernanceExprParity` (`cmd/server/readyz_drill_test.go:366-419`): derives `wantExpr` from `config.Load()` (ENABLED=false, `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` neutralized) as `"expr: audit_governance_backlog_age_seconds > " + strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)`; asserts the expr, `OR audit_governance_degraded == 1`, `for: 10m`, `severity: warning`, `/readyz stays 200`, and exactly 2 `expr: audit_governance_` lines file-wide. Stdlib-only (I6).
- `TestNoExecutable450LiteralOutsideAlertsYml` (`cmd/server/readyz_drill_test.go:540-583`): `\b450\b` scan over `cmd`, `internal`, `sdk/go` `.go` files with comments/strings stripped — an executable 450 may exist only inside alerts.yml.
- `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (`internal/config/config_audit_governance_test.go:64`): shipped default 900 through the loader, env-neutralized.
- `TestRuntimeReadyDegradesOnBacklogLag` (`internal/auditgovernance/runtime_test.go:618`, harness `runtimeConfig` `:41-49` with `MaxLagSeconds=4`): backlog beyond maxLag → `Ready()` nil (degraded), `BacklogAge()` exposes the age.

## Requirements

### REQ-1 — Config exposes the derived alert threshold (accessor, net-new)

`internal/config/config_audit_governance.go`, on `AuditGovernanceConfig`:

```go
// BacklogAlertThresholdSeconds returns the age-arm threshold of the
// AuditGovernanceBacklogDegraded alert: maxLag×0.5, floored — the shipped
// default 900s → 450s (deploy/prometheus/alerts.yml expr). The alert's
// degraded==1 arm remains the config-true signal for any non-default
// maxLag; this accessor is what the alerts.yml parity pin derives from,
// so an operator override of AUDIT_GOVERNANCE_MAX_LAG_SECONDS stays
// coupled to the alert threshold. Value receiver; zero I/O.
func (c AuditGovernanceConfig) BacklogAlertThresholdSeconds() int {
	return c.MaxLagSeconds / 2
}
```

- Floor semantics are deliberate and match Go integer division (odd `MaxLagSeconds` floors; the shipped default 900 is exact).
- Every valid config keeps the ordering `threshold < maxLag`: validation implies `MaxLagSeconds > ClaimTTLSeconds > 2×HTTPTimeoutSeconds ≥ 2` (`:265`, `validAuditGovernanceWorker`), so `MaxLagSeconds ≥ 4` → `MaxLagSeconds/2 < MaxLagSeconds` — the alert age arm always fires before the Ready() warn.
- **No validation-bounds change** (the `604_800` cap and `> ClaimTTLSeconds` relation stay untouched).

### REQ-2 — Unit test pins the derivation (acceptance a)

`internal/config/config_audit_governance_test.go`, new `TestAuditGovernanceBacklogAlertThresholdDerived`. **Every expectation must be written as derived arithmetic — `900/2`, `1800/2`, `901/2` — never as a literal `450` want-value.** `TestNoExecutable450LiteralOutsideAlertsYml` (`cmd/server/readyz_drill_test.go:540`) strips comments and string literals and bans any executable `450` token outside alerts.yml; a literal `450` in the new test's expectations would trip the exact FM5 pin this contract relies on (findings F1/H1 — the ban *forces* derivation, reinforcing the single-×0.5-site invariant at test level too). Go integer division floors, so `901/2 == 450` pins floor semantics with no executable literal; `1800/2` (rather than bare `900`, which is not banned) keeps the table uniform and self-documenting.

1. **Shipped default (loader path):** `t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")` + `t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "")` (empty → `getEnvInt` falls back), `loadAuditGovernanceConfig()` → `cfg.BacklogAlertThresholdSeconds() == 900/2` — mirroring `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`. A default drift 900→N fails this case via the accessor, forcing the alerts.yml parity update (FM5/FM6).
2. **Non-default (struct form):** `cfg := validAuditGovernanceConfig(); cfg.MaxLagSeconds = 1800` → `cfg.BacklogAlertThresholdSeconds() == 1800/2`. A hardcoded (field-ignoring) accessor fails this case.
3. **Odd-value floor (struct form):** `cfg.MaxLagSeconds = 901` → `cfg.BacklogAlertThresholdSeconds() == 901/2` (= 450, floored). **Necessary:** the 1800 case cannot distinguish floor from ceil (both give 900); a ceil `(n+1)/2` implementation → 451 ≠ 450 fails here. Floor is the safe direction for a warning alert: 450 < 901, so the age arm still fires before the Ready() warn (FM2).
4. **Ordering hardening (struct form):** for the three valid inputs {900, 1800, 901}, assert `cfg.BacklogAlertThresholdSeconds() < cfg.MaxLagSeconds` — REQ-1's ordering invariant at test level (the age arm always fires before the Ready() warn). It holds for every *valid* config via the validation chain (`ClaimTTLSeconds > 2×HTTPTimeoutSeconds ≥ 2` ∧ `MaxLagSeconds > ClaimTTLSeconds` → `MaxLagSeconds ≥ 4` → `MaxLagSeconds/2 < MaxLagSeconds`). **[F3 resolution:** the design's "optional `threshold < maxLag` hardening assertion" is promoted to a required case here — this spec is authoritative over the design's "optional" wording — scoped to the three valid cases only.]
5. **Zero-value (FM7 pin):** `(AuditGovernanceConfig{}).BacklogAlertThresholdSeconds() == 0` — no panic, deterministic. The zero value is not a valid config (fails `Validate()`), so this case pins misuse-boundedness only; the sole production consumer is the parity test, which uses validated `config.Load()`. The `0` literal is ban-safe (`\b450\b` never matches). **[F2 resolution:** the design's FM7 zero-value branch, previously unpinned, is pinned here.]

The default case must be loader-based (same path the relay consumes); the non-default, floor, ordering, and zero-value cases use struct form.

### REQ-3 — Parity pin consumes the accessor (acceptance b; contract item 5 at the config boundary)

`cmd/server/readyz_drill_test.go:388-389` `TestAlertsYMLAuditGovernanceExprParity`: replace the inline arithmetic

```go
wantExpr := "expr: audit_governance_backlog_age_seconds > " +
	strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)
```

with the accessor:

```go
wantExpr := "expr: audit_governance_backlog_age_seconds > " +
	strconv.Itoa(cfg.AuditGovernance.BacklogAlertThresholdSeconds())
```

- All other assertions unchanged (OR arm, `for: 10m`, `severity: warning`, `/readyz stays 200`, exactly-2 exprs, stdlib-only). `strconv` import stays (still used by the `Itoa`).
- `TestNoExecutable450LiteralOutsideAlertsYml` is **untouched** and must stay green — no executable 450 appears in config (the accessor computes it).
- Result: exactly one ×0.5 derivation site (the config accessor); alerts.yml's `> 450` literal is coupled to it by the parity test; `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` anchors the default side (900) at its owner.

### REQ-4 — Ready()/alert ordering regression preserved (acceptance c)

No change to `internal/auditgovernance/runtime.go`. `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:618`, `MaxLagSeconds=4`) must stay green: backlog beyond maxLag → `Ready()` nil (degraded, `/readyz` 200) — the warn fires at maxLag while the derived threshold (`maxLag/2`) fires earlier, which is the alert-before-warn ordering the direction requires. The alert `degraded == 1` arm (config-true for any maxLag) and the age arm's static `> 450` remain as shipped.

## Acceptance mapping (supplied checks → testable requirements)

| Supplied acceptance | Requirement | Test / pin |
|---|---|---|
| (a) config exposes derived threshold; unit test asserts 900 → 450 and 1800 → 900 | REQ-1, REQ-2 | `TestAuditGovernanceBacklogAlertThresholdDerived` (new, `internal/config`; expectations in derived arithmetic — `900/2`, `1800/2`, `901/2` — plus the zero-value and ordering-hardening cases) |
| (b) grep-consistency test/doc check (contract item 5): alerts.yml expr == derived default threshold | REQ-3 | `TestAlertsYMLAuditGovernanceExprParity` (re-routed through the accessor) + `TestNoExecutable450LiteralOutsideAlertsYml` + `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` |
| (c) `TestRuntimeReadyDegradesOnBacklogLag` still passes; degraded warn at maxLag while derived threshold fires earlier | REQ-4 | `TestRuntimeReadyDegradesOnBacklogLag` (unchanged) |

## Non-goals (out of scope)

- No change to `alerts.yml` (expr, description, arms, `for:`), `probeAndRecord`, `Ready()`, or any `internal/auditgovernance` behavior.
- No change to `boundedAuditGovernanceTiming` / `validAuditGovernanceRetry` bounds (the direction flags the 604800 acceptance only as context).
- No new validation that `MaxLagSeconds` is even; floor semantics are pinned by test instead.
- No doc rewrite of `docs/configuration.md:275` ("Oldest undelivered outbox age that `/readyz` permits." — stale wording) — that fix is owned by the sibling `internal/cluster` completion direction (its REQ-6), not this one.
- No other module's 450 references (telemetry, runtime comments) change; `TestNoExecutable450LiteralOutsideAlertsYml` already forbids new executable literals.

## Verification steps

> **Re-verified against the current worktree (HEAD `15763e2` + dirty B3-2 state) at finalization time — baseline green before implementation, so any post-implementation failure is attributable to the change itself:**
>
> 1. ✅ `go build ./...` and `go vet ./...` — clean.
> 2. ✅ `go test ./internal/config/ -run 'TestAuditGovernance' -v` — 9/9 PASS, incl. `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (default-side pin `config_audit_governance_test.go:64`).
> 3. ✅ `go test ./cmd/server/ -run 'TestAlertsYMLAuditGovernanceExprParity|TestNoExecutable450LiteralOutsideAlertsYml' -v` — 2/2 PASS (parity derives via inline `MaxLagSeconds/2` at `readyz_drill_test.go:389`; ban scan clean over `cmd/`, `internal/`, `sdk/go`).
> 4. ✅ `go test ./internal/auditgovernance/ -run 'TestRuntimeReadyDegradesOnBacklogLag' -v` — PASS ≈4.9s (harness `MaxLagSeconds=4`, `runtime_test.go:633`).

1. `go build ./...` and `go vet ./...` (accessor + tests compile; `make check` gates).
2. `go test ./internal/config/ -run 'TestAuditGovernance' -v` — REQ-2 test (derived-form expectations) green alongside the existing default pin.
3. `go test ./cmd/server/ -run 'TestAlertsYMLAuditGovernanceExprParity|TestNoExecutable450LiteralOutsideAlertsYml' -v` — REQ-3 green with the accessor-routed derivation, and the 450-ban still clean (the re-route adds no literal; the new config test's derived arithmetic adds none either — F1/H1).
4. `go test ./internal/auditgovernance/ -run 'TestRuntimeReadyDegradesOnBacklogLag' -v` — REQ-4 green, no runtime delta.
5. `make check` full pass (SQLite + local FS baseline; `gofmt -l` clean; all touched files ≤ 500 lines — the spec adds ~15 lines to `config_audit_governance.go` and one test).
