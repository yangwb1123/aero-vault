# Requirements Specification — `internal/shutdown`: fix `GoStarted` startup deadlock on panic / never-signalled readiness

**Module:** `internal/shutdown` (production fix in `group.go` + two new tests in `group_test.go`)
**Direction:** "Fix GoStarted startup deadlock when worker panics or never signals readiness"
**Source analysis:** `docs/auto/analyses/internal-shutdown-8803597d.json` (direction 1; the other directions in the file — WaitGroup race, double hook firing, waiter leak — are explicitly **out of scope**)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 8 / effort 2 / confidence 10

---

## 1. Scope

`GoStarted` (`internal/shutdown/group.go:116-128`) is the package's readiness-gated startup primitive: it blocks the caller on `<-ready` (**:127**) until the worker signals on the unbuffered channel (**:120**). Two failure modes leave that block permanent:

1. **Panic before signal.** The goroutine registers `defer g.recoverPanic(name)` (**:124**) *after* the readiness contract, so when `fn` panics the panic is recovered and logged, the goroutine exits cleanly, and `close(ready)` never happens. `<-ready` blocks forever — the caller (and therefore the whole boot path) hangs with no timeout, and `Shutdown` can never be reached.
2. **Never signals.** `fn` runs but never closes `ready` (a worker that blocks before its first signal, or forgets the contract). The unconditional `<-ready` has no `select` on `g.root.Done()`, so cancelling the group context does not unblock it either.

**Verified empirically on this checkout** (repro test, 3s bound): a goroutine calling `GoStarted` whose `fn` panics before `close(ready)` did **not** return within 3s — the hang is real, not theoretical. The package is currently **dead code** (no non-test file imports `aero-vault/internal/shutdown`; `GoStarted` has zero callers outside the package), so the hang is latent today — but the module exists to be wired into the boot path, and any future caller using `GoStarted` for a panic-prone initializer inherits the hang.

This spec scopes exactly: **(1)** `GoStarted` unblocks when `fn` exits (panic or plain return) without signalling — with exactly-once, double-close-safe signalling; **(2)** `GoStarted` unblocks when the group root context is cancelled even if `fn` never signals; **(3)** two new tests `TestGroup_GoStarted_PanicBeforeReady` and `TestGroup_GoStarted_NeverSignalsCancelled` with the exact names demanded by the acceptance. **No changes to `Go`, `Shutdown`, `recoverPanic`, `Phase*`, or any other file in the repo.**

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `group.go:99-108` — GoStarted: unbuffered ready chan, unconditional `<-ready`, no ctx/timeout select | `GoStarted` sits at **:116-128** (the cited :99-112 is the `Go` method — same shape, no ready chan). `ready := make(chan struct{})` at **:120** (unbuffered); goroutine at :122-126; `<-ready` at **:127** — unconditional, no `select`, no context, no timeout | ✅ **substance exact; range shifted +17 lines** |
| E2 | `group.go:120-128` — recoverPanic runs after fn returns, so ready is never closed on panic | `recoverPanic` at **:130-137**; the GoStarted goroutine registers `defer g.wg.Done()` (:123) and `defer g.recoverPanic(name)` (:124) — both run after `fn` exits (LIFO: recoverPanic first), and neither touches `ready` | ✅ **exact** |
| E3 | `group_test.go:130-149` — TestGroup_GoStarted only covers the happy path | `TestGroup_GoStarted` at **:125-141**: fn stores `started`, `close(ready)` (:131), `<-ctx.Done()` (:132); test asserts `started` after a sleep (:135-138) and runs `Shutdown` (:140). No panic-before-ready case, no never-signals case. (`TestGroup_PanicRecovery` at :143-154 exercises **`Go`**, not `GoStarted` — the panic path of the readiness-gated variant is untested) | ✅ **exact** |
| E4 | Repo-wide grep: `aero-vault/internal/shutdown` not imported by any non-test file | `grep -rn "aero-vault/internal/shutdown" --include="*.go" .` minus `internal/shutdown/` → **zero matches** (exit 1); `grep -rn "GoStarted" --include="*.go" .` minus the package → **zero matches**. Dead code confirmed | ✅ **exact** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "GoStarted blocks the caller forever on `<-ready` if fn panics before `close(ready)`" | ✅ **holds, empirically** — bounded repro on this checkout: `go test` with a goroutine-wrapped `GoStarted` (fn panics before close) timed out at 3s with `HANG: GoStarted did not return within 3s after fn panicked` (test failed; goroutine never unblocked; `recoverPanic` swallowed the panic so the suite did not crash) |
| "The deferred recoverPanic runs after the panic, so the goroutine exits cleanly but ready is never closed" | ✅ **holds** (E2) — the panic is recovered inside the goroutine, `wg.Done` runs, and the caller's `<-ready` at :127 blocks forever |
| "No ctx/timeout select — cancelling the root context does not unblock" | ✅ **holds** — :127 is a bare receive; `g.root.Done()` is never selected on anywhere in `GoStarted` |
| "Package is dead code; hang is latent, not yet triggered in production" | ✅ **holds** (E4) — the boot path today (`cmd/server/main.go`) does not import `internal/shutdown` |

