# Requirements Specification — `internal/mcp`: tests for the tenant-boundary rejection branch and the fail-closed delete gate

**Module:** `internal/mcp` (test-only; zero production-code changes)
**Direction:** "Add missing tests for the two security-critical branches: cross-tenant resources/read rejection and fail-closed delete"
**Source analysis:** `docs/auto/analyses/internal-mcp-63e779eb.json` (direction 1 of 3; directions 2–3 — pagination, usage ledger — are explicitly **out of scope**)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 8 / effort 2 / confidence 9

---

## 1. Scope

`internal/mcp` is the MCP protocol adapter (AGENTS.md §2.2): `resources/read` accepts client-supplied `aero-vault://{tenant}/{bucket}/{key}` URIs, and the tenant-boundary check in `readResource` (`internal/mcp/server.go:370-376`) is the **only** defense against cross-tenant data exfiltration via crafted URIs. Separately, `FileService.authorize` fails **closed** for `ActionDelete` when no authorizer is configured (`internal/service/access.go:91-101`), and the MCP `delete_file` tool is the surface that must exercise that gate. Both branches have **zero test coverage today**:

- `TestReadResource_*` (`internal/mcp/server_test.go:578-648`) covers success/prefix/short-URI/not-found/invalid-params only; `TestReadResource_Success` runs with `context.Background()` (no tenant middleware context), so the `uriTenant != allowedTenant` branch is dead code.
- `internal/mcp/helpers_test.go:40-43` claims the no-authorizer delete denial "is covered by `TestCallTool_DeleteFile_FailClosed` in server_test.go" — **no such test exists** (verified by grep over the whole package). Every test `Server` is built on a `FileService` injected with `allowAllProvider` (helpers_test.go:30-35), so the fail-closed gate is never reached through the MCP surface.

This spec scopes exactly: **(1)** `TestReadResource_TenantMismatch`, **(2)** `TestReadResource_TenantScoped`, **(3)** `TestCallTool_DeleteFile_FailClosed` (with the exact names demanded by the acceptance), plus the minimal same-package test plumbing they require (a context-carrying `handle` variant and a no-authorizer server builder) and the helpers_test.go comment-truthfulness fix. **No production files change** — the two branches already behave per the acceptance (verified in §2); the gap is coverage, not behavior.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `internal/mcp/server.go:371-373` — tenant-boundary check | `readResource` at **`:352`**; check at **`:370-376`**: `uriTenant := parts[0]` (:370), `allowedTenant := s.tenantFor(ctx)` (:371), `if uriTenant != allowedTenant {` (:372), `return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("tenant mismatch: requested %q but authenticated as %q", …)}` (:373-376). The check precedes **any** storage access (`s.svc.Get` at :377) | ✅ **exact** |
| E2 | `internal/mcp/server_test.go:578-648` — `TestReadResource_*` coverage | `TestReadResource_Success` **:578**, `_BadURIPrefix` **:611**, `_ShortURI` **:620**, `_NotFound` **:630**, `_InvalidParams` **:639** — exactly the five claimed; none exercises the mismatch branch; all go through `handle`/`handleResult` which hardcode `context.Background()` (`server_test.go:20-43`) | ✅ **exact** |
| E3 | `internal/mcp/helpers_test.go:42` — comment claiming `TestCallTool_DeleteFile_FailClosed` exists | Comment at **`:40-43`** ("The default-config (no authorizer) delete denial is covered by `TestCallTool_DeleteFile_FailClosed` in server_test.go."); `grep -rn "TestCallTool_DeleteFile_FailClosed\|FailClosed" internal/mcp/` finds **only the comment** — the test does not exist | ✅ **exact — the claim is false** |
| E4 | `internal/mcp/helpers_test.go:30-32` — every test injects `allowAllProvider` | `type allowAllProvider struct{}` at **`:30`**, `Authorize` at **`:32`**; injected by `newTestServer` (`:35`, `.WithAuthorizer(allowAllProvider{})`). Verified exhaustively: every test `Server` in the package is built on that svc — incl. re-built search servers (`server_test.go:495-498, 521-523`) and `TestNewServer_EmptyTenantFallback` (`:723`) | ✅ **exact** |
| E5 | `internal/service/access.go:83-92` — fail-closed delete gate | `authorize` at **`:83-101`**: `requireActiveTenant` (:88), `if s.authorizer == nil {` (**:91**), `if action == access.ActionDelete && !s.deleteFailOpen {` (**:92**), AV-exempt principal check (:97-99), denial `return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)` (**:100-101**). `ErrForbidden = errors.New("forbidden")` at `file.go:35`. `svc.Delete` → `authorizeObject(ctx, access.ActionDelete, obj)` at `file_delete.go:159` — the gate is on the MCP `delete_file` path | ✅ **symbol exact; range conservative** — the cited :83-92 covers the header + tenant pre-check + gate condition; the denial return sits at :100-101 (corrected for the design stage) |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "`TestReadResource_*` covers prefix/short-URI/not-found/invalid-params only; the mismatch path is dead code with zero coverage" | ✅ **holds** (E2). Branch coverage: `server.go:372` is never taken by any test |
| "`TestReadResource_Success` runs with no tenant middleware context" | ✅ **holds** — `handleResult` → `srv.Handle(context.Background(), …)` (`server_test.go:34-43`) |
| "`helpers_test.go:42` claims coverage by a test that does not exist anywhere in the package" | ✅ **holds** (E3) |
| "Every MCP test injects `allowAllProvider`, so the fail-closed delete gate is never exercised through the MCP surface" | ✅ **holds** (E4). No test builds a `FileService` without `.WithAuthorizer(...)`; the `nil`-authorizer + `ActionDelete` branch at `access.go:91-101` is unreached from `internal/mcp` |
| "Both branches are exactly the ones a regression would silently ship in" | ✅ **holds** — a change that removed the mismatch check or made the gate fail-open would pass `go test ./internal/mcp/` today |

