# CHANGELOG

All functional changes, in reverse-chronological order. Dates are UTC.

---

## 2026-08-11

### Added
- **Thumbnail REST: 415 for unsupported image types; 400 for invalid `?w=`/`?h=`** (`internal/thumbnail/errors.go`, `internal/api/rest/thumbnail.go`, `internal/api/rest/handler_helpers.go`, `internal/api/rest/specgen.go`, `internal/api/rest/router.go`)
  - `GET /v1/files/{key}/thumbnail` now returns **415 UnsupportedMediaType** for `image/*` content types outside `image/jpeg`/`image/png`/`image/gif` (webp/bmp/avif/tiff and aliases like `image/jpg`, `image/x-png`, `image/jpeg; charset=utf-8`), via the new sentinel `thumbnail.ErrUnsupportedFormat`, with the supported-type list in the message. Previously these surfaced as 400 `InvalidArgument` (a valid image the server cannot decode is a capability matter, not a client argument error). Non-image content types keep the existing 400 "object is not an image" path; corrupt bytes of a whitelisted type stay 400 via the unchanged byte-level `ErrUnsupported`.
  - `?w=`/`?h=` values that are not non-negative integers (`?w=abc`, `?h=-1`, `?w=` empty, overflow) now return **400 InvalidArgument** naming the parameter, validated before the ETag/`If-None-Match` handling and before any decode — previously they were silently ignored and produced a default 256px thumbnail whose garbage-derived ETag polluted shared caches. `?w=0`/absent still means default 256; `> 2048` still clamps server-side.
  - `/openapi.json` documents the thumbnail route's 415 response (`apiRoute.Responses` extension; all other routes' specs are byte-identical).
  - Deliberate compatibility note: objects stored with alias/parameterized image content types whose bytes were decodable previously produced working 200 thumbnails (decoders key off bytes); they now return 415 with the supported list — the declared Content-Type is the contract the gate keys off (no byte sniffing).

---

## 2026-08-10

### Added
- **Thumbnail: progressive (SOF2) JPEG sources capped at `MaxProgressiveSourceDim` 4096** (`internal/thumbnail/thumbnail.go`, `internal/thumbnail/progressive_test.go`)
  - A progressive JPEG at `MaxSourceDim` allocated ~1.1 GiB per request (≈ 4.4 GiB aggregate across the 4 decode slots); SOF2 sources above 4096 are now rejected from the header with the existing `ErrImageTooLarge` → 413, cutting the documented progressive ceiling 4× to ~275 MiB per request (~1.1 GiB aggregate), while all common progressive exports (≤ 2048) and 4K-class sources (≤ 4096) keep working. Baseline (SOF0/SOF1) JPEGs are unaffected and still decode up to `MaxSourceDim`.
  - Detection is a segment-aware header walk over the already-buffered `DecodeConfig` tee (never scans APPn payloads or entropy data, so APP1/EXIF/ICC `0xFF 0xC2` sequences cannot false-positive), gated on `format == "jpeg"` and dims > 4096 so the common small-image path is untouched; the sentinel set, HTTP mapping and error message are unchanged. Fixtures are in-code (the stdlib has no progressive encoder): hand-built header-only SOF2, APP1-padded, and fully decodable progressive JPEGs; `FuzzGenerate` gains the two new seeds.
  - `Generate` now runs `compositeOnWhite(dst)` before `jpeg.Encode`: JPEG has no alpha, and the generic encoder path serializes premultiplied `RGBA()` values verbatim, rendering transparent regions black (or darkened). Fully opaque sources are returned unchanged (the skip is load-bearing, not an optimization); the composite is O(w×h) with a ≤1 MiB allocation pin, origin-safe for sub-images. Half-transparent red now renders pink (255,127,127) instead of darkened red (127,0,0).
  - Gate-condition pins: corrected spec assertion (`g,b<200` — the exact composite value is 127; the original `<60` failed correct code and passed the buggy baseline), semi-transparent no-downscale (ratio≥1) path, composite allocation bound, non-zero-origin `SubImage` fidelity, HTTP-level alpha pixel test. `-race` clean.
