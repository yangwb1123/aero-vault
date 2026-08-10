# Design — `internal/auditgovernance`: single-source the 450s alert threshold (maxLag×0.5) instead of the hardcoded literal in alerts.yml

**Module:** `internal/auditgovernance` (threshold semantics) + `internal/config` (default owner) + `cmd/server` (parity/scan pins) + `internal/telemetry` (fixture + gauge surface) + `deploy/prometheus/alerts.yml` (threshold literal)
**Spec:** `docs/requirements/internal-auditgovernance-backlog-alert-threshold-single-source-v1.spec.md` (REQ-1..4, D1..D4, AC-1..3)
**Contract:** `docs/campaigns/campaign-aero-vault-b3.yaml:7` (item 2: "Ready 解耦（H1）：maxLag 翻转 → degraded + 450s 告警；终态行排除")
**HEAD:** `15763e2` + worktree (verification basis) · **Date:** 2026-08-08 · **Rev 2 (post-review re-verification):** F1 — odd-delta narrative corrected (`901/2 = 450`, so 900→901 does NOT fail S1; the S1-failing negative control is 900→902, and S2 is the intended 900 pin) · full line-range/path audit fixes · F2 — walker hardening (loud `WalkDir` errors, no silent empty walk) · F3 — stripper limitation documented ("zero false positives" is a current-tree statement, not an invariant)
**Scope lock:** exactly two test-file deltas (R1 dead-const removal, R2 fixture rename + scan pin) on top of already-shipped pins (S1/S2). Zero production-code delta; no config surface, no DB migration, no wire/API change, no `alerts.yml` content change.

---

## 1. Verification register (evidence re-checked, not trusted)

All evidence-cited line numbers were re-read on this worktree; every claim in the supplied evidence is **verified true** except one scope correction (noted). Both acceptance pins were executed, not just read.

