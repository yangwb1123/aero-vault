# Requirements Specification — `internal/cli`: B3 dead-letter terminal state + relay metrics observability surface (`admin audit governance` / `admin audit metrics`)

**Module:** `internal/cli`
**Direction:** "CLI observability surface for B3 dead-letter terminal state and relay metrics (admin audit governance status/metrics)"
**Source analysis:** `docs/auto/analyses/internal-cli-17314662.json`
**Date:** 2026-08-08 · **HEAD:** `15763e2` (verification basis = this checkout)
**Score:** value 9 / risk reduction 8 / effort 6 / confidence 7

---

## 1. Scope

The B3-1/B3-4 machinery is server-side only: terminal rows land in `audit_governance_outbox` via `failed_at_ns` (migration 0042), claim/lag predicates exclude them (partial indexes 0043), and relay counters exist only as OTel/Prometheus instruments (`audit_governance.relay_attempted/delivered/failed/dead_total`, `audit_governance.backlog_age_seconds`). The CLI's sole audit surface is `admin audit list` → `GET /v1/admin/audit` (legacy `audit_log`). No route in `internal/api/rest/admin.go`, `internal/api/rest/router.go` or `cmd/server/main.go` exposes outbox dead rows, oldest-pending age, or relay counters. Operators cannot distinguish terminal dead facts from retrying facts (`PROMETHEUS_ENABLED` defaults off, `internal/config/config.go:223`), so the B3-1 terminal classification is unobservable from the operator CLI.

This spec adds **two new actions on the existing `audit` admin resource** — `admin audit governance [--limit N]` and `admin audit metrics` — implemented in one new production file `internal/cli/cli_admin_governance.go` (per-resource file convention: `cli_admin_files.go`, `cli_admin_buckets.go`) plus one new test file `internal/cli/cli_admin_governance_test.go` (stub-server pattern of `TestCmdAdminAudit_List_GetsCorrectPath`, cli_test.go:1636). Both commands GET **proposed companion REST endpoints** (flagged proposed, **not** implemented by this module — see §4 D1) and render the payload losslessly, following the existing admin-subcommand conventions (`readSuccessfulResponse`/`printResponseBody`, response.go:84-102; exit codes 0/1/2).

**Out of scope (see §4):** implementing the REST endpoints (proposed companion contract), any change to `internal/telemetry`, migrations, `internal/repository`, `internal/auditgovernance`, `admin audit list` behavior, degradation signaling (sibling direction "D1 degraded-read-path signaling in CLI error handling"), Prometheus, docs beyond the two usage strings, any `go.mod`/schema change.

---

## 2. Evidence verification

Every citation in the direction was checked against the repository on this commit.

