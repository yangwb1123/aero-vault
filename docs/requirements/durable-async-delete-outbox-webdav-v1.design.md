# Design: Transactional outbox (durable_async) — WebDAV DELETE surface acceptance contract (`vault.file.deleted@1.1` audit + `vault.file.notify@1.1` notification)

> **Companion spec:** `docs/requirements/durable-async-delete-outbox-webdav-v1.md` (FR-1…FR-4, AC-1…AC-4) · **Module:** `internal/api/webdav` (composition surface: `internal/service` + `internal/repository` + `internal/events` + `cmd/server`) · **Status:** design + implementation-machinery-landed (round-1/round-2 campaign); **this design's delta = 2 new test files + 1 docs patch, zero production code changes** · **Baseline:** HEAD `acfaaf4` + in-flight quarantine WIP (excluded, §8) · **Gates:** `make check` green · stdlib only (I6) · I1/I2 discipline (**zero DB migrations** — `0041_event_outbox` exists) · **Zero wire-level API changes**

---

## 1. Evidence re-verification (independent check against HEAD `acfaaf4`)

All 7 spec citations (E1–E7), all supplementary facts, and the measured claims were re-checked against the working tree. **All hold.** Line nits (immaterial): E5's `HasEventOutboxFact` call site is `notifier.go:74` (spec's ":74" ✅ exact; the dedupe block is :70-87, spec's :58-79 straddles `deliver` start); E6 `CompleteAuditGovernance`/`RetryAuditGovernance` sit at :124/:135 (not :102/:112 — same file, later lines); the spec's "harness 返回 repo handle" is loose — `newTestServerWithSvc` returns `*service.FileService`, repo reachable via the `Repo()` accessor (`file.go:205`, `service_test.go:313` pins it).

| # | Claim | Verified location | Verdict |
|---|-------|-------------------|---------|
| E1 | `Event` schema `repository.go:175-189`, no version field; versioning lives in `OutboxEventType` (`event_outbox.go:14-21`); `EventType` legacy four (created/updated/deleted/accessed :188-196) | `internal/repository/repository.go:175-196` · `internal/repository/event_outbox.go:14-21` (`EventTypeFileDeleted11` :22, `EventTypeFileNotify11` :25) | ✅ exact |
| E2 | `s.emit` `file.go:297-314` builds minimal map payload, swallows errors ("lifecycle events are best-effort…") | `internal/service/file.go:297-314` | ✅ exact |
| E3 | Spec's "no audit write" claim **stale**: `deleteAuditEntry` :100-113 + `deleteFacts` :123-137 committed in the same tx as the delete via `HardDeleteObjectWithEvent` :46 / `SoftDeleteObjectWithEvent` :86; `DeleteVersion` :174-219 stays bus-only (E14) | `internal/service/file_delete.go` | ✅ (stale claim confirmed; correction verified) |
| E4 | Bus persist-then-broadcast, drop-on-full, errors warn-only | `internal/events/bus.go:76-104` (`InsertEvent` :84, warn-and-return :86-88, transport warn :101-103); `TestSubscribe_BufferedAndDropsWhenFull` `bus_test.go:172` | ✅ (drift ±4) |
| E5 | Notifier D2 dedupe + `postEventTo` shared by bus Notifier and outbox relay, no HMAC on notify path | `internal/events/notifier.go:70-87` (D2 skip on `HasEventOutboxFact(…, EventTypeFileNotify11)`, call :74; E14 paths keep bus path), `postEventTo` :137-153; webhook HMAC `webhook.go:66-71` `WithSecret`, `TestWebhookDeliverWithHMAC` `webhook_test.go:150` | ✅ exact |
| E6 | Outbox precedents: claim/complete/retry + status-shape predicate | `internal/repository/audit_governance_claim.go:16/:124/:135` · `internal/repository/billing_outbox.go:11`; event outbox claim uses billing shape (`event_outbox.go:251-264`) | ✅ (drift ≤12) |
| E7 | `auditgovernance/model.go:17-28` hardcoded `governancePath="api/v1/events"` :19, mechanism untouched; new L2 face = `AuditSink` port + config-driven `AuditSinkL2` | `internal/auditgovernance/model.go:17-27` · `internal/events/audit_sink.go` (port, `ErrSinkNotBound`/`ErrSinkUnauthorized`) · `internal/events/audit_sink_l2.go` (endpoint validation, 401/403 short-circuit, `X-Audit-Fact-Id` echo receipt; zero sibling imports) | ✅ |
| F1 | WebDAV `DELETE` single entry: `davFS.RemoveAll` → `svc.Delete(ctx, tenant, DefaultBucket, name, **hard=true**)`; `ErrNotFound` → `os.ErrNotExist` | `internal/api/webdav/dav.go:141-147`; **surface mapping amended** (§2.1): client-visible 404 comes from x/net's Stat-before-RemoveAll pre-check via `davFS.Stat` (`webdav.go:260-265`); `RemoveAll`'s `os.ErrNotExist` mapping is observable only for virtual directories (Stat succeeds as dir, RemoveAll fails) → 405 (`webdav.go:266-268`) | ✅ exact (adapter layer); see §2.1 |
| F2 | `davFS.Rename` = copy-then-delete with rollback → every MOVE emits 1 audit row + 2 facts for the source (direction-3 item) | `dav.go:150-206` (source delete :198, rollback :199-204); `TestMoveRollbackOnDeleteFailure` `dav_test.go:863` | ✅ exact |
| F3 | WebDAV module zero audit/outbox coverage: `rg audit|outbox|deleted@|notify@ internal/api/webdav/` → zero hits; `TestDeleteRemovesResource` `dav_test.go:139` asserts status+404 only | verified | ✅ |
| F4 | `newTestServerWithSvc` `dav_test.go:43-70`: SQLite+Migrate+local FS+`service.NewFileService(store, repo, nil)`+`mw.Tenant(webdav.Handler("/webdav", svc, nil))`; **no Bus/relay**; no principal/RequestID middleware → `actor`/`request_id` empty strings legal (`deleteFacts`/`deleteAuditEntry` accept empty) | verified; `Repo()` accessor `service/file.go:205` | ✅ |
| F5 | `docs/configuration.md:354-358` documents `EVENT_OUTBOX_*`; `AUDIT_SINK_L2_*` absent (grep zero hits) | verified | ✅ (genuine docs gap, §6.3) |
| F6 | Relay always-on: `cmd/server/workers.go:158-191` `startEventOutboxRelay`, L2 only when `cfg.AuditSinkL2.Endpoint != ""`; migration `0041_event_outbox.{up,down}.sql` in both sqlite + postgres | verified | ✅ |

