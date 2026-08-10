# Design — WebDAV-surface audit-governance e2e harness: activation gate + binding matrix (B3 items 5+6; vehicle for T-3/T-4/D1)

**Module:** `internal/api/webdav` — deliverable is **test-only**: new `internal/api/webdav/dav_governance_e2e_test.go` (package `webdav_test`, ≤500-line gate, table-driven cells). Mechanism under test lives in `internal/auditgovernance` + `internal/repository` + `internal/events` + `internal/config` — **zero production edits** (D3).
**Spec:** `docs/requirements/internal-api-webdav-audit-governance-e2e-harness-v1.spec.md` (REQ-1..5, AC-1.1..5.3, D1..D8)
**Contract:** `docs/proposals/audit-contract-batch-aero-vault.md:12-13,17` — B3-5 (scope enforcement from a surface) + B3-6 (first-event e2e through the production capture chain); G4 items 5+6; direction 1 of `docs/auto/analyses/internal-api-webdav-c346cab0.json` (acceptance preserved verbatim in §6)
**Siblings:** `internal-api-webdav-audit-governance-terminal-classification-v1.design.md` (full T-3 matrix) · `internal-api-s3compat-audit-governance-deterministic-fact-ids-v1.design.md` (T-4/gap-reconcile) · `cmd-server-audit-governance-ready-degraded-v1.*` (B3-2, landed HEAD `15763e2`) · `activation-gate-fail-closed-*` (direction 2 — B3-6 empty-bindings test pin)
**Date:** 2026-08-08 · **HEAD:** `15763e2` + working-tree deltas (mechanism layer partially uncommitted — §5 step 0)

---

## 0. Baseline caveat (verified, not trusted)

The evidence (spec summary + requirements artifact) claims specific line numbers and gaps. **Every claim was re-checked against this worktree** (`go build ./...` ✅, `go vet` ✅, `gofmt -l` clean ✅, `go test ./internal/api/webdav/ -count=1` → ok 36.892s ≈ claimed 36.8s ✅). Verdict: substantively **accurate**; five cosmetic drifts + one provenance ambiguity (below) change no requirement.

