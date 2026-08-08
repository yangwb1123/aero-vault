# Design — `internal/shutdown`: fix `GoStarted` startup deadlock on panic / never-signalled readiness

**Module:** `internal/shutdown` · **Design for spec:** `docs/requirements/internal-shutdown-gostarted-deadlock-v1.spec.md` (REQ-1…REQ-4, AC-1…AC-3)
**HEAD:** `acfaaf4` · **Date:** 2026-08-07 · **Effort:** ~1h (validated prototype already written and reverted)

---

## 0. Prior-attempt disposition (gate requirement)

The design gate re-checks every outstanding finding from `docs/auto/runs/`. Findings reviewed:

| Source | Finding | Disposition (with evidence) |
|---|---|---|
| `docs/auto/runs/fix-gostarted-startup-deadlock-when-worker-panic-8fc438b0/DECISIONS.md` | Only entry: `stage 'requirements' — PASS` (2026-08-07 00:41:37). No design-gate verdict exists for this direction — **this is the first design attempt** | No outstanding findings to resolve. Design below implements the spec's REQ-1…REQ-4 verbatim in intent |
| Sibling runs with similar name | Repo-wide grep of `docs/auto/runs/` for `GoStarted` matches **only this pipeline's** `pipeline.yaml` + requirements artifact; `ls docs/auto/runs/ | grep -iE "shutdown|gostarted"` → only `fix-gostarted-startup-deadlock-when-worker-panic-8fc438b0`; no `archive/` dir | No sibling attempts exist; nothing to reconcile |
| Spec §4 decisions D1–D4 | D1 close-on-exit (no re-panic) · D2 exactly-once guard · D3 test-side bound, no production timeout · D4 no API change | All honored (§1–§3 below). **D2 refined with a new finding:** a bare `defer sync.Once(close)` is *insufficient* — see §2.2 |
| Spec §4 non-goals | WaitGroup race, double phase-hook firing, waiter leak in `waitWithTimeout`, changes to `Go`/`Shutdown`/`recoverPanic`/`cmd/server/*` — all excluded | Excluded. Design touches only `GoStarted` in `group.go` + two new tests in `group_test.go` |
| Spec §6 risks | happy-path double-close · select race · test-hang-on-regression · scope creep · dead-code masking | Each has an explicit mitigation (§5) — all empirically validated (§7) |

**No blocking findings exist; no open items are carried into the gate.**

---

## 1. Design overview

`GoStarted` (`internal/shutdown/group.go:116-128`) is the readiness-gated startup primitive. Today: `ready := make(chan struct{})` (:120) → worker goroutine with `defer wg.Done` + `defer recoverPanic` (:123-124) → caller blocks on bare `<-ready` (:127). Two permanent-block failure modes (both **reproduced empirically on `acfaaf4`** in this design stage, 2s-bounded repro tests → `HANG: GoStarted did not return within 2s…`):

1. **Panic before signal** — `recoverPanic` (:130-137) runs after `fn` exits; `close(ready)` never happens → caller (boot path) hangs forever.
2. **Never signals** — bare `<-ready` never selects on `g.root.Done()`; root-ctx cancellation (`Shutdown`'s `PhaseWorkers` cancel at :159, or parent-ctx cancel propagating through `NewGroup`'s `WithCancel` at :73-75) does not unblock the caller.

The design makes `GoStarted`'s return *event-driven* on three conditions (signal · worker exit · root cancel) with exactly-once, double-close-safe signalling; no production timeout; no API change. Package is verified **dead code** (zero imports of `aero-vault/internal/shutdown` outside the package; zero `GoStarted` callers — repo-wide grep, exit 1), so the fix is a latent-trap removal with no caller migration burden.

---

## 2. Concrete change

### 2.1 Production — `internal/shutdown/group.go` (`GoStarted`, :116-128)

```go
func (g *Group) GoStarted(name string, fn func(ctx context.Context, ready chan<- struct{})) {
	g.mu.Lock()
	g.names = append(g.names, name)
	g.mu.Unlock()
	ready := make(chan struct{})
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer g.recoverPanic(name)
		defer func() {
			select {
			case <-ready:
			default:
				close(ready)
			}
		}()
		fn(g.root, ready)
	}()
	select {
	case <-ready:
	case <-g.root.Done():
	}
}
```

**+10 lines** (202 → 212 for the §2.1 hunk verbatim, as measured by the design gate; the final file is 220 lines including the reviewer-mandated method-contract comment and the probe hand-off comment — well under the 500-line hard gate). No new imports (`sync`, `context`, `time` already imported).

**Mechanics, in defer-LIFO order (registered: `wg.Done` → `recoverPanic` → close-probe):**

