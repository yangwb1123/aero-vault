# aero-vault — Engineering Roadmap

aero-vault is already broad and well-tested: four protocols (REST, S3, WebDAV,
MCP) over one `FileService`, pluggable storage and metadata DB, multi-tenancy,
auth, a durable job queue, a full RAG pipeline, and OTel/Prometheus telemetry.

So this roadmap is **not** a list of missing features. It targets the gap that
separates a feature-complete single-node service from a system that survives
**production scale, multiple replicas, real tenants, and real cost.** Each
direction below is anchored to the code it builds on, with the *why* stated in
impact terms — not "nice to have."

## Priority at a glance

| # | Direction | Type | Impact | Effort | Risk |
|---|-----------|------|--------|--------|------|
| 1 | Scalable vector retrieval | Performance | 🔴 Core value-prop ceiling | M | Low–Med |
| 2 | Observability, cost & backpressure | Ops / Perf | 🔴 Can't operate without it | M | Low |
| 3 | Horizontal scale-out & HA | Architecture | 🔴 Blocks >1 replica | L | Med–High |
| 4 | Operational control plane (tenants/keys/secrets) | Feature / Security | 🟠 Blocks real SaaS | M | Med |
| 5 | Data-integrity & large-object hardening | Edge cases | 🟠 Bites under failure/scale | S–M each | Low–Med |
| 6 | Production resilience & circuit breakers | Reliability | 🔴 Cascading failure risk | M | Low |
| 7 | S3 feature parity | Interop | 🟠 Migration blocker | L | Low |
| 8 | Content integrity & self-healing | Reliability | 🟠 Silent corruption risk | M | Med |
| 9 | Storage tiering & intelligent lifecycle | Cost | 🟠 Uncompetitive at scale | L | Low–Med |
| 10 | Metadata HA & disaster recovery | Architecture | 🟠 Single point of failure | L | Med–High |

**Suggested sequencing:** #1–#5 first (foundation), then #6 (resilience),
#7 + #9 in parallel (integration + cost), #8 + #10 as the last mile.

---

## 1. Scalable vector retrieval

**Today.** `repository.SearchChunks` (`internal/repository/sql.go`) does a
*brute-force* cosine search: it `SELECT`s **every chunk for the tenant**, loads
the `BYTEA`/`BLOB` embeddings into memory, scores them in Go, and sorts. The
code comment is candid — *"scales to ~100K chunks per tenant."* Lexical search
(`internal/ai/bm25.go`) is worse for scale-out: the entire BM25 index is rebuilt
**in memory, from all chunks, every 30 seconds** (`cmd/server/main.go`).

**Why it matters.** Retrieval *is* the product's differentiator. Both cost and
latency grow **linearly with corpus size**, per query, on the request path — so
one large tenant degrades search for everyone, and the usable corpus is capped
well below what customers will bring. This is the single clearest line between
"impressive demo" and "production RAG."

**Direction.** Introduce a `VectorIndex` abstraction behind the existing
`SearchChunks` seam (mirrors how `storage.Storage` already abstracts backends).
✅ *Seam implemented.* `ai.VectorIndex` (`internal/ai/vectorindex.go`) now
fronts retrieval; `Search` calls `s.vindex.SearchVectors(...)` instead of the
repository directly, defaulting to the brute-force `repoVectorIndex` and
overridable via `WithVectorIndex`. A pgvector/Qdrant adapter now has a clean
home with **no change to `Search`**. **External vector store** ✅ *also shipped*:
`ai.QdrantIndex` (`internal/ai/qdrant.go`) implements **both** seams — the read
seam (`VectorIndex.SearchVectors`) and the write seam (`ChunkSink`) — over
Qdrant's REST API (stdlib `net/http`, no new deps), wired opt-in via
`AI_VECTOR_BACKEND=qdrant` (+ `AI_VECTOR_URL`/`_API_KEY`/`_COLLECTION`) and
registered on both `Search` and the indexer so writes propagate. Pinned by
`httptest` contract tests; live round-trip against a real Qdrant is opt-in CI
via `make test-integration-qdrant` (Docker); skips gracefully when no Qdrant is
reachable.
- **Postgres path:** pgvector with an HNSW/IVFFlat index → approximate top-k in
  the database, not the app. Move lexical search to Postgres FTS (`tsvector`) or
  a persisted/incremental BM25 so it isn't rebuilt from scratch per instance.
  **Incremental BM25** ✅ *shipped*: the in-memory index now implements
  `ChunkSink` and is maintained incrementally on every index/delete (O(1)
  bookkeeping for `df`/`avgLen` via a running length total + an objectID→chunkIDs
  map), so the 30-second full-corpus rebuild is gone (one build at startup, then
  live upserts/deletes); concurrency-safe (race-tested).
  ✅ *Adapters shipped and **runtime-verified**.* `ai.PgVectorIndex`
  (`internal/ai/pgvector.go`, cosine `<=>` ANN) implements `VectorIndex`;
  `ai.PgFTSIndex` (`internal/ai/lexicalindex.go`, `to_tsvector`/`ts_rank`)
  implements the new `LexicalIndex` seam. Both are wired opt-in
  (`AI_VECTOR_BACKEND=pgvector`, `AI_LEXICAL_BACKEND=pgfts`, `AI_VECTOR_DSN=…`)
  and default off; `Search` routes through both seams. **Verified end-to-end
  against a live pgvector Postgres** (`internal/integration`, `make
  test-integration`): pgvector returns the nearest neighbour and pgFTS ranks the
  lexical hit. Operator provisioning SQL (vector column + HNSW/GIN indexes) is in
  `deploy/postgres/pgvector-setup.sql` and confirmed to apply on PG 16.
