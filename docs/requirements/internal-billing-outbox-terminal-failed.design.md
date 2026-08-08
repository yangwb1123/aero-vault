# Design — `internal/billing` outbox terminal-failure handling (max attempts / dead-letter)

**Direction:** "Add terminal-failure handling (max attempts / dead-letter) to the billing usage outbox"
**Input spec:** `docs/requirements/internal-billing-outbox-terminal-failed.spec.md` (REQ-1…REQ-5, verified 2026-08-07)
**Baseline:** working tree at `add-terminal-failure-handling-max-attempts-dead--c33c33cf` (requirements stage PASS)

---

## 0. Verification ledger (evidence re-checked against the tree, not taken on faith)

| Claim (from requirements deliverable) | Re-verified location | Verdict |
|---|---|---|
| `deliverFact` has no error classification; all failures → `retryFact` | `internal/billing/outbox.go:50-69` (missing binding `:51-54` → `retryFact`; metadata `:56-60` → `retryFact`; `AppendUsage` err `:61-64` → `retryFact`; unconditional `RetryBillingUsage` at `:74`) | ✅ exact |
| `billingBackoff` 1 s base, ×2, 5-min cap + downward jitter, no attempt cap | `outbox.go:82-89` | ✅ exact |
| Claim predicates are only `pending`/`inflight`; `attempts` incremented, never compared | `internal/repository/billing_outbox.go:33-36` (PG), `:51-56` (SQLite), `attempts=attempts+1` at `:31`/`:77` | ✅ exact |
| CHECK at 0038 `:29`; table at `:24`; identical in both dialects | `internal/repository/migrations/{sqlite,postgres}/0038_snaplink_billing.up.sql` | ✅ exact |
| 0042 audit terminal precedent exists both dialects; audit table has **no status column** | `0042_audit_governance_terminal_failed.{up,down}.sql` (both dialects; column `failed_at_ns` approach) | ✅ exact |
| No DELETE path for billing outbox | `billing_outbox.go` is UPDATE-only; `grep -rn "DELETE FROM billing_usage"` → no matches; no prune method exists (only audit/events have prunes) | ✅ exact |
| `RetryEventOutbox` terminal predicate `CASE WHEN attempts >= $1 THEN 'failed'` | `internal/repository/event_outbox.go:362-387`; `maxAttempts <= 0 → 1` at `:365-369` | ✅ exact |
| Relay `failImmediately` passes `maxAttempts=fact.Attempts` | `internal/events/event_outbox_relay.go:227-244` | ✅ exact |
| `EVENT_OUTBOX_MAX_ATTEMPTS` default 10, bounds 1..1000 | `internal/config/config_event_outbox.go:32`, `:99-100` | ✅ exact |
| 4xx classification surface = `*apiError` via `decodeAPIError` | `internal/billing/client.go:147-153`, `models.go:60-67`; 401 invalidates token at `client.go:121` | ✅ exact |
| `RetryBillingUsage` has exactly one production call site | `outbox.go:74`; interface at `internal/repository/billing_types.go:61`; sole implementer `*sqlStore` (asserted `:64`); `billing.Store` embeds `repository.BillingStore` (`runtime.go:20`) | ✅ exact |
| Migration runner applies each file in a tx; `PRAGMA foreign_keys` set at connection open (no-op in-tx) | `internal/repository/sql.go:113-137` (`applyMigration` BeginTx), `sqlite.go:31` | ✅ exact |
| Sqlite rebuild precedent | `0002_multitenant.up.sql` (`CREATE …_new` → `INSERT SELECT` → `DROP` → `RENAME` → recreate indexes) | ✅ exact |
| Next free migration number = 0043 | `ls {sqlite,postgres}/0043*` → no such files; max in-tree is 0042 | ✅ exact |
| No `TestOutbox*` tests exist today | `grep -rn "TestOutbox" internal/billing internal/repository` → none | ✅ exact |
| `.env.example:181` / `docs/configuration.md:248` are the billing outbox block tails | both confirmed verbatim | ✅ exact |

**New evidence found during this design pass (not in the requirements deliverable):**

