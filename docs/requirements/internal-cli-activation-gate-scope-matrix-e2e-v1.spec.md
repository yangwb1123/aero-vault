# Requirements Specification — `internal/cli`: activation-gate + `audit:event:write` scope matrix e2e and usage/handler parity (B3-5/B3-6)

**Module:** `internal/cli`
**Direction:** "Activation-gate + audit:event:write scope matrix e2e and usage/handler parity test for the CLI admin path (B3-5/B3-6)"
**Source analysis:** `docs/auto/analyses/internal-cli-17314662.json` (direction 2)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = current working tree; all line numbers below are working-tree)
**Score:** value 8 / risk reduction 7 / effort 5 / confidence 8

---

## 1. Module & scope

The direction is **test-only**: two new test files in `internal/cli/` (package `cli`), zero production-code changes, zero new dependencies. The CLI admin path (`admin keys add` / `admin audit list`, `internal/cli/cli_admin.go`) and the relay-side scope constant (`internal/auditgovernance/model.go:17`) are read-only inputs.

**In scope:**
1. `internal/cli/cli_governance_e2e_test.go` — a cli-package e2e that assembles a real server (SQLite + local FS + auth registry + `auditgovernance.WrapRepository`, mirroring the `cmd/server/main.go:82/:212` assembly pattern) and drives it exclusively through `cli.Run(...)` with `AERO_ENDPOINT`/`AERO_API_KEY`/`AERO_TENANT` env wiring:
   - **AC-1 (B3-6 positive):** gate on (`Enabled=true` + one `active` binding for tenant `acme`) → CLI `admin keys add` provisions a key carrying the literal scope `audit:event:write` → the mutation's first fact lands in `audit_governance_outbox` (exactly one row, `origin_kind='admin'`, `action='key.add'`, `tenant_id='acme'`, `fact_kind='security'`) **and** is visible through the CLI admin path (`admin audit list` exit 0, stdout contains the entry).
   - **AC-2 (B3-6 negative):** gate off (no runtime, no wrap) → CLI admin surface stays fully functional (exit 0, local audit entry visible), the outbox stays **empty** (zero governance capture), and `AuditGovernanceCanDisable` reports safe (the gate-off boot is legal — mirrors `cmd/server/audit_governance.go:18-31`).
   - **AC-3 (B3-5):** matrix across scope-present/scope-absent keys against `admin audit list` on a gate-on server: `[admin]` → 200/exit 0; `[read,write]` → 403 at the admin gate (`admin scope required`); `[read]` → 403; `[audit:event:write]` (the relay's `RequiredScope` literal, provisioned via the CLI) → 403 at the read gate (`missing scope: read`) — proving the relay scope grants nothing on the audit admin surface.
2. `internal/cli/cli_parity_test.go` — the usage/handler parity test (AC-4): every `cliHandlers` key (minus the declared help-alias group) appears in `usage()` text and vice versa; every admin resource/action pair dispatches and appears in `adminUsage()` text and vice versa (P1–P6, §3 REQ-5).

**Out of scope (cross-referenced, deliberately not duplicated):**
- The server-side activation-gate matrix `cmd/server/governance_e2e_test.go` (file-origin facts M1–M6, `newGovernanceE2E` harness; 489 lines, unlanded in the working tree) — the CLI e2e is the **CLI-driven cell**; it does not re-drive file-origin delivery, terminal classification, or the fake `/token` flow.
- The admin-origin cell spec `docs/requirements/internal-auth-audit-governance-admin-security-facts-e2e-v1.spec.md` (cmd/server-level `governance_e2e_admin_test.go`: deterministic fact-ID recomputation, delivery, terminal state for `key.add` facts) — the CLI e2e asserts only **outbox arrival + CLI visibility**, not ID recomputation or delivery.
- The B3-5 grep-consistency drift guards (`docs/requirements/internal-api-rest-audit-governance-grep-consistency-drift-guards-v1.spec.md`: source-read pins on `RequiredScope`/counter pairs/alert expr) — the parity test pins the **CLI operator surface**, not relay internals.
- Any production change: adding `help` to `usage()` text, renaming CLI fields, fixing the `admin keys list` rendering drift (F1, §2) — all deliberately excluded; the parity test passes on the current tree as specified (verified, §3 REQ-5).

**Why this direction exists (verified):** every existing `internal/cli` test is a stub `httptest` server (e.g. `TestRun_AdminAudit_Dispatches`, `cli_test.go:1715-1737`, asserts only the request path against a fake handler). No test drives `cli.Run` against a real assembled server with auth + `WrapRepository`, so (a) a scope regression on `/v1/admin/audit` or (b) a failed activation (bindings absent / control tables not seeded) passes CI silently. Separately, the command surface is triple-maintained by hand — `cliHandlers` (`cli.go:70-93`), `usage()` (`cli.go:108-137`) and `adminUsage()` (`cli_admin.go:14-36`) — with no parity test (`grep -rn "cliHandlers" internal/cli/*_test.go` → zero hits).

---

## 2. Evidence verification

### 2.1 Direction citations (all 8 re-checked against the working tree)

| # | Direction citation | Verified location (working tree) | Verdict |
|---|---|---|---|
| E1 | `internal/auditgovernance/model.go:17` (`RequiredScope`) | `model.go:17`: `RequiredScope    = "audit:event:write"` in the const block (`:10-21`) | ✅ **exact** |
| E2 | `internal/cli/cli_admin.go:113-129` (`--scopes` provisioning) | `adminKeysAdd` `cli_admin.go:107-131`; `--scopes` flag parsed `:116-120`; body `{"token","tenant","label","scopes"}` sent to `POST /v1/admin/keys` `:124-126` | ✅ **substance exact; line drift** (cited 113-129, actual 107-131) |
| E3 | `cmd/server/main.go:82,212` (`WrapRepository` assembly) | `run()` `:82` and `runMCP()` `:212`: `repo = auditgovernance.WrapRepository(repo, auditRuntime)` after `auditRuntime.Start(ctx)` | ✅ **exact** (both sites; the e2e mirrors `:82`'s pattern) |
| E4 | `internal/cli/cli.go:51-77` (`cliHandlers` map) vs `:118-137` (`usage()`) | `cliHandlers` var `cli.go:70`, map literal `:73-93` (14 entries + 3 help aliases); `usage()` `:108-137` | ✅ **substance exact; line drift** (cited 51-77/118-137) |
| E5 | `internal/cli/cli_admin.go:25-36` (`adminUsage`) | `adminUsage()` `:14-36` (6 resources, 18 resource/action lines: keys 3, tenants 6, jobs 2, audit 1, files 1, buckets 5) | ✅ **substance exact; line drift** (cited 25-36) |
| E6 | `internal/cli/cli_test.go:1715` (`TestRun_AdminAudit_Dispatches`) | `cli_test.go:1715-1737` — stub-only: asserts `Run(["admin","audit","list"])` hits path `/v1/admin/audit` against `httptest.NewServer`; no auth, no server assembly | ✅ **exact** |
| E7 | `internal/repository/migrations/sqlite/0040_audit_governance_control.up.sql` (bindings/control tables) | Control table + singleton seed, `audit_governance_bindings` (state CHECK active/draining) + state index. Bindings are **seeded by `Runtime.New` → `applyDesiredBindings`** (`runtime.go:52-107`) via `store.ApplyAuditGovernanceBindings`, not by the migration | ✅ **exact** (gate state = bindings rows + `Capture()` truth) |
| E8 | "all cli tests are stub httptest" | Confirmed: no `internal/cli` test imports `repository`/`service`/`rest`/`auditgovernance`; the only full-server harnesses live in `internal/integration/` and `cmd/server/` | ✅ **holds** |

### 2.2 Problem-statement checks

| Statement | Verdict |
|---|---|
| "A scope regression or a failed activation would pass CI silently" | ✅ **holds** — `cli_test.go`'s admin tests stub the HTTP layer (`TestRun_AdminAudit_Dispatches` :1715, `TestCmdAdminAudit_*` :1670-1690); the scope decision (`requireAdmin`/`requireRESTScope`) never executes |
| "A B3 CLI command added to one surface and not the others breaks the operator contract undetected" | ✅ **holds** — no parity test; the recent `admin files delete` addition (`cli.go:136`, `cli_admin.go:30,54`, `cli_admin_files.go`) was applied to all three surfaces by hand with no guard |
| "The relay already enforces `RequiredScope='audit:event:write'`" | ✅ **holds** — requested unconditionally in `fetch` (`token.go:64` `ClientCredentials(ctx, RequiredScope)`), response scope validated by `validTokenScopes` (`token.go:152-153`: empty or exactly `[audit:event:write]`); the **only** `ClientCredentials(` call in the package |

### 2.3 Additional verified facts the e2e depends on (beyond the direction's citations)

| # | Fact | Location |
|---|---|---|
| F1 | **Pre-existing drift (must NOT be asserted):** real-server `admin keys list` renders empty columns — `AdminHandler.ListKeys` serializes raw `auth.Key` (Go field names `Token/Tenant/Scopes`, `auth.go:20-27`, no json tags), while the CLI parses `token_hash/tenant_id/scopes/label` (`cli_admin.go:79-105`); stub tests pin the snake_case shape (`cli_admin_test.go:90-135`). Out of scope; the scope matrix asserts HTTP behavior only | `internal/api/rest/admin.go:105-111`; `internal/cli/cli_admin.go:79-105` |
| F2 | The audit-admin path has a **two-gate scope chain**: `requireRESTScope` on the whole `/v1` router (GET ⇒ `read`) at `router.go:227,365-375` (`Reg.Require`, `auth_middleware.go:198-216`, 403 plain-text `"missing scope: read"`), then `requireAdmin` (`admin.go:457-467`, 403 JSON envelope `Forbidden / "admin scope required"`). `Key.Has` (`auth.go:46-49`): `admin` scope ⇒ any scope | `internal/api/rest/router.go:227,365-375`; `internal/api/rest/admin.go:457-467`; `internal/auth/auth_middleware.go:198-216` |
| F3 | `AdminHandler.AddKey` accepts **arbitrary scope strings** (no `knownScope` filter; `knownScope` `auth.go:140` applies only to env-seeded `AUTH_KEYS`); persisted scopes round-trip verbatim via `scopesToString`/`parseScopeString` (store.go:42-63) — so provisioning a key with the literal `audit:event:write` is legal and authenticates, yet grants nothing | `internal/api/rest/admin.go:114-142`; `internal/auth/store.go:42-63` |
| F4 | Outbox write is **synchronous with the mutation**: `auditedRepository.RecordAudit` (`auditgovernance/repository.go:27-41`) → `RecordAuditWithGovernance` (`internal/repository/audit_governance_write.go:20-47`) inserts `audit_log` + `audit_governance_outbox` in one tx (outbox schema: migration `0039`); `fact_kind='security'` for `key.*` actions (`facts.go:89-93`). No relay run needed for the outbox assertion — **the e2e must not `Start()` the runtime** (no delivery race) | `internal/auditgovernance/repository.go:27-41`; `internal/repository/audit_governance_write.go:20-47`; `internal/auditgovernance/facts.go:89-93`; `0039_audit_governance_outbox.up.sql` |
| F5 | `AddKey` audits `key.add` with `TenantID = body.Tenant` (`admin.go:141` `h.audit(r, "key.add", body.Tenant, …)`; `auditForTenant` `:414-427`), so the fact's tenant = the binding tenant only if the CLI passes `--tenant acme`; `ListKeys`/`ListAudit` are **not** audited (no extra facts) | `internal/api/rest/admin.go:141,414-427,105-111,432-441` |
| F6 | **Import-cycle constraint:** `cmd/server` and `internal/integration` both import `internal/cli` (`cmd/server/main.go:16`; `internal/integration/admin_files_delete_test.go:25`, `authz_cli_failclosed_test.go:33`) ⇒ the e2e **cannot reuse** the `internal/integration` full-server harness or `cmd/server/governance_e2e_test.go`; it must assemble inline. `internal/api/rest`, `internal/server`, `internal/middleware`, `internal/auditgovernance`, `internal/repository`, `internal/storage`, `internal/service`, `internal/auth`, `internal/config` do **not** import `internal/cli` (verified) ⇒ importable from the cli test | grep-verified |
| F7 | Assembly prerequisites, all verified: `rest.NewRouter(svc, repo, nil×3, bus=nil, reg, logger, false, nil, nil, 0, false, opts...)` (`router.go:214`; admin routes unconditional `:329-353`); `server.ApplyMiddleware(handler, repo, reg, rl=nil, cfg, logger, mw.NewConcurrencyLimiter(0).Middleware(), nil)` (nil rate limiter = pass-through, proven by `internal/integration/fullserver_test.go:140-160`); unknown tenants allowed (implicit-tenant compat, `middleware/tenant_status.go:19-33`); `repo.(auditgovernance.Store)` assertion pattern (`cmd/server/audit_governance.go:19,38`; `sqlStore` implements it, `repository/audit_governance_types.go:108`); `AuditGovernanceConfig.Validate` requirements incl. `ClaimTTLSeconds > 2*HTTPTimeoutSeconds`, `MaxLagSeconds > ClaimTTLSeconds`, `ClientSecretEnv` must match `^AUDIT_GOVERNANCE_CLIENT_SECRET_[A-Z0-9_]+` (config_audit_governance.go, env only read by `config.Load`, not by `Runtime.New`) | `internal/api/rest/router.go:214`; `internal/server/chain.go:92`; `internal/middleware/tenant_status.go:19-33`; `internal/config/config_audit_governance.go:145-210` |
| F8 | CLI error contract on 403: `readSuccessfulResponse` → `renderError` (`response.go:43-60`): JSON envelope ⇒ `HTTP 403 Forbidden: admin scope required`; plain-text ⇒ `HTTP 403: missing scope: read`; exit code 1 | `internal/cli/response.go:43-60,90-97`; `internal/cli/cli_admin.go:400-427` |
| F9 | `t.Setenv` panics on duplicate keys ⇒ matrix cases must swap `AERO_API_KEY`/`AERO_TENANT` via `os.Setenv` with explicit restore (or a helper); `AERO_ENDPOINT` set once with `t.Setenv` | Go testing contract |

---

## 3. Requirements

### REQ-1 — E2E harness: inline server assembly mirroring `main.go:82` (AC-1/AC-2/AC-3)

New file `internal/cli/cli_governance_e2e_test.go` (package `cli`). A `newGovernanceE2EServer(t, gateOn bool) (ts *httptest.Server, dsn string)` helper assembles, per F6/F7:

1. `repository.Open(ctx, "sqlite", dsn)` with `dsn = "file:"+filepath.Join(t.TempDir(),"g.db")`, `repo.Migrate(ctx)`, `t.Cleanup(repo.Close)`.
2. `storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})`; `service.NewFileService(store, repo, logger)` (logger = `slog.New(slog.NewTextHandler(io.Discard, nil))`).
3. Auth: `authReg, _ := auth.Parse("boot-key:default:admin")` (env-seeded operator key, `admin` scope ⇒ any scope per `Key.Has`) then `authReg.WithStore(repo)` (persistent store ⇒ `Enabled()` true; `admin keys add` persists hashed).
4. **Gate on** (`gateOn=true`): `runtime, err := auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)` where `cfg = config.AuditGovernanceConfig{Enabled: true, Revision: 1, HMACKey: "0123456789abcdef0123456789abcdef" (32B), BaseURL/TokenURL: stub receiver URL (see D3), Bindings: []config.AuditGovernanceBinding{{TenantID: "acme", ClientID: "e2e-client", ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E", ClientSecret: "e2e-secret-0000", State: "active"}}, HTTPTimeoutSeconds: 5, PollMilliseconds: 1000, BatchSize: 8, ClaimTTLSeconds: 30, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 900, ReconcileBatchSize: 2, DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 2}` (all `Validate` constraints satisfied, F7; the `Enabled: true` field is exactly what `AUDIT_GOVERNANCE_ENABLED=true` maps to in `config_audit_governance.go:56`). `New` seeds the 0040 control + bindings rows (the direction's "control table seeded" gate). **Do not `Start()` the runtime** (F4 — outbox write is synchronous; the relay loop's delivery is the cmd/server e2e's scope). `repo = auditgovernance.WrapRepository(repo, runtime)` — the `main.go:82` pattern.
5. Router + chain: `r := rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger, false, nil, nil, 0, false)`; `final := server.ApplyMiddleware(r, repo, authReg, nil, &config.Config{}, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)`; `ts := httptest.NewServer(final)`; `t.Cleanup(ts.Close)`. Return `(ts, dsn)`.
6. **Gate off** (`gateOn=false`): identical assembly **without** step 4 (no runtime, no wrap) — the `main.go:70-83` disabled branch shape.

Outbox assertion helper (F4): open a second `sql.DB` on `dsn` (`modernc.org/sqlite`, already a direct dependency; precedent `cmd/server/governance_e2e_test.go:309-325` `outboxRow`) and expose `outboxRows(t, dsn) ([]outboxRow, error)` selecting `action, origin_kind, tenant_id, fact_kind, delivered_at_ns` from `audit_governance_outbox`.

Env handling (F9): `t.Setenv("AERO_ENDPOINT", ts.URL)` once; a `setClientEnv(t, key, tenant string)` helper swaps `AERO_API_KEY`/`AERO_TENANT` via `os.Setenv` + `t.Cleanup` restore (matrix-safe).

### REQ-2 — AC-1: activation gate, first fact reaches outbox + CLI-visible (B3-6 positive)

`TestCLIE2E_ActivationGate_FirstAdminFact` — server from REQ-1 with `gateOn=true`:

1. `setClientEnv(t, "boot-key", "")`; `Run([]string{"admin","keys","add","gov-key","--scopes","audit:event:write","--tenant","acme"})` → **exit 0**. This is both the B3-6 provisioning step (a key carrying the literal relay scope) and the **single mutation** whose audit entry is the first fact.
2. Outbox (no polling — synchronous, F4): **exactly one row**, `action="key.add"`, `origin_kind="admin"`, `tenant_id="acme"`, `fact_kind="security"` (F5: one audited action in the whole flow; `ListKeys`/`ListAudit` are not audited).
3. CLI visibility: `Run([]string{"admin","audit","list"})` (same boot key) → **exit 0**; captured stdout contains `"action":"key.add"` and `"tenant_id":"acme"` (AuditEntry JSON tags `repository.go:293-301`).

### REQ-3 — AC-2: gate off → CLI surfaces the disabled state (B3-6 negative)

`TestCLIE2E_GateOff_DisabledState` — server with `gateOn=false`:

1. `setClientEnv(t, "boot-key", "")`; `Run([]string{"admin","keys","add","gov-key","--scopes","admin","--tenant","acme"})` → **exit 0** (admin surface unaffected by gate state).
2. Outbox: `COUNT(*) == 0` — zero governance capture; the disabled state's observable signature through the CLI path.
3. `Run([]string{"admin","audit","list"})` → **exit 0**; stdout contains `"action":"key.add"` (the local `audit_log` entry is still written and visible — `RecordAudit` plain path).
4. `repo.(auditgovernance.Store).AuditGovernanceCanDisable(ctx)` → **safe=true, nil error** — the gate-off boot is a legal state (mirrors `cmd/server/audit_governance.go:18-31`).

### REQ-4 — AC-3: scope matrix on the audit admin path (B3-5)

`TestCLIE2E_ScopeMatrix_AuditAdmin` — server with `gateOn=true`; provision via boot key:
`admin keys add scope-gov --scopes audit:event:write --tenant acme`
`admin keys add scope-rw  --scopes read,write         --tenant acme`
`admin keys add scope-ro   --scopes read               --tenant acme`
`admin keys add scope-admin --scopes admin             --tenant acme`

Matrix (each row: `setClientEnv(t, key, "acme")`; `Run(["admin","audit","list"])`; assert exit code + captured stderr):

| Key | Scopes | Expected | Assertion |
|---|---|---|---|
| `scope-admin` | `[admin]` | 200 | exit 0; stdout is the JSON audit listing |
| `scope-rw` | `[read,write]` | 403 at the **admin gate** (passes read gate, F2) | exit 1; stderr contains `HTTP 403` and `admin scope required` |
| `scope-ro` | `[read]` | 403 at the admin gate | exit 1; stderr contains `HTTP 403` and `admin scope required` |
| `scope-gov` | `[audit:event:write]` | 403 at the **read gate** (relay scope grants nothing, F2/F3) | exit 1; stderr contains `HTTP 403` and `missing scope: read` |

The `scope-gov` row is the direction's "audit:event:write scope … 403 on audit admin without the scope" — the literal `RequiredScope` string round-trips the CLI provisioning path (F3) and is verified **not** to unlock the audit admin surface. Row expectations are gate-distinct (F8), so a regression in either gate fails its own row.

### REQ-5 — AC-4: usage/handler parity (grep test)

New file `internal/cli/cli_parity_test.go` (package `cli`), stdlib-only, no source parsing (all checks run against **runtime output** via the existing `captureStderr`/`captureStdout` helpers):

- **P1 (forward, top level):** capture `usage()` output; for every key in `cliHandlers` **except the declared help-alias group** `helpAliasGroup = {"help","-h","--help"}` (a test-local const), assert the key appears as a whole word (`\b` boundaries) in the captured text. A command added to the map but not to `usage()` fails.
- **P2 (reverse, top level):** parse the `commands:` block of the captured `usage()` text (lines indented by exactly two spaces; first whitespace token = command; for `admin`-prefixed lines the token is `admin`), assert every token is a key in `cliHandlers`. A command removed from the map but still documented fails.
- **P3 (alias group):** for each alias in `helpAliasGroup`, `Run([]string{alias})` exits 0 and prints the usage text — the aliases are pinned behaviorally as one documented command.
- **P4 (forward, admin):** a test-local dispatch table mirroring `cmdAdmin`'s switch (`cli_admin.go:38-63`) + the per-resource action switches (`cli_admin.go:65-77,164-182,338-348,400-410`; `cli_admin_buckets.go:12-24`; `cli_admin_files.go:14`): `{keys: [list add revoke], tenants: [list create delete status quota budget], jobs: [list retry], audit: [list], files: [delete], buckets: [lifecycle encryption website quota delete]}`. For every pair, assert `\b<resource> <action>\b` appears in captured `adminUsage()` output. A resource/action added to dispatch but not documented fails.
- **P5 (reverse, admin):** parse the `resources:` block of `adminUsage()` output (two-space-indented lines, first two tokens = resource/action), assert every documented pair is in the table. A documented pair whose dispatch was removed fails.
- **P6 (table ↔ switch, resource level):** for every resource in the table, `c.cmdAdmin([]string{resource, "__parity_probe__"})` (stub client, captured stderr) must **not** print `unknown admin resource`; for a resource absent from the table (e.g. `frobnicate`), it must. This closes the table↔switch loop behaviorally without source parsing.

**Verified current-state baseline:** P1–P6 all pass on the working tree (audited in §2: 12 non-alias keys all documented in `usage()`; 18/18 admin pairs documented in `adminUsage()`; both directions consistent; the working tree's recent `admin files delete` addition `cli.go:136`/`cli_admin.go:30`/`cli_admin_files.go:14` is fully in parity — the test would have caught a one-sided addition).

---

## 4. Decisions & non-goals

| # | Decision | Rationale |
|---|---|---|
| D1 | **Zero production-code changes** | The direction is test-only; the parity test as specified passes on the current tree (no `usage()` edit needed — the help aliases are handled by P3's declared group, §3 REQ-5). If the implementer prefers prose documentation of `help` in `usage()`, that is a cosmetic option, not a requirement. |
| D2 | **Two new test files** (`cli_governance_e2e_test.go`, `cli_parity_test.go`) | Keeps concerns separate; `_test.go` files are exempt from the 500-line gate (`engineering.yaml` filesize `ignore_patterns: ["_test.go"]`). |
| D3 | **Runtime constructed but not started; stub receiver for BaseURL/TokenURL** | Outbox write is synchronous with the mutation (F4) — no relay loop needed for AC-1, and not starting it removes delivery/claim races. The URLs must still pass `validateAuditGovernanceURL` (https or loopback http, `config_audit_governance.go:158-172`): an `httptest.NewServer` returning 500 (or `http://127.0.0.1:1`) satisfies this; a 500-stub keeps rows pending if a future change starts the loop. |
| D4 | **Inline assembly in the cli package (F6)** | `internal/integration` and `cmd/server` both import `internal/cli` ⇒ import cycle; the e2e replicates the `main.go:82` pattern + `internal/integration/fullserver_test.go` assembly instead of reusing either harness. |
| D5 | **Outbox asserted via direct SQL on the dsn (second connection), not via store accessors** | `OldestPendingAuditGovernance` proves ≥1 pending row but cannot assert the exact row/action/count ("first fact" = exactly one row). Precedent: `cmd/server/governance_e2e_test.go:309-325`. `modernc.org/sqlite` is already a direct dependency (I6-safe). |
| D6 | **Env↔config mapping documented, not executed** | `AUDIT_GOVERNANCE_ENABLED=true` maps to `cfg.Enabled=true` (`config_audit_governance.go:56`); the test constructs the config struct directly (sibling-harness precedent `internal/integration/fullserver_test.go:76-78`), avoiding `config.Load()`'s full env surface. The binding's `ClientSecretEnv` is a constant — env is only read by `config.Load`, not `Runtime.New` (F7). |
| D7 | **Matrix keys are scopes only — no `admin keys list` rendering assertions (F1)** | Real-server `admin keys list` renders empty columns (pre-existing CLI/server field-name drift, F1). AC-3 asserts HTTP behavior + stderr text only; F1 is recorded as a side-finding for a future direction, explicitly out of scope. |
| D8 | **No relay delivery / terminal / ID-recomputation assertions** | Those are the cmd/server matrix (`governance_e2e_test.go`) and the admin-origin cell spec's scope; duplicating them here would expand beyond the direction. |

**Non-goals:** ① production changes of any kind; ② the fake `/token` OAuth flow and relay delivery (cmd/server e2e); ③ deterministic fact-ID recomputation and terminal classification (admin-origin cell spec); ④ relay source-drift guards (grep-consistency spec); ⑤ new REST endpoints; ⑥ new dependencies (I6); ⑦ `admin keys list` rendering fix (F1).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

> The two supplied acceptance bullets are preserved below; each maps to the named test(s) that run under `go test ./...` inside `make check` (Makefile `test` target, `Makefile:18`).

**AC-1 — B3-6 (activation gate, first event):** `TestCLIE2E_ActivationGate_FirstAdminFact` (§3 REQ-2). *Preserved verbatim:* "cli-package e2e builds a server via WrapRepository with AUDIT_GOVERNANCE_ENABLED=true + one active binding, provisions a key with audit:event:write via admin keys add, drives one mutation, and asserts the first fact reaches the outbox and is visible through the CLI admin path". Testable form: `gateOn` server; `admin keys add gov-key --scopes audit:event:write --tenant acme` exits 0; outbox has exactly one row (`action='key.add'`, `origin_kind='admin'`, `tenant_id='acme'`, `fact_kind='security'`); `admin audit list` exits 0 and its stdout contains `"action":"key.add"` + `"tenant_id":"acme"`.

**AC-2 — B3-6 (negative):** `TestCLIE2E_GateOff_DisabledState` (§3 REQ-3). *Preserved verbatim:* "negative case: gate off -> CLI surfaces the disabled state". Testable form: `gateOn=false` server; `admin keys add` exits 0; `admin audit list` exits 0 and shows the local entry; outbox `COUNT(*) == 0`; `AuditGovernanceCanDisable` returns safe — the CLI path operates normally and no governance fact exists, which is the disabled state's observable signature through the CLI (no dedicated status surface exists today; adding one is out of scope).

**AC-3 — B3-5 (scope matrix):** `TestCLIE2E_ScopeMatrix_AuditAdmin` (§3 REQ-4). *Preserved verbatim:* "matrix test across scope-present/scope-absent keys asserting 403 on audit admin without the scope". Testable form: four provisioned keys; `[admin]` → 200/exit 0; `[read,write]` and `[read]` → 403 `admin scope required` (the audit-admin gate); `[audit:event:write]` → 403 `missing scope: read` (the relay scope grants nothing on the admin surface) — each row asserting exit code 1 + its gate-distinct stderr line.

**AC-4 — B3-5 (parity):** `TestCLI_UsageParity` (P1–P3) + `TestCLI_AdminUsageParity` (P4–P6) (§3 REQ-5). *Preserved verbatim:* "grep/parity test asserting every cliHandlers key and admin resource/action appears in usage()/adminUsage() text (and vice versa)". Testable form: P1/P4 forward text coverage, P2/P5 reverse coverage, P3 alias-group behavioral pin, P6 table↔switch closure. Baseline (verified): passes on the current tree.

---

## 6. Risks

| # | Risk | Mitigation |
|---|---|---|
| R1 | The direction's two scope domains (relay OAuth scope vs API-key scopes) are easy to conflate in implementation — a key with `audit:event:write` does **not** pass any API-key gate | §2 F2/F3 and REQ-4's gate-distinct stderr assertions pin the distinction; the matrix asserts the 403 *reason* per row, not just the status |
| R2 | Relay delivery racing the outbox assertion if the runtime is started | D3: runtime constructed but never `Start()`ed; outbox write is synchronous (F4) |
| R3 | `t.Setenv` duplicate-key panic across matrix rows | F9 + REQ-1's `setClientEnv` helper (os.Setenv + cleanup restore) |
| R4 | Assembling the server duplicates production wiring and could drift from `main.go` | The e2e uses the same public constructors + `server.ApplyMiddleware` 12-ring chain as production (F7); the cmd/server matrix's `newGovernanceE2E` is the same-shape sibling — any assembly drift is caught by the e2e's own assertions |
| R5 | Parity test false-positives from prose (e.g. "audit" appearing in "admin audit list" text) | P1/P4 use whole-word `\b` token matching against captured runtime output; P2/P5 parse only the indented command/resource blocks, not prose |
| R6 | Future work adds a legitimate alias-style command (e.g. `-v`) and trips P1 | P3's declared `helpAliasGroup` is the documented escape hatch; adding a new alias requires an explicit test-table amendment — by design, not silent |
