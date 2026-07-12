Now I have a thorough picture of the entire codebase. Here is my comprehensive Staff Engineer code review:

---

# Staff Engineer Code Review: `aero-vault`

## Executive Summary

**Overall Code Quality: Needs Work**

The project demonstrates strong architectural thinking (clean layering, DAG-based assembly, opt-in gating, contract testing) and sound fundamentals. However, it is currently burdened by significant structural violations of its own engineering constraints (20 files exceed the 500-line limit, the Repository interface is a 99-method God interface, `main.go` is a 861-line God function) and several quality anti-patterns. **The codebase needs substantial refactoring before it can be considered production-maintainable.**

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Single file ≤ 500 lines | **20 files violate** | ≤ 500 | ❌ |
| Function ≤ 50 lines | Not measured but likely violated in large files | ≤ 50 | ⚠️ |
| Cyclomatic complexity | Not measured (no `gocyclo` on CI) | < 10 | ⚠️ |
| Test coverage (average) | **~65%** (variance: 0%–100%) | > 80% | ⚠️ |
| Code duplication | Moderate (SQL dialect switches replicated) | < 5% | ⚠️ |
| `gofmt` compliance | **2 violations** (`arn_test.go`, `condition.go`) | 0 | ❌ |
| `go vet` | Clean ✅ | 0 | ✅ |
| `go build` | Clean ✅ | Success | ✅ |

---

## Findings

### Category: Organization | Severity: Critical

| Field | Value |
|-------|-------|
| **Title** | 20 Go files exceed the 500-line hard limit |
| **Location** | Multiple files (see shell output above) |
| **Description** | HARNESS.md enforces a hard 500-line limit per file, yet 20 `.go` files violate it. This is a **fundamental engineering contract violation**. Key offenders: `cmd/server/main.go` (861), `internal/api/rest/handler.go` (958), `internal/api/s3compat/handler.go` (890), `internal/auth/condition.go` (657), `internal/service/service_test.go` (644), `internal/storage/storage_test.go` (1120) |
| **Current State** | Large files contain dozens of methods/functions, making them untestable in isolation, hard to review, and prone to merge conflicts |
| **Recommended State** | Split each file by concern. `handler.go` → `handler_get.go`, `handler_put.go`, `handler_bucket.go`, etc. `main.go` → extract builder functions into `internal/server/` package |
| **Impact** | Reviewability, maintainability, onboarding severely degraded. Violates own quality gate |
| **Effort** | L |

---

### Category: Organization | Severity: Critical

| Field | Value |
|-------|-------|
| **Title** | Repository interface has 99 methods — God interface anti-pattern |
| **Location** | `internal/repository/repository.go` lines 81-179 |
| **Description** | The `Repository` interface defines **99 methods** covering objects, buckets, chunks, events, uploads, quota, API keys, leases, audit, idempotency, webhooks, and jobs. This violates Interface Segregation Principle and creates a monolithic dependency for every consumer. |
| **Current State** | A single `sqlStore` struct implements all 99 methods across 7 files |
| **Recommended State** | Split into domain-focused interfaces: `ObjectRepository`, `BucketRepository`, `ChunkRepository`, `EventRepository`, `AuthRepository`, `JobRepository`, `AuditRepository`. Combine via struct embedding or compose into a `DB` aggregator |
| **Impact** | Every mock for testing must stub 99 methods. Adding a new method touches the interface, mock, and all consumers. Tight coupling across unrelated domains. |
| **Effort** | L |

---

### Category: Organization | Severity: High

| Field | Value |
|-------|-------|
| **Title** | SQL dialect switch statements duplicated across every repository method |
| **Location** | All files under `internal/repository/sql_*.go` |
| **Description** | Every repository method contains a `switch s.dialect { case dialectPostgres: ... default: ...}` block. This pattern is repeated ~30+ times across the codebase. Adding a new dialect (e.g. MySQL) requires touching every method. |
| **Current State** | Inline switches in each method |
| **Recommended State** | Use a query builder pattern or a `dialectQueries` map. Or use `fmt.Sprintf` with dialect-specific templates. Or move to a `QueryBuilder` abstraction. At minimum, extract dialect-specific parts into helper functions. |
| **Code Example** | `sql_objects.go` — every method from `UpsertObject` to `HardDeleteObject` has a `switch s.dialect` |
| **Impact** | High duplication, error-prone when adding new dialect, violates DRY |
| **Effort** | M |

