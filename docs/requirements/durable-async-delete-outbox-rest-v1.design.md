# Design: Transactional outbox (durable_async) for `vault.file.deleted@1.1` audit + `vault.file.notify@1.1` notification (REST delete path)

> **Companion spec:** `docs/requirements/durable-async-delete-outbox-rest-v1.md` (FR-1…FR-4, AC-1…AC-4) · **Module:** `internal/api/rest` (composition surface: `internal/service` + `internal/repository` + `internal/events` + `cmd/server`) · **Status:** design + implementation (machinery landed round-1/round-2; G1/G2 landed in WIP — both green this session; §6.1 is the spec of record) · **Baseline:** HEAD `acfaaf4` + in-flight quarantine WIP (excluded, §8) · **Gates:** `make check` green · stdlib only (I6) · I1/I2 discipline (**zero DB migrations** — `0041_event_outbox` exists) · **Zero wire-level API changes**

---

## 1. Evidence re-verification (independent check against HEAD)

All 8 direction citations and all supplementary facts were re-checked against the working tree. **All hold.** Line nits (immaterial): E1 `s.emit` sits at :53/:92 not :51/:90, and the `DeleteVersion` emit is :212 (evidence's :161 is the function start); E3 `Publish` body is :76-104; E6 `CompleteAuditGovernance`/`RetryAuditGovernance`/`requireGovernanceClaim` sit at :124/:135/:150 (not :102-110/:112-125/:127-136); E7 `reconcile` is :16, with `deliverBatch` :59/`deliverFact` :80/`retryFact` :97/`boundedBackoff` :130 (evidence's :33-69 straddles `reconcile` tail + `deliverBatch`).

| # | Claim | Verified location | Verdict |
|---|-------|-------------------|---------|
| E1 | `s.emit` after delete; `deleteAuditEntry` + `deleteFacts` commit **in the same tx** via `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`; `s.emit` is local-broadcast only | `internal/service/file_delete.go` — WithEvent calls :46/:86 (hard/soft), `deleteAuditEntry` :100-113, `deleteFacts` :123-137, `s.emit(EventDeleted)` :53/:92 (after commit), `DeleteVersion` :161-214 emit :212 (**no** WithEvent — E14 bus-only path) | ✅ |
| E2 | `file.go:297` minimal emit payload, sink errors swallowed | `internal/service/file.go:297-314` — payload `{backend,size,etag,content_type}`, comment "best-effort and must never break a user request" | ✅ exact |
| E3 | `bus.go:80-104` `Publish` = `InsertEvent` → broadcast → transport; errors swallowed | `internal/events/bus.go:76-104` — `InsertEvent` :84, warn-and-return :86-88, transport warn :101-103; non-atomic, non-blocking | ✅ (drift ±4) |
| E4 | `admin.go:410-428` best-effort admin audit, `_ = h.repo.RecordAudit` swallowed | `internal/api/rest/admin.go` — `audit` :410-413, `auditForTenant` :414-428, swallow :421 | ✅ exact |
| E5 | tx'd audit+outbox precedent (`RecordAuditWithGovernance`/`InsertEventWithGovernance`) | `internal/repository/audit_governance_write.go:14-43` / :45-79 — BeginTx → INSERT → insertAuditGovernance → Commit | ✅ |
| E6 | claim/attempts/lease fencing precedent | `internal/repository/audit_governance_claim.go` — `ClaimAuditGovernance` :16, `CompleteAuditGovernance` :124, `RetryAuditGovernance` :135, `requireGovernanceClaim` :150 | ✅ (drift ≤25) |
| E7 | governance relay reconcile/deliver/backoff shape | `internal/auditgovernance/relay.go` — `reconcile` :16, `deliverBatch` :59, `deliverFact` :80, `retryFact` :97, `boundedBackoff` :130-145 (2× exp, cap; deterministic SHA-256-hash jitter ±25% — distinct from the events-package **downward-only** jitter, D-7) | ✅ (drift) |
| E8 | `billing_outbox.go` status-shape claim predicate | `internal/repository/billing_outbox.go:8-25` — `ClaimBillingUsage`; status shape `(pending AND next_attempt<=now) OR (inflight AND claim_until<=now)` | ✅ exact |

**Material findings (all verified):**