**New evidence found beyond the direction's citations (shaped the requirements):**

1. **Double-close hazard in any naive "defer close(ready)" fix.** `TestGroup_GoStarted` (:131) has `fn` close `ready` itself on the happy path; a deferred close that runs on normal return would then close an already-closed channel and panic. In the goroutine, defers run LIFO (close-defer → recoverPanic → wg.Done if registered last), so the spurious panic would be swallowed and logged — but it would log a false "goroutine panicked" and is a test-visible regression risk. REQ-1 therefore mandates **exactly-once** signalling (guard with a flag or `sync.Once`).
2. **`TestGroup_GoStarted` is the regression anchor for the happy path.** Its fn closes ready then waits on `<-ctx.Done()`; after the fix it must still return from `GoStarted` immediately after the signal and complete `Shutdown` cleanly (:140). This pins the normal-path semantics the fix must not disturb.
3. **The acceptance's "bounded timeout" must live in the test, not the production code.** The panic path is deterministic (goroutine exit ⇒ close) and the never-signals path has a deterministic escape hatch (root-ctx cancellation) — both assertable with a test-side `select { case <-done: case <-time.After(bound): t.Fatal }`. A production-side fixed timeout would be a new, arbitrary contract (how long may a legitimate slow starter take?) and is out of scope (§4 D3).
4. **`NewGroup` derives `root` from the caller's ctx** (`group.go:70-72`: `ctx, cancel := context.WithCancel(ctx)`), so cancelling the *parent* context propagates to `g.root.Done()` — the never-signals test can use the fully public API (parent cancel) instead of reaching for the unexported `g.cancel`.

---

## 3. Requirements

All changes are in `internal/shutdown/group.go` (production) and `internal/shutdown/group_test.go` (tests). `group.go` is 202 lines today; the fix adds ≈10 lines, far below the 500-line hard gate.

### REQ-1 — `GoStarted` must unblock when `fn` exits without signalling ready

Modify `GoStarted` (`group.go:116-128`) so the goroutine **guarantees exactly-once signalling** of `ready`:

- If `fn` panics before `close(ready)` (or returns without closing it), `ready` is closed on the goroutine's exit path, so the caller's `<-ready` (or the REQ-2 select) unblocks.
- If `fn` closed `ready` normally, the exit path must **not** close it again — a double `close` on the happy path would panic inside the goroutine (swallowed by `recoverPanic` but logged as a spurious panic). Guard with a closed-flag or `sync.Once`; the normal path (fn closes, then returns) must produce **no panic and no spurious log**.
- The panic itself is still handled by the existing `defer g.recoverPanic(name)` — REQ-1 does **not** change `recoverPanic` (:130-137) or the `wg` accounting (`wg.Done` still runs; `Shutdown`'s `PhaseWait` still waits for the goroutine).
- The caller sees a plain return (no error propagation, no re-panic). Per the acceptance, "returns within a bounded timeout instead of hanging" is the contract; signalling *why* it returned (panic vs. normal exit) is out of scope (§4 D4).

### REQ-2 — `GoStarted` must unblock when the group root context is cancelled, even if `fn` never signals

Replace the bare `<-ready` (:127) with a select on `ready` and `g.root.Done()`:

- `fn` signals → return as today.
- `fn` never signals and the group's root context is cancelled (via `Shutdown`'s `PhaseWorkers` cancel at `group.go:159` — `g.cancel()` — or via cancellation of the parent context passed to `NewGroup`, which propagates through `WithCancel` at :73-75) → return.
- The worker goroutine keeps its lifecycle contract unchanged: it remains tracked by `wg`, and if it ignores `ctx.Done()` the existing `waitWithTimeout` (:174-185) still bounds `Shutdown`'s wait.

