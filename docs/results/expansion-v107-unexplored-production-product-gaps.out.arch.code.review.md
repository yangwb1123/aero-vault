Now I have a comprehensive understanding of the codebase. Let me compile the full Staff Engineer review.

---

# Staff Engineer Code Quality Review: aero-vault

## Overall Assessment: **Good — with notable technical debt**

The codebase demonstrates strong engineering discipline: clean layering, well-defined interfaces, proper dependency injection, and good test coverage for many packages. However, several systemic issues need attention to maintain long-term velocity and quality.

---

## Code Organization

| Aspect | Assessment | Details |
|--------|-----------|---------|
| **Modularity** | ✅ Excellent | Clean separation: `service` → `storage`/`repository`, protocol adapters are thin, `main.go` wires via dependency injection |
| **Layering** | ✅ Good | Protocol → Service → Storage/Repository hierarchy respected; no handler bypasses FileService |
| **Dependency direction** | ⚠️ Minor concern | `ai` package imports `storage` and `repository` (necessary), but `jobs` registry is interface-hacked to avoid import — a sign the job abstraction needs formalization |
| **File size violations** | ❌ Several | `handler.go` (958), `s3compat/handler.go` (890), `auth/condition.go` (657), `main.go` (861), `repository.go` (394), `cli_test.go` (1440) — all exceed AGENTS.md 500-line limit |

### Finding: File size violations (Critical)

| Field | Value |
|-------|-------|
| **Title** | Multiple production and test files exceed 500-line limit |
| **Location** | `internal/api/rest/handler.go` (958), `internal/api/s3compat/handler.go` (890), `internal/auth/condition.go` (657), `cmd/server/main.go` (861), `internal/api/rest/admin.go` (411 — borderline), test files in CLI/storage/auth |
| **Current State** | Handler files accumulate all HTTP endpoint methods in one struct+file; condition.go contains the entire condition operator system; main.go does config, wiring, DI, metrics, auth, and server setup |
| **Recommended State** | Split handler.go into domain-specific files (e.g., `files.go`, `buckets.go`, `batch.go`, `folders.go`). Split condition.go by operator family (string, numeric, date, IP, ARN). Split main.go into `wire.go` (DI assembly) and `server.go` (HTTP lifecycle) |
| **Impact** | Review difficulty, merge conflicts, onboarding friction, violates project rules |
| **Effort** | M |

### Finding: `internal/auth/condition.go` — Monolithic file (657 lines)

| Field | Value |
|-------|-------|
| **Severity** | High | 
| **Description** | All IAM condition operators, parsing, context resolution, and evaluation live in one file. The `Get` method alone handles 30+ condition key types in a single switch statement. This violates Single Responsibility and the 500-line limit. |
| **Current State** | `func (c *ConditionContext) Get(key string) (string, bool)` is a massive switch with per-key validation. All condition operators are in one file with overlapping concerns. |
| **Recommended State** | Split into `condition_string.go`, `condition_numeric.go`, `condition_date.go`, `condition_ip.go`, `condition_arn.go`, `condition_bool.go`. Extract a `ConditionEvaluator` interface. |
| **Impact** | Maintainability — one change to a date operator requires touching a 657-line file. Testing is harder. |
| **Effort** | M |

---

## Naming & Documentation

| Aspect | Assessment | Details |
|--------|-----------|---------|
| **Consistency** | ✅ Excellent | Go idioms followed (`Err*`, `New*`, `With*`), package names reflect domain (not `utils`!) |
| **Public API docs** | ✅ Good | Most exported types and functions have doc comments |
| **Internal clarity** | ✅ Good | Inline comments explain complex logic well (e.g., SSE rewrap, RRF merge, versionID generation) |
| **`interface{}` usage** | ⚠️ Minor | `dto.go: Hits interface{}` and `policy.go: map[string]interface{}` for JSON polymorphic fields — acceptable for JSON payloads but weakly typed |

### Finding: `interface{}` in API response type (Low)

```go
// dto.go:88
Hits interface{} `json:"hits"`
```
The search response's `Hits` field uses `interface{}` when it should be `[]Hit`. This loses compile-time type safety. While it allows nil/empty flexibility, a typed nil slice would work identically.

**Recommendation**: Change to `[]Hit` — `json.Marshal` already serializes nil slices as `null`, and empty slices as `[]`.

---

## Error Handling

| Aspect | Assessment | Details |
|--------|-----------|---------|
| **Error wrapping** | ✅ Good | `fmt.Errorf("...: %w", err)` used consistently with `errors.Is` / `errors.As` |
| **Sentinel errors** | ✅ Good | Well-defined at package boundaries (`service.ErrNotFound`, `repository.ErrNotFound`, `storage.ErrNotFound`) |
| **Shadowed errors** | ⚠️ Some issues | Several places use `var err error` then shadow with `:=` creating potential for unhandled intermediate errors |
| **Silent swallowing** | ⚠️ Notable pattern | Non-critical failures silently continue (quota decrement, chunk cleanup, event publish) |

