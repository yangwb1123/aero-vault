Now I have a thorough understanding of the AI subsystem. Let me present the full review.

---

# Staff Engineer Code Review: `internal/ai/` Subsystem

**Reviewed Package:** `github.com/aero-vault/aero-vault/internal/ai`
**Files:** 20 production source files (2,986 non-test lines), 16 test files (3,397 lines)
**Test Coverage:** 84.0% statement coverage

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Generally < 10, `Indexer.handle` (9), `Search.Query` (8) | < 10 | ✅ |
| Function length | Most < 50; `Answer` (43), `IndexObjectByID` (48) | < 50 lines | ✅ |
| Test coverage | 84.0% | > 80% | ✅ |
| Code duplication | `chat.go` RecordUsage block ~15 lines × 2, insertion sort × 2 | < 5% | ⚠️ |
| Documentation coverage | ~80% public types/funcs documented | > 70% | ✅ |
| Single-file size | Largest: `qdrant.go` (355), `search.go` (357) | < 500 | ✅ |
| Custom sort vs stdlib | 2 hand-rolled insertion sorts | use `sort.Slice` | ❌ |

---

## Detailed Findings

### 1. 🟥 CRITICAL: `context.Background()` in telemetry calls breaks observability

| Field | Value |
|-------|-------|
| **Category** | Error Handling / Logging |
| **Severity** | Critical |
| **Title** | Lost trace context in indexer error counter |
| **Location** | `internal/ai/indexer.go:313, 316` — `handleExtractError()` |
| **Description** | `handleExtractError` calls `telemetry.IncIndexerSkip(context.Background(), ...)` instead of propagating the caller's `ctx`. This breaks OpenTelemetry trace correlation — the counter increment is detached from the request trace tree, making it impossible to correlate indexer skips with the originating request. |
| **Current State** | ```go
func (ix *Indexer) handleExtractError(key string, err error) error {
    if errors.Is(err, ErrUnsupported) {
        telemetry.IncIndexerSkip(context.Background(), "unsupported")
        return nil
    }
    telemetry.IncIndexerSkip(context.Background(), "error")
    return fmt.Errorf("extract %q: %w", key, err)
}
``` |
| **Recommended State** | Accept a `context.Context` parameter (even though the function signature currently takes `(key string, err error)`). The callers already hold a context. |  |
| **Code Example** | ```go
func (ix *Indexer) handleExtractError(ctx context.Context, key string, err error) error {
    if errors.Is(err, ErrUnsupported) {
        ix.logger.Info("indexer: skipping unsupported content", "key", key)
        telemetry.IncIndexerSkip(ctx, "unsupported")
        return nil
    }
    telemetry.IncIndexerSkip(ctx, "error")
    return fmt.Errorf("extract %q: %w", key, err)
}
``` |
| **Impact** | All skip telemetry is orphaned from request context. Trace-based debugging of indexing failures becomes impossible. |
| **Effort** | S |

---

### 2. 🟧 HIGH: Duplicate RecordUsage logic in `chat.go` — maintenance risk

| Field | Value |
|-------|-------|
| **Category** | Quality / Technical Debt |
| **Severity** | High |
| **Title** | 15-line RecordUsage block duplicated across Answer and AnswerStream |
| **Location** | `internal/ai/chat.go:180-195` (AnswerStream) and `internal/ai/chat.go:224-239` (Answer) |
| **Description** | Both methods contain an identical ~15-line block for building `chunkIDs`, `objIDs`, extracting tokens, computing cost, and calling `RecordUsage` + `RecordAIUsage`. This is a classic copy-paste pattern that will inevitably diverge when someone adds a field (e.g. `StreamingLatency`). The `objSeen` vs `seen` naming inconsistency within the duplicate blocks confirms this. |
| **Current State** | Duplicate code: ```go
chunkIDs := make([]int64, 0, len(hits))
objSeen := map[int64]struct{}{}
objIDs := make([]int64, 0, len(hits))
for _, h := range hits {
    chunkIDs = append(chunkIDs, h.ChunkID)
    if _, ok := objSeen[h.ObjectID]; !ok {
        objSeen[h.ObjectID] = struct{}{}
        objIDs = append(objIDs, h.ObjectID)
    }
}
pt, ct, tt := tokensFromUsage(resp.Usage)
cost := costMicros(pt, ct, c.promptMicrosPer1K, c.completionMicrosPer1K)
if err := c.repo.RecordUsage(ctx, repository.Usage{...}); err != nil { ... }
telemetry.RecordAIUsage(ctx, req.Tenant, resp.Model, pt, ct, cost)
``` |
| **Recommended State** | Extract a helper: `recordChatUsage(ctx, tenant, caller, query, hits, resp, latency)` that owns the entire usage-recording concern. |
| **Impact** | Two maintenance surfaces for the same logic. Adding a field means updating both copies, likely forgetting one. |
| **Effort** | S |

