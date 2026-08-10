# Design — `cmd/server`: decouple `Ready()` — degraded state + 450 s alert/`BacklogAge`; store-probe timeouts degrade instead of 503 (D1)

**Module:** `cmd/server` + `internal/auditgovernance` + `internal/telemetry` · **Spec:** `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.spec.md` (REQ-1..7, D1..D5, AC-1..4)
**HEAD:** `acfaaf4` · **Baseline:** the campaign worktree = `acfaaf4` + ~30k uncommitted sibling lines (B3-1, readyz-probe-timeout, chain refactor, access-control, event-outbox) — register rows tagged [B]/[W] per row (see §1) · **Date:** 2026-08-08
**Scope lock:** exactly one behavior change — audit-governance backlog lag and store-probe timeouts stop 503-ing the node and become a degraded state (200 + marker + gauges + one alert). B3-1/B3-3/B3-4/B3-6, billing-runtime readiness, drain semantics, config surface are non-goals (spec §4).

---

## 1. Verification register (evidence re-checked, not trusted)

The evidence under verification was the requirements-stage deliverable for direction B3-2. Every line-number citation it makes was re-read on this checkout; the spec's own evidence table was then re-checked symbol-by-symbol.

**Baseline audit (this revision) — the "HEAD `acfaaf4`" claim was false and is corrected here.** HEAD is `acfaaf4`, but the working tree carries **29,967 uncommitted insertions across 220 files** from sibling campaigns: B3-1 (permanent-error classification), readyz-probe-timeout, chain refactor (`applyMiddleware` → `internal/server`), access-control, event-outbox, plus antivirus/metadata/reconcile work. The register below was verified against **the tree**, so roughly half its rows reference symbols that do not exist at `acfaaf4` (`readyzProbeTimeout`, the `http_test.go` stubs/tests, `failed_at_ns`, `FailAuditGovernance`, `EventOutboxTerminalFailures`, migration 0042 — none has `git log -S` history). Sibling commit-readiness was audited on the dirty tree: `gofmt -l` clean · `go build ./...` clean · `go vet ./...` clean · `go test ./...` green (29 packages, no FAIL/panic) — the tree is self-contained and gate-ready. **Baseline decision:** commit-siblings-first was rejected — the ~30k lines mix ~10 mid-flight campaigns (several at requirements stage only), the batch pipeline owns commit sequencing, and one mega-commit would defeat review/bisect. Instead the register is restated with per-row tags: **[B]** = valid at `acfaaf4` and in the tree (unmodified files / identical content), **[W]** = tree-only symbol or numbering (absent or shifted at `acfaaf4`). After the siblings land, [W] rows re-verify mechanically against the new HEAD — this audit confirms the cited content is exactly what the tree holds.

