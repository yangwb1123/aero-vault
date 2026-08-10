# Design — internal/ai label → `internal/auditgovernance` + `internal/repository`: permanent-error terminal classification incl. `ErrInvalidEvent` (B3.1 / G4 / T-3, delta)

**Module:** `internal/ai` (analysis label only — zero changes in that package; implementation surface is `internal/auditgovernance` + `internal/repository`, per spec §1)
**Spec:** `docs/requirements/internal-ai-audit-governance-permanent-error-terminal-classification-v1.spec.md` (REQ-1..6, D1..D4)
**Contract:** `docs/campaigns/implementation-gate.md:21` (gate item 1 / T-3)
**Siblings:** `docs/requirements/internal-access-audit-governance-permanent-error-classification-v1.design.md` (owns the baseline classifier/tests/0043) · `cmd-server-…-v1.design.md` (superseded — and **amended by this delta**: its `ErrInvalidEvent`-transient closed-list pin is cancelled, see §0.1) · `internal-access-…relay-metrics-ready-degraded-v1.design.md` (owns the package `TestMain`, §0.1 D-C) · `cmd-server-…ready-degraded-v1.design.md` (orthogonal)
**Merge rule:** `docs/requirements/internal-ai-audit-governance-permanent-error-terminal-classification-v1.merge-plan.md` (companion — landing order, membership winner, fold rule, transient-table ownership)
**Date:** 2026-08-08 · **HEAD:** `acfaaf4` · **Worktree:** dirty — every cited line exists **only** in the uncommitted worktree (see §0)

---

## 0. Baseline caveat (verified, not trusted)

The evidence's "HEAD `acfaaf4` + worktree" verification basis is **accurate and disclosed** (§1 "the worktree already contains a prior-campaign implementation"), but the operative fact is stronger than it says:

| State | What exists |
|---|---|
| HEAD `acfaaf4` | **No terminal classification at all.** `git show HEAD:internal/auditgovernance/relay.go` has no `isPermanentDeliveryError`/`failFact`/`ErrReceiptConflict` — every publish error goes to `retryFact`. No 0042/0043 migration (`git log --all -- <path>` → empty). |
| Worktree | The contract-A baseline (classifier with closed list {`ErrReceiptConflict`, `ErrInvalidReceipt`, 409, 422}, `failFact`, 0042 `failed_at_ns` staged `A` + code half unstaged `M`) **plus** untracked 0043 partial-index migrations and the terminal/EXPLAIN/deviation test files. |

**Consequences for this design:**
1. All line numbers in the evidence/spec are correct **for the worktree**, which is the state this design extends. The implementation must not start until the baseline worktree is committed (§5.1).
2. This design is a **delta**: the only behavioral change is D1 (flip `ErrInvalidEvent` to permanent). Everything else is pinning (D3) + docs (D4). The evidence's framing "REQ-1..6" is correct — but REQ-2/REQ-3/REQ-5/REQ-6 are already implemented in the worktree and only need commit + pin verification, not new code.
3. The evidence's verification register is substantively correct; the nits are cosmetic line anchors (model.go:23→24, relay_terminal_test.go:222-224→221, pending_idx_test.go 285→282 lines, classifier anchored at `relay.go:212` = body line of a `:204-221` function). None affect design decisions.

### §0.1 Supersede rule — sibling contradiction, resolved (adopted from the companion merge plan)

The testing review flagged a guaranteed CI red: the cmd-server sibling pins `ErrInvalidEvent` **transient** in `TestPermanentDeliveryErrorClosedList` while this delta flips it **permanent** in `TestIsPermanentDeliveryErrorClosedList` — same package, different function names, both compile, whichever lands second fails. This design now states the rule explicitly:

