# Architecture

aero-vault is a single Go binary (`cmd/server`) that wires a set of layered,
loosely-coupled subsystems around one shared `FileService`. Every protocol
gateway (REST, S3, WebDAV, MCP) is a thin adapter over that service, so an object
written through one protocol is immediately visible through all others.

## Layered overview

```
┌───────────────────────────────────────────────────────────────────────────┐
│  Protocol / API layer                                                       │
│  internal/api/rest   internal/api/s3compat   internal/api/webdav            │
│  internal/mcp        internal/webui (static UI)                             │
├───────────────────────────────────────────────────────────────────────────┤
│  Middleware chain (internal/middleware, internal/auth, internal/telemetry)  │
│  RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog│
├───────────────────────────────────────────────────────────────────────────┤
│  Service layer                                                              │
│  internal/service.FileService — the single object-CRUD entry point          │
│  (authorization, quota, versioning, locks, tags, range, presign, events)     │
├──────────────────────────────┬────────────────────────────────────────────┤
│  Storage abstraction         │  Repository / metadata                      │
│  internal/storage.Storage    │  internal/repository.Repository             │
│  local · s3 · oss · cos      │  sqlite · postgres (+ migrations)           │
│  (+ envelope SSE, presign)   │  objects, versions, tags, ACLs, quotas,     │
│                              │  events, jobs, chunks/embeddings, usage     │
├───────────────────────────────────────────────────────────────────────────┤
│  Eventing & async                                                           │
│  internal/events (bus + webhooks)   internal/jobs (durable worker pool)     │
├───────────────────────────────────────────────────────────────────────────┤
│  AI / RAG pipeline (internal/ai)                                            │
│  extractor → chunker → embedder → index;  search (vector/BM25/hybrid) →     │
│  rerank → chat → agent;  PII detection                                      │
├───────────────────────────────────────────────────────────────────────────┤
│  Cross-cutting workers                                                      │
│  internal/antivirus   internal/replication   internal/reconcile (lifecycle) │
├───────────────────────────────────────────────────────────────────────────┤
│  Telemetry (internal/telemetry) — OpenTelemetry traces + metrics, Prometheus│
└───────────────────────────────────────────────────────────────────────────┘
```

### Storage abstraction (`internal/storage`)

`storage.Storage` is the contract every backend implements. It is deliberately
small and provider-agnostic:

```go
type Storage interface {
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (string, error)
    PresignPut(ctx, key, expiry) (string, error)
    InitMultipart / UploadPart / CompleteMultipart / AbortMultipart ...
    Backend() string
}
```

Implementations:

| Backend | File | Notes |
|---------|------|-------|
| `local` | `local.go` | On-disk objects with sidecar `*.meta.json` metadata. Optional envelope (AES-GCM) server-side encryption when `STORAGE_LOCAL_SSE_KEY` is set. The storage interface retains raw HMAC presigning, while the REST adapter uses Aero capability URLs so GET/PUT remain inside FileService policy enforcement. |
| `s3` | `s3.go` | AWS SDK v2; works against AWS S3 or any S3-compatible endpoint (MinIO, OSS-S3, COS-S3). Path-style toggle. |
| `oss` | `oss.go` | Native Alibaba Cloud OSS SDK. |
| `cos` | `cos.go` | Native Tencent Cloud COS SDK. |

A `factory.go` constructs the right backend from config (`storage.NewFromConfig`).
`main.go` uses the same factory for the **replication** replica target, so any
backend can replicate to any other.

### Repository / metadata (`internal/repository`)

The `Repository` interface persists all metadata: objects, historical versions,
tags, bucket configs (versioning / object-lock / lifecycle), ACLs, tenant
quotas, lifecycle events, background jobs, webhook-failure records, and the
RAG-related tables (chunks/embeddings, AI-consumption usage rows).

Two implementations share a common SQL core (`sql.go`):

- **`sqlite`** (`sqlite.go`) — `modernc.org/sqlite`, pure-Go, no CGO. The default.
- **`postgres`** (`postgres.go`) — `jackc/pgx/v5`.

Schema is managed by versioned migrations under
`internal/repository/migrations/{sqlite,postgres}` (`0001_init` … `0010_acl`),
applied automatically on startup via `repo.Migrate(ctx)`.

### Service layer (`internal/service`)

`FileService` is the single object-CRUD entry point shared by the REST and
S3-compat handlers (and, indirectly, WebDAV and MCP). It owns the business rules
that must hold regardless of protocol:

- **Quota enforcement** — checks tenant byte/object limits *before* streaming
  bytes to storage.
- **Versioning** — when a bucket has versioning enabled, every `Put` creates a
  new historical row; otherwise the row is upserted in place.
- **Object-lock / WORM** — per-object retention; locked objects reject
  delete/overwrite until the retention window expires.
