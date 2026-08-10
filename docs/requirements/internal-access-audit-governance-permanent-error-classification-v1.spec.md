# Requirements Specification — `internal/access`: permanent-error classification → terminal within ≤1 attempt + pending-row partial index (contract item 1, T-3)

**Module:** `internal/access` (analysis label; implementation surface is `internal/auditgovernance` + `internal/repository` — see §1)
**Direction:** "Permanent-error classification → terminal within ≤1 attempt + pending-row partial index (contract item 1, T-3)" (direction 2)
**Source analysis:** `docs/auto/analyses/internal-access-f4571c58.json`
**Contract:** `docs/campaigns/implementation-gate.md:21` (gate item 1: dead-letter terminal state — status/dead_at columns + partial index; 422/409/tenant-mismatch/invalid-receipt → terminal ≤1 attempt; transient bounded retry cap 300 s; dead rows excluded from claim/lag; T-3)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` (verification basis = this checkout)
**Score:** value 8 / risk reduction 8 / effort 4 / confidence 8

---

## 1. Module & scope

The analysis file labels this direction under `internal/access` — the scope/authorization layer whose decisions feed `RecordAudit`/`InsertEvent`. **No cited evidence or required change lives in `internal/access/` itself** (verified: `grep -rn "RecordAudit" internal/access/` → no hits). The audit-governance delivery pipeline implementing the contract lives in `internal/auditgovernance` (relay, HTTP publisher, runtime) and `internal/repository` (outbox schema, claim/lag/fail/cleanup queries). The module label is retained for traceability to the analysis; all requirements below target those two packages.

**Problem (verified):** `deliverFact` (`internal/auditgovernance/relay.go:80-102`) treats **only** `ErrReceiptConflict` as terminal (`:84` → `failFact` `:85-89`). Every other permanent rejection — HTTP **409/422** (surfaced as `*httpStatusError` from `validateReceipt`'s non-202 branch, `http.go:178-182`) and **tenant-mismatch/invalid-receipt** (`ErrInvalidReceipt`, `model.go:26`, from `receiptMatches` mismatch `http.go:214-225` and receipt-shape failures `http.go:186/190/194`) — falls into `retryFact` (`:90-92`) and is retried **forever**: `boundedBackoff` (`relay.go:163-179`) caps the per-delay value at `maxBackoff` (300 s default, `internal/config/config_audit_governance.go:65`), not the retry horizon — there is no attempt cap anywhere (`grep MaxAttempts internal/auditgovernance internal/config` → empty; `RetryAuditGovernance` `audit_governance_claim.go:137-152` has no attempt predicate).

**Terminal-state shape (verified):** migration `0042` (both dialects) adds `failed_at_ns INTEGER/BIGINT NOT NULL DEFAULT 0` — a **timestamp column**, whereas the contract (`implementation-gate.md:21`) prescribes **`status`/`dead_at` columns** (the events outbox 0041 is status-led: `status TEXT CHECK (... 'pending','inflight','delivered','failed')` + `event_outbox_due_idx (status, available_at_ns, ...)`). Additionally there is **no partial index over pending rows**: claim/lag filter `delivered_at_ns=0 AND failed_at_ns=0` (`audit_governance_claim.go:38/62/195`), but the only due index (`0039`, both dialects) is `audit_governance_due_idx (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` — a full index lacking `failed_at_ns`, so pending-row lookups re-scan.

**In scope:** ① a permanent-error classifier replacing the single-sentinel branch in `deliverFact`; ② a new migration adding the pending-row partial index(es) with EXPLAIN-verified index use on both dialects; ③ documentation of the `status`/`dead_at` vs `failed_at_ns` deviation (contract-compliant path given I2, see REQ-4); ④ tests pinning T-3. **Out of scope:** B3-2 (`Ready()` maxLag flip / degraded state), B3-3 (deterministic fact IDs), B3-4 (relay telemetry), any attempt-cap configuration, events-outbox changes, any `cmd/server` change.

---

## 2. Evidence verification

Every citation in the direction was checked against this checkout (`acfaaf4`).

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `relay.go:deliverFact/retryFact/failFact/classifyRelayError` — "only receipt conflict:true is terminal" | `deliverFact` `:80-102`; single sentinel `:84` `if errors.Is(err, ErrReceiptConflict)` → `failFact` `:85-89`; all other errors → `retryFact` `:90-92`; `failFact` `:111-122` (terminal-with-retention, claim-loss only warned); `retryFact` `:124-137`; `classifyRelayError` `:181-190` (log label only) | ✅ **exact.** `:84` is the only terminal path in the relay. |
| E2 | "409/422 and tenant-mismatch/invalid-receipt fall into retryFact" — `http.go:validateReceipt/receiptMatches` | `validateReceipt` `http.go:178-206`: non-202 → `&httpStatusError{Status: response.StatusCode}` `:182` (so 409/422 land here); media-type/body-size/JSON errors → `ErrInvalidReceipt` `:186/:190/:194`; conflict → `ErrReceiptConflict` `:201`; `receiptMatches` false → `ErrInvalidReceipt` `:204`; `receiptMatches` `:214-225` requires `EventID==fact.ID` (`:217`), `TenantID==fact.TenantID` (`:217`), non-zero `AcceptedAt` (`:218`), status ∈ {ledgered, indexed, archived} (`:222-225`); 401 also invalidates the token in `Publish` `:126-127` | ✅ **exact.** 409/422/`ErrInvalidReceipt` are precisely the errors flowing to `retryFact`. Sentinels in `model.go`: `ErrInvalidReceipt` `:26`, `ErrReceiptConflict` `:27`, `httpStatusError{Status int}` + `Error()` `:31-37` |
| E3 | "boundedBackoff caps the delay, not the retry horizon" | `boundedBackoff` `relay.go:163-179` — `return min(max(jittered, initial/2), maximum)`; `internal/config/config_audit_governance.go:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)`; `grep -rn "MaxAttempts" internal/auditgovernance/` → no hits (the only `MaxAttempts` in `internal/config/` is the **events outbox** knob `EVENT_OUTBOX_MAX_ATTEMPTS`, `config_event_outbox.go:20/:32` — a different pipeline, not the governance relay); `RetryAuditGovernance` `audit_governance_claim.go:137-152` takes `(id, owner, token, lastErr, next time.Time)` — no attempt predicate | ✅ **holds.** Per-delay cap only; governance retry horizon unbounded. |
| E4 | 0042 "adds failed_at_ns instead of status/dead_at on 0039" | `internal/repository/migrations/sqlite/0042_audit_governance_terminal_failed.up.sql` + `postgres/...up.sql`: `ADD COLUMN failed_at_ns INTEGER/BIGINT NOT NULL DEFAULT 0` (down: `DROP COLUMN`); 0039 table has **no status column** (schema is `delivered_at_ns`-based); 0041 `event_outbox` is status-led (`status TEXT ... CHECK IN ('pending','inflight','delivered','failed')`) | ✅ **exact.** Shape deviation from contract `implementation-gate.md:21` confirmed. |
| E5 | "no partial index over pending rows (claim/lag filter delivered_at_ns=0 AND failed_at_ns=0; audit_governance_due_idx lacks failed_at_ns)" | Claim predicates `delivered_at_ns=0 AND failed_at_ns=0`: `audit_governance_claim.go:38` (Postgres `claimAuditGovernancePostgres`), `:62` (SQLite `claimAuditGovernanceSQLite`), `:88` (`claimAuditGovernanceIDs`); `OldestPendingAuditGovernance` `:188-201` (`MIN(o.created_at_ns) ... AND o.failed_at_ns=0`); `HasPendingDrainingAuditGovernance` `:202-210`; `FailAuditGovernance` `:159-172` (sets `failed_at_ns`, fenced `WHERE ... failed_at_ns=0 AND claim_owner=$4 AND claim_token=$5 AND lease_expires_at_ns > $6`); `CleanupFailedAuditGovernance` `audit_governance_cleanup.go:113`; 0039 indexes (both dialects): `audit_governance_due_idx (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` + `audit_governance_tenant_idx` — no `failed_at_ns`, no partial predicate; contrast 0041 `event_outbox_due_idx (status, available_at_ns, lease_expires_at_ns, created_at_ns)` ("Status-led due index") | ✅ **all exact.** Only the two full indexes exist on `audit_governance_outbox`; pending filters are index-unserved. |
| E6 | `config_audit_governance.go:65` MaxBackoffSeconds default 300 | `internal/config/config_audit_governance.go:65`: `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` | ✅ **exact.** |
| E7 | `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` | `internal/repository/audit_governance_test.go:334`: fencing (stale owner/token cannot fail, `:344-347`), terminal (never re-claimed `:350-352`, never pending `:353-355`), retention prune before/after window `:358-365`, no origin tombstone → re-enqueueable after prune `:368-371` | ✅ **exact.** Pins the repository-level terminal semantics the classifier will now feed more classes into. |
| E8 | "extend TestRuntimeConflictingReceiptIsTerminalWithRetention" | `internal/auditgovernance/runtime_test.go:117-186`: httptest sink answering 202+`conflict:true`; atomic `posts` counter; asserts `posts==1` (`:163-171`), `ClaimAuditGovernance` → 0 rows (`:173-175`), `OldestPendingAuditGovernance` → not pending (`:176-177`), retention prune before→0 / after→1 (`:180-186`). Harness `runtimeConfig` `:39-46` (poll 10 ms, initial backoff 1 s, max backoff 2 s, retention 3600 s) | ✅ **exact.** This is the template and extension point. |
| E9 | "extend TestBoundedBackoffIsDeterministicAndCapped" | `internal/auditgovernance/runtime_test.go:189-196`: determinism (`first == second`) + cap within `[initial/2, maximum]` for `maximum=5 s` | ✅ **exact.** Extension needed to pin the contract's 300 s default. |
| E10 | EXPLAIN precedent + both-dialect testing hooks | SQLite: `internal/repository/perf_probe_test.go:170` `TestProbeQueryPlans` (`EXPLAIN QUERY PLAN` on raw `*sql.DB`, scan `id,parent,notused,detail`) — currently `t.Log` only, no assertion; migration files are reachable from package-internal tests via `//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql` (`internal/repository/sql.go:21`, unexported `migrationsFS`). Postgres: `internal/integration/audit_governance_postgres_test.go` (`//go:build integration`) with skip-if-unreachable pattern (`postgres_integration_test.go:41-46` `t.Skipf("no Postgres at %s: %v", ...)`) | ✅ **both mechanisms present.** |
| E11 | Contract item 1 / T-3 | `docs/campaigns/implementation-gate.md:21`: "死信终态（F3）：status/dead_at 列 + 部分索引；移植 sink `DeliveryError.Permanent` 分类（422/409/tenant mismatch/无效回执 → 终态 ≤1 次尝试）；瞬态有界重试 cap 300s；dead 行排除出 claim/lag · T-3：422 → 一个周期内终态；`Ready()` 含 dead 行 = true；批次继续" | ✅ **contract located.** The dead-row lag exclusion (`Ready()` = true when only dead rows exist) is already satisfied by `OldestPendingAuditGovernance`'s `failed_at_ns=0` predicate (E5). |
| E12 | I2 constraint on editing applied migrations | 0042 files are tracked/committed at HEAD (`git ls-files` + `git log` on `0042_audit_governance_terminal_failed.up.sql`); AGENTS.md I2 forbids editing applied migration files | ✅ **holds.** The partial index and deviation documentation must land in a **new** migration `0043`. |

---

## 3. Requirements

### REQ-1 — Permanent-error classifier

Add `isPermanentDeliveryError(err error) bool` to `internal/auditgovernance/relay.go`, adjacent to `classifyRelayError` (`:181-190`):

- **Permanent (terminal-with-retention):** `ErrReceiptConflict` · `ErrInvalidReceipt` · `*httpStatusError` with `Status == 409` or `Status == 422`.
- **Transient (bounded-backoff retry):** every other error — all other `httpStatusError` statuses (401, 403, 400, 429, 5xx), `ErrInvalidEvent`, `ErrTokenUnavailable`, token-source errors, transport/network errors, context errors.
- Classify via `errors.Is` / `errors.As` so wrapped sentinels classify identically.
- Exhaustive by construction: permanent membership is an explicit closed list; anything not in it is transient. No status-code ranges, no substring matching.

### REQ-2 — `deliverFact` uses the classifier (single-sentinel replacement)

In `deliverFact` (`relay.go:80-102`), replace the single `errors.Is(err, ErrReceiptConflict)` branch (`:84`) with `isPermanentDeliveryError(err)`:

- Permanent → `failFact` (existing `:85-89` path unchanged: `failed_at_ns` set, `last_error` retained, row kept until `CleanupFailedAuditGovernance` after the retention window — terminal-with-retention).
- Transient → `retryFact` (existing `:90-92` path unchanged).
- **`ErrReceiptConflict` behavior must be byte-identical to today** — `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117`) must pass unmodified.
- Update the branch comment to document the four terminal classes (conflict receipt / invalid receipt / HTTP 409 / HTTP 422) and the transient remainder, and cross-reference `classifyRelayError` (kept as-is, log label only) for sentinel-list consistency.

