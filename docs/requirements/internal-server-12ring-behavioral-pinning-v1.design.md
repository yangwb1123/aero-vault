# Design — `internal/server`: behaviorally pin all 12 rings in the assembled chain

**Module:** `internal/server` · **Direction:** "Behaviorally pin all 12 rings in the assembled chain, not just names/order" (direction 1)
**Inputs:** `docs/requirements/internal-server-12ring-behavioral-pinning-v1.spec.md` (requirements stage PASS, 2026-08-07 04:52:59) · `docs/auto/analyses/internal-server-c7d0225e.json`
**This design is test-only: zero production-code changes.** API surface, ring order, config, and all existing tests are untouched.

---

## 1. Evidence verification (untrusted claims → re-measured against the repository)

All citations below were re-measured on the **working tree** at `acfaaf4` + uncommitted campaign state (see §1.1 for why that is the correct basis). Every claim that is load-bearing for this design is confirmed; the only substantive correction is the *verification basis itself*.

### 1.1 Critical correction — "verified against HEAD acfaaf4" is wrong for `internal/server`

The requirements spec states its verification basis is HEAD `acfaaf4`. **`internal/server/` does not exist at HEAD:**

```
$ git ls-tree acfaaf4 internal/server/        → (empty)
$ git log --all --oneline -- internal/server/chain.go → (empty)
$ git status --short internal/server/          → A  internal/server/chain.go
                                                → A  internal/server/chain_test.go
```

`internal/server/chain.go` (97 lines) and `chain_test.go` (140 lines) are **staged, uncommitted working-tree files** created by sibling campaign `close-the-i4-gap-harness-middleware-chain-must-m-7b3a8be7` (its implement ran, left the tree, then FAILED validation exit=1; report artifact deleted). The sibling `bucketcors…31b43d4f` gate made the same false "HEAD acfaaf4" claim about `chain.go`. **Root cause:** the pipeline runs in-place in this repo; implementations are uncommitted; reviewers verified against the working tree while labeling it HEAD.

**Consequence for this design:** the implement stage will modify this same working tree, so the working tree *is* the correct verification and acceptance basis. The spec's content is substantively accurate against the working tree (all line citations below were re-measured). Compatibility constraint: if the environment is ever reset to clean HEAD, `internal/server` disappears and this direction is blocked until the sibling's implementation is committed (§3.2).

### 1.2 Citation-by-citation verdicts (working-tree measurements)

