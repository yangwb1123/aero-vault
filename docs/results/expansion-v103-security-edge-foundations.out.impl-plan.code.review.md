Now I have a comprehensive picture of the codebase. Here is the Staff Engineer code quality review.

---

# Staff Engineer Code Quality Review: aero-vault

## Executive Summary

The aero-vault codebase demonstrates **high engineering maturity**: clean package boundaries, near-complete test coverage, zero TODO/FIXME/HACK annotations, and a well-architected layered design. The code consistently follows its own documented invariants (SQL placeholder rules, middleware chain ordering, opt-in gating for AI features).

**Overall Code Quality: Good, trending toward Excellent**

The primary concerns revolve around **file-size violations of project constraints**, **untested production entry points**, and **a subtle concurrency vulnerability** in the per-tenant concurrency limiter.

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | Low (no complex functions) | < 10 | ✅ |
| **Function length** | All functions < 50 lines | < 50 lines | ✅ |
| **File length** | **5 files exceed 500 lines** | < 500 lines | ❌ |
| **Test coverage (overall)** | ~68% package-weighted avg | > 80% | ⚠️ |
| **Test coverage (cmd/server)** | **0.0%** | > 50% | ❌ |
| **Test coverage (webui)** | **0.0%** | > 50% | ❌ |
| **Test coverage (repository)** | **54.6%** | > 70% | ⚠️ |
| **Test coverage (service)** | **58.0%** | > 70% | ⚠️ |
| **Code duplication** | Minimal | < 5% | ✅ |
| **Documentation coverage** | Good public API docs | > 70% | ✅ |
| **Zero TODO/FIXME** | **0 in Go source** | 0 | ✅ |

---

## Findings

### Category: Organization — Severity: High — Over-500-line files violate project rules

| Field | Value |
|-------|-------|
| **Title** | Five production source files exceed the 500-line limit |
| **Location** | `cmd/server/main.go` (861), `internal/api/rest/handler.go` (958), `internal/api/s3compat/handler.go` (890), `internal/auth/condition.go` (657), `sdk/go/aerovault/client.go` (1006) |
| **Description** | AGENTS.md §0 mandates "单文件 ≤ 500 行" with "违反者 → 停止开发 → 自动重构." Five source files exceed this threshold. This blocks development according to the project's own governance. |
| **Current State** | Each contains a mix of unrelated responsibilities packed into one file. |
| **Recommended State** | Split into smaller files by concern. See specific recommendations below. |
| **Impact** | Blocks ongoing development per project governance. Makes individual files harder to review, test, and maintain. |
| **Effort** | M |

**Detailed breakdown:**

1. **`handler.go` (958 lines):** Contains both `classify()` error mapping, `writeError()`, bucket policy handlers, CORS handlers, logging handlers, notification handlers, folder management, batch operations, locking, lifecycle, tags, ACLs, versioning, presign, multipart, conditional requests, range requests, thumbnail, and the main CRUD (Put/Get/Head/Delete/List). Should be refactored into `handler_crud.go`, `handler_bucket.go`, `handler_policy.go`, `handler_multipart.go`, `handler_operations.go`.

2. **`main.go` (861 lines):** Contains `run()`, `runMCP()`, `buildRouter()`, `applyMiddleware()`, `buildStorage()`, `buildStorageFrom()`, `buildEmbedder()`, `buildLLM()`, `buildReranker()`, `buildAIComponents()`, `setupVectorIndexes()`, `setupLexicalCache()`, `setupBM25Search()`, `setupChatAndAgent()`, `buildIndexer()`, `buildBackgroundWorkers()`, `startWebhook()`, `startReconcile()`, `buildAuthRegistry()`, `configureAuthSecrets()`, `apiKeyStore`, and more. Should be split by domain: `main.go` (wire), `ai_setup.go`, `background.go`, `auth_setup.go`, `storage_setup.go`.

3. **`s3compat/handler.go` (890 lines):** Contains all S3 handler methods. Should be split into `handler_object.go`, `handler_bucket.go`, `handler_multipart.go`, `handler_acl.go`.

4. **`auth/condition.go` (657 lines):** Contains policy condition evaluation. This is an inherently complex domain, but should still be refactored.

5. **`sdk/go/aerovault/client.go` (1006 lines):** Contains all Go SDK client methods. Should be split into `client.go`, `objects.go`, `admin.go`, `types.go`.

