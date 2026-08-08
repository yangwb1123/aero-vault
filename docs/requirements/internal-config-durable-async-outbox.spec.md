# Requirements Specification — `internal/config`: durable-async event outbox section

**Module:** `internal/config`
**Direction:** "Add a dedicated durable-async outbox config section (enable flag, poll interval, batch size, claim TTL, retention, max attempts) for `vault.file.deleted@1.1` / `vault.file.notify@1.1`, parameterized on the proven billing outbox pattern"
**Source analysis:** `docs/auto/analyses/internal-config-a932ee1e.json` (direction 2)
**Status:** Spec (evidence-verified). Two of the six knobs are unshipped gaps; four are shipped and become regression guards.

---

## 1. Scope

Define the config surface for the deletion transactional outbox relay, parameterized on the billing outbox pattern (`BILLING_OUTBOX_*`). The section must expose exactly: **enable flag, poll interval, batch size, claim TTL, retention, max attempts**. HTTP timeout is already part of the shipped surface and stays (it is required to express the claim-TTL invariant).

Out of scope (see §5): reworking `BillingConfig`'s private outbox fields, a shared `OutboxConfig` struct, changes to the `event_outbox` table schema, changes to the `vault.file.deleted@1.1` / `vault.file.notify@1.1` payload schema, and new relay behavior beyond what the knobs parameterize.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| Cited symbol | Verified location | Verdict |
|---|---|---|
| `BillingConfig.OutboxPollMillis` / `OutboxBatchSize` / `ClaimTTLSeconds` | `internal/config/config_billing.go:27-29` | ✅ present |
| `loadBillingConfig` (env reads, defaults 1000/32/30) | `internal/config/config_billing.go:44`, `:53-55` | ✅ present |
| Billing bounds: batch `≤ 500`, `ClaimTTL > HTTPTimeout` | `internal/config/config_billing.go:140-141` | ✅ present |
| `EventsConfig` has only WebhookURL/WebhookSecret/Transport/TransportDSN/SubBufferSize | `internal/config/config_app.go:19-26` | ✅ present (true at analysis time) |
| `ClaimBillingUsage` | `internal/repository/billing_outbox.go:11` | ✅ present |
| `claimBillingUsagePostgres` with `FOR UPDATE SKIP LOCKED` | `internal/repository/billing_outbox.go:26`, `:36` | ✅ present |
| `runOutbox` (poll loop) | `internal/billing/outbox.go:14` | ✅ present |
| `billingBackoff` (1s base, 2×, 5 min cap, jitter) | `internal/billing/outbox.go:82` | ✅ present |
| `InsertEvent` | `internal/repository/sql_events.go:9` (exact) and `internal/repository/repository_interface.go:100` (citation said `:97` — off by 3; line 97 is `ListParts`, the `── Events ──` interface section follows) | ✅ present, minor line drift |
| Persist-then-broadcast (`Publish` → `InsertEvent` → `broadcast`) | `internal/events/bus.go:80`, `:84`, `:90` | ✅ present |

**The direction's problem statement is stale for the events side.** Since the analysis, an events outbox section and its full machinery shipped:

