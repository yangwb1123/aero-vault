# Configuration

aero-vault is configured entirely through environment variables. On startup it
also loads a `.env` file from the working directory if present (via
`godotenv`); real environment variables take precedence. See `.env.example` for a
copy-paste starting point.

Notes that apply throughout:

- **Empty-string handling:** an environment variable that is set but empty is
  treated as *unset* (the default applies).
- **Booleans** accept Go `strconv.ParseBool` values: `true`/`false`, `1`/`0`,
  `t`/`f`, etc. An unparseable value falls back to the default.
- **Defaults** below are the in-code defaults from `internal/config/config.go`.
- Variables marked *(config.go only)* are honored by the loader but are **not**
  listed in `.env.example`.

Validation (fails fast on startup): the storage backend must be one of
`local|s3|oss|cos` and its required fields must be present; `DB_DRIVER` must be
`sqlite|postgres` and `DB_DSN` non-empty; if `AI_INDEX_ENABLED=true` and
`AI_EMBED_PROVIDER=http`, then `AI_EMBED_ENDPOINT` is required.

---

## Application

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ADDR` | `:8080` | HTTP listen address. |
| `APP_LOG_LEVEL` | `info` | Log level: `debug` \| `info` \| `warn`/`warning` \| `error`. Invalid values fail startup. |

## Storage backend selection

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_BACKEND` | `local` | Active backend: `local` \| `s3` \| `oss` \| `cos`. Lower-cased. |

### Local filesystem (`STORAGE_BACKEND=local`)

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_LOCAL_ROOT` | `./var/objects` | Root directory for stored objects. **Required** for the local backend. |
| `STORAGE_LOCAL_PUBLIC_URL` | _(empty)_ | Public base URL used to build presigned URLs (e.g. `http://localhost:8080/files`). |
| `STORAGE_LOCAL_SIGN_KEY` | _(empty)_ | HMAC key for presigning local URLs; empty disables local presigning. |
| `STORAGE_LOCAL_SSE_KEY` | _(empty)_ | Single master passphrase for envelope (AES-256-GCM) server-side encryption; empty disables SSE. When a keyfile is also set, this becomes the legacy key for pre-rotation (no-id) objects. |
| `STORAGE_LOCAL_SSE_KEYFILE` | _(empty)_ | Path to a JSON key ring (`{"primary":"id","keys":{"id":"passphrase",…}}`) enabling versioned keys + zero-downtime rotation; takes precedence over `STORAGE_LOCAL_SSE_KEY`. Each object records its key id, so rotating = add a key + move `primary` (no re-encryption); old objects keep decrypting under their original key. Key ids must match `[A-Za-z0-9._-]+`. |
| `STORAGE_LOCAL_SSE_KEY_URL` | _(empty)_ | HTTP secret store (e.g. Vault KV) serving the same key-ring JSON; fetched once at startup. Takes precedence over `STORAGE_LOCAL_SSE_KEYFILE`. Keeps key material out of local files. |
| `STORAGE_LOCAL_SSE_KEY_TOKEN` | _(empty)_ | Bearer token sent as `Authorization: Bearer …` when fetching `STORAGE_LOCAL_SSE_KEY_URL`. |
| `STORAGE_LOCAL_SSE_KMS_URL` | _(empty)_ | HTTP KMS endpoint that wraps/unwraps data keys remotely (`POST /wrap`, `POST /unwrap`) — the wrapping key never reaches aero-vault. Takes precedence over all key-ring sources. Compatible with a thin proxy in front of AWS/GCP KMS or Vault Transit. |
| `STORAGE_LOCAL_SSE_KMS_KEY_ID` | _(empty)_ | KMS key id used to wrap new data keys (recorded per-object so unwrap targets the right key). |
| `STORAGE_LOCAL_SSE_KMS_TOKEN` | _(empty)_ | Bearer token for `STORAGE_LOCAL_SSE_KMS_URL`. |
| `STORAGE_SSE_REWRAP_ON_START` | `false` | On boot, re-wrap every object still encrypted under an older key id onto the current `primary` key — rewrites only the sidecar envelope (object bodies are untouched), idempotent — so retired keys can be removed from the ring. Opt-in; run it on one instance after a rotation. |

