Now I have a thorough understanding of the codebase. Let me compile the comprehensive Staff Engineer code review.

---

# Staff Engineer Code Review — aero-vault

**Review Date:** 2026-07-12  
**Reviewer:** Staff Engineer  
**Repository:** `/home/u1/aero-vault`  
**Scope:** Full codebase review (243 Go source files, ~53K lines)

---

## Code Quality Metrics Summary

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Single file ≤ 500 lines** (non-test) | 5 violations | ≤ 500 | ❌ |
| **Single function ≤ 50 lines** | ~2-3 violations (see below) | ≤ 50 | ⚠️ |
| **Cyclomatic complexity ≤ 10** | Not measured (no gocyclo) | ≤ 10 | ⚠️ |
| **Test coverage (overall)** | 64.0% | ≥ 80% | ❌ |
| **Code duplication** | Moderate (REST↔S3 handlers) | < 5% | ⚠️ |
| **No `utils/` `common/` `helper/`** | ✅ None found | — | ✅ |
| **No God types (≤ 300 lines)** | ❌ Several God types | ≤ 300 | ❌ |
| **gofmt compliance** | ✅ | — | ✅ |
| **go vet pass** | ✅ | — | ✅ |
| **Tests pass** | ✅ | — | ✅ |
| **No TODO/FIXME litter** | ✅ (zero found) | — | ✅ |

---

## Detailed Findings

### 🔴 Category: Code Organization | Severity: Critical | Title: File Size Limit Violations

**Location:** Multiple files (see table)

**Description:** The `AGENTS.md` engineering constraint mandates single files ≤ 500 lines. Five non-test files and 15 test files exceed this limit. This directly violates the "stop development → auto refactor" rule.

| File | Lines | Severity |
|------|-------|----------|
| `sdk/go/aerovault/client.go` | 1,006 | Critical |
| `internal/api/rest/handler.go` | 958 | Critical |
| `internal/api/s3compat/handler.go` | 890 | Critical |
| `cmd/server/main.go` | 861 | Critical |
| `internal/auth/condition.go` | 657 | Critical |
| `internal/cli/cli_test.go` | 1,440 | High (test) |
| `internal/storage/storage_test.go` | 1,120 | High (test) |
| `internal/repository/chunks_events_buckets_test.go` | 922 | High (test) |

**Current State:** Files have grown organically past the limit without being split.

**Recommended State:** Each file exceeding 500 lines should be split. For example:
- `rest/handler.go` → `handler.go`, `bucket_config.go`, `bucket_lifecycle.go`
- `s3compat/handler.go` → `handler.go`, `bucket_config.go`, `multipart.go`
- `auth/condition.go` → Split into `condition_string.go`, `condition_numeric.go`, `condition_ip.go`, `condition_date.go`
- `cmd/server/main.go` → Extract `builder.go`, `wiring.go` from orchestrator logic

**Impact:** Violates AGENTS.md constraint. Impedes maintainability, readability, and onboarding.

**Effort:** L (requires careful decomposition)

---

### 🔴 Category: Code Organization | Severity: Critical | Title: God Types Approaching

**Location:**
- `internal/api/rest/handler.go` — `Handler` struct + 43 methods (~958 lines)
- `internal/api/s3compat/handler.go` — `Handler` struct + 48 methods (~890 lines)
- `cmd/server/main.go` — 22 function declarations in one file (~861 lines)

**Description:** The `AGENTS.md` mandates "no God type (≤ 300 lines per type)." The REST `Handler` type has 43 methods spanning 958 lines. While the type definition itself is small, the file containing all its methods far exceeds the 300-line limit. Similarly, `s3compat.Handler` has 48 methods. These are effectively God objects that handle too many concerns.

**Current State:** One Handler struct with all REST operations (CRUD, bucket configs, lifecycle, notifications, logging, folders, tags, ACLs, presign, restore, batch operations).

**Recommended State:** Decompose handlers by sub-domain:
```
internal/api/rest/
├── handler.go          # Core CRUD (Put, Get, Delete, Head, List)
├── bucket_config.go    # Bucket-level config (policy, CORS, logging, versioning, notifications)
├── bucket_lifecycle.go # Lifecycle operations
├── folders.go          # Folder operations
├── batch.go            # Batch operations
└── util.go             # Shared utilities (writeJSON, extractMetadataHeaders, etc.)
```

**Impact:** Low maintainability, difficult to test, high cognitive load for new developers.

**Effort:** L

---