| Evidence citation | Re-verified location (tag: [B] both baselines / [W] tree-only) | Verdict |
|---|---|---|
| `runtime.go:145-161` `Ready()` — drain + maxLag hard errors, no degraded/BacklogAge | **[B]** `internal/auditgovernance/runtime.go:145-161` (unmodified in the tree — same lines at `acfaaf4`): drain lookup `:147-149`, drain-in-progress `:150-152`, backlog lookup `:154-156`, maxLag flip `:157-159`, `return nil` `:160` | ✅ **exact** (confirmed in this review, §D1 source) |
| "store calls use r.httpTimeout ctx" (direction's claim) | **[B]** `r.httpTimeout` used only in `New` (apply-desired-bindings, `applyDesiredBindings(applyCtx, ...)` `:72`) and `Close` (drain wait `time.After(r.claimTTL + r.httpTimeout)` `:126`); `Ready` passes the caller ctx straight to both store probes | ✅ **correction holds** — the unbounded probe is the D1 gap (REQ-1 closes it) |
| `http.go:51-74` readyzHandler, `extra.Ready` error → 503 `:65-69`, readinessGroup `:40-48`, `readyzProbeTimeout` `:34-38` | **[W]** `cmd/server/http.go`: `readinessChecker` `:30-31` [B]; `readyzProbeTimeout = 2s` `:34-38` (**absent at `acfaaf4`** — `git log -S` → no commit ever; probe-timeout campaign, uncommitted); `readinessGroup.Ready` `:40-48`; `readyzHandler` `:51-74`; `extra.Ready(req.Context())` `:66` → 503 `runtime dependency unavailable` `:67-68`; healthy body `{"ok":true}` written at `:72-73`; route `r.Get("/readyz", readyzHandler(...))` `:101` | ✅ **tree-exact**; at `acfaaf4` all of these shift −5: group `:35-44`, handler `:46-68`, `extra.Ready` `:59`, 503 `:60-61`, body `:64-66`, route `:94` (the probe-timeout insert is the +5). The spec's "drift from cited `:39-58`" note compares two worktree states, not HEAD |
| `audit_governance.go:51-64` runtimeReadiness; `config_audit_governance.go:55` gate | **[B]** `cmd/server/audit_governance.go:51-64` (unmodified; billing + audit runtimes → `readinessGroup`, nil when both absent); `Enabled: getEnvBool("AUDIT_GOVERNANCE_ENABLED", false)` at `internal/config/config_audit_governance.go:55` [B]; wiring `cmd/server/main.go:70` (build) / `:157` (`runtimeReadiness(billingRuntime, auditRuntime)` → `buildRouter`) — **[W] numbering** (at `acfaaf4`: `:69`/`:155`; chain-refactor/access-control edits shift main.go); second construction site `main.go:196-206` is `runMCP()` (at `acfaaf4`: `:194-201`; no readiness/gauge wiring) | ✅ **exact** — sites identical at both baselines, main.go numbering [W] |
| `config_audit_governance.go:61` poll default; `:66` maxLag 900 | **[B]** (file unmodified) `PollMilliseconds: getEnvInt("AUDIT_GOVERNANCE_POLL_MILLISECONDS", 1000)` at `:61`; `MaxLagSeconds: getEnvInt("AUDIT_GOVERNANCE_MAX_LAG_SECONDS", 900)` at `:66`; validation `MaxLagSeconds > ClaimTTLSeconds` `:241`, `<= 604_800` `:251` | ✅ **exact** |
| `audit_governance_claim.go:188-201` `failed_at_ns=0` lag exclusion | **[W]** `internal/repository/audit_governance_claim.go`: `OldestPendingAuditGovernance` `:188-201`, predicate `WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0` at `:195`; `HasPendingDrainingAuditGovernance` `:202-210` (cited `:203-210` off by one at the func line). **At `acfaaf4`: `failed_at_ns` has zero hits repo-wide** (B3-1, uncommitted); `OldestPendingAuditGovernance` `:164-175` has only `delivered_at_ns=0`; `HasPendingDrainingAuditGovernance` `:177-184` | ✅ **tree-exact** (T-3 lag side needs no repository work — the exclusion already exists in the tree) |
| alerts.yml — only `EventOutboxTerminalFailures`, no audit-governance rule | **[W]** `deploy/prometheus/alerts.yml`: group `aero-vault-integrity` at `:58` [B]; `EventOutboxTerminalFailures` `:105-112` (**absent at `acfaaf4`** — event-outbox campaign, uncommitted; there `ScrubFoundCorruptObjects` is `:99-106` and `grep -in "audit"` → zero hits); in the tree `grep -n "audit"` → only the `:104` comment / `:112` description | ✅ **tree-exact**; the "no audit-governance rule" substance holds at both baselines |
| `http_test.go:103` body assert; `:69-88` probe-timeout idiom; `:27-56` stub idiom | **[W]** `cmd/server/http_test.go`: stub family (partial-embed) `:27-56`; `TestReadyzStorageProbeTimeout` `:69-88` (elapsed ∈ [1 s, 5 s], blocking stub); `TestReadyzErrNotFoundIsReady` `:93-108`, body assert `{"ok":true}` at `:103` — **all absent at `acfaaf4`**, where the file is 18 lines with only `TestRedirectWebUI` (`:9`) | ✅ **tree-exact** |
| `metrics.go:326` gauge pattern | **[B]** (file unmodified) `internal/telemetry/metrics.go:326-334` `RegisterQueueDepthGauge` (observable gauge, lazy meter `aero-vault/domain`, callback reads fn per scrape); file header `:13-17` (dots→underscores, `_total` only on counters) | ✅ **exact** |
| `configuration.md:274` | **[B]** `docs/configuration.md:274`: "Oldest undelivered outbox age that `/readyz` permits." (line intact at both baselines) — semantics change required (REQ-6) | ✅ **exact** |
| `runtime.go:202` startup gate | **[B]** `internal/auditgovernance/runtime.go:202` (unmodified): `applyDesiredBindings` unbound-backlog error ("audit governance unbound backlog blocks startup"); the pin test is **[W]**: `TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference` is at `runtime_test.go:197-233` in the tree, `:125` at `acfaaf4` | ✅ **exact** (gate [B]); ⚠️ pin numbering [W] |
| "draining pinned by TestRuntimeRejectsRemovedBinding…" | **[W] numbering** — That test pins the **startup** rejection path; `grep -rn "\.Ready(" internal/auditgovernance/ cmd/server/` → only `http.go:44` (group loop) and `:66` (worktree lines; at `acfaaf4`: `:39`/`:59` — same two sites); **no test anywhere asserts `Ready()`'s drain or maxLag errors** | ⚠️ **partially accurate** — matches spec E8; REQ-7.1/AC-4 adds the missing `Ready()`-branch pin |
| "runtime_test.go is at the 400/500-line gate" | **[W]** `wc -l internal/auditgovernance/runtime_test.go` → **400 in the tree** (drain-pin test added by a sibling); **328 at `acfaaf4`**. `runtime.go` → **209** [B]; `metrics.go` → **393** [B]; `cmd/server/http_test.go` → **129** [W] (18 at `acfaaf4`); `cmd/server/audit_governance.go` → **65** [B] | ✅ **tree-exact** — the "new file `runtime_ready_test.go` is mandatory" conclusion holds in the tree (400+150 > 500) but not at bare `acfaaf4` (328+150 < 500); the new-file decision is therefore **tree-contingent** (re-check after the siblings land) |
| "`grep -rn "BacklogAge\|Degraded" internal/auditgovernance/ cmd/server/` → empty" | **[B]** identical at both baselines: `BacklogAge` → zero hits repo-wide ✅. `Degraded` → substring hits only: `cfg.AI.DegradedMode` (`cmd/server/http.go:112`, `main.go`, `internal/config/config_ai.go`, `internal/api/rest/search.go`) plus `aiDegraded` method names — the AI kill-switch flag, unrelated symbol | ⚠️ **substring-only collision** — no `Degraded()` method exists anywhere; substance holds (spec §1's grep phrasing is loose, not wrong) |
| Test-harness API surface for REQ-7 | `runtimeConfig` harness `runtime_test.go:39-46` (maxLag 4 s, poll 10 ms, ClaimTTL 3 s) [B — unchanged lines, file modified elsewhere]; `publisherConfig` **[W]** `internal/auditgovernance/http_test.go:20-25` (sic — not `cmd/server/http_test.go`; at `acfaaf4`: `:18-24`); `New(cfg, store, logger)` `runtime.go:54` [B]; `ClaimAuditGovernance` `audit_governance_claim.go:16` [B]; `CompleteAuditGovernance` `:126` [W] (`:124` at `acfaaf4`); `FailAuditGovernance` `:159-172` **[W — B3-1, absent at `acfaaf4`]** (owner+token+live-lease fenced); `ApplyAuditGovernanceBindings` `audit_governance_binding.go:18` **[B — the file exists at `acfaaf4` with the same symbol at `:18` (unmodified); the review claim "file doesn't exist at acfaaf4" is wrong]**; `WrapRepository`/`RecordAuditWithGovernance` `internal/auditgovernance/repository.go:15/:33` [B — unmodified]; `newPublisher` `internal/auditgovernance/http.go:30-59` [B — same span at `acfaaf4` (`:30-58` + trailing blank `:59`; the receipt-conflict edit is at `:190+` and does not shift this region)] + `secureEndpoint` `:60-72` [B] (loopback `http://127.0.0.1:1` passes; no network at construction — `newTokenSource` builds a struct only) | ✅ **all present in the tree**; per-symbol tags as marked |
| WAL second-writer for backdating | **[B]** (file unmodified) `internal/repository/sqlite.go`: `db.SetMaxOpenConns(1)` (`:26`) + `PRAGMA journal_mode = WAL` (`:31`) (the repo's own conn serializes its writes; a second `database/sql` conn to the same `file:` DSN can write) | ✅ **holds** |
| Prometheus test single-registration rule | **[B]** (both files unmodified) `internal/telemetry/prometheus_test.go:1-24` NOTE + `TestMain` (`main_test.go:8-11`) | ✅ **exact** |
| Metric name transformation dots→underscores, no `_total` on gauges | **[B]** `metrics.go:17` header (counters `_total`), `jobs.pending` → `jobs_pending` (existing `RegisterQueueDepthGauge`) | ✅ **holds** |

**Spec-requirement checks (all hold; tags per §1):** REQ-1 semantics are exactly the E1/E2 shape [B]; REQ-2's cache getters have zero existing dependents to break [B]; REQ-3's healthy-body byte-identity is pinned by `http_test.go:103` **[W]** (the pin test exists only in the tree — re-verify after the probe-timeout campaign commits); REQ-4's two gauge names are free (`grep "audit_governance" internal/telemetry/` → empty) [B]; REQ-5's 450 = `MaxLagSeconds` 900 × 0.5 (E5/E7) [B]; REQ-6's doc line is the one cited [B]; REQ-7's file gate math is **[W]** (runtime_test.go 400 in the tree vs 328 at `acfaaf4` — the new-file mandate is tree-contingent, see the 400/500-gate row).

**F3 hardening audit (alert swap — added this revision; every claim re-read on this checkout):**

| Fact | Verified location | Finding |
|---|---|---|
| Alertmanager routing config in-repo? | `grep -rn "alertmanager" deploy/ .github/ Makefile` → **absent**; `docs/analysis-v8-gaps-roadmap.md:440` lists the alertmanager config as a helm-chart gap; `deploy/helm/aero-vault/templates/` has no alerting config | Routing is **operator-owned** — severity must carry the routing intent in-band (D5.1) |
| Severity conventions | `alerts.yml`: `critical` ×2 (`:21` `HighServer5xxRate` `:14`, `:118` `ScrubFoundCorruptObjects` `:114` — data-loss/integrity); `warning` ×9 incl. `EventOutboxTerminalFailures` `:105-112` (delivery-path failure, durable L0 fallback); `info` ×2 (`:53` `:76`) | `warning` is the established convention for delivery-path degradation; the analog rule is `EventOutboxTerminalFailures` — a requirement, not an assumption (D5.1) |
| `MaxLagSeconds` valid range | `config_audit_governance.go:241` (`> ClaimTTLSeconds`) and `:251` (`<= 604_800`) | 450-literal valid only at the default 900 → D5.2 drift guard |
| Grafana dashboards keyed on readyz 503s? | Both `deploy/grafana/*.json`: **zero** `readyz`/`probe`/`up`/503-specific references; only aggregate 5xx panels (`status=~"5.."` `aero-vault-dashboard.json:201/:699`; `status="500"` `aero-vault-ai-ops-dashboard.json:84`) | **Nothing goes silent** — they lose a driver, not a trigger; the old "dashboards keyed on readyz 503s" phrasing in §5 is corrected (D5.4) |
| Deploy consumer of the readyz 503 | `deploy/helm/aero-vault/templates/deployment.yaml:83-88` — `readinessProbe: httpGet {path: /readyz}` (period 10 s) | LB-eviction consumer is **intentionally removed** by this change; probe unchanged and still 503s on drain/genuine errors (D5.4) |
| Existing rule-validation gate | `.github/workflows/ci.yml:84-86` runs `go test ./...`; **no promtool** in CI or Makefile | The AC-3 Go test is the *only* artifact gate → must YAML-parse, not grep (D5.3) |

---

## 2. Design

### D1 — `internal/auditgovernance/runtime.go`: bounded probes + degraded cache + `Ready()` flip removal (~+55 lines, 209 → ~265)

New package constant, adjacent to `Ready()` (comment cross-references `readyzProbeTimeout`, `cmd/server/http.go:34-38` — same rationale, same value, independent symbol):

```go
// storeProbeTimeout bounds Ready()'s two store probes independently of
// AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS (5s default, relay HTTP bound) and
// REQUEST_TIMEOUT_SECONDS: a wedged relay store must not hold /readyz.
// Mirrors readyzProbeTimeout (cmd/server/http.go) — same rationale, same
// value, deliberately a separate symbol.
const storeProbeTimeout = 2 * time.Second
```

New fields on `Runtime` (after `cleanupBatch`): `mu sync.Mutex; degraded bool; backlogAge time.Duration`. Note `sync` is already imported (`startOnce`, `stopOnce`).

`Ready()` rewrite — the single behavioral flip (maxLag → record+`nil`) plus the probe bound; hard paths unchanged in message text (no test depends on the strings, but log stability argues for keeping them):

```go
func (r *Runtime) Ready(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)
	defer cancel()
	draining, err := r.store.HasPendingDrainingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxErr(err) {
			r.record(true, 0) // timeout/cancel → degraded, age unknown
			return nil
		}
		return errors.New("audit governance drain lookup failed")
	}
	if draining {
		return errors.New("audit governance binding drain is in progress")
	}
	oldest, ok, err := r.store.OldestPendingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxErr(err) {
			r.record(true, 0)
			return nil
		}
		return errors.New("audit governance backlog lookup failed")
	}
	if !ok {
		r.record(false, 0) // no pending (dead rows excluded by the repository predicate)
		return nil
	}
	age := time.Since(oldest)
	r.record(age > r.maxLag, age) // was: hard error when ok && age > maxLag
	return nil
}

// isProbeCtxErr classifies the only two degraded-able probe outcomes. A store
// that returns a non-context error after the deadline is a genuine failure →
// hard 503 (spec §6 risk, mitigated by probing only through probeCtx).
func isProbeCtxErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// record publishes one probe result to the cache. Never called with the
// mutex held; safe under concurrent Ready()/run().
func (r *Runtime) record(degraded bool, age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.degraded = degraded
	r.backlogAge = age
}
```

### D2 — Cache getters + run-loop feed (REQ-2)

```go
// Degraded reports whether the most recent probe timed out or found pending
// backlog older than maxLag. Cache getter — zero store I/O; fresh to within
// one poll interval (default 1s; run() also refreshes it).
func (r *Runtime) Degraded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.degraded
}

// BacklogAge reports the oldest pending age at the most recent probe; 0 when
// no pending (dead rows excluded) or when the probe timed out (age unknown).
// Cache getter — zero store I/O.
func (r *Runtime) BacklogAge() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.backlogAge
}
```

`run()` feed — one bounded probe per poll cycle after `cleanupDelivered()` (spec REQ-2: the "run() 循环 maxLag×0.5 降级告警" input; the cache is what the gauges/alert read, so the alert is fed even when nothing calls `/readyz`):

```go
		r.deliverBatch()
		r.cleanupDelivered()
		r.probeAndRecord(context.Background()) // degraded-state feed; never hard-fails
		timer.Reset(r.pollEvery)
```

```go
// probeAndRecord refreshes the degraded cache from one bounded probe.
// Context errors degrade (age unknown → 0); genuine store errors leave the
// cache untouched (Ready() reports those hard); drain-in-progress is not a
// degraded state — it stays Ready()-hard.
func (r *Runtime) probeAndRecord(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)
	defer cancel()
	draining, err := r.store.HasPendingDrainingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxErr(err) {
			r.record(true, 0)
		}
		return
	}
	if draining {
		return
	}
	oldest, ok, err := r.store.OldestPendingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxErr(err) {
			r.record(true, 0)
		}
		return
	}
	if !ok {
		r.record(false, 0)
		return
	}
	age := time.Since(oldest)
	r.record(age > r.maxLag, age)
}
```

Shared helpers across `Ready`/`probeAndRecord`: `isProbeCtxErr` + `record` + `storeProbeTimeout` (spec's "one small helper shared by Ready and run" is realized as this pair — the probe sequence itself must stay explicit in `Ready` so hard-error classification is visible).

### D3 — `cmd/server`: degraded (non-503) readiness payload (REQ-3)

`cmd/server/http.go` (+`fmt` import):

```go
// degradedChecker is the optional degraded-state surface for readiness
// extras: cache-backed getters only — no store I/O on the readiness path.
// Implementers keep the node ready (200) while exposing the degraded marker.
type degradedChecker interface {
	Degraded() bool
	BacklogAge() time.Duration
}

func (g readinessGroup) Degraded() bool {
	for _, checker := range g {
		if d, ok := checker.(degradedChecker); ok && d.Degraded() {
			return true
		}
	}
	return false
}

func (g readinessGroup) BacklogAge() time.Duration {
	var max time.Duration
	for _, checker := range g {
		if d, ok := checker.(degradedChecker); ok {
			if age := d.BacklogAge(); age > max {
				max = age
			}
		}
	}
	return max
}
```

`readyzHandler` (`:65-69` region) — the only changed branch; healthy path byte-identical:

```go
		if extra != nil {
			if err := extra.Ready(req.Context()); err != nil {
				http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)
				return
			}
			if d, ok := extra.(degradedChecker); ok && d.Degraded() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"ok":true,"degraded":true,"backlog_age_seconds":%d}`,
					int64(d.BacklogAge().Seconds()))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
```

`cmd/server/audit_governance.go` — **no change** (`runtimeReadiness` already returns `readinessGroup`, which gains the two methods; `billing.Runtime` doesn't implement `degradedChecker` and contributes `false`/`0` via the type-assertion).

### D4 — Telemetry: two gauges, cache-fed (REQ-4)

`internal/telemetry/metrics.go` (after `RegisterQueueDepthGauge`, `:326-334`; same lazy-meter pattern):

```go
// RegisterAuditGovernanceGauges registers the audit-governance degraded
// surface: backlog age (seconds) and degraded flag (0/1), read from fn on
// each scrape. fn must only read cached state — a /metrics scrape must never
// touch the store (a wedged store degrades, it must not hang the scrape).
// Call once, after the meter provider is installed.
func RegisterAuditGovernanceGauges(fn func(context.Context) (int64, int64)) {
	m := otel.Meter("aero-vault/domain")
	_, _ = m.Int64ObservableGauge("audit_governance.backlog_age_seconds",
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			age, _ := fn(ctx)
			o.Observe(age)
			return nil
		}))
	_, _ = m.Int64ObservableGauge("audit_governance.degraded",
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			_, degraded := fn(ctx)
			o.Observe(degraded)
			return nil
		}))
}
```

Export: `audit_governance.backlog_age_seconds` → `audit_governance_backlog_age_seconds`; `audit_governance.degraded` → `audit_governance_degraded` (dots→underscores; gauges get no `_total`, per the file header).

Wiring — `cmd/server/main.go` `run()`, immediately after `registerGauges(repo)` (`:154`):

```go
	registerGauges(repo)
	if auditRuntime != nil {
		telemetry.RegisterAuditGovernanceGauges(func(context.Context) (int64, int64) {
			age, degraded := int64(auditRuntime.BacklogAge().Seconds()), int64(0)
			if auditRuntime.Degraded() {
				degraded = 1
			}
			return age, degraded
		})
	}
