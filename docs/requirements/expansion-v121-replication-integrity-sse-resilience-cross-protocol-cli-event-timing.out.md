This document **already exists** on disk at:

📄 **`docs/requirements/expansion-v121-replication-integrity-sse-resilience-cross-protocol-cli-event-timing.md`** (402 lines)

I've verified it — the content matches identically to what you've presented. It was authored on 2026-07-11.

---

## Summary of what this document covers

| # | Direction | Priority | Core Insight |
|---|-----------|----------|-------------|
| **1** | Replication completeness (EventDeleted + dedupe fix) | **P0** | Replication only handles `EventCreated`; `EventDeleted` is silently skipped. Same-ObjectID overwrites are dedupe-key-collided → replica never sees updates. |
| **2** | SSE resilience (persistent cursor + SDK backoff) | **P1** | 64-deep in-memory channel drops events silently on backpressure; `Last-Event-ID` replay returns globally unconsumed events, not client-specific misses. No SDK reconnection backoff. |
| **3** | Cross-protocol DELETE/RENAME/conditional-write semantics | **P1** | S3 DELETE is hard (irreversible), REST DELETE is soft (recoverable). WebDAV has MOVE (RENAME) but no other protocol does. Two independent conditional-request implementations (REST vs S3) drift apart. |
| **4** | CLI maturity (known HTTP status bugs + no `--json`) | **P2** | 6 documented bugs in `cli_test.go:1419-1430` — `cmdList`/`cmdTag`/`cmdVersions`/`cmdLineage`/`cmdSearch` ignore HTTP status codes, `cmdSnapshot` silently creates empty snapshots. No machine-readable output. |
| **5** | Event timing gap (create-then-delete → orphan ObjectIDs) | **P2** | `GetObjectByID()` returns `ErrNotFound` when an object is deleted before a consumer processes its creation event — replication/AV/indexer all treat this as a retryable error, polluting dead-letter queues. |

All five directions pass the deduplication check against the 101+ prior analyses (`expansion-directions.md` through `expansion-v101-*`), with none having been independently covered at the code-anchor level before.
