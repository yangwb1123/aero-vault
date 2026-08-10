# Requirements Specification — `cmd/server`: activation-gate first-event e2e through the production assembly + RequiredScope/alerts grep-consistency gate (B3-5/B3-6)

**Module:** `cmd/server`
**Direction:** "Activation-gate first-event e2e through the production assembly (buildAuditGovernanceRuntime + config env + runtimeReadiness) plus a RequiredScope/alerts grep-consistency gate (B3-5/B3-6)"
**Source analysis:** `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 2)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = this checkout, including the uncommitted B3-campaign worktree: `cmd/server/governance_e2e_test.go`, `cmd/server/readyz_drill_test.go` untracked, D1-drill edits in `build.go`/`http.go`)
**Score:** value 8 / risk reduction 7 / effort 4 / confidence 8

---

## 1. Scope

The activation-gate e2e `newGovernanceE2E` (`cmd/server/governance_e2e_test.go:182-227`) hand-assembles the wiring **in main.go order but with a hand-written `config.AuditGovernanceConfig` literal** fed straight into `auditgovernance.New`. It never exercises the production chain: env resolution (`config.Load()` → `loadAuditGovernanceConfig`, `internal/config/config_audit_governance.go:53-84` — `AUDIT_GOVERNANCE_ENABLED`, bindings-file strictness, `AUDIT_GOVERNANCE_CLIENT_SECRET_*` lookup at `:156`), `cfg.Validate()` (URL security, HMAC 32..4096, `ClaimTTL > 2×HTTPTimeout`, binding dedup, HMAC≠secret), `buildAuditGovernanceRuntime` (`cmd/server/audit_governance.go:15-49`) in its **enabled** path, `runtimeReadiness` (`:51-69`), or `registerGauges` (`cmd/server/build.go:121-136`). Consequences: a drift between the harness literal and the production validation/secret chain passes CI silently; the enabled assembly is covered by zero tests (the only `buildAuditGovernanceRuntime` test, `audit_governance_test.go:26-42`, is the disabled path).

Separately, B3-5 "grep consistency" has **zero enforcement**: `grep` of `Makefile`, `.github/`, `checks/` finds no reference to `RequiredScope` (`internal/auditgovernance/model.go:17`), and the alerts.yml `450` literal (`deploy/prometheus/alerts.yml:163`) is coupled to the `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` default 900 (`internal/config/config_audit_governance.go:66`) only by comment. The parity test that exists in the worktree (`readyz_drill_test.go:332-369`) pins the literal against a **hardcoded test constant** (`alertLagThresholdSeconds = 450`, `:37`) — a drift of the config default (900 → 1800) would still pass CI.

This spec adds, in `cmd/server` only, **test-only** requirements (zero production/schema/dependency footprint): a production-assembly activation-gate e2e mirroring the REQ-1/REQ-2 pins, enabled-path `buildAuditGovernanceRuntime` assertions, a `RequiredScope` grep-consistency gate, an alerts-parity test that **derives** the threshold from the config default, and a backlog-age gauge-callback unit test.

**Out of scope:** direction 1 of the same analysis (D1 `/readyz` probe-bound/degraded drill — `readyz_drill_test.go`, already in the worktree); direction 3 (orphaned `internal/shutdown.Group`); B3-1/B3-3/B3-4 implementation; any change to the alert rule semantics (`expr`/`severity`/`for` stay as shipped); any new env knob or config surface; any production code change; modification of the existing matrix harness or its tests.

---

## 2. Evidence verification

Every direction citation was checked against this checkout (line numbers reflect the working tree as read).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `governance_e2e_test.go:132-168` — "harness bypasses buildAuditGovernanceRuntime/env load" | `newGovernanceE2E` is at **`:182-227`** in this checkout (citation range covers receiver helpers `:132-168`); it builds a `config.AuditGovernanceConfig` literal (`:196-212`) and calls `auditgovernance.New` directly (`:214`) — no `config.Load()`, no `buildAuditGovernanceRuntime`; cleanup defers at `:222-226` (relay deliberately unstarted; tests call `startRelay`) | ✅ **substance exact; line drift noted.** |
| E2 | `audit_governance.go:12-58` — "enabled path untested; only disabled path at audit_governance_test.go:26-42" | `buildAuditGovernanceRuntime` `:15-49`; enabled branch `auditgovernance.New` at `:42`; `audit_governance_test.go:26-42` (`TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved`) passes only `&config.Config{}` (Enabled=false) | ✅ **exact.** |
| E3 | `build.go:107-126` — "registerGauges backlog-age callback untested" | `auditGovernanceBacklogAgeGaugeFn` `:98-108` (err/`!ok` fail-open branch `:101`); `registerGauges` `:121-136`; `grep buildRouter\|registerGauges\|runtimeReadiness cmd/server/*_test.go` → **zero hits** | ✅ **substance exact; line range drifted** (citation `:107-126` covers the D1-drill-added callback region in the HEAD diff; worktree file is 9 lines shorter). |
| E4 | `config_audit_governance.go:53-84,147-160` — "env-driven bindings + secret resolution" | `loadAuditGovernanceConfig` `:53-84`; `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` `:66`; `resolveAuditGovernanceSecrets` `:140-160`, `os.LookupEnv` at **`:156`**, `AUDIT_GOVERNANCE_CLIENT_SECRET_` prefix gate `:151-153`; `readAuditGovernanceBindings` mode/regular-file/symlink/1MiB strictness `:86-106` | ✅ **exact.** |
| E5 | `model.go:17` — "RequiredScope = audit:event:write; no grep check references it" | `RequiredScope = "audit:event:write"` at `model.go:17`; `grep RequiredScope Makefile .github/ checks/` → **empty**; only by-name references exist in Go tests (`governance_e2e_test.go:84,98`, `internal/auditgovernance/http_test.go:127`) and production (`token.go:64,153`) | ✅ **exact.** |
| E6 | `alerts.yml:159-169` — "450 literal vs default 900, comment-only coupling" | rule `AuditGovernanceBacklogDegraded` `:157-169`; `expr: audit_governance_backlog_age_seconds > 450` at **`:163`**; `:160` comment "(default 900s maxLag → 450s)"; no config/templating reference | ✅ **exact.** |
| E7 | "zero cmd/server tests reference buildRouter/readyzHandler/registerGauges/runtimeReadiness" | `buildRouter`, `registerGauges`, `runtimeReadiness`: zero test refs (E3). **`readyzHandler` is referenced** by `http_test.go:70,94,114` and `readyz_drill_test.go:147` — the direction's "zero" overstates; the D1-drill tests already cover the handler seam | ⚠️ **partially accurate** (correction: handler seam is covered; the three assembly symbols are not). |
| E8 | "B3-5 has zero enforcement" — incl. the worktree parity test | `readyz_drill_test.go:332-369` `TestAlertsYMLAuditGovernanceExprParity` pins `expr > 450`, `severity: warning`, "/readyz stays 200" against the **hardcoded** `alertLagThresholdSeconds = 450` (`:37`) — config-default drift (900→1800) passes CI; it is the comment-only coupling in enforcement form | ✅ **gap confirmed; REQ-4 hardens it.** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "harness bypasses buildAuditGovernanceRuntime/env load" | ✅ **holds** (E1). |
| "buildAuditGovernanceRuntime tested only in the disabled path" | ✅ **holds** (E2). |
| "env resolution (…MAX_LAG_SECONDS=900 at :66, CLIENT_SECRET_* lookup at :156) untested at the cmd/server seam" | ✅ **holds** (E4) — the config package tests `loadAuditGovernanceConfig`/`readAuditGovernanceBindings` directly (`config_audit_governance_test.go:55,83-107`), but no cmd/server test drives them via `config.Load()`; the e2e literal never touches them. |
| "registerGauges and runtimeReadiness untested" | ✅ **holds** (E3). |
| "no Makefile/checks/.github reference to RequiredScope" | ✅ **holds** (E5). |
| "alerts.yml 450 coupled to default 900 only by comment" | ✅ **holds** (E6, E8). |

**Feasibility facts verified for the acceptance criteria:** `config.Load()` (`internal/config/config.go:51-`) ends in `cfg.Validate()`; `TestValidate_OK` (`config_test.go:194`) proves default envs validate — a test can drive the full production env chain. `getEnvInt` treats an empty string as unset (`config.go:367-377`) — `t.Setenv(key, "")` yields the default (no `os.Unsetenv` needed). `validateAuditGovernanceURL` accepts loopback HTTP (`config_audit_governance.go:139-146`) — an `httptest.Server` URL satisfies `BaseURL`/`TokenURL`; the harness timing literal (poll 5 ms, ClaimTTL 30, MaxLag 60, MaxBackoff 2) satisfies every `Validate()` bound (`config_audit_governance.go:206-259`). `auditgovernance.New` performs no network I/O (publisher construction only; token fetch is per-delivery). `RegisterAuditGovernanceBacklogAgeGauge`/`RegisterQueueDepthGauge` swallow registration errors (`internal/telemetry/metrics.go:354-380`) — `registerGauges` can be invoked once per package safely. Public store API for terminal-state seeding: `ClaimAuditGovernance`/`FailAuditGovernance` (`repository/audit_governance_types.go:95,101`).

---

## 3. Requirements

### REQ-1 — Production-assembly activation-gate e2e (B3-6/T-3, REQ-1/REQ-2 pins through production wiring)

New file `cmd/server/governance_assembly_e2e_test.go` (package `main`; ≤ 500-line gate; reuses `govReceiver`, `waitForRow`, `quiesce`, `eventRowID`, `outboxRow`, `wantFactID` from `governance_e2e_test.go` — same package, no duplication).

A helper `newAssemblyE2E(t *testing.T, mode string) *govHarness` builds the wiring **exclusively through production functions**, in main.go order (`cmd/server/main.go:62,70,80-83,154-157`):

1. Start `httptest` receiver (`newGovReceiver(mode)`), write a bindings file `bindings.json` (mode `0o600`, JSON `{"revision":1,"bindings":[{"tenant_id":"acme","client_id":"e2e-client","client_secret_env":"AUDIT_GOVERNANCE_CLIENT_SECRET_ACME"}]}` — no `state` field so `resolveAuditGovernanceSecrets` defaults it to `"active"`).
2. `t.Setenv` **every** `AUDIT_GOVERNANCE_*` the harness literal currently hardcodes, with identical values: `ENABLED=true`, `BASE_URL=<receiver.URL>`, `TOKEN_URL=<receiver.URL>/token`, `BINDINGS_FILE=<path>`, `HMAC_KEY=0123456789abcdef0123456789abcdef` (32 B, distinct from the secret), `CLIENT_SECRET_ACME=e2e-secret-0000`, `HTTP_TIMEOUT_SECONDS=5`, `POLL_MILLISECONDS=5`, `BATCH_SIZE=16`, `CLAIM_TTL_SECONDS=30`, `INITIAL_BACKOFF_SECONDS=1`, `MAX_BACKOFF_SECONDS=2`, `MAX_LAG_SECONDS=60`, `RECONCILE_BATCH_SIZE=8`, `DELIVERED_RETENTION_SECONDS=3600`, `CLEANUP_INTERVAL_SECONDS=60`, `CLEANUP_BATCH_SIZE=100`.
3. `cfg, err := config.Load()` — exercises env resolution + bindings-file strictness + secret lookup + `cfg.Validate()`; `t.Fatal` on error.
4. `repo`/`store` via `repository.Open(ctx,"sqlite",dsn)`+`Migrate` and `storage.NewLocal` (same as `newGovernanceE2E`); `rt, err := buildAuditGovernanceRuntime(cfg, repo, logger)` → non-nil, no error; `t.Cleanup(rt.Close)`.
5. `checks := runtimeReadiness(nil, rt)` → **non-nil** (first test reference of the symbol); `checks.Ready(context.Background())` → `nil` (no drain, no backlog at startup).
6. `wrepo := auditgovernance.WrapRepository(repo, rt)`; `bus := events.New(wrepo, logger)`; `bus.WithRepository(wrepo)`; `svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)`.
7. `registerGauges(repo, rt)` — invoked **exactly once per package** (mirror `main.go:154`; OTel duplicate registration is swallowed, `metrics.go:354-380`, but a `sync.Once` guard in the new file documents the single-call rule, mirroring `internal/telemetry`'s TestMain idiom).
8. `rt.Start(context.Background())` deferred before `t.Cleanup` (harness `startRelay` semantics).

Return the `govHarness` (the existing struct — `dsn`, `receiver`, `rt`, `svc` all fit).

**REQ-1.1 — bound tenant (mirrors `TestGovernanceE2EActivationGateBoundTenant`, `governance_e2e_test.go:362-390`):** `TestGovernanceAssemblyE2EBoundTenant` — `putObject(t, h.svc, e2eTenant, "gate.txt")`; pre-start snapshot asserts the outbox row (tenant, origin kind, attempts 0, `deliveredAtNS==0`, `failedAtNS==0`, `availableAtNS!=0`, empty claim); **`SELECT COUNT(*) FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?` == 1** (the "exactly one outbox row" pin, `?` placeholder per I1); `startRelay`; `waitForRow(deliveredAtNS>0, attempts==1, claimOwner=="", lastError=="")`; `quiesce(50ms, postCount==1)`; `tokenCalls==1`; `firstPost().eventID == outbox id`; `Authorization == "Bearer "+e2eToken`; `row.id == wantFactID(t, h.dsn, h.receiver.source, obj.ID)` (T-4 reuse).

**REQ-1.2 — unbound tenant (mirrors `TestGovernanceE2EActivationGateUnboundTenant`, `:396-411`):** `TestGovernanceAssemblyE2EUnboundTenant` — PUT as tenant `"other"`; outbox query → `sql.ErrNoRows`; `startRelay`; `quiesce(1s, postCount==0 && tokenCalls==0)`.

Both tests fail loudly if the harness literal ever drifts from the production chain: `config.Load()` → `Validate()` → `auditgovernance.New` (which re-runs `cfg.Validate()`, `runtime.go:59-61`) is now the only path into the relay.

### REQ-2 — `buildAuditGovernanceRuntime` enabled-path assertions (B3-6)

`TestBuildAuditGovernanceRuntimeEnabledPath` (same file): env-driven config via `config.Load()` with the REQ-1 env set, **minus** `HTTP_TIMEOUT_SECONDS` (unset — see REQ-2.3) and **with a second binding** `{"tenant_id":"drainme","client_id":"e2e-drain","client_secret_env":"AUDIT_GOVERNANCE_CLIENT_SECRET_DRAIN","state":"draining"}` (+ its secret env). Fresh repo, no receiver needed (`auditgovernance.New` does no network I/O; `BaseURL`/`TokenURL` = `https://audit.example`, `https://sso.example/token` — HTTPS, no loopback requirement).

- **REQ-2.1 — non-nil:** `rt, err := buildAuditGovernanceRuntime(cfg, repo, logger)` → `err == nil && rt != nil`; the enabled branch (`audit_governance.go:38-43`) is executed for the first time in any test.
- **REQ-2.2 — Bound/Capture reflect bindings:** `rt.Bound("acme")==true`, `rt.Capture("acme")==true` (defaulted state `"active"`), `rt.Bound("drainme")==true`, `rt.Capture("drainme")==false` (draining state), `rt.Bound("other")==false`, `rt.Capture("other")==false` — pins the `resolveAuditGovernanceSecrets` state defaulting (`config_audit_governance.go:148-150`) through the production chain.
- **REQ-2.3 — HTTPTimeout default:** `cfg.AuditGovernance.HTTPTimeoutSeconds == 5` when `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` is unset (`getEnvInt` default, `config_audit_governance.go:60`; `t.Setenv(..., "")` is the unset idiom, `config.go:367-377`), and the runtime still constructs (the `Validate()` bound `ClaimTTLSeconds > 2×HTTPTimeoutSeconds` holds at defaults 30 > 10).

### REQ-3 — B3-5 `RequiredScope` grep-consistency gate

`TestRequiredScopeGrepConsistencyGate` (same file; repo root = `filepath.Join("..","..")` from the package dir — the established `readyz_drill_test.go:338` idiom). Reads the repo and asserts:

- **Exactly one authoritative definition:** across all `*.go` under the repo root, `grep -n "RequiredScope\s*="` → exactly 1 hit, at `internal/auditgovernance/model.go:17` (assert the file path and the constant value `"audit:event:write"`).
- **Zero literal drift in Go:** no `*.go` file other than `model.go` contains the bare literal `audit:event:write` (all references must use the constant — today: `token.go:64,153`, `governance_e2e_test.go:84,98`, `internal/auditgovernance/http_test.go:127`; the gate fails if a future edit hardcodes the value).
- **Zero literal in enforcement surfaces:** `Makefile`, `.github/`, `checks/`, `deploy/` contain no `audit:event:write` (they may reference the constant by name only — today zero references exist, which is the gap this gate closes).
- **Docs reference by name:** at least one `docs/requirements/*.md` file contains `RequiredScope` (docs may quote the value — the design docs already do — but the contract is by-name).

Scan with `filepath.WalkDir` + `os.ReadFile`; skip `docs/auto/` analysis artifacts and `vendor/` if present; `t.Fatal` with the offending file:line list on any violation. Stdlib-only (I6 — no new dependency; the Go scanner is a simple literal search over file contents).

### REQ-4 — Alerts-parity threshold derived from the config default

Harden `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:332-369`): **replace the hardcoded `const alertLagThresholdSeconds = 450` (`:37`) with a value derived at test runtime** — `t.Setenv("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", "")`, `cfg, _ := config.Load()`, `want := cfg.AuditGovernance.MaxLagSeconds / 2`. Keep the existing pins (rule marker, `expr: audit_governance_backlog_age_seconds > <want>`, `severity: warning`, "/readyz stays 200") and **add**: `want == 450` (pins the default itself to 900 — a config-default change without an alerts.yml update fails CI; an alerts.yml change without a config change fails CI; the current hardcoded constant fails to catch the former). If the D1-drill file is not merged by implementation time, implement the same derivation in the new test file and ensure no hardcoded threshold constant exists anywhere in `cmd/server` tests.

### REQ-5 — Backlog-age gauge callback unit test

`TestAuditGovernanceBacklogAgeGaugeFn` (same file): `fn := auditGovernanceBacklogAgeGaugeFn(rt)` over a REQ-1 assembly (relay **not** started), then:

- **Live pending → age:** after one bound-tenant `putObject`, `fn(ctx) > 0` (row pending; no relay ⇒ deterministic — `BacklogAge` reads the store directly, `runtime.go:151-160`).
- **Dead-only → 0:** land the row terminal via the public API — `ClaimAuditGovernance(ctx, tenant, token, 1, 10, time.Minute)` + `FailAuditGovernance(ctx, id, tenant, token, "dead")` (`audit_governance_types.go:95,101`) → `fn(ctx) == 0` (terminal-row exclusion, `repository/audit_governance_claim.go:211-223`).
- **Store error → 0 (fail-open):** `repo.Close()` then `fn(ctx) == 0` (the `err != nil || !ok` branch, `build.go:101`).

This makes the untested `registerGauges` backlog-age callback observable; REQ-1.7 covers the registration wiring itself.

---

## 4. Decisions & non-goals

- **D1 — The existing matrix harness stays untouched; the assembly e2e is additive.** `newGovernanceE2E` remains the fast, literal-config harness for the M1-M6 matrix (`governance_e2e_test.go:413+`); the new tests mirror only the REQ-1/REQ-2 activation-gate pins through the production chain. Replacing the shared harness would couple the matrix tests to env setup and widen blast radius.
- **D2 — The production chain is entered via `config.Load()`**, the only public entry to `loadAuditGovernanceConfig` from package `main` (the config loader is unexported). `godotenv.Load()` (`config.go:53`) is a no-op in tests (no `.env` in the package dir); unset envs fall back to defaults; the test sets every audit env it depends on, so a stray dev environment cannot change the assertions (except by explicit overrides, which `t.Setenv` prevents).
- **D3 — "Unset" is spelled `t.Setenv(key, "")`** — `getEnvInt`/`getEnvBool` treat empty as unset (`config.go:307-376`), which avoids `os.Unsetenv` bookkeeping.
- **D4 — `registerGauges` is invoked at most once per package** (sync.Once guard), mirroring `internal/telemetry`'s single-registration rule (`prometheus_test.go:1-24`); the observable behavior is pinned by the REQ-5 callback test.
- **D5 — The parity test derives the threshold from the config default** (`MaxLagSeconds/2`) instead of a test constant — the acceptance's "threshold drift fails CI" is only true if *both* drift directions fail: alerts.yml literal ↔ config default.
- **D6 — Zero production footprint:** no production code change, no migration, no `go.mod` change, no new env knob, no alert-rule semantic change. All requirements are tests in `cmd/server` plus the parity-test hardening in the untracked `readyz_drill_test.go`.
- **Non-goals:** D1 drill (direction 1 — probe-bound `extra.Ready`, degraded `{"ok":true,"degraded":true}` payload, `TestReadyzDegradedExtraReturns200WithMarker` family); shutdown.Group wiring (direction 3); B3-1 (permanent-error classifier), B3-3 (fact-ID determinism — already pinned by `wantFactID`), B3-4 (relay counter family); alert `expr`/`severity`/`for` changes; `RequiredScope` value changes (the gate pins the constant as-is); any change to `docs/configuration.md`/`.env.example`.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (B3-6/T-3 production-assembly e2e) —** *"with AUDIT_GOVERNANCE_ENABLED=true + bindings file + AUDIT_GOVERNANCE_CLIENT_SECRET_* env, one PUT through the real FileService+EventBus wiring yields exactly one outbox row and exactly one POST whose event_id equals DeterministicFactID recomputation (T-4 reuse); unbound tenant yields zero rows, zero POSTs, zero token calls."*
*Testable:* `TestGovernanceAssemblyE2EBoundTenant` (REQ-1.1) — env-driven `config.Load()` → `buildAuditGovernanceRuntime` → `runtimeReadiness` → wrapped repo → bus → `FileService`; one `putObject` yields `COUNT(*)==1` outbox row, exactly one POST (`quiesce(50ms)`), `event_id == wantFactID(...)` reuse, `tokenCalls==1`, `Bearer` auth. `TestGovernanceAssemblyE2EUnboundTenant` (REQ-1.2) — `"other"` tenant: `sql.ErrNoRows`, `quiesce(1s)` zero POSTs and zero token calls. The relay is the only moving part; all row-state assertions reuse the I1-compliant raw-sqlite helpers.

**AC-2 (B3-5 grep-consistency gate) —** *"grep-consistency gate passes — RequiredScope appears exactly once as the authoritative constant and every check/docs reference it; a parity test pins alerts.yml 450 == AUDIT_GOVERNANCE_MAX_LAG_SECONDS default 900 × 0.5 so threshold drift fails CI."*
*Testable:* `TestRequiredScopeGrepConsistencyGate` (REQ-3) — exactly one `RequiredScope` definition at `model.go:17`, zero `audit:event:write` literals in `.go` outside `model.go`, zero literals in `Makefile`/`.github/`/`checks/`/`deploy/`, ≥1 by-name doc reference. `TestAlertsYMLAuditGovernanceExprParity` hardened (REQ-4) — `want := config.Load().AuditGovernance.MaxLagSeconds / 2`; asserts `want == 450`, `expr: ... > 450`, severity and description pins. Alerts literal drift (450→600) and config-default drift (900→1800) both fail CI; the current hardcoded constant catches neither.

**AC-3 (B3-6 enabled path) —** *"buildAuditGovernanceRuntime enabled-path assertions — non-nil runtime, Bound()/Capture() reflect bindings, HTTPTimeout default (5s) applies when AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS is unset."*
*Testable:* `TestBuildAuditGovernanceRuntimeEnabledPath` (REQ-2) — `config.Load()` with two bindings (active + draining): `err == nil && rt != nil`; `Bound`/`Capture` matrix (REQ-2.2); `HTTPTimeoutSeconds == 5` with the env unset and successful runtime construction (REQ-2.3).

---

## 6. Risks

- **Env leakage into `config.Load()`** — the test drives the full env chain; a developer machine with stray `AUDIT_GOVERNANCE_*` values could perturb it. Mitigated: every audit env the assertions depend on is `t.Setenv`'d (D2), empty-string = unset (D3), and CI env is clean (`TestValidate_OK` proves default-env validity).
- **Timing flake** — the assembly e2e reuses the proven harness idioms: 5 ms poll, `waitForRow(10s)` with predicate dumps, `quiesce` for negatives (never `waitFor` on a negative), counter/`>` assertions only. Identical timing envelope to the passing `TestGovernanceE2EActivationGateBoundTenant`.
- **OTel duplicate registration** — `registerGauges` registers four observable gauges; errors are swallowed, so a duplicate call only no-ops — but the sync.Once guard (D4) keeps the wiring deterministic and documents the rule.
- **Path dependence of the grep gate** — `../../` resolves to the repo root from the package dir during `go test` (the `readyz_drill_test.go:338` precedent); `filepath.WalkDir` is layout-tolerant. `docs/auto/` analysis artifacts are excluded so regenerated analyses cannot trip the literal scan.
- **Dependency on the untracked worktree** — REQ-4 hardens `readyz_drill_test.go` (untracked, D1-drill campaign) and REQ-1 reuses `governance_e2e_test.go` (untracked). If either file is rejected at merge, the derivation and helpers must move into `governance_assembly_e2e_test.go` without the hardcoded constant (the requirement is the derivation, not the file).
- **Hard gates** — new file must stay ≤ 500 lines (REQ-1/2/3/5 shared helpers keep it ~300); `make check` (gofmt/build/vet/test, SQLite+local FS, zero network beyond `httptest`) applies unchanged; `wantFactID`/`outboxRow` reuse keeps the SQL `?`-placeholder rule (I1) intact.
- **Over-pinning the 900 default** — REQ-4's `want == 450` assertion freezes the default; a deliberate, coordinated config+alerts change requires a deliberate spec update. That is the intent ("threshold drift fails CI"), not a liability.

*Verification basis: all citations re-checked on this checkout (`15763e2` + B3-campaign worktree); line numbers reflect the working tree as read during this spec's production.*