- `internal/config/config_event_outbox.go` — `EventOutboxConfig` (PollMilliseconds, BatchSize, ClaimTTLSeconds, HTTPTimeoutSeconds, MaxAttempts), `loadEventOutboxConfig()` (`EVENT_OUTBOX_POLL_INTERVAL_MILLIS` 1000 / `EVENT_OUTBOX_BATCH_SIZE` 32 / `EVENT_OUTBOX_CLAIM_TTL_SECONDS` 30 / `EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS` 5 / `EVENT_OUTBOX_MAX_ATTEMPTS` 10), `withDefaults()`, `Validate()`; wired into `config.go` `Load()` and `config_validate.go` `Config.Validate()`.
- `internal/repository/event_outbox.go` — `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`/`SoftDeleteObjectByIDWithEvent` (delete + audit row + facts in one transaction, rollback on any failure, `:102/:147/:186`), `ClaimEventOutbox` (`:251`, Postgres `FOR UPDATE SKIP LOCKED` `:266`), `CompleteEventOutbox` (`:336`), `RetryEventOutbox` (`:364`), `PruneEventOutbox` (`:393`), `HasEventOutboxFact` (`:437`); table `event_outbox` via migration `internal/repository/migrations/{sqlite,postgres}/0041_event_outbox.{up,down}.sql`.
- `internal/events/event_outbox_relay.go` — `EventOutboxRelay` claim→deliver→complete loop, `eventOutboxBackoff` (`:345`, mirrors `billingBackoff`), `prune()` (`:366`), `Run` (`:106`); wired at `cmd/server/workers.go:158` `startEventOutboxRelay`.
- Enqueue call site: `internal/service/file_delete.go:46/86` (hard/soft delete), facts built at `:123` `deleteFacts`; payload schema `internal/events/payload.go` (`schema_version 1.1`, byte-stable field order).
- Tests already covering the shipped behavior: `internal/repository/event_outbox_test.go` (one-tx atomicity `:71/:136/:398`, claim lifecycle `:220`, lease-expiry redelivery `:259`, retry→terminal failed `:300`, prune `:355`), `internal/events/event_outbox_relay_test.go` (delivery lifecycle `:144`, 5xx retry `:181`, claim-lost reclaim `:229`, deleted-fact completes without delivery `:272`, backoff bounds `:299`), `internal/events/schema_test.go` (golden JSON `:31`), `internal/service/file_delete_test.go` (`TestAdminDelete_EmitsExactlyOneDeletedFact` `:156`, `TestDeleteDenied_NoOutboxRow_ObjectUntouched` `:68`).

**Remaining gaps vs. the direction (the actual spec scope):**

| Direction knob | Shipped? | Evidence |
|---|---|---|
| Enable flag | ❌ **gap** | Relay is always started: `cmd/server/workers.go:150` "It always starts: deletion atomicity is not gated"; `config_event_outbox.go` comment "the relay always starts". No `EVENT_OUTBOX_ENABLED` anywhere (verified: absent from `config.go`, `.env.example`, `docs/configuration.md`). |
| Poll interval | ✅ | `EVENT_OUTBOX_POLL_INTERVAL_MILLIS` |
| Batch size | ✅ | `EVENT_OUTBOX_BATCH_SIZE` |
| Claim TTL | ✅ | `EVENT_OUTBOX_CLAIM_TTL_SECONDS` |
| Retention | ❌ **gap** | Hardcoded in relay: `eventOutboxDeliveredRetain = 24h`, `eventOutboxFailedRetain = 7*24h`, prune cadence `eventOutboxPruneEveryRounds = 60` (`event_outbox_relay.go:61-63`), passed to `PruneEventOutbox` at `:366`. No env surface. |
| Max attempts | ✅ | `EVENT_OUTBOX_MAX_ATTEMPTS` |

Also a documentation gap: `docs/configuration.md:354-358` documents `EVENT_OUTBOX_*`, but `.env.example` has **no** `EVENT_OUTBOX_*` entries (grep returns nothing; `BILLING_OUTBOX_*` is present at `.env.example:179-181`).

---

## 3. Requirements

### REQ-1 — Enable flag (`EVENT_OUTBOX_ENABLED`) — **GAP**

