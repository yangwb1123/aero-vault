# Requirements Specification — `internal/middleware` analysis bundle: B3-6 activation gate — reject enabled + empty bindings at boot (fail-closed)

**Module:** `internal/middleware` (analysis bundle label; the gate lives in `internal/config` + `internal/auditgovernance` + `cmd/server`)
**Direction:** "B3-6 activation gate: reject enabled + empty bindings at boot (fail-closed), currently boots a silent no-op relay"
**Source analysis:** `docs/auto/analyses/internal-middleware-697499e2.json` (direction 1 of 3)
**Date:** 2026-08-08 · **HEAD:** `15763e2` + uncommitted WIP worktree (verification basis = this checkout)
**Score:** value 8 / risk reduction 7 / effort 2 / confidence 9

---

## 0. Status summary

The direction's premise — "no layer rejects it, an enabled + empty-bindings boot starts a silent no-op relay" — is **stale against the current worktree**: the fail-closed gate and its unit tests already exist (uncommitted WIP; see §2 verdicts E2/E3 and §5). What remains open is exactly one acceptance item: the **e2e boot-failure case** (AC-2, first half). This spec preserves all three supplied acceptance checks verbatim (§3), converts each into a testable form, and records the verified evidence.

---

## 1. Scope

Contract acceptance (F-03, `docs/campaigns/implementation-gate.md:26`): **"空 bindings + enabled → boot 失败"** — an enabled Audit Governance relay with zero bound tenants must never boot into a capture-nothing state. This spec scopes exactly the B3-6 activation gate across the boot chain: config load (`loadAuditGovernanceConfig` → `Validate`) → `auditgovernance.New` → server boot (`buildAuditGovernanceRuntime`), plus the T-3-adjacent no-silent-no-op invariant and its documented drain escape (`AUDIT_GOVERNANCE_DRAIN`). Out of scope (§4): repository-layer length checking, all other B3-* contract items, T-3 delivery semantics, any config-surface or migration change.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this checkout. Line numbers are worktree positions; where the direction's cited range no longer matches, the drift itself is evidence (the gate insertion shifted `Validate`'s body down by 37 lines).