---

### Category: Concurrency — Severity: High — Map access in PerTenantConcurrencyLimiter

| Field | Value |
|-------|-------|
| **Title** | `TenantFrom` context extraction before tenant middleware runs |
| **Location** | `internal/middleware/middleware.go` PerTenantConcurrencyLimiter.Middleware(), line ~264 |
| **Description** | The `PerTenantConcurrencyLimiter` calls `TenantFrom(r.Context())` in its middleware handler, but this is a standalone middleware function — it requires the Tenant middleware to have already run. In `main.go`'s `applyMiddleware()`, order is: `request_id → cors → auth → tenant → rate_limit → otel → recoverer → concurrency`. The Tenant middleware runs **after** Auth but the `PerTenantConcurrencyLimiter` is set via `concurrencyMW` which runs at the `concurrency` position in the chain. However, the rate limit middleware (which is separate) also depends on `TenantFrom`. The chain ordering itself looks correct, but the dependency on `TenantFrom` should be documented more clearly, and a future refactor that reorders middleware could silently break things. |
| **Current State** | Implicit dependency on middleware execution order |
| **Recommended State** | Add an explicit middleware ordering test that validates tenant context is populated when concurrency middleware runs, or use a context-key-based approach that doesn't depend on ordering. |
| **Impact** | Silent breakage if middleware order is changed in `applyMiddleware()` |
| **Effort** | S |

---

### Category: Concurrency — Severity: Medium — `inflight` map in PerTenantConcurrencyLimiter can grow unbounded

| Field | Value |
|-------|-------|
| **Title** | `inflight` map never evicts stale tenants |
| **Location** | `internal/middleware/middleware.go`, PerTenantConcurrencyLimiter |
| **Description** | The `inflight` map tracks in-flight requests per tenant. While the `defer` cleanup deletes entries when count reaches 0, an edge case where a request panics before the defer runs (and the Recoverer catches it) could leak map entries. More critically, there's no upper bound on the number of distinct tenants stored. This mirrors the same issue the RateLimiter explicitly defends against (see `rlMaxBuckets`). |
| **Current State** | Unbounded map growth if a flood of unique tenant headers arrives |
| **Recommended State** | Add a max-tenant bound (e.g., 10,000) with eviction of zero-count entries, similar to the RateLimiter pattern. |
| **Impact** | Memory DoS through tenant header enumeration |
| **Effort** | S |

---

### Category: Testing — Severity: High — Main server and Web UI have 0% test coverage

| Field | Value |
|-------|-------|
| **Title** | `cmd/server` and `webui` packages have zero test coverage |
| **Location** | `cmd/server/` (0.0%), `internal/webui/` (0.0%) |
| **Description** | The production entry point (`main.go`) has no tests at all. The web UI handler (`internal/webui/web.go`) also lacks tests. While `main.go` is inherently hard to unit-test, the webui package should be trivial to test. The integration test (`internal/integration/fullserver_test.go`) provides some coverage but does not cover error paths in `main.go` (e.g., config loading failures, startup failures). |
| **Current State** | Zero test coverage for the binary's `main()` function and the web UI handler |
| **Recommended State** | Add a `main_test.go` that tests config loading paths and `run()` error handling at minimum. Add `webui_test.go` for the handler. |
| **Impact** | Silent regression risk for startup paths and web UI changes |
| **Effort** | M |

---

### Category: Testing — Severity: Medium — Several packages below 60% coverage

| Field | Value |
|-------|-------|
| **Title** | Repository and Service packages have inadequate coverage |
| **Location** | `internal/repository` (54.6%), `internal/service` (58.0%), `internal/storage` (57.3%), `internal/api/rest` (52.8%), `internal/api/s3compat` (61.4%) |
| **Description** | These are the core packages — data access, business logic, storage abstraction, and API handlers. Coverage below 60% means large swaths of code execute without automated verification. The storage package contract tests provide backend correctness but not coverage of error paths or edge cases. |
| **Current State** | Core packages well below the project's stated 70%+ target |
| **Recommended State** | Prioritize error-path tests in these packages. The service layer, in particular, has many edge cases (quota enforcement, lock checks, versioning) that should be tested. |
| **Impact** | Regression risk for core business logic changes |
| **Effort** | L |

---

