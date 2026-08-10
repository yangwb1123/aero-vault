# Design: quarantine-shaped admin gap → scan-stable deterministic fact ID (T-4 pin)

> **Companion spec:** `docs/requirements/antivirus-quarantine-gap-factid-v1.md` (REQ-1/2/3, acceptance a/b/c) · **Module:** `internal/auditgovernance` (test-only) · **Status:** design (not implemented) · **Baseline:** HEAD `15763e2` + uncommitted working tree (all citations re-pinned against the working tree) · **Gates:** `make check` green · single file ≤ 500 lines · stdlib only (I6) · **zero production code changes** (pure test pin) · **zero DB migrations / zero wire-level changes** (I2; no REST/S3/MCP/OpenAPI/config surface touched)

---

## 1. Evidence re-verification (independent check against working tree)

Every citation in the requirements doc was re-checked directly. **All claims hold**; two line-precision nits (immaterial, corrected below). Empirical checks added: RFC3339Nano parse behavior and the seed-path table-write set.

| # | Requirement doc claim | Verified against working tree | Verdict |
|---|----------------------|-------------------------------|---------|
| E1 | `fact_id_test.go:63-178` — synthetic-only AC-1 pins, single `time.Now()` per `assertGapEqualsAtomic` | `fact_id_test.go` (207 lines): `assertGapEqualsAtomic` :65-81 passes exactly one `time.Now()` (:74); `…_Admin` :86-114 seeds `RecordAuditWithGovernance`; `…_File` :121-146 seeds `InsertEventWithGovernance`; `…_PruneReenqueueSameID` :152-190; `…_NoUUIDInFactsGo` :195-207. **No test touches the quarantine direct-SQL path** (`insertAuditEntry` via `SoftDeleteObjectByIDWithEvent`); no two-now re-scan exists anywhere | ✅ |
| E2 | `facts.go:15-20` — silent `now()` fallback | `facts.go:16-19`: `occurred, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); if err != nil \|\| occurred.IsZero() { occurred = now.UTC() }` — **no error log** | ✅ (parse at :16, fallback :17-19) |
| E3 | `factFromGap` drifted to :48-72, formula :66-69 | `facts.go:48-72`; `fact.OriginID = gap.OriginID` :66; single-call-site comment :67-69; `fact.ID = repository.DeterministicFactID(...)` :70-71 | ✅ (formula :70-71) |
| E4 | `DeterministicFactID` :10-38, second-truncate :31 | `audit_governance_factid.go:28-37`; `bucket := occurredAt.UTC().Truncate(time.Second).Unix()` **:32**; pure (no clock/random) | ✅ |
| E5 | store-authoritative recompute :38-44 | `audit_governance_write.go:35-39`: `RecordAuditWithGovernance` re-parses `entry.CreatedAt` (REQ-2 comment :32-34) then recomputes `fact.ID` | ✅ (:35-39) |
| E6 | `EnqueueAuditGovernance` :113-133, `rows==1` :131-132 | `audit_governance_write.go:111-137`; recompute :126-127; `insertAuditGovernanceResult(..., true)` :128; `return rows == 1, tx.Commit()` **:136** | ✅ |
| E7 | `ON CONFLICT (origin_kind,origin_id) DO NOTHING` :159-160 | `audit_governance_write.go:160`, appended only under `ignoreDuplicate` (`Enqueue` always passes `true`, :128) | ✅ |
| E8 | `insertAuditEntry` in `event_outbox.go:21-30` (wrong file) | **Corrected:** `internal/repository/audit.go:21-30` (RFC3339Nano stamp :23). Claim's substance holds: `SoftDeleteObjectByIDWithEvent` (`event_outbox.go:186-227`) calls it at **:220** | ✅ (file corrected) |
| E9 | quarantine row shape | `internal/service/object_worker.go`: `quarantineReason = "av_infected"` :41; `quarantineAuditEntry` :94-106 (Action `AuditActionFileDelete` :101, Target `bucket+"/"+key` :102, Detail `av_infected` :104); actor from principal, pinned `system:antivirus` by `internal/access/permissions.go:12` (`SystemActorAntivirus`) | ✅ |
| E10 | gap-scan swallow :258 | `audit_governance_write.go:258`: `gap.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)` — error discarded; full silent chain verified (facts.go:58-62 `createdAt=""` → :16 parse fail → :17-19 fallback) | ✅ |
| E11 | `relay.go:38` fresh clock per scan | `internal/auditgovernance/relay.go:38`: `fact := r.redactor.factFromGap(gap, time.Now().UTC())` | ✅ |
| E12 | seed-surface public on `Repository` | `repository_interface.go:33` `SoftDeleteObjectByIDWithEvent`; `:18` `UpsertObject`; `:175` `RecordAudit`; `:176` `ListAudit`; `ListAudit` scans `id` into `AuditEntry.ID` (`audit.go:62`, `repository.go:294`). **But none on `AuditGovernanceStore`** (`audit_governance_types.go:88-106`) — see §2.2 A | ✅ (+ finding) |