- **Happy path** (fn closes `ready` at :131, then returns): probe's non-blocking receive succeeds → no-op; `recoverPanic` no-op; `wg.Done`. **No double close, no spurious panic log** — empirically verified (§7, log-check: 0 `panicked` logs).
- **Panic before signal**: probe (runs first in LIFO) closes `ready` → caller unblocks; `recoverPanic` catches and logs the genuine panic exactly once (verified: exactly 1 log); `wg.Done` still runs so `Shutdown`'s `PhaseWait`/`waitWithTimeout` (:174-185) accounts for the goroutine.
- **Plain return without signal**: identical exit path (the probe runs on *any* exit) — covered by the same mechanism.
- **Never signals**: caller's `select` returns on `g.root.Done()` (root cancelled via `Shutdown` :159 or parent-ctx cancel through `NewGroup` :73-75). The worker goroutine remains `wg`-tracked; if it ignores `ctx.Done()`, `waitWithTimeout` still bounds `Shutdown`.

### 2.2 Key design finding — why `sync.Once` alone is insufficient (refines spec REQ-1 / D2)

The spec's REQ-1 permits "a closed-flag or `sync.Once`". **A deferred `once.Do(close)` does not work here**: `fn` receives the raw `chan<- struct{}` and closes it *directly* (the anchor test does `close(ready)` at `group_test.go:131`), so `fn`'s close never routes through the `Once` — the deferred `once.Do` would then fire and close the already-closed channel → panic inside the goroutine (swallowed by `recoverPanic`, but logged as a spurious "goroutine panicked" on every normal run).

The correct exactly-once mechanism is a **non-blocking receive probe** (`select { case <-ready: default: close(ready) }`): channel closedness *is* the closed-flag. It is race-free by construction — `fn`'s `close(ready)` happens-before `fn` returns (program order, same goroutine), which happens-before the defer runs — and there is no other writer of `ready` (the caller only receives). `go test -race` confirms (AC-3, §7).

### 2.3 Tests — `internal/shutdown/group_test.go` (158 → 203 lines)

Two new tests, placed after `TestGroup_GoStarted` (:141), both using the goroutine-wrap + 2s bounded `select` pattern (regression → fast named `t.Fatal`, never a suite hang). Names chosen so `-run GoStarted` matches all three (AC-3).

**`TestGroup_GoStarted_PanicBeforeReady`** (REQ-3a / AC-1): `NewGroup(context.Background(), quietLogger())`; goroutine calls `GoStarted` with `fn` = `panic("test panic before ready")` (no close), closes `done` after return; assert `done` within `time.After(2*time.Second)` else `t.Fatal`; then `g.Shutdown(ctx, 5*time.Second)` completes.

**`TestGroup_GoStarted_NeverSignalsCancelled`** (REQ-3b / AC-2): `parent, cancel := context.WithCancel(...)`; `NewGroup(parent, …)` (fully public API — `NewGroup` derives `root` via `WithCancel`, so parent cancel propagates); goroutine calls `GoStarted` with `fn` = `<-ctx.Done()` (never closes `ready`; exits on cancel so `wg` drains), closes `done` after return; `time.Sleep(20*time.Millisecond)` settle, then `cancel()`; assert `done` within `time.After(2*time.Second)`; then `g.Shutdown(ctx, 5*time.Second)` completes.

**`TestGroup_GoStarted` (:125-141) unchanged** — the happy-path anchor (REQ-4): fn closes `ready` → `GoStarted` returns promptly → worker waits on `<-ctx.Done()` → `Shutdown` completes. Pins the exactly-once probe (no double-close) and the select (signal wins over cancellation — either branch returning is correct, but the test exercises the signal path).

---

## 3. API changes and compatibility constraints

| Aspect | Constraint |
|---|---|
| **Signature** | **No change** (D4): `GoStarted(name string, fn func(ctx context.Context, ready chan<- struct{}))`. No error return, no new exported symbol, no new config |
| **Behavioral contract (documented in the method comment)** | `GoStarted` now returns when *any* of: (a) `fn` signals `ready`; (b) `fn` exits (panic or plain return) without signalling; (c) `g.root` is cancelled. Previously only (a). This is a strict superset of unblocking behavior — no caller can depend on the removed permanent-block semantics (and there are zero callers today, §1) |
| **`wg` accounting** | Unchanged: `wg.Add(1)` still precedes the caller's wait; `wg.Done` still runs on every exit; `waitWithTimeout` still bounds `Shutdown`. Caller returning from `GoStarted` never implied worker completion — still true |
| **`recoverPanic` / `Go` / `Shutdown` / phases / `waitWithTimeout`** | Untouched (non-goal) |
| **Dead-code status** | Package remains unwired; `cmd/server/*` untouched. Any future wiring must pair readiness callbacks with either a reliable signal or `ctx.Done()` awareness — the panic/never-signal trap is now removed before any caller exists |
| **Edge: `GoStarted` called after root already cancelled** | Returns immediately on `g.root.Done()` (previously: would block forever unless `fn` signalled). Strictly a hang → return improvement; cannot break a caller |

---

## 4. Failure modes

