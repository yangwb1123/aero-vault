# Requirements: Fail-closed liveness gate on the RAG read path (`internal/ai`)

- **Status:** v1 (evidence-backed; all citations re-verified against the repository at commit `35ff4ce`)
- **Module:** `internal/ai` (+ one read-only seam in `internal/repository`)
- **Direction:** "Fail-closed liveness gate on the RAG read path: search/chat/agent must never return chunks of deleted or soft-deleted objects"
- **Scope boundary:** read-path gating + result-cache invalidation only. **Out of scope:** changing write-side deletion mechanics, the outbox relay, the `vault.file.deleted@1.1` payload shape, audit/notify behavior, or any backend's retrieval SQL.

---

## 1. Evidence verification register

Every citation in the direction was re-checked against the working tree. All hold.

| # | Cited claim | Verified location | Verdict |
|---|-------------|-------------------|---------|
| E1 | `Search.Query` → `filterAuthorizedHits`; `hitAuth==nil` short-circuits; cache bypass tied to `hitAuth` | `internal/ai/search.go:338-371` (`Query`), `:373-385` (`filterAuthorizedHits` — `if s.hitAuth == nil { return hits, nil }` at `:374`), cache read `:342-345` (`if s.results != nil && s.hitAuth == nil`), cache write `:367-368` | ✅ |
| E2 | `repoVectorIndex` → `repo.SearchChunks` | `internal/ai/vectorindex.go:24-31` (`SearchVectors` calls `r.repo.SearchChunks`) | ✅ |
| E3 | `SearchChunks` selects from `chunks` only, no `objects` join, no `deleted_at` filter | `internal/repository/sql_chunks.go:76-101` (`SELECT id, object_id, … FROM chunks WHERE tenant_id=$1 AND embedding IS NOT NULL` at `:81-82`) | ✅ |
| E4 | pgvector raw SQL on `chunks`, no liveness | `internal/ai/pgvector.go:129-146` (`SearchVectors`; `FROM %[2]s` where table is the chunks table) | ✅ |
| E5 | Qdrant external store keyed by `object_id`, no liveness | `internal/ai/qdrant.go:146-160` (`SearchVectors` → `/points/search`, payload carries `ObjectID`) | ✅ |
| E6 | BM25 in-memory retrieval, no liveness | `internal/ai/bm25.go:231-240` (`Search` → `collectCandidates`) | ✅ |
| E7 | Chat and Agent both funnel through `Search.Query` | `internal/ai/chat.go:33` (`NewChat(search *Search, …)`), `:132` (`c.search.Query`); `internal/ai/agent.go:186` (`callSearch`), `:198` (`a.search.Query(… Mode:"hybrid")`) | ✅ |
| E8 | REST + MCP search also funnel through `Search.Query` | `internal/api/rest/search.go:68`; `internal/mcp/server.go:273` | ✅ (strengthens E7: *all* RAG surfaces) |
| E9 | Result cache: TTL-bounded staleness documented; no delete-event invalidation; no invalidation API | `internal/ai/result_cache.go:8-13` (STALENESS comment), full file (only `get`/`put`/`newResultCache`/`resultCacheKey`) | ✅ |
| E10 | `SoftDeleteObject` sets `DeletedAt` — liveness signal exists | `internal/repository/sql_objects_maint.go:20-40` (`UPDATE objects SET deleted_at=$1 …`) | ✅ |

