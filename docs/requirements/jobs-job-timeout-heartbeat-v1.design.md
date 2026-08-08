# Design — `internal/jobs`: bound job execution time and add lease heartbeat

**Module:** `internal/jobs` (+ `internal/repository`, `internal/config`, `cmd/server/workers.go`)
**Source spec:** `docs/requirements/jobs-job-timeout-heartbeat-v1.spec.md` (REQ-1…REQ-6)
**HEAD:** `acfaaf4` — every line number below re-verified on this checkout.
**Hard gates:** `make check` (gofmt/build/vet/test; single file ≤ 500 lines; no new deps; no migration files).

---

## 0. Evidence verification (untrusted claims → verified)

All five direction citations and the spec's wiring claims were re-checked against the tree:

| Claim | Verified at HEAD `acfaaf4` |
|---|---|
| `reapAfter=10min` default | `NewPool` `:125-138`, `reapAfter: 10 * time.Minute` `:136`; `Pool` struct `:128-134` has **no** timeout/heartbeat field ✅ |
| Reaper calls `ReapStuckJobs` with no liveness | `reaper` `:231-247`; `p.repo.ReapStuckJobs(ctx, p.reapAfter)` `:239`; loop = ticker + single call ✅ |
| `execute` passes bare pool ctx | `execute` `:222-229`, `return h(ctx, job)` `:228`; zero `context.WithTimeout` in package ✅ |
| `ClaimJob` sets `started_at` once, never refreshed | `ClaimJob` `:100-148`; PG `started_at=now(), updated_at=now()` `:103`, SQLite `started_at=$2, updated_at=$3` `:131`; no refresh path; no touch/heartbeat method in `Repository` (`repository_interface.go` Jobs section `:140-152`) ✅ |
| `ReapStuckJobs` keys both branches on `started_at` | `:241-267`; fail-permanent `WHERE status='running' AND attempts >= max_attempts AND started_at <= $3` `:253`; requeue `... AND started_at <= $2` `:256`; both reset `worker=''` (`:250`, `:255`) ✅ |

**Additional grounding (all confirmed):**
- `sqlStore` is the **sole** `Repository` implementer; the only non-`repository` `ClaimJob` reference is `jobs.go:186`; test doubles either embed the interface (`depthcap_test.go:14`) or are the real sqlite store — an interface addition is additive (I6-free, no concrete fake to break).
- `fastPool` (`jobs_test.go:32-44`) sets `pollEvery/baseBackoff/maxBackoff/reapEvery` but **not** `reapAfter`; the only `time.Sleep` in `jobs_test.go` is `waitFor`'s `2ms` poll — no existing handler runs ≥ 50 ms, so tightening `reapAfter` in `fastPool` is safe.
- `workers.go:58` is the only production `NewPool` call site: `go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).Run(ctx)`; `workers.go` already imports `time` (`:7`).
- `JobsCfg{Workers, MaxDepth}` (`config_app.go:48-54`), env read `config.go:256-259` (`getEnvInt("JOBS_WORKERS", 4)` / `"JOBS_MAX_DEPTH", 0`); `config_test.go:400-401,487-488` asserts `Jobs.Workers` defaults/override.
- `docs/configuration.md:129-130` and `.env.example:83-84` document `JOBS_WORKERS`/`JOBS_MAX_DEPTH`.
- `jobs` table has `updated_at TEXT/TIMESTAMPTZ NOT NULL` (migration `0009` both dialects); **no** liveness column.
- **Lease-clock consistency (pre-empts the sibling "RFC3339Nano wart" finding class):** every `updated_at` write on a `running` row is Go-side `time.Now().UTC().Format(time.RFC3339Nano)` (`ClaimJob` `:103/:131`, `CompleteJob`, `RetryJob`, `FailJob`, reaper `:254/:260`). The only non-Go writer is the SQLite migration `DEFAULT strftime('%Y-%m-%dT%H:%M:%fZ','now')`, which is **never exercised** — `EnqueueJob` `:90-96` always passes explicit values (`$8,$9` distinct placeholders, per the in-file I1 NOTE). Lexicographic `updated_at` comparison is therefore self-consistent within Go-written values; PG compares `TIMESTAMPTZ` natively. Rekeying the reaper on `updated_at` introduces no dialect asymmetry.
- **Behavioral-compatibility argument for REQ-3:** on a running row, `updated_at` is written exactly at claim time (legacy) or at claim + heartbeats (new). A deployment **without** heartbeats has `updated_at == started_at` for every `running` row, so `updated_at <= cutoff` ≡ `started_at <= cutoff` — the predicate change is a no-op until heartbeats actually run. Existing reaper tests use `maxAge=-time.Second` (cutoff in the future → both branches fire identically under either clock column) — verified against `jobs_test.go:86,116,150`.
- Prior failed implement attempt of this exact campaign (memory index: 2026-08-07T05:23:12Z, `validation failed (exit=1)`): **no artifacts retained** (run dir contains only `requirements-10762e10/`), so there is nothing to reconcile beyond enforcing the hard gates in this design's budgets (§8).