- `EventOutboxConfig` gains `Enabled bool`, loaded via `getEnvBool("EVENT_OUTBOX_ENABLED", true)` in `loadEventOutboxConfig()`.
- **Default `true`** — preserves the shipped always-on behavior; a default of `false` would silently stop delivery on every existing deployment. Rationale: the relay exists for core deletion atomicity; the flag is an ops kill-switch, not an opt-in.
- Semantics of `false`: `startEventOutboxRelay` (`cmd/server/workers.go:158`) does not start the relay loop (claim/deliver/complete **and** the in-loop prune); it logs `"event outbox relay disabled"`. The transactional enqueue inside `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` is **unchanged** — the delete transaction still writes both facts (deletion atomicity is not gated; a disabled relay never blocks or alters the business flow). Rows accumulate until re-enabled, at which point the backlog drains in `created_at_ns` order.
- No cross-validation required: the flag is independent of the five numeric knobs (invalid numeric knobs are rejected regardless of `Enabled`, since `Config.Validate()` validates unconditionally today — keep that).

### REQ-2 — Poll interval — **SATISFIED (regression guard)**

`EVENT_OUTBOX_POLL_INTERVAL_MILLIS`, default `1000`, bounds `1..60000` (`config_event_outbox.go`). Flows to `EventOutboxRelayOptions.PollInterval` via `workers.go:160`. No change.

### REQ-3 — Batch size — **SATISFIED (regression guard)**

`EVENT_OUTBOX_BATCH_SIZE`, default `32`, bounds `1..500` (mirrors billing `config_billing.go:141`). No change.

### REQ-4 — Claim TTL — **SATISFIED (regression guard), with a stricter invariant than billing**

`EVENT_OUTBOX_CLAIM_TTL_SECONDS`, default `30`, bounds `1..600` and **must exceed `2 × EVENT_OUTBOX_HTTP_TIMEOUT_SECONDS`** (`config_event_outbox.go` `Validate()`). The direction's phrasing "claim TTL > HTTP timeout" (the billing rule, `config_billing.go:141`) is implied by the shipped `> 2×` rule, which additionally prevents concurrent duplicate POSTs from a slow target plus lease expiry (documented D7 invariant). **Do not relax the event rule to the billing rule** during any shared-config refactor. No change.

### REQ-5 — Retention — **GAP**

- `EventOutboxConfig` gains two knobs matching the two existing prune horizons:
  - `EVENT_OUTBOX_DELIVERED_RETENTION_HOURS`, default `24` — rows with `status='delivered'` older than this are pruned (`PruneEventOutbox` deletes by `delivered_at_ns`).
  - `EVENT_OUTBOX_FAILED_RETENTION_HOURS`, default `168` (7 days) — rows with `status='failed'` older than this are pruned (by `created_at_ns`).
- Bounds: **both must be positive** (acceptance: "positive retention") and `≤ 8760` (1 year); `0` is rejected, not interpreted as "disable" — table growth must stay bounded.
- Wiring: `EventOutboxRelayOptions` gains `DeliveredRetain`/`FailedRetain time.Duration`; `cmd/server/workers.go` passes the configured hours; `NewEventOutboxRelay` zero-falls back to the current constants (24h/168h), keeping the package constants as fallback defaults. `prune()` (`event_outbox_relay.go:366`) then passes the configured cutoffs. No repository change: `PruneEventOutbox(ctx, deliveredBefore, failedBefore)` already takes both cutoffs.
- Prune cadence (`every 60 rounds`, i.e. ≈1 min at default poll) stays derived from the poll interval — **not** a new knob (scope discipline; it bounds prune overhead, not data retention).

### REQ-6 — Max attempts — **SATISFIED (regression guard)**

`EVENT_OUTBOX_MAX_ATTEMPTS`, default `10`, bounds `1..1000`; a fact reaching the cap is set terminal `failed` (covered by `TestEventOutboxRetryBackoffAndTerminalFailed`, `event_outbox_test.go:300`). No change.

### REQ-7 — Validation & defaults unit surface — **GAP (tests)**