| # | Claim (evidence/spec) | Measured | Verdict |
|---|---|---|---|
| E1 | `chain_test.go:56-100` = shape-only tests | `TestBuildChain_12RingsInOrder` `:64-85` (len 12 `:71-73`, order `:74-79`, non-nil `:80-84`), `_Idempotent` `:87-101` | ✅ exact. No request executed. |
| E2 | `chain_test.go:114-140` = `TestApplyMiddleware_PassesThrough` | `:119-140`: 418 `:131-133`, `X-Content-Type-Options` `:134-136`, `X-Request-ID` `:137-139`, call site `:126` passes `nil` rl + `nil` corsProvider | ✅ exact. 2 of 12 rings pinned; zero-config shape. |
| E3 | `chain_test.go:22-36` = `wantRings` | `:22-35` | ✅ exact (12 names, pinned order). |
| E4 | `fullserver_test.go:81-83` harness inert note | inert-note comment `:80-82`, cfg-required note `:82-83`, func `startFullServerWithConfig` `:84`; harness calls `server.ApplyMiddleware` at `:169`; `startFullServerOpts` passes `&config.Config{}` `:77` | ✅ present, minor ±2-line drift (spec already flagged). |
| E5 | `chain.go:36-69` `ChainLink` + `BuildChain` head | `ChainLink` `:42-45`, `BuildChain` `:58-89`, nil-concurrencyMW panic `:60-62` (spec `:61-63`), tenant closure `:62-65` (spec `:64-67`), 12-ring table `:66-85` (spec `:68-87`), `ApplyMiddleware` `:92-99` | ✅ present; ±1-2 line drift. Ring table order matches AGENTS.md §2.5. |
| E6 | `ratelimit.go:141-160` `Middleware()` | func `:141-160`: nil→pass `:143-145`, `rateLimitBypass` `:147-150` (`/healthz` listed, `:130-138`), 429 `http.Error` `:152-156`; `writeRateLimitHeaders` `:126-128` sets `Retry-After: int(wait.Seconds())+1`; `NewRateLimiter` `:38-43` (returns nil only for rps/burst ≤0 → `(1,1)` valid); `Start(ctx)` actually at `:48-53` (spec cited `:57-61` — drift), returns immediately, evictor stops on ctx cancel; `Allow` `:86-110`: new bucket `tokens=burst` → request 1 allowed, µs-later request 2: `elapsed≈0 → tokens<1` → denied, `wait=1.0s` | ✅ exact semantics; two minor line drifts. Deterministic without sleeps. |
| E7 | `validation.go:15-40` `ErrBodyTooLarge`/`limitErrReader` | `ErrBodyTooLarge` `:15`; `limitErrReader` `:17-43` (type `:17-30`, cap+peek `:32-40`, sentinel return `:43`); doc comment `:48-49` still says "wrapped with io.LimitReader" (**stale** — code wraps with `limitErrReader` at `:63-66`); 413 branch `:70-77` sets `Connection: close`, rejects before body read | ✅ exact nuance confirmed: over-cap streamed read surfaces the sentinel, never silent truncation. |
| — | Baseline `go test ./internal/server/` ok 0.668s | Re-run: **`ok 0.693s`**; `go build ./...` clean; `go vet ./internal/server/ ./internal/middleware/` clean | ✅ green baseline confirmed. |
| — | `make check` gate = Makefile:123 | `check: fmt vet vet-integration build test test-race-meta cli-check` at Makefile:123; `test` = `go test ./...` (covers `internal/server`); `test-race` (opt-in) covers `./internal/...` | ✅ exact. |
| — | Supplementary anchors | `TenantWithStatus` `tenant_status.go:15-42`: 403 `TenantDisabled` `:34`, `writeTenantStatusError` `:43-50` (JSON `{"error":{"code":…}}`), unknown tenant passes `:38-42`; `Recoverer` `middleware.go:73-93` → 500 `"internal server error"` `:84`; `NewConcurrencyLimiter` `:124-151`, `reqWeight` GET=1 `:130-136`, 429 `Retry-After: 1` `:159-160`; `rejectConcurrency` `:295-298`; `config.go:48` `MaxBodySize int`; `UpsertTenant` `repository/tenants.go:16-34` (defaults only empty→`"active"`), `GetTenant` `:37-55` (no row → `(zero, false, nil)`); `telemetry.WithMiddlewareTiming` `telemetry/http.go:17-24` **does not recover panics** (REQ-4 cannot false-green via the timing wrapper); `HTTPMiddleware` uses default no-op OTel provider (hermetic); execution order outer→inner `request_id→cors_bucket→cors→secure_headers→max_body→auth→tenant→rate_limit→otel→recoverer→concurrency→access_log` ⇒ tenant runs **before** rate_limit, auth before tenant (matches AGENTS.md §2.5) | ✅ all confirmed. |

**Verdict:** every load-bearing claim verified; only the "HEAD" label (§1.1) and ±1-2 line-number drifts (E4/E5/E6) needed correction — none substantive. The requirements spec stands as the contract.

---

## 2. Prior-attempt disposition ledger (gate will re-check)

### 2.1 This pipeline's own run
| Stage | Verdict | Disposition |
|---|---|---|
| requirements (`DECISIONS.md` 04:52:59) | PASS | Adopted verbatim as contract (§3-§6). No design-gate verdict exists yet (this document is that deliverable). |

### 2.2 Sibling `close-the-i4-gap-harness-middleware-chain-must-m-7b3a8be7` (created `internal/server`)
| Finding/verdict | Disposition with evidence |
|---|---|
| design_gate PASS (17-item ledger, 2026-08-06 21:10:49) | Ledger items verified **landed in the tree**: A1 fail-fast panic + `TestBuildChain_NilConcurrencyMWPanics` (`chain.go:60-62`, `chain_test.go:109-120`); bypass-table fold (`middleware_test.go` `TestTenantWithStatus_BypassTable`); production wiring (`cmd/server/main.go:166`), harness wiring (`fullserver_test.go:169`); X-Request-ID/403-body/413-equality folds live in harness AC tests (`internal/integration/middleware_chain_test.go`). No open gate-blocking items for this direction. |
| implement FAILED (2026-08-06 21:23:12, validation exit=1) | Report artifact deleted (ENOENT); root cause unrecoverable. **Superseded by evidence:** the tree it left builds (`go build ./...` clean) and `go test ./internal/server/` passes (`ok 0.693s`). |
| Ledger residual: "citation fix + chunked-truncation residual → implementation-time doc notes" | The residual is the **stale `io.LimitReader` doc comment** at `validation.go:48-49` (still present). **Explicitly dispositioned: out of scope, rejected with evidence.** (1) This design is contract-bound test-only (spec D1); a production-file edit is outside it. (2) REQ-3 pins the true contract executably, so the comment cannot mislead the suite. (3) The comment sits in the exact file the sibling `maxbodysize…` implement will rewrite (its gate FAIL lists F5/F6 in `validation.go`) — editing it now creates a collision with a pending implement. Recorded as follow-up for that campaign. |

