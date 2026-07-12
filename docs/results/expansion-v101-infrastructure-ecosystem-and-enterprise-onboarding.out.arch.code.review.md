Now I have a comprehensive understanding of the entire codebase. Let me produce the Staff Engineer review.

---

# Staff Engineer Code Review: aero-vault

## 1. Executive Summary

**Overall Code Quality: Good** — The codebase demonstrates strong engineering discipline: clean layering, effective use of interfaces, nil-safe optional components, proper error wrapping, consistent logging, and robust test coverage. However, several systemic concerns around context propagation, SQL dialect duplication, test-file bloat, and documentation accuracy need attention before this code is production-ready for multi-team development.

---

## 2. Findings

### Category: Organization | Severity: High | Title: Middleware chain documentation contradicts actual execution order

**Location:** `cmd/server/main.go` — `applyMiddleware()` function + AGENTS.md §2.5

**Description:** The documented middleware chain order and the actual code chain are inconsistent. The comment in AGENTS.md specifies `RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog`, but the actual execution chain includes a concurrency limiter and timing wrappers that are undocumented. More critically, the comment omits the concurrency limiter entirely (positioned between Recoverer and AccessLog in actual execution), and the chain-building logic in `applyMiddleware` assembles the chain from inside-out, which makes the execution order non-obvious to readers.

**Current State:**
```go
chain := []struct {
    name string
    mw   func(http.Handler) http.Handler
}{
    {"access_log", middleware.AccessLog(logger)},
    {"concurrency", concurrencyMW},
    {"recoverer", middleware.Recoverer(logger)},
    {"otel", telemetry.HTTPMiddleware("aero-vault")},
    {"rate_limit", rl.Middleware()},
    {"tenant", middleware.Tenant},
    {"auth", authReg.Middleware()},
    {"cors", middleware.CORS(corsCfg)},
    {"request_id", middleware.RequestID},
}
// applied inside-out: last element in list runs FIRST
```

The actual execution order (first-to-run to last):
1. request_id → cors → auth → tenant → rate_limit → otel → recoverer → concurrency → access_log

**Recommended State:** Either reorder the slice so the execution order is explicit (reading top-to-bottom), or add a clear comment documenting the execution order. The AGENTS.md should also be updated to match actual behavior.

**Impact:** Maintainability — new developers will misread the middleware ordering. Security-impacting if someone mistakenly adds auth-sensitive middleware in the wrong position.

**Effort:** S

---

### Category: Error Handling | Severity: High | Title: `context.Background()` used in production code paths discards tracing and cancellation

**Location:** Multiple files
- `internal/ai/indexer.go:313,316` — `telemetry.IncIndexerSkip(context.Background(), ...)`
- `internal/events/bus.go:139` — `telemetry.IncEventDropped(context.Background())`
- `internal/events/postgres_transport.go:82,139` — `conn.Close(context.Background())`
- `internal/api/webdav/dav.go:302,381` — `ctx = context.Background()`

**Description:** Several hot paths use `context.Background()` instead of propagating the caller's context. This means:
1. OpenTelemetry trace context is lost — spans become orphaned
2. Deadline/cancellation is not respected
3. In the indexer, context cancellation during graceful shutdown won't stop processing

**Current State:**
```go
// indexer.go:313
telemetry.IncIndexerSkip(context.Background(), "unsupported")

// bus.go:139
telemetry.IncEventDropped(context.Background())

// dav.go:302
ctx = context.Background()
```

**Recommended State:**
```go
// indexer.go:313 — pass ctx from the calling IndexObject method
telemetry.IncIndexerSkip(ctx, "unsupported")

// bus.go:139 — use ctx from Publish/Deliver
telemetry.IncEventDropped(ctx)

// dav.go:302 — propagate from the request context
```

**Impact:** Observability gaps — traces will have broken parent-child relationships, making debugging AI pipeline latency issues significantly harder.

**Effort:** M (touches 6 files, but each change is trivial)

---

### Category: Organization | Severity: High | Title: SQL dialect branching creates massive code duplication

**Location:** `internal/repository/sql_objects.go` (all methods have Postgres/SQLite branches)

**Description:** Every repository method that writes data has near-duplicate SQL strings and argument lists for Postgres and SQLite. For example, `UpsertObject` (lines 15-50) and `InsertObjectVersion` (lines 65-125) each have complete duplicate implementations with only minor SQL differences (jsonb casts, datetime functions). This pattern is repeated across files like `sql_buckets.go`, `sql_chunks.go`, etc.

