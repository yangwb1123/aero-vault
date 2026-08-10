# Requirements Specification — `internal/ai` + `cmd/server`: AI-wired governance e2e (indexer consumption + usage recording concurrent with the relay; B3-5 emission-site gate)

**Direction:** "Extend the B3-5/B3-6 matrix-provisioned e2e to AI-wired flows: first-event e2e with indexer consumption + usage recording running concurrently"

**Module:** `internal/ai` (surface: `cmd/server/governance_ai_wired_e2e_test.go`, same package as the existing matrix harness)

---

## 1. Evidence verification

Every citation in the direction was re-checked against this checkout.

| Direction claim | Verdict |
|---|---|
| "`newGovernanceE2E` — no AI wiring; REQ-1 activation-gate test" | ✅ **holds** — `cmd/server/governance_e2e_test.go:182` (`newGovernanceE2E`), REQ-1 test at `:360-394`; the file contains **zero** `internal/ai` references (grep count = 0). Harness = FileService + EventBus + `auditgovernance.WrapRepository` only. |
| "`internal/ai/indexer.go:143-179` (event consumer + MarkEventConsumed)" | ✅ **holds** — `Run` at `:143` (drain backlog → select on live `sub` → `handle`), `drainBacklog` at `:164` (`NextUnconsumedEvents`), `handle` at `:174` = `processEvent` + `MarkEventConsumed` (`:179`). |
| "`internal/ai/search.go:387-410` (usage recording on the same tenant)" | ✅ **holds** — `recordUsage` at `:387-410`; `repo.RecordUsage` call at `:462`; failure is warn-only (`"audit usage failed"`). `RecordUsage` → `internal/repository/sql_chunks.go:115`. |
| "`internal/repository/audit_governance_write.go` (InsertEventWithGovernance atomic capture)" | ✅ **holds** — `InsertEventWithGovernance` at `:53-94`: single tx inserts `object_events` **and** the outbox row (`insertAuditGovernance`), computes `DeterministicFactID` store-authoritative, `tx.Commit()` at the end. |
| "`cmd/server/main.go:82,212` (WrapRepository ordering)" | ✅ **holds** — `repo = auditgovernance.WrapRepository(repo, auditRuntime)` at `:82` (`run()`, followed by `bus.WithRepository(repo)` at `:83`) and `:212` (`runMCP()`, followed by `events.NewWithBuffer(repo,…)` at `:215`). Both assembly sites point the bus at the wrapped repo **before** FileService/AI construction. |
| "claim/lag predicates `delivered_at_ns=0 AND failed_at_ns=0`, `audit_governance_claim.go:78,218`" | ✅ **holds** — claim query at `:78`, `OldestPendingAuditGovernance` (lag) at `:218`; both use the identical predicate. |
| "no grep/CI check enumerates event-emission sites" | ✅ **holds** — production `.InsertEvent(` call sites are exactly `internal/events/bus.go:84` (bus `Publish`) and `internal/auditgovernance/repository.go:41` (wrapper pass-through; definition at `internal/repository/sql_events.go:9`); **no test scans for them**. Source-scan precedent exists (`readyz_drill_test.go:332-369`, `internal/auditgovernance/fact_id_test.go:200`), so the gate is idiomatic. |

**Seam facts discovered while verifying (feed the requirements):**

- `MarkEventConsumed` (`sql_events.go:96-107`) updates **only** `object_events.consumed_at` — it never touches `audit_governance_outbox`; the wrapper (`internal/auditgovernance/repository.go:24-43`) overrides only `RecordAudit` + `InsertEvent`, so `NextUnconsumedEvents`/`MarkEventConsumed`/`RecordUsage` pass through unwrapped. The claim/lag predicates therefore cannot be disturbed by indexer consumption — that is exactly what T-3 must pin at the seam.
- `AI_INDEX_ENABLED` → `config.AI.Enabled` (`internal/config/config.go:146`) → `buildEmbedder` returns nil when disabled (`cmd/server/build.go:144-146`) → `buildAIComponents` gated on `embedder != nil` (`main.go:132-133`) → `buildIndexer` (`cmd/server/ai.go:32`, `:105-129`): `bus.Subscribe()` (buffered 64, `internal/events/bus.go:115`) + `go indexer.Run(systemCtx, idxSub)`.
- Deterministic in-process AI components for the harness (AGENTS.md test-mode patterns): `ai.NewHashEmbedder(dim)` (`embedder.go:37`, name `hash-<dim>` matches write/query embed models), `ai.NewSearch(repo, emb, logger)` (`search.go:37`, default `repoVectorIndex`), `ai.NewIndexer(repo, store, ext, nil, emb, logger)` (`indexer.go:115`, nil chunker → `NewChunker()`), `ai.NewDefaultExtractor()` (`extractor.go:30`). Chunks persist via `InsertChunks` (`sql_chunks.go:16`; table `chunks`, migration `0004_ai.up.sql:1`); usage persists to `ai_usage` (`0004_ai.up.sql:19`).

