# Design: Quarantine path as transactional outbox producer (`vault.file.deleted@1.1` + self-contained `vault.file.notify@1.1`)

> **Companion spec:** `docs/requirements/quarantine-outbox-producer-v1.md` (FR-1…FR-5, AC-1…AC-4) · **Module:** `internal/repository` + `internal/service` + `internal/antivirus` + `internal/events` (minimal touch) · **Status:** design (not implemented) · **Baseline:** HEAD `acfaaf4` · **Gates:** `make check` green · single file ≤ 500 lines · stdlib only (I6) · no `go.mod` changes · I1/I2 discipline (**zero DB migrations** — `0041_event_outbox` exists) · **Zero wire-level API changes** (no REST/S3/MCP/WebDAV/OpenAPI/config changes)

---

## 1. Evidence re-verification (independent check against HEAD)

All 8 cited symbols and all 8 supplementary facts were re-checked directly against the working tree. **All hold.** Line nits (immaterial): E6 cites `relay.go:22-48`, `reconcile` sits at :15-47; E3's `insertAuditEntry` body starts at :21; E1 body is :38-74.

| # | Claim | Verified location | Verdict |
|---|-------|-------------------|---------|
| E1 | `QuarantineObjectByID` — soft delete + `s.emit`, **no audit, no outbox**; delete/usage/event across three transactions | `internal/service/object_worker.go:38-74` (repo soft-delete :65, `addTenantUsage` :69-71, `s.emit(EventDeleted)` :72) | ✅ |
| E2 | `Publish` — `InsertEvent` then broadcast; "Errors are logged but never propagated" | `internal/events/bus.go:76-95` (warn-and-swallow :81-86) | ✅ |
| E3 | `RecordAudit` direct callers = only `admin.go:421` (+ decorator delegation) | `grep -rn RecordAudit` → `admin.go:421`, `auditgovernance/repository.go:22-33` (delegates to same method, not an independent caller); `audit.go:32-46` | ✅ |
| E4 | `admin.go:421` `_ = h.repo.RecordAudit(...)` | `grep -n` confirms line 421 exact | ✅ |
| E5 | `ClaimBillingUsage` claim-based outbox (status/attempts/claim_owner/claim_until_ns) | `internal/repository/billing_outbox.go:12-21` + postgres/sqlite claim SQL | ✅ |
| E6 | `auditgovernance` gap reconciliation precedent | `internal/auditgovernance/relay.go:15-47` (`reconcile` loop, gap scan + enqueue) | ✅ (line drift) |
| E7 | `ScanObjectByID` — scan → tags `av_status`/`av_signature` **then** quarantine; `res.Signature` in hand | `internal/antivirus/worker.go:119-157` (tag write :137-148, quarantine call :149-153) | ✅ |
| E8 | `Scanner` port (`SignatureScanner`/`HTTPScanner`) | `internal/antivirus/antivirus.go:17-20` (+ `Result` :10-13, EICAR const) | ✅ |

**Material findings (all verified):**

| Fact | Verified |
|------|----------|
| `0041_event_outbox.{up,down}.sql` exists in **both** sqlite+postgres; **no `UNIQUE`** in either up file | ✅ |
| `SoftDeleteObjectWithEvent`/`HardDeleteObjectWithEvent` — keyed, single tx, zero-row → `ErrNotFound` rollback | `event_outbox.go:96-176` ✅ |
| `validateOutboxFacts` (type/origin/tenant/1 MiB/schema_version 1.1) :53-77; `insertOutboxFacts` :180; `HasEventOutboxFact` (D2) :360+ | ✅ |
| `deleteAuditEntry`/`deleteFacts` shapes (actor from principal, `Detail` soft/hard, 2 facts, sequencer generated) | `file_delete.go:99-146` ✅ |
| `EventOutboxRelay` always starts (`workers.go:63`, `startEventOutboxRelay` :158); `deliverDeleted` sink==nil → complete (:190-191); `deliverNotify` no rules → complete (:236); defaults poll 1000 ms / batch 32 / TTL 30 s / attempts 10 | ✅ |
| Notifier D2 dedupe — skip bus when `notify@1.1` outbox row exists (any status); comment names quarantine as current bus-only E14 path | `notifier.go:70-87` ✅ |
| `SystemContext` SubjectID = `"aero-vault-system"` (:16-21); `PrincipalSystem` bypass at `authorizer.go:23`; AV job runs under `SystemContext` (`workers.go:33`) | ✅ |
| `GetObjectByID` has **no** `deleted_at` filter (`sql_objects.go:188-199`); `SoftDeleteObjectByID` tx body :42-63; **no ByID WithEvent variant** in `repository_interface.go:31-32` | ✅ |
| `BuildDeletedFact(obj, actor, requestID, tenant)` / `BuildNotifyFact(obj, actor, requestID, tenant, sequencer)` — exactly **2 production call sites** (`file_delete.go:134,140`); `newSequencer` injectable (:31-42); goldens byte-pinned (`schema_test.go:15,17`) | ✅ |
| `setupSvc` real sqlite repo + `FileService` + local storage (`antivirus_test.go:50-73`); jobs composition (`NewQueue` :76, `NewPool` :120, `Pool.Run` :141, `Enqueue` dedupe `(id, deduped, err)` :91-103); tests are internal package `antivirus` | ✅ |