| Fact | Verified |
|------|----------|
| `event_outbox.go` fully landed: `EventTypeFileDeleted11` :22 / `EventTypeFileNotify11` :25, `validateOutboxFacts` :61 (allowed-set + schema_version 1.1 + ≤1 MiB), `HardDeleteObjectWithEvent` :102, `SoftDeleteObjectWithEvent` :147, `SoftDeleteObjectByIDWithEvent` :186 (WIP, sibling campaign), `insertOutboxFacts` :229, `ClaimEventOutbox` :251 (status shape, E8), `CompleteEventOutbox` :336 (same-tx `event_outbox_delivered`), `RetryEventOutbox` :364 (attempts≥max → `failed`), `PruneEventOutbox` :393, `HasEventOutboxFact` :437 | ✅ |
| Migration `0041_event_outbox.{up,down}.sql` exists in **both** sqlite + postgres | ✅ |
| `payload.go`: `BuildDeletedFact` :109 / `BuildNotifyFact` :137 (variadic `reason`/`signature` = **WIP**, omitempty; REST goldens unchanged); `newSequencer` crypto/rand 16B hex; goldens byte-pinned in `schema_test.go:31-132` (4 tests) | ✅ |
| **`occurred_at` absent from envelope** — only in sibling `audit_governance_*`/`billing_usage` tables (`grep` full `internal/events` + `event_outbox*.go`: zero hits; `audit_governance_claim.go:14`, `billing_usage.go:13`); event-outbox timing = row columns `created_at_ns`/`available_at_ns`/`delivered_at_ns` | ✅ |
| Relay always starts (`cmd/server/workers.go` `startEventOutboxRelay` :158-180; comment "always starts: deletion atomicity is not gated"); defaults poll 1000 ms / batch 32 / TTL 30 s / HTTP 5 s / attempts 10; `Validate` enforces `ClaimTTL > 2×HTTP_TIMEOUT` (`config_event_outbox.go:67`) | ✅ |
| `deliverNotify` :236-270 re-resolves bucket notification rules **at delivery time** from payload metadata `{tenant,bucket,key}`, delivers payload **verbatim** (`deliverPayload`), completes | ✅ |
| Notifier D2 dedupe — skip bus when `notify@1.1` outbox row exists (any status; no race — WithEvent commits before `s.emit`); E14 paths (DeleteVersion/delete-marker/quarantine) keep bus path (`notifier.go:70-87`) | ✅ |
| AC-1/AC-2/AC-3 tests all present: `event_outbox_test.go` (`TestDeleteObjectWithAudit_OneTx` :71, `TestDeleteObjectWithEvent_OneTx` :136, `TestEventOutboxClaimCompleteLifecycle` :220, `TestEventOutboxClaimLeaseExpiryRedelivers` :259, `TestEventOutboxRetryBackoffAndTerminalFailed` :300), `relay_test.go` (`TestOutboxRelay_DeliveryLifecycle` :144, `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` :229, `TestEventOutboxBackoffBounds` :298) | ✅ |
| AC-4 partial: `TestComposition_AuditSinkL2BoundTenant` :788 + `TestDeleteResponse_DoesNotBlockOnDelivery` :685 (`fullserver_test.go`); **G1/G2 implemented in WIP**: `TestComposition_DeleteDeliversBothFacts` :876, `TestComposition_MidClaimRestartRedeliversOnce` :1015 + harness bus/notifier wiring :86-92 + helpers `outboxPayload` :1258 / `dumpBodies` :1171 / `setDeleteRule` :1245 / `sequencerHexRe` :1275 / `assertNotifyContent` :1281 | ✅ green this session (G1 5.4s / G2 2.5s; previously the two genuine gaps) |
| Tests green today: `go test ./internal/repository/ -run 'TestDeleteObjectWithAudit_OneTx|TestDeleteObjectWithEvent_OneTx|TestEventOutbox'` ok 2.0s; `./internal/events -run 'TestOutboxRelay|TestEventSchema'` ok 6.9s; integration `TestDeleteResponse_DoesNotBlockOnDelivery` + `TestComposition_AuditSinkL2BoundTenant` ok 2.132s (spec claimed 2.146s) | ✅ |

---

## 2. API changes

### 2.1 Wire-level (protocol/config) — **none**

