# Design — D1 read-path completion: cache-fed backlog-age gauge (REQ-4 fix)

> Evidence: `docs/auto/runs/complete-d1-read-path-bounded-store-probes-degra-8f4abe23/artifacts/requirements-10762e10/requirements.md`
> Working base: commit `15763e2` + uncommitted worktree delta (`cmd/server/build.go` modified).

## 0. Verification ledger (evidence → verdict)

| Claim | Verdict | Evidence in tree |
|---|---|---|
| `runtime.go` Ready→probeAndRecord, 2s storeProbeTimeout, timeout/cancel → degraded+nil | ✅ accurate | `internal/auditgovernance/runtime.go:246-294`; drain fork `:253-259`, backlog fork `:265-272`, `Ready`=`probeAndRecord` `:294` |
| `audit_governance_claim.go` ctx pass-through probes, no WithTimeout, terminal exclusion | ✅ accurate | `internal/repository/audit_governance_claim.go:211-248` — `QueryRowContext` only, `delivered_at_ns=0 AND failed_at_ns=0` in both queries |
| `http.go` shared 2s probe budget, degraded → 200 marker | ✅ accurate | `cmd/server/http.go:96-124` (`readyzProbeTimeout`, marker at `:119-121`) |
| `runtime_test.go` maxLag pin moved `:415`→`:618` | ✅ accurate | `runtime_test.go:618` = `TestRuntimeReadyDegradesOnBacklogLag` |
| Repository layer pins green | ✅ | `go test ./internal/repository/ -run AuditGovernance` → ok 3.1s |
| Runtime pins green | ✅ | `go test ./internal/auditgovernance/` → ok 28.4s |
| Seam pins green (probe timeout/error/healthy/lag/drain/dead-letter) | ✅ | `go test ./cmd/server/ -run 'TestReadyz…|TestAlerts…|TestNoExecutable…'` → ok 4.6s |
| **Regression: `cmd/server/build.go:103` gauge callback mutated to live store query** | **CONFIRMED** | `build.go:101-106`: `age, _, _ := rt.PendingBacklogAge(ctx) // M1-PROD mutation`; doc comment `:94-100` still states the cache contract. HEAD version was a different inline closure (diff shows the whole cache-fed refactor is uncommitted) |
| Drill hangs | **REPRODUCED** | `go test ./cmd/server/ -run TestReadyzAuditGovernanceDegradedDrill -timeout 70s` → FAIL at 70.08s (goroutine dump; blocked in store probe) |
| `setPanicBacklog` "existing-but-unused" | **Partially refuted / gap confirmed** | `runtime_gauge_scrape_test.go:93` DOES arm it — but only against a **locally-registered cache-fed callback** (`int64(rt.BacklogAge().Seconds())`), never against the production `auditGovernanceBacklogAgeGaugeFn`. The production callback has **no** panic/zero-I/O guard, and `cmd/server` has no `scriptedStore` (only `hangingAuditStore`, `readyz_drill_test.go:397-410`). The gap is real; the wording "unused" is imprecise. |
| "requirements.md (114 lines)" | **Metadata drift** | File is ~37 lines (the summary itself); the §4 verbatim (a)–(d) text is not present at the cited path. |

## 1. Root cause & mechanism (why today's pins miss it)

- `readyz` wedge drill (`readyz_drill_test.go:445-466`) wraps the store in a `hangingAuditStore`; after asserting the 200 marker it calls the **production** `auditGovernanceBacklogAgeGaugeFn(rt)(ctx)` with `ctx = context.Background()`.
- The mutated callback performs live probes (`HasPendingDraining` + `OldestPending`). Against the hanging store these block until the ctx deadline **never fires** (`context.Background()` — the OTel scrape context carries no deadline). Result: unrecoverable hang, not a timeout; the test was itself designed as the *wedge detector* and is precisely the pin that catches the mutation.
- `TestReadyzDeadLetteredBacklog200AndGaugeZero` uses a **healthy live store**, so the mutated callback returns correct values there — it cannot distinguish cache-fed from store-fed; it only catches wrong-*values*, never wrong-*source*.
- Production blast radius of recommitting the mutation: every `/metrics` scrape performs 2 SQL queries; against a wedged relay DB, the scrape blocks indefinitely; `database/sql` connections accumulate (SQLite pool is unbounded here), starving other endpoints; plus `age, _, _` swallows genuine store errors → error-shape becomes 0 (indistinguishable from healthy-empty), silently disabling the 450s alert's first arm (`backlog_age > 450`) whenever the DB is erroring, not wedged. The `degraded==1` OR-arm protects the wedge case only.