- **Dev path:** keep the brute-force/SQLite implementation for zero-dependency
  local use.
- **External option:** ✅ *shipped*: `ai.QdrantIndex` (`internal/ai/qdrant.go`) is
  the Qdrant adapter — implements both `VectorIndex` (read) and `ChunkSink`
  (write), wired opt-in via `AI_VECTOR_BACKEND=qdrant`.
- ✅ **Embedding-model identity at query time** enforced: `Search` drops vector
  hits whose `embed_model` differs from the query embedder's `Name()`, so
  vectors from different models are never compared even at matching dimension;
  re-index on model change via `AI_REINDEX_STALE_ON_START`. See Direction #5.

---

## 2. Observability, cost governance & backpressure

**Today.** Telemetry exposes exactly two instruments —
`http.server.requests` and `http.server.duration_ms` (`internal/telemetry`).
There are **no domain metrics**: no bytes/objects stored per tenant, no job
queue depth/lag/failure rate, no embedding/search/LLM latency, no token counts.
`ai_usage` rows capture *what* was consumed (chunk/object IDs) but **not tokens,
cost, or latency** (`repository.Usage`). The job queue and the per-request LLM
calls are **uncapped** — rate limiting is a single generic per-tenant RPS bucket.

**Why it matters.** You cannot capacity-plan, alert on, or bill a storage+AI
platform from HTTP metrics alone. AI features carry **real per-call dollar cost**
that today no tenant budget constrains — a runaway client or a loop in an agent
is an unbounded cost event. An unbounded job queue is an outage vector under
burst.

**Direction.**
- **Domain metrics:** queue depth/lag/failures, embedding & search & LLM
  latency, token usage, cache hit rate, storage bytes/objects per tenant — plus
  a starter Grafana dashboard and alert rules (`deploy/grafana` already exists).
  ✅ *First wave implemented.* `internal/telemetry/metrics.go` adds domain OTel
  counters — `ai_requests`/`ai_tokens`/`ai_cost_micros` (by tenant+model),
  `reconcile_orphan_blobs`(+`_deleted`), `idempotency_replays` — recorded at
  their seams and verified to surface in the Prometheus scrape. **Observable
  gauges** ✅ *also added*: `jobs_pending` (queue depth) + per-tenant
  `storage_bytes`/`storage_objects` (via `repo.ListTenantQuotas`), collected on
  scrape (verified). Ships with a Grafana dashboard
  (`deploy/grafana/aero-vault-ai-ops-dashboard.json`, **12 panels**) and
  Prometheus alert rules (`deploy/prometheus/alerts.yml`, **4 groups, 8
  rules**). **Embedding/search latency histograms** ✅ *added*
  (`ai_embed_duration_ms{model}`, `ai_search_duration_ms{mode}`). **Dashboard
  extended** ✅: 6 new panels (ids 7–12) cover embed/search latency p50/p95,
  embed requests+tokens/sec by model, job queue depth, and storage
  bytes/objects per tenant. **Alert coverage extended** ✅: `aero-vault-ai-latency`
  group adds `HighEmbedLatencyP95` (p95 > 500ms for 10m), `HighSearchLatencyP95`
  (p95 > 2s for 10m), and `JobQueueDepthHigh` (> 1000 for 5m). *Remaining
  (external):* validating live dashboards against a running Grafana/collector.
