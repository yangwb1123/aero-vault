Now I have enough data for a comprehensive review. Let me compile the full analysis.

---

# Staff Engineer Code Review — `aero-vault`

## Executive Summary

| Dimension | Grade | Key Concern |
|-----------|-------|-------------|
| **Code Organization** | Good | Well-layered, but files exceed the 500-line constraint |
| **Naming & Documentation** | Good | Generally clear naming; some missing doc comments |
| **Error Handling** | Needs Work | Inconsistent patterns; swallowed errors; unbounded error messages |
| **Logging** | Good | Structured JSON logging, correlation IDs present |
| **Testing** | Good | Good overall coverage, but some core packages below threshold |
| **Technical Debt** | Noteworthy | High-complexity functions, duplicated code, gofmt violations |
| **Code Quality** | Good | Reasonable metrics with some hotspots |

---

## Detailed Findings

### Finding 1: File Size Limit Violations (AGENTS.md rule)

| Field | Value |
|-------|-------|
| Category | **Organization** |
| Severity | **High** |
| Title | Multiple production files exceed 500-line limit |
| Location | `internal/auth/condition.go` (657), `internal/api/rest/handler.go` (958), `cmd/server/main.go` (861), `internal/api/s3compat/handler.go` (890) |
| Description | AGENTS.md mandates max 500 lines per file. Four production files violate this, making them hard to navigate and maintain. |
| Current State | Large monolithic files mixing distinct concerns (handler.go: Put, Get, List, Head, etc.) |
| Recommended State | Split condition.go by operator family, split handler.go into separate files per resource (files, buckets, admin, batch) |
| Impact | Maintainability: new developers must parse >800 lines to understand one handler file. Increases merge conflicts. |
| Effort | **M** |

### Finding 2: Extremely High Cyclomatic Complexity

| Field | Value |
|-------|-------|
| Category | **Quality** |
| Severity | **High** |
| Title | `compileSingleCondition` has cyclomatic complexity of 53 (target ≤ 10) |
| Location | `internal/auth/condition.go:258` |
| Description | This function is a single switch statement with ~25 branches for each condition operator. While each branch is simple, the complexity metric exceeds the target by 5x. |
| Current State | A single function handling all operator types in one switch |
| Recommended State | Split into operator-family functions (`compileStringCondition`, `compileNumericCondition`, `compileDateCondition`, `compileIPCondition`, `compileARNCondition`) mapped from operator to handler via a `map[ConditionOperator]func(string) (ConditionFunc, error)` |
| Impact | Testability: 53 independent paths in one function make exhaustive testing impractical. |
| Effort | **M** |

### Finding 3: Auth Condition Complexity (ConditionContext.Get)

| Field | Value |
|-------|-------|
| Category | **Quality** |
| Severity | **Medium** |
| Title | `ConditionContext.Get` has complexity 18 |
| Location | `internal/auth/condition.go:90` |
| Description | The `Get` method has a 17-way key prefix match to resolve IAM condition keys (aws:SourceIp, s3:x-amz-acl, etc.). Complex and fragile to extend. |
| Current State | Single function with manual prefix matching for every known key |
| Recommended State | Register key handlers in a map at init time, or use a switch-based registry pattern |
| Impact | Adding a new condition key requires modifying this central function |
| Effort | **M** |

### Finding 4: Middleware Chain Order — Documentation Deviation

| Field | Value |
|-------|-------|
| Category | **Organization** |
| Severity | **Medium** |
| Title | Middleware chain order differs from AGENTS.md specification |
| Location | `cmd/server/main.go:applyMiddleware` |
| Description | AGENTS.md mandates: `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`. The code applies these in reverse (wrapping inside-out), resulting in an extra `Concurrency` layer between Recoverer and AccessLog. While the extra layer may be intentional, the doc should be updated. |
| Current State | Effective order: `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → Concurrency → AccessLog` |
| Recommended State | Update AGENTS.md to document the actual chain, or remove Concurrency if not needed at that position |
| Impact | Documentation drift — future maintainers may rely on the documented chain order |
| Effort | **S** |

### Finding 5: `gofmt` Violations

| Field | Value |
|-------|-------|
| Category | **Technical Debt** |
| Severity | **Medium** |
| Title | Two files fail `gofmt -l`, violating CI gate |
| Location | `internal/auth/arn_test.go`, `internal/auth/condition.go` |
| Description | AGENTS.md CI gate requires `gofmt -l .` to produce no output. Two files fail this check. |
| Current State | Non-canonical formatting present |
| Recommended State | Run `gofmt -w` on both files |
| Impact | CI gate violation — submit would be rejected |
| Effort | **S** |

### Finding 6: `FileService.Put` Complexity (13) and Error Handling