### 🔴 Category: Code Organization | Severity: High | Title: `main.go` Orchestration Monolith

**Location:** `cmd/server/main.go` (861 lines)

**Description:** `main.go` violates the 500-line rule and contains all wiring logic inline rather than in a structured builder pattern. It has 22 top-level functions including `run()`, `runMCP()`, `buildRouter()`, `buildPrometheus()`, `buildStorage()`, `buildStorageFrom()`, `buildEmbedder()`, `buildLLM()`, `buildReranker()`, `buildScanner()`, `buildAIComponents()`, `setupVectorIndexes()`, `setupLexicalCache()`, `setupBM25Search()`, `setupChatAndAgent()`, `buildIndexer()`, `registerIndexerJobs()`, `startReindexOnStartup()`, `buildBackgroundWorkers()`, etc.

**Current State:** All wiring logic is in one file, tightly coupling configuration, infrastructure building, and component assembly.

**Recommended State:** Extract into a `wiring` or `builder` package:
```
cmd/server/
├── main.go             # Entry point, signal handling, -- about 50 lines
├── builder.go          # Component assembly (buildRouter, buildEmbedder, etc.)
├── wiring.go           # DAG wiring (buildAIComponents, buildBackgroundWorkers)
└── factory.go          # Infrastructure (buildStorage, buildPrometheus)
```

**Impact:** Any configuration or dependency change requires modifying this single large file, risking merge conflicts and introducing bugs.

**Effort:** L

---

### 🟠 Category: Testing | Severity: High | Title: Low Coverage in Core Packages

**Location:** Multiple packages

**Description:** Several critical packages fall well below the 80% target:

| Package | Coverage | Status |
|---------|----------|--------|
| `cmd/server` | 0.0% | ❌ (no test files) |
| `internal/api/rest` | 52.8% | ❌ |
| `internal/service` | 58.0% | ❌ |
| `internal/storage` | 57.3% | ❌ |
| `internal/repository` | 54.6% | ❌ |
| `internal/events` | 64.0% | ❌ |
| `internal/reconcile` | 60.6% | ❌ |

**Current State:** The core service layer (FileService), repository layer, and storage layer — the three most critical packages — all have below-60% coverage. The `cmd/server` package has no test files at all.

**Recommended State:** 
- `cmd/server`: Add integration/unit tests for server startup and wiring (currently at 0%)
- `internal/service`: Target ≥80%, especially for error paths (quota exceeded, lock violations, corruption)
- `internal/storage`: Focus on contract tests for all storage backends (S3, OSS, COS paths are untested in CI)
- `internal/repository`: Add tests for migration, versioning, and edge cases

**Impact:** Low confidence in critical paths. Bug escapes to production are likely. The CI gate cannot catch regressions in core business logic.

**Effort:** XL (systematic coverage improvement across all packages)

---

### 🟠 Category: Code Quality | Severity: High | Title: Code Duplication Between REST and S3 Handlers

**Location:**
- `internal/api/rest/handler.go` — `checkBucketPolicy`, `extractMetadataHeaders`, `writeMetadataHeaders`, `handleConditional`
- `internal/api/s3compat/handler.go` — `checkBucketPolicy`, `extractMetaHeaders`, `writeS3ObjectMeta`, `getObjectPreconditions`

**Description:** There is significant code duplication between the REST and S3-compat protocol adapters. Both implement nearly identical logic for:
1. **Bucket policy checking** — identical pattern with same policy-parsing logic
2. **Metadata header extraction** — `extractMetadataHeaders` vs `extractMetaHeaders` (different but very similar)
3. **Error classification** — both have separate error-to-HTTP-status mapping
4. **Conditional request handling** — both implement If-Match/If-None-Match

**Current State:** Each protocol adapter independently reimplements shared protocol behavior. Changes must be made in both places.

**Recommended State:** Extract shared protocol logic into a shared library:
```
internal/api/
├── rest/           # REST-specific
├── s3compat/       # S3-specific
├── webdav/         # WebDAV-specific
└── shared/         # Shared protocol concerns
    ├── metadata.go # extractMetadataHeaders, writeContentResponseHeaders
    ├── conditions.go # conditional request handling
    ├── errors.go   # error classification
    └── policy.go   # bucket policy enforcement
```

**Impact:** Bug fixes and enhancements must be duplicated across both handlers. Risk of divergence and inconsistency. Violates DRY principle.

**Effort:** M

---

### 🟠 Category: Code Quality | Severity: Medium | Title: Massive Condition Function Duplication in `auth/condition.go`

