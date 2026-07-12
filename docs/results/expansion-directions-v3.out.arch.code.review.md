Now I have a comprehensive understanding. Let me provide my full Staff Engineer analysis.

---

# Staff Engineer Review: AeroVault Code Quality & Next Steps

After thorough investigation, I can see the full picture. Let me clarify the situation first, then provide a complete review.

## Current State Assessment

The pi-batch pipeline has already **generated and applied code changes** from many expansion documents. The working tree contains **103 modified files** with uncommitted changes spanning the entire codebase. Here's what I found:

### What was generated and applied

| Area | Changes | Scope |
|------|---------|-------|
| **REST handler** | Bucket policy enforcement, Content-MD5, StorageClass, refactored Range/Conditional handling | `handler.go` +610/-163 |
| **S3 compat** | Policy enforcement, legal hold, restore stub, Content-MD5, StorageClass | `handler.go` +202/-72 |
| **Auth module** | Refactored Lookup → lookupStore/lookupJWT, scope validation | Full refactor |
| **Service layer** | New error types (BadDigest, SizeMismatch, ObjectCorrupt, MetadataTooLarge), ReadVerificationConfig | `file.go` -507/+166 |
| **AI pipeline** | Search refactored into validation/vector/semantic methods, Indexer split into applyPII/handleExtractError | `search.go`, `indexer.go`, `agent.go`, `chat.go`, `embedder.go`, `llm.go`, `rerank.go`, `bm25.go` |
| **WebDAV** | Rename rollback, error propagation, constants | `dav.go` |
| **CLI** | New commands | `cli.go`, `cli_test.go` |
| **Delete**d files | CHANGELOG.md, DECISIONS.md, ROADMAP.md, TODO.md, docker-compose.demo.yml | — |

### Build & test status (the good news)

| Check | Result |
|-------|--------|
| `go build ./...` | ✅ Passes |
| `go vet ./...` | ✅ Passes |
| `go test ./...` | ✅ All 24 packages pass |
| `gofmt -l .` | ⚠️ 2 files need formatting (`auth/arn_test.go`, `auth/condition.go`) |
| `gocyclo -over 10` | ✅ No violations (but see below) |

### The problems I found

