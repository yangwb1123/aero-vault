# Design — `cmd/server`: bound the `/readyz` storage probe with a short, independent timeout

**Module:** `cmd/server` · **Spec:** `docs/requirements/cmd-server-readyz-probe-timeout-v1.spec.md` (REQ-1..3, D1/D2, AC-1..3)
**HEAD:** `acfaaf4` (all citations re-verified on this checkout) · **Date:** 2026-08-06
**Scope lock:** exactly one behavior change — a 2 s deadline on the storage probe inside `readyzHandler`. Nothing else moves.

---

## 1. Verification register (evidence re-checked, not trusted)

| Citation | Re-verified location (HEAD `acfaaf4`) | Verdict |
|---|---|---|
| `cmd/server/http.go:54` — raw `req.Context()` into `store.Stat("@healthz/probe")` | `readyzHandler` at `:46-68`; probe at `:54` (`if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound)`); route `r.Get("/readyz", readyzHandler(repo, store, extraReady))` at `:94` | ✅ exact. No `context.WithTimeout` anywhere in the handler; the only other `WithTimeout` uses in `cmd/server` are `audit_governance.go:27` and shutdown `http.go:207` — unrelated |
| `cmd/server/http.go:50` | `repo.Ping(req.Context())` at `:50-52`; `extra.Ready(req.Context())` at `:58-63`; 200 `{"ok":true}` at `:64-66` | ✅ present |
| `internal/storage/s3.go:175-207` — Stat is a live `HeadObject`, caller ctx only | `Stat` `:175-178` → `StatWithOptions` `:179-207`; `s.client.HeadObject(ctx, input)` at `:192`; no internal deadline | ✅ present. **Nuance (spec E3):** client built via `NewHTTPClient` (`s3.go:62`) sets `http.Client.Timeout = max(connect,read,write)` and `ResponseHeaderTimeout = ReadTimeout` (`storage.go:129-145`) — probe is transport-bounded at ~30 s default, not unbounded. The defect stands: 30 s ≫ LB probe intervals |
| `internal/config/config.go:90` — `STORAGE_READ_TIMEOUT` default 30 | `ReadTimeout: getEnvInt("STORAGE_READ_TIMEOUT", 30)` at `:90` | ✅ exact |
| `internal/middleware/ratelimit.go:132` — `/readyz` exempt | `rateLimitBypass` `:131-137`, `"/readyz"` at `:133` | ✅ exact (the `:132` arm is `"/healthz"` in the same expression) |
| Bonus: `ConcurrencyLimiter` has no readyz bypass | `internal/middleware/middleware.go:115-176`; GET weight 1 at `:131-139`; no path check anywhere in `Middleware()` | ✅ confirmed — each wedged probe holds a weighted slot for up to 30 s; pile-up can exhaust `MAX_INFLIGHT_REQUESTS` |
| Bonus: `tenant_status.go` also exempts `/readyz` | `internal/middleware/tenant_status.go:50-51` | ✅ confirmed (context only; no design impact) |
| Stub idiom `deleteFailStorage` | `internal/api/webdav/dav_test.go:824-838` — embeds `storage.Storage`, overrides one method | ✅ confirmed, adopted verbatim in §4 |
| `repository.Repository` size / `Ping` | `internal/repository/repository_interface.go:11` (`Ping(ctx) error`); interface is huge (143 methods) → partial stub is the only sane test double | ✅ confirmed |
| `readyzProbeTimeout` symbol free | `grep -rn readyzProbeTimeout` across `*.go` → empty; `go build ./cmd/...` OK | ✅ no collision |
| `http.go` already imports `time` | line 9 | ✅ no import churn |
| `main.go` passes `extraReady` | `cmd/server/main.go:156` → `runtimeReadiness(billingRuntime, auditRuntime)` (`audit_governance.go:51-65`) | ✅ untouched by this design (REQ-1 keeps `extra.Ready` on the raw ctx) |
| Local backend Stat is fast | `internal/storage/local_read.go:67` (`LocalStorage.Stat`) | ✅ local FS stat — µs–ms; wrap is a no-op for the CI baseline |

**Problem-statement checks (all hold):** (1) probe bound = transport `ResponseHeaderTimeout` 30 s (`storage.go:140` ← `config.go:90`), request ctx has no deadline of its own; (2) a readiness endpoint answering in up to 30 s defeats LB/orchestrator failover (typical probe intervals 1–10 s); (3) rate-limit bypass (`ratelimit.go:133`) **and** no concurrency bypass (`middleware.go`) → N wedged probes consume N weighted slots.