### 2.3 Sibling `maxbodysize-silently-truncates-oversized-chunked-c0acfed6` (author of `ErrBodyTooLarge`/`limitErrReader` — the ring REQ-3 pins)
design_gate **FAIL** (2026-08-07 03:07:56). Findings dispositioned one-by-one:

| Gate finding | Disposition with evidence |
|---|---|
| QA P0 — `middleware_chain_test.go:199` asserts status range `[400,499)` not exact 413 + XML `<Code>EntityTooLarge</Code>` | **Out of scope, explicitly rejected.** P0 targets the S3-adapter acceptance of the maxbodysize direction (file `internal/integration/middleware_chain_test.go`, package `integration`); this direction's non-goals exclude harness changes and its module is `internal/server`. Overlap closed at assembly level by REQ-3's **exact 413** pin through `ApplyMiddleware`. The S3 XML pin belongs to the maxbodysize implement, which never ran. |
| Security F1 — `mcp/transport.go:56` `io.ReadAll(io.LimitReader(…,1<<20))` clean-EOF truncation | **Out of scope, rejected.** Production change in `internal/mcp`; this design is test-only and its non-goals exclude middleware/other-module changes. The MCP body cap is a separate transport surface from the `max_body` ring. |
| Protocol/Security F4 — unbounded `(0,nil)` peek loop in `limitErrReader` (`validation.go:32-40`) | **Out of scope, rejected.** Production change in `internal/middleware/validation.go`; belongs to the maxbodysize implement. REQ-3's streamed branch uses `httptest` bodies (data or EOF — never a `(0,nil)`-forever reader), so the finding cannot flake this design; the pin remains valid whether or not F4 lands. |
| F2a/F3 — SigV4 framed-byte docs; QA P1 ×4 (multipart oversize, wire-format guard, transport-error passthrough, adapter classify tests); F5 (`max %d bytes` echo); F7 telemetry counter; F2/F3/F6 log/XML follow-ups | **Out of scope, rejected.** All are production-code/doc/test changes in `internal/mcp`, `s3compat`, `rest`, `telemetry`, or `validation.go` — the maxbodysize campaign's own implement surface, none in `internal/server`. Deliberately **not** pinned here: the 413 body text (F5 territory) and the S3 XML body (P0) are left unpinned to avoid coupling this test-only change to pending production edits. |
| Implementation sanity note | Confirmed in tree: `go test ./internal/middleware/ -run MaxBodySize` and `go test ./internal/integration/ -run TestFullServer_MaxBodySize` pass on the current tree; sentinel mapped in `s3compat/errors.go:56/97/120`, `rest/handler_helpers.go:51`. |

### 2.4 Sibling `add-a-composition-profile-harness-to-startfullse-ebd6c467` — design_gate FAIL (2026-08-06 14:20:57)
Findings (stale line counts, G1-G5 doc claims, FWD-1 relay forward) concern **its own deliverable** (harness composition-profile design doc, `startFullServer` relay forwarding). **Out of scope, rejected:** different direction; this design's non-goals exclude `internal/integration` changes; none of its findings implicate `internal/server/chain_test.go` or the ring behaviors pinned here. Its FWD-1 (non-nil `&EventOutboxRelayOptions{}` forwarding) is orthogonal — the harness tests this design relies on (`startFullServerOpts`) do not pass relay opts.

