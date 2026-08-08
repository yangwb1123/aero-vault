# Requirements Specification — `internal/billing`: terminal-failure handling (max attempts / dead-letter) for the billing usage outbox

**Module:** `internal/billing` (+ `internal/repository` billing outbox, `internal/config`, migrations)
**Direction:** "Add terminal-failure handling (max attempts / dead-letter) to the billing usage outbox"
**Source analysis:** `docs/auto/analyses/internal-billing-d0d7ddd3.json` (direction 1)
**Status:** Spec (evidence-verified against the repository, 2026-08-07). All cited symbols verified verbatim; no staleness found. The events outbox (`RetryEventOutbox` `CASE WHEN attempts >= maxAttempts`, relay `failImmediately`) is the in-repo precedent this spec mirrors.

---

## 1. Scope

Give the billing usage outbox a **terminal `failed` state and an attempt cap**:

1. Schema: `status` CHECK constraint gains `'failed'` (migration 0043, sqlite + postgres).
2. Repository: `RetryBillingUsage` gains a `maxAttempts` parameter and lands a fact in terminal `'failed'` when `attempts >= maxAttempts` (mirrors `RetryEventOutbox`, `internal/repository/event_outbox.go:362-387`).
3. Runtime: `deliverFact` classifies **permanent** failures — Snaplink HTTP **4xx** (`apiError`), missing tenant binding, unparseable metadata — as terminal immediately; all other errors retry until the cap.
4. Config: `BILLING_OUTBOX_MAX_ATTEMPTS` knob (default 10, bounds 1..1000, mirroring `EVENT_OUTBOX_MAX_ATTEMPTS`).
5. Tests + docs: four acceptance checks made testable; `.env.example` + `docs/configuration.md` updated.

Out of scope (see §4): retention/cleanup of terminal rows (no DELETE path added), telemetry/metrics, changes to the claim query, the due index, or the `BillingUsageFact` shape, and any change to the events/audit/webhook outboxes.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| Cited symbol | Verified location | Verdict |
|---|---|---|
| `internal/billing/outbox.go:71` — `retryFact` re-queues unconditionally | `outbox.go:71-80`: `retryFact` calls `store.RetryBillingUsage` unconditionally; backoff via `billingBackoff` (`outbox.go:82-89`, 1 s base, ×2, **5 min cap + jitter**, no attempt cap anywhere) | ✅ exact |
| `internal/billing/outbox.go:53` — missing tenant binding → retry | `outbox.go:51-54`: `client, ok := r.bindings[fact.TenantID]; if !ok { r.retryFact(...) }` | ✅ exact |
| `internal/billing/outbox.go:58` — unparseable metadata → retry | `outbox.go:56-60`: `json.Unmarshal` error → `retryFact` | ✅ exact |
| `internal/repository/billing_outbox.go:33` — claim re-selects pending rows regardless of attempts | `billing_outbox.go:33-36` (Postgres) and `:51-56` (SQLite): the only claim predicates are `(status='pending' AND next_attempt_at_ns <= now) OR (status='inflight' AND claim_until_ns <= now)`; `attempts` is **incremented** at claim (`:31`, `:77`), never compared | ✅ exact |
| `internal/repository/migrations/sqlite/0038_snaplink_billing.up.sql:24` — CHECK constraint | `:24` is `CREATE TABLE billing_usage_outbox`; the CHECK `status IN ('pending','inflight','delivered')` is at **`:29`**; identical constraint in the postgres 0038 pair | ✅ present (constraint at :29; table at :24) |
| `internal/repository/migrations/0042_audit_governance_terminal_failed.up.sql` — terminal-state precedent | Both dialects exist; adds `failed_at_ns`; claim predicates require `failed_at_ns=0` (`audit_governance_claim.go:38/:62/:88`); terminal rows pruned by `CleanupFailedAuditGovernance` (`audit_governance_cleanup.go:107-135`). **Note:** the audit outbox has no status column (0039 schema is `delivered_at_ns`-based) — hence the column approach there vs. the status approach mandated by AC-4 here | ✅ present |

Additional facts verified for this spec (not cited by the direction):

