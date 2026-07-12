I now have a thorough understanding of the entire codebase. Let me compile the comprehensive Staff Engineer review.

---

# Code Quality Review: Aero-Vault

## Executive Summary

**Overall Code Quality:** Good — **approaching Excellent** with targeted remediation

This is a well-engineered Go project with a clean hexagonal architecture, strong separation of concerns, proper testing patterns, and good documentation. The codebase shows careful attention to invariants, safety defaults, and idiomatic Go. There are no systemic architectural problems. However, several quality concerns in file sizing, test coverage gaps, dead code, SQL dialect duplication, and a few code quality lapses need attention.

---

## Findings

### Finding 1 — Large Files Violation (monolithic handlers)

| Field | Description |
|-------|-------------|
| **Category** | Organization |
| **Severity** | **High** |
| **Title** | Multiple source files exceed 500-line limit |
| **Location** | `internal/api/rest/handler.go` (958 lines), `cmd/server/main.go` (861 lines), `internal/api/s3compat/handler.go` (890 lines), `internal/api/s3compat/xml.go` (438 lines - approaching), `internal/repository/sql_objects.go` (434 lines - approaching) |
| **Description** | Per AGENTS.md §0, single file ≤ 500 lines is a hard constraint. The REST handler (958 lines), main.go (861 lines), and S3compat handler (890 lines) exceed this by a wide margin. The make target `complexity-lines` enforces this for non-test files but only checks *after* the violation exists. |
| **Current State** | `handler.go` contains all REST route handlers (Put, Get, Head, Delete, List, Presign, Multipart, Bucket ops, Batch ops, Folder ops, dto conversion, etc.) in a single file |
| **Recommended State** | Split into cohesive sub-files: `handler_crud.go` (Put/Get/Head/Delete), `handler_bucket.go`, `handler_multipart.go`, `handler_special.go` (Presign/Thumbnail/Batch/Folder) |
| **Code Example** | **Current:** `internal/api/rest/handler.go` — 958 lines, mixed responsibilities. **Recommended:** Split into 4+ files ≤ 500 lines each. |
| **Impact** | Maintainability: new developers must scroll through a 958-line file to find relevant handlers. Risk of merge conflicts. |
| **Effort** | M |

---

### Finding 2 — Unused Variable in Search (dead code)

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | **Medium** |
| **Title** | Unused `ch` variable in BM25 search path |
| **Location** | `internal/ai/search.go`, line 192–201 |
| **Description** | The `searchLexical` method fetches an object via `s.repo.GetObjectByID(ctx, h.Doc.objectID)` but only assigns the result to `ch`, then uses `_ = ch` to suppress the "unused" compiler error. The retrieved Object is never used — this is dead code that suggests either an incomplete feature or leftover debugging code. |
| **Current State** | ```go ch, _ := s.repo.GetObjectByID(ctx, h.Doc.objectID) bm25Hits = append(bm25Hits, ranked{...}) _ = ch ``` |
| **Recommended State** | Remove the dead call entirely, or implement the intended use (e.g., populating additional fields from the object) |
| **Impact** | Confusing to future readers; indicates incomplete functionality; wastes a DB call on every BM25 search |
| **Effort** | S |

---

### Finding 3 — SQL Dialect Duplication (maintainability risk)

| Field | Description |
|-------|-------------|
| **Category** | Quality / Technical Debt |
| **Severity** | **High** |
| **Title** | Repeated switch/case blocks for SQLite vs Postgres in every repository method |
| **Location** | `internal/repository/sql_objects.go` (entire file), also `sql_chunks.go`, `sql_events.go`, etc. |
| **Description** | Every data access method duplicates SQL for both dialects using a `switch s.dialect` block. This creates a 2× explosion of SQL strings, makes adding a new dialect extremely error-prone, and results in dialect-specific logic scattered across 10+ files. The `rebind` function already handles placeholder conversion ($N → ?) but methods still duplicate entire queries. |
| **Current State** | Each method has an `if dialectPostgres { ... } else { ... }` block duplicating the entire SQL query with minor differences (jsonb casting, now() formatting, RETURNING syntax differences) |
| **Recommended State** | Extract dialect-specific SQL into generated or templated queries. Create a query builder that handles the 3 actual differences: (1) JSON type casting (::jsonb vs text), (2) NOW() expression (now() vs datetime()), (3) RETURNING syntax (RETURNING vs `SELECT last_insert_rowid()`). |
| **Code Example** | **Current** (sql_objects.go L60-L85): Two complete SQL statements for upsert. **Recommended:** A query template with placeholders for {{jsonCast}}, {{now}}, {{returning}}. |
| **Impact** | Adding a single column requires updating 2+ SQL statements. Adding a new DB dialect (e.g., MySQL or DuckDB) would require touching every file. High maintenance burden. |
| **Effort** | L |