| # | Direction citation | Verified location (worktree) | Verdict |
|---|---|---|---|
| E1 | "decode accepts empty bindings array — verified no len check" (`config_audit_governance.go:109-139`) | `decodeAuditGovernanceBindings` `:122-140`: 1 MiB size cap `:123-125`, `DisallowUnknownFields` `:128`, single `Decode` `:130-132`, `ensureAuditGovernanceJSONEOF` `:133-136`, `resolveAuditGovernanceSecrets` `:137-139` — **no length check**; `{"revision":1,"bindings":[]}` decodes cleanly. `readAuditGovernanceBindings` `:99-120` checks file presence/regularity/permissions/TOCTOU only. | ✅ **holds** |
| E2 | "Validate checks shapes, not emptiness" (`config_audit_governance.go:240-265` `validAuditGovernanceRetry`/`boundedAuditGovernanceTiming`/`validateAuditGovernanceBindings`) | **Superseded.** Worktree `Validate` `:178-227` now opens with the fail-closed gate `:186-195`: `c.Drain && len(c.Bindings) > 0` → `"AUDIT_GOVERNANCE_DRAIN requires an empty bindings manifest"` `:191`; `!c.Drain && len(c.Bindings) == 0` → `"audit governance bindings must not be empty"` `:194`. In-file comment `:183-189` documents first placement (deterministic before URL/HMAC/duration checks) and the drain escape, and states the guard mirrors `BillingConfig.Validate` (`config_billing.go:138`). `validAuditGovernanceRetry`/`boundedAuditGovernanceTiming`/`validateAuditGovernanceBindings` now sit at `:243-266`/`:258-267`/`:289-314` — the cited range `240-265` matches the **pre-gate** file, confirming the analysis predates the fix. | ❌ **no longer holds** (implemented) |
| E3 | "`Runtime.New` → `applyDesiredBindings` accepts an empty desired set" (`runtime.go:211-234`) | Split verdict. `applyDesiredBindings` `runtime.go:329-345` still has no length check (it maps `cfg.Bindings` 1:1 and passes through), but `New` `:76-127` now calls `cfg.Validate()` `:82-84` **before** `applyDesiredBindings` `:94`, so an enabled empty non-drain config fails closed pre-store-I/O. Drain-mode flag `draining: cfg.Drain && len(cfg.Bindings) == 0` `:124`; `Draining()` accessor `:165-168`. The direction's cited range `211-234` does not match `applyDesiredBindings` (it sits at `:329`); `:211-234` in the current file is `PendingBacklogAge`/`Degraded`/`BacklogAge`. | ❌ **superseded at the `New` layer**; ✅ per-function claim (no len check inside `applyDesiredBindings`) still holds |
| E4 | "`ApplyAuditGovernanceBindings` rejects revision/digest/states, not empty desired" (`audit_governance_binding.go:18-24`) | `:18-27` checks `revision == 0`/`digest == ""`/`validGovernanceBindingStates(desired)`; `validGovernanceBindingStates` `:29-41` returns `true` for an empty slice (loop over zero elements, no length check). Empty desired is accepted by design — drain mode must be able to apply the DELETE-all (`replaceGovernanceBindings` `:56-72`). | ✅ **holds** (deliberate; gate lives upstream in config+runtime) |
| E5 | "activation-gate e2e tests cover capture behavior, not the boot failure" (`governance_e2e_test.go:362-411`) | `TestGovernanceE2EActivationGateBoundTenant` `:373-405`, `TestGovernanceE2EActivationGateUnboundTenant` `:407-421` — capture behavior only (outbox row / POST / token-call counts). `grep -rn "EmptyBindings\|empty-manifest\|boot failure" cmd/server/` → no boot-failure case anywhere. | ✅ **holds** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "config Validate validates per-binding shape only — `bindings: []` passes" | ❌ **stale** — gate at `Validate` `:186-195` (E2); decode still permissive (E1), which is fine because `Validate` is the enforcement point. |
| "`Runtime.New` → `applyDesiredBindings` accepts empty desired" | ⚠️ **superseded** — `New` calls `cfg.Validate()` `:82-84` first (E3); `applyDesiredBindings` alone remains len-agnostic. |
| "repository accepts empty desired (checks revision/digest/state, not length)" | ✅ **holds** (E4) — and must: drain mode's DELETE-all is a legal empty apply. |
| "`AUDIT_GOVERNANCE_ENABLED=true` + empty bindings boots a capture-nothing relay" | ❌ **stale** — such a boot now errors at config load and again at `New` (E2/E3). |
| "e2e tests cover capture, not boot failure" | ✅ **holds** (E5) — AC-2's first half remains uncovered. |

**Boot-chain wiring (verified):** `main()` → `run()` → `config.Load()` (error → `fmt.Fprintf(os.Stderr, "fatal: %v")` + `os.Exit(1)`, `cmd/server/main.go:27-34`) → `loadAuditGovernanceConfig` → `Validate` (`config_audit_governance.go:65-96`, gate `:186-195`); second layer `buildAuditGovernanceRuntime` (`cmd/server/audit_governance.go:31-50`) → `auditgovernance.New` → `cfg.Validate()` (`runtime.go:82-84`), error wrapped `"configure Snaplink Audit Governance: %w"` (`audit_governance.go:44-46`). Drain-mode boot logs a distinct WARN naming `AUDIT_GOVERNANCE_DRAIN` + revision + digest fingerprint (`audit_governance.go:53-60`); `audit_governance_bound_tenants`/`audit_governance_draining` gauges (`internal/telemetry/metrics.go:402-403`) back the `AuditGovernanceEnabledUnbound` alert (`deploy/prometheus/alerts.yml:202`); `.env.example:191` documents the drain flag contract.

**Test evidence (run on this checkout):** `go test ./internal/config/ -run 'TestAuditGovernance' -count=1` → ok; `go test ./internal/auditgovernance/ -count=1` → ok (0.5 s).

---

## 3. Requirements

The three supplied acceptance checks are preserved verbatim (quoted), each followed by its testable form.

### REQ-1 — Config load and `Runtime.New` fail closed (AC-1)

> **Supplied check (AC-1):** "config load with `AUDIT_GOVERNANCE_ENABLED=true` + bindings file containing `"bindings":[]` (or missing) returns an error; `Runtime.New` fails closed (unit test on `config.Validate` + `runtime.New`)"

