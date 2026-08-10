# Internal CLI Activation-Gate + `audit:event:write` Scope-Matrix E2E — v1 Design

> Parent spec: `docs/requirements/internal-cli-activation-gate-scope-matrix-e2e-v1.spec.md`
> Direction: **test-only** — two new files in `internal/cli/` (package `cli`), zero production-code changes, zero new dependencies (I6). The CLI admin path and `internal/auditgovernance` are read-only inputs.
> Verification basis: working tree at HEAD `15763e2` (same as the parent spec).
> Sibling designs (different cell, do not conflict): `activation-gate-scope-alignment-matrix-e2e-v1/v2.design.md` — the `cmd/server` file-origin cell; this design is the **CLI-driven cell** and deliberately does not duplicate delivery/terminal/ID-recomputation assertions.

---

## 1. Evidence verification ledger (untrusted claims → verdicts)

Every claim in the parent spec (and the direction citations it restates) was re-verified against the working tree. All 8 direction citations hold; the spec's additional facts F1–F9 hold; **two spec claims are corrected** (C1, C2) and **two attributions are refined** (C3, C4) — none change the testable design.

| # | Claimed | Verdict | Verified location |
|---|---------|---------|-------------------|
| E1 | `internal/auditgovernance/model.go:17` `RequiredScope = "audit:event:write"` | ✅ exact | `model.go:10-21` const block, `:17` |
| E2 | `adminKeysAdd` `--scopes` provisioning (`cli_admin.go:113-129`) | ✅ substance exact; line drift | `cli_admin.go:107-131`; `--scopes` parsed `:116-120`; body `{"token","tenant","label","scopes"}` → `POST /v1/admin/keys` `:124-126`; `scopes` comma-split verbatim `:121-122` (no known-scope filter) |
| E3 | `cmd/server/main.go:82,212` `WrapRepository` assembly | ✅ exact | `main.go:82` (run), `:212` (runMCP); both after `auditRuntime.Start(ctx)` |
| E4 | `cliHandlers` map vs `usage()` (`cli.go:51-77`/`:118-137`) | ✅ substance exact; line drift + count corrected | `cli.go:70` (var), `:73-93` (**15 entries = 12 non-alias + `help`/`-h`/`--help`** — review correction: not 14), `usage()` `:108-137` |
| E5 | `adminUsage()` (`cli_admin.go:25-36`) | ✅ substance exact; line drift | `cli_admin.go:14-36`; 6 resources / 18 pairs (keys 3, tenants 6, jobs 2, audit 1, files 1, buckets 5) |
| E6 | `TestRun_AdminAudit_Dispatches` stub-only (`cli_test.go:1715`) | ✅ exact | `cli_test.go:1715-1737`: `httptest.NewServer` + path assertion; no auth/server assembly; all 77 cli tests are stub-level (E8) |
| E7 | 0040 control/bindings migration; bindings seeded by `Runtime.New`, not the migration | ✅ exact | `0040_audit_governance_control.up.sql` (control singleton + `audit_governance_bindings` + delivered_origins); `runtime.go:329-353` `applyDesiredBindings` → `store.ApplyAuditGovernanceBindings`; postgres pair exists (I2) |
| E8 | "all cli tests are stub httptest"; no cli test imports repo/service/rest/auditgovernance | ✅ holds | `internal/cli/*_test.go` import stdlib only |
| F1 | Real-server `admin keys list` renders empty columns (Go field names vs CLI snake_case) | ✅ holds (structurally) | `AdminHandler.ListKeys` (`admin.go:105-111`) returns raw `auth.Key` — fields `Token/Tenant/Scopes/…` have no json tags (`auth.go:40-45`); CLI parses `token_hash/tenant_id/scopes/label` (`cli_admin.go:79-105`). Out of scope; never asserted |
| F2 | Two-gate scope chain on `/v1/admin/audit`: `requireRESTScope` (GET⇒`read`) then `requireAdmin` (`admin`) | ✅ holds, **attribution refined (C3)** | `router.go:227` `r.Use(requireRESTScope(reg))`; `requireRESTScope` GET/HEAD/OPTIONS⇒read `router.go:365-375`; `requireAdmin` `admin.go:455-467` → 403 JSON `Forbidden`/`admin scope required`; `Key.Has` admin⇒any `auth.go:46-49`. **Refinement:** for a `[audit:event:write]` key the read-gate 403 actually fires first at the auth middleware's own `checkScope(method, k)` (`auth_middleware.go:162-164`, `:189`) — a chain ring *outside* the router — with the identical plain-text `missing scope: read`. Both sites are second-line defenses for each other; the observable stderr line is the same, so the matrix assertion is site-agnostic |
| F3 | `AddKey` accepts arbitrary scope strings (no `knownScope` filter); round-trips verbatim | ✅ exact | `admin.go:114-142` — only non-empty token/tenant/scopes validated; `knownScope` (`auth.go:139`) applies only to env-seeded `AUTH_KEYS` (`auth.go:110-123`); `scopesToString`/`parseScopeString` round-trip (`store.go:42-63`) |
| F4 | Outbox write synchronous with the mutation; no relay run needed | ✅ exact | `auditedRepository.RecordAudit` (`auditgovernance/repository.go:27-41`) → `RecordAuditWithGovernance` (`repository/audit_governance_write.go:20-47`) inserts `audit_log` + outbox in one tx; `fact_kind='security'` for `key.*` (`auditgovernance/facts.go:89-93`); `Capture()` truth = in-memory `states` map from `cfg.Bindings` (`runtime.go:187-189`) |
| F5 | `AddKey` audits `key.add` with `TenantID = body.Tenant`; ListKeys/ListAudit not audited | ✅ exact | `admin.go:141` `h.audit(r, "key.add", body.Tenant, …)`; no `h.audit` call in `ListKeys` (`:105-111`) or `ListAudit` (`:430-441`) — exactly one audited action per `keys add` |
| F6 | Import-cycle: `cmd/server` + `internal/integration` import `internal/cli` ⇒ inline assembly required | ✅ exact | `cmd/server/main.go:16`; `internal/integration/admin_files_delete_test.go:25`, `authz_cli_failclosed_test.go:33`; the 9 importable packages verified clean (`grep` count 0 each) |
| F7 | Assembly prerequisites (router sig, 12-ring chain, nil rl, unknown tenants, Store assertion, Validate constraints, env read only by `config.Load`) | ✅ holds, **one refinement (C4)** | `rest.NewRouter` `router.go:214` (13 positional + opts; admin routes unconditional `:329-353`); `server.BuildChain` 12 rings `chain.go:58-85` (Auth ring outside the router ⇒ C3); `(*RateLimiter).Middleware()` nil→pass-through `ratelimit.go:141-144`; `NewConcurrencyLimiter(0)` → empty sem `middleware.go:124-129`; `MaxBodySize(0)` → pass-through `validation.go:65-68` (zero `&config.Config{}` is safe); unknown tenants pass `internal/middleware/tenant_status.go:19-33` (review correction: package path pinned); `sqlStore` implements `auditgovernance.Store` (`audit_governance_types.go:108`); spec's proposed cfg satisfies every `Validate` constraint (worker `config_audit_governance.go:244-250`, retry `:252-259`, bounded `:261-268`, bindings `:277-307`, HMAC≠secret `:185-190`) |
| F8 | CLI 403 error contract: JSON envelope ⇒ `HTTP 403 Forbidden: admin scope required`; plain ⇒ `HTTP 403: missing scope: read`; exit 1 | ✅ exact | `renderError` `response.go:43-60` (JSON branch `:48-54`); `readSuccessfulResponse` exits 1 on ≥300 `:85-92`; `cmdAdminAudit` (`cli_admin.go:400-427`) uses `renderError` at `:417` (sibling admin list paths at `:372,:392,:445,:465`) |
| F9 | `t.Setenv` panics on duplicate keys ⇒ matrix must swap env via `os.Setenv` + restore | ✅ holds | Go testing contract; `NewClient()` re-reads env on every `Run` (`cli.go:33-43`) so per-row env swapping is effective |
| C1 | **Spec claim "server-side matrix `cmd/server/governance_e2e_test.go` … unlanded in the working tree" is FALSE** | ⚠️ corrected | The file **is landed**: 489 lines, `newGovernanceE2E` harness at `:182`, 5 tests incl. `TestGovernanceE2EActivationGateBoundTenant`/`UnboundTenant` (`:362,396`). Design impact: none — the CLI cell still cannot import `cmd/server` (F6); the landed sibling is a same-shape pattern to mirror, and its `outboxRow` precedent is real |
| C2 | Spec cites `outboxRow` precedent at `governance_e2e_test.go:309-325` | ⚠️ line drift | `outboxRow` is at `:279`; `:309-325` is `waitForRow`/`factIDForEvent` region. Substance unaffected |
| C4 | Spec D6 "env only read by `config.Load`, not by `Runtime.New`" | ✅ confirmed with mechanism | `resolveAuditGovernanceSecrets` (`config_audit_governance.go:142-165`, `os.LookupEnv`) is called only from the bindings-file loader (`:126`) inside `config.Load`; `Runtime.New` → `Validate` (requires syntactically valid `ClientSecretEnv` + non-empty `ClientSecret`), never reads env. Direct construction with `ClientSecret` set works without the env var existing |

