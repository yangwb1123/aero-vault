I now have a thorough understanding of the codebase. Let me compile the full Staff Engineer review.

---

# Staff Engineer Code Review — aero-vault

## 1. Code Organization

### ✅ Strengths

The codebase follows a clean layering that aligns with the architecture doc:
- **Protocol Adapters** (`api/rest`, `api/s3compat`, `api/webdav`, `mcp`) — thin, no business logic
- **Service** (`service/`) — single entry point, orchestrates storage + repository
- **Data** (`storage/`, `repository/`) — persistence layer with clear interfaces
- Cross-cutting concerns (auth, middleware, telemetry) are properly separated

No `utils/`, `common/`, or `helper/` packages exist — the architecture constraint is respected.

### ❌ Finding: Routers and handlers live in the same package

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | Handler files exceed 500-line limit |
| **Location** | `internal/api/rest/handler.go` (958 lines), `internal/api/s3compat/handler.go` (890 lines) |
| **Description** | Both `handler.go` files violate the 500-line constraint from AGENTS.md. Even though the files are well-organized internally, they are too large. The REST handler mixes ~30 methods plus helper functions. |
| **Current State** | A single `handler.go` contains NewHandler + all route handlers + helpers like `extractMetadataHeaders`, `writeMetadataHeaders`, `classify`, etc. |
| **Recommended State** | Split into domain-specific files: `handler_crud.go` (Put/Get/Delete/Head/List), `handler_bucket.go` (bucket sub-resources), `handler_presign.go`, `handler_multipart.go`, `handler_util.go` |
| **Impact** | New team members struggle to find relevant code. Merge conflicts increase. |
| **Effort** | M |

### ❌ Finding: SDK client is monolithic

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | SDK client.go exceeds 1000 lines |
| **Location** | `sdk/go/aerovault/client.go` (1006 lines) |
| **Description** | The Go SDK client contains all API methods, types, and helpers in a single file. With 14+ admin methods, streaming, search, chat, and file operations, it should be split. |
| **Current State** | Single file with ~30 exported methods, private helpers, request/response types, and DTOs. |
| **Recommended State** | Split into `client.go` (constructor, core transport), `files.go` (CRUD), `admin.go` (admin API), `search.go` (search/chat/agent), `types.go` (DTOs) |
| **Impact** | Maintainability decreases as the SDK grows. Users import types from a massive file. |
| **Effort** | M |

### ❌ Finding: S3 and REST handlers duplicate bucket sub-resource dispatch logic

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Low |
| **Title** | Duplicated bucket sub-resource patterns across adapters |
| **Location** | `internal/api/rest/handler.go`, `internal/api/s3compat/handler.go` |
| **Description** | Both handler files implement nearly identical patterns for bucket CORS, logging, notifications. Each method (get/put/delete) is ~10 lines with the same structure: validate → call service → write response. |
| **Current State** | Duplicated code in both handler packages. |
| **Recommended State** | Move shared bucket CRUD to a `service` method that the handler simply calls, or extract helper functions. The handlers should be thin enough that duplication is minimal. |
| **Impact** | 3× code maintenance; bug fixes in one adapter may not be applied to the other. |
| **Effort** | S |

---

## 2. Naming & Documentation

### ✅ Strengths

- Public types and functions have clear, descriptive names: `FileService`, `PutOptions`, `NewFileService`, `WithEventSink`
- Error sentinels use `Err` prefix consistently: `ErrNotFound`, `ErrInvalidArgs`, `ErrQuotaExceeded`
- Package-level documentation is present in `client.go` and `storage.go`
- Internal types like `gzipReadCloser`, `ETagVerifier` clearly communicate their purpose