| # | Direction citation | Verified location | Verdict |
|---|---|---|---|
| E1 | `internal/cli/cli_admin.go:400-427` — "cmdAdminAudit only supports 'list' against /v1/admin/audit" | `cmdAdminAudit` spans :400-428; switch body :401-427 with **exactly one action** — `case "list"` → `GET /v1/admin/audit?limit=…` (:410), `--limit` parsed :404-408; `default:` → "unknown audit action", exit 2 (:424-426). Dispatch: `cmdAdmin` `case "audit"` cli_admin.go:52; usage line `audit list [--limit N]` cli_admin.go:29 and top-level cli.go:135 | ✅ **exact** — gap confirmed |
| E2 | `internal/repository/migrations/sqlite/0042_audit_governance_terminal_failed.up.sql` — "failed_at_ns terminal state" | `ALTER TABLE audit_governance_outbox ADD COLUMN failed_at_ns INTEGER NOT NULL DEFAULT 0`; comment: conflict receipts are permanent → relay marks `failed_at_ns` instead of rescheduling; failed rows excluded from claim and pruned by `CleanupFailedAuditGovernance` after the delivered-retention window. Down file present (0042 .down.sql: `DROP COLUMN failed_at_ns`). Postgres twin present (`migrations/postgres/0042_...`) | ✅ **exact** |
| E3 | `internal/repository/migrations/sqlite/0043_audit_governance_pending_partial_index.up.sql` — "claim/lag predicates exclude failed rows" | Two partial indexes, both `WHERE delivered_at_ns = 0 AND failed_at_ns = 0`: `audit_governance_pending_claim_idx (available_at_ns, created_at_ns, id)` (claim path) and `audit_governance_pending_lag_idx (created_at_ns)` (lag path / `OldestPendingAuditGovernance` MIN). Comment explicitly documents the **status/dead_at deviation** ("0042 shipped failed_at_ns (timestamp-led 0039 schema …); Deviation documented, not renamed (zero-behavior rename; I2)") | ✅ **exact** |
| E4 | `internal/telemetry/metrics.go:103-106` — "relay counters" | ⚠️ **off-by-2:** the four counters are at **:105-108** — `mAuditGovRelayAttempted/Delivered/Failed/Dead` → `audit_governance.relay_attempted_total` / `.relay_delivered_total` / `.relay_failed_total` / `.relay_dead_total` (:105-108; :103-104 are event-outbox L2 counters). Increment sites: relay.go:83 (attempted), :112 (delivered), :121 (dead), :163 (failed). `grep` across `internal/cli/` → **zero CLI consumers** | ✅ substance exact (2-line drift) |
| E5 | `internal/telemetry/metrics.go:360` — "backlog gauge" | `RegisterAuditGovernanceBacklogAgeGauge` :360-373; instrument `audit_governance.backlog_age_seconds` :370, callback reads `fn(ctx)` per scrape; wired at `cmd/server/build.go:101-106` (`auditGovernanceBacklogAgeGaugeFn` over `Runtime.BacklogAge()` cache — "terminal (dead-lettered) rows are excluded by the store query, so a fully dead-lettered backlog reports 0"). No CLI consumer | ✅ **exact** |
| E6 | `internal/auditgovernance/relay.go:115-130` — "failFact terminal persistence" | `failFact` :120-134 (doc comment :115-119 — cited range covers doc tail + body): `IncAuditGovernanceRelayDead` :121, `FailAuditGovernance(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken, cause.Error())` :124; claim-loss on fail write warned, never retried in-loop ("terminal-with-retention"). `isPermanentDeliveryError` gates the terminal branch (conflict/invalid receipt/HTTP 409/422) | ✅ substance exact (function at :120; cited :115-130 covers comment + body) |
| E7 | `internal/repository/audit_governance_claim.go:211` — "OldestPendingAuditGovernance" | `OldestPendingAuditGovernance` :211-223; SQL :214-219 — `SELECT MIN(o.created_at_ns) … WHERE o.delivered_at_ns=0 AND o.failed_at_ns=0`; `ok==false` when no pending row (:220-221). Claim predicate `failed_at_ns=0` :78,110,169; `FailAuditGovernance` sets `failed_at_ns` + clears claim :182-196 | ✅ **exact** |
| E8 | `cli_test.go:1636` — "TestCmdAdminAudit_List_GetsCorrectPath" | `TestCmdAdminAudit_List_GetsCorrectPath` :1636-1661 — httptest stub records `gotMethod`/`gotURL`, asserts `GET` + URL `"/v1/admin/audit?"`, output contains `"audit"`. Sibling pattern tests: `…_List_WithLimit` :1663-1678, `…_UnknownAction_Returns2` :1680-1688 (exit 2). Helpers `newTestClient` :67, `captureStdout` :28, `captureStderr` :48 (cli_test.go) | ✅ **exact** |
| E9 | "no route in rest/admin.go, router.go or main.go exposes outbox dead rows, oldest-pending age, or relay counters" | `internal/api/rest/admin.go` — sole audit surface `ListAudit` :430-443 (`GET /v1/admin/audit`, legacy `audit_log`); no governance/outbox/metrics handler. `internal/api/rest/router.go` — single admin audit route :202 (OpenAPI row) + :350 (`r.Get("/admin/audit", adm.ListAudit)`); no governance route. `cmd/server/main.go` — governance wiring only: `buildAuditGovernanceRuntime` :70, `Start` :81, `WrapRepository` :82, `registerGauges` :154; zero governance routes. `grep -rni "governance\|outbox" internal/api/rest/ internal/cli/` (non-test) → single unrelated comment hit | ✅ **gap confirmed** |
| E10 | "PROMETHEUS_ENABLED defaults off" | `internal/config/config.go:223` — `PrometheusEnabled: getEnvBool("PROMETHEUS_ENABLED", false)` | ✅ **exact** |
| E11 | Admin-subcommand conventions the new code must follow | `adminJobsRetry` cli_admin.go:380-398 (path-escaped id, exit 1 + `renderError` on ≥300); `adminJobsList` :350-377 (`--limit/--status/--type` flag parse, JSON passthrough via `printResponseBody`); `renderError` response.go:43-66 (apiErrorBody envelope), `readSuccessfulResponse` :84-94, `printResponseBody` :96-102; `c.do` client helper (cli.go) | ✅ **all present** |
| E12 | Store-side pin that terminal rows are absent from pending age (acceptance's "while terminal rows are absent from it") | `OldestPendingAuditGovernance` predicate (E7); server-side pin `TestRuntimeBacklogAgeZeroWhenAllTerminal` runtime_ready_test.go:254 (all-terminal → age 0 / `ok==false`); prune order `ORDER BY failed_at_ns,id` (CleanupFailedAuditGovernance, audit_governance_cleanup.go:123-124) | ✅ **all present** (server-side; CLI relies on the wire contract) |

**Problem-statement checks:**

| Statement | Verdict |
|---|---|
| "Terminal rows land in audit_governance_outbox via failed_at_ns (migration 0042)" | ✅ **holds** (E2, E6). |
| "Claim/lag exclude them (partial indexes 0043)" | ✅ **holds** (E3, E7). |
| "Relay counters exist only as OTel/Prometheus instruments … no CLI consumer" | ✅ **holds** (E4, E5 — zero hits in `internal/cli/`). |
| "The CLI's sole audit surface is `admin audit list` → GET /v1/admin/audit (legacy audit_log)" | ✅ **holds** (E1 — one action, one route, legacy table). |
| "No route … exposes outbox dead rows, oldest-pending age, or relay counters" | ✅ **holds** (E9). |
| "Operators cannot distinguish terminal dead facts from retrying facts (PROMETHEUS_ENABLED defaults off)" | ✅ **holds** (E10 — default false; counter/gauge surfaces are disabled by default, CLI has no substitute). |
| "Terminal classification is unobservable from the operator CLI" | ✅ **holds** — no CLI path reads `failed_at_ns`-derived state anywhere (E1/E4/E5/E9). |

---

## 3. Requirements

### REQ-1 — Wire contract + dispatch (new file `internal/cli/cli_admin_governance.go`)

Two new actions on the existing `audit` resource (dispatch switch `cmdAdminAudit`, cli_admin.go:401):

- **`admin audit governance [--limit N]`** → `GET /v1/admin/audit/governance[?limit=N]`
- **`admin audit metrics`** → `GET /v1/admin/audit/governance/metrics`

- Paths are package-level unexported constants in the new file (single edit point if the companion REST contract lands with a different path).
- `--limit` parsed exactly like `cmdAdminAudit` list (:404-408) / `adminJobsList` (:352-365): loop over args, `--limit` consumes the next token, unknown flags ignored (parity, not new behavior).
- Dispatch: `case "governance": return c.adminAuditGovernance(args)` and `case "metrics": return c.adminAuditMetrics(args)` in `cmdAdminAudit`; `default:` → "unknown audit action: %s", exit 2 (:424-426) unchanged.
- Usage text updated in **both** places: `adminUsage` (cli_admin.go:29 block — `audit governance [--limit N]`, `audit metrics`) and the top-level usage string (cli.go:135 block — `admin audit governance [--limit N]`, `admin audit metrics`).

### REQ-2 — T-3 governance rendering (dead rows + oldest-pending age)

`adminAuditGovernance` GETs `/v1/admin/audit/governance` and renders the server payload **losslessly as JSON passthrough** (`printResponseBody`, response.go:96-102 — identical to `admin audit list` and `admin jobs list`; no re-formatting, no field dropping, no client-side recomputation):

- Exit 0 + body on stdout for any 2xx; exit 1 + `renderError` to stderr for HTTP ≥ 300 (response.go:84-94 convention); exit 1 + transport error to stderr on `c.do` failure; exit 2 for unknown action (unchanged default).
- The dead rows (store state `failed_at_ns != 0`, E2) arrive in the payload's `dead` array with their `failed_at` dead timestamp; the CLI renders them verbatim — the operator sees the terminal timestamp, which is what distinguishes dead from retrying facts.
- The oldest-pending age arrives as `oldest_pending_age_seconds`; the CLI renders the server-provided value **verbatim** (server-side semantics = `now − MIN(created_at_ns)` over the pending predicate that excludes `failed_at_ns != 0` rows, E7/E12). The CLI must not merge dead rows into the age or alter the payload structure — terminal facts are represented only under `dead`.

### REQ-3 — B3-4 metrics rendering (relay counters)

`adminAuditMetrics` GETs `/v1/admin/audit/governance/metrics` and renders the server payload as JSON passthrough with the same exit-code contract as REQ-2:

- The payload carries `attempted` / `delivered` / `failed` / `dead` (1:1 with the OTel instruments `audit_governance.relay_attempted_total` / `.relay_delivered_total` / `.relay_failed_total` / `.relay_dead_total`, E4) plus `oldest_pending_age_seconds` (E5 gauge input); the CLI renders all five values verbatim.

### REQ-4 — Tests (new file `internal/cli/cli_admin_governance_test.go`, stub-server pattern)

Stub-server tests following the exact pattern of `TestCmdAdminAudit_List_GetsCorrectPath` (cli_test.go:1636-1661) with helpers `newTestClient`/`captureStdout`/`captureStderr` (cli_test.go:28-67):

- **T-3 governance test:** stub records `gotMethod`/`gotURL`; returns `{"dead":[{"id":"f1","tenant_id":"acme","action":"key.add","origin_kind":"admin","attempts":3,"failed_at":"2026-08-08T12:34:56.789Z","last_error":"audit governance reports a conflict"}],"oldest_pending_age_seconds":12.5}`. Asserts: method `GET`; URL path `/v1/admin/audit/governance`; exit 0; output contains the dead timestamp `2026-08-08T12:34:56.789Z` (the `failed_at_ns != 0` terminal marker at the wire level) and the pending age `12.5`; output structure keeps the dead row under `"dead"` separate from `"oldest_pending_age_seconds"` (terminal rows absent from the pending age). Variant with `--limit 25` asserts query `limit=25` (parity with `TestCmdAdminAudit_List_WithLimit` :1663-1678).
- **B3-4 metrics test:** stub returns `{"attempted":5,"delivered":3,"failed":1,"dead":2,"oldest_pending_age_seconds":9.5}`; asserts `GET /v1/admin/audit/governance/metrics`, exit 0, output contains all five values.
- **Error contract test:** stub returns HTTP 500 with an `apiErrorBody` envelope; asserts exit 1 and stderr non-empty (parity with the ≥300 branch of `cmdAdminAudit` list). Existing `TestCmdAdminAudit_UnknownAction_Returns2` (cli_test.go:1680) stays green unchanged (unknown actions still exit 2).

---

## 4. Decisions & non-goals

- **D1 — REST endpoints are a proposed companion contract, not part of this module's change.** No route is added to `internal/api/rest`; the CLI is fully tested against stub servers (REQ-4), so its correctness does not depend on the server side landing. The wire contract (§3 REQ-1/REQ-2/REQ-3 payload shapes) is specified here so the CLI implementation and the future REST delivery share one contract; if the REST direction ships a different path, only the two path constants change.
- **D2 — New files, not an extension of `cli_admin.go`.** `cli_admin.go` is at 470/500 lines (hard gate); the new production file follows the per-resource convention (`cli_admin_files.go`, `cli_admin_buckets.go`). The only edits to existing files are the two dispatch `case`s in `cmdAdminAudit` (cli_admin.go:401, ~4 lines), the `adminUsage` block (cli_admin.go:29, 2 lines) and the top-level usage (cli.go:135, 2 lines) — each well under 500 lines.
- **D3 — JSON passthrough rendering, not reformatting.** Matches every existing admin list command (`admin audit list`, `admin jobs list`); it is lossless by construction (all fields incl. `failed_at` and `oldest_pending_age_seconds` reach the operator) and trivially testable (value-presence assertions). A human-friendly table would be new CLI behavior with no precedent and no acceptance requirement.
- **D4 — Actions on the existing `audit` resource, not a new resource.** `admin audit governance` / `admin audit metrics` extend the existing action switch; the direction's own title ("admin audit governance status/metrics") prescribes this shape. Unknown-action exit 2 behavior is preserved.
- **D5 — `oldest_pending_age_seconds` in the metrics payload.** The acceptance (B3-4) requires "attempted/delivered/failed/dead/oldest-age from the server payload", so the metrics payload carries the age too; the governance payload carries it as well (T-3). Both are the same store-derived value (E7/E12), rendered verbatim.
- **Non-goals:** REST endpoint implementation (D1); telemetry/metrics changes (no CLI-side counters — the CLI is a *consumer* of the server instruments); migrations, repository, `internal/auditgovernance` changes; changes to `admin audit list` (legacy surface untouched); degraded-mode signaling in the CLI (sibling direction); Prometheus/OTel; schema/dependency changes; README/docs beyond the two usage strings (usage text is the in-tree doc, per existing convention).

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**Supplied acceptance (verbatim):**
> **T-3:** stub-server test (pattern of `TestCmdAdminAudit_List_GetsCorrectPath`, cli_test.go:1636) asserting the new subcommand GETs the governance endpoint, renders terminal rows with their dead timestamp (`failed_at_ns != 0`), and renders oldest-pending age that matches `OldestPendingAuditGovernance` output while terminal rows are absent from it;
> **B3-4:** metrics subcommand renders attempted/delivered/failed/dead/oldest-age from the server payload (new REST endpoint is the proposed companion contract — not yet in repository, flagged proposed).

**AC-1 (T-3, path + dead timestamp + age) —** *"the new subcommand GETs the governance endpoint, renders terminal rows with their dead timestamp (failed_at_ns != 0), and renders oldest-pending age that matches OldestPendingAuditGovernance output while terminal rows are absent from it."*
*Testable:* REQ-1 + REQ-2 + REQ-4 — `TestCmdAdminAuditGovernance_GetsCorrectPathAndRenders` against a stub server: (a) recorded request is `GET` with path `/v1/admin/audit/governance` (and `limit=25` when `--limit 25` is passed); (b) exit 0 and stdout contains the dead row's `failed_at` timestamp `2026-08-08T12:34:56.789Z` — the wire serialization of the store's `failed_at_ns != 0` terminal marker — plus `last_error`; (c) stdout contains `oldest_pending_age_seconds` value `12.5` **verbatim** — on the real server this value is the `OldestPendingAuditGovernance` MIN-age output (E7), whose pending predicate excludes `failed_at_ns != 0` rows, so a terminal row can never contribute to it; the CLI must not recompute or restructure it (structure assertion: dead row serialized under `"dead"`, age under `"oldest_pending_age_seconds"` — terminal rows absent from the age). The store-level exclusion property itself is pinned server-side (`TestRuntimeBacklogAgeZeroWhenAllTerminal`, runtime_ready_test.go:254) and is not re-asserted from the CLI package.

**AC-2 (B3-4, metrics) —** *"metrics subcommand renders attempted/delivered/failed/dead/oldest-age from the server payload (new REST endpoint is the proposed companion contract — not yet in repository, flagged proposed)."*
*Testable:* REQ-3 + REQ-4 — `TestCmdAdminAuditMetrics_RendersCounters` against a stub server returning `{"attempted":5,"delivered":3,"failed":1,"dead":2,"oldest_pending_age_seconds":9.5}`: recorded request is `GET /v1/admin/audit/governance/metrics`; exit 0; stdout contains each of the five values. Counter names match the OTel instruments 1:1 (E4); the endpoint remains proposed — the CLI's contract is pinned by the stub, and the acceptance's "flagged proposed" is honored by D1 (no REST route added by this module).

**AC-3 (error/exit contract, convention parity) —** *"exit codes follow the existing admin-subcommand contract."*
*Testable:* REQ-2/REQ-3 + REQ-4 — stub returning HTTP 500 + `apiErrorBody` → exit 1 with non-empty stderr (`renderError`); transport failure → exit 1; `admin audit frob` → exit 2 (existing `TestCmdAdminAudit_UnknownAction_Returns2` stays green); all assertions use the `captureStdout`/`captureStderr` helpers (cli_test.go:28-67).

**Acceptance mapping:** T-3 → AC-1; B3-4 → AC-2; supplied acceptance bullets preserved verbatim above. Baseline gates: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./internal/cli/` green; new production file `cli_admin_governance.go` and test file `cli_admin_governance_test.go` each ≤ 500 lines; `cli_admin.go` stays ≤ 500 (470 + ~6).