**No additional gaps found.** One behavioral consequence the spec implies but does not state explicitly is captured in §3 C-5 (notification delivery path switch) — it is automatic via the existing D2 skip, needs no code beyond FR-2.

---

## 2. API changes

### 2.1 Wire-level (protocol/config) — **none**

No REST/S3/MCP/WebDAV routes, no OpenAPI diff, no SSE frames, no env vars, no flags. Payload `schema_version` stays `"1.1"`.

### 2.2 Go-level API (complete breakage surface = 4 source files + 1 interface + builders)

**A. `internal/repository/repository_interface.go` + `internal/repository/event_outbox.go`** — new method (interface + `sqlStore` impl, sqlite/postgres via `rebind`, I1):

```go
SoftDeleteObjectByIDWithEvent(ctx context.Context, id int64, entry AuditEntry, facts []OutboxFact) error
```

Tx body = mirror of `SoftDeleteObjectByID` (`sql_objects_maint.go:42-63`) + `insertAuditEntry` + `insertOutboxFacts`:

1. `BeginTx`
2. `validateOutboxFacts(facts)` — **inside the tx** (deliberate divergence from the keyed variant which validates pre-tx; see D-2)
3. `SELECT tenant_id, bucket, key FROM objects WHERE id=$1 AND deleted_at IS NULL` → `ErrNotFound` on no row
4. `UPDATE objects SET deleted_at=$1 WHERE id=$2 AND deleted_at IS NULL` → zero rows → `ErrNotFound` (rolls back, **no phantom facts/audit** — GAP-4 parity)
5. `deleteObjectAccessState(ctx, s, tx, tenant, bucket, key)` → `insertAuditEntry` → `insertOutboxFacts` → `Commit`

Placement: `event_outbox.go`, next to the keyed WithEvent variants. Plain `SoftDeleteObjectByID` **untouched** (reconcile and any other callers keep it).

**B. `internal/service/object_worker.go`** — `QuarantineObjectByID` gains a parameter and swaps the repo call:

```go
func (s *FileService) QuarantineObjectByID(ctx context.Context, objectID int64, signature string) error
```

- Tombstone branch, `DeletedAt` guard, `preflightQuota`, chunkCleaner warn-only: **unchanged** (tombstone → `DeleteVersion`, still no facts — spec §6).
- New helpers (mirroring `file_delete.go` shapes, placed beside them):
  - `quarantineAuditEntry(ctx, obj)`: `Action=AuditActionFileDelete`, `Actor` = principal `SubjectID` ("" legal), `Target=bucket/key`, `TenantID=obj.TenantID`, `Detail=quarantineReason` (D-4).
  - `quarantineFacts(ctx, obj, signature)`: deleted@1.1 via `BuildDeletedFact(obj, actor, requestID, tenant, quarantineReason)`; notify@1.1 via `BuildNotifyFact(obj, actor, requestID, tenant, "", signature)` (`""` sequencer → `newSequencer()` inside builder). `requestID = middleware.RequestIDFrom(ctx)` ("" in job context, legal).
- Swap `repo.SoftDeleteObjectByID` → `repo.SoftDeleteObjectByIDWithEvent(ctx, obj.ID, entry, facts)`. `addTenantUsage` + `s.emit(EventDeleted)` stay **after** commit, unchanged (usage failure does not suppress committed facts — spec FR-2.4).

**C. `internal/antivirus/worker.go`**:

```go
const SystemActor = "system:antivirus"
type ObjectController interface {
    SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error
    QuarantineObjectByID(ctx context.Context, objectID int64, signature string) error  // +signature
}
```