### REQ-3 — Two new tests in `internal/shutdown/group_test.go`

Both tests follow the same bounded-window pattern so a regression fails **fast** instead of hanging the suite: call `GoStarted` inside a goroutine, `select` on completion vs. `time.After(2*time.Second)`, `t.Fatal` on timeout. (Names are chosen so the acceptance command `-run GoStarted` matches all three tests.)

**REQ-3a — `TestGroup_GoStarted_PanicBeforeReady`** (new, after `TestGroup_GoStarted` at :141):

1. `g := NewGroup(context.Background(), quietLogger())`.
2. Goroutine: `g.GoStarted("panic-before-ready", func(ctx context.Context, ready chan<- struct{}) { panic("test panic before ready") })`; close a `done` channel after it returns.
3. Assert: `select { case <-done: case <-time.After(2 * time.Second): t.Fatal("GoStarted hung after fn panicked before signalling ready") }` — **this test fails (hangs → timeout-fatal) on today's code** and passes only after REQ-1.
4. Sanity after unblocking: `g.Shutdown(context.Background(), 5*time.Second)` completes without hanging (the panicked goroutine is accounted for in `wg` and `recoverPanic` logs via the quiet logger).

**REQ-3b — `TestGroup_GoStarted_NeverSignalsCancelled`** (new, after REQ-3a):

1. `parent, cancel := context.WithCancel(context.Background())`; `g := NewGroup(parent, quietLogger())` (public-API cancellation; E4-item-4).
2. Goroutine: `g.GoStarted("never-signals", func(ctx context.Context, ready chan<- struct{}) { <-ctx.Done() })` — never closes `ready`; exits when the context cancels so `wg` drains; close a `done` channel after `GoStarted` returns.
3. After a short settle (`time.Sleep(20*time.Millisecond)` to prove the worker is really parked), call `cancel()`.
4. Assert: `select { case <-done: case <-time.After(2 * time.Second): t.Fatal("GoStarted hung after root context cancellation with no readiness signal") }` — **this test fails on today's code** (bare `<-ready` ignores `g.root.Done()`) and passes only after REQ-2.
5. Follow-up `g.Shutdown(context.Background(), 5*time.Second)` completes (worker exits on `ctx.Done()`).

### REQ-4 — Happy-path regression anchor unchanged

`TestGroup_GoStarted` (:125-141) must keep passing byte-unchanged: fn closes ready (:131) → `GoStarted` returns promptly → worker waits on `<-ctx.Done()` → `Shutdown` completes. This pins REQ-1's exactly-once guard (no spurious double-close panic on the normal path) and REQ-2's select (signal wins over cancellation).

---

## 4. Decisions & non-goals

- **D1 — Close-on-exit for the panic path, not re-panic.** Closing `ready` from the goroutine's exit path is the minimal mechanism that satisfies the acceptance ("returns within a bounded timeout") and composes with the existing `recoverPanic` design. Re-panicking into the caller would change `GoStarted`'s signature/contract and is not demanded by any acceptance check.
- **D2 — Exactly-once signalling guard.** Mandated by E4-item-1: the existing happy-path test closes `ready` inside `fn`; an unguarded `defer close(ready)` would double-close on every normal run and log a spurious panic. A `sync.Once` (or closed-flag) keeps the normal path silent.
- **D3 — Test-side bounded window, no production timeout.** A production timeout on readiness would introduce an arbitrary latency contract for legitimate slow starters. The two failure modes have deterministic unblock mechanisms (exit-path close; root-ctx cancellation), so the tests bound the *assertion* with `time.After(2*time.Second)` while production stays event-driven. This also keeps the fix race-clean (`go test -race` requirement).
- **D4 — No API change.** `GoStarted(name string, fn func(ctx context.Context, ready chan<- struct{}))` keeps its signature; no error return, no new exported symbol. Callers (none today, E4) are unaffected when the package is eventually wired into the boot path.
- **Non-goals:** the other directions in `internal-shutdown-8803597d.json` (WaitGroup race, double phase-hook firing, waiter leak in `waitWithTimeout`) are **not** included; no changes to `Go`, `Shutdown`, `recoverPanic`, phases, or `waitWithTimeout`; no changes to `cmd/server/*` (the package remains unwired); no timeout/retry knobs in configuration.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

