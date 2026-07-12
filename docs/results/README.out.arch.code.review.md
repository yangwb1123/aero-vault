Now I have a comprehensive understanding of the codebase. Here is my full Staff Engineer review.

---

# Staff Engineer Code Review: Aero-Vault

## Executive Summary

The Aero-Vault project is a sophisticated Go-based object storage system with a well-defined architecture and clean separation of concerns. The codebase demonstrates strong engineering practices in many areas — clear domain boundaries, consistent use of interfaces, robust storage contract tests, and solid AI/RAG pipeline abstraction. However, there are **several critical violations of the team's own engineering constraints** (documented in `AGENTS.md`) and notable technical debt that must be addressed.

---

## 1. Critical: Engineering Constraint Violations

### Finding 1: File Size Limit Violations (Multiple Files)

| Field | Description |
|-------|-------------|
| Category | Organization |
| Severity | **Critical** |
| Title | Production files exceed 500-line limit per AGENTS.md |
| Location | `cmd/server/main.go` (861), `internal/api/rest/handler.go` (958), `internal/api/s3compat/handler.go` (890), `internal/auth/condition.go` (657) |
| Description | AGENTS.md mandates "单文件 ≤ 500 行" and states "违反者将在 HARNESS.md 定义的自动检查中被拒绝". Four production source files exceed this limit. |
| Current State | `main.go` is a monolith containing server assembly, storage building, middleware chain construction, auth registry setup, AI component wiring, background workers, and more — all in one file. |
| Recommended State | Split `main.go` into package-level builders (e.g., `server/server.go`, `ai/wiring.go`, `worker/wiring.go`). Split `handler.go` by resource (e.g., `objects.go`, `admin.go`, `system.go`). Split `s3compat/handler.go` by S3 sub-resource (e.g., `objects.go`, `multipart.go`, `bucket.go`). |
| Impact | Violates team-agreed engineering constraints. Reviewability suffers — a single file with 900+ lines is hard to reason about. Onboarding new developers becomes harder. Merge conflicts become more likely. |
| Effort | L (large — but incremental refactoring possible) |

### Finding 2: Function Length Limit Violations (~30 functions)

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **High** |
| Title | Multiple functions exceed 50-line limit |
| Location | `config.Load()` (198 lines), `condition.go:compileSingleCondition` (215 lines), `agent.Run()` (85 lines), `mcp/server.go:listTools` (88 lines), `rest/router.go:NewRouter` (100 lines), `s3compat/xml.go:cannedFromFlags` (107 lines), `s3compat/errors.go:writeS3Error` (83 lines), `service/file_crud.go:Put` (78 lines), `cmd/server/main.go:run` (78 lines), `cmd/server/main.go:buildStorageFrom` (71 lines), `auth/condition.go:ConditionContext.Get` (54 lines), `auth/condition.go:ConditionBlock.Compile` (54 lines), and more |
| Current State | Functions contain too much logic; `config.Load()` builds a massive struct with inline env-default assignment for every field |
| Recommended State | Extract config loading into sub-builders (e.g., `loadAIConfig()`, `loadStorageConfig()`). Extract S3 error handling into a proper error-classification layer. Break `compileSingleCondition` into operator-specific builders. |
| Impact | Testability, readability, and maintainability degrade with function size. Long functions are harder to review and more likely to hide bugs. |
| Effort | L (multiple extractions across packages) |

---

## 2. Critical: Code Quality Issues

### Finding 3: Middleware Chain Violates Documented Order (I4)

| Field | Description |
|-------|-------------|
| Category | Organization |
| Severity | **Critical** |
| Title | Middleware chain in `applyMiddleware` differs from specified order in AGENTS.md |
| Location | `cmd/server/main.go:applyMiddleware` (lines 199-223) vs `AGENTS.md` §2.5 |
| Description | AGENTS.md §2.5 states middleware order must be `RequestID → CORS → Auth → Tenant → RateLimit(global) → OTel → Recoverer → AccessLog`. The actual implementation reverses this: `AccessLog → Concurrency → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID`. Additionally, `ConcurrencyLimiter` is inserted between AccessLog and Recoverer but is not documented. |
| Current State | The concurrency limiter runs _before_ the recoverer, so panics inside the concurrency limiter are uncaught. The chain order is also undocumented for the concurrency component. |
| Recommended State | Either update AGENTS.md I4 to reflect the actual chain (including ConcurrencyLimiter), or reorder the middleware to match the documented invariant. Recoverer should be outermost to catch all panics. |
| Impact | If the ConcurrencyLimiter panics (e.g., channel operations on closed semaphore), the panic is uncaught and crashes the server. Violates I4. |
| Effort | S |