No REST/S3/MCP/WebDAV routes, no OpenAPI diff, no SSE frames, no env vars, no flags. `DELETE /v1/files/{key}[?hard=1]` response contract unchanged (204; 404/409 per bucket-policy/protection). Payload `schema_version` stays `"1.1"`. The outbox facts are **internal persistence + relay delivery**; nothing about the REST surface changes.

### 2.2 Go-level (complete breakage surface)

**Production behavior: zero changes — already landed** (round-1 `fb74b19` / round-2 `4cca6db`); the only production-file touch is a comment-only jitter-range doc fix (D-7):

- `internal/service/file_delete.go` — `deleteAuditEntry`/`deleteFacts` built pre-tx, committed via `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`.
- `internal/repository/event_outbox.go` — full outbox (claim/complete/retry/prune/has-fact) + `repository_interface.go` additions.
- `internal/events/payload.go` + `event_outbox_relay.go` — self-contained @1.1 builders + always-on relay.
- `cmd/server/workers.go` — relay assembly.

**New code for this design = the G1/G2 integration tests** in `internal/integration/fullserver_test.go` (spec §6 G1/G2; skeletons in §6.1) **+ two test-only harness/aux deltas**: (a) the harness now wires the production bus+notifier shape (`startFullServerWithRelay` :55, §6.1 shared-fixture delta); (b) a comment-only jitter-range fix + `TestEventOutboxBackoffBounds` bounds tightening (D-7). Test-only additions cannot break the production surface; the shared fixtures: `startFullServerWithRelay` :55, `outboxStatus` :1226, `outboxPayload` :1258, `waitForBodies` :1336, `assertAuditRowFor` :1319, `setDeleteRule` :1245 (integration mirror of the `relay_test.go:108` shape), `sequencerHexRe` :1275, `assertNotifyContent` :1281, `dumpBodies` :1171.

The in-flight WIP diff (`payload.go` variadic `reason`/`signature`; `SoftDeleteObjectByIDWithEvent` :186; `object_worker.go` `QuarantineObjectByID`) belongs to the sibling quarantine campaign — its **additive** builder signatures are backward-compatible with this design's call sites (variadic, empty default) and its goldens are pinned to stay byte-identical (AC-3 relies on this).

---

## 3. Compatibility constraints

| # | Constraint | Mechanism |
|---|-----------|-----------|
| C-1 | **REST wire contract unchanged (hard):** `DELETE /v1/files/{key}` status codes, `?hard=1` semantics, bucket-policy check order (`handler.go:239-250`) identical pre/post design | No handler edits (this design adds tests only) |
| C-2 | **Legacy `object_events` stream semantics unchanged:** `s.emit` still runs after commit → SSE `/events/stream` replay, indexer, webhook, replication subscribers consume exactly as today | `bus.go` untouched; outbox is additive persistence |
| C-3 | **Notify dedupe contract:** a delete that committed WithEvent must produce **exactly one** notify@1.1 delivery (relay), and the bus path must skip it (D2 `HasEventOutboxFact`); E14 paths (DeleteVersion/delete-marker) have no outbox row → bus path preserved, never silently dropped | `notifier.go:70-87` skip + relay delivery |
| C-4 | **L0 audit always-on:** `audit_log` row is written per-tenant regardless of L2 binding; no L2 → delete still 2xx, relay completes `deleted@1.1` without delivery (record-keeping degradation, not failure) | `deliverDeleted` nil-sink complete path |
| C-5 | **At-least-once window explicit:** deliver→complete is crash-recoverable; a crash mid-window redelivers (S3-equivalent semantics). Exactly-once holds **after** `complete` (`event_outbox_delivered` same-tx insert) | claim lease + complete tx |
| C-6 | **Multi-instance safety:** concurrent relays fence via owner+token; `EVENT_OUTBOX_CLAIM_TTL_SECONDS > 2×HTTP_TIMEOUT` validated at config load — no duplicate concurrent POST in a correctly configured deployment. **Programmatic relay opts bypass `config.Validate`** (`NewEventOutboxRelay` default-fills without the invariant check) — tests constructing relays directly must self-honor it (G2: ClaimTTL=30s / HTTPTimeout=5s) | `config_event_outbox.go:67` |
| C-7 | **No new migrations/deps:** `0041_event_outbox` exists in both dialects; stdlib only (I6); new SQL already uses `rebind` (I1) | — |
| C-8 | **`occurred_at` stays out of the envelope:** timing semantics remain row-column based (`created_at_ns`/`available_at_ns` at insert, `delivered_at_ns` at complete). Adding the field = schema evolution, explicitly out of scope | `event_outbox.go:244-248,340-353` |
| C-9 | **Golden bytes stable:** `schema_test.go` goldens must pass unmodified; WIP additive fields are `omitempty` and struct-ordered after existing fields | `payload.go` struct order + variadic builders |