```

Registration is unconditional w.r.t. `PROMETHEUS_ENABLED` (lazy binding to the installed provider, mirroring `registerGauges`); `runMCP()` (`main.go:196-206`) does **not** register (no gauges today).

### D5 — Alert rule + alert-path hardening (REQ-5; F3 hardening)

`deploy/prometheus/alerts.yml`, group `aero-vault-integrity`, after `EventOutboxTerminalFailures` (`:105-112`):

```yaml
      - alert: AuditGovernanceBacklogDegraded
        expr: audit_governance_degraded == 1 or audit_governance_backlog_age_seconds > 450
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Audit governance backlog degraded"
          description: "Audit-governance outbox oldest pending age exceeded the 450s fixed early-warning threshold (calibrated to the AUDIT_GOVERNANCE_MAX_LAG_SECONDS default of 900s; the audit_governance_degraded arm is the config-true signal for any non-default maxLag) or the relay store probe degraded. /readyz remains 200 with a degraded marker — inspect the audit sink and OldestPendingAuditGovernance. Route alongside EventOutboxTerminalFailures (same severity, same receiver family)."
```

- Both arms fire while readiness is **200**: age ∈ (450, 900] = alert-but-not-degraded (early warning before the old flip threshold); age > 900 or probe-timeout = alert-and-degraded. 450 is a literal (alerts.yml has no templating; `maxLag` is runtime config).

**D5.1 — Severity/paging contract (resolves the warning-vs-paging assumption).** `severity: warning` is a **requirement**, not an assumption: the repo ships no Alertmanager routing config (§1 audit) — routing is operator-owned — so the rule must declare its routing intent in-band (the description names the sibling rule and receiver family). `warning` matches the one existing delivery-path analog, `EventOutboxTerminalFailures` (`:105-112`, also warning): audit-governance backlog lag is a delivery-path degradation with a durable L0 fallback, not data loss — `critical` is reserved repo-wide for data-loss/integrity signals (`HighServer5xxRate` `:21`, `ScrubFoundCorruptObjects` `:118`). Deploy contract: Alertmanager must route `AuditGovernanceBacklogDegraded` to the **same receivers as `EventOutboxTerminalFailures`**; verification = the D5.3 startup warning names rule + severity (concrete routing cross-check item) and the release note repeats the contract.

**D5.2 — 450-literal vs non-default maxLag (drift guard).** 450 is a **fixed constant**, valid only as `maxLag` default 900 × 0.5; alerts.yml has no templating. Valid `MaxLagSeconds` range is (ClaimTTL, 604800] (`config_audit_governance.go:241/:251`) — at `maxLag=3600` the age arm fires 8× before the degraded arm (early-warning semantics change); at `maxLag<450` the age arm fires *after* the degraded arm and adds nothing. Therefore: (a) the description above never claims config-true derivation — the "maxLag×0.5 at default" phrasing is **forbidden** and pinned by the AC-3 test; (b) the `audit_governance_degraded == 1` arm is the **config-true** signal (fires iff age > maxLag or probe timeout, any config); (c) `buildAuditGovernanceRuntime` (`cmd/server/audit_governance.go:15-49`) logs a startup **warning** when `MaxLagSeconds != 900`:

```go
	if cfg.AuditGovernance.MaxLagSeconds != 900 {
		logger.Warn("AuditGovernanceBacklogDegraded age arm calibrated for default maxLag",
			"alert_age_arm_seconds", 450, "max_lag_seconds", cfg.AuditGovernance.MaxLagSeconds,
			"config_true_arm", "audit_governance_degraded")
	}
