# Requirements Specification — `internal/api/rest` (analysis label): source-level drift guards for `RequiredScope`, the relay counter name pairs, and the alert expr↔gauge name (B3-5 "grep consistency" half)

**Module:** `internal/api/rest` (analysis label; implementation surface is a **new test file in `internal/auditgovernance/`** reading `internal/auditgovernance/model.go` + `token.go`, `internal/telemetry/metrics.go`, `internal/auditgovernance/relay.go`, and `deploy/prometheus/alerts.yml` — see §1)
**Direction:** "Close the B3-5 'grep consistency' acceptance the matrix e2e design dispositioned away: source-level drift guards for RequiredScope, relay counter name pairs, and the alert expr↔gauge name" (direction 2 of the analysis)
**Source analysis:** `docs/auto/analyses/internal-api-rest-8e390260.json`
**Contract:** `docs/campaigns/implementation-gate.md:25` (gate item 5, aero-vault scope alignment): acceptance includes "grep 一致性检查绿" (grep-consistency check green); downstream B4-2
**Date:** 2026-08-08 · **HEAD:** `15763e2` (+ uncommitted worktree; verification basis = current working tree)
**Score:** value 8 / risk reduction 6 / effort 3 / confidence 9

---

## 1. Module & scope

The analysis labels this direction under `internal/api/rest`, but — as with its sibling direction — **no change is required in `internal/api/rest/`**: the drift guards are pure test additions in `internal/auditgovernance/` (source-read tests, mirroring the shipped precedent `TestNoUUIDInFactsGo`). The module label is retained for traceability: the guarded contract (relay token scope + relay counters + backlog-age alert) is what the REST admin ingress (`AdminHandler.auditForTenant` → `repo.RecordAudit`) and every file-origin capture depend on. The actual surface is `internal/auditgovernance` (new test file; `model.go`/`token.go`/`relay.go` read-only), `internal/telemetry/metrics.go` (read-only), `deploy/prometheus/alerts.yml` (read-only), plus the existing shipped/uncommitted e2e suite in `cmd/server/`.

**Why this direction exists (verified):** the matrix-provisioned scope-alignment e2e (`cmd/server/governance_e2e_test.go`, 489 lines, untracked) is behavioral-only for the "grep consistency" half. The design explicitly dispositioned source-grep away as accepted risk (`docs/requirements/activation-gate-scope-alignment-matrix-e2e-v2.design.md:177` — "no source-grep"; `:213` — "(b) REQ-4 accepted-risk disposition — closed by explicit documentation"). The only shipped source-read guard in the repo is `TestNoUUIDInFactsGo` (`internal/auditgovernance/fact_id_test.go:193-205`). Three drift surfaces are therefore unpinned; this spec pins all three:

| # | Drift surface | Why it is invisible today |
|---|---|---|
| S1 | `RequiredScope` const (`model.go:17`) and its enforcement sites (`token.go:64`, `:152-153`) | Pinned only *behaviorally*: `validTokenScopes` (`token.go:152-153`) + the fake-token form assertion (`governance_e2e_test.go:81-98`). A constant-value drift fails loudly at runtime — but a **second enforcement site** (a literal `"audit:event:write"` inlined elsewhere) or a doc/const-block drift is invisible. |
| S2 | Relay counter name pairs: 4 wrapper funcs in `metrics.go:189-212` (counter name strings `:105-108`) vs 4 increment sites in `relay.go:83/:112/:121/:163` | A rename at either end silently orphans the alert's data source. The string-level hop (metrics.go counter names → Prometheus `audit_governance_relay_*_total`) is already pinned by `TestAuditGovernanceMetrics_SurfaceInScrape` (`internal/telemetry/metrics_test.go:82-108`, working tree); the **wrapper-name ↔ call-site hop is not**. |
| S3 | Alert expr (`deploy/prometheus/alerts.yml`, HEAD `:163`) vs gauge name (`metrics.go:370`) | The design's own T3 YAML-parse pin lives inside the **unlanded** D1 delta (`cmd/server/readyz_drill_test.go:384-420`, untracked), so today nothing in committed CI verifies the `audit_governance_backlog_age_seconds > 450` relationship. A case-sensitive keyword grep (`audit.*(lag|oldest|dead)`) hits only the comment (`:158`) and description (`:169`) — never the `expr` line (verified against HEAD). |