---

## 4. Failure modes

| # | Trigger | Observable | Behavior | Design response |
|---|---------|-----------|----------|-----------------|
| FM-1 | DB error mid-delete-tx (disconnect/lock) | REST 5xx; object row, audit, facts all rolled back | all-or-nothing (AC-1a) | No new code; asserted by `TestDeleteObjectWithAudit_OneTx` rollback branch |
| FM-2 | `validateOutboxFacts` failure (programming error: >1 MiB payload, schema ≠ 1.1, unknown type) | tx rolled back; object **not** deleted | Guard, not expected path (constants-only builders) | `event_outbox_test.go:71-134` forced-rollback assertion |
| FM-3 | Relay crashes mid-claim | rows stuck `inflight` with lease; after `lease_expires_at_ns` any instance re-claims | **redelivery, no double-schedule** (claim fencing + `requireEventOutboxClaim` on complete/retry) | `TestEventOutboxClaimLeaseExpiryRedelivers` + `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` |
| FM-4 | Delivery target down (5xx/network) | durable retry: attempts++, backoff `available_at_ns` (2×, cap 5 min, **downward-only jitter [0.75, 1.0)×base** — D-7), `attempts>=10` → terminal `failed` + `last_error` | relay machinery; alert via relay telemetry | `TestEventOutboxRetryBackoffAndTerminalFailed` / `TestEventOutboxBackoffBounds` (bounds pinned to [0.75, 1.0)×base, D-7) |
| FM-5 | L2 returns 401/403 | `deleted@1.1` fails **immediately** (no retry loop) — authz is not transient | `failImmediately` :216 | `TestOutboxRelay_L2UnauthorizedFailsImmediately` |
| FM-6 | Notify rules lookup error (`GetBucketNotifications` fails) | retry with backoff; payload intact | `deliverNotify` :236-270 | covered by FM-4 tests |
| FM-7 | No L2 + no notify rules | both facts complete without network; rows pruned by `PruneEventOutbox` | silent no-op (design intent); **tests must insert the rule BEFORE the delete** — a late insert = silent complete = 0 POSTs (G1 ①, guarded by `len==1` before any body read) | `TestOutboxRelay_DeletedFactCompletesWithoutDelivery` + G1 ① |
| FM-8 | `ChunkCleaner` failure on delete | warn log only; delete proceeds | AGENTS.md §2.1 ③ | unchanged |
| FM-9 | Process restart with pending rows | new instance reclaims (pending or expired-inflight) and delivers; complete rows never re-deliver | G2 e2e proves it end-to-end | **new test** §6.1 G2 |
| FM-10 | WIP quarantine regression risk | none for REST path: builders variadic, default empty → REST goldens byte-identical | sibling campaign's responsibility | `schema_test.go` zero edits (C-9) |

---

## 5. Migration steps

1. **DB:** none — `0041_event_outbox` (+ `event_outbox_delivered`) already exists in both dialects; `origin_id` has no FK and accepts any `objects.id` (I2: no new files, no edits to applied ones).
2. **Config/env:** none — relay always starts (`workers.go` comment + :158-180); defaults poll 1 s / batch 32 / TTL 30 s / attempts 10; existing `EVENT_OUTBOX_*` vars already documented in `docs/configuration.md`.
3. **Deploy:** the machinery shipped with round-1/round-2; **this design's only delta is test code** (G1/G2), so rollout = `make check` green + existing release process. No behavioral activation step.
4. **No backfill:** deletes that happened before the WithEvent path shipped are not retro-audited/delivered (documented limitation; reconcile does not touch `event_outbox`).
5. **Rollback:** revert the round-1/round-2 commits (or the test-only delta — trivial). Pending rows drain harmlessly via the always-on relay; delivered rows are append-only history; `event_outbox_delivered` rows are inert. No data cleanup required.
6. **Ops notes:** with bucket-notification rules, REST deletes now produce relay-delivered notify@1.1 (durable, ≤ poll-interval latency) instead of in-process Notifier dispatch — same destination, at-least-once semantics; D2 skip prevents double-delivery. Without rules: zero network, complete + prune.

