# AGENTS.md

Contract for agents/contributors working **in** this repo: how to **verify**,
**where** things live, what you must **not break**. Overview → `README.md`;
deep reference → `docs/`.

## What this is

`aero-vault` — one Go binary (`cmd/server`, Go 1.25, module
`github.com/aero-vault/aero-vault`) serving one object backend over four
protocols: REST (`/v1`), S3-compatible (`/s3`), MCP (`/mcp` + stdio), and WebDAV
(opt-in, off until `WEBDAV_PREFIX` set), plus opt-in RAG, multi-tenancy, and
observability. **Everything funnels through one `service.FileService`; protocol
packages are thin adapters — an object written via any protocol is visible
through all.**

## Build · test · verify

Run before claiming a change is done — this *is* the CI gate
(`.github/workflows/ci.yml`), in order:

```bash
gofmt -l .         # MUST print nothing — gofmt -w whatever you touch
go build ./...
go vet ./...
go test ./...      # SQLite + local FS only: no network, no Docker
```

Postgres paths (pgvector/pgFTS/LISTEN-NOTIFY) are checked by `make
test-integration` (Docker), outside the gate. `make build` → `bin/aero-vault`;
`make run`. Subcommands: `aero-vault mcp` (stdio), `aero-vault cli …` (client).

## Repository map — where to make a change

| Path | Responsibility |
|------|----------------|
| `cmd/server/main.go` | Entry point; the one place subsystems are wired (config → storage → repo → service → workers → middleware → router). |
| `internal/config` | Env-var config (+ optional `.env`); zero-value-safe defaults. |
| `internal/service` | **`FileService`** — object-CRUD core: quota, versioning, object-lock/WORM, tags, ACL, range, conditional, presign, events. Backend-agnostic. |
| `internal/storage` | `Storage` interface + `local`/`s3`/`oss`/`cos` backends, `factory.go`, envelope SSE, HMAC presign. `contract_test.go` = shared backend suite. |
| `internal/repository` | `Repository` metadata. `sqlite.go` (default, pure-Go) + `postgres.go` share `sql.go`. Migrations in `internal/repository/migrations/{sqlite,postgres}`. |
| `internal/api/rest` | JSON API (`/v1`): files, search, chat, agent, events/SSE, buckets, admin, ACL, thumbnails, OpenAPI/Swagger. |
| `internal/api/s3compat` | S3 gateway (`/s3`): objects, ListV2, multipart, tagging, ACL, copy, batch delete; SigV4 verify. |
| `internal/api/webdav` | WebDAV adapter — dispatched **outside** chi so `PROPFIND`/`MKCOL` work. |
| `internal/mcp` | MCP server (JSON-RPC over HTTP + stdio). Tools: `list_files`, `read_file`, `search` (search only when AI is on). |
| `internal/auth` | Scoped API keys, HS256 JWT, AWS SigV4 verify, anonymous public-read. |
| `internal/middleware` | RequestID, CORS, Tenant, RateLimiter, Recoverer, AccessLog. |
| `internal/events` | In-process event bus (+ opt-in Postgres LISTEN/NOTIFY transport) + HMAC-signed webhook sender with durable retry. |
| `internal/jobs` | Durable worker pool (`Registry`/`Queue`/`Pool`) backed by the jobs table. |
| `internal/ai` | RAG: extract → chunk → embed → index; search (vector/BM25/hybrid+RRF) → rerank → chat → agent; PII. **Off by default.** |
| `internal/antivirus`, `internal/replication`, `internal/reconcile` | Event-driven workers: scan/quarantine, cross-region copy, orphan sweep + lifecycle + retention GC. |
| `internal/telemetry` | OTel traces+metrics (OTLP), Prometheus `/metrics`, per-request middleware. |
| `internal/cli` | Built-in HTTP client behind `aero-vault cli …` (upload/get/ls/rm/search/tag; snapshot backup). |
| `internal/webui` | Embedded static UI (`/ui`). |
| `sdk/{python,js,go}` | Client SDKs (`sdk/go` is a separate nested module). |

