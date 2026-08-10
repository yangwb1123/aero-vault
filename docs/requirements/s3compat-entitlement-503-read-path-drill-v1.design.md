# Design — s3compat: pin `ErrEntitlementUnavailable` → 503 at the adapter seam + prove governance-degraded never breaks S3 reads (D1 drill)

**Module:** `internal/api/s3compat` (+ `internal/service`, `internal/billing`, `internal/auditgovernance` read-only) · **Spec:** `docs/requirements/s3compat-entitlement-503-read-path-drill-v1.spec.md` (REQ-1..5, D1..D4, AC-1..3)
**HEAD:** `15763e2` (clean tree: `go test ./internal/api/s3compat/` green at verification time) · **Date:** 2026-08-08
**Scope lock:** no production behavior change. Deliverables = (a) one doc comment at the classification site, (b) a new adapter-level drill test file, (c) a 4th return value on a test harness + one new e2e test, (d) a classification-consistency gate. Billing runtime, `/readyz` semantics, REST adapter, `ErrObjectCorrupt`→410 pinning are non-goals (spec §4).

---

## 1. Verification register (evidence re-checked, not trusted)

Every claim in the requirements evidence was re-read on this checkout (`15763e2`). The spec's verdicts **hold**; two numeric drifts are corrected below. Rows marked ⚠️ carry a correction that does not change the substance.