> **SSE key-ring operations.** Treat a key id as **immutable**: to rotate, add a
> *new* id and point `primary` at it — never change an existing id's passphrase in
> place (objects written under it would no longer decrypt). Keep retired ids in the
> ring as long as any object still references them. When migrating from
> `STORAGE_LOCAL_SSE_KEY` to a keyfile, **keep the old `STORAGE_LOCAL_SSE_KEY` set**
> — it decrypts pre-rotation (no-id) objects; dropping it makes those objects
> unreadable until it is restored.

### AWS S3 / S3-compatible (`STORAGE_BACKEND=s3`)

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_S3_ENDPOINT` | _(empty = AWS)_ | Custom S3 endpoint; set for MinIO / OSS-S3 / COS-S3. Leave empty for AWS. |
| `STORAGE_S3_REGION` | `us-east-1` | S3 region. |
| `STORAGE_S3_BUCKET` | _(empty)_ | Backing bucket. **Required** for the s3 backend. |
| `STORAGE_S3_ACCESS_KEY` | _(empty)_ | Access key. |
| `STORAGE_S3_SECRET_KEY` | _(empty)_ | Secret key. |
| `STORAGE_S3_FORCE_PATH_STYLE` | `true` | Use path-style addressing (required by MinIO and most S3-compatible stores). |

### Alibaba Cloud OSS (`STORAGE_BACKEND=oss`)

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_OSS_ENDPOINT` | _(empty)_ | OSS endpoint. **Required** for the oss backend. |
| `STORAGE_OSS_BUCKET` | _(empty)_ | OSS bucket. **Required** for the oss backend. |
| `STORAGE_OSS_ACCESS_KEY` | _(empty)_ | Access key ID. |
| `STORAGE_OSS_SECRET_KEY` | _(empty)_ | Access key secret. |

### Tencent Cloud COS (`STORAGE_BACKEND=cos`)

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_COS_BUCKET_URL` | _(empty)_ | Bucket URL including bucket + region + appid. **Required** for the cos backend. |
| `STORAGE_COS_SECRET_ID` | _(empty)_ | Secret ID. |
| `STORAGE_COS_SECRET_KEY` | _(empty)_ | Secret key. |

## Database (metadata)

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_DRIVER` | `sqlite` | `sqlite` \| `postgres`. Lower-cased. |
| `DB_DSN` | `file:./var/aero.db?_pragma=foreign_keys(1)` | Connection string. **Required** (non-empty). For Postgres, e.g. `postgres://aero:aero@localhost:5432/aero_vault?sslmode=disable`. |

