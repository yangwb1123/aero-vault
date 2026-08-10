# Merge / landing plan — sibling-design contradiction resolution for the `ErrInvalidEvent` permanent flip (internal-ai delta)

**Companion to:** `docs/requirements/internal-ai-audit-governance-permanent-error-terminal-classification-v1.design.md` (the delta; adopts this rule)
**Audited designs:** cmd-server-…permanent-error-classification · internal-access-…permanent-error-classification · internal-access-…relay-metrics-ready-degraded · cmd-server-…ready-degraded · internal-ai-…terminal-classification
**Verified against:** live worktree 2026-08-08 (untracked test files, `TestMain` at `relay_metrics_test.go:30`, pin at `relay_terminal_test.go:199-230`, classifier at `relay.go:204-221`)
**Verdict:** the testing review's "guaranteed CI red" is real and currently unresolved — this plan resolves it. Membership winner = **the flip (permanent)**; the two permanent-error siblings' transient pins are amended, not just outvoted.

---

## 1. Contradiction inventory (all pins re-verified, not trusted)

| Design | File plan for `package auditgovernance` | `ErrInvalidEvent` pin | TestMain | Closed-list test name |
|---|---|---|---|---|
| **cmd-server-…permanent-error** (B3-1c) | `runtime_classify_test.go` **new** ~230 l (§ D3) | **transient** — `TestPermanentDeliveryErrorClosedList` at `:200` (§ D3/§6) | none planned | `TestPermanentDeliveryErrorClosedList` |
| **internal-access-…permanent-error** (B3-1a; supersedes B3-1c) | `relay_terminal_test.go` **new** ~250 l (§ D5.1) + fold rule D7 ("if `runtime_classify_test.go` exists, delete it and fold its cases in") | **transient** — D5.1a closed-list table | none planned | D5.1a (worktree name: `TestIsPermanentDeliveryErrorClosedList`) |
| **internal-access-…relay-metrics** (B3-4) | `relay_metrics_test.go` **new** — **owns the package `TestMain` (`:30`)** | n/a | **owner** | n/a |
| **cmd-server-…ready-degraded** (B3-2) | `runtime_ready_test.go` **new** ~180 l | n/a | none planned | n/a |
| **internal-ai … terminal-classification** (this delta) | edits `relay_terminal_test.go` (D2 pin flip); commits 0043 + both untracked test files | **permanent** (D1 flip + D2 pin flip) | none planned (correct) | `TestIsPermanentDeliveryErrorClosedList` |

**Live worktree facts (anchor, not speculation):**
- `internal/auditgovernance/relay_terminal_test.go` — untracked, 231 l. `TestIsPermanentDeliveryErrorClosedList` at `:199`; permanent slice `:201-206` = {`ErrReceiptConflict`, `ErrInvalidReceipt`, `&httpStatusError{409}`, `&httpStatusError{422}`}; `ErrInvalidEvent` **transient** at `:221`. Terminal 5-case table + retention present. **Transient status table absent** (phantom in both the internal-ai and cmd-server plans).
- `internal/auditgovernance/relay_metrics_test.go` — untracked, 211 l; `func TestMain` at `:30`; `TestRuntimeRelayCountersTrackDeliveryOutcomes` at `:88`.
- `internal/repository/audit_governance_pending_idx_test.go` — untracked, 282 l (`assertPlan`, 3 tests).
- 0043 pair untracked both dialects; 0042 staged `A` (never committed).
- `grep -rn "retryFact(" internal/auditgovernance/` → **2 hits** (call `relay.go:101` + definition `relay.go:134`). No grep-assert exists in any test file.
- Classifier `isPermanentDeliveryError` `relay.go:204-221`; `ErrInvalidEvent` absent, doc comment lists it transient.

**Mechanics of the guaranteed red (confirmed):** all three permanent-error artifacts live in `package auditgovernance`; the two pins use **different function names** (`TestPermanentDeliveryErrorClosedList` vs `TestIsPermanentDeliveryErrorClosedList`), so both compile; whichever lands second fails `go test ./internal/auditgovernance/` deterministically — a pure merge-order failure with no code defect. The internal-access fold rule (D7) *increases* the blast radius: folding cmd-server's closed-list case "as-is" imports the opposite pin into the canonical file.

---

## 2. Decisions (the supersede/amendment rule)

### D-A — Closed-list membership winner: **permanent (the flip stands)**

All three designs' transient pins describe the *pre-flip* classifier; the flip's case is verified, not asserted (reliability reviewer, code-anchored):