---

## 6. Testable acceptance mapping

Test packages: `internal/repository`, `internal/events`, `internal/integration`; assertions via `testing` only (I6). `make check` gates all. Status: AC-1…AC-3 + AC-4(deleted@1.1) green (verified §1); **G1/G2 implemented in WIP and green this session** (§6.1).

| AC | Test (name + file) | Assertions | Status |
|----|--------------------|-----------|--------|
| **AC-1a** (in-tx atomic) | `TestDeleteObjectWithAudit_OneTx` — `internal/repository/event_outbox_test.go:71`; `TestDeleteObjectWithEvent_OneTx` :136 | hard/soft delete → object gone, exactly 2 outbox rows (deleted@1.1+notify@1.1), exactly 1 `audit_log` row; forced validation failure → error, object row intact, 0 outbox, no audit | ✅ green |
| **AC-1b** (durable_async, never-waits) | `TestDeleteResponse_DoesNotBlockOnDelivery` — `internal/integration/fullserver_test.go:685` | L2 target blocked (4 s hang) → REST DELETE returns 204 while outbox row is `pending\|inflight` (`delivered` unreachable), `audit_log` present; after recovery ≤15 s → `delivered` + ≥1 POST | ✅ green (0.28 s) |
| **AC-2** (claim/lease/retry/dedup) | `TestEventOutboxClaimCompleteLifecycle` :220 · `TestEventOutboxClaimLeaseExpiryRedelivers` :259 · `TestEventOutboxRetryBackoffAndTerminalFailed` :300 · `TestOutboxRelay_DeliveryLifecycle` (`relay_test.go:144`, byte-exact + exactly-once-after-complete) · `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` :229 · `TestEventOutboxBackoffBounds` :298 (bounds pinned to the actual [0.75, 1.0)×base, D-7) | claim sets owner+token+attempts+lease; expiry → re-claim by other owner; 5xx → backoff bounds → terminal `failed`; lost claim → reclaim not double-schedule | ✅ green |
| **AC-3** (schema) | `TestEventSchema_GoldenJSON` :31 · `TestEventSchema_RequiredFields` :42 · `TestEventSchema_Deleted11Envelope` :96 · `TestEventSchema_SequencerUniquePerCall` :132 — `internal/events/schema_test.go` | golden bytes for both envelopes; required fields incl. `object_id`, no `records` on deleted@1.1, self-contained `records[0].s3.object.{key,size,eTag,versionId,sequencer}` on notify@1.1; sequencer unique per call (restore→re-delete safe); `occurred_at` asserted as row-column timing (`created_at_ns`/`available_at_ns`/`delivered_at_ns`), not envelope field | ✅ green |
| **AC-4a** (deleted@1.1 e2e) | `TestComposition_AuditSinkL2BoundTenant` — `fullserver_test.go:788` | REST PUT→DELETE (t1) → L2 receives exactly 1 POST with `event_type/tenant/object_id`; `audit_log` t1 row; unbound tenant t2 → 0 POSTs but audit intact; no-L2 server → 2xx + relay completes | ✅ green (1.84 s) |
| **AC-4b = G1** (notify@1.1 e2e) | `TestComposition_DeleteDeliversBothFacts` — `fullserver_test.go:876` | §6.1 (implemented, all review deltas) | ✅ green (5.4s) |
| **AC-4c = G2** (mid-claim restart e2e) | `TestComposition_MidClaimRestartRedeliversOnce` — `fullserver_test.go:1015` | §6.1 (implemented, all review deltas) | ✅ green (2.5s) |

### 6.1 New test specifications (the only implementation work)

**Both tests are implemented in the WIP and green** (this session: G1 5.4s, G2 2.5s — `go test ./internal/integration/ -run 'TestComposition_DeleteDeliversBothFacts|TestComposition_MidClaimRestartRedeliversOnce' -count=1`). This section is the spec of record; the code at the cited lines is authoritative and incorporates every review delta below.