| Evidence claim | Verified location (this worktree) | Verdict |
|---|---|---|
| `alerts.yml:163` expr `> 450` | Rule `AuditGovernanceBacklogDegraded` `deploy/prometheus/alerts.yml:186-195`; expr at `:187` = `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`; derivation comment `:179`, `for: 10m` `:188`, `severity: warning` `:190`, "/readyz stays 200" `:193` | ✅ line drift `:163→:187` + OR-arm amendment, both as claimed |
| `config_audit_governance.go:66` default 900 | `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` at `:68` (drift `:66→:68`) | ✅ default still 900 |
| `telemetry/metrics.go:357` comment | `RegisterAuditGovernanceBacklogAgeGauge` doc comment `:364-367`, "alert at maxLag×0.5, default 450s" at `:367` (func at `:368`) | ✅ comment only, no code derivation |
| `runtime.go:149` comment | `PendingBacklogAge` doc "drives the degraded alert (maxLag×0.5)" `:194`; `probeAndRecord` comment "alert threshold maxLag×0.5" `:281`; code at `:283-288` compares `age > r.maxLag` — never the half-lag numeric | ✅ comments only |
| Contract item 2 "450s alert" | `docs/campaigns/campaign-aero-vault-b3.yaml:7` — "Ready 解耦（H1）：maxLag 翻转 → degraded + 450s 告警；终态行排除" | ✅ |
| `runtime_test.go:415` / T-3 `relay_terminal_test.go:125-128` | Relocated: `TestRuntimeReadyDegradesOnBacklogLag` at `internal/auditgovernance/runtime_test.go:618-671` (maxLag 4s, 4.5s wait, `Ready()` nil-degrade, draining still fails); `assertTerminalState` at `relay_terminal_test.go:117-129` with `OldestPendingAuditGovernance ok==false` check at `:126-128` | ✅ relocation as claimed |
| `TestAlertsYMLAuditGovernanceExprParity` at `cmd/server/readyz_drill_test.go:384-424`, untracked, passes | Func at `:384`; `wantExpr` = `"expr: audit_governance_backlog_age_seconds > " + strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)` `:392-393`; `os.ReadFile("../../deploy/prometheus/alerts.yml")` `:395-398`; exactly 2 `expr: audit_governance_` file-wide `:400-404`; block asserts wantExpr / OR arm / `for: 10m` / `severity: warning` / `/readyz stays 200` `:406-423`; env neutralization `:385-386` (two `t.Setenv`). **Executed: PASS (0.005s)** | ✅ |
| `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` at `internal/config/config_audit_governance_test.go:64-85` | Asserts `cfg.MaxLagSeconds != 900` `:82-83` via `loadAuditGovernanceConfig()` with identical neutralization `:76-77` (two `t.Setenv`). **Executed: PASS (0.002s)** | ✅ |
| Dead `const alertLagThresholdSeconds = 450` `readyz_drill_test.go:37`, never referenced | Repo-wide grep: only its definition `:37`, its own doc comment `:33-36`, and the parity test's comment `:381` which claims "the old hand-kept alertLagThresholdSeconds literal is gone — no second constant to drift" (false while the const exists). One of only two executable `450` literals in Go — the other is the arbitrary fixture at `metrics_test.go:175,179,180` (R2) | ✅ |
| Arbitrary scrape fixture 450 `metrics_test.go:175-180` | `age := int64(450)` `:175`, `v != 450` `:179`, `want 450` `:180` — gauge-surface value, any number would do | ✅ |
| Only other Go `450`-adjacent occurrences are comments | `metrics.go:367`, `runtime_test.go:617`, `config_audit_governance_test.go:66` — all spelled `450s`, which does **not** match `\b450\b` (no word boundary before `s`); plus the dead-const doc comment's bare `450` at `readyz_drill_test.go:34` (deleted by R1). `4500` literals (`internal/repository/ai_usage_cost_test.go:36,65-66`, `internal/auditgovernance/runtime_test.go:651`) never match either → zero false positives for the R2 scan | ✅ |
| `readyz_drill_test.go` exactly 500 lines; test files exempt from both size gates | `wc -l` = 500. `Makefile:162,175` (production-only `find` filters: gocyclo `files=` at `:162`, filesize awk at `:175`, both `-not -name '*_test.go'`); `engineering.yaml:17` (python `check-filesize` ignore `_test.go`) | ✅ R1/R2 edits unblocked |
| `make check` covers the pins; no Makefile/CI edit needed | `Makefile:18` `test: go test ./...`; `check:` target `:126` = fmt vet vet-integration build test test-race-meta cli-check; `.github/workflows/ci.yml:85` `go test ./...`, `:88` race step | ✅ |
| Store query excludes terminal rows | `internal/repository/audit_governance_claim.go:211-223` `OldestPendingAuditGovernance`, predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` `:218` | ✅ |
| Regression pins present | `TestReadyzBacklogLagDegradesNot503` `readyz_drill_test.go:215-253`; `TestReadyzDeadLetteredBacklog200AndGaugeZero` `:291-367`; `TestRuntimeBacklogAgeZeroWhenNoPending` `runtime_test.go:676-701`; `TestRuntimeBacklogAgeZeroWhenAllTerminal` `runtime_ready_test.go:254-294` | ✅ |
| Baseline green | `go build ./...` clean · `go vet` (4 pkgs) clean · `gofmt -l` clean · `go test ./internal/config/ ./internal/telemetry/ ./cmd/server/` ok (cached) · `go test -count=1 ./internal/auditgovernance/` ok (30.4s) | ✅ |
| R2 scan scope `cmd/, internal/, mcp/, sdk/go/` | **`mcp/` does not exist at top level** — the MCP package is `internal/mcp/` (already inside `internal/`). Complete Go root inventory: `cmd` (17 files), `internal` (488), `sdk/go` (12); no other top-level dir contains `.go` | ⚠️ **correction** — scope is `{cmd, internal, sdk/go}` |

**Problem-statement checks (direction claims vs. current tree):**

| Statement | Verdict |
|---|---|
| "The alert expr is a hardcoded literal `> 450` while its semantics is maxLag×0.5" | ✅ still true as a static artifact — `alerts.yml:187` must contain the literal; single-sourcing = the literal is *pinned* to the derivation (D1), not templated |
| "Nothing pins the pairing (no test reads alerts.yml)" | ❌ obsolete — S1 reads `alerts.yml` and derives the expectation from `config.Load()` |
| "The derivation lives only in comments" | ❌ obsolete — S1 derives (`config.Load()/2`, const-free); S2 pins the default at its owner |
| "A config-default change would violate contract item 2 with zero test failure" | ❌ obsolete — 900→N fails S2 for any N; even N also fails S1 (`N/2 ≠ 450`; odd N like 901 keeps `N/2 = 450` in Go integer division, so S2 — not S1 — is the 900 pin) |
| "No second literal 450 exists outside alerts.yml+config default" | ⚠️ partially — dead const (R1) and the metrics fixture 450 (R2) are the only executable literals; comments remain |

---

## 2. Design

### D1 (SHIPPED — documented, not re-implemented) — derived parity pin: `TestAlertsYMLAuditGovernanceExprParity`

The single-sourcing mechanism is a Go test that **derives** the expected expr threshold from the config loader the production binary uses, then reads the static `alerts.yml` and asserts the rule block matches. Neither side can drift silently:

```go
t.Setenv("AUDIT_GOVERNANCE_ENABLED", "false")    // skip the bindings-file requirement
t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "") // empty → getEnvInt falls back to the shipped default
cfg, err := config.Load()
wantExpr := "expr: audit_governance_backlog_age_seconds > " +
    strconv.Itoa(cfg.AuditGovernance.MaxLagSeconds/2)