---

### Finding 4 — Test Coverage Gap in Core Packages

| Field | Description |
|-------|-------------|
| **Category** | Testing |
| **Severity** | **High** |
| **Title** | Repository (54.6%), Service (58.0%), and Storage (57.3%) below 60% coverage |
| **Location** | `internal/repository/`, `internal/service/`, `internal/storage/` |
| **Description** | The three most critical packages — data access, business logic, and storage abstraction — fall below 60% coverage. The repository package at 54.6% is particularly concerning: it contains the database interaction layer where bugs can cause silent data corruption. |
| **Current State** | `repository`: 54.6%, `service`: 58.0%, `storage`: 57.3% |
| **Recommended State** | Raise to ≥80% coverage per AGENTS.md §0 target. Focus on: repository error paths (duplicate key, not-found, transaction rollbacks), service edge cases (quota enforcement, lock checks, multipart abort races), storage backend contract tests for all backends. |
| **Impact** | Regression risk: subtle bugs in quota accounting, versioning, or SQL interaction may go undetected. |
| **Effort** | L |

---

### Finding 5 — Hardcoded HTTP Status Codes

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | **Low** |
| **Title** | HTTP status codes written as bare numbers instead of `net/http` constants |
| **Location** | `internal/api/rest/handler.go`, `internal/api/rest/acl.go`, `internal/api/rest/management.go`, and throughout the REST package |
| **Description** | The codebase pervasively uses bare integer literals for HTTP status codes (`http.StatusOK`, `http.StatusCreated` are correct in some places, but many use raw `200`, `201`, `204`, `400`, `403`, `404`, `409`, `500`) |
| **Current State** | `w.WriteHeader(200)`, `writeJSON(w, http.StatusCreated, ...)` — mixed usage |
| **Recommended State** | Consistently use `net/http` constants: `http.StatusOK`, `http.StatusCreated`, `http.StatusNoContent`, etc. |
| **Code Example** | `w.WriteHeader(http.StatusNoContent)` instead of `w.WriteHeader(204)` |
| **Impact** | Low — functional correctness is unaffected. Readability and reviewer efficiency. |
| **Effort** | S |

---

### Finding 6 — Middleware Chain Bypass in Main.go

| Field | Description |
|-------|-------------|
| **Category** | Error Handling / Security |
| **Severity** | **Medium** |
| **Title** | WebDAV and MCP handlers bypass standard middleware chain |
| **Location** | `cmd/server/main.go` (around lines 400-500) |
| **Description** | Per AGENTS.md §2.5, the middleware chain must be `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`. The `NewRouter` (REST) properly applies Auth via `r.Use(mw.Auth)`, but the WebDAV and MCP handlers are mounted on separate paths and may not pass through the full middleware stack. WebDAV has "dispatches outside chi route registration" per AGENTS.md. |
| **Current State** | WebDAV handler mounted as a separate path on the main chi router; MCP has its own transport handler. Auth middleware may not apply uniformly. |
| **Recommended State** | Audit the main.go router assembly to verify all protocol adapters pass through the full middleware chain. Add integration tests that verify auth headers are enforced on WebDAV and MCP endpoints. |
| **Impact** | Security: authentication bypass on WebDAV/MCP endpoints could allow unauthenticated access to the storage layer. |
| **Effort** | M |

---

### Finding 7 — Event Bus Absence of Context Cancellation Handling

| Field | Description |
|-------|-------------|
| **Category** | Error Handling |
| **Severity** | **Medium** |
| **Title** | Event broadcast ignores context cancellation on subscriber channel sends |
| **Location** | `internal/events/bus.go`, lines 113-120 |
| **Description** | The `broadcast` method tries to send to subscriber channels with a `select/default` pattern that drops events when a subscriber buffer is full. However, it never checks `ctx.Done()`, so a canceled context will not interrupt the broadcast loop. Additionally, the `Publish` method doesn't surface the event ID back for tracking on failure. |
| **Current State** | Events are broadcast synchronously to all subscribers in a single goroutine. A slow subscriber triggers drops for all subscribers. No context awareness. |
| **Recommended State** | Consider fan-out goroutines per subscriber with context-aware select, or document the trade-off clearly. The current design is acceptable for an MVP but could cause cascading latency under load. |
| **Impact** | Under load, one slow subscriber causes all subscribers to miss events (drops), including the event persistence step (already done) and the webhook notifier. |
| **Effort** | M |

---

