# Failure-mode stress — terminal-classification design (F1–F10 + unlisted modes)

**Design stressed:** `docs/requirements/internal-auditgovernance-terminal-classification-v1.design.md` (§6 F1–F10, §7 migration steps, §8 D1–D5)
**Sibling stressed:** `docs/requirements/internal-ai-audit-governance-permanent-error-terminal-classification-v1.design.md` (+ its `merge-plan.md`)
**Basis:** live worktree (uncommitted baseline: staged 0042, untracked 0043 + tests), re-verified symbol-by-symbol. Package tests re-run green: `go test ./internal/auditgovernance/ ./internal/repository/ -run 'Terminal|Permanent|Backoff|FailedFact|PendingIndex|Deviation|ConflictFail|Transient|RePosted' -count=1` → `ok` ×2.
**Method:** every F-row's claim was re-derived from code, not from the design's table.

---

## 0. Verdict summary

| # | Failure mode | Verdict | Note |
|---|---|---|---|
| F1 | fail-write-loss → lease-reclaim re-POST | ✅ **holds** (recovery correct) — **1 doc error**: counter claim inverted | §1 |
| F2 | retry-write-loss | ✅ holds (same lease-reclaim mechanism) | §1 |
| F3 | cleanup failure | ✅ holds (warn + `nextCleanup` reschedule; batch-boundary re-poll) | — |
| F4 | receiver 500 forever | ✅ holds (bounded retry ≤300 s, no dead-letter, D3) | — |
| F5 | 409/422 after successful delivery | ✅ holds (terminal + retained 7 d + pruned; receiver-bug surface) | §4 |
| F6 | backoff overflow | ✅ holds (`delay < maximum` loop bound + `min(max(...), maximum)` clamp) | — |
| F7 | clock skew | ⚠️ **partially scoped** — prune-cutoff only; lease/`available_at_ns` skew interaction unlisted; terminality itself skew-immune | §3 |
| F8 | misclassification pin | ✅ holds for unilateral edits — **does not** hold for sibling edits (see §5) | §5 |
| F9 | replica/claim fencing | ✅ holds (owner+token+live-lease on all writes; steal-by-expiry by design; SQLite tx fence / PG `FOR UPDATE SKIP LOCKED`) | §2 |
| F10 | ack-lost → idempotent duplicate re-POST | ✅ holds — contract assumption, not enforcement (see §4) | §4 |
| — | **Unlisted:** partial migration apply, concurrent-migration race, mixed-version rollout (old binary re-claims terminal rows), schema-only rollback stall, fail-write metric overcount | ⚠️ **three are real and unaddressed**; two are provably safe | §6 |

**Headline:** the design's F-table is substantively correct — every terminal-semantics invariant (value-based `failed_at_ns` exclusion, fencing, idempotent receipt, transactional fail-stop migrations) verifies against code. The stress found **4 factual/doc defects** (F1 counter inversion, §7.2 migration-locking phantom, §7.2 idempotency overclaim, F7 under-scope), **1 real unlisted hazard** (mixed-version rollout re-claims terminal rows), and a **governance gap in the coordination constraint**: the two designs *can* land independently with CI fully green, and the merge plan's resolution never inventories design A.

---

## 1. F1 — fail-write-loss → lease-reclaim re-POST (holds, with one doc error)

