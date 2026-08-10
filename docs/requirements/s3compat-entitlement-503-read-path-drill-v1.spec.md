# Requirements Specification — `internal/api/s3compat`: pin the `ErrEntitlementUnavailable` 503 class at the adapter seam + prove governance-degraded never breaks S3 reads (D1 drill)

**Module:** `internal/api/s3compat`
**Direction:** "D1 drill at the S3 adapter seam: pin the read-path 503 class (`ErrEntitlementUnavailable`) and prove governance-degraded never breaks S3 reads"
**Source analysis:** `docs/auto/analyses/internal-api-s3compat-eeefa063.json` (direction 1)
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = this checkout)
**Score:** value 8 / risk reduction 8 / effort 4 / confidence 7

---

## 1. Scope

`internal/api/s3compat/errors.go:120` maps `service.ErrEntitlementUnavailable` → S3 code `ServiceUnavailable` → HTTP **503** (`errors.go:66`, message `errors.go:90`). `grep -rn "ErrEntitlementUnavailable\|ServiceUnavailable" internal/api/s3compat/*_test.go` → **zero hits**: the only 5xx class dedicated to a service dependency in the adapter's code map is entirely unpinned at the seam. Two drill properties are therefore unproven and untested:

1. **Read-path immunity.** The direction hypothesizes that "GET/HEAD/list can hard-503 on a dependency hiccup", contradicting B3-2's degrade-not-503 posture. Evidence verification below **corrects this**: the sentinel is reachable in the S3 adapter **only through write/bucket-mutation preflights** (`preflightQuota` → `usageAccountant.CheckQuota`, `internal/service/file_crud.go:25-40`); **no read path (GET/HEAD/stat/list/tagging-read/ACL-read) calls it** — reads are projection-independent by construction. So the D1 drill's honest outcome is: reads stay **200/404** (no degrade branch needed — there is no read-path dependency to degrade), and the write-path **503 is pinned with documented rationale** (hard commercial boundary, fail-closed by design, REST parity). Both properties get regression-guard tests, so a future wiring that puts `CheckQuota` on a read path (or silently changes the write classification) fails loudly.
2. **Governance-degraded never breaks reads.** The degraded-`Ready` precedent (backlog > `maxLag` → `nil` + `Degraded()==true`, `internal/auditgovernance/runtime.go:219-263`) is covered only at the subsystem level (`runtime_test.go:415`); no adapter-level test proves S3 object reads stay 200 at the HTTP seam while the governance relay is backlogged past `maxLag`.