**Post-change bound:** probe answers in ≤ ~2 s (context deadline fires, SDK `HeadObject` aborts on ctx cancel) instead of ≤ ~30 s (transport timeout) — a **15× reduction**, plus prompt cancellation instead of waiting out the transport window.

---

## 2. Design

### D1 — Deadline on the storage probe only (`cmd/server/http.go`, ~3 changed lines)

```go
// readyzProbeTimeout bounds the /readyz storage probe independently of
// STORAGE_READ_TIMEOUT (30s) and REQUEST_TIMEOUT_SECONDS (120s): a wedged
// object store must not hold the readiness endpoint for tens of seconds.
const readyzProbeTimeout = 2 * time.Second

func readyzHandler(
	repo repository.Repository, store storage.Storage, extra readinessChecker,
) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := repo.Ping(req.Context()); err != nil {          // unchanged
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		probeCtx, cancel := context.WithTimeout(req.Context(), readyzProbeTimeout) // NEW
		defer cancel()                                                              // NEW
		if _, err := store.Stat(probeCtx, "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if extra != nil {                                            // unchanged
			if err := extra.Ready(req.Context()); err != nil { ... }
		}
		...
	}
}
```

- `repo.Ping(req.Context())` and `extra.Ready(req.Context())` stay on the raw request context (REQ-1, D2 of spec).
- **No new error classification.** `context.DeadlineExceeded`, whether bare or wrapped by the AWS SDK (`*smithy.OperationError`), is not `storage.ErrNotFound` (distinct sentinels — `internal/storage/storage.go:14`) → falls through the existing `:54-56` branch → **503 `storage unavailable`**, unchanged body text.
- `storage.ErrNotFound` still → 200 (probe key absent ≠ storage down). Exemption survives the wrap because the wrap is on the *call*, not on the classification.
- `defer cancel()` frees the 2 s timer promptly; ctx cancellation propagates into the SDK (aws-sdk-go-v2 honors ctx), aborting the in-flight `HeadObject` instead of waiting out the transport window.
- Client disconnect still cancels earlier (derived from `req.Context()`), so the wrap is a strict superset bound.

### D2 — Constant, not config (spec D1)

`readyzProbeTimeout = 2 * time.Second`, package-level const adjacent to `readyzHandler`. **Independent:** not derived from `STORAGE_READ_TIMEOUT`/`REQUEST_TIMEOUT_SECONDS`; moving either must not silently move the readiness bound. **Not configurable:** no `internal/config` change, no `.env.example`, no `docs/configuration.md`, no validation surface (keeps I5 opt-in posture and effort-1 scope). Promote to env config only if ops reports false positives.

### D3 — Test stubs (partial-stub idiom, cf. `deleteFailStorage`)

Two stubs embedding interfaces with a single override each, in `cmd/server/http_test.go` (package `main`):

```go
// stubReadyRepo embeds repository.Repository; only Ping is reachable by readyzHandler.
type stubReadyRepo struct{ repository.Repository }

func (s *stubReadyRepo) Ping(ctx context.Context) error { return nil }

// blockingStatStorage emulates a wedged object store: Stat returns only when the
// caller context is done (the handler's 2s deadline), mimicking the S3 backend
// whose HeadObject has no internal deadline.
type blockingStatStorage struct{ storage.Storage }

func (s *blockingStatStorage) Stat(ctx context.Context, _ string) (storage.ObjectInfo, error) {
	<-ctx.Done()
	return storage.ObjectInfo{}, ctx.Err()
}

// notFoundStatStorage answers the probe key as absent — the healthy-store path.
type notFoundStatStorage struct{ storage.Storage }

func (s *notFoundStatStorage) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, storage.ErrNotFound
}
```

The embedded interfaces are nil and never dereferenced: `readyzHandler` calls only `repo.Ping`, `store.Stat`, and (with `extra=nil` in both tests) no third method. This is exactly the `deleteFailStorage` pattern (`dav_test.go:827-838`).

### D4 — Regression tests

**`TestReadyzStorageProbeTimeout`** (spec REQ-3 / AC-1):