**In scope:** ① one new test file `internal/auditgovernance/drift_guards_test.go` (name negotiable; the direction's suggested `scope_drift_test.go` is acceptable) containing the three source-read guards; ② nothing else — no production-code changes, no new metrics, no docs edits. **Out of scope:** the D1 drill delta (sibling direction 1 — degraded sentinel, /readyz payload, gauge freshness), the admin-origin e2e cell (sibling direction 3), T-3 terminal-matrix changes (stays pinned by the shipped `governance_e2e_test.go` M1–M6 cells), any `internal/api/rest` handler change, and any YAML dependency promotion (I6).

---

## 2. Evidence verification

Every citation in the direction was re-checked against the current working tree (HEAD `15763e2` + uncommitted changes) and against HEAD where the direction cites a shipped line.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | "`RequiredScope` (`internal/auditgovernance/model.go:17`)" | `model.go:17`: `RequiredScope    = "audit:event:write"` in the const block (`:10-21`) | ✅ **exact** |
| E2 | "`validTokenScopes` (`token.go:152-153`)" | `token.go:152-153`: `func validTokenScopes(scopes []string) bool { return len(scopes) == 0 \|\| len(scopes) == 1 && scopes[0] == RequiredScope }` | ✅ **exact** |
| E3 | "the client-credentials call (`token.go:64`) passes it unconditionally" | `token.go:64`: `wire, err := s.client.ClientCredentials(ctx, RequiredScope)` in `fetch()`; the **only** `ClientCredentials(` call in the package (the interface decl `:44` has no leading dot; `WithClientCredentials` `:57` is a different token) | ✅ **exact**; repo-wide `grep -rn "ClientCredentials(" internal/auditgovernance/` confirms no second call site |
| E4 | "the four relay counter registrations (`internal/telemetry/metrics.go:185-213`)" | ⚠️ **line drift, substance holds:** `:185-213` is the span of the four **wrapper funcs** `IncAuditGovernanceRelay{Attempted,Delivered,Failed,Dead}` at `metrics.go:189/:197/:204/:212` (comments `:187-213`); the **counter name strings** (`m.Int64Counter("audit_governance.relay_attempted_total")` etc.) are at `metrics.go:105-108`; fields `:58-61` | ✅ **holds** (S2's anchor is the wrapper names per the acceptance wording; the name strings `:105-108` are pinned at scrape level by `metrics_test.go:82-108`) |
| E5 | "increment sites (`internal/auditgovernance/relay.go:83` attempted, `:112` delivered, `:121` dead, `:163` failed)" | `relay.go:83` `IncAuditGovernanceRelayAttempted` (entry of `deliverFact`); `:112` `…Delivered` (only after `CompleteAuditGovernance` returns nil); `:121` `…Dead` (entry of `failFact`); `:163` `…Failed` (entry of `retryFact`, after the cumulative-window terminal check) | ✅ **exact**; repo-wide non-test grep shows these are the only 4 sites |
| E6 | "the alert expr (`deploy/prometheus/alerts.yml:163`)" | **HEAD** `:163`: `expr: audit_governance_backlog_age_seconds > 450`; `for: 10m` `:164`; alert `AuditGovernanceBacklogDegraded` `:162`; group `aero-vault-audit-governance` `:156-169`. **Working tree** (unlanded D1 v3 delta): expr at `:187` rewritten to `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` | ✅ at HEAD; ⚠️ **working-tree delta diverges** — see §3 REQ-3 and §4 D1 |
| E7 | "case-sensitive grep hits only comment `:158` and description `:169`, verified" | HEAD: `grep -nE 'audit.*(lag\|oldest\|dead)'` hits only `:158` (comment) and `:169` (description); the `expr` line `:163` contains none of the keywords — grep-invisible | ✅ **exact** |
| E8 | "the only shipped grep guard is `TestNoUUIDInFactsGo` (`internal/auditgovernance/fact_id_test.go:192-205`)" | `fact_id_test.go:193-205` (comment `:193`, func `:196`): `runtime.Caller(0)` → `os.ReadFile(facts.go)` → `strings.Contains(src, "uuid")` fails | ✅ **exact** (region 192-205); `fact_id_test.go` is 417 lines, untracked |
| E9 | "`governance_e2e_test.go:81-98` (REQ-4 hard form pins)" | `:81-88` hard pins grant_type/scope/resource on the fake `/token` handler; `:95-98` echo `scope=audit:event:write` | ✅ **exact** |
| E10 | "`governance_e2e_test.go:360-489` (shipped matrix, behavioral only)" | `:360-489` — `TestGovernanceE2EActivationGateBoundTenant` `:362` … M1–M6 matrix cells; file is exactly 489 lines (≤500 gate) | ✅ **exact** |
| E11 | "accepted-risk 'no source-grep' disposition — `activation-gate-scope-alignment-matrix-e2e-v2.design.md:177,213`" | `:177` — REQ-4 row ends "…no source-grep"; `:213` — "(b) REQ-4 accepted-risk disposition — closed by explicit documentation" (§3.6 compensating assertion spelled out) | ✅ **exact** |
| E12 | "precedent idiom: `os.ReadFile` + `strings.Contains` source-read" | `fact_id_test.go:196-205` (E8); also `cmd/server/readyz_drill_test.go:384-420` (untracked) reads `../../deploy/prometheus/alerts.yml` with stdlib-only string scan | ✅ **exact** |
| E13 | "all three run under `go test ./...` inside `make check`" | `Makefile:124` `check: fmt vet vet-integration build test test-race-meta cli-check`; `Makefile:18` `go test ./...` (the `test` target) | ✅ **holds** — a new test in `internal/auditgovernance/` is picked up automatically |
| E14 | Sibling delta's own T3 pin (discovery beyond the direction) | `cmd/server/readyz_drill_test.go:384-420` `TestAlertsYMLAuditGovernanceExprParity` (untracked, unlanded): asserts exactly **one** `expr: audit_governance_` line file-wide, derived threshold `config.Load()/2 == 450`, `OR audit_governance_degraded == 1`, `for: 10m`, `severity: warning`, `/readyz stays 200`. `internal/config/config_audit_governance_test.go:64-85` (modified) pins the default `MaxLagSeconds == 900` (the ×0.5 numerator) | ✅ **verified** — the D1 delta already implements T3; this direction's REQ-3 must land its own pin *independently* and provably coexist (see §4 D1) |

**Problem-statement checks (current tree):**

| Statement | Verdict |
|---|---|
| "a constant drift fails loudly but a second enforcement site or doc drift is invisible" | ✅ **holds** — the only guards are behavioral (`validTokenScopes` + fake-token form pin, E2/E9); no source-read guard exists for `RequiredScope` |
| "a rename at either end silently orphans the alert's data source" | ✅ **holds** for the wrapper↔call-site hop (E4/E5); the string hop is already scrape-pinned (E4) |
| "today nothing in CI verifies the `audit_governance_backlog_age_seconds > 450` relationship" | ✅ **holds at HEAD** — the only alerts.yml readers in the tree are in the untracked D1 delta (E12/E14) |

---

## 3. Requirements

### REQ-1 — Source-read pin: `RequiredScope` const value + its unconditional enforcement site (S1)

New test `TestRequiredScopeSourcePin` in `internal/auditgovernance/drift_guards_test.go`, mirroring the `TestNoUUIDInFactsGo` idiom (`runtime.Caller(0)` → `os.ReadFile` on the package dir, stdlib-only — `os`, `path/filepath`, `runtime`, `regexp`):

1. **Value pin (model.go):** regex `RequiredScope\s+=\s+"audit:event:write"` must match `model.go` — the const must keep both the name and the exact value. A rename or value change fails CI.
2. **Unconditional-enforcement pin (token.go):** `token.go` must contain exactly **one** `\.ClientCredentials\(` call occurrence (regex `\.ClientCredentials\(`), and that occurrence must match `\.ClientCredentials\(ctx, RequiredScope\)` — the scope argument is the constant, never a literal or a variable. A second call site (e.g. a future token path inlining `"audit:event:write"` or passing a different scope) fails CI.
3. **Validation-site pin (token.go, flagged optional strengthening):** regex `scopes\[0\] == RequiredScope` must match `token.go` (`:153`) — prevents a literal-value copy drift in `validTokenScopes` while the const itself stays intact.

**Verified current state (green baseline):** 1. `model.go:17` matches; 2. exactly one call, `token.go:64`; 3. `token.go:153` matches.

### REQ-2 — Bidirectional pin: relay counter wrapper names ↔ increment sites (S2)

New test `TestRelayCounterIncrementSitesBijection` in the same file:

1. **Registration side (metrics.go):** regex `func (IncAuditGovernanceRelay\w+)\(ctx context\.Context\)` must match **exactly 4** function definitions (`metrics.go:189/:197/:204/:212`). Collect names → set A.
2. **Increment side (relay.go):** regex `telemetry\.(IncAuditGovernanceRelay\w+)\(context\.Background\(\)\)` must match **exactly 4** call sites (`relay.go:83/:112/:121/:163`). Collect names → set B.
3. **Bijection:** assert A == B and |A| == |B| == 4, and that each name occurs **exactly once** in each file (per-name occurrence count == 1). A rename at either end, a second increment site for an existing name, an unreferenced wrapper, or a 5th counter registered without an increment site (or vice versa) fails CI.

**Verified current state (green baseline):** A = B = {Attempted, Delivered, Failed, Dead}; each appears exactly once per file (repo-wide non-test grep confirms no other sites). The counter **name strings** (`metrics.go:105-108` → Prometheus `audit_governance_relay_*_total`) are out of REQ-2's scope — already pinned at scrape level by `TestAuditGovernanceMetrics_SurfaceInScrape` (`internal/telemetry/metrics_test.go:82-108`), and per-outcome values by `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`internal/auditgovernance/relay_metrics_test.go`).

### REQ-3 — YAML-parse pin: alert expr ↔ registered gauge name (S3; design T3 landed independently of the D1 delta)

New test `TestAlertsYMLAuditGovernanceExprNamePin` in the same file, reading `deploy/prometheus/alerts.yml` via `filepath.Join("..", "..", "deploy", "prometheus", "alerts.yml")` (same relative depth as the sibling's `cmd/server/readyz_drill_test.go:394`), stdlib-only (no YAML dependency promotion, I6):

1. **Rule presence:** `alert: AuditGovernanceBacklogDegraded` must occur; its block (marker to EOF or to the next `- alert:`) must contain the age arm `expr: audit_governance_backlog_age_seconds > 450` and `for: 10m`.
2. **Name↔registration guard (the actual expr↔gauge drift guard):** collect every `audit_governance_\w+` token from every `expr:` line in the file; for each token `audit_governance_X`, `internal/telemetry/metrics.go` must contain the quoted registered name `"audit_governance.X"` (first underscore → dot; matches OTel's dots→underscores export rule). Today's registered gauge names: `audit_governance.backlog_age_seconds` (`metrics.go:370`), `audit_governance.degraded` (`:384`). A gauge rename, an expr rename, or an expr referencing a name that was never registered fails CI.
3. **Threshold:** the age arm keeps the literal `> 450` (direction T3; 450 = shipped default `MaxLagSeconds` 900 × 0.5, anchored on the config side by `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`, working tree). Implementation note: the sibling delta derives the threshold via `config.Load()/2` and asserts the same resulting string — either form is acceptable; they agree while the default stays 900 (see §6 R3).

**Evidence-forced amendment to the direction's wording (must be recorded):** the direction's acceptance (3) says "the only `audit_governance_*` name in any expr is `audit_governance_backlog_age_seconds`". This is **true at HEAD** (`alerts.yml:163` — single name) but **false in the current working tree**: the unlanded D1 v3 delta rewrote the expr (`alerts.yml:187`) to `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`, and `RegisterAuditGovernanceDegradedGauge` (`metrics.go:377-384`) ships the degraded gauge. The literal "only name" check would fail the moment the D1 delta merges — a CI time bomb. REQ-3 replaces "only name" with the **closed-registered-set membership** guard (step 2), which is the direction's actual intent (no expr name may orphan the alert's data source) and passes in both states: HEAD (names = {`backlog_age_seconds`}) and post-D1 (names = {`backlog_age_seconds`, `degraded`}, both registered). Non-contradiction with the delta's own pin is proven in §4 D1.

---

## 4. Decisions & non-goals

| # | Decision | Rationale |
|---|---|---|
| D1 | **Coexistence with the unlanded D1 delta's T3 pin** (`cmd/server/readyz_drill_test.go:384-420`) | The delta asserts **exactly one** `expr: audit_governance_` *line* file-wide + the OR arm + `for: 10m`; REQ-3 asserts **name membership** (⊆ registered set) + the age arm + `for: 10m`. Both hold under HEAD *and* post-delta (verified against both file states); neither asserts the other's predicate, so no contradiction. If the delta lands first, no REQ-3 amendment is needed; if it never lands, REQ-3 still gives CI the T3 age-arm pin (the direction's "landed independently of the D1 delta"). |
| D2 | **One new file** `internal/auditgovernance/drift_guards_test.go` for all three tests | The direction's `scope_drift_test.go` is an example, not a mandate; one file keeps the diff to a single ≤500-line unit, all three guards are the same concern (source-level drift), and `internal/auditgovernance` is the natural home (the read targets are package-internal + one repo file). File line-count target ≈ 180-220 (well under the 500-line hard gate; `runtime_test.go` is already at 498). |
| D3 | **Stdlib-only guards** (`os.ReadFile`, `strings`, `regexp`, `path/filepath`, `runtime`) | I6 + the precedent idiom (E12); the sibling explicitly avoided promoting the indirect `go.yaml.in/yaml/v2` dep — same decision here; `regexp` is stdlib. |
| D4 | **No production-code changes** | The direction is pure drift-guard testing; `model.go`/`token.go`/`relay.go`/`metrics.go`/`alerts.yml` are read-only. |
| D5 | **T-3 unchanged** | The terminal matrix stays pinned by the shipped `governance_e2e_test.go` M1–M6 cells (REQ-1/REQ-2/REQ-3/REQ-5 + M6 `conflict:true`); no cell is modified, no cell is added. |

**Non-goals:** ① D1 drill read-path half (degraded sentinel, /readyz payload, gauge freshness — sibling direction 1); ② admin-origin matrix cell (sibling direction 3); ③ new metrics or alert-rule edits; ④ YAML dependency promotion; ⑤ behavior changes to scope enforcement or relay counting; ⑥ any `internal/api/rest` change.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**T-4 — grep-consistency half of B3-5** (all three run under `go test ./...` inside `make check` — `Makefile:18,124`):

1. **`TestRequiredScopeSourcePin`** — new source-read test in `internal/auditgovernance/drift_guards_test.go` (mirroring `TestNoUUIDInFactsGo`): reads `model.go` + `token.go` and asserts (a) regex `RequiredScope\s+=\s+"audit:event:write"` matches `model.go`; (b) `token.go` has exactly one `\.ClientCredentials\(` call and it matches `\.ClientCredentials\(ctx, RequiredScope\)` (REQ-1.1/1.2). *Preserved verbatim from the direction:* "RequiredScope==\"audit:event:write\" and that the client-credentials call (token.go:64) passes it unconditionally".
2. **`TestRelayCounterIncrementSitesBijection`** — bidirectional pin: each `IncAuditGovernanceRelay*` name defined in `metrics.go:189-212` (the direction's cited span 185-213) appears at exactly one increment site in `relay.go` and vice versa — 4-name pair, bijection (REQ-2). *Preserved verbatim:* "each IncAuditGovernanceRelay* name registered in metrics.go:185-213 appears at exactly one increment site in relay.go and vice versa (4-name pair — B3-4 relay metrics contract)".
3. **`TestAlertsYMLAuditGovernanceExprNamePin`** — YAML-parse (stdlib string scan, D3) of `deploy/prometheus/alerts.yml`: `AuditGovernanceBacklogDegraded` block contains `audit_governance_backlog_age_seconds > 450` and `for: 10m`; every `audit_governance_*` name in any expr is a registered metric name in `metrics.go` (dots→underscores; today's closed set {`audit_governance_backlog_age_seconds`, `audit_governance_degraded`} — §3 REQ-3, amendment documented in §3). *Preserved intent:* "design T3 landed independently of the D1 delta" — the age-arm relationship is CI-pinned regardless of the delta's fate.

**T-3 — unchanged:** the terminal matrix stays pinned by the shipped `cmd/server/governance_e2e_test.go` M1–M6 cells (`:360-489`); no test in this direction touches the matrix, the relay state machine, or the repository.

---

## 6. Risks

| # | Risk | Mitigation / disposition |
|---|---|---|
| R1 | **Coordination with the unlanded D1 delta** (`readyz_drill_test.go`, `alerts.yml:187`): if the delta merges before this direction, REQ-3's guard must not contradict the delta's exactly-one-expr pin; if it merges after, the delta must not break REQ-3 | Proven non-contradiction (§4 D1): the delta pins expr-*line* count + OR arm; REQ-3 pins *name membership* + age arm. Both verified green against both file states. |
| R2 | **Literal `> 450` threshold vs derived threshold** (sibling derives `config.Load()/2`): a default `MaxLagSeconds` change (900→N) would require coordinated updates of the alert, the config-side pin, the sibling's derived pin, and REQ-3's literal | Documented in REQ-3.3; the config-side anchor (`TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold`, working tree) makes a unilateral drift fail CI before it reaches the alert. |
| R3 | **Regex brittleness** (const-block formatting, call-shape refactor) | Same accepted tradeoff as the shipped precedent `TestNoUUIDInFactsGo`; regexes are pinned to current formatting and are trivial to update in the same commit as an intentional refactor. |
| R4 | **Hard gates** | New file well under the 500-line limit (D2); stdlib-only (I6); no testify; single-file diff; `gofmt` clean by construction. |
| R5 | **Behavioral-matrix regression** (T-3) | Out of scope by construction (D5); `governance_e2e_test.go` is untouched. |
