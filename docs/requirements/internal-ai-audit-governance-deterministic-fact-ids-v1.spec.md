# Requirements Specification — deterministic fact IDs across the three constructors (B3.3 / G4 / T-4)

**Module:** `internal/ai` (analysis label; implementation surface is `internal/auditgovernance` + `internal/repository` — see §1)
**Direction:** "Deterministic fact IDs across the three constructors (B3.3 / T-4)" (direction 1 of `internal-ai-99180452.json`)
**Source analysis:** `docs/auto/analyses/internal-ai-99180452.json`
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 9 / risk reduction 8 / effort 6 / confidence 9

---

## 1. Module & scope

The analysis file labels this direction under `internal/ai`; **no cited evidence or required change lives in `internal/ai/`** (verified: `grep -rn "AuditGovernance\|governanceWire\|factFrom" internal/ai/` → no hits). The audit-governance relay implementing the contract lives in `internal/auditgovernance` (fact redaction/construction, relay/reconcile, HTTP publisher) and `internal/repository` (outbox writes, gap listing, migrations). The module label is retained for traceability; all requirements target those two packages.

**Problem (as cited by the direction):** `factFromAudit` and `factFromEvent` assign `uuid.NewString()` at construction time, and `factFromGap` regenerates a fresh UUID on the reconcile path — so the same underlying fact receives a different `event_id` (and ledger idempotency key, since `governanceWire` sets `IdempotencyKey: fact.ID`) depending on whether it arrived via atomic capture or gap reconcile. The direct path computes the ID before `origin_id` is even known (`origin_id` is assigned post-INSERT via RETURNING in `RecordAuditWithGovernance`/`InsertEventWithGovernance`), so B3's `SHA-256(source|tenant|event_type|origin_kind|origin_id|time_bucket)[:32]` needs ID computation moved after origin assignment (or reconstructed in `factFromGap` from the gap's known `origin_id` + `occurred_at` bucket) for the two paths to converge. Nothing deterministic exists today.

**In scope:** ① a single exported deterministic-ID function in `internal/repository`, applied by the repository's three write methods after `origin_id` assignment **and** by `factFromGap` (both mechanisms the direction names, one formula), ② canonicalization of `OccurredAt` to the durably stored origin `created_at` (a required convergence precondition — §E8), ③ removal of `uuid.NewString` from `internal/auditgovernance/facts.go` (the three constructors stop minting IDs; a transient `SourceID` field carries the per-tenant source into the formula), ④ tests pinning T-4 (capture-vs-gap ID equality for both origins, restart stability, `uuid`-free facts.go, same-ID dedupe). **Out of scope:** B3-1 (permanent-error classification), B3-2 (`Ready()` decoupling), B3-4 (relay telemetry — direction 3 of the same analysis), B3-5/B3-6, any schema change (no migration: dedupe stays `ON CONFLICT (origin_kind, origin_id) DO NOTHING`, §REQ-6), sink-side verification (`QueryEvents 1 行` runs in the snaplink repository), claim-token `uuid.NewString` in `relay.go:62` (distinct concern — tokens are per-claim random, not fact identity).

---

## 2. Evidence verification

