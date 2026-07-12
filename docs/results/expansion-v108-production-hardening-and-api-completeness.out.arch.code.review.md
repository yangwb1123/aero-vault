Now I have a comprehensive understanding of the codebase. Let me compile the full Staff Engineer code quality review.

---

# Staff Engineer Code Quality Review

**Target Subsystem:** `internal/repository` + `internal/service` 数据持久化层与服务核心

**Review Date:** 2026-07-12

---

## Executive Summary

The `internal/repository` and `internal/service` packages form the backbone of aero-vault — the data persistence layer (Repository) and the core business logic (FileService). The codebase shows strong engineering DNA: clean interfaces, good layering, well-defined abstractions. However, it suffers from a **growing God Interface anti-pattern** in the Repository layer, **code bloat** approaching violation thresholds, and **accumulated shortcuts** (stubbed methods, silently swallowed errors, dead code, wrong pagination semantics) that degrade long-term maintainability.

**Overall Assessment: Needs Work** — the foundations are sound but technical debt is accumulating faster than it's being refactored.

---

## 1. Code Organization

### Finding 1A: God Interface — Repository (⛔ CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | Repository interface violates God type rule (394 lines, ~55 methods) |
| **Location** | `internal/repository/repository.go:76-393` |
| **Description** | The `Repository` interface spans 394 lines with ~55 methods covering objects, buckets, uploads, events, chunks, jobs, idempotency, API keys, leases, tenants, audit, webhook failures, and quotas. This violates the AGENTS.md rule that types >300 lines must be split. Any new backend implementation must implement all ~55 methods. |
| **Current State** | One monolithic `Repository` interface implemented by `sqlStore` struct. New storage backends (e.g., MySQL, FoundationDB) are practically infeasible due to interface size. |
| **Recommended State** | Split into domain interfaces: `ObjectRepository` (CRUD+tags+versions), `BucketRepository` (config+policies), `UploadRepository` (multipart), `EventRepository`, `JobRepository`, `AdminRepository` (keys+tenants+audit+leases), `QuotaRepository`. Keep a `Repository` composite that embeds all, but accept narrower interfaces in consumer code. |
| **Code Example** | **Current**: `type Repository interface { ... 55 methods ... }` → every consumer takes the full god interface. **Recommended**: `type ObjectRepository interface { UpsertObject(...); GetObject(...); ListObjects(...); ... }` and `type Repository interface { ObjectRepository; BucketRepository; ... }` — consumers like `FileService` only depend on what they use. |
| **Impact** | High — blocks backend portability, makes testing harder (mocking 55 methods), violates project's own AGENTS.md rules |
| **Effort** | L (2-3 weeks) |

### Finding 1B: God File — sql_buckets.go (📌 HIGH)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | High |
| **Title** | sql_buckets.go at 419 lines, approaching 500-line limit |
| **Location** | `internal/repository/sql_buckets.go` |
| **Description** | The file combines bucket CRUD, CORS, logging, notifications, lifecycle expiration, and soft-delete cleanup — 15+ methods from multiple domains. The `ListExpired` function alone is ~60 lines. |
| **Current State** | Monolithic file with mixed bucket concerns |
| **Recommended State** | Split into `sql_buckets_crud.go`, `sql_buckets_config.go`, `sql_buckets_cors.go`, `sql_buckets_logging.go`, `sql_buckets_notifications.go`, `sql_buckets_lifecycle.go` |
| **Impact** | Medium — onboarding new devs to find the right method within 419 lines |
| **Effort** | M (half-day mechanical split) |