| # | Evidence claim | Re-verified location (this checkout) | Verdict |
|---|---|---|---|
| E1 | `errors.go:120` maps `ErrEntitlementUnavailable` → `ServiceUnavailable`; `:66` status 503; `:90` message | `internal/api/s3compat/errors.go`: `"ServiceUnavailable": http.StatusServiceUnavailable` at `:66`; message `"The tenant entitlement projection is unavailable."` at `:90`; entry `{service.ErrEntitlementUnavailable, "ServiceUnavailable"}` at `:120`; `classify` `:142-150` pure map lookup (only `InvalidArgument`/`InternalError` special-cased to raw `err.Error()`); `s3ErrorCode` `errors.Is` loop `:125-133` | ✅ **exact** |
| E2 | Zero `ErrEntitlementUnavailable`/`ServiceUnavailable` refs in s3compat `*_test.go`; zero `billing` refs | `grep -rn "ErrEntitlementUnavailable\|ServiceUnavailable" internal/api/s3compat/*_test.go` → exit 1; `grep -rn "billing" …` → exit 1 | ✅ **exact** |
| E3 | Sentinel raised at `billing/runtime.go:141` (Ready), `:152/:156/:159` (CheckQuota), `:178` (Apply); `CheckQuota` invoked only from `preflightQuota` (`file_crud.go:31`), all call sites writes | `internal/billing/runtime.go`: `Ready` func header `:141` (sentinel lines `:145-147`); `CheckQuota` unbound/lookup-failed/not-initialized wraps `:152/:156/:159`; `Apply` unbound `:178`; `var _ service.UsageAccountant = (*Runtime)(nil)` `:199`. `preflightQuota` at `internal/service/file_crud.go:25`, `CheckQuota` call at `:31`. **Call-site audit: 12 sites** — `file_crud.go:113,134` (Put), `file_multipart.go:49,125`, `file_multipart_complete.go:252`, `file_multipart_copy.go:62`, `file_restore.go:37`, `file_delete.go:162,185` (Delete/DeleteVersion), `file_bucket_settings.go:55,76` (DeleteBucket/CreateBucket), `object_worker.go:70` (AV quarantine, not s3compat-reachable). Zero read-path call sites | ⚠️ **11 → 12 call sites** (the spec's own enumeration lists 12; count drift only). Substance holds: reads are projection-independent by construction; the read-path 503 is **not reachable** |
| E4 | `writeS3Error` has no degrade branch; all call sites share one code map | `classify` (`errors.go:142-150`) is a pure map lookup — no sentinel special-casing. `grep -c "writeS3Error("` over non-test `.go` → **144** occurrences (spec said 143; 1-line counting drift) | ⚠️ **143 → 144 occurrences**; substance holds |
| E5 | Class is latent: `BILLING_ENABLED` default off; wiring `main.go:103-104`; interface `usage_accounting.go:29-33`; sentinel `service/file.go:32`; attach `file.go:125-127` | `internal/config/config_billing.go:46` `getEnvBool("BILLING_ENABLED", false)`; `cmd/server/main.go:103-104` `svc.WithUsageAccountant(billingRuntime)`; `internal/service/usage_accounting.go:29-33` interface (CheckQuota + Apply); `internal/service/file.go:32` sentinel; `:125-127` `WithUsageAccountant`; fail-closed rationale comment `file_crud.go:21-24` | ✅ **exact** |
| E6 | `ErrObjectCorrupt`→410 precedent (corrected to `:64`/`:122`) | `errors.go:64` `"ObjectCorrupt": http.StatusGone`; `:122` `{service.ErrObjectCorrupt, "ObjectCorrupt"}`; sentinel `service/file.go:39`; zero s3compat test refs (grep exit 1) | ✅ **exact** (spec's correction right) |
| E7 | `probeAndRecord` `runtime.go:219-263` degrades-not-fails; `TestRuntimeReadyDegradesOnBacklogLag` at `runtime_test.go:415` | `internal/auditgovernance/runtime.go:219-263`: degraded branch `:247-251` (`ok && age > r.maxLag` → `recordDegraded(true, age); return nil`); `Ready` `:261-263` = `probeAndRecord`; `Degraded` `:181-189`; `BacklogAge` `:190-198`. `internal/auditgovernance/runtime_test.go:415` (comment `:412`) | ✅ **exact** |
| E8 | `/readyz` seam `audit_governance.go:51-65`, wired `main.go:157` | `cmd/server/audit_governance.go:51-64` `runtimeReadiness` (billing + audit → `readinessGroup`); `cmd/server/main.go:157` `runtimeReadiness(billingRuntime, auditRuntime)` into `buildRouter` | ✅ **exact** |
| H1 | Plain harness `newTestServer` `handler_test.go:19-36`; HTTP helper `do` `:39` | `handler_test.go:19-36` (sqlite + `storage.NewLocal` + `allowAllProvider{}` + `NewRouter`, no accountant); `do(t, method, url, body, hdr)` `:39` | ✅ **exact** |
| H2 | Governance e2e harness `:83-140`: runtime **not** started, `MaxLagSeconds: 4`, loopback `:9` relay; second-WAL-connection row reads | `audit_governance_e2e_test.go:83-140` `newGovernanceE2EServerWithAuthz` (`MaxLagSeconds: 4` at `:105`; `auditgovernance.New` `:112`; `WrapRepository` `:117`; `NewRouter` `:124`; `No Runtime.Start()` doc `:16`); delegator `newGovernanceE2EServer` `:66-68`; `governanceOutboxRow` / `governanceOutboxRowForAction` second `database/sql` conn idiom | ✅ **exact** |
| H3 | Deterministic SQL-backdating precedent | B3-2 spec `cmd-server-audit-governance-ready-degraded-v1.spec.md` AC-2: `UPDATE audit_governance_outbox SET created_at_ns = <now-8s>` on a second WAL connection (2× margin over maxLag 4 s); column exists in `migrations/sqlite/0039_audit_governance_outbox.up.sql:21` | ✅ **exact** |
| H4 | No direct 5xx `WriteHeader` outside `writeS3Error`; only bare non-2xx is `headBucket` 404 | `grep -rn "WriteHeader(http.Status" internal/api/s3compat/*.go` (non-test) → only 200/204/206/202/404; bare `w.WriteHeader(http.StatusNotFound)` at `s3_bucket_handlers.go:282` (`headBucket` `:275`) | ✅ **exact** |
| H5 | REST parity: same sentinel → 503 | `internal/api/rest/handler_helpers.go:33-34`: `case errors.Is(err, service.ErrEntitlementUnavailable): return "EntitlementUnavailable", …, http.StatusServiceUnavailable` | ✅ **exact** |
| H6 | 5 call sites of the governance harness | `audit_governance_e2e_test.go:192,293` (via delegator), `audit_governance_delete_e2e_test.go:37,186,253` (37/186 via delegator, 253 via `WithAuthz`) | ✅ **exact** |
| H7 | File-size headroom for the 500-line gate | `wc -l`: `errors.go` 150, `audit_governance_e2e_test.go` 321, `handler_test.go` 938 (untouched) | ✅ **exact** — REQ-1 (~+15 comment lines → ~165) and REQ-5 (~+75 → ~396) stay under 500 |

**Conclusion of the drill's open question (D1):** the read-path 503 the direction feared is **not reachable** — `ErrEntitlementUnavailable` surfaces only through write/bucket-mutation preflights (E3). The drill pins immunity (reads 200/404, E3) and documents the write-path 503 as deliberate (E1/E5/H5). The spec's verdict stands on every substantive claim.

---

## 2. Design overview

Four decisions from the spec, all drill-only:

- **D1 — pin the status quo, add no degrade branch.** Reads are immune by construction (E3); REQ-3 locks that immunity as a regression guard. The write-path 503 is a hard commercial boundary: fail-closed (`file_crud.go:21-24` — a repository outage must not silently bypass the cap), cross-adapter consistent (H5 REST parity), and S3-native (503 `ServiceUnavailable` is the standard retryable server-side code; AWS SDK clients retry it). B3-2's degrade-not-503 posture belongs to *readiness*, not the commercial write gate.
- **D2 — drill-first, wiring-safe.** The class is latent (`BILLING_ENABLED` default off, E5; no test attaches an accountant, E2). The drill de-latents the contract before the billing boundary is production-wired.
- **D3 — test-only + one comment.** Zero production behavior change, zero new `go.mod` deps (I6), no config/docs/alerts surface.
- **D4 — harness signature change is mechanical.** 4th return value on a test-only helper; 5 call sites updated with no assertion changes; `go test ./...` is the compile gate (I6).

---

## 3. API changes

### 3.1 Production code: none (one comment)

`internal/api/s3compat/errors.go` — comment above the `errToS3Code` entry at `:120` (inside the composite literal, Go permits it):

```go
	{service.ErrQuotaExceeded, "QuotaExceeded"},
	// ErrEntitlementUnavailable → ServiceUnavailable/503 is a pinned contract,
	// not a gap. Read paths (GET/HEAD/stat/list) are projection-independent:
	// the sentinel can only surface through write/bucket-mutation preflights
	// (preflightQuota → CheckQuota, internal/service/file_crud.go:25-40).
	// The write-path 503 is deliberate: a hard commercial boundary that is
	// fail-closed by design (a repository outage must not silently bypass the
	// cap), cross-adapter consistent (REST pins the same sentinel at 503,
	// internal/api/rest/handler_helpers.go:33-34), and S3-native (503
	// ServiceUnavailable is retryable by SDK clients). No degrade branch is
	// added — B3-2's degrade-not-503 applies to readiness, not this gate.
	// Regression guards: entitlement_drill_test.go (write 503 drill, read
	// 200/404 drill).
	{service.ErrEntitlementUnavailable, "ServiceUnavailable"},
```

No signature, value, or control-flow change. `gofmt`-safe (comment placement inside a slice literal is stable).

### 3.2 Test harness API: one signature extension

`internal/api/s3compat/audit_governance_e2e_test.go`:

- `newGovernanceE2EServerWithAuthz(t, bindingState, authz)` — return type changes from `(*httptest.Server, repository.AuditGovernanceStore, string)` to `(*httptest.Server, repository.AuditGovernanceStore, string, *auditgovernance.Runtime)`; the runtime already exists in the harness body (`:112`) and is already `t.Cleanup`'d — callers only gain a handle. New `return srv, govStore, dsn, runtime`.
- `newGovernanceE2EServer(t, bindingState)` (delegator, `:66-68`) — same 4th return value, forwarded.
- **5 call sites** (H6): `e2e_test.go:192,293` and `delete_e2e_test.go:37,186` gain `, _`; `delete_e2e_test.go:253` (already `WithAuthz`) gains `, _`. Zero assertion changes.

This is a **test-only API** — no production caller, no external consumer. Test files are compiled together in one package, so the compiler enforces completeness (I6 hard gate).

### 3.3 New test file: `internal/api/s3compat/entitlement_drill_test.go` (package `s3compat`)

New symbols (all unexported):

- `toggleableAccountant` — implements `service.UsageAccountant` (`usage_accounting.go:29-33`): field `fail atomic.Bool`; `CheckQuota` returns `fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)` when set (byte-identical production error shape, `billing/runtime.go:152`), else `nil`; `Apply` returns `(repository.TenantQuota{}, nil)`. `atomic.Bool` (not a bare bool) so `go test -race` sees no unsynchronized access.
- `newEntitlementDrillServer(t, acct)` — clone of `newTestServer` (`handler_test.go:19-36`) + `svc.WithUsageAccountant(acct)`; same cleanup posture.
- Tests (see §7 for the full acceptance mapping):
  - `TestS3EntitlementUnavailableWritePath503` (REQ-2)
  - `TestS3EntitlementUnavailableReadsStay200` (REQ-3)
  - `TestClassifyEntitlementUnavailable` (REQ-4.1)
  - `TestS3CodeMapsMutuallyComplete` (REQ-4.2)
  - `TestNoDirect5xxOutsideWriteS3Error` (REQ-4.3)
- REQ-5 test `TestS3CompatReadsUnaffectedByGovernanceBacklogDegraded` lives in the **existing** `audit_governance_e2e_test.go` (it needs the harness in §3.2).

---

## 4. Compatibility constraints

| Constraint | Binding |
|---|---|
| **Zero production behavior change** | REQ-1 is a comment; REQ-2/3/4/5 are tests. `git diff` on non-test code must show only `errors.go` comment lines |
| **Classification maps are frozen in content** | REQ-4 pins *consistency* of `errToS3Code`/`s3CodeStatus`/`s3CodeMessage`, never their values. Adding/removing a code now requires touching all three maps or the gate fails (both directions) |
| **`writeS3Error` stays the single 5xx emitter** | REQ-4.3 regex forbids direct 5xx `WriteHeader` in any non-test file except `errors.go`; `headBucket`'s bare 404 (`s3_bucket_handlers.go:282`) is 4xx and exempt |
| **No new `go.mod` deps (I6)** | `os.ReadFile` + `regexp` + `filepath.Glob` + stdlib `testing`/`database/sql`/`net/http/httptest`/`sync/atomic` only |
| **Hard gates** | `gofmt -l` clean; `go build ./...`; `go vet ./...`; `go test ./...` (SQLite + local FS, zero network — loopback `:9` relay never dialed); every touched file < 500 lines (H7) |
| **No sleeps / no clocks in tests** | Entitlement drill is fully deterministic (toggle flag, no timers). Governance drill backdates via SQL on a second WAL connection (H3 idiom, 2× margin: 8 s vs `MaxLagSeconds` 4 s) |
| **I4 chain rules untouched** | Tests drive `NewRouter` via `httptest` exactly like existing harnesses; no handler self-wiring |
| **Opt-in default (I5) unchanged** | `BILLING_ENABLED` stays default-off; no config surface touched |

---

## 5. Failure modes and guards

| Failure mode | Trigger | Guard |
|---|---|---|
| Future wiring puts an entitlement dependency on a read path (e.g. `CheckQuota` in `Get`) | A read now 503s; the "immune by construction" property (E3) silently dies | `TestS3EntitlementUnavailableReadsStay200` 503s → fails. Immunity is call-site-audited today (E3), not type-enforced — this test is the loud future drift detector (documented in its doc-comment and the REQ-1 comment) |
| Write-path classification silently changes (code/message/status drift) | SDK clients see a different retry contract; REST/S3 diverge | `TestClassifyEntitlementUnavailable` pins the exact `("ServiceUnavailable", "The tenant entitlement projection is unavailable.", 503)` triple incl. the `%w`-wrapped production shape (via `errors.Is`); REQ-2 phase B asserts the byte-identical `<Code>/<Message>` pair across all four write paths |
| Map drift: a code added to one of the three maps but not the others | A classification silently degrades to `InternalError` (status `:136-139` fallback) | `TestS3CodeMapsMutuallyComplete` fails (key-set identity + `errToS3Code` code ∈ both maps, both directions) |
| A handler starts emitting 5xx directly | `writeS3Error` no longer the single 5xx emitter; error bodies diverge | `TestNoDirect5xxOutsideWriteS3Error` fails (source regex over non-test files) |
| Governance degraded semantics regress (backlog > maxLag → `Ready()` hard error / `Degraded()==false`) | `/readyz` 503s on backlog again; parity claim false | REQ-5's parity assertions (`Ready()==nil`, `Degraded()==true`, `BacklogAge()>4s` at the HTTP seam) fail |
| Harness signature churn misses a call site | Package fails to compile | `go test ./...` (hard gate) — 5 sites, all mechanical |
| Backdating UPDATE matches wrong rows | Test asserts on a stale/wrong outbox row | REQ-5 filters `WHERE origin_kind='file'` (harness convention) and asserts `RowsAffected == 1` before proceeding; single PUT seeds exactly one pending row |
| Test flake from timing | — | None: no sleeps anywhere; backdate margin is 2× (`now−8s` vs maxLag 4 s); entitlement drill has no clocks |
| `ErrObjectCorrupt`→410 unpinned (adjacent gap) | Not a failure of this drill | Explicitly out of scope (spec §4); the REQ-4 gate shape is reusable for a future drill |

---

## 6. Migration steps

No DB migration, no config migration, no rollout — this is a test/comment drill. Step order (each step leaves `make check` green):

1. **Baseline sanity:** confirm tree state (`gofmt -l` empty, `go build ./...`, `go vet ./...`, `go test ./...`) — verified green at design time.
2. **REQ-1 comment** (3.1): comment-only edit to `errors.go`. Zero risk; run `go test ./internal/api/s3compat/`.
3. **REQ-2/3/4 — new file** (3.3): add `entitlement_drill_test.go` (~240 lines, no edits to existing files). Run `go test ./internal/api/s3compat/ -run 'TestS3EntitlementUnavailable|TestClassify|TestS3CodeMaps|TestNoDirect5xx'`.
4. **REQ-5 — harness extension + new test** (3.2): change `newGovernanceE2EServerWithAuthz`/`newGovernanceE2EServer` signatures, update the 5 call sites with `, _`, add `TestS3CompatReadsUnaffectedByGovernanceBacklogDegraded` to `audit_governance_e2e_test.go`. The compiler forces completeness of step 3's call-site updates.
5. **Full gate:** `make check` (hard gates §4); `make test-race` for the s3compat package (new `atomic.Bool` usage); confirm `wc -l` headroom (H7).
6. **Docs:** no `configuration.md`/alerts/docs changes (spec D3). Spec + this design are the artifact trail.

No sequencing hazard: steps 2–4 touch disjoint files (`errors.go` / new file / `audit_governance_e2e_test.go`), so sibling-campaign commit interleaving cannot break any intermediate state.

---

## 7. Testable acceptance mapping

| Acceptance criterion (spec §5, intent preserved) | Named tests | Determinism / mechanism |
|---|---|---|
| **AC-1** — drill asserting S3 GET/HEAD/list status while the entitlement projection is unavailable (outcome: reads 200/404, writes pinned 503 + documented rationale) | **Write side:** `TestS3EntitlementUnavailableWritePath503` — Phase A seed PUT 200 (healthy); Phase B `fail=true`: PUT new object → 503, DELETE seeded object → 503, PUT new bucket (`CreateBucket`) → 503, POST `?restore` → 503 — all four assert XML `<Code>ServiceUnavailable</Code>` + `<Message>The tenant entitlement projection is unavailable.</Message>` byte-identical (shared code map, AC-3 seam); Phase C `fail=false`: GET → 200 (recovery, nothing sticky). **Read side:** `TestS3EntitlementUnavailableReadsStay200` — seed while healthy, `fail=true`: GET 200 + byte-identical body, HEAD 200, missing-key GET 404 `<Code>NoSuchKey</Code>` (not converted to 503), ListObjectsV2 200 with `<Key>k.txt</Key>`, HeadBucket 200. **Rationale:** REQ-1 comment (§3.1) + this design §2 D1 | Toggle flag, no clocks, no sleeps; production error shape replicated from `billing/runtime.go:152` |
| **AC-2** — adapter-level assertion that a backlog > maxLag outbox leaves object GET 200 (`runtime_test.go:415` parity at the HTTP seam) | `TestS3CompatReadsUnaffectedByGovernanceBacklogDegraded` (in `audit_governance_e2e_test.go`): (1) `srv, _, dsn, runtime := newGovernanceE2EServer(t, "active")`; (2) PUT `/b/k.txt` → 200 (enqueues exactly one pending governance fact through production-shaped wiring; relay loopback `:9` never drains, runtime not started — H2 posture); (3) second SQLite connection on `dsn`: `UPDATE audit_governance_outbox SET created_at_ns = ? WHERE origin_kind='file'` with `now−8s` (2× margin over `MaxLagSeconds: 4`), assert `RowsAffected == 1`; (4) parity: `runtime.Ready(ctx)==nil`, `runtime.Degraded()==true`, `runtime.BacklogAge() > 4*time.Second`; (5) while degraded: GET 200 + exact body, HEAD 200, missing 404 `<Code>NoSuchKey</Code>`, list 200; (6) second `runtime.Ready(ctx)==nil` (stable degraded state, never 503) | SQL backdating on second WAL connection (H3 idiom, no sleeps); mirrors `runtime_test.go:415` assertions restated at the HTTP seam |
| **AC-3** — classification-consistency gate over all `writeS3Error` call sites (object + bucket share one code map) | `TestClassifyEntitlementUnavailable` (REQ-4.1): `classify(service.ErrEntitlementUnavailable)` → `("ServiceUnavailable", "The tenant entitlement projection is unavailable.", 503)`; the `%w`-wrapped shape classifies identically (`errors.Is`, `errors.go:125-133`); `s3ErrorResponse("ServiceUnavailable")` → `(503, msg)`. `TestS3CodeMapsMutuallyComplete` (REQ-4.2): `errToS3Code` codes ⊆ `s3CodeStatus` ∩ `s3CodeMessage`; status/message key sets identical (both-direction drift guard). `TestNoDirect5xxOutsideWriteS3Error` (REQ-4.3): `os.ReadFile` + `regexp` over `filepath.Glob("*.go")` (non-test, exclude `errors.go`): `WriteHeader\(http\.Status(ServiceUnavailable\|InternalServerError\|BadGateway\|GatewayTimeout\|NotImplemented\|500\|501\|502\|503\|504\|505)\)` → zero matches (bare 404 in `headBucket` exempt, 4xx). Plus REQ-2 Phase-B's byte-identical `<Code>/<Message>` across the four object/bucket/delete/restore paths | Pure unit pins + source grep; stdlib only (I6) |

All three ACs map to named, deterministic tests; no sleeps, no new deps, no network. Acceptance is *fully* machine-checkable via `go test ./internal/api/s3compat/`.

---

## 8. Risks and residual gaps

- **Harness churn (D4):** 5 call sites, compiler-enforced; the alternative (constructing a second runtime in the test) was rejected — the production-shaped single instance is the point of the parity assertion (§3.2).
- **"Immune by construction" is audit-strength, not type-strength:** REQ-3 is the guard that makes future drift fail loudly; documented in the test doc-comment and REQ-1 comment.
- **REQ-5's `RowsAffected == 1` assertion** depends on the harness's single-PUT seeding; if a sibling campaign changes the outbox schema, the UPDATE fails loudly (compile/DB error), not silently.
- **Adjacent unpinned class (`ErrObjectCorrupt`→410, E6)** is deferred (spec §4) — same shape of gap, different class; the REQ-4 gate shape is reusable.
- **File gates:** `entitlement_drill_test.go` ~240 < 500; `audit_governance_e2e_test.go` 321+~75 ≈ 396 < 500; `errors.go` 150+~15 ≈ 165 < 500. `make check` stays green (SQLite + local FS, `httptest` only, zero network).

*Verification basis: every row above re-read on this checkout (`15763e2`); line numbers reflect the working tree as read during design production.*
