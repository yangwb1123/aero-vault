# Design — `internal/api/rest` (analysis label): Ready() decoupled to degraded + 450 s alert, relay metrics attempted/delivered/failed/dead/oldest-age — delta design (probe timeout, degraded sentinel, `/readyz` payload, three pins)

**Module:** `internal/auditgovernance` + `cmd/server` + `internal/telemetry` + `deploy/prometheus` (the `internal/api/rest` label is analysis traceability only — no handler changes; see §1 of the spec)
**Spec:** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.spec.md` (REQ-1..6, D1..D5, AC-1..4) · **HEAD:** `15763e2` + uncommitted worktree · **Date:** 2026-08-08
**Scope lock:** delta only — REQ-2 (probe timeout), REQ-3 (degraded sentinel), REQ-4 (`/readyz` degraded payload), REQ-6 (T1–T5 pins); REQ-1/REQ-5 are preservation (pin, don't touch). No config surface, no migration, no new metric, no `internal/api/rest` change. **Hardening-review exceptions (documented, see §4/F12):** H1 adds `timeoutSeconds: 10` to the helm readinessProbe and H2 bounds `repo.Ping` — both required to make the design's own F1/compat-#1 claims ("no LB eviction", "keep the node in rotation") true as shipped; no new config env surface. **H3 (gate, §6 step 6):** `test-race-meta` (`Makefile:116-120`, inside `make check` at `:123`) gains `./internal/auditgovernance/` — one line, matching the metadata-atomicity precedent — so AC-1 aux (F15), whose mutex contract is provable only under `-race`, is enforced by CI rather than a manual acceptance step.

---

## 1. Verification register (evidence re-checked, not trusted)

All evidence claims were re-read on this checkout. **Every claim in the evidence verified TRUE** — no stale citation found; three *spec-level* design gaps were found in the verification and are resolved in §2 (G1, G2, G3).

| Evidence claim | Re-verified location (working tree, HEAD `15763e2` + uncommitted) | Verdict |
|---|---|---|
| Commit `15763e2` landed; spec is preservation + delta | `git log --oneline -1` → `15763e2 feat(gov): B3-2 Ready decoupling — …`; worktree dirty (109 files vs HEAD, incl. sibling readyz-probe-timeout work) | ✅ **exact** |
| `Ready()` maxLag flip → nil + warn; drain/store errors stay hard-fail | `internal/auditgovernance/runtime.go:162-182`; flip `:178-181` (`if ok && age > r.maxLag { logger.Warn("audit governance relay degraded", …) }` then `return nil`); drain hard-error `:167-169`; store hard-errors `:164-166` (`drain lookup failed`), `:175-177` (`backlog lookup failed`) | ✅ **exact** |
| `BacklogAge()` accessor at `runtime.go:151-159` | Same file: `BacklogAge(ctx) (time.Duration, bool, error)` `:151-159` — **store-querying** method | ✅ **exact** (naming conflict with REQ-3, see G1) |
| `OldestPending` excludes `failed_at_ns=0` at `claim.go:195` | `internal/repository/audit_governance_claim.go:188-196` (`OldestPendingAuditGovernance`), predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` at `:195`; `HasPendingDrainingAuditGovernance` `:202-208` | ✅ **exact** |
| Gauge `audit_governance.backlog_age_seconds` at `metrics.go:354-365`, wired `build.go:113-120` | `internal/telemetry/metrics.go:352-365` (`RegisterAuditGovernanceBacklogAgeGauge`, observable gauge); `cmd/server/build.go:113-120` (`if auditRuntime != nil` → callback calls `auditRuntime.BacklogAge(ctx)` — **a per-scrape store query today**; swap per G3/§2 D3) | ✅ **exact** |
| Alert `AuditGovernanceBacklogDegraded` at `alerts.yml:162-169` | `deploy/prometheus/alerts.yml:156-169`: group `aero-vault-audit-governance` `:156`, comment `:158`, rule `:162-169` — `expr: audit_governance_backlog_age_seconds > 450`, `for: 10m`, `severity: warning`; case-sensitive `grep -E 'audit.*(lag|oldest|dead)'` hits only comment `:158` + description `:169` — **grep alone cannot pin the expr** (T3 must YAML-parse) | ✅ **exact** |
| Relay counters at `metrics.go:103-106`; increments at `relay.go:83/:112/:121/:135` | `internal/telemetry/metrics.go:103-106` (attempted/delivered/failed/dead, `_total`); `internal/auditgovernance/relay.go:83` (attempted, `deliverFact` entry), `:112` (delivered, only after `CompleteAuditGovernance` nil), `:121` (dead, `failFact` entry), `:135` (failed, `retryFact` entry) | ✅ **exact** |
| No `Degraded()` / `BacklogAge()` cache getter; `/readyz` always `{"ok":true}` | `grep -rn 'Degraded()' internal/auditgovernance/ cmd/server/` → zero (only substring hits on AI `DegradedMode`); `cmd/server/http.go:71-73` writes `{"ok":true}` unconditionally; `extra.Ready(req.Context())` `:66` on the **unbounded caller ctx**; `readyzProbeTimeout = 2s` `:34-38` wraps **only** the storage probe `:59-61` | ✅ **exact** |
| `runtime_test.go` at 498/500 lines → new-file mandate | `wc -l internal/auditgovernance/runtime_test.go` → **498**; harness `runtimeConfig` `:39-46` (maxLag 4 s, poll 10 ms) | ✅ **exact** |
| `TestRuntimeReadyDegradesOnBacklogLag` `:415`; `TestRuntimeBacklogAgeZeroWhenNoPending` `:473` (empty store only) | `runtime_test.go:415-466` (sleep-based crossing of 4 s maxLag — the T1 backdate technique replaces this idiom); `:471-497` (empty store only — **failed-row case unpinned** = S11) | ✅ **exact** |
| Repo-level T-3 pin at `audit_governance_test.go:442` | `internal/repository/audit_governance_test.go:419-449`: `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`; `OldestPending ok==false` at `:442`; fenced `FailAuditGovernance` at `:436` | ✅ **exact** |
| `http_test.go` 129 lines, three nil-extra readyz tests | `cmd/server/http_test.go:69-88` (probe-timeout idiom: elapsed ∈ [1 s, 5 s]), `:93-108` (body assert `{"ok":true}` at `:103`), `:110-129` (immediate error); stub idiom `:27-56` | ✅ **exact** |
| `metrics_test.go` scrape harness | `internal/telemetry/metrics_test.go`: `scrapeValue` `:61-75` (line-exact), `TestAuditGovernanceMetrics_SurfaceInScrape` `:82-108` (four counters, value 1), `TestObservableGauges_SurfaceInScrape` `:114` (single-shot registration pattern) | ✅ **exact** |
| Config: maxLag default 900, validation bounds | `internal/config/config_audit_governance.go:66` (`getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)`); `:241` (`> ClaimTTLSeconds`), `:251` (`<= 604_800`) → 450 = 900 × 0.5 | ✅ **exact** |
| `extra` wiring = `runtimeReadiness(billingRuntime, auditRuntime)` | `cmd/server/main.go:157` → `runtimeReadiness` (`cmd/server/audit_governance.go:51-64`) returns a **`readinessGroup`** (not the bare runtime) when any member exists | ✅ **exact** — composition gap, see D4 |

