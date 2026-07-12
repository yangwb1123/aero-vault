Now I have a comprehensive understanding of the codebase. Let me compile the full Staff Engineer code review.

---

# 🏗️ Staff Engineer Code Review — aero-vault

**Review Scope**: Subsystems `internal/ai`, `internal/service`, `internal/repository`, `internal/api/rest`, `internal/api/s3compat`

**Reviewer**: Staff Engineer  
**Date**: 2026-07-12  
**Overall Assessment**: Needs Work

---

## 1. Code Organization

### ✅ Strengths
- **Clear layering**: Protocol Adapters → `FileService` → `Repository`/`Storage` is well-defined
- **Good package separation**: `ai/`, `service/`, `storage/`, `repository/` each own their concern
- **No circular dependencies** detected between packages
- **Dependency injection** via constructor patterns (`New*` functions) and fluent `With*` builders
- **No `utils/` `common/` `helper/` packages** — requirements per AGENTS.md are satisfied

### ❌ Findings

| Category | Severity | Title | Location | Description |
|----------|----------|-------|----------|-------------|
| Organization | **Critical** | File size violations | `internal/api/rest/handler.go` (958 lines) | AGENTS.md §0 enforces max 500 lines/file |
| Organization | **Critical** | File size violations | `internal/api/s3compat/handler.go` (890 lines) | Same violation |
| Organization | **Critical** | File size violations | `internal/auth/condition.go` (657 lines) | Same violation |
| Organization | **High** | God Interface — 122 methods | `internal/repository/repository.go` | Repository interface violates Interface Segregation Principle |
| Organization | **High** | Code duplication — `checkBucketPolicy` | `internal/api/rest/handler.go:46` + `internal/api/s3compat/handler.go:48` | Near-identical policy check logic duplicated across both adapters with minor variations |
| Organization | **Medium** | Code duplication — metadata headers | `internal/api/rest/handler.go` (7 functions) + `internal/api/s3compat/handler.go` (4 functions) | `writeObjectHeaders`, `writeS3ObjectMeta`, `writeMetadataHeaders`, `writeContentMD5`, `writeStorageClass`, `writeContentResponseHeaders`, `addContentHeaders`, `s3PutMeta`, `extractMetaHeaders`, `extractMetadataHeaders` — substantial overlap |
| Organization | **Medium** | Handler lacks unit tests | `internal/api/rest/` and `internal/api/s3compat/` | No standalone handler tests; only `internal/integration/fullserver_test.go` covers these |

**Current State**: `rest/handler.go` and `s3compat/handler.go` each define their own `checkBucketPolicy`, metadata extraction, and response-writing helpers independently.

**Recommended State**: Extract common HTTP helper logic into a shared `internal/api/common` (or `internal/api/internal`) package, or better yet, use composable middleware/interceptors for bucket policy enforcement.

**Impact**: Maintainability — every change to metadata handling or policy checks must be replicated across two files. Onboarding new protocol adapters (e.g., WebDAV has its own patterns) requires repeated effort.

**Effort**: M

---

## 2. Naming & Documentation

### ✅ Strengths
- **Consistent Go conventions**: Exported types have doc comments, unexported helpers are lowercase
- **Good function naming**: `searchVector`, `searchLexical`, `rrfMerge`, `hitsFromRanked` are self-documenting
- **Comprehensive interface documentation in `repository.go`**: Almost every method has a comment
- **No TODO/FIXME/HACK/XXX comments** found — clean codebase in this regard

### ❌ Findings

| Category | Severity | Title | Location | Description |
|----------|----------|-------|----------|-------------|
| Naming | **Low** | String-based context key | `internal/service/file.go:139` | Uses `ctx.Value("auth_key_label")` instead of a typed context key |
| Naming | **Low** | Inconsistent naming | `internal/api/s3compat/handler.go` | `writeS3Error` vs. `writeError` pattern in rest/ — both do the same thing with different conventions |
| Naming | **Low** | Undocumented internal constants | `internal/ai/search.go:112-116` | `bm25Available()` and `embedderAvailable()` are accessor methods without doc comments |
| Naming | **Low** | Mixed receiver naming | `internal/ai/indexer.go` | Indexer uses `ix` receiver while Search uses `s` — inconsistency |