```go
h := readyzHandler(&stubReadyRepo{}, &blockingStatStorage{}, nil)
rec := httptest.NewRecorder()
start := time.Now()
h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
elapsed := time.Since(start)
// assert: rec.Code == 503; body contains "storage unavailable"; 1s <= elapsed <= 5s
```

Determinism argument: the stub returns **only** after `ctx.Done()` fires (2 s), so a response cannot precede the deadline — the lower bound guards only against an accidental near-instant 503 (e.g., a stub that errors before blocking), the upper bound is the "within ~2 s" claim made timing-robust against loaded CI. No wall-clock equality assertions.

**`TestReadyzErrNotFoundIsReady`** (spec REQ-3):

```go
h := readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, nil)
// assert: rec.Code == 200; body == `{"ok":true}`; elapsed < 1s
```

The `< 1 s` assertion pins that the exemption is honored **without waiting out the deadline** (regression guard on the only behavioral fork of the modified line; a stub returning immediately gives 3 orders of magnitude of slack, so no realistic flake).

**Optional (not required, 15 lines):** `TestReadyzImmediateStorageError` — a stub returning `errors.New("boom")` immediately asserts 503 in < 1 s, pinning that the wrap neither delays nor swallows non-deadline errors. The two required tests already cover the 503 branch (via deadline) and the 200 branch; this third one covers the existing immediate-503 branch. Include only if it does not grow `http_test.go` beyond comfort (it does not — file stays ~120 lines).

---

## 3. API changes & compatibility constraints

| Surface | Change |
|---|---|
| REST `/v1`, S3 `/s3`, MCP, WebDAV, `/healthz`, OpenAPI, events, DB schema, config env, wire payloads | **none** |
| `readyzHandler` signature | **none** (package-private, `cmd/server` only; called at `http.go:94`) |
| `/readyz` response shape | **none** — same codes (200/503), same bodies (`{"ok":true}` / `"storage unavailable"`), same `Content-Type: text/plain; charset=utf-8` from `http.Error` |
| `/readyz` response timing, healthy path | **identical** — healthy `HeadObject` < 100 ms ≪ 2 s; local-FS `Stat` µs–ms (CI baseline backend) |
| `/readyz` response timing, wedged store | 503 at ~2 s (was up to ~30 s transport timeout) — the intended change; orchestrators see the same signal shape sooner |
| Slow-but-healthy store (HEAD > 2 s) | behavior flips 200-after-30 s → 503-after-2 s; accepted readiness semantics (a store that can't answer a HEAD in 2 s can't serve traffic either) — spec §6 |
| Middleware chain (I4) | untouched — the wrap is inside the handler; `/readyz` keeps its existing auth/tenant/ratelimit exemptions; `ConcurrencyLimiter` slot hold per wedged probe shrinks 30 s → 2 s (15×, pile-up pressure reduced, not eliminated — that is a non-goal) |
| Invariants I1/I2/I3/I5/I6 | untouched — no SQL, no migrations, no storage-key logic, no new flags, no new `go.mod` deps |

## 4. Failure modes

| # | Mode | Behavior | Disposition |
|---|---|---|---|
| FM1 | Wedged/partitioned object store | 503 at ~2 s (was ~30 s); concurrency slot held 2 s instead of 30 s | The fix. Test: `TestReadyzStorageProbeTimeout` |
| FM2 | Healthy store, transient > 2 s latency | False-negative 503; self-heals on next probe | Accepted trade-off (spec §6); revisit only on reported false positives → then promote const to config (D2 keeps the door open) |
| FM3 | SDK wraps the deadline error (`*smithy.OperationError` wrapping `context.DeadlineExceeded`) | `errors.Is(err, storage.ErrNotFound)` false → 503, no new classification | Verified: distinct sentinels; branch maps any non-NotFound error to 503 (REQ-1) |
| FM4 | Client disconnects during probe | `req.Context()` cancels first → stub/SDK returns `context.Canceled` → same 503 branch | Correct; no change needed |
| FM5 | Timer leak | `defer cancel()` releases the 2 s timer when `Stat` returns | No leak |
| FM6 | Loaded CI makes `elapsed` > 5 s in the timing test | Test flake | Blocking stub + generous window + `-count=1`; lower bound is informational (deadline guarantees ≥ 2 s); if a runner proves systematically > 5 s, widen the upper bound only (non-semantic) |
| FM7 | OSS/COS backends | Same wrap applies; both honor ctx (`cos.go:101`, `oss.go:94` Stat → SDK Head) | Covered by the same handler-level change; no backend-specific code |