- `ScanObjectByID`: both controller calls run under `access.WithPrincipal(ctx, access.Principal{SubjectID: SystemActor, TenantID: obj.TenantID, Kind: access.PrincipalSystem})`; the quarantine call passes `res.Signature` (already in hand at :149 — E7). New import `internal/access` (no cycle: antivirus already imports `repository`; access imports repository only).
- `cmd/server/workers.go:33` outer `SystemContext` **left unchanged** — the inner wrap replaces the context principal for controller calls only (D-5).

**D. `internal/events/payload.go`** — additive optional fields + variadic params (D-1):

```go
type deletedFact struct { … Actor string `json:"actor"`; Reason string `json:"reason,omitempty"` }
type notifyFact  struct { … Records []notifyRecord `json:"records"`; Signature string `json:"signature,omitempty"` }
func BuildDeletedFact(obj repository.Object, actor, requestID, tenant string, reason ...string) []byte
func BuildNotifyFact(obj repository.Object, actor, requestID, tenant, sequencer string, signature ...string) []byte
```

`file_delete.go:134,140` (REST path) compiles **unchanged**; `schema_test.go` **zero edits** (hard constraint, AC-3).

**Compile-breakage audit:** `QuarantineObjectByID` call sites = `worker.go:149` + definition only; **no test mocks implement `ObjectController`** (`grep` over `*_test.go` → none). Builders: 2 production call sites + tests, all compatible via variadic.

---

## 3. Compatibility constraints

| # | Constraint | Mechanism |
|---|-----------|-----------|
| C-1 | **Payload byte-compatibility (hard):** REST-path goldens in `schema_test.go:15,17` must stay byte-identical; schema stays 1.1 | `omitempty` fields appended **after** `actor` / `records` (JSON emits in struct order); `validOutboxPayload` only checks `schema_version=="1.1"` — unaffected |
| C-2 | **Zero wire-level change** | §2.1 |
| C-3 | **Authz behavior unchanged:** `PrincipalSystem` bypasses `authorizeObject` (`authorizer.go:23`) — wrapping in `WithPrincipal` changes only the audit `actor` string, never the decision | FR-4 |
| C-4 | **Other delete producers unaffected:** REST path untouched (variadic builders); plain `SoftDeleteObjectByID` kept; tombstone/DeleteVersion, delete-marker, bucket cascade remain bus-only (D2 comment E14) | §2.2 |
| C-5 | **Quarantine notification delivery switches path** (automatic, zero extra code): D2 skip engages because quarantine now writes `notify@1.1` rows → bus→Notifier path skipped, relay delivers from self-contained payload. Consequence: latency ≤ poll interval (default 1 s) instead of in-process immediate; delivery becomes **durable** (retry ×10, backoff); payload gains `signature`. No rules → relay completes silently, zero network. **This is a feature, but operators with bucket-notification rules should know it** | `notifier.go:70-87` + relay |
| C-6 | **Local subscribers unaffected:** `s.emit(EventDeleted)` still broadcasts → indexer/webhook/SSE/replication consume exactly as today (webhook is a bus subscriber, not the Notifier) | §2.2 B |
| C-7 | **Idempotency contract preserved:** queue dedupe `virus_scan:<object_id>` (prevents double-enqueue) + service `DeletedAt` guard (prevents double-execute). **No** `UNIQUE(event_type, origin_id)` — `RestoreObject` reuses row ids (D1), a unique key would swallow the second delete's facts | S7 |
| C-8 | **I1/I2/I4/I5/I6:** new SQL via `rebind`, no migrations, middleware chain untouched, no flag changes, stdlib only | — |

---

## 4. Failure modes

