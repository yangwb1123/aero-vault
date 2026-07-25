# Strategic Extension Directions — AeroVault v0.1

> **Author:** AI Architect · **Date:** 2026-07-11  
> **Scope:** Global codebase scan (298+ Go files, 48 schema migrations, 4 protocol adapters, AI pipeline, Helm/k8s deploy)  
> **Purpose:** Identify 4 high-value, strategically differentiated extension directions that build on existing foundations and solve real enterprise gaps.

---

## Executive Summary

AeroVault already delivers an impressive breadth: S3-compatible object storage, multi-tenancy, AI/RAG pipeline, event-driven webhooks, SSE encryption, replication, antivirus, versioning, WORM, lifecycle, and a full admin surface. However, several critical gaps separate it from being a **production-grade enterprise platform**. The four directions below represent the highest leverage investments — each one transforms a missing capability into a durable competitive advantage while reusing existing code paths.

| # | Direction | Strategic Impact | Existing Foundation |
|---|-----------|-----------------|-------------------|
| 1 | **Cold Storage Tiering & Lifecycle Transitions** | Cost arbitrage — without it, all data sits on hot storage, killing economics for archival use cases | `storage_class` field (mig 0021), `reconcile/lifecycle.go`, restore path |
| 2 | **Object Lock Compliance & SSE-C Encryption** | Regulatory gate — financial/healthcare customers require GOVERNANCE/COMPLIANCE modes and customer-managed keys | Object lock WORM path, SSE-KMS infrastructure, `_aero_legal_hold` metadata |
| 3 | **True Event Notification Pipeline (SQS/SNS/Lambda Delivery)** | Enterprise integration — current webhook-only delivery is insufficient for event-driven architectures | Notification rules in schema, event bus, webhook retry infrastructure |
| 4 | **AI-Native Metadata Pipeline (Auto-Tagging, Classification, Summarization)** | Product differentiator — transforms storage into an intelligent data platform; no competitor offers this in-band | Indexer pipeline, LLM integration, PII detector, extractor seam, metadata system |

---

## Direction 1: Cold Storage Tiering & Lifecycle Transitions

### Current State

The schema already tracks `storage_class` per object (migration 0021, `repository.Object.StorageClass`). Objects are created with a default class (typically `STANDARD`), and the lifecycle engine (`reconcile/lifecycle.go`) can expire objects after N days — but **only to deletion** (soft or hard). There is no automated transition between storage classes.

The `restoreObject` handler in both S3-compat (`handler.go:880`) and REST (`handler.go:618`) only restores soft-deleted objects — it has **no awareness of data archived to GLACIER/DEEP_ARCHIVE** and no restore workflow (expiration window, tier, polling).

### What's Missing

| Gap | Impact |
|-----|--------|
| `x-amz-storage-class` on PUT → `STANDARD_IA`, `ONEZONE_IA`, `INTELLIGENT_TIERING`, `GLACIER`, `DEEP_ARCHIVE` | Users must use the same hot storage for all data |
| Lifecycle rule to transition: `STANDARD → STANDARD_IA` after N days | No automatic cost optimization |
| Lifecycle rule to transition: `STANDARD_IA → GLACIER` after N days | Cold data never moves to cheap storage |
| Lifecycle rule to transition: `GLACIER → DEEP_ARCHIVE` after N months | Long-term retention costs are punitive |
| S3 `POST /restore` with `<RestoreRequest><Days>...</Days></RestoreRequest>` | No restore-from-archive workflow |
| Temporary copy-on-restore with expiry | Restored objects compete for hot storage |
| `x-amz-restore` response header on HEAD/GET | Clients can't poll restoration status |
| `StorageClassAnalysis` dashboard (which classes consume what) | Ops teams can't track cost allocation |

### Why It's Needed

In real deployments, 60–80% of stored data is accessed less than once per quarter. Without lifecycle transitions, every byte sits on the fastest (most expensive) tier. For archival/backup use cases — a primary S3 adoption driver — this makes the product uneconomical at scale. Every major S3-compatible platform (MinIO, Ceph, AWS itself) supports this.