```

**D5.3 — Deploy atomicity gate.** Binary-without-rule is a **fully silent wedge** (FM11: readyz 200, no series, no rule, old 503 eviction gone). Three enforced layers: (1) **CI artifact gate** — `TestAlertsYMLAuditGovernanceRuleConsistency` (AC-3) YAML-parses `../../deploy/prometheus/alerts.yml` (the repo artifact) and runs in `go test ./...` (`.github/workflows/ci.yml:84-86`; no promtool exists anywhere — §1 audit), so the artifact cannot merge without the rule; (2) **fleet gate** — `buildAuditGovernanceRuntime`, when the runtime is enabled, logs once at startup:

```go
	logger.Warn("deployed Prometheus must contain alert AuditGovernanceBacklogDegraded (deploy/prometheus/alerts.yml); without it the audit-governance degraded state has no alert path")
```

(3) **ordering** — alerts.yml lands **with or before** the binary, never after (rule-first is safe: absent series → no fire, `absent()` deliberately not in the expr; binary-first is the wedge). Enforced by the §5 release checklist (Prometheus rule deployment is operator-side, so this layer is procedural).

**D5.4 — Single-path signal fan-in (documented property).** Old flip consumers: (a) helm `readinessProbe` on `/readyz` (`deployment.yaml:83-88`) → LB eviction — **intentionally removed** by this change (the decoupling goal); the probe stays, still 503s on drain/genuine errors, no chart change needed; (b) dashboards — **audited: no shipped panel keys on readyz 503s** (§1); only aggregate 5xx panels (`status=~"5.."`) lose a *driver*, not a trigger. New signal: **exactly one machine paging path — Prometheus scrape → Alertmanager**; the `/readyz` degraded payload is inspection-only. This single point of failure is accepted and must be stated in the release note; D7 restores a second (non-paging) consumer in the shipped dashboard.

### D6 — Config doc semantics (REQ-6)

`docs/configuration.md:274` — replace "Oldest undelivered outbox age that `/readyz` permits." with:

> Oldest undelivered outbox age above which audit-governance readiness is **degraded** (`/readyz` stays 200 with `degraded:true`; alert age arm at 450 s = half the default).

`.env.example:197` — value unchanged (900), comment not present there (values only). No `internal/config` change.

### D7 — Dashboard consumer for the degraded signal (new; REQ-5.4)

`deploy/grafana/aero-vault-ai-ops-dashboard.json`: add one panel after "Job queue depth (pending)" (`:122`) — title **"Audit-governance backlog (degraded)"**, two targets: `audit_governance_backlog_age_seconds` (time series, threshold line at 450) and `audit_governance_degraded` (0/1). This is the second consumer of the fan-in (D5.4) and the dashboard-side replacement for the readyz-503 trigger that never existed (audit, §1). Repo-pinned by `TestGrafanaAuditGovernancePanel` (AC-3) — the panel must survive dashboard edits.

---

## 3. API changes & compatibility constraints

| Surface | Change |
|---|---|
| `Runtime.Ready(ctx)` | Signature unchanged. **Semantics:** now errors only for drain-in-progress and genuine (non-context) store failures; maxLag-lag and probe timeouts return `nil` + record degraded. Only caller is `http.go:66` — verified no other call sites |
| `Runtime.Degraded()` / `Runtime.BacklogAge()` | **New** methods, cache getters, zero store I/O. No existing dependents (`grep` empty) |
| `readinessChecker` (`http.go:30-31`) | **Unchanged.** New sibling interface `degradedChecker` (optional, type-asserted) — `billing.Runtime` and any third-party `readinessChecker` without the methods are unaffected |
| `readyzHandler` / `readinessGroup` | Signature unchanged. `readinessGroup` gains `Degraded()`/`BacklogAge()` (methods, not interface changes) |
| `/readyz` healthy response | **Byte-identical** `{"ok":true}` + `Content-Type: application/json` — pinned by `http_test.go:103` ([W] pin, tree-only test) |
| `/readyz` degraded response | **New:** HTTP 200 `{"ok":true,"degraded":true,"backlog_age_seconds":N}` (only when `extra` implements `degradedChecker` and reports degraded). LB/orchestrator keep the node |
| `/readyz` hard-fail response | **Unchanged:** 503 `runtime dependency unavailable` for drain-in-progress / genuine store error (drain semantics are a non-goal, pinned by AC-4) |
| `/readyz` timing, wedged relay store | 503-after-unbounded → **200 after ≤ ~2 s** (internal `storeProbeTimeout`; applies to the **probe-level wedge** — the full-store wedge still hangs at the unbounded `repo.Ping`, §7 item 6c); the storage probe (`readyzProbeTimeout`, `:59-61` [W] tree numbering) untouched |
| `/metrics` | **New** gauge pair `audit_governance_backlog_age_seconds` / `audit_governance_degraded`; scrape-safe (cache-fed). Prometheus disabled → no-op (lazy meter) |
| `alerts.yml` | **New** rule `AuditGovernanceBacklogDegraded` (warning, `for: 5m`); no existing rule touched. Severity = routing contract (D5.1), 450 = fixed constant (D5.2), deploy gate = CI artifact test + startup warning + ordering (D5.3) |
| `runtime_test.go` | **Untouched by this design** ([W] state: 400 lines in the tree, 328 at `acfaaf4`); new `runtime_ready_test.go` (mandate is tree-contingent — §1 400/500-gate row) |
| REST/S3/MCP/WebDAV, DB schema, config env, wire payloads, middleware chain (I4) | **none** |
| Invariants I1/I2/I3/I5/I6 | untouched — no SQL changes, no migrations, no storage-key logic, no new flags, no new `go.mod` deps |
| `docs/configuration.md:274` | Text-only semantics update (REQ-6); no value, key, or validation change |

**Coordination constraints (spec §4):** B3-4 (relay counter family) must **reuse** `audit_governance_backlog_age_seconds`/`audit_governance_degraded` and may add `relay_*` counters, but must not re-define an oldest-age gauge under another name; B3-1 (permanent-error classifier) changes *which* rows are `failed` — the `failed_at_ns=0` exclusion (E4) means dead-row handling composes unchanged.

---

## 4. Failure modes

| # | Mode | Behavior | Disposition |
|---|---|---|---|
| FM1 | Wedged relay store (probe hangs) | `Ready()` → degraded + `nil`; `/readyz` → **200** `degraded:true` after ~2 s (**probe-level wedge**: DB answers `Ping` but the governance query hangs/contends on the single conn — old behavior was "503 after the hung probe finally errored"; a full store wedge still hangs at the unbounded `repo.Ping` `http.go:55` *before* this branch — pre-existing, out of scope, §7 item 6c); gauges show `degraded 1`, `backlog_age_seconds 0` (age unknown); alert fires after 5 m of `degraded==1` | The fix (D1/D3). Tests: `TestRuntimeReadyStoreTimeoutDegrades`, `TestReadyzAuditGovernanceDegradedDrill` |
| FM2 | Sink wedged, store healthy; pending age crosses maxLag | `Ready()` → `nil` + degraded; `/readyz` 200 with marker; alert fires at age > 450 s (**before** the old 900 s 503 flip — the early-warning intent) | Tests: phase B of `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion`; AC-3 scrape + alerts.yml consistency |
| FM3 | Store returns non-context error after the deadline (SDK wraps deadline inside another error) | `isProbeCtxErr` false → classified genuine → **503** | Accepted (spec §6): mitigated by probing only through `probeCtx`; the AC-1 fake returns `ctx.Err()` (realistic wedged shape). If ops reports, widen classification to `errors.Is` on the wrapped chain first |
| FM4 | Genuine SQL failure (not ctx) | Unchanged hard error → 503 — correct, the store is actually broken | `Ready()` hard paths unchanged; AC-4 asserts the drain branch only |
| FM5 | Drain in progress | Unchanged hard error → 503. **Cache note:** the drain branch does not record — the cache may still say not-degraded while /readyz 503s; harmless (503 dominates) and documented in D2 | AC-4 pin |
| FM6 | Dead rows only (terminal, retained) | `OldestPendingAuditGovernance` `ok==false` → not-degraded, `BacklogAge()==0` — T-3 lag exclusion preserved with zero repository work | Phase A of AC-2 test |
| FM7 | Cache staleness between probes | ≤ one poll interval (default 1 s); drain hard-fail can't be stale (live check precedes degraded check in `Ready`) | Documented in D2; no action |
| FM8 | Loaded CI timing flake in the 3 tests with elapsed windows | Blocking stubs make the lower bound deterministic (response cannot precede the 2 s deadline); upper bound 5 s generous; backdating via SQL not sleeps | Mirrors `TestReadyzStorageProbeTimeout` idiom (`http_test.go:69-88`, [W] tree-only — proven in the local suite, **not yet in CI** until the probe-timeout campaign commits) |
| FM9 | OTel duplicate gauge registration (TestMain shared handler) | Second registration errors silently (return values dropped, as the existing pattern does) — but the scrape would break | REQ-7.3 single-registration rule (register once across the package, e.g. TestMain); mirror `prometheus_test.go` NOTE |
| FM10 | `/metrics` scrape while store wedged | **Never blocks** — callbacks read only the mutex-guarded cache (D4 comment makes this a review point) | `grep` review: fn body must not call store methods |
| FM11 | Deploy lag: binary shipped, `alerts.yml` not (or fleet partial) | readyz 200 + degraded marker, gauges emit, **no rule** → fully silent wedge (old 503 eviction gone, no alert path) | Guarded by D5.3: CI artifact gate + startup warning + with-or-before ordering; `absent()` deliberately not in expr (no false fires on unmixed fleet) |
| FM12 | Non-default `maxLag` (valid range (ClaimTTL, 604800]) | Age arm drifts: `maxLag=3600` → fires 8× early (early-warning semantics change); `maxLag<450` → fires after the degraded arm, redundant | Guarded by D5.2: description pins 450 as fixed constant, degraded arm is config-true, startup warning on `MaxLagSeconds != 900` |

---

## 5. Migration & rollback

- **Migration steps: none.** No schema migration (I2 untouched — not a single SQL change), no config surface (D1/D6: const + doc text only), no wire changes, no deploy ordering. One release, code-only.
- **Deploy sequence (D5.3, hard requirement):** `alerts.yml` lands **with or before** the binary — never after. Rule-first is safe (absent series → no fire; `absent()` deliberately not in the expr); binary-first is the fully silent wedge (FM11). CI artifact gate = the AC-3 YAML test (`go test ./...`); fleet gate = the D5.3 startup warning; operator routing cross-check = D5.1.
- **Rollback:** revert `runtime.go` (const + cache + `Ready` flip), `http.go` (degradedChecker + payload branch), `metrics.go` (gauge pair), `main.go` (wiring), `alerts.yml` (rule), `audit_governance.go` (D5.2/D5.3 startup warnings), `deploy/grafana/aero-vault-ai-ops-dashboard.json` (D7 panel), `configuration.md` (line 274); delete `runtime_ready_test.go` + the http_test additions + the telemetry test additions (incl. `TestGrafanaAuditGovernancePanel`) + `TestBuildAuditGovernanceRuntimeAlertWarnings`. Lossless — no data, no state, no ordering constraints.
- **Operational watch (post-deploy, D5.4):** with the flip removed, nodes that *would* have 503'd at 900 s now stay 200 — `AuditGovernanceBacklogDegraded` is the replacement signal and the **only machine paging path** (single-path fan-in: Prometheus → Alertmanager). Ship-time checklist: (1) `go test ./...` green — artifact gate; (2) boot log shows no D5.2/D5.3 warnings (non-default maxLag or missing rule → act); (3) Alertmanager routes the rule alongside `EventOutboxTerminalFailures` (D5.1); (4) the D7 panel is live in the AI & Ops dashboard. Dashboard audit: **no shipped panel was ever keyed on readyz 503s** (§1) — nothing else goes silent; the helm readinessProbe (`deployment.yaml:83-88`) keeps 503 semantics for drain/genuine errors and needs no chart change.

---

## 6. Testable acceptance mapping (AC → test → assertion anchors → gate)

| Acceptance (spec §5) | Test | Assertion anchors | Gate |
|---|---|---|---|
| **AC-1** D1 drill — hung store probes → 200 with degraded marker, never 503; recovery restores `{"ok":true}` | `TestRuntimeReadyStoreTimeoutDegrades` (`internal/auditgovernance/runtime_ready_test.go`) — `hangingStore` embeds `Store`, overrides `ApplyAuditGovernanceBindings`→nil and both probe methods to block on `<-ctx.Done()` then return `ctx.Err()`; gated by `atomic.Bool`; `New(runtimeConfig("http://127.0.0.1:1"), fake, logger)` (loopback passes `secureEndpoint`; no network at construction) | `Ready(context.Background()) == nil`; elapsed ∈ [1 s, 5 s] (blocking stub ⇒ cannot precede the 2 s `storeProbeTimeout`); `Degraded()==true`; `BacklogAge()==0`; flip healthy → `Ready()==nil`, `Degraded()==false` | `go test ./internal/auditgovernance/` |
| **AC-1** end-to-end half | `TestReadyzAuditGovernanceDegradedDrill` (`cmd/server/http_test.go`) — `auditgovernance.New` with the same hanging-store idiom; `extra := runtimeReadiness(nil, runtime)`; `readyzHandler(&stubReadyRepo{}, &notFoundStatStorage{}, extra)` | status **200** (never 503); body contains `"degraded":true`; elapsed ∈ [1 s, 5 s]; healthy → status 200, body exactly `{"ok":true}` | `go test ./cmd/server/` |
| **AC-2** T-3 lag exclusion — dead rows → zero pending; live row > maxLag → degraded; fresh live row → healthy | `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` (`runtime_ready_test.go`) — real SQLite (`repository.Open`+`Migrate`), `runtimeConfig` (maxLag 4 s, poll 10 ms); 3 phases: (A) 2 facts landed terminal via `ClaimAuditGovernance`+`FailAuditGovernance` → `OldestPendingAuditGovernance ok==false`, `Ready()==nil`, `Degraded()==false`, `BacklogAge()==0`; (B) +1 live fact backdated to now-8 s via `UPDATE audit_governance_outbox SET created_at_ns` on a second WAL connection (repo `MaxOpenConns(1)` serializes its own writes; WAL allows the second writer) → `Ready()==nil`, `Degraded()==true`, `BacklogAge() > 4s`; (C) **claim+complete the backdated row first** (`ClaimAuditGovernance` then `CompleteAuditGovernance` — completion is fenced on an active claim; without this, `MIN(created_at_ns)` keeps the 8 s row as oldest and `Degraded()==true` deterministically — reviewer-mandated fix), then +1 fresh live fact → `Degraded()==false`, `BacklogAge() < 4s` | No sleeps anywhere; 8 s backdate vs 4 s maxLag = 2× margin; `>` assertions only | `go test ./internal/auditgovernance/` |
| **AC-4** drain stays hard-fail | `TestRuntimeReadyDrainHardFails` (`runtime_ready_test.go`) — **sequencing (reviewer-mandated): record the pending fact *before* flipping the binding to `draining`** (all fact-insert paths gate on `governanceCaptureActive`, `state=='active'`); flip via direct `ApplyAuditGovernanceBindings(ctx, 2, digest, [{acme,draining}])` (a second `New` at the same revision fails `ErrAuditGovernanceRevisionDrift`); **claim before complete** (completion is claim-fenced) → `Ready(ctx)` errors containing `"drain is in progress"`; via `readyzHandler` that is HTTP **503**; after `CompleteAuditGovernance` → `Ready()==nil`; empty store → `Ready()==nil` | Only the maxLag flip changes; drain hard-fail before and after | `go test ./internal/auditgovernance/ ./cmd/server/` |
| **AC-3** alert observability — gauges surface in scrape; rule references exactly the emitted names | `TestAuditGovernanceGauges` (`internal/telemetry/metrics_test.go`) — register the gauge pair **once** across the package (TestMain pattern, `prometheus_test.go:1-24` NOTE); `fn` returns (450, 1) → scrape body contains `audit_governance_backlog_age_seconds 450` and `audit_governance_degraded 1`; re-scrape after `fn` returns (0, 0) → `audit_governance_degraded 0` | OTel duplicate-instrument rule ⇒ single registration | `go test ./internal/telemetry/` |
| **AC-3** rule/metric drift (hardened) | `TestAlertsYMLAuditGovernanceRuleConsistency` (`internal/telemetry/metrics_test.go`) — **YAML-parse** (not line-grep) `../../deploy/prometheus/alerts.yml` (package-relative); assert rule `AuditGovernanceBacklogDegraded` exists, `expr` references exactly `audit_governance_degraded` and `audit_governance_backlog_age_seconds`, **no other** `audit_governance_*` name (incl. B3-4 collision), `severity: warning` (D5.1), and the description does **not** contain `maxLag×0.5` (D5.2 fixed-constant phrasing pin — drift in either direction fails) | — | `go test ./internal/telemetry/` |
| **D5.4** dashboard consumer | `TestGrafanaAuditGovernancePanel` (`internal/telemetry/metrics_test.go`) — read `../../deploy/grafana/aero-vault-ai-ops-dashboard.json`, assert a panel whose target exprs reference exactly `audit_governance_degraded` and `audit_governance_backlog_age_seconds` | — | `go test ./internal/telemetry/` |
| **D5.2/D5.3** startup warnings | `TestBuildAuditGovernanceRuntimeAlertWarnings` (`cmd/server/audit_governance_test.go`, 45 → ~60 lines) — valid enabled `config.AuditGovernanceConfig` with `MaxLagSeconds: 3600` → captured slog buffer contains the D5.2 warning; `MaxLagSeconds: 900` → absent; enabled runtime always logs the D5.3 rule-presence warning | — | `go test ./cmd/server/` |
| **REQ-3** healthy-payload byte-identity | `TestReadyzHealthyExtraReturns200Unchanged` + existing `TestReadyzErrNotFoundIsReady` (`http_test.go:103` body assert) | fake `Degraded→false` → 200, body exactly `{"ok":true}` | `go test ./cmd/server/` |
| **REQ-3** degraded payload shape | `TestReadyzDegradedExtraReturns200WithMarker` — fake extra `{Ready→nil, Degraded→true, BacklogAge→123s}` | status 200, body exactly `{"ok":true,"degraded":true,"backlog_age_seconds":123}` | `go test ./cmd/server/` |
| Hard gates | `make check` | gofmt clean · `go build ./...` · `go vet ./...` · `go test ./...` (SQLite + local FS, zero network) · every touched `.go` file ≤ 500 lines | CI |

> **Baseline note (§6):** the anchors in this table (`http_test.go:103`, the `runtime_test.go` 400-gate, the `TestReadyzStorageProbeTimeout` `:69-88` idiom, AC-2's `FailAuditGovernance`/`failed_at_ns`/0042 dependency) are **tree-baseline facts** ([W] tags in §1). At bare `acfaaf4` the files are smaller (http_test.go 18, runtime_test.go 328) and the symbols do not exist. Re-verify the [W] anchors against the new HEAD after the sibling campaigns commit, before implementing AC-1..4.
| File-size gates | `wc -l` | `runtime.go` 209 → ~265; `runtime_ready_test.go` new ~180 (< 500); `http.go` 189 → ~215; `http_test.go` 129 → ~195; `audit_governance.go` 65 → ~75 (D5.2/D5.3 warnings); `metrics.go` 393 → ~415; `runtime_test.go` 400 → **untouched** | reviewer |

---

## 7. Disposition of prior attempts (gate re-check)

1. **This direction's own run** (`docs/auto/runs/b3-2-decouple-ready-replace-maxlag-hard-flip-wit-d8aa61ce/`): `DECISIONS.md` records stage `requirements` **PASS** (2026-08-07 15:09:24) — the evidence under verification. **No design-gate verdicts exist yet**; this document is the first design. Its two corrections (E1 `r.httpTimeout` nuance, E8 drain-pin partial accuracy) are re-confirmed in §1 and drive REQ-1 and AC-4 respectively.
2. **Sibling direction runs** (`b3-2-decouple-ready-replace-maxlag-503-flip-with-2d36a88d`, `decouple-ready-into-degraded-mode-and-prove-d1-r-cc3e5055`, `ready-decoupling-maxlag-flip-degraded-450s-alert-1578f806/505b40e2`, `ready-decoupling-maxlag-flip-degraded-maxlag-0-5-f4e8ea54`): all recorded `requirements` **FAIL** (agent exited 1, 2026-08-07 13:13–13:14) — no artifacts to reconcile beyond the two corrections above. The `maxlag-0-5` variant (threshold as maxLag×0.5 expression) is superseded by D5's literal-450 decision (alerts.yml has no templating).
3. **Sibling design** `cmd-server-audit-governance-permanent-error-classification-v1.design.md` (B3-1 — **implemented in the uncommitted campaign worktree, NOT landed in git**: `git log -S "FailAuditGovernance"` / `-S "failed_at_ns"` → empty; migration `0042_audit_governance_terminal_failed.{up,down}.sql` untracked; the register rows citing its symbols are tagged [W]): its R1 refinement ("terminal = final within the retention window") and the 7 d prune re-insert with fresh UUIDs concern the **events outbox**, not `audit_governance_outbox` — no shared rows; B3-1's classifier changes which governance rows reach `failed`, and E4's `failed_at_ns=0` exclusion makes AC-2's phase A compose unchanged. Disposition: **no conflict; coordination note only** (spec §4).
4. **Related design** `cmd-server-readyz-probe-timeout-v1.design.md` (— **implemented in the uncommitted worktree, NOT landed in git**: `git log -S "readyzProbeTimeout"` → empty; `http.go:34-38` and the `http_test.go` tests are [W]): its `readyzProbeTimeout` (storage probe) is deliberately mirrored, not reused, by D1's `storeProbeTimeout` — two layers (object store vs relay store), two symbols, same value and rationale.
5. **Git history**: `git log -S "BacklogAge"` / `-S "storeProbeTimeout"` → no prior implementation ever landed (and no worktree hits either — §1 row 14); the symbols are free. Consistent with the [B]/[W] register: "landed" claims elsewhere in this batch refer to the worktree, not git.
6. **Adversarial reviews of this design** (2026-08-07, `docs/auto/runs/b3-2-decouple-ready-replace-maxlag-hard-flip-wit-d8aa61ce/artifacts/adversarial_review-9c87f3a7/`): plan validated on the tree; dispositions — (a) **AC-2 phase C unsatisfiable as written** (`MIN(created_at_ns)` over all pending rows keeps the 8 s-backdated row as oldest ⇒ `Degraded()==true` deterministically): **fixed in §6** — claim+complete the backdated row before the fresh-fact assertion. (b) **AC-4 sequencing under-specified**: **fixed in §6** — fact → flip to draining (direct `ApplyAuditGovernanceBindings`, revision 2) → claim → complete → assert. (c) **FM1 over-scope**: `repo.Ping` (`http.go:55`) runs unbounded *before* `extra.Ready`, so a full store wedge still hangs `/readyz` at `Ping`; D1's ≤2 s bound covers the probe-level wedge only. **Decision: `repo.Ping` keeps the caller ctx — pre-existing, out of scope** (a future direction may bound it with the same constant); FM1/§3 re-scoped accordingly. (d) **Alert swap vectors (F3)**: **resolved by the concurrent hardening revision** — D5.1 (severity/paging contract: page warnings, same receivers as `EventOutboxTerminalFailures`), D5.2 (450 fixed-constant drift guard + startup warning on non-default maxLag), D5.3 (deploy atomicity: CI artifact gate + startup warning + with-or-before ordering), D5.4 (single-path fan-in documented; D7 adds the second consumer). (e) **F4**: `isProbeCtxErr` treats `context.Canceled` (caller disconnect) as degraded for ≤1 poll — accepted; add one comment sentence at implementation.

---

## 8. Files changed (complete list)

| File | Change | Size after |
|---|---|---|
| `internal/auditgovernance/runtime.go` | +`storeProbeTimeout` const; +`mu/degraded/backlogAge` fields; `Ready()` bounded + maxLag flip → record; +`record`/`isProbeCtxErr`/`Degraded`/`BacklogAge`/`probeAndRecord`; `run()` +1 feed call | ~265 lines (< 500 ✅) |
| `internal/auditgovernance/runtime_ready_test.go` | **new** — `hangingStore` fake; `TestRuntimeReadyDegradedOnMaxLagAndDeadRowExclusion` (3 phases), `TestRuntimeReadyDrainHardFails`, `TestRuntimeReadyStoreTimeoutDegrades` | ~180 lines (< 500 ✅) |
| `cmd/server/http.go` | +`degradedChecker` interface; `readinessGroup.Degraded()`/`BacklogAge()`; degraded payload branch in `readyzHandler`; +`fmt` import | ~215 lines (< 500 ✅) |
| `cmd/server/http_test.go` | +`TestReadyzDegradedExtraReturns200WithMarker`, `TestReadyzHealthyExtraReturns200Unchanged`, `TestReadyzAuditGovernanceDegradedDrill` | ~195 lines (< 500 ✅) |
| `cmd/server/audit_governance.go` | +D5.2/D5.3 startup warnings in `buildAuditGovernanceRuntime` (`:15-49`) | ~75 lines (< 500 ✅) |
| `cmd/server/main.go` | `run()`: +`RegisterAuditGovernanceGauges` wiring when `auditRuntime != nil` (after `registerGauges`, `:154`) | ~1 line net |
| `internal/telemetry/metrics.go` | +`RegisterAuditGovernanceGauges` | ~415 lines (< 500 ✅) |
| `internal/telemetry/metrics_test.go` | +gauge scrape test (single registration) + `TestAlertsYMLAuditGovernanceRuleConsistency` (YAML-parsed, hardened per D5.1/D5.2) + `TestGrafanaAuditGovernancePanel` | < 500 ✅ |
| `deploy/grafana/aero-vault-ai-ops-dashboard.json` | +1 panel (D7) — audit-governance backlog age + degraded, threshold 450 | JSON, no Go line gate (repo-pinned by test) |
| `deploy/prometheus/alerts.yml` | +rule `AuditGovernanceBacklogDegraded` after `:112` | YAML, no Go gate |
| `docs/configuration.md` | `:274` semantics text only | — |
| `docs/requirements/cmd-server-audit-governance-ready-degraded-v1.design.md` | this document | — |

No other files. No `go.mod` changes (I6 ✅). No `internal/config` / `.env.example` / migration / schema changes (I2/I5 ✅).