**New evidence found beyond the direction's citations (each shaped a requirement):**

1. **`ctxTenantID` is unexported** (`internal/middleware/middleware.go:14-20`); the only sanctioned way to put a request-scoped tenant in a context is the `mw.Tenant` HTTP middleware reading `X-Aero-Tenant` (`middleware.go:35,46-48`; `TenantWithStatus` at `tenant_status.go:15`). In-package precedent: `TestTenantForHonorsExplicitDefaultContext` (`server_regression_test.go:14-27`) — `mw.Tenant(http.HandlerFunc(...))` + `httptest.NewRequest` + `Header.Set(mw.TenantHeader, …)`, capturing `r.Context()` inside the handler. This is the "set via mw.Tenant" mechanism the acceptance requires.
2. **`handle`/`handleResult` hardcode `context.Background()`** (`server_test.go:20-43`) — no context-carrying variant exists; the two new `resources/read` tests need one. Refactoring `handle` to delegate to a new `handleCtx(t, srv, ctx, body)` keeps the decode logic single-sourced.
3. **The delete denial surfaces as a JSON-RPC *result*, not an rpc error**: `toolDeleteFile` (`server.go:306-314`) maps `svc.Delete` errors via `errResult` → `toolResult{Content:[{type:text, text:err.Error()}], IsError:true}` (`server.go:401-407`, `protocol.go:56-60`). The fail-closed assertion must decode `toolResult` from `resp.Result` (mirroring `TestCallTool_DeleteFile_*`, `server_test.go:384-441`) and expect `resp.Error == nil`.
4. **A no-authorizer stack is one line away from the existing helper**: `NewFileService(store, repo, nil)` leaves `authorizer` nil (`file.go:130-134`); `deleteFailOpen` zero value = fail-closed (`file.go:104-111`); `requireActiveTenant` passes under default config (`tenantStatus=false`, `access.go:117-119`); a plain `context.Background()` carries no principal, so the AV quarantine exemption (`access.go:97-99`) cannot fire. The gate deterministically returns `ErrForbidden` → message text **`forbidden: no authorization provider configured`**.
5. **`seedObject` supports arbitrary tenants** (`helpers_test.go:51-58`), so the TenantScoped test can seed under `"acme"` without new plumbing.
6. **`readResource` rejects before touching storage** (E1) — the mismatch/rejection legs need no cross-tenant seed data at all; only the success leg of TenantScoped needs the `"acme"` object.

---

## 3. Requirements

All changes are in `internal/mcp/*_test.go` + `internal/mcp/helpers_test.go`; **no production file is touched** (`internal/mcp/server.go`, `internal/service/access.go` stay byte-identical). The 500-line hard gate exempts `*_test.go` (Makefile:162) — the test files may grow.

### REQ-1 — `TestReadResource_TenantMismatch` (tenant-boundary rejection branch)

New test in `internal/mcp/server_test.go`, after `TestReadResource_InvalidParams` (:639-648), in the existing `// ---- resources/read ----` block.

- Build the request-scoped context with the in-package `mw.Tenant` precedent (`server_regression_test.go:17-23`): handler = `mw.Tenant(http.HandlerFunc(...))`, `httptest.NewRequest(http.MethodPost, "/mcp", nil)`, `Header.Set(mw.TenantHeader, "default")`, capture `request.Context()`.
- Send via the new `handleCtx` helper (REQ-4): `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"aero-vault://other/default/secret.txt"}}` — URI tenant `"other"` ≠ context tenant `"default"`.
- Assert: `resp.Error != nil`; `resp.Error.Code == -32000`; `strings.Contains(resp.Error.Message, "tenant mismatch")`.
- No seeding required (the check precedes storage access).

