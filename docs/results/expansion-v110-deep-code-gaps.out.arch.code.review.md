Now I have a comprehensive understanding of the entire codebase. Let me compile the full review.

---

# Staff Engineer Code Quality Review: aero-vault

## Executive Summary

This is a **well-architected** Go system with clear domain separation and thoughtful design patterns. However, it suffers from **significant file size violations**, **duplicated code across protocol adapters**, and a **God-interface in the repository layer** that represent the highest-priority technical debt items.

---

## 1. Code Organization

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Organization** | **Critical** | Production files exceed 500-line limit | Multiple files |
| **Organization** | **High** | Repository God Interface (50 methods) | `internal/repository/repository.go:70-190` |
| **Organization** | **High** | Main function does too much | `cmd/server/main.go:1-861` |
| **Organization** | **Medium** | Config struct and loader too large | `internal/config/config.go:1-446` |
| **Organization** | **Medium** | Web UI package has no tests | `internal/webui/web.go` |
| **Organization** | **Low** | `internal/thumbnail/` is a trivially small package | `internal/thumbnail/thumbnail.go` |

### Finding ORG-1: Production files exceed 500-line limit

**Description**: The AGENTS.md engineering constraints mandate `single file ≤ 500 lines`. Several critical production files violate this:

| File | Lines | Over limit |
|------|-------|------------|
| `sdk/go/aerovault/client.go` | 1,006 | +506 ❌ |
| `internal/api/rest/handler.go` | 958 | +458 ❌ |
| `internal/api/s3compat/handler.go` | 890 | +390 ❌ |
| `cmd/server/main.go` | 861 | +361 ❌ |
| `internal/auth/condition.go` | 657 | +157 ❌ |

**Current State**: Monolithic files containing 36-49 handler methods each, making navigation difficult and increasing merge conflict probability. The `rest/handler.go` handles objects, buckets, folders, batch ops, presign, CORS, notifications, lifecycle, versioning, and logging — all in one file.

**Recommended State**: Split each large file:
- `rest/handler.go` → `rest/handler_buckets.go`, `rest/handler_objects.go`, `rest/handler_multipart.go`, `rest/handler_admin.go`, `rest/handler_search.go`, `rest/helpers.go`
- `s3compat/handler.go` → `s3compat/objects.go`, `s3compat/buckets.go`, `s3compat/multipart.go`, `s3compat/conditionals.go`, `s3compat/xml_helpers.go`
- `cmd/server/main.go` → `cmd/server/app.go` (wiring), `cmd/server/routes.go` (router), `cmd/server/ai.go` (AI setup), `cmd/server/workers.go` (background)
- `auth/condition.go` → `auth/condition/` sub-package with separate files per operator type

**Impact**: Maintainability, onboarding friction, merge conflicts

**Effort**: L

### Finding ORG-2: Repository God Interface

**Current State**: `Repository` interface in `repository.go` declares ~50 methods covering objects, buckets, chunks, events, uploads, tenants, API keys, quota, audit, jobs, idempotency, leases, and webhook failures. Any implementation (SQLite, Postgres) must implement ALL methods regardless of relevance.

**Recommended State**: Split into focused interfaces:

```go
type ObjectRepository interface { ... }
type BucketRepository interface { ... }
type ChunkRepository interface { ... }
type EventRepository interface { ... }
type TenantRepository interface { ... }
type AuditRepository interface { ... }
type JobRepository interface { ... }

// Composite for convenience
type Repository interface {
    ObjectRepository
    BucketRepository
    ChunkRepository
    EventRepository
    TenantRepository
    AuditRepository
    JobRepository
    ...
}
```

**Impact**: Testability — without splitting, unit tests for components only needing a subset of `Repository` must mock 50 methods.

**Effort**: L (breaking change, requires interface segregation across all consumers)

---

## 2. Naming & Documentation

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Naming** | **Medium** | Duplicate metadata extraction functions | `rest/handler.go` and `s3compat/handler.go` |
| **Naming** | **Medium** | `dto.go` suffix naming inconsistent with Go conventions | `internal/api/rest/dto.go` |
| **Documentation** | **Low** | Package-level docs present but sparse for exported symbols | Various |
| **Documentation** | **Low** | No `TODO`/`FIXME` comments anywhere in codebase | All files |

### Finding NAM-1: Duplicate metadata extraction functions

**Current State**: `extractMetaHeaders` in `s3compat/handler.go` and `extractMetadataHeaders` in `rest/handler.go` are near-identical functions parsing the same HTTP headers with slightly different names.

