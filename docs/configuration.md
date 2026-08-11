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
| `APP_WRITE_TIMEOUT` | `60` | HTTP write timeout in seconds. SSE streams exempt themselves via `SetWriteDeadline`. Set to `0` to disable. |
| `APP_IDLE_TIMEOUT` | `120` | HTTP idle (keep-alive) timeout in seconds. Set to `0` to disable. |
| `APP_MAX_BODY_SIZE` | `0` | Maximum request body size in bytes. Requests above the limit receive `413`; `0` disables the limit. Unknown-length (chunked) bodies over the limit are rejected with `413`, never truncated. |
| `APP_TLS_ENABLED` | `false` | Enable TLS/HTTPS. Requires `APP_TLS_CERT_FILE` and `APP_TLS_KEY_FILE`. |
| `APP_TLS_CERT_FILE` | _(empty)_ | Path to TLS certificate file (PEM). Required when `APP_TLS_ENABLED=true`. |
| `APP_TLS_KEY_FILE` | _(empty)_ | Path to TLS private key file (PEM). Required when `APP_TLS_ENABLED=true`. |
| `REQUEST_TIMEOUT_SECONDS` | `120` | Per-request context deadline applied to all AI endpoints (`/search`, `/chat`, `/chat/stream`, `/agent`, `/lineage`) **and the `/thumbnail` route** (bounding the decode-slot wait, see "Thumbnail decode budget"). Set to `0` to disable. |
| `MAX_INFLIGHT_REQUESTS` | `0` | Global weighted in-flight request limit (reads cost 1, writes cost 2); `0` disables. |
| `PER_TENANT_CONCURRENCY_MAX` | `0` | Optional per-tenant in-flight limit used alongside the global cap; `0` disables per-tenant partitioning. |
| `EVENTS_SUB_BUFFER` | `64` | Per-subscriber in-process event channel buffer depth. Increase if subscribers fall behind under high event throughput. Set to `0` to use the default. |
| `CORS_EXPOSE_HEADERS` | _(empty)_ | Comma-separated extra response headers browsers may read. Default set: `ETag`, `Idempotency-Replayed`, `Retry-After`, `X-Request-ID`, `X-Version-Id`. |

