# Design: Fix silent 32 MiB truncation in SignatureScanner — streaming matcher + HTTPScanner-only drain

> **Companion spec:** `docs/requirements/signature-scanner-32mib-truncation-v1.md` (FR-1…FR-4, AC-1…AC-3) · **Module:** `internal/antivirus` only (2 production files + 1 new test file) · **Status:** design (not implemented) · **Baseline:** HEAD `acfaaf4` + uncommitted WIP (quarantine-outbox direction implemented in-tree) · **Gates:** `make check` green · non-test files ≤ 500 lines (worker.go 201 → ~205, antivirus.go 110 → ~135) · stdlib only (I6) · zero `go.mod` changes · **zero DB migrations · zero config keys · zero wire-level API changes**

---

## 1. Evidence re-verification (independent, against working tree)

All 3 direction citations and the requirements spec's own corrections were re-checked directly:

| # | Claim | Verified location | Verdict |
|---|-------|-------------------|---------|
| E1 | `SignatureScanner.Scan` reads `io.LimitReader(r, 32<<20)` and returns `{Clean: true}` on overflow with no truncation signal | `internal/antivirus/antivirus.go:55-67` — `const max = 32 << 20` (:58), `io.ReadAll(io.LimitReader(r, max))` (:59), `bytes.Contains` loop (:62-65), `return Result{Clean: true}, nil` (:66) | ✅ (line drift from cited :74-90 confirmed — that range is `HTTPScanner`; substance exact) |
| E2 | Worker unconditionally drains remainder after scan → wasted full-object read on signature path | `internal/antivirus/worker.go:164` — `_, _ = io.Copy(io.Discard, rc) // drain remainder` immediately after `w.scanner.Scan(ctx, rc)` (:160) and its error check; clean-tag write follows at :174-175, quarantine at :190-194 | ✅ (line drift from cited :96-101 confirmed) |
| E3 | `APP_MAX_BODY_SIZE` defaults to 0 = unlimited → >32 MiB uploads reachable | `internal/config/config.go:48` (`MaxBodySize … (0 = unlimited)` — exact), :83 (`getEnvInt("APP_MAX_BODY_SIZE", 0)`); `internal/middleware/validation.go:16` (`maxBytes <= 0` → pass-through); pinned by `validation_test.go:14-25` `TestMaxBodySize_DisabledWhenZero` | ✅ exact |
| S1 | `Scanner.Scan` has exactly one call site | `grep` of non-test `.Scan(` → only `worker.go:160` (`w.scanner.Scan(ctx, rc)`); all other hits are `sql.Rows.Scan`/`bufio.Scanner` in unrelated packages | ✅ |
| S2 | Worker uses `w.store.Get` directly (bypasses FileService), `defer rc.Close()` after scan | `worker.go:150-156` (`rc, _, err := w.store.Get(...)` + `defer rc.Close()`) | ✅ |
| S3 | `setupSvc` + `NewWorker(repo, svc.Storage(), …).WithObjectController(svc)` is the established test assembly; `TestWorkerQuarantinesInfected` pins quarantine assertions | `antivirus_test.go:102-122` (`setupSvc` :66-79); quarantine = `repo.GetObject → ErrNotFound` + `quota.UsedBytes/UsedObjects == 0` | ✅ |

**Prototype validation:** the proposed sliding-window matcher was prototyped and executed outside the tree (6 checks): EICAR at >32 MiB offset → infected; EICAR split across 7-byte chunk boundaries → infected; 33 MiB clean stream fully consumed → clean; empty stream → clean; pre-canceled context → error; custom signature in tail → infected. **All passed.**

**Empirical check of the HTTP drain** (httptest server responding without reading the body, 8 MiB request): after `client.Do` returns, Go's transport has stopped consuming the request body (only the socket-buffer prefix is sent); an `io.Copy(io.Discard, body)` then deterministically reads the remainder to EOF with no error (two runs: 7.34/7.93 MiB drained). Conclusions pinned into the design: (a) the drain's role is client-side hygiene/connection reuse — it does **not** guarantee the remote service received the full object when the service responds early (a protocol-level property of the remote scanner, out of scope per spec §5); (b) a client-side "storage reader fully consumed" assertion is deterministic and is the right acceptance invariant for the HTTPScanner path; (c) no concurrent-consumption race between the transport and the drain after `Do` returns.

