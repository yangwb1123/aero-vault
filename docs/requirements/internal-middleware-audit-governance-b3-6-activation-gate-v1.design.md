# Design — B3-6 activation gate: reject enabled + empty bindings at boot (fail-closed)

**Module:** `internal/config` + `internal/auditgovernance` + `cmd/server` (analysis-bundle label `internal/middleware`)
**Spec:** `docs/requirements/internal-middleware-audit-governance-b3-6-activation-gate-v1.spec.md`
**Verification basis:** HEAD `15763e2` + uncommitted WIP worktree (this checkout) · 2026-08-08

---

## 0. Executive summary

**The supplied evidence is verified with one material conclusion: the direction was partially stale — the fail-closed gate already exists in the uncommitted worktree.** All five citations were re-checked at the current file positions (`go test` evidence in §1). The single open acceptance item — AC-2's first half, the boot-path e2e proving enabled + empty bindings refuses boot with zero events captured (spec R2.1/R2.2) — is **implemented, mutation-hardened, and green** in this worktree (§2.2; mutation battery in the adversarial review, report `docs/auto/runs/b3-6-activation-gate-reject-enabled-empty-bindin-78a434bc/artifacts/adversarial_review-9c87f3a7/meta/mutation_rigor.md`).

Consequently this design is **test-only**: zero production code changes, zero config-surface changes, zero migrations. Deliverables: one seam unit test (A), one pure refactor of the e2e harness (B, config-literal extraction), one new e2e test file (C).

