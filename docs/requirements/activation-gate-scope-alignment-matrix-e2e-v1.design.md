# Activation-Gate Scope-Alignment Matrix E2E — v1 Design

> Parent spec: `docs/auto/runs/activation-gate-scope-alignment-matrix-e2e-at-th-25e1ba30/artifacts/requirements-10762e10/requirements.md`
> Direction: **test-only**. No production code, no schema, no dependency changes.
> Target: new file `cmd/server/governance_e2e_test.go` (package `main`), ≤ 500 lines.

---

## 1. Evidence verification ledger (untrusted claims → verdicts)

Every citation in the parent spec was re-verified against the working tree (HEAD `15763e2`).
Two citations carry **line drift**; one substance claim is confirmed at different lines.
No claim was found false in substance.

| # | Claimed | Verdict | Verified location (this checkout) |
|---|---------|---------|-----------------------------------|
| E1 | `main.go:82-84` `WrapRepository` + `bus.WithRepository` inside `if auditRuntime != nil` (block `:79-85`) | ✅ exact | `cmd/server/main.go:79-85`; `:82` `auditgovernance.WrapRepository(repo, auditRuntime)`, `:83` `bus.WithRepository(repo)` |
| E2 | `file.go:308-324` `emit` → `sink.Publish` at `:324`; best-effort swallow; `noopSink{}` default | ✅ exact | `internal/service/file.go:308` `emit`, `:324` `s.sink.Publish(ctx, e)`; swallow comment `:306-307`; `noopSink{}` default `:134`, nil-guard `:139-141` |
| E3 | `authz_gate_test.go:65-100` `newAuthzServer` (unwrapped repo, sink-less svc); `:146` `outboxCount` counts `event_outbox`; zero `auditgovernance` refs in package | ✅ (approx) | `internal/api/s3compat/authz_gate_test.go:68-93`; `:144-146` counts `event_outbox`; `grep -c auditgovernance authz_gate_test.go` = 0 |
| E4 | `config_audit_governance.go:55` — `AUDIT_GOVERNANCE_ENABLED` default `false` | ✅ exact | `internal/config/config_audit_governance.go:55` |
| E5 | `token.go:64` `ClientCredentials(ctx, RequiredScope)`; `:152-153` `validTokenScopes` — constant, never literal | ✅ exact | `internal/auditgovernance/token.go:64`, `:152-153` |
| E6 | `audit_governance_claim.go:195` — `OldestPendingAuditGovernance` predicate `delivered_at_ns=0 AND failed_at_ns=0` | ✅ exact | `internal/repository/audit_governance_claim.go:195` |
| E7 | `relay.go:227-236` `isPermanentDeliveryError` = conflict/invalid-receipt/409/422; **200 transient-retry** | ✅ exact | `internal/auditgovernance/relay.go:228-237`; 200 absent from closed list → `retryFact` (`:132-135`) |
| E8 | `validateReceipt` requires 202 | ⚠️ **file drift** | `internal/auditgovernance/http.go:178-185` (not relay.go); non-202 → `&httpStatusError{Status}` → permanent only when 409/422 |
| E8b | capture=false double-gate at `repository.go:44-48` + `audit_governance_binding.go:139-151` | ⚠️ **line drift, substance ✅** | Gate 1 = `internal/auditgovernance/repository.go:23-26` (`RecordAudit`) / `:39-40` (`InsertEvent`): `if !r.runtime.Capture(tenant)` → plain path; `:44-47` is the **capture=true** branch. Gate 2 = `internal/repository/audit_governance_binding.go:139-151` `governanceCaptureActive` (`ErrNoRows → false`, `state == Active`), invoked from `audit_governance_write.go:40-48` / `:86-91` |
| E9 | `model.go:17` `RequiredScope`, `:19` `governancePath` | ✅ exact | `internal/auditgovernance/model.go:17` (`"audit:event:write"`), `:19` (`"api/v1/events"`) |
| E10 | fact IDs already `DeterministicFactID` (SHA-256, `audit_governance_factid.go:27-35`, applied `write.go:70-71`) | ⚠️ **line drift** | `internal/repository/audit_governance_factid.go:27-35` ✅; applied at `audit_governance_write.go:38-39` (`RecordAuditWithGovernance`), `:84-85` (`InsertEventWithGovernance`), `:126` (`EnqueueAuditGovernance`) — **not** `:70-71` (that region is the `object_events` INSERT) |
| E13 | T-4 sibling claim "facts.go still `uuid.NewString()`" is stale | ✅ confirmed stale | `internal/auditgovernance/facts.go:29-47` `factFromEvent` has no ID; ID assigned store-side via `DeterministicFactID`; gap path `facts.go:70-71` also recomputes deterministically |