> **Implement-time correction (Go 1.26.5, re-verified with a probe):** the drain-to-EOF result above holds only when the request body's `Close` is a no-op. With a **closable** body (the production shape: `LocalStorage.Get` returns `*os.File`), Go's transport **closes the request body itself** when the remote responds early, so the worker's drain reads nothing (`file already closed`, ignored by `_, _ = io.Copy`) — and when the remote reads the full body, the transport already consumed it, so the drain reads nothing either. The drain is therefore pure client-side hygiene on both branches and cannot be distinguished by byte counts with a closable reader; the AC-3c harness pins it deterministically by handing the worker a no-op-close reader (`noCloseCountingStore`, §8). Production behavior is unaffected: the drain never errors the job and never re-reads the object on the signature path.

---

## 2. Prior-attempt disposition (docs/auto/runs siblings — every finding addressed)

| Run | Stage verdict | Disposition for this design |
|-----|---------------|-----------------------------|
| `fix-silent-32-mib-truncation-in-signaturescanner-ae567cf6` (own) | requirements **PASS**; no prior design exists (design-a77de8a6 is this artifact) | Spec FR-1…FR-4 accepted as-is; §4 acceptance mapped 1:1 in §7 below |
| `route-antivirus-worker-mutations-tag-write-quara-27bd11cc` | design_gate **FAIL** — blocking Security Finding 1: a scoped `PrincipalAntivirus` authorization rule would default-deny the indexer/replication workers (kept on `PrincipalSystem`) under `ACCESS_ENABLED`, silently losing `pii_scan`/`replicated` tags | **Not applicable — that direction was never implemented** (DECISIONS.md ends at design_gate FAIL; no implement stage exists). The working tree still carries the blanket `PrincipalSystem → allow` short-circuit (`internal/access/authorizer.go:24-27`, verified) that the finding required preserving. This design makes **zero changes outside `internal/antivirus`** — no authorization code, no access package. The in-tree WIP `SystemActor` pinning (`worker.go:22-42`, `access.WithPrincipal`, `Kind: PrincipalSystem`) comes from the *implemented* quarantine-outbox direction, whose own security reviews verified it is attribution-only, kind-locked, and non-elevating; this design does not touch it. Re-check criterion (System-kind preservation tests / authorizer behavior) is unaffected because no authorization surface changes. |
| `route-antivirus-worker-mutations-…` concurrency findings R1–R7 | R1 (tags lost-update), R2 (overwrite race) pre-existing; R3–R7 "None invalidate design A–H" per reviewer | R1 is analysis-JSON direction #2 — explicitly out of scope per spec §5 (this design changes no tag merge or quarantine ordering). R2 (stale-verdict overwrite race) pre-exists and is untouched: scan/tag/quarantine sequencing in `ScanObjectByID` is byte-for-byte preserved. R3–R7 non-blocking per the reviewer's own verdict; nothing in this design interacts with them (no reader lifecycle change other than the gated drain; `rc.Close()` still deferred in all paths). |
| `quarantine-path-becomes-the-pilot-producer-for-v-f5cf68c0` | design_gate **PASS**, implement **PASS**, acceptance **PASS** | Its in-tree tests must stay green: `TestScanObjectByIDQuarantineWritesAuditAndOutbox`, `TestQuarantineJobCompletesWithoutRelayThenRelayDrainsDisjoint`, `TestQuarantineFactGoldenBytes`, `TestQuarantineCompositionE2E`, `TestScanObjectByIDRejectsTenantMismatch`, `TestScanObjectByIDBoundsOversizedSignature`. This design changes only `SignatureScanner.Scan` internals and the drain gate; the quarantine/audit/outbox/tenant-guard paths are untouched. `TestScanObjectByIDBoundsOversizedSignature` drives `HTTPScanner` — the drain is preserved on that path, so the test's behavior is unchanged. |
| `fix-search-over-retrieve-k-2-colliding-with-qdra-0c0a987c` (memory index) | implement FAILED (validation) | Different module (`internal/ai`/Qdrant retrieve-k); no shared surface with `internal/antivirus`. Unrelated; no disposition required. |

