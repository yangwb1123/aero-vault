# Requirements: Renew the singleton lease while the guarded action runs (heartbeat)

- **Status:** v1 (evidence-backed; every citation re-verified against the repository at commit `acfaaf4`)
- **Module:** `internal/cluster` (+ wiring in `internal/reconcile` — the four singleton-gated sweep jobs)
- **Direction:** "Renew the singleton lease while the guarded action runs (heartbeat), eliminating the long-sweep split-brain window"
- **Source analysis:** `docs/auto/analyses/internal-cluster-56f4b39c.json` (direction 1 of 3) · run: `docs/auto/runs/renew-the-singleton-lease-while-the-guarded-acti-7cf7f4fd`
- **Scope boundary:** heartbeat renewal of the cluster lease while the guarded action runs, and adoption by the four reconcile-family sweeps. **Out of scope:** lease release/Stop paths, clock-skew independence, TTL clamping, any repository/SQL/migration change, any config/env change, any change to `Guard`'s existing semantics.

---

## 1. Evidence verification register

Every citation in the direction was re-checked against the working tree. All hold, with minor snapshot line drift corrected.

| # | Cited claim | Verified location | Verdict |
|---|-------------|-------------------|---------|
| E1 | Guard acquires the lease exactly once; no renewal while fn runs | `internal/cluster/singleton.go:56-62` (`held, err := s.repo.AcquireLease(ctx, s.lease, s.holder, ttl)`), `:65` (`fn(ctx)` — synchronous, no renew loop between acquire and fn) | ✅ (cited `:56-63`; `fn(ctx)` is at `:65`) |
| E2 | Docstring: ttl ~2× interval, takeover only on expiry | `internal/cluster/singleton.go:50` ("ttl should be ~2× the action's interval so a dead holder's lease frees and another replica takes over after ~2 rounds"), `:52-53` ("A lease error is logged and fn is skipped — fail-safe: better to skip a destructive sweep than to run it twice") | ✅ (cited `:49-54`) |
| E3 | Reconcile sweep: ttl = 2×interval; renewal only at next round start | `internal/reconcile/job.go:97-101` — `maybeSweep` docstring ("The lease TTL is 2× the interval so the holder keeps renewing each round") and `j.singleton.Guard(ctx, 2*j.interval, j.sweep)` at `:101` | ✅ (cited `:98-101`) |
| E4 | Same pattern in retention / lifecycle / upload GC | `internal/reconcile/retention.go:85-86` (`r.singleton.Guard(ctx, 2*r.interval, r.sweep)` at `:86`) · `internal/reconcile/lifecycle.go:66-67` (`:67`) · `internal/reconcile/upload_gc.go:75-76` (`:76`) | ✅ exact |
| E5 | Sweep duration unbounded: pages of 200, per-object Stat | `internal/reconcile/job.go:133-163` (`sweepOrphanRows`): `j.repo.ListObjects(ctx, tenant, bucket, "", marker, 200)` at `:140`, per-object `j.store.Stat(ctx, obj.StorageKey)` at `:147`, `HasMore`/`NextMarker` pagination at `:156-161`. Additionally `scrubAll` (`internal/reconcile/scrub.go:24-52`) reads every object's content per sweep (`scrubObject` → `store.Get` + MD5) — same unbounded profile | ✅ (cited `:150-168`; loop spans `:133-163` — drift, substance exact) |
| E6 | Documented guarantee "Prevents duplicate destructive sweeps" | `docs/configuration.md:379` — `RECONCILE_CLUSTER_SINGLETON` row: "Run reconcile and lifecycle sweeps on only one instance at a time, using a DB advisory `leases` table. Prevents duplicate destructive sweeps when running multiple replicas." | ✅ exact |

## 2. Additional verified facts (beyond the direction's citations)