---

### 3. 🟧 HIGH: Agent lacks any logging — debugging blind spot

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | High |
| **Title** | No agent tool-call logging |
| **Location** | `internal/ai/agent.go` — entire file |
| **Description** | The `Agent` struct has a `logger` field but **never uses it**. `Run()` executes tool-call loops without any logging of tool dispatch, tool results, or errors. In production, when an agent behaves unexpectedly (e.g. infinite tool loops, wrong tool selection), there is zero observability into what happened. Compare with `Indexer` and `Chat` which properly use structured logging through their lifecycle. |
| **Current State** | Zero logging calls anywhere in `agent.go` despite having a `logger` field. |
| **Recommended State** | Log each tool call with its name, arguments, error status, and duration. Add step-level context: `log.Info("agent: tool call", "step", step, "tool", name, "args", args, "duration", dur)` and `log.Warn("agent: tool error", "tool", name, "err", result)` where appropriate. |
| **Impact** | Debugging agent misbehavior in production requires code-level tracing or guesswork. |
| **Effort** | S |

---

### 4. 🟧 HIGH: `context.Background()` in test fixtures

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | High |
| **Title** | Test helpers hard-code `context.Background()` |
| **Location** | `internal/ai/integration_test.go:27, 30, 51, 60, 79, 88` |
| **Description** | `newTestEnv`, `putObject`, and `seedChunks` all use `context.Background()` instead of `t.Context()` (Go 1.25 testing feature). This means: (1) test contexts cannot carry deadlines per test, (2) leaked goroutines are not cleaned up when a test times out, and (3) the pattern sets a bad precedent for future tests. |
| **Current State** | ```go
repo, err := repository.Open(context.Background(), "sqlite", ...)
...
store, err := storage.NewLocal(...)
svc := service.NewFileService(store, repo, nil)
``` |
| **Recommended State** | ```go
func newTestEnv(t *testing.T) *testEnv {
    t.Helper()
    ctx := t.Context() // Go 1.25
    repo, err := repository.Open(ctx, "sqlite", ...)
    ...
    return &testEnv{repo: repo, store: store, svc: svc}
}
``` |
| **Impact** | If a test times out, goroutines from dangling operations may leak. Test isolation is weaker. |
| **Effort** | M (large find-and-replace across many test functions) |

---

### 5. 🟨 MEDIUM: Dead/no-op code in `pii.go`

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | `strings.Repeat("0", 0)` is a no-op expression |
| **Location** | `internal/ai/pii.go:144` — `MapPII()` |
| **Description** | `strings.Repeat("0", 0)` always returns `""`. This is confusing dead code — appears to be a leftover from a copy/paste or partial refactor. It wastes reader mental cycles trying to understand what it does. |
| **Current State** | ```go
parts = append(parts, k+"="+strings.Repeat("0", 0)+itoa(v))
``` |
| **Recommended State** | ```go
parts = append(parts, k+"="+itoa(v))
``` |
| **Impact** | Confusing for new readers. No runtime impact. |
| **Effort** | S |

---

### 6. 🟨 MEDIUM: Hand-rolled insertion sort instead of stdlib

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | Custom O(n²) insertion sort duplicates stdlib |
| **Location** | `internal/ai/bm25.go:244-250` (`sortHitsDesc`), `internal/ai/search.go:231-234` (`rrfMerge` tail sort) |
| **Description** | Two instances of hand-written insertion sort (O(n²) worst-case) instead of using `sort.Slice` from the standard library. Standard library sort is O(n log n), well-tested, and more readable. Sorting `bm25Hit` slices is a hot path on every `BM25.Search()` call. |
| **Current State** | ```go
func sortHitsDesc(h []bm25Hit) {
    for i := 1; i < len(h); i++ {
        j := i
        for j > 0 && h[j].Score > h[j-1].Score {
            h[j], h[j-1] = h[j-1], h[j]
            j--
        }
    }
}
``` |
| **Recommended State** | ```go
func sortHitsDesc(h []bm25Hit) {
    sort.Slice(h, func(i, j int) bool { return h[i].Score > h[j].Score })
}
``` For the tiebreak case in `rrfMerge`: ```go
sort.SliceStable(merged, func(i, j int) bool {
    if merged[i].score != merged[j].score {
        return merged[i].score > merged[j].score
    }
    return merged[i].chunkID < merged[j].chunkID
})
``` |
| **Impact** | Perf regression on large result sets (O(n²) vs O(n log n)). Reduces code readability. Adds cognitive load for maintainers. |
| **Effort** | S |

---