**Location:** `internal/auth/condition.go` (657 lines, lines 258–464)

**Description:** The `compileSingleCondition` function has a 200-line switch statement with nearly identical blocks for each condition operator. For example, the numeric comparison functions all follow the same pattern:

```go
case ConditionNumericLessThan:
    return func(cv string) bool {
        cvVal, err1 := strconv.ParseFloat(cv, 64)
        condVal, err2 := strconv.ParseFloat(value, 64)
        if err1 != nil || err2 != nil {
            return false
        }
        return cvVal < condVal
    }, nil
case ConditionNumericLessThanEquals:
    return func(cv string) bool {
        cvVal, err1 := strconv.ParseFloat(cv, 64)
        condVal, err2 := strconv.ParseFloat(value, 64)
        if err1 != nil || err2 != nil {
            return false
        }
        return cvVal <= condVal
    }, nil
```

This pattern repeats for 20+ condition types.

**Current State:** Each condition operator has its own inline closure with duplicated parsing and error handling.

**Recommended State:** Use a function factory pattern:

```go
func compileNumericCondition(op ConditionOperator, value string) (ConditionFunc, error) {
    condVal, err := strconv.ParseFloat(value, 64)
    if err != nil {
        return nil, fmt.Errorf("invalid numeric condition value %q: %w", value, err)
    }
    return func(cv string) bool {
        cvVal, err := strconv.ParseFloat(cv, 64)
        if err != nil {
            return false
        }
        switch op {
        case ConditionNumericLessThan:
            return cvVal < condVal
        case ConditionNumericLessThanEquals:
            return cvVal <= condVal
        case ConditionNumericEquals:
            return cvVal == condVal
        // ...
        }
        return false
    }, nil
}
```

**Impact:** The switch is hard to maintain. Adding a new operator requires copy-paste of the entire block. The file is 657 lines, exceeding the 500-line limit.

**Effort:** M

---

### 🟠 Category: Code Organization | Severity: Medium | Title: `cli/` Package Leaking to `fmt.Println` for Output

**Location:** `internal/cli/cli_crud.go`, `cli_admin.go`, `cli_search.go`, `cli_snapshot.go`

**Description:** The CLI package uses `fmt.Println` directly for user output instead of using a writer abstraction or structured output. This makes testing difficult and prevents output redirection or formatting control.

**Current State:**
```go
// cli_crud.go:89
fmt.Println(string(body))

// cli_admin.go:86
fmt.Printf("%-40s tenant=%-20s scopes=%-15s label=%s\n", ...)
```

**Recommended State:** Define an output abstraction:
```go
type CLI struct {
    out io.Writer
}
func (c *CLI) print(v ...interface{}) {
    fmt.Fprintln(c.out, v...)
}
```

Or use structured output (JSON) as an option.

**Impact:** Testing CLI output requires stdout capture. Adding a `--json` flag or i18n is difficult.

**Effort:** S

---

### 🟡 Category: Testing | Severity: Medium | Title: Test File Size Violations

**Location:** 15 test files exceeding 500 lines (up to 1,440 lines)

**Description:** While test files are exempt from the strict 500-line rule in practice, 15 test files exceeding 500 lines (some hitting 1,440 lines) indicate that tests could benefit from better organization. Large test files often indicate:
- Tests are not following Arrange-Act-Assert cleanly
- Test helpers are mixed with test functions
- Table-driven tests could consolidate duplicated test cases

**Notable examples:**
- `internal/cli/cli_test.go` — 1,440 lines
- `internal/storage/storage_test.go` — 1,120 lines
- `internal/auth/condition_test.go` — 910 lines
- `internal/api/webdav/dav_test.go` — 893 lines

**Current State:** One monolithic test file per package.

**Recommended State:** Split large test files by concern:
```
internal/storage/
├── storage_test.go        # Core contract tests
├── local_test.go           # Local backend tests
├── s3_test.go              # S3 backend tests (integration)
├── encryption_test.go      # SSE tests
└── circuitbreaker_test.go  # Circuit breaker tests
```

**Impact:** Hard to find relevant tests, slow test execution, difficult to parallelize.

**Effort:** M

---

### 🟡 Category: Code Quality | Severity: Medium | Title: `interface{}` Usage in Public APIs

**Location:**
- `internal/auth/policy.go:28` — `Principal map[string]interface{}`
- `internal/api/rest/dto.go:88` — `Hits interface{} json:"hits"`