- **D-A — Membership winner: permanent (this flip).** The two permanent-error siblings' transient pins describe the pre-flip classifier; their rationale (retry-forever keeps the `/readyz` 503 bridge alive) is superseded by the verified counter-case (deterministic producers `http.go:101/:105/:113`; contract T-3 "`Ready()` 含 dead 行 = true"; Error log + `relay_dead_total` + 7 d retained row; horizon re-attempt *converges to delivery*). Both directions remain pinned by the flipped closed-list table.
- **D-B — Landing order:** ① contract-A baseline worktree commit → ② B3-1 commit (internal-access spec: classifier + F-A1 split + 0043 + `relay_terminal_test.go` + `audit_governance_pending_idx_test.go` + 300 s pin + merged transient table + grep-assert) → ③ this delta (flip + docs) → ④ B3-2 ready-degraded → ⑤ B3-4 relay-metrics. `runtime_classify_test.go` is **never authored** (cmd-server D3 cancelled); the internal-access D7 fold degenerates to a no-op — no opposite pin can be imported.
- **D-C — TestMain singleton:** `relay_metrics_test.go:30` owns the package's single `TestMain`; a second one in `package auditgovernance` is a compile error. This delta plans none (compliant); B3-2's `runtime_ready_test.go` plans none (compliant).
- **D-D — Fold-rule collision:** cmd-server's AC-1/AC-2/AC-4 cases are already covered 1:1 by the terminal table's five cases + retention block; its closed-list pin is never authored (the canonical pin is `TestIsPermanentDeliveryErrorClosedList`, flipped); its AC-3 transient table folds into the merged transient table (D-E).
- **D-E — Transient-table ownership:** authored **once**, at the B3-1 commit, in `relay_terminal_test.go` — merged membership {401, 403, 500, 503, conn-refused}, **inter-POST gap ≤ 3 s** (2 s cap + 1 s slack; strict ≤ 2 s flakes ~50 % of runs), poll-until claimability (5 s deadline). It is phantom today (verified absent from the worktree).

---

## 1. Verification register (evidence claims re-checked, not trusted)

