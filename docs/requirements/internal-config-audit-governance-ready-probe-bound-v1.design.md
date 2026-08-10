# Design — config-driven /readyz audit-governance store-probe bound (close the D1 drill gap's config-coupling tail)

Module: `internal/auditgovernance` (+ config-module pins). Spec: `docs/requirements/internal-config-audit-governance-ready-probe-bound-v1.spec.md` (HEAD `15763e2`). All citations re-verified at HEAD `15763e2`.

## 1. Verification summary (evidence claims re-checked)

| Evidence claim | Verdict at HEAD `15763e2` |
|---|---|
| `cmd/server/http.go:38-66` — gap already closed: 2s `readyzProbeTimeout` bounds ping (:96-99), storage (:102-105), `extra.Ready(probeCtx)` (:109) | **CONFIRMED.** Const :44-52; the "raw, unbounded request context" claim is stale. |
| `runtime.go:168-183` — superseded by `probeAndRecord` (:251-268) | **CONFIRMED.** Timeout/cancel → Warn + `recordDegraded(true,0)` + nil (:253-256/:260-263); draining → error (:257-259); lag → degraded (:264-266); genuine errors fail-closed (:258/:262 texts "audit governance drain/backlog lookup failed"); `isProbeCtxError` (:231-235) covers DeadlineExceeded **and** Canceled. |
| `runtime.go:156-166` — `PendingBacklogAge` passes caller ctx, now gauge-only | **CONFIRMED.** :198-207; `Ready()`'s reads go through the runtime's own bounded `probeCtx` (:252). |
| `config_audit_governance.go` — field :26, default 5 via env :73, `ClaimTTL > 2×HTTPTimeout` :261, cap 29 :284 | **CONFIRMED.** Envelope 1..29 also documented (docs/configuration.md:269); `getEnvInt` empty-string → default (config.go:367-371). |
| `relay.go:132-142` → relocated :247-255 — delivery timeouts already transient | **CONFIRMED.** Closed list (conflict/invalid receipt + HTTP 409/422); everything else transient. Caveat reading satisfied. |
| Net-new gap: `const storeProbeTimeout = 2s` (runtime.go:26, sole use :252) hardcoded; warn unpinned (all harness loggers `io.Discard` — runtime_ready_test.go:149, readyz_drill_test.go:83/:491); no loader pin for HTTP_TIMEOUT | **CONFIRMED.** `r.httpTimeout` already carried (runtime.go:88, :92) for the relay; unused by `Ready`. |
| Harness configs already `HTTPTimeoutSeconds=1` (runtime_test.go:44, readyz_drill_test.go:99); existing windows [1s,5s] and poll deadlines 3s/4s stay valid | **CONFIRMED.** A 1s-bound probe fires at 1s (context timers never fire early), satisfying every `elapsed ≥ 1s` lower bound exactly; drill test windows readyz_drill_test.go:169-173/:247, runtime_ready_test.go:181-188. |
| `runtime_ready_test.go` = 472 lines (500-line gate) | **CONFIRMED.** New test file required. |

## 2. API changes (exact, zero public surface)

**Production — `internal/auditgovernance/runtime.go`, 3 hunks:**
1. Delete the const block :22-26 (`storeProbeTimeout` + its comment).
2. :252 `context.WithTimeout(ctx, storeProbeTimeout)` → `context.WithTimeout(ctx, r.httpTimeout)`.
3. Rewrite `probeAndRecord`'s doc comment (:246-249) to name the configured bound (`r.httpTimeout` = `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS`).

