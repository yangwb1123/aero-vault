# Activation-Gate Scope-Alignment Matrix E2E — v2 Design (hardened)

> Supersedes `activation-gate-scope-alignment-matrix-e2e-v1.design.md` (v1's evidence ledger E1–E13 remains accurate; v2 fixes harness-mechanics errors found by re-auditing the same checkout HEAD `15763e2`).
> **Revision 2.1 (REQ-traceability re-audit closure):** adds the 4th permanent-class matrix cell M6 (`conflict:true` receipt — closes recommendation (a)), documents the REQ-4 drift→loud-failure chain (closes recommendation (b)), and fixes two surviving acceptance-assertion defects (M4 sentinel substring; sourceID shape-pin length). See §9.
> Parent spec: `docs/auto/runs/activation-gate-scope-alignment-matrix-e2e-at-th-25e1ba30/artifacts/requirements-10762e10/requirements.md`
> Direction: **test-only**. No production code, schema, or dependency changes. Target: `cmd/server/governance_e2e_test.go` (package `main`), ≤ 500 lines hard gate.

---

## 1. Hardening audit ledger (v1 → v2, each claim re-verified against source)

| # | v1 claim | Verdict | Evidence (this checkout) | v2 fix |
|---|----------|---------|--------------------------|--------|
| A1 | Fake `/token` returns `{"AccessToken":…,"TokenType":…,"ExpiresIn":…}` (untagged field names) | **BLOCKING — wrong wire format** | SDK decodes via **tagged** `wireTokenResponse` (`access_token`/`token_type`/`expires_in`/`scope`, snaplink `interfaces/ssoclient/remote/token.go:196-218`); untagged `ssoclient.TokenResponse` is only the post-decode struct. Key `"AccessToken"` ≠ tag `access_token` → SDK error `token response omitted access_token` → `fetch` → `ErrTokenUnavailable` → every POST transient → all tests time out. In-repo precedent: `internal/auditgovernance/runtime_test.go:57` `{"access_token":"token","token_type":"Bearer","expires_in":60}` | §3.2: snake_case body `{"access_token":"e2e-token","token_type":"Bearer","expires_in":3600,"scope":"audit:event:write"}` |
| A2 | Scope pin "optional; don't fail if SDK omits" | **Upgradeable to hard** | `token.go:64` always passes `RequiredScope`; SDK sends `scope` iff `len(scopes)>0` (`remote/token.go:150-152`) → scope **always** present. `resource=audit-governance` injected by `resourceTransport` (`token.go:102-128`). Including `scope` in the token response exercises `validTokenScopes` exact-match path (`token.go:152-153`) | §3.2: token handler hard-asserts form `grant_type=client_credentials`, `scope=audit:event:write`, `resource=audit-governance`; response includes `scope` |
| A3 | "receiver derives tenant_id from the body if it can" | **Impossible — wire has no tenant** | `governanceEvent` (`model.go:60-88`) carries `event_id/source_system/…` — **no `tenant_id`**; `governancePayload` (`http.go:161-175`) has `fact_kind` + optional size/backend only | §3.2: tenant echo comes **solely from the per-cell script constant**; document the absence |
| A4 | `putObject` returns `Object.ID` = outbox `origin_id` | **BLOCKING — false equivalence** | Outbox `origin_id` = **`object_events` row id** (`RETURNING id` at `audit_governance_write.go:66-76`, `fact.OriginID = id` `:78-79`), a different AUTOINCREMENT sequence from `objects.id` returned by `Put` | §3.4: helper `eventRowID(t, dsn, objectID)` = `SELECT id FROM object_events WHERE object_id=? ORDER BY id DESC LIMIT 1`; used for outbox row, REQ-2 `ErrNoRows`, `wantFactID` |
| A5 | REQ-2: `tokenCalls` "must be 1" | **Wrong for zero-POST test** | Token fetch is lazy (`AccessToken`, `token.go:52-58`); no POST → no fetch | §3.5: REQ-2 asserts `tokenCalls==0`; the 7 POSTing tests (BoundTenant + M1–M6) assert `==1` |
| A6 | REQ-2: `waitFor(1s, postCountTotal()==0)` | **Vacuous** | `waitFor` polls until cond true; cond is true at t=0 → returns instantly, proves nothing | §3.4: negative assertions use `quiesce(1s, …)` (stability window), never `waitFor` |
| A7 | "Records POSTs `[]event_id` (atomic)" | **Race — slice cannot be atomic** | `-race` sweep (G1) would fail | §3.2: `sync.Mutex`-guarded POST list `{eventID, at}` + `atomic.Int64` counters |
| B1 | M1 post-PUT snapshot asserts `attempts=0, delivered=0, failed=0` with relay already started | **Racy** | pollEvery=5ms; claim can fire between `Put` return and first read | §3.4/§3.5: harness **does not start the relay**; tests PUT first, snapshot deterministically, then `rt.Start(ctx)` (REQ-1 "first PUT → one row + one POST" still proven — relay claims exactly once after start) |
| B2 | M5 intermediate `available_at_ns > now` | **Racy/unsound** | `CompleteAuditGovernance` (`audit_governance_claim.go:126-131`) never clears `available_at_ns` → post-terminal reads see a stale past timestamp | §3.5 M5: two-stage deterministic wait (state persists ≥750ms): `waitFor(postCount==1 ∧ last_error!='' ∧ delivered=0 ∧ failed=0)` → assert `available_at_ns − POST1.at ≥ 700ms` (both timestamps fixed once `retryFact` commits — zero wall-clock reads) → `waitFor(delivered ∧ attempts==2 ∧ last_error='')` → `quiesce`: POSTs==2 ∧ `Δt(POST2−POST1) ≥ 700ms` |
| B3 | Backoff "1s ± 25 % → ≤1.25 s" | **Claim correct as function property** | `boundedBackoff` (`relay.go:181-197`): fraction ∈ [−250,250] → [0.75,1.25]s; clamps [0.5,2]s don't bind for initial=1s/max=2s | Keep math claim in doc; E2E asserts only the **lower bound** ≥700ms (proves 200-transient was gated by backoff, not retried immediately) — no wall-clock upper bound (scheduling/-race noise). Unit precedent: `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189`) |
| C1 | `occurredAt` "DB-default ns" | **Imprecise** | `object_events.created_at` = TEXT, `strftime('%Y-%m-%dT%H:%M:%fZ','now')` (migration `0003_events.up.sql:11`, ms precision); `flexTime` parses RFC3339Nano (`sql_helpers.go:196-233`) | §3.4: read `created_at` as TEXT, parse `time.RFC3339Nano` (matches the write path's `RETURNING created_at` scan byte-for-byte) |
| C2 | I1 placeholders | **Correct** | Harness SQL is raw SQLite (`?`); `rebind` (`sql.go:106-127`) is production-side. Precedent `authz_gate_test.go:144-146` | Keep; `?` only, ns int64 comparisons |
| E1 | WAL concurrency | **Correct, with nuance** | `repository.Open` → `journal_mode=WAL` + `foreign_keys=ON` + `db.SetMaxOpenConns(1)` (`sqlite.go:31-43`); harness raw conn is a WAL **reader** → never `SQLITE_BUSY`; repo writes serialize in one pool | Keep; no `busy_timeout` needed (harness conn is read-only); test writes stay single-goroutine pre-`waitFor` |
| F1 | "t.Cleanup registers rt.Close() before repo.Close()" | **LIFO-inverted ambiguity — dangerous as literally read** | Cleanup runs **last-registered first** (`testing.go:1630-1660`) | §3.4: register `receiver.Close()` first, `repo.Close()` second, `rt.Close()` **last** → execution `rt.Close → repo.Close → receiver.Close` |
| F2 | Close bound `claimTTL+httpTimeout` | **Correct** | `runtime.go:122-134`; in-flight POST uses Background-derived ctx (deliverFact `relay.go:124-125`) → completes ≤ httpTimeout before drain | Keep; use `context.Background()` for `rt.Start` (T.Context is canceled *before* Cleanup — `testing.go:1584-1587` — works, but Background removes the coupling) |
| G1 | Sweep: "make test-race" | **BLOCKING — doesn't cover this file** | `make test-race` = `go test -race -count=1 -timeout 120s ./internal/...` (Makefile:106-109) — **`./cmd/server/` excluded**; `make check` = `fmt vet vet-integration build test test-race-meta cli-check` (Makefile:123), `test` = plain `go test ./...` (no `-count=1`, cached replay OK) | §7: add explicit `go test -race -count=1 -timeout 120s ./cmd/server/` |
| H1 | Binding config `{TenantID, ClientID, ClientSecret, State}` | **BLOCKING — Validate rejects it** | `validateAuditGovernanceBindings` requires `validAuditSecretEnv(ClientSecretEnv)` (`config_audit_governance.go:152, 288-291`, `envNamePattern = ^[A-Z_][A-Z0-9_]*$`) — empty value fails → `Runtime.New` returns error | §3.3: add `ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E"` |
| H2 | `wantFactID` sourceID = `"aero."+base64url(hmac…)` | **Wrong prefix + fragile private-formula duplication** | `SourcePrefix = "aero-vault"` (`model.go:12`), `tenantSourceID` = `SourcePrefix + "." + base64url(hmac(redactionDomain\0 tenant\0 "source-system"\0 tenant\0))` (`redaction.go:43-49`, `writeMACFields` adds NUL per field). Wire `source_system` == `fact.SourceID` (set in `governanceWire`, `http.go:142-160`) | §3.4: **wire-derived** `source`: capture `source_system` from the first POST body → `DeterministicFactID(wireSource, "acme", "file."+rowType, "file", eventRowID, occurredAt)` — pure observed-input recomputation, zero private-formula duplication; plus cheap shape pin: `strings.HasPrefix(source, "aero-vault.") ∧ len==54` (11 + 43 base64url-raw chars; the v2 "56" was itself wrong — see §9.2 R2)

Everything not listed above (M2/M3/M4 permanent classes, `202-echo` receipt shape, per-origin counting, claim predicates, quiesce rationale, unbound-backlog startup block, config durations) was re-verified **correct** in v1 and is carried forward unchanged.

---

## 2. Scope & non-goals (unchanged from v1)

**In scope:** one new test file; harness + REQ-1/REQ-2 activation gate (bound → exactly one outbox row + one POST; unbound → zero rows); REQ-3 matrix M1–M5 with exact per-cell outbox state; T-3 pins (202-only acceptance, 200 transient, permanent closed list) through observed wire behavior; deterministic-fact-ID pin (E13) via recomputation from observed inputs.
**Non-goals:** no production change; no `audit_log`/admin-path coverage; no Postgres variant (CI is SQLite); no REQ-4 source-grep hard gate (behavioral pins only); no webhook/`event_outbox` interaction (bus has no subscribers).

## 3. Harness design (`governance_e2e_test.go`)

### 3.1 Wiring (main.go order, `main.go:70-90`) — relay start is **explicit, after PUT** (B1)

```
repo   = repository.Open(ctx, "sqlite", "file:"+tmp/"e2e.db")   // + Migrate
store  = storage.NewLocal(LocalConfig{Root: tmp/"objects"})
cfg    = e2eConfig(t, receiverURL)          // §3.3
rt     = auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)   // applies bindings (backlog-free: no PUT yet)
wrepo  = auditgovernance.WrapRepository(repo, rt)
bus    = events.New(wrepo, logger); bus.WithRepository(wrepo)   // mirrors main.go:82-83 (no-op-safe)
svc    = service.NewFileService(store, wrepo, logger).WithEventSink(bus)
// t.Cleanup: rt.Close() (registered LAST, runs FIRST), repo.Close(), receiver.Close()  — see F1
// tests then: putObject → deterministic snapshot → rt.Start(context.Background())
```

Imports: `context, bytes, encoding/json, fmt, io, log/slog, net/http, net/http/httptest, path/filepath, strings, sync, sync/atomic, testing, time, database/sql` + `internal/{auditgovernance,config,events,repository,service,storage}`. Stdlib only (I6). Package `main` (sibling precedent `cmd/server/audit_governance_test.go`).

### 3.2 Fake receiver (one `httptest.Server`, two routes, mutex-guarded state)

| Route | Behavior |
|---|---|
| `POST /token` | Always `200 application/json` with **snake_case OAuth2 wire** (A1, precedent `runtime_test.go:57`): `{"access_token":"e2e-token","token_type":"Bearer","expires_in":3600,"scope":"audit:event:write"}`. **Hard-asserts** the request form: `grant_type=client_credentials`, `scope=audit:event:write`, `resource=audit-governance` (A2). Counts calls (`atomic.Int64`). |
| `POST /api/v1/events` | Asserts path == `api/v1/events` and query == `wait_for=ledgered` (REQ-4 pin, `model.go:19` + `http.go:36-39`). Scripted per POST-sequence index (fresh harness per cell). Decodes body → `event_id`, `source_system` (captured for `wantFactID`). **Tenant echo is script-constant only — the wire carries no `tenant_id`** (A3). Modes: `202-echo` (echo `event_id`, script tenant, `status=ledgered`, `accepted_at=time.Now().UTC()`, `conflict=false`), `202-conflict` (valid echo receipt with `conflict=true` — the **4th permanent class**, `ErrReceiptConflict`; contract A, `http.go:196-200`), `409`, `422`, `200-then-202`, `202-wrong-tenant` (valid JSON, `tenant_id` = "mallory"). All 202 responses: `Content-Type: application/json` explicitly (else `ErrInvalidReceipt`, `http.go:178-185`). Records `[]post{eventID, at}` under mutex + `atomic.Int64` count (A7). |

Response body shape (matches `receiptEnvelope`, `model.go:39-52`): `{"receipt":{"event_id":"…","tenant_id":"acme","status":"ledgered","accepted_at":"…","conflict":false,"duplicate":false}}`.

### 3.3 Config — **passes `Validate` exactly** (H1 fix + re-verified durations)

```go
cfg := config.AuditGovernanceConfig{
    Enabled: true,
    BaseURL: receiver.URL,  TokenURL: receiver.URL + "/token",   // loopback http allowed (secureEndpoint)
    HMACKey: "0123456789abcdef0123456789abcdef",                  // 32 B, ≠ client secrets
    HTTPTimeoutSeconds: 5,  PollMilliseconds: 5,  BatchSize: 16,
    ClaimTTLSeconds: 30,    InitialBackoffSeconds: 1,  MaxBackoffSeconds: 2,
    MaxLagSeconds: 60,      ReconcileBatchSize: 8,
    DeliveredRetentionSeconds: 3600,  CleanupIntervalSeconds: 60,  CleanupBatchSize: 100,
    Revision: 1,
    Bindings: []config.AuditGovernanceBinding{{
        TenantID: "acme", ClientID: "e2e-client",
        ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E",     // REQUIRED by validAuditSecretEnv (H1)
        ClientSecret: "e2e-secret-0000", State: "active",
    }},
}
```

Constraint re-check (all pass): `ClaimTTL 30 > 2×HTTP 5` ✓ · `MaxLag 60 > ClaimTTL 30` ✓ · `PollMs 5 ≤ 60k` ✓ · `HTTP 5 ≤ 29` ✓ · retention/cleanup bounds ✓ · HMAC ≥32 B, ≠ secrets ✓ · binding env name `^[A-Z_][A-Z0-9_]*$` + prefix ✓ · revision > 0 ✓.

### 3.4 Assertion helpers

- `eventRowID(t, dsn, objectID) int64` — `SELECT id FROM object_events WHERE object_id=? ORDER BY id DESC LIMIT 1` (A4: outbox `origin_id` is the **event row id**, not `objects.id`).
- `outboxRow(t, dsn, eventRowID) (row, error)` — raw `database/sql` over the same DSN (precedent `authz_gate_test.go:144-146`; I1 `?` only):
  ```sql
  SELECT id,tenant_id,origin_kind,origin_id,fact_kind,action,attempts,
         available_at_ns,claim_owner,claim_token,lease_expires_at_ns,
         last_error,delivered_at_ns,failed_at_ns
  FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?
  ```
  All columns exist in `0039` + `0042` (failed_at_ns). Per-origin counting only — async convergence can't bleed across cells.
- `wantFactID(t, dsn, objectID)` — read event row (`id`, `type`, `created_at` TEXT → `time.Parse(time.RFC3339Nano)`), take `source_system` from the first POST body, recompute `repository.DeterministicFactID(source, "acme", "file."+type, "file", eventRowID, occurredAt)` (`audit_governance_factid.go:27-35`; occurred truncated to seconds by the function itself) and compare to outbox `id`; also assert `^[0-9a-f]{32}$` + `source` shape pin `aero-vault.` prefix, len 54 (H2; 11 + 43 base64url-raw chars — see §9.2 R2). Inputs are all *observed* (wire + DB) — drift in any formula input breaks the pin loudly.
- `putObject(t, svc, tenant, key)` → `svc.Put(ctx, tenant, "default", key, strings.NewReader("x"), 1, PutOptions{})` (returns `repository.Object`; used only for the `object_events` lookup and `o.TenantID`). **Synchronous invariant (verified):** `emit` (`file.go:308-324`) → `bus.Publish` → `InsertEventWithGovernance` run inline in the request goroutine (`bus.go:81-104`, `file_crud.go:255` before `return saved`) → outbox row exists when `Put` returns. So row-presence assertions are race-free; only relay *transitions* need `waitFor`.
- `waitFor(t, cond, 10s)` — poll 5 ms; on timeout `t.Fatalf` with last observed row dump. Test-goroutine only (safe `t.Fatalf`).
- `quiesce(t, d, cond)` — sample every 5 ms for `d`; fail on first false sample. **Negative assertions use `quiesce`, never `waitFor`** (A6).
- `startRelay(t, rt)` — `rt.Start(context.Background())` (F3). Harness helper does NOT auto-start (B1).

### 3.5 Test inventory (revised mechanics)

| Test | REQ | Script | Assertions (exact) |
|---|---|---|---|
| `TestGovernanceE2EActivationGateBoundTenant` | REQ-1 | `202-echo` | PUT → **snapshot before Start** (deterministic): row count=1 for `eventRowID`, `tenant_id=acme`, `origin_kind='file'`, `attempts=0`, `delivered=0`, `failed=0`, `available_at_ns>0`; `object_events` row exists → `startRelay` → `waitFor`: POSTs==1 ∧ `delivered_at_ns>0` ∧ `attempts=1` ∧ `claim_owner=''` ∧ `last_error=''`; `quiesce(50ms)`: POSTs stays 1; fact ID == `wantFactID` recomputation; POST body `event_id == outbox.id`, `Authorization: Bearer e2e-token` present; `tokenCalls==1` |
| `TestGovernanceE2EActivationGateUnboundTenant` | REQ-2 | `202-echo` (unused) | PUT to `tenant="other"` → `outboxRow` = `sql.ErrNoRows` for its `eventRowID`; `object_events` row **exists** (gate-1 fallthrough `auditgovernance/repository.go:39-40`) → `startRelay` → `quiesce(1s)`: `postCountTotal()==0`; `tokenCalls==0` (A5) |
| `TestGovernanceE2EMatrixDelivered` | M1 | `202-echo` | PUT → Start → `waitFor`: `delivered_at_ns>0 ∧ failed_at_ns=0`, `attempts==1`, `last_error=''`, `claim_owner=''`; `wantFactID` |
| `TestGovernanceE2EMatrixConflict409` | M2 | `409` | PUT → Start → `waitFor`: `failed_at_ns>0 ∧ delivered_at_ns=0`, `attempts==1`, `last_error` contains `"409"`; `quiesce(50ms)`: POSTs stays 1 (permanent closed list `relay.go:228-236`; terminal predicate makes re-claim impossible — no retry ever, quiesce is belt-and-braces) |
| `TestGovernanceE2EMatrixUnprocessable422` | M3 | `422` | same shape; `last_error` contains `"422"` |
| `TestGovernanceE2EMatrixTenantMismatch` | M4 | `202-wrong-tenant` | receipt `tenant_id="mallory"` ≠ fact tenant → `ErrInvalidReceipt` (`http.go:178-185`/`receiptMatches` `:209-219`) → `failed_at_ns>0`, `attempts==1`, `last_error` contains `"receipt is invalid"` (sentinel text `model.go:26`; the reverse substring "invalid receipt" never occurs — F2/R1) |
| `TestGovernanceE2EMatrixTransient200` | M5 | `200-then-202` | PUT → Start → **stage 1** `waitFor`: POSTs==1 ∧ `last_error!=''` ∧ `delivered=0 ∧ failed=0` (state persists ≥750ms — backoff `[0.75,1.25]s` from `boundedBackoff`, B3) → assert `available_at_ns − POST1.at ≥ 700ms` (B2; both timestamps fixed — no wall-clock read) → **stage 2** `waitFor`: `delivered_at_ns>0 ∧ attempts==2 ∧ last_error=''` → assert outbox `id` == `wantFactID` recomputation (E13 at terminal, per §8) → `quiesce(50ms)`: POSTs==2; `Δt(POST2−POST1) ≥ 700ms` (deterministic: claim predicate gates on `available_at_ns`, `audit_governance_claim.go:73-82`; no upper-bound wall-clock assert) |
| `TestGovernanceE2EMatrixConflict` | M6 | `202-conflict` | PUT → Start → `waitFor`: `failed_at_ns>0 ∧ delivered_at_ns=0`, `attempts==1`, `last_error` contains `"reports a conflict"` (distinct `ErrReceiptConflict` sentinel `model.go:27` — proves the failure is the conflict class, not `ErrInvalidReceipt`); `quiesce(50ms)`: POSTs stays 1 — the **4th member of the permanent closed list** (`relay.go:228-236`), terminal → no re-claim ever |

M2/M3/M4/M6 are table-driven through one shared runner (M6's only deltas: scripted mode + `last_error` sentinel) to hold the 500-line budget.

### 3.6 REQ-4 (grep-consistency) — disposition carried from v1, one upgrade

Behavioral pins only (no source-grep test — no precedent, brittle); accepted-risk disposition documented here and in §8:
- POST path+query `api/v1/events?wait_for=ledgered` asserted by the receiver on first POST (drift in `model.go:19` breaks the e2e).
- Scope pin **upgraded from optional to hard** (A2): token handler asserts `scope=audit:event:write` + `resource=audit-governance` in the form body — `token.go:64` + `resourceTransport` make these unconditional.
- **RequiredScope-drift compensation (v1 compensating assertion ②, now explicit):** the fake `/token` response echoes `"scope":"audit:event:write"` (§3.2), which the SDK parses into `Scopes` (`remote/token.go:215`) and `validTokenScopes` exact-matches against `RequiredScope` (`token.go:152-153`). A drift of the constant fails **both** tripwires: ① the fake's hard form assertion (the request carries the drifted scope) and ② `validTokenScopes` (response scope ≠ drifted constant). Each surfaces as `ErrTokenUnavailable` (`token.go:65-66`) → transient → bounded-backoff retry → M1's `waitFor(10s)` times out with the `last_error` dump. A `RequiredScope`/`RequiredResource` drift therefore fails loudly at M1; it cannot silently pass.

## 4. API changes (unchanged)

- Production API: none.
- Test API: unexported helpers in the new file only: `newGovernanceE2E` (returns `{svc, dsn, receiver, rt}` — relay **unstarted**), `eventRowID`, `outboxRow`, `wantFactID`, `waitFor`, `quiesce`, `startRelay`, `putObject`, `postCount`/`postTimes`. No new packages; `go.mod` untouched (I6).

## 5. Compatibility constraints (unchanged, re-confirmed)

- **I4:** harness reproduces `main.go:79-89`: `WrapRepository` → `bus.WithRepository` → `NewFileService(...).WithEventSink(bus)`; relay start deferred but still after wiring. Any reordering → zero rows → tests fail (the point).
- **I5:** direct component construction; `buildAuditGovernanceRuntime` deliberately not used (env + bindings file path). CI baseline: SQLite + local FS + loopback httptest.
- **I1:** harness SQL `?` only; ns int64 columns compared as int64; `created_at` TEXT parsed RFC3339Nano (byte-identical to the write path's `RETURNING` scan — `flexTime` layouts, `sql_helpers.go:216-233`).
- **I2:** zero migrations; `Open`+`Migrate` on temp dir → full 0043 schema.
- **I6:** stdlib only.
- **Gate:** ≤ 500 lines (`cli.py check-filesize` via `make check` → `cli-check`); table-driven M2–M4/M6; one shared harness.
- Determinism: no sleeps beyond `waitFor`/`quiesce`; per-origin counting; all backoff-proof timestamps are fixed once the retry commits (`available_at_ns`, receiver-recorded POST times) — no wall-clock reads; lower-bound-only asserts.

## 6. Failure modes & mitigations (v1 table, corrected)

| Mode | Symptom | Mitigation |
|---|---|---|
| Relay claims row before snapshot (B1) | `attempts>1` in M1 pre-state | Relay starts **after** the deterministic snapshot; terminal assertions always `waitFor` on the expected predicate |
| M5 backoff jitter (B3) | second POST at `[0.75,1.25]s` | `waitFor` 10 s; lower-bound-only timing asserts on fixed timestamps (`Δt ≥ 700ms`, `available_at − POST1.at ≥ 700ms`); never a fixed-delay or upper-bound wall-clock assert |
| **Fake token wire shape (A1)** | `ErrTokenUnavailable` → retries → timeouts | snake_case body per SDK decoder; failure surfaces as clear `waitFor` timeout + `last_error` dump |
| **Origin-id equivalence (A4)** | wrong row / false `ErrNoRows` | `eventRowID` lookup from `object_events` by `object_id`; never `Object.ID` |
| SQLite concurrency | `database is locked` | WAL + `MaxOpenConns(1)` (`sqlite.go:31-43`); harness conn is read-only → no busy risk; test writes single-goroutine pre-`waitFor` |
| Fake 202 without `application/json` | spurious `ErrInvalidReceipt` → M1 fails as M4 | Content-Type set explicitly; M4 differs only by `tenant_id` |
| **Cleanup LIFO (F1)** | repo closed under live relay → use-after-close | registration order `receiver → repo → rt`; execution `rt → repo → receiver`; `rt.Close` bounded by `claimTTL+httpTimeout` (`runtime.go:122-134`) |
| Unbound-backlog startup block | `applyDesiredBindings` error if rows pre-binding | bindings applied at `New` before any PUT; T2 writes only to an unbound tenant |
| `tokenCalls` drift (A5) | REQ-2 asserts 1 → fails | REQ-2 asserts `tokenCalls==0`; POSTing tests assert `==1` at end (no timing dependency) |
| "Exactly one POST" flake | relay double-delivery after ack-lost | ack-lost re-claim requires lease expiry (30 s) ≫ `quiesce` windows (50 ms); terminal predicates make re-claim impossible |

## 7. Migration & sweep plan (G1 fix)

1. Land this design; PR contains **only** `cmd/server/governance_e2e_test.go`.
2. Implement per §3; `gofmt`; `go vet ./cmd/server/`.
3. `make check` (gofmt/vet/build/`go test ./...`/test-race-meta/cli-check incl. filesize ≤ 500). Note: plain `go test ./...` may serve cached results — fine for gate, not for flake detection.
4. **Flake sweep:** `go test ./cmd/server/ -run 'TestGovernanceE2E' -count=1 -v` ×3.
5. **Race sweep:** `go test -race -count=1 -timeout 120s ./cmd/server/` — **required because `make test-race` covers `./internal/...` only** (Makefile:106-109). Optionally also `go test -race -count=1 -timeout 120s ./internal/auditgovernance/ ./internal/repository/`.
6. No schema/rollback/ops steps. Rollback = delete the file.

## 8. Acceptance mapping (unchanged mapping; corrected mechanics)

| Requirement | Test | Acceptance |
|---|---|---|
| REQ-1 (bound tenant, first PUT → exactly one outbox row + one POST) | `…BoundTenant` | row count=1 for the event origin, POST count=1 after quiesce, delivered terminal, fact-ID recomputation matches |
| REQ-2 (unbound → zero rows) | `…UnboundTenant` | `sql.ErrNoRows` on origin query; `quiesce(1s)` total POSTs==0; `object_events` row present (gate-1 fallthrough) |
| REQ-3 (M1–M5 exact per-cell state) | 5 `…Matrix*` | per-cell predicates as §3.5; M2/M3/M4 `failed_at_ns>0` with `attempts==1`; M5 transient→delivered with `attempts==2`, exactly 2 POSTs, backoff-gated Δt |
| REQ-5 (T-3 pins) | all matrix tests | M1 202-only acceptance; M5 200 ∉ permanent list; M2/M3/M4/**M6** prove the **full 4-member permanent closed list** (409 / 422 / invalid-receipt / `conflict:true` receipt, `relay.go:228-236`) — single POST each, no retry |
| REQ-4 (proposed, accepted-risk) | behavioral pins | POST path+query pin (hard); token form scope+resource pins (hard); response echoes `scope=audit:event:write` → `RequiredScope` drift fails M1 loudly via `ErrTokenUnavailable` (see §3.6); no source-grep |
| T-3 / M6 — `conflict:true` receipt (extension beyond REQ-3's M1–M5; closes v1 recommendation (a)) | `TestGovernanceE2EMatrixConflict` | 202+`conflict=true` → terminal `failed_at_ns>0`, `attempts==1`, `last_error` = `"audit governance receipt reports a conflict"`, exactly 1 POST (no retry, terminal-with-retention) |
| E13/T-4 identity pin | `wantFactID` in M1 + M1/M5 terminal | outbox `id` == `DeterministicFactID` over observed wire `source_system` + event-row inputs |
| Three supplied acceptance checks | harness invariants | ① one row + one POST for bound first-PUT; ② zero rows for unbound; ③ cells reach exactly the scripted terminal state, no retry beyond the classified class |

---

## 9. Re-audit closure (REQ-1–REQ-5 traceability, revision 2.1)

Re-audit of this design against the acceptance-mapping/coverage table (referenced as "§7" in the review task; it is §8 in both v1 and v2 — stale numbering, same table). All code pins re-verified against HEAD `15763e2`.

### 9.1 Coverage table verdict

| Requirement | Cell | Verdict | Evidence |
|---|---|---|---|
| REQ-1 (bound first-PUT → exactly one row + one POST) | `…ActivationGateBoundTenant` | ✅ complete | deterministic pre-start snapshot (B1) + `waitFor` POSTs==1 + `quiesce(50ms)` stays 1 + fact-ID recompute |
| REQ-2 (unbound → zero rows) | `…ActivationGateUnboundTenant` | ✅ complete | `sql.ErrNoRows` + `quiesce(1s)` POSTs==0 + `tokenCalls==0` (A5/A6) + gate-1 fallthrough row |
| REQ-3 (matrix M1–M5 exact per-cell state) | 5 `…Matrix*` | ✅ complete (as specified) | per-cell predicates §3.5; M5 two-stage backoff proof (B2/B3) |
| REQ-5 (T-3 pins) | all matrix tests | ✅ complete **after M6** | M1 202-only · M5 200-transient · M2/M3/M4/M6 = all 4 permanent members (`relay.go:228-236`) |
| REQ-4 (grep-consistency, proposed) | behavioral pins | ✅ documented accepted-risk | §3.6: no source-grep (no precedent, brittle); hard path+query, form scope+resource pins; response-scope drift compensation (§9.3) |
| E13/T-4 identity pin | `wantFactID` (M1 + M1/M5 terminal) | ✅ complete | pure recomputation over observed wire `source_system` + event-row inputs; shape pin corrected to len 54 (§9.2) |
| Three supplied acceptance checks | harness invariants | ✅ preserved | rows ①/②/③ in §8 |

### 9.2 Defects found by this re-audit and fixed

| # | Defect (verified at HEAD) | Fix |
|---|---|---|
| **R1 (was F2, survived v2)** | M4 asserts `last_error` contains `"invalid receipt"` — the actual sentinel is `"audit governance receipt is invalid"` (`model.go:26`); the substring never matches → M4 fails deterministically | §3.5 M4 row: assert `"receipt is invalid"` |
| **R2 (H2 shape pin, survived v2)** | `wantFactID` shape pin `len==56` — actual `tenantSourceID` = `SourcePrefix + "." + base64.RawURLEncoding(hmac-sha256)` (`redaction.go:43-49`) = 11 + 43 = **54** chars (verified by recomputation); pin never matches | §3.4: `len==54` |
| **R3 (M5 stage-1 assert, hardened)** | `available_at_ns − now ≥ 500ms` reads the wall clock after `waitFor` returns; the residual (~740ms) needs the test goroutine to survive ~240ms of preemption — a `-race` flake vector on loaded CI | §3.5/§6/§5: assert `available_at_ns − POST1.at ≥ 700ms` — both timestamps are fixed once `retryFact` commits (`available_at = POST1.at + delay + ε ≥ 750ms`); zero wall-clock reads |
| **R4 (E13 pin, doc gap)** | §8/§9.1 claim `wantFactID` at "M1 + M1/M5 terminal" but §3.5's M5 row omitted it | §3.5 M5 stage 2: add outbox `id` == `wantFactID` recomputation (inputs: first-POST `source_system` + event row — both observed) |
| **R5 (M6-addition residue)** | Adding M6 left three stale spots: A5 row said "6 POSTing tests" (now 7), a dangling "(H4)" reference in §3.5, and M2's row cited `relay.go:228-237` (file is 236 lines) | A5 → "7 POSTing tests (BoundTenant + M1–M6)"; drop "(H4)"; M2 row → `relay.go:228-236` |

### 9.3 Open recommendations from the v1 requirements review — closure

- **(a) 4th permanent class (`conflict:true` / `ErrReceiptConflict`) — closed by matrix cell.** The closed list (`relay.go:228-236`) has 4 members; v2 exercised 3. Added `202-conflict` receiver mode (§3.2), matrix cell M6 (§3.5), and an explicit acceptance row in §8 (T-3/M6). Cost: one scripted mode + one table-driven row (M6's deltas are the mode and the `last_error` sentinel `"reports a conflict"`, distinct from M4's `"receipt is invalid"`). Strictly stronger than documenting acceptance-by-unit-test (`runtime_test.go:117` still pins retention semantics at unit level).
- **(b) REQ-4 accepted-risk disposition — closed by explicit documentation.** Disposition (behavioral path/query + scope/resource pins, no source-grep) was already in §2/§3.6; now the compensating assertion is spelled out end-to-end (§3.6): the fake token response echoes `scope="audit:event:write"` → SDK parses it into `Scopes` (`remote/token.go:215`) → `validTokenScopes` exact-matches `RequiredScope` (`token.go:152-153`) → a constant drift trips the form pin **and** the scope pin, each surfacing as `ErrTokenUnavailable` → transient → retry → M1 `waitFor(10s)` timeout with `last_error` dump. A `RequiredScope`/`RequiredResource` drift fails M1 loudly; it cannot silently pass. The fake asserts the form before serving the token, so the response-scope check is reachable only when the form pin already passed — both tripwires are live.

### 9.4 Budget impact

+38 lines (≈176 → 214), far under the 500-line gate; M6 rides the existing table-driven runner (implementation delta: one scripted mode, one table row, two assertion-string fixes). Sweep/migration plan (§7) unchanged — the new test matches `-run 'TestGovernanceE2E'`.