### Building Blocks Already Exist

- `reconcile/lifecycle.go` — periodic sweep engine, reads `expire_after_days` from bucket config
- `storage_class` field — fully migrated, read/written through the entire stack
- `service.GetBucketLifecycle` / `service.SetBucketLifecycle` — API surface already present
- `storage.Storage` interface — new backends can implement a `Tier` or `Restore` method
- `Object.Metadata["_aero_restore_status"]` pattern (similar to `_aero_scrub_status`) for tracking restoration

### Implementation Sketch

1. **Extend lifecycle rules** to include `Transitions[]` alongside `Expiration`:
   ```json
   {"rules": [{"id": "archive", "days": 90, "transition": "GLACIER"}]}
   ```
2. **Add `Storage.Tier(ctx, key, targetClass)` and `Storage.Restore(ctx, key, days)`** to the backend interface — local can be a no-op; S3/OSS/COS use native APIs.
3. **Add `JobRestoreFromCold`** to the job pool for async archive retrieval.
4. **Expose S3-compliant `POST ?restore`** with XML request body (`Days`, `Tier`).
5. **Add storage-class distribution metrics** to `telemetry/prometheus.go`.

---

## Direction 2: Object Lock Governance Mode & SSE-C Encryption

### Current State

Object lock exists as a simple `locked_until` timestamp on the `objects` row. The S3-compat handler reads `x-amz-object-lock-legal-hold: ON` and stores it as `_aero_legal_hold` metadata. The PUT `?legal-hold` and `?retention` sub-resources are not parsed. The bucket-level object lock defaults to `GOVERNANCE` mode (hardcoded in `bucketconfig.go:183`) but the **mode is never stored or enforced**.

SSE-C (customer-provided encryption keys) is completely absent — only SSE-S3 (keyfile) and SSE-KMS (HTTP KMS) exist. Compliance frameworks like PCI-DSS, HIPAA, and FedRAMP often require customer-managed keys.

### What's Missing

| Gap | Impact |
|-----|--------|
| `PUT /{key}?legal-hold` with `<LegalHold><Status>ON</Status></LegalHold>` | No legal hold API — only header-based, non-standard |
| `GET /{key}?legal-hold` returning legal hold status | Clients can't audit hold state |
| `PUT /{key}?retention` with `<Retention><Mode>GOVERNANCE|COMPLIANCE</Mode><RetainUntilDate>...</RetainUntilDate></Retention>` | No per-object retention API |
| Governance bypass — `x-amz-bypass-governance-retention:true` header with appropriate scope | Admins can't override GOVERNANCE locks for legal discovery |
| `locked_until` split into `retain_until + retention_mode` | Can't distinguish GOVERNANCE vs COMPLIANCE enforcement |
| SSE-C: `x-amz-server-side-encryption-customer-algorithm: AES256` + key headers | No customer-managed key option |
| SSE-C: `x-amz-copy-source-server-side-encryption-customer-*` for CopyObject | Can't copy SSE-C objects |
| SSE-C: multipart upload with customer keys | Can't upload large objects with SSE-C |

### Why It's Needed

Object lock with governance/compliance modes is **not optional** for regulated industries:
- **SEC 17a-4** requires COMPLIANCE-mode WORM for electronic records
- **FINRA** requires similar protections
- **HIPAA** and **GDPR** benefit from governance-mode retention for data lifecycle management

SSE-C unlocks:
- Customers who want to manage their own keys without running a KMS
- Air-gapped / classified environments
- Multi-tenant isolation requirements where the platform operator must not have access to plaintext

### Building Blocks Already Exist

- `repository.Object.LockedUntil` and `service.LockObject` — core WORM path
- `SetLockedUntil` in both SQLite and Postgres dialects
- `file_crud.go:hardDeleteObject` checks locked_until and `_aero_legal_hold`
- SSE-KMS infrastructure (`kms.go`, `encrypt.go`, `rewrap.go`) — envelope encryption pattern
- `storage.PutOptions` can carry encryption context
- The S3-compat handler already parses `x-amz-object-lock-*` headers on PUT