### Finding 1C: Dialect Duplication (📌 HIGH)

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | High |
| **Title** | SQL dialect branching copy-pasted across ~15 functions |
| **Location** | `internal/repository/sql_objects.go`, `internal/repository/sql_buckets.go`, `internal/repository/quota.go` |
| **Description** | Every transaction method duplicates the entire SQL string for Postgres vs SQLite dialects using `if s.dialect == dialectPostgres { q = ... } else { q = ... }`. This is highly error-prone — the two branches can diverge silently. Currently ~12 such blocks. |
| **Current State** | Inline dialect branching with full string duplication |
| **Recommended State** | Use a query template system: store SQL templates with a `{{jsonb}}` marker that gets replaced by `::jsonb` (Postgres) or nothing (SQLite), and a `{{now}}` marker for current timestamp. Or better, use `sprig` or a simple `strings.ReplaceAll` approach. |
| **Code Example** | **Current**: ```go if s.dialect == dialectPostgres { q = `INSERT INTO ... VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12) RETURNING ...` } else { q = `INSERT INTO ... VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING ...` } ``` **Recommended**: ```go q := `INSERT INTO ... VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10{{jsonb}},$11{{jsonb}},$12{{now}}) RETURNING ...`; q = s.dialectize(q) ``` |
| **Impact** | High — each new method needs two SQL versions, increasing bug surface area |
| **Effort** | M (build a `dialectize` helper, convert all call sites) |

---

## 2. Naming & Documentation

### Finding 2A: Underscore-Prefixed "System" Keys (🔄 MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | Medium |
| **Title** | Magic string `_aero_` prefix repeated across layers |
| **Location** | `internal/service/file_crud.go:99` (`_aero_content_md5`), `internal/service/file_crud.go:129-131` (`_aero_content_encoding`, `_aero_scrub_status`, `_aero_legal_hold`), `internal/service/file.go:130` |
| **Description** | System metadata keys use `_aero_` prefix as a convention, but this is scattered as string literals across multiple files. No centralized constant definition. A typo like `_aero_scrub_status` vs `_aero_scrub_status` would be silent. |
| **Current State** | String literals `"_aero_content_md5"`, `"_aero_scrub_status"`, `"_aero_legal_hold"`, `"_aero_content_encoding"` |
| **Recommended State** | Define constants: `const MetaContentMD5 = "_aero_content_md5"`, `const MetaScrubStatus = "_aero_scrub_status"`, etc. |
| **Impact** | Low — but a category of preventable bugs |
| **Effort** | S |

### Finding 2B: `defaults()` Returns Shadowing (⚠️ MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | Medium |
| **Title** | `defaults()` in `file.go` always returns same defaults for bucket |
| **Location** | `internal/service/file.go:159-163` |
| **Description** | The `defaults()` function accepts tenant and bucket and applies defaults when empty. However, several callers use `tenant, _ = defaults(tenant, "")` to only get tenant defaulted, but this ignores the fact that `defaults` also defaults bucket. Function signature is ambiguous. |
| **Current State** | `tenant, _ = defaults(tenant, "")` — discarding bucket default, but the function intent is unclear |
| **Recommended State** | Create `defaultTenant(tenant string) string` and `defaultBucket(bucket string) string` helpers separately, or make `defaults` return all values and always use both. |
| **Impact** | Low — works correctly but confusing to readers |
| **Effort** | S |

---

## 3. Error Handling

### Finding 3A: Silently Swallowed Errors (⛔ CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | **Critical** |
| **Title** | Multiple locations silently discard errors with `_ = err` |
| **Location** | `internal/repository/sql_buckets.go:91`, `internal/repository/sql_buckets.go:96`, `internal/repository/sql_buckets.go:106`, `internal/repository/sql_chunks.go:*`, `internal/repository/sql_helpers.go:*` |
| **Description** | Throughout the repository layer, non-critical errors are discarded with `_ = err`. While some are genuinely best-effort (chunk cleanup on hard delete), others like the `DELETE FROM parts WHERE upload_id NOT IN (SELECT id FROM uploads)` are silently skipped, potentially leaving orphaned parts. |
| **Current State** | ```go if _, err := tx.ExecContext(ctx, ...); err != nil { _ = err // Best-effort } ``` |
| **Recommended State** | At minimum, `slog.Warn("...", "err", err)`. If truly safe to ignore, add a comment explaining *why* (e.g., "chunks are ephemeral, GC will clean up orphans"). |
| **Impact** | High — orphaned data accumulates silently, debugging becomes guesswork |
| **Effort** | M (add logging to all swallowed error sites) |