Every citation in the direction was checked against this checkout (HEAD `acfaaf4`).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `facts.go:13,30,48` — "uuid.NewString in all three constructors" | `factFromAudit` starts `facts.go:13`, `ID: uuid.NewString()` at `:22`; `factFromEvent` starts `:30`, `ID: uuid.NewString()` at `:39`; `factFromGap` starts `:48` — **no direct `uuid` call**, but it delegates to `factFromEvent` (`:55`) / `factFromAudit` (`:65`) and only then sets `fact.OriginID = gap.OriginID` (`:56`, `:66`) | ✅ **exact in effect** — all three constructors emit a fresh UUID; a gap re-mints a new ID on every reconcile pass. Line refs point at function starts; the `uuid` calls are at `:22`/`:39`. |
| E2 | `repository.go:32,43` — direct path computes the ID before origin_id is known | `RecordAudit` `repository.go:32`: `fact := r.runtime.redactor.factFromAudit(entry, time.Now().UTC())` → `RecordAuditWithGovernance(ctx, entry, fact)` `:33`; `InsertEvent` `:43-44` likewise | ✅ **exact.** |
| E3 | `audit_governance_write.go:28,70` — "origin_id assigned after fact construction" | `RecordAuditWithGovernance` `write.go:14-43`: `row.Scan(&fact.OriginID)` at `:28` (INSERT … `RETURNING id` `:24-27`), `fact.OriginKind = AuditOriginAdmin` `:32`; `InsertEventWithGovernance` `:45-93`: `fact.OriginKind, fact.OriginID = AuditOriginFile, id` at `:70` (RETURNING id `:66-69`) | ✅ **exact.** |
| E4 | `http.go` `governanceWire` — "IdempotencyKey: fact.ID" | `governanceWire` `http.go:142-153`: `EventID: fact.ID` `:148`, `IdempotencyKey: fact.ID` `:153`; wire `SourceSystem` = `binding.sourceID` (`:111`), where `sourceID = tenantSourceID(binding.TenantID)` (`:51`); `receiptMatches` requires `receipt.EventID == fact.ID` (`:216-217`) | ✅ **exact** — a regenerated ID ⇒ sink sees a new idempotency key ⇒ double-ledgering. |
| E5 | B3 formula `SHA-256(source\|tenant\|event_type\|origin_kind\|origin_id\|time_bucket)[:32]` | Formula stated verbatim in `docs/campaigns/campaign-aero-vault-b3.yaml` (B3-3) and `docs/campaigns/implementation-gate.md` (B3-3, T-4 "再生成 → sink Duplicate"); **input mapping unverified in the contract** — `docs/proposals/audit-contract-batch-aero-vault.md:15` flags "time_bucket 粒度" and "动作串 mismatch" as 未验证, and `internal/repository/audit_governance_types.go:36-54` `AuditGovernanceFact` has no `event_type`/`time_bucket` fields | ⚠️ **formula pinned by the campaign; input mapping is a spec decision** — §D3/D4 fix `event_type→Action`, `time_bucket→occurred truncated to seconds`. |
| E6 | Dedup "ON CONFLICT DO NOTHING" | `insertAuditGovernanceResult` `write.go:126-158`: `ON CONFLICT (origin_kind,origin_id) DO NOTHING` at `:140` (only when `ignoreDuplicate` — set by `EnqueueAuditGovernance` `:96-117`, the gap path); conflict target is the **origin tuple**, not the ID; outbox `UNIQUE (origin_kind, origin_id)` in `migrations/sqlite/0039_audit_governance_outbox.up.sql:23` | ✅ **present** — with deterministic IDs, same origin ⇒ same ID, so origin-tuple dedupe and ID dedupe coincide (see REQ-6). |
| E7 | "Nothing deterministic exists today" | `grep -rn "sha256\|SHA-256\|NewString" internal/auditgovernance/facts.go` → no hits; only `uuid` (E1). `relay.go:186` uses `sha256` for backoff jitter only | ✅ **holds.** |
| E8 | (unverified in the direction, required precondition) event `created_at` precision drift | Service `emit` `internal/service/file.go:310-330` never sets `Event.CreatedAt`; `factFromEvent` then uses `now.UTC()` (ns) at `facts.go:31-33`, while the origin row's `created_at` is filled by DB defaults — sqlite `strftime('%Y-%m-%dT%H:%M:%fZ','now')` (ms) `migrations/sqlite/0003_events.up.sql:10`, postgres `TIMESTAMPTZ NOT NULL DEFAULT now()` (µs) `migrations/postgres/0003_events.up.sql:9`. `InsertEventWithGovernance`'s INSERT omits `created_at` (`write.go:60-69`) | ✅ **real drift** — gap path parses the stored value (`flexTime`, `sql_helpers.go:196-231`; gap listing `write.go:262`), so atomic `occurred` ≠ gap `occurred` ⇒ bucket divergence ⇒ T-4 fails without canonicalization (REQ-2). Audit path has no drift: `auditedRepository.RecordAudit` always sets `entry.CreatedAt` first (`repository.go:29-31`), and `audit_log.created_at` is explicit TEXT in both dialects. |
| E9 | Failed-prune re-enqueue must stay ID-identical | `CleanupFailedAuditGovernance` `audit_governance_cleanup.go:104-141` writes **no origin tombstone** (comment `:104-108`: "a failed row's origin was never ledgered, so a later mutation of the same origin may enqueue a fresh fact") ⇒ after the 7d retention prune the gap resurfaces and is re-enqueued — a fresh UUID today would re-ledger at the sink; a deterministic ID folds to sink Duplicate (T-4) | ✅ **holds — the re-enqueue path is the T-4 scenario.** |
| E10 | Validation order permits repository-assigned IDs | `validAuditGovernanceFact` requires `fact.ID != ""` (`write.go:148-159`, `validAuditGovernanceIdentity` `:161-164`) but runs inside `insertAuditGovernanceResult`, i.e. **after** origin assignment in all three write methods (`write.go:28→37`, `:70→76`, `:112`) | ✅ **exact** — assigning the ID just before insert passes validation; constructors may leave `ID` empty. |
| E11 | Source is deterministically computable at construction | `tenantSourceID` `redaction.go:43-50` = `"aero-vault." + HMAC-SHA256(key, tenant)` — pure function of (key, tenant), available to the redactor (`newRedactor` `:21-26` requires key ≥32 bytes); identical to the wire's `SourceSystem` (`http.go:51,111`) | ✅ **exact** — the formula's `source` input can be computed inside the three constructors and threaded to the repository. |
| E12 | Existing tests unaffected | `audit_governance_test.go:40-41` / `audit_governance_pending_idx_test.go:222-224` build facts with `uuid.NewString()` but never assert ID values — a deterministic overwrite in the write methods keeps them green; `http_test.go` drives `Publisher` with hand-built facts (explicit IDs, `:206-225`) — bypasses the store, unaffected; `runtime_test.go:52` harness (poll 10 ms, backoff 1→2 s, retention 3600 s) asserts delivery counts, not IDs | ✅ **no existing test pins a fact ID or occurred precision.** |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "factFromAudit and factFromEvent assign uuid.NewString() at construction time" | ✅ exact (E1). |
| "factFromGap regenerates a fresh UUID on the reconcile path" | ✅ exact — via delegation (`relay.go:38` reconcile → `factFromGap` → `factFromEvent`/`factFromAudit` → new UUID every poll cycle). |
| "same underlying fact receives a different event_id … depending on atomic capture vs gap reconcile" | ✅ holds by construction (E1/E2/E3/E4). |
| "origin_id is assigned post-INSERT via RETURNING" | ✅ exact (E3). |
| "B3's SHA-256(…)[:32] needs ID computation moved after origin assignment (or reconstructed in factFromGap …) for the two paths to converge" | ✅ direction's two mechanisms are both implementable; REQ-1/REQ-3/REQ-4 implement **both** against one shared function. |
| "Nothing deterministic exists today" | ✅ (E7). |