### Implementation Sketch

1. **Schema**: Add `retention_mode TEXT` column to `objects` (nullable, `"GOVERNANCE"`/`"COMPLIANCE"`) and migrate. Add bucket-level `default_retention_mode`.
2. **Repository**: `SetObjectRetention(ctx, tenant, bucket, key, mode string, until time.Time)` with mode enforcement — COMPLIANCE blocks all deletes including admin; GOVERNANCE allows bypass with `SkipGovernance()` scope.
3. **Service**: `LockObject` gains mode parameter; `hardDeleteObject` checks mode before blocking.
4. **SSE-C**: New `storage.SSECConfig` with per-request key material; `PutOptions`/`GetOptions` carry the customer key. The `storage.Storage` interface gains `SSECapable() bool`. Implement for local (pass-through) and S3 (native headers).
5. **S3 API**: Add `?legal-hold` and `?retention` sub-resource handlers; add `x-amz-bypass-governance-retention` support in `DeleteObject`.

---

## Direction 3: True Event Notification Pipeline (SQS/SNS/Lambda Delivery)

### Current State

The event system (`internal/events/bus.go`) publishes lifecycle events (`created`, `deleted`, `accessed`) to in-process subscribers and optionally to a webhook URL via HMAC-signed HTTP POST with durable retry (`webhook_failures` table).

The schema already stores `NotificationRule` with `QueueARN`, `TopicARN`, and `LambdaARN` fields (from S3-compat XML parsing — `bucketconfig.go` has the XML marshalling). Bucket notification rules can be set and retrieved. **However, the delivery layer does not actually connect to SQS, SNS, or Lambda.** The ARN fields are persisted but never read by any delivery component.

### What's Missing

| Gap | Impact |
|-----|--------|
| SQS queue delivery with proper ARN parsing | No async queue-based integration |
| SNS topic delivery with subscription filtering | No pub/sub fan-out to multiple consumers |
| Lambda invocation via AWS SDK or generic HTTP | No serverless compute trigger |
| Dead-letter queue (DLQ) for failed notifications | Production reliability gap |
| Event filtering by object key prefix/suffix (already stored in `FilterKey`) | Notifications fire for every object indiscriminately |
| Batch event delivery (SQS batch of up to 10) | High-volume cost/performance |

### Why It's Needed

The true value of S3-compatible storage in modern architectures is **event-driven data pipelines**:
- Ingest a CSV → trigger a Lambda to parse it → write to database
- Upload an image → SNS fan-out to thumbnail service + CDN invalidation + audit log
- Delete a document → SQS message to downstream systems for cache invalidation
- Without these, the storage platform is a silo, not a platform

The webhook-only approach also creates a single point of failure and lacks the at-least-once delivery guarantees that queue-based architectures provide.

### Building Blocks Already Exist

- `events.Bus` with `Publish` → `Subscribe` pattern — already handles fan-out
- `NotificationRule` schema — already stores ARNs, filter keys, event types
- S3-compat XML parsing for `putBucketNotifications` (`handler.go:809`)
- `webhook_failures` retry table — retry infrastructure exists
- Repository event persistence (`events` table, `NextUnconsumedEvents`)

### Implementation Sketch

1. **New package `internal/notifications`** with three delivery plugins:
   - `sqsDelivery` — uses AWS SDK to send messages to SQS (or generic HTTP for self-hosted)
   - `snsDelivery` — publishes to SNS topics
   - `lambdaDelivery` — invokes Lambda functions (or generic HTTP endpoint)
2. **Each plugin implements**:
   ```go
   type DeliveryPlugin interface {
     Deliver(ctx context.Context, rule NotificationRule, event Event) error
   }
   ```
3. **Router** in `backgroundWorkers` that on startup, loads all bucket notification configs, matches events to rules, and dispatches through the appropriate plugin.
4. **Event filtering** already in schema (`FilterKey`) — implement prefix/suffix matching before delivery.
5. **Dead-letter**: extend `webhook_failures` pattern or use a dedicated DLQ table for undeliverable notifications.
6. **Configuration**: `EVENTS_SQS_ENABLED`, `EVENTS_SNS_ENABLED`, `EVENTS_LAMBDA_ENABLED` flags (opt-in).

