# Strategic Extensions Analysis

> **Author:** Architecture & Product  
> **Date:** 2026-07-10  
> **Scope:** Global scan — identify 5 high-value extension directions beyond the current feature matrix.  
> **Principle:** Each direction is grounded in concrete codebase gaps, operational pain points observed in production object-storage systems, and market differentiation potential for AeroVault.

---

## Overview

AeroVault has built a remarkably complete foundation: multi-protocol object storage (REST, S3, WebDAV, MCP), AI/RAG pipeline, multi-tenancy, background workers, SSE encryption, and basic lifecycle management. The system is already production-worthy for mid-scale deployments.

The five directions below represent the **next tier of capability** — they address real gaps that appear when scaling from "works in dev" to "enterprise-grade multi-region deployment." Each is ordered by strategic impact.

---

## Direction 1: Multi-Region Active-Active Replication with Conflict Resolution

### Why

Current `replication.Worker` is a one-way, async fan-out from primary to a single replica backend. This is sufficient for DR (disaster recovery) but not for:

- **Geo-distributed active-active** — writes accepted in any region, synced to all others.
- **Low-latency local reads** — users in EU read from EU storage, users in APAC read from APAC.
- **Cross-region consistency** — last-writer-wins (LWW) or CRDT-based merge for concurrent updates.
- **Online migration** — moving from one storage backend to another without downtime.

### Codebase Gap Analysis

| Component | Current State | Gap |
|-----------|--------------|-----|
| `internal/replication/` | Single `replica` backend, triggered by `object.created` events only | No delete-replication, no bidirectional sync |
| `internal/events/bus.go` | Postgres LISTEN/NOTIFY transport wired | No topic-based routing (per-region channels), no ordering guarantees |
| `internal/storage/storage.go` | No metadata about replica region/status | No `storage.Location()` or region-awareness |
| `internal/repository/sql_objects.go` | Single `storage_key` per object | No per-region `storage_keys[]` or version vector |
| `internal/service/file_crud.go` | `Put` writes to one storage backend | No fan-out-or-await-quorum logic |
| `internal/middleware/middleware.go` | No geo-awareness in routing | No region-affinity header parsing |

### Scope

1. **Version vectors** — store a logical clock per object (Lamport or DOT) so concurrent edits can be detected.
2. **Multi-backend registry** — `FileService` manages N storage backends (primary per write-region + replicas).
3. **CRDT or LWW merge** — on sync conflict, deterministic resolution (timestamps + node priority).
4. **Read-affinity middleware** — route GET requests to the nearest replica based on `X-Forwarded-For` or `X-Aero-Region`.
5. **Backfill jobs** — existing objects can be replicated to new regions via JobPool without re-upload.

### Edge Cases

- **Split-brain recovery** — when two regions accept writes during a partition, how to merge after recovery.
- **Tombstones** — deletes in one region must propagate to others (current replicator only handles `EventCreated`).
- **Partial failure** — some replicas succeed, some fail; async retry with idempotency keys.
- **Bandwidth throttling** — cross-region replication at scale can saturate network links.

### Performance Impact

- **Write latency:** Quorum-write (write to 2 of 3 regions before acking client) adds 1–2 RTTs.
- **Storage cost:** 3×+ for triplicated data on S3/OSS/COS.
- **Throughput:** Parallel fan-out with buffered channels, backpressure via `jobs.MaxDepth`.

---

## Direction 2: Intelligent Storage Class Tiering & Automated Lifecycle Transitions

### Why

Current `lifecycle` (`internal/reconcile/lifecycle.go`) only handles time-based expiry with `soft_delete` or `hard_delete`. Real-world object storage **cost optimization** requires multi-tier transitions:

| Tier | Cost/GB | Use Case |
|------|---------|----------|
| STANDARD | ~$0.023 | Hot data, frequent access |
| STANDARD_IA | ~$0.0125 | Infrequent, but immediate read |
| GLACIER_FLEXIBLE | ~$0.004 | Cold, 1–5 min retrieval |
| DEEP_ARCHIVE | ~$0.001 | Frozen, 12 hr retrieval |

Without tier transitions, users are forced to either over-pay for cold data on STANDARD or manually manage a complex migration pipeline.

### Codebase Gap Analysis

