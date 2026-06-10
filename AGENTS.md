# AGENTS.md

Contract for agents/contributors working **in** this repo: how to **verify**,
**where** things live, what you must **not break**. Overview → `README.md`; deep
reference → `docs/`.

## What it is

`aero-vault` — one Go binary (`cmd/server`, Go 1.25, module
`github.com/aero-vault/aero-vault`) serving one object store over four protocols
— REST (`/v1`), S3 (`/s3`), MCP (`/mcp` + stdio), WebDAV (opt-in via
`WEBDAV_PREFIX`) — plus opt-in RAG, multi-tenancy, and observability.
**Everything funnels through one `service.FileService`; protocol packages are
thin adapters, so an object written via any protocol is visible through all.**

## Verify — the CI gate

Run from the repo root before claiming a change done, in this order (this *is*
`.github/workflows/ci.yml`):

```bash
gofmt -l .         # MUST print nothing — gofmt -w whatever you touch
go build ./...
go vet ./...
go test ./...      # SQLite + local FS only — no network, no Docker
```

Postgres paths (pgvector/pgFTS/LISTEN-NOTIFY) run under `make test-integration`
(Docker), outside the gate. `make build` → `bin/aero-vault`; `make run`.
Subcommands: `aero-vault mcp` (stdio), `aero-vault cli …` (client).

## Where things live

| Path | Responsibility |
|------|----------------|
| `cmd/server/main.go` | Entry point — the **one** place subsystems are wired (config → storage → repo → service → workers → middleware → router). |
| `internal/config` | Env-var config (+ optional `.env`), zero-value-safe defaults. Thread every new setting through here — never hardcode. |
| `internal/service` | **`FileService`** — object-CRUD core: quota, versioning, object-lock/WORM, tags, ACL, range, conditional, presign, events. Backend-agnostic. |
| `internal/storage` | `Storage` iface + `local`/`s3`/`oss`/`cos` backends, `factory.go`, envelope SSE (versioned keys + KMS), HMAC presign. `contract_test.go` = shared suite. |
| `internal/repository` | `Repository` metadata: `sqlite.go` (default, pure-Go) + `postgres.go` share `sql.go`; `migrations/{sqlite,postgres}`. |
| `internal/api/rest` | JSON API `/v1`: files, search, chat, agent, events/SSE, buckets, admin, ACL, thumbnails, OpenAPI. |
| `internal/api/s3compat` | S3 gateway `/s3`: objects, listing (v1/v2), multipart, tagging, ACL, copy, batch-delete; SigV4. |
| `internal/api/webdav` | WebDAV — dispatched **outside** chi so `PROPFIND`/`MKCOL` work. |
| `internal/mcp` | MCP server (HTTP + stdio): `list_files`, `read_file`, `search` (search only when AI on). |
| `internal/auth` | Scoped API keys (opt-in persisted + hashed), HS256 JWT, SigV4 verify, anonymous public-read. |
| `internal/middleware` | RequestID, CORS, Tenant, RateLimiter, Recoverer, AccessLog. |
| `internal/events` | In-process event bus (+ opt-in PG LISTEN/NOTIFY transport) + HMAC webhook sender with durable retry. |
| `internal/jobs` | Durable worker pool (`Registry`/`Queue`/`Pool`) on the jobs table. |
| `internal/ai` | RAG (**off by default**): extract→chunk→embed→index; search (vector/BM25/hybrid+RRF; pluggable `VectorIndex`/`LexicalIndex`/`ChunkSink`)→rerank→chat→agent; PII. |
| `internal/antivirus`, `internal/replication`, `internal/reconcile` | Event-driven workers: scan/quarantine, cross-region copy, orphan/lifecycle/retention GC. |
| `internal/telemetry` | OTel traces+metrics (OTLP), Prometheus `/metrics`. |
| `internal/cli` | HTTP client behind `aero-vault cli …` (upload/get/ls/rm/search/tag; snapshot). |
| `internal/webui` | Embedded static UI (`/ui`). |
| `sdk/{python,js,go}` | Client SDKs (`sdk/go` is a separate nested module). |

