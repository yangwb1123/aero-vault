Now I have comprehensive understanding of the codebase. Let me compile my thorough review.

---

# Staff Engineer Code Quality Review: aero-vault

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Many functions ~10-15 | < 10 | ⚠️ |
| Function length | Mixed (some > 50 lines) | < 50 lines | ⚠️ |
| File length violations | 5 files exceed 500 lines | ≤ 500 | ❌ |
| Test coverage | ~42% (estimated) | > 80% | ⚠️ |
| Code duplication (REST↔S3) | ~15% duplicated patterns | < 5% | ⚠️ |
| Documentation coverage | Good on public APIs | > 70% | ✅ |
| Packages without tests | `webui`, `replication` | 100% | ⚠️ |

---

## Findings

### 1. Code Organization

| Field | Description |
|-------|-------------|
| **Category** | Organization |
| **Severity** | Critical |
| **Title** | Multiple files violate the 500-line file limit (AGENTS.md constraint) |
| **Location** | `internal/api/rest/handler.go` (958 lines), `internal/api/s3compat/handler.go` (890 lines), `cmd/server/main.go` (861 lines), `internal/auth/condition.go` (657 lines), `internal/api/webdav/dav.go` (458 lines, close to limit) |
| **Description** | AGENTS.md mandates a hard 500-line limit per file. Five files exceed or approach this threshold. The project's own rules say "违反者将在 HARNESS.md 定义的自动检查中被拒绝" (violators will be rejected in CI). |
| **Current State** | These large files accumulate many handler functions, mixing main assembly logic, or overloading a file with condition operators. |
| **Recommended State** | Split handler.go into sub-files by domain (e.g., `objects.go`, `buckets.go`, `folders.go`, `multipart.go`). Split `main.go` into `server.go`/`infra.go`/`ai.go`. Split `condition.go` into operator-specific files. |
| **Code Example** | `handler.go` has 48 public functions. REST handler alone has ~24 handler functions + ~12 helper/writer functions. Domain sub-files would bring each under 300 lines. |
| **Impact** | High maintainability cost; difficult onboarding; violates the project's own governance rules. |
| **Effort** | L |

| Field | Description |
|-------|-------------|
| **Category** | Organization |
| **Severity** | High |
| **Title** | Parallel handler structures between REST and S3 create duplication |
| **Location** | `internal/api/rest/handler.go` vs `internal/api/s3compat/handler.go` |
| **Description** | Both handlers independently implement: `checkBucketPolicy` (same logic, different signature — REST takes `action`, S3 takes `bucket,action`), metadata header writers, bucket config endpoints, error responses. The REST handler has `writeError`/`classify` while S3 has `writeS3Error`. |
| **Current State** | Two parallel implementations of similar patterns. `checkBucketPolicy` is copy-pasted with minor differences. |
| **Recommended State** | Extract shared handler utilities into a common pattern (not a `common/` package — forbidden per AGENTS.md — but a shared interface in `service` package or a small utility set in the `rest` package that both can call). |
| **Impact** | Medium — bug fixes need to be applied in two places; inconsistent behavior risk. |
| **Effort** | M |

| Field | Description |
|-------|-------------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | REST handler has weak domain separation in router |
| **Location** | `internal/api/rest/router.go` |
| **Description** | `NewRouter` registers routes for the REST API, but handler methods from `Handler`, `AIHandler`, `AdminHandler`, and `SSEHandler` are interleaved in one function. The router also defines dispatcher functions like `postKey`, `putKey`, `getKey`, `deleteKey` that use path suffix parsing. |
| **Current State** | 85+ route registrations in a single function. Dispatcher functions use fragile `strings.HasSuffix(r.URL.Path, ...)` path matching. |
| **Recommended State** | Use chi's URL parameter matching or dedicated sub-routers instead of path suffix matching. Group route registrations by domain (ai, admin, objects, buckets). |
| **Impact** | Low-medium — fragile routing that's order-dependent; difficult to trace request flow. |
| **Effort** | M |

---

### 2. Naming & Documentation