### REQ-2 — `TestReadResource_TenantScoped` (scoped success + default-URI rejection)

New test in `internal/mcp/server_test.go`, same block, adjacent to REQ-1.

1. `srv, svc, _ := newTestServer(t, nil)`; `seedObject(t, svc, "acme", "default", "doc.txt", "acme content")`.
2. Request-scoped context with tenant **`"acme"`** (same `mw.Tenant` + `X-Aero-Tenant: acme` pattern).
3. **Success leg**: `uri = "aero-vault://acme/default/doc.txt"` via `handleCtx` → `resp.Error == nil`; unmarshal `readResourceResult`; `len(Contents) == 1`; `Contents[0].Text == "acme content"`; `Contents[0].URI == uri`.
4. **Rejection leg**: `uri = "aero-vault://default/default/doc.txt"` via `handleCtx` → `resp.Error.Code == -32000` and message contains `"tenant mismatch"` (the default-tenant URI must be rejected even though the server default is `"default"` — the ctx tenant wins per `tenantFor`, `server.go:46-51`).

### REQ-3 — `TestCallTool_DeleteFile_FailClosed` (fail-closed delete gate via the MCP surface) + comment truthfulness

New test in `internal/mcp/server_test.go`, after `TestCallTool_DeleteFile_NotFound` (:429), in the existing delete-tool block. **The test must carry the exact name claimed by the comment** so `helpers_test.go:40-43` becomes literally true.

1. Build a server **without an authorizer** via the new builder (REQ-4) — this is the first test in the package to do so.
2. `seedObject(t, svc, "default", "default", "keep.txt", "must survive")`.
3. `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{"key":"keep.txt"}}}` via `handleCtx` with `context.Background()` (server default tenant suffices).
4. Assert **denial as a tool result**: `resp.Error == nil` (JSON-RPC level succeeds); unmarshal `toolResult` from `resp.Result`; `result.IsError == true`; `strings.Contains(result.Content[0].Text, "forbidden")` and `strings.Contains(result.Content[0].Text, "no authorization provider configured")` — pinning `ErrForbidden` (message `"forbidden"`, `file.go:35`) and the exact gate message (`access.go:100`).
5. Assert **no side effect**: a follow-up `read_file` for `keep.txt` returns a non-error result (read is not gated — `access.go:102` allows non-delete actions with nil authorizer) with the original content — proving the delete was *denied*, not performed.
6. **Comment**: `helpers_test.go:40-43` must not reference a nonexistent test. Satisfied by the test's existence at the claimed name+location; additionally reword the comment to state the test's actual purpose (e.g. "The default-config (no authorizer) delete denial is exercised by TestCallTool_DeleteFile_FailClosed below in server_test.go") so the claim cannot rot again if the test is ever moved.

### REQ-4 — Minimal same-package test plumbing

- **`handleCtx`** in `internal/mcp/helpers_test.go`: `func handleCtx(t *testing.T, srv *Server, ctx context.Context, body string) rpcResponse` — the body of today's `handle` with `context.Background()` replaced by `ctx`. Refactor `handle` to delegate: `return handleCtx(t, srv, context.Background(), body)` (single decode path; no behavioral change to the ~40 existing call sites).
- **No-authorizer builder** in `internal/mcp/helpers_test.go`: `func newTestServerNoAuthorizer(t *testing.T, search *ai.Search) (*Server, *service.FileService, repository.Repository)` — byte-for-byte the `newTestServer` body (`:19-35`) **minus** the `.WithAuthorizer(allowAllProvider{})` line, with a doc comment stating it exercises the fail-closed gate (FR-1, `access.go:91-101`) and is only for the delete-gate test.
- **Imports** added to `internal/mcp/server_test.go`: `net/http`, `net/http/httptest`, `mw "github.com/aero-vault/aero-vault/internal/middleware"`. `helpers_test.go` needs no new imports (`context`, `service`, `repository`, `storage`, `ai` already present).
- Constraints: gofmt-clean; no new exported symbols (test package `mcp`); no changes to `server.go`, `protocol.go`, `transport.go`, or any non-test file.

---

## 4. Decisions & non-goals