- **Thumbnail: decode memory bounds — dimension pre-check, input/metadata caps, package-level decode semaphore** (`internal/thumbnail/thumbnail.go`, `internal/thumbnail/semaphore_test.go`, `Makefile`, `.github/workflows/ci.yml`)
  - `MaxSourceDim` 8192 header pre-check (`ErrImageTooLarge` → 413 before any pixel buffer), `MaxSourceBytes` 128 MiB `LimitReader`, `MaxMetadataBytes` 8 MiB sticky `limitedBuffer` tee (`ErrMetadataTooLarge`, protects the config scan from APPn/COM/XMP flood), and a package-level semaphore capping concurrent `Generate` decode sections at `maxConcurrentDecodes = 4` (aggregate ≈1.1 GiB worst case for PNG RGBA, independent of per-request concurrency settings). Waiters hold only a stream reader and allocate nothing.
  - Deterministic contract pins: `maxConcurrentDecodes == 4` constant pin + absolute 2 GiB live-heap ceiling (a silent 4→8→16 raise now fails tests), blocking-before-`DecodeConfig` / release-on-error / release-on-panic slot tests, 120 s watchdog on the aggregate test (slot leak fails fast instead of a 10-min `go test` timeout), `testing.Short()` skip for the ~1.2 GiB aggregate test, and CI race coverage (`test-race-thumbnail` in `make check`, race step in GitHub Actions).