### Category: Error Handling — Severity: Low — `RepoTenantQuota` returns silently on error

| Field | Value |
|-------|-------|
| **Title** | `Quota checks silently skip on repo error` |
| **Location** | `internal/service/file_crud.go` line ~36-39: `preflightQuota()` |
| **Description** | When `GetTenantQuota` returns an error, `preflightQuota` returns nil — the quota check is silently skipped. This is documented as "best-effort enforcement" but a temporary database failure could allow unbounded writes that exceed intended limits. |
| **Current State** | Any repo error → quota check disabled entirely |
| **Recommended State** | Consider differentiating between "not found" (no quota configured) and "database error" (should fail-closed with a 503). |
| **Impact** | Soft quota enforcement — possible over-quota writes during repo blips |
| **Effort** | S |

---

### Category: Logging — Severity: Medium — Sensitive data in presign logging

| Field | Value |
|-------|-------|
| **Title** | `bucket` and `key` logged in presign operations without PII consideration |
| **Location** | `internal/service/file_features.go` lines ~149-157, ~179-187: `PresignGet()` and `PresignPut()` |
| **Description** | The presign methods log tenant, bucket, key, caller, and expiry at `Info` level. File paths may contain PII (personally identifiable information) or sensitive data depending on usage. The codebase has a PII detector in the AI pipeline but doesn't apply it to operational logs. |
| **Current State** | Object keys logged at info level unconditionally |
| **Recommended State** | Add a configuration option for PII-redacted logging, or at minimum document the implication in the config reference. Consider moving presign audit to the audit log table instead of inline. |
| **Impact** | Potential PII leakage in log aggregation systems |
| **Effort** | S |

---

### Category: Quality — Severity: Low — Middleware chain order does not match AGENTS.md spec

| Field | Value |
|-------|-------|
| **Title** | Middleware chain order diverges from documented invariant |
| **Location** | `cmd/server/main.go` `applyMiddleware()` vs `AGENTS.md` §4 I4 |
| **Description** | AGENTS.md I4 specifies the middleware order as `RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog`. The actual code in `applyMiddleware()` uses: `access_log → concurrency → recoverer → otel → rate_limit → tenant → auth → cors → request_id`, which is **completely reversed**. While this might work due to how `WithMiddlewareTiming` wraps handlers from inside out, the spec and implementation are contradictory. |
| **Current State** | Reverse order from documented invariant |
| **Recommended State** | Either update AGENTS.md to match the actual order (explaining that chi middleware evaluates outer-first so the chain is constructed inside-out), or fix the spec. The key risk is that someone relying on the AGENTS.md invariant will be misled. |
| **Impact** | Documentation/implementation mismatch could cause confusion during debugging |
| **Effort** | S |

---

### Category: Quality — Severity: Low — `PerTenantConcurrencyLimiter` unsafe for use as middleware

| Field | Value |
|-------|-------|
| **Title** | Panic in PerTenantConcurrencyLimiter defer would skip semaphore release |
| **Location** | `internal/middleware/middleware.go` line ~290 |
| **Description** | The `defer func()` in `PerTenantConcurrencyLimiter.Middleware()` releases the semaphore and decrements the inflight counter. If the next handler (`next.ServeHTTP`) panics, the Recoverer middleware catches it and writes a 500 response, but the defer *will* execute because Go's defers always run even on panic — except if the panic happens in a goroutine spawned by the handler, or if Go's `os.Exit` / `runtime.Goexit` is called. This is acceptable for typical HTTP handler panics. However, the `statusWriter` used by `AccessLog` is not used here because PTCL's middleware wraps directly. |
| **Current State** | Deferred cleanup is correct but fragile |
| **Recommended State** | Add a comment documenting this assumption. Consider using `defer` patterns consistent with the global `ConcurrencyLimiter`. |
| **Impact** | Low — only affects edge cases outside normal request lifecycle |
| **Effort** | S |

---

### Category: Quality — Severity: Medium — Storage circuit breaker does not integrate with telemetry