### Finding 4: Context Leakage in Background Context Usage

| Field | Description |
|-------|-------------|
| Category | Error Handling |
| Severity | **High** |
| Title | Production code uses `context.Background()` instead of propagating request context |
| Location | `internal/ai/indexer.go:313,316` — telemetry calls during indexer skip; `internal/api/webdav/dav.go:302,381` — WebDAV context nil checks with Background fallback |
| Description | The indexer's `handleExtractError` uses `context.Background()` for telemetry metric calls, losing trace context and correlation IDs. The WebDAV handler falls back to `context.Background()` when `ctx` is nil on the struct, indicating the context isn't propagated properly through the webdav layer. |
| Current State | Metrics recorded in the indexer have no parent trace context. WebDAV writes lose request context (correlation ID, cancellation, timeout). |
| Recommended State | Thread `ctx` through the indexer call chain instead of using Background. Fix WebDAV `davFile`/`davDir` structs to properly propagate the request context rather than capturing it as a mutable field that can be nil. |
| Impact | Lost observability (trace breaks), no request cancellation during WebDAV operations, no deadline propagation. |
| Effort | M |

### Finding 5: Known Documented Bugs in CLI (Unfixed)

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **High** |
| Title | 6 documented bugs in CLI implementation with no fix date |
| Location | `internal/cli/cli_test.go` lines 1419-1432 |
| Description | The test file documents that `cmdList`, `cmdTag`, `cmdVersions`, `cmdLineage`, `cmdSearch` never check HTTP status codes and always return 0 on any response. `cmdSnapshot` silently ignores missing DB files. |
| Current State | BUG comments exist but no tracking ticket, no FIXME in production code, and the bugs remain unfixed. CLI returns success (exit code 0) even on 5xx errors from the server. |
| Recommended State | Fix each CLI command to check HTTP status codes before returning success. Create tracking issues. Add FIXME-annotated TODO in the production code referencing the issue number. |
| Impact | CLI commands report success for failed operations, which can break CI/CD pipelines and user scripts that rely on exit codes. |
| Effort | M |

---

## 3. Architecture and Design Issues

### Finding 6: `config.Load()` — God Function

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **High** |
| Title | `config.Load()` is a 198-line monolith |
| Location | `internal/config/config.go:43` |
| Description | `Load()` builds every config sub-struct in a single function with inline environment lookups. The function has no helper abstractions — it directly assigns ~100+ fields across ~15 sub-structs. |
| Current State | Adding a new config knob requires modifying the middle of this monolithic function. Testing config validation for specific backends is difficult. |
| Recommended State | Extract sub-loaders: `loadStorageConfig()`, `loadAIConfig()`, `loadAuthConfig()`, `loadRateLimitConfig()`, etc. Have `Load()` call each sub-loader. |
| Impact | Lowers maintainability — any config change risks breaking unrelated config blocks. Poor testability. |
| Effort | M |

### Finding 7: SQL Dialect Duplication

| Field | Description |
|-------|-------------|
| Category | Organization |
| Severity | **Medium** |
| Title | Repeated SQL dialect branching with duplicated query strings |
| Location | `internal/repository/sql_objects.go`, `internal/repository/sql_buckets.go`, and others |
| Description | Every SQL operation that differs between SQLite and Postgres duplicates the full query string in dialect-switch branches. For example, `UpsertObject` (lines 11-62) has two complete INSERT queries with minor differences (jsonb casts, `now()` vs `$13`). |
| Current State | ~15 SQL operations have duplicate query strings, making maintenance error-prone. Adding a field means editing both branches. |
| Recommended State | Use parameterized templates or build queries with a shared base and dialect-specific adornments. At minimum, extract the common query fragments. |
| Impact | Adding new columns or changing indexes requires editing 2-3 copies of every query. Missing one branch causes silent deployment failures on Postgres. |
| Effort | L |

