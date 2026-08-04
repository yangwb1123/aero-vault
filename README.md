# aero-vault

**An AI-native file platform** that can expose the same unified object backend over
**REST**, an **S3-compatible** API, **WebDAV**, and the **Model Context Protocol
(MCP)** — with a built-in retrieval-augmented generation (RAG) pipeline,
multi-tenancy, and first-class observability.

Upload a file once and it is immediately available through every enabled
protocol and (optionally) as an embedded, searchable chunk that a RAG chat
endpoint can cite. Storage is pluggable across local disk and any
S3-compatible store (AWS S3, MinIO, Alibaba OSS, Tencent COS).

```
                       ┌──────────────────────────────────────────────┐
   REST  /v1/*         │                                              │
   S3 (opt-in) /s3/* ─▶ │   auth · tenant · rate-limit · OTel · CORS    │
   WebDAV /webdav      │            (middleware chain)                 │
   MCP   /mcp          │                                              │
                       └───────────────────────┬──────────────────────┘
                                               ▼
                                    ┌────────────────────┐
                                    │    FileService     │  one shared core
                                    └─────┬───────┬──────┘
                            ┌─────────────┘       └──────────────┐
                            ▼                                    ▼
                   ┌────────────────┐                  ┌──────────────────┐
                   │  Storage       │                  │  Repository      │
                   │  local/s3/     │                  │  sqlite/postgres │
                   │  oss/cos       │                  │  (metadata)      │
                   └────────────────┘                  └──────────────────┘
                            │                                    │
                            └──────────► events bus ◄────────────┘
                                              │
              ┌───────────────┬──────────────┼───────────────┬────────────┐
              ▼               ▼               ▼               ▼            ▼
          indexer        antivirus      replication       webhooks      job queue
       (extract/chunk/   (scan/         (cross-region)   (HMAC-signed)  (durable,
        embed → RAG)      quarantine)                                    retrying)
```

---

## Features

- **Four protocols, one backend** — REST (`/v1`), opt-in S3-compatible gateway
  (for example `/s3`), WebDAV (`/webdav`), and MCP (`/mcp` JSON-RPC + stdio).
- **Pluggable storage** — `local` filesystem, `s3` (AWS / MinIO / any
  S3-compatible endpoint), native Alibaba `oss`, native Tencent `cos`.
- **Pluggable metadata DB** — SQLite (default, embedded) or PostgreSQL.
- **Multi-tenancy** — tenant isolation via the `X-Aero-Tenant` header and a
  `tenant/bucket/key` storage-key scheme.
- **Enterprise identity & authorization** — API keys, local JWT, Snaplink's Go
  browser-flow, token-client, and resource-server SDKs,
  SigV4, normalized principals, object ownership, nested departments, and
  inheritable user/group/role/department allow/deny ACLs enforced in FileService.
- **File operations & distribution** — revocable/password/expiry/use-limited
  share links, stable cacheable public image URLs for blogs, and portable
  per-prefix tar.gz backup exports.
- **Per-tenant quotas** — byte and object limits enforced before upload.
- **Versioning, object-lock / WORM, tagging, ACLs, lifecycle** — bucket-level
  toggles plus per-object retention locks and canned ACLs.
- **Events & webhooks** — an in-process event bus persists object-lifecycle
  events, streams them over SSE, and POSTs HMAC-signed webhooks with retry.
- **Streaming without a second stateful transport** — SSE powers event and
  chat streams; gRPC and WebSocket endpoints are intentionally not exposed in
  v0.4 because the REST/SDK surface covers the current public and browser use
  cases.
- **Durable background job queue** — a worker pool runs indexing, antivirus, and
  replication jobs with retry, backed by a jobs table.
- **RAG pipeline** — automatic text extraction, chunking, and embedding;
  semantic + BM25 hybrid search with reciprocal-rank fusion; optional reranking;
  RAG chat with citations; a tool-calling agent; AI-consumption lineage.
- **PII detection & redaction**, **virus scanning** (with quarantine),
  **on-demand image thumbnails**, **cross-region replication**.
- **HTTP correctness** — range requests (`206`), conditional requests
  (`If-Match` / `If-None-Match` / `304`), presigned URLs, multipart upload.
- **Observability** — OpenTelemetry traces + metrics (OTLP) and a Prometheus
  `/metrics` endpoint; `/healthz` and `/readyz` probes.

---

## Quick start

### Prerequisites

