Now I have all the information needed for a thorough staff engineering review. Here is my comprehensive analysis:

---

# Staff Engineer Code Review: aero-vault

## Executive Summary

**Overall Code Quality: Good** — This is a well-architected Go project with clean separation of concerns, good error handling patterns, comprehensive logging, and solid test coverage. However, several critical violations of the project's own engineering constraints exist and need immediate attention.

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity (production) | **53** (compileSingleCondition) | < 10 | ❌ |
| Function length | Several > 50 lines | < 50 | ⚠️ |
| Test coverage | **~65% overall** | > 80% | ⚠️ |
| Code duplication | Low, well-factored | < 5% | ✅ |
| Documentation coverage | Moderate (~60%) | > 70% | ⚠️ |
| File size limit | **4 files > 500 lines** | ≤ 500 per file | ❌ |

---

## Findings

### Category: Organization — Severity: Critical — File Size Violations

Several production files exceed the 500-line constraint defined in `AGENTS.md §0`.

| File | Lines | Violation |
|------|-------|-----------|
| `internal/api/rest/handler.go` | **958** | ❌ 92% over limit |
| `cmd/server/main.go` | **861** | ❌ 72% over limit |
| `internal/api/s3compat/handler.go` | **890** | ❌ 78% over limit |
| `internal/auth/condition.go` | **657** | ❌ 31% over limit |

**Description:** The project explicitly states "单文件 ≤ 500 行" with the consequence "停止开发 → 自动重构". Four production files breach this limit, with `handler.go` at nearly double the threshold.

**Current State:** Large monolithic files that mix multiple concerns.

**Recommended State:** Split each file into domain-specific modules:
- `handler.go` → `handler_crud.go`, `handler_bucket.go`, `handler_batch.go`, `handler_folder.go`, and extract `classify()` + response helpers into a separate module
- `main.go` → extract `buildAIComponents`, `buildBackgroundWorkers`, and wiring functions into `wiring.go` under `cmd/server/`
- `condition.go` → split operator groups by domain (string/numeric/date/IP/ARN)
- `s3compat/handler.go` → split by operation category

**Impact:** Maintainability - these files are hard to navigate, review, and test. Violates project engineering constraints and will cause CI rejection.

**Effort:** M (2-3 sessions)

---

### Category: Quality — Severity: Critical — Cyclomatic Complexity

| Function | File:Line | Complexity | Target |
|----------|-----------|------------|--------|
| `compileSingleCondition` | `condition.go:258` | **53** | ≤ 10 |
| `(*ConditionContext).Get` | `condition.go:90` | **18** | ≤ 10 |
| `(*FileService).Put` | `file_crud.go:71` | **13** | ≤ 10 |
| `(*sqlStore).DeleteBucket` | `sql_buckets.go:69` | **13** | ≤ 10 |
| `(*Handler).BucketDispatch` | `s3compat/handler.go:401` | **13** | ≤ 10 |

**Current State (compileSingleCondition):** A single switch statement with ~25+ condition operator cases, each with its own parsing and predicate logic. Example:
```go
func compileSingleCondition(op ConditionOperator, key, value string) (ConditionFunc, error) {
    switch op {
    case ConditionStringEquals: ...
    case ConditionStringNotEquals: ...
    case ConditionNumericEquals: ...
    // ... ~25 more cases
    }
}
```

**Recommended State:** Use a strategy/registry pattern:
```go
type conditionCompiler func(key, value string) (ConditionFunc, error)

var compilers = map[ConditionOperator]conditionCompiler{
    ConditionStringEquals:      compileStringEquals,
    ConditionNumericEquals:     compileNumericEquals,
    ConditionDateEquals:        compileDateEquals,
    ConditionIpAddress:         compileIpAddress,
    // ...
}

func compileSingleCondition(op ConditionOperator, key, value string) (ConditionFunc, error) {
    compiler, ok := compilers[op]
    if !ok {
        return nil, fmt.Errorf("unknown condition operator: %s", op)
    }
    return compiler(key, value)
}
```

**Impact:** Testability - a 53-complexity function requires ~54+ test cases for path coverage. Each new operator risks regression.

**Effort:** M

---

### Category: Organization — Severity: Medium — Main.go Wires Too Much

**Location:** `cmd/server/main.go` (861 lines)

**Description:** The main function and its helpers contain all the application wiring. While the startup sequence is linear, the sheer volume of builder functions (`buildEmbedder`, `buildLLM`, `buildReranker`, `buildAIComponents`, `setupVectorIndexes`, `setupLexicalCache`, `setupBM25Search`, `setupChatAndAgent`, `buildIndexer`, `registerIndexerJobs`, `startReindexOnStartup`, `buildBackgroundWorkers`, `startWebhook`, `startReconcile`, `buildAuthRegistry`, `configureAuthSecrets`, etc.) makes this file a knowledge bottleneck.

