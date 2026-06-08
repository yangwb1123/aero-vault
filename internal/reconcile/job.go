// Package reconcile is the background sweeper that compares the metadata
// repository against the storage backend.
//
// Two divergences are detected, in both directions:
//
//   - DB row exists but storage object is missing → "orphan_row" (likely caused
//     by an out-of-band storage deletion or a failed cleanup). The row is
//     soft-deleted so it leaves the active set; an admin can still restore it.
//   - Storage object exists but no DB row references it → "orphan_blob" (e.g. a
//     PUT whose blob landed but whose DB write failed, or a hard delete whose
//     row was removed before its blob). Cleanup is opt-in via deleteOrphanBlobs
//     (env RECONCILE_DELETE_ORPHAN_BLOBS=true) and only acts on blobs older than
//     a grace period, so an in-flight upload that has not yet committed its DB
//     row is never deleted.
//
// The job is opt-in via RECONCILE_INTERVAL_MINUTES > 0.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// leaseReconcileSweep is the distributed-lease name guarding the sweep so that,
// across replicas, only one instance runs it per round.
const leaseReconcileSweep = "reconcile-sweep"

type Job struct {
	repo              repository.Repository
	store             storage.Storage
	interval          time.Duration
	deleteOrphanBlobs bool
	gracePeriod       time.Duration
	tenants           []string // tenants to scan; empty means scan default only
	singleton         *cluster.Singleton
	logger            *slog.Logger
}

// WithClusterSingleton makes the sweep run on only one replica at a time, gated
// by a repository lease held by `holder` (a per-instance id). Without it, every
// replica sweeps independently (fine for a single instance).
func (j *Job) WithClusterSingleton(holder string) *Job {
	j.singleton.Enable(holder)
	return j
}

func New(repo repository.Repository, store storage.Storage, interval time.Duration, deleteOrphanBlobs bool, gracePeriod time.Duration, tenants []string, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	if len(tenants) == 0 {
		tenants = []string{"default"}
	}
	return &Job{repo: repo, store: store, interval: interval, deleteOrphanBlobs: deleteOrphanBlobs, gracePeriod: gracePeriod, tenants: tenants, singleton: cluster.NewSingleton(repo, leaseReconcileSweep, logger), logger: logger}
}

// Run blocks until ctx is canceled. It runs an initial sweep immediately, then
// every j.interval. Intentionally cheap (single-threaded, batched).
func (j *Job) Run(ctx context.Context) {
	if j.interval <= 0 {
		return
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	j.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep, gated by the cluster singleton when enabled. The
// lease TTL is 2× the interval so the holder keeps renewing each round; if it
// dies, the lease frees after ~2 intervals and another replica takes over.
func (j *Job) maybeSweep(ctx context.Context) {
	j.singleton.Guard(ctx, 2*j.interval, j.sweep)
}

// sweep reconciles every configured tenant in both directions: orphan rows
// (DB row without a blob) and orphan blobs (blob without a DB row).
func (j *Job) sweep(ctx context.Context) {
	start := time.Now()
	var scanned, orphanRows, orphanBlobs, deletedBlobs int
	for _, t := range j.tenants {
		sc, or := j.sweepOrphanRows(ctx, t)
		ob, db := j.sweepOrphanBlobs(ctx, t)
		scanned += sc
		orphanRows += or
		orphanBlobs += ob
		deletedBlobs += db
	}
	telemetry.RecordReconcileBlobs(ctx, orphanBlobs, deletedBlobs)
	j.logger.Info("reconcile sweep done",
		"scanned", scanned,
		"orphan_rows", orphanRows,
		"orphan_blobs", orphanBlobs,
		"orphan_blobs_deleted", deletedBlobs,
		"delete_enabled", j.deleteOrphanBlobs,
		"duration_ms", time.Since(start).Milliseconds())
}

// sweepOrphanRows soft-deletes rows whose backing blob is missing, across every
// bucket the tenant owns. Returns (rows scanned, orphan rows found).
func (j *Job) sweepOrphanRows(ctx context.Context, tenant string) (scanned, orphans int) {
	buckets, err := j.repo.ListBuckets(ctx, tenant)
	if err != nil {
		j.logger.Warn("reconcile: list buckets", "tenant", tenant, "err", err)
		return 0, 0
	}
	for _, bucket := range buckets {
		var marker string
		for {
			page, err := j.repo.ListObjects(ctx, tenant, bucket, "", marker, 200)
			if err != nil {
				j.logger.Warn("reconcile: list objects", "tenant", tenant, "bucket", bucket, "err", err)
				break
			}
			for _, obj := range page.Objects {
				scanned++
				_, err := j.store.Stat(ctx, obj.StorageKey)
				if errors.Is(err, storage.ErrNotFound) {
					orphans++
					j.logger.Warn("reconcile: storage missing for DB row",
						"tenant", tenant, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
					// Soft-delete to take it out of the active set; admin can restore.
					_ = j.repo.SoftDeleteObject(ctx, tenant, obj.Bucket, obj.Key)
				}
			}
			if !page.HasMore {
				break
			}
			marker = page.NextMarker
		}
	}
	return scanned, orphans
}

// sweepOrphanBlobs finds blobs in storage with no DB row and, when enabled,
// deletes those older than the grace period. Returns (orphan blobs found,
// blobs deleted). With deleteOrphanBlobs=false it only detects and logs — the
// safe default. The blob walk is scoped to the tenant's "<tenant>/" prefix,
// which also excludes backend-internal paths such as ".multipart/".
func (j *Job) sweepOrphanBlobs(ctx context.Context, tenant string) (orphans, deleted int) {
	prefix := tenant + "/"
	var marker string
	for {
		page, err := j.store.List(ctx, prefix, marker, 200)
		if err != nil {
			j.logger.Warn("reconcile: list blobs", "tenant", tenant, "err", err)
			break
		}
		for _, oi := range page.Objects {
			referenced, err := j.repo.StorageKeyReferenced(ctx, oi.Key)
			if err != nil {
				j.logger.Warn("reconcile: storage-key lookup", "tenant", tenant, "storage_key", oi.Key, "err", err)
				continue
			}
			if referenced {
				continue // some object version (live or soft-deleted) still pins this blob
			}
			// Unreferenced blob. The write path stores the blob before its DB
			// row, so a freshly written blob may simply be mid-commit; only act
			// once it is older than the grace period.
			if j.gracePeriod > 0 && time.Since(oi.LastModified) <= j.gracePeriod {
				continue
			}
			orphans++
			if !j.deleteOrphanBlobs {
				j.logger.Warn("reconcile: orphan blob (cleanup disabled)",
					"tenant", tenant, "storage_key", oi.Key, "last_modified", oi.LastModified)
				continue
			}
			if err := j.store.Delete(ctx, oi.Key); err != nil {
				j.logger.Warn("reconcile: orphan blob delete", "tenant", tenant, "storage_key", oi.Key, "err", err)
				continue
			}
			deleted++
			j.logger.Info("reconcile: orphan blob deleted", "tenant", tenant, "storage_key", oi.Key)
		}
		if !page.HasMore {
			break
		}
		marker = page.NextMarker
	}
	return orphans, deleted
}