| # | Fact | Location |
|---|------|----------|
| F1 | **Renew-own already exists in the repository.** `AcquireLease` "atomically grants **or renews** the named singleton lease to holder"; "a holder may always renew its own lease; a different holder may take over only once the existing lease has expired". The renew-own branch is the UPDATE's `holder=$4 OR expires_at < $5` | `internal/repository/leases.go:9-15` (doc), `:23` (SQL), two-step UPDATE-then-INSERT `:19-41` |
| F2 | `LeaseStore` (the interface `Singleton` depends on) exposes only `AcquireLease` — sufficient for heartbeat; interface unchanged; `repository.Repository` still satisfies it | `internal/cluster/singleton.go:15-17` |
| F3 | All four sweeps are single-threaded: `Run` blocks inside `maybeSweep` until the sweep returns, so lease-hold duration = full sweep duration | `job.go:84-95`, `retention.go:70-81`, `lifecycle.go:51-62`, `upload_gc.go:61-72` |
| F4 | Existing cluster tests pin `Guard`'s exact lease-call counts on `fakeLease` (`singleton_test.go:11-24`): `TestSingleton_DisabledAlwaysRuns` (0 calls), `TestSingleton_EnabledRunsWhenHeld` (1 call), `TestSingleton_EnabledSkipsWhenNotHeld`, `TestSingleton_LeaseErrorFailsSafe` | `internal/cluster/singleton_test.go:26-68` |
| F5 | Real-SQLite singleton takeover semantics already tested: node-A holds and sweeps; node-B skipped while lease valid; node-A renew-owns on its next round | `internal/reconcile/job_test.go:379-410` (`TestJobSweep_ClusterSingleton_OnlyOneRuns`) |
| F6 | Production wiring: all four jobs get the singleton with one per-instance `instanceID` (uuid) | `cmd/server/workers.go:114-121` |
| F7 | `Guard` validates neither ttl nor cadence today — FR-3's validation applies only to the new renewing mode (direction 3's ttl clamp stays out of scope) | `internal/cluster/singleton.go:56-65` |
| F8 | **Cancellation is safe against false orphans:** `sweepOrphanRows` treats only `errors.Is(err, storage.ErrNotFound)` as an orphan; a cancelled ctx yields `ctx.Err()`, never `ErrNotFound` — lease-loss cancellation cannot misclassify rows as orphaned | `internal/reconcile/job.go:147-149` |

## 3. Problem statement

`Singleton.Guard` acquires the lease exactly once (`singleton.go:56`), then runs `fn` synchronously with no renewal (`:65`). The four reconcile-family jobs gate their destructive sweeps with `Guard(ctx, 2*interval, sweep)` (E3/E4) and their `Run` loops block in `maybeSweep` until the sweep finishes (F3). The lease is therefore renewed only at the start of the next round. Sweep duration is unbounded (E5): `sweepOrphanRows` walks every bucket in pages of 200 with a per-object `store.Stat` round-trip, and the opt-in MD5 scrub reads every object's full content — a large tenant, a slow S3 `List`/`Stat`, or the scrub can easily exceed `2 × RECONCILE_INTERVAL_MINUTES`.

When the TTL passes mid-sweep, `AcquireLease`'s take-over-on-expiry branch (F1, `expires_at < now`) grants a second replica the same lease; that replica then runs the same destructive sweep concurrently — orphan-blob deletion with `RECONCILE_DELETE_ORPHAN_BLOBS=true`, retention purge of soft-deleted rows and their blobs, lifecycle hard deletes, upload GC purge — violating the documented guarantee "Prevents duplicate destructive sweeps" (`configuration.md:379`, E6). This is the slow-holder case the design must protect.

The fix: **heartbeat renewal** — renew the lease while `fn` runs, using the renew-own path that already exists in the repository (F1). No SQL, migration, or interface change is required.

## 4. Functional requirements

### FR-1 — Renewing guard primitive (additive)

