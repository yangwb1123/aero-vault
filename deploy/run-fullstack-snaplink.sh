#!/usr/bin/env bash
set -euo pipefail

runtime_db="aero_vault_codex_fullstack_20260729"
pg_user="$(docker exec aero-postgres printenv POSTGRES_USER)"
pg_password="$(docker exec aero-postgres printenv POSTGRES_PASSWORD)"
minio_user="$(docker exec aero-minio printenv MINIO_ROOT_USER)"
minio_password="$(docker exec aero-minio printenv MINIO_ROOT_PASSWORD)"
runtime_dsn="host=127.0.0.1 port=5432 user=${pg_user} password=${pg_password} dbname=${runtime_db} sslmode=disable"

state_dir="${AERO_STATE_DIR:-/var/lib/aero-vault}"
presign_file="${state_dir}/presign-secret"
mkdir -p "${state_dir}"
if [[ ! -s "${presign_file}" ]]; then
  umask 077
  openssl rand -hex 32 >"${presign_file}"
fi
presign_secret="$(<"${presign_file}")"
share_file="${state_dir}/share-secret"
if [[ ! -s "${share_file}" ]]; then
  umask 077
  openssl rand -hex 32 >"${share_file}"
fi
share_secret="$(<"${share_file}")"
operator_file="${state_dir}/operator-token"
if [[ ! -s "${operator_file}" ]]; then
  umask 077
  openssl rand -hex 32 >"${operator_file}"
fi
operator_token="$(<"${operator_file}")"

# Reconcile bounds storage-only upload failures after a grace period far longer
# than the request/write timeouts. Extend its tenant list before provisioning a
# new Vault tenant.
exec env \
  APP_ADDR=127.0.0.1:18081 \
  APP_LOG_LEVEL=info \
  APP_WRITE_TIMEOUT=600 \
  DB_DRIVER=postgres \
  DB_DSN="${runtime_dsn}" \
  STORAGE_BACKEND=s3 \
  STORAGE_S3_ENDPOINT=http://127.0.0.1:9000 \
  STORAGE_S3_REGION=us-east-1 \
  STORAGE_S3_BUCKET=aero-vault \
  STORAGE_S3_ACCESS_KEY="${minio_user}" \
  STORAGE_S3_SECRET_KEY="${minio_password}" \
  STORAGE_S3_FORCE_PATH_STYLE=true \
  STORAGE_WRITE_TIMEOUT=600 \
  S3_COMPAT_PREFIX=/s3 \
  WEBDAV_PREFIX=/webdav \
  WEBUI_ENABLED=true \
  PROMETHEUS_ENABLED=true \
  AUTH_PRESIGN_SECRET="${presign_secret}" \
  AUTH_KEYS="${operator_token}:*:admin" \
  AUTH_PERSIST_KEYS=true \
  AUTH_KEY_CACHE_TTL_SECONDS=30 \
  AUTH_JWT_ISSUER=https://sso.ywbsd.site \
  AUTH_JWKS_ENDPOINT=https://sso.ywbsd.site/.well-known/jwks.json \
  AUTH_JWKS_AUDIENCE=aero-vault \
  AUTH_JWKS_TENANT_CLAIM=ten \
  AUTH_JWKS_CLIENT_TENANTS=aero-vault:default,YuaX9KzERXbIplgmSCVgDunx:aero-im \
  AUTH_JWKS_DEFAULT_SCOPES=read,write \
  AUTH_OIDC_ISSUER=https://sso.ywbsd.site \
  AUTH_OIDC_CLIENT_ID=aero-vault \
  AUTH_OIDC_REDIRECT_URI=https://source.ywbsd.site/auth/oidc/callback \
  AUTH_OIDC_AUTHORIZATION_ENDPOINT=https://sso.ywbsd.site/auth/login \
  AUTH_OIDC_TOKEN_ENDPOINT=https://sso.ywbsd.site/token \
  AUTH_OIDC_SCOPES=openid,profile,email \
  ACCESS_CONTROL_ENABLED=true \
  ACCESS_DEFAULT_POLICY=tenant \
  ACCESS_SHARE_SECRET="${share_secret}" \
  ACCESS_PUBLIC_BASE_URL=https://source.ywbsd.site \
  AI_INDEX_ENABLED=true \
  AI_EMBED_PROVIDER=hash \
  AI_EMBED_MODEL=hash-32 \
  AI_EMBED_DIM=32 \
  AI_CHAT_PROVIDER=mock \
  AI_HYBRID_SEARCH=true \
  AI_VECTOR_BACKEND=qdrant \
  AI_VECTOR_URL=http://127.0.0.1:6333 \
  AI_VECTOR_COLLECTION=aero_chunks_codex_fullstack_20260729 \
  AI_LEXICAL_BACKEND=pgfts \
  AI_VECTOR_DSN="${runtime_dsn}" \
  EVENTS_TRANSPORT=postgres \
  EVENTS_TRANSPORT_DSN="${runtime_dsn}" \
  RECONCILE_INTERVAL_MINUTES=15 \
  RECONCILE_DELETE_ORPHAN_BLOBS=true \
  RECONCILE_ORPHAN_GRACE_MINUTES=60 \
  RECONCILE_TENANTS="default,admin,aero-im" \
  RECONCILE_CLUSTER_SINGLETON=true \
  JOBS_WORKERS=2 \
  /home/u1/aero-vault/bin/aero-vault