---

## 3. Requirements

### REQ-1 — Single deterministic ID function (the formula, one definition)

New exported pure function in `internal/repository` (new file `internal/repository/audit_governance_factid.go`, ≤500-line gate safe; keeps `audit_governance_write.go` at 273 lines):

```go
func DeterministicFactID(source, tenant, eventType, originKind string,
    originID int64, occurredAt time.Time) string
```

- Hash input = canonical byte string of the six fields in **fixed order** with NUL separators: `source \x00 tenant \x00 eventType \x00 originKind \x00 decimal(originID) \x00 decimal(unixSeconds(occurredBucket))`, where `occurredBucket = occurredAt.UTC().Truncate(time.Second)`. NUL cannot occur in any field (`source` is `SourcePrefix + "." + base64url`, `tenant`/`action` are constrained by `normalizedTenant`/`safeAction` (`facts.go:70-74`, `:84-98`), the rest are digits) — unambiguous framing, no `|`-concatenation ambiguity.
- ID = `hex(SHA-256(frame))[:32]` — **first 32 hex chars of the hex digest** (128-bit), lowercase, per the direction's "32-hex SHA-256 ID".
- Pure: no randomness, no mutable state, no clock. Same inputs ⇒ same output in any process, any restart.

### REQ-2 — Occurred canonicalization (convergence precondition)

