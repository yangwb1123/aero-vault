#!/usr/bin/env bash
# Seed the running aero-vault demo with a document and exercise the RAG flow:
# upload → (async embed/index via the job queue) → search → chat with citations.
#
# Usage:  ./deploy/demo/seed.sh            # against http://localhost:8080
#         AERO_VAULT_URL=http://host:8080 ./deploy/demo/seed.sh
set -euo pipefail

URL="${AERO_VAULT_URL:-http://localhost:8080}"
KEY="docs/aero-vault-intro.md"

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }

read -r -d '' DOC <<'EOF' || true
# aero-vault

aero-vault is an AI-native file platform. It speaks REST, S3, WebDAV and MCP
over one unified backend. Objects are automatically extracted, chunked and
embedded by a background job queue, then made searchable via hybrid (vector +
BM25) retrieval with reciprocal-rank fusion. A retrieval-augmented chat endpoint
answers questions with inline citations back to the source objects. Storage is
pluggable across local disk and any S3-compatible store (MinIO, OSS, COS), and
every operation is multi-tenant and observable via OpenTelemetry.
EOF

say "Health"
curl -fsS "$URL/healthz"; echo

say "Upload $KEY"
curl -fsS -X PUT --data-binary "$DOC" -H 'Content-Type: text/markdown' \
  "$URL/v1/files/$KEY" >/dev/null
echo "uploaded ($(printf '%s' "$DOC" | wc -c) bytes)"

say "Wait for async indexing (embed via Ollama can take a moment)"
for i in $(seq 1 60); do
  hits=$(curl -fsS -X POST "$URL/v1/search" -H 'Content-Type: application/json' \
    -d '{"query":"how are documents made searchable","k":3,"mode":"hybrid"}')
  if printf '%s' "$hits" | grep -q '"object_key"'; then
    echo "indexed after ${i}s"
    printf '%s\n' "$hits" | python3 -m json.tool 2>/dev/null || printf '%s\n' "$hits"
    break
  fi
  sleep 1
done

say "Job queue status"
curl -fsS "$URL/v1/admin/jobs?limit=5" | python3 -m json.tool 2>/dev/null || true

say "RAG chat"
curl -fsS -X POST "$URL/v1/chat" -H 'Content-Type: application/json' \
  -d '{"query":"What protocols does aero-vault speak and how is search done?","k":4,"mode":"hybrid"}' \
  | python3 -m json.tool 2>/dev/null || true

echo
say "Done — explore the API at $URL/docs"
