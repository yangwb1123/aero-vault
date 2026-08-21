# Deployment

aero-vault ships as a single static Go binary and a minimal container image. This
guide covers running it with Docker, the bundled Docker Compose demo, the Helm
chart, and notes for production.

The server exposes:

- `:8080` (default `APP_ADDR`) — HTTP API (REST `/v1`, S3 `/s3`, WebDAV, MCP,
  `/ui`, `/docs`).
- `/healthz` (liveness), `/readyz` (readiness — pings the DB).
- `/metrics` (Prometheus) when `PROMETHEUS_ENABLED=true`.

---

## Docker

The repository `Dockerfile` is a two-stage build producing a `distroless`
`nonroot` image (uid/gid `65532`):

```dockerfile
FROM golang:1.26.1-alpine AS build    # CGO_ENABLED=0, trimpath
...
FROM gcr.io/distroless/static-debian12:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/aero-vault"]
```

Build and run:

```bash
# Build (or: make docker → aero-vault:dev)
docker build -t aero-vault:latest .

# Run with the default local/SQLite config, persisting state under /app/var.
docker run --rm -p 8080:8080 \
  -e STORAGE_BACKEND=local \
  -e STORAGE_LOCAL_ROOT=/data/objects \
  -e DB_DSN='file:/data/aero.db?_pragma=foreign_keys(1)' \
  -v "$PWD/data:/data" \
  aero-vault:latest
```

Because the image is `distroless:nonroot` with no shell, pass all configuration
via `-e` / `--env-file`. The container runs as a non-root user, so mounted volumes
must be writable by uid `65532`.

A simpler single-service compose file (`docker-compose.yml`) is also included for
running just the app.

---

## Docker Compose (full RAG demo)

`deploy/docker-compose.demo.yml` brings up the whole platform plus its dependencies with
one command:

| Service | Image | Purpose |
|---------|-------|---------|
| `app` | built from `Dockerfile` | aero-vault, on `:8080`. |
| `postgres` | `postgres:16-alpine` | Metadata DB (`aero`/`aero`/`aero_vault`), on `:5432`. |
| `minio` | `minio/minio` | S3-compatible object store, API `:9000`, console `:9001` (`minioadmin`/`minioadmin`). |
| `minio-init` | `minio/mc` | Creates the `aero-vault` bucket, then exits. |
| `ollama` | `ollama/ollama` | Local LLM + embeddings, on `:11434`. |
| `ollama-init` | `ollama/ollama` | Pulls the chat + embedding models, then exits. |
| `otel-collector` | `otel/opentelemetry-collector-contrib` | OTLP gRPC `:4317`, OTLP HTTP `:4318`, Prometheus scrape `:8889`. |

```bash
docker compose -f deploy/docker-compose.demo.yml up --build
```

The first run downloads the Ollama models (a few hundred MB up to ~1.3 GB). The
`app` service waits for Postgres, MinIO, the bucket init, the model pull, and the
collector before starting. Once it is listening:

```bash
./deploy/demo/seed.sh             # upload → index → search → RAG chat
open http://localhost:8080/docs   # Swagger UI
open http://localhost:9001        # MinIO console
```

Override models with `DEMO_CHAT_MODEL` / `DEMO_EMBED_MODEL`. To use an NVIDIA GPU,
uncomment the `deploy.resources.reservations.devices` block on the `ollama`
service (requires `nvidia-container-toolkit`).

The demo's `app` environment block is a complete, working example of a
production-shaped configuration (Postgres + S3 + AI + job pool + OTLP +
Prometheus); see [`docs/configuration.md`](configuration.md) for the worked
reference.

---

## Kubernetes (Helm)

A Helm chart lives in [`deploy/helm/aero-vault`](../deploy/helm/aero-vault)
(chart version `0.1.0`, appVersion `0.4.0`). Out of the box it deploys a single
replica using SQLite + local storage on a PersistentVolume — a working,
dependency-free install.

```bash
# Default: 1 replica, SQLite + local storage on a PVC
helm install av deploy/helm/aero-vault

# Point at your image
helm install av deploy/helm/aero-vault \
  --set image.repository=ghcr.io/you/aero-vault --set image.tag=0.4.0
```

Configuration is split between a ConfigMap (non-secret env under `.config`) and a
Secret (sensitive env under `.secrets`), both injected via `envFrom`, so **any**
environment variable the binary reads is settable from values. Secrets override
ConfigMap values. For GitOps, prefer `existingSecret: <name>`.