- **No DELETE path exists:** `internal/repository/billing_outbox.go` contains only `UPDATE` statements (claim/complete/retry); `grep -rn "DELETE FROM billing_usage"` matches nothing; no prune method exists for `billing_usage_outbox`/`billing_usage_operations` (only audit and events outboxes have prunes). ✅ problem statement's growth claim confirmed.
- `RetryBillingUsage` (`billing_outbox.go:136-147`): fencing `WHERE id=$3 AND status='inflight' AND claim_owner=$4`, `last_error` truncated to 512 bytes, `requireBillingClaim` (`:149-161`). **Exactly one call site outside the repository:** `outbox.go:74`.
- `BillingStore` interface: `internal/repository/billing_types.go:55-63` (6 methods; `var _ BillingStore = (*sqlStore)(nil)` at `:64`). Embedded by `internal/billing/runtime.go:20` (`Store` = `repository.BillingStore` + `AcquireLease`).
- **Events-outbox precedent (the pattern to mirror):** `RetryEventOutbox(ctx, id, owner, token, lastErr, next, maxAttempts)` sets `status = CASE WHEN attempts >= $1 THEN 'failed' ELSE 'pending' END` (`event_outbox.go:362-387`, `maxAttempts <= 0 → 1`); relay `failImmediately` passes `maxAttempts=fact.Attempts` so the terminal predicate holds on the first write (`event_outbox_relay.go:227-244`); default 10 via `EVENT_OUTBOX_MAX_ATTEMPTS`, bounds 1..1000 (`config_event_outbox.go:32/:99-100`).
- **4xx classification surface:** `apiError{Status, Code}` (`internal/billing/models.go:63-68`); every non-2xx response is returned as `*apiError` via `decodeAPIError` (`client.go:147-153`); a 401 also invalidates the cached token (`client.go:121`). `errors.As` is the available classification mechanism.
- **Config wiring:** `BillingConfig` fields/env reads (`config_billing.go:19-55`), `Validate` (`:127-151`), called from `Config.Validate` via `config_validate.go:54`; runtime constructed at `cmd/server/billing.go:22` (`billing.New(cfg.Billing, store, logger)`).
- **Migration mechanics:** runner applies each file in a transaction (`sql.go:89-106`); sqlite opens with `PRAGMA foreign_keys = ON` (`sqlite.go:31`) and `PRAGMA foreign_keys` is a no-op inside a transaction — the sqlite rebuild must not rely on toggling FKs; the rebuilt table is the **child** side of the FK (`billing_usage_operations` is untouched), so `DROP TABLE billing_usage_outbox` inside the tx is safe. Table-rebuild precedent: `0002_multitenant.up.sql` (`CREATE ..._new` → `INSERT SELECT` → `DROP` → `RENAME` → recreate indexes). Next free number is **0043** (0042 exists for both dialects). Down migrations are required by AGENTS.md I2 (never auto-executed).
- **Test surface today:** `internal/billing` has only `TestRuntime*`/`TestClient*` — no `TestOutbox*`, so a new test must carry the `TestOutbox` prefix to match `-run TestOutbox` (AC-1). Runtime tests construct `Runtime` directly with a real sqlite store (`runtime_test.go:17-32` helper, direct construction at `:36-39`) — zero-value `maxAttempts` hazard, see D5.

---

## 3. Requirements

### REQ-1 — Schema: terminal `'failed'` status (migration 0043, both dialects) — **GAP**