---

### Category: Organization | Severity: High

| Field | Value |
|-------|-------|
| **Title** | `cmd/server/main.go` is a God function (861 lines) |
| **Location** | `cmd/server/main.go` |
| **Description** | `main.go` contains 34+ top-level functions, all wiring the dependency graph. This should be in an `internal/server` package with focused builder files. The file violates the 500-line limit by 72%. |
| **Current State** | One massive file with all bootstrapping logic |
| **Recommended State** | Extract builders to `internal/server/bootstrap.go`, `internal/server/ai.go`, `internal/server/storage.go`, `internal/server/auth.go`, `internal/server/router.go` |
| **Impact** | New developers can't navigate bootstrapping; merge conflicts are guaranteed; testability is zero (0% coverage on `cmd/server`) |
| **Effort** | M |

---

### Category: Organization | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | `PerTenantConcurrencyLimiter` has a race condition on `inflight` access |
| **Location** | `internal/middleware/middleware.go` |
| **Description** | `PerTenantConcurrencyLimiter` uses `sync.Mutex` to protect `inflight map[string]int`, but the `global.sem` channel operations happen outside the lock. If a tenant's request passes the per-tenant check but the global sem acquire fails, the tenant count is already incremented but not decremented on the failure path. |
| **Current State** | Per-tenant count incremented before global sem acquire; on failure, global slots released but tenant count is not decremented |
| **Recommended State** | Restructure: acquire global first, THEN check+increment tenant count. Or use an atomic map with per-key mutexes. |
| **Code Example** | ```go
// Current: increments tenant count BEFORE global acquire
pt.inflight[tenant] += cost
// ... then global sem acquire might fail
// in failure path: releases global but NOT tenant
``` |
| **Impact** | Leaked tenant inflight count — eventually per-tenant limit becomes stuck, preventing that tenant from serving any requests |
| **Effort** | S |

---

### Category: Error Handling | Severity: High

| Field | Value |
|-------|-------|
| **Title** | Error swallowing in event bus and quota paths |
| **Location** | `internal/events/bus.go` (Publish), `internal/service/file_crud.go` (writePutObject) |
| **Description** | Several critical paths swallow errors: `Bus.Publish` logs but never propagates errors; `writePutObject` logs quota errors but continues; `Indexer.applyPII` ignores `repo.UpdateTags` errors. While some of this is by design (lifecycle events must not break user requests), the pattern is applied inconsistently and silently drops failures that operators need to know about. |
| **Current State** | Errors logged but not propagated |
| **Recommended State** | Add a telemetry counter for every error-swallow path. Operators need to observe how many quota updates failed, how many PII tag writes failed. Make the swallowing explicit with a comment like "non-fatal: best-effort" and couple with `IncSwallowedError` metric. |
| **Impact** | Silent data loss in quota tracking, event delivery gaps undetected |
| **Effort** | S |

---

### Category: Error Handling | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | Inconsistent error wrapping and sentinel error usage |
| **Location** | Throughout codebase |
| **Description** | Some errors use `fmt.Errorf("...: %w", err)` (proper wrapping), others use `fmt.Errorf("...: %v", err)` (string wrapping). Some use sentinel errors (`ErrNotFound`, `ErrLocked`) consistently, but some paths return raw `repository.ErrNotFound` while others wrap it in `service.ErrNotFound`. |
| **Current State** | Mixed: both `service.ErrNotFound` and `repository.ErrNotFound` exist as separate sentinels. Some service methods convert, some don't. |
| **Recommended State** | Eliminate `repository.ErrNotFound` — make it unexported. Service layer should define all public sentinels. Repository returns opaque errors; service translates. |
| **Impact** | Callers must check both `service.ErrNotFound` and `errors.Is(err, repository.ErrNotFound)`. Fragile and inconsistent. |
| **Effort** | M |

---

### Category: Logging | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | Missing structured context in several error log paths |
| **Location** | Various |
| **Description** | Several error log calls use `s.logger.Warn("message", "err", err)` without request ID, tenant, or key context. In async paths (indexer, workers), the context often carries no request ID, making correlation impossible. |
| **Current State** | Some log calls include `request_id`, `tenant`, `key` but many omit critical identifiers |
| **Recommended State** | Audit all `logger.Warn`/`logger.Error` calls to ensure they carry: (1) a correlation ID (request ID or job ID), (2) tenant, (3) affected resource key. Use a `logging.WithContext(ctx)` helper that extracts these automatically. |
| **Impact** | Debugging production issues is needlessly difficult without correlatable log context |
| **Effort** | M |