- New `internal/config/config_event_outbox_test.go` (stdlib `testing` only, per AGENTS.md I6), following `config_billing_test.go` patterns (`t.Setenv` + `loadEventOutboxConfig` + `Config.Validate`):
  - Defaults: no `EVENT_OUTBOX_*` env → `Enabled=true`, poll `1000`, batch `32`, TTL `30`, HTTP timeout `5`, max attempts `10`, delivered retention `24`, failed retention `168`.
  - Bounds rejected by `Load()`: poll `0`/`60001`, batch `0`/`501`, TTL `≤ 2×timeout` and `> 600`, timeout `0`/`30`, attempts `0`/`1001`, delivered/failed retention `0`/`8761`.
  - `withDefaults()` fills a hand-built zero config to the same defaults (existing contract, `config_validate.go` `Config.Validate`).
- Config surface stays within the 500-line file limit (current `config_event_outbox.go` is 78 lines; adding two fields + two bound checks keeps it well under).

### REQ-8 — Documentation alignment — **GAP**

- `.env.example` gains the `EVENT_OUTBOX_*` block (all seven vars incl. `EVENT_OUTBOX_ENABLED` and the two retention vars), mirroring the `BILLING_OUTBOX_*` block at `.env.example:179-181`.
- `docs/configuration.md:354-358` table updated: add `EVENT_OUTBOX_ENABLED` (default `true`) and the two retention rows; the existing "failed rows are pruned after 7 days, delivered rows after 24h" note becomes "defaults; see `EVENT_OUTBOX_*_RETENTION_HOURS`".

---

## 4. Decisions & non-goals

- **D1 — Shared `OutboxConfig` is rejected (documented decision).** The direction floats "a shared OutboxConfig consumed by both billing and the event outbox" as *proposed, not verified*. Verified divergence makes the merge net-negative:
  - Different claim-TTL invariant: billing `ClaimTTL > HTTPTimeout` (`config_billing.go:141`) vs. events `ClaimTTL > 2×HTTPTimeout` (`config_event_outbox.go`); a shared validator would have to special-case one consumer.
  - Different knob sets: events has `HTTPTimeout`/`MaxAttempts`/retention; billing has none of those; billing has `ProjectionIntervalSec`.
  - Different env prefixes (`BILLING_OUTBOX_*` vs `EVENT_OUTBOX_*`) and different `Validate()` gating (billing validates only when `Enabled`; events validates unconditionally).
  - A merge would either rename shipped env vars (breaking) or carry dual prefixes (a second private copy anyway). Keep the two private copies; factor a shared helper only if a third outbox consumer appears.
