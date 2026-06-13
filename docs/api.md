# API reference

aero-vault exposes a JSON REST API under `/v1`, an S3-compatible gateway under
`/s3`, WebDAV, and MCP. This document covers the **REST API** (derived from
`internal/api/rest/openapi.json`, version `0.4.0`) and the **S3-compatibility
matrix**.

- **Base URL:** the server root (e.g. `http://localhost:8080`).
- **OpenAPI spec:** `GET /openapi.json`
- **Swagger UI:** `GET /docs`

## Authentication

Two REST security schemes are advertised (either works):

| Scheme | Header | Notes |
|--------|--------|-------|
| `bearer` | `Authorization: Bearer <token>` | API key **or** JWT. `ApiKey <token>` is also accepted. |
| `apiKey` | `X-Api-Key: <token>` | Convenience header (also used by S3 clients that can't sign). |

When `AUTH_KEYS` (and/or `AUTH_JWT_SECRET`) is unset, auth is **disabled** and all
requests pass through (MVP mode). When enabled, scopes are enforced by method:

- `read` — `GET`, `HEAD`, `OPTIONS` (and WebDAV `PROPFIND`/`PROPPATCH`)
- `write` — `POST`, `PUT`, `DELETE`
- `admin` — required for all `/v1/admin/*` routes (an `admin`-scoped key
  satisfies `read`/`write` too)

The active tenant is selected with the `X-Aero-Tenant` header (defaults to
`default`). A tenant-scoped key pins this header to the key's tenant.

## Errors

Errors use a consistent envelope:

```json
{ "error": { "code": "NotFound", "message": "object not found", "request_id": "…" } }
```

| HTTP | `code` | Cause |
|------|--------|-------|
| 400 | `InvalidArgument` | Bad request / malformed body. |
| 403 | `AccessDenied` / `Forbidden` | Missing scope, tenant mismatch, ACL denial. |
| 404 | `NotFound` / `NoSuchUpload` | Object or multipart upload not found. |
| 409 / 423 | object-lock codes | Object under retention lock. |
| 412 | `PreconditionFailed` | `If-Match` / `If-None-Match` failed. |
| 416 | `InvalidRange` | Range not satisfiable. |
| 507 | `QuotaExceeded` | Tenant byte/object quota exceeded. |
| 500 | `InternalError` | Unexpected error. |

In the examples below, `$BASE` is the server URL and `$KEY`/`$TOKEN` are
placeholders. Set `-H "Authorization: Bearer $TOKEN"` and
`-H "X-Aero-Tenant: acme"` as needed.

---

## Health & metrics (untagged)

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/healthz` | Liveness probe. |
| `GET` | `/readyz` | Readiness probe (pings the DB). |
| `GET` | `/metrics` | Prometheus metrics (when `PROMETHEUS_ENABLED=true`). |

```bash
curl -s "$BASE/healthz"     # {"ok":true}
curl -s "$BASE/readyz"      # {"ok":true} or 503
```

---

## `files`

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/v1/files` | List objects. |
| `POST` | `/v1/files` | Multipart-form upload. |
| `GET` | `/v1/files/{key}` | Download (`?version=ID` for history). |
| `PUT` | `/v1/files/{key}` | Raw upload. |
| `HEAD` | `/v1/files/{key}` | Stat. |
| `DELETE` | `/v1/files/{key}` | Delete (`?hard=1` for physical wipe). |
| `POST` | `/v1/files/{key}/presign` | Presigned URL. |
| `POST` | `/v1/files/{key}/lock` | Apply object lock (retention). |
| `GET` | `/v1/files/{key}/tags` | Get tags. |
| `PUT` | `/v1/files/{key}/tags` | Replace tags. |
| `DELETE` | `/v1/files/{key}/tags` | Clear tags. |
| `GET` | `/v1/files/{key}/versions` | List versions. |
| `GET` | `/v1/files/{key}/acl` | Get object ACL. |
| `PUT` | `/v1/files/{key}/acl` | Set canned ACL. |
| `GET` | `/v1/files/{key}/thumbnail` | On-demand JPEG thumbnail of an image (`?w=&h=`). |
| `POST` | `/v1/multipart` | Init multipart upload. |
| `PUT` | `/v1/multipart/{uploadID}/parts/{n}` | Upload part. |
| `POST` | `/v1/multipart/{uploadID}/complete` | Complete. |
| `DELETE` | `/v1/multipart/{uploadID}` | Abort. |

### Raw upload / download

```bash
# Upload (raw body). User metadata via X-Amz-Meta-* or X-Meta-* headers.
curl -s -X PUT --data-binary @report.pdf \
  -H 'Content-Type: application/pdf' \
  -H 'X-Meta-team: research' \
  "$BASE/v1/files/docs/report.pdf"
# -> 201 { "bucket":"default","key":"docs/report.pdf","size":…,"etag":"…", … }

# Download
curl -s "$BASE/v1/files/docs/report.pdf" -o report.pdf

# Conditional GET (304 when ETag matches) and Range (206 Partial Content)
curl -s -H 'If-None-Match: "<etag>"' "$BASE/v1/files/docs/report.pdf" -D -
curl -s -H 'Range: bytes=0-1023'      "$BASE/v1/files/docs/report.pdf" -D -

# Optimistic concurrency on write
curl -s -X PUT --data-binary @v2.pdf -H 'If-Match: "<etag>"'        "$BASE/v1/files/docs/report.pdf"
curl -s -X PUT --data-binary @new.pdf -H 'If-None-Match: *'         "$BASE/v1/files/docs/new.pdf"   # create-only
```

### Multipart-form upload

```bash
curl -s -X POST \
  -F 'file=@report.pdf' \
  -F 'key=docs/report.pdf' \
  -F 'metadata={"team":"research"}' \
  "$BASE/v1/files"
# -> 201 objectDTO  (key defaults to the uploaded filename if omitted)
```

### List, stat, delete

```bash
curl -s "$BASE/v1/files?prefix=docs/&limit=100&marker="   # paginated list
curl -s -I "$BASE/v1/files/docs/report.pdf"               # HEAD: stat headers
curl -s -X DELETE "$BASE/v1/files/docs/report.pdf"        # soft delete (204)
curl -s -X DELETE "$BASE/v1/files/docs/report.pdf?hard=1" # physical delete
```

List response:

```json
{ "objects": [ { "bucket":"default","key":"docs/report.pdf","size":12345,"etag":"…", "content_type":"application/pdf","backend":"local","created_at":"…","updated_at":"…" } ],
  "next_marker": "", "has_more": false }
```

### Presigned URLs

```bash
# op = get | put ; expires in seconds (default 300)
curl -s -X POST "$BASE/v1/files/docs/report.pdf/presign?op=get&expires=900"
# -> { "url": "https://…", "expires": "2026-05-24T12:34:56Z" }
```

### Tags

```bash
curl -s -X PUT  -d '{"team":"research","status":"final"}' "$BASE/v1/files/docs/report.pdf/tags"
curl -s          "$BASE/v1/files/docs/report.pdf/tags"      # { "tags": { … } }
curl -s -X DELETE "$BASE/v1/files/docs/report.pdf/tags"     # clear
```

### Versions

```bash
curl -s "$BASE/v1/files/docs/report.pdf/versions"          # list historical versions
curl -s "$BASE/v1/files/docs/report.pdf?version=<id>" -o old.pdf   # fetch a version
```

### Object lock (WORM)

```bash
# Retain (cannot delete/overwrite) for N seconds from now.
curl -s -X POST -d '{"seconds":3600}' "$BASE/v1/files/docs/report.pdf/lock"
# -> { "locked_until": "2026-05-24T13:34:56Z" }
```

### ACL

Canned ACLs: `private`, `public-read`, `public-read-write`, `authenticated-read`.

```bash
curl -s -X PUT -H 'x-amz-acl: public-read' "$BASE/v1/files/docs/report.pdf/acl"
curl -s         "$BASE/v1/files/docs/report.pdf/acl"
```

### Thumbnails

```bash
curl -s "$BASE/v1/files/images/photo.jpg/thumbnail?w=200&h=200" -o thumb.jpg
```

### Multipart upload (REST)

```bash
# 1. init
UID=$(curl -s -X POST -d '{"key":"big.bin","content_type":"application/octet-stream"}' \
  "$BASE/v1/multipart" | python3 -c 'import sys,json;print(json.load(sys.stdin)["upload_id"])')
# 2. upload parts (n >= 1)
curl -s -X PUT --data-binary @part1 "$BASE/v1/multipart/$UID/parts/1"
curl -s -X PUT --data-binary @part2 "$BASE/v1/multipart/$UID/parts/2"
# 3. complete (server-persisted parts are authoritative)
curl -s -X POST "$BASE/v1/multipart/$UID/complete"
# abort instead:
curl -s -X DELETE "$BASE/v1/multipart/$UID"
```

---

## `search`

| Method | Path | Summary |
|--------|------|---------|
| `POST` | `/v1/search` | Semantic + hybrid search. |

Requires an embedder (`AI_INDEX_ENABLED=true`); otherwise returns `503`.

```bash
curl -s -X POST "$BASE/v1/search" \
  -H 'Content-Type: application/json' \
  -d '{"query":"vector database","k":5,"mode":"hybrid","bucket":""}'
```

Request fields: `query` (required), `k` (default 10), `mode`
(`vector` | `bm25` | `hybrid`), `bucket` (optional). Response:

```json
{ "query": "vector database",
  "hits": [ { "score": 0.83, "object_key": "docs/intro.md", "bucket": "default", "seq": 2, "chunk": "…" } ] }
```

---

## `chat`

| Method | Path | Summary |
|--------|------|---------|
| `POST` | `/v1/chat` | RAG chat with citations. |
| `POST` | `/v1/chat/stream` | Streaming RAG chat (SSE). |

Requires a chat LLM (`AI_CHAT_PROVIDER=http` or `mock`); otherwise `503`.

```bash
# Non-streaming
curl -s -X POST "$BASE/v1/chat" \
  -H 'Content-Type: application/json' \
  -d '{"query":"what is in the docs?","k":4,"mode":"hybrid","temperature":0.2}'
# -> { "answer": "…", "citations": [ { "object_key": "…", … } ] }
```

Request fields: `query` (required), `k`, `mode`, `temperature`, and `prior`
(prior chat messages for multi-turn).

```bash
# Streaming (Server-Sent Events): event: token frames, then event: done
curl -s -N -X POST "$BASE/v1/chat/stream" \
  -H 'Content-Type: application/json' \
  -d '{"query":"summarize everything","k":4,"mode":"hybrid"}'
```

SSE events: `token` (each chunk is a JSON-encoded string), `done` (final answer +
citations), `error`.

---

## `agent`

| Method | Path | Summary |
|--------|------|---------|
| `POST` | `/v1/agent` | Tool-calling agent loop. |
| `GET` | `/v1/lineage/objects/{id}` | AI consumption history. |
| `POST` | `/mcp` | JSON-RPC MCP server. |

```bash
# Agent (requires an LLM)
curl -s -X POST "$BASE/v1/agent" \
  -H 'Content-Type: application/json' \
  -d '{"query":"find the largest file and tell me its name"}'
# -> { "answer": "…", … }

# Lineage: which AI calls consumed object #42
curl -s "$BASE/v1/lineage/objects/42?limit=20"
```

### MCP over HTTP (JSON-RPC 2.0)

The MCP server exposes tools `list_files`, `read_file`, and `search` (the last
only when an embedder is configured) plus object resources.

```bash
# List available tools
curl -s -X POST "$BASE/mcp" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# Call a tool
curl -s -X POST "$BASE/mcp" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call",
       "params":{"name":"search","arguments":{"query":"intro","k":5}}}'

# List / read resources (aero-vault://{tenant}/{bucket}/{key})
curl -s -X POST "$BASE/mcp" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"resources/list"}'
```

MCP is also available over stdio for local agent integrations: `aero-vault mcp`.

---

## `events`

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/v1/events/stream` | SSE lifecycle stream. |

```bash
curl -s -N "$BASE/v1/events/stream"   # streams object-lifecycle events as SSE
```

Lifecycle events are also delivered to an HTTP webhook when `EVENTS_WEBHOOK_URL`
is set (HMAC-SHA256 signed via `X-Aero-Signature` when `EVENTS_WEBHOOK_SECRET` is
set).

---

## `buckets`

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/v1/buckets/{bucket}/config` | Get config. |
| `PUT` | `/v1/buckets/{bucket}/versioning` | Toggle versioning. |
| `PUT` | `/v1/buckets/{bucket}/object-lock` | Default retention (seconds). |
| `PUT` | `/v1/buckets/{bucket}/lifecycle` | Expire-after-days policy. |
| `GET` | `/v1/buckets/{bucket}/acl` | Get bucket ACL. |
| `PUT` | `/v1/buckets/{bucket}/acl` | Set bucket canned ACL. |

```bash
curl -s "$BASE/v1/buckets/default/config"
# -> { "name":"default","versioning":false,"object_lock_seconds":0, "expire_after_days":0,"expire_action":"" }

curl -s -X PUT -d '{"enabled":true}'                       "$BASE/v1/buckets/default/versioning"
curl -s -X PUT -d '{"seconds":3600}'                       "$BASE/v1/buckets/default/object-lock"
curl -s -X PUT -d '{"days":30,"action":"soft_delete"}'     "$BASE/v1/buckets/default/lifecycle"
curl -s -X PUT -H 'x-amz-acl: private'                     "$BASE/v1/buckets/default/acl"
curl -s         "$BASE/v1/buckets/default/acl"
```

---

## `admin`

All `/v1/admin/*` routes require the `admin` scope (when auth is enabled).

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/v1/usage` | Current tenant's quota & usage. |
| `PUT` | `/v1/admin/tenants/{tenant}/quota` | Set quota. |
| `PUT` | `/v1/admin/tenants/{tenant}/budget` | Set per-tenant daily AI spend cap (USD). |
| `POST` | `/v1/admin/tenants` | Create or upsert a tenant record. |
| `GET` | `/v1/admin/tenants` | List all tenants. |
| `DELETE` | `/v1/admin/tenants/{tenant}` | Delete a tenant record. |
| `PUT` | `/v1/admin/tenants/{tenant}/status` | Set tenant status (`active`\|`disabled`). |
| `GET` | `/v1/admin/audit` | List audit-log entries (admin actions). |
| `GET` | `/v1/admin/keys` | List API keys (tokens redacted). |
| `POST` | `/v1/admin/keys` | Add key. |
| `DELETE` | `/v1/admin/keys/{token}` | Revoke key. |
| `POST` | `/v1/admin/jwt` | Issue JWT. |
| `GET` | `/v1/admin/webhook-failures` | List undelivered webhooks. |
| `GET` | `/v1/admin/jobs` | List background jobs + status histogram. |
| `POST` | `/v1/admin/jobs/{id}/retry` | Requeue a job. |

```bash
# Self-serve usage (any authenticated tenant sees its own row)
curl -s "$BASE/v1/usage"
# -> { "tenant":"acme","used_bytes":…,"used_objects":…,"max_bytes":…,"max_objects":…,"updated_at":"…" }

# Set a quota (admin)
curl -s -X PUT -d '{"max_bytes":10737418240,"max_objects":100000}' \
  "$BASE/v1/admin/tenants/acme/quota"

# API key lifecycle (admin)
curl -s -X POST -d '{"token":"prod-rw","tenant":"acme","scopes":["read","write"]}' "$BASE/v1/admin/keys"
curl -s          "$BASE/v1/admin/keys"
curl -s -X DELETE "$BASE/v1/admin/keys/prod-rw"

# Issue a JWT (requires AUTH_JWT_SECRET)
curl -s -X POST -d '{"sub":"svc-a","tenant":"acme","scopes":["read"],"ttl_seconds":3600}' \
  "$BASE/v1/admin/jwt"
# -> { "token":"<jwt>","tenant":"acme","scopes":["read"],"ttl_seconds":3600 }

# Operations
curl -s "$BASE/v1/admin/webhook-failures?limit=50"
curl -s "$BASE/v1/admin/jobs?limit=20"
curl -s -X POST "$BASE/v1/admin/jobs/123/retry"
```

### Tenant management

Tenants created here are persisted in the DB and survive restarts. Omitting `display_name` is fine.

```bash
# Create or upsert a tenant
curl -s -X POST -d '{"tenant_id":"acme","display_name":"Acme Corp"}' \
  "$BASE/v1/admin/tenants"
# -> { "tenant_id":"acme","display_name":"Acme Corp","status":"active","created_at":"…" }

# List all tenants
curl -s "$BASE/v1/admin/tenants"
# -> { "tenants": [ { "tenant_id":"acme", … } ] }

# Disable / re-enable a tenant (status: "active" | "disabled")
curl -s -X PUT -d '{"status":"disabled"}' "$BASE/v1/admin/tenants/acme/status"
curl -s -X PUT -d '{"status":"active"}'   "$BASE/v1/admin/tenants/acme/status"

# Delete a tenant record (204; 404 if not found)
curl -s -X DELETE "$BASE/v1/admin/tenants/acme"

# Set per-tenant daily AI budget (0 clears the override)
curl -s -X PUT -d '{"daily_budget_usd":5.00}' "$BASE/v1/admin/tenants/acme/budget"
```

### Audit log

Admin and security actions are recorded automatically: key add/revoke, tenant create/delete/status, quota/budget set.

```bash
# Most recent 50 entries (default 100)
curl -s "$BASE/v1/admin/audit?limit=50"
# -> { "audit": [ { "actor":"ops","action":"key.add","target":"…","detail":"…","created_at":"…" } ] }
```

---

# S3-compatibility

The S3 gateway is mounted at `S3_COMPAT_PREFIX` (default `/s3`) and implements a
practical subset of the S3 REST API in **path-style** form
(`/{bucket}/{key+}`). It shares the same `FileService`, so S3 and REST see the
same objects.

## Supported operations

| S3 operation | HTTP | Supported | Notes |
|--------------|------|-----------|-------|
| PutObject | `PUT /{bucket}/{key}` | ✅ | User metadata via `x-amz-meta-*`; canned ACL via `x-amz-acl`. |
| GetObject | `GET /{bucket}/{key}` | ✅ | Honors `Range` → `206`; returns `x-amz-meta-*`. |
| HeadObject | `HEAD /{bucket}/{key}` | ✅ | |
| DeleteObject | `DELETE /{bucket}/{key}` | ✅ | Physical delete. |
| CopyObject | `PUT` + `x-amz-copy-source` | ✅ | `x-amz-metadata-directive: COPY` (default) or `REPLACE`. |
| ListObjectsV2 | `GET /{bucket}?list-type=2` | ✅ | `prefix`, `continuation-token`, `max-keys`, `start-after`. |
| HeadBucket | `HEAD /{bucket}` | ✅ | |
| CreateBucket | `PUT /{bucket}` | ✅ | Registers the bucket; canned ACL via `x-amz-acl`. |
| DeleteObjects (batch) | `POST /{bucket}?delete` | ✅ | XML body; supports quiet mode. |
| GetObjectTagging | `GET /{bucket}/{key}?tagging` | ✅ | |
| PutObjectTagging | `PUT /{bucket}/{key}?tagging` | ✅ | |
| DeleteObjectTagging | `DELETE /{bucket}/{key}?tagging` | ✅ | |
| GetObjectAcl | `GET /{bucket}/{key}?acl` | ✅ | Canned-ACL → policy view. |
| PutObjectAcl | `PUT /{bucket}/{key}?acl` | ✅ | Canned ACL via `x-amz-acl` header. |
| CreateMultipartUpload | `POST /{bucket}/{key}?uploads` | ✅ | |
| UploadPart | `PUT /{bucket}/{key}?uploadId=&partNumber=` | ✅ | |
| CompleteMultipartUpload | `POST /{bucket}/{key}?uploadId=` | ✅ | Server-persisted parts are authoritative. |
| AbortMultipartUpload | `DELETE /{bucket}/{key}?uploadId=` | ✅ | |
| ListParts | `GET /{bucket}/{key}?uploadId=` | ✅ | |
| ListMultipartUploads | `GET /{bucket}?uploads` | ✅ | |
| ListObjects (v1) | `GET /{bucket}` (no `list-type=2`) | ✅ | `prefix`, `marker`, `max-keys`; `NextMarker` when truncated. |
| Versioning / lock / lifecycle sub-resources | `GET`/`PUT /{bucket}?versioning`, `?object-lock`, `?lifecycle` | ✅ | XML config round-trips; `GET ?lifecycle` is `404 NoSuchLifecycleConfiguration` when unset. |
| DeleteBucketLifecycle | `DELETE /{bucket}?lifecycle` | ✅ | Clears the expiry policy; `204 No Content`. |
| GetBucketAcl / PutBucketAcl | `GET`/`PUT /{bucket}?acl` | ✅ | Canned ACL ↔ `AccessControlPolicy`; PUT via `x-amz-acl` header or policy body. |
| GetBucketLocation | `GET /{bucket}?location` | ✅ | Empty `LocationConstraint` (us-east-1); `404 NoSuchBucket` if absent. |
| ListObjectVersions | `GET /{bucket}?versions` | ✅ | Honors `?prefix`; every stored version is a `<Version>` (newest `IsLatest=true`); paginated by key via `?max-keys`/`?key-marker` (`NextKeyMarker` when truncated). |
| Presigned URLs | — | ✅ (REST) | Use `POST /v1/files/{key}/presign`. |

## SigV4 usage

The S3 endpoint supports **AWS Signature Version 4**. Verification is *additive*:
only requests presenting SigV4 (an `AWS4-HMAC-SHA256` `Authorization` header or a
presigned `X-Amz-Signature` query) are checked against `S3_SIGV4_CREDENTIALS`;
bearer / no-auth flows are untouched.

Configure credentials as
`accessKey:secretKey:tenant[:scope+scope]` (comma-separated):

```bash
S3_SIGV4_CREDENTIALS="AKIAEXAMPLE:secretEXAMPLE:acme:read+write"
```

A SigV4 request resolves to that tenant (the gateway sets `X-Aero-Tenant`
accordingly unless the credential's tenant is `*`) and is scope-checked by method
(`GET`/`HEAD` → `read`, otherwise `write`). Both header-signed and presigned-URL
forms are supported, including streaming (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`)
bodies.

### Example: AWS CLI against the S3 gateway

```bash
export AWS_ACCESS_KEY_ID=AKIAEXAMPLE
export AWS_SECRET_ACCESS_KEY=secretEXAMPLE
export AWS_DEFAULT_REGION=us-east-1

# Endpoint = server root + S3_COMPAT_PREFIX. Path-style is required.
aws --endpoint-url http://localhost:8080/s3 s3api put-object \
  --bucket docs --key report.pdf --body report.pdf
aws --endpoint-url http://localhost:8080/s3 s3api list-objects-v2 --bucket docs
aws --endpoint-url http://localhost:8080/s3 s3 cp s3://docs/report.pdf ./out.pdf
```

If you have not configured SigV4 credentials but auth is enabled, S3 clients can
instead pass the `X-Api-Key` header (the gateway accepts it as a fallback).