**Description:** Go 1.18+ supports generics. The use of `interface{}` (now `any`) in DTO types and policy structures means the caller has to type-assert results, losing compile-time safety.

**Current State:**
```go
// policy.go
type PolicyStatement struct {
    Principal interface{} `json:"Principal"`
    Action    interface{} `json:"Action"`
    Resource  interface{} `json:"Resource"`
    Condition map[string]map[string]interface{} `json:"Condition"`
}
```

**Recommended State:** Define concrete types for each possible variant, or use a well-typed union:
```go
type Principal struct {
    AWS   []string `json:"AWS,omitempty"`
    CanonicalUser []string `json:"CanonicalUser,omitempty"`
}
```

For the search hits DTO, use a concrete generic type:
```go
type SearchResult[T any] struct {
    Hits []T `json:"hits"`
}
```

**Impact:** Runtime type assertions are error-prone. New developers may not know what types to expect. Hinders IDE support and refactoring.

**Effort:** S (straightforward typing improvement)

---

### 🟡 Category: Code Organization | Severity: Low | Title: Middleware Chain is Split Across Two Files

**Location:** 
- `internal/middleware/middleware.go` — RequestID, Tenant, Recoverer, AccessLog, ConcurrencyLimiter, PerTenantConcurrencyLimiter
- `internal/auth/auth_middleware.go` — Auth middleware
- `internal/telemetry/otel.go` — OTel middleware

**Description:** The middleware chain is defined in `main.go` as:
```
access_log → concurrency → recoverer → otel → rate_limit → tenant → auth → cors → request_id
```

But the middleware implementations are distributed across three packages (`middleware`, `auth`, `telemetry`). This creates implicit coupling and makes it hard to understand the request flow.

**Current State:** The middleware chain order is specified inline in `main.go:applyMiddleware()` with a struct slice, while implementations are scattered.

**Recommended State:** Create a `middleware/chain.go` that explicitly defines the chain and provides a builder:
```go
type Chain struct {
    middlewares []func(http.Handler) http.Handler
}

func (c *Chain) Then(handler http.Handler) http.Handler { ... }
```

With clear documentation of the order and why it's important.

**Impact:** Easy to accidentally reorder middleware when making changes. New developers must read `main.go` to understand the full request flow.

**Effort:** S

---

### 🟡 Category: Testing | Severity: Low | Title: No Tests for `cmd/server` Package

**Location:** `cmd/server/` (0% coverage, no test files)

**Description:** The main server package has zero tests. While the `internal/integration` package covers full-server integration tests, the server bootstrapping code (config loading, infrastructure init, router assembly) is untested.

**Current State:** The entire `cmd/server` directory has no `*_test.go` files.

**Recommended State:** Add focused tests:
1. Test `buildRouter` returns correct route registrations
2. Test `applyMiddleware` applies middleware in correct order
3. Test `readyzHandler` responds correctly when DB is down
4. Test `buildStorageFrom` correctly constructs each storage backend

**Impact:** Configuration changes or refactoring of startup logic has no safety net.

**Effort:** M

---

### 🟢 Category: Logging | Severity: Low | Title: Structured Slog Usage is Good, But Could Be Better

**Location:** Throughout codebase

**Description:** The codebase uses `log/slog` consistently with JSON format and structured fields. This is an excellent foundation. However, there are some areas for improvement:

1. **Warnings vs Errors:** Some conditions that should be errors are logged as warnings (e.g., `logger.Warn("repo write failed; storage object orphaned")` in `file_crud.go:184` — a storage object being orphaned is an error requiring operator attention).

2. **Missing context fields:** Some log lines don't include `request_id` or `tenant_id`, making correlation difficult.

3. **Panic recovery logging:** `middleware.go:70` logs `"panic"` as a key instead of `"error"` or `"panic"`, which is fine, but the stack trace is missing goroutine id.

**Current State:** Good foundation, minor inconsistencies.

**Recommended State:**
```go
// Orphaned object should be logged as Error, not Warn
s.logger.Error("storage object orphaned after repo write failure",
    "tenant", obj.TenantID,
    "bucket", obj.Bucket,
    "key", obj.Key,
    "error", err,
    "request_id", middleware.RequestIDFrom(ctx),
)
```

**Impact:** Operators might miss critical conditions logged as warnings.

**Effort:** S

---

### 🟢 Category: Error Handling | Severity: Low | Title: Error Wrapping is Good, Could Standardize Error Messages

**Location:** Throughout codebase

