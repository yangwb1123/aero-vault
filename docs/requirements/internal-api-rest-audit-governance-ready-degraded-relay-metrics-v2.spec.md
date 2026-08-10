# Requirements Specification — `internal/api/rest` (analysis label): D1 drill read-path half — bounded `Ready()` probes + degraded sentinel + `/readyz` degraded payload + run-loop gauge freshness (delta v2 of the B3-2 contract)

**Module:** `internal/api/rest` (analysis label; implementation surface is `internal/auditgovernance` + `cmd/server` + `deploy/helm` + `Makefile` — see §1; no `internal/api/rest` handler change)
**Direction:** "Implement the D1 drill read-path half: bound Ready()'s store probes with a degraded sentinel + /readyz degraded payload + run-loop gauge freshness (the unlanded delta of B3-2)"
**Source analysis:** `docs/auto/analyses/internal-api-rest-8e390260.json` (direction 1 of 3)
**Design (authoritative delta):** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.design.md` (uncommitted; D1–D3, G1–G4, H0–H3 — this spec renders it as testable requirements)
**Parent spec (shipped subset):** `docs/requirements/internal-api-rest-audit-governance-ready-degraded-relay-metrics-v1.spec.md` (REQ-1..6, AC-1..4; S1–S5 shipped by `15763e2`)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (+ uncommitted worktree; verification basis = current working tree)
**Score:** value 10 / risk reduction 9 / effort 6 / confidence 9

---

## 1. Module, scope & state on this checkout

The analysis labels this direction under `internal/api/rest` for traceability; the actual surface is the `/readyz` seam (`cmd/server/http.go`, registered outside `api/rest` at `:104`), the audit-governance runtime (`internal/auditgovernance/runtime.go`), the gauge wiring (`cmd/server/build.go`), the helm chart (`deploy/helm/aero-vault/templates/deployment.yaml`), and `Makefile` (`test-race-meta`). No `internal/api/rest` file changes.

**Delta scope (locked, per the design):** D1 bounded probes + degraded cache (`runtime.go`), D2 degraded payload + `readinessGroup` composition (`http.go`), D3 gauge callback reads the cache (`build.go`), H1 helm `timeoutSeconds: 10`, H2 `repo.Ping` bound, H3 `test-race-meta` scope extension. Preservation (pin, don't touch): REQ-1 fail-closed branches, healthy `/readyz` body byte-identity, alert rule, relay counters.

**State on this checkout (critical):** commit `15763e2` shipped the maxLag flip, `BacklogAge(ctx)`, the gauge, and the alert (parent-spec S1–S5). The uncommitted worktree additionally contains **sibling readyz-probe-timeout work** (the three `http_test.go` readyz tests at `:69-129`, `readyz_drill_test.go`, `readyzProbeTimeout`), which **already bounds `extra.Ready` via `probeCtx`** (`http.go:69`) — so the direction's "unbounded `extra.Ready(req.Context())` at :66" claim no longer holds on the working tree (it was true at HEAD). The remaining, still-missing delta:

| # | Item | Status on this checkout |
|---|------|------------------------|
| S1 | `Ready()` maxLag flip → nil + warn (`runtime.go:178-181`) | ✅ shipped (`15763e2`) |
| S2 | `BacklogAge(ctx)` accessor (`runtime.go:151-159`) | ✅ shipped |
| S3 | Gauge `audit_governance.backlog_age_seconds` (`metrics.go:352-365`) | ✅ shipped |
| S4 | Alert `AuditGovernanceBacklogDegraded`, expr `> 450` (`alerts.yml:181-186`) | ✅ shipped |
| S5 | Relay counters attempted/delivered/failed/dead | ✅ shipped |
| S6 | **Runtime-side probe bound**: `Ready()`'s two store probes (`runtime.go:163`, `:174`) run on the **unbounded caller ctx** — a wedged relay store still hangs `/readyz` | ❌ missing (no `storeProbeTimeout`, `probeAndRecord`, `isProbeCtxError`) |
| S7 | **Degraded sentinel**: `Degraded()`, `BacklogAge()` cache getters, `degradedMu` | ❌ missing (grep across `internal/auditgovernance/` + `cmd/server/`: zero hits; only unrelated `cfg.AI.DegradedMode` at `http.go:115`) |
| S8 | **`/readyz` degraded payload** `{"ok":true,"degraded":true,"backlog_age_seconds":N}`; `readinessGroup` composition | ❌ missing — `readyzHandler` writes `{"ok":true}` unconditionally (`http.go:73-75`); group (`:43-49`) has only `Ready`; `runtimeReadiness` (`audit_governance.go:51-64`) returns the bare group |
| S9 | **Gauge freshness**: callback queries the store per scrape (`build.go:100`); run loop never refreshes a cache | ❌ missing (REQ-5 freshness holds only by accident of the 2 s `readyzProbeTimeout`) |
| S10 | **H1/H2**: helm readinessProbe `timeoutSeconds`; `repo.Ping` bound | ❌ missing — `deployment.yaml:83-88` sets none (k8s 1 s default cancels the ≥ 2 s degraded 200 → LB eviction); `repo.Ping(req.Context())` at `http.go:56-58` unbounded |
| S11 | **H3**: `test-race-meta` scope | ❌ missing — `Makefile:116-123` covers only `./internal/repository/ ./internal/reconcile/` |

**Out of scope:** B3-1 permanent-error classification, B3-3 deterministic fact IDs, B3-5 grep-consistency guards, B3-6 `Validate()`, admin-origin e2e (sibling directions of the same analysis), billing-runtime readiness, any config/migration/`.env.example` change, any new metric, `docs/configuration.md:274` wording (flagged follow-up), and any change to the shipped alert rule or counter registrations.

---

## 2. Evidence verification (re-checked on the working tree, not trusted)

Every citation in the direction was re-read on this checkout (HEAD `15763e2` + uncommitted worktree). Two claims are **stale relative to the working tree** and are corrected here; all others verify.

| # | Direction citation | Verified location (current working tree) | Verdict |
|---|---|---|---|
| E1 | `runtime.go:146-182`: `BacklogAge(ctx)` store-querying at `:151-159`; `Ready()` maxLag flip at `:170-181` | `BacklogAge(ctx) (time.Duration, bool, error)` at `runtime.go:151-159` (store query `OldestPendingAuditGovernance` + `time.Since`); `Ready` at `:162-182` — drain probe `:163`, drain hard-error `:164-166`, drain-in-progress hard-error `:167-169`, backlog probe via `r.BacklogAge(ctx)` `:174`, backlog hard-error `:175-177`, maxLag flip `:178-181` (warn + nil) | ✅ **exact** |
| E2 | No `Degraded()`/`probeAndRecord`/`isProbeCtxError`/`degradedMu`/`PendingBacklogAge` (grep, zero hits) | `grep -rn 'Degraded\|probeAndRecord\|isProbeCtxError\|degradedMu\|PendingBacklogAge' internal/auditgovernance/ cmd/server/` (non-test) → single hit `cmd/server/http.go:115` (`cfg.AI.DegradedMode`, unrelated AI flag); test files: only `readyz_drill_test.go:358,370` (alert-name marker, unrelated) | ✅ **verified absent** |
| E3 | `http.go:34-38` `readyzProbeTimeout=2s`; `:42-49` readinessGroup without degradedChecker; `:51-73` readyzHandler | `readyzProbeTimeout` comment `:34-40`, const `:41`; `readinessGroup` `:43-49` (AND-`Ready` only); `readyzHandler` `:54-75` (storage probe under `probeCtx` `:62-66`; `extra.Ready(probeCtx)` `:69-71`; unconditional `{"ok":true}` `:73-75`) | ✅ **holds** (line drift only) |
| E4 | "`extra.Ready(req.Context())` unbounded at :66" | Working tree: `extra.Ready(probeCtx)` at `http.go:69` — **already bounded** by the 2 s `readyzProbeTimeout`; `git diff HEAD -- cmd/server/http.go` shows this is the uncommitted sibling readyz-probe-timeout work | ❌ **stale vs working tree** (true at HEAD only). Operative gaps move to the **runtime side**: `Ready()`'s own probes (`runtime.go:163`, `:174`) remain on the caller ctx, and the degraded payload is absent |
| E5 | `build.go:112-120` per-scrape `BacklogAge(ctx)` store query in gauge callback | `auditGovernanceBacklogAgeGaugeFn` at `cmd/server/build.go:94-101` — callback calls `rt.BacklogAge(ctx)` (`:100`) **per scrape** (store query); registered `:127` | ✅ **holds** (line drift: 94-101/127) |
| E6 | `audit_governance.go:51-64` `runtimeReadiness` returns `readinessGroup` | `runtimeReadiness` `cmd/server/audit_governance.go:51-64` — builds a `readinessGroup` (billing + audit) or nil; wired at `cmd/server/main.go:157` | ✅ **exact** |
| E7 | `http_test.go:69-129` three readyz tests, none exercising a degraded extra | `TestReadyzStorageProbeTimeout` `:69-88` (blocking-stat stub, elapsed ∈ [1 s, 5 s]), `TestReadyzErrNotFoundIsReady` `:93-108` (body assert `{"ok":true}` at `:103`), `TestReadyzImmediateStorageError` `:110-129`; stub idiom `stubReadyRepo`/`blockingStatStorage`/`notFoundStatStorage`/`errStatStorage` `:27-60`; **all three pass `nil` extra** | ✅ **exact** (these tests are uncommitted sibling work — they land with this delta's prerequisite) |
| E8 | `metrics.go:352-365` `RegisterAuditGovernanceBacklogAgeGauge` | `internal/telemetry/metrics.go:352-365` — `Int64ObservableGauge("audit_governance.backlog_age_seconds")`, callback per scrape | ✅ **exact** |
| E9 | Alert expr `audit_governance_backlog_age_seconds > 450`, `for: 10m` | `deploy/prometheus/alerts.yml:176-186` — group `aero-vault-audit-governance` `:176`, rule `AuditGovernanceBacklogDegraded` `:181`, `expr: audit_governance_backlog_age_seconds > 450` `:182`, `for: 10m` `:183`, `severity: warning` `:184` (analysis cited `:163` — drift from uncommitted edits; rule content identical) | ✅ **holds** (line drift) |
| E10 | Helm `deployment.yaml:83-88` readinessProbe, no `timeoutSeconds` | `deploy/helm/aero-vault/templates/deployment.yaml:83-88` — `readinessProbe` block (`httpGet: /readyz`, `initialDelaySeconds: 3`, `periodSeconds: 10`); `grep -n timeoutSeconds` → **zero hits in the file** | ✅ **exact** |
| E11 | `Makefile:116-123` `test-race-meta` excludes `./internal/auditgovernance/` | `Makefile:116-123` — `go test -race -count=1 -timeout 300s ./internal/repository/ ./internal/reconcile/`; target inside `check` (`:122`) | ✅ **exact** |
| E12 | Rename surface for `BacklogAge(ctx)` (design G1) | Call sites: `runtime.go:174` (internal), `cmd/server/build.go:100` (removed by REQ-7's cache-getter swap), `runtime_test.go:449` and `:492` — 4 total, all mechanical | ✅ **exact** |
| E13 | `runtime_test.go` at 498/500 lines → new-file mandate; harness `runtimeConfig` maxLag 4 s / poll 10 ms | `wc -l internal/auditgovernance/runtime_test.go` → **498**; `runtimeConfig` `:39-46` (`MaxLagSeconds=4`, `PollMilliseconds=10`); `TestRuntimeReadyDegradesOnBacklogLag` `:415-466`; `TestRuntimeBacklogAgeZeroWhenNoPending` `:471-497` | ✅ **exact** |
| E14 | `go.yaml.in/yaml/v2` available for T3 | `go.mod:73` — `go.yaml.in/yaml/v2 v2.4.4 // indirect` (promoted to direct by the T3 test import; zero new transitive downloads; I6 justification: already resolved in the build graph, test-only, YAML is not templated) | ✅ **exact** |
| E15 | `go test -race -count=1 ./internal/auditgovernance/` fits the unchanged 300 s budget (H3 feasibility) | **Re-run on this checkout:** `go test -race -count=1 -timeout 300s ./internal/auditgovernance/` → `ok … 42.687s` | ✅ **verified live** |

