# Design: Shared transactional-outbox kernel (billing usage · vault.file.deleted@1.1 · vault.file.notify@1.1)

> **Companion spec:** `docs/requirements/transactional-outbox-kernel-v1.md` (FR-1…FR-8, AC-1…AC-6, NFR-1…NFR-7) · **Module:** `internal/outbox` (new) + `internal/billing` + `internal/events` + `internal/repository` · **Baseline:** HEAD `acfaaf4` · **Gates:** `make check` (gofmt · go build · go vet · go test) · Invariants I1/I2/I5/I6 · File budget: ≤500 lines/file, ≤50 lines/function, no God types · stdlib only (I6)

---

## 0. Evidence verification (untrusted claims → verified)

Every claim in the evidence was re-checked against the tree. All confirmed; two path/line nuances recorded.

| Evidence claim | Verdict | Verified at |
|---|---|---|
| `outbox.go` runOutbox :14 / deliverBatch :28 / deliverFact :50 / retryFact :71 / billingBackoff :82 | ✅ exact | `internal/billing/outbox.go` (95 lines; timer first-tick immediate, per-fact goroutines + WaitGroup, math/rand ±25 % jitter) |
| `billing_outbox.go` SKIP LOCKED postgres claim, owner-guarded complete/retry, claim-lost sentinel | ✅ | `internal/repository/billing_outbox.go`: `claimBillingUsagePostgres` :26-43 (`FOR UPDATE SKIP LOCKED`, OR predicate pending-due ∨ inflight-expired, `attempts+1` at claim); `CompleteBillingUsage` :128-134 / `RetryBillingUsage` :136-147 fenced `WHERE id=$2 AND status='inflight' AND claim_owner=$3`; `requireBillingClaim` :149-160 → "billing usage claim lost". Note: file lives in `internal/repository/`, not `internal/billing/` |
| `runtime.go` Store port, Start/Close, pollEvery/batchSize/claimTTL | ✅ | `internal/billing/runtime.go`: `Store` :19-23 (`repository.BillingStore` + `AcquireLease`), `owner: uuid.NewString()` :67, knobs :69-71, `Start` :85-100 / `Close` :68-83 once-guarded, `run` :102-119 |
| `auditgovernance/repository.go` transactional capture | ✅ | `internal/auditgovernance/repository.go`: `RecordAudit` :22-34 / `InsertEvent` :36-47 → `RecordAuditWithGovernance` / `InsertEventWithGovernance` when `runtime.Capture(tenant)` |
| migrations 0038/0039 | ✅ | `{sqlite,postgres}/0038_snaplink_billing.up.sql` (billing_usage_outbox: status CHECK {pending,inflight,delivered}, `next_attempt_at_ns`, `claim_owner`, `claim_until_ns`, `last_error`, `UNIQUE(operation_id,dimension)`, **no `claim_token`**); `0039_audit_governance_outbox.up.sql` (owner+token+lease, `UNIQUE(origin_kind,origin_id)`) |
| `bus.go:80-84` Publish→InsertEvent | ✅ | `internal/events/bus.go`: `Publish` :78-108 → `repo.InsertEvent` :81 (durable insert, then non-blocking broadcast; errors logged, never propagated) |

**Critical new evidence (all confirmed):**

| Claim | Verdict | Verified at |
|---|---|---|
| Third outbox exists: `event_outbox` (0041, both dialects) | ✅ | `{sqlite,postgres}/0041_event_outbox.up.sql` (pending/inflight/delivered/failed, owner+token+lease, AUTOINCREMENT authority id, `event_outbox_delivered` fidelity table) |
| Transactional capture `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`/`SoftDeleteObjectByIDWithEvent` | ✅ | `internal/repository/event_outbox.go` :102/:147/:186; `validateOutboxFacts` :61-84 (≤1 MiB, schema_version == "1.1", rolls back delete tx); interface `repository_interface.go` :32-35 |
| Full relay started unconditionally from `workers.go` | ✅ | `internal/events/event_outbox_relay.go` (374 lines: Run :111, deliverBatch :140, deliverFact :171, deliverDeleted :190, deliverNotify :236, complete :305, retry :320, failImmediately, eventOutboxBackoff :345, prune :362); `cmd/server/workers.go` :63 `startEventOutboxRelay` :158-177, **always started**, core deletion atomicity not gated |
| `FileService.Delete` already transactional; EventBus emit is legacy fan-out | ✅ | `internal/service/file_delete.go`: hard :46 / soft :86 call `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` with `deleteAuditEntry` + `deleteFacts` (:118-141); `s.emit(ctx, obj, EventDeleted)` (:53/:92) remains |
| No strict decoder; `DisallowUnknownFields` only in config loaders | ✅ | `internal/config/config_{billing,audit_governance,audit_sink_l2}.go` only; relay parses only `notifyPayloadMeta {tenant,bucket,key}` :266-284 |
| Wire envelope names differ from acceptance (`tenant`/`actor`, no `occurred_at` on wire) | ✅ | `internal/events/payload.go`: `deletedFact` :33-52 (`tenant`/`actor`/`object_id`/`version_id`/`request_id`; no timestamp); `notifyFact` :54-73; goldens `schema_test.go` :31/:42/:96/:132 |
| Telemetry exists only for event outbox | ✅ | `internal/telemetry/metrics.go` :91-98 (`event_outbox.delivered/retried/failed/claim_lost/pruned/l2_*_total`); no billing counters |
| No shared kernel package exists | ✅ | `internal/` has no `outbox` package (`ls internal/outbox` → not found) |
| Backoff divergence | ✅ | billing: 1s ×2 cap 5 min, math/rand ±25 % (`outbox.go:82-94`); event: 1s ×2 cap 5 min, crypto/rand downward [0.75,1) (`event_outbox_relay.go:345-361`); governance: deterministic bounded (`runtime_test.go:189`) |
| Billing claim has no token; event/governance claims do | ✅ | 0038 has no `claim_token`; `ClaimEventOutbox(ctx, owner, token, limit, ttl)`; `ClaimAuditGovernance(ctx, owner, token, revision, limit, ttl)` with bindings join |
| Existing test baseline | ✅ | `internal/repository/event_outbox_test.go`, `internal/events/event_outbox_relay_test.go` (RetriesOn5xx, claim-lost→reclaim, L2 fail-immediate, backoff bounds), `internal/billing/runtime_test.go` (2 quota tests), `internal/events/schema_test.go` |
| Baseline HEAD | ✅ | `git log` → `acfaaf4` |

