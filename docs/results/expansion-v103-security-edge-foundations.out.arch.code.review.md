Now I have a thorough understanding of the codebase. Let me compile the comprehensive review.

---

# Staff Engineer Review: aero-vault Code Quality Assessment

## Executive Summary

This is a well-architected Go service with clean package boundaries, good interface design, and solid separation of concerns. The codebase follows modern Go conventions (slog, structured errors, `errors.Is`/`errors.As`). However, there are significant file-size and function-length violations against the project's own `AGENTS.md` constraints, along with coverage gaps and a few production-context concerns.

---

## Findings

### Category: Organization | Severity: **Critical** | File Size Violations

**Title:** `main.go` exceeds 500-line limit (861 lines)

| Field | Description |
|-------|-------------|
| **Location** | `cmd/server/main.go` (861 lines) |
| **Description** | The project's `AGENTS.md` mandates max 500 lines per file. `main.go` contains 25+ functions including a massive `buildStorageFrom` (64 lines), `configureAuthSecrets` (48 lines), `buildBackgroundWorkers` (43 lines), plus all adapter/wiring functions. |
| **Current State** | 861 lines, single file |
| **Recommended State** | Split into `< 500 line` files. Suggested splits: `main.go` (run + main lifecycle), `wiring.go` (buildStorage, buildAI*, build* functions), `infra.go` (initInfrastructure, background workers), `keys.go` (apiKeyStore adapter). |
| **Impact** | Maintainability violation; onboarding friction; violates project constraints |
| **Effort** | M |

---

### Category: Organization | Severity: **Critical** | File Size Violations

**Title:** REST and S3 handler files exceed 500-line limit (958/890 lines)

| Field | Description |
|-------|-------------|
| **Location** | `internal/api/rest/handler.go` (958 lines), `internal/api/s3compat/handler.go` (890 lines) |
| **Description** | These handler files are nearly 1000 lines each. They bundle every HTTP handler method for the entire REST and S3 API surface into one monolithic struct. |
| **Current State** | `Handler` struct with 30+ methods in a single file |
| **Recommended State** | Split by domain: `handler_files.go` (get/put/delete), `handler_buckets.go`, `handler_multipart.go`, `handler_batch.go`, `handler_versions.go`. Or per-file groups of related methods. |
| **Impact** | Violates 500-line constraint; hard to navigate; merge conflict magnet |
| **Effort** | L |

---

### Category: Organization | Severity: **Critical** | Function Length Violations

**Title:** Multiple functions exceed 50-line limit (project constraint)

| Field | Description |
|-------|-------------|
| **Location** | Various files (see below) |
| **Description** | `AGENTS.md` mandates ≤ 50 lines per function. Violations found: |
| **Current State** | `compileSingleCondition` (215 lines, `internal/auth/condition.go`), `Load` (198 lines, `internal/config/config.go`), `NewRouter` (136 lines, `internal/api/rest/router.go`), `toObjectDTO` (101 lines, `internal/api/rest/handler.go`), `writeS3Error` (83 lines, `internal/api/s3compat/handler.go`), `unescape` (248 lines, `internal/repository/sql_helpers.go`), `run` (78 lines, `cmd/server/main.go`), `cannedFromFlags` (119 lines), `parseLogLevel` (110 lines) |
| **Recommended State** | Extract helper functions, decompose condition compilation pipeline, split router registration into logical groups |
| **Impact** | High cyclomatic complexity; violates project constraint; hard to test individual paths |
| **Effort** | L |

---

### Category: Organization | Severity: **Critical** | God Types

**Title:** `auth/condition.go` (657 lines) exceeds 300-line God type threshold

