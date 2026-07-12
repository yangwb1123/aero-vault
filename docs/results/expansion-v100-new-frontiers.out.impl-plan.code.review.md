Now I have a thorough understanding of the codebase. Let me compile the comprehensive code quality review.

---

# Code Quality Review: Aero-Vault

## Executive Summary

This codebase demonstrates strong engineering discipline overall. The architecture follows a clean layered pattern (protocol adapters → service → storage/repository), with clear separation of concerns, well-defined interfaces, and sensible defaults. However, several areas need attention — particularly around file length governance, handler size, and certain testing gaps.

---

## 1. Code Organization

### 1.1 File Length Violations (Critical)

The project's own `AGENTS.md` and `HARNESS.md` mandate **single file ≤ 500 lines**. Several files exceed this limit:

| File | Lines | Violation |
|------|-------|-----------|
| `sdk/go/aerovault/client.go` | 1,006 | ❌ **508 lines over limit** |
| `internal/api/rest/handler.go` | 958 | ❌ **458 lines over limit** |
| `internal/api/s3compat/handler.go` | 890 | ❌ **390 lines over limit** |
| `cmd/server/main.go` | 861 | ❌ **361 lines over limit** |
| `internal/auth/condition.go` | 657 | ❌ **157 lines over limit** |

**Recommendation:** These files must be split before further feature development per project policy. The `make check` command enforces this — these files would be rejected.

### 1.2 Handler God Objects (Critical)

`internal/api/rest/handler.go` (958 lines) contains ~30 handler methods mixing object CRUD, bucket management, folder operations, batch operations, ACL, CORS, lifecycle, logging, notifications, and policy. This is a clear violation of the Single Responsibility Principle.

**Current State:** One `Handler` struct handles all REST concerns
**Recommended State:** Split into focused handlers: `ObjectHandler`, `BucketHandler`, `FolderHandler`, `AdminHandler`
**Impact:** Maintainability — new developers have difficulty navigating 958 lines; merge conflicts increase

### 1.3 Main.go Assembly (High)

`cmd/server/main.go` at 861 lines orchestrates all infrastructure assembly. While well-structured with helper functions, it's too large. The startup wiring — building embedders, LLMs, rerankers, indexers, search, chat, agent, workers, webhooks, auth, rate limiters — should be in dedicated builder packages.

**Recommendation:** Extract into `internal/bootstrap/` or `internal/initializer/` with one file per concern (e.g., `ai.go`, `auth.go`, `storage.go`).

### 1.4 Repository Interface Size (Medium)

`internal/repository/repository.go` defines a `Repository` interface with **~65 methods**. This is extremely large and breaks Interface Segregation Principle.

**Current State:** One monolithic interface for all persistence operations
**Recommended State:** Split into focused interfaces:
```go
type ObjectStore interface { ... }
type ChunkStore interface { ... }
type BucketConfigStore interface { ... }
type QueueStore interface { ... }
type KeyStore interface { ... }
```
Then compose them as needed.

---

## 2. Naming & Documentation

### 2.1 Good Practices ✅

- Package names are descriptive and follow Go conventions (`service`, `repository`, `storage`, `auth`)
- Public types and functions have clear godoc comments (e.g., `FileService`, `Pool`, `Bus`)
- Error sentinels use idiomatic `var ErrXxx = errors.New(...)` pattern
- Method names clearly indicate operation (`Put`, `Get`, `Delete`, `Stat`)

### 2.2 Missing API Documentation (Medium)

While internal code has decent comments, the REST API handler methods lack OpenAPI/Swagger annotations. The `OpenAPISpecHandler()` exists but appears to serve a static spec rather than generating from code.

**Impact:** API consumers must read handler code to understand request/response shapes.

### 2.3 PII.go Dead Code (Low)