Layers, request flow, key scheme → `docs/architecture.md`.

## Invariants — do not break

1. **Cross-dialect SQL — the #1 footgun.** Queries use Postgres `$N` placeholders
   that `s.rebind` (`repository/sql.go`) rewrites to `?` for SQLite. SQLite `?` is
   **positional by count**, so **never reuse a `$N`** — give each bind its own
   number, even for the same value (`updated_at=$8, created_at=$9`; reusing `$8`
   passes on PG by name but breaks SQLite). Store/compare time as
   `time.RFC3339Nano`. (Memory `sqlite-placeholder-reuse`.)
2. **Migrations come in pairs.** Every schema change = matching
   `migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql`, applied on startup by
   `repo.Migrate`. Add the next `NNNN`; never edit or renumber an applied one.
   Verify both dialects.
3. **One physical key per object.** `storageKey(tenant,bucket,key)` → `path.Join`;
   tenants and buckets are *prefixes*, not separate backends. Keys are validated
   against traversal (empty / leading `/` / `..`); versioned objects keep one blob
   per version (`@v` suffix) and GC matches the exact `storage_key` — never parse
   it back. Never reach `Storage` from a handler bypassing `FileService`, nor
   weaken that validation. (Memory `versioned-storage-keys`.)
4. **Middleware order is load-bearing.** `RequestID → CORS → Auth → Tenant →
   RateLimit → OTel → Recoverer → AccessLog` (assembled in `main.go`). **Auth
   before Tenant** — a tenant-scoped key pins `X-Aero-Tenant`, which Tenant reads
   into context (default `default`; tenant `*` = operator key). Health, `/metrics`,
   `/openapi.json`, `/docs`, `/ui` bypass auth. Handlers don't self-apply the
   chain; isolated handler tests see no tenant/auth — by design, not a bug.
   (Memory `handlers-rely-on-global-middleware`.)
5. **Opt-in, safe-by-default.** Everything past core file-CRUD (AI, pgvector/pgFTS,
   Qdrant, event transport, cluster singletons, retention GC, persisted keys,
   WebDAV…) sits behind a config flag, default off, so the verified
   SQLite+local-FS path is unchanged. `embedder`/`llm`/`reranker` and the
   `search`/`chat`/`agent` handed to `rest.NewRouter` may be `nil` — guard for it;
   missing AI must never break plain uploads/downloads.
6. **Dependencies are deliberate.** Stdlib-first; justify any new `go.mod` module
   and `go mod tidy`. Unit tests use stdlib `testing` only — no frameworks.

## Testing

```go
repo, _ := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
_ = repo.Migrate(ctx)
store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
```

(see `rest/acl_test.go`, `repository/lifecycle_test.go`.) HTTP handlers via
`net/http/httptest`; chat/agent via `ai.MockLLM{}`; embeddings via the
deterministic `ai.HashEmbedder` (no network). New storage backends must pass
`storage.contract_test.go`. To exercise tenant/auth, wrap as `main.go` does
(`mw.Tenant(h)`) — don't self-wrap.

## Adding things — entry points

- **REST route** → handler in `rest`, register in `router.go`, set scope, add `*_test.go`, document in `openapi.json`.
- **Storage backend** → implement `storage.Storage`, extend `factory.go` + `BackendKind`, wire `config.go` + `main.go:buildStorageFrom`, pass the contract suite.
- **Table / metadata** → add the dual migration pair, then `Repository` methods in `sql.go` (mind invariant 1).
- **Background job** → job-type const + handler, `jobReg.Register(...)` in `main.go`, enqueue via the `Queue`.
- **MCP tool** → extend `listTools`/`callTool` in `mcp/server.go`.

## Configuration

Env-vars (+ optional `.env`); `.env.example` and `docs/configuration.md` are
exhaustive. Defaults: `local` storage under `./var/objects`, SQLite at
`./var/aero.db`, **no auth**, AI off, WebUI on, WebDAV off.
