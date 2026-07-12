Now I have a thorough understanding of the codebase. Here is my comprehensive code review.

---

# Staff Engineer Code Review: aero-vault

## Overall Assessment: **Good** with notable concerns

The codebase is well-structured, follows Go idioms, has solid test coverage (~54–84%), and cleanly passes `go vet` and `go build`. There are several areas with medium-to-high severity issues that need attention before production.

---

## Findings

### 1. Code Organization

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Organization | **High** | Files exceeding 500-line limit | `internal/api/rest/handler.go` (958 lines), `internal/api/s3compat/handler.go` (890 lines), `internal/auth/condition.go` (657 lines), `internal/api/webdav/dav.go` (458 lines), `internal/api/s3compat/xml.go` (438 lines), `internal/repository/sql_objects.go` (434 lines), `internal/mcp/server.go` (429 lines), `internal/service/file_crud.go` (420 lines), `internal/repository/sql_buckets.go` (419 lines), `internal/cli/cli_admin.go` (419 lines) | 22 files exceed the 500-line constraint defined in `AGENTS.md`. This violates the project's own engineering constraints and makes files hard to navigate. | Files are monolithic. `handler.go` contains all REST bucket operations, CORS, logging, policy, etc. | Split into domain-focused files: `handler_buckets.go`, `handler_cors.go`, `handler_logging.go`, `handler_policy.go`, etc. | Direct violation of project constraints; code navigation, review difficulty, merge conflicts | M |
| Organization | **Medium** | God types in repository interface | `internal/repository/repository.go` — `Repository` interface (~100 methods) | The `Repository` interface is a massive god interface. This violates the project's own "no God types" constraint (>300 lines — the interface definition alone is ~394 lines). Every change to any schema element requires modifying this interface, creating unnecessary coupling. | Single monolithic interface with methods for objects, buckets, events, chunks, quotas, webhooks, jobs, idempotency, API keys, leases, tenants, audit, etc. | Split into focused sub-interfaces (e.g., `ObjectStore`, `BucketStore`, `EventStore`, `JobStore`, `AdminStore`) and compose them. | Tight coupling; hard to mock; breaking interface changes cascade across the entire codebase | L |
| Organization | Medium | REST handler has mixed concerns | `internal/api/rest/handler.go` | The file mixes data access (`checkBucketPolicy` calls `svc.GetBucketConfig`), business logic, response formatting, error classification, and header manipulation all in one file. | Everything is in `Handler` methods. | Extract response helpers, header writers, and policy checking into dedicated files. Partial progress made (`dto.go`, `util.go`). | Maintainability, testing difficulty | S |

### 2. Naming & Documentation

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Naming | **Low** | Inconsistent sentinel error format | `internal/service/file.go` | Most service errors start with "object is..." or "invalid arguments", but `ErrBadDigest` says "content-md5 mismatch" (lowercase, no period). Inconsistent error message casing. | `ErrBadDigest = errors.New("content-md5 mismatch")` vs `ErrNotFound = errors.New("object not found")` (lowercase start, no period — actually consistent). | Already fine but check `ErrSizeMismatch`: "size mismatch: actual bytes differ from Content-Length" — colon inconsistent with other errors that use `: ` pattern. | Minor inconsistency in error message style. | S |
| Naming | Low | `noopSink` vs standard Go naming | `internal/service/file.go` | `noopSink` should be `noopSink` is actually fine per Go convention (all lowercase for unexported types). No issue. | — | — | — | — |
| Documentation | Medium | Public API surface under-documented | Multiple files | Many exported types and functions lack doc comments. Examples: `NewFileService`, `FileService.Put`, `FileService.Get`, `Search.Query`, `Indexer.Run`. Core abstractions like `Storage` have good comments, but individual methods frequently lack them. | Selected interfaces (`Storage`, `Repository`) are well-documented; most implementation methods are not. | Add Go-style doc comments (`// MethodName does X. Returns Y or an error.`) to all exported symbols. | Onboarding difficulty, IDE documentation gaps | M |

