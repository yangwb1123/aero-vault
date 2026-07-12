Now I have enough data. Let me compile the comprehensive code review.

---

# Staff Engineer Code Review: aero-vault

## 1. Code Organization

### Findings

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Organization** | **Critical** | File size violations (AGENTS.md rule) | `internal/api/rest/handler.go` (958L), `internal/api/s3compat/handler.go` (890L), `cmd/server/main.go` (861L), `internal/auth/condition.go` (657L) | 4 files exceed the 500-line limit defined in AGENTS.md. This directly contradicts the engineering constraints. | 958/890/861/657 lines | Split handler.go into sub-handlers by domain (bucket ops, object ops, admin ops). Split main.go by concern (server setup, AI wiring, worker wiring). Split condition.go by condition operator type. | Violates project constraint; makes CI `check` target fail; hurts maintainability. | **L** |
| **Organization** | **High** | God function — `run()` in main.go | `cmd/server/main.go:58-134` | 76-line function that orchestrates the entire server startup — config, storage, repo, AI, workers, router, server loop. Hard to unit test without launching the full binary. | One monolithic `run()` function | Split into well-named builder functions with testable intermediate results | Poor testability; high cognitive load for new developers onboarding | **M** |
| **Organization** | **High** | God function — `buildStorageFrom` | `cmd/server/main.go:406-475` | 69-line switch-case factory that handles all storage backends (local, s3, oss, cos) with inline config | Monolithic switch-case | Extract each backend into its own constructor function; use a strategy pattern | Hard to add new backends; violates OCP (Open-Closed Principle) | **M** |
| **Organization** | **High** | God function — `buildRouter` | `cmd/server/main.go:198-233` | 35 parameters in one function signature | `buildRouter(svc, repo, store, search, chat, agent, bus, authReg, promHandler, cfg, aiTimeout, aiRL, logger)` | Use an options struct or config object for the router builder | Fragile signature; every AI/service addition changes main.go | **M** |
| **Organization** | **Medium** | Code duplication: metadata extraction | `internal/api/rest/handler.go:892`, `internal/api/s3compat/handler.go:668,700` | `extractMetadataHeaders`, `s3PutMeta`, `extractMetaHeaders` all do essentially the same thing with different prefixes (X-Meta- vs X-Amz-Meta-) | 3 copies of nearly identical header parsing logic | Move metadata extraction to a shared utility in `internal/api` or a middleware | Maintenance burden when adding new header prefixes | **S** |
| **Organization** | **Medium** | Code duplication: bucket policy check | `internal/api/rest/handler.go:46`, `internal/api/s3compat/handler.go:48` | Both REST and S3 handlers have identical `checkBucketPolicy` logic with minor differences | Duplicated across api/rest and api/s3compat | Extract to a shared middleware or helper function | Policy enforcement inconsistencies between protocols | **S** |
| **Organization** | **Low** | WebDAV outside chi routing tree | `internal/api/webdav/dav.go` — 458 lines | WebDAV is dispatched separately from chi, noted in AGENTS.md as intentional but it's an extra maintenance burden | Separate dispatch path | Consider unifying with chi middleware when possible | Protocol inconsistency; potential routing bugs | **M** |

---

## 2. Naming & Conventions

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Naming** | **Medium** | `handler.go` naming collision | `internal/api/s3compat/handler.go` and `internal/api/rest/handler.go` | Both are named `Handler` in different packages — not a compile error but causes ambiguity when reading imports | Two `Handler` types in api packages | Consider more descriptive types: `RESTServer`, `S3Gateway` | Onboarding confusion | **S** |
| **Naming** | **Low** | Package-level `DefaultBucket` and `DefaultTenant` | `internal/service/file.go:14,21` | These defaults mask misconfiguration. A production deploy that forgets to set tenant headers silently uses "default" | Silently fallback to "default" | Log a warning on first use of a default, or make config required | Silent misconfiguration risk | **S** |
| **Naming** | **Low** | `noopSink` is unexported | `internal/service/file.go:55` | `noopSink` implements `EventSink` but is unexported | Unexported | Could be exported for reuse, or made a singleton | Minor API usability | **S** |