| Component | Current State | Gap |
|-----------|--------------|-----|
| `internal/reconcile/lifecycle.go` | Hard/soft delete only | No `STANDARD→STANDARD_IA→GLACIER` transitions |
| `internal/storage/storage.go` | No `StorageClass` enforcement | Backend implementations don't map `StorageClass` to provider tier |
| `internal/service/file_features.go` | `SetBucketLifecycle` stores `ExpireAfterDays` | No `TransitionDays[]` multi-stage rules |
| `internal/service/file_crud.go` | `DefaultStorageClass` only at write time | No re-classification after write |
| `internal/repository/sql_buckets.go` | `bucket_config` has single `expire_after_days` | No `lifecycle_rules` table |
| `internal/repository/migrations/sqlite/0024*` | Last migration — bucket_notifications | No lifecycle-rules migration |

### Scope

1. **Lifecycle rules engine** — parse S3-compatible XML rules `{"ID": "tier", "Filter": {...}, "Transitions": [{"Days": 30, "StorageClass": "STANDARD_IA"}], "Expiration": {...}}`.
2. **Tier transition worker** — `reconcile.Lifecycle` scans objects whose age exceeds a transition threshold, calls `storage.Migrate(ctx, storageKey, srcClass, dstClass)`.
3. **Storage backend migration** — implement `Migrate` on `local` (copy + delete), `s3` (CopyObject with `x-amz-storage-class`), `oss` (copy + delete), `cos` (copy + delete).
4. **Restore from Glacier** — `RestoreObject` triggers `storage.Restore(ctx, storageKey, days)` for temporary warm access.
5. **Size-based rules** — optionally transition based on object size (>128KB → IA, >256MB → Glacier).

### Edge Cases

- **Objects under legal hold or retention lock** — must never be transitioned to a tier where they become unmodifiable.
- **Tier with minimum storage duration** — S3 IA/Glacier charge penalty for deleting before 30/90 days.
- **Partial-bucket transition** — `Filter.Prefix` and `Filter.Tag` must be evaluated correctly.
- **Restore-in-progress tracking** — GET on a Glacier object before restore completes must return `RestoreInProgress` metadata.

### Performance Impact

- **Reconcile cycle time:** Tier transitions touch storage (copy+delete), not just metadata — scan rate per cycle drops from ~500k objects/min to ~50k objects/min.
- **Deletion penalty tracking:** Must log objects deleted before minimum duration so billing systems can apply penalty charges.
- **Concurrent restore limit:** S3 allows up to 100 simultaneous restore requests per account; enforce with a semaphore.

---

## Direction 3: Enterprise IAM / Fine-Grained Access Control Engine

### Why

Current auth (`internal/auth/policy.go`) supports a basic IAM-style bucket policy document with limited action sets and no condition keys. For enterprise adoption:

- **Condition keys** — `aws:SourceIp`, `aws:CurrentTime`, `aws:MultiFactorAuthAge`, `aws:VpcSourceIp`.
- **Resource-level granularity** — deny GET on `/secrets/*` but allow on `/public/*`.
- **Group/Role-based** — assign roles to tenants/users, not just flat API keys.
- **STS / Temporary Credentials** — time-limited session tokens for federation (OIDC, SAML).
- **Policy evaluation engine** — full DENY-override-ALLOW semantics, not-allowed implies deny.

### Codebase Gap Analysis

| Component | Current State | Gap |
|-----------|--------------|-----|
| `internal/auth/policy.go` | `ParsePolicy` supports basic action+effect+principal | No condition block parser, no resource ARN matching |
| `internal/auth/policy_test.go` | Tested with simple allow/deny | No condition-evaluation tests |
| `internal/auth/auth_middleware.go` | `authReg.Middleware()` checks key→tenant only | No per-request policy evaluation |
| `internal/auth/store.go` | Key persistence (hash→tenant+scopes) | No role/group storage |
| `internal/api/rest/handler.go` | No per-path authorization | Every handler uses `mw.TenantFrom` only |
| `internal/api/s3compat/handler.go` | `checkBucketPolicy` called per action | No resource-subresource granularity, no `x-amz-expected-bucket-owner` enforcement |
| `internal/repository/migrations/sqlite/` | 24 migrations — no `roles`, `policies`, `sessions` tables | — |

### Scope

