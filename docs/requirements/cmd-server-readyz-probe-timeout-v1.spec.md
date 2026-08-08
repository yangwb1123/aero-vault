# Requirements Specification — `cmd/server`: bound the `/readyz` storage probe with a short, independent timeout

**Module:** `cmd/server`
**Direction:** "Bound the /readyz storage probe with a short, independent timeout"
**Source analysis:** `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 1)
**Date:** 2026-08-06 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 7 / effort 1 / confidence 9

---

## 1. Scope

`readyzHandler` passes the raw request context into `store.Stat(ctx, "@healthz/probe")` (`cmd/server/http.go:54`). On S3/OSS/COS backends that is a live `HeadObject`/`Head` call whose only effective bound is the transport-level `ResponseHeaderTimeout` default of **30 s** (`STORAGE_READ_TIMEOUT`, `internal/config/config.go:90` → `internal/storage/storage.go:140`). A wedged or partitioned object store therefore blocks the readiness endpoint for tens of seconds per probe, defeating LB/orchestrator failover and masking the very outage readiness is meant to surface. `/readyz` is exempt from rate limiting (`internal/middleware/ratelimit.go:133`) and from the tenant-status gate, and the `ConcurrencyLimiter` (`internal/middleware/middleware.go:115-176`) has **no** `/readyz` bypass — repeated wedged probes also occupy concurrency slots.

This spec scopes exactly one change: **give the storage probe inside `readyzHandler` a short, independent deadline**. Out of scope (see §4): rate-limit changes, storage-layer deadlines, config surface, `/healthz`, and any status-code/JSON-schema change.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `cmd/server/http.go:54` — raw `req.Context()` into `store.Stat(ctx, "@healthz/probe")` | `readyzHandler` `:46-68`; probe at `:54`: `if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound)` | ✅ **exact**. No `context.WithTimeout` anywhere in the handler; the only `WithTimeout` uses in `cmd/server` are `audit_governance.go:27` and the shutdown path `http.go:207` — unrelated. |
| E2 | `cmd/server/http.go:50` — handler shape | `:46` func def; `:50-52` `repo.Ping` branch; `:54` probe; `:58-63` `extra.Ready`; `:64-66` 200 `{"ok":true}`; route registration `r.Get("/readyz", readyzHandler(repo, store, extraReady))` at `:94` | ✅ **present** (def at `:46`, minor drift from `:50`). |
| E3 | `internal/storage/s3.go:175-207` — `Stat` is a live `HeadObject` with only the caller context | `Stat` `:175-178` → `StatWithOptions` `:179-207`; `s.client.HeadObject(ctx, input)` at `:192`; no `context.WithTimeout` inside | ✅ **present** (range now `:175-207`, call at `:192`). **Nuance:** the client is built with `NewHTTPClient(cfg.Timeouts)` (`s3.go:62`), which sets `http.Client.Timeout = max(connect, read, write)` and `ResponseHeaderTimeout = ReadTimeout` (`storage.go:129-145`) — so the probe is bounded at the **transport** by ~30 s (default), not unbounded. The direction's "tens of seconds per probe" stands; "no internal deadline" means no deadline in the storage layer itself. |
| E4 | `internal/config/config.go:90` — `STORAGE_READ_TIMEOUT` default 30 | `ReadTimeout: getEnvInt("STORAGE_READ_TIMEOUT", 30)` at `:90` | ✅ **exact**. |
| E5 | `internal/middleware/ratelimit.go:132` — `/readyz` exempt from rate limiting | `rateLimitBypass` `:131-137`, `/readyz` at `:133` | ✅ **exact** (`:132` holds the `"/healthz"` arm of the same expression). **Bonus findings:** `internal/middleware/tenant_status.go:50-51` also exempts `/readyz`; `ConcurrencyLimiter` (`internal/middleware/middleware.go:115-176`) has **no** bypass, so each wedged probe also holds a concurrency slot (GET weight 1, `middleware.go:131-139`) — pile-up can exhaust the `MAX_INFLIGHT_REQUESTS` budget for real traffic. |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "A wedged object store blocks /readyz for tens of seconds per probe" | ✅ **holds.** Probe bound = transport `ResponseHeaderTimeout` 30 s (`storage.go:140` ← `config.go:90`). The request context carries no deadline of its own (http.Server cancels it only on client disconnect), and the server `WriteTimeout` 60 s (`http.go:181`) does not bound handler work. |
| "Defeats LB/orchestrator failover and masks the outage" | ✅ **holds** — a readiness endpoint that answers in up to 30 s is useless to LB probes (typically 1-10 s intervals). |
| "Repeated probes can pile up" | ✅ **holds** — rate-limit bypass (`ratelimit.go:133`) *and* no concurrency bypass (`middleware.go`), so N wedged probes consume N of the weighted slots. |

---

## 3. Requirements

### REQ-1 — Probe deadline (the change)

In `readyzHandler` (`cmd/server/http.go:46-68`), wrap **only the storage probe**:

- `probeCtx, cancel := context.WithTimeout(req.Context(), readyzProbeTimeout); defer cancel()`; pass `probeCtx` to `store.Stat`.
- `repo.Ping(req.Context())` (`:50`) and `extra.Ready(req.Context())` (`:59`) stay on the raw request context — unchanged.
- **No new error classification.** Any non-`storage.ErrNotFound` error from `Stat` — including `context.DeadlineExceeded`, whether bare or wrapped by the AWS SDK — flows through the existing branch `:54-56` → **503 `storage unavailable`**. `storage.ErrNotFound` still yields 200 (probe key absent ≠ storage down).

### REQ-2 — Timeout value: short and independent

`readyzProbeTimeout = 2 * time.Second`, a package-level constant in `cmd/server/http.go`.

- **Independent:** not derived from `STORAGE_READ_TIMEOUT` (30 s default, `config.go:90`) nor `REQUEST_TIMEOUT_SECONDS` (120 s default, `config.go:80`) — a change to either must not silently move the readiness bound.
- **Short:** 2 s is below typical LB/orchestrator probe intervals (10 s) yet orders of magnitude above a healthy probe cost: local-FS `Stat` (µs-ms), SQLite `Ping` (µs), and a healthy `HeadObject` (<100 ms).
- **Not configurable** (decision D1, §4).

### REQ-3 — Regression guards (tests, `cmd/server/http_test.go`)

The codebase's partial-stub idiom (embed the interface, override one method — cf. `deleteFailStorage` in `internal/api/webdav/dav_test.go:827-838`) applies to both large interfaces (`repository.Repository` has 143 methods, `storage.Storage` 17):

- **`TestReadyzStorageProbeTimeout`** — `stubRepo{repository.Repository}` with `Ping` → `nil`; `blockingStatStorage{storage.Storage}` whose `Stat` blocks until `ctx.Done()` then returns `ctx.Err()`; call `readyzHandler(repo, store, nil)` via `httptest`. Assert: status **503**, body contains `storage unavailable`, elapsed **∈ [1 s, 5 s]** with the real 2 s constant. Because the stub blocks unconditionally, a response can only arrive *after* the deadline fires (lower bound proves the wrap works and the stub was cancelled); the upper bound proves boundedness.
- **`TestReadyzErrNotFoundIsReady`** — same stubs but `Stat` returns `storage.ErrNotFound` immediately. Assert: status **200**, body `{"ok":true}`. Guards the only behavioral fork on the modified line (the exemption must survive the wrap).

---

## 4. Decisions & non-goals

- **D1 — Package constant, not env config.** The direction asks for "a short, independent timeout" at effort 1; an env knob would expand into `internal/config`, `docs/configuration.md`, `.env.example`, and validation (I5/AGENTS "扩展入口"). Promote to config only if ops reports false positives.
- **D2 — Deadline applies to the storage probe only.** `repo.Ping` (SQLite/Postgres, local or fast) and `extra.Ready` (in-process runtime deps) are out of the direction's scope; bounding them would change behavior beyond the cited defect.
- **Non-goals:** no rate-limit changes for `/readyz` (the citation is pile-up *context*, not a requested fix); no internal deadline inside `internal/storage/s3.go`/`oss.go`/`cos.go`; no `/healthz` change; no change to readyz status codes, JSON body, or the 503 error text; no new `go.mod` dependencies.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — Direction's test acceptance.** *"A cmd/server test with a storage stub that blocks until ctx.Done() proves readyzHandler returns 503 within ~2s when wrapped in a probe timeout."*
*Testable:* `TestReadyzStorageProbeTimeout` in `cmd/server/http_test.go` (REQ-3): `blockingStatStorage.Stat` blocks on `ctx.Done()`; with `readyzProbeTimeout = 2 s`, the handler must return 503 `storage unavailable` with elapsed ∈ [1 s, 5 s]. The blocking stub makes the assertion deterministic: the response cannot precede the deadline, so the lower bound only guards against an accidental near-instant 503 (e.g. a stub that fails before blocking), and the upper bound is the actual "within ~2s" claim made timing-robust.

**AC-2 — `go test ./cmd/server/ -count=1` passes.**

**AC-3 — `go build ./... && go vet ./...` pass.**

(These three are the direction's acceptance verbatim, made concrete; they are also a subset of the `make check` gate: fmt/vet/build/test.)

---

## 6. Risks

- **Timing flake on loaded CI** — mitigated: blocking stub (response only after deadline) + generous bounds [1 s, 5 s] around the 2 s constant; no wall-clock equality assertions.
- **SDK wraps the context error** — the existing `:54` branch maps *any* non-`ErrNotFound` error to 503, so a wrapped `DeadlineExceeded` still yields 503 with no new classification (REQ-1).
- **2 s rejects a slow-but-healthy store** — a store that cannot answer a HEAD in 2 s cannot serve traffic either; accepted trade-off, revisit only on reported false positives (D1 keeps the door open).
- **Transient 503s on deadline fire** — correct readiness semantics: if the probe cannot complete in 2 s, the instance should be taken out of rotation; the 503 body and code are unchanged, so orchestrators observe the same signal they already handle.

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); `make check` gate applies to the eventual implementation (gofmt/build/vet/test, single file ≤ 500 lines — the change is ~3 lines in `cmd/server/http.go` plus tests in `cmd/server/http_test.go`, currently 18 lines).*
