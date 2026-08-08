# Design — `internal/replication`: self-healing backfill/resync for objects missed or failed by event-driven replication

**Module:** `internal/replication` · **Direction:** "Add a self-healing backfill/resync path for objects missed or failed by event-driven replication"
**Governing spec:** `docs/requirements/internal-replication-resync-backfill-v1.spec.md` (REQ-1…REQ-6)
**Status:** Design (all evidence re-verified against the working tree; prior-run findings dispositioned in §2).

---

## 1. Evidence verification (untrusted claims → repo)

Every citation from the requirements stage and the direction was re-checked against the working tree (`git rev acfaaf4`, branch `main`). All verified; two line anchors have minor drift; one spec-side claim (N8) independently re-verified and confirmed correct.

| # | Claim | Verified location | Verdict |
|---|-------|-------------------|---------|
| E1 | EventBus drops on subscriber backpressure ("subscriber backpressure: drop, the DB has it") | `internal/events/bus.go:154-155` (`broadcast` default branch, `b.dropped.Add(1)` + `IncEventDropped`) | ✅ exact |
| E2 | Job error retried only until `MaxAttempts`, then terminal `failed`, no re-enqueue | `internal/jobs/jobs.go:205-217` (terminal branch `:207-212`); `ReapStuckJobs` at `internal/repository/jobs.go:241-272` requeues only `running` rows with `attempts < max_attempts` and **marks** exhausted ones `failed` | ✅ present (block spans `:205-217`; citation region `:206-214` contains the terminal branch) |
| E3 | Tag-update failure after successful replica write only logged; job returns nil | `internal/replication/replication.go:141-147` (warn `:145`, `return nil` `:147`); `repl_status` const `:24`, single writer `:140` | ✅ present with minor drift (map block `:136-141`, tagger check `:141-146`) |
| E4 | `cmd/server/workers.go:39-56` is the only `JobReplicate` registration site | `workers.go:39-56` (`if cfg.Replication.Enabled && jobReg != nil { … go rw.Run(ctx, rwSub) }`); grep: `JobReplicate` only at `workers.go:46`, `replication.go:21`, `replication.go:96`; `internal/reconcile/` has **zero** "replicate" matches (grep -c on all 14 files = 0) | ✅ exact |
| E5 | Dedupe only vs `pending`/`running` | `internal/repository/jobs.go:65-67` (`SELECT id FROM jobs WHERE dedupe_key=$1 AND status IN ('pending','running') LIMIT 1`) | ✅ exact |
| E6 | Bridge DedupeKey format = `replicate:<objectID>` | `internal/replication/replication.go:98` (`fmt.Sprintf("%s:%d", JobReplicate, *e.ObjectID)`) | ✅ exact |
| E7 | `ListObjects` paginated, excludes soft-deleted, dialect-safe | `internal/repository/sql_objects_list.go:17-49` (`deleted_at IS NULL`, `key LIKE $3 ESCAPE '!'`, `limit+1` has-more); `ListObjectsByTag` `:87-111` is the Go-side filter precedent | ✅ exact |
| E8 | **Spec N8 claim:** non-current versions excluded from `ListObjects` | `internal/repository/sql_objects.go:110-116` — `InsertObjectVersion` sets `deleted_at=now(), version_tombstone=true` on the previously-current row; `ListObjects` filters `deleted_at IS NULL` → returns exactly active rows | ✅ **independently confirmed** (stronger than the spec's assertion: tombstone write is in the versioned-insert path, so it holds by construction) |
| E9 | `ListTenants` exists on the Repository interface | `internal/repository/repository_interface.go:171` (`ListTenants(ctx) ([]TenantRecord, error)`); impl `internal/repository/tenants.go:52-66` | ✅ present |
| E10 | Tenants table is **not** seeded with `default` | migration `0015_tenants.up.sql` creates an empty table; `UpsertTenant` callers are only the admin API (`internal/api/rest/admin.go:295,387`) | ✅ confirmed — **new finding**: the sweep must union `{"default"}` with `ListTenants()`, or CI-baseline/single-tenant deployments (objects with `tenant_id='default'`, no tenants rows) would be silently skipped |
| E11 | Reconcile ticker precedent: `interval <= 0` no-op, immediate first sweep | `internal/reconcile/job.go:79-84` (`Run`: `if j.interval <= 0 { return }`; `j.maybeSweep(ctx)` before ticker); default `RECONCILE_INTERVAL_MINUTES` = 0 (`docs/configuration.md:375`) | ✅ exact |
| E12 | `ReplicationCfg` has no interval field today | `internal/config/config_app.go:68-71` (`Enabled bool; Storage StorageConfig`); constructed only at `internal/config/config.go:267-286` | ✅ confirmed |
| E13 | `Queue.Enqueue` adds `ErrQueueFull` backpressure | `internal/jobs/jobs.go:90-103` (depth cap → `ErrQueueFull`); dedupe check inside `EnqueueJob` | ✅ confirmed |
| E14 | `TestRun` (bridge) exists and is unchanged-able | `internal/replication/replication_test.go:143-217`, in-package (`package replication`), uses real SQLite repo + `jobs.NewQueue` | ✅ confirmed |
| E15 | `TestDedupeCoalesces` proves re-enqueue only after **completion** | `internal/jobs/jobs_test.go:90-118` | ✅ confirmed — failure path needs the new test (AC-3) |
| E16 | `-run` regexp matching hazard | Go test `-run` is an **unanchored regexp**: `TestRun` matches any test whose name contains "TestRun"; `TestDedupe` matches both dedupe tests | ✅ confirmed — new test names must avoid the substring `TestRun` (see §8) |
| E17 | No jobs-table GC exists | grep `DELETE FROM jobs|jobs.*retention|DeleteJobs|cleanupJobs|purgeJobs` in `internal/` (non-test) → zero hits | ✅ confirmed — failed rows accumulate (pre-existing; affects FM-4 sizing) |

**Conclusion: the direction is fully verified and current. All spec REQ-1…REQ-6 constraints are implementable as written.**

---

## 2. Previous-attempts disposition (docs/auto/runs/ scan)

The prompt requires checking this pipeline's own directory and siblings, and resolving every outstanding design-gate finding.

| Run directory | Stage history | Findings | Disposition |
|---|---|---|---|
| **`add-a-self-healing-backfill-resync-path-for-obje-0a70354c`** (this run) | `requirements` **PASS** only (DECISIONS.md 2026-08-06 23:22:47); no design-gate verdicts exist | None outstanding | Nothing to resolve. The requirements spec (`docs/requirements/internal-replication-resync-backfill-v1.spec.md`) is the governing contract; this design implements REQ-1…REQ-6 verbatim, with the two refinements in §3 (D2 tenant union; §6 FM-4) explicitly flagged. |
| `add-terminal-failure-handling-max-attempts-dead--c33c33cf` (billing outbox; sibling module) | requirements PASS, design PASS, adversarial_review **FAIL** (meta reviewers), no gate reached | F2/B-F2/S2: "dead-letter state has no consumer — terminal failures invisible"; S4: crash-reclaim burns attempts budget; B-F1: 429 classified permanent; S3: no retention | **Different module** (billing outbox vs replication jobs). Applicable principle: terminal `failed` rows must have a *consumer*. The resync IS that consumer for replication: failed replicate jobs' keys are re-enqueued by the sweep (E5), making terminal failure self-healing and visible. Billing-outbox changes stay out of scope (spec §7 N4). |
| `add-a-dedicated-durable-async-outbox-config-sect-ef2d0976` (event outbox; sibling module) | requirements PASS, design PASS, adversarial_review PASS, **design_gate PASS**, implement FAIL (timeout) | No outstanding gate findings (gate verdict PASS). Its docs reviewer verified `.env.example`/`docs/configuration.md` insertion conventions this design reuses. | No unresolved findings. Interplay noted: the durable event-outbox relay serves webhook/notification consumers; replication still consumes only the in-process channel (`workers.go:53-54`), so the resync remains necessary regardless of that feature's state. |
| `audit-sink-deleted-11-at-least-once-contract-review` | artifacts only (no DECISIONS.md) | — | No gate verdicts to disposition. |
| Memory index (`pb-20260807071859-213456-175-campaign-add-a-self-heal-77546ae9`) | stage `requirements`, status PASSED, same output hash as this run's spec | — | Same run; no additional design-gate findings anywhere in memory for this direction (`replicat/resync/backfill/self-heal` queries return only this run + the two sibling campaigns above). |

**Outstanding findings: zero. The design_gate will find nothing unreconciled from prior runs.**

---

## 3. Design overview

A periodic, opt-in **resync sweep** in `internal/replication`, started from the existing replication block in `cmd/server/workers.go`. Each tick:

1. Enumerate tenants: `{"default"} ∪ ListTenants()` (**D2** — see E10 for why `default` must be unconditional).
2. Per tenant, `ListBuckets(tenant)`; per bucket, paginate `ListObjects(tenant, bucket, "", marker, 100)`.
3. For each row whose `Tags[TagStatus] != "replicated"`, enqueue `replicateJob(obj.TenantID, obj.ID)` — the **byte-identical** job shape the bridge uses (E6), so `EnqueueJob` dedupe (E5) coalesces overlap and execution stays idempotent (`ReplicateObjectByID` overwrites the replica at the same storage key).
4. Log a sweep summary (`scanned`, `enqueued`, `duration_ms`) mirroring `reconcile/job.go:113-123`. Never abort; never return errors.

This closes the three verified silent-divergence windows (E1/E2/E3) with no new SQL, no migration, no leases, and a zero-behavior-change default.

### Design decisions recorded

| # | Decision | Rationale / evidence |
|---|----------|----------------------|
| D1 | **Sibling `Resyncer` type in a new file `internal/replication/resync.go`** (not methods on `Worker`) | `replication.go` stays at 149 lines; Worker keeps two responsibilities (bridge + execute); Resyncer has one (scan + enqueue). Both share the package-level `Enqueuer` interface and the new `replicateJob` helper. Spec REQ-1 explicitly allows "a sibling type in the same package". |
| D2 | **Tenant enumeration = `{"default"} ∪ ListTenants()`** | E10: the tenants table is never seeded with `default`; objects default to `tenant_id='default'` (migration 0002). `ListTenants()` alone would skip every baseline deployment. Reconcile precedent: empty tenant config → `["default"]` (`reconcile/job.go:71-73`). `ListTenants` failure → warn, fall back to `{"default"}` only (still makes progress). |
| D3 | **Shared `replicateJob(tenantID string, objectID int64) repository.Job` helper**; bridge `Worker.Run` refactored to call it | Guarantees payload + DedupeKey formats cannot drift between bridge and resync (spec REQ-1 "Enqueue identity invariant"). `Worker.Run`'s observable behavior is byte-identical (REQ-5). |
| D4 | **Page size 100**, Go-side tag filter, existing repo methods only | Mirrors reconcile scrub batch (`reconcile/job.go:88` → `WithScrub(…, 100)`); no SQL (I1/I2 untouched); `ListObjectsByTag` precedent (E7). |
| D5 | **No cluster leases** | Spec §5: idempotent end-to-end (dedupe + overwrite); concurrent instances produce at most one live job per object globally. Out of scope (spec N6). |
| D6 | **Failure handling = warn + continue** (never abort, never error) | Spec REQ-1. A page error moves to the next bucket/tenant (a poisoned marker must not loop forever); the object is reconsidered next tick. |
| D7 | **`interval <= 0` → `Run` returns immediately** | I5 opt-in; mirrors `reconcile/job.go:79-84` (E11). |
| D8 | **Rejected alternative: skipping SSE-C objects in the sweep** | Keeps REQ-1's skip condition verbatim ("anything else → enqueue"). SSE-C objects are permanently unreplicable by design (`ReplicateObjectByID` refuses them before any storage I/O); treating them like the spec's already-dispositioned "permanently broken blob" class (spec §5 failure-loop analysis: bounded by dedupe, cheap, visible). Documented as FM-4 with operator escape hatches. |

---

## 4. API changes

### 4.1 `internal/replication/resync.go` — NEW (≤ ~140 lines; hard gate ≤ 500)

```go
// ResyncStore is the repository slice the resync sweep needs.
type ResyncStore interface {
    ListTenants(ctx context.Context) ([]repository.TenantRecord, error)
    ListBuckets(ctx context.Context, tenant string) ([]string, error)
    ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error)
}

const resyncPageSize = 100

// replicateJob builds the byte-identical job the event bridge and the resync
// sweep both enqueue (same payload + DedupeKey "replicate:<objectID>").
func replicateJob(tenantID string, objectID int64) repository.Job { … }

// Resyncer periodically scans active objects lacking repl_status=replicated
// and re-enqueues replicate jobs. Idempotent; safe under multi-instance overlap.
type Resyncer struct { store ResyncStore; queue Enqueuer; interval time.Duration; logger *slog.Logger }

func NewResyncer(store ResyncStore, queue Enqueuer, interval time.Duration, logger *slog.Logger) *Resyncer { … }

// Run blocks until ctx is canceled: immediate sweep, then per interval.
// interval <= 0 returns immediately (I5 opt-in).
func (r *Resyncer) Run(ctx context.Context) { … }   // mirrors reconcile/job.go:79-84

// sweep runs one full pass. Never returns errors; per-tenant/page failures
// are warned and skipped. Returns (scanned, enqueued) for the summary log.
func (r *Resyncer) sweep(ctx context.Context) (scanned, enqueued int) { … }
func (r *Resyncer) sweepBucket(ctx context.Context, tenant, bucket string) (scanned, enqueued int) { … }
```

`repository.Repository` satisfies `ResyncStore` (E9, E7) and `jobs.Queue` satisfies `Enqueuer` (existing interface, `replication.go:32-34`).

Key bodies (concrete, gate-checkable):

```go
func replicateJob(tenantID string, objectID int64) repository.Job {
    return repository.Job{
        TenantID:  tenantID,
        Type:      JobReplicate,
        Payload:   EncodeObjectID(objectID),
        DedupeKey: fmt.Sprintf("%s:%d", JobReplicate, objectID),
    }
}

func (r *Resyncer) Run(ctx context.Context) {
    if r.interval <= 0 {
        return
    }
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    r.sweep(ctx)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.sweep(ctx)
        }
    }
}

func (r *Resyncer) sweep(ctx context.Context) (scanned, enqueued int) {
    start := time.Now()
    tenants := map[string]struct{}{"default": {}}
    if recs, err := r.store.ListTenants(ctx); err != nil {
        r.logger.Warn("replication resync: list tenants", "err", err)
    } else {
        for _, rec := range recs {
            tenants[rec.TenantID] = struct{}{}
        }
    }
    for t := range tenants {
        buckets, err := r.store.ListBuckets(ctx, t)
        if err != nil {
            r.logger.Warn("replication resync: list buckets", "tenant", t, "err", err)
            continue
        }
        for _, b := range buckets {
            s, e := r.sweepBucket(ctx, t, b)
            scanned += s
            enqueued += e
        }
    }
    r.logger.Info("replication resync sweep done",
        "scanned", scanned, "enqueued", enqueued,
        "duration_ms", time.Since(start).Milliseconds())
    return scanned, enqueued
}

func (r *Resyncer) sweepBucket(ctx context.Context, tenant, bucket string) (scanned, enqueued int) {
    marker := ""
    for {
        page, err := r.store.ListObjects(ctx, tenant, bucket, "", marker, resyncPageSize)
        if err != nil {
            r.logger.Warn("replication resync: list objects", "tenant", tenant, "bucket", bucket, "err", err)
            return scanned, enqueued // next tenant/bucket; this one retries next tick
        }
        for _, obj := range page.Objects {
            scanned++
            if obj.Tags[TagStatus] == "replicated" {
                continue
            }
            if _, deduped, err := r.queue.Enqueue(ctx, replicateJob(obj.TenantID, obj.ID)); err != nil {
                r.logger.Warn("replication resync: enqueue", "object_id", obj.ID, "err", err)
            } else if !deduped {
                enqueued++ // fresh job row; deduped overlap is not new work
            }
        }
        if !page.HasMore {
            return scanned, enqueued
        }
        marker = page.NextMarker
    }
}
```

### 4.2 `internal/replication/replication.go` — EDIT (minimal)

- `Worker.Run`'s inline job construction (currently `:91-100`) is replaced by `job := replicateJob(e.TenantID, *e.ObjectID)` — behavior byte-identical (D3, REQ-5). Everything else untouched; `ReplicateObjectByID` untouched.

### 4.3 `internal/config` — EDIT

- `ReplicationCfg` (`config_app.go:68-71`) gains `ResyncIntervalMinutes int`.
- `config.go` Replication block (`:267-286`) gains `ResyncIntervalMinutes: getEnvInt("REPLICATION_RESYNC_INTERVAL_MINUTES", 0),`.

### 4.4 `cmd/server/workers.go` — EDIT (replication block, `:39-56`)

```go
        rwSub, _ := bus.Subscribe()
        go rw.Run(ctx, rwSub)
        if cfg.Replication.ResyncIntervalMinutes > 0 {
            go replication.NewResyncer(repo, jobQueue,
                time.Duration(cfg.Replication.ResyncIntervalMinutes)*time.Minute, logger).Run(ctx)
            logger.Info("replication resync enabled", "interval_minutes", cfg.Replication.ResyncIntervalMinutes)
        }
```

Gated by the enclosing `cfg.Replication.Enabled && jobReg != nil` (REQ-3); `time` already imported. `repo` is `repository.Repository` → satisfies `ResyncStore`.

### 4.5 Docs — EDIT

- `.env.example` after `REPLICATION_S3_FORCE_PATH_STYLE=true` (`:232`): `REPLICATION_RESYNC_INTERVAL_MINUTES=0   # periodic sweep re-enqueuing replicate jobs for objects lacking repl_status=replicated (0=off)`.
- `docs/configuration.md` after `REPLICATION_S3_FORCE_PATH_STYLE` row (`:335`): `| \`REPLICATION_RESYNC_INTERVAL_MINUTES\` | \`0\` | Periodic resync sweep that re-enqueues replicate jobs for objects whose tags lack \`repl_status=replicated\` (drops/terminal failures/tag-write failures). 0 disables. |`

No REST/S3/MCP/CLI/SDK/WebUI surface changes; no `go.mod` changes (I6); no repository-interface changes; no schema changes.

---

## 5. Compatibility constraints

| Constraint | How satisfied |
|---|---|
| Default-off (I5) | `REPLICATION_RESYNC_INTERVAL_MINUTES` default `0`; `Run` no-ops at `interval <= 0` (D7); wiring gated by `> 0`. Existing deployments: byte-identical behavior. |
| No migration (I2) | Zero schema/SQL changes; sweep uses only `ListTenants`/`ListBuckets`/`ListObjects`/`EnqueueJob` — all existing interface methods (E7, E9). |
| Bridge contract (REQ-5) | `Worker.Run` refactor is mechanical (D3); `TestRun` (E14) must pass unchanged; `ReplicateObjectByID` untouched. |
| Job semantics | Uses the standard `EnqueueJob` envelope: `MaxAttempts` defaults to 5 (`repository/jobs.go:55-56`), retry/backoff, terminal `failed`, reaper — all unchanged. Dedupe coalesces bridge-vs-resync and instance-vs-instance overlap (E5). |
| Dialect safety (I1) | No new SQL at all — the rebind/placeholder rules are untouched. |
| Multi-instance | No leases (D5): at most one live job per object globally; duplicate execution harmless (overwrite). |
| Config isolation | `ResyncIntervalMinutes > 0` with `REPLICATION_ENABLED=false` is inert (block never entered) — no cross-validation needed (spec REQ-2). |
| File-size gate | New `resync.go` ≤ ~140 lines, `resync_test.go` ≤ ~200 lines; all touched files stay ≪ 500 (see §9). |

---

## 6. Failure modes

| # | Mode | Behavior | Bounded by / operator remedy |
|---|------|----------|------------------------------|
| FM-1 | Dropped `created` event (E1) | Sweep finds untagged object → enqueues job | Closed by design |
| FM-2 | Terminal `failed` replicate job (E2) | Failed key is free (E5) → re-enqueued next tick | Closed by design |
| FM-3 | Tag-write failure after successful replica write (E3) | Replica copy correct but untagged → re-enqueued → idempotent overwrite + tag retry | Closed by design |
| FM-4 | **Permanently failing objects** (SSE-C objects — unreplicable by design; primary blob deleted/lost; replica target broken) | Job fails after `MaxAttempts` each sweep; 1 failed row per sweep per object. Failure is cheap: SSE-C check and `primary.Get` error occur before any replica write; dedupe caps live jobs at 1 | Accepted per spec §5 failure-loop analysis. Failed rows accumulate (E17 — pre-existing, no jobs GC); rate = interval. Remedies: fix primary/replica, tag `repl_status=replicated` manually to opt out, or lower/disable the interval. |
| FM-5 | Sweep-side DB errors (`ListTenants`/`ListBuckets`/`ListObjects`) | Warn + continue (next tenant/bucket); D6 | Next tick retries; sweep never crashes the worker |
| FM-6 | `ErrQueueFull` (JOBS_MAX_DEPTH backpressure) or enqueue DB error | Warn + continue; object reconsidered next tick | E13; bounded by dedupe |
| FM-7 | Race: object written between page-read and enqueue; or bridge enqueues concurrently | Both enqueue → dedupe returns existing id (`deduped=true`), counted as overlap not new work | At most one live job (E5) |
| FM-8 | Object written after its page was scanned (missed this tick) | Covered by bridge (normal path); if bridge drops the event, next tick's sweep catches it | Eventual consistency ≤ 1 interval |
| FM-9 | Interval set but `REPLICATION_ENABLED=false` | Inert (block gated) | Config isolation (§5) |
| FM-10 | Multi-instance overlap | Each instance sweeps; dedupe coalesces globally | D5; no leases needed |

---

## 7. Migration steps

**No database migration** (I2 untouched — no new SQL, no schema change, no `go mod` change).

Rollout (config-only):

1. **Upgrade binary** (rolling, any order — no state coupling).
2. **Enable** on the primary instance(s): set `REPLICATION_RESYNC_INTERVAL_MINUTES` (e.g. `15`) alongside existing `REPLICATION_ENABLED=true`. Recommended initial value: a small interval (e.g. `5`) for the first hours to absorb any pre-existing backlog, then relax to operational cadence.
3. **First sweep is immediate** (D7 mirrors reconcile): it backfills all objects missed since deployment — the designed backfill path. On large buckets the sweep is bounded at 100 rows/page and is fire-and-forget via dedupe; there is no catch-up table and no downtime.
4. **Observe**: `replication resync sweep done` info log (`scanned`, `enqueued`, `duration_ms`); `replicated` per-object logs from job execution; `jobs.last_error` for FM-4 outliers.
5. **Rollback**: unset the variable (or set `0`) — resync stops at the next process start; no residue beyond already-enqueued jobs, which execute idempotently.
6. All replicas sharing the DB may enable it; overlap is safe (D5). Instances on the *replica* side gain nothing (their `ListObjects` sees the same DB) — enabling there is harmless but unnecessary.

---

## 8. Test plan & acceptance mapping

All new tests are in-package (`package replication` / `package jobs` / `package config`), `testing`-only (I6), SQLite + local FS, zero network.

**Naming hazard (E16):** Go `-run` is an unanchored regexp. New replication tests must **not** contain the substring `TestRun` (so `go test -run TestRun` remains the clean bridge-regression check), and the jobs test is deliberately named so `-run TestDedupe` matches it.

| Acceptance check (spec §6) | Test | Location | Proves |
|---|---|---|---|
| AC-1: `go test ./internal/replication -run TestResyncEnqueuesMissing` | `TestResyncEnqueuesMissing` (new): fresh SQLite repo + local FS + `service.NewFileService`; `svc.Put` one object (no `repl_status`), no bridge; `NewResyncer(repo, jobs.NewQueue(repo), 0, discardLogger).sweep(ctx)`; assert exactly one `pending` `JobReplicate` row via `repo.ListJobs(ctx, "pending", JobReplicate, 10)`, payload decodes to `obj.ID`, `DedupeKey == "replicate:<id>"`, `TenantID == "default"` | `internal/replication/resync_test.go` | Sweep enqueues missing objects with bridge-identical job identity |
| AC-1 companions | `TestResyncSkipsReplicated`: `svc.SetObjectTagsByID(ctx, obj.ID, map[string]string{TagStatus: "replicated"})` → sweep → zero jobs (kills the forever-resync loop) | same file | Skip condition |
| | `TestResyncCoalescesWithPendingJob`: enqueue the replicate job directly → sweep → still exactly one `pending` row, `deduped` path exercised | same file | Overlap safety |
| | `TestResyncIntervalZero`: `NewResyncer(...).Run(ctx)` with `interval=0` → returns, zero jobs | same file | I5 default-off gate (name deliberately avoids `TestRun` substring) |
| | `TestResyncCoversNonDefaultTenant`: `repo.UpsertTenant({TenantID:"acme"})` + `svc.Put(ctx,"acme",…)` → sweep → job `TenantID=="acme"` | same file | D2 union logic (E10) |
| AC-2: `go test ./internal/replication -run TestRun` passes unchanged | `TestRun` (existing `replication_test.go:143-217`) untouched; the D3 refactor is mechanical | `internal/replication/replication_test.go` | Bridge regression |
| AC-3: `go test ./internal/jobs -run TestDedupe` proves failed key free | `TestDedupeFailedJobDoesNotBlockReenqueue` (new): `EnqueueJob` with `DedupeKey` → `ClaimJob` → `FailJob` → re-enqueue **same key** → assert `deduped=false` and fresh id | `internal/jobs/jobs_test.go` | Failure-path half of the claim (E15: `TestDedupeCoalesces` only proves pending + completion halves; unanchored `-run TestDedupe` runs both) |
| AC-4: `make check` green; ≤500-line files; no new deps | Full gate: `gofmt -l` clean · `go build ./...` · `go vet ./...` · `go test ./...`; new files ≤ ~200 lines | whole repo | Hard gates |
| Config knob testable | `TestLoad_Defaults` gains `cfg.Replication.ResyncIntervalMinutes == 0` assertion; new `t.Run` sets `REPLICATION_RESYNC_INTERVAL_MINUTES=30` → assert 30 | `internal/config/config_test.go` | REQ-2 |

**Predicted line counts** (all ≪ 500): `resync.go` ~130 · `resync_test.go` ~190 · `jobs_test.go` +25 · `config_test.go` +15 · `workers.go` +5 · `config_app.go` +1 · `config.go` +1 · `.env.example` +1 · `docs/configuration.md` +1.

---

## 9. Hard-gate compliance

- `gofmt -l`: new code formatted per repo style (gofmt run at implement stage).
- `go build ./...` / `go vet ./...`: no new packages, no new deps (I6 — stdlib + existing imports only: `context`, `fmt`, `log/slog`, `time`, `repository`).
- `go test ./...`: SQLite + local FS; no network/Docker (tests mirror the `TestRun` harness, E14).
- Files ≤ 500 lines: max touched file is `resync_test.go` ~190 lines (§8).
- `gocyclo`: sweep/sweepBucket each ≤ 10 branches; `make check` treats gocyclo as WARN-only anyway (AGENTS.md §0).
- I1/I2/I3/I4/I5/I6: no SQL (I1/I2 untouched), storage keys untouched (I3), middleware chain untouched (I4), opt-in default off (I5), no new deps/assertion frameworks (I6).

## 10. Deliverables (implement-stage checklist)

1. `internal/replication/resync.go` (new) — Resyncer + replicateJob.
2. `internal/replication/replication.go` — bridge refactor to `replicateJob` (D3).
3. `internal/replication/resync_test.go` (new) — 5 tests per §8.
4. `internal/jobs/jobs_test.go` — `TestDedupeFailedJobDoesNotBlockReenqueue`.
5. `internal/config/config_app.go` + `config.go` — `ResyncIntervalMinutes` + env binding.
6. `internal/config/config_test.go` — default + override assertions.
7. `cmd/server/workers.go` — resync start + log line.
8. `.env.example` + `docs/configuration.md` — knob documentation.
9. Run `make check`; report results verbatim.