**Additional load-bearing facts verified for this design:**
- `auth.Parse("boot-key:default:admin")` — `token:tenant:scopes` format (`auth.go:110-121`), `admin` is a known scope; `Registry.Enabled()` is true once `.WithStore(repo)` is set (`auth.go:143-147`) ⇒ `requireAdmin` no longer short-circuits (`admin.go:457-461`) and `requireRESTScope` engages.
- Persisted keys authenticate: `authenticateBearer` → `Lookup` (`auth.go:222-247`) → `lookupStore(HashToken(token))`; tenant-scoped key with matching/absent `X-Aero-Tenant` proceeds (`auth_middleware.go:155-161`). No `WithKeyCache` in the harness ⇒ every matrix row reads fresh from the store (no staleness).
- `checkScope(method, k)` (`auth_middleware.go:189`) requires `read` for GET ⇒ the `[audit:event:write]` row is rejected at the Auth ring with plain-text `missing scope: read` **before** the router runs (C3); the `[read]`/`[read,write]` rows pass the Auth+router read gates and are rejected at `requireAdmin` with the JSON envelope. Gate-distinct stderr lines per row are therefore stable.
- `AuditGovernanceCanDisable` (`repository/audit_governance_binding.go:154-160`): `NOT EXISTS(bindings) AND NOT EXISTS(outbox WHERE delivered_at_ns=0)`. Gate-off harness: 0040 creates empty tables (bindings only seeded by `Runtime.New`) ⇒ `safe=true` deterministically.
- `modernc.org/sqlite v1.50.1` is a direct dependency; the driver is registered by `internal/repository`'s import, so the harness's second raw connection (`sql.Open("sqlite", dsn)`) works — precedent `governance_e2e_test.go:279-280`; `repository.Open` uses WAL (`sqlite.go:31`) ⇒ a second read-only connection never sees `SQLITE_BUSY`.
- Filesize gate exempts tests: `make check` uses `find . -name '*.go' -not -name '*_test.go'` — the `find` sits at `Makefile:175` (review correction; 174 is the echo line).
- No naming collisions: no existing `cli_governance_e2e_test.go`, `cli_parity_test.go`, or `TestCLIE2E_*`/`TestCLI_*Parity*` identifiers.