### Finding 8: Global-State `DefaultStorageClass` and `DefaultBucket`

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **Medium** |
| Title | Mutable package-level variables used for configuration |
| Location | `internal/service/file.go:17-19` |
| Description | `DefaultStorageClass`, `DefaultBucket`, and `DefaultTenant` are mutable package variables. `WithDefaultStorageClass()` modifies a global. `storageKey()` and `defaults()` functions access these globals. |
| Current State | Any test or package that calls `WithDefaultStorageClass()` mutates state visible to all other tests and goroutines. |
| Recommended State | Pass default values through config structs instead of package-level globals. Consider making these part of `FileService` fields or config objects. |
| Impact | Test pollution (tests affecting each other via mutable globals). Hard to detect race conditions. |
| Effort | M |

---

## 4. Testing and Coverage Gaps

### Finding 9: Critical Untested Production Code

| Field | Description |
|-------|-------------|
| Category | Testing |
| Severity | **Critical** |
| Title | `cmd/server/main.go` and `internal/webui/web.go` have 0% test coverage |
| Location | `cmd/server/`, `internal/webui/web.go` |
| Description | The server assembly code (`main.go` — 861 lines of critical wiring logic) has zero automated test coverage. `webui/web.go` also has 0%. |
| Current State | The main.go assembly — which builds storage, repository, AI pipeline, event bus, middleware chain — has no integration tests validating the wiring. Deployment failures from miswired components can only be caught manually. |
| Recommended State | Add an integration test (`fullserver_test.go` exists but tests the running server, not the wiring). Add unit tests for `buildStorageFrom`, `buildEmbedder`, `buildLLM`, `buildAIComponents` in isolation. |
| Impact | Wiring bugs (e.g., nil embedder passed to indexer, wrong middleware order) are caught only in production. `main.go` changes cannot be safely refactored. |
| Effort | L |

### Finding 10: Repository Test Coverage Below Target

| Field | Description |
|-------|-------------|
| Category | Testing |
| Severity | **High** |
| Title | Repository package coverage at 54.6% — below 80% target |
| Location | `internal/repository/` |
| Description | `internal/repository/` has only 54.6% statement coverage. Critical SQL paths (`ListDeletedObjects`, `ListObjectVersionsWithOpts`, `StorageClassCounts`, `SetObjectMetaKey`, `DeleteBucket`, `ListBuckets`) may have low or no coverage. |
| Current State | The large test file `chunks_events_buckets_test.go` (922 lines) covers bucket CRUD and events but many edge cases (deleted object listing, version listing pagination, CORS rule CRUD, notification rules) are likely uncovered. |
| Recommended State | Add targeted tests for uncovered code paths identified by `go tool cover`. Target 70%+ for the repository layer. |
| Impact | SQL regressions can silently reach production. Schema migration issues might only be caught in staging. |
| Effort | L |

### Finding 11: No Race Detection in CI

| Field | Description |
|-------|-------------|
| Category | Testing |
| Severity | **Medium** |
| Title | Tests don't run with `-race` flag |
| Location | CI configuration (to be checked) |
| Description | The CI gate in AGENTS.md specifies `go test ./...` without `-race`. Several components use `sync.Mutex` and shared maps (auth registry, per-tenant concurrency limiter, BM25 index). |
| Current State | Race conditions in the auth registry (`Registry.keys` map accessed with RWMutex), the per-tenant concurrency limiter (`inflight` map), and BM25 index mutations could exist undetected. |
| Recommended State | Add `-race` to CI test command. Fix any detected races. At minimum, the `PerTenantConcurrencyLimiter` should be race-tested. |
| Impact | Latent data races can cause crashes under high concurrency. |
| Effort | S |

---

## 5. Code Quality: Specific Findings

### Finding 12: Dead Code in PII Detection

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **Low** |
| Title | `strings.Repeat("0", 0)` is dead code in `MapPII` |
| Location | `internal/ai/pii.go:120` |
| Description | `MapPII` contains `strings.Repeat("0", 0)` which always produces an empty string. The intention may have been `strconv.Itoa(v)` or padding, but it's a no-op. Since the custom `itoa` already handles the number, the `strings.Repeat` call has zero effect. |
| Current State | `parts = append(parts, k+"="+strings.Repeat("0", 0)+itoa(v))` — the `strings.Repeat` does nothing. |
| Recommended State | Remove `strings.Repeat("0", 0)` entirely: `k+"="+itoa(v)` |
| Impact | Minor — no functional impact, but represents dead code and reduces readability. |
| Effort | S |