---

## 1. Design overview

Two additive mechanisms, both required by AC-1/AC-2:

1. **Lease heartbeat (REQ-1/REQ-2/REQ-3)** — closes the *duplicate concurrent execution* defect: `runOne` runs a goroutine that periodically refreshes the running job's `updated_at` (the lease clock) via a new `TouchJob` repo primitive; the reaper keys **both** of its branches on `updated_at`, so a live handler is never requeued regardless of duration. Schema-free (I2): `updated_at` already exists in migration `0009`.
2. **Per-job execution timeout (REQ-4/REQ-5)** — closes the *hung worker* defect, opt-in: `execute()` wraps the handler context in `context.WithTimeout` when `jobTimeout > 0`; a hung handler is canceled, the failure goes through the existing retry/permanent-fail path, and the worker slot is freed. Default `0` = disabled (I5).

No new goroutine ownership problem: the heartbeat goroutine shares **no mutable state** with the handler (only read-only `job.ID`/`worker` and the repo), and its termination is pinned by a `hbStop` channel closed via `defer` in `runOne` (channel-close happens-before is the join edge — see §6 S1).

---

## 2. API changes

### 2.1 `internal/repository` — `TouchJob` (interface + `sqlStore`)

`repository_interface.go`, Jobs section, immediately after `ClaimJob` (`:143`):

```go
// TouchJob refreshes the lease (updated_at) of a running job owned by worker.
// It is a no-op when the job is not 'running' or is owned by a different
// worker, so a late beat can never extend a job already requeued and
// re-claimed by someone else.
TouchJob(ctx context.Context, id int64, worker string) error
```

`sqlStore` implementation in `repository/jobs.go` (place after `FailJob`; single `rebind`ed UPDATE — dialect-neutral, I1: three distinct placeholders):

```go
// TouchJob refreshes the lease clock (updated_at) of a running job owned by
// worker. The status='running' guard makes the beat a no-op against a job
// already completed/failed/requeued; the worker=$3 guard makes a stale
// worker's late beat a no-op against a job re-claimed by another worker.
func (s *sqlStore) TouchJob(ctx context.Context, id int64, worker string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET updated_at=$1 WHERE id=$2 AND status='running' AND worker=$3`),
		now, id, worker)
	return err
}
```

- Returns a DB error (if any) or nil — **no** RowsAffected-based distinction (no-op is not an error by contract).
- No `jobCols`/`scanJob`/`flexTime` changes; no migration (I2).
- Budget: `repository/jobs.go` 293 → ~310 lines. ✅ ≤ 500.

### 2.2 `internal/jobs` — `Pool` fields + `WithJobTimeout` + heartbeat in `runOne`

`Pool` struct (`jobs.go:128-134`) gains two fields:

```go
	reapAfter   time.Duration
	jobTimeout  time.Duration // 0 = disabled (default)
	heartbeatEvery time.Duration
```

`NewPool` (`:125-138`) sets `heartbeatEvery: reapAfter / 3` with a 1 s floor:

```go
	heartbeatEvery: 10 * time.Minute / 3, // reapAfter/3; floor applied below