1. **`docs/snaplink-billing.md:72` is stale under this change:** "Accepted local mutations remain in the durable outbox and retry indefinitely." — the retry-forever claim becomes false once the cap/terminal state lands. This file is the billing integration's behavior documentation (linked from `docs/configuration.md`). The design therefore adds a **doc fix** (D6) beyond the spec's REQ-5 doc list; it is a consistency correction, not a scope expansion (no code, no acceptance change).
2. **The new `Validate` bounds check cannot break existing config tests:** `config_billing_test.go` exercises only `loadBillingConfig` (env path, default 10) and standalone `validateBillingURL`; `config_audit_governance_test.go:97` builds `BillingConfig{Enabled: true, …}` but calls only `validateCommercialCredentialSeparation`, never `BillingConfig.Validate()`. `Config.Validate()` → `validateCommercialIntegrations` (`config_validate.go:49-57`) passes the env-loaded config. Verified no hand-built `BillingConfig` reaches `Validate()`.
3. **Deterministic client construction for tests:** `client_test.go:60-64` builds `&tokenSource{client: credentials, now: time.Now}` with a `fakeCredentialsClient` stub — the AC-1/AC-2 tests need **no real token endpoint** (the existing tests prove this pattern; token acquisition failures are plain errors → retry path, which the 500-control case must not trip).
4. **Billing client already carries a receipt/idempotency mechanism:** `client.go:135` sends `Idempotency-Key: factID`; 2xx is the commit point (→ `CompleteBillingUsage`). The at-least-once contract and its 2xx-but-not-persisted loss boundary are **pre-existing and unchanged** by this design (see sibling disposition S4).
5. **PG constraint auto-name:** 0038 PG declares `status` with an inline column CHECK; Postgres auto-names it `billing_usage_outbox_status_check`. `DROP CONSTRAINT IF EXISTS` degrades safely if the name ever differs (no-op) — worst case a redundant duplicate constraint, never a failed migration (FM8).
6. **Empirical SQL verification (2026-08-07):** the exact 0043 sqlite up/down DDL and the new `RetryBillingUsage` UPDATE from §3.1/§3.3 were executed against a scratch SQLite DB with `PRAGMA foreign_keys = ON` inside a transaction (the repo runner's exact conditions, `sql.go:113-137` + `sqlite.go:31`). All assertions passed: migration up/down apply; a row at `attempts=7` survives the rebuild verbatim; `attempts=2 >= maxAttempts=2` lands `'failed'` while `attempts=1 < 2` lands `'pending'`; a `'failed'` row is excluded from the claim predicate even with `next_attempt_at_ns` backdated to 0 (due-index exclusion, AC-3); and the **pre-migration 3-value CHECK rejects an `INSERT ... status='failed'`** (constraint error 275), which is exactly the AC-4 gate — any repository test writing `'failed'` fails-fast if 0043 is absent or wrong.

---

## 1. Prior-attempt disposition (docs/auto/runs/ — every outstanding finding, resolved or rejected with evidence)

The gate re-checks outstanding findings from this pipeline's own directory and siblings. Disposition of every gate-relevant finding found:

### S1 — `extract-the-billing-durable-outbox-into-a-shared-38c0a3d2` (design gate **FAIL**: B1/H1/H2/B2/AC-6/F1–F10, all against the shared-kernel design `transactional-outbox-kernel-v1.design.md` §2.1)

That direction proposed **extracting** the billing/events/audit outboxes into a shared kernel — a superset of this direction. Its blockers concern kernel-level constructs this design does **not** introduce:

| Finding | Disposition | Evidence |
|---|---|---|
| **B1 (BLOCKER)** — per-batch `newClaimToken()` with `(string, error)` absent: on `rand.Read` failure the relay returns 32 hex **zeros** → same-owner generations share token `0000…` → stale generation's complete passes owner+token+live lease → silent loss | **REJECTED as not applicable** — this design adds **no claim token**. The billing outbox fences claims by `claim_owner` only (`billing_outbox.go:31/:77/:92/:140`); the owner is `uuid.NewString()` per Runtime (`runtime.go:46`). No new RNG-derived identity is introduced anywhere in the change. Token generation (`newTokenSource`, `token.go`) is untouched. | `billing_outbox.go` claim/complete/retry WHERE clauses; `runtime.go:46` |
| **H1 (HIGH)** — terminal-boundary telemetry drift (`OnRetried(terminal=false)` vs `Failed`) | **REJECTED as not applicable** — the kernel's `Hooks.OnRetried/Failed` pair does not exist in the billing runtime; the spec and this design make **telemetry a non-goal** (spec §4). Billing runtime today has zero outbox counters; none are added. | `internal/billing/*` contains no telemetry calls; spec §4 non-goals |
| **H2 (HIGH)** — bare-sentinel nil-deref: `errors.Unwrap(outbox.ErrTerminal).Error()` panics on the driver goroutine | **REJECTED as not applicable** — this design introduces **no `ErrTerminal` sentinel**. Terminality is decided by the repository `CASE WHEN attempts >= $1` predicate; `failFact`/`retryFact` pass a concrete, provably non-nil `cause` at every call site (`errors.New(...)` twice, the `*apiError`/transport error returned by `AppendUsage`), and `cause.Error()` is never unwrapped. | Design §3.2 (below); `outbox.go:51-64` existing non-nil error values |
| **B2 (BLOCKER)** — `complete`/`retry` parent context never named (kernel pinned `WithTimeout(context.Background(), …)`) | **RESOLVED by explicit design pin (unchanged behavior)** — this design keeps the existing context flow: `deliverFact` passes the `runOutbox` ctx to `retryFact`, `failFact`, and `CompleteBillingUsage`, exactly as today. If shutdown cancels the persistence write, the row stays `'inflight'` with `claim_until_ns` in the future and is reclaimed after the claim TTL (`billing_outbox.go:33-36` predicates) — at-least-once is preserved; duplicates are absorbed by the `Idempotency-Key: factID` header (`client.go:135`). Switching to `context.Background()` is deliberately **out of scope** (no behavior change outside the terminal state). | `outbox.go:50-80` (ctx threaded unchanged); `billing_outbox.go:33-36` reclaim predicate |
| **AC-6 location defect** — kernel AC-6 named `internal/service/file_delete_outbox_test.go` → import cycle | **RESOLVED by construction** — all tests in this design are in-package: `internal/billing/outbox_test.go` (billing already imports repository), `internal/repository/billing_test.go`, `internal/config/config_billing_test.go`. No new package, no import cycle. | Design §5 |
| **F1–F10 fake-Store coverage gaps** — kernel AC enumerated 8 items, 6 unpinned | **RESOLVED by construction** — this design maps **each** of the 4 supplied acceptance checks to exactly one named test with concrete assertions (Design §5), plus the 500-control and cap-boundary pins; no loose acceptance items remain. | Design §5 mapping table |

### S2 — `build-a-dual-backend-outbox-delivery-event-schem-75f5517b` (design gate **FAIL**: v2v5-digest-wire MUT-D findings)

**REJECTED as not applicable** — all findings concern the audit-governance L2 wire format (digest/redaction of `governanceWire` payloads, `internal/auditgovernance`). The billing outbox shares zero symbols with that path; its wire payload is the typed `usageRequest` (fact ID, dimension, quantity, occurredAt, metadata) over HTTPS with an idempotency key (`client.go:100-135`, `models.go:30-38`) — no digest, no redaction, no wire-format change in this design.

### S3 — `add-a-dedicated-durable-async-outbox-config-sect-ef2d0976` (gate **PASS**; implement timed out; code landed in-tree)

Its config-contract conventions are **adopted**: `EVENT_OUTBOX_MAX_ATTEMPTS` default 10 / bounds 1..1000 / error message format (`config_event_outbox.go:32/:99-100`) is mirrored verbatim for `BILLING_OUTBOX_MAX_ATTEMPTS`. One deliberate deviation, with evidence: the events config validates **unconditionally** (F3 convention), but `BillingConfig.Validate()` **early-returns when `!Enabled`** (`config_billing.go:127-129`) — this design follows the **existing BillingConfig structure** (all billing knobs — HTTPTimeout, OutboxPollMillis, ClaimTTL, batch size — are only validated in the Enabled branch), so the new bounds check lives in that same branch. This is not a regression: zero-value `Config{}` hand-built in tests never reaches `BillingConfig.Validate()` (verified, §0 item 2).

### S4 — `reuse-the-audit-governance-transactional-outbox--8327eed2` + `audit-sink-deleted-11-at-least-once-contract-review` (audit/events delivery-contract findings: receiver dedupe key, ordering, 2xx-commit-point)

**REJECTED as not applicable to the billing outbox, with evidence:** (a) the billing receiver dedupe key already exists — `Idempotency-Key: factID` (`client.go:135`, asserted in `client_test.go:37-39`); (b) the 2xx = commit-point boundary is pre-existing (`2xx → CompleteBillingUsage`, `outbox.go:66-69`) and unchanged; (c) no ordering guarantee is claimed or changed (per-fact goroutines in `deliverBatch`, `outbox.go:37-47`). The contract-review blockers were **doc-only increments for the audit sink design**; the billing outbox's contract surface is untouched by this direction (which is about the *terminal state*, not delivery semantics).

### S5 — `replace-the-hardcoded-audit-governance-block-wit-cd58c0a7` (gate **FAIL**: B1–B4 on `AuditSinkConfig` L2Variant assembly)

**REJECTED as not applicable** — findings concern `internal/config` audit-sink config derivation (`AuditSinkConfig`); zero shared symbols with `BillingConfig`/billing runtime. No config-assembly logic is added by this design (one env read + one bounds check in the existing billing Validate block).

### S6 — This run's own `DECISIONS.md` (requirements stage PASS)

No outstanding findings; the requirements deliverable's only follow-ups are REQ-1…REQ-5, all implemented by this design. The one spec point this design extends is D6 (doc staleness at `docs/snaplink-billing.md:72`, §0 item 1) — additive, evidence-backed, no acceptance impact.

---

## 2. API changes (complete list)

| Surface | Change | Breaking? |
|---|---|---|
| `repository.BillingStore.RetryBillingUsage` (`internal/repository/billing_types.go:61`) | Signature gains `maxAttempts int`: `RetryBillingUsage(ctx, id, owner, lastErr string, next time.Time, maxAttempts int) error` | Compile-time for the single implementor `*sqlStore` (`:64`) and the single production caller (`outbox.go:74`); both updated in the same change. No external implementors exist (`grep` over `internal/`, `cmd/`). |
| `*sqlStore.RetryBillingUsage` (`internal/repository/billing_outbox.go:136-147`) | New terminal predicate in the UPDATE (Design §3.1) | Same as above |
| `billing.Runtime` (`internal/billing/runtime.go`) | New unexported field `maxAttempts int`, set in `New` from `cfg.OutboxMaxAttempts` | None (unexported) |
| `billing.Runtime.deliverFact` / new `failFact` (`internal/billing/outbox.go`) | Error classification (Design §3.2); `retryFact` passes `r.maxAttempts` | None (unexported) |
| `config.BillingConfig` (`internal/config/config_billing.go`) | New field `OutboxMaxAttempts int`; env `BILLING_OUTBOX_MAX_ATTEMPTS` default 10; `Validate` bounds 1..1000 | Additive with default — no config break |
| Migrations | 4 new files `0043_billing_outbox_terminal_failed.{up,down}.sql` (sqlite + postgres) | Schema evolution per I2; up is data-preserving (Design §3.3) |
| Docs | `.env.example` (+1 row after `:181`), `docs/configuration.md` (+1 row after `:248`), `docs/snaplink-billing.md:72` (stale "retry indefinitely" fixed) | None |

**Deliberately unchanged:** `BillingUsageFact` shape (no `Status` field — claims return only rows just claimed), claim queries (both dialects), `billing_usage_due_idx`, `billingBackoff`, `Runtime.New` signature, `billing.Store` (inherits the interface change), REST/S3/CLI/SDK surface (billing is server-internal), `CompleteBillingUsage`.

---

## 3. Design

### 3.1 Repository — terminal predicate in the retry statement (REQ-2)

`internal/repository/billing_outbox.go` (replace `RetryBillingUsage` body; keep the `len(lastErr) > 512` truncation and `requireBillingClaim` fencing):

```go
func (s *sqlStore) RetryBillingUsage(
	ctx context.Context, id, owner, lastErr string, next time.Time, maxAttempts int,
) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if len(lastErr) > 512 {
		lastErr = lastErr[:512]
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE billing_usage_outbox
SET status = CASE WHEN attempts >= $1 THEN 'failed' ELSE 'pending' END,
    next_attempt_at_ns=$2, claim_owner='', claim_until_ns=0, last_error=$3
WHERE id=$4 AND status='inflight' AND claim_owner=$5`),
		maxAttempts, next.UTC().UnixNano(), strings.TrimSpace(lastErr), id, owner)
	return requireBillingClaim(result, err)
}
```

- **I1 discipline:** 5 distinct placeholders, text order `$1..$5`, `next.UTC().UnixNano()` computed once; `s.rebind` converts for SQLite in text order (`sql.go:41-43`).
- **Parity:** identical shape to `RetryEventOutbox` (`event_outbox.go:362-387`), including the `maxAttempts <= 0 → 1` guard (fail-closed for zero-value runtimes, spec D5).
- **Terminal rows are excluded from claims for free:** `ClaimBillingUsage` predicates are `status='pending'` / `status='inflight'` only (`billing_outbox.go:33-36`, `:51-56`); a `'failed'` row matches neither, so it is invisible to claims and to the `billing_usage_due_idx` scan (`status, next_attempt_at_ns, claim_until_ns, created_at_ns` — the leading `status` column means the index never seeks `'failed'` rows). **No claim-query or index change** (spec non-goal).
- Interface line (`billing_types.go:61`) and the `var _ BillingStore = (*sqlStore)(nil)` assertion (`:64`) updated together.

### 3.2 Runtime — permanent-failure classification + terminal path (REQ-3)

`internal/billing/runtime.go`: add `maxAttempts int` field; in `New`, `maxAttempts: cfg.OutboxMaxAttempts` (config validated at `New` via `cfg.Validate()`, `runtime.go:31-33`).

`internal/billing/outbox.go` — `deliverFact` gains classification; `failFact` is new; `retryFact` passes the cap:

```go
func (r *Runtime) deliverFact(ctx context.Context, fact repository.BillingUsageFact) {
	client, ok := r.bindings[fact.TenantID]
	if !ok {
		r.failFact(ctx, fact, errors.New("billing tenant binding missing"))
		return
	}
	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(fact.MetadataJSON), &metadata); err != nil {
		r.failFact(ctx, fact, errors.New("billing usage metadata invalid"))
		return
	}
	err := client.AppendUsage(ctx, fact.ID, fact.Dimension, fact.Quantity, fact.OccurredAt, metadata)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			r.failFact(ctx, fact, err)
			return
		}
		r.retryFact(ctx, fact, err)
		return
	}
	if err := r.store.CompleteBillingUsage(ctx, fact.ID, fact.ClaimOwner); err != nil {
		r.logger.Warn("billing usage acknowledgement failed", "fact_id", fact.ID, "err", err)
	}
}