**Nuances found (non-blocking):**
1. `event_outbox` table has an extra `claim_token` **and** a `failed` status; billing has neither — the kernel must parameterize the terminal policy (FR-3), not unify status vocabularies (FR-4).
2. Event `RetryEventOutbox` fences `lease_expires_at_ns > $now` (live lease); billing `Complete/RetryBillingUsage` do **not** — the kernel contract (FR-3 "id+owner+token+live lease") requires aligning billing's fence (additive, no schema change).
3. Event `RetryEventOutbox` takes `maxAttempts` **per call** (failImmediately passes `maxAttempts=fact.Attempts` to force terminal on first write) — the kernel must expose the same per-call override via a sentinel, not via Options alone.

---

## 1. Design overview

New package `internal/outbox` (stdlib only) owns the three-way-shared machinery:

```
billing.Runtime ──┐                          ┌── billingOutboxStore (internal/billing)  ── billing_usage_outbox SQL (0038+0043)
                  ├──► outbox.Driver ◄──────┤
EventOutboxRelay ─┘   (internal/outbox)     └── eventOutboxStore (internal/events)      ── event_outbox SQL (0041)
                            │
                            ├─ Store port: Claim / Complete / Retry (per-table adapters)
                            ├─ Backoff: 1s base, ×2, 5 min cap, crypto/rand downward jitter [0.75,1)
                            ├─ timer-poll loop: immediate first tick, per-batch token, bounded claim ctx,
                            │   per-fact concurrent dispatch + WaitGroup, ctx.Done() exit, prune hook cadence
                            └─ outcome hooks → consumer telemetry (event_outbox.* counters stay put)
```

The kernel **owns**: claim loop, batch token, bounded claim/persist contexts, per-fact dispatch fan-out, complete-or-retry decision, backoff, terminal policy application, prune cadence, claim-lost handling (warn + hook, never in-loop reschedule).

The consumers **keep**: row SQL (per-table claim predicates, status vocabularies, dedupe keys, fencing columns), fact *delivery* (billing binding lookup + `AppendUsage`; event type switch + L2 sink + bucket-notification rule resolution + byte-exact POST), retention/prune body, telemetry counters (via hooks), config knobs and env names, owner strings (`event-outbox:<hostname>` / uuid).