## S3-compatible gateway

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_COMPAT_PREFIX` | `/s3` | Mount prefix for the S3-compatible router. Set empty to disable the S3 gateway. |

## Background jobs

| Variable | Default | Description |
|----------|---------|-------------|
| `JOBS_WORKERS` | `4` | Background worker-pool size. `>0` routes indexing/antivirus/replication through the durable jobs table with retry; `0` makes the indexer process events inline (and disables antivirus/replication, which require the pool). |

## AI / RAG

| Variable | Default | Description |
|----------|---------|-------------|
| `AI_INDEX_ENABLED` | `false` | Enable text extraction + chunking + embedding of uploaded objects. Master switch for the AI pipeline. |
| `AI_HYBRID_SEARCH` | `false` | Fuse vector + BM25 retrieval with reciprocal-rank fusion. |
| `AI_EMBED_PROVIDER` | `hash` | Embedder: `hash` (built-in, dependency-free) or `http` (OpenAI-compatible). Lower-cased. |
| `AI_EMBED_ENDPOINT` | _(empty)_ | Embedding service base URL, e.g. `http://localhost:11434` (Ollama). **Required when** `AI_INDEX_ENABLED=true` **and** `AI_EMBED_PROVIDER=http`. |
| `AI_EMBED_MODEL` | `text-embedding-3-small` | Embedding model name. |
| `AI_EMBED_API_KEY` | _(empty)_ | API key for the embedding endpoint. |
| `AI_EMBED_DIM` | `256` | Embedding dimensionality (e.g. `768` for `nomic-embed-text`). |
| `AI_CHAT_PROVIDER` | _(empty = off)_ | Chat LLM: `http` (OpenAI-compatible), `mock` (echo, for testing), or empty. Lower-cased. |
| `AI_CHAT_ENDPOINT` | _(empty)_ | Chat service base URL, e.g. `http://localhost:11434`. |
| `AI_CHAT_MODEL` | `gpt-4o-mini` | Chat model name. |
| `AI_CHAT_API_KEY` | _(empty)_ | API key for the chat endpoint. |
| `AI_TENANT_DAILY_BUDGET_USD` | `0` | Default per-tenant daily AI spend cap (USD); 0 = unlimited. Chat returns `402` once a tenant's recorded same-day spend reaches the cap. Requires `AI_COST_*` pricing to estimate spend. |
| `AI_PER_TENANT_BUDGETS` | `false` | Let each tenant override the default cap via `PUT /v1/admin/tenants/{tenant}/budget` (`{"daily_budget_usd": N}`). A tenant's override (when > 0) wins over the default — and enforces even when no default is set; `0` clears it. |
| `AI_RERANK_PROVIDER` | _(empty = off)_ | Reranker: `http` (Cohere/Voyage/bge-reranker wire shape) or `heuristic` (dependency-free fallback). Lower-cased. |
| `AI_RERANK_ENDPOINT` | _(empty)_ | Reranker service URL. |
| `AI_RERANK_MODEL` | `bge-reranker-v2` | Reranker model name. |
| `AI_RERANK_API_KEY` | _(empty)_ | API key for the reranker. |
| `AI_EXTRACTOR_ENDPOINT` | _(empty)_ | Optional remote text extractor (Tika/Unstructured `/extract`). Empty uses the built-in extractor. |
| `AI_EXTRACTOR_API_KEY` | _(empty)_ | API key for the remote extractor. |
| `AI_PII_SCAN` | `false` | Scan indexed text for PII and tag the object. |
| `AI_PII_REDACT` | `false` | Redact detected PII before embedding (requires `AI_PII_SCAN`). |

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTH_KEYS` | _(empty = open)_ | Comma-separated API keys as `token:tenant:scope+scope` (e.g. `prod-rw:acme:read+write,ops:*:admin`). Tenant `*` = operator (any tenant). Empty disables API-key auth (MVP/open mode). |
| `AUTH_JWT_SECRET` | _(empty)_ | Secret enabling HS256 JWT verification and issuance (`POST /v1/admin/jwt`). |
| `AUTH_ANONYMOUS_PUBLIC_READ` | `false` | Allow unauthenticated `GET`/`HEAD` of public-read objects (the handler still enforces the object ACL). |
| `S3_SIGV4_CREDENTIALS` | _(empty)_ | AWS SigV4 credentials for the S3 endpoint: `accessKey:secretKey:tenant[:scope+scope]`, comma-separated (e.g. `AKIA...:secret...:acme:read+write`). |

## CORS *(config.go only)*

| Variable | Default | Description |
|----------|---------|-------------|
| `CORS_ALLOWED_ORIGINS` | _(empty)_ | Comma-separated allowed origins. |
| `CORS_ALLOWED_METHODS` | _(empty)_ | Comma-separated allowed methods. |
| `CORS_ALLOWED_HEADERS` | _(empty)_ | Comma-separated allowed request headers. |

> The response always exposes `ETag`, `X-Request-ID`, and `X-Version-Id`.

## Rate limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `RATE_LIMIT_RPS` | `0` | Per-tenant token-bucket refill rate (requests/sec). `0` disables rate limiting. |
| `RATE_LIMIT_BURST` | `0` | Token-bucket burst capacity. |

## Antivirus

Async scanning runs via the job pool, so it requires `JOBS_WORKERS>0`.

| Variable | Default | Description |
|----------|---------|-------------|
| `AV_ENABLED` | `false` | Enable malware scanning of uploaded objects. |
| `AV_PROVIDER` | `signature` | `signature` (built-in EICAR-style scanner) or `http` (external engine). Lower-cased. |
| `AV_ENDPOINT` | _(empty)_ | External scanner URL for `AV_PROVIDER=http`. |
| `AV_API_KEY` | _(empty)_ | API key for the external scanner. |
| `AV_QUARANTINE` | `false` | Soft-delete infected objects. |

## Cross-region replication

Async replication to a secondary backend; requires `JOBS_WORKERS>0`.

| Variable | Default | Description |
|----------|---------|-------------|
| `REPLICATION_ENABLED` | `false` | Enable replication to the secondary backend. |
| `REPLICATION_BACKEND` | `local` | Replica backend: `local` \| `s3` \| `oss` \| `cos`. Lower-cased. |
| `REPLICATION_LOCAL_ROOT` | _(empty)_ | Replica root dir (local replica backend). |
| `REPLICATION_LOCAL_SIGN_KEY` | _(empty)_ | HMAC sign key for the local replica. *(config.go only)* |
| `REPLICATION_LOCAL_SSE_KEY` | _(empty)_ | SSE master passphrase for the local replica. |
| `REPLICATION_LOCAL_SSE_KEYFILE` | _(empty)_ | Versioned SSE key ring for the local replica (mirror of `STORAGE_LOCAL_SSE_KEYFILE`). |
| `REPLICATION_S3_ENDPOINT` | _(empty)_ | Replica S3 endpoint. |
| `REPLICATION_S3_REGION` | `us-east-1` | Replica S3 region. |
| `REPLICATION_S3_BUCKET` | _(empty)_ | Replica S3 bucket. |
| `REPLICATION_S3_ACCESS_KEY` | _(empty)_ | Replica S3 access key. |
| `REPLICATION_S3_SECRET_KEY` | _(empty)_ | Replica S3 secret key. |
| `REPLICATION_S3_FORCE_PATH_STYLE` | `true` | Path-style addressing for the replica S3 endpoint. *(config.go only)* |

> The replica reuses the same `StorageConfig` shape; OSS/COS replica targets are
> constructed from the corresponding `STORAGE_OSS_*` / `STORAGE_COS_*` values
> when `REPLICATION_BACKEND` is `oss`/`cos`.

## Events / webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `EVENTS_WEBHOOK_URL` | _(empty)_ | POST object-lifecycle events to this URL. Empty disables webhooks. |
| `EVENTS_WEBHOOK_SECRET` | _(empty)_ | HMAC-SHA256 signing key; signature is sent in `X-Aero-Signature`. |

## Background reconcile / lifecycle

| Variable | Default | Description |
|----------|---------|-------------|
| `RECONCILE_INTERVAL_MINUTES` | `0` | `>0` enables periodic orphan reconciliation + lifecycle expiry on this interval. `0` disables. |
| `RECONCILE_DELETE_ORPHAN_BLOBS` | `false` | Delete blobs that have no remaining DB reference during reconciliation. |
| `RECONCILE_ORPHAN_GRACE_MINUTES` | `60` | Minimum age (minutes) an orphan blob must reach before it is eligible for deletion. |
| `RECONCILE_TENANTS` | `default` | Comma-separated list of tenants to scan. Empty/unset defaults to `default`. |

## Telemetry

| Variable | Default | Description |
|----------|---------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTLP/HTTP endpoint for traces + metrics, e.g. `http://localhost:4318`. Empty installs no-op providers (still propagates trace headers). |
| `PROMETHEUS_ENABLED` | `false` | Expose Prometheus metrics at `/metrics`. |