```

(field value `3*time.Minute+20*time.Second`; constructor expression written as `reapAfter/3` after the struct literal, or a small helper — implementation detail; tests override the field directly.)

New builder, mirroring the `Queue.WithMaxDepth` idiom (`:78-82`):

```go
// WithJobTimeout bounds each handler execution; a handler exceeding the
// timeout is canceled, retried with backoff, and eventually failed
// permanently. Zero (default) disables the timeout. Must be called before Run.
func (p *Pool) WithJobTimeout(d time.Duration) *Pool {
	p.jobTimeout = d
	return p
}
```

`execute()` (`:222-229`) wraps the handler context:

```go
func (p *Pool) execute(ctx context.Context, h Handler, job repository.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	if p.jobTimeout > 0 {
		hctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		return h(hctx, job)
	}
	return h(ctx, job)
}
```

`runOne` (`:185-217`): after the registry lookup (`:191-195`, so the no-handler path `:196-199` never starts a heartbeat) and before `p.execute` (`:201`):

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
			case <-ctx.Done():
				return
			}
		}
	}()
	defer close(hbStop)
	runErr := p.execute(ctx, h, job)
```

Key decisions, all pinned:
- **Pool ctx, not the timeout ctx**: the lease keeps beating while a handler is hung; the timeout (if enabled) is what eventually terminates the handler. If the pool ctx dies first (shutdown), `ctx.Done()` exits the goroutine.
- `defer close(hbStop)` covers success, retry, and permanent-fail returns of `runOne`; an in-flight beat racing `CompleteJob`/`RetryJob`/`FailJob` is a no-op via the `status='running'` guard (this is the *same* state-machine guard the existing `Test*OnlyTransitionsRunning` suite already pins).
- `TouchJob` failures: warn-logged, **non-fatal** (REQ-2 failure contract).
- Budget: `jobs.go` 266 → ~315 lines. ✅ ≤ 500.

### 2.3 `internal/config` + `cmd/server/workers.go` + docs

`config_app.go:48-54`:

```go
type JobsCfg struct {
	Workers        int
	MaxDepth       int // >0 caps pending jobs (backpressure); Enqueue returns ErrQueueFull when reached
	TimeoutSeconds int // >0 bounds each handler run via context timeout; 0 = disabled
}
```

`config.go:256-259`:

```go
		Jobs: JobsCfg{
			Workers:        getEnvInt("JOBS_WORKERS", 4),
			MaxDepth:       getEnvInt("JOBS_MAX_DEPTH", 0),
			TimeoutSeconds: getEnvInt("JOBS_TIMEOUT_SECONDS", 0),
		},
```

`cmd/server/workers.go:58`:

```go
	go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).
		WithJobTimeout(time.Duration(cfg.Jobs.TimeoutSeconds) * time.Second).
		Run(ctx)
```

Docs (one row each): `docs/configuration.md` after `:130` —

```
| `JOBS_TIMEOUT_SECONDS` | `0` | `>0` bounds each handler execution with a context deadline; a timed-out handler is retried with backoff (up to `max_attempts`), then failed. `0` = no timeout. Keep this **below** the reap window (10 min) so the timeout fires before the reaper can requeue a live job. |
```

`.env.example` after `:84`:

```
JOBS_TIMEOUT_SECONDS=0                     # >0: per-handler context deadline (retry on timeout); keep < reap window (10min); 0 = disabled
```

No public REST/S3/MCP/CLI surface changes (admin `/admin/jobs` already surfaces `last_error`, where `context deadline exceeded` becomes visible). No telemetry changes: timeout surfaces through the existing `IncJobRetried`/`IncJobFailed` counters (`jobs.go:194,203,210,216`); no new metrics (scope).

### 2.4 Tests (REQ-6)

`internal/jobs/jobs_test.go` — extend `fastPool` (`:32-44`):

```go
	p.reapEvery = 5 * time.Millisecond
	p.reapAfter = 50 * time.Millisecond
	p.heartbeatEvery = 10 * time.Millisecond
```

(existing tests: only `waitFor` sleeps 2 ms; all handlers complete in ms ≪ 50 ms, and every claimed job now heartbeats anyway).