Resources rendered: Deployment + Service + ConfigMap (always); ServiceAccount,
Secret, PVC, Ingress, HorizontalPodAutoscaler, and PodDisruptionBudget
(conditionally). The pods run as uid/gid `65532` with a read-only root filesystem
and an `emptyDir` mounted at `/tmp` for multipart buffering. Liveness/startup
probes hit `/healthz`; readiness hits `/readyz`.

### Production values (Postgres + S3 + autoscaling)

```yaml
# prod-values.yaml
replicaCount: 3
persistence:
  enabled: false            # no local PVC — state lives in Postgres + S3
config:
  STORAGE_BACKEND: "s3"
  STORAGE_S3_ENDPOINT: "https://s3.amazonaws.com"
  STORAGE_S3_BUCKET: "my-aero-vault"
  STORAGE_S3_REGION: "us-east-1"
  STORAGE_S3_FORCE_PATH_STYLE: "false"   # true for MinIO / most S3-compatible stores
  DB_DRIVER: "postgres"
  PROMETHEUS_ENABLED: "true"
  AI_INDEX_ENABLED: "true"
  AI_EMBED_PROVIDER: "http"
  AI_EMBED_ENDPOINT: "http://ollama:11434"
  OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector:4318"
secrets:
  DB_DSN: "postgres://aero:pass@postgres:5432/aero_vault?sslmode=disable"
  STORAGE_S3_ACCESS_KEY: "AKIA..."
  STORAGE_S3_SECRET_KEY: "..."
  AUTH_KEYS: "prod-rw:acme:read+write,ops:*:admin"
autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10
  targetCPUUtilizationPercentage: 80
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "0"   # allow large uploads
  hosts:
    - host: vault.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls:
    - secretName: vault-tls
      hosts: [vault.example.com]
```

```bash
helm install av deploy/helm/aero-vault -f prod-values.yaml

# Validate before applying
helm lint deploy/helm/aero-vault
helm template av deploy/helm/aero-vault -f prod-values.yaml | kubectl apply --dry-run=client -f -
```

See [`deploy/helm/aero-vault/README.md`](../deploy/helm/aero-vault/README.md) for
the full chart reference.

---

## Production guidance

**Stateless scaling.** Use **PostgreSQL** for metadata and an **S3-compatible**
object store for bytes (`DB_DRIVER=postgres`, `STORAGE_BACKEND=s3`). With external
state, the app is stateless and scales horizontally — disable the PVC and raise
`replicaCount` / enable autoscaling. The local/SQLite default is single-replica
only (`ReadWriteOnce`, embedded DB), suitable for dev or small single-node
deployments.

**Background workers.** Keep `JOBS_WORKERS>0` in production so indexing,
antivirus, and replication run on the durable, retrying job queue rather than
inline. The jobs table makes the work crash-safe across restarts; inspect it via
`GET /v1/admin/jobs`.

**Security.** Enable auth (`AUTH_KEYS` and/or `AUTH_JWT_SECRET`) before exposing
the service. Use tenant-scoped keys for callers and reserve `*:admin` operator
keys for control-plane use. Tenant-scoped admin keys are confined to their own
tenant for delegated keys/JWTs, quotas, and administrative file deletion;
global tenant/job/audit/config views require the operator tenant `*`. For S3
clients, configure `S3_SIGV4_CREDENTIALS`.
Keep all secrets in a Kubernetes Secret (or `existingSecret`), never in the
ConfigMap. Set per-tenant quotas via `PUT /v1/admin/tenants/{tenant}/quota`.

**Quotas & rate limiting.** Protect shared deployments with per-tenant quotas and
the per-tenant token bucket (`RATE_LIMIT_RPS` / `RATE_LIMIT_BURST`).

**Large uploads.** Behind nginx-ingress set
`nginx.ingress.kubernetes.io/proxy-body-size: "0"`; for multipart uploads ensure
`/tmp` is writable (the chart mounts an `emptyDir`).