### Finding 13: Missing OpenAPI Spec for Admin Endpoints

| Field | Description |
|-------|-------------|
| Category | Documentation |
| Severity | **Medium** |
| Title | Admin API endpoints lack OpenAPI documentation |
| Location | `internal/api/rest/admin.go`, `internal/api/rest/openapi.go` |
| Description | The admin endpoints (tenant management, API key management, audit log, JWT signing, bucket management) likely have incomplete or missing OpenAPI schema entries. |
| Current State | The OpenAPI spec is served at `/openapi.json` and `/docs`, but admin routes added after the initial spec may not be documented. |
| Recommended State | Audit `openapi.json` generation to ensure all admin endpoints are documented. Add integration tests that validate the OpenAPI spec matches actual routes. |
| Impact | SDK generators produce incomplete clients. Developers must read source code to understand admin API contracts. |
| Effort | M |

### Finding 14: Inconsistent Error Wrapping

| Field | Description |
|-------|-------------|
| Category | Error Handling |
| Severity | **Medium** |
| Title | Some errors are wrapped, some are returned raw |
| Location | Various — examples: `service/file_crud.go` wraps storage errors, `service/file_features.go` returns repo errors raw |
| Description | In `service/file_features.go`, methods like `SetTags`, `ListVersions`, `SetBucketVersioning` return repository errors directly without wrapping. In contrast, `file_crud.go` consistently wraps errors with context (e.g., `fmt.Errorf("storage put: %w", err)`). |
| Current State | Mixed approach makes it hard for callers (e.g., S3 error classification in `s3compat/errors.go`) to reliably classify errors. `s3compat/errors.go` only maps `repository.ErrNotFound` and `service.Err*` but misses raw `sql.ErrNoRows` propagating from the repository. |
| Recommended State | Every exported method in `service.FileService` should wrap errors with context. Repository methods should ensure all sentinel errors are preserved through the chain. |
| Impact | Callers may receive unexpected error types, causing 500s instead of proper 404s/403s. |
| Effort | M |

---

## 6. Concurrency and Safety

### Finding 15: PerTenantConcurrencyLimiter Lock Contention

| Field | Description |
|-------|-------------|
| Category | Quality |
| Severity | **Medium** |
| Title | PerTenantConcurrencyLimiter uses global mutex on every request |
| Location | `internal/middleware/middleware.go:222-249` |
| Description | `PerTenantConcurrencyLimiter` acquires `pt.mu.Lock()` on every request and release, creating a global contention point under high concurrency. The map-based inflight tracking with `map[string]int` is not sharded. |
| Current State | All tenants share a single mutex for the per-tenant tracking map. Under high concurrency (1000+ RPS), this could become a bottleneck. |
| Recommended State | Use `sync.Map` or a sharded map (e.g., by tenant hash) to reduce lock contention. Alternatively, use an atomic-based approach with per-tenant semaphore channels. |
| Impact | Under high concurrency, the mutex becomes a Hot Spot that limits throughput. |
| Effort | M |

### Finding 16: SSE Rewrap Starts Without Readiness Check

| Field | Description |
|-------|-------------|
| Category | Error Handling |
| Severity | **Low** |
| Title | SSE key rewrap runs before repository is available |
| Location | `cmd/server/main.go:maybeRewrapSSE` |
| Description | `maybeRewrapSSE` is called between `buildStorage` and `repository.Open()`. If SSE keys are stored in the DB (e.g., KMS configuration references DB-based secrets), the rewrap runs before the DB is migrated. |
| Current State | The goroutine launched by `maybeRewrapSSE` runs concurrently with repo setup. It could fail if KMS config depends on DB state. |
| Recommended State | Move `maybeRewrapSSE` after the repository migration completes, or ensure KMS config doesn't depend on DB. |
| Impact | Under specific KMS configurations, SSE key rewrap silently fails on startup. |
| Effort | S |

---