### Finding 8 — Missing gofmt Compliance (CI breakage)

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | **High** |
| **Title** | `gofmt` compliance failure detected during review |
| **Location** | `internal/auth/arn_test.go`, `internal/auth/condition.go` |
| **Description** | The `make check` target (required before every commit per AGENTS.md and HARNESS.md) runs `gofmt -l .` and panics if any file needs formatting. During this review, `make check` reported two files failing gofmt. This means CI would reject the current state. |
| **Current State** | Two files need formatting |
| **Recommended State** | Run `gofmt -w internal/auth/arn_test.go internal/auth/condition.go` to fix, then ensure the pre-commit hook catches future drift |
| **Impact** | CI gate failure — any PR would be rejected. |
| **Effort** | S |

---

### Finding 9 — Missing `_test.go` Suffix for Package Tests

| Field | Description |
|-------|-------------|
| **Category** | Testing |
| **Severity** | **Low** |
| **Title** | `cmd/server` and `webui` packages have no tests |
| **Location** | `cmd/server/main.go`, `internal/webui/` |
| **Description** | The `cmd/server` package is flagged `[no test files]` by `go test`. While main packages are typically thin, the main.go assembly logic (861 lines!) has zero test coverage. The `internal/webui` package also has no test files. |
| **Current State** | No test files for the server assembly or web UI |
| **Recommended State** | At minimum, add an integration test that boots the server config, verifies routes, and tests the middleware chain. Consider extracting the assembly logic from `main.go` into testable components. |
| **Impact** | The entire server assembly — where all the components are wired together — has no automated verification. |
| **Effort** | M |

---

### Finding 10 — Redundant `defaultTenant` Calls in Repository Layer

| Field | Description |
|-------|-------------|
| **Category** | Quality / Technical Debt |
| **Severity** | **Low** |
| **Title** | Duplicate tenant defaulting in every repository method |
| **Location** | All `internal/repository/sql_*.go` files |
| **Description** | Every repository method calls `tenant = defaultTenant(tenant)` as its first line. The service layer already calls `defaults(tenant, bucket)` before passing to the repo. This is redundant — either the service layer should be the sole authority for defaults, or the repo layer should enforce them. Having both is defensive duplication. |
| **Current State** | Both service and repository layers default tenant/bucket independently |
| **Recommended State** | Remove `defaultTenant` calls from repository methods and let the service layer be the sole tenant/bucket normalizer. Or keep them as defense-in-depth and document accordingly. |
| **Impact** | Low — works correctly, but future refactoring risks inconsistency if only one layer is updated. |
| **Effort** | S |

---

### Finding 11 — Rate Limiter Semaphore Release Pattern Could Panic

| Field | Description |
|-------|-------------|
| **Category** | Error Handling |
| **Severity** | **Medium** |
| **Title** | ConcurrencyLimiter semaphore release not protected against double-receive on channel close |
| **Location** | `internal/middleware/middleware.go`, lines 97-134 |
| **Description** | The `PerTenantConcurrencyLimiter.Middleware` releases semaphore slots in a defer block using unbuffered channel receive (`<-pt.global.sem`). If the sem channel is ever closed (e.g., during a reset), this would panic receiving from a closed channel. Additionally, the per-tenant tracking map is protected by a mutex but the counter manipulation could be done more cleanly with `sync.Map` or `atomic` operations. |
| **Current State** | Channel receives in defer; mutex-protected map for per-tenant tracking |
| **Recommended State** | Document that the channel is never closed. Alternatively, use a counting semaphore pattern (`sync.WaitGroup` style) or rate-limit library. |
| **Impact** | Potential panic if the concurrency limiter lifecycle is not perfectly managed. Low probability. |
| **Effort** | S |

---

### Finding 12 — Ordering of `BucketStats` Query (potential correctness issue)

