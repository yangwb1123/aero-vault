# Design v2 (implementation-ready) — deterministic fact IDs: s3compat e2e + PG re-enqueue identity pin

**Module:** `internal/api/s3compat` (pin point) · mechanism in `internal/auditgovernance` + `internal/repository`
**Supersedes:** `internal-api-s3compat-audit-governance-deterministic-fact-ids-v1.design.md` + `-v1.spec.md` (v1 spec's REQ-1…REQ-5 are preserved 1:1 unless amended below; this document is the authoritative delta + implementation contract)
**Date:** 2026-08-08 · **Design basis:** HEAD `15763e2` + dirty worktree (identical mechanism to the spec's cited `f666fd2`; all gates re-verified green at this state)
**Scope:** one 6-line production hardening (P-1, unexported function) + three test additions + one configuration.md cell. Zero migrations, zero exported-API change, zero new dependencies.

---

## 0. Review disposition (v1 → v2 delta)

v2 folds in the three review reports (database / security-crypto / go-testing) and the direction's follow-up findings (a)–(e). Every item below has a landed location in this document.

| # | Finding (source) | Disposition | Landed |
|---|---|---|---|
| (a) | F2 claim overstated: every existing test recomputes via the production `DeterministicFactID`, so a consistent formula change (hash algorithm, truncation length) is invisible to all of them — including the v1-proposed e2e, which also recomputes. | **Accepted — new absolute golden values**, derived by executing the production function at HEAD (scratch tests, removed after capture). Four formula vectors (A/B/B′/C) + two production-shaped anchors (D source, E full ID) in §3. New tests: `TestDeterministicFactID_GoldenValue` (repository) + in-file goldens inside the e2e. | §3, §5.1 |
| (b1) | HMAC key rotation is **not idempotency-safe**: rotated key ⇒ every fact ID changes; in-flight facts (claimed-but-unacked, pruned-then-re-enqueued) get a new EventID ⇒ receiver double-ledgers; tombstones protect only delivered facts. | **Accepted — documented** in the design residual risks (R-1) and in `docs/configuration.md:267` (exact replacement text in §8). No code change: the invariant is operational. | §8 R-1 |
| (b2) | Shared-key invariant must be narrowed: "identical on every replica" is wrong across **independent DBs** (independent origin counters) — byte-identical IDs for `origin_id=1` in the same second ⇒ receiver folds the second as Duplicate ⇒ silent ledger loss. Safe only across replicas of the **same origin-counter store**. | **Accepted — documented** (R-2) + configuration.md:267 replacement. | §8 R-2 |
| (c) | `tenant` is the only unconstrained frame field; `tenantSourceID("acme\x00evil")` returns `(…, nil)` today (verified live). | **Accepted — implement the predicate** (P-1, §2): reject C0 controls + DEL in `tenantSourceID`; update the framing doc-comment that currently claims "tenant is constrained" (half-true). One unit test. | §2 |
| (d) | The new s3compat e2e file must respect the ≤500-line hard gate; `authz_gate_test.go` (570 lines) already violates it (Makefile's `make check` line gate excludes `*_test.go`, so this is a review-level gate, not CI-enforced — the new file must comply regardless). | **Accepted** — §5.1 carries an explicit line budget (~340/500) and a `wc -l` gate command (§7); the pre-existing violation is noted in R-5, out of scope. | §5.1, §7, R-5 |
| (e) | F1–F11 matrix re-verification + precise specs for the two outstanding test files (GAP-1 e2e incl. the F5-only "exactly 1 outbox row" detector and the F4 capture-inactive negative; GAP-2 PG prune→re-enqueue loop). | **Accepted** — re-verified matrix in §4; byte-level implementable specs in §5. | §4, §5 |
| (db-1) | `RETURNING id, created_at` + `ON CONFLICT DO NOTHING` never coexist in one statement; conflict paths all consume via `RowsAffected`; relay replay safe by construction + verified live (3/3 concurrent-replay probes). | Confirmed — no design change. The e2e's double-enqueue assertion (`(false, nil)`) pins the `RowsAffected`-based fallback from outside the package. | §5.1 REQ-3.5 |
| (db-2) | I1 placeholder discipline clean; PG tests must use raw `$N` (no rebind). | Confirmed — GAP-2 spec (§5.2) uses `$N` via the existing `raw *sql.DB` only; the e2e's sqlite second-connection SQL uses `?`. | §5 |
| (sec-1) | 128-bit truncation: adequate; cross-origin collision fails loudly at the TEXT PK, never silently merges (dedupe constraint targets `(origin_kind,origin_id)`); residual security rests on key secrecy — document. | Accepted — folded into R-1/R-2 framing + F7 note (a collision manifests as a silently dropped capture, not a 500, because `Bus.Publish` swallows). | §4 F7, §8 |
| (sec-2) | EventID/IdempotencyKey over HTTP is outbound-only, keyed, oracle-free; no IDOR; receiver must treat bearer token (not EventID possession) as auth. | Confirmed — no change. Noted once in §8. | §8 |
| (gt-1) | F5 attribution to `authz_gate_test.go`'s `outboxCount` is false: that harness never wires governance and counts `event_outbox` (written synchronously by `repo.Delete`, not via `Bus.Publish`). | **Accepted — corrected**: the F5 detector is exclusively the new e2e's "exactly 1 row after PUT". v1 §4's F5 row is amended accordingly. | §4 F5 |
| (gt-2) | Latent prod hole: `listGovernanceAuditGaps` ignores `created_at` parse errors and `factFromGap` falls back to live `now` on zero `OccurredAt` — a live clock entering ID math (F9), unexercised by any test. | Accepted as **documented residual risk** (R-3); remediation explicitly out of scope for this test-only direction. | R-3 |
| (gt-3) | v1's verdict "F1–F11 all detected" was unfulfilled: F1/F4/F5(s3compat)/F6/F7/F8(PG) had no detector. | **Accepted** — the §4 matrix now maps every F to a detector that exists *after* this v2 lands, marking NEW items. | §4 |

---

## 1. Change set summary

| ID | File | Change |
|----|------|--------|
| P-1 | `internal/auditgovernance/redaction.go` + `internal/auditgovernance/http_test.go` | Add control-char predicate to `tenantSourceID` (~6 lines) + one unit test. **The only production-code change in this direction.** |
| T-1 | `internal/repository/audit_governance_factid_test.go` | Add `TestDeterministicFactID_GoldenValue` (absolute formula vectors). |
| T-2 | `internal/api/s3compat/audit_governance_e2e_test.go` (**new file**, ≤500 lines) | `TestS3CompatAuditGovernanceDeterministicFactID` (AC-1 + AC-2 + F5 detector + goldens) and `TestS3CompatAuditGovernanceCaptureInactive` (F4 negative). |
| T-3 | `internal/integration/audit_governance_postgres_test.go` | Add `TestPostgresAuditGovernancePruneReenqueueSameID` (GAP-2, live PG full loop). |
| D-1 | `docs/configuration.md` (row 267) | Replace the `AUDIT_GOVERNANCE_HMAC_KEY` guidance cell (share scope + rotation semantics). |

Nothing else: no migration, no exported symbol, no `openapi.json`, no `go.mod`, no other doc.

---

## 2. P-1 — tenant NUL/control-char predicate (finding c)

**Why:** the fact-ID frame `source \x00 tenant \x00 eventType \x00 originKind \x00 decimal \x00 decimal` is injective only if no field carries NUL. Every other field is structurally constrained (`source` = `"aero-vault." + base64url`; `eventType`/`action` = `safeAction` charset `[a-z0-9._:-]`; `originKind` = DB `CHECK IN ('admin','file')`; the last two are decimal digits). `tenant` is the sole unconstrained input — verified live that `tenantSourceID("acme\x00evil")` returns `(value, nil)` today. The framing itself is never transmitted (only the ID), and ingress already rejects NUL twice (net/http headers, Postgres TEXT), so this is defense-in-depth that makes the doc-comment claim at `audit_governance_factid.go:15-18` true, not a vulnerability fix.

**Exact change** — `internal/auditgovernance/redaction.go`, inside `tenantSourceID` (currently :43-50), after the existing trim-equality check:

```go
if strings.IndexFunc(tenant, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
	return "", ErrInvalidConfig
}
```

Rejecting all C0 controls (`< 0x20`, which includes NUL 0x00) + DEL (0x7f) is deliberately broader than NUL-only: any control character would likewise corrupt the NUL-framed byte stream's readability, and no legitimate tenant can contain one (JWT `ten` claim and tenant headers are non-empty strings; the DB tenant column is plain text). `factFromAudit`/`factFromEvent`/`factFromGap` ignore this error (`source, _ := …`) exactly as they already ignore the trim-equality error — a pathological tenant yields an empty source deterministically on *both* the atomic and gap paths, so ID convergence is unaffected.

**Doc-comment touch-up** — `internal/repository/audit_governance_factid.go:16-17`: change "tenant/action are constrained by the redactor's normalizers" to "tenant/action are constrained by the redactor's normalizers (tenantSourceID rejects control chars, safeAction restricts to `[a-z0-9._:-]`)".

**Unit test** — new func in `internal/auditgovernance/http_test.go` (where `TestTenantSourceIDIsKeyedOpaqueAndDomainSeparated` already lives):

```go
func TestTenantSourceIDRejectsControlChars(t *testing.T) {
	r, err := newRedactor("audit-governance-hmac-key-32-bytes-minimum")
	if err != nil { t.Fatal(err) }
	for _, tenant := range []string{"acme\x00evil", "acme\x01", "acme\tx", "acme\x7f"} {
		if _, err := r.tenantSourceID(tenant); err == nil {
			t.Fatalf("tenantSourceID(%q) accepted a control char", tenant)
		}
	}
	for _, tenant := range []string{"acme", "acme space", "default", "tenant.with._-chars"} {
		if _, err := r.tenantSourceID(tenant); err != nil {
			t.Fatalf("tenantSourceID(%q) rejected a valid tenant: %v", tenant, err)
		}
	}
}
```

Gate: `go test ./internal/auditgovernance/ -run 'TenantSourceID' -count=1`.

---

## 3. Absolute golden values (finding a)

**Derivation:** each vector below was produced by executing the *current* production code at HEAD `15763e2` (scratch `_test.go` files importing the real packages; run once, captured, removed — tree verified clean afterwards). Any future formula change — hash algorithm, truncation length, separator, field order, bucket semantics — breaks these literals loudly.

### 3.1 Formula vectors → `TestDeterministicFactID_GoldenValue`

New func in `internal/repository/audit_governance_factid_test.go` (package `repository_test`), table-driven over `repository.DeterministicFactID(source, tenant, eventType, originKind, originID, occurredAt)`:

| Vector | source | tenant | eventType | originKind | originID | occurredAt (UTC) | bucket | expected |
|---|---|---|---|---|---|---|---|---|
| A | `aero-vault.abc123` | `acme` | `tenant.status` | `admin` | 42 | `2026-08-08T01:17:41.123456789Z` | 1786151861 | `efb5b5b734546a54aa21f5f7949ef896` |
| B | `aero-vault.abc123` | `acme` | `tenant.status` | `admin` | 42 | `2026-08-08T01:17:42.123456789Z` (A + 1s) | 1786151862 | `c6e9cdbbbe8a7d15a31d2a03f5fa2fbc` |
| B′ | `aero-vault.abc123` | `acme` | `tenant.status` | `admin` | 42 | `2026-08-08T01:17:41.999999999Z` | 1786151861 | `efb5b5b734546a54aa21f5f7949ef896` (= A; pins second-bucket truncation) |
| C | `""` | `""` | `""` | `""` | 0 | zero `time.Time{}` | 0 | `7a13533df046f5ca96da3f9e8b6c0c7d` |

Assertion per row: `got := repository.DeterministicFactID(...); got != want → t.Fatalf` (plus the existing shape regexp). B′'s equality-with-A is the load-bearing truncation pin — it is what makes the "consistent formula change" visible even when every other test recomputes.

### 3.2 Production-shaped anchors → inside the e2e (T-2)

These pin the **test-local HMAC replica** (F1) *and* the formula together, absolutely, without a live DB row:

| Anchor | Inputs | Expected |
|---|---|---|
| D | key `"0123456789abcdef0123456789abcdef"` (= `string(testShareSecret)`), tenant `"default"`; replica = `"aero-vault." + base64.RawURLEncoding(HMAC-SHA256(key, "aero-vault/audit-governance/v1"\x00"default"\x00"source-system"\x00"default"\x00))` | `aero-vault.PE5txdoOQd0AhKXa_qH1g8c0l6kCKdGEPJpRNVqi1E8` |
| E | `DeterministicFactID(D, "default", "file.created", "file", 1, 2026-08-08T01:17:41Z)` | `3494289b9f82a731f3022b534a8b01de` |

E is asserted as a pure recompute (origin 1, fixed timestamp) inside the e2e — independent of the live row's clock — so the *combination* replica+formula is absolutely pinned. A drift in the replica's domain string, field order, NUL convention, prefix, or base64 variant, or any formula change, breaks D/E.

**Honest limit (documented, accepted):** a *coordinated* change in the input mapping (e.g., action naming `"file."+type`, `defaultTenant`, `SourcePrefix`) applied to both production and the test replica passes all tests — the REQ-5 input mapping is a frozen contract; cross-version ID stability matters to the receiver (R-1). Convergence is what is pinned, not cross-version stability.

---

## 4. F1–F11 acceptance matrix (re-verified; finding e)

Legend: ✅ = detector exists in the current tree today · **NEW** = detector lands with this v2 · ⚠️ = constraint/documented.

| F | Failure | Detector (after v2) | File / test | Status |
|---|---|---|---|---|
| F1 | source framing drifts (domain, NUL convention, prefix, base64 variant) | e2e absolute recompute + golden D | `audit_governance_e2e_test.go` **NEW**; `http_test.go` in-package (partial — cannot see coordinated drift, see §3.2) | **FIXED** |
| F2 | formula changes (hash, truncation, separators, bucket) | golden vectors A/B/B′/C + D/E | `audit_governance_factid_test.go` `TestDeterministicFactID_GoldenValue` **NEW**; e2e D/E **NEW**; existing relative tests keep pinning sensitivity/boundaries | **FIXED** (v1 claim was overstated — no absolute golden existed; the e2e alone would *not* have fixed it, it recomputes via the production function) |
| F3 | occurred canonicalization regresses (caller `now` stored instead of DB `created_at`) | `o.occurred_at_ns == e.created_at.UnixNano()` | e2e REQ-2.2 **NEW**; existing `GapEqualsAtomic_File` (`fact_id_test.go:104-148`), PG `InsertEventRoundTrip:144-151` | ✅ |
| F4 | capture inactive (binding missing/draining, revision drift) | **zero** outbox rows after PUT (negative: HTTP 200 + object stored + 1 `object_events` row) | `TestS3CompatAuditGovernanceCaptureInactive` **NEW** (draining binding) | **FIXED** (was s3compat-gap) |
| F5 | event path broken (sink nil, bus bypassed, wrapped repo not used) — `Bus.Publish` swallows errors, so a broken path yields HTTP 200 + zero rows; **this row assertion is the only detector** | **exactly 1** outbox row after PUT | e2e REQ-2.1 **NEW**. Amended: `authz_gate_test.go`'s `outboxCount` is *not* a detector (counts `event_outbox`, no governance wiring) | **FIXED** |
| F6 | prune/gap semantics drift (tombstone left by prune, gap query wrong) | gap count==1 + field match + re-enqueue byte-identical | e2e REQ-3.1-3.5 **NEW**; existing sqlite `PruneReenqueueSameID`; PG mirror **NEW** (T-3) | **FIXED** |
| F7 | dedupe regression (`UNIQUE`/`ON CONFLICT` dropped) | second enqueue → `(false, nil)` | e2e REQ-3.5 (optional) **NEW**; existing sqlite double-enqueue `(false,nil)` (`audit_governance_test.go:203-207`), PG tombstone guard; live-concurrency probe (db reviewer) | ✅ + **NEW** e2e |
| F8 | PG branch divergence (`::jsonb`, µs `flexTime`, claim SQL) | PG suite under `AERO_PG_DSN`; **T-3 exercises the PG `ListGaps` SQL on the prune path** (previously unexercised) | `TestPostgresAuditGovernanceInsertEventRoundTrip` (existing), `TestPostgresAuditGovernancePruneReenqueueSameID` **NEW** | **FIXED** |
| F9 | clock-bucket race (live `time.Now()` in expected-ID math) | design constraint — expected values always read from DB rows; `-count=5` stable; **latent prod hole documented in R-3** | all tests; review gate | ✅ (tests) / ⚠️ R-3 |
| F10 | wire identity drift (`ClaimAuditGovernance` ↔ `governanceWire.EventID/IdempotencyKey`) | claimed `fact.ID == pinned row ID` | e2e REQ-2.4 **NEW**; existing PG roundtrip + `cmd/server/governance_e2e_test.go`; `http.go:148,153` | ✅ + **NEW** e2e |
| F11 | format gates (gofmt, ≤500 lines/file, stdlib-only) | `make check` + `wc -l` on the new file (§7); note: Makefile's line gate excludes `*_test.go` — the new e2e complies anyway (R-5) | — | ⚠️ (see R-5) |

---

## 5. Precise implementation specs

### 5.1 GAP-1 — `internal/api/s3compat/audit_governance_e2e_test.go` (NEW file, package `s3compat`, ≤500 lines)

**Reused from the package** (do not redefine): `do` (`handler_test.go:39`), `allowAllProvider` (`authz_gate_test.go:28`), `testShareSecret` (`authz_gate_test.go:32`), the `_ "modernc.org/sqlite"` import. New imports: `crypto/hmac`, `crypto/sha256`, `database/sql`, `encoding/base64`, `io`, `log/slog`, `regexp`, `time`, `github.com/aero-vault/aero-vault/internal/auditgovernance`, `github.com/aero-vault/aero-vault/internal/config`, `github.com/aero-vault/aero-vault/internal/events`, `github.com/aero-vault/aero-vault/internal/repository`, `github.com/aero-vault/aero-vault/internal/service`, `github.com/aero-vault/aero-vault/internal/storage`. Logger: `slog.New(slog.NewTextHandler(io.Discard, nil))` for both the runtime and the bus (pattern: `authz_gate_test.go` `TestDeniedDeleteEmitsNoEvent`; `nil` also works — it defaults to `slog.Default()`).

**File layout + line budget (~340 total, hard ceiling 500):**

```
~20  imports + package
~8   const e2eGovernanceTenant = "default"  +  e2eFactIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
~10  func e2eSourceID(key, tenant string) string      // HMAC replica of tenantSourceID
~55  func newGovernanceE2EServer(t *testing.T, bindingState string) (*httptest.Server, repository.AuditGovernanceStore, string)
~40  func governanceOutboxRow(t *testing.T, dsn string, bucket, key string) (id string, originID int64, occurredNS int64, action, tenantID, createdRaw string)
~150 func TestS3CompatAuditGovernanceDeterministicFactID(t *testing.T)
~60  func TestS3CompatAuditGovernanceCaptureInactive(t *testing.T)
```

**`e2eSourceID`** — byte-for-byte replica of `tenantSourceID` + `writeMACFields` (`redaction.go:43-50,74-79`):

```go
func e2eSourceID(key, tenant string) string {
	mac := hmac.New(sha256.New, []byte(key))
	for _, field := range []string{"aero-vault/audit-governance/v1", tenant, "source-system", tenant} {
		mac.Write([]byte(field))
		mac.Write([]byte{0})
	}
	return "aero-vault." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
```

**`newGovernanceE2EServer`** — production-shaped wiring, mirrors `cmd/server/main.go:79-86` + `authz_gate_test.go:69-95`:

1. `dir := t.TempDir()`; `dsn := "file:" + filepath.Join(dir, "gov.db")`; `repository.Open(ctx, "sqlite", dsn)` + `Migrate`; `storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})`; `store := repo.(repository.AuditGovernanceStore)`.
2. `cfg := config.AuditGovernanceConfig{Enabled: true, BaseURL: "http://127.0.0.1:9", TokenURL: "http://127.0.0.1:9/token", HMACKey: string(testShareSecret), HTTPTimeoutSeconds: 1, PollMilliseconds: 10, BatchSize: 10, ClaimTTLSeconds: 3, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2, MaxLagSeconds: 4, ReconcileBatchSize: 20, DeliveredRetentionSeconds: 3600, CleanupIntervalSeconds: 60, CleanupBatchSize: 20, Revision: 1, Bindings: []config.AuditGovernanceBinding{{TenantID: e2eGovernanceTenant, ClientID: "vault-e2e", ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_E2E", ClientSecret: "e2e-secret", State: bindingState}}}` — loopback URLs pass `validateAuditGovernanceURL` (`127.0.0.1` is `IsLoopback()`) and are **never dialed** (`Runtime` is not `Start()`ed); the env name must carry the mandatory `AUDIT_GOVERNANCE_CLIENT_SECRET_` prefix (`validAuditSecretEnv`), and `ClientSecret` must differ from `HMACKey` (it does).
3. `runtime, err := auditgovernance.New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))` — `New` applies the binding to the DB (`applyDesiredBindings`), so `Capture("default")` reflects `bindingState`.
4. `wrapped := auditgovernance.WrapRepository(repo, runtime)`; `bus := events.New(wrapped, nil).WithRepository(wrapped)`; `svc := service.NewFileService(store, wrapped, nil).WithEventSink(bus).WithAuthorizer(allowAllProvider{})`; `srv := httptest.NewServer(NewRouter(svc, nil, allowAllProvider{}))`.
5. Cleanup (LIFO order = repo → runtime → srv): `t.Cleanup(func() { _ = repo.Close() })`, `t.Cleanup(func() { _ = runtime.Close() })` (safe pre-`Start`), `t.Cleanup(srv.Close)`.
6. Return `srv, store, dsn`. No `Runtime.Start()`, no goroutines, no receiver traffic — deterministic and timing-free.

**`governanceOutboxRow`** — second sqlite connection (`sql.Open("sqlite", dsn)`), exact SQL with sqlite `?` placeholders:

```sql
SELECT o.id, o.origin_id, o.action, o.tenant_id, o.occurred_at_ns, e.created_at
FROM audit_governance_outbox o JOIN object_events e ON e.id = o.origin_id
WHERE o.origin_kind='file' AND o.tenant_id='default' AND e.bucket=? AND e.key=?
```

Returns the six columns; a second `SELECT COUNT(*) FROM audit_governance_outbox` (whole table) for the count. `e.created_at` is the sqlite TEXT default `strftime('%Y-%m-%dT%H:%M:%fZ','now')` → parses under `time.RFC3339Nano`.

**`TestS3CompatAuditGovernanceDeterministicFactID`** — assertions, in order (each `t.Fatalf` on mismatch):

1. **F5 detector (the only one):** `do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil)` → 200; then `governanceOutboxRow` → exactly 1 row (`COUNT(*)==1`), `action == "file.created"`, `tenant_id == "default"`, `originID > 0`.
2. **F3 canonicalization:** `created, err := time.Parse(time.RFC3339Nano, createdRaw)`; assert `occurredNS == created.UnixNano()`.
3. **Golden D (F1+F2):** `expectedSource := e2eSourceID(string(testShareSecret), "default")`; assert `expectedSource == "aero-vault.PE5txdoOQd0AhKXa_qH1g8c0l6kCKdGEPJpRNVqi1E8"`.
4. **Golden E (F2, clock-free):** assert `repository.DeterministicFactID(expectedSource, "default", "file.created", "file", 1, time.Date(2026, 8, 8, 1, 17, 41, 0, time.UTC)) == "3494289b9f82a731f3022b534a8b01de"`.
5. **Absolute row recompute:** `expectedID := repository.DeterministicFactID(expectedSource, "default", "file.created", "file", originID, created)`; assert `id == expectedID` and `e2eFactIDPattern.MatchString(id)`.
6. **F10 wire identity:** `claimed, err := store.ClaimAuditGovernance(ctx, "e2e-owner", "e2e-token", 1, 10, time.Minute)`; `len(claimed)==1`, `claimed[0].ID == id`, `claimed[0].OriginID == originID`.
7. **AC-2 / F6:** via the second connection `DELETE FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?`; then `gaps, err := store.ListAuditGovernanceGaps(ctx, "default", 10)` → `len(gaps)==1`, `gaps[0].OriginKind=="file"`, `gaps[0].OriginID==originID`, `gaps[0].Action=="file.created"`, `gaps[0].OccurredAt.UnixNano()==occurredNS`.
8. **Re-enqueue (F6/F7):** `rebuilt := repository.AuditGovernanceFact{SourceID: expectedSource, TenantID: "default", OriginKind: "file", FactKind: "file", Action: gaps[0].Action, OriginID: gaps[0].OriginID, OccurredAt: gaps[0].OccurredAt}` (ID empty — `EnqueueAuditGovernance` recomputes store-authoritatively, `write.go:126-127`; mirrors `relay.go:27→38→40`); `inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)` → `inserted==true`; re-read → `COUNT(*)==1` and `id` **byte-identical**.
9. **F7 dedupe (optional but cheap):** `again, err := store.EnqueueAuditGovernance(ctx, rebuilt)` → `again==false && err==nil` (`ON CONFLICT (origin_kind,origin_id) DO NOTHING` + `RowsAffected()==0` → `(false,nil)`, `write.go:131-136,160`).

**`TestS3CompatAuditGovernanceCaptureInactive`** — F4 negative, same harness with `bindingState = "draining"` (valid per `validGovernanceBindingStates`; `Runtime.Capture("default")` false):

1. `do(t, "PUT", srv.URL+"/b/k.txt", []byte("hello"), nil)` → **200** (I5: capture must never break CRUD).
2. `do(t, "GET", srv.URL+"/b/k.txt", nil, nil)` → **200** (object stored).
3. `governanceOutboxRow` → **zero** outbox rows (`COUNT(*)==0`).
4. Distinguish "inactive capture" from "broken persistence": `SELECT COUNT(*) FROM object_events WHERE bucket='b' AND key='k.txt'` == **1** (the legacy `InsertEvent` path persisted the event; `auditedRepository.InsertEvent` short-circuits to `r.Repository.InsertEvent` when capture is off, `repository.go:31-35`).

### 5.2 GAP-2 — `TestPostgresAuditGovernancePruneReenqueueSameID` (extend `internal/integration/audit_governance_postgres_test.go`)

`//go:build integration`, `TestPostgres` prefix (runs under `make test-integration` and `.github/workflows/integration-pg.yml`'s `-run 'TestPostgres|TestPg'`). Reuses `freshRepo` and the `uuid` import already in the file. ~60 lines. This is the **full ListGaps → factFromGap-equivalent → Enqueue loop on live PG**: `factFromGap` is package-private, so the gap→fact reconstruction is performed inline with the *identical field mapping* (see note) and the ID identity is decided by `EnqueueAuditGovernance`'s store-authoritative recompute — which is exactly the property the receiver depends on.

```go
func TestPostgresAuditGovernancePruneReenqueueSameID(t *testing.T) {
	ctx := context.Background()
	repo, raw := freshRepo(t)
	store := repo.(repository.AuditGovernanceStore)
	tenant := "audit-pg-" + uuid.NewString()
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "digest-1",
		[]repository.AuditGovernanceBindingState{{TenantID: tenant, State: repository.AuditGovernanceBindingActive}}); err != nil {
		t.Fatalf("apply governance binding: %v", err)
	}
	const sourceID = "aero-vault.test-pg" // fixed literal — no HMAC needed; the store recomputes from fact.SourceID
	event := repository.Event{TenantID: tenant, Bucket: "default", Key: "pg-prune.txt",
		Type: repository.EventCreated, Payload: map[string]string{"size": "7", "backend": "local"}}
	fact := repository.AuditGovernanceFact{
		SourceID: sourceID, TenantID: tenant, FactKind: "file", Action: "file.created",
		OccurredAt: time.Time{}, // zero — store canonicalizes to the PG DB-default created_at (µs)
	}
	originID, err := store.InsertEventWithGovernance(ctx, event, fact)
	if err != nil || originID <= 0 {
		t.Fatalf("insert event with governance: id=%d err=%v", originID, err)
	}
	// Pre-prune outbox row.
	var preID string
	var preNS int64
	if err := raw.QueryRowContext(ctx, `SELECT id, occurred_at_ns FROM audit_governance_outbox
WHERE origin_kind='file' AND origin_id=$1 AND tenant_id=$2`, originID, tenant).Scan(&preID, &preNS); err != nil {
		t.Fatalf("read pre-prune outbox row: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(preID) {
		t.Fatalf("pre-prune id %q not 32-hex", preID)
	}
	// Prune (the T-4 bypass: no delivered-origin tombstone is written).
	if _, err := raw.ExecContext(ctx,
		`DELETE FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=$1`, originID); err != nil {
		t.Fatalf("prune outbox row: %v", err)
	}
	// Full gap path on live PG — exercises listGovernanceEventGaps' JOIN,
	// flexTime µs parse and "file." action prefix.
	gaps, err := store.ListAuditGovernanceGaps(ctx, tenant, 10)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps=%+v err=%v want=1", gaps, err)
	}
	if gaps[0].OriginKind != "file" || gaps[0].OriginID != originID ||
		gaps[0].Action != "file.created" || gaps[0].OccurredAt.UnixNano() != preNS {
		t.Fatalf("gap=%+v want kind=file origin=%d action=file.created occurred=%d", gaps[0], originID, preNS)
	}
	// factFromGap-equivalent reconstruction (same six ID inputs factFromGap
	// feeds DeterministicFactID; factFromGap itself is pinned on sqlite by
	// TestDeterministicFactID_GapEqualsAtomic_*).
	rebuilt := repository.AuditGovernanceFact{
		SourceID: sourceID, TenantID: tenant, OriginKind: "file", FactKind: "file",
		Action: gaps[0].Action, OriginID: gaps[0].OriginID, OccurredAt: gaps[0].OccurredAt,
	}
	inserted, err := store.EnqueueAuditGovernance(ctx, rebuilt)
	if err != nil || !inserted {
		t.Fatalf("re-enqueue: inserted=%v err=%v", inserted, err)
	}
	// Byte-identical ID, exactly one row, and the dedupe branch returns (false, nil).
	var postID string
	var count int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*), MAX(id) FROM audit_governance_outbox
WHERE origin_kind='file' AND origin_id=$1 AND tenant_id=$2`, originID, tenant).Scan(&count, &postID); err != nil {
		t.Fatalf("read re-enqueued row: %v", err)
	}
	if count != 1 || postID != preID {
		t.Fatalf("re-enqueued count=%d id=%q want count=1 id=%q", count, postID, preID)
	}
	if again, err := store.EnqueueAuditGovernance(ctx, rebuilt); err != nil || again {
		t.Fatalf("duplicate enqueue inserted=%v err=%v want (false,nil)", again, err)
	}
}
```

**Why the reconstruction is exact:** the ID depends only on the six formula inputs. Pre-prune `OccurredAt` was canonicalized from `RETURNING created_at` (µs); the gap's `OccurredAt` is parsed from the *same* stored `created_at` (`flexTime`, `sql_helpers.go:200-214`) — identical value, so `UnixNano()` equality (`preNS`) is asserted *before* re-enqueue. `Action` is re-prefixed identically (`listGovernanceEventGaps` sets `"file."+action`; the atomic path stored `fact.Action`). `SourceID`/`TenantID`/`OriginKind`/`OriginID` pass through unchanged. `EnqueueAuditGovernance` then recomputes the ID from these fields (`write.go:126-127`) — same inputs ⇒ same ID. The digest payload fields (`TargetDigest` etc.) are not ID inputs; their mapping is pinned by the sqlite `GapEqualsAtomic` tests and is out of this test's scope.

---

## 6. Explicit non-changes (v1 preserved)

- `tenantSourceID` stays unexported — the e2e replica remains the deliberate, only-possible external pin (now doubly anchored by golden D).
- No `Runtime.Start()`, no receiver POST, no clock in expected-ID math (F9).
- `uuid.NewString` stays at `relay.go:62` (claim token, never event identity).
- No migration, no `openapi.json`, no config key, no `go.mod` change (I6).
- Scope exclusions from v1 §6 unchanged: activation-gate matrix, terminal classification, relay metrics/degraded, receiver delivery counts.

---

## 7. Gate commands

```bash
# P-1 unit test (tenant predicate)
go test ./internal/auditgovernance/ -run 'TenantSourceID' -count=1

# T-1 golden formula values
go test ./internal/repository/ -run 'GoldenValue' -count=1

# T-2 GAP-1 e2e (AC-1 + AC-2 + F4 + F5)
go test ./internal/api/s3compat/ -run 'AuditGovernance' -count=1

# T-3 GAP-2 PG loop (auto-skips without AERO_PG_DSN; CI: integration-pg.yml)
AERO_PG_DSN="postgres://aero:aero@localhost:55432/aero?sslmode=disable" \
  go test -tags=integration ./internal/integration/ -run 'TestPostgresAuditGovernance' -count=1

# AC-3 sqlite regression set (existing)
go test ./internal/auditgovernance/ -run 'DeterministicFactID|GapEqualsAtomic|PruneReenqueue|NoUUID' -count=1

# Hard gates: whole-repo + the 500-line gate on the NEW file (Makefile excludes
# _test.go from its line scan, so this is explicit here per AGENTS.md)
make check
wc -l internal/api/s3compat/audit_governance_e2e_test.go   # must be ≤ 500
grep -n "uuid" internal/auditgovernance/facts.go           # no output (AC-3)
```

---

## 8. Residual risks (folded-in review findings)

**R-1 — HMAC key rotation is not idempotency-safe (security review b1).** `AUDIT_GOVERNANCE_HMAC_KEY` is an input to `source` ⇒ to every fact ID. Rotating it changes the ID of every not-yet-delivered fact. Delivered facts are protected (the local `delivered_origins` tombstone suppresses re-gap), but **in-flight facts — claimed-but-unacked, or pruned-then-re-enqueued — derive a new EventID under the new key, and the receiver double-ledgers the same origin**. Requirement: keep the key stable for at least `AUDIT_GOVERNANCE_DELIVERED_RETENTION_SECONDS`; rotate only with a drained outbox (zero pending/claimed rows), rotating on all replicas of a store atomically; if any pruned-but-undelivered origin must survive rotation, the receiver must dedupe on origin identity, not EventID. Documented in `docs/configuration.md:267` (D-1).

**R-2 — shared key ⇔ shared origin-counter store (security review b2).** "Identical on every replica" is only safe for replicas over the **same** DB (single origin counter + fenced claims). Two independent DBs sharing key + receiver + tenant names produce byte-identical EventIDs for `origin_id=1` events in the same second-bucket; the receiver folds the second as Duplicate → **silent ledger loss for one instance** (the one issue in the scheme that can lose audit events at scale). Never share the key across independent databases; if that topology is ever required, an instance/deployment identifier must be added to the source derivation (out of scope here). Documented in `docs/configuration.md:267` (D-1).

**R-3 — latent live-clock entry in ID math (go-testing review gt-2, F9 note).** `listGovernanceAuditGaps` ignores `created_at` parse errors (`gap.OccurredAt, _ = time.Parse(...)`) and `factFromGap` falls back to `now.UTC()` on zero `OccurredAt` — a malformed `audit_log.created_at` would put a live clock into ID computation, breaking cross-restart identity. Not exercised by any test (all tests feed parseable timestamps). Remediation (fail/repair on parse error) is out of scope for this test-only direction — flagged for a future direction.

**R-4 — coordinated-change blindness (F2 limit).** Golden values pin the formula and the test-local replica absolutely, but a symmetric change to the input mapping (action naming, `defaultTenant`, `SourcePrefix`) in both production and the replica passes all tests. The REQ-5 input mapping is a frozen contract; treat any change as a receiver-visible breaking change (R-1 applies).

**R-5 — line-gate precedent.** `make check`'s 500-line scan excludes `*_test.go`, so the pre-existing `authz_gate_test.go` (570 lines) is not CI-blocked but violates the AGENTS.md hard gate. Out of scope here; the new e2e complies (~340 lines, §5.1) and is explicitly `wc -l`-gated in §7. Do not grow it past 500 (split helpers into a second `_test.go` file if ever needed).

**R-6 — collision failure mode (security review sec-1).** A deliberate ID collision requires the HMAC key; there is no oracle (IDs never returned to clients). With the key, the whole redaction scheme is already compromised, so no new capability is granted. App-layer note: because `Bus.Publish` swallows errors, a collision/PK-violation manifests as a silently dropped governance capture (HTTP 200, no row) — the e2e's row assertion is also the detector for that class.

**R-7 — PG gate skip.** T-3 silently skips without `AERO_PG_DSN`; CI coverage depends on `integration-pg.yml` (same exposure as the existing 3 PG tests). Local verification command in §7.

---

## 9. Migration / rollback

No schema/data migration; `0039/0040/0042/0043` pairs (both dialects) unchanged. Codebase transition: P-1 (redaction.go + http_test.go), T-1, T-2 (new file), T-3, D-1. Rollback = revert P-1, T-1, T-2, T-3, D-1 — zero production surface beyond the 6-line predicate (which is itself a no-op for all valid tenants). Operators: no deployment action; `AUDIT_GOVERNANCE_*` opt-in behavior unchanged.