---

## 2. Feasibility facts verified

- The harness constructs the bus with the wrapped repo and re-points it (`events.New(wrepo, logger)` + `bus.WithRepository(wrepo)`), so an indexer built on the **same wrapped repo** consumes exactly the events the relay delivers; live delivery is via `bus.Subscribe()` (broadcast after `Publish` persists through the wrapper's `InsertEvent` → `InsertEventWithGovernance`). The 5 s `pollEvery` (`indexer.go:135`) is irrelevant to the tests — `Run` selects on the live sub first.
- `ai.Request` (`search.go:93-101`) takes `Tenant/Bucket/Query/K/Mode/Caller/ReqID`; `validate` defaults `K=10`, `Mode="vector"`; vector mode requires a non-nil embedder (provided) and searches the repo chunks via `repoVectorIndex` — the same chunks the indexer inserted.
- `ai_usage` assertion columns: `tenant_id, caller, query, request_id, object_ids` (JSON text) — raw-sqlite `?`-placeholder reads per I1, mirroring `outboxRow`/`eventRowID`.
- Existing helpers (`putObject`, `eventRowID`, `outboxRow`, `waitForRow`, `quiesce`, `rowFor`, `wantFactID`, `startRelay`, `govHarness`) are package-`main` and directly reusable — no duplication.

---

## 3. Requirements

New file `cmd/server/governance_ai_wired_e2e_test.go` (package `main`; ≤ 500-line gate — the three tests + helper stay ~300; reuses every `governance_e2e_test.go` helper). Zero production/schema/dependency footprint; all SQL sqlite `?` (I1); stdlib-only (I6).

### REQ-1 — AI-wired harness variant `newGovernanceE2EWithAI(t *testing.T, mode string) *govHarness`

Identical to `newGovernanceE2E` (`governance_e2e_test.go:182-250`) through the wrapped-repo/bus/FileService step, then, mirroring `main.go:132-133` → `ai.go:105-129`:

1. `emb := ai.NewHashEmbedder(8)` (deterministic; `hash-8` on both write and query sides).
2. `indexer := ai.NewIndexer(wrepo, store, ai.NewDefaultExtractor(), nil, emb, logger)` — **`wrepo`** (the same wrapped repository the bus holds; `NextUnconsumedEvents`/`MarkEventConsumed` pass through the wrapper, which is the point of the pin).
3. `search := ai.NewSearch(wrepo, emb, logger)`.
4. `sub, cancel := bus.Subscribe()`; `go indexer.Run(context.Background(), sub)`; `t.Cleanup(cancel)` — indexer **running before any PUT** (production order: indexer starts with the server, relay claims after the event exists).
5. Return the `govHarness` (unchanged struct) plus the `search` handle (extend the struct with `search *ai.Search`).

`AI_INDEX_ENABLED` naming note (documented, not asserted through env): the harness is literal-config by design (sibling-spec D2); the gate's production meaning is `config.AI.Enabled` → `buildEmbedder` non-nil → `buildIndexer` (main.go:132-133), and the helper wires exactly those components.

### REQ-2 — T-3: `TestGovernanceE2EAIWiredIndexerConsumption` (mode `202-echo`)

Pins that the bound-tenant **first** PUT yields exactly one outbox row + exactly one relay POST while `MarkEventConsumed` runs concurrently, and that the claim/lag predicates (`audit_governance_claim.go:78,218`) are unaffected.

- **Phase A (indexer only, deterministic B1 snapshot):** `putObject(t, h.svc, e2eTenant, "wired.txt")`; `eventRowID`; assert `COUNT(*) FROM audit_governance_outbox WHERE origin_kind='file' AND origin_id=?` **== 1** and the pending snapshot `(tenant, origin kind, attempts==0, deliveredAtNS==0, failedAtNS==0, availableAtNS!=0, claimOwner=="")` — the indexer consumed nothing yet, but its `Run` loop is live; the outbox capture inside `InsertEventWithGovernance`'s tx is unaffected.
- **Phase B (concurrent consumption, relay unstarted):** `waitForRow`-style positive wait (10 s deadline) on the **event** row: `consumed_at IS NOT NULL` via `SELECT consumed_at FROM object_events WHERE id=?`; assert ≥ 1 `chunks` row for the object (`SELECT COUNT(*) FROM chunks WHERE object_id=?`). At this point `MarkEventConsumed` has completed **while the outbox row is still pending** — assert the row is still exactly one and still `deliveredAtNS==0 AND failedAtNS==0` (claim/lag predicates unchanged), and `OldestPendingAuditGovernance` returns a value (lag query still sees the row: `claim.go:218` predicate still matches).
- **Phase C (relay + indexer concurrent):** `startRelay(t, h.rt)`; `waitForRow(deliveredAtNS>0 && failedAtNS==0 && attempts==1 && claimOwner=="" && lastError=="")` (claim predicate `claim.go:78` produced exactly one claim); `quiesce(50ms, postCount==1)`; `tokenCalls==1`; `COUNT(*)==1` again; `row.id == wantFactID(t, h.dsn, h.receiver.source, obj.ID)` (T-4 formula reuse); `OldestPendingAuditGovernance` now empty (delivered row excluded from lag).

### REQ-3 — T-4: `TestGovernanceE2EAIWiredSearchUsageRePin` (mode `202-echo`)

Re-pins `wantFactID` (`governance_e2e_test.go:296-328`) with a **live `search.Query` executed between PUT and relay delivery**, exercising usage recording on the same tenant while the outbox row is pending.

- Harness: `newGovernanceE2EWithAI(t, "202-echo")`; PUT; wait for indexer consumption (Phase B of REQ-2: `consumed_at IS NOT NULL`, chunks exist).
- **Between PUT and delivery** (relay still unstarted): `hits, err := h.search.Query(ctx, ai.Request{Tenant: e2eTenant, Bucket: "default", Query: "x", K: 10, Mode: "vector", Caller: "e2e:search", ReqID: "e2e-usage-1"})` — assert `err == nil`, ≥ 1 hit with `ObjectID == obj.ID` (deterministic: `HashEmbedder` encodes query and the 1-byte chunk to the same padded shingle vector), and exactly **one** `ai_usage` row: `SELECT COUNT(*) FROM ai_usage WHERE tenant_id=? AND caller=? AND query=? AND request_id=?` == 1, `object_ids` JSON contains `obj.ID` (`sql_chunks.go:115` write through the wrapper, which does not override `RecordUsage` — documented pass-through).
- Then `startRelay`; `waitForRow(delivered, attempts==1, failedAtNS==0)`; `quiesce(50ms, postCount==1)`; **`row.id == wantFactID(t, h.dsn, h.receiver.source, obj.ID)`** — the re-pin: usage recording + chunk reads on the same tenant neither perturb the outbox delivery nor the deterministic fact-ID inputs (`object_events` id/type/created_at + `source_system`).
- Negative stability: `quiesce(50ms, postCount==1)` unchanged after the search (search produces no events, no POSTs).

### REQ-4 — B3-5: `TestInsertEventEmissionSitesGrepConsistencyGate`

Source-scan gate (repo root = `filepath.Join("..","..")`, the `readyz_drill_test.go:338` idiom; `filepath.WalkDir` + `os.ReadFile`, stdlib-only):

- **Call-site allowlist:** across all `*.go` under the repo root (skip `docs/auto/`, `vendor/`), every production `.InsertEvent(` occurrence must be in exactly one of: `internal/events/bus.go` (bus `Publish`, `:84` — the sole emitter) or `internal/auditgovernance/repository.go` (wrapper pass-through, `:41`). Any other production call site (new emitter, handler writing events directly) fails with the offending file:line list. Test files are exempt (raw `sqlStore` access in tests is legitimate); the gate enforces the production emission paths.
- **Definition pin:** `internal/repository/sql_events.go` contains exactly one `func (s *sqlStore) InsertEvent(` (definition, not a call site — drift of the raw implementation's location fails loudly).
- **Assembly-ordering pin (`main.go:82,212`):** read `cmd/server/main.go`, split on `^func ` boundaries; assert exactly **two** `auditgovernance.WrapRepository(repo, auditRuntime)` occurrences, one inside `run()` and one inside `runMCP()`; in `run()`, a `bus.WithRepository(repo)` statement appears **after** the wrap within the same function (`:82→:83`); in `runMCP()`, `events.NewWithBuffer(repo` appears after the wrap (`:212→:215`). This proves every emission site's repository value is the governance-wrapped one in both assemblies — a reordering that put bus construction before the wrap fails CI.

---

## 4. Decisions & non-goals

- **D1 — `newGovernanceE2E` and the M1-M6 matrix stay untouched; the AI wiring is an additive helper** (`newGovernanceE2EWithAI`) in a new file. The matrix tests keep their fast literal harness; only the two new scenarios and the gate live in the new file.
- **D2 — The indexer and search are built on the wrapped repository** (`wrepo`) — the same object the bus holds — mirroring production, where `buildIndexer` receives the repo variable *after* `WrapRepository` (main.go:82-84 → ai.go:105). The wrapper's pass-through of `NextUnconsumedEvents`/`MarkEventConsumed`/`RecordUsage` is documented in the test comment, not asserted (interface behavior, not a pin).
- **D3 — Deterministic phases, no wall-clock races:** indexer running before PUT (production order); relay unstarted until the pending-state assertions complete (B1 snapshot idiom); all waits are positive state waits (`waitForRow` 10 s), all negatives are `quiesce`. The concurrency is real (indexer `handle` runs during Phase C's claim/deliver window) but no assertion depends on race timing.
- **D4 — Harness stays literal-config:** `AI_INDEX_ENABLED` is not driven through `config.Load()` here — the production chain is the sibling spec's territory (`cmd-server-audit-governance-production-assembly-e2e-v1.spec.md`). This direction wires the components `config.AI.Enabled` gates (`build.go:144`), with the mapping documented in REQ-1.
- **Non-goals:** `ai_usage` dedupe/idempotency and usage-failure metrics (sibling direction: "Give ai_usage rows deterministic identity…"); Agent usage recording (`agent.go`); BM25/hybrid/lexical wiring (vector-only, default `repoVectorIndex`); pgvector/Qdrant; any production code, migration, config surface, or `go.mod` change.

---

## 5. Acceptance criteria (preserved from the direction, made testable)

**T-3 (matrix e2e with indexer enabled) —** *"the bound-tenant first PUT still yields exactly one outbox row + one relay POST and claim/lag predicates (`delivered_at_ns=0 AND failed_at_ns=0`, `audit_governance_claim.go:78,218`) are unaffected by concurrent `MarkEventConsumed`."*
*Testable:* `TestGovernanceE2EAIWiredIndexerConsumption` (REQ-2) — indexer (`ai.NewIndexer` on the wrapped repo, live `bus.Subscribe()` sub) running before the PUT; `COUNT(*)==1` outbox rows at pending, post-consumption, and terminal states; `consumed_at` set and ≥1 `chunks` row while the row is still pending; lag query sees the pending row and excludes the delivered row; terminal `deliveredAtNS>0 && failedAtNS==0 && attempts==1`; exactly one POST (`quiesce`), one token call, `row.id == wantFactID(...)`.

**T-4 (wantFactID re-pinned with a live search) —** *"the existing `wantFactID` recomputation (`governance_e2e_test.go`) re-pinned with a live `search.Query` executed between PUT and relay delivery."*
*Testable:* `TestGovernanceE2EAIWiredSearchUsageRePin` (REQ-3) — `search.Query` (vector mode, `HashEmbedder`) after indexer consumption and before `startRelay`: ≥1 hit containing `obj.ID`, exactly one `ai_usage` row for `(acme, e2e:search, "x", e2e-usage-1)` with `object_ids` containing the object; delivery afterwards still exactly-once and `row.id == wantFactID(...)`.

**B3-5 (grep/CI consistency) —** *"a grep/CI consistency check proving all emission sites route through the governance wrapper."*
*Testable:* `TestInsertEventEmissionSitesGrepConsistencyGate` (REQ-4) — production `.InsertEvent(` call sites ⊆ {`internal/events/bus.go`, `internal/auditgovernance/repository.go`}; exactly one `sqlStore.InsertEvent` definition; `main.go` has exactly two `WrapRepository` sites, each followed (same function) by the bus construction/repoint — `run()` `:82→:83`, `runMCP()` `:212→:215`.

---

## 6. Risks

- **Timing flake** — identical envelope to the passing matrix e2e (5 ms poll, `waitForRow` 10 s, `quiesce` for negatives); the indexer path is deterministic via the live sub (broadcast after `Publish`; `Run` selects on `sub` after a no-op backlog drain). The 5 s `pollEvery` never gates an assertion.
- **`search.Query` determinism** — `HashEmbedder(8)` produces identical vectors for query and the single 1-byte chunk (same 5-rune padded shingle), so the hit assertion cannot flake; `matchesEmbedModel` matches (`hash-8` on both sides).
- **Grep-gate brittleness** — the allowlist is exact file+line; a legitimate future emission site requires a deliberate spec update (that is the gate's purpose). `docs/auto/` and `vendor/` are excluded so regenerated analysis artifacts cannot trip the literal scan; the `../../` root idiom is the established `readyz_drill_test.go` precedent.
- **File-size gate** — ≤ 500 lines: the three tests share `newGovernanceE2EWithAI` and all existing helpers; budget ≈ 300 lines. `make check` (gofmt/build/vet/test, SQLite+local FS, zero network beyond `httptest`) applies unchanged; `wantFactID`/`outboxRow`/`eventRowID` reuse keeps the I1 `?`-placeholder rule.

*Verification basis: all citations re-checked on this checkout (working tree as read during this spec's production; line numbers reflect the current tree).*