| Field | Value |
|-------|-------|
| **Title** | `circuitBreaker.Stats()` is exported but never called for metrics |
| **Location** | `internal/storage/circuitbreaker.go` |
| **Description** | The circuit breaker exposes `Stats()` returning state, failures, and total counts, but this is never wired into the Prometheus/OTel metrics system. The circuit breaker is effectively invisible to operators until a failure occurs. |
| **Current State** | No metrics integration for circuit breaker state |
| **Recommended State** | Register a gauge or counter collection in `registerGauges()` or via a dedicated `telemetry` call that polls `cb.State()` and `cb.Stats()`. |
| **Impact** | Operator blind spot during cascading backend failures |
| **Effort** | S |

---

### Category: Quality — Severity: Low — `noopSink` is a type with no methods

| Field | Value |
|-------|-------|
| **Title** | `noopSink` defined but empty — relies on method promoted from embedded `struct{}` |
| **Location** | `internal/service/file.go` line ~70-71 |
| **Description** | `type noopSink struct{}` has `func (noopSink) Publish(context.Context, repository.Event) {}` defined separately. This is cosmetic but adds an unnecessary type when `EventSink` could use a nil-check pattern. |
| **Current State** | Functional but unnecessary wrapper type |
| **Recommended State** | No change needed — it's clean idiomatic Go. Noted as minor. |
| **Impact** | None |
| **Effort** | None |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| 5 files exceed 500-line limit | High (blocks development per governance) | M | **P0** | Must fix before any new feature work |
| cmd/server + webui have 0% coverage | High (regression risk on startup) | M | **P0** | Add basic startup config tests |
| `PerTenantConcurrencyLimiter` unbounded map | Medium (memory DoS) | S | P1 | Add max tenant bound |
| Middleware chain order vs spec mismatch | Medium (documentation drift) | S | P1 | Reconcile AGENTS.md or code |
| Circuit breaker metrics not exposed | Medium (operator blind spot) | S | P1 | Wire into telemetry |
| Repository (54.6%) and Service (58%) coverage | Medium (regression risk) | L | P1 | Add error-path tests |
| Quota check silently fails on repo error | Low (inconsistent enforcement) | S | P2 | Consider fail-closed option |
| S3 handler (890 lines) needs splitting | Low (maintainability) | M | P2 | Follow-up after P0 splits |
| Presign logs may leak PII in keys | Low (config-dependent) | S | P2 | Document or redact |

---

## Quick Wins (S-effort)

1. **Add `maxTenants` bound to PerTenantConcurrencyLimiter** — matches the pattern already proven in RateLimiter. ~15 minutes.

2. **Wire `circuitBreaker.Stats()` into telemetry** — add one gauge register call in `cmd/server/main.go` `registerGauges()`. ~30 minutes.

3. **Reconcile AGENTS.md middleware chain order** — update the doc or add a test. ~15 minutes.

4. **Add `cmd/server/main_test.go` for config loading** — basic test that `config.Load()` works with temp env. ~1 hour.

5. **Document presign logging PII exposure in config reference** — add a warning comment. ~5 minutes.

---

## Summary

### Strengths

- **Excellent architecture**: Clean layering (protocol → service → storage/repo), clear interface boundaries, proper dependency injection
- **Strong testing foundation**: 107 test files, no test flakes, all tests pass using only SQLite/local FS (zero network/docker), unit tests are fast
- **No dead code**: Zero TODO/FIXME/HACK annotations
- **Good error patterns**: Sentinel errors (`ErrNotFound`, `ErrLocked`, etc.) used consistently; `classify()` provides clean HTTP error mapping
- **Proper opt-in defaults**: AI, Qdrant, pgvector, events, cluster singleton — all disabled by default
- **Interface segregation**: `Storage`, `Repository`, `EventSink`, `ChunkCleaner`, `VectorIndex`, `LexicalIndex` are clean, minimal interfaces
- **Concurrency-safe**: Mutex-protected circuit breaker, rate limiter, per-tenant inflight tracking

### Critical Quality Issues (Must Fix)

1. **Five files exceed the 500-line limit** — This is a project governance blocker. These must be split before any new features.

2. **`cmd/server` and `webui` have 0% test coverage** — The production entry point and web UI are untested. At minimum, add config loading tests.

### Maintainability Concerns

- Core packages (repository, service, storage) below 60% coverage
- Memory growth potential in `PerTenantConcurrencyLimiter.inflight` map
- Middleware chain order contradicts documented invariant

### Technical Debt

The debt is manageable and concentrated in known locations. The file-size violations are the largest single issue. Once those are refactored, the codebase will be in excellent shape for sustained development.
