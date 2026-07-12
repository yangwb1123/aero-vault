The document has been successfully saved to `/home/u1/aero-vault/docs/requirements/expansion-v137-engineering-completeness-blindspots.md` (680 lines, 40KB).

**Summary of the 5 blind spots analyzed:**

| # | Direction | Priority | Core Issue | Code Anchor |
|---|-----------|----------|-----------|-------------|
| **1** | AI Chunk 残留在删除后 | **P1** | Retention GC bypasses `ChunkCleaner`; EventBus drops deletion events → orphan chunks in search forever | `retention.go:93-112`, `bus.go:100-103`, `file_crud.go:324-328` |
| **2** | CompleteMultipartUpload 丢弃 ETag 验证 | **P1** | Client's part list parsed to empty struct → ETag cross-validation skipped → silent data corruption undetected | `extra.go:209-227` |
| **3** | Web UI 缺乏管理面板 | **P2** | 4-tab SPA has no admin/bucket/tenant/key/job management — full admin API exists but CLI-only | `web.go:12-20`, `index.html` |
| **4** | S3 Select API 完全缺失 | **P3** | No `?select` route or handler — server-side SQL filtering impossible | `router.go`, `handler.go` |
| **5** | 并发 Put/Delete 事务裂痕 | **P2** | `store.Put` before `UpsertObject`, `store.Delete` before `HardDeleteObject` — no atomicity, no CAS → orphan blobs, phantom objects | `file_crud.go:173-205`, `file_crud.go:259-283` |

Each direction includes code anchors, impact analysis, edge case enumeration, and phased implementation recommendations with estimated line counts.