The ID's `time_bucket` must be computed from the value the origin row **durably stores**, because the gap path reconstructs occurred from that column (E8):

- `RecordAuditWithGovernance` (`write.go:14-43`): after the existing `entry.CreatedAt` defaulting (`:19-21`), set `fact.OccurredAt = parse(time.RFC3339Nano, entry.CreatedAt)` when the parse succeeds (keep `fact.OccurredAt` on parse error). The audit path stores `entry.CreatedAt` verbatim, so the parse is lossless.
- `InsertEventWithGovernance` (`write.go:45-93`): extend both dialect INSERTs from `RETURNING id` to **`RETURNING id, created_at`**; scan `created_at` into a `flexTime` (`sql_helpers.go:196-214` handles `time.Time` for postgres and `[]byte`/`string` for sqlite) and set `fact.OccurredAt` to the returned time. This absorbs the DB-default precision (sqlite ms / postgres µs) so the atomic path's occurred is byte-identical to what the gap path will parse.
- `factFromAudit`/`factFromEvent` fallback-to-`now` behavior (`facts.go:16-19`, `:31-33`) is unchanged — it only fires for zero input timestamps; REQ-2 makes the ID independent of which `now` fired.

### REQ-3 — The three repository write methods assign the ID after origin assignment

All three methods in `internal/repository/audit_governance_write.go` must compute `fact.ID` from the fact's **final** fields immediately before the outbox insert (overwrite any caller-set ID — the repository is the single authority, and the overwrite is idempotent with REQ-4's gap-path computation):

- `RecordAuditWithGovernance`: after `row.Scan(&fact.OriginID)` (`:28`) and REQ-2 canonicalization:
  `fact.ID = DeterministicFactID(fact.SourceID, fact.TenantID, fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)` — then `insertAuditGovernance` at `:37`.
- `InsertEventWithGovernance`: after `fact.OriginKind, fact.OriginID = AuditOriginFile, id` (`:70`) and REQ-2 canonicalization: same assignment — then `insertAuditGovernance` at `:76`.
- `EnqueueAuditGovernance` (gap path, `:96-117`): before `insertAuditGovernanceResult` (`:112`), recompute `fact.ID` from the fact's fields (origin already known from the gap).

`validateAuditGovernanceFact` (`:148-159`, incl. `validAuditGovernanceIdentity` `:161-164` with `fact.ID != ""` at `:162`) is evaluated inside `insertAuditGovernanceResult` — after assignment in all three paths (E10) — so the computed ID satisfies `fact.ID != ""` and no other validation changes.

### REQ-4 — Constructors stop minting UUIDs; `factFromGap` computes the ID; `SourceID` field