## Storage backend selection

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_BACKEND` | `local` | Active backend: `local` \| `s3` \| `oss` \| `cos`. Lower-cased. |
| `STORAGE_DEFAULT_CLASS` | _(empty = `STANDARD`)_ | Default storage class assigned when a request does not provide one. |
| `STORAGE_CONNECT_TIMEOUT` | `5` | Cloud-backend connect timeout in seconds. |
| `STORAGE_READ_TIMEOUT` | `30` | Cloud-backend response/read timeout in seconds. |
| `STORAGE_WRITE_TIMEOUT` | `30` | Cloud-backend write/overall request timeout in seconds. |
| `STORAGE_VERIFY_ON_READ` | `false` | Verify stored object integrity while it is read. |
| `STORAGE_VERIFY_MAX_SIZE` | `10485760` | Maximum object size fully verified on read before large-object sampling rules apply. |
| `STORAGE_VERIFY_SAMPLE` | `true` | Permit sampled verification for objects larger than `STORAGE_VERIFY_MAX_SIZE`. |
| `STORAGE_CB_ENABLED` | `false` | Enable the storage circuit-breaker wrapper. |
| `STORAGE_CB_FAILURE_THRESHOLD` | `5` | Consecutive backend failures before the circuit opens. |
| `STORAGE_CB_RECOVERY_TIMEOUT` | `30` | Seconds before an open circuit admits a half-open probe. |
| `STORAGE_CB_HALF_OPEN_MAX` | `1` | Maximum concurrent probes admitted while half-open. |

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
| `S3_COMPAT_PREFIX` | _(empty / disabled)_ | Mount prefix for the S3-compatible router. Set a non-empty prefix such as `/s3` to enable the gateway. |

## Background jobs

| Variable | Default | Description |
|----------|---------|-------------|
| `JOBS_WORKERS` | `4` | Background worker-pool size. `>0` routes indexing/antivirus/replication through the durable jobs table with retry; `0` makes the indexer process events inline (and disables antivirus/replication, which require the pool). |
| `JOBS_MAX_DEPTH` | `0` | `>0` caps pending-job backlog; enqueue is rejected with `ErrQueueFull` once the backlog reaches this size, allowing callers to shed load or return `429`. `0` = no cap. |

## AI / RAG

| Variable | Default | Description |
|----------|---------|-------------|
| `AI_INDEX_ENABLED` | `false` | Enable text extraction + chunking + embedding of uploaded objects. Master switch for the AI pipeline. |
| `AI_HYBRID_SEARCH` | `false` | Fuse vector + BM25 retrieval with reciprocal-rank fusion. |
| `AI_DEGRADED_MODE` | `false` | Global AI kill switch: all AI endpoints return `503` without calling providers. |
| `AI_CHUNK_WINDOW` | `600` | Text chunk window size in characters. |
| `AI_CHUNK_OVERLAP` | `80` | Character overlap between adjacent chunks. |
| `AI_AGENT_MAX_STEPS` | `4` | Maximum agent tool-call loop iterations before forcing a final answer. |
| `AI_EMBED_CACHE_SIZE` | `0` | `>0` memoizes up to N query embeddings in a bounded in-memory cache, cutting repeated embed latency and provider cost. `0` disables. |
| `AI_SEARCH_CACHE_SIZE` | `0` | `>0` caches up to N whole search results; identical normalized queries skip embed + retrieval + rerank. `0` disables. |
| `AI_SEARCH_CACHE_TTL_SECONDS` | `30` | TTL (seconds) bounding staleness of cached search results. Only used when `AI_SEARCH_CACHE_SIZE > 0`. |
| `AI_REINDEX_STALE_ON_START` | `false` | On boot, re-embed objects whose stored chunks reference a different embedding model than the current embedder. One-shot; safe to run repeatedly (idempotent). |
| `AI_EMBED_PROVIDER` | `hash` | Embedder: `hash` (built-in, dependency-free) or `http` (OpenAI-compatible). Lower-cased. |
| `AI_EMBED_ENDPOINT` | _(empty)_ | Embedding service base URL, e.g. `http://localhost:11434` (Ollama). **Required when** `AI_INDEX_ENABLED=true` **and** `AI_EMBED_PROVIDER=http`. |
| `AI_EMBED_MODEL` | `text-embedding-3-small` | Embedding model name. |
| `AI_EMBED_API_KEY` | _(empty)_ | API key for the embedding endpoint. |
| `AI_EMBED_DIM` | `256` | Embedding dimensionality (e.g. `768` for `nomic-embed-text`). |
| `AI_CHAT_PROVIDER` | _(empty = off)_ | Chat LLM: `http` (OpenAI-compatible), `mock` (echo, for testing), or empty. Lower-cased. |
| `AI_CHAT_ENDPOINT` | _(empty)_ | Chat service base URL, e.g. `http://localhost:11434`. |
| `AI_CHAT_MODEL` | `gpt-4o-mini` | Chat model name. |
| `AI_CHAT_API_KEY` | _(empty)_ | API key for the chat endpoint. |
| `AI_COST_PROMPT_PER_1K` | `0` | USD cost per 1 000 prompt tokens sent to the chat LLM. Used to estimate and record per-call spend. `0` = don't estimate. |
| `AI_COST_COMPLETION_PER_1K` | `0` | USD cost per 1 000 completion tokens from the chat LLM. |
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
| `AI_VECTOR_BACKEND` | _(empty = brute-force)_ | Vector retrieval backend: `pgvector` (ANN via Postgres + the `vector` extension; uses `AI_VECTOR_DSN`) or `qdrant` (external Qdrant store; uses `AI_VECTOR_URL`). Empty keeps the default brute-force repository scan. Opt-in; unverified in CI. Lower-cased. |
| `AI_VECTOR_DSN` | _(empty)_ | Postgres DSN for the `pgvector` (and `pgfts`) backends. Required when `AI_VECTOR_BACKEND=pgvector`. |
| `AI_VECTOR_URL` | _(empty)_ | Qdrant REST base URL, e.g. `http://localhost:6333`. Required when `AI_VECTOR_BACKEND=qdrant`. The adapter mirrors chunk writes into Qdrant and serves search from it; the collection is auto-provisioned at startup from the embedder's dimension (Cosine), best-effort and idempotent. |
| `AI_VECTOR_API_KEY` | _(empty)_ | Qdrant API key, sent as the `api-key` header when non-empty. |
| `AI_VECTOR_COLLECTION` | `aero_chunks` | Qdrant collection holding chunk points. |
| `AI_LEXICAL_BACKEND` | _(empty = in-process BM25)_ | Lexical backend: `pgfts` (Postgres full-text search; reuses `AI_VECTOR_DSN`). Empty keeps the in-process BM25 index. Opt-in; unverified in CI. Lower-cased. |

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTH_KEYS` | _(empty = open)_ | Comma-separated API keys as `token:tenant:scope+scope` (e.g. `prod-rw:acme:read+write,ops:*:admin`). Tenant `*` = operator (any tenant). Empty disables API-key auth (MVP/open mode). **Deployment caveat — the `admin` scope is operator-grade on its own:** `requireAdmin` checks scope only (never the caller's tenant) and the admin API takes the target tenant from the request body/path, so a tenant-scoped admin key (e.g. `ops:acme:admin`) is operator-equivalent: it can mint keys/JWTs for any tenant, delete files in any tenant, and set quotas. Never issue tenant-scoped admin keys unless operator equivalence is intended. |
| `AUTH_JWT_SECRET` | _(empty)_ | Secret enabling HS256 JWT verification and issuance (`POST /v1/admin/jwt`). |
| `AUTH_JWKS_ENDPOINT` | _(empty)_ | Snaplink JWKS URL. Setting it activates `interfaces/ssoclient/rs`; Aero does not implement external JWT cryptography. |
| `AUTH_JWKS_KEY_TTL` | `3600` | Snaplink SDK background JWKS refresh interval in seconds; unknown keys also trigger the SDK's bounded refresh path. |
| `AUTH_JWT_ISSUER` | _(empty)_ | Pins `iss` for local HS256 and the Snaplink resource-server SDK. |
| `AUTH_JWKS_AUDIENCE` | _(empty)_ | Pins external tokens to an `aud`, `client_id`, or `azp` value. Required for browser OIDC login. |
| `AUTH_JWKS_TENANT_CLAIM` | `ten` | External JWT claim mapped to the Aero tenant: `ten`, `tenant_id`, or `sub`. |
| `AUTH_JWKS_CLIENT_TENANTS` | _(empty)_ | Optional `client_id:tenant` pairs. Use this for Snaplink tenant-bound clients because Snaplink access tokens intentionally omit `tenant_id`; an unmapped client fails closed when this map is configured. |
| `AUTH_JWKS_DEFAULT_SCOPES` | _(empty)_ | Comma-separated Aero scopes used only when a verified external token has no recognized `read`/`write`/`admin` scope. Use only with issuer and audience/client pinning. |
| `AUTH_OIDC_ISSUER` | _(empty)_ | Enables browser Authorization Code + PKCE login through Snaplink's `remote.BrowserFlow` and `TokenClient` when supplied with client ID and redirect URI. |
| `AUTH_OIDC_CLIENT_ID` | _(empty)_ | Public OIDC client ID; must equal `AUTH_JWKS_AUDIENCE`. |
| `AUTH_OIDC_REDIRECT_URI` | _(empty)_ | Exact registered callback, e.g. `https://vault.example.com/auth/oidc/callback`. |
| `AUTH_OIDC_AUTHORIZATION_ENDPOINT` | `<issuer>/auth/login` | Snaplink browser-hosted login endpoint. |
| `AUTH_OIDC_TOKEN_ENDPOINT` | `<issuer>/token` | Authorization-code token endpoint. |
| `AUTH_OIDC_SCOPES` | `openid,profile,email` | Comma-separated scopes requested during login. |
| `AUTH_PRESIGN_SECRET` | _(empty = process-random)_ | HMAC key for REST presigned GET/PUT capability URLs. Configured values must be at least 32 bytes. Set the same value on every replica so URLs survive restarts and load-balancer routing; an empty value is suitable only for single-process development because issued URLs become invalid after restart. GET capabilities still traverse Aero Vault, so tenant suspension, bucket policy, ACL explicit deny, and object deletion take effect immediately. |
| `AUTH_ANONYMOUS_PUBLIC_READ` | `false` | Parse-only (no effect through the production chain): intended to allow unauthenticated `GET`/`HEAD` of public-read objects with the handler ACL gate enforcing object ACLs. The auth ring does admit anonymous object reads (`internal/auth`), but the REST router's `requireRESTScope` scope gate then rejects them with `401 not authenticated` before the handler's ACL check runs (`requireRESTScope` → `Require(read)` has no anonymous carve-out); the S3 gateway has no anonymous admission at all. The flag therefore currently serves no path — do not rely on it until the 12-ring fix lands (recorded as defect S1 in `docs/requirements/internal-cli-activation-gate-scope-matrix-e2e-v1.design.md` §2.5, with a 12-ring integration test as the acceptance vehicle). |
| `AUTH_PERSIST_KEYS` | `false` | Back runtime API keys with the DB (`api_keys` table, tokens sha256-hashed). Keys survive restart and are shared across replicas. Also acts as an implicit auth switch: setting this without `AUTH_KEYS` still enables auth. |
| `AUTH_KEY_CACHE_TTL_SECONDS` | `0` | `>0` adds a bounded TTL'd read-through cache in front of the DB key lookup, reducing per-request DB hits. Revokes are bounded by this TTL — keep short (e.g. 30). When `EVENTS_TRANSPORT_DSN` is set, add/revoke also broadcasts immediately via a dedicated Postgres LISTEN/NOTIFY channel (`aero_key_invalidate`) so other replicas drop the cache entry without waiting for TTL expiry. |
| `S3_SIGV4_CREDENTIALS` | _(empty)_ | AWS SigV4 credentials for the S3 endpoint: `accessKey:secretKey:tenant[:scope+scope]`, comma-separated (e.g. `AKIA...:secret...:acme:read+write`). |