- **Thumbnail: Go fuzz target over the network-facing decode path** (`internal/thumbnail/thumbnail_test.go`) — `FuzzGenerate` with seeds for header-only PNG, over-`MaxSourceDim` dims, APP1-flooded JPEG, and truncated inputs; nil-coherent error surface, fixture builders typed `testing.TB`.
- **Thumbnail REST dispatch: version-pinned reads never shadowed; over-cap keys fall back** (`internal/api/rest/thumbnail.go`, `internal/api/rest/thumbnail_test.go`)
  - `?version=` on the thumbnail route now delegates unconditionally to `Get` (which resolves the pinned version): a version-pinned read of a soft-deleted object at a key ending in `/thumbnail` served the trimmed key's derived thumbnail (wrong-object content) — now serves the pinned version's own bytes.
  - `Stat` returning `ErrInvalidArgs` (a legal object key whose `/thumbnail` suffix exceeds the 200-char cap, e.g. 191-char keys) now falls through to the subresource interpretation instead of surfacing a 400 regression.
  - Test pins for the full dispatch contract: exact-key bucket-policy Deny → 403 (never the trimmed key's thumbnail); exact-key arm inherits `If-None-Match` 304 and `Range` 206/`Content-Range`; D3 delegation arm reachable (access-enabled authorizer + anonymous public-read harness — anonymous reads of public objects at the full key keep working); 200/201-char full-key fallback.

- **Thumbnail REST: per-object cache directive — `public` only for genuinely anonymous-readable derivations** (`internal/api/rest/thumbnail.go`)
  - The derivation path previously emitted `Cache-Control: public, max-age=86400` unconditionally, so shared caches stored private-object thumbnails that an external anonymous caller could never fetch from origin. The directive is now `private, max-age=86400` unless the request itself was admitted anonymously — `allowAnonymous` admits anonymous readers solely for public-readable objects (object ACL or bucket-ACL fallback), so `public` is safe exactly then; authenticated callers (who may hold private access) get `private`. The 304 mirrors the 200's directive (RFC 9111 §3.2/§3.4) so a revalidating shared cache can never adopt a conflicting directive.
  - Pins: no-auth harness → private; authenticated → private; anonymous + object public-read ACL → public; anonymous + bucket-ACL public-read → public; authenticated on a public object → private; 304 equality both ways.

### Fixed
- **Thumbnail: `TestSemaphoreBlocksBeforeDecodeConfig` leaked a slot-holding goroutine** — the parked `Generate` is now unblocked via a signal reader and its exit awaited before the slots are released, so no slot or goroutine leaks into later tests (the leaked goroutine wedged `TestSemaphoreReleasesOnError` under `-race`).

---

## 2026-08-08

### Added
- **Audit governance: bounded readiness probes + degraded sentinel + `/readyz` degraded payload + cache-fed gauges** (`internal/auditgovernance/runtime.go`, `cmd/server/http.go`, `cmd/server/build.go`, `internal/telemetry/metrics.go`, `deploy/prometheus/alerts.yml`, `deploy/helm/aero-vault/templates/deployment.yaml`, `Makefile`)
  - `Runtime.Ready()`'s two store probes now run under a shared 2s `storeProbeTimeout` (mirroring `readyzProbeTimeout`): a wedged relay store degrades the audit-governance readiness contribution (nil + degraded sentinel, age unknown → 0) instead of hanging `/readyz`; genuine store errors, drain-in-progress and ping/storage failures stay fail-closed 503s. `BacklogAge(ctx)` renamed to `PendingBacklogAge(ctx)`; the freed name is the new zero-I/O cache getter alongside `Degraded()` (single-lock `(degraded, age)` pair writes).
  - `/readyz` gains one new wire form — HTTP 200 `{"ok":true,"degraded":true,"backlog_age_seconds":N}` via the new `degradedChecker` interface + `readinessGroup` OR/max composition (billing contributes false/0); the healthy body `{"ok":true}` stays byte-identical. `repo.Ping` is now bounded by `readyzProbeTimeout` (H2).
  - Both audit gauges (`audit_governance_backlog_age_seconds`, new `audit_governance_degraded` 0/1) are cache-fed (D3): scrapes never block on the store; the run loop refreshes the cache once per poll cycle (freshness ≤ poll interval, independent of `/readyz` traffic).
  - Alert `AuditGovernanceBacklogDegraded` expr becomes `audit_governance_backlog_age_seconds > 450 OR audit_governance_degraded == 1` — the degraded arm closes the F11/F16 wedge alert-silence (a probe timeout records age 0, which alone would starve `for: 10m`; the OR resets only on genuine recovery). Description rewritten (drops `{{ $value }}`, which is the degraded bit when only that arm matches).
  - Helm readinessProbe gains `timeoutSeconds: 10` (k8s's 1s default canceled every ≥2s degraded 200 → NotReady/eviction, defeating the "degrade, never evict" contract; worst case 6s < 10s) and must not carry a `failureThreshold` key (T8 pin). `Makefile test-race-meta` now also races `./internal/auditgovernance/` (the `degradedMu` pair discipline is CI-enforced).
  - Tests: `internal/auditgovernance/runtime_ready_test.go` (T1b degraded sentinel, T1c fail-closed branches + pre-canceled ctx, T5 terminal rows, T6 run-loop refresh with zero `Ready()` calls, F17 wedge run-loop survival, T7 concurrent pair discipline under `-race`); `cmd/server/readyz_drill_test.go` (marker-body update, cache priming + degraded-gauge asserts, wedge drill, OR-arm/`for: 10m`/single-expr alert pins); `cmd/server/http_test.go` (T2 degraded/healthy wire forms, group composition, H2 blocking-Ping, helm probe pin); `internal/telemetry/metrics_test.go` (T4 gauge scrape surfaces).
- **AI read-path degrade: hybrid search falls back to the healthy modality instead of 500** (`internal/ai/search.go`, `internal/telemetry/metrics.go`, `deploy/prometheus/alerts.yml`, `deploy/grafana/aero-vault-ai-ops-dashboard.json`)
  - `mode=hybrid` now collects both halves and degrades on single-modality failure: vector half (embedder / vector backend) down → **200 BM25-only**; lexical half (pgFTS) down → **200 vector-only**; warn log (`embed failed; falling back to lexical results` / `vector index failed; …` / `lexical search failed; …`) + new counter `ai_search_degraded_total{reason∈embed,vector,lexical}`. Pure `vector`/`bm25` modes and both-halves-failed / deadline-fired paths keep surfacing errors (500 unchanged). Chat/Agent inherit the degrade via the shared `search.Query`.
  - **200 responses carry no degrade marker by design** (rerank precedent); the counter is the only visibility contract — new `SearchDegraded` alert (`aero-vault-ai-search` group, `rate(...[5m]) > 0 for 5m`, per-reason labels) plus a 13th AI/Ops dashboard panel (`{{reason}} degraded/s`). AGENTS.md Ops counts corrected to 15 alerts / 6 groups / 13 panels (the 12-vs-14 drift shipped by B3-2 is fixed in the same change).
  - **Deadline semantics:** a fired request deadline never degrades — the failing half's wrapped error (phase preserved, `errors.Is(err, context.DeadlineExceeded)` holds) is returned, bare `ctx.Err()` only in the race window where both halves already succeeded. Note: with `REQUEST_TIMEOUT_SECONDS < 30` the embedder-client-timeout degrade branch is unreachable (deadline always preempts); retrieval-deadline → 500 vs rerank-deadline → 200 raw-order asymmetry is pre-existing and intentional.
  - Tests: `internal/ai/degrade_test.go` (AC-1 embed/lexical degrade + pure-mode pins + classifier/both-fail/deadline/vector-index pins D-AC-6…9), `internal/api/rest/search_ai_test.go` (AC-2 REST seam 200-with-hits + pure-vector 500 negative control), `internal/telemetry/metrics_test.go` (AC-4 labeled scrape pin via new `scrapeValueLabel`).
- **Audit governance: cumulative 300s transient retry cap** (`internal/auditgovernance/relay.go`, `internal/repository/audit_governance_claim.go`, migration `0044_audit_governance_first_attempt_anchor`)
  - A fact failing with **transient-only** errors (previously retried forever with bounded per-attempt backoff) now goes terminal once `now − first_attempt_at_ns > AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` (default `300`, `2..86400`): `failed_at_ns` set, `last_error` retained, never re-claimed, pruned after the delivered-retention window — identical dead-row semantics to the permanent classes.
  - The window anchor (`first_attempt_at_ns`, migration 0044 both dialects, `DEFAULT 0`) is set exactly once inside the fenced claim UPDATE (`CASE WHEN first_attempt_at_ns=0`), so lease re-claims, ack-lost re-claims, and crash recovery preserve the original first-attempt time; retry/fail/complete never reset it.
  - Decision is a pure, strictly-greater comparison (`==` boundary stays transient) with a zero-anchor/negative-elapsed safe direction — an un-anchored row or DB-clock-ahead skew can never trigger premature terminality; the decision is monotone in time, so concurrent claim workers (expired-lease stale holder + current holder) compute the same direction and the fenced writes land exactly one outcome.
  - Config validation now enforces the window floor: `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS >= 2` (existing `<= 86400` cap unchanged); the harness minimum is exactly 2s.
  - **Behavior change for deployments with a stuck transient receiver:** the failure stream dead-letters after the cumulative window instead of retrying indefinitely; `Ready()`/`BacklogAge` clear once the row is terminal (dead rows were already excluded).
  - Tests: `TestCumulativeWindowExceededBoundary`, `TestCumulativeWindowDecisionMonotone`, `TestRuntimeTransientStreamTerminalizesAfterCumulativeWindow`, `TestRuntimeMultiWorkerWindowRaceLandsSingleOutcome` (`internal/auditgovernance/cumulative_window_test.go`); `TestAuditGovernanceFirstAttemptAnchorPersists` (`internal/repository/audit_governance_test.go`); `TestAuditGovernanceCumulativeWindowEnvelope` / `TestAuditGovernanceMaxBackoffDefaultIsCumulativeWindow` (`internal/config/config_audit_governance_test.go`); Postgres lease-recovery anchor parity (`internal/integration/audit_governance_postgres_test.go`).

### Changed
- **Inbound scope gate on the HTTP `/mcp` mount: `write` AND `audit:event:write`, or `admin`** (`cmd/server/http.go`)
  - The `/mcp` POST route is now wrapped by `mcpScopeGate(authReg)` — `authReg.Require(auth.Scope(auditgovernance.RequiredScope))` — before MCP dispatch: every tool (read tools included) requires the audit-governance scope in addition to the global chain's coarse method scope; `admin` keys keep full access (`Key.Has` implies); registry disabled → pass-through unchanged (I5 baseline `TestFullServer_MCP` stays green). Denial is transport-level: unauthenticated → HTTP 401 (Auth ring, `WWW-Authenticate: Bearer realm="aero-vault"`); authenticated without the scope → HTTP 403, byte-exact body `missing scope: audit:event:write`, no `WWW-Authenticate`, no JSON-RPC error code — MCP clients fail `initialize` and report the server as disconnected (never a tool list).
  - **Operator note (breaking with auth enabled):** `AUTH_KEYS` / SigV4 / Snaplink principals cannot express the audit scope (`knownScope`) and lose HTTP `/mcp` entirely; provision MCP principals via JWT claims or `/v1/admin/keys` (`Registry.AddKey`), or switch them to REST `/v1/files`. **Stdio MCP (`aero-vault mcp`) is unaffected** — the gate lives only at the HTTP mount. Auth-disabled deployments are byte-identical.
  - `RequiredScope` grep consistency: `internal/auditgovernance/http_test.go:76,184` literals refactored to the constant; `grep -rn '"audit:event:write"' internal/ cmd/` now returns exactly `internal/auditgovernance/model.go:17`.
  - Tests: `cmd/server/governance_mcp_scope_e2e_test.go` — `TestGovernanceE2EMCPWriteFileProvisionedBearer` (provisioned bearer → object + outbox row + exactly one relay POST with `Bearer e2e-token`, fact-ID recomputation), `TestGovernanceE2EMCPScopeGateDeniesUnprovisioned` (403 byte pin + zero side effects over quiesce, 401 variant, admin clause, read-tool gating).

---

## 2026-08-07

### Fixed
- **Reconcile hard-delete: legal-hold/WORM gate moved before blob destruction; lifecycle hard_delete preserves non-current versions** (`internal/reconcile/deletion.go`, `internal/reconcile/lifecycle.go`, `internal/reconcile/retention.go`)
  - `hardDeleteKey`/`hardDeleteVersion` previously deleted storage blobs/chunks for every version **before** the repository-level gate (`ErrLegalHoldActive`), and that gate never checked WORM `locked_until` at all. A legal hold or WORM lock placed after the sweep's protection pre-check (but before the blob deletes) destroyed held blobs — for WORM, the row was deleted too, with no fallback. Both functions now re-run the three-source protection check (LockedUntil + `_aero_legal_hold` + `ObjectHasLegalHold`) **before any destructive action** and skip with zero side effects via the package-private `errKeyProtected` sentinel; skipped keys are never counted as deleted (`lifecycle hard` / `retention purged` / `noncurrent purged`). Protection-check failures remain fail-closed (skip, no delete).
  - **Behavior change (versioned buckets):** lifecycle `expire_action=hard_delete` now purges only the expired current version (and its delete marker) instead of nuking every version of the key at once. Non-current (tombstone) rows and their blobs stay under the bucket's `noncurrent_days` window and are only removed by the non-current-version sweep (or an explicit version delete). Non-versioned buckets are unaffected. Tombstone rows keep occupying tenant usage until purged, consistent with existing accounting.
  - New WARN log lines for operators: `lifecycle hard delete skipped: protected`, `lifecycle non-current version skipped: protected`, `retention hard delete skipped: protected`.
  - Tests: `TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow` (T-1), `TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow` (T-1b), `TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs` (T-1c) in `internal/reconcile/deletion_test.go`; `TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions` (T-2, incl. positive control) in `internal/reconcile/lifecycle_test.go`. All four are deterministic red→green regression tests (failed on the unfixed tree).

### Fixed
- **MaxBodySize: silent truncation of oversize chunked uploads** (`internal/middleware/validation.go`, `internal/api/s3compat/errors.go`, `internal/api/rest/handler_helpers.go`)
  - `MaxBodySize` replaced `io.LimitReader` (clean `io.EOF` at the cap — truncation indistinguishable from a body ending exactly at the limit) with a limit reader that peeks one byte past the cap and returns the new `ErrBodyTooLarge` sentinel when the body is longer. Unknown-length (`Transfer-Encoding: chunked`) uploads over `APP_MAX_BODY_SIZE` previously stored a silently truncated object and returned `200 + ETag`; they are now rejected with `413` and leave no object or blob behind.
  - **Behavior change for deployments that set `APP_MAX_BODY_SIZE`:** oversize chunked uploads change from silent corruption to explicit `413`. Clients should use multipart uploads or raise the limit. Known `Content-Length` early-reject (413 + `Connection: close`), pass-through at `0`, and exactly-at-limit bodies are unchanged.
  - Adapter mappings: S3 → `413 EntityTooLarge`, REST → `413 BodyTooLarge` (previously leaked as 500 `InternalError`). Multipart part uploads are covered automatically (same `materializeUnknownSize` path).
  - Tests: 4 new unit tests (`internal/middleware/validation_test.go`) and `TestFullServer_MaxBodySizeChunkedS3Put413` (`internal/integration/middleware_chain_test.go`) with S3 4xx + no-residue (HTTP 404 + `repository.ErrNotFound`) + under-limit chunked control group + REST 413 pin.

### Fixed
- **Antivirus: silent 32 MiB truncation in SignatureScanner** (`internal/antivirus/antivirus.go`, `internal/antivirus/worker.go`)
  - `SignatureScanner.Scan` replaced the `io.LimitReader(32<<20)` + `io.ReadAll` bounded read with a streaming sliding-window matcher (`O(maxSigLen)` memory, 64 KiB chunk): the whole object is scanned, `Clean:true` is returned only after EOF, and EICAR (or a custom signature) placed beyond 32 MiB is now detected and quarantined instead of being silently reported clean.
  - The worker's remainder drain is now gated to `*HTTPScanner` only (client-side hygiene for the in-flight POST body); the signature scanner consumes the whole stream inside `Scan`, so the unconditional drain no longer re-reads the object for nothing. Peak memory drops from 32 MiB to ~128 KiB; storage I/O is unchanged.
  - New tests in `internal/antivirus/truncation_test.go` (10 tests): >32 MiB tail EICAR → infected + quarantine (and infected tags when quarantine is off), no drain on the signature path (byte-counted), drain preserved on the HTTPScanner path, chunk-boundary/empty-stream/canceled-context/custom-tail unit pins.
  - Hardening (final review): HTTPScanner response decode is bounded to 1 MiB via `io.LimitReader` (a hostile/broken endpoint can no longer stream unbounded JSON into worker memory); startup WARN when `AV_API_KEY` is configured with a non-`https://` `AV_ENDPOINT` (`cmd/server/build.go`).
  - The HTTPScanner path now counts every byte pulled from storage and emits `WARN "remote scanner responded before receiving the full object"` when a remote engine answers before consuming the whole object (log-only; verdicts unchanged) — the operator-visible signal for the remote-side truncation residual.
  - Test-coverage closure (`internal/antivirus/hardening_test.go`, 15 tests): HTTPScanner error branches (non-2xx/malformed/oversized/transport/bad-endpoint/API-key header), the `Worker.Run` event→job bridge (enqueue/skips/error/cancel), the no-signatures matcher branch, and all `ScanObjectByID` fail-closed error paths (missing controller, unknown object, storage error, tag error, quarantine error). Package coverage 79.4% → 100%.
  - Existing ≤32 MiB verdicts are bit-identical; `av_status`/`av_signature` tag shape unchanged.

- **Folder-ACL prefix matching: SQL LIKE wildcard leakage** (`internal/repository/sql_access_acl.go`, `internal/access/manager.go`)
  - `ListApplicableACL` replaced `$5 LIKE resource_key || '%'` with a literal prefix comparison (`substr($5, 1, length(resource_key)) = resource_key`). Folder ACL keys containing `%` or `_` (e.g. `report_2026/`) no longer widen to sibling keys (`reportX2026/…`, `50x/…`).
  - `PutACL` now rejects `%`/`_` in **folder** ACL keys with HTTP 400 `InvalidArgument` (defense-in-depth). Object and bucket ACL keys are unaffected; existing wildcard rows remain readable/deletable but are no longer updatable.
  - **Behavior change:** folder-ACL prefix matching is now **case-sensitive** on all backends (SQLite `LIKE` was ASCII case-insensitive; e.g. folder ACL `Docs/` no longer grants `docs/…`). S3 keys are case-sensitive, so this unifies SQLite/Postgres semantics.
  - Operators can audit affected rows with: `SELECT tenant_id, bucket, resource_key FROM resource_acls WHERE resource_kind='folder' AND (resource_key LIKE '%\%%' ESCAPE '\' OR resource_key LIKE '%\_%' ESCAPE '\');` (Postgres) or the equivalent `ESCAPE '!'` form on SQLite.

---

## 2026-06-13

### Added
- **RRF hybrid sort deterministic tiebreaker** (`internal/ai/search.go`)
  - After accumulation, chunks with identical RRF scores are sorted by `(score DESC, chunkID ASC)`, eliminating nondeterministic ordering from map iteration.

- **BM25 hard-delete synchronous chunk cleanup** (`internal/ai/bm25.go`, `internal/storage/file_service.go`)
  - `FileService.WithChunkCleaner` wires an optional `ChunkSink` into the hard-delete path. When set, BM25 entries for the deleted object's chunks are evicted synchronously, preventing orphan entries from accumulating until restart.

- **Web UI: chat tab + drag-and-drop upload** (`internal/webui/static/index.html`)
  - New chat panel calls `/v1/chat` with SSE streaming and renders assistant responses incrementally. File upload via drag-and-drop uses the existing PUT endpoint. Tenant selector switches context without a page reload.

- **Python SDK admin methods** (`sdk/python/aero_vault.py`)
  - 14 new methods: `add_key`, `list_keys`, `revoke_key`, `issue_jwt`, `list_webhook_failures`, `list_jobs`, `retry_job`, `create_tenant`, `list_tenants`, `delete_tenant`, `set_tenant_status`, `list_audit`, `set_quota`, `set_budget`.

- **JS SDK admin methods** (`sdk/js/aero-vault.js`)
  - Same 14 admin methods as Python SDK, mirroring the full server admin API surface.

- **Go SDK admin methods** (`sdk/go/aerovault/client.go`, `sdk/go/aerovault/types.go`)
  - 14 new methods: `AddKey`, `ListKeys`, `RevokeKey`, `IssueJWT`, `ListWebhookFailures`, `ListJobs`, `RetryJob`, `CreateTenant`, `ListTenants`, `DeleteTenant`, `SetTenantStatus`, `ListAudit`, `SetQuota`, `SetBudget`.
  - New admin types: `APIKey`, `TenantRecord`, `AuditEntry`, `Job`, `WebhookFailure`, `AddKeyRequest`, `IssueJWTRequest`, `IssueJWTResponse`.
  - All three SDKs (Python, JS, Go) now cover the full server admin API surface.

- **Grafana tenant template variable fixed** (`deploy/grafana/aero-vault-ai-ops-dashboard.json`)
  - Template query changed from `label_values(ai_cost_micros_total, tenant)` to `label_values(storage_bytes, tenant)`. Tenants with no AI usage (storage-only) now appear in the dropdown and panels 11/12 (storage_bytes, storage_objects) populate correctly for all tenants.

- **MCP tools: `write_file`, `delete_file`, `chat`** (`internal/mcp/server.go`, `cmd/server/main.go`)
  - MCP agents can now write objects back to the vault, delete objects, and invoke RAG chat directly from MCP — without switching to REST. `write_file` and `delete_file` are always registered; `chat` is only exposed when a Chat service is wired.
  - Stdio MCP path (`aero-vault mcp`) also gains chat support via `buildLLM` + budget config.

- **AI-specific per-tenant rate limiting** (`AI_RATE_LIMIT_RPS` / `AI_RATE_LIMIT_BURST`)
  - A second `RateLimiter` instance, independent from `RATE_LIMIT_RPS`, gates only `/v1/search`, `/v1/chat`, `/v1/chat/stream`, `/v1/agent`, `/v1/lineage`. Storage and admin endpoints are unaffected.

- **Indexer skip metric** (`indexer_skip_total{reason}`)
  - `telemetry.IncIndexerSkip(ctx, reason)` is called on every skip path in `ai/indexer.go`. Prometheus exposes the counter with `reason=unsupported|error|empty` so operators can observe how many objects the indexer skips and why.

- **`AI_AGENT_MAX_STEPS` config** — Agent's tool-call loop depth is now configurable (default 4). Previously hardcoded.

- **`AI_CHUNK_WINDOW` / `AI_CHUNK_OVERLAP` config** — Chunker window and overlap sizes are now configurable (defaults 600/80). Enables tuning per corpus type.

- **ChatStream structured error frames** (`internal/api/rest/search.go`)
  - After SSE headers are sent, errors are now emitted as `event: error\ndata: {"code":"…","message":"…"}\n\n` rather than an unstructured string. Codes: `BudgetExceeded`, `InternalError`.

- **PII credit card Luhn validation** (`internal/ai/pii.go`)
  - The credit card regex now runs a Luhn check on each match. Unix timestamps, object IDs, and other numeric sequences that fail Luhn are no longer flagged as credit cards.

- **Qdrant integration test + `make test-integration-qdrant`** (`internal/integration/qdrant_integration_test.go`, `Makefile`)
  - `TestQdrantIntegration` exercises the full Qdrant adapter lifecycle (EnsureCollection, UpsertObjectChunks, SearchVectors, DeleteObjectChunks) against a live container. Skips gracefully when no Qdrant is reachable.

- **Grafana dashboard extended to 12 panels** (`deploy/grafana/aero-vault-ai-ops-dashboard.json`)
  - New panels 7–12: embed/search latency p50/p95, embed requests+tokens/sec by model, job queue depth, storage bytes/objects per tenant.

- **Prometheus alerts: `aero-vault-ai-latency` group** (`deploy/prometheus/alerts.yml`)
  - `HighEmbedLatencyP95`, `HighSearchLatencyP95`, `JobQueueDepthHigh`.

- **S3 bucket sub-resources** — `?acl` GET/PUT, `GetBucketLocation`, DELETE `?lifecycle`, paginated `?versions`.

- **Tenant CRUD + audit log** — admin endpoints for tenant lifecycle + `audit_log` table (migration 0016).

- **Persisted API keys** — sha256-hashed, scoped, TTL-cached, cross-replica invalidation via Postgres LISTEN/NOTIFY.

- **KMS key versioning + rotation** — `SecretProvider` interface, `keyfile`/HTTP/KMS providers, `STORAGE_SSE_REWRAP_ON_START`.

- **Qdrant adapter** (`internal/ai/qdrant.go`) — implements both `VectorIndex` (read) and `ChunkSink` (write), wired opt-in via `AI_VECTOR_BACKEND=qdrant`.

- **pgvector + pgFTS adapters** — `PgVectorIndex`, `PgFTSIndex`, wired opt-in; verified end-to-end against live Postgres via `make test-integration`.

- **Domain OTel metrics** — 14 instruments covering AI cost/tokens/latency, queue depth, storage gauges, reconcile orphans, idempotency replays, event drops.

- **Incremental BM25** — `ChunkSink`-based O(1) bookkeeping; no more 30-second full-corpus rebuild.

- **Idempotency-Key** — Stripe-style write deduplication, TTL/GC, optional body-hash fingerprint.

- **Cluster singletons** — `leases` table + `cluster.Singleton` helper gates reconcile/lifecycle/retention to one replica.

- **Write-ahead retention** — `RetentionJob` purges soft-deleted rows older than `RECONCILE_RETENTION_DAYS`.

- **WebDAV spill buffer** — uploads/downloads spill to temp file at >8 MiB; MOVE streams through the same buffer.

---

## Earlier

See git log for complete history prior to 2026-06-13.