**Current State:** All wiring centralized, with detailed knowledge of every subsystem's construction.

**Recommended State:** Extract a `wiring.go` or `app.go` in the `main` package. Even better, consider a lightweight dependency-injection pattern or an `App` struct that encapsulates the built subsystems.

**Impact:** Onboarding difficulty - new developers must understand this one file to know how the system fits together.

**Effort:** M

---

### Category: Error Handling — Severity: Low — Weak Error Classification in classify()

**Location:** `internal/api/rest/handler.go` — `classify()` function

**Description:** The error-to-HTTP-status mapping uses a flat switch on `errors.Is()`. Some error types map to inappropriate HTTP status codes:
- `ErrObjectCorrupt` maps to `http.StatusGone` (410) — should be `500 Internal Server Error` or `422 Unprocessable Entity`
- Generic errors fall through to `http.StatusInternalServerError` with the full error message exposed to the client, potentially leaking internal details

**Current State:**
```go
case errors.Is(err, service.ErrObjectCorrupt):
    return "ObjectCorrupt", "object is marked as corrupt", http.StatusGone
```

**Recommended State:** Create a typed `APIError` interface that implements `HTTPStatus() int` and `UserMessage() string`, so each service error carries its own presentation metadata.

**Impact:** Some error responses may confuse clients (410 Gone for a corrupt but present object).

**Effort:** S

---

### Category: Logging — Severity: Low — Inconsistent Correlation ID Usage

**Description:** The `X-Request-ID` correlation ID is properly threaded through middleware and used in access logs, but the `FileService.emit()` method doesn't include the request ID in its logged warnings consistently. Some warn logs include it, others don't.

**Location:** `internal/service/file_crud.go` (multiple locations), `internal/service/file_multipart.go`

**Current State:**
```go
s.logger.Warn("size mismatch", "expected", size, "actual", info.Size,
    "tenant", tenant, "bucket", bucket, "key", key)
// Missing request_id!
```

**Recommended State:** Ensure all log entries include `"request_id"` from context. Create a helper `s.log(ctx, msg, attrs...)` that always injects the request ID.

**Effort:** S

---

### Category: Testing — Severity: Medium — Coverage Gaps in Critical Packages

**Description:** Several core packages fall short of the 80% coverage target:

| Package | Coverage | Gap |
|---------|----------|-----|
| `internal/service` | **58.0%** | ❌ -22% |
| `internal/api/rest` | **52.8%** | ❌ -27.2% |
| `internal/repository` | **54.6%** | ❌ -25.4% |
| `internal/storage` | **57.3%** | ❌ -22.7% |
| `internal/webui` | **0.0%** | ❌ -80% |
| `internal/telemetry` | **61.5%** | ❌ -18.5% |

**Current State:** The past sprint achieved 70.2% overall, but individual package coverage varies widely. The service layer (58%) contains the core business logic — all CRUD operations, versioning, multipart uploads, quota enforcement.

**Impact:** Untested code paths in the service layer risk regression on refactors. The CI gate requires ≥50% per the AGENTS.md, but the implicit target is 80%.

**Effort:** L (ongoing investment needed)

---

### Category: Testing — Severity: Medium — Large Integration-style Unit Tests

**Description:** Several test files are very large, suggesting they are integration tests that bypass the project's stated preference for isolated unit tests with mocks:

| Test File | Lines |
|-----------|-------|
| `internal/auth/condition_test.go` | 910 |
| `internal/api/webdav/dav_test.go` | 893 |
| `internal/api/s3compat/handler_test.go` | 847 |
| `internal/mcp/server_test.go` | 761 |
| `internal/reconcile/lifecycle_test.go` | 701 |
| `internal/reconcile/job_test.go` | 570 |

**Current State:** Tests like `condition_test.go` have cyclomatic complexity 27 (exceeding the project limit). Many tests use real DB/storage rather than isolated test doubles.

**Impact:** Slow test feedback cycles (tests take 29+ seconds in service, 38s in rest). Tests that exercise real dependencies are harder to diagnose when they fail.

**Effort:** L

---

### Category: Naming — Severity: Low — Inconsistent Receiver Names

**Description:** The codebase uses `s` for FileService receiver (`file_crud.go`), `h` for Handler, but occasionally deviates.

**Location:** Multiple files

**Current State:**
```go
// file.go uses 's' for FileService
func (s *FileService) Put(ctx context.Context, ...) 

// handler.go uses 'h' for Handler
func (h *Handler) Put(w http.ResponseWriter, r *http.Request)
```

This is consistent within packages, which is good. No major naming issues found.

**Impact:** Negligible. Standard Go naming conventions are followed.

**Effort:** None needed

---

### Category: Technical Debt — Severity: Medium — Missing WebUI Tests

**Location:** `internal/webui/web.go`

**Description:** The WebUI package has zero test coverage. While it's an embedded SPA handler with minimal logic, any future changes risk regression.