Layers, request flow, key scheme: `docs/architecture.md`.

## Invariants you must not break

**Cross-dialect SQL — the #1 footgun.** Queries use Postgres `$N` placeholders run
through `s.rebind(q)` (`repository/sql.go`), which rewrites `$N → ?` for SQLite.
SQLite `?` is **positional by count**, so **never reuse a `$N`** — give each column
its own placeholder and bind twice (`created_at=$8, updated_at=$9`; reusing `$8`
works on PG by name but breaks SQLite). Store/compare timestamps as
`time.RFC3339Nano` strings. (Memory `sqlite-placeholder-reuse`.)

**Migrations come in pairs.** Each schema change needs matching
`internal/repository/migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql` in
lockstep; applied on startup via `repo.Migrate`. Latest is `0016_audit_log`.
Verify on both dialects.

**One physical key per object.** `storageKey(tenant,bucket,key)` (unexported func
in `service`) → `path.Join(...)`; tenants and buckets are *prefixes*, not separate
backends. Keys are validated against traversal (empty / leading `/` / `..`).
Versioned objects keep one blob per version (`@v` suffix); storage GC must match
the exact `storage_key` — never parse it back. Never bypass `FileService` to reach
`Storage` from a handler, nor weaken that validation. (Memory `versioned-storage-keys`.)

**Middleware order is load-bearing.** `RequestID → CORS → Auth → Tenant →
RateLimit → OTel → Recoverer → AccessLog` (assembled in `main.go`). **Auth before
Tenant** — a tenant-scoped key pins `X-Aero-Tenant`, which Tenant reads into
context (default `default`; tenant `*` = operator key). Health, `/metrics`,
`/openapi.json`, `/docs`, `/ui` bypass auth. Handlers do **not** self-apply this
chain; isolated handler tests see no tenant/auth — expected, not a bug. (Memory
`handlers-rely-on-global-middleware`.)

**Opt-in and safe-by-default.** Everything past core file-CRUD (AI, pgvector/pgFTS,
event transport, cluster-singleton, retention GC, persisted keys, WebDAV…) sits
behind a config flag, default off, so the verified SQLite + local-FS path is
unchanged. `embedder`/`llm`/`reranker` and the `search`/`chat`/`agent` passed to
`rest.NewRouter` may be `nil` — guard for nil; missing AI must never break plain
uploads/downloads.

**Dependencies are deliberate.** Stdlib-first; justify any new `go.mod` module and
`go mod tidy`. Unit tests use stdlib `testing` only — no frameworks.

## Testing

Standard setup (see `rest/acl_test.go`, `repository/lifecycle_test.go`):

```go
repo, _ := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
_ = repo.Migrate(ctx)
store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
```

HTTP handlers via `net/http/httptest`. Chat/agent use `ai.MockLLM{}`; embeddings
use deterministic `ai.HashEmbedder` (no network). New storage backends must pass
`storage.contract_test.go`. To exercise tenant/auth, wrap as `main.go` does
(`mw.Tenant(h)`) — don't self-wrap.

## Common changes — entry points

- **REST route** → handler in `rest`, register in `router.go`, set scope, add `*_test.go`, document in `openapi.json`.
- **Storage backend** → implement `storage.Storage`, extend `factory.go` + `BackendKind`, wire `config.go` + `main.go:buildStorageFrom`, pass the contract suite.
- **Table / metadata** → add the dual migration pair, then `Repository` methods in `sql.go` (mind the placeholder rule).
- **Background job** → job-type const + handler, `jobReg.Register(...)` in `main.go`, enqueue via the `Queue`.
- **MCP tool** → extend `listTools`/`callTool` in `mcp/server.go`.

## Configuration

Env-vars (+ optional `.env`); see `.env.example` and `docs/configuration.md`
(exhaustive). Defaults: `local` storage under `./var/objects`, SQLite at
`./var/aero.db`, **no auth**, AI off, WebUI on, WebDAV off. Thread new settings
through `internal/config` — never hardcode.