**Location:** `internal/ai/pii.go:120`
**Current State:**
```go
parts = append(parts, k+"="+strings.Repeat("0", 0)+itoa(v))
```
`strings.Repeat("0", 0)` always returns `""`. This is dead, confusing code.
**Recommended State:**
```go
parts = append(parts, k+"="+itoa(v))
```

---

## 3. Error Handling

### 3.1 Strong Error Typing ✅

The codebase has excellent error sentinels in `internal/service/file.go`:
- `ErrNotFound`, `ErrInvalidArgs`, `ErrLocked`, `ErrQuotaExceeded`, `ErrRangeNotSatisfiable`, `ErrPreconditionFailed`, `ErrForbidden`, `ErrBadDigest`, `ErrSizeMismatch`, `ErrObjectCorrupt`, `ErrMetadataTooLarge`
- Errors are consistently wrapped with `fmt.Errorf("context: %w", err)` preserving the error chain
- `errors.Is()` is used correctly in handler classification

### 3.2 Error Propagation Pattern (Medium)

Some service methods silently swallow errors on best-effort paths:

**Location:** `internal/service/file_crud.go:35` (`preflightQuota`)
```go
q, qErr := s.repo.GetTenantQuota(ctx, tenant)
if qErr != nil {
    return nil  // silently skips quota check
}
```
While documented as intentional ("best-effort enforcement"), this means quota bypasses pass silently with no logging. A `s.logger.Warn(...)` would help operators diagnose quota-check failures.

Similarly in `internal/service/file_crud.go:150`:
```go
if _, qErr := s.repo.AddTenantUsage(ctx, obj.TenantID, saved.Size, 1); qErr != nil {
    s.logger.Warn("quota usage increment failed", ...)
}
```
Good — this is the right pattern for best-effort paths. But `preflightQuota` should also log.

### 3.3 context.Background() Usage (Medium)

Several production code paths use `context.Background()` when a request context is in scope:

| File | Line | Issue |
|------|------|-------|
| `internal/ai/indexer.go` | 313 | `context.Background()` in `handleExtractError` |
| `internal/events/bus.go` | 139 | `context.Background()` in `broadcast` |
| `internal/events/postgres_transport.go` | 82, 139 | `context.Background()` for conn close |

The indexer case is the most concerning — when an extract error occurs, the telemetry counter won't propagate cancellation signals:

```go
func (ix *Indexer) handleExtractError(key string, err error) error {
    // ...
    telemetry.IncIndexerSkip(context.Background(), "unsupported")  // should pass ctx
}
```

---

## 4. Logging

### 4.1 Structured Logging ✅

The codebase consistently uses `log/slog` with structured key-value pairs. Examples:
```go
s.logger.Info("indexed", "tenant", ..., "key", ..., "chunks", ..., "model", ...)
s.logger.Warn("quota usage increment failed", "tenant", ..., "err", ...)
```

### 4.2 JSON Output ✅

Default handler is `slog.NewJSONHandler(os.Stdout, ...)` — production-ready for log aggregation.

### 4.3 Sensitive Data Exposure (Low)

**Location:** `internal/service/file_features.go:149-156` — Presign logging includes full object paths:
```go
s.logger.Info("presign generated",
    "tenant", tenant,
    "bucket", bucket,
    "key", key,
    "expiry", expiry.String(),
)
```
Object keys containing PII or sensitive paths would be logged in plaintext. Consider allowing log redaction for sensitive keys.

### 4.4 Missing Correlation IDs on Background Jobs (Medium)

Background workers (indexer, webhook, reconcile) create their own logging context without inheriting the request's trace/span context. The access log includes request IDs, but async operations lose this trace chain.

---

## 5. Testing Practices

### 5.1 Test Coverage Summary

