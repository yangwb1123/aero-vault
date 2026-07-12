All five directions verified conclusively against the codebase. Here's my architectural assessment:

---

## Verification Results — 5/5 Confirmed

Your "silent configuration acceptance" framing is precisely correct. I traced every claimed code path:

### ✅ Direction 1: Notifications (Configuration Orphan #1)

```
migrations/0024_bucket_notifications.up.sql     → ALTER TABLE buckets ADD notification_rules TEXT  ✅
internal/service/file_features.go:261-276       → Get/Set/DeleteBucketNotifications               ✅
internal/api/s3compat/handler.go:809-841        → PUT/GET/DELETE ?notification                    ✅
Any worker reading NotificationRule             → ❌ grep returns zero hits across all internal/   ❌
```

The `buckets.notification_rules` column is written by `SetBucketNotifications` but **never read by any goroutine** — confirmed via `grep -rn "NotificationRule\|notification_rules\|notification.*worker\|notify.*worker" internal/`.

### ✅ Direction 2: Server Access Logging (Configuration Orphan #2)

```
migrations/0023_bucket_logging.up.sql            → ALTER TABLE buckets ADD logging_target/prefix  ✅
internal/service/file_features.go:243-258        → Get/Set/DeleteBucketLogging                   ✅
internal/api/s3compat/handler.go:722-767         → PUT/GET/DELETE ?logging                        ✅
Any AccessLogWriter goroutine                    → ❌ zero hits                                   ❌
```

Same pattern: `logging_target` and `logging_prefix` columns are written but never consumed.

### ✅ Direction 3: Multipart Concurrency (Data Integrity Risk)

I read the full `file_multipart.go` — the race condition is even more severe than described. In the **versioning path**:

```go
// saveMultipartObject calls InsertObjectVersion when versioning is enabled
func (s *sqlStore) InsertObjectVersion(ctx context.Context, obj Object) (Object, error) {
    tx, _ := s.db.BeginTx(ctx, nil)
    // Step 1: Soft-delete current active row
    _, err = tx.ExecContext(ctx, `UPDATE objects SET deleted_at=... 
        WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`)
    // Step 2: Insert new version row
    // ...
    tx.Commit()
}
```

**Two concurrent `CompleteMultipart` calls on the same upload:**
1. Both read the upload (status `in_progress` — no state machine exists)
2. Both call `s.store.CompleteMultipart` — storage backend merges parts twice
3. Both reach `saveMultipartObject` → `InsertObjectVersion`
4. Transaction A soft-deletes the old row, inserts new version `v1`
5. Transaction B soft-deletes A's `v1`, inserts new version `v2`
6. **Result**: B's version is the sole survivor in DB. A's merged blob is an orphan. A's client got a success response but the data is lost.

In the **non-versioning path**: `UpsertObject` uses `ON CONFLICT DO UPDATE` — both calls succeed, second overwrites first. Storage backend ran merge twice. Client A's success is a lie.

### ✅ Direction 4: DeleteBucket Blob Orphans (Storage Leak)

Confirmed: `sql_buckets.go:69-120` performs exactly 6 SQL DELETE operations (uploads, parts, chunks, objects, events, bucket row) and **zero** `s.store.Delete` calls. Every blob the bucket ever owned remains on disk/in S3 forever.

### ✅ Direction 5: Health API (Observability Gap)

Confirmed — `main.go:168-209`:
- `/healthz`: returns `{"ok":true}` — no DB check, no storage check
- `/readyz`: DB Ping + Storage Stat only — no worker health, no indexer state, no AI pipeline
- `/info`: hardcoded `"version":"0.1.0"` — not injected at build time

---

## Architectural Observations Beyond the Analysis

During verification I found two additional concerns worth noting:

### 1. The `CompleteMultipart` race is actually a **data corruption** bug (Priority: **P0**)

Your P1 rating is appropriate for engineering priority, but the data corruption potential in versioned buckets — where two concurrent completes can silently lose one user's data while returning 200 OK — arguably qualifies as P0 (customer-data-loss severity). I'd recommend moving it to **immediate fix**, before any new feature work.

### 2. Direction 4 has a secondary vector: **S3 AbortMultipart orphan parts**

When `AbortMultipart` is called, `file_multipart.go:215-227` calls `s.store.AbortMultipart` then `s.repo.DeleteUpload`. But if `DeleteUpload` fails (DB error), the storage backend parts are already gone from the repo's perspective. Conversely, if `AbortMultipart` fails on the storage side but DB cleanup succeeds, the orphan parts on the storage backend are invisible to Reconcile GC (which scans by storage keys matching DB records — orphan parts don't have DB records). This creates a **second class of invisible orphans** beyond bucket deletion.

---

## Implementation Effort Estimates (Refined)

Based on actual codebase structure, here are more precise estimates:

| Direction | Files to Create/Modify | Estimated LOC | Actual Criticality |
|-----------|----------------------|---------------|-------------------|
| **3. Multipart Concurrency** | `file_multipart.go` (~+80 lines), `repository/sql_uploads.go` (+new status column migration) | ~120 | **P0** (data corruption on versioned buckets) |
| **1. Notification Router** | `events/notify.go` (new) + `main.go` (~+15) | ~350 | P1 |
| **2. Access Log Writer** | `logging/writer.go` (new) + `main.go` (~+15) | ~400 | P1 |
| **4. BlobCleaner** | `reconcile/blobcleaner.go` (new) or extend `reconcile/job.go` | ~200 | P2 |
| **5. Health API** | `health/registry.go` (new) + `health/checkers.go` (new) + `main.go` (~+30) | ~350 | P2 |

---

## Recommended Sprint Order (Revised)

```
IMMEDIATE HOTFIX:
  Direction 3 — Add upload status state machine (in_progress → completing → completed)
                20 lines of Go + migration to add status column
                Prevent data corruption in versioned buckets

SPRINT N:
  Direction 1 — NotificationRouter: ~350 lines, highest user-facing impact
  Direction 3 — Complete: UploadPart TOCTOU + Part conflict docs + tests

SPRINT N+1:
  Direction 2 — AccessLogWriter: ~400 lines
  Direction 4 — BlobCleaner: ~200 lines

SPRINT N+2:
  Direction 5 — HealthRegistry + component reporters + /v1/admin/health
  Direction 4 — Optional: bucket recycle bin (soft-delete + restore)
```

Would you like me to begin implementing any of these directions? The **multipart concurrency fix (Direction 3)** would be the most impactful starting point — it addresses an active data corruption risk with minimal code change.