This spec is a **drill/pin**: production behavior is unchanged; the deliverables are (a) a documented rationale at the classification site, (b) a new adapter-level drill test file, (c) an extension of the existing governance e2e harness, (d) a classification-consistency gate. Out of scope (§4): `ErrObjectCorrupt`→410 pinning, the REST adapter (`internal/api/rest/handler_helpers.go:33-34` already pins 503 `EntitlementUnavailable`), `/readyz` semantics (landed at `15763e2`), billing runtime behavior, any production behavior change, config/docs/alerts surface.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `errors.go:66,90,120` — "`{service.ErrEntitlementUnavailable, 'ServiceUnavailable'}` → 503" | `errToS3Code` entry `{service.ErrEntitlementUnavailable, "ServiceUnavailable"}` at `errors.go:120`; `s3CodeStatus["ServiceUnavailable"] = http.StatusServiceUnavailable` at `:66`; message "The tenant entitlement projection is unavailable." at `:90`; single classifier `classify` `:142-150` routes every `writeS3Error` call through this map | ✅ **exact.** |
| E2 | "grep shows zero references in s3compat `*_test.go`" | `grep -rn "ErrEntitlementUnavailable\|ServiceUnavailable" internal/api/s3compat/*_test.go` → empty (exit 1); `grep -rn "billing" internal/api/s3compat/*_test.go` → empty | ✅ **holds.** No test attaches an accountant to the S3 service; the class is latent (see E5). |
| E3 | "sentinel raised on read-path dependency failure (`billing/runtime.go:141-159`)" | Sentinel raised at `internal/billing/runtime.go:141` (`Ready` — readiness probe, not a per-request read), `:152/:156/:159` (`CheckQuota` — unbound / lookup failed / not initialized), `:178` (`Apply`). `CheckQuota` is invoked **only** from `preflightQuota` (`file_crud.go:31`), whose 11 call sites are all writes/bucket mutations: `file_crud.go:113,134` (Put), `file_multipart.go:49,125`, `file_multipart_complete.go:252`, `file_multipart_copy.go:62`, `file_restore.go:37`, `file_delete.go:162,185` (Delete/DeleteVersion), `file_bucket_settings.go:55,76` (DeleteBucket/CreateBucket), `object_worker.go:70` (AV quarantine, not s3compat-reachable). GET/HEAD/stat/list/tagging-read/ACL-read call none of these | ⚠️ **"read-path" framing imprecise** — the sentinel is write-path-only today; the read-path 503 the direction fears is **not reachable** in this assembly. The drill therefore pins *immunity* (200/404) rather than fixing a live bug. `Ready` `:141` is `/readyz`-only (`cmd/server/main.go:157` via `runtimeReadiness`, `audit_governance.go:51-64`). |
| E4 | "`writeS3Error` has no degrade branch for it" | `classify` (`errors.go:142-150`) is a pure map lookup — no special-casing of the sentinel; all 143 `writeS3Error` call sites across the package (object + bucket handlers, grep count) share the identical code map | ✅ **holds.** |
| E5 | "the class is the only read-path 5xx" / reachability in production | `cmd/server/main.go:66` builds `billingRuntime`; `:74-77` starts it and wraps the repo; `:103-104` `svc.WithUsageAccountant(billingRuntime)`; gate `BILLING_ENABLED` default **false** (`internal/config/config_billing.go:46`) — I5 opt-in. `billing.Runtime` satisfies `service.UsageAccountant` (`runtime.go:147,174`); interface at `internal/service/usage_accounting.go:29-33`; sentinel definition `internal/service/file.go:32`; attach point `file.go:125-127` | ✅ **class is latent today, live under `BILLING_ENABLED=true`.** |
| E6 | "degrade precedent in-package: `ErrObjectCorrupt` → 410 Gone, errors.go:104,118" | `"ObjectCorrupt": http.StatusGone` at `errors.go:64`; mapping `{service.ErrObjectCorrupt, "ObjectCorrupt"}` at `:122`; sentinel `service/file.go:39`. **Line drift:** cited 104/118, actual 64/122 (a later edit reordered the map). Like the entitlement class, `ErrObjectCorrupt` has **zero** s3compat test references (grep empty) — the 410 is equally unpinned, but pinning it is out of scope (§4) | ⚠️ **precedent exists; lines drifted.** |
| E7 | "`Ready()` now degrades-not-fails on backlog > maxLag (`auditgovernance/runtime.go:170-181`), covered only at subsystem level by `runtime_test.go:415`" | `probeAndRecord` `internal/auditgovernance/runtime.go:219-263` (B3-2 degraded branch `:247-251`: `ok && age > r.maxLag` → `recordDegraded(true, age); return nil`); `Ready` `:261-263`; `Degraded` `:181-189`; `BacklogAge` `:190-198`; subsystem pin `TestRuntimeReadyDegradesOnBacklogLag` `internal/auditgovernance/runtime_test.go:415` (comment `:412`). No s3compat test exercises any of it | ✅ **exact** (lines drifted slightly: 170-181 → 181-263; symbol names identical). |
| E8 | "`/readyz` extra seam verified wired via `cmd/server/audit_governance.go:51-65`" | `runtimeReadiness` `cmd/server/audit_governance.go:51-64` (billing + audit runtimes into `readinessGroup`); wired at `cmd/server/main.go:157` | ✅ **exact.** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "errors.go:120 maps `ErrEntitlementUnavailable` → 503; zero s3compat test references" | ✅ **holds** (E1, E2). |
| "`writeS3Error` has no degrade branch for the sentinel" | ✅ **holds** (E4). |
| "GET/HEAD/list can hard-503 on a dependency hiccup" | ❌ **not reachable today** — reads never consult the projection (E3); the 503 class is write-path-only and latent unless `BILLING_ENABLED=true` (E5). The drill pins immunity instead of fixing a live bug. |
| "the degrade precedent exists in-package (`ErrObjectCorrupt` → 410)" | ✅ **holds with line drift** (E6). |
| "`Ready()` degrades-not-fails on backlog > maxLag; covered only at subsystem level" | ✅ **holds** (E7, E8). |