**Measured evidence re-run (this session, `-count=1`):**

| Claim | Result |
|-------|--------|
| `go build ./...` exit 0 | ✅ |
| `go vet ./internal/api/webdav/ ./internal/events/ ./internal/repository/` clean | ✅ |
| `go test ./internal/api/webdav/ ./internal/events/ ./internal/repository/ ./internal/service/` | ✅ all `ok` (42.8s / 9.1s / 39.3s / 30.2s) |
| 4 integration composition tests | ✅ 4/4 PASS, 9.924s (`TestDeleteResponse_DoesNotBlockOnDelivery` 0.33s · `TestComposition_AuditSinkL2BoundTenant` 1.88s · `TestComposition_DeleteDeliversBothFacts` 5.26s · `TestComposition_MidClaimRestartRedeliversOnce` 2.45s) vs spec's claimed 9.876s — same ballpark, minor drift expected |

**Genuine module-surface gaps confirmed (the deliverable of this design):** G1 — no WebDAV-surface test asserting audit row + 2 facts; G2 — no WebDAV-surface timing test (response-vs-delivery decoupling); G3 — no WebDAV-surface L1/L2 composition; docs gap — `AUDIT_SINK_L2_*` undocumented.

---

## 2. API changes

### 2.1 Wire-level (protocol/config) — **none**