1. **Producers are deterministic.** All three `ErrInvalidEvent` producers (`http.go:101` `validOutboundFact`, `:105` missing binding, `:113` marshal) are publisher-side invariants — retry can never converge; retry-forever was bounded-backoff-forever noise.
2. **The sibling's own rationale is superseded, not violated.** "Retry-forever keeps the `/readyz` 503 bridge alive" is mechanically true but goal-subsumed: the signal moves to an **Error-level log on attempt 1**, a **monotonic `audit_governance.relay_dead_total` counter** (`telemetry/metrics.go:106,210`, alertable via `/metrics`), a **7 d retained row with `last_error`**, and the **horizon re-attempt** — which on every reachable path *converges to delivery* (reconstructed facts are normalized by `factFromGap` and pass `validOutboundFact` by construction), so recurrence-per-window wording is wrong (see D-F-3).
3. **The contract sides with the flip.** `implementation-gate.md:21` T-3: "`Ready()` 含 dead 行 = true". The flip makes malformed-fact rows dead → excluded from `OldestPendingAuditGovernance` → readyz healthy — contract-clean probe semantics; a permanently-pending poisoned row 503-ing the node forever was a false degradation the runtime cannot fix.
4. **Both directions stay pinned.** The flipped closed-list table still fails on any drift in *either* direction; the amended cmd-server pin is the same assertion under the canonical name.

### D-B — Landing order (each step `make check`-green; no step may skip a predecessor)

| # | Commit | Contents | Gate |
|---|---|---|---|
| 1 | **Contract-A baseline** | staged 0042 pair + unstaged code half (`relay.go` conflict-terminal branch, `ErrReceiptConflict`, `failFact`, claim/lag/fail/cleanup SQL, `runtime_test.go`/`audit_governance_test.go` terminal tests) | precondition stated by internal-access §5.1 *and* internal-ai §5.1; nothing else lands before it |
| 2 | **B3-1 (internal-access spec)** | classifier D1/D2 + **F-A1 split** (`http.go:188-190`, adopted from cmd-server §1) + 0043 pair + `relay_terminal_test.go` + `audit_governance_pending_idx_test.go` + `runtime_test.go` 300 s pin + **the merged transient table** (D-E) + the grep-assert (D-F-1) | `make check` |
| 3 | **B3-1b (this internal-ai delta)** | D1 flip + D2 pin flip + D4 docs. The untracked pins shipped in commit 2 — D3 reduces to a commit-scope note | `make check` |
| 4 | **B3-2 (ready-degraded)** | `runtime_ready_test.go`, `http.go` degraded payload, gauges, alert rule | per B3-2 §6 |
| 5 | **B3-4 (relay-metrics)** | `relay_metrics_test.go` (TestMain) + relay.go increments + `metrics.go` counters | per B3-4 §6 |

Commit 1 must precede 2 (baseline contains the classifier's preconditions); 3 must follow 2 (it flips the pin file 2 ships); 4/5 follow their own D4 canonical merge rule ("B3-2 lands first or both in one change") and are orthogonal to 2/3 (B3-1's `failed_at_ns=0` exclusion means dead-row semantics compose unchanged; the flip only changes *which* rows are dead).

### D-C — `TestMain` singleton ownership: **`relay_metrics_test.go` owns it, exclusively**

A second `TestMain` in `package auditgovernance` is a **compile error**, not a runtime failure. Rule: `relay_metrics_test.go:30` is the package's single `TestMain`; no other auditgovernance test file — from B3-1, B3-1b, B3-2, or any future sibling — may define one. Tests needing the Prom handler reuse the exported-in-package `promHandler` var. Audited: B3-1 (none), B3-1b (none), B3-2's `runtime_ready_test.go` (none) — all compliant; the rule is now explicit so it stays that way.

### D-D — Fold-rule collision on `relay_terminal_test.go`: **`runtime_classify_test.go` is never authored; the fold becomes a no-op delete**

The collision is that cmd-server's D3 *creates* the file whose later deletion (internal-access D7) would import the opposite pin. Resolution:

1. **Cancel cmd-server D3 at the source.** `runtime_classify_test.go` is never created by any commit. The internal-access D7 "delete and fold" then degenerates to a no-op — nothing to delete, nothing folded as-is, the opposite pin can never be imported.
2. **cmd-server's AC-1/AC-2/AC-4 cases are already covered 1:1** by the worktree terminal table — mapping: AC-1 (409/422) → cases (a)/(b); AC-2 (tenant-mismatch / non-ledgered / unparseable) → cases (c)/(d)/(e); AC-4 (retention) → the 409 retention block. **Zero new test code.**
3. **cmd-server's closed-list pin is never authored.** The single canonical pin is `TestIsPermanentDeliveryErrorClosedList` with the **flipped** membership (D-A).
4. **cmd-server's AC-3 transient table is the only genuinely new artifact** — it folds in as the merged transient table (D-E).

