# Requirements: Durable-async + transactional RAG chunk invalidation (`internal/ai`)

- **Status:** v1 (evidence-backed; every citation re-verified against the working tree)
- **Module:** `internal/ai` (+ seams in `internal/service`, `internal/repository`, `internal/events`, `internal/reconcile`, `cmd/server`)
- **Direction:** "Make RAG chunk invalidation durable_async and transactional with admin delete (outbox-driven delete_chunks + orphan sweep)" (COMPOSE-2026-017)
- **Source analysis:** `docs/auto/analyses/internal-ai-99180452.json` (entry 1 of 3)
- **Verification basis:** HEAD `35ff4ce` **plus uncommitted WIP** — the parallel campaign's deletion outbox (`internal/repository/event_outbox.go`, `internal/events/event_outbox_relay.go`, `internal/events/payload.go`, migration `0041_event_outbox`, `EVENT_OUTBOX_*` config; see `docs/requirements/transactional-outbox-delete-events-v1.md` / `audit-sink-deleted-11-v1.md`). **The analysis snapshot predates this WIP; §2 lists the claims that are now stale.** All line numbers below are against the working tree.
- **Scope boundary:** write-side invalidation only — make chunk deletion (a) never block the business flow, (b) atomically triggered by the delete transaction, (c) durably drained, (d) backstopped by an orphan sweep. **Out of scope:** the read-path liveness gate (covered by `rag-liveness-gate-v1.md`), the notify@1.1/audit L2 consumer work, the legacy event bus for other consumers, and any retrieval SQL.

---

## 1. Evidence verification register

Every citation in the direction was re-checked against the working tree.

| # | Cited claim | Verified location | Verdict |
|---|-------------|-------------------|---------|
| E1 | `indexer.go:DeleteObjectChunks` — repo rows then sinks, error on first sink failure | `internal/ai/indexer.go:230-239` (`repo.DeleteChunksForObject` at `:231`, sink loop `:233-238`, returns `fmt.Errorf("sink delete chunks %d…")`) | ✅ |
| E2 | `processEvent`: `EventDeleted` → `JobDeleteChunks` | `internal/ai/indexer.go:184-201` (branch at `:196-197`: `dispatch(ctx, JobDeleteChunks, e.TenantID, *e.ObjectID, …)`) | ✅ |
| E3 | `dispatch` uses `DedupeKey` | `internal/ai/indexer.go:207-225` (`DedupeKey: fmt.Sprintf("%s:%d", jobType, objectID)` at `:220`; queue-nil → runs inline) | ✅ |
| E4 | `EncodeObjectID` — object_id-only job payload | `internal/ai/indexer.go:36-40` (`objectIDPayload{ObjectID int64}`) | ✅ |
| E5 | `sink.go:ChunkSink.DeleteObjectChunks` | `internal/ai/sink.go:17` (interface); `Indexer` registers sinks via `WithChunkSink` (`indexer.go:84-91`) | ✅ |
| E6 | BM25/pgvector/Qdrant adapters | BM25 `bm25.go:210` (in-memory map delete); Qdrant `qdrant.go:281-285` (**HTTP `POST /points/delete?wait=true`** — one network RTT per object, in the business flow today); **pgvector is NOT a `ChunkSink`** — `PgVectorIndex` reads the repo `chunks` table directly (`pgvector.go:121-146`), so repo-row deletion covers it | ⚠️ partial — pgvector has no adapter; only BM25 + Qdrant are sinks |
| E7 | `file_delete.go` synchronous `chunkCleaner` calls | `hardDeleteObject` `:27-34` (per version), `softDeleteObject` `:81-87`, `DeleteVersion` `:195-201` — all warn-only on failure, all inside the request path | ✅ |
| E8 | `file.go:59-63,137-149` — ChunkCleaner "called synchronously on hard delete" | `internal/service/file.go:59-64` (interface doc: *"called synchronously on hard delete, before the repository row is removed… Non-fatal"*), `:136-149` (`WithChunkCleaner` / `ChunkCleaner()` accessor used by reconcile) | ✅ |
| E9 | `file.go:297-313` — `emit` after the delete; payload lacks version identity | `internal/service/file.go:297-313` (`emit` builds `Payload{backend,size,etag,content_type}` only; `s.sink.Publish` after `HardDeleteObjectWithEvent` committed). Legacy `Event` struct has no version discriminator (`repository/repository.go:175-193`) | ✅ (for the **legacy** event; see S1/S2 for the outbox facts) |
| E10 | `delete_marker.go:53`, `object_worker.go:61`, `file_bucket_delete.go:70` | `delete_marker.go:53-56` (sync cleanup of the **shadowed prior current's** chunks — event at `:69` carries the *marker row's* id, an identity mismatch); `object_worker.go:61-65` (quarantine → `SoftDeleteObjectByID` at `:70`, **no outbox facts**); `file_bucket_delete.go:70-75` (per-object cleanup inside `deleteBucketData`, then `repo.DeleteBucket` — **no per-object facts**) | ✅ |
| E11 | `bus.go:Publish → InsertEvent` — second, non-transactional commit | `internal/events/bus.go:77-99` (`repo.InsertEvent` at `:84`; errors logged never propagated; local broadcast buffer 64 with drop-on-full `:111,:154-155`; durable catch-up `NextUnconsumedEvents` polled by `Indexer.Run` every 5 s, `indexer.go:143-171`) | ✅ |
| E12 | `reconcile/deletion.go:82` `cleanObjectChunks` seam; no orphan-chunk sweep | `internal/reconcile/deletion.go:82-92` (seam used by retention/lifecycle purges via `WithChunkCleaner`, `cmd/server/workers.go:88-101`); `reconcile/job.go:106-129` `sweep` = orphan rows + orphan blobs + scrub **only** — no chunks pass | ✅ |