```

Assertions, block-scoped to the `AuditGovernanceBacklogDegraded` rule: `wantExpr`, the `OR audit_governance_degraded == 1` arm (F11/F16 — a regression dropping it must fail CI), `for: 10m`, `severity: warning`, `/readyz stays 200`, and exactly two `expr: audit_governance_` rules file-wide (this rule + the `AuditGovernanceEnabledUnbound` drain-mode companion).

### D2 (SHIPPED — documented) — default-side pin at its owner: `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`

The other half of the ×0.5 arithmetic (the multiplicand, 900) is pinned in the package that owns the default, through the same loader the relay consumes, with the same env neutralization. A unilateral default edit fails here; a unilateral `alerts.yml` edit fails D1.

### D3 (DELTA R1) — delete the dead literal and its superseded doc comment

`cmd/server/readyz_drill_test.go:32-37` — the blank line, the 4-line doc comment ("pins the alerts.yml expr threshold…"), and `const alertLagThresholdSeconds = 450`. After deletion, `readyz_drill_test.go:381`'s claim ("the old hand-kept alertLagThresholdSeconds literal is gone") becomes true. Net −6 lines (500 → 494). No other file references the symbol (verified repo-wide).

### D4 (DELTA R2) — de-magic the fixture + scoped scan pin (zero exemptions)

**Fixture rename** (`internal/telemetry/metrics_test.go:175,179,180`): `int64(450)` → `int64(137)`, `v != 450` → `v != 137`, `want 450` → `want 137`. The value is an arbitrary gauge-surface fixture (any value would do); 137 keeps the acceptance literal — no allowlist.

**Scan pin** (sibling test in `cmd/server/readyz_drill_test.go`, stdlib-only — `os`/`path/filepath`/`io/fs`/`strings`/`regexp`, I6):

```go
// stripGoCommentsAndStrings removes // line comments and "…"/`…` string
// literals (with escape handling) so the scan sees only executable tokens.
func stripGoCommentsAndStrings(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '"':
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == '"' {
					break
				}
			}
		case '`':
			for i++; i < len(src) && src[i] != '`'; i++ {
			}
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				b.WriteByte('\n')
				continue
			}
			b.WriteByte(src[i])
		default:
			b.WriteByte(src[i])
		}
	}
	return b.String()
}