### Finding 3B: `WriteAccessLog` is a No-Op Stub (📌 HIGH)

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | High |
| **Title** | WriteAccessLog implemented as a complete no-op |
| **Location** | `internal/repository/sql_buckets.go:215-224` |
| **Description** | `WriteAccessLog` accepts 7 parameters and immediately returns nil. The function signature has the parameters but all are assigned to `_`. This means any caller relying on access logging gets silent success with no actual logging. The function also calls `s.CreateBucket()` earlier in the getter methods but not here, which is inconsistent. |
| **Current State** | ```go func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error { _ = tenant; _ = sourceBucket; _ = method; _ = key; _ = status; _ = latencyMs; _ = userAgent; return nil } ``` |
| **Recommended State** | Either implement the function (write log entries to target bucket), or remove it from the interface and document that access logging is not yet implemented. A stub that silently succeeds is the worst option — it breaks the principle of least surprise. |
| **Impact** | High — callers believe access logging works |
| **Effort** | M to implement, S to remove from interface |

### Finding 3C: `ListExpired` Has a Copy-Paste Bug (⛔ CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | **Critical** |
| **Title** | ListExpired SQL query has dead first query and missing WHERE clause |
| **Location** | `internal/repository/sql_buckets.go:128-152` |
| **Description** | The `ListExpired` function defines two SQL query strings: one assigned to `q` (with `updated_at < $1` WHERE clause and `LIMIT $2`), and a second (the one actually executed) that lacks the time comparison in the WHERE clause and only has `LIMIT $1`. The `q` variable is never used, and at the end `_ = q` is a dead reference. The executed query returns all non-deleted objects from expired buckets instead of filtering by age, so the function then loops through ALL of them in Go to find expired ones — a correctness bug that becomes a performance bug as the bucket grows. |
| **Current State** | ```go q := `SELECT ... WHERE ... AND o.updated_at < $1 LIMIT $2` // NEVER USED rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT ... WHERE ... LIMIT $1`), limit) // Missing time filter! // Later: for rows.Next() { if updated.Time.Add(...).Before(now) { out = append(out, obj) } } // Go-side filtering _ = q // Dead reference ``` |
| **Recommended State** | Use the first query (with `updated_at < $1` and correct LIMIT) and pass both the cutoff time and the limit as parameters. Remove the dead `q` variable. |
| **Impact** | High — lifecycle expiration reads all objects from expired buckets into memory to find a few expired ones; correctness bug for large buckets |
| **Effort** | S (one-line SQL fix + parameter addition) |

---

## 4. Logging

### Finding 4A: Inconsistent Logger Field Formats (🔄 MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | Medium |
| **Title** | Log attribute patterns vary across error/warn calls |
| **Location** | `internal/service/file_crud.go:69`, `file_crud.go:123`, `file_crud.go:173`, `file_multipart.go:130` |
| **Description** | Some log calls use `"key", value` pairs, others use `"key", value, "key2", value2` — but the grouping of related attributes (tenant, bucket, key) is inconsistent. Some calls include the `err` key, others don't. |
| **Current State** | Mixed: `s.logger.Warn("size mismatch", "expected", size, "actual", info.Size, "tenant", tenant, "bucket", bucket, "key", key)` vs `s.logger.Error("repo write failed; storage object orphaned", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "err", err)` |
| **Recommended State** | Define a consistent log field order convention: first the message, then `"tenant"`, `"bucket"`, `"key"` (when relevant), then `"err"` (when present), then domain-specific fields. Add a `With()` helper or define structured log helpers. |
| **Impact** | Low — makes log aggregation and alerting slightly harder |
| **Effort** | S |

---