## Enterprise access, shares, and public assets

These controls are opt-in. Enabling them also requires at least one ordinary
authentication source (API key, JWT/JWKS, SigV4, or persistent API-key store).

| Variable | Default | Description |
|----------|---------|-------------|
| `ACCESS_CONTROL_ENABLED` | `false` | Enable normalized principals, ownership, department/resource ACL enforcement, protected shares, and public assets. |
| `ACCESS_DELETE_FAIL_CLOSED` | `true` | Fail-closed delete gate: with access control disabled (`ACCESS_CONTROL_ENABLED=false`), object deletes (REST/WebDAV/admin/MCP/quarantine via the service gate) are denied unless this is explicitly `false` (restores the legacy allow). **The S3 gateway is unaffected** — it always denies deletes without access control, and this flag does not re-enable it; only `ACCESS_CONTROL_ENABLED=true` does. See breaking-change notes. |
| `ACCESS_DEFAULT_POLICY` | `deny` | `deny` requires ownership/ACL/admin access. `tenant` allows the existing `read`/`write` scope fallback only when no resource ACL applies; useful for gradual migration. |
| `ACCESS_SHARE_SECRET` | _(empty)_ | Required when enabled; at least 32 bytes and identical on every replica. HMAC-protects share passwords. |
| `ACCESS_PUBLIC_BASE_URL` | _(empty)_ | Canonical external base URL placed in returned share/asset URLs, e.g. `https://source.ywbsd.site`. Empty derives it from the request. |