### Finding: Inconsistent error propagation in `indexer.go` (Medium)

```go
// indexer.go:173-175
func (ix *Indexer) IndexObjectByID(ctx context.Context, objectID int64) error {
    ...
    text, err := ix.extractor.Extract(ctx, obj.ContentType, rc)
    text, err = ix.applyPII(ctx, obj, text, err)  // err is overridden!
    if err != nil {
        return ix.handleExtractError(obj.Key, err)
    }
```

The `err` from `Extract` is passed into `applyPII` but then **overwritten** by the return vale. If `Extract` fails AND `applyPII` succeeds, the extraction error is lost. `applyPII` receives the error and checks it, but doesn't propagate it when PII has no error.

**Recommendation**: Check `err` from `Extract` before passing to `applyPII`:

```go
text, err := ix.extractor.Extract(ctx, obj.ContentType, rc)
if err != nil {
    // handle extraction error before PII
    return ix.handleExtractError(obj.Key, err)
}
text, err = ix.applyPII(ctx, obj, text, err)  // err is nil now
```

### Finding: `context.Background()` used in production path (Medium)

```go
// indexer.go:185
telemetry.IncIndexerSkip(context.Background(), "unsupported")
```

The indexer's error handler uses `context.Background()` instead of passing the request context. This means tracing context + tenant attribution is lost for indexer skip metrics.

**Recommendation**: Pass `ctx` through to `handleExtractError`:

```go
func (ix *Indexer) handleExtractError(ctx context.Context, key string, err error) error {
    ...
    telemetry.IncIndexerSkip(ctx, "unsupported")
```

### Finding: `uploadStorageKey` fallback — invariant violation (Medium)

```go
// file.go:124-130
func uploadStorageKey(u repository.Upload) string {
    if u.StorageKey != "" {
        return u.StorageKey
    }
    return storageKey(u.TenantID, u.Bucket, u.Key)
}
```

This fallback exists for "in-flight across the migration" but represents a permanent workaround for a historical schema migration. If the migration is complete, this should be removed. If not, the fallback may yield an unversioned key on a versioned bucket, causing data corruption.

**Recommendation**: Add a startup validation that all `Upload.StorageKey` fields are populated. If the migration is complete, remove the fallback entirely.

---

## Logging

| Aspect | Assessment | Details |
|--------|-----------|---------|
| **Structured logging** | ✅ Excellent | `slog` with key-value pairs throughout, JSON format in production |
| **Log levels** | ✅ Good | Proper use of `Info`, `Warn`, `Error` — no `Debug` overuse |
| **Correlation IDs** | ✅ Good | `middleware.RequestIDFrom(ctx)` propagated through `emit()`, error responses |
| **Sensitive data** | ✅ Good | No passwords/keys in log messages (checked the codebase) |
| **CLI inconsistency** | ⚠️ Notable | CLI package uses `fmt.Println` instead of structured logging |

### Finding: CLI uses unstructured `fmt.Println` (Low)

```go
// cli_search.go:40, cli_admin.go:86, etc.
fmt.Println(string(respBody))
fmt.Printf("%-40s tenant=%-20s ...", ...)
```

The CLI package uses `fmt.Print*` family for output while the rest of the codebase uses structured `slog`. While acceptable for a CLI tool (user-facing output), it means CLI code cannot participate in structured log routing or be easily tested (output goes to stdout, not a logger).

**Recommendation (minor)**: For a CLI tool, `fmt.Print*` is acceptable for user-facing output. However, consider using `slog` for diagnostics and `fmt` only for structured table output. Consider `tabwriter` for alignment.

---

## Testing Practices

| Aspect | Assessment | Details |
|--------|-----------|---------|
| **Coverage** | ⚠️ Mixed | 5 packages <60%: `rest` (52.8%), `repository` (54.6%), `service` (58.0%), `storage` (57.3%), `reconcile` (60.6%), `events` (64.0%) |
| **Test organization** | ✅ Good | Table-driven tests, parallel execution, temp directories for isolation |
| **Mock quality** | ✅ Good | `ai.MockLLM`, `ai.HashEmbedder`, `noopSink` are clean, deterministic |
| **Integration tests** | ⚠️ Incomplete | `integration/fullserver_test.go` and `integration/postgres_integration_test.go` exist but have no statements coverage |
| **No test for main.go** | ❌ Missing | `cmd/server` has `[no test files]` — server wiring/boot is untested |

