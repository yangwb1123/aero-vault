All 5 directions are **fully verified** against HEAD `cae49d9`. Here is the evidence summary:

---

## Verification Results

### ✅ Direction 1: S3 Event Notification — Delivery Engine Missing

| Claim | Code Evidence |
|-------|---------------|
| XML parsed | `xml.go:395` — `notificationConfiguration`, `topicConfig`, `queueConfig`, `lambdaConfig` |
| S3 routed | `handler.go:296` — `q.Has("notification")` → `dispatchBucketNotifications` |
| Service passthrough | `file_features.go:261-273` — `GetBucketNotifications`/`SetBucketNotifications` call repo |
| Repository stores JSON | `sql_buckets.go:381-417` — `notification_rules TEXT` column, JSON marshaled |
| **No delivery engine** | **No component subscribes to events and matches against notification rules**. The only event consumer is `startWebhook` at `main.go:709` which fires ALL events to a single configured URL. `events.NewWebhook` has no per-bucket rule matching logic. |

### ✅ Direction 2: Storage Class Lifecycle — No Transitions or Glacier Restore

| Claim | Code Evidence |
|-------|---------------|
| `StorageClass` in `Object` | `repository.go:34` — `StorageClass string` |
| Write-time acceptance | `file_crud.go:buildPutObject` reads `x-amz-storage-class` |
| Lifecycle config | `BucketConfig` has `ExpireAfterDays` + `ExpireAction` only — **no transition rules** |
| Lifecycle sweep | `reconcile/lifecycle.go:68-96` — only does `soft_delete`/`hard_delete`. No transition logic. |
| `RestoreObject` = soft-delete recovery | `sql_objects.go:288` — `UPDATE objects SET deleted_at=NULL` — **not glacier restore** |

### ✅ Direction 3: Stale Multipart Upload — No Auto-Cleanup

| Claim | Code Evidence |
|-------|---------------|
| Table exists | `migrations/sqlite/0001_init.up.sql` — `multipart_uploads` + `multipart_parts` |
| Manual abort exists | `extra.go:222` — `abortMultipartUpload` → `svc.AbortMultipart` |
| **No auto-cleanup** | **`reconcile/retention.go` and `reconcile/lifecycle.go` reference `objects` table only. No `ListUploads` call with `created_at < NOW()-TTL` exists anywhere.** |

### ✅ Direction 4: Multipart Min Part Size — No Enforcement

| Claim | Code Evidence |
|-------|---------------|
| `uploadPart` handler | `extra.go:161` — validates `partNumber < 1 \|\| partNumber > 10000` but **NOT** `ContentLength` |
| Service `UploadPart` | `file_multipart.go:60` — direct pass-through to `s.store.UploadPart`, **no size check** |
| No `MIN_PART_SIZE` | `grep` across entire codebase — no `5*1024*1024` or `minPartSize` in service/s3compat. Only in `snapshot_test.go:40` for formatting. |

### ✅ Direction 5: Object Lock Mode — Dropped at Every Level

| Claim | Code Evidence |
|-------|---------------|
| `putBucketObjectLock` drops `Mode` | `bucketconfig.go:151-155` — only reads `in.Rule.DefaultRetention.Days`, **Mode discarded** |
| `getBucketObjectLock` hardcodes `GOVERNANCE` | `bucketconfig.go:138` — `Mode: "GOVERNANCE"` hardcoded |
| `SetBucketObjectLock` accepts only seconds | `sql_buckets.go:237` — `UPDATE buckets SET object_lock_seconds=$1` — **no mode column** |
| No `object_lock_mode` column | Schema `0001_init.up.sql`: no such column. Migration `0024` is for notifications only. |
| `hardDeleteObject` checks only time | `file_crud.go:166` — `obj.LockedUntil != nil && obj.LockedUntil.After(time.Now())` — **no mode-based bypass** |

---

## Code References Summary

```
# Notification delivery gap
internal/api/s3compat/xml.go:395        ← XML types ✅
internal/api/s3compat/handler.go:767-810 ← route dispatch ✅
internal/service/file_features.go:261-273 ← service passthrough ✅
internal/repository/sql_buckets.go:381-417 ← repo persist ✅
internal/events/bus.go                   ← event bus (reusable infrastructure) ℹ️
internal/events/webhook.go               ← existing webhook delivery (reusable) ℹ️
cmd/server/main.go:709                   ← webhook startup (single URL, no rule matching)

# Lifecycle transition gap
internal/repository/repository.go:30     ← BucketConfig (only ExpireAfterDays + ExpireAction)
internal/reconcile/lifecycle.go:68-96    ← only deletes, no transitions
internal/repository/sql_objects.go:288   ← RestoreObject = soft-delete recovery

# Stale multipart cleanup gap
internal/repository/sql_uploads.go:48    ← ListUploads exists but unused in reconcile
internal/api/s3compat/extra.go:212       ← CreateMultipartUpload
internal/reconcile/retention.go          ← no multipart cleanup

# Min part size gap
internal/api/s3compat/extra.go:161       ← uploadPart (no size check)
internal/service/file_multipart.go:60    ← UploadPart (no size check)

# Object lock mode gap
internal/api/s3compat/bucketconfig.go:138-155 ← get/put drop mode
internal/repository/sql_buckets.go:237    ← SetBucketObjectLock (only seconds)
internal/repository/migrations/sqlite/   ← no object_lock_mode migration
internal/service/file_crud.go:166        ← hardDeleteObject (time-only check)
```

All five directions are accurately diagnosed. The document's claims about protocol-layer implementation vs execution-layer gaps are correct.

Would you like me to implement any of these five directions? I can start with a specific one or prioritize based on your needs.