| Field | Description |
|-------|-------------|
| **Location** | `internal/auth/condition.go` (657 lines) |
| **Description** | `AGENTS.md` forbids God types over 300 lines. At 657 lines, this file is a single-package module containing condition parsing, evaluation, IP matching, string/numeric/date/ARN comparators, and all condition operators. |
| **Current State** | 657 lines, 20+ exported functions, 25+ internal functions |
| **Recommended State** | Split into `condition.go` (core types + ParseCondition), `condition_string.go`, `condition_ip.go`, `condition_arn.go`, `condition_numeric.go`. The package doc is excellent, but the file is too large. |
| **Impact** | Violates project constraint; maintenance burden as IAM policy grows |
| **Effort** | M |

---

### Category: Error Handling | Severity: **High** | Context Leakage

**Title:** `context.Background()` used in production code paths

| Field | Description |
|-------|-------------|
| **Location** | `internal/ai/indexer.go:313,316`, `internal/events/bus.go:139`, `internal/api/webdav/dav.go:302,381` |
| **Description** | Several production code paths call `context.Background()` instead of propagating the request/operation context. In `indexer.go`, telemetry counters `IncIndexerSkip` use `context.Background()`, losing tracing parent spans and cancelation. In `bus.go:139`, `IncEventDropped` uses a detached context. In `webdav/dav.go`, fallback contexts lose tenant identity. |
| **Current State** | `telemetry.IncIndexerSkip(context.Background(), "unsupported")` — no parent context |
| **Recommended State** | Thread the context from the caller or use `context.TODO()` as a signal for future plumbing. At minimum, use `context.TODO()` to flag the debt. |
| **Code Example (Before)** | `telemetry.IncIndexerSkip(context.Background(), "unsupported")` |
| **Code Example (After)** | `telemetry.IncIndexerSkip(ctx, "unsupported")` — where `ctx` is the event processing context |
| **Impact** | Lost tracing, no cancelation propagation, potential goroutine leaks |
| **Effort** | S |

---

### Category: Error Handling | Severity: **Medium** | Silently Discarded Errors

**Title:** Error values discarded in close/remove cleanup paths

| Field | Description |
|-------|-------------|
| **Location** | `internal/storage/local_meta.go:33-38`, `internal/storage/local_write.go:26,39-40,100`, `internal/storage/local_multipart.go:47,108,122,137-138,183`, `internal/ai/pgvector.go:104` |
| **Description** | Many close/cleanup operations discard errors with `_ =`. While this is common in Go for deferred `Close()` calls, on cleanup paths in production storage code it can silently hide data integrity issues (failed fsync, incomplete temp file removal, DB connection leak). |
| **Current State** | `_ = tmp.Close()` — error silently ignored |
| **Recommended State** | Log the error when it occurs (e.g., `if err := tmp.Close(); err != nil { logger.Warn(...) }`). For critical paths (meta file writes), propagate errors. |
| **Impact** | Data corruption can go undetected; resource leaks during shutdown |
| **Effort** | M |

---

### Category: Error Handling | Severity: **Medium** | SQL Injection Risk via fmt.Sprintf

**Title:** pgvector and pgFTS use `fmt.Sprintf` with table/column names

| Field | Description |
|-------|-------------|
| **Location** | `internal/ai/pgvector.go:129`, `internal/ai/lexicalindex.go:90` |
| **Description** | Table and column names from `PgVectorOptions`/`PgFTSOptions` are injected into SQL via `fmt.Sprintf`. While these come from operator config (not user input), the code comments document this — but this is a fragile pattern. Any future code path that allows config injection would become an SQL injection vector. |
| **Current State** | ```go q := fmt.Sprintf(`SELECT ... FROM %[2]s WHERE ...`, p.opts.VectorColumn, p.opts.Table) ``` |
| **Recommended State** | Validate table/column names against a whitelist (alphanumeric + underscore regex), or use the database-safe identifier quoting. Document the restriction clearly. |
| **Impact** | Potential SQL injection if options source changes; violates defense-in-depth |
| **Effort** | S |

---

### Category: Testing | Severity: **High** | Coverage Below Targets