## Other gateways

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBDAV_PREFIX` | _(empty = disabled)_ | Mount prefix for WebDAV, e.g. `/webdav`. Empty disables WebDAV. |
| `WEBUI_ENABLED` | `true` | Serve the static web UI at `/ui`. |

---

## Demo configuration reference

The values used by `docker-compose.demo.yml` (a realistic production-shaped
config) are a good worked example:

```env
APP_ADDR=:8080
DB_DRIVER=postgres
DB_DSN=postgres://aero:aero@postgres:5432/aero_vault?sslmode=disable
STORAGE_BACKEND=s3
STORAGE_S3_ENDPOINT=http://minio:9000
STORAGE_S3_BUCKET=aero-vault
STORAGE_S3_FORCE_PATH_STYLE=true
S3_COMPAT_PREFIX=/s3
AI_INDEX_ENABLED=true
AI_HYBRID_SEARCH=true
AI_EMBED_PROVIDER=http
AI_EMBED_ENDPOINT=http://ollama:11434
AI_EMBED_MODEL=nomic-embed-text
AI_EMBED_DIM=768
AI_CHAT_PROVIDER=http
AI_CHAT_ENDPOINT=http://ollama:11434
AI_CHAT_MODEL=llama3.2:1b
JOBS_WORKERS=4
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
PROMETHEUS_ENABLED=true
```