**Independent empirical checks added by this design:**

| Check | Result |
|-------|--------|
| `time.Parse(RFC3339Nano, "2026-08-08 01:17:41.123456789+00:00")` (space-separated) | **fails** (`cannot parse " 01:17:41..." as "T"`) → valid negative control |
| `time.Parse(RFC3339Nano, "2026-08-08T01:17:41Z")` (T + truncated) | **parses** → correctly rejected as negative control by REQ-3 |
| RFC3339Nano round-trip (`Format` after `Parse`) | byte-stable → gap-path re-format/re-parse is lossless for parseable rows |
| `SoftDeleteObjectByIDWithEvent` write set | `audit_log` (1 row, via `insertAuditEntry` :220) + `event_outbox` (facts, `insertOutboxFacts` :223). **Does not write `object_events`** → gap scan (`listGovernanceAuditGaps` :235-262) sees exactly **one** admin gap; no file-gap interference. REQ-1's "exactly 1 gap" holds |
| `event_outbox` rows do not suppress the admin gap | gap SQL LEFT JOINs only `audit_governance_outbox`/`audit_governance_delivered_origins` (:239-242) — verified |
| `validDeleteFacts` importability | `internal/repository/event_outbox_test.go:54` is **package-private to `repository`** — not importable from `package auditgovernance`; local builder required (§2.2 C) |
| `AuditEntry.ID` populated by `ListAudit` | `audit.go:62` scans `id` into `e.ID` (`repository.go:294`) → negative-control gap locatable by `OriginID` |
| `fact_id_test.go` current size | 207 lines; ~170-line addition lands ≈ 377 < 500 gate ✓ |

**Conclusion:** the spec's problem statement and acceptance design are sound; the design below adds the two structural accommodations above (seed-surface access, local fact builder) and fixes one ambiguity (negative-control isolation via a separate test function on a fresh store).

---

## 2. API changes

### 2.1 Production (wire-level / Go-level / config / schema) — **none**

No production file is modified. `git status` after implementation must show only the test file (plus this doc and the spec). This is the requirement's hard scope boundary: store-side error propagation, staleness metrics, and EICAR end-to-end are B3-4/B3-6 directions, not this pin.

### 2.2 Test-surface changes — one file, two new tests, two local helpers

All changes in `internal/auditgovernance/fact_id_test.go` (package `auditgovernance`):

**A. Seed access via type assertion (zero harness churn).** `factIDStore` returns `repository.AuditGovernanceStore`; the dynamic type is `*sqlStore`, which implements both `AuditGovernanceStore` **and** `Repository`. New tests obtain the seed surface with:

```go
repo := store.(repository.Repository) // dynamic type *sqlStore; never fails in practice
```

No change to `factIDStore`/`newRedactor`/`factIDHMACKey`/`factIDPattern`/`SourcePrefix` — all existing.

**B. Local seed helper `seedQuarantineGap`** — replicates the production quarantine write via the real producer surface (mirrors `event_outbox_test.go:32` `UpsertObject` + `event_outbox_test.go:450` `SoftDeleteObjectByIDWithEvent`, call at `:457`):