- **Tags, ACLs, lifecycle** — metadata operations.
- **Range & conditional requests** — `ParseByteRange` and conditional helpers
  power `206 Partial Content` and `304 Not Modified`.
- **Presigned URLs**, **multipart upload**.
- **Event emission** — every mutating operation publishes a lifecycle event to
  the bus via the `EventSink`.

The service depends only on `storage.Storage` and `repository.Repository`,
keeping it backend-agnostic.

### Enterprise authorization boundary

`internal/access` is an independent policy-decision domain. Authentication
adapters normalize API keys and local JWTs directly. Snaplink's
`interfaces/ssoclient/remote.BrowserFlow` and `TokenClient` own bounded
one-time state, secure cookies, authorization URL construction, PKCE, and OAuth
token exchange;
access tokens pass through `interfaces/ssoclient/rs` and then a thin identity
mapper. FileService sends `{action, tenant, bucket, key, owner}` to
the authorizer before every object operation. REST, S3, WebDAV, MCP, multipart,
copy, version reads, and AI retrieval therefore share one enforcement point.

Snaplink is reused for identity, login, token cryptography/JWKS rotation, tenant
membership, and coarse application authorization. It intentionally does not own Aero's resource ACL:
Snaplink's current permission provider is application-scoped and its access
tokens omit a tenant claim. Aero maps the issuer-pinned `client_id` to a tenant
with `AUTH_JWKS_CLIENT_TENANTS`, then resolves local department membership and
file/folder ACLs. This prevents coupling storage policy to Snaplink data types
or availability.

The access repository is a narrow `access.Store` implemented by the SQL
repository, rather than widening the core object repository interface. It owns
department hierarchy/membership, allow/deny ACL entries, hashed share tokens,
and stable public-asset slugs. Shares and public assets resolve to exact,
short-lived request capabilities; FileService still makes the final decision.

### Eventing & async

- **Event bus** (`internal/events/bus.go`) — an in-process pub/sub. The
  `FileService` publishes object-lifecycle events; subscribers include the SSE
  endpoint, the indexer, the antivirus worker, the replication worker, and the
  webhook sender. Events are also persisted to the repository.
- **Webhooks** (`internal/events/webhook.go`) — POSTs events to
  `EVENTS_WEBHOOK_URL`, optionally HMAC-SHA256 signed (`X-Aero-Signature`) with
  `EVENTS_WEBHOOK_SECRET`, with a durable retry loop backed by a
  webhook-failures table.
- **Job queue** (`internal/jobs`) — a durable worker pool (`JOBS_WORKERS`) backed
  by a jobs table with retry. A `Registry` maps job types to handlers; a `Queue`
  enqueues work; a `Pool` runs the workers. Used by the indexer
  (`JobIndexObject` / `JobDeleteChunks`), antivirus (`JobScan`), and replication
  (`JobReplicate`). When `JOBS_WORKERS=0`, the indexer falls back to processing
  events inline (no durability/retry).

### AI / RAG pipeline (`internal/ai`)

The pipeline subscribes to the event bus and turns uploaded objects into
searchable, citable knowledge:

1. **Extract** — `extractor.go` pulls plain text from an object; an optional
   remote extractor (Tika/Unstructured, via `AI_EXTRACTOR_ENDPOINT`) wraps it.
2. **Chunk** — `chunker.go` splits text into overlapping chunks.
3. **Embed** — `embedder.go` produces vectors. Two providers: a built-in,
   dependency-free `hash` embedder, or an `http` (OpenAI-compatible) embedder
   (e.g. Ollama). Optional **PII detection/redaction** (`pii.go`) runs before
   embedding.
4. **Index** — `indexer.go` writes chunks + embeddings to the repository. It runs
   as an event→job bridge when the job pool is enabled.
5. **Search** — `search.go` performs vector search, BM25 (`bm25.go`), or
   **hybrid** retrieval fused with reciprocal-rank fusion; an optional
   **reranker** (`rerank.go`) reorders results.
6. **Chat & Agent** — `chat.go` answers questions with inline citations
   (RAG); `agent.go` runs a tool-calling loop over the file tools.
7. **Lineage** — every AI read/search records a usage row, exposed at
   `GET /v1/lineage/objects/{id}` for AI-consumption auditing.

All AI features are off by default and gated behind `AI_*` configuration.

### Telemetry (`internal/telemetry`)

- `otel.go` installs OpenTelemetry tracer + meter providers. When
  `OTEL_EXPORTER_OTLP_ENDPOINT` is set, traces and metrics are exported over
  OTLP/HTTP; when unset, no-op providers keep the app running while still
  propagating trace headers.
- `http.go` is the per-request middleware that opens a span and records two
  instruments: an `http.server.requests` counter and an
  `http.server.duration_ms` histogram, each labeled by `method` and `status`
  class.