No WebDAV method/status/header changes, no new routes, no env vars, no flags. `DELETE /webdav/{path}` contract unchanged (x/net v0.55.0 `handleDelete`): success is **204 only** (`webdav.go:269` — the test's "{204,200}" is a cross-version tolerance, not an emission; G1 keeps it for `TestDeleteRemovesResource` parity); missing-key **404 comes from the Stat-before-RemoveAll pre-check** via `davFS.Stat` (`webdav.go:260-265`), not from `RemoveAll`'s `ErrNotFound→os.ErrNotExist` (that mapping is observable only for virtual directories, where Stat succeeds and `RemoveAll` fails → **405**); **every service/storage error on the DELETE path — including FM-1's tx rollback and FM-13's post-commit quota failure — surfaces as 405 Method Not Allowed** (`webdav.go:266-268`), never 5xx (RFC 4918 §9.6 discusses no 405; clients misread storage failures as "method not allowed" — accepted, pre-existing). `MOVE` (copy-then-delete, `Overwrite:F` default per x/net `moveFiles`), `PROPFIND`, `PUT`, `GET` semantics identical. The outbox facts are **internal persistence + relay delivery**; nothing about the WebDAV surface changes. WebDAV has no `?hard=` — deletes are **always hard** (`RemoveAll` → `hard=true`), so `audit_log.Detail` is always `"hard"` and `DeleteVersion`/delete-marker bus-only paths are unreachable from WebDAV.

### 2.2 Go-level (complete breakage surface)

**Production code: zero changes.** The delete path WebDAV exercises (`svc.Delete` → `hardDeleteObject` → `HardDeleteObjectWithEvent` with `deleteAuditEntry` + `deleteFacts`) already commits the audit row + both outbox facts in one transaction, and the always-on relay (`workers.go:158-191`) drains them — no new production symbols, no signature changes, no `internal/events` edits. The D2 dedupe (`notifier.go:70-87`) already covers WebDAV deletes since they share the same service path.

**New code for this design (test-only + docs):**

| # | File | Content | Constraints |
|---|------|---------|-------------|
| T1 | `internal/api/webdav/dav_audit_test.go` (**new**, ≤500 lines) | G1 `TestWebDAVDelete_CommitsAuditAndBothFacts` + G3 `TestWebDAVDelete_CompositionL1L2` + shared L0/L1/L2 assertion helpers copied from `fullserver_test.go` shape (no cross-package import of `internal/integration` — I6 decoupling) | `testing` only (I6); ≤500 lines/file (hard gate); `gofmt` clean |
| T2 | `internal/api/webdav/dav_relay_test.go` (**new**, ≤500 lines) | G2 `TestWebDAVDelete_ResponseDoesNotBlockOnDelivery` + the new relay harness `newTestServerWithRelay` (see §6.2). **Deliberately not appended to `dav_test.go` (893 lines, already over the 500-line convention)** | same |
| T3 | `docs/configuration.md` (patch) | add `AUDIT_SINK_L2_ENDPOINT` + `AUDIT_SINK_L2_BINDINGS_FILE` rows next to the `EVENT_OUTBOX_*` block (:354-358) | the **only** non-test delta |

Existing `dav_test.go` harness (`newTestServer`/`newTestServerWithSvc`) and all its call sites: **untouched** (AC-4 regression surface). G1 reuses `newTestServerWithSvc` as-is — L0 assertions reach the repo via `svc.Repo()` (`file.go:205`), so **no harness signature change is needed for G1**.

---

## 3. Compatibility constraints

| # | Constraint | Mechanism |
|---|-----------|-----------|
| C-1 | **WebDAV wire contract unchanged (hard):** DELETE statuses {204,200}, 404 mapping, MOVE copy-then-delete rollback, tenant isolation identical pre/post design | No handler edits; G1 keeps `TestDeleteRemovesResource` semantics (`dav_test.go:139`) |
| C-2 | **Legacy `object_events` stream semantics unchanged:** `s.emit` still runs after commit → SSE replay, indexer, AV, replication, webhook consume exactly as today; outbox is additive persistence | `bus.go` untouched |
| C-3 | **Notify dedupe contract (scoped to the WithEvent delete path — caveat FM-12):** a delete that committed WithEvent (which **every WebDAV delete does** — `RemoveAll` hard path) must produce exactly one notify@1.1 relay delivery, and the bus notifier must skip it (D2 `HasEventOutboxFact`, `notifier.go:70-87`); no race — WithEvent tx commits before `s.emit`, and `CompleteEventOutbox` never deletes rows (status→`delivered`, retained until prune) so the dedupe key stays true in both directions. **Outside this scope** (E14 paths: `DeleteVersion`/delete-marker/quarantine, WebDAV-unreachable), the same `origin_id`-keyed check false-positives on stale rows → notification loss (FM-12) | existing machinery; G3 asserts the skip e2e from the WebDAV surface |
| C-4 | **L0 audit always-on:** `audit_log` row (`Action=="file.delete"` per `audit.go:13`, `Detail=="hard"` — WebDAV is always hard) written regardless of L2 binding; no L2 → delete still 2xx, relay completes `deleted@1.1` without delivery | `deliverDeleted` nil-sink complete path |
| C-5 | **At-least-once window explicit:** deliver→complete crash-recoverable; exactly-once holds **after** complete (`event_outbox_delivered` same-tx insert). WebDAV DELETE response never waits on delivery | claim lease + complete tx; G2 asserts from WebDAV surface |
| C-6 | **Multi-instance safety / programmatic relay opts:** `NewEventOutboxRelay` default-fills without `config.Validate` — **tests constructing relays directly must self-honor `ClaimTTL > 2×HTTPTimeout`** (config enforces it at load, `config_event_outbox.go:67`). That rule is *necessary, not sufficient*: it bounds only **single-fact delivery + batch drain < TTL** — the batch shares one lease timestamp (`claimEventOutboxIDs`, `event_outbox.go:316`) and `deliverNotify` runs on a `claimTTL` ctx (`event_outbox_relay.go:250`). **Ops bound (documented `config_event_outbox.go:55-57`): `EVENT_OUTBOX_BATCH_SIZE × per-fact delivery < EVENT_OUTBOX_CLAIM_TTL_SECONDS`** — defaults 32 × 5s ≫ 30s, so worst-case no-crash duplicates are possible in production (FM-14); the harness is safe because it uses 1–2 facts with ms deliveries. New harness opts in §6.2 | harness opts in §6.2 |
| C-7 | **No new migrations/deps:** `0041_event_outbox` (+ `event_outbox_delivered`) exists in both dialects; stdlib only (I6); no new SQL | — |
| C-8 | **`repository.Event` unchanged:** no version field; versioning stays in outbox payload envelope (`schema_version:"1.1"`); `object_events`/SSE untouched | — |
| C-9 | **Golden bytes stable:** `schema_test.go` goldens must pass unmodified; WIP additive fields (`reason`/`signature`) are `omitempty`, struct-ordered after existing fields | — |
| C-10 | **Harness isolation:** new tests copy assertion helpers (`assertAuditRowFor`-shape / `outboxStatus` / `outboxPayload` / `setDeleteRule`) into the webdav package — never import `internal/integration` from a unit test package | I6, package decoupling |

---

## 4. Failure modes

| # | Trigger | Observable (WebDAV surface) | Behavior | Design response |
|---|---------|----------------------------|----------|-----------------|
| FM-1 | DB error mid-delete-tx (disconnect/lock) | DELETE **405** at the WebDAV surface (x/net maps any `RemoveAll` error → 405, `webdav.go:266-268`); object row, audit, facts all rolled back | all-or-nothing (AC-1) | asserted by `TestDeleteObjectWithAudit_OneTx` rollback branch (service/repo level); G1 asserts happy-path commit from WebDAV |
| FM-2 | `validateOutboxFacts` failure (programming error: >1 MiB payload, schema ≠ 1.1, unknown type) | on the keyed hard/soft paths validation runs **before** `BeginTx` (`event_outbox.go:106/:151` vs `:109/:154` — no tx is started; observable identical to a rollback); object **not** deleted; DELETE 405 | Guard, not expected path (constants-only builders) | `event_outbox_test.go:71-134` forced-rollback |
| FM-3 | Relay crashes mid-claim | rows stuck `inflight` with lease; after `lease_expires_at_ns` any instance re-claims (predicate has **no owner exclusion**, `event_outbox.go:186-220`) | redelivery, no double-schedule (owner+token fencing; stale `complete`/`retry` → `ErrClaimLost`, warned + counted, never re-scheduled in-loop) | `TestEventOutboxClaimLeaseExpiryRedelivers` + `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` — single-fact; batch/multi-target drain > TTL is the no-crash duplicate variant, **FM-14** |
| FM-4 | Delivery target down (5xx/network) | durable retry: attempts++, backoff (2×, cap 5 min, jitter [0.75,1.0)×base), attempts≥10 → terminal `failed` + `last_error`; WebDAV DELETE already returned 2xx | relay machinery | `TestEventOutboxRetryBackoffAndTerminalFailed` / `TestEventOutboxBackoffBounds` |
| FM-5 | L2 returns 401/403 | `deleted@1.1` fails **immediately** (no retry loop — authz is not transient) | `failImmediately` path | `TestOutboxRelay_L2UnauthorizedFailsImmediately` |
| FM-6 | Notify rules lookup error (`GetBucketNotifications` fails) | retry with backoff; payload intact | `deliverNotify` | covered by FM-4 tests |
| FM-7 | No L2 + no notify rules | both facts complete without network; rows pruned by `PruneEventOutbox` | silent no-op (design intent) — **tests must insert the rule BEFORE the DELETE**; a late insert = silent complete = 0 POSTs | G3 `setDeleteRule` ordering; `len==1` guard before any body read |
| FM-8 | `ChunkCleaner` failure on delete | warn log only; delete proceeds | AGENTS.md §2.1 ③ | unchanged |
| FM-9 | Process restart with pending rows | new instance reclaims (pending or expired-inflight) and delivers; complete rows never re-deliver | G2 e2e (REST) proves it; G2-WebDAV adds surface variant | **new test** §6.2 G2 |
| FM-10 | WebDAV **MOVE** emits delete signals: plain rename = 1 audit + 2 facts for the source; **`Overwrite:T` onto an existing dst = 2 audit rows + 4 facts + legacy `created@dst`** — x/net `moveFiles` deletes dst *before* `Rename` (`file.go:621-628`), and `davFS.Rename`'s copy-then-delete then emits `created@dst` + `deleted@src` on the legacy bus | every rename writes audit + facts; possible notification of a "deleted" key that still exists at destination | documented, not a bug — direction-3 (MOVE suppression) explicitly out of scope (§8) | none |
| FM-11 | WebDAV-specific error mapping | **surface-truth:** 404 arrives only via x/net's Stat-before-RemoveAll pre-check (`webdav.go:260-265`); every `RemoveAll` error — virtual-dir `ErrNotFound`, tx rollback (FM-1), post-commit quota failure (FM-13) — maps to **405** (`webdav.go:266-268`), never 5xx | existing contract; G1 asserts 404 → zero rows | none |
| FM-12 | D2 **false positive** via `origin_id` reuse → E14 notification **loss**: soft-DELETE K (WithEvent → notify fact `origin_id=X`, retained 24h until prune) → `RestoreObject` revives row X **in place** (UPDATE, same id, `sql_objects_maint.go:240-262`) → `DeleteVersion` on K (bus-only, no outbox facts, emits `EventDeleted` with `ObjectID=X`, `file_delete.go:195-219`) → notifier's `HasEventOutboxFact(X, notify)`=TRUE from the **stale** fact → bus path skipped → version-delete notification **silently lost**, contradicting `notifier.go:78-86`'s "never silently drop them" | requires soft-delete → restore → version-delete within 24h + matching rule; low probability; **WebDAV-unreachable** (no version delete/restore on that surface); pre-existing machinery — C-3 caveat | production fix: per-delete occurrence key (the payload's fresh sequencer — never `obj.ID`, cf. `file_delete.go:122`) in the dedupe EXISTS, or outbox-ize `DeleteVersion` (direction 3) | none (documented) |
| FM-13 | Post-commit quota failure: `addTenantUsage` runs **after** the WithEvent tx commits (`file_delete.go:50-54`); on error the DELETE returns failure while the object is deleted and both facts deliver | client-visible lie (committed mutation reported as error; surfaces as 405 per FM-11); at-least-once unaffected; pre-existing — FM-11's blanket error mapping doesn't distinguish committed vs rolled-back | none | none (documented) |
| FM-14 | Batch drain > TTL no-crash duplicate (**ops bound**, not code): a batch claims up to 32 facts under **one** lease timestamp (`claimEventOutboxIDs` shared `until`, `event_outbox.go:312-316`); worst case 32 × 5s ≫ 30s — facts claimed early expire while the batch drains and are re-claimed (self or peer) → duplicate POSTs, **no crash** | only with slow targets at batch scale; harness tests use 1–2 facts + ms deliveries → unreachable | at-least-once contract ("receivers must be idempotent", relay:24-26); ops bound `BATCH_SIZE × per-fact delivery < TTL` in C-6; production follow-up = lease-per-fact or smaller batches | none (documented) |

---

## 5. Migration steps

1. **DB:** none — `0041_event_outbox` (+ `event_outbox_delivered`) already exists in both dialects (I2: no new files, no edits to applied ones). WebDAV deletes already write into it via the shared service path; no schema work for this design.
2. **Config/env:** none — relay always starts (`workers.go` `startEventOutboxRelay`); `EVENT_OUTBOX_*` documented; `AUDIT_SINK_L2_*` **already parsed and validated** by `internal/config/config_audit_sink_l2.go` (endpoint scheme check H1, bindings-file token hygiene) — the only gap is documentation, fixed by T3. No operator action to enable L0/L1; L2 activates by setting `AUDIT_SINK_L2_ENDPOINT` + `AUDIT_SINK_L2_BINDINGS_FILE`.
3. **Deploy:** the machinery shipped with round-1/round-2; **this design's delta is test code + docs** — rollout = `make check` green + existing release process. No behavioral activation step; WebDAV DELETE behavior is unchanged before/after.
4. **No backfill:** deletes that happened before the WithEvent path shipped are not retro-audited/delivered (documented limitation; reconcile does not touch `event_outbox`).
5. **Rollback:** revert the test-only delta (trivial) or the round-1/round-2 commits. Pending rows drain harmlessly via the always-on relay; delivered rows are append-only history; `event_outbox_delivered` rows inert. No data cleanup required.
6. **Ops notes:** with bucket-notification rules, **WebDAV** deletes now produce relay-delivered notify@1.1 (durable, ≤ poll-interval latency) instead of in-process Notifier dispatch — same destination, at-least-once; D2 skip prevents double-delivery. MOVE operations produce a notify for the source key (direction-3 suppression pending). Without rules: zero network, complete + prune. `AUDIT_SINK_L2_ENDPOINT` must be HTTPS or loopback (H1); bindings file tokens are static secrets — rotate by file replacement (no hot-reload; restart required).

---

## 6. Testable acceptance mapping

Test packages: `internal/repository`, `internal/events`, `internal/service`, `internal/integration` (existing) + `internal/api/webdav` (new G1/G2/G3); assertions via `testing` only (I6). `make check` gates all.

| AC (spec §4) | Coverage — existing ✅ | Coverage — **new 🟥 (this design)** |
|--------------|------------------------|-------------------------------------|
| **AC-1** delete → exactly 1 `deleted@1.1` + 1 self-contained `notify@1.1`, schema rejects malformed | ✅ `TestEventSchema_GoldenJSON`/`RequiredFields`/`Deleted11Envelope`/`SequencerUniquePerCall` (`schema_test.go`), `TestDeleteObjectWithAudit_OneTx`/`TestDeleteObjectWithEvent_OneTx` (`event_outbox_test.go`), `TestFileServiceDelete_WritesAuditRow` (`file_delete_test.go`) | 🟥 **G1** — WebDAV-surface: PUT→DELETE commits audit row + both facts; DELETE-missing → 404 + zero rows |
| **AC-2** commit → kill relay → restart → exactly-once delivery, non-blocking | ✅ `TestDeleteResponse_DoesNotBlockOnDelivery` (REST), `TestComposition_MidClaimRestartRedeliversOnce`, lease/claim/retry suite (`event_outbox_test.go:259/:300`, `event_outbox_relay_test.go:181/:229/:609`) | 🟥 **G2** — WebDAV-surface timing variant: DELETE returns while L2 target hangs; facts drain after release; exactly 1 POST; 5s no-dup window |
| **AC-3** L0/L1/L2 all selectable, no code change | ✅ L0 service-level; L1 `TestWebhookDeliverWithHMAC`/`TestWebhookDeliver_SendsEventIDHeaderWithHMAC` (`webhook_test.go`), `TestComposition_DeleteDeliversBothFacts` (notify relay, REST); L2 `TestComposition_AuditSinkL2BoundTenant` | 🟥 **G3** — WebDAV-surface L1 (webhook HMAC + notify rule) + L2 (AuditSinkL2 bound/unbound tenant) composition; 🟥 **T3** docs patch (`AUDIT_SINK_L2_*` rows) |
| **AC-4** no regression | ✅ full suite green (§1) | 🟥 run `go test ./internal/api/webdav/ ./internal/integration/ -count=1` + `make check` after landing |

### 6.1 New test spec G1 — `TestWebDAVDelete_CommitsAuditAndBothFacts` (`internal/api/webdav/dav_audit_test.go`)

```go
// L0 assertions via svc.Repo() (file.go:205) — no relay participation needed;
// the WithEvent tx commits facts visible to any handle on the same SQLite file.
func TestWebDAVDelete_CommitsAuditAndBothFacts(t *testing.T) {
    srv, svc := newTestServerWithSvc(t)          // existing harness, unchanged
    repo := svc.Repo()
    // 1) PUT /webdav/gone.txt ("bye") → DELETE /webdav/gone.txt → 204 or 200
    //    (mirror TestDeleteRemovesResource dav_test.go:139 status assertion)
    // 2) obj := pre-delete repo.GetObject(ctx, "default","default","gone.txt")
    //    → keep obj.ID for the object_id pin
    // 3) audit_log (SELECT ... WHERE tenant_id='default'): exactly 1 row,
    //    Action=="file.delete" (audit.go:13), Detail=="hard" (RemoveAll hard=true),
    //    Target=="default/gone.txt", TenantID=="default", Actor=="" (no principal
    //    middleware in harness — legal empty), RequestID==""
    // 4) event_outbox: exactly 2 rows, types {vault.file.deleted@1.1,
    //    vault.file.notify@1.1}, origin_id==obj.ID, tenant_id=="default"
    //    a. deleted@1.1 payload: schema_version=="1.1",
    //       event_type=="vault.file.deleted@1.1", object_id==obj.ID,
    //       bucket=="default", key=="gone.txt", actor=="", request_id=="",
    //       NO "records" field (assert absent — not S3 shape)
    //    b. notify@1.1 payload: schema_version=="1.1",
    //       event_type=="vault.file.notify@1.1",
    //       records[0].eventName=="s3:ObjectRemoved:Delete",
    //       records[0].s3.object.{key=="gone.txt", size=="3", eTag==obj.ETag,
    //       versionId==obj.VersionID, sequencer matches ^[0-9a-f]{32}$}
    // 5) DELETE /webdav/notexist.txt → 404 (os.ErrNotExist mapping);
    //    audit_log count unchanged; event_outbox count unchanged
    // 6) GET /webdav/gone.txt → 404 (TestDeleteRemovesResource semantics kept)
}
```

### 6.2 New test spec G2 + relay harness — `internal/api/webdav/dav_relay_test.go`

**Harness delta** (new, alongside — not inside — `dav_test.go`, which is 893 lines):

```go
// newTestServerWithRelay mirrors startFullServerWithRelay (fullserver_test.go:55)
// but mounts only the WebDAV handler: SQLite+Migrate+local FS + FileService +
// mw.Tenant(webdav.Handler(...)) + events.NewEventOutboxRelay(repo, logger,
// opts). Optional AuditSink injection. Returns (*httptest.Server, *service.FileService, cancel).
// Relay opts MUST self-honor C-6: PollInterval=50ms, BatchSize=32,
// ClaimTTL=60s, HTTPTimeout=10s, MaxAttempts=10 (60 > 2×10 and < 600s cap;
// programmatic opts bypass config.Validate). The 60/10 bump over the REST
// sibling (30/5) doubles the hang-guard slack; the batch-lease caveat (FM-14)
// is unreachable at 1–2 facts with ms deliveries.
// Cleanup order (LIFO): relayCancel → ts.Close → repo.Close.
```

```go
func TestWebDAVDelete_ResponseDoesNotBlockOnDelivery(t *testing.T) {
    // L2 target: httptest server with a hang-guard (release channel; handler
    // blocks while open — must stay strictly below relay HTTPTimeout=10s so
    // the fact stays inflight, not failed; the 4s guard mirrors the sibling's
    // "4s < 5s" discriminator with double margin) + 200 + X-Audit-Fact-Id
    // echo on release.
    // Release-before-timeout invariant (sibling style, cf. fullserver_test.go
    // :680-685): close(release) MUST land well below the 10s HTTPTimeout —
    // the work between DELETE-return and close(release) is a single SQLite
    // SELECT (µs–ms), ~100× margin even under pathological -race scheduling;
    // "exactly 1 POST" is hard by design (a late release burns attempt 1 and
    // the backoff retry produces POST #2), so keep that margin and never add
    // work between the DELETE and the release.
    // sink := events.NewAuditSinkL2(target.URL, map[string]string{"default": token}, ...)
    // 1) PUT /webdav/k → DELETE /webdav/k while hang-guard open
    //    → response ALREADY returned (timing proof: DELETE 204 while
    //      delivery unreachable) — mirror TestDeleteResponse_DoesNotBlockOnDelivery
    // 2) at this moment: audit_log row EXISTS (L0 not coupled); deleted@1.1
    //    row status ∈ {pending, inflight} (delivered unreachable by construction)
    // 3) close(release) → poll ≤15s (50ms steps) → both facts status=="delivered"
    //    (via repo handle outboxStatus helper), L2 exactly 1 POST
    //    (len==1 guard BEFORE body read), X-Audit-Fact-Id == outbox row id
    // 4) FIXED 5s no-dup window — **state-witnessed** (sibling shape,
    //    fullserver_test.go :986-990): deleted@1.1 L2 POST counter unchanged
    //    AND both fact rows still status=="delivered"; complete is
    //    state-based (the claim predicate excludes 'delivered'), so a
    //    relay-side redelivery is impossible — counters alone would be
    //    timing-fragile. NOTE: this harness sets NO delete rule (FM-7), so
    //    the notify@1.1 fact completes silently with 0 POSTs by construction
    //    — "notify+L2 counters" in step 4 means exactly one counter: the
    //    deleted@1.1 L2 POST.
}
```

### 6.3 New test spec G3 + docs patch — `TestWebDAVDelete_CompositionL1L2` (`dav_audit_test.go`) + `docs/configuration.md`

```go
// Harness: newTestServerWithRelay with bus wiring (G3-only extension):
//   bus := events.New(repo, logger); svc.WithEventSink(bus)  // notifier/webhook
//   notif := events.NewNotifier(repo, logger); sub,_ := bus.Subscribe()
//   go notif.Run(ctx, sub)                                   // D2 dedupe live
//   wh := events.NewWebhook(whURL, logger).WithSecret(secret); go wh.Run(ctx, sub2)
//   relay opts with AuditSink: events.NewAuditSinkL2(l2URL, {"default": token})
//   (mirror cmd/server/workers.go:61-80 + :158-191 production wiring)
func TestWebDAVDelete_CompositionL1L2(t *testing.T) {
    // 1) L1 webhook: PUT /webdav/k → DELETE /webdav/k → webhook target receives
    //    exactly 1 HMAC POST: X-Aero-Signature "sha256=..." verifies against
    //    secret, body.type=="deleted" (legacy bus shape — webhook.go:86-90)
    // 2) L1 notify rule: setDeleteRule(repo, notifyURL) MUST run BEFORE the
    //    DELETE (FM-7 trap) → relay delivers notify@1.1 payload byte-verbatim
    //    (postEventTo, no HMAC — S3-notification shape, contract-internal);
    //    D2 dedupe live: bus notifier produced NO second notify POST
    //    (len(bodies)==1 guard; 5s window — state-witnessed: fact rows still
    //    "delivered", not just counters quiet, sibling :986-990 shape)
    // 3) L2: DELETE → exactly 1 POST to l2URL: Authorization: Bearer <token>,
    //    X-Audit-Fact-Id == event_outbox row id, body contains
    //    "event_type":"vault.file.deleted@1.1" + object_id; 200+echo → complete
    // 4) unbound tenant: PUT/DELETE with X-Aero-Tenant: other → 0 L2 POSTs
    //    (ErrSinkNotBound → graceful complete), facts still delivered status,
    //    audit_log row STILL written (L0 always-on, C-4)
}
```

**Docs patch T3** — `docs/configuration.md`, insert after the `EVENT_OUTBOX_*` block (:354-358):

| `AUDIT_SINK_L2_ENDPOINT` | (empty) | L2 audit-sink endpoint for `vault.file.deleted@1.1` delivery. HTTPS or loopback HTTP only (H1); empty = L2 off, relay completes facts without delivery (L0 `audit_log` remains authoritative). |
| `AUDIT_SINK_L2_BINDINGS_FILE` | (empty) | JSON file mapping tenant → static Bearer token (`{"tenants":{"<tenant>":"<token>"}}`); token hygiene: no logging, file-replace rotation, restart required. |

### 6.4 Shared assertion helpers (copied into `dav_audit_test.go` / `dav_relay_test.go`, C-10)

`outboxStatus(repo, originID, eventType)` / `outboxPayload(repo, originID, eventType)` (raw `SELECT` shape of `fullserver_test.go:1226/:1258`, literals only — I1 `rebind`-free), `assertAuditRowFor(repo, tenant, target)` shape, `setDeleteRule(repo, url)` (mirror `:1245`), `sequencerHexRe` (`^[0-9a-f]{32}$`). Total across both new files must stay ≤500 lines each (hard gate) — split G1+G3 into `dav_audit_test.go`, G2+harness into `dav_relay_test.go`.

---

## 7. Decisions taken where the spec left freedom

| # | Decision | Rationale |
|---|----------|-----------|
| D-1 | G1 reuses `newTestServerWithSvc` as-is; repo reached via `svc.Repo()` (`file.go:205`) | Spec's "harness 返回 repo handle" is loose; `Repo()` accessor makes a signature change unnecessary — zero existing call-site churn (AC-4) |
| D-2 | New harness `newTestServerWithRelay` lives in the **new** file `dav_relay_test.go`, not `dav_test.go` | `dav_test.go` is 893 lines (over the 500-line convention, pre-existing); appending would compound it. New files keep the delta self-contained and each ≤500 lines |
| D-3 | G3 harness wires bus+notifier+webhook unconditionally, mirroring `workers.go:61-80/:158-191` | Without the bus, `s.emit` is a no-op (`noopSink`) and the D2 dedupe skip (`notifier.go:70-87`) is dead code in the module tests — the C-3 claim would be untested from the WebDAV surface |
| D-4 | Relay opts fixed at Poll 50ms / Batch 32 / TTL 60s / HTTP 10s / MaxAttempts 10 | C-6: programmatic opts bypass `config.Validate`; **ClaimTTL=60s > 2×HTTPTimeout=20s and < 600s cap** — the validated bound, but it guarantees race-freedom only for **single-fact delivery + batch drain < TTL** (batch shares one lease; `deliverNotify` runs on a `claimTTL` ctx — C-6/FM-14). The harness's 1–2 facts with ms deliveries keep the lease boundary unreachable; the 60/10 bump doubles the G2 hang-guard slack over the REST sibling (30/5) |
| D-5 | `audit_log.Detail=="hard"` asserted in G1 | WebDAV `RemoveAll` is always hard (`dav.go:141-147`); pins the adapter's contract against a future soft-delete refactor |
| D-6 | G3's unbound-tenant branch uses `X-Aero-Tenant: other` through `mw.Tenant` | matches production tenant extraction; WebDAV remains default-bucket — tenant is the only isolation axis available at the surface |
| D-7 | Helpers copied into the webdav package (C-10) rather than exported from `internal/integration` | unit-test packages must not import an integration test package; duplication is deliberate and small |
| D-8 | No new env vars, no new config code | `AUDIT_SINK_L2_*` already exists in `config_audit_sink_l2.go` with validation; the gap is documentation only (T3) |

---

## 8. Out of scope (spec §5, unchanged)

| Item | Reason |
|------|--------|
| MOVE false-delete suppression (rename emits audit + facts for source) | direction 3; copy-then-delete is existing pinned semantics (`TestMoveRollbackOnDeleteFailure`) |
| share/version/RAG invalidation, chunk invalidation | direction 3 |
| admin-op audit outbox-ization | direction 3; delete audit already transactional |
| `repository.Event` version field / `object_events` schema change | spec: "@1.1 goes on the envelope"; legacy stream compatibility (C-2/C-8) |
| `auditgovernance` mechanism changes (binding table/revision/draining/redaction shape) | legacy governance is a separate tenant-bound face; L2 is the new port face (E7) |
| webhook pipeline / `webhook_failures` / DLQ changes | existing durable retry; L2 is a separate delivery face |
| L2 bindings persistence / management API | config-driven bindings file satisfies the acceptance; dynamic management would be a new direction (then 0042 migration pair, I2) |
| HMAC on notify-rule targets | S3-notification shape is unsigned by contract (`postEventTo` shared semantics); webhook worker (HMAC) is the signed L1 face |
| actor identity pipeline | `access.PrincipalFrom` with empty-legal fallback (WebDAV harness has no principal middleware) |
| quarantine WIP (`SoftDeleteObjectByIDWithEvent`, variadic `reason`/`signature`) | sibling campaign; additive `omitempty` fields keep goldens byte-identical (C-9) — no interaction with the WebDAV hard-delete path |

---

## 9. Landing checklist

1. `gofmt -l` clean on the two new test files.
2. `go vet ./internal/api/webdav/ ./internal/events/ ./internal/repository/ ./internal/service/`.
3. `go test ./internal/api/webdav/ -count=1` (G1/G2/G3 + existing 40+ tests green; ~43s baseline).
4. `go test ./internal/integration/ -run 'TestComposition|TestDeleteResponse' -count=1` (no regression on the shared machinery).
5. `make check` full gate.
6. Confirm both new files ≤500 lines; no production file modified (git status shows only `dav_audit_test.go`, `dav_relay_test.go`, `docs/configuration.md` for this design).
