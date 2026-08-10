# Design — `internal/integration` D1 server-level readyz drill: `/readyz` stays 200 (degraded) with sink down + gauge/alert path, timeout semantics pinned through the production assembly

**Module:** `internal/integration` (server-level drill tests) + enabling seam `internal/readiness` (new) · **Spec:** `docs/requirements/internal-integration-d1-server-level-readyz-drill-v1.spec.md` (REQ-0..5, AC-1..4) · **Contract:** `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 / D1; `docs/campaigns/implementation-gate.md:22`
**HEAD:** `15763e2` + uncommitted B3-campaign worktree (all citations re-verified on this checkout) · **Date:** 2026-08-08
**Scope lock:** one new production file (`internal/readiness/readiness.go`, pure move + delegation, byte-identical bodies), one new test file (`internal/integration/audit_governance_readyz_test.go`), mechanical `cmd/server` deletions/delegations, `fullserver_test.go` stub swap. Zero behavior change to `/readyz` wire output, zero config/migration/alert/`go.mod` change. Nothing else moves.

---

## 1. Verification register (evidence treated as untrusted; every claim re-checked on this tree)

| Evidence claim | Re-verified location (this tree) | Verdict |
|---|---|---|
| Deliverable 1: spec at `docs/requirements/internal-integration-d1-server-level-readyz-drill-v1.spec.md` | exists, 139 lines; §1–§6 complete (REQ-0..5, D1..D5, AC-1..4) | ✅ **exact** |
| Deliverable 2: stage artifact at `docs/auto/runs/d1-drill-at-ser-a6b168ce/artifacts/requirements-10762e10/requirements.md` | exists, 34 lines; mirrors run-layout convention (`artifacts/requirements-<hash>/requirements.md`) | ✅ **exact** |
| "`extra.Ready` no longer unbounded — `readyzHandler` wraps it in the same 2s `probeCtx` as `repo.Ping`/`store.Stat` (`http.go:109`)" | `readyzHandler` `cmd/server/http.go:90`; `pingCtx` `:96-98`; `probeCtx` `:102-103`; `extra.Ready(probeCtx)` **`:109`**; degraded marker `:113-121`; healthy `{"ok":true}` `:125-127`; `readyzProbeTimeout = 2s` `:52` | ✅ **exact** |
| "`Runtime` self-bounds with `storeProbeTimeout` (`runtime.go:26`)" | `const storeProbeTimeout = 2 * time.Second` `internal/auditgovernance/runtime.go:26`; probes under it in `probeAndRecord` `:251-252` | ✅ **exact** |
| "decision made and shipped: timeout/cancel → `isProbeCtxError` (`runtime.go:231-236`) → `recordDegraded(true,0)` → `Ready` nil → 200 degraded; genuine store errors stay fail-closed 503" | `isProbeCtxError` `:231-236`; timeout branches `:255-259` (drain probe), `:268-272` (backlog probe) → `recordDegraded(true, 0)` → `return nil`; genuine-error branches `:260-262`, `:273`; `Ready` `:293-294`; "Store errors remain fail-closed" comment `:280-281` | ✅ **exact** |
| "both halves pinned at unit + seam level (`runtime_ready_test.go:176/206`, `readyz_drill_test.go:444/161`)" | `TestRuntimeReadyDegradedSentinel` `internal/auditgovernance/runtime_ready_test.go:176` (elapsed ∈ [1s,5s], age 0, Degraded true); `TestRuntimeReadyFailClosedOnGenuineStoreError` `:206`; `TestReadyzExtraProbeTimeout` `cmd/server/readyz_drill_test.go:161` (503 bounded); `TestReadyzAuditGovernanceDegradedDrill` `:444` (200 degraded age-0) | ✅ **exact** |
| "server-level gap: `governance_e2e_test.go:182-228` has no HTTP server" | `newGovernanceE2E` `cmd/server/governance_e2e_test.go:182-228` — FileService + EventBus + WrapRepository + relay config, **no HTTP server, no Ready/readyz call**; `startRelay` `:239-243` | ✅ **exact** |
| "`fullserver_test.go:136-142` is a Ping-only stub" | `internal/integration/fullserver_test.go:136-142` — `repo.Ping(req.Context())` → 503 else `{"ok":true}`; never touches `Runtime`; `TestFullServer_Readyz` `:204-213` asserts 200 only | ✅ **exact** |
| "seam package-private in `package main` — unreachable from `internal/integration`" | `readinessChecker` `http.go:28-30`, `degradedChecker` `:34-41`, `readinessGroup` `:54`, `readyzHandler` `:90`, `runtimeReadiness` `cmd/server/audit_governance.go:73-87`, gauge fns `cmd/server/build.go:99-115` — all unexported | ✅ **exact** |
| "`internal/readiness` must be created (REQ-0)" | `ls internal/` — **no `internal/readiness`** | ✅ **exact (gap real)** |
| "sink-down drill: backdate 8s > maxLag 4s, no sleeps" | backdating is the house idiom (`internal/reconcile/lifecycle_test.go:43-72` second WAL writer; WAL + `MaxOpenConns(1)` on the repo pool `internal/repository/sqlite.go:11,31-33` — a second raw connection on the same `file:` DSN is a legal concurrent writer); `drillRuntimeConfig` maxLag 4 > claimTTL 3 (`readyz_drill_test.go:80-100`, config validation `config_audit_governance.go:268`) | ✅ **exact** |
| "gauge > maxLag×0.5 derived from harness config — never a literal 450; `TestNoExecutable450LiteralOutsideAlertsYml` scans `internal/`" | `TestNoExecutable450LiteralOutsideAlertsYml` `readyz_drill_test.go:540` — **scans roots `{cmd, internal, sdk/go}`** (evidence understates: the scan covers `internal/integration` and `cmd` too; the regex is stripped before matching so the test cannot self-hit); `BacklogAlertThresholdSeconds() = MaxLagSeconds/2` `config_audit_governance.go:48-50`; default instance 900→450 pinned by `TestAlertsYMLAuditGovernanceExprParity` `readyz_drill_test.go:381` (expr derived from `config.Load()`) vs `alerts.yml:187` `expr: audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1`, `for: 10m` | ✅ **exact (constraint strictly stronger than claimed)** |
| "wedged-store bounded window elapsed ∈ [1s, 5s], 200 degraded age-0" | seam pin `TestReadyzAuditGovernanceDegradedDrill` `readyz_drill_test.go:444-461` (elapsed ∈ [1s,5s], 200, degraded, age 0) — the server-level REQ-4 re-pins the same shape through `readiness.Handler` + a hanging repo wrapper (`hangingAuditStore` idiom `:425-441`) | ✅ **exact** |
| "dead-only backlog via live relay + 422 receiver → 200 `{"ok":true}` + gauges 0" | `govReceiver` modes incl. `422` `governance_e2e_test.go:57-113`; terminal-row exclusion predicate `delivered_at_ns=0 AND failed_at_ns=0` `internal/repository/audit_governance_claim.go:211`; run loop refreshes the degraded cache per poll cycle `runtime.go:325` | ✅ **exact** |
| "non-goals: PG dead-row pin, B3-6 gate, billing readiness, alert/config changes" | scope matches the direction's split (`docs/auto/analyses/internal-integration-7479f0a2.json` directions 1/3 + this direction) | ✅ **exact** |

**Design-load-bearing facts verified independently (not from the evidence):**

| # | Fact | Location |
|---|------|----------|
| V1 | Worst-case degraded-path latency = ping 2s + storage 2s + extra 2s = 6s < helm `readinessProbe timeoutSeconds: 10` (`deployment.yaml:85-91`) — the comment at `http.go:44-48` documents exactly this budget; the seam move must not change it | `cmd/server/http.go:44-52`; `deploy/helm/aero-vault/templates/deployment.yaml:85-91` |
| V2 | `readiness.Handler(repo, store, nil)` must reproduce the fullserver stub behavior exactly: `extra == nil` → skip extra branch → healthy `{"ok":true}` | `http.go:110-127` |
| V3 | `NewGroup` must return `nil` when empty (billing + audit both nil) — `readyzHandler`'s `extra != nil` gate depends on it | `audit_governance.go:73-87` (empty → `nil`) |
| V4 | Gauge callbacks are cache-fed (`rt.BacklogAge()`/`rt.Degraded()` getters, zero store I/O per scrape) and are the *same functions* `registerGauges` hands to telemetry (`build.go:153-154`) — asserting them directly in the drill is asserting production wiring, not a copy | `build.go:99-115`, `:153-155`; `runtime.go:222-226` |
| V5 | The degraded-marker body writes `int64(dc.BacklogAge().Seconds())` (floor truncation) via literal `fmt.Fprintf` — tests must use the floor-tolerant parse (`readyz_drill_test.go:230-243` idiom) | `http.go:119-121` |
| V6 | The 500-line production gate excludes `*_test.go` (`find . -name '*.go' -not -name '*_test.go'`) — the new harness file is exempt but kept ≤ ~600 lines | `Makefile:174-178` |
| V7 | Config shrink constraints for the harness: `ClaimTTLSeconds > 2×HTTPTimeoutSeconds` (`config_audit_governance.go:261`), `MaxBackoffSeconds >= 2` (`:266`), `MaxLagSeconds > ClaimTTLSeconds` (`:268`), `ClientSecretEnv` must match `^AUDIT_GOVERNANCE_CLIENT_SECRET_`; the shipped `drillRuntimeConfig` (1/10/10/3/1/2/4) satisfies all | `readyz_drill_test.go:80-100`; `config_audit_governance.go:255-275` |
| V8 | `runtime.go:318-323` — the run loop calls `probeAndRecord` once per poll cycle, so the degraded cache is fresh (≤ poll interval) independent of `/readyz` traffic; the sink-down drill needs ≥ 1 poll cycle (poll 5ms) after backdating before the gauge asserts | `runtime.go:310-330` |
| V9 | `Runtime.Close()` is non-blocking when the relay was never started (`startOnce.Do(close(done))`, bounded wait `claimTTL + httpTimeout`); the wedged-store drill (relay not started) still needs LIFO cleanup (`rt.Close` before `repo.Close`) per `readyz_drill_test.go:99-108` | `runtime.go:140-150` |

**Baseline (measured this session):** `go build ./...` clean; `go vet` clean; readyz/drill test subset green (evidence's claimed baseline reproduces).

---

## 2. Design — API changes

### 2.1 New package `internal/readiness` (the only production change; REQ-0)

New file `internal/readiness/readiness.go` (~140 lines, well under the 500-line gate). Imports `internal/repository`, `internal/storage`, `internal/auditgovernance` — none of which import `readiness`, so **no import cycle** (`cmd/server` already imports all of them). Every body is a byte-identical move from `cmd/server`:

```go
package readiness