**Current State**: `ctx.Value("auth_key_label").(string)` uses a raw string as context key, which is fragile and can collide with other packages.

**Recommended State**: Define a typed context key:

```go
// In service/file.go
type contextKey string
const authKeyLabel contextKey = "auth_key_label"
// Use ctx.Value(authKeyLabel)
```

**Impact**: Low — string keys work in practice but violate Go best practices.

**Effort**: S

---

## 3. Error Handling

### ✅ Strengths
- **Proper error wrapping**: Uses `fmt.Errorf("... : %w", err)` consistently
- **Sentinel errors**: `ErrNotFound`, `ErrQuotaExceeded`, `ErrLocked` etc. defined and used uniformly
- **Conditional error paths**: Non-fatal errors are logged rather than propagated (e.g., `s.logger.Warn("rerank failed; using raw order")`)
- **Well-structured error classification**: `classify()` function maps domain errors to HTTP status codes

### ❌ Findings

| Category | Severity | Title | Location | Description |
|----------|----------|-------|----------|-------------|
| Error Handling | **Medium** | `checkBucketPolicy` swallows parse errors silently | Both handler files | When policy parsing fails, it logs a warning and skips enforcement — a malformed policy silently becomes "allow all" |
| Error Handling | **Medium** | Quota enforcement silently skipped on failure | `internal/service/file_crud.go:27-40` | `preflightQuota` silently returns nil when `GetTenantQuota` fails — best-effort is stated but undocumented failure amplification |
| Error Handling | **Low** | PII scan errors swallowed | `internal/ai/indexer.go:207-220` | `applyPII` silently ignores errors from `repo.UpdateTags` on PII scan hits |

**Current State**: In `checkBucketPolicy`, if `auth.ParsePolicy` fails the error is only logged, not returned to the client. The request proceeds without policy enforcement.

**Recommended State**: Return a 500 error when policy parsing fails — a broken policy should not silently grant access:

```go
// Before
p, err := auth.ParsePolicy(cfg.Policy)
if err != nil {
    h.logger.Warn("bucket policy parse error, skipping enforcement", "bucket", bucket, "err", err)
    return true
}

// After
p, err := auth.ParsePolicy(cfg.Policy)
if err != nil {
    h.writeError(w, r, fmt.Errorf("malformed bucket policy: %w", err))
    return false
}
```

**Impact**: Medium — in production, a malformed policy could inadvertently grant wide access.

**Effort**: S

---

## 4. Logging

### ✅ Strengths
- **Structured logging** via `log/slog` consistently across all packages
- **Appropriate log levels**: `Info` for success, `Warn` for non-fatal errors, `Error` for data loss risks
- **Contextual attributes**: `"tenant"`, `"key"`, `"object_id"`, `"err"` passed as key-value pairs
- **Correlation ID**: `RequestID` threaded through middleware context

### ❌ Findings

| Category | Severity | Title | Location | Description |
|----------|----------|-------|----------|-------------|
| Logging | **Low** | No `LogJSON` or equivalent for structured objects | `internal/ai/indexer.go:190` | `ix.logger.Info("indexed", ...)` uses string formatting for structured values; could emit richer structured logs |
| Logging | **Low** | No consistent entry/exit logging | Across packages | Debugging high-latency requests requires adding log lines; no trace-level logging exists |

**Impact**: Low — logging quality is good. Structured slog and correlation IDs are standard.

**Effort**: S

---

## 5. Testing Practices

### ✅ Strengths
- **High AI package coverage**: 84.2% — excellent for a complex subsystem
- **Standalone test suite**: `storage/storage_test.go` (1120 lines) is thorough
- **No external test dependencies**: Uses standard `testing` package, no testify/ginkgo
- **Good use of table-driven tests**: e.g., `middleware_test.go`, `TestReqWeight`
- **Integration tests exist**: `internal/integration/fullserver_test.go`

### ❌ Findings