**Harness evidence (used by REQ-2/REQ-3/REQ-5):**

| # | Claim | Verified location | Verdict |
|---|---|---|---|
| H1 | Plain S3 test harness builds `FileService` without an accountant | `handler_test.go:19-36` (`newTestServer`: sqlite + `storage.NewLocal` + `allowAllProvider{}` + `NewRouter`); HTTP helper `do(t, method, url, body, hdr)` `handler_test.go:39` | ✅ |
| H2 | Governance e2e harness: production-shaped wiring, runtime **not** started, `MaxLagSeconds: 4`, loopback `:9` relay (unreachable → facts stay pending) | `audit_governance_e2e_test.go:83-140` (`newGovernanceE2EServerWithAuthz`: cfg `MaxLagSeconds: 4` at `:105`; `auditgovernance.New` `:112`; `WrapRepository` `:117`; `NewRouter` `:124`); "No `Runtime.Start()`" doc `:16` (file header); outbox row reads via second SQLite connection (`governanceOutboxRow` `:165`) — WAL allows a second writer | ✅ |
| H3 | Deterministic backdating idiom (no sleeps) has precedent | B3-2 spec `cmd-server-audit-governance-ready-degraded-v1.spec.md` AC-2: `UPDATE audit_governance_outbox SET created_at_ns` on a second WAL connection; the same second-connection idiom is already in-file (H2) | ✅ |
| H4 | No direct 5xx `WriteHeader` anywhere outside `writeS3Error` | `grep -rn "WriteHeader(http.Status" internal/api/s3compat/*.go` (non-test) → only 200/204/206/202/404 (`s3_bucket_handlers.go:282` headBucket 404 is a bare `WriteHeader`, not an error body — the only direct non-2xx) | ✅ → REQ-4.3 |
| H5 | REST parity: same sentinel → 503 in the REST adapter | `internal/api/rest/handler_helpers.go:33-34`: `ErrEntitlementUnavailable` → `"EntitlementUnavailable", ..., http.StatusServiceUnavailable` | ✅ → REQ-1 rationale |

---

## 3. Requirements

### REQ-1 — Pinned classification decision, documented at the seam

`internal/api/s3compat/errors.go`, entry `{service.ErrEntitlementUnavailable, "ServiceUnavailable"}` (`:120`): add a doc comment stating the pinned contract (no behavior change):

- **Read paths are projection-independent.** `ErrEntitlementUnavailable` can only surface through write/bucket-mutation preflights (`preflightQuota` → `CheckQuota`, `file_crud.go:25-40`); GET/HEAD/stat/list must never emit this class — the drill tests (REQ-3) are the regression guard.
- **Write-path 503 is deliberate, not a gap.** The entitlement projection is a hard commercial boundary: fail-closed by design (`file_crud.go:21-24` — a repository outage must not silently bypass the cap), cross-adapter consistent (REST pins the same sentinel at 503 `EntitlementUnavailable`, `handler_helpers.go:33-34`), and S3-native (503 `ServiceUnavailable` is the standard retryable server-side error — AWS SDK clients retry it, correct for a projection that may recover).
- **No degrade branch is added.** B3-2's degrade-not-503 posture applies to *readiness* (audit-governance backlog, `auditgovernance/runtime.go:247-251`); the write-path commercial boundary keeps the loud, retryable 503.

