# Requirements: Shared transactional-outbox kernel (billing usage · vault.file.deleted@1.1 · vault.file.notify@1.1)

Module: `internal/billing` (+ `internal/events`, `internal/repository`)
Direction: *"Extract the billing durable outbox into a shared transactional-outbox kernel used by audit vault.file.deleted@1.1, notify vault.file.notify@1.1, and billing usage"*
Baseline: HEAD `acfaaf4` · Gates: `make check` (gofmt, go build, go vet, go test) · Invariants I1/I2/I5/I6

---

## 1. Evidence verification summary

All 7 direction citations verified against the current tree (symbols present; snapshot line numbers drift-corrected below).

| # | Citation | Verified at (current tree) |
|---|----------|------------------------------|
| 1 | `internal/billing/outbox.go`: runOutbox/deliverBatch/deliverFact/retryFact/billingBackoff | `runOutbox` :14-25 (timer, first tick immediate), `deliverBatch` :28-48 (ClaimBillingUsage, per-fact goroutines + WaitGroup), `deliverFact` :50-69 (binding lookup → AppendUsage → CompleteBillingUsage), `retryFact` :71-80 (RetryBillingUsage with next-attempt), `billingBackoff` :82-94 (1s base, ×2, 5 min cap, `math/rand` ±25 % jitter) |
| 2 | `internal/repository/billing_outbox.go`: Claim/Complete/RetryBillingUsage, SKIP LOCKED, claim-owner guard | `ClaimBillingUsage` :11-24 (owner/ttl validation, limit clamp 1..500→32, dialect dispatch); `claimBillingUsagePostgres` :26-43 (`FOR UPDATE SKIP LOCKED`, OR predicate pending-due ∨ inflight-expired, `attempts+1` at claim); `claimBillingUsageSQLite` :45-68 (tx + `s.rebind`); `CompleteBillingUsage` :128-134 (`WHERE id=$2 AND status='inflight' AND claim_owner=$3`); `RetryBillingUsage` :136-147 (same fence); `requireBillingClaim` :149-160 ("billing usage claim lost" when RowsAffected≠1) |
| 3 | `internal/billing/runtime.go`: Store interface, Start/Close, pollEvery/batchSize/claimTTL | `Store` :19-23 (`repository.BillingStore` + `AcquireLease`); `owner: uuid.NewString()` :67; knobs from `config.BillingConfig` :69-71 (`OutboxPollMillis`/`OutboxBatchSize`/`ClaimTTLSeconds`, `config_billing.go:27-29`); `Start` :85-100 / `Close` :68-83 (once-guarded, cancel + WaitGroup); `run` :102-119 spawns `runProjector` + `runOutbox` |
| 4 | `internal/auditgovernance/repository.go`: transactional capture | `auditedRepository.RecordAudit` :22-34 and `InsertEvent` :36-47 → `RecordAuditWithGovernance`/`InsertEventWithGovernance` when `runtime.Capture(tenant)`; both live in `internal/repository/audit_governance_write.go:14,45`; store contract `audit_governance_types.go:78-79` |
| 5 | `migrations/sqlite/0039_audit_governance_outbox.up.sql` | `UNIQUE (origin_kind, origin_id)`; `claim_owner`/`claim_token`/`lease_expires_at_ns`/`available_at_ns`/`attempts`/`last_error`/`delivered_at_ns`; postgres twin exists (`migrations/postgres/0039_…`) |
| 6 | `migrations/sqlite/0038_snaplink_billing.up.sql` | `billing_usage_outbox`: status CHECK {pending,inflight,delivered}, `next_attempt_at_ns`, `claim_owner`, `claim_until_ns`, `last_error`, `UNIQUE (operation_id, dimension)`; **no `claim_token` column**; postgres twin exists |
| 7 | `internal/events/bus.go:80-84` | `Publish` :78-108 → `repo.InsertEvent` :81 (durable insert, then non-blocking broadcast; errors logged, never propagated) |

**Direction claims confirmed by direct reading:** (a) billing claim is owner-only — no per-batch token (0038 has no `claim_token`); (b) audit-governance claim adds token + revision + bindings join (`audit_governance_claim.go:16-…, FOR UPDATE OF o SKIP LOCKED`); (c) the two backoff implementations differ (`billingBackoff` math/rand ±25 %, governance bounded deterministic, `runtime_test.go:189`); (d) no shared package exists — `grep` for any outbox kernel under `internal/` returns nothing; (e) `confidence_note` "no existing shared package" still true.