### 2.5 Sibling `bucketcors-resolves-tenant-as-default…` — design_gate FAIL (2026-08-06 22:53:11)
Findings (unrevised doc, wrong ring counts "早 9 环", missing 50k cache cap, cors_bucket security vectors) concern **its own design doc** for the `cors_bucket` tenant-resolution fix. **Out of scope, rejected:** different module (`internal/middleware/cors_bucket.go`); this design pins `cors`/`cors_bucket` only as pass-through (REQ-1..4 use no CORS headers and `nil` provider). Note: its gate report's ring numbering omits 4 rings (otel/recoverer/concurrency/access_log) — a defect in that report, not in this design; our `wantRings` order is re-verified against `chain.go:66-85` (12/12).

### 2.6 Incidental matches (`replace-the-hardcoded-audit…`, `route-antivirus-worker-mutations…`)
Matched only the word "behavioral*" in reviewer prose; directions are audit-governance and AV-tag writes — **no overlap** with the middleware chain.

**Net:** no outstanding finding from any prior attempt blocks this design; every finding is either resolved in-tree (with evidence) or explicitly rejected as out-of-scope (with evidence), per the disposition ledger above.

---

## 3. Design core

### 3.1 API changes
**None.** No production file is modified. The only change is four new test functions in `internal/server/chain_test.go` (package `server`, same package — reusing `newTestChain(t)` at `:39-54` for the SQLite repo + migrated schema + disabled auth registry + discard logger, and the existing `ringNames` helper). Existing tests (`TestBuildChain_12RingsInOrder`, `_Idempotent`, `_NilConcurrencyMWPanics`, `TestApplyMiddleware_PassesThrough`) stay untouched. File budget: 140 → ≈340 lines, under the 500-line hard gate.

Every request goes through `ApplyMiddleware` — the exact assembly point `cmd/server/main.go:166` and `internal/integration/fullserver_test.go:169` use. Signatures (unchanged, `chain.go:58-99`):

```go
func BuildChain(repo repository.Repository, authReg *auth.Registry, rl *middleware.RateLimiter,
    cfg *config.Config, logger *slog.Logger, concurrencyMW func(http.Handler) http.Handler,
    corsProvider middleware.BucketCORSProvider) []ChainLink
func ApplyMiddleware(handler http.Handler, repo repository.Repository, authReg *auth.Registry,
    rl *middleware.RateLimiter, cfg *config.Config, logger *slog.Logger,
    concurrencyMW func(http.Handler) http.Handler, corsProvider middleware.BucketCORSProvider) http.Handler
```

### 3.2 Compatibility constraints
1. **Working-tree dependency:** the design targets `internal/server` files that are uncommitted artifacts of sibling `close-the-i4-gap` (staged, present in the working tree, absent from HEAD). Acceptance is defined against the working tree; a clean-HEAD reset invalidates it (failure mode F3).
2. **Zero production diff** (spec D1) — no interaction with other in-flight campaigns' files except reading `internal/middleware` symbols, all of which are in HEAD (`ratelimit.go`, `middleware.go`, `tenant_status.go`, `validation.go` HEAD + uncommitted `limitErrReader` overlay, which is already green).
3. **Determinism:** no sleeps, no wall-clock bounds, no network, no global state — the four tests use token exhaustion (burst=1), a seeded DB row, a reader sentinel, a panic, and channel synchronization only. Safe under `go test -count=1`, `-race`, and CI.
4. **No new dependencies** (I6): stdlib only (`errors`, `sync` added to the existing import set).
5. **Assertion hygiene:** the 413 response *body* is deliberately not asserted (F5 of the pending maxbodysize implement may change it); `Retry-After` is asserted for *presence* only (value is `"2"` today — `int(1.0s)+1` — but timing-derived).

### 3.3 The four tests

**REQ-1 → `TestChain_RateLimit_429AndHealthzBypass`**
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
rl := middleware.NewRateLimiter(1, 1)   // burst=1, rps=1 (non-nil: ratelimit.go:38-43)
rl.Start(ctx)                            // returns immediately; evictor stops on cancel (:48-53)
handler := ApplyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    w.WriteHeader(http.StatusOK)
}), repo, authReg, rl, cfg, logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil)
```
1. `GET /v1/files` → **200** (fresh `"default"` bucket, `tokens=burst=1`, consumed).
2. `GET /v1/files` immediately after → **429**, body `rate limit exceeded`, `Retry-After` header present (µs apart ⇒ no 1 s refill possible; deterministic).
3. `GET /healthz` after exhaustion → **200** via `rateLimitBypass` (`ratelimit.go:130-138,147-150`) — status 200 from the *inner* handler proves the bypass is end-to-end (request reached the innermost ring), not merely "no 429".

Pins: `rate_limit` ring rejection + bypass, at assembly level, without sleeps.

**REQ-2 → `TestChain_Tenant_Disabled403`**
```go
ctx := context.Background()
_ = repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "acme", Status: "disabled"}) // tenants.go:16-34
handler := ApplyMiddleware(inner200, repo, authReg, nil, cfg, logger,
    middleware.NewConcurrencyLimiter(0).Middleware(), nil)
