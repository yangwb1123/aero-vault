I see you've shared `expansion-v119-strategic-extension-directions.md` — the file already exists at `/home/u1/aero-vault/docs/requirements/`. This is a well-structured distillation from the broader analysis series (v1–v117+) into 4 concrete, high-leverage directions.

The document is already comprehensive. Here's what I observe and what might be useful next steps:

---

## Document Assessment

The 4 directions are well-chosen. Each has:
- A clear **current-state vs gap** analysis backed by actual code paths
- **Building blocks already exist** (not greenfield)
- **Implementation sketch** that respects the existing architecture (I1–I6 constraints)
- **Cross-cutting concerns** for performance, security, observability

The strongest alignment with existing assets:

| Direction | Existing Foundation Depth | Risk |
|-----------|-------------------------|------|
| **1. Cold Tiering** | Very strong — `storage_class`, lifecycle engine, event bus all ready | GLACIER restore semantics on local FS need definition |
| **2. Object Lock + SSE-C** | Strong for lock (WORM path exists), moderate for SSE-C (envelope pattern exists) | Schema migration needed for `retention_mode`; SSE-C touches every storage backend |
| **3. Event Notifications** | Strong — schema stores ARNs, webhook retry infrastructure; just need delivery plugins | AWS SDK dependency; self-hosted fallback needed |
| **4. AI-Native Metadata** | Strongest differentiator — indexer pipeline, LLM interface, metadata system all ready | LLM cost/latency at ingest; separate enrichment queue is essential |

---

## Suggested Next Steps

Since the document explicitly says *"Each direction should be broken into a separate ADR with detailed design decisions, migration scripts, and test plans before coding begins"*, here are the concrete options:

### Option A: Pick one direction and create an ADR
Which one do you want to move forward with? Based on the README's P1/P2 framework:
- **Direction 1** (Cold Tiering) — P1, medium effort, high cost impact
- **Direction 3** (Event Notifications) — P1, leverages existing schema most directly
- **Direction 4** (AI Metadata) — biggest product differentiator but P2-ish in complexity
- **Direction 2** (Object Lock SSE-C) — regulatory gate, but heavy schema + storage layer work

### Option B: Validate the codebase claims
I could trace through the actual code to verify the "Building Blocks" sections claim accuracy:
- Does `reconcile/lifecycle.go` actually have the right hook points for transitions?
- Does `NotificationRule` truly store ARNs that go unused?
- Is the `ai.Extractor` interface composable enough for enrichment injection?

### Option C: Create the ADR template + first ADR
I can draft a proper ADR following the project's conventions, with:
- Design decisions with alternatives considered
- Migration scripts (dual SQLite/Postgres)
- Test plan covering the `HARNESS.md` gate
- Metric definitions for the OTel instrumentation

---

What would you like to do with this document?