```go
func seedQuarantineGap(t *testing.T, store repository.AuditGovernanceStore) (repository.Object, repository.AuditEntry) {
    t.Helper()
    repo := store.(repository.Repository)
    ctx := context.Background()
    obj, err := repo.UpsertObject(ctx, repository.Object{
        TenantID: "acme", Bucket: "b", Key: "k", VersionID: "v-1",
        Backend: "local", StorageKey: "acme/b/k@v-1", Size: 42,
        ETag: "etag-1", ContentType: "text/plain",
    })
    if err != nil { t.Fatalf("seed object: %v", err) }
    entry := repository.AuditEntry{
        TenantID: "acme", Actor: "system:antivirus", // FR-4 shape; literal pins the stored shape
        Action: repository.AuditActionFileDelete, Target: "b/k", Detail: "av_infected",
    }
    if err := repo.SoftDeleteObjectByIDWithEvent(ctx, obj.ID, entry, quarantineDeleteFacts(obj, "acme")); err != nil {
        t.Fatalf("quarantine seed: %v", err)
    }
    return obj, entry
}
```

**C. Local fact builder `quarantineDeleteFacts`** — `validateOutboxFacts` (`event_outbox.go:61-83`) requires ≥1 fact with `schema_version == "1.1"` payload and valid event type; the repository-package `validDeleteFacts` is not importable, so the shape is rebuilt locally (only `schema_version` is validated at insert; payload is stored byte-exact):