| Field | Description |
|-------|-------------|
| **Category** | Quality |
| **Severity** | **Low** |
| **Title** | `ListObjects` ordering uses string comparison on keys |
| **Location** | `internal/repository/sql_objects.go`, `ListObjects` method |
| **Description** | Pagination via `key > $4` uses string comparison on keys (`ORDER BY key ASC LIMIT $5`). This works for simple key names but can produce incorrect pagination with numeric-prefixed or locale-sensitive keys (e.g., "10" < "2" in string sort). |
| **Current State** | String-based pagination: `WHERE ... AND key > $4 ORDER BY key ASC` |
| **Recommended State** | Document the limitation in the ListObjects method. Consider a natural-sort ordering or integer-based cursor if key patterns include numeric prefixes. For most S3-compatible use cases this is fine. |
| **Impact** | Potentially confusing pagination for keys with numeric prefixes. Low severity. |
| **Effort** | S |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Not measured (gocyclo not installed) | < 10 | ⚠️ (no gating) |
| Function length | Most < 50 lines | < 50 lines | ✅ (generally good) |
| File length (non-test) | handler.go: 958, main.go: 861, s3compat/handler.go: 890 | < 500 | ❌ (Finding #1) |
| Test coverage (core) | repo: 54.6%, service: 58.0%, storage: 57.3% | > 80% | ❌ (Finding #4) |
| Test coverage (overall avg) | ~70% (estimated) | > 80% | ⚠️ |
| gofmt compliance | 2 files failing | 100% | ❌ (Finding #8) |
| go vet compliance | Clean | 0 warnings | ✅ |
| Build | Clean | Clean | ✅ |
| Code duplication (SQL dialect) | High — 2× per query | Low | ❌ (Finding #3) |
| Documentation coverage | Good for exports | > 70% | ✅ |
| Dead code | 1 instance (unused `ch`) | 0 | ❌ (Finding #2) |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| **File size violations** (handler.go, main.go, s3compat/handler.go) | High | M | **P0** | Violates AGENTS.md hard constraint. Split into sub-files. |
| **SQL dialect duplication** | High | L | **P0** | Every new column = 2+ SQL edits. Extract query templates. |
| **Test coverage < 60% in core** (repo, service, storage) | High | L | **P0** | Risk of regression in data-critical operations. |
| **Unused variable** in search.go (dead code) | Low | S | **P1** | Minor but confusing; remove the dead call. |
| **Middleware bypass** (WebDAV/MCP auth) | Medium | M | **P1** | Security audit needed for non-REST protocol adapters. |
| **gofmt drift** | Low | S | **P1** | Fix before next PR |
| **HTTP status code hardcoding** | Low | S | **P2** | Readability; switch to net/http constants |
| **Dual tenant defaulting** (service + repo) | Low | S | **P2** | Clean up redundant layer |
| **Event bus context awareness** | Medium | M | **P2** | Consider subscriber isolation |
| **No tests for cmd/server or webui** | Medium | M | **P2** | Integration test for assembly logic |
| **String-based pagination edge cases** | Low | S | **P3** | Document limitation |
| **ConcurrencyLimiter panic risk** | Low | S | **P3** | Document channel lifecycle |

---

## Final Summary

### Overall Code Quality: **Good** (with upward trajectory)

This is a well-designed Go project that demonstrates sound engineering practices: hexagonal architecture, clean package boundaries, proper use of interfaces, strong error sentinel patterns, structured logging via `slog`, and comprehensive migration management. The team clearly understands Go idioms and software design principles.

### Critical Quality Issues (must fix)

1. **File size violations**: Three production files exceed 500 lines (handler.go, main.go, s3compat/handler.go). Rename and split immediately — this is a hard constraint in the project's own rules.
2. **SQL dialect duplication**: 96 migration files (48 per dialect) and 10+ repository files with duplicated SQL. This is the single largest long-term maintenance liability. Adopt a query template approach.
3. **Test coverage gaps**: Repository (54.6%) and Service (58.0%) need focused effort to reach 80%. Prioritize error paths, edge cases, and contract tests.
4. **gofmt non-compliance**: Two files need formatting. The CI gate will reject any PR right now.

### Maintainability Concerns

- The dialect `switch/case` pattern in repository methods will become increasingly painful as new features are added. Consider extracting SQL templates with Go `text/template` or a lightweight query builder.
- `main.go` at 861 lines is doing too much assembly by hand. Consider a builder pattern or dependency injection framework.
- The event bus subscriber isolation model (one goroutine broadcasting to all) could become a bottleneck.

### Quick Wins (easy fixes)

1. Run `gofmt -w internal/auth/arn_test.go internal/auth/condition.go` — 30 seconds
2. Remove unused `_ = ch` and `ch, _ := s.repo.GetObjectByID(...)` from `internal/ai/search.go` — 5 minutes
3. Add `make install-tools` to CI pipeline and run `gocyclo` to track cyclomatic complexity — 15 minutes
4. Create a tracking issue for file size refactoring and begin splitting `handler.go` into sub-files — 2-3 hours

### Recommendations for Team Productivity

1. **Add `golangci-lint`** to the CI pipeline. It would catch the unused variable, gofmt issues, and many other quality problems automatically.
2. **Improve the `Makefile` complexity checks** — currently `complexity-lines` only warns on gocyclo > 10 without failing the build. Make it fail.
3. **Add a database migration smoke test** — a `go test -run TestMigration` that applies all migrations to both SQLite and Postgres, verifies the schema version count, and rolls back.
4. **Standardize on HTTP status constants** — use `net/http` constants throughout, not bare integers.
5. **Document the "why" behind middleware chain ordering** — the comment in AGENTS.md mentions it but the rationale for the specific order is not documented in code. A future change could silently reorder them.