- **Cost accounting:** extend `Usage` with token counts + estimated cost +
  latency, and enforce **per-tenant AI budgets/quotas** at the `Chat`/`Embed`
  seam (reuse the existing quota machinery). ✅ *Accounting implemented.*
  `ai_usage` now carries `model`/`prompt_tokens`/`completion_tokens`/
  `total_tokens`/`latency_ms`/`cost_micros` (migration `0014`); the Chat seam
  records real token usage + measured latency + estimated cost (priced via
  `AI_COST_PROMPT_PER_1K` / `AI_COST_COMPLETION_PER_1K`), and the `/v1/lineage`
  API surfaces them. **Per-tenant daily budget enforcement** ✅ *also implemented*
  (`AI_TENANT_DAILY_BUDGET_USD`): the chat seam sums the tenant's recorded spend
  for the current UTC day via `repo.SumAICostMicros` and rejects over-budget
  calls before invoking the LLM (REST returns `402 BudgetExceeded`). **Per-tenant
  budget overrides** ✅ *added* (`AI_PER_TENANT_BUDGETS` + `PUT /v1/admin/tenants/
  {tenant}/budget`): each tenant can override the global default via its stored
  quota row (`daily_budget_micros`); the override wins when set and enforces even
  without a global cap. **Embedder usage** ✅ *added*: the HTTP embedder parses the
  provider's `usage` and surfaces it as `ai_embed_requests_total` /
  `ai_embed_tokens_total{model}`, so embedding spend is observable alongside chat.
- **Backpressure:** queue-depth caps with `429 + Retry-After`, and a
  dead-letter path for jobs that exhaust retries (the admin `ListJobs`/`RetryJob`
  surface already exists to operate it). ✅ *Implemented.* The durable queue
  already dead-letters (terminal `failed` status after `max_attempts`, operable
  via admin retry); added a **depth cap** — `Queue.WithMaxDepth`
  (`JOBS_MAX_DEPTH`) backed by `repo.CountJobsByStatus("pending")` returns
  `ErrQueueFull` once the backlog is reached, so enqueue sites can shed load /
  return 429. **AI-specific rate limiting** ✅ *added*: `AI_RATE_LIMIT_RPS` /
  `AI_RATE_LIMIT_BURST` create a second per-tenant token bucket applied only to
  `/v1/search`, `/v1/chat`, `/v1/chat/stream`, `/v1/agent`, `/v1/lineage`, so
  expensive AI operations have an independent quota from storage I/O. **Indexer
  skip observability** ✅ *added*: `indexer_skip_total{reason}` counter
  (reason=`unsupported`/`error`/`empty`) surfaces in Prometheus so operators
  can see how many objects are skipped and why.
- **Caching:** memoize embeddings and hot search results to cut both latency and
  cost. ✅ *Embedding cache implemented.* `ai.NewCachingEmbedder`
  (`AI_EMBED_CACHE_SIZE`) wraps any embedder in a bounded in-memory cache so
  recurring query embeddings skip the provider — concurrency-safe, opt-in,
  pass-through when disabled. **Hot-search-result cache** ✅ *also implemented*
  (`Search.WithResultCache` / `AI_SEARCH_CACHE_SIZE` + `_TTL_SECONDS`): identical
  normalized queries skip embed+retrieval+rerank, bounded + TTL'd to cap
  staleness, opt-in, returns copies so callers can't mutate cached entries.

---

## 3. Horizontal scale-out & high availability

**Today.** Several subsystems assume a single process:
- The **event bus is in-process** (`internal/events/bus.go`): it broadcasts only
  to *local* subscribers and **drops events at a 64-deep buffer** under load.
  SSE clients therefore see only the events of the instance they're pinned to.
- **Singleton jobs run on every replica.** `cmd/server/main.go` unconditionally
  launches `reconcile`, `lifecycle`, and the 30s BM25 rebuild in every process.
  With N replicas that is N× duplicated sweeps **racing each other's deletes**
  and N× full index rebuilds.