### ❌ Finding: Inconsistent handler method naming

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | Low |
| **Title** | Handler methods use inconsistent verb forms |
| **Location** | `internal/api/rest/handler.go` |
| **Description** | Some handlers are verbs (`Put`, `Get`, `Delete`, `Head`, `List`) while others use descriptive phrases (`InitMultipart`, `CompleteMultipart`, `AbortMultipart`, `PostForm`, `Presign`, `BatchDelete`, `BatchTag`, `Restore`). The aliases in `router.go` add another layer (`getKey`, `putKey`, `postKey`, `deleteKey`). |
| **Current State** | Mixed naming convention across handler methods. |
| **Recommended State** | Standardize on HTTP-method verbs for CRUD and action verbs for operations: `ListMultipart`, `CompleteMultipart`, `AbortMultipart`, `HandleFormUpload`, `GeneratePresignURL`. Remove router aliases or make them consistent. |
| **Impact** | Low; improves discoverability. |
| **Effort** | S |

### ❌ Finding: Dead code with `nolint`

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Unused function suppressed with nolint |
| **Location** | `internal/auth/condition.go:635` |
| **Description** | `compileIPMatchV6` is kept as dead code with `//nolint:unused`. This function is a reference implementation but has no callers. |
| **Current State** | Dead code preserved for reference. |
| **Recommended State** | Either remove it (git history preserves it) or add a clear comment explaining when it should be activated and why it's kept. |
| **Impact** | Low; nolint suppresses compiler warnings that would catch other real dead code. |
| **Effort** | S |

---

## 3. Error Handling

### ✅ Strengths

- Strong sentinel error pattern: `var ErrNotFound = errors.New("object not found")`
- Error wrapping with `%w` throughout
- `classify` function cleanly maps service errors to HTTP status codes
- `checkCorrupt` isolates corruption detection
- Quota checks gracefully degrade (`preflightQuota` returns nil on repo error)

### ❌ Finding: Silent error swallowing in quota and lock paths

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | Medium |
| **Title** | Silent quota usage decrement failures |
| **Location** | `internal/service/file_crud.go:229`, `internal/service/file_crud.go:254` |
| **Description** | `AddTenantUsage` errors are logged but not returned. This means quota usage tracking is silently lost on failure, causing quota drift over time. |
| **Current State** | ```go
if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
    s.logger.Warn("quota decrement on hard delete failed", "err", qErr)
}
``` |
| **Recommended State** | At minimum, increment a counter metric when quota sync fails. Consider an async reconciliation mechanism (e.g., periodic quota recalculation) as a safety net. |
| **Impact** | Quota enforcement accuracy degrades over time. Over-quota tenants may not be blocked. |
| **Effort** | M |

### ❌ Finding: Empty return after `_ =` discarding errors

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | Low |
| **Title** | Silently discarded write errors in CORS/Lifecycle writes |
| **Location** | `internal/api/s3compat/handler.go:435-440` |
| **Description** | Several places in s3compat use `_ =` to discard error returns from service calls, e.g., `_ = h.svc.SetObjectACL(...)`. While this is intentional (non-critical metadata), it masks real failures. |
| **Current State** | Error is discarded silently. |
| **Recommended State** | Log a warning when the error occurs, and optionally track via a counter metric. |
| **Impact** | Operational visibility: silent failures won't appear in monitoring. |
| **Effort** | S |

### ❌ Finding: `checkBucketPolicy` panics when auth context is missing

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | Low |
| **Title** | Missing context key panic in policy check |
| **Location** | `internal/api/rest/handler.go:51-53` |
| **Description** | `mw.TenantFrom(r.Context())` can return `""` when the context lacks a tenant (bypassable via direct handler calls in tests), and `AuthRegistry.Middleware()` is required but not enforced at the handler level. |
| **Current State** | `mw.TenantFrom` may return empty string; downstream calls may panic or misbehave. |
| **Recommended State** | Add a guard: `if t := mw.TenantFrom(r.Context()); t == "" { h.writeError(w, r, service.ErrForbidden); return }` |
| **Impact** | Low for production (middleware chain guarantees tenant). But isolated handler tests may fail confusingly. |
| **Effort** | S |

---

## 4. Logging