| Field | Value |
|-------|-------|
| Category | **Quality** |
| Severity | **Medium** |
| Title | `FileService.Put` exceeds complexity target and mixes concerns |
| Location | `internal/service/file_crud.go:71` |
| Description | The `Put` method handles validation, lock check, versioning, MD5 verification, storage write, and repo write in one function (13 cyclomatic complexity, 89 lines). When `verifyMD5()` fails, the storage blob is deleted — but this delete itself can fail silently. |
| Current State | Monolithic Put function doing everything inline |
| Recommended State | Extract helper functions: `prepareStorageKey`, `prepareVersioning`, `verifyAndCleanup`, `finalizeObject` |
| Impact | Testing all paths requires complex setup; error recovery path (post-MD5-failure delete) is not itself error-handled |
| Effort | **M** |

### Finding 7: `chat.go` Code Duplication

| Field | Value |
|-------|-------|
| Category | **Quality** |
| Severity | **Medium** |
| Title | `Answer` and `AnswerStream` share ~90% identical code |
| Location | `internal/ai/chat.go` |
| Description | Both methods build the prompt identically, then record usage identically. The differences are only in the LLM call (streaming vs non-streaming) and how tokens/latency are measured. |
| Current State | Two separate methods with duplicated prompt-building, budget check, usage recording, and citation building |
| Recommended State | Extract common logic: `buildChatPrompt`, `recordChatUsage`, then have both call these. Consider a single `AnswerInternal` that the stream/non-stream variants call. |
| Impact | Bug fix in one method (e.g., citation formatting) must be manually applied to both |
| Effort | **S** |

### Finding 8: `RegisterStorageClassGauge` Only Scans Default Tenant

| Field | Value |
|-------|-------|
| Category | **Observability** |
| Severity | **Medium** |
| Title | Storage class gauge only reads "default" tenant |
| Location | `internal/telemetry/metrics.go:RegisterStorageClassGauge` |
| Description | The callback hardcodes `fn(ctx, "default")`, so multi-tenant deployments get incomplete storage class metrics. |
| Current State | `for cls, count := range fn(ctx, "default") { ... }` |
| Recommended State | Pass the tenant list from config, or register one gauge per known tenant. The `fn` signature should be `func(ctx, tenant string) map[string]int64` and the gauge callback should iterate all tenants. |
| Impact | Operators in multi-tenant setups cannot see per-tenant storage class distribution |
| Effort | **S** |

### Finding 9: `chat.go` — `hardDeleteObject` / `softDeleteObject` Duplication

| Field | Value |
|-------|-------|
| Category | **Quality** |
| Severity | **Low** |
| Title | Nearly identical quota decrement and emit logic in delete paths |
| Location | `internal/service/file_crud.go` |
| Description | Both `hardDeleteObject` and `softDeleteObject` call `s.repo.AddTenantUsage` and `s.emit` with the same structure. Only the repo call differs. |
| Current State | Two functions each repeating the emit + AddTenantUsage + error log pattern |
| Recommended State | Extract common tail logic into a helper: `deletePostProcess(ctx, obj, tenant)` |
| Impact | Future changes to post-delete actions must be applied in two places |
| Effort | **S** |

### Finding 10: PerTenantConcurrencyLimiter TOCTOU Race

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | Global semaphore slots leaked on tenant budget rejection |
| Location | `internal/middleware/middleware.go:211` |
| Description | When global semaphore is acquired but the per-tenant budget check fails, global slots are released. An adversary with valid tenant credentials could cause global slots to be acquired and released without doing work, reducing throughput. |
| Current State | Global slots acquired first, then tenant check — release on failure |
| Recommended State | Check tenant budget before acquiring global slots. Restructure: check tenant → acquire global → register inflight |
| Impact | Denial-of-service vector where a fast-spawning tenant exhausts global semaphore by forcing acquire/release cycles |
| Effort | **S** |

### Finding 11: Agent `read_file` Hardcoded 4KB Limit

| Field | Value |
|-------|-------|
| Category | **Design** |
| Severity | **Low** |
| Title | Agent read_file truncates at 4KB with no configuration |
| Location | `internal/ai/agent.go` (line with `4<<10`) |
| Description | The Agent tool `read_file` hardcodes `io.LimitReader(rc, 4<<10)`, forcing an arbitrary 4KB limit on all files regardless of size or content type. |
| Current State | `body, _ := io.ReadAll(io.LimitReader(rc, 4<<10))` |
| Recommended State | Make the limit configurable via `Agent.MaxReadBytes` with a sensible default, or use the first N bytes plus a summary |
| Impact | The agent cannot read documents larger than 4KB — undermines RAG tool-calling utility |
| Effort | **S** |

### Finding 12: Missing Test Coverage in Core Packages

| Field | Value |
|-------|-------|
| Category | **Testing** |
| Severity | **Medium** |
| Title | Core packages below 80% coverage threshold |
| Location | `internal/service` (58.0%), `internal/storage` (57.3%), `internal/repository` (54.6%), `internal/events` (64.0%), `internal/reconcile` (60.6%), `internal/api/rest` (52.8%) |
| Description | AGENTS.md says 80% target. The six core business-logic packages are significantly below this. |
| Current State | Package-level coverage ranges from 52.8% to 64.0% |
| Recommended State | Add tests for error paths, edge cases (empty keys, nil metadata, versioning corner cases), and quota boundary conditions |
| Impact | Untested error paths can hide bugs; regression risk is higher than acceptable |
| Effort | **L** |