**Additional facts verified and load-bearing for this design:**
- `Runtime.New(cfg, store, logger)` requires `cfg.Enabled`; **`applyDesiredBindings` runs at construction** (revision + binding states → `audit_governance_bindings`), and an unbound backlog **blocks startup** (`runtime.go:226-243`).
- Relay run loop (`runtime.go:185-203`): `reconcile → deliverBatch → cleanupDelivered`, `pollEvery` timer. `deliverBatch` → `ClaimAuditGovernance` (attempts+1, owner/token/lease; claim predicates `delivered=0 AND failed=0 AND available<=now AND lease<=now AND binding revision match`) → per-fact `deliverFact`: POST → permanent → `FailAuditGovernance` (`failed_at_ns`, terminal, retention-pruned); transient → `RetryAuditGovernance` (`available_at_ns = now+backoff`, `last_error` set); success → `CompleteAuditGovernance` (`delivered_at_ns`, claim cleared, `last_error=''`).
- `boundedBackoff(id, attempts, initial, max)` = `initial·2^(attempts-1)` ± 25 % jitter, clamped `[initial/2, max]` (`relay.go:181-197`).
- `Publisher` (http.go:97-130): POST `{BaseURL}/api/v1/events?wait_for=ledgered`, `Authorization: Bearer`, per-binding token via `remote.NewTokenClient`; **`newTokenSource` errors when `TokenURL` is empty** (token.go:33-43) → harness **must** serve a fake token endpoint.
- Receipt wire (`model.go:39-52`): `{"receipt":{"event_id","tenant_id","status","accepted_at","conflict","duplicate"}}`; acceptance = `status ∈ {ledgered,indexed,archived}` + non-zero `accepted_at` + `event_id`/`tenant_id` match; **tenant mismatch → `ErrInvalidReceipt` → permanent**.
- Fake token response has **no JSON tags** (`ssoclient.TokenResponse`): `{"AccessToken":"…","TokenType":"Bearer","ExpiresIn":3600}` decodes by default field matching.
- Outbox DDL: migration `0039` (`audit_governance_outbox`, `UNIQUE(origin_kind,origin_id)`), `0040` control, `0042` adds `failed_at_ns`, `0043` partial pending index. `event_outbox` (0041) is the **notifier** outbox — different table; never asserted here.
- `repository.Open` sets `journal_mode=WAL` + `foreign_keys=ON` (`sqlite.go:31`) → concurrent relay/test DB access is safe.
- Hard gate: `cli.py check-filesize` (≤ 500 lines/file) runs in `make check` and covers the new test file.

---

## 2. Scope & non-goals

**In scope:** one new test file; harness + REQ-1/REQ-2 activation gate (bound → exactly one outbox row + exactly one POST; unbound → zero rows); REQ-3 matrix M1–M5 (delivered / 409 / 422 / tenant-mismatch / transient-200) with exact per-cell outbox state; T-3 pins (202-only acceptance, 200 transient, permanent closed list) asserted through observed wire behavior; deterministic-fact-ID pin (E13) via recomputation.