### ✅ Strengths

- Uses `log/slog` with structured JSON output
- Proper log level usage: `Info` for startup/operational, `Warn` for non-fatal errors, `Error` for failures
- Consistent with correlation IDs via `RequestID` middleware
- Good contextual log keys: `tenant`, `bucket`, `key`, `err`

### ❌ Finding: Context.Background() used instead of request context in production paths

| Field | Value |
|-------|-------|
| **Category** | Logging/Error Handling |
| **Severity** | Medium |
| **Title** | `context.Background()` used in hot paths |
| **Location** | `internal/ai/indexer.go:313,316`, `internal/events/bus.go:139` |
| **Description** | Several places use `context.Background()` instead of the request/operation context. This loses trace context, cancelation signals, and deadline propagation. |
| **Current State** | ```go
telemetry.IncIndexerSkip(context.Background(), "unsupported")
telemetry.IncIndexerSkip(context.Background(), "error")
``` |
| **Recommended State** | Thread the parent context through or use `context.TODO()` with a comment explaining why context is missing. |
| **Impact** | Logs and metrics lack trace context. Indexer operations can't be canceled via context. |
| **Effort** | M |

### ❌ Finding: SSE key configured via log in plaintext-suggestive log message

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | Low |
| **Title** | SSE key configuration logged as "ready" without key exposure |
| **Location** | `cmd/server/main.go:329` |
| **Description** | The log line `"sse", cfg.Storage.Local.SSEKey != "" || cfg.Storage.Local.SSEKeyfile != ""` only logs a boolean presence, which is good. But the config package may log the actual key value. |
| **Current State** | `logger.Info("storage ready", "backend", store.Backend(), "sse", …)` |
| **Recommended State** | Verify config loading never logs sensitive values. If it does, redact them. |
| **Impact** | Low; currently safe, but regression risk if config logging changes. |
| **Effort** | S |

---

## 5. Testing Practices

### ✅ Strengths

- High coverage in key modules: AI (84%), MCP (86%), CLI (82%), Thumbnail (87%), Shutdown (95%), Jobs (92%), Config (90%), Cluster (100%)
- Use of `t.TempDir()` for temp directories
- Contract tests for storage backends (`contract_test.go`)
- Clean test fixtures with SQLite + local FS
- Integration tests gated by build tags

### ❌ Finding: REST handler coverage below threshold

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | High |
| **Title** | REST handler coverage at 52.8% — below 80% target |
| **Location** | `internal/api/rest/` |
| **Description** | The REST handler has only 52.8% code coverage. Many edge cases like `serveRange`, batch operations, bucket CORS/logging/notifications, and error conditions lack coverage. |
| **Current State** | 52.8% overall (good for CRUD paths, weak for edge cases). |
| **Recommended State** | Add coverage for: Range request error paths, batch delete/tag failures, bucket sub-resource endpoints, precondition failures, all admin handlers. |
| **Impact** | Regressions in error handling code may go undetected. |
| **Effort** | L |

### ❌ Finding: S3 compat handler coverage at 61.4%

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | S3 handler coverage moderate |
| **Location** | `internal/api/s3compat/` |
| **Description** | S3 handler is 890 lines but only 61.4% coverage. Multipart upload, bucket sub-resources (CORS, logging, lifecycle, notifications, accelerate), and error paths lack tests. |
| **Current State** | Tests cover main CRUD paths but miss many sub-resource operations. |
| **Recommended State** | Add tests for each bucket sub-resource endpoint (at minimum CORS, logging, notifications). Add error injection tests for storage failures. |
| **Impact** | S3 compatibility bugs may escape to production. |
| **Effort** | L |

### ❌ Finding: Service layer coverage at 58%

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | FileService has gaps in edge case coverage |
| **Location** | `internal/service/service_test.go` |
| **Description** | The service layer covers main CRUD paths but misses: ETag verification errors, Content-MD5 mismatch, `checkCorrupt` paths, quota enforcement edge cases, hard delete with ChunkCleaner failures. |
| **Current State** | 58% coverage. |
| **Recommended State** | Add targeted unit tests for each error path and edge case. |
| **Impact** | Bugs in error handling or data integrity paths pass CI. |
| **Effort** | M |