- Add a **transient** `SourceID string` field to `repository.AuditGovernanceFact` (`audit_governance_types.go:36-54`). It is **not persisted**: the outbox INSERT column list (`write.go:133-136`) and `auditGovernanceCols` (`claim.go:10-11`) are unchanged; claim-roundtripped facts carry `SourceID == ""` without behavioral impact (the publisher uses `binding.sourceID`, `http.go:111`, not the field).
- `factFromAudit` (`facts.go:13-27`) and `factFromEvent` (`:30-44`): **remove `ID: uuid.NewString()`** (`:22`, `:39`) — leave `ID` empty — and add `SourceID: r.tenantSourceID(tenant)` (deterministic per tenant, E11; equals the wire's `SourceSystem`). Drop the `github.com/google/uuid` import.
- `factFromGap` (`:48-68`): keep delegation; after setting `fact.OriginID` (`:56`, `:66`), compute
  `fact.ID = DeterministicFactID(fact.SourceID, fact.TenantID, fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)`
  — the direction's "reconstructed in factFromGap from the gap's known origin_id + occurred_at bucket" mechanism, calling the same single formula as REQ-3.
- **`grep -n "uuid" internal/auditgovernance/facts.go` → no hits.** (`relay.go:62` `claimToken := uuid.NewString()` is out of scope — it is per-claim auth, never an event identity.)

### REQ-5 — Formula input mapping (pinned)

| Formula input | Source | Convergence |
|---|---|---|
| `source` | `fact.SourceID` (per-tenant `tenantSourceID`, REQ-4; empty for direct store callers/tests ⇒ still deterministic, 32-hex) | identical on both paths (same tenant, same key) |
| `tenant` | `fact.TenantID` — already normalized (`normalizedTenant` `facts.go:70-74` on both paths; gap listing is per-tenant) | identical |
| `event_type` | `fact.Action` — the safeAction-normalized action (`facts.go:84-98`); admin: `entry.Action`, file: `"file."+event.Type`; gap re-derives the identical raw values from the origin row (`write.go:234`, `:264-265`) | identical (same fallbacks fire on the same raw values) |
| `origin_kind` | `fact.OriginKind` — set `:32`/`:70` (direct) or from `gap.OriginKind` (gap) | identical |
| `origin_id` | `fact.OriginID` — RETURNING id (direct) / `gap.OriginID` (gap) — same origin row PK | identical |
| `time_bucket` | `occurredBucket(fact.OccurredAt)` = UTC truncate to whole seconds, decimal Unix seconds — occurred canonicalized per REQ-2 to the stored origin `created_at` | identical (same stored value on both paths) |

### REQ-6 — Dedupe semantics: no schema change

- `ON CONFLICT (origin_kind, origin_id) DO NOTHING` (`write.go:140`; outbox `UNIQUE (origin_kind, origin_id)` at `migrations/sqlite/0039_audit_governance_outbox.up.sql:23`) remains the dedupe mechanism and target — **no migration** (I2: 0039 is applied and untouched).
- With deterministic IDs, same origin ⇒ same ID (REQ-1 inputs are all origin-derivable), so origin-tuple dedupe and ID dedupe coincide: a re-enqueued reconciled fact carries the same ID and is deduped. After the terminal-failed retention prune (no tombstone, E9), the re-created row has the same ID as pre-prune — the sink's idempotency key folds the re-delivery to Duplicate (T-4's `QueryEvents 1 行` is sink-side, out of scope here).

### REQ-7 — Tests

Follow existing harness patterns (`openGovernanceStore` `audit_governance_test.go:16-43`; `runtimeConfig` `runtime_test.go:40-47`; claim/fail/cleanup cycle from `runtime_test.go:117-186` / `relay_terminal_test.go`).