### 7. 🟨 MEDIUM: Dead code in `search.go` — unused `GetObjectByID` call

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | `GetObjectByID` call with discarded result |
| **Location** | `internal/ai/search.go:197-200` — `searchLexical()` BM25 branch |
| **Description** | When no lexical index is configured (the in-memory BM25 path), the code fetches each object via `GetObjectByID` but only to assign `_ = ch` — the result is never used. This is dead code that makes an unnecessary SQL query per search hit in the BM25 path. |
| **Current State** | ```go
ch, _ := s.repo.GetObjectByID(ctx, h.Doc.objectID)
bm25Hits = append(bm25Hits, ranked{...})
_ = ch
``` |
| **Recommended State** | Remove both lines entirely. All the data needed for the `ranked` struct is already in `h.Doc`. |
| **Impact** | Each BM25 search makes N unnecessary SQL queries (where N = top K hits). Worsens search latency on the in-memory BM25 path. |
| **Effort** | S |

---

### 8. 🟨 MEDIUM: Custom `itoa` instead of `strconv.Itoa`

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | Unnecessary custom integer-to-string conversion |
| **Location** | `internal/ai/pii.go:149-159` — `itoa()` function |
| **Description** | The PII package defines its own `itoa` function (with a fixed 10-byte buffer) instead of using `strconv.Itoa` from the standard library. This is unnecessary, duplicates stdlib, and could have latent bugs (e.g. overflow for very large ints, though unlikely in practice). |
| **Current State** | ```go
func itoa(n int) string { ... } // 11 lines of custom code
``` |
| **Recommended State** | `strconv.Itoa(n)` — one function call, well-tested, standard. |
| **Impact** | Unnecessary maintenance burden. Reviewers waste time verifying correctness of custom stdlib replacement. |
| **Effort** | S |

---

### 9. 🟨 MEDIUM: `applyRerankOrTrim` has duplicate trim logic

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | Repeated trim-after-k pattern in both branches |
| **Location** | `internal/ai/search.go:260-274` — `applyRerankOrTrim()` |
| **Description** | The `if len(out) > k { return out[:k] }` pattern appears in both the reranker-success branch and the fallback branch. This is a code smell — the trim is an invariant that should apply once at the end. |
| **Current State** | ```go
if s.rerank != nil && len(out) > 0 {
    reranked, err := s.rerank.Rerank(ctx, query, out, k)
    if err == nil { return reranked }
    s.logger.Warn(...)
    if len(out) > k { return out[:k] }  // duplicate
    return out
}
if len(out) > k { return out[:k] }      // duplicate
return out
``` |
| **Recommended State** | ```go
func (s *Search) applyRerankOrTrim(ctx context.Context, query string, out []Hit, k int) []Hit {
    if s.rerank != nil && len(out) > 0 {
        if reranked, err := s.rerank.Rerank(ctx, query, out, k); err == nil {
            return reranked
        }
        s.logger.Warn("rerank failed; using raw order", "err", err)
    }
    if len(out) > k { out = out[:k] }
    return out
}
``` |
| **Impact** | Small — maintainability. If the trim logic changes (e.g. to preserve more results), both branches need updating. |
| **Effort** | S |

---

### 10. 🟩 LOW: `pgvector.go` and `qddrant.go` file sizes at boundary

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Low |
| **Title** | Qdrant adapter approaching file-size limit |
| **Location** | `internal/ai/qdrant.go` (355 lines), `internal/ai/pgvector.go` (184 lines) |
| **Description** | `qdrant.go` at 355 lines is approaching the 500-line limit. The file contains both collection management (EnsureCollection, scopeFilter) and CRUD operations (UpsertObjectChunks, DeleteObjectChunks) plus HTTP helpers. Consider splitting into `qdrant_collection.go`, `qdrant_crud.go`, and `qdrant_http.go` when it grows further. |
| **Effort** | L |

---

### 11. 🟩 LOW: `sink.go` interface lacks context documentation

| Field | Value |
|-------|-------|
| **Category** | Naming / Documentation |
| **Severity** | Low |
| **Title** | ChunkSink documentation could clarify error semantics |
| **Location** | `internal/ai/sink.go` |
| **Description** | The `ChunkSink` interface comment says implementations must be "safe for concurrent use" but doesn't specify whether `UpsertObjectChunks` should be retry-safe (idempotent) or what happens on partial failure. The indexer propagates sink errors as fatal — is that the contract expectation? |
| **Effort** | S |

---

### 12. 🟩 LOW: `lexicalindex.go` — SQL injection risk from table name

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Format-based SQL with table name could enable injection |
| **Location** | `internal/ai/lexicalindex.go:100` — `SearchLexical()` |
| **Description** | The table name is injected via `fmt.Sprintf` into the SQL query. While `PgFTSOptions.Table` defaults to `"chunks"` and is set by the operator, a future feature that dynamically sets this could enable SQL injection. Should use parameterized table names (or document that table is not user-controllable). |
| **Current State** | ```go
q := fmt.Sprintf(`SELECT ... FROM %[2]s WHERE ...`, p.tsLang, p.table)
``` |
| **Recommended State** | Document that `Table` is not user-controllable, or validate it against a whitelist. |
| **Impact** | Low risk in current usage, but a future change could introduce injection. |
| **Effort** | S |