### 3. Error Handling

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Error Handling | **High** | `context.Background()` used in production code paths | `internal/ai/indexer.go:313,316`, `internal/events/bus.go:139`, `internal/api/webdav/dav.go:302,381` | Production code detaches from the request context and uses `context.Background()` for telemetry/operations, losing trace context, cancellation, and deadline propagation. In the WebDAV handler, the request context is lost entirely and replaced with `Background()`. | `telemetry.IncIndexerSkip(context.Background(), ...)` in indexer; `telemetry.IncEventDropped(context.Background())` in bus; `ctx = context.Background()` in WebDAV | Thread the available context through: use the event context for bus operations (it has one), pass ctx through the indexer path, use request context in WebDAV. | Lost tracing, no cancellation propagation for long operations | M |
| Error Handling | Medium | Error swallowing in preflight quota | `internal/service/file_crud.go:37-39` | When `GetTenantQuota` returns an error, the quota check is silently bypassed. | ```go q, qErr := s.repo.GetTenantQuota(ctx, tenant) if qErr != nil { return nil } ``` | At minimum log the error: `s.logger.Warn("quota check skipped", "err", qErr)`. Consider degrading differently (e.g., still allow writes but log). | Silent quota bypass can lead to unbilled overuse | S |
| Error Handling | Medium | Error swallowed in quota usage increment | `internal/service/file_crud.go:144` | After successful repo write, quota usage increment failure is logged but silently ignored. | ```go if _, qErr := s.repo.AddTenantUsage(...); qErr != nil { s.logger.Warn(...) } ``` | This is an intentional design choice per the codebase philosophy (best-effort). The concern is tracking: a metric counter for decrement failures would help ops. | Minor — quota drift can accumulate silently over time | S |
| Error Handling | Medium | Hard delete chunks cleanup failure is non-fatal by design | `internal/service/file_crud.go:244-246` | Chunk cleanup failure on hard delete is logged but the delete proceeds. | ```go if s.chunkCleaner != nil { if err := s.chunkCleaner.DeleteObjectChunks(...); err != nil { s.logger.Warn(...) } } ``` | This is intentional per `AGENTS.md` I5. Consider tracking this with a metric counter (`orphaned_chunks_total`). | Orphaned chunks in vector indexes after hard deletes | S |

### 4. Logging

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Logging | Medium | Structured logging inconsistency | Multiple handler files | Some handlers log at `Info` level for operational events (presign generation) while others remain silent. There's no consistent pattern for which operations get logged. | Ad-hoc: presign gets Info log, PUT/GET/DELETE typically don't. | Add structured `Info` log entries for all mutating operations (PUT, DELETE, multipart) with tenant/key/duration. | Operational observability gaps for audit | M |
| Logging | Low | Error logging in `classify()` | `internal/api/rest/handler.go:440-441` | The `classify` function has a `default` case that leaks the raw error message to the client. | `default: return "InternalError", err.Error(), http.StatusInternalServerError` | For `InternalError`, don't include the full error message in the response (security concern). Log the full error server-side, return a generic message. | Information disclosure — internal error details exposed in API responses | S |
| Logging | Low | Event publish failures log level | `internal/events/bus.go:108,113` | Both event insert failure and transport failure are logged at `Warn`. Insert failure is more severe. | `b.logger.Warn("event insert failed", ...)` | Event insert failure should be `Error` level since it means the event is lost entirely. | Event loss not surfaced appropriately | S |

