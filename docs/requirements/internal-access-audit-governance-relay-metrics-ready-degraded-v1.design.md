# Design — `internal/access` direction 3: audit-governance relay counters + composition with the B3-2 degraded readiness design

**Spec:** `docs/requirements/internal-access-audit-governance-relay-metrics-ready-degraded-v1.spec.md` (REQ-1..REQ-6)
**Sibling design (shared surfaces):** `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.design.md` (B3-2 — owns `Ready()` flip, `Degraded()`/`BacklogAge()` cache, `storeProbeTimeout`, `degradedChecker`, the two gauges, the alert rule, startup warnings, dashboard panel)
**HEAD:** `acfaaf4` (verification basis = this checkout) · **Date:** 2026-08-08

> **Division of labor (spec §1 composition note):** the spec's REQ-2/REQ-3/REQ-4/REQ-5 are **sibling-locked** — the sibling design already specifies them in depth. This design owns **REQ-1 (the four `relay_*` counters)** and the **test composition** (REQ-6); it *adopts* the sibling design for everything shared and defines the merge rule so the two directions land without drift or duplicate definitions. Where this design references the sibling, the reference is authoritative.

---

## 1. Verification register (evidence re-checked, not trusted)

All spec citations re-checked on this checkout (`acfaaf4`).