| Field | Description |
|-------|-------------|
| **Category** | Naming |
| **Severity** | High |
| **Title** | Inconsistent method naming between REST and S3 handlers |
| **Location** | `internal/api/rest/handler.go` vs `internal/api/s3compat/handler.go` |
| **Description** | REST handler: `Put`, `Get`, `Head`, `Delete`, `List`, `PostForm`. S3 handler: `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `listObjectsV1/V2`. Plus REST dispatchers: `putKey`, `postKey`, `getKey`, `deleteKey` (mixing HTTP verbs with "key"). |
| **Current State** | REST handler uses HTTP-verb-only names for core operations and `key`-suffixed for dispatchers; S3 uses `Object`-suffixed names. |
| **Recommended State** | Choose one convention and apply consistently. Either all `Object`-suffixed (S3 style) or all verb-only. Dispatchers should describe routing, not mix verbs+keys. |
| **Impact** | Medium — confusing for new contributors; code search is harder. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Naming |
| **Severity** | Medium |
| **Title** | `keyFromPath` vs `keyFromURL` — same concept, different names |
| **Location** | `internal/api/rest/handler.go` (line 42: `keyFromPath`), `internal/api/s3compat/handler.go` (function `keyFromURL`) |
| **Description** | Both functions extract the object key from the URL path. REST uses `chi.URLParam(r, "*")` and strips leading `/`. The S3 version uses a different implementation. Same concept, different names. |
| **Recommended State** | Unify naming — either both `keyFromPath` or both `keyFromURL`. Consider extracting a shared function. |
| **Impact** | Low — minor inconsistency. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Documentation |
| **Severity** | Low |
| **Title** | Public APIs well-documented, but internal logic could use more comments |
| **Location** | Various |
| **Description** | Package-level doc comments are excellent throughout. However, some complex functions like `Put` in `file_crud.go` (80+ lines) and middleware chains lack inline comments explaining non-obvious decisions. The concurrency limiter's weight system (GET=1, others=2) is undocumented. |
| **Current State** | Public APIs have comprehensive Go-style doc comments. Internal logic varies. |
| **Recommended State** | Add brief comments explaining why certain design choices were made (e.g., why concurrency weight distinguishes GET vs write operations). |
| **Impact** | Low — current state is usable. |
| **Effort** | S |

---

### 3. Error Handling

| Field | Description |
|-------|-------------|
| **Category** | Error Handling |
| **Severity** | High |
| **Title** | Several CLI commands silently ignore HTTP error status codes |
| **Location** | `internal/cli/cli_test.go` (lines 1419-1430, documented BUG comments) |
| **Description** | The test file explicitly documents that `cmdList`, `cmdTag`, `cmdVersions`, `cmdLineage`, and `cmdSearch` never check HTTP response status codes. A 5xx response prints whatever the server returns, and the method returns 0 (success). These bugs apply to the production CLI code, not just tests. |
| **Current State** | `cmdList`, `cmdTag`, `cmdVersions`, `cmdLineage`, `cmdSearch` ignore HTTP status codes. `cmdSnapshot` silently swallows missing DB file errors. |
| **Recommended State** | Validate `resp.StatusCode` in each CLI command method. Return non-zero exit code on non-2xx responses. Print the error body to stderr. |
| **Code Example** | Currently: `func (c *Client) cmdList(args []string) int { ... resp, _ := c.do("GET", path, nil, nil); body, _ := io.ReadAll(resp.Body); fmt.Println(string(body)); return 0 }`. Should be: `func (c *Client) cmdList(args []string) int { ... resp, err := c.do("GET", path, nil, nil); if err != nil { fmt.Fprintln(os.Stderr, err); return 1 } defer resp.Body.Close(); if resp.StatusCode >= 400 { body, _ := io.ReadAll(resp.Body); fmt.Fprintln(os.Stderr, "error:", resp.Status, string(body)); return 1 } ... }` |
| **Impact** | High — CLI silently reports success even when operations fail, causing automation and users to trust incorrect results. |
| **Effort** | M |

| Field | Description |
|-------|-------------|
| **Category** | Error Handling |
| **Severity** | Medium |
| **Title** | S3 handler silently swallows ACL errors on write |
| **Location** | `internal/api/s3compat/handler.go` (line 115) |
| **Description** | In `PutObject`, after a successful `svc.Put`, the canned ACL header (`x-amz-acl`) is applied but its error is discarded with `_ = h.svc.SetObjectACL(...)`. |
| **Current State** | ACL failures are invisible to the caller. |
| **Recommended State** | Log the error at minimum. Consider returning an error if the ACL is critical to the write operation. |
| **Impact** | Medium — subtle inconsistency where a PUT succeeds but ACL is silently not set. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Error Handling |
| **Severity** | Medium |
| **Title** | Error wrapping style inconsistent |
| **Location** | Various files |
| **Description** | Some errors are wrapped with `fmt.Errorf("context: %w", err)` while others use `fmt.Errorf("context: %v", err)` losing the wrapped error. Some paths return internal errors (like storage errors) directly to the API caller, potentially leaking implementation details. |
| **Current State** | Mixed: `storage put: %w` (good) vs `s.repo.Ping(req.Context())` errors passed directly (could leak SQL). `classify()` in REST handler surfaces raw error messages in `InternalError` cases. |
| **Recommended State** | Consistent error wrapping with `%w` throughout. Sanitize internal errors before returning to API clients (use generic messages like "internal error" with logging). |
| **Impact** | Medium — potential information disclosure and debugging difficulty. |
| **Effort** | L |

---

### 4. Logging

| Field | Description |
|-------|-------------|
| **Category** | Logging |
| **Severity** | Low |
| **Title** | Webdav error logging uses Debug level |
| **Location** | `internal/api/webdav/dav.go` (line 49) |
| **Description** | The WebDAV handler's `Logger` callback logs errors at Debug level: `logger.Debug("webdav", ..., "err", err)`. This means WebDAV errors will be invisible in production unless debug logging is enabled. |
| **Current State** | All WebDAV errors logged at Debug level. |
| **Recommended State** | Log actual errors at WARN or ERROR level. Use Debug only for success or expected informational messages. |
| **Impact** | Low — WebDAV errors go unnoticed. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Logging |
| **Severity** | Low |
| **Title** | No structured logging context propagation in some middleware |
| **Location** | `internal/middleware/middleware.go` |
| **Description** | The access log middleware creates a new logger entry per request but uses `slog.Default()` implicitly without adding request-scoped attributes (tenant, request_id) to the logger context. These are logged as extra keys on the log line but not as structured logger attributes. |
| **Current State** | Access log includes method/path/status/bytes/duration_ms/request_id/tenant as flat key-value pairs on the log line. |
| **Recommended State** | Use `slog.With("request_id", id, "tenant", tenant)` to create a contextual logger that attaches these to all subsequent log calls in the request scope. Consider attaching the logger to the request context. |
| **Impact** | Low — functional but not optimal for structured log analysis. |
| **Effort** | S |

---

### 5. Testing Practices

| Field | Description |
|-------|-------------|
| **Category** | Testing |
| **Severity** | High |
| **Title** | No test files in `webui` and `replication` packages |
| **Location** | `internal/webui/` and `internal/replication/` |
| **Description** | The `internal/webui` package has zero tests despite serving an embedded web UI. The `internal/replication` package has no tests. AGENTS.md mandates "all business logic must be testable" and "no submission without tests." |
| **Current State** | No tests at all for these packages. |
| **Recommended State** | Add at least smoke tests for webui (verify static files are served, index.html content). Add integration tests for replication (verify replication worker processes events). |
| **Impact** | High — untested code in production paths. |
| **Effort** | M |

| Field | Description |
|-------|-------------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Test files exceeding 500-line limit |
| **Location** | `internal/cli/cli_test.go` (1440 lines), `internal/storage/storage_test.go` (1120 lines), `internal/repository/chunks_events_buckets_test.go` (922 lines), `internal/auth/condition_test.go` (910 lines), `internal/api/webdav/dav_test.go` (893 lines), `internal/api/s3compat/handler_test.go` (847 lines), `internal/ai/integration_test.go` (762 lines) |
| **Description** | AGENTS.md's 500-line constraint applies to all files including tests. Many test files far exceed this limit. |
| **Current State** | 7+ test files exceed 500 lines, making them hard to navigate and maintain. |
| **Recommended State** | Split large test files by test domain (e.g., `cli_upload_test.go`, `cli_list_test.go`). Use subtests with `t.Run`. |
| **Impact** | Medium — reduces test readability and maintainability. |
| **Effort** | L |

| Field | Description |
|-------|-------------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Test patterns mix unit and integration concerns |
| **Location** | Various test files |
| **Description** | Some test files (e.g., `internal/ai/integration_test.go`, `internal/integration/fullserver_test.go`) use build tags like `//go:build integration` but others use runtime detection (ping Qdrant, check Postgres). The pattern is inconsistent. Some tests construct full HTTP servers (`httptest.NewServer`) while others test service methods directly. |
| **Current State** | Mixed approach: some tests use `integration` build tags, some use runtime detection. Some test through HTTP, some through the service directly. |
| **Recommended State** | Standardize on: unit tests (no network, direct service calls) with `_test.go` suffix; integration tests (require Docker/network) with `//go:build integration` build tag and runtime auto-skip via `t.Skip`. |
| **Impact** | Medium — developer confusion about when/how to run tests. |
| **Effort** | M |

