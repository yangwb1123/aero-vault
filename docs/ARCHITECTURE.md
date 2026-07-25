# Architecture — aero-vault

> See also: `docs/DEVELOPER_GUIDE.md` for workflow, `AGENTS.md` for engineering constraints.

## Overview

```
Protocol Layer (REST / S3 / WebDAV / MCP)
       ↓
Middleware Chain (RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog)
       ↓
FileService (internal/service) — single object-CRUD entry point
       ↓
Storage (internal/storage) — local★/s3/oss/cos
  + Repository (internal/repository) — SQLite★/Postgres
       ↓
EventBus (internal/events) — pub/sub + webhooks
  + JobPool (internal/jobs) — durable background workers
       ↓
AI Pipeline (internal/ai) — Extractor → Chunker → Embedder → Search/Chat/Agent
```

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Single binary | Easy deployment, no external dependencies for MVP |
| SQLite default | Zero configuration, embedded |
| Opt-in AI | AI pipeline disabled by default; no vector DB dependency |
| Storage-first writes | Storage blob committed before metadata row (orphan blobs handled by reconcile) |
| EventBus non-blocking | `Publish` never blocks the caller; dropped events are acceptable |

## Storage Key Scheme

```go
storageKey(tenant, bucket, key) = path.Join(tenant, bucket, key)
// Versioned: storageKey + "@v" + versionID
```

## Testing Layers

1. **Go unit tests** — `go test ./...` (23 packages)
2. **Python checks** — `python3 -m pytest checks/` (33 tests)
3. **E2E tests** — `make test-e2e` (31 tests against real server)
4. **Race detection** — `go test -race ./internal/...`
5. **Engineering gates** — `python3 cli.py accept`