| # | Spec claim | Re-check | Verdict |
|---|---|---|---|
| E1 | Zero telemetry in `internal/auditgovernance/` | `grep -rn "telemetry\|metrics\." internal/auditgovernance/` → no hits; `relay.go`/`runtime.go` import list contains no telemetry | ✅ exact |
| E2 | `event_outbox.*` precedent — counters + helpers + increment placement after the durable write | `metrics.go`: struct fields `mEventOutbox*` (after `:56`), `initDomain` registrations `event_outbox.delivered_total`/`retried_total`/`failed_total`/`claim_lost_total`/`pruned_total`/`l2_*`; helpers `IncEventOutbox*`; increments at `internal/events/event_outbox_relay.go:328` (`complete` → `IncEventOutboxDelivered` **after** `CompleteEventOutbox` nil), `:347`/`:349` (`retry` → `IncEventOutboxFailed`/`IncEventOutboxRetried`); `IncEventOutboxClaimLost(context.Background())` at `:346` proves the `Background()`-ctx precedent | ✅ exact (line numbers ±2 from spec — `:124-137` is the first five helpers; the family extends to `:172`) |
| E3 | Alert precedent `EventOutboxTerminalFailures` (`alerts.yml:99-112`) | Rule block `:105-112`, `expr: sum(rate(event_outbox_failed_total[15m])) > 0`, `for: 5m`, `severity: warning`; `grep -n audit alerts.yml` → only the `:104` comment and `:112` description | ✅ exact; no audit-governance rule exists |
| E4 | `Ready()` maxLag hard flip `:157-159`, zero dependents | `runtime.go:157-159` `if ok && time.Since(oldest) > r.maxLag { return errors.New("audit governance backlog exceeds maximum lag") }`; `grep -rn "\.Ready(" internal/auditgovernance/ cmd/server/` → only `http.go:66`; **no test asserts the maxLag or drain errors** | ✅ exact |
| E5 | `readyzHandler` 503 mapping `http.go:66-68`; `readinessGroup` `:40-48`; no deadline on `extra.Ready` | `readyzHandler :51-74`: `extra.Ready(req.Context())` `:66` → 503 `runtime dependency unavailable` `:67-68`; `readyzProbeTimeout = 2s` `:34-38` wraps **only** `store.Stat` `:59-61`; healthy body `{"ok":true}` `:71-73`; route `:101` | ✅ exact |
| E6 | Failed rows excluded from `OldestPending` (`failed_at_ns=0`); pinned by terminal test | `audit_governance_claim.go:188-201` `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`; `HasPendingDrainingAuditGovernance :202-210` same predicate + `b.state='draining'`; `audit_governance_test.go:334-372` pins never-pending (`:357-358`), fencing (`:347-350`), retention prune (`:361-365`) | ✅ exact; **no repository change needed** |
| E7 | Contract items 2 & 4; 450 = maxLag default 900 × 0.5 | `docs/campaigns/implementation-gate.md:22` (item 2: 删 `Ready()` 翻转 → `degraded` + maxLag×0.5（450s）告警; 终态行排除; 读路径超时降级非 503) and `:24` (item 4: attempted/delivered/failed/dead/oldest-age; 0 Observe today); `config_audit_governance.go:66` `MaxLagSeconds` default 900; validation `> ClaimTTLSeconds` `:241`, `<= 604_800` `:251` | ✅ exact |
| E8 | Sibling spec name-lock | `cmd-server-audit-governance-ready-degraded-v1.spec.md` REQ-4 (`audit_governance.backlog_age_seconds`, `audit_governance.degraded`), REQ-5 (`AuditGovernanceBacklogDegraded`), §4 non-goal: B3-4 "may add `relay_*` counters … must not re-define an oldest-age gauge under another name"; **sibling design already exists and is complete** (D1-D7) | ✅ exact — this design is B3-4; the sibling design is the base |
| E9 | Sink/`receipt` mechanics for the counter wiring test | `validateReceipt` (`http.go:121-151`): 202 + `conflict:true` → `ErrReceiptConflict` → `failFact` (terminal); 202 + matching receipt → success → `CompleteAuditGovernance`; non-202 → `httpStatusError` → `retryFact`. **Wire body carries no `tenant_id`** (`governanceEvent`, `model.go:60-82`) — per-tenant routing in the test must go through the `Authorization` header (per-binding client credentials → distinct tokens issued by the test's `/token` endpoint), not body fields | ✅ verified — drives D5 sink design |
| E10 | Test idioms: `runtimeConfig` (maxLag 4 s, poll 10 ms), `WrapRepository`, sink/token server, poll-until-seen | `runtime_test.go:39-46`, `:60-98` (sink decodes `event_id`, echoes receipt), `:120-172` (conflict terminal + never re-POSTed); `telemetry` TestMain/sharedPromHandler (`main_test.go:8-23`); `EnablePrometheus` (`prometheus.go:30-59`) is idempotent-ish (second install would orphan instruments — TestMain-only rule) | ✅ verified — all reused |
| E11 | 500-line gates | `relay.go` 191, `metrics.go` 393, `runtime_test.go` 410 (⇒ new test files), `http_test.go` 129, `metrics_test.go` 79 | ✅ verified (targets in §8) |

**Key finding (not in the direction's evidence, confirmed):** the sibling B3-2 design and this spec plan **the same shared test files** (`runtime_ready_test.go`, `http_test.go` additions, `metrics_test.go` gauge/alert tests) under partially different names. §2 D4 resolves this with a canonical-name merge rule — otherwise the two directions collide at merge time.

---

## 2. Design

### D1 — Telemetry: four relay counters (REQ-1)

`internal/telemetry/metrics.go` — exactly the `event_outbox` template (E2), three additions:

**1. Struct fields** (after `mEventOutboxL2Rejected`):

```go
	mAuditGovRelayAttempted metric.Int64Counter
	mAuditGovRelayDelivered  metric.Int64Counter
	mAuditGovRelayFailed     metric.Int64Counter
	mAuditGovRelayDead       metric.Int64Counter
```

**2. Registrations in `initDomain`** (after the event_outbox block):

```go
	mAuditGovRelayAttempted, _ = m.Int64Counter("audit_governance.relay_attempted_total")
	mAuditGovRelayDelivered, _ = m.Int64Counter("audit_governance.relay_delivered_total")
	mAuditGovRelayFailed, _ = m.Int64Counter("audit_governance.relay_failed_total")
	mAuditGovRelayDead, _ = m.Int64Counter("audit_governance.relay_dead_total")
```

**3. Four helpers** (after `IncEventOutboxL2Rejected`, same lazy `initDomain()` shape, no attributes):

```go
// IncAuditGovernanceRelayAttempted counts one audit-governance delivery
// attempt: a claimed fact processed by the relay, including retries.
func IncAuditGovernanceRelayAttempted(ctx context.Context) {
	initDomain()
	mAuditGovRelayAttempted.Add(ctx, 1)
}
// IncAuditGovernanceRelayDelivered counts one durable completion: receipt
// accepted AND the row completed (fires only after CompleteAuditGovernance
// returns nil — event_outbox placement precedent).
func IncAuditGovernanceRelayDelivered(ctx context.Context) { initDomain(); mAuditGovRelayDelivered.Add(ctx, 1) }
// IncAuditGovernanceRelayFailed counts one transient failure rescheduled for
// retry (retryFact; analog of event_outbox.retried_total).
func IncAuditGovernanceRelayFailed(ctx context.Context) { initDomain(); mAuditGovRelayFailed.Add(ctx, 1) }
// IncAuditGovernanceRelayDead counts one terminal-with-retention failure
// (failFact; dead-letter class, contract naming — the repo column is
// failed_at_ns, a documented deviation owned by the T-3 sibling).
func IncAuditGovernanceRelayDead(ctx context.Context) { initDomain(); mAuditGovRelayDead.Add(ctx, 1) }
```

Prometheus export: `audit_governance_relay_attempted_total` / `..._delivered_total` / `..._failed_total` / `..._dead_total` (dots→underscores, `_total` suffix, no attributes).

### D2 — `internal/auditgovernance/relay.go`: increment sites (REQ-1)

Import `"github.com/aero-vault/aero-vault/internal/telemetry"` (no cycle: telemetry imports only otel/prometheus). Four sites, exactly the direction's mapping (spec D1):

```go
func (r *Runtime) deliverFact(fact repository.AuditGovernanceFact) {
	telemetry.IncAuditGovernanceRelayAttempted(context.Background()) // D2.1 entry
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.publisher.Publish(ctx, fact)
	cancel()
	if errors.Is(err, ErrReceiptConflict) {
		r.failFact(fact, err)
		return
	}
	if err != nil {
		r.retryFact(fact, err)
		return
	}
	ctx, cancel = context.WithTimeout(context.Background(), r.httpTimeout)
	err = r.store.CompleteAuditGovernance(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken)
	cancel()
	if err != nil {
		r.logger.Warn("audit governance acknowledgement lost",
			"fact_ref", r.redactor.opaqueFact(fact), "error_class", "store")
		return // D2.2 — warn-only branch becomes warn+return; nothing followed it
	}
	telemetry.IncAuditGovernanceRelayDelivered(context.Background()) // D2.2 durable
}

func (r *Runtime) failFact(fact repository.AuditGovernanceFact, cause error) {
	telemetry.IncAuditGovernanceRelayDead(context.Background()) // D2.4 entry
	...

func (r *Runtime) retryFact(fact repository.AuditGovernanceFact, cause error) {
	telemetry.IncAuditGovernanceRelayFailed(context.Background()) // D2.3 entry
	...
```

- **D2.1** `attempted` at `deliverFact` entry (first statement) — every claim processed, retries included.
- **D2.2** `delivered` after `CompleteAuditGovernance` returns nil. The ack-lost branch becomes `warn + return` (no statements followed it — control-flow-equivalent) and increments **nothing** (claim-lost is not in the contract's four-name list, spec D1).
- **D2.3** `failed` at `retryFact` entry — transient/rescheduled class.
- **D2.4** `dead` at `failFact` entry — terminal-with-retention class.

`context.Background()` at all four sites (no fact-scoped ctx exists at entry; matches the `IncEventOutboxClaimLost(context.Background())` precedent, E2). Counters are domain-level; the ctx carries only trace linkage.

### D3 — Counting semantics & the reconciliation invariant

Per `deliverFact` call, exactly **one** outcome path follows `attempted`:

```
attempted ──► delivered   (publish ok + durable complete)
          ├─► failed      (transient → retryFact)
          ├─► dead        (terminal → failFact)
          └─► (nothing)   (ack-lost: publish ok, complete write failed)
```

**Invariant: `attempted = delivered + failed + dead + ack_lost`**, i.e. `attempted ≥ delivered + failed + dead`, equality iff zero ack-loss. The un-instrumented ack-lost remainder is a *diagnostic* (visible in logs as "acknowledgement lost"), not an error — the spec's "delivered+failed+dead exactly" phrasing is refined here to the ≥ form.

Two documented semantics:

1. **Event-counting, not row-transition counting.** `failed`/`dead` increment at *entry* (spec D1) — if the `RetryAuditGovernance`/`FailAuditGovernance` write itself fails (claim lost), the row is re-claimed after lease expiry and the counter increments again on the next attempt. A single terminal row can therefore contribute >1 `dead` while the store is wedged. Bounded by claim-TTL cycles; when the store is healthy, `dead` ≈ terminal rows (one per row) and `failed` ≈ retry reschedules. This is a deliberate deviation from the events-outbox placement (which increments *after* the durable write) — entry placement makes the counters an **attempted-class signal** that still moves when the store is wedged, which is exactly the stalled-relay detection the contract wants (`implementation-gate.md:24` "stalled relay 可检测").
2. **`delivered` is durable-placement** (after `CompleteAuditGovernance` nil, D2.2) — an ack-lost complete does not count; the eventual re-claim + re-delivery (idempotent duplicate receipt, contract A) + complete increments it once. Eventually consistent, mirrors `IncEventOutboxDelivered` placement (E2).

### D4 — Composition with the sibling B3-2 design (REQ-2/3/4/5) + canonical test merge

**Adoption table** — every shared surface is specified by the sibling design; this design adds nothing to them:

| Surface (spec REQ) | Owner (sibling design) | This design |
|---|---|---|
| `storeProbeTimeout` const, bounded `Ready()` probes, `isProbeCtxErr`/`record` | D1 | adopt |
| `Degraded()`/`BacklogAge()` cache getters, `run()` feed (`probeAndRecord`) | D1/D2 | adopt |
| `degradedChecker`, `readinessGroup.Degraded()`/`BacklogAge()`, `readyzHandler` degraded payload | D3 | adopt |
| `RegisterAuditGovernanceGauges` + `main.go` wiring | D4 | adopt |
| Alert `AuditGovernanceBacklogDegraded` + startup warnings + dashboard panel | D5/D7 | adopt (this spec's REQ-5/AC-4 maps to it; **no second rule** — counters get no rule, per spec non-goals) |
| Config-doc semantics | D6 | adopt |
| `audit_governance.relay_*` counters + helpers + relay.go sites | — | **this design D1/D2** |

**Name lock (spec D2, sibling spec §4 non-goal):** the gauge names `audit_governance.backlog_age_seconds`/`audit_governance.degraded` and the alert name `AuditGovernanceBacklogDegraded` are owned by B3-2. This design registers only `relay_*` counters. Renaming either gauge, or defining a second oldest-age gauge, breaks the sibling's tests and the alert expr — forbidden.

**Canonical test merge (the collision risk):** both specs specify the same shared tests under different names. Single definition per test, canonical names = the sibling's (landed-first design):

| This spec's name (REQ-6) | Canonical name (merged) | File |
|---|---|---|
| `TestRuntimeReadyMaxLagDegradesAndDeadRowExclusion` | `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` | `internal/auditgovernance/runtime_ready_test.go` |
| `TestRuntimeReadyDrainStillHardFails` | `TestRuntimeReadyDrainHardFails` | same |
| `TestRuntimeReadyStoreTimeoutDegrades` | `TestRuntimeReadyStoreTimeoutDegrades` (identical) | same |
| `TestReadyzAuditGovernanceMaxLagDrill` | `TestReadyzAuditGovernanceDegradedDrill` | `cmd/server/http_test.go` |
| `TestReadyzDegradedExtraReturns200WithMarker` / `TestReadyzHealthyExtraReturns200Unchanged` | identical | `cmd/server/http_test.go` |
| `TestAlertsYMLAuditGovernanceRelayRuleConsistency` | `TestAlertsYMLAuditGovernanceRuleConsistency` (sibling's, hardened per D5.1/D5.2 — asserts the same rule name/expr/severity/450-phrasing) | `internal/telemetry/metrics_test.go` |

The assertions are identical by construction (the rule and gauges are single-owned); only the function names merge. **Implementation rule:** B3-2 lands first (or both land in one change); whichever lands second merges into the landed names. §8 lists only this direction's *unique* files plus the merge annotations.

### D5 — Tests unique to this direction

**D5.1 `internal/auditgovernance/relay_metrics_test.go` (new).** Package TestMain mirroring `internal/telemetry/main_test.go:8-23` — each Go test binary has its own process globals, so this cannot collide with the telemetry package's TestMain:

```go
var promHandler http.Handler

func TestMain(m *testing.M) {
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck // no-op provider → Prometheus-backed (EnablePrometheus)
	h, err := telemetry.EnablePrometheus()
	if err != nil {
		panic(err)
	}
	promHandler = h
	os.Exit(m.Run())
}
```

`TestRuntimeRelayCountersTrackDeliveryOutcomes` (AC-3 counter half):

- **One httptest server**, three roles routed deterministically:
  - `/token` — decode Basic auth, return `{"access_token":"token-<client_id>","token_type":"Bearer","expires_in":60}` — **distinct token per binding**. (The wire body carries no `tenant_id` — E9 — so the Authorization header is the routing key.)
  - sink path — route on `Authorization: Bearer token-*`:
    - `token-succ` → decode body `event_id`; **202** + `{"receipt":{"event_id":<id>,"tenant_id":"succ","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}` → delivered
    - `token-conf` → **202** + same shape + `"conflict":true` → dead
    - `token-retry` → **500** → failed (retry loop)
  - per-branch `atomic.Int32` POST counters.
- Three active bindings (`succ`/`conf`/`retry`, distinct `ClientID`/`ClientSecret`, shared `TokenURL` = server `/token`), `runtimeConfig(server.URL)` (maxLag 4 s, poll 10 ms, batch 10, claimTTL 3 s, revision 1); real SQLite `repository.Open`+`Migrate`; seed one fact per tenant via `WrapRepository(repo, runtime).RecordAudit(ctx, repository.AuditEntry{TenantID: t, Action: "key.add"})` (existing idiom, E10).
- `runtime.Start(ctx)`; poll until all three branch counters ≥ 1 (3 s deadline — E10 idiom); `runtime.Close()`; scrape `promHandler`.
- Assertions (line-exact parse helper — see D5.3):
  - `audit_governance_relay_delivered_total == 1` — exactly one success fact exists; completes are fenced-once.
  - `audit_governance_relay_dead_total == 1` — exactly one terminal fact; `failFact` fires once per claim, and the terminal row is never re-claimed (`runtime_test.go:162-170` pin).
  - `audit_governance_relay_attempted_total >= 3`, `audit_governance_relay_failed_total >= 1` — the retry tenant may re-claim within the window; `>=` absorbs post-window retry rounds (spec risk row).
  - No wall-clock equality; no `==` on attempted/failed.

**D5.2 `internal/telemetry/metrics_test.go` — `TestAuditGovernanceMetrics_SurfaceInScrape` (add).** Calls the four `IncAuditGovernanceRelay*` helpers once each (`context.Background()`), scrapes `sharedPromHandler`, asserts the four names with value 1. **Gauge assertions live in the sibling's `TestAuditGovernanceGauges`** — the gauge pair is registered exactly once per package (OTel duplicate-instrument rule); if this direction lands first, it must still leave the gauge registration to a single test so the sibling merges without a second registration.

**D5.3 Scrape parsing must be line-exact.** `strings.Contains(body, "audit_governance_relay_delivered_total 1")` is unsound: `"..._total 1"` is a substring of `"..._total 10"`. Both new tests use a small helper:

```go
// scrapeValue returns the value of the first line matching "<name> <float>".
func scrapeValue(body, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			v, err := strconv.ParseFloat(fields[1], 64)
			return v, err == nil
		}
	}
	return 0, false
}
```

---

## 3. API changes & compatibility constraints

| Surface | Change |
|---|---|
| `telemetry.IncAuditGovernanceRelayAttempted/Delivered/Failed/Dead(ctx)` | **New** exported helpers (+ 4 unexported counters). No existing symbol changed |
| `auditgovernance.Runtime` | **No new exported members from this direction** — `Degraded()`/`BacklogAge()` are sibling-owned (D4). `relay.go` gains only internal increments + one import |
| `deliverFact` ack-lost branch | `warn` → `warn + return`; control-flow-equivalent (nothing followed), no observable change |
| `readyzHandler`/`readinessGroup`/`Ready()` | **Untouched by this direction** (sibling-owned) |
| REST/S3/MCP/WebDAV wire payloads, DB schema (I2), config env (I5), middleware chain (I4), storage keys (I3) | **none** |
| `/metrics` | New series only under the existing `aero-vault/domain` meter, lazy after first relay activity (`initDomain` pattern — an idle relay emits no series, consistent with all other domain counters) |
| No new `go.mod` deps (I6) | prometheus exporter + `database/sql` already in tree |

**Compatibility with the sibling B3-2:** gauge names, alert name, `Degraded()`/`BacklogAge()` surface are single-owned; this direction adds only `relay_*` counters (sibling spec §4 non-goal satisfied). The two designs share files (`runtime_ready_test.go`, `http_test.go`, `metrics_test.go`, `alerts.yml`) — they must land in one change or B3-2 first, with the D4 merge rule.

---

## 4. Failure modes

| # | Mode | Behavior | Disposition |
|---|---|---|---|
| FM1 | Store wedged while `failFact`/`retryFact` runs | Entry placement (D2) means `dead`/`failed` still increment even when the Fail/Retry write fails; a terminal row may count >1 `dead` across claim-TTL cycles | Documented semantics (D3.1) — an attempted-class signal is exactly what stalled-relay detection needs; healthy-store rates ≈ row transitions |
| FM2 | Complete-write failure (ack-lost) | `delivered` not incremented; re-claim + idempotent re-delivery + complete increments it later | Eventually consistent (D3.2); the invariant gap `attempted − (delivered+failed+dead)` is the un-instrumented ack-lost count |
| FM3 | Counter-underflow illusion on scrape | `attempted < delivered+failed+dead` is **impossible** by construction (D3) — if observed, it's a second instrumentation site bug | The relay_metrics test's `>=` assertions pin this |
| FM4 | Duplicate `EnablePrometheus` in the auditgovernance test binary | Second call builds a provider with no instruments (instruments bind to the first provider via `domainOnce`) → empty scrape, confusing failures | **TestMain-only rule** (D5.1); no per-test calls; mirrors telemetry's own TestMain |
| FM5 | Loaded-CI timing flake in the wiring test | Retry tenant may re-claim within the observe window | Spec mitigation kept: `==` only on delivered/dead (exactly-one invariants), `>=` on attempted/failed, poll-until-seen before scrape, no wall-clock equality |
| FM6 | Scrape assertion false-negative from substring matching | `Contains(body, "..._total 1")` matches `..._total 10` | Line-exact `scrapeValue` helper (D5.3) in both tests |
| FM7 | Metric-name drift | Any rename of the four counters breaks `TestAuditGovernanceMetrics_SurfaceInScrape` (`go test ./...` gate); gauge renames break the sibling's tests + alert expr | Drift guard in both directions |
| FM8 | Both directions merge without the D4 rule | Duplicate test function names / double gauge registration → compile or scrape failure in CI | Merge rule + implementation order (D4); the shared files are touched by exactly one PR |
| FM9 | Import cycle | none — telemetry imports no auditgovernance | — |
| FM10 | `/metrics` scrape while relay store wedged | Gauge callbacks read only the mutex cache (sibling D4); counters are client-side increments | unaffected by this design |

---

## 5. Migration & rollback

- **Migration steps: none.** No schema migration (I2 untouched — zero SQL changes; the `failed_at_ns=0` exclusion already exists, E6), no config surface (I5), no wire change, no `go.mod` change (I6).
- **Deploy sequence.** Counters ship with the binary — no ordering constraint of their own. The shared alert rule `AuditGovernanceBacklogDegraded` follows the sibling D5.3 contract: lands **with or before** the binary (rule-first is safe: absent series → no fire; binary-first is the fully silent wedge the direction removes). If B3-2 has not landed, this change must not ship the gauges' *consumers* (readyz degraded payload, alert rule) without the sibling's runtime flip — in practice: land both directions in one release.
- **Rollback.** Revert `metrics.go` (4 fields + 4 registrations + 4 helpers), `relay.go` (4 increments + import, restore the warn-only ack-lost branch), delete `relay_metrics_test.go`, remove the `metrics_test.go` addition. Lossless — counters are derived state; no data, no state, no ordering constraints.
- **Operational watch.** Post-deploy, alert on `sum(rate(audit_governance_relay_dead_total[15m]))` / `relay_failed_total` is available to operators (the contract's "指标可喂 H2 告警" — the shipped rule is gauge-based; counter rules are operator-side, per spec non-goals). The invariant `attempted ≥ delivered+failed+dead` and the ack-lost gap are the diagnostic lens.

---

## 6. Testable acceptance mapping (AC → test → assertion anchors → gate)

| Acceptance (spec §5) | Test | Assertion anchors | Gate |
|---|---|---|---|
| **AC-3** counter half — counters incremented at deliverFact/failFact/retryFact, exposed at /metrics | `TestRuntimeRelayCountersTrackDeliveryOutcomes` (**new**, `internal/auditgovernance/relay_metrics_test.go`) — one sink, three token-routed branches (202 / 202+conflict / 500), one fact per tenant, poll-until-seen, scrape via TestMain `promHandler` | `relay_delivered_total == 1`; `relay_dead_total == 1`; `relay_attempted_total >= 3`; `relay_failed_total >= 1` (line-exact `scrapeValue`, D5.3) | `go test ./internal/auditgovernance/` |
| **AC-3** surface half — names/values pinned | `TestAuditGovernanceMetrics_SurfaceInScrape` (**new**, `internal/telemetry/metrics_test.go`) — four Inc helpers once, scrape `sharedPromHandler` | `audit_governance_relay_attempted_total 1`, `..._delivered_total 1`, `..._failed_total 1`, `..._dead_total 1` | `go test ./internal/telemetry/` |
| **AC-1** (D1 drill) — 200-with-marker, never 503; hung store degrades | Adopted canonical: `TestReadyzAuditGovernanceDegradedDrill`, `TestReadyzDegradedExtraReturns200WithMarker`, `TestRuntimeReadyStoreTimeoutDegrades` (sibling design §6; same assertions as this spec's REQ-6.1/6.3 — merged per D4) | 200 + `"degraded":true` on lag and hung-store paths; `Ready()==nil` within [1 s, 5 s]; recovery → 200 `{"ok":true}` / `Degraded()==false` | `go test ./internal/auditgovernance/ ./cmd/server/` |
| **AC-2** dead-row exclusion | Adopted canonical: `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` phase A (dead-only store → `ok==false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`); **untouched**: `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` (`audit_governance_test.go:334-372`) | — | `go test ./internal/auditgovernance/ ./internal/repository/` |
| **AC-4** alert rule | Adopted canonical: `TestAlertsYMLAuditGovernanceRuleConsistency` (sibling D5/D7; this spec's REQ-6.4 name merges into it) — YAML-parse `../../deploy/prometheus/alerts.yml`; rule `AuditGovernanceBacklogDegraded`, expr references exactly `audit_governance_degraded` and `audit_governance_backlog_age_seconds` and **no other** `audit_governance_*` name (incl. relay-counter collision guard), `severity: warning`, description contains `450` but not `maxLag×0.5` | — | `go test ./internal/telemetry/` |
| Gauge surface (shared) | Adopted: `TestAuditGovernanceGauges` (single registration) + `TestGrafanaAuditGovernancePanel` | — | `go test ./internal/telemetry/` |
| Hard gates | `make check` | gofmt clean · `go build ./...` · `go vet ./...` · `go test ./...` (SQLite + local FS + httptest loopback, zero external network) · every touched `.go` file ≤ 500 lines | CI |

> **Composition note:** the AC-1/AC-2/AC-4 rows are marked *adopted* — the sibling design owns those tests. If the sibling lands after this design (against the recommended order), this design's D4 canonical names are the merge target either way.

---

## 7. Disposition of prior attempts & coordination

1. **Sibling design B3-2** (`cmd-server-audit-governance-ready-degraded-v1.design.md`) is complete (D1-D7, 443 lines) and owns every shared surface. This design is the delta — nothing in REQ-2/3/4/5 is re-designed here (adoption table, D4). Its adversarial-review corrections (AC-2 phase-C sequencing, AC-4 drain sequencing, FM1 `repo.Ping` scope, D5.1-D5.4 alert hardening) are **inherited** — the merged tests implement the corrected forms.
2. **B3-1** (permanent-error classification, `docs/requirements/internal-access-audit-governance-permanent-error-classification-v1.spec.md`): changes *which* rows reach `failed` — the `failed_at_ns=0` exclusion (E6) makes this design's semantics compose unchanged. No shared rows with the events outbox (per the sibling design's §7.3).
3. **Spec-vs-design refinement (D3):** the spec's "attempted = delivered+failed+dead exactly" is refined to `attempted ≥ delivered+failed+dead` (ack-lost remainder un-instrumented by contract design). No acceptance criterion changes; the test assertions already use `>=` on attempted.
4. **Shared-file collision (E11 finding):** resolved by D4's canonical-name table. Both designs' §8 file lists touch `runtime_ready_test.go`/`http_test.go`/`metrics_test.go`/`alerts.yml` — exactly one PR may own them.

---

## 8. Files changed (complete list — this direction's unique footprint)

| File | Change | Size after |
|---|---|---|
| `internal/telemetry/metrics.go` | +4 counter fields (after `mEventOutboxL2Rejected`), +4 `initDomain` registrations (after the event_outbox block), +4 `IncAuditGovernanceRelay*` helpers (after `IncEventOutboxL2Rejected`) | 393 → ~430 (< 500 ✅) |
| `internal/auditgovernance/relay.go` | +`telemetry` import; +4 increments (deliverFact entry; delivered after complete-nil with ack-lost branch → `warn+return`; retryFact entry; failFact entry) | 191 → ~203 (< 500 ✅) |
| `internal/auditgovernance/relay_metrics_test.go` | **new** — TestMain (single `EnablePrometheus`), `scrapeValue` helper, `TestRuntimeRelayCountersTrackDeliveryOutcomes` | ~160 (< 500 ✅) |
| `internal/telemetry/metrics_test.go` | +`TestAuditGovernanceMetrics_SurfaceInScrape` (+ `scrapeValue` helper; gauge assertions deliberately absent — single-registration rule) | 79 → ~120 (< 500 ✅) |

**Shared files (owned by sibling B3-2; this direction only merges into them per D4):** `internal/auditgovernance/runtime_ready_test.go`, `cmd/server/http.go` + `http_test.go`, `internal/telemetry/metrics_test.go` gauge/alert/dashboard tests, `cmd/server/main.go` + `audit_governance.go` (warnings), `deploy/prometheus/alerts.yml`, `deploy/grafana/aero-vault-ai-ops-dashboard.json`, `docs/configuration.md`.

No other files. No `go.mod` changes (I6 ✅). No `internal/config`/`.env.example`/migration/schema changes (I2/I5 ✅).