**`TestPoolLongRunningJobNotDuplicated`** (AC-1): 2 workers, `jobTimeout` disabled; handler sleeps 150 ms (3× reapAfter) then returns nil, incrementing an `atomic.Int32`; enqueue one job (`Type:"noop"`); `waitFor(t, 3*time.Second, ...)` on `ListJobs` status `succeeded`. Assert: counter == **1**, `Attempts == 1`, status `succeeded`. Regression mechanism: with the heartbeat or the REQ-3 predicate reverted, the reaper requeues at ~50 ms and worker 2 executes → counter == 2 → test fails. Margins: sleep 3× reap; heartbeats 5× per window; reaper tick 5 ms.

**`TestPoolJobTimeout`** (AC-2): 1 worker, `p.WithJobTimeout(60 * time.Millisecond)`; handler records `sawDeadline` (first call: `select { case <-ctx.Done(): sawDeadline = true; return ctx.Err(); case <-time.After(5*time.Second): return errors.New("cancel did not propagate") }`; later calls return nil). Enqueue one job; `waitFor` succeeded. Assert: `Attempts == 2`, `LastError` contains `deadline exceeded` (via `ListJobs`), `sawDeadline == true`, status `succeeded`; then enqueue a second job and assert it completes → **worker stayed alive**. Timing: 60 ms timeout + 2-20 ms backoff ≪ 3 s waitFor. Note `reapAfter=50ms` < timeout 60 ms — the first attempt's lease is kept fresh by `heartbeatEvery=10ms`, so the reaper cannot requeue mid-run; this also regression-tests heartbeat-during-hang.

`internal/repository/jobs_test.go` (package `repository_test`, using existing `openJobsTestRepo`/`enqueueAndClaim`/`jobStatus` helpers, `:14-61`):

**`TestTouchJob`**:
1. claim (`worker-A`) → `TouchJob(id, "worker-A")` → nil; `ReapStuckJobs(ctx, time.Hour)` → 0 (fresh lease).
2. `TouchJob(id, "worker-B")` → nil but **no refresh**: `ReapStuckJobs(ctx, 0)` → 1 (requeued), status `pending`.
3. re-claim, `CompleteJob`, then `TouchJob(id, "worker-A")` → nil, no error, status stays `succeeded` (terminal guard).
4. `TouchJob(999999, "worker-A")` → nil (absent row is a no-op, no error).

**`TestReapStuckJobsUsesUpdatedAt`**: claim, `time.Sleep(5*time.Millisecond)`, `ReapStuckJobs(ctx, time.Millisecond)` → 1, status `pending` (predicate fires on `updated_at`); complements the fresh-lease arm of TestTouchJob.

`internal/config/config_test.go`: extend the `Jobs.Workers` default/override assertions (`:400-401,487-488`) with `TimeoutSeconds == 0` default and `JOBS_TIMEOUT_SECONDS=120` → 120 override (pattern already established in that file).

---

## 3. Compatibility constraints

| Constraint | Design response |
|---|---|
| `NewPool` signature | **Unchanged** — timeout is a builder (`WithJobTimeout`), mirroring the established `WithMaxDepth` idiom; zero call-site churn beyond `workers.go:58`. |
| `Repository` interface growth | Additive single method; sole implementer `sqlStore`; only embedded-interface doubles exist in tests — nothing else to update. |
| Behavior for existing deployments (no heartbeat, `JOBS_TIMEOUT_SECONDS=0`) | Bit-identical: timeout disabled path returns `h(ctx, job)` exactly as today; reaper predicates on `updated_at` ≡ `started_at` on running rows (§0). The heartbeat goroutine *does* run but only writes `updated_at` on rows it owns — observably identical outcomes. |
| Existing repo tests | `Test*OnlyTransitionsRunning` + reap tests use `maxAge=-1s` (future cutoff) — fire identically under either clock column (§0). State-machine guards unchanged. |
| SQL dialect | `TouchJob` and the reaper use the single `rebind`ed query path (I1); no per-dialect SQL, no migration pair (I2). |
| Existing env/config | `JOBS_TIMEOUT_SECONDS` defaults 0; `.env.example`/`configuration.md` rows are additive. |
| Handlers that legitimately exceed the timeout | Opt-in default-off (I5); operators who enable it accept retry-then-fail semantics, visible in `last_error`. |
| Graceful shutdown | Unchanged when timeout disabled (documented residual R5); with timeout enabled, a hung handler now yields to cancellation. |