**Corrections to the direction's problem statement (all verified):**

| # | Direction claim | Working-tree fact | Evidence |
|---|-----------------|-------------------|----------|
| C1 | "there is no outbox row committed atomically with the metadata delete (verified: emit … runs after HardDeleteObject; bus.Publish → InsertEvent is a second transaction)" | **Stale.** `HardDeleteObjectWithEvent` / `SoftDeleteObjectWithEvent` (`internal/repository/event_outbox.go:96-179`) commit `vault.file.deleted@1.1` + `vault.file.notify@1.1` **in the delete transaction** (zero-row delete rolls back with the facts, `:117-121`). `file_delete.go` `deleteFacts` (`:122-150`) builds them, invoked at `file_delete.go:46,86`; relay + claim/retry/lease machinery exists (`event_outbox_relay.go`, `TestDeleteObjectWithEvent_OneTx` in `event_outbox_test.go:136`). **However, the `deleted@1.1` fact has no AI consumer** — `deliverFact` routes it to the L2 `AuditSink` or `complete` only (`event_outbox_relay.go:171-225`); the D3 comment explicitly forbids local re-broadcast ("would double-fire webhook/indexer/AV/replication/SSE") | `event_outbox.go:22,96-179`; `event_outbox_relay.go:171-225`; `file_delete.go:46,86,122-150` |
| C2 | "vault.file.deleted@1.1 (proposed) emitted with version_id/storage_key" | Half-done. `deletedFact` payload **has `version_id`** (`internal/events/payload.go:31`) but **not `storage_key`**; golden-byte tests pin the shape (`schema_test.go:31` `TestEventSchema_GoldenJSON`, `:96` `TestEventSchema_Deleted11Envelope`) | `payload.go:24-40`; `schema_test.go` |
| C3 | "event schema: … emit distinguishable event for version deletes" | Version deletes today emit **no outbox fact at all** (`DeleteVersion` → `repo.DeleteObjectVersion`, `file_delete.go:205`); same for delete-marker, quarantine, bucket delete (`notifier.go:68-83` D2 comment names these "E14 paths" that keep the bus path because they have no outbox row) | `notifier.go:68-83`; `file_bucket_settings.go:55-64` |
| C4 | (implied) "repo.HardDeleteObject and repo.DeleteChunksForObject commit separately" | Still true: `DeleteChunksForObject` (`sql_chunks.go:11`) is its own statement. **Mitigating fact:** `chunks.object_id` has `ON DELETE CASCADE` in both dialects (`migrations/{sqlite,postgres}/0004_ai.up.sql:13/:3`), so the repo-row half of hard deletes cascades inside the delete tx on the repo path — but soft deletes never cascade, and BM25/Qdrant are never covered | `sql_chunks.go:11`; migrations `0004_ai` |

---

## 2. Problem statement (current-state gaps, all verified)