**Shared fixture delta — production bus+notifier shape in the harness** (`startFullServerWithRelay` :55): the harness now wires `bus := events.New(repo, logger)` → `svc.WithEventSink(bus)` → `notif := events.NewNotifier(repo, logger)` → `sub, _ := bus.Subscribe()` → `go notif.Run(notifCtx, sub)` (+ `notifCancel`/`bus.Close` cleanups, LIFO: bus.Close → notifCancel → relayCancel → ts.Close → repo.Close) — mirroring `cmd/server/workers.go:141-147`. Without this, `s.emit` is a no-op (`noopSink`) and the D2 dedupe skip (`notifier.go:70-87`) is dead code in tests. Existing tests are unaffected: with no notification rules `GetBucketNotifications` is empty and the notifier early-returns (`notifier.go:65-68`). A D2 regression now puts the legacy `buildS3Event` body on the notify target alongside the relay's @1.1 envelope → `len(bodies)==2` fails G1 loudly (the C-3 claim is actually tested e2e).

**G1 — REST DELETE → both facts delivered, byte-exact, exactly-once** (`TestComposition_DeleteDeliversBothFacts` :876):

```go
// internal/integration/fullserver_test.go
func TestComposition_DeleteDeliversBothFacts(t *testing.T) {
    // 1) startFullServerWithRelay(relayOpts{PollInterval:50ms, BatchSize:32,
    //    ClaimTTL:30s, HTTPTimeout:5s, MaxAttempts:10, AuditSink}) → L2 target
    //    (200+echo, AC-4a shape :788) + notify target (2nd httptest server, 200).
    //    setDeleteRule (:1245) MUST run BEFORE the DELETE (① FM-7 trap:
    //    deliverNotify with zero matching rules completes silently → 0 POSTs;
    //    the len==1 guard below then fails loudly, not passes)
    // 2) PUT /v1/files/k (tenant default) → DELETE /v1/files/k?hard=1 → 204
    // 3) poll outboxStatus(dsn, originID, "vault.file.deleted@1.1") == "delivered" &&
    //    outboxStatus(..., "vault.file.notify@1.1") == "delivered" (≤15 s, 50ms steps)
    // 4) ABSOLUTE ==1 with guard BEFORE any body read: at the delivered moment
    //    (a POST completes before its fact completes → counters are settled;
    //    a D2 bus-path duplicate lands within ms — pre-snapshot duplicates
    //    cannot hide), n := len(bodies); copy bodies[0] ONLY if n==1; n!=1 →
    //    t.Fatalf (0-body read would panic; 2-body read must Fatal, never
    //    compare bodies[0]); then bytes.Equal(bodies[0], rowPayload) where
    //    rowPayload = outboxPayload(dsn, originID, "vault.file.notify@1.1") —
    //    ORIGIN_ID-SCOPED SELECT (:1258, outboxStatus shape :1226; relay POSTs
    //    this column verbatim)
    // 5) content pins = assertNotifyContent (:1281, after the byte-equal —
    //    closes the row↔ground-truth mirror gap: a wrong sequencer/key in
    //    BuildNotifyFact would relay faithfully and still pass byte-equal):
    //    schema_version=="1.1", event_type=="vault.file.notify@1.1",
    //    tenant=="default", bucket=="default", key=="k",
    //    records[0].eventName=="s3:ObjectRemoved:Delete",
    //    records[0].s3.object.key=="k", records[0].s3.object.sequencer matches
    //    sequencerHexRe ^[0-9a-f]{32}$ (:1275; newSequencer, payload.go:17-29)
    // 6) L2 exactly 1 deleted@1.1 POST (len==1 guard + AC-4a strings.Contains
    //    pins: event_type / tenant / object_id)
    // 7) FIXED 5s no-dup window (≥5×PollInterval — not 2×PollInterval, and not
    //    relative "counters unchanged": the bus path delivers within ms,
    //    possibly before the first snapshot): after 5s, notify len(bodies)==1 &&
    //    L2 len==1 && both facts still "delivered" via outboxStatus (state
    //    witness — immune to counter timing)
}
```

Fixture notes: the harness already exposes DSN + repo (`*fullServerHarness`); `outboxPayload` :1258 mirrors `outboxStatus`'s raw-connection shape (I1 `rebind`-free, literals only). Notify rule insertion uses `setDeleteRule` :1245 (same repository handle). The harness bus wiring is unconditional (no rule-gating) — see the shared-fixture delta above.

**G2 — process-level crash/restart redelivery, exactly once** (`TestComposition_MidClaimRestartRedeliversOnce` :1341):