- **D1 — No production changes.** Both branches were traced end-to-end and behave exactly as the acceptance expects (E1: `-32000` + `"tenant mismatch"`; E5: `ErrForbidden` wrapped `"no authorization provider configured"`). The direction's problem is *coverage*, so REQ-1…3 are pure test additions — any design-stage impulse to "harden" or restructure `readResource`/`authorize` is out of scope.
- **D2 — Test placement in `server_test.go`.** Co-locates with the `resources/read` block (:578-648) and the delete-tool block (:384-441), and makes the `helpers_test.go:40-43` claim ("in server_test.go") true without a location edit. `server_regression_test.go` remains the reference for the `mw.Tenant` ctx pattern (its `TestTenantForHonorsExplicitDefaultContext` is copied, not moved).
- **D3 — `handleCtx` over inline duplication.** Three new tests need a context-carrying `Handle`; factoring `handle` to delegate keeps the rpcResponse decode in one place and costs zero churn to existing callers.
- **D4 — Separate builder over parameterizing `newTestServer`.** `newTestServer` has ~55 call sites; adding an options argument would churn every one. A 16-line sibling builder is cheaper and self-documenting.
- **Non-goals:** directions 2 (cursor pagination for `resources/list`/`list_files`) and 3 (usage-ledger rows for `resources/read`) from `internal-mcp-63e779eb.json` are **not** included; no changes to `server.go`/`access.go`/`protocol.go`; no new tool/resource surface; no golden/JSON fixtures; no `openapi.json` impact (test-only, per AGENTS.md §3 the OpenAPI sync rule applies to REST routes, not MCP tests).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

The three supplied acceptance checks map 1:1; each is a deterministic command plus concrete assertions.

**AC-1 — `TestReadResource_TenantMismatch` (REQ-1).**
*Testable:* `go test ./internal/mcp/ -run '^TestReadResource_TenantMismatch$' -count=1` passes. Setup: request-scoped ctx tenant `"default"` via `mw.Tenant` + `X-Aero-Tenant: default`; URI `aero-vault://other/default/secret.txt`. Assert: `resp.Error.Code == -32000` **and** `strings.Contains(resp.Error.Message, "tenant mismatch")`. (Fails on any change that removes or bypasses the `server.go:372` check.)

**AC-2 — `TestReadResource_TenantScoped` (REQ-2).**
*Testable:* `go test ./internal/mcp/ -run '^TestReadResource_TenantScoped$' -count=1` passes. Setup: seed `acme/default/doc.txt` = `"acme content"`; ctx tenant `"acme"`. Assert success leg (`Contents[0].Text == "acme content"`, `Contents[0].URI` echo) **and** rejection leg (`aero-vault://default/default/doc.txt` → `-32000`, `"tenant mismatch"`). Guards `tenantFor` ctx-over-default precedence (`server.go:46-51`).

**AC-3 — `TestCallTool_DeleteFile_FailClosed` + comment (REQ-3).**
*Testable:* `go test ./internal/mcp/ -run '^TestCallTool_DeleteFile_FailClosed$' -count=1` passes. Setup: `newTestServerNoAuthorizer`; seed `default/default/keep.txt`. Assert: tool result with `IsError == true`, text containing `"forbidden"` and `"no authorization provider configured"`; follow-up `read_file` succeeds with the original content (no side effect). And: `helpers_test.go:40-43` no longer references a nonexistent test — the comment is either true (test exists at the claimed name/location) or reworded.

**Completion gate (all ACs):** `go test ./internal/mcp/ -count=1` — the three new tests plus the full existing package suite (no regressions); `gofmt -l internal/mcp` empty. Both are subsets of the `make check` gate; the 500-line rule is unaffected (test files exempt, Makefile:162).

---

## 6. Risks

- **Weak assertions passing vacuously.** Mitigated by exact assertions in REQ-1…3: pin the `-32000` code, the `"tenant mismatch"` substring, `IsError == true`, and both message substrings; the TenantScoped success leg asserts actual content, not just "no error".
- **Comment/claim drift (the original defect).** The `helpers_test.go:40-43` comment asserted coverage that did not exist. REQ-3 step 6 makes the claim true at the exact name+location, so a future grep of `TestCallTool_DeleteFile_FailClosed` resolves to a real test; reworded wording additionally states the test's purpose.
- **`mw.Tenant` ctx pattern fragility.** The ctx key is unexported; the tests depend on the middleware contract (`TenantFromContext`, `middleware.go:60-63`). If the middleware ever changes its context key, `tenantFor` and the new tests break together — which is the point (they pin the cross-tenant boundary contract, AGENTS.md §2.2 "跨租户 URI 拒绝").
- **Scope creep from the sibling directions.** The analysis file contains two more directions on the same module; the implement stage must not fold pagination or usage-ledger work into this change. Non-goals in §4 bind.
- **Fail-closed gate semantics drift.** The test pins `access.go:91-101` behavior (nil authorizer + delete → `ErrForbidden`) through the MCP surface only; the AV-quarantine exemption (`access.go:97-99`) and `WithDeleteFailOpen` escape hatch (`file.go:104-111`) remain covered by `internal/service` tests and are deliberately **not** re-tested here (no scope expansion).

*Verification basis: all cited line numbers re-confirmed on this checkout (`acfaaf4`); the denial message text `forbidden: no authorization provider configured` derived from `ErrForbidden = errors.New("forbidden")` (`file.go:35`) + `fmt.Errorf("%w: no authorization provider configured", …)` (`access.go:100`).*