1. **G1 — Synchronous invalidation in the business flow.** Six call sites invoke `chunkCleaner.DeleteObjectChunks` inside the request path: `hardDeleteObject`, `softDeleteObject`, `DeleteVersion` (`file_delete.go:27,81,195`), delete-marker (`delete_marker.go:53`), quarantine (`object_worker.go:61`), bucket cascade (`file_bucket_delete.go:70`). With a Qdrant sink this is one blocking HTTP RTT per object (`qdrant.go:283` `wait=true`); failures are warn-only, so stale searchable chunks are the fail-open outcome. This is the exact opposite of "durable_async that never blocks the business flow".
2. **G2 — Non-transactional.** `Indexer.DeleteObjectChunks` commits `repo.DeleteChunksForObject` and then sink deletes as separate transactions (`indexer.go:230-239`). A crash between them leaves sinks stale; a sink failure after the repo delete leaves repo rows gone but the sink entry searchable, with only a job retry to repair.
3. **G3 — The async fallback is not atomic.** `EventDeleted` → bus → `Indexer.dispatch` → `delete_chunks` job (`indexer.go:196-197`) rides the legacy events table: `Bus.Publish → InsertEvent` is a second transaction after the delete committed (`bus.go:84`), subscribers drop on a full buffer (`bus.go:154-155`), and the durable catch-up (`NextUnconsumedEvents` poll) only ever sees events whose *event* insert committed. The atomic outbox fact (`deleted@1.1`, committed with the delete) exists but nothing consumes it for chunk invalidation (C1).
4. **G4 — No durable backstop.** `cleanObjectChunks` (`reconcile/deletion.go:82`) fires only on lifecycle/retention purges; no job sweeps chunks whose object row is gone or `deleted_at` set. Any missed/failed invalidation is permanent.

---

## 3. Functional requirements

> Incremental by design: the atomic fact + claim/retry relay already exist (C1). FRs close the AI gap without re-building the outbox.

### FR-1 — Outbox-driven chunk deletion (the delete_chunks drain)
The `vault.file.deleted@1.1` fact becomes the single durable trigger for chunk invalidation.

- **FR-1.1** Add an optional `ChunkDeleter` port to the relay (`EventOutboxRelayOptions`, alongside `AuditSink`): `DeleteObjectChunks(ctx context.Context, objectID int64) error`. `deliverFact`'s `deleted@1.1` branch (`event_outbox_relay.go:171-196`) invokes it via `deliverDeleted` (`:190-225`) with the fact's `object_id` (from the payload, `payload.go:39`) after/independent of the L2 `AuditSink`; failure takes the existing `retry` path (backoff+jitter → terminal `failed`), success → `complete`. Claim fencing/lease/redelivery and prune are reused unchanged — no new table, no new config beyond optionally binding the port.
- **FR-1.2** Wire in `cmd/server`: bind `ai.Indexer` (already satisfies `service.ChunkCleaner`, `main.go:249`; `DeleteObjectChunks` is exported, `indexer.go:230`) as the relay's `ChunkDeleter` when AI is enabled. The relay is always-on (`cmd/server/workers.go:177`), so no new gating flag.
- **FR-1.3** The drain worker must never block the delete request: relay runs on its own goroutine/timer (`event_outbox_relay.go:Run`), claim batch is disjoint per claimer — no change needed, assert in tests (AC-4).
- **FR-1.4** Telemetry: `event_outbox.chunks_deleted_total` / `event_outbox.chunks_failed_total` (mirror `IncEventOutboxL2Delivered` pattern, `internal/telemetry/metrics.go:96-98`); terminal-failed chunk deletions are counted for the sweep backstop.

### FR-2 — Remove synchronous ChunkCleaner calls from the business flow
Delete the six `s.chunkCleaner.DeleteObjectChunks` invocations (`file_delete.go:27-34,81-87,195-201`; `delete_marker.go:53-56`; `object_worker.go:61-65`; `file_bucket_delete.go:70-75`). Every removed site must have a durable trigger (the `deleted@1.1` fact); per-site coverage:

