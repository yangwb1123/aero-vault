package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// LifecycleJob applies per-bucket retention policy (ExpireAfterDays). Runs on
// the same cadence as Reconcile. Soft-delete is the default action; hard
// delete is enabled when expire_action="hard_delete" on the bucket.
const leaseLifecycleSweep = "lifecycle-sweep"

type LifecycleJob struct {
	repo      repository.Repository
	store     storage.Storage
	interval  time.Duration
	singleton *cluster.Singleton
	logger    *slog.Logger
}

func NewLifecycle(repo repository.Repository, store storage.Storage, interval time.Duration, logger *slog.Logger) *LifecycleJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleJob{repo: repo, store: store, interval: interval, singleton: cluster.NewSingleton(repo, leaseLifecycleSweep, logger), logger: logger}
}

// WithClusterSingleton makes the (destructive) lifecycle sweep run on only one
// replica at a time, gated by a repository lease held by `holder`.
func (l *LifecycleJob) WithClusterSingleton(holder string) *LifecycleJob {
	l.singleton.Enable(holder)
	return l
}

func (l *LifecycleJob) Run(ctx context.Context) {
	if l.interval <= 0 {
		return
	}
	t := time.NewTicker(l.interval)
	defer t.Stop()
	l.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep, gated by the cluster singleton when enabled (lease
// TTL 2× interval; the holder renews each round).
func (l *LifecycleJob) maybeSweep(ctx context.Context) {
	l.singleton.Guard(ctx, 2*l.interval, l.sweep)
}

func (l *LifecycleJob) sweep(ctx context.Context) {
	soft, hard := l.sweepExpired(ctx)
	noncurrent := l.sweepNonCurrentVersions(ctx)
	transitions := l.sweepTransitions(ctx)
	if soft > 0 || hard > 0 || noncurrent > 0 || transitions > 0 {
		l.logger.Info("lifecycle sweep", "soft_deleted", soft, "hard_deleted", hard,
			"noncurrent_versions_purged", noncurrent, "transitioned", transitions)
	}
}

func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
	expired, err := l.repo.ListExpired(ctx, 200)
	if err != nil {
		l.logger.Warn("lifecycle list expired", "err", err)
		return
	}
	for _, obj := range expired {
		action := obj.Metadata["__expire_action"]
		if l.handleExpiredObject(ctx, obj, action) {
			if action == "hard_delete" {
				hard++
			} else {
				soft++
			}
		}
	}
	return
}

func (l *LifecycleJob) handleExpiredObject(ctx context.Context, obj repository.Object, action string) bool {
	if action == "hard_delete" {
		if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
			return false
		}
		if err := l.store.Delete(ctx, obj.StorageKey); err != nil {
			l.logger.Warn("lifecycle storage delete", "key", obj.Key, "err", err)
			return false
		}
		return l.repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key) == nil
	}
	return l.repo.SoftDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key) == nil
}

// sweepNonCurrentVersions permanently removes version tombstones (old versions
// from versioning-enabled buckets) whose bucket's noncurrent_days window has
// passed. Returns the count of purged rows.
func (l *LifecycleJob) sweepNonCurrentVersions(ctx context.Context) int {
	versions, err := l.repo.ListExpiredNonCurrentVersions(ctx, 200)
	if err != nil {
		l.logger.Warn("lifecycle list non-current versions", "err", err)
		return 0
	}
	purged := 0
	for _, v := range versions {
		// Double-check that this is not protected by an object lock.
		if v.LockedUntil != nil && v.LockedUntil.After(time.Now()) {
			continue
		}
		// Delete the backing blob.
		if err := l.store.Delete(ctx, v.StorageKey); err != nil {
			l.logger.Warn("lifecycle non-current storage delete", "key", v.Key, "version", v.VersionID, "err", err)
			continue
		}
		// Hard delete the row. The repository method checks for legal holds
		// (the SQL query already excluded legal holds, but this is a safety net).
		if err := l.repo.HardDeleteObjectByID(ctx, v.ID); err != nil {
			if errors.Is(err, repository.ErrLegalHoldActive) {
				l.logger.Warn("lifecycle non-current version skipped: legal hold", "key", v.Key, "version", v.VersionID)
			} else {
				l.logger.Warn("lifecycle non-current version hard delete", "key", v.Key, "version", v.VersionID, "err", err)
			}
			continue
		}
		purged++
	}
	if purged > 0 {
		l.logger.Info("lifecycle non-current versions purged", "count", purged)
	}
	return purged
}

// sweepTransitions finds objects whose bucket has transition rules and whose age
// qualifies for a storage-class change, then updates the DB record. Returns the
// number of objects transitioned.
func (l *LifecycleJob) sweepTransitions(ctx context.Context) int {
	objs, err := l.repo.ListTransitionable(ctx, 200)
	if err != nil {
		l.logger.Warn("lifecycle list transitionable", "err", err)
		return 0
	}
	transitioned := 0
	for _, obj := range objs {
		targetClass, ok := obj.Metadata["__transition_to"]
		if !ok || targetClass == "" || targetClass == obj.StorageClass {
			continue
		}
		if err := l.repo.UpdateObjectStorageClass(ctx, obj.TenantID, obj.Bucket, obj.Key, targetClass); err != nil {
			l.logger.Warn("lifecycle transition update", "key", obj.Key,
				"from", obj.StorageClass, "to", targetClass, "err", err)
			continue
		}
		transitioned++
	}
	if transitioned > 0 {
		l.logger.Info("lifecycle transitions applied", "count", transitioned)
	}
	return transitioned
}