### Finding: Inadequate coverage in core packages (High)

| Package | Coverage | Target | Gap |
|---------|----------|--------|-----|
| `internal/api/rest` | 52.8% | >80% | ❌ |
| `internal/repository` | 54.6% | >80% | ❌ |
| `internal/service` | 58.0% | >80% | ❌ |
| `internal/storage` | 57.3% | >80% | ❌ |
| `internal/events` | 64.0% | >80% | ❌ |

These are the **critical path** packages. The service layer is the core business logic — 58% coverage means nearly half the execution paths are untested. Error recovery paths (e.g., partial storage failures, quota edge cases, concurrent versioned writes) are likely untested.

**Recommendation**: Target 80% on all core packages before adding new features. Focus service tests on: concurrent versioned writes, quota boundary conditions, multipart error recovery, and lock/WORM edge cases.

### Finding: Test file size violations (Medium)

```bash
1440 internal/cli/cli_test.go
1120 internal/storage/storage_test.go
922  internal/repository/chunks_events_buckets_test.go
910  internal/auth/condition_test.go
893  internal/api/webdav/dav_test.go
```

These test files are too large. A 1440-line test file is hard to navigate, slow to execute, and encourages developers to add tests at the bottom rather than in organized locations.

**Recommendation**: Split test files to match the production code organization. For storage, have `local_test.go`, `s3_test.go`, `encrypt_test.go`, `multipart_test.go` mirroring the production files.

### Finding: No server startup tests (High)

`cmd/server` has zero test files. The `main.go` wiring logic (861 lines) is entirely untested. This means:
- Config parsing errors don't get exercised
- Auth registry construction issues go undetected
- AI component assembly failures are integration-time surprises
- SSE rewrap, KMS setup, and bus transport wiring are all blind spots

**Recommendation**: Create `cmd/server/main_test.go` with integration tests that start the server, hit `/healthz`, `/readyz`, perform PUT/GET/DELETE cycles. Use the SQLite+local FS default path so it works in CI without Docker.

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| `internal/api/rest/handler.go` (958 lines) violates 500-line limit | High | M | P0 | Must split per domain (files, buckets, batch, folders, multipart) |
| `internal/api/s3compat/handler.go` (890 lines) | High | M | P0 | Must split by sub-resource (objects, buckets, mulitpart, tagging, ACL) |
| `cmd/server/main.go` (861 lines) — monolithic wiring | High | L | P0 | Extract DI wiring into `wire.go` / `server.go` |
| `internal/auth/condition.go` (657 lines) | Medium | M | P1 | Split by operator family |
| Core package coverage <60% (rest, repository, service, storage) | High | L | P0 | Must reach 80% before new features |
| `context.Background()` in indexer error handler | Medium | S | P1 | Metric attribution lost |
| `uploadStorageKey` migration fallback | Medium | S | P1 | Potential versioned-bucket corruption |
| Test files >500 lines (4 files) | Medium | M | P2 | Split test files to match production structure |
| `dto.go` uses `interface{}` for `Hits` | Low | S | P2 | Type safety improvement |
| CLI uses `fmt.Print*` for all output | Low | S | P3 | Acceptable for CLI; minor concern |
| `applyPII` error shadowing in `indexer.go` | Medium | S | P1 | Error may be lost |
| `main.go` `run()` function too large (multiple concerns) | High | L | P1 | Extract `buildServer`, `runWithShutdown` etc. |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | Not measured but `condition.go` `Get()` method is high (30+ branches) | < 10 | ⚠️ |
| **Function length** | Most functions <50 lines ✅ | < 50 | ✅ |
| **Test coverage (avg)** | ~70% (weighted) | > 80% | ⚠️ |
| **Code duplication** | SQL dialect branches are duplicated (upsert, insert, list queries repeated for postgres/sqlite) | < 5% | ⚠️ |
| **Documentation coverage** | ~70% of exported symbols documented | > 70% | ⚠️ |

---

## Specific Deep Dives

### 1. SQL Dialect Duplication Pattern (Medium Debt)

In `repository/sql_objects.go` and `repository/sql_buckets.go`, queries are duplicated for Postgres vs SQLite:

```go
// sql_objects.go:37-73
switch s.dialect {
case dialectPostgres:
    q = `INSERT INTO objects (...) VALUES ($1,$2,...) ON CONFLICT ... 
         DO UPDATE SET ... RETURNING ...`
    args = []any{...}
default:
    q = `INSERT INTO objects (...) VALUES ($1,$2,...) ON CONFLICT ... 
         DO UPDATE SET ...`
    args = []any{...}
```

This pattern repeats across ~6 files. Differences are:
- `::jsonb` cast (Postgres only)
- `RETURNING` clause (Postgres uses `RETURNING`, SQLite uses `RETURNING` too now in modern SQLite, but with different syntax)
- Placeholder differences (handled by `rebind()`)
- Timestamp handling (`now()` vs scalar)