**Mechanism verified in code** (`internal/repository/audit_governance_claim.go`):
- Claim predicate requires only `delivered_at_ns=0 AND failed_at_ns=0 AND available_at_ns<=now AND lease_expires_at_ns<=now` — **no token match** for re-claim (steal-by-expiry is the design). A failed row whose `FailAuditGovernance` write was lost still has `failed_at_ns=0` and a live lease → after `ClaimTTLSeconds` any worker re-claims → re-POST → re-classify → re-fail. Recovery is automatic and correct; termination lands on the first claim whose fail write succeeds. "≤1 attempt violated in this edge" is accurately acknowledged.
- **Provable non-timing edge:** lease-expiry-before-fail-write cannot happen on the single-process path — config validation `ClaimTTLSeconds > 2*HTTPTimeoutSeconds` (`internal/config/config_audit_governance.go:234`) bounds `Publish` (≤1 timeout) + fail write (≤1 timeout) strictly inside the lease. F1's "claim lost" trigger is therefore store-error-only, not a slow-receiver race. This deserves one line in the design (it's currently implicit).
- Re-claim cadence during a sustained DB-down window is `claimTTL` (30 s default), not backoff — bounded rate, no livelock; `attempts` grows harmlessly (no attempt cap by D3).

**❌ Doc error (F1 detection column):** "warn log + `relay_dead_total` **not** incremented" is **false**. `IncAuditGovernanceRelayDead` fires at `internal/auditgovernance/relay.go:121`, the *first* statement of `failFact`, **before** the `FailAuditGovernance` call (`:123-126`). The counter increments unconditionally on the terminal-classification path, even when the fail write fails. Consequences: (a) `audit_governance_relay_dead_total` measures *classifications attempted*, not *terminal rows* — it overcounts during DB-write failure; (b) the sibling's §4.1 alert `sum(rate(audit_governance_relay_dead_total[15m])) > 0` fires on sustained write failure with the misleading annotation "facts reached terminal failed state". The metric semantic ("attempted", monotonic, log-independent) is defensible and should be *documented*, not silently contradicted by the F1 row.

## 2. F9 — replica/claim fencing (holds)

- `CompleteAuditGovernance` / `RetryAuditGovernance` / `FailAuditGovernance` all fence on exact `(claim_owner, claim_token)` **and** `lease_expires_at_ns > now` (`audit_governance_claim.go:159-207`); a stale or foreign owner's write hits 0 rows → `requireGovernanceClaim` → "claim lost" → warn. No split-brain write is possible.
- Claim races: PG `FOR UPDATE OF o SKIP LOCKED` (single-winner), SQLite per-row fenced UPDATE inside one tx (single-writer). Only one owner ever holds a row.
- Restart safety: `owner` is a fresh UUID per process; a dead replica's leases expire and rows are stolen — fencing not violated.
- Cross-replica skew side effect (see §3): a fast-clock replica re-claims early → duplicate POSTs, absorbed by the idempotent receipt (§4). F9 frames fencing only; the skew trigger is unlisted.

## 3. F7 — clock skew (terminality is skew-immune; scope under-covers)

**The strong property holds:** terminality is **value-based** — claim/retry/fail/lag/drain-pending all predicate on `failed_at_ns = 0`, and `FailAuditGovernance` writes `failed_at_ns = now` with the failing worker's clock. No amount of skew between workers can resurrect a terminal row (a row with `available_at_ns` in the future *and* `failed_at_ns>0` is excluded — the columns are independent, failed wins). The 0043 partial-index predicates are the same value-equality → skew-immune.

**F7 as written covers only the prune cutoff** (`now - retention` vs `failed_at_ns` → early/late prune, same exposure as the delivered path). Two skew interactions are unlisted:

1. **Lease/`available_at_ns` skew** — the claim predicate `available_at_ns <= now AND lease_expires_at_ns <= now` is evaluated with the *claiming* worker's clock. A fast-clock worker re-claims rows scheduled by a slow-clock worker early → (a) duplicate POSTs (idempotency-absorbed), (b) transient retry gaps shorter than the designed backoff — **the AC-2.4 gap-growth guarantee is single-replica-only** and should say so. A slow-clock worker delays retries (safe direction).
2. **Backward-skew fail write** — `failed_at_ns = worker_now − Δ`; if Δ > retention, the next cleanup tick prunes the row immediately → terminal-with-retention degrades to terminal-without-retention (diagnostics lost early). Same class as (1); one line suffices.