- **R1.1 — load path.** `loadAuditGovernanceConfig` with `AUDIT_GOVERNANCE_ENABLED=true` and a bindings file containing `{"revision":1,"bindings":[]}` must return an error; a missing `AUDIT_GOVERNANCE_BINDINGS_FILE` must also error (`readAuditGovernanceBindings` `:100-103`). *Testable (existing, passing):* `TestAuditGovernanceEmptyBindingsLoadPathFailsClosed` (`config_audit_governance_test.go:287-302`) — writes an empty-manifest file, sets only `ENABLED` + `BINDINGS_FILE` (+ cleared `DRAIN`), asserts the load error contains `"bindings"`; the nil and `[]` forms are pinned in `TestAuditGovernanceBindingsValidation` (`:246-260`).
- **R1.2 — `Validate` matrix.** `Validate` must reject `enabled ∧ ¬drain ∧ len(Bindings)==0` and `enabled ∧ drain ∧ len(Bindings)>0`, and accept `enabled ∧ drain ∧ len(Bindings)==0` (the documented disable-flow escape). *Testable (existing, passing):* `TestAuditGovernanceDrainFlagRequiresEmptyManifest` (`config_audit_governance_test.go:264-283`).
- **R1.3 — `Runtime.New` fails closed before store I/O.** `New` with enabled + empty non-drain bindings must return an error whose text contains `"bindings"` and must not touch the store (no apply, no control-revision bump). *Testable (existing, passing):* `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO` (`runtime_test.go:249-271`) — after the rejected `New`, a revision-1 direct apply succeeds (proving control was still 0) and `AuditGovernanceCanDisable` still reports the pre-apply state.
- **R1.4 — drain positive path.** `New` with `Drain=true` + empty bindings + strictly higher revision must apply the DELETE-all and expose `Draining()==true`, `BoundTenants()==0`, `AppliedDigest()!=""`. *Testable (existing, passing):* `TestRuntimeDrainAppliesEmptyDesiredAndExposesMode` (`runtime_test.go:283-338`), plus `TestDrainFlagWithNonEmptyManifestRefusesBoot` (`:340-372`, armed drain refuses boot, persisted state untouched).

### REQ-2 — Boot path e2e: empty bindings → startup error + zero events (AC-2)

> **Supplied check (AC-2):** "boot path with enabled + empty bindings exits with error and zero events captured; enabled + ≥1 valid binding boots and captures (existing matrix tests retained)"

- **R2.1 — boot seam refuses empty bindings.** `buildAuditGovernanceRuntime` (`cmd/server/audit_governance.go:31-50`) with `Enabled=true, Drain=false, Bindings=nil` must return an error wrapping `"configure Snaplink Audit Governance"` and containing `"bindings"`, with a `nil` runtime; `main.go:27-34` maps the propagated error to stderr + `os.Exit(1)`. *Testable (existing, passing, same seam):* `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn` and `TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved` (`cmd/server/audit_governance_test.go`) prove the seam is directly unit-testable; **new**: add the symmetric non-drain empty-bindings case to this file asserting error + nil runtime + no persisted binding mutation (`AuditGovernanceCanDisable` unchanged).
- **R2.2 — e2e: zero events captured under the refused boot (NEW — the missing case).** In `cmd/server/governance_e2e_test.go`, add an empty-bindings variant of the existing harness (`newGovernanceE2E` `:193-246`, which builds `config.AuditGovernanceConfig` programmatically): `cfg.Bindings = nil`, `cfg.Drain = false`, same receiver — `auditgovernance.New` must fail with `"bindings"` and no runtime may be constructed. Then, on the harness's `FileService`+`EventBus` wiring (same `putObject` helper), assert **zero capture**: `SELECT` of the outbox row returns `sql.ErrNoRows` (as in `TestGovernanceE2EActivationGateUnboundTenant` `:407-421`), `receiver.postCount == 0`, `receiver.tokenCalls == 0`. Name: `TestGovernanceE2EActivationGateEmptyBindingsBootFails`.
- **R2.3 — positive path retained.** Enabled + ≥1 valid binding boots and captures: `TestGovernanceE2EActivationGateBoundTenant` (`:373-405`, exactly 1 outbox row + 1 POST + 1 token call, fact-ID determinism), `TestGovernanceE2EActivationGateUnboundTenant` (`:407-421`), and the matrix tests `TestGovernanceE2EMatrixDelivered/PermanentClasses/Transient200` (`:424+`) must remain green and unchanged — R2.2 must be a **new** test, not a modification of the harness contract.