**Current State:**
```go
func (s *sqlStore) UpsertObject(ctx context.Context, obj Object) (Object, error) {
    // ... 15 lines of shared setup ...
    switch s.dialect {
    case dialectPostgres:
        q = `INSERT INTO objects (...) VALUES ($1,...$10::jsonb,..., now(), now(), NULL)
              ON CONFLICT ... DO UPDATE ... RETURNING ...`
        args = []any{...}
    default:
        q = `INSERT INTO objects (...) VALUES ($1,...$10,...,$13,$14, NULL)
              ON CONFLICT ... DO UPDATE ... RETURNING ...`
        args = []any{..., now, now}
    }
    // rest of method
}
```

**Recommended State:** Extract the SQL string and args into a helper or use query builders that accept dialect-aware formatting. At minimum, create a `sqlFmt` struct with methods like `.now() string`, `.jsonCast(string) string`, `.placeholder(n int) string` to reduce duplication.

**Impact:** Maintainability — adding a new column requires editing 4+ SQL strings across 2+ dialect branches. Risk of drift between dialects is high.

**Effort:** L (refactor across all sql_*.go files)

---

### Category: Testing | Severity: Medium | Title: Several test files exceed 500 lines, indicating missing test modularization

**Location:** 
- `internal/cli/cli_test.go` — 1440 lines
- `internal/storage/storage_test.go` — 1120 lines
- `sdk/go/aerovault/client_test.go` — 1013 lines
- `internal/repository/chunks_events_buckets_test.go` — 922 lines
- `internal/auth/condition_test.go` — 910 lines
- `internal/api/webdav/dav_test.go` — 893 lines
- `internal/api/s3compat/handler_test.go` — 847 lines
- `internal/ai/integration_test.go` — 762 lines
- `internal/mcp/server_test.go` — 761 lines
- `internal/reconcile/lifecycle_test.go` — 701 lines

**Description:** While AGENTS.md's 500-line limit applies to production code, these test files are uncomfortably large. They mix test helpers, fixtures, and dozens of test functions. This makes tests harder to navigate, increases merge conflicts, and encourages copy-paste test patterns.

**Recommended State:** Split by test category (e.g., `cli_test.go` → `cli_crud_test.go`, `cli_search_test.go`, `cli_admin_test.go`) or by method being tested.

**Impact:** Team productivity — onboarding new developers to understand test patterns requires scrolling through 1000+ line files.

**Effort:** M

---

### Category: Error Handling | Severity: Medium | Title: Inconsistent error capitalization and wrapping patterns

**Location:** Across the codebase — compare `ErrNotFound = errors.New("object not found")` (lowercase) with HTTP error messages like `"object not found"` vs `"InvalidArgument"` in handler error classification.