**Review dispositions (second verification pass — every actionable review item has an explicit disposition):**

| Review item | Disposition | Location |
|---|---|---|
| R7 — AC-1 must assert the literal scope string `audit:event:write` in stdout (201 echo or audit Detail), closing the truncate+validation-relax wrong-reason vector | **Incorporated** — AC-1(a) asserts the literal in the 201 echo; AC-1(c) asserts it in the audit `Detail`; FM4 detection/mitigation extended; §3 contract table updated | §2.3 AC-1 step 1, §5 FM4, §7 AC-1, §8 R7 |
| R8 — AC-4 P1/P4 must require block-line membership (first token / first two tokens of indented block lines), closing the prose-only forward direction | **Incorporated** — P1 and P4 strengthened to first-token / first-two-tokens of indented `commands:`/`resources:` block lines; P1+P2 and P4+P5 are now bidirectional closures; verified satisfiable on the current tree (12/12 keys, 18/18 pairs) | §2.4, §5 FM6, §7 AC-4, §8 R4/R8 |
| Corrected facts — gate-distinct assertion belongs to AC-3 (not AC-1); cliHandlers has 15 entries (not 14); filesize find at Makefile:175; tenant_status.go under internal/middleware/ | **Already correct / fixed** — gate-distinct was already attributed solely to AC-3 (§2.3 matrix, §7 AC-3 row; AC-1 asserts only exit-0/outbox/stdout tokens, no 403s); E4 count corrected to 15; Makefile pinned to :175; tenant_status.go path pinned | §1 E4/F7 + ledger bullets, §2.3, §5 FM4, §7, §8 K4 |
| Security findings — Finding A (anonymous public-read dead through the 12-ring chain), `+` scope round-trip asymmetry, tenant-scoped-admin-key operator-equivalence | **Disposed** — S1/S2 rejected for this test-only direction with evidence + recorded follow-ups; S3 accepted as the documented operator-equivalence model with a deployment caveat | §2.5 (new), §8 non-goals |

---

## 2. Design

### 2.1 Scope and shape

Two new files, package `cli`, stdlib-only, no production changes:

| File | Purpose | Tests |
|---|---|---|
| `internal/cli/cli_governance_e2e_test.go` | Inline real-server harness (SQLite + local FS + auth + `auditgovernance.WrapRepository`, mirroring `main.go:79-85` + `server.ApplyMiddleware`), driven exclusively through `cli.Run(...)` | AC-1, AC-2, AC-3 |
| `internal/cli/cli_parity_test.go` | Runtime-output parity between `cliHandlers` ↔ `usage()` and admin dispatch ↔ `adminUsage()` | AC-4 (P1–P6) |

### 2.2 Harness — `newGovernanceE2EServer(t, gateOn) (*httptest.Server, string)`

Assembly (all pieces verified in §1):

1. `repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "g.db"))`; `Migrate(ctx)`; `t.Cleanup(repo.Close)`.
2. `storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})`; `service.NewFileService(store, repo, logger)` with `logger = slog.New(slog.NewTextHandler(io.Discard, nil))`.
3. Auth: `authReg, _ := auth.Parse("boot-key:default:admin")`; `authReg.WithStore(repo)` — operator key, `admin` scope (⇒ any scope), registry enabled.
4. **Gate on:** `cfg := config.AuditGovernanceConfig{Enabled: true, Revision: 1, HMACKey: "0123456789abcdef0123456789abcdef", BaseURL/TokenURL: <500-stub httptest URL>, Bindings: [{TenantID: "acme", ClientID: "e2e-client", ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E", ClientSecret: "e2e-secret-0000", State: "active"}], HTTPTimeoutSeconds: 5, PollMilliseconds: 1000, BatchSize: 8, ClaimTTLSeconds: 30, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 900, ReconcileBatchSize: 2, DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 2}` — satisfies all `Validate` constraints (F7/C4); `auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)` seeds 0040 bindings via `applyDesiredBindings`. **Never `Start()` the runtime** (F4 — outbox write is synchronous; zero token fetches by construction, v2 sibling A5). `repo = auditgovernance.WrapRepository(repo, runtime)`. **Gate off:** same assembly without step 4.
5. `r := rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger, false, nil, nil, 0, false)`; `final := server.ApplyMiddleware(r, repo, authReg, nil, &config.Config{}, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)` (zero cfg is safe — MaxBodySize(0)/CORS-empty/nil-rl all pass-through, F7); `ts := httptest.NewServer(final)`; `t.Cleanup(ts.Close)`.