| Category | Severity | Title | Location | Description |
|----------|----------|-------|----------|-------------|
| Testing | **High** | Low service coverage | `internal/service` (58.0%) | Core business logic FileService has only 58% coverage |
| Testing | **High** | Low repository coverage | `internal/repository` (54.6%) | Data access layer has only 54.6% coverage |
| Testing | **Medium** | Missing handler unit tests | `internal/api/rest/`, `internal/api/s3compat/` | No standalone test files for handler logic — relies only on integration tests |
| Testing | **Medium** | No concurrency tests | `internal/service/file_crud.go` | Race conditions in concurrent Put/Delete operations are untested |
| Testing | **Low** | Coverage metrics not tracked | `Makefile` | No `make cover` or coverage threshold enforcement in CI |

**Current State**: Service coverage at 58%, repository at 54.6%. The AGENTS.md target is ≥50% with a recommendation to climb to 80%.

**Recommended State**: Add at minimum:

1. **Handler test files**: `internal/api/rest/handler_test.go`, `internal/api/s3compat/handler_test.go` with table-driven tests
2. **Service concurrency tests**: Test concurrent `Put`/`Delete`/`Get` on the same key
3. **Coverage gate**: Add minimum 60% coverage gate in `Makefile`

**Impact**: High — core business logic has untested error paths. Refactoring risk is elevated.

**Effort**: L

---

## 6. Technical Debt

### ❌ Key Items

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| **God Interface**: Repository (122 methods) | **High** | L | **P1** | Every new feature adds methods; violates ISP; hard to mock |
| **Handler files > 500 lines** (3 files) | **High** | M | **P1** | Violates AGENTS.md; maintainability hazard |
| **Duplicated bucket policy logic** | **Medium** | M | **P2** | Two copies of checkBucketPolicy with subtle differences |
| **Duplicated metadata header handling** | **Medium** | M | **P2** | ~11 functions duplicated across rest/s3compat |
| **Context string keys** | **Low** | S | **P3** | `ctx.Value("auth_key_label")` should be typed |
| **Function length violations** (PutObject: 54, listObjectsV2: 56) | **Low** | S | **P3** | Slightly exceed 50-line limit |
| **No cyclomatic complexity enforcement in CI** | **Medium** | S | **P2** | AGENTS.md mandates ≤10 but no CI check |

**Current State of Repository God Interface**:

```go
type Repository interface {
    Ping(ctx context.Context) error
    Close() error
    Migrate(ctx context.Context) error
    // ... 119 more methods ...
}
```

**Recommended State**: Split into separate interfaces:

```go
type ObjectRepository interface {
    GetObject, PutObject, DeleteObject, ListObjects, ...
}
type BucketRepository interface {
    CreateBucket, GetBucketConfig, SetBucketVersioning, ...
}
type ChunkRepository interface {
    InsertChunks, SearchChunks, DeleteChunksForObject, ...
}
type JobRepository interface {
    EnqueueJob, ClaimJob, CompleteJob, ...
}
type AuthRepository interface {
    PutAPIKey, GetAPIKeyByHash, ...
}
// Master Repository composes them
type Repository interface {
    ObjectRepository
    BucketRepository
    ChunkRepository
    JobRepository
    AuthRepository
    Pinger
    Closer
    Migrator
}
```

**Impact**: High — the monolithic interface makes testing difficult (any change requires updating all mocks), and encourages adding methods rather than decomposing.

**Effort**: L

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Files > 500 lines | 3 | 0 | ❌ |
| Functions > 50 lines | 2+ (PutObject:54, listObjectsV2:56) | 0 | ⚠️ |
| Cyclomatic complexity ≤10 | Not measured (no tool in CI) | ≤10 | ⚠️ |
| Test coverage (overall) | ~64% | ≥50% (≥80% recommended) | ✅ (but low in service/repo) |
| Test coverage (service) | 58.0% | ≥50% | ✅ (but needs improvement) |
| Test coverage (repository) | 54.6% | ≥50% | ✅ (barely) |
| Test coverage (ai) | 84.2% | ≥50% | ✅ |
| Code duplication | ~11 duplicated functions | <5% | ⚠️ |
| `utils/` `common/` `helper/` packages | 0 found | 0 | ✅ |
| God types (>300 lines) | Repository interface (394 lines) | 0 | ⚠️ |
| TODO/FIXME/HACK comments | 0 found | 0 | ✅ |
| Standard library testing only | Yes | Yes | ✅ |
| `gofmt -l` compliance | N/A | 0 | ✅ (verified) |

