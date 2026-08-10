# Design — `cmd/server`: permanent-error classification (409/422/tenant-mismatch/invalid-receipt → terminal) with single-sentinel replacement in `deliverFact`

**Module:** `cmd/server` · **Spec:** `docs/requirements/cmd-server-audit-governance-permanent-error-classification-v1.spec.md` (REQ-1..4, D1..D3, AC-1..4)
**HEAD:** `acfaaf4` (all citations re-verified on this checkout, 2026-08-07) · **Date:** 2026-08-07 · **Rev 2** (2026-08-07): folded the four adversarial-review evidence sets — R1 amended to "final within the retention window" (horizon re-attempt); F-A1 read-error split (`http.go:190`); D2 rationale naming 410/501/400 + token-path 401/403/empty-scope transience; §5 runbook SQL with `WHERE failed_at_ns>0` guard + RowsAffected expectation + binding-presence check; ~25-line closed-list pin test; AC-3 ≤3 s slack.
**Scope lock:** exactly one behavior change — `deliverFact` classifies publish errors into permanent (terminal-with-retention) vs transient (bounded-backoff retry) via a closed-list classifier — plus the **F-A1 companion fix** (2 lines in `validateReceipt`, `internal/auditgovernance/http.go:188-190`): a body *read* error on a 202 response is transient, not `ErrInvalidReceipt`. F-A1 is a correctness prerequisite of the same classification (without it, a transient transport error is falsely dead-lettered), not a second feature: it changes error *values* on one client-side path only. **No API/wire/config/schema/repository change, no `cmd/server` handler change** — the scope statement survives the F-A1 edit; rollback remains a binary revert.

---

## 1. Verification register (spec evidence re-checked, not trusted)

Every spec citation was re-located on this checkout. Three minor line-number drifts found in the spec's own citations (semantics unaffected, listed as ⚠️); one new design-relevant finding (R1) absent from the spec.