---

## 4. Failure modes and mitigations

| # | Failure mode | Mitigation |
|---|---|---|
| F1 | `TouchJob` DB error mid-run (incl. SQLITE_BUSY) | Warn-log, non-fatal. In the same outage the reaper's own DB access fails (`reaper` `:241-244` warn-continues), so no spurious requeue; on recovery the next beat refreshes the lease. Interval `reapAfter/3` tolerates ≥ 1 consecutive missed beat. BUSY is *not* retried (matches sibling cluster-run stance R2; DSN-level `busy_timeout` is an operator concern, out of scope). |
| F2 | Heartbeat goroutine outlives `runOne` | Impossible by construction: `defer close(hbStop)` on every return path; channel-close happens-before guarantees the goroutine observes the stop on its next select. `ctx.Done()` arm covers pool shutdown. No shared mutable state with the handler → no data race (verified: goroutine reads only `job.ID`, `worker`, `p.repo`, `p.logger`, `p.heartbeatEvery` — all immutable after `NewPool`). |
| F3 | Timeout ≥ reap window (TOCTOU residual) | Pre-existing today (any handler > 10 min); with heartbeat the lease stays fresh during the run, so the residual window is only "handler keeps running past timeout *and* heartbeats stalled". Mitigated operationally: document `JOBS_TIMEOUT_SECONDS < reap window` (§2.3). `worker=` guard hardening of `RetryJob`/`CompleteJob` explicitly deferred (spec D4 non-goal — pre-existing TOCTOU). |
| F4 | `WithJobTimeout` after `Run` (data race on field) | Build-before-run contract, same as `fastPool`'s direct field sets today; documented in the doc comment. No mutex needed (consistent with `Pool`'s existing build-time fields). |
| F5 | Timeout shorter than legitimate handler runtime | Handler is retried up to `MaxAttempts` (default 5, `EnqueueJob` `:48-49`), then `failed` with `context deadline exceeded` in `last_error` — visible in `/admin/jobs`; operator tuning knob. |
| F6 | `TouchJob` late beat extends a re-claimed job's lease | Impossible: `worker=$3` guard — the reaper clears `worker=''` on requeue (`:255`), the second claimant writes its own `worker`, so the first worker's beat hits 0 rows. |
| F7 | Heartbeat keeps an eternally-hung handler alive (no timeout configured) | By design (lease = liveness); operator closes with `JOBS_TIMEOUT_SECONDS`. Work-progress detection explicitly out of scope (see §6 S4). |

---

## 5. Migration steps

**None required.** Schema-free (I2): `updated_at` already exists in migration `0009` (both dialects); no `.up/.down` pair, no `jobCols`/`scanJob` churn, `Migrate` untouched.

Rollout (zero-downtime, additive):
1. Ship the code change. Existing deployments: heartbeat runs, reaper semantics unchanged (≡ `started_at` clock), timeout disabled — no config action.
2. Operationally: set `JOBS_TIMEOUT_SECONDS` (e.g. 300) in environments where hung handlers were observed; keep it below the reap window.
3. Rollback: unset `JOBS_TIMEOUT_SECONDS` (0 = disabled); no DB rollback possible or needed (no schema delta).

---

## 6. Sibling-run findings — disposition (gate re-check)

Read: own run `DECISIONS.md` (requirements PASS), `add-a-renewing-heartbeat-leader-mode-to-cluster--293c5bd7` design gate (VERDICT: FAIL), `renew-the-singleton-lease-while-the-guarded-acti-7cf7f4fd` design gate (VERDICT: FAIL). Both failed gates concern **`internal/cluster` `GuardRenewing`** — a different subsystem (named-singleton leases table vs. per-job liveness) — but every finding is dispositioned:

| Finding (source gate) | Disposition for this design |
|---|---|
| G1 join/run-ctx protocol (293c5bd7) | **Adopted by analogy, pinned here**: heartbeat termination protocol = `hbStop` close-on-return (`defer`) + `ctx.Done()` arm; join edge = channel-close happens-before (§2.2, F2). |
| G3 panic-cell happens-before (293c5bd7) | **N/A + adopted**: our handler panic cell (`execute` recover) shares no state with the heartbeat goroutine; the only new goroutine has zero shared mutable state (verified list in F2), so no happens-before edge exists to declare. The panic cell itself is unchanged. |
| G2b join-before-re-panic (293c5bd7) | N/A — no re-panic in this design. |
| G6 observability matrix (293c5bd7) | **Adopted**: failure observability table = F1-F7 (§4); explicit decision: **no new metrics/alerts** — timeout surfaces via existing `IncJobRetried`/`IncJobFailed` + `last_error`; TouchJob failures via existing pool logger. The sibling's "observability surface undefined" failure mode is avoided by §4's explicit table. |
| lease G1 holder-ID collision (293c5bd7) | **Adopted by analogy**: `worker=$3` is the holder check (workers are `w0..wN`, unique per pool); reaper clears `worker` on requeue → stale beat no-op (F6). No `leases` table reuse (spec non-goal, D4). |
| lease G2 SQLITE_BUSY (293c5bd7) | **Adopted stance**: BUSY not retryable; non-fatal warn; reaper fails in the same outage (F1). DSN `busy_timeout` enforcement rejected as out-of-scope (jobs module has no DSN authority; the failure is non-fatal by contract). |
| lease G3 RFC3339Nano format wart (293c5bd7) | **Explicitly rejected with evidence** (§0): all `updated_at` writes are Go-side RFC3339Nano; the SQLite `strftime` DEFAULT is never exercised (`EnqueueJob` passes explicit values); comparisons are self-consistent; PG is native TIMESTAMPTZ. No padding helper needed because — unlike the cluster `leases` table — **no** writer uses a different format. |
| lease G4 keep-renewing-until-exit (293c5bd7) | **Intentional, inverted semantics documented**: here renewals *must* continue until `runOne` returns (lease = handler liveness against the reaper), bounded by `jobTimeout` when configured. No "zombie third replica" analog exists (reaper requeues, it does not take over). |
| lease G5 row-deletion self-heal (293c5bd7) | N/A — jobs rows are never deleted mid-run. |
| in-goroutine re-panic plan defect (293c5bd7) | N/A — no recover/re-panic in the heartbeat goroutine; its only error path is warn-log. |
| F-01/F5 artifact integrity (7cf7f4fd) | **Adopted**: this is a materialized design (≈260 lines), plus mirror `docs/requirements/jobs-job-timeout-heartbeat-v1.design.md`; DECISIONS.md summary will reflect actual file contents. |
| F-02 alert false-fire (7cf7f4fd) | N/A — this design adds zero alerts (no `StaleLeaderHoldingLease`-style static threshold anywhere). |
| F1 wire-up test (7cf7f4fd) | **Adopted**: `JOBS_TIMEOUT_SECONDS` parse asserted in `config_test.go` (§2.4); `WithJobTimeout` behavior asserted in `TestPoolJobTimeout`; the `workers.go` chain is a one-line composition covered by build. No main-package integration harness exists for workers; the builder idiom (`WithMaxDepth`) is already the repo's tested pattern. |
| F-03 hung-but-heartbeating invisible (7cf7f4fd) | **Explicitly rejected with evidence**: heartbeat *is* the liveness signal — a heartbeating handler is by definition alive; if it is hung and unproductive, the operator-enableable `JOBS_TIMEOUT_SECONDS` bounds it. Work-progress counters (e.g. per-sweep progress) belong to a different direction; adding metrics here violates the scope-lock (spec §4 non-goals: "no changes to telemetry counters"). |
| F2 test flake vectors (7cf7f4fd) | **Adopted**: ≥ 3× margins on all timing assertions; no wall-clock equality; failure mode (counter == 2) is the regression itself (§2.4). |
| F-04/DA-01 `busy_timeout` enforcement (7cf7f4fd) | Rejected as out-of-scope — DSN/DB-layer configuration outside `internal/jobs`; non-fatal contract (F1) makes it non-blocking here. |
| DA-02 `objects_storage_key_idx` migration (7cf7f4fd) | N/A — unrelated subsystem; no migrations in this design at all. |
| Prior implement validation failure (own campaign, 2026-08-07T05:23:12Z) | No artifacts retained (verified: run dir contains only `requirements-10762e10/`); nothing to reconcile. The `exit=1` failure mode is pre-empted by §8 budgets + AC-3. |