**Non-goals:** no production change; no `audit_log`/admin-path coverage (RecordAudit already has unit coverage; the harness exercises the event path, which is the activation-gate surface); no Postgres variant (CI is SQLite per AGENTS §0); no REQ-4 source-grep test as a hard gate (see §4.5); no webhook/event_outbox notifier interaction (bus subscribers are not attached).

---

## 3. Harness design (`governance_e2e_test.go`)

### 3.1 Wiring (main.go order, `main.go:70-90`)

```
repo   = repository.Open(ctx, "sqlite", "file:"+tmp/"e2e.db")   // + Migrate
store  = storage.NewLocal(LocalConfig{Root: tmp/"objects"})
cfg    = e2eConfig(t, receiverURL)          // §3.3
rt     = auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)   // applies bindings
wrepo  = auditgovernance.WrapRepository(repo, rt)
bus    = events.New(wrepo, logger); bus.WithRepository(wrepo)
svc    = service.NewFileService(store, wrepo, logger).WithEventSink(bus)
rt.Start(ctx)                                // t.Cleanup: rt.Close() then repo.Close()
```

`governance_e2e_test.go` imports: `context`, `bytes`, `encoding/json`, `fmt`, `io`, `log/slog`, `net/http`, `net/http/httptest`, `path/filepath`, `regexp`, `strings`, `sync/atomic`, `testing`, `time`, `database/sql`, and `internal/{auditgovernance,config,events,repository,service,storage}`. Package `main` (sibling of `cmd/server/audit_governance_test.go`). Stdlib `testing` only (I6).

### 3.2 Fake receiver (one `httptest.Server`, two routes)