| # | Trigger | Observable | Behavior | Design response |
|---|---------|-----------|----------|-----------------|
| FM-1 | `validateOutboxFacts` failure (programming error: payload > 1 MiB, `schema_version` ≠ 1.1, empty facts) | tx rolled back; object **not** deleted; tags stay `infected`; job `failed` after `MaxAttempts` | Error returned to job handler → Pool retry/backoff → terminal `failed` | Validation inside tx (D-2) makes AC-1 rollback literal; job-failure telemetry is the alert surface. Malformed facts are impossible from FR-2 code (constants + `res.Signature`), so this is a guard, not an expected path |
| FM-2 | DB error mid-tx (disconnect, lock) | rollback, job retry with backoff | Same as today's `SoftDeleteObjectByID` failure | Unchanged semantics; outbox/audit/delete all-or-nothing |
| FM-3 | Zero-row race: concurrent quarantine of same id | `ErrNotFound` → job retry → next `GetObjectByID` reads `deleted_at` → `nil` no-op | Idempotent by construction; no phantom rows (GAP-4) | AC-1c asserts no new rows |
| FM-4 | `addTenantUsage` failure **after** commit | job error → retry → `DeletedAt` guard → `nil`; quota decrement lost | **Pre-existing semantics** (three-transaction design); outbox facts already committed and **must not** be suppressed (spec FR-2.4) | Documented, unchanged; not a regression |
| FM-5 | `insertAuditEntry`/`insertOutboxFacts` failure | whole tx rolls back → delete not performed → job retries | Stricter than today (today quarantine never audits); mirrors REST WithEvent path | Intended (FR-1); surfaced via job failure |
| FM-6 | Relay delivery failure for `notify@1.1` (target down) | durable retry → terminal `failed` + `last_error`; `deleted@1.1`/`audit_log` unaffected | Existing relay machinery; L0 `audit_log` is authoritative | No new code |
| FM-7 | `ChunkCleaner` failure | warn log only, delete proceeds | Unchanged (AGENTS.md §2.1 ③) | — |
| FM-8 | Actor resolution: caller context has no principal | `actor=""` in facts/audit | Legal (existing convention); FR-4 makes this unreachable on the quarantine path (worker always injects `SystemActor`) | AC-1 asserts `system:antivirus` |

---

## 5. Migration steps

