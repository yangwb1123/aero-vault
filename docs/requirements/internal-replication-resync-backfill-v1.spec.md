# Requirements Specification — `internal/replication`: self-healing backfill/resync for objects missed or failed by event-driven replication

**Module:** `internal/replication`
**Direction:** "Add a self-healing backfill/resync path for objects missed or failed by event-driven replication"
**Source analysis:** `docs/auto/analyses/internal-replication-9317f27a.json` (direction 1)
**Status:** Spec (evidence-verified). All five cited evidence locations verified against the repository; one has minor line drift (noted in §2). Scope strictly limited to the direction: no changes to the event bridge, job retry policy, `ReplicateObjectByID` semantics, or the other two directions in the analysis file.

---

## 1. Scope

Add a periodic, opt-in **resync sweep** for the replication module: a ticker-driven scan (started from the replication wiring in `cmd/server/workers.go`) that lists active objects, finds those whose tags lack `repl_status=replicated`, and re-enqueues `replicate` jobs using the **exact same payload and DedupeKey format** as the event bridge. This closes the three verified silent-divergence windows: dropped `created` events (bus backpressure), jobs that exhausted `MaxAttempts` into terminal `failed`, and successful replica writes whose `repl_status` tag update failed.

In scope:
- New periodic resync logic inside `internal/replication` (scan + enqueue), with a single-sweep entry point callable from tests.
- New config knob gating the resync interval (default off), plus `.env.example` / `docs/configuration.md` documentation, per repo convention.
- Wiring in `cmd/server/workers.go` to start the resync loop when replication is enabled and the interval is configured.
- Two new tests (one in `internal/replication`, one in `internal/jobs`) making the supplied acceptance checks genuinely testable.

Out of scope (see §8): skipping soft-deleted objects inside `ReplicateObjectByID` (analysis direction 2), replica write integrity/size-ETag verification (analysis direction 3), changes to the EventBus or its drop policy, changes to job retry/backoff policy, replication of deletes, new SQL queries or migrations, and distributed-lease (cluster singleton) gating of the resync.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| Cited symbol | Verified location | Verdict |
|---|---|---|
| EventBus drops on subscriber backpressure, "subscriber backpressure: drop, the DB has it" | `internal/events/bus.go:154-155` (`broadcast` default branch) | ✅ exact. Events are durable only in the `events` table (`InsertEvent` at `bus.go:78-83`); the replication bridge consumes **only** the in-process channel (`cmd/server/workers.go:53-54` → `replication.Worker.Run`, `replication.go:87-105`). No replication consumer reads `NextUnconsumedEvents` — a dropped event is permanently lost to replication. |
| Job error retried only until `MaxAttempts`, then terminal `failed` with no re-enqueue | `internal/jobs/jobs.go:206-214` (terminal branch at `:207-212`: `if job.Attempts >= job.MaxAttempts { FailJob; return }`) | ✅ present (block spans `:205-217`; citation region covers the terminal branch and the retry branch). `ReapStuckJobs` (`internal/repository/jobs.go:241-272`) requeues only stuck `running` jobs with `attempts < max_attempts`; `failed` rows are never re-enqueued by any code path. |
| Tag-update failure after successful replica write is only logged; `repl_status` stays missing while job returns nil | `internal/replication/replication.go:137-143` | ✅ present with minor line drift: tags-map block is `:136-141`, tagger check `:141-146`, warn `:145` (`w.logger.Warn("replication: tag update", ...)`), `return nil` `:147`. Claim fully verified: the job succeeds (replica copy written), `repl_status` never lands, and no retry exists. `repl_status` has exactly one writer in the repo (`tags[TagStatus] = "replicated"`, `replication.go:140`; const at `:24`). |
| No other callers of `JobReplicate` besides `cmd/server/workers.go:39-56` | `cmd/server/workers.go:39-56` | ✅ exact. The block (`if cfg.Replication.Enabled && jobReg != nil {` … `go rw.Run(ctx, rwSub) }`) is the only registration/bridge site. Grep: `JobReplicate` appears only at `workers.go:46` (register), `replication.go:21` (const), `replication.go:96` (bridge enqueue). |
| No replication logic in `internal/reconcile/` | `grep -rni "replicat" internal/reconcile/` | ✅ confirmed: zero matches. |
| `DedupeKey` dedupes only against pending/running rows | `internal/repository/jobs.go:65-67` | ✅ exact: `SELECT id FROM jobs WHERE dedupe_key=$1 AND status IN ('pending','running') LIMIT 1`. `failed`/`succeeded` keys are free — re-enqueue of a failed job inserts a fresh row. |
| Bridge dedupe-key format (must be reused verbatim by resync) | `internal/replication/replication.go:98` | ✅ `fmt.Sprintf("%s:%d", JobReplicate, *e.ObjectID)` → `replicate:<objectID>`. |
| Paginated, dialect-safe object listing excluding soft-deleted rows | `internal/repository/sql_objects_list.go:17-49` (`ListObjects`, `deleted_at IS NULL`) | ✅ present; `ListObjectsByTag` (`:87-111`) is the established Go-side tag-filter precedent (tags live as JSON in `objects.tags`; the codebase does not SQL-filter tags across dialects). |
| Interval-gated background job convention | `internal/reconcile/job.go:79-96` (`interval <= 0` → no-op; immediate first sweep then ticker); config `RECONCILE_INTERVAL_MINUTES` default `0` (`docs/configuration.md:375`) | ✅ the pattern the resync mirrors. |
| `ReplicationCfg` has no interval field today | `internal/config/config_app.go:68-71` (`Enabled bool; Storage StorageConfig`) | ✅ confirmed — new field required. |
| `EnqueueJob` defaults `MaxAttempts=5` | `internal/repository/jobs.go:55-56` | ✅ re-enqueued resync jobs inherit the standard retry envelope. |
| Existing bridge regression test | `internal/replication/replication_test.go:143-217` (`TestRun`) | ✅ present; uses a real repo + real `jobs.Queue`; must pass unchanged. |
| Existing dedupe test | `internal/jobs/jobs_test.go:90-118` (`TestDedupeCoalesces`) | ✅ present — but it proves re-enqueue after **completion**, not after **failure**. `-run TestDedupe` (unanchored regexp) already matches it; a new failure-path test is needed for the acceptance claim to be proven. |

