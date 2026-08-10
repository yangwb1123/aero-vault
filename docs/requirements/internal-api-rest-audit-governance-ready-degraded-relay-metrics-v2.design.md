# Design — D1 drill read-path half: bounded `Ready()` probes + degraded sentinel + `/readyz` degraded payload + run-loop gauge freshness (v2; delta of the B3-2 contract)

**Module:** `internal/auditgovernance` + `cmd/server` + `deploy/helm` + `Makefile` (analysis label `internal/api/rest` is traceability only — no `internal/api/rest` file changes; `/readyz` is registered in `cmd/server/http.go:104`)
**Requirements:** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v2.spec.md` (REQ-1..9, T1b/T1c/T2/T5/T6/T3/T8/T7) · **Design basis (shipped subset):** `…-v1.design.md` + `…-v1.spec.md` (S1–S5 landed by `15763e2`)
**HEAD:** `15763e2` + uncommitted worktree · **Date:** 2026-08-08
**Scope lock:** D1 bounded probes + degraded cache (`runtime.go`), D2 degraded payload + `readinessGroup` composition (`http.go`), D3 gauge callback reads the cache (`build.go`), H1 helm `timeoutSeconds: 10`, H2 `repo.Ping` bound, H3 `test-race-meta` scope. Preservation pins: REQ-1 fail-closed branches, healthy `/readyz` body byte-identity, alert rule, relay counters. **Out of scope:** config/env surface, migration, new metric, alert-rule change, counter change, billing-runtime readiness, `internal/api/rest`, sibling directions (B3-1/B3-3/B3-5/B3-6, admin-origin e2e).

---

## 1. Verification register (evidence re-checked, not trusted)

Every citation in the evidence (the v2 spec) was re-read on this checkout. **14 of 15 verify as claimed; E4's staleness correction is confirmed; E15's number is drifted but its conclusion holds; two anchors drift in line numbers only (E8, E9).**

| # | Evidence claim | Re-verified location (working tree) | Verdict |
|---|---|---|---|
| E1 | `BacklogAge(ctx)` store-querying `runtime.go:151-159`; `Ready()` `:162-182`, drain probe `:163`, backlog probe via `BacklogAge(ctx)` `:174`, maxLag flip `:178-181` | `internal/auditgovernance/runtime.go` — `BacklogAge` `:151-159` (`OldestPendingAuditGovernance` + `time.Since`); `Ready` `:162-182`; `HasPendingDrainingAuditGovernance(ctx)` `:163`; `r.BacklogAge(ctx)` `:174`; `if ok && age > r.maxLag { logger.Warn("audit governance relay degraded", …) }` `:178-181` then `return nil` | ✅ **exact** |
| E2 | Zero hits for `Degraded`/`probeAndRecord`/`isProbeCtxError`/`degradedMu`/`PendingBacklogAge` | `grep -rn` across `internal/auditgovernance/` + `cmd/server/` (non-test): single hit `cmd/server/http.go:115` (`cfg.AI.DegradedMode`, unrelated AI flag); test-only hits `cmd/server/readyz_drill_test.go:358,370` (alert-name marker string) | ✅ **verified absent** |
| E3 | `readyzProbeTimeout` `http.go:34-41`; `readinessGroup` `:43-49`; `readyzHandler` `:54-75` | `cmd/server/http.go` — const + comment `:34-41`; group (AND-`Ready` only) `:43-49`; handler `:54-75`: `repo.Ping(req.Context())` `:56-58` (unbounded — H2 gap), storage probe under `probeCtx` `:62-66`, `extra.Ready(probeCtx)` `:69-71`, unconditional `{"ok":true}` `:73-75` | ✅ **holds** |
| E4 | "`extra.Ready(req.Context())` unbounded at :66" | Working tree has `extra.Ready(probeCtx)` at `http.go:69` — **already bounded** by the 2 s `readyzProbeTimeout`; `git diff HEAD -- cmd/server/http.go` confirms this is uncommitted sibling readyz-probe-timeout work (`probeCtx` wrap + const + 3 tests). Operative gaps move to the **runtime side**: `Ready()`'s own probes (`runtime.go:163`, `:174`) still run on the caller ctx, and the degraded payload is absent | ✅ **correction confirmed** (stale at HEAD; the spec's correction is right) |
| E5 | Gauge callback per-scrape store query `build.go:94-101` | `cmd/server/build.go:94-101` — `auditGovernanceBacklogAgeGaugeFn` `:98`, calls `rt.BacklogAge(ctx)` `:100` per scrape; registration gate `if auditRuntime != nil` `:127` | ✅ **holds** |
| E6 | `runtimeReadiness` `audit_governance.go:51-64` returns a `readinessGroup` | `cmd/server/audit_governance.go:51-64` — appends `billingRuntime` and `auditRuntime` into a `readinessGroup`, nil when empty; wired `cmd/server/main.go:157` | ✅ **exact** |
| E7 | `http_test.go:69-129` three nil-extra readyz tests | `cmd/server/http_test.go` — `TestReadyzStorageProbeTimeout` `:69-88` (blocking-stat stub, elapsed ∈ [1 s, 5 s]), `TestReadyzErrNotFoundIsReady` `:93-108` (body `{"ok":true}` at `:103`), `TestReadyzImmediateStorageError` `:110-129`; stubs `stubReadyRepo`/`blockingStatStorage`/`notFoundStatStorage`/`errStatStorage` `:27-60`; all pass `nil` extra | ✅ **exact** |
| E8 | Gauge registration `metrics.go:352-365` | `internal/telemetry/metrics.go` — `RegisterAuditGovernanceBacklogAgeGauge` comment `:364`, func `:368` (observable int64 gauge `audit_governance.backlog_age_seconds`) | ✅ **holds** (drift: 364-368 vs 352-365; content identical) |
| E9 | Alert expr `> 450`, `for: 10m` | `deploy/prometheus/alerts.yml:176-186` — group `aero-vault-audit-governance` `:176`; rule `AuditGovernanceBacklogDegraded` `:181-186`: `expr: audit_governance_backlog_age_seconds > 450` `:182`, `for: 10m` `:183`, `severity: warning` `:184` | ✅ **holds** (drift: rule `:181-186`; analysis cited `:163` — uncommitted edits) |
| E10 | Helm readinessProbe `deployment.yaml:83-88`, no `timeoutSeconds` | `deploy/helm/aero-vault/templates/deployment.yaml:83-88` — `readinessProbe` (`httpGet: /readyz`, `initialDelaySeconds: 3`, `periodSeconds: 10`); `grep -n timeoutSeconds` → zero hits in file | ✅ **exact** |
| E11 | `Makefile:116-123` `test-race-meta` excludes auditgovernance | `Makefile` — target `test-race-meta` runs `go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/`; inside `check` `:122`; comment `:113-116` names the metadata-atomicity scope | ✅ **exact** |
| E12 | `BacklogAge(ctx)` rename surface = 4 call sites | **Corrected — the actual surface is 9** (the spec's 4 undercounted sibling-test call sites; re-grepped live): `runtime.go:174` (internal), `cmd/server/build.go:100` (renamed here; *deleted* later by D3's callback swap, §6 step 4), `runtime_test.go:449`, `:492`, `internal/auditgovernance/cumulative_window_test.go:190`, `cmd/server/readyz_drill_test.go:214`, `:276`, `:301`, `:319` — 9 total, all mechanical (7 in test files) | ✅ **verified live** (grep; spec's 4 is stale) |
| E13 | `runtime_test.go` 498/500; harness `runtimeConfig` maxLag 4 s / poll 10 ms | `wc -l internal/auditgovernance/runtime_test.go` → **498**; `runtimeConfig` `:39-46` (`MaxLagSeconds=4`, `PollMilliseconds=10`); `TestRuntimeReadyDegradesOnBacklogLag` `:415-466`; `TestRuntimeBacklogAgeZeroWhenNoPending` `:471-497` | ✅ **exact** |
| E14 | `go.yaml.in/yaml/v2` available for T3 | `go.mod:73` — `go.yaml.in/yaml/v2 v2.4.4 // indirect`, zero importers. **Moot after the T3 fold (reviewer):** T3's unique assertions fold into the existing stdlib `TestAlertsYMLAuditGovernanceExprParity` (`readyz_drill_test.go:344-373`), so **no promotion happens** — strictly better under I6 ("Stdlib 优先") and avoids two overlapping tests with two derivation semantics (literal 450 vs config-derived) | ✅ **verified** (promotion dropped) |
| E15 | `go test -race -count=1 ./internal/auditgovernance/` fits the 300 s budget | **Re-run live on this checkout:** `go test -race -count=1 -timeout 300s ./internal/auditgovernance/` → `ok … 78.658s`. The spec's 42.7 s number is **stale** (worktree has grown since that run; machine variance), but the feasibility conclusion holds with **3.8× margin** under the unchanged 300 s budget | ✅ **verified live** (number drifted: 78.7 s) |