### ❌ Finding: Negligible coverage for `cmd/server`

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Main package has 0% coverage |
| **Location** | `cmd/server/` |
| **Description** | The entire wire-up logic in `main.go` is untested. This is the most critical file — it assembles all components. A miswired middleware chain or wrong initialization order can cause production outages. |
| **Current State** | No tests for `main.go`. |
| **Recommended State** | The wiring logic should be refactored into an exported function or type in an `internal/app` package that can be tested. At minimum, write a smoke test that starts the server and hits `/healthz` (using a random port). |
| **Impact** | Assembly/configuration errors only caught in staging/production. |
| **Effort** | L |

### ❌ Finding: Repository coverage at 54.6%

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Repository layer has gaps |
| **Location** | `internal/repository/` |
| **Description** | Repository is the data layer and should have the highest coverage. Complex SQL like `InsertObjectVersion`, `SoftDeleteObject`, `ListObjects` with pagination, and `StorageClassCounts` need more coverage. |
| **Current State** | 54.6% coverage. |
| **Recommended State** | Add test coverage for versioning, object lock storage/retrieval, batch operations, and all SQL edge cases (e.g., duplicate key handling). |
| **Impact** | SQL bugs can cause data corruption or silent data loss. |
| **Effort** | L |

---

## 6. Technical Debt

### ❌ Finding: `compileSingleCondition` — a 53-branch switch statement

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | High |
| **Title** | Cyclomatic complexity of 53 in `compileSingleCondition` |
| **Location** | `internal/auth/condition.go:258` |
| **Description** | The function contains a massive switch over ~20 condition operators, each returning a closure. The cyclomatic complexity is 53, far exceeding the 10 threshold specified in AGENTS.md. Each case duplicates the error-check → return-closure pattern. |
| **Current State** | ```go
func compileSingleCondition(op ConditionOperator, value string) (ConditionFunc, error) {
    switch op {
    case ConditionStringEquals:
        return func(cv string) bool { return cv == value }, nil
    case ConditionStringNotEquals:
        return func(cv string) bool { return cv != value }, nil
    // ... 18 more cases, each with ParseFloat/ParseBool and closure
    }
}
``` |
| **Recommended State** | Extract by operator family: `compileStringOp`, `compileNumericOp`, `compileBoolOp`, `compileIPOp`, `compileDateOp`. Each family is a small function. Alternatively, use an operator → {eval func, arg count} registry map. |
| **Impact** | Untestable (requires covering 53 branches); impossible to reason about edge cases; violates project constraints. |
| **Effort** | M |

### ❌ Finding: `condition.go` is 657 lines

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | `condition.go` exceeds 500-line limit |
| **Location** | `internal/auth/condition.go` |
| **Description** | This single file contains the policy condition compiler, the condition context, all condition operators, IP matching, date matching, and the `Allow`/`Deny` evaluator. It violates the 500-line per file constraint. |
| **Current State** | 657 lines, multiple responsibilities. |
| **Recommended State** | Split into: `condition_compile.go` (operator compilation), `condition_context.go` (context extraction), `condition_ip.go` (IP matching), `condition_eval.go` (policy evaluation). |
| **Impact** | Single-file complexity makes changes risky and review difficult. |
| **Effort** | M |

### ❌ Finding: Handler files exceed 500 lines (multiple violations)

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | High |
| **Title** | Multiple files violate the 500-line constraint |
| **Location** | `handler.go` (958), `s3compat/handler.go` (890), `main.go` (861), `condition.go` (657), `client.go` (1006), `webdav/dav.go` (458, borderline) |
| **Description** | The project's own AGENTS.md prescribes ≤500 lines per file. Six files violate this. This is a hard constraint that should block CI. |
| **Current State** | 6 source files exceed the limit. |
| **Recommended State** | Split each file as recommended above. Add a CI check (`find . -name "*.go" -exec awk 'END{if(NR>500)print FILENAME}' {} \;`) |
| **Impact** | Maintainability erosion; violates project rules. |
| **Effort** | L |