```go
// rest/handler.go
func extractMetadataHeaders(h http.Header) map[string]string { ... }

// s3compat/handler.go
func extractMetaHeaders(h http.Header) map[string]string { ... }
```

**Recommended State**: Extract shared protocol helpers into a shared package (e.g., `internal/api/internal/headers.go` or use the existing service layer):

```go
// internal/api/internal/headers.go
func ExtractAMetaHeaders(h http.Header) map[string]string { ... }
```

**Effort**: S

### Finding NAM-2: No TODO/FIXME across entire codebase

**Current State**: Zero `TODO`/`FIXME`/`HACK` annotations in any `.go` file. This is suspicious for a project of this size and typically indicates one of: issues tracked externally; or developers not marking incomplete work.

**Impact**: Risk that known limitations or incomplete features go unnoticed during reviews. Recommend tracking at least architectural debt items.

---

## 3. Error Handling

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Error Handling** | **High** | Inconsistent error wrapping conventions | Multiple files |
| **Error Handling** | **Medium** | Sensitive error details leaked in HTTP responses | `rest/handler.go:classify()` |
| **Error Handling** | **Low** | `sink.Publish` errors silently swallowed | `service/file.go:emit()` |

### Finding ERR-1: Inconsistent error wrapping

**Current State**: Some errors use `fmt.Errorf("%w: some detail", err)` while others use `fmt.Errorf("some detail: %w", err)`. The wrapping convention is inconsistent across the codebase, making stack unwinding via `errors.Is`/`errors.As` fragile.

**Example inconsistencies**:

```go
// Convention A (service/file_crud.go:146):
return repository.Object{}, fmt.Errorf("storage put: %w", err)

// Convention B (service/file_crud.go:316):
return nil, repository.Object{}, fmt.Errorf("%w: invalid Content-MD5 base64: %v", ErrInvalidArgs, err)

// Convention C (service/file_multipart.go:134):
return repository.Object{}, fmt.Errorf("repo write: %w", err)
```

**Recommended State**: Adopt a single convention throughout:
```go
// Prefer: wrapMessage + ": " + "%w"
return fmt.Errorf("storage put: %w", err)
```

**Effort**: S

### Finding ERR-2: Error details leaked to external clients

**Current State**: The `classify()` function in `rest/handler.go:322` returns `err.Error()` verbatim for the `InternalError` default case, potentially leaking internal paths, SQL schema details, or stack traces to API clients.

```go
default:
    return "InternalError", err.Error(), http.StatusInternalServerError
```

**Impact**: Information disclosure risk in production deployments. Internal error details belong in logs, not HTTP responses.

**Recommended State**:
```go
default:
    h.logger.Error("unexpected error serving request",
        "error", err,
        "request_id", mw.RequestIDFrom(r.Context()),
    )
    return "InternalError", "an internal error occurred", http.StatusInternalServerError
```

**Effort**: S

---

## 4. Logging

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Logging** | **Medium** | Sensitive config values may appear in startup logs | `cmd/server/main.go:configureAuthSecrets()` |
| **Logging** | **Medium** | Missing correlation IDs in some background workers | Various worker `Run()` methods |
| **Logging** | **Low** | Tenant context missing from some log entries | `service/file_crud.go:emit()` |

### Finding LOG-1: Sensitive data in startup logs

**Current State**: `configureAuthSecrets` in main.go logs configuration status but the slog attributes on startup like "JWT issuer pinning enabled" don't log secrets directly. However, `buildEmbedder` and `buildLLM` print endpoints and model names which could include API keys in the endpoint URL.

**Impact**: Low risk currently, but as the codebase evolves, sensitive values could leak.

### Finding LOG-2: Missing request context in background workers

**Current State**: Background workers (reconciler, lifecycle, retention) use `context.Background()` and thus lose the request correlation chain. When these workers emit log lines, there's no `request_id` to trace the operation back to a user action.

**Recommended State**: Pass derived contexts with request IDs:
```go
logger.With("request_id", requestID).Info(...)
```
Or use `slog.Group` to attach correlation info to background operations.

**Effort**: M

---

## 5. Testing Practices

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Testing** | **Critical** | `internal/webui` has zero tests | `internal/webui/web.go` |
| **Testing** | **High** | No contract tests for protocol adapters | `internal/api/rest/`, `internal/api/s3compat/` |
| **Testing** | **Medium** | Test files exceed 500 lines | `cli_test.go` (1440), `storage_test.go` (1120) |
| **Testing** | **Medium** | No fuzz tests for input parsing | All packages |
| **Testing** | **Low** | Integration tests require Docker (documented but not discoverable) | `internal/integration/` |