| Site | Fact today | Change |
|------|-----------|--------|
| `hardDeleteObject` / `softDeleteObject` | ✅ committed atomically (`HardDeleteObjectWithEvent` / `SoftDeleteObjectWithEvent`, `file_delete.go:46,86`) | remove sync calls only |
| `DeleteVersion` | ❌ (`repo.DeleteObjectVersion`, `file_delete.go:205`) | commit `deleted@1.1` for the **removed version row** in the same tx (new `DeleteObjectVersionWithEvent` or fact insert inside the existing tx); payload `version_id` = removed version — makes the fact distinguishable from full-object deletes (C3) |
| delete-marker | ❌ (`InsertDeleteMarker`, `sql_objects_versions.go:10`) | commit `deleted@1.1` in the marker tx with **origin = the shadowed prior-current version's row id** (the chunks actually invalidated today at `delete_marker.go:53`). This also fixes a latent identity bug: the legacy event at `delete_marker.go:69` carries the marker row's id, not the shadowed version's (E10) |
| quarantine | ❌ (`SoftDeleteObjectByID`, `object_worker.go:70`) | add `SoftDeleteObjectByIDWithEvent` (origin = `obj.ID`) |
| bucket cascade | ❌ (`repo.DeleteBucket`, `file_bucket_settings.go:38-64`, call at `:61`) | per-object `deleted@1.1` facts in the `DeleteBucket` tx (loop already has the objects at `deleteBucketData`, `file_bucket_delete.go:55-84`) |

> If any site is deferred, it **must not** silently lose invalidation: deferring a site to the FR-5 sweep backstop is acceptable and must be stated per site in the PR. Default plan: all five rows above.

### FR-3 — Retire the legacy delete→job bridge (no double-fire)
- **FR-3.1** Remove the `EventDeleted → dispatch(JobDeleteChunks)` branch in `Indexer.processEvent` (`indexer.go:196-197`) and the `JobDeleteChunks` registration (`cmd/server/ai.go:134-148`, registration at `:145`). Deletes are now delivered exactly-once-per-claim by the relay (D3: re-broadcast would double-fire).
- **FR-3.2** Keep `EventCreated → index_object` dispatch, `EncodeObjectID`/`DecodeObjectID`, and `DeleteObjectChunks` unchanged; `DeleteObjectChunks` remains the shared implementation of the relay port, the reconcile seam, and the sweep.
- **FR-3.3** The legacy `EventDeleted` emission (`file.go:297-313`) and the bus itself stay untouched for all other consumers (webhook/SSE/audit/AV/replication).

### FR-4 — Idempotency and observable exactly-once contract for `DeleteObjectChunks`
- **FR-4.1** Contract (document in `indexer.go` doc comment): `DeleteObjectChunks` is idempotent — repo `DELETE FROM chunks WHERE object_id=$1` (`sql_chunks.go:11`), BM25 map delete (`bm25.go:210`), and Qdrant delete-by-filter `object_id` (`qdrant.go:281-285`) all no-op on repeat. Relay retries (at-least-once window, D7 semantics in `event_outbox_relay.go`) therefore yield exactly-once *observable* deletion per object.
- **FR-4.2** The repo-first/sinks-after ordering is kept, but the **first** error now flows into relay retry (not a warn in the request path); after `maxAttempts` the fact is terminal-failed and the FR-5 sweep is the backstop.

