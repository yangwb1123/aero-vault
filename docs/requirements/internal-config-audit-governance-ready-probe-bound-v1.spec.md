# Requirements Specification — `internal/config`: the /readyz audit-governance store-probe bound follows operator config (close the D1 drill gap's config-coupling tail)

Module: `internal/config`. Direction (source: `docs/auto/analyses/internal-config-a932ee1e.json`, direction 2): *"Bound the /readyz audit-governance read path and classify read-timeout as degraded instead of 503 (close the D1 drill gap)"*. All citations verified against the tree at HEAD `15763e2`.

## 1. Status statement (what exists vs. what this direction requires)

Two of the three supplied acceptance checks landed in commit `15763e2` ("feat(gov): B3-2 Ready decoupling — backlog degrades instead of 503; backlog-age gauge + 450s alert") **before** this analysis was written; the direction's problem description is partially stale. What the worktree already ships:

- **(a) seam level** — the extra readiness group is bounded and read-path timeouts degrade instead of 503: `readyzHandler` runs `extra.Ready(probeCtx)` under the same 2s `readyzProbeTimeout` as the storage probe (cmd/server/http.go:109), and `TestReadyzAuditGovernanceDegradedDrill` (cmd/server/readyz_drill_test.go:444) pins wedged-store → 200 degraded marker, age 0, bounded elapsed.
- **(b) runtime level** — `Runtime.Ready` returns nil (degraded) on probe timeout/cancel incl. a pre-canceled caller ctx, while genuine store errors and draining bindings still fail: `isProbeCtxError` fork in `probeAndRecord` (runtime.go:231-268), pinned by `TestRuntimeReadyDegradedSentinel`, `TestRuntimeReadyFailClosedOnGenuineStoreError` c1/c2/c3, `TestRuntimeReadyDegradesOnBacklogLag` incl. the draining gate (runtime_test.go:618, :662-669).
- **The direction's caveat is verified true**: the "relay HTTP delivery timeouts" reading is already satisfied — `isPermanentDeliveryError` (relay.go:247-255) classifies HTTP delivery timeouts as transient/retried. No work remains on that reading.

What remains — this spec's scope, entirely under acceptance (c) plus two assertability gaps:

1. **(c) is unimplemented.** The readiness store-probe bound is the hardcoded `const storeProbeTimeout = 2 * time.Second` (runtime.go:26, call site :252), not config's `HTTPTimeoutSeconds`. `internal/config` is the single source of timing truth (D3), yet nothing in config represents, derives, or pins this bound — `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (default 5) is carried by the runtime as `r.httpTimeout` (runtime.go:92) for the relay and is exactly the value the direction names for reuse.
2. **The "with a warn" clause of acceptance (a) is never asserted**: every readiness-test harness logger is `io.Discard` (`newReadyRuntime` runtime_ready_test.go, `newReadyzDrillRuntime` readyz_drill_test.go), so the probe-timeout `logger.Warn` lines (runtime.go:254, :261) have no pin.
3. **No config-module loader pin exists** for the HTTP timeout default/override — only struct-form validation pins (config_audit_governance_test.go:140-163: `ClaimTTL == 2×HTTPTimeout` rejected, 30 rejected).

## 2. Evidence verification (direction citations vs. this worktree)