### 1.1 Critical new evidence beyond the citations (each shapes a requirement)

1. **The predicted "third outbox" already exists in-repo** (campaign round 1/2 landed it after this analysis was written): `internal/repository/event_outbox.go` (480 lines) + migration **0041_event_outbox** (both dialects) + `internal/events/event_outbox_relay.go` (374 lines) + `cmd/server/workers.go:63,158-177` (`startEventOutboxRelay`, **always started**; core deletion atomicity is not gated). It defines `OutboxEventType` `vault.file.deleted@1.1`/`vault.file.notify@1.1` (`event_outbox.go:22-24`), transactional capture `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`/`SoftDeleteObjectByIDWithEvent` (:102/:147/:186; delete + audit_log + facts in one tx, zero-row delete rolls back with `ErrNotFound`), payload validation (`validateOutboxFacts` :61-84, ≤1 MiB, JSON with `schema_version` == "1.1"), and a full relay (`Run` :111 / `deliverBatch` :140 / `deliverFact` :171 / `deliverDeleted` :190 / `deliverNotify` :236 / `complete` :305 / `retry` :320 / `eventOutboxBackoff` :345 / `prune` :362). **The divergence the direction warns about has tripled: three claim→deliver→complete loops, three fencing shapes, three backoffs, three status models.** The extraction is now *more* urgent, not hypothetical.
2. **The delete path is already transactional for audit+notify facts**: `internal/service/file_delete.go:46` (hard) and :86 (soft) call `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` with `deleteAuditEntry` + `deleteFacts` (:118-141, `events.BuildDeletedFact`/`BuildNotifyFact`). The direction's snapshot ("FileService.Delete emits only via EventBus … neither satisfies transactional outbox") is superseded: the outbox rows are written in the delete transaction today; `s.emit(ctx, obj, EventDeleted)` (:53/:92) remains as the *legacy* EventBus fan-out for webhook/indexer/SSE consumers. Consequently the acceptance's "outbox rows visible in same tx" and "never-blocking" items are partially satisfied — the kernel must **preserve** them, not create them.
3. **Schema builders and golden tests already pin the wire bytes**: `internal/events/payload.go` `deletedFact`/`notifyFact` (fixed field order, byte-stable), `schema_test.go:31/42/96/132` (golden JSON, required fields, deleted@1.1 envelope, sequencer uniqueness). Any envelope work must be **additive**; the direction's acceptance envelope lists `tenant_id`/`actor_digest`/`occurred_at` while the wire uses `tenant`/`actor` and carries no `occurred_at` (occurrence time lives in the row's `created_at_ns`/`available_at_ns`). The kernel's strict decoder is the reconciliation point (FR-6).
4. **No strict decoder exists today** for either payload: the relay parses only `notifyPayloadMeta {tenant,bucket,key}` (`event_outbox_relay.go:274-284`); `DisallowUnknownFields` appears only in config loaders. The acceptance's "strict decoder" is genuinely missing work.
5. **Existing test baseline the kernel must keep green**: `internal/repository/event_outbox_test.go` (one-tx delete+audit+facts, claim lifecycle, lease-expiry redelivery, retry/terminal-failed, prune, soft-delete-by-id, `TestHasEventOutboxFact`), `internal/events/event_outbox_relay_test.go` (delivery lifecycle, 5xx retry, claim-lost→reclaim-not-double-schedule, L2 delivery/unauthorized fail-immediate, backoff bounds), `internal/events/schema_test.go`, `internal/billing/runtime_test.go` (quota fail-closed — the acceptance's "keep passing" target), `internal/auditgovernance/runtime_test.go`.
6. **Telemetry exists only for the event outbox** (`internal/telemetry/metrics.go:49-56,91-98`: `event_outbox.delivered_total`/`retried_total`/`failed_total`/`claim_lost_total`/`pruned_total`/`l2_*`); billing has none. Metric names must not change (NFR-5).
7. **Divergence ledger (verified, per-table)** — the kernel must parameterize what is genuinely consumer-specific and unify what is not:

| Dimension | billing_usage_outbox (0038) | audit_governance_outbox (0039) | event_outbox (0041) |
|---|---|---|---|
| Fencing | owner only (no token col) | owner+token+revision+bindings join | owner+token |
| Status model | pending/inflight/delivered | `delivered_at_ns`/`failed_at_ns` flags | pending/inflight/delivered/failed |
| Terminal failure | none (retry forever, 5 min cap) | Fail → failed + retention cleanup | `attempts>=maxAttempts` → failed + prune 24h/7d |
| Dedupe | `UNIQUE(operation_id,dimension)` | `UNIQUE(origin_kind,origin_id)` | none (row id is authority; `AUTOINCREMENT`) |
| Backoff | 1s, ×2, 5 min cap, math/rand ±25 % | deterministic bounded (test-pinned) | 1s, ×2, 5 min cap, crypto/rand downward jitter |
| Claim batch | 1..500, default 32 | 1..500, default 32 | 1..500, default 32 |
| Capture | ApplyBillingUsage (same repo call) | RecordAudit/InsertEvent WithGovernance | Hard/SoftDeleteObject…WithEvent |
| Retention | none | gap-scan + cleanup jobs | relay prune (24h/7d) |
| Telemetry | none | none | 8 counters |

---

## 2. Spec decisions (direction's "proposed" items, with rationale)

- **Kernel shape**: a new `internal/outbox` package holding (a) a `Store` port (`Claim`/`Complete`/`Retry`, owner+token fenced, TTL lease, batch cap), (b) a single shared `Backoff` (crypto/rand downward jitter, 1s base, ×2, 5 min cap — the event-relay/webhook precedent; strictly-faster-than-base shrinks the at-least-once window), (c) a timer-poll driver (immediate first tick, per-batch bounded claim ctx, per-fact concurrent dispatch + WaitGroup, cancel-on-shutdown) that replaces both `billing.Runtime.runOutbox`/`deliverBatch`/`deliverFact`/`retryFact` and `EventOutboxRelay.Run`/`deliverBatch`/`complete`/`retry`. Fact *dispatch* (binding lookup / AppendUsage; event-type switch / sink / notify rules) stays in the consumers.
- **Fencing unifies on owner+token**: 2 of 3 variants already use per-batch tokens; the token makes claim-lost fencing precise (a stale owner after restart cannot complete/retry another owner's claim). Requires an **additive** dual migration **0042** adding `claim_token TEXT NOT NULL DEFAULT ''` to `billing_usage_outbox` (both dialects, I2; existing rows valid via default). No other schema change.
- **Terminal-failure policy is parameterized, not unified**: `Retry` takes `maxAttempts`; event relay passes `EVENT_OUTBOX_MAX_ATTEMPTS` (10), billing passes 0 = never-terminal (preserves today's retry-forever semantics; adding terminal-failed to billing is out of scope).
- **Dedupe keys stay table-specific**: billing `(operation_id,dimension)`, event row-id, governance origin-pair — the kernel does not impose a dedupe model (FR-4).
- **Audit-governance outbox machinery is out of scope** as a kernel consumer (binding/revision/gap-scan/redaction are consumer-specific); the kernel port is shaped so it could adopt later without rework. The direction's "audit vault.file.deleted@1.1" requirement is served by the event_outbox deleted@1.1 path, which *is* a kernel consumer.
- **Envelope contract via kernel strict decoder** (FR-6): the decoder is the single place the acceptance's semantic envelope (`tenant_id, bucket, key, version_id, actor_digest, occurred_at, request_id`) is materialized; wire bytes stay pinned by existing golden tests (NFR-6).

---

## 3. Functional requirements

### FR-1 — Shared outbox kernel package
`internal/outbox` provides one claim→complete/retry machinery consumed by (i) `billing.Runtime` outbox loop and (ii) `EventOutboxRelay`. Both consumers must be refactored onto it; no third copy of claim SQL, backoff, or poll loop may remain in `internal/billing` or `internal/events`.

### FR-2 — Claim contract
Claim(owner, token, limit, ttl) returns due facts only: `status='pending' AND available/next_attempt <= now` OR `inflight AND lease_expired`; increments `attempts`; stamps owner+token+lease-until. Constraints:
- Postgres path uses `FOR UPDATE … SKIP LOCKED` (per-table, as today); SQLite path is a tx with re-SELECT+UPDATE fence.
- **I1**: SQLite statements must route through `s.rebind`; Postgres must use `$N` directly; no `$N` reuse; the existing per-table SQL (billing `billing_outbox.go`, event `event_outbox.go`) is the reference for correctness.
- Batch limit clamp 1..500, default 32 (all three variants already agree).
- Claim identity validation (`owner != "" && token != "" && ttl > 0`) returns a descriptive error (precedent: `billing_outbox.go:16-17`).

### FR-3 — Complete/Retry fencing
- Complete and Retry are fenced by `id + owner + token + live lease`; a fenced-out write returns a claim-lost sentinel and must **not** be retried in-loop (reclaim after lease expiry is the recovery mechanism; the relay documents this at `event_outbox_relay.go:16-23`).
- Retry reschedules to `available/next_attempt = now + backoff(attempts)`, clears owner/token/lease, truncates `last_error` to 512 bytes, and — when `maxAttempts > 0` and `attempts >= maxAttempts` — lands the row terminal-failed (never claimable again). Billing passes `maxAttempts=0` (never-terminal, current behavior).
- Per-batch token generation: crypto/rand 16 bytes → 32 hex chars (precedent `event_outbox_relay.go:132-138`).

### FR-4 — Consumer-specificity boundaries (explicitly NOT unified)
Table/columns, status vocabulary, dedupe keys, retention/prune (event relay keeps its 24h/7d prune; billing keeps no prune), telemetry counters, config knobs and their env names/defaults (`BILLING_*` `config_billing.go:27-29`; `EVENT_OUTBOX_*` `config_event_outbox.go:15-44`) remain per-consumer and are passed into the kernel as parameters.

### FR-5 — Transactional capture stays in the business transaction (never-blocking)
`FileService.Delete` hard/soft paths continue to write audit row + `vault.file.deleted@1.1` + `vault.file.notify@1.1` facts **in the same DB transaction** via `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` (unchanged `file_delete.go:46,86`). The kernel must not be invoked from the delete path at all: enqueue-only, delivery happens exclusively in the relay goroutine. Fact validation failures (bad type, empty payload, >1 MiB, non-1.1 `schema_version`) roll back the delete transaction (existing `validateOutboxFacts` semantics, `event_outbox.go:61-84`).

### FR-6 — Strict envelope decoder
The kernel (or the `internal/events` payload module it depends on) exposes a strict decoder for `vault.file.deleted@1.1` with `json.Decoder.DisallowUnknownFields` + required-field validation, materializing the envelope: `tenant_id, bucket, key, version_id, actor_digest, occurred_at, request_id` (semantic names; wire names `tenant`/`actor` and row `created_at_ns` map onto them — wire bytes pinned by `schema_test.go` goldens). `vault.file.notify@1.1` decodes strictly too and must expose only self-contained fields (no audit-row/outbox-row references; `records[].s3.object.sequencer` is emit-time random, not a row id).

### FR-7 — Relay driver
The shared driver: timer poll (immediate first tick; consumer-configured interval), per-batch claim under a bounded context (consumer `httpTimeout`), per-fact concurrent dispatch with WaitGroup, `ctx.Done()` exit. The event relay keeps its delivery-time rule re-resolution (`GetBucketNotifications`) and byte-exact payload POST inside its consumer dispatch; the billing runtime keeps binding lookup + `AppendUsage` + `CompleteBillingUsage` in its dispatch.

### FR-8 — Migration 0042 (additive, dual-file)
Add `claim_token TEXT NOT NULL DEFAULT ''` to `billing_usage_outbox` in `internal/repository/migrations/{sqlite,postgres}/0042_*.{up,down}.sql` (I2: new files only; never edit 0038/0039/0041; `.down` never auto-executes). No other table change.

---

## 4. Non-functional requirements

- **NFR-1 (I1)**: every kernel SQLite statement through `s.rebind`; Postgres `$N`; no placeholder reuse; RFC3339Nano for any new time column (outbox tables keep UnixNano as today).
- **NFR-2 (I5)**: billing remains opt-in (`BILLING_ENABLED`); the event relay remains always-started (existing `workers.go` decision) — the kernel flips no gating; `nil` sink/embedder-style degradation unchanged.
- **NFR-3 (I6)**: stdlib only; no assertion framework in kernel tests; no new `go.mod` dependency without justification + `go mod tidy`.
- **NFR-4**: `make check` green; kernel files ≤ 500 lines, functions ≤ 50 lines, no God types; `gofmt`/`vet` clean.
- **NFR-5**: telemetry names unchanged (`event_outbox.*_total` set); kernel reports outcomes to consumers so counters stay where they are.
- **NFR-6 (backward compatibility)**: env var names/defaults, DB row formats, payload wire bytes (golden tests), and the public repository interface behavior are unchanged for anything outside `internal/outbox` + the two refactored consumers. `internal/auditgovernance` untouched.
- **NFR-7 (durability semantics)**: at-least-once until Complete (crash → reclaim after `claimTTL`); exactly-once only after Complete; no cross-fact ordering guarantee; claim-lost never double-schedules (all documented relay semantics preserved).

---

## 5. Acceptance criteria (direction's checks preserved, made testable)

**AC-1 — Kernel unit contract, both dialects (I1).** Shared-kernel tests (new `internal/outbox/*_test.go`, `testing` only):
- claim returns only due facts; claim increments attempts and stamps owner/token/lease;
- concurrent claimers don't double-claim (Postgres `SKIP LOCKED`; SQLite tx fence);
- complete/retry with wrong owner, wrong token, or expired lease → claim-lost sentinel; the same fact is reclaimed by a new claimer afterwards (idempotent re-delivery);
- retry reschedules with `backoff(attempts)`; with `maxAttempts=N`, the N-th retry lands terminal-failed and is never claimed again; `maxAttempts=0` never terminates (billing mode);
- SQLite run in CI against both `billing_usage_outbox` and `event_outbox` shapes (kernel parameterized); Postgres runs under `//go:build integration` (`make test-integration`), auto-skip when unreachable; placeholder discipline asserted by running the suite on both dialects (I1).

**AC-2 — Billing regression.** `internal/billing/runtime_test.go` keeps passing against the kernel-backed runtime (quota fail-closed, readiness, zero-limit enforcement), plus a new billing delivery test: enqueue a usage fact → claim → deliver to a fake `AppendUsage` → Complete marks delivered; sink 5xx → fact back to pending with backoff → delivered on next poll; claim-lost on complete warns and does not reschedule.

**AC-3 — Delete-transaction composition + sink outage.** Extend the existing one-tx tests (`event_outbox_test.go`): `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` with a kernel-produced audit fact + notify fact commit atomically; with the L2 sink down (fake sink returning 500), the relay leaves the fact `pending` (state transition pending→inflight→pending asserted via `last_error`/`attempts`), and after the sink recovers the fact is redelivered after backoff and `CompleteEventOutbox` marks `delivered` (delivered_at_ns set). Mirrors `TestOutboxRelay_RetriesOn5xx` against the kernel.

**AC-4 — Crash recovery / TTL reclaim.** Claim a fact with a short TTL, never complete/retry it (simulated crash), then claim again after lease expiry → the same fact is returned to a new owner (precedent `TestEventOutboxClaimLeaseExpiryRedelivers`); the first owner's late Complete returns claim-lost; the fact is delivered exactly once *after* the second claim (attempts == 2).

**AC-5 — Event schema round-trip with strict decoder.** New decoder tests: `decode(BuildDeletedFact(...))` yields the envelope with all seven fields (`tenant_id, bucket, key, version_id, actor_digest, occurred_at, request_id` — mapping wire `tenant`/`actor`/row `created_at_ns`), re-encode → decode is lossless; decoder rejects unknown fields, missing required fields, and `schema_version != "1.1"`; existing golden-byte tests stay green (wire unchanged). `notify@1.1`: strict decode exposes only self-contained fields; assertion that the payload contains no audit/outbox row identifiers (no `origin_id`, no outbox column names); relay POSTs the payload byte-exact (existing byte-exactness assertion extended to the kernel path).

**AC-6 — Composition e2e, never-blocking.** Service/e2e test (httptest): delete through the real business flow (current trigger: REST `DELETE /v1/files/...` — the admin-delete route is a separate direction; this AC applies unchanged to it once it lands) → 2xx; **immediately after the response**, both outbox rows (`vault.file.deleted@1.1` + `vault.file.notify@1.1`) are visible with status `pending` in the same DB (same-transaction visibility); with fake L0/L1 sinks down or hanging, the delete still completes and returns (never-blocking assertion: delete returns with rows still `pending` — delivery provably not awaited; no wall-clock flake), then async delivery to recovered fake sinks flips both rows to `delivered`. Business-flow latency is bounded by the delete transaction only (no sink I/O on the request path).

---

## 6. Out of scope (explicitly not required here)

Audit-governance outbox adoption of the kernel (bindings/revision/gap-scan/redaction); admin file-delete route; EventBus/notifier/webhook-DLQ changes; notify rules engine; billing terminal-failed or retention policy; telemetry renames; payload wire-format redesign (golden-pinned; decoder is additive); any change to `internal/auditgovernance`.
