# Requirements Specification — `internal/jobs`: bound job execution time and add lease heartbeat to prevent duplicate concurrent execution of long-running jobs

**Module:** `internal/jobs` (+ `internal/repository`, `internal/config`, `cmd/server/workers.go`)
**Direction:** "Bound job execution time and add lease heartbeat to prevent duplicate concurrent execution of long-running jobs"
**Source analysis:** `docs/auto/analyses/internal-jobs-3fcb0121.json` (direction 1)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 9 / risk reduction 8 / effort 4 / confidence 9

---

## 1. Scope

The reaper and workers share one process (`Pool.Run` starts both, `internal/jobs/jobs.go:141-157`), and `ReapStuckJobs` requeues any job with status `running` whose `started_at` is older than `reapAfter` (default **10 min**, `jobs.go:136`) with no liveness signal — `started_at` is set once at claim time (`internal/repository/jobs.go:103,131`) and never refreshed. A handler that legitimately runs > 10 min (external AV scan, webhook POST, LLM/embed call, slow replication) is therefore requeued by the **in-process** reaper and re-executed by another worker while the first handler is still running, duplicating side effects (webhook delivery, replica copy). There is also no per-job timeout: handlers receive the bare pool ctx (`jobs.go:228` `return h(ctx, job)`), so a hung handler occupies a worker slot indefinitely and blocks graceful shutdown (`Run`'s `wg.Wait` at `jobs.go:156` waits for it).

This spec scopes exactly the direction's two mechanisms, both of which are required by the acceptance checks:

1. **Lease heartbeat** — a periodic liveness refresh so the reaper only requeues genuinely orphaned jobs.
2. **Per-job timeout** — a configurable deadline wrapping the handler context inside `execute()` so a hung handler is canceled, retried, and cannot pin a worker forever.

Out of scope (see §4): migration/schema changes, worker-guard hardening of `RetryJob`/`CompleteJob`, reuse of the `leases` table, per-job-type timeouts, making the heartbeat interval configurable, backoff/dedupe changes.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit (`acfaaf4`).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `internal/jobs/jobs.go:113-117` — `reapAfter=10min` default | `NewPool` at `:125-138`; `reapAfter: 10 * time.Minute` at **`:136`**; struct fields `:128-134`. No timeout field exists on `Pool` | ✅ **content exact, line drift** (cited range sits ~19 lines earlier in an older revision). |
| E2 | `internal/jobs/jobs.go:232-243` — reaper calls `ReapStuckJobs` with no liveness check | `reaper` at **`:231-247`**; `p.repo.ReapStuckJobs(ctx, p.reapAfter)` at **`:239`**; loop body = ticker + single call, no liveness/heartbeat anywhere | ✅ **near-exact** (range now `:231-247`). |
| E3 | `internal/jobs/jobs.go:199-209` — `execute` passes pool ctx with no deadline | `execute` at **`:222-229`**; `return h(ctx, job)` at **`:228`**; zero `context.WithTimeout` uses in `internal/jobs/jobs.go` | ✅ **content exact, line drift** (cited range is `runOne`'s bookkeeping at `:197-217`; the *claim* is about `execute`, which is correct). |
| E4 | `internal/repository/jobs.go:97-145` — `ClaimJob` sets `started_at` once, never refreshed | `ClaimJob` at **`:100-148`**; PG branch `started_at=now()` at `:103`, SQLite branch `started_at=$2` at `:131`; no code path writes `started_at` again; no heartbeat/touch method exists in `repository.Repository` (`repository_interface.go:201` lines — `ClaimJob` `:143`, `ReapStuckJobs` `:150`; only `AcquireLease` `:166`, which is the reconcile singleton mechanism, unrelated) | ✅ **near-exact** (range now `:100-148`). |
| E5 | `internal/repository/jobs.go:239-263` — `ReapStuckJobs` requeues running jobs solely by `started_at <= cutoff` | `ReapStuckJobs` at **`:241-267`**; fail-permanent branch `WHERE status='running' AND attempts >= max_attempts AND started_at <= $3` at `:253`; requeue branch `WHERE status='running' AND attempts < max_attempts AND started_at <= $2` at `:259`. The `jobs` table (migration `0009_jobs.up.sql`) has no liveness/lease column — only `started_at`/`updated_at` | ✅ **near-exact** (range now `:241-267`). |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Reaper and workers share one process; `Pool.Run` starts both and `wg.Wait` blocks shutdown" | ✅ **holds** — `Run` `:141-157`: workers loop `:144-150` + reaper goroutine `:151-155`, then `wg.Wait()` `:156`. A hung handler (no timeout) blocks graceful shutdown. |
| "`started_at` set once at claim, never refreshed; reaper requeues by `started_at <= cutoff` with no liveness signal" | ✅ **holds** — `ClaimJob` sets `started_at` once (`:103`/`:131`); `ReapStuckJobs` keys both branches on `started_at` (`:253`, `:259`). A live-but-long handler (> 10 min default) is requeued and re-executed by another worker. |
| "No per-job timeout; handler receives bare pool ctx" | ✅ **holds** — `execute` `:222-229` passes `ctx` unwrapped; a hung handler pins its worker slot and delays shutdown. |
| "Side effects can duplicate (webhook delivery, replica copy)" | ✅ **holds** — webhook and replication handlers run on this pool via `cmd/server/workers.go` (`startWebhook`, `replication.NewWorker`), and `runOne`'s failure path (`RetryJob` `:215`) would also re-run a canceled/requeued job. |

**Additional verified facts (wiring the fix):**

- `Queue.WithMaxDepth` (`jobs.go:78-82`) establishes the **builder idiom** for pool options — adopted for the timeout (D1).
- `jobs_test.go`'s `fastPool` helper (`:32-44`) sets `pollEvery/baseBackoff/maxBackoff/reapEvery` but **not** `reapAfter` (still 10 min) — the AC-1 test must set it (REQ-6).
- `cmd/server/workers.go:58` builds the pool: `go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).Run(ctx)` — the only production call site; `JobsCfg{Workers, MaxDepth}` (`internal/config/config_app.go:48-54`, env read `internal/config/config.go:256-259`) has no timeout knob; `docs/configuration.md:129-130` and `.env.example:83-84` document the `JOBS_*` vars.
- `sqlStore` is the **only** implementer of `repository.Repository`; test doubles embed the interface (e.g. `countFailureRepository` in `depthcap_test.go:13-16`) — adding a method to the interface is additive and breaks nothing (REQ-1).
- `ReapStuckJobs` is dialect-neutral (single `rebind`ed query set, `:241-267`) — a predicate change needs no migration and no per-dialect SQL (I2 respected).

---

## 3. Requirements

### REQ-1 — `TouchJob`: repository heartbeat primitive (`internal/repository`)

Add to the `Repository` interface (`repository_interface.go`, next to `ClaimJob`/`ReapStuckJobs`) and implement on `sqlStore` (`jobs.go`):

```go
// TouchJob refreshes the lease (updated_at) of a running job owned by worker.
// It is a no-op when the job is not 'running' or is owned by a different worker.
TouchJob(ctx context.Context, id int64, worker string) error
```

Implementation (single `rebind`ed UPDATE, dialect-neutral):

```sql
UPDATE jobs SET updated_at=$1 WHERE id=$2 AND status='running' AND worker=$3
```

- **Schema-free by design (I2):** the `jobs` table already has `updated_at` (migration `0009`); it becomes the lease clock. No new column, no migration pair, `jobCols`/`scanJob` untouched.
- **Guards:** `status='running'` (a terminal write racing the heartbeat wins — the heartbeat becomes a no-op, never resurrects a completed/failed row) and `worker=$3` (a stale worker's late beat cannot extend the lease of a job already requeued and re-claimed by another worker).
- **Failure contract:** DB error → returned error, **never** an abort of the running handler (see REQ-2).

### REQ-2 — Heartbeat loop in `runOne` (`internal/jobs/jobs.go`)

- New `Pool` field `heartbeatEvery time.Duration`, set in `NewPool` to **`reapAfter / 3`** (default 10 min → ~3 min 20 s), floored at 1 s. Rationale: at interval `reapAfter/3`, up to two consecutive missed beats (transient DB errors) still keep the lease inside the reap cutoff.
- In `runOne` (`:185-217`), after a successful claim and before `p.execute`, start a heartbeat goroutine:

```go
hbStop := make(chan struct{})
go func() {
    t := time.NewTicker(p.heartbeatEvery)
    defer t.Stop()
    for {
        select {
        case <-t.C:
            if err := p.repo.TouchJob(ctx, job.ID, worker); err != nil {
                p.logger.Warn("job heartbeat", "id", job.ID, "type", job.Type, "err", err)
            }
        case <-hbStop:
            return
        }
    }
}()
defer close(hbStop)
```

- The goroutine uses the **pool** ctx (not the per-job timeout ctx from REQ-4), so it keeps beating while a handler is hung — the lease stays fresh until the timeout fires. `TouchJob` failures are warn-logged and non-fatal; the reaper remains the fallback if heartbeats cannot be persisted.
- `close(hbStop)` runs when `runOne` returns (defer), covering both success and failure bookkeeping; the `status='running'` guard makes any in-flight beat harmless during `CompleteJob`/`RetryJob`/`FailJob`.

### REQ-3 — Reaper keys on the lease clock, not `started_at` (`internal/repository/jobs.go`)

In `ReapStuckJobs` (`:241-267`), change **both** predicates from `started_at <= $cutoff` to `updated_at <= $cutoff`:

- Requeue branch (`:257-261`): `WHERE status='running' AND attempts < max_attempts AND updated_at <= $2` — a job whose worker is alive (heartbeat refreshing `updated_at`) is never requeued, however long it runs.
- Fail-permanent branch (`:250-254`): `WHERE status='running' AND attempts >= max_attempts AND updated_at <= $3` — **must move too**: with heartbeats, a max-attempts job legitimately running past `reapAfter` would otherwise be marked `failed` while still executing (a second, silent duplicate-termination bug).
- `started_at` remains the attempt-start marker (observability only). `last_error='worker lease expired after maximum attempts'` message unchanged. Doc comment updated to say "whose lease (`updated_at`) has not been refreshed for maxAge".

**Compatibility with existing tests (verified):** `TestReapStuckJobs` (maxAge 0 — `updated_at` = claim time ≤ fresh cutoff → requeued ✓) and `TestReapStuckJobAtMaxAttemptsFailsPermanently` (maxAge −1 s — cutoff in the future → fail-permanent fires ✓) still pass unchanged.

### REQ-4 — Per-job execution timeout in `execute()` (`internal/jobs/jobs.go`)

- New `Pool` field `jobTimeout time.Duration`; **`0` = disabled** (default, preserving current behavior; opt-in per the repo's I5 default-off culture).
- Builder `func (p *Pool) WithJobTimeout(d time.Duration) *Pool` — mirrors the `Queue.WithMaxDepth` idiom (`:78-82`); `NewPool`'s signature is unchanged (no caller churn).
- In `execute()` (`:222-229`), wrap the handler context:

```go
if p.jobTimeout > 0 {
    hctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
    defer cancel()
    return h(hctx, job)
}
return h(ctx, job)
```

- Timeout outcome: the handler returns `ctx.Err()` (`context.DeadlineExceeded`); `runOne`'s existing failure path handles it — backoff retry if `attempts < max_attempts` (`:211-215`), permanent `FailJob` otherwise (`:208-210`). Panic recovery (`:222-229`) untouched. The worker goroutine returns normally — **the worker stays alive**.
- No new error classification or telemetry: the timeout surfaces as an ordinary handler error in `last_error` (`context deadline exceeded`), and existing `IncJobRetried`/`IncJobFailed` counters apply.

### REQ-5 — Configuration + wiring (`internal/config`, `cmd/server/workers.go`, docs)

- `JobsCfg` (`config_app.go:48-54`) gains `TimeoutSeconds int`; `config.go:256-259` reads `getEnvInt("JOBS_TIMEOUT_SECONDS", 0)` (0 = disabled). Follows the existing `JOBS_WORKERS`/`JOBS_MAX_DEPTH` naming and int-seconds pattern.
- `cmd/server/workers.go:58` becomes:

```go
go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).
    WithJobTimeout(time.Duration(cfg.Jobs.TimeoutSeconds) * time.Second).
    Run(ctx)
```

- Document the knob: one row in `docs/configuration.md` next to `JOBS_WORKERS`/`JOBS_MAX_DEPTH` (`:129-130`) and one line in `.env.example` (`:83-84`), including the operational note that `JOBS_TIMEOUT_SECONDS` should be **less than the reap window** (10 min default) so the timeout fires before the reaper can requeue (see §6 R3).

### REQ-6 — Regression tests

`internal/jobs/jobs_test.go`:

- **`TestPoolLongRunningJobNotDuplicated`** (AC-1): extend `fastPool` (`:32-44`) to also set `reapAfter = 50ms` and `heartbeatEvery = 10ms` (existing tests are unaffected — all their handlers complete in ms, well under 50 ms). Test: 2 workers, `jobTimeout` disabled, handler sleeps 150 ms (> 3 × `reapAfter`) and returns nil; enqueue one job; `waitFor` succeeded. Assert via atomic counter: **exactly 1 handler call**, job `Attempts == 1`, status `succeeded`. Determinism margins: sleep 3× reap window; heartbeats 5× per window; reaper tick 5 ms. Without the heartbeat this test fails (requeue → second worker executes → calls = 2).
- **`TestPoolJobTimeout`** (AC-2): 1 worker, `p.jobTimeout = 60ms` (via `WithJobTimeout`); handler blocks on `ctx.Done()` on the first call, records `sawDeadline := ctx.Err() != nil`, returns `ctx.Err()`; subsequent calls return nil immediately. Enqueue one job; `waitFor` succeeded. Assert: `Attempts == 2` (retried), `sawDeadline == true` (the cancel actually propagated), `LastError` contains `deadline exceeded`; then enqueue a second job and assert it completes — **worker stayed alive**. Total runtime ≪ 1 s (fastPool backoff 2-20 ms).

`internal/repository/jobs_test.go`:

- **`TestTouchJob`**: claim → `TouchJob(id, worker)` → nil; `ReapStuckJobs(ctx, time.Hour)` returns 0 (lease fresh). `TouchJob` with a different worker → nil but **no refresh** (`ReapStuckJobs(ctx, 0)` requeues it). `TouchJob` after `CompleteJob` → no-op (no error, `updated_at` unchanged).
- **`TestReapStuckJobsUsesUpdatedAt`**: claim, sleep past a short `maxAge`, `ReapStuckJobs` requeues (covers the predicate switch; complements `TestTouchJob`'s fresh-lease arm).

---

## 4. Decisions & non-goals

- **D1 — Builder option, not `NewPool` signature change.** `WithJobTimeout` mirrors `Queue.WithMaxDepth` (`jobs.go:78-82`); the only production call site (`workers.go:58`) and all tests stay source-compatible.
- **D2 — Lease clock = existing `updated_at`, no migration.** A dedicated `lease_expires_at` column would be semantically cleaner but requires a dual migration pair (I2), `jobCols`/`scanJob` churn, and wider diff — out of proportion to the direction (effort 4). `updated_at` is written only by claim/terminal transitions and (after this change) the heartbeat, so it is a faithful liveness clock today. Recorded as a future-hardening note in §6 (R4).
- **D3 — Timeout default off (`0`), opt-in.** The problem statement explicitly lists legitimate > 10 min handlers (AV scan, webhook POST, LLM/embed, slow replication); a default-on timeout would break them out of the box. The heartbeat alone closes the *duplicate-execution* defect (the value-9 half); the timeout closes the *hung-worker* defect for operators who enable it. Consistent with I5 (opt-in defaults, `nil`/disabled must not break the baseline).
- **D4 — Heartbeat interval internal, not configurable.** Derived from `reapAfter/3`; the direction asks for a configurable *timeout*, not a configurable heartbeat. Tests set the fields directly.
- **Non-goals:** no reuse of the `leases` table / `AcquireLease` (named-singleton semantics for reconcile; per-job liveness is a different granularity — citing it as a precedent only); no `worker=` guard hardening of `RetryJob`/`CompleteJob` (pre-existing TOCTOU, outside this direction — see R3); no per-job-type timeout map; no changes to backoff, dedupe, telemetry counters, or the `Pool.Run`/reaper structure; no new `go.mod` dependencies (I6).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — *"`go test ./internal/jobs -run TestPoolLongRunningJobNotDuplicated`: handler sleeping > reapAfter with heartbeats is executed exactly once."***
*Testable:* REQ-6 `TestPoolLongRunningJobNotDuplicated` — `fastPool` with `reapAfter = 50ms`, `heartbeatEvery = 10ms`, `jobTimeout` disabled; handler sleeps 150 ms (> 3 × `reapAfter`). The reaper ticks every 5 ms throughout; **without** the heartbeat the job would be requeued at ~50 ms and a second worker would execute it (calls = 2). Assertion: atomic handler counter == 1, `Attempts == 1`, status `succeeded`. Margins are ≥ 3× on both sides, so the test is deterministic, and it exercises the real reaper + real `TouchJob` + real `ReapStuckJobs` path (no stubs).

**AC-2 — *"`go test ./internal/jobs -run TestPoolJobTimeout`: handler exceeding configured timeout is canceled, job retried, worker stays alive."***
*Testable:* REQ-6 `TestPoolJobTimeout` — `WithJobTimeout(60ms)`; first attempt blocks until `ctx.Done()` and returns `ctx.Err()` (asserts the cancel propagated: `sawDeadline`), retry succeeds. Assertions: `Attempts == 2`, `LastError` contains `deadline exceeded`, final status `succeeded`; a second enqueued job then completes on the same worker — proving the worker slot was not consumed. Timing: timeout 60 ms, retry backoff 2-20 ms, `waitFor` 3 s — deterministic.

**AC-3 — *"`go test ./internal/jobs ./internal/repository && go vet ./... && gofmt -l internal/jobs` passes."***
*Testable:* the exact command; `go test ./internal/jobs ./internal/repository` additionally runs the new `TestTouchJob`/`TestReapStuckJobsUsesUpdatedAt` and the existing repo suite (whose two reaper tests were verified compatible with REQ-3 in §3). This is a subset of the `make check` hard gate (gofmt/build/vet/test; `jobs.go` grows from 266 → ~310 lines, `repository/jobs.go` 293 → ~310 — both well under the 500-line cap).

---

## 6. Risks

- **R1 — Timing flake in heartbeat test.** Mitigated by ≥ 3× margins (sleep 150 ms vs reap 50 ms; heartbeat 10 ms vs reap 50 ms); no wall-clock equality assertions; the failure mode (calls = 2) is the regression itself, asserted exactly.
- **R2 — Heartbeat DB errors during a long run.** TouchJob failures are warn-logged and non-fatal; the reaper's own DB access fails in the same outage, so no spurious requeue; on recovery, the next beat refreshes the lease. Interval `reapAfter/3` tolerates ≥ 1 consecutive missed beat.
- **R3 — Residual TOCTOU when `jobTimeout >= reapAfter`.** If an operator sets a timeout longer than the reap window (or heartbeats stall while the handler keeps running), the reaper can requeue a live job; on retry, the original worker's `RetryJob` (guarded only by `status='running'`) can re-claim the attempt out from under the second worker. This is **pre-existing** behavior (today: any handler > 10 min hits it); REQ-4 mitigates it operationally — document `JOBS_TIMEOUT_SECONDS < reap window (default 10 min)` (REQ-5). Extending the `worker=` guard to `RetryJob`/`CompleteJob` is explicitly deferred (D4 non-goal).
- **R4 — `updated_at` as lease clock is implicit.** Any future code writing `updated_at` mid-execution would silently extend leases. Contained: today only claim/terminal transitions and the heartbeat write it, and the `worker=` guard bounds cross-worker effects. A dedicated `lease_expires_at` column remains the hardening path if the invariant ever breaks.
- **R5 — Shutdown with timeout disabled.** With `JOBS_TIMEOUT_SECONDS=0`, a hung handler still blocks `Run`'s `wg.Wait` — unchanged from today, by design (D3); the duplicate-execution defect (the primary risk) is closed by the heartbeat regardless.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); `make check` gate applies to the eventual implementation (gofmt/build/vet/test, single file ≤ 500 lines, no new deps, no migration files).*
