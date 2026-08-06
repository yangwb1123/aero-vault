# Snaplink Audit Governance integration

Aero Vault can optionally relay its local admin/security audit rows and file
lifecycle events to the separately deployed `snaplink-audit-governance`
service. The file operation and admin API never make a synchronous Governance
call: the local database first persists a redacted outbox fact, and background
workers deliver it later.

## Fixed trust and schema contract

For each tenant, provision all of the following before enabling the runtime:

- tenant source ID: `aero-vault.` + base64url-no-padding of the keyed,
  tenant-domain HMAC described under Redaction below;
- source allow-list containing that tenant's signed OAuth `client_id`;
- active schema ID and event type `aero.vault.security`, version `1`;
- classification `confidential`;
- retention class `security`;
- required payload fields: `fact_kind`;
- allowed payload fields: `detail_sha256`, `fact_kind`,
  `object_size_bytes`, `request_id`, `storage_backend`.

The relay uses `client_credentials` with exactly `audit:event:write` and
`resource=audit-governance`. The event body does not contain `tenant_id`; Audit
Governance derives it from the validated token and checks the derived
`source_system` against the pre-registered `(tenant, client_id)` binding.

The default example client ID is `aero-vault-audit-relay`. A multi-tenant
production installation should provision one distinct confidential client per
tenant and list the corresponding client ID on only that tenant's source.

## Desired binding file

Start from
[`deploy/snaplink-audit-governance-bindings.example.json`](../deploy/snaplink-audit-governance-bindings.example.json):

```json
{
  "revision": 1,
  "bindings": [
    {
      "tenant_id": "tenant-example",
      "client_id": "aero-vault-audit-relay",
      "client_secret_env": "AUDIT_GOVERNANCE_CLIENT_SECRET_TENANT_EXAMPLE",
      "state": "active"
    }
  ]
}
```

The JSON may name a secret environment variable but cannot contain a secret.
Secret names must start with `AUDIT_GOVERNANCE_CLIENT_SECRET_`. The loader is
strict, bounded to 1 MiB, rejects unknown/trailing JSON, duplicate tenants or
clients, reused secret variables or secret values, symlinks, non-regular files,
and group/world-writable files. `state` is either `active` (capture and deliver)
or `draining` (stop capture and deliver only the existing backlog); omitted
state defaults to `active`. Binding changes are cold configuration: replace the
desired file, increment `revision`, and roll instances.

The latest revision and a keyed digest of the complete desired manifest are
persisted in the metadata database. A lower revision or different content at
the same revision fails startup, including after a restore. Identical content
at the same revision is idempotent across replicas. Credential or HMAC-key
rotation changes the digest and therefore requires a higher revision.

Billing and Audit Governance credentials are separate trust domains. If both
integrations are enabled, startup rejects any shared client ID, secret variable
name, or secret value.

## Redaction boundary and key rotation

The durable outbox and outbound body never contain file content, raw object or
bucket paths, local audit detail, credentials, bearer tokens, ETags, or content
types. Actor, target, request ID, and audit detail are either omitted or stored
as keyed HMAC-SHA-256 pseudonyms. The required
`AUDIT_GOVERNANCE_HMAC_KEY` is an independent secret of 32 to 4096 bytes; do not
reuse an OAuth client secret, Billing credential, JWT key, presign key, or
storage credential. All replicas must receive the same key. `action` remains a
bounded canonical action such as `key.add`, `tenant.status`, `file.created`, or
`file.deleted`; unrecognised values collapse to a fixed `*.unknown` action.

Pseudonyms use the binary message
`"aero-vault/audit-governance/v1" NUL tenant_id NUL field NUL value NUL` and
base64url-no-padding output. Each field has a separate domain (`actor`,
`target`, `request`, `detail`, `source-system`, and log-only domains), so equal
low-entropy values cannot be correlated across tenants or fields. The source
ID uses `field=source-system` and `value=tenant_id`. Calculate and register that
opaque source ID locally before enabling delivery; the HMAC key itself never
goes to Audit Governance. The existing wire key `detail_sha256` is retained for
schema compatibility, but its value is now a keyed HMAC pseudonym rather than
an unkeyed digest.

For key rotation, first register the newly calculated source IDs alongside the
old IDs, replace the dedicated secret, increment the binding revision, and roll
all replicas. Existing outbox rows retain their old actor/target pseudonyms and
remain valid; rows published by a new replica use the new source ID. Keep both
source-ID generations allowed until the rollout and backlog drain are complete,
then revoke the old IDs. A rollback also needs a new, higher revision because
same-revision drift is deliberately rejected.