1. **Condition expression parser** — `{ "IpAddress": { "aws:SourceIp": ["10.0.0.0/8"] }, "Bool": { "aws:SecureTransport": "true" } }`.
2. **Resource ARN format** — `arn:aero:tenant:default:bucket:my-bucket/*` with wildcard support.
3. **Policy evaluation engine** — for a given `(principal, action, resource, context)`, evaluate all matching policies and return `(allow|deny, matched_rule)`.
4. **Session/token service** — `POST /v1/admin/sts` returns a short-lived token bound to a specific policy and context (IP, time, scope).
5. **OIDC / SAML federation** — exchange an external JWT/SAML assertion for an AeroVault session token.

### Edge Cases

- **Policy size limit** — AWS caps at 20 KB per policy. Enforce the same.
- **Permission boundary** — prevent privilege escalation: even an admin cannot grant broader permissions than they hold.
- **Deny priority** — an explicit `Deny` in any matching policy must override all `Allow` statements.
- **NotAction evaluation** — `"NotAction": "s3:DeleteObject"` = allow everything *except* delete.
- **Permission propagation delay** — policy changes must be applied within seconds (cache invalidation via Postgres NOTIFY).

### Performance Impact

- **Policy evaluation latency:** Per-request overhead of ~50–100µs with a well-compiled DAG evaluator. Use a trie-based resource matcher to avoid O(n) scan of all policies.
- **Session token validation:** HMAC-signed tokens require no DB round-trip on each request (self-contained).
- **Cache pressure:** Each unique `(principal, action, resource)` combination may be cached — bounded LRU with TTL.

---

## Direction 4: Data Governance & Compliance Suite

### Why

Current governance features are scattered: `PIIDetector` (PII scan), `scrub` (integrity check), `retention` (soft-delete purge), `object lock`. Regulated industries (healthcare/HIPAA, finance/SOX, legal/eDiscovery) require a **unified compliance framework**:

| Requirement | Current | Target |
|-------------|---------|--------|
| Legal hold | `_aero_legal_hold=ON` metadata flag | Multi-hold, with event source and duration |
| Retention events | Not present | Event-driven holds (e.g. "litigation hold" activates on a corporate event) |
| Classification taxonomy | Not present | Label objects `confidential`, `pii`, `public` with propagation rules |
| Chain of custody | `audit_log` for admin ops | Per-object full access trail: every GET/HEAD/PUT with principal, IP, timestamp |
| Retention policy simulation | Not present | Dry-run `?retention-simulate` on a bucket to predict which objects expire when |
| GDPR erase workflow | Not present | Identify all objects containing a user's PII via search, queue erase with confirmation |
| Data export (portability) | Snapshot (full backup) | Per-tenant/per-bucket export in standard format (ZIP/Parquet) |

### Codebase Gap Analysis

| Component | Current State | Gap |
|-----------|--------------|-----|
| `internal/ai/pii.go` | `PIIDetector` — regex-based scan | No taxonomy engine, no classification labels stored in chunk metadata |
| `internal/reconcile/scrub.go` | Only MD5 integrity check | No classification sync (stale labels) |
| `internal/reconcile/retention.go` | Time-based soft-delete purge | No event-driven hold |
| `internal/repository/audit.go` | `audit_log` for admin operations only | No per-object access audit |
| `internal/service/file_crud.go` | `checkCorrupt` / `LockedUntil` | No multi-hold model |
| `internal/service/file_features.go` | `LockObject` sets single `LockedUntil` | No `AddLegalHold` / `RemoveLegalHold` with audit trail |
| `internal/api/rest/admin.go` | Admin handler — jobs, tenants, keys | No `GET /v1/admin/compliance/holds`, `POST .../export`, `POST .../gdpr-erase` |

### Scope

