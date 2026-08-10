# Design — internal/access label → `internal/auditgovernance` + `internal/repository`: permanent-error classification → terminal ≤1 attempt + pending-row partial index (contract item 1, T-3)

**Module:** `internal/access` (analysis label only — no change in that package; implementation surface is `internal/auditgovernance` + `internal/repository`, per spec §1)
**Spec:** `docs/requirements/internal-access-audit-governance-permanent-error-classification-v1.spec.md` (REQ-1..6, D1..D5)
**Contract:** `docs/campaigns/implementation-gate.md:21` (gate item 1 / T-3; committed at HEAD)
**Sibling:** `docs/requirements/cmd-server-audit-governance-permanent-error-classification-v1.design.md` (same direction, earlier design; superseded on classifier/tests by this doc — D1/D5 fold rules)
**Date:** 2026-08-07 · **HEAD:** `acfaaf4` · **Worktree:** dirty — this design builds on the *staged* state, see §0.

---

## 0. Baseline caveat (verified, not trusted) — the "HEAD acfaaf4" claim is false; citations hold for the staged worktree

Every functional citation in the evidence/spec (relay terminal branch, `failFact`, `ErrReceiptConflict`, 0042 `failed_at_ns`, claim/lag `failed_at_ns=0` predicates, `CleanupFailedAuditGovernance`, both terminal tests) was re-checked in **both** states:

| State | What exists |
|---|---|
| HEAD `acfaaf4` | **No terminal classification at all.** `deliverFact` (`relay.go:80-96`, 158-line file) sends *every* publish error — including receipt conflict — to `retryFact` → bounded-backoff forever. No `failFact`, no `ErrReceiptConflict` sentinel (4 sentinels only), no 0042 migration, no `failed_at_ns` column/predicates, no `CleanupFailedAuditGovernance`, no terminal tests (`audit_governance_test.go` ends at `:300`; `runtime_test.go:117` is the backoff test). |
| Worktree (staged, uncommitted) | The "contract A" baseline: conflict-only terminal (`relay.go:84`), `failFact` `:111-122`, `ErrReceiptConflict` (`model.go:27`), 0042 (staged `A`, **never committed** — `git log --all` empty), `failed_at_ns=0` predicates (`claim.go:38/62/88/146/168/195`), `FailAuditGovernance` `:159-172`, `CleanupFailedAuditGovernance` (`cleanup.go:113`), `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` `:334`, `TestRuntimeConflictingReceiptIsTerminalWithRetention` `:117`, `TestBoundedBackoffIsDeterministicAndCapped` `:189`. |

**Consequences for this design:**
1. All line numbers in the spec/evidence are correct **for the staged worktree**, which is the state this design extends. The eventual merge sequence must land the staged baseline first (§5.1).
2. **E12 ("0042 tracked/committed at HEAD") is false.** 0042 files are staged-but-uncommitted. The I2 argument "0042 is committed, don't edit" therefore does **not** strictly bind. We still ship the partial index as **new migration 0043** (see D3): the cmd-server campaign's spec/design/runbook already treat 0042 as shipped and reference its file identity, and migration numbers once assigned must not churn across in-flight campaigns. Folding the index into 0042 is rejected (cross-campaign coupling; would invalidate sibling docs).
3. If the staged baseline is ever reverted/reset, every premise of this design reverts with it (at HEAD the problem is retry-forever for *everything* including conflict). Implementation must not start until the staged baseline is committed.

---

## 1. Verification register (spec evidence re-checked, not trusted)