- **D2 — Enable-flag default `true`** (REQ-1): preserves shipped behavior; disabling is an explicit ops decision.
- **D3 — Retention split into delivered/failed horizons** rather than one value: the shipped prune already distinguishes the two (`PruneEventOutbox` takes two cutoffs); one knob would force one horizon to be derived from the other, which is less expressive for the same surface.
- **Non-goals:** no `event_outbox` schema change (prune predicates already keyed on `delivered_at_ns`/`created_at_ns`; `AUTOINCREMENT` id already guards against prune id-reuse); no payload/schema change (`vault.file.deleted@1.1` / `vault.file.notify@1.1` byte-exactness preserved); no billing knob changes.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**AC-1 — Unit: defaults + bounds validation (billing-mirrored).**
*Testable:* `config_event_outbox_test.go` — `loadEventOutboxConfig()` with clean env returns the §3 defaults (REQ-1..REQ-6, incl. `Enabled=true` and the two retention defaults); `Load()`/`Validate()` rejects each out-of-bounds value individually (REQ-7 list) with an error naming the offending env var; claim-TTL rule asserted as `> 2×HTTP_TIMEOUT` (satisfies the direction's "> HTTP timeout" and keeps the stricter shipped invariant); retention positive and `≤ 8760`.

**AC-2 — Outbox delivery: same-transaction enqueue, restart survival, backoff+jitter retry, terminal state at max attempts.**
*Already satisfied — regression guard.* *Testable:* `internal/repository/event_outbox_test.go` `TestDeleteObjectWithEvent_OneTx` (`:136`, delete + facts commit/rollback together; `TestHardDeleteAuditInsertFailure_RollsBack` `:758` for rollback), `TestEventOutboxClaimCompleteLifecycle` (`:220`), `TestEventOutboxRetryBackoffAndTerminalFailed` (`:300`), `TestEventOutboxClaimLeaseExpiryRedelivers` (`:259`); `internal/events/event_outbox_relay_test.go` `TestOutboxRelay_DeliveryLifecycle` (`:144`) and `TestOutboxRelay_RetriesOn5xx` (`:181`) with `TestEventOutboxBackoffBounds` (`:299`, jitter bounds `[0.75, 1.0)×base`, cap 5 min — the `billingBackoff` shape). Restart survival = rows are plain table state (`event_outbox`), not memory; re-claim after restart is the `ClaimEventOutbox` `pending`/`inflight` predicate. Run `make test` (SQLite baseline) plus `make test-race`.

**AC-3 — Event schema round-trip: `vault.file.deleted@1.1` JSON through the outbox table.**
*Already satisfied — regression guard.* *Testable:* `internal/events/schema_test.go` `TestEventSchema_GoldenJSON` (`:31`, byte-exact goldens), `TestEventSchema_Deleted11Envelope` (`:96`); repository stores `payload` as TEXT byte-exact (`0041_event_outbox.up.sql`, "TEXT (not jsonb) keeps bytes byte-exact") and validates `schema_version == "1.1"` at insert (`event_outbox.go` `validOutboxPayload`); relay delivers notify facts verbatim (`payload.go` comment "never re-derived at delivery time"); `internal/service/file_delete_test.go` `TestAdminDelete_EmitsExactlyOneDeletedFact` (`:156`).

**AC-4 — Composition e2e: response-before-delivery and crashed-worker reaping.**
*Already satisfied — regression guard.* *Testable:* the relay runs in its own goroutine started by `startEventOutboxRelay` (`workers.go:177` `go relay.Run(ctx)`), so the delete HTTP response is independent of delivery — assert via `TestOutboxRelay_DeliveryLifecycle` + `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` (`relay_test.go:229`); crashed-worker reaping = claim predicate `status='inflight' AND lease_expires_at_ns <= now` re-claims after `ClaimTTL` — assert via `TestEventOutboxClaimLeaseExpiryRedelivers` (`event_outbox_test.go:259`). New env-dependent e2e (REQ-1/REQ-5) *Testable:* with `EVENT_OUTBOX_ENABLED=false`, `startEventOutboxRelay` returns without starting a goroutine and logs the disabled line while delete transactions still enqueue both facts (assert with a repo-level delete + row count); with a retention env set, `prune()` deletes only rows older than the configured horizon (extend `TestEventOutboxPrune` (`event_outbox_test.go:355`) shape at the relay level, or assert the options thread from config → `NewEventOutboxRelay`).

---

## 6. Risks

- **Enable flag semantics drift** (D2): if a future change gates the transactional enqueue on `Enabled`, deletion atomicity breaks (facts silently missing). Mitigation: REQ-1 pins enqueue-unchanged; add a comment at the enqueue site and a test asserting `EVENT_OUTBOX_ENABLED=false` still produces both facts.
- **Retention default flip**: defaults 24h/168h exactly reproduce today's hardcoded behavior — any other default would change table-growth characteristics on upgrade. Guarded by REQ-5 defaults + AC-1.
- **Relaxation of the claim-TTL invariant during refactor** (D4 in §3, REQ-4): guarded by D1 (no shared struct) and the `> 2×` unit test.
- **`.env.example` drift**: the block was missing while docs existed; REQ-8 makes the block part of the shipped change and the same PR must touch both files (AGENTS.md "扩展入口" pattern: config surface → docs in one change).

*Verification basis: all line numbers re-confirmed on this checkout; `make check` gate applies to the eventual implementation (gofmt/build/vet/test, ≤500 lines/file).*