### REQ-3 — No silent no-op (AC-3, T-3-adjacent)

> **Supplied check (AC-3):** "no silent no-op — grep/assert that every enabled configuration path ends in either a bound tenant set or a startup error"

- **R3.1 — outcome matrix is exhaustive.** The enabled-boot outcome space is exactly: `{enabled ∧ ¬drain ∧ empty} → boot error` (REQ-1/REQ-2), `{enabled ∧ drain ∧ empty} → drain boot, WARN-logged, gauges 1/0` (R1.4 + R3.2), `{enabled ∧ non-empty} → bound tenants ≥ 1` (R2.3), `{disabled} → no-op relay` (pinned by `TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved`). Exhaustiveness is structural: the gate is first-placed in `Validate` (`config_audit_governance.go:183-189` comment) and re-asserted by `New` (`runtime.go:82-84`); drain is the only length-based escape and it is itself fail-closed (R1.2). *Testable:* the union of the named tests above covers every cell; a reviewer check (grep `AuditGovernance.Enabled` consumers) must find only `loadAuditGovernanceConfig`/`buildAuditGovernanceRuntime`/`New` as enabled-entry points, each terminating in error-or-bound.
- **R3.2 — the drain escape is never silent.** A drain boot must log a WARN naming `AUDIT_GOVERNANCE_DRAIN` + revision + digest fingerprint and expose `bound_tenants=0`/`draining=1` gauges (the `AuditGovernanceEnabledUnbound` alert surface, `alerts.yml:202`). *Testable (existing, passing):* `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn` (`cmd/server/audit_governance_test.go`, asserts `level=WARN`, `"drain mode"`, flag name, `revision=2`) and `internal/telemetry/metrics_test.go:227-238` (gauge values 0/1 and flip back to 2/0 after re-bound).

---

## 4. Out of scope / non-goals

- **Repository-layer length check in `ApplyAuditGovernanceBindings`** (`audit_governance_binding.go:18-27`): deliberately not added — drain mode must apply an empty desired set (E4); the gate belongs at config+runtime where the `Drain` flag is known. No change.
- B3-1 (permanent-error classification), B3-2 (Ready/degraded semantics), B3-3 (fact-ID determinism), B3-4 (relay telemetry), B3-5 (scope rejection) — sibling directions of the same campaign; each already has its own spec or is out of this direction's scope.
- T-3 delivery semantics (202-only acceptance, 200 transient, permanent closed list) — pinned by the existing matrix tests (R2.3) and not modified here.
- Any migration, `go.mod`, config-surface, `.env.example`, or alert change; any change to the existing harness tests beyond adding R2.2.

---

## 5. Verification status (this worktree)

Implemented and green (unit level; `go test` evidence in §2):

- `config_audit_governance.go:186-195` gate + tests `TestAuditGovernanceEmptyBindingsLoadPathFailsClosed`, `TestAuditGovernanceDrainFlagRequiresEmptyManifest`, nil/`[]` pins (`config_audit_governance_test.go:246-302`).
- `runtime.go:82-84` `New`-level gate + tests `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO`, `TestRuntimeDrainAppliesEmptyDesiredAndExposesMode`, `TestDrainFlagWithNonEmptyManifestRefusesBoot` (`runtime_test.go:249-372`).
- Drain observability: WARN log test + gauge tests (R3.2); `.env.example:191`; `docs/snaplink-audit-governance.md:162` documents the gate ("the fail-closed activation gate refuses an empty manifest without the flag").

Open work (exactly one acceptance gap):

- **R2.1/R2.2** — the e2e boot-failure case: enabled + empty bindings → startup error + zero events captured (new `TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot` at the `cmd/server` seam and `TestGovernanceE2EActivationGateEmptyBindingsBootFails` in the e2e harness). Everything else in AC-1/AC-2/AC-3 is covered by existing passing tests.

**Verification-basis caveat:** the gate and its unit tests exist only in the **uncommitted worktree** (`git diff HEAD` shows `+37` in `config_audit_governance.go`; HEAD `15763e2` contains none of it, and `cmd/server/governance_e2e_test.go` is not in HEAD at all). The direction's cited line ranges (`240-265`, `211-234`) match the pre-gate file, which is consistent with the analysis having been generated before the WIP implementation landed. Any later commit of this worktree must carry the gate, the unit tests, and the new R2.1/R2.2 e2e together.