- `prometheus.go` installs a Prometheus exporter and returns the `/metrics`
  handler when `PROMETHEUS_ENABLED=true`. Through the exporter these surface as
  `http_server_requests_total` and `http_server_duration_ms_{bucket,sum,count}`,
  alongside standard `go_*` and `process_*` runtime metrics.

## Request flow

A request entering the server passes through the middleware chain (defined in
`cmd/server/main.go`), outermost first:

```
RequestID → BucketCORS → CORS → SecureHeaders → MaxBodySize → Auth → Tenant
          → RateLimit → OTel → Recoverer → Concurrency → AccessLog → handler
```

- **RequestID** assigns/echoes `X-Request-ID` (also exposed via CORS).
- **CORS** applies the configured allow-lists.
- **Auth** verifies an API key (`Authorization: Bearer`/`ApiKey`, or `X-Api-Key`),
  a JWT, or an AWS SigV4 signature; enforces per-method scopes; and, for
  tenant-scoped keys, **pins `X-Aero-Tenant`** to the key's tenant. Health,
  `/metrics`, `/openapi.json`, `/docs`, and `/ui` bypass auth.
  Share and public-asset reads do not bypass Auth: it explicitly admits only
  anonymous `GET`/`HEAD`, after which the handler resolves an exact capability
  and FileService enforces it.
- **Tenant** reads `X-Aero-Tenant` into the request context (defaults to
  `default`) and rejects known tenants whose persisted status is `disabled`.
  Auth runs *before* Tenant so a key can pin the tenant. FileService repeats the
  status guard at its protocol-independent boundary, which also covers public
  share/asset capabilities; trusted system workers retain cleanup access.
- **RateLimit** applies a per-tenant token bucket when `RATE_LIMIT_RPS > 0`.
- **OTel** records the span + metrics.
- **Recoverer** turns panics into `500`s; **AccessLog** writes one structured log
  line per request.

The top-level chi router then dispatches:

- `/v1/*` → REST sub-router (`rest.NewRouter`).
- `/s3/*` (or `S3_COMPAT_PREFIX`) → S3-compat router.
- `/mcp` → MCP JSON-RPC HTTP handler.
- `/ui` → static web UI (when enabled).
- `/webdav/*` (or `WEBDAV_PREFIX`) → WebDAV handler, dispatched *outside* chi so
  that `PROPFIND`/`MKCOL` verbs work.
- `/healthz`, `/readyz`, `/metrics`, `/openapi.json`, `/docs` → top-level
  handlers.

Inside a handler, the typical write path is:

```
handler → FileService.Put → quota check → storage.Put (bytes) →
  repository upsert/version (metadata) → events.Publish →
    bus → {indexer→job queue, antivirus, replication, webhook, SSE}
```

## Multi-tenancy model

Tenancy is a first-class, header-driven concept:

- The **tenant** is carried in the `X-Aero-Tenant` request header. When absent it
  defaults to `default` (so single-tenant deployments need no extra config).
- When API-key (or SigV4) auth is enabled, a **tenant-scoped key pins the
  tenant**: the auth middleware sets `X-Aero-Tenant` to the key's tenant and
  rejects any mismatching header. A key with tenant `*` is an **operator** key
  that may act for any tenant.
- All metadata rows are scoped by tenant, and **every storage key is prefixed by
  tenant** (see below), so two tenants can use identical bucket/key names without
  collision.
- Per-tenant **quotas** (byte + object limits) are enforced in the service layer.
- A persisted tenant with status **`disabled`** receives `403 TenantDisabled`
  on data-plane access. Unknown tenant IDs remain valid for compatibility with
  deployments that use implicit tenants without provisioning tenant records.
- The MCP server resolves the request-scoped tenant from context, falling back to
  its configured default in stdio mode.

## Storage-key scheme

User-facing identifiers are `(tenant, bucket, key)`. The service layer maps these
to a single physical storage key:

```go
// internal/service/file.go
func storageKey(tenant, bucket, key string) string {
    return path.Join(tenant, bucket, key)
}
```

So an object the user knows as bucket `docs`, key `reports/q1.pdf` for tenant
`acme` is physically stored under:

```
acme/docs/reports/q1.pdf
```

For the **local** backend this is a path beneath `STORAGE_LOCAL_ROOT` (with a
sibling `…/q1.pdf.meta.json` sidecar); for **s3/oss/cos** it is the object key
inside the single backing bucket. This single-prefix layout is what lets one
backing bucket serve unlimited tenants and logical buckets.

Keys are validated to reject traversal: empty keys, keys beginning with `/`, and
keys containing `..` are rejected, and the local backend additionally guards
against symlink/`..` escapes outside the root.

MCP exposes objects as resource URIs of the form
`aero-vault://{tenant}/{bucket}/{key}`, mirroring the same three-part identity.