**Cross-checks beyond the spec's own list (also re-verified):** `ClaimAuditGovernance` at `internal/repository/audit_governance_claim.go:20`, `FailAuditGovernance` at `:182`, `OldestPendingAuditGovernance` at `:211-222` with terminal predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` at `:219-220`; repo pin `audit_governance_test.go:519` (`TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`), `OldestPending ok==false` at `:542`; `relay_metrics_test.go:88`; `metrics_test.go:106` / `:168`; `billing.Runtime.Ready` at `internal/billing/runtime.go:136` (no `Degraded()` — group contributes false/0); config defaults `AUDIT_GOVERNANCE_POLL_MILLISECONDS=1000` / `AUDIT_GOVERNANCE_MAX_LAG_SECONDS=900` at `internal/config/config_audit_governance.go:61/:66` (alert 450 = 900×0.5).

---

## 2. Design

### D1 — `internal/auditgovernance/runtime.go`: bounded probes + degraded cache (231 → ~276 lines)

**New package constant** (comment cross-references `readyzProbeTimeout`, `cmd/server/http.go:34-41` — same rationale, same 2 s value, independent symbol; deliberately **not** derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (5 s default — relay-HTTP bound, too slow for a readiness probe); no new config knob):

```go
// storeProbeTimeout bounds Ready()'s two store probes independently of
// AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS (5s default, relay HTTP bound) and
// the caller's request context: a wedged relay store must not hold /readyz
// beyond ~2s per probe. Mirrors readyzProbeTimeout (cmd/server/http.go).
const storeProbeTimeout = 2 * time.Second
```

