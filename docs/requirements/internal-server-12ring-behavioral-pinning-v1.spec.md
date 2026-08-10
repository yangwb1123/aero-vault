# Requirements Specification — `internal/server`: behaviorally pin all 12 rings in the assembled chain

**Module:** `internal/server`
**Direction:** "Behaviorally pin all 12 rings in the assembled chain, not just names/order" (direction 1)
**Source analysis:** `docs/auto/analyses/internal-server-c7d0225e.json`
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 7 / effort 3 / confidence 9

---

## 1. Scope

`internal/server` exists so that production (`cmd/server/main.go`) and the integration harness share one assembled 12-ring chain (FR-1/AC-1 of `docs/requirements/internal-integration-harness-12ring-chain-v1.md`; package doc `internal/server/chain.go:1-10`). Today the module's own tests pin only the chain's **shape** — 12 names in pinned order, non-nil middleware (`TestBuildChain_12RingsInOrder`, `chain_test.go:64-85`) — and the **zero-config pass-through** (418 + `X-Content-Type-Options` + `X-Request-ID`, `TestApplyMiddleware_PassesThrough`, `chain_test.go:119-140`, with `nil` rl and `nil` corsProvider). No test executes a *rejecting* request through `ApplyMiddleware`: a regression that makes the rate-limit ring reject all traffic, the tenant ring admit disabled tenants, the max-body ring reject all bodies, the recoverer swallow panics, or the concurrency ring never throttle would keep `go test ./internal/server/` green. The integration harness explicitly notes the default config leaves MaxBodySize/CORS inert (`fullserver_test.go:78-83`), so per-ring rejection behavior is pinned nowhere at assembly level.

This spec adds **four tests in `internal/server/chain_test.go`** (same package, reusing the existing `newTestChain` helper) that behaviorally pin six rings through `ApplyMiddleware` — the exact assembly point where a wiring regression would otherwise be invisible:

| Ring | Behavior pinned |
|---|---|
| `rate_limit` | 429 after burst exhaustion; `/healthz` bypass |
| `tenant` | 403 `TenantDisabled` for a `status=disabled` tenant row |
| `max_body` | 413 on `Content-Length` > cap; over-cap streamed body surfaces `ErrBodyTooLarge` |
| `recoverer` | panic in innermost handler → 500 |
| `concurrency` | 429 + `Retry-After` when the weighted semaphore is full |

**Test-only change: zero production code is modified.** Out of scope (see §4): CORS-preflight pinning, OTel/BucketCORS/AccessLog behavior, `internal/integration` harness changes, ring order/config changes.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `internal/server/chain_test.go:56-100` | `TestBuildChain_12RingsInOrder` at `:64-85` (len==12 `:71-73`, order `:74-79`, non-nil `:80-84`); `TestBuildChain_Idempotent` `:87-101` | ✅ **exact.** Shape assertions only — no request is executed. |
| E2 | `internal/server/chain_test.go:114-140` | `TestApplyMiddleware_PassesThrough` `:119-140`: asserts 418 (`:131-133`), `X-Content-Type-Options` (`:134-136`), `X-Request-ID` (`:137-139`); call site `:126` passes `nil` rl and `nil` corsProvider | ✅ **exact.** Behavior of 2 of 12 rings; `nil` rl → pass-through (`ratelimit.go:142-144`), `nil` provider → pass-through (`cors_bucket.go:148-150`). |
| E3 | `internal/server/chain_test.go:22-36` | `wantRings` order contract at `:22-35` | ✅ **exact.** |
| E4 | `internal/integration/fullserver_test.go:81-83` | "rings that the default config leaves inert (MaxBodySize, CORS)" at `:78-80`; "cfg required — a nil config would nil-dereference inside server.ApplyMiddleware" `:81-82`; func def `:83` | ✅ present, **minor drift**: `:81-83` holds the cfg-required note; the inert claim sits at `:78-80`. Substance holds — `startFullServerOpts` passes `&config.Config{}` (`:75-77`). |
| E5 | `internal/server/chain.go:36-69` | `ChainLink` `:42-45`; `BuildChain` `:58-89` (nil-concurrencyMW panic `:61-63`; tenant-status closure over `repo.GetTenant` `:64-67`; 12-ring table `:68-87`); `ApplyMiddleware` `:92-99` | ✅ **present** (cited range covers `ChainLink` + BuildChain head; body extends to `:89`). |
| E6 | `internal/middleware/ratelimit.go:141-160` | `RateLimiter.Middleware()` `:141-160`: `nil`→pass-through `:142-144`; `rateLimitBypass` incl. `/healthz` `:148-151`; 429 `http.Error` `:152-156`; `Retry-After` via `writeRateLimitHeaders` `:126-128` | ✅ **exact.** `NewRateLimiter(1,1)` is valid (non-nil, `:38-43`). |
| E7 | `internal/middleware/validation.go:15-40` | `ErrBodyTooLarge` `:15`; `limitErrReader` type `:17-30`; `Read` cap+peek logic `:32-40`, sentinel return at `:44` | ✅ **exact.** **Nuance:** `MaxBodySize`'s doc comment (`:48-49`) says the body is "wrapped with io.LimitReader", but the code wraps with `limitErrReader` (`:63-66`): the cap does **not** silently truncate — an over-cap read returns `ErrBodyTooLarge` so adapters can reject with 413 instead of storing a truncated object. The direction's "truncates streamed bodies via LimitReader" therefore tests as: ≤cap reads succeed, over-cap read surfaces the sentinel, exactly-at-cap yields clean EOF (`:21-30`). |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Its own tests only verify ring names/order/non-nil and 2 of 12 rings' behavior (X-Content-Type-Options, X-Request-ID)" | ✅ **holds** — E1/E2/E3; `PassesThrough` is the zero-config shape (`nil` rl, `nil` corsProvider, `chain_test.go:126`). |
| "A regression that makes rate-limit reject all traffic / auth bypass everything / max-body reject all bodies keeps the suite green" | ✅ **holds** — no test in `internal/server` executes a rejecting request through the assembled chain. The middleware unit tests (`ratelimit_test.go`, `validation_test.go`, `cors_test.go`) exercise rings in isolation, so a *wiring* regression in `BuildChain`/`ApplyMiddleware` (wrong ring, wrong config plumbed, wrong wrap order) is invisible. |
| "Default config leaves MaxBodySize/CORS rings inert; per-ring rejection behavior nowhere pinned at assembly level" | ✅ **holds** — E4; `startFullServerOpts` passes `&config.Config{}` (`fullserver_test.go:75-77`); `tenant`/`recoverer`/`concurrency`/`rate_limit` rejection behavior is likewise unpinned in `internal/server`. |