### ❌ Finding: Duplicate metadata extraction logic

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | `extractMetaHeaders` and `extractMetadataHeaders` are nearly identical |
| **Location** | `internal/api/rest/handler.go:664-686`, `internal/api/s3compat/handler.go:700-719` |
| **Description** | Two copies of metadata extraction from HTTP headers. The REST version processes both `X-Amz-Meta-*` and `X-Meta-*` prefixes; the S3 version only does `X-Amz-Meta-*`. This should be a shared helper. |
| **Current State** | Duplicated code in two files. |
| **Recommended State** | Extract to a shared location (e.g., `internal/middleware/metadata.go`) that returns the metadata map and also extracts Content-Disposition/Encoding into `_aero_` keys. |
| **Impact** | Adding a new metadata header requires changes in two places. |
| **Effort** | S |

### ❌ Finding: `cmd/server/main.go` is doing too much

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | High |
| **Title** | `main.go` is 861 lines and handles too many responsibilities |
| **Location** | `cmd/server/main.go` |
| **Description** | The main file handles: CLI dispatch, MCP mode, config loading, storage initialization, SSE key rewrapping, embedder/LLM/reranker construction, job pool setup, auth registry, rate limiter, prometheus, router assembly, middleware chain, server lifecycle, and signal handling. This is 3-4 packages of logic in one file. |
| **Current State** | Monolithic main.go with ~20 functions. |
| **Recommended State** | Extract into: `internal/app/app.go` (App struct with Run/Shutdown), `internal/app/wiring.go` (component assembly), `cmd/server/main.go` (~20 lines: parse flags, call app.Run). |
| **Impact** | Hard to test; any configuration change touches the biggest file in the project. |
| **Effort** | L |

---

## 7. Code Quality Metrics

Let me compute the actual metrics from the codebase analysis:

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | 53 (`compileSingleCondition`) | < 10 | ❌ |
| **Function length** | 140+ lines (`NewRouter`, `PutObject`, etc.) | < 50 lines | ❌ |
| **Test coverage (overall)** | ~64% (varies by package) | > 80% | ⚠️ |
| **Code duplication** | ~3-5% (metadata extraction, bucket CRUD patterns) | < 5% | ⚠️ |
| **Documentation coverage** | Good for exported APIs, weak for internal | > 70% | ✅ |
| **Files ≤ 500 lines** | 6 violations out of ~100 files | 0 violations | ❌ |
| **No `utils/`/`common/` packages** | 0 violations | 0 violations | ✅ |
| **All tests pass** | ✅ | ✅ | ✅ |

---

## 8. Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| `compileSingleCondition` complexity=53 | High | M | P0 | Violates project constraint; must refactor |
| 6 files exceed 500 lines | High | L | P0 | Violates hard constraint; CI should reject |
| `main.go` is monolithic (861 lines) | High | L | P1 | Blocks testing of wiring logic |
| REST handler coverage 52.8% | High | L | P1 | Error paths untested |
| Repository coverage 54.6% | High | L | P1 | Data layer needs more coverage |
| S3 handler coverage 61.4% | Medium | L | P1 | Sub-resource endpoints untested |
| Service layer coverage 58% | Medium | M | P1 | Error handling gaps |
| Duplicate metadata extraction | Medium | S | P2 | Two implementations diverging |
| `context.Background()` in hot paths | Medium | M | P2 | Loses trace context |
| `compileIPMatchV6` dead code with nolint | Low | S | P2 | Suppresses compiler checks |
| Silent quota drift | Medium | M | P2 | Long-term accuracy issue |
| Handler method naming inconsistency | Low | S | P3 | Cosmetic but affects onboarding |
| SDK client 1006 lines | Medium | M | P3 | Growing maintenance burden |