When upgrading from the legacy unkeyed source-ID format, treat the first HMAC
deployment as a rotation: pre-register every new HMAC source ID, keep the legacy
source ID allowed during the rolling update and backlog drain, and remove it
only after no old process remains. There is intentionally no runtime fallback
that emits the dictionary-attackable legacy identifier.

The only payload keys are the five registered schema fields. `fact_kind` is one
of `admin`, `security`, or `file`; object size and the allow-listed storage
backend are included only when known.

## Durability, HA, and failure behavior

Migrations `0039_audit_governance_outbox` and
`0040_audit_governance_control` are paired for SQLite and Postgres.
Local `audit_log`/`object_events` persistence and its redacted outbox insert use
one database transaction. Reconcile scans every configured tenant for historic
local rows without an outbox origin, alternates audit and file origins to avoid
starvation, and inserts them idempotently.

Postgres replicas claim due rows with `FOR UPDATE SKIP LOCKED`; SQLite claims
inside a transaction. Every acknowledgement/retry is fenced by a per-process
owner, a fresh random claim token, and an unexpired lease. Failed facts retry
forever with deterministic exponential backoff and a configured cap. Governance
event ID and idempotency key are identical, so a worker crash after remote
acceptance is safe to replay.
Claims also match the persisted desired-binding revision, so a superseded
replica cannot take work belonging to a newly added or rotated binding.

Delivered outbox bodies are retained for
`AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS` and then atomically replaced by
small permanent `(origin_kind, origin_id)` tombstones before deletion. Reconcile
joins both live outbox rows and tombstones, so cleanup cannot recreate an
already delivered origin or silently omit a never-delivered origin. Tombstones
are part of the metadata backup contract.

`/healthz` remains pure liveness. `/readyz` fails only when the database/storage
checks fail, Billing has no usable projection, or the oldest undelivered audit
fact for a currently bound tenant exceeds `AUDIT_GOVERNANCE_MAX_LAG_SECONDS`,
or a `draining` tenant still has pending facts. Unbound local history does not
pollute readiness. A short Governance outage does not evict replicas. Shutdown
stops new claims, lets the current bounded batch finish, and then closes idle
connections. Production supervisors allow 95 seconds, covering the configured
60-second maximum claim lease and 29-second maximum HTTP timeout.

## Lossless tenant unbinding and disable

Never delete an active binding in one step. The database rejects startup if a
desired manifest would leave any undelivered row unbound, and the error exposes
only keyed opaque references. Use this sequence for one tenant:

1. Set its state to `draining`, increment the revision, and roll every replica.
   Database binding state is authoritative, so even an older replica stops
   adding outbox rows while local audit/event history continues.
2. Wait for `/readyz` to recover. While that tenant has pending rows it returns
   `503`; workers on all replicas may continue draining with fenced claims.
3. Revoke the tenant source allow-list entry and its OAuth client credential in
   Snaplink/Audit Governance.
4. Remove the binding, increment the revision again, and roll replicas.

To disable the integration entirely, drain every tenant, apply and roll one
enabled manifest with an empty `bindings` array and a higher revision, and only
then set `AUDIT_GOVERNANCE_ENABLED=false`. Startup refuses the disabled state
while persisted bindings or undelivered rows remain. To restore a removed
tenant, provision fresh source/client credentials and add an `active` binding
at a higher revision; reconcile then captures local history produced while it
was draining or absent, without rebuilding tombstoned delivered origins.

Back up the outbox with the metadata database. After restore, workers reconcile
and replay undelivered facts; Audit Governance idempotency prevents duplicates.

## Deployment

For Compose, provide the referenced URLs, dedicated HMAC key, and client secret,
then layer the
opt-in override onto the demo stack:

```bash
docker compose -f deploy/docker-compose.demo.yml \
  -f deploy/docker-compose.audit-governance.yml up -d
```

For Helm, set `auditGovernance.enabled`, both URLs,
`auditGovernance.hmacSecretName`, and an existing ConfigMap holding the binding
JSON. The named HMAC Secret is injected through the key selected by
`auditGovernance.hmacSecretKey`; put each OAuth client secret into
`existingSecret` or the chart's Secret values. The chart mounts one ConfigMap
key with `subPath`, so the runtime sees a regular read-only file rather than a
projected-volume symlink. Roll the Deployment after a desired-file revision or
Secret change. The pod termination grace period is 95 seconds.

For systemd, place non-secret settings and the dedicated secret variables in a
mode-0600 `/etc/aero-vault/audit-governance.env`; the supplied unit reads it
optionally and allows 95 seconds for shutdown. Keep the binding JSON outside
the repository and set its path in that environment file.