- Auth/config state is per-process (see #4).

The durable, `SKIP LOCKED` job queue is the one piece that *already* scales
horizontally — which shows the intended direction.

**Why it matters.** Any real deployment needs ≥2 replicas for HA and throughput.
Today, scaling out silently introduces duplicated/raced background work, lossy
and partitioned live events, and config drift between instances. This blocks the
most basic production topology.

**Direction.**
- **Pluggable event transport** for cross-instance, at-least-once fan-out:
  Postgres `LISTEN/NOTIFY`, Redis Streams, or NATS behind an interface; keep the
  in-process bus as the default/dev implementation. SSE then reflects
  cluster-wide events. ✅ *Postgres transport shipped (opt-in; runtime-verified).*
  `events.PostgresTransport` (`postgres_transport.go`) bridges the bus via
  `pg_notify`/`LISTEN` with reconnect-backoff. The `Bus` gained a `WithTransport`
  hook (fires on local-origin `Publish`) + a broadcast-only `Deliver` (for remote
  events) — a **loop-free design verified by a unit test** (local publish →
  transport fires once; remote deliver → no persist, no re-notify). Wired opt-in
  (`EVENTS_TRANSPORT=postgres`). **The live LISTEN/NOTIFY round-trip is verified
  end-to-end** against a real Postgres (`internal/integration`
  `TestPgEventTransport`, `make test-integration`): publish → `pg_notify` →
  `WaitForNotification` → deliver.
- **Cluster singletons** via leader election or DB advisory locks, so
  reconcile/lifecycle/index-build run **once per cluster**, not once per pod.
  ✅ *Implemented (opt-in via `RECONCILE_CLUSTER_SINGLETON`).* A generic `leases`
  table (migration `0013`) + atomic `repo.AcquireLease` (renew-own /
  take-over-on-expiry) now gate the **destructive** reconcile and lifecycle sweeps
  so only the lease holder runs them; a dead holder's lease frees after ~2
  intervals. (The 30s full BM25 rebuild is **gone** — the in-memory index is now
  maintained incrementally via `ChunkSink`, #1; each replica still holds its own
  in-memory index, and the shared-index fix is the opt-in Qdrant/pgvector external
  store, #1.) The lease gating is now a reusable **`cluster.Singleton`** helper
  ✅ *added* (`Enable`/`Guard`, fail-safe on lease error) that the reconcile,
  lifecycle and retention sweeps share.
- **Shared index + config** ✅ *addressed*: the external vector stores
  (#1: Qdrant / pgvector) serve as the shared vector index — replicas read/write
  the same external store rather than each holding a private copy. API keys and
  tenant records are persisted in the shared DB (#4: `AUTH_PERSIST_KEYS`,
  migration `0015`), so config changes survive restart and propagate across
  replicas.

---

## 4. Operational control plane: tenants, keys & secrets

**Today.** The admin API *looks* complete — `AddKey`, `RevokeKey`, `IssueJWT`,
`SetQuota` exist (`internal/api/rest/admin.go`). But underneath, the auth
`Registry` is an **in-memory `map[string]Key` holding plaintext tokens**
(`internal/auth/auth.go`): runtime key changes are **lost on restart, never
shared across replicas, and never hashed.** Signing keys, the SSE encryption
key, the JWT secret, and provider credentials all come from **plaintext env
vars** with no rotation or expiry.

**Why it matters.** A multi-tenant SaaS must onboard tenants and
rotate/revoke/expire credentials **without a redeploy**, have those changes
**survive restarts and propagate to every replica**, and never store secrets in
plaintext — the latter is a hard blocker for SOC2/ISO and most security reviews.

**Direction.**
- **Persist API keys** in the repository (tokens **hashed**, with scopes,
  expiry, and last-used). ✅ *Implemented (opt-in via `AUTH_PERSIST_KEYS`).*
  A new `api_keys` table (migration `0012`) stores **sha256-hashed** tokens with
  scopes/label/expiry/last-used; the `Registry` consults it (alongside in-memory
  env keys) via a decoupled `PersistentStore` interface, so admin `AddKey`/
  `RevokeKey`/`ListKeys` survive restart and are shared across replicas, and
  expired keys are rejected. **Read-through cache** ✅ *added*
  (`Registry.WithKeyCache` / `AUTH_KEY_CACHE_TTL_SECONDS`): bounded TTL'd cache
  in front of the store lookup, invalidated locally on add/revoke. **Event-driven
  cross-replica invalidation** ✅ *added* (`Registry.WithKeyChangePublisher` /
  `InvalidateCachedKey`): a successful persisted add/revoke broadcasts the token
  hash over a **dedicated** Postgres LISTEN/NOTIFY channel (`aero_key_invalidate`,
  reusing `EVENTS_TRANSPORT_DSN`), so other replicas drop the entry immediately
  rather than waiting out the TTL. The channel is separate from the lifecycle bus,
  so key hashes never reach webhooks or the durable event log. Persisted **tenant**
  records ✅ *done* (see the tenant-CRUD item below).
- **Pluggable secret backend** for the envelope-encryption keys, with **key
  versioning + rotation** ✅ *done*. A `storage.SecretProvider` interface supplies
  versioned master keys; the object envelope records the key id (`kid`) so the
  master key rotates **without rewriting objects** — old objects decrypt under
  their original version, new writes use the current one. Two built-in providers:
  `env` (single passphrase = pre-existing behaviour; stamps no id → byte-compatible
  with old envelopes) and `keyfile` (`STORAGE_LOCAL_SSE_KEYFILE` — a JSON key ring
  with a `primary` pointer). Background **re-wrap** ✅ *added*
  (`STORAGE_SSE_REWRAP_ON_START` → `storage.RewrapStale`): on boot, objects still
  on an older key id are re-wrapped onto the current key — rewriting only the
  sidecar envelope (the body is untouched), idempotent. An **HTTP secret-store
  provider** ✅ *added* (`STORAGE_LOCAL_SSE_KEY_URL` + `_TOKEN`) fetches the key
  ring from a Vault-KV-style endpoint (bearer auth). A **remote-wrap KMS client**
  ✅ *added* (`DataKeyWrapper` + `STORAGE_LOCAL_SSE_KMS_URL`): the per-object data
  key is wrapped/unwrapped over HTTP (`/wrap`, `/unwrap`) so the master key **never
  reaches aero-vault**. *Remaining (external):* validating against a real KMS
  deployment.
- Admin CRUD for tenants, quotas, and bucket policies, with an **audit trail**
  (the persisted event log is a natural foundation). ✅ *Tenant CRUD + quotas +
  bucket policies done.* Persisted `tenants` table (migration `0015`, verified on
  real Postgres) + `repo` CRUD; admin endpoints `POST/GET /v1/admin/tenants`,
  `DELETE /v1/admin/tenants/{tenant}`, `PUT …/{tenant}/status` (active|disabled).
  Quotas (`SetQuota`) and bucket policy/lifecycle/ACL admin already existed.
  **Audit trail** ✅ *added*: an `audit_log` table (migration `0016`, verified on
  real Postgres) records admin/security actions (key add/revoke, tenant
  create/delete/status, quota set) with actor/action/target/detail; exposed via
  `GET /v1/admin/audit`.

---

## 5. Data-integrity & large-object hardening (edge cases)

A cluster of correctness gaps that stay invisible in demos and surface exactly
under retry, burst, or failure recovery:

- **Reconcile orphan-blob cleanup.** ✅ *Implemented.* `internal/reconcile` now
  enumerates all buckets per tenant (new `repo.ListBuckets`) for the orphan-row
  direction, and implements the orphan-*blob* direction: a storage-driven walk
  scoped to the `<tenant>/` prefix checks each physical key against
  `repo.StorageKeyReferenced` and, when `RECONCILE_DELETE_ORPHAN_BLOBS=true`,
  deletes the unreferenced ones older than `RECONCILE_ORPHAN_GRACE_MINUTES`
  (default 60). Detect-and-log is the safe default; versioned (`@v…`) and
  soft-deleted blobs are protected. ✅ *Done:* the counts surface as metrics
  (`reconcile_orphan_blobs`/`_deleted`, see #2) and the destructive sweep runs as
  a cluster singleton (`RECONCILE_CLUSTER_SINGLETON`, see #3).
- **Write idempotency.** ✅ *Implemented.* `/v1` object mutations now honor an
  `Idempotency-Key` header (opt-in, Stripe-style): a retried `PUT`/`POST`/`DELETE`
  replays the original response (`Idempotency-Replayed: true`) instead of
  creating a duplicate version. Backed by a persisted `idempotency_keys` table
  (migration `0011`), keyed by `(tenant, key)`, with a request fingerprint,
  409 on key-reuse/in-flight, 5xx-releases-claim, and fail-closed on store
  errors. **TTL/GC** ✅ *added* (`IDEMPOTENCY_TTL_HOURS` →
  `repo.DeleteIdempotencyKeysBefore`, swept by the `RetentionJob`). **Body-hash
  fingerprint (v2)** ✅ *added* (opt-in, `IDEMPOTENCY_HASH_BODY`): the fingerprint
  folds in a SHA-256 of the request body so the same key replayed with *different
  bytes* is rejected (409) instead of replaying.
- **Large objects buffered fully in memory.** ✅ *Fixed.* The WebDAV read and
  write paths now use a bounded `spillBuffer` (`internal/api/webdav/spill.go`):
  ≤8 MiB stays in RAM, larger payloads spill to a temp file (removed on Close).
  Range/Seek preserved; covered by a 9 MiB round-trip + Range test. **`Rename`'s
  copy path** ✅ *also fixed*: MOVE now streams the object through the same bounded
  `spillBuffer` (no more `io.ReadAll`) and carries the source's metadata/tags.
- **Embedding-model drift.** ✅ *Fixed.* `Search` now drops vector hits whose
  `embed_model` differs from the query embedder's `Name()`, so vectors from
  different models are never compared even at matching dimension (guard test in
  `internal/ai/drift_test.go`). **Re-index on embedder change** ✅ *added*:
  `Indexer.ReindexStale` (via `repo.ListObjectIDsToReindex`) re-embeds objects
  whose chunks use a different model; opt-in one-shot on boot via
  `AI_REINDEX_STALE_ON_START`.
- **Soft-deleted rows are never GC'd.** ✅ *Fixed (opt-in).* A `RetentionJob`
  (`internal/reconcile/retention.go`, `RECONCILE_RETENTION_DAYS`) permanently
  purges rows soft-deleted longer ago than the window — and their blobs — via
  `repo.ListSoftDeletedBefore`; it honors object-lock and runs as a cluster
  singleton. The **dropped-event metric** for #3's 64-buffer is ✅ done
  (`events_dropped_total` via `telemetry.IncEventDropped` / `Bus.Dropped()`).

These are individually small but collectively define whether the platform is
**trustworthy with data** when it's busy — which is when trust matters most.

---

## 6. Production resilience — circuit breakers & bulkheads

**Today.** Every storage backend call (S3, OSS, COS, or local disk) runs directly
on the request goroutine with **no timeout, no circuit breaker, and no fallback.**
If S3 becomes slow or unavailable (`internal/storage/s3.go` uses Go's default
`http.Client` with no timeout), every in-flight request blocks until TCP
connection timeout (~30–120s). There is no per-tenant or per-backend concurrency
limiter — one slow tenant can exhaust the goroutine pool for everyone. The
rate limiter (`internal/middleware/ratelimit.go`) is per-tenant token-bucket only,
it does not protect against backend-side degradation.

**Why it matters.** In production, storage backends degrade (network partition,
throttling, slow query). Without resilience patterns, a slow S3 backend becomes
a total service outage. Circuit breakers and bulkheads are the difference between
a partial degradation and a cascading failure.

**Direction.**
- **Per-backend circuit breaker** — wrap `storage.Storage` calls in a
  `gobreaker`-style circuit breaker at the `FileService` boundary. Configurable
  thresholds: open after N% errors in a sliding window, half-open after M
  seconds. Fail-fast with a clear error (`ErrBackendUnavailable`) instead of
  blocking.
- **Storage client timeouts** — add connect/read/write timeouts to every
  storage backend's HTTP client. Currently `s3.go`, `oss.go`, and `cos.go`
  all use `http.DefaultClient` (no timeout). At minimum: 5s connect, 30s read,
  30s write per request.
- **Request concurrency limiter** — a middleware that limits in-flight requests
  across all tenants with a weighted semaphore (1 unit per GET/HEAD, 2 per
  PUT/DELETE with body). Return `429 Too Many Requests` with `Retry-After` when
  the limit is reached. Configurable via `MAX_INFLIGHT_REQUESTS`.
- **Degradation mode** — when the AI backend is unreachable or consistently
  slow, `/search` and `/chat` return `503` immediately instead of waiting for
  timeout. Detect via a health-check goroutine that probes the embed/chat
  endpoints every 10s.
- **Per-tenant resource quotas on storage calls** — extend the existing
  `RATE_LIMIT_RPS` to also cap concurrent I/O per tenant. A tenant that
  exceeds `RATE_LIMIT_BURST` gets `429` regardless of the global concurrency
  pool.

---

## 7. S3 feature parity

**Today.** The S3-compat handler covers the popular subset: object CRUD, multipart
upload, copy, batch delete, tagging, ACL (canned), versioning (get/put/list),
bucket lifecycle, bucket location, object lock, and SigV4 auth
(`internal/api/s3compat/`). However, several widely-used S3 features are missing:

| S3 API | Status | Impact |
|--------|--------|--------|
| `GET/PUT ?policy` (bucket policies) | ❌ Missing | Primary S3 auth mechanism; customers migrating from S3 expect it |
| `GET/PUT ?notification` | ❌ Missing | Hook S3 events to external systems (webhook equivalent) |
| `GET/PUT ?cors` (per-bucket CORS) | ❌ Missing | Currently only global CORS middleware — tenants can't self-serve |
| `GET/PUT ?logging` (server access logs) | ✅ Implemented | Best-effort S3 access-log objects plus durable request rows |
| `POST ?select` (S3 Select) | ❌ Missing | Query CSV/JSON/Parquet objects server-side |
| `POST ?restore` (Glacier restore) | ❌ Missing | Needed when #9 adds cold storage tier |
| ListObjectsV2 with `?tag-key` | ❌ Missing | Filter listing by tags |
| `PUT ?tagging` on CreateMultipartUpload | ❌ Missing | Tag objects during multipart initiation |
| `GET/PUT ?accelerate` (Transfer Acceleration) | ❌ Missing | CDN-style accelerated uploads |

**Why it matters.** S3 compatibility is the primary integration surface. Every
missing API is a reason a potential user chooses a different backend. Many of
these (bucket policies, notifications, logging) are hard requirements for
Regulated/enterprise workloads.

**Direction.** Implement each missing endpoint as an isolated handler within
`internal/api/s3compat/`. Each is small-to-medium (50–150 lines), well-bounded
by the existing XML codec (`xml.go`) and error types (`errors.go`). Priority
order:

1. **Bucket policies** (`?policy`) — persist a JSON policy document in the
   bucket config; evaluate at auth time (extends `internal/auth`).
2. **Bucket CORS** (`?cors`) — per-bucket CORS rules evaluated by a middleware
   before the global CORS; revert to global as fallback.
3. **Server access logging** (`?logging`) — record every S3 request after the
   response completes, writing a best-effort log object to the configured
   target bucket/prefix and a durable request row for audit/reconciliation.
4. **Bucket notifications** (`?notification`) — map S3 event types to the
   existing webhook infrastructure (`internal/events/webhook.go`).
5. **S3 Select** — SQL expression evaluated via `encoding/csv` + `encoding/json`
   with a streaming result; pure-Go, no dependencies.
6. **Tag-based listing** — extend `ListObjectsV2` response with tag filtering.

---

## 8. Content integrity & self-healing

**Today.** The only content verification is the storage backend's native ETag
(MD5 for S3, random hash for local FS). There is **no user-visible content
checksum** on PUT or GET — no `x-amz-checksum-sha256` or `x-amz-checksum-crc32c`
header (S3 API standard). There is no periodic data scrub: once written, an
object is never re-read to verify integrity. Silent data corruption (bit rot on
disk, bit flip in transit) goes undetected until a user reports it.

**Why it matters.** An object store that cannot detect or report data corruption
cannot be used for regulated data, backups, or archival. The S3 API provides
checksum headers specifically for end-to-end integrity — their absence is both a
compliance gap and a trust gap.

**Direction.**
- **Content checksum on PUT** — accept `x-amz-content-sha256` (S3-compat) and
  `Content-MD5` headers. Compute the checksum while streaming the body; verify
  after the full write; return `400 BadDigest` on mismatch before committing the
  metadata row. ✅ *Small, well-bounded change in `file_crud.go` PUT path.*
- **Content checksum on GET** — return `x-amz-checksum-sha256` and/or
  `x-amz-checksum-crc32c` header computed from the stored object. When the
  checksum was provided at upload time, return it; otherwise compute it lazily
  and cache in metadata.
- **Periodic data scrub job** — a background worker (using `internal/jobs`
  infrastructure) that reads every object, computes its checksum, and compares
  against the stored value. Corrupt objects are quarantined (moved to a
  `quarantine/` prefix + flagged in metadata). Configurable interval + throttle
  (e.g. 10% of objects per day). Runs as a cluster singleton.
- **Read-repair for replication** — when a GET detects partial/corrupt data
  (checksum mismatch or truncated stream), attempt to fetch from the replication
  target before returning to the caller. Log and alert on successful repair.
- **Integrity metrics** — `storage_checksum_mismatches_total{reason}` in
  Prometheus + alert on any mismatch.

---

## 9. Storage tiering & intelligent lifecycle

**Today.** The `BucketConfig` lifecycle is binary: expire after N days with
action `soft_delete` or `hard_delete` (`internal/repository/repository.go:44`).
There is no concept of storage class (S3 Standard / Infrequent Access / Glacier),
no tier transitions, and no restore API. Every object in a bucket lives on the
same backend with the same durability/cost profile.

**Why it matters.** The #1 cost optimization for any object store is tiering.
Without it, users overpay for cold data on hot storage, or can't use the service
for archival use cases. S3 customers expect `STANDARD` → `STANDARD_IA` →
`GLACIER` transitions as a built-in lifecycle rule.

**Direction.**
- **Storage class model** — add a `storage_class` field to `repository.Object`
  and `BucketConfig` lifecycle rules that specify a target class
  (`STANDARD`, `STANDARD_IA`, `GLACIER`, `DEEP_ARCHIVE`). The PUT endpoint
  accepts `x-amz-storage-class` (S3-compat).
- **Tier-aware storage backend** — each `storage.Storage` backend declares its
  supported classes. `local` implements `STANDARD` only; `s3` maps to S3
  storage classes natively. A multi-backend approach: hot data on local NVMe,
  warm on S3 Standard, cold on S3 Glacier via lifecycle rules.
- **Lifecycle transition worker** — a new job type (`JobTransitionObject`)
  that moves an object from one backend to another: copy, verify checksum,
  update `storage_key` + `storage_class`, delete from old backend (with grace
  period). Scheduled by the existing reconcile loop.
- **Restore API** — `GET ?restore` (S3-compat) or `POST /v1/files/{key}/restore`
  initiates a transition from cold → hot tier. Returns `202 Accepted` + job ID.
  The transition worker completes the restore; subsequent GETs return the object
  from the hot tier.
- **Cost-optimized scheduling** — tier transitions run as scheduled jobs
  respecting `RECONCILE_CLUSTER_SINGLETON`. Configurable per-tenant and
  per-bucket. Metrics: `lifecycle_transitions_total` by source/target class.

---

## 10. Metadata HA & disaster recovery

**Today.** Object replication exists (one-way, one-target, via
`internal/replication/`), but the metadata database has **no DR story.** If the
SQLite file is lost, all object metadata is gone — the platform is blind.
Postgres supports streaming replication, but there is no application-level
integration: no automated failover, no read-replica awareness, no point-in-time
recovery API. The snapshot utility (`internal/snapshot/snapshot.go`) is
SQLite-only and requires manual invocation.

**Why it matters.** The metadata database is a single point of failure. For any
deployment with ≥2 replicas (see #3) or an RPO/RTO requirement, losing the DB
means losing the ability to find, list, or manage objects — even though the
object blobs themselves survive. This is the last remaining single-node
assumption in the architecture.

**Direction.**
- **Postgres streaming replication integration** — detect primary loss or
  degradation via a health-check goroutine. On failure: promote the hottest
  standby, reconfigure the application connection pool, begin serving. Design
  for eventual consistency: in-flight writes during the failover window may be
  lost, but reads succeed against the promoted replica.
- **Read-replica awareness** — when configured with multiple Postgres hosts
  (`DB_READ_DSN`), route read-only queries (GET, LIST, SEARCH) to replicas and
  write queries (PUT, DELETE, POST) to the primary. Uses the existing
  `repository.Repository` interface with a `ReadReplica` wrapper.
- **Point-in-time recovery API** — `POST /v1/admin/restore?timestamp=...`
  restores metadata to a prior state (requires Postgres WAL archive). Returns a
  job ID; the operation runs as a background job. Implemented as a sequence of
  SQL statements, not a full DB restore — selective by tenant/bucket if needed.
- **Health endpoint enhancement** — `/readyz` should reflect:
  - DB connection health (+ replication lag for Postgres)
  - Storage backend health (each configured backend)
  - Last successful reconcile timestamp
  Returns `503` when any critical subsystem is unhealthy.
- **SQLite limitation documentation** — explicitly state in
  `docs/deployment.md` that SQLite is single-node only and cannot support
  failover or DR. The Helm chart defaults to SQLite for dev but documents the
  Postgres migration path.

---

## Non-goals (deliberately deferred)

To keep the roadmap honest about scope:
- **New storage backends / protocols** — the abstractions are proven; breadth
  isn't the gap.
- **A heavier built-in Web UI** — the API-first surface (REST + OpenAPI + SDKs)
  is the right primary interface.
- **Fine-grained RBAC/policy language** — valuable eventually, but the scoped
  API-key + ACL model is sufficient for now.