### Finding 13: Swallowed Errors in `Run()` Shutdown

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | `shutdownOtel` error swallowed in `runServer` |
| Location | `cmd/server/main.go:204` (`_ = shutdownOtel(shutdownCtx)`) |
| Description | OTEL shutdown error is silently discarded with `_ =`, even though this could lose telemetry data during flush. |
| Current State | `_ = shutdownOtel(shutdownCtx)` |
| Recommended State | Log the error instead: `if err := shutdownOtel(shutdownCtx); err != nil { logger.Warn("otel shutdown", "err", err) }` |
| Impact | OTel exporter may fail to flush pending spans/metrics during shutdown without any notification |
| Effort | **S** |

### Finding 14: Missing Doc Comments on Public Types

| Field | Value |
|-------|-------|
| Category | **Naming & Documentation** |
| Severity | **Low** |
| Title | Several exported types lack doc comments |
| Location | `internal/service/file.go` (`ReadVerificationConfig`, `PutOptions`), `internal/ai/indexer.go` |
| Description | Exported types in Go should have doc comments per go convention. While some have comments, several are undocumented. |
| Current State | Some exported types have no doc comments |
| Recommended State | Add //-style doc comments to all exported types |
| Impact | godoc output is incomplete; new developers must read implementation to understand purpose |
| Effort | **S** |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | 53 (worst: `compileSingleCondition`) | < 10 | ❌ |
| **File length** | 958 lines max (handler.go) | ≤ 500 lines | ❌ (4 files exceed) |
| **Test coverage** | 52.8%–92.0% | > 80% | ⚠️ (6 packages below) |
| **Code duplication** | Moderate (chat.go Answer/AnswerStream, crud.go delete paths) | < 5% | ⚠️ |
| **Documentation coverage** | ~60% of exports documented | > 70% | ⚠️ |
| **gofmt compliance** | 2 files failing | 0 | ❌ |
| **go vet compliance** | Clean | Clean | ✅ |

---

## Technical Debt Register

| # | Item | Impact | Effort | Priority | Notes |
|---|------|--------|--------|----------|-------|
| 1 | `compileSingleCondition` complexity 53 | High | M | P0 | Must fix before adding new condition operators |
| 2 | File size violations (4 production files) | Medium | M | P1 | Violates AGENTS.md — blocks CI |
| 3 | `gofmt` violations | Medium | S | P0 | Blocks CI gate immediately |
| 4 | Missing test coverage (6 packages) | Medium | L | P1 | Increased regression risk |
| 5 | chat.go code duplication | Medium | S | P2 | Duplicate prompt-building and usage-recording |
| 6 | `RegisterStorageClassGauge` default-tenant only | Medium | S | P2 | Multi-tenant observability gap |
| 7 | Agent read_file 4KB hard limit | Low | S | P2 | Limits agent utility |
| 8 | Middleware chain doc drift | Low | S | P3 | Minor doc inconsistency |
| 9 | PerTenantConcurrencyLimiter TOCTOU | Low | S | P3 | Targeted DoS potential |
| 10 | Swallowed OTEL shutdown error | Low | S | P3 | Logging improvement |

---

## Final Summary

### Overall Code Quality: **Good** (with notable exceptions)

The codebase is well-structured with clear layering (handler → service → repository/storage), proper use of interfaces for testability, and good use of domain-driven package organization. The AGENTS.md document is a commendable reference for architectural standards.

### Critical Quality Issues (P0 — Must Fix Before Next Merge)

1. **`gofmt` violations** in `internal/auth/arn_test.go` and `internal/auth/condition.go` — these block the CI gate immediately.
2. **`compileSingleCondition` complexity of 53** — this is 5× the target and makes the code untestable exhaustively. Refactor into operator-family dispatch maps.
3. **File size violations** in 4 production files — per AGENTS.md §0, these must be split before further work.

### Maintainability Concerns

- **High-complexity hotspots** in the auth package (`condition.go`) represent the highest long-term maintenance risk. Adding a new condition operator or condition key requires modifying the same already-complex functions.
- **Code duplication** in the chat layer (Answer vs AnswerStream) means bugs will be duplicated too.
- **Uneven test coverage** across core packages (service 58%, repository 54.6%) means the most critical business logic has the least safety net.

### Technical Debt

The largest debt item is the auth condition evaluation engine, which has evolved into a 657-line file with a complexity-53 function. This is the single highest-value refactoring target. The second largest is the REST handler at 958 lines, which should be split by resource domain.

### Quick Wins (S effort, high impact)

1. Run `gofmt -w internal/auth/arn_test.go internal/auth/condition.go` → unblocks CI
2. Extract shared prompt-build + usage-record from `chat.go` Answer/AnswerStream → eliminates duplication
3. Log OTEL shutdown error instead of swallowing → improves observability
4. Register all tenants in `RegisterStorageClassGauge` rather than just `"default"` → fixes multi-tenant metrics
5. Add doc comments to exported types missing them → improves developer experience
6. Fix PerTenantConcurrencyLimiter TOCTOU (check tenant before acquiring global semaphore) → security improvement