**Rename (G1 — Go has no overloading):** `BacklogAge(ctx) (time.Duration, bool, error)` (`runtime.go:151-159`) → `PendingBacklogAge(ctx)`, same signature/body. The name `BacklogAge` is freed for the zero-I/O cache getter. Call sites — **9 total (E12)**: `runtime.go:174` (internal), `build.go:100` (renamed here, *replaced* by the cache getter in D3's step-4 body swap), `runtime_test.go:449/:492`, `cumulative_window_test.go:190`, `readyz_drill_test.go:214/:276/:301/:319` (the two pinned runtime tests stay green verbatim, `runtime_test.go` stays 498 lines).

**`Runtime` gains three fields** (struct has no mutex today; `sync` already imported):

```go
degradedMu   sync.RWMutex   // guards degraded + backlogAge
degraded     bool           // last probe result: lag > maxLag, or probe timeout/cancel
backlogAge   time.Duration  // last probe age; 0 when none/unknown
```

**New zero-I/O accessors:**

```go
func (r *Runtime) Degraded() bool             // RLock read
func (r *Runtime) BacklogAge() time.Duration  // RLock read
```

**`Ready()` restructure** — both store probes (`HasPendingDrainingAuditGovernance`, `OldestPendingAuditGovernance` — the age source) run under `context.WithTimeout(ctx, storeProbeTimeout)` via a shared helper:

```go
func (r *Runtime) probeAndRecord(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)
	defer cancel()
	draining, err := r.store.HasPendingDrainingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxError(err) { r.recordDegraded(true, 0); return nil } // wedged store → degraded, never 503
		return errors.New("audit governance drain lookup failed")          // genuine error → fail-closed (unchanged)
	}
	if draining {
		return errors.New("audit governance binding drain is in progress") // unchanged
	}
	oldest, ok, err := r.store.OldestPendingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxError(err) { r.recordDegraded(true, 0); return nil }
		return errors.New("audit governance backlog lookup failed")
	}
	age := time.Duration(0)
	if ok {
		age = time.Since(oldest)
	}
	if ok && age > r.maxLag {
		r.logger.Warn("audit governance relay degraded",
			"backlog_age", age.String(), "max_lag", r.maxLag.String()) // unchanged warn
		r.recordDegraded(true, age)
		return nil
	}
	r.recordDegraded(false, age) // 0 when !ok (healthy/empty)
	return nil
}
// isProbeCtxError: errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
// recordDegraded writes BOTH fields under a single degradedMu.Lock() — readers can only observe valid pairs.
```

- **H0 (hardening, carried from v1 design):** `recordDegraded(degraded, age)` must write **both** fields under a **single** `degradedMu.Lock()` acquisition (per-field writes allow torn pairs); probe timeout/cancel → `(true, 0)` — age unknown per parent-spec REQ-3, **not** `(false, 0)`. `Ready()` becomes: `return r.probeAndRecord(ctx)` — its behavior is now the helper's.
- **Preserved branches (pin, don't touch):** genuine non-context store errors → hard errors with unchanged strings; drain-in-progress → hard error; lag > maxLag → warn + nil; terminal rows excluded by the unchanged store predicate (`claim.go:219-220`).
- **Run-loop refresh (G3):** in `run()`, after `cleanupDelivered()` and an `r.stopping()` guard (same as the other phases), call `probeAndRecord(context.Background())` once per poll cycle. A store error in the loop logs and skips recording — it never stops the loop. Gauge freshness ≤ poll interval (default 1 s) independent of `/readyz` traffic. Note: a wedged store serializes the loop ≤ 2 s per cycle behind `storeProbeTimeout`; the loop's own store calls are already bounded by `httpTimeout` (5 s default) — no new order of magnitude, documented, no code change.

### D2 — `cmd/server/http.go`: degraded payload + `readinessGroup` composition (192 → ~225 lines)

New interface next to `readinessChecker` (`:31`):

```go
type degradedChecker interface {
	Degraded() bool
	BacklogAge() time.Duration
}
```

`readinessGroup` (`:43-49`) gains composition methods — **mandatory (G2)**: production `extra` is a `readinessGroup` (`audit_governance.go:51-64`), so a bare type-assert on the runtime in `readyzHandler` fails; with the group methods, `main.go:157` wiring is untouched and `billing.Runtime` (no `Degraded()`, `billing/runtime.go:136`) contributes false/0:

```go
func (g readinessGroup) Degraded() bool {           // OR over implementing members; false when none
	for _, c := range g {
		if dc, ok := c.(degradedChecker); ok && dc.Degraded() {
			return true
		}
	}
	return false
}
func (g readinessGroup) BacklogAge() time.Duration { // max over implementing members; 0 when none
	var max time.Duration
	for _, c := range g {
		if dc, ok := c.(degradedChecker); ok {
			if a := dc.BacklogAge(); a > max {
				max = a
			}
		}
	}
	return max
}
```

`readyzHandler` (`:54-75`) — after `extra.Ready(probeCtx)` succeeds (`:69-71`), type-assert; degraded → **HTTP 200** with the marker body; healthy path stays byte-identical:

```go
if extra != nil {
	if err := extra.Ready(probeCtx); err != nil {
		http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)
		return
	}
	if dc, ok := extra.(degradedChecker); ok && dc.Degraded() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true,"degraded":true,"backlog_age_seconds":%d}`, int64(dc.BacklogAge().Seconds()))
		return
	}
}
// healthy path byte-identical
w.Header().Set("Content-Type", "application/json")
w.WriteHeader(http.StatusOK)
_, _ = w.Write([]byte(`{"ok":true}`))
```

Body written via a literal `fmt.Fprintf` template, **not** `json.Marshal` — keeps the `{"ok":true}` byte-identity pin (`http_test.go:103`) trivially stable. 503 paths (`database unavailable` / `storage unavailable` / `runtime dependency unavailable`) unchanged; group `Ready()` stays AND (a member hard-error still 503s).

**H2 (carried from v1 design, still required):** `repo.Ping(req.Context())` (`:56-58`) gains the same `readyzProbeTimeout` bound as the storage probe:

```go
pingCtx, pingCancel := context.WithTimeout(req.Context(), readyzProbeTimeout)
defer pingCancel()
if err := repo.Ping(pingCtx); err != nil { … 503 "database unavailable" … }
```

Without it, the ≤ 6 s worst-case latency claim (ping 2 s + storage probe 2 s + audit drain probe 2 s) is unbounded on a wedged database.

### D3 — `cmd/server/build.go`: gauge callback reads the cache, never the store (net +0)

Swap the **named callback's body** from the per-scrape store query to the zero-I/O cache getter — keep the named function `auditGovernanceBacklogAgeGaugeFn` (`:94-101`), don't inline it: the drill (`readyz_drill_test.go:273`) binds to it by name, so an inline-closure swap breaks the drill's compilation (reviewer). Keep the `if auditRuntime != nil` registration gate (`:127`):

```go
func auditGovernanceBacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64 {
	return func(ctx context.Context) int64 {
		return int64(rt.BacklogAge().Seconds()) // cache getter; 0 when healthy/unknown
	}
}
```

- Scrapes never block on the store — this is **parent-spec REQ-5 compliance** ("a scrape must never block on the store"), i.e. a requirement fix, not just freshness (the shipped callback is a per-scrape store query).
- `Degraded()` must **not** gate the value: age > 0 implies lag, and the 450 threshold sits below default maxLag 900 — a healthy-but-age-carrying reading is impossible.
- Freshness ≤ poll interval (run-loop refresh, D1) + `/readyz` probe cadence, independent of scrape traffic.

### D4 — No other surfaces

No config env surface (no `.env.example`, validation, or docs change — `docs/configuration.md:274` wording stays a flagged follow-up), no DB migration, no OpenAPI change, no new metric, alert rule unchanged (shipped), relay counters unchanged, `internal/api/rest` untouched, billing-runtime readiness untouched.

---

## 3. API changes (complete list)

| Symbol | Change | Breakage surface |
|---|---|---|
| `auditgovernance.Runtime.BacklogAge(ctx)` | **Renamed** → `PendingBacklogAge(ctx)` (G1) | **9 call sites** (E12), all internal to the module (binary module, no external consumers): 2 production (`runtime.go:174`, `build.go:100` — the latter deleted by D3 in a later step) + 7 test (`runtime_test.go:449/:492`, `cumulative_window_test.go:190`, `readyz_drill_test.go:214/:276/:301/:319`); all mechanical |
| `auditgovernance.Runtime.Degraded() bool` | **New** — zero-I/O cache getter | none (additive) |
| `auditgovernance.Runtime.BacklogAge() time.Duration` | **New** — zero-I/O cache getter (name freed by rename) | none (additive) |
| `auditgovernance.storeProbeTimeout` | **New** — package const, 2 s | none |
| `auditgovernance.Runtime.Ready(ctx)` | **Behavior** — probe timeout/cancel → nil + degraded (was: hang until caller ctx); lag/drain/genuine-error branches unchanged | `/readyz` response surface for the wedged-store case (intended); healthy body byte-identical |
| `readinessGroup.Degraded()/BacklogAge()` | **New** — OR/max composition | none (additive); `main.go:157` untouched |
| `cmd/server.readyzHandler` | **Behavior** — degraded extra → 200 + `{"ok":true,"degraded":true,"backlog_age_seconds":N}`; **H2:** `repo.Ping` bounded by `readyzProbeTimeout` | additive; healthy body byte-identical (`http_test.go:103` stays green); ping 503 path unchanged, now bounded |
| `cmd/server/build.go` gauge callback | **Swap** — per-scrape store query → cache getter | observable: gauge value reflects last probe (freshness ≤ poll interval) instead of a live query per scrape; identical when the probe is current |
| `runtime.go run()` loop | **Behavior** — one `probeAndRecord(context.Background())` per poll cycle | none (internal) |
| `deploy/helm/.../deployment.yaml` | **H1:** readinessProbe gains `timeoutSeconds: 10` | rollout artifact; k8s probe window change (1 s default → 10 s) |
| `Makefile test-race-meta` | **H3:** scope gains `./internal/auditgovernance/` | CI runtime +~78 s (E15) inside the unchanged 300 s timeout |

**Wire-format surface (the only observable API):** `/readyz` gains exactly one new response form — HTTP 200 `{"ok":true,"degraded":true,"backlog_age_seconds":N}`. The healthy 200 body `{"ok":true}` and all 503 bodies are byte-identical to today.

---

## 4. Compatibility constraints

1. **`/readyz` healthy body is a wire contract**: byte-identical `{"ok":true}` — pinned by `TestReadyzErrNotFoundIsReady` (`http_test.go:103`). The degraded body is the only new wire form, still HTTP 200 — consumers keyed on status code (k8s `readinessProbe httpGet` in `deployment.yaml:83-88`, LB checks) keep the node in rotation, which is the D1 point. **H1 is mandatory for this claim to be true end-to-end**: the chart sets no `timeoutSeconds`, so k8s applies its 1 s default and cancels every degraded-path response (≥ 2 s) before it is written → pod NotReady after 3 failures → LB eviction. `timeoutSeconds: 10` (worst case = ping 2 s + storage 2 s + audit probe 2 s = 6 s < 10 s), pinned by T8.
2. **503s preserved where they mean something**: repo ping failure, storage probe failure, drain-in-progress, and genuine (non-context) audit-governance store errors still 503 — the audit branches get their first dedicated pins (T1c; previously zero coverage, G4). Only the two D1 conditions (lag > maxLag, probe timeout/cancel) stop 503ing.
3. **Error strings unchanged** (`audit governance drain lookup failed`, `audit governance binding drain is in progress`, `audit governance backlog lookup failed`, `runtime dependency unavailable`, `storage unavailable`, `database unavailable`).
4. **Existing tests stay green as-is, except seven mechanical renames across three test files** (`runtime_test.go:449/:492`, `cumulative_window_test.go:190`, `readyz_drill_test.go:214/:276/:301/:319`) **and two intentional drill updates** in `readyz_drill_test.go` (D2: `TestReadyzBacklogLagDegradesNot503` gains the marker body; D3: `TestReadyzDeadLetteredBacklog200AndGaugeZero` gains cache priming — §7). `TestRuntimeReadyDegradesOnBacklogLag` (`:415-466`) and `TestRuntimeBacklogAgeZeroWhenNoPending` (`:471-497`) keep semantics verbatim; `runtime_test.go` **stays 498 lines** — new tests go to `runtime_ready_test.go` (500-line gate).
5. **Single-registration rule** for the OTel gauge: the test binary registers `audit_governance.backlog_age_seconds` exactly once (T4 pattern) — OTel rejects duplicate instruments on the same meter.
6. **No config migration**: no env change, no `.env.example` change, no validation change; maxLag 900 / alert 450 literal relationship untouched (non-default-maxLag drift is the known spec D5 limitation).
7. **`degradedChecker` is additive**: `billing.Runtime` (no `Degraded()`) contributes false/0 through the group — no behavior change to billing readiness; empty group → `Degraded()` false, `BacklogAge()` 0, 200 `{"ok":true}`.
8. **Rename is the only breaking change**, internal-package only, lands as its own green step (§6 step 1).

---

## 5. Failure modes

| # | Failure | Behavior after this design | Operative invariant |
|---|---|---|---|
| F1 | Relay store wedges (probe hangs) | `Ready()` bounded at 2 s per probe → nil + degraded (age unknown → 0); `/readyz` 200 `"degraded":true`; no 503, no restart loop; **no LB eviction only once H1 lands (F12)** | D1; `storeProbeTimeout`; T1b |
| F2 | Sink down, backlog grows > maxLag | `Ready()` nil + warn; gauge rises; alert `AuditGovernanceBacklogDegraded` fires after `> 450` for 10 m; `/readyz` 200 degraded | shipped S1/S3/S4 preserved |
| F3 | Genuine store error (non-context) | Hard error → 503 (fail-closed) — unchanged; both branches pinned by **T1c** (was: zero coverage — a context/genuine conflation passed every test) | REQ-1; T1c |
| F4 | Binding drain in progress | 503 — unchanged | REQ-1 drain branch; pinned by `TestReadyzDrainStill503` (`readyz_drill_test.go:240-259`, the negative control that catches a bug skipping `extra` entirely — previously uncited) + the drain branch inside `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:466`) |
| F5 | No /readyz traffic | Gauge still refreshed by the run loop per poll cycle (1 s default) — no stale-forever value; **proven by T6 (zero `Ready()` calls)** | G3; T6 |
| F6 | Probe timeout races the run loop | `degradedMu` single-acquisition writes; last writer wins; both write degraded semantics — no torn state | H0; T7 under `-race` |
| F7 | Non-default maxLag | Alert threshold drift (450 literal) — degraded condition and alert decouple; known limitation, documented | spec D5 |
| F8 | Gauge re-registration in tests | T4 single-shot registration — OTel rejects a duplicate instrument on the same meter | compat #5 |
| F9 | Timing flake in T1b/T2 | Blocking stubs (response cannot precede the 2 s deadline → deterministic lower bound), WAL second-writer backdating (no sleeps), interval assertions only | spec §6 |
| F10 | Alert/metric name drift | T3 YAML-parses the rule and pins expr ↔ gauge name both ways; forbids other `audit_governance_*` names in exprs | T3 |
| F11 | **Wedge → alert-silent degraded** — store wedges from healthy baseline: `degraded=true`, age unknown → 0, so the `> 450` alert can never fire for F1 | `/readyz` 200 `"degraded":true` is the wedge signal (T1b/T2); gauge 0 is spec-pinned (REQ-3 "age unknown → 0"); accepted, amendment (retain last known age) flagged, not adopted | spec REQ-3 |
| F12 | **k8s readinessProbe default `timeoutSeconds: 1` < degraded-path latency** — chart sets none; k8s cancels the 200-degraded body, pod NotReady after 3 failures → LB eviction: F1's "no LB eviction" is **false as shipped** | H1: `timeoutSeconds: 10` + T8 string pin; H2: bound `repo.Ping` + blocking-ping test; worst case = 6 s < 10 s | compat #1; T8 |
| F13 | Stale gauge with zero `/readyz` traffic — a `Ready()`-only cache freezes at the last probe value | run-loop `probeAndRecord` per poll cycle (G3), freshness ≤ poll interval (1 s default, `config_audit_governance.go:61`); **proven by T6** | G3; T6 |
| F14 | Partial group-member outage — degraded + healthy + non-implementer members; empty group; one member hard-errors | group `Degraded()` OR / `BacklogAge()` max over implementing members; non-implementers contribute false/0; group `Ready()` stays AND — a member hard-error still 503s; pinned by T2-add | G2; T2-add |
| F15 | Gauge callback / run loop / `Ready()` race — torn (degraded, age) pairs or data races | `degradedMu` single-acquisition writes, getters under RLock — valid pairs only; discipline provable **only under `-race`** → T7 + **mandatory** H3 scope extension (CI-enforced, not convention-only) | H0; T7; H3 |
| F16 | Probe timeout clobbers a known lag reading — gauge 460 → 0 mid-window restarts the alert's `for: 10m` accumulation; intermittent 460→0→460 cycles can starve the alert | Follows F11's decision (0 on timeout, spec-pinned); next lag/healthy probe re-records; the F11 amendment closes this too | spec REQ-3 |
| F17 | Run-loop cadence under wedge — `probeAndRecord` serialized in `run()` blocks the loop ≤ 2 s per cycle | Bounded by `storeProbeTimeout`; the loop's own store calls are already bounded by `httpTimeout` (5 s default) — no new order of magnitude; probe errors log and skip recording, loop never stops on probe failure | D1; pinned by `TestRuntimeRunLoopSurvivesWedgedStore` (T6 wedge variant) |
| F18 | Caller-canceled probe marks degraded — k8s/LB cancels at 1 s → `context.Canceled` → REQ-2 pins Canceled ⇒ degraded | Recorded degraded with age 0; the run-loop refresh (Background ctx, T6) self-corrects within one poll interval — bounded blip, invisible to the already-gone caller | REQ-2; G3; pinned by T1c (c3, pre-canceled ctx) |

---

## 6. Migration steps

No DB migration, no config migration, no wire-format break (healthy `/readyz` body identical; degraded body is additive). The "migration" is code + rollout:

1. **Rename** `Runtime.BacklogAge(ctx)` → `PendingBacklogAge(ctx)` — **9 call sites** (E12): `runtime.go:174`, `build.go:100`, `runtime_test.go:449/:492`, `cumulative_window_test.go:190`, `readyz_drill_test.go:214/:276/:301/:319`. All mechanical, including the 7 test sites; `build.go:100` is renamed here and only *deleted* in step 4 (D3's body swap — the callback keeps calling `PendingBacklogAge` until then). `make check` green at this point — **pure rename, zero behavior change across all 9 sites**. (Commit boundary if committing separately.)
2. **`runtime.go`**: add `storeProbeTimeout`; `degradedMu`/`degraded`/`backlogAge` fields; `probeAndRecord` + `isProbeCtxError` + `recordDegraded`; zero-I/O getters `Degraded()`/`BacklogAge()`; `Ready()` → delegate to `probeAndRecord`; run-loop refresh after `cleanupDelivered()`. Keep `TestRuntimeReadyDegradesOnBacklogLag` + `TestRuntimeBacklogAgeZeroWhenNoPending` green verbatim.
3. **`cmd/server/http.go`**: `degradedChecker` interface; `readinessGroup` `Degraded()`/`BacklogAge()`; `readyzHandler` degraded branch (healthy body untouched); **H2** ping bound.
4. **`cmd/server/build.go`**: swap gauge callback to the cache getter (drop the store query — scrapes become I/O-free).
5. **H1 (`deploy/helm/aero-vault/templates/deployment.yaml`)**: add `timeoutSeconds: 10` to the readinessProbe block (`:83-88`). Without this, the degraded 200 is canceled by k8s's 1 s default and the node is evicted — the D1 claim is unobservable in production.
6. **Tests**: new `internal/auditgovernance/runtime_ready_test.go` (~250-270 lines — T1b/T1c(+c3)/T5/T6(+F17 wedge variant)/T7; the `readyz_drill_test.go` analog is 373, so the earlier ~200 estimate was optimistic; still < 500); update `cmd/server/readyz_drill_test.go` (373 → ~385: `TestReadyzBacklogLagDegradesNot503` to the marker body, `TestReadyzDeadLetteredBacklog200AndGaugeZero` cache priming with the named fn retained, T3's unique asserts folded into `TestAlertsYMLAuditGovernanceExprParity`); extend `cmd/server/http_test.go` (129 → ~230: T2 + T2-add + T8); extend `internal/telemetry/metrics_test.go` (189 → ~230: **T4 only** — T3 no longer lands here).
7. **H3 (`Makefile:116-123`)**: `test-race-meta` gains `./internal/auditgovernance/` (one line + comment update). T7 then runs under `-race` in every `make check`. Feasibility re-verified live on this checkout: **78.7 s** (E15 — spec's 42.7 s is stale but both fit the unchanged 300 s budget with 3.8× margin).
8. **Gate**: `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` / cli-check), zero network/Docker, SQLite + local FS + `httptest`. **No dependency change**: T3 is folded into the existing stdlib parity test (`os.ReadFile` + `strings` + `strconv`, already imported); `go.yaml.in/yaml/v2` stays `// indirect` (E14, I6).
9. **Rollout observation**: confirm `/readyz` healthy body unchanged; induce a sink outage and observe (a) 200 + degraded marker within the k8s probe window (verify the pod stays Ready), (b) gauge climb, (c) alert at 450 s/10 m, (d) recovery restores `{"ok":true}` and gauge 0. Operational note: the gauge no longer queries the store per scrape — it reflects the last probe (freshness ≤ poll interval + probe cadence). Wedged-*store* (not sink) shows `degraded:true` with gauge 0 — the `/readyz` marker is the wedge signal (F11).
10. **Follow-ups (explicitly out of scope, flagged):** `docs/configuration.md:274` wording ("Oldest undelivered outbox age that `/readyz` permits" — lag no longer 503s); startup warning for non-default maxLag vs the 450 literal.