---

### 6. Technical Debt

| Field | Description |
|-------|-------------|
| **Category** | Technical Debt |
| **Severity** | Critical |
| **Title** | Known CLI bugs documented in test file but not fixed |
| **Location** | `internal/cli/cli_test.go` (lines 1419-1433) |
| **Description** | Five confirmed bugs are documented in comments (`cmdList`, `cmdTag`, `cmdVersions`, `cmdLineage`, `cmdSearch` ignore HTTP status codes; `cmdSnapshot` silently swallows missing DB file errors). These are explicitly labeled "BUG" but remain unfixed. |
| **Current State** | Bugs are documented in a test file comment but not tracked in an issue tracker or fixed. |
| **Recommended State** | Fix the bugs, or create permanent issue tracking and reference the issue number in the code. Delete stale bug documentation once fixed. |
| **Impact** | High — known bugs persisting in production code. |
| **Effort** | M |

| Field | Description |
|-------|-------------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | `compileIPMatchV6` is unused dead code |
| **Location** | `internal/auth/condition.go` (line 639+, marked with `//nolint:unused`) |
| **Description** | An alternative IPv6 implementation `compileIPMatchV6` is kept as "reference" with `//nolint:unused` to suppress the linter. Dead code increases maintenance burden. |
| **Current State** | ~20 lines of unused, unexported function kept for "reference". |
| **Recommended State** | Remove dead code. Git history preserves it. If kept, add a clear comment explaining why with a planned removal date or decide to use it instead of the `net.ParseIP` version which works fine with IPv6. |
| **Impact** | Low — suppressed with nolint but adds confusion. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | `gofmt` issues in auth package |
| **Location** | `internal/auth/arn_test.go`, `internal/auth/condition.go` |
| **Description** | The `make check` command showed these two files have gofmt issues. This violates the CI gate requirement that `gofmt -l .` must produce zero output. |
| **Current State** | Two files with formatting issues not caught by CI or pre-commit hooks. |
| **Recommended State** | Run `gofmt -w` on the project to fix all formatting issues. Add a pre-commit hook or editor configuration to prevent formatting drift. |
| **Impact** | Low — cosmetic but violates stated CI gate. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | `ai/indexer.go` and BM25 have no dedicated unit tests (only integration tests) |
| **Location** | `internal/ai/indexer.go`, `internal/ai/bm25.go` |
| **Description** | The indexer orchestrates the entire extraction-chunking-embedding pipeline. BM25 implements in-memory keyword search. Despite being core to the AI pipeline, BM25 only has integration tests (`bm25_test.go` opens a full SQLite database). Indexer has no dedicated unit tests — only integration-level test coverage. |
| **Current State** | BM25 tested through integration tests. Indexer tested only through end-to-end flows. |
| **Recommended State** | Extract testable interfaces and write pure unit tests with mock dependencies for both BM25 and the indexer. |
| **Impact** | Medium — core AI components lack isolated unit tests, making regression detection harder. |
| **Effort** | M |