| Direction citation | Verification at HEAD `15763e2` |
|---|---|
| `cmd/server/http.go:38-66` — "readyzProbeTimeout bounds only the storage probe; `extra.Ready(req.Context())` unbounded" | **DRIFT — already closed.** `readyzProbeTimeout = 2s` at :44-52 (comment: same 2s budget bounds `repo.Ping`, the storage probe, *and* the extra readiness group; worst-case degraded latency 6s < helm 10s); ping under `pingCtx` :96-99; storage probe under `probeCtx` :102-105; **`extra.Ready(probeCtx)` at :109** — the raw-request-context claim is stale. |
| `internal/auditgovernance/runtime.go:168-183` — "Ready: store error → error (503); only backlog>maxLag degrades" | **DRIFT — superseded by `probeAndRecord`** (:251-268, both probes under `storeProbeTimeout` at :252): probe timeout/cancel → `logger.Warn("audit governance store probe timed out — degraded", probe=…)` + `recordDegraded(true, 0)` + return nil (:253-256, :260-263); draining → error "audit governance binding drain is in progress" (:257-259); backlog > maxLag → degraded (:264-266); genuine store errors → "audit governance drain/backlog lookup failed" (:267). `Ready` = `probeAndRecord` (:293-295). `isProbeCtxError` (:231-235) covers DeadlineExceeded **and** Canceled. |
| `internal/auditgovernance/runtime.go:156-166` — "BacklogAge passes caller ctx through to OldestPendingAuditGovernance" | **PARTIAL.** `PendingBacklogAge` (:198-207) still passes caller ctx — but it is now only the gauge/test accessor; `Ready()`'s backlog read goes through the runtime's own bounded `probeCtx` (:252). |
| `internal/config/config_audit_governance.go` — "HTTPTimeoutSeconds default 5; ClaimTTLSeconds > 2×HTTPTimeoutSeconds invariant" | **EXACT.** Field :26; `getEnvInt("AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS", 5)` :73; `ClaimTTLSeconds > 2*c.HTTPTimeoutSeconds` :261 (within `validAuditGovernanceWorker`); cap `HTTPTimeoutSeconds <= 29` :284 (within `boundedAuditGovernanceTiming`); documented as `1..29` at docs/configuration.md:269. |
| `internal/auditgovernance/relay.go:132-142` — "isPermanentDeliveryError — HTTP delivery timeouts are already transient" | **EXACT, relocated :247-255.** Closed list (conflict/invalid receipt, HTTP 409/422); everything else — transport, context, token — transient and retried. The direction's caveat reading is confirmed satisfied. |
| *(net-new finding — the remaining gap)* | `const storeProbeTimeout = 2 * time.Second` runtime.go:26, sole use :252 — hardcoded, mirroring `readyzProbeTimeout` by comment (:22-25), **not** config-driven; no loader-level pin of the HTTP_TIMEOUT default/override in `internal/config`; the probe-timeout Warn is never asserted (all harness loggers `io.Discard`). |

## 3. Verified current state (green baseline — do not regress)

`go test ./internal/auditgovernance/ ./cmd/server/ -run 'TestRuntimeReady|TestReadyz'` → both packages `ok` at HEAD `15763e2`. The pins this direction preserves:

- `TestRuntimeReadyDegradedSentinel` (runtime_ready_test.go:176) — hang → nil, Degraded, age 0, elapsed ∈ [1s, 5s].
- `TestRuntimeReadyFailClosedOnGenuineStoreError` (runtime_ready_test.go:206) — c1 drain-error / c2 backlog-error fail-closed; c3 pre-canceled ctx → nil + degraded + < 1s.
- `TestRuntimeReadyDegradesOnBacklogLag` (runtime_test.go:615-669) — maxLag degrades **and** the draining gate still fails.
- `TestReadyzAuditGovernanceDegradedDrill` (readyz_drill_test.go:444), `TestReadyzBacklogLagDegradesNot503` (:212), `TestReadyzDrainStill503` (:258), `TestReadyzExtraProbeTimeout`, `TestReadyzHealthyExtra200`, `TestReadyzDeadLetteredBacklog200AndGaugeZero`, `TestReadyzImmediateExtraError`.
- **Harness configs already use `HTTPTimeoutSeconds = 1`** — `runtimeConfig` (runtime_test.go:44) and `drillRuntimeConfig` (readyz_drill_test.go:84) — so a config-driven bound (1s in tests) keeps every existing elapsed window ([1s, 5s]) and poll deadline (3s/4s in the run-loop tests) valid without retuning.

## 4. Requirements

### REQ-1 — The readyz store-probe bound is config-driven (acceptance c, implementation)

`Runtime`'s readiness store-probe bound becomes the config-derived value New already computes and carries: `timeout := time.Duration(cfg.HTTPTimeoutSeconds) * time.Second` → field `r.httpTimeout` (runtime.go:92). `probeAndRecord`'s wrap changes from `context.WithTimeout(ctx, storeProbeTimeout)` (runtime.go:252) to `context.WithTimeout(ctx, r.httpTimeout)`; `const storeProbeTimeout` (runtime.go:26) is deleted.