### Finding TST-1: Web UI has zero test coverage

**Current State**: `internal/webui/web.go` has no test file at all. The build tag check shows `? github.com/aero-vault/aero-vault/internal/webui [no test files]`.

**Impact**: The `/ui` endpoint is untested. Even a basic http handler test for redirect and file serving would catch regressions.

**Recommended State**: Add at minimum:
```go
func TestHandler_Redirect(t *testing.T) {
    h := Handler()
    ts := httptest.NewServer(h)
    defer ts.Close()
    // test /ui redirects to /ui/
    // test /ui/ returns 200 with HTML
    // test /ui/static/... serves embedded files
}
```

**Effort**: S

### Finding TST-2: No contract tests for protocol adapters

**Current State**: `storage/contract_test.go` exists and ensures all Storage backends pass the same suite. No equivalent exists for the REST or S3-compat handlers — each is tested in isolation without a shared contract ensuring consistent behavior.

**Impact**: Risk of behavioral drift between REST and S3 APIs for equivalent operations (e.g., conditional headers, range requests, error codes).

**Recommended State**: Create a shared integration test suite (`internal/api/contract_test.go`) that validates both adapters against the same scenarios:
```go
func TestContract_PutAndGet(t *testing.T) {
    // Run the same test against both REST and S3 adapters
}
```

**Effort**: M

---

## 6. Technical Debt

| Finding | Severity | Title | Location |
|---------|----------|-------|----------|
| **Tech Debt** | **High** | Duplicated protocol helper code | `rest/handler.go` vs `s3compat/handler.go` |
| **Tech Debt** | **High** | Middleware chain order differs from documented spec | `cmd/server/main.go:applyMiddleware()` |
| **Tech Debt** | **Medium** | No dependency injection framework (manual wiring in main) | `cmd/server/main.go` |
| **Tech Debt** | **Medium** | Circuit breaker config defaults embeded in main.go factory | `cmd/server/main.go:buildStorageFrom()` |

### Finding TBD-1: Middleware chain order inversion

**Current State**: AGENTS.md specifies middleware chain: `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`

But `applyMiddleware` in main.go applies in REVERSE order (last applied runs first):

```go
chain := []struct{
    name string
    mw   func(http.Handler) http.Handler
}{
    {"access_log", middleware.AccessLog(logger)},      // Applied first -> runs last
    {"concurrency", concurrencyMW},
    {"recoverer", middleware.Recoverer(logger)},
    {"otel", telemetry.HTTPMiddleware("aero-vault")},
    {"rate_limit", rl.Middleware()},
    {"tenant", middleware.Tenant},                     // Applied ~6th -> runs ~3rd
    {"auth", authReg.Middleware()},
    {"cors", middleware.CORS(...)},
    {"request_id", middleware.RequestID},              // Applied last -> runs first ✅
}
```

This means `RateLimit` and `OTel` middleware run BEFORE auth and tenant extraction. The rate limiter thus cannot distinguish tenants, and OTel middleware sees requests before tenant context is available. This contradicts the documented invariant I4 in AGENTS.md.

**Recommended State**: Reorder to match the spec, applying outermost middleware first:
```go
chain := []struct{...}{
    {"request_id", middleware.RequestID},
    {"cors", middleware.CORS(...)},
    {"auth", authReg.Middleware()},
    {"tenant", middleware.Tenant},
    {"rate_limit", rl.Middleware()},
    {"otel", telemetry.HTTPMiddleware("aero-vault")},
    {"recoverer", middleware.Recoverer(logger)},
    {"concurrency", concurrencyMW},
    {"access_log", middleware.AccessLog(logger)},
}
```

**Impact**: Rate limits are tenant-blind; OTel metrics miss tenant dimension. Fixing the order aligns runtime with documented architecture and enables tenant-aware rate limiting.

**Effort**: M (requires careful testing to ensure no regression)

### Finding TBD-2: Circuit breaker default fallback in main.go

**Current State**: `buildStorageFrom()` in main.go applies circuit-breaker defaults inline:

```go
if fc.CircuitBreaker.Enabled {
    if fc.CircuitBreaker.FailureThreshold <= 0 {
        fc.CircuitBreaker.FailureThreshold = 5       // magic number
    }
    if fc.CircuitBreaker.RecoveryTimeout <= 0 {
        fc.CircuitBreaker.RecoveryTimeout = 30 * time.Second  // magic number
    }
    if fc.CircuitBreaker.HalfOpenMaxRequests <= 0 {
        fc.CircuitBreaker.HalfOpenMaxRequests = 1    // magic number
    }
}
```