**Title:** Overall coverage at 64%, several key packages below 60%

| Field | Description |
|-------|-------------|
| **Location** | `internal/repository` (54.6%), `internal/storage` (57.3%), `internal/service` (58.0%), `internal/api/rest` (52.8%) |
| **Description** | `AGENTS.md` aspirational target is 80% coverage; only 9 of 22 packages meet that. The persistence layer, service layer, and REST API are all below 60%. The core CRUD paths in `service` package — the central controller — is only 58% covered when it mediates between all protocol adapters and storage. |
| **Current State** | 64% overall; key packages 52-58% |
| **Recommended State** | Add tests for: repository SQL edge cases (empty results, pagination boundaries, transactional rollbacks), service error paths (quota enforcement, lock violations, corrupted objects), handler middleware integration tests (auth rejection, rate limiting). |
| **Impact** | Regression risk for core data paths; refactoring without safety net |
| **Effort** | L |

---

### Category: Testing | Severity: **Medium** | Massive Test Files

**Title:** Several test files exceed 500 lines

| Field | Description |
|-------|-------------|
| **Location** | `internal/storage/storage_test.go` (1120), `internal/repository/chunks_events_buckets_test.go` (922), `internal/api/webdav/dav_test.go` (893), `internal/api/s3compat/handler_test.go` (847), `internal/auth/condition_test.go` (910), `internal/mcp/server_test.go` (761), `internal/ai/integration_test.go` (762), `internal/service/service_test.go` (644), `internal/api/rest/handlers_test.go` (629) |
| **Description** | Multiple test files exceed 500 lines. While file-size constraints apply primarily to production code (per AGENTS.md), large test files are equally hard to navigate and maintain. |
| **Current State** | Monolithic test files with hundreds-of-lines test functions |
| **Recommended State** | Split tests by domain (e.g., `storage_test.go` → `storage_put_test.go`, `storage_get_test.go`, `storage_multipart_test.go`) and use shared test fixtures. Extract reusable helpers into `storage_testutil.go`. |
| **Impact** | Maintainability; slow test runs; hard to find specific tests |
| **Effort** | M |

---

### Category: Testing | Severity: **Medium** | Missing `cmd/server` and `webui` tests

| Field | Description |
|-------|-------------|
| **Location** | `cmd/server` (0% coverage), `internal/webui` (0% coverage) |
| **Description** | The main server wiring (861 lines of critical assembly logic) has zero test coverage. The `make check` gate only runs unit tests (which excludes `cmd/server` due to no test files). The WebUI package similarly has no tests. |
| **Current State** | `cmd/server` — 0%, `internal/webui` — 0% |
| **Recommended State** | Add integration-style tests for `cmd/server` that exercise the assembly (`buildRouter`, `applyMiddleware`, `buildStorageFrom`) with mocked dependencies. Even basic structural tests for the middleware chain order would validate I4. |
| **Impact** | Wiring regressions (wrong middleware order, missing dependency injection) only caught in manual/QA testing |
| **Effort** | M |

---

### Category: Naming | Severity: **Low** | Inconsistent Receiver Name Patterns

| Field | Description |
|-------|-------------|
| **Location** | `internal/service/file.go`, `internal/storage/local.go`, `internal/events/bus.go`, and others |
| **Description** | Some packages use single-letter receivers (`s *FileService`, `r *Registry`), others use abbreviated names (`ls *LocalStorage`, `pt *PerTenantConcurrencyLimiter`). Go convention prefers short (1-2 char) but *consistent* receivers. The `mcp` package uses `s *Server` which conflicts with `service` conventions. |
| **Current State** | Mixed: `s`, `r`, `ls`, `pt`, `h`, `b`, `ix`, `p`, `e` |
| **Recommended State** | Establish a project-level receiver naming convention. Rest is minor. |
| **Impact** | Low; minor cognitive overhead when switching between packages |
| **Effort** | S |

