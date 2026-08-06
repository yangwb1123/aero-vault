# Snaplink billing integration

Aero Vault treats Snaplink as the subscription and entitlement decision source,
while retaining one local authority for file CRUD and current storage usage:
`FileService` plus `tenant_quotas`. Remote responses only project the two hard
caps; they never overwrite `used_bytes` or `used_objects`.

## Server-owned tenant bindings

Enable billing and mount a JSON file based on
[`deploy/snaplink-billing-bindings.example.json`](../deploy/snaplink-billing-bindings.example.json):

```dotenv
BILLING_ENABLED=true
BILLING_BASE_URL=https://billing.example.com
BILLING_TOKEN_URL=https://sso.example.com/token
BILLING_BINDINGS_FILE=/etc/aero-vault/billing-bindings.json
SNAPLINK_BILLING_ACME_SECRET=read-from-a-secret-manager
```

Every tenant uses a distinct confidential client. The JSON stores only
`tenant_id`, `client_id`, and `client_secret_env`; the referenced environment
variable must exist at startup. Aero obtains a machine token with Snaplink's
`TokenClient.ClientCredentials` and requests
`billing:entitlement:read metering:write`. Neither tenant nor source is sent in
an entitlement or usage request. `snaplink-billing` must bind each `client_id`
to the same tenant and to an Aero-owned source system, and must allow these
dimensions:

- `storage_bytes_allocated`
- `storage_bytes_reclaimed`
- `storage_objects_created`
- `storage_objects_deleted`

Aero additionally verifies that the entitlement response's `tenant_id` equals
the server-side binding before applying it.

## Quota and usage semantics

The projector reads `storage_bytes` and `storage_objects` from the entitlement.
It writes only `max_bytes` and `max_objects`, in the same transaction as the
versioned projection. Current used values remain local. Explicit unlimited
grants map to Aero's local `0 = unlimited` representation. A non-unlimited hard
limit of zero is enforced by the subscription guard before the older local
quota representation is evaluated, so zero can never accidentally become
unlimited.

Every FileService storage delta updates the local current gauge and inserts
stable outbox facts in one database transaction. Snaplink's monthly usage
ledger accepts positive quantities only, so signed deltas map to separate
positive event dimensions: allocation/reclaim and create/delete. This avoids
treating deletion as positive current capacity. The outbox is claimed with a
database lease; retries use exponential backoff and central delivery uses the
fact ID as both body ID and idempotency key. A process crash can redeliver a
fact, but Snaplink idempotency prevents double counting.

The reservation API is implemented by the typed client for positive, monthly
metered dimensions, but is deliberately absent from file request paths.
Current-storage enforcement is a local gauge operation, and a synchronous
remote reservation would both use the wrong semantic and make upload success
depend on remote availability.

## Failure behavior

- A configured tenant without any durable projection fails write/delete
  preflight closed and makes `/readyz` return 503.
- Once a projection exists, a temporary Snaplink outage leaves that projection
  and the local current-usage gauge in force. An inactive, not-yet-effective,
  or expired entitlement rejects capacity-increasing mutations; cleanup can
  still reduce usage.
- Usage delivery never runs synchronously on a file request. Accepted local
  mutations remain in the durable outbox and retry indefinitely.
- `/healthz` remains a liveness probe. `/readyz` checks the database, storage,
  and initial projection availability without exposing dependency error text.
- HTTP is time-bounded, redirects are rejected, response bodies are bounded,
  and errors/logs never contain client secrets or bearer tokens.

## Multi-replica Helm example

Store the binding JSON in a ConfigMap (it has no secret) and client secrets in a
Secret. The chart's generic volume hooks can mount it:

```yaml
replicaCount: 3
config:
  BILLING_ENABLED: "true"
  BILLING_BASE_URL: "https://billing.example.com"
  BILLING_TOKEN_URL: "https://sso.example.com/token"
  BILLING_BINDINGS_FILE: "/etc/aero-vault/billing-bindings.json"
secrets:
  SNAPLINK_BILLING_ACME_SECRET: "replace-via-secret-manager"
extraVolumes:
  - name: billing-bindings
    configMap:
      name: aero-vault-billing-bindings
extraVolumeMounts:
  - name: billing-bindings
    mountPath: /etc/aero-vault
    readOnly: true
```

Use PostgreSQL for multiple replicas. Projector work is coordinated through the
existing database lease table; outbox claims use `FOR UPDATE SKIP LOCKED` and
claim expiry. SQLite remains suitable for a single replica and uses guarded
transactional claims.
