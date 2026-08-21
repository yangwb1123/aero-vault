# API reference

aero-vault exposes a JSON REST API under `/v1`, an optional S3-compatible
gateway, WebDAV, and MCP. This document covers the **REST API** (derived from
the runtime route table in `internal/api/rest/router.go` and served at
`/openapi.json`) and the **S3-compatibility matrix**.

- **Base URL:** the server root (e.g. `http://localhost:8080`).
- **OpenAPI spec:** `GET /openapi.json`
- **Swagger UI:** `GET /docs`

## Authentication

Two REST security schemes are advertised (either works):

| Scheme | Header | Notes |
|--------|--------|-------|
| `bearer` | `Authorization: Bearer <token>` | API key **or** JWT. `ApiKey <token>` is also accepted. |
| `apiKey` | `X-Api-Key: <token>` | Convenience header (also used by S3 clients that can't sign). |

When all API-key/JWT/JWKS credential sources are unset, auth is **disabled** and all
requests pass through (MVP mode). When enabled, scopes are enforced by method:

- `read` — `GET`, `HEAD`, `OPTIONS` (and WebDAV `PROPFIND`/`PROPPATCH`)
- `write` — `POST`, `PUT`, `DELETE`
- `admin` — required for all `/v1/admin/*` routes (an `admin`-scoped key
  satisfies `read`/`write` too)

The active tenant is selected with the `X-Aero-Tenant` header (defaults to
`default`). A tenant-scoped key pins this header to the key's tenant.

### Browser OIDC login

When `AUTH_OIDC_*` is configured, the embedded UI exposes a provider login:

| Method | Path | Summary |
|--------|------|---------|
| `GET` | `/auth/oidc/login` | Start Authorization Code + PKCE. |
| `GET` | `/auth/oidc/callback` | Validate state and exchange the code. |
| `GET` | `/auth/oidc/logout` | Clear the UI token and end the provider session. |

The callback returns tokens to `/ui` in the URL fragment, which the UI removes
from browser history before making API requests. Snaplink's SDK owns one-time
state, the HttpOnly cookie, authorization redirect, PKCE, code exchange, and
access-token verification with issuer and client/audience pinning; Aero only
maps the successful result to its UI and Principal.

## Errors

Errors use a consistent envelope:

```json
{ "error": { "code": "NotFound", "message": "object not found", "request_id": "…" } }
```

| HTTP | `code` | Cause |
|------|--------|-------|
| 400 | `InvalidArgument` | Bad request / malformed body. |
| 403 | `AccessDenied` / `Forbidden` | Missing scope, tenant mismatch, ACL denial. Authorization denials carry the raw denial reason in `message` (e.g. `forbidden: default_deny`). |
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

PUT_URL=$(curl -s -X POST \
  "$BASE/v1/files/docs/direct.txt/presign?op=put&expires=900" | jq -r .url)
curl -X PUT --data-binary @direct.txt "$PUT_URL"
```

REST presigned GET and PUT URLs terminate at Aero Vault's object routes rather
than at the raw storage backend. GET supports `GET`, `HEAD`, conditional and
Range requests while remaining subject to the current tenant status, bucket
policy, explicit ACL denies, and object lifecycle; disabling a tenant or
deleting the object therefore invalidates an already-issued URL immediately.
PUT continues through quota, versioning, object lock, integrity, event, and
indexing checks. The signature binds the operation, tenant, object path, and
expiry; extra query parameters are rejected. In a multi-replica deployment,
configure the same `AUTH_PRESIGN_SECRET` (at least 32 bytes) on every replica.

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

## Enterprise permissions, sharing, image publishing, and backup

Set `ACCESS_CONTROL_ENABLED=true` to activate this surface. Every authenticated
caller is normalized to `{subject, tenant, roles, groups, scopes}`. Snaplink can
provide identity and application scopes; Aero Vault independently owns
resource-level decisions so the same ACL applies to REST, S3, WebDAV, MCP, and
AI search/chat results.

Authorization order is fail-closed: explicit deny → explicit allow → exact
share/public capability → object owner → tenant/file administrator → optional
tenant scope fallback. A folder ACL with `inherit=true` covers descendants, and
membership in a child department also inherits parent-department grants.

Supported actions are `object:list`, `object:read`, `object:preview`,
`object:download`, `object:create`, `object:write`, `object:delete`,
`object:restore`, `object:share`, `object:manage_acl`, `asset:publish`,
`object:export`, and `*`.

For server-to-server integrations, provision a separate persisted key instead
of copying a browser OIDC token. The full-stack deployment keeps its bootstrap
operator token in `/var/lib/aero-vault/operator-token` (mode 0600):

```bash
OPERATOR_TOKEN=$(sudo cat /var/lib/aero-vault/operator-token)
curl -s -X POST "$BASE/v1/admin/keys" \
  -H "Authorization: Bearer $OPERATOR_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"token":"replace-with-a-random-project-secret",\
       "tenant":"default","scopes":["read","write"],\
       "label":"blog-production"}'
```

Only a SHA-256 hash of a persisted project key is stored. Give each project a
different key so it can be audited and revoked without affecting browser SSO or
other applications.

### Departments and resource ACLs

```bash
# Operator: create a department and add a Snaplink/Aero subject.
DEPT=$(curl -s -X POST "$BASE/v1/admin/departments" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"engineering"}' | jq -r .id)
curl -s -X PUT "$BASE/v1/admin/departments/$DEPT/members/user-42" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"role":"member"}'