| Mode | Behavior | Status |
|---|---|---|
| `fn` panics before `close(ready)` | Probe closes `ready` on exit path → caller unblocks; `recoverPanic` logs exactly once; `wg.Done` runs | **Fixed** (REQ-1) — was permanent hang |
| `fn` returns without signalling | Same exit path as panic (probe runs on any exit) | **Fixed** (REQ-1) |
| `fn` never signals; root never cancelled | `GoStarted` still blocks. **By design** (D3: no production timeout — a fixed timeout would be an arbitrary latency contract for legitimate slow starters) | Documented limitation; escape hatch = root cancellation or caller-side goroutine-wrap + select |
| Double-close on happy path | Prevented by probe (channel closedness = flag). Verified: 0 spurious logs on happy path, exactly 1 genuine log on panic path | **Prevented** |
| Signal ∧ cancellation simultaneously ready | `select` takes either branch; both correct — caller must not wait for worker post-signal lifecycle (`wg` owns that). No assertion depends on branch choice | Race-free by construction; AC-3 under `-race` |
| `fn` closes `ready` then panics | Caller already returned on signal; probe no-ops; `recoverPanic` logs; `wg.Done` | Correct |
| Regression (fix reverted) | New tests fail fast with named `t.Fatal` at 2s each — never a suite hang | Tripwire tests |

---

## 5. Migration steps

1. **No data/schema/config migration.** No DB, no storage, no env vars, no wire protocol touched. Package is internal and dead code (§1).
2. **Code migration:** apply §2.1 hunk + §2.3 tests; run gates (§6). Rollback = revert the one hunk; the two new tests become the 2s tripwire.
3. **Future wiring note (documentation only):** when `internal/shutdown` is eventually imported by `cmd/server/main.go`, `GoStarted` callers get panic-safe startup for free; callbacks that cannot signal promptly must still select on `ctx.Done()` for prompt cancellation, and `Shutdown`'s `PhaseWorkers` cancel is the system-wide unblock.

---

## 6. Testable acceptance mapping (each verified in §7)

| Acceptance (from direction) | Test / command | Assertion |
|---|---|---|
| **AC-1** fn panics before `close(ready)`; `GoStarted` returns within bounded timeout | `go test ./internal/shutdown/ -run '^TestGroup_GoStarted_PanicBeforeReady$' -count=1` | `done` fires within `time.After(2*time.Second)` (else `t.Fatal`); then `g.Shutdown(ctx, 5*time.Second)` completes. **Fails (HANG) on today's code; passes post-fix** |
| **AC-2** fn never signals; root ctx cancelled; `GoStarted` returns | `go test ./internal/shutdown/ -run '^TestGroup_GoStarted_NeverSignalsCancelled$' -count=1` | 20ms settle proves worker parked; after `cancel()`, `done` fires within 2s; `Shutdown` completes. **Fails (HANG) on today's code; passes post-fix** |
| **AC-3** `go test -race ./internal/shutdown -run GoStarted` passes | `go test -race ./internal/shutdown -run GoStarted -count=1` | Matches all three tests by name; race-clean (probe + select + `wg`) |
| **REQ-4** happy-path anchor | `go test ./internal/shutdown/ -run '^TestGroup_GoStarted$' -count=1` | Unchanged test passes byte-identical |
| **Hard gates** | `gofmt -l internal/shutdown` (empty) · `go vet ./internal/shutdown/` · `go build ./...` · `go test ./internal/shutdown/ -count=1` (full package suite, 8 existing + 2 new) | All pass; `group.go` 220 lines, `group_test.go` 203 — under 500 |

---

## 7. Empirical validation performed (this checkout, `acfaaf4`)

All in the design stage, then **reverted** (tree is back to baseline; `git status --short internal/shutdown/` empty):

1. **Pre-fix reproduction** — temp `zz_repro_test.go` with the exact REQ-3a/REQ-3b shapes: both tests **FAILED** on today's code with `HANG: GoStarted did not return within 2s after fn panicked` and `…after root ctx cancellation` (4.02s total — bounded, no suite hang).
2. **Prototype fix** (§2.1 applied): both repro tests **PASS** (0.00s / 0.02s); full package suite passes (all 8 existing tests + anchor); `gofmt` clean; `go vet` clean.
3. **AC-3 shape**: `go test -race ./internal/shutdown -run GoStarted -count=1` → `ok … 1.057s`.
4. **Exactly-once log check** (temp test, visible logger): happy path → **0** "panicked" logs (no double-close); panic path → **exactly 1** (genuine panic). This is the evidence for the §2.2 finding (why a bare `sync.Once` defer fails).
5. **Baseline gates**: `go build ./...` exit 0; `go test ./internal/shutdown/ -count=1` → `ok`; `gofmt -l internal/shutdown/` empty; `go vet ./internal/shutdown/` clean.
6. **Dead code**: `grep -rn "aero-vault/internal/shutdown" --include="*.go" .` (minus package) → zero matches; `GoStarted` callers outside package → zero.

All spec evidence-citations (E1–E4, line numbers) re-verified verbatim against the checkout: `GoStarted` :116-128, `<-ready` :127, defers :123-124, `recoverPanic` :130-137, `TestGroup_GoStarted` :125-141, `TestGroup_PanicRecovery` :143-154.