**Config boundary — NO change.** Env `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (default 5, :73), envelope 1..29 (:284), `ClaimTTLSeconds > 2×HTTPTimeoutSeconds` (:261), docs/configuration.md — all exactly as shipped.

**Handler boundary — NO change.** `readyzProbeTimeout` (2s) stays the seam cap for ping + storage + extra. Effective /readyz audit bound = `min(readyzProbeTimeout, r.httpTimeout)` — Go's derived-deadline semantics give min() for free. The 6s < 10s worst-case comment (http.go:46-52) stays **exact** for every config value: the seam can never exceed 2s per audit probe.

**Test-only API additions:**
- `scriptedStore` (runtime_ready_test.go): new `backlogHang bool` field + `setBacklogHang(bool)` setter; `OldestPendingAuditGovernance` honors it (drain probe healthy, backlog probe hangs). ~8 lines (472 → ~480, under the 500 gate).
- New capturing-logger harness in `probe_bound_test.go`: `bytes.Buffer` + `slog.NewTextHandler(&buf, nil)` passed to `New` (the spec's "with a warn" clause needs a logger that isn't `io.Discard`).

**Comment sweep** (they reference the deleted const / "the 2s probe deadline"): runtime_ready_test.go:4-7/:174/:182-183; readyz_drill_test.go:5/:158/:170/:233/:248. Seam-level comments about the *handler's* 2s cap stay.

## 3. Compatibility constraints

- **Behavior-identical at default**: production default 5s → seam audit probes still 2s (handler cap); only the run-loop background probe (:322) lengthens 2s → 5s on a wedged store — still bounded, still degrades (never 503), loop never stops (`TestRuntimeRunLoopSurvivesWedgedStore` F17).
- **Operator lowering** (`HTTP_TIMEOUT < 2`) tightens seam + background probes; **raising** toward 29 lengthens only the background per-cycle stall (≤29s, bounded, non-fatal).
- **Every existing pin stays green unchanged**: `TestReadyzAuditGovernanceDegradedDrill` (elapsed ∈ [1s,5s] — the 1s bound fires at 1s), `TestReadyzBacklogLagDegradesNot503`, `TestReadyzDrainStill503`, `TestReadyzHealthyExtra200`, `TestReadyzExtraProbeTimeout` (own 2s checker, config-free), `TestReadyzDeadLetteredBacklog200AndGaugeZero`, `TestReadyzImmediateExtraError`, `TestRuntimeReadyDegradedSentinel`, c1/c2/c3, `TestRuntimeReadyDegradesOnBacklogLag` + draining gate, run-loop tests (3s/4s deadlines ≫ 1s bound).
- **No schema/DB migration, no telemetry series, no alerts.yml, no `go.mod` change.**

## 4. Failure modes (FM)

| # | Mode | Guard |
|---|------|-------|
| FM-1 | Regression to a hardcoded 2s const | REQ-3 upper bound `elapsed < 1.5s` fails (2s ∉ [1s, 1.5s)). |
| FM-2 | `HTTPTimeoutSeconds` raised near 29 | Background probe stall ≤ 29s/poll cycle — bounded, still degrades; seam unaffected (2s cap); documented trade-off (§7 of spec). |
| FM-3 | `httpTimeout` zero | `WithTimeout(ctx, 0)` → both probes degrade instantly, Ready nil. Impossible in production: validation `HTTPTimeoutSeconds > 0` (:260); no defensive code. |
| FM-4 | Elapsed-window flake | Upper bound 1.5s has 500 ms slack over the event-driven 1s return (same idiom as the existing [1s,5s] windows); lower bound 1s is exact (context timers never fire early). |
| FM-5 | Warn-pin brittleness (logger format drift) | Assert on the slog record's message + `probe` attr (`msg="audit governance store probe timed out — degraded"`, `probe=drain`/`backlog`), not buffer text; count == 1. |
| FM-6 | Wrong-probe warn ambiguity (both probes share one ctx; hang mode hangs drain first) | Backlog-warn subtest uses the new `backlogHang` so only the backlog probe hangs; a misread that asserts both warns fails loudly (drain warn fires first, Ready returns). |
| FM-7 | Pre-expired caller ctx (DeadlineExceeded arm) | REQ-5 twin: degraded + nil + age 0 + `elapsed < 1s`, immediate return. |
| FM-8 | Unpinned "with a warn" clause (today: all harness loggers `io.Discard`) | REQ-4 capturing-handler assertions; healthy probe emits **no** timeout warn (keeps the pin discriminating). |

## 5. Migration steps

No data/DB migration; no config migration (env name, default, envelope unchanged). Implementation sequence:

1. `runtime.go`: delete const; swap call site (:252) to `r.httpTimeout`; fix doc comment.
2. Comment sweep in `runtime_ready_test.go` + `readyz_drill_test.go`.
3. `internal/config/config_audit_governance_test.go`: REQ-2 loader pins.
4. `runtime_ready_test.go`: `scriptedStore.backlogHang` extension (test-only, ~8 lines).
5. NEW `internal/auditgovernance/probe_bound_test.go`: REQ-3 window test + REQ-4 warn tests + REQ-5 DeadlineExceeded twin (keeps runtime_ready_test.go at 472; new file well under 500).
6. `make check` (gofmt / build / vet / test) + targeted: `go test ./internal/config/ ./internal/auditgovernance/ ./cmd/server/`.

## 6. Testable acceptance mapping

| Supplied acceptance | Status | Testable form |
|---|---|---|
| **(a)** readyz: pending-read blocks beyond bound → 200 degraded + warn, not 503 | Implemented since `15763e2`; warn clause unpinned | `TestReadyzAuditGovernanceDegradedDrill` (unchanged) + **REQ-4** `TestProbeTimeoutWarnsDrain` / `TestProbeTimeoutWarnsBacklog` / `TestProbeHealthyEmitsNoTimeoutWarn` (capturing handler; 1 warn each, `probe` attr exact; healthy → none) |
| **(b)** `Runtime.Ready` nil on expired/deadline-exceeded ctx; genuine errors + draining still fail | Implemented | c1/c2/c3 unchanged + **REQ-5** DeadlineExceeded twin (expired `context.WithDeadline` ctx → nil, `Degraded()==true`, `BacklogAge()==0`, `elapsed < 1s`); `TestRuntimeReadyDegradesOnBacklogLag` + draining gate unchanged; `TestReadyzDrainStill503` unchanged |
| **(c)** probe bound = config `HTTPTimeoutSeconds` | Net-new (spec core) | **REQ-1** wiring (one-line call-site change; const deleted); **REQ-2** `TestAuditGovernanceHTTPTimeoutLoaderDefaultAndOverride` (env-neutralized `loadAuditGovernanceConfig`: unset/empty → 5; `=7` → 7; envelope pins :154/:159 untouched); **REQ-3** `TestProbeBoundFollowsConfig` (harness `HTTPTimeoutSeconds=1` + hang → nil, degraded, age 0, **elapsed ∈ [1s, 1.5s)** — regression to 2s const fails) |

## 7. Gates

`gofmt -l` clean · `go build ./...` · `go vet ./...` · `go test ./...` (`make check`). Line gates: runtime.go one-const deletion + one-line swap; runtime_ready_test.go 472 → ~480; new probe_bound_test.go (~140 lines) ≤ 500.

## 8. Verification steps

```
go test ./internal/config/ -run 'TestAuditGovernance'
go test ./internal/auditgovernance/ -run 'TestRuntimeReady|TestProbeBound|TestProbe'
go test ./cmd/server/ -run 'TestReadyz'
make check
```