**Description:** The codebase uses `fmt.Errorf("context: %w", err)` consistently for error wrapping, which is excellent. However, error messages are not standardized:
- Some use lowercase (`"storage put: %w"`)
- Some use title case (`"Content-MD5 mismatch"`)
- Some use `%w` while others use `%v`

Additionally, some wrapper messages don't add context useful for debugging:
```go
// Not great — doesn't say which put failed
return repository.Object{}, fmt.Errorf("storage put: %w", err)
```

**Current State:** Functional but inconsistent error messages.

**Recommended State:** Standardize with:
1. Lowercase (Go convention)
2. Include identifying context
3. Always use `%w` for wrap

```go
return repository.Object{}, fmt.Errorf("storage put: tenant=%s bucket=%s key=%s: %w", tenant, bucket, key, err)
```

**Impact:** Debugging production issues is harder than necessary.

**Effort:** S

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| 5 non-test files exceed 500 lines | High | L | **P0** | Directly violates AGENTS.md constraint |
| REST handler God object (958 lines, 43 methods) | High | L | **P0** | Must be decomposed |
| S3 handler God object (890 lines, 48 methods) | High | L | **P0** | Must be decomposed |
| REST/S3 code duplication | High | M | **P1** | Shared logic duplicated |
| `cmd/server` 0% test coverage | High | M | **P1** | Server wiring untested |
| Core package coverage < 60% (service, storage, repo) | High | XL | **P1** | Biggest risk area |
| `condition.go` massive switch duplication (657 lines) | Medium | M | **P1** | Exceeds 500-line limit |
| `main.go` monolith (861 lines) | Medium | L | **P1** | All wiring in one file |
| CLI `fmt.Println` for output | Low | S | **P2** | Testing difficulty |
| `interface{}` in public DTOs | Low | S | **P2** | Type safety |
| Middleware chain distribution across packages | Low | S | **P2** | Documentation gap |
| 15 test files exceed 500 lines | Low | M | **P2** | Test maintainability |
| Error message standardization | Low | S | **P3** | Minor quality issue |
| Log level consistency | Low | S | **P3** | Orphaned objects should be Error |

---

## Final Summary

### Overall Code Quality: **Needs Work**

The codebase has a strong foundation — consistent use of slog for logging, proper error wrapping, clean package structure (no `utils/` packages), comprehensive integration tests, and full CI pipeline passing. The architecture as documented in `AGENTS.md` is solid.

However, there are **critical quality issues** that directly violate the project's own engineering constraints specified in `AGENTS.md`. The 500-line file limit is violated by 5 non-test files. The 300-line God type limit is violated by handler files approaching 1,000 lines. These must be addressed before any new feature work, per the project's own rule: *"Refactoring takes priority over feature development."*

### Critical Quality Issues (Must Fix)
1. **Five non-test files exceed 500 lines** — direct AGENTS.md violation, stops development
2. **REST Handler and S3 Handler are God objects** — 43+ methods each, needs domain splitting
3. **Core package coverage below 60%** — service, storage, repository are highest-risk, lowest-coverage
4. **Code duplication between REST and S3 handlers** — duplicate metadata handling, policy checking, error classification

### Maintainability Concerns
- The `auth/condition.go` switch statement (200+ lines of nearly identical closures) will be a pain point
- `main.go` monolith makes configuration changes risky
- CLI `fmt.Println` pattern makes testing and formatting changes difficult
- Middleware chain isn't documented as an architectural invariant (AGENTS.md documents it, but the code doesn't enforce it)

### Quick Wins (S effort, high impact)
1. Add `cmd/server` unit tests for router and middleware chain
2. Standardize error messages with `%w` and contextual info
3. Fix log levels — orphaned objects and storage failures should be `Error`, not `Warn`
4. Type the `interface{}` uses in policy DTOs and search results
5. Create a `middleware/chain.go` builder to encode and document middleware order

### Technical Debt Summary
- **P0 items (must fix before proceeding):** 3 (file limits, God types)
- **P1 items (high impact):** 5 (coverage, duplication, main.go, condition.go)
- **P2 items (medium impact):** 4 (CLI I/O, type safety, middleware chain, test file sizes)
- **P3 items (low impact):** 2 (error msgs, log levels)

The codebase needs a **focused refactoring sprint** before new feature work to address the structural violations in `AGENTS.md` and raise test coverage in core packages to ≥70%. The delegation of `internal/service` and `internal/storage` package testing is the highest-leverage improvement.