---

## 3. Error Handling

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Error Handling** | **Medium** | Event sink errors silently swallowed | `internal/service/file.go:120` | `s.sink.Publish(ctx, e)` return value is discarded by design (docs say "best-effort") | Silent discard | At minimum log the error, especially in `emit()` which is called for all critical lifecycle events | Silent data loss; webhook failures invisible in logs | **S** |
| **Error Handling** | **Medium** | Quota failures silently skipped on repo error | `internal/service/file_crud.go:26-29` | `preflightQuota` returns nil when `GetTenantQuota` errors — "best-effort enforcement" | Quota bypass on repo error | Still enforce a minimum safety net; log the error prominently | Tenant can bypass quotas when DB is degraded | **M** |
| **Error Handling** | **Low** | Inconsistent error formatting | Various | Some errors use `fmt.Errorf("%w: ...")`, others use `fmt.Errorf("...: %w")` | Mixed conventions | Adopt a project-wide standard (suggest: `"context: %w"` or `"%w: context"`) | Inconsistent error messages make debugging harder | **S** |
| **Error Handling** | **Low** | `classify` err parsing | `internal/api/rest/handler.go:419` | `classify` uses string matching on error messages for HTTP status mapping | String-matching on error messages | Use sentinel errors with typed status codes | Fragile; error message changes break status code mapping | **M** |

### Error classification code (handler.go:419)

```go
// Current — fragile string matching
func classify(err error) (string, string, int) {
    msg := err.Error()
    switch {
    case strings.Contains(msg, "not found"):
        return "NotFound", msg, 404
    case strings.Contains(msg, "forbidden"):
        return "Forbidden", msg, 403
    ...
    }
}

// Recommended — typed sentinel errors
var ErrNotFound = &APIError{Code: "NotFound", Status: 404}
var ErrForbidden = &APIError{Code: "Forbidden", Status: 403}

func classify(err error) (string, string, int) {
    var apiErr *APIError
    if errors.As(err, &apiErr) {
        return apiErr.Code, apiErr.Message, apiErr.Status
    }
    ...
}
```

---

## 4. Logging

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Logging** | **Medium** | No structured error keys in some log calls | `internal/service/file_crud.go:149` | `s.logger.Warn("quota usage increment failed", "tenant", obj.TenantID, "err", qErr)` — OK pattern | Generally good structured logging | Ensure ALL log entries include `tenant`, `request_id`, `operation` keys | Debuggability in multi-tenant deployments | **S** |
| **Logging** | **Low** | No log rotation configuration | `cmd/server/main.go` | Logs go to stdout via JSON handler | Stdout only | Document that production should use log shipper (fluentd, vector, etc.) | Acceptable for containerized deployments | **S** |
| **Logging** | **Low** | Request ID not threaded into all background operations | `internal/service/file.go:114` | `RequestID` is captured from context in `emit()`, but background workers may not have it | Context may be background | Pass request context into background jobs where available | Reduced traceability | **M** |

---