- Go **1.26.1+** (matches Snaplink's SDK requirement), or Docker to run the image.
- Optional: Docker + Docker Compose for the full demo stack.

### Build and run locally

```bash
# Build the binary (output: ./bin/aero-vault)
make build

# Configure (defaults to local storage + an embedded SQLite DB)
cp .env.example .env

# Run
./bin/aero-vault          # or: make run

# In another shell
curl -s localhost:8080/healthz
curl -s -X PUT --data-binary "hello world" \
  -H 'Content-Type: text/plain' \
  localhost:8080/v1/files/hello.txt
curl -s localhost:8080/v1/files/hello.txt          # -> hello world
open http://localhost:8080/docs                     # Swagger UI
open http://localhost:8080/ui                        # static web UI
```

With the defaults the server starts with **no authentication** (MVP mode), the
`local` storage backend writing under `./var/objects`, and an embedded SQLite
database at `./var/aero.db`.

### One-command RAG demo

`deploy/docker-compose.demo.yml` brings up the full platform plus a local LLM
([Ollama](https://ollama.com)), an S3-compatible object store (MinIO), a
PostgreSQL metadata DB, and an OpenTelemetry collector:

```bash
docker compose -f deploy/docker-compose.demo.yml up --build
```

The first run downloads the Ollama models (a few hundred MB up to ~1.3 GB), so
it takes a few minutes. Once the `app` service is listening:

```bash
./deploy/demo/seed.sh             # upload a doc, wait for indexing, run a RAG chat
open http://localhost:8080/docs   # Swagger UI
open http://localhost:9001        # MinIO console (minioadmin / minioadmin)
```

Models are overridable:

```bash
DEMO_CHAT_MODEL=llama3.2:3b DEMO_EMBED_MODEL=nomic-embed-text \
  docker compose -f deploy/docker-compose.demo.yml up --build
```

The demo enables AI indexing, hybrid search, Postgres, S3 (MinIO), the job pool,
Prometheus `/metrics`, and OTLP export to the collector — a realistic
production-shaped configuration.

---

## Protocols

| Protocol | Mount | Notes |
|----------|-------|-------|
| **REST** | `/v1` | Primary JSON API: files, search, chat, agent, events, buckets, admin. OpenAPI at `/openapi.json`, Swagger UI at `/docs`. |
| **S3-compatible** | Disabled by default; set `S3_COMPAT_PREFIX` (for example `/s3`) | Path-style `GET/PUT/HEAD/DELETE` objects, `ListObjectsV2`, multipart, tagging, ACL, copy, batch delete. Auth via AWS SigV4 or `X-Api-Key`. |
| **WebDAV** | `/webdav` (set `WEBDAV_PREFIX`; empty disables) | Mountable from Finder, Explorer, rclone, Cyberduck. `PROPFIND`/`MKCOL` supported. |
| **MCP** | `POST /mcp` (HTTP) or `aero-vault mcp` (stdio) | Model Context Protocol server exposing `list_files`, `read_file`, and `search` tools plus object resources (`aero-vault://{tenant}/{bucket}/{key}`). |
| **SSE** | `/v1/events/stream`, `/v1/chat/stream` | One-way event and token streams; uses the same authentication, tenant, rate-limit, and FileService boundaries as REST. |

All enabled protocols share one `FileService` core, so an object written through
any protocol is visible through every other enabled protocol.

REST/OpenAPI plus the language SDKs is the canonical integration surface. A
future gRPC adapter would be appropriate for internal, strongly typed
service-to-service traffic, while WebSocket would be appropriate for
bidirectional collaboration or presence. Neither should bypass `FileService`
or duplicate authorization rules.

See [`docs/api.md`](docs/api.md) for the full REST reference and S3-compatibility
matrix.

---

## Configuration

Configuration is read from environment variables (and an optional `.env` file).
The most common knobs:

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ADDR` | `:8080` | Listen address. |
| `APP_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `STORAGE_BACKEND` | `local` | `local` \| `s3` \| `oss` \| `cos`. |
| `STORAGE_LOCAL_ROOT` | `./var/objects` | Root dir for the local backend. |
| `STORAGE_S3_ENDPOINT` | _(empty = AWS)_ | S3 endpoint; point at MinIO/OSS-S3/COS-S3 for compatibility. |
| `STORAGE_S3_BUCKET` | _(required for s3)_ | Backing S3 bucket. |
| `DB_DRIVER` | `sqlite` | `sqlite` \| `postgres`. |
| `DB_DSN` | `file:./var/aero.db?_pragma=foreign_keys(1)` | Database DSN. |
| `S3_COMPAT_PREFIX` | _(empty / disabled)_ | Mount prefix for the S3 gateway; set a value such as `/s3` to enable it. |
| `JOBS_WORKERS` | `4` | Background worker pool size (`0` = inline indexing). |
| `AI_INDEX_ENABLED` | `false` | Enable extraction + chunking + embedding of uploads. |
| `AI_HYBRID_SEARCH` | `false` | Fuse vector + BM25 with reciprocal-rank fusion. |
| `AI_EMBED_PROVIDER` | `hash` | `hash` (built-in, dep-free) or `http` (OpenAI-compatible). |
| `AI_CHAT_PROVIDER` | _(off)_ | `http` (OpenAI-compatible), `mock`, or empty. |
| `AUTH_KEYS` | _(open)_ | `token:tenant:scope+scope,...`; empty = no auth. |
| `AUTH_JWT_SECRET` | _(off)_ | Enables HS256 JWT verification + issuance. |
| `AUTH_OIDC_ISSUER` | _(off)_ | Enables browser OIDC Authorization Code + PKCE through Snaplink's SDK. |
| `ACCESS_CONTROL_ENABLED` | `false` | Enable ownership, departments, resource ACLs, shares, and public assets. |
| `S3_SIGV4_CREDENTIALS` | _(off)_ | `accessKey:secretKey:tenant[:scope+scope],...` for the S3 endpoint. |
| `PROMETHEUS_ENABLED` | `false` | Expose `/metrics`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(off)_ | OTLP/HTTP endpoint, e.g. `http://localhost:4318`. |
| `WEBDAV_PREFIX` | _(disabled)_ | e.g. `/webdav`. |
| `WEBUI_ENABLED` | `true` | Static UI at `/ui`. |

This is an abridged set. See [`docs/configuration.md`](docs/configuration.md) for
the **exhaustive** reference (every variable, grouped by subsystem, with
defaults).

---

## SDKs

| SDK | Path | Notes |
|-----|------|-------|
| **Python** | [`sdk/python`](sdk/python) | Zero-dependency, standard-library-only client (`pip install ./sdk/python` or copy `aero_vault.py`). Doubles as a CLI. See [`sdk/python/README.md`](sdk/python/README.md). |
| **JavaScript / TypeScript** | [`sdk/js`](sdk/js) | JS/TS client for browser and Node. |
| **Go** | [`sdk/go`](sdk/go) | Native Go client. |

Python example:

```python
from aero_vault import Client

av = Client("http://localhost:8080", token="prod-rw", tenant="acme")
av.upload("docs/readme.txt", b"hello world", content_type="text/plain")
av.upload("images/hero.jpg", open("hero.jpg", "rb"), content_type="image/jpeg")
asset = av.publish_asset("images/hero.jpg", "blog/hero.jpg")
print(av.list_assets())
share = av.create_share("images/hero.jpg", allow_download=True, ttl_seconds=3600)
av.revoke_share(share["share"]["id"])
hits = av.search("vector database", k=5, mode="hybrid")
print(av.chat("what is in the docs?").answer)
```

---

## Documentation

| Doc | Contents |
|-----|----------|
| [`docs/architecture.md`](docs/architecture.md) | Layered architecture, request flow, multi-tenancy model, storage-key scheme. |
| [`docs/api.md`](docs/api.md) | REST API reference (by tag) with curl examples; S3-compatibility matrix; SigV4 usage. |
| [`docs/configuration.md`](docs/configuration.md) | Exhaustive environment-variable reference, grouped by subsystem. |
| [`docs/deployment.md`](docs/deployment.md) | Docker, Docker Compose, Helm chart, and production guidance. |

---

## Operations

- **Health:** `GET /healthz` (liveness), `GET /readyz` (readiness — pings the DB).
- **Metrics:** `GET /metrics` (Prometheus) when `PROMETHEUS_ENABLED=true`.
- **Tracing/metrics export:** set `OTEL_EXPORTER_OTLP_ENDPOINT` (OTLP/HTTP).
- **Dashboards/scrape config:** see [`deploy/prometheus/`](deploy/prometheus) and
  [`deploy/grafana/`](deploy/grafana).
- **Kubernetes:** a Helm chart lives in
  [`deploy/helm/aero-vault`](deploy/helm/aero-vault).

---

## Development

```bash
make build     # build ./bin/aero-vault
make run       # go run ./cmd/server
make test      # go test ./...
make tidy      # go mod tidy
make docker    # docker build -t aero-vault:dev .
```

The project also ships a **Python engineering CLI** for code-quality gating:

```bash
# One-time setup
python3 cli.py setup          # install tools + pre-commit hook

# Daily use
python3 cli.py check           # quick check (filesize + vet)
python3 cli.py harness         # full gates (filesize + complexity + arch)
python3 cli.py accept          # full acceptance suite (HARNESS.md)
python3 cli.py invariants      # engineering invariants I1–I6
python3 cli.py adr-compliance  # ADR compliance check
python3 cli.py diagnose        # system diagnosis
python3 cli.py health-report   # health report
```

Thresholds are declared in [`engineering.yaml`](engineering.yaml).
CI (GitHub Actions) runs the same CLI gates on every push and
pull request; see [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

---

## License

See the repository for license details.