**Supplementary verification for the acceptance mechanics** (beyond the direction's citations): `TenantWithStatus` disabled → 403 `TenantDisabled` JSON at `tenant_status.go:33-36` + `writeTenantStatusError` `:43-50`; unknown tenants stay allowed (back-compat) `:39-42`; `UpsertTenant` persists `Status` verbatim and defaults only empty→`"active"` (`tenants.go:11-29`); `Recoverer` → 500 `"internal server error"` (`middleware.go:73-93`); `ConcurrencyLimiter` weighted acquire (GET = 1) → 429 `"too many concurrent requests"` + `Retry-After: 1` (`middleware.go:124-151`, `rejectConcurrency` `:295-298`); `config.AppConfig.MaxBodySize int` (`config.go:48`), converted `int64(...)` at `chain.go:68`; `telemetry.HTTPMiddleware`/`WithMiddlewareTiming` run safely on the default no-op OTel provider (no global state in tests). Execution order of the assembled chain matches AGENTS.md §2.5 (request_id outermost → access_log innermost): `tenant` (index 5) runs **before** `rate_limit` (index 4), so a disabled-tenant request is rejected by the tenant ring before the rate-limit ring, and a header-less request's rate-limit bucket key is `"default"` (`TenantFrom`, `ratelimit.go:117-122`). Baseline confirmed: `go test ./internal/server/ -count=1` → `ok` before this change.

---

## 3. Requirements

All four tests live in `internal/server/chain_test.go` (same package; reuse `newTestChain(t)` → SQLite repo + migrated schema + disabled auth registry + discard logger, `chain_test.go:39-54`) and every request goes through `ApplyMiddleware` — the same assembly point production uses. **No production file changes.**

### REQ-1 — Rate-limit ring: 429 after burst, `/healthz` bypass (`TestChain_RateLimit_429AndHealthzBypass`)

- `rl := middleware.NewRateLimiter(1, 1)` (valid, non-nil: `ratelimit.go:38-43`); `ctx, cancel := context.WithCancel(context.Background())`; `rl.Start(ctx)` (per the acceptance; returns immediately, `ratelimit.go:57-61`); `t.Cleanup(cancel)`.
- `handler := ApplyMiddleware(inner200, repo, authReg, rl, cfg, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)` — all other rings at zero-config, so only the rate-limit ring can reject.
- Request sequence (same recorder pattern as `TestApplyMiddleware_PassesThrough`):
  1. `GET /v1/files` → **200** — consumes the single burst token for the `"default"` bucket.
  2. `GET /v1/files` immediately after → **429**, body `rate limit exceeded`, `Retry-After` header present. Deterministic without sleeps: burst=1, rps=1, back-to-back requests (µs apart) cannot refill the token (1 s refill). Assert header *presence*, not the exact value (`int(wait.Seconds())+1` = `"2"` at `ratelimit.go:126-128`), for robustness.
  3. `GET /healthz` after exhaustion → **200** via `rateLimitBypass` (`ratelimit.go:148-151`); the inner handler's status proves the request reached the innermost ring — the bypass is end-to-end, not just "no 429".

### REQ-2 — Tenant ring: disabled tenant → 403 through the assembled chain (`TestChain_Tenant_Disabled403`)

- Seed: `repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "acme", Status: "disabled"})` (`tenants.go:11-29`).
- `handler := ApplyMiddleware(inner200, repo, authReg, nil, cfg, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)`.
- `GET /v1/files` with `X-Aero-Tenant: acme` → **403**, `Content-Type: application/json`, body contains `"code":"TenantDisabled"` (`tenant_status.go:33-36,43-50`). The tenant ring runs before `rate_limit`/`otel`/`recoverer`, so the rejection is observable regardless of the other rings' config.
- Negative control (same chain): `X-Aero-Tenant: ghost` (no row) → **200** — pins that the 403 comes from the disabled row, not blanket rejection (unknown tenants remain allowed, `tenant_status.go:39-42`).

### REQ-3 — Max-body ring: 413 + over-cap stream sentinel (`TestChain_MaxBody_413AndStreamCap`)

- `cfg := &config.Config{App: config.AppConfig{MaxBodySize: 10}}` (`config.go:48`, `int64` conversion at `chain.go:68`); everything else zero-config.
- **Content-Length branch:** `POST /v1/files` with `Content-Length: 11` (11-byte body) → **413**, `Connection: close`, rejected before any body read (`validation.go:70-77`).
- **Streamed branch:** `POST /v1/files` with `ContentLength: -1` (unknown/chunked) and a 13-byte body; the inner handler runs `data, err := io.ReadAll(r.Body)` → `len(data) == 10` **and** `errors.Is(err, middleware.ErrBodyTooLarge)` (`validation.go:17-44`). This pins the direction's "truncates streamed bodies via LimitReader" as the code's actual non-truncating contract: reads are capped at 10 B and the over-cap condition is signalled by the sentinel (the mechanism adapters map to 413), never silently truncated.
- **Boundary:** body of exactly 10 bytes → `io.ReadAll` returns clean EOF, no error (`validation.go:21-30`) — guards the exactly-at-cap distinction.

### REQ-4 — Recoverer 500 and Concurrency 429 (`TestChain_Recoverer_500`, `TestChain_Concurrency_429RetryAfter`)

- **Recoverer:** `ApplyMiddleware(http.HandlerFunc(func(...) { panic("boom") }), ...)` → **500**, body `internal server error` (`middleware.go:73-93`). Deterministic: the panic unwinds `access_log` and `concurrency` (no recoverers) into the recoverer ring, which converts it to a normal 500 response (the OTel ring outside the recoverer observes the 500 via its statusWriter).
- **Concurrency:** `concurrencyMW := middleware.NewConcurrencyLimiter(1).Middleware()` (production shape; GET weight 1, `middleware.go:133-139`):
  - Request A (GET) blocks in the inner handler on a `release` channel, holding the sole semaphore slot.
  - While A is blocked, request B (GET, same chain, same limiter instance) → **429**, `Retry-After: 1`, body `too many concurrent requests` (`middleware.go:141-151`, `rejectConcurrency` `:295-298`).
  - Close `release` → A completes → **200**.
  - Synchronization via channels only (no sleeps); two `httptest.Recorder`s; `t.Cleanup(close(release))` so a failed assertion cannot leak a blocked handler.

---

## 4. Decisions & non-goals

- **D1 — Test-only change.** The direction's effort 3 is four tests; `internal/server/chain.go` and all middleware packages are untouched. Every assertion targets `ApplyMiddleware`, the production assembly point — that is the module's FR-1/AC-1 purpose (the chain cannot drift between production and tests).
- **D2 — Assert status/header/sentinel, never wall-clock timing.** The rate-limit test relies on burst=1 + back-to-back requests; the concurrency test on a blocked slot + channels; the recoverer test on a panic. No sleeps, no elapsed-time bounds.
- **D3 — Existing tests stay untouched.** `TestBuildChain_12RingsInOrder` / `_Idempotent` / `_NilConcurrencyMWPanics` / `TestApplyMiddleware_PassesThrough` remain the shape/zero-config contract; the new tests add the rejection-behavior contract.
- **Non-goals:** no CORS-preflight behavioral test (named in the direction's problem narrative but absent from its acceptance list); no OTel/BucketCORS/AccessLog/RequestID behavioral tests (no rejection surface; RequestID/SecureHeaders already pinned by `PassesThrough`); no changes to `internal/integration/fullserver_test.go`; no middleware implementation changes; no new config surface; no new `go.mod` dependencies; no change to ring order, `wantRings`, or `BuildChain` signatures.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — Direction's test 1:** *"request through ApplyMiddleware with `middleware.NewRateLimiter(1,1)` + `rl.Start(ctx)` returns 429 after burst and passes `/healthz` (bypass)"*
→ **REQ-1** (`TestChain_RateLimit_429AndHealthzBypass`): request 1 → 200 (burst consumed), request 2 → 429 with `Retry-After` present, request 3 `/healthz` → 200. Deterministic without sleeps: burst=1 with rps=1 cannot refill between back-to-back requests.

**AC-2 — Direction's test 2:** *"SQLite repo with a tenant row `status=disabled` + `X-Aero-Tenant` header returns 403 `TenantDisabled` through the assembled chain"*
→ **REQ-2** (`TestChain_Tenant_Disabled403`): `UpsertTenant` seed → 403, `application/json`, body contains `"code":"TenantDisabled"`; unknown-tenant control returns 200.

**AC-3 — Direction's test 3:** *"`cfg.App.MaxBodySize=10` rejects `Content-Length>10` with 413 and truncates streamed bodies via LimitReader"*
→ **REQ-3** (`TestChain_MaxBody_413AndStreamCap`): `Content-Length: 11` → 413 + `Connection: close`; chunked 13-byte body → `io.ReadAll` returns exactly 10 bytes + `errors.Is(err, middleware.ErrBodyTooLarge)` (the direction's "truncates" is pinned as the code's documented non-truncating sentinel contract — E7 nuance); exactly-10-byte body → clean EOF.

**AC-4 — Direction's test 4:** *"panicking innermost handler returns 500 (Recoverer ring) and a concurrent request through `NewConcurrencyLimiter(1).Middleware()` gets 429 with `Retry-After`"*
→ **REQ-4** (`TestChain_Recoverer_500`, `TestChain_Concurrency_429RetryAfter`): panic → 500 `internal server error`; sole slot held → concurrent GET → 429 + `Retry-After: 1` + `too many concurrent requests`; released request → 200.

**AC-5 — Gate:** `go test ./internal/server/ -count=1` passes, and `make check` passes (gate = fmt/vet/build/test/test-race-meta/cli-check, Makefile:123). Baseline verified green before this spec (`ok`, 0.668s); the four new tests must keep it green. The new tests are also covered by `make test-race` (`ConcurrencyLimiter` test exercises concurrent `ServeHTTP` — race detector must stay clean).

---

## 6. Risks

- **Rate-limit refill race** — a heavily stalled runner could delay request 2 by >1 s and observe a refilled token. Mitigated by the 1 s refill vs. µs request spacing; if CI proves flaky, the remedy is a deterministic 1.1 s wait before request 2 (still no flake risk, just slower). Deferred until reported.
- **Concurrency-test leak** — if the 429 assertion fails before the release channel closes, request A stays blocked. Mitigated by `t.Cleanup(close(release))`; the blocked goroutine holds no shared state and cannot hang `go test` (the binary exits after the tests finish).
- **`httptest` + telemetry statusWriter interplay** — the OTel ring wraps the recorder in its own `statusWriter` (`telemetry/http.go`); status codes pass through unchanged and `Flush` is forwarded. Metrics go to the default no-op provider — no global state required in tests.
- **Ambiguous acceptance wording ("truncates via LimitReader")** — resolved by E7: the ring's documented and actual contract is the `ErrBodyTooLarge` sentinel (deliberately *not* silent truncation, `validation.go:17-22`). The test pins that contract; if an implementer later swaps in a true `io.LimitReader`, the streamed branch fails loudly — which is exactly the drift this direction exists to catch.
- **Assertion overreach** — REQ-2's negative control and REQ-3's boundary case are one-liners inside the acceptance-mandated tests (not new tests); they exist so the 403/413 assertions prove the specific rejection rather than blanket rejection.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); the change is test-only — four functions in `internal/server/chain_test.go` (currently 140 lines; the new tests keep it well under the 500-line single-file gate), no production diff, `make check` gate applies unchanged.*