Environment:
- `t.Setenv("AERO_ENDPOINT", ts.URL)` once (auto-restored).
- `setClientEnv(t, key, tenant string)`: `os.Setenv("AERO_API_KEY", key)`, `os.Setenv("AERO_TENANT", tenant)` + `t.Cleanup` restore — never `t.Setenv` for these two (F9). `Run` re-reads env per call (F9), so row swapping is effective.
- `outboxRows(t, dsn)`: second raw `sql.DB` on the dsn; `SELECT action, origin_kind, tenant_id, fact_kind, attempts, delivered_at_ns FROM audit_governance_outbox` with `?` placeholders (test-side raw SQL; I1's rebind is production-side; precedent §1 C2). The harness DB is per-test (`t.TempDir`) ⇒ no cross-test contamination.

### 2.3 AC tests

**`TestCLIE2E_ActivationGate_FirstAdminFact` (AC-1, gate-on):**
1. `setClientEnv(t, "boot-key", "")`; `Run(["admin","keys","add","gov-key","--scopes","audit:event:write","--tenant","acme"])` → exit 0 **and stdout contains the literal `audit:event:write`** — the 201 echo `{"tenant","scopes"}` (`admin.go:141-142`) is printed verbatim by `printResponseBody` (`cli_admin.go:143-144`). **This is the R7 hardening**: it closes the residual truncate+validation-relax wrong-reason vector — a future `knownScope`-style truncation paired with a relaxed `len(scopes)>0` validation would store an empty-scope key whose echo lacks the literal, failing (a). (B3-6 provisioning: the literal `RequiredScope` is now a legal API key that grants nothing.) The audit `Detail` (`token=**** scopes=[audit:event:write]`) at step 3 is the redundant second surface.
2. Outbox: **exactly one row**, `action="key.add"`, `origin_kind="admin"`, `tenant_id="acme"`, `fact_kind="security"`, and (hardening over the spec) `attempts=0 AND delivered_at_ns=0` — pins "constructed, never started" (D3).
3. `Run(["admin","audit","list"])` → exit 0; stdout contains `"action":"key.add"`, `"tenant_id":"acme"`, **and `audit:event:write`** — the audit `Detail` is `token=**** scopes=[audit:event:write]`, printed raw (AuditEntry tags `repository.go:293-301`; `cmdAdminAudit` prints the raw JSON body). Redundant second surface for R7.

**`TestCLIE2E_GateOff_DisabledState` (AC-2, gate-off):**
1. `Run(["admin","keys","add","gov-key","--scopes","admin","--tenant","acme"])` → exit 0 (admin surface unaffected by gate state).
2. Outbox `COUNT(*) == 0` (zero governance capture).
3. `Run(["admin","audit","list"])` → exit 0; stdout contains `"action":"key.add"` (plain `RecordAudit` path still writes `audit_log`).
4. `repo.(auditgovernance.Store).AuditGovernanceCanDisable(ctx)` → `safe=true, nil` (mirrors `cmd/server/audit_governance.go:18-31`'s disabled-branch precondition; deterministic — empty bindings + empty outbox, §1).

**`TestCLIE2E_ScopeMatrix_AuditAdmin` (AC-3, gate-on):**
Provision via boot key: `scope-admin`→`admin`, `scope-rw`→`read,write`, `scope-ro`→`read`, `scope-gov`→`audit:event:write` (all `--tenant acme`). Rows (`setClientEnv(t, key, "acme")`; `Run(["admin","audit","list"])`):

| Key | Scopes | Exit | stderr must contain | Gate that fires |
|---|---|---|---|---|
| scope-admin | `[admin]` | 0 | — (stdout = JSON listing) | none |
| scope-rw | `[read,write]` | 1 | `HTTP 403` and `admin scope required` | admin gate (`requireAdmin`, JSON envelope) |
| scope-ro | `[read]` | 1 | `HTTP 403` and `admin scope required` | admin gate |
| scope-gov | `[audit:event:write]` | 1 | `HTTP 403` and `missing scope: read` | read gate (Auth-ring `checkScope`; router `requireRESTScope` is the second-line same-text defense — C3) |

Row expectations are gate-distinct (F8): a regression in either gate fails its own row only. The `scope-gov` row is the direction's proof that the relay's `RequiredScope` literal, round-tripped through CLI provisioning (F3), unlocks nothing on the admin surface.

### 2.4 Parity tests (AC-4)

Runtime-output only, via the existing `captureStdout`/`captureStderr` (`cli_test.go:28,48`); no source parsing; no production edits.

- **P1 (forward, block-membership — R8 hardening):** every `cliHandlers` key except `helpAliasGroup = {"help","-h","--help"}` (test-local const) must be the **first whitespace token of some two-space-indented line** in `usage()`'s `commands:` block (reusing P2's block parser). Whole-text `\b` presence is no longer sufficient — a command mentioned only in prose fails P1. P1+P2 together form a bidirectional block↔map closure.
- **P2 (reverse):** parse only the two-space-indented lines of `usage()`'s `commands:` block; first whitespace token must be a `cliHandlers` key (multiword lines like `snapshot create` yield the map key `snapshot`).
- **P3 (aliases):** `Run([alias])` → exit 0 + usage text, for each of the three aliases — pins the alias group behaviorally as one documented command.
- **P4 (forward admin, block-membership — R8 hardening):** test-local dispatch table mirroring `cmdAdmin`'s switch + per-resource action switches: `{keys:[list,add,revoke], tenants:[list,create,delete,status,quota,budget], jobs:[list,retry], audit:[list], files:[delete], buckets:[lifecycle,encryption,website,quota,delete]}` (18 pairs); every table pair must be the **first two whitespace tokens of some two-space-indented line** in `adminUsage()`'s `resources:` block (reusing P5's block parser) — prose-only documentation no longer satisfies it. P4+P5 form the bidirectional closure.
- **P5 (reverse admin):** parse `adminUsage()`'s `resources:` block (two-space indent, first two tokens); every documented pair must be in the table.
- **P6 (table↔switch closure):** for each table resource, `(&Client{}).cmdAdmin([]string{resource, "__parity_probe__"})` must **not** print `unknown admin resource` (it prints `<resource> action` errors — dispatch exists, no HTTP is made); for `frobnicate` it must. Verifies the six resource switches (`cli_admin.go:38-77,164-182,338-348,400-410`; `cli_admin_buckets.go:12-24`; `cli_admin_files.go:12-18`).

Baseline (verified, §1 E4/E5): P1–P6 pass on the current tree — all 12 non-alias keys head `commands:`-block lines, all 18/18 admin pairs head `resources:`-block lines (first token / first two tokens, verified in-tree), `admin files delete` fully in parity.

### 2.5 Security-review dispositions (three findings, all explicitly disposed)

From the security review at HEAD `15763e2`. None changes the testable design; each is either rejected with evidence for this test-only direction or accepted as the documented model.

| # | Finding | Disposition | Evidence |
|---|---------|-------------|----------|
| S1 | **Finding A — `AUTH_ANONYMOUS_PUBLIC_READ` is dead through the 12-ring chain:** anonymous object GET passes the Auth ring (`withAnonymousPrincipal`, `auth_middleware.go:142-146`) but is rejected by the router's `requireRESTScope`→`Require(read)` with `401 not authenticated` (`auth_middleware.go:206-209`); the documented contract (`docs/configuration.md:194`) and the passing ACL tests (which mount `reg.Middleware()` **without** `requireRESTScope`) both expect 200 | **REJECTED for this direction** — the fix (anonymous-read carve-out in `Require`/`requireRESTScope` + a 12-ring integration test) is a **production change**, which this test-only direction must not make (D1/K3). Fail-closed (401, not escalation), so it cannot corrupt any AC-1..AC-4 assertion. Recorded follow-up: the AC-3 harness and the landed sibling harness (`cmd/server/governance_e2e_test.go`) are the designated 12-ring vehicles | Every AC matrix row uses authenticated, freshly provisioned keys; no assertion exercises the anonymous class; the finding's own empirical evidence is the production chain, orthogonal to CLI-driven provisioning |
| S2 | **`+` scope round-trip asymmetry (persisted path):** `scopesToString`/`parseScopeString` (`store.go:42-63`) join/split on `+`, so a scope like `audit:event:write+admin` stays inert in the in-memory path but splits into two scopes — including real `admin` — after a persisted-store round-trip | **REJECTED for this direction** — callers are admin-only (AddKey/IssueJWT); the split can only produce `read`/`write`/`admin`, all already grantable by the same admin caller, so it is **not a non-admin escalation**; the fix (validate scopes at `AddKey` with the `knownScope` set, or reject `+`) is a production change. Recorded follow-up. No fixture scope contains `+`; AC-1's literal-scope assertion makes any future `+`-containing fixture a loud failure by construction | `Key.Has` honors only `read`/`write`/`admin` (`auth.go:46-49`); provisioning path is `requireAdmin`-gated (`admin.go:114-142`); observed, admin-only hygiene gap |
| S3 | **Tenant-scoped admin key = operator equivalence:** `requireAdmin` checks scope only, never the caller's tenant; `AddKey`/`IssueJWT`/`DeleteFile` take the target tenant from body/path — a `{tenant: acme, scopes:[admin]}` key (exactly the shape AC-2/AC-3 provision) can mint keys for any tenant, delete files in any tenant, set quotas | **ACCEPTED as the documented model, with a deployment caveat** — `admin_files_delete.go:10-14` states the admin surface is cross-tenant by design (“operator-equivalence model”). AC-2/AC-3 provision `--tenant acme --scopes admin` keys **solely** to assert in-tenant gate behavior (exit 0 for `admin`, 403 rows for non-admin scopes); no assertion exercises cross-tenant capability and none can — the direction is test-only. Caveat recorded for deployment docs: never issue tenant-scoped admin keys unless operator equivalence is desired | `requireAdmin` (`admin.go:455-467`) checks `k.Has(ScopeAdmin)` only; the design's rows all run with `setClientEnv(t, key, "acme")` and assert only per-row gate outcomes within `acme` |
| S4 | **4 of 18 CLI `admin` pairs hit tenant-write routes, not the admin gate:** `buckets lifecycle|encryption|website|delete` → `PUT/DELETE /v1/buckets/{bucket}[/lifecycle|encryption|website]` (tenant-level registration `router.go:272-294`, write-scope gated by `requireRESTScope` only; `PutBucketLifecycle` is an `AdminHandler` method but deliberately registered tenant-level **without** `requireAdmin`, `admin.go:207-232`); only `buckets quota` → `PUT /v1/admin/buckets/{bucket}/quota` is admin-gated (`router.go:198`, `admin.go:239`) | **Naming nuance, not an escalation — documented, not renamed:** the four routes are tenant-confined writes (auth ring pins `X-Aero-Tenant` to the key's tenant), consistent with their OpenAPI `buckets` tag; no admin verb is ungated. A rename would break the P4/P5 18/18 pin and existing scripts. Fix applied: column-0 `note:` line in `adminUsage()` (`cli_admin.go`) stating the four actions are tenant-scoped writes and only `quota` is admin-gated — outside the two-space-indented block, so P4/P5 parsing is unaffected (verified: `go test ./internal/cli/` green) | `cli_admin_buckets.go:51,81,128,183` (tenant routes) vs `:164` (admin route); P4/P5 parse only two-space-indented block lines (`adminUsage()` `cli_admin.go:14-38`); OpenAPI tags `buckets` vs `admin` (`router.go:89-111,196-198`) |

---

## 3. API changes

**Production API changes: none.** This direction is test-only (D1). The design instead *pins* the following existing contracts as read-only inputs — any future production change that alters one fails the new tests loudly:

| Contract | Pinned surface | Pinned by |
|---|---|---|
| CLI env contract | `AERO_ENDPOINT` / `AERO_API_KEY` / `AERO_TENANT` read per `Run` (`cli.go:33-43`); `Authorization: Bearer` + `X-Aero-Tenant` (`cli.go:60-69`) | AC-1/AC-2/AC-3 |
| CLI error contract | exit 1 on HTTP ≥300; 403 rendering: JSON envelope `HTTP 403 Forbidden: admin scope required` vs plain `HTTP 403: missing scope: read` (`response.go:43-60`) | AC-3 |
| REST admin gate | `requireAdmin` 403 JSON (admin.go:463); scope gate plain-text 403 (auth_middleware.go:163, router.go:365-375) | AC-3 |
| Key provisioning | `POST /v1/admin/keys` accepts arbitrary scope strings, verbatim round-trip — asserted **literally** in the 201 echo and the audit `Detail` (R7); `key.add` audited with body tenant; ListKeys/ListAudit un-audited | AC-1 (exactly-one-row + literal scope echo), AC-3 |
| Governance capture | `Capture(tenant)` in-memory truth; `RecordAuditWithGovernance` single-tx outbox write (0039 schema) | AC-1, AC-2 |
| Disable safety | `AuditGovernanceCanDisable` = empty bindings ∧ no pending outbox | AC-2 |

New *test-surface* API (package `cli`, test files only): `newGovernanceE2EServer`, `setClientEnv`, `outboxRows`, `helpAliasGroup`, the parity dispatch table. No exported production symbols change.

---

## 4. Compatibility constraints

| # | Constraint | Consequence |
|---|---|---|
| K1 | **Import cycle** (F6): `cmd/server`, `internal/integration` import `internal/cli` | Harness assembled inline in-package from the 9 importable packages; never import the sibling harnesses |
| K2 | **I6 stdlib-first** | No new `go.mod` deps: `modernc.org/sqlite` already direct; stdlib `testing` only (no assert frameworks) |
| K3 | **I5 opt-in safety** | Gate-on path is explicit config construction; runtime never `Start()`ed ⇒ no network, no delivery, no token fetch; gate-off path is the default assembly |
| K4 | **Filesize gate** | `_test.go` exempt (filesize `find` at `Makefile:175`) — keep both files < 500 lines anyway (targets: e2e ≈ 300, parity ≈ 180) |
| K5 | **`make check`** | New tests run under `go test ./...`; SQLite + local FS + loopback `httptest` only (CI zero-network-compatible, precedent `cmd/server/governance_e2e_test.go`) |
| K6 | **Race discipline** | No `t.Parallel()` (env mutation); no goroutines in the harness (no relay); matrix rows sequential; `go test -race ./internal/cli/` clean by construction |
| K7 | **Env hygiene** | `AERO_ENDPOINT` via `t.Setenv` (once); `AERO_API_KEY`/`AERO_TENANT` via `os.Setenv` + cleanup (F9); per-test DB via `t.TempDir()` ⇒ no cross-test state |
| K8 | **SQLite concurrency** | WAL + repo pool (`sqlite.go:31`); harness raw connection is a read-only WAL reader; test writes single-goroutine pre-assertion — no `SQLITE_BUSY` exposure |
| K9 | **Cleanup ordering** | `t.Cleanup` LIFO: register `ts.Close` first, `repo.Close` second, (gate-on) `rt.Close` last ⇒ execution `rt.Close → repo.Close → ts.Close` (v2 sibling F1) |
| K10 | **I2 migrations** | No migration files touched; harness relies on `Migrate` applying 0039/0040 (both sqlite+postgres pairs exist) |

---

## 5. Failure modes

| # | Failure mode | Detection | Mitigation / design response |
|---|---|---|---|
| FM1 | Relay accidentally started (now or future) — delivery/claim race pollutes the outbox assertion | AC-1 "exactly one row" fails; hardened `attempts=0 AND delivered_at_ns=0` assertion fails | Runtime constructed, never started (D3); 500-stub BaseURL/TokenURL keeps any hypothetical delivery failing loudly; the hardened row fields make the "not started" property *asserted*, not incidental |
| FM2 | `t.Setenv` duplicate-key panic across matrix rows | Test panic | `setClientEnv` uses `os.Setenv` + `t.Cleanup` restore (F9) |
| FM3 | Env leakage into/from other cli tests (`AERO_ENDPOINT` etc.) | Wrong endpoint errors, flaky ordering | `t.Setenv` auto-restore; each test builds its own server; no shared env state |
| FM4 | Scope-domain conflation: `audit:event:write` (relay OAuth scope) mistaken for an API-key scope; residual wrong-reason vector: `knownScope`-style truncation **and** relaxed `len(scopes)>0` validation would store an empty-scope key that still 403s `missing scope: read` | Silent false-pass if the matrix asserted only status codes / if the scopes string were never asserted | Gate-distinct stderr assertions per row (`admin scope required` vs `missing scope: read`) — each gate regression fails its own row (R1); AC-1(a)/(c) additionally assert the **literal** `audit:event:write` in stdout (201 echo + audit `Detail`) — an empty-scope key fails (R7) |
| FM5 | Stale persisted-key authentication (hash lookup) | All matrix rows fail | No `WithKeyCache` in harness ⇒ fresh store lookup per request (§1); keys provisioned before their row |
| FM6 | Parity false positives from prose (e.g. `audit` in `admin audit list` help text) — including the prose-only forward direction (a command dropped from the block but still mentioned in prose) | P1/P4 vacuous | P1/P2/P4/P5 all require block-line membership (keys/pairs must head indented block lines, R8) — prose is never sufficient in either direction; `\b` whole-word matching retained as the tokenizer; `helpAliasGroup` declared (R4/R5) |
| FM7 | New alias-style command added (`-v`) tripping P1 | P1 fails | Declared `helpAliasGroup` escape hatch; adding an alias requires an explicit test-table amendment — loud by design (R6) |
| FM8 | Parity table drift from the actual switches (a resource added only in dispatch) | P4 fails (missing doc), P6 catches probe behavior | P6 closes table↔switch behaviorally without source parsing |
| FM9 | `MaxBodySize`/CORS/ratelimit ring misassembly in the harness (zero cfg) | Requests rejected/limited | Verified pass-throughs (§1 F7); mirror `internal/integration/fullserver_test.go:140-160` shape |
| FM10 | Future `Validate` tightening rejects the harness cfg | `Runtime.New` errors at setup, all gate-on tests fail | Harness cfg documented against every `Validate` rule (§1 F7/C4); any tightening surfaces here first — intended |
| FM11 | Cross-test DB contamination (shared dsn) | Extra outbox rows / wrong counts | Per-test `t.TempDir()` dsn (F4/REQ-1) |

---

## 6. Migration steps

**Schema/data migration: none.** 0039/0040 are already applied by `repo.Migrate` in the harness (K10); the outbox is read via a second connection, not via new store accessors (D5 — `OldestPendingAuditGovernance` cannot assert "exactly one row").

**Implementation sequence** (each step independently verifiable):

1. `internal/cli/cli_governance_e2e_test.go`: harness (REQ-1) + AC-1 test; run `go test ./internal/cli/ -run TestCLIE2E_ActivationGate_FirstAdminFact -count=1`.
2. AC-2 test; `-run TestCLIE2E_GateOff_DisabledState`.
3. AC-3 matrix test; `-run TestCLIE2E_ScopeMatrix_AuditAdmin`.
4. `internal/cli/cli_parity_test.go`: P1–P6; `-run 'TestCLI_(Usage|AdminUsage)Parity'`.
5. Full gate: `gofmt -l .` → `go build ./...` → `go vet ./...` → `go test ./...`; `go test -race -count=1 ./internal/cli/` (K6); `make check` (K5/K4).

Rollback: delete the two test files — zero production surface is touched, so rollback is a pure revert.

---

## 7. Testable acceptance mapping

> All tests run under `go test ./...` (inside `make check`). Each AC from the parent spec maps to exactly one test with the listed concrete assertions.

| AC (spec §5) | Test | Concrete assertions (all must hold) |
|---|---|---|
| **AC-1** B3-6 positive — gate on, first fact reaches outbox + CLI-visible | `TestCLIE2E_ActivationGate_FirstAdminFact` | (a) `Run(["admin","keys","add","gov-key","--scopes","audit:event:write","--tenant","acme"])` == 0 **and stdout contains the literal `audit:event:write`** (201 echo `{"tenant","scopes"}`, R7); (b) outbox == exactly 1 row `{action:"key.add", origin_kind:"admin", tenant_id:"acme", fact_kind:"security"}` with `attempts=0, delivered_at_ns=0`; (c) `Run(["admin","audit","list"])` == 0 and stdout contains `"action":"key.add"`, `"tenant_id":"acme"`, **and `audit:event:write`** (audit `Detail` = `token=**** scopes=[audit:event:write]`) |
| **AC-2** B3-6 negative — gate off → CLI surfaces disabled state | `TestCLIE2E_GateOff_DisabledState` | (a) `admin keys add --scopes admin --tenant acme` == 0; (b) outbox `COUNT(*)` == 0; (c) `admin audit list` == 0, stdout contains `"action":"key.add"`; (d) `repo.(auditgovernance.Store).AuditGovernanceCanDisable(ctx)` == `(true, nil)` |
| **AC-3** B3-5 — scope matrix, per-row 403 reason | `TestCLIE2E_ScopeMatrix_AuditAdmin` | `[admin]` → exit 0 + JSON listing stdout; `[read,write]` → exit 1 + stderr contains `HTTP 403` and `admin scope required`; `[read]` → exit 1 + `admin scope required`; `[audit:event:write]` → exit 1 + stderr contains `HTTP 403` and `missing scope: read` |
| **AC-4** B3-5 — usage/handler parity both directions | `TestCLI_UsageParity` (P1–P3) + `TestCLI_AdminUsageParity` (P4–P6) | P1: all 12 non-alias `cliHandlers` keys each head a `commands:`-block line (first token — R8); P2: all 12 `commands:`-block tokens are map keys; P3: `help`/`-h`/`--help` each exit 0 + usage; P4: all 18 admin pairs each head a `resources:`-block line (first two tokens — R8); P5: all 18 documented pairs in the dispatch table; P6: 6 resources probe without `unknown admin resource`, `frobnicate` with it |

**Baseline guarantee:** the full acceptance set passes on the current working tree (verified — §1 E4/E5 and the 18/18 admin-pair audit); landing the tests is a pure regression net, not a behavior change.

---

## 8. Risks & residual

| # | Risk | Assessment |
|---|---|---|
| R1 | Harness assembly drifts from production wiring | Mitigated: same public constructors + `server.ApplyMiddleware` 12-ring chain (F7); landed sibling harnesses (`cmd/server/governance_e2e_test.go:182`, `internal/integration/fullserver_test.go`) are same-shape references; the e2e's own assertions catch drift |
| R2 | The `[audit:event:write]` row's 403 origin (Auth-ring `checkScope` vs router `requireRESTScope`) is ambiguous | Deliberately not asserted by origin — both emit identical plain-text `missing scope: read`; the design documents both sites (C3). If origin-level pinning is ever wanted, that belongs to the grep-consistency direction (out of scope) |
| R3 | `admin keys list` rendering drift (F1) tempts an in-scope fix | Explicitly out of scope (D7); recorded for a future direction; AC-3 asserts HTTP behavior + stderr only |
| R4 | Parity test brittleness if usage prose gains the same tokens as commands | P1/P2/P4/P5 all parse indented blocks (map keys / table pairs must head block lines, R8) — prose is never sufficient in either direction; R6's declared alias group is the documented escape hatch |
| R5 | "Exactly one row" could break if a future admin action starts auditing list/read paths | That is a deliberate, loud contract pin (F5); any such change must update AC-1 consciously |
| R6 | File growth vs 500-line gate | `_test.go` exempt (K4); both files targeted well under; parity file is self-contained and stateless |
| R7 | AddKey scope round-trip regression — `knownScope`-style truncation **and** validation relaxation would store an empty-scope key that still 403s `missing scope: read` | Closed: AC-1(a)/(c) assert the literal `audit:event:write` in stdout (201 echo `admin.go:141-142` + audit `Detail` `cli_admin.go:417-419`); a truncated key fails both (FM4) |
| R8 | Parity prose-only forward direction — a command/pair dropped from the block but still mentioned in prose | Closed: P1/P4 require block-line membership (first token / first two tokens of indented lines), verified satisfiable for all 12 keys and 18 pairs on the current tree (§1 E4/E5) |

**Non-goals (unchanged from parent spec):** production changes of any kind; relay delivery / fake `/token` / terminal classification / ID recomputation (cmd/server + admin-origin cells); relay source-drift grep guards; new REST endpoints; new dependencies; `admin keys list` rendering fix; the three security-review follow-ups recorded in §2.5 (S1 anonymous public-read 12-ring fix, S2 `+` scope validation, S3 tenant-scoped admin-key confinement — all require production changes and are therefore out of this test-only direction).