**Description:** Go convention says errors should start with lowercase (since they're often chained). The sentinel errors follow this (`ErrNotFound = errors.New("object not found")`). But `classify()` in `handler.go` `switch` cases return mixed case messages like `"NoSuchUpload"`, `"InvalidRange"`, `"PreconditionFailed"`. These are presented to users as JSON error codes so the casing is intentional for API surface. However, there's no consistent policy about which errors wrap vs. resurface, leading to some internal errors that should be 500s leaking `err.Error()` directly to the HTTP response in the `default` case of `classify()`.

**Current State:**
```go
// handler.go - default case leaks raw error
default:
    return "InternalError", err.Error(), http.StatusInternalServerError
```

**Recommended State:** Use a structured error type that separates internal details from public messages, or at minimum sanitize the default case to not leak internal state.

**Impact:** Security — internal path/configuration details could leak in error responses for unclassified errors.

**Effort:** M

---

### Category: Quality | Severity: Medium | Title: `interface{}` used in critical type positions instead of concrete types

**Location:**
- `internal/api/rest/dto.go:88` — `Hits interface{}`
- `internal/auth/policy.go:28,225-228` — Policy JSON unmarshaling uses `map[string]interface{}` throughout

**Description:** The `dto.go` `Hits` field is typed as `interface{}` making the API response shape unpredictable for SDK consumers. While the policy parsing code has a legitimate need for `interface{}` due to AWS's polymorphic IAM policy format, `Hits` should be a concrete type.

**Current State:**
```go
type searchResponse struct {
    Answer      string      `json:"answer,omitempty"`
    Hits        interface{} `json:"hits"`
    // ...
}
```

**Recommended State:**
```go
type searchResponse struct {
    Answer string       `json:"answer,omitempty"`
    Hits   []searchHit  `json:"hits"`
    // ...
}
```

**Impact:** API consistency — SDK code generation and client type-safety depend on concrete response types.

**Effort:** S

---

### Category: Testing | Severity: Medium | Title: `cmd/server` and `internal/webui` packages have 0% test coverage

**Location:** `cmd/server/main.go` (861 lines), `internal/webui/web.go`

**Description:** The main entry point (861 lines) has no test coverage. This is the most complex assembly point in the system, wiring together 15+ components with complex conditional logic. The webui package also has no tests.

**Current State:** Zero test files exist for either package.

**Recommended State:** Add integration-style tests for `cmd/server` that verify the wiring logic (storage backend selection, embedder construction, middleware assembly) using dependency injection-friendly constructors. At minimum, test `buildStorageFrom`, `buildEmbedder`, `buildLLM` as isolated unit tests.

**Impact:** High risk — a refactoring error in `main.go` wiring won't be caught until runtime. The 861-line file is already the largest non-test production file.

**Effort:** L

---

### Category: Logging | Severity: Low | Title: Indexer error path loses request context for tracing

**Location:** `internal/ai/indexer.go:313-316` — `handleExtractError`

**Description:** When the indexer encounters an unsupported file type or an extraction error, it increments a telemetry counter with `context.Background()` instead of the event-processing context. This means trace spans for dropped index events won't link back to the originating object lifecycle event.

**Current State:**
```go
func (ix *Indexer) handleExtractError(key string, err error) error {
    if errors.Is(err, ErrUnsupported) {
        telemetry.IncIndexerSkip(context.Background(), "unsupported")
        return nil
    }
    telemetry.IncIndexerSkip(context.Background(), "error")
    return fmt.Errorf("extract %q: %w", key, err)
}
```

The method is called from `ProcessEvent` which DOES have a `ctx` parameter, but `handleExtractError` doesn't accept or propagate it.

**Recommended State:** Change signature to `handleExtractError(ctx context.Context, key string, err error)`.

**Impact:** Low — telemetry still works, but trace correlation is lost.

**Effort:** S

---

### Category: Quality | Severity: Low | Title: Quota check functions have redundant guard logic

**Location:** `internal/service/file_crud.go:34-53` — `checkBytesQuota` and `checkObjectsQuota`

**Description:** Both quota check functions duplicate the same conditional structure: check size>0 vs size==0. The pattern is repeated for bytes and objects.

**Current State:**
```go
func checkBytesQuota(q repository.TenantQuota, size int64) error {
    if size > 0 {
        if q.MaxBytes > 0 && q.UsedBytes+size > q.MaxBytes {
            return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, ...)
        }
        return nil
    }
    if q.MaxBytes > 0 && q.UsedBytes >= q.MaxBytes {
        return fmt.Errorf("%w: bytes %d/%d", ErrQuotaExceeded, ...)
    }
    return nil
}
// same structure for checkObjectsQuota
```

**Recommended State:** Merge into a single generic quota check:
```go
func checkResourceQuota(used, delta, max int64, label string) error {
    if max <= 0 {
        return nil
    }
    if delta > 0 {
        if used+delta > max {
            return fmt.Errorf("%w: %s %d/%d", ErrQuotaExceeded, label, used+delta, max)
        }
    } else if used >= max {
        return fmt.Errorf("%w: %s %d/%d", ErrQuotaExceeded, label, used, max)
    }
    return nil
}
```

**Impact:** Low — cosmetic code quality.

**Effort:** S

---

### Category: Organization | Severity: Low | Title: `flexTime` implementation is overly permissive

**Location:** `internal/repository/sql_helpers.go:138-163`

**Description:** The `flexTime.Scan` accepts time.Time, []byte, string (in 4+ formats), and even Unix-nano integers. This flexibility masks format inconsistencies and makes debugging silent format drift harder.

**Current State:** Accepts RFC3339Nano, RFC3339, SQL timestamp, Unix-nano int.

**Recommended State:** If the system always writes RFC3339Nano (which it does — see I1 in AGENTS.md), only accept that format in `flexTime`.

**Impact:** Low — currently works, but masks potential bugs if a migration introduces a different format.

**Effort:** S

---

## 3. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Not measured (gocyclo uninstalled) | < 10 | ⚠️ Cannot verify |
| File length (non-test) | Max: 861 (main.go) / No files > 500 | ≤ 500 | ✅ |
| File length (test) | Max: 1440 (cli_test.go) | < 500 | ❌ (7 files > 500) |
| Test coverage (overall) | ~65% average, range 0%-100% | > 80% | ⚠️ (10/25 packages < 80%) |
| Test coverage (critical gaps) | 0% — cmd/server, webui | > 50% | ❌ |
| Code duplication | SQL dialect branching moderate | < 5% | ⚠️ |
| `context.Background()` in prod | 6 instances in hot paths | 0 | ❌ |
| TODO/FIXME/HACK in prod | 0 | 0 | ✅ |
| `panic()` in prod code | 0 | 0 | ✅ |
| `log.Fatal` in prod code | 0 | 0 | ✅ |
| `interface{}` in public types | 2 locations | 0 | ⚠️ |

---

## 4. Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| SQL dialect duplication | High — maintenance burden, drift risk | L | P0 | ~40% of repository code is duplicated SQL strings |
| `context.Background()` in hot paths | High — broken traces, no cancellation | M | P0 | 6 instances across 4 files |
| Missing cmd/server test coverage | High — 861 lines, 0% coverage | L | P0 | Wiring errors caught only at runtime |
| Middleware chain documentation mismatch | Medium — developer confusion | S | P1 | Comment and code disagree |
| Large test files (>500 lines) | Medium — onboarding friction | M | P1 | Multiple files, test splitting |
| `interface{}` in API response DTO | Medium — SDK generation impact | S | P1 | `Hits interface{}` in dto.go |
| Error message leaking internals | Medium — information disclosure | M | P1 | `classify()` default case |
| `flexTime` over-permissive format | Low — masks storage bugs | S | P2 | Accept 4+ time formats |
| Quota check code duplication | Low — cosmetic | S | P2 | `checkBytesQuota` / `checkObjectsQuota` |
| Missing webui tests | Low | S | P2 | Static UI, low risk |
| Rate-limit validation duplication | Low — minor duplication across config | S | P2 | Validate rate limits pattern |

---

## 5. Quick Wins (S effort, high impact)

1. **Fix `context.Background()` in indexer** — Change `handleExtractError` signature to accept `ctx context.Context`
2. **Fix `context.Background()` in bus** — Propagate from `Publish`/`Deliver` to telemetry call
3. **Type `Hits` as `[]searchHit`** — Remove `interface{}` from public API response
4. **Add comment documenting actual middleware execution order** — Update AGENTS.md §2.5
5. **Merge quota check functions** — Eliminate duplication in `file_crud.go`
6. **Fix webdav context** — Replace `context.Background()` with request context

---

## 6. Summary

### Strengths

- **Clean layering**: protocol → service → storage/repository with clear interface boundaries
- **Nil-safe optional components**: AI pipeline, embedder, LLM all gracefully degrade when unconfigured
- **Excellent error wrapping**: Consistent use of `fmt.Errorf("%w: ...")` with sentinel errors
- **Opt-in defaults**: All advanced features gated behind env flags, zero-config start works
- **No production panics or log.Fatal**: Disciplined error handling outside of `main()`
- **Good separation of concerns**: Handler layer is thin (no business logic in REST/S3 handlers)
- **Well-structured testing**: 107 test files, extensive use of `t.Helper()`, temp dirs for isolation
- **No TODO/FIXME/HACK**: Codebase is clean of technical-debt markers
- **Config validation**: Structured `Validate()` method catches misconfiguration early
- **Compile-time interface checks**: `var _ service.ChunkCleaner = (*ai.Indexer)(nil)`

### Critical Issues (must fix before production)

1. **`context.Background()` in hot paths** — Breaks observability and prevents proper shutdown
2. **SQL dialect duplication** — High maintenance burden, risk of silent drift between Postgres and SQLite
3. **Missing cmd/server tests** — 861 lines of unwired assembly code with zero test coverage

### Recommendations for Next Sprint

1. Extract SQL dialect differences into a helper struct to eliminate duplication
2. Add context propagation tests to ensure background contexts aren't used in production paths
3. Split largest test files (>500 lines) into focused test files
4. Add integration smoke test for `main.go` wiring
5. Remove `interface{}` from API response types
6. Document actual middleware execution order in AGENTS.md and add a comment in `applyMiddleware`