| Route | Behavior |
|---|---|
| `POST /token` | Always `200 application/json` `{"AccessToken":"e2e-token","TokenType":"Bearer","ExpiresIn":3600}` (field names exact — no JSON tags in `ssoclient.TokenResponse`). Counts calls (must be 1 for the whole test: token cached for TTL−TTL/5). |
| `POST /api/v1/events?wait_for=ledgered` | Scripted per POST-sequence index (from `httptest.Server` URL → one harness per matrix cell keeps scripts trivial). Decodes body → `event_id` (`model.go:61`), `tenant_id` (`payload`/`source_system` — tenant echoed from the **scripted** map: receiver derives `tenant_id` from the body if it can, else uses the script's override). Modes: `202-echo` (echo `event_id`, `tenant_id`, `accepted_at=now`, `status=ledgered`), `409`, `422`, `200-then-202` (first POST → `200`, subsequent → `202-echo`), `202-wrong-tenant`. Records `POSTs []event_id` (atomic). Content-Type of 202 responses: `application/json` (else the relay would legitimately classify `ErrInvalidReceipt`). |

Per-cell POST-count assertions use a `sync/atomic` counter; the harness exposes `postCount(eventID)` and `postCountTotal()`.

### 3.3 Config (passes `AuditGovernanceConfig.Validate`, `config_audit_governance.go:165-196`)

```go
cfg := config.AuditGovernanceConfig{
    Enabled: true,
    BaseURL: receiver.URL,  TokenURL: receiver.URL + "/token",   // loopback http allowed
    HMACKey: "0123456789abcdef0123456789abcdef",                  // 32 B, ≠ client secrets
    HTTPTimeoutSeconds: 5,  PollMilliseconds: 5,  BatchSize: 16,
    ClaimTTLSeconds: 30,    InitialBackoffSeconds: 1,  MaxBackoffSeconds: 2,
    MaxLagSeconds: 60,      ReconcileBatchSize: 8,
    DeliveredRetentionSeconds: 3600,  CleanupIntervalSeconds: 60,  CleanupBatchSize: 100,
    Revision: 1,
    Bindings: []config.AuditGovernanceBinding{{
        TenantID: "acme", ClientID: "e2e-client", ClientSecret: "e2e-secret-0000", State: "active",
    }},
}
```

Timing rationale: `pollEvery=5ms` → convergence ≪ 1 s; M5 second POST lands ≤ `1.25 s` (backoff 1 s ± 25 %, `relay.go:181-197`); all waits use `waitFor(t, cond, 10*time.Second)`.

### 3.4 Assertion helpers

- `outboxRow(t, dsn, originID) row` — raw `database/sql` over the SQLite DSN (precedent: `authz_gate_test.go:146` `outboxCount`):
  ```sql
  SELECT id,tenant_id,origin_kind,origin_id,fact_kind,action,attempts,
         available_at_ns,claim_owner,claim_token,lease_expires_at_ns,
         last_error,delivered_at_ns,failed_at_ns
  FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?
  ```
  (**I1**: SQLite `?` placeholder — never `$N` in harness SQL; row counted per `origin_id`, never globally, so async convergence can't bleed across cells.)
- `putObject(t, svc, tenant, key)` → `svc.Put(ctx, tenant, "default", key, strings.NewReader("x"), 1, PutOptions{})`; returns `repository.Object` (`.ID` = `objects.id` = event `origin_id`). **Synchronous invariant:** the outbox row is written before `Put` returns (bus.Publish → `InsertEventWithGovernance` runs in the request goroutine, `writePutObject` `file_crud.go:255` emits after repo write) — so "zero rows" assertions are race-free; only relay transitions need `waitFor`.
- `waitFor(t, cond func() bool, timeout)` — poll 5 ms; `t.Fatalf` on timeout with last observed row dump (diagnosability).
- `quiesce(t, d, cond)` — assert no further change for `d` (10 × pollEvery = 50 ms) to pin "exactly one POST".
- `wantFactID(t, dsn, eventRowID, occurredAt)` — recompute `repository.DeterministicFactID(sourceID, "acme", "file.created", "file", eventRowID, occurredAt)` and compare to outbox `id` (E13/T-4 pin). `sourceID` = `"aero."+base64url(hmac-sha256(HMACKey, redactionDomain, tenant, "source-system", tenant))` per `redaction.go:43-49` — the harness holds `HMACKey` in cfg so the formula is reproducible; `occurredAt` read back from the `object_events` row (DB-default ns) so the frame matches the atomic-path inputs (`audit_governance_write.go:80-85`). Assert `id` also matches `^[0-9a-f]{32}$` (SHA-256 hex prefix, `audit_governance_factid.go:34-35`).

### 3.5 Test inventory

| Test | REQ | Script | Assertions (exact) |
|---|---|---|---|
| `TestGovernanceE2EActivationGateBoundTenant` | REQ-1 | `202-echo` | PUT → row count=1 for origin (`tenant_id=acme`, `origin_kind='file'`, `attempts=0`, `delivered=0`, `failed=0`); `object_events` row exists; `waitFor`: POSTs=1 **and** `delivered_at_ns>0`, `attempts=1`, `claim_owner=''`, `last_error=''`; `quiesce` 50 ms: POSTs stays 1; fact ID matches `DeterministicFactID` recomputation |
| `TestGovernanceE2EActivationGateUnboundTenant` | REQ-2 | `202-echo` (unused) | PUT to `tenant="other"` → `outboxRow` = `sql.ErrNoRows` for that origin; `object_events` row **exists** (plain `InsertEvent` path still persists the event — pins gate-1 fallthrough `repository.go:39-40`); `waitFor` 1 s: `postCountTotal()==0` |
| `TestGovernanceE2EMatrixDelivered` | REQ-3 M1 | `202-echo` | `delivered_at_ns>0 ∧ failed_at_ns=0`, `attempts=1`, `last_error=''`; POST body `event_id == outbox.id`, `Authorization: Bearer e2e-token` present |
| `TestGovernanceE2EMatrixConflict409` | REQ-3 M2 | `409` | `waitFor`: `failed_at_ns>0 ∧ delivered_at_ns=0`, `attempts=1`, `last_error` contains `"409"`; `quiesce`: POSTs stays 1 (no retry — permanent closed list `relay.go:228-237`) |
| `TestGovernanceE2EMatrixUnprocessable422` | REQ-3 M3 | `422` | same shape; `last_error` contains `"422"` |
| `TestGovernanceE2EMatrixTenantMismatch` | REQ-3 M4 | `202-wrong-tenant` | receipt `tenant_id="mallory"` ≠ fact tenant → `ErrInvalidReceipt` (http.go:214-219) → `failed_at_ns>0`, `attempts=1`, `last_error` contains `"invalid receipt"` |
| `TestGovernanceE2EMatrixTransient200` | REQ-3 M5 | `200-then-202` | `waitFor`: `attempts>=1 ∧ delivered=0 ∧ failed=0`, `last_error` non-empty, `available_at_ns > now` (backoff scheduled, 200 ∉ permanent list); `waitFor`: delivered with `attempts=2`, `last_error=''` — transient-then-success, no retry storm |

### 3.6 REQ-4 (grep-consistency) — disposition

Parent spec flags REQ-4 as **proposed / no precedent**. Decision: **not** a separate source-grep test (brittle: test files legitimately embed literals; no precedent in-tree). Instead the two pins are asserted **behaviorally**:
- `governancePath` pin: the receiver asserts it observes `POST {BaseURL}/api/v1/events?wait_for=ledgered` (path+query match) on the first POST — drift in `model.go:19` breaks the e2e.
- Scope pin: `validTokenScopes` requires `scopes == ["audit:event:write"]` when the token has scopes — the fake token returns no scopes, so add one optional harness assertion: token request includes `scope=audit:event:write` in the form body (token.go:64 passes `RequiredScope` to `ClientCredentials`) — assert when the SDK sends it; do not fail if the SDK omits empty-scope params. Recorded as accepted-risk with the spec's "proposed" flag.

---

## 4. API changes

- **Production API:** none. No handler, no interface, no config key, no migration.
- **Test API (new file `cmd/server/governance_e2e_test.go`, package `main`):** helpers `newGovernanceE2E(t, script)` (returns `{svc, dsn, receiver, rt}`), `outboxRow`, `waitFor`, `quiesce`, `postCount`, `putObject`, `wantFactID`. None exported; no new packages; `go.mod` untouched (I6).

## 5. Compatibility constraints

- **I4 (wiring order):** harness reproduces `main.go:79-89` exactly: `WrapRepository` → `bus.WithRepository` → `NewFileService(...).WithEventSink(bus)`. Any reordering silently produces zero rows and the activation-gate tests fail — that is the point.
- **I5 (opt-in safety):** harness constructs components directly; no env vars, no production config path (`buildAuditGovernanceRuntime` deliberately **not** used — it reads env + bindings file). CI baseline unchanged: SQLite + local FS + loopback httptest (precedent `authz_gate_test.go`).
- **I1:** harness SQL uses `?` only (SQLite); timestamps compared as `int64` ns columns, never parsed strings.
- **I2:** zero migrations. `repository.Open` + `Migrate` on a temp dir gives the full 0043 schema.
- **I6:** stdlib only; no testify, no new deps.
- **Gate:** file ≤ 500 lines (`cli.py check-filesize`); 6 tests + harness must fit — keep `putObject`/`waitFor`/`quiesce` minimal, one shared `newGovernanceE2E`.
- Determinism: no sleeps beyond `waitFor`; per-origin row counting; `time.Now` only for `available_at_ns > now` comparisons (monotonic-safe direction: assert strictly greater, tolerate ±2 s skew).
- Token endpoint: single fetch per harness (`ExpiresIn=3600`, skew = TTL/5); assert `tokenCalls==1` only at the end (no hard timing dependency).

## 6. Failure modes & mitigations

| Mode | Symptom | Mitigation |
|---|---|---|
| Relay claims row before assertion | `attempts>1` in M1/M2 cell | All terminal-state assertions go through `waitFor` on the *expected* terminal predicate; intermediate `attempts` only asserted with `>=` where jitter applies (M5) |
| M5 backoff jitter | second POST at `[0.5,1.25] s` | `waitFor` 10 s; never assert a fixed delay; assert `available_at_ns > now` and `attempts==2` after delivery |
| SQLite concurrency (relay goroutine + test) | `database is locked` | WAL already set by `repository.Open` (`sqlite.go:31`); keep test writes single-goroutine (PUT before `waitFor`); raw read DSN via `database/sql` with default busy handling |
| Fake receiver 202 without `application/json` | spurious `ErrInvalidReceipt` → M1 fails as if M4 | Content-Type set explicitly in fake; M4 test deliberately sends valid JSON but wrong `tenant_id` so the two cells differ only by tenant |
| `rt.Close` ordering | use-after-close / drain hang | `t.Cleanup` registers `rt.Close()` before `repo.Close()`; `Close` bounded by `claimTTL+httpTimeout` (`runtime.go:122-134`) |
| Unbound-backlog startup block | `applyDesiredBindings` error if rows exist pre-binding | Harness applies bindings at `auditgovernance.New` (construction) **before** any PUT — no backlog can exist; T2 writes only to an unbound tenant so no backlog is ever created |
| Token shape drift (`TokenResponse` untagged) | `ErrTokenUnavailable` → retries, M1 timeout | Fake uses exact field names `AccessToken/TokenType/ExpiresIn`; failure surfaces as a clear `waitFor` timeout with `last_error` dump |
| POST-count "exactly one" flake | relay double-delivery after ack-lost | `quiesce(50 ms)` = 10 × pollEvery; ack-lost would re-claim only after `lease_expires_at_ns` (30 s) — far outside the window, so the assertion is sound |

## 7. Migration steps

1. Land this design doc; open the implementation PR containing **only** `cmd/server/governance_e2e_test.go`.
2. Implement harness + 7 tests per §3; `gofmt`; `go vet ./cmd/server/`.
3. `make check` (hard gate: gofmt/build/vet/test + filesize ≤ 500).
4. `go test ./cmd/server/ -run 'TestGovernanceE2E' -count=1 -v` ×3 locally (flake sweep); `make test-race` (relay goroutine × test goroutine).
5. No schema/rollback/ops steps — zero production footprint. Rollback of the test direction = delete the file.

## 8. Acceptance mapping (REQ → test → gate)

| Requirement | Test | Acceptance |
|---|---|---|
| REQ-1 (item 6: bound tenant, first-PUT → exactly one outbox row + one POST) | `TestGovernanceE2EActivationGateBoundTenant` | outbox row count=1 for the origin, POST count=1 after quiesce, delivered state terminal |
| REQ-2 (unbound tenant → zero rows) | `TestGovernanceE2EActivationGateUnboundTenant` | `sql.ErrNoRows` on the origin query; total POSTs=0; `object_events` row still present (gate-1 fallthrough) |
| REQ-3 (matrix M1–M5 exact outbox state per cell) | 5 `TestGovernanceE2EMatrix*` tests | per-cell column predicates as in §3.5 table; M2/M3/M4 `failed_at_ns>0`; M5 transient→delivered with `attempts==2` |
| REQ-5 (T-3 pins: 202-only acceptance; 200 transient; permanent closed list) | all matrix tests | M1 succeeds only via 202+valid receipt; M5 proves 200 ≠ permanent; M2/M3/M4 prove 409/422/invalid-receipt = permanent (single POST each, no retry) |
| REQ-4 (grep-consistency, proposed) | behavioral pins in fake receiver | observed POST path+query = `api/v1/events?wait_for=ledgered`; token scope param pin optional, documented accepted-risk |
| E13/T-4 identity pin (no production change) | `wantFactID` in M1 | outbox `id` = recomputed `DeterministicFactID` frame from the stored `object_events` id + `occurred_at_ns` |
| Three supplied acceptance checks (verbatim-intent) | harness invariants | ① exactly one outbox row + one POST for first PUT to a bound tenant; ② zero rows for unbound tenant; ③ matrix cells reach exactly the scripted terminal state with no retry beyond the classified class |