Divergence ledger → kernel contract (this table is the design's invariant map):

| Dimension | billing (0038) | event (0041) | Kernel contract |
|---|---|---|---|
| Fencing | owner (+token after 0043, +live lease) | owner+token+live lease | `Complete`/`Retry` fenced by id+owner+token+live lease; fenced-out → claim-lost sentinel error |
| Status model | pending/inflight/delivered | +failed | **not unified** — per-table SQL keeps its own CHECK |
| Terminal policy | never (retry forever) | `attempts >= maxAttempts` → failed | `Options.MaxAttempts` (0 = never); `ErrTerminal` sentinel for fail-immediate |
| Dedupe | `UNIQUE(operation_id,dimension)` | row-id authority (AUTOINCREMENT) | **not unified** — enforced at insert, invisible to kernel |
| Backoff | ±25 % math/rand | downward crypto/rand | **unified** on `outbox.Backoff` (event/webhook precedent; strictly ≤ base shrinks at-least-once window) |
| Batch | 1..500 default 32 | same | clamp + default in kernel Options **and** per-table SQL (existing) |
| Capture | `ApplyBillingUsage` | delete-tx `insertOutboxFacts` | unchanged; kernel never runs on the write path (FR-5) |
| Retention | none | prune 24h/7d every 60 rounds | `Options.PruneEveryRounds` + `Prune` hook (0 = disabled) |
| Telemetry | none | `event_outbox.*_total` (8) | `Hooks` — counters stay in consumers, names unchanged (NFR-5) |

---

## 2. API changes

### 2.1 New package `internal/outbox` (≈250 lines, 1 file + tests)

```go
package outbox

// Fact is the kernel's view of one claimed row. Raw carries the consumer's
// typed row (billing: repository.BillingUsageFact; events:
// repository.EventOutboxRow); the kernel never interprets it.
type Fact struct {
    ID       string // authority row id (string form: billing uuid / event int64)
    Attempts int    // incremented at claim by per-table SQL
    Raw      any    // consumer row, opaque to the kernel
}

// Store is the per-table persistence port, implemented by thin adapters in
// the consumer packages. Claim returns only due rows and stamps
// owner+token+lease; Complete/Retry are fenced by id+owner+token+live lease
// and return an error when fenced out (claim-lost).
type Store interface {
    Claim(ctx context.Context, owner, token string, limit int, ttl time.Duration) ([]Fact, error)
    Complete(ctx context.Context, id, owner, token string) error
    Retry(ctx context.Context, id, owner, token, lastErr string, next time.Time, maxAttempts int) error
}

// Hooks are consumer outcome callbacks (telemetry stays in consumers). Nil hooks are no-ops.
type Hooks struct {
    OnDelivered func(ctx context.Context, fact Fact)                        // after successful Complete
    OnRetried   func(ctx context.Context, fact Fact, err error, terminal bool) // after Retry; terminal = landed 'failed'
    OnClaimLost func(ctx context.Context, fact Fact, phase string)          // "complete" | "retry"
}

type Options struct {
    Owner            string        // consumer identity; empty → uuid per driver
    PollInterval     time.Duration // <=0 → 1s (immediate first tick)
    BatchSize        int           // clamped 1..500; <=0 → 32
    ClaimTTL         time.Duration // <=0 → 30s
    HTTPTimeout      time.Duration // bounds claim/complete/retry calls; <=0 → 5s
    MaxAttempts      int           // 0 = never terminal (billing); >0 → terminal-failed when attempts >= MaxAttempts
    PruneEveryRounds int           // 0 = disabled; N = invoke Prune every N poll rounds (event: 60)
    Prune            func(ctx context.Context) error
    Hooks            Hooks
}

// ErrTerminal: dispatch wraps a cause with ErrTerminal to fail immediately —
// kernel retries with next=now and maxAttempts=attempts so the per-table
// terminal predicate holds on the first write (event relay's unauthorized-L2
// path; billing never returns it).
var ErrTerminal = errors.New("outbox: terminal failure")

// New builds a driver. dispatch delivers one claimed fact: nil → Complete;
// error → Retry with Backoff(attempts); ErrTerminal-wrapped → fail now.
func New(store Store, dispatch func(ctx context.Context, fact Fact) error,
    opts Options, logger *slog.Logger) *Driver

func (d *Driver) Run(ctx context.Context) // poll until ctx cancelled

// Backoff: 1s base, ×2, 5 min cap, downward-only crypto/rand jitter [0.75, 1.0)×base.
func Backoff(attempt int) time.Duration
```

Driver internals (all functions ≤50 lines):
- `Run`: timer(0) first tick → `deliverBatch` → `rounds++`; `PruneEveryRounds>0 && rounds%N==0` → prune under `HTTPTimeout` ctx; `timer.Reset(PollInterval)`; `ctx.Done()` exits.
- `deliverBatch`: per-batch `newClaimToken()` (crypto/rand 16 B → 32 hex, moved from relay :132-138); claim under `context.WithTimeout(ctx, HTTPTimeout)`; per-fact goroutines + WaitGroup; break on `ctx.Err()`.
- `deliverFact`: `err := dispatch(ctx, fact)`; nil → `complete`; else → `retry`.
- `complete`: bounded `Store.Complete(id, owner, token)`; error → warn + `OnClaimLost("complete")`, **no in-loop reschedule**; else `OnDelivered`.
- `retry`: `terminal := errors.Is(err, ErrTerminal)`; `maxAttempts := opts.MaxAttempts`; `next := now+Backoff(attempts)`; if terminal → `maxAttempts = fact.Attempts`, `next = now`, `lastErr = errors.Unwrap(err).Error()` (cause message, not the sentinel prefix); bounded `Store.Retry(...)`; error → warn + `OnClaimLost("retry")`; else `OnRetried(fact, cause, terminal)`.

### 2.2 Repository changes (internal API only — `repository.Repository` interface untouched)

**`internal/repository/billing_types.go` — `BillingStore` signatures gain `token`:**

```go
// before                          // after
ClaimBillingUsage(ctx, owner string, limit int, ttl time.Duration) ([]BillingUsageFact, error)
                                    ClaimBillingUsage(ctx, owner, token string, limit int, ttl time.Duration) ([]BillingUsageFact, error)
CompleteBillingUsage(ctx, id, owner string) error
                                    CompleteBillingUsage(ctx, id, owner, token string) error
RetryBillingUsage(ctx, id, owner, lastErr string, next time.Time) error
                                    RetryBillingUsage(ctx, id, owner, token, lastErr string, next time.Time) error
```

Callers: `internal/billing/outbox.go` (the **sole production caller** — `deliverBatch` :29, `deliverFact` :66, `retryFact` :74; it must move with the signature change, see §5 step 2) and `internal/repository/billing_test.go` — all internal, all updated in the same atomic step. `internal/billing/runtime_test.go` needs **no** changes (verified: it never calls claim/complete/retry; its `repo.(Store)` type assertion is unaffected by the interface change). `BillingUsageFact`/`BillingUsageMutation` structs unchanged.

**`internal/repository/billing_outbox.go` — SQL rewrites (I1: sqlite via `s.rebind`, distinct placeholders):**
- `ClaimBillingUsage`: validate `token != ""` too (FR-2 identity: owner+token+ttl). Postgres `UPDATE … SET status='inflight', attempts=attempts+1, claim_owner=$1, claim_token=$2, claim_until_ns=$3 WHERE id IN (SELECT … LIMIT $6 FOR UPDATE SKIP LOCKED) RETURNING …`. SQLite path: same stamping through `claimBillingUsageIDs` with `s.rebind`.
- `CompleteBillingUsage`: fence `WHERE id=$2 AND status='inflight' AND claim_owner=$3 AND claim_token=$4 AND claim_until_ns>$5` (**live-lease fence added** — kernel contract parity with event; defense-in-depth: per-batch tokens already exclude stale completions, so no observable regression).
- `RetryBillingUsage`: same fence + token column, `last_error` truncation to 512 B unchanged. No `failed` status, no `maxAttempts` parameter (billing never terminal — status vocabulary stays per-table, FR-4).

**`internal/repository/event_outbox.go` / `repository_interface.go`:** unchanged — `ClaimEventOutbox`/`CompleteEventOutbox`/`RetryEventOutbox(…, maxAttempts)`/`PruneEventOutbox` already token-fenced with live-lease checks; they become the event adapter's backend as-is.

### 2.3 Consumer refactors (public surfaces unchanged)

**`internal/billing`:**
- **Delete** `outbox.go` (runOutbox/deliverBatch/deliverFact/retryFact/billingBackoff all move into the kernel; `math/rand` import goes away).
- **Add** `outbox_store.go` (~40 lines): `billingOutboxStore{store Store}` implementing `outbox.Store` — Claim maps `[]BillingUsageFact` → `[]outbox.Fact{Raw: f, ID: f.ID, Attempts: f.Attempts}`; Retry forwards and **ignores maxAttempts** (documented: billing has no terminal state).
- **Add** `dispatch.go` (~30 lines): `func (r *Runtime) dispatchFact(ctx context.Context, fact outbox.Fact) error` — type-assert `fact.Raw.(repository.BillingUsageFact)` (mismatch → descriptive error, surfaces in `last_error`), binding lookup, metadata `json.Unmarshal`, `client.AppendUsage(...)`. Delivery timeouts unchanged: `http.Client{Timeout: httpTimeout}` (5 s) < `claimTTL` (30 s), preserving the live-lease margin.
- **`runtime.go`**: `New` additionally builds `outbox.New(billingOutboxStore{r.store}, r.dispatchFact, outbox.Options{Owner: r.owner, PollInterval: r.pollEvery, BatchSize: r.batchSize, ClaimTTL: r.claimTTL, HTTPTimeout: r.httpTimeout, MaxAttempts: 0}, r.logger)`; `run`'s second goroutine becomes `r.outbox.Run(ctx)` (projector goroutine unchanged). Public surface `New/Start/Close/Ready/CheckQuota/Apply` unchanged.

**`internal/events` (relay internals; `EventOutboxRelay`, `NewEventOutboxRelay`, `EventOutboxRelayOptions` unchanged — `cmd/server/workers.go` untouched):**
- **Delete from relay**: `Run` loop, `deliverBatch`, `complete`, `retry`, `failImmediately`, `eventOutboxBackoff`, `jitter`, `newClaimToken` (all now in kernel).
- **Keep**: constructor + default fallbacks, `deliverFact` (becomes dispatch), `deliverDeleted`, `deliverNotify`, `parseNotifyPayload`, `deliverPayload`, `postEventTo`, `ruleMatches`, `resolveTargets`, prune body, owner string `event-outbox:<hostname>`.
- **Add** `outbox_store.go` (~45 lines): `eventOutboxStore{repo repository.Repository}` — Claim maps rows (`strconv.FormatInt(row.ID,10)`), Complete/Retry parse the id back and forward `maxAttempts` verbatim.
- **Dispatch contract change** (internal): `deliverDeleted` returns an error instead of calling complete/retry — `sink == nil` → `nil` (kernel completes); `ErrSinkNotBound` → `IncEventOutboxL2Unbound` + `nil`; `ErrSinkUnauthorized` → `IncEventOutboxL2Rejected` + `fmt.Errorf("%w: %v", outbox.ErrTerminal, ErrSinkUnauthorized)`; other → error (5xx/transport retried). `deliverNotify` returns `nil` on success / error on retry; the "delivery approached its lease" warning stays inside.
- **Hooks** → telemetry (names unchanged): `OnDelivered` → `IncEventOutboxDelivered`; `OnRetried(terminal)` → `IncEventOutboxFailed` / `IncEventOutboxRetried`; `OnClaimLost` → `IncEventOutboxClaimLost`. `Prune` hook → `repo.PruneEventOutbox(now-24h, now-7d)` + `IncEventOutboxPruned` + warn; `PruneEveryRounds: 60`.

### 2.4 Strict decoder — `internal/events/payload_decoder.go` (new, ≈150 lines; lives in `internal/events`, **not** the kernel: the kernel must stay schema-agnostic and must not import `internal/events`)

```go
// DeletedEnvelope is the semantic projection of vault.file.deleted@1.1.
// Wire names tenant/actor map onto TenantID/ActorDigest; OccurredAt is NOT
// on the wire (row created_at_ns / audit occurred_at) and is supplied by the
// caller. Semantic json tags make re-encode→decode lossless in the semantic
// space; wire bytes stay pinned by schema_test.go goldens (NFR-6).
type DeletedEnvelope struct {
    SchemaVersion string    `json:"schema_version"`
    EventType     string    `json:"event_type"`
    TenantID      string    `json:"tenant_id"`
    Bucket        string    `json:"bucket"`
    Key           string    `json:"key"`
    ObjectID      int64     `json:"object_id"`
    VersionID     string    `json:"version_id"`
    Size          int64     `json:"size"`
    ETag          string    `json:"etag"`
    Backend       string    `json:"backend"`
    RequestID     string    `json:"request_id"`
    ActorDigest   string    `json:"actor_digest"`
    Reason        string    `json:"reason,omitempty"`
    OccurredAt    time.Time `json:"occurred_at"`
}

func DecodeDeletedFact(payload []byte, occurredAt time.Time) (DeletedEnvelope, error)
// json.Decoder + DisallowUnknownFields; required set = the fields asserted by
// TestEventSchema_RequiredFields (schema_version, event_type, tenant, bucket,
// key, object_id, version_id, size, etag, backend, request_id, actor);
// schema_version must == "1.1"; event_type must == "vault.file.deleted@1.1".

// NotifyEnvelope: semantic projection of notify@1.1 (schema_version,
// event_type, tenant_id, bucket, key, version_id, request_id, actor_digest,
// records[] with sequencer). Strict decode; the payload is self-contained by
// construction — the test asserts no audit/outbox row identifiers appear.
func DecodeNotifyFact(payload []byte) (NotifyEnvelope, error)
```

The relay does **not** adopt the decoder on the hot path (its `notifyPayloadMeta` parse stays; wire bytes must pass through byte-exact — decoder is validation/observability + acceptance surface only).

---

## 3. Compatibility constraints

| Constraint | Guarantee | Mechanism |
|---|---|---|
| Wire bytes unchanged | deleted@1.1 / notify@1.1 payloads byte-identical | no builder changes; goldens (`schema_test.go`) stay green; relay still POSTs stored bytes verbatim |
| Env vars / defaults unchanged | `BILLING_OUTBOX_*` (`config_billing.go:27-29,53-55`), `EVENT_OUTBOX_*` (`config_event_outbox.go:15-44`) | config files untouched |
| Telemetry names unchanged | `event_outbox.delivered/retried/failed/claim_lost/pruned/l2_*_total` | counters fired from kernel `Hooks`; L2 counters inside dispatch — same names, same semantics (NFR-5) |
| Public API unchanged | `billing.Runtime` (New/Start/Close/Ready/CheckQuota/Apply), `events.EventOutboxRelay` + `Options`, `repository.Repository` interface, `cmd/server/workers.go` | refactors are internal to the two consumers |
| Internal API change (accepted) | `repository.BillingStore` claim/complete/retry gain `token` | callers = billing runtime + tests only; billing is opt-in (I5) |
| Never-blocking preserved | delete path never touches the kernel; enqueue stays same-tx INSERT; delivery only in relay goroutine (FR-5) | kernel is invoked solely from `Driver.Run` goroutines |
| Backoff behavior change (intentional) | billing retry jitter: ±25 % math/rand → downward [0.75,1) crypto/rand | spec decision FR-3; strictly ≤ base shrinks at-least-once window; no test pins the old jitter; relay bounds test (existing) keeps its contract via `outbox.Backoff` |
| Billing complete/retry gain live-lease fence | contract parity with event (FR-3) | one predicate added per statement; per-batch tokens already make stale completions impossible → no observable regression |
| Log text changes (accepted) | "billing usage delivery deferred"/"event outbox delivery deferred" → unified "outbox delivery deferred" | behavior (warn level, no reschedule) identical; no test asserts log strings |
| `internal/auditgovernance` untouched | out of scope (FR-4 note; bindings/revision/gap-scan/redaction are consumer-specific) | zero edits |
| I1 | sqlite statements via `s.rebind`, distinct placeholders | changed SQL is a rewrite of the existing reference implementations |
| I2 | only new migration files 0043 (0042 is taken in-tree by `0042_audit_governance_terminal_failed`, both dialects — next free number is 0043; no ordering dependency on 0042) | never edit 0038/0039/0041/0042 |
| I5 | billing opt-in (`BILLING_ENABLED`), relay always-started | no gating flips |
| I6 | stdlib only | crypto/rand, encoding/json, strconv, errors — no go.mod change |

---

## 4. Failure modes

| # | Failure | Kernel/consumer behavior | Recovery |
|---|---|---|---|
| F1 | Claim DB error | warn, skip batch | next poll tick (both consumers today) |
| F2 | Dispatch error (sink 5xx/transport, notify target failure, binding missing, metadata invalid) | `Retry` with `Backoff(attempts)`; `last_error` ≤512 B; event: terminal `failed` at `attempts >= MaxAttempts` (10) → never re-claimed, pruned 7d; billing: retry forever, 5 min cap (unchanged) | backoff → redeliver; terminal rows removed by prune |
| F3 | Complete fenced out (lease expired mid-flight / owner+token mismatch after TTL reclaim) | warn + `OnClaimLost("complete")` + counter; **no in-loop reschedule** (claim-lost never double-schedules, D7) | row re-claimed after lease expiry → at-least-once redelivery |
| F4 | Retry persistence error | warn + `OnClaimLost("retry")`; row remains `inflight` until lease expiry | TTL reclaim → redeliver (at-least-once window widens; documented semantics preserved) |
| F5 | L2 unauthorized (401/403) | dispatch returns `ErrTerminal`-wrapped; kernel Retry with `next=now`, `maxAttempts=attempts` → immediate terminal `failed`, `l2_rejected_total` | none (terminal); pruned after 7d — mirrors `failImmediately` today |
| F6 | Prune failure | warn; cadence continues | next prune round (60 polls) |
| F7 | Shutdown mid-batch | `ctx.Done()` breaks dispatch fan-out; per-call store ops bounded by `HTTPTimeout`; `WaitGroup` waits | in-flight rows reclaim after `ClaimTTL` |
| F8 | Dispatch type-assertion failure (programming error: adapter/dispatch mismatch) | returns descriptive error → F2 path; surfaces in `last_error` (event terminates at maxAttempts; billing retries with visible error) | fix wiring; no data loss (row still claimable) |
| F9 | Migration on live DB (**0043**) | `ADD COLUMN claim_token TEXT NOT NULL DEFAULT ''` — PG: fast metadata-only (PG 11+); SQLite: table rewrite (brief lock, local/embedded deployments); 0043 applies after `0042_audit_governance_terminal_failed` on upgraded DBs, at open on fresh DBs — no ordering dependency | standard `repo.Migrate` version-skip (stem-keyed); existing rows valid via default; in-flight rows with `claim_token=''` complete-fence only after next re-claim (≤ claimTTL) — no stranded rows beyond the normal reclaim window; **pinned in `billing_test.go`** (pre-migration-row shape: INSERT `claim_token=''` + `claim_owner='X'` + future `claim_until_ns` → complete/retry with any non-empty token → claim-lost, forcing re-claim) |
| F10 | Token collision / empty identity | 128-bit crypto/rand per batch; `owner/token/ttl` validation in per-table Claim (FR-2) | negligible; invalid identity → descriptive error, no claim |

---

## 5. Migration steps (ordered, each step leaves `make check` green)

1. **Migration files (I2):** create `internal/repository/migrations/{sqlite,postgres}/0043_billing_outbox_claim_token.{up,down}.sql`. UP: `ALTER TABLE billing_usage_outbox ADD COLUMN claim_token TEXT NOT NULL DEFAULT '';` DOWN: `ALTER TABLE billing_usage_outbox DROP COLUMN claim_token;` (both dialects). Never edit 0038/0039/0041/0042 — 0042 is taken in-tree by `0042_audit_governance_terminal_failed`; 0043 is the next free number (no ordering dependency on 0042). **Green:** additive column, zero Go references; no test enumerates the migration list; fresh test DBs apply it harmlessly at open (`repo.Migrate`).
2. **Repository SQL + interface + call sites (one atomic step):** rewrite `internal/repository/billing_outbox.go` per §2.2 (token stamping on claim, token+live-lease fence on complete/retry, I1 placeholders — PG claim `$1..$6`/complete `$1..$5`/retry `$1..$6`; SQLite per-row UPDATE via `s.rebind` becomes `$1..$6` with **no** `LIMIT` — the SELECT keeps `LIMIT $3`); update `BillingStore` in `billing_types.go`; update `billing_test.go` call sites (+ new assertions: claim stamps `claim_token`, wrong-token complete/retry → claim-lost, expired-lease complete → claim-lost, re-claim after TTL with `attempts==2`); **and, in the same step, update the sole production caller `internal/billing/outbox.go:29/:66/:74`** — thread a per-batch token from a 5-line local `newBatchToken()` (crypto/rand 16 B → 32 hex, explicitly marked for deletion at step 4). The signature change is atomic with its callers: splitting it out (as the original §2.2 callers list implied) breaks `go build ./...` and `make check`. **Green:** the new SQL/fence semantics are exercised by the extended `billing_test.go` in this same step.
3. **Kernel:** add `internal/outbox/outbox.go` (Driver/Options/Hooks/Store/Fact/Backoff/ErrTerminal/newClaimToken) + `internal/outbox/kernel_test.go` (fake Store contract suite — §6 AC-1). **Green:** new package, no dependents yet; stdlib-only imports, no consumer references.
4. **Billing consumer:** delete `internal/billing/outbox.go` (the step-2 `newBatchToken` helper goes with it — the kernel owns token generation from here on); add `outbox_store.go` + `dispatch.go`; wire driver in `runtime.go` (`MaxAttempts: 0`); **`internal/billing/runtime_test.go` needs no signature changes** (verified — it never calls claim/complete/retry; the `repo.(Store)` assertion keeps compiling); add AC-2 delivery regression tests. **Green:** adapter implements `outbox.Store` (kernel exists since step 3); repository signatures already carry `token` (step 2).
5. **Events consumer:** rewrite relay internals onto the driver; add `outbox_store.go`; delete moved functions (`deliverBatch`, `complete`, `retry`, `failImmediately`, `eventOutboxBackoff`, `jitter`, `newClaimToken`) — **`Run(ctx)` stays** as a thin `d.Run(ctx)` delegation (workers.go:178 contract); rewrite the ~13 `relay.deliverBatch(ctx)` call sites in `event_outbox_relay_test.go` (:154/:172/:204/:215/:244/:251/:286/:444/:466/:481/:494/:523/:529/:548/:553/:597/:602/:616) to drive the kernel (short-poll `Run` or explicit driver tick) and move backoff-bounds tests to `kernel_test.go` (`outbox.Backoff`); add `payload_decoder.go` + `payload_decoder_test.go`. **Green:** repo event methods (`ClaimEventOutbox`/`CompleteEventOutbox`/`RetryEventOutbox`/`PruneEventOutbox`) unchanged; `NewEventOutboxRelay`/`Options`/`Run` signatures untouched so `workers.go` compiles unchanged; all moved-symbol test references updated in-step.
6. **Integration:** add `internal/integration/outbox_kernel_postgres_test.go` (`//go:build integration`, `freshRepo`/`pgDSN` auto-skip pattern): billing token fencing under concurrent two-repo SKIP LOCKED claims + kernel driver over the real postgres Store.
7. **E2E never-blocking test:** add `internal/service/file_delete_outbox_test.go` (§6 AC-6).
8. **Gates:** `make check`, `make test-race`, `make test-integration` (postgres reachable); verify `gofmt -l` empty and per-file line budgets.

---

## 6. Testable acceptance mapping

**AC-1 — Kernel unit contract, both dialects (I1).**
- `internal/outbox/kernel_test.go` (fake in-memory Store; `testing` only, no assertions framework):
  1. claim returns only due facts; claim stamps owner/token and increments attempts (fake asserts the driver's per-batch token + owner passed through);
  2. two drivers claiming concurrently → no double-claim (fake serializes; real SQL concurrency is covered by AC-1b);
  3. complete/retry with wrong owner, wrong token, or expired lease → claim-lost error → driver warns + fires `OnClaimLost(phase)` + **no reschedule**; the same fact is reclaimed by a new claimer afterwards (idempotent re-delivery);
  4. retry reschedules with `next = now + Backoff(attempts)`; `MaxAttempts=N` → N-th retry passes `maxAttempts` such that the row lands terminal (fake asserts the parameter; real `failed` status asserted in repository tests); `MaxAttempts=0` never terminates;
  5. `ErrTerminal`-wrapped dispatch error → `next=now` + `maxAttempts=attempts` (immediate terminal);
  6. `Backoff` bounds: `[0.75, 1.0) × base` for attempts 1..n, cap 5 min, monotone base doubling (existing relay bounds contract moved here);
  7. per-batch tokens differ across batches; prune hook invoked every `PruneEveryRounds` rounds with a bounded ctx (and never when 0);
  8. shutdown: `ctx` cancel mid-batch exits promptly, in-flight facts still complete/retry with bounded timeouts.
- **AC-1b — SQLite, CI (both table shapes, kernel parameterized):** extend `internal/repository/billing_test.go` (token fencing + live-lease + reclaim, §5.2); keep `internal/repository/event_outbox_test.go` green (claim lifecycle, lease-expiry redelivery, retry/terminal-failed, prune, one-tx composition — all now exercised through the kernel-backed relay); placeholder discipline (I1) asserted by running the same suite on both dialects.
- **AC-1c — Postgres, `//go:build integration`:** `internal/integration/outbox_kernel_postgres_test.go` — concurrent `ClaimBillingUsage` from two repos (`FOR UPDATE SKIP LOCKED`, pattern: `audit_governance_postgres_test.go`), token fence on complete/retry, lease-expiry reclaim; auto-skip when PG unreachable; outside CI gate (`make test-integration`).

**AC-2 — Billing regression.** `internal/billing/runtime_test.go` keeps passing (quota fail-closed, readiness, zero-limit enforcement — `TestRuntimeFailsUnknownProjectionClosed`, `TestRuntimeEnforcesExplicitZeroAndPreservesProjectedUse`). New `TestRuntimeOutboxDeliversUsageFact`: fake `AppendUsage` endpoint (httptest) → `ApplyBillingUsage` enqueues 2 facts → driver (short poll) claims → delivers → `CompleteBillingUsage` marks `delivered` (`delivered_at_ns` set); endpoint 500 → fact back to `pending` with `attempts`/`last_error` and `next_attempt_at_ns = now+Backoff` → endpoint recovers → delivered on next poll; claim-lost on complete (simulate by completing with a forged token) → warn path, no reschedule, re-claim after TTL delivers exactly once (attempts == 2).

**AC-3 — Delete-transaction composition + sink outage.** Keep green: `internal/repository/event_outbox_test.go` one-tx delete+audit+facts (atomic commit/rollback on invalid fact) and `TestOutboxRelay_RetriesOn5xx` (now through the kernel). Extend `internal/events/event_outbox_relay_test.go`: with a fake `AuditSink` returning 500, `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` commit atomically and the relay leaves the fact `pending` (state pending→inflight→pending asserted via `last_error` + `attempts`); after sink recovery the fact is redelivered after backoff and lands `delivered` (`delivered_at_ns` set).

**AC-4 — Crash recovery / TTL reclaim.** Keep green: `TestEventOutboxClaimLeaseExpiryRedelivers` (claim → no complete (simulated crash) → re-claim after lease expiry → same fact to a new owner; first owner's late complete → claim-lost; attempts == 2 → delivered exactly once after the second claim). Add the billing-table variant in `billing_test.go` (§5.2) and the fake-Store variant in `kernel_test.go` (AC-1.3).

**AC-5 — Event schema round-trip with strict decoder.** New `internal/events/payload_decoder_test.go`:
- `DecodeDeletedFact(BuildDeletedFact(goldenObject(), "alice", "req-1", "default"), occurredAt)` → envelope with all seven acceptance fields populated: `tenant_id` (wire `tenant`), `bucket`, `key`, `version_id`, `actor_digest` (wire `actor`), `occurred_at` (parameter, not on wire), `request_id` — plus `object_id`/`size`/`etag`/`backend`/`reason`;
- re-encode → decode is lossless (semantic JSON round-trip);
- rejects unknown fields (`DisallowUnknownFields`), missing required fields (the `TestEventSchema_RequiredFields` set), and `schema_version != "1.1"`;
- `DecodeNotifyFact` strict-decode exposes only self-contained fields; assert the payload contains no audit/outbox row identifiers (no `origin_id`, no outbox column names);
- existing golden-byte tests stay green (wire unchanged); relay POSTs the payload byte-exact (existing byte-exactness assertion now exercised on the kernel path).

**AC-6 — Composition e2e, never-blocking.** New `internal/service/file_delete_outbox_test.go` (httptest + real repo/store, REST `DELETE /v1/files/{key}` — current trigger; admin-delete route is a separate direction, applies unchanged when it lands):
1. delete → 2xx;
2. **immediately after the response** (no sleeps), both `event_outbox` rows (`vault.file.deleted@1.1` + `vault.file.notify@1.1`) are visible with `status='pending'` in the same DB (same-transaction visibility);
3. with the L2 sink down/hanging (fake sink 500 / never-answering handler), the delete still completes and returns **with rows still `pending`** — never-blocking proven by state, not by wall clock (no timing flake);
4. async delivery: run the kernel-backed relay against recovered fake sinks → both rows flip to `delivered`;
5. business-flow latency bounded by the delete transaction only — asserted structurally (no sink I/O on the request path by construction, FR-5).

---

## 7. Out of scope (unchanged from requirements)

Audit-governance outbox adoption of the kernel; admin file-delete route; EventBus/notifier/webhook-DLQ changes; notify rules engine; billing terminal-failed state or retention; telemetry renames; wire-format redesign (golden-pinned; decoder is additive); any change to `internal/auditgovernance`.
