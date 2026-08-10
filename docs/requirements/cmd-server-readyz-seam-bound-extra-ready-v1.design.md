# Design — `cmd/server` /readyz seam drill: bound `extra.Ready` with `readyzProbeTimeout` and pin degraded-not-503 at the HTTP seam

**Module:** `cmd/server` · **Spec:** `docs/auto/runs/d1-readyz-seam-bound-extrare-ready-and-pin-degraded-a6d3019e/artifacts/requirements-a6d3019e/requirements.md` (REQ-1..5, AC-1..5) · **Contract:** `docs/proposals/audit-contract-batch-aero-vault.md` B3-2 (backlog > maxLag → degraded, never 503); sibling shipped specs `cmd-server-readyz-probe-timeout-v1` + `cmd-server-audit-governance-ready-degraded-v1`
**HEAD:** `15763e2` (worktree, all citations re-verified on this checkout) · **Date:** 2026-08-08 · **Hardening pass (adversarial review):** applied + verified — see §9.
**Scope lock:** exactly two production changes — (1) `http.go:66` `extra.Ready(req.Context())` → `extra.Ready(probeCtx)` (+ doc comment), (2) `build.go:113-118` gauge-callback extraction. One new test file. Nothing else moves: no `Runtime`/config/telemetry/alert/schema changes, no new endpoints, no payload changes.

---

## 1. Verification register (evidence re-checked, not trusted)

The supplied evidence was treated as untrusted; every claim was re-verified against this worktree before design.

| Evidence claim | Re-verified location (this worktree) | Verdict |
|---|---|---|
| `http.go:51-70` — `extra.Ready(req.Context())` at `:66` unbounded; `readyzProbeTimeout` (2s, `:38`) wraps only `store.Stat` (`:59-61`) | `readyzHandler` `http.go:51-70`; `const readyzProbeTimeout = 2 * time.Second` `:38`; `probeCtx, cancel := context.WithTimeout(req.Context(), readyzProbeTimeout)` `:59`; `store.Stat(probeCtx, "@healthz/probe")` `:61`; **`extra.Ready(req.Context())` `:66`**; 503 mapping `:66-68`; body `{"ok":true}` `:72-73`; `readinessGroup` `:40-48` | ✅ **exact — GAP-1 holds** |
| `http_test.go:69-127` — all three readyz tests pass `extra=nil` (`:70,:94,:114`) | `TestReadyzStorageProbeTimeout` `:69-91` (nil `:70`; elapsed bounds `:80-88`: `<1s` fail, `>5s` fail); `TestReadyzErrNotFoundIsReady` `:93-110` (nil `:94`); `TestReadyzImmediateStorageError` `:113-127` (nil `:114`); stubs `:27-56` | ✅ **exact — GAP-2 holds** |
| `audit_governance.go:75-90` → actually `:51-64` (file is 65 lines) — line drift, claim holds | `runtimeReadiness` `audit_governance.go:51-64` (composes `billingRuntime` + `auditRuntime` into `readinessGroup`, nil when both absent); `buildAuditGovernanceRuntime` `:15-49`; wiring `main.go:157` → `buildRouter` → `r.Get("/readyz", readyzHandler(repo, store, extraReady))` `http.go:101` | ⚠️ **drift confirmed as claimed; claim holds** |
| `runtime.go` `Ready` `:162-183` — Warn `:178-181` + return nil `:182` when age > maxLag; `BacklogAge` `:151-160` | `internal/auditgovernance/runtime.go` — `BacklogAge` `:151-160`; `Ready` `:162-183`: drain probe `:163-168` (error → hard fail), backlog branch `:174-182`: `ok && age > r.maxLag` → `Warn("audit governance relay degraded", …)` `:178-181` → `return nil` `:182` | ✅ **exact** |
| `runtime_test.go:415` exact (`TestRuntimeReadyDegradesOnBacklogLag`); `:473` is empty-store only, **not** terminal-only | `:415` exact (sleep-based 4.5s, not backdated); `:473` = `TestRuntimeBacklogAgeZeroWhenNoPending` — seeds **no rows**, asserts empty-store ok=false. The terminal-row (dead-lettered) case is **not** package-pinned → REQ-4's gap is real | ✅ **exact** |
| `alerts.yml:162-170` — expr `:163` `audit_governance_backlog_age_seconds > 450`; "/readyz stays 200" is comment-only at `:169` | rule group `deploy/prometheus/alerts.yml:156-169` (the **file is 169 lines** — the evidence's `:170` is past EOF; description at `:169` is the last line and the `AuditGovernanceBacklogDegraded` rule is the file's final rule, so block-scope-to-EOF slicing is valid); `alert:` `:162`; expr `:163`; `for: 10m` `:164`; severity `warning` `:166`; description `:169` contains "/readyz stays 200 (degraded)". Gauge name appears exactly twice in the file (`:157` comment, `:163` expr) — whole-file Contains is collision-free | ✅ **~exact — GAP-4 holds** |
| `build.go:113-118` anonymous gauge callback (err/!ok → 0) | `registerGauges` `build.go:104-127`; `telemetry.RegisterAuditGovernanceBacklogAgeGauge(func(ctx){ age, ok, err := auditRuntime.BacklogAge(ctx); if err != nil || !ok { return 0 }; return int64(age.Seconds()) })` at `:113-118` | ✅ **exact — GAP-3 holds** |
| "REQUEST_TIMEOUT_SECONDS 120s is AI-group only; `/readyz` has no request timeout" | `main.go:156` `aiTimeout := time.Duration(cfg.App.RequestTimeoutSec) * time.Second` → `rest.NewRouter(…aiTimeout…)` only; server `WriteTimeout` `http.go:159` = `cfg.App.WriteTimeoutSec` (60s default). Strongest true statement: **no deadline bounds `extra.Ready`** | ✅ **holds** |
| "degradedChecker payload never shipped; both acceptance texts pin `{"ok":true}`" | `http.go:72-73` byte-identical body; no degraded marker anywhere in worktree | ✅ **holds** |