## 5. Migration & rollback

- **Migration steps: none.** No schema migration (I2 untouched), no config surface (D2), no wire changes, no deploy coordination. Ship as a code-only change in one release.
- **Rollback:** revert the single hunk (const + `WithTimeout` wrap) in `cmd/server/http.go`; delete the two tests. Lossless — no data, no state, no ordering constraints.
- **Observability:** no new metrics; existing `readyz` behavior (503 on storage failure) is unchanged in shape, so dashboards/alerts keyed on `/readyz` continue to work, now with tighter latency. If desired (non-goal, not implemented here), a `readyz_storage_probe_ms` histogram could later quantify false-positive pressure.

## 6. Testable acceptance mapping

| Acceptance (spec §5) | Test / command | Assertion | Gate |
|---|---|---|---|
| AC-1: blocking-stub test proves 503 within ~2 s | `TestReadyzStorageProbeTimeout` (`cmd/server/http_test.go`) | `rec.Code == 503`; body contains `storage unavailable`; `1s <= time.Since(start) <= 5s` (deterministic: response can only arrive after the 2 s deadline fires) | `go test ./cmd/server/` |
| AC-2 | `go test ./cmd/server/ -count=1` | all tests pass (existing `TestRedirectWebUI` + 2–3 new) | `make check` (test) |
| AC-3 | `go build ./... && go vet ./...` | zero errors | `make check` (build/vet) |
| Hard gate: gofmt | `gofmt -l .` | no output (new code formatted; no tabs/alignment traps) | `make check` (fmt) |
| Hard gate: file size | `wc -l cmd/server/http.go cmd/server/http_test.go` | `http.go` 215 → ~219 (< 500); `http_test.go` 18 → ~120 (< 500) | reviewer |

## 7. Disposition of prior attempts (gate re-check)

1. **This run's own directory** (`docs/auto/runs/bound-the-readyz-storage-probe-with-a-short-inde-e2702041/`): `DECISIONS.md` records only stage `requirements` PASS (2026-08-06 15:26:46). **No design-gate verdicts exist yet** — nothing outstanding to resolve; this design implements the spec 1:1 (REQ-1 wrap shape, REQ-2 const semantics, REQ-3 stub idiom + both tests, D1/D2 non-goals respected, AC-1..3 preserved testable, §6 risks each dispositioned: FM3 SDK-wrapped errors → existing branch; FM6 flake → window + blocking stub; 2 s trade-off → documented, config door open).
2. **Sibling run** `fail-closed-liveness-gate-on-the-rag-read-path-s-6e061f2f/` (only run with a similar name in `docs/auto/runs/`): different direction — RAG search liveness gate (`Search.Query` → `filterLiveHits`). Its gate findings (SQL query-plan shape, marker-JSON predicate removal, test-coverage corrections, `LiveObjectIDs` repo method) concern `internal/ai` + `internal/repository` SQL — **no shared code path with `cmd/server/readyzHandler`**. Disposition: **N/A, no overlap**; none of its blocking findings can affect this change (no SQL, no schema, no new repo methods here).
3. **Git history** (`git log -S readyzHandler` → `9e9a216`, `fb43973`, `2aec1a0`, `8794994`, July 2026): pre-campaign broad-expansion *docs* analyses (requirement-analysis / technical-design / code-review stages of the old pipeline, all in `docs/results/`), **no implementation of this direction ever landed**; `readyzProbeTimeout` appears nowhere in history or tree. Disposition: **no prior implementation to reconcile**.

## 8. Files changed (complete list)

| File | Change | Size after |
|---|---|---|
| `cmd/server/http.go` | +`readyzProbeTimeout` const; wrap `store.Stat` call with `context.WithTimeout` + `defer cancel()` (2 new statements, 1 changed line) | ~219 lines (< 500 ✅) |
| `cmd/server/http_test.go` | +3 stubs, +2 tests (+ optional 3rd) | ~120 lines (< 500 ✅) |
| `docs/requirements/cmd-server-readyz-probe-timeout-v1.design.md` | this document | 173 lines |

No other files. No `go.mod` changes (I6 ✅). No `docs/configuration.md` / `.env.example` changes (D2 ✅).