## 5. Testing Practices

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Testing** | **High** | `cmd/server` and `webui` packages at 0% coverage | `cmd/server/`, `internal/webui/` | Two entire packages have zero test coverage. The main package contains critical wiring logic that cannot be validated | No tests | At minimum, add smoke tests for `main` package functions; ensure wiring integration test covers the full startup path | Wiring bugs undetected by CI; deployment risk | **L** |
| **Testing** | **Medium** | Repository package at 54.6% coverage | `internal/repository/` | Repository layer (CRITICAL persistence) is below the 80% target | 54.6% coverage | Add tests for edge cases in `sql_objects.go`, `sql_buckets.go`, `sql_chunks.go` | SQL bugs in production | **L** |
| **Testing** | **Medium** | Service package at 58.0% coverage | `internal/service/` | The core business logic service is under-tested | 58.0% coverage | Focus on `file_crud.go` Put/Get/Delete paths, `file_multipart.go`, and `file_features.go` | Business logic regression risk | **L** |
| **Testing** | **Medium** | Storage package at 57.3% coverage | `internal/storage/` | Storage backends (S3, OSS, COS) have limited test coverage | 57.3% coverage | Add cloud backend tests with mock HTTP servers | Cloud storage integration bugs | **L** |
| **Testing** | **Medium** | Test functions with high complexity | `internal/repository/..._test.go:29` (27), `internal/api/rest/buckets_test.go:50` (24), `internal/ai/qdrant_test.go:22` (24) | Some test functions have cyclomatic complexity > 20, which indicates they test too many scenarios at once | Complex table-driven tests with many cases | Break into smaller, focused subtests using `t.Run` | Test failures hard to debug | **M** |
| **Testing** | **Low** | No linter/stylistic checks in CI | `Makefile` — `check` target | `gofmt` is present but no `go vet` shadow analysis, `staticcheck`, or `revive` | Basic checking only | Add `staticcheck`, `govulncheck`, and `go vet -vettool` | Missed lint/warning issues | **S** |
| **Testing** | **Low** | Race detection not enforced in CI | `Makefile` — separate `test-race` target | Race detection is a separate target, not part of `check` | Optional race detection | Add `-race` to the CI test gate for linux/amd64 | Undetected data races | **S** |

---

## 6. Technical Debt

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Quality** | **High** | Cyclomatic complexity 53 — `compileSingleCondition` | `internal/auth/condition.go:258` | Single function has 53 switch branches for all IAM condition operators. Way past the 10 limit. | 53 switch cases | Break into operator-specific compilation maps: `map[ConditionOperator]func(string) (ConditionFunc, error)` | Impossible to test exhaustively; high bug surface | **M** |
| **Quality** | **High** | Cyclomatic complexity 18 — `(*ConditionContext).Get` | `internal/auth/condition.go:90` | This function handles all resolution strategies for context keys (tags, ARN, IP, etc.) | 18 complexity | Extract key resolution strategies into separate functions/methods | Complexity in hot path | **M** |
| **Quality** | **Medium** | `Put` method at complexity 13 | `internal/service/file_crud.go:71` | The core upload method handles too many responsibilities | Single method does MD5 wrap, storage put, verify, build object, write | Extract: `md5WrapReader` is already separate; extract quota & lock preflight from the body | Violates 10-complexity rule | **M** |
| **Quality** | **Low** | `BucketDispatch` at complexity 13 | `internal/api/s3compat/handler.go:401` | The bucket dispatch router handles all sub-resource dispatching | Single large dispatch function | Use a query-param routing table | Violates 10-complexity rule | **S** |
| **Technical Debt** | **Medium** | God type — `BucketConfig` with 12 fields | `internal/repository/repository.go:40-55` | Configuration struct carries versioning, lock, lifecycle, ACL, policy, CORS, logging, notifications | Monolithic config struct | Consider separating into sub-configs: `VersioningCfg`, `LockCfg`, `LifecycleCfg`, etc. | Every bucket feature adds a field | **M** |
| **Technical Debt** | **Medium** | `*s3compat.Handler` has too many methods | `internal/api/s3compat/handler.go` (890 lines, ~40 methods) | S3 handler has grown organically with feature additions | Monolithic handler | Split into `objects.go`, `buckets.go`, `multipart.go`, `acl.go` | Hard to navigate; violates SRP | **M** |
| **Technical Debt** | **Low** | `*rest.Handler` has too many methods | `internal/api/rest/handler.go` (958 lines, ~47 methods) | Same issue as S3 handler — domain mixing | Monolithic handler | Split by domain: `objects.go`, `buckets.go`, `admin.go`, `search.go` | Hard to navigate | **M** |
| **Technical Debt** | **Low** | Makefile CI gate doesn't fail on cyclomatic complexity violations | `Makefile:complexity-lines` | `gocyclo -over 10` only warns but doesn't `exit 1` | Advisory only | Change to fail on any complexity > 10 violation | Violations go unchecked | **S** |