**Design-load-bearing facts verified independently (not from the evidence):**

| # | Fact | Location |
|---|------|----------|
| V1 | `billing.Runtime.Ready` exists (`:136`) — the extra group can contain billing too; both share the probe budget | `internal/billing/runtime.go:136` |
| V2 | `OldestPendingAuditGovernance` predicate `delivered_at_ns=0 AND failed_at_ns=0`, **JOINs `audit_governance_bindings` on tenant** — backdating must target a bound tenant (`acme`) | `internal/repository/audit_governance_claim.go:211-223` |
| V3 | `HasPendingDrainingAuditGovernance` = EXISTS pending row JOIN binding `state='draining'` — drain-503 boundary control needs **both** a draining binding and a pending fact | `audit_governance_claim.go:225-232` |
| V4 | `ClaimAuditGovernance(ctx, owner, token, revision, limit, ttl)` → `[]AuditGovernanceFact` (`.ID` populated); `FailAuditGovernance(ctx, id, owner, token, lastErr)` sets `failed_at_ns=now` and requires exact claim (`requireGovernanceClaim` rows==1); claim SQL requires `b.revision=$N` → claim with revision **1** (matches `New`'s `applyDesiredBindings` with `cfg.Revision=1`) | `audit_governance_claim.go:20-30,182-208`; `runtime.go:73` |
| V5 | `audit_governance_outbox` has `created_at_ns INTEGER NOT NULL` (0039) and `failed_at_ns INTEGER NOT NULL DEFAULT 0` (0042) — both backdating and terminal-landing SQL are schema-valid | `migrations/sqlite/0039_…outbox.up.sql`, `0042_…terminal_failed.up.sql` |
| V6 | SQLite driver registered as `"sqlite"` (`modernc.org/sqlite`); repo sets WAL + `MaxOpenConns(1)` — the repo's own pool is serialized but a **second raw connection on the same `file:` DSN is a legal concurrent writer** (WAL), the established in-repo backdating idiom | `internal/repository/sqlite.go:11,31-33`; `internal/reconcile/lifecycle_test.go:43-72` |
| V7 | `AuditGovernanceConfig` exact fields + validation: `Validate()` requires valid `BaseURL`/`TokenURL` URLs, `Revision ≥ 1`, `HMACKey` 32..4096 bytes, `MaxLag > ClaimTTL` (`:249`), binding state ∈ {active, draining}, `ClientSecretEnv` matching `^AUDIT_GOVERNANCE_CLIENT_SECRET_`, distinct secrets | `config_audit_governance.go` struct `:18-36`, `Validate` `:241-262`, `validAuditGovernanceState` `:293` |
| V8 | Shrunk-but-valid config precedent (`runtimeConfig`/`publisherConfig`): HTTPTimeout 1 / Poll 10 / Batch 10 / ClaimTTL 3 / backoff 1→2 / MaxLag 4 / ReconcileBatch 20 / retention 3600 / cleanup 60+20 / Revision 1 / HMACKey `"audit-governance-hmac-key-32-bytes-minimum"` / binding acme+vault-audit+`AUDIT_GOVERNANCE_CLIENT_SECRET_ACME`+machine-secret | `internal/auditgovernance/runtime_test.go:39-46`; `internal/auditgovernance/http_test.go:20-28` |
| V9 | `Runtime.Close()` is non-blocking when the relay was never started (`startOnce.Do(close(done))`), bounded by `claimTTL+httpTimeout` otherwise | `runtime.go:122-132` |
| V10 | OTel gauge `audit_governance.backlog_age_seconds` (name `metrics.go:360`) exports as Prometheus `audit_governance_backlog_age_seconds` (dots→underscores) — the name alerts.yml:163 references | `internal/telemetry/metrics.go:354-365` |
| V11 | No direct YAML dep in `go.mod` (`go.yaml.in/yaml/v2` indirect at `:73`) — the parity test must be stdlib-only (I6) | `go.mod:73` |
| V12 | `readyzHandler`/`runtimeReadiness` have exactly one production call site each; symbol `auditGovernanceBacklogAgeGaugeFn` is free; test cwd for `./cmd/server/` = `cmd/server` → alerts.yml at `../../deploy/prometheus/alerts.yml` | grep; `http.go:101`; `main.go:157` |
| V13 | Baseline green: `go test ./cmd/server/ -run 'TestReadyz|TestRedirectWebUI'` → `ok` (2.0s); file sizes `http.go` 189 / `build.go` 184 / `http_test.go` 129 lines | measured this session |
| V14 | **Capture gates the outbox insert:** `governanceCaptureActive` (`audit_governance_binding.go:139-152`) returns true only for `state == 'active'`; `InsertEventWithGovernance` (`audit_governance_write.go:86-92`) and `EnqueueAuditGovernance` (`:119-122`) silently skip the outbox row when not active → a draining-start harness can never seed a pending fact (AC-3.2 defect, §9-F1) | read + empirically reproduced |
| V15 | `replaceGovernanceBindings` (`audit_governance_binding.go:89-105`) DELETEs all binding rows then re-INSERTs → the drain flip leaves exactly one draining row; `ApplyAuditGovernanceBindings(rev 2)` is the shipped rebind idiom (`runtime_test.go:440-448`) | read |
| V16 | `BacklogAge` reads `time.Since(time.Unix(0, created_at_ns).UTC())` (`runtime.go:151-160`, `billing_projection.go:153-157`) — wall clock, no monotonic hazard; the backdate is computed from the same clock → age = backdate + process-elapsed ≥ backdate, immune to NTP/clock jumps | read + measured (10×, age ∈ [7.5s, 8.5s]) |
| V17 | `repo.Ping` (`repository/sql.go:39`) is `PingContext` — a pool check, no query; it stays on `req.Context()` unbounded (SRE review precision, §9-P1) | read |

**Baseline run:** `go build ./...` + `go vet ./...` assumed green at HEAD; the readyz-focused test run above measured green. Full `make check` gate is required before merge (HARNESS).

---

## 2. Design

### D1 — One shared `probeCtx` bounds both probes (`cmd/server/http.go`, 2-line behavior change + doc comment)

```go
// readyzProbeTimeout bounds the /readyz storage probe independently of
// STORAGE_READ_TIMEOUT (30s default) and REQUEST_TIMEOUT_SECONDS (120s default):
// a wedged object store must not hold the readiness endpoint for tens of
// seconds per probe, defeating LB/orchestrator failover. The same 2s budget
// also bounds the extra readiness group (billing/audit-governance store
// queries) — a wedged DB must not hold /readyz either. The preceding
// repo.Ping is a connection-pool check (no query) and remains unbounded.
const readyzProbeTimeout = 2 * time.Second
```

`http.go:66` becomes:

```go
		if extra != nil {
			if err := extra.Ready(probeCtx); err != nil {   // was req.Context()
				http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)
				return
			}
		}
```

`defer cancel()` at `:60` still covers both probes; `probeCtx` is created before the storage probe, so the extra group gets the remaining budget after a fast storage probe (F2 semantics preserved). No other production change in this file. **Precision (SRE review, §9-P1):** `repo.Ping(req.Context())` at `:55` is a connection-pool check (no query) and stays unbounded — the "≤ 2s" claim covers the storage probe and the extra group's store queries, not Ping.

Resulting behavior matrix (status/body semantics byte-identical to today, now deadline-bounded):

| Probe outcome | Status | Body |
|---|---|---|
| storage probe error (non-NotFound) | 503 | "storage unavailable" (unchanged) |
| `extra.Ready` error — drain-in-progress, genuine store failure, **or probe deadline** | 503 | "runtime dependency unavailable" (unchanged mapping, now ≤ 2s) |
| all probes succeed (incl. degraded backlog > maxLag — `Ready` returns nil) | 200 | `{"ok":true}` (byte-identical) |

### D2 — Gauge callback extraction (`cmd/server/build.go`, zero behavior change)

```go
// auditGovernanceBacklogAgeGaugeFn returns the backlog-age gauge callback.
// Terminal (dead-lettered) rows are excluded by the store query, so a fully
// dead-lettered backlog reports 0; store errors also report 0 (fail-open
// gauge — the degraded signal is alert-driven, B3-2).
func auditGovernanceBacklogAgeGaugeFn(rt *auditgovernance.Runtime) func(context.Context) int64 {
	return func(ctx context.Context) int64 {
		age, ok, err := rt.BacklogAge(ctx)
		if err != nil || !ok {
			return 0
		}
		return int64(age.Seconds())
	}
}
```

`registerGauges` body becomes:

```go
	if auditRuntime != nil {
		telemetry.RegisterAuditGovernanceBacklogAgeGauge(auditGovernanceBacklogAgeGaugeFn(auditRuntime))
	}
```

Closure semantics preserved exactly (same three lines, same return logic). The extraction is the minimal testability enabler for AC-4; asserting through an OTel `/metrics` scrape is rejected (global single-shot instrument registration, and `registerGauges` would drag queue-depth/storage gauges into every test).

### D3 — New drill test file `cmd/server/readyz_drill_test.go` (package `main`, ≤ 500-line hard gate)

Reuses the shipped partial-stub idiom (`http_test.go:27-56`) for the wedged/immediate/healthy extra cases:

```go
// blockingReadyChecker emulates a wedged audit-governance store: Ready blocks
// on the caller context (the OldestPendingAuditGovernance hang shape) and can
// only return after the probe deadline fires.
type blockingReadyChecker struct{ readinessChecker }

func (c *blockingReadyChecker) Ready(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type errorReadyChecker struct{ readinessChecker }

func (c *errorReadyChecker) Ready(context.Context) error { return errors.New("injected extra failure") }

type okReadyChecker struct{ readinessChecker }

func (c *okReadyChecker) Ready(context.Context) error { return nil }
```

Runtime-backed harness (REQ-3/REQ-4) — real `Runtime` + real SQLite, no sleeps (deterministic SQL backdating via a second raw connection, V6):

```go
// alertLagThresholdSeconds pins the alerts.yml expr threshold to the shipped
// AUDIT_GOVERNANCE_MAX_LAG_SECONDS default (900, config_audit_governance.go:66)
// × 0.5 — the B3-2 degraded alert half-lag.
const alertLagThresholdSeconds = 450

func newReadyzDrillRuntime(t *testing.T) (*auditgovernance.Runtime, auditgovernance.Store, string) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "drill.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() }) // registered FIRST
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.AuditGovernanceConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1", TokenURL: "http://127.0.0.1:1/token",
		HMACKey: "audit-governance-hmac-key-32-bytes-minimum", Revision: 1,
		HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10, ClaimTTLSeconds: 3,
		InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 4,
		ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600,
		CleanupIntervalSeconds: 60, CleanupBatchSize: 20,
		Bindings: []config.AuditGovernanceBinding{{
			TenantID: "acme", ClientID: "vault-audit",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_ACME",
			ClientSecret:    "machine-secret", State: "active", // ALWAYS active (V14)
		}},
	}
	if err := cfg.Validate(); err != nil { // MaxLag 4 > ClaimTTL 3; state ∈ {active,draining}
		t.Fatal(err)
	}
	store, ok := repo.(auditgovernance.Store)
	if !ok {
		t.Fatal("repository is not an audit governance store")
	}
	runtime, err := auditgovernance.New(cfg, store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup runs LAST-registered FIRST ⇒ runtime.Close() executes before
	// repo.Close() (LIFO teardown — repo registered first above). The relay
	// is never started in these tests, so Close() is non-blocking (V9); the
	// reverse registration would close the store the runtime still queries
	// (use-after-close under -race, FM-7).
	t.Cleanup(runtime.Close) // registered SECOND ⇒ runs FIRST
	return runtime, store, dsn
}

// seedPendingDrillFact inserts one pending fact for tenant acme via the
// public store API (shape of runtime_test.go:415). Requires an ACTIVE
// binding — capture skips the outbox insert otherwise (V14).
func seedPendingDrillFact(t *testing.T, ctx context.Context, store auditgovernance.Store) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: now,
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
}

// backdateDrillFact rewrites created_at_ns on the seeded pending row so the
// backlog age crosses maxLag deterministically — no sleeps. WAL permits a
// second writer (V6); the repo serializes only its own pool.
func backdateDrillFact(t *testing.T, dsn string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn) // same driver name as internal/repository
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cut := time.Now().UTC().Add(-age).UnixNano()
	if _, err := db.Exec(
		`UPDATE audit_governance_outbox SET created_at_ns = ?`+
			` WHERE tenant_id = 'acme' AND delivered_at_ns = 0 AND failed_at_ns = 0`, cut); err != nil {
		t.Fatal(err)
	}
}
```

Test list and exact assertions in §6.

### D4 — No degraded payload marker

Body stays byte-identical `{"ok":true}`. Degradation is carried by the gauge + alert only (shipped B3-2 design; the sibling spec's `degradedChecker` payload was never shipped and stays out of scope).

### D5 — Stdlib-only alerts.yml parity pin

`os.ReadFile` + `strings` only (V11, I6). No YAML dependency promotion.

---

## 3. API changes & compatibility constraints

**External/HTTP API — no contract change.** `/readyz` keeps its exact response contract for every outcome: `200 {"ok":true}` and the three 503 bodies (`database unavailable` / `storage unavailable` / `runtime dependency unavailable`) are byte-identical. The single observable change is *latency bounding*: a wedged extra probe now 503s in ≤ 2s instead of holding the connection until client disconnect or `APP_WRITE_TIMEOUT` (60s default). LB/orchestrator probes see identical success/failure semantics with strictly faster failover — the intended improvement, not a behavior break.

**Internal Go API — no exported changes.**
- `readinessChecker` interface, `readinessGroup`, `readyzHandler`, `runtimeReadiness` signatures unchanged. `billing.Runtime.Ready` (V1) is untouched and now implicitly bounded by the shared probe ctx.
- New unexported package-level func `auditGovernanceBacklogAgeGaugeFn(*auditgovernance.Runtime) func(context.Context) int64` in `package main` (V12: symbol free).
- No config keys, no telemetry instruments, no alert rules, no DB schema, no migrations.

**Compatibility constraints (must hold):**
- `http_test.go:69-127` (all three shipped readyz tests, `extra=nil` path) stay green with **zero edits** — the extra-nil branch is byte-identical after D1.
- `runtime_test.go:415` and `:473` stay green — no `internal/auditgovernance` production edits at all.
- Hard gates: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` (SQLite + local FS, zero network beyond the in-process handler), single-file ≤ 500 lines (**measured: `readyz_drill_test.go` = 357 lines** — under the gate; `http.go` 189→191, `build.go` 184→192).
- No `go.mod` changes (I6). No testify or other assertion frameworks.
- `make check` does not run `go mod tidy`/`golines` — no tidy churn expected (no new imports beyond stdlib + existing packages).
- Shared-budget semantics (F2) preserved and documented: with a wedged *storage* probe, the storage 503 fires first and the extra group never runs — existing behavior, unchanged.

---

## 4. Failure modes

| # | Failure mode | Behavior after this design | Mitigation / note |
|---|--------------|----------------------------|-------------------|
| FM-1 | Wedged audit-governance store query inside `extra.Ready` (`OldestPendingAuditGovernance` hang) | `/readyz` returns 503 "runtime dependency unavailable" at the 2s probe deadline — **the fix** (was: unbounded, up to 60s WriteTimeout or client disconnect) | AC-2.1 pins elapsed ∈ [1s, 5s] |
| FM-2 | Wedged *storage* probe | Storage 503 at 2s fires first; `extra.Ready` never runs (shared budget, F2) — unchanged | Documented; AC-2.1 deliberately uses `notFoundStatStorage` so the full budget reaches the extra probe |
| FM-3 | Deadline expiry vs. drain-in-progress vs. genuine store error | All three map to the same 503 body "runtime dependency unavailable" — indistinguishable at HTTP level | Accepted: the gauge + alert differentiate degraded vs. hard-fail; changing bodies is out of scope (D2/D4) |
| FM-4 | `extra.Ready` returns nil while backlog > maxLag (degraded) | 200 `{"ok":true}` within budget; operator signal = `audit_governance_backlog_age_seconds` gauge + alert | AC-3.1 pins degraded-200 at the seam; AC-5.1 pins the alert coupling |
| FM-5 | Backlog-age gauge callback store error | Callback returns 0 (fail-open → alert suppressed) — shipped B3-2 behavior, unchanged by the extraction | AC-4.1/4.2 keep the ok/err branches pinned; a semantic change here is out of scope |
| FM-6 | **AC-4.2 spec flake (found in design):** `int64(age.Seconds())` truncates toward zero — a truly fresh (< 1s old) pending row yields gauge 0, failing the spec's `> 0` control | — | **Correction:** the "live-row" phase backdates the row by a deterministic 2s (below maxLag 4s → still 200, above the truncation floor → gauge ≥ 2 > 0). Intent preserved: the callback reports real ages, not a constant zero |
| FM-7 | **Teardown ordering spec ambiguity (found in design):** `t.Cleanup` runs last-registered-first; the spec's "runtime.Close() registered before repo.Close()" would, taken literally, execute `repo.Close()` first (use-after-close under `-race`) | — | **Correction:** register `repo.Close()` first, `runtime.Close()` second (D3 code) ⇒ runtime closes first. Relay never started (no `Start`), so `Close()` is non-blocking (V9) |
| FM-8 | Timing flake in AC-2.1's elapsed bounds | Blocking stub makes the lower bound deterministic (response cannot precede the 2s deadline); the ≤ 5s upper bound only proves boundedness — same idiom as the shipped `TestReadyzStorageProbeTimeout` (`http_test.go:80-88`) | — |
| FM-9 | `-race` hazard from the backdating second connection | Second connection is a separate `*sql.DB`; the raw `UPDATE` touches a disjoint column of one row; WAL + short transaction window | Standard in-repo idiom (V6) |
| FM-10 | AC-3.2 passes vacuously (a bug skipping `extra` entirely would pass AC-3.1) | AC-3.2 is the negative control: drain → `Ready` errors → 503 proves the seam genuinely consults `extra` |
| FM-11 | **AC-3.2 seeding defect (found in review, reproduced, fixed):** the design seeded the pending fact with a draining binding from the start — but capture gates the outbox insert on `state='active'` (V14), so zero rows land, `HasPendingDrainingAuditGovernance` stays false, and the 503 assertion fails (test would return 200) | **Correction (implemented):** harness always binds `active`; AC-3.2 seeds first, then flips via `ApplyAuditGovernanceBindings(ctx, 2, "acme-v2", [{acme, draining}])` (V15, `runtime_test.go:440-448` idiom) | — |

---

## 5. Migration & rollback

**No data, schema, config, or alert migration.** This is a code-only change with a fully additive test file.

Migration steps (implementation order):

1. **Edit `cmd/server/http.go`** — extend the `readyzProbeTimeout` doc comment (`:34-38`); change `:66` to `extra.Ready(probeCtx)`.
2. **Edit `cmd/server/build.go`** — add `auditGovernanceBacklogAgeGaugeFn`; replace the inline closure at `:113-118` with `telemetry.RegisterAuditGovernanceBacklogAgeGauge(auditGovernanceBacklogAgeGaugeFn(auditRuntime))`.
3. **Add `cmd/server/readyz_drill_test.go`** — harness (D3) + the seven tests of §6 (applied + verified in this hardening pass). `http_test.go` untouched.
4. **Gate:** `make check` (gofmt/build/vet/test), then `make test-race ./cmd/server/...` (or the repo's race target) for the new file — the drill exercises real SQLite + runtime teardown ordering.
5. **Commit** as one change; **deploy** as a routine release. No ordering constraints with other components (no cross-service contract).
6. **Rollback:** `git revert` of the commit. `http.go` reverts to the unbounded `req.Context()` (pre-change state — the pre-existing gap, no data impact either direction); `build.go` reverts to the inline closure (identical semantics); the test file is additive and can stay or revert with the commit.

---

## 6. Testable acceptance mapping (AC → test → assertions → gate)

| AC (spec) | Test (all in `cmd/server/readyz_drill_test.go`) | Exact assertions | Gate |
|---|---|---|---|
| AC-1.1 code shape | — (review/grep pin) | `http.go:66` passes `probeCtx`; `defer cancel()` at `:60` still covers both probes; comment names both probes | `git diff` review |
| AC-1.2 no regression | existing `http_test.go:69-127` | All three readyz tests green with **zero edits** | `go test ./cmd/server/ -run TestReadyz` |
| AC-2.1 `TestReadyzExtraProbeTimeout` | `readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, &blockingReadyChecker{})` (notFound storage ⇒ full ~2s budget reaches the extra probe) | status **503**; body contains `"runtime dependency unavailable"`; `elapsed ∈ [1s, 5s]` — identical bounds to `TestReadyzStorageProbeTimeout` (`http_test.go:80-88`) | `go test ./cmd/server/ -run TestReadyzExtraProbeTimeout -count=1` |
| AC-2.2 `TestReadyzImmediateExtraError` | `errorReadyChecker` | 503, same body, `elapsed < 1s` — deadline wrap neither delays nor swallows immediate errors (mirror `TestReadyzImmediateStorageError`) | same |
| AC-2.3 `TestReadyzHealthyExtra200` | `okReadyChecker` | **200**, body exactly `{"ok":true}`, `elapsed < 1s` (mirror `TestReadyzErrNotFoundIsReady`) | same |
| AC-3.1 `TestReadyzBacklogLagDegradesNot503` | `newReadyzDrillRuntime(t)` (binding active); `seedPendingDrillFact(t, ctx, store)`; `backdateDrillFact(t, dsn, 8*time.Second)` (8s > maxLag 4s, 2× margin); `extra := runtimeReadiness(nil, runtime)`; `h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)` | Pre-assert: `runtime.BacklogAge(ctx)` → `ok==true && age > 4*time.Second`. Then: status **200**, body exactly `{"ok":true}` (no marker — D4), `elapsed < 1s` (well within the 2s budget), never 503 | `go test ./cmd/server/ -run TestReadyzBacklogLagDegradesNot503 -count=1` |
| AC-3.2 `TestReadyzDrainStill503` (boundary control) | harness (binding active); `seedPendingDrillFact`; **then flip the binding**: `store.ApplyAuditGovernanceBindings(ctx, 2, "acme-v2", [{acme, draining}])` (V15, `runtime_test.go:440-448` idiom) — seeding under a draining binding would land zero rows (FM-11/V14) | `Ready` errors on `HasPendingDrainingAuditGovernance` → **503**, body contains `"runtime dependency unavailable"`, `elapsed < 1s` — pins the exact degraded/503 boundary and that `extra` is genuinely consulted | same |
| AC-4.1 `TestReadyzDeadLetteredBacklog200AndGaugeZero` | harness active; **phase 0 — EMPTY store** (no rows at all, terminal pin at the seam): assert `BacklogAge ok==false`, gauge 0, readyz 200; then seed one fact and land it terminal via the public lease-fenced API (V4): `facts, err := store.ClaimAuditGovernance(ctx, "acme", "tok", 1, 10, time.Minute)` then `store.FailAuditGovernance(ctx, facts[0].ID, "acme", "tok", "dead")` (row now `failed_at_ns != 0`, `delivered_at_ns = 0` — dead-lettered, never re-claimed) | phase 0 **and** phase 1: `runtime.BacklogAge(ctx)` → `ok==false`; `auditGovernanceBacklogAgeGaugeFn(runtime)(ctx) == 0`; handler **200**, body exactly `{"ok":true}`, `elapsed < 1s` | same |
| AC-4.2 (live-row control, phase 2 of AC-4.1) | insert one fresh pending fact; `backdateDrillFact(t, dsn, 2*time.Second)` (**spec correction, FM-6**: 2s is below maxLag 4s → handler stays 200, above the `int64(Seconds())` truncation floor → gauge deterministically ≥ 2 — age = 2s + process-elapsed ≥ 2s at read, V16) | gauge fn returns `> 0` (callback reports real ages, not a constant zero); handler still **200** | same |
| AC-5.1 `TestAlertsYMLAuditGovernanceExprParity` | `os.ReadFile(filepath.Join("..", "..", "deploy", "prometheus", "alerts.yml"))` (V12); stdlib `strings` only (V11); block-scope to the substring from `alert: AuditGovernanceBacklogDegraded` to EOF (gauge name appears exactly twice in the file — `:157` comment, `:163` expr — whole-file Contains is collision-free) | contains `expr: audit_governance_backlog_age_seconds > ` + `strconv.Itoa(alertLagThresholdSeconds)` (name == exported gauge name per V10; threshold == constant-pinned default 900 × 0.5); contains `severity: warning`; contains `/readyz stays 200` | `go test ./cmd/server/ -run TestAlertsYMLAuditGovernanceExprParity -count=1` |

**Effort:** 2 production edits (~10 changed lines total) + one new test file (**measured 357 lines** incl. harness — under the 500-line hard gate). Timing budget: AC-2.1 ≈ 2s; all runtime-backed tests ≈ 1-3s each (no sleeps — backdating is SQL-side; measured 0.76-0.81s each on this worktree). Consistent with the direction's effort 2.

---

## 7. Prior-art disposition

| Prior artifact | Relation | Disposition |
|---|---|---|
| `cmd-server-readyz-probe-timeout-v1` (shipped) | created `readyzProbeTimeout` + `probeCtx` for the storage probe only; explicitly kept `extra.Ready` on the raw ctx | **Superseded by this drill** for the extra group — D1 extends the same budget; storage-probe semantics untouched |
| `cmd-server-audit-governance-ready-degraded-v1` (shipped) | `Runtime.Ready` degrade + gauge + 450s alert; its proposed `degradedChecker` payload was never shipped | **Untouched**; this drill pins its HTTP-seam consequences (AC-3.1) and its gauge callback (AC-4) |
| `internal-auditgovernance-terminal-classification-*` / B3-1 | `failed_at_ns` terminal column (0042), claim/Fail API | **Reused as the dead-letter landing path** (AC-4.1, V4) — no edits |
| `runtime_test.go:415` sleep-based lag test | package-level degraded pin | **Kept as-is**; the seam drill uses deterministic SQL backdating instead (no sleep budget) |

## 8. Files changed (complete list)

| File | Change |
|---|---|
| `cmd/server/http.go` | doc comment `:34-40` (Ping-precision, §9-P1); `:66` `req.Context()` → `probeCtx` (2 lines) — **applied + verified** |
| `cmd/server/build.go` | new `auditGovernanceBacklogAgeGaugeFn` (8 lines + doc); `:113` call site → extracted func (1 line) — **applied + verified** |
| `cmd/server/readyz_drill_test.go` | **new** — harness + 7 tests, **measured 357 lines** (≤ 500 gate, package `main`) — **applied + verified** |
| `docs/requirements/cmd-server-readyz-seam-bound-extra-ready-v1.design.md` | this document |

*Verification basis: every direction citation and every spec claim re-checked against this worktree (register §1); line numbers reflect the working tree as read during this design's production. Spec corrections folded in: FM-6 (AC-4.2 truncation flake → deterministic 2s backdate), FM-7 (t.Cleanup LIFO registration order), and FM-11 (AC-3.2 draining-seed no-op → seed-active-then-flip).*

## 9. Hardening audit (adversarial review) — findings, corrections, evidence

Audit dimensions per task: deterministic SQL backdating · drain-503 boundary · truncation floor · t.Cleanup order under -race · 500-line gate · terminal (empty-store) pinning. All findings verified empirically on this worktree (`go test -race`, real SQLite); the implementation above is the verified form.

### F1 — AC-3.2 drain seeding defect (found in review; independently confirmed by the SQLite reviewer)

**Defect:** the original design's AC-3.2 harness (`state="draining"` + seed) lands **zero outbox rows** — capture gates the insert on `state='active'` (V14: `governanceCaptureActive` `audit_governance_binding.go:139-152`; `InsertEventWithGovernance` `audit_governance_write.go:86-92`; `EnqueueAuditGovernance` `:119-122` silently skip). `HasPendingDrainingAuditGovernance` → false → `Ready` nil → handler **200, test fails**. `New` cannot rescue it: seeding pre-`New` errors on the unbound-backlog check.

**Empirical proof (throwaway test, deleted):** draining binding + `InsertEventWithGovernance` → event row written, `draining-with-pending=false`.

**Fix (implemented, passing):** harness always binds `active`; AC-3.2 seeds then flips via `ApplyAuditGovernanceBindings(ctx, 2, "acme-v2", [{acme, draining}])` — `replaceGovernanceBindings` deletes-then-inserts (V15), leaving exactly one draining row; `runtime_test.go:440-448` is the shipped idiom. Verified: 503 + body + < 1s in 0.76s of test time.

### F2 — terminal-case pinning: empty store was uncovered at the seam

The drill file originally exercised only the fresh-store (pending rows) and dead-lettered shapes; the empty store was pinned only at package level (`runtime_test.go:473`). **Hardened:** AC-4.1 gains phase 0 — pristine store: `BacklogAge ok==false`, gauge 0, readyz 200 `{"ok":true}` < 1s — before the seed/claim/fail phase. Both terminal shapes (no rows, and rows-but-none-pending) now pinned at the HTTP seam. Verified: all three phases pass.

### V1 — 8s SQL backdating: deterministic, no sleeps, -race clean (confirmed)

- `backdateDrillFact` opens a second raw `sql.Open("sqlite", dsn)` — same driver registered by `internal/repository` (`_ "modernc.org/sqlite"`); WAL is a persistent file property so the second connection inherits it; separate pools share no Go memory → race-detector clean.
- Age math is wall-clock delta (`time.Since(time.Unix(0, ns).UTC())`, V16): age = backdate + process-elapsed ≥ backdate — immune to NTP/clock jumps; the 8s backdate keeps a 4s margin over maxLag 4s before even the pre-assert could move.
- SQLite reviewer measured: 10× iterations age ∈ [7.5s, 8.5s]; probe SELECT < 100ms even with a held write tx (WAL reader/writer independence); accidental contention fails fast at busy_timeout=0 (~470µs) — loud failure, never a hang.
- Verified on this worktree: 10 consecutive `-count=10` drill runs green; full `cmd/server` package under `-race` green (17.7s).

### V2 — truncation-floor choice (2s backdate vs maxLag 4s): sound (confirmed)

age at read = 2s + elapsed ≥ 2s ⇒ `int64(age.Seconds())` ≥ 2 deterministically, 1s above the floor. Margin to maxLag: 2s of process-elapsed — and even crossing it is assertion-neutral (degraded → nil → 200). A 1s backdate would sit on the floor; 3s would halve the maxLag margin — 2s is the correct midpoint. Verified: phase-2 gauge > 0 across all runs.

### V3 — t.Cleanup order under -race: correct (confirmed)

LIFO: repo.Close registered first, runtime.Close second ⇒ runtime closes before the store it queries; relay never started ⇒ `Close()` non-blocking (V9). Error paths need no manual closes — repo cleanup registered immediately after `Open` runs even after `t.Fatal`. Verified: full package `-race` green, no use-after-close.

### V4 — line gate: measured 357 ≤ 500 (confirmed)

`readyz_drill_test.go` = 357 lines (gofmt-clean); `http.go` 191, `build.go` 192 (design predicted 191/195).

### P1 — doc-comment precision (SRE review, applied)

`repo.Ping(req.Context())` (`http.go:55`) is a pool check (no query, V17) and stays unbounded — the "≤ 2s for a wedged DB" claim covers the storage probe and the extra group's store queries, not Ping. Comment tightened accordingly; behavior unchanged.

### Gate evidence (this worktree, after hardening)

`gofmt -l` clean · `go build ./...` ok · `go vet ./...` ok · `go test ./...` exit 0 · `go test -race ./cmd/server/` ok · drill tests `-count=10` ok · all three shipped `TestReadyz*` green with zero edits (AC-1.2).