| # | Spec citation | Re-verified location (HEAD `acfaaf4`) | Verdict |
|---|---|---|---|
| E1 | `deliverFact` `:80-102`; single sentinel at `:84` → `failFact` `:85-89`; else `retryFact` `:90-92` | `relay.go:80` func; `:84` `if errors.Is(err, ErrReceiptConflict)` is the **only** terminal branch; `r.failFact(fact, err)` at **`:90`**, `r.retryFact(fact, err)` at **`:94`** | ✅ semantics exact. ⚠️ **branch line numbers off by 5**: `:85-89` is the branch comment, `:90-92` is failFact+return+`}`, `:93` is the `if err != nil` gate. No impact on the design. |
| E2 | `boundedBackoff` `:163-179`; per-delay cap `maxBackoff` 300 s default at `config_audit_governance.go:65` | `relay.go:163-179` — final `return min(max(jittered, initial/2), maximum)`; `:65` `MaxBackoffSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_BACKOFF_SECONDS", 300)` | ✅ **exact.** |
| E3 | "grep `MaxAttempts` → empty" — no attempt cap | `grep -rn MaxAttempts internal/auditgovernance/` → **empty** ✅. But `grep internal/config/` → **hits**: `config_event_outbox.go:20/32/63/99` (`EVENT_OUTBOX_MAX_ATTEMPTS`, default 10) — the **events outbox** subsystem, unrelated to audit governance | ✅ substantive claim (audit-governance relay has no attempt cap) holds. ⚠️ the spec's "grep `MaxAttempts` → empty" is overstated across `internal/config/`; the correct scoped claim is `internal/auditgovernance/` only. |
| E4 | `http.go:validateReceipt` `:178-206`; non-202 → `httpStatusError` `:180-182`; `ErrInvalidReceipt` `:186/:190/:194/:204`; `ErrReceiptConflict` `:201`; `receiptMatches` `:214-221`; 401 token invalidation `:126-127` | all exact: `:180-182` (non-202 → `&httpStatusError{Status}`), `:186` media-type, `:190` body-size/read, `:194` JSON unmarshal, `:201` conflict, `:204` `receiptMatches` false; `receiptMatches` `:214-221` requires `EventID==fact.ID` `:216`, `TenantID==fact.TenantID` `:216`, non-zero `AcceptedAt` `:217`, status ∈ {ledgered, indexed, archived} `:220`; `Publish` 401 → `binding.tokens.Invalidate(token)` at `:126-127` | ✅ **exact.** |
| E5 | `model.go` sentinels `:26-27`; `httpStatusError` `:34-37` | `ErrInvalidEvent` `:25`, `ErrInvalidReceipt` `:26`, `ErrReceiptConflict` `:27`, `ErrTokenUnavailable` `:28`; `type httpStatusError struct` at **`:31`**, `Error()` `:35-37` | ✅ sentinels exact. ⚠️ `httpStatusError` is at `:31`/`:35-37`, not `:34-37` (cosmetic). |
| E6 | 0042 migration + `FailAuditGovernance` + `failed_at_ns=0` predicates + `OldestPendingAuditGovernance` + `CleanupFailedAuditGovernance` | `0042_audit_governance_terminal_failed.{up,down}.sql` present (`ADD COLUMN failed_at_ns INTEGER NOT NULL DEFAULT 0`); `FailAuditGovernance` `audit_governance_claim.go:159-172` (sets `failed_at_ns`, clears claim owner/token, zeroes lease, truncates `last_error` ≤512 B, fenced `WHERE ... failed_at_ns=0 AND claim_owner=$4 AND claim_token=$5 AND lease_expires_at_ns > $6`); `failed_at_ns=0` predicates at `:38/:62/:88/:146/:168/:195`; `OldestPendingAuditGovernance` `:188-201`; `CleanupFailedAuditGovernance` `audit_governance_cleanup.go:113` | ✅ **all exact.** |
| E7 | `TestRuntimeConflictingReceiptIsTerminalWithRetention` `runtime_test.go:117-186`; `runtimeConfig` `:39-46` (poll 10 ms, backoff 1 s→2 s) | test at `:117-186` (atomic `posts`; 202+`conflict:true` sink; posts==1 across 500 ms observe; `ClaimAuditGovernance` len-0; `OldestPendingAuditGovernance` not-pending; retention prune 0→1 at `:180-186`); `runtimeConfig` `:40-50` with `PollMilliseconds=10` at `:43`, `InitialBackoffSeconds,MaxBackoffSeconds=1,2` at `:45`, `DeliveredRetentionSeconds,CleanupIntervalSeconds=3600,60` at `:47`, `ClaimTTLSeconds=3` at `:44` | ✅ **exact** (spec cited `:39-46`; actual range `:40-50`, retention/cleanup at `:47` — same function). |
| E8 | `Ready()` `runtime.go:145-160`; `readyzHandler` 503 `cmd/server/http.go:66-67`; wiring `audit_governance.go:15/:51` | `Ready` `:145-160` (`OldestPendingAuditGovernance` `:153`, `time.Since(oldest) > r.maxLag` `:157`, maxLag 900 s `config_audit_governance.go:66`); `readyzHandler` `:51`, `http.Error(w, "runtime dependency unavailable", 503)` `:67`; `buildAuditGovernanceRuntime` `audit_governance.go:15`, `runtimeReadiness` `:51` | ✅ **exact.** |
| E9 | Proposal classifier contract at `docs/proposals/audit-contract-batch-aero-vault.md:8` | `:8` — "B3-1：`isPermanentDeliveryError` 分类函数替换 `deliverFact` 单哨兵（conflict/无效回执/409/422 → 终态；401/403/5xx 保持瞬态，cap 300s 已满足）" | ✅ **exact.** |

**R1 — NEW finding (not in spec, shapes §4): dead-letter is final *within the retention window* — no operator requeue surface, but one automatic horizon re-attempt.** While a failed row exists (`failed_at_ns > 0`), `reconcile()` never re-creates its fact: both gap queries require **no outbox row at all** — `listGovernanceAuditGaps` (`audit_governance_write.go:223` `LEFT JOIN ... o ... WHERE ... o.id IS NULL`) and `listGovernanceEventGaps` (`:252`, same shape) — and a terminal-failed row satisfies `o.id IS NOT NULL`, so it is not a gap. `grep -rn failed_at_ns internal/api/ cmd/server/ internal/cli/` → **empty** (no admin endpoint or CLI command can requeue a failed row). **However, dead-letter is not permanent:** `CleanupFailedAuditGovernance` (`audit_governance_cleanup.go:113`) prunes failed rows after the 7 d default retention *without an origin tombstone* (deliberate — comment at `:107-111`: "a failed row's origin was never ledgered, so a later mutation of the same origin may enqueue a fresh fact"); the origin rows in `audit_log`/`object_events` are never pruned (only bucket-rm deletes `object_events`, `sql_buckets.go:97`), so the pruned origin becomes a gap again and `reconcile()` (`relay.go:16`, runs every poll, 1 s default) re-enqueues it with a **fresh UUID** (`facts.go:22/:39`, `available_at_ns=now`, `attempts=0`) → **one automatic POST re-attempt per prune cycle (~7 d) while the binding is active**. Pre-existing behavior (0042 + reconcile predate this design), but it materially softens F1: "manual DB is the only recovery" holds only for *operator-instant* recovery; an unguided horizon re-attempt exists and is functionally equivalent to the §5 runbook UPDATE (which just preserves row identity/history). The events-outbox mirror is strictly less recoverable: `PruneEventOutbox`'s comment states "nothing ever re-enqueues from this table (unlike audit governance, there is no gap-scan)" (`internal/repository/event_outbox.go:390`). This raises the stakes on the closed-list decision (spec D2) and on AC-2 exercising every `ErrInvalidReceipt` producer — but does not change the scope.

