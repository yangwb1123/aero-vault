Now I have a comprehensive picture. Let me compile the full staff engineering review.

---

# Staff Engineering Review: Aero-Vault Codebase

## Executive Summary

The codebase is structurally sound with a well-defined DAG architecture, clear package separation, and good test coverage in the AI subsystem. However, there are **critical violations of the project's own engineering constraints**, significant **code duplication between protocol adapters**, and **7 declared but unfixed bugs**. The codebase shows signs of rapid iteration outpacing refactoring discipline.

---

## 1. Critical: File Size Violations (AGENTS.md §0)

The project's own rules require ≤500 lines per file and ≤50 lines per function. These are repeatedly violated.

| File | Lines | Violates | Recommended Action |
|------|-------|----------|--------------------|
| `sdk/go/aerovault/client.go` | 1006 | ≤500 | Split into `client.go`, `client_files.go`, `client_admin.go`, `client_s3.go` |
| `internal/api/rest/handler.go` | 958 | ≤500 | Split bucket sub-resources into `handler_buckets.go`, keep CRUD handlers |
| `internal/api/s3compat/handler.go` | 890 | ≤500 | Same approach — bucket configs, object operations, listings |
| `cmd/server/main.go` | 861 | ≤500 | Extract `buildRouter`, `buildStorageFrom`, `initInfrastructure` into separate files in a `cmd/server/internal` dir |
| `internal/auth/condition.go` | 657 | ≤500 | Extract condition operators into per-operator files or a registry pattern |

**Function length violations** (>50 lines):

| Function | Lines | Location |
|----------|-------|----------|
| `compileSingleCondition` | 215 | `internal/auth/condition.go:260` |
| `ListBucketVersions` | 72 | `s3compat/handler.go` |
| `buildStorageFrom` | 71 | `cmd/server/main.go` |
| `run()` | 78 | `cmd/server/main.go` |

The 215-line `compileSingleCondition` is particularly egregious — it's a single switch statement with 25+ nearly identical cases that should be a registry of condition factories.

---

## 2. Critical: Declared but Unfixed Bugs

Seven bugs are documented in test file comments but remain unfixed (verified present):

| # | Location | BUG | Impact |
|---|----------|-----|--------|
| 1 | `cli_test.go:1419` | `cmdList` never checks HTTP status code | CLI silently shows 5xx responses as valid output |
| 2 | `cli_test.go:1422` | `cmdTag` never checks HTTP status | Same |
| 3 | `cli_test.go:1424` | `cmdVersions` never checks HTTP status | Same |
| 4 | `cli_test.go:1426` | `cmdLineage` never checks HTTP status | Same |
| 5 | `cli_test.go:1428` | `cmdSearch` never checks HTTP status | Same |
| 6 | `cli_test.go:1430` | `cmdSnapshot` ignores missing DB file | Silent success on no-op |
| 7 | `lifecycle_test.go:436` | Lifecycle GC ignores store.Delete errors | Orphaned storage objects possible |

**Severity: High** — These are not just test bugs; they reflect real logic errors in the CLI commands. The test comments documenting them without fixing is a code smell.

---

## 3. Significant: Code Duplication Between REST and S3 Handlers

Both `rest/handler.go` and `s3compat/handler.go` independently implement nearly identical patterns:

| Pattern | REST Handler | S3 Handler | Lines Duplicated |
|---------|-------------|------------|------------------|
| `checkBucketPolicy` | `handler.go:33-52` | `handler.go:36-56` | ~20 (almost identical) |
| Range/conditional handling | `handler.go:179-213` | `handler.go:162-209` | ~50 (semantic equivalent) |
| Response header writing | `handler.go:219-244` | `handler.go:203-224` | ~25 (patterns differ in field names) |
| Byte range parsing logic | `handler.go:290-307` | `handler.go:176-196` | ~30 (identical logic) |

**Impact**: When adding new features (e.g., a new response header), both handlers must be updated in lockstep. This is a maintenance time bomb.

**Recommendation**: Extract shared handler logic into an `internal/api/shared` package with:
- `func WriteObjectHeaders(...)`
- `func HandleByteRange(...)`
- `func CheckBucketPolicy(...)`
- `func ParseAndValidateKey(...)`

---

## 4. High: `context.Background()` Misuse

Several locations use `context.Background()` where the parent context should be propagated:

