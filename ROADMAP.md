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

**Suggested sequencing:** #1 and #2 first — highest ROI, mostly isolated seams,
low risk. #3 + #4 are one coupled "scale-out" epic (shared state needs shared
eventing). #5 is a continuous hardening track that can run in parallel.

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
home with **no change to `Search`**. *Remaining (needs Postgres to verify):* the
pgvector HNSW/IVFFlat adapter itself, and moving BM25 to Postgres FTS.
- **Postgres path:** pgvector with an HNSW/IVFFlat index → approximate top-k in
  the database, not the app. Move lexical search to Postgres FTS (`tsvector`) or
  a persisted/incremental BM25 so it isn't rebuilt from scratch per instance.
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
- **External option:** an interface-level adapter for a dedicated vector store
  (Qdrant/Milvus) for very large deployments.
- Enforce **embedding-model identity at query time** (the `embed_model`/`dim`
  columns already exist) so vectors from different models aren't compared — see
  also edge case in #5.

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
  (`deploy/grafana/aero-vault-ai-ops-dashboard.json`) and Prometheus alert rules
  (`deploy/prometheus/alerts.yml`: 5xx rate, p95 latency, per-tenant spend,
  orphan-blob accumulation, replay storms, dropped events). **Embedding/search
  latency histograms** ✅ *added* (`ai_embed_duration_ms{model}`,
  `ai_search_duration_ms{mode}`). *Remaining (external):* validating live
  dashboards against a running Grafana/collector.
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
  return 429.
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
  cluster-wide events. ✅ *Postgres transport shipped (opt-in; runtime UNVERIFIED
  and **runtime-verified**).* `events.PostgresTransport` (`postgres_transport.go`)
  bridges the bus via `pg_notify`/`LISTEN` with reconnect-backoff. The `Bus`
  gained a `WithTransport` hook (fires on local-origin `Publish`) + a
  broadcast-only `Deliver` (for remote events) — a **loop-free design verified by
  a unit test** (local publish → transport fires once; remote deliver → no
  persist, no re-notify). Wired opt-in (`EVENTS_TRANSPORT=postgres`). **The live
  LISTEN/NOTIFY round-trip is verified end-to-end** against a real Postgres
  (`internal/integration` `TestPgEventTransport`, `make test-integration`):
  publish → `pg_notify` → `WaitForNotification` → deliver.
- **Cluster singletons** via leader election or DB advisory locks, so
  reconcile/lifecycle/index-build run **once per cluster**, not once per pod.
  ✅ *Partially implemented (opt-in via `RECONCILE_CLUSTER_SINGLETON`).* A
  generic `leases` table (migration `0013`) + atomic `repo.AcquireLease`
  (renew-own / take-over-on-expiry) now gate the **destructive** reconcile and
  lifecycle sweeps so only the lease holder runs them; a dead holder's lease
  frees after ~2 intervals. (BM25 rebuild stays per-replica by design — each
  instance needs its own in-memory index; the real fix is a shared index, #1.)
  The lease gating is now a reusable **`cluster.Singleton`** helper ✅ *added*
  (`Enable`/`Guard`, fail-safe on lease error) that the reconcile, lifecycle and
  retention sweeps share — a generic leader-election primitive for any future
  singleton task.
- **Shared index + config** so replicas don't each rebuild/hold their own
  (couples naturally with #1's persisted index and #4's persisted keys).

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
  with a `primary` pointer), with the former env key serving as the legacy slot for
  no-id objects. Background **re-wrap** ✅ *added* (`STORAGE_SSE_REWRAP_ON_START`
  → `storage.RewrapStale`): on boot, objects still on an older key id are
  re-wrapped onto the current key — rewriting only the sidecar envelope (the body
  is untouched), idempotent — so retired key versions can be dropped from the ring.
  An **HTTP secret-store provider** ✅ *added* (`STORAGE_LOCAL_SSE_KEY_URL` +
  `_TOKEN`) fetches the key ring from a Vault-KV-style endpoint (bearer auth)
  instead of a local file, keeping key material off-disk. A **remote-wrap KMS
  client** ✅ *added* (`DataKeyWrapper` + `STORAGE_LOCAL_SSE_KMS_URL`): the
  per-object data key is wrapped/unwrapped over HTTP (`/wrap`, `/unwrap`) so the
  master key **never reaches aero-vault**; the envelope records `wrap:"kms"` + the
  KMS key id (backward-compatible — local envelopes are unchanged). Speaks a small
  generic shape compatible with a thin proxy in front of AWS/GCP KMS or Vault
  Transit. *Remaining (external):* validating against a real KMS deployment.
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
  soft-deleted blobs are protected. *Next:* surface the counts as metrics and
  make the destructive sweep a cluster singleton (see #2, #3).
- **Write idempotency.** ✅ *Implemented.* `/v1` object mutations now honor an
  `Idempotency-Key` header (opt-in, Stripe-style): a retried `PUT`/`POST`/`DELETE`
  replays the original response (`Idempotency-Replayed: true`) instead of
  creating a duplicate version. Backed by a persisted `idempotency_keys` table
  (migration `0011`), keyed by `(tenant, key)`, with a request fingerprint,
  409 on key-reuse/in-flight, 5xx-releases-claim, and fail-closed on store
  errors. **TTL/GC** ✅ *added* (`IDEMPOTENCY_TTL_HOURS` →
  `repo.DeleteIdempotencyKeysBefore`, swept by the `RetentionJob`) so the dedupe
  table stays bounded. *Next (v2):* optionally hash the request body to also
  catch same-key/different-bytes.
- **Large objects buffered fully in memory.** ✅ *Fixed.* The WebDAV read and
  write paths now use a bounded `spillBuffer` (`internal/api/webdav/spill.go`):
  ≤8 MiB stays in RAM, larger payloads spill to a temp file (removed on Close),
  so big uploads/downloads no longer OOM. Range/Seek preserved; covered by a
  9 MiB round-trip + Range test. (`Rename`'s copy path is the remaining buffer.)
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
  the new `repo.ListSoftDeletedBefore`; it honors object-lock and runs as a
  cluster singleton. The **dropped-event metric** for #3's 64-buffer is ✅ done
  (`events_dropped_total` via `telemetry.IncEventDropped` / `Bus.Dropped()`).

These are individually small but collectively define whether the platform is
**trustworthy with data** when it's busy — which is when trust matters most.

---

## Non-goals (deliberately deferred)

To keep the roadmap honest about scope:
- **New storage backends / protocols** — the abstractions are proven; breadth
  isn't the gap.
- **A heavier built-in Web UI** — the API-first surface (REST + OpenAPI + SDKs)
  is the right primary interface.
- **Fine-grained RBAC/policy language** — valuable eventually, but the scoped
  API-key + ACL model is sufficient until #4's persisted identity layer lands to
  build on.