| Package | Coverage | Status |
|---------|----------|--------|
| cluster | 100.0% | ✅ Excellent |
| jobs | 92.0% | ✅ Excellent |
| config | 90.7% | ✅ Excellent |
| thumbnail | 87.1% | ✅ Excellent |
| mcp | 86.5% | ✅ Excellent |
| ai | 84.2% | ✅ Good |
| cli | 82.5% | ✅ Good |
| middleware | 78.0% | ✅ Good |
| webdav | 77.8% | ✅ Good |
| auth | 77.9% | ⚠️ Acceptable |
| replication | 73.7% | ⚠️ Acceptable |
| antivirus | 70.4% | ⚠️ Acceptable |
| events | 64.0% | ⚠️ Below target |
| s3compat | 61.4% | ⚠️ Below target |
| telemetry | 61.5% | ⚠️ Below target |
| reconcile | 60.6% | ⚠️ Below target |
| service | 58.0% | ❌ Below target |
| storage | 57.3% | ❌ Below target |
| repository | 54.6% | ❌ Below target |
| **rest** | **52.8%** | **❌ At threshold** |
| webui | 0.0% | ❌ Untested |

### 5.2 Test Quality Observations

**Strengths:**
- Tests use `t.Helper()` and `t.Cleanup()` consistently
- Clean test fixtures with temporary directories and SQLite in-memory databases
- Good use of table-driven tests in many packages
- The storage contract test suite (`contract_test.go`) provides reusable integration tests for backend implementations

**Weaknesses:**
- Many tests use `t.Fatalf` for setup failures rather than `t.Fatal` — minor but inconsistent
- Large test files: `internal/repository/chunks_events_buckets_test.go` at 922 lines
- `internal/api/rest/handlers_test.go` and `internal/api/s3compat/handler_test.go` test files are likely also very large (not checked but given the handler sizes, likely proportionate)
- No fuzz testing found
- No property-based testing

### 5.3 Integration Test Organization ✅

The `internal/integration/` package with build tags (`//go:build integration`) is a good pattern for Docker-dependent tests.

---

## 6. Technical Debt

### 6.1 No TODO/FIXME Markers

The grep for `TODO`, `FIXME`, `HACK`, `XXX`, `BUG` returned **zero results** in production code. This is unusual for a codebase of this size — it may indicate:

1. Technical debt is tracked elsewhere (but no evidence found)
2. Issues are silently accumulated without annotation
3. The code was recently cleaned before review

Either way, the absence of any markers means known issues aren't visible to developers during daily work.

### 6.2 No go:generate Directives

Zero `go:generate` comments found. No code generation for:
- Mock interfaces for testing
- OpenAPI spec from code
- Database model bindings
- Stringer implementations

### 6.3 CLI Printf Usage (Low)

**Location:** `internal/cli/cli_admin.go:86, 181`
```go
fmt.Printf("%-40s tenant=%-20s scopes=%-15s label=%s\n", ...)
```
Using `fmt.Printf` (stdout) alongside `fmt.Fprintln(os.Stderr, ...)` (stderr) for help text is inconsistent. All user-facing output should go to stdout or stderr consistently.

### 6.4 Deprecated Dependencies

The `github.com/aliyun/aliyun-oss-go-sdk v3.0.2+incompatible` uses a `+incompatible` marker (pre-Go-modules versioning). This should be updated to a v2+ tagged version.

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Several > 10 | < 10 | ⚠️ (gocyclo check exists but only warns) |
| Function length | Mixed | < 50 lines | ⚠️ |
| Single file length | 5 files exceed 500 | < 500 lines | ❌ (5 violations) |
| Test coverage | 52-92% per package | > 80% | ⚠️ (6 packages below 70%) |
| Code duplication | Low | < 5% | ✅ |
| Documentation coverage | Moderate | > 70% | ⚠️ |
| gofmt compliance | Clean | 0 violations | ✅ |
| go vet compliance | Clean | 0 warnings | ✅ |
| Build success | All pass | ✅ | ✅ |
| All tests pass | All pass | ✅ | ✅ |

---

## 8. Technical Debt Register