---

## Direction 4: AI-Native Metadata Pipeline (Auto-Tagging, Classification, Summarization)

### Current State

The AI pipeline (`internal/ai/indexer.go`) extracts text, chunks it, embeds it, and indexes it for semantic search. PII detection is optional. The LLM powers RAG chat/agent but is **never invoked during indexing**. Metadata is entirely user-supplied — the platform stores whatever `x-amz-meta-*` headers the caller sends, and the extractor only produces plain text for chunking.

There is no:
- Automatic content classification (document type, sentiment, language, topic)
- Automatic tag generation from content
- Content summarization / description generation at ingest
- Custom extraction schemas ("extract invoice number, date, total from PDFs in bucket X")
- Multimodal understanding (image description, OCR text extraction)

### What's Missing

| Gap | Impact |
|-----|--------|
| LLM-based auto-tagging on index (classify → store as object tags) | Objects remain untagged; tag-based search/filtering is underutilized |
| Document summarization pipeline (extract → summarize → store as `_aero_summary` metadata) | Chat/Agent search returns raw chunks without contextual summaries |
| Custom extraction schemas (user defines JSONPath + LLM prompt per bucket prefix) | Can't extract structured data from unstructured documents |
| Image understanding (captioning, OCR, diagram parsing) | Images are invisible to the search pipeline |
| Automated content-type detection and reclassification | Uploaded files with wrong MIME types never get corrected |
| Async "enrichment" job queue (separate from indexer, lower priority) | Indexing latency increases when enrichment is slow |
| Enriched metadata surfaced in search results + lineage | Users can't see generated metadata in the UI or API |

### Why It's Needed

This is the **product's core differentiator**. Every S3-compatible store offers storage. Only AeroVault has a built-in AI pipeline. But the current pipeline treats AI as a retrieval feature, not a metadata enrichment engine. By making the LLM a first-class citizen of the **ingest path**, we transform the platform from "object storage with search" into an **intelligent data platform** that:

- Automatically categorizes and tags uploaded documents (reducing manual metadata effort by 80%+)
- Generates searchable summaries so users find the right document before opening it
- Extracts structured data (invoice amounts, contract dates, customer names) from unstructured content without external tools
- Enables policy-based actions: "auto-delete documents classified as 'draft' after 30 days"

No other S3-compatible product offers this in-band. MinIO, Ceph, and others focus purely on storage performance. This is AeroVault's wedge.

### Building Blocks Already Exist

- `ai.Indexer` with extensible pipeline (extractor → chunker → embedder → sink)
- `ai.Extractor` interface with `RemoteExtractor` and `DefaultExtractor` — already composable via decorator pattern
- `ai.NewDefaultExtractor()` — can add LLM enrichment as another step
- `ai.LLM` interface (`Chat`, `ChatStream`) — ready for classification/summarization calls
- `ai.PIIDetector` — precedent for in-pipeline analysis
- Object `Tags` and `Metadata` — enrichment targets already exist
- `repository.SetObjectMetaKey` — atomic metadata updates without rewriting the whole row
- `telemetry.IncIndexerSkip` — precedent for non-fatal pipeline telemetry

### Implementation Sketch

1. **New enrichment step in the indexer pipeline** (after extraction, before chunking):
   ```go
   type IndexEnricher interface {
     Enrich(ctx context.Context, text string, obj Object) (*Enrichment, error)
   }
   type Enrichment struct {
     Tags       map[string]string
     Summary    string
     Classifications []string
     Custom     map[string]string // user-defined extraction results
   }
   ```
2. **Built-in enrichers**:
   - `LLMClassifier` — prompts LLM: "Classify this document into one of: [user-defined labels]. Output a single word."
   - `LLMSummarizer` — prompts LLM: "Summarize this document in 1-2 sentences."
   - `LLMTagger` — prompts LLM: "Extract 3-5 keywords as comma-separated tags."
   - `CustomExtractor` — user-provided JSON schema + prompt template per bucket prefix