---

### Category: Testing | Severity: High

| Field | Value |
|-------|-------|
| **Title** | Several large test files exceed 500 lines |
| **Location** | `storage_test.go` (1120), `handler_test.go` (629), `dav_test.go` (893), `condition_test.go` (910), `service_test.go` (644), `cli_test.go` (1440), etc. |
| **Description** | Test files should follow the same maintainability standards as production code. Multi-thousand-line test files are unreadable, hard to debug, and encourage copy-paste testing. |
| **Current State** | Large test files with many test functions |
| **Recommended State** | Split by test group. Use Go subtables (`t.Run`) extensively. Extract test helpers into `testutil` packages. |
| **Impact** | Test maintenance cost high, new developers find it hard to add tests |
| **Effort** | L |

---

### Category: Testing | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | Tests use `time.Sleep` for async coordination |
| **Location** | `internal/events/bus_test.go:254`, `internal/jobs/jobs_test.go:51`, `internal/middleware/ratelimit_test.go:138`, etc. |
| **Description** | Several tests rely on `time.Sleep` for synchronization, making them flaky under CI load. |
| **Current State** | `time.Sleep(20ms)`, `time.Sleep(50ms)`, etc. |
| **Recommended State** | Use channels, `sync.WaitGroup`, or `assert.Eventually` patterns. For rate-limiter tests, use a deterministic ticker mock. |
| **Impact** | Flaky CI, wasted developer time re-running tests |
| **Effort** | M |

---

### Category: Testing | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | `cmd/server` and `internal/webui` packages have 0% test coverage |
| **Location** | `cmd/server/main.go`, `internal/webui/web.go` |
| **Description** | The main entry point and the web UI package have no tests. The CLI admin tests skip server tests entirely. |
| **Current State** | 0% coverage |
| **Recommended State** | Add integration test for server bootstrap (smoke test: config → start → healthz). WebUI can have a simple handler test. |
| **Impact** | Bootstrap regressions go undetected; web UI is untested |
| **Effort** | M |

---

### Category: Technical Debt | Severity: High

| Field | Value |
|-------|-------|
| **Title** | `apiKeyStore` adapter implemented as methods on `main.go` |
| **Location** | `cmd/server/main.go:822-859` |
| **Description** | The `apiKeyStore` type and its 5 methods are defined at the bottom of `main.go`, mixing persistence adapter logic with server assembly. This should live in `internal/auth/store.go` or similar. |
| **Current State** | Defined in `main.go` |
| **Recommended State** | Move to `internal/auth/store.go` alongside the `PersistentStore` interface |
| **Impact** | Poor discoverability, `main.go` even larger, violates separation of concerns |
| **Effort** | S |

---

### Category: Quality | Severity: Medium

| Field | Value |
|-------|-------|
| **Title** | `condition.go` is 657 lines — likely exceeds complexity limits |
| **Location** | `internal/auth/condition.go` |
| **Description** | The auth condition evaluation file is 657 lines with complex IAM policy condition parsing. Without complexity analysis, it's almost certain some functions exceed cyclomatic complexity of 10. |
| **Current State** | 657 lines, complex parsing logic |
| **Recommended State** | Split by condition type (string conditions, numeric, date, etc.). Extract a `ConditionEvaluator` per type. Add `gocyclo` to CI. |
| **Impact** | Logic errors in policy evaluation are hard to spot; untestable sub-paths |
| **Effort** | M |

---

### Category: Quality | Severity: Low

| Field | Value |
|-------|-------|
| **Title** | `gofmt` violations exist |
| **Location** | `internal/auth/arn_test.go`, `internal/auth/condition.go` |
| **Description** | HARNESS.md enforces `gofmt -l .` must output nothing, but 2 files fail formatting |
| **Current State** | Unformatted files |
| **Recommended State** | Run `gofmt -w` on these files |
| **Impact** | CI will reject commits; style inconsistency |
| **Effort** | S |

---

### Category: Organization | Severity: Low