- `internal/repository/migrations/sqlite/0043_billing_outbox_terminal_failed.up.sql`: rebuild `billing_usage_outbox` per the `0002_multitenant.up.sql` precedent (`CREATE TABLE billing_usage_outbox_new` with identical columns/FK/UNIQUE but `status ... CHECK (status IN ('pending', 'inflight', 'delivered', 'failed'))` → `INSERT ... SELECT` all columns → `DROP TABLE billing_usage_outbox` → `ALTER TABLE ... RENAME TO billing_usage_outbox` → recreate `billing_usage_due_idx` and `billing_usage_tenant_idx` exactly as in 0038).
- `internal/repository/migrations/postgres/0043_billing_outbox_terminal_failed.up.sql`: `ALTER TABLE billing_usage_outbox DROP CONSTRAINT IF EXISTS billing_usage_outbox_status_check;` then `ADD CONSTRAINT billing_usage_outbox_status_check CHECK (status IN ('pending','inflight','delivered','failed'));` (0038's inline column CHECK is auto-named `billing_usage_outbox_status_check`).
- `.down.sql` pairs for both dialects restore the 3-value constraint (I2: never auto-executed, provided for completeness; never edit applied files).
- **No new columns.** `status` + `last_error` + `created_at_ns` already carry everything the terminal state needs (see D3). Existing `'pending'/'inflight'/'delivered'` rows are preserved verbatim by the rebuild.

### REQ-2 — Repository: attempt cap in the retry statement (mirror `RetryEventOutbox`) — **GAP**

- `internal/repository/billing_outbox.go` `RetryBillingUsage` signature becomes:
  `RetryBillingUsage(ctx context.Context, id, owner, lastErr string, next time.Time, maxAttempts int) error`
  with the statement
  `UPDATE billing_usage_outbox SET status = CASE WHEN attempts >= $1 THEN 'failed' ELSE 'pending' END, next_attempt_at_ns=$2, claim_owner='', claim_until_ns=0, last_error=$3 WHERE id=$4 AND status='inflight' AND claim_owner=$5`
  (placeholders renumbered in text order per I1; `s.rebind` applied).
- Guards retained/mirrored: `maxAttempts <= 0 → 1` (as `RetryEventOutbox`, `event_outbox.go:365`); `last_error` truncated to 512 bytes; `requireBillingClaim` fencing (`n != 1 → "billing usage claim lost"`).
- `BillingStore` interface updated (`billing_types.go:61`). The only other caller is `outbox.go:74`.
- **Claim path and due index unchanged:** `ClaimBillingUsage` (both dialects) and `billing_usage_due_idx` (`status, next_attempt_at_ns, claim_until_ns, created_at_ns`) stay as-is — a `'failed'` row matches neither `status='pending'` nor `status='inflight'`, so it is excluded from claims and from the due-index scan automatically (AC-3 falls out of the existing predicates).

### REQ-3 — Runtime: permanent-failure classification + terminal path — **GAP**

- `Runtime` gains `maxAttempts int`, set in `New` from `cfg.OutboxMaxAttempts`.
- `deliverFact` (`outbox.go:50-69`) error handling becomes:
  1. **`*apiError` with `400 <= Status < 500`** (via `errors.As`) → terminal immediately — a deterministic rejection (invalid dimension, bad fact; `client.go:147-153` proves every non-2xx arrives as `*apiError`).
  2. **Missing tenant binding** (`outbox.go:53`) → terminal immediately — the binding map is immutable for the process lifetime (config loaded once), so retry cannot succeed.
  3. **Unparseable metadata** (`outbox.go:58`) → terminal immediately — metadata is written by internal code (`ApplyBillingUsage`), so a parse failure is data corruption.
  4. **All other errors** (5xx, transport, token acquisition, context cancellation) → `retryFact`, which now passes `r.maxAttempts`; the repository lands the fact in terminal `'failed'` once `attempts >= maxAttempts`.
- New `failFact(ctx, fact, cause)` runtime method: persists via the same repository statement with `maxAttempts = fact.Attempts` (the `failImmediately` pattern, `event_outbox_relay.go:227-244` — the claim already incremented attempts, so `attempts >= maxAttempts` holds on the first write). On persistence failure: Warn log, no further action (mirrors `retryFact`'s persistence-failure handling). On success: Warn log with `fact_id`, `attempt`, and `cause` — operator visibility parity with the retry path; **no telemetry changes in scope** (the direction's visibility concern is addressed by the terminal transition itself becoming observable through the `failed` status + `last_error`).
- `retryFact` logging unchanged; "billing usage delivery deferred" now only fires for non-terminal deferrals. `billingBackoff` unchanged.

### REQ-4 — Config knob `BILLING_OUTBOX_MAX_ATTEMPTS` — **GAP**

- `BillingConfig` gains `OutboxMaxAttempts int`; `loadBillingConfig` reads `getEnvInt("BILLING_OUTBOX_MAX_ATTEMPTS", 10)` (`config_billing.go:45-55` block).
- `Validate` (`config_billing.go:127-151`) rejects `OutboxMaxAttempts < 1 || OutboxMaxAttempts > 1000` with a message mirroring `EVENT_OUTBOX_MAX_ATTEMPTS` bounds (`config_event_outbox.go:99-100`). Validation flows through `Config.Validate` → `config_validate.go:54` unchanged.
- Wiring: `billing.New` (`runtime.go:60-77`) → `Runtime.maxAttempts`.
- `.env.example` billing block gains the row after `BILLING_OUTBOX_CLAIM_TTL_SECONDS` (`.env.example:181`); `docs/configuration.md` billing table gains the row after `BILLING_OUTBOX_CLAIM_TTL_SECONDS` (`docs/configuration.md:248`).

### REQ-5 — Tests (the four acceptance checks, testable) — **GAP**

- **AC-1** → new file `internal/billing/outbox_test.go`, test **`TestOutboxMaxAttemptsExcludesFactFromClaim`** (the `TestOutbox` prefix is required for `-run TestOutbox`):
  - Real sqlite store (`openRuntimeTestStore` pattern, `runtime_test.go:17-32`); `Runtime{store, bindings: tenant→Client against httptest returning 500 (transient), maxAttempts: 2, logger: discard}`; seed facts via `ApplyBillingUsage`.
  - Claim (attempts=1) → `deliverFact` → transient failure → row returns to `'pending'` with a future `next_attempt_at_ns`.
  - Force due-ness without sleeping: `store.RetryBillingUsage(ctx, id, owner, "x", time.Now().Add(-time.Minute), 99)` (test plumbing; keeps attempts).
  - Claim again (attempts=2) → `deliverFact` → failure → repository terminal predicate (`2 >= 2`) → `'failed'`.
  - **Assert:** a subsequent `ClaimBillingUsage` returns **0 facts**; a recording `Store` wrapper (embeds `Store`, overrides `RetryBillingUsage` to record `maxAttempts`/`lastErr` then delegate) proves the final call passed `maxAttempts=2` and non-empty `lastErr`.
  - Gate: `go test ./internal/billing -run TestOutbox -count=1` passes.
- **AC-2** → same file, test **`TestOutboxHTTP4xxIsTerminalWithLastError`**:
  - httptest server: token endpoint returns a token; usage endpoint returns `400 {"error":"invalid_dimension"}` (plus a `422` variant); client built via `newClient` (pattern `client_test.go:32-85`).
  - Claim a fact → `deliverFact` → **Assert:** the recording store received the terminal write — `maxAttempts == fact.Attempts` (i.e., no retry budget left) and `lastErr` containing `status=400` — and a subsequent `ClaimBillingUsage` returns 0 rows (terminal `'failed'` with `last_error` persisted).
  - Control case proving classification is status-specific: same setup with a `500` response → row returns to `'pending'` (deferred, reclaimable after due) — i.e., 4xx goes terminal where 5xx does not.
- **AC-3** → `internal/repository/billing_test.go`, test **`TestBillingUsageTerminalFailedExcludedFromClaimAndDueScan`**:
  - `ApplyBillingUsage` → claim → `RetryBillingUsage(..., maxAttempts=fact.Attempts, next=past)` → row `'failed'`.
  - **Assert 1:** `ClaimBillingUsage(ctx, "worker", 32, ttl)` returns 0 rows — terminal rows are never returned by any claim.
  - **Assert 2 (due-index exclusion):** the failed row's `next_attempt_at_ns` is **in the past** (it would be due were it `'pending'`) yet it is not returned — the claim's `status='pending'` predicate excludes it from the `billing_usage_due_idx` scan (`status, next_attempt_at_ns, claim_until_ns, created_at_ns`).
  - **Assert 3 (cap boundary):** with `maxAttempts=2`, a row at `attempts=1` after a failed delivery returns to `'pending'` (reclaimable once due) while a row at `attempts=2` lands `'failed'` — pins the `attempts >= maxAttempts` predicate.
- **AC-4** → migration + convergence:
  - Inspection: 0043 up files (both dialects) include `'failed'` in the CHECK; every test's `repo.Migrate` exercises the migration (no applied-file edits per I2).
  - Convergence test (repository or billing): seed a fact, drive `attempts` to the cap via the claim + `RetryBillingUsage(next=past)` loop, then deliver with a failing client → **the fact's next failed attempt lands it terminal `'failed'` with `last_error`, and it is excluded from subsequent claims** — an in-flight poison pill already at cap converges on its next failed attempt, with no further retries.

---

## 4. Decisions & non-goals

- **D1 — One repository statement with the terminal predicate, not a separate `FailBillingUsage`.** `RetryBillingUsage` gains `maxAttempts` and decides `'failed'` vs `'pending'` atomically in the same UPDATE (`CASE WHEN attempts >= $1`), exactly mirroring `RetryEventOutbox` (`event_outbox.go:362-387`) and the relay's `failImmediately` (`event_outbox_relay.go:227-244`). Permanent classes (REQ-3.1-3.3) reuse it with `maxAttempts=fact.Attempts`. Rejected alternative: a dedicated `FailBillingUsage` method — a second write path with its own fencing to maintain, no atomicity or consistency benefit, and divergent from the established project pattern (the direction itself flags the divergence from the project's terminal-state patterns as the problem).
- **D2 — The 4xx class is inclusive `400..499` per the acceptance wording**, which includes 401 and 429. Rationale: 401 already invalidates the cached token (`client.go:121`), so the terminal attempt runs with a fresh token; 429-with-`Retry-After` handling is a refinement explicitly deferred (out of scope) — the attempt cap remains the safety net for legitimate-but-rate-limited facts. The control case in AC-2 (500 → retry) pins that 5xx stays retryable.
- **D3 — No `failed_at_ns` column and no retention/DELETE job.** The direction's problem statement notes unbounded table growth, but the acceptance list — the contract — contains no cleanup requirement; adding a prune would expand scope. The terminal state is the prerequisite that makes any future retention job possible, and `status='failed'` + `created_at_ns` are sufficient prune keys, so a future job needs **no** additional schema change. The 0042 audit pattern used a column because its outbox table has no status column (0039 schema); `billing_usage_outbox` already has `status` + `last_error`.
- **D4 — Default 10, bounds 1..1000**: mirrors `EVENT_OUTBOX_MAX_ATTEMPTS` (`config_event_outbox.go:32/:99-100`), keeping the two project outboxes consistent.
- **D5 — Zero-value `maxAttempts` on hand-built `Runtime` structs** (the existing test pattern `runtime_test.go:36-39`): the repository guard `maxAttempts <= 0 → 1` makes an unset runtime **fail closed** (terminal on the first failed attempt) rather than retry forever — the safe direction. Production always sets it via `New` + config validation. New tests must set `maxAttempts` explicitly (or rely on the closed default deliberately).
- **Non-goals:** retention/cleanup (D3); telemetry/metrics (no acceptance item; Warn logs now surface the terminal transition with cause, and the `failed` status + `last_error` are persisted for operator inspection); changes to `BillingUsageFact` (no `Status` field needed — `ClaimBillingUsage` returns only the rows it just claimed); changes to the claim query, due index, or `billingBackoff`; any change to the events/audit/webhook outboxes; changes to already-applied migrations (I2).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