const ProbeTimeout = 2 * time.Second        // ← moved readyzProbeTimeout (http.go:52)

type Checker interface { Ready(context.Context) error }        // ← readinessChecker (http.go:28-30)
type DegradedChecker interface {                               // ← degradedChecker (http.go:34-41)
    Degraded() bool
    BacklogAge() time.Duration
}
type Group []Checker                                           // ← readinessGroup (http.go:54)
// Ready / Degraded / BacklogAge methods — moved byte-for-byte (http.go:55-84)

func NewGroup(checkers ...Checker) Checker                     // ← runtimeReadiness (audit_governance.go:73-87)
// nil when empty (billing + audit both nil → nil) — preserves the
// readyzHandler `extra != nil` gate (V3).

func Handler(repo repository.Repository, store storage.Storage, extra Checker) http.HandlerFunc
// ← readyzHandler (http.go:90-127), byte-for-byte:
//   pingCtx (2s) → repo.Ping → 503 "database unavailable"
//   probeCtx (2s) → store.Stat("@healthz/probe") → 503 "storage unavailable"
//   extra != nil → extra.Ready(probeCtx) → 503 "runtime dependency unavailable"
//   extra.(DegradedChecker) && Degraded() → 200 {"ok":true,"degraded":true,"backlog_age_seconds":N}
//   else → 200 {"ok":true}

func BacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64  // ← build.go:99-105
func DegradedGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64    // ← build.go:107-115
```

**Explicit non-moves:** `auditGovernanceDrainGaugesFn` (`build.go:117-131`) stays in `cmd/server`; the helm/config/telemetry surfaces are untouched.

### 2.2 `cmd/server` deltas (delegation only, no behavior change)

| File | Change |
|---|---|
| `cmd/server/http.go` | Delete the moved seam block (`:24-127`: `readinessChecker`, `degradedChecker`, `readyzProbeTimeout`, `readinessGroup`, `readyzHandler`). `buildRouter` (`:143`) keeps its signature with the type swapped to `extraReady readiness.Checker`; register `r.Get("/readyz", readiness.Handler(repo, store, extraReady))` (`:154`). Single production call site (V12-style: `main.go` only). |
| `cmd/server/audit_governance.go` | Delete `runtimeReadiness` (`:73-87`). |
| `cmd/server/build.go` | Delete `auditGovernanceBacklogAgeGaugeFn`/`auditGovernanceDegradedGaugeFn` (`:99-115`); `registerGauges` (`:153-154`) calls `readiness.BacklogAgeGaugeFn(rt)` / `readiness.DegradedGaugeFn(rt)`. |
| `cmd/server/main.go` | `:157` — `readiness.NewGroup(billingRuntime, auditRuntime)` (was `runtimeReadiness(...)`). |

### 2.3 New test file `internal/integration/audit_governance_readyz_test.go` (REQ-1..5)

`package integration`, sqlite, **no `//go:build integration`** → runs in baseline CI (`go test ./...`). Harness `newGovernanceServer(t, sinkMode)` mirrors the production assembly order (`main.go`): sqlite repo + `Migrate` → `storage.NewLocal` → `auditgovernance.New` (config from the `drillRuntimeConfig` shape, V7: maxLag 4 > claimTTL 3, sink = `http://127.0.0.1:1` for "down" or a local 422 receiver for "dead") → `WrapRepository` → `events.New` + `WithRepository` → `service.NewFileService(...).WithEventSink(bus)` → chi router with `/healthz` + `/readyz` = `readiness.Handler(wrepo, store, rt)` + the 12-ring middleware chain (the `fullserver_test.go:157-161` idiom) → `httptest.Server`. `rt.Start(ctx)` starts the live relay. LIFO cleanup: receiver close → `rt.Close` → `repo.Close`.