```go
// internal/integration/fullserver_test.go
func TestComposition_MidClaimRestartRedeliversOnce(t *testing.T) {
    // 1) Server A = startFullServerWithRelay(t, nil) — relayOpts==nil → the
    //    harness starts NO relay goroutine (guard :137). "Facts stay pending"
    //    holds by construction — the NewTimer(0) immediate-round race is gone,
    //    and "stop server A" is EXACT: closing h.repo is a complete stop (no
    //    dormant hour-poll relay touching the closed DB; the hidden relayCancel
    //    is moot). Notify rule → target returning 500 with FLIP-ON-FIRST-500
    //    (count-based inside the handler under mutex — never a wall-clock
    //    sleep; a wall-clock flip would race attempt 2's dispatch at [0.8,1.1]s).
    //    DELETE /v1/files/k?hard=1 → 204 (both facts committed on DSN file D)
    // 2) A-phase: both facts strictly "pending" (outboxStatus on h.dsn — no
    //    relay → deterministic), then 300ms settle with ZERO POSTs on both
    //    targets (the D2 skip is exercised live: row exists → notifier skips)
    // 3) crash: h.repo.Close(); keep D on disk. B MUST reuse h.dsn — the
    //    harness builds a fresh t.TempDir() per call; a second t.TempDir()
    //    would be a different DB
    // 4) Server B = explicit construction on the same file: repository.Open(
    //    ctx, "sqlite", h.dsn) → bRepo.Migrate(ctx) (idempotent version-skip;
    //    while A is closed; WAL inherited from the file header) →
    //    events.NewEventOutboxRelay(bRepo, logger, opts{PollInterval:50ms,
    //    BatchSize:32, ClaimTTL:30s, HTTPTimeout:5s, MaxAttempts:10, AuditSink})
    //    — ClaimTTL=30s/HTTPTimeout=5s is C-6-compliant (30 > 2×5): programmatic
    //    opts bypass config.Validate (NewEventOutboxRelay default-fills only),
    //    so the test must self-honor the invariant; the earlier 200ms skeleton
    //    was 50× over the line and lease-boundary-racy under -race. COMBINED
    //    cleanup: t.Cleanup(func(){ bCancel(); _ = bRepo.Close() }) — relay
    //    cancel strictly precedes repo close (two separate cleanups could
    //    close the repo while the relay still polls). No REST router needed
    // 5) B-phase poll ≤15s, accepting pending|inflight mid-flight (claim
    //    predicate is time-only on 'pending'): deleted@1.1 → L2 200+echo →
    //    'delivered' on a1; notify@1.1 → 500 (flip) → RetryEventOutbox →
    //    available_at = now + [0.75,1.0]s (downward-only jitter, D-7) → a2 at
    //    [0.8,1.1]s → 200 → 'delivered' (worst case ~1.1s; ~14s margin;
    //    MaxAttempts=10 absorbs an unexpected second 500 streak — a4 ≤ 7.2s
    //    still < 15s)
    // 6) 2XX-ONLY, status-scoped counters: notify target tracks notifyTotal
    //    AND notify2xx separately (A's 500s must never count toward "exactly
    //    1"). Assert notify2xx==1 && notifyTotal==2 (1×500 flip + 1×200) &&
    //    L2 total==1
    // 7) stability (2s: counters unchanged 2/1/1) then ≥10 cycles at 50ms (1s)
    //    more: counters still 2/1/1 AND both rows still "delivered" via
    //    outboxStatus (state witness — immune to counter timing)
}
```

Fixture note: the second server is constructed explicitly in-test (`repository.Open` on `h.dsn` + `events.NewEventOutboxRelay` — no `httptest` router; the relay is self-contained). `openSQLite` sets `MaxOpenConns(1)` + WAL, and B's handle plus `outboxStatus`'s raw conns inherit WAL from the file header — no cross-handle lock hazard given A is closed before B opens (ordering per step ③/④). Spec §6 allows this; the requirement is *same DB file + real relay instance*, not helper reuse.

Post-landing: `go test ./internal/integration/ -count=1` + full `make check` (gofmt/build/vet/test) — both new tests green this session (G1 5.8s / G2 4.5s).

---

## 7. Decisions taken where the spec left freedom