// failFact lands a claimed fact in the terminal 'failed' state (spec D1,
// events failImmediately pattern, event_outbox_relay.go:227-244). The claim
// already incremented attempts, so maxAttempts=fact.Attempts makes the
// repository terminal predicate (attempts >= maxAttempts) hold on this write.
func (r *Runtime) failFact(ctx context.Context, fact repository.BillingUsageFact, cause error) {
	if err := r.store.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, cause.Error(), time.Now().UTC(), fact.Attempts); err != nil {
		r.logger.Warn("billing usage terminal persistence failed", "fact_id", fact.ID, "err", err)
		return
	}
	r.logger.Warn("billing usage delivery failed permanently", "fact_id", fact.ID,
		"attempt", fact.Attempts, "err", cause)
}

func (r *Runtime) retryFact(ctx context.Context, fact repository.BillingUsageFact, cause error) {
	delay := billingBackoff(fact.Attempts)
	next := time.Now().UTC().Add(delay)
	if err := r.store.RetryBillingUsage(ctx, fact.ID, fact.ClaimOwner, cause.Error(), next, r.maxAttempts); err != nil {
		r.logger.Warn("billing usage retry persistence failed", "fact_id", fact.ID, "err", err)
		return
	}
	r.logger.Warn("billing usage delivery deferred", "fact_id", fact.ID,
		"attempt", fact.Attempts, "retry_in", delay, "err", cause)
}
```

**Classification table (exhaustive over the error surface of `AppendUsage`):**

| Error source | Concrete type | Class | Action |
|---|---|---|---|
| HTTP 4xx response (incl. 401/422/429) | `*apiError` (`client.go:150`, `models.go:60-67`) | permanent | `failFact` (terminal immediately) |
| HTTP 5xx response | `*apiError` | transient | `retryFact` until cap |
| Missing tenant binding | `errors.New(...)` | permanent | `failFact` |
| Unparseable `metadata_json` | `errors.New(...)` | permanent (internal write path ⇒ data corruption) | `failFact` |
| Token acquisition / transport / body-decode failures | plain `errors.New(...)` (`client.go:107/:131/:150-151`) | transient | `retryFact` until cap |

- **4xx rationale (spec D2):** 401 already invalidates the cached token (`client.go:121`), so the *next* fact fetches a fresh token; parity with the events relay's `ErrSinkUnauthorized → failImmediately` (`event_outbox_relay.go:206-225`). 429-with-`Retry-After` honoring is explicitly deferred; the cap is the safety net (default 10).
- **Logging:** `retryFact` logging unchanged (spec REQ-3); the final terminal transition via the retry path still logs "deferred" with the terminal row state persisting — identical to the events relay `retry` (`event_outbox_relay.go:322-340`). `failFact` logs "delivery failed permanently" with `fact_id`/`attempt`/`cause` — operator visibility parity with the retry path. **No telemetry** (spec non-goal).
- **Context:** both `failFact` and `retryFact` use the `runOutbox` ctx, unchanged (S1/B2 disposition, Design §1).

### 3.3 Migrations (REQ-1, I2)

**`internal/repository/migrations/sqlite/0043_billing_outbox_terminal_failed.up.sql`** — rebuild per the `0002_multitenant.up.sql` precedent. The rebuilt table is the FK **child** (`billing_usage_operations` untouched), so `DROP TABLE` inside the runner tx is safe even with `PRAGMA foreign_keys = ON` at connection open (no table references `billing_usage_outbox`; the pragma is a no-op in-tx, `sql.go:113-137`). All 16 columns + UNIQUE + both indexes recreated exactly as 0038, with only the CHECK widened:

```sql
CREATE TABLE billing_usage_outbox_new (
  id                 TEXT PRIMARY KEY,
  operation_id       TEXT NOT NULL REFERENCES billing_usage_operations(operation_id) ON DELETE CASCADE,
  tenant_id          TEXT NOT NULL,
  dimension          TEXT NOT NULL,
  quantity           INTEGER NOT NULL CHECK (quantity > 0),
  occurred_at_ns     INTEGER NOT NULL,
  metadata_json      TEXT NOT NULL DEFAULT '{}',
  status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'inflight', 'delivered', 'failed')),
  attempts           INTEGER NOT NULL DEFAULT 0,
  next_attempt_at_ns INTEGER NOT NULL,
  claim_owner        TEXT NOT NULL DEFAULT '',
  claim_until_ns     INTEGER NOT NULL DEFAULT 0,
  last_error         TEXT NOT NULL DEFAULT '',
  created_at_ns      INTEGER NOT NULL,
  delivered_at_ns    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (operation_id, dimension)
);
INSERT INTO billing_usage_outbox_new (id, operation_id, tenant_id, dimension, quantity,
  occurred_at_ns, metadata_json, status, attempts, next_attempt_at_ns, claim_owner,
  claim_until_ns, last_error, created_at_ns, delivered_at_ns)
  SELECT id, operation_id, tenant_id, dimension, quantity, occurred_at_ns, metadata_json,
         status, attempts, next_attempt_at_ns, claim_owner, claim_until_ns, last_error,
         created_at_ns, delivered_at_ns
  FROM billing_usage_outbox;