**Recommended State**: Move defaults into `config.go` next to the config struct, or into `storage/factory.go` where the config type is defined:

```go
// In config_storage.go or factory.go
func (c *CBConfig) defaults() {
    if c.FailureThreshold <= 0 { c.FailureThreshold = 5 }
    if c.RecoveryTimeout <= 0 { c.RecoveryTimeout = 30 * time.Second }
    if c.HalfOpenMaxRequests <= 0 { c.HalfOpenMaxRequests = 1 }
}
```

**Effort**: S

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Moderate (most funcs < 10) | < 10 | ✅ (most) / ⚠️ (some long funcs) |
| Function length | Most < 50 lines | < 50 lines | ✅ (most) |
| Production file size | **5 files > 500 lines** | < 500 lines | ❌ (see ORG-1) |
| Test coverage | ~70-80% estimated | > 80% | ⚠️ (webui at 0%) |
| Code duplication | ~3-4% estimated | < 5% | ⚠️ (duplicated header parsing) |
| Documentation coverage | ~50% on exported symbols | > 70% | ⚠️ (many exported symbols lack docs) |
| God types | `Repository` interface (50 methods) | None | ❌ (see ORG-2) |
| `utils/*` packages | None | None | ✅ |
| Circular dependencies | None detected | None | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Production files > 500 lines | Maintainability, onboarding | L | **P0** | Violates explicit AGENTS.md constraint |
| Repository God Interface (50 methods) | Testability, cohesion | L | **P0** | Affects every component that depends on Repository |
| Middleware chain order wrong | Runtime behavior differs from spec | M | **P1** | Rate limiting is tenant-blind |
| Duplicated header parsing (REST/S3) | Maintenance drag, drift risk | S | **P1** | Two implementations of the same logic |
| Error wrapping convention inconsistency | Debugging difficulty | S | **P2** | errors.Is/As behavior differs by function |
| Web UI zero test coverage | Regression risk for `/ui` endpoint | S | **P2** | Easy win |
| Error details leaked in API responses | Information disclosure | S | **P1** | InternalError case |
| No fuzz tests for input parsing | Robustness | M | **P2** | S3 XML parsing, metadata headers |
| Integration tests Docker-only | CI complexity | M | **P2** | Documented but limits automation |
| No contract tests between REST/S3 | Behavioral drift | M | **P2** | Both adapters should behave identically |

---

## Final Summary

### Overall Code Quality: **Good** (trending toward Needs Work without addressing file size violations)

### Critical Quality Issues (Must Fix Before Production)

1. **🔴 5 Production files exceed 500-line limit** — This is an explicit engineering constraint violation (AGENTS.md). Each file requires immediate splitting. The constraint violation itself would cause `make check` to fail.

2. **🔴 Repository God Interface** — The `Repository` interface with 50 methods violates ISP and makes testing unnecessarily difficult. Every mock must implement 50 methods even if the test only needs 2.

3. **🔴 Middleware chain order mismatch** — Rate limiting runs before tenant extraction, making per-tenant rate limits ineffective per the documented architecture.

### Maintainability Concerns

- **Duplication across protocol adapters**: The REST and S3-compat handlers duplicate header parsing, metadata extraction, conditional handling, and CORS logic. Any bug fix must be applied in two places.
- **Main goroutine complexity**: `cmd/server/main.go` at 861 lines assembles every component. Adding a new worker or backend requires touching this single file.
- **No standardized error wrapping**: Inconsistent error wrapping patterns make debugging harder, especially when errors propagate through 3-4 layers.

### Quick Wins (S Effort)

1. **Add webui tests** — Wrapping a `httptest.NewServer` around the handler gives immediate coverage
2. **Extract shared header helpers** into `internal/api/internal/` — Eliminates the duplicated `extractMetaHeaders` / `extractMetadataHeaders`
3. **Redact InternalError details** — Replace `err.Error()` with a generic message in the classify function
4. **Move circuit breaker defaults** from `main.go` into config struct initialization

### Recommendations for New Contributors

1. **Look at `internal/service/file.go` first** — It's the cleanest, best-documented package and the core business logic
2. **Avoid modifying `rest/handler.go` or `s3compat/handler.go`** — They're already over the size limit; any addition should go into a new file
3. **Watch for middleware ordering** — Understand that middleware runs in reverse declaration order; double-check `applyMiddleware` before adding new middleware