# Owner/admin: make engineering/ readable by the department, recursively.
curl -s -X PUT "$BASE/v1/access/acl" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"key\":\"engineering/\",\"resource_kind\":\"folder\",\
       \"principal_type\":\"department\",\"principal_id\":\"$DEPT\",\
       \"actions\":[\"object:read\",\"object:download\"],\
       \"effect\":\"allow\",\"inherit\":true}"
```

ACL principals may be `user`, `department`, `group`, `role`, `authenticated`,
or `everyone`. `GET /v1/access/check?...&action=object:read` explains the current
caller's decision. Canned S3 ACLs remain available for compatibility; resource
ACLs are the enterprise authorization model.

### Revocable sharing

```bash
SHARE=$(curl -s -X POST "$BASE/v1/shares" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"key":"images/photo.jpg","password":"review-only",\
       "allow_preview":true,"allow_download":true,\
       "ttl_seconds":3600,"max_uses":20}')
SHARE_URL=$(printf '%s' "$SHARE" | jq -r .url)

# Password header is preferred; query passwords may appear in logs/history.
curl -H 'X-Aero-Share-Password: review-only' "$SHARE_URL" -o photo.jpg
```

Only the creation response contains the raw token. The database stores its
SHA-256 hash. Links can be expired, use-limited, password-protected, and revoked
with `DELETE /v1/shares/{id}`. `HEAD` does not consume a use; each successful
`GET` does. Invalid ranges and conditional `304` responses do not consume a
use. Deleting the source object revokes all of its share links before the key
can be reused, so an old capability can never expose a newly uploaded object.
Deletion also removes exact-object ACLs; inherited folder and bucket policies
continue to apply when the key is restored or uploaded again.

### Stable public image URLs for blogs

```bash
curl -s -X PUT "$BASE/v1/files/blog/hero.jpg" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: image/jpeg' \
  --data-binary @hero.jpg

curl -s -X POST "$BASE/v1/assets" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"key":"blog/hero.jpg","slug":"blog/hero.jpg",\
       "cache_control":"public, max-age=86400"}'