1. **DB:** none — `0041_event_outbox` (+ `event_outbox_delivered`) exists in both dialects; `origin_id` has no FK and accepts any `objects.id` (I2: no new migration files, no edits to applied ones).
2. **Config/env:** none — relay already always starts (`workers.go:63`); defaults poll 1 s / batch 32 / TTL 30 s / attempts 10.
3. **Deploy:** single code rollout. Behavior activates at first commit: every subsequent quarantine writes 1 audit row + 2 outbox facts atomically with the soft delete.
4. **No backfill:** quarantines that happened before deploy are not retro-audited (documented limitation; reconcile does not touch outbox).
5. **Rollback:** revert the commit. Pending outbox rows from quarantines made while the feature was live drain harmlessly via the always-on relay (delivered → pruned by existing `PruneEventOutbox`); delivered facts are append-only history. No data cleanup required.
6. **Ops notes:** with bucket-notification rules, quarantine notifications now arrive via relay (durable, ≤ poll-interval latency) and carry `signature`; `admin` audit queries start showing `actor=system:antivirus` `action=file.delete` rows (the feature's purpose). Existing relay/job telemetry covers observability; no new metrics.

---

## 6. Testable acceptance mapping

Test package `antivirus` (internal, `setupSvc` base), `repository`, `events`; assertions via `testing` only (I6). `make check` gates all.

| AC | Test (name + file) | Assertions |
|----|--------------------|-----------|
| **AC-1a** (positive atomicity) | `TestScanObjectByIDQuarantineWritesAuditAndOutbox` — `antivirus_test.go`; EICAR put → `ScanObjectByID(ctx, obj.ID)` with `quarantine=true`, `ctx=context.Background()` | `event_outbox` has exactly **2 pending** rows for `origin_id=obj.ID`: deleted@1.1 payload JSON contains `"actor":"system:antivirus"` + `"reason":"av_infected"`; notify@1.1 payload contains `"signature":"EICAR-Test-File"`. `audit_log` exactly **1** row: `action=file.delete`, `actor=system:antivirus`, `detail` contains `av_infected`. `deleted_at` non-null; quota zeroed (existing asserts kept) |
| **AC-1b** (forced rollback) | `TestSoftDeleteObjectByIDWithEventValidationRollback` — `event_outbox_test.go`; seed an object; call with a fact whose payload has `"schema_version":"2.0"` (fails `validOutboxPayload`) | error returned; `objects.deleted_at` still NULL; `event_outbox` count for origin = **0**; `audit_log` count = **0** (validation runs inside the tx, D-2) |
| **AC-1c** (double-delete guard) | `TestSoftDeleteObjectByIDWithEventAlreadyDeleted` — `event_outbox_test.go`; soft-delete once, call again with valid facts | `ErrNotFound`; no new outbox/audit rows |
| **AC-2a** (dispatcher-stopped job) | `TestQuarantineJobCompletesWithoutRelay` — `antivirus_test.go`; composition: `setupSvc` + real `jobs.Queue`/`Registry`/`Pool`(workers=1) + `Worker.WithObjectController(svc)`; **no relay constructed**; EICAR → `Enqueue` (`DedupeKey=virus_scan:<id>`) → short `Pool.Run` | job `done`, `attempts==1`; outbox 2 rows **still pending** (durable); `jobs` has no `failed` row |
| **AC-2b** (relay drain no reentry) | `TestRelayDrainDoesNotReenterJobs` — `antivirus_test.go`; after AC-2a, construct `events.NewEventOutboxRelay(repo, logger, opts)` with small poll; short `Run` | both rows terminal (`delivered`/`failed`; no notification rules → complete, zero network); job `attempts` unchanged; no new `virus_scan` rows; `audit_log` count unchanged (L0 authoritative) |
| **AC-3a** (quarantine golden bytes) | `TestQuarantineFactGoldenBytes` — `antivirus_test.go`; fixed inputs (object ID 42 / `version_id v-abc` / injected `request_id` + sequencer via `newSequencer` swap) | deleted@1.1 and notify@1.1 payloads byte-equal golden constants including `"reason":"av_infected"`, `"signature":"EICAR-Test-File"`, `"actor":"system:antivirus"`, `"schema_version":"1.1"` |
| **AC-3b** (REST goldens untouched) | existing `events/schema_test.go` — **zero edits** | passes unchanged; proves additive fields invisible to REST path |
| **AC-4** (composition e2e + idempotency) | `TestQuarantineCompositionE2E` — `antivirus_test.go`; real repo + FileService + `SignatureScanner` + Queue/Pool + relay; sqlite+local FS, zero network; EICAR → `svc.Put` → enqueue (dedupe key) → pool | poll jobs: `done`, `attempts==1`; poll audit: exactly 1; poll outbox: exactly 1 deleted@1.1 (contains `av_infected`) + 1 notify@1.1 (contains `EICAR-Test-File`). **Repeat delivery:** re-run handler with same id → `DeletedAt` guard no-op → outbox/audit counts unchanged. Relay drain → terminal; no duplicate audit |

---

## 7. Decisions taken where the spec left freedom

| # | Decision | Rationale |
|---|----------|-----------|
| D-1 | Builders extended with **variadic** `reason ...string` / `signature ...string` (rejected: explicit params) | Spec hard-constrains `schema_test.go` to zero edits (AC-3); explicit params would force call-site churn there. Exactly 2 production call sites; empty default = the documented legal empty value. Quarantine is the only non-empty caller; AC-1a/AC-3a pin its non-emptiness |
| D-2 | `validateOutboxFacts` **inside** the tx in `SoftDeleteObjectByIDWithEvent` (keyed variants validate pre-tx) | FR-1 text says in-tx; makes AC-1b literally exercise the tx rollback path (BeginTx → validation failure → deferred Rollback) |
| D-3 | `quarantineAuditEntry`/`quarantineFacts` as new helpers next to `deleteAuditEntry`/`deleteFacts` (rejected: parameterizing the existing ones) | Zero churn on the REST path; `Detail="av_infected"` differs from `"soft"/"hard"` vocabulary — a flat detail string, action vocabulary stays flat (AGENTS.md audit contract) |
| D-4 | `const quarantineReason = "av_infected"` in `internal/service` (used for both `deletedFact.Reason` and audit `Detail`) | Single source of truth for the reason vocabulary's first entry; spec FR-2.2 |
| D-5 | `workers.go:33` outer `SystemContext` left as-is; inner `WithPrincipal(SystemActor)` wins for controller calls | Minimal diff; both are `PrincipalSystem` → authz identical; audit actor becomes `system:antivirus` exactly on the quarantine path |
| D-6 | No new metrics/telemetry | Relay + job telemetry already exist (S6); outbox rows, audit rows and job status are queryable in tests and ops alike |

---

## 8. Out of scope (spec §6, unchanged)

AuthorizationProvider port/fail_closed; delete-race terminal-state skip; tombstone `DeleteVersion` branch; REST delete path; L2 AuditSink adapters; outbox schema/dedupe-key changes (`UNIQUE(event_type, origin_id)` explicitly rejected — D1/RestoreObject row-id reuse).

**VERDICT: PASS** — all 8 evidence citations and all 8 material findings independently re-verified against HEAD (immaterial line nits only: E6 ±7 lines, E1/E3 ±1); the design reuses the existing outbox/relay/audit machinery wholesale with a 4-file Go API surface, zero migrations, zero wire-level changes, and every acceptance criterion mapped to a concrete test in existing packages with `testing`-only assertions.