// TestNoExecutable450LiteralOutsideAlertsYml pins acceptance AC-2: the only
// executable 450 threshold literal in the Go tree is the alerts.yml expr
// (pinned separately by TestAlertsYMLAuditGovernanceExprParity). The regex
// lives in this test's own raw string and is stripped before matching, so the
// scan cannot self-hit. Comments and string literals are stripped — comment
// drift stays allowed; only executable tokens are pinned (FM5).
func TestNoExecutable450LiteralOutsideAlertsYml(t *testing.T) {
	pat := regexp.MustCompile(`\b450\b`)
	// `go test` runs with cwd = the package dir (cmd/server), so the roots
	// must be anchored to the repo root via the same `../..` the parity
	// test uses for deploy/prometheus/alerts.yml — a relative "cmd" walk
	// would traverse cmd/server/cmd and silently see nothing.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, root := range []string{"cmd", "internal", "sdk/go"} { // mcp lives at internal/mcp
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err // mid-walk errors must be loud, never silent skips
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(pat.FindAllStringIndex(stripGoCommentsAndStrings(string(src)), -1)); n > 0 {
				rel, _ := filepath.Rel(repoRoot, path)
				hits = append(hits, fmt.Sprintf("%s: %d", rel, n))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err) // loud: a relocated test walking nothing must fail, not pass
		}
	}
	if len(hits) > 0 {
		t.Fatalf("executable 450 literal(s) outside alerts.yml: %v", hits)
	}
}
```

Properties:

- **Self-verifying:** the pattern `\b450\b` lives in the test's own raw string literal → stripped → no self-hit; the const at `readyz_drill_test.go:37` (if R1 is skipped) is an executable token → caught.
- **Cwd anchoring (verified by simulation):** the test runs with cwd = `cmd/server` (package dir, same as the parity test's `../../deploy/...`), so roots are `filepath.Abs(filepath.Join("..", ".."))`-anchored. Simulated from `cmd/server`: PRE-state (R1/R2 not yet applied) → exactly 2 files hit — `cmd/server/readyz_drill_test.go: 1` (dead const; the `:34` comment is stripped) and `internal/telemetry/metrics_test.go: 2` (`int64(450)` + `v != 450`; `want 450` is a `t.Fatalf` string → stripped); POST-state (const removed, fixture renamed) → 0 hits.
- **Zero false positives verified (current tree):** after R1+R2 the only `\b450\b` matches in Go are the scan's own doc comment (`:534-535`) and `t.Fatalf` string (`:574`) — both stripped — plus the pattern's raw string (`:541`, stripped). The `4500` literals (`internal/repository/ai_usage_cost_test.go:36,65-66`, `internal/auditgovernance/runtime_test.go:651`) never match `\b450\b` (no word boundary mid-number), and comment occurrences spelled `450s` (`metrics.go:367`, `runtime_test.go:617`, `config_audit_governance_test.go:66`) do not match either. Zero exemptions — but this is a **current-tree statement, not an invariant** (see stripper limitation below).
- **Loud failure (F2, hardened):** the walker propagates mid-walk errors (`return err`) and the `WalkDir` result is checked (`t.Fatal`) — a relocation that makes all three roots fail to open fails loudly, never silently PASSes an empty walk.
- **Stripper limitation (F3, documented):** `stripGoCommentsAndStrings` is not a lexer — `/* … */` block comments are **not** stripped. A future block comment containing a bare `450` (or a `//`-like sequence preceding one) would false-positive. The failure is loud (FM7's forced decision), which is the acceptance's intent; "zero false positives" is a statement about the current tree (every 450-bearing comment today is `//` style), not an invariant.
- **Scope correction vs. spec:** the spec's "`mcp/`" root does not exist; `internal/mcp/` is covered by `internal/`. Roots `{cmd, internal, sdk/go}` are the complete Go source layout (verified by inventory).
- **Comment drift stays allowed:** comments are documentation and remain exempt — `alerts.yml`-side docs (`:179,184,193`) and Go comments may still mention "450s"; only executable tokens are pinned.

### Shipped inventory this design relies on (regression net, unchanged)

`TestRuntimeReadyDegradesOnBacklogLag` (runtime_test.go:618-671), `TestReadyzBacklogLagDegradesNot503` (readyz_drill_test.go:215-253), `TestRuntimeBacklogAgeZeroWhenNoPending` (:676-701), `TestRuntimeBacklogAgeZeroWhenAllTerminal` (runtime_ready_test.go:254-294), `TestReadyzDeadLetteredBacklog200AndGaugeZero` (:291-367), degraded-flag 0/1 encoding pin (metrics_test.go:192+), store predicate `delivered_at_ns=0 AND failed_at_ns=0` (audit_governance_claim.go:211-223).

---

## 3. API changes

**Zero production API changes.** Explicit enumeration of every surface this design touches:

| Surface | Change |
|---|---|
| Go package API (`internal/auditgovernance`, `internal/config`, `internal/telemetry`, `cmd/server`) | None — no exported symbol added/removed/signature-changed. `stripGoCommentsAndStrings` + the scan test are unexported test-file locals |
| Config surface | None — `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` default 900 and semantics unchanged; remains operator-tunable (REQ-3) |
| `deploy/prometheus/alerts.yml` | None *by this design* — the OR-arm amendment (`:187`) is already shipped in the worktree and is what D1 pins |
| HTTP/wire protocols (REST/S3/WebDAV/MCP) | None |
| DB schema / migrations | None |
| CLI / SDK | None |
| Operator-observable alert contract | Unchanged: `AuditGovernanceBacklogDegraded` fires on `age > 450 OR degraded == 1` for 10m, severity warning, description documents `/readyz stays 200`. The design pins, it does not alter |
| Test surface | +1 test (`TestNoExecutable450LiteralOutsideAlertsYml`), +1 unexported helper, −1 dead const, 3-line fixture edit |

---

## 4. Compatibility constraints

1. **Static `alerts.yml` stays static (D1).** No templating/generation from config — the pins *are* the single-sourcing mechanism. Generation would add deploy-tooling surface for no behavioral gain.
2. **Env neutralization is load-bearing (D3 of the spec).** Both S1 and S2 set `AUDIT_GOVERNANCE_MAX_LAG_SECONDS=""` so the expectation is computed from the *shipped default*, never an ambient override. An operator tuning the knob must not re-anchor the static comparison; non-default maxLag is signaled by the degraded arm (`alerts.yml:187`).
3. **Anchored to the shipped default, not the runtime value.** The static file cannot follow a runtime override by construction (REQ-3); this is documented behavior, not a gap.
4. **The R2 scan is scoped to Go source roots `{cmd, internal, sdk/go}`** with comment/string stripping and a single token regex `\b450\b`. `4500` and comments are definitionally out of scope; no allowlist exists to drift. If a legitimate executable `450` is ever needed, the scan forces an explicit decision (rename/extend) — that is the acceptance's intent.
5. **Test-file edits are unblocked by the line gate.** `readyz_drill_test.go` goes 500 → 494 after R1 (−6) → **576** after R2b (+82: 78-line scan block incl. doc comment + 3 new stdlib imports `fmt`/`io/fs`/`regexp` + import-block layout; line-exact in the shipped tree). Both size gates exempt `*_test.go` (`Makefile:162,175`, `engineering.yaml:17`). Production files remain untouched, so the 500-line production gate is unaffected.
6. **Stdlib-only (I6).** D1/D4 use `os`/`strings`/`strconv`/`fmt`/`regexp`/`path/filepath`/`io/fs` — no new `go.mod` dependency, no YAML parser, no testify.
7. **Untracked-file coupling.** `readyz_drill_test.go` is untracked (worktree); D3/D4 must land in the same changeset as the rest of the audit-governance worktree so the gate stays green at the commit boundary.

---

## 5. Failure modes

| # | Failure | Detection | Owner |
|---|---------|-----------|-------|
| FM1 | `MaxLagSeconds` default edited 900→N without touching `alerts.yml` | S2 fails (`MaxLagSeconds != 900`) for **any** N; S1 also fails for **even** N (`N/2 ≠ 450`, e.g. 902 → `> 451`). Odd N (901 → `901/2 = 450` in Go integer division) leaves S1 matching alerts.yml — S2 is the intended 900 pin (F1). Migration: both sides in one commit | D2 + D1 |
| FM2 | `alerts.yml` literal edited 450→M without touching config | S1 fails (`wantExpr` block assert) | D1 |
| FM3 | F11/F16 regression: `OR audit_governance_degraded == 1` arm dropped | S1 fails (block-scoped string assert) — the wedge would otherwise go alert-silent | D1 |
| FM4 | `for` / `severity` / description drift in the rule | S1 fails (`for: 10m`, `severity: warning`, `/readyz stays 200` asserts) | D1 |
| FM5 | Executable `450` literal reintroduced in Go (new call site, new const) | R2 scan fails; no exemptions, so no silent allowlist growth | D4 |
| FM6 | Operator sets `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` ≠ 900 in the environment | Not a failure — neutralized in tests (D3); alert stays anchored to shipped default; non-default lag surfaces via degraded arm. If this is ever deemed insufficient, the fix is a new mechanism (per-config threshold), explicitly a non-goal | D3 |
| FM7 | R2 scan false positive (legitimate future executable 450) | Scan fails loudly → explicit decision: rename the value or extend the acceptance. The scan's own regex and helper are in the scanned tree; both self-strip. Known stripper limit (F3): `/* */` block comments are not stripped — a bare 450 inside one false-positives; loud by design, matching acceptance intent | D4 |
| FM8 | R2 scan blind spot (new top-level Go root outside `{cmd, internal, sdk/go}`) | Current inventory has no other Go root; a new one would silently escape the scan → mitigate by the root list living next to the pin (any layout change requires touching the test, which is review-visible) | D4 |
| FM9 | S1's relative path `../../deploy/prometheus/alerts.yml` breaks (file moved/renamed) | Loud failure at test time (`os.ReadFile` error → `t.Fatal`), never silent | D1 |
| FM9b | Scan's `../..` repo-root anchor breaks (test file relocated to another package dir) | Loud by construction (F2): the walker propagates root/mid-walk errors (`return err`) and `WalkDir`'s result is checked (`t.Fatal`) — a relocation that errors all three roots fails, never silently PASSes an empty walk. Anchor mirrors the parity test's path idiom; both move together or both fail loudly | D4 |
| FM10 | Scan strips a string/comment and misses a `450` hidden in a `//nolint`-style construct | Cannot happen for the pinned cases: the acceptance is about executable literals; a literal hidden in a string is by definition not the threshold literal (which is executable in `alerts.yml`, not Go) | D4 |
| FM11 | `gofmt`/vet regressions from the edits | `make check` fmt/vet steps; D3/D4 edits are small and mechanical | — |

