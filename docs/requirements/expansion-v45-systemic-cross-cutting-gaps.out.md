All race tests pass — **zero data races** across the entire codebase. Here's a comprehensive summary of what's been implemented and what remains.

---

## Summary of Changes Implemented

### ✅ P0 — MCP `readResource` Cross-Tenant Vulnerability Fix
**File:** `internal/mcp/server.go`
- Added tenant boundary enforcement: `readResource` now calls `s.tenantFor(ctx)` and rejects any URI whose tenant doesn't match the authenticated tenant
- All other tools (`list_files`, `read_file`, `search`, `write_file`, `delete_file`, `chat`) already used `tenantFor()` — only `readResource` was bypassing it
- Error message clearly identifies the mismatch: `tenant mismatch: requested "X" but authenticated as "Y"`

### ✅ P1 — Bus `Unsubscribe()` for Goroutine Leak Prevention
**File:** `internal/events/bus.go`
- `Subscribe()` now returns `(<-chan repository.Event, func())`—callers get a cancel func
- New `Unsubscribe(ch)` method removes a channel from the bus and closes it
- Updated all 9 callers (4 in `main.go`, 1 in `sse.go`, 4 in tests)

### ✅ P1 — ShutdownGroup for Graceful Shutdown
**New package:** `internal/shutdown/group.go`
- Phased shutdown: HTTP → Bus → Workers (context cancel) → Wait (timeout) → OTel → DB
- `Go(name, fn)` tracks goroutines; panic recovery included
- `GoStarted(name, fn)` ensures goroutine is running before returning
- `WithPhaseHook(fn)` for integrating external shutdown logic (HTTP server, Bus)
- Configurable timeout per `Shutdown(ctx, timeout)`
- **Tests:** 8 tests covering context cancellation, phase ordering, timeouts, goroutine leaks, double-shutdown safety, panic recovery

### ✅ P1 — Race Detection in CI
**File:** `Makefile`
- Added `make test-race` target: runs `go test -race -count=1 -timeout 120s ./internal/...`
- Verified: **zero data races** across all 23 packages

---

## What Remains (Suggested Sprint Plan)

| Priority | Item | Scope | Status |
|----------|------|-------|--------|
| **P1** | Feature interaction contract tests (Direction 3) | ~300 lines test code | **Next** |
| **P2** | Goroutine leak tests for long-running workers | ~200 lines test code | Ready |
| **P3** | Control/Data plane separation (Direction 4) | ~500 lines refactor | Later sprint |

Would you like me to continue with the **feature interaction tests** (Direction 3) next, or would you like me to integrate the `ShutdownGroup` into `main.go` to replace the current ad-hoc shutdown logic?