1. **Classification taxonomy engine** — define labels (`confidential`, `pii`, `restricted`), auto-classify via content analysis (PII scan, keyword), propagate labels to child chunks.
2. **Multi-hold legal hold manager** — `POST /v1/admin/legal-holds` creates a hold with scope (tenant/bucket/prefix/tag), any object matching gets metadata `_aero_hold:<hold_id>=<source>`; hold blocks both soft and hard delete.
3. **Event-driven retention** — `PUT /v1/buckets/{bucket}/retention-events` defines rules like "when an object is tagged `case-closed`, start a 7-year retention clock."
4. **Per-object access trail** — `internal/repository/access_log.go`: log every GET/HEAD/PUT/DELETE to a separate `access_log` table (partitioned by tenant or time). Expose via `GET /v1/lineage/objects/{id}/access`.
5. **GDPR erasure pipeline** — search for all chunks containing a PII pattern → queue `delete` jobs → report completion. Respect retention holds (cannot erase objects under hold).
6. **Compliance dry-run** — `GET /v1/buckets/{bucket}/compliance-summary?simulate=true` returns projected expiry/transition dates for all objects under current policies.

### Edge Cases

- **Conflicting holds** — an object under both "litigation hold" (indefinite) and "7-year regulatory retention" must expire at max(litigation, regulatory).
- **Classification escalation** — a user tags an object `public`, but the system's content analysis detects PII → deny/override and alert.
- **Erase vs. hold conflict** — GDPR erase request for a user whose data is under legal hold → log exception, surface to compliance officer.
- **Time-of-check to time-of-use** — a hold is applied after an object is read but before it's deleted; delete must be re-authorized.

### Performance Impact

- **Access audit log insert:** Every GET/HEAD adds a synchronous DB write → batch-write with 1s flush window or use a separate Kafka/SQS pipeline.
- **Classification at index time:** `Indexer` already calls `PIIDetector`; extending to taxonomy labels adds ~5ms per chunk.
- **Hold evaluation:** Checking N holds on every delete is O(N); index holds by `(tenant, bucket, prefix, tag)`.

---

## Direction 5: Observability & Operations Platform — SLI/SLO, Distributed Tracing, Cost Analysis

### Why

Current observability (`internal/telemetry/`) provides Prometheus metrics and OTel instrumentation but lacks:

- **SLO tracking** — "P99 GET latency < 200ms over a 30-day rolling window."
- **Distributed tracing** — when a request flows through middleware → `FileService` → storage + events + worker, which component is slow?
- **Per-tenant cost allocation** — "Tenant `acme` consumed 15 million AI tokens and 500 GB of storage this month."
- **Structured access logs** — S3 server access log format (CSV with requestor, action, bytes, latency).
- **Anomaly detection** — alert when embedding latency spikes 3σ above baseline.

### Codebase Gap Analysis

| Component | Current State | Gap |
|-----------|--------------|-----|
| `internal/telemetry/metrics.go` | Counters + histograms (latency, errors) | No SLO burn-rate tracking, no multi-window alert evaluation |
| `internal/telemetry/otel.go` | Basic OTel setup | No span propagation across goroutines, no `traceparent` extraction from incoming requests |
| `internal/telemetry/http.go` | HTTP middleware recording duration | No per-path/tenant/host breakdown in span attributes |
| `internal/service/file_crud.go` | No spans at all | `Put` / `Get` / `Delete` have no child spans for storage vs repo |
| `internal/middleware/middleware.go` | RequestID + tenant on context | No trace ID propagation, no baggage |
| `internal/ai/search.go` | `telemetry.RecordSearchLatency` | No span tree: embed → retrieve → rerank |
| `internal/ai/cost.go` | Per-query AI cost tracking | No monthly/tenant aggregation, no billing export |
| `internal/repository/ai_usage_cost_test.go` | Unit test | No `SUM(tokens) GROUP BY tenant, date` query pattern |
| `deploy/grafana/` | 12-panel dashboard | No SLO burn-rate panels, no per-tenant cost breakdown |

### Scope

1. **Distributed tracing with OpenTelemetry** — propagate `traceparent` from HTTP requests down through `FileService` → `storage.Backend` → `repository.Repository` → `events.Bus` → `jobs.Pool`. Each async worker (antivirus, replication, indexer) gets its own span that references the originating trace.
2. **SLO engine** — define service-level objectives via config: `SLO_GET_LATENCY_P99=200ms` and `SLO_AI_SEARCH_P95=500ms`. Burn-rate counters alert when error budget is consumed too fast (`alert: HighErrorBudgetBurn`).
3. **Per-tenant cost allocation** — `internal/telemetry/cost.go`: aggregate AI token usage (from `ai_usage_cost`), storage bytes (from `tenant_quotas`), request volume (from `metrics`), and export as a monthly CSV to `GET /v1/admin/billing/{tenant}/{year}/{month}`.
4. **Structured access log** — implement S3 server access log spec: `[bucket] [requester] [time] [action] [key] [bytes] [status] [latency]`. Write to a separate logging backend (file, or a designated bucket via `SetBucketLogging` target).
5. **Anomaly detection middleware** — wrap AI endpoint handlers with latency prediction; when actual exceeds 3× predicted, emit a `WARN` log and increment `ai_latency_anomaly_total`.