| Location | Line | Issue |
|----------|------|-------|
| `internal/ai/indexer.go` | 313, 316 | `context.Background()` in `handleExtractError` — telemetry counter context should be propagated from caller |
| `internal/events/bus.go` | 139 | `context.Background()` in `broadcast` — dropped event telemetry loses request tracing |
| `internal/events/postgres_transport.go` | 82, 139 | `context.Background()` for `conn.Close()` — should use the transport's context |
| `internal/api/webdav/dav.go` | 302, 381 | Falls back to `context.Background()` when request context has no tenant — silently loses tracing |

**Impact**: Span context is lost, making debugging production incidents harder. The indexer and event bus telemetry increments will never be traceable to a specific request.

---

## 5. High: Error Handling Anti-Patterns

**5a. Silently discarded errors:**

```go
// rest/handler.go:242
_, _ = io.Copy(w, rc)  // copy errors ignored
```

```go
// s3compat/handler.go:119
_ = h.svc.SetObjectACL(...)  // ACL failure silently swallowed
```

```go
// Multiple locations
_, _ = w.Write(...)  // write errors ignored
```

**5b. Error message leaking internal details:**

```go
// rest/handler.go:487 (classify function default case)
default:
    return "InternalError", err.Error(), http.StatusInternalServerError
```

The raw `err.Error()` is returned to the client, potentially leaking internal paths, SQL queries, or stack traces.

**5c. Missing error wrapping context:**

Many calls use `return err` instead of `return fmt.Errorf("operation %s: %w", op, err)`, making debugging harder without stack traces.

---

## 6. Medium: Middleware Chain Order Mismatch

The documented middleware order in `AGENTS.md` (§2.5) is:
```
RequestID → CORS → Auth → Tenant → RateLimit(global) → OTel → Recoverer → AccessLog
```

The actual order in `cmd/server/main.go:buildRouter`/`applyMiddleware` (line 261-274) is:
```
access_log → concurrency → recoverer → otel → rate_limit → tenant → auth → cors → request_id
```

In the `applyMiddleware` function (line 255):
```go
{"access_log", ...},     // applied last (wraps outermost)
{"concurrency", ...},
{"recoverer", ...},
{"otel", ...},
{"rate_limit", ...},
{"tenant", ...},
{"auth", ...},
{"cors", ...},
{"request_id", ...},     // applied first (wraps innermost)
```

**Issues**:
1. **CORS after Auth** — preflight OPTIONS requests must be authenticated, which breaks CORS
2. **Tenant after Auth** — auth may depend on tenant header extraction
3. **Rate limit after Tenant extraction** — correct (rate limiter uses tenant), but **AccessLog also after** means rate-limited requests aren't logged with tenant info
4. The order is **reversed** from what's documented (outermost handler wraps innermost, so the first item in the list is the outermost/last-to-run)

---

## 7. Medium: Concurrency Safety Concerns

**7a. Event bus subscriber lifecycle:**

```go
// events/bus.go:76-79
func (b *Bus) Subscribe() (<-chan repository.Event, func()) {
    ch := make(chan repository.Event, b.subBuffer)
    b.mu.Lock()
    b.subs = append(b.subs, ch)
    b.mu.Unlock()
    return ch, func() { b.Unsubscribe(ch) }
}
```

The caller must call the returned cancel function, but nothing enforces this. Several callers may leak goroutines.

**7b. PerTenantConcurrencyLimiter locking:**

```go
// middleware/middleware.go:193-196
pt.mu.Lock()
if pt.inflight[tenant] >= pt.perTenant {
    pt.mu.Unlock()
    // Release global slots...
    return
}
pt.inflight[tenant] += cost
pt.mu.Unlock()
```

The unlock-then-release pattern has a TOCTOU race: between unlocking and releasing global slots, the per-tenant refcount is wrong.

---

## 8. Medium: Test Quality & Coverage Gaps

| Package | Coverage | Status |
|---------|----------|--------|
| `ai` | 84.2% | ✅ Good |
| `auth` | 77.9% | ✅ Acceptable |
| `service` | 58.0% | ⚠️ Below 80% target |
| `rest` | 52.8% | ⚠️ Below 80% target |
| `s3compat` | 61.4% | ⚠️ Below 80% target |
| `middleware` | 78.0% | ✅ Near target |
| `webui` | 0% | ❌ No tests |
| `config` | ? | ❌ Likely minimal |

**Additional issues**:
- **Known BUG comments in test files** (7 bugs) — tests document real logic errors but aren't fixed
- **CLI tests are 1440 lines** — violates the 500-line constraint themselves
- **No race detection in CI** (`-race` flag absent)
- **No fuzz testing** for condition parsing, extractor, or PII