No outstanding blocking finding applies to this design; the two previously-blocking items are (a) a never-merged direction and (b) explicitly out-of-scope directions #2/#3 of the analysis JSON.

---

## 3. Design decision: streaming matcher (option (a)), not explicit truncation signal (option (b))

The spec permits either; only (a) satisfies the full requirement set:

- **FR-3 + AC-2 branch 1** (tail EICAR → `infected` + quarantine) is *unsatisfiable* under (b): an unscanned object must not be quarantined, so a >32 MiB object carrying tail malware would never be caught by the local scanner — the direction's core security property is lost.
- Under (b), *every* >32 MiB object (including legitimate ones) can never be verified clean: `Scan` errors → job retries with backoff → terminal `failed` after `MaxAttempts` (5), permanent absence of `av_status`, queue churn (5 attempts × every large object), and no sweep exists to recover (direction #3, out of scope).
- Under (a), storage I/O is **identical to today**: the worker already reads the full object (32 MiB scan + unconditional drain); the matcher simply sees the bytes that were previously discarded. Wall time ≈ unchanged; peak memory *drops* from 32 MiB to ~128 KiB (window + chunk).
- (a) keeps `Result`/`Scanner`/`EICAR`/`HTTPScanner` surfaces untouched and makes ≤32 MiB behavior bit-identical.

---

## 4. API changes

### 4.1 Wire-level / config — none
No REST/S3/MCP/WebDAV/OpenAPI/env/flags. No DB schema. No tag-schema change (`av_status`, `av_signature` written exactly as today).

### 4.2 Go-level — `internal/antivirus` only

**A. `antivirus.go` — `SignatureScanner.Scan` internals replaced** (public surface, `Result`, `EICAR`, `HTTPScanner`, `NewSignatureScanner` unchanged):

```go
func (s *SignatureScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	// Streaming matcher: single pass over the whole stream, O(maxSigLen) memory.
	// The window keeps the last maxSigLen-1 bytes; every signature occurrence is
	// fully contained in window∪chunk at the iteration where its final byte
	// arrives (its start stays in the window until then), so no offset is
	// missed. Clean is returned only after the stream reports EOF — a partial
	// read can never produce a clean verdict.
	maxSigLen := 0
	for _, sig := range s.sigs {
		if len(sig) > maxSigLen {
			maxSigLen = len(sig)
		}
	}
	if maxSigLen == 0 {
		return Result{Clean: true}, nil // no signatures configured
	}
	win := make([]byte, 0, maxSigLen-1+64<<10)
	chunk := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		n, err := r.Read(chunk)
		if n > 0 {
			win = append(win, chunk[:n]...)
			for name, sig := range s.sigs {
				if len(sig) > 0 && bytes.Contains(win, sig) {
					return Result{Clean: false, Signature: name}, nil
				}
			}
			if keep := maxSigLen - 1; len(win) > keep {
				win = append(win[:0], win[len(win)-keep:]...) // trim front; overlap-safe (memmove semantics)
			}
		}
		if err != nil {
			if err == io.EOF {
				return Result{Clean: true}, nil
			}
			return Result{}, err
		}
	}
}
```

Design notes:
- **`const max = 32 << 20` is deleted.** The old comment's premise ("large binaries are streamed past for a remote engine") is replaced by the streaming-matcher contract.
- **Memory bound:** window ≤ `maxSigLen-1 + 64 KiB`; chunk 64 KiB reused. Strictly better than the old 32 MiB `io.ReadAll` allocation.
- **Correctness of the window:** for a signature of length `L ≤ maxSigLen` starting at offset `p`, every chunk iteration covers `[p, p+L)` as long as `p` lies within the last `maxSigLen-1` bytes of the previous window (prototype-validated incl. 7-byte chunk boundaries).
- **`ctx.Err()` check per iteration** (new): strictly more correct; inert in practice because the job pool reuses one long-lived context across jobs/retries (sibling reviewer R6 finding — no per-job cancellation). Adds a deterministic test.
- **Read errors** mid-stream propagate (`err != io.EOF` → `Result{}, err`), so a partial read with an error is never reported clean.
- `bytes.Contains(win, sig)` per chunk per signature: memchr-class cost, O(total bytes) across the stream; the region previously drained (not matched) now gets matched — CPU add is negligible vs. the storage I/O that already happened.

**B. `worker.go` — drain gated to `HTTPScanner` only** (position unchanged, right after the `Scan` error check at :160-163; final-review correction: the drain is pure client-side hygiene in production — Go's transport closes closable request bodies on early response, so the AC-3c harness pins gate *presence* with a no-op-close reader, see §1/§8):

```go
	// The HTTP scanner streams the object as the in-flight POST body; count
	// every byte pulled so a remote engine that answers before receiving the
	// whole object is detectable (a client-side lower bound on what it saw).
	// The signature scanner consumes the whole stream inside Scan and is not
	// wrapped — zero overhead on the built-in path. Atomic: after Do returns
	// the transport may keep reading the body from a background goroutine
	// (e.g. when the body cannot be closed), racing with the worker's drain.
	var scanned atomic.Int64
	if _, ok := w.scanner.(*HTTPScanner); ok {
		rc = &scanCounter{ReadCloser: rc, n: &scanned}
	}
	defer rc.Close()

	res, err := w.scanner.Scan(ctx, rc)
	// ... error check ...
	// Drain only for the HTTP scanner: the object stream is the in-flight POST
	// body, and consuming the remainder lets the transport finish/clean up the
	// request (client-side hygiene; the remote service decides how much it
	// reads). The signature scanner consumes the whole stream inside Scan, so
	// draining here would re-read the object for nothing.
	if _, ok := w.scanner.(*HTTPScanner); ok {
		_, _ = io.Copy(io.Discard, rc)
		// A remote engine may answer before consuming the request body; the
		// transport then stops reading and closes a closable body early, so
		// fewer bytes than the object size reached it. The verdict may cover
		// only a prefix: surface it as a warning rather than silently tagging
		// the object clean (the truncation fix is client-side; this is the
		// operator-visible signal for the remote-side residual).
		if scanned.Load() < obj.Size {
			w.logger.Warn("antivirus: remote scanner responded before receiving the full object", …)
		}
	}
```

- Type switch on the concrete `*HTTPScanner` (both scanners live in this package; `Scanner` interface stays untouched — spec FR-1 forbids interface changes). Comment documents that any future scanner returning before consuming its stream must be added to the drain set.
- **FR-2 is satisfied without worker-logic changes:** with the streaming matcher, `Scan` returns `Clean:true` only after EOF, so the existing `res.Clean → tags[TagStatus]="clean"` write (:174-175) is by construction "clean only after full scan". Read/context errors propagate (`fmt.Errorf("scan %q: %w", …)`) → job retry/terminal failure, **no tag written** — the fail-closed path already in place.
- **Security-F2 hardening (implement-time fold-in):** the counter is a client-side lower bound — `scanned < obj.Size` is possible only if the transport did not send the whole body, so the remote engine cannot have scanned it all. Verdicts are unchanged; the warn is log-only, and fires on the real (closable) backend shape where the transport closes the body on early response.

**C. New test file `internal/antivirus/truncation_test.go`** (tests are excluded from the 500-line gate, but a separate file keeps this direction reviewable and the 687-line `antivirus_test.go` untouched). All helpers (`setupSvc`, `quietLogger`) reused from the package.

---

## 5. Compatibility constraints

| Surface | Constraint |
|---|---|
| `Scanner` / `Result` / `EICAR` / `NewSignatureScanner` / `NewHTTPScanner` / `HTTPScanner` | Unchanged signatures and semantics |
| ≤32 MiB objects | Bit-identical verdicts (prototype-validated; `TestSignatureScannerEICAR`/`TestSignatureScannerExtra` must stay green unmodified) |
| Worker tag/quarantine path, WIP `SystemActor`/`WithObjectController`/`maxSignatureBytes`/tenant guard | Untouched (worker diff is only the 4-line drain gate) |
| `TestScanObjectByIDBoundsOversizedSignature` (HTTPScanner) | Drain preserved → unchanged behavior |
| Remote scanner protocol | Untouched; early-response prefix-scan is the remote service's protocol property (out of scope, spec §5) |
| Concurrency | No new shared state: matcher is per-call; window/chunk are stack-local; no lock added |
| I6 / deps / config / migrations | Zero new imports beyond existing package imports (`bytes`, `io`, `context` already imported in `antivirus.go`); no config keys; no migrations |

---

## 6. Failure modes

| # | Mode | Behavior | Severity |
|---|------|----------|----------|
| F1 | Storage read error mid-stream | `Scan` returns error → worker propagates → job retry/terminal `failed`, **no tag written** (fail-closed). Same shape as today's read-error path; the silent-truncation false-clean is eliminated | resolved by design |
| F2 | Context canceled mid-scan | `Scan` returns `ctx.Err()` → same fail-closed path (new behavior; inert under the pool's long-lived ctx) | benign |
| F3 | Multi-GB object | Scan reads the full object — but storage I/O is unchanged vs. today (drain already read it); only the matcher now sees the tail. Wall time ≈ unchanged; CPU add negligible | benign, documented |
| F4 | Memory | ≤ `maxSigLen + 64 KiB` per scan (was 32 MiB). Operator-configured oversized custom signature only grows the window to that signature's length | benign |
| F5 | Multiple signatures match | Reported name follows map iteration order — same nondeterminism as today (old code also iterated a map); tests use non-overlapping signatures | pre-existing, unchanged |
| F6 | Pathological reader returning `(0, nil)` | Busy loop — identical to `io.ReadAll`'s own behavior; no real backend does this | pre-existing, unchanged |
| F7 | HTTPScanner early-responding remote service | Remote may scan only the prefix (protocol-level property). Drain retained for client hygiene; verdict correctness on that path is the remote service's contract | out of scope (spec §5) |
| F8 | Regression to ≤32 MiB paths | Existing suite (16 tests incl. WIP) pins EICAR/custom/clean/HTTPScanner behavior; new tests pin tail/boundary/consumption | guarded by AC-3a |

---

## 7. Migration steps

1. **No schema/config/data migration.** Single-binary rollout; behavior is self-contained in `internal/antivirus`.
2. **Operational note (no action required):** objects already tagged `av_status=clean` that are >32 MiB were never fully scanned; this fix does not retroactively rescan them (rescan sweep = analysis-JSON direction #3, out of scope). Operators wanting closure can re-upload or await that direction.
3. **Rollback:** revert the commit. Tag shape is identical on both sides (`av_status`/`av_signature`), so no data compatibility issue; note that rolling back re-opens the silent-truncation window for *new* scans.
4. **Release ordering:** none required relative to the in-tree quarantine-outbox WIP — the two change sets are orthogonal (different code regions; the WIP's tests pass unchanged, AC-3a).

---

## 8. Testable acceptance mapping (spec §4 → tests; each red-today proof)

All new tests live in `internal/antivirus/truncation_test.go` (functional) and `internal/antivirus/perf_test.go` (F5 performance evidence); helpers `setupSvc`/`quietLogger` from `antivirus_test.go`.

| Spec AC | Test | Assertion | Red today? |
|---|---|---|---|
| AC-1 | `TestSignatureScannerTailBeyond32MiB` | `NewSignatureScanner(nil)` on `io.MultiReader(bytes.NewReader(make([]byte, 32<<20)), strings.NewReader(EICAR))` → `err == nil`, `!res.Clean`, `res.Signature == "EICAR-Test-File"` | ✅ today returns `{Clean:true}` (tail never read) |
| AC-2 branch 1 | `TestScanObjectByIDTailEICARNeverClean_QuarantineOn` | `setupSvc` + Put 32 MiB zeros + EICAR tail (`size = 32<<20 + len(EICAR)`); worker with `quarantine=true`; assert `repo.GetObject → ErrNotFound` and `quota.UsedBytes/UsedObjects == 0` (per `TestWorkerQuarantinesInfected` :102-122) | ✅ today writes `clean`, never quarantines |
| AC-2 branch 2 | `TestScanObjectByIDTailEICARNeverClean_NoQuarantine` | Same object, worker `quarantine=false`; assert object present and `tags[TagStatus]=="infected"`, `tags[TagSignature]=="EICAR-Test-File"` | ✅ today writes `clean` |
| AC-3a | `go test ./internal/antivirus` | full existing suite + WIP tests + new tests green; `gofmt`/`vet`/`build` clean | — |
| AC-3b | `TestScanObjectByIDNoDrainForNonHTTPScanner` | Counting storage decorator (`countingStore` embedding `storage.Storage`, overriding `Get` to wrap the reader) + fake head-only scanner (reads exactly 1 KiB via `io.ReadFull`, returns `{Clean:true}`); assert post-`ScanObjectByID` bytes read `== 1024` (scanner consumption only) | ✅ today the unconditional drain reads the whole remainder → `== size` |
| AC-3c | `TestScanObjectByIDHTTPScannerConsumesWholeStream` | `httptest` server responding immediately **without** reading the body (`{"clean":true}`); 8 MiB object; **no-op-close** counting store (transport closes closable bodies on early response — §1 correction); assert bytes read `== size` after `ScanObjectByID` — pins the HTTPScanner-path drain (after `Do` returns the transport stops consuming; the drain deterministically consumes the remainder) | red if the drain were removed; green today and after |

**Design-pinning unit tests** (prototype-validated, guard the algorithm independent of AC):
`TestSignatureScannerSplitAcrossChunkBoundaries` (7-byte `tinyReader`, EICAR straddling reads → infected) · `TestSignatureScannerCleanLargeFullyConsumed` (33 MiB clean → `Clean:true` **and** counting reader at 100%) · `TestSignatureScannerEmptyStream` (clean) · `TestSignatureScannerCanceledContext` (pre-canceled ctx → error) · `TestSignatureScannerCustomSignatureInTail` (custom sig at >32 MiB offset → infected, multi-sig support).

**Performance evidence (QA F5 closure)** — `internal/antivirus/perf_test.go`: the three load-bearing performance claims now carry executable proof, not just comments. All timing assertions are ratio-based best-of-3 (machine speed cancels), so they hold on any hardware:

| Claim | Test / Benchmark | Proof (on a >32 MiB clean object) |
|---|---|---|
| Peak memory ~maxSigLen+64 KiB (~128 KiB) | `TestSignatureScannerPeakHeapBounded` | GC-disabled `HeapAlloc` delta over a 33 MiB clean scan < maxSigLen+192 KiB (EICAR-only and 4 KiB-signature cases); pre-fix `io.ReadAll` held a 32 MiB buffer for the same input |
| No cap growth / no per-chunk allocation | `TestSignatureScannerAllocsIndependentOfSize` | `testing.AllocsPerRun` identical at 1 MiB vs 33 MiB (exactly 2 allocs: window + chunk) — allocation count cannot scale with size |
| O(total bytes × sigs), no superlinear window trim/copy | `TestSignatureScannerTimeLinearInSize` | 64 MiB scan ≤ 12× 8 MiB; 8 signatures ≤ 12× 1 signature (per chunk: `len(sigs)` × `bytes.Contains` over ≤ maxSigLen-1+64 KiB window + one ≤ maxSigLen-1-byte trim copy, both linear) |
| Wall-time parity vs old 32 MiB-capped path | `TestSignatureScannerWallTimeParityWithLegacy` | Streaming ≤ 3× legacy (`io.ReadAll(32 MiB)` + contains + unconditional worker drain) on a 64 MiB clean object — total I/O identical, matcher adds ≤ 2× memchr |
| Durable ns/op · B/op · allocs/op record | `BenchmarkSignatureScannerScan` / `…ScanMultiSig` / `…ScanLegacy32MiB` | `go test -bench . -benchmem ./internal/antivirus/`: B/op ≈ 192 KiB and allocs/op = 2 at any size (vs ~32 MiB B/op on the legacy path), MB/s comparable |

---

## 9. Hard-gate compliance

- `gofmt -l` clean (code above is gofmt-shaped) · `go build ./...` · `go vet ./...` · `go test ./...` (SQLite/local FS, zero network — new tests use `httptest` and local readers only).
- Non-test file sizes: `antivirus.go` 110 → ~139 lines; `worker.go` 201 → ~205 lines (**≤ 500 gate, ample margin**; `antivirus_test.go` 687 lines is excluded by the gate's `-not -name '*_test.go'` filter, Makefile:162).
- Function sizes: matcher ≈ 35 lines, drain gate 4 lines, each test ≤ 40 lines (**≤ 50-line convention**).
- I6: no new imports, no assertion framework, no `go.mod` changes.
- I5: no opt-in flag touched; default CI baseline (SQLite + local FS) exercises the full change via the AC tests.

**Final-review hardening (folded in, zero wire/config/DB/deps surface):** HTTPScanner response decode is bounded via `io.LimitReader(resp.Body, 1<<20)` (§4.A function, one line — closes the subsystem's only memory-DoS lever; a >1 MiB response now decodes as malformed → error → fail-closed, no tag; legit verdicts are a few hundred bytes); startup WARN in `buildScanner` when `AV_API_KEY` is set with a non-`https://` `AV_ENDPOINT` (log-only, zero behavior change; deliberately crosses the `internal/antivirus` boundary — the proper logger lives in `cmd/server`).

**Deferred follow-ups (ship-as-is, tracked):** F3 — `ConsumesFullStream` capability vs the concrete-type gate: the gate is semantically precise today (the predicate is really “is the stream an in-flight HTTP POST body”, not “did the scanner consume its stream” — a capability's default direction does not map onto it) and the drain is production-inert for closable readers, so a future wrapper losing it has no observable effect; re-evaluate before a third scanner type lands. F5b — `CheckRedirect: http.ErrUseLastResponse`: behavior change with silent-breakage risk for redirecting deployments; needs a config gate/release note, not a silent fold-in. F1 — legacy false-`clean` corpus rescan sweep (direction #3). F6 — per-job scan deadline + enqueue-failure telemetry.

**F2 closed (implement-time fold-in, §4.B):** the remote-side truncation residual is now operator-visible — the HTTPScanner path counts every byte pulled from storage (`scanCounter`, atomic — the transport may read the body from a background goroutine after `Do` returns) and warns `remote scanner responded before receiving the full object` when fewer bytes than `obj.Size` were consumed. Verdicts unchanged; pinned by `TestScanObjectByIDHTTPScannerWarnsOnEarlyResponse` (32 MiB object, early-responding server, closable store) and `TestScanObjectByIDHTTPScannerNoWarnWhenFullBodyConsumed` (server reads the body → no warn, clean tag).

VERDICT: PASS - all evidence verified (2 confirmed line drifts), both sibling gate verdicts dispositioned with evidence (never-merged FAIL direction; implemented PASS direction's tests preserved), streaming-matcher algorithm prototype-validated incl. chunk-boundary and >32MiB-tail cases, drain semantics empirically pinned, zero API/config/migration surface, and every acceptance check maps to a red-today-then-green test.