---

### Category: Logging | Severity: **Medium** | Mixed Logging Styles

| Field | Description |
|-------|-------------|
| **Location** | `cmd/server/main.go` (uses both `fmt.Fprintf` and `slog`), `internal/snapshot/snapshot.go` (uses `fmt.Errorf`), `internal/auth/auth.go` |
| **Description** | While the project primarily uses `slog` for structured logging, bootstrap paths in `main.go` use `fmt.Fprintf(os.Stderr, ...)` for errors and `slog` for info messages. Snapshot utility uses `fmt.Errorf` for errors without structured fields. |
| **Current State** | `fmt.Fprintf(os.Stderr, "fatal: %v\n", err)` — unstructured |
| **Recommended State** | Replace bootstrap `fmt.Fprintf` with `slog.Error` where a logger is available. For pre-logger errors, consider a helper like `logFatal(logger, msg, err)` that writes structured + stderr. |
| **Impact** | Inconsistent log parsing; bootstrap errors not captured by log aggregators |
| **Effort** | S |

---

### Category: Technical Debt | Severity: **High** | Missing CI Gate for File Constraints

| Field | Description |
|-------|-------------|
| **Location** | Project-level (`AGENTS.md` rules violated but not enforced) |
| **Description** | `AGENTS.md` defines hard file-size (≤500), function-length (≤50), cyclomatic (≤10), and God-type (≤300) constraints with stated CI rejection consequences. However, no automated enforcement exists — `make check` only runs `gofmt`, `go build`, `go vet`, `go test`. These constraints have been violated across the codebase with no CI signal. |
| **Current State** | Constraints documented but unenforced; violations exist throughout |
| **Recommended State** | Add `golangci-lint` or custom checks to `make check`: `lll` (line length), `funlen` (function length), `cyclop` (cyclomatic), `godot` / `gocritic`. Integrate these into CI as non-blocking warnings first, then blocking over 2 sprints. |
| **Impact** | Enforcement gap means constraints are aspirational, not contractual; tech debt grows unchecked |
| **Effort** | S |

---

### Category: Technical Debt | Severity: **Medium** | pgvector SQL Injection Surface

| Field | Description |
|-------|-------------|
| **Location** | `internal/ai/pgvector.go:129` |
| **Description** | Table/column names injected via `fmt.Sprintf` into SQL. While documented as "operator config, not user input," this is a fragile pattern. |
| **Current State** | See Error Handling finding above |
| **Recommended State** | Add identifier validation with regex `^[a-zA-Z_][a-zA-Z0-9_]*$` before `Sprintf`. |
| **Effort** | S |

---

### Category: Quality | Severity: **High** | `auth/condition.go` Cyclomatic Complexity

| Field | Description |
|-------|-------------|
| **Location** | `internal/auth/condition.go`: `compileSingleCondition` (215 lines) |
| **Description** | The condition compilation function is a 215-line single function with deeply nested switch cases, multiple closure returns, and inline string parsing. This far exceeds the ≤10 cyclomatic complexity target. |
| **Current State** | One large function with multiple condition operator branches |
| **Recommended State** | Extract each condition operator into its own function/type (`ipAddressCondition`, `stringEqualsCondition`, `numericEqualsCondition`, `boolCondition`, `arnCondition`, `dateCondition`). Use an operator registry map. |
| **Impact** | Difficult to test, difficult to add new condition operators, high bug surface |
| **Effort** | M |

---

### Category: Quality | Severity: **Medium** | Magic Numbers and Hardcoded Constants