## Code Quality Metrics Summary

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| File size compliance (prod) | 4/4 files violate 500-line limit | < 500 lines | ❌ |
| Function length compliance | ~30 functions > 50 lines | < 50 lines | ❌ |
| Overall test coverage | ~62.5% | > 80% | ⚠️ |
| Repository coverage | 54.6% | > 80% | ❌ |
| cmd/server coverage | 0% | > 50% | ❌ |
| webui coverage | 0% | > 50% | ❌ |
| AI package coverage | 84.2% | > 80% | ✅ |
| Authentication coverage | 77.9% | > 80% | ⚠️ |
| CLI coverage | 82.5% | > 80% | ✅ |
| Race detection in CI | Not present | Present | ❌ |
| gofmt compliance | Passes | Pass | ✅ |
| go vet compliance | Passes | Pass | ✅ |
| `go build` compliance | Passes | Pass | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| File size violations (4 files) | High | L | **P0** | Violates team engineering constraints; blocks CI |
| Function length violations (30+) | High | L | **P0** | Violates team constraints; reduces testability |
| Middleware chain unmet invariant (I4) | High | S | **P0** | Panic safety risk; documented invariant violated |
| CLI bugs (6 unfixed) | High | M | **P1** | CLI reports success on failure; breaks automation |
| Background context in indexer | Medium | S | **P1** | Lost telemetry context |
| SQL dialect duplication | Medium | L | **P1** | Maintenance burden for schema changes |
| main.go 0% coverage | High | L | **P1** | Wiring bugs undetectable in CI |
| Repository coverage < 55% | High | L | **P2** | SQL regressions possible |
| God function `config.Load()` (198 lines) | Medium | M | **P2** | Maintainability issue |
| Global mutable DefaultStorageClass | Medium | M | **P2** | Test pollution risk |
| Missing `-race` in CI | Medium | S | **P2** | Latent races undetected |
| Inconsistent error wrapping | Medium | M | **P2** | Error classification fragility |
| Dead code in pii.go | Low | S | **P3** | Minor cleanup |
| PerTenantMutex contention | Low | M | **P3** | Performance under extreme load |
| SSE rewrap startup ordering | Low | S | **P3** | Edge case startup failure |

---

## Final Summary

| Aspect | Assessment |
|--------|-----------|
| **Overall Code Quality** | **Needs Work** |
| **Critical Quality Issues** | 4 production files violate 500-line limit, ~30 functions exceed 50-line limit, middleware chain violates documented invariant I4, 6 known unfixed CLI bugs, `main.go` has 0% test coverage |
| **Maintainability Concerns** | `config.Load()` (198-line god function), SQL dialect duplication across 15+ queries, global mutable defaults, background context leakage in indexer and WebDAV |
| **Technical Debt** | Significant accumulated debt primarily from file/function size violations, missing coverage on critical assembly code, and documented but unfixed bugs. The project's own engineering constraints (AGENTS.md) are systematically violated. |
| **Quick Wins** | Remove dead `strings.Repeat("0",0)` in pii.go (5 min). Add `-race` to CI test command (10 min). Fix `context.Background()` in indexer telemetry calls (15 min). Add OpenAPI docs for missing admin endpoints (2-4 hours). Reorder middleware to match documented I4 or update documentation (30 min). |

### What the Project Does Well

Despite the issues above, the project has notable strengths:
- **Strong interface design**: `Storage` interface with contract tests is an excellent pattern. The `EventSink`, `ChunkCleaner`, `Embedder`, `LLM`, `Reranker`, `Extractor` interfaces all enable clean composability.
- **Well-defined layering**: Protocol adapters → FileService → Storage/Repository separation is clean and maintainable.
- **Opt-in safety defaults**: AI, pgvector, Qdrant, events, cluster features all gated behind flags with nil-safety.
- **Good test infrastructure**: The storage contract tests, mock AI components, and temp-db fixtures make testing straightforward.
- **Comprehensive telemetry**: 15+ OTel instruments, Prometheus metrics, middleware timing.
- **Config-driven flexibility**: Multiple backends for storage, vector index, lexical search, LLM, reranker — all runtime-swappable.

**Recommendation**: Address P0 items (file/function size limits, middleware chain I4) immediately. Schedule P1 items (CLI bugs, context propagation, main.go coverage, SQL duplication) for the current sprint. Create a tech-debt tracking board for P2/P3 items. The codebase has excellent bones — the structural issues are straightforward to fix with disciplined refactoring.
