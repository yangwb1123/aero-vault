# aero-vault — full-stack RAG demo

One command brings up the whole platform with a local LLM, S3-compatible
storage, a Postgres metadata DB, and an OpenTelemetry collector.

```bash
docker compose -f deploy/docker-compose.demo.yml up --build
```

| Service        | Role                                   | URL / Port                          |
|----------------|----------------------------------------|-------------------------------------|
| app            | aero-vault server                      | http://localhost:8080 (`/docs`)     |
| postgres       | object metadata                        | localhost:5432 (aero/aero)          |
| minio          | S3-compatible object storage           | http://localhost:9001 (console)     |
| ollama         | local embeddings + chat (OpenAI API)   | http://localhost:11434              |
| otel-collector | traces + metrics sink                  | :4317 (gRPC), :4318 (HTTP), :8889   |

The `ollama-init` service pulls the models on first run, so the initial
`up` takes a few minutes (the app waits for it). Default models are
`llama3.2:1b` (chat) and `nomic-embed-text` (embeddings, 768-dim); override
with `DEMO_CHAT_MODEL` / `DEMO_EMBED_MODEL`.

## Try it

```bash
# upload a doc, watch it get indexed, search, and run a RAG chat
./deploy/demo/seed.sh

# or by hand:
curl -X PUT --data-binary @README.md http://localhost:8080/v1/files/readme.md
curl -X POST http://localhost:8080/v1/search \
  -H 'content-type: application/json' \
  -d '{"query":"object storage","k":5,"mode":"hybrid"}'
curl -X POST http://localhost:8080/v1/chat \
  -H 'content-type: application/json' \
  -d '{"query":"what is this project?","mode":"hybrid"}'

# Python SDK (stdlib-only)
pip install ./sdk/python
python -c "from aero_vault import Client; print(Client().chat('what is aero-vault?').answer)"
```

## How the pieces connect

- **Storage** → the app uses `STORAGE_BACKEND=s3` pointed at MinIO
  (`http://minio:9000`, path-style).
- **Indexing** → each upload emits an event; the indexer enqueues an
  `index_object` job; a worker extracts text, calls Ollama's
  `/v1/embeddings`, and stores chunks. Watch progress at
  `GET /v1/admin/jobs`.
- **Search/Chat** → `/v1/search` fuses vector + BM25; `/v1/chat` retrieves
  context and calls Ollama's `/v1/chat/completions`, returning citations.
- **Telemetry** → OTLP/HTTP to the collector; the collector prints spans and
  re-exports metrics on `:8889`. The app also serves `/metrics` directly.

## Teardown

```bash
docker compose -f deploy/docker-compose.demo.yml down -v   # -v also drops volumes
```