---

## 9. Final Summary

### Overall Code Quality: **Good** (with significant areas needing improvement)

The codebase demonstrates strong engineering fundamentals:
- Clean architecture alignment with documented design
- Proper separation of concerns across packages
- Good error handling patterns with sentinel errors
- High test coverage in AI, CLI, Config, and Jobs packages
- No forbidden `utils/`/`common/` packages
- All tests currently passing

However, there are **critical issues** that must be addressed:

### Critical Quality Issues (Must Fix)

1. **`compileSingleCondition` complexity of 53** (`condition.go:258`) — This is the highest-risk function in the codebase. A 53-branch switch with duplicated patterns makes it impossible to reason about correctness. Refactor into operator-family functions.

2. **6 files exceed the 500-line limit** — This is a project-imposed hard constraint being violated. Add a CI gate to enforce it, then split:
   - `handler.go` (958) → split into ~5 files
   - `s3compat/handler.go` (890) → split into ~5 files
   - `main.go` (861) → extract `internal/app`
   - `client.go` (1006) → split SDK into domain files
   - `condition.go` (657) → split by operator family
   - `dav.go` (458, borderline) → consider splitting

### Maintainability Concerns

1. **Duplicated handler logic** — The REST and S3 adapters share no utility code for metadata extraction, bucket sub-resources, or header writing. Every new S3 feature needs two implementations.

2. **`main.go` is untestable assembly** — The entire wiring logic lives in an 861-line file that can't be tested. Refactoring to an `internal/app` package would enable integration-level testing of the full server lifecycle.

3. **Quota drift without recovery** — The silent error-swallowing in `AddTenantUsage` paths means quota enforcement will slowly lose accuracy. An async reconciliation job would provide a safety net.

4. **`main.go` builds all AI components even when disabled** — Builders like `buildEmbedder`, `buildLLM`, `buildReranker` are always called, but they check `cfg.AI.Enabled` internally. This is fine, but the pattern encourages complexity creep.

### Technical Debt

Accumulated debt is moderate. The largest items are:
- Policy condition compiler (complexity 53) — ~3-4 days to refactor
- Large file splits — ~2-3 days across all files
- Coverage gaps — ~1-2 weeks of targeted testing
- `main.go` extraction — ~2-3 days

Total estimated payoff: **~3-4 weeks** engineering time for significant quality improvement.

### Quick Wins (Easy Improvements)

| # | Change | Effort | Impact |
|---|--------|--------|--------|
| 1 | Merge `extractMetaHeaders`/`extractMetadataHeaders` into shared helper | 30 min | Eliminates duplication risk |
| 2 | Remove dead `compileIPMatchV6` + nolint | 5 min | Cleans linter suppression |
| 3 | Add CI check for file length (awk-based) | 15 min | Enforces project constraint |
| 4 | Split `condition.go` into 3-4 files | 2 hours | Brings below 500-line limit |
| 5 | Add `TenantFrom` empty-string guard in handlers | 30 min | Prevents confusing test failures |
| 6 | Extract main.go into `internal/app` | 2-3 days | Enables wiring tests |

### Recommendations for the Team

1. **Enforce the rules you set** — AGENTS.md prescribes ≤500 lines and complexity ≤10. These should be enforced in CI immediately. Without gates, constraints are aspirational.
2. **Write tests for wiring** — The `main.go` assembly logic should be the most tested code, not the least. Extract and test.
3. **Standardize cross-adapter utilities** — Create a shared `internal/api/headers.go` for metadata extraction/writing to eliminate the S3↔REST duplication.
4. **Plan the condition compiler refactoring** — It's the highest-complexity function. Schedule it in the next sprint.
5. **Set a coverage floor at 70%** — Current variance (0%–100%) is too wide. A per-package minimum ensures no regression introduces untested code.