### REQ-2 — Write-path drill: entitlement-unavailable → pinned 503 (AC-1 write side)

New file `internal/api/s3compat/entitlement_drill_test.go` (package `s3compat`; mirrors `handler_test.go:19-36`, + `service.WithUsageAccountant`):

- `toggleableAccountant` — implements `service.UsageAccountant` (`usage_accounting.go:29-33`) with an `atomic.Bool fail`; when set: `CheckQuota` returns `fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)` (the production error shape, `billing/runtime.go:152`); when clear: `CheckQuota` → nil, `Apply` → `(repository.TenantQuota{}, nil)`.
- Helper `newEntitlementDrillServer(t, acct)` — `newTestServer` clone that attaches the accountant.
- `TestS3EntitlementUnavailableWritePath503`:
  - *Phase A (seed, healthy):* PUT `/b/k.txt` → **200** (the write preflight passes with `fail=false`).
  - *Phase B (projection unavailable):* `fail=true`; then each of the four write/bucket-mutation paths must return **503** with XML containing exactly `<Code>ServiceUnavailable</Code>` and `<Message>The tenant entitlement projection is unavailable.</Message>`:
    1. PUT `/b/k2.txt` (object path — `Put` → `preflightQuota`, `file_crud.go:113/134`),
    2. DELETE `/b/k.txt` (delete path — `file_delete.go:162`),
    3. PUT `/b2` (bucket path — `CreateBucket` → `file_bucket_settings.go:76`),
    4. POST `/b/k.txt?restore` (restore path — `file_restore.go:37`).
  - Assert the identical code/message pair across all four — **object and bucket paths share the same code map** (AC-3 seam).
  - *Phase C (recovery):* `fail=false`; GET `/b/k.txt` → **200** — projection recovery restores service; nothing about the 503 was sticky.

### REQ-3 — Read-path drill: reads stay 200/404 while the projection is unavailable (AC-1 read side)

Same file:

- `TestS3EntitlementUnavailableReadsStay200` — seed `/b/k.txt` while healthy, then `fail=true`, and assert the **complete read classification**:
  - GET `/b/k.txt` → **200**, body byte-identical to the seed payload;
  - HEAD `/b/k.txt` → **200**;
  - GET `/b/missing.txt` → **404** with `<Code>NoSuchKey</Code>` (404 preserved — not converted to 503);
  - GET `/b?list-type=2` (ListObjectsV2) → **200** with `<Key>k.txt</Key>` present;
  - HEAD `/b` (HeadBucket) → **200**.
- Contract note in the test doc-comment: reads never call `CheckQuota` (verified call-site audit, E3); this test is the **regression guard** — if a future change adds an entitlement dependency to a read path, this test 503s and fails.

### REQ-4 — Classification-consistency gate (AC-3)

Same file:

- **REQ-4.1 — `TestClassifyEntitlementUnavailable`:** `classify(service.ErrEntitlementUnavailable)` → `("ServiceUnavailable", "The tenant entitlement projection is unavailable.", 503)`; the wrapped production shape `fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)` classifies identically (via `errors.Is`, `errors.go:125-133`); `s3ErrorResponse("ServiceUnavailable")` → `(503, msg)`.
- **REQ-4.2 — `TestS3CodeMapsMutuallyComplete`:** every code referenced in `errToS3Code` (`errors.go:94-123`) has entries in both `s3CodeStatus` and `s3CodeMessage`, and the two maps have identical key sets (drift guard in both directions — a code added to one map but not the other fails).
- **REQ-4.3 — `TestNoDirect5xxOutsideWriteS3Error` (source grep):** read the package's non-test `.go` files (exclude `errors.go`), regex `WriteHeader\(http\.Status(ServiceUnavailable|InternalServerError|BadGateway|GatewayTimeout|NotImplemented|500|501|502|503|504|505)\)` → **zero matches**. Pins "`writeS3Error` is the single 5xx emitter for object AND bucket paths" (H4; the bare 404 in `headBucket`, `s3_bucket_handlers.go:282`, is 4xx and exempt). `os.ReadFile` + `regexp` over `filepath.Glob("*.go")` — no new deps (I6).