| Evidence claim | Verified (this worktree) | Verdict |
|---|---|---|
| Deliverable 1: `docs/requirements/internal-api-webdav-audit-governance-e2e-harness-v1.spec.md` (170 lines) | Exists, exactly 170 lines | ✅ exact |
| Deliverable 2: `docs/auto/runs/webdav-surface-audit-governance-e2e-harness-acti-67a9712f/artifacts/requirements-10762e10/requirements.md` — PASS-pattern stage summary | Exists (31 lines, 5452 B); content is the summary described (REQ-1..5 condensed, verification table, D3 zero-production finding) | ✅ (spec E7's "166 lines" is drift — the file at that path is the 31-line summary, not a full REQ spec) |
| GAP-1: `grep auditgovernance internal/api/webdav/` → zero hits | Exit 1, 0 hits; both harnesses read in full: `newWebdavRelayHarness` (`dav_relay_test.go:47-90`, comment `:37`) builds the bus from the **raw** repo and starts only `events.NewEventOutboxRelay`; `newTestServerWithSvcDSN` (`dav_audit_test.go:44-68`) raw repo, no bus | ✅ exact — GAP real |
| Production chain `cmd/server/main.go:79-86` | `run()` block is `:70-83` (`buildAuditGovernanceRuntime` `:70`, `Start` `:81`, `WrapRepository` `:82`, `bus.WithRepository` `:83`), `svc…WithEventSink(bus)` at `:94`; wrap duplicated in `runMCP()` `:212-217` | ✅ (drift correctly reported in evidence itself) |
| s3compat adapter-sibling matrix `:59-162` | `newGovernanceE2EServerWithAuthz` spans `:83-137` (wrap → bus → svc → router, `bindingState` param); draining cell `TestS3CompatAuditGovernanceCaptureInactive` `:292-321`; golden source/ID pins `:220-231` | ✅ exact |
| B3-6 empty-bindings fail-closed gate "already shipped" | Gate **exists in this worktree** at `config_audit_governance.go:179-182` (`if !c.Drain && len(c.Bindings) == 0`), `Drain` field `:21,:57` — but it is a **worktree-only delta**: `git show HEAD:…` has no "must not be empty", and the `MaxBackoffSeconds >= 2` floor (`:263`) is also `git diff HEAD`-added. The spec's hedge "committed-or-worktree-shipped" is accurate; **this harness has zero dependency on those lines** (always ≥1 binding; MaxBackoff 4 ≥ 2 passes at HEAD too) | ⚠️ exact-with-caveat (gate at `:179-182`, not `:172-175`; uncommitted — §5 step 0) |
| T-3 store half: `OldestPendingAuditGovernance` `:211-224`, `failed_at_ns=0` predicate; 0042/0043/0044 migrations | `internal/repository/audit_governance_claim.go:211-224` (file is in **`internal/repository/`**, spec cites it package-unqualified); dead-row predicates also at `:54,:78,:110,:169,:190`; 0042 committed at HEAD `15763e2`, **0043/0044 pairs untracked** (worktree) | ✅ exact-with-path-drift |
| T-4: `DeterministicFactID` `audit_governance_factid.go:22-47`; source shape `aero-vault.`+43 base64url = 54 chars | Formula read in full (NUL frame, `[:32]` hex); `redaction.go:43-51` `tenantSourceID` → `aero-vault.` + `base64.RawURLEncoding` of 32-byte HMAC = 43 chars = 54 total; s3compat golden pin `e2eSourceID` + "aero-vault.PE5txdo…" reproduces | ✅ exact |
| D1 mechanism: `runtime.go:213-221` Degraded, `:222-229` BacklogAge, `:251-291` probeAndRecord (age>maxLag → degraded + nil), `:293-295` Ready | `Capture` `:187-189`, `PendingBacklogAge` `:198-211`, `Degraded` `:222-229`, `BacklogAge` `:230-238`, `probeAndRecord` `:251-291`, `Ready` `:293-295`; run loop refreshes the cache once per poll cycle (`:312-318`, G3 comment); `readyzHandler` 503 iff `extra.Ready` err (`cmd/server/http.go:108-110`) | ✅ (ranges drift by the PendingBacklogAge block) |
| Degraded window ≈[4,6]s deterministic (ClaimTTL 3s, MaxBackoff/MaxLag 4s) | Claim predicate lease-gated (`available_at_ns<=now AND lease_expires_at_ns<=now`, `claim.go:54/:78`); `cumulativeWindowExceeded` window == `maxBackoff` (`relay.go:145-153`); retry sets `available_at_ns` + clears claim (`:160-172`); `boundedBackoff` ±25% jitter (`:209-229`); config passes every validator (`config_audit_governance.go:248-276,192-199,203-215,313-316`) | ✅ exact |
| Sibling mechanics: `cmd/server/governance_e2e_test.go` 489 lines, receiver `:74-143`, `writeReceipt` `:145-151`, harness `:182-227`, helpers `:248-348`; `runtime_test.go:38-48` config precedent | 489 lines confirmed; `serve` `:74-143` hard-pins token form (grant_type/scope/resource/BasicAuth) + `wait_for=ledgered` + modes incl. 409/422/202-conflict/202-wrong-tenant; `runtimeConfig` `:38-48` (HTTPTimeout 1/Poll 10/Batch 10/ClaimTTL 3/Initial 1/Max 2/MaxLag 4) | ✅ exact |
| All five supplied acceptance checks preserved verbatim in spec §6 | Compared line-by-line against `internal-api-webdav-c346cab0.json` direction-1 `acceptance` — all five verbatim | ✅ exact |
| Module helper pool: `do` `dav_test.go:70`, `auditRowsFor`/`assertAuditRowFor` | `do` at `dav_test.go:70-93`; `auditRowsFor` at `dav_audit_test.go:228`, `assertAuditRowFor` at `:245` (spec's `:180-203` is actually a LOCK test — drift) | ✅ (helper locations drift) |
| **Provenance ambiguity (new)** | `docs/auto/runs/…-acti-97a0d944/artifacts/requirements-10762e10/requirements.md` also exists (16 lines, 2742 B, fingerprint `52f10f99…`) and is what the memory index cites; the evidence cites `-67a9712f` (31-line summary) which matches its own description. Both runs' `DECISIONS.md` record stage `requirements` FAIL (agent exit 1, verified for 67a9712f) | ⚠️ run dirs should be reconciled (one is a stale retry copy) — no requirement impact |

**Consequence of the verification:** the spec is sound and implementation-ready; the only sequencing constraint is §5 step 0 (mechanism files are partially uncommitted — no implementation may start before the sibling campaign's worktree is committed).

---

## 1. Design overview

The spec prescribes **one test-only deliverable** with zero production delta:

| Req | Nature | Delta |
|---|---|---|
| REQ-1 — governance harness (AC-1.1..1.4) | test infra | new file `internal/api/webdav/dav_governance_e2e_test.go` (package `webdav_test`): main.go-order wiring + fake receiver + config builder + row/recompute helpers |
| REQ-2 — bound "default" × (PUT, DELETE) first-event e2e + T-4 + B3-5 (AC-2.1..2.5) | test | `TestWebDAVGovernance_BoundDefaultFirstEventFactID` |
| REQ-3 — draining + unbound local-only (AC-3.1..3.3) | test | `TestWebDAVGovernance_DrainingAndUnboundLocalOnly` |
| REQ-4 — T-3 terminal cells 409 / 202-conflict (AC-4.1..4.3) | test | `TestWebDAVGovernance_Terminal409`, `TestWebDAVGovernance_Terminal202Conflict` |
| REQ-5 — D1 cells 500 / hang (AC-5.1..5.3) | test | `TestWebDAVGovernance_Degraded500`, `TestWebDAVGovernance_HangTimeoutLoopContinues` |

Wiring (mirrors `cmd/server/main.go:70-83,94` **in order**, verified §0):

```
repo   = repository.Open(ctx, "sqlite", dsn) + Migrate
govStore = repo.(repository.AuditGovernanceStore)          // satisfies auditgovernance.Store (runtime.go:18-20)
cfg    = harnessGovernanceConfig(receiver.URL, …)          // passes cfg.Validate (runtime.go:82)
rt, _  = auditgovernance.New(cfg, govStore, logger)        // bindings applied at New; NOT Start()ed
wrepo  = auditgovernance.WrapRepository(repo, rt)          // repository.go:10-16
bus    = events.New(wrepo, logger).WithRepository(wrepo)   // bus.go:37, :67-77 — F2: wrapped repo mandatory
svc    = service.NewFileService(store, wrepo, logger).
          WithAuthorizer(allowAllProvider{}).WithEventSink(bus)
h      = mw.Tenant(dav.Handler("/webdav", svc, logger))    // dav.go:24; middleware.go:25,46
```

`rt.Start(ctx)` is called **by each test after its deterministic pre-start snapshot** (sibling B1 precedent `governance_e2e_test.go:239-241,362-395`). Cleanup LIFO (D7): `receiver.server.Close` (registered first) → `repo.Close` → `rt.Close` (last; bounded by claimTTL+httpTimeout, `runtime.go:145-155`); hang-mode release channel closed in `t.Cleanup` before receiver close (`dav_relay_test.go:29-33` pattern).

Cells (all via the WebDAV surface, fresh single-version objects, PUT then DELETE — DELETE hard path emits exactly one `EventDeleted`, `file_delete.go:46,53`; version-promotion emit `:211-215` must not fire):

| # | Tenant / binding | Receiver mode | Asserts (spec AC) |
|---|---|---|---|
| 1 | `default` / active | 202-echo | AC-2.1..2.5 (2 rows, origin keying, T-4 ID, first POST=PUT, tokenCalls==1) |
| 2 | `default` / draining | 202-echo | AC-3.1 (0 rows, 0 POSTs, 0 token calls, local audit row kept) |
| 3 | `other` (unbound) | 202-echo | AC-3.2 (same zeros) |
| 4 | `default` / active | 409 | AC-4.1 (terminal, absent from pending, backlog ok==false) |
| 5 | `default` / active | 202-conflict | AC-4.2 (terminal, "reports a conflict") |
| 6 | `default` / active | 500 | AC-5.1 (degraded ≈[4,6]s, Ready()==nil, terminal convergence) |
| 7 | `default` / active | hang | AC-5.2 (1s timeout abort, loop continues postCount≥2, release→delivered) |

---

## 2. API changes

### 2.1 Production API — **none** (D3)

No config keys, no endpoints, no routes, no DB schema, no public Go symbols. The only production-adjacent deltas referenced by the harness (empty-bindings gate, `MaxBackoffSeconds>=2` floor) are **already in the worktree** and belong to sibling directions; the harness does not depend on them (§0).

### 2.2 Test-harness API (new, package-internal to `webdav_test`)

All symbols live in `dav_governance_e2e_test.go`; nothing exported; helpers **copied** from `cmd/server/governance_e2e_test.go` and `internal/api/s3compat/audit_governance_e2e_test.go` shapes, never imported (I6/C-10; `governance_e2e_test.go` is `package main` and unimportable anyway).

```go
type govReceiverMode string // "202-echo" | "202-conflict" | "409" | "500" | "hang"

type governanceReceiver struct {
    mode       govReceiverMode
    mu         sync.Mutex
    posts      []govPost            // {eventID string; at time.Time; authz string}
    source     string               // first observed source_system
    postCount  atomic.Int64
    tokenCalls atomic.Int64
    release    chan struct{}        // hang mode: blocked POSTs wait here; closed in t.Cleanup
}
func (r *governanceReceiver) serve(w http.ResponseWriter, req *http.Request) // token + events routes (AC-1.2)
func (r *governanceReceiver) writeReceipt(w http.ResponseWriter, eventID, tenant string, conflict bool) // 202 JSON, sibling shape :145-151

func harnessGovernanceConfig(baseURL, tokenURL, hmacKey string, bindings []config.AuditGovernanceBinding) config.AuditGovernanceConfig
// AC-1.3 values: HTTPTimeout 1 / Poll 10ms / Batch 10 / ClaimTTL 3 (3 > 2×1) /
// Initial 1 / MaxBackoff 4 (≥2 floor, ≥initial) / MaxLag 4 (4 > 3) /
// ReconcileBatchSize 8 / DeliveredRetention 3600 / CleanupInterval 60 /
// CleanupBatch 100 / Revision 1 / HMACKey 32B distinct / ClientSecretEnv
// "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E" / loopback URLs. Self-honored (bypasses env
// loading but not cfg.Validate — runtime.go:82).

func newGovernanceWebdavHarness(t *testing.T, mode govReceiverMode, bindingState string, tenantID string) (
    srv *httptest.Server, govStore repository.AuditGovernanceStore, rt *auditgovernance.Runtime,
    recv *governanceReceiver, dsn string)
// main.go-order wiring per §1; rt constructed but unstarted; receiver routes
// registered before rt.New (cfg needs the loopback URLs); cleanup LIFO per D7.
// bindingState "active"|"draining" (F6); unbound cell = binding on "default" +
// requests with header X-Aero-Tenant: other (F8).

func governanceOutboxRowForAction(t *testing.T, dsn, key, action string) (
    found bool, id string, originID int64, occurredNS int64, actionGot, tenantID, createdRaw string, count int)
// ErrNoRows-tolerant (never t.Fatal on ErrNoRows) — s3compat discipline :165-188.
// Type-filtered by action: after PUT+DELETE both rows match the same key.

func eventRowID(t *testing.T, dsn string, action string) int64   // object_events.id for the origin
func wantFactID(t *testing.T, source, tenant, action string, originID int64, createdRaw string) string
// DeterministicFactID recompute: createdRaw parsed time.Parse(time.RFC3339Nano),
// byte-identical to the write-path flexTime scan (audit_governance_write.go:70,73);
// cross-check against outbox occurred_at_ns.
func waitFor(t *testing.T, timeout time.Duration, poll time.Duration, desc string, cond func() bool) // 10s/5-10ms, positives
func quiesce(t *testing.T, d time.Duration, cond func() bool) bool                                   // negatives only (AC-3.3, D6)
```

Existing helpers reused: `do` (`dav_test.go:70`), `auditRowsFor`/`assertAuditRowFor` (`dav_audit_test.go:228/:245`), `allowAllProvider` (`dav_test.go:896`).

### 2.3 Interface surface consumed (all existing)

- `repository.AuditGovernanceStore` (asserted from the raw repo) — store queries: `OldestPendingAuditGovernance` (AC-4.1).
- `auditgovernance.Store` — accepted by `auditgovernance.New`; satisfied by the same assertion (embed, `runtime.go:18-20`).
- `auditgovernance.Runtime` public methods only: `Start`, `Close`, `Ready`, `PendingBacklogAge`, `BacklogAge`, `Degraded`, `Capture`.

---

## 3. Compatibility constraints

| # | Constraint | Design response |
|---|---|---|
| C1 | **I4 middleware chain**: handlers never self-attach auth; `Auth ≺ Tenant ≺ RateLimit` | Harness mounts `mw.Tenant(dav.Handler(...))` exactly like `dav_relay_test.go:81-83`; no auth middleware in the harness (CI baseline = no auth). Tenant via `X-Aero-Tenant` (absent → `default`, `middleware.go:25,46`) |
| C2 | **I6 stdlib-only + no new deps** | `testing`/`sync`/`atomic`/`net/http/httptest`/`database/sql` + `modernc.org/sqlite` (already a module test dep, `dav_relay_test.go` imports it); helpers copied, never imported from `package main` or `internal/integration` |
| C3 | **AGENTS.md hard gate: single file ≤500 lines** | Table-driven cells, one receiver, shared helpers; budget ≈470 lines (sibling `s3compat/audit_governance_e2e_test.go` = 321 lines for a comparable surface). Contingency if the gate is approached: move receiver+helpers to `dav_governance_e2e_receiver_test.go` (same package) — the named file keeps all cells and stays ≤500 |
| C4 | **Additive-only (AC-1.4)** | Governance wiring lives solely in the new harness; existing relay/audit suites construct no governance runtime → zero cross-talk; `TestWebDAVDelete_*`, `dav_audit_test.go`, `dav_test.go` untouched |
| C5 | **Race safety (`make test-race`)** | Receiver state mutex/atomic-guarded (`posts`, `postCount`, `tokenCalls`, `source`); hang-mode release channel closed in `t.Cleanup` **before** receiver close; `rt.Close` LIFO-last (bounded by claimTTL+httpTimeout) |
| C6 | **Config validation envelope** | AC-1.3 values satisfy `validAuditGovernanceWorker` (ClaimTTL 3 > 2×HTTPTimeout 1), `validAuditGovernanceRetry` (MaxBackoff 4 ≥ 2 ≥ Initial 1; MaxLag 4 > ClaimTTL 3), `boundedAuditGovernanceTiming` (all caps), HMAC ≥32B distinct from secrets, loopback-only URLs, `ClientSecretEnv` prefix `AUDIT_GOVERNANCE_CLIENT_SECRET_` — all verified in §0 |
| C7 | **Timing determinism** | Positives via `waitFor` (10s cap, 5-10ms poll); negatives via `quiesce` only (vacuous-at-t=0 discipline, AC-3.3); claim order asserted on **committed columns** (`available_at_ns, created_at_ns, id`), never wall-clock; no exact wall-clock upper bounds in D1 cells |
| C8 | **CI baseline scope** | SQLite + local FS only; no Postgres/Qdrant/network; `go test ./internal/api/webdav/` budget ≈ 36.9s + ~20s ≈ 57s, far under the default 10m package timeout |
| C9 | **`cmd/server` unimportable** | `rt.Ready(ctx)==nil` is the /readyz pin (D4/F11 — `readyzHandler` 503 iff `extra.Ready` err, `http.go:108-110`); no HTTP-level `/readyz` test (non-goal) |
| C10 | **No production harness hooks** | F2: the bus must hold the **wrapped** repo — a raw-repo bus yields HTTP 200 + zero rows; AC-1.1's positive `waitFor` on the first row makes that failure loud (never a vacuous pass) |
| C11 | **Events-relay isolation** | The governance harness never starts `EventOutboxRelay`; the governance relay claims only `audit_governance_outbox` rows — no cross-talk with the L1/L2 relay tests |

---

## 4. Failure modes

Each mode names the regression that would trigger it, the observable failure signature, and the AC that pins it. All failures are **loud** (timeout or assertion), none silent.

| # | Regression / drift | Failure signature | Caught by |
|---|---|---|---|
| FM-1 | Harness wires a **raw-repo bus** (main.go order broken; F2) | PUT/DELETE return 200; zero `audit_governance_outbox` rows | AC-1.1/2.1 `waitFor` on the first row times out (positive assertion — cannot vacuous-pass) |
| FM-2 | `RequiredScope`/`RequiredResource`/`resourceTransport` drift | `/token` returns 400 → `ErrTokenUnavailable` → transient retries; rows never deliver; `postCount` never reaches 2 | AC-2.5 (`tokenCalls==1` never true) + AC-2.4 timeout |
| FM-3 | Token fetch loses laziness/caching | Extra `/token` calls | AC-2.5 `tokenCalls==1`; AC-3.1/3.2 `tokenCalls==0` |
| FM-4 | Version-promotion emit fires on hard delete of a fresh single-version object (`file_delete.go:211-215`) | Third row appears | AC-2.1 exact-count (==1 per op, ==2 total) |
| FM-5 | `origin_id` bound to `objects.id` instead of `object_events.id` (F3/D2) | Row key mismatch + T-4 recompute mismatch | AC-2.1 `origin_id==eventRowID`; AC-2.3 formula equality |
| FM-6 | `created_at` parse drift (flexTime vs `time.Parse(RFC3339Nano)`) | `occurred_at_ns` mismatch vs recompute | AC-2.3 cross-check `occurredAt.UnixNano()` |
| FM-7 | Source-shape drift (prefix/length/algorithm) | `source_system` ≠ 54 chars / wrong prefix | AC-2.3 `aero-vault.` + `len==54` + recompute |
| FM-8 | Claim order drift (first POST ≠ PUT fact) | First observed `event_id` ≠ PUT row id | AC-2.4 first-POST assertion (committed columns) |
| FM-9 | Double-delivery (claim predicate loses `delivered_at_ns=0`/`failed_at_ns=0` fences) | `postCount` grows past 2 after quiesce | AC-2.4 `postCount==2` + `quiesce(50ms)` |
| FM-10 | Terminal rows re-claimable / dead-row exclusion regression | Post-terminal POSTs; `OldestPendingAuditGovernance` returns the failed row | AC-4.1 `quiesce` `postCount==2`; `(zero,false,nil)`; `BacklogAge ok==false` + cache `0/false` |
| FM-11 | `last_error` sentinel drift | Wrong/absent error text | AC-4.1 "audit governance HTTP 409"; AC-4.2 "reports a conflict" |
| FM-12 | D1: `probeAndRecord` stops recording degraded (age>maxLag → nil contract broken) | `BacklogAge()` never exceeds 4s while rows pending | AC-5.1 `waitFor(rt.BacklogAge() > 4s)` then `Ready()==nil` |
| FM-13 | D1: `Ready()` 503s on degraded backlog (B3-2 regression) | `Ready()` returns error in the degraded window | AC-5.1/5.2/5.3 `Ready()==nil` at every observation point |
| FM-14 | D1: hang POST wedges the loop (no ctx timeout / client Timeout) | No second claim/POST while receiver hangs | AC-5.2 `postCount ≥ 2` while hanging, `attempts ≥ 2` |
| FM-15 | Abort/release race over-pinned | Exact `postCount` assertion flaky | AC-5.2 pins `≥2` + delivered convergence, never an exact count |
| FM-16 | Goroutine leak under `-race` (release channel not closed before receiver close; `rt.Close` not LIFO-last) | `-race` failure / hang at test end | AC-5.2 cleanup order (D7, F12) |
| FM-17 | `ErrNoRows` treated as fatal in row helper | Draining/unbound cells fail on the first negative query | AC-3.1/3.2 `governanceOutboxRowForAction` ErrNoRows-tolerant (s3compat discipline) |
| FM-18 | Negative assertions via `waitFor` (vacuous at t=0) | False-green on absent rows/POSTs | AC-3.3 `quiesce`-only negatives (D6) |
| FM-19 | 500-line gate / file bloat | CI/review gate failure | C3 budget + contingency split |

---

## 5. Migration steps

**Zero runtime/operator migration** — no production edits (D3): no config keys, no endpoints, no schema. The steps below are the **landing sequence** for the worktree:

1. **Step 0 — commit the mechanism anchor (prerequisite, owned by sibling campaigns).** The harness pins mechanism code that is currently split across HEAD and uncommitted worktree state:
   - Committed at HEAD `15763e2`: `internal/auditgovernance/*` (runtime/relay/token/http/model/facts/redaction), migration 0042, `readyz`/gauge plumbing.
   - **Uncommitted (untracked or `M`)**: `internal/repository/migrations/{sqlite,postgres}/0043_*`, `0044_*` pairs; `internal/repository/audit_governance_factid.go` (+test); `internal/repository/audit_governance_claim.go`, `audit_governance_write.go`, `audit_governance_types.go` deltas; `internal/config/config_audit_governance.go` (+test) deltas (empty-bindings gate, retry floor); `cmd/server/governance_e2e_test.go`; `internal/api/s3compat/audit_governance_e2e_test.go`.
   - **No implementation of this design may start before that commit** — the harness's pins (dead-row predicates, store-authoritative fact IDs, 0043/0044 indexes, config validation envelope) have no committed anchor otherwise. This mirrors the sibling design's §7 step 0.
2. **Step 1 — add `internal/api/webdav/dav_governance_e2e_test.go`** per §1-§2 (cells of §1, harness API of §2.2, constraints of §3).
3. **Step 2 — local gates**: `gofmt -l .` clean; `go build ./...`; `go vet ./...`; `go test ./internal/api/webdav/ -count=1` green (≈57s budget); `go test ./internal/api/webdav/ -race -count=1` green; full `make check`.
4. **Step 3 — commit** the single test file (no `go.mod` changes; no migration files — 0043/0044 ride the mechanism commit of step 0).
5. **Rollback**: delete the test file; zero production impact by construction.

---

## 6. Testable acceptance mapping (direction acceptance, preserved verbatim → concrete tests)

| Supplied acceptance check (direction 1 text, verbatim) | Test function | Concrete pins |
|---|---|---|
| "(T-4) bound 'default' × (PUT,DELETE) → exactly one audit_governance_outbox row per event, origin_id==object_events.id, id==repository.DeterministicFactID recomputed from observed wire/row inputs, ^[0-9a-f]{32}$" | `TestWebDAVGovernance_BoundDefaultFirstEventFactID` (AC-2.1..2.4) | Pre-start snapshot on both rows: `attempts==0 ∧ delivered_at_ns==0 ∧ failed_at_ns==0 ∧ available_at_ns>0`. After `rt.Start` + delivery: exactly 2 rows (`file.created` from PUT, `file.deleted` from DELETE; `origin_kind='file'`, `fact_kind='file'`, `tenant_id='default'`); `origin_id == eventRowID` (the `object_events.id`, never `objects.id`); `id == wantFactID(source, "default", "file."+type, originID, createdRaw)` with `source` = first POST body `source_system` (`aero-vault.` prefix, `len==54`), `occurredAt.UnixNano() == occurred_at_ns`; `^[0-9a-f]{32}$` |
| "(B3-5) fake POST /token hard-asserts grant_type=client_credentials, scope==audit:event:write, resource==audit-governance, tokenCalls==1 for bound cell and ==0 for draining/unbound" | `TestWebDAVGovernance_BoundDefaultFirstEventFactID` (AC-2.5) + `TestWebDAVGovernance_DrainingAndUnboundLocalOnly` (AC-3.1/3.2) | Receiver `/token` route: `ParseForm` then `grant_type=="client_credentials" && scope==auditgovernance.RequiredScope && resource==auditgovernance.RequiredResource` + BasicAuth (violation → 400/401 → bound cell fails loudly, FM-2); bound cell `tokenCalls==1` (lazy cache, `token.go:47-72`); draining/unbound `tokenCalls==0` via `quiesce(1s)` |
| "(T-3) receiver modes 409 and 202-conflict → failed_at_ns>0, attempts==1, exactly 1 POST, absent from OldestPendingAuditGovernance, BacklogAge ok==false" | `TestWebDAVGovernance_Terminal409` (AC-4.1), `TestWebDAVGovernance_Terminal202Conflict` (AC-4.2/4.3) | Per row: `failed_at_ns>0 ∧ delivered_at_ns==0 ∧ attempts==1`; `last_error` contains "audit governance HTTP 409" / "reports a conflict"; exactly one POST per `event_id` (`postCount==2` distinct, `quiesce(50ms)`); `govStore.OldestPendingAuditGovernance(ctx) → (zero, false, nil)`; `rt.PendingBacklogAge(ctx) → (0, false, nil)`; after a poll-cycle probe `rt.BacklogAge()==0 ∧ rt.Degraded()==false`; `rt.Ready(ctx)==nil` |
| "(D1) modes 500 and hang → rt.Ready()==nil while BacklogAge>maxLag(4s) and at terminal, POST aborts at HTTPTimeout and the loop continues" | `TestWebDAVGovernance_Degraded500` (AC-5.1), `TestWebDAVGovernance_HangTimeoutLoopContinues` (AC-5.2/5.3) | 500 mode: rows pending (`delivered==0 ∧ failed==0`) with `attempts` growing across lease-gated re-claims; `waitFor(rt.BacklogAge() > 4s)` then `rt.Ready(ctx)==nil` (degraded ≈[4,6]s window, F9); `Ready()==nil` re-asserted at `failed_at_ns>0` terminal. Hang mode: blocked POST aborts at the 1s HTTPTimeout (rows pending, `Ready()==nil`), `postCount ≥ 2` while the receiver still hangs (lease-gated re-claim ≈3s — loop continues), release channel closed in `t.Cleanup` before receiver close, then `202-echo` converges to `delivered_at_ns>0` (final `postCount ∈ {≥2}`, never exact — FM-15) |
| "draining/unbound cells → 0 governance rows, 0 POSTs, local audit_log hard row still written" | `TestWebDAVGovernance_DrainingAndUnboundLocalOnly` (AC-3.1..3.3) | Draining (`bindingState:"draining"`) + unbound (`X-Aero-Tenant: other`) × (PUT, DELETE): `governanceOutboxRowForAction → found==false` (ErrNoRows-tolerant, FM-17); `quiesce(1s)` `postCount==0 ∧ tokenCalls==0`; `assertAuditRowFor(repo, tenant, "hard")` present (L0 always-on); `object_events` rows exist (gate-1 fallthrough, `repository.go:39-40`) |

**Effort note:** one new test file ≤500 lines, ≈470-line budget; zero production edits; package test time ≈ 57s total. Timing budget per cell: bound ≈2-3s, draining/unbound ≈1-2s, T-3 ≈2-3s each, D1 ≈8s each — all within `go test` defaults, `-race` included.

---

## 7. Decisions & non-goals

**Decisions (from spec D1-D8, design-verified):** D1 relay `Start` deferred (snapshot race-free) · D2 origin identity = `object_events.id` · D3 zero production changes (gate already in worktree — §0 caveat: uncommitted) · D4 `rt.Ready()==nil` is the /readyz pin (cmd/server unimportable) · D5 harness timing shrink MaxBackoff/MaxLag → 4s (validated envelope, deterministic degraded window) · D6 `waitFor` positives / `quiesce` negatives only · D7 cleanup LIFO + hang-release-before-close · D8 single new file, package `webdav_test`, table-driven.

**Non-goals (unchanged from spec §5):** B3-6 empty-bindings config test pin (direction 2; gate shipped, test missing — sibling `activation-gate-fail-closed` campaign); full T-3 permanent-class matrix (422/malformed/tenant-mismatch/200-then-202 — sibling terminal-classification design); cumulative-window terminalization timing asserts (sibling T-3); MOVE copy-then-delete governance flow (direction 3); T-4 gap-reconcile e2e (sibling s3compat deterministic-fact-ids); HTTP-level `/readyz` request (cmd/server package space); L1/L2 events-outbox relay behavior; draining delivery lifecycle (drain-503 semantics already shipped/unit-pinned); Postgres variant, telemetry/metrics, config surface changes.