**Current State:** 0% coverage.

**Recommended State:** Add a basic HTTP handler test for the WebUI route (at minimum verify it returns 200 and HTML Content-Type).

**Effort:** S

---

### Category: Technical Debt — Severity: High — SDK Duplication

**Location:** `sdk/go/aerovault/` — `client.go` (1006 lines)

**Description:** The Go SDK in `sdk/` duplicates a lot of logic that exists in the server (types, request building, error handling). This is a maintenance burden — changes to the API must be mirrored in the server handlers and in the SDK.

**Current State:** A 1006-line client that re-defines request/response types.

**Recommended State:** Consider generating the SDK from OpenAPI spec (`openapi.json`), or at minimum sharing type definitions via a common internal package. Currently, `openapi.json` is generated from code — the SDK should follow the same contract.

**Impact:** API changes require synchronized changes across server handlers, DTOs, OpenAPI spec, and SDK. High risk of drift.

**Effort:** L

---

### Category: Quality — Severity: Medium — External Transport Wiring in Events

**Location:** `internal/events/postgres_transport.go`

**Description:** The cross-instance event transport (Postgres LISTEN/NOTIFY) is wired in `main.go` via `setupPostgresTransport()` and `configureAuthSecrets()`. The Postgres transport for auth key invalidation is wired inside `configureAuthSecrets`. This creates hidden coupling between the auth package and the events package.

**Current State:** Auth's key invalidation transport is wired inside an auth configuration function that takes an events.Publisher:
```go
keyTr := events.NewPostgresTransport(cfg.Events.TransportDSN, "aero_key_invalidate")
reg.WithKeyChangePublisher(func(ctx context.Context, hash string) { ... })
```

**Recommended State:** Move all cross-cutting transport wiring to a single `wiring.go` in main, keeping `configureAuthSecrets` pure configuration.

**Impact:** The Postgres transport dependency is hidden inside a function named `configureAuthSecrets`, making it hard to discover.

**Effort:** S

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| File size violations (4 files > 500 lines) | High | M | **P0** | Violates AGENTS.md constraint; CI gate will reject |
| `compileSingleCondition` complexity (53) | High | M | **P0** | Untestable; technical risk |
| Main.go as God object (861 lines) | Medium | M | P1 | Onboarding bottleneck |
| Coverage gaps in service/rest/repo (52-58%) | Medium | L | P1 | Regression risk |
| SDK-to-server type duplication (1006 lines) | Medium | L | P2 | Maintenance overhead |
| WebUI 0% coverage | Low | S | P2 | Minimal risk currently |
| Postgres transport wiring hidden in auth config | Low | S | P2 | Architectural clarity |
| Weak error-to-HTTP mapping (Gone vs InternalError) | Low | S | P2 | Client confusion risk |
| Missing request_id in some log statements | Low | S | P3 | Debugging friction |
| Large integration-style test files (6 files > 500 lines) | Low | L | P3 | Slow CI feedback |

---

## Final Summary

### Strengths
1. **Excellent architecture** — The layered design (Protocol → Middleware → FileService → Storage+Repository → EventBus) is clean and well-documented in AGENTS.md
2. **Good error handling** — Sentinel errors (`ErrNotFound`, `ErrLocked`) with `errors.Is()` support
3. **Comprehensive observability** — OTel middleware, Prometheus metrics, structured JSON logging
4. **Strong test patterns** — Use of `httptest`, SQLite TempDir fixtures, `ai.MockLLM`/`HashEmbedder` mocks
5. **Opt-in defaults** — AI, events, replication all disabled by default (security-conscious)
6. **Consistent Go conventions** — Good naming, proper interfaces, no `utils/` packages

### Critical Issues (Must Fix Before Next Sprint)
1. **Split `handler.go` (958 lines), `main.go` (861 lines), and `s3compat/handler.go` (890 lines)** — All three violate the 500-line constraint
2. **Refactor `compileSingleCondition` (complexity 53)** — Use a compiler registry pattern to bring complexity under 10
3. **Raise service/repository coverage to ≥70%** — Core business logic at 58% is below the project's aspirations

### Maintainability Concerns
- The SDK duplication will become a growing burden as the API evolves
- Large integration-style tests (some > 900 lines) slow down the feedback loop
- The `main.go` megafile makes the system's assembly difficult to reason about

### Quick Wins
1. Split `condition.go` using a compiler registry (complexity 53 → ~2 each) — **1-2 hours**
2. Add WebUI handler test (coverage 0% → ~80%) — **30 minutes**
3. Add `request_id` to all warn/error logs — **1 hour**
4. Extract `wiring.go` from `main.go` — **2-3 hours**

The codebase is fundamentally solid and well-engineered. The issues identified are primarily about scaling what was likely a smaller project into a production-grade system, where the original organizational patterns no longer fit. Prioritize the file size and complexity violations above all else — these are the project's own stated engineering constraints.