### REQ-3 — Migration `0043`: pending-row partial index (both dialects)

New migration pair `internal/repository/migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` (I2: 0042 is committed — do not edit it; `0043` is the next free number, verified `0042` is the current max).

- **up:** create partial index(es) on `audit_governance_outbox` with the exact predicate `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`, column lists chosen so that **both** pending access paths resolve through a partial index (recommended shapes, final choice is implementer's subject to the EXPLAIN evidence in REQ-5.3):
  - claim path — `(available_at_ns, lease_expires_at_ns, created_at_ns, id)` — serves `WHERE ... AND available_at_ns <= $N AND lease_expires_at_ns <= $N ... ORDER BY available_at_ns, created_at_ns, id LIMIT $N` (`audit_governance_claim.go:37-39/:61-64`);
  - lag path — `(created_at_ns)` — serves `MIN(o.created_at_ns)` in `OldestPendingAuditGovernance` (`:189-196`) and the `EXISTS` in `HasPendingDrainingAuditGovernance` (`:202-210`).
  - **down:** drop the created index(es) (reversible; no column changes so `down` needs no other work).
- Naming follows the `audit_governance_*_idx` convention; the existing full `audit_governance_due_idx`/`audit_governance_tenant_idx` from 0039 are **not** touched.
- Both dialects use the same DDL (column types differ only in `INTEGER` vs `BIGINT`, irrelevant to indexes).

### REQ-4 — `status`/`dead_at` vs `failed_at_ns`: document the deviation (contract-compliant path)

The contract (`implementation-gate.md:21`) prescribes `status`/`dead_at` columns; 0042 shipped `failed_at_ns` and is committed (E12). **Alignment by renaming is explicitly not required** (it would need a new migration renaming a column on a shipped table for zero behavioral gain — claim/lag already key on `failed_at_ns=0`, E5). Instead:

- The `0043` up migration header comment (both dialects) must document the deviation: contract shape `status`/`dead_at` → shipped shape `failed_at_ns` (0042), rationale (0039 schema is timestamp-led with no status column; claim/lag predicates already exclude `failed_at_ns != 0`; `CleanupFailedAuditGovernance` prunes by `failed_at_ns` + retention), and the reference to `implementation-gate.md:21` item 1.
- The `failFact` doc comment (`relay.go:111`) is updated to reference the same deviation note (one line).
- No edits to 0039/0042/0041 files (I2).

### REQ-5 — Tests

**REQ-5.1 — Runtime terminal tests (T-3; extend the `TestRuntimeConflictingReceiptIsTerminalWithRetention` pattern).** New table-driven test(s) in `internal/auditgovernance/relay_terminal_test.go` (new file — `runtime_test.go` is 400/500 lines, see D5), reusing the exact harness of `runtime_test.go:117-186` (`runtimeConfig` `:39-46`, httptest sink with `/token` handler, atomic POST counter, `repository.Open(sqlite)` + `Migrate`, `WrapRepository`, `runtime.Start`, poll-until-first-POST with 3 s deadline):

- Table over the four permanent classes: (a) HTTP **409**, (b) HTTP **422** — sink answers status only, no body (→ `httpStatusError`, `http.go:182`); (c) **tenant-mismatch** — 202 + receipt with `tenant_id != fact.TenantID` (→ `receiptMatches` false, `http.go:217`); (d) **non-ledgered status** — 202 + receipt with `status:"rejected"` (→ `http.go:222-225`); (e) **unparseable body** — 202 + body `not-json` (→ `http.go:194`).
- Per case, after `runtime.Close()` and an observe window **≥ 2.5 s** (must exceed the harness max backoff 2 s + poll slack so a misclassified-transient row would have re-POSTed): `posts.Load() == 1` (exactly one attempt, never re-POSTed); `store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute)` → `len == 0` (never re-claimed); `store.OldestPendingAuditGovernance(ctx)` → `ok == false` (absent from lag — discriminates even inside the backoff window, its SQL has no `available_at_ns` filter); retention block verbatim from `runtime_test.go:180-186` (`CleanupFailedAuditGovernance` before window → 0, after window → 1) for at least one case (409).
- **REQ-5.1a — transient boundary pin (classifier unit test):** closed-list table test over `isPermanentDeliveryError`: permanent {`ErrReceiptConflict`, `ErrInvalidReceipt`, `&httpStatusError{409}`, `&httpStatusError{422}`, and each wrapped via `fmt.Errorf("%w")`}; transient {`&httpStatusError{400,401,403,429,500,503}`, `ErrInvalidEvent`, `ErrTokenUnavailable`, `errors.New("transport")`, context.DeadlineExceeded}.

**REQ-5.2 — Transient retry cap pinned to the 300 s contract default (extend `TestBoundedBackoffIsDeterministicAndCapped`, `runtime_test.go:189-196`).** Add a case: `boundedBackoff("fact-1", 20, time.Second, 300*time.Second)` (the `config_audit_governance.go:65` default) must be deterministic and within `[500 ms, 300 s]` — the contract's "瞬态有界重试 cap 300s" pin. No wall-clock dependence (pure function). `runtime_test.go` stays ≤ 500 lines (currently 400; the extension is ~10 lines).

**REQ-5.3 — EXPLAIN QUERY PLAN shows index use on both dialects.** New package-internal test file `internal/repository/audit_governance_pending_idx_test.go` (`package repository` — needs raw DB + `migrationsFS`, both unexported):

- Seed an outbox row + binding (store calls or raw SQL), then probe the two pending shapes with `EXPLAIN QUERY PLAN` (SQLite, pattern of `perf_probe_test.go:170`): the claim SELECT (`audit_governance_claim.go:58-64` shape) and the lag `MIN(created_at_ns)` (`:189-196` shape). **Assert** (not just `t.Log`) that each plan detail names the corresponding 0043 partial index and does **not** contain `SCAN audit_governance_outbox` (no full-table scan).
- **REQ-5.3a — direct terminal-flag read:** same file, raw `SELECT failed_at_ns, attempts FROM audit_governance_outbox` after `FailAuditGovernance` → `failed_at_ns > 0` and `attempts == 1` (the "exactly 1 attempt" anchor; `FailAuditGovernance` is the sole writer of `failed_at_ns`).
- **REQ-5.3b — Postgres dialect (integration, `//go:build integration`):** extend `internal/integration/audit_governance_postgres_test.go` with a plan probe using `EXPLAIN (FORMAT TEXT)` (Postgres form of "EXPLAIN QUERY PLAN") on the claim SELECT and lag SELECT against a seeded outbox; assert the plan names the partial index and contains no `Seq Scan on audit_governance_outbox`. Follow the skip-if-unreachable pattern (`postgres_integration_test.go:41-46`); lives outside the CI gate (`make check` = SQLite only).

**REQ-5.4 — Deviation documentation pinned.** In `audit_governance_pending_idx_test.go`: read `migrationsFS.ReadFile("migrations/sqlite/0043_audit_governance_pending_partial_index.up.sql")` and the postgres counterpart and assert the header comment documents the deviation (contains the tokens `failed_at_ns`, `status`/`dead_at`, and `implementation-gate`); assert `0042` up files still exist unmodified (contain `failed_at_ns`, no `CREATE INDEX`) — pins the documented-deviation path, not a silent re-ship.

### REQ-6 — No other changes

- **No attempt cap, no new configuration** (`MaxAttempts` or env knob): the direction replaces retry-forever with classification, not a counter. `retryFact`/`boundedBackoff`/`failFact` semantics untouched.
- **No other repository/schema changes** beyond 0043: `failed_at_ns` plumbing, `FailAuditGovernance`, claim/lag exclusion, `CleanupFailedAuditGovernance` already exist (E5).
- **No `internal/access/` changes** (module label only, §1), **no `cmd/server` changes** (`readyzHandler`/`runtimeReadiness` behavior for terminal rows is already correct once the relay stops re-claiming them — the 503 flip is B3-2, out of scope), **no events-outbox changes**.

---

## 4. Decisions & non-goals

- **D1 — Classifier lives in `relay.go` next to `classifyRelayError`** (relay policy over the sentinel set, not part of the error model in `model.go`); unexported.
- **D2 — Permanent list is exactly {conflict, invalid-receipt, 409, 422}.** 400/404/429 are not permanent (429 is backpressure; 400/404 are outside the direction's cited classes). REQ-5.1a pins the boundary in both directions.
- **D3 — Index shape is evidence-driven:** REQ-3 recommends column lists; the hard requirement is the `WHERE delivered_at_ns=0 AND failed_at_ns=0` predicate + REQ-5.3's EXPLAIN evidence on both dialects. If the recommended shapes fail the plan assertion on a dialect, the implementer adjusts columns (still 0043, still same predicate) — the evidence, not the column list, is the contract.
- **D4 — Deviation documented, not renamed.** 0042 is committed (E12/I2); `status`/`dead_at` alignment would be a rename migration with zero behavioral change (claim/lag already exclude failed rows). REQ-4 + REQ-5.4 make the documentation testable.
- **D5 — New test files for the line-count gate:** `runtime_test.go` is 400/500 lines and `audit_governance_test.go` is external-package (`repository_test`, no raw DB/migrationsFS); REQ-5.1/5.3/5.4 land in new files (`relay_terminal_test.go`, `audit_governance_pending_idx_test.go`) — a test-side file split reusing existing helpers, not a scope change. `classifyRelayError` cross-reference keeps the two sentinel enumerations from drifting.
- **Non-goals:** B3-2 (`Ready()` maxLag flip, degraded state, 450 s alert), B3-3 (deterministic fact IDs / receiver IdempotencyKey stability), B3-4 (relay telemetry), B3-6 (`Validate()` empty bindings), events-outbox behavior, any `cmd/server` or `internal/access` handler change, any attempt-cap configuration.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**T-3.1 — 409/422/tenant-mismatch/invalid-receipt → terminal within exactly 1 attempt.** *"httptest-based runtime test returning 409/422/tenant-mismatch/invalid-receipt → row terminal after exactly 1 attempt (failed_at_ns>0, never re-claimed, absent from OldestPendingAuditGovernance, pruned only post-retention)."*
*Testable (REQ-5.1, REQ-5.3a):* table over {409, 422, tenant-mismatch receipt, non-ledgered status receipt, unparseable body}. Per case: `posts == 1` after first-POST (3 s deadline) + observe window ≥ 2.5 s (> harness max backoff 2 s) + `runtime.Close()`; `ClaimAuditGovernance` → 0 rows; `OldestPendingAuditGovernance` → `ok == false`; retention: `CleanupFailedAuditGovernance(now-1h)` → 0, `(now+1h)` → 1 (409 case). "Exactly 1 attempt" anchored twice: `posts == 1` (runtime) and raw `attempts == 1` + `failed_at_ns > 0` (repository-level read, REQ-5.3a). `TestRuntimeConflictingReceiptIsTerminalWithRetention` itself passes **unmodified** (byte-identical conflict behavior, REQ-2).

**T-3.2 — Transient retries pinned to the 300 s cap.** *"transient retries pinned to now+300s cap (extend TestBoundedBackoffIsDeterministicAndCapped)."*
*Testable (REQ-5.2):* `boundedBackoff("fact-1", 20, time.Second, 300*time.Second)` deterministic and within `[500 ms, 300 s]` — the contract's 300 s per-delay cap (config default `:65`), provable without wall-clock. Transient classes stay on the retry path: classifier unit table (REQ-5.1a) pins 401/403/400/429/5xx/`ErrInvalidEvent`/`ErrTokenUnavailable`/transport as non-permanent.

**T-3.3 — Pending-row partial index with EXPLAIN-verified index use.** *"migration gains partial index WHERE delivered_at_ns=0 AND failed_at_ns=0 with EXPLAIN QUERY PLAN on claim/lag showing index use on both dialects."*
*Testable (REQ-3, REQ-5.3, REQ-5.3b):* migration `0043` (both dialects, up/down) with predicate `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; SQLite unit probe asserts the claim and lag plans name the partial index and contain no `SCAN audit_governance_outbox`; Postgres integration probe (`EXPLAIN (FORMAT TEXT)`, `//go:build integration`, skip-if-unreachable) asserts index use and no `Seq Scan on audit_governance_outbox`.

**T-3.4 — Naming deviation resolved.** *"status/dead_at vs failed_at_ns naming aligned with contract or deviation documented in 0039/0042."*
*Testable (REQ-4, REQ-5.4):* deviation is documented (not renamed — D4): 0043 header comments (both dialects) state the contract shape (`status`/`dead_at` per `implementation-gate.md:21`), the shipped shape (`failed_at_ns`, 0042), and the rationale; repository test asserts the comment tokens and that 0042 files are unchanged (no silent re-ship). 0039/0042 files are not edited (I2).

---

## 6. Risks

- **Misclassification of a transient error as permanent** — a receiver-side *temporary* 409/422 would dead-letter a recoverable fact. Mitigated: permanent list is exactly the four direction classes (D2); REQ-5.1a pins the transient boundary; `last_error` retains the full cause until the retention prune (7 d default, `config_audit_governance.go:68`); the existing prune+gap-reconcile horizon re-attempt (documented in `cmd-server-audit-governance-permanent-error-classification-v1.design.md` R1) still applies.
- **Timing flake on loaded CI** — mitigated by the proven harness pattern (`runtime_test.go:117-186`): atomic counters, poll-until-first-POST with 3 s deadline, observe window ≥ 2.5 s = harness max backoff (2 s) + poll slack, no wall-clock equality assertions.
- **Index plan regression on one dialect** — SQLite and Postgres planners differ; the column list is adjustible within 0043 (D3) as long as the EXPLAIN evidence holds; both probes are committed tests (REQ-5.3/5.3b) so drift fails CI/integration.
- **Classifier/sentinel-list drift** — `isPermanentDeliveryError` and `classifyRelayError` both enumerate the sentinels; REQ-2 mandates the cross-reference comment and REQ-5.1a exercises every `ErrInvalidReceipt` producer (`http.go:186/190/194/204`) plus both statuses.
- **Line-count gate (≤500 lines/file, hard gate)** — `runtime_test.go` at 400 lines; new runtime tests go to `relay_terminal_test.go` (D5); `audit_governance_pending_idx_test.go` is a new internal-package file. No existing file crosses 500.
- **I2 violation risk** — any temptation to edit 0039/0042 to "align" naming is blocked by D4 + REQ-5.4 (0042-files-unchanged assertion).

*Verification basis: all line numbers re-confirmed on this checkout (`acfaaf4`); `make check` gate applies to the eventual implementation (gofmt/build/vet/test — SQLite + local FS, zero network beyond `httptest`); Postgres probes run only under `go test -tags=integration` (CI gate-external).*