| Field | Description |
|-------|-------------|
| **Location** | `internal/ai/embedder.go:66` (`const k = 5` shingle size), `internal/ai/search.go:104` (`rrfK = 60.0`), `internal/api/rest/handler.go` (various), `internal/mcp/server.go` (`4<<20` = 4MB limit) |
| **Description** | Several magic numbers and magic strings are embedded in function bodies without named constants. While some are documented in comments, extracted constants would improve readability and configurability. |
| **Current State** | `const k = 5` in `embedder.go`, `const rrfK = 60.0` in `search.go`, `io.LimitReader(rc, 4<<20)` in `mcp/server.go` |
| **Recommended State** | Extract to package-level typed constants or config fields where appropriate (e.g., `MaxReadLimit = 4 << 20`; `ShingleSize = 5`; `DefaultRRF_K = 60.0`). |
| **Impact** | Low; minor readability concern |
| **Effort** | S |

---

### Category: Quality | Severity: **Low** | Redundant `defaultTenant`/`DefaultTenant` Pattern

| Field | Description |
|-------|-------------|
| **Location** | `internal/service/file.go:17` (`DefaultTenant = "default"`), `internal/middleware/middleware.go:46` (`t = "default"`), `internal/mcp/server.go:39` (`tenant = "default"`) |
| **Description** | The string `"default"` default tenant is repeated in three separate packages. If the default tenant name ever changes, all three must be updated in coordination. |
| **Current State** | Three separate `"default"` defaults |
| **Recommended State** | Export the constant from a single package (likely `service` since it defines `DefaultTenant`) and reference it from middleware and mcp. |
| **Impact** | Low; coordination risk if default changes |
| **Effort** | S |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Test Coverage (overall) | 64.0% | ≥80% | ⚠️ (below target) |
| Test Coverage (repository) | 54.6% | ≥80% | ❌ |
| Test Coverage (storage) | 57.3% | ≥80% | ❌ |
| Test Coverage (service) | 58.0% | ≥80% | ❌ |
| Test Coverage (rest) | 52.8% | ≥80% | ❌ |
| Test Coverage (cmd/server) | 0.0% | ≥80% | ❌ |
| File size ≤ 500 lines | 44 violations | 0 | ❌ |
| Function length ≤ 50 lines | 30+ violations | 0 | ❌ |
| Cyclomatic complexity ≤ 10 | Not measured formally | ≤10 | ⚠️ (likely violated) |
| God types (≤300 lines) | `condition.go` 657 lines | 0 | ❌ |
| Code duplication | Low (good) | <5% | ✅ |
| Documentation coverage | Good (godoc everywhere) | >70% | ✅ |
| TODO/FIXME comments | 0 | 0 | ✅ |
| `gofmt` compliance | Clean | Clean | ✅ |
| `go vet` compliance | Clean | Clean | ✅ |
| `go build` | Clean | Clean | ✅ |
| All tests passing | ✅ | ✅ | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| main.go (861 lines) needs splitting | High | M | P0 | Violates project constraint; blocks onboarding |
| handler.go (958 lines) / s3compat/handler.go (890 lines) need splitting | High | L | P0 | Monolithic handlers; merge conflict magnet |
| condition.go (657 lines) exceeds God type limit | Medium | M | P1 | Need to split before adding IAM condition operators |
| context.Background() in production paths | High | S | P0 | Lost tracing and cancelation |
| Coverage gap in cmd/server (0%) | Medium | M | P1 | Wiring errors escape detection |
| Coverage < 60% in repository/storage/service/rest | High | L | P1 | Regression risk on core data paths |
| SQL injection surface in pgvector/pgFTS | Medium | S | P1 | Defense-in-depth violation |
| Magic numbers (shingle size, RRF_K, read limit) | Low | S | P2 | Minor readability debt |
| No automated constraint enforcement in CI | Medium | S | P1 | Rules documented but unenforced |
| Mixed logging styles (fmt.Fprintf + slog) | Low | S | P2 | Bootstrap errors not in structured logs |
| Duplicate "default" tenant constants | Low | S | P3 | Coordination risk if changed |

---

## Final Summary

### Overall Code Quality: **Needs Work**