The three supplied acceptance checks map 1:1; each is a deterministic command plus concrete assertions.

**AC-1 — fn panics before `close(ready)`; `GoStarted` returns within a bounded timeout instead of hanging (REQ-3a / REQ-1).**
*Testable:* `go test ./internal/shutdown/ -run '^TestGroup_GoStarted_PanicBeforeReady$' -count=1` passes. Setup: `GoStarted` in a goroutine, fn does `panic("test panic before ready")` with no `close(ready)`. Assert: `done` fires within `time.After(2*time.Second)` (`t.Fatal` on timeout), then `g.Shutdown(ctx, 5*time.Second)` completes. **On today's code this test fails** (3s-bounded repro already demonstrated the hang on `acfaaf4`).

**AC-2 — fn never signals ready and the group root ctx is cancelled; `GoStarted` returns (REQ-3b / REQ-2).**
*Testable:* `go test ./internal/shutdown/ -run '^TestGroup_GoStarted_NeverSignalsCancelled$' -count=1` passes. Setup: `NewGroup(parent, …)` with cancelable parent; `GoStarted` in a goroutine, fn does `<-ctx.Done()` and never closes `ready`; after a 20ms settle, `cancel()`. Assert: `done` fires within `time.After(2*time.Second)` (`t.Fatal` on timeout), then `g.Shutdown(ctx, 5*time.Second)` completes. **On today's code this test fails** (bare `<-ready` at `group.go:127` never selects on `g.root.Done()`).

**AC-3 — `go test -race ./internal/shutdown -run GoStarted` passes (REQ-3a + REQ-3b + REQ-4 under the race detector).**
*Testable:* `go test -race ./internal/shutdown -run GoStarted -count=1` passes — matches `TestGroup_GoStarted`, `TestGroup_GoStarted_PanicBeforeReady`, `TestGroup_GoStarted_NeverSignalsCancelled`; exercises the fix under `-race` (the `ready` channel + `sync.Once`/flag guard are the race-sensitive spots).

**Completion gate (all ACs):** `go test ./internal/shutdown/ -count=1` (the two new tests plus the full existing package suite — no regressions); `gofmt -l internal/shutdown` empty; `go vet ./internal/shutdown/`. All three are subsets of the `make check` gate; `group.go` stays ≈212 lines, well under the 500-line hard gate.

---

## 6. Risks

- **Fix regresses the happy path.** A naive `defer close(ready)` would panic on `TestGroup_GoStarted`'s normal close (double close), swallowed by `recoverPanic` and logged as a spurious panic. Mitigated by REQ-1's exactly-once guard and REQ-4's unchanged anchor test; AC-3 runs the anchor under `-race`.
- **Select race between signal and cancellation.** In REQ-2, `ready` and `g.root.Done()` may both be ready; either branch returning is correct (the caller must not wait for the worker's post-signal lifecycle — `wg` + `waitWithTimeout` own that). No assertion depends on which branch wins, so the select is race-free by construction; AC-3 confirms under `-race`.
- **Test hangs instead of failing on regression.** If the fix is ever reverted, a naive test that called `GoStarted` directly would hang the whole `go test` run until the 10m default timeout. Mitigated by the goroutine-wrapped call + 2s bounded `select` in both new tests — regression produces a fast, named `t.Fatal`, not a suite hang.
- **Scope creep from the sibling directions.** The analysis file contains three more defects on the same module (WaitGroup race, double hook firing, waiter leak); the implement stage must not fold them into this change. Non-goals in §4 bind.
- **Dead-code status masks boot-path impact.** The hang is latent today (E4), so the new tests are the only guard; if the package is later wired into `cmd/server/main.go` with a panic-prone initializer, the pre-fix behavior would hang the boot path. The fix removes the trap before any caller exists — that is the direction's stated risk-reduction value.

*Verification basis: all cited line numbers re-confirmed on this checkout (`acfaaf4`); the hang was reproduced empirically with a 3s-bounded repro test (FAIL: "HANG: GoStarted did not return within 3s after fn panicked"); dead-code status re-confirmed by repo-wide grep (zero imports of `aero-vault/internal/shutdown` outside the package, zero callers of `GoStarted`).*