---

### 13. 🟩 LOW: SSE protocol parsing doesn't validate all edge cases

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | SSE scanner skips non-`data:` lines silently |
| **Location** | `internal/ai/llm.go:205-230` — `sseScanner` |
| **Description** | The SSE scanner silently drops lines that don't start with `data:` or `:`. This means protocol errors (e.g. a malformed `event:` or `id:` line from a non-standard provider) are silently dropped instead of being logged. While this works for OpenAI-compatible providers, it makes debugging provider compatibility harder. |
| **Current State** | ```go
if line == "" { continue }
if strings.HasPrefix(line, ":") { continue }
const prefix = "data:"
if strings.HasPrefix(line, prefix) { ... }
// else: silently dropped
``` |
| **Recommended State** | Log unexpected SSE fields at debug level: `slog.Debug("unexpected SSE line", "line", line[:min(60, len(line))])` |
| **Impact** | Low — works with all standard providers. Only surfaces during provider integration. |
| **Effort** | S |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| `context.Background()` in indexer telemetry | High | S | **P0** | Breaks observability trace correlation |
| Duplicate RecordUsage in `chat.go` | Medium | S | P1 | 15 lines × 2, will diverge |
| Agent has no logging | High | S | P1 | Production blind spot |
| `context.Background()` in test fixtures | Medium | M | P1 | Test isolation |
| Dead/no-op `strings.Repeat("0", 0)` in `pii.go` | Low | S | P2 | Confusing dead code |
| Custom insertion sort × 2 | Medium | S | P2 | O(n²) on hot path |
| Dead `GetObjectByID` call in search | Medium | S | P2 | Unnecessary SQL per hit |
| Custom `itoa` instead of `strconv.Itoa` | Low | S | P3 | Unnecessary |
| Duplicate trim logic in `applyRerankOrTrim` | Low | S | P3 | Code smell |
| SSE scanner silently drops non-data lines | Low | S | P3 | Debugging friction |

---

## Quick Wins (S-effort improvements)

1. **Fix `context.Background()` → propagate context.** The `handleExtractError` signature change is trivial and fixes observability. **P0.**
2. **Add agent logging.** The `logger` field already exists — just add structured log calls for each tool dispatch and step. **P1.**
3. **Remove dead `_ = ch` line.** Delete the unnecessary `GetObjectByID` call. **P2.**
4. **Replace custom sorts with `sort.Slice`.** Both insertion sorts can be replaced in <10 lines total. **P2.**
5. **Remove `strings.Repeat("0", 0)`.** Delete the no-op. **P2.**
6. **Replace `itoa` with `strconv.Itoa`.** One-line change. **P3.**

---

## Overall Assessment

| Dimension | Rating |
|-----------|--------|
| **Overall Code Quality** | **Good** — but with notable issues |
| **Architecture** | Excellent. Interface seams (`ChunkSink`, `VectorIndex`, `LexicalIndex`, `Reranker`, `Extractor`, `Embedder`) are well-designed. Clean separation of concerns between indexer, search, chat, and agent. |
| **Test Quality** | **Good** — 84% coverage with well-structured test fixtures (`testEnv`, `fakeSink`). Integration tests exist for all major paths. Some tests still use `context.Background()` which should be `t.Context()`. |
| **Error Handling** | **Needs Work** — `context.Background()` leak in telemetry breaks observability. Agent doesn't log tool errors. |
| **Logging** | **Good for indexer/chat, missing for agent.** Indexer and Chat use structured logging properly with context. Agent has zero logging despite having a `logger` field. |
| **Technical Debt** | **Manageable.** ~13 findings, mostly S-effort. No showstoppers. The custom insertion sorts and dead code are easy to clean up. |
| **Maintainability** | **Good overall.** The interface-based design makes swapping backends trivial. The duplicate code in `chat.go` RecordUsage is the biggest long-term risk. |

### Recommendations for Sprint Backlog

1. **P0: Fix context propagation** in `handleExtractError` — critical for observability.
2. **P1: Add agent logging** — production debugging blocker.
3. **P1: Extract RecordUsage helper** in `chat.go` — before the next AI usage field is added.
4. **P1: Replace test `context.Background()` with `t.Context()`** — test hygiene.
5. **P2: Remove dead code** (`GetObjectByID`, `strings.Repeat`, custom `itoa`, custom sorts).

The subsystem is well-engineered overall with strong architecture and solid test coverage. The issues found are mostly localized and cheap to fix.