3. **Async enrichment queue** — separate from the main indexer `jobQueue`; enrichment failures don't block indexing
4. **Configuration per bucket**: `AI_ENRICHMENT_ENABLED`, bucket-level enrichment rules in `BucketConfig`
5. **SDK exposure**: `ai_enrichment_total{enricher}` counter, latency histograms
6. **UI/API**:
   - Search results show `summary` and `auto_tags` alongside chunks
   - `/v1/lineage/objects/{id}` includes enrichment metadata
   - Bucket config API: `PUT /v1/buckets/{bucket}/enrichment` for custom schemas

---

## Cross-Cutting Considerations

### Performance & Scalability

| Direction | Concern | Mitigation |
|-----------|---------|------------|
| Cold Storage | GLACIER restore latency (minutes to hours) | Async job pool with polling endpoint (similar to existing `admin/jobs/{id}`) |
| Notifications | High event throughput could overwhelm delivery plugins | Bounded delivery channel + backpressure (existing `Jobs.MaxDepth` pattern) |
| AI Enrichment | LLM calls during indexing increase write latency | Separate enrichment queue; indexer proceeds without waiting for enrichment |
| SSE-C | Per-request key derivation costs | Reuse key-wrapping pattern from SSE-KMS; cache derived keys per object |

### Security

| Direction | Implication |
|-----------|-------------|
| Object Lock (COMPLIANCE) | Even root/admin cannot delete — needs immutability enforcement at storage layer |
| SSE-C | Never log the customer key; key exists only in memory during request |
| Notifications (Lambda) | Need to validate Lambda ARNs; prevent unauthorized invocation |
| AI Enrichment | LLM may see sensitive data before PII redaction — ensure enrichment order is `extract → PII → enrichment → chunk` |

### Observability

Every direction should expose Prometheus metrics:
- `cold_restore_requests_total`, `cold_restore_latency_seconds` (Direction 1)
- `sse_c_requests_total` (Direction 2)
- `notification_delivery_total{target=“sqs|sns|lambda|webhook”, status=“ok|fail”}` (Direction 3)
- `ai_enrichment_total{enricher=“classifier|summarizer|tagger”, status=“ok|fail”}` (Direction 4)

### Migration Strategy

All four directions share a common deployment pattern:
1. **Schema expansion** — additive migrations (new columns, new tables)
2. **Opt-in configuration** — feature flags default to `false`, preserving existing behavior
3. **Graceful degradation** — nil/disabled components don't affect the core CRUD path (per I5)
4. **Backward-compatible API** — new S3 sub-resources don't break existing clients

---

## Appendix: Code Paths Referenced

| Component | Key Files |
|-----------|-----------|
| Storage backends | `internal/storage/storage.go`, `local.go`, `s3.go`, `oss.go`, `cos.go` |
| SSE encryption | `internal/storage/encrypt.go`, `kms.go`, `rewrap.go` |
| Lifecycle engine | `internal/reconcile/lifecycle.go` |
| Object lock/WORM | `internal/service/file_crud.go` (checkLockBeforeOverwrite, hardDeleteObject) |
| Event bus | `internal/events/bus.go` |
| Webhook retry | `internal/events/webhook.go`, `internal/repository/webhook_failures.go` |
| Notification rules | `internal/repository/sql_buckets.go` (GetBucketNotifications, SetBucketNotifications) |
| AI pipeline | `internal/ai/indexer.go`, `extractor.go`, `chunker.go`, `embedder.go` |
| LLM integration | `internal/ai/llm.go`, `chat.go`, `agent.go` |
| PII detection | `internal/ai/pii.go` |
| Job pool | `internal/jobs/jobs.go` |
| Telemetry | `internal/telemetry/prometheus.go`, `metrics.go` |

---

*This document is a strategic analysis, not a implementation plan. Each direction should be broken into a separate ADR with detailed design decisions, migration scripts, and test plans before coding begins.*