- **Config boundary unchanged**: env `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS`, default 5 (:73), validation envelope 1..29 (:284) with `ClaimTTLSeconds > 2×HTTPTimeoutSeconds` (:261) stay exactly as shipped. No new field, no new env, no validation change — a consumer-side wiring change in `internal/auditgovernance` only.
- **Handler-level cap untouched**: `readyzProbeTimeout` (2s, cmd/server/http.go:52) also bounds `repo.Ping` and the storage probe; it remains the outer cap at the seam. Effective /readyz audit-probe bound = `min(readyzProbeTimeout, cfg.HTTPTimeoutSeconds)`; the worst-case 6s < helm-10s comment (http.go:46-52) stays valid because the handler bound is unchanged.
- **Stale comments updated** (they reference the deleted const or the old 2s semantics): runtime.go:22-26/:246-249, runtime_ready_test.go:5-7 and :180-183 ("cannot precede the 2s probe deadline" → the configured bound), readyz_drill_test.go elapsed-bound comments.

### REQ-2 — Config-module pins for the bound's source (acceptance c, config boundary)

New tests in `internal/config/config_audit_governance_test.go`, mirroring the existing loader default-pin idiom (`TestAuditGovernanceMaxLagDefaultIsTwiceBacklogAlertThreshold` — env-neutralized `loadAuditGovernanceConfig`):

- `AUDIT_GOVERNANCE_ENABLED=false`, `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` unset/empty → `HTTPTimeoutSeconds == 5` (the shipped default; the `getEnvInt` empty-string fallback is the established pattern).
- `AUDIT_GOVERNANCE_ENABLED=false`, `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS=7` → `HTTPTimeoutSeconds == 7` (operator override flows through the loader).
- The envelope needs no new pins: `ClaimTTL == 2×HTTPTimeout` rejected and `30` rejected already exist (config_audit_governance_test.go:154-163); REQ-1 must not weaken them.

### REQ-3 — The probe demonstrably follows operator config (acceptance c, runtime)

New discriminating runtime test (new file `internal/auditgovernance/probe_bound_test.go` — `runtime_ready_test.go` is at 472 lines, near the 500-line hard gate):

- Harness: `runtimeConfig` shape (HTTPTimeoutSeconds = 1) + `scriptedStore` hang mode (runtime_ready_test.go:49-64 — blocks on `ctx.Done`, event-driven, no sleeps).
- Assert: `Ready()` returns nil (degraded), `Degraded()==true`, `BacklogAge()==0`, and **elapsed ∈ [1s, 1.5s)** — the 1s config bound fired; a regression to a hardcoded 2s const yields ≈2s and fails the upper bound. The return is event-driven on the ctx deadline, so the 500 ms window has the same flake posture as the existing [1s, 5s] windows.
- The two existing harness configs already set HTTPTimeoutSeconds=1 (runtime_test.go:44, readyz_drill_test.go:84), so this test needs no new fixture.

### REQ-4 — Acceptance (a) preserved and "with a warn" made assertable

- The seam pins stay green **unchanged** (they are the (a) gate): `TestReadyzAuditGovernanceDegradedDrill` (200 + degraded marker + age 0 + elapsed ∈ [1s, 5s] + degraded gauge 1 — the "store stub that blocks past the bound" is `hangingAuditStore`, readyz_drill_test.go:423-441, which blocks on ctx rather than sleeping, the CI-safe form of "sleeps past the bound"), `TestReadyzBacklogLagDegradesNot503`, `TestReadyzDrainStill503`, `TestReadyzHealthyExtra200`, `TestReadyzDeadLetteredBacklog200AndGaugeZero`.
- **New warn assertion** (runtime-level, capturing slog handler — e.g., a `bytes.Buffer` + `slog.NewTextHandler`): each timed-out probe emits exactly one `logger.Warn` with message `"audit governance store probe timed out — degraded"` and `probe=drain` (runtime.go:254) or `probe=backlog` (runtime.go:261); a healthy probe emits none. This is the (a)-clause never pinned today (all harness loggers are `io.Discard`).

### REQ-5 — Acceptance (b) preserved (regression gates)