---

## 9. Low: TODO/Debt Accumulation

**9a. Dead code paths:**
- `internal/ai/pii.go:114` `MapPII` function used only in `indexer.go:300` — single caller in a tag update block
- `internal/auth/arn.go` — `Region` field marked as unused
- `internal/api/rest/search.go:251` — awkward `var _ = service.DefaultBucket` to suppress unused import

**9b. Missing cleanup handlers:**
- `internal/events/bus.go` — `Close()` doesn't nil the transport
- `cmd/server/main.go` — `bus.Close()` is called but doesn't prevent further `Publish` calls (use-after-close risk)

**9c. No BUGS.md tracking:**
The project has 7 declared bugs but no centralized bug tracking document. They're hidden in test file comments.

---

## 10. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity (compileSingleCondition) | ~30 | < 10 | ❌ |
| Function length (compileSingleCondition) | 215 lines | < 50 lines | ❌ |
| File length (top 5 files) | 657-1006 lines | < 500 lines | ❌ |
| Test coverage (avg all packages) | ~60% | > 80% | ⚠️ |
| Race-safe concurrency | ⚠️ Partial | All | ⚠️ |
| Code duplication (REST/S3 handlers) | ~200 lines duplicated | < 5% duplication | ⚠️ |
| Known unfixed bugs | 7 | 0 | ❌ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| File size violations (5 files >500 lines) | High | M | **P0** | Violates AGENTS.md §0, blocks CI gate |
| 7 declared but unfixed bugs in CLI tests | High | M | **P0** | Real logic errors, not just tests |
| REST/S3 handler code duplication | High | L | **P1** | ~200 lines duplicated, divergence risk |
| context.Background() misuse (6 locations) | Medium | S | **P1** | Lost tracing context |
| Middleware chain order mismatch | Medium | S | **P1** | CORS after Auth breaks OPTIONS |
| 215-line compileSingleCondition function | Medium | M | **P1** | Refactor to operator registry |
| Missing CI race detection | Medium | S | **P1** | Latent concurrency bugs |
| WebUI has zero tests | Medium | M | **P2** | Any change is untested |
| No centralized bug tracking (BUGS.md) | Low | S | **P2** | Bugs documented only in test comments |
| Silently discarded errors (io.Copy, SetObjectACL) | Medium | S | **P1** | Masked failures |
| Error messages leak internal details | Medium | S | **P1** | Security concern |
| PerTenantConcurrencyLimiter TOCTOU race | Medium | M | **P1** | Concurrency bug |

---

## Quick Wins (Can implement in <1 hour)

1. **Fix middleware chain order** — Swap CORS before Auth in `applyMiddleware`, reverse the list to match documented order (RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog)

2. **Create BUGS.md** — Migrate the 7 documented bugs from test file comments to a centralized tracker with severity estimates

3. **Eliminate 3 `context.Background()` calls** — Propagate context in `indexer.go:313,316` and `bus.go:139` from the caller chain

4. **Extract shared HTTP helpers** — Begin with `func WriteObjectHeaders(...)` and `func ParseByteRange(...)` shared between REST and S3 handlers

5. **Document known bugs in CLI** — Add proper error checking to `cmdList`, `cmdTag`, `cmdVersions`, `cmdLineage`, `cmdSearch` for HTTP status codes

---

## Final Verdict

| Dimension | Rating |
|-----------|--------|
| **Overall Code Quality** | ⚠️ **Needs Work** |
| **Architecture** | ✅ Good — DAG design, layered separation |
| **Test Coverage** | ⚠️ 60% average, 0% for webui |
| **Constraint Compliance** | ❌ File/function size limits violated |
| **Error Handling** | ⚠️ Inconsistent — some good patterns, some silent swallows |
| **Technical Debt** | **P0 urgent**: file splits + fix 7 bugs |
| **Onboarding Clarity** | ⚠️ Good docs but bugs hidden in test files |

**The codebase has a solid architectural foundation but is suffering from deferred refactoring.** The most urgent issues are the file size violations (which block the project's own CI gate) and the 7 declared bugs (which erode trust in the CLI). I recommend starting with:

1. **Split the 5 oversized files** (enforce AGENTS.md §0 compliance)
2. **Fix the 7 CLI bugs** (clear P0 logic errors)
3. **Extract shared handler logic** from REST and S3 handlers
4. **Fix middleware chain order** to match documented specification
5. **Create BUGS.md** for centralized tracking