| Issue | Severity | Details |
|-------|----------|---------|
| **File length violations** | 🔴 **Critical** | `handler.go` 958 lines, `s3compat/handler.go` 890 lines, `auth/condition.go` 657 lines all exceed the **500-line limit** (AGENTS.md Rule #1) |
| **Function length** | ⚠️ **High** | Need to verify individual functions stay ≤ 50 lines |
| **Test coverage** | ⚠️ **Unknown** | Tests pass but coverage may have regressed |
| **Generated code quality** | ⚠️ **Mixed** | Some is well-structured (validators extracted), some is repetitive (`checkBucketPolicy` duplicated in REST + S3) |
| **No commitment** | ⚠️ **High** | None of these changes are committed — they're all in the working tree |

---

## Answer to Your Question

You asked: **"Which .arch.md and .impl-plan.md should I implement?"**

**The answer is: the pi-batch pipeline already generated code for many expansion documents simultaneously, and it's applied to the source tree but not committed.** You don't need to pick just one — instead, the next priority is to **review, validate, and commit the generated changes**, then address the file-length and code-quality regressions.

The expansion documents that had code generated (12 with actual implementation reports):

| Document | Status | Code Quality |
|----------|--------|-------------|
| `expansion-v105-deep-code-integrity-gaps` | ✅ Applied | Good (policy checks, refactored) |
| `expansion-v108-production-hardening` | ✅ Applied | Good (error types, validation) |
| `expansion-v112-architect-product-frontiers` | ✅ Applied | Moderate |
| `expansion-v118-architect-frontiers` | ✅ Applied | Moderate |
| `expansion-v118-security-benchmark-config` | ✅ Applied | Moderate |
| `expansion-v100-architect-frontiers` | ✅ Applied | Moderate |
| `expansion-v102-genuine-code-level-gaps` | ✅ Applied | Good |
| `expansion-v104-architect-systemic-gaps` | ✅ Applied | Good |
| `expansion-v110-deep-code-gaps` | ✅ Applied | Moderate |
| `expansion-v84-product-architect-directions` | ✅ Applied | Basic |
| `expansion-v86-system-production-gaps` | ✅ Applied | Basic |
| `expansion-v92-multi-backend-sse-filters` | ✅ Applied | Basic |

The remaining **117 expansion documents** had architecture analysis and implementation plans generated, but **no code was produced for them**.

---

## Code Quality Review

### 1. Code Organization — ⚠️ Needs Work

**Good**: The pi-batch refactored the auth module properly (split `Lookup` into `lookupStore`/`lookupJWT`). The search module now has clean validation methods (`validateMode`, `validate`).

**Bad**: 
- `checkBucketPolicy` is **duplicated** verbatim in both `internal/api/rest/handler.go` and `internal/api/s3compat/handler.go`. This is a 30-line copy-paste. Should be a shared helper.
- `handler.go` at 958 lines and `s3compat/handler.go` at 890 lines **violate the 500-line constraint** — these must be split.

### 2. Naming & Documentation — ✅ Good

The generated code follows Go conventions. Public methods are documented. Constants like `DefaultStorageClass`, `MaxMetadataSize` are well-named.

### 3. Error Handling — ✅ Good

New typed errors added: `ErrBadDigest`, `ErrSizeMismatch`, `ErrObjectCorrupt`, `ErrMetadataTooLarge`. WebDAV Rename now has a **rollback path** if source delete fails after destination write — this is excellent defensive coding.

### 4. Testing — ⚠️ Needs Verification

All tests pass, but `go test -cover` needs to be run to verify coverage hasn't regressed below the 50% threshold.

### 5. Technical Debt

| Debt | Impact | Location |
|------|--------|----------|
| Duplicated `checkBucketPolicy` | Medium | REST + S3 handler |
| 958-line handler.go | High | Must split |
| 890-line s3compat/handler.go | High | Must split |
| 657-line condition.go | High | Must split |
| Uncommitted worktree (103 files) | Critical | All changes at risk |
| 2 files not gofmt'd | Low | auth/arn_test.go, auth/condition.go |

---

## Recommended Next Steps

Based on this analysis, I recommend the following **priority order**:

### P0 — Secure and validate the generated code

1. **Run `gofmt -w`** on the two unformatted files
2. **Run `make check`** end-to-end to ensure all quality gates pass
3. **Review and commit** the generated changes as a single comprehensive commit, or split into logical commits per subsystem

### P1 — Fix file-length violations

The three files exceeding 500 lines need immediate splitting:

| File | Lines | Split Strategy |
|------|-------|---------------|
| `internal/api/rest/handler.go` | 958 | Extract Range/Conditional/Thumbnail helpers into separate files |
| `internal/api/s3compat/handler.go` | 890 | Extract bucket policy, ACL, legal hold handlers |
| `internal/auth/condition.go` | 657 | Already a standalone file — needs internal split |

### P2 — Consolidate duplicated logic

- Extract `checkBucketPolicy` into a shared helper (e.g., `internal/api/s3compat/policy.go`)
- Ensure REST and S3 handlers share it

### P3 — Determine next expansion direction

Once the generated code is committed and cleaned up, the **ROADMAP** items **6–10** are the highest-value unaddressed areas. The most relevant expansion documents pre-generated for these:

| ROADMAP Item | Best-matched .arch.md | Best-matched .impl-plan.md |
|---|---|---|
| #6 Circuit breakers | `expansion-v113-synchronous-teardown-webhook-circuit-breaker-and-operational-gaps.out.arch.md` | Same-stem `.impl-plan.md` |
| #7 S3 parity | `expansion-v114-s3-protocol-completeness-and-operational-gaps.out.arch.md` | Same-stem `.impl-plan.md` |
| #8 Content integrity | `expansion-v105-deep-code-integrity-gaps.out.arch.md` | Same-stem `.impl-plan.md` |
| #9 Storage tiering | `expansion-v91-storage-tiering-metadata-query-events-cache.out.arch.md` | Same-stem `.impl-plan.md` |
| #10 Metadata HA | `expansion-v120-bootstrap-migration-graceful-shutdown.out.arch.md` | Same-stem `.impl-plan.md` |

---

## Final Verdict

| Metric | Status |
|--------|--------|
| **Overall Code Quality** | **Good** (generated code is solid, but file-size violations exist) |
| **Critical Issues** | 🔴 3 files exceed 500-line limit; 🔴 103 uncommitted changes at risk |
| **Maintainability** | ⚠️ REST/S3 handler files too large; duplicated policy check |
| **Technical Debt** | Moderate — fixable in 2–3 hours of cleanup work |
| **Quick Wins** | `gofmt -w` (2 files); commit generated changes; split 3 large files |

**Clear next action**: Run `make check` → fix violations → commit the worktree. Then we can discuss which ROADMAP item to tackle next.

Would you like me to start with the cleanup work (P0/P1), or would you like me to pick one specific expansion direction from the list above and implement it from scratch with a fresh implementation?