### REQ-5 — Governance-degraded parity at the HTTP seam (AC-2)

Extend `internal/api/s3compat/audit_governance_e2e_test.go`:

- **Harness:** `newGovernanceE2EServerWithAuthz` (`:83-140`) gains a 4th return value `*auditgovernance.Runtime`; the delegating `newGovernanceE2EServer` (`:66-68`) and its 5 call sites (`e2e_test.go:192,293`; `delete_e2e_test.go:37,186,253`) are updated mechanically (existing assertions unchanged; the runtime was already constructed and `t.Cleanup`'d inside the harness — callers only gain a handle to it).
- `TestS3CompatReadsUnaffectedByGovernanceBacklogDegraded`:
  1. `srv, _, dsn, runtime := newGovernanceE2EServer(t, "active")`.
  2. PUT `/b/k.txt` → **200** — enqueues exactly one pending governance fact through the production-shaped wrapped repo (loopback `:9` relay never drains; runtime **not** started — same posture as the existing e2e tests, H2).
  3. **Deterministic backdate** (no sleeps): second SQLite connection on `dsn` (WAL — the `governanceOutboxRow` `:165` idiom, H2/H3): `UPDATE audit_governance_outbox SET created_at_ns = ? WHERE origin_kind='file'` with `now−8s` (harness `MaxLagSeconds: 4` → 2× margin, matching the B3-2 spec's AC-2 margin).
  4. **Parity assertion at the seam** (mirror of `runtime_test.go:415` at the HTTP seam): `runtime.Ready(ctx)` → **nil**; `runtime.Degraded()` → **true**; `runtime.BacklogAge() > 4*time.Second`.
  5. **Adapter assertion:** while that degraded state holds — GET `/b/k.txt` → **200** with the exact body; HEAD → **200**; GET `/b/missing.txt` → **404** `<Code>NoSuchKey</Code>`; GET `/b?list-type=2` → **200** with the key. Governance-degraded never breaks S3 reads (I5).
  6. Second `runtime.Ready(ctx)` → nil again (degraded state is stable, never 503).

---

## 4. Decisions & non-goals

- **D1 — Pin the status quo; do not add a degrade branch.** The evidence resolves the direction's open question: the read path has **no** entitlement dependency to degrade (E3) — reads are immune by construction, and REQ-3 locks that. The write path keeps 503 with documented rationale (REQ-1): hard commercial boundary, fail-closed design comment (`file_crud.go:21-24`), REST parity (`handler_helpers.go:33-34`), S3 retry semantics. B3-2's degrade-not-503 belongs to readiness, not to the commercial write gate.
- **D2 — Drill-first, wiring-safe.** The class is latent today (`BILLING_ENABLED` default off; no test attaches an accountant, E2/E5). The drill de-latents the contract *before* the billing boundary is production-wired: when `WithUsageAccountant` lands (or is re-verified), the adapter's failure contract and read immunity are already pinned.
- **D3 — Test-only + one comment.** No production behavior change, no new `go.mod` deps (I6), no config/docs/alerts surface. The "documented rationale" acceptance branch is satisfied by the REQ-1 comment + this spec.
- **D4 — Harness signature change is mechanical.** The 4th return value touches 5 call sites but changes no assertions; `go test ./...` compiles the whole package (I6, hard gate).
- **Non-goals:** `ErrObjectCorrupt`→410 pinning (same shape of gap, different class — E6 notes it for a future drill); REST adapter classification (already pinned, `handler_helpers.go:33-34`); `/readyz` payload semantics (landed at `15763e2`); billing runtime behavior, quota/entitlement policy, `BILLING_ENABLED` gating; adding any entitlement check to read paths (the opposite of the drill's intent); alerts/docs/config changes; any change to the `errToS3Code`/`s3CodeStatus`/`s3CodeMessage` values themselves (REQ-4 pins consistency, not content).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 (D1 drill) —** *"drill test asserting S3 GET/HEAD/list status when the entitlement projection is unavailable (200/404 after adopting a degrade branch, or pinned 503 with documented rationale — untested either way today)."*
*Testable:* REQ-2 + REQ-3 with `toggleableAccountant` (fails exactly like `billing/runtime.go:152`). The evidence-backed verdict is: **reads → 200/404, writes → pinned 503 with documented rationale** (REQ-1 comment). `TestS3EntitlementUnavailableReadsStay200` asserts GET/HEAD/list/HeadBucket = 200 and missing-key = 404 NoSuchKey while `fail=true` — this is the "degrade" outcome, achieved by construction (no read-path dependency exists, E3) and locked as a regression guard. `TestS3EntitlementUnavailableWritePath503` pins the 503 shape on PUT/DELETE/CreateBucket/restore with recovery back to 200.

**AC-2 (governance-degraded never breaks S3 reads) —** *"adapter-level assertion that a backlog > maxLag outbox (rows enqueued past maxLag, Runtime.Ready called or relay polled) leaves object GET 200 (runtime_test.go:415 parity at the HTTP seam)."*
*Testable:* REQ-5 — one pending governance fact (enqueued by a real PUT through the production-shaped wiring), backdated 8 s past `maxLag` 4 s deterministically via a second WAL connection (no sleeps, 2× margin); `runtime.Ready(ctx)==nil`, `Degraded()==true`, `BacklogAge()>4s` (the `runtime_test.go:415` assertions, restated at the seam); then GET = 200 + exact body, HEAD = 200, missing = 404, list = 200 — the adapter-level proof that I5 holds for governance degradation.

**AC-3 (classification-consistency grep) —** *"classification-consistency grep over all writeS3Error call sites (object + bucket paths share the same code map)."*
*Testable:* REQ-4 — `TestClassifyEntitlementUnavailable` (unit pin incl. the `%w`-wrapped production shape), `TestS3CodeMapsMutuallyComplete` (errToS3Code ⊆ status ∩ message; status/message key sets identical), `TestNoDirect5xxOutsideWriteS3Error` (source regex: no 5xx `WriteHeader` outside `errors.go`), and the REQ-2 phase-B assertion that the four object/bucket/delete/restore paths emit the byte-identical `<Code>/<Message>` pair through the shared map.

---

## 6. Risks

- **Timing flake** — none: the governance drill backdates `created_at_ns` via SQL (H3 idiom, 2× margin over `maxLag` 4 s), never sleeps; the entitlement drill is fully deterministic (no clocks).
- **Harness signature churn** — the 4th return value of `newGovernanceE2EServerWithAuthz` touches 5 call sites; all mechanical, compile-checked by `go test ./...` (hard gate). Alternative (constructing a second runtime on the same store in the new test) was rejected: `Ready()` probes on a second runtime are fine, but the production-shaped single instance is the point of the parity assertion.
- **Map-consistency test drift** — `TestS3CodeMapsMutuallyComplete` is a white-box iteration over package maps; adding/removing a code without touching all three maps fails the test (both directions). Low flake risk; it is a pure consistency gate.
- **False confidence from "immune by construction"** — the immunity is a property of the current call-site audit (E3), not of the type system; REQ-3 is the guard that makes future drift fail loudly. Documented in the test's doc-comment and REQ-1.
- **Hard gates** — `entitlement_drill_test.go` ≈ 240 lines < 500; `audit_governance_e2e_test.go` 321 + ≈ 75 = 396 < 500; `errors.go` 150 + ≈ 15 comment lines = 165 < 500; no new deps (I6); single-function/cyclomatic limits trivially met (test helpers). `make check` must stay green (SQLite + local FS, `httptest` only, zero network).
- **Scope containment** — the adjacent unpinned class (`ErrObjectCorrupt` → 410, E6) is explicitly deferred (§4) to keep this drill single-class; a future drill can reuse the REQ-4 gate shape.

*Verification basis: all citations re-checked on this checkout (`15763e2`); line numbers reflect the working tree as read during this spec's production.*