---

## 7. Testable acceptance mapping

| Acceptance (direction) | Test (spec REQ-6) | Assertion design | Determinism |
|---|---|---|---|
| AC-1: handler sleeping > reapAfter with heartbeats executes exactly once | `TestPoolLongRunningJobNotDuplicated` (`internal/jobs`) | atomic handler counter == 1; `Attempts == 1`; status `succeeded`; run with 2 workers, `reapAfter=50ms`, `heartbeatEvery=10ms`, sleep 150 ms, reaper tick 5 ms | sleep 3× reap window; heartbeat 5× per window; reverting heartbeat/REQ-3 flips the counter to 2 (test fails = regression detected) |
| AC-2: handler exceeding configured timeout is canceled, job retried, worker stays alive | `TestPoolJobTimeout` (`internal/jobs`) | `sawDeadline == true` (cancel propagated); `Attempts == 2`; `LastError` contains `deadline exceeded`; status `succeeded`; second enqueued job completes on same pool | 60 ms timeout + 2-20 ms backoff ≪ 3 s `waitFor`; heartbeat protects the 60 ms run from the 50 ms reap window |
| AC-3: `go test ./internal/jobs ./internal/repository && go vet ./... && gofmt -l internal/jobs` | the exact command (subset of `make check`) | plus new repo tests `TestTouchJob`, `TestReapStuckJobsUsesUpdatedAt` and config test in `go test ./internal/config` | — |

Every AC maps to a named, runnable, deterministic test asserting the *outcome*, not timing internals; the tests exercise the real reaper + real `TouchJob` + real `ReapStuckJobs` (no stubs).

---

## 8. Hard-gate compliance budget

| Gate | Evidence |
|---|---|
| `gofmt -l` clean | All touched files formatted (`internal/jobs/jobs.go`, `internal/jobs/jobs_test.go`, `internal/repository/jobs.go`, `internal/repository/repository_interface.go`, `internal/repository/jobs_test.go`, `internal/config/config_app.go`, `internal/config/config.go`, `internal/config/config_test.go`, `cmd/server/workers.go`); AC-3 enforces the subset. |
| `go build ./...` / `go vet ./...` | No new deps (I6); additive interface method implemented on the sole concrete type; builder pattern compiles at the single call site. |
| `go test ./...` | New tests + untouched existing suites (§0 compatibility proofs). |
| File ≤ 500 lines | `jobs.go` 266 → ~315; `repository/jobs.go` 293 → ~310; `jobs_test.go` 242 → ~335; `repository/jobs_test.go` 171 → ~230; `config_app.go` 83 → ~86; `config.go` 392 → ~395; `workers.go` 234 → ~235. All well under 500. |
| No migration files | §5 — schema-free by design (I2). |
| Single function ≤ 50 lines / cyclomatic ≤ 10 | `execute` +~4 lines; `runOne` +~13 lines (heartbeat block); `TouchJob` 6 lines; no new branches beyond the `jobTimeout > 0` guard. |
| Single-test runtimes | Both new pool tests ≪ 1 s (AC-1: 150 ms sleep; AC-2: ~100 ms); repo tests millisecond-scale. |

---

## 9. Open items / residuals (accepted)

- R3 TOCTOU residual when `jobTimeout >= reapAfter` and heartbeats stall — pre-existing, operational guidance documented (§4 F3).
- R4 `updated_at`-as-lease-clock is implicit — today only claim/terminal transitions and the heartbeat write it (`worker=` guard bounds cross-worker effects); dedicated `lease_expires_at` column recorded as the future hardening path (spec §6).
- R5 Hung handler with timeout disabled still blocks `Run`'s `wg.Wait` — unchanged today, by design (opt-in, I5); duplicate-execution defect (the primary risk) is closed by the heartbeat regardless.

*Verification basis: all anchors re-confirmed on checkout `acfaaf4`; spec `docs/requirements/jobs-job-timeout-heartbeat-v1.spec.md` REQ-1…REQ-6 implemented 1:1; scope locked to the direction (no migration, no `leases` reuse, no `RetryJob` worker-guard, no per-type timeouts).*