**Conclusion: the direction's problem statement is fully verified and current.** Replication has zero recovery paths today; the resync is a pure addition with no existing behavior to preserve beyond `TestRun`.

---

## 3. Problem: three verified silent-divergence windows

1. **Dropped `created` event (backpressure).** `bus.broadcast` drops the event when the replication subscriber's 64-slot channel is full (`bus.go:154-155`); the durable DB copy is never consumed by replication. Result: object never replicated, no job, no error, no trace.
2. **Terminal `failed` job.** A replicate job failing `MaxAttempts` times lands in `failed` (`jobs.go:207-212`) with no re-enqueue anywhere; `ReapStuckJobs` only rescues stuck `running` rows. Result: object permanently divergent, error visible only in `jobs.last_error`.
3. **Tag write failure after successful replica write.** `SetObjectTagsByID` failure is logged and the job still returns nil (`replication.go:141-147`). Result: replica copy is correct but `repl_status` is missing, so no signal distinguishes it from cases 1/2 — and nothing retries.

All three converge on the same observable: **an active object row whose tags lack `repl_status=replicated`** and no live job for it. A periodic scan over exactly that set is the minimal self-healing closure: `EnqueueJob` re-inserts freely because dedupe matches only `pending`/`running` rows (`repository/jobs.go:65-67`), and `ReplicateObjectByID` is idempotent (overwrites the replica at the same storage key, `replication.go:106-118`).

---

## 4. Requirements

### REQ-1 — Resync sweep in `internal/replication` — NEW

A periodic sweep that, for every tenant/bucket pair, paginates `ListObjects` and re-enqueues a `replicate` job for every returned object whose tags lack `repl_status=replicated`.

- Scan uses only existing repository methods: `ListBuckets(tenant)` + `ListObjects(ctx, tenant, bucket, "", marker, batch)` (interface at `repository_interface.go:24`, `:54`). **No new SQL, no migration** (I2): tags are JSON in `objects.tags`; the Go-side tag filter mirrors the `ListObjectsByTag` precedent (`sql_objects_list.go:87-111`) and stays dialect-safe (I1 untouched).
- Page size bounded (≤ 100, matching the reconcile scrub batch `reconcile/job.go` `WithScrub(…, 100)`); one full scan per tick is acceptable at this page size (reconcile already full-scans every interval).
- **Skip condition:** `obj.Tags[TagStatus] == "replicated"` → no enqueue. Anything else (missing tag, any other value) → enqueue. `ListObjects` already excludes soft-deleted rows (`deleted_at IS NULL`), so the sweep never enqueues deleted objects.
- **Enqueue identity invariant:** the resync enqueues `repository.Job{TenantID: obj.TenantID, Type: JobReplicate, Payload: EncodeObjectID(obj.ID), DedupeKey: <same format as bridge>}` — byte-identical `DedupeKey` (`replicate:<objectID>`, `replication.go:98`) and payload as the bridge, so a bridge-enqueued pending job coalesces with a resync enqueue (and vice versa). Extract the bridge's job-construction into a small shared helper (e.g. `replicateJob(objectID, tenantID)`) so the formats cannot drift.
- **Safety under overlap:** `EnqueueJob` dedupes against live rows, so a resync tick racing the bridge or another instance's tick produces at most one live job per object; duplicate execution is harmless (`ReplicateObjectByID` overwrites). No cluster lease required (explicitly **not** adding `RECONCILE_CLUSTER_SINGLETON`-style gating — out of scope).
- **Failure handling:** a page/list-bucket error is logged (`w.logger.Warn`) and the sweep continues with the next page/tenant; a failed `Enqueue` (`ErrQueueFull` or DB error) is logged and the object is simply re-considered next tick. The sweep never aborts the worker and never returns errors to a caller.
- **Entry points:** a single-sweep method (e.g. `resyncOnce(ctx) error` or `(scanned, enqueued int, err error)`) callable from tests without waiting for a tick; a `RunResync(ctx)` ticker loop (immediate first sweep, then per interval; `interval <= 0` → return immediately, mirroring `reconcile/job.go:81-84`). Implement on the existing `Worker` (it already holds `repo` and `queue`) or as a sibling type in the same package — the design stage picks; the in-package test calls whichever.

