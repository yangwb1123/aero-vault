# AGENTS.md

Contract for AI agents and contributors working **in** this repo. Terse by
design: how to verify, where things live, what you must not break. User-facing
overview is `README.md`; deep reference is `docs/`. Don't duplicate this there.

## What this is

`aero-vault` — one Go binary (`cmd/server`, Go 1.25, module
`github.com/aero-vault/aero-vault`) serving one object backend over four
protocols: REST (`/v1`), S3-compatible (`/s3`), WebDAV, MCP (`/mcp` + stdio),
plus opt-in RAG, multi-tenancy, and observability. **Everything funnels through
one `service.FileService`; protocol packages are thin adapters.** An object
written through any protocol is visible through all.

## Build · test · verify

Run before claiming any change is done — this *is* the CI gate
(`.github/workflows/ci.yml`), in order:

```bash
gofmt -l .         # MUST print nothing
go build ./...
go vet ./...
go test ./...      # SQLite + local FS only — no network, no Docker
```

`gofmt -w` everything you touch. `make build` → `bin/aero-vault`; `make run`.
Subcommands: `aero-vault mcp` (MCP stdio), `aero-vault cli …` (built-in client).

## Repository map — where to make a change

| Path | Responsibility |
|------|----------------|
| `cmd/server/main.go` | Entry point; the one place all subsystems are wired (config → storage → repo → service → workers → middleware → router). |
| `internal/config` | Env-var config (+ optional `.env`); zero-value-safe defaults. |
| `internal/service` | **`FileService`** — the object-CRUD core: quota, versioning, object-lock/WORM, tags, ACL, range, conditional, presign, events. Backend-agnostic. |
| `internal/storage` | `Storage` interface + `local`/`s3`/`oss`/`cos` backends, `factory.go`, envelope SSE, HMAC presign. `contract_test.go` is the shared backend suite. |
| `internal/repository` | `Repository` metadata layer. `sqlite.go` (default, pure-Go) + `postgres.go` share `sql.go`. Migrations under `migrations/{sqlite,postgres}`. |
| `internal/api/rest` | Primary JSON API (`/v1`): files, search, chat, agent, events/SSE, buckets, admin, ACL, thumbnails, OpenAPI/Swagger. |
| `internal/api/s3compat` | S3 gateway (`/s3`): objects, ListV2, multipart, tagging, ACL, copy, batch delete; SigV4 verify. |
| `internal/api/webdav` | WebDAV adapter — dispatched **outside** chi so `PROPFIND`/`MKCOL` work. |
| `internal/mcp` | MCP server (JSON-RPC over HTTP + stdio). Tools: `list_files`, `read_file`, `search`. |
| `internal/auth` | Scoped API keys, HS256 JWT, AWS SigV4 verify, anonymous public-read. |
| `internal/middleware` | RequestID, CORS, Tenant, RateLimiter, Recoverer, AccessLog. |
| `internal/events` | In-process event bus + HMAC-signed webhook sender with durable retry. |
| `internal/jobs` | Durable worker pool (`Registry`/`Queue`/`Pool`) backed by the jobs table. |
| `internal/ai` | RAG: extract → chunk → embed → index; search (vector/BM25/hybrid+RRF) → rerank → chat → agent; PII. **Off by default.** |
| `internal/antivirus`, `internal/replication`, `internal/reconcile` | Event-driven workers: scan/quarantine, cross-region copy, orphan sweep + lifecycle. |
| `internal/telemetry` | OTel traces+metrics (OTLP), Prometheus `/metrics`, per-request middleware. |
| `internal/webui` | Embedded static UI (`/ui`). |
| `sdk/{python,js,go}` | Client SDKs (`sdk/go` is a separate nested module). |

Layers, request flow, key scheme: `docs/architecture.md`.

## Conventions you must not break

**Cross-dialect SQL — the #1 footgun.** Queries use Postgres `$N` placeholders
run through `s.rebind(q)` (`internal/repository/sql.go`), which rewrites `$N → ?`
for SQLite. SQLite `?` is **positional by count**, so **never reuse a `$N`**:
`created_at=$8, updated_at=$8` works on PG (by name) but breaks SQLite ("missing
argument with index N"). Give each column its own placeholder and bind twice.
Store/compare timestamps as `time.RFC3339Nano` strings. (Memory `sqlite-placeholder-reuse`.)

**Migrations come in pairs.** Every schema change needs matching
`migrations/{sqlite,postgres}/NNNN_*.{up,down}.sql`, kept in lockstep; they apply
on startup via `repo.Migrate`. Latest is `0010_acl`. Verify on both dialects.

**One physical key per object.** `service.storageKey(tenant,bucket,key)` →
`path.Join(...)`; tenants and buckets are *prefixes*, not separate backends. Keys
are validated against traversal (empty / leading `/` / `..`). Never bypass
`FileService` to reach `Storage` from a handler, and never weaken that validation.

**Middleware order is load-bearing.** `RequestID → CORS → Auth → Tenant →
RateLimit → OTel → Recoverer → AccessLog` (assembled in `main.go`). **Auth before
Tenant** — a tenant-scoped key pins `X-Aero-Tenant`, which Tenant reads into
context. Health, `/metrics`, `/openapi.json`, `/docs`, `/ui` bypass auth.
Handlers do **not** self-apply this chain; isolated handler tests see no
tenant/auth — expected, not a bug. (Memory `handlers-rely-on-global-middleware`.)

**Multi-tenancy is header-driven.** `X-Aero-Tenant` (default `default`).
Tenant-scoped keys pin/enforce it; tenant `*` is an operator key. All metadata
and storage keys are tenant-scoped.

**AI is opt-in and nilable.** Everything in `internal/ai` is gated behind `AI_*`
config; `embedder`/`llm`/`reranker` and the `search`/`chat`/`agent` passed to
`rest.NewRouter` may be `nil`. Guard for nil — missing AI must never break plain
uploads/downloads.

**Dependencies are deliberate.** Stdlib-first; justify any new `go.mod` module
and run `go mod tidy`. Unit tests use stdlib `testing` only — no frameworks, no
network, no Docker.

## Testing conventions

Standard setup (see `internal/api/rest/acl_test.go`, `repository/lifecycle_test.go`):

```go
repo, _ := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
_ = repo.Migrate(ctx)
store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
```

HTTP handlers and HTTP-backed clients via `net/http/httptest`. Chat/agent use
`ai.MockLLM{}`; embeddings use deterministic `ai.HashEmbedder` (no network). New
storage backends must pass `storage.contract_test.go`. To test tenant/auth, wrap
the handler as `main.go` does (e.g. `mw.Tenant(h)`) — don't self-wrap the handler.

## Common changes — entry points

- **REST route** → handler in `internal/api/rest`, register in `router.go`, set
  scope, add `*_test.go`, update `openapi.go`.
- **Storage backend** → implement `storage.Storage`, extend `factory.go` +
  `BackendKind`, wire config in `config.go` + `main.go:buildStorageFrom`, pass
  the contract suite.
- **Table / metadata** → add the dual migration pair, then `Repository` methods
  via `sql.go`, respecting the placeholder rule.
- **Background job** → job-type const + handler, `jobReg.Register(...)` in
  `main.go`, enqueue through the `Queue`.
- **MCP tool** → extend `listTools`/`callTool` in `internal/mcp/server.go`.

## Configuration

All config is env-vars (+ optional `.env`); see `.env.example` and
`docs/configuration.md` (exhaustive). Defaults: `local` storage under
`./var/objects`, SQLite at `./var/aero.db`, **no auth**, AI off, WebUI on. Thread
new settings through `internal/config` — never hardcode.