**Additional verified facts the spec relies on (not in the direction's evidence list):**

| # | Fact | Location |
|---|------|----------|
| F1 | The existing "live" definition in the repository is `deleted_at IS NULL`: `GetObject` (and therefore `FileService.Stat`) only ever returns rows with `deleted_at IS NULL`; soft-deleted rows return `ErrNotFound`. `CanReadObject` is exactly `Stat` — so the *permission* filter, when installed, already excludes soft-deleted objects; the gap is when it is **not** installed. | `internal/repository/sql_objects.go:164-177`; `internal/service/access.go:26-28`; `internal/service/file_get.go:127-156` |
| F2 | `hitAuth` is nil in the default configuration: `buildAccessManager` returns nil unless `ACCESS_CONTROL_ENABLED=true`; config default is `false` (I5 default). | `cmd/server/access.go:11-13`; `internal/config/config.go:215-216`; `cmd/server/main.go:224-226` (authorizer installed only `if accessManager != nil`) |
| F3 | `internal/ai` has **zero** liveness awareness today: no reference to `deleted_at` or `IsDeleteMarker` in any non-test file under `internal/ai/`. | `grep -rn "deleted_at\|IsDeleteMarker" internal/ai/` (non-test) — empty |
| F4 | Chunks table schema: `chunks.object_id` FK → `objects(id) ON DELETE CASCADE`; SQLite enables `PRAGMA foreign_keys=ON`. Hard delete therefore cascades chunk rows **on the SQLite repo path only**; in-memory BM25, Qdrant, and Postgres paths are not covered, and a crash between metadata delete and chunk cleanup leaves repo chunks retrievable in every backend class. | `internal/repository/migrations/sqlite/0004_ai.up.sql`; `internal/repository/sqlite.go:31` |
| F5 | Delete event already carries object identity: `Event{ObjectID *int64, TenantID, Bucket, Key, …}`; emitted with the removed object on soft delete (`file_delete.go:53`), hard delete (`:92`), version delete (`:212`). Delete-marker path emits with the **marker** row's ID (`delete_marker.go:69`) while the chunks removed are the prior current version's (`delete_marker.go:53-54`). | `internal/repository/repository.go:175-193`; `internal/service/file_delete.go`; `internal/service/delete_marker.go:48-71` |
| F6 | `InsertDeleteMarker` sets `deleted_at` + `version_tombstone` on the prior current version before inserting the marker row — so the deleted_at predicate already covers delete-marker-shadowed versions; the marker row itself is `deleted_at IS NULL` with metadata `_aero_delete_marker=true` (exclude explicitly). | `internal/repository/sql_objects_versions.go:10-26`; `internal/service/delete_marker.go:11,40,62-65` |
| F7 | Async invalidation path exists and is deduplicated: `Indexer.processEvent` maps `EventDeleted` → `dispatch(JobDeleteChunks, …, DedupeKey "delete_chunks:<objectID>")`; `Indexer.DeleteObjectChunks` removes repo rows then sinks; it is also the registered `ChunkCleaner` invoked synchronously by every deletion path (hard `file_delete.go:27-31`, soft `:81-82`, version `:195-196`, marker `delete_marker.go:53-54`, bucket delete `file_bucket_delete.go:70-71`, quarantine `object_worker.go:61-62`), all warn-only/fail-open. | `internal/ai/indexer.go:192-197,208-215,230-238`; `cmd/server/ai.go:146-152`; `internal/service/file.go:57-64` |
| F8 | Result cache is opt-in and off by default: `AI_SEARCH_CACHE_SIZE` default `0` (disabled), `AI_SEARCH_CACHE_TTL_SECONDS` default `30`; enabled only when size > 0. | `internal/config/config.go:155-156`; `cmd/server/ai.go:68-70` |
| F9 | Chat response exposes `Citations []Hit`; agent exposes `Steps []AgentStep` (tool name/args/result). Both are the deterministic assertion surface for e2e ("no trace of content"). `MockLLM` exists for deterministic chat/agent tests. | `internal/ai/chat.go:112-115`; `internal/ai/agent.go:46-57`; `internal/ai/llm.go:281` |
| F10 | Telemetry counter pattern to follow: per-reason counters exist (`IncIndexerSkip(ctx, reason)`); a search-hit-drop counter is a natural addition. | `internal/telemetry/metrics.go:241` |

**Verdict on the direction's problem statement:** fully confirmed. All five retrieval backends (`repoVectorIndex` brute-force, pgvector, Qdrant, in-memory BM25, pgFTS) read chunk data with no liveness join; the only post-retrieval filter (`filterAuthorizedHits`) is skipped when `hitAuth==nil`, which is the default (`ACCESS_CONTROL_ENABLED=false`); the result cache can serve stale hits of deleted objects in that same default; and every deletion path's chunk cleanup is fail-open (warn-only) with an async fallback that can be lost (drop-on-full bus) or crash-interrupted. Soft-deleted rows keep their `chunks` rows until cleanup, so the leak window is real in the default configuration.

---

## 2. Requirements

### R1 — Liveness predicate definition (normative)

**R1.1** An object is **live** iff a row exists in `objects` with the object's ID and `deleted_at IS NULL` **and** the row is not a delete marker (`metadata["_aero_delete_marker"] == "true"`). This mirrors the repository's existing read definition (`GetObject`, F1) plus explicit marker exclusion (F6).

**R1.2** The repository exposes one batched read-only lookup: given a tenant and a set of object IDs, return the subset of IDs that are live (or an equivalent `id → live` map). It must be a single query (no per-hit N+1 in the common path) and must not mutate state.

- *Rationale:* the lookup is the "verification hook" the direction proposes; the batch shape keeps the per-query cost at O(1) queries for `K ≤ 10` hits (limit clamp at `search.go:151`).
- *Testability:* a `repository.Repository`-level unit test asserts the mapping for: live row, soft-deleted row (`deleted_at` set), absent row (hard-deleted), delete-marker row. A contract test asserting the SQLite path returns `ErrNotFound`-equivalent behavior for soft-deleted rows already exists implicitly via `GetObject` (F1); the new method is tested against the same migrated test DB (`newTestEnv`, `internal/ai/integration_test.go:25-42`).

### R2 — Liveness gate on every hit, every mode, every surface (normative)

**R2.1** `Search.Query` applies the R1 predicate to every hit before it is returned — in **vector**, **bm25**, and **hybrid** modes — **regardless of whether `HitAuthorizer` is installed** (`hitAuth == nil` must not weaken the gate; `filterAuthorizedHits` at `search.go:373-385` remains, but liveness is a separate, unconditional filter).

**R2.2** The gate runs after retrieval/merge (`hitsFromRanked`, `search.go:268-281`, invoked at `:357`) and **before** `applyRerankOrTrim`, `recordUsage`, and cache insertion, so dropped hits never reach usage accounting or the cache.

**R2.3** The gate also applies to **result-cache hits**: a cached entry is only served if every hit in it still satisfies R1 (fail-closed even if invalidation has not yet run, e.g. bus drop or crash window). This is what makes the e2e property "no trace before, during, and after drain" hold with caching enabled.

- *Scope note:* retrieval backends (E3–E6) are **not** required to change — the gate at the single chokepoint (E7/E8: REST `search.go:68`, MCP `server.go:273`, chat `chat.go:132`, agent `agent.go:198` all call `Search.Query`) covers all backends uniformly, including the external Qdrant store that cannot be joined at SQL level.
- *Testability:* unit tests in `internal/ai` drive `Search.Query` with injected backends (`fakeVectorIndex`, `vectorindex_test.go:13-20`) and real repo/BM25, asserting zero hits for deleted objects across all three modes (see AC-1).

### R3 — Fail-closed on lookup error (normative)

**R3.1** If the liveness lookup (R1.2) returns an error for the query, `Search.Query` returns an error to the caller (deny the whole query) — never unfiltered hits. This is the "denies the query" branch of the direction.

**R3.2** Any hit whose object ID is not confirmed live by a successful lookup is dropped (the "drops the hit" branch). Unknown ⇒ not live.

**R3.3** Dropped hits are counted with a telemetry counter following the existing per-reason pattern (`IncIndexerSkip(ctx, reason)` precedent, F10), e.g. `IncSearchHitDropped(ctx, reason)` with `reason ∈ {not_live, lookup_error}`. The counter is observable on `/metrics`.

- *Testability:* a fake repository returns (a) an error from the lookup → `Query` returns an error; (b) a map missing one ID → that hit absent from results, others present.

### R4 — Cache invalidation on the delete event (normative)

**R4.1** The result cache gains an invalidation entry point: remove all cached entries whose hits reference a given object ID (single ID or batch). Bounded by cache capacity (F8: default off; when on, entries are bounded), a scan of entries is acceptable; correctness (no entry referencing a deleted object survives) is the requirement, not the mechanism.

**R4.2** Invalidation fires for **every deletion path that removes an object's chunks**, keyed by the ID of the object whose chunks were removed:

- soft delete — `EventDeleted` carries the removed object's `ObjectID` (F5);
- hard delete — same;
- version delete — same;
- delete-marker — the removed chunks belong to the **prior current version** (`current.ID`, `delete_marker.go:53-54`), so invalidation must be keyed on that ID, not on the marker row's ID that `EventDeleted` carries (`delete_marker.go:69`); the direction's "event carries object identity used for cache invalidation" holds for all non-marker paths via `Event.ObjectID`, and the marker path must deliver `current.ID` to the invalidation hook at the same point the synchronous `ChunkCleaner` call does;
- bucket delete and quarantine paths — same identity rule (F7 lists the call sites).

**R4.3** Wiring freedom: the invalidation hook may be attached to `Indexer.DeleteObjectChunks` (the single function all paths already funnel through as `ChunkCleaner`/job handler, F7) or to the individual call sites; the observable contract is R4.2. The EventBus drop-on-full and job-drain timing must not be relied upon for correctness (R2.3 is the correctness mechanism; R4 is hygiene/latency).

- *Testability:* unit — seed cache with a query whose hits include object X; invoke the delete-event/invalidation path for X; same query returns no X hit (cache still warm for other objects). Delete-marker variant: cache seeded with hits of the version shadowed by `CreateDeleteMarker`; after marker creation, cached hits for that version are purged.

### R5 — Delete-event identity contract (normative, verification only)

**R5.1** No change to the `Event` shape (`repository.go:175-193` already carries `ObjectID`/`TenantID`/`Bucket`/`Key`). Requirement is a **contract test**: for soft/hard/version deletes, the emitted `EventDeleted`'s `ObjectID` equals the object whose chunks were removed; for delete-marker, the identity of the shadowed version is passed to the invalidation hook (R4.2).

- *Testability:* capture emitted events (existing `EventSink` seam, `internal/service/file.go:51-55`) and assert identity; mirror `internal/events/schema_test.go` style for the fact-level assertions if needed.

### R6 — Outbox/job drain removes hits (verification only, no new machinery)

**R6.1** The existing `delete_chunks` job (`JobDeleteChunks`, `indexer.go:21`, dispatch at `:192-197`, handler `:230-238`) drains: after the job runs, repo chunk rows (`DeleteChunksForObject`, `sql_chunks.go:11-14`) and sink entries (`ChunkSink.DeleteObjectChunks`) are gone, and `Search.Query` returns zero hits referencing the object. Re-running the job is idempotent and safe (dedupe key `delete_chunks:<id>`, F7).

- *Testability:* see AC-3.

---

## 3. Acceptance criteria (preserved from the direction, made testable)

The five supplied acceptance checks are preserved verbatim in intent; each is expanded into a concrete, executable test. All unit tests run under `go test ./internal/ai/... ./internal/repository/...` (SQLite+local FS, zero network). The composition e2e runs under `make test-integration` (or the existing full-server harness in `internal/integration/fullserver_test.go`).

### AC-1 — unit: vector/bm25/hybrid queries exclude deleted objects, `hitAuth` nil, failing sink

> "unit: vector/bm25/hybrid queries exclude hits whose object is soft-deleted or hard-deleted even with hitAuth nil and with sink delete injected to fail"

Test matrix (all with `hitAuth` unset — the default config, F2):

1. **Soft-deleted:** `putObject` → index (seed chunks) → `svc.Delete` (soft path) → `Search.Query` in each of `vector`, `bm25`, `hybrid` returns no hit with `ObjectID`/`ObjectKey` of the deleted object. Chunks rows still exist in `chunks` (assert via `ListChunksForObject`) to prove the gate — not the cleanup — is what excludes them.
2. **Hard-deleted:** `putObject` → seed chunks → inject a `ChunkCleaner` whose `DeleteObjectChunks` **fails** (simulating sink failure / crash window, F7 fail-open) → `svc.Delete` (hard) → the object row and (on SQLite) its cascade-deleted repo chunks are gone, but canned hits survive via `fakeVectorIndex` (vector) and in-memory `BM25` (bm25/hybrid) referencing the deleted `ObjectID` → `Search.Query` returns none of them.
3. **Delete-marker (versioned bucket):** enable bucket versioning, upload, seed chunks, `CreateDeleteMarker` with failing `ChunkCleaner` → hits referencing the shadowed version's `ObjectID` are excluded (predicate via F6 `deleted_at` set on the prior version).
4. **Control:** a live object's hits are still returned in all three modes (no regression).

### AC-2 — fail_closed: lookup error drops the hit or denies the query

> "fail_closed: liveness lookup error drops the hit (or denies the query) rather than returning it"

1. Fake repository whose liveness lookup returns an error → `Search.Query` returns an **error** (R3.1); assert no hits returned.
2. Fake repository returning a map that omits one hit's `ObjectID` → that hit absent, other hits present (R3.2).
3. Assert the telemetry counter (R3.3) incremented for the lookup-error case.

### AC-3 — outbox delivery: `delete_chunks` drain removes hits

> "outbox delivery: delete_chunks drain removes hits"

1. `putObject` → seed chunks → hard delete (chunks deliberately surviving, e.g. failing inline `ChunkCleaner` or direct row/sink seeding).
2. Run the `delete_chunks` drain exactly as the job path does: `Indexer.DeleteObjectChunks(ctx, objectID)` (or enqueue `JobDeleteChunks` through `JobPool` in a `JOBS_WORKERS>0` harness — matches `main.go` wiring).
3. Assert: repo chunk rows gone (`ListChunksForObject` empty), sink entries gone (`BM25.Search`/Qdrant stub empty), `Search.Query` returns zero hits in all modes.
4. Re-run the job; assert idempotent (no error, no change) — the `DedupeKey` behavior at `indexer.go:208-215` is exercised.

### AC-4 — event schema: delete event carries object identity used for cache invalidation

> "event schema: delete event carries object identity used for cache invalidation"

1. Unit (cache): enable `WithResultCache`; run a query returning hits of object X; warm entry present. Invoke the delete-event path for X (emit `EventDeleted{ObjectID: X}` through the indexer/invalidation hook as wired in R4). Same query returns no X hit; a second live object Y's hits in the same cached entry remain.
2. Unit (identity): capture `EventDeleted` from soft, hard, and version deletes; assert `ObjectID != nil` and equals the removed object's ID (F5). For delete-marker: assert the invalidation hook receives the shadowed version's ID (`current.ID`).
3. Contract: with caching enabled and the delete arriving via the bus subscriber (as `buildIndexer` wires it, `cmd/server/ai.go:146-152`), cache entries referencing the deleted object are purged after event processing.

### AC-5 — composition e2e: no trace before, during, and after outbox drain

> "composition e2e: delete file via admin API → /search, /chat, /agent return no trace of its content before, during, and after outbox drain"

Full-server composition test (harness: `internal/integration/fullserver_test.go`; AI enabled, `AI_INDEX_ENABLED=true`, embedder configured, `MockLLM` for chat/agent determinism, result cache enabled with a TTL long enough that staleness would otherwise be observable):

1. Upload a file whose content contains a unique marker string; wait for indexing; assert `/v1/search` returns it, `/v1/chat` citations reference it, `/v1/agent` search-tool output contains it. Warm the result cache with the exact query used later.
2. Delete via the public delete API — **REST `DELETE /v1/files/{key}`** (the direction says "admin API"; there is no admin delete endpoint in the REST surface — `admin/{tenants,keys,jwt,jobs,config,webhook-failures}` only — so the real service delete path is used, which is what emits the event and drives the outbox).
3. **Before drain:** with the chunk-cleanup job path stalled (queue drained/disabled or a failing sink), immediately re-run `/v1/search` (same query — cache hit path), `/v1/chat`, `/v1/agent`: no response may contain the marker string; `/v1/search` returns no hit with the deleted `object_key`; `/v1/chat` citations contain no deleted chunk; `/v1/agent` steps/tool output contain no deleted chunk (F9).
4. **During drain:** run the `delete_chunks` drain; repeat the three endpoints at each observable intermediate state (chunks partially removed); still no trace.
5. **After drain:** assert chunk rows and sink entries are gone (AC-3) and the three endpoints still return no trace.
6. **Soft-delete variant:** repeat steps 2–5 with a soft delete (object row `deleted_at` set, chunks rows present throughout — AC-1.1 matrix), asserting the gate holds in the default `hitAuth==nil` configuration (`ACCESS_CONTROL_ENABLED=false`).

---

## 4. Test plan summary

| Layer | Where | Tests |
|-------|-------|-------|
| Repository unit | `internal/repository/*_test.go` | R1.2 mapping (live / soft-deleted / absent / marker rows) on migrated SQLite |
| AI unit | `internal/ai/` (`search_test.go` additions; seams: `fakeVectorIndex`, `newTestEnv`, real `BM25`, `WithResultCache`) | AC-1 matrix, AC-2, AC-4.1, cache invalidation incl. marker variant |
| AI integration | `internal/ai/integration_test.go` | AC-3 job drain + idempotency |
| Contract | `internal/events/`-style or `internal/ai/` | AC-4.2 identity assertions (soft/hard/version/marker) |
| Composition e2e | `internal/integration/fullserver_test.go` | AC-5 (REST delete → search/chat/agent, cache warm, before/during/after drain; soft-delete variant) |

All tests follow the repository test conventions (stdlib `testing` only, I6; SQLite `file:` temp DB + `Migrate`, `MockLLM`/`NewHashEmbedder` for determinism).

## 5. Risks and notes

- **Per-query cost:** the gate adds one batched lookup per query (R1.2) plus one per cache hit. With `K ≤ 10` this is negligible relative to embed+retrieve; the batch shape is a requirement to keep it O(1) queries.
- **`hitAuth` redundancy:** when `ACCESS_CONTROL_ENABLED=true`, `CanReadObject`→`Stat` already excludes soft-deleted objects (F1); the gate is strictly additive and must not weaken that path.
- **Delete-marker identity quirk** (F5/F6): the emitted `EventDeleted` for markers carries the marker row's ID, which is *not* the ID of the object whose chunks are removed; R4.2 mandates keying on the removed object's ID. This is the one place where "the event carries the identity" does not hold verbatim — the spec resolves it without changing the event shape (out of scope).
- **SQLite FK cascade** (F4) means the hard-delete repo-chunk case is partly self-healing on the default path; the gate's value is concentrated in soft-delete windows, external sinks (Qdrant), in-memory BM25, Postgres, and crash/bus-drop windows — all covered by AC-1/AC-5.
- **Not changed by this spec:** write-side cleanup ordering, event payloads, outbox relay, per-backend SQL, `Hit` JSON shape, API contracts.