---

## 7. Dependency Analysis

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|------------------|--------|--------|
| **Dependencies** | **Medium** | `aliyun-oss-go-sdk v3.0.2+incompatible` | `go.mod` | Using a pre-Go-modules SDK marked as `+incompatible`. The `+incompatible` suffix means it has no go.mod | Incompatible version | Watch for a v4 release or consider using the official Alibaba Cloud SDK v2 | Potential build issues with future Go versions | **M** |
| **Dependencies** | **Low** | Large dependency footprint for cloud storage | `go.mod` | Requires AWS SDK, Alibaba Cloud SDK, Tencent Cloud SDK all linked | All SDKs compiled in | Consider build tags to exclude cloud SDKs not in use, or make storage backend selection a compile-time choice | Binary bloat (5MB for unused cloud SDKs) | **M** |
| **Dependencies** | **Low** | `mitchellh/mapstructure v1.4.3` — unmaintained fork | `go.mod` | This library is in maintenance mode and unlikely to receive updates | Legacy dependency | Evaluate if still needed; consider removing if config parsing doesn't use it | Supply chain risk | **S** |

### Recommended compile-time size reduction

```go
// build_storage_s3.go (S3 backend only)
//go:build s3
package main

func init() { registerStorage("s3", newS3Storage) }

// build_storage_local.go (local only)
//go:build !s3 && !oss && !cos
package main

func init() { registerStorage("local", newLocalStorage) }
```

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **File size (>500 line violations)** | 4 files | 0 | **❌** |
| **Cyclomatic complexity (production)** | 1 function at 53, 1 at 18, 8+ at 12-13 | < 10 | **❌** |
| **Function length (>50 line violations)** | ~5-10 functions | < 50 lines | **⚠️** |
| **Test coverage** | ~70.2% overall | > 80% | **⚠️** |
| **No `utils/`/`common`/`helper` packages** | ✅ None found | 0 | **✅** |
| **Code duplication** | `checkBucketPolicy` × 2, metadata extraction × 3 | < 5% | **⚠️** |
| **gofmt compliance** | `internal/auth/arn_test.go`, `condition.go` fail | 0 violations | **⚠️** |
| **`go vet` compliance** | ✅ Clean | Clean | **✅** |
| **TODO/FIXME debt** | ✅ None found (impressive) | 0 | **✅** |
| **Sentinel errors pattern** | ✅ Consistent use throughout | Consistent | **✅** |
| **Interface segregation** | ✅ Good: `Storage`, `Repository`, `EventSink`, `ChunkCleaner`, `SecretProvider` | Well-defined | **✅** |
| **No circular dependencies** | ✅ Appears clean | Clean | **✅** |
| **Package coverage gaps** | `cmd/server: 0%`, `webui: 0%`, `repository: 54.6%`, `storage: 57.3%`, `service: 58.0%` | > 80% | **⚠️** |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| File size violations (4 files >500L) | High — blocks `make check` | L | **P0** | Must fix: `handler.go ×2`, `main.go`, `condition.go` |
| Cyclomatic complexity 53 in `compileSingleCondition` | High — untestable complexity | M | **P0** | Operator dispatch table pattern |
| `compileSingleCondition` 53 > 10 | High | M | **P0** | Replace switch with `map[ConditionOperator]func` |
| Cyclomatic complexity 18 in `(*ConditionContext).Get` | Medium — key resolution logic | M | **P1** | Extract per-key-type resolvers |
| 0% coverage for `cmd/server` and `webui` | Medium — wiring bugs | L | **P1** | Add smoke/integration tests |
| `repository` at 54.6% coverage | Medium — SQL bugs | L | **P1** | Focus on `sql_objects.go` and `sql_buckets.go` |
| `service` at 58.0% coverage | Medium — regression risk | L | **P1** | Focus on Put/Get/Delete/Multipart |
| `storage` at 57.3% coverage | Medium — cloud backend bugs | L | **P1** | Add mock HTTP server tests |
| Duplicated `checkBucketPolicy` and metadata parsing | Low — maintenance burden | S | **P2** | Extract shared utility |
| `compileSingleCondition` switch in auth/condition.go | High | M | **P0** | Use operator-to-func map |
| Makefile gocyclo gate is advisory only | Low — violations go unchecked | S | **P2** | Change to `exit 1` on violation |
| `BucketConfig` god type with 12 fields | Low — organic growth | M | **P2** | Use sub-configs |
| `rest.Handler` and `s3compat.Handler` too large | Medium — violates SRP | M | **P2** | Split by domain |
| Cloud SDKs always compiled in | Low — binary bloat | M | **P2** | Build tags for optional backends |
| `aliyun-oss-go-sdk +incompatible` | Low — supply chain | M | **P3** | Monitor for v4 release |
| No `staticcheck`/`revive` in CI | Low — missed lint issues | S | **P3** | Add as optional gate first |