- **REQ-7.1 — T-4 core, admin origin (AC-1):** lifecycle test: (1) `redactor.factFromAudit` → `store.RecordAuditWithGovernance(ctx, entry, fact)` with an explicit `entry.CreatedAt`; (2) `ClaimAuditGovernance` → capture `claimed[0].ID`; (3) `FailAuditGovernance` (owner/token from the claim); (4) `CleanupFailedAuditGovernance(ctx, now.Add(time.Hour), 10)` (row pruned, no tombstone ⇒ gap resurfaces); (5) `ListAuditGovernanceGaps(ctx, tenant, 10)`; (6) `redactor.factFromGap(gap, now)` → assert `fact.ID == claimed[0].ID` **and** `fact.ID` matches `^[0-9a-f]{32}$`. Deterministic — no sleeps, no httptest.
- **REQ-7.2 — T-4 core, file origin (AC-1):** identical lifecycle with `InsertEventWithGovernance` and an event whose `CreatedAt` is **zero** (the production shape — service `emit` never sets it, E8) → same equality + format assertions. Proves REQ-2 canonicalization: without it, gap occurred (DB-default precision) ≠ atomic occurred (ns) and the assertion fails.
- **REQ-7.3 — Restart stability (AC-3):** (a) same tuple → `DeterministicFactID` returns identical output across repeated calls and across two independently opened `repository.Open` instances on the same DB file (fresh store objects = fresh process state; the derivation holds no state); (b) lifecycle variant: after step (4) of REQ-7.1, re-`EnqueueAuditGovernance` the gap fact, `ClaimAuditGovernance` again, assert the re-created row's ID equals the pre-prune ID.
- **REQ-7.4 — Grep asserts (AC-2):** `grep -n "uuid" internal/auditgovernance/facts.go` → no hits; `grep -n "uuid.NewString" internal/auditgovernance/` → only `relay.go:62` (claim token, out of scope). Implement as a small source-read test (os.ReadFile on `facts.go` via `runtime.Caller`-anchored path, mirroring the campaign's grep-consistency pattern) plus the documented gate check.
- **REQ-7.5 — Same-ID dedupe (AC-4):** enqueue the same gap-derived fact twice via `EnqueueAuditGovernance` → first `(true, nil)`, second `(false, nil)` (ON CONFLICT DO NOTHING), and both invocations carry the same 32-hex ID; existing `TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts` (`audit_governance_test.go:98`) must pass unmodified.
- **REQ-7.6 — Regression:** existing governance suite (`audit_governance_test.go`, `audit_governance_pending_idx_test.go`, `runtime_test.go`, `relay_terminal_test.go`, `relay_metrics_test.go`, `http_test.go`) passes unmodified (E12) — their facts now carry deterministic IDs and no test pins ID values.

---

## 4. Decisions & non-goals

- **D1 — One formula, two call sites.** The direction offers two mechanisms ("ID computation moved after origin assignment" / "reconstructed in factFromGap"); both are implemented against **one** exported pure function (`DeterministicFactID`, REQ-1) so convergence holds by construction and the acceptance test can assert `factFromGap`'s output directly (the direction's wording). The repository write methods overwrite unconditionally (REQ-3), making the store authoritative even for callers that pre-set an ID.
- **D2 — `source` = per-tenant source-system ID, threaded via a transient `SourceID` field.** Faithful to the formula's `source` input and identical to the wire's `SourceSystem` (E11); the repository alone cannot derive it (no redaction key), so the redactor computes it at construction. The field is deliberately **not persisted** (no schema change; claim round-trips carry `""` with zero behavioral impact).
- **D3 — `event_type` maps to `fact.Action`** (the normalized action string). `AuditGovernanceFact` has no `event_type` field (`audit_governance_types.go:36-54`); the wire carries `EventType: SchemaID` plus `Action: fact.Action` (`http.go:146-149`), so `Action` is the semantic event type. Marked as an adaptation — the contract text's field mapping was flagged unverified in `docs/proposals/audit-contract-batch-aero-vault.md:15`.
- **D4 — `time_bucket` = occurred truncated to whole seconds (UTC), decimal Unix seconds.** Granularity is a free choice for convergence (occurred is immutable per origin row — the bucket never changes for a given origin); seconds is the finest stable bucket that tolerates the dialect DB-default precision drift, which REQ-2 eliminates anyway. Unverified "time_bucket 粒度" from the proposal is hereby pinned.
- **D5 — Canonical NUL framing instead of raw `|` concatenation.** `|` framing is ambiguous across `tenant`/`action` values; NUL cannot appear in any field (REQ-1). The hex digest is truncated to the **first 32 hex chars** (128-bit), per the direction's "32-hex".
- **D6 — No schema change.** Dedupe stays `ON CONFLICT (origin_kind, origin_id) DO NOTHING` (REQ-6): deterministic IDs make origin-tuple dedupe and ID dedupe coincide, and I2 forbids touching 0039.
- **Non-goals:** B3-1/B3-2/B3-4/B3-6 (sibling directions of the B3 campaign), `relay.go` claim-token UUID, sink-side `QueryEvents` verification (snaplink repository), any migration, any configuration surface, any change to `internal/service` (`emit` stays as-is; REQ-2 absorbs the zero-`CreatedAt` shape), billing/events outbox parallels.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — Capture-vs-gap ID equality (T-4).** *Enqueue via `RecordAuditWithGovernance`, then force the gap path (bypass outbox row) and assert `factFromGap` yields the same 32-hex SHA-256 ID.*
*Testable (REQ-7.1/REQ-7.2):* claim → fail → retention-prune (`CleanupFailedAuditGovernance(now+1h)`, the supported no-tombstone bypass) → gap resurfaced → `factFromGap(gap).ID == claimed[0].ID`, both matching `^[0-9a-f]{32}$` — for the admin origin (explicit `CreatedAt`) **and** the file origin (zero `CreatedAt`, proving REQ-2).