---

## 3. Requirements

### REQ-1 — Bound `Ready()`'s two store probes with a degraded timeout (S6; design D1)

In `internal/auditgovernance/runtime.go`:

- New package constant `storeProbeTimeout = 2 * time.Second`, commented as mirroring `readyzProbeTimeout` (`cmd/server/http.go:34-41` — same rationale, same value, independent symbol); **not** derived from `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` (5 s default, relay-HTTP bound, too slow for a readiness probe). No new config knob.
- Restructure `Ready()` (`:162-182`) so **both** store probes (`HasPendingDrainingAuditGovernance` `:163`, `OldestPendingAuditGovernance` — the age source) run under `context.WithTimeout(ctx, storeProbeTimeout)` via a shared helper `probeAndRecord(ctx) error` that also records the cache (REQ-2).
- Probe ctx **timeout/cancel** on either probe (`isProbeCtxError`: `errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)`) → record degraded with age 0, **return nil** — a wedged relay store degrades the audit-governance readiness contribution, it never 503s the node (the D1 read-path half).
- **Preserved branches (pin, don't touch):** genuine non-context store errors → hard errors `"audit governance drain lookup failed"` / `"audit governance backlog lookup failed"`; drain in progress → `"audit governance binding drain is in progress"`; lag > maxLag → warn + nil; terminal rows excluded by the unchanged store predicate (`internal/repository/audit_governance_claim.go:211-222`, `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`). Error strings unchanged.

### REQ-2 — Degraded sentinel: `Degraded()` / `BacklogAge()` cache getters + `degradedMu` (S7; design D1, G1, H0)

- **Rename (G1 — Go has no overloading):** `BacklogAge(ctx) (time.Duration, bool, error)` (`runtime.go:151-159`) → `PendingBacklogAge(ctx)`, same signature/body — the name `BacklogAge` is freed for the cache getter. Call sites: `runtime.go:174` (internal), `build.go:100` (deleted by REQ-4), `runtime_test.go:449`, `:492` (mechanical; the two pinned tests stay green verbatim).
- `Runtime` gains `degradedMu sync.RWMutex` guarding `degraded bool` + `backlogAge time.Duration` (the struct has no mutex today; `sync` already imported).
- `recordDegraded(degraded, age)` writes **both** fields under a **single** `degradedMu.Lock()` acquisition (readers can only observe valid pairs — design H0 correction: the earlier draft's per-field writes and inverted timeout branch are rejected); `probeAndRecord` calls it on every branch (lag > maxLag → `(true, age)`; probe timeout/cancel → `(true, 0)` — age unknown per parent-spec REQ-3; healthy/empty → `(false, age)` with 0 when `!ok`).
- New zero-I/O getters: `func (r *Runtime) Degraded() bool` and `func (r *Runtime) BacklogAge() time.Duration` (RLock reads). Freshness ≤ one probe; `/readyz` re-probes live on every request.

### REQ-3 — Run-loop refresh (S9, gauge freshness; design G3, T6)

In `run()` (`runtime.go`), after `cleanupDelivered()` and an `r.stopping()` guard (same as the other phases), call `probeAndRecord(context.Background())` **once per poll cycle**. A store error in the loop logs and skips recording — it never stops the loop. This makes the gauge fresh (≤ poll interval, default 1 s — `internal/config/config_audit_governance.go:61`) **independent of `/readyz` traffic**, proven by T6 (zero `Ready()` calls).

### REQ-4 — Gauge callback reads the cache, never the store (S9; design D3)

In `cmd/server/build.go`, replace the per-scrape store query (`auditGovernanceBacklogAgeGaugeFn` `:94-101`, `rt.BacklogAge(ctx)` at `:100`) with the zero-I/O getter: callback returns `int64(auditRuntime.BacklogAge().Seconds())` (0 when healthy/unknown). This is parent-spec REQ-5 compliance ("a scrape must never block on the store") — the shipped callback is a per-scrape store query, so the swap is a requirement fix, not just freshness. `Degraded()` must **not** gate the value (age > 0 implies lag; the 450 threshold sits below default maxLag 900). Keep the `if auditRuntime != nil` registration gate (`:127`).

### REQ-5 — `/readyz` degraded payload + `readinessGroup` composition (S8; design D2, G2)

In `cmd/server/http.go`:

- New interface next to `readinessChecker` (`:31`): `type degradedChecker interface { Degraded() bool; BacklogAge() time.Duration }`.
- `readinessGroup` (`:43-49`) gains `Degraded() bool` (OR over members implementing `degradedChecker`; false when none) and `BacklogAge() time.Duration` (max over implementing members; 0 when none). **Composition is mandatory** — production `extra` is a `readinessGroup` (`audit_governance.go:51-64`), so a bare type-assert on the runtime fails; with the group methods, `cmd/server/main.go:157` wiring is untouched. `billing.Runtime` (no `Degraded()`) contributes false/0; group `Ready()` stays AND (a member hard-error still 503s).
- `readyzHandler` (`:54-75`): after `extra.Ready(probeCtx)` succeeds (`:69-71`), type-assert `extra.(degradedChecker)`; when implemented and `Degraded()` → **HTTP 200**, `Content-Type: application/json`, body `{"ok":true,"degraded":true,"backlog_age_seconds":<int64 seconds>}` (written via `fmt.Fprintf` literal template, **not** `json.Marshal` — keeps the healthy byte-identity pin trivial). Healthy path stays **byte-identical** `{"ok":true}` (`:73-75`) — `TestReadyzErrNotFoundIsReady` (`http_test.go:103`) must stay green. 503 paths (`database unavailable` / `storage unavailable` / `runtime dependency unavailable`) unchanged.

### REQ-6 — H1: helm readinessProbe window ≥ degraded-path latency

`deploy/helm/aero-vault/templates/deployment.yaml` readinessProbe block (`:83-88`) gains `timeoutSeconds: 10`. Rationale (design F12): k8s's 1 s default cancels every degraded-path response (≥ 2 s — runtime probe bound), marks the pod NotReady after 3 failures, and LB-evicts it — falsifying the D1 "degrade, never evict" claim. Worst-case handler latency after this delta = ping 2 s (REQ-7) + storage probe 2 s + audit probe 2 s = 6 s < 10 s.

### REQ-7 — H2: bound `repo.Ping` with `readyzProbeTimeout`

`repo.Ping(req.Context())` (`http.go:56-58`) gains the same `readyzProbeTimeout` bound as the storage probe (design H2) — makes the ≤ 6 s worst case deterministic on a wedged database. Pinned by a blocking-Ping test mirroring `TestReadyzStorageProbeTimeout` (elapsed ∈ [1 s, 5 s], 503 `"database unavailable"`).

### REQ-8 — H3: `test-race-meta` covers `./internal/auditgovernance/`

`Makefile` `test-race-meta` (`:116-123`) gains `./internal/auditgovernance/` (one line; comment updated to name the package). Feasibility verified live: `go test -race -count=1 -timeout 300s ./internal/auditgovernance/` passes in **42.7 s** today — well inside the unchanged 300 s budget; the existing relay/run-loop tests gain race coverage for free, and the new `degradedMu` contract (REQ-2) is CI-enforced, not convention-only.

### REQ-9 — Test placement & hard gates (design §6-7)

- **New file** `internal/auditgovernance/runtime_ready_test.go` — `runtime_test.go` is 498/500 lines (E13); renames only in the existing file (stays 498).
- Extend `cmd/server/http_test.go` (+~50, 129 → ~180 OK) and `internal/telemetry/metrics_test.go` (+~30, 189 → ~220 OK; includes T3's YAML parse and T4's single-shot gauge registration mirroring `TestObservableGauges_SurfaceInScrape` at `:168`).
- All tests: SQLite + local FS + `httptest`, zero network/Docker; no new `go.mod` direct deps beyond promoting `go.yaml.in/yaml/v2` (E14, I6-justified).
- Gate: `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` / cli-check) — with REQ-8, T7 runs under `-race` in every `make check`.

---

## 4. Decisions & non-goals

- **D1 — Probe bound is a package constant** (`storeProbeTimeout = 2 s`), not config; mirrors `readyzProbeTimeout`; deliberately not derived from the relay HTTP timeout.
- **D2 — The degraded signal is a cache, not a live query**: `Degraded()`/`BacklogAge()` do zero store I/O; `/readyz` re-probes live; the gauge scrape reads the last probe result; the run loop keeps it fresh (REQ-3).
- **D3 — Healthy `/readyz` body is a wire contract**: byte-identical `{"ok":true}`; the degraded body is the only new wire form, still HTTP 200 (k8s/LB keep the node in rotation — the D1 point).
- **D4 — Fail-closed preserved**: only the two D1 conditions (lag > maxLag, probe timeout/cancel) stop 503ing; genuine store errors, drain-in-progress, ping failure, storage-probe failure still 503 — the genuine-error branches get their first dedicated pins (T1c, design G4).
- **D5 — Wedge → alert-silent degraded is accepted**: age 0 on probe timeout is spec-pinned (parent-spec REQ-3 "age unknown → 0"); the `/readyz` degraded marker is the wedge signal; the amendment option (retain last known age) is flagged, not adopted (design F11/F16).
- **Non-goals:** no config env surface, no migration, no new metric, no `internal/api/rest` change, no alert-rule change, no counter change, billing-runtime readiness untouched, `docs/configuration.md:274` wording flagged as follow-up, sibling directions (B3-1/B3-3/B3-5/B3-6, admin-origin e2e) out of scope.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**D1 — (T1b) hanging-store fake → `Ready(background)==nil` ∧ `Degraded()==true` ∧ `BacklogAge()==0`, elapsed ∈ [1 s, 5 s].**
*Test:* `TestRuntimeReadyDegradedSentinel` (hanging-store case) in `internal/auditgovernance/runtime_ready_test.go` — store fake whose probe methods block on `<-ctx.Done()` and return `ctx.Err()` (partial-stub idiom: embed the real repo so `New()`'s `ApplyAuditGovernanceBindings` works; loopback publisher base URL so `New()` makes no network calls; cf. `blockingStatStorage`, `http_test.go:36-41`). Assert `Ready(context.Background())` returns nil; elapsed ∈ [1 s, 5 s] (blocking stub ⇒ response cannot precede the 2 s `storeProbeTimeout`; upper bound proves boundedness — the proven `TestReadyzStorageProbeTimeout` idiom); `Degraded()==true`; cache `BacklogAge()==0` (age unknown). Timeout degrades, never 503 — the D1 read-path half.

**D1 — (T1c) genuine non-context store error → `Ready()==error` ∧ `Degraded()==false`.**
*Test:* same file, `errProbeStore` partial stub (`http_test.go:46-52` idiom) — (c1) `HasPendingDrainingAuditGovernance` returns an immediate non-context error → `Ready(background)` returns `"audit governance drain lookup failed"` ∧ `Degraded()==false`; (c2) drain probe nil, `OldestPendingAuditGovernance` immediate error → `"audit governance backlog lookup failed"` ∧ `Degraded()==false`. Errors return immediately (no ctx involvement, no timing dependence) — pins the preserved fail-closed branches (design G4: previously zero coverage) and the genuine side of the `isProbeCtxError` fork: a too-broad match fails (c), a too-narrow match fails (b).

**D1 — (T2) `readyzHandler` with degraded fake extra → HTTP 200, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`; healthy body byte-identical `{"ok":true}`.**
*Test:* `cmd/server/http_test.go` — `TestReadyzDegradedExtraReturns200WithMarker` (fake `{Ready→nil, Degraded→true, BacklogAge→123s}` with `stubReadyRepo`/`notFoundStatStorage`) → status 200, response body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}`; `TestReadyzHealthyExtraReturns200Unchanged` (same fake, `Degraded→false`) → 200, body byte-identical `{"ok":true}` (healthy wire contract preserved — `http_test.go:103` idiom). Design auxiliary (T2-add, same file): `readinessGroup` composition — degraded fake + healthy fake + non-implementer (billing-like `Ready`-only) → group `Degraded()` true / `BacklogAge()` max; a member `Ready` error → 503 `"runtime dependency unavailable"`; empty group → 200 `{"ok":true}`. Plus design `TestReadyzAuditGovernanceDegradedDrill`: real `auditgovernance.New` + hanging store through `readyzHandler` → 200, body contains `"degraded":true`, elapsed ∈ [1 s, 5 s].

**T-3 — (T5) seed fact → Claim + terminal Fail → `OldestPendingAuditGovernance` ok==false ∧ `BacklogAge()==0` ∧ `Ready()==nil`.**
*Test:* `TestRuntimeBacklogAgeZeroWhenAllTerminal` in `runtime_ready_test.go` — seed one fact via `InsertEventWithGovernance`; `ClaimAuditGovernance(ctx,"t","tok",1,1,time.Minute)` + `FailAuditGovernance(ctx,id,"t","tok","conflict:true")` (lease-fenced public API, `internal/repository/audit_governance_claim.go:20` / `:182`). Assert `OldestPendingAuditGovernance` ok==false (re-pin of `audit_governance_test.go:542` at runtime level) ∧ `PendingBacklogAge(ctx)` ok==false (gauge 0; cache `BacklogAge()==0` after the probe) ∧ `Ready()==nil` ∧ `Degraded()==false` — terminal rows excluded from lag; a dead-lettered backlog never blocks readiness.

**T-3 — (T6) run-loop refreshes cache with zero `Ready()` calls (gauge freshness, G3).**
*Test:* `TestRuntimeRunLoopRefreshesCacheWithoutReadyCalls` in `runtime_ready_test.go` — real SQLite store; seed one fact, backdate `created_at_ns` −8 s via `UPDATE audit_governance_outbox SET created_at_ns=? WHERE id=?` on a second WAL connection (no sleeps); `Start(ctx)` with poll 10 ms (`runtimeConfig` harness, `runtime_test.go:39-46`); **never call `Ready()`**; deadline-poll (≤ 3 s) until `Degraded()==true`; assert cache `BacklogAge() > 4*time.Second`; `Close()`. Proves the run-loop feed is testable, not just claimed.

**T-3 — (T3) YAML-parse `deploy/prometheus/alerts.yml`: expr references exactly `audit_governance_backlog_age_seconds > 450` with `for: 10m`.**
*Test:* `TestAlertsYMLAuditGovernanceRuleConsistency` in `internal/telemetry/metrics_test.go` — YAML-parse `../../deploy/prometheus/alerts.yml` (package-relative; `go.yaml.in/yaml/v2`, E14; no promtool in CI — `go test ./...` is the artifact gate): rule `AuditGovernanceBacklogDegraded` exists; its `expr` references exactly `audit_governance_backlog_age_seconds` with threshold `> 450`; `for: 10m`; `severity: warning`; and **no other `audit_governance_*` name appears in any expr** (drift guard both ways — a case-sensitive grep matches only comment/description today, `alerts.yml:177-180`/`:186`, so the parse is the pin).

**T-3 — (T8) helm `deployment.yaml` readinessProbe contains `timeoutSeconds: 10` (H1) + blocking `repo.Ping` bound test (H2).**
*Test:* `TestHelmReadinessProbeTimeoutSeconds` in `cmd/server/http_test.go` — string-pin the helm template (Go-templated, not parseable YAML): the readinessProbe block (`deployment.yaml:83-88`) must contain `timeoutSeconds: 10` (drift in either direction fails; worst case = ping 2 s + storage 2 s + audit probe 2 s = 6 s < 10 s). H2: `TestReadyzPingProbeTimeout` — `stubReadyRepo` variant whose `Ping` blocks on `<-ctx.Done()` → 503 `"database unavailable"`, elapsed ∈ [1 s, 5 s] (mirror of `TestReadyzStorageProbeTimeout`).

**T-3 — (T7) `-race` via `Makefile` `test-race-meta` gaining `./internal/auditgovernance/` (H3).**
*Test:* `TestRuntimeDegradedCacheConcurrentAccess` in `runtime_ready_test.go` — N goroutines × `Ready()`/`probeAndRecord`/`Degraded()`/`BacklogAge()` against a scripted store (healthy→lag→hang); assert only valid (degraded, age) pairs. **CI-enforced:** `test-race-meta` (`Makefile:116-123`) gains `./internal/auditgovernance/` so T7 runs under `-race` in every `make check` — the `degradedMu` contract is provable only under `-race`; feasibility verified live (E15: 42.7 s within the unchanged 300 s budget).

**Preservation pins (must stay green, not re-written):** `TestRuntimeReadyDegradesOnBacklogLag` (`runtime_test.go:415`), `TestRuntimeBacklogAgeZeroWhenNoPending` (`:473`, modulo the `PendingBacklogAge` rename at `:492`), `TestReadyzStorageProbeTimeout`/`TestReadyzErrNotFoundIsReady`/`TestReadyzImmediateStorageError` (`http_test.go:69-129`), `TestAuditGovernanceMetrics_SurfaceInScrape` (`metrics_test.go:106`), `TestRuntimeRelayCountersTrackDeliveryOutcomes` (`relay_metrics_test.go:88`), `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:519`). New pin on preserved behavior: T1c (fail-closed genuine-error branches).

---

## 6. Risks & gates

- **Stale-citation hazard**: the direction's "unbounded `extra.Ready` at :66" and "alerts.yml:163" are stale vs. the working tree (E4/E9 — the sibling probe-timeout work and uncommitted alert edits shifted them). The requirements above are written against **current** lines; implementers must re-grep, not trust the analysis line numbers.
- **Overload-collision (G1)** is the only breaking change: the `BacklogAge(ctx)` → `PendingBacklogAge(ctx)` rename, 4 mechanical call sites (E12), internal package only; rename-only step lands green before behavior work.
- **Timing flakes**: T1b/T8 use blocking stubs (deterministic lower bound: response cannot precede the 2 s probe deadline) with interval assertions only; T5/T6 use WAL second-writer backdating, no sleeps; T5's −8 s backdate vs. 4 s maxLag gives 2× margin.
- **Hard gates**: `runtime_test.go` 498 → 498 (renames only; new tests in `runtime_ready_test.go` ~200 lines); `runtime.go` 231 → ~276; `http.go` 192 → ~225 (incl. H2 ping bound); `build.go` +0 net; `metrics.go` 454 unchanged; `http_test.go` 129 → ~230; `metrics_test.go` 189 → ~220 — all under the 500-line single-file gate.
- **Duplicated-instrument rejection**: T4 (design auxiliary) registers `audit_governance.backlog_age_seconds` exactly once in the test binary (OTel rejects duplicates on the same meter), mirroring `TestObservableGauges_SurfaceInScrape` (`metrics_test.go:168`).
- **`-race` budget**: verified live at 42.7 s for the package today (E15); the new T7 workload is deadline-bounded by the same blocking stubs — the unchanged 300 s `test-race-meta` timeout holds.
- **Gate**: `make check` (gofmt / build / vet / `go test ./...` / `test-race-meta` / cli-check), zero network/Docker, SQLite + local FS + `httptest`. T3's YAML parse is the only new dependency and it is already in the module graph (E14, I6-justified; `go mod tidy` promotes it to direct).

*Verification basis: every citation re-read on this checkout (HEAD `15763e2` + uncommitted worktree); line numbers reflect the working tree as read during this spec's production; `go test -race` run live on 2026-08-08 (E15).*