| # | Claim | Verdict |
|---|---|---|
| E1 | `relay.go` `deliverFact`/`failFact`/`retryFact`/`classifyRelayError`/`boundedBackoff` at `:80/:84/:111/:124/:163/:181`; `:84` is the only terminal path | ✅ **worktree-exact** (spec's "HEAD" framing false — §0). `:84` `if errors.Is(err, ErrReceiptConflict)` → `failFact` `:111-122`; all other errors → `retryFact` `:124-137`; `boundedBackoff` `:163-179` (per-delay cap `min(max(jittered, initial/2), maximum)`, ±25% jitter); `classifyRelayError` `:181-190` (log label only). At HEAD the file is 158 lines and has no terminal path. |
| E2 | `http.go` `validateReceipt` `:178` / `receiptMatches` `:214`; `model.go` sentinels `:26-27,:31-37` | ✅ **worktree-exact.** non-202 → `&httpStatusError{Status}` `:182`; `ErrInvalidReceipt` producers `:186/:190/:194/:204`; `ErrReceiptConflict` `:201`; `receiptMatches` `:214-225` (EventID/TenantID/`AcceptedAt` nonzero, status ∈ {ledgered, indexed, archived}). `model.go`: `ErrInvalidReceipt` `:26`, `ErrReceiptConflict` `:27`, `httpStatusError` `:31-37`. |
| E3 | `boundedBackoff` caps per-delay, not horizon; no attempt cap anywhere | ✅ **holds both states.** `grep MaxAttempts internal/auditgovernance/` → empty; the only `MaxAttempts` in `internal/config/` is `EVENT_OUTBOX_MAX_ATTEMPTS` (`config_event_outbox.go:20/:32`) — the evidence's correction is itself correct. `RetryAuditGovernance` (`claim.go:137-152`) has no attempt predicate. |
| E4 | 0042 adds `failed_at_ns` instead of contract's `status`/`dead_at`; 0041 is status-led | ✅ content-exact (sqlite `INTEGER`, postgres `BIGINT`, `NOT NULL DEFAULT 0`; down = `DROP COLUMN`); ⚠️ **0042 is staged, not committed** (E12 false). 0041 `event_outbox` is status-led (`status TEXT CHECK ... 'pending','inflight','delivered','failed'` + `event_outbox_due_idx (status, available_at_ns, ...)`). |
| E5 | no partial index over pending rows; 0039 `audit_governance_due_idx` lacks `failed_at_ns`; claim/lag filter `delivered_at_ns=0 AND failed_at_ns=0` at `:38/:62/:88/:195` | ✅ **worktree-exact.** 0039 (both dialects): `due_idx (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns)` + `tenant_idx` — no partial predicate. Claim (pg `:37-39`, sqlite `:61-64`), `claimAuditGovernanceIDs` `:88`, `OldestPendingAuditGovernance` `:188-201` (`MIN(o.created_at_ns)` + `failed_at_ns=0`), `HasPendingDrainingAuditGovernance` `:202-210` (EXISTS + `b.state='draining'`). |
| E6 | `config_audit_governance.go:65` `MaxBackoffSeconds` default 300 | ✅ **exact, both states.** `getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` at `:65`. |
| E7 | `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` `:334` | ✅ **worktree-exact** (`:334-371`): fencing (stale owner/token cannot fail `:344-347`), terminal (never re-claimed `:350-352`, never pending `:353-355`), retention before/after `:358-365`, re-enqueueable after prune `:368-371`. **Absent at HEAD.** |
| E8 | `TestRuntimeConflictingReceiptIsTerminalWithRetention` `:117`; harness params (poll 10 ms, backoff 1→2 s, retention 3600 s) | ✅ **worktree-exact.** `:117-186`; harness `runtimeConfig` `:40-46`: `PollMilliseconds=10`, `InitialBackoffSeconds=1`, `MaxBackoffSeconds=2`, `DeliveredRetentionSeconds=3600`. Assertions `:166-186` (posts==1 after 500 ms observe, claim→0, OldestPending→false, prune 0→1). At HEAD `:117` is the backoff test. ⚠️ The existing observe window (500 ms) is **shorter than the 1 s initial backoff** — fine for the conflict case (never re-POSTs), but the new table (REQ-5.1) requires the ≥ 2.5 s window (D5.1). |
| E9 | `TestBoundedBackoffIsDeterministicAndCapped` `:189` | ✅ **worktree-exact** (`:189-196`); determinism + cap ∈ [initial/2, maximum] for max 5 s. Absent at HEAD (there it is `:117`). |
| E10 | EXPLAIN precedent + both-dialect hooks | ✅ `perf_probe_test.go:170` `TestProbeQueryPlans` (`EXPLAIN QUERY PLAN`, `t.Log` only, no assertion — pattern only); `migrationsFS` `//go:embed` at `sql.go:21` (unexported ⇒ REQ-5.4 needs `package repository` internal test, D5.3); `internal/integration/audit_governance_postgres_test.go` exists (`//go:build integration`), skip-if-unreachable pattern at `postgres_integration_test.go:46`. |
| E11 | Contract item 1 / T-3 at `implementation-gate.md:21` | ✅ **exact + committed.** Row 1: "死信终态（F3）：status/dead_at 列 + 部分索引；移植 sink `DeliveryError.Permanent` 分类（422/409/tenant mismatch/无效回执 → 终态 ≤1 次尝试）；瞬态有界重试 cap 300s；dead 行排除出 claim/lag · T-3：422 → 一个周期内终态；`Ready()` 含 dead 行 = true；批次继续". |
| E12 | "0042 files are tracked/committed at HEAD" | ❌ **false.** `git ls-files --stage` shows `A` (staged new); `git log --all -- <path>` empty — **never committed**. Correction folded into §0/D3/D4 (REQ-5.4's "0042 unchanged" assertion must pin the *staged* content, not HEAD). |
| — | Spec header "verification basis = this checkout" | ✅ consistent with the above; the evidence table's "checked against HEAD acfaaf4" phrasing is the misleading part, now corrected. |

Net: the functional claims are sound against the actual implementation basis (staged worktree); the baseline framing and E12 are wrong and are corrected above. The design below is otherwise unchanged in substance from the spec's REQ-1..6.

---

## 2. Design

### D1 — `isPermanentDeliveryError` classifier (`internal/auditgovernance/relay.go`, ~14 lines + 1 import)

Placed adjacent to `classifyRelayError` (`:181-190`); unexported; relay *policy*, not error model (spec D1). Adopted verbatim from the sibling cmd-server design D1 (same direction; this document supersedes it):

```go
// isPermanentDeliveryError reports whether a publish error is terminal: the
// receiver will never ledger this fact, so bounded-backoff retry can only
// churn forever. Closed list by construction — conflict receipt, invalid
// receipt (tenant-mismatch / malformed / non-ledgered), and HTTP 409/422.
// Everything else (400/401/403/404/410/429/501/other 4xx-5xx,
// ErrInvalidEvent, ErrTokenUnavailable, token-source, transport, context)
// is transient.
// Keep in sync with classifyRelayError's sentinel enumeration.
func isPermanentDeliveryError(err error) bool {
	if errors.Is(err, ErrReceiptConflict) || errors.Is(err, ErrInvalidReceipt) {
		return true
	}
	var status *httpStatusError
	return errors.As(err, &status) &&
		(status.Status == http.StatusConflict || status.Status == http.StatusUnprocessableEntity)
}
```

- `relay.go` gains `"net/http"`; no other import churn. `http.StatusConflict`/`http.StatusUnprocessableEntity` confirmed in stdlib.
- The `errors.Is` branch runs first; on today's paths an `*httpStatusError` never wraps a sentinel, so branch order is classification-equivalent. The comment states the closed list so future wrapping stays safe.
- `classifyRelayError` **unchanged** (log label only). The cross-reference comment is the drift guard; REQ-5.1a's closed-list table is the mechanical guard (every permanent/transient member pinned in both directions).
- **Boundary is deliberate:** 400/404/410/429/501, token-path errors (`ErrTokenUnavailable`, opaque `*ssoclient.TokenError` on token-endpoint 401/403), `ErrInvalidEvent`, transport/context errors stay transient. Rationale (sibling design D2, retained): with no requeue surface, retry-forever + the `/readyz` 503 bridge keeps the operator signal alive instead of silently dropping governance data; 401 self-heals (token invalidated at `http.go:126-127`).

### D2 — `deliverFact` branch swap (`relay.go:84`, ~8 changed lines)

Replace the single-sentinel branch with the classifier; `failFact`/`retryFact` bodies untouched:

```go
	if isPermanentDeliveryError(err) {
		// Terminal-with-retention: conflict receipt, invalid receipt
		// (tenant-mismatch / malformed / non-ledgered), or HTTP 409/422 —
		// the receiver will never ledger this event, so retrying is
		// bounded-backoff-forever. Fail the fact (never re-claimed) and keep
		// the row + last_error until the retention prune (7d default),
		// mirroring the events outbox 'failed' state. All other errors
		// (400/401/403/404/410/429/501/other 4xx-5xx, token-path, transport)
		// stay transient with bounded-backoff retry.
		r.failFact(fact, err)
		return
	}
	if err != nil {
		r.retryFact(fact, err)
		return
	}
```

`ErrReceiptConflict` flows through the classifier's first branch onto the exact same `failFact` path as today ⇒ `TestRuntimeConflictingReceiptIsTerminalWithRetention` must pass **unmodified** (byte-identical pin).

### D3 — Migration `0043`: pending-row partial indexes (both dialects; new files, I2-clean)

New pair `internal/repository/migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` (0043 confirmed as the next free number; 0042 is the current max).

**up** (both dialects, identical DDL):

```sql
-- Deviation note (contract implementation-gate.md:21 item 1): the contract
-- prescribes status/dead_at columns + partial index; 0042 shipped
-- failed_at_ns (timestamp-led 0039 schema, claim/lag already exclude
-- failed_at_ns!=0, CleanupFailedAuditGovernance prunes by failed_at_ns).
-- Deviation documented, not renamed (zero-behavior rename; I2). Partial
-- index serves the two pending access paths:
--   claim: WHERE delivered_at_ns=0 AND failed_at_ns=0 AND available_at_ns<=?
--          AND lease_expires_at_ns<=? ... ORDER BY available_at_ns,created_at_ns,id
--   lag:   MIN(created_at_ns) WHERE delivered_at_ns=0 AND failed_at_ns=0
--          (joined form is served by due_idx on SQLite, by this index on PG)
CREATE INDEX audit_governance_pending_claim_idx ON audit_governance_outbox
  (available_at_ns, created_at_ns, id)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;
CREATE INDEX audit_governance_pending_lag_idx ON audit_governance_outbox
  (created_at_ns)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;
```

**down:** `DROP INDEX IF EXISTS audit_governance_pending_lag_idx; DROP INDEX IF EXISTS audit_governance_pending_claim_idx;` — `IF EXISTS` matches the repo convention (0036 `DROP INDEX IF EXISTS webhook_failures_retryable_idx`) and keeps a manual partial re-run replay-safe (first DROP ok, second errors on re-run); down files are never auto-run (I2), but a re-run must not fail.

Column-shape rationale (evidence-driven per spec D3 — the predicate + EXPLAIN evidence is the contract, not the column list):
- **Claim index:** `(available_at_ns, created_at_ns, id)` matches both the range predicate (`available_at_ns <= $N`) **and** the `ORDER BY o.available_at_ns, o.created_at_ns, o.id` in index order (no temp sort) on both dialects. `lease_expires_at_ns` is deliberately **not** interleaved (it is a residual filter: pending rows are lease=0 or expired-lease<now; including it between the ORDER BY columns would force a sort and risks a full-scan plan choice). This deviates from the spec REQ-3's suggested `(available_at_ns, lease_expires_at_ns, created_at_ns, id)` — permitted by spec D3, and gated by the EXPLAIN assertions (D5.3/5.3b). If the plan probe on either dialect prefers the spec's shape (or a different one), adjust columns **within 0043** until both probes pass — the committed probes are the gate.
- **Lag index:** `(created_at_ns)` serves `MIN(o.created_at_ns)` with dialect-split coverage (EXPLAIN-verified on both drivers): the **joined** `OldestPendingAuditGovernance` query resolves via the pre-existing `due_idx` on SQLite (a `delivered_at_ns=0` scan — unchanged from baseline; the D3 validation harness's `pending_lag_idx` plan for this query was measured after it dropped `due_idx` mid-section, a schema state the real 0039 never has) and via this index on Postgres; the **isolated/covering** MIN form uses this index on SQLite. The `EXISTS` probe resolves via `tenant_idx` on SQLite and this index on PG. D5.3 pins: no full-table scan on the joined form + this index on the isolated form (SQLite); this index on both real queries (PG).
- Existing `audit_governance_due_idx` / `audit_governance_tenant_idx` (0039) untouched. SQLite ≥ 3.8.0 partial indexes (modernc.org/sqlite v1.50.1 — fine); Postgres partial indexes since 9.2 — fine.
- **Why not fold into 0042 (corrected rationale vs spec E12):** 0042 is staged, not committed — I2 does not strictly forbid editing it. But the sibling campaign's docs/runbook already reference 0042 as shipped, and migration-number churn across in-flight campaigns is the higher-risk path. 0043 works regardless of commit order. Rejected alternative.

### D4 — Deviation documentation (REQ-4): 0043 header + `failFact` comment

- The 0043 up-file header (both dialects) carries the tokens `failed_at_ns`, `status`, `dead_at`, `implementation-gate` (the doc-pin test D5.4 asserts these).
- `failFact` doc comment (`relay.go:106-110`) gains one line: "naming deviation from contract (status/dead_at) documented in 0043 — see its header." No edits to 0039/0041/0042 files.

### D5 — Tests (REQ-5; new files respect the ≤500-line hard gate)

**D5.1 — `internal/auditgovernance/relay_terminal_test.go`** (`package auditgovernance`, new file, ~250 lines; reuses `runtimeConfig`/httptest-sink harness from `runtime_test.go:40-186`). Supersedes the sibling design's planned `runtime_classify_test.go` — see D7.

- **Structure is table-driven with a shared case-setup helper** so every function in the file stays < 50 lines (soft gate): `TestRuntimePermanentDeliveryErrorsAreTerminal` holds only the table + `t.Run`/`t.Parallel()`; `runTerminalCase(t, tc)` owns the end-to-end flow (sink → repo/Migrate → `New(runtimeConfig)` → `WrapRepository.RecordAudit` → `Start` → poll-first-POST → observe → `Close` → asserts); `terminalSink` owns the per-row sink (token endpoint + status/body); `assertTerminalState` and `assertTerminalRetention` own the anchors; `pollUntil`/`observeWindow` own the timing loops. The 409 case sets `retention: true` to additionally run the prune block.
- Table over the five terminal cases (REQ-5.1): (a) **409** — sink answers status only, no body; (b) **422** — same; (c) **tenant-mismatch** — 202 + receipt with `tenant_id != fact.TenantID` (the sink captures the posted `event_id` and echoes it so the mismatch fails on TenantID, not EventID); (d) **non-ledgered status** — 202 + `status:"rejected"`; (e) **unparseable body** — 202 + `not-json`. Each case: its own httptest server, tempdir sqlite DB, `posts atomic.Int32`, `t.Parallel()` (independent DBs/servers ⇒ safe). Timing: the current package suite is ~3.5 s; serial 5-case would add ~13.5 s (~17 s); parallel keeps the wall-clock cost at one observe window (~6–7 s total).
- Per case, **precise window placement** (fixes the E8 gap in the existing 500 ms pattern): poll-until-first-POST (3 s deadline) → **observe 2.6 s (≥ the 2.5 s floor) with the runtime still running** (a misclassified-transient row would re-POST at ~1 s = initial backoff 1 s ± 25 % jitter, worst case ~1.3 s; the floor 2.5 s > harness max backoff 2 s + poll slack, leaving ~1.2 s margin) → `runtime.Close()` → assert `posts.Load() == 1`; `store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute)` → `len == 0`; `store.OldestPendingAuditGovernance(ctx)` → `ok == false`.
- Retention block (REQ-5.1, 409 case only, verbatim pattern of `runtime_test.go:180-186`): `CleanupFailedAuditGovernance(ctx, now.Add(-time.Hour), 10)` → 0; `(now.Add(time.Hour), 10)` → 1.
- **D5.1a — classifier boundary pin** (REQ-5.1a, same file, ~40 lines): closed-list table — permanent: `ErrReceiptConflict`, `ErrInvalidReceipt`, `&httpStatusError{409}`, `&httpStatusError{422}`, each wrapped via `fmt.Errorf("%w: ...")`; transient: `&httpStatusError{400,401,403,404,410,429,500,501,503}` (501 matches the D1 enumeration and the sibling pin), `ErrInvalidEvent`, `ErrTokenUnavailable`, `errors.New("transport")`, `context.DeadlineExceeded`.

**D5.2 — 300 s backoff pin** (REQ-5.2): extend `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:189-196`, +10 lines): `boundedBackoff("fact-1", 20, time.Second, 300*time.Second)` deterministic (two calls equal) and **`> 200 s` (≤ 300 s)** — the actual range is [225 s, 300 s] (20 attempts: 8 doublings snap at 256 s > max/2 → 300 s, then ±25 % jitter clamped at 300 s), so `> 200 s` fails a broken doubling chain, not just a missing cap; pins the contract's 300 s cap (`config_audit_governance.go:65`). Pure function, no wall-clock. `runtime_test.go`: 400 → 410 lines (< 500).

**D5.3 — `internal/repository/audit_governance_pending_idx_test.go`** (`package repository` — **internal** package required for `migrationsFS`; raw DB via `repo.(*sqlStore).db`, no second connection needed; ~230 lines — the growth over the original ~140 budget is the probe seed, data not logic):

- **Seed is the D3 validation-harness mix** (raw SQL, replicated from `docs/auto/runs/…/design-validate-d3/seed.sql`): 20 bindings (t19 draining), 50,000 delivered + 5,000 failed history rows, 300 pending rows with **heavy `available_at_ns` ties** (200 claimable in 20 batches × 10 sharing one `available_at_ns`, 50 future, 50 leased-live). Seeding guidance (empirically verified): pending-row ties are the **decisive** planner lever (they mirror a batch-flush pattern); "extra delivered rows" alone is a weak lever (the strict CLI only flips at ~1M delivered). **`ANALYZE` after seeding is REQUIRED** — without `sqlite_stat1` the planner picks `due_idx` + a temp b-tree and the `pending_claim_idx` assertion fails outright (verified). Plan *choice* is SQLite-version-sensitive (3.53.1 vs 3.53.4 differ at this seed); the pinned modernc v1.50.1 makes the committed test deterministic — noted in the test comment.
  - **Claim-plan probe** (REQ-5.3): `EXPLAIN QUERY PLAN` on the sqlite claim inner SELECT shape (`claim.go:61-64`, bound-parameter form, args = seed clock/revision/limit); **assert** the detail names `audit_governance_pending_claim_idx` and contains no `SCAN` of the outbox (pattern of `perf_probe_test.go:170` but with `t.Errorf`, not `t.Log`; the alias-qualified scan reads `SCAN o` — both spellings forbidden).
  - **Lag-plan probes** (REQ-5.3, corrected vs the validation report): the **joined** `MIN(o.created_at_ns)` shape (`claim.go:189-196`) asserts only the no-full-table-scan property — on SQLite it resolves via the pre-existing `due_idx` (unchanged from baseline; the report's `pending_lag_idx` plan for this query was measured after its harness dropped `due_idx` mid-section, a state the real schema never has); the **isolated** MIN form asserts it names `audit_governance_pending_lag_idx` (the covering MIN path on this index).
  - **D5.3a — direct terminal read** (REQ-5.3a): after `FailAuditGovernance`, raw `SELECT failed_at_ns, attempts FROM audit_governance_outbox` → `failed_at_ns > 0` and `attempts == 1` (the "exactly 1 attempt" anchor; `attempts` increments once on claim — `claim.go:35` pg, `:87` sqlite via `claimAuditGovernanceIDs`).
- **D5.4 — doc-pin** (REQ-5.4, same file): `migrationsFS.ReadFile("migrations/sqlite/0043_audit_governance_pending_partial_index.up.sql")` + postgres counterpart; assert header contains `failed_at_ns`, `status`, `dead_at`, `implementation-gate`. Assert both 0042 up files still exist with `failed_at_ns` and **no** `CREATE INDEX` (pins the documented-deviation path against silent re-ship; per §0 this asserts the *staged* content — if 0042's content legitimately changes before the final merge, this test forces a conscious revisit of the 0043 header).

**D5.3b — Postgres dialect** (REQ-5.3b, `//go:build integration`): extend `internal/integration/audit_governance_postgres_test.go` (+~85 lines): seed the same harness mix on the fresh PG schema (raw SQL, `gen_pg.py` replication — `::bigint` casts where integer arithmetic would overflow), `ANALYZE`, then `EXPLAIN (FORMAT TEXT)` on the claim SELECT and the lag SELECT; assert plans name `audit_governance_pending_claim_idx`/`audit_governance_pending_lag_idx` and contain no `Seq Scan on audit_governance_outbox`; skip-if-unreachable pattern (`postgres_integration_test.go:46`). CI gate-external (`make check` is SQLite-only).

### D6 — No other changes (REQ-6)

No attempt cap, no new config/env (`MaxAttempts` stays events-outbox-only), no exported-API change, no `cmd/server` change (readyz semantics already correct once the relay stops re-claiming terminal rows; the maxLag flip is B3-2, out of scope), no events-outbox change, no `internal/access/` change. `classifyRelayError`, `failFact`, `retryFact`, `boundedBackoff`, all claim/lag/fail/cleanup SQL: untouched.

### D7 — Test-file collision with the sibling campaign

The cmd-server design (D3) planned `internal/auditgovernance/runtime_classify_test.go` for the same table. Fold rule: **this campaign owns `relay_terminal_test.go`** (spec REQ-5.1); the classifier + terminal cases live there once. If `runtime_classify_test.go` exists at implementation time (it does not today), delete it and fold its cases in — never two files defining the same table. The sibling design's classifier code (D1 here) and branch swap (D2 here) are adopted as-is, so the two campaigns merge into one implementation train.

---

## 3. API changes & compatibility constraints

| Surface | Change | Compatibility |
|---|---|---|
| Public API (SDK/CLI/REST/MCP/WebDAV/S3) | **None** | No versioning concern |
| `internal/auditgovernance` | + `isPermanentDeliveryError(err) bool` (unexported); `deliverFact` condition swap; +1 import (`net/http`); `failFact` comment +1 line | `ErrReceiptConflict` path byte-identical; `TestRuntimeConflictingReceiptIsTerminalWithRetention` passes unmodified |
| `internal/repository` | + 0043 migration pair (2 indexes/dialect, up/down) | Applies on top of any 0039..0042 schema; no column/query changes; existing tests unaffected |
| Config/env | **None** | No new knobs; 300 s cap is the existing default (`config_audit_governance.go:65`) |
| Dependencies | **None** (stdlib `net/http` constants) | I6 satisfied; no `go mod` changes |

Constraints carried from the spec: I1 (new 0043 SQL has no placeholders — n/a), I2 (0043 only; 0039/0041/0042 untouched), I5 (all opt-in defaults unchanged; nil dependencies unaffected), hard gates (`gofmt`, `go build`, `go vet`, `go test`, ≤500 lines/file — D5 file budgets above).

Behavioral contract: permanent ⇒ `failed_at_ns` set on the **first** attempt (`attempts == 1`), row excluded from claim/lag (`failed_at_ns=0` predicates already in place), retained with `last_error` until `CleanupFailedAuditGovernance` after the retention window (7 d default), then pruned **without** an origin tombstone ⇒ the origin becomes a gap again and `reconcile()` re-enqueues a fresh fact (`uuid.NewString()`, `attempts=0`, `available_at_ns=now`) — one automatic horizon re-attempt per prune cycle **while the binding is active** (sibling design R1, retained: dead-letter is final only *within* the retention window; the events outbox has no such re-enqueue path).

---

## 4. Failure modes

| # | Failure | Mitigation |
|---|---|---|
| F1 | Transient error misclassified permanent (a sink that *temporarily* 409s/422s, e.g. eventual-consistency hiccup, dead-letters a recoverable fact) | Permanent list is exactly the four direction classes (D1); D5.1a pins the boundary both directions; `last_error` retains the full cause until the 7 d prune; recovery paths: operator runbook UPDATE (preserves row identity), the ~7 d prune+gap-reconcile horizon re-attempt, or re-running the source operation. No admin/CLI requeue surface exists (`grep failed_at_ns internal/api/ cmd/server/ internal/cli/` → empty). |
| F2 | Index-plan regression on one dialect (SQLite/PG planners differ; seed-size sensitivity) | Column list adjustable within 0043 (D3) as long as EXPLAIN evidence holds; both probes are committed tests (D5.3 SQLite in `make check`; D5.3b PG under `-tags=integration`); seeding guidance documented in the test. |
| F3 | Timing flake on loaded CI | Proven harness pattern (atomic counters, poll-until-first-POST 3 s deadline, observe window 2.6 s ≥ 2.5 s floor > max backoff 2 s, no wall-clock equality); `t.Parallel()` per case (independent DBs/servers) — serial would add ~13.5 s to the ~3.5 s suite (~17 s); parallel keeps the wall-clock cost at one observe window (~6–7 s total). |
| F4 | I2 violation / 0042 drift (someone edits 0042 or re-ships the index inside it) | D3 (new 0043 only) + D5.4 doc-pin asserts 0042 files contain no `CREATE INDEX`. Note §0: 0042 is staged — if its content legitimately changes pre-merge, the 0043 header must be revisited (the test forces the conversation). |
| F5 | Line-count gate (≤500/file) | `runtime_test.go` 400→410; new files budgeted (D5); no existing file crosses 500. |
| F6 | Baseline commit-order risk (baseline worktree not committed before this lands) | §0.1 + §5.1 precondition; implementation must not start on a clean HEAD. |
| F7 | Dual-campaign test duplication | D7 fold rule; single table, single file. |
| F8 | Classifier/sentinel-list drift (`isPermanentDeliveryError` vs `classifyRelayError`) | Cross-reference comment (D1) + D5.1a exercises every `ErrInvalidReceipt` producer (`http.go:186/190/194/204`) and both statuses. |

---

## 5. Migration steps & rollback

**5.1 Preconditions (commit order):** land the **entire** contract-A baseline *worktree* first — the 0042 migration files (index-staged) **and** the unstaged code half (`relay.go` terminal branch, `ErrReceiptConflict`, `failFact`, claim/lag/fail/cleanup SQL, `runtime_test.go`/`audit_governance_test.go` terminal tests). Committing only the staged index would ship 0042 without its code; the precondition covers the full uncommitted worktree, not just the staged files. This design's 0043 and classifier are layered on top of it. Do not start on a clean HEAD (§0).

**5.2 Implementation order:** ① D1 classifier + D2 swap + D5.1a pin (pure unit, no schema) → ② D5.1 runtime table (T-3.1 on the conflict-only baseline) → ③ 0043 up/down + D5.3/D5.3a/D5.4 → ④ D5.2 → ⑤ D5.3b (integration, gate-external). The generated D5.1 table and D5.1a pin reference the classifier, so `make check` goes green only once ① and ② land together — the ordering above, not the file generation, is what keeps each step green (gofmt/build/vet/test — SQLite + local FS, zero network beyond `httptest`).

**5.3 Deployment:** one release commit. `repo.Migrate` applies 0043 at startup on both dialects (serial, version-skipping). No data migration — the index is over existing columns; existing pending rows (including any already-terminal `failed_at_ns>0` rows from the baseline) are covered by the predicate. Postgres `CREATE INDEX` takes a brief `SHARE` lock on `audit_governance_outbox` — acceptable at outbox scale (migration is one-shot; no `CONCURRENTLY` in the migration runner).

**5.4 Rollback:** revert the commit. Old binary (post-baseline) reverts to conflict-only terminal classification: 409/422/`ErrInvalidReceipt` become retry-forever again (pre-change behavior); rows already marked `failed_at_ns` by the new binary remain terminal until the retention prune — they are **not** resurrected by the older binary (claim predicates exclude `failed_at_ns != 0`). 0043 down is optional (index is inert if unused) but run it in the rollback migration if keeping the schema byte-identical to the released baseline. To resurrect wrongly-dead-lettered rows immediately, the guarded UPDATE below (sibling runbook form, folded in) — row identity/history preserved; otherwise the ~7 d prune+gap-scan horizon re-attempts automatically (R1).

```sql
-- Resurrect terminal-failed rows. Guarded by the terminal predicate: only
-- failed-and-not-delivered rows are touched; a non-terminal id no-ops. Failed
-- rows are single-writer (nothing writes failed_at_ns>0 rows except the
-- prune's DELETE), so this is race-free against the runtime.
-- Rowcount expectation: 1 per target row; 0 = already pruned or already
-- resurrected by the R1 horizon; >1 = unintended batch — abort. Verify with
-- SQLite changes() / PG rowcount (GET DIAGNOSTICS rowcount, or the driver's
-- RowsAffected) — changes() is SQLite-only.
-- Binding-presence check first: SELECT 1 FROM audit_governance_bindings
-- WHERE tenant_id='<tenant>' — claim/lag join bindings (claim.go:61,
-- :188-201), so a resurrected row of a dropped binding is unclaimable,
-- invisible to the lag scan, and unpruneable (orphan leak).
-- available_at_ns is already in the past on failed rows (set at enqueue,
-- never advanced between claim and fail) — do NOT set it; a future-dated
-- literal would delay claimability (must be UnixNano if ever set).
-- Re-failure restarts the 7 d clock (fresh failed_at_ns).
UPDATE audit_governance_outbox
SET failed_at_ns = 0, last_error = ''
WHERE failed_at_ns > 0
  AND delivered_at_ns = 0
  AND id = '<fact_id>';
```

---

## 6. Testable acceptance mapping (AC → test → assertion anchors → gate)

| AC (contract T-3) | Test | Assertion anchors | Gate |
|---|---|---|---|
| **T-3.1** 409/422/tenant-mismatch/invalid-receipt → terminal within exactly 1 attempt | `relay_terminal_test.go` table (5 cases) + `TestRuntimeConflictingReceiptIsTerminalWithRetention` unmodified | `posts == 1` after first-POST + ≥ 2.5 s observe + `Close()`; `ClaimAuditGovernance` → 0 rows; `OldestPendingAuditGovernance` → `ok == false`; retention: `CleanupFailedAuditGovernance(now-1h)` → 0, `(now+1h)` → 1 (409 case); raw `failed_at_ns > 0` ∧ `attempts == 1` (D5.3a) | `make check` (SQLite) |
| **T-3.2** transient retries pinned to 300 s cap | D5.1a closed-list table + `TestBoundedBackoffIsDeterministicAndCapped` extended | `isPermanentDeliveryError` false for {400,401,403,404,410,429,500,501,503, `ErrInvalidEvent`, `ErrTokenUnavailable`, transport, DeadlineExceeded}; `boundedBackoff("fact-1", 20, 1s, 300s)` deterministic ∧ `> 200 s` ∧ `≤ 300 s` (actual range [225 s, 300 s]) | `make check` |
| **T-3.3** pending-row partial index, EXPLAIN-verified both dialects | D5.3 (SQLite unit), D5.3b (PG integration) | 0043 up/down present both dialects, predicate `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; SQLite claim plan names `audit_governance_pending_claim_idx`, no `SCAN` of the outbox; SQLite lag: joined form no full-table scan, isolated form names `audit_governance_pending_lag_idx` (joined MIN is served by the pre-existing `due_idx` on SQLite — baseline-unchanged, see D3); PG plans name both indexes, no `Seq Scan on audit_governance_outbox` | `make check` / `make test-integration` (skip-if-unreachable) |
| **T-3.4** naming deviation resolved (documented, not renamed) | D5.4 doc-pin | 0043 headers (both dialects) contain `failed_at_ns`, `status`, `dead_at`, `implementation-gate`; 0042 up files contain `failed_at_ns` and no `CREATE INDEX`; `failFact` comment cross-ref | `make check` |

**REQ trace:** REQ-1 → D1/D5.1a · REQ-2 → D2/D5.1 · REQ-3 → D3/D5.3/D5.3b · REQ-4 → D4/D5.4 · REQ-5 → D5.1-D5.4 · REQ-6 → D6.

---

## 7. Files changed (complete list)

| File | Change | Size budget |
|---|---|---|
| `internal/auditgovernance/relay.go` | +`isPermanentDeliveryError` (~14 lines), branch swap (~8), `failFact` comment (+1), +`net/http` import | 191 → ~215 |
| `internal/repository/migrations/sqlite/0043_audit_governance_pending_partial_index.{up,down}.sql` | **new** — 2 partial indexes + deviation header; down = `DROP INDEX IF EXISTS` (0036 convention) | ~16 + 3 |
| `internal/repository/migrations/postgres/0043_audit_governance_pending_partial_index.{up,down}.sql` | **new** — same DDL | ~16 + 3 |
| `internal/auditgovernance/relay_terminal_test.go` | **new** — T-3.1 table (5 cases, parallel, shared helpers, every fn < 50 lines) + D5.1a pin | ~250 |
| `internal/repository/audit_governance_pending_idx_test.go` | **new** (`package repository`) — ANALYZE + tie-seeded EXPLAIN probes + raw terminal read + doc-pin | ~230 |
| `internal/auditgovernance/runtime_test.go` | backoff 300 s case, `> 200 s` assertion (+10) | 400 → 410 |
| `internal/integration/audit_governance_postgres_test.go` | +PG EXPLAIN probes (`//go:build integration`) | +~85 |
| `docs/requirements/internal-access-audit-governance-permanent-error-classification-v1.spec.md` | §0/E12 baseline correction note (0042 staged-not-committed; HEAD framing) | — |

Not touched: 0039/0040/0041/0042 migrations, `model.go`, `http.go`, `token.go`, `facts.go`, `runtime.go`, `audit_governance_claim.go`/`_cleanup.go`/`_write.go`, `config_*`, `cmd/server/*`, `internal/access/*`, events outbox.