**F-A1 — NEW finding (the one real defect): a transient 202 read-error is classified permanent.** `validateReceipt` (`http.go:188-190`) conflates two producers into one sentinel: `io.ReadAll` on a 202 response failing with a **transport error** (network reset mid-body, or the 5 s `HTTPTimeoutSeconds` default firing mid-body on a slow sink) is returned as `ErrInvalidReceipt` alongside the deterministic size/shape failures. Under D1 this becomes `failFact` → dead-letter; today's code self-heals (`ErrInvalidReceipt` → retry → idempotent re-POST → `duplicate:true` → complete — the receiver has *already ledgered*, since 202 is sent only after `wait_for=ledgered`). The false terminal row is a permanent bookkeeping lie until the prune, and the duplicate-completion path never runs. **Fix (2 lines, in-scope-adjacent):** split the combined condition — `if err != nil { return err }` (raw transport error → transient; `classifyRelayError` already labels it "audit governance transport failure", log-only) and keep `ErrInvalidReceipt` for `len(body) > maxResponseBytes` and shape failures. **Sequencing vs the sibling identity-first design:** both touch `validateReceipt` but are orthogonal — the split is at the body-read (`:188-190`), *before* the Conflict/`receiptMatches` order (`:196-204`) that identity-first reorders; whichever lands first, the other applies cleanly, and the classifier (D1/D2) and AC-2(a) depend on neither. Severity: LOW probability, MEDIUM impact (wrong terminal state; no governance data loss — the receiver holds the event).

**Cross-check against the sibling design** (`docs/requirements/auditgovernance-receipt-conflict-identity-v1.design.md`, *not yet implemented* — `http.go:198-204` still checks `Conflict` before `receiptMatches`): that direction reorders `validateReceipt` to identity-first. It touches the same function but is orthogonal — in both orders an identity-mismatched receipt lands on `ErrInvalidReceipt` → terminal under this classifier. **Sequencing note:** if the identity-first design merges first, AC-2(a) (below) still passes unchanged; the classifier and the F-A1 split do not depend on the order — F-A1 edits the body-read (`http.go:188-190`), identity-first the Conflict/`receiptMatches` order (`:196-204`), and both apply cleanly in either merge order.

**Post-change behavior:** 409/422/tenant-mismatch/malformed receipts now produce exactly-one-POST terminal-with-retention instead of bounded-backoff-forever; `ErrReceiptConflict` is byte-identical (pinned by the existing test, which must pass unmodified). Wedged rows stop occupying the claim cycle and stop driving `OldestPendingAuditGovernance` → the `/readyz` 503 bridge (`runtime.go:157` → `cmd/server/http.go:67`) no longer trips on permanently-rejected facts. Post-F-A1, a 202 body *read* error is no longer classified permanent: it retries (idempotent re-POST → `duplicate:true` → complete) exactly as today.

---

## 2. Design

### D1 — `isPermanentDeliveryError` classifier (`internal/auditgovernance/relay.go`, ~12 lines + 1 import)

Placed next to `classifyRelayError` (`:181-190`), per spec D1 (relay *policy*, not error model; unexported). Closed list, `errors.Is`/`errors.As`, no status ranges, no substring matching:

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

- `relay.go` gains `"net/http"` (for `StatusConflict`/`StatusUnprocessableEntity`); no other import churn (gofmt-stable).
- Ordering note: the `errors.Is` branch runs first; on today's paths an `*httpStatusError` never wraps a sentinel, so branch order is classification-equivalent — the comment states the closed list so future wrapping stays safe.
- `classifyRelayError` **unchanged** (still a log label only); the cross-reference comment in the classifier is the mandated REQ-3 drift guard. Alternative considered and rejected: have `classifyRelayError` delegate to the classifier — changes the log label enumeration (`ErrInvalidEvent`/`ErrTokenUnavailable` are currently labeled with their text; folding them into the classifier would only add dead branches). Keep the two enumerations, pin them together with the comment, AC-2, and the closed-list pin test (D3) — a future permanent-misclassification edit must fail a test.

### D2 — `deliverFact` branch swap (same file, ~6 changed lines)