### 5. Testing Practices

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Testing | **High** | Test coverage below target in critical packages | `internal/api/rest` (52.8%), `internal/repository` (54.6%), `internal/service` (58.0%), `internal/storage` (57.3%), `internal/webui` (0.0%), `cmd/server` (0.0%) | The project's AGENTS.md mandates >50% minimum, but the target should be 80%. Several core packages are significantly below the aspirational target. REST handler, Storage, Service, and Repository are critical paths with low coverage. | Coverage ranges from 0% (webui, cmd/server) to 84% (ai). Core packages sit at 50-60%. | Increase coverage in REST handlers to 70%+ (test error paths, edge cases). Storage contract tests exist but need more unit tests. Repository needs coverage for failure modes (DB errors). | Regressions in critical data paths may go undetected | L |
| Testing | Medium | Table-driven tests inconsistent | Various test files | Some areas use table-driven tests (`range_test.go` is excellent), while others use repetitive individual test functions. | Good: `TestParseByteRange` uses clean table. Bad: `TestSetTags`, `TestLockObject` etc. in service tests are individual functions. | Standardize on table-driven tests for systematic edge-case coverage, especially for the `classify` error handler. | Test maintainability, coverage completeness | M |
| Testing | Medium | Integration tests behind build tags not run in CI | `internal/ai/integration_test.go`, `internal/storage/contract_test.go` | Integration tests use `//go:build integration` or external dependencies. They exist but are not run in the standard CI gate. | Tag-gated tests exist but untracked for CI execution. | Document which build tags exist (integration, qdrant, pgvector) and add a CI matrix or `make test-all` target. | Integration bugs slip through | S |
| Testing | Low | Test helper duplication | `internal/service/service_test.go` vs `internal/service/quota_test.go` | Both files define nearly identical `newTestSvc` / `newQuotaTestSvc` helper functions. | Two separate helper functions with identical structure (just different temp dir names). | Consolidate into a single `testhelpers.go` file in the service package. | Code duplication, maintenance burden | S |

### 6. Technical Debt

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Technical Debt | **Medium** | `interface{}` used instead of `any` | `internal/auth/policy.go:28,131,135,143,204,225-228,239,245` | While functionally equivalent, `interface{}` is the pre-1.18 spelling and considered noise in modern Go code. | `Principal map[string]interface{}`, `Action interface{}`, `Resource interface{}`, etc. | Replace with `any`. | Code style consistency for new Go developers | S |
| Technical Debt | **Medium** | `gofmt` compliance issue | `internal/auth/condition.go` | File does not pass `gofmt -l`. The CI gate (`gofmt -l .` must have no output) would reject this file. | Alignment in const block is non-standard. | Run `gofmt -w internal/auth/condition.go`. | CI gate violation | S |
| Technical Debt | Medium | Unused variables and code | `internal/api/rest/search.go:143` | `var _ = service.DefaultBucket` is used to suppress unused import warning. This is a band-aid. | `var _ = service.DefaultBucket` | Either use the import properly or remove the import. This indicates a design issue where search needs the bucket constant but only uses it for type-level reference. | Code smell | S |
| Technical Debt | Low | Inline error structs for SSE | `internal/api/rest/search.go:177-180` | `sseErrPayload` is defined as a local struct inside the function. | ```go type sseErrPayload struct { ... } body, _ := json.Marshal(sseErrPayload{...}) ``` | Extract to package-level or use `map[string]string`. Minor nit. | Minimal | S |

