# CHANGELOG

All functional changes, in reverse-chronological order. Dates are UTC.

---

## 2026-06-13

### Added
- **RRF hybrid sort deterministic tiebreaker** (`internal/ai/search.go`)
  - After accumulation, chunks with identical RRF scores are sorted by `(score DESC, chunkID ASC)`, eliminating nondeterministic ordering from map iteration.

- **BM25 hard-delete synchronous chunk cleanup** (`internal/ai/bm25.go`, `internal/storage/file_service.go`)
  - `FileService.WithChunkCleaner` wires an optional `ChunkSink` into the hard-delete path. When set, BM25 entries for the deleted object's chunks are evicted synchronously, preventing orphan entries from accumulating until restart.

- **Web UI: chat tab + drag-and-drop upload** (`internal/webui/static/index.html`)
  - New chat panel calls `/v1/chat` with SSE streaming and renders assistant responses incrementally. File upload via drag-and-drop uses the existing PUT endpoint. Tenant selector switches context without a page reload.

- **Python SDK admin methods** (`sdk/python/aero_vault.py`)
  - 14 new methods: `add_key`, `list_keys`, `revoke_key`, `issue_jwt`, `list_webhook_failures`, `list_jobs`, `retry_job`, `create_tenant`, `list_tenants`, `delete_tenant`, `set_tenant_status`, `list_audit`, `set_quota`, `set_budget`.

- **JS SDK admin methods** (`sdk/js/aero-vault.js`)
  - Same 14 admin methods as Python SDK, mirroring the full server admin API surface.

- **Go SDK admin methods** (`sdk/go/aerovault/client.go`, `sdk/go/aerovault/types.go`)
  - 14 new methods: `AddKey`, `ListKeys`, `RevokeKey`, `IssueJWT`, `ListWebhookFailures`, `ListJobs`, `RetryJob`, `CreateTenant`, `ListTenants`, `DeleteTenant`, `SetTenantStatus`, `ListAudit`, `SetQuota`, `SetBudget`.
  - New admin types: `APIKey`, `TenantRecord`, `AuditEntry`, `Job`, `WebhookFailure`, `AddKeyRequest`, `IssueJWTRequest`, `IssueJWTResponse`.
  - All three SDKs (Python, JS, Go) now cover the full server admin API surface.

- **Grafana tenant template variable fixed** (`deploy/grafana/aero-vault-ai-ops-dashboard.json`)
  - Template query changed from `label_values(ai_cost_micros_total, tenant)` to `label_values(storage_bytes, tenant)`. Tenants with no AI usage (storage-only) now appear in the dropdown and panels 11/12 (storage_bytes, storage_objects) populate correctly for all tenants.

- **MCP tools: `write_file`, `delete_file`, `chat`** (`internal/mcp/server.go`, `cmd/server/main.go`)
  - MCP agents can now write objects back to the vault, delete objects, and invoke RAG chat directly from MCP — without switching to REST. `write_file` and `delete_file` are always registered; `chat` is only exposed when a Chat service is wired.
  - Stdio MCP path (`aero-vault mcp`) also gains chat support via `buildLLM` + budget config.

- **AI-specific per-tenant rate limiting** (`AI_RATE_LIMIT_RPS` / `AI_RATE_LIMIT_BURST`)
  - A second `RateLimiter` instance, independent from `RATE_LIMIT_RPS`, gates only `/v1/search`, `/v1/chat`, `/v1/chat/stream`, `/v1/agent`, `/v1/lineage`. Storage and admin endpoints are unaffected.

- **Indexer skip metric** (`indexer_skip_total{reason}`)
  - `telemetry.IncIndexerSkip(ctx, reason)` is called on every skip path in `ai/indexer.go`. Prometheus exposes the counter with `reason=unsupported|error|empty` so operators can observe how many objects the indexer skips and why.

- **`AI_AGENT_MAX_STEPS` config** — Agent's tool-call loop depth is now configurable (default 4). Previously hardcoded.

- **`AI_CHUNK_WINDOW` / `AI_CHUNK_OVERLAP` config** — Chunker window and overlap sizes are now configurable (defaults 600/80). Enables tuning per corpus type.

- **ChatStream structured error frames** (`internal/api/rest/search.go`)
  - After SSE headers are sent, errors are now emitted as `event: error\ndata: {"code":"…","message":"…"}\n\n` rather than an unstructured string. Codes: `BudgetExceeded`, `InternalError`.

- **PII credit card Luhn validation** (`internal/ai/pii.go`)
  - The credit card regex now runs a Luhn check on each match. Unix timestamps, object IDs, and other numeric sequences that fail Luhn are no longer flagged as credit cards.

- **Qdrant integration test + `make test-integration-qdrant`** (`internal/integration/qdrant_integration_test.go`, `Makefile`)
  - `TestQdrantIntegration` exercises the full Qdrant adapter lifecycle (EnsureCollection, UpsertObjectChunks, SearchVectors, DeleteObjectChunks) against a live container. Skips gracefully when no Qdrant is reachable.

- **Grafana dashboard extended to 12 panels** (`deploy/grafana/aero-vault-ai-ops-dashboard.json`)
  - New panels 7–12: embed/search latency p50/p95, embed requests+tokens/sec by model, job queue depth, storage bytes/objects per tenant.

- **Prometheus alerts: `aero-vault-ai-latency` group** (`deploy/prometheus/alerts.yml`)
  - `HighEmbedLatencyP95`, `HighSearchLatencyP95`, `JobQueueDepthHigh`.

- **S3 bucket sub-resources** — `?acl` GET/PUT, `GetBucketLocation`, DELETE `?lifecycle`, paginated `?versions`.

- **Tenant CRUD + audit log** — admin endpoints for tenant lifecycle + `audit_log` table (migration 0016).

- **Persisted API keys** — sha256-hashed, scoped, TTL-cached, cross-replica invalidation via Postgres LISTEN/NOTIFY.

- **KMS key versioning + rotation** — `SecretProvider` interface, `keyfile`/HTTP/KMS providers, `STORAGE_SSE_REWRAP_ON_START`.

- **Qdrant adapter** (`internal/ai/qdrant.go`) — implements both `VectorIndex` (read) and `ChunkSink` (write), wired opt-in via `AI_VECTOR_BACKEND=qdrant`.

- **pgvector + pgFTS adapters** — `PgVectorIndex`, `PgFTSIndex`, wired opt-in; verified end-to-end against live Postgres via `make test-integration`.

- **Domain OTel metrics** — 14 instruments covering AI cost/tokens/latency, queue depth, storage gauges, reconcile orphans, idempotency replays, event drops.

- **Incremental BM25** — `ChunkSink`-based O(1) bookkeeping; no more 30-second full-corpus rebuild.

- **Idempotency-Key** — Stripe-style write deduplication, TTL/GC, optional body-hash fingerprint.

- **Cluster singletons** — `leases` table + `cluster.Singleton` helper gates reconcile/lifecycle/retention to one replica.

- **Write-ahead retention** — `RetentionJob` purges soft-deleted rows older than `RECONCILE_RETENTION_DAYS`.

- **WebDAV spill buffer** — uploads/downloads spill to temp file at >8 MiB; MOVE streams through the same buffer.

---

## Earlier

See git log for complete history prior to 2026-06-13.