---

### 7. Code Quality

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | High |
| **Title** | Handler function complexity — S3 `PutObject` has multiple sub-resource dispatch branches |
| **Location** | `internal/api/s3compat/handler.go` (lines 69-121, `PutObject`) |
| **Description** | `PutObject` has 6 sequential early-return branches (copy-source, tagging, uploadId, acl, legal-hold, restore) plus optional AC l handling. Estimated cyclomatic complexity ≈ 8-10. Same pattern in `GetObject` (similar complexity). |
| **Current State** | Early-return pattern with 6 sequential checks before the main logic. |
| **Recommended State** | Extract sub-resource dispatch into a separate method or use a dispatch table. Consider a routing approach where sub-resources are separate route registrations rather than parsed inside the handler. |
| **Impact** | Medium — hard to test all branches; easy to miss a branch when modifying. |
| **Effort** | M |

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | REST handler `DeleteFolder` does full list pagination for deletion |
| **Location** | `internal/api/rest/handler.go` (lines 789-827, `DeleteFolder`) |
| **Description** | `DeleteFolder` lists ALL objects under a folder prefix in a loop (potentially infinite with `for {}`) before deleting. For large folders, this loads every key into memory. |
| **Current State** | Paginated listing loops to collect all keys, then batch-deletes them all. |
| **Recommended State** | Consider paginated deletion: list 1000, delete 1000, repeat. Use the repository's native prefix deletion if available. Add a limit or recursive depth guard. |
| **Impact** | Low-medium — potential memory issue for folders with millions of objects. |
| **Effort** | S |

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Magic numbers and string literals scattered |
| **Location** | Various |
| **Description** | Constants like `1000` (max list page in webdav), `32 << 20` (max multipart form size in REST), `15` (shutdown timeout seconds), `300` (default presign expiry) are used as raw literals rather than named constants. |
| **Current State** | Raw literals without named constants. |
| **Recommended State** | Extract meaningful constants with descriptive names: `maxListPageSize = 1000`, `maxMultipartFormMemory = 32 << 20`, `defaultPresignExpirySecs = 300`, `shutdownGracePeriod = 15 * time.Second`. |
| **Impact** | Low — values are self-explanatory in context but maintenance drift risk. |
| **Effort** | S |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| CLI bugs — status codes not checked | High | M | P0 | Documented known bugs in production code |
| 5 files exceed 500-line limit | High | L | P0 | Violates project's own governance rules |
| No tests for webui and replication | High | M | P1 | Untested production code |
| REST↔S3 handler code duplication | Medium | M | P1 | Duplicated checkBucketPolicy, header writers, bucket config endpoints |
| Inconsistent error wrapping | Medium | L | P1 | Mixed %w and %v, potential info leak |
| 7+ test files exceed 500 lines | Medium | L | P1 | Same limit applies to tests per AGENTS.md |
| gofmt issues in auth package | Low | S | P2 | Violates CI gate |
| Dead code compileIPMatchV6 | Low | S | P2 | Suppressed with nolint |
| Magic numbers scattered | Low | S | P2 | Readability debt |
| WebDAV error logging at Debug level | Low | S | P2 | Production error invisibility |
| Inconsistent test patterns (build tags vs runtime) | Medium | M | P2 | Developer confusion |
| Indexer/BM25 lack pure unit tests | Medium | M | P2 | Integration-only coverage for core AI |