```
- `GET /v1/files` with `X-Aero-Tenant: acme` → **403**, `Content-Type: application/json`, body contains `"code":"TenantDisabled"` (`tenant_status.go:34,43-50`). Tenant ring runs before `rate_limit`/`otel`/`recoverer` (execution order §1.2), so rejection is observable regardless of other rings' config.
- Negative control: `X-Aero-Tenant: ghost` (no row; `GetTenant` → `(zero,false,nil)` `tenants.go:50-52`) → **200** — pins the 403 comes from the disabled row, not blanket rejection.

**REQ-3 → `TestChain_MaxBody_413AndStreamCap`**
```go
cfg := &config.Config{App: config.AppConfig{MaxBodySize: 10}} // config.go:48; int64 at chain.go:68
```
Three branches, each a fresh recorder/request through `ApplyMiddleware(innerReadAll, repo, authReg, nil, cfg, logger, NewConcurrencyLimiter(0).Middleware(), nil)`:
1. **Known length:** `POST /v1/files`, `Content-Length: 11` → **413** + `Connection: close` (rejected before body read, `validation.go:70-77`). Body text not asserted (see §3.2.5).
2. **Streamed:** `ContentLength: -1` + `Transfer-Encoding: chunked` (same construction as `chunkedRequest` in `validation_test.go:82-89`), 13-byte body; inner handler `data, err := io.ReadAll(r.Body)` → `len(data) == 10` **and** `errors.Is(err, middleware.ErrBodyTooLarge)` — pins the non-truncating sentinel contract (`validation.go:17-43`), the documented resolution of the direction's "truncates via LimitReader" wording.
3. **Boundary:** exactly 10 bytes → `io.ReadAll` returns clean EOF, `err == nil` (`:21-30`).

**REQ-4 → `TestChain_Recoverer_500` + `TestChain_Concurrency_429RetryAfter`**
- **Recoverer:** `ApplyMiddleware(http.HandlerFunc(func(...) { panic("boom") }), …)` → **500**, body `internal server error` (`middleware.go:73-93`). Sound by construction: `telemetry.WithMiddlewareTiming` does not recover (`telemetry/http.go:17-24`), so a regression that removes the recoverer ring crashes the test loudly — no false green.
- **Concurrency:** `concurrencyMW := middleware.NewConcurrencyLimiter(1).Middleware()` (production shape; GET weight 1, `middleware.go:130-136`):
  - Request A (GET) runs in a goroutine; its inner handler signals `blocked` then blocks on `<-release`, holding the sole slot.
  - Main goroutine: request B (GET, same chain instance) → **429**, `Retry-After: 1`, body `too many concurrent requests` (`middleware.go:159-160`).
  - `releaseOnce.Do(func(){ close(release) })` → A completes → `<-done` → `recA.Code == 200`.
  - `t.Cleanup(func(){ releaseOnce.Do(func(){ close(release) }) })` — `sync.Once` prevents a double-close panic when the main path also closes; a failed B assertion cannot leak a blocked handler (cleanup unblocks A; the goroutine holds no shared state and cannot hang `go test`).
  - Channel happens-before (`<-done` before reading `recA`) keeps the race detector clean; no sleeps.

### 3.4 Failure modes

| # | Failure mode | Detection | Mitigation |
|---|---|---|---|
| F1 | Rate-limit refill race (heavily stalled CI delays request 2 >1 s) | Flaky 429→200 | 1 s refill vs µs spacing; if ever reported, fall back to a deterministic 1.1 s wait before request 2 (still no flake, just slower). Deferred until reported (spec §6). |
| F2 | Concurrency-test leak (assertion fails before release) | Blocked goroutine | `t.Cleanup` + `sync.Once` release (§3.3); binary exits after tests regardless. |
| F3 | Clean-HEAD reset (working tree loses `internal/server`) | `go build` fails | Blocked on sibling implement being committed; documented in DECISIONS.md. Not a design defect — environment contract. |
| F4 | Pending maxbodysize implement lands (rewrites `validation.go` docs/F4 loop) | — | REQ-3 depends only on the already-green sentinel semantics; compatible by construction. |
| F5 | Race detector on the concurrency test | `go test -race ./internal/server/` | Channels only, `<-done` before recorder read. |
| F6 | `go test ./...` (make check `test` target) | 413/429 regressions in other packages | None expected — zero production diff; existing integration tests (`TestFullServer_MaxBodySize*`) unaffected. |

### 3.5 Migration steps
N/A — test-only, no schema/config/env/API migration. **Implementation sequence:** (1) append the four tests to `internal/server/chain_test.go`; (2) `gofmt -l internal/server/` clean; (3) `go test ./internal/server/ -count=1`; (4) `go test -race -count=1 ./internal/server/`; (5) `make check` (fmt → vet → vet-integration → build → test → test-race-meta → cli-check; Makefile:123). No commit (pipeline convention).

---

## 4. Testable acceptance mapping

| Acceptance (direction, verbatim) | Mapping | Executable proof |
|---|---|---|
| **AC-1** *request through ApplyMiddleware with `middleware.NewRateLimiter(1,1)` + `rl.Start(ctx)` returns 429 after burst and passes `/healthz` (bypass)* | **REQ-1** `TestChain_RateLimit_429AndHealthzBypass` | 200 → 429 (+`Retry-After` present) → 200 via `/healthz`, all through one `ApplyMiddleware` chain, no sleeps. |
| **AC-2** *SQLite repo with tenant row `status=disabled` + `X-Aero-Tenant` header returns 403 `TenantDisabled` through the assembled chain* | **REQ-2** `TestChain_Tenant_Disabled403` | `UpsertTenant` seed → 403 + `application/json` + `"code":"TenantDisabled"`; `ghost` control → 200. |
| **AC-3** *`cfg.App.MaxBodySize=10` rejects `Content-Length>10` with 413 and truncates streamed bodies via LimitReader* | **REQ-3** `TestChain_MaxBody_413AndStreamCap` | CL 11 → 413 + `Connection: close`; chunked 13 B → `io.ReadAll` = 10 B + `errors.Is(err, ErrBodyTooLarge)` (the direction's "truncates" pinned as the code's non-truncating sentinel contract — E7 nuance); 10 B exact → clean EOF. |
| **AC-4** *panicking innermost handler returns 500 (Recoverer ring) and a concurrent request through `NewConcurrencyLimiter(1).Middleware()` gets 429 with `Retry-After`* | **REQ-4** `TestChain_Recoverer_500` + `TestChain_Concurrency_429RetryAfter` | panic → 500 `internal server error`; sole slot held → concurrent GET → 429 + `Retry-After: 1` + `too many concurrent requests`; released → 200. |
| **AC-5** *`go test ./internal/server/` and `make check` pass* | Gate | Baseline green today (`ok 0.693s`, `go build ./...` clean, `go vet` clean); four tests must keep `go test ./internal/server/ -count=1` and `make check` (Makefile:123) green; `go test -race ./internal/server/` clean (concurrent `ServeHTTP`). |

**Non-goals (unchanged from spec §4):** CORS-preflight behavioral test; OTel/BucketCORS/AccessLog behavior; `internal/integration` changes (incl. the maxbodysize P0 S3 XML pin — §2.3); middleware implementation changes; new config surface; new `go.mod` deps; ring order/`wantRings`/`BuildChain` signature changes.

---

## 5. Risks

- **Sibling coupling (highest):** the whole direction rides on uncommitted `internal/server` files (F3). Verified present and green today; any environmental reset invalidates acceptance — flagged to the gate, not resolvable in-code.
- **Assertion overreach:** REQ-2's negative control and REQ-3's boundary are one-liners inside acceptance-mandated tests; REQ-3 deliberately skips the 413 body text to stay decoupled from pending maxbodysize edits (§3.2.5).
- **Rate-limit flake:** bounded and mitigated (F1); deferred per spec §6 until reported.

*Verification basis: all anchors re-measured on the working tree at `acfaaf4` + uncommitted campaign state on 2026-08-07; the change is four test functions in `internal/server/chain_test.go` (≈340 lines total, < 500-line gate), zero production diff, `make check` gate unchanged.*