For Snaplink, configure a tenant-bound `aero-vault` OAuth client, pin issuer and
audience, and map that trusted client to the Aero tenant. State/cookie handling,
authorization URL construction, PKCE, and code exchange use Snaplink's
`interfaces/ssoclient/remote.BrowserFlow` and `TokenClient`; validation calls
`interfaces/ssoclient/rs` directly. Only the mapping below is application-specific:

```dotenv
AUTH_JWT_ISSUER=https://sso.example.com
AUTH_JWKS_ENDPOINT=https://sso.example.com/.well-known/jwks.json
AUTH_JWKS_AUDIENCE=aero-vault
AUTH_JWKS_CLIENT_TENANTS=aero-vault:default
AUTH_JWKS_DEFAULT_SCOPES=read,write
```

Snaplink remains the identity, login, tenant-membership, and application-role
authority. Aero Vault owns file/folder ACLs, ownership, shares, and published
asset state; no Snaplink domain type enters the service layer.

## Snaplink billing and metering

This optional machine-to-machine integration is independent from browser OIDC.
Each Aero tenant has its own Snaplink confidential client. The binding file
contains identifiers and the name of a secret environment variable; it cannot
contain a client secret because the decoder rejects unknown fields.

| Variable | Default | Description |
|----------|---------|-------------|
| `BILLING_ENABLED` | `false` | Enable entitlement projection and the durable usage outbox. |
| `BILLING_BASE_URL` | _(required)_ | HTTPS base URL of `snaplink-billing`. |
| `BILLING_TOKEN_URL` | _(required)_ | HTTPS Snaplink OAuth token endpoint used for `client_credentials`. |
| `BILLING_BINDINGS_FILE` | _(required)_ | Server-only JSON mapping each Aero `tenant_id` to a Snaplink `client_id` and `client_secret_env`. |
| `BILLING_ALLOW_INSECURE_HTTP` | `false` | Permit HTTP only for `localhost` or a loopback IP in development. Never permits a non-loopback HTTP endpoint. |
| `BILLING_HTTP_TIMEOUT_SECONDS` | `5` | End-to-end timeout for token, entitlement, and usage HTTP requests. |
| `BILLING_PROJECTION_INTERVAL_SECONDS` | `60` | Refresh interval for versioned entitlement snapshots. |
| `BILLING_OUTBOX_POLL_MILLISECONDS` | `1000` | Durable usage outbox polling interval. |
| `BILLING_OUTBOX_BATCH_SIZE` | `32` | Facts claimed per poll (`1..500`). |
| `BILLING_OUTBOX_CLAIM_TTL_SECONDS` | `30` | Multi-replica delivery lease; must exceed the HTTP timeout. |