Result: exactly one file (`relay_terminal_test.go`), one table per concern, one closed-list pin, flipped.

### D-E — Transient-table coverage ownership: **`relay_terminal_test.go`, authored once at the B3-1 commit**

The transient table is **phantom in both designs** (verified absent from the worktree; the only committed transient evidence is `relay_metrics_test.go`'s 500-retry counter assertions, which pin counts, not gaps or claimability). Ownership and shape:

- **Owner/author:** the B3-1 commit (internal-access), in `relay_terminal_test.go` — single file, single table.
- **Merged membership:** {401, 403, 500, 503, conn-refused} (cmd-server's AC-3 401/403/500 ∪ internal-ai AC-2's 500/503/conn-refused). 401 case keeps the token-revalidation sink behavior; conn-refused case tolerates the immediate-connect-failure path in the first-POST poll.
- **Gap bound: ≤ 3 s = 2 s harness cap + 1 s slack** — cmd-server's verified conclusion; strict ≤ 2 s flakes ~50 % of runs (49.9 % of fact IDs jitter-clamp to exactly 2.0 s; observed gap = 2 s + poll/HTTP latency > 2 s on correct code). The internal-ai AC-2 "≤ 2 s" phrasing is amended (D-F-2).
- **Claimability anchor:** poll-until `ClaimAuditGovernance` (5 s deadline), never fixed sleep.
- The 300 s default-boundary pin stays `TestBoundedBackoffIsDeterministicAndCapped` (`runtime_test.go:202-204`, `> 200 s` ∧ `≤ 300 s`) + the one-line `getEnvInt(…, 300)` default check (closes the silent-drift hole where a 240-300 s default change passes the window).

### D-F — Carried mechanical fixes (verified, must land with the design)

1. **Grep-assert (AC-4):** the spec'd `grep -rn "retryFact("` matches the definition too (2 hits today) — the pattern must be **`grep -rn "r\.retryFact(" internal/auditgovernance/`** (package-wide, exactly 1, definition excluded). It is **phantom** today — author it at the B3-1 commit; comment that a deliberate second guarded call site (ack-lost→retry, requeue) requires conscious pin amendment.
2. **AC-2 gap bound:** ≤ 2 s → **≤ 3 s** (rationale D-E).
3. **F2(c) wording (reliability review, factual):** the horizon re-attempt does **not** "surface recurrence once per window" — reconstructed facts are normalized and pass `validOutboundFact` by construction, so the re-POST *succeeds*: it is a **self-healing convergence** (one Error log per malformed origin). And the residual-visibility list must add **`audit_governance.relay_dead_total`** (`telemetry/metrics.go:106,210`; the only log-independent alarm for the malformed-fact class) or explicitly accept "probe-silent by design, contract-clean".
4. **Phantom references:** "transient table … stays as-is" and "grep-assert … stays as-is" in D2/§6 are references to files that do not exist — reword to "authored at the B3-1 commit (D-E/D-F-1); untouched by this delta".

---

## 3. Per-sibling amendment instructions

### 3.1 `cmd-server-audit-governance-permanent-error-classification-v1.design.md` (B3-1c)

1. **Header:** add to the sibling/supersession line — *"superseded on classifier/tests by the internal-access design; further amended by the internal-ai delta: `ErrInvalidEvent` membership is flipped to **permanent**; this document's closed-list pin is cancelled."*
2. **§ D3:** cancel the creation of `runtime_classify_test.go`; replace its file plan with a mapping note — AC-1/AC-2/AC-4 land inside `relay_terminal_test.go`'s existing table (D-D.2), AC-3 folds into the merged transient table (D-E).
3. **§ D3 closed-list pin (`TestPermanentDeliveryErrorClosedList`):** never authored. Membership amended to the flipped set: `ErrInvalidEvent` moves from the transient list to the permanent list. (`ErrInvalidConfig` stays transient — it is not a delivery-path error and neither design moves it.)
4. **§ 1 F-A1 (the `http.go:188-190` read-error split):** **retained** — adopted verbatim into the B3-1 implementation commit (landing order D-B step 2); it is a correctness prerequisite of the classification, independent of the flip.
5. **§ 6 AC-3 gap ≤ 3 s:** already correct — keep as the canonical bound (D-E).

### 3.2 `internal-access-audit-governance-permanent-error-classification-v1.design.md` (B3-1a)

1. **§ D5.1a closed-list table:** remove `ErrInvalidEvent` from the transient list; the pinned table is the flipped one (permanent = {conflict, invalid-receipt, 409, 422, **`ErrInvalidEvent`**}). Note the worktree's `TestIsPermanentDeliveryErrorClosedList` is the canonical function name; cmd-server's `TestPermanentDeliveryErrorClosedList` is never authored (D-D.3).
2. **§ D7 fold rule:** amend the second sentence — "if `runtime_classify_test.go` exists at implementation time (it does not today)" → "**`runtime_classify_test.go` is never authored (cmd-server D3 cancelled); the fold is a no-op — no case is ever imported as-is, so no opposite pin can be folded in.**" Add D-E's transient-table ownership note.
3. **§ D5.1:** unchanged — this design is the canonical owner of `relay_terminal_test.go`; add the merged transient table (401/403/500/503/conn-refused, gap ≤ 3 s, poll-until claimability) as part of D5.1's table set.
4. **New note (D5.1 or § 7):** the package `TestMain` is owned by `relay_metrics_test.go` (B3-4) — this design must not add one (D-C).
5. **§ 5.1 commit order:** unchanged (baseline first); add B3-1b (the flip) as the immediately following commit (D-B).

### 3.3 `internal-access-audit-governance-relay-metrics-ready-degraded-v1.design.md` (B3-4)

1. **§ D5.1:** confirmed sole owner of the package `TestMain` (`relay_metrics_test.go:30`) — add one sentence stating the **singleton rule** (a second `TestMain` in the package is a compile error; D-C).
2. **No other changes.** The B3-1b flip changes which rows reach `failFact`, not the increment sites (D2) or the counting semantics (D3): `dead` now also increments on `ErrInvalidEvent` terminal rows — that is precisely the log-independent alarm the flip's F2 mitigation relies on (`relay_dead_total`). The D4 canonical merge rule with B3-2 is preserved, orthogonal.

### 3.4 `cmd-server-audit-governance-ready-degraded-v1.design.md` (B3-2)

1. **§ 7.3 coordination note:** strengthen — B3-1's flip adds `ErrInvalidEvent` to the terminal class; AC-2 phase A (dead-only store → `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`) is unaffected and *strengthened* (more rows are dead); its phase A drives `FailAuditGovernance` directly, so it has no classifier dependence.
2. **No other changes.** `runtime_ready_test.go` plans no `TestMain` (D-C compliant). The gauge/alert/dashboard surfaces have no membership dependence (they read the degraded cache, not the classifier).

### 3.5 This design (`internal-ai-…-terminal-classification-v1.design.md`) — adoption edits

Applied in §4 below (header sibling line + §0.1 + F2 + F5 + AC-2 + AC-4 + D2/D3 wording):
1. **Header + §0.1:** replace the bare "superseded" with the explicit rule (D-A/D-D) — cmd-server B3-1c is cancelled on classifier/tests; its closed-list pin amended to the flipped membership; `runtime_classify_test.go` never authored.
2. **F5:** name the third claimant and the resolution (D-D), and the TestMain rule (D-C).
3. **AC-2:** gap ≤ 3 s; "unchanged by this delta" → "authored at the B3-1 commit; untouched by this delta".
4. **AC-4:** grep pattern `r\.retryFact(`.
5. **F2:** horizon wording (self-healing convergence) + `relay_dead_total` in the residual-visibility list.
6. **§5.1:** reference the landing order (D-B).

---

## 4. Adoption text (applied to the internal-ai design)

The following edits were applied to `docs/requirements/internal-ai-audit-governance-permanent-error-terminal-classification-v1.design.md` so the design *adopts* the rule rather than merely being told about it:

- **Header sibling line** → names the merge plan and the flip-amendment.
- **New §0.1 "Supersede rule (sibling contradiction, resolved)"** → D-A membership winner, D-D fold cancellation, D-C TestMain singleton, D-B landing order, D-E transient-table ownership.
- **§4 F2** → the design's concurrent revision had already folded the corrected horizon wording (self-healing convergence) + `relay_dead_total` (③) + an optional §4.1 alert rule — verified present; no further edit needed.
- **§4 F5** → explicit three-claimant resolution + TestMain rule.
- **§6 AC-2** → gap ≤ 3 s with the jitter-at-cap rationale.
- **§6 AC-4** → `r\.retryFact(` pattern.
- **§2 D2** → phantom-reference correction ("authored at the B3-1 commit").

With these, the contradiction is resolved *before* implementation: whichever artifact lands, the closed-list membership is single-valued (permanent), the transient table has one owner and one gap bound, `runtime_classify_test.go` cannot exist to be folded, and the package `TestMain` has exactly one owner.