### REQ-2 — Config knob `REPLICATION_RESYNC_INTERVAL_MINUTES` — NEW

- `ReplicationCfg` (`internal/config/config_app.go:68-71`) gains `ResyncIntervalMinutes int`, loaded via `getEnvInt("REPLICATION_RESYNC_INTERVAL_MINUTES", 0)` in `config.go`'s `Replication` defaults block (`:267`).
- **Default `0` = disabled** — preserves I5 opt-in safety: today's deployments get identical behavior; the resync is explicitly enabled. Follows the `RECONCILE_INTERVAL_MINUTES` convention (`docs/configuration.md:375`).
- Documented in `docs/configuration.md` (new row in the `REPLICATION_*` block, `:324-339`) and `.env.example` (`:221-227`).
- No cross-validation with `REPLICATION_ENABLED` required (a non-zero interval with replication disabled is inert).

### REQ-3 — Wiring in `cmd/server/workers.go` — NEW

Inside the replication block (`:39-56`), after `go rw.Run(ctx, rwSub)` and only when `cfg.Replication.ResyncIntervalMinutes > 0`, start `go rw.RunResync(ctx)` (or the chosen entry). The resync must not start when replication is disabled (the block is already gated by `cfg.Replication.Enabled && jobReg != nil`). One log line on start (e.g. `"replication resync enabled", "interval_minutes", n`) mirrors the existing `logger.Info("replication enabled", …)` at `:55`.

### REQ-4 — Observability — NEW (minimal)

The completed sweep logs a summary (`scanned`, `enqueued`, `duration_ms`) at Info level, mirroring `reconcile/job.go:113-123`. No new telemetry metric (avoid scope creep; `telemetry` additions are optional in design if trivially consistent).

### REQ-5 — Regression guard: bridge untouched

`Worker.Run` (event bridge) is not modified except for the optional extraction of the job-construction helper in REQ-1; its behavior must be byte-identical. `ReplicateObjectByID` is **not** modified. `TestRun` (`replication_test.go:143-217`) must pass unchanged.

### REQ-6 — Tests (make the supplied acceptance checks genuinely testable)

| Test | Location | What it proves |
|---|---|---|
| `TestResyncEnqueuesMissing` (new) | `internal/replication/replication_test.go` | Repo with an object created via `svc.Put` (no `repl_status` tag, no bridge running) → call the single-sweep entry → assert exactly one `pending` `JobReplicate` row exists, `payload` decodes to that object's ID, `DedupeKey == "replicate:<id>"`, `TenantID` matches. |
| `TestResyncSkipsReplicated` (new) | `internal/replication/replication_test.go` | Same setup, but run `ReplicateObjectByID` first (tag becomes `replicated`) → sweep → assert **no** job enqueued (prevents a forever-resync loop). |
| `TestResyncCoalescesWithPendingJob` (new, optional but cheap) | `internal/replication/replication_test.go` | Enqueue the replicate job directly, then sweep → assert still exactly one pending row (`deduped=true` path). |
| `TestRun` | unchanged | Bridge regression: a `created` event still enqueues exactly one replicate job. |
| `TestDedupeFailedJobDoesNotBlockReenqueue` (new) | `internal/jobs/jobs_test.go` | Enqueue with `DedupeKey` → `ClaimJob` → `FailJob` → re-enqueue **same key** → assert `deduped=false` and a fresh job id (the failure-path half of the acceptance claim; `TestDedupeCoalesces` covers the pending + completion halves). `-run TestDedupe` (unanchored regexp) then runs both. |

---