Helpers (ported idioms): `putObject` (bound tenant `acme`), `waitForOutboxRow` (poll the sqlite DSN, deadline-bounded), `backdateOutboxRow` (second WAL writer: `UPDATE audit_governance_outbox SET created_at_ns=? WHERE tenant_id='acme' AND delivered_at_ns=0 AND failed_at_ns=0` — `?` placeholders per I1), minimal 422 `govReceiver` shape (`/token` + `/api/v1/events?wait_for=ledgered` → 422).

### 2.4 HTTP wire surface

**No wire change.** `/readyz` statuses and bodies are byte-identical to HEAD (both the healthy `{"ok":true}` and the already-shipped degraded marker `{"ok":true,"degraded":true,"backlog_age_seconds":N}`). `internal/integration` gains no exported Go API (test-only package). The only Go API additions are the `internal/readiness` symbols above, which are `internal/`-scoped and thus invisible outside the module.

---

## 3. Compatibility constraints

| # | Constraint | Enforcement |
|---|---|---|
| C1 | **Wire-identical `/readyz`:** statuses (200 healthy / 200 degraded-marker / 503 per-probe), bodies, ordering (ping → storage → extra), 2s-per-probe budget, worst-case 6s < helm `timeoutSeconds: 10` (V1). | Bodies are byte-identical moves; the pre-existing seam pins (`http_test.go:72/96/116/148/182/199/243/268`, `readyz_drill_test.go:148-461`) become the move's regression net — they must pass unmodified in *behavior* (type/package renames only). |
| C2 | **No config/flag surface change (I5):** audit governance stays opt-in; `readiness.Handler(repo, store, nil)` with nil extra reproduces the current fullserver stub exactly (V2); `NewGroup` nil-on-empty preserved (V3). | REQ-0 acceptance: `TestFullServer_Readyz` (`fullserver_test.go:204-213`) still asserts 200 against the production handler. |
| C3 | **No migrations (I2), no `go.mod` change (I6):** test SQL uses `?` placeholders (I1); no assertion framework; stdlib `net/http/httptest` + `database/sql`. | `go mod tidy` produces no diff; `make check` migration step is a no-op. |
| C4 | **Package boundaries:** `internal/readiness` imports only `{repository, storage, auditgovernance}` (no cycle); `cmd/server` (package main) has no external consumers, so the type swaps are module-internal. | `go build ./...`; `go vet ./...` |
| C5 | **450-literal scan:** the new test derives thresholds from the harness config (`MaxLagSeconds/2`), never an executable `450`; the scan roots are `{cmd, internal, sdk/go}` (stronger than the evidence's "internal/"), so `internal/integration/audit_governance_readyz_test.go` is in scope. | `TestNoExecutable450LiteralOutsideAlertsYml` (`readyz_drill_test.go:540`) stays green. |
| C6 | **Line gates:** `internal/readiness/readiness.go` ≈ 140 lines (< 500); the new test file is exempt (`Makefile:174-178` excludes `*_test.go`) but kept ≤ ~600 lines. | `make check`. |
| C7 | **Middleware chain (I4) untouched:** the drill exercises it as an observer; no handler self-wiring introduced. | diff review; chain pins in `middleware_chain_test.go` unaffected. |

---

## 4. Failure modes

| # | Failure mode | Detection | Mitigation / behavior on failure |
|---|---|---|---|
| F1 | **Seam-move regression** — a moved body differs (e.g. `extra.Ready(probeCtx)` accidentally on `req.Context()`, degraded branch reordered, `Group.Ready` short-circuit changed). | The 10+ pre-existing seam pins (`http_test.go` ×8, `readyz_drill_test.go` ×8) fail on byte-level body/status/elapsed assertions. | The pins are the move's regression net (spec REQ-0 acceptance). Any failure blocks merge; fix = restore byte-identical body, never edit the pin. |
| F2 | **Import cycle** (`internal/readiness` importing something that imports it back). | `go build` fails immediately. | Constraint C4 (imports restricted to repository/storage/auditgovernance); `cmd/server` remains the only importer of `readiness`. |
| F3 | **`NewGroup` empty-slice non-nil** — `readiness.NewGroup()` returning `&Group{}` instead of `nil` flips `readyzHandler`'s `extra != nil` gate → healthy path still 200 but `TestReadyz...` nil-extra pins and `TestFullServer_Readyz` keep passing while `http.go:110` branch is skipped silently; degraded drills would then never exercise the extra. | The nil-extra seam pins (`http_test.go:72/96/116/148`) assert healthy 200 with `nil` — they do **not** distinguish `nil` vs empty Group. | Guard: REQ-0 acceptance adds an explicit unit check in `internal/readiness/readiness_test.go`: `NewGroup() == nil`, `NewGroup(nil, nil) == nil`; plus the deployed degraded drills fail loudly (they would 200-healthy instead of degraded-marker). |
| F4 | **Sink-down drill flake** — relay retry backoff (1s→2s) vs. poll 5ms: the degraded marker must persist across ≥ 2 GETs ≥ 1 poll cycle apart while the row stays pending. | `waitForOutboxRow(pending)` (deadline-bounded) confirms the row exists before backdating; the ≥ 2-GET loop re-checks `pending` predicate between GETs. | Deterministic by construction: no sleeps (D3 backdating); if the relay accidentally delivers (sink down at `127.0.0.1:1` — connection refused, permanent-class? **no**: refused = transient → retried), the wait predicate fails loudly. Backoff cap 2s keeps the cycle tight. |
| F5 | **Backdate race** — the run loop's `probeAndRecord` reads the row between `waitForOutboxRow` and the backdate UPDATE, caching age ≈ 0. | The drill's phase ordering: backdate → then GET (handler's `Ready` re-probes and refreshes the cache); gauge assert happens **after** a GET (V8: cache fresh ≤ poll interval). | No flake: any interleaving still yields age ≥ 8s − ε on the post-backdate probe; assert `> maxLag` (4s), not an exact age — but the marker body is exact `8` (backdate value, deterministic floor). |
| F6 | **Wedged-store drill unboundedness** — if `storeProbeTimeout` regresses, the GET hangs past 5s. | Assertion `elapsed ∈ [1s, 5s]` fails both directions (cannot precede the 2s deadline — deterministic lower bound; > 5s = unbounded). | This is the D1 verify-only re-pin: any violation means the shipped decision regressed; fix in `runtime.go`/`readiness.Handler`, not the test. |
| F7 | **Genuine-error vs timeout conflation** — a non-context store error accidentally routed to `recordDegraded(true,0)` (fail-open regression) would make the dead-letter/healthy drill assert 200-degraded instead of 200-healthy, and the wedged drill's age-0 would not distinguish it. | The fail-closed 503 half is already pinned at unit+seam (`runtime_ready_test.go:206`, `readyz_drill_test.go:161/258`) — cited, not duplicated (scope lock). The new dead-letter drill (REQ-5) would catch a fail-open regression on the *healthy* side: it asserts byte-identical `{"ok":true}` + gauges (0,0). | REQ-5 is the server-level tripwire for fail-open; REQ-4 for fail-closed/timeout conflation (age must be 0, gauges (0,1)). |
| F8 | **450 literal self-hit** — the new file hardcodes 450 (or 900) in an executable position. | `TestNoExecutable450LiteralOutsideAlertsYml` scans `{cmd, internal, sdk/go}` (C5). | Derive: `int64(cfg.AuditGovernance.MaxLagSeconds / 2)`. |
| F9 | **Gauge asserts against a stale cache** — relay not started (wedged drill) or no poll cycle elapsed. | Ordering discipline: GET before gauge assert (the handler's `Ready` runs `probeAndRecord`); sink-down drill: `waitForOutboxRow` + backdate before GET. | D4 (direct-callback assert) uses the exact functions `registerGauges` registers (`build.go:153-154`, V4) — no `/metrics` duplication, no global-instrument re-registration risk. |
| F10 | **Concurrency / cleanup order** — live relay goroutine + httptest requests + second WAL writer; `repo.Close` before `rt.Close` → panic/data race. | `make test-race` (gate); LIFO cleanup (V9, `readyz_drill_test.go:99-108` discipline). | Race failure blocks merge. |
| F11 | **Dead-letter drill relay misclassification** — 422 not treated as permanent, or receiver unreachable. | `waitForOutboxRow(failedAtNS > 0 && deliveredAtNS == 0 && attempts == 1)` with deadline; failure is loud (test fails), not silently skipped. | The drill proves the *relay* dead-letters through production code (D5) — if the classification regresses, the drill fails and the seam-level shortcut (`readyz_drill_test.go:288`) is not relied upon. |

---

## 5. Migration steps

Numbered, each with an exit gate. No data migration (C3); this is a code-only refactor + test addition.

1. **Baseline.** Run `make check` (gofmt · build · vet · test). Gate: green (measured green at spec time).
2. **Create `internal/readiness/readiness.go`** — pure move of `ProbeTimeout`, `Checker`, `DegradedChecker`, `Group`, `NewGroup`, `Handler`, `BacklogAgeGaugeFn`, `DegradedGaugeFn` with byte-identical bodies + the `NewGroup()==nil` guard unit test (F3). Gate: `go build ./...`.
3. **Rewire `cmd/server`** — delete moved blocks in `http.go`/`audit_governance.go`/`build.go`; swap `buildRouter`'s `extraReady` type; register `readiness.Handler`; `main.go:157` → `readiness.NewGroup(...)`. **Do not touch test files yet.**
4. **Regression-net proof.** Run `go test ./cmd/server/ ./internal/readiness/` with the *old* test files — compile errors are the mechanical-rename inventory (expected list: `http_test.go:72/96/116/148/182/199/243/268`, `readyz_drill_test.go:39-52/148/150/227/269/291-292/301/329/356/448`). Apply the mechanical renames only (`readyzHandler`→`readiness.Handler`, embedded `readinessChecker`→`readiness.Checker`, `runtimeReadiness(nil, rt)`→`readiness.NewGroup(nil, rt)`, gauge fns→`readiness.*`). Gate: **every pin passes with zero assertion edits** — this is the byte-identity proof of the move.
5. **Swap the fullserver stub.** `internal/integration/fullserver_test.go:136-142` → `r.Get("/readyz", readiness.Handler(repo, store, nil))`. Gate: `TestFullServer_Readyz` passes unchanged (C2).
6. **Add the drill file.** `internal/integration/audit_governance_readyz_test.go` with the four tests (§6) + helpers (§2.3). Gate: `go test ./internal/integration/ -run 'TestGovernanceServer'` green.
7. **Full gates.** `make check` · `make test-race` · `go test ./...` · confirm the 450-scan and parity pins stay green. Gate: all green.
8. **Docs.** Update `docs/CHANGELOG.md`; the spec + this design are the requirement trail (no `docs/api.md` change — zero wire surface change).

---

## 6. Testable acceptance mapping

Each supplied acceptance check maps to exactly one test; the enabling seam has its own acceptance. All assertions are deterministic (no sleeps; backdating + bounded windows).

| Acceptance (direction/supplied) | Test (file: `internal/integration/audit_governance_readyz_test.go`) | Assertions | Determinism mechanism |
|---|---|---|---|
| (1) sink down → `/readyz` 200 `{"ok":true}` degraded, no restart loop | `TestGovernanceServerReadyzDegradesWithSinkDown` (REQ-2) | Phase 0 (negative control): GET → 200 `{"ok":true}` byte-identical. `putObject(acme)` → `waitForOutboxRow(pending)` → `backdateOutboxRow(8s)` (> maxLag 4s, 2× margin). Phase 1: GET → 200 + exact marker `{"ok":true,"degraded":true,"backlog_age_seconds":8}` (floor-tolerant parse, V5), elapsed < 1s (cache/probe path, not a hang). Phase 2: ≥ 2 further GETs ≥ 1 poll cycle apart (poll 5ms) → still 200 + marker while row stays pending; `DegradedGaugeFn == 1` throughout. **Any 503 fails the test.** | Backdating replaces "wait until backlog > MaxLag" (D3); sink = `http://127.0.0.1:1` (connection refused → transient → relay keeps retrying, row stays pending). |
| (2) BacklogAge gauge > maxLag×0.5 (450s at default 900) with sink down | `TestGovernanceServerBacklogGaugeExceedsHalfMaxLag` (REQ-3) | Same sink-down state; after a GET: `readiness.BacklogAgeGaugeFn(rt)(ctx) > int64(cfg.AuditGovernance.MaxLagSeconds/2)` (4s → threshold 2s; **derived, no literal**) and `DegradedGaugeFn == 1`. Default-config instance (900→450) cross-referenced, not re-pinned: `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:381`) + `TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` (`config_audit_governance_test.go:64-87`); scrape surface at `internal/telemetry/metrics_test.go:171/192`. | Run loop refreshes the cache per poll cycle (V8); GET before gauge assert; threshold from the harness's own config (C5/F8). |
| (3) wedged store → bounded window, deterministic 200-degraded or 503 — resolving "读路径超时降级非 503" | `TestGovernanceServerReadyzBoundedWhenStoreWedged` (REQ-4) | Decision = **degrade-on-timeout** (already shipped, verify-only): repo wrapper hangs the two probe methods on `ctx.Done()` (`hangingAuditStore` idiom); relay not started. GET → **200**, body contains `"degraded":true` + `"backlog_age_seconds":0` (age unknown), elapsed ∈ **[1s, 5s]** (cannot precede the 2s `storeProbeTimeout`; ≤ 5s = boundedness); gauges `(0, 1)` (alert OR-arm, `alerts.yml:187`). Fail-closed half (genuine error → 503) cited to existing pins (`runtime_ready_test.go:206`, `readyz_drill_test.go:161/258`) — not duplicated (scope lock). | Lower bound is physical (response cannot precede the 2s probe deadline); upper bound is the boundedness proof (proven idiom `http_test.go:71-88`). |
| (4) dead-only backlog at server level → 200 + gauges 0 | `TestGovernanceServerDeadLetteredBacklogReadyAndGaugeZero` (REQ-5) | Sink mode `422` (permanent class) through the **live relay** (D5): `putObject(acme)` → `waitForOutboxRow(failedAtNS > 0 && deliveredAtNS == 0 && attempts == 1)` → GET → **200 `{"ok":true}` byte-identical** (terminal rows excluded from `OldestPendingAuditGovernance` — `audit_governance_claim.go:211`); `BacklogAgeGaugeFn == 0`; `DegradedGaugeFn == 0`. Contrast with (1): same harness, pending → degraded; dead-lettered → healthy — the two shapes cannot be conflated. | Terminal within one poll cycle (attempts == 1); wait predicate deadline-bounded (F11). |
| (enabling, REQ-0) seam importable; move behavior-identical | `go test ./cmd/server/ ./internal/readiness/ ./internal/integration/` + all pre-existing seam pins | Moved bodies byte-identical (every pin passes with rename-only edits, step 4 gate); `NewGroup() == nil` unit guard (F3); `TestFullServer_Readyz` 200 against `readiness.Handler(repo, store, nil)`; full `make check` + `make test-race` green. | The pre-existing seam pins are the regression net; the move is the only production diff and it is provably behavior-neutral. |

**Non-goals honored (scope lock):** PG dead-row pin (`audit_governance_postgres_test.go` — direction 1), B3-6 activation-gate work (direction 3), billing-runtime readiness, drain-still-503 server-level re-pin (seam-pinned at `readyz_drill_test.go:258`), any `alerts.yml`/config/migration change, `/metrics` scrape in the harness (D4), `auditGovernanceDrainGaugesFn` relocation.

---

## 7. Risks & gates (summary)

- **Main risk — seam-move blast radius (F1):** mitigated by the regression-net proof (migration step 4): old pins passing with rename-only edits is the byte-identity certificate. Four packages under `make check` + `make test-race`.
- **Timing flake:** zero sleeps by construction (D3 backdating; physical lower bound on the wedge). The only wall-clock cost is the ~2s wedge drill.
- **Gauge correctness:** asserted via the production-registered callbacks (D4/V4), freshness by ordering (F9).
- **Literal drift:** config-derived thresholds only (C5/F8); the 900→450 default instance stays pinned at its owners, and the ratio invariant is what the drill pins (REQ-3).

*Verification basis: all citations re-checked on this tree (HEAD `15763e2` + uncommitted B3-campaign worktree); line numbers reflect the tree as read while producing this design. The evidence's claims were substantively accurate; one understatement corrected (§1 row 10: the 450-scan roots are `{cmd, internal, sdk/go}`, stronger than "internal/").*