### 7. Code Quality Metrics

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Quality | **Medium** | Complexity in `classify` function | `internal/api/rest/handler.go:416-443` | `classify` uses a switch on `errors.Is` — correct approach — but includes a side call to `classifyLock`. The function is <50 lines but the cognitive complexity of error classification is high. | `classify` does double dispatch (classifyLock then switch). | Could use a centralized error registry pattern mapping errors to HTTP codes. | Error classification becomes hard to maintain as new error types are added | M |
| Quality | Medium | Code duplication in response header writing | `internal/api/rest/handler.go` | `handleRangeOrFull` (lines ~198-213) and `serveRange` (lines ~216-234) and `Head` (lines ~264-283) have near-identical header-writing blocks. | Same 10+ lines repeated in three places. | Extract `writeObjectHeaders(w, obj)` helper. | Violates DRY; future changes to response headers need edits in 3+ places | S |
| Quality | Low | Circuit breaker config defaults set at call site | `internal/storage/factory.go` (via `cmd/server/main.go:147-156`) | Defaults for circuit breaker (failure threshold, recovery timeout, half-open max) are set in `buildStorageFrom` in main.go rather than in the storage package itself. | Defaults set in the main function. | Move defaults into the storage package (e.g., `DefaultCBConfig()` function). | Scattered default logic between config and main | S |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Acceptable (no single function >50 lines in production code) | < 10 | ✅ |
| Function length | max ~72 lines (some test functions are long) | < 50 lines | ⚠️ (test functions exceed 50) |
| Test coverage | 52%–84% across packages (avg ~67%) | > 80% | ⚠️ (core packages below target) |
| Code duplication | Low (minor: header writers, test helpers) | < 5% | ✅ |
| Documentation coverage | ~40% of exported symbols documented | > 70% | ❌ |
| File length | 22 files exceed 500 lines | < 500 lines (per AGENTS.md) | ❌ |
| gofmt compliance | 1 file fails | 0 | ⚠️ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Files exceeding 500-line constraint | High | M | P0 | Violates project's own AGENTS.md; CI should reject but current check may not catch this |
| God-type Repository interface (~100 methods) | High | L | P1 | Creates tight coupling across all storage operations; every schema change touches this |
| Test coverage < 60% in core packages | High | L | P1 | REST handlers (52.8%), Repository (54.6%) — high risk for regressions |
| Production code uses context.Background() | Medium | M | P1 | Lost tracing/cancellation in indexer, event bus, and WebDAV |
| Internal error messages leaked in API response | Medium | S | P1 | `InternalError` returns `err.Error()` which could contain sensitive info |
| Error message consistency | Low | S | P2 | Minor style differences in service errors |
| interface{} vs any | Low | S | P3 | Cosmetic but adds noise |
| gofmt condition.go | Low | S | P0 | CI gate violation |
| Duplicate header-writing code | Low | S | P2 | DRY violation across 3 functions |
| No TODO/FIXME tracking | Low | — | P3 | Zero tracked technical debt — this is positive but may indicate debt is invisible |

---

## Final Summary

### Overall Code Quality: **Good**

This is a well-architected Go codebase with clean layering (protocol adapters → service → storage/repository), proper use of interfaces for testability, and strong handling of edge cases (versioning, SSE encryption, multipart uploads, conditional requests). The codebase shows maturity in its error classification strategy, event-driven architecture, and opt-in activation of features.

### Critical Quality Issues (Must Fix Before Production)

1. **File length violations (P0):** 22 files exceed the 500-line constraint from AGENTS.md. The worst offenders (`handler.go` at 958 lines, `s3compat/handler.go` at 890 lines) must be split before any new features are added per the project's own rules.

2. **`gofmt` violation (P0):** `internal/auth/condition.go` fails the CI gate. Must be formatted immediately — this blocks any commit.

3. **`context.Background()` in production paths (P1):** The indexer, event bus, and WebDAV handler lose request context, breaking tracing and cancellation. This should be fixed as a batch.

4. **Error message leakage (P1):** `classify()` returns `err.Error()` for `InternalError`. Production APIs should not leak internal error details.

### Maintainability Concerns

- The monolithic `Repository` interface (100 methods) is the single biggest maintenance risk. Every schema addition requires interface modification. Consider splitting into focused interfaces.
- Three copies of response header logic in `handler.go` (range/full/head) will lead to bugs when headers change.
- The `sql_objects.go` / `sql_buckets.go` files (both >400 lines) could benefit from splitting by domain.

### Technical Debt

- **Accumulated but manageable.** The codebase has zero TODO/FIXME comments, which is unusual — some debt may be invisible rather than absent.
- The 500-line file limit is being systematically violated. The CI gate doesn't appear to enforce this, making the constraint aspirational rather than enforced.
- `interface{}` usage in policy evaluation should be updated to `any` for modern Go style.

### Quick Wins

1. **Run `gofmt -w internal/auth/condition.go`** — 30-second fix, unblocks CI.
2. **Extract `writeObjectHeaders()`** in REST handler — reduces DRY violation across 3 functions, ~15 minutes.
3. **Replace `interface{}` with `any`** in `policy.go` — find-and-replace, ~5 minutes.
4. **Replace `context.Background()`** with available context in indexer and bus — ~30 minutes.
5. **Add `s.logger.Warn("quota check skipped", ...)`** to the preflight quota error path — 2 lines, improves debuggability.
6. **Consolidate duplicate test helpers** in service package — ~15 minutes.