---

## 6. Migration steps

No operator, deploy, DB, or config migration — this is a test-forest change. Steps for the implementing agent:

1. **R1** — `cmd/server/readyz_drill_test.go`: delete lines 32-37 (blank + 4-line doc comment + `const alertLagThresholdSeconds = 450`) → −6 lines (500 → 494). Verify `grep -rn "alertLagThresholdSeconds" --include="*.go" .` → only the now-accurate comment remains (formerly `:381`; −6 → `:375` before R2b's import additions, `:378` in the shipped tree).
2. **R2a** — `internal/telemetry/metrics_test.go`: three mechanical edits at `:175,179,180` (`450` → `137` in fixture, assert, and want-message).
3. **R2b** — append the 78-line scan block below (2-line helper comment + `stripGoCommentsAndStrings` + `TestNoExecutable450LiteralOutsideAlertsYml`) plus the 3 new stdlib imports (`fmt`, `io/fs`, `regexp`); roots `{cmd, internal, sdk/go}` anchored via `filepath.Abs(filepath.Join("..", ".."))` — the test runs with cwd = package dir, verified by simulation; note the `mcp/` correction vs. spec §3. Final file: 494 + 82 = 576 lines (line-exact in the shipped tree).
4. **Verify locally:**
   - `gofmt -l cmd/server/readyz_drill_test.go internal/telemetry/metrics_test.go` → empty
   - `go build ./...` · `go vet ./internal/auditgovernance/ ./internal/config/ ./internal/telemetry/ ./cmd/server/`
   - `go test ./cmd/server/ -run 'TestAlertsYMLAuditGovernanceExprParity|TestNoExecutable450LiteralOutsideAlertsYml' -count=1 -v` → both PASS
   - `go test ./internal/config/ -run TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold -count=1 -v` → PASS
   - `go test ./internal/telemetry/ -run GaugeSurfaceInScrape -count=1` → PASS
   - `go test -count=1 ./internal/auditgovernance/` → ok (30s)
   - Full `make check` (fmt, vet-integration, build, test, test-race-meta, cli-check)
5. **Mutation-test the pins (negative control, revert after):**
   - `alerts.yml:187` `450`→`451` → S1 FAILS → revert
   - re-add `const x = 450` → R2 scan FAILS → revert
   - `config_audit_governance.go:68` 900→901 → S2 FAILS **only** (`901/2 = 450`: S1 unchanged — S2 is the intended 900 pin; F1) → revert; then 900→902 → S2 **and** S1 FAIL (`902/2 = 451 ≠ 450`) → revert
6. **Land** R1+R2 in the same changeset as the uncommitted worktree (S1/S2/regression pins are part of it; `readyz_drill_test.go` is untracked until then). No commit-boundary dependency beyond keeping the gate green.

---

## 7. Testable acceptance mapping (spec §5, made executable)

| Acceptance (direction) | Pin(s) | Verification command | Expected |
|---|---|---|---|
| **(1)** A test/CI grep reads `alerts.yml` and asserts the expr threshold equals default MaxLagSeconds/2 (450) | S1 `TestAlertsYMLAuditGovernanceExprParity` (readyz_drill_test.go:384-424; derives `config.Load()`/2, const-free, env-neutralized; asserts expr, OR arm, `for: 10m`, severity, "/readyz stays 200", exactly 2 audit-governance exprs) | `go test ./cmd/server/ -run TestAlertsYMLAuditGovernanceExprParity -count=1 -v` | PASS (verified 0.005s); mutation: `alerts.yml` 450→451 → FAIL |
| **(2)** No second literal 450 exists outside alerts.yml + config default | R1 (dead const deleted) + R2 (fixture 450→137 + `TestNoExecutable450LiteralOutsideAlertsYml`) | `go test ./cmd/server/ -run TestNoExecutable450LiteralOutsideAlertsYml -count=1 -v`; `grep -rn "\b450\b" --include="*.go" cmd/ internal/ sdk/go/` | PASS; grep shows only comments and the scan's own string literals (allowed) — zero executable tokens; mutation: re-add const → FAIL |
| **(3)** Changing MaxLagSeconds shifts the derivation consistently; /readyz stays 200-degraded; gauge semantics unchanged | Derivation is *computed at test time*: **any** default edit fails S2 (asserts 900); **even-N** edits additionally fail S1 (`N/2 ≠ 450` until alerts.yml follows — odd N like 901 keeps `N/2 = 450` in Go integer division, so S1 alone cannot catch an odd delta; S2 is the intended 900 pin, F1). Alert stays anchored to shipped default (drill's 4s test-local config excluded via env neutralization). Regression net: `TestRuntimeReadyDegradesOnBacklogLag`, `TestReadyzBacklogLagDegradesNot503`, `TestRuntimeBacklogAgeZeroWhenAllTerminal`, `TestRuntimeBacklogAgeZeroWhenNoPending`, `TestReadyzDeadLetteredBacklog200AndGaugeZero`, store predicate (`delivered_at_ns=0 AND failed_at_ns=0`), `assertTerminalState` | `go test ./...` (make check; CI `ci.yml:85`); targeted: `go test ./internal/auditgovernance/ ./cmd/server/ -count=1` | All PASS (auditgovernance 30.4s verified); mutation: default 900→901 → S2 FAIL only (S1 unaffected: `901/2 = 450`); default 900→902 → S2 **and** S1 FAIL (`902/2 = 451`) |

**Gate coverage:** the pins run under `make check` (`test: go test ./...`, `Makefile:18`) and CI (`go test ./...` + race step, `ci.yml:85,88`). No Makefile/CI edit required. Production files all under the 500-line gate (untouched); test files exempt by both gates (`Makefile:162,175`, `engineering.yaml:17`).

---

## 8. Risks & non-goals

- **Pin-drift risk (low):** pins live in `cmd/server` + `internal/config` + `internal/telemetry`; all covered by `make check`.
- **R2 scan brittleness (mitigated):** scoped roots, comment/string stripping, single token regex, zero exemptions, loud walker (F2) — if a legitimate executable 450 ever appears, the scan forces an explicit decision, which is the acceptance's intent (FM7). Known limitation (F3): `/* */` block comments are not stripped — "zero false positives" is a current-tree statement, not an invariant; a future block comment containing a bare 450 would false-positive loudly, forcing that documented decision.
- **Spec discrepancy fixed here:** spec §3's scan scope listed a nonexistent top-level `mcp/`; this design corrects it to `{cmd, internal, sdk/go}` (mcp is `internal/mcp/`), with no coverage loss.
- **Non-goals:** templating/generating `alerts.yml` (D1), CI grep rules, per-config alert thresholds, severity/`for`/description content beyond what S1 asserts, `AuditGovernanceEnabledUnbound` companion rule, gauge naming, config surface, DB migrations, the D1 read-path drill (sibling spec, shipped), any production-code change.

*Verification basis: every citation re-read on this worktree (HEAD `15763e2` + uncommitted changes); both pins executed (PASS); baseline `go build`/`go vet`/`gofmt`/4-package tests confirmed green on this run.*