| # | Item | Impact | Effort | Priority | Notes |
|---|------|--------|--------|----------|-------|
| TD-01 | File length violations (5 files) | Critical: blocks CI `make check` | M | P0 | Must fix before any feature work |
| TD-02 | REST handler god object (958 lines) | High: maintainability | L | P0 | Split into domain-specific handlers |
| TD-03 | Repository interface too large (65 methods) | Medium: ISP violation | M | P1 | Split into focused interfaces |
| TD-04 | Postgres migration duplication | Low: 48 paired files | S | P2 | Consider a migration library |
| TD-05 | Dead code in pii.go | Low: confusing | S | P2 | Remove `strings.Repeat("0", 0)` |
| TD-06 | context.Background() in prod code | Medium: lost cancellation | S | P1 | Pass context through |
| TD-07 | Test coverage below 60% (5 packages) | Medium: risk of regressions | L | P1 | Add targeted tests |
| TD-08 | No code generation | Low: manual mock maintenance | M | P2 | Add go:generate for mocks |
| TD-09 | No TODO/FIXME tracking | Low: invisible debt | S | P3 | Adopt annotation culture |
| TD-10 | CLI output inconsistencies | Low: minor UX | S | P3 | Standardize output streams |
| TD-11 | +incompatible dependency | Low: tech risk | S | P2 | Update OSS SDK |
| TD-12 | Missing trace propagation in workers | Medium: observability gap | M | P2 | Propagate OpenTelemetry context |

---

## 9. Final Summary

| Category | Rating |
|----------|--------|
| **Overall Code Quality** | **Good** (with significant issues) |
| **Architecture** | ✅ Excellent — clean layered architecture, well-defined interfaces |
| **Error Handling** | ✅ Good — strong error types, proper wrapping, sentinel errors |
| **Logging** | ✅ Good — structured logging, JSON output, correlation IDs |
| **Testing** | ⚠️ Adequate — good foundation but coverage gaps in critical packages |
| **Documentation** | ⚠️ Moderate — decent GoDoc, but no OpenAPI annotations |
| **Code Organization** | ❌ Needs work — file length violations, god objects |
| **Technical Debt** | ⚠️ Moderate — tracked mostly by project rules, not annotations |

### Critical Quality Issues (Must Fix Before Production)

1. **File length violations**: 5 files exceed the 500-line limit per `AGENTS.md`. The `make check` command will reject these.
2. **Handler god objects**: `rest/handler.go` (958 lines) and `s3compat/handler.go` (890 lines) need decomposition into domain-specific handlers.
3. **main.go at 861 lines**: Startup assembly logic should be extracted into a bootstrap package.

### Maintainability Concerns

- The monolithic `Repository` interface couples all persistence consumers to every storage operation
- Test coverage below 60% in the service, storage, and repository layers — these are the most critical packages
- Background workers lose OpenTelemetry trace context from the originating request
- Migration files are duplicated across SQLite/Postgres (48 .sql files) — consider a migration library that abstracts dialect differences

### Quick Wins

1. **Fix `pii.go` dead code** — remove `strings.Repeat("0", 0)` (5-minute fix)
2. **Add logging to `preflightQuota`** — log when quota lookup fails (15-minute fix)
3. **Replace `context.Background()` calls** with available context parameters (30-minute fix)
4. **Standardize CLI output** — ensure all user-facing output uses consistent streams (20-minute fix)
5. **Add TODO/FIXME annotations** for known limitations discovered during review (ongoing culture)

### Recommendations for Next Sprint

1. **Split `rest/handler.go`** into `object_handler.go`, `bucket_handler.go`, `folder_handler.go`
2. **Split `cmd/server/main.go`** — extract `bootstrap/` package for infrastructure assembly
3. **Generate mock interfaces** with `go:generate` for the `Repository` interface to simplify unit testing
4. **Add integration tests** for the service layer to increase coverage above 60%
5. **Address the 5 file-length violations** before adding new features