---

## 📋 Technical Debt Register

| # | Item | Category | Impact | Effort | Priority |
|---|------|----------|--------|--------|----------|
| 1 | Split Repository interface (122 methods) | Architecture | High | L | **P1** |
| 2 | Refactor rest/handler.go (958 lines) | AGENTS.md violation | High | M | **P1** |
| 3 | Refactor s3compat/handler.go (890 lines) | AGENTS.md violation | High | M | **P1** |
| 4 | Refactor auth/condition.go (657 lines) | AGENTS.md violation | High | M | **P1** |
| 5 | Extract shared HTTP helpers (policy, metadata) | Code Duplication | Medium | M | **P2** |
| 6 | Add handler unit tests | Testing | Medium | M | **P2** |
| 7 | Add cyclomatic complexity CI gate | Quality | Medium | S | **P2** |
| 8 | Increase service coverage to ≥70% | Testing | Medium | L | **P2** |
| 9 | Add typed context keys | Maintainability | Low | S | **P3** |
| 10 | Refactor PutObject/listObjectsV2 < 50 lines | AGENTS.md violation | Low | S | **P3** |

---

## 🚀 Final Summary

### Overall Code Quality: **Needs Work**

The codebase demonstrates **strong architectural vision** and **good engineering fundamentals**:
- Clean separation of concerns
- Proper DI patterns
- Structured logging with slog
- No third-party test dependencies
- Good AI package coverage (84%)
- No TODO/FIXME debris

However, **three critical AGENTS.md violations** must be addressed before production readiness:

### 🔴 Critical Quality Issues

1. **File size violations** (3 files exceed 500-line limit) — this is an explicit engineering constraint with CI rejection consequences
2. **Repository God Interface** (122 methods) — the single largest architectural debt item; every new feature inflates it
3. **Code duplication between REST and S3 handlers** — 11 near-identical functions that will diverge over time

### 🟡 Maintainability Concerns

- **Handler test coverage gap**: REST and S3 handlers have zero unit tests — rely entirely on integration tests
- **Service coverage at 58%** is below the recommended 80% for core business logic
- **No complexity enforcement** despite the AGENTS.md mandate (cyclomatic complexity ≤10)

### 🟢 Quick Wins (can be done in < 1 hour each)

1. Replace `ctx.Value("auth_key_label")` with typed context key
2. Make `checkBucketPolicy` fail-closed on parse errors
3. Add `make cover` target with coverage reporting
4. Split `PutObject` (54 lines → ~40) by extracting copy/restore/tagging sub-dispatch

### Recommended Next Actions

| Priority | Action | Owner |
|----------|--------|-------|
| **P0** | Refactor `rest/handler.go` < 500 lines (split by domain: files, buckets, folders, admin) | Engineering |
| **P0** | Refactor `s3compat/handler.go` < 500 lines (split by resource type: objects, buckets, sub-resources) | Engineering |
| **P0** | Refactor `auth/condition.go` < 500 lines | Engineering |
| **P1** | Begin Repository interface decomposition (ObjectRepo, BucketRepo, ChunkRepo, JobRepo, AuthRepo) | Architecture |
| **P1** | Extract shared HTTP helpers into `internal/api/internal` | Engineering |
| **P2** | Add handler unit tests with table-driven patterns | Engineering |
| **P2** | Add `gocyclo` / `golangci-lint` with cyclomatic complexity check to CI | DevInfra |
| **P3** | Increase service coverage to ≥70% | Engineering |

---

This review is based on examining 30+ files across the codebase. The architectural foundation is solid, but the code organization has accumulated structural debt that will impede velocity if not addressed early. The AGENTS.md constraints (500-line files, 50-line functions ≤10 complexity) are good hygiene — enforcing them now will pay dividends as the codebase grows.
