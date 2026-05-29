# aero-vault

**An AI-native file platform** that exposes the same unified object backend over
**REST**, an **S3-compatible** API, **WebDAV**, and the **Model Context Protocol
(MCP)** — with a built-in retrieval-augmented generation (RAG) pipeline,
multi-tenancy, and first-class observability.

Upload a file once and it is immediately available as an S3 object, a WebDAV
file, an MCP resource, and (optionally) an embedded, searchable chunk that a RAG
chat endpoint can cite. Storage is pluggable across local disk and any
S3-compatible store (AWS S3, MinIO, Alibaba OSS, Tencent COS).

```
                       ┌──────────────────────────────────────────────┐
   REST  /v1/*         │                                              │
   S3    /s3/*    ───▶ │   auth · tenant · rate-limit · OTel · CORS    │
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

- **Four protocols, one backend** — REST (`/v1`), S3-compatible gateway (`/s3`),
  WebDAV (`/webdav`), and MCP (`/mcp` JSON-RPC + stdio).
- **Pluggable storage** — `local` filesystem, `s3` (AWS / MinIO / any
  S3-compatible endpoint), native Alibaba `oss`, native Tencent `cos`.
- **Pluggable metadata DB** — SQLite (default, embedded) or PostgreSQL.
- **Multi-tenancy** — tenant isolation via the `X-Aero-Tenant` header and a
  `tenant/bucket/key` storage-key scheme.
- **Authentication & authorization** — scoped API keys, HS256 JWTs, and AWS
  SigV4 for the S3 endpoint; per-route `read`/`write`/`admin` scopes; optional
  anonymous public-read.
- **Per-tenant quotas** — byte and object limits enforced before upload.
- **Versioning, object-lock / WORM, tagging, ACLs, lifecycle** — bucket-level
  toggles plus per-object retention locks and canned ACLs.
- **Events & webhooks** — an in-process event bus persists object-lifecycle
  events, streams them over SSE, and POSTs HMAC-signed webhooks with retry.
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

- Go **1.25+** (matches `go.mod`) to build from source, or Docker to run the image.
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

`docker-compose.demo.yml` brings up the full platform plus a local LLM
([Ollama](https://ollama.com)), an S3-compatible object store (MinIO), a
PostgreSQL metadata DB, and an OpenTelemetry collector:

```bash
docker compose -f docker-compose.demo.yml up --build
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
  docker compose -f docker-compose.demo.yml up --build
```

The demo enables AI indexing, hybrid search, Postgres, S3 (MinIO), the job pool,
Prometheus `/metrics`, and OTLP export to the collector — a realistic
production-shaped configuration.

---

## Protocols

| Protocol | Mount | Notes |
|----------|-------|-------|
| **REST** | `/v1` | Primary JSON API: files, search, chat, agent, events, buckets, admin. OpenAPI at `/openapi.json`, Swagger UI at `/docs`. |
| **S3-compatible** | `/s3` (configurable via `S3_COMPAT_PREFIX`) | Path-style `GET/PUT/HEAD/DELETE` objects, `ListObjectsV2`, multipart, tagging, ACL, copy, batch delete. Auth via AWS SigV4 or `X-Api-Key`. |
| **WebDAV** | `/webdav` (set `WEBDAV_PREFIX`; empty disables) | Mountable from Finder, Explorer, rclone, Cyberduck. `PROPFIND`/`MKCOL` supported. |
| **MCP** | `POST /mcp` (HTTP) or `aero-vault mcp` (stdio) | Model Context Protocol server exposing `list_files`, `read_file`, and `search` tools plus object resources (`aero-vault://{tenant}/{bucket}/{key}`). |

All protocols share one `FileService` core, so an object written through any
protocol is visible through every other.

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
| `S3_COMPAT_PREFIX` | `/s3` | Mount prefix for the S3 gateway (empty disables). |
| `JOBS_WORKERS` | `4` | Background worker pool size (`0` = inline indexing). |
| `AI_INDEX_ENABLED` | `false` | Enable extraction + chunking + embedding of uploads. |
| `AI_HYBRID_SEARCH` | `false` | Fuse vector + BM25 with reciprocal-rank fusion. |
| `AI_EMBED_PROVIDER` | `hash` | `hash` (built-in, dep-free) or `http` (OpenAI-compatible). |
| `AI_CHAT_PROVIDER` | _(off)_ | `http` (OpenAI-compatible), `mock`, or empty. |
| `AUTH_KEYS` | _(open)_ | `token:tenant:scope+scope,...`; empty = no auth. |
| `AUTH_JWT_SECRET` | _(off)_ | Enables HS256 JWT verification + issuance. |
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

CI (GitHub Actions) builds, vets, tests, and `gofmt`-checks on every push and
pull request; see [`.github/workflows/`](.github/workflows).

---

## License

See the repository for license details.