## 4. F10 / F5 — idempotent duplicate re-POST (holds; contract assumption, not enforcement)

- `receiptMatches` (`internal/auditgovernance/http.go:214-225`) **deliberately ignores `duplicate`** — `{duplicate:true, conflict:false, status:ledgered}` + matching `event_id`/`tenant_id` + nonzero `accepted_at` completes exactly like a first POST. Pinned both directions by `http_test.go:192-252` ("duplicate toggle changed acceptance" for every accepted status). Ack-lost → lease expiry → re-claim → re-POST → duplicate receipt → `CompleteAuditGovernance`. Fully verified.
- **Contract assumption, not enforcement:** if the receiver answers a duplicate with `conflict:true` (some dedup implementations return 409) it becomes `ErrReceiptConflict` → terminal fail of an *already-delivered* row (F5's receiver-bug class; no data loss — the event was ledgered; row retained 7 d then pruned). If the receiver does not echo `accepted_at` / a ledgered status on duplicates → `ErrInvalidReceipt` → same misclassification. F10's "receiver answers idempotently (contract A)" is the load-bearing assumption and is currently only a comment in `receiptMatches` — worth making explicit in the design's failure table (it is F10's recovery precondition).

## 5. Coordination constraint — both designs cannot land independently *without a coordinated pin update*: **verified, but with two sharp edges**

**The constraint is real.** Exactly one mutable artifact decides membership: `isPermanentDeliveryError` (`relay.go:212-221`) + exactly one pin function `TestIsPermanentDeliveryErrorClosedList` (`relay_terminal_test.go:199-230`, `ErrInvalidEvent` in the transient slice at `:221`). Both designs share both symbols; the final tree has exactly one membership. A's C1/D2 and B's D-A/D-B both record the requirement for a coordinated pin update. **Verified true.**

**Sharp edge 1 — the two designs CAN land independently, with CI green, in either order.** A's delta is test-only (one new function appended to `relay_terminal_test.go`); B's delta edits the classifier and the pin slices in the same file. Different hunks → clean merge, no conflict. And crucially: **B's flip overwrites the very pin that implements A's AC-1.1/AC-2.1, so the union of both acceptance suites passes after either ordering.** A's spec claims ("`ErrInvalidEvent` stays transient", AC-2.1 `ErrInvalidEvent → false`, D2) become false against the tree with **no failing test anywhere** — the loser is falsified silently, not loudly. The sibling's "whichever lands second fails" claim holds only against the *cmd-server* sibling's differently-named pin (`TestPermanentDeliveryErrorClosedList` — a real CI-red, because both pins compile and contradict); it does **not** hold against A, which shares the function name and gets overwritten rather than contradicted.

**Sharp edge 2 — the resolution document doesn't inventory design A.** `grep internal-auditgovernance-terminal-classification` across B's design *and* its merge plan → **0 hits**. The merge plan's audited inventory (cmd-server · internal-access · relay-metrics · ready-degraded · internal-ai) omits A entirely, and its D-A ruling ("the two permanent-error siblings' transient pins are amended, not just outvoted") never engages A's §8 D2 ("not adopted by this spec"). Two documents therefore record the same coordination with **opposite expected outcomes**: A says the flip is not adopted; the merge plan says the flip wins and the transient pins are amended. The coordination is documented on both sides, but **the outcome is unresolved between the documents themselves** — the very thing the constraint exists to prevent.