**AC-2 — No `uuid.NewString` remains in facts.go.** *Assert no `uuid.NewString` remains in facts.go.*
*Testable (REQ-7.4):* `grep -n "uuid" internal/auditgovernance/facts.go` → no hits (import removed); source-read test fails CI if reintroduced; the only remaining `uuid.NewString` in the package is the claim token at `relay.go:62` (out of scope).

**AC-3 — ID stable across process restarts.** *Assert ID is stable across process restarts for the same (tenant, origin_kind, origin_id, time_bucket) tuple.*
*Testable (REQ-7.3):* `DeterministicFactID` is a pure function — identical output across repeated calls and across two independently opened repository instances on the same DB file (fresh store objects simulate restart state; the derivation holds no random/mutable state); the prune → re-enqueue cycle re-creates the outbox row with the **same** ID (REQ-7.3b).

**AC-4 — Same-ID re-enqueue is deduped.** *Assert re-enqueueing a reconciled fact with the same ID is deduped (ON CONFLICT DO NOTHING).*
*Testable (REQ-7.5):* `EnqueueAuditGovernance` twice on the same gap fact → `(true, nil)` then `(false, nil)` with identical 32-hex IDs on both calls; `TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts` passes unmodified; grep-pin the conflict target `(origin_kind, origin_id)` at `write.go:140` (no schema change).

---

## 6. Risks

- **Occurred drift between paths (the main correctness risk)** — closed by REQ-2: the ID is computed from the stored origin `created_at` (RETURNING / entry value), not from the constructor's `now` fallback. Residual: a hand-crafted unparseable `audit_log.created_at` would make the gap fall back to `now` (`facts.go:16-19`) and diverge; `created_at` is only ever written by this code as RFC3339Nano (E8), so this is theoretical — documented, not mitigated.
- **`SourceID` field drift** — claim round-trips return `SourceID == ""` (not persisted); the publisher already uses `binding.sourceID` (`http.go:111`), so nothing downstream can observe the field. If a future feature persists it, the formula must be re-validated — the field's comment will say "ID derivation only".
- **Key rotation changes IDs** — `source` derives from the redaction key (`tenantSourceID`, E11); rotating `AUDIT_GOVERNANCE_*` key changes every fact ID, so facts in flight lose sink idempotency. Pre-existing property of the wire's `SourceSystem`; unchanged by this spec (D2).
- **32-hex collision surface** — 128-bit truncation is standard idempotency strength; the outbox PK is `TEXT` and `(origin_kind, origin_id)` remains the hard uniqueness backstop (REQ-6).
- **Regression risk to the terminal-failed lifecycle** — REQ-7.1/7.2 reuse the exact claim→fail→prune cycle pinned by T-3 tests; no timing dependence (no sleeps, no wall-clock equality).
- **File-size gates** — `facts.go` 112 lines (shrinks), `audit_governance_write.go` 273 (≈+15), new `audit_governance_factid.go` ≈40: all ≤500 ✓; `make check` (gofmt/build/vet/test — SQLite + local FS, zero network) applies to the implementation.