---

## 7. Testable acceptance mapping

| Acceptance (spec §5) | Test | File / mechanics | Assertion surface |
|---|---|---|---|
| **T1b** hanging-store → nil + degraded + age-0, elapsed ∈ [1 s, 5 s] | `TestRuntimeReadyDegradedSentinel` (hanging-store case) | `internal/auditgovernance/runtime_ready_test.go` (new; `runtime_test.go` is 498/500) | store fake whose probe methods block on `<-ctx.Done()` and return `ctx.Err()` (partial-stub idiom: embed the real repo so `New()`'s `ApplyAuditGovernanceBindings` works; loopback publisher base URL so `New()` makes no network calls; cf. `blockingStatStorage`, `http_test.go:36-41`) → `Ready(context.Background())` returns nil; elapsed ∈ [1 s, 5 s] (blocking stub ⇒ response cannot precede the 2 s `storeProbeTimeout`; upper bound proves boundedness — the proven `TestReadyzStorageProbeTimeout` idiom); `Degraded()==true`; cache `BacklogAge()==0` (age unknown) |
| **T1c** genuine non-context store error → `Ready()==error` ∧ `Degraded()==false` | `TestRuntimeReadyFailClosedOnGenuineStoreError` (c1/c2/c3 subtests) | same file; `errProbeStore` partial stub (`http_test.go:46-52` idiom) | (c1) `HasPendingDrainingAuditGovernance` returns an immediate non-context error → `Ready(background)` returns `"audit governance drain lookup failed"` ∧ `Degraded()==false`; (c2) drain probe nil, `OldestPendingAuditGovernance` immediate error → `"audit governance backlog lookup failed"` ∧ `Degraded()==false`. Errors return immediately (no ctx involvement, no timing dependence) — pins the preserved fail-closed branches (G4: previously zero coverage) and the non-context side of the `isProbeCtxError` fork (too-broad match fails c, too-narrow fails b). **(c3 — F18 pin): pre-canceled ctx → `Ready(alreadyCanceledCtx)==nil` ∧ `Degraded()==true` ∧ cache `BacklogAge()==0`** — returns immediately (already-canceled, no timing dependence); covers the `context.Canceled` side of the fork that T1b (DeadlineExceeded via the 2 s store timeout) and c1/c2 (non-context) leave open — REQ-2 |
| **T2** degraded fake extra → 200 exact body; healthy byte-identical | `TestReadyzDegradedExtraReturns200WithMarker` / `TestReadyzHealthyExtraReturns200Unchanged` / `TestReadyzAuditGovernanceDegradedDrill` | `cmd/server/http_test.go` | fake `{Ready→nil, Degraded→true, BacklogAge→123s}` with `stubReadyRepo`/`notFoundStatStorage` → status 200, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`; same fake `Degraded→false` → 200, body byte-identical `{"ok":true}` (`http_test.go:103` idiom); **T2-add** `TestReadyzGroupDegradedComposition`/`TestReadyzGroupReadyFailPropagates`/`TestReadyzGroupEmpty`: real `readinessGroup` with degraded fake + healthy fake + billing-like non-implementer → group `Degraded()` true / `BacklogAge()` max (123); member `Ready` error → 503 `"runtime dependency unavailable"`; empty group → 200 `{"ok":true}`; **drill**: real `auditgovernance.New` + hanging store through `readyzHandler` → 200 never 503, body contains `"degraded":true`, elapsed ∈ [1 s, 5 s] |
| **T5** terminal row → `OldestPending` ok==false ∧ `BacklogAge()==0` ∧ `Ready()==nil` | `TestRuntimeBacklogAgeZeroWhenAllTerminal` | same file | seed one fact via `InsertEventWithGovernance`; `ClaimAuditGovernance(ctx,"t","tok",1,1,time.Minute)` + `FailAuditGovernance(ctx,id,"t","tok","conflict:true")` (lease-fenced public API, `claim.go:20`/`:182`) → `OldestPendingAuditGovernance` ok==false (re-pin of `audit_governance_test.go:542` at runtime level) ∧ `PendingBacklogAge(ctx)` ok==false (gauge 0; cache `BacklogAge()==0` after a probe) ∧ `Ready()==nil` ∧ `Degraded()==false` — a dead-lettered backlog never blocks readiness |
| **T6** run-loop refresh with zero `Ready()` calls | `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` + **F17 wedge variant** `TestRuntimeRunLoopSurvivesWedgedStore` | same file | real SQLite store; seed one fact, backdate `created_at_ns` **−16 s** via `UPDATE audit_governance_outbox SET created_at_ns=? WHERE id=?` on a second WAL connection (no sleeps); `Start(ctx)` with poll 10 ms (`runtimeConfig` harness, `runtime_test.go:39-46`); **never call `Ready()`**; deadline-poll (≤ 3 s) until `Degraded()==true`; assert cache `BacklogAge() > 4*time.Second`; `Close()`. Proves the run-loop feed (G3), not just claims it. **Backdate hardened −8 → −16 s** (reviewer): the pass path flips on loop iteration 1 (age 8 s > maxLag 4 s) but the failure path is a deterministic false-negative (a first probe delayed > 3 s deadline, or > 4 s decay, fails forever — no retry); −16 s gives **12 s absolute slack** and the ≈ 2× margin phrasing is dropped. **Wedge variant (F17, ~20 lines):** hanging store → `Degraded()==true` within the deadline (the run loop probes even with zero `Ready()` calls), then restore a healthy store → `Degraded()==false` within the deadline — proves the loop keeps running through wedged probes and recovers ("loop never stops on probe failure"); `Close()` returns non-blocking |
| **T3** alert expr pinned — **folded into the shipped stdlib parity test (I6)** | `TestAlertsYMLAuditGovernanceExprParity` (**extended in place**, `readyz_drill_test.go:344-373`; no new test, no YAML dependency promotion) | `cmd/server/readyz_drill_test.go` | already pins: rule `AuditGovernanceBacklogDegraded` exists; expr name == exported gauge name (dots→underscores); **threshold derived via `config.Load()`** (`MaxLagSeconds/2` = 450 at the shipped default — strictly stronger than T3's literal 450: an operator maxLag override can't silently drift it); `severity: warning`; the "/readyz stays 200" description. **T3's unique adds (2–3 lines; stdlib `os.ReadFile`/`strings`/`strconv` already imported):** the rule block also contains `for: 10m`; and **exactly one `expr: audit_governance_` occurrence file-wide** (no other `audit_governance_*` name in any expr — today only the rule at `alerts.yml:182`, plus the comment at `:177`, which the expr-scoped check ignores). Prometheus firing itself stays untestable in `go test` — "shipped" is the honest status |
| **T8** helm `timeoutSeconds: 10` (H1) + **no `failureThreshold`** + blocking-Ping bound (H2) | `TestHelmReadinessProbeTimeoutSeconds` / `TestReadyzPingProbeTimeout` | `cmd/server/http_test.go` | string-pin the Go-templated helm file (not parseable YAML): the readinessProbe block (`deployment.yaml:83-88`) must contain `timeoutSeconds: 10` **and must not contain a `failureThreshold` key** — a future `failureThreshold: 1` would silently re-enable fast eviction (NotReady after one failure ≈ 10 s) and defeat H1, so the pin forbids it (drift either way fails; worst case = ping 2 s + storage 2 s + audit probe 2 s = 6 s < 10 s). H2: `stubReadyRepo` variant whose `Ping` blocks on `<-ctx.Done()` → 503 `"database unavailable"`, elapsed ∈ [1 s, 5 s] (mirror of `TestReadyzStorageProbeTimeout`) |
| **T7** `-race` via `test-race-meta` (H3) | `TestRuntimeDegradedCacheConcurrentAccess` | same file as T1b | N goroutines × `Ready()`/`probeAndRecord`/`Degraded()`/`BacklogAge()` against a scripted store (healthy→lag→hang); assert only valid (degraded, age) pairs. **CI-enforced**: `test-race-meta` (`Makefile:116-123`) gains `./internal/auditgovernance/` so T7 runs under `-race` in every `make check` — the `degradedMu` contract is provable only under `-race`; feasibility verified live (E15: 78.7 s on this checkout, within the unchanged 300 s budget) |
| — (T4, design auxiliary) gauge surface | `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` | `internal/telemetry/metrics_test.go` | registers the gauge exactly once (fixed callback 450) → scrape body line-exact via `scrapeValue`; re-scrape after callback change → updated value; single-shot registration mirrors `TestObservableGauges_SurfaceInScrape` (`:168`) |
| — (drill inventory, reviewer) `readyz_drill_test.go` D2/D3 interactions | `TestReadyzBacklogLagDegradesNot503` (**updated**) / `TestReadyzDeadLetteredBacklog200AndGaugeZero` (**updated**) / `TestReadyzDrainStill503` (**preserved**) | `cmd/server/readyz_drill_test.go` (373 lines, 7 tests — the design's own E2/E7 evidence cites it but the mapping and preservation pins previously omitted it) | **`TestReadyzBacklogLagDegradesNot503` (`:207-237`):** seeds 8 s lag (> maxLag 4 s) and asserts the v1-era exact body `{"ok":true}` "no degraded marker" — **fails as written under D2**; update to the marker body, exact `{"ok":true,"degraded":true,"backlog_age_seconds":8}` (the 8 s backdate can only grow; the whole backdate→probe path is sub-second — the test's own < 1 s elapsed assertions already require it, so the truncating `Seconds()` lands on 8 deterministically; assert the exact body, with the parsed age floor ≥ 8 as the safety net) and re-scope the header comment ("lag > maxLag now carries the marker; still 200 — degraded is a payload, never a 503"). **`TestReadyzDeadLetteredBacklog200AndGaugeZero` (`:270-342`):** two D3 conflicts — (a) binds `auditGovernanceBacklogAgeGaugeFn(rt)` at `:273`, so an inline-closure swap breaks compilation (D3 keeps the named fn, body swapped); (b) the phase-2 `gauge(ctx) > 0` assertion fails deterministically under the zero-initialized cache (relay never starts in the drill; the cache only changes via `probeAndRecord`) — the drill **primes the cache** with `rt.Ready(ctx)` after the phase-2 backdate, before the gauge assertion (then `serveReadyz` re-probes and re-records; body stays `{"ok":true}` — age 2 s < maxLag 4 s). **`TestReadyzDrainStill503` (`:240-259`):** preserved verbatim — 503 + `runtime dependency unavailable` + < 1 s; the negative control that catches a bug skipping `extra` entirely (its own comment says so). `TestReadyzExtraProbeTimeout`/`TestReadyzImmediateExtraError`/`TestReadyzHealthyExtra200` (`:158-206`): fake-only, unaffected (extra-level 503/200 paths and the healthy byte-identical body are unchanged) — stay green verbatim |

**Preservation pins (must stay green, not re-written):** `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:415`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`:473`, modulo the `PendingBacklogAge` rename at `:492`), `TestReadyzStorageProbeTimeout`/`TestReadyzErrNotFoundIsReady`/`TestReadyzImmediateStorageError` (`http_test.go:69-129`, nil-extra), **`TestReadyzDrainStill503` (`readyz_drill_test.go:240`) and `TestReadyzExtraProbeTimeout`/`TestReadyzImmediateExtraError`/`TestReadyzHealthyExtra200` (`:158-206`) — preserved verbatim (F4's uncited pins, now cited)**, `TestAuditGovernanceMetrics_SurfaceInScrape` (`metrics_test.go:106`), `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`), `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`, terminal pin at `:542`). New pins on preserved behavior: T1c (fail-closed genuine-error branches — an addition, not a re-write) and the T6 wedge variant (F17). `TestAlertsYMLAuditGovernanceExprParity` is **extended in place** (T3 fold), not re-written; the two D2/D3-affected drill tests are updated exactly as specified in the drill row above.

---

## 8. Risks & gates

- **Stale-citation hazard (confirmed live):** the direction's "unbounded `extra.Ready` at :66" (E4) and "alerts.yml:163" (E9) are stale vs. the working tree — the sibling probe-timeout work and uncommitted alert edits shifted them. This design is written against **current** lines; implementers must re-grep, not trust the analysis line numbers. E15's 42.7 s is likewise stale (78.7 s on this checkout) — the 300 s budget holds either way.
- **Overload-collision (G1)** is the only breaking change: an internal-package rename with **9 mechanical call sites** (E12 — 2 production + 7 test; the spec's 4 undercounted sibling tests); rename-only step lands green before behavior work (§6 step 1).
- **Timing flakes**: T1b/T8 use blocking stubs (deterministic lower bound: response cannot precede the 2 s probe deadline) with interval assertions only; **T6** (T5 has zero timing — terminal via the Claim/Fail public API) uses WAL second-writer backdating, no sleeps; the backdate is **−16 s** (hardened from −8 s): the failure mode is a deterministic false-negative on a first probe delayed > deadline, so the defense is the **12 s absolute slack** before age decays to maxLag 4 s — the loose "2× margin" phrasing is dropped.
- **Line gates** (re-verified on this checkout — all current sizes confirmed): `runtime_test.go` **498** → 498 (renames only); `readyz_drill_test.go` **373** → ~385 (two drill updates + T3 fold); `runtime_ready_test.go` new ~250-270 (the ~200 estimate was optimistic; the `readyz_drill_test.go` analog is 373 — drift, not gate risk); `runtime.go` 231 → ~276; `http.go` 192 → ~225 (incl. H2 ping bound); `build.go` 192 → net +0; `metrics.go` 454 unchanged; `http_test.go` 129 → ~230; `metrics_test.go` 189 → ~230 (**T4 only** — T3 no longer lands here) — all under the 500-line single-file gate.
- **Duplicated-instrument rejection**: T4 registers `audit_governance.backlog_age_seconds` exactly once in the test binary (OTel rejects duplicates on the same meter), mirroring `TestObservableGauges_SurfaceInScrape` (`:168`).
- **`-race` budget**: verified live at **78.7 s** for the package (E15 re-run; spec's 42.7 s was machine/worktree-dependent) — the new T7 workload is deadline-bounded by the same blocking stubs; the unchanged 300 s `test-race-meta` timeout holds with 3.8× margin.
- **F12 (carried)**: the shipped helm readinessProbe has no `timeoutSeconds` — k8s's 1 s default cancels every degraded-path response (≥ 2 s) and evicts the node, falsifying the D1 "degrade, never evict" claim as shipped. Mitigated by H1 (`timeoutSeconds: 10`) + T8, H2 (ping bound) — both are in this delta's scope.
- **F11/F16 (accepted)**: age 0 on probe timeout is spec-pinned; the wedge case is alert-silent by design (the `/readyz` marker is the wedge signal); amendment option (retain last known age) flagged, not adopted.
- **Gate**: `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` / cli-check), zero network/Docker, SQLite + local FS + `httptest`. **No new dependency** — T3 folds into the existing stdlib parity test (E14, I6: `go.yaml.in/yaml/v2` stays `// indirect`).

*Verification basis: every evidence citation re-read on this checkout (HEAD `15763e2` + uncommitted worktree); line numbers reflect the working tree as read during this design's production; `go test -race -count=1 -timeout 300s ./internal/auditgovernance/` re-run live on 2026-08-08 → 78.658 s (E15).*