**What a mechanical enforcement would need (neither design ships it):** the pin is self-referential (it asserts `isPermanentDeliveryError`'s behavior), so any sibling that edits both classifier and pin in one commit is invisible to CI. Enforcement requires an *immutable* membership assertion — e.g., a test asserting the transient slice's exact member list (including `ErrInvalidEvent` by name) independent of the classifier, or a doc-pinned comment test — that fails on B's flip unless the flip also amends it *in the same coordinated commit*. As designed, the "coordinated closed-list pin update" is a human gate only; that is worth stating explicitly in A's C1.

## 6. Unlisted failure modes

### 6.1 Partial migration apply — **provably safe, but design's claims wrong in two places**
- Each migration is applied inside a **single transaction** (`applyMigration`, `internal/repository/sql.go:112-133` — DDL tx + `schema_migrations` insert commit together), and **Migrate failure is fail-stop at startup** (`cmd/server/main.go:189-191` returns err → server never serves). A 0042-committed + 0043-failed state is never served: startup aborts, next boot applies only 0043 (no `schema_migrations` row for it). Version-serial ordering ("0043 requires 0042") verified (`listMigrationFiles` sorts `0042 < 0043`). **No partial-schema serve is possible.** This is the design's strongest unstated property — worth one line in §7.2.
- ❌ **§7.2 error 1:** "concurrent startup races are handled by the existing migration locking (unchanged)" — **there is no migration locking**: no `BEGIN IMMEDIATE`, no advisory lock, no busy-timeout pragma anywhere in `internal/repository/` (grep-verified; SQLite opens with only `PRAGMA foreign_keys=ON; journal_mode=WAL`, `sqlite.go:31`). Two replicas starting simultaneously → one fails (duplicate column / duplicate index) → fail-stop → restart retries. Error-not-corruption, but the claim "handled by locking" is a phantom; the actual protection is fail-stop + retry.
- ❌ **§7.2 error 2:** "0043 index DDL is idempotent under `IF NOT EXISTS`-equivalent semantics only if the runner serializes — it does" — the runner provides **no** `IF NOT EXISTS` semantics (`CREATE INDEX` plain in both dialects) and, per above, no serialization. **Replay-safety applies to the down files only** (`DROP INDEX IF EXISTS`); a manual 0043.up run outside the runner bricks every subsequent startup until the stray index is dropped or a `schema_migrations` row is inserted. Fix the sentence to: "up is not replay-safe; down is."

### 6.2 Replica lag / mixed-version rollout during 0043 — **one real unlisted hazard**
- PG streaming-replica lag: **irrelevant** — the store holds a single DB handle (`sqlStore.db`), no read-replica path exists; the app never reads from a lagging standby. Migrations run on the primary before serving.
- **Real hazard — old binary during rolling deploy:** a pre-0042 replica's claim/retry SQL lacks the `failed_at_ns` predicates, so it can re-claim and re-POST rows the new binary has terminal-failed (claim: `lease_expires_at_ns <= now` only; retry: no `failed_at_ns=0` fence). The old binary (no classifier at HEAD — every error → `retryFact`) treats the receiver's 409/422 as transient, re-POSTs on bounded-backoff cadence, and its `RetryAuditGovernance` **overwrites `last_error`** on the terminal row. The row stays `failed_at_ns>0` (new binary never re-claims it) and is still pruned at the retention cutoff by the new binary's cleanup (prune has no claim fencing, but claimed rows always have `failed_at_ns=0` — the fail write clears the claim in the same UPDATE — so no claimed row is ever pruned; verified). Net: duplicate POSTs of permanently-failed facts + "≤1 attempt" violated + diagnostics overwritten **for the deploy window only**, absorbed by receiver idempotency, bounded by deploy duration and the 7 d prune. **Unlisted in §7.2** (which covers migration serialization and rollback, not mixed-binary claim semantics). One-line fix: "roll all replicas within one window, or accept the bounded duplicate-POST window during mixed-version serving."
- Reverse direction (new binary + old schema) is impossible: Migrate runs before serving and is fail-stop.

### 6.3 Schema-only rollback with the new binary — **unlisted, silent stall**
§7.2's "post-rollback behavior: terminal facts become claimable again → re-POSTed under the old transient path" is true **only if the binary is rolled back in lockstep**. After `0042.down` with a still-running new binary, every claim/retry/fail SQL references `failed_at_ns` → SQL error → `deliverBatch` warns and skips → **silent delivery stall** (no crash, no error page, `readyz` may stay healthy since `OldestPendingAuditGovernance` errors → `Ready()` returns "lookup failed" — actually gated, but no loud signal at claim time). One line: rollback is binary+schema in lockstep; schema-only rollback stalls the relay with warn-only logging.

### 6.4 Already-covered modes (verified, no action)
- F2/F3 (retry-write loss, cleanup failure) — mechanisms verified; F3's `count==batch → nextCleanup=pollEvery` keeps batch progress.
- F6 (backoff overflow) — `for ... delay < maximum` loop bound + `min(max(jittered, initial/2), maximum)` clamp; overflow impossible.
- F5 (409 after delivered) — receiver-bug class; row was ledgered (no data loss), retained, pruned; correct as designed.
- AC-2.4 determinism — re-derived: harness `runtime_test.go:40-47` (initial 1 s → max 2 s, ±25 % per-ID jitter) ⇒ gap₁ ∈ [0.75, 1.25] s < gap₂ ∈ [1.5, 2.0] s (clamped by `min(...,maximum)`); the 5 s observe window anchors at POST#1 (pollUntil returns immediately after posts≥1, 10 ms cadence) ⇒ POST#3 at ≤3.25 s inside the window ⇒ `len(gaps)≥2` guaranteed. Test passes (re-run green). The only multi-replica caveat is §3(1) (skew degrades the gap guarantee to best-effort — single-replica claim should be stated).

---

## 7. Required design-doc corrections (consolidated)

| # | Doc | Current text | Correction |
|---|---|---|---|
| 1 | §6 F1 | "warn log + `relay_dead_total` **not** incremented" | Counter **is** incremented (relay.go:121, before the write); semantic = classifications attempted, overcounts on fail-write-loss; sibling alert inherits the inflation |
| 2 | §7.2 | "handled by the existing migration locking" | No locking exists; protection = per-migration tx + fail-stop startup + restart retry |
| 3 | §7.2 | "idempotent under IF NOT EXISTS-equivalent semantics … the runner serializes — it does" | Up is **not** replay-safe (plain `CREATE INDEX`); only down is (`DROP INDEX IF EXISTS`); no serialization exists |
| 4 | §6 F7 | prune-cutoff only | Add: lease/`available_at_ns` skew → early re-claim + duplicate POSTs + shortened retry gaps (AC-2.4 is single-replica-deterministic); backward-skew fail write → early prune |
| 5 | §6 F10 | "receiver answers idempotently (contract A)" | State explicitly this is a contract assumption; a conflict/non-ledgered duplicate reply misclassifies a delivered row terminal (F5 class) |
| 6 | §7.2 | multi-replica section | Add: mixed-version window — pre-0042 replica re-claims/re-POSTs terminal rows and overwrites `last_error`; roll replicas in one window |
| 7 | §7.2 | post-rollback semantics | Add: rollback is binary+schema in lockstep; schema-only rollback under the new binary = warn-only delivery stall |
| 8 | §5 C1 | "must not both land" | Add the sharp edges: independent landing is CI-green (pin overwrite, not contradiction — the "second landing fails" claim holds only vs the cmd-server pin); the merge plan's resolution does not inventory this design; a mechanically enforced pin would need an immutable membership assertion, not the self-referential classifier test |

**Bottom line:** the design's F-table survives code-level stress — all ten listed modes behave as documented except the F1 counter row (wrong) and F7 (under-scoped); no listed mode breaks terminal semantics. The two genuinely open risks are both *coordination* risks, not code risks: the ErrInvalidEvent membership conflict resolves silently (whoever lands last wins, CI green, one design's docs falsified, resolution doc unaware of one party), and the mixed-version rollout window lets a pre-0042 replica re-POST terminal rows. Neither is a code defect; both are landing-governance gaps that the corrections above would close.