- `TestRuntimeReadyDegradedSentinel`, `TestRuntimeReadyFailClosedOnGenuineStoreError` c1/c2/c3, `TestRuntimeReadyDegradesOnBacklogLag` incl. the draining gate (runtime_test.go:662-669), `TestReadyzDrainStill503` — all unchanged and green.
- **Net-new twin**: c3 covers `context.Canceled`; add the `context.DeadlineExceeded` arm (a `context.WithDeadline` ctx already expired — the direction's "ctx is already expired/deadline-exceeded for the backlog read" phrasing) — same assertions as c3: Ready nil, Degraded true, age 0, elapsed < 1s (immediate, never waiting out the bound). This closes the second `isProbeCtxError` arm at test level.

## 5. Acceptance mapping (supplied checks → testable requirements)

| Supplied acceptance | Status | Testable form |
|---|---|---|
| **(a)** readyz-level: pending-read blocks beyond the bound → 200 (degraded) with a warn, not 503 — store stub blocking past the bound | **Already implemented** (since `15763e2`) | `TestReadyzAuditGovernanceDegradedDrill` (hanging store stub → 200 `{"ok":true,"degraded":true,"backlog_age_seconds":0}`, elapsed ∈ [1s, 5s], degraded gauge 1) + `TestReadyzBacklogLagDegradesNot503`; **warn clause net-new** via REQ-4's capturing-handler assertion |
| **(b)** `Runtime.Ready` nil (degraded) on already-expired/deadline-exceeded ctx for the backlog read; genuine store errors and draining bindings still fail (503) — preserving `TestRuntimeReadyDegradesOnBacklogLag` and the draining gate | **Already implemented** | c1/c2 (error text "audit governance drain/backlog lookup failed", `Degraded()==false`), c3 (pre-canceled → nil, degraded, age 0, < 1s) + REQ-5's net-new DeadlineExceeded twin; `TestRuntimeReadyDegradesOnBacklogLag` (:615-669) incl. the draining gate; `TestReadyzDrainStill503` |
| **(c)** the bound reuses config `HTTPTimeoutSeconds` so the probe follows operator config | **Net-new (this spec's core)** | REQ-1 wiring (`r.httpTimeout` replaces `storeProbeTimeout`); REQ-2 loader pins (default 5, override 7) at the config boundary; REQ-3 discriminating elapsed ∈ [1s, 1.5s) with `HTTPTimeoutSeconds=1` — a regression to the 2s const fails REQ-3 |

## 6. Non-goals (out of scope)

- **No new config field/env** — the direction explicitly names reuse of `HTTPTimeoutSeconds`; a dedicated `READY_PROBE_TIMEOUT_SECONDS` would be scope expansion.
- **No change to `readyzProbeTimeout`/handler-level caps** — ping + storage + extra wrapper stay at 2s; the 6s < helm-10s worst-case comment (http.go:46-52) stays valid.
- **No relay/delivery-path change** — `isPermanentDeliveryError` (relay.go:247-255) is the direction's caveat reading, verified already satisfied.
- **No maxLag / alert / gauge / alerts.yml change** — that is direction 1, already landed (`internal-config-audit-governance-alert-threshold-derived-v1.spec.md`).
- **No docs/configuration.md change** — `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (`1..29`) is documented at :269; only stale code comments (REQ-1) are updated.

## 7. Risks & gates

- **Dual use of `HTTPTimeoutSeconds`** (relay HTTP delivery + readiness probe): raising it toward the 29s cap lengthens the run-loop background probe stall on a wedged store (runtime.go:322) to ≤ 29s per poll cycle — still bounded, still degrades (never 503), the loop never stops (F17: `TestRuntimeRunLoopSurvivesWedgedStore`), and the /readyz seam stays ≤ 2s via the handler cap. This is the accepted trade-off of the direction's chosen reuse; the behavior change is only observable when `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS < 2` at the seam and for background probes otherwise.
- **Flake posture**: the new elapsed windows are event-driven (stub returns at `ctx.Done`) with ≥ 500 ms slack — the same idiom as the existing suite's [1s, 5s] windows.
- **Hard gates**: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` (`make check`). New runtime test file keeps `runtime_ready_test.go` (472 lines today) under the 500-line limit; runtime.go stays a one-const-deletion + one-line call-site change.

## 8. Verification steps

```
go test ./internal/config/ -run 'TestAuditGovernance'
go test ./internal/auditgovernance/ -run 'TestRuntimeReady|TestProbeBound|TestWarn'
go test ./cmd/server/ -run 'TestReadyz'
make check
```