The codebase is **architecturally sound** with clean package layering, well-defined interfaces, excellent godoc coverage, and good use of modern Go idioms (slog, errors.Is/As, structured error wrapping, context propagation patterns). The DAG architecture from `AGENTS.md` is faithfully reflected in the implementation.

However, the codebase **systematically violates its own engineering constraints** from `AGENTS.md`:

- **`AGENTS.md` §0: File size ≤ 500 lines** — 44+ files exceed this limit
- **`AGENTS.md` §0: Function length ≤ 50 lines** — 30+ functions exceed this limit  
- **`AGENTS.md` §0: God types ≤ 300 lines** — `condition.go` (657 lines) violates this
- **`AGENTS.md` §0: Test coverage ≥ 80%** — Only 9 of 22 packages meet this; overall is 64%

The CI gate (`make check`) does not enforce these constraints, making them aspirational rather than contractual.

### Critical Quality Issues (Must Fix Before Production)

1. **File-size violations**: main.go, handler.go, s3compat/handler.go, condition.go — all violate project constraints by wide margins
2. **Function-length violations**: `compileSingleCondition` (215 lines), `NewRouter` (136 lines), `Load` (198 lines), `unescape` (248 lines) — far above the 50-line limit
3. **`context.Background()` in production code paths** — indexer.go, bus.go, webdav/dav.go all use detached contexts, losing tracing and cancelation
4. **Zero test coverage on `cmd/server`** — 861 lines of critical wiring logic untested

### Maintainability Concerns

- The REST handler (`internal/api/rest/handler.go`) and S3 handler (`internal/api/s3compat/handler.go`) at ~900 lines each are the primary risk: any API change touches these monoliths
- `auth/condition.go` at 657 lines is a God module that will grow as IAM conditions expand
- Repository interface at ~50 methods is very large; it's the central persistence contract and every change touches all implementations (SQLite + Postgres + mocks)

### Technical Debt Summary

| Priority | Count | Key Items |
|----------|-------|-----------|
| P0 (immediate) | 4 | File/function constraint violations, context leakage |
| P1 (this sprint) | 4 | CI enforcement, SQL injection surface, coverage gaps |
| P2 (next sprint) | 3 | Magic numbers, mixed logging, duplicate constants |

### Quick Wins (Easy Improvements)

| # | Change | Effort | Impact |
|---|--------|--------|--------|
| 1 | Replace `context.Background()` calls with `context.TODO()` or proper parent context | S | Fixes tracing leak |
| 2 | Validate pgvector/pgFTS table names with regex before Sprintf | S | Closes SQL injection surface |
| 3 | Centralize `DefaultTenant` constant in one package | S | Removes duplicated magic string |
| 4 | Extract `rrfK`, `shingleSize`, `maxReadLimit` as named constants | S | Improves readability |
| 5 | Add `golangci-lint` with `funlen`/`lll`/`cyclop` to `make check` (non-blocking) | S | Enables visibility into constraint violations |
| 6 | Split `cmd/server/main.go` into `main.go` + `wiring.go` + `infra.go` | M | Fixes largest file-size violation |

### Strengths to Preserve

- **Excellent documentation**: Every public type, function, and package has clear godoc
- **Clean architecture**: True to the DAG in AGENTS.md; no circular dependencies
- **Good error handling patterns**: Error wrapping with `%w`, sentinel errors, `errors.Is/As` throughout
- **Solid testing foundation**: Table-driven tests, standard library `testing`, good mock patterns
- **No TODO/FIXME/HACK comments**: Zero technical debt comments — realistic code that doesn't cut corners
- **SQL placeholder safety**: Correct use of `$N` + `rebind` throughout repository layer (complies with I1)
- **Migration discipline**: Dual SQLite/Postgres migration files (complies with I2)
- **Opt-in safe defaults**: AI, pgvector, Qdrant, events, cluster — all flag-gated (complies with I5)
