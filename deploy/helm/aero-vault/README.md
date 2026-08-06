# aero-vault Helm chart

Deploy the aero-vault AI-native file platform to Kubernetes.

## Install

```bash
# default: single replica, SQLite + local storage on a PVC (no external deps)
helm install av deploy/helm/aero-vault

# point at your image
helm install av deploy/helm/aero-vault \
  --set image.repository=ghcr.io/you/aero-vault --set image.tag=0.4.0
```

## Production (Postgres + S3 + RAG, horizontally scalable)

```yaml
# prod-values.yaml
replicaCount: 3
persistence:
  enabled: false            # no local PVC; state lives in Postgres + S3
config:
  STORAGE_BACKEND: "s3"
  STORAGE_S3_ENDPOINT: "https://s3.amazonaws.com"
  STORAGE_S3_BUCKET: "my-aero-vault"
  STORAGE_S3_REGION: "us-east-1"
  DB_DRIVER: "postgres"
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
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: vault.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls:
    - secretName: vault-tls
      hosts: [vault.example.com]
```

```bash
helm install av deploy/helm/aero-vault -f prod-values.yaml
```

## Resources rendered

| Template            | When                                            |
|---------------------|-------------------------------------------------|
| Deployment, Service | always                                          |
| ConfigMap           | always (non-secret env from `.config`)          |
| ServiceAccount      | `serviceAccount.create=true` (default)          |
| Secret              | `.secrets` non-empty and no `existingSecret`    |
| PersistentVolumeClaim | `persistence.enabled` and no `existingClaim`  |
| Ingress             | `ingress.enabled`                               |
| HorizontalPodAutoscaler | `autoscaling.enabled`                       |
| PodDisruptionBudget | `podDisruptionBudget.enabled`                   |

## Configuration

Non-secret env goes in `.config` (→ ConfigMap), sensitive env in `.secrets`
(→ Secret); both are injected via `envFrom`, so any env var the binary reads is
settable. See [`.env.example`](../../../.env.example) for the full list.

Secrets override the ConfigMap (the Secret's `envFrom` is listed last). For
GitOps, prefer `existingSecret: <name>` and manage the Secret out of band rather
than putting credentials in values.

## Notes

- The image is `distroless:nonroot`; the chart runs as uid/gid 65532 with a
  read-only root filesystem. A `tmp` `emptyDir` is mounted at `/tmp` for
  multipart upload buffering.
- The default (local/SQLite) config needs `persistence.enabled=true` and a
  single replica (ReadWriteOnce). For multiple replicas use Postgres + S3 and
  disable persistence.
- Probes hit `/healthz` (liveness/startup) and `/readyz` (readiness).
- Enable the Snaplink Audit Governance relay with
  `auditGovernance.enabled=true`, its HTTPS `baseURL`/`tokenURL`, and
  `bindingsConfigMap`. Set `hmacSecretName` to a dedicated Secret containing
  the key selected by `hmacSecretKey`. Store the binding JSON under
  `bindingsKey` and put every named `AUDIT_GOVERNANCE_CLIENT_SECRET_*` variable
  in `existingSecret`; the HMAC key, Audit OAuth clients, and Billing machine
  credentials must all be distinct. See
  [`docs/snaplink-audit-governance.md`](../../../docs/snaplink-audit-governance.md).

## Validate locally

```bash
helm lint deploy/helm/aero-vault
helm template av deploy/helm/aero-vault -f prod-values.yaml | kubectl apply --dry-run=client -f -
```
