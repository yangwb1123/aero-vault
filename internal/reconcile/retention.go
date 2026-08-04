package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ChunkCleaner is the minimal interface RetentionJob needs to clean up AI
// chunks when purging soft-deleted objects. Satisfied by *ai.Indexer.
type ChunkCleaner interface {
	DeleteObjectChunks(ctx context.Context, objectID int64) error
}

// RetentionJob permanently purges rows that have been soft-deleted for longer
// than a retention window, together with their backing blobs. Without it,
// soft-deleted rows (and their blobs) accumulate forever. It mirrors
// LifecycleJob: a ticker loop gated, when configured, behind a cluster lease so
// the destructive sweep runs on only one replica at a time.
const leaseRetentionGC = "retention-gc"

type RetentionJob struct {
	repo         repository.Repository
	store        storage.Storage
	interval     time.Duration
	retention    time.Duration
	idemTTL      time.Duration
	singleton    *cluster.Singleton
	chunkCleaner ChunkCleaner
	logger       *slog.Logger
}

func NewRetention(repo repository.Repository, store storage.Storage, interval time.Duration, retention time.Duration, logger *slog.Logger) *RetentionJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &RetentionJob{repo: repo, store: store, interval: interval, retention: retention, singleton: cluster.NewSingleton(repo, leaseRetentionGC, logger), logger: logger}
}

// WithIdempotencyTTL enables GC of idempotency_keys older than ttl, so the
// dedupe table doesn't grow without bound. Runs on the same sweep cadence.
func (r *RetentionJob) WithIdempotencyTTL(ttl time.Duration) *RetentionJob {
	r.idemTTL = ttl
	return r
}

// WithClusterSingleton makes the (destructive) retention GC run on only one
// replica at a time, gated by a repository lease held by `holder`.
func (r *RetentionJob) WithClusterSingleton(holder string) *RetentionJob {
	r.singleton.Enable(holder)
	return r
}

// WithChunkCleaner attaches a ChunkCleaner so that AI chunks are removed when
// soft-deleted objects are permanently purged. Without this, chunks orphaned
// by retention GC remain searchable indefinitely.
func (r *RetentionJob) WithChunkCleaner(cc ChunkCleaner) *RetentionJob {
	r.chunkCleaner = cc
	return r
}

func (r *RetentionJob) Run(ctx context.Context) {
	if r.interval <= 0 || (r.retention <= 0 && r.idemTTL <= 0) {
		return
	}
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep, gated by the cluster singleton when enabled (lease
// TTL 2× interval; the holder renews each round).
func (r *RetentionJob) maybeSweep(ctx context.Context) {
	r.singleton.Guard(ctx, 2*r.interval, r.sweep)
}

func (r *RetentionJob) sweep(ctx context.Context) {
	if r.retention > 0 {
		r.purgeSoftDeleted(ctx)
	}
	if r.idemTTL > 0 {
		r.purgeIdempotency(ctx)
	}
}

// purgeIdempotency deletes idempotency keys older than the configured TTL so
// the dedupe table stays bounded.
func (r *RetentionJob) purgeIdempotency(ctx context.Context) {
	before := time.Now().Add(-r.idemTTL).UTC().Format(time.RFC3339Nano)
	n, err := r.repo.DeleteIdempotencyKeysBefore(ctx, before)
	if err != nil {
		r.logger.Warn("retention idempotency gc", "err", err)
		return
	}
	if n > 0 {
		r.logger.Info("idempotency gc", "purged", n)
	}
}

func (r *RetentionJob) purgeSoftDeleted(ctx context.Context) {
	before := time.Now().Add(-r.retention).UTC().Format(time.RFC3339Nano)
	objs, err := r.repo.ListSoftDeletedBefore(ctx, before, 200)
	if err != nil {
		r.logger.Warn("retention list soft-deleted", "err", err)
		return
	}
	if len(objs) == 0 {
		return
	}
	purged := 0
	for _, obj := range objs {
		if r.purgeOneSoftDeleted(ctx, obj) {
			purged++
		}
	}
	if purged > 0 {
		r.logger.Info("retention sweep", "chunk_cleanup_enabled", r.chunkCleaner != nil, "purged", purged)
	}
}

// purgeOneSoftDeleted attempts to permanently remove one soft-deleted object.
// Returns true when the object was purged. Does NOT fail on chunk cleanup or
// storage delete errors (best-effort).
func (r *RetentionJob) purgeOneSoftDeleted(ctx context.Context, obj repository.Object) bool {
	protected, err := objectKeyDeletionProtected(ctx, r.repo, obj)
	if err != nil {
		r.logger.Warn("retention protection check", "key", obj.Key, "err", err)
		return false
	}
	if protected {
		return false
	}
	if err := hardDeleteKey(ctx, r.repo, r.store, r.chunkCleaner, obj, r.logger); err != nil {
		r.logger.Warn("retention hard delete", "key", obj.Key, "err", err)
		return false
	}
	return true
}