| # | Claim | Verdict |
|---|---|---|
| E1 | `deliverFact` `relay.go:82-113` routes through classifier `:87`; `failFact` `:120-132`; `retryFact` `:134-148`; `boundedBackoff` `:174-190` (per-delay cap only); `classifyRelayError` `:192-201`; `isPermanentDeliveryError` `:204-221` | ✅ **worktree-exact.** Classifier = {`ErrReceiptConflict`, `ErrInvalidReceipt`, `*httpStatusError` 409/422}; `ErrInvalidEvent` **absent** (doc comment explicitly lists it transient). No `MaxAttempts` anywhere in the package (`grep` → empty). |
| E2 | `ErrInvalidEvent` producers at `http.go:101/:105/:113` (validOutboundFact / binding missing / marshal); `validateReceipt` `:178-206`; `receiptMatches` `:214-225` | ✅ **exact.** non-202 → `&httpStatusError{Status}` `:182`; `ErrInvalidReceipt` `:186/:190/:194/:204`; `ErrReceiptConflict` `:201`; 401 token invalidation `:126-127`. All three `ErrInvalidEvent` producers are deterministic publisher-side — retry cannot fix (D1 rationale). |
| E3 | `model.go` sentinels + `httpStatusError` | ✅ `ErrInvalidEvent` `:25`; `httpStatusError` struct `:31`, `Error()` `:35-37` (spec's `:23-28`/`:30-37` off by one on `ErrInvalidConfig` `:24` — cosmetic). |
| E4/E5 | 0039 has no `status`/`dead_at`, `due_idx` without `failed_at_ns`; 0042 adds `failed_at_ns` only, no index — both dialects | ✅ **content-exact** (sqlite `INTEGER` / pg `BIGINT` `NOT NULL DEFAULT 0`; down = `DROP COLUMN`). ⚠️ 0042 is staged, **never committed** (§0). |
| E6 | 0043 partial indexes untracked, both dialects: `pending_claim_idx (available_at_ns, created_at_ns, id)` + `pending_lag_idx (created_at_ns)`, both `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`; down = `DROP INDEX IF EXISTS` ×2; header documents the `status`/`dead_at` deviation | ✅ **exact** (all four files `??` untracked). |
| E7 | `failed_at_ns=0` predicates at claim.go `:38/:62/:88/:146/:168/:195/:207`; `CleanupFailedAuditGovernance` cleanup.go `:113` | ✅ **exact.** Claim/lag/fail all exclude dead rows; prune `DELETE ... WHERE failed_at_ns>0 AND failed_at_ns <= $1` both dialects. |
| E8 | `config_audit_governance.go:65` `MaxBackoffSeconds` default 300; `docs/configuration.md:273` "Retry cap; facts retry indefinitely." | ✅ **both exact.** |
| E9 | Harness `runtimeConfig` `runtime_test.go:40-47` (poll 10 ms / backoff 1→2 s / retention 3600 s); `TestRuntimeConflictingReceiptIsTerminalWithRetention` `:117-186`; `TestBoundedBackoffIsDeterministicAndCapped` `:189-205` with 300 s pin `:202-204` | ✅ **exact.** Pin: deterministic ∧ `> 200 s` ∧ `≤ 300 s` at max=300 s. |
| E10 | `relay_terminal_test.go`: terminal table (`runTerminalCase`, observe window 2.6 s `:86`, `assertTerminalState`/`assertTerminalRetention`), closed-list test `:199` with `ErrInvalidEvent` in the **transient** list `:221` | ✅ **exact** (spec's `:222-224` = block tail; item at `:221`). Permanent list `:200-205` = {conflict, invalid-receipt, 409, 422}, each wrapped `%w`. |
| E11 | EXPLAIN + deviation pins in `internal/repository/audit_governance_pending_idx_test.go` (untracked, 282 lines) | ✅ present: `TestAuditGovernancePendingIndexesServeClaimAndLagPlans`, `TestAuditGovernance0043DeviationHeaderPinned`. |
| E12 | Build/tests green | ✅ `go build ./...` clean; `go test ./internal/auditgovernance/ ./internal/repository/ ./internal/config/` → `ok` (cached) on this worktree. |
| — | **Sibling rationale not surfaced by the spec:** internal-access design D2 deliberately kept `ErrInvalidEvent` transient — "with no requeue surface, retry-forever + the `/readyz` 503 bridge keeps the operator signal alive instead of silently dropping governance data". The spec's D1 reversal doesn't engage this counterargument | ⚠️ **design gap — addressed here (§4 F2):** the flip is still correct (producers deterministic; direction names it permanent), but the operator-signal consequence must be explicit. |

Net: the evidence is trustworthy on substance; every design-relevant fact reproduces. The delta below is exactly the spec's REQ-1 (classifier flip) + REQ-5 (docs) + pin-commit of the existing worktree implementation (REQ-2/3/6).

---

## 2. Design (delta on the worktree baseline)

### D1 — Classifier flip: `ErrInvalidEvent` → permanent (`relay.go:204-221`, ~1 line + 2 comment edits)

```go
func isPermanentDeliveryError(err error) bool {
	if errors.Is(err, ErrReceiptConflict) || errors.Is(err, ErrInvalidReceipt) ||
		errors.Is(err, ErrInvalidEvent) {
		return true
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.Status == http.StatusConflict || status.Status == http.StatusUnprocessableEntity
	}
	return false
}
```

- **Rationale (evidence E2):** all three `ErrInvalidEvent` producers (`http.go:101` `validOutboundFact` failure, `:105` missing binding, `:113` `json.Marshal` failure) are deterministic publisher-side defects — the fact's shape/context is fixed, so a re-POST can never succeed. The direction's problem statement explicitly lists publisher-side `ErrInvalidEvent` among the permanent rejections (spec D1).
- **Comment edits (mandatory, keep classifier↔`classifyRelayError` sync per the existing cross-reference convention):** ① the classifier's doc comment currently enumerates `ErrInvalidEvent` in the "everything else is transient" clause — remove it; ② the `deliverFact` branch comment (`relay.go:85-90`) enumerates permanent classes without `ErrInvalidEvent` — add it, and note that the terminal branch increments `audit_governance.relay_dead_total` (the log-independent visibility surface replacing the `/readyz` bridge; F2/§4.1). `classifyRelayError` needs **no** change (already labels `ErrInvalidEvent` with its own text — no drift).
- **No signature/API change:** unexported, same shape; closed-list-by-construction property preserved (`errors.Is`/`errors.As` — wrapped sentinels classify identically, already exercised by the permanent-list `%w` wrap in the pin test).

### D2 — Flip the closed-list pin (`relay_terminal_test.go:200-224`)

Move `ErrInvalidEvent` from the transient slice (`:221`) to the permanent slice (`:200-205`). The permanent slice's `fmt.Errorf("%w: wrapped", err)` wrap already proves wrapped-sentinel classification for the new member. No other test changes: the terminal table (5 cases), 300 s backoff pin, EXPLAIN tests, and deviation-header pin all stay as-is and must keep passing unmodified. Two items named here are **planned, not present** (verified): the transient table (500/503/conn-refused) and the single-`retryFact`-call-site grep-assert are **authored at the B3-1 commit** (§0.1 D-E / §6 AC-4 pattern `r\.retryFact(`); this delta does not touch them.

### D3 — Commit the existing pins (no code change, commit-scope)

The following worktree artifacts are **untracked/staged and must ship in this commit** — they are the acceptance anchors (spec REQ-2/3/6):

| Artifact | Role |
|---|---|
| `migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` (untracked `??`) | REQ-3 — dead-predicate partial indexes, both dialects; deviation header; I2-clean (new file, 0042 untouched) |
| `internal/auditgovernance/relay_terminal_test.go` (untracked) | REQ-6.1/6.2/6.3 — terminal table, transient table, closed-list pin (flipped by D2) |
| `internal/repository/audit_governance_pending_idx_test.go` (untracked) | REQ-6.4 — EXPLAIN-qualified index use + 0043-deviation pin (0042 contains no `CREATE INDEX`) |
| Staged 0042 pair + unstaged code half (`relay.go`, `http.go`, `model.go`, claim/cleanup/types, `runtime_test.go`, `audit_governance_test.go`) | §0 precondition — must land **before** this delta (commit-order, §5.1) |

### D4 — Docs sync (`docs/configuration.md:273`)

Replace the `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` row value "Retry cap; facts retry indefinitely." with text stating: transient failures retry with bounded backoff capped at this value; permanent rejections (conflict / invalid receipt / invalid event, HTTP 409/422) land terminal-with-retention and are pruned after `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS`. (Spec REQ-5 wording.)

### D5 — Non-goals (unchanged from spec)

No attempt cap / no config knob (REQ-4) · no `status`/`dead_at` rename (0042 deviation documented, pinned) · no changes to claim/cleanup SQL · no `cmd/server`/events-outbox changes · B3-2/B3-3/B3-4 out of scope · `classifyRelayError` untouched.

---

## 3. API changes & compatibility constraints

| Surface | Change | Compatibility |
|---|---|---|
| Public API (REST/S3/MCP/WebDAV/SDK/CLI) | **None** | No versioning concern |
| `internal/auditgovernance` | `isPermanentDeliveryError` +1 branch (`errors.Is(err, ErrInvalidEvent)`); 2 comments | Unexported; same signature; no call-site changes |
| Behavior: malformed facts (`ErrInvalidEvent`) | retry-forever (bounded per-delay 300 s) → **terminal-with-retention** on first attempt | Deterministic producers (`http.go:101/:105/:113`) ⇒ no legitimate transient path is reclassified; 5xx/429/401/transport/context stay transient |
| `internal/repository` | 0043 pair **committed** (already written, untracked) | I2: 0042 untouched (deviation pin test asserts); applies on any 0039..0042 schema |
| Config/env | **None** | 300 s cap remains the transient horizon; no new knobs (REQ-4) |
| Dependencies | **None** | I6 satisfied; no `go.mod` change |

**Behavioral contracts that must hold (constraints carried from the spec):**
- Permanent ⇒ `failed_at_ns` set on the **first** attempt; row excluded from claim/lag (`failed_at_ns=0` predicates already in place, E7); retained with `last_error` (≤512 B) until the retention prune.
- **Horizon re-attempt (unchanged baseline, spec-adjacent):** after the retention prune (~7 d default) the row is gone, the origin becomes a gap, and `reconcile()` re-enqueues a fresh fact (`uuid.NewString()`, `attempts=0`) — so a *deterministic* failure re-POSTs **once per retention window**, not zero times, and not forever-bounded-backoff. "Terminal" = terminal within the window (same semantics as the conflict case; sibling design R1). The flip does not eliminate this cycle; it bounds the POST rate from ~1/backoff-cap to ~1/retention-window.
- `TestRuntimeConflictingReceiptIsTerminalWithRetention` (conflict path) and `TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference` (binding removal, `New()`-time rejection — not delivery classification) pass **unmodified**.
- `relay_metrics_test.go`'s 500-retry client (re-POST + `failed` counter) unaffected — 500 stays transient.

---

## 4. Failure modes

| # | Failure | Mitigation |
|---|---|---|
| F1 | **Misclassification drift** — a future producer returns `ErrInvalidEvent` for a transient condition, or a future edit moves the sentinel into the transient branch | Producers are exactly 3 fixed sites (`http.go:101/:105/:113`, E2); closed-list pin flipped by D2 fails any drift in **both** directions; the classifier↔`classifyRelayError` cross-reference comment is updated (D1). |
| F2 | **Operator-signal regression** (sibling design D2's stated rationale: retry-forever kept the `/readyz` maxLag 503 bridge alive for malformed facts; the flip silences that signal) | Accepted deliberately — **probe-silent by design, contract-clean**: the producers are deterministic, so the persistent 503 was an *unrecoverable false-degradation alarm* — the runtime can never clear the row, readiness de-registers the pod while liveness stays green (no restart; helm readinessProbe on `/readyz`, `deployment.yaml:85`), and the contract's T-3 line ("`Ready()` 含 dead 行 = true") prescribes this direction. Post-flip, `/readyz` 503 means **recoverable backlog** (transient class, 300 s cap), drain-in-progress, or dependency down. The malformed class moves off the probe onto **four log/counter surfaces**: ① `failFact` Error log (persist-then-log, fires on attempt 1); ② row + `last_error` (≤512 B) retained until the 7 d prune, SQL-queryable; ③ **`audit_governance.relay_dead_total`** — monotonic Int64Counter, incremented by `failFact` (`relay.go:121`, before persistence) on every terminal classification, **log-independent** (fires with the log pipeline down), scraped as `audit_governance_relay_dead_total` on `/metrics` (opt-in) and via OTLP, wire-pinned by `relay_metrics_test.go`; ④ **optional alert rule `AuditGovernanceRelayTerminalFailures`** in `deploy/prometheus/alerts.yml` (rule text §4.1; parity with the `EventOutboxTerminalFailures` precedent; inert unless `PROMETHEUS_ENABLED` — I5). **Correction of the earlier §3 claim:** the horizon re-attempt does **not** surface recurrence once per window — the reconstructed fact passes `validOutboundFact` by construction, so the re-POST *succeeds and delivers* (self-healing convergence); recurrence is visible only via fresh `relay_dead_total` increments (new malformed facts). Sink-outage signal (5xx/network) is unaffected — those stay transient and keep the 503 bridge. Documented in the D1 comment so the rationale survives. |
| F3 | **Merge-order risk** — delta lands before/without the baseline worktree commit | §5.1 precondition: baseline commit first; D1/D2 anchors are worktree-based line numbers and would shift on a clean HEAD (no classifier exists there). |
| F4 | **Doc drift** (`docs/configuration.md:273` reworded back, or missed) | Review gate only (no test pin — spec REQ-5 is doc-only; keeps scope tight). |
| F5 | **Test-file ownership collision** — three claimants on `relay_terminal_test.go`: internal-access authored it, cmd-server's D3 planned `runtime_classify_test.go` (fold rule D7 would import its opposite `ErrInvalidEvent`-transient pin), this delta edits it | §0.1 D-D: cmd-server D3 **cancelled** — `runtime_classify_test.go` is never authored, so the fold is a no-op and no opposite pin can be imported; cmd-server's AC-1/2/4 are already covered by the five-case table + retention block; its closed-list pin is never authored (canonical pin = `TestIsPermanentDeliveryErrorClosedList`, flipped). Single file, single table, single pin. **`TestMain` rule (D-C):** `relay_metrics_test.go:30` owns the package's only `TestMain` — no other auditgovernance test file may define one (compile error). |
| F6 | **Timing flake on loaded CI** | Proven harness (atomic counters, poll-until 3 s deadline, observe 2.6 s > max backoff 2 s, no wall-clock equality) — unchanged by this delta (D2 touches a pure-function list only). |
| F7 | **File-size gate** (≤500 lines/file) | Delta: `relay.go` 221+3, `relay_terminal_test.go` 231±0 — no file crosses. |

---

**4.1 Deploy artifact — `AuditGovernanceRelayTerminalFailures` (optional-by-default, ships with the flip):**

```yaml
      # IncAuditGovernanceRelayDead (internal/telemetry/metrics.go:106,210;
      # incremented by failFact at relay.go:121): audit-governance facts
      # reaching terminal 'failed' — conflict receipt, invalid receipt, HTTP
      # 409/422, or malformed event (ErrInvalidEvent, permanent-error
      # classification). Rows are retained AUDIT_GOVERNANCE_DELIVERED_
      # RETENTION_SECONDS (7d default) then pruned; the horizon re-attempt
      # re-POSTs a *normalized* fact that delivers, so a fresh increment means
      # a NEW unrecoverable fact, not the same origin. Counter is monotonic
      # and log-independent — fires even with the log pipeline down. L0
      # audit_log remains authoritative in all cases. readyz intentionally
      # does NOT 503 for this class (contract T-3: Ready() with dead rows =
      # true); this alert is the probe-silent trade's visibility surface.
      - alert: AuditGovernanceRelayTerminalFailures
        expr: sum(rate(audit_governance_relay_dead_total[15m])) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Audit-governance facts reached terminal failed state"
          description: "Audit-governance facts hit terminal 'failed' (conflict/invalid receipt, 409/422, or malformed event). Rows are retained 7d (delivery-recovery SLA) then pruned; horizon re-attempts deliver normalized data, so recurrence means new unrecoverable facts. Inspect audit_governance_outbox.last_error; L0 audit_log remains authoritative."
```

> Naming follows the `EventOutboxTerminalFailures` precedent (same terminal-with-retention + 7 d prune + L0-authoritative shape). The rule is class-agnostic — the counter carries no attributes — matching the precedent exactly. `AGENTS.md:140`'s alert count ("12 条") is already stale (the file holds 13; `EventOutboxTerminalFailures` post-dates it) and moves to 14 in the same commit.

---

## 5. Migration steps & rollback

**5.1 Preconditions (commit order — hard):** ① commit the **entire contract-A baseline worktree** — staged 0042 pair **and** the unstaged code half (`relay.go`, `http.go`, `model.go`, claim/cleanup/types, `runtime_test.go`, `audit_governance_test.go`, `repository_interface.go` etc.) — so the terminal classifier, `failed_at_ns` schema, and conflict tests are in history; ② then commit this delta (D1+D2+D4 + the untracked pins 0043/`relay_terminal_test.go`/`audit_governance_pending_idx_test.go`). Never start on a clean HEAD (§0). Committing only the staged index would ship 0042 without its code half.

**5.2 Implementation order (each step `make check`-green):** ① D1 classifier flip + comment edits → ② D2 pin flip → ③ `go build ./... && go vet ./... && go test ./internal/auditgovernance/ ./internal/repository/` (SQLite; zero network beyond `httptest`) → ④ D4 docs line → ⑤ commit. No new migration is authored — 0043 already exists and is covered by its EXPLAIN + deviation-pin tests.

**5.3 Deployment:** one release commit. `repo.Migrate` applies 0043 at startup (serial, version-skipped) on both dialects; no data migration (index over existing columns; existing pending/terminal rows covered by the predicate). Postgres `CREATE INDEX` takes a brief `SHARE` lock on `audit_governance_outbox` — one-shot, acceptable at outbox scale (no `CONCURRENTLY` in the runner). The classifier flip is pure code — no restart ordering beyond the normal rollout.

**5.4 Rollback:** revert the delta commit. Old binary (baseline) treats `ErrInvalidEvent` as transient again → malformed facts return to bounded-backoff retry (pre-change behavior). Rows already `failed_at_ns>0` from the flipped window remain terminal until the retention prune — **not** resurrected by the older binary (claim predicates exclude `failed_at_ns != 0`). 0043 down is optional (index inert if unused); run it only if schema byte-identity with the released baseline is required. For immediate resurrection of wrongly-dead-lettered rows, the sibling design's guarded UPDATE (`internal-access-…-v1.design.md` §5.4: `failed_at_ns=0, last_error=''` with binding-presence check, rowcount 1-per-target) applies unchanged — re-failure restarts the retention clock.

---

## 6. Testable acceptance mapping (spec AC → test → assertion anchors → gate)

| AC (spec §5) | Test | Assertion anchors | Gate |
|---|---|---|---|
| **AC-1** — 422/409/malformed/tenant-mismatch → exactly 1 POST, terminal, not claimable, not pending, retention-pruned | `TestRuntimePermanentDeliveryErrorsAreTerminal` (5 cases: 409, 422, tenant-mismatch, non-ledgered status, unparseable body; `runTerminalCase` observe 2.6 s > max backoff 2 s) + `TestRuntimeConflictingReceiptIsTerminalWithRetention` (unmodified) | `posts == 1` after observe + `Close()`; `ClaimAuditGovernance` → `len == 0`; `OldestPendingAuditGovernance` → `ok == false`; `CleanupFailedAuditGovernance(now-1h)` → 0, `(now+1h)` → 1; direct read-back `failed_at_ns > 0` | `make check` |
| **AC-2** — transient 5xx/network still retry, delay capped ≤300 s | Transient table (merged membership {401, 403, 500, 503, conn-refused}: re-POST ≥2 within 6 s, **inter-POST gap ≤ 3 s = 2 s harness max backoff + 1 s slack** — strict ≤ 2 s flakes ~50 % of runs: for attempts ≥ 2 the jittered delay caps at *exactly* 2 s in ~50 % of runs, so the observed gap = 2 s + poll/HTTP latency > 2 s on correct code; row claimable after backoff via poll-until, 5 s deadline) + `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:202-204`: `> 200 s` ∧ `≤ 300 s` at max=300 s) + one-line `getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` default check | authored at the B3-1 commit (§0.1 D-E); untouched by this delta | `make check` |
| **AC-3** — dead-predicate partial index both dialects, EXPLAIN-qualified, no re-ship into 0042 | `TestAuditGovernancePendingIndexesServeClaimAndLagPlans` (seeded store; claim + lag plans name `audit_governance_pending_claim_idx`/`pending_lag_idx`, both `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`) + `TestAuditGovernance0043DeviationHeaderPinned` (0043 header tokens both dialects; 0042 contains no `CREATE INDEX`) | commit of the untracked 0043 pair + test file (D3) | `make check` (SQLite); PG plans re-verified under `make test-integration` (skip-if-unreachable) |
| **AC-4** — no unbounded retry path: `retryFact` reachable only from non-permanent classes | `TestIsPermanentDeliveryErrorClosedList` **with `ErrInvalidEvent` in the permanent slice (D2 flip)** — both directions, wrapped `%w`; grep-assert **`grep -rn "r\.retryFact(" internal/auditgovernance/`** → exactly 1 hit (`relay.go:101` call site; the definition at `relay.go:134` is excluded — the bare `retryFact(` pattern matches both and fails on landing), package-wide scope (a call site added in a *new* file must not escape), guarded by `if isPermanentDeliveryError(err) → failFact; return`; authored at the B3-1 commit | the closed list **is** the gate: any future permanent class without classifier coverage fails CI | `make check` + grep assert |

**REQ trace:** REQ-1 → D1/D2 · REQ-2 → D3 (existing code, pinned by AC-1/AC-4) · REQ-3 → D3 (0043 commit) · REQ-4 → D5 · REQ-5 → D4 · REQ-6 → D2/D3/AC-1..4.

---

## 7. Files changed (complete list for this delta)

| File | Change |
|---|---|
| `internal/auditgovernance/relay.go` | +1 classifier branch (`errors.Is(err, ErrInvalidEvent)`), 2 comment edits (D1) |
| `internal/auditgovernance/relay_terminal_test.go` | move `ErrInvalidEvent` transient→permanent in the closed-list pin (D2); file itself untracked → added by this commit |
| `docs/configuration.md` | row `AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS` reworded (D4) |
| `internal/repository/migrations/{sqlite,postgres}/0043_audit_governance_pending_partial_index.{up,down}.sql` | **added to git** (already written, untracked; D3) |
| `internal/repository/audit_governance_pending_idx_test.go` | **added to git** (untracked; D3) |
| `deploy/prometheus/alerts.yml` | +1 rule `AuditGovernanceRelayTerminalFailures` (§4.1; 13→14) |
| `AGENTS.md` | `:140` alert count 12→14 (corrects pre-existing drift; dashboard panel counts 12/17 already match) |
| Baseline worktree files (staged 0042, unstaged code half) | **committed first** (§5.1), not modified by this delta |