## 2. Design (REQ-4 — the only production change)

**Decision: restore the documented contract in `cmd/server/build.go`, and extend the panic guard to the production callback.** The degraded-cache seam (`probeAndRecord`, `/readyz` marker, run-loop refresh at `runtime.go:251,322`, drain/degraded gauges) stays as shipped; it is verified complete.

```go
// auditGovernanceBacklogAgeGaugeFn returns the backlog-age gauge callback.
// D3: the value comes from the run-loop-refreshed cache (Runtime.BacklogAge
// getter — zero store I/O per scrape). ctx is never touched: a scrape must
// never issue a query; the REQ-2 guard (readyz_drill_test.go, panic-armed
// store) proves it by construction.
func auditGovernanceBacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64 {
	return func(context.Context) int64 {
		return int64(rt.BacklogAge().Seconds())
	}
}
```

Second, a new cmd-side seam test that arms the panic overlay **against the production callback** (cmd/server cannot import `internal/auditgovernance`'s unexported `scriptedStore`, so this is a new wrapper in the same file family):

```go
// panicGuardedStore wraps a real audit-governance store: once armed, both
// readiness-probe methods panic — the REQ-2 proof that the PRODUCTION gauge
// callback is cache-fed. Any store query from the callback after arming
// crashes the test (loud regression surface, mirrors scriptedStore).
type panicGuardedStore struct {
	auditgovernance.Store
	mu         sync.Mutex
	armed      bool
}
func (s *panicGuardedStore) setProbeArmed(p bool) { s.mu.Lock(); s.armed = p; s.mu.Unlock() }
func (s *panicGuardedStore) HasPendingDrainingAuditGovernance(ctx context.Context) (bool, error) {
	if s.armed { panic("store probe from audit_governance gauge callback") }
	return s.Store.HasPendingDrainingAuditGovernance(ctx)
}
func (s *panicGuardedStore) OldestPendingAuditGovernance(ctx context.Context) (time.Time, bool, error) {
	if s.armed { panic("store probe from audit_governance gauge callback") }
	return s.Store.OldestPendingAuditGovernance(ctx)
}

// TestReadyzBacklogAgeGaugeCacheFedNoProbePanic — REQ-2/REQ-4 acceptance at
// the production seam: prime the cache through Ready() (2s-backdated fact),
// arm the panic, then the production gauge fns must return the cached
// (age>0, degraded=0) pair without touching the store. Pre-fix this panics.
func TestReadyzBacklogAgeGaugeCacheFedNoProbePanic(t *testing.T) { … }
```

No other changes. `PendingBacklogAge(ctx)` stays (public accessor used by tests/pre-asserts).

## 3. API changes

| Surface | Change |
|---|---|
| `cmd/server.build` `auditGovernanceBacklogAgeGaugeFn` | Body only; the `func(context.Context) int64` signature, name, and registration call (`build.go:154`) unchanged |
| `internal/auditgovernance.Runtime` | **No change** — `BacklogAge()` (zero-I/O cache getter), `PendingBacklogAge(ctx)` (querying), `Degraded()`, `Draining()`, `BoundTenants()` all remain |
| HTTP `/readyz` | Unchanged wire: `{"ok":true}` healthy, `{"ok":true,"degraded":true,"backlog_age_seconds":N}` degraded |
| Metrics | `audit_governance_backlog_age_seconds`, `audit_governance_degraded`, `audit_governance_bound_tenants`, `audit_governance_draining` — names/types unchanged |
| alerts.yml | Unchanged (`expr … cost > 450 OR audit_governance_degraded == 1`, `for: 10m`) — parity test keeps it pinned |
| Config | **No change** — no new env; `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` and the `BacklogAlertThresholdSeconds()` derivation stay the single source of the 450 |

## 4. Compatibility constraints

- **DB/schema (I2,Z):** no migrations; the probes' SQL text unchanged (I1-safe; placeholder rules untouched).
- **Opt-in default (I5):** gauges registered only when audit-governance is enabled; `nil` runtime → no gauge registration (unchanged).
- **Stdlib-only (I6):** fix adds no imports; test adds `sync` only — no go.mod change.
- **Binary wire/BPM parity:** alert rule name/expr — rule expects `backlog_age_seconds` from the same gauge; the pristine cache reports the same value as before except it can't diverge per-scrape.
- **Operator runbook:** `docs/snaplink-audit-governance.md` behavior text — /readyz 200 + marker + 450s alert is already the shipped contract (per the earlier adversarial finding, ensure lines 136-138 reflect degrade-not-fail); no action beyond confirming the doc matches the shipped code.
- **Concurrency:** `degradedMu` single-lock pair discipline is preserved (T7 race pin); the callback's RLock read is safe against concurrent `probeAndRecord` on the run loop and /readyz.

## 5. Failure modes

| Mode | Behavior with fix | Behavior with the regression (must not ship) |
|---|---|---|
| Relay DB wedged (`-connected` | Scrape reads cached (degraded=1,age=0/age), zero I/O, no block | Scrape blocks on Background ctx until `storeProbeTimeout` per query and the OTel callback returns error → /metrics scrape hangs → prometheus scrape timeout, DB pool exhaustion |
| Genuine store error in probe | Run loop `probeFailed` warn, cache keeps last-known-good; /readyz fail-closed 503 (unchanged, REQ-5) | `age, _, _` swallows err → exports 0 (healthy-looking) → alert first arm silent |
| Run loop blocked 2s (wedge) | Loop resumes next poll (F17 pin) | — |
| Cache staleness after startup | Gauge reads 0 until first poll (≤1s default) or first /readyz — documented D3; alert has `for_12:15m` margin | n/a |
| Divergence: Ready fails (503) while cached degrades=false | Known accepted seam (fail-closed evict wins; alert tooling sees 503) — **pre-existing**, out of scope | n/a |

## 6. Migration steps (checklist, each gate-gated)

1. Patch `cmd/server/build.go:101-106` to the cache-fed body (REMOVE-AND-RETREE: delete the `PendingBacklogAge` line; name the param `context.Context`).
2. Add `panicGuardedStore` + `TestReadyzBacklogAgeGaugeCacheFedNoProbePanic` in `cmd/server/readyz_drill_test.go` (and keep `hangingAuditStore` for REQ-3 wedge drill unchanged).
3. Run gates: `gofmt -l cmd/server/ internal/` (no output) · `go vet ./cmd/server/ ./internal/…` · `go test ./cmd/server/ -run 'TestReadyz|TestAlerts|TestNoExecutable450' -timeout 120s` (drill now passes within the 5s bound) · `go test ./internal/auditgovernance/ -timeout 180s` · `go test ./internal/repository/ -run AuditGovernance`.
4. `make check` head-to-toe (CI gate, zero-network SQLite+local) — must be all-green, including `TestReadyzAuditGovernanceDegradedDrill` (previously hanging).
5. Optional race meta: `make test-race-meta` (T7 semantics under -race).
6. Commit as its own change with a pointer to this design file (do NOT re-commit the mutation).

## 7. Testable acceptance mapping

| Req | Acceptance (testable) | Pin | Today | After |
|---|---|---|---|---|
| REQ-1/2 | terminal exclusion + ctx pass-through | `runtime_test.go` dead-rows pins; `repository` pending-idx suite; `audit_governance_claim.go:211-248` | Green | Green (no code change) |
| REQ-2 (zero store I/O from gauge) | panic guard armed **against production** `auditGovernanceBacklogAgeGaugeFn` returns cached value | new `TestReadyzBacklogAgeGaugeCacheFedNoProbePanic` | Red: mutated callback panics; pre-fix default: hang | Green |
| REQ-3 | wedge → nil Ready, degraded (true,0), /readyz 200 + marker within 2s bound | `TestRuntimeReadyDegradedSentinel` (+fabric only), `TestRuntimeReadyFailClosed…` c3, `TestReadyzAuditGovernanceDegradedDrill` | **Red (45s/70s hang)** | Green (≤5s) |
| REQ-5 | exact fail-closed strings + drain 503 | c1/c2 (`audit governance {drain,backlog} lookup failed`), `TestReadyzDrainStill503` | Green | Green |
| REQ-6 | maxLag flip, dead-only, healthy byte-identity, I1/I2/I5/I6, 450-pinning | `TestRuntimeReadyDegradesOnBacklogLag`, `TestReadyzBacklogLagDegradesNot503`, `TestReadyzDeadLettered…` phases 0/1, `TestReadyzHealthyExtra200`, parity + literal 450 scan | Green except drill | Green |
| (a)-(d) acceptance (verbatim §4 not present in cited file — reconstructed) | (b) 200-marker on lag ≥ (c) wedge drill hang (d) dead-lettered 0; (a) gauge cache-fed | above | (c) ❌ | All ✅ |

**Exit criteria:** `go test ./cmd/server/ ./internal/auditgovernance/ ./internal/repository/ ./internal/telemetry/ -count=1` green; `grep -rn "PendingBacklogAge" cmd/` returns only `cmd/server/…`-free (zero) hits; `make check` green; drill sub-test elapsed ≤ 5s.