The four supplied checks are preserved verbatim and mapped to the concrete, runnable criteria in REQ-5:

| # | Supplied acceptance check | Testable form (REQ-5) | Gate command |
|---|---|---|---|
| AC-1 | `go test ./internal/billing -run TestOutbox -count=1` passes with a new test asserting a fact whose Attempts exceeds MaxAttempts is excluded from subsequent `ClaimBillingUsage` | `TestOutboxMaxAttemptsExcludesFactFromClaim`: maxAttempts=2; claim→transient failure→force due→claim (attempts=2)→failure→terminal; subsequent `ClaimBillingUsage` returns 0; recording store proves final `maxAttempts=2`, non-empty `lastErr`. The `attempts >= maxAttempts` predicate (events precedent, D1) is pinned by driving attempts exactly to the cap. | `go test ./internal/billing -run TestOutbox -count=1` |
| AC-2 | `deliverFact` treats `apiError` status 4xx (e.g., 400/422) as non-retryable and persists a terminal `'failed'` status with `last_error` instead of calling `retryFact` | `TestOutboxHTTP4xxIsTerminalWithLastError`: 400/422 → terminal write (`maxAttempts == fact.Attempts`, `lastErr` contains `status=400`), excluded from claims; 500 control → back to `'pending'` | `go test ./internal/billing -run TestOutbox -count=1` |
| AC-3 | Repository test asserts `ClaimBillingUsage` never returns rows in the terminal state and that terminal facts are excluded from the due-index scan | `TestBillingUsageTerminalFailedExcludedFromClaimAndDueScan`: terminal row with past `next_attempt_at_ns` (would be due) never claimed; cap boundary 1<2 retries, 2>=2 terminal | `go test ./internal/repository -run TestBillingUsage -count=1` |
| AC-4 | `billing_usage_outbox` status CHECK constraint migration includes `'failed'` and existing in-flight poison pills converge to terminal state after their next failed attempt | 0043 sqlite+postgres up/down inspected (CHECK includes `'failed'`, exercised by every `Migrate`); convergence test: attempts driven to cap, next failed delivery → terminal, excluded | `go test ./internal/billing ./internal/repository -count=1` (plus `make check`) |

All changes respect the hard gates: `gofmt`/`go build`/`go vet`/`go test`, single files stay well under 500 lines (`outbox.go` 82 lines + ~25; `billing_outbox.go` ~150 + ~15; `config_billing.go` 165 + ~10), stdlib-only tests (AGENTS.md I6), dual migration files + `.down.sql` pairs (I2), `s.rebind` placeholder discipline (I1), and the CI baseline (SQLite; postgres 0043 is a declarative ALTER exercised by the integration gate).