DROP TABLE billing_usage_outbox;
ALTER TABLE billing_usage_outbox_new RENAME TO billing_usage_outbox;
CREATE INDEX billing_usage_due_idx
  ON billing_usage_outbox (status, next_attempt_at_ns, claim_until_ns, created_at_ns);
CREATE INDEX billing_usage_tenant_idx
  ON billing_usage_outbox (tenant_id, created_at_ns);
```

**`internal/repository/migrations/postgres/0043_billing_outbox_terminal_failed.up.sql`** — metadata-only constraint swap; 0038's inline column CHECK auto-names to `billing_usage_outbox_status_check`:

```sql
ALTER TABLE billing_usage_outbox
  DROP CONSTRAINT IF EXISTS billing_usage_outbox_status_check;
ALTER TABLE billing_usage_outbox
  ADD CONSTRAINT billing_usage_outbox_status_check
  CHECK (status IN ('pending', 'inflight', 'delivered', 'failed'));
```

**Down pairs (both dialects, never auto-executed per I2)** — the 3-value CHECK cannot admit `'failed'` rows, so the down first deletes them, then restores the constraint (sqlite via the same rebuild; postgres via drop/re-add). Design decision D7: the only executable down semantics are (a) delete-failed-then-rebuild or (b) full table DROP (the `0041_event_outbox.down.sql` precedent). We choose (a): it preserves pending/inflight/delivered facts and is no more destructive than necessary; the deleted rows are terminal poison pills that cannot be delivered anyway.

### 3.4 Config (REQ-4)

`internal/config/config_billing.go`:

- `BillingConfig` gains `OutboxMaxAttempts int` (after `ClaimTTLSeconds`).
- `loadBillingConfig` block (`:45-55`): `OutboxMaxAttempts: getEnvInt("BILLING_OUTBOX_MAX_ATTEMPTS", 10)`.
- `Validate` numeric block (`:140-146`): add

```go
if c.OutboxMaxAttempts <= 0 || c.OutboxMaxAttempts > 1000 {
	return errors.New("BILLING_OUTBOX_MAX_ATTEMPTS must be within 1..1000")
}
```

(mirrors `config_event_outbox.go:99-100` verbatim in message shape; placed inside the existing `!Enabled` early-return — §0 item 2 proves no existing test constructs a hand-built enabled `BillingConfig` through `Validate`).

Docs: `.env.example` — bare row `BILLING_OUTBOX_MAX_ATTEMPTS=10` after `:181` (the billing block is bare-values, per the sibling docs reviewer's verified convention); `docs/configuration.md` — row after `:248`:

```
| `BILLING_OUTBOX_MAX_ATTEMPTS` | `10` | Per-fact delivery cap (`1..1000`); a fact failing past the cap becomes terminal `failed` and is never reclaimed. |
```

### 3.5 Doc fix (D6)

`docs/snaplink-billing.md:72` — replace "Accepted local mutations remain in the durable outbox and retry indefinitely." with a terminal-state sentence, e.g.:

> Accepted local mutations remain in the durable outbox. Delivery retries with exponential backoff up to `BILLING_OUTBOX_MAX_ATTEMPTS` (default 10); permanent failures (HTTP 4xx rejections, a missing tenant binding, corrupt metadata) and facts past the cap land in terminal `failed` status with `last_error` set and are never reclaimed.

---

## 4. Compatibility constraints, failure modes, migration steps

### 4.1 Compatibility

1. **Config:** additive with default 10 — existing deployments boot unchanged.
2. **Behavioral delta on upgrade (intended, per the direction):** (a) facts failing past 10 attempts become terminal instead of retrying forever; (b) 4xx rejections are no longer retried — they die at the first attempt. Operator-visible via the `failed` status + `last_error` + the new Warn log line.
3. **Operator lowering the cap at runtime** (e.g., 100 → 10): pending facts with `attempts >= 10` converge to `failed` on their next failed delivery (`CASE` evaluates the current row's attempts); successful deliveries are unaffected. No data is lost — only undeliverable facts die.
4. **Schema:** 0043 up preserves all rows verbatim (sqlite rebuild is a copy; PG is metadata-only). No new columns; no index changes. Down pairs exist but are never auto-run (I2).
5. **Interface:** `RetryBillingUsage`'s signature change is contained — one interface, one implementation, one production caller (verified §0). `billing.Store` inherits automatically. No REST/S3/CLI/SDK surface involved.

### 4.2 Failure modes (new or altered)

| # | Mode | Behavior | Safety net |
|---|---|---|---|
| FM1 | `failFact`/`retryFact` persistence write fails (DB error, claim lost, ctx canceled at shutdown) | Warn log; row stays `'inflight'` with `claim_until_ns` in the future | Claim-TTL reclaim (`billing_outbox.go:33-36`); fact re-delivered or converged terminal at cap; duplicates absorbed by `Idempotency-Key` (`client.go:135`) |
| FM2 | `BILLING_OUTBOX_MAX_ATTEMPTS` out of 1..1000 | Startup fails in `Validate` (env path and `New` path) | Fail-closed at boot; operator fixes env |
| FM3 | Hand-built `Runtime` (tests only) with zero `maxAttempts` | Repo guard `<= 0 → 1` ⇒ first failure terminal | Fail-closed (spec D5); production always sets via `New` + validation |
| FM4 | 429 rate-limit responses | Treated terminal (inclusive 4xx, spec D2) | Default cap 10 means a fact survives 9 rate-limited attempts; `Retry-After` honoring deferred |
| FM5 | Snaplink 401 on a valid cached token (clock skew etc.) | Fact dies terminal; token already invalidated (`client.go:121`) so subsequent facts retry with a fresh token | Parity with events relay 401/403 terminal handling (`event_outbox_relay.go:206-225`) |
| FM6 | Wrong PG constraint auto-name (drift) | `DROP CONSTRAINT IF EXISTS` no-ops; `ADD` creates a second CHECK with the same predicate | Redundant constraint, migration still succeeds (never a failed upgrade); worst case cosmetic |
| FM7 | Rollback with `'failed'` rows present | Down migration deletes them (documented; never auto-run) | I2: down is manual, off-band |
| FM8 | Sqlite rebuild lock during upgrade | DDL + copy in one tx (WAL); readers block briefly at outbox scale | Standard DDL window; identical to 0002 precedent |

### 4.3 Migration/deployment steps

1. Land the change with `make check` green (unit tests exercise `repo.Migrate` on fresh DBs, which applies 0043 in both dialects' CI paths).
2. Ship the binary; on startup `Migrate` applies 0043 up (sqlite rebuild in-tx; PG constraint swap metadata-only). Rows preserved verbatim; nothing is reclaimed differently.
3. Post-upgrade convergence (AC-4): any in-flight poison pill with `attempts >= 10` (or with a permanent-failure class) lands terminal on its **next failed attempt** — within one poll interval + the 5-min backoff cap. Pending/delivered facts unaffected.
4. Operators observe terminal rows via `status='failed'` + `last_error`; the direction's growth concern is now bounded (failed rows stop accumulating attempts and stop hammering the API; a future retention job can prune by `status='failed'` + `created_at_ns` with **no further schema change** — spec D3).
5. Rollback = deploy the previous binary; 0043 down is never auto-run and, if ever used manually, drops terminal rows first (D7).

---

## 5. Testable acceptance mapping (REQ-5, 1:1 with the supplied checks)

All tests stdlib-only (I6), deterministic, no sleeps, real SQLite via the existing helpers (`openRuntimeTestStore` `runtime_test.go:17-32`; `openBillingTestStore` `billing_test.go:12-27`).

| # | Supplied acceptance check | Test (name / file / assertions) | Gate command |
|---|---|---|---|
| AC-1 | `go test ./internal/billing -run TestOutbox -count=1` passes with a new test asserting a fact whose Attempts exceeds MaxAttempts is excluded from subsequent `ClaimBillingUsage` | **`TestOutboxMaxAttemptsExcludesFactFromClaim`** — `internal/billing/outbox_test.go`. Runtime `{store, maxAttempts: 2, logger: discard}`; binding = `newClient(server.URL, server.Client(), &tokenSource{client: &fakeCredentialsClient{}, now: time.Now})` (deterministic, §0 item 3); usage endpoint returns 500. Seed via `ApplyBillingUsage`; claim → `deliverFact` → retry (`pending`, future due); force due via plumbing `store.RetryBillingUsage(ctx, id, owner, "x", past, 99)` (records `maxAttempts==99` — distinguishable); claim again (attempts=2) → `deliverFact` → 500 → terminal (`2 >= 2`). **Assert:** recording `Store` wrapper (embeds `Store`, overrides `RetryBillingUsage`, records `(maxAttempts, lastErr)`) saw final call `maxAttempts == 2`, `lastErr != ""`; a subsequent `ClaimBillingUsage(ctx, "worker", 32, time.Minute)` returns **0 facts**. | `go test ./internal/billing -run TestOutbox -count=1` |
| AC-2 | `deliverFact` treats `apiError` status 4xx (e.g., 400/422) as non-retryable and persists a terminal `'failed'` status with `last_error` instead of calling `retryFact` | **`TestOutboxHTTP4xxIsTerminalWithLastError`** — same file. Usage endpoint returns `400 {"error":"invalid_dimension"}` (subtests: 400, 422). Claim (attempts=1) → `deliverFact` → **Assert:** recording store received a write with `maxAttempts == fact.Attempts` (no retry budget left ⇒ `failFact` path, not `retryFact`) and `lastErr` contains `status=400`; subsequent `ClaimBillingUsage` → 0 rows. **Control:** same fixture, endpoint returns 500 → row returns to `'pending'` (write with `maxAttempts == runtime cap`), reclaimable after due — pins that the classification is status-specific. | `go test ./internal/billing -run TestOutbox -count=1` |
| AC-3 | Repository test asserts `ClaimBillingUsage` never returns rows in the terminal state and that terminal facts are excluded from the due-index scan | **`TestBillingUsageTerminalFailedExcludedFromClaimAndDueScan`** — `internal/repository/billing_test.go` (pure repository, no runtime). `ApplyBillingUsage` → claim (attempts=1) → `RetryBillingUsage(maxAttempts=1, next=past)` → row `'failed'`. **Assert 1:** `ClaimBillingUsage` → 0 rows. **Assert 2 (due-index exclusion):** the terminal write used `next` **in the past** (the row would be due were it `'pending'`), yet the claim still returns 0 — only the `status='pending'` predicate (served by the leading `status` column of `billing_usage_due_idx`) can explain the exclusion. **Assert 3 (cap boundary):** `maxAttempts=2`: `attempts=1` failure → `'pending'` and reclaimable when due; `attempts=2` failure → `'failed'`; pins `attempts >= maxAttempts`. This test **also proves AC-4's CHECK** — any of its `status='failed'` writes fail the migration's CHECK if 0043 didn't include `'failed'`. | `go test ./internal/repository -run TestBillingUsage -count=1` |
| AC-4 | `billing_usage_outbox` status CHECK constraint migration includes `'failed'` and existing in-flight poison pills converge to terminal state after their next failed attempt | (a) **Inspection:** 0043 up files (both dialects) include `'failed'` in the CHECK; down pairs restore the 3-value constraint (I2); exercised by every `repo.Migrate` in the test suite. (b) **`TestOutboxPoisonPillConvergesAtCap`** — `internal/billing/outbox_test.go`: `maxAttempts=3`; drive a pending row to `attempts=2` via claim + plumbing-retry cycles (simulating a pre-existing poison pill); next claim (attempts=3) + failing 500 client → terminal `'failed'` with `last_error` on that attempt; subsequent claim → 0. | `go test ./internal/billing ./internal/repository -count=1` (plus `make check`) |

**Config regression coverage (beyond the 4 ACs):** `internal/config/config_billing_test.go` gains `TestLoadBillingConfigDefaultsOutboxMaxAttempts` (env load ⇒ 10) and bounds cases (0 and 1001 rejected, 1000 accepted) via `t.Setenv` — pinned through the same `loadBillingConfig` path the existing tests use.

---

## 6. Change set & hard-gate compliance

| File | Change | Size budget |
|---|---|---|
| `internal/repository/migrations/{sqlite,postgres}/0043_billing_outbox_terminal_failed.{up,down}.sql` | new (4 files, I2 pairs) | ≤ 45 lines each |
| `internal/repository/billing_outbox.go` | `RetryBillingUsage` signature + `CASE` statement (+3 lines) | 165 → ~168 |
| `internal/repository/billing_types.go` | interface line (+1) | ≤ 70 |
| `internal/billing/outbox.go` | classification + `failFact` (+~30) | 95 → ~125 |
| `internal/billing/runtime.go` | `maxAttempts` field + `New` wiring (+2) | ≤ 190 |
| `internal/billing/outbox_test.go` | new (AC-1, AC-2, AC-4) | ~230 |
| `internal/repository/billing_test.go` | +`TestBillingUsageTerminalFailedExcludedFromClaimAndDueScan` | 109 → ~170 |
| `internal/config/config_billing.go` | field + env + bounds (+8) | 187 → ~195 |
| `internal/config/config_billing_test.go` | +3 cases | ~60 → ~100 |
| `.env.example`, `docs/configuration.md`, `docs/snaplink-billing.md` | doc rows / stale-line fix | — |

**Hard gates:** every file stays far below 500 lines (max ~230); `gofmt` clean (code above is gofmt-shaped); `go build ./...` / `go vet ./...` (no new packages, no new deps — I6: stdlib `errors.As` only); `go test ./...` (SQLite+local FS, zero network in tests — httptest is loopback); I1 (placeholder discipline, written out in §3.1); I2 (dual-file pairs, down never auto-run); single-file edits only, no applied-migration edits. `billing_test.go`'s claim of 2 facts per mutation (`billing_test.go:68-105` already asserts the count) makes the fixtures reuse-ready.

**VERDICT-relevant summary:** all six evidence citations confirmed verbatim; every outstanding finding from sibling gates dispositioned with evidence (S1 B1/H1/H2 rejected as inapplicable — no tokens/sentinels/telemetry introduced; B2 pinned unchanged; AC-6/F1–F10 resolved by construction; S2/S4/S5 rejected as unrelated modules; S3 conventions adopted with the one deviation proven safe); REQ-1…REQ-5 implemented 1:1; the four acceptance checks map to runnable named tests with gate commands.