### FR-5 — Orphan-chunk sweep (durable backstop)
- **FR-5.1** New reconcile job (or extension of `reconcile/job.go` `sweep`): per tenant, scan `chunks LEFT JOIN objects o ON o.id = chunks.object_id` where `o.id IS NULL OR o.deleted_at IS NOT NULL` (covers hard-deleted rows — cascade already removed them on the repo path, so this catches soft-deleted, marker-shadowed `version_tombstone` rows, and any non-cascaded backends), group by `object_id`, call `DeleteObjectChunks` per object. Batch + per-tenant, same shape as `sweepOrphanRows` (`reconcile/job.go:131`); cluster-singleton and interval gating reused (`maybeSweep`, `reconcile/job.go:99-104`).
- **FR-5.2** Gating: `RECONCILE_CHUNK_SWEEP_ENABLED` (default **false**, opt-in per I5; wired via `ReconcileConfig` + `cmd/server/workers.go:85-101` pattern), only effective when `RECONCILE_INTERVAL_MINUTES > 0` and AI indexing was enabled at some point (no-op when the chunks table is empty — cheap query).
- **FR-5.3** Telemetry: `reconcile.chunks_scanned_total` / `reconcile.chunks_deleted_total` per sweep round (mirror `RecordReconcileBlobs`, `reconcile/job.go:125`).
- **FR-5.4** Safety: the sweep never touches chunks of live objects (`deleted_at IS NULL` rows are excluded by the join predicate); a concurrent re-index of a restored object is safe because restore clears `deleted_at` before re-index (soft delete → restore → re-index order makes the sweep's snapshot harmless).

### FR-6 — Event schema: `storage_key` on `vault.file.deleted@1.1`
- **FR-6.1** Add `storage_key` to `deletedFact` (`payload.go:24-40`), populated from `obj.StorageKey` in `BuildDeletedFact` (`file_delete.go` `deleteFacts` already has the object). `version_id` is already present. Payload stays self-contained and byte-stable (fixed field order).
- **FR-6.2** Update `schema_test.go` golden bytes + required-field assertions (`TestEventSchema_GoldenJSON`, `TestEventSchema_RequiredFields`, `TestEventSchema_Deleted11Envelope`) and note the schema change in the sibling `audit-sink-deleted-11-v1.md` (payload shape is pinned there by contract).
- **FR-6.3** Version deletes emit the fact with the removed version's `version_id` (FR-2), so the outbox layer can distinguish "exact version removed" from "whole object removed" without re-querying (the object row may be gone).

---

## 4. Acceptance criteria (preserved from the direction, made testable)

| # | Supplied acceptance | Testable spec |
|---|--------------------|---------------|
| **AC-1** | *unit: DeleteObjectChunks idempotent and sinks all receive delete exactly once per object* | New table-driven test in `internal/ai` (extend `sink_test.go`): real in-memory SQLite repo + counting fake `ChunkSink` + real `BM25` + Qdrant against `httptest` server (`qdrant_test.go:213` pattern). (i) index one object → assert repo rows + sink state present; (ii) call `DeleteObjectChunks` once → repo rows gone, each sink's `DeleteObjectChunks` invoked exactly once, sink state empty; (iii) call again (retry simulation) → no error, sink call counts unchanged (idempotent no-op); (iv) two objects → per-object isolation (calls = 1 per object, exactly-once observable). |
| **AC-2** | *outbox delivery: crash-injection test where HardDeleteObject commits but chunk rows survive → delete_chunks outbox row present and drained worker removes repo chunks + sink entries* | Extend `internal/repository/event_outbox_test.go` + relay test (`event_outbox_relay_test.go`): (i) insert object + chunks; (ii) `HardDeleteObjectWithEvent` with **no** chunk cleanup (simulates the crash window / removed sync call) → assert chunks rows still present (crash window is observable) **and** `HasEventOutboxFact(deleted@1.1)` true; (iii) run one relay `deliverBatch` round with a `ChunkDeleter` bound → assert `DeleteObjectChunks(originID)` was called with the fact's `object_id`, repo chunks gone, fake sink delete recorded; (iv) restart-safety: claim lease expiry → redelivery (existing `TestEventOutboxClaimLeaseExpiryRedelivers` pattern) delivers the same fact again; second delivery is a no-op (AC-1). |
| **AC-3** | *event schema: vault.file.deleted@1.1 (proposed) emitted with version_id/storage_key* | Extend `schema_test.go` (`TestEventSchema_GoldenJSON` `:31`, `TestEventSchema_RequiredFields` `:42`, `TestEventSchema_Deleted11Envelope` `:96`): golden-byte JSON for `deleted@1.1` asserts `version_id` and `storage_key` present and populated for (a) full-object hard delete and (b) single-version delete (distinguishable: `version_id == removed version`; payload built from the removed row — no post-delete re-query). `TestEventSchema_Deleted11Envelope` additionally asserts `object_id` matches the chunk-deletion target. |
| **AC-4** | *composition e2e: admin hard delete with Qdrant sink latency injected completes within budget while chunk deletion drains async, and a deleted file returns zero hits after drain* | New AI-enabled server test in `internal/integration` (extend `fullserver_test.go` — it already boots the outbox relay, `:44`): register an `httptest` Qdrant sink whose `DELETE /points/delete` sleeps ≥ 1 s (latency injection); (i) upload + index file; (ii) admin hard delete via API → assert response returns < 300 ms (budget) while sink is still sleeping (async drain, no RTT in the request); (iii) poll until the relay's `chunks_deleted_total` increments / sink receives the delete → assert `/v1/search`, `/v1/chat`, `/v1/agent` return zero hits for the deleted content (BM25 + Qdrant paths); (iv) kill the sink (delete fails, fact retries → terminal failed) → run the FR-5 sweep once → zero hits again (backstop). |

---

## 5. Test plan (artifacts)

| Test | Location | Gating |
|------|----------|--------|
| AC-1 idempotency/exactly-once table | `internal/ai/sink_test.go` (extend) | `go test ./internal/ai/` |
| AC-2 crash-injection + drain | `internal/repository/event_outbox_test.go`, `internal/events/event_outbox_relay_test.go` (extend) | `go test ./internal/repository/ ./internal/events/` |
| AC-3 schema golden bytes | `internal/events/schema_test.go` (extend) | `go test ./internal/events/` |
| AC-4 composition e2e | `internal/integration/` (new AI-enabled server test) | `go test ./internal/integration/` (local, SQLite + httptest; no Docker) |
| Regression: per-site fact emission | `internal/service/file_delete_test.go`, `delete_marker_test.go`, `object_worker`/bucket tests — assert removed sync-call sites now produce `deleted@1.1` facts and no chunk delete happens in-request | `go test ./internal/service/` |
| Regression: reconcile sweep | `internal/reconcile/` (new `chunk_sweep_test.go` mirroring `scrub_test.go`) | `go test ./internal/reconcile/` |
| Gate | `gofmt` · `go vet` · `go build ./...` · `go test ./...` · ≤ 500 lines/file · stdlib only (I6) | `make check` |

**Required test infra notes:** relay port tests need a `ChunkDeleter` fake (mirror `AuditSink` fake in `event_outbox_relay_test.go`); AC-4 needs an AI-enabled `fullserver` variant — the current one runs "no auth, no AI" (`fullserver_test.go:44`), so the new test boots its own server with `AI_INDEX_ENABLED`-equivalent wiring (BM25 + httptest Qdrant sinks), not a modification of the no-AI baseline.

---

## 6. Non-goals (explicitly rejected, per scope boundary)

1. **Read-path liveness gate** (direction 2 of the analysis): no `objects`-join in retrieval SQL, no fail-closed hit filtering, no result-cache invalidation — specified separately in `rag-liveness-gate-v1.md`.
2. **Notify@1.1 / audit L2 / webhook consumer work** (direction 3 remainder): no changes to `buildS3Event`, notify rules, `AuditSink` delivery, or the legacy `Event` struct's version discrimination beyond FR-6's payload field.
3. **JobPool changes**: no new job types, no `JOBS_*` config changes; `JobDeleteChunks` is retired (FR-3), `JobIndexObject` untouched.
4. **Legacy event bus**: `EventDeleted` emission, bus, and other subscribers (webhook/SSE/audit/AV/replication) are unchanged.
5. **Storage/repository delete semantics**: no change to blob deletion order (`hardDeleteObject` storage-first for WebDAV MOVE rollback), quota accounting, or audit rows.

---

## 7. Risks and open decisions

| # | Risk / decision | Mitigation |
|---|-----------------|------------|
| R1 | **Payload golden-byte churn** (FR-6 `storage_key`): the sibling campaign pins `deleted@1.1` bytes (`schema_test.go`, `audit-sink-deleted-11-v1.md`); both must be updated in the same change set | single PR touches `payload.go` + both schema tests + spec note |
| R2 | **Delete-marker identity bug** (E10): today's legacy event targets the marker row id while chunks of the shadowed version are cleaned; the FR-2 fact must target the shadowed version's id or invalidation misses | covered by FR-2 table row + AC-3(b) assertion |
| R3 | **Double-fire** if the legacy bridge is kept alongside the outbox consumer | FR-3 removes the bridge; `DeleteObjectChunks` idempotency makes any residual overlap harmless (AC-1) |
| R4 | **Terminal-failed facts** leave stale chunks permanently | FR-5 sweep is the durable backstop (AC-4 iv); `chunks_failed_total` makes it observable |
| R5 | **Scope of bucket-delete facts** (FR-2): per-object facts inside `DeleteBucket` tx is the largest single repository change | alternative = explicitly delegate bucket delete to the sweep backstop (documented per site); decision recorded in the implementation PR |
| R6 | Sweep vs concurrent indexing race | FR-5.4 predicate (`o.id IS NULL OR o.deleted_at IS NOT NULL`) never matches live rows; restore clears `deleted_at` before re-index |

---

## 8. Effort

Direction-scored 7/10. Largest items: four repository fact-emission variants (FR-2, ~1 unit), relay `ChunkDeleter` port + wiring (FR-1), sweep job (FR-5), and the AC-4 integration harness. No new dependencies (stdlib + existing chi/otel only, I6).