```go
	if isPermanentDeliveryError(err) {
		// Terminal-with-retention: conflict receipt, invalid receipt
		// (tenant-mismatch / malformed / non-ledgered), or HTTP 409/422 —
		// the receiver will never ledger this event, so retrying is
		// bounded-backoff-forever. Fail the fact (never re-claimed) and keep
		// the row + last_error until the retention prune (7d default),
		// mirroring the events outbox 'failed' state. All other errors
		// (400/401/403/404/410/429/501/other 4xx-5xx, token-path, transport)
		// stay transient with bounded-backoff retry (rationale below).
		r.failFact(fact, err)
		return
	}
	if err != nil {
		r.retryFact(fact, err)
		return
	}
```

**Transient-by-deliberate-fallthrough — the boundary is a documented decision (R1 makes dead-letter irreversible inside the window):** 400 (with or without an RFC 7807 body — `validateReceipt` discards non-202 bodies at `http.go:181`, so classification is status-only by construction), 404, 410 Gone and 501 Not Implemented (RFC-permanent statuses, but they signal *deployment* breakage ops must fix, not facts to dead-letter), 429, and other 4xx-5xx all fall through to transient. Rationale: with no requeue surface (R1), retry-forever + the `/readyz` 503 bridge (`runtime.go:157` → `cmd/server/http.go:67`) is the safer failure mode — it keeps the operator signal alive instead of silently dropping governance data. **The token path is transient by design too:** `ErrTokenUnavailable` (8 producers, `token.go:66/81/99/104/108/123/128/133`) and the SDK's opaque `*ssoclient.TokenError` on token-endpoint **401/403** (bad client secret / revoked client — the most likely real-world permanent config error; verified not wrapped in any sentinel, so `errors.As` cannot misclassify it) classify transient. 401 self-heals (`Publish` invalidates the token at `http.go:126-127` → retry re-fetches); 403 does not self-heal, and `validTokenScopes` (`token.go:152-153`) accepts empty `scope` claims, so an empty-scope token 403s forever — retry-forever + readyz is the alert for that corner too. These are deliberate: spec D2's "401/403 transient" covers the *sink* path; the token path is called out here.