---

## Final Summary

### Overall Code Quality: **Needs Work**

The codebase has **good architectural foundations** and **well-documented public APIs**, strong adherence to clean architecture layering, and comprehensive migration management with dual SQLite/Postgres files. The DDD-style package organization is commendable and the middleware chain is correctly ordered.

### Critical Quality Issues (Must Fix)

1. **CLI bugs in production code** (P0) — Five CLI commands silently ignore HTTP status codes, documented in test BUG comments but unfixed. This undermines the entire CLI tool's reliability for scripting and automation.

2. **File size limit violations** (P0) — 5 files exceed the project's own 500-line hard limit. The largest (`rest/handler.go` at 958 lines, `s3compat/handler.go` at 890 lines) must be split per the project's stated "重构优先级高于功能开发" (refactoring priority over feature development).

### Maintainability Concerns

- **Duplicate handler patterns** between REST and S3 protocols (~15% duplication rate). Each new bucket sub-resource must be implemented twice.
- **Large files** make navigation difficult: `handler.go` has 48 functions, `main.go` orchestrates the entire application lifecycle in one file.
- **Mixed error handling styles** make debugging harder — some errors wrap context, some leak implementation details to API callers.

### Technical Debt

The most concerning debt is the **documented-but-unfixed CLI bugs** — an anti-pattern where known issues are annotated in test comments rather than fixed or tracked in an issue system. The **untested packages** (`webui`, `replication`) and **integration-only test coverage** for core AI components (indexer, BM25) represent risk areas.

### Quick Wins (S effort)

1. Run `gofmt -w .` to fix the two formatting issues
2. Remove dead code `compileIPMatchV6` in `condition.go`
3. Fix WebDAV error log level from Debug to Warn
4. Extract magic numbers as named constants (list page size, presign expiry, shutdown timeout)
5. Log ACL errors in S3 PutObject instead of swallowing them

These quick wins would immediately improve code quality consistency while the larger structural refactors (file splitting, CLI bug fixes, test gap closure) should be prioritized based on the sprint roadmap.