See [Snaplink billing integration](snaplink-billing.md) for binding format,
central source-binding requirements, quota semantics, failure behavior, and a
Helm mounting example.

## Snaplink Audit Governance relay

This optional, default-off runtime forwards only redacted admin, security, and
file lifecycle facts. It is independent from browser OIDC and from Billing;
startup rejects reused Billing/Audit client IDs, secret environment names, or
secret values.

| Variable | Default | Description |
|----------|---------|-------------|
| `AUDIT_GOVERNANCE_ENABLED` | `false` | Enable durable capture, reconcile, and delivery. An enabled boot with an empty `bindings` manifest is refused (fail-closed activation gate) unless `AUDIT_GOVERNANCE_DRAIN` is set. **Strict boolean:** any set, non-canonical value (e.g. `yes`, `on`) fails startup instead of silently disabling the relay — only `strconv.ParseBool` spellings (`true`/`false`/`1`/`0` and case variants) are accepted. |
| `AUDIT_GOVERNANCE_DRAIN` | `false` | Permit an empty `bindings` manifest to apply a drain (disable flow only). `true` with a non-empty manifest fails startup; clear the flag (and remove the empty manifest) after disabling. **Strict boolean:** non-canonical values (e.g. `yes`) fail startup instead of silently no-op'ing the drain. |
| `AUDIT_GOVERNANCE_BASE_URL` | _(required)_ | Audit Governance base URL. HTTPS is required except for a literal loopback HTTP endpoint. |
| `AUDIT_GOVERNANCE_TOKEN_URL` | _(required)_ | Snaplink OAuth token endpoint, with the same URL policy. |
| `AUDIT_GOVERNANCE_BINDINGS_FILE` | _(required)_ | Revisioned, strict JSON tenant/client binding file; duplicate credentials, symlinks, and group/world-writable files are rejected. |
| `AUDIT_GOVERNANCE_HMAC_KEY` | _(required)_ | Dedicated `32..4096` byte HMAC key for tenant/field-domain pseudonyms. Must not reuse an OAuth or Billing credential. **Share scope:** only across replicas of the *same* origin-counter store (the same underlying DB) — never across independent databases, where identical `origin_id` values collide into duplicate EventIDs and the receiver silently drops one instance's ledger rows. **Rotation is not idempotency-safe:** rotating changes every fact ID, so in-flight facts (claimed-but-unacked, or pruned-then-re-enqueued) get a new EventID and the receiver double-ledgers them (tombstones protect only delivered facts); keep the key stable for at least `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS` and rotate only after the outbox is fully drained. |
| `AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS` | `5` | Token/event/database operation timeout (`1..29`). |
| `AUDIT_GOVERNANCE_POLL_MILLISECONDS` | `1000` | Outbox reconcile/claim interval. |
| `AUDIT_GOVERNANCE_BATCH_SIZE` | `32` | Facts claimed per poll (`1..500`). |
| `AUDIT_GOVERNANCE_CLAIM_TTL_SECONDS` | `30` | Fenced delivery lease (`1..60`); must exceed twice the HTTP timeout so publish plus acknowledgement/retry remain fenced. |
| `AUDIT_GOVERNANCE_INITIAL_BACKOFF_SECONDS` | `1` | Initial retry delay before deterministic jitter. |
| `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` | `300` | Retry cap (`2..86400`): the per-attempt delay **and** the cumulative transient-retry window. A fact failing with transient-only errors (e.g. a receiver answering 500) is re-POSTed with bounded backoff until `now − first attempt` strictly exceeds this value, then dead-letters with the same terminal-with-retention semantics as permanent classes (`failed_at_ns` set, never re-claimed, pruned after the delivered retention). The window anchor (`first_attempt_at_ns`) is set once on the row's first claim and survives lease/ack-lost re-claims. |
| `AUDIT_GOVERNANCE_MAX_LAG_SECONDS` | `900` | Oldest undelivered outbox age after which the relay reports degraded (`degraded=1`; `/readyz` stays `200` with `degraded:true`). The `AuditGovernanceBacklogDegraded` alert fires earlier at half this value (450s at default) as early warning, or immediately when degraded. |
| `AUDIT_GOVERNANCE_RECONCILE_BATCH_SIZE` | `100` | Historical local facts reconciled per tenant and poll (`2..500`); batches alternate admin/security and file origins. |
| `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS` | `604800` | Retain delivered outbox bodies before replacing them with permanent origin tombstones (`3600..31536000`). |
| `AUDIT_GOVERNANCE_CLEANUP_INTERVAL_SECONDS` | `3600` | Delivered-row cleanup interval (`60..86400`, no greater than retention). Full batches continue at the poll interval. |
| `AUDIT_GOVERNANCE_CLEANUP_BATCH_SIZE` | `100` | Delivered rows atomically tombstoned and removed per cleanup transaction (`1..500`). |