`failFact` (`:111-122`) and `retryFact` (`:124-137`) are **untouched** — no attempt counter, no config, no repo call changes (REQ-3). `ErrReceiptConflict` flows through `isPermanentDeliveryError` → `failFact` on the exact same code path as today (the classifier's first branch is the old `errors.Is`), so the existing `TestRuntimeConflictingReceiptIsTerminalWithRetention` must pass **unmodified** — that is the byte-identical pin.

### D3 — Tests: new file `internal/auditgovernance/runtime_classify_test.go` (same package, ~230 lines)

Spec REQ-4 names `runtime_test.go` (400 lines); spec §6 risk explicitly sanctions splitting helpers when the ≤500-line gate is at risk ("split helpers into a shared test file if needed — a test-side refactor, not a scope change"). Decision: **new sibling file in package `auditgovernance`**, reusing `runtimeConfig` (`runtime_test.go:40-50`), `WrapRepository` (`repository.go:15`), and the sink idiom (token endpoint + atomic `posts`). `runtime_test.go` stays at 400 lines untouched; the new file stays well under 500.

Shared helpers in the new file:

```go
// classifySink serves /token, counts fact POSTs, and records a per-POST
// timestamp (inter-POST gaps need timestamps, not just counts); per-case response.
func classifySink(t *testing.T, status int, body func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32)
```

Terminal assertion helper (mirrors the conflict test's skeleton `:117-186` verbatim, parameterized by response):

```go
func assertTerminal(t *testing.T, status int, respond func(w http.ResponseWriter, r *http.Request)) {
	// repo.Open(sqlite temp) + Migrate → store := repo.(Store) → New(runtimeConfig(server.URL))
	// → WrapRepository → RecordAudit{acme, tenant.status} → runtime.Start
	// → poll posts>=1 (deadline 3 s, 10 ms cadence) → observe ≥2.5 s (past max backoff) → Close
	// → posts==1, ClaimAuditGovernance len==0, OldestPendingAuditGovernance ok==false
}
```

Key facts the sink/timing design depends on (all verified):
- **AC-2(a) tenant-isolation:** `receiptMatches` checks `EventID` and `TenantID` in one `||` chain (`http.go:216-217`), and `validateReceipt` checks `Conflict` *before* `receiptMatches` (`:196-204`). To isolate the tenant branch, the sink must **capture the posted `event_id`** (POST body is `governanceEvent`, `json:"event_id"` = `fact.ID`, `model.go:61`) and echo it back with a wrong `tenant_id`; bodies must omit `conflict` (defaults false). A hardcoded `"event_id":"x"` (as the conflict test uses) would fail on EventID, not TenantID — still `ErrInvalidReceipt`, but the AC-2(a) claim "tenant-mismatch is terminal" would not be what's exercised.
- **AC-3 re-POST timing:** `runtimeConfig` gives initial backoff 1 s / max 2 s. `retryFact` → `RetryAuditGovernance` sets `available_at_ns = now + delay` **and immediately releases the lease** (`lease_expires_at_ns=0`, `audit_governance_claim.go:146-151`); the claim predicate needs `available_at_ns <= now AND lease_expires_at_ns <= now` (`:62-63`). So after `Close`, the row becomes claimable ~jittered(1 s..2 s) after the first POST — **no need to wait out the 3 s claim TTL**. Assert claimability by polling `ClaimAuditGovernance(ctx, "observer2", ...)` until `len >= 1` (deadline 5 s) rather than sleeping a fixed wall-clock.
- **AC-3 inter-POST gap:** for attempts ≥ 2 the delay caps at *exactly* `MaxBackoffSeconds` (2 s) in ~50 % of runs (symmetric ±25 % jitter on `boundedBackoff`, `relay.go:163-179`), so the observed gap on correct code is 2 s + poll/HTTP latency — assert `gap ≤ 3 s` (2 s cap + 1 s slack), never strict `≤ 2 s` (~50 % flake).
- **AC-3 401 case:** `Publish` invalidates the token on 401 (`http.go:126-127`) → the retry re-fetches from `/token`; the sink must keep serving `/token` for the whole test (existing harness does).
- **AC-4:** retention block is the verbatim `:180-186` pattern (`CleanupFailedAuditGovernance(ctx, now.Add(-time.Hour), 10) == 0`, then `now.Add(time.Hour) == 1`) — run against the 409 case.

### D4 — No other code moves

The single exception is the **F-A1 split** in `validateReceipt` (`internal/auditgovernance/http.go:188-190`, 2 lines — §1 F-A1), required for the classification to be *correct*: it changes error *values* on one path only and is sequenced independently of the sibling identity-first reorder (either merge order works). No `cmd/server` changes (REQ-3: `readyzHandler`/`runtimeReadiness` are already correct once terminal rows stop being re-claimed; the 503 flip itself is B3-2, separate direction). No config surface, no `go.mod` change (I6: stdlib `net/http` only).

---

## 3. API changes & compatibility constraints

| Surface | Change |
|---|---|
| Wire protocol to the audit sink (POST body, headers, token flow) | **None.** Classification is purely client-side inside `deliverFact`. |
| `Runtime` public API (`New`, `Start`, `Close`, `Ready`) | **None.** |
| Repository interface / SQL schema | **None.** `failed_at_ns` (0042), `FailAuditGovernance`, claim/lag exclusion, `CleanupFailedAuditGovernance` already shipped. |
| Config / env | **None** (no `MaxAttempts`, no new knob — spec D3). |
| Logging | `failFact` logs at Error level with `attempt` + full cause (existing); 409/422/`ErrInvalidReceipt` failures now emit this line where previously only warn-level `retryFact` lines appeared — **observable log-shape change**, intended. `classifyRelayError` labels unchanged. F-A1: a 202 read-error stays on the `retryFact` warn line (no false terminal Error line). |
| Behavior change (the point) | 409/422/tenant-mismatch/malformed receipt: exactly 1 POST then terminal-with-retention, instead of retry-forever. `ErrReceiptConflict`: **byte-identical** (pinned by unmodified existing test). F-A1: 202 read-error → transient retry (idempotent re-POST → `duplicate:true` → complete) instead of false terminal. |
| Receiver contract | Unchanged — the classification mirrors the receiver's own acceptance predicate (`receiptMatches`), so no receiver change is implied. |

**Backward-compat hazards to hold:** (1) the existing conflict test must pass unmodified — it is the regression pin; (2) `runtime_test.go` line count stays 400 (new tests in the sibling file); (3) `relay.go` 191 → ~204 lines (≤500 ✓); (4) no new dependencies.

---

## 4. Failure modes

| # | Mode | Consequence | Mitigation |
|---|---|---|---|
| F1 | **Transient error misclassified permanent** (a sink that *temporarily* 409s/422s, e.g. eventual-consistency hiccup, dead-letters a recoverable fact) | Delivery suspended until one of: (a) **operator-instant recovery** — §5 runbook UPDATE (preserves row identity/history); (b) **automatic horizon re-attempt** — the 7 d prune (no tombstone, `audit_governance_cleanup.go:107-113`) turns the origin back into a gap and `reconcile()` re-enqueues with a fresh UUID (`facts.go:22/:39`) → one POST per prune cycle while the binding is active (R1: final only within the retention window); (c) re-running the source operation. No admin/CLI requeue surface (`grep failed_at_ns internal/api/ cmd/server/ internal/cli/` → empty). | Permanent list is exactly the four direction classes (spec D2 — 400/404/410/429/501 and token-path errors stay transient by deliberate fallthrough, rationale in D2); AC-3 pins 401/403/500 transient and the closed-list pin test pins the boundary both directions; `last_error` (≤512 B, full cause) retained until the 7 d prune (`config_audit_governance.go:68`) for diagnosis; §5 runbook + R1 horizon documented so operators know both recovery paths. |
| F2 | **Permanent error misclassified transient** (a future permanent sentinel added without updating the classifier) | Back to retry-forever + `/readyz` 503 amplification (the original bug). | Closed-list by construction (anything unmatched is transient — safe direction); cross-reference comment (D1) + AC-2 exercises every `ErrInvalidReceipt` producer (`http.go:186/190/194/204`; the read-error branch is transient post-F-A1) + **closed-list pin test asserts the permanent set exactly, both directions** — a future permanent-misclassification edit fails it. |
| F3 | **Timing flake on loaded CI** | AC-1/AC-3 flaky. | Existing harness pattern already proven at `runtime_test.go:117-186`: atomic counters, poll-until-first-POST (3 s deadline), observe **past max backoff (≥2.5 s after first POST** — a misclassified-transient row re-POSTs at T0+[0.75,1.25] s, so a 500 ms observe window would miss it; 250× at 10 ms cadence), no wall-clock equality (only `==`/`>=`/`<=`), tiny configured backoff (1 s/2 s) making observe windows ≥5× the poll cycle; AC-3 claimability uses poll-until rather than fixed sleep and inter-POST gaps assert ≤3 s (2 s cap + 1 s slack — strict ≤2 s flakes ~50 % of runs when the jittered delay caps at exactly 2 s). |
| F4 | **Classifier drift from `classifyRelayError`** (two enumerations of the sentinels) | Log labels diverge from classification. | D1 cross-reference comment; AC-2 covers the sentinel side, AC-3 the status side; **closed-list pin test asserts closedness** (anything not listed is transient) — the property no positive-only test can pin; `classifyRelayError` is log-only (no behavior). |
| F5 | **Line-count gate** (`runtime_test.go` ≤500) | `make check` failure. | New tests in `runtime_classify_test.go` (spec §6 sanctioned split); `runtime_test.go` untouched at 400; new file ~230. |
| F6 | **Conflict with sibling identity-first design** (touches `validateReceipt` order) | Both change the same function; merge-order risk. | Orthogonal: identity-mismatch → `ErrInvalidReceipt` → terminal in both orders; AC-2(a) passes either way. Sequencing note in §1. |
| F7 | **202 read-error falsely terminal (F-A1)** — transport error mid-body on a 202 (network reset / 5 s HTTP timeout) misclassified `ErrInvalidReceipt` → dead-letter of an already-ledgered fact | Permanent bookkeeping lie until the prune; duplicate-completion path never runs. | 2-line split in `validateReceipt` (`http.go:188-190`): raw read error returned unwrapped (transient, `classifyRelayError` → "audit governance transport failure"); `ErrInvalidReceipt` kept for size/shape only. Sequenced with the sibling identity-first design (orthogonal — different lines). AC-2 covers the size branch; the read-error branch is not simulatable with a plain `httptest` sink (a hijacked-conn sink would be needed) and is excluded by design. |

---

## 5. Migration & rollback

**No data migration, no schema change, no config migration.** The repository layer (0042 `failed_at_ns`) shipped in the previous campaign and is not part of this change (spec E6/REQ-3).

**Deploy:** binary-only; rolling restart of `cmd/server` is safe (the classifier is stateless; a mixed fleet briefly classifies differently, but the outcome converges — permanent rows land terminal, transient rows retry, on whichever instance claims them).

**Rollback:** revert the single commit; old binary resumes retry-forever for 409/422/`ErrInvalidReceipt` (pre-change behavior). Rows already marked `failed_at_ns` by the new binary remain terminal after rollback until the retention prune (7 d default) — they are **not** resurrected by the old binary (claim predicates exclude `failed_at_ns != 0`). If rollback must resurrect wrongly-dead-lettered rows (F1) *now* (the automatic alternative is waiting for the prune+gap-scan horizon, R1), run the guarded UPDATE below — flag in the rollback runbook; there is no automated requeue surface.

```sql
-- Resurrect terminal-failed rows. Guarded: only terminal rows are touched;
-- failed rows are single-writer (nothing in the runtime writes failed_at_ns>0
-- rows except the prune's DELETE), so this is race-free against the runtime.
-- Expectation: RowsAffected == 1 per target row (0 = already pruned or already
-- resurrected by the R1 horizon — verify with changes() on SQLite / rowcount
-- on PG, or scope by id: AND id='<fact_id>'). >1 = unintended batch — abort.
UPDATE audit_governance_outbox
SET failed_at_ns = 0            -- available_at_ns is already in the past on
WHERE failed_at_ns > 0          -- failed rows (claimable at claim time, never
  AND delivered_at_ns = 0;      -- advanced between claim and fail); if you set
                                -- it anyway, use the DB clock:
                                --   SQLite: strftime('%s','now')*1000000000
                                --   PG:     (EXTRACT(EPOCH FROM now())*1e9)::bigint
```

**Runbook notes (folded from the database-ops review):** ① **binding-presence check first** — claim and lag queries inner-join `audit_governance_bindings` (`audit_governance_claim.go:61` `JOIN ... b.revision=$3`, `OldestPendingAuditGovernance` `:188-201`); confirm `SELECT 1 FROM audit_governance_bindings WHERE tenant_id='<tenant>'` returns a row, else the resurrected row is unclaimable, invisible to the lag scan, and unpruneable — orphan leak (pre-existing for any pending row of a dropped tenant; resurrection doesn't create it). ② `last_error`/`attempts` are untouched by the UPDATE — harmless (claim never reads `last_error`; no attempt cap; the next terminal write overwrites both). ③ **re-failure restarts the 7 d clock** (fresh `failed_at_ns`). ④ The UPDATE and "do nothing" are functionally equivalent recovery paths at the prune+gap-scan horizon — the UPDATE just preserves row identity/history; if the sink has healed and the binding is active, doing nothing also re-delivers within ~7 d. ⑤ `available_at_ns=<now>` is **redundant** on failed rows (always already in the past) and a footgun: it must be UnixNano-scale and ideally the DB's clock — a future-dated literal delays claimability (PG errors on a string literal against `bigint`).

**Migration steps (runbook):**
1. `git checkout` + `make check` green on this design's implementation commit.
2. Deploy binary; observe `/readyz` stays 200 and `readyzHandler` no longer trips on sink rejections.
3. Monitor `audit_governance` Error-level logs for new terminal failures (F1 watch): a burst of 409/422 terminal lines on a healthy sink indicates receiver-side contract mismatch — investigate before the 7 d prune horizon.
4. No SQL, no env, no config steps.

---

## 6. Testable acceptance mapping (AC → REQ → test → assertion anchors → gate)

All tests in `internal/auditgovernance/runtime_classify_test.go`, SQLite + `httptest` only (zero network, CI baseline `make check`: gofmt/build/vet/test). Harness = `runtimeConfig(server.URL)` (poll 10 ms, backoff 1 s→2 s, TTL 3 s, retention 3600 s, cleanup 60 s) + `WrapRepository` + `RecordAudit{acme, tenant.status}`.

| AC (spec §5) | REQ | Test | Assertion anchors (all verified against store SQL) |
|---|---|---|---|
| **AC-1** 409/422 terminal | REQ-4.1 | `TestRuntimeHTTPStatusesTerminal` — table over 409, 422; sink writes status only (no body) → `&httpStatusError` (`http.go:182`) | `posts==1` after poll-first-POST (3 s) + observe **past max backoff (≥2.5 s after first POST** — a misclassified-transient row re-POSTs at T0+[0.75,1.25] s, so the observe window must exceed it for all three anchors to discriminate) + `Close`; `ClaimAuditGovernance("observer","token",1,10,time.Minute)` → `len==0` (only `FailAuditGovernance` `:159-172` writes `failed_at_ns>0`; note that *within* the backoff window a non-terminal row is not claimable either — `available_at_ns` is in the future — so this anchor only discriminates past max backoff, which the observe window guarantees); `OldestPendingAuditGovernance` → `ok==false` (`:188-201` excludes `failed_at_ns!=0`; also discriminates inside the window — its SQL has no `available_at_ns` filter); inter-POST gap n/a (no second POST). |
| **AC-2** tenant-mismatch / malformed / non-ledgered terminal | REQ-4.2 | `TestRuntimeInvalidReceiptsTerminal` — table: (a) sink captures posted `event_id` (JSON `model.go:61`) and echoes it with `tenant_id:"other"` + `status:"ledgered"` + `accepted_at` set → `receiptMatches` false on tenant (`http.go:216-217`); (b) `status:"rejected"` → `:220` false; (c) body `not-json` → `:194`; (d) `Content-Type: text/plain` → `:186`; (e) 202 body > 64 KiB → `:190` size branch (the read-error branch is transient post-F-A1 and not simulatable with a plain `httptest` sink — excluded by design); all other 202 responses with `Content-Type: application/json`, `conflict` omitted | identical anchors to AC-1 (`posts==1`, claim len 0, not-pending, observe ≥2.5 s). "≤1 attempt" = exactly-1-POST-and-never-again (first POST is the single attempt; terminal entered from its response). Every `ErrInvalidReceipt` producer (`:186/:190/:194/:204`) covered — the original plan only exercised `:194`/`:204`. |
| **AC-3** 401/403/5xx transient | REQ-4.3 | `TestRuntimeTransientStatusesRetry` — table over 401, 403, 500; sink serves `/token` throughout (401 invalidates token, `http.go:126-127`); sink records a per-POST timestamp (a counter-only sink cannot compute per-gap intervals) | `posts>=2` within 6 s (re-POST happens — `retryFact` path); each inter-POST gap **≤ 3 s = `runtimeConfig.MaxBackoffSeconds` (2 s, `runtime_test.go:45`) + 1 s slack** — strict ≤2 s flakes ~50 % of runs (for attempts ≥ 2 the jittered delay caps at *exactly* 2 s in ~50 % of runs, so the observed gap = 2 s + poll/HTTP latency > 2 s on correct code); proves the 300 s default boundary (`config_audit_governance.go:65`) is not over-tightened; after `Close`, poll `ClaimAuditGovernance("observer2",...)` until `len>=1` (deadline 5 s) — row remains claimable, `failed_at_ns==0` (`RetryAuditGovernance` releases lease immediately, `:146-151`; claim predicate `:62-63`). |
| **AC-4** retention prune of 409-failed rows | REQ-4.4 | `TestRuntimeHTTP409TerminalWithRetention` — AC-1 409 case + verbatim `:180-186` block | `CleanupFailedAuditGovernance(now-1h, 10)` → `n==0` (survives before window); `(now+1h, 10)` → `n==1` (pruned after) — 409 shares conflict's terminal-with-retention semantics, not hard-delete/never-prune. |
| **Pin** conflict byte-identical | REQ-2 | existing `TestRuntimeConflictingReceiptIsTerminalWithRetention` (`runtime_test.go:117-186`) | **must pass unmodified** — same `deliverFact` path (`isPermanentDeliveryError` first branch ≡ old `errors.Is`). |
| **Pin** closed-list boundary | REQ-2/REQ-3 | `TestPermanentDeliveryErrorClosedList` (~25 lines, new) — table asserting **both directions**: permanent = {`ErrReceiptConflict`, `ErrInvalidReceipt`, `&httpStatusError{409}`, `&httpStatusError{422}`}; transient = {`ErrInvalidEvent`, `ErrInvalidConfig`, `ErrTokenUnavailable`, `&httpStatusError{400}`, `&httpStatusError{401}`, `&httpStatusError{403}`, `&httpStatusError{410}`, `&httpStatusError{429}`, `&httpStatusError{500}`, `&httpStatusError{501}`, raw transport error, `context.Canceled`}; optionally assert `classifyRelayError` returns each sentinel's own text (no silent "audit governance transport failure" fallthrough) | closedness ("anything not listed is transient") pinned in both directions — the exact property positive-only AC-2/AC-3 cannot assert; a future edit adding a permanent sentinel without updating the classifier now fails a test (F2). |

**Gate:** `make check` (gofmt, `go build ./...`, `go vet ./...`, `go test ./...`). Line-count: `relay.go` ~204, `runtime_test.go` 400 (untouched), `runtime_classify_test.go` ~230 (incl. the ~25-line closed-list pin test) — all ≤500.

---

## 7. Files changed (complete list)

| File | Change |
|---|---|
| `internal/auditgovernance/relay.go` | +`isPermanentDeliveryError` (~12 lines, adjacent to `classifyRelayError` `:181`); `deliverFact` branch swap (`:84` → `isPermanentDeliveryError(err)`); +`net/http` import; branch comment documents the four terminal classes |
| `internal/auditgovernance/http.go` | **F-A1 split** (2 lines in `validateReceipt`, `:188-190`): raw `io.ReadAll` error returned unwrapped (transient); `ErrInvalidReceipt` kept for `len(body) > maxResponseBytes` + shape failures — sequenced with the sibling identity-first design (orthogonal, different lines, either merge order) |
| `internal/auditgovernance/runtime_classify_test.go` | **new** — AC-1..AC-4 tests + `TestPermanentDeliveryErrorClosedList` (~25 lines) + `classifySink` (per-case response + per-POST timestamps)/`assertTerminal` helpers (reuses `runtimeConfig`, `WrapRepository` from existing files) |
| `internal/auditgovernance/runtime_test.go` | **unchanged** (pin) |
| `docs/requirements/cmd-server-audit-governance-permanent-error-classification-v1.design.md` | this file |

Not touched (explicit non-goals): `cmd/server/*` (B3-2), `model.go` (spec D1), `http.go` *other than the 2-line F-A1 split* (the sibling identity-first reorder is a separate design), repository/migrations (already shipped), config, `go.mod` (I6), `runtime_test.go` (pin).