# -> {"asset":{...},"url":"https://source.ywbsd.site/public/assets/blog/hero.jpg"}
```

Only `image/*` objects may be published. Public endpoints support `GET`,
`HEAD`, ETag/304, byte ranges/206, `nosniff`, and caller-selected cache control.
Unpublishing removes the stable URL without deleting the protected source file.
Deleting the source automatically unpublishes it; restoring or re-uploading the
key requires an explicit publish operation.

Other projects can upload and publish with the SDKs:

```python
av.upload("blog/hero.jpg", open("hero.jpg", "rb"), content_type="image/jpeg")
published = av.publish_asset("blog/hero.jpg", "blog/hero.jpg",
                             cache_control="public, max-age=86400")
print(published["url"])
```

```js
await av.upload("blog/hero.jpg", imageBytes, { contentType: "image/jpeg" });
const published = await av.publishAsset("blog/hero.jpg", "blog/hero.jpg", {
  cacheControl: "public, max-age=86400",
});
document.querySelector("img").src = published.url;
```

The SDKs also expose the management side of these resources: list/revoke
shares, list/unpublish assets, list/delete ACL entries, and full department and
membership lifecycle operations. Applications therefore do not need to mix
SDK calls with hand-written REST requests for administrative cleanup.

### Portable backup export

```bash
curl -L "$BASE/v1/exports/archive?bucket=default&prefix=blog/" \
  -H "Authorization: Bearer $TOKEN" -o aero-blog-backup.tar.gz
```

The gzip-compressed tar contains `manifest.json` plus `objects/<key>` entries.
The manifest records content type, ETag, user metadata, tags, size, and update
time. Export is backend-independent and includes only objects for which the
caller has `object:export`; internal `_aero_*` metadata is never exposed.

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

> **Scope gate (breaking with auth enabled):** the HTTP `/mcp` mount requires
> the authenticated principal to hold **`write` AND `audit:event:write`, or
> `admin`** — all tools, read tools included (denial is transport-level, HTTP
> 403 `missing scope: audit:event:write`, before any JSON-RPC dispatch).
> Unauthenticated requests get HTTP 401. `AUTH_KEYS` / SigV4 / Snaplink keys
> cannot express the audit scope (`knownScope`); provision MCP principals via
> JWT claims or `/v1/admin/keys`, or use stdio (`aero-vault mcp`, ungated).

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
The stdio transport has no HTTP principal and is **not** covered by the scope
gate above.

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
| `GET` | `/v1/buckets` | List buckets for the current tenant. |
| `GET` | `/v1/buckets/{bucket}/config` | Get config. |
| `PUT` | `/v1/buckets/{bucket}/versioning` | Toggle versioning. |
| `PUT` | `/v1/buckets/{bucket}/object-lock` | Default retention (seconds). |
| `PUT` | `/v1/buckets/{bucket}/lifecycle` | Expire-after-days policy. |
| `GET` | `/v1/buckets/{bucket}/lifecycle` | Get lifecycle policy. |
| `GET` | `/v1/buckets/{bucket}/acl` | Get bucket ACL. |
| `PUT` | `/v1/buckets/{bucket}/acl` | Set bucket canned ACL. |
| `GET` | `/v1/buckets/{bucket}/versions` | List all versions and delete markers with `prefix`, `key-marker`, `version-id-marker`, and `max-keys` pagination. |
| `GET` | `/v1/buckets/{bucket}/stats` | Get object count and total bytes. |
| `DELETE` | `/v1/buckets/{bucket}` | Delete the bucket and its objects. |

```bash
curl -s "$BASE/v1/buckets/default/config"
# -> { "name":"default","versioning":false,"object_lock_seconds":0, "expire_after_days":0,"expire_action":"" }

curl -s -X PUT -d '{"enabled":true}'                       "$BASE/v1/buckets/default/versioning"
curl -s -X PUT -d '{"seconds":3600}'                       "$BASE/v1/buckets/default/object-lock"
curl -s -X PUT -d '{"days":30,"action":"soft_delete"}'     "$BASE/v1/buckets/default/lifecycle"
curl -s -X PUT -H 'x-amz-acl: private'                     "$BASE/v1/buckets/default/acl"
curl -s         "$BASE/v1/buckets/default/acl"
curl -s         "$BASE/v1/buckets/default/versions?max-keys=100"
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
| `DELETE` | `/v1/admin/tenants/{tenant}` | Delete an empty tenant and its control-plane records; returns `409 TenantNotEmpty` while buckets, objects, or uploads remain. |
| `PUT` | `/v1/admin/tenants/{tenant}/status` | Set tenant status (`active`\|`disabled`). |
| `GET` | `/v1/admin/audit` | List audit-log entries (admin actions). |
| `GET` | `/v1/admin/keys` | List API keys (tokens redacted). |
| `POST` | `/v1/admin/keys` | Add key. |
| `DELETE` | `/v1/admin/keys/{token}` | Revoke key. |
| `POST` | `/v1/admin/jwt` | Issue JWT. |
| `GET` | `/v1/admin/webhook-failures` | List undelivered webhooks. |
| `GET` | `/v1/admin/jobs` | List background jobs + status histogram. |
| `POST` | `/v1/admin/jobs/{id}/retry` | Requeue a job. |
| `DELETE` | `/v1/admin/files/{tenant}/{key}` | Soft-delete an object in the default bucket; add `?hard=1` for physical deletion. Requires the `vault.file.delete` admin boundary. |
| `DELETE` | `/v1/admin/files/{tenant}/{bucket}/{key}` | Same operation with an explicit bucket. |

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

# Disable / re-enable a tenant (status: "active" | "disabled"). Disabled
# tenants receive 403 on data-plane APIs and public share/asset reads; operator
# health, readiness, UI, docs, and OIDC endpoints remain available.
curl -s -X PUT -d '{"status":"disabled"}' "$BASE/v1/admin/tenants/acme/status"
curl -s -X PUT -d '{"status":"active"}'   "$BASE/v1/admin/tenants/acme/status"

# Delete an empty tenant (204; 404 if not found; 409 while data resources remain).
# Delete objects/uploads/buckets through their normal APIs first so storage and
# indexing cleanup runs through FileService.
curl -s -X DELETE "$BASE/v1/admin/tenants/acme"

# Set per-tenant daily AI budget (0 clears the override)
curl -s -X PUT -d '{"daily_budget_usd":5.00}' "$BASE/v1/admin/tenants/acme/budget"
```

### Audit log

Admin and security actions are recorded automatically: key add/revoke, tenant create/delete/status, quota/budget set, and administrative file deletion. Admin file-delete rows retain `action="file.delete"`; their `detail` is `hard;permission=vault.file.delete` or `soft;permission=vault.file.delete`.

```bash
# Most recent 50 entries (default 100)
curl -s "$BASE/v1/admin/audit?limit=50"
# -> { "audit": [ { "actor":"ops","action":"key.add","target":"…","detail":"…","created_at":"…" } ] }
```

---

# S3-compatibility

The S3 gateway is disabled by default. Set `S3_COMPAT_PREFIX` to a non-empty
mount prefix such as `/s3` to enable a practical subset of the S3 REST API in
**path-style** form (`/{bucket}/{key+}`). It shares the same `FileService`, so
S3 and REST see the same objects.

## Supported operations

| S3 operation | HTTP | Supported | Notes |
|--------------|------|-----------|-------|
| PutObject | `PUT /{bucket}/{key}` | ✅ | User metadata via `x-amz-meta-*`; canned ACL via `x-amz-acl`. |
| GetObject | `GET /{bucket}/{key}` | ✅ | Honors `Range` → `206`; returns `x-amz-meta-*`. |
| HeadObject | `HEAD /{bucket}/{key}` | ✅ | |
| DeleteObject | `DELETE /{bucket}/{key}` | ✅ | Creates a delete marker when versioning is enabled; otherwise deletes the object. `versionId` deletes an exact version. |
| CopyObject | `PUT` + `x-amz-copy-source` | ✅ | `x-amz-metadata-directive: COPY` (default) or `REPLACE`. |
| ListObjectsV2 | `GET /{bucket}?list-type=2` | ✅ | `prefix`, `delimiter`, `continuation-token`, `max-keys`, `start-after`; grouped keys are returned as `CommonPrefixes` and count toward `KeyCount`/`max-keys`. |
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
| ListObjects (v1) | `GET /{bucket}` (no `list-type=2`) | ✅ | `prefix`, `delimiter`, `marker`, `max-keys`; grouped keys are returned as `CommonPrefixes`, with `NextMarker` when truncated. |
| Versioning / lock / lifecycle sub-resources | `GET`/`PUT /{bucket}?versioning`, `?object-lock`, `?lifecycle` | ✅ | XML config round-trips; `GET ?lifecycle` is `404 NoSuchLifecycleConfiguration` when unset. |
| DeleteBucketLifecycle | `DELETE /{bucket}?lifecycle` | ✅ | Clears the expiry policy; `204 No Content`. |
| GetBucketAcl / PutBucketAcl | `GET`/`PUT /{bucket}?acl` | ✅ | Canned ACL ↔ `AccessControlPolicy`; PUT via `x-amz-acl` header or policy body. |
| GetBucketLocation | `GET /{bucket}?location` | ✅ | Empty `LocationConstraint` (us-east-1); `404 NoSuchBucket` if absent. |
| ListObjectVersions | `GET /{bucket}?versions` | ✅ | Returns `<Version>` and `<DeleteMarker>` entries. Combined-count pagination supports `prefix`, `max-keys`, `key-marker`, and `version-id-marker`, including continuation within one key. |
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
bodies. The S3 gateway is inside the global Auth → Tenant status chain, so
known disabled tenants are also rejected for both header-signed and presigned
SigV4 requests.

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