The OAuth request always asks for the single scope `audit:event:write` and
RFC 8707 resource `audit-governance`; neither is runtime-configurable. See
[Snaplink Audit Governance integration](snaplink-audit-governance.md) for the
fixed schema, source provisioning, redaction, HA, and deployment contract.

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
| `AI_RATE_LIMIT_RPS` | `0` | Per-tenant AI endpoint rate limit (req/s). Applies to `/v1/search`, `/v1/chat`, `/v1/chat/stream`, `/v1/agent`, and `/v1/lineage` independently of `RATE_LIMIT_RPS`. `0` disables. |
| `AI_RATE_LIMIT_BURST` | `0` | Burst size for the AI rate limiter. `0` disables. |
| `ADMIN_RATE_LIMIT_RPS` | `0` | Per-tenant rate limit for `/v1/admin/*`, independent of the global and AI limiters. `0` disables. |
| `ADMIN_RATE_LIMIT_BURST` | `0` | Burst size for the admin rate limiter. `0` disables. |

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
| `EVENTS_TRANSPORT` | _(empty = in-process)_ | Cross-instance event transport: `postgres` (Postgres `LISTEN`/`NOTIFY`, requires `EVENTS_TRANSPORT_DSN`). Empty keeps the default in-process fan-out. |
| `EVENTS_TRANSPORT_DSN` | _(empty)_ | Postgres DSN for the `postgres` event transport (and for the `aero_key_invalidate` cross-replica key-invalidation channel). |

## Deletion transactional outbox