```go
func quarantineDeleteFacts(obj repository.Object, tenant string) []repository.OutboxFact {
    deleted := fmt.Sprintf(`{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":%q,"bucket":%q,"key":%q,"object_id":%d}`,
        tenant, obj.Bucket, obj.Key, obj.ID)
    notify := fmt.Sprintf(`{"schema_version":"1.1","event_type":"vault.file.notify@1.1","tenant":%q,"bucket":%q,"key":%q}`,
        tenant, obj.Bucket, obj.Key)
    return []repository.OutboxFact{
        {EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(deleted)},
        {EventType: repository.EventTypeFileNotify11, OriginID: obj.ID, TenantID: tenant, Payload: []byte(notify)},
    }
}
```

**D. Test 1 — `TestDeterministicFactID_QuarantineGapScanStable` (REQ-1 + REQ-2 + REQ-3a).**

```go
func TestDeterministicFactID_QuarantineGapScanStable(t *testing.T) {
    ctx := context.Background()
    store := factIDStore(t)                       // harness: sqlite + Migrate + acme binding (revision 1)
    repo := store.(repository.Repository)         // seed surface lives on Repository only
    redactor, err := newRedactor(factIDHMACKey)
    if err != nil { t.Fatalf("new redactor: %v", err) }
    obj, _ := seedQuarantineGap(t, store)

    nowA := time.Now()
    nowB := nowA.Add(5 * time.Minute)             // hard constraint: different second bucket

    // ── REQ-1: scan stability before any enqueue ──
    gapsA, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
    if err != nil || len(gapsA) != 1 { t.Fatalf("gapsA=%+v err=%v want exactly 1 (quarantine admin gap)", gapsA, err) }
    if gapsA[0].OriginKind != repository.AuditOriginAdmin { t.Fatalf(...) }
    if gapsA[0].OriginID != obj.ID { t.Fatalf("gap origin %d != audit row %d", gapsA[0].OriginID, obj.ID) }

    // independent cross-check of the parse-success path (REQ-3a): fetch the row
    // via the public ListAudit surface, parse created_at test-side
    rows, err := repo.ListAudit(ctx, 10)
    // locate by (actor, action, detail) — don't assume order
    parsed, err := time.Parse(time.RFC3339Nano, row.CreatedAt)   // must succeed
    if !gapsA[0].OccurredAt.Equal(parsed) { t.Fatalf("store parse diverged from test parse") }

    factA := redactor.factFromGap(gapsA[0], nowA)
    gapsB, err := store.ListAuditGovernanceGaps(ctx, "acme", 10) // still a gap: nothing enqueued yet
    factB := redactor.factFromGap(gapsB[0], nowB)

    if factA.ID != factB.ID { t.Fatalf("gap ID %q (nowA) != %q (nowB): scan must be stable", factA.ID, factB.ID) }
    if !factIDPattern.MatchString(factA.ID) { t.Fatalf(...) }
    if factA.OccurredAt.Equal(parsed) == false || factA.OccurredAt.Equal(nowA.UTC()) {
        t.Fatalf("now() fallback consulted: OccurredAt=%v parsed=%v nowA=%v", factA.OccurredAt, parsed, nowA.UTC())
    }
    want := repository.DeterministicFactID(factA.SourceID, "acme",
        repository.AuditActionFileDelete, repository.AuditOriginAdmin, gapsA[0].OriginID, parsed)
    if factA.ID != want { t.Fatalf("gap ID %q != formula recompute %q", factA.ID, want) }

    // ── REQ-2: enqueue dedupe ──
    inserted1, err := store.EnqueueAuditGovernance(ctx, factA)
    if err != nil || !inserted1 { t.Fatalf("first enqueue: inserted=%v err=%v want true", inserted1, err) }
    inserted2, err := store.EnqueueAuditGovernance(ctx, factB)
    if err != nil || inserted2 { t.Fatalf("second enqueue: inserted=%v err=%v want false (ON CONFLICT)", inserted2, err) }
    claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 1, time.Minute)
    if err != nil || len(claimed) != 1 || claimed[0].ID != factA.ID {
        t.Fatalf("claimed=%+v err=%v want exactly 1 row with ID %q", claimed, err, factA.ID)
    }
}
```

**E. Test 2 — `TestDeterministicFactID_QuarantineGapParseFallbackLoud` (REQ-3b), on a fresh store** (separate `factIDStore(t)` instance ⇒ zero pollution of Test 1's "exactly 1 gap" assertion; simpler than in-function ordering):

```go
func TestDeterministicFactID_QuarantineGapParseFallbackLoud(t *testing.T) {
    ctx := context.Background()
    store := factIDStore(t)
    redactor, _ := newRedactor(factIDHMACKey)
    repo := store.(repository.Repository)
    const drifted = "2026-08-08 01:17:41.123456789+00:00" // space-separated: RFC3339Nano parse MUST fail
    if err := repo.RecordAudit(ctx, repository.AuditEntry{
        TenantID: "acme", Actor: "system:antivirus", Action: repository.AuditActionFileDelete,
        Target: "b/k", Detail: "av_infected", CreatedAt: drifted,
    }); err != nil { t.Fatalf("record drifted audit: %v", err) }
    // non-empty CreatedAt is stored verbatim (RecordAudit audit.go:32-41;
    // stamps only an empty CreatedAt, :33-35) — the drifted shape survives

    rows, _ := repo.ListAudit(ctx, 10)
    var originID int64 // locate the negative-control row by CreatedAt, not order
    // ... find row.ID where row.CreatedAt == drifted ...
    gaps, err := store.ListAuditGovernanceGaps(ctx, "acme", 10)
    var gap *repository.AuditGovernanceGap // filter by OriginID == originID
    if !gap.OccurredAt.IsZero() { t.Fatalf("store-level parse failure must be observable as zero OccurredAt (write.go:258 swallow)") }

    nowA := time.Now(); nowB := nowA.Add(5 * time.Minute)
    factA := redactor.factFromGap(*gap, nowA)
    factB := redactor.factFromGap(*gap, nowB)
    if factA.ID == factB.ID {
        t.Fatalf("unparseable created_at %q made the fact ID clock-dependent; format drift must fail here, not silently mint a second ID", drifted)
    }
    if !factA.OccurredAt.Equal(nowA.UTC()) || !factB.OccurredAt.Equal(nowB.UTC()) {
        t.Fatalf("fallback did not stamp the scan clock: factA=%v nowA=%v factB=%v nowB=%v",
            factA.OccurredAt, nowA.UTC(), factB.OccurredAt, nowB.UTC())
    }
}
```

---

## 3. Compatibility constraints

1. **Zero production surface** — no interface, schema, HTTP, config, or dependency change (I2/I5/I6). The only gate-sensitive file is `fact_id_test.go` (207 → ≈ 377 lines, ≤ 500 ✓).
2. **Hard constraint: `nowA`/`nowB` must be in different second buckets** (`nowB = nowA + 5min` guarantees it deterministically). `DeterministicFactID` truncates to the second (:32); same-bucket nows would let a fallback-driven ID coincide with the parse-driven ID and mask the regression — the exact hole the spec documents in E1/E12.
3. **Negative-control shape is fixed** to the space-separated `"2026-08-08 01:17:41.123456789+00:00"` (SQLite's default `datetime` text shape). `"2026-08-08T01:17:41Z"` parses under RFC3339Nano (empirically verified) and is explicitly **not** a control.
4. **Seed shape is fixed** to `(actor="system:antivirus", action="file.delete", target="b/k", detail="av_infected")` with empty `CreatedAt` (so `insertAuditEntry` stamps RFC3339Nano — the realistic production shape from `quarantineAuditEntry`).
5. **Harness reuse only**: `factIDStore` (SQLite + Migrate + acme binding revision 1), `newRedactor(factIDHMACKey)`, `factIDPattern`, `SourcePrefix`. Claim calls pass `revision=1` to match the harness binding (claim SQL joins `audit_governance_bindings b ON b.tenant_id=o.tenant_id AND b.revision=$N`).
6. **Test isolation**: Test 2 runs on its own store — no ordering dependency, no shared state; each test is independently `go test -run`-able.
7. **No wall-clock coupling**: assertions only compare against `nowA`/`nowB` passed into `factFromGap` (the fallback stamps exactly the passed now — `facts.go:18` `now.UTC()`), never against a fresh `time.Now()` inside an assertion.
8. **Assertion surface is public-only** (`ListAuditGovernanceGaps`, `EnqueueAuditGovernance`, `ClaimAuditGovernance`, `ListAudit`); no direct SQL in tests, no `_test.go` DB access outside the harness.

## 4. Failure modes

| # | Failure | Observable effect | Who catches it |
|---|---------|-------------------|----------------|
| FM-1 | `created_at` stamping drifts to a space-separated shape (e.g., someone replaces RFC3339Nano with SQLite `datetime('now')` at `audit.go:23` or `event_outbox.go:220`) | gap parse fails → fallback mints a clock-dependent ID; outbox dedupe then diverges across reconcile scans → **duplicate POSTs to the audit sink** | REQ-1 red (cross-check parse fatal; `!Equal(now)` fatal) **and** REQ-3 red (ID divergence across nows is the loud signal — the test's `t.Fatalf` is the designed alarm, since the store silently swallows at `write.go:258`) |
| FM-2 | `DeterministicFactID` formula changed (bucket granularity, field order, hash) | gap path, atomic path, and store recompute all call the same function → path convergence **preserved**; only absolute ID values change | Not caught by this pin (scope boundary): the pin guards **path convergence**, not formula absoluteness. Any formula change must come with its own review |
| FM-3 | Same-second-bucket masking reintroduced by a future test edit (single now, or `nowB = nowA + 100ms`) | fallback passes silently | REQ-1's `!Equal(now)` + REQ-3's loud divergence both still red on real drift; the doc comment on `nowB` marks the constraint for review |
| FM-4 | Enqueue dedupe regressed (ON CONFLICT dropped, unique constraint removed, `ignoreDuplicate` flipped) | second enqueue inserts a second outbox row → duplicate delivery on next claim | REQ-2 red: `inserted2==false` fatal + claim count/ID fatal |
| FM-5 | Seed path changes (e.g., `SoftDeleteObjectByIDWithEvent` starts writing `object_events`) | gap list gains a file gap → "exactly 1 gap" fails | REQ-1 red — **designed-loud**: the assertion documents the write-set assumption (verified in §1); a deliberate change must update the test consciously |
| FM-6 | Binding revision drift / claim signature change | `ClaimAuditGovernance` returns 0 rows or errors | REQ-2 step 3 red; harness revision is pinned by existing tests too |
| FM-7 | `quarantineDeleteFacts` payload shape breaks `validateOutboxFacts` (schema_version, event type, 1 MiB) | seed call fails inside the tx → whole seed rolls back | Test 1/2 fatal at `seedQuarantineGap` — the delete transaction's all-or-nothing is itself re-pinned |

**Net risk statement:** the two tests fail loudly on the two production failure paths the requirements target (format drift into the silent fallback; dedupe regression), and every failure is attributable to exactly one production behavior with a message naming it.

## 5. Migration steps

No data migration, no schema migration, no rollout ordering (test-only). Implementation sequence:

1. **Add helpers + tests** to `internal/auditgovernance/fact_id_test.go` (§2.2 B–E). Keep the file under 500 lines.
2. **Targeted run:** `go test ./internal/auditgovernance/ -run 'DeterministicFactID' -count=1` — all 5 tests green (3 existing matching the filter + 2 new; the 4th existing test `TestNoUUIDInFactsGo` does not match the filter). The new tests must pass **without touching any production file** — that is the pin's core assertion (the current tree already behaves correctly).
3. **Negative-control sanity:** temporarily flip the drifted shape to `"2026-08-08T01:17:41Z"` and confirm Test 2 reds — the shape parses at `write.go:258` → `gap.OccurredAt` is non-zero → the `gap.OccurredAt.IsZero()` **precondition fatal fires first** (the ID-divergence fatal is never reached; either way the control is provably sensitive). Revert. This proves the control actually exercises the fallback.
4. **Full gates:** `make check` (gofmt/build/vet/test) and `git status` — only `fact_id_test.go` modified among Go files; no `go.mod` diff; `make tidy` untouched.
5. **Commit:** test + this design + the companion spec. No backport needed (behavior is already correct; the pin is forward-looking regression insurance).

## 6. Testable acceptance mapping

| Acceptance (spec §3) | Test | Concrete assertions (each a `t.Fatalf` with failure semantics) |
|----------------------|------|----------------------------------------------------------------|
| (a) two scans with different nows → byte-identical ID == `DeterministicFactID` recomputed from DB fields, before any enqueue | `TestDeterministicFactID_QuarantineGapScanStable` | `len(gapsA)==1` · `OriginKind=="admin"` · `OriginID==obj.ID` · `gapsA[0].OccurredAt.Equal(parsed)` (independent `ListAudit`-side parse) · `factA.ID==factB.ID` · `^[0-9a-f]{32}$` · `factA.ID == DeterministicFactID(factA.SourceID, "acme", "file.delete", "admin", gapsA[0].OriginID, parsed)` · both scans before any `EnqueueAuditGovernance` |
| (b) `EnqueueAuditGovernance` twice → inserted true then false; exactly one outbox row via public claim surface with stable ID | same test, second phase | `inserted1==true && err==nil` · `inserted2==false && err==nil` (RowsAffected 1→0 via `write.go:136`) · `ClaimAuditGovernance(...,1,1,...)` → `len==1 && claimed[0].ID==factA.ID` |
| (c) RFC3339Nano shape proves parse-success (no fallback); truncated shape fails **loudly** | REQ-3a: same test (`OccurredAt.Equal(parsed) && !Equal(nowA.UTC())`); REQ-3b: `TestDeterministicFactID_QuarantineGapParseFallbackLoud` | parse-success: `factA.OccurredAt.Equal(parsed)==true && factA.OccurredAt.Equal(nowA.UTC())==false` · negative control: `gap.OccurredAt.IsZero()==true` (store swallow observable) · `factBadA.ID != factBadB.ID` with explicit `t.Fatalf` naming the clock-dependence · `factBadA.OccurredAt.Equal(nowA.UTC()) && factBadB.OccurredAt.Equal(nowB.UTC())` (fallback provenance) |

**Gate self-check:** `make check` green · no production file touched · `go test ./internal/auditgovernance/ -run DeterministicFactID` green · negative-control sanity flip proven red then reverted (§5 step 3) · `fact_id_test.go` ≤ 500 lines.