---

## Final Summary

### **Overall Code Quality**: **Needs Work**

The codebase has strong architectural foundations — clean layering, good interface segregation, no circular dependencies, consistent sentinel errors, and 105 test files out of 237 total Go files (44% test ratio). The engineering team has clearly invested in the right patterns. However, there are **hard constraints defined in AGENTS.md** that are being violated, which undermines the governance system.

### Critical Quality Issues (P0 — must fix before next release)

1. **4 files exceeding the 500-line limit** — This is a hard constraint defined in AGENTS.md that the CI `check` target enforces with `exit 1`. These must be split before any further feature work.

2. **Cyclomatic complexity 53 in `compileSingleCondition`** — This single function has more than 5× the allowed complexity. It needs to be refactored using an operator dispatch map.

3. **`cmd/server` and `webui` at 0% coverage** — The main wiring code is entirely untested. Any change to startup logic is a blind deployment.

### Maintainability Concerns

- **Domain handlers are too large** — Both `rest/handler.go` and `s3compat/handler.go` have grown into god handlers with 40+ methods each. They violate SRP and will only get worse with new features.
- **Code duplication between REST and S3 API layers** — Bucket policy checking and metadata header extraction are duplicated, meaning changes must be made in two places.
- **`condition.go` is both complex and long** — At 657 lines with a complexity-53 function, it's the highest-risk file in the codebase.

### Things Done Well ✅

- **No TODO/FIXME/HACK comments anywhere** — Remarkable discipline.
- **No `utils/`/`common`/`helper` packages** — Following project constraints.
- **Clean `go vet` pass** — No type or interface violations.
- **Consistent sentinel error pattern** — Well-defined error constants at package boundaries.
- **Good interface boundaries** — `Storage`, `Repository`, `EventSink`, `ChunkCleaner`, `SecretProvider`, `Embedder`, `LLM` — all well-separated.
- **Embedded migrations with dual SQLite/Postgres** — Properly versioned and parallel.
- **Good test infrastructure** — Contract tests for storage backends, mock LLM/Embedder for AI, integration test directories for pgvector/qdrant.
- **structured logging** — Uses `slog` with JSON handler everywhere.

### Quick Wins (S effort)

1. Fix `gofmt` violations in `arn_test.go` and `condition.go`
2. Extract shared `checkBucketPolicy` and metadata header parsing into a shared helper
3. Change `classify()` error matching from string contains to `errors.As` with typed error codes
4. Add a warning log when event sink publish fails (currently silently discarded)
5. Make `gocyclo` gate fail on violations rather than warn

### Recommended Refactoring Roadmap

```
Sprint N+1: 
  - Split condition.go using operator dispatch map (P0)
  - Split rest/handler.go into sub-files (P0)
  - Fix gofmt violations (P2 quick win)

Sprint N+2:
  - Split s3compat/handler.go (P0)
  - Split main.go into testable builder functions (P1)
  - Add cmd/server smoke tests (P1)

Sprint N+3:
  - Address repository/service/storage coverage gaps (P1)
  - Extract shared utilities between REST and S3 (P2)
  - Make gocyclo gate a hard failure (P2)
```