| Field | Value |
|-------|-------|
| **Title** | Mix of global package-level vars and config injection |
| **Location** | `internal/service/file.go` — `DefaultStorageClass`, `DefaultBucket`, `DefaultTenant` |
| **Description** | `DefaultStorageClass` is a mutable global variable. The `WithDefaultStorageClass` function can be called from anywhere, making the default unpredictable during tests. |
| **Current State** | Global mutable state |
| **Recommended State** | Move to `FileService` config field; set at construction time |
| **Impact** | Test pollution; race condition if called concurrently |
| **Effort** | S |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| 20 files exceed 500-line limit | High | L | **P0** | Violates own HARNDESS.md contract. Must fix before any feature work |
| Repository 99-method God interface | High | L | **P0** | Fundamental design flaw |
| `main.go` 861-line God function | High | M | **P0** | Refactor into `internal/server/` |
| SQL dialect switches duplicated 30+ times | Medium | M | **P1** | DRY violation |
| `PerTenantConcurrencyLimiter` race condition | High | S | **P0** | Can lock out tenants |
| Inconsistent error wrapping | Medium | M | **P1** | Sentinels duplicated across layers |
| Errors swallowed silently | Medium | S | **P1** | OTel counters needed |
| Missing structured log context | Medium | M | **P1** | Correlation identifiers |
| Test files violate line limits | Medium | L | **P2** | Not urgent but degrading |
| `time.Sleep` in tests (flaky tests) | Medium | M | **P1** | CI reliability |
| `cmd/server` 0% coverage | Medium | M | **P2** | Bootstrap untested |
| `apiKeyStore` in main.go | Low | S | **P2** | Minor but poor practice |
| `condition.go` 657 lines likely complex | Medium | M | **P1** | Auth correctness |
| Global mutable `DefaultStorageClass` | Low | S | **P2** | Test pollution |
| `gofmt` violations | Low | S | **P1** | CI gate will fail |

---

## Quick Wins (S-effort fixes)

1. **Fix `gofmt` violations**: `gofmt -w internal/auth/arn_test.go internal/auth/condition.go`
2. **Fix `PerTenantConcurrencyLimiter` race**: reorder acquire global → check tenant
3. **Add OTel counters for error-swallow paths**: 3-4 lines each in `bus.go`, `file_crud.go`, `indexer.go`
4. **Move `DefaultStorageClass` to `FileService` config**: eliminate mutable global
5. **Move `apiKeyStore` to `internal/auth/`**: extract from `main.go`
6. **Add `gocyclo` to CI HARNESS.md**: the constraint exists on paper but isn't enforced

---

## Recommendations for the Team

1. **Stop feature work. Refactor first.** The AGENTS.md states "重构优先级高于功能开发" — this must be taken seriously. The 20 file-limit violations and God interface are existential maintainability threats.

2. **Adopt the Interface Segregation Principle for Repository.** Split into 5-6 focused interfaces. This alone will make testing dramatically easier.

3. **Add structural enforcement to CI.** HARNESS.md constraints (`golines`, `gocyclo`) must run in CI, not just be documented. Currently `gofmt` and `go vet` pass, but the structural checks are not enforced.

4. **Create an `internal/server/` package.** Extract all bootstrapping from `main.go` into focused files. This unlocks testability for the assembly logic.

5. **Add a linter configuration.** `golangci-lint` with `gocyclo`, `golines`, `dupl`, `errcheck` would catch 80% of the issues above automatically.

6. **Add `t.Helper()` to all test helpers.** Some test helpers already use it; audit and enforce consistently.

7. **Eliminate `time.Sleep` from tests systematically.** Add an `asyncassert` package with `Eventually` patterns.

---

## Closing Summary

**Overall Code Quality: Needs Work**

The architecture vision is solid — clean layering, opt-in gating, contract testing are signs of sophisticated engineering. However, the implementation has accumulated significant structural debt that violates the project's own quality gates. The single biggest issue is **20 files exceeding the 500-line limit**, which is a hard violation of HARNESS.md and AGENTS.md. The 99-method Repository God interface is the most impactful design flaw — it couples every subsystem to every data concern.

The good news: these are structural, not foundational, problems. The code compiles, passes vet, and has decent average coverage. A focused 2-3 day refactoring sprint targeting the top P0 items (file splits, interface segregation, main.go extraction) would dramatically improve maintainability. The quick wins alone (6 items, likely < 1 day total) would eliminate several CI-gate failures and runtime bugs.

**Priority order:**
1. P0 — Fix structural violations (file limits, God interface, race condition)
2. P1 — Fix error handling consistency, dialect duplication, test flakiness
3. P2 — Coverage gaps and minor quality issues