## 5. Design decision recorded (rationale for the spec's constraints)

- **Ticker in the replication Worker, not a reconcile job.** The acceptance check targets `go test ./internal/replication -run TestResyncEnqueuesMissing`, i.e. the logic lives in `internal/replication`; `internal/reconcile/` has no replication coupling today (§2) and its interval (`RECONCILE_INTERVAL_MINUTES`) can be disabled while replication runs — the resync needs its own knob (REQ-2). The reconcile `Run` ticker pattern (immediate sweep, `interval <= 0` no-op) is reused as the shape.
- **Go-side tag filter over `ListObjects`, not SQL.** Tags are JSON; SQLite vs Postgres JSON predicates would force a migration or dialect branching. `ListObjectsByTag` already established the in-memory-filter-over-paginated-pages precedent (`sql_objects_list.go:87-111`). Filtering the complement (`!= "replicated"`) in Go is the same pattern.
- **No leases.** The sweep is idempotent end-to-end (dedupe on enqueue + overwrite on execute); concurrent instances racing produce no duplicate work and no corruption. Adding `RECONCILE_CLUSTER_SINGLETON`-style leases would expand scope and add a cross-package dependency.
- **Failure-loop analysis.** An object whose primary blob is permanently gone fails every resync attempt (`primary.Get` → error → job fails after 5 attempts) and is re-enqueued next tick. That is correct self-healing behavior (a later `restore`/`put` succeeds), bounded by dedupe (at most one live job per object) and cheap (one job per interval per broken object). Noted for design; not a requirement change.

---

## 6. Acceptance criteria (preserved, made testable)

1. **`go test ./internal/replication -run TestResyncEnqueuesMissing` passes.** The test: fresh SQLite repo + local FS + `service.NewFileService`, one `svc.Put` object (no `repl_status`), no bridge running; run the single-sweep entry; assert a `pending` `replicate` job exists with payload decoding to the object's ID and `DedupeKey == "replicate:<id>"`. (Test added by REQ-6.)
2. **`go test ./internal/replication -run TestRun` passes unchanged** — no regression in the bridge (REQ-5).
3. **`go test ./internal/jobs -run TestDedupe` passes, proving a failed job's DedupeKey does not block re-enqueue.** `TestDedupeCoalesces` (existing) plus new `TestDedupeFailedJobDoesNotBlockReenqueue` (enqueue → claim → fail → re-enqueue same key → fresh row, `deduped=false`) together prove the claim; the unanchored `-run TestDedupe` regexp runs both.
4. **Full suite:** `make check` (gofmt/build/vet/test) green; no new `go.mod` dependencies (I6); new/changed files ≤ 500 lines (hard gate).

---

## 7. Non-goals / explicitly out of scope

| # | Thing | Why out of scope |
|---|-------|------------------|
| N1 | Skipping soft-deleted objects inside `ReplicateObjectByID` (analysis direction 2) | Different defect (stale replica copy after delete); independent fix, would touch job-execution semantics — this spec changes neither the bridge nor `ReplicateObjectByID` (REQ-5). Note: the resync scan already excludes soft-deleted rows via `ListObjects`. |
| N2 | Replica write integrity checks (size/ETag, analysis direction 3) | Independent defect in `ReplicateObjectByID`; out of this direction's scope. |
| N3 | EventBus drop policy / durable-event replay for replication | The resync makes replay unnecessary for the replication consumer; changing the bus affects all subscribers. |
| N4 | Job retry policy changes (`failed` → requeue, more attempts) | The resync already re-enqueues `failed` objects on the next tick; policy changes are global and risk hot loops. |
| N5 | Replication of deletes / `repl_status` values beyond `replicated` | Direction defines resync over the existing single status value only. |
| N6 | Cluster-singleton leases for the sweep | Idempotent by construction (§5); adds scope and coupling. |
| N7 | New SQL/migrations | Avoided by Go-side filtering (REQ-1); keeps I1/I2 untouched. |
| N8 | Scanning non-current object versions (`ListObjectVersions` per key) | The bridge already replicates each version at creation time; the sweep covers the active set, which is where all three failure windows surface. Versioned non-current rows are excluded by `ListObjects`' `deleted_at IS NULL`. |

---

## 8. Engineering constraints (hard gates)

- `make check` must stay green: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` (SQLite + local FS, zero network/Docker), every touched file ≤ 500 lines.
- Stdlib + existing packages only; no new `go.mod` dependency (I6); no assertion frameworks in tests (I6).
- No new repository SQL (I1/I2 untouched); the only repository interaction is existing `ListBuckets`/`ListObjects`/`EnqueueJob`.
- Environment-variable naming and defaults follow `docs/configuration.md` + `.env.example` (AGENTS.md §0).