## 5. Testing Practices

### Finding 5A: Shallow Service Tests (📌 HIGH)

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | High |
| **Title** | Service tests cover happy paths but miss error/corner cases |
| **Location** | `internal/service/service_test.go` |
| **Description** | The test file (644 lines) focuses on CRUD happy paths, Content-MD5, ACL, range requests, and versioning. Missing tests include: lock violations during multipart upload, quota enforcement edge cases (partial fills, unbounded streams), concurrent same-key writes with versioning, chunk cleaner failure during hard delete, `ListObjectsByTag` pagination across multiple pages, `ListExpired` with the broken SQL query, and simultaneous hard/soft delete with versioning. |
| **Current State** | Tests exist for basic CRUD, MD5, ACL, range, versioning; coverage unknown for error paths |
| **Recommended State** | Add table-driven tests for each error path. On the `preflightQuota` functions, test with `MaxObjects=0` (should pass), `UsedBytes >= MaxBytes` with `size==0` (should reject), exactly-at-cap, and negative delta (delete reduces usage below zero). |
| **Impact** | Medium — error handling code paths are exercised only accidentally |
| **Effort** | L (add ~200 lines of table-driven tests across error paths) |

### Finding 5B: Repository Tests Leak Implementation Details (⚠️ MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Repository tests couple to SQLite-specific behavior |
| **Location** | `internal/repository/chunks_events_buckets_test.go` (922 lines) |
| **Description** | The largest test file at 922 lines tests multiple domains (chunks, events, buckets) in one file. Tests are written against SQLite and may not run against Postgres. There's no indication of integration test build tags for Postgres-dependent tests. |
| **Current State** | Single test file per domain, all run against SQLite |
| **Recommended State** | Split into domain test files matching the proposed interface split. Add `//go:build integration` variants for Postgres. Use `t.Cleanup` consistently. Consider `testcontainers-go` for Postgres CI. |
| **Impact** | Medium — no Postgres coverage in CI; 922-line test file approaching project limit |
| **Effort** | M (mechanical split + Postgres CI setup) |

---

## 6. Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| **God Interface (Repository ~55 methods)** | High | L | **P0** | Violates AGENTS.md type limit (300 lines). Blocks new backends. |
| **`ListExpired` broken SQL (dead variable, missing WHERE)** | High | S | **P0** | Lifecycle expiration reads entire bucket into memory. |
| **`WriteAccessLog` no-op stub** | High | M | **P0** | Silent data loss — callers think logging works. |
| **Silently swallowed errors (~6 sites)** | Medium | M | **P1** | Orphaned data accumulates silently. |
| **Dialect SQL duplication (~12 branches)** | Medium | M | **P1** | Bug-prone; every new method requires dual SQL. |
| **`ListObjectsByTag` client-side pagination broken** | Medium | M | **P1** | `HasMore` is incorrect when filtered results < page size. |
| **`sql_buckets.go` > 400 lines** | Low | S | P2 | Approaching 500-line limit. |
| **`_aero_*` magic strings not centralized** | Low | S | P2 | Typo-prone string literals. |
| **`flexTime` parsing 5+ time formats** | Low | S | P2 | 50 lines of complexity that could be 5 lines with a single format. |
| **`checkBytesQuota`/`checkObjectsQuota` near-duplicate** | Low | S | P3 | Two functions with same structure; could be genericized. |
| **`preflightQuota`/`preflightMultipartQuota` near-duplicate** | Low | S | P3 | Same logic repeated across files. |
| **`gofmt` issues in 2 files** | Low | S | P3 | `internal/auth/arn_test.go` and `internal/auth/condition.go` not formatted. |

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Repository interface size** | 394 lines / ~55 methods | < 300 lines / < 20 methods | ❌ |
| **`file_crud.go` length** | 420 lines | ≤ 500 lines | ✅ (but warning) |
| **`sql_buckets.go` length** | 419 lines | ≤ 500 lines | ✅ (but warning) |
| **`repository.go` (interface) length** | 394 lines | ≤ 300 lines | ❌ |
| **Silent error swallows** | ~6 sites | 0 | ❌ |
| **Dead code** | `_ = q` in ListExpired, `WriteAccessLog` stub | 0 | ❌ |
| **`gofmt` compliance** | 2 files fail | 0 | ❌ |
| **SQL dialect duplication** | ~12 function-pairs | 0 | ❌ |
| **Test coverage (repo+service)** | Likely >70% per sprint goal | >80% | ⚠️ unknown for error paths |
| **Cyclomatic complexity (per function)** | Most <10 | <10 | ✅ (need gocyclo tool to verify) |