File delete (`FileService.Delete`, hard and soft) commits the metadata delete and two versioned event facts (`vault.file.deleted@1.1` / `vault.file.notify@1.1`) in **one transaction**; the always-on relay — unless `EVENT_OUTBOX_ENABLED=false` — drains them (claim → deliver → complete). `notify@1.1` facts are POSTed byte-exact to matching bucket-notification targets; `deleted@1.1` facts are durable lifecycle records that are completed without local re-broadcast. Delivery is exactly-once only after `complete`; the deliver→complete window is at-least-once (S3-equivalent) — receivers must be idempotent. |
| Variable | Default | Description |
|----------|---------|-------------|
| `EVENT_OUTBOX_ENABLED` | `true` | Kill-switch for the relay loop (claim → deliver → complete → prune). Enqueue of the two delete facts is **never** gated — rows keep accumulating while disabled and drain (FIFO) once re-enabled. Unparseable values fall back to the default (`true`). |
| `EVENT_OUTBOX_POLL_INTERVAL_MILLIS` | `1000` | Relay claim poll interval (`1..60000`). |
| `EVENT_OUTBOX_BATCH_SIZE` | `32` | Facts claimed per poll (`1..500`). |
| `EVENT_OUTBOX_CLAIM_TTL_SECONDS` | `30` | Fenced delivery lease; must exceed twice `EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS` so a slow target plus lease expiry cannot cause concurrent duplicate POSTs without any crash (`1..600`). Worst-case in-flight time per fact is `targets×timeout` — raise the TTL when a rule targets more than 3 endpoints at the default timeout. |
| `EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS` | `5` | Per-target HTTP POST timeout (`1..29`). |
| `EVENT_OUTBOX_MAX_ATTEMPTS` | `10` | Delivery attempts before a fact becomes terminal `failed` (`1..1000`); prune cutoffs are configurable — defaults 24h for delivered, 7 days for failed, see `EVENT_OUTBOX_DELIVERED_RETENTION_HOURS` / `EVENT_OUTBOX_FAILED_RETENTION_HOURS`. |
| `EVENT_OUTBOX_DELIVERED_RETENTION_HOURS` | `24` | Delivered rows are pruned after this many hours (`1..8760`). Non-numeric values silently fall back to the default (`24`). Prune runs once per 60 relay rounds — a 60s–60min cadence across the poll bounds — so effective retention is the horizon plus up to one prune interval (≈2× the horizon at the extremes, e.g. poll 60s + retention 1h ⇒ 1–2h). With replicas sharing one `event_outbox` table, effective retention is the **minimum across running nodes** — the most aggressive prune wins fleet-wide; configure identically on every replica. |
| `EVENT_OUTBOX_FAILED_RETENTION_HOURS` | `168` | Terminal `failed` rows are pruned after this many hours (`1..8760`; default 168 = 7 days, matching the shipped behavior). Non-numeric values silently fall back to the default (`168`). Same prune cadence and fleet-min property as `EVENT_OUTBOX_DELIVERED_RETENTION_HOURS`. |
| `AUDIT_SINK_L2_ENDPOINT` | _(empty = L2 off)_ | Optional L2 audit sink: the relay POSTs each `vault.file.deleted@1.1` fact to this URL with the tenant's bearer token. Must be an absolute URL without credentials/query/fragment; scheme must be `https`, or `http` only to a loopback host (`localhost` / loopback IP). Enforced twice at startup (config validation + relay construction) — an invalid value aborts boot. Empty disables L2: facts are still completed without delivery and the L0 `audit_log` stays authoritative. |
| `AUDIT_SINK_L2_BINDINGS_FILE` | _(empty)_ | JSON file `{"bindings":[{"tenant":"…","token":"…"\|"token_env":"…"}]}` mapping tenant → bearer token. Must be a regular file with mode 0600 (owner-only) and ≤1 MiB; unknown fields and trailing JSON are rejected — any violation fails startup. `token` values must be ≥16 characters with no surrounding whitespace; prefer the `token_env` indirection (env var must start with `AUDIT_SINK_L2_TOKEN_` and resolve) to keep secrets out of the filesystem. Duplicate tenants or tokens are rejected. The wildcard tenant `"*"` is accepted as an operator-grade mapping (same trust class as the auth operator key). Without this file every tenant is unbound: deleted facts complete without delivery — the startup log line `event outbox relay L2 audit sink enabled … bindings: 0` is the only signal. |

> L2 delivery rides the same relay machinery and the same at-least-once
> contract as the `EVENT_OUTBOX_*` options above (claim lease, retry/backoff,
> idempotent receivers) — no separate knobs; per-fact in-flight time is still
> bounded by `EVENT_OUTBOX_CLAIM_TTL_SECONDS`. Tokens are never logged.

## Background reconcile / lifecycle

| Variable | Default | Description |
|----------|---------|-------------|
| `RECONCILE_INTERVAL_MINUTES` | `0` | `>0` enables periodic orphan reconciliation + lifecycle expiry on this interval. `0` disables. |
| `RECONCILE_DELETE_ORPHAN_BLOBS` | `false` | Delete blobs that have no remaining DB reference during reconciliation. |
| `RECONCILE_ORPHAN_GRACE_MINUTES` | `60` | Minimum age (minutes) an orphan blob must reach before it is eligible for deletion. |
| `RECONCILE_TENANTS` | `default` | Comma-separated list of tenants to scan. Empty/unset defaults to `default`. |
| `RECONCILE_CLUSTER_SINGLETON` | `false` | Run reconcile and lifecycle sweeps on only one instance at a time, using a DB advisory `leases` table. Prevents duplicate destructive sweeps when running multiple replicas. Requires `RECONCILE_INTERVAL_MINUTES > 0`. |
| `RECONCILE_RETENTION_DAYS` | `0` | `>0` permanently purges rows soft-deleted more than N days ago (and their blobs) during the retention sweep. `0` disables. Runs as a cluster singleton when `RECONCILE_CLUSTER_SINGLETON=true`. |
| `RECONCILE_SCRUB_ENABLED` | `false` | Verify stored Content-MD5 checksums during reconcile and mark corrupt objects. |
| `UPLOAD_GC_TTL_HOURS` | `168` | Remove abandoned multipart uploads older than this many hours during reconcile. `0` disables upload GC. |