| # | Decision | Rationale |
|---|----------|-----------|
| D-1 | **No production behavior changes at all** — design delta = G1/G2 tests only + one comment-only jitter doc fix (D-7) | All 8 citations + all FR claims verified landed and green (§1); adding code would be speculative churn against I5/refactor-first |
| D-2 | G1/G2 live in `internal/integration/fullserver_test.go` reusing `startFullServerWithRelay`/`outboxStatus`/`waitForBodies` | Spec §6 guidance; harness is the established composition surface (AC-1b/AC-4a prove it) |
| D-3 | G1 asserts notify body **byte-equal** to `event_outbox.payload` (not re-parsed/field-wise) | The relay's verbatim invariant (`deliverPayload`) is the strongest exactly-once witness; field-wise asserts would allow payload drift |
| D-4 | G2 crash window simulated via `relayOpts == nil` on server A (**no relay goroutine** — facts stuck `pending` by construction) instead of mid-claim kill or `PollInterval=time.Hour` | Deterministic, no `NewTimer(0)` first-round race; semantically identical to "crashed before first poll"; "stop server A" is exact (no dormant relay can touch the closed DB; hidden `relayCancel` is moot). The *mechanism* under test (reclaim + redeliver + no-dup on a shared DSN) is identical — claim-lost redelivery is already unit-proven (:259), G2 proves the composition (same DB file, second live relay) |
| D-5 | `occurred_at` asserted as row-column timing, never added to envelope | Verified absent (§1); schema evolution explicitly out of scope (spec §5) |
| D-6 | WIP (quarantine `reason`/`signature`/`SoftDeleteObjectByIDWithEvent`) excluded from all assertions except AC-3's stable goldens | Sibling campaign owns it; its additive builder shape cannot break this design (C-9) |
| D-7 | **Jitter mismatch resolved by fixing the comment + test, not the implementation.** `jitter` (webhook.go:22-32) yields **downward-only [0.75, 1.0)×base** (`n ∈ [0, d/2)` → `d - d/4 + n/2`), not ±25%; the old comment and `TestEventOutboxBackoffBounds` [0.75b, 1.25b] overstate it (and could never catch drift to e.g. [0.8, 1.1]). Keep the implementation — true ±25% would be a production behavior change (slower worst-case dispatch: attempt 5 ≈15.3s would break G2's 15s budget) outside this design's scope, and downward-only jitter strictly *shrinks* the C-5 at-least-once window. Drive-by edits: `webhook.go` + `event_outbox_relay.go:341` comments, `TestEventOutboxBackoffBounds` → [3/4·b, b), AC-1b comment (`fullserver_test.go:767`). `auditgovernance.boundedBackoff` is a *different* (deterministic-hash) ±25% — unchanged |

---

## 8. Out of scope (spec §5, unchanged)

Admin audit surface (`admin.go:410-428` outbox-ization) · `DeleteVersion`/delete-marker/retention-purge WithEvent-ization · quarantine WIP batch behavior · envelope `occurred_at` field · legacy `object_events`/`bus.go` restructuring · webhook/notification-rule engine changes · new migrations or `go.mod` dependencies.

**VERDICT: PASS** — all 8 evidence citations and all material facts independently re-verified against HEAD + WIP (immaterial line nits only: E1 ±2, E3 ±4, E6 ≤25, E7 ≤17); the design requires zero wire-level changes, zero migrations, zero production-behavior changes, and exactly two integration tests (G1/G2) — **both implemented in the WIP and green this session (G1 5.8s / G2 4.5s)** with every review delta incorporated: harness bus+notifier wiring (workers.go:141-147 shape), absolute `==1` + `len==1 && bytes.Equal` guard before any body read, notify-body content pins (event_type/tenant/bucket/key/records/sequencer `^[0-9a-f]{32}$`), fixed 5s no-dup window, G2 ClaimTTL=30s/HTTPTimeout=5s (C-6-compliant), 2xx-only flip-on-first-500 counters, MaxAttempts=10, pending|inflight poll + 2s stability + ≥10-cycle no-dup with `outboxStatus` state witness, combined cancel-then-close cleanup, `relayOpts==nil` for server A, origin_id-scoped payload SELECT; the jitter comment/`TestEventOutboxBackoffBounds` mismatch is resolved by documented decision D-7 (comment + bounds fixed to the actual [0.75, 1.0)×base); every acceptance criterion maps to a named test, with AC-1…AC-4a green (repository 2.0s, events 6.9s, integration 2.132s) plus G1/G2 green this session.