---

## 8. Quick Wins (S effort, High impact)

| # | Issue | Fix |
|---|-------|-----|
| 1 | **`ListExpired` SQL bug** | Replace the wrong query `LIMIT $1` with the correct one using `updated_at < $1 LIMIT $2`. Remove `_ = q`. Saves memory and fixes correctness. |
| 2 | **`WriteAccessLog` stub** | Either implement it or remove from the interface. A no-op stub is the worst option. |
| 3 | **`_aero_*` magic strings** | Centralize into constants in a new `internal/service/metadata.go`. |
| 4 | **`gofmt` violations** | Run `gofmt -w` on `internal/auth/arn_test.go` and `internal/auth/condition.go`. |
| 5 | **`checkBytesQuota` / `checkObjectsQuota` merge** | Create a single `checkQuota(q TenantQuota, deltaBytes, deltaObjects int64) error`. |
| 6 | **Silent error swallows** | Add `slog.Warn` with context to each `_ = err` site. |
| 7 | **`ListObjectsByTag` pagination** | Add a comment documenting the known limitation, or move filtering into the SQL query using JSON containment operators. |

---

## 9. Final Summary

### Overall Code Quality: **Needs Work**

The Repository + Service layer has strong architectural bones — clear separation from storage, consistent tenant/bucket/key model, good use of interfaces for testability. However, the codebase is showing clear signs of **accumulated shortcuts** and **interface bloat** that must be addressed before adding new features.

### Critical Quality Issues (Must Fix Before Production)

1. **`Repository` God Interface** — 55 methods violates project rules and makes the system untestable and unportable. Split into domain interfaces.
2. **`ListExpired` SQL bug** — the time-filtered WHERE clause is missing from the actual query, making lifecycle expiration read entire buckets into memory.
3. **`WriteAccessLog` no-op stub** — provides false assurance that access logging works.

### Maintainability Concerns

- **Dialect branching** is copy-pasted ~12 times. Every new method adds 2x SQL surface area. Build a `dialectize()` helper.
- **Error handling culture** — `_ = err` appears at 6+ sites without logging. This erodes observability.
- **Dead code** — `ListExpired` has a dead variable (`q`) and an unused query string; `WriteAccessLog` has dead parameters.

### Technical Debt Summary

- **P0 items**: 3 (God Interface, ListExpired bug, WriteAccessLog stub)
- **P1 items**: 4 (swallowed errors, dialect duplication, tag pagination, bucket file size)
- **P2 items**: 5+ (magic strings, flexTime complexity, test coverage gaps)
- **Estimated payoff**: ~2-3 weeks to clear P0, ~1 week for P1, ongoing for P2

### Recommendation

1. **Week 1**: Fix P0 bugs (`ListExpired` SQL, `WriteAccessLog` stub/remove), add logging to swallowed errors, run `gofmt -w`.
2. **Week 2-3**: Split the `Repository` God Interface into domain interfaces. Start with the interface definition split, then refactor consumers (`FileService`, `reconcile`, `events`, `jobs`, `auth`) to depend on narrower interfaces.
3. **Week 4**: Build a `dialectize()` query template helper to eliminate SQL duplication.
4. **Ongoing**: Add error-path tests for every service and repository method. Target >80% branch coverage.