`Singleton` gains a renewing mode, e.g. `GuardRenewing(ctx context.Context, ttl, renewEvery time.Duration, fn func(context.Context))` (final name at design's discretion). `Guard` is **unchanged** — same signature, same semantics, same tests (F4).

- Acquire the lease exactly once up front, with the same fail-safe behavior as `Guard`: acquire error → warn log + skip `fn`; not held → skip `fn` (never run without the lease).
- While `fn` runs, renew the lease on a steady cadence `renewEvery` by calling the existing `repo.AcquireLease(ctx, lease, holder, ttl)` — the holder's renew-own branch returns true regardless of the remaining TTL (F1).
- `fn` runs **synchronously in the calling goroutine** (the single-threaded `Run` model is preserved, F3); the renewal loop runs in a helper goroutine.
- `fn` receives a child context derived from the caller's `ctx`; the renewal loop passes that same child context to `AcquireLease` so an in-flight renewal aborts as soon as `fn` completes or is cancelled.
- `GuardRenewing` returns only after `fn` has returned **and** the renewal goroutine has exited (no goroutine or ticker leak; no `AcquireLease` call is observed after `GuardRenewing` returns).
- Disabled singleton (`Enable` never called): `GuardRenewing` runs `fn(ctx)` directly and never touches the lease — identical to `Guard` (I5).

### FR-2 — Exclusivity invariant (the split-brain fix)

- While holder A's `fn` runs and A's renewals succeed, any other replica's `Guard`/`GuardRenewing` acquire must return not-held, and that replica's `fn` must never run concurrently.
- **Any failed renewal** — an error from `AcquireLease`, or a `false` result (the lease was taken over after expiry) — means A no longer holds a valid lease. A must treat itself as having lost leadership: cancel `fn`'s context, wait for `fn` to exit, then return. `fn` is **never restarted**.
- This is the fail-safe posture already documented on `Guard` ("better to skip a destructive sweep than to run it twice", `singleton.go:52-53`): on lease loss the remaining sweep work is skipped, never executed under a foreign lease. Cancellation cannot misclassify rows as orphans (F8).
- Outer `ctx` cancellation propagates to `fn` as today (child context); `GuardRenewing` waits for `fn` to exit and returns.

### FR-3 — Cadence contract

- `GuardRenewing` must enforce `0 < renewEvery < ttl`; a violation (`renewEvery <= 0` or `renewEvery >= ttl`) is a programming error and must **panic** with a message naming the offending parameters — fail-fast rather than silently running without heartbeat (F7).
- Recommended production cadence: `renewEvery <= ttl/2`, i.e. at least two renewal attempts per TTL window, so one missed or slow attempt is survivable (the lease expires only when a full `ttl` passes with no successful renewal).

### FR-4 — Production wiring (the four reconcile-family sweeps)

The four singleton-gated sweeps switch from `Guard` to the renewing guard, keeping the existing ttl and lease names:

| Call site | Today (verified) | After |
|-----------|------------------|-------|
| `Job.maybeSweep` | `Guard(ctx, 2*j.interval, j.sweep)` (`job.go:101`) | `GuardRenewing(ctx, 2*j.interval, j.interval/2, j.sweep)` |
| `LifecycleJob.maybeSweep` | `Guard(ctx, 2*l.interval, l.sweep)` (`lifecycle.go:67`) | `GuardRenewing(ctx, 2*l.interval, l.interval/2, l.sweep)` |
| `RetentionJob.maybeSweep` | `Guard(ctx, 2*r.interval, r.sweep)` (`retention.go:86`) | `GuardRenewing(ctx, 2*r.interval, r.interval/2, r.sweep)` |
| `UploadGCJob.maybeSweep` | `Guard(ctx, 2*j.interval, j.sweep)` (`upload_gc.go:76`) | `GuardRenewing(ctx, 2*j.interval, j.interval/2, j.sweep)` |

- `ttl = 2 × interval` unchanged; `renewEvery = interval/2 = ttl/4` ⇒ **4 renewal attempts per TTL window** (satisfies FR-3 with 2× headroom).
- No config/env changes: `RECONCILE_INTERVAL_MINUTES`, `RECONCILE_CLUSTER_SINGLETON`, the four lease names (`reconcile-sweep`, `lifecycle-sweep`, `retention-gc`, `upload-gc`), and the per-instance `instanceID` holder (F6) are all unchanged. Singleton-disabled and single-replica behavior is identical to today (I5).
- Short sweeps behave exactly as today (one acquire, no renewal needed, lease expires naturally after the round) — `TestJobSweep_ClusterSingleton_OnlyOneRuns` (F5) must stay green.

### FR-5 — Observability

- Initial acquire failure: existing warn `"singleton: acquire lease"` (unchanged).
- Renewal failure (error or not-held): one warn log naming lease, holder, and outcome, followed by the FR-2 cancellation path.
- Successful renewals: **no** log (4× per TTL window per job would spam); debug level at design's discretion.

### FR-6 — Documentation

- `Singleton` docstring (`singleton.go:49-54`): document the renewing mode and its contract (FR-1…FR-3).
- `docs/configuration.md:379`: extend the `RECONCILE_CLUSTER_SINGLETON` description to state that the lease is renewed while a sweep runs, so a sweep longer than the lease TTL does not hand leadership to a second replica mid-run — making the existing "Prevents duplicate destructive sweeps" claim actually enforced.

### FR-7 — Engineering gates

- Files ≤ 500 lines (all touched files currently ≤ 130; `singleton.go` is 66), functions ≤ 50 lines, cyclomatic complexity ≤ 10 (AGENTS.md §0).
- Stdlib only — no new `go.mod` dependencies (I6); tests keep using `testing` only.
- No repository/SQL/migration/`LeaseStore` changes (I1/I2 untouched); `Guard`'s existing tests pass unmodified (F4).
- Rolling upgrade safe: lease names, holder strings, and `AcquireLease` semantics are unchanged, so a mixed-version fleet (old replicas renewing each round, new replicas also heartbeating) shares the same lease protocol.

## 5. Scope boundaries (explicit non-goals)

| Non-goal | Reason |
|----------|--------|
| `ReleaseLease` / Stop path (analysis direction 2) | Separate direction, not selected; failover after a *completed* round still waits up to ttl — unchanged by this spec |
| Clock-skew independence / TTL clamping (analysis direction 3) | Separate direction, not selected; `GuardRenewing` adds cadence validation only (FR-3) |
| Repository SQL, `leases` migrations (0013), `LeaseStore` interface | Renew-own already exists (F1); any change would violate I1/I2 for no benefit |
| Making sweeps interruptible mid-object / mid-page | Cancellation is best-effort via ctx (F8); sweeps already break out of pagination loops on ctx errors |
| Changing `Guard` semantics, disabled-singleton behavior, or single-replica behavior | I5 + F4 |

## 6. Acceptance checks (preserved from the direction, made testable)

### AC-1 — Heartbeat prevents concurrent entry past TTL expiry

> *Direction:* "New unit test in internal/cluster with a stateful fake lease: replica B's Guard acquires the lease while replica A's fn is still running past TTL expiry; with heartbeat renewal the test asserts B never enters fn concurrently."

Testable form — new test `TestSingleton_Renewing_PreventsConcurrentTakeover` in `internal/cluster/singleton_test.go`:

- **Stateful fake:** add `statefulLease` (mutex-protected) alongside the existing `fakeLease` (which stays untouched for the four `TestSingleton_*` tests, F4). It models the real `leases.go:19-41` semantics: fields `holder string`, `expiresAt time.Time` (or injectable `now` func), `renewals []time.Time`, `calls int`; `AcquireLease(ctx, name, holder, ttl)` grants if free/expired, renews if `holder == current`, denies otherwise; records a timestamp per call.
- **Heartbeat scenario:** A: `GuardRenewing(ctx, ttl=100ms, renewEvery=25ms, fnA)`; `fnA` signals entry on a channel and blocks on a release channel for ≥ 2×ttl. Synchronize deterministically: wait for `fnA` entered **and** poll the fake until `renewals >= 1` and `time.Since(initialAcquire) > ttl` (deadline-bounded, no fixed sleeps).
- **B (same shared fake, different holder):** `GuardRenewing(ctx, ttl=100ms, renewEvery=25ms, fnB)` with `fnB` setting a `ranB` flag. Assert: `GuardRenewing` returned, `ranB == false`, and B's acquire was denied — A's lease was renewed past its original expiry.
- **Assert A's `fn` is still running** at the moment B's Guard returned (i.e., the test exercises the exact window where the unfixed code would have expired the lease).
- **Teardown:** release A; both Guards return; assert **no further `AcquireLease` calls** after both Guards have returned (settle 3×renewEvery — proves no renewal leak, FR-1).
- **Negative control (proves the test discriminates):** identical scenario but with plain `Guard` on A (no heartbeat) — assert B's `fn` **does** run (`ranB == true`), demonstrating the exact hazard `GuardRenewing` removes. The test must be **red against today's `Guard` and green only with `GuardRenewing`**.

### AC-2 — Lease renewed at least once per TTL window during a long fn

> *Direction:* "New test asserts the lease is renewed at least once per TTL window while fn blocks longer than ttl (fakeLease records renewal timestamps)."

Testable form — new test `TestSingleton_Renewing_RenewsWithinTTLWindows`:

- Parameters: `ttl = 100ms`, `renewEvery = 25ms` (= ttl/4), `fn` blocks on a release channel for `D = 400ms` (= 4×ttl).
- `statefulLease` records a timestamp per `AcquireLease` call (already required by AC-1).
- After release and return, assert:
  1. `calls >= 1 + D/ttl` (= **≥ 5**: initial acquire + at least one renewal per elapsed TTL window; with renewEvery = ttl/4 the expected count is ~17, giving ≥3× margin);
  2. max gap between consecutive `AcquireLease` timestamps `<= ttl` (= 100ms; expected ≤ 25ms, giving 4× margin) — the literal "at least once per TTL window" property;
  3. the first renewal timestamp is **before** `fn`'s exit timestamp (renewal happens while `fn` is still blocked, not after).
- All assertions use ≥2× margins against expected cadence so the test is deterministic under CI load.

### AC-3 — Cluster and reconcile suites stay green

> *Direction:* "go test ./internal/cluster -count=1 passes; existing tests (TestSingleton_*) stay green."

- `go test ./internal/cluster -count=1` passes, including the four existing tests **unmodified**: `TestSingleton_DisabledAlwaysRuns`, `TestSingleton_EnabledRunsWhenHeld`, `TestSingleton_EnabledSkipsWhenNotHeld`, `TestSingleton_LeaseErrorFailsSafe` — their exact 0/1 `fakeLease` call-count assertions prove `Guard`'s non-renewing semantics are untouched.
- Additionally (FR-4 wiring): `go test ./internal/reconcile -count=1` passes, including `TestJobSweep_ClusterSingleton_OnlyOneRuns` (`job_test.go:379-410`, real SQLite + migration 0013 + renew-own).

### AC-4 — Engineering gates

> *Direction:* "go vet ./... and gofmt -l are clean; no file exceeds 500 lines."

- `go vet ./...` → no findings; `gofmt -l .` → empty.
- Every touched file ≤ 500 lines: `internal/cluster/singleton.go` (66 today), `internal/cluster/singleton_test.go` (68), `internal/reconcile/job.go`, `lifecycle.go`, `retention.go`, `upload_gc.go` (all ≤ 130), `docs/configuration.md`.
- Verify with `make check` (the repo's hard gate: `gofmt -l` · `go build ./...` · `go vet ./...` · `go test ./...` · file-length check).

## 7. FR-level tests (beyond the supplied acceptance checks)

| # | Test | Covers |
|---|------|--------|
| T-1 | `GuardRenewing` on a **disabled** singleton runs `fn` directly with zero lease calls (extend `TestSingleton_DisabledAlwaysRuns` pattern) | FR-1 (I5) |
| T-2 | `GuardRenewing` panics when `renewEvery <= 0` and when `renewEvery >= ttl` (recover + assert message); `Guard` does not panic on the same inputs | FR-3 |
| T-3 | Renewal returning `false` (takeover) mid-fn: `fn`'s ctx is cancelled, `fn` observes `ctx.Err() != nil` and exits, `GuardRenewing` returns without restarting `fn` | FR-2 |
| T-4 | Renewal returning an error mid-fn: same cancellation + return behavior, one warn log emitted | FR-2, FR-5 |

## 8. Residual risks and notes for the design stage

- **Failover after a completed round still waits ≤ ttl** — the lease expires naturally (no release API; direction 2). Heartbeat does not regress this; it only removes the *mid-run* expiry window.
- **Window between lease loss and `fn` stop:** at most `renewEvery` + one in-flight operation. A lost renewal is detected at the next tick, then `fn`'s ctx is cancelled and `GuardRenewing` waits for exit — a second replica can enter at most `renewEvery` earlier than A stops. With `renewEvery = interval/2`, worst-case overlap is one sweep-operation; the guarantee "no duplicate destructive sweep while the holder is alive and renewing" holds unconditionally.
- **One in-flight renewal may land after `fn` returns** (ticker fired just before exit): worst case it extends the lease once by ≤ ttl, delaying takeover by ≤ ttl — benign (no split-brain), and direction 2's release path would remove it entirely.
- **Renewal failure policy is strict fail-safe** (FR-2: any failed renewal ⇒ yield): a transient DB error mid-sweep skips the remainder of that round; the next round re-acquires and re-runs. Chosen deliberately — the guarded actions are destructive and rounds are cheap (AGENTS.md §2.4: "better to skip a destructive sweep than to run it twice").
- **SQLite concurrency:** heartbeat adds up to 4 extra `UPDATE leases` per TTL window per job on the shared DB. The renew-own UPDATE is a single-row indexed write (leases table, `name` unique); if SQLite `SQLITE_BUSY` appears under multi-replica load it surfaces as a warn + skipped round (fail-safe), never as dual execution. Production cluster mode already relies on `busy_timeout` for lease writes.