**Design gaps found during verification (spec-internal, resolved below):**

- **G1 — Go has no method overloading.** REQ-3's cache getter `func (r *Runtime) BacklogAge() time.Duration` collides with the shipped `BacklogAge(ctx) (time.Duration, bool, error)` (`runtime.go:151-159`). Both the evidence spec (REQ-4) and the sibling spec `cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-3 name-lock the interface method `BacklogAge() time.Duration` — so the **store-querying method must be renamed**, not the cache getter. Call-site surface: exactly 4 (`runtime.go:174`, `cmd/server/build.go:114`, `runtime_test.go:449`, `:492`).
- **G2 — `readinessGroup` composition.** `extra` in production is a `readinessGroup` (possibly wrapping `billing.Runtime`, which has no `Degraded()`). A bare type-assert `extra.(degradedChecker)` in `readyzHandler` fails against the group. Resolution: `readinessGroup` itself gains `Degraded()`/`BacklogAge()` (OR / max over members implementing `degradedChecker`) — main.go wiring stays untouched, billing contributes `false`/`0` today, and the group composes if billing ever gains a degraded tier.
- **G3 — gauge freshness.** If the cache is written only by `Ready()` (per `/readyz` hit), the gauge goes stale when /readyz is quiet. Closure: the run loop records once per poll cycle (same as sibling spec REQ-2's `probeAndRecord`), so the gauge tracks the relay's live state with freshness ≤ poll interval, independent of probe traffic.
- **G4 — fail-closed branches unpinned (hardening-review Finding C).** `Ready()`'s genuine store-error branches (`runtime.go:164-166` drain lookup, `:175-177` backlog lookup) are preserved REQ-1 behavior that REQ-2 restructures into `probeAndRecord`, yet **zero tests feed an erroring store** into `Ready()`/`readyzHandler` — a regression that conflates context errors with genuine errors (e.g., `isProbeCtxError` matching too broadly) passes T1(a)/(b) and all three shipped readyz tests (nil-extra). Closure: **T1(c)** pins both branches (`Ready()==error ∧ Degraded()==false`); the handler-level 503 side (`extra.Ready` error → 503, `http.go:66-68`) is pinned by T2-add's `TestReadyzGroupReadyFailPropagates`.

---

## 2. Design

### D1 — `internal/auditgovernance/runtime.go`: bounded probes + degraded cache (~+45 lines, 231 → ~276)

New package constant next to `BacklogAge` (comment cross-references `readyzProbeTimeout`, `cmd/server/http.go:34-38` — same rationale, same 2 s value, independent symbol; deliberately **not** derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` whose 5 s default is too slow for a readiness probe):

```go
// storeProbeTimeout bounds Ready()'s two store probes independently of
// AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS (5s default, relay HTTP bound) and
// the caller's request context: a wedged relay store must not hold /readyz
// beyond ~2s per probe. Mirrors readyzProbeTimeout (cmd/server/http.go).
const storeProbeTimeout = 2 * time.Second
```

`Runtime` gains three fields (the struct has no mutex today — `sync` is already imported):

```go
degradedMu   sync.RWMutex   // guards degraded + backlogAge
degraded     bool           // last probe result: lag > maxLag or probe timeout
backlogAge   time.Duration  // last probe age; 0 when none/unknown
```

**Rename (G1):** `BacklogAge(ctx) (time.Duration, bool, error)` → `PendingBacklogAge(ctx)` — same signature, same body (`OldestPendingAuditGovernance` query + `time.Since`). Call-site migration: `runtime.go:174` (internal), `cmd/server/build.go:114` (replaced by the cache getter, §2 D3), `runtime_test.go:449/:492` (mechanical rename, zero semantic change — the two pinned tests stay green verbatim).

**New accessors (cache getters, zero store I/O):**

```go
func (r *Runtime) Degraded() bool
func (r *Runtime) BacklogAge() time.Duration
```

**`Ready()` restructure** — both probes under `context.WithTimeout(ctx, storeProbeTimeout)`, with a shared helper (also called by the run loop, G3):

```go
func (r *Runtime) probeAndRecord(ctx context.Context) error {
    probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)
    defer cancel()
    draining, err := r.store.HasPendingDrainingAuditGovernance(probeCtx)
    if err != nil {
        if isProbeCtxError(err) { r.recordDegraded(true, 0); return nil }  // wedged store → degraded, never 503 (spec REQ-2/REQ-3: timeout ⇒ degraded=true, age unknown → 0)
        return errors.New("audit governance drain lookup failed")             // genuine error → fail-closed (unchanged)
    }
    if draining {
        return errors.New("audit governance binding drain is in progress")    // unchanged
    }
    oldest, ok, err := r.store.OldestPendingAuditGovernance(probeCtx)  // returns time.Time (claim.go:190)
    if err != nil {
        if isProbeCtxError(err) { r.recordDegraded(true, 0); return nil }
        return errors.New("audit governance backlog lookup failed")
    }
    var age time.Duration
    if ok {
        age = time.Since(oldest)
    }
    if ok && age > r.maxLag {
        r.logger.Warn("audit governance relay degraded", "backlog_age", age.String(), "max_lag", r.maxLag.String())  // unchanged warn
        r.recordDegraded(true, age)
        return nil
    }
    r.recordDegraded(false, age)  // 0 when !ok
    return nil
}
// isProbeCtxError: errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
```

> **Hardening (H0) — snippet corrected from the earlier draft, which did not compile and inverted the feature:** the store returns `time.Time` (`claim.go:190`), so `age > r.maxLag` and `time.Since(age)` were type errors, and `recordDegraded(false, 0)` on probe timeout recorded **not-degraded** — contradicting spec REQ-2/REQ-3 (`timeout ⇒ degraded=true`) and the design's own F1/T1(b) (`Degraded()==true` after a hanging probe). `recordDegraded(degraded, age)` must write **both** fields under a single `degradedMu.Lock()` acquisition (readers can then only observe valid pairs — F15); getters read under `RLock`. The timeout branch records age **0** per spec REQ-3 (age unknown) — the wedge→alert-silent consequence is documented as F11 (accepted, with a flagged amendment option).

Semantics preserved: lag → nil+warn; drain → hard error; genuine store errors → hard error; terminal rows excluded (unchanged predicate, `claim.go:195`). **New:** either probe timing out/canceled → `nil` + degraded with age unknown (0) — the D1 "degrade, never 503" read-path half. `PendingBacklogAge(ctx)` keeps its caller-ctx signature (tests and any direct callers keep full control; it is no longer on the readyz path).

**Run-loop refresh (G3):** in `run()`, after `cleanupDelivered()`, call `probeAndRecord(context.Background())` once per poll cycle (after an `r.stopping()` guard, same as the other phases; a store error in the loop records nothing and logs — it does not stop the loop). This keeps the gauge fresh with zero /readyz traffic (F13, proven by T6). Note (F17): a wedged store serializes the loop ≤ 2 s per cycle behind `storeProbeTimeout`; the loop's own store calls are already bounded by `httpTimeout` (5 s default), so no new order-of-magnitude — documented, no code change.

### D2 — `cmd/server/http.go`: degraded payload + `readinessGroup` composition

```go
type degradedChecker interface {
    Degraded() bool
    BacklogAge() time.Duration
}

func (g readinessGroup) Degraded() bool {           // OR over implementing members; false when none
    for _, c := range g {
        if dc, ok := c.(degradedChecker); ok && dc.Degraded() { return true }
    }
    return false
}
func (g readinessGroup) BacklogAge() time.Duration { // max over implementing members; 0 when none
    var max time.Duration
    for _, c := range g {
        if dc, ok := c.(degradedChecker); ok {
            if a := dc.BacklogAge(); a > max { max = a }
        }
    }
    return max
}
```

`readyzHandler` (`http.go:51-73`), after `extra.Ready` succeeds:

```go
if extra != nil {
    if err := extra.Ready(req.Context()); err != nil {   // 503 path unchanged
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

Body written as a literal `fmt.Fprintf` template, **not** `json.Marshal` — keeps the `{"ok":true}` byte-identity pin (`http_test.go:103`) trivially stable and the degraded body deterministic. Storage probe, `readyzProbeTimeout`, route registration (`:101`), `runtimeReadiness` (`audit_governance.go:51-64`), and `main.go:157` all **unchanged** — the group methods make the production wiring implement `degradedChecker` with zero main.go edits. **Exception (H2, hardening review):** `repo.Ping(req.Context())` (`http.go:56`) gains the same `readyzProbeTimeout` bound as the storage probe — without it the ≤ 6 s worst-case latency claim (F12) is unbounded on a wedged database.

### D3 — `cmd/server/build.go`: gauge callback reads the cache, never the store

Replace the per-scrape store query (`build.go:113-120`) with the zero-I/O cache getter — the gauge is the *oldest pending age* instrument (name-true, sibling spec REQ-4 name-lock), value 0 when healthy/unknown, so it can never trip `> 450` outside the degraded condition; `Degraded()` must **not** gate the value (a healthy-but-age-carrying reading is impossible: age > 0 implies lag, and the alert threshold 450 sits below default maxLag 900):

```go
if auditRuntime != nil {
    telemetry.RegisterAuditGovernanceBacklogAgeGauge(func(ctx context.Context) int64 {
        return int64(auditRuntime.BacklogAge().Seconds()) // cache getter; 0 when healthy/unknown
    })
}
```

Scrapes never touch the store; freshness ≤ poll interval (run-loop refresh, D1) independent of `/readyz` traffic. **This swap is also spec REQ-5 compliance** (`internal-api-rest-…v1.spec.md` REQ-5: "a scrape must never block on the store") — the shipped callback (`build.go:113-120`) is a per-scrape store query, so the swap is a requirement fix, not just freshness.

### D4 — No other surfaces

No config surface (no new env var, `.env.example`, validation, or docs change — `docs/configuration.md:274` wording stays a flagged follow-up), no migration, no OpenAPI change, no `internal/api/rest` change, no new metric (gauge + four counters are the full family), alert rule unchanged (already shipped). `degradedChecker` has no production members beyond the audit runtime today; `billing.Runtime.Ready` (`internal/billing/runtime.go:136`) untouched.

---

## 3. API changes (complete list)

| Symbol | Change | Breakage surface |
|---|---|---|
| `auditgovernance.Runtime.BacklogAge(ctx)` | **Renamed** → `PendingBacklogAge(ctx)` (G1, §2 D1) | 2 prod + 2 test call sites; all mechanical; internal package only (binary module, no external consumers) |
| `auditgovernance.Runtime.Degraded() bool` | **New** — cache getter, zero I/O | none (additive) |
| `auditgovernance.Runtime.BacklogAge() time.Duration` | **New** — cache getter, zero I/O | none (additive; name freed by the rename) |
| `auditgovernance.storeProbeTimeout` | **New** — package const, 2 s | none |
| `auditgovernance.Runtime.Ready(ctx)` | **Behavior** — probe-timeout/cancel → nil + degraded (was: hang until caller ctx, error → 503); lag/drain/genuine-error branches unchanged | `/readyz` response surface for the wedged-store case (intended); healthy body byte-identical |
| `readinessGroup.Degraded()/BacklogAge()` | **New** — OR/max composition | none (additive) |
| `cmd/server.readyzHandler` | **Behavior** — degraded extra → 200 + `{"ok":true,"degraded":true,"backlog_age_seconds":N}`; 503 paths unchanged | additive; healthy body byte-identical (`http_test.go:103` stays green) |
| `cmd/server/build.go` gauge callback | **Swap** — store query → cache getter | observable: gauge value now reflects last probe (freshness ≤ poll interval) instead of a live query per scrape; value identical when the probe is current |
| `runtime.go run()` loop | **Behavior** — one `probeAndRecord` per poll cycle | none (internal) |

---

## 4. Compatibility constraints

1. **`/readyz` healthy body is a wire contract**: byte-identical `{"ok":true}` — pinned by `TestReadyzErrNotFoundIsReady` (`http_test.go:103`). The degraded body is the only new wire form, and it is still HTTP 200 (consumers keyed on status code — k8s `readinessProbe httpGet` in `deploy/helm/aero-vault/templates/deployment.yaml:83-88`, LB checks — keep the node in rotation, which is the D1 point). **Hardening (H1): the chart's readinessProbe sets no `timeoutSeconds`, so k8s applies its 1 s default — every degraded-path response (≥ 2 s) is canceled before it is written, the pod is marked NotReady after 3 failures and LB-evicted, which falsifies F1's "no LB eviction" as shipped (F12).** Required delta: `timeoutSeconds: 10` on the readinessProbe (worst-case handler latency = ping 2 s + storage probe 2 s + audit drain probe 2 s = 6 s; 10 s keeps 4 s headroom), pinned by T8. **Hardening (H2): `repo.Ping(req.Context())` (`http.go:56`) is unbounded** — bound it with `readyzProbeTimeout` (same idiom as the storage probe) so the ≤ 6 s worst case is deterministic; pin with a blocking-ping test mirroring `TestReadyzStorageProbeTimeout`.
2. **503s preserved where they mean something**: repo ping failure, storage probe failure, drain-in-progress, and genuine (non-context) audit-governance store errors still 503 — the audit side now pinned by T1(c) (runtime-level) and T2-add `TestReadyzGroupReadyFailPropagates` (handler-level, `http.go:66-68`, previously zero coverage). Only the two D1 conditions (lag > maxLag, probe timeout) stop 503ing.
3. **Error strings unchanged** (`drain lookup failed`, `drain is in progress`, `backlog lookup failed`, `runtime dependency unavailable`, `storage unavailable`, `database unavailable`) — they flow only into logs / `http.Error`, and no test pins them, but changing them adds zero value.
4. **Existing tests stay green as-is, except two mechanical renames** (`runtime_test.go:449`, `:492` — `BacklogAge(` → `PendingBacklogAge(`). `TestRuntimeReadyDegradesOnBacklogLag` (`:415-466`) and `TestRuntimeBacklogAgeZeroWhenNoPending` (`:471-497`) keep their semantics verbatim; `runtime_test.go` line count **stays 498** (renames, no additions) — new tests go to `runtime_ready_test.go` (mandatory, 500-line gate).
5. **Single-registration rule** for the OTel gauge: the test binary registers `audit_governance.backlog_age_seconds` exactly once (T4), mirroring `TestObservableGauges_SurfaceInScrape` (`metrics_test.go:114`) — OTel panics on duplicate instruments.
6. **No config migration**: no env change, no `.env.example` change, no validation change; `MaxLagSeconds` default 900 / alert literal 450 relationship untouched (drift for non-default maxLag is the known limitation from spec D5 — the alert stays a literal 450; with maxLag < 900 the alert fires *before* the degraded condition, with maxLag > 900 a degraded-but-silent window exists).
7. **`degradedChecker` is additive**: `billing.Runtime` (no `Degraded()`) contributes `false`/`0` through the group — no behavior change to billing readiness.

---

## 5. Failure modes

| # | Failure | Behavior after this design | Operative invariant |
|---|---|---|---|
| F1 | Relay store wedges (probe hangs) | `Ready()` bounded at 2 s per probe → nil + degraded (age unknown → 0); `/readyz` 200 with `"degraded":true`; no 503, no restart loop; **no LB eviction only once H1 (helm probe window) lands — see F12** | D1 read-path half; `storeProbeTimeout`; H1 |
| F2 | Sink down, backlog grows > maxLag | `Ready()` nil + warn; gauge rises; alert `AuditGovernanceBacklogDegraded` fires after `> 450` for 10 m; `/readyz` 200 degraded | shipped S1/S3/S4 preserved |
| F3 | Genuine store error (non-context) | Hard error → 503 (fail-closed) — unchanged; both branches pinned by **T1(c)** (was: zero coverage — a context/genuine conflation passed every test) | REQ-1 store-error branch; T1(c) |
| F4 | Binding drain in progress | 503 — unchanged | REQ-1 drain branch |
| F5 | No /readyz traffic | Gauge still refreshed by the run loop per poll cycle (1 s default) — no stale-forever value | G3 closure |
| F6 | Probe timeout races the run loop | Cache writes are mutex-guarded; last writer wins; both write degraded semantics — no torn state | `degradedMu` |
| F7 | Non-default maxLag | Alert threshold drift (450 literal) — degraded condition and alert decouple; known limitation, documented, sibling spec's scope | spec D5 |
| F8 | Gauge re-registration in tests | T4 single-shot registration (TestMain/one test) — the OTel SDK rejects a duplicate instrument name on the same meter with an error and drops the registration (no panic — wording corrected); the rule stands: exactly one test registers `audit_governance.backlog_age_seconds` | compat #5 |
| F9 | Timing flake in T1/T2 | Blocking stubs (response cannot precede the 2 s deadline → deterministic lower bound), `UPDATE created_at_ns` backdating on a second WAL connection (no sleeps), interval assertions only | spec §6 |
| F10 | Alert/metric name drift | T3 pins the rule expr to exactly the shipped gauge name and forbids other `audit_governance_*` names; T4 pins the export name line-exact (`scrapeValue`) | spec §6 |
| F11 | **Wedge → alert-silent degraded** — store wedges from a healthy baseline: `degraded=true`, age unknown → 0, so the `> 450` alert (F2's instrument, store-alive backlog growth) can never fire for F1 | `/readyz` 200 `"degraded":true` is the F1 attention driver (T1(b)/T2); gauge 0 is spec-pinned (REQ-3 "age unknown → 0") — accepted limitation, **documented here**; flagged amendment option (not default): retain the last known age on timeout so the gauge family stays continuous — testable either way (scripted fake: healthy→lag→hang) | spec REQ-3; F16 |
| F12 | **k8s readinessProbe default `timeoutSeconds: 1` < degraded-path latency** — helm `deployment.yaml:83-88` sets none; k8s cancels `/readyz` at 1 s, the 200-degraded body is written to a dead conn, pod NotReady after 3 failures → LB eviction: F1's "no LB eviction" and compat #1's "keep the node in rotation" are **false as shipped** | H1: `timeoutSeconds: 10` on the helm readinessProbe + T8 string pin; H2: bound `repo.Ping` with `readyzProbeTimeout` + blocking-ping test; worst case = 6 s < 10 s | compat #1; T8 |
| F13 | **Stale gauge with zero `/readyz` traffic** — a `Ready()`-only cache freezes at the last probe value | run-loop `probeAndRecord` per poll cycle (G3), freshness ≤ poll interval (1 s default, `config_audit_governance.go:61`) independent of probe traffic; **proven by T6, which never calls `Ready()`** | G3; T6 |
| F14 | **Partial group-member outage** — one group member degraded, another healthy; non-implementer members (billing-like); empty group; one member hard-errors | group `Degraded()` OR / `BacklogAge()` max over implementing members; non-implementers contribute false/0; group `Ready()` stays AND — a member hard-error still 503s (unchanged); composition pinned by T2-add | G2; T2-add |
| F15 | **Gauge callback / run loop / `Ready()` race** — the scrape callback reads the cache concurrently with probe writes; torn (degraded, age) pairs or data races | `degradedMu` single-acquisition writes (H0), getters under `RLock` — readers can only observe valid pairs; mutex discipline is **only provable under `-race`** → T7 + **mandatory** `test-race-meta` scope extension to `./internal/auditgovernance/` (H3, §6 step 6) — CI-enforced, not convention-only | `degradedMu`; T7; H3 |
| F16 | **Probe timeout clobbers a known lag reading** — gauge 460 → 0 mid-window restarts the alert's `for: 10m` accumulation; repeated 460→0→460 cycles (intermittent timeouts) can starve the alert | Follows F11's decision (0 on timeout, spec-pinned); the next lag/healthy probe re-records; if the F11 amendment is accepted, the retained-age variant closes this too | spec REQ-3; F11 |
| F17 | **Run-loop cadence under wedge** — `probeAndRecord` serialized in `run()` blocks the loop ≤ 2 s per cycle, delaying reconcile/deliver/cleanup | Bounded by `storeProbeTimeout`; the loop's own store calls are already bounded by `httpTimeout` (5 s default) — no new order-of-magnitude; probe errors log and skip recording, the loop never stops on probe failure | D1; documented |
| F18 | **Caller-canceled probe marks degraded** — k8s/LB cancels at 1 s → `context.Canceled` → spec REQ-2 pins Canceled ⇒ degraded | Recorded degraded with age 0; the run-loop refresh (Background ctx, T6) self-corrects within one poll interval — blip is bounded and invisible (the caller already gave up) | REQ-2; G3 |

---

## 6. Migration steps

No DB migration, no config migration, no wire-format break (healthy `/readyz` body identical). The "migration" is code + rollout:

1. **Rename** `Runtime.BacklogAge(ctx)` → `PendingBacklogAge(ctx)` (4 call sites: `runtime.go:174`, `build.go:114`, `runtime_test.go:449`, `:492`). `make check` green at this point (pure rename).
2. **`runtime.go`**: add `storeProbeTimeout`, `degradedMu`/`degraded`/`backlogAge` fields, `probeAndRecord`, cache getters `Degraded()`/`BacklogAge()`, `Ready()` restructure, run-loop refresh. Keep `TestRuntimeReadyDegradesOnBacklogLag` + `TestRuntimeBacklogAgeZeroWhenNoPending` green verbatim.
3. **`cmd/server/http.go`**: `degradedChecker` interface + `readinessGroup` methods + `readyzHandler` degraded branch (healthy body untouched).
4. **`cmd/server/build.go`**: swap gauge callback to the cache getter (drop the store query — scrapes become I/O-free).
4b. **Hardening H1/H2 (`deploy/helm/aero-vault/templates/deployment.yaml` + `cmd/server/http.go`)**: add `timeoutSeconds: 10` to the readinessProbe (F12 — without it the degraded 200 is canceled by k8s's 1 s default and the node is evicted, falsifying F1); bound `repo.Ping` with `readyzProbeTimeout` (H2 — makes the ≤ 6 s worst case deterministic). These are the only two lines that make the degraded-tier observable end-to-end.
5. **Tests (T1–T5, §7)**: new `internal/auditgovernance/runtime_ready_test.go`; extend `cmd/server/http_test.go` (+~50 OK, 129 → ~180); extend `internal/telemetry/metrics_test.go` (+~30 OK, 135 → ~165).
6. **Gate**: `make check` (gofmt, build, vet, `go test ./...`, `test-race-meta`, cli-check) — the artifact gate for T3's YAML parse and T4's scrape. **H3 (mandatory — resolves T7's gate status):** extend `test-race-meta` (`Makefile:116-120`) to `go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/ ./internal/auditgovernance/` and update the target comment to name the package. This matches the existing precedent — the target exists because "the direction's acceptance requires -race on the concurrent metadata tests", and AC-1 aux (F15) is the same pattern (a mutex contract provable only under `-race`); the alternative (convention-only, manual acceptance step) leaves F15 unenforced by CI, so it is rejected. T7 therefore runs under `-race` in every `make check`; verified safe to add: `go test -race -count=1 ./internal/auditgovernance/` passes today (34.8 s, deadline-bounded by the blocking stubs, well inside the unchanged 300 s timeout) and the existing relay/run-loop tests gain race coverage for free.
7. **Rollout observation**: after deploy, confirm `/readyz` healthy body unchanged; induce a sink outage and observe (a) 200 + degraded marker **within the k8s probe window (H1: `timeoutSeconds: 10` — verify the pod stays Ready via `kubectl get pods`), (b) gauge climb, (c) alert at 450 s/10 m, (d) recovery restores `{"ok":true}` and gauge 0. Note the operational change: the gauge no longer queries the store per scrape — it reflects the last probe (freshness ≤ poll interval + /readyz cadence). Note (F11): a wedged *store* (not sink) shows `degraded:true` with gauge 0 — the alert fires only for the store-alive backlog case; the /readyz marker is the wedge signal.
8. **Follow-ups (explicitly out of scope here, flagged):** `docs/configuration.md:274` wording ("Oldest undelivered outbox age that `/readyz` permits" is now stale — lag no longer 503s); startup warning for non-default maxLag vs the 450 literal (sibling spec scope).

---

## 7. Testable acceptance mapping

| Acceptance (spec §5) | Tests (REQ-6) | File / mechanics | Assertion surface |
|---|---|---|---|
| **AC-1** D1 drill: degraded sentinel + `/readyz` 200 while alert can fire | **T1** `TestRuntimeReadyDegradedSentinel` | `internal/auditgovernance/runtime_ready_test.go` (new; runtime_test.go is 498/500) | (a) seed fact, backdate `created_at_ns` −8 s via second WAL connection (`UPDATE audit_governance_outbox SET created_at_ns=? WHERE id=?`, no sleeps) → `Ready()==nil` ∧ `Degraded()==true` ∧ `BacklogAge()>4s`; complete the row → `Degraded()==false` (explicit re-probe — the cache only refreshes on probe). (b) hanging-store fake (probe blocks on `<-ctx.Done()`, returns `ctx.Err()`; loopback base URL so `New()` makes no network calls) → `Ready(background)==nil`, elapsed ∈ [1 s, 5 s], `Degraded()==true`, `BacklogAge()==0` (age unknown). (c) **erroring-store fake** — same partial-stub family as (b) (`errProbeStore struct { repository.AuditGovernanceStore }`, real repo embedded so `New()`'s `ApplyAuditGovernanceBindings` works, probe methods overridden; cf. `errStatStorage` idiom, `http_test.go:46-52`): (c1) `HasPendingDrainingAuditGovernance` → immediate **non-context** `errors.New` → `Ready(background)` returns `"audit governance drain lookup failed"` ∧ `Degraded()==false`; (c2) drain probe returns `false,nil`, `OldestPendingAuditGovernance` → immediate error → `"audit governance backlog lookup failed"` ∧ `Degraded()==false`. Pins the preserved fail-closed branches (`runtime.go:164-166/:175-177`, G4) and the genuine-error side of the `isProbeCtxError` fork — a too-broad match (all errors → degraded) fails (c), a too-narrow match (timeouts → hard error) fails (b); no timing dependency (errors return immediately, not via ctx) |
| | **T2** `TestReadyzDegradedExtraReturns200WithMarker` / `TestReadyzHealthyExtraReturns200Unchanged` / `TestReadyzAuditGovernanceDegradedDrill` | `cmd/server/http_test.go` | fake `{Ready→nil, Degraded→true, BacklogAge→123s}` → **200**, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`; `Degraded→false` → 200, body exactly `{"ok":true}` (byte-identity preserved); real `auditgovernance.New` + hanging store → 200 never 503, body contains `"degraded":true`, elapsed ∈ [1 s, 5 s] |
| **AC-2** alert rule exists with 450 s | **T3** `TestAlertsYMLAuditGovernanceRuleConsistency` | `internal/telemetry/metrics_test.go` | YAML-parse `../../deploy/prometheus/alerts.yml` (package-relative; no promtool in CI — `go test ./...` is the artifact gate): rule `AuditGovernanceBacklogDegraded` exists, `expr` references exactly `audit_governance_backlog_age_seconds` with `> 450`, `for: 10m`, `severity: warning`, and no other `audit_governance_*` name in the expr (drift guard both ways). The case-sensitive grep matches only comment/description today (`alerts.yml:158/:169`) — the parse is the pin |
| **AC-3** relay counters + oldest-age registered/incremented | shipped pins + **T4** `TestAuditGovernanceBacklogAgeGaugeSurfaceInScrape` | `internal/telemetry/metrics_test.go` | existing `TestAuditGovernanceMetrics_SurfaceInScrape` (`:82-108`, four counters value 1) + `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`); T4 registers the gauge once (fixed callback `450`) → scrape body line-exact `audit_governance_backlog_age_seconds 450` via `scrapeValue`; re-scrape after callback → `0` → `0`; single-shot registration mirrors `TestObservableGauges_SurfaceInScrape` (`:114`) |
| **AC-4** T-3: failed row → neither `OldestPending` nor gauge | **T5** `TestRuntimeBacklogAgeZeroWhenAllTerminal` | `internal/auditgovernance/runtime_ready_test.go` | seed one fact; `ClaimAuditGovernance(ctx,"t","tok",1,1,time.Minute)` + `FailAuditGovernance(ctx,id,"t","tok","conflict:true")` (fenced public API, `claim.go:159-172`) → `OldestPendingAuditGovernance` ok==false (re-pin of `audit_governance_test.go:442` at runtime level) ∧ `PendingBacklogAge(ctx)` ok==false (gauge 0) ∧ `Ready()==nil` ∧ `Degraded()==false` |
| **AC-1 aux (F13)** gauge freshness with zero /readyz traffic | **T6** `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` | `internal/auditgovernance/runtime_ready_test.go` | real SQLite store; seed + WAL-backdate one row −8 s; `Start(ctx)` (poll 10 ms); **never call `Ready()`**; deadline-poll (≤ 3 s) until `Degraded()==true`, assert `BacklogAge()>4s`; `Close()`. Proves the run-loop feed — the G3 closure is testable, not just claimed |
| **AC-1 aux (F14)** partial group-member outage | **T2-add** `TestReadyzGroupDegradedComposition` / `TestReadyzGroupReadyFailPropagates` / `TestReadyzGroupEmpty` | `cmd/server/http_test.go` | build a real `readinessGroup`: degraded fake + healthy fake + non-implementer (billing-like, `Ready` only) → group `Degraded()` true, `BacklogAge()` max (123 s); `readyzHandler` with the group → 200 + exact degraded body; a member `Ready` error → 503 `runtime dependency unavailable`; empty group → 200 `{"ok":true}` |
| **AC-1 aux (F15)** concurrent probes vs cache readers | **T7** `TestRuntimeDegradedCacheConcurrentAccess` | `internal/auditgovernance/runtime_ready_test.go` | N goroutines × `Ready()`/`probeAndRecord`/`Degraded()`/`BacklogAge()` against a scripted store (healthy→lag→hang); assert only valid (degraded, age) pairs; **CI-enforced**: T7 runs under `-race` in every `make check` via the `test-race-meta` scope extension (H3, §6 step 6) — the mutex contract is provable only under `-race` |
| **compat #1 (F12)** k8s probe window ≥ degraded latency | **T8** `TestHelmReadinessProbeTimeoutSeconds` | `cmd/server/http_test.go` | string-pin the helm template (it is Go-templated, not parseable YAML): `deployment.yaml` readinessProbe block must contain `timeoutSeconds: 10` — drift in either direction fails (worst case = ping 2 s + storage 2 s + audit probe 2 s = 6 s < 10 s) |

**Preservation pins (must stay green, not re-written):** `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:415`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`:471`, modulo the `PendingBacklogAge` rename), `TestReadyzStorageProbeTimeout`/`TestReadyzErrNotFoundIsReady`/`TestReadyzImmediateStorageError` (`http_test.go:69-129`, all nil-extra), `TestAuditGovernanceMetrics_SurfaceInScrape`, `TestRuntimeRelayCountersTrackDeliveryOutcomes`, `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:419`). **New pin on preserved behavior (T1(c) — joins this list as an addition, not a re-write):** the fail-closed genuine-error branches of `Ready()` (`runtime.go:164-166/:175-177`) — preserved by REQ-1, restructured by REQ-2 into `probeAndRecord`, previously zero-coverage (G4).

---

## 8. Risks & gates

- **Overload-collision resolution (G1)** is the only breaking change; it is a rename inside an internal package with 4 mechanical call sites — mitigated by step 1 of §6 (rename-only commit, `make check` green before behavior work).
- **Timing flakes**: T1/T2 use blocking stubs (deterministic lower bound: response cannot precede the 2 s probe deadline), WAL second-writer backdating (no sleeps), and interval/`>` assertions only — the proven `TestReadyzStorageProbeTimeout` idiom.
- **Line gates**: `runtime_test.go` 498 → 498 (renames only; new tests in `runtime_ready_test.go`); `runtime.go` 231 → ~276 (< 500); `http.go` 189 → ~220 (< 500, incl. H2 ping bound); `metrics.go` 444 (no registration changes); `http_test.go` 129 → ~230 (T2 + T2-add + T8); `metrics_test.go` 135 → ~180; `runtime_ready_test.go` new (~200) — all under the 500-line single-file gate.
- **Duplicated-instrument rejection**: T4 single-shot registration (shared TestMain handler, `metrics_test.go:114` pattern) — the OTel SDK returns an error and drops a duplicate instrument (no panic), so the rule is enforced by convention + the scrape assertion.
- **F12 (new)**: the shipped helm readinessProbe has no `timeoutSeconds` — k8s's 1 s default cancels every degraded-path response (≥ 2 s) and evicts the node, falsifying F1/compat #1. Mitigated by H1 (`timeoutSeconds: 10`) + T8, H2 (ping bound).
- **F13/F14/F15 (new)**: G3 (run-loop feed), G2 (group composition) and the `degradedMu` contract now each have a dedicated test (T6, T2-add, T7) — previously the mitigations were claims without proof. **T7 is CI-enforced, not convention-only**: `test-race-meta` gains `./internal/auditgovernance/` (H3, one line) — otherwise AC-1 aux (F15) is unenforced by `make check` as written.
- **F11/F16 (new, accepted)**: age 0 on probe timeout is spec-pinned; the wedge case is alert-silent by design and documented (the /readyz marker is the wedge signal); amendment option flagged.
- **Alert drift**: T3 pins expr ↔ gauge name both ways; alert file has no templating; 450 literal unchanged.
- **Gate**: `make check` (gofmt / build / vet / test / test-race-meta / cli-check) — zero network, zero Docker, SQLite + local FS + `httptest`; `test-race-meta` now includes `./internal/auditgovernance/` (H3, runs T7 under `-race`). T3's YAML parse uses **`go.yaml.in/yaml/v2`** (already in the module graph as an indirect dep, `go.mod:73` — promoted to direct by the test import via `go mod tidy`; zero new transitive downloads). Justification under I6: the package is already resolved in the build graph, the parse is test-only, and a hand-rolled section scanner would be more fragile than the artifact it guards. No other `go.mod` change.

*Verification basis: every evidence citation re-read on this checkout (HEAD `15763e2` + uncommitted worktree); line numbers reflect the working tree as read during this design's production.*