**Observability.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` to ship traces + metrics to a
collector, and/or `PROMETHEUS_ENABLED=true` to scrape `/metrics` directly. A
ready-made scrape config is in [`deploy/prometheus/`](../deploy/prometheus) and a
starter Grafana dashboard in [`deploy/grafana/`](../deploy/grafana).

**Resilience.** Run multiple replicas with a PodDisruptionBudget. Configure
backups for Postgres and rely on the object store's durability for bytes;
optionally enable cross-region replication (`REPLICATION_*`) for a second copy.
Use `RECONCILE_INTERVAL_MINUTES>0` to periodically reconcile orphans and apply
lifecycle expiry.

**Backup scope.** `GET /v1/exports/archive` is a portable, permission-filtered
object export; it is not a control-plane disaster-recovery image. SQLite + local
FS installations can capture the database (including departments, ACLs,
shares, assets, keys, and audit state) together with blobs while the server is
running. Snapshot creation opens the live SQLite database with a busy timeout
and uses `VACUUM INTO` to produce one transactionally consistent database image
(without `-wal`/`-shm` sidecars):

```bash
aero-vault cli snapshot create backup.tgz \
  --db file:./var/aero.db --objects ./var/objects
aero-vault cli snapshot restore backup.tgz \
  --db file:./var/aero.db --objects ./var/objects
```

Restore pre-validates the full gzip/tar stream, rejects non-regular or escaping
paths, and confines writes to the selected database and object roots. For
Postgres + S3/MinIO deployments, back up PostgreSQL with `pg_dump`/WAL archival
and enable bucket versioning or replication; both metadata and object-store
backups are required for a complete restore.

**Health checks.** Wire liveness to `/healthz` and readiness to `/readyz`.
Readiness covers database/storage plus enabled Billing projection and Audit
Governance binding drain/maximum-backlog-lag checks; liveness never depends on
those remote services. Supervisors must allow at least 95 seconds for a bounded
Audit Governance relay drain. See
[the Audit Governance deployment contract](snaplink-audit-governance.md).

### `source.ywbsd.site` systemd + FRP deployment

This workspace's production-shaped single-node deployment uses
[`deploy/systemd/aero-vault.service`](../deploy/systemd/aero-vault.service) and
[`deploy/run-fullstack-snaplink.sh`](../deploy/run-fullstack-snaplink.sh). It
binds Aero Vault to `127.0.0.1:18081`; the `mlf2-web-aero-vault` FRP HTTP proxy
publishes that listener as `source.ywbsd.site` and fixes
`X-Forwarded-Proto: https` for OIDC callbacks and generated public URLs.

The launch script reuses PostgreSQL/MinIO data and creates three mode-0600
secrets in `/var/lib/aero-vault`: `presign-secret`, `share-secret`, and the
bootstrap `operator-token`. Persistent project API keys are SHA-256 hashed in
PostgreSQL. Restart only the application after rebuilding; the FRP client does
not need a restart unless its proxy definition changes.

```bash
make check
sudo systemctl restart aero-vault.service
curl -fsS https://source.ywbsd.site/readyz
```

## Thumbnail cache TTL purge (2026-08-16)

Activation is **independent of the Reconcile ticker**: `THUMBNAIL_CACHE_TTL > 0`
alone starts the per-process physical-purge driver (initial sweep at start,
then once per cadence). Cadence = the `RECONCILE_INTERVAL_MINUTES` interval
when set (unchanged for existing deployments), else one sweep per TTL — the
default-config bound, worst case physical retention of a never-read expired
key ≤ 2×TTL. Observability: `thumbnail.cache.sweep_runs_total` per executed
pass (liveness), `thumbnail.cache.swept_total` entries removed; alert
`ThumbnailCacheSweepStalled` (alerts.yml) fires when no pass executes within
3× the sweep interval (envelope-adaptive since `d21de9c`, not a static 1h
window). **No action required for existing deployments —
`RECONCILE_INTERVAL_MINUTES > 0` deployments see zero cadence change**;
`TTL = 0` deployments see zero change (no driver, lazy-only bound). The
sweep is a memory curve change only.

## Thumbnail cache hit-ratio accounting (2026-08-16)

TTL-expired reads are now a **distinct class** from genuine misses:
`thumbnail.cache.misses_total` drops to genuine-miss levels, the new
`thumbnail_cache_expired_total` counter rises for expired reads, and
`ThumbnailCacheHitRatioLow` (plus the hit-ratio panel) no longer counts
expired reads as misses — a class of deployments that previously paged on the
alert (TTL below the hot-key inter-request gap) will silently stop paging,
by design. An all-expired workload shows **no data** on the hit-ratio panel
(hits=0 ∧ misses=0, activity guard false) while `expired_total` rises —
visually identical to an idle/disabled cache; triage via
`rate(thumbnail_cache_expired_total[5m])` and size `THUMBNAIL_CACHE_TTL`
above the hot-key inter-request interval. `thumbnail_cache_misses_total` is a
semantics change for any external dashboard consuming it (in-repo panel +
alert are description-updated); no config change, no migration.