### Edge Cases

- **Trace sampling rate** — 100% tracing for AI endpoints (low volume) vs. 1% for object CRUD (high volume).
- **SLO multi-window** — use both 5-minute and 30-minute burn-rate windows to detect fast vs. slow consumption.
- **Cross-service trace correlation** — if a single request triggers an indexer job (async), link the job's trace to the PUT's trace via `traceparent` in the jobs table.
- **Cost allocation for shared resources** — vector index CPU in Qdrant is shared across tenants; approximate allocation by query volume per tenant.

### Performance Impact

- **Span overhead:** ~1µs per span creation + export batching (default 1s batch). Negligible for <1000 req/s.
- **Access log IO:** S3-style access logs add ~200 bytes per request. At 10k req/s, that's 2 MB/s of logging — use a dedicated writer goroutine + compressed output.
- **SLO evaluation:** Counter-based, no per-request storage; O(1) CPU for burn-rate calculation.

---

## Summary: Strategic Priority Matrix

| Direction | User Value | Effort | Risk | Time to ROI | Dependencies |
|-----------|-----------|--------|------|-------------|-------------|
| 1. Multi-Region Active-Active | ★★★★★ | 8 weeks | High (data loss risk) | 6 months | New: CRDT library, multi-storage registry |
| 2. Storage Class Tiering | ★★★★☆ | 4 weeks | Medium (data movement risk) | 3 months | `storage.Migrate` interface change |
| 3. Enterprise IAM | ★★★★★ | 8 weeks | Medium (security critical) | 6 months | New: policy engine, STS, OIDC lib |
| 4. Compliance Suite | ★★★★☆ | 6 weeks | Low (additive features) | 4 months | New: compliance tables, hold manager |
| 5. Observability Platform | ★★★☆☆ | 4 weeks | Low (no data loss) | 2 months | OTel SDK, Grafana operator |

### Recommended Sequencing

```
Phase 1 (Weeks 1–4): Direction 5 (Observability) + Direction 2 (Tiering)
  → Quick wins, immediate production value, low risk.

Phase 2 (Weeks 5–10): Direction 4 (Compliance) + Direction 1 (Multi-Region) design
  → Compliance is additive; Active-Active needs careful design.

Phase 3 (Weeks 11–18): Direction 1 (Multi-Region) implementation + Direction 3 (IAM) design
  → Two high-effort, high-value tracks in parallel.

Phase 4 (Weeks 19–24): Direction 3 (IAM) implementation
  → Last but most complex; benefits from insights gained in earlier phases.
```

---

## Appendix: Cross-Cutting Concerns

### Storage Interface Extension

Directions 1, 2, and 3 all require additions to `storage.Storage`:

```go
type Storage interface {
    // Existing...
    Migrate(ctx, storageKey, srcClass, dstClass string) (ObjectInfo, error)
    Restore(ctx, storageKey string, days int) error
    Location() string // e.g. "us-east-1" for geo-awareness
}
```

### New Repository Tables

| Table | Purpose | Direction |
|-------|---------|-----------|
| `lifecycle_rules` | Per-bucket multi-stage transition rules | 2 |
| `access_log` | Per-object access trail | 4 |
| `legal_holds` | Multi-hold scope definitions | 4 |
| `compliance_labels` | Object classification labels | 4 |
| `sts_sessions` | Temporary credential sessions | 3 |
| `replication_config` | Per-bucket replication rules (regions, filters) | 1 |

### Existing Config Surface Already Prepared

Notably, the config framework (`internal/config/config.go`) and env template (`.env.example`) are already designed for extensibility — many `AUTH_*`, `REPLICATION_*`, and `STORAGE_*` keys have reserved or partially-implemented entries that these directions would fully wire.

---

*End of document.*