## Write idempotency (`/v1`)

| Variable | Default | Description |
|----------|---------|-------------|
| `IDEMPOTENCY_TTL_HOURS` | `0` | `>0` lets the retention sweep delete stored `Idempotency-Key` records older than this many hours, bounding the dedupe table. `0` keeps them forever. |
| `IDEMPOTENCY_HASH_BODY` | `false` | Fold a SHA-256 of the request body into the `Idempotency-Key` fingerprint (Stripe-style v2): the same key replayed with **different bytes** is rejected with `409 IdempotencyConflict` instead of replaying the stored response. Bodies are buffered while hashing — up to 8 MiB in memory, larger payloads spill to a temp file — and handed to the handler unchanged. **Caveat:** enabling (or disabling) it changes fingerprints, so keys claimed before the flip will `409` on retry until they expire (`IDEMPOTENCY_TTL_HOURS`) or new keys are used. |

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

## Thumbnail decode budget

`/v1/files/<key>/thumbnail` (and the S3-compatible derivation path) decodes images
with only the standard library. The decode pipeline is bounded by package-level
constants (`internal/thumbnail`):

| Constant | Value | Meaning |
|----------|-------|---------|
| `MaxSourceDim` | `8192` px | Declared source dimensions above this are rejected from the header before any pixel buffer is allocated |
| `MaxProgressiveSourceDim` | `4096` px | Declared source dimensions above this are rejected from the header for progressive (SOF2) JPEG sources |
| `MaxSourceBytes` | `128 MiB` | Compressed input consumed per request |
| `MaxMetadataBytes` | `8 MiB` | Pre-image metadata (JPEG APPn/COM segments) accepted before `ErrMetadataTooLarge` |
| `maxConcurrentDecodes` | `4` | In-flight decode calls at once (package-level semaphore) |
| `quality` | `82` | JPEG encode quality |

Per-request worst-case decode allocation: ≈ 268 MiB (PNG RGBA at `MaxSourceDim`);
≈ 275 MiB (progressive JPEG, capped at `MaxProgressiveSourceDim`). Aggregate
ceilings: ≈ 1.1 GiB (PNG RGBA) / ≈ 1.1 GiB (progressive JPEG) across all
concurrent decodes.

**Decode-slot wait contract:** on the REST derivation path the decode slot is
acquired **before** the object stream opens (`thumbnail.GenerateContextWithOpener`
acquires, then invokes the opener, then decodes), so a request parked on the
semaphore holds no stream at all — at most `maxConcurrentDecodes` (4) object
streams are concurrently open for thumbnailing on **any** backend (local fd /
S3/OSS/COS GET), for any request concurrency. Waiters acquire a slot via a
`select` on the request context — cancellation (client disconnect, or the
`REQUEST_TIMEOUT_SECONDS` deadline applied to the `/thumbnail` route) releases
a parked waiter immediately with `context.Canceled`/`context.DeadlineExceeded`,
without opening a stream and without consuming a slot. In-flight decodes abort
at the next phase boundary (post-config, post-decode, pre-encode) — and inside
the scale/rotation phases within `cancelCheckRows` (64) rows of pixel work —
when the request context is done; the decode slot is released immediately and
the (already-open) stream is closed. Stream failures caused by the request
context surface as the context error (504 `Timeout` for a server-side deadline,
silent return on client disconnect), never as an invalid-image error;
object-stream open failures classify as the underlying object error
(404/403/409/…), never as an image error.

**Deployment prerequisite:** set `MAX_INFLIGHT_REQUESTS` and/or `RATE_LIMIT_RPS`
> 0 in production. The semaphore bounds in-flight *decodes* (4), not the number
of *waiters*; the request-context lifetime bounds each wait, but admission
control is the outer bound on concurrent waiters (defaults are 0 = unlimited).

---

## Demo configuration reference

The values used by `deploy/docker-compose.demo.yml` (a realistic production-shaped
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