**Recommendation**: Build queries from parts. Define a `sqlDialect` interface with methods for `Now()`, `CastJSON(string) string`, `ReturningClause() string`, and build queries programmatically. This eliminates the `switch` duplication.

### 2. FileService `checkLockBeforeOverwrite` / `checkMultipartLock` — Duplicate Logic (Low)

```go
// file_crud.go:163-170
func (s *FileService) checkLockBeforeOverwrite(...) error {
    if !versioning {
        if cur, err := s.repo.GetObject(...); err == nil {
            if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) { ... }

// file_multipart.go:99-107 — same logic
func (s *FileService) checkMultipartLock(...) error {
    if !bcfg.Versioning {
        if cur, gErr := s.repo.GetObject(...); gErr == nil {
            if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) { ... }
```

**Recommendation**: Extract `checkObjectLockOverwrite(ctx, tenant, bucket, key, versioning bool) error`. Both callers use identical logic.

### 3. Duplicate HTTP Header Writing Pattern (Medium)

In `handler.go`, 4+ methods write almost identical response headers:
- `handleRangeOrFull` (28 lines of headers)
- `serveRange` (31 lines of headers)  
- `Head` (27 lines of headers)
- `handleConditional` (partial headers in 304 path)

**Recommendation**: Extract `writeObjectHeaders(w, obj, extraHeaders)` and `writeObjectHeadersWithRange(w, obj, off, length)`. This reduces boilerplate and makes header consistency guaranteed.

### 4. `ai/search.go` — Query Pipeline Clean

The search pipeline in `search.go` is well-structured: cache check → embed → search vectors → search BM25 → RRF merge → rerank → trim → audit → cache store. Decent separation of responsibilities. The `rrfMerge` function with inline insertion sort is simple and appropriate for the small result sizes.

### 5. `main.go` — The God Function Problem (High)

`run()` at 861 lines handles:
- Config loading
- Logger setup
- Storage initialization
- SSE rewrap
- Repository setup
- Bus + transport
- Embedder/LLM/Reranker construction
- Service construction
- Job pool setup
- AI component assembly (search, chat, agent)
- Indexer setup
- Background worker setup (antivirus, replication, webhook, reconcile)
- Auth registry
- Rate limiter setup
- Prometheus setup
- Router construction
- Middleware application
- Server lifecycle

**Recommendation**: Extract into well-named functions: `buildConfig()`, `initInfrastructure()` (already partially done), `buildAIStack()`, `buildBackgroundWorkers()` (done), `buildAuthRegistry()` (done), `buildRouter()` (done), `runServer()` (done). These are already partially extracted but `run()` still orchestrates too many steps. Consider a `Server` struct with explicit fields.

---

## Quick Wins (S Effort)

| Issue | Fix |
|-------|-----|
| `context.Background()` in `handleExtractError` | Pass `ctx` through |
| `err` shadowing in `indexer.go` `IndexObjectByID` | Check `err` before `applyPII` |
| `interface{}` in `dto.go` Hits | Change to `[]Hit` |
| `uploadStorageKey` fallback removal | Add startup validation or remove |
| `.gitignore` for OS files | Currently only `var/` — add `.DS_Store`, `*.swp`, etc. |

---

## Final Summary

| Dimension | Score | Commentary |
|-----------|-------|------------|
| **Overall Code Quality** | **Good** | Clean layering, good naming, proper error wrapping, excellent test infrastructure for many packages |
| **Critical Quality Issues** | ✅ File size violations in handler.go, s3compat, condition.go, main.go | These violate project rules (AGENTS.md 500-line limit) and must be addressed before further changes to those files |
| **Maintainability Concerns** | ⚠️ SQL dialect duplication across repository; God-function in main.go; spaghetti is developing in handler.go as sub-resources grow | New engineers will struggle with 958-line handler files |
| **Technical Debt** | ⚠️ Medium — 13 items in register, 4 critical | The dialect duplication pattern and file size violations compound with each PR |
| **Quick Wins** | 5 items, all < 1 hour | Easy quality improvements with high impact |

### Top 3 Actions

1. **P0: Split `handler.go` and `s3compat/handler.go`** — This violates the project's own 500-line rule. These files grow with every new endpoint and will only get worse.
2. **P0: Improve core package coverage to 80%** — `repository` (54.6%), `service` (58.0%), `storage` (57.3%), `rest` (52.8%) are too low for a production storage system.
3. **P1: Extract SQL dialect differences into a builder pattern** — Every new repository method duplicates the `switch s.dialect` pattern, which is error-prone and violates DRY.