**Corrections to the previous revision (all verified in-worktree):**
1. **FM-4's "500-line hard gate" does not apply to `*_test.go`.** Both enforcement points exempt test files — the Makefile `complexity-lines` awk uses `-not -name '*_test.go'` (`Makefile:168,:179`) and `checks/filesize.py` inherits `ignore_patterns: ["_test.go", …]` from `engineering.yaml:17` (`checks/test_filesize.py:36-38` documents the default). In-repo proof: `cmd/server/readyz_drill_test.go` at 576 lines passes CI; `cli.py check-filesize` PASSes at 511. The prior "exactly 500 lines — the hard gate forbids growing it" rationale was vacuous; refactor B's real value is FM-3 (single source of the harness timing envelope shared with C).
2. **FM-7's guard is the revision-1 direct-apply control-row probe**, not the `AuditGovernanceCanDisable` probe alone. `CanDisable` is `NOT EXISTS(bindings) AND NOT EXISTS(undelivered outbox)` (`audit_governance_binding.go:154-162`) — it cannot see a control-row-only write (the `audit_governance_control` singleton is pre-seeded by migration 0040 and never probed), so the prior claim "any control-row bump … would flip it" was false. The demonstrated M3 leak (apply-before-`Validate` in `New`) is closed by the direct-apply probe added to both A and C: `ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe)` must **succeed** (⇒ control revision still 0).
3. **Refactor B's net math is 500 → 511 (+11), not "500 → ~481"** — the extracted literal is 14 lines (not ~24), so net −19 is mathematically unreachable. The file stands at 511 lines.
4. **The nil/`[]` pins live in `TestAuditGovernanceBindingsRequireDistinctCredentials:244-258`**, not in `TestAuditGovernanceBindingsValidation` (that function does not exist; the spec's own citation is the stale one).
5. **FM-2's justification text was inverted** — see §4 FM-2.

---

## 1. Verification of supplied evidence (untrusted claims → verdicts)

All line numbers are worktree positions, re-verified by reading the code and running the tests, not by trusting the evidence.

| # | Supplied claim | Verdict | Verified evidence |
|---|---|---|---|
| E1 | decode accepts `bindings:[]` — no len check (`config_audit_governance.go:122-140`) | ✅ **holds** | `decodeAuditGovernanceBindings`: 1 MiB cap, `DisallowUnknownFields`, single `Decode`, `ensureAuditGovernanceJSONEOF`, `resolveAuditGovernanceSecrets` — **no length check**; `{"revision":1,"bindings":[]}` decodes cleanly. `readAuditGovernanceBindings` checks presence/regularity/permissions/TOCTOU only. |
| E2 | `Validate` checks shapes only (cited `:240-265`) | ❌ **superseded** | Worktree `Validate` opens with the fail-closed gate: `:225` `"AUDIT_GOVERNANCE_DRAIN requires an empty bindings manifest"`, `:228` `"audit governance bindings must not be empty"` (first placement, before URL/HMAC/revision/duration/shape checks; first-placement comment documents determinism + drain escape + the `BillingConfig.Validate` mirror). Shape checks follow. Load path calls `cfg.Validate()` at `:128` inside `loadAuditGovernanceConfig` (`:92`). |
| E3 | `applyDesiredBindings` accepts an empty desired set (`runtime.go:211-234`) | ❌ **superseded at `New`** | `New` (`runtime.go:76-127`) calls `cfg.Validate()` at `:82-84` **before** `applyDesiredBindings` (`:94`) — enabled+empty+¬drain fails pre-store-I/O. `applyDesiredBindings` itself remains len-agnostic (1:1 map). The cited range is actually the readiness accessors (`PendingBacklogAge` `:198`, `BacklogAge` `:222` — the pre-gate file layout). Drain flag: `draining: cfg.Drain && len(cfg.Bindings) == 0`; `Draining()`/`BoundTenants()`/`AppliedDigest()` accessors are zero-I/O. |
| E4 | `ApplyAuditGovernanceBindings` checks revision/digest/state, not length | ✅ **holds** (deliberate) | `audit_governance_binding.go` (`revision == 0` / `digest == ""` / `validGovernanceBindingStates` — loops zero elements → `true`, no len check). Empty desired is legal by design: drain mode's DELETE-all goes through `replaceGovernanceBindings` in one transaction with `unboundGovernanceBacklog` — an undelivered outbox fact rolls back the DELETE-all and refuses boot (`audit governance unbound backlog blocks startup`; store-level reproduction in the security review). |
| E5 | e2e tests cover capture only, no boot failure (`governance_e2e_test.go:373/407`) | ✅ **holds** (at design time) | **Now delivered:** `TestGovernanceE2EActivationGateEmptyBindingsBootFails` in the new file `governance_e2e_boot_gate_test.go` (§2.2-C). The capture-only matrix lives at `governance_e2e_test.go:384/:418/:435/:452/:482` (post-refactor positions). HEAD `15763e2` has neither the file nor the gate. |

**Test evidence (run on this checkout, all green):**
- `go test ./internal/config/ -run TestAuditGovernance -count=1` → ok (0.01 s)
- `go test ./internal/auditgovernance/ -count=1` → ok (32 s)
- `go test ./cmd/server/ -run 'TestGovernanceE2E|TestBuildAuditGovernanceRuntime|TestDisabledAuditGovernance' -count=1` → ok (2.6 s)
- `-race` on A + C + seam → ok (adversarial review)

**One correction to the spec's citations:** spec §3 R1.1 cites `TestAuditGovernanceBindingsValidation` (`config_audit_governance_test.go:246-260`) for the nil/`[]` pins. No such function exists; the pins are the tail of `TestAuditGovernanceBindingsRequireDistinctCredentials` (`:244-258`, `cfg.Bindings = nil` and `cfg.Bindings = []AuditGovernanceBinding{}` both asserting error text contains `"bindings"`). Substance identical; name/position drift only — **this design uses the real location**.

**Doc/ops pins backing AC-3 (verified present):** `.env.example:191` (drain-flag contract), `deploy/prometheus/alerts.yml:202` (`AuditGovernanceEnabledUnbound`), gauges `audit_governance_bound_tenants`/`audit_governance_draining` (`internal/telemetry/metrics.go`), `docs/snaplink-audit-governance.md:162` ("the fail-closed activation gate refuses an empty manifest without the flag").

**Security review (supplementary, no bypass found):** a 15-case env matrix + binary boots + store-level transaction test closed all four attack surfaces (CLI impossible by construction — pure HTTP client; MCP stdio gated via the same `config.Load` triple gate; direct `New` callers gated by `New`'s own re-assertion; `&Runtime{}` literal impossible — unexported fields, only inside `New`). `ParseBool` strictness means `AUDIT_GOVERNANCE_ENABLED=yes` silently parses false (non-bypass footgun on the *enable* flag, fail-safe on `DRAIN`); refused boots diverge from drain boots on five axes (stream/prefix/exit code/wording/ordering), so no operator-confusion disguise path exists.

---

## 2. API changes (exact)

### 2.1 Production — NONE

The gate is already implemented and unit-tested in the worktree: `Validate` `:225/:228` (first placement, before URL/HMAC/revision/duration/shape checks), reached by the load path at `:128` and re-asserted by `New` `:82-84` pre-store-I/O. No function, struct, env var, schema, or telemetry change in this design. The drain escape (`AUDIT_GOVERNANCE_DRAIN=true` + empty manifest → transactional DELETE-all + WARN) is unchanged.

### 2.2 Test-only changes — all landed in this worktree (all `package main` of `cmd/server`)

**A. Seam unit test — `cmd/server/audit_governance_test.go` (147 lines; +102 vs HEAD).**

`TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot` (`:99`) — mirrors the skeleton of `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn` (`:54`): open SQLite repo + migrate, capturing `slog` logger, `cfg := &config.Config{AuditGovernance: …}` with the same valid timing envelope as the drain test, but `Enabled: true, Drain: false, Bindings: nil`. Assertions, in order:

1. `runtime, err := buildAuditGovernanceRuntime(cfg, repo, logger)` → `err != nil` containing **both** `"configure Snaplink Audit Governance"` (seam wrap, `cmd/server/audit_governance.go:44`) and `"bindings"` (survives both wraps: `New:82-84` "invalid audit governance config: …" → seam).
2. `runtime == nil` — no runtime object escapes a refused boot.
3. **No persisted mutation, probe 1:** `store.AuditGovernanceCanDisable(ctx)` → `err == nil && safe == true` (`audit_governance_binding.go:154-162`: `NOT EXISTS(bindings) AND NOT EXISTS(undelivered outbox)` — true on a fresh DB pre-apply).
4. **No persisted mutation, probe 2 (control row):** `store.ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe)` must **succeed** — pins control revision still 0. `CanDisable` cannot see a control-row-only write (control singleton pre-seeded by migration 0040, never probed); the direct-apply probe is the established idiom from `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO` and closes the demonstrated M3 leak (FM-7). Runs *after* the `CanDisable` check (it mutates).
5. **No drain WARN:** `!strings.Contains(logs.String(), "drain mode")` — the refused boot must not emit the drain-mode WARN line; the WARN stays exclusive to the legal drain escape (positive pin: `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn`).

**B. Pure refactor — `cmd/server/governance_e2e_test.go` (511 lines).**

Extract the config literal inside `newGovernanceE2E` into the package-level helper `governanceE2EConfig()` (`:189`, same idiom as `runtimeConfig(server.URL)` in `internal/auditgovernance/runtime_test.go`), returning `Revision: 1` + the single standard `e2eTenant` binding with fixed loopback endpoints (`BaseURL: "http://127.0.0.1:1"`) so boot-gate tests can use it standalone without dialing a live receiver; live harnesses override `BaseURL`/`TokenURL` with the receiver endpoint. `newGovernanceE2E` (`:215`) becomes `cfg := governanceE2EConfig()` (`:233`). **Zero behavior change** — final config value-identical (verified: all 7 pre-existing harness/matrix tests green before and after). **Net math: 500 → 511 (+11)** — the literal is 14 lines, not ~24; net −19 is mathematically unreachable (helper 19 + usage 3 − literal 14 = +8 minimum, and the file was already past 500 when the refactor landed). The refactor's value is **FM-3** (single source of the timing envelope shared with C), not line pressure.

**C. New file — `cmd/server/governance_e2e_boot_gate_test.go` (99 lines, new file).**

`TestGovernanceE2EActivationGateEmptyBindingsBootFails` (`:36`) — the missing AC-2 case. It cannot reuse `newGovernanceE2E` (which `t.Fatal`s on `New` error — harness contract, untouched per spec §4); it builds the failed-boot scenario inline, reusing existing package-level helpers:

```go
func TestGovernanceE2EActivationGateEmptyBindingsBootFails(t *testing.T) {
	receiver := newGovReceiver("202-echo")                       // reuse
	ctx := context.Background()
	dir := t.TempDir(); dsn := "file:" + filepath.Join(dir, "e2e.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)             // migrate as newGovernanceE2E
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := governanceE2EConfig()                                 // shared helper (refactor B)
	cfg.Bindings = nil                                           // enabled ∧ ¬drain ∧ empty
	rt, err := auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)
	// 1. refuses boot: err != nil, contains "bindings", rt == nil
	// 2. no persisted mutation: CanDisable still true, then the
	//    revision-1 direct-apply control probe must succeed (revision == 0)
	// 3. wiring with a nil runtime: WrapRepository(repo, nil) returns the raw
	//    repo (repository.go:15-19) — no auditedRepository, no outbox path, no relay.
	wrepo := auditgovernance.WrapRepository(repo, nil)
	bus := events.New(wrepo, logger); bus.WithRepository(wrepo)
	svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)
	obj := putObject(t, svc, e2eTenant, "boot-gate.txt")         // reuse
	originID := eventRowID(t, dsn, obj.ID)                       // gate-1 fallthrough: events row exists
	if _, err := outboxRow(t, dsn, originID); err != sql.ErrNoRows {
		t.Fatalf("refused boot produced an outbox row: %v", err)
	}
	// 4. zero events captured — structurally: no runtime ⇒ relay never started ⇒
	//    no goroutines ⇒ tokenCalls/postCount are 0 without quiesce/startRelay.
	if receiver.postCount.Load() != 0 || receiver.tokenCalls.Load() != 0 {
		t.Fatalf("events captured under refused boot: post=%d token=%d",
			receiver.postCount.Load(), receiver.tokenCalls.Load())
	}
	t.Cleanup(receiver.server.Close); t.Cleanup(func() { _ = repo.Close() })
}
```

Assertions mapped to AC-2: error + nil runtime + no persisted mutation (CanDisable + control probe + `sql.ErrNoRows` on the outbox query while the `object_events` row exists — the write path itself works) + `postCount == 0` + `tokenCalls == 0`. Zero `time.*`/`Sleep`/`quiesce` — assertions are single-shot and structural (FM-5).

---

## 3. Compatibility constraints

- **Zero production surface ⇒ zero compatibility surface.** No env semantics, schema, public API, alert, or doc change. Every existing boot config (enabled+bound, enabled+drain+empty, disabled) behaves identically to the current worktree.
- **Existing green pins stay untouched:** matrix tests `:384-482`, `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn` `:54`, `TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved` `:17`, all `internal/config` + `internal/auditgovernance` unit tests. R2.1/R2.2 are additive only.
- **500-line gate — corrected scope:** the 500-line limit **applies to production files only**. `*_test.go` is exempt in both enforcement points — Makefile `complexity-lines` awk (`-not -name '*_test.go'`, `Makefile:168,:179`) and `checks/filesize.py` (`engineering.yaml:17` `ignore_patterns`; in-repo proof `readyz_drill_test.go` at 576 lines passes CI). The prior claim that "`make check` gates" `governance_e2e_test.go` at 500 lines was false; the gate is not an enforcement point for any test file here. (Production files remain gated, satisfying AGENTS.md's hard-gate table.)
- **Harness contract unchanged:** `newGovernanceE2E(t, mode)` keeps its `t.Fatal`-on-New-error semantics and cleanup order (`rt.Close → repo.Close → server.Close`); the new test is a sibling, not a fork.
- **Commit hygiene (spec §5 caveat):** HEAD `15763e2` contains *none* of the gate; the commit carrying this worktree must land gate (`config_audit_governance.go` +37, `runtime.go` +158/−19) + unit tests + seam/e2e tests **in one commit**. Splitting re-opens the silent-no-op window. **Compile-coupling note:** the `runtime.go` hunks interleave the gate with the B3-2 round-2 readiness work (`BacklogAge(ctx)` → `PendingBacklogAge(ctx)` rename at `:198/:222`, consumed by `cmd/server/build.go:103/:153` for the readyz degraded marker) — the minimal coherent commit is the B3-6 ∪ B3-2-r2 cluster; `access.go`/`workers.go`/Makefile/CI and unrelated packages are excludable.
- **I6 (stdlib-only):** the new tests use only `testing`, `strings`, `database/sql`, `bytes` (already imported by the package) — no new dependencies.

---

## 4. Failure modes — re-derived to the actual implemented guards

All guards below were exercised by the mutation battery (M1/M1a/M2/M3/FM-6-mutation) in the adversarial review; production files restored byte-identical afterwards.

| # | Mode | Guard (actual, verified) |
|---|------|--------------------------|
| FM-1 | Gate deleted/loosened in `Validate` (`:225/:228`) — boot regresses to silent no-op relay | **7 tests fail loudly under M1** (both checks deleted): seam A (`audit_governance_test.go:123` `err=<nil>`), e2e C (`:58` "New accepted"), + `TestAuditGovernanceBindingsRequireDistinctCredentials`, `TestAuditGovernanceDrainFlagRequiresEmptyManifest`, `TestAuditGovernanceEmptyBindingsLoadPathFailsClosed`, `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO`, `TestDrainFlagWithNonEmptyManifestRefusesBoot`. **M1a** (delete only the `:228` empty check, keep drain): A + C + load-path + nil/`[]` pins fail while the drain test stays green — correct per-check discrimination |
| FM-2 | Gate moved below URL/HMAC checks in `Validate` — error precedence drifts | **Sole guard: `TestAuditGovernanceEmptyBindingsLoadPathFailsClosed` (`:292`)**. Correction: the prior revision's justification was inverted — the test sets **only** `ENABLED` + `BINDINGS_FILE` (URL/HMAC unset), and it is placement-sensitive *precisely because* the env is otherwise-invalid: under M2 the URL error masks the `"bindings"` error and the test fails. Verified: seam A + e2e C **pass** under M2 (valid envelopes ⇒ bindings error still fires first — placement-insensitive exactly as designed, and the design's claim about them holds) |
| FM-3 | Harness config drifts between `newGovernanceE2E` and the new e2e (timing-envelope divergence → false-positive/negative) | Removed structurally: single `governanceE2EConfig()` source (refactor B, `:189`); C reuses it with `Bindings = nil`; no literal duplication (behavior-neutrality diff-verified) |
| FM-4 | `governance_e2e_test.go` grows past 500 lines | **Vacuous — no such gate for `*_test.go`.** Both enforcement points exempt test files (Makefile awk `-not -name '*_test.go'`; `checks/filesize.py` `ignore_patterns` incl. `"_test.go"`); `readyz_drill_test.go` at 576 lines passes CI; `cli.py check-filesize` PASS at 511. No test guards this file's length. Refactor B's actual rationale is FM-3 (delivered); C still lives in a new file by hygiene, not gate pressure |
| FM-5 | Flaky timing in the new e2e (relay race) | Impossible by construction: no runtime ⇒ `Start` (the only `go r.run()` spawn point) never runs ⇒ no relay goroutine ⇒ `postCount`/`tokenCalls` trivially 0; C uses zero `time.*`/`Sleep`/`quiesce` — assertions are single-shot, non-temporal; `-race` on A+C+seam ok |
| FM-6 | Future `WrapRepository` change stops short-circuiting nil runtime → panic in test wiring | Pinned by `repository.go:15-19` contract; C's `WrapRepository(repo, nil)` exercises it — FM-6 mutation (short-circuit removed) makes `putObject` **panic loudly** at `governance_e2e_boot_gate_test.go:86` (nil deref in `Capture`). Any such change fails C at wiring time |
| FM-7 | Binding/control state leaks into the DB despite refused boot (write lands before the gate) | **Two probes in both A and C**: (1) `AuditGovernanceCanDisable` still `true` — catches binding rows and undelivered outbox rows; (2) **revision-1 direct-apply control probe** — `ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe)` must succeed, pinning control revision still 0. The second is load-bearing because `CanDisable` cannot see a control-row-only write (control singleton pre-seeded by migration 0040, never probed) — the prior claim "any control-row bump … would flip it" was false, demonstrated by M3 (apply-before-`Validate` in `New`): both tests passed as designed with the leak invisible; with the probe both **fail** under M3. Probe order: after the `CanDisable` check (it mutates), before wiring |
| FM-8 | Refused boot emits the drain WARN (silent-no-op disguise) | A.6 negative pin `!contains("drain mode")` (refused boot logs nothing; WARN is exclusive to the legal drain escape, positively pinned by `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn`). Security review confirms five-axis divergence: stderr vs stdout, `fatal:`/`mcp:` prefix vs structured JSON, exit code 1 vs 0-and-serving, refusal wording vs state announcement, and refusal precedes any relay log |

---

## 5. Migration steps

**No runtime migration** (no schema, config surface, or env change; I2/I6 untouched). The only "migration" is the commit itself — a WIP→landed transition, ordered to keep every intermediate state green.

**Executed in this worktree** (by the adversarial review; all production files restored byte-identical, `diff`-verified; full green battery incl. `-race`):
1. ~~Refactor B~~ — `governanceE2EConfig()` extracted (`governance_e2e_test.go:189`); 7 pre-existing harness/matrix tests green before and after (behavior-neutral).
2. ~~Test A~~ — `TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot` added (`audit_governance_test.go:99`).
3. ~~Test C~~ — `cmd/server/governance_e2e_boot_gate_test.go` added (99 lines).

**Remaining:**
4. **Gate** — `make check` (gofmt / build / vet / full test / production-file line gate). Full-suite `go test ./...` for the SQLite+local baseline (I5) — re-verified green in §1.
5. **Docs** — flip spec §5 "Open work" to closed (R2.1/R2.2 are landed); no other doc change.
6. **Single commit** — gate + unit tests + seam + e2e + spec update together (spec §5 caveat; compile-coupling to B3-2-r2 per §3). `make check` in CI is the merge gate.

---

## 6. Testable acceptance mapping

Supplied checks preserved verbatim; each mapped to its test, with the state of each at this checkout (all tests re-run green in §1; R2.1/R2.2 landed and passing).

| Acceptance check (supplied) | Spec req | Test (file:line) | Assertions | Status |
|---|---|---|---|---|
| AC-1: "config load … returns an error; `Runtime.New` fails closed (unit test on `config.Validate` + `runtime.New`)" | R1.1 | `TestAuditGovernanceEmptyBindingsLoadPathFailsClosed` (`config_audit_governance_test.go:292`); nil/`[]` pins `TestAuditGovernanceBindingsRequireDistinctCredentials` `:244-258` (spec's cited `TestAuditGovernanceBindingsValidation` does not exist); missing-file `readAuditGovernanceBindings` path | load error contains `"bindings"` (env: only `ENABLED` + `BINDINGS_FILE` — the placement-sensitivity that makes this the sole FM-2 guard); both `nil` and `[]` forms rejected | ✅ green |
| 〃 | R1.2 | `TestAuditGovernanceDrainFlagRequiresEmptyManifest` (`:269`) | `¬drain ∧ empty → error`; `drain ∧ non-empty → error`; `drain ∧ empty → ok` | ✅ green |
| 〃 | R1.3 | `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO` (`runtime_test.go:252`) | error contains `"bindings"`; post-rejection revision-1 direct apply succeeds (control still 0 — no store I/O) — **the established idiom the A/C control probes reuse** | ✅ green |
| 〃 | R1.4 | `TestRuntimeDrainAppliesEmptyDesiredAndExposesMode` (`:289`); `TestDrainFlagWithNonEmptyManifestRefusesBoot` (`:338`) | drain apply DELETE-all (transactional, unbound-backlog-guarded); `Draining()==true`, `BoundTenants()==0`, `AppliedDigest()!=""`; armed drain refuses boot, state untouched | ✅ green |
| AC-2: "boot path with enabled + empty bindings exits with error and zero events captured; enabled + ≥1 valid binding boots and captures" | R2.1 | `TestBuildAuditGovernanceRuntimeEmptyBindingsRefusesBoot` (`audit_governance_test.go:99`) | err contains `"configure Snaplink Audit Governance"` + `"bindings"`; `runtime == nil`; `CanDisable` still `true`; **control probe succeeds (revision still 0)**; no `"drain mode"` WARN | ✅ **landed, green** |
| 〃 | R2.2 | `TestGovernanceE2EActivationGateEmptyBindingsBootFails` (`governance_e2e_boot_gate_test.go:36`) | `New` errors with `"bindings"`, nil runtime; `CanDisable` still `true` + control probe succeeds; `WrapRepository(repo, nil)` short-circuits (putObject works, `object_events` row exists); `outboxRow` → `sql.ErrNoRows`; `postCount == 0`; `tokenCalls == 0` | ✅ **landed, green** |
| 〃 | R2.3 | `TestGovernanceE2EActivationGateBoundTenant` (`:384`), `UnboundTenant` (`:418`), `TestGovernanceE2EMatrixDelivered/PermanentClasses/Transient200` (`:435/:452/:482`) | 1 outbox row + 1 POST + 1 token call, fact-ID determinism; unbound → `ErrNoRows`; matrix retained unchanged (post-refactor positions) | ✅ green |
| AC-3: "no silent no-op — every enabled configuration path ends in either a bound tenant set or a startup error" | R3.1 | Exhaustive union: R1.1-R1.4 + R2.1-R2.3 + `TestDisabledAuditGovernanceRequiresPersistedBindingsRemoved` (`audit_governance_test.go:17`) | outcome space = {error} ∪ {drain+WARN} ∪ {bound ≥1} ∪ {disabled}; gate first-placed (`config_audit_governance.go:225/:228`) + re-asserted `New:82-84`; drain is the only length-based escape and is itself fail-closed (transactional DELETE-all + unbound-backlog rollback) | ✅ green |
| 〃 | R3.2 | `TestBuildAuditGovernanceRuntimeDrainBootLogsWarn` (`:54`); gauge flip test (`telemetry/metrics_test.go`); `alerts.yml:202`; `.env.example:191` | WARN names `AUDIT_GOVERNANCE_DRAIN` + revision + digest fingerprint; gauges 0/1 → 2/0 after re-bound | ✅ green |

**Done-bar:** all rows green — R2.1/R2.2 are landed and passing, and `make check` + `-race` pass on the single landing commit.

**VERDICT on the supplied evidence:** the two "stale" verdicts (E2/E3) and the "holds" verdict requiring action (E5) are confirmed by direct code inspection and test runs; the claims are trustworthy except for the pre-gate line ranges and one test-name citation (`TestAuditGovernanceBindingsValidation` → `TestAuditGovernanceBindingsRequireDistinctCredentials`), both of which are cosmetic drift consistent with the analysis predating the WIP implementation. **Post-design corrections recorded in §0:** the 500-line gate premise (FM-4) and the FM-7 guard description were falsified and are corrected here; refactor B's net math is 500 → 511 (+11), not 500 → ~481.

---

## 7. Security-review hardening — strict boolean parse for the two flags

**Finding (security review, non-bypass):** `loadAuditGovernanceConfig` read `AUDIT_GOVERNANCE_ENABLED` / `AUDIT_GOVERNANCE_DRAIN` through the generic `getEnvBool`, which silently falls back to the default when `strconv.ParseBool` fails. The common `yes`/`on` spellings therefore parsed to **false**: on the enable flag that is capture silently off (fail-open for the audit trail — a fresh or fully-drained DB boots silently without the relay), on the drain flag a silent no-op (the operator believes the drain ran while the relay keeps running). Not a gate bypass — the config is not enabled — but a fail-open-for-capture footgun on the enable flag.

**Chosen hardening — fail-closed strict parse, scoped to the two flags:**

| Option | Verdict | Rationale |
|---|---|---|
| Boot-time WARN only | ❌ rejected | Boot continues without capture; the WARN is invisible in automated deploys that don't surface boot logs. Strictly weaker than refusal, and the finding's failure direction is security-sensitive (capture off). |
| Docs-only restriction | ❌ rejected | Leaves the silent coercion in place; documentation cannot protect against a typo. |
| **Fail-closed parse with explicit error** | ✅ **implemented** | A set, non-empty, non-canonical value is a hard `Load()` error → `fatal:` + exit 1 on both the server and MCP boot paths (GATE #1/#2), before any repo/store I/O. The error names the flag and the offending value. |

**Scope discipline (why the global `getEnvBool` was NOT changed):** the strictness lives in `getAuditGovernanceEnvBool` (`config_audit_governance.go`), used only by this loader. The other ~40 boolean flags keep their existing semantics — zero blast radius, and the B3-6 gate code (`Validate` first-placement) is byte-identical.

**Non-perturbation analysis (B3-6 gate semantics + drain escape matrix):**
- Accepted set is unchanged — every `strconv.ParseBool` spelling (`1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False`) still parses; unset/empty still neutralizes to the default. Every previously legal configuration loads identically.
- The strictness only converts the previously-silent coercion cases into explicit boot refusals. For `DRAIN=yes` the pre-fix behavior was already either a confusing gate refusal (empty manifest) or a silent drain no-op (non-empty manifest); both are now a clear error naming the flag. The four canonical drain matrix rows (`drain∧empty → legal`, `drain∧non-empty → refused`, `¬drain∧empty → refused`, `¬drain∧non-empty → legal`) are untouched.
- Verified by re-running the full gate battery green: `internal/config` (incl. new pins), `internal/auditgovernance` (incl. `TestRuntimeDrainAppliesEmptyDesiredAndExposesMode`, `TestDrainFlagWithNonEmptyManifestRefusesBoot`, `TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO`), `cmd/server` (seam A, e2e C, bound/unbound/matrix e2e, drain-WARN pin, readyz drill) — and by binary smoke: `AUDIT_GOVERNANCE_ENABLED=yes` / `DRAIN=yes` → `fatal: load config: …invalid boolean value "yes"…` + exit 1; `ENABLED=false` boots normally.

**Pins added:** `TestAuditGovernanceBooleanFlagsFailClosedOnNonCanonicalValues` (load path: `yes`/`on`/`2`/`tru` on both flags → error naming flag and value) and `TestAuditGovernanceEnvBoolCanonicalAndNeutralized` (all canonical spellings parse; unset/empty neutralizes). Docs: `docs/configuration.md` (both rows), `.env.example`, `docs/snaplink-audit-governance.md`.